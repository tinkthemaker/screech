# Changelog

User-visible changes, newest first. Each version's section becomes its
GitHub release notes. Releases before 0.8.0 are described on their GitHub
release pages.

## 0.8.0 — 2026-10-14

0.7.0 was never published; its changes ship here too, so this covers
everything since 0.6.1.

### Upgrading

- The first launch upgrades the database in place: station and track text
  stored by older versions is cleaned, and the fake "tracks" taken from
  stream URLs are deleted. Loves are kept, and each step either completes
  or rolls back. Running `screech export backup old.db` before that first
  launch gives you an easy way back; export never upgrades the database.
- `go install github.com/tinkthemaker/screech/cmd/screech@latest` now works.
  On Windows, the release page carries a Scoop manifest (`screech.json`).

### New

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

### Learning

- Stations you've listened to happily without ever pressing love now come
  back into the candidate pool. In a simulated-listener test, this raised
  the share of liked picks for a listener who sticks with stations but
  never presses love from about 30% to 45%.
- "a night favorite" (and the other dayparts) no longer appears for
  stations you mostly skipped.
- Artist names like "Sleeping With Sirens" are no longer cut at "with".

### Interface

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
- Built with Go 1.26.9, which carries the October 2026 standard-library
  security fixes for HTTP and TLS.
