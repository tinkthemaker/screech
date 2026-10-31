# Song Radio, Resume and Diagnostics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:**
- FIND plays the song or artist the listener typed first, then shuffles similar styles, like any algorithmic radio.
- A reconnect on a discovery station resumes the current song instead of skipping it.
- `screech.log` says how each discovery song ended.

**Spec:** `docs/superpowers/specs/2026-10-28-youtube-stations-design.md`, section "Revision, 2026-10-31: song radio, resume, diagnostics". That section binds; read it in full.

**Branch:** `fix/song-radio`, cut from `main` at `5c68781`.

## Global Constraints

- Everything in `AGENTS.md` still holds:
  - comments explain *why*, in the existing voice;
  - network text goes through `core.CleanText`;
  - no unit test touches the network or runs yt-dlp (`httptest` and interface fakes; see `internal/discover/*_test.go`);
  - never log a media URL;
  - the eval harness late-window E[like] stays unchanged (genre 0.618/0.602, station 0.496/0.496, genre+quality 0.601/0.584, genre+links 0.630/0.609).
- The first-song budget stays 8 s. The Bandcamp search is capped at 2 s and runs in parallel with the first YouTube lookup.
- `GOTOOLCHAIN=go1.26.10` for every Go command. The host is Windows with Git Bash.

### Task A: song radio in `internal/discover`, plus its wiring in `internal/app`  *(Sonnet)*

**Files:**
- `internal/discover/` (new `search.go` for the Bandcamp search client; changes to `queue.go`, `youtube.go`, `bandcamp.go` and `titles.go`; tests)
- `internal/app/discover.go` (adapter wiring and its tests)

**Interfaces (Produces):**

```go
// internal/discover/search.go
type SearchResult struct {
    Tags   []string      // norm_names of exact-or-near tag matches, best first
    Bands  []SearchBand  // type "b"
    Tracks []SearchTrack // type "t" and "a"
}
type SearchBand struct{ Name, Genre, URL string; Tags []string }
type SearchTrack struct{ Name, Band, URL string; Album bool }
type Searcher interface{ Search(ctx context.Context, text string) (SearchResult, error) }
// BandcampClient gains: func (c BandcampClient) Search(ctx context.Context, text string) (SearchResult, error)
//   POST https://bandcamp.com/api/bcsearch_public_api/1/autocomplete_elastic
//   body {"search_text":text,"search_filter":"","full_page":false,"fan_id":null}
//   response: {"auto":{"results":[{type,name,band_name,genre_name,tag_names,item_url_root,item_url_path,...}]},"tag":{"matches":[{norm_name,count,...}]},"genre":{...}}
// NewQueue's bc argument may also implement Searcher (BandcampClient does); a nil or non-Searcher bc means "can't classify": treat the phrase as step 4.
```

`Song` gains `Duration time.Duration`, filled from `YTMedia.Duration` or `BCItem.Duration`.

**Behaviour:** exactly as the spec revision's "What a typed phrase becomes" and "Where song radio finds songs":
- classify, with steps 1–4 in order;
- the seed song is served first;
- style tags (at most 3, filtered);
- discoveries from Bandcamp style-tag feeds, the seed's Mix plus a one-hop Mix crawl to other artists, and YTM searches on style tags;
- anchors from Bandcamp `top` and YTM style-tag top 5;
- the 8-song artist spacing on song and artist radio only;
- the typed phrase is never searched again, and words are never dropped from it;
- genre stations (step 3) behave exactly as today;
- title cleanup: a literal `null`/`NA`/`None` counts as empty, and trailing commas are trimmed from artists.

**Tests** (fakes only):
- `TestClassifyArtist` ("chelsea wolfe" with a band match plus a tag match: artist radio wins);
- `TestClassifySong` ("engorged in putrescence" with a track match: song radio, the first song is the matching track, style tags come from the band);
- `TestClassifyGenre` ("swedish death metal" with only a tag match: genre station, unchanged);
- `TestClassifyFallsBackToTopHit` (no matches);
- `TestClassifyBandcampDown`;
- `TestSongRadioNeverSearchesTheTypedPhraseAgain` (the fake YouTube records queries);
- `TestSongRadioSpacesArtists` (a Mix that is 90% one band gives no artist twice in any 8);
- `TestSongRadioHopsToOtherArtists`;
- `TestStyleTagFiltering` (genre_name and location dropped, at most 3);
- `TestCleanTitleNullAndCommas`;
- a `BandcampClient.Search` request/parse test with `httptest`;
- `TestSongDurationCarried`.

Keep every existing test green, and run the eval harness.

- [ ] Fail first, implement, then run `go test -race -count=1 ./internal/discover ./internal/app`. Commit `discover: what you type plays first, then songs like it.`

### Task B: resume on reconnect, and diagnostics, in `internal/player`  *(Sonnet)*

**Files:**
- `internal/player/` (`queue.go`, `mpv.go`, `player.go`, tests)

Do NOT touch `internal/app` or `internal/discover`; Task A owns them.

**Interfaces (Produces):**

```go
// player.Song gains Duration time.Duration (0 = unknown)
// func (m *MPV) SetSongLog(logf func(format string, args ...any))  // nil = no song lines
```

**Behaviour:** as the spec revision's "Reconnects resume the song" and "Diagnostics".
- **Position tracking.** The player tracks the current song's position as wall time since it started playing, minus paused time (`EventPaused`/`EventResumed` and the pause it sets itself).
- **Resume.** `Play("discover:"+seed)` for the seed already registered, while a current song is in progress with `pos < Duration-15s` (or Duration unknown and `pos > 0`), reloads that song with `loadfile <url> replace -1 start=<pos>,force-media-title=…`. It keeps the queue, its prefetch and its history.
  - At most 2 resumes per song; the third reconnect advances.
  - A URL older than 1 h asks the source again: the source can't re-resolve, so treat it as an advance.
- **Diagnostics.** One `logf` line per song end: `song <key> <source> expected <d>s played <p>s ended <why>`.
  - `<why>` is one of `eof`, `error: <mpv file_error>`, `next`, `new station`, `resumed`.
  - The source is `bandcamp` for a `bc:` key; for a YouTube key it is the URL's `c=` value (e.g. `c=VISIONOS`), or `youtube` when it has none.
  - Never the URL.

**Tests** (the fake mpv):
- `TestReconnectResumesTheSong`: the second `Play` of the same seed mid-song gives a `loadfile` with the same URL and `start=`; no `Next` call.
- `TestReconnectNearTheEndAdvances`.
- `TestResumeAtMostTwice`.
- `TestPausedTimeNotCounted`.
- `TestSongLogLines`: each ending kind writes its line, and no line contains `http`.
- `TestDifferentSeedStillStartsFresh`.

Run `TestMPVLifecycle` and `TestMPVSongQueue` against real mpv.

- [ ] Fail first, implement, then run `go test -race -count=1 ./internal/player ./internal/session`. Commit `player: a reconnect picks the song up where it stopped, and the log says how each song ended.`

### Task C: wiring and docs  *(controller, after A and B merge)*

- In `internal/app/discover.go`'s `songAdapter`: map `discover.Song.Duration` to `player.Song.Duration`, and call `pl.SetSongLog(log.Linef)`.
- README and CHANGELOG lines for listeners, under `## 0.9.0`, since it's unreleased.
- Run the full AGENTS.md gate, the layout guard and the eval. Then live: "engorged in putrescence", "chelsea wolfe" and "swedish death metal", and a forced reconnect.
