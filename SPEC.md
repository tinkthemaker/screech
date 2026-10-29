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

No playlists. No accounts. No recording. No podcasts. No general config UI
(one TOML file); the focused theme picker is the presentation-only exception.
No album art / sixel. No Nerd Fonts (box-drawing, blocks, braille — all standard
Unicode). v1 additionally cuts: visible station browser, search, manual station
starring, heard/loved views (they return as demoted toggles post-slice).

## Interaction model (root principle)

The algorithm learns from exactly two inputs plus a clock, so the UI needs exactly
two controls:

- **next** (`space` or `n`) — pressed fast (<90s) it's a skip (negative). Pressed
  after a long listen it's a variety request (listen credit already banked, no penalty).
  On a discovery station it asks the player for the next song, and the
  station is never charged a skip; a second next within 3 s changes station
  (see Discovery songs). While paused on a discovery station it is the same
  next song, and resumes playback with it.
- **love** (`l`) — strong positive on the track's artist + station + tags.
  Toggle: loving the same track (or, trackless, *that same station*) a
  second time returns the boosts. Returning is floored at the (1,1) prior:
  decay has usually eaten part of the boost by then, so subtracting the
  full dose would leave a station worse off than one never loved at all.
  Taking back praise is not punishment. A trackless love is keyed to its
  station, never global, so loving a second silent stream cannot read as
  un-loving the first.
- **dead streams** (`TuneDead`) — a stream that fails to lock closes its
  listen with no skip semantics and no bandit reward; fail_count is the
  whole penalty. Network trouble is not dislike — and an outage is not the
  stations' fault either: three consecutive failures read as the network,
  so those strikes are returned (`ForgiveFailure`) and the UI goes offline
  instead of burning through the cache (see Playback).

Shipped in v0.2, still within the austerity budget:

- **presets** (`f` save/unsave, digits 1-9 recall): deterministic *recall*, the one
  job the bandit can't do. Deliberately zero effect on the taste model — love
  teaches, presets remember. Saved stations render as ticks on the band
  line, each carrying its slot digit — an unlabelled tick can tell you a
  saved station lives at that position but never which key returns to it.
  Nine slots stay nine: presets are muscle memory, not a list. Everything past
  that lives in the library's saved-stations view (below).
- **seeding** (radio search: `/`, then Tab in the FIND bar; the window's RADIO
  tab): type a genre or artist. Genres resolve strongest: a tag
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
  than occupying permanent space in the idle display, and it behaves like
  one: it tucks itself away after four seconds without a volume key, and
  any other key closes it and still does its own job — the slider never
  turns space into a dead key.
- **pause** (`p`): one pause state — mpv's own `pause` property — so `p`,
  keyboard media keys, headset buttons, the OS media flyout, and MPRIS all
  hold the same output. Pausing splits the listen: the banked segment keeps
  its dwell credit, paused wall time counts toward nothing, and a fast
  skip after resuming is judged on the session's total active dwell, not
  the fresh-looking segment. Resuming inside 30s unpauses in place and
  picks up from mpv's buffer a few seconds behind live, like pausing live
  TV; past that the buffer is stale or the server has hung up, so resume
  reloads and rejoins the live edge. The flyout's Stop button lands the
  same way but unloads the stream, so it always reloads on resume;
  Next/Previous do nothing — there is no playlist. Choosing a station
  (space, digits, library, seed) turns the radio back on regardless of
  who paused it.
- **sleep timer** (`z`): steps 15/30/45/60/90 minutes then off, counted
  down in the footer. The last 30 seconds ease the player's volume toward
  silence — the persisted listener volume is untouched and restored —
  and expiry pauses the radio.
- **help** (`?`): a read-only key sheet over whatever is underneath. Any
  other key closes it and still does its own job, like the volume strip.
- **station info** (`i`): a read-only sheet for the tuned station —
  directory facts plus the personal ledger: audible time, visits, first
  and last heard, named loves, preset slot, strikes. Its MATCH figure is
  the mean of the Beta the picker samples this station from in the
  current daypart, not a parallel score, and the sheet re-requests as the
  radio moves, so it always answers for what is playing.

Pause, sleep, help and info arrived in v0.8; none of them teaches the
model anything (pause only stops the clock).

`q` quits. Launch resumes the last station and plays immediately — zero-press startup,
like a physical radio. Everything else is automatic or read-only.

## Architecture

Four layers, buildable in vertical slices:

1. **Core package** — SQLite, scoring, fingerprints, ad-risk, radio-browser client.
   No UI knowledge. The portable heart for Path 2.
2. **Bubble Tea TUI** — talks to core directly at first, daemon API later.
   The listening state machine is not the TUI's: `internal/session` holds
   it as a UI-free value (`State`, Elm-style `Update(Msg) (State, []Cmd)`)
   that any face drives. It owns phases, picks, the tune/drop/offline/
   restart rules below, pause from any source, the sleep timer, auto-hop,
   love, presets, volume and the transient feedback line. The TUI renders
   a `session.State`, maps keys to intents, and does only face work on top
   (ceremony on each pick, wave energy, readouts, overlays, library and
   info sheets that read core directly). `internal/app` is the shared
   startup — config, single-instance lock, last-run log, database, mpv —
   and returns errors for the face to present. State-machine behaviour is
   pinned by `internal/session`'s tests; rendering by the TUI's.
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

### Faces over one engine

Layers 1 and 2 above are a dependency direction, not just a list. `core`
knows no UI. `internal/session` sits on `core` and `player` and knows no
face either. A face imports `session` and draws it:

    core -> session -> faces: tui (terminal), gui (Windows window)
    app  = the shared startup both programs call first

Two packages sit beside the engine. `internal/ytdlp` is the one place yt-dlp runs: discovery, a tune's page link
and `screech doctor` all come through it, so its rules hold app-wide. It
finds the executable from `ytdlp_path` (`""`: PATH, then beside the
executable, then the data folder; `"off"` disables it; anything else is used
as given). It hands yt-dlp a JavaScript runtime (Deno on PATH is used as is,
else the first of node and bun on PATH, as `--js-runtimes <name>:<path>`),
runs one yt-dlp process at a time for the whole app with a tune's lookups
ahead of prefetches, works in the data folder, and passes `--no-warnings`.
`internal/discover` is the discovery song queue: it mixes discoveries and
anchors from YouTube Music (through `internal/ytdlp`) and Bandcamp genre
tags, never repeats a song within its last 200, never plays one artist twice
in a row, and puts new-to-you bands first among the discoveries. The player
walks that queue behind a `discover:` sentinel (see Playback and Discovery
songs).

`cmd/screech` runs `app.Start`, then the Bubble Tea faceplate over a
`session.State`. `cmd/screech-gui` (Windows only; every file in it is
`//go:build windows`) runs the same `app.Start`, then a frameless Wails v2
window. The window has no Bubble Tea loop to ride, so the engine runs on a
`session.Runner`: one goroutine owns the `State`, each command runs on its
own goroutine, a 250 ms tick republishes the changes no message causes
(feedback expiring, the sleep minutes), and the face receives a
`session.Snapshot` only when it differs from the last. A `Snapshot` is a
comparable value: everything the now-playing area draws and nothing it
needs a slice or map for. Level readings go out at most every 33 ms; the
meter animates between them. The single-instance lock covers both
programs, so only one of them plays at a time. The window's own design
record is `docs/superpowers/specs/2026-10-18-lcars-gui-design.md`.

The strip keeps familiar player controls in reading order: PAUSE/RESUME,
NEXT/RETRY NOW, LOVE, MORE/LESS. The close control is a red `✕` cap in the
top bar's right segment (`432-480 x 0-20`, radius `0 10px 10px 0`, colour
`#FF5555`, black text; the tan segment widens to `332-428`). It calls the
same bridge quit path as `q`, and EXIT is gone. On a discovery station the
track row shows the song, the status shows `NEW TO YOU` when the band has
never been heard on Screech before, and the reason line reads
`discovering · <phrase>`. Station and track text that
exceeds its row makes one measured pass after a reading pause, then returns
to the start; reduced motion leaves it still. The frameless HWND gets a
DPI-scaled window region on restore and each fold change, following the
LCARS face's 48-px left curves and smaller right caps. Dragged onto a
monitor at another scale, Wails resizes the window on `WM_DPICHANGED`
without telling Go, so the page watches `devicePixelRatio` and calls
`Rescaled`, and the shell reshapes the region to the window's new size.

**The bridge contract** (`internal/gui`, which imports neither Wails nor
`x/sys/windows`, so its tests run on every OS). `gui.Bridge` is the one
object bound to the page, and every bound method is exactly one of two
things: a single queued session intent, which first sends `session.Touch`
so a startup notice clears on the first interaction, as it does in the
terminal; or a single `core` read for a library view (recent, loved,
stations, taste, info), returned as a DTO with lowerCamel JSON, times in unix
milliseconds and durations in whole seconds. Nothing on that surface
blocks; slow work runs on the runner and reports back as an event. Go
pushes two events, `snapshot` (a `session.Snapshot`) and `level` (`[L, R,
peak]`). The page draws `Snapshot`s and never decides behaviour: wording
the terminal already has is reused verbatim, and even the first-run
wildcard is an intent (`Wildcard`), not page logic. What only the shell
can do goes through the small `gui.Window` interface (fold and unfold,
quit), and display choices the page can't read for itself (reduced motion)
come from `Bridge.Prefs()`. Two tests pin the surface from both sides:
`TestBoundSurface` lists the exported methods, and `TestBridgeJSContract`
requires `bridge.js` to name exactly those, so a method can't exist on one
side only.

**The console's tabs** are, in order: FIND (the front door, selected by
default), RADIO, RECENT, LOVED, STATIONS, TASTE and MORE. The tab geometry is
unchanged: seven 28 px slots from y 156. MORE holds what INFO and SLEEP held,
the station sheet and a SLEEP pill that cycles the timer and shows
`SLEEP nn`. The MORE button opens the console on FIND, and so does `/`; `H`
still opens LOVED, and `I` opens MORE. With no history at all, the window
opens unfolded on FIND; otherwise it starts folded and playing, as before.

**FIND** is a bar at `136-464 x 148-184` (36 px tall, radius 18) in three
parts: an orange `FIND` cap 52 px wide, a black field with a 2 px orange
border, and an orange `GO` cap 52 px wide in the same colour as FIND. The
placeholder, lilac at 15 px, reads `Artist, song, genre or YouTube link` and
must fit the field. Typed text is ice at 16 px, and a long link scrolls inside
the field. Enter or GO starts a discovery station from what was typed, folds
the console, and sends the strip to `TUNING`. A label at y 204 (peach,
11 px) reads `PICK UP WHERE YOU LEFT OFF`, above up to four rows from y 220,
each 28 px tall: your most recent discovery stations, shown as the phrase (or
the song a link started from) with a relative time on the right. A click, or
the down arrow then Enter, resumes one. With none, the label and rows are absent. The hint,
centred at y 348 in grey `#777` at 11 px, reads `ESC TO CLOSE · TAB FOR
RADIO`, with the key names in ice. There is no heading and no other text.

**RADIO** is the same bar in blue, with a `RADIO` cap and the placeholder
`Station, genre or country`. Enter tunes the best radio-browser match, which
is today's search (`Seed`). Below the bar the rows are the listener's
most-listened radio stations, laid out as in STATIONS; a click or Enter tunes
one. A full results list needs an async radio-browser search, since nothing
slow may run on a Wails call path, so it is a follow-up.

The bridge gains four methods for these views. `Discover(query)` is FIND's
Enter, a `session.Discover` intent: the core decides whether the text is a
link and whether the station already exists. `ResumeDiscovery(uuid)` is a
click or Enter on a FIND row, a plain `TuneTo` with the reason `discovering`,
since the station is already in the database. `RecentDiscoveries()` (a core
read of up to four discovery stations, newest first) feeds FIND's list, and
`RadioTop()` (a core read of the most-listened radio stations, the listener's
own stations and discoveries left out) feeds RADIO's rows. A failed read comes
back as an empty list, so the bar still opens.

**The layout guard.** The front end is plain ES modules served from
`embed.FS` with no build step, so nothing but a test stands between a CSS
edit and a crooked panel. `go test -tags layout -run "Layout|KeyboardMap"
./internal/gui` (Windows only; skipped when Edge is absent) renders every
window state (each strip state, every console view, empty and 150-row
lists, nine full presets) in headless Edge, at 100% and 150% scaling,
folded and unfolded. The fixtures are built from the bridge's own Go types
and fed to the real page through a fake bridge. It fails on any box edge off
the 4 px grid, text off the x = 136 column (filled rows 124), text that
overflows or escapes its box, two leaves that overlap, wrong words or
colours where a fixture pins them, and any strip element that moves between
states or between folded and unfolded. `TestKeyboardMap` replays scripted
key and wheel events on the real page and checks which bridge calls each
one makes, including that the wheel never sets the volume. Screenshots land
in `.review/gui/`, and CI uploads them.

## Playback (TUI)

Shell out to `mpv --no-video --idle` over JSON IPC. mpv handles codecs, ICY,
reconnects, playlist-file URLs, buffering. Player is behind a Go interface so a
pure-Go backend can swap in for Path 2 (enables real FFT).

IPC transport differs by OS: unix socket on linux/mac, **named pipe on Windows**
(`\\.\pipe\…`, via go-winio). The thin client (~150 lines) supports both behind
build tags. Observe `metadata` (prefer `icy-title` key) and `media-title` as
fallback; watch `core-idle` / `paused-for-cache` for the connect spinner.

**Stream lifecycle (v0.7).** Live radio drops constantly, and mpv does not
reconnect non-seekable ICY streams by default, so a dropped server used to
leave `LIVE` on screen over silence. The rules, verified against mpv 0.41:

- `end-file` events are filtered by load generation: between a `loadfile`
  and the next `start-file`, any `end-file` belongs to the file being
  replaced (mpv always reports the old file's `stop` before the new
  `start-file`) and is dropped. What remains is about the current stream:
  `error` → stream failed, `eof` → stream ended on its own.
- `--stream-lavf-o=reconnect_streamed=1` lets ffmpeg ride out brief drops
  inside one connection; the app-level rules below are the backstop.
  `--ytdl=no`: mpv's hook would otherwise run yt-dlp on every URL that
  fails to load, delaying the failure everything below keys on. Page links
  are resolved by screech itself instead (next item).
- **Page links.** A listener's own station may be a page (YouTube,
  SoundCloud, Vimeo, Mixcloud, Bandcamp or Twitch, matched by host in
  `streamurl.IsPage`). With yt-dlp found (see yt-dlp under Discovery songs),
  `Play` runs `yt-dlp -f bestaudio/best --no-playlist --playlist-items 1
  --get-url` plus `--print` lines for artist, track and title before `loadfile`, under
  a hard 6 s timeout: the tune watchdog allows 12 s from the pick, so mpv
  keeps half. `/best` is there because YouTube live streams offer only muxed
  HLS, which mpv plays audio-only under `--no-video`. The old station is
  stopped first (that `stop`'s end-file falls inside `loadPending` and is
  dropped) so its titles can't land on the new listen. A resolution that
  fails or times out returns `player.ErrResolve`, which the session handles
  as `PlayFailed`: one strike, on to the next station. A Play overtaken
  while resolving returns `player.ErrSuperseded` and loads nothing; the
  check and the `loadfile` share one lock hold, and the session ignores
  that error, so a skip during yt-dlp leaves no strike and no stray listen.
  The direct URL is cached in memory per page for an hour, well inside the
  ~6 h its signature lasts, and dropped when its stream errors or ends, so
  a reconnect or the next visit resolves afresh. It is never stored: the
  station keeps its page link. The URL carries no ICY metadata and its
  filename is junk, so the title goes in as a per-file option,
  `loadfile <url> replace -1 force-media-title=%n%<title>` (mpv's
  length-prefixed quoting, so commas, quotes and `=` survive; needs mpv
  0.38+), and rides the media-title path like any other title. The title is
  "artist - track" when yt-dlp knows both, else the page title, else none,
  leaving the filename filter to keep the URL out of the heard list. The
  extractor is an interface (`player.Extractor`), so unit tests run a
  scripted yt-dlp and never the network. With yt-dlp off or missing, a page
  link goes to mpv as-is and fails there, as before.
- **Discovery stations (`discover:`).** A discovery station's `StreamURL` is
  the sentinel `discover:<seed>`, where the seed is the phrase or link it was
  started from; only the player resolves it, and `Play` still rejects every
  other non-http(s) URL. The player holds the station's song queue behind the
  sentinel and walks it itself. It resolves the first song, then prefetches
  one ahead while the current song plays. Songs advance by themselves: the
  next song loads with `force-media-title` set to `Artist - Title` (the
  `loadfile ... replace -1 force-media-title=%n%...` path that page links use) and
  emits a title event, and it never emits `EventStreamEnded` between songs, so
  a song change is never read as a dropped stream. The first song must play
  inside the tune watchdog's 12 s, so its lookups are capped at 8 s in total.
  Bandcamp gets a first try of at most 2 s, since it needs no yt-dlp: when no
  tag is settled yet it is one tag probe (the first tag, slice `new`), and
  with a tag settled the usual cache read. If it serves a song, that is the
  first song; otherwise YouTube gets the rest of the budget. Later, a Bandcamp
  tried ahead of a YouTube that is waiting behind it gets a quarter of the
  time left. Either way a hang counts as a failure its 10 s gap holds back, so
  it costs one wait, not one per ask, and an artist phrase (no Bandcamp tag)
  still reaches YouTube. The budget stays 8 s: that leaves mpv 4 s to lock
  before the watchdog, which strikes, where a late lookup does not. Background
  prefetch calls get up to 20 s each. A prefetched media URL older than 1 h is
  resolved again before it loads. A first song that can't be found fails
  `Play` (`ErrResolve`), which the session reads as `PlayFailed`. Once a song
  has loaded, a song that won't resolve or play is skipped, and three in a row
  raise `EventStreamError`, the event a dropped stream raises. Either way the
  existing strike-and-move-on path runs. A lookup that fails because the
  network is down takes the existing offline path and never strikes the
  station: network trouble is not a bad station. A network failure that the
  prefetch reported more than 5 s before its song ended is asked about once
  more before it ends the walk, since a song can play for minutes after the
  network goes. A lookup that merely ran out of time is a failed song, not an
  outage, unless a source marked it `ErrNetwork`; the one exception is a
  tune's first lookup, where a timeout can't tell a slow network from a bad
  seed, so it takes the no-strike road as network trouble does. The strip clock counts
  the station, not the song.
- Personal `mpv.conf` still applies (audio device, exclusive mode — the
  parts worth keeping), but options that break live radio are pinned at
  spawn: `keep-open` and `loop-*` would swallow the eof that drop
  detection keys on, `pause=yes` would make every tune look dead,
  `resume-playback`/`save-position-on-quit` would "resume" a live stream
  at a stale offset, and the default demuxer cache (150MiB ahead, 50MiB
  back) only grows for radio — about 55 minutes of never-seeked back
  buffer per hour at 128k — so it is bounded to 32MiB/4MiB.
- Pause is mpv's `pause` property, observed like the other properties:
  property-changes map to paused/resumed events (an external `stop`
  end-file maps to stopped), and `Play` always sends `pause=false` after
  `loadfile` because mpv's pause survives the load — a chosen station must
  never start silent.
- Until an ICY title arrives, `media-title` falls back to mpv's `filename`:
  the whole URL percent-decoded, then everything after its last `/`, query
  included (`aac-128`, `kexp128.mp3`, `rq?rj-ttl=5&rj-tok=…`; KEXP sends an
  empty `icy-title`, so its stays that forever). The player drops a
  media-title equal to any form `streamurl.MpvFilenames` derives from the
  URL it loaded, or to the `filename` mpv last reported, which covers
  playlist entries Screech never saw. It used to show as the track and log
  a junk heard-track on every tune (migrations 4 and 5 purged them).
- A failure *before* lock (error, eof, or 12s of nothing) is the station's:
  one strike, move on. A failure *after* lock (eof, error, or a 20s buffer
  stall) is a drop: re-`loadfile` the same station without touching the open
  listen, so dwell credit and the readout clock carry across the gap. More
  than three drops without a stable minute in between counts as a failure.
- Three consecutive pre-lock failures mean the network, not the stations
  (offline DNS fails in milliseconds; striking each would bench the whole
  cache in a minute). The strikes are returned, the listen closes without
  semantics, and the UI enters `OFFLINE`: retries back off 5s → 60s,
  alternating the last station that locked with a fresh pick, and never
  strike. Any lock clears it.
- A crashed mpv is respawned (same volume, new IPC endpoint) and the station
  resumed as a reconnect, at most three times per five minutes.
- mpv is a child that must not outlive screech: a kill-on-close job object
  on Windows, `PDEATHSIG` on Linux. macOS has no kernel equivalent; a clean
  quit, SIGTERM, SIGHUP, or panic all still stop it.

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
station, not a seed, not one of the listener's own) are pruned; re-vouched stations heal one local `fail_count`
strike, so temporary outages rehabilitate. Locally, three consecutive play
failures still bench a station immediately — `lastcheckok` alone was never
enough.

A small embedded seed list (SomaFM, Radio Paradise, WFMU, KEXP, NTS, WWOZ…) covers
first-run when the API is unreachable and gives cold start a decent floor.

**The listener's own stations (`custom:`).** Any http/https stream link is a
station: a `[[stations]]` table in config.toml, written by hand or by
`screech add <link> [--name N] [--tags T]`. The config file is their record
and the database only a cache of it, so the database stays disposable. Each
launch (`internal/app`, so both faces) hands the list to
`Core.SetCustomStations`, which upserts every entry under `custom:` plus the
first 16 hex digits of the SHA-256 of its link (scheme and host case-folded),
then deletes the custom rows whose link has left the config, with their
preset slots. Hashing the link means reordering or renaming entries never
re-identifies a station, and listens, loves, heard tracks and bandit counts
stay under the uuid, so adding a removed link back finds its history. The
link is played as given (there is no directory to resolve it) and gets the
same ad-risk curation as directory stations; a launch heals one strike, as
it does for seeds. The directory sync can't touch these rows: its uuids
never collide with `custom:` ones, `PruneStale` spares `custom:%` beside
`seed:%`, and the click report skips both namespaces. `NeedsSync` counts
directory rows only (seeds included), so links in a fresh config never pass
for a synced cache. `screech add`/`remove` edit config.toml as text, so
comments, order and spacing survive; each edit is re-read and checked before
it replaces the file, and neither command opens the database. A running
instance picks the change up on its next launch.

**Discovery stations (`disc:`).** A station started from a phrase or a
YouTube link is a namespace of its own. Its id is `disc:` plus the first 16
hex digits of the SHA-256 of `"discover:"` followed by its identity key: the
trimmed, lower-cased phrase, or the canonical video URL for a link. A watch
page with a playlist along for the ride, a `youtu.be` share, the music site
and the short form all name the same video, so they name the same station,
and starting the same phrase again resumes it with its history. Startup
deletes `custom:` rows that have left the config, but never `disc:` rows:
stations started from FIND are history, not config. `PruneStale`, `NeedsSync`
and the click report treat `disc:` like `custom:`. Config-defined discovery
stations are upserted at every launch, as links are. Stations started from
FIND live in the database, and the FIND list shows the most recent of them.
Pressing F (preset), or `screech add --discover`, keeps one for good. The
picker sees a discovery station as one of the listener's own stations (the
4-seat custom slice), and never charges it a skip for a song change.

## Discovery songs

**Where a station starts.** FIND is the one bar that starts a discovery
station in both faces. In the window it is the console's first tab. In the
terminal, `/` opens it as a full sheet in place of the faceplate, like help and
the theme picker, with the hint `esc close · tab radio`. Enter starts a station
from what was typed, a phrase or a link (a link is keyed by its video, see
Station data), and Tab switches the bar to radio search. Your recent discovery
stations sit under the bar, to pick up where you left off.

A discovery station's queue (`internal/discover`) mixes **discoveries**, bands
the listener has probably not heard, smaller and newer, with **anchors**, the
genre's well-known tracks. The anchor share starts at about one in five and
stays between one in eight and one in three. Three quick skips in a row (each
within 60 s of its song starting) raise it for the next ten songs, and a LOVE
on a discovery lowers it. The two sources take turns for discoveries, with
Bandcamp and YouTube at about half each. Either source can be missing, and the
station carries on with the other.

**YouTube Music, through yt-dlp.**

- **Search:** `https://music.youtube.com/search?q=<query>#songs` with
  `--flat-playlist` returns real songs: titles and ids, no views or durations.
- **Anchors** come from the first 5 results for the phrase. **Discoveries**
  come from results 10 to 50, and from variant queries (the phrase plus
  `underground`, `demo` or `new`).
- **Resolving** gets the media URL, artist, track, duration and view count. A
  discovery that resolves with over 1 M views is re-labelled as an anchor.
- **Songs only:** anything outside 90 s to 12 min is skipped (no mixes, no full
  albums).
- A YouTube link seeds from its own Mix (`list=RD<id>`) for related songs. Its
  tags supply the phrase for the Bandcamp side and for the phrase searches; a
  video with no tags takes the artist it resolved to, or its cleaned title. The
  Mix is a few dozen songs, so once it is used up the station carries on from
  the phrase searches, as a typed phrase's would.

**Bandcamp genre tags, with no yt-dlp needed.**

- **The request:** `POST https://bandcamp.com/api/discover/1/discover_web` with
  JSON `{"category_id":0,"tag_norm_names":[<tag>],"geoname_id":0,
  "slice":<"new"|"rand"|"top">,"cursor":"*","size":60,"include_result_types":["a"]}`.
  It is undocumented, and it can break.
- **The response:** each result carries `band_name`, `title` (the release) and
  `featured_track{title, duration, stream_url}`. `stream_url` is a signed MP3
  that mpv plays directly. Results without a `featured_track` are skipped, and
  so are tracks outside 90 s to 12 min, as on YouTube (a feed that gives no
  duration passes).
- **Slices:** `new` and `rand` give discoveries, and `top` gives anchors.
- **Tags from the phrase:** lower-cased and hyphenated (`swedish death metal`
  -> `swedish-death-metal`). If that returns nothing, leading words are dropped
  (`death-metal`). If nothing ever matches, as with an artist name, Bandcamp
  sits out and YouTube carries the station.
- **Pacing:** at most one Bandcamp request per 10 s per station, with a
  `screech/<version>` User-Agent. The one exception is the walk down the tags
  at the start of a station: after an empty answer the next tag is the same
  question, so up to 4 probes go out back to back, once. A station's first ask
  makes only the first probe, so nobody waits on the walk; it goes on at the
  next ask that is Bandcamp's turn. Results are cached
  for 30 min. A request times out after 4 s, well inside the first song's 8.

**New to you.** A discovery whose artist key appears in neither `tracks_heard`
nor the loves is new. New artists come first among discoveries, and the strip
shows `NEW TO YOU`.

**No repeats.** A song (video id or Bandcamp track id) never repeats on a
station within its last 200 songs, and the same artist never plays twice in a
row.

**Titles.** Cleanup applies only to discovery songs. Bracketed or parenthesised
packaging is removed: Official (Music) Video/Audio, Official Visualizer,
Lyric(s) Video, Audio, Video, HD, HQ and 4K. Remaster and live notes stay.
Trailing `ft.`, `feat.` and `featuring` credits are removed, and ` - Topic` is
removed from a channel name used as the artist; then `core.CleanText` runs.
yt-dlp's `artist` and `track` are used when present, otherwise the cleaned
video title through the existing split. Bandcamp gives `band_name` and
`featured_track.title`.

**yt-dlp.**

- **Lookup order:** `ytdlp_path = ""` (the default) means find it: PATH, then
  beside the executable, then the data folder. `"off"` disables it, and any
  other value is used as given.
- **JS runtime:** Deno on PATH is used as is. Otherwise the first of node and
  bun on PATH is passed as `--js-runtimes <name>:<path>`.
- **Process rules:** one yt-dlp process at a time, app-wide, with a tune's
  lookups ahead of prefetches; the working directory is the data folder; and
  `--no-warnings`.
- **Caches,** in memory only: search results for 6 h (one entry per phrase,
  holding its longest answer, so the anchors' five come out of the discoveries'
  fifty), and what resolving a video said for 10 min, so a video that lost the
  draw is not resolved a second time. A prefetched media URL older than 1 h is
  asked for again by the player; that is its re-ask, not a cache. Media URLs
  are never written to the database, the config or the log.
- **Without yt-dlp,** discovery stations still play from Bandcamp. A YouTube
  link can't start a station: the note says `YouTube links need yt-dlp · see
  screech doctor`, and no station is made. A link that isn't a YouTube video (a
  playlist, a channel, any other site) is refused the same way with `can't
  start from that link · paste a YouTube video, or type a genre or artist`.
- **`screech doctor`** reports the yt-dlp it found, where, and its version, and
  suggests `yt-dlp -U` when that release is over 60 days old. It also reports
  the JS runtime it will hand yt-dlp, whether Bandcamp's discover feed answered,
  and how many discovery stations the config carries.

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
- every write floors α and β at 1.0, so no reward path can push a count
  below the untouched prior

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
- heard favourites: up to 12 stations whose own posterior mean (the Beta the
  sampler draws from) is above 0.5. Without them a station could only come
  back through its tags, a loved artist, or the random slice, which lost
  tagless favourites for listeners who rarely press love
- the listener's own stations: up to 4 `custom:` stations per pick, drawn at
  random when there are more. Unheard and often untagged, they can't win tag
  affinity or overlap, and the random slice draws a dozen from twenty
  thousand. In the harness's genre+links scenario, three liked links went
  from 0% of picks to 7% for a listener who never loves (8% with loves), a
  dull fourth was gone from the late picks, and late E[like] held
  (0.604 → 0.609; 0.595 → 0.630 with loves)
- a small pure-random slice (serendipity)
- never the current or previous station; never stations with fail_count ≥ 3

The pool is handed to the sampler in a fixed order, so a seeded rng is
reproducible. `go test -tags eval -run TestEval -v ./internal/core` runs the
simulated-listener harness: synthetic catalogs, a listener with hidden taste
who stays or skips, and E[like] of the picks over time against a random
pick. Its scenarios cover genre taste, station-specific taste, genre plus
per-station quality, and genre taste plus four pasted links (three liked,
one dull), each with and without loves. Change the picker or the rewards,
then check this table.

Never-heard stations get priors seeded from tag affinity × loved-artist overlap ×
ad-risk penalty.

**Explainability:** every pick carries a reason string, shown dim under the band
line: "plays 3 artists you love" / "tag: ambient" / "added by you" /
"wildcard". A listener-added station that wins without artist or favourite
evidence says "added by you", never "wildcard".

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
   and off by default.** With `auto_hop_ads` on, only a literal marker in the
   title (`AdMarker`) hops — the empty and station-name cases stay suspicion,
   because DJ talk and station IDs produce them too. A hop is a skip through
   `Tune`, so it lands as a break skip (β += 0.25 when fast), the same lesson
   as pressing SPACE; one hop per 30 s keeps a run of stations all mid-break
   from chaining hops faster than a listener can follow.

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
- `meta` — key/value (last station, last sync, volume, theme)
- `presets` — slot 1-9 → station, saved_at

Schema changes are numbered migrations recorded in `PRAGMA user_version`,
each applied with its version stamp inside one transaction. Opening runs
`quick_check`; a database that fails it (or isn't SQLite at all) is moved
aside with its WAL/SHM as `screech.db.corrupt.<ts>` and rebuilt, and the
status line says so. Text arriving from the network (station metadata, ICY
titles) is stripped of terminal escapes, controls, and bidi overrides at
ingest and again on read; migration 3 normalized rows stored before that
(re-deriving artist keys), so loves recorded by older versions still toggle,
forget, and count.

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
single framed receiver body centers between them — which includes the
standard 80×24 window, the most common terminal there is. Its left bay
contains track, artist, broadcast identity, and the station's genre tags; its
right bay contains the live signal and the station-memory dial. A raised
full-width readout explains the current pick, and when it carries feedback
it keeps that feedback's own label (`LOVED  Track and station`, `ONLINE
Signal restored`) rather than a generic one.

"Sufficient height" is counted, not estimated. The height check adds up
every row the panel will actually draw — both rails, the readout, the
genre block when the station has tags, the density strip when there is
history — and only then decides between faceplate and stacked layout. It
used to assume the meter's first rows were free, and at common heights
(a 100×20 window, say) the readout and the bottom rail were silently cut
off by the screen edge. A frame is never drawn without its bottom edge.

**The station-memory dial is an instrument, not a hairline.** Three rows: a
listening-density strip, the band carrying the needle and the preset ticks,
and the slot digits beneath the ticks they belong to. One row of band glyphs
was defensible while the signal meter was also one row; beside a six-row
meter it read as a placeholder.

The strip replaced a row of evenly spaced graduations, and the reason is
worth keeping. Graduations imply a quantity along the axis and there isn't
one: dial position is `fnv32(uuid)`, so a mark at 30% measures nothing while
sitting directly above ticks that measure something real — decoration shaped
like data, competing with data. Cumulative listen time *is* a genuine
distribution over that same axis, so the row carries that instead,
normalized to its own peak. It answers "where do I actually live on this
band", which no other part of the interface does. With no history there is
no distribution and the row is dropped rather than drawn empty.

It draws as a one-row histogram in eighth blocks, one quiet ember, height
against a common baseline. Shade blocks `░▒▓` were the obvious material and
were wrong: fill density has no baseline, so adjacent heavy cells merge into
a solid rectangle instead of reading as separate values, and the top of that
scale measured 5.08 contrast against the panel where the band line it sits
under measures 5.28. A background strip outweighing its own instrument reads
as a rendering fault. Bars are capped below a full cell for the same reason.

**Panel figures sit right-aligned on their label rows**, instrument-panel
convention: track play count, cumulative station time, saved-station count,
channel layout. Every number screech knew used to live in the header rail,
leaving the panel itself numberless. Both counts are fetched off the UI
thread and matched against what's playing when they land, so a slow query
returning after a skip cannot stamp the previous station's figures on the
new one. A first hearing states nothing — "heard 1x" is noise, not a fact.
A live dB readout was considered and rejected: it would be the most
instrument-like figure available and it would twitch twenty times a second
in an interface whose main state is meant to be left alone for hours.

**The chassis has two materials.** A seam runs the full height between the
bays at ~62% of the frame metal, so the faceplate reads as one body with two
compartments rather than two adjacent text columns.

A dithered bevel under the top rail was tried alongside it and removed. Low
contrast is not the same as low visual weight: at 1.34 against the panel it
measured subtle, but shade blocks across a contiguous span form one unbroken
field of colour and the eye reads the shape long before it reads the
contrast. It looked like corruption at the top of the chassis. The seam
carries the engineered-object job on its own, without inventing area that
means nothing — which is the general lesson. Decoration that encodes nothing
still has to earn its area.

**One hero.** The track title is the only Bright+Bold element on the panel.
The station is letterspaced at mid weight as an engraved source plate,
subject to the same 14-character gate the compact hero uses — letterspacing
a long directory name is legible in principle and unreadable in practice.

**The faceplate is height-aware.** It was a fixed ten rows at every terminal
size, which on a tall window left a business card floating in three quarters
of an empty screen — while the library view beside it filled the same
terminal correctly. Surplus height goes to the signal bay first, since the
meter is the one part of the panel that's alive, then to a small amount of
internal padding. Filling the terminal is explicitly *not* the goal: a
receiver stretched down eighty rows would be worse than the original
problem. The panel grows to a substantial size and then centers. The two
bays are composed independently and zipped, so the left bay's blocks
distribute down the panel beside the instruments instead of clumping at the
top with a hole underneath.
Narrow terminals collapse to the v0.4 stacked composition. Meter columns touch
to form a continuous signal silhouette, library selection is a full-row background bar, and
microlabels anchor every data field. Surfaces derive from the accent hue so any
configured color keeps its temperature. The footer exposes live state (`loved`,
`preset 7`, `75%`) rather than acting as a static command legend.

**Color material (v0.4).** The accent is no longer a garnish. Grays derive
from the accent hue (an amber accent warms them to stone, a violet cools
them to lavender — the fixed olive family is gone), and the dim floor is
raised so "quiet" never means illegible.

**Legibility is measured, not eyeballed.** Green carries ~71% of perceived
luminance and red ~21%, so deriving grays by scaling RGB uniformly made the
same nominal lightness three shades darker on the crimson finish than on
amber: dim measured 3.2–4.2 across the finishes, under AA in every one of
them, while carrying every microlabel on screen. Grays are therefore
renormalized to a target luminance after being warmed toward the accent, and
the wave's ramp floor is a luminance target rather than a fixed fraction of
the accent — most of the meter lives at the bottom of that ramp, so the low
step is the majority of what the eye actually sees, and at 35% of a crimson
accent it measured 1.3 against the panel. Daypart dimming scales surfaces
and text at different rates for the same reason: these backgrounds sit in
sRGB's linear toe while text sits up the gamma curve, so an even multiplier
quietly eats contrast. `TestThemeContrastFloors` and
`TestWaveRampIsVisibleAndMonotonic` hold the line at every finish and
daypart. The header readout sits on the
accent while the stream is live; buffering blinks. The dial marker carries
a five-cell ember→accent→ember gradient. A loved track's whole row takes
the faintest accent surface, readable from across the room. The status
line's label carries a category glyph and weight: accent for seed/love,
mid for recall, dim for wildcard. Still one hue — many temperatures.

**Hue is identity, and the clock must not change it.** The daypart temper
applies warmth as a channel push — red up, blue down — which rotates the hue
as a side effect. For most accents that side effect is under two degrees and
reads as warmth. For one already at the red end of the wheel there is
nowhere to rotate but into magenta: midday moved the crimson finish to hue
339, which is pink. The temper is now capped at 6° of drift.

**Red is the hue the ramp rules were not written for.** "Push the top toward
pale" works for amber (gold), green and blue, which stay recognisable when
lightened. Any red desaturates through salmon — even a true blood red tops
out at a dusty rose — and the wave is six rows of ramp top. Slayer therefore
carries a second stop and runs blood into ember, like worked iron. Its
accent is a true red at hue ~1 rather than CSS crimson at 348.

Two-hue ramps hold their brightness climb in *relative luminance*, not HSL
lightness. Lightness is not what the eye reads: rotating from mint toward
blue loses perceived brightness even as lightness rises, because blue
carries 7% of luminance where green carries 71%. Left alone, Lagoon's and
Charm's ramps ran darker at the top, which would make a loud bar look
quieter than a middling one. Height is what the ramp encodes; the second hue
is signature, and signature does not get to invert the reading.

**Theme picker (v0.5).** `t` opens a focused finish selector. Movement previews
the whole interface immediately, Enter persists the selection in local state,
and Escape restores the prior palette. Receiver is the default warm phosphor
finish. Austere removes hue entirely and expresses the same hierarchy through
black, white, and luminance alone. Verdant, Azure, Violet, and Slayer are fixed
receiver finishes in other hues; every gray, surface, and ramp derives from
the accent, so the faceplate reads the same in any color. Charm, Lagoon and
Slayer carry a two-hue signature: the ramp top blends into a second hue
instead of pale gold, and the header rule runs the full gradient. Note that
the baked ramp array has to be rebuilt after a second stop is assigned —
the wave reads that array, not the live color function, so for a while the
two-hue finishes had a single-hue meter and nobody noticed. Theme changes never
affect listening state. The chosen finish tempers with the daypart — brighter
and cooler midday, dimmer and warmer at night; austere takes the lightness
shift only, and picker previews render under the current daypart.

**Library views.** `h` opens recent tracks; `j`/`k` (and page keys) scroll the
hundred most recent — they used to load and then show only what fit. `H`
opens a unified library on the loved view; Tab cycles recent, loved,
saved stations, and a taste view. Loved tracks are
deduplicated, searchable across artist/title/origin station, and
keyboard-selectable. The selected track can seed more music from its artist,
return to its origin station, or be removed with a guarded action.

The **saved-stations view** is the full two-tier station list: presets
plus every station with loved tracks, unified and sorted most-listened
first. A preset's slot sits left of its name like a channel number; the
right side is aligned columns of listen time and love count. The slot used
to trail the love count on the right, where `♥1 1` read as one number and
the ASCII form `<31` read as "less than 31". It's unbounded — presets stay at nine for
instant digit recall, but the library holds every station you've ever
marked. `/` filters by name, `j`/`k` select, `enter` tunes, digits jump to a
preset, and a guarded `x` removes the station from recall (preset slot and
loved rows) while leaving listen history and the taste model untouched.
Radio metadata is recall and discovery context, never presented as an
on-demand replay URL. Esc clears an active filter first, then closes —
in both filterable views.

The **taste view** reads the record back at the listener: all-time and
seven-day audible time, an audible-time histogram across the four
dayparts, the artists heard most in the last 90 days (only titles caught
inside a listen that ran at least the fast-skip window, or the one still
open — a ten-second skip is not taste), the tag affinities still above
prior after decay, the most-listened stations of the last 30 days, and
the loved-track count.

**The wave (honest amplitude, synthetic texture).** Touching braille cells,
the top block of rows the left channel and the bottom block the right, with
a vertical gradient from a visible ember base through the accent to a pale
peak and peak-hold ticks that cool through the ramp as they fall. Bars are
bass-weighted, so per-channel levels still read as a spectrum, not a uniform
bounce; the tilt is a lean (1.0 → 0.74), not a cliff, or the right of the
bay goes structurally dead. Amplitude is real (mpv astats per-channel RMS +
peak over IPC ~20Hz, mono mixdown fallback); texture is synthetic. The
filter is labelled (`--af=@meter:lavfi=[astats…]`) and read as
`af-metadata/meter/lavfi.astats.*`: an unlabelled path is an error on mpv
0.41, and an observed property that errors simply never fires, which left
the wave on its fallback unnoticed until 0.9. If level data stops (other
backends), it falls back to self-animated breathing after 3s. Real FFT arrives with Path 2's pure-Go audio; the renderer already takes
a `[]float64` either way.

**Resolution is per-row, and one row is not enough.** A braille cell's eight
dots are four rows of two columns, so a single row of braille resolves four
heights, not eight. At four levels the meter was effectively binary across
the whole useful loudness range and read as static rather than signal. Each
channel therefore spans up to three stacked cells — twelve levels — sized by
the height available. The bay never grows past that: a meter needs headroom
above typical program level, and past twelve levels that headroom is a whole
terminal row sitting permanently empty inside a drawn frame.

**The meter is calibrated to broadcast, not to the format.** dBFS maps over
−36 to −3, the range real streams occupy, rather than the format's full
−48 to 0. Mapping the format wasted most of the scale on levels nothing ever
sends, so a quiet ambient set and a loud master landed within a few percent
of each other. Typical streaming loudness (≈ −14 dBFS) should sit around
two-thirds up the bay with room above for peaks.

**Set piece: tuning (the signature moment).** A band line under the header
(`──────╂──────`). On next: accent marker springs to the new station's position
(overshoot, settle — harmonica), wave flatlines to `▁▁▁`, old station name
dissolves through random glyphs resolving left-to-right into the new name
(decrypt effect). Wave swells when the stream locks. No generic spinner exists
anywhere in the app. A retune opens with a bounded static collapse in the
wave bay — dither noise fizzling into the ember baseline — and while the
needle travels, the dial's warm bleed becomes a detune smear that clears when
the spring settles on the lock.

**Set piece: love.** One-frame inverse flash on ♥, holds coral ~2s while the row
cools from a coral flash into the permanent love wash, then a persistent dim
♥. The dial marker pulses coral once and the footer's L chip keeps the coral
while loved. Structured feedback (`LOVED  Track and station`) lands the
same frame as the keypress, always; never block render on network.

**Idle (the main state).** After ~3 min without keys: everything dims except track
title and wave; the accent marker breathes (slow luminance sine, ~6s period). Any
key snaps bright. Natural track changes step the new title dim → mid → bright.
Marquee etiquette for long titles: pause 2s, scroll once, pause, then rest on
the head of the title with an ellipsis — no endless loops. (For a while it
did loop, every few seconds for as long as the track played, in an
interface meant to be left alone for hours.) Past 5 min without any activity the chrome — wordmark, frame
metal, key strip, microlabels — eases to an ember floor over ~30s; the wave,
dial marker, and LIVE readout keep living, and any key, tune, or player state
event restores the room instantly.

**Boot.** ~600ms: rules draw outward from center, wordmark letterspaces in, then
content. Skippable on any key — the key still lands.

**Reduced motion.** `--reduced-motion` (or `SCREECH_REDUCED_MOTION`) is a
real mode, not a single switch: no boot sequence, no decrypt scramble, no
static collapse, no scrolling titles, no blinking (buffering, cursors), no
idle breathing, no love flash, and the dial needle lands on the new station
instead of springing to it. Everything still changes state on the same
frame; it just stops moving to get there. The decrypt, when it does run,
resolves into exactly the text that stays on screen — it used to resolve
into the plain name and then snap to the letterspaced plate a frame later.

**Startup notices survive startup.** A message the user must see once (a
damaged database moved aside) holds the readout until the first key. It
used to share the transient note slot, and the zero-press resume cleared it
within milliseconds of launch, so nobody ever saw it.

**Texture.** Mid-contrast readout cluster top-right: `AAC  128K  2:14:06`.
Fixed-width clock formatting and uppercase codec labels provide
instrument-panel credibility. The command legend sits two rows below the
status line, keeping the whole display a single 12–14-line object.

**Reality.** One `tea.Tick` heartbeat (15–20fps); every animation is a pure
function of elapsed time; no per-effect goroutines. Fixed zone heights so frames
never reflow. All truncation through go-runewidth, ellipsis, never wrap.
Degradation ladder, in the order things are shed:

- **Header.** The subtitle goes first (`PERSONAL RADIO`, or `SYNCING
  DIRECTORY…` while a refresh runs — background work lives there, so it
  never hides playback state), then the codec/bitrate/clock readout, then
  the `BREAK` tag, then the wordmark. The state word — `LIVE`, `OFFLINE`,
  `BUFFERING`, `RECONNECTING`, `TUNING`, `STOPPED` — is the last thing
  standing. It used to be the first: a narrow window dropped the whole right
  side at once.
- **Key strip.** Separators tighten, then navigation labels drop (`quit`,
  `theme`, `library`, `discover`, `skip`) before state labels (`loved`,
  `preset 7`, `75%`), and bare keys come last. The full strip is 95 columns,
  so an 80-column window used to get nothing but `SPACE L F / H V T Q`. The
  strip also follows state: offline, `SPACE` reads `retry`; once the player
  has stopped for good, only the keys that still do something remain.
- **Body.** Below 78 columns, or when the height can't hold every row of
  the faceplate, it collapses to the stacked composition; below 28 inner
  columns the stacked wave and band go; at the narrowest, rows are clipped
  rather than wrapped.

"Never break" is enforced, not hoped for: `TestViewFitsEveryTerminalSize`
renders every overlay — boot and reduced motion included — at every width
1–30 and height 1–16 (sampled beyond) and fails on any panic, overflowing
row, or a frame whose bottom edge went missing. There is no minimum terminal
size at startup.
`ascii = true` in TOML swaps every fancy glyph for ASCII (bad SSH insurance),
and `TestASCIIModeIsSevenBit` holds it to that: in ASCII mode every rendered
cell is 7-bit. Before that test, a multiplication sign, a few hard-coded
ellipses and a track display that split on the Unicode dot had all leaked
through — the last one made the faceplate print the whole track as the title
and `NOW PLAYING` as the artist.

## Config

One TOML at `os.UserConfigDir()/screech/config.toml`, created with commented
defaults on first run: `accent`, `ascii`, `mpv_path`, `data_dir`, `auto_hop_ads`,
`sync_limit`, `ytdlp_path` (empty: find yt-dlp; `"off"`: never use it), and
commented examples of `[[stations]]` and a discovery station. `data_dir` expands
`~` and resolves relative paths against the config directory (a
double-click launch has a different CWD than a shell).
Bad values never stop the radio: unknown keys, a malformed accent, and an
out-of-range `sync_limit` (1–50,000) are repaired with a warning in the log
and in `screech doctor`; only invalid TOML is fatal.

`[[stations]]` tables (see Station data) take `name` and exactly one of `url`
or `discover` (required), and `tags` (a string or a list), `codec`, `bitrate`,
`country`, `homepage`. `discover` is a phrase or a YouTube link that starts a
discovery station, and it takes the place of `url`.
They are decoded in a second pass of their own, field by field, because
decoding them into the config struct would turn one wrong type into a fatal
load. Every text field goes through `core.CleanText`; tags are lowercased
and comma-joined; the url must pass the player's own rule (http or https,
with a host). A table missing its name, with neither or both of `url` and
`discover`, with a link that fails the rule, or repeating an earlier table's
link (by `custom:` id) is skipped, and
an unknown key or a wrongly typed field is dropped, each with a warning that
names the table. A top-level setting written below a `[[stations]]` header
belongs to that table in TOML; its warning says so and where to move it,
rather than calling it unknown. A page link with yt-dlp off or not found loads
but is warned about, since it can't play. `screech doctor` reports how many
stations the config carries and how many of them are discovery stations, and
checks yt-dlp (exit bit 32 when it won't run) and Bandcamp's discover feed
(exit bit 64 when it doesn't answer).

## Stack

bubbletea + lipgloss + harmonica · hand-rolled mpv IPC client (unix socket +
windows named pipe) · hand-rolled radio-browser client · modernc.org/sqlite ·
go-runewidth · BurntSushi/toml. No bubbles/list in v1 (nothing to list).
The Windows window: Wails v2.16.0 (builds with plain `go build` and
`-tags desktop,production`; no Wails CLI, no Node, no cgo), a plain-JS front
end, Antonio (OFL) as its font. Daemon later: net/http stdlib; phone page:
htmx.

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

A Wails desktop app reusing core unchanged: the Windows window (`cmd/screech-gui`,
v0.9) is its first step, and it keeps mpv. What stays Path 2: mpv → pure-Go audio
(oto + decoders) or webview `<audio>`. Daemon optional. ICY reading in-process.
Real FFT feeds the same wave renderer. Known tradeoff: without the daemon,
multi-device state doesn't sync (acceptable; SQLite sync or optional daemon later).
