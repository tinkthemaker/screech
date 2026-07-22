# SCREECH

A terminal radio that learns what you like. Point it at the world's internet
radio stations, press one key when you want something else, another when you
love what's playing. It figures out the rest. No accounts, no cloud, no
playlists, no telemetry. One binary, one config file, one database, all yours.

```
  SCREECH  PERSONAL RADIO                            ● LIVE  AAC · 128K · 2:14:06
  ▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔

  ╭── RECEIVER / NOW PLAYING ──────────────────────────────────────────────────╮
  │ TRACK                              SIGNAL                                  │
  │ Billions and Billions              ▂ ▄ ▆ █ ▇ ▅ ▃ ▂  ▂ ▅ ▇ ▆ ▄ ▃           │
  │ Stellardrone                       ▁ ▂ ▄ ▆ ▅ ▃ ▂ ▁  ▁ ▃ ▆ ▅ ▃ ▂           │
  │                                    LOW                                HIGH │
  │ BROADCAST                          STATION MEMORY                          │
  │ SPACE STATION SOMA                 ───┴────────╂────────────┴───────────── │
  │                                    PRESET 7 · SAVED                       │
  │ WHY THIS STATION  Plays 3 artists you love                               │
  ╰────────────────────────────────────────────────────────────────────────────╯

  [SPACE] skip  [L] love  [F] preset 7  [/] discover  [H] library  [V] 75%
```

On wide terminals Screech becomes a receiver faceplate: the music owns the
left bay, while the live signal and station-memory dial occupy the right. The
amber marker is the station playing, the small ticks are saved presets, and
the raised readout always explains why the station was picked. Narrow windows
collapse back to a compact stacked layout.

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

First launch starts playing immediately from the built-in stations while the
radio-browser.info directory fills in behind it. If the directory is
unreachable, those stations (SomaFM, Radio Paradise, WFMU, KEXP, NTS, WWOZ)
keep Screech useful offline. Every later launch resumes where you left off,
zero presses.

The catalog stays alive on its own. Once a week (in the background, playback
never waits) screech re-pulls the top slice: new stations arrive, stations
the directory stopped vouching for get pruned unless you have history with
them, and stations that failed for you get a strike forgiven when the
directory says they're healthy again. Presets, loved stations, and anything
you've listened to are never pruned.

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
suspects a break, the status line shows: `BREAK  SPACE skips without penalty`.

**l** — love the current track. Strong positive on the artist, the station,
and its tags. The loudest signal you can send; use it honestly. It's a
toggle: press again on the same track (or, with no parsed title, the same
station) to take the love back — screech returns exactly the boost it gave
and the heart goes out.

**f** — save the current station to a preset, or unsave it. Presets are pure
recall and never touch the taste model. Love teaches, presets remember.

**1–9** — jump straight back to a saved preset, deterministically. Saved
stations show as ticks on the dial line. Nine slots stay nine: they're
muscle memory, not a list. Everything past that lives in the library.

**v** — open Screech's volume slider. Left/right arrows or `-`/`+` adjust
the player in five-percent steps without changing system volume. The level
is remembered across launches; `v`, enter, or esc closes the slider.

**h** — recently heard tracks, newest first. Loved tracks carry an ember
heart.

**H** — open the library, starting with loved tracks. `tab` moves through
recent tracks, loved tracks, and saved stations. In the loved view, `/`
searches across artist, title, and origin station; arrows or `j`/`k` select;
`enter` asks screech for more from that artist; `r` returns to the station
that played it; and pressing `x` twice removes it.

The saved-stations view is the full two-tier station list: your presets
(marked with their slot number) plus every station you've loved a track on,
most-listened first, unbounded. `/` searches by name, `j`/`k` select,
`enter` tunes, digits jump straight to a preset, and pressing `x` twice
removes the station from recall — clearing its preset slot and its loves
while leaving listen history and the taste model alone.

**/** — seed the radio. Type a genre and screech tunes a matching station
instantly: a tag in the local cache plays immediately, and a tag the cache
doesn't carry (regional genres like "corridos" often aren't in the top-20k
global slice) is searched against the directory, cached, and tuned. Type an
artist and it pseudo-loves the name so fingerprints catch them as you
listen, tunes any station actually named for them, and — when none exists,
which is the common case — falls back to a small curated artist→genre map
so you still land in the right musical family. The reason line always says
which fired: `seeded: corridos` vs `seeded genre: corridos`. Typing
"artist - song" uses the artist half. Genre seeds deliberately echo through
the next four picks, then fall back to the ordinary decaying taste signal.

**q** — quit.

## What it's doing underneath

Every station carries Beta(α, β) counts split by daypart (morning, day,
evening, night). Listening bumps α, capped so falling asleep to a stream
can't swamp anything. Fast skips bump β. Streams that fail never touch the
taste model — a dead stream isn't a bad station, it just gets a strike, and
three strikes benches it. When you press next, a Thompson sample from each
candidate picks the winner: uncertainty does the explore/exploit balancing
and there are no knobs to tune.

Every track title that comes down the stream is parsed and logged (about a
third of stations never send usable titles — fingerprints are one signal,
never a gate). Stations that play artists you love surface through
rarity-weighted overlap: sharing an obscure artist means far more than
sharing a chart-topper. All counts decay with a roughly three-week
half-life, so your taste can drift and the model follows. The status line
always explains the pick: `plays 3 artists you love`, `tag: ambient`,
`seeded: jungle`, `wildcard`.

Ad avoidance is mostly curation. Commercial simulcast networks get downranked
before you ever hear them; college, community, and listener-supported streams
get upranked. Title-based break detection exists but stays humble: it flags
suspicion, discounts your skips, and never auto-hops unless you enable that
in the config.

The wave under the station name moves with the stream's real loudness,
measured by mpv and reported over the same pipe that carries titles. Texture
is synthetic; amplitude is true.

## Config

`%AppData%\screech\config.toml` on Windows and
`~/.config/screech/config.toml` elsewhere. The commented defaults cover the
accent color, ASCII fallback, mpv path, data directory, ad hopping, and
directory cache size. Everything Screech learns lives in one SQLite file.

## Building

```
go build -o dist/screech ./cmd/screech
```

Tagged releases are built for Windows, macOS, and Linux by the release
workflow. SQLite is pure Go, so cross-compilation does not require CGO.

## Troubleshooting

Run `screech doctor` for dependency, database, IPC, and network checks.
Set `ascii = true` if terminal glyphs render poorly. The last-run log lives
next to the config file.

## Design

[SPEC.md](SPEC.md) holds the full interaction, algorithm, and visual design.
