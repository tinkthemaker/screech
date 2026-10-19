# Shared Listening Engine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move Screech's listening state machine and its startup sequence out of the terminal UI into UI-free packages, so a second face (the LCARS window, next plan) can drive exactly the same behaviour, with the terminal working identically before and after.

**Architecture:** A new `internal/session` package holds the state machine as a value type with an Elm-style `Update(Msg) (State, []Cmd)`, the same shape the Bubble Tea model has today, so the existing logic and tests move across almost verbatim. The TUI `Model` embeds a `session.State`, forwards session messages to it, and reacts to what changed (tune ceremony, wave energy, readouts). A new `internal/app` package holds startup (config, single-instance lock, log, database, mpv) and returns errors instead of printing them.

**Tech Stack:** Go 1.26.9 toolchain, Bubble Tea (TUI only), `modernc.org/sqlite` via `internal/core`, mpv via `internal/player`.

**Spec:** [docs/superpowers/specs/2026-10-18-lcars-gui-design.md](../specs/2026-10-18-lcars-gui-design.md), sections "How it gets built" §1 (one listening engine, two faces) and §2 (shared startup). This plan is milestone 1; the window itself is a second plan, written once this engine's API exists in code.

## Global Constraints

- Module `github.com/tinkthemaker/screech`; run every go command with `GOTOOLCHAIN=go1.26.9`.
- No new third-party dependencies in this plan.
- `internal/session` and `internal/app` never import `internal/tui` or Bubble Tea.
- The terminal's behaviour, wording, exit codes and log lines are unchanged. The existing TUI test suite is the behavioural specification.
- Text from the network is already cleaned by `core`; nothing here prints raw stream metadata.
- Comments explain why, in the existing voice; keep the existing comments when code moves (they move with it).
- Schema: no migrations in this plan.
- Full gate (AGENTS.md) at the end, unchanged: `gofmt -l .`, `go mod tidy -diff`, `go vet ./...`, `staticcheck ./...` (v0.8.1), `GOOS=linux staticcheck ./...`, `go test -race -count=1 ./...`, `govulncheck ./...` (v1.8.0), and the CGO-free cross-builds for linux/amd64, darwin/arm64, linux/arm64, windows/arm64, freebsd/amd64.
- Work on a branch, `feat/shared-engine`; do not push.

## Review Focus

1. **Stale results after the listener moved on.** A `PlayFailed`, `Restarted` or `Tuned` that lands after another pick must not strike, retune or reopen the wrong station. Test in Task 2: `TestStalePlayFailedIgnored`.
2. **Two NEXT presses before the first tune lands.** Both `Tuned` results arrive; the second pick wins, the phase ends `Tuning` on it, and nothing is stuck. Test in Task 2: `TestDoubleNextLastPickWins`.
3. **Quit while work is in flight.** The event channel closing behind `Quit` must not respawn mpv. Test in Task 3: `TestQuitThenEventsClosedDoesNotRestart`.
4. **Sleep timer expiring while offline or paused.** The player ends at the listener's stored volume, never left at a faded level. Test in Task 4: `TestSleepExpiryRestoresVolumeFromAnyPhase`.
5. **Out-of-range volume from a slider.** `SetVolume{150}` and `SetVolume{-10}` clamp to 100 and 0, and the clamped value is what's persisted and sent to the player. Test in Task 5: `TestSetVolumeClamps`.

---

## File map

| File | Responsibility |
|---|---|
| `internal/app/log.go` | Buffered last-run log (moved from `cmd/screech/main.go`) |
| `internal/app/lock_unix.go`, `lock_windows.go` | Single-instance lock (moved from `cmd/screech/single_*.go`) |
| `internal/app/start.go` | `Start`/`StartFrom`: config → lock → log → database → mpv → volume restore |
| `internal/app/*_test.go` | Log, lock and startup-failure tests |
| `internal/session/session.go` | `State`, `Options`, `New`, `Init`, `Update` dispatch, constants |
| `internal/session/msgs.go` | Every message type (intents in, results/events back) |
| `internal/session/tune.go` | Picks, skips, tune results, stall watchdog, failure strikes, offline/backoff/retry |
| `internal/session/events.go` | Player events: title/auto-hop, playing, buffering, level, drops, backend death/restart |
| `internal/session/pause.go` | Pause/resume (own and external) and the sleep timer |
| `internal/session/intents.go` | Love, presets, volume, seed, sync, touch, quit |
| `internal/session/*_test.go` | State-machine tests ported from `internal/tui` plus the Review Focus tests |
| `internal/tui/app.go`, `keys.go`, `playback.go`, `library.go` | Face only: render state, map keys to intents, react to changes |
| `cmd/screech/main.go` | Uses `app.Start`; prints `StartError`s exactly as today |

---

### Task 1: Shared startup in `internal/app`

**Files:**
- Create: `internal/app/log.go`, `internal/app/start.go`, `internal/app/log_test.go`, `internal/app/start_test.go`
- Move (git mv, then rename package and identifiers): `cmd/screech/single_unix.go` → `internal/app/lock_unix.go`, `cmd/screech/single_windows.go` → `internal/app/lock_windows.go`
- Modify: `cmd/screech/main.go` (`run`, `logPath`, `openLog`, `closeLog`, `logLine`, `fail`, `errAlreadyRunning`)

**Interfaces:**
- Produces:
  ```go
  package app

  var ErrAlreadyRunning = errors.New("another screech is already running")
  func AcquireSingleInstance(dataDir string) (release func(), err error)

  type Log struct{ /* path, file, buffered lines */ }
  func NewLog(path string) *Log
  func DefaultLogPath() string            // UserConfigDir/screech/screech.log, as logPath() today
  func (l *Log) Linef(format string, args ...any) // buffers until Open
  func (l *Log) Open()                    // truncates, flushes the buffer; idempotent
  func (l *Log) Close()
  func (l *Log) Path() string

  type Stage string
  const (
      StageConfig   Stage = "config"
      StageLock     Stage = "single instance lock"
      StageDatabase Stage = "opening database"
      StageMpv      Stage = "mpv"
  )
  type StartError struct{ Stage Stage; Err error }
  func (e *StartError) Error() string     // "<Stage>: <Err>" except StageMpv: "<Err>\n\n" + MpvHelp
  func (e *StartError) Unwrap() error
  const MpvHelp = "screech needs mpv for playback.\n  windows: ..." // verbatim from main.go today

  type Runtime struct {
      Config config.Config
      DBPath string
      Core   *core.Core
      Player player.Player
      Notice string // "database was damaged; moved aside to <file> and started fresh", or ""
      Log    *Log
  }
  func Start(log *Log) (*Runtime, error)  // StartFrom(log, config.Load)
  func StartFrom(log *Log, load func() (config.Config, string, error)) (*Runtime, error)
  func (r *Runtime) Close()               // player, core, lock, log — reverse order
  ```
- `Start` returns `ErrAlreadyRunning` unwrapped (callers test it with `errors.Is`) and never opens the log in that case. Every other failure is a `*StartError`, with the log opened and a `FATAL: ...` line written. Log lines and their order are today's: `screech <version> starting`, `config ok; db at <path>`, each `config warning: <w>`, `database open; <n> stations cached`, the notice, `mpv connected over IPC`, and `volume restore failed: <err>` when that happens.

- [ ] **Step 1: Write the failing tests**

```go
// log_test.go
func TestLogBuffersUntilOpen(t *testing.T) {
    p := filepath.Join(t.TempDir(), "screech.log")
    os.WriteFile(p, []byte("previous run\n"), 0o600)
    l := NewLog(p)
    l.Linef("one %d", 1)
    if b, _ := os.ReadFile(p); string(b) != "previous run\n" {
        t.Fatalf("log touched before Open: %q", b)
    }
    l.Open(); l.Linef("two"); l.Close()
    if b, _ := os.ReadFile(p); string(b) != "one 1\ntwo\n" {
        t.Fatalf("got %q, want buffered line then live line, old run truncated", b)
    }
}

// start_test.go
func TestStartRefusesWhileLockHeldAndLeavesLogAlone(t *testing.T)
    // AcquireSingleInstance(dir) first; StartFrom with a loader returning
    // Config{DataDir: dir} → errors.Is(err, ErrAlreadyRunning); the log file
    // written before the call is byte-for-byte unchanged.

func TestStartMpvFailureCleansUp(t *testing.T)
    // Loader returns Config{DataDir: dir, MpvPath: filepath.Join(dir, "no-mpv")}
    // and dbPath in dir → *StartError with Stage == StageMpv, Error() contains
    // MpvHelp; afterwards AcquireSingleInstance(dir) succeeds (lock released)
    // and the log contains "database open;" then a "FATAL:" line.

func TestStartConfigFailure(t *testing.T)
    // Loader returns an error → *StartError{Stage: StageConfig}; log opened
    // and contains "FATAL: config:".

func TestAcquireSingleInstanceExcludes(t *testing.T)
    // second Acquire on the same dir → ErrAlreadyRunning; after release a
    // third Acquire succeeds.
```

- [ ] **Step 2: Run to verify they fail**

Run: `GOTOOLCHAIN=go1.26.9 go test ./internal/app/`
Expected: FAIL to compile (package and identifiers don't exist yet).

- [ ] **Step 3: Implement `internal/app`**

Move the lock files with `git mv` and rename `acquireSingleInstance`/`errAlreadyRunning` to the exported names. Move `logPath`/`openLog`/`closeLog`/`logLine` into the `Log` type, keeping their comments. Build `StartFrom` from the body of `run()` between the panic guard and `tui.Run`, returning errors instead of calling `fail`.

- [ ] **Step 4: Rewire `cmd/screech/main.go`**

`run()` keeps the panic guard (now using `log.Open`/`log.Linef`/`log.Path`), calls `app.Start(log)`, and maps errors back to today's behaviour: `ErrAlreadyRunning` → `screech: another screech is already running` on stderr, `holdConsoleOnExit()`, exit 1; a `*StartError` → `fail(err.Error())` (which prints `screech: ` + message, holds the console, exits 1). Then the tui options take `rt.Notice`, and `tui.Run(rt.Core, rt.Player, opts)` with `defer rt.Close()`. `runDoctor` and `runExport` are untouched.

- [ ] **Step 5: Run the tests**

Run: `GOTOOLCHAIN=go1.26.9 go test -race -count=1 ./internal/app/ ./cmd/screech/` and `GOOS=linux GOTOOLCHAIN=go1.26.9 go vet ./internal/app/ ./cmd/screech/`
Expected: PASS; vet clean on both OSes.

- [ ] **Step 6: Live check**

Build `dist/screech.exe` and start it twice in a sandboxed APPDATA (same method as previous smoke runs). Expected: the second copy prints `screech: another screech is already running` and exits 1; the first copy's `screech.log` is unchanged by the second. Then quit the first copy with `q`.

- [ ] **Step 7: Commit**

`git commit -m "Share startup between faces: config, lock, log, database and mpv in internal/app."`

---

### Task 2: `internal/session` core: picks, skips and the road to offline

**Files:**
- Create: `internal/session/session.go`, `msgs.go`, `tune.go`, `stub_test.go`, `helpers_test.go`, `tune_test.go`
- Read from (copy logic, don't delete yet): `internal/tui/app.go` (`resumeCmd`, `tuneCmd`, `tuneDeadCmd`, `seedCmd`, `playCmd`, `reconnectCmd`, `applyPick`, the `tickMsg`/`tunedMsg`/`seededMsg`/`playFailedMsg` branches of `Update`), `internal/tui/playback.go` (`failTune`, `offlineRetry`), `internal/tui/keys.go` (SPACE branch, `beginSkip`), `internal/tui/connectivity_test.go` (helpers)

**Interfaces:**
- Produces (the whole package surface; later tasks fill in handlers):
  ```go
  package session

  type Phase int
  const (Idle Phase = iota; Tuning; Playing; Buffering; Offline; Stopped; Paused)
  func (p Phase) String() string // "idle", "tuning", "playing", "buffering", "offline", "stopped", "paused"

  type Msg interface{ isMsg() }
  type Cmd func() Msg

  type Options struct {
      AutoHopAds bool
      SyncLimit  int    // <= 0 means 20000
      Notice     string // startup notice; cleared by the first Touch
      Dot        string // separator in notes and feedback; "" means "·"
  }

  const FeedbackDuration = 2400 * time.Millisecond // faces show Feedback this long after FeedbackAt

  type State struct {
      Now         time.Time
      Phase       Phase
      Station     core.Station
      HaveStation bool
      Reason      string
      PickSeq     int // +1 on every applied pick, including re-picks of the same station
      Artist, Title, TrackKey string
      HaveTrack, LovedTrack, Suspect bool
      Level       Level
      TuneAt, PlayStart, PausedAt time.Time
      Reconnecting, Resuming, Seeking, Syncing, Virgin bool
      Volume      int
      Presets     map[int]string
      SleepStep   int // index into SleepSteps, -1 off
      SleepAt     time.Time
      Notice, Note, Feedback, Fatal string
      FeedbackAt  time.Time
      Quitting    bool
      // unexported: core, pl, opts, drops, lockAt, bufferAt, failStreak,
      // retryN, retryAt, lastGood, haveLastGood, restarts, pauseReload,
      // sleepVol, hopAt
  }
  type Level struct{ L, R, Peak float64; Stereo bool }

  var SleepSteps = []time.Duration{15 * time.Minute, 30 * time.Minute, 45 * time.Minute, 60 * time.Minute, 90 * time.Minute}

  func New(c *core.Core, pl player.Player, opts Options) State
  func (s State) Init() []Cmd           // listen, set volume, resume-or-tune, sync when due
  func (s State) Update(msg Msg) (State, []Cmd)
  func (s State) CurrentSlot() int      // preset slot of the playing station, 0 if unsaved

  // msgs.go — intents (faces send these)
  type Next struct{}; type Love struct{}; type TogglePreset struct{}
  type RecallPreset struct{ Slot int }; type TogglePause struct{}; type CycleSleep struct{}
  type StepVolume struct{ Delta int }; type SetVolume struct{ Percent int }
  type Seed struct{ Query string }; type TuneTo struct{ UUID, Reason string }
  type Wildcard struct{}; type RefreshPresets struct{}; type Touch struct{}; type Quit struct{}
  type Tick struct{ Now time.Time }
  // msgs.go — results and events (session commands produce these; tests inject them)
  type PlayerEvent player.Event; type EventsClosed struct{}
  type Tuned struct{ Pick core.Pick; Err error }
  type Seeded struct{ Result core.SeedResult; OK bool }
  type Synced struct{ N, Pruned int; Err error }
  type PlayFailed struct{ UUID string; Err error }
  type Restarted struct{ Err error }
  type VolumeSet struct{ Err error }; type PauseSet struct{ Err error }
  ```
- Constants move here unchanged from `internal/tui/app.go`: `tuneStall` 12 s, `offlineAfter` 3, `maxDrops` 3, `stableAfter` 60 s, `bufferStall` 20 s, `maxRestarts` 3, `restartWindow` 5 min, `resumeInPlace` 30 s, `sleepFade` 30 s, `autoHopCooldown` 30 s, `defaultSyncLimit` 20000, and `retryBackoff`. `feedbackDur` becomes `FeedbackDuration`.
- Every place the TUI wrote `m.th.G.Dot` into a note or feedback string uses `opts.Dot`.
- Tick handles the three connectivity watchdogs and drop forgiveness in this task: tune stall → `failTune("no signal")`; offline retry due → `offlineRetry()`; a stable minute zeroes drops. (Buffer stall and sleep come in Tasks 3 and 4.)
- In this task Update handles: `Tick`, `Tuned`, `Seeded`, `PlayFailed`, `Next`, `TuneTo`, `Seed`, `Wildcard`. The other message types compile and are ignored until their task.
- `Next` follows the SPACE branch: ignored when `Stopped`; `offlineRetry()` when `Offline`; otherwise the feedback ladder (`LEARNED  Break skip protected` / `LEARNED  Long listen saved` / `LEARNED  Fast skip noted` / `NEXT  Finding another station`) and the skip. Keep a private `beginSkip(feedback string) Cmd` for auto-hop in Task 3.
- `TuneTo` is a no-op when `UUID` is the current station; a `core.TuneTo` error goes to `Note`; otherwise the pick is applied with `Reason` set from the intent.
- `applyPick` keeps only the state half of today's `applyPick` (station, reason, phase, clocks, track reset, drops/reconnecting/resuming/lockAt/bufferAt reset, `Virgin = false`, `PickSeq++`) and returns the play command. The decrypt, typewriter, dial, wave, relayout, readout and info-sheet work stays in the TUI (Task 6).

- [ ] **Step 1: Write the test scaffolding and failing tests**

`stub_test.go`: copy `stubPlayer` from `internal/tui/view_test.go` as is. `helpers_test.go`: `testState(t) (State, *stubPlayer)` using `core.Open` on a temp DB (as `testModel` does) and `Options{Dot: "·"}`; `run(t, cmds []Cmd) []Msg` with the same 250 ms timeout for parked listeners as `runCmd`; `drive(t, s State, msg Msg) State`; `playing(t, s State) State` (`Tuned{Pick: core.Pick{Station: core.SeedStations()[0], Reason: "test"}}`, then `PlayerEvent{Type: player.EventPlaying}`; the latter needs Task 3, so in this task `playing` sets `s.Phase = Playing; s.PlayStart = s.Now` directly with a `// TODO(task 3)` removed in Task 3).

Port these tests from `internal/tui/connectivity_test.go` using the mapping below, one session test per TUI test, same name: `TestThreeFailuresGoOffline`, `TestOfflineRetryGoesBackToLastGood`, `TestOfflineFailureBacksOffWithoutStriking`, `TestPlayErrorStrikesWithoutTouchingOffline`, `TestOfflineRetryFiresOnce`, `TestOfflineRetryPickErrorReschedules`, `TestFailedSkipRestoresPlaying`.

Mapping: `m.ph` → `s.Phase` (`phTune` → `Tuning`, `phPlay` → `Playing`, `phBuffer` → `Buffering`, `phOffline` → `Offline`, `phDead` → `Stopped`, `phPaused` → `Paused`); `m.st` → `s.Station`; `m.haveSt` → `s.HaveStation`; `m.now` → `s.Now`; `m.retryN`/`m.retryAt`/`m.failStreak`/`m.drops` → the unexported fields; `tickMsg(t)` → `Tick{Now: t}`; `tunedMsg{pick: p, err: e}` → `Tuned{Pick: p, Err: e}`; `evMsg(e)` → `PlayerEvent(e)`; `playFailedMsg{...}` → `PlayFailed{...}`; a SPACE key press → `Next{}`. Assertions about rendered text stay in the TUI.

New tests:

```go
func TestStalePlayFailedIgnored(t *testing.T) {
    s, _ := testState(t)
    seeds := core.SeedStations()
    s = drive(t, s, Tuned{Pick: core.Pick{Station: seeds[0]}})
    s = drive(t, s, Tuned{Pick: core.Pick{Station: seeds[1]}})
    before := s.PickSeq
    s, cmds := s.Update(PlayFailed{UUID: seeds[0].UUID, Err: errors.New("x")})
    if len(cmds) != 0 || s.PickSeq != before || s.Station.UUID != seeds[1].UUID {
        t.Fatal("a failure for the station we left must change nothing")
    }
    // and seeds[0] took no strike: StationByUUID(seeds[0].UUID).FailCount == 0
}

func TestDoubleNextLastPickWins(t *testing.T)
    // playing; Next{} twice without running cmds; deliver Tuned for seeds[1]
    // then seeds[2] → Station == seeds[2], Phase == Tuning, PickSeq advanced by 2.

func TestNextWhileStoppedIgnored(t *testing.T)
    // s.Phase = Stopped → Update(Next{}) returns no cmds, phase unchanged.

func TestTuneToCurrentStationIsNoOp(t *testing.T)
    // playing on seeds[0]; TuneTo{UUID: seeds[0].UUID} → no cmds, PickSeq unchanged.

func TestPickSeqCountsRepicks(t *testing.T)
    // two Tuned for the same station → PickSeq increases by 2 (faces rerun the ceremony).
```

- [ ] **Step 2: Run to verify they fail**

Run: `GOTOOLCHAIN=go1.26.9 go test ./internal/session/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement `session.go`, `msgs.go`, `tune.go`**

Copy the logic from the TUI functions listed above, dropping every visual line (decrypt, typewriter, dial, wave energy, relayout, readout and info commands, `m.history = false`). Commands become `Cmd` funcs returning session messages (`Tuned`, `Seeded`, `PlayFailed`, or nil). The listen command is `func() Msg { ev, ok := <-pl.Events(); if !ok { return EventsClosed{} }; return PlayerEvent(ev) }` and is returned wherever the TUI returned `m.listen()`.

- [ ] **Step 4: Run the tests**

Run: `GOTOOLCHAIN=go1.26.9 go test -race -count=1 ./internal/session/`
Expected: PASS (all ported and new tests).

- [ ] **Step 5: Commit**

`git commit -m "session: picks, skips, strikes and the offline backoff, out of the TUI."`

---

### Task 3: Player events in `session`

**Files:**
- Create: `internal/session/events.go`, `internal/session/events_test.go`
- Read from: `internal/tui/playback.go` (`handlePlayerEvent`, `dropStream`, `backendDied`), the `playerRestartedMsg`/`evClosedMsg`/`hangupMsg` branches and the buffer-stall line of the tick branch in `internal/tui/app.go`, `internal/tui/connectivity_test.go`, `internal/tui/autohop_test.go`

**Interfaces:**
- Consumes: Task 2's package surface.
- Produces: Update handles `PlayerEvent` (all `player.EventType`s except `EventPaused`, `EventResumed` and `EventStopped`, which Task 4 adds), `EventsClosed`, `Restarted`, `Quit`; Tick adds the buffer-stall watchdog (`Buffering` for more than `bufferStall` → `dropStream("signal stalled")`).
- `EventTitle` sets `Artist`, `Title`, `TrackKey`, `HaveTrack`, `LovedTrack`, `Suspect` from `core.NoteTitle`, clearing all of them when the title doesn't parse, then runs auto-hop exactly as today (only when `opts.AutoHopAds`, `Playing`, `HaveStation`, `core.AdBreak()` and the cooldown allows it, via `beginSkip("HOPPED  Ad break skipped")`).
- `EventLevel` stores `Level{L, R, Peak, Stereo}` with the mono fallback (`L == 0 && R == 0 && Level > 0` → both = `Level`) and re-arms the listener; nothing else.
- `Quit` sets `Quitting`, calls `core.EndListen(time.Now())` and `pl.Close()`, and returns no commands. Faces exit after sending it.
- `Fatal` wording is today's: `mpv exited · restart screech` (+ ` · <err>`), with `opts.Dot`.

- [ ] **Step 1: Write the failing tests**

Port, same names, with Task 2's mapping: `TestStreamDropReconnectsInPlace`, `TestFourDropsStrikeAndMoveOn`, `TestStableMinuteForgivesDrops`, `TestBufferStallReconnects`, `TestEventPlayingClearsOffline`, `TestEventDiedRestartsAndRearms`, `TestFourthDeathIsFatal`, `TestLateLockWhileOfflineReopensListen`, `TestStreamEventsIgnoredBeforeFirstTune`, and from `autohop_test.go`: `TestAutoHopOnMarkerTitle`, `TestAutoHopCooldown`, `TestAutoHopRespectsOptInAndStrictness`. Replace the `playing` helper's direct field writes with the real `PlayerEvent{Type: player.EventPlaying}` and drop its TODO.

New tests:

```go
func TestQuitThenEventsClosedDoesNotRestart(t *testing.T) {
    s, pl := testState(t)
    s = playing(t, s)
    s, _ = s.Update(Quit{})
    s, cmds := s.Update(EventsClosed{})
    run(t, cmds)
    if pl.restartCount() != 0 {
        t.Fatal("the event stream closing behind Quit is expected, not a crash")
    }
}

func TestTitleSetsTrackAndClearsOnJunk(t *testing.T)
    // playing; PlayerEvent{EventTitle, "Boards of Canada - Roygbiv"} → Artist
    // "Boards of Canada", Title "Roygbiv", HaveTrack; then a title equal to the
    // station name → HaveTrack false, Artist/Title/TrackKey "".

func TestLevelMonoFallback(t *testing.T)
    // PlayerEvent{EventLevel, Level: 0.6} → s.Level.L == 0.6 && s.Level.R == 0.6,
    // and exactly one listen command is returned.
```

- [ ] **Step 2: Run to verify they fail**

Run: `GOTOOLCHAIN=go1.26.9 go test ./internal/session/ -run 'Drop|Stall|Stable|Playing|Died|Death|LateLock|BeforeFirst|AutoHop|Quit|Title|Level'`
Expected: FAIL.

- [ ] **Step 3: Implement `events.go`** (the logic listed under Read from, minus visuals; `m.lastAct` stays in the TUI).

- [ ] **Step 4: Run the whole package**

Run: `GOTOOLCHAIN=go1.26.9 go test -race -count=1 ./internal/session/`
Expected: PASS.

- [ ] **Step 5: Commit**

`git commit -m "session: player events, drops, crash restarts and auto-hop."`

---

### Task 4: Pause, resume and sleep in `session`

**Files:**
- Create: `internal/session/pause.go`, `internal/session/pause_test.go`
- Read from: `internal/tui/playback.go` (`pause`, `resume`, the `EventPaused`/`EventResumed`/`EventStopped` branches), the sleep part of the tick branch, the `p` and `z` keys, `internal/tui/pause_test.go`, and in `internal/tui/sheets_test.go` `TestPauseWhileOfflineStopsRetrying` and `TestSleepExpiryWhileOffline`

**Interfaces:**
- Consumes: Tasks 2 and 3.
- Produces: Update handles `TogglePause`, `CycleSleep`, `PauseSet`, `VolumeSet`, and the three pause-related `PlayerEvent` types; Tick adds the sleep countdown, fade and expiry.
- Sleep expiry runs pause then volume restore inside one `Cmd` (call `SetPaused(true)`, then `SetVolume(s.Volume)`, return the first error as `PauseSet`/`VolumeSet`), replacing today's `tea.Sequence`. The order is the point: a radio coming back at faded level would still be asleep.
- Feedback strings unchanged: `PAUSED  P resumes`, `PAUSED  From media controls`, `PAUSED  Stopped from media controls`, `SLEEP  Off`, `SLEEP  <n> min`, `SLEEP  Paused · P resumes`.

- [ ] **Step 1: Write the failing tests**

Port all 14 tests in `internal/tui/pause_test.go` (same names; `pressRune(t, m, 'p')` → `drive(t, s, TogglePause{})`, `'z'` → `CycleSleep{}`, SPACE → `Next{}`) and the two sheets tests named above.

New test:

```go
func TestSleepExpiryRestoresVolumeFromAnyPhase(t *testing.T) {
    for _, ph := range []Phase{Offline, Paused, Playing} {
        s, pl := testState(t)
        s = playing(t, s)
        s.Volume = 70
        s = drive(t, s, CycleSleep{})                  // 15 min
        if ph == Offline { s = goOffline(t, s) }      // helper: three failTune strikes
        if ph == Paused { s = drive(t, s, TogglePause{}) }
        s = drive(t, s, Tick{Now: s.SleepAt.Add(-10 * time.Second)})
        s = drive(t, s, Tick{Now: s.SleepAt.Add(time.Second)})
        if pl.volumeLevel() != 70 {
            t.Fatalf("%v: player left at %d after sleep, want the listener's 70", ph, pl.volumeLevel())
        }
    }
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `GOTOOLCHAIN=go1.26.9 go test ./internal/session/ -run 'Pause|Resume|Sleep|Stop|Echo|Space|Death'`
Expected: FAIL.

- [ ] **Step 3: Implement `pause.go`**

- [ ] **Step 4: Run the whole package**

Run: `GOTOOLCHAIN=go1.26.9 go test -race -count=1 ./internal/session/`
Expected: PASS.

- [ ] **Step 5: Commit**

`git commit -m "session: one pause, from any source, and the sleep timer."`

---

### Task 5: Listener intents and startup in `session`

**Files:**
- Create: `internal/session/intents.go`, `internal/session/intents_test.go`
- Read from: `internal/tui/keys.go` (`l`, `f`, digits, `adjustVolume`, the prompt's Enter and the virgin SPACE shortcut), `internal/tui/library.go` (Enter → seed by artist), `internal/tui/app.go` (`Init`, `syncCmd`, `volumeCmd`, the `syncedMsg`/`seededMsg`/`volumeMsg`/`pauseMsg` branches), `internal/tui/v2_test.go`

**Interfaces:**
- Consumes: Tasks 2–4.
- Produces: Update handles `Love`, `TogglePreset`, `RecallPreset`, `StepVolume`, `SetVolume`, `RefreshPresets`, `Synced`, `Touch`; `Init` is complete.
- `Love` requires `HaveStation && Phase != Stopped`; feedback `LOVED  Track and station` / `LOVED  Station` / `UNLOVED  Love returned`; errors go to `Note` as `could not save love · <err>`.
- `TogglePreset` requires `HaveStation && Phase != Stopped`; calls `core.TogglePreset`, refreshes `Presets`, and gives feedback `PRESETS  All nine full · F on a saved one frees a slot` / `SAVED  Preset <n> · press <n> to return` / `CLEARED  Preset <n>`.
- `RecallPreset`: an empty slot gives `PRESET  <n> is empty · F saves`; the current station is a no-op; otherwise `core.TuneTo` with reason `preset <n>`.
- `StepVolume` and `SetVolume` clamp to 0–100, persist with `core.SetVolume` (error → `Note` `could not save volume · <err>`), and return the player `SetVolume` command.
- `Touch` clears `Notice`. `Seed` sets `Seeking` and clears `Note`; `Seeded` with `OK == false` sets `Note` to `no matches for <label>`.
- `Init` returns: listen, set volume to `s.Volume`, resume (`core.ResumeUUID` station when its `FailCount == 0`, reason `where you left off`, else `core.Tune`), and the 90 s-timeout sync when `Syncing || core.SyncStale(limit)`.

- [ ] **Step 1: Write the failing tests**

Port from `internal/tui/v2_test.go`: `TestDigitRecallIgnoresEmptySlot` and the state assertions of `TestVolumeSliderControlsPlayerAndPersists` (renamed `TestStepVolumeControlsPlayerAndPersists`).

New tests:

```go
func TestSetVolumeClamps(t *testing.T) {
    s, pl := testState(t)
    s = drive(t, s, SetVolume{Percent: 150})
    if s.Volume != 100 || pl.volumeLevel() != 100 { t.Fatalf("got %d/%d, want 100", s.Volume, pl.volumeLevel()) }
    s = drive(t, s, SetVolume{Percent: -10})
    if s.Volume != 0 || pl.volumeLevel() != 0 { t.Fatalf("got %d/%d, want 0", s.Volume, pl.volumeLevel()) }
    // and core reports Volume() == 0 after reopening the same database
}

func TestTouchClearsNotice(t *testing.T)
    // New(..., Options{Notice: "database was damaged"}) → Notice set; Touch{} → "".

func TestTogglePresetSavesThenClears(t *testing.T)
    // playing on seeds[0]; TogglePreset{} → CurrentSlot() == 1, Feedback has prefix
    // "SAVED  Preset 1"; again → CurrentSlot() == 0, Feedback "CLEARED  Preset 1".

func TestLoveTogglesTrack(t *testing.T)
    // playing + a parsed title; Love{} → LovedTrack, Feedback "LOVED  Track and station";
    // Love{} → !LovedTrack, Feedback "UNLOVED  Love returned".

func TestInitResumesLastStation(t *testing.T)
    // core with last_station = seeds[0] (StartListen once, then reopen) →
    // running Init's commands yields Tuned{Pick.Station == seeds[0], Reason "where you left off"}.
```

- [ ] **Step 2: Run to verify they fail**

Run: `GOTOOLCHAIN=go1.26.9 go test ./internal/session/ -run 'Volume|Touch|Preset|Love|Init|Digit'`
Expected: FAIL.

- [ ] **Step 3: Implement `intents.go` and finish `Init`**

- [ ] **Step 4: Run the whole package**

Run: `GOTOOLCHAIN=go1.26.9 go test -race -count=1 ./internal/session/` and `GOOS=linux GOTOOLCHAIN=go1.26.9 go vet ./internal/session/`
Expected: PASS; vet clean.

- [ ] **Step 5: Commit**

`git commit -m "session: love, presets, volume, seed and startup."`

---

### Task 6: The TUI becomes a face over `session`

**Files:**
- Modify: `internal/tui/app.go`, `keys.go`, `playback.go`, `library.go`, `theme_picker.go`, `receiver.go`, `chrome.go`, `view.go`, `info.go` and any other file that reads a moved field
- Modify: every `internal/tui/*_test.go` that uses moved fields or message types
- Modify: `cmd/screech/main.go` (`tui.Options` → also builds `session.Options`)

**Interfaces:**
- Consumes: the full `session` API.
- Produces:
  ```go
  type Options struct { // tui
      Accent string; ASCII bool; ReducedMotion bool
      Session session.Options // AutoHopAds, SyncLimit, Notice; tui sets Dot from its theme
  }
  // Model gains `s session.State` and loses every field now in State.
  func (m Model) step(msg session.Msg) (Model, tea.Cmd)
  func teaCmds(cmds []session.Cmd) tea.Cmd // each Cmd wrapped so a nil result stays nil
  ```
- Session gains one intent for face-originated messages: `type Notify struct{ Feedback, Note string }`. A non-empty `Feedback` goes through `setFeedback`, and a non-empty `Note` sets `Note`. The TUI's own feedback and notes (`INFO  Nothing tuned yet`, `LIBRARY  Track removed`, `STATIONS  Removed`, `could not open history`, `could not remove station`, …) are sent this way, so the session stays the one owner of `Feedback`/`Note`.
- Library `r` on a loved track whose station is already playing: session `TuneTo` is a no-op there, so the TUI closes the library itself before sending the intent. Today it re-applies the same station; the change keeps the music going instead of restarting the tune ceremony. This is the only intended behaviour change.
- `Update` gets one new branch, `case session.Msg: return m.step(msg)`, so session results and injected test messages take the same path. `tickMsg` does its visual work, then `m.step(session.Tick{Now: m.now})` batched with the next tick. Keys map to intents (SPACE/N → `Next`, L → `Love`, F → `TogglePreset`, digits → `RecallPreset`, P → `TogglePause`, Z → `CycleSleep`, `-`/`[` and `+`/`=`/`]` → `StepVolume{∓5}`, prompt Enter → `Seed`, virgin SPACE → `Wildcard`, library Enter → `Seed{artist or title}`, library `r` → `TuneTo{reason "from a loved track"}`, saved-station Enter → `TuneTo{reason "from saved stations"}`, saved-list digits → `RecallPreset` after the TUI's own empty-slot check, library removals → `RefreshPresets` after the `core` call). Every key first sends `Touch`. Every quit path sends `Quit`, then `tea.Quit`.
- `step` reacts by comparing the state before and after:
  - `PickSeq` changed: run today's ceremony (decrypt, station decrypt, typewriter of `Reason`, dial target, close the library, clear `stTotal` and the play count, relayout, station stats, dial density, and the info-sheet re-request when it's open).
  - `Phase` changed: wave energy 1 for `Playing`, 0.2 for `Buffering`, 0.05 for everything else.
  - `Artist`/`Title` changed: rebuild the display string with the theme's dot, set `trackAt`, fetch the play count.
  - The message was a `PlayerEvent` of type `EventLevel`: feed `s.Level` to the wave and set `stereo`. Any other `PlayerEvent`: `lastAct = now`.
  - After a `Love` intent leaves `LovedTrack` true without reduced motion: set `loveAt`.
- Rendering reads `m.s.*` wherever it read the moved fields; `receiver.go`'s feedback check uses `session.FeedbackDuration`.

- [ ] **Step 1: Mechanical test migration first**

Rewrite the TUI tests to the new names, using Task 2's mapping in reverse (`m.ph` → `m.s.Phase`, `phTune` → `session.Tuning`, `tunedMsg{pick: p}` → `session.Tuned{Pick: p}`, `evMsg(e)` → `session.PlayerEvent(e)`, `tickMsg` stays a TUI type). Delete from the TUI the tests whose ports now live in `internal/session` (all of `connectivity_test.go`'s and `pause_test.go`'s state-only tests and `autohop_test.go`). Keep any assertion about rendered text or wave energy as a TUI test (for example `TestOfflineStatusReadsRetryingNow`, `TestFooterDeadStrip`). Record in the commit message which tests moved where.

- [ ] **Step 2: Run to verify the TUI tests fail to compile**

Run: `GOTOOLCHAIN=go1.26.9 go test ./internal/tui/`
Expected: FAIL to compile (fields and types not moved yet).

- [ ] **Step 3: Rewire the TUI** as described under Interfaces, deleting the moved functions, constants and fields from `internal/tui`.

- [ ] **Step 4: Run every package**

Run: `GOTOOLCHAIN=go1.26.9 go test -race -count=1 ./...`
Expected: PASS, including `TestViewFitsEveryTerminalSize`, `TestReceiverFitsWheneverShapeSaysSo` and `TestASCIIModeIsSevenBit`.

- [ ] **Step 5: Live parity check**

Rebuild `dist/screech.exe`; in a sandboxed APPDATA at 100×30 with real mpv: tune, skip, love, P to pause and again to resume, Z once, F then F, a preset digit, `/` seed, H library and Enter on a loved track, quit. Turn the network off until OFFLINE shows, then on until it recovers. Expected: the same screens and feedback text as before this plan (compare against `.review/ui/live-*.txt` captures), and no `mpv.exe` left afterwards.

- [ ] **Step 6: Commit**

`git commit -m "The terminal becomes a face over the shared listening engine."`

---

### Task 7: Docs and the full gate

**Files:**
- Modify: `SPEC.md` (architecture list: `internal/session` and `internal/app` between core and the TUI), `AGENTS.md` (layout line; `internal/session` tests are where state-machine behaviour is pinned; the mapping table from Task 2 for future ports), `CHANGELOG.md` (no listener-visible change: add nothing unless Task 6's live check found a behaviour difference, which must be fixed rather than documented)

- [ ] **Step 1: Update the docs** as listed.

- [ ] **Step 2: Run the full gate** from Global Constraints.
Expected: every command clean; `go test -race` passes in all packages; the five cross-builds succeed.

- [ ] **Step 3: Commit**

`git commit -m "Document the shared engine."`
