# LCARS Window Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `screech-gui.exe`: a 480 × 128 LCARS strip that unfolds to a 480 × 428 console. It drives the same listening engine as the terminal, and an automated geometry guard holds its layout to the approved mockup.

**Architecture:** `internal/session` gains a `Snapshot` (what a face draws) and a goroutine `Runner` (drives a `State` for faces that aren't Bubble Tea). `internal/gui` is a platform-neutral bridge: Wails-bound methods turn clicks into session intents and library reads into JSON rows. It also holds the embedded HTML/CSS/JS front end. `cmd/screech-gui` is the Windows-only shell: startup message boxes, the single-instance handoff, Wails options, window placement and folding.

**Tech Stack:** Go 1.26.9, Wails v2.16.0 (no Wails CLI, no Node), plain ES-module JavaScript, Antonio (OFL) font, `golang.org/x/sys/windows` for message boxes and window lookup, `github.com/tc-hib/go-winres` v0.3.3 (tool only) for the icon and DPI manifest, headless Microsoft Edge for the layout guard.

**Spec:** [docs/superpowers/specs/2026-10-18-lcars-gui-design.md](../specs/2026-10-18-lcars-gui-design.md). Mockup coordinates: [assets/2026-10-18-lcars-gui-mockup.html](../specs/assets/2026-10-18-lcars-gui-mockup.html).

**Depends on:** [2026-10-18-shared-listening-engine.md](2026-10-18-shared-listening-engine.md), completed. It provides `internal/app` (`Start`, `Runtime`, `StartError`, `ErrAlreadyRunning`) and `internal/session` (`State`, `New`, `Init`, `Update`, every intent and result message, `Notify`).

## Global Constraints

- Every Go file under `cmd/screech-gui` is `//go:build windows`. `internal/gui` imports neither Wails nor `x/sys/windows`, so its tests run on every OS.
- Production GUI build: `CGO_ENABLED=0 GOOS=windows go build -tags desktop,production -trimpath -ldflags "-s -w -H windowsgui" -o dist/screech-gui.exe ./cmd/screech-gui`. Plain `go build ./...` compiles the Wails stub (no tags). That is expected, and it must keep compiling on all CI targets.
- The front end has no build step. Files are served from `embed.FS` exactly as committed.
- Layout values are copied from the mockup and the spec's visual system. Every box edge is a multiple of 4 CSS px. No value is tuned to Antonio's glyph widths. Fonts are swappable through `--font-names` and `--font-labels`.
- The page never decides behaviour. It sends intents and draws `Snapshot`s. Display wording that the terminal already has is reused verbatim.
- Nothing slow runs on a Wails call path. A bound method returns after one `core` read or after queueing one intent.
- The mouse wheel never changes the volume: there's no wheel listener on the slider, and no native `<input type=range>` anywhere.
- The terminal is unchanged except where a task says otherwise. Its full test suite stays green.
- The full gate from AGENTS.md runs unchanged, plus three new lines: `GOOS=windows staticcheck ./...`, `GOOS=windows govulncheck ./...` (the Wails dependency tree is only visible to Windows analysis), and the production GUI build above.
- New dependencies must be at least a week old: Wails v2.16.0 (2026-09-14) and go-winres v0.3.3 (2023-10-13) both are.
- Work happens on branch `feat/lcars-window`, cut from the finished `feat/shared-engine`.

## Review Focus

1. **Snapshot staleness.** The runner must publish after *every* state change that alters a drawn field. That includes clock-driven ones that no message triggers: feedback expiring 2.4 s after it was set, and the sleep minutes changing. Test: `TestRunnerRepublishesWhenFeedbackExpires`.
2. **Runner shutdown with commands in flight.** A command goroutine that finishes after `Stop` must not block forever, panic on a closed channel, or reach the player. Test: `TestRunnerStopWithSlowCommand`.
3. **Second launch while the first window is still starting.** The lock is held but the window doesn't exist yet. The second copy must not claim "playing in a terminal" when the first is a window. Test: `TestHandoffRetriesBeforeBlamingTerminal`, which uses a fake window finder.
4. **Restored position off every screen** (unplugged monitor), or the unfolded window crossing the bottom of the work area. Tests: `TestPlacementOffScreenCentres` and `TestUnfoldNearBottomGrowsUp`, which use fake monitor geometry.
5. **Text that fits in the fixture but not in real data.** Long names, empty lists, 100+ rows and an all-nine-full presets row must all pass the guard. The guard runs fixtures `long-names`, `empty-*`, `many-rows` and `presets-full`.

---

## File map

| File | Responsibility |
|---|---|
| `internal/session/snapshot.go` | `Snapshot`, `StationCard`, `State.Snapshot()` |
| `internal/session/runner.go` | `Runner`, `Sink`: single goroutine owner of `State`, command goroutines, 250 ms tick, level throttle |
| `internal/core/filter.go` | `FilterLovedTracks`, `FilterSavedStations` (moved from `internal/tui/library.go`) |
| `internal/core/window.go` | `WindowPlacement`, `(*Core).WindowPlacement`, `(*Core).SetWindowPlacement` (meta keys) |
| `internal/gui/bridge.go` | `Bridge`: bound intent and library methods, `Window` and `Emitter` seams, `Sink` adapter |
| `internal/gui/rows.go` | JSON DTOs (`RecentRow`, `LovedRow`, `StationRow`, `TasteView`, `InfoView`) and converters from core types |
| `internal/gui/assets.go` | `//go:embed all:frontend` → `Assets` |
| `internal/gui/frontend/index.html` | Static frame markup: strip and console, positioned absolutely like the mockup |
| `internal/gui/frontend/css/lcars.css` | Palette, type roles, frame, buttons, pills, rows, meter, slider, focus edge, reduced motion |
| `internal/gui/frontend/js/main.js` | Boot, snapshot rendering, strip states, meter animation, slider, buttons |
| `internal/gui/frontend/js/console.js` | Tabs, lists, actions, search, info, sleep label, presets footer, elbow clock |
| `internal/gui/frontend/js/keys.js` | Keyboard map and focus movement |
| `internal/gui/frontend/js/bridge.js` | Real bridge (`window.go.gui.Bridge`, `window.runtime.EventsOn`) or the fixture bridge when `?fixture=` is present |
| `internal/gui/frontend/fonts/Antonio-Variable.ttf`, `OFL.txt` | Bundled font and licence |
| `internal/gui/layout_test.go`, `fixtures_test.go`, `testdata/guard.js`, `testdata/fixture.js` | Headless-Edge layout guard (`//go:build windows && layout`), fixtures built from real Go types |
| `cmd/screech-gui/main.go` | Startup, message boxes, panic box, Wails options, lifecycle hooks |
| `cmd/screech-gui/shell.go` | `Window` implementation: fold/unfold geometry, placement save/restore, off-screen check |
| `cmd/screech-gui/handoff.go` | Second-launch handoff: find the window, bring it forward, or say "terminal" |
| `cmd/screech-gui/winapi.go` | The few user32 calls (FindWindowW, ShowWindow, SetForegroundWindow, MonitorFromPoint, GetMonitorInfoW) |
| `cmd/screech-gui/winres/winres.json`, `winres/icon.png`, `cmd/screech-gui/screech-gui-rsrc_windows_{amd64,arm64}.syso` | Icon (resource ID 3, which Wails loads) and the per-monitor-v2 DPI manifest. The `.syso` files must sit in the package directory to link: `go-winres make --in cmd/screech-gui/winres/winres.json --out cmd/screech-gui/screech-gui-rsrc --arch amd64,arm64` (`--out` is a filename prefix) |
| `.github/workflows/ci.yml`, `release.yml` | Windows analysis lines, GUI build, zips, Scoop `bin` + `shortcuts` |

---

### Task 1: Wails feasibility spike (go/no-go)

**Files:**
- Create: `cmd/screech-gui/main.go` (temporary minimal), `cmd/screech-gui/winres/winres.json`, `cmd/screech-gui/winres/icon.png`, the two `.syso` files, `internal/gui/assets.go`, `internal/gui/frontend/index.html` (black page with one orange 96 × 128 block)
- Modify: `go.mod`/`go.sum` (`github.com/wailsapp/wails/v2 v2.16.0`)

**Interfaces:**
- Produces: the build command in Global Constraints, proven. `internal/gui.Assets embed.FS`.
- `winres.json` holds `RT_GROUP_ICON` with the numeric name `#3` (Wails loads `AppIconID = 3`, see `wails/v2/internal/frontend/desktop/windows/winc/app.go`). It also holds `RT_MANIFEST` with `dpiAwareness` `permonitorv2,permonitor`, Common Controls v6 and `supportedOS` Windows 10. Wails' Windows frontend does not set DPI awareness itself, so without the manifest Windows stretches the bitmap at 150%. Generate with `go run github.com/tc-hib/go-winres@v0.3.3 make --in cmd/screech-gui/winres/winres.json --out cmd/screech-gui/rsrc --arch amd64,arm64` and commit the `.syso` output.
- Icon: an orange (`#FF9900`) LCARS elbow on black, 256 × 256 PNG, drawn by hand in a small Go program under `.review/` (not committed). Copied Star Trek assets are an anti-goal.

- [ ] **Step 1:** Add Wails and the minimal `main.go`: `wails.Run(&options.App{Title: "Screech", Width: 480, Height: 128, Frameless: true, DisableResize: true, BackgroundColour: options.NewRGB(0,0,0), AssetServer: &assetserver.Options{Assets: sub(gui.Assets, "frontend")}, Windows: &windows.Options{WindowClassName: "ScreechLCARS", WebviewUserDataPath: <temp dir>, IsZoomControlEnabled: false, DisablePinchZoom: true}})`.
- [ ] **Step 2:** Build with the production command. Expected: `dist/screech-gui.exe` exists and has no console subsystem (`go version -m` shows `-H windowsgui`).
- [ ] **Step 3:** Run it at 100% and at 150% display scaling (or `--force-device-scale-factor` is not available to WebView2; use the real Windows setting, or a second monitor at 150%). Screenshot both with the Windows screen-capture API from a throwaway PowerShell script under `.review/`. Expected: black from the first frame (capture at 0, 100 and 300 ms after launch; no white frame); 480 × 128 CSS px (720 × 192 physical at 150%); crisp edges at 150%; taskbar shows the orange icon; Alt+F4 closes it.
- [ ] **Step 4:** Prove the cross-platform rules: `for t in linux/amd64 darwin/arm64 linux/arm64 windows/amd64 windows/arm64 freebsd/amd64; do GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build ./...; done` passes. `go vet ./...`, `GOOS=linux staticcheck ./...`, `GOOS=windows staticcheck ./...`, `go mod tidy -diff`, `govulncheck ./...` and `GOOS=windows govulncheck ./...` are clean.
- [ ] **Step 5: Go/no-go.** If any of Steps 2–4 fails in a way that needs Node, cgo, the Wails CLI or a version newer than v2.16.0, stop and report. The fallback decision belongs to the lead.
- [ ] **Step 6: Commit** `"An empty LCARS window: Wails v2 builds with plain go build, no Node."`

---

### Task 2: Shared library filters in `core`

**Files:**
- Create: `internal/core/filter.go`, `internal/core/filter_test.go`
- Modify: `internal/tui/library.go` (`filteredSaved`, `filteredLibrary`, `lovedMatchScore` → calls into core)

**Interfaces:**
- Produces:
  ```go
  func FilterLovedTracks(entries []LovedTrack, query string) []LovedTrack     // lovedMatchScore ranking, ties newest first
  func FilterSavedStations(entries []SavedStation, query string) []SavedStation // every term in the lower-cased name
  ```
  An empty or space-only query returns `entries` unchanged.

- [ ] **Step 1:** Move the TUI's filter tests (search `internal/tui/*_test.go` for `filteredLibrary`, `filteredSaved` and `lovedMatchScore`) into `filter_test.go` as tests of the core functions. Run `go test ./internal/core -run Filter`. Expected: FAIL (undefined).
- [ ] **Step 2:** Move the code (`lovedMatchScore` and helpers become unexported in core) and make the TUI call it.
- [ ] **Step 3:** `go test -race -count=1 ./internal/core ./internal/tui`. Expected: PASS.
- [ ] **Step 4: Commit** `"Library filters move to core so both faces rank matches the same way."`

---

### Task 3: Window placement in the meta table

**Files:**
- Create: `internal/core/window.go`, `internal/core/window_test.go`

**Interfaces:**
- Produces:
  ```go
  type WindowPlacement struct {
      X, Y     int  // physical screen pixels, top-left of the window, as the OS reports them
      Unfolded bool
      Saved    bool // false: never saved; the shell centres the window
  }
  func (c *Core) WindowPlacement() WindowPlacement
  func (c *Core) SetWindowPlacement(p WindowPlacement) error // writes meta gui_x, gui_y, gui_unfolded in one transaction
  ```
  It uses the same meta helpers as `Theme`/`SetTheme`, needs no migration, and makes `Saved` true once all three keys exist.

- [ ] **Step 1:** Tests: a fresh DB gives `Saved == false`. After a round trip of `{X: -1200, Y: 40, Unfolded: true}`, reopening the DB reads back the same values with `Saved == true`. Negative coordinates (monitors left of the primary) survive.
- [ ] **Step 2:** Run, see FAIL, implement, and run `go test -race -count=1 ./internal/core -run Window`. Expected: PASS.
- [ ] **Step 3: Commit** `"Remember where the window was and whether it was unfolded."`

---

### Task 4: `session.Snapshot` and `session.Runner`

**Files:**
- Create: `internal/session/snapshot.go`, `internal/session/runner.go`, `internal/session/snapshot_test.go`, `internal/session/runner_test.go`

**Interfaces:**
- Produces:
  ```go
  // Snapshot is everything a face needs to draw the now-playing area. It is
  // comparable (no slices or maps), so a runner can publish only on change.
  type Snapshot struct {
      Phase        string // Phase.String()
      HaveStation  bool
      Station      StationCard
      Reason       string
      Artist       string
      Title        string
      HaveTrack    bool
      Loved        bool
      Suspect      bool
      PlayStartMs  int64 // unix ms; 0 until the stream locks
      PausedAtMs   int64
      RetryAtMs    int64 // Offline: when the next retry fires
      Reconnecting bool
      Resuming     bool
      Seeking      bool
      Syncing      bool
      Virgin       bool
      Volume       int
      Slots        [9]bool // Slots[i]: preset i+1 holds a station
      CurrentSlot  int     // 1–9, or 0 when the playing station is unsaved
      SleepAtMs    int64
      Feedback     string // only while Now < FeedbackAt + FeedbackDuration
      Notice       string
      Note         string
      Fatal        string
  }
  type StationCard struct{ UUID, Name, Country, Codec string; Bitrate int }
  func (s State) Snapshot() Snapshot

  type Sink interface {
      Snapshot(Snapshot)
      Level(Level)
  }
  type Runner struct{ /* owns State; msgs chan Msg (buffered 64); done chan struct{} */ }
  func NewRunner(s State, sink Sink) *Runner
  func (r *Runner) Start()        // publishes the first snapshot, runs Init's commands, starts the loop and a 250 ms ticker
  func (r *Runner) Send(m Msg)    // any goroutine; a no-op after Stop
  func (r *Runner) Current() Snapshot // last published, mutex-guarded
  func (r *Runner) Stop()         // Update(Quit) on the loop, then the loop exits; idempotent; returns after Quit ran
  ```
- Loop rules: (1) one goroutine owns `State`. (2) Each command runs in its own goroutine. Its result is sent with `select { case r.msgs <- m: case <-r.done: }`, so nothing blocks after Stop. (3) After every `Update`, compute `Snapshot()` and call `sink.Snapshot` only if it differs from the last one. (4) A `PlayerEvent` of type `EventLevel` calls `sink.Level(s.Level)` at most once per 33 ms. Drop the extras: the meter animates between readings. (5) Ticks are `Tick{Now: time.Now()}`.

- [ ] **Step 1: Tests**
  - `TestSnapshotFeedbackExpires`: set feedback; `Snapshot()` at `FeedbackAt + 2399ms` has it and at `+2400ms` doesn't.
  - `TestSnapshotSlotsAndCurrent`: presets {1: A, 3: B}, playing B → `Slots == [9]bool{true, false, true}` and `CurrentSlot == 3`.
  - `TestSnapshotOfflineCarriesRetry`: go offline via three failed tunes → `Phase == "offline"`, and `RetryAtMs` equals `retryAt` in ms.
  - `TestRunnerPublishesOnChangeOnly`: a recording sink. `Send(Next{})` twice with no state change between publishes produces exactly the expected snapshots, never two identical ones in a row.
  - `TestRunnerRepublishesWhenFeedbackExpires` (Review Focus 1): after `Send(Love{})`, within 3 s the sink gets a snapshot with `Feedback != ""` and then one with `Feedback == ""`.
  - `TestRunnerThrottlesLevels`: emit 100 level events within 100 ms → the sink records 2–5 `Level` calls.
  - `TestRunnerStopWithSlowCommand` (Review Focus 2): a stub player whose `Play` blocks 500 ms. `Send(Next{})`, then `Stop()` immediately. Stop returns in under 200 ms, the race detector stays quiet, and no `Play` call starts after `Close`.
  - `TestRunnerSendAfterStopIsNoOp`.
- [ ] **Step 2:** Run `go test ./internal/session -run 'Snapshot|Runner'` and see FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `go test -race -count=3 ./internal/session`. Expected: PASS three times. `-count=3` shakes out timing flakes in the runner tests.
- [ ] **Step 5: Commit** `"session: snapshots and a runner for faces that aren't Bubble Tea."`

---

### Task 5: `internal/gui` bridge

**Files:**
- Create: `internal/gui/bridge.go`, `internal/gui/rows.go`, `internal/gui/bridge_test.go`

**Interfaces:**
- Consumes: `session.Runner`, `session.Snapshot`, `session.Notify`, `core` (Tasks 2–4).
- Produces:
  ```go
  type Emitter interface{ Emit(event string, data any) }   // "snapshot" → session.Snapshot, "level" → [3]float64{L, R, Peak}
  type Window interface {
      SetUnfolded(unfolded bool) // resize, keep the strip where it is, persist
      Quit()
  }
  type Bridge struct{ /* core, runner, win, emitter (atomic) */ }
  func New(c *core.Core, win Window) *Bridge
  func (b *Bridge) Attach(r *session.Runner)
  func (b *Bridge) SetEmitter(e Emitter)       // called from Wails OnStartup; events before it are dropped
  func (b *Bridge) Sink() session.Sink         // unexported adapter so Snapshot/Level aren't bound to JS

  // Bound to JS as window.go.gui.Bridge.*. Each intent first sends session.Touch.
  func (b *Bridge) Current() session.Snapshot
  func (b *Bridge) Next()
  func (b *Bridge) Love()
  func (b *Bridge) TogglePause()
  func (b *Bridge) CycleSleep()
  func (b *Bridge) TogglePreset()
  func (b *Bridge) RecallPreset(slot int)        // 1–9; others ignored
  func (b *Bridge) SetVolume(percent int)
  func (b *Bridge) StepVolume(delta int)         // the page sends ±5
  func (b *Bridge) Seed(query string)            // trimmed; empty ignored
  func (b *Bridge) ReturnToStation(uuid string)  // TuneTo{uuid, "from a loved track"}
  func (b *Bridge) MoreFromArtist(artist, title string) // Seed{artist, or title when artist is empty}; both empty ignored
  func (b *Bridge) TuneSaved(uuid string)        // TuneTo{uuid, "from saved stations"}
  func (b *Bridge) Recent() ([]RecentRow, error) // core.RecentlyHeard(100)
  func (b *Bridge) Loved(filter string) ([]LovedRow, error)      // core.LovedTracks(500) then core.FilterLovedTracks
  func (b *Bridge) Stations(filter string) ([]StationRow, error) // core.SavedStations then core.FilterSavedStations
  func (b *Bridge) Taste() (TasteView, error)    // core.Taste(time.Now())
  func (b *Bridge) Info() (InfoView, error)      // current station's core.StationInfo; error "nothing tuned yet" when none
  func (b *Bridge) RemoveLoved(artistKey, title string) error // ForgetLovedTrack; then Notify{Feedback: "LIBRARY  Track removed"}
  func (b *Bridge) RemoveStation(uuid string) error           // RemoveStation; RefreshPresets; Notify{Feedback: "STATIONS  Removed"}
  func (b *Bridge) SetUnfolded(unfolded bool)
  func (b *Bridge) Quit()                        // win.Quit(); the shell's OnBeforeClose stops the runner
  ```
- DTOs carry times as unix ms and durations as whole seconds, with JSON tags in lowerCamel. The fields are exactly what the TUI's corresponding views show. Read `internal/tui/library.go`, `taste.go` and `info.go` and mirror their fields; don't invent new ones.
- Removal errors return the TUI's wording (`could not remove loved track`, `could not remove station`) as the error *and* as a `Notify{Note: ...}`.

- [ ] **Step 1: Tests** use a real temp core, the session stub player and a recording `Window` and `Emitter`.
  - `TestIntentsReachSession`: each intent method changes the runner's snapshot the way the matching TUI key does (Next → `tuning` with `NEXT  Finding another station`; TogglePreset → `CurrentSlot == 1`; SetVolume(150) → `Volume == 100`; RecallPreset(0) and RecallPreset(10) → no change).
  - `TestEverythingTouches`: with a startup notice, any intent clears `Notice`.
  - `TestLovedFilterMatchesCore`, `TestRemoveStationRefreshesPresets`, `TestInfoWithoutStation`.
  - `TestSinkEmitsSnapshotsAndLevels`: emitted event names and payload shapes.
  - `TestBoundSurface`: reflect over `*Bridge`'s exported methods and compare against the list above. `Snapshot` and `Level` must be absent, so the JS surface is pinned.
- [ ] **Step 2:** FAIL → implement → `go test -race -count=1 ./internal/gui`. Expected: PASS. `GOOS=linux go vet ./internal/gui` is clean.
- [ ] **Step 3: Commit** `"gui: the bridge between the window's page and the shared engine."`

---

### Task 6: Layout guard and fixtures (written before the page)

**Files:**
- Create: `internal/gui/layout_test.go` (`//go:build windows && layout`), `internal/gui/fixtures_test.go` (same tags), `internal/gui/testdata/guard.js`, `internal/gui/testdata/fixture.js`
- Modify: `AGENTS.md` (how to run the guard)

**Interfaces:**
- Fixtures are Go values built from real types, so they can't drift from the bridge. Each fixture is `{Name string; Snapshot session.Snapshot; Unfolded bool; Tab string; Recent []RecentRow; Loved []LovedRow; Stations []StationRow; Taste TasteView; Info InfoView; Selected int}`. Required fixtures:
  - Strip: `playing`, `tuning`, `reconnecting`, `resuming`, `seeking`, `buffering`, `paused`, `paused-sleep`, `offline`, `stopped`, `suspect`, `syncing`, `virgin`, `feedback`, `notice`, `loved-track`, `long-names` (120-char station, 200-char track, 80-char reason), `volume-0`, `volume-100`.
  - Console: `console-recent`, `console-loved`, `console-loved-filter`, `console-stations`, `console-taste`, `console-search`, `console-info`, `console-sleep`, `empty-recent`, `empty-loved`, `empty-stations`, `empty-taste`, `many-rows` (150 loved), `presets-full`, `console-offline`.
- The harness starts `httptest.NewServer`. It serves `gui.Assets` under `/` and adds `/fixture.js` (from testdata) and `/fixtures.json` (marshalled fixtures). It also serves `/guard.js`, which runs after `document.fonts.ready`, measures the DOM and writes JSON into `<pre id="guard">`.
- For each fixture at scale 1 and 1.5, it runs `msedge --headless=new --disable-gpu --hide-scrollbars --window-size=480,{128|428} --force-device-scale-factor={1|1.5} --virtual-time-budget=3000` twice: once with `--screenshot=.review/gui/<fixture>@<scale>.png` and once with `--dump-dom`. It finds `msedge` on PATH or under `%ProgramFiles(x86)%\Microsoft\Edge\Application`, and skips if it's absent.
- `guard.js` checks, using `getBoundingClientRect()` in CSS px relative to the window:
  1. Every `[data-col=text]` element has `left == 136`, and every `[data-col=fill]` has `left == 124`.
  2. Every `[data-box]` element has left, top, width and height that are multiples of 4.
  3. Every `[data-text]` element lies inside its nearest `[data-box]` ancestor and inside the window. `scrollWidth <= clientWidth`, unless the element has the `ellipsis` class (then its own rect must still fit).
  4. No two `[data-leaf]` elements intersect, ignoring ancestor/descendant pairs.
  5. It reports the rect of every `#strip [data-fixed]` element by `data-id`.
- The Go side fails on any violation, naming the fixture, scale, element and rule. It also compares every fixture's `data-fixed` rects with `playing` at the same scale, folded and unfolded. Any difference fails, which covers "nothing in the strip moves".

- [ ] **Step 1:** Write the harness, the guard and the fixtures. Run `go test -tags layout -run Layout -v ./internal/gui`. Expected: FAIL, because the page has no frame yet. Every fixture should report missing elements, not crash.
- [ ] **Step 2: Commit** `"A layout guard that renders every window state in headless Edge."` (red on purpose; the next two tasks turn it green).

---

### Task 7: The strip

**Files:**
- Create/modify: `internal/gui/frontend/index.html`, `css/lcars.css`, `js/main.js`, `js/bridge.js`, `fonts/Antonio-Variable.ttf`, `fonts/OFL.txt`
- Font source: `https://raw.githubusercontent.com/google/fonts/main/ofl/antonio/Antonio%5Bwght%5D.ttf` (74,104 bytes) and `.../ofl/antonio/OFL.txt`. This is a TTF rather than the spec's woff2: no conversion tool is needed, and WebView2 renders both. Record the change in the spec's Font line.

**Interfaces:**
- Consumes: `Bridge` JS names (Task 5), fixtures (Task 6).
- Geometry: the strip markup reproduces the mockup's playing strip exactly. Left block `0,0,96,128`, radius 48. Top bar segments `96–328`, `332–412`, `416–480`, 12 tall. Inner curves at `96,12` and `96,76`. Button bar at y 100–128: spacer `96–120`, NEXT `124–252`, LOVE `256–336`, PAUSE `340–420`, MORE `424–480` (rounded end, 14 padding). Station text box `136–376` × `24–48`. Track box `136–464` × `52–70`. Status row `136–464` × `74–88`.
- Grid snaps: a few mockup values are off the 4-px grid; these are the corrected values, and nothing else moves.
  - Meter box `392–464` × `24–48` (72 wide, not 70). Its 12 bars (4 wide, 2 apart, 70 in total) sit centred inside, 1 px in from each side.
  - Volume control: `VOL` label box `320–344` (text right-aligned), track `352–440` (88 × 6 centred in the 14 px row, radius 3, `#333`, orange fill), value box `448–464` (right-aligned). The status text box ends at `312`. The thumb is 6 × 14, tan, radius 3. The click/drag target is the full `352–440` × `74–88`.
  - Button colours from the mockup: LOVED `#FF8A8A`, RESUME `#F0D0F0`, dead `#3A2020` with `#7A5050` text.
- Any other mockup value found off-grid snaps to the nearest multiple of 4 that keeps the spec's gaps. List each snap in the report.
- States follow the spec's state table. `stopped` keeps the orange frame and puts `STOPPED` in red-alert text. Its track row reads `mpv stopped · run screech doctor` (with `Fatal` appended when present). Only MORE is live. Offline switches the frame to the `--a1/--a2/--a3` palette: NEXT reads `RETRY NOW`, LOVE is dead, and the status reads `■ OFFLINE · retrying in n s` from `RetryAtMs`. The `BREAK` chip is a 14-tall coral pill at the status row's start, before the state word. Feedback replaces the status row's left side while present. The startup notice shows in the track row until the first intent.
- The status text is `● LIVE m:ss` (`h:mm:ss` from an hour), computed in the page from `PlayStartMs`. `❚❚ PAUSED m:ss` counts from `PausedAtMs`. The note after `·` is, in priority order: feedback, `Note`, sleep (`sleep n min`), then `Reason`.
- Meter: 12 bars, 4 wide, 2 apart, inside the 70 × 24 box. Height = `max(2px, level × texture(bar, t) × 24)`, where `texture` is a smooth deterministic noise in 0.35–1.0 per bar. It eases toward new readings on `requestAnimationFrame`. Left bars take `L` and right bars take `R`. Paused, tuning and offline show the 2 px floor. It runs under reduced motion too.
- Slider: pointer events only (`pointerdown` captures the pointer, `pointermove`, `pointerup`), and it sends `SetVolume` at most every 50 ms plus once on release. There's no wheel listener, and `wheel` events on the window are `preventDefault`ed only when `ctrlKey` is held (blocking zoom).
- Page hygiene: `user-select:none` everywhere except the search input; `cursor:default`; no scrollbars; `contextmenu` prevented; black `html` background; `--wails-draggable:drag` on the top bar and the left block's top 100 px, and `no-drag` on every control.
- Every element the guard checks carries its `data-*` markers (`data-col`, `data-box`, `data-text`, `data-leaf`, `data-fixed` with `data-id`).

- [ ] **Step 1:** Build the strip. Run the guard: `go test -tags layout -run 'Layout/(playing|tuning|reconnecting|resuming|seeking|buffering|paused|paused-sleep|offline|stopped|suspect|syncing|virgin|feedback|notice|loved-track|long-names|volume-0|volume-100)' -v ./internal/gui`. Expected: PASS for every strip fixture at both scales.
- [ ] **Step 2:** Open `.review/gui/playing@1.png` next to the mockup's strip, and `offline@1.png` next to the mockup's offline strip. Compare edges with a pixel diff in a throwaway script. Report any difference above anti-aliasing, with coordinates.
- [ ] **Step 3: Commit** `"The strip: every state, the meter and the volume slider."`

---

### Task 8: The console

**Files:**
- Modify: `index.html`, `css/lcars.css`; create `js/console.js`

**Interfaces:**
- Geometry from the mockup's LOVED console:
  - Sidebar tabs: 96 × 28 at y 156, 188, 220, 252 (RECENT, LOVED, STATIONS, TASTE: blue, ice when selected), 284 and 316 (SEARCH, INFO: tan), and 348 (SLEEP: peach).
  - Bottom elbow `0,380,96,48`, bottom-left radius 48, lilac. Its label is the local time `HH-MM`, updated each minute.
  - Inner curve `96,392`. Bottom bar segments `96–300` (lilac) and `304–480` (blue, right radius 6), 12 tall at y 416. An inner curve at `96,128` joins the strip's left block, which grows to 152 tall.
- The content area runs from y 140 to 368, x 124/136 to 464.
  - Header box `136–464` × `140–160` (16/20 type plus a small count). The filter pill is 20 tall at `140–160` and right-aligned to 464; the mockup's 139/22 snaps to the grid.
  - Rows are 28 tall and contiguous, as in the mockup, starting at y 168 with x `124–464`. The selected row is filled ice with radius 14. Text sits at 136, with the origin right-aligned to 452.
  - Lists scroll inside `168–336` (six rows) with no visible scrollbar, and the selected row is scrolled into view. Action pills sit at `344–368`, 24 tall, from x 124, 4 apart.
- TASTE on the grid: section labels are 16-tall boxes. Rows use a 16-px pitch (a 12-tall box, 4 gap) with 8-tall bars centred in them.
  - "When you listen": label `168–184`, then four rows from 188 (ending 248).
  - Artists (`136–292`) and tags (`312–464`): labels `264–280`, then up to five rows from 284 (ending by 364).
  - The mockup's 6-px label margins become 4.
  - Footer at y 384–404: `PRESETS` label at 136, nine 20-px slots 4 apart, and SAVE/SAVED pill right-aligned at 464.
  - Slot colours: saved tan, current orange, empty `#262626` with `#666` digits. Clicking a saved slot calls `RecallPreset`, and clicking SAVE calls `TogglePreset`.
- Views (data from the bridge; wording from the TUI views):
  - RECENT: track and station, newest first.
  - LOVED: the `/ FILTER` pill opens a one-line LCARS input in the header. Actions are RETURN TO STATION, MORE FROM ARTIST and REMOVE. REMOVE arms on the first press (it reads `CONFIRM`, in red), removes on a second press within 3 s, and disarms when the selection changes.
  - STATIONS: listening time, then a preset badge, then filter. Actions are TUNE and REMOVE (same confirm rule).
  - TASTE: header totals and loved count; time-of-day bars; artists (90 days) and tags side by side. The TUI's `taste.go` rules decide what is shown when empty.
  - SEARCH: an LCARS input. Enter calls `Seed`, and `Seeking` shows `◌ SEEKING` on the strip. `Note` (`no matches for …`) shows under the input.
  - INFO: the TUI's `info.go` fields as label/value rows.
  - SLEEP: a press calls `CycleSleep`, and the tab label shows `SLEEP` or `SLEEP nn` (minutes left).
- Empty states: one lilac line at `136,168`, in the TUI's wording where it has one.
- The console reloads its current view's data when it opens, when the tab changes, after a removal, and when `Snapshot.Station.UUID` changes (RECENT, INFO).

- [ ] **Step 1:** Build the console. Run the guard for all `console-*`, `empty-*`, `many-rows` and `presets-full` fixtures plus the folded-vs-unfolded strip comparison. Expected: PASS at both scales.
- [ ] **Step 2:** Compare `console-loved@1.png` and `console-taste@1.png` with the mockup's two consoles as in Task 7. The footer changed by design (nine slots plus SAVE, no volume).
- [ ] **Step 3: Commit** `"The console: library tabs, search, info, sleep and the presets footer."`

---

### Task 9: Keyboard, focus and motion

**Files:**
- Create: `js/keys.js`; modify `css/lcars.css`, `js/main.js`, `js/console.js`
- Create: `internal/gui/testdata/keys.html`, which drives `keys.js` against the fake bridge and records calls. A `layout`-tagged test, `TestKeyboardMap`, loads it in headless Edge and reads the recorded calls from `--dump-dom`.

**Interfaces:**
- The map, from the spec:

  | Key | Action |
  |---|---|
  | SPACE, N | `Next` |
  | L | `Love` |
  | F | `TogglePreset` |
  | 1–9 | `RecallPreset` |
  | P | `TogglePause` |
  | Z | `CycleSleep` |
  | `/` | unfold to SEARCH and focus the input |
  | H | unfold to LOVED |
  | I | unfold to INFO |
  | `-`, `[` and `+`, `=`, `]` | `StepVolume(∓5)` |
  | TAB, Shift+TAB | cycle tabs while unfolded |
  | J/K and ↓/↑ | rows |
  | ENTER | the row's primary action (LOVED: MORE FROM ARTIST, as `enter` does; STATIONS: TUNE) |
  | R | RETURN TO STATION |
  | X | REMOVE (confirm rule) |
  | ESC | close search or info first, then fold |
  | Q | `Quit` |

  Keys go to the search input while it has focus, except ESC and ENTER.
- Focus: a 2 px `--ice` inset edge on the focused control (`box-shadow: inset 0 0 0 2px`). There's no outline anywhere (`outline: none`), and focus never changes a box's size.
- Motion: pressed buttons flash `filter: brightness(1.35)` for 120 ms, and the tuning name fades in over 400 ms. Folding is immediate. `prefers-reduced-motion` and the `?reduced=1` flag (set by the shell from `--reduced-motion` / `SCREECH_REDUCED_MOTION`) disable everything except the meter. Buffering pulses `BUFFERING` at 0.5 Hz, and shows it steady under reduced motion.

- [ ] **Step 1:** `TestKeyboardMap` covers every row of the table, including wheel-over-slider → no `SetVolume` and Ctrl+wheel → `preventDefault`. FAIL → implement → PASS.
- [ ] **Step 2:** Rerun the whole guard to confirm the focus edge moves nothing: `go test -tags layout -v ./internal/gui`. Expected: PASS.
- [ ] **Step 3: Commit** `"The window answers the terminal's keys; focus is a lit edge, not a ring."`

---

### Task 10: The Windows shell

**Files:**
- Modify: `cmd/screech-gui/main.go`; create `shell.go`, `handoff.go`, `winapi.go`, `shell_test.go`, `handoff_test.go` (all `//go:build windows`)

**Interfaces:**
- Consumes: `app.Start`, `session.New`/`NewRunner`, `gui.New`/`Attach`/`SetEmitter`/`Sink`, `core.WindowPlacement`.
- `main()` order:
  1. `log := app.NewLog(app.DefaultLogPath())`, plus a deferred panic guard that logs the stack and shows a message box: "Screech crashed. Details were written to <path>."
  2. `rt, err := app.Start(log)`:
     - `ErrAlreadyRunning`: `handoff(finder, 3*time.Second)`. Found → restore and foreground the window, exit 0. Not found → message box "Screech is already playing in a terminal.", exit 1.
     - `*StartError`: a message box (title "Screech", `MB_ICONERROR`) with `err.Error()`, exit 1.
  3. Build `session.New(rt.Core, rt.Player, session.Options{AutoHopAds: cfg.AutoHopAds, SyncLimit: cfg.SyncLimit, Notice: rt.Notice, Dot: "·"})`, the bridge and the runner.
  4. `wails.Run` with Task 1's options. Add `StartHidden: true`; `Height` from the saved fold state; `OnStartup` (store ctx, `SetEmitter`, `runner.Start()`); `OnDomReady` (restore placement, `WindowShow`); `OnBeforeClose` (save placement, `runner.Stop()`, return false); `OnShutdown` (`rt.Close()`); and `WebviewUserDataPath: filepath.Join(cfg.DataDir, "webview")`. Pass reduced motion to the page as `?reduced=1` on the start URL.
- Icon redraw: the spike's icon is a plain L. Replace it with a real LCARS elbow at 256 × 256 on transparent, then regenerate the `.syso` files.
  - The elbow is an orange `#FF9900` vertical block 96 wide joined to a horizontal bar 48 tall across the top.
  - Its outer top-left corner is a quarter circle of radius 96, and the inner joint is a concave quarter circle of radius 40.
  - It's inset 16 px from the canvas edges and must read at 16 × 16: render it at 16, 24, 32 and 48 and check by eye that the curve survives.
  - Also emit 16/32/48 PNGs into the icon group.
- `handoff(f windowFinder, wait time.Duration) bool` polls `f.Find("ScreechLCARS")` every 100 ms until `wait` runs out. Review Focus 3: a window still starting gets the full wait before the terminal is blamed.
- `shell` implements `gui.Window`:
  - `SetUnfolded(true)` grows downward. If the unfolded bottom would cross the monitor work area's bottom, it moves up by the overflow and remembers the shift. `SetUnfolded(false)` shrinks and moves back down by the remembered shift. Either way it persists `WindowPlacement`.
  - On restore, if the point 48 px right of and 6 px below the top-left corner (the top bar) isn't on any monitor, it centres on the primary monitor.
  - The monitor maths sits behind `type monitors interface{ WorkAreaAt(x, y int) (rect, bool); Primary() rect }`, so it's testable without Win32.
- [ ] **Step 1: Tests**
  - `TestHandoffRetriesBeforeBlamingTerminal`: the finder succeeds on its 5th poll → true. Never → false after ≈ wait.
  - `TestPlacementOffScreenCentres`.
  - `TestUnfoldNearBottomGrowsUp`: work area bottom 1040 and window y 1000 → it moves to y 612 on unfold (1000 + 428 − 1040 = 388 overflow) and back to 1000 on fold.
  - `TestUnfoldMidScreenKeepsTop`.
  - FAIL → implement → `go test -count=1 ./cmd/screech-gui` → PASS.
- [ ] **Step 2: Live checks** (throwaway data dir via `data_dir` in a temp `config.toml` under a temp `APPDATA`):
  - Each startup message box, triggered for real: a bad TOML, `mpv_path` pointing nowhere, the terminal already running, and the window already running (it comes to the front, no box).
  - Position and fold state survive a restart.
  - An unplugged-monitor position is simulated by writing far-off `gui_x`/`gui_y` into meta.
  - Screenshot each box and each window state into `.review/gui/live-*.png`.
- [ ] **Step 3: Commit** `"The window's shell: message boxes, one Screech at a time, and a remembered place."`

---

### Task 11: Release, docs and 0.9.0

**Files:**
- Modify: `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `internal/version/version.go` (`0.9.0`), `CHANGELOG.md`, `README.md`, `SPEC.md`, `AGENTS.md`, `docs/superpowers/specs/2026-10-18-lcars-gui-design.md` (Font line: TTF; the unfold-near-bottom rule; no visible close/minimise control; elbow shows local time)
- Create: `docs/screenshots/window.png` (from the live run)

**Interfaces:**
- CI `check` job: add `GOOS=windows staticcheck ./...`, `GOOS=windows govulncheck ./...`, and `windows/arm64` to the cross-compile loop. CI `test-platforms` on `windows-latest`: add the production GUI build and `go test -tags layout ./internal/gui` (Edge is preinstalled on GitHub's Windows runners). Upload `.review/gui/*.png` as an artifact.
- Release: build `screech-gui.exe` for windows/amd64 and windows/arm64 by cross-compiling with the production command (CGO-free, proven in Task 1). Put it next to `screech.exe` in both Windows zips. In the Scoop manifest, `bin` lists `screech.exe` and `screech-gui.exe`, and `shortcuts` is `[["screech-gui.exe", "Screech"]]`.
- CHANGELOG `## 0.9.0 — <date>`, written for listeners: the window, how to open it, that it shares everything with the terminal, the slider-only volume, and that only one Screech runs at a time.
- README: a "The window (Windows)" section with the screenshot, the keys, and the fact that it needs WebView2 (built into Windows 10/11).
- SPEC: architecture now reads `core` → `session` → faces (`tui`, `gui`), with `app` shared startup, plus the bridge contract and the layout guard.

- [ ] **Step 1:** Make the edits. Validate the workflows with `actionlint` if it's available, otherwise review them by eye against the existing steps.
- [ ] **Step 2:** Full gate (Global Constraints) plus the production GUI build and `go test -tags layout ./internal/gui`. Expected: all clean.
- [ ] **Step 3: Commit** `"Version 0.9.0: the LCARS window ships beside the terminal."`

---

### Task 12: Live acceptance on Windows

**Files:** none committed (evidence in `.review/gui/live/`)

- [ ] **Step 1:** In a throwaway data directory with real mpv, run the spec's live list with `dist/screech-gui.exe`:
  - Play, skip, love/unlove (LOVED shows).
  - Pause from the window, then from a keyboard media key (the window follows).
  - Sleep timer: set it, check the label counts down, then cycle it off. Expiry and the fade are covered by session tests.
  - Offline (disable the network adapter, or block mpv's traffic with a temporary firewall rule), then back.
  - `taskkill /F /IM mpv.exe`, then recovery.
  - Unfold and fold near the bottom edge; every tab; search; info; presets save/recall/free and nine-full.
  - Quit with Q, with Alt+F4 and from the taskbar.
- [ ] **Step 2:** Afterwards: no `mpv.exe` or `screech-gui.exe` left. `dist/screech.exe` starts and plays when the window is closed, and refuses while it's open. The log reads cleanly.
- [ ] **Step 3:** Report the evidence paths to the lead. Nothing to commit unless a defect is found; defects go back through the task that owns the file.
