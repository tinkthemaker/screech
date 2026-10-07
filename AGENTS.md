# Working on screech

Go terminal radio: `cmd/screech` (entry, doctor, per-OS single-instance and
console hold), `internal/core` (SQLite store, bandit, fingerprints,
radio-browser client), `internal/player` (mpv over JSON IPC), `internal/tui`
(Bubble Tea faceplate), `internal/config`, `internal/version`.
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

- `TestMPVLifecycle` drives a real mpv (`--ao=null`) and skips when mpv isn't
  on PATH or under `-short`; CI runners skip it, so run it locally when
  touching `internal/player`.
- `TestViewFitsEveryTerminalSize` renders every overlay at every small
  terminal size; any view change must keep it green (no panic, no overflow).
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
