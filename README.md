# SCREECH

A terminal radio that learns what you like. Point it at the world's internet
radio stations, press one key when you want something else, another when you
love what's playing. It figures out the rest. No accounts, no cloud, no
playlists, no telemetry. One binary, one config file, one database, all yours.

```
  S C R E E C H                          aac · 128k · 2h14m
  ▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔

  S P A C E   S T A T I O N   S O M A

  Stellardrone · Billions and Billions                 ♥

  ▂▃▅▇▆▃▂▁▂▄▆▇▅▃▁▂▃▅▆▄▂▃▄▆▅▃▂▁▂▃▄▅▄▃▂▁▂▃▄▂
  ───┴──────────╂──────────┴─────────────┴────────────
  plays 3 artists you love

  space next · l love · f save · / seed · q quit
```

The amber marker is the station playing. The small ticks are your saved
presets. The line under the band always tells you why this station was
picked.

## Needs

- **mpv** on your PATH. It does all the audio heavy lifting: codecs, ICY
  metadata, reconnects.
  - Windows: `scoop install mpv` or `choco install mpv` or [mpv.io](https://mpv.io/installation/)
  - macOS: `brew install mpv`
  - Linux: `apt install mpv` (or dnf/pacman)
- A terminal with truecolor if you want the amber to glow. Windows Terminal,
  wezterm, kitty, and alacritty all qualify.

## Run

```
screech.exe            # prebuilt, in dist/
go run ./cmd/screech   # or from source (Go 1.23+)
```

First launch syncs the station directory from radio-browser.info (a few
seconds, ~4000 stations) and opens on the seed prompt: type a genre or
artist, or press space to let it wander. If the directory is unreachable it
falls back to a built-in seed list (SomaFM, Radio Paradise, WFMU, KEXP, NTS,
WWOZ) and works anyway. Every launch after that resumes where you left off,
zero presses.

Something wrong? `screech doctor` checks each dependency in turn (config,
database, mpv, the IPC handshake, the directory) and reports what it finds.
Every run also writes a log next to the config file, and startup errors hold
the window open instead of vanishing with the message.

## Controls

**space** (or `n`) — next station. This is also your skip button: hit it the
moment ads or talk start. Pressed fast (under 90 seconds) it counts against
the station; pressed after a long listen it's just a variety request, since
the listen already banked its credit. Skips during a suspected ad break are
discounted, so escaping ads never punishes a good station. When screech
suspects a break, the status line reminds you: `break? · space hops away`.

**l** — love the current track. Strong positive on the artist, the station,
and its tags. The loudest signal you can send; use it honestly.

**f** — save the current station to a preset, or unsave it. Presets are pure
recall and never touch the taste model. Love teaches, presets remember.

**1–9** — jump straight back to a saved preset, deterministically. Saved
stations show as ticks on the dial line.

**/** — seed the radio. Type a genre and screech tunes a matching station
instantly (tag data exists for every cached station). Type an artist and it
pseudo-loves the name, searches the directory for stations built around them,
and gets sharper as it hears more music. Typing "artist - song" uses the
artist half. Seeds tilt every future pick, then decay like all other signals,
so your actual listening keeps overwriting them.

**q** — quit.

## What it's doing underneath

Every station carries Beta(α, β) counts split by daypart (morning, day,
evening, night). Listening bumps α, capped so falling asleep to a stream
can't swamp anything. Fast skips bump β. When you press next, a Thompson
sample from each candidate picks the winner: uncertainty does the
explore/exploit balancing and there are no knobs to tune.

Every track title that comes down the stream is parsed and logged. Stations
that play artists you love surface through rarity-weighted overlap: sharing
an obscure artist means far more than sharing a chart-topper. All counts
decay with a roughly three-week half-life, so your taste can drift and the
model follows. The status line always explains the pick: `plays 3 artists
you love`, `tag: ambient`, `seeded: jungle`, `wildcard`.

Ad avoidance is mostly curation. Commercial simulcast networks get downranked
before you ever hear them; college, community, and listener-supported streams
get upranked. Title-based break detection exists but stays humble: it flags
suspicion, discounts your skips, and never auto-hops unless you enable that
in the config.

The wave under the station name moves with the stream's real loudness,
measured by mpv and reported over the same pipe that carries titles. Texture
is synthetic, amplitude is true. A full per-band spectrum needs the future
pure-Go audio path; the renderer is already built for it.

## Config

`%AppData%\screech\config.toml` on Windows, `~/.config/screech/config.toml`
elsewhere. Created with commented defaults on first run:

- `accent` — the one color. Phosphor amber by default.
- `ascii` — swap every fancy glyph for 7-bit ASCII (bad SSH insurance).
- `mpv_path` — if mpv lives somewhere odd.
- `data_dir` — where the database goes. Empty means next to the config.
- `auto_hop_ads` — auto-tune away from suspected breaks. Off by default
  because detection is conservative and DJs legitimately trip it.

Everything screech knows lives in one SQLite file beside the config: listens,
tracks heard, loves, presets, station cache. Delete it and screech forgets
you. Its only outbound calls are stream audio, directory syncs, and the
radio-browser click endpoint (their requested etiquette when you tune in).

## Building

```
go build -o dist/screech ./cmd/screech                     # this platform
GOOS=windows GOARCH=amd64 go build -o dist/screech.exe ./cmd/screech
```

Pure Go all the way down, including SQLite, so cross-compiling just works.

## Troubleshooting

- Crashes or won't start: run `screech doctor`, read the verdicts.
- Installed mpv but it's "not found": PATH refreshes only in new terminals.
- Glyphs look wrong over SSH or in an odd font: set `ascii = true`.
- Wave feels lifeless: your mpv may lack the lavfi astats filter; the wave
  falls back to self-animation and everything else keeps working.
- The log of the last run sits next to the config file.

## Design

[SPEC.md](SPEC.md) holds the full design: the algorithm math, the visual
language, and the roadmap. Next stops on it: a homelab daemon that relays
streams for browser clients, a thumb-sized phone page over Tailscale, and
eventually a desktop shell reusing this same core with real FFT.
