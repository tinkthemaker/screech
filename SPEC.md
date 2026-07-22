# SCREECH — DESIGN SPEC v2

Internet radio with a discovery algorithm and almost no controls. Go, terminal-first,
self-hosted, single binary, no accounts, no cloud. Demoscene austerity: one accent
color, four tones, standard Unicode only. This document supersedes v1; the deltas
from review are folded in.

## What changed from v1 (summary)

- Skip and tune merged into one control: **next**. Dwell time carries the signal.
- Star (`f`) deleted. Station list, search, heard/loved views demoted out of v1 core.
- Daemon design: **stream relay** instead of metadata-only ICY connections.
- Fingerprints: IDF-weighted artist overlap, not raw Jaccard. Parser hygiene specified.
- Bandit rewards bounded; decay is lazy (on read), no background jobs.
- Runtime ad detection expectations lowered; auto-hop is opt-in and off by default.
- Build order flipped: playback + logging vertical slice first, algorithm second.
- Visual language specified (see UI section). It's a design deliverable, not decoration.

## Hard scope cuts

No playlists. No accounts. No recording. No podcasts. No config UI (one TOML file).
No album art / sixel. No Nerd Fonts (box-drawing, blocks, braille — all standard
Unicode). v1 additionally cuts: visible station browser, search, manual station
starring, heard/loved views (they return as demoted toggles post-slice).

## Interaction model (root principle)

The algorithm learns from exactly two inputs plus a clock, so the UI needs exactly
two controls:

- **next** (`space` or `n`) — pressed fast (<90s) it's a skip (negative). Pressed
  after a long listen it's a variety request (listen credit already banked, no penalty).
- **love** (`l`) — strong positive on the track's artist + station + tags.
  Toggle: loving the same track (or, trackless, the same station) a second
  time returns the exact boosts — a bounded dose, not a permanent scar.
- **dead streams** (`TuneDead`) — a stream that fails to lock closes its
  listen with no skip semantics and no bandit reward; fail_count is the
  whole penalty. Network trouble is not dislike.

Shipped in v0.2, still within the austerity budget:

- **presets** (`f` save/unsave, digits 1-9 recall): deterministic *recall*, the one
  job the bandit can't do. Deliberately zero effect on the taste model — love
  teaches, presets remember. Saved stations render as ticks on the band line.
  Nine slots stay nine: presets are muscle memory, not a list. Everything past
  that lives in the library's saved-stations view (below).
- **seeding** (`/`): type a genre or artist. Genres resolve strongest: a tag
  in the local cache tunes instantly, and a tag the cache doesn't carry
  (e.g. "corridos" on a thin global-pop cache) is searched against the
  directory, upserted, and tuned. Artists are honest-best-effort: the name
  is pseudo-loved so fingerprints catch it later, stations *named* for the
  artist are tuned when they exist, and when they don't (the common case) a
  small curated artist→genre map finds a station in the family that plays
  their music. The reason line always says which path fired —
  `seeded: corridos` vs `seeded genre: corridos` — so a genre guess is
  never silent. A genre seed also echoes through the next four picks. First
  run plays from embedded stations while directory sync runs in the
  background; every later launch keeps zero-press resume.
- **volume** (`v`, then arrows or `-`/`+`): application-only mpv volume,
  persisted in SQLite. It appears as a temporary instrument slider rather
  than occupying permanent space in the idle display.

`q` quits. Launch resumes the last station and plays immediately — zero-press startup,
like a physical radio. Everything else is automatic or read-only.

## Architecture

Four layers, buildable in vertical slices:

1. **Core package** — SQLite, scoring, fingerprints, ad-risk, radio-browser client.
   No UI knowledge. The portable heart for Path 2.
2. **Bubble Tea TUI** — talks to core directly at first, daemon API later.
3. **Daemon** — small JSON API (~8 endpoints) on the homelab. For browser clients it
   **relays the stream**: one upstream connection per tuned station, reads ICY titles
   from the bytes it's already forwarding, serves the audio same-origin over HTTPS.
   This replaces v1's metadata-only connections (ICY interleaves titles into audio,
   so "metadata-only" cost full stream bandwidth and doubled listener counts). The
   TUI needs no relay: mpv sees titles and the TUI reports them to the daemon.
4. **Phone web client** — Go stdlib + htmx, served by the daemon. `<audio>` element
   pointed at the daemon relay URL (same-origin, so no mixed-content blocking under
   Tailscale HTTPS). One big NEXT button, a small heart, now-playing text. Tailscale
   for off-LAN, zero exposed ports.

## Playback (TUI)

Shell out to `mpv --no-video --idle` over JSON IPC. mpv handles codecs, ICY,
reconnects, playlist-file URLs, buffering. Player is behind a Go interface so a
pure-Go backend can swap in for Path 2 (enables real FFT).

IPC transport differs by OS: unix socket on linux/mac, **named pipe on Windows**
(`\\.\pipe\…`, via go-winio). The thin client (~150 lines) supports both behind
build tags. Observe `metadata` (prefer `icy-title` key) and `media-title` as
fallback; watch `core-idle` / `paused-for-cache` for the connect spinner.

## Station data

radio-browser.info API, cached in SQLite. Etiquette: resolve a server from the
`all.api.radio-browser.info` pool, send a real User-Agent (`screech/x.y`), hit the
click endpoint on tune (fire-and-forget). Sync the top `sync_limit` stations
(default 20,000) by votes with `hidebroken=true`, paged 5,000 at a time with
dedupe; a partial slice beats none. Use **`url_resolved`**, never raw `url` (raw
may be a .pls/.m3u — mpv copes, `<audio>` won't). Self-reported tags are weak
signal only.

**Dead-station lifecycle (v0.3):** the cache refreshes weekly in the background
(staleness = last_sync older than 7 days, or cache under half the limit after
the knob grows). On each sync: stations absent from the fresh slice with no user
history (no listens, no bandit counts, no loves, not preset, not the resume
station, not a seed) are pruned; re-vouched stations heal one local `fail_count`
strike, so temporary outages rehabilitate. Locally, three consecutive play
failures still bench a station immediately — `lastcheckok` alone was never
enough.

A small embedded seed list (SomaFM, Radio Paradise, WFMU, KEXP, NTS, WWOZ…) covers
first-run when the API is unreachable and gives cold start a decent floor.

## The algorithm

Three components. Honest about what each can and can't know.

### 1. Empirical station fingerprints

Log every parsed track per station → observed artist set per station. Overlap is
**IDF-weighted**: an artist's weight is inversely related to how many stations play
them (sharing a chart artist means nothing; sharing an obscure one means a lot).
Loving a track credits the artist; stations known to play loved artists surface
through the candidate pool and priors.

**Parser hygiene (load-bearing):** StreamTitle is a convention, not a standard.
Log only on title change. Reject empty titles, titles equal to the station name,
URLs/promo strings, and known ad markers. Split on the first ` - `; normalize the
artist key (case-fold, strip leading "the", strip feat./ft. suffixes, collapse
whitespace). Accept that a third of stations yield nothing usable — fingerprints
are one signal, never a gate.

**Cold-start honesty:** fingerprints exist only for heard stations. New-station
discovery rides on tag affinity + priors + the random slice for the first weeks,
and that's fine. Future option (do not build now): daemon "scout" mode that
background-samples a candidate's stream for ten minutes to fingerprint it.

### 2. Thompson sampling bandit

Per station × daypart: Beta(α, β). On tune request: sample each candidate's Beta,
play the argmax. Uncertainty handles explore/exploit with no knobs.

**Bounded rewards (prevents sleep-listening from swamping):**
- listen end, duration ≥ 90s: α += clamp(minutes/10, 0.2, 3.0)
- listen end, duration < 90s (fast skip): β += 1.0
- fast skip during suspected ad break: β += 0.25 (discounted)
- love: α += 2.0 on the current station (artists/tags credited separately)

**Lazy decay:** half-life ~21 days. Each row stores `updated_at`; on read/write,
pull (α, β) toward the (1, 1) prior by `0.5^(Δt/half-life)`. No cron, no
background decay pass. Same treatment for tag affinities.

**Open-listen hygiene:** screech is single-instance against its database, so
any listen still open at startup belongs to a dead process (window close,
SIGKILL, power loss) and is reaped: closed at its start time, no bandit
credit. Clean quits end the listen first, banking the dwell-time credit.

### 3. Daypart context

Four dayparts (morning/day/evening/night, local time). Bandit reads the daypart row
blended with the all-time row; blend weight shrinks as daypart evidence accumulates.
Thin data backs off to all-time automatically.

### Candidate pool per tune

- top stations by decayed tag affinity
- stations whose observed artists overlap the loved set (IDF-weighted)
- a small pure-random slice (serendipity)
- never the current or previous station; never stations with fail_count ≥ 3

Never-heard stations get priors seeded from tag affinity × loved-artist overlap ×
ad-risk penalty.

**Explainability:** every pick carries a reason string, shown dim under the band
line: "plays 3 artists you love" / "tag: ambient" / "wildcard".

## Ad minimization

Curation does the real work; runtime detection is a bonus, and expectations stay low.

1. **Curation (~80%):** ad_risk score per station. Downrank big-network CDN
   hostnames (iHeart/Audacy/Cumulus etc.) and "top 40 hits"-style tags with very
   high click counts. Uprank .edu homepages, college/community/freeform/
   listener-supported markers.
2. **Runtime (conservative):** watch titles for ad markers (literal
   "advertisement"/"commercial", empty title, title == station name). Most
   commercial streams *don't* change titles during breaks, and DJs legitimately set
   the title to the station name — so detection only flags a *suspected* break
   (dim indicator) and discounts skips made during it. **Auto-hop is config-gated
   and off by default.**

## SQLite schema

`modernc.org/sqlite` (pure Go). WAL mode.

- `stations` — radio-browser cache + ad_risk + fail_count + fetched_at
- `listens` — station, start/end, daypart, skip_fast, during_ad
- `tracks_heard` — station, artist_key, artist, title, raw, heard_at
  (indexed on station, artist, and heard_at — the recent-history sort key)
- `loved` — artist_key, artist, title, station, loved_at. A row with empty
  artist/title is a trackless station love; the `l` toggle reads these rows
  as ground truth.
- `bandit` — (station, daypart) → alpha, beta, updated_at  (daypart 'all' = all-time row)
- `tag_affinity` — tag → weight, updated_at
- `meta` — key/value (schema version, last station, last sync)

## UI — visual language

Screech is an ambient object: on screen for hours, touched twice an hour. The
visual budget goes to the idle state and the two touch moments.

**Materials.** The wide layout is one receiver faceplate rather than a set of
dashboard cards: a warm-black body, quiet metal edge, raised reason readout,
and small glyph clusters. Content is clamped to 92 columns and centered; it
never stretches without limit. When track metadata exists, the track is the
hero and the station becomes its broadcast source; without metadata the
station takes the hero position. Short station names may use letterspaced caps;
longer directory names use natural spacing and bold weight. Four core tones:
accent (default amber `#FFB000`, TOML-overridable), bright, mid, dim — warm grays
to match. Coral is reserved for love, warnings, and destructive confirmation.
Dither glyphs `░▒▓` are allowed for faint gradients. Braille block (U+2800) is
allowed for fine detail.

**Faceplate layout (v0.5).** The brand/status rail pins to the top and the
stateful key strip pins to the bottom. At 78 columns and sufficient height, a
single framed receiver body centers between them. Its left bay contains track,
artist, and broadcast identity; its right bay contains the live signal and the
station-memory dial. A raised full-width readout explains the current pick.
Narrow terminals collapse to the v0.4 stacked composition. Meter columns touch
to form a continuous signal silhouette, library selection is a full-row background bar, and
microlabels anchor every data field. Surfaces derive from the accent hue so any
configured color keeps its temperature. The footer exposes live state (`loved`,
`preset 7`, `75%`) rather than acting as a static command legend.

**Color material (v0.4).** The accent is no longer a garnish. Grays derive
from the accent hue (an amber accent warms them to stone, a violet cools
them to lavender — the fixed olive family is gone), and the dim floor is
raised so "quiet" never means illegible. The header readout sits on the
accent while the stream is live; buffering blinks. The dial marker carries
a five-cell ember→accent→ember gradient. A loved track's whole row takes
the faintest accent surface, readable from across the room. The status
line's label carries a category glyph and weight: accent for seed/love,
mid for recall, dim for wildcard. Still one hue — many temperatures.

**Library views.** `h` opens recent tracks. `H` opens a unified library on the
loved view; Tab cycles recent, loved, and saved stations. Loved tracks are
deduplicated, searchable across artist/title/origin station, and
keyboard-selectable. The selected track can seed more music from its artist,
return to its origin station, or be removed with a guarded action.

The **saved-stations view** is the full two-tier station list: presets
(carrying their slot number) plus every station with loved tracks, unified
and sorted most-listened first. It's unbounded — presets stay at nine for
instant digit recall, but the library holds every station you've ever
marked. `/` filters by name, `j`/`k` select, `enter` tunes, digits jump to a
preset, and a guarded `x` removes the station from recall (preset slot and
loved rows) while leaving listen history and the taste model untouched.
Radio metadata is recall and discovery context, never presented as an
on-demand replay URL. Esc closes.

**The wave (honest amplitude, synthetic texture).** Two rows of half-blocks
per bar, a vertical gradient from a visible ember base through the accent to
a pale peak, peak-hold ticks that cool through the ramp as they fall. Bars
are bass-weighted — the left end responds harder to the loudness signal — so
the single amplitude number still reads as a spectrum, not a uniform bounce.
Amplitude is real (mpv astats RMS over IPC ~20Hz); texture is synthetic. If
level data stops (other backends), it falls back to self-animated breathing
after 3s. Real FFT arrives with Path 2's pure-Go audio; the renderer already
takes a `[]float64` either way.

**Set piece: tuning (the signature moment).** A band line under the header
(`──────╂──────`). On next: accent marker springs to the new station's position
(overshoot, settle — harmonica), wave flatlines to `▁▁▁`, old station name
dissolves through random glyphs resolving left-to-right into the new name
(decrypt effect). Wave swells when the stream locks. No generic spinner exists
anywhere in the app.

**Set piece: love.** One-frame inverse flash on ♥, holds accent ~2s, decays to a
persistent dim ♥. Structured feedback (`LOVED  Track and station`) lands the
same frame as the keypress, always; never block render on network.

**Idle (the main state).** After ~3 min without keys: everything dims except track
title and wave; the accent marker breathes (slow luminance sine, ~6s period). Any
key snaps bright. Natural track changes step the new title dim → mid → bright.
Marquee etiquette for long titles: pause 2s, scroll once, pause, reset — no
endless loops.

**Boot.** ~500ms: rules draw outward from center, wordmark letterspaces in, then
content. Skippable on any key.

**Texture.** Mid-contrast readout cluster top-right: `AAC  128K  2:14:06`.
Fixed-width clock formatting and uppercase codec labels provide
instrument-panel credibility. The command legend sits two rows below the
status line, keeping the whole display a single 12–14-line object.

**Reality.** One `tea.Tick` heartbeat (15–20fps); every animation is a pure
function of elapsed time; no per-effect goroutines. Fixed zone heights so frames
never reflow. All truncation through go-runewidth, ellipsis, never wrap.
Degradation ladder: <50 cols drop wave + band line; <40 drop readout; never break.
`ascii = true` in TOML swaps every fancy glyph for ASCII (bad SSH insurance).

## Config

One TOML at `os.UserConfigDir()/screech/config.toml`, created with commented
defaults on first run: `accent`, `ascii`, `mpv_path`, `data_dir`, `auto_hop_ads`.

## Stack

bubbletea + lipgloss + harmonica · hand-rolled mpv IPC client (unix socket +
windows named pipe) · hand-rolled radio-browser client · modernc.org/sqlite ·
go-runewidth · BurntSushi/toml. No bubbles/list in v1 (nothing to list).
Daemon later: net/http stdlib; phone page: htmx.

## Build order (v2 — data starts flowing on day one)

1. **Vertical slice:** schema + store, radio-browser client + seeds, mpv IPC,
   listen/track logging, hardcoded station, minimal render. It plays radio and
   remembers what it heard.
2. **Bandit + pool** (~a week of listening data exists by now): tune, reasons,
   bounded rewards, lazy decay, ad-risk curation.
3. **Full visual treatment:** boot, wave, dial sweep, decrypt, love flash, idle
   breathing, degradation ladder.
4. **Fingerprints** (needs weeks of tracks_heard): IDF overlap into pool + priors.
5. **Daemon:** extract API, stream relay, move TUI onto it.
6. **Phone page:** htmx, big NEXT, heart.
7. Demoted extras: `/` search, heard/loved toggles, runtime ad suspicion +
   opt-in auto-hop.

## Path 2 notes (do not build, do not preclude)

Wails desktop app reusing core unchanged. mpv → pure-Go audio (oto + decoders) or
webview `<audio>`. Daemon optional. ICY reading in-process. Real FFT feeds the
same wave renderer. Known tradeoff: without the daemon, multi-device state doesn't
sync (acceptable; SQLite sync or optional daemon later).
