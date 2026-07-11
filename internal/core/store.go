package core

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
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
}

// TagList returns the station's tags split and trimmed.
func (s *Station) TagList() []string {
	if s.Tags == "" {
		return nil
	}
	parts := strings.Split(s.Tags, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// StreamURL is what players should open. url_resolved, falling back to url.
func (s *Station) StreamURL() string {
	if s.URLResolved != "" {
		return s.URLResolved
	}
	return s.URL
}

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer; screech is a one-process app
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
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
CREATE INDEX IF NOT EXISTS idx_tracks_artist ON tracks_heard(artist_key);
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
`)
	return err
}

// --- stations ---

func (s *Store) UpsertStations(sts []Station, fetchedAt time.Time) error {
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
		ad_risk=excluded.ad_risk, fetched_at=excluded.fetched_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	ts := fetchedAt.Unix()
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
	rows, err := s.db.Query(`SELECT uuid,name,url,url_resolved,homepage,tags,country,codec,
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
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) StationCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM stations`).Scan(&n)
	return n, err
}

func (s *Store) BumpFailCount(uuid string, delta int) error {
	_, err := s.db.Exec(`UPDATE stations SET fail_count = MAX(0, fail_count + ?) WHERE uuid = ?`, delta, uuid)
	return err
}

// --- listens / tracks / loved ---

func (s *Store) InsertListen(station string, start time.Time, daypart string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO listens(station_uuid, started_at, daypart) VALUES(?,?,?)`,
		station, start.Unix(), daypart)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) FinishListen(id int64, end time.Time, skipFast, duringAd bool) error {
	_, err := s.db.Exec(`UPDATE listens SET ended_at=?, skip_fast=?, during_ad=? WHERE id=?`,
		end.Unix(), b2i(skipFast), b2i(duringAd), id)
	return err
}

func (s *Store) InsertTrack(station, artistKey, artist, title, raw string, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO tracks_heard(station_uuid,artist_key,artist,title,raw,heard_at)
		VALUES(?,?,?,?,?,?)`, station, artistKey, artist, title, raw, at.Unix())
	return err
}

func (s *Store) InsertLoved(station, artistKey, artist, title string, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO loved(station_uuid,artist_key,artist,title,loved_at)
		VALUES(?,?,?,?,?)`, station, artistKey, artist, title, at.Unix())
	return err
}

func (s *Store) LovedArtistKeys() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT DISTINCT artist_key FROM loved WHERE artist_key != ''`)
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
	rows, err := s.db.Query(`SELECT DISTINCT station_uuid, artist_key FROM tracks_heard WHERE artist_key != ''`)
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

func (s *Store) GetBandit(station, daypart string) (banditRow, bool, error) {
	var r banditRow
	var ts int64
	err := s.db.QueryRow(`SELECT alpha,beta,updated_at FROM bandit WHERE station_uuid=? AND daypart=?`,
		station, daypart).Scan(&r.Alpha, &r.Beta, &ts)
	if err == sql.ErrNoRows {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	r.UpdatedAt = time.Unix(ts, 0)
	return r, true, nil
}

func (s *Store) PutBandit(station, daypart string, alpha, beta float64, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO bandit(station_uuid,daypart,alpha,beta,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(station_uuid,daypart) DO UPDATE SET alpha=excluded.alpha, beta=excluded.beta, updated_at=excluded.updated_at`,
		station, daypart, alpha, beta, at.Unix())
	return err
}

// AllBandit loads every bandit row grouped by station then daypart.
func (s *Store) AllBandit() (map[string]map[string]banditRow, error) {
	rows, err := s.db.Query(`SELECT station_uuid,daypart,alpha,beta,updated_at FROM bandit`)
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
	rows, err := s.db.Query(`SELECT tag,weight,updated_at FROM tag_affinity`)
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
	_, err := s.db.Exec(`INSERT INTO tag_affinity(tag,weight,updated_at) VALUES(?,?,?)
		ON CONFLICT(tag) DO UPDATE SET weight=excluded.weight, updated_at=excluded.updated_at`,
		tag, weight, at.Unix())
	return err
}

// --- meta ---

func (s *Store) GetMeta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// --- presets ---

func (s *Store) Presets() (map[int]string, error) {
	rows, err := s.db.Query(`SELECT slot, station_uuid FROM presets`)
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
	_, err := s.db.Exec(`INSERT INTO presets(slot, station_uuid, saved_at) VALUES(?,?,?)
		ON CONFLICT(slot) DO UPDATE SET station_uuid=excluded.station_uuid, saved_at=excluded.saved_at`,
		slot, uuid, at.Unix())
	return err
}

func (s *Store) DeletePreset(slot int) error {
	_, err := s.db.Exec(`DELETE FROM presets WHERE slot=?`, slot)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
