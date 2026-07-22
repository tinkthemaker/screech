// Package core is screech's portable heart: station cache, listen/track log,
// taste model (Thompson-sampling bandit + tag affinities + empirical
// fingerprints), and the radio-browser client. It knows nothing about UIs.
package core

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"
)

type Core struct {
	mu    sync.Mutex
	store *Store
	rb    *RadioBrowser
	rng   *rand.Rand

	logf func(string, ...any) // non-fatal error sink; nil = drop (tests)

	stations []Station
	bandit   map[string]map[string]banditRow // station -> daypart -> row
	tags     map[string]banditRow            // tag -> weight row (Alpha=weight)
	loved    map[string]bool                 // loved artist keys
	fp       *fingerprints
	presets  map[int]string // slot 1-9 -> station uuid

	currentUUID  string
	previousUUID string
	listenID     int64
	listenStart  time.Time

	lastRawTitle string
	curTrack     Track
	hasTrack     bool
	everTrack    bool
	suspectAd    bool
}

func Open(dbPath string) (*Core, error) {
	st, err := OpenStore(dbPath)
	if err != nil {
		return nil, err
	}
	c := &Core{
		store: st,
		rb:    NewRadioBrowser(),
		rng:   rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	// Seeds are merged on every open: idempotent, guarantees playable
	// fallbacks exist no matter what state the cache is in.
	if err := st.UpsertStations(SeedStations(), time.Now()); err != nil {
		st.Close()
		return nil, err
	}
	if err := c.reload(); err != nil {
		st.Close()
		return nil, err
	}
	return c, nil
}

func (c *Core) reload() error {
	stations, err := c.store.LoadStations()
	if err != nil {
		return err
	}
	bandit, err := c.store.AllBandit()
	if err != nil {
		return err
	}
	tags, err := c.store.AllTagAffinity()
	if err != nil {
		return err
	}
	loved, err := c.store.LovedArtistKeys()
	if err != nil {
		return err
	}
	sa, err := c.store.StationArtists()
	if err != nil {
		return err
	}
	presets, err := c.store.Presets()
	if err != nil {
		return err
	}
	c.presets = presets
	c.stations = stations
	c.bandit = bandit
	c.tags = tags
	c.loved = loved
	c.fp = newFingerprints(sa)
	return nil
}

func (c *Core) Close() error {
	return c.store.Close()
}

// SetLogger installs a sink for non-fatal errors that would otherwise be
// swallowed — chiefly best-effort persistence writes. Playback never depends
// on these succeeding, but a silently failing store means the taste model
// stops learning with no trace, so we leave a breadcrumb in the run log. Safe
// to leave unset (tests do): errors are then dropped as before.
func (c *Core) SetLogger(logf func(string, ...any)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logf = logf
}

// warn reports a non-fatal error under op if a logger is installed. Callers
// must hold c.mu (all current callers do) since it reads c.logf.
func (c *Core) warn(op string, err error) {
	if err == nil || c.logf == nil {
		return
	}
	c.logf("core: %s: %v", op, err)
}

func (c *Core) StationCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.stations)
}

// NeedsSync reports whether the cache is effectively empty (seeds only).
func (c *Core) NeedsSync() bool {
	return c.StationCount() <= len(seedStations)
}

// Sync refreshes the station cache from radio-browser. On failure the
// existing cache (at minimum the seeds) keeps working; the error is returned
// for the status line, not as a stop condition.
func (c *Core) Sync(ctx context.Context, limit int) (int, error) {
	sts, err := c.rb.FetchTop(ctx, limit)
	if err != nil {
		return 0, err
	}
	if err := c.store.UpsertStations(sts, time.Now()); err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.reload(); err != nil {
		return 0, err
	}
	c.warn("set last_sync meta", c.store.SetMeta("last_sync", fmt.Sprint(time.Now().Unix())))
	return len(sts), nil
}

func (c *Core) StationByUUID(uuid string) (Station, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.stations {
		if c.stations[i].UUID == uuid {
			return c.stations[i], true
		}
	}
	return Station{}, false
}

// ResumeUUID returns the station playing when screech last quit.
func (c *Core) ResumeUUID() string {
	v, _ := c.store.GetMeta("last_station")
	return v
}

// IsVirgin reports whether screech has never played anything (first run).
func (c *Core) IsVirgin() bool {
	return c.ResumeUUID() == ""
}

// Presets returns a copy of the preset slots (1-9 -> station uuid).
func (c *Core) Presets() map[int]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[int]string, len(c.presets))
	for k, v := range c.presets {
		out[k] = v
	}
	return out
}

// TogglePreset saves the current station to the lowest free slot, or unsaves
// it if already saved. Presets are pure recall: they never touch the taste
// model. full=true means all nine slots are taken.
func (c *Core) TogglePreset(now time.Time) (slot int, saved bool, full bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.currentUUID == "" {
		return 0, false, false
	}
	for s, u := range c.presets {
		if u == c.currentUUID {
			delete(c.presets, s)
			c.warn("delete preset", c.store.DeletePreset(s))
			return s, false, false
		}
	}
	for s := 1; s <= 9; s++ {
		if _, used := c.presets[s]; !used {
			c.presets[s] = c.currentUUID
			c.warn("save preset", c.store.SetPreset(s, c.currentUUID, now))
			return s, true, false
		}
	}
	return 0, false, true
}

// TuneTo ends the current listen (dwell time judges it, as always) and hands
// back the requested station deterministically. Used by preset recall.
func (c *Core) TuneTo(uuid string, now time.Time) (Pick, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.stationByUUIDLocked(uuid)
	if st == nil {
		return Pick{}, fmt.Errorf("station no longer in cache")
	}
	c.endListenLocked(now, true)
	return Pick{Station: *st, Reason: "preset"}, nil
}

// Tune ends the current listen (if any) with skip semantics and picks the
// next station. The fast-skip judgment happens here: dwell time carries the
// negative signal, there is no separate skip control.
func (c *Core) Tune(now time.Time) (Pick, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.endListenLocked(now, true)
	return c.pick(now, c.rng)
}

// StartListen begins logging a listen on the given station. Call once the
// player has actually been pointed at it.
func (c *Core) StartListen(uuid string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.currentUUID != "" && c.currentUUID != uuid {
		c.previousUUID = c.currentUUID
	}
	c.currentUUID = uuid
	c.listenStart = now
	c.lastRawTitle = ""
	c.curTrack = Track{}
	c.hasTrack = false
	c.everTrack = false
	c.suspectAd = false
	id, err := c.store.InsertListen(uuid, now, DaypartFor(now))
	if err != nil {
		// listenID stays 0, so endListenLocked won't record a reward for this
		// listen. Non-fatal, but the taste signal is lost — say so.
		c.warn("insert listen", err)
	} else {
		c.listenID = id
	}
	c.warn("set last_station meta", c.store.SetMeta("last_station", uuid))
	c.rb.Click(uuid)
}

// EndListen closes the current listen without a skip judgment (quit, stream
// death). Use Tune for user-driven changes.
func (c *Core) EndListen(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.endListenLocked(now, false)
}

func (c *Core) endListenLocked(now time.Time, userSkip bool) {
	if c.currentUUID == "" || c.listenID == 0 {
		return
	}
	dur := now.Sub(c.listenStart)
	skipFast := userSkip && dur < fastSkipWindow
	duringAd := skipFast && c.suspectAd
	c.warn("finish listen", c.store.FinishListen(c.listenID, now, skipFast, duringAd))

	daypart := DaypartFor(c.listenStart)
	switch {
	case skipFast && duringAd:
		c.applyRewardLocked(c.currentUUID, daypart, 0, skipBetaDuringAd, now)
	case skipFast:
		c.applyRewardLocked(c.currentUUID, daypart, 0, skipBeta, now)
	case dur >= fastSkipWindow:
		c.applyRewardLocked(c.currentUUID, daypart, listenAlpha(dur), 0, now)
		if st := c.stationByUUIDLocked(c.currentUUID); st != nil {
			bump := clamp(dur.Minutes()/30, 0.05, 1.0)
			for _, t := range st.TagList() {
				c.bumpTagLocked(t, bump, now)
			}
		}
	}
	c.listenID = 0
}

// NoteTitle ingests a raw StreamTitle from the player. Returns the parsed
// track (if usable) and whether the title smells like an ad break.
func (c *Core) NoteTitle(raw string, now time.Time) (Track, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if raw == c.lastRawTitle || c.currentUUID == "" {
		return c.curTrack, c.hasTrack, c.suspectAd
	}
	c.lastRawTitle = raw

	st := c.stationByUUIDLocked(c.currentUUID)
	name := ""
	if st != nil {
		name = st.Name
	}
	suspect := SuspectAd(raw, name)
	if suspect && !c.everTrack {
		// Streams without ICY support surface the station name (or nothing)
		// as a permanent title. That's not an ad break, it's silence — only
		// suspect these once this listen has produced a real track.
		low := strings.ToLower(strings.TrimSpace(raw))
		if low == "" || low == strings.ToLower(strings.TrimSpace(name)) {
			suspect = false
		}
	}
	c.suspectAd = suspect

	tr, ok := ParseTitle(raw, name)
	if ok {
		c.curTrack = tr
		c.hasTrack = true
		c.everTrack = true
		c.warn("insert track", c.store.InsertTrack(c.currentUUID, tr.ArtistKey, tr.Artist, tr.Title, raw, now))
		c.fp.note(c.currentUUID, tr.ArtistKey)
	} else {
		c.hasTrack = false
	}
	return c.curTrack, c.hasTrack, c.suspectAd
}

// Love records a strong positive on the current track (or, with no parsed
// track, on the station itself). Credits station α, tag affinities, and the
// loved-artist set that drives fingerprint overlap.
func (c *Core) Love(now time.Time) (Track, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.currentUUID == "" {
		return Track{}, false
	}
	daypart := DaypartFor(now)
	c.applyRewardLocked(c.currentUUID, daypart, loveAlpha, 0, now)
	if st := c.stationByUUIDLocked(c.currentUUID); st != nil {
		for _, t := range st.TagList() {
			c.bumpTagLocked(t, 0.5, now)
		}
	}
	if !c.hasTrack {
		return Track{}, false
	}
	tr := c.curTrack
	c.warn("insert loved", c.store.InsertLoved(c.currentUUID, tr.ArtistKey, tr.Artist, tr.Title, now))
	if tr.ArtistKey != "" {
		c.loved[tr.ArtistKey] = true
	}
	return tr, true
}

// MarkStationFailed bumps a station's failure count (stream wouldn't start).
// Three strikes removes it from candidate pools.
func (c *Core) MarkStationFailed(uuid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.warn("bump fail count", c.store.BumpFailCount(uuid, 1))
	if st := c.stationByUUIDLocked(uuid); st != nil {
		st.FailCount++
	}
}

// MarkStationHealthy resets the failure count after a successful play.
func (c *Core) MarkStationHealthy(uuid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.warn("reset fail count", c.store.BumpFailCount(uuid, -maxFailCount))
	if st := c.stationByUUIDLocked(uuid); st != nil {
		st.FailCount = 0
	}
}

func (c *Core) SuspectedAd() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.suspectAd
}

// --- internals ---

func (c *Core) stationByUUIDLocked(uuid string) *Station {
	for i := range c.stations {
		if c.stations[i].UUID == uuid {
			return &c.stations[i]
		}
	}
	return nil
}

// applyRewardLocked adds (dAlpha, dBeta) to the given daypart row and the
// all-time row, decaying each toward the prior first (lazy decay).
func (c *Core) applyRewardLocked(uuid, daypart string, dAlpha, dBeta float64, now time.Time) {
	for _, dp := range []string{daypart, DaypartAll} {
		if dp == "" {
			continue
		}
		row := banditRow{Alpha: 1, Beta: 1, UpdatedAt: now}
		if rows := c.bandit[uuid]; rows != nil {
			if r, ok := rows[dp]; ok {
				row = r
			}
		}
		a, b := row.decayed(now)
		a += dAlpha
		b += dBeta
		nr := banditRow{Alpha: a, Beta: b, UpdatedAt: now}
		if c.bandit[uuid] == nil {
			c.bandit[uuid] = map[string]banditRow{}
		}
		c.bandit[uuid][dp] = nr
		c.warn("put bandit", c.store.PutBandit(uuid, dp, a, b, now))
	}
}

func (c *Core) bumpTagLocked(tag string, delta float64, now time.Time) {
	row := banditRow{Alpha: 1, UpdatedAt: now}
	if r, ok := c.tags[tag]; ok {
		row = r
	}
	w := decayToward(row.Alpha, row.UpdatedAt, now) + delta
	c.tags[tag] = banditRow{Alpha: w, UpdatedAt: now}
	c.warn("put tag affinity", c.store.PutTagAffinity(tag, w, now))
}

// SeedResult is what a typed seed query resolved to.
type SeedResult struct {
	Pick  Pick
	Kind  string // "tag" or "artist"
	Label string
}

// Seed tilts the taste model toward a typed query and picks a station for it.
// Genres are strong (tag data exists for every cached station). Artists are
// honest-best-effort: the name is pseudo-loved so fingerprints catch it as
// listening accumulates, and the directory is searched for stations named for
// it. Song titles can't be resolved; "artist - song" input uses the artist.
func (c *Core) Seed(ctx context.Context, query string, now time.Time) (SeedResult, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	if i := strings.Index(q, " - "); i > 0 {
		q = strings.TrimSpace(q[:i])
	}
	if q == "" {
		return SeedResult{}, false
	}

	// Tag path: offline, strong.
	c.mu.Lock()
	if tag, n := c.matchTagLocked(q); n >= 3 {
		c.bumpTagLocked(tag, 2.0, now) // love-sized bump; decays like the rest
		cands := c.candidatesFor(now, func(st *Station) bool {
			for _, t := range st.TagList() {
				if t == tag {
					return true
				}
			}
			return false
		})
		if cd, _, err := c.pickFrom(cands, now, c.rng); err == nil {
			c.endListenLocked(now, true)
			p := Pick{Station: *cd.st, Reason: "seeded: " + tag}
			c.mu.Unlock()
			return SeedResult{Pick: p, Kind: "tag", Label: tag}, true
		}
	}

	// Artist path: record the intent as a pseudo-love regardless of whether a
	// station turns up now — fingerprints will catch the artist later.
	key := NormalizeArtist(q)
	if key != "" && !c.loved[key] {
		c.loved[key] = true
		c.warn("insert loved (seed)", c.store.InsertLoved("", key, strings.TrimSpace(query), "", now))
	}
	c.mu.Unlock()

	// Directory search by name, outside the lock (network).
	if found, err := c.rb.SearchByName(ctx, q, 20); err == nil && len(found) > 0 {
		c.mu.Lock()
		c.warn("upsert stations (seed)", c.store.UpsertStations(found, now))
		c.warn("reload after seed", c.reload())
		c.mu.Unlock()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	cands := c.candidatesFor(now, func(st *Station) bool {
		return strings.Contains(strings.ToLower(st.Name), q)
	})
	if len(cands) == 0 {
		// Loose fallback: the query hiding inside a tag.
		cands = c.candidatesFor(now, func(st *Station) bool {
			for _, t := range st.TagList() {
				if strings.Contains(t, q) {
					return true
				}
			}
			return false
		})
	}
	cd, _, err := c.pickFrom(cands, now, c.rng)
	if err != nil {
		return SeedResult{Kind: "artist", Label: q}, false
	}
	c.endListenLocked(now, true)
	return SeedResult{Pick: Pick{Station: *cd.st, Reason: "seeded: " + q}, Kind: "artist", Label: q}, true
}

// matchTagLocked resolves a query against the station tag vocabulary.
// Exact beats prefix beats contains; popularity breaks ties.
func (c *Core) matchTagLocked(q string) (string, int) {
	counts := map[string]int{}
	for i := range c.stations {
		for _, t := range c.stations[i].TagList() {
			counts[t]++
		}
	}
	if n := counts[q]; n > 0 {
		return q, n
	}
	if len(q) < 3 {
		return "", 0
	}
	best, bestN, bestRank := "", 0, 0
	for t, n := range counts {
		rank := 0
		switch {
		case strings.HasPrefix(t, q):
			rank = 2
		case strings.Contains(t, q):
			rank = 1
		}
		if rank > bestRank || (rank == bestRank && rank > 0 && n > bestN) {
			best, bestN, bestRank = t, n, rank
		}
	}
	return best, bestN
}
