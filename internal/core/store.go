package core

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Station is a cached radio-browser entry plus screech's own columns.
type Station struct {
	UUID        string
	Name        string
	URL         string
	URLResolved string
	Homepage    string
	Tags        string // comma-separated, lowercase
	Country     string
	Codec       string
	Bitrate     int
	Votes       int
	ClickCount  int
	LastCheckOK bool
	AdRisk      float64
	FailCount   int

	// tagList memoizes TagList. Tags is treated as immutable once a
	// Station exists: the cache and the directory both rebuild whole
	// Station values rather than editing one in place.
	tagList []string
}

// TagList returns the station's tags split and trimmed. The result is
// memoized because the tune path walks it once per candidate per pick, and
// the cache holds twenty thousand stations by default.
func (s *Station) TagList() []string {
	if s.tagList != nil || s.Tags == "" {
		return s.tagList
	}
	parts := strings.Split(s.Tags, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	s.tagList = out
	return out
}

// StreamURL is what players should open. url_resolved, falling back to url.
func (s *Station) StreamURL() string {
	if s.URLResolved != "" {
		return s.URLResolved
	}
	return s.URL
}

// queryer is the small surface the store needs from *sql.DB or *sql.Tx.
type queryer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

type Store struct {
	db *sql.DB
	q  queryer
	// recoveredFrom records where a corrupted database was archived during
	// OpenStore, so the app can tell the user the cache started over.
	recoveredFrom string
}

func openOne(path string) (*Store, error) {
	// The path is escaped: a data_dir carrying '?', '#', or '&' would
	// otherwise truncate the DSN into an unrelated (or invalid) one, and the
	// driver error that follows says nothing about the real cause.
	dsn := "file:" + escapeDSNPath(path) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer; screech is a one-process app
	s := &Store{db: db, q: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.quickCheck(); err != nil {
		db.Close()
		return nil, err
	}
	// Reap listens left open by a dead previous process (window close,
	// SIGKILL, power loss). Screech is single-instance against this
	// database, so any open listen at startup is definitionally dead.
	// Close them at their start time: the honest record of a crashed
	// session is "we don't know how long it played", not "zero credit
	// for a listen that may have run for hours".
	cutoff := time.Now().Add(-2 * time.Minute)
	if _, err := s.q.Exec(`UPDATE listens SET ended_at=started_at WHERE ended_at IS NULL AND started_at < ?`, cutoff.Unix()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func OpenStore(path string) (*Store, error) {
	var lastErr error
	var archived string
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			// The first attempt found corruption: move the bad file aside
			// and try to build a fresh database. Seed stations will be
			// restored on the next directory sync.
			bak, err := archiveCorruptDB(path)
			if err != nil {
				return nil, err
			}
			archived = bak
		}
		s, err := openOne(path)
		if err == nil {
			s.recoveredFrom = archived
			return s, nil
		}
		lastErr = err
		if !isCorruption(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("could not open database after corruption recovery: %w", lastErr)
}

// escapeDSNPath percent-encodes the three characters that change how a
// file: DSN is parsed. Everything else (drive letters, backslashes, spaces)
// is left alone so the DSN stays readable in error messages.
func escapeDSNPath(path string) string {
	return dsnPathEscaper.Replace(path)
}

var dsnPathEscaper = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")

// errIntegrity marks a database that opened fine but failed quick_check.
// It is corruption by definition — a clean open only proves the header
// parsed — so OpenStore recovers from it like any other damaged file.
var errIntegrity = errors.New("database failed integrity check")

func isCorruption(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errIntegrity) {
		return true
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() & 0xff {
		case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
			return true
		}
	}
	s := err.Error()
	return strings.Contains(s, "database disk image is malformed") ||
		strings.Contains(s, "database is malformed") ||
		strings.Contains(s, "file is not a database") ||
		strings.Contains(s, "database corruption")
}

// archiveCorruptDB moves a damaged database (and its WAL/SHM companions —
// a stale journal left next to a fresh database can be replayed into it)
// aside, and returns the stem they were moved to.
func archiveCorruptDB(path string) (string, error) {
	bak := path + ".corrupt." + strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.Rename(path, bak); err != nil {
		return "", err
	}
	for _, ext := range []string{"-wal", "-shm"} {
		if err := os.Rename(path+ext, bak+ext); err != nil && !os.IsNotExist(err) {
			return bak, err
		}
	}
	return bak, nil
}

// OpenStoreReadOnly opens an existing database for inspection without
// creating files, writing schema, or touching user data. It is used by the
// doctor command and is intentionally read-only.
func OpenStoreReadOnly(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	dsn := "file:" + escapeDSNPath(path) + "?_pragma=busy_timeout(5000)&mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, q: db}
	// sql.Open is lazy: without a probe a garbage file would report ok.
	if err := s.quickCheck(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Begin starts a transaction. The caller is responsible for Commit/Rollback.
func (s *Store) Begin() (*sql.Tx, error) { return s.db.Begin() }

// WithTx returns a Store that executes all queries inside the given
// transaction. Use it to group multiple persistence operations atomically.
func (s *Store) WithTx(tx *sql.Tx) *Store { return &Store{q: tx} }

type migration struct {
	version int
	sql     string
	// fn runs after sql inside the same transaction, for migrations that
	// need to touch rows (rewrite, backfill) rather than only schema.
	fn func(tx *sql.Tx) error
}

var migrations = []migration{
	{1, `
CREATE TABLE IF NOT EXISTS stations(
	uuid TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	url TEXT NOT NULL,
	url_resolved TEXT NOT NULL DEFAULT '',
	homepage TEXT NOT NULL DEFAULT '',
	tags TEXT NOT NULL DEFAULT '',
	country TEXT NOT NULL DEFAULT '',
	codec TEXT NOT NULL DEFAULT '',
	bitrate INTEGER NOT NULL DEFAULT 0,
	votes INTEGER NOT NULL DEFAULT 0,
	clickcount INTEGER NOT NULL DEFAULT 0,
	lastcheckok INTEGER NOT NULL DEFAULT 1,
	ad_risk REAL NOT NULL DEFAULT 0,
	fail_count INTEGER NOT NULL DEFAULT 0,
	fetched_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS listens(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	station_uuid TEXT NOT NULL,
	started_at INTEGER NOT NULL,
	ended_at INTEGER,
	daypart TEXT NOT NULL DEFAULT '',
	skip_fast INTEGER NOT NULL DEFAULT 0,
	during_ad INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS tracks_heard(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	station_uuid TEXT NOT NULL,
	artist_key TEXT NOT NULL DEFAULT '',
	artist TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	raw TEXT NOT NULL DEFAULT '',
	heard_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tracks_station ON tracks_heard(station_uuid);
CREATE INDEX IF NOT EXISTS idx_listens_station ON listens(station_uuid);
CREATE INDEX IF NOT EXISTS idx_tracks_artist ON tracks_heard(artist_key);
CREATE INDEX IF NOT EXISTS idx_tracks_heard_at ON tracks_heard(heard_at);
CREATE TABLE IF NOT EXISTS loved(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	station_uuid TEXT NOT NULL DEFAULT '',
	artist_key TEXT NOT NULL DEFAULT '',
	artist TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	loved_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS bandit(
	station_uuid TEXT NOT NULL,
	daypart TEXT NOT NULL,
	alpha REAL NOT NULL DEFAULT 1,
	beta REAL NOT NULL DEFAULT 1,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY(station_uuid, daypart)
);
CREATE TABLE IF NOT EXISTS tag_affinity(
	tag TEXT PRIMARY KEY,
	weight REAL NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS meta(
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS presets(
	slot INTEGER PRIMARY KEY CHECK(slot BETWEEN 1 AND 9),
	station_uuid TEXT NOT NULL,
	saved_at INTEGER NOT NULL
);
`, nil},
	// The love toggle, the heart on every title change, and unlove all
	// look up loved rows by (artist_key, title) or by station; without
	// these each one is a full scan of a table that only grows.
	{2, `
CREATE INDEX IF NOT EXISTS idx_loved_artist_title ON loved(artist_key, title);
CREATE INDEX IF NOT EXISTS idx_loved_station ON loved(station_uuid);
`, nil},
	// Rows written before CleanText existed can carry double spaces or
	// control bytes that the cleaned ingest now differs from: a track
	// loved under an older version must still toggle, forget, and count
	// under its cleaned name.
	{3, "", normalizeTextRows},
	// Before the player suppressed mpv's URL-basename media-title, every
	// tune logged the stream filename ("aac-128") as a heard track.
	{4, "", purgeFilenameTracks},
}

func (s *Store) migrate() error {
	var v int
	if err := s.q.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for _, m := range migrations {
		if m.version <= v {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if m.sql != "" {
			if _, err := tx.Exec(m.sql); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if m.fn != nil {
			if err := m.fn(tx); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		// The version stamp belongs to the migration's transaction: a
		// crash between commit and stamp would re-run a migration that
		// may not be idempotent.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// normalizeTextRows rewrites each table's display text through CleanText,
// in place. A changed artist also gets its key recomputed — artist_key is
// NormalizeArtist's output — while an unchanged artist keeps the stored
// key (seeding writes pseudo-love rows whose key exists with an empty
// title). Changed rows are collected before any UPDATE runs: exec while
// iterating deadlocks on this single-connection database.
func normalizeTextRows(tx *sql.Tx) error {
	for _, table := range []struct {
		name string
		cols []string
	}{
		{"loved", []string{"artist_key", "artist", "title"}},
		{"tracks_heard", []string{"artist_key", "artist", "title", "raw"}},
	} {
		if err := cleanTableRows(tx, table.name, table.cols); err != nil {
			return err
		}
	}
	return nil
}

func cleanTableRows(tx *sql.Tx, table string, cols []string) error {
	keyIdx, artIdx := -1, -1
	for i, col := range cols {
		switch col {
		case "artist_key":
			keyIdx = i
		case "artist":
			artIdx = i
		}
	}
	rows, err := tx.Query(`SELECT id,` + strings.Join(cols, `,`) + ` FROM ` + table)
	if err != nil {
		return err
	}
	var updates [][]any
	for rows.Next() {
		var id int64
		vals := make([]string, len(cols))
		dest := make([]any, 0, len(cols)+1)
		dest = append(dest, &id)
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return err
		}
		cleaned := make([]string, len(cols))
		for i, v := range vals {
			cleaned[i] = CleanText(v)
		}
		if artIdx >= 0 && keyIdx >= 0 && cleaned[artIdx] != vals[artIdx] {
			cleaned[keyIdx] = NormalizeArtist(cleaned[artIdx])
		}
		changed := false
		for i := range vals {
			if cleaned[i] != vals[i] {
				changed = true
				break
			}
		}
		if changed {
			args := make([]any, 0, len(cols)+1)
			for _, v := range cleaned {
				args = append(args, v)
			}
			updates = append(updates, append(args, id))
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(updates) == 0 {
		return nil
	}
	sets := make([]string, len(cols))
	for i, col := range cols {
		sets[i] = col + "=?"
	}
	stmt, err := tx.Prepare(`UPDATE ` + table + ` SET ` + strings.Join(sets, `,`) + ` WHERE id=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, args := range updates {
		if _, err := stmt.Exec(args...); err != nil {
			return err
		}
	}
	return nil
}

// urlBasenames returns the unescaped and escaped last path segment of a
// stream URL — the values mpv may report as media-title before ICY metadata
// arrives. Mirrors the player's curFile rule; duplicated here rather than
// shared so core doesn't import the player package.
func urlBasenames(rawurl string) (string, string) {
	u, err := url.Parse(rawurl)
	if err != nil || u.Path == "" || u.Path == "/" {
		return "", ""
	}
	return path.Base(u.Path), path.Base(u.EscapedPath())
}

// purgeFilenameTracks deletes tracks_heard rows that are only mpv's
// URL-basename media-title masquerading as a track: no artist, and a title
// equal to the basename of that row's station URL. loved rows are left
// alone — loving one was a deliberate act.
func purgeFilenameTracks(tx *sql.Tx) error {
	names := map[string][]string{}
	srows, err := tx.Query(`SELECT uuid, url, url_resolved FROM stations`)
	if err != nil {
		return err
	}
	for srows.Next() {
		var uuid, u, ur string
		if err := srows.Scan(&uuid, &u, &ur); err != nil {
			srows.Close()
			return err
		}
		for _, raw := range []string{u, ur} {
			if base, esc := urlBasenames(raw); base != "" {
				names[uuid] = append(names[uuid], base, esc)
			}
		}
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return err
	}

	var ids []int64
	trows, err := tx.Query(`SELECT id, station_uuid, title FROM tracks_heard
		WHERE artist_key = '' AND artist = '' AND title <> ''`)
	if err != nil {
		return err
	}
	for trows.Next() {
		var id int64
		var uuid, title string
		if err := trows.Scan(&id, &uuid, &title); err != nil {
			trows.Close()
			return err
		}
		if slices.Contains(names[uuid], title) {
			ids = append(ids, id)
		}
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	stmt, err := tx.Prepare(`DELETE FROM tracks_heard WHERE id=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.Exec(id); err != nil {
			return err
		}
	}
	return nil
}

// quickCheck runs PRAGMA quick_check and returns an error if the database
// is anything other than healthy.
func (s *Store) quickCheck() error {
	var ok string
	if err := s.q.QueryRow(`PRAGMA quick_check`).Scan(&ok); err != nil {
		return err
	}
	if ok != "ok" {
		return fmt.Errorf("%w: %s", errIntegrity, ok)
	}
	return nil
}

// SchemaVersion reports the schema stamp, PRAGMA user_version: the
// authoritative record of which migrations this database has had.
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.q.QueryRow(`PRAGMA user_version`).Scan(&v)
	return v, err
}

// --- stations ---

// UpsertStations merges a directory slice into the station cache. Every
// column here is directory-owned and refreshed wholesale, with two
// exceptions: fail_count is local and only ever forgiven a strike, and
// ad_risk is the one value Screech computes rather than receives.
//
// ComputeAdRisk is deterministic over the very columns being refreshed, so
// overwriting it costs nothing today. That stops being true the moment a
// second source of ad-risk exists (a lazily judged score, a user override):
// this clause would erase it on the next weekly sync. Keep such a value in
// its own table keyed by uuid rather than widening this one.
// upsertChunk bounds one transaction's size. The store runs on a single
// connection, so a 20k-row sync transaction would hold the writer for its
// whole duration and block every UI-thread read (love, titles, presets).
const upsertChunk = 500

func (s *Store) UpsertStations(sts []Station, fetchedAt time.Time) error {
	ts := fetchedAt.Unix()
	for start := 0; start < len(sts); start += upsertChunk {
		end := start + upsertChunk
		if end > len(sts) {
			end = len(sts)
		}
		if err := s.upsertStations(sts[start:end], ts); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) upsertStations(sts []Station, ts int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO stations
		(uuid,name,url,url_resolved,homepage,tags,country,codec,bitrate,votes,clickcount,lastcheckok,ad_risk,fetched_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(uuid) DO UPDATE SET
		name=excluded.name, url=excluded.url, url_resolved=excluded.url_resolved,
		homepage=excluded.homepage, tags=excluded.tags, country=excluded.country,
		codec=excluded.codec, bitrate=excluded.bitrate, votes=excluded.votes,
		clickcount=excluded.clickcount, lastcheckok=excluded.lastcheckok,
		ad_risk=excluded.ad_risk, fetched_at=excluded.fetched_at,
		fail_count=MAX(0, stations.fail_count - 1)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, st := range sts {
		ok := 0
		if st.LastCheckOK {
			ok = 1
		}
		if _, err := stmt.Exec(st.UUID, st.Name, st.URL, st.URLResolved, st.Homepage,
			st.Tags, st.Country, st.Codec, st.Bitrate, st.Votes, st.ClickCount, ok, st.AdRisk, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) LoadStations() ([]Station, error) {
	rows, err := s.q.Query(`SELECT uuid,name,url,url_resolved,homepage,tags,country,codec,
		bitrate,votes,clickcount,lastcheckok,ad_risk,fail_count FROM stations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Station
	for rows.Next() {
		var st Station
		var ok int
		if err := rows.Scan(&st.UUID, &st.Name, &st.URL, &st.URLResolved, &st.Homepage,
			&st.Tags, &st.Country, &st.Codec, &st.Bitrate, &st.Votes, &st.ClickCount,
			&ok, &st.AdRisk, &st.FailCount); err != nil {
			return nil, err
		}
		st.LastCheckOK = ok == 1
		// Rows written before display-text sanitization existed can still
		// carry terminal escapes; clean at read so old data is safe too.
		st.Name = CleanText(st.Name)
		st.Tags = CleanText(st.Tags)
		st.Country = CleanText(st.Country)
		st.Codec = CleanText(st.Codec)
		st.Homepage = CleanText(st.Homepage)
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) StationCount() (int, error) {
	var n int
	err := s.q.QueryRow(`SELECT COUNT(*) FROM stations`).Scan(&n)
	return n, err
}

// PruneStale deletes stations the directory no longer vouches for: fetched
// before the given cutoff, absent from the fresh slice, and carrying no user
// history. Seeds, presets, anything listened to, loved on, or scored by the
// bandit, and the resume station all survive regardless.
func (s *Store) PruneStale(before time.Time, keepUUID string) (int64, error) {
	res, err := s.q.Exec(`DELETE FROM stations WHERE fetched_at < ?
		AND uuid NOT LIKE 'seed:%'
		AND uuid != ?
		AND uuid NOT IN (SELECT DISTINCT station_uuid FROM bandit)
		AND uuid NOT IN (SELECT DISTINCT station_uuid FROM listens)
		AND uuid NOT IN (SELECT DISTINCT station_uuid FROM loved WHERE station_uuid != '')
		AND uuid NOT IN (SELECT station_uuid FROM presets)`,
		before.Unix(), keepUUID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) BumpFailCount(uuid string, delta int) error {
	_, err := s.q.Exec(`UPDATE stations SET fail_count = MAX(0, fail_count + ?) WHERE uuid = ?`, delta, uuid)
	return err
}

// --- listens / tracks / loved ---

func (s *Store) InsertListen(station string, start time.Time, daypart string) (int64, error) {
	res, err := s.q.Exec(`INSERT INTO listens(station_uuid, started_at, daypart) VALUES(?,?,?)`,
		station, start.Unix(), daypart)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) FinishListen(id int64, end time.Time, skipFast, duringAd bool) error {
	_, err := s.q.Exec(`UPDATE listens SET ended_at=?, skip_fast=?, during_ad=? WHERE id=?`,
		end.Unix(), b2i(skipFast), b2i(duringAd), id)
	return err
}

func (s *Store) InsertTrack(station, artistKey, artist, title, raw string, at time.Time) error {
	_, err := s.q.Exec(`INSERT INTO tracks_heard(station_uuid,artist_key,artist,title,raw,heard_at)
		VALUES(?,?,?,?,?,?)`, station, artistKey, artist, title, raw, at.Unix())
	return err
}

func (s *Store) InsertLoved(station, artistKey, artist, title string, at time.Time) error {
	_, err := s.q.Exec(`INSERT INTO loved(station_uuid,artist_key,artist,title,loved_at)
		VALUES(?,?,?,?,?)`, station, artistKey, artist, title, at.Unix())
	return err
}

type RecentTrack struct {
	StationUUID string
	StationName string
	Artist      string
	Title       string
	HeardAt     time.Time
	Loved       bool
}

// LovedTrack is a deduplicated library entry. LoveCount preserves how often
// the user reinforced the track while HeardCount gives the UI useful context.
type LovedTrack struct {
	StationUUID string
	StationName string
	ArtistKey   string
	Artist      string
	Title       string
	LovedAt     time.Time
	LoveCount   int
	HeardCount  int
}

func (s *Store) RecentlyHeard(n int) ([]RecentTrack, error) {
	rows, err := s.q.Query(`
		SELECT t.station_uuid, COALESCE(st.name, t.station_uuid), t.artist, t.title,
		       t.heard_at, EXISTS(
		         SELECT 1 FROM loved l
		         WHERE l.station_uuid=t.station_uuid AND l.artist_key=t.artist_key
		           AND l.title=t.title
		       )
		FROM tracks_heard t
		LEFT JOIN stations st ON st.uuid=t.station_uuid
		ORDER BY t.heard_at DESC, t.id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecentTrack
	for rows.Next() {
		var r RecentTrack
		var heard int64
		if err := rows.Scan(&r.StationUUID, &r.StationName, &r.Artist, &r.Title, &heard, &r.Loved); err != nil {
			return nil, err
		}
		r.StationName = CleanText(r.StationName)
		r.Artist = CleanText(r.Artist)
		r.Title = CleanText(r.Title)
		r.HeardAt = time.Unix(heard, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) LovedTracks(n int) ([]LovedTrack, error) {
	rows, err := s.q.Query(`
		WITH ranked AS (
			SELECT l.station_uuid, COALESCE(st.name, l.station_uuid) AS station_name,
			       l.artist_key, l.artist, l.title, l.loved_at,
			       ROW_NUMBER() OVER (
			         PARTITION BY l.artist_key, lower(trim(l.title))
			         ORDER BY l.loved_at DESC, l.id DESC
			       ) AS rank,
			       COUNT(*) OVER (
			         PARTITION BY l.artist_key, lower(trim(l.title))
			       ) AS love_count
			FROM loved l
			LEFT JOIN stations st ON st.uuid=l.station_uuid
			WHERE trim(l.title) != ''
		)
		SELECT r.station_uuid, r.station_name, r.artist_key, r.artist, r.title,
		       r.loved_at, r.love_count,
		       (SELECT COUNT(*) FROM tracks_heard t
		        WHERE t.artist_key=r.artist_key
		          AND lower(trim(t.title))=lower(trim(r.title))) AS heard_count
		FROM ranked r
		WHERE r.rank=1
		ORDER BY r.loved_at DESC
		LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LovedTrack
	for rows.Next() {
		var entry LovedTrack
		var lovedAt int64
		if err := rows.Scan(&entry.StationUUID, &entry.StationName, &entry.ArtistKey,
			&entry.Artist, &entry.Title, &lovedAt, &entry.LoveCount, &entry.HeardCount); err != nil {
			return nil, err
		}
		entry.StationName = CleanText(entry.StationName)
		entry.Artist = CleanText(entry.Artist)
		entry.Title = CleanText(entry.Title)
		entry.LovedAt = time.Unix(lovedAt, 0)
		out = append(out, entry)
	}
	return out, rows.Err()
}

func (s *Store) ForgetLovedTrack(artistKey, title string) (bool, error) {
	result, err := s.q.Exec(`DELETE FROM loved
		WHERE artist_key=? AND lower(trim(title))=lower(trim(?))`, artistKey, title)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// LovedTrackExists reports whether a (artist_key, title) pair is in the
// loved set. Used to make love a toggle rather than a one-way valve.
// Matching mirrors ForgetLovedTrack exactly: a row that reads as loved here
// must be a row that unlove can actually delete.
func (s *Store) LovedTrackExists(artistKey, title string) (bool, error) {
	var n int
	err := s.q.QueryRow(`SELECT COUNT(*) FROM loved
		WHERE artist_key=? AND lower(trim(title))=lower(trim(?))`, artistKey, title).Scan(&n)
	return n > 0, err
}

// TracklessStationLoveExists reports whether this station carries a
// trackless love: the row written when the user loves a stream that never
// sent a usable title. These are per-station on purpose. Loving a silent
// stream must never read as un-loving a different silent stream.
func (s *Store) TracklessStationLoveExists(stationUUID string) (bool, error) {
	var n int
	err := s.q.QueryRow(`SELECT COUNT(*) FROM loved
		WHERE station_uuid=? AND artist_key='' AND trim(title)=''`, stationUUID).Scan(&n)
	return n > 0, err
}

// ForgetTracklessStationLove removes one station's trackless love rows and
// leaves every other station's alone.
func (s *Store) ForgetTracklessStationLove(stationUUID string) (bool, error) {
	res, err := s.q.Exec(`DELETE FROM loved
		WHERE station_uuid=? AND artist_key='' AND trim(title)=''`, stationUUID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// CountLovedArtist reports how many loved rows remain for an artist key,
// so Unlove only removes the artist from the loved set when the last
// loved track by them is gone.
func (s *Store) CountLovedArtist(artistKey string) (int, error) {
	var n int
	err := s.q.QueryRow(`SELECT COUNT(*) FROM loved WHERE artist_key=?`, artistKey).Scan(&n)
	return n, err
}

func (s *Store) LovedArtistKeys() (map[string]bool, error) {
	rows, err := s.q.Query(`SELECT DISTINCT artist_key FROM loved WHERE artist_key != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// StationArtists returns station -> set of artist keys observed there.
func (s *Store) StationArtists() (map[string]map[string]bool, error) {
	rows, err := s.q.Query(`SELECT DISTINCT station_uuid, artist_key FROM tracks_heard WHERE artist_key != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var st, a string
		if err := rows.Scan(&st, &a); err != nil {
			return nil, err
		}
		if out[st] == nil {
			out[st] = map[string]bool{}
		}
		out[st][a] = true
	}
	return out, rows.Err()
}

// --- bandit ---

type banditRow struct {
	Alpha, Beta float64
	UpdatedAt   time.Time
}

func (s *Store) PutBandit(station, daypart string, alpha, beta float64, at time.Time) error {
	_, err := s.q.Exec(`INSERT INTO bandit(station_uuid,daypart,alpha,beta,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(station_uuid,daypart) DO UPDATE SET alpha=excluded.alpha, beta=excluded.beta, updated_at=excluded.updated_at`,
		station, daypart, alpha, beta, at.Unix())
	return err
}

// AllBandit loads every bandit row grouped by station then daypart.
func (s *Store) AllBandit() (map[string]map[string]banditRow, error) {
	rows, err := s.q.Query(`SELECT station_uuid,daypart,alpha,beta,updated_at FROM bandit`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]banditRow{}
	for rows.Next() {
		var st, dp string
		var r banditRow
		var ts int64
		if err := rows.Scan(&st, &dp, &r.Alpha, &r.Beta, &ts); err != nil {
			return nil, err
		}
		r.UpdatedAt = time.Unix(ts, 0)
		if out[st] == nil {
			out[st] = map[string]banditRow{}
		}
		out[st][dp] = r
	}
	return out, rows.Err()
}

// --- tag affinity ---

func (s *Store) AllTagAffinity() (map[string]banditRow, error) { // reuse: Alpha=weight
	rows, err := s.q.Query(`SELECT tag,weight,updated_at FROM tag_affinity`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]banditRow{}
	for rows.Next() {
		var tag string
		var r banditRow
		var ts int64
		if err := rows.Scan(&tag, &r.Alpha, &ts); err != nil {
			return nil, err
		}
		r.UpdatedAt = time.Unix(ts, 0)
		out[tag] = r
	}
	return out, rows.Err()
}

func (s *Store) PutTagAffinity(tag string, weight float64, at time.Time) error {
	_, err := s.q.Exec(`INSERT INTO tag_affinity(tag,weight,updated_at) VALUES(?,?,?)
		ON CONFLICT(tag) DO UPDATE SET weight=excluded.weight, updated_at=excluded.updated_at`,
		tag, weight, at.Unix())
	return err
}

// --- meta ---

func (s *Store) GetMeta(key string) (string, error) {
	var v string
	err := s.q.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) SetMeta(key, value string) error {
	_, err := s.q.Exec(`INSERT INTO meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// HistEntry is one row of the listen-time leaderboard.
type HistEntry struct {
	UUID  string
	Name  string
	Total time.Duration
}

// SavedStation is one row of the library's saved-stations list: any station
// that's a preset or carries loved tracks, with its cumulative listen time
// for sorting and its preset slot (0 = not a preset) for the dial tick.
type SavedStation struct {
	UUID       string
	Name       string
	Total      time.Duration
	PresetSlot int // 1-9 when the station holds a preset slot, else 0
	LoveCount  int // loved rows credited to this station
}

// SavedStations returns the unified saved-stations list, most-listened
// first. A station qualifies by being a preset or having any loved rows —
// the two ways a user says "remember this one".
func (s *Store) SavedStations() ([]SavedStation, error) {
	rows, err := s.q.Query(`
		WITH listen_time AS (
			SELECT station_uuid,
			       SUM(MAX(0, COALESCE(ended_at, started_at) - started_at)) AS total
			FROM listens GROUP BY station_uuid
		),
		love_count AS (
			SELECT station_uuid, COUNT(*) AS n
			FROM loved WHERE station_uuid != '' GROUP BY station_uuid
		),
		saved AS (
			SELECT station_uuid FROM presets
			UNION
			SELECT station_uuid FROM love_count
		)
		SELECT sv.station_uuid,
		       COALESCE(st.name, sv.station_uuid) AS name,
		       COALESCE(lt.total, 0) AS total,
		       COALESCE(p.slot, 0) AS slot,
		       COALESCE(lc.n, 0) AS loves
		FROM saved sv
		LEFT JOIN stations st ON st.uuid = sv.station_uuid
		LEFT JOIN listen_time lt ON lt.station_uuid = sv.station_uuid
		LEFT JOIN presets p ON p.station_uuid = sv.station_uuid
		LEFT JOIN love_count lc ON lc.station_uuid = sv.station_uuid
		ORDER BY total DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SavedStation
	for rows.Next() {
		var e SavedStation
		var secs int64
		if err := rows.Scan(&e.UUID, &e.Name, &secs, &e.PresetSlot, &e.LoveCount); err != nil {
			return nil, err
		}
		e.Name = CleanText(e.Name)
		e.Total = time.Duration(secs) * time.Second
		out = append(out, e)
	}
	return out, rows.Err()
}

// RemoveStation forgets a saved station: clears its preset slot (if any)
// and deletes the loved rows credited to it. Listen history and bandit
// counts stay — they're historical fact, not recall state. The two deletes
// run in one transaction so recall state is never left half-removed.
func (s *Store) RemoveStation(uuid string) (presetCleared bool, lovesRemoved int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM presets WHERE station_uuid=?`, uuid)
	if err != nil {
		return false, 0, err
	}
	if n, e := res.RowsAffected(); e == nil {
		presetCleared = n > 0
	}
	res, err = tx.Exec(`DELETE FROM loved WHERE station_uuid=?`, uuid)
	if err != nil {
		return presetCleared, 0, err
	}
	n, e := res.RowsAffected()
	if e != nil {
		return presetCleared, 0, e
	}
	if err := tx.Commit(); err != nil {
		return false, 0, err
	}
	return presetCleared, int(n), nil
}

// StationTotal is how long this station has been listened to across every
// session. The faceplate shows it as a readout, which is the sort of figure
// an instrument panel should carry and screech was keeping entirely to
// itself. Open listens contribute nothing rather than counting to now.
func (s *Store) StationTotal(uuid string) (time.Duration, error) {
	var secs int64
	err := s.q.QueryRow(`SELECT COALESCE(SUM(MAX(0,
		COALESCE(ended_at, started_at) - started_at)), 0)
		FROM listens WHERE station_uuid = ?`, uuid).Scan(&secs)
	return time.Duration(secs) * time.Second, err
}

// VacuumInto writes a consistent copy of the database to path — SQLite's
// online-backup API. A read-only connection still runs it: the copy is a
// new file, not a write to this database.
func (s *Store) VacuumInto(path string) error {
	_, err := s.q.Exec(`VACUUM INTO ?`, path)
	return err
}

// TasteTotals is the TASTE tab's listening ledger in one pass: per-daypart
// audible sums plus the share whose listens started inside the week
// window. The caller folds the daypart rows into the overall totals.
func (s *Store) TasteTotals(weekStart time.Time) (byDaypart, weekByDaypart map[string]time.Duration, err error) {
	rows, err := s.q.Query(`SELECT daypart,
		COALESCE(SUM(MAX(0, COALESCE(ended_at, started_at) - started_at)), 0),
		COALESCE(SUM(CASE WHEN started_at >= ? THEN MAX(0, COALESCE(ended_at, started_at) - started_at) ELSE 0 END), 0)
		FROM listens GROUP BY daypart`, weekStart.Unix())
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byDaypart, weekByDaypart = map[string]time.Duration{}, map[string]time.Duration{}
	for rows.Next() {
		var dp string
		var total, week int64
		if err := rows.Scan(&dp, &total, &week); err != nil {
			return nil, nil, err
		}
		byDaypart[dp] = time.Duration(total) * time.Second
		weekByDaypart[dp] = time.Duration(week) * time.Second
	}
	return byDaypart, weekByDaypart, rows.Err()
}

// TasteArtists counts heard tracks per artist, but only titles caught
// inside a listen that ran at least minListen — a ten-second skip is not
// taste — or inside the currently open one, and only from the last
// `since` days. The display name is the most recent form the stream sent.
func (s *Store) TasteArtists(now, since time.Time, minListen time.Duration, n int) ([]ArtistPlays, error) {
	rows, err := s.q.Query(`SELECT t.artist_key,
		(SELECT t2.artist FROM tracks_heard t2 WHERE t2.artist_key = t.artist_key
		 ORDER BY t2.heard_at DESC, t2.id DESC LIMIT 1) AS artist,
		COUNT(*) AS plays
		FROM tracks_heard t
		WHERE t.artist_key <> '' AND t.heard_at >= ?
		  AND EXISTS (SELECT 1 FROM listens l
		              WHERE l.station_uuid = t.station_uuid
		                AND t.heard_at BETWEEN l.started_at AND COALESCE(l.ended_at, ?)
		                AND (l.ended_at IS NULL OR l.ended_at - l.started_at >= ?))
		GROUP BY t.artist_key
		ORDER BY plays DESC, artist
		LIMIT ?`, since.Unix(), now.Unix(), int64(minListen.Seconds()), n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtistPlays
	for rows.Next() {
		var a ArtistPlays
		var key string
		if err := rows.Scan(&key, &a.Artist, &a.Plays); err != nil {
			return nil, err
		}
		a.Artist = CleanText(a.Artist)
		out = append(out, a)
	}
	return out, rows.Err()
}

// TopStationsSince is the listen-time leaderboard restricted to listens
// that started inside the window — the TASTE tab's "lately" column.
func (s *Store) TopStationsSince(since time.Time, n int) ([]HistEntry, error) {
	rows, err := s.q.Query(`SELECT l.station_uuid,
		COALESCE(st.name, l.station_uuid) AS name,
		SUM(MAX(0, COALESCE(l.ended_at, l.started_at) - l.started_at)) AS total
		FROM listens l LEFT JOIN stations st ON st.uuid = l.station_uuid
		WHERE l.started_at >= ?
		GROUP BY l.station_uuid ORDER BY total DESC LIMIT ?`, since.Unix(), n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistEntry
	for rows.Next() {
		var e HistEntry
		var secs int64
		if err := rows.Scan(&e.UUID, &e.Name, &secs); err != nil {
			return nil, err
		}
		e.Name = CleanText(e.Name)
		e.Total = time.Duration(secs) * time.Second
		out = append(out, e)
	}
	return out, rows.Err()
}

// LovedTrackCount is the deduplicated loved-track total — the same
// (artist_key, lowered title) grouping the library applies.
func (s *Store) LovedTrackCount() (int, error) {
	var n int
	err := s.q.QueryRow(`SELECT COUNT(*) FROM (
		SELECT 1 FROM loved WHERE trim(title) != ''
		GROUP BY artist_key, lower(trim(title)))`).Scan(&n)
	return n, err
}

// StationStats collects one station's listen history for the info sheet:
// cumulative audible time (the same sum as StationTotal), visits that
// lasted at least a minute, the first and most recent listen starts, and
// how many loved rows name a track rather than the station itself.
func (s *Store) StationStats(uuid string) (listened time.Duration, visits int, first, last time.Time, lovedTracks int, err error) {
	var secs, firstAt, lastAt int64
	err = s.q.QueryRow(`SELECT
		COALESCE(SUM(MAX(0, COALESCE(ended_at, started_at) - started_at)), 0),
		COALESCE(SUM(CASE WHEN ended_at IS NOT NULL AND ended_at - started_at >= 60 THEN 1 ELSE 0 END), 0),
		COALESCE(MIN(started_at), 0),
		COALESCE(MAX(started_at), 0)
		FROM listens WHERE station_uuid = ?`, uuid).Scan(&secs, &visits, &firstAt, &lastAt)
	if err != nil {
		return
	}
	listened = time.Duration(secs) * time.Second
	if firstAt > 0 {
		first = time.Unix(firstAt, 0)
	}
	if lastAt > 0 {
		last = time.Unix(lastAt, 0)
	}
	err = s.q.QueryRow(`SELECT COUNT(*) FROM loved
		WHERE station_uuid = ? AND (artist_key <> '' OR title <> '')`,
		uuid).Scan(&lovedTracks)
	return
}

// ListenTotals is cumulative listen time per station, for the dial's
// density strip. Unlike TopListened this isn't ranked or capped: the strip
// wants the whole distribution, including the long tail of stations you
// only ever heard once.
func (s *Store) ListenTotals() (map[string]time.Duration, error) {
	rows, err := s.q.Query(`SELECT station_uuid,
		SUM(MAX(0, COALESCE(ended_at, started_at) - started_at)) AS total
		FROM listens GROUP BY station_uuid HAVING total > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Duration{}
	for rows.Next() {
		var uuid string
		var secs int64
		if err := rows.Scan(&uuid, &secs); err != nil {
			return nil, err
		}
		out[uuid] = time.Duration(secs) * time.Second
	}
	return out, rows.Err()
}

// TrackPlayCount is how many times this track has come down any stream.
// Matching mirrors the loved-track queries so "heard 3x" and the library's
// heard count never disagree.
func (s *Store) TrackPlayCount(artistKey, title string) (int, error) {
	var n int
	err := s.q.QueryRow(`SELECT COUNT(*) FROM tracks_heard
		WHERE artist_key = ? AND lower(trim(title)) = lower(trim(?))`,
		artistKey, title).Scan(&n)
	return n, err
}

// TopListened ranks stations by cumulative listen time. Open listens (no end
// yet) count as zero; stations pruned from the cache keep their logged name
// via the join fallback.
func (s *Store) TopListened(n int) ([]HistEntry, error) {
	rows, err := s.q.Query(`
		SELECT l.station_uuid,
		       COALESCE(st.name, l.station_uuid) AS name,
		       SUM(MAX(0, COALESCE(l.ended_at, l.started_at) - l.started_at)) AS total
		FROM listens l
		LEFT JOIN stations st ON st.uuid = l.station_uuid
		GROUP BY l.station_uuid
		HAVING total > 0
		ORDER BY total DESC
		LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistEntry
	for rows.Next() {
		var e HistEntry
		var secs int64
		if err := rows.Scan(&e.UUID, &e.Name, &secs); err != nil {
			return nil, err
		}
		e.Name = CleanText(e.Name)
		e.Total = time.Duration(secs) * time.Second
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- presets ---

func (s *Store) Presets() (map[int]string, error) {
	rows, err := s.q.Query(`SELECT slot, station_uuid FROM presets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var slot int
		var uuid string
		if err := rows.Scan(&slot, &uuid); err != nil {
			return nil, err
		}
		out[slot] = uuid
	}
	return out, rows.Err()
}

func (s *Store) SetPreset(slot int, uuid string, at time.Time) error {
	_, err := s.q.Exec(`INSERT INTO presets(slot, station_uuid, saved_at) VALUES(?,?,?)
		ON CONFLICT(slot) DO UPDATE SET station_uuid=excluded.station_uuid, saved_at=excluded.saved_at`,
		slot, uuid, at.Unix())
	return err
}

func (s *Store) DeletePreset(slot int) error {
	_, err := s.q.Exec(`DELETE FROM presets WHERE slot=?`, slot)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
