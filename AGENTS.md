# Working on screech

Go terminal radio: `cmd/screech` (entry, doctor, export, console hold),
`internal/app` (shared startup: config, per-OS single-instance lock, log,
database, mpv), `internal/core` (SQLite store, bandit, fingerprints,
radio-browser client), `internal/player` (mpv over JSON IPC),
`internal/session` (the listening state machine every face drives),
`internal/tui` (Bubble Tea faceplate over a `session.State`),
`internal/config`, `internal/version`.
[SPEC.md](SPEC.md) is the design record; keep it in step with behavior.

## Verify a change

```
gofmt -l .                     # must print nothing
go mod tidy -diff              # must print nothing
go vet ./...
staticcheck ./...              # honnef.co/go/tools/cmd/staticcheck@v0.8.1
GOOS=linux staticcheck ./...   # unix-only files (IPC socket, flock, SIGHUP)
go test -race -count=1 ./...
govulncheck ./...              # golang.org/x/vuln/cmd/govulncheck@v1.8.0
for t in linux/amd64 darwin/arm64 linux/arm64 windows/arm64 freebsd/amd64; do
  GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build ./...
done
```

- The `go.mod` `toolchain` line (go1.26.9) carries the stdlib security
  fixes, but a newer local Go wins over it. If yours is older than the
  patched releases, run `govulncheck` and release builds with
  `GOTOOLCHAIN=go1.26.9`. Go 1.27.2's export data breaks staticcheck v0.8.1.
- The `TestFake*` tests in `internal/player` re-exec the test binary as a
  scripted mpv (`fakempv_test.go`), so CI covers spawn, IPC, crash, restart
  and close without mpv installed. `TestMPVLifecycle` drives a real mpv
  (`--ao=null`) and skips when mpv isn't on PATH or under `-short`; run it
  locally when touching `internal/player`.
- `TestViewFitsEveryTerminalSize` renders every overlay at every small
  terminal size; any view change must keep it green (no panic, no overflow).
- `receiverShape` is the single faceplate-vs-stacked decision; `View`, the
  wave sizing and `relayout` all read it. `TestReceiverFitsWheneverShapeSaysSo`
  holds the row accounting to the rendered panel, and `TestASCIIModeIsSevenBit`
  fails on any non-ASCII cell in `ascii = true` mode — use `m.th.G` glyphs,
  never literal `…`, `×`, `·` in view code.
- Listening behaviour (tune, drop, offline, restart, pause, sleep, auto-hop,
  love, presets, volume, feedback wording) lives in `internal/session` and
  is tested there by feeding messages to `State.Update` (`drive`, `playing`
  in `helpers_test.go`). A face never re-implements it: the TUI's `step`
  forwards to the session and only reacts to what changed (`PickSeq`,
  phase, track parts). Face-originated feedback goes through
  `session.Notify`, so the session stays the one owner of `Feedback`/`Note`.
- `go test -tags eval -run TestEval -v ./internal/core` (~30s) runs the
  simulated-listener harness (`eval_test.go`). Run it before and after any
  change to the pool, rewards, decay or priors and compare the late-window
  E[like]; `EVAL_SEED_BASE=101` reruns on fresh worlds so a tweak isn't
  tuned to one seed set.
- `go test -tags live ./internal/core -run Live` hits the real radio-browser
  directory; plain unit tests must never touch the network (`openTestCore`
  points the client at a dead transport).

## Conventions

- Comments explain *why*, in the existing voice; keep them when code stays.
- Schema changes are new numbered entries in `migrations` (store.go), never
  edits to old ones; the user_version stamp rides in the same transaction.
- Text from the network (station metadata, ICY titles) goes through
  `core.CleanText` before it is stored or shown.
- CI tool versions are pinned; bump only to releases at least a week old that
  support the go.mod language version.
- User-visible changes get a line in `CHANGELOG.md` under the upcoming
  version's `## X.Y.Z — date` heading. The release workflow publishes that
  section as the GitHub release notes, so write it for listeners. Bump
  `internal/version.Current` to match before tagging `vX.Y.Z`.
