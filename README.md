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

  [SPACE] skip  [L] love  [F] preset 7  [/] discover  [H] library  [V] 75%  [T] theme
```

From 78 columns up (a standard 80×24 window included) Screech becomes a
receiver faceplate: the music owns the left bay, while the live signal and
station-memory dial occupy the right. The amber marker is the station
playing, the small ticks are saved presets, and the raised readout always
explains why the station was picked. Narrower or shorter windows collapse to
a compact stacked layout.

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
go run ./cmd/screech   # from source (Go 1.26+)
```

Prebuilt binaries for Windows, macOS, and Linux are attached to each tagged
release. Building writes to `dist/`, which is not tracked.

```
screech                   start the radio (default)
screech doctor            read-only checkup of config, database, mpv, IPC, network
screech --version         print the version
screech --help            usage
screech --reduced-motion  calm the animations (or set SCREECH_REDUCED_MOTION=1)
```

Reduced motion turns off the boot sequence, the station-name scramble, the
tuning static, scrolling titles, blinking, the idle breathing, and the love
flash; the dial needle lands instead of springing. Nothing else changes.

Only one screech runs per data directory; a second launch says so and exits
without touching the first one's log.

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
the player in five-percent steps without changing system volume; `-`/`+`
(or `[`/`]`) also work straight from the main screen. The level is
remembered across launches. The slider tucks itself away after a few
seconds; `v`, enter, or esc closes it sooner, and any other key closes it
and does its usual job.

**p** — pause and resume. It's mpv's own pause underneath, so keyboard
media keys, headset buttons, and the OS media flyout all drive the same
hold. A pause under 30 seconds resumes from the buffer a few beats behind
live, like pausing live TV; a longer one reloads and rejoins the live edge.
Pausing ends the listen, so paused time neither teaches nor counts as
listening. The flyout's Stop does the same pause, and its Next/Previous
buttons do nothing — there's no playlist to walk.

**z** — sleep timer. Each press steps 15, 30, 45, 60, 90 minutes, then
off; the footer counts it down. Over the last 30 seconds the volume eases
toward silence — your saved level is untouched — and when the timer lands
the radio pauses itself.

**t** — open the theme picker. Move with arrows or `j`/`k` (or press a
digit) to preview a finish live, Enter applies and remembers it, and Escape
restores the previous theme. Receiver is the warm phosphor default and
Austere is strict monochrome; Verdant, Azure, Violet, and Slayer are the
same faceplate in other hues, and Charm and Lagoon carry a two-hue
gradient. Every finish tempers with the time of day.

**h** — recently heard tracks, newest first; `j`/`k` scroll. Loved tracks
carry an ember heart.

**H** — open the library, starting with loved tracks. `tab` moves through
recent tracks, loved tracks, and saved stations. In the loved view, `/`
searches across artist, title, and origin station; arrows or `j`/`k` select;
`enter` asks screech for more from that artist; `r` returns to the station
that played it; and pressing `x` twice removes it.

The saved-stations view is the full two-tier station list: your presets
(their slot number sits in front of the name, like a channel) plus every
station you've loved a track on, most-listened first, unbounded. `/`
searches by name, `j`/`k` select, `enter` tunes, digits jump straight to a
preset, and pressing `x` twice removes the station from recall — clearing
its preset slot and its loves while leaving listen history and the taste
model alone. In both searchable views, esc clears the search before it
closes the library.

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
suspicion and discounts your skips, and it never hops on its own. The
`auto_hop_ads` config key is a placeholder for that behavior and currently
does nothing.

The wave under the station name moves with the stream's real loudness,
measured by mpv and reported over the same pipe that carries titles. Texture
is synthetic; amplitude is true.

## When the signal drops

Internet radio drops constantly, and none of it should need you.

- A station that was playing and loses its stream (the server hangs up, or
  the buffer stalls for 20 seconds) is reconnected in place: same station,
  same listen, `RECONNECTING` in the header. Only a station that drops
  more than three times without a steady minute in between gets a strike
  and is left behind.
- A station that never locks (12 seconds of nothing, or an outright error)
  gets one strike and screech moves on. Three strikes benches it until a
  directory refresh vouches for it again.
- When three stations in a row fail, the problem is the network, not the
  stations: screech takes back the strikes it just handed out, shows
  `OFFLINE`, and retries on its own (5s, 10s, 20s… up to a minute),
  alternating between the last station that played and a fresh one.
  `SPACE` tries immediately. The first stream that locks clears it.
- If mpv itself crashes, screech restarts it and picks the station back up,
  up to three times in five minutes before it asks you to restart.
- If screech is killed outright, the mpv it started goes with it on Windows
  and Linux, instead of playing on with nothing to stop it.

## Config

`%AppData%\screech\config.toml` on Windows,
`~/Library/Application Support/screech/config.toml` on macOS, and
`~/.config/screech/config.toml` on Linux. First launch writes it with
commented defaults covering the accent color, ASCII fallback, mpv path, data
directory, ad hopping, and directory cache size (`sync_limit`, at most
50,000). `data_dir` accepts `~` and paths relative to the config file.

A typo never stops the radio: unknown keys, a malformed accent, or an
out-of-range `sync_limit` are repaired or ignored, and each one is listed
in the log and by `screech doctor`. A config that isn't valid TOML is the
one thing that does stop startup, with the line number.

The theme picker and volume are remembered in the local SQLite file.
Everything Screech learns lives there too.

## Your data

Everything stays on your machine: `config.toml`, `screech.db` (stations,
listens, loved tracks, the taste model), and `screech.log` (the last run
only). The network traffic is the streams you listen to and the
radio-browser.info directory: the weekly catalog refresh, `/` searches, and
the click report their etiquette asks for when a station starts. Nothing
else, and no telemetry.

To start over, quit and delete the data directory. If the database is ever
damaged, screech moves it aside as `screech.db.corrupt.<timestamp>`, starts
fresh, and says so on the status line until you press a key.

Station names and track titles come from strangers' servers, so screech
strips terminal control sequences and layout-breaking characters before it
displays or stores them.

## Building

```
go build -o dist/screech ./cmd/screech
```

Tagged releases are built for Windows, macOS, and Linux by the release
workflow. SQLite is pure Go, so cross-compilation does not require CGO.

## Troubleshooting

Run `screech doctor` for dependency, database, IPC, and network checks. It
changes nothing on disk (the mpv handshake runs in a throwaway temp
directory), reports files a fresh install hasn't created yet as `--` rather
than failures, and exits with a bitmask of what failed: 1 config, 2
database, 4 mpv, 8 IPC, 16 network.

Set `ascii = true` if terminal glyphs render poorly; everything on screen is
then plain 7-bit text. Small windows are fine: the layout sheds detail step
by step, down to a few columns, rather than refusing to start. The header
keeps the playback state (`LIVE`, `OFFLINE`, `BUFFERING`) to the last, and
the key strip drops its quieter labels before the keys themselves.
The last-run log lives next to the config file.

## Design

[SPEC.md](SPEC.md) holds the full interaction, algorithm, and visual design.
