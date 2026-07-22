// Package core is screech's portable heart: station cache, listen/track log,
// taste model (Thompson-sampling bandit + tag affinities + empirical
// fingerprints), and the radio-browser client. It knows nothing about UIs.
package core

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Core struct {
	mu    sync.Mutex
	store *Store
	rb    *RadioBrowser
	rng   *rand.Rand

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
	seedTag      string
	seedPicks    int
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

// Close ends the current listen (a clean quit banks whatever credit the
// dwell time earned) and closes the store.
func (c *Core) Close() error {
	c.mu.Lock()
	c.endListenLocked(time.Now(), false)
	c.mu.Unlock()
	return c.store.Close()
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

// Sync refreshes the station cache from radio-browser, then prunes stations
// the directory no longer vouches for (unless they carry user history). On
// failure the existing cache (at minimum the seeds) keeps working; the error
// is returned for the status line, not as a stop condition.
func (c *Core) Sync(ctx context.Context, limit int) (added int, pruned int, err error) {
	sts, err := c.rb.FetchTop(ctx, limit)
	if err != nil {
		return 0, 0, err
	}
	fetchedAt := time.Now()
	if err := c.store.UpsertStations(sts, fetchedAt); err != nil {
		return 0, 0, err
	}
	// Anything older than this sync that the fresh slice didn't re-vouch for
	// and that has no history is a corpse candidate.
	n, err := c.store.PruneStale(fetchedAt, c.ResumeUUID())
	if err != nil {
		return len(sts), 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.reload(); err != nil {
		return len(sts), int(n), err
	}
	_ = c.store.SetMeta("last_sync", fmt.Sprint(time.Now().Unix()))
	return len(sts), int(n), nil
}

// SyncStale reports whether the cache deserves a background refresh: the
// last sync is missing or older than a week, or the cache holds less than
// half the configured slice (e.g. after the default limit grew).
func (c *Core) SyncStale(limit int) bool {
	if c.NeedsSync() {
		return false // the first-run background sync owns this case
	}
	v, _ := c.store.GetMeta("last_sync")
	if v == "" {
		return true
	}
	ts, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return true
	}
	if time.Since(time.Unix(ts, 0)) > 7*24*time.Hour {
		return true
	}
	return c.StationCount() < limit/2
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

// TopListened returns the listen-time leaderboard for the history view.
func (c *Core) TopListened(n int) ([]HistEntry, error) {
	return c.store.TopListened(n)
}

// SavedStations returns the library's unified saved-stations list: presets
// and loved stations together, most-listened first.
func (c *Core) SavedStations() ([]SavedStation, error) {
	return c.store.SavedStations()
}

// RemoveStation forgets a saved station from the library list: clears its
// preset slot and deletes the loved rows credited to it, then resyncs the
// in-memory loved set and presets. Listen history and bandit counts stay —
// removing recall is not rewriting taste.
func (c *Core) RemoveStation(uuid string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, _, err := c.store.RemoveStation(uuid); err != nil {
		return err
	}
	presets, err := c.store.Presets()
	if err != nil {
		return err
	}
	c.presets = presets
	loved, err := c.store.LovedArtistKeys()
	if err != nil {
		return err
	}
	c.loved = loved
	return nil
}

func (c *Core) RecentlyHeard(n int) ([]RecentTrack, error) {
	return c.store.RecentlyHeard(n)
}

// LovedTracks returns the user's deduplicated track library, newest first.
func (c *Core) LovedTracks(n int) ([]LovedTrack, error) {
	return c.store.LovedTracks(n)
}

// ForgetLovedTrack removes a track from recall without undoing historical
// listens or rewards already learned by the station bandit.
func (c *Core) ForgetLovedTrack(artistKey, title string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	removed, err := c.store.ForgetLovedTrack(artistKey, title)
	if err != nil || !removed {
		return removed, err
	}
	loved, err := c.store.LovedArtistKeys()
	if err != nil {
		return true, err
	}
	c.loved = loved
	return true, nil
}

// IsVirgin reports whether screech has never played anything (first run).
func (c *Core) IsVirgin() bool {
	return c.ResumeUUID() == ""
}

// Volume is Screech's persisted player level, independent of system volume.
func (c *Core) Volume() int {
	v, _ := c.store.GetMeta("volume")
	n, err := strconv.Atoi(v)
	if err != nil {
		return 100
	}
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}

func (c *Core) SetVolume(percent int) error {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return c.store.SetMeta("volume", strconv.Itoa(percent))
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
			_ = c.store.DeletePreset(s)
			return s, false, false
		}
	}
	for s := 1; s <= 9; s++ {
		if _, used := c.presets[s]; !used {
			c.presets[s] = c.currentUUID
			_ = c.store.SetPreset(s, c.currentUUID, now)
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

// TuneDead picks the next station after the current stream failed. The
// dead listen closes without skip semantics: a stream that never played
// teaches nothing about taste, and the failure already cost the station a
// strike. Judging it as a fast skip would double-charge the station and
// misrecord network trouble as dislike.
func (c *Core) TuneDead(now time.Time) (Pick, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.endListenLocked(now, false)
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
	if err == nil {
		c.listenID = id
	}
	_ = c.store.SetMeta("last_station", uuid)
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
	_ = c.store.FinishListen(c.listenID, now, skipFast, duringAd)

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
// track (if usable), whether the title smells like an ad break, and whether
// the track is already in the loved set — so the UI can light the heart on
// tracks loved in earlier sessions, not just ones loved this listen.
func (c *Core) NoteTitle(raw string, now time.Time) (Track, bool, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if raw == c.lastRawTitle || c.currentUUID == "" {
		return c.curTrack, c.hasTrack, c.suspectAd, c.trackLovedLocked()
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
		_ = c.store.InsertTrack(c.currentUUID, tr.ArtistKey, tr.Artist, tr.Title, raw, now)
		c.fp.note(c.currentUUID, tr.ArtistKey)
	} else {
		c.hasTrack = false
	}
	return c.curTrack, c.hasTrack, c.suspectAd, c.trackLovedLocked()
}

// trackLovedLocked reports whether the current parsed track is in the loved
// set (artist + title match, or the artist alone when the track has no
// usable title key).
func (c *Core) trackLovedLocked() bool {
	if !c.hasTrack || c.curTrack.ArtistKey == "" {
		return false
	}
	exists, err := c.store.LovedTrackExists(c.curTrack.ArtistKey, c.curTrack.Title)
	return err == nil && exists
}

// Love is a toggle: on a track or station that isn't loved it records a
// strong positive (station α, tag affinities, the loved-artist set that
// drives fingerprint overlap); pressed again on the same target it returns
// exactly those boosts. A returned love decays away like every other
// bounded reward — it is a bounded dose, not a permanent scar on the model.
//
// Returns (track, hadTrack, nowLoved): nowLoved is the resulting state —
// true when the press loved, false when it unloved.
func (c *Core) Love(now time.Time) (Track, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.currentUUID == "" {
		return Track{}, false, false
	}

	// The toggle is grounded in what the user can see: the loved row for
	// the currently playing track (or, trackless, the row for the station).
	if c.hasTrack && c.curTrack.ArtistKey != "" {
		if exists, err := c.store.LovedTrackExists(c.curTrack.ArtistKey, c.curTrack.Title); err == nil && exists {
			return c.unloveLocked(now)
		}
	} else if exists, err := c.store.LovedTrackExists("", ""); err == nil && exists {
		return c.unloveLocked(now)
	}

	daypart := DaypartFor(now)
	c.applyRewardLocked(c.currentUUID, daypart, loveAlpha, 0, now)
	if st := c.stationByUUIDLocked(c.currentUUID); st != nil {
		for _, t := range st.TagList() {
			c.bumpTagLocked(t, 0.5, now)
		}
	}
	if !c.hasTrack {
		// Trackless love: record the intent against the station so a later
		// unlove (and the loved library's origin links) can find it.
		_ = c.store.InsertLoved(c.currentUUID, "", "", "", now)
		return Track{}, false, true
	}
	tr := c.curTrack
	_ = c.store.InsertLoved(c.currentUUID, tr.ArtistKey, tr.Artist, tr.Title, now)
	if tr.ArtistKey != "" {
		c.loved[tr.ArtistKey] = true
	}
	return tr, true, true
}

// unloveLocked reverses a Love on the current track (or, trackless, on the
// current station): deletes the loved rows, returns the station α and tag
// boosts, and drops the artist from the loved set when no other loved
// track by them remains. Already-banked listen rewards are historical fact
// and stay.
func (c *Core) unloveLocked(now time.Time) (Track, bool, bool) {
	daypart := DaypartFor(now)
	c.applyRewardLocked(c.currentUUID, daypart, -loveAlpha, 0, now)
	if st := c.stationByUUIDLocked(c.currentUUID); st != nil {
		for _, t := range st.TagList() {
			c.bumpTagLocked(t, -0.5, now)
		}
	}

	if !c.hasTrack || c.curTrack.ArtistKey == "" {
		_, _ = c.store.ForgetLovedTrack("", "")
		return Track{}, false, false
	}
	tr := c.curTrack
	removed, err := c.store.ForgetLovedTrack(tr.ArtistKey, tr.Title)
	if err != nil || !removed {
		return tr, true, false
	}
	if n, err := c.store.CountLovedArtist(tr.ArtistKey); err == nil && n == 0 {
		delete(c.loved, tr.ArtistKey)
	}
	return tr, true, false
}

// MarkStationFailed bumps a station's failure count (stream wouldn't start).
// Three strikes removes it from candidate pools.
func (c *Core) MarkStationFailed(uuid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.markFailedLocked(uuid)
}

func (c *Core) markFailedLocked(uuid string) {
	_ = c.store.BumpFailCount(uuid, 1)
	if st := c.stationByUUIDLocked(uuid); st != nil {
		st.FailCount++
	}
}

// MarkStationHealthy resets the failure count after a successful play.
func (c *Core) MarkStationHealthy(uuid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.store.BumpFailCount(uuid, -maxFailCount)
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
		_ = c.store.PutBandit(uuid, dp, a, b, now)
	}
}

func (c *Core) bumpTagLocked(tag string, delta float64, now time.Time) {
	row := banditRow{Alpha: 1, UpdatedAt: now}
	if r, ok := c.tags[tag]; ok {
		row = r
	}
	w := decayToward(row.Alpha, row.UpdatedAt, now) + delta
	c.tags[tag] = banditRow{Alpha: w, UpdatedAt: now}
	_ = c.store.PutTagAffinity(tag, w, now)
}

// SeedResult is what a typed seed query resolved to.
type SeedResult struct {
	Pick  Pick
	Kind  string // "tag" or "artist"
	Label string
}

// Seed tilts the taste model toward a typed query and picks a station for it.
//
// Resolution order, strongest signal first:
//  1. Genre tag in the local cache — tune immediately, offline.
//  2. Genre tag in the directory — a query that matches radio-browser's tag
//     vocabulary (e.g. "corridos") seeds even when the local cache is thin;
//     the directory results are upserted and one is tuned.
//  3. Artist by name — the pseudo-love is recorded so fingerprints catch the
//     artist later, and stations named for them are tuned.
//  4. Artist by curated genre — when no station is named for the artist
//     (the common case), fall back to the genre family that carries their
//     music. The reason line says which path fired, so a genre guess is
//     never silent.
func (c *Core) Seed(ctx context.Context, query string, now time.Time) (SeedResult, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	if i := strings.Index(q, " - "); i > 0 {
		q = strings.TrimSpace(q[:i])
	}
	if q == "" {
		return SeedResult{}, false
	}

	// 1. Genre in the local cache: tune immediately, no network.
	c.mu.Lock()
	if tag, n := c.matchTagLocked(q); n >= 1 {
		if p, ok := c.tuneTagLocked(tag, now); ok {
			c.mu.Unlock()
			return SeedResult{Pick: p, Kind: "tag", Label: tag}, true
		}
	}
	c.mu.Unlock()

	// 2. Genre in the directory: the query may be a real tag the local
	// cache doesn't carry. Search, upsert, and tune.
	if p, ok := c.seedFromDirectoryTag(ctx, q, now); ok {
		return SeedResult{Pick: p, Kind: "tag", Label: q}, true
	}

	// 3. Artist by name. Record the pseudo-love regardless of outcome so
	// fingerprints catch the artist as listening accumulates.
	c.mu.Lock()
	key := NormalizeArtist(q)
	if key != "" && !c.loved[key] {
		c.loved[key] = true
		_ = c.store.InsertLoved("", key, strings.TrimSpace(query), "", now)
	}
	c.mu.Unlock()

	if found, err := c.rb.SearchByName(ctx, q, 20); err == nil && len(found) > 0 {
		_ = c.store.UpsertStations(found, now)
		c.mu.Lock()
		_ = c.reload()
		p, ok := c.pickSeededLocked(q, now)
		c.mu.Unlock()
		if ok {
			return SeedResult{Pick: p, Kind: "artist", Label: q}, true
		}
	}

	// 4. Artist by curated genre. The pseudo-love from step 3 is already
	// banked; this finds a station that plays their kind of music.
	if key != "" {
		for _, tag := range genreCandidates(key) {
			if p, ok := c.seedFromDirectoryTag(ctx, tag, now); ok {
				p.Reason = "seeded genre: " + tag
				return SeedResult{Pick: p, Kind: "genre", Label: q + " → " + tag}, true
			}
		}
	}

	// Nothing resolved: local loose match as the last resort.
	c.mu.Lock()
	defer c.mu.Unlock()
	cands := c.candidatesFor(now, func(st *Station) bool {
		return strings.Contains(strings.ToLower(st.Name), q)
	})
	if len(cands) == 0 {
		cands = c.candidatesFor(now, func(st *Station) bool {
			for _, t := range st.TagList() {
				if strings.Contains(t, q) {
					return true
				}
			}
			return false
		})
	}
	if cd, _, err := c.pickFrom(cands, now, c.rng); err == nil {
		c.endListenLocked(now, true)
		return SeedResult{Pick: Pick{Station: *cd.st, Reason: "seeded: " + q}, Kind: "artist", Label: q}, true
	}
	return SeedResult{Kind: "artist", Label: q}, false
}

// tuneTagLocked bumps the tag and tunes a station carrying it. Caller's
// lock must be held.
func (c *Core) tuneTagLocked(tag string, now time.Time) (Pick, bool) {
	c.bumpTagLocked(tag, 2.0, now) // love-sized bump; decays like the rest
	c.seedTag, c.seedPicks = tag, 4
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
		return Pick{Station: *cd.st, Reason: "seeded: " + tag}, true
	}
	return Pick{}, false
}

// seedFromDirectoryTag searches the directory for a genre tag, upserts the
// results, and tunes one. Returns false when the directory has nothing for
// the tag (or is unreachable).
func (c *Core) seedFromDirectoryTag(ctx context.Context, tag string, now time.Time) (Pick, bool) {
	found, err := c.rb.SearchByTag(ctx, tag, 25)
	if err != nil || len(found) == 0 {
		return Pick{}, false
	}
	_ = c.store.UpsertStations(found, now)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.reload(); err != nil {
		return Pick{}, false
	}
	return c.tuneTagLocked(tag, now)
}

// pickSeededLocked tunes a station whose name contains the query from the
// freshly-reloaded cache. Caller's lock must be held.
func (c *Core) pickSeededLocked(q string, now time.Time) (Pick, bool) {
	cands := c.candidatesFor(now, func(st *Station) bool {
		return strings.Contains(strings.ToLower(st.Name), q)
	})
	if cd, _, err := c.pickFrom(cands, now, c.rng); err == nil {
		c.endListenLocked(now, true)
		return Pick{Station: *cd.st, Reason: "seeded: " + q}, true
	}
	return Pick{}, false
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
