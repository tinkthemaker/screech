# Changelog

User-visible changes, newest first. Each version's section becomes its
GitHub release notes. Releases before 0.9.0 are described on their GitHub
release pages.

## 0.9.0 — 2026-10-26

0.7.0 and 0.8.0 were never published; their changes ship here too, so this
covers everything since 0.6.1.

### New: a window for Windows

- **The LCARS window.** `screech-gui.exe` opens a small window in the style
  of a starship panel: a 480 × 128 strip with the station, the track, a
  level meter, a volume slider and PAUSE, NEXT, LOVE, MORE and EXIT buttons.
  MORE unfolds it into a console with your library (RECENT, LOVED, STATIONS,
  TASTE), SEARCH, INFO, the sleep timer and the nine presets. It sits behind
  other windows like any other, remembers where you left it and whether it
  was unfolded, and opens centred if that spot is no longer on any screen.
  Windows only for now; the terminal works as it always has.
- **How to open it.** Run `screech-gui.exe`, which sits next to
  `screech.exe` in the Windows zip, or use the "Screech" Start-menu shortcut
  the Scoop package adds. No terminal needed. If startup fails (a config
  that isn't valid TOML, mpv missing), the window says why in a message box.
- **It is the same Screech.** The window and the terminal share your
  library, loves, presets, history, volume and learning, and both read
  `config.toml` (`mpv_path`, `data_dir`, `auto_hop_ads` and `sync_limit`
  apply; `accent` and `ascii` are terminal-only). Pause, sleep, reconnects,
  offline retries and mpv restarts behave the same, because they are the
  same engine. The terminal's keys work when the window has focus; the
  README lists them.
- **Volume is the slider and the keys, nothing else.** Click or drag the
  slider on the strip, or press `-`/`+` (or `[`/`]`). The mouse wheel never
  changes it, so a stray scroll over a window parked behind a game or a web
  page can't.
- **One Screech at a time.** Opening the window a second time brings the
  first to the front. The terminal refuses to start while the window is
  open, and the window says so if a terminal is already playing.
- **A familiar player strip.** Playback controls start at the left, EXIT is
  always visible at the right, and long station and track names scroll once
  to show their full text. The outer window corners follow the LCARS frame.
  EXIT, `q`, Alt+F4 and the taskbar's Close all stop the radio. Drag the
  window by its top bar or the block at the left.
- **Needs WebView2**, Microsoft's web view, which is built into Windows 10
  and 11. If it's missing, the window offers to install it.
- `--reduced-motion`, `SCREECH_REDUCED_MOTION` and Windows' own animation
  setting calm the window too: no button flash, no fade-in, no pulsing.
  The level meter keeps moving, since it's information.

### Upgrading

- The first radio launch upgrades the database in place: station and track
  text stored by older versions is cleaned, and fake "tracks" taken from
  stream URLs are deleted. Loves are kept, and each step either completes
  or rolls back. Before that first launch, run the new terminal executable
  with `screech export backup old.db` to keep an easy way back; export works
  with the old database and never upgrades it.
- `go install github.com/tinkthemaker/screech/cmd/screech@latest` now works.
  On Windows, the release page carries a Scoop manifest (`screech.json`).

### New in the terminal and shared engine

- **Pause** with `p`. Keyboard media keys, headset buttons and the OS media
  flyout drive the same pause instead of silencing mpv behind screech's
  back. Paused time is not counted as listening, and resuming within 30
  seconds picks up from the buffer.
- **Sleep timer** with `z`: 15 to 90 minutes, fading out over the last 30
  seconds, then pausing. Your saved volume is untouched.
- **Key sheet** with `?`, listing every key.
- **Station sheet** with `i`: the station's directory details (country,
  tags, homepage, stream) beside what screech has learned about it — time
  listened, visits, loved tracks, and how well it matches you right now.
- **Taste view**, the fourth library tab (`H`, then `tab`): listening time,
  time of day, the artists you actually hear, the tags the model leans on,
  your stations of the month, and your loved-track count.
- **Your own stations.** `screech add <link>` adds any http or https stream
  to `config.toml`, with `--name` and `--tags` if you like, and
  `screech remove` takes it back out; you can also write a `[[stations]]`
  table by hand. They join the rotation the next time Screech starts,
  survive every directory refresh, come round now and then even without
  tags, and say `added by you` when picked for being yours.
  `screech doctor` counts them. The config file is all you need to keep: a
  fresh database rebuilds them from it.
- **Export**: `screech export loved` and `screech export heard` write CSV to
  stdout; `screech export backup <path>` writes a consistent copy of the
  database. All three work while the radio is playing.
- **`auto_hop_ads`** now does something: when it's on and a stream's title
  literally announces a break ("advertisement", "commercial break"),
  screech moves to another station. Empty titles and station IDs never
  trigger it. Off by default.

### Playback and reliability

- A dropped stream reconnects to the same station instead of showing LIVE
  over silence. A stream stuck buffering for 20 seconds counts as a drop.
- Going offline no longer strikes station after station until the cache is
  ruined. After three failures in a row, screech shows OFFLINE, forgives the
  strikes, and retries on its own (5 seconds, backing off to a minute).
  SPACE retries immediately.
- A crashed mpv is restarted and the station resumed, up to three times in
  five minutes.
- mpv no longer keeps playing after screech is killed (Windows and Linux).
- Settings in a personal `mpv.conf` that break live radio are overridden
  for screech's mpv, and its buffers are bounded.

### Fixes

- **The level meter follows the music.** Screech asked mpv for its loudness
  readings under a name mpv 0.41 doesn't answer to, so the terminal's wave
  had been breathing on its own fallback, not the audio. It now reads them
  the way mpv documents, and both the wave and the window's meter move with
  the real level.
- **No more web addresses in RECENT.** On a station that sends no track
  titles, mpv reports the stream's file name instead, and when the address
  ends in a query (`…?token=…`) Screech took it for a track and logged it.
  It now recognises those however the address is written.
- When mpv dies, the reason Screech shows now reliably carries mpv's own
  last words; it used to report the death before mpv's error output had
  arrived.

### Learning

- Stations you've listened to happily without ever pressing love now come
  back into the candidate pool. In a simulated-listener test, this raised
  the share of liked picks for a listener who sticks with stations but
  never presses love from about 30% to 45%.
- "a night favorite" (and the other dayparts) no longer appears for
  stations you mostly skipped.
- Artist names like "Sleeping With Sirens" are no longer cut at "with".

### Interface

- The RECENT list, in both the terminal and the window, no longer repeats a
  track that a relaunch or reconnect logged twice in a row. The CSV from
  `screech export heard` still has every row.
- The full two-column panel now appears whenever it fits — including a
  standard 80x24 window — and never with rows cut off. Smaller windows get
  the compact layout instead of a refusal to start.
- The header keeps the playback state (LIVE, OFFLINE, BUFFERING, PAUSED)
  visible at any width, and the key strip drops labels before keys.
- ASCII mode is pure 7-bit ASCII, and `--reduced-motion` turns off every
  animation, not just the love flash.
- Stations that never send track titles say so after 10 seconds.
- The volume strip closes itself 4 seconds after the last change. The
  recent-tracks list scrolls. Saved stations show their preset number.

### Data and safety

- Station names and track titles from the network can no longer write to
  your clipboard, retitle the terminal window, or break the layout.
- A damaged database is moved aside and rebuilt, and screech tells you so,
  instead of refusing to start.
- CSV exports guard cells that a spreadsheet would run as formulas.
- `screech doctor` works again (it had started launching the radio), and
  unknown arguments are rejected instead of ignored.
- Config typos are repaired with a warning; `data_dir` accepts `~` and
  paths relative to the config file.
- Built with Go 1.26.10, which includes the October 2026 HTTP and TLS
  security fixes and rejects compromised intermediate certificates
  (GO-2026-6809).
