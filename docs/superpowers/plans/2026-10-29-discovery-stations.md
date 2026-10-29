# Discovery Stations and the FIND Front Door Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship discovery stations, built from a phrase or a YouTube link, that play an endless mix of new and niche songs with occasional anchors from YouTube Music and Bandcamp. They start from a new FIND front door in the window and the terminal.

**Architecture:**
- **New packages.** `internal/ytdlp` owns every yt-dlp process: lookup, JS runtime, one-at-a-time lock and working directory. `internal/discover` owns the song queue: sources, mix, no-repeat, new-to-you and title cleanup.
- **The player** plays a station whose URL is the sentinel `discover:<seed>` through a `player.SongSource`. It advances songs itself, so `EventStreamEnded` never fires between songs.
- **The session** adds a `Discover` intent and next-song NEXT.
- **The faces.** The window gets FIND, RADIO and MORE tabs and a ✕ cap. The terminal's `/` prompt gets FIND and RADIO modes.

**Tech Stack:** Go 1.26 (`GOTOOLCHAIN=go1.26.10`), mpv JSON IPC, yt-dlp 2026.10.26, Bandcamp `discover_web` JSON, Wails v2 page (plain ES modules), Bubble Tea.

**Spec:** `docs/superpowers/specs/2026-10-28-youtube-stations-design.md`. Approved mockup: `.superpowers/brainstorm/1043-1793244117/content/front-door-v6.html`.

## Global Constraints

- Follow `AGENTS.md`: comments say *why*, in the existing voice. Schema changes are new numbered `migrations` only. Network and config text goes through `core.CleanText`. View code uses `m.th.G` glyphs, never literal `…`, `×` or `·`.
- **No unit test may run yt-dlp, call Bandcamp or touch the network.** The fakes are the test binary re-exec (`SCREECH_FAKE_YTDLP`, see `internal/player/extract_test.go`), `httptest`, and interfaces.
- mpv keeps `--ytdl=no`. Radio URLs never touch yt-dlp. `Play` rejects every non-http(s) URL except the `discover:` sentinel.
- Media URLs, whether from YouTube or Bandcamp `stream_url`, are never written to the database, the config or the log.
- Discovery station ids use the `disc:` namespace: `disc:` plus the first 16 hex digits of SHA-256 of the identity key.
- Exact copy:
  - feedback: `NEXT SONG  Again for a new station`;
  - strip status flag: `NEW TO YOU`;
  - reason: `discovering · <phrase>` (the face's dot glyph);
  - FIND placeholder: `Artist, song, genre or YouTube link`;
  - RADIO placeholder: `Station, genre or country`;
  - FIND label: `PICK UP WHERE YOU LEFT OFF`;
  - hint: `ESC TO CLOSE · TAB FOR RADIO`.
- Timings:
  - first-song lookup budget 8 s;
  - prefetch call cap 20 s;
  - double-NEXT window 3 s;
  - search cache 6 h;
  - media cache 1 h;
  - Bandcamp cache 30 min, at most 1 request per 10 s per queue.
- The gate for every task: `gofmt -l $(git ls-files '*.go')` prints nothing, then `GOTOOLCHAIN=go1.26.10 go vet ./...` and `GOTOOLCHAIN=go1.26.10 go test -race -count=1 ./<touched packages>`.

## Review Focus

1. **A dead network mid-station** must take the offline path, not strike the station. Task 4 test: `TestQueueSourceNetworkErrorIsDrop`.
2. **An empty Bandcamp tag and yt-dlp missing at once:** the station must fail cleanly, with a strike and a move on, not hang the tune. Task 2 test: `TestQueueNoSourcesErrors`.
3. **A YouTube link with a playlist parameter** (`&list=`, `youtu.be`, `music.youtube.com`) must seed from its video id. Task 3 test: `TestDiscoveryKeyCanonicalisesLinks`.
4. **A NEXT that arrives while the next song is still resolving** must not double-advance or load two songs. Task 4 test: `TestQueueNextWhileResolving`.
5. **A long typed query or link** in the window's bar must stay inside the field. Task 7 fixture: `console-find-typed` with a 200-character link.

---

### Task 1: `internal/ytdlp`, the one place yt-dlp runs  *(Sonnet)*

**Files:**
- Create: `internal/ytdlp/ytdlp.go`, `internal/ytdlp/lock.go`, `internal/ytdlp/find.go`, `internal/ytdlp/ytdlp_test.go`

**Interfaces (Produces):**

```go
type Runner struct {
    Path      string // the yt-dlp executable
    JSRuntime string // "" or "node:<path>" / "bun:<path>"; passed as --js-runtimes
    Dir       string // working directory (the data folder)
}
type Hit struct{ ID, Title string }                 // a flat search or mix entry
type Media struct {
    URL, Artist, Track, Title, Channel string
    Duration float64 // seconds
    Views    int64
    Tags     []string
}
func (r Runner) Songs(ctx context.Context, query string, n int) ([]Hit, error) // music.youtube.com/search?q=<query>#songs, --flat-playlist -J --playlist-items 1:n
func (r Runner) Mix(ctx context.Context, videoID string, n int) ([]Hit, error) // https://www.youtube.com/watch?v=<id>&list=RD<id>, flat
func (r Runner) Resolve(ctx context.Context, page string) (Media, error)       // -f bestaudio/best --no-playlist --get-url + tagged --print lines
func Foreground(ctx context.Context) context.Context                           // marks a tune's lookup: jumps the lock queue
func Find(configured, exeDir, dataDir string) (path string, ok bool)           // "" = search PATH, exeDir, dataDir; "off" = (“”, false)
func DetectJSRuntime() string                                                  // deno on PATH -> ""; else node, else bun -> "name:path"
func Version(ctx context.Context, path string) (string, error)                 // yt-dlp --version
```

- [ ] **Step 1: Write the failing tests** in `ytdlp_test.go`. Reuse the re-exec pattern: `TestMain` runs a fake yt-dlp when `SCREECH_FAKE_YTDLP` is set. The fake records argv and cwd to a file named by `SCREECH_FAKE_YTDLP_LOG` and prints canned output.
  - `TestSongsParsesFlatSearch`: canned `{"entries":[{"id":"knQEA9D-Qq0","title":"Left Hand Path"}, …]}` → two Hits. The argv contains `--flat-playlist`, `-J`, `--playlist-items`, `1:2` and `https://music.youtube.com/search?q=swedish+death+metal#songs`.
  - `TestResolveParsesTaggedLines`: output lines `screech-artist:Entombed`, `screech-track:Left Hand Path`, `screech-title:…`, `screech-dur:356.0`, `screech-views:1234567` and `https://rr1.googlevideo.com/x` → those Media fields.
  - `TestRunnerPassesJSRuntimeAndDir`: `JSRuntime:"node:C:/n/node.exe"` → argv has `--js-runtimes node:C:/n/node.exe`, and the recorded cwd equals `Dir`.
  - `TestOneProcessAtATime`: three concurrent `Songs` calls against a fake that sleeps 200 ms and logs start/end; the log shows no overlap.
  - `TestForegroundJumpsQueue`: with one call holding the lock, a background waiter and then a `Foreground` waiter queue up; the foreground one runs first.
  - `TestFindOrder`: in temp dirs, `Find("", exeDir, dataDir)` prefers PATH (prepend a temp dir with `yt-dlp.exe`/`yt-dlp` to PATH via `t.Setenv`), then exeDir, then dataDir. `Find("off",…)` returns false. `Find("C:/x/yt.exe",…)` returns it unchanged.
- [ ] **Step 2:** Run `GOTOOLCHAIN=go1.26.10 go test ./internal/ytdlp` and expect FAIL (package doesn't exist).
- [ ] **Step 3: Implement.**
  - Every call runs `exec.CommandContext` with `cmd.Dir = r.Dir`, `--no-warnings`, `--js-runtimes` when set, and `cmd.WaitDelay = time.Second` (see `internal/player/extract.go`).
  - **The lock** is a priority mutex in `lock.go`: one holder, with foreground waiters served before background waiters, FIFO within each class.
  - `Resolve` prints with `screech-` prefixes, so parsing doesn't depend on output order.
  - Errors carry the last stderr line. A context error wraps `ctx.Err()`.
- [ ] **Step 4:** Run the tests and expect PASS. Also run `GOOS=linux go vet ./internal/ytdlp`.
- [ ] **Step 5: Commit** `ytdlp: one place runs yt-dlp — a lock, a working folder and a JS runtime.`

### Task 2: `internal/discover`, the song queue  *(Sonnet)*

**Files:**
- Create: `internal/discover/queue.go`, `youtube.go`, `bandcamp.go`, `titles.go`, `queue_test.go`, `bandcamp_test.go`, `titles_test.go`

**Interfaces (Produces).** No import of `ytdlp`; Task 6 adapts.

```go
type YTHit struct{ ID, Title string }
type YTMedia struct{ URL, Artist, Track, Title, Channel string; Duration float64; Views int64; Tags []string }
type YouTube interface {
    Songs(ctx context.Context, query string, n int) ([]YTHit, error)
    Mix(ctx context.Context, videoID string, n int) ([]YTHit, error)
    Resolve(ctx context.Context, page string) (YTMedia, error)
}
type BCItem struct{ TrackID int64; Artist, Title, StreamURL string; Duration float64 }
type Bandcamp interface{ Tag(ctx context.Context, tag, slice string) ([]BCItem, error) } // slice: "new"|"rand"|"top"
type BandcampClient struct{ HTTP *http.Client; UserAgent string; Endpoint string } // Endpoint "" = https://bandcamp.com/api/discover/1/discover_web
type Song struct {
    Key      string // "yt:<id>" or "bc:<trackid>": the no-repeat key
    Artist, Title string // cleaned
    URL      string // direct media URL
    Anchor, NewToYou bool
    Resolved time.Time
}
type Known func(artistKey string) bool // core.KnownArtist
type Queue struct{ /* … */ }
func NewQueue(seed string, yt YouTube, bc Bandcamp, known Known, rng *rand.Rand, now func() time.Time) *Queue
func (q *Queue) Next(ctx context.Context) (Song, error) // the next playable song; resolves media; never repeats within the last 200; never the same artist twice running
func (q *Queue) Skipped(quick bool)                     // quick = skipped within 60 s of play
func (q *Queue) Loved()
func (q *Queue) AnchorShare() float64                   // for tests
func BandcampTags(phrase string) []string               // "Swedish Death Metal" -> ["swedish-death-metal","death-metal","metal"]
func CleanTitle(artist, title string) (string, string)
func VideoID(link string) (string, bool)                // watch?v=, youtu.be/, music.youtube.com, shorts/; strips &list=…
var ErrNoSources = errors.New("discover: no source has songs for this seed")
```

- [ ] **Step 1: Write the failing tests** (fakes implement `YouTube` and `Bandcamp`):
  - `TestBandcampTags`: `"Swedish Death Metal"` → `["swedish-death-metal","death-metal","metal"]`; `" dungeon synth "` → `["dungeon-synth","synth"]`.
  - `TestBandcampClientRequestAndParse` (httptest): the POST body has `"tag_norm_names":["death-metal"]`, `"slice":"new"`, `"size":60` and `"include_result_types":["a"]`, and the User-Agent is the client's. A response with one album carrying `featured_track` and one without → one `BCItem{Artist:"Sheeple", Title:"Meat Your Death", StreamURL:…, Duration:156.72}`.
  - `TestCleanTitle`, a table:
    - `("Dismember","Dreaming In Red (OFFICIAL MUSIC VIDEO)")` → `("Dismember","Dreaming In Red")`;
    - `("Entombed - Topic","Left Hand Path [HD]")` → `("Entombed","Left Hand Path")`;
    - `("X","Song ft. Y")` → `("X","Song")`;
    - `("X","Song (Remastered 2019)")` stays as it is;
    - `("X","Song (Live at Wacken)")` stays as it is.
  - `TestVideoID`: `https://www.youtube.com/watch?v=abcdefghijk&list=PL1`, `https://youtu.be/abcdefghijk?t=3` and `https://music.youtube.com/watch?v=abcdefghijk` → `abcdefghijk`; `https://example.com` → false.
  - `TestQueueMixesAnchorsAndDiscoveries`: over 100 `Next` calls with plentiful fakes, the anchor share is between 0.12 and 0.30, and both sources appear.
  - `TestQueueNeverRepeatsOrDoublesArtist`: with 30 distinct songs over 6 artists, 60 calls never repeat a Key within 200 and never give the same artist twice in a row.
  - `TestQueueNewToYouFirst`: discoveries by unknown artists come before known ones, with `NewToYou` set.
  - `TestQueueSkipsOutOfRangeDurations`: a YouTube resolve of 45 s or 2 h is skipped.
  - `TestQueueRelabelsPopularDiscovery`: a discovery that resolves with `Views > 1_000_000` comes back `Anchor:true`.
  - `TestQueueAdaptsAnchorShare`: three `Skipped(true)` raise `AnchorShare()`, `Loved()` lowers it, and it stays within [1/8, 1/3].
  - `TestQueueLinkSeedUsesMix`: a seed that is a YouTube link calls `Mix` with its id and never calls `Songs` with the link text.
  - `TestQueueBandcampOnlyWhenYouTubeNil`: `yt == nil` still yields songs.
  - `TestQueueNoSourcesErrors`: an empty Bandcamp and a nil YouTube give `ErrNoSources` within the context deadline.
  - `TestQueueBandcampPacing`: two `Tag` calls for the same tag and slice within 10 s hit the cache.
- [ ] **Step 2:** Run `GOTOOLCHAIN=go1.26.10 go test ./internal/discover` and expect FAIL.
- [ ] **Step 3: Implement.** The approach the tests don't fix:
  - **Candidate pools.** YouTube anchors are `Songs(phrase, 5)`. YouTube discoveries are `Songs` results 10–50, plus variant queries (the phrase with `underground`, `demo` or `new`), or `Mix(id, 50)` for a link seed. Bandcamp anchors are `Tag(tag,"top")`; Bandcamp discoveries are `Tag(tag,"new")` and `Tag(tag,"rand")`, using the first tag from `BandcampTags` that returns anything.
  - **Each `Next`** rolls anchor versus discovery by `AnchorShare` (start 0.2), alternates the discovery source, and filters by no-repeat and no-same-artist. YouTube candidates are resolved (duration 90–720 s); Bandcamp items are already playable.
  - **Artist keys** come from `core.NormalizeArtist`.
  - **A link seed's Bandcamp phrase** is the first of its resolved `Tags` whose `BandcampTags` answers.
  - **Caches:** search 6 h, Bandcamp 30 min.
- [ ] **Step 4:** Run the tests and expect PASS.
- [ ] **Step 5: Commit** `discover: a queue that finds new bands in a sound, with a classic now and then.`

### Task 3: discovery stations in core, config and the CLI  *(Sonnet)*

**Files:**
- Create: `internal/core/discovery.go`, `internal/core/discovery_test.go`
- Modify:
  - `internal/core/store.go` (`PruneStale` spares `disc:%`; add `HeardArtistKey`, `RecentDiscoveries`)
  - `internal/core/core.go` (`directoryCount` skips `disc:`)
  - `internal/core/radiobrowser.go` (`Click` skips `disc:`)
  - `internal/core/pool.go` (the custom slice takes `IsCustom || IsDiscovery`; the reason for a discovery is `discovering · <name>`)
  - `internal/config/stations.go` (`Discover` field), `internal/config/edit.go`
  - `cmd/screech/main.go`, `cmd/screech/stations.go` (`add --discover`)
  - `internal/app/start.go` (`customStations` maps `Discover`)
  - their tests

**Interfaces (Produces):**

```go
const DiscoverScheme = "discover:"
func DiscoveryKey(seed string) string           // link -> "https://www.youtube.com/watch?v=<id>", else lower(trim(seed)) collapsed spaces
func DiscoveryUUID(seed string) string          // "disc:" + hex(sha256("discover:"+DiscoveryKey(seed)))[:16]
func IsDiscovery(uuid string) bool              // prefix "disc:"
func (c *Core) StartDiscovery(seed string, now time.Time) (Pick, error) // upsert row {UUID, Name: seed (CleanText), URL: "discover:"+seed, URLResolved same, LastCheckOK:true}; ends the current listen as TuneTo does; Reason "discovering · "+name
func (c *Core) RenameDiscovery(uuid, name string) error // only while the row's Name still equals its seed link
type DiscoveryRow struct{ UUID, Name string; LastAt time.Time }
func (c *Core) RecentDiscoveries(n int) ([]DiscoveryRow, error) // disc: stations by latest listen start, newest first
func (c *Core) KnownArtist(artistKey string) bool               // in tracks_heard or loved
// config
type Station struct{ /* existing */ Discover string `toml:"discover,omitempty"` }
func (s Station) ID() string // Discover != "" -> core.DiscoveryUUID(s.Discover)
```

- [ ] **Step 1: Write the failing tests:**
  - `TestDiscoveryKeyCanonicalisesLinks`: the three link forms from Task 2's `TestVideoID` give the same key; `" Swedish  Death Metal "` → `"swedish death metal"`.
  - `TestStartDiscoveryCreatesAndResumes`: the same seed twice gives one row with the same UUID. `StreamURL()` is `discover:Swedish death metal`, and the reason starts with `discovering`.
  - `TestDiscoveryRowsSurvivePruneAndCustomSync`: `PruneStale` and `SetCustomStations(nil, …)` both leave `disc:` rows.
  - `TestNeedsSyncIgnoresDiscovery`.
  - `TestRecentDiscoveriesNewestFirst`: three discoveries with listens at t1 < t2 < t3, and n=2 → [t3, t2].
  - `TestKnownArtist`: false before; true after `NoteTitle("Entombed - Left Hand Path")` is recorded on a listen.
  - `TestRenameDiscoveryOnlyReplacesLinkName`.
  - In `internal/config`: `TestStationDiscoverParses` (`discover = "dungeon synth"` with a name; both `url` and `discover` set → a warning and skipped). `TestAddDiscoverRoundTrip` via `addStationIn`.
  - In `cmd/screech`: `TestParseArgsAddDiscover` (`add --discover "dungeon synth" --name "Dungeon"`). Plain `add https://youtu.be/abcdefghijk` writes `discover =`.
- [ ] **Step 2:** Run `GOTOOLCHAIN=go1.26.10 go test ./internal/core ./internal/config ./cmd/screech` and expect FAIL.
- [ ] **Step 3: Implement** to the signatures above. No schema change: `disc:` rows are ordinary `stations` rows. `customStations` in `internal/app/start.go` turns a config `Discover` entry into a `core.Station` with `UUID: core.DiscoveryUUID(d)` and the sentinel URL, upserted through a new `(c *Core) SetDiscoveryStations([]Station, time.Time) error`, which never deletes.
- [ ] **Step 4:** Run the tests and expect PASS. Then run `go test -tags eval -run TestEval ./internal/core` and confirm the late-window E[like] is unchanged from `main` (no discovery stations exist in eval worlds).
- [ ] **Step 5: Commit** `Discovery stations: a disc: namespace, started from a phrase or a link, kept as history.`

### Task 4: the player plays a song queue  *(Sonnet)*

**Files:**
- Create: `internal/player/queue.go`, `internal/player/queue_test.go`
- Modify: `internal/player/mpv.go` (Play branch, the `eof`/`error` handling in queue mode, the `NewToYou` field), `internal/player/player.go` (`Event.NewToYou`), `internal/player/fakempv_test.go` (fake mpv answers a second `loadfile`)

**Interfaces (Produces):**

```go
type Song struct{ Key, Artist, Title, URL string; NewToYou bool }
type SongSource interface {
    Next(ctx context.Context) (Song, error)
    Skipped(quick bool)
    Loved()
}
type SongQueue interface{ NextSong() error; SongLoved() } // implemented by *MPV; session type-asserts it
func (m *MPV) SetSongSources(open func(seed string) (SongSource, error))
func IsDiscoverURL(u string) bool // prefix "discover:"
var ErrSourceNetwork = errors.New("player: song lookup failed on the network") // sources wrap this for network trouble
// Event gains: NewToYou bool (set on EventTitle while a queue plays)
```

- [ ] **Step 1: Write the failing tests** (fake mpv plus a scripted `SongSource`):
  - `TestQueuePlaysFirstSongWithTitle`: `Play("discover:x")` sends `loadfile <url1> replace -1 force-media-title=%n%A - T`.
  - `TestQueueAdvancesOnEOFWithoutStreamEnded`: the fake mpv sends `end-file reason=eof` → a second loadfile with song 2, and no `EventStreamEnded`.
  - `TestQueueSkipsFailedSongThenErrorsAfterThree`: three `end-file reason=error` in a row → one `EventStreamError`. A success in between resets the count.
  - `TestQueueSourceNetworkErrorIsDrop`: a `Next` returning `ErrSourceNetwork` makes `Play` return an error wrapping it (the session's offline path), not `ErrResolve`.
  - `TestQueueNextSongCallsSkipped`: `NextSong()` within 60 s of a load calls `Skipped(true)` and loads the next song.
  - `TestQueueNextWhileResolving`: two `NextSong()` calls while `Next` blocks → exactly one more loadfile.
  - `TestQueueTitleEventCarriesNewToYou`.
  - `TestPlayStillRejectsNonHTTP`: `ftp://…` and `discover:` with no `SetSongSources` both error.
- [ ] **Step 2:** Run `GOTOOLCHAIN=go1.26.10 go test -race ./internal/player -run Queue` and expect FAIL.
- [ ] **Step 3: Implement.**
  - **`Play("discover:seed")`** opens a source, gets the first song under an 8-s `ytdlp`-agnostic context, loads it, then starts a prefetch goroutine (20-s cap).
  - **On `eof` in queue mode,** load the prefetched song (or fetch one) and start the next prefetch. A newer `Play` cancels everything through the existing `playSeq`.
  - **Media URLs** are never logged.
- [ ] **Step 4:** Run the tests and expect PASS. Run `TestMPVLifecycle` locally with real mpv (`go test ./internal/player -run MPVLifecycle`).
- [ ] **Step 5: Commit** `player: a discovery station is a queue the player walks itself.`

### Task 5: session — `Discover`, next-song NEXT, new-to-you  *(Sonnet; after Tasks 3 and 4)*

**Files:**
- Modify: `internal/session/msgs.go` (`type Discover struct{ Query string }`), `session.go`, `tune.go`, `events.go`, `snapshot.go` (`NewToYou bool json:"newToYou"`, `Discovery bool json:"discovery"`), `stub_test.go` (stubPlayer implements `player.SongQueue` and records calls)
- Test: `internal/session/discover_test.go`

- [ ] **Step 1: Write the failing tests:**
  - `TestDiscoverIntentTunesDiscoveryStation`: the phase goes to Tuning, `Snapshot().Discovery` is true, and the reason starts with `discovering`.
  - `TestNextOnDiscoveryPlaysNextSong`: playing a discovery station, `Next{}` calls `NextSong` once, sets the feedback `NEXT SONG  Again for a new station`, and doesn't call `core.Tune`. The listen is unchanged.
  - `TestDoubleNextChangesStation`: a second `Next{}` within 3 s runs the station skip (a new PickSeq). After 3 s, it's next-song again.
  - `TestLoveOnDiscoveryTellsQueue`: `Love{}` calls `SongLoved`.
  - `TestTitleEventSetsNewToYou`.
  - `TestFirstTitleRenamesLinkStation`.
  - `TestNextOnRadioUnchanged`.
  - `TestPlayFailedNetworkTakesOfflinePath`: a `PlayFailed` whose `Err` wraps `player.ErrSourceNetwork` must not call `MarkStationFailed`. It goes through `failTune`, as a stream that never locked does, so the existing offline backoff applies.
- [ ] **Step 2:** Run `GOTOOLCHAIN=go1.26.10 go test ./internal/session` and expect FAIL.
- [ ] **Step 3: Implement.**
  - `Discover` runs a cmd calling `core.StartDiscovery`, which returns `Tuned` and lands through `applyPick`.
  - `next()` checks `core.IsDiscovery(s.Station.UUID)` and `s.pl.(player.SongQueue)`; it keeps `lastSongNextAt`.
  - Phases other than Playing and Buffering keep today's behaviour.
- [ ] **Step 4:** Run the tests and expect PASS, then run `go test ./internal/tui ./internal/gui` (they share the session).
- [ ] **Step 5: Commit** `session: NEXT means next song on a discovery station, twice means a new station.`

### Task 6: wiring, doctor and config semantics  *(Sonnet; after Tasks 1–4)*

**Files:**
- Modify:
  - `internal/config/config.go` (`ytdlp_path`: `""` = find, `"off"` = disabled; the page-link warning only when off or not found)
  - `internal/app/start.go` (`ytdlp.Find(cfg.YtdlpPath, exeDir, cfg.DataDir)`, `DetectJSRuntime`, `Runner{Dir: cfg.DataDir}`, `pl.SetExtractor` through the runner, `pl.SetSongSources` opening a `discover.Queue` via adapters)
  - `internal/player/extract.go` (`YTDLP.Extract` delegates to `ytdlp.Runner.Resolve`)
  - `cmd/screech/doctor.go`
- Create: `internal/app/discover.go` (adapters `ytdlp.Runner` → `discover.YouTube`, `discover.Queue` → `player.SongSource`, mapping network errors to `player.ErrSourceNetwork`)

- [ ] **Step 1: Write the failing tests:**
  - `TestYtdlpPathOffDisables` and `TestYtdlpPathEmptyFinds` in `internal/config`.
  - `TestStartWiresSongSources` in `internal/app`, with a fake mpv and a dataDir holding a fake `yt-dlp`.
  - `TestDoctorReportsYtdlpAndRuntime` in `cmd/screech`: the output contains the yt-dlp path, the version, `js runtime: node` (or `none`) and `discovery stations: N`.
  - The existing `TestFake*` page-link tests stay green through the delegation.
- [ ] **Step 2:** Run them and expect FAIL.
- [ ] **Step 3: Implement.**
  - **The adapter's first `Next`** after a source opens wraps its ctx in `ytdlp.Foreground`, so a tune's lookup jumps ahead of other stations' prefetches. Later calls stay background.
  - **Bandcamp client:** `discover.BandcampClient{HTTP: &http.Client{Timeout: 8*time.Second}, UserAgent: version.UserAgent()}`.
  - **Startup log** line: `discovery: yt-dlp <path> (<runtime>), bandcamp on`.
  - **Doctor** checks Bandcamp with one `Tag(ctx,"ambient","top")`. It suggests `yt-dlp -U` when the version date is more than 60 days before today.
- [ ] **Step 4:** Run `GOTOOLCHAIN=go1.26.10 go test -race ./internal/... ./cmd/...` and expect PASS.
- [ ] **Step 5: Commit** `Wire discovery up: yt-dlp found on its own, Bandcamp beside it, doctor says so.`

### Task 7: the window's FIND front door  *(Sonnet; after Task 5)*

**Files:**
- Modify:
  - `internal/gui/bridge.go`: `Discover(query string)`, `ResumeDiscovery(uuid string)`, and `RecentDiscoveries() []DiscoveryRow`, where `DiscoveryRow{UUID, Name string; LastAtMs int64}`; `RadioTop() []StationRow` (your most-listened non-own stations).
  - `internal/gui/rows.go`
  - `internal/gui/frontend/index.html`, `css/lcars.css`, `js/main.js`, `js/console.js`, `js/keys.js`, `js/bridge.js`
  - `internal/gui/fixtures_test.go`, `testdata/keys-test.js`, `bridge_test.go` (`TestBoundSurface`)
  - `cmd/screech-gui/main.go`: first launch opens unfolded on FIND when `Virgin`.

**Geometry (copy exactly from the spec and `front-door-v6.html`):**
- **Strip:** the ✕ cap is `#btnClose` at `432–480 × 0–20`, radius `0 10px 10px 0`, background `#FF5555`, with black `✕` (13 px, bold); clicking it calls `bridge.Quit()`. The tan segment is `332–428`. EXIT is removed, and `#btnMore` spans `376–480` with right radius 14.
- **Tabs:** `tabFind` 156 (default), `tabRadio` 188, RECENT 220, LOVED 252, STATIONS 284, TASTE 316, `tabMore` 348 (tan). SEARCH, INFO and SLEEP are gone as tabs; MORE renders the info rows and a SLEEP pill (`CycleSleep`, label `SLEEP` / `SLEEP nn`).
- **FIND bar** at `136–464 × 148–184`, radius 18:
  - the left cap `FIND` is 52 wide;
  - the field has a 2-px border;
  - the right cap `GO ▸` is 52 wide, the same colour as FIND (`--o`);
  - the input `.findin` uses the placeholder `Artist, song, genre or YouTube link`, lilac at 15 px. Typed text is ice at 16 px.
- **FIND label** at y 204. Up to four rows from y 220, 28 tall, each the name on the left and a relative time on the right. The hint is centred at y 348, `#777`, 11 px.
- **RADIO:** the same bar in `--blue` with the cap `RADIO`, placeholder `Station, genre or country`; Enter calls `Seed`. Rows from y 200 come from `RadioTop()`.
- **Strip status** shows `NEW TO YOU` when `snapshot.newToYou`.
- **Keys:**
  - `/` opens FIND and focuses the input; MORE opens FIND;
  - Tab cycles tabs (FIND → RADIO …); Esc folds;
  - `I` opens MORE; `H` opens LOVED; `Z` cycles sleep.
  - Enter in `.findin` calls `Discover`, then folds.

- [ ] **Step 1: Write the failing fixtures and tests:**
  - `console-find`, with four recent rows.
  - `console-find-empty`, with no rows (no label).
  - `console-find-typed`, with a 200-char YouTube link in the input. The guard's text-fit rule applies to `.findin` with the `ellipsis` class.
  - `console-radio`.
  - `console-more`, with sleep set.
  - Update every strip fixture's expectation for `#btnClose` and the absence of `#btnExit`.
  - `TestKeyboardMap` rows: `/`, Tab from FIND to RADIO, Esc folds, Enter in FIND calls `Discover`.
  - `TestBoundSurface` lists the new methods.
- [ ] **Step 2:** Run `go test -tags layout -run "Layout|KeyboardMap" -v ./internal/gui` and expect FAIL.
- [ ] **Step 3: Implement.**
  - **Inner corners** use the existing `.ic` pattern only. No new square black notch.
  - **`data-*` guard markers** go on every new element: `data-box`, `data-text`, `data-leaf`, `data-fixed`/`data-id` for the strip's `#btnClose`.
  - **Every box edge** is a multiple of 4.
- [ ] **Step 4:** Run `go test -tags layout -run "Layout|KeyboardMap" -count=1 ./internal/gui` and `go test -race ./internal/gui ./cmd/screech-gui`, and expect PASS. Look at `.review/gui/console-find@1.png` next to the mockup.
- [ ] **Step 5: Commit** `Window: FIND first — one bar for a discovery station, RADIO beside it, ✕ in the corner.`

### Task 8: the terminal's FIND prompt  *(Sonnet; after Task 5)*

**Files:**
- Modify: `internal/tui/app.go` (`promptMode` of `findMode|radioMode`), `keys.go` (prompt keys: Tab toggles mode, Enter sends `session.Discover` in FIND or `session.Seed` in RADIO), `chrome.go` (the prompt line), `help.go` (`/` now reads `find`)
- Test: `internal/tui/find_test.go`

- [ ] **Step 1: Write the failing tests:**
  - `TestSlashOpensFindPrompt`: the view contains `FIND` and the placeholder `artist, song, genre or YouTube link`.
  - `TestTabTogglesRadio`: the prompt shows `RADIO` and `station, genre or country`.
  - `TestEnterInFindSendsDiscover` and `TestEnterInRadioSendsSeed`, through the existing intent recorder.
  - `TestFindPromptListsRecentDiscoveries`: with three `disc:` listens, an empty buffer shows `pick up where you left off` and the names. ↓ and Enter send `TuneTo` for the second one.
  - `TestFindHintLine`: `esc close` and `tab radio` appear, written with `m.th.G` dots.
  - Then run `TestViewFitsEveryTerminalSize` and `TestASCIIModeIsSevenBit` unchanged.
- [ ] **Step 2:** Run `GOTOOLCHAIN=go1.26.10 go test ./internal/tui` and expect FAIL.
- [ ] **Step 3: Implement** inside the existing prompt overlay. The recent list shows only when the buffer is empty, has at most 3 rows, and is dropped on short terminals before anything else.
- [ ] **Step 4:** Run `go test -race ./internal/tui` and expect PASS.
- [ ] **Step 5: Commit** `Terminal: / finds — a discovery station by default, Tab for radio.`

### Task 9: docs  *(Haiku; after Tasks 1–8)*

**Files:**
- Modify: `README.md` (the FIND front door, discovery stations, NEXT next-song, yt-dlp + JS runtime, ✕), `SPEC.md` (the architecture paragraph gains `ytdlp` and `discover`; the station-data `disc:` namespace; the queue; the bridge methods; the tabs), `CHANGELOG.md` (under `## 0.9.0`, for listeners), `AGENTS.md` (the package list gains `internal/ytdlp` and `internal/discover`)

- [ ] **Step 1:** Write the sections, copying exact strings from the Global Constraints.
- [ ] **Step 2: Commit** `Docs: discovery stations, the FIND front door, and yt-dlp that finds itself.`

### Task 10: owner's machine, build and live check  *(lead)*

- [ ] **Step 1:** Copy `E:/Projects/screech-pilot/yt-dlp.exe` to `%APPDATA%/screech/yt-dlp.exe`. In `%APPDATA%/screech/config.toml`, remove the `ytdlp_path = "yt-dlp"` line, and replace the `yt-restream` station with `name = "Swedish death metal"`, `discover = "swedish death metal"`.
- [ ] **Step 2:** Run the full AGENTS.md gate, plus the layout guard, plus `CGO_ENABLED=0 go build -tags desktop,production -trimpath -ldflags "-H windowsgui -s -w" -o dist/screech-gui.exe ./cmd/screech-gui` and `go build -o dist/screech.exe ./cmd/screech`.
- [ ] **Step 3:** Run `dist/screech.exe doctor`. Expect yt-dlp found, js runtime node, Bandcamp answered.
- [ ] **Step 4: Live check:** start `swedish death metal` from FIND and hear three songs in a row; NEXT, then a double NEXT; paste a YouTube link; the RADIO tab; ✕.
