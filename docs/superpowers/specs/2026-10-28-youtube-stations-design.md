# Discovery stations and the FIND front door

Status: approved by the owner on 2026-10-28. It builds on `main` at
`4a94019` (0.9.0, untagged). Mockups are in `.superpowers/brainstorm/1043-1793244117/content/`;
the approved one is `front-door-v6.html`.

## Why

The owner wants to type a genre, an artist or a song, or paste a YouTube
link, and hear a station that **keeps finding new and niche music in that
sound**, with a well-known track now and then. Today they get this through
a pilot outside the app (`E:\Projects\screech-pilot\restream.py`). The pilot
is a separate Python process that needs ffmpeg and a local server, and it
plays only the seeds it was given.

Inside Screech, a YouTube link station is one video on repeat. When the
video ends, the session reads mpv's end of file as a dropped radio stream
and reconnects to the same link. yt-dlp isn't found on the owner's machine:
the config says `ytdlp_path = "yt-dlp"`, and none is on PATH.

The goal: from one bar in either face, start a **discovery station** that
plays song after song from YouTube and Bandcamp. It mixes discoveries (bands
you haven't heard, smaller and newer) with occasional anchors (the genre's
well-known tracks), shows real titles, and learns like any station. The
pilot retires. The owner wants to listen at work on 2026-10-29.

## Goals

1. **A FIND front door** in the window and the terminal. One bar starts a
   discovery station from a phrase or a YouTube link. Your recent discovery
   stations sit under it, and radio search lives one tab over.
2. **Discovery stations.** Each is an endless queue that mixes discoveries
   and anchors from YouTube Music and Bandcamp genre tags. Songs don't
   repeat on the same station, and the same band never plays twice in a
   row.
3. **NEXT means next song** on a discovery station. A second NEXT within
   3 s changes station.
4. **yt-dlp just works.** It's found without setup, given a JS runtime when
   one exists, run one process at a time in a fixed working folder, and
   checked by `screech doctor`.
5. **Clean titles.** Packaging such as "(Official Audio)" and "ft. …" is
   stripped.
6. **The window gets a close button** in its top-right corner, folded or
   not.

## Non-goals

- Accounts, sign-in, cookies, downloads, ffmpeg, or a local restream
  server.
- Screech downloading or auto-updating yt-dlp (doctor says when to run
  `yt-dlp -U`).
- Playing a loved track again from the library (track-level library
  playback).
- Lyrics, album art, or a "now playing" history view beyond what exists.
- A new picker for radio stations. Discovery stations reach the radio
  picker only as the listener's own stations already do (the 4-seat slice).

## Anti-goals (failure even if it "works")

- Radio streams failing slower than today. mpv keeps `--ytdl=no`, and radio
  URLs never touch yt-dlp.
- Bursts of yt-dlp processes. A reconnect storm got the pilot throttled by
  YouTube.
- A signed media URL in the database, the config or the log.
- An overcrowded UI. The front door is one bar, one short list and one
  faint hint. No explainer text.
- Unit tests that run yt-dlp, call Bandcamp, or touch the network.
- A square black notch anywhere in the window's frame. Every inner corner
  is rounded the way `lcars.css` draws them, with the `.ic` square under a
  black quarter-round.

## What the listener sees

### Window (`screech-gui`), matching `front-door-v6.html`

**Strip, folded and unfolded:**
- **Close button.** The top bar's right segment becomes a red `✕` cap at
  `432–480 × 0–20`, radius `0 10px 10px 0`, colour `#FF5555`, with black
  text. It quits like Q. The tan segment widens to `332–428`.
- **Button row:** PAUSE, NEXT, LOVE, then MORE/LESS at `376–480` with a
  rounded right end. EXIT is gone; the ✕ cap replaces it.
- **Discovery status.** On a discovery station the track row shows the
  song, and the status shows `NEW TO YOU` when the band has never been
  heard on Screech before. The reason line reads `discovering · <phrase>`.

**Sidebar tabs**, in order: **FIND** (top, selected by default), **RADIO**,
RECENT, LOVED, STATIONS, TASTE, **MORE**. The tab geometry is unchanged:
seven 28-px slots from y 156.
- **MORE** holds what INFO and SLEEP held: the info rows, plus a SLEEP pill
  that cycles the timer and shows `SLEEP nn`.
- **MORE (the button)** opens the console on FIND, and so does `/`. `H`
  still opens LOVED, and `I` opens MORE.

**FIND view:**
- **The bar** at `136–464 × 148–184` (36 tall, radius 18), in three parts:
  - a left cap `FIND`, orange, 52 wide;
  - a black field with a 2-px orange border;
  - a right cap `GO ▸`, orange, 52 wide, the **same colour as FIND**.

  The placeholder, in lilac at 15 px, reads `Artist, song, genre or YouTube
  link`, and it must fit inside the field (the layout guard checks it).
  Typed text is ice at 16 px. A long link scrolls inside the field.
- **Starting a station.** Enter or GO starts a discovery station from what
  was typed, folds the console, and the strip goes to `◌ TUNING`.
- **Pick up where you left off.** A label at y 204 (peach, 11 px) heads up
  to four rows from y 220, each 28 tall. They are your most recent
  discovery stations: the phrase, or the song a link started from, with a
  relative time on the right. A click, or ↓ then Enter, resumes it. With
  none yet, the label and rows are absent.
- **The hint**, centred at y 348, grey `#777`, 11 px: `ESC TO CLOSE · TAB
  FOR RADIO`, with the key names in ice.
- **No heading**, and no other text.

**RADIO view:**
- **The bar** is the same, in blue, with a left cap reading `RADIO` and the
  placeholder `Station, genre or country`.
- **Results.** Enter tunes the best radio-browser match, which is today's
  search (`Seed`). Below the bar, the rows are your most-listened radio
  stations, laid out as in STATIONS, and each tunes with a click or Enter.
  *Built this way tonight:* a full results list would need an async
  radio-browser search, because nothing slow may run on a Wails call
  path. That list is a follow-up.

**First launch:** with no history at all, the window opens unfolded on
FIND. Otherwise it starts as today, folded and playing. *My call.*

### Terminal (`screech`)

- **`/` opens a FIND overlay**, the same model as the window in the
  terminal's own style:
  - a bar line `FIND ▸ ` with the placeholder `artist, song, genre or
    YouTube link`;
  - a `pick up where you left off` list under it;
  - a faint hint line `esc close · tab radio`.
- **Tab switches the overlay to RADIO**, which holds today's
  radio-browser search.
- **Everything else** follows the existing overlay rules
  (`TestViewFitsEveryTerminalSize`, ASCII mode, `m.th.G` glyphs).
- **The key strip** gains nothing new. `/` already reads `discover`.

### Command line and config

- **`screech add --discover "swedish death metal"`** adds a discovery
  station to `config.toml` (`[[stations]] name = …, discover = "…"`). A
  YouTube link given to plain `screech add` becomes a discovery station
  seeded from that link.
- **The config accepts `discover = "<phrase or YouTube link>"`** as a third
  source beside `url`. Exactly one of the two may be set.
- **`screech doctor`** reports:
  - the yt-dlp it found, where, and its version;
  - the JS runtime it will hand yt-dlp;
  - whether Bandcamp discovery answered;
  - how many discovery stations are configured.

  It suggests `yt-dlp -U` when the release is over 60 days old.

## Behaviour

### Discovery stations

- **Identity:** `disc:` plus a hash of `"discover:" + lower(trimmed
  phrase)`, or of the canonical video URL for a link. It is a namespace of
  its own: startup deletes `custom:` rows that are missing from the
  config, and FIND-started stations are history, not config. `PruneStale`,
  `NeedsSync` and the click report treat `disc:` like `custom:`. Starting the same
  phrase again resumes the same station and its history.
- **Where they live.** Stations started from FIND live in the database
  (they are history, not config). The FIND list is the most recent of them.
  Pressing F (preset), or `screech add --discover`, keeps one for good.
  Config-defined ones are upserted at every launch, as links are today.
- **Seen by the picker** as the listener's own stations: in the 4-seat
  custom slice, never charged a skip for a song change.
- **The station's `StreamURL`** is a sentinel only the player resolves (for
  example `discover:<id>`). `Play` keeps rejecting every other non-http(s)
  URL.

### Where songs come from

The queue mixes **discoveries** and **anchors**. It starts at about one
anchor in five, and it adapts:
- **Quick NEXTs** (three skips inside 60 s of play each) raise the anchor
  share for the next ten songs.
- **A LOVE on a discovery** lowers it back.
- The anchor share stays between 1 in 8 and 1 in 3. *My call.*

The two sources take turns for discoveries, with Bandcamp and YouTube at
about half each. Either source can be missing, and the station carries on
with the other.

**YouTube Music**, through yt-dlp:
- **Search.** `https://music.youtube.com/search?q=<query>#songs` with
  `--flat-playlist` returns real songs: titles and ids, no views or
  durations.
- **Anchors** come from the first 5 results for the phrase.
- **Discoveries** come from results 10–50, and from variant queries (the
  phrase plus `underground`, `demo`, `new`, or a neighbouring tag).
- **Resolving** gets the media URL, artist, track, duration and view count.
  A "discovery" that resolves with over 1 M views is re-labelled as an
  anchor.
- **Songs only.** Anything outside 90 s – 12 min is skipped (no mixes, no
  full albums).
- **A YouTube link** seeds from its own Mix (`list=RD<id>`) for related
  songs. Its title and tags supply the phrase for the Bandcamp side.

**Bandcamp genre tags**, with no yt-dlp needed:
- **The request:** `POST https://bandcamp.com/api/discover/1/discover_web`
  with JSON `{"category_id":0,"tag_norm_names":[<tag>],"geoname_id":0,
  "slice":<"new"|"rand"|"top">,"cursor":"*","size":60,"include_result_types":["a"]}`.
  It is undocumented; the owner accepts that it can break.
- **The response.** Each result carries `band_name`, `title` (the release)
  and `featured_track{title, duration, stream_url}`. `stream_url` is a
  signed MP3 that mpv plays directly. Results without a `featured_track`
  are skipped.
- **Slices:** `new` and `rand` give discoveries, and `top` gives anchors.
- **Tags from the phrase:** lower-case it and hyphenate it (`swedish death
  metal` → `swedish-death-metal`). If that returns nothing, drop leading
  words (`death-metal`). If nothing ever matches, as with an artist name,
  Bandcamp sits out and YouTube carries the station.
- **Pacing.** At most one Bandcamp request per 10 s per station, with a
  `screech/<version>` User-Agent. Results are cached for 30 min.

**New to you:** a discovery whose artist key appears in neither
`tracks_heard` nor the loves is "new". New artists come first among
discoveries, and the strip shows `NEW TO YOU`.

**No repeats:** a song (video id or Bandcamp track id) never repeats on a
station within its last 200 songs, and the same artist never plays twice
in a row.

### The song queue and the player

- **The player owns the queue** behind the sentinel URL. It resolves the
  first song, then prefetches one ahead while the current song plays.
- **Songs advance by themselves.** The player loads the next song with
  `force-media-title` set to `Artist - Title` (the existing
  `loadfile … replace -1 force-media-title=%n%…` path). It emits a title
  event, and never `EventStreamEnded`, between songs.
- **First-song budget.** The first song must play inside the tune
  watchdog's 12 s. Its lookups are capped at 8 s in total. Bandcamp gets a
  first try of at most 2 s, since it needs no yt-dlp: when no tag is settled
  yet that is one tag probe (the first tag, slice `new`); with a tag already
  settled it is the usual cache read. If it serves a song, that is the first
  song; otherwise YouTube gets the rest of the budget (ruling R21: "Bandcamp
  gets a <=2 s first try, then YouTube"). A hanging Bandcamp can never take
  more than that, and an artist phrase, which is no Bandcamp tag, still
  reaches YouTube. Background prefetch calls get up to 20 s each.
- **Failures.** A song that won't resolve or play is skipped. Three in a
  row raise `PlayFailed`, so the existing strike-and-move-on path runs. A
  lookup that fails because the network is down takes the existing offline
  path and never strikes. So does a first lookup that runs out of its 8 s: on
  the first lookup a timeout can't tell a slow network from a bad seed. A
  later lookup that times out is only a skipped song.
- **Expiry.** A prefetched media URL older than 1 h is resolved again
  before it loads.
- **The strip clock** counts the station, not the song.

### NEXT and learning

- **NEXT on a discovery station** asks the player for the next song. The
  listen stays open, so the station is never charged a skip. The feedback
  reads `NEXT SONG · again for a new station`.
- **A second NEXT within 3 s** runs today's station skip (`Tune`).
- **LOVE** loves the current song, as for any track.
- **Every song** is a heard track (`tracks_heard`), so RECENT, TASTE and
  export see it.

### yt-dlp

- **Lookup order.** `ytdlp_path = ""` (the default) means "find it": PATH,
  then beside the executable, then the data folder. `"off"` disables it,
  and any other value is used as given.
- **JS runtime.** Deno on PATH is used as is. Otherwise the first of node
  and bun on PATH is passed as `--js-runtimes <name>:<path>`.
- **Process rules:**
  - one yt-dlp process at a time, app-wide, with a tune's lookups ahead of
    prefetches;
  - the working directory is the data folder;
  - `--no-warnings`.
- **Caches, in memory only:** search results for 6 h, media URLs for 1 h.
- **Without yt-dlp,** discovery stations still play from Bandcamp. A
  YouTube link station can't start, and says why on the strip.

### Titles

- **Cleanup, applied only to discovery songs:**
  - Bracketed or parenthesised packaging is removed: Official (Music)
    Video/Audio, Official Visualizer, Lyric(s) Video, Audio, Video, HD, HQ,
    4K. Remaster and live notes stay.
  - Trailing `ft. …`, `feat. …` and `featuring …` are removed.
  - ` - Topic` is removed from a channel name used as the artist.
  - Then `core.CleanText`.
- **Artist and title source.** yt-dlp's `artist` and `track` are used when
  present, otherwise the cleaned video title through the existing split.
  Bandcamp gives `band_name` and `featured_track.title`.

## Retiring the pilot

- **yt-dlp:** the pilot's `yt-dlp.exe` is copied into `%APPDATA%\screech\`,
  and the config's `ytdlp_path = "yt-dlp"` line is removed, so detection
  finds the copy.
- **The pilot's station:** the `yt-restream` station is replaced by
  `discover = "swedish death metal"`.
- **The pilot folder** stays on disk untouched.

## Docs and release

- **README:** the FIND front door, discovery stations, NEXT-means-next-song,
  yt-dlp plus a JS runtime, and the close button.
- **SPEC.md:** the discovery queue, the sources, the yt-dlp rules, and the
  new tab layout and bridge methods.
- **CHANGELOG:** lines under the unreleased 0.9.0, written for listeners.

## Testing

**Offline unit tests**, with the extractor and the Bandcamp client behind
interfaces and fakes:
- the mix ratio and how it adapts;
- new-to-you ordering;
- no repeats, and no back-to-back artists;
- the duration filter;
- the 1 M-view re-label;
- tag fallback;
- title cleanup;
- yt-dlp discovery and `--js-runtimes`;
- one process at a time;
- song advance with no drop;
- three failures raising `PlayFailed`;
- NEXT going to the next song, and a double NEXT changing station.

**Window and terminal:**
- the GUI layout guard, with new fixtures `console-find`,
  `console-find-empty`, `console-find-typed`, `console-radio` and
  `console-more`, plus the existing strip fixtures with the ✕ cap and
  without EXIT;
- `TestKeyboardMap` for `/`, Tab, Esc and I;
- the terminal size tests for the FIND overlay.

**The gate:** the full AGENTS.md gate, and the eval harness to confirm the
radio picker is unchanged.

**Live, on the owner's machine:**
- start `swedish death metal` from FIND and hear three songs in a row with
  clean titles;
- NEXT, then a double NEXT;
- paste a YouTube link;
- check the RADIO tab, ✕, and `screech doctor`.
