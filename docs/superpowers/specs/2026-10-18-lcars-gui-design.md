# Screech desktop window (LCARS) — design

Status: approved by the owner · 2026-10-18 · target release 0.9.0

Approved mockup (open in a browser; shown 1:1):
[assets/2026-10-18-lcars-gui-mockup.html](assets/2026-10-18-lcars-gui-mockup.html)
· screenshot: [assets/2026-10-18-lcars-gui-mockup.png](assets/2026-10-18-lcars-gui-mockup.png)
· console close-up at 2×: [assets/2026-10-18-lcars-gui-console-2x.png](assets/2026-10-18-lcars-gui-console-2x.png)

The owner approved the mockup's look. After review, two parts changed:

- The console's presets row now holds all nine slots and a SAVE pill, and
  sits as a fixed footer on every tab. The console's volume slider is gone;
  the console is 4 px shorter as a result.
- **Volume is a slider on the strip, never the mouse wheel** (owner's
  decision: the window often sits behind games and web pages, where a stray
  scroll must not change the volume). The strip's status row ends in `VOL`,
  an 88-px slider and the number. Shown in
  [assets/2026-10-18-lcars-gui-fonts.png](assets/2026-10-18-lcars-gui-fonts.png).

## Intent

Screech today is terminal only. The owner wants a desktop window for it in the
style of Star Trek's LCARS panels: something small to leave open beside other
work, glance at, and click NEXT or the heart on, which can unfold into a
console for the library when wanted. It is radio, so it should never take a
whole screen: a big window would be mostly dead space.

The one thing that would make this a failure even if it works: **sloppy
layout.** The owner's words: "a correctly spaced and exceptionally well
thought out UI." Every edge, gap and state has to look deliberate.

## Decided by the owner

- **Platform:** Windows only, for now.
- **Shape:** option B, "the strip": a short, wide bar that can be parked along
  a screen edge, which unfolds downward into a console with the library. It
  never goes full screen.
- **Window behaviour:** a normal window. It sits behind other windows when
  something else is focused. Not always-on-top.
- **Sound:** silent. No button chirps; the only sound is the radio.
- **Look:** the approved mockup (above), including its spacing rules,
  palette, type and the four strip states shown.
- **Additions:** the window may do things the terminal doesn't, where they
  earn their place.
- **Volume:** a slider on the strip. The mouse wheel never changes it.
- **Font:** Antonio, "for now". Compared against Okemos (closest to the
  show's face, but no lower case or digits) and League Gothic; the show's own
  Swiss 911 Ultra Compressed is commercial. Keep the face swappable: one CSS
  variable per role (names, labels), no layout constant tuned to Antonio's
  glyph widths beyond what the layout guard checks.
- **Tech:** left to the builder's lead (see below); the owner agreed with the
  calls listed under "How it gets built", including the shared engine,
  Wails v2.16.0 and the window behaviour.

## Goals

1. Everything a listener does in the terminal can be done in the window:
   next, love, pause, sleep timer, presets (save, recall, clear), discover
   (`/`), station info, the four library views and their actions, volume.
2. The window and the terminal are two faces of one Screech: same database,
   same learning, same config, same mpv behaviour, same stream-drop,
   offline, crash-recovery and pause rules. Behaviour must not drift between
   them.
3. The layout matches the approved mockup at 1:1, and every state not
   mocked up is designed inside the same system.
4. It feels like an instrument, not a web page in a box.

## Non-goals (this release)

- macOS and Linux windows. (The terminal stays cross-platform.)
- Always-on-top, a system-tray icon, taskbar thumbnail buttons, global
  hotkeys. (Windows media keys already work through mpv.)
- Sounds of any kind.
- The terminal's theme picker and daypart tints. The window has one palette:
  LCARS.
- A resizable or full-screen window.
- The daemon and phone page in SPEC.md's roadmap.

## Anti-goals

- Uneven gaps, edges that almost line up, text that collides or clips,
  anything that shifts position when the state changes.
- Web-page tells: scrollbars on the frame, selectable text on labels, the
  browser context menu, an I-beam cursor over buttons, blue default focus
  rings, a white flash while the window loads.
- A window that freezes: no database, directory or mpv call ever runs on the
  thread that draws the UI.
- Any regression in the terminal version.
- Copied Star Trek assets: no show logos, sound clips or the show's own
  font. Visual style only.

## Constraints

- Go module `github.com/tinkthemaker/screech`, Go 1.26.9 toolchain, existing
  CI pins and the one-week rule for new dependency versions (AGENTS.md).
- The terminal build must keep building for linux/amd64, darwin/arm64,
  linux/arm64, windows/arm64, freebsd/amd64 with `CGO_ENABLED=0`, and
  `go vet`, `staticcheck` (also `GOOS=linux`), `go test -race ./...` and
  `govulncheck` must stay clean.
- mpv stays the audio backend. Its process rules (job object, `HideWindow`,
  restart limits, pinned options) apply unchanged.
- Network text goes through `core.CleanText` before it is shown (already
  true of everything core returns).
- Local only: no accounts, no telemetry, nothing new over the network.

## How it gets built (builder's lead's calls)

These are my decisions, not the owner's. Each has a reason; a builder who
finds a reason is wrong should say so rather than quietly deviate.

### 1. One listening engine, two faces

Today the listening state machine lives inside the terminal UI
(`internal/tui`: `app.go`, `playback.go`, `keys.go`): tune and fail
handling, in-place reconnects (`maxDrops`, `stableAfter`), the 20 s buffer
watchdog, offline detection and retry backoff, mpv restart limits,
pause/resume including pauses from media keys, the sleep timer and its fade,
auto-hop, feedback messages and the volume. A second UI must not copy that
logic, because the two would drift.

**Call:** extract it into a new UI-free package, `internal/session`, that both
the terminal and the window drive.

- `session` owns: phases (tuning, playing, buffering, paused, offline,
  stopped), the current pick and track, reconnect/offline/restart rules, pause
  and sleep, auto-hop, volume, presets toggling, and the transient feedback
  line. It talks to `core.Core` and `player.Player` exactly as the TUI does
  now.
- Its inputs are listener intents (Next, Love, TogglePause, CycleSleep,
  TogglePreset, RecallPreset(n), Seed(query), SetVolume/StepVolume, RetryNow,
  Quit), player events, and a clock tick. Its output is an immutable
  `Snapshot` (everything a face needs to draw the now-playing area), published
  whenever it changes, plus any one-shot notices.
- The TUI keeps everything visual: rendering, motion, layout, overlays,
  library and info sheets (which read `core` directly, as now). Its `Model`
  holds a session and renders its snapshots.
- The existing TUI tests are the behavioural specification. Tests that check
  the state machine move to `session` (or are kept as TUI tests over the new
  seam). The full TUI suite must be green before the window work starts.
  This refactor is its own milestone and its own commit series.
- Rejected alternative: running the TUI's Bubble Tea model headless inside the
  window process and reading its private state. It avoids the refactor but
  welds the window to terminal internals and Bubble Tea's runtime.

### 2. Shared startup

`cmd/screech/main.go`'s `run()` does: load config, take the single-instance
lock, open the log, open the database (and its recovered-from notice), start
mpv and restore the volume. Move the reusable parts into an internal package
(name is the builder's choice) so both programs start the same way.

- **The window and the terminal exclude each other** through the existing
  named mutex (same name, derived from the data directory). Only one Screech
  plays at a time.
- The shared startup **returns errors to its caller instead of printing
  them**, because the window has no console. The terminal prints them as
  today. The window shows each in a native Windows message box with the
  terminal's wording, then exits: config that can't be parsed, mpv missing
  (with the same install instructions), mpv failing to start, the database
  failing to open. Never a silent exit.
- **Already running:** if the running copy is the window, the second launch
  brings that window to the front and exits quietly. If it's the terminal,
  a message box says Screech is already playing in a terminal.
- Both write the same `screech.log` (only one can run).
- The window reads the same `config.toml`. `mpv_path`, `data_dir`,
  `auto_hop_ads` and `sync_limit` apply; `accent` and `ascii` don't.

### 3. Wails v2, plain web front end

- **Wails v2**, the stable line, pinned to **v2.16.0** (2026-09-14). v2.17.0
  came out on 2026-10-18 and is too new under the one-week rule. Wails v3 is
  beta; not used.
- The front end is **plain HTML, CSS and JavaScript** (ES modules), no
  framework and no npm build step, embedded with `embed.FS`. LCARS is shapes
  and type; it needs no component library, and a dependency-free front end
  keeps the build one command.
- Builder verifies (not checked yet): that Wails v2 builds with an empty
  frontend install/build step, and whether the Wails CLI is needed for builds
  or only for generating JS bindings.
- **New program:** `cmd/screech-gui`, producing `screech-gui.exe`, built with
  the GUI subsystem (no console window). Every Go file in it, and in any
  Windows-only GUI package, is `//go:build windows`, so non-Windows builds
  and tools never see it. The builder confirms the cross-build loop above
  still passes.
- WebView2 ships with Windows 10 and 11. If it is missing, Wails's built-in
  install prompt handles it.
- **Font:** Antonio (SIL Open Font License), bundled as the variable TTF
  (`Antonio[wght].ttf`, `font-weight: 100 700`) in the embedded assets with
  its licence file. A TTF, not woff2 — Google's repo ships the variable
  font only as TTF. No network font loading.

### 4. The Go/JS bridge

- Go pushes `Snapshot`s to the page as Wails events. Audio levels arrive
  far faster than the page needs; throttle level updates to about 30 per
  second and send only the numbers the meter uses.
- **The meter is not a spectrum.** It follows the terminal's wave rule from
  SPEC.md: honest amplitude, synthetic texture. The bar heights' overall size
  comes from the real loudness in `player.EventLevel` (`LevelL`/`LevelR`,
  mono mirrored); the variation between bars is generated. mpv exposes no
  per-band data and the player layer doesn't change for this.
- The page calls bound Go methods for intents and for library reads
  (recent, loved, saved stations, taste, station info, filter, seed). Every
  bound method returns quickly; anything slow runs off the UI thread and
  reports back by event.
- The page never decides behaviour. It sends intents and draws state, so
  the window and terminal can't disagree.

### 5. The window

- **Frameless**, not resizable. Size in device-independent pixels: **480 ×
  128** folded, **480 × 428** unfolded. Unfolding grows the window downward
  from its current top-left corner.
- **Dragging:** the top bar and the left block are drag handles; buttons and
  rows are not. The window moves freely; it does not snap to screen edges
  (my call: the owner didn't choose, and snapping can be added later without
  touching anything else).
- **Title** "Screech" (taskbar). An app icon in the LCARS palette (builder
  designs it: an orange elbow is enough).
- **Close** quits Screech and stops playback, like `q`. There's no close button
  in the mockup: Alt+F4, the taskbar's Close, and `Q` all quit. The builder
  decides whether the frame needs a visible close/minimise control and, if
  so, designs it inside the system (for example, a small pill at the end of
  the top bar). It must not crowd the strip.
- **Remembers** its position and folded/unfolded state, stored in the
  database's `meta` table (as the theme and volume already are). If the saved
  position is off every connected screen, it opens centred on the primary
  screen.
- **High DPI:** the layout is in CSS pixels, so 125%, 150% and 200% scaling
  stay crisp and proportional. No layout constant changes with scaling.
- The page background and the native window background are black from the
  first frame (no white flash).

## The visual system (approved)

The mockup is the reference. These rules are what it encodes; anything not
mocked up follows them.

- **Grid:** every size is a multiple of 4.
- **Gaps:** 4 between all bar segments and buttons.
- **Left block:** 96 wide; outer curve radius 48; inner curve (the concave
  joint between block and bar) radius 24.
- **Bars:** top bar 12 tall; button bar 28 tall; bottom bar 12 tall.
- **Text column:** plain text starts 16 past the inner curve (x = 136 in a
  480-wide window). Filled rows and pills start 12 further left (x = 124) so
  their text lands on the same edge. Right edge is 16 from the window's
  right.
- **Vertical rhythm:** 12 clear of the bars above and below; 8 between items
  within a section; 16 between sections. In the strip, rows are 4 apart.
- **Console footer:** the presets row is fixed at y = 384–404, 12 above the
  bottom bar (y = 416–428). Tab content runs from y = 140 and ends by
  y = 368, 16 above the footer; a tab whose content is shorter leaves the
  space empty rather than moving the footer.
- **Type:** Antonio. Station 22 (line 24), track 14 (line 18), status and
  labels 11 (line 14), library rows 13, section header 16. Labels and
  buttons in capitals, letter-spaced. Station and track names in the case
  the stream sends, truncated with an ellipsis, never wrapped.
- **Palette (TNG era):** orange `#FF9900` frame; tan `#FFCC99`; peach
  `#FF9966`; lilac `#CC99CC`; blue `#9999FF`; ice `#99CCFF` (selected tab,
  track text); red `#CC6666` (LOVE). Red alert: `#CC3333`, `#FF6666`,
  `#FFAAAA`; dead buttons `#3A2020`. Black background.
- **Buttons:** label bottom-right, black, 13, 8 in from the right and 4 up
  from the bottom; a button with a rounded end takes 14 on the right.
- **Pills:** 24 tall, with 12 of padding each side; the presets row uses 20.
- **Unfolding never moves anything in the strip.** Only MORE becomes LESS.

### The strip (480 × 128)

From the mockup: left block with the wordmark SCREECH; top bar in three
segments; station name and the level meter on the first row; track on the
second; status on the third (state word left, the volume control right);
buttons NEXT, LOVE, PAUSE, MORE along the bottom.

**Volume control:** `VOL`, then a slider (track 88 × 6, radius 3, dark
`#333` with an orange fill; thumb 6 × 14, tan, radius 3, centred on the
track), then the value right-aligned in a 16-px box, 8 apart. Click or drag
anywhere on the track; the click target is the full 14-px row height. The
status text on the left truncates before it reaches the control.

Every state the terminal shows needs a design. The mockup covers four; the
builder designs the rest in the same language:

| State | Status row | Buttons | Notes |
|---|---|---|---|
| Playing | `● LIVE mm:ss` (orange) · pick reason | all live | mocked |
| Tuning | `◌ TUNING` (peach) · feedback | all live | mocked: next station name at reduced opacity; the track row shows the pick reason |
| Reconnecting / Resuming / Seeking | `◌ RECONNECTING` / `RESUMING` / `SEEKING` | all live | same treatment as tuning |
| Buffering | `BUFFERING` | all live | the TUI blinks it; here a slow pulse, still under reduced motion |
| Paused | `❚❚ PAUSED mm:ss` · sleep note if set | PAUSE lit as RESUME | mocked |
| Offline | `■ OFFLINE` · `retrying in n s` | NEXT → RETRY NOW; LOVE dead | mocked: whole frame in red alert |
| Stopped (mpv gone for good) | `STOPPED` in red alert | only MORE/LESS live | track row says what happened and points at `screech doctor` |
| Suspected break | adds a `BREAK` chip | as playing | coral/red text chip, not a frame change |
| Directory syncing / first run | the terminal's wording | as appropriate | |
| Feedback | e.g. `LEARNED  Fast skip noted`, `SAVED  Preset 3` | — | replaces the status row's left side for 2.4 s (the TUI's `feedbackDur`), then reverts |
| Startup notice | e.g. damaged database moved aside | — | stays until the first interaction, as in the TUI |

LOVE becomes a brighter **LOVED** when the current track is already loved;
pressing it again un-loves, as `L` does.

### The console (480 × 428)

The strip, unchanged, on top. Below the button bar, the left block continues
as a sidebar: tabs RECENT, LOVED, STATIONS, TASTE (blues; selected in ice),
then SEARCH and INFO (tan) and SLEEP (peach), then the bottom elbow and the
bottom bar. The content panel on the right shows the selected item.

- **LOVED** (mocked): header with count and a `/ FILTER` pill; rows of
  track and origin station; the selected row filled ice; under the list, the
  actions RETURN TO STATION, MORE FROM ARTIST, REMOVE (the TUI's `r`,
  `enter`, `x`, with REMOVE asking for a second press as `x` does); then the
  presets row.
- The presets row is the console's footer on every tab, in the same place,
  so it doesn't move when you switch tabs (both console mockups show it).
  Long lists (RECENT, LOVED, STATIONS) scroll inside the content area above
  it.
- **TASTE** (mocked): listening totals and the loved count in the header;
  time-of-day bars; artists (90 days) and tags side by side. Same data and
  rules as the TUI's TASTE view. The TUI's "stations of the month" list has no
  room here; it belongs on STATIONS, which already orders saved stations by
  listening time.
- **RECENT**, **STATIONS**: same data as the TUI's views. Builder designs
  within the system; lists scroll inside the content panel with no visible
  browser scrollbar.
- **SEARCH**: the content panel becomes the discover prompt (`/` in the TUI):
  an LCARS input field and the same seed behaviour.
- **INFO**: the content panel shows the station sheet (`i` in the TUI): the
  same fields.
- **SLEEP**: each press steps 15 → 30 → 45 → 60 → 90 → off, like `Z`; the
  label shows the minutes left (for example `SLEEP 28`).
- **Presets row** (mocked in the 2× close-up): the PRESETS label, nine
  20-px round slots 1–9 (4 apart), and a SAVE pill at the right edge, all
  20 tall. Saved slots are tan, the slot of the station playing is orange,
  empty slots dark grey. Click a saved slot to tune it. The pill reads SAVE on
  an unsaved station and a lit SAVED on a saved one; pressing it saves or
  frees the current station (`F`, via `core.TogglePreset`). When all nine are
  full, the feedback line says so, as in the TUI.
- **Volume** lives on the strip, not in the console: the slider there, and
  `-`/`+` (steps of 5, the TUI's step) when the window has focus. **The mouse
  wheel never changes the volume** (owner's decision). The value persists
  exactly as the TUI's does.

### Keyboard

When the window has focus, the terminal's keys work: SPACE/N, L, F, 1–9, P, Z,
`/`, H (unfold to LOVED), I (unfold to INFO), `-`/`+` and `[`/`]` (volume),
TAB / Shift+TAB (tabs), J/K and arrows (rows), ENTER, R, X, ESC (closes the
search/info panel first, then folds the console), Q (quit). Keyboard focus is
visible in the system's own style (a lit edge), never a browser focus ring.

### Motion

Small and purposeful: a button flashes lighter for about 120 ms when pressed;
the tuning name fades in; the meter moves with the real audio level; folding
and unfolding is immediate (the window resizes; nothing slides). No boot
animation. Windows' "Show animations" setting (reported to the page as
`prefers-reduced-motion`) and `--reduced-motion` turn off everything but the
meter, which is information, not decoration.

## Verification

- **Session extraction:** the whole existing TUI test suite stays green;
  state-machine tests run against `session` directly. Race detector on.
- **Bridge:** Go tests that a snapshot carries what each strip state needs,
  and that intents map to the same session calls the TUI makes.
- **Layout (the anti-goal guard):** the front end runs in headless Edge
  against a fake bridge fed fixture snapshots, one per state in the table
  above and one per console view, with long-name and empty-library fixtures.
  For each: a screenshot at 100% and 150% scaling, plus automated geometry
  checks from the DOM. Every text element in the content column starts at
  x = 136 (filled rows at 124); every box edge sits on the 4-px grid; no text
  overflows its box or overlaps another; nothing in the strip moves between
  folded and unfolded or between states. Screenshots are kept as review
  artifacts.
- **Live:** on Windows with real mpv, in a throwaway data directory: play,
  skip, love, pause from the window and from a media key, sleep timer, offline
  (network off) and back, an mpv kill and recovery, unfold/fold, every tab,
  quit. Afterwards no `mpv.exe` is left running, and the terminal version
  still starts and refuses to run while the window is open.
- The existing gate in AGENTS.md, unchanged, plus a Windows build of
  `screech-gui.exe`.

## Release

- Version 0.9.0. CHANGELOG entry written for listeners.
- The release workflow builds `screech-gui.exe` for windows/amd64 and
  windows/arm64 (builder chooses a Windows runner or cross-compiling, whichever
  Wails v2 supports cleanly) and adds it to the Windows zips.
- Scoop manifest: `bin` lists both programs; a `shortcuts` entry adds a Start
  menu item "Screech" pointing at `screech-gui.exe`.
- README: a short section on the window, with a screenshot.

## Left to the builder

- Designs for the states and views the mockup doesn't show, inside the
  system above.
- The app icon; whether the frame gets a visible close/minimise control.
- Package names, the session API's exact shape, and how the TUI tests are
  split between `session` and `tui`.
- Exact feedback wording where the terminal's doesn't fit the strip.
- What the decorative number in the bottom elbow shows (the mockup has
  `08-47`).

## Decided while building

The points "Left to the builder" leaves open, as shipped in 0.9.0. Where the
code differs from what this spec or the plan said, the code is recorded.

- **Unfolding near the bottom of the work area.** Unfolding grows downward
  from the window's top-left corner. If the 428-px window would cross the
  bottom of the work area of the monitor under its top bar, it moves up by
  the overflow (and never above that work area's top) and remembers the
  shift. Folding moves it back down by that shift, unless the window was
  dragged in between; then it folds where it is. Position and fold state
  are saved either way. (`cmd/screech-gui/shell.go`)
- **No visible close or minimise control.** The frame stays as mocked. Q,
  Alt+F4 and the taskbar's Close all quit through one path that saves the
  placement, stops the session runner and then closes mpv, the database and
  the lock, once. A minimised window is restored by the second-launch
  handoff.
- **The bottom elbow shows the local time** as `HH-MM`, taken from the
  page's clock and rechecked each second while the console is unfolded, so
  it turns over on the minute. The mockup's `08-47` is only the markup's
  placeholder. (`js/console.js`)
- **The icon is an orange (`#FF9900`) LCARS elbow** on transparent, drawn
  for this project: a 96-px block joined to a 48-px bar with an outer
  quarter circle of radius 96 and a concave inner one of radius 40. The
  executable carries 256, 48, 32 and 16 px images as resource ID 3, which
  Wails loads.
- **Reduced motion reaches the page through a bound `Bridge.Prefs()`
  method**, not a start-URL query: Wails v2 has no start-URL option. This
  replaces the plan's `?reduced=1`. The shell fills `Prefs.ReducedMotion`
  from `--reduced-motion` or `SCREECH_REDUCED_MOTION`, and the page also
  honours the `prefers-reduced-motion` media query. The layout guard's
  `reduced` fixture goes through the same call. The meter keeps moving.
- **`Bridge.Wildcard()`** carries the first-run wildcard to the engine, as
  the terminal's `session.Wildcard`. The hint is the terminal's own wording,
  `type a genre or artist · space for a wildcard`, shown in the SEARCH
  input's placeholder on a first launch, and SPACE in an empty SEARCH box
  calls `Wildcard()`. (The plan's draft said NEXT; NEXT is a skip and stays
  one.)
- **ESC** unwinds in this order: filter text, the filter box, the SEARCH
  box (cleared, then blurred), a filter left behind, then the fold. INFO
  isn't a transient panel in the window, so ESC folds straight from it
  rather than closing it first.
- **Feedback wording** is the engine's, unchanged. On the strip, its lead
  word (up to the first double space) takes the status word's place in
  peach and the remainder follows after a `·`, so `LEARNED  Fast skip noted`
  reads `LEARNED · Fast skip noted`.
- **Packages:** `internal/app` (shared startup), `internal/session` (the
  engine, `Snapshot`, `Runner`), `internal/gui` (the bridge, the row types,
  the embedded front end), `cmd/screech-gui` (the Windows shell:
  `main.go`, `shell.go`, `handoff.go`, `winapi.go`).
