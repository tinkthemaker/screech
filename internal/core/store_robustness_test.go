package core

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A database that won't open gets moved aside — WAL and SHM companions with
// it — and a fresh store takes its place, reporting where the damage went.
func TestCorruptDatabaseIsArchivedAndRecovered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "screech.db")
	garbage := []byte("this is not a sqlite database at all, not even close")
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(path+ext, []byte("stale journal"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	c, err := Open(path)
	if err != nil {
		t.Fatalf("Open should recover a corrupt database, got: %v", err)
	}
	defer c.Close()
	if c.RecoveredFrom() == "" {
		t.Fatal("RecoveredFrom should name the archive path")
	}

	// The corrupt file moved to the .corrupt stem; the live path is a new
	// db. (SQLite typically unlinks the -wal/-shm journal files itself when
	// the failed open's connection closes, so there is usually nothing left
	// for archiveCorruptDB to move — it still moves them when they persist,
	// e.g. on a quick_check failure after a valid open.)
	if _, err := os.Stat(c.RecoveredFrom()); err != nil {
		t.Errorf("archived database missing: %v", err)
	}
	matches, err := filepath.Glob(path + ".corrupt.*")
	if err != nil || len(matches) == 0 {
		t.Errorf("expected the .corrupt stem, found %v", matches)
	}
	for _, m := range matches {
		if !strings.HasPrefix(m, c.RecoveredFrom()) {
			t.Errorf("archive file %q not under stem %q", m, c.RecoveredFrom())
		}
	}
	if c.StationCount() == 0 {
		t.Error("fresh database should still carry the seed stations")
	}
}

// A database that opens but fails quick_check is recovered through the same
// path (errIntegrity). Constructing a file that reads as valid SQLite yet
// reports corruption is impractical — a damaged page usually fails the open
// itself — so this only asserts the sentinel classifies as corruption.
func TestIntegrityFailureCountsAsCorruption(t *testing.T) {
	if !isCorruption(errIntegrity) {
		t.Error("errIntegrity must classify as corruption")
	}
}

func TestMigrationStampsUserVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v, err := st.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != len(migrations) {
		t.Errorf("user_version=%d, want %d (all migrations applied)", v, len(migrations))
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type='index' AND name IN ('idx_loved_artist_title','idx_loved_station')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("v2 loved indexes missing: found %d", n)
	}
}

// A database stamped v1 gains the v2 indexes on open.
func TestMigrationV2AppliesToV1Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+escapeDSNPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations[0].sql); err != nil {
		t.Fatalf("build v1 schema: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore on v1 db: %v", err)
	}
	defer st.Close()
	v, _ := st.SchemaVersion()
	if v != len(migrations) {
		t.Errorf("user_version=%d after migrate, want %d", v, len(migrations))
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type='index' AND name IN ('idx_loved_artist_title','idx_loved_station')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("v2 indexes not applied to v1 database: found %d", n)
	}
}

// The chunked commit must land every row: 1,234 stations crosses two
// 500-row chunks plus a tail.
func TestUpsertStationsChunksLandAllRows(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "chunk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	sts := make([]Station, 1234)
	for i := range sts {
		sts[i] = Station{
			UUID: "chunk-" + padInt(i), Name: "Station " + padInt(i),
			URL: "http://s/" + padInt(i), LastCheckOK: true,
		}
	}
	if err := st.UpsertStations(sts, time.Now()); err != nil {
		t.Fatalf("UpsertStations: %v", err)
	}
	n, err := st.StationCount()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1234 {
		t.Fatalf("StationCount=%d, want 1234", n)
	}
	var name string
	if err := st.db.QueryRow(`SELECT name FROM stations WHERE uuid='chunk-0999'`).Scan(&name); err != nil {
		t.Fatalf("row in the tail chunk missing: %v", err)
	}
}

// Text stored before CleanText existed is rewritten by migration 3 so the
// cleaned ingest keeps matching it: love toggles, x forgets, play counts
// all key on the cleaned form.
func TestMigrationV3CleansStoredText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.db")
	db, err := sql.Open("sqlite", "file:"+escapeDSNPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations[0].sql + migrations[1].sql); err != nil {
		t.Fatalf("build v2 schema: %v", err)
	}
	now := time.Now().Unix()
	if _, err := db.Exec(`INSERT INTO loved(station_uuid, artist_key, artist, title, loved_at)
		VALUES('s1','stalekey','Art'||char(27)||'ist','Some  Song',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tracks_heard(station_uuid, artist_key, artist, title, raw, heard_at)
		VALUES('s1','stalekey','Art'||char(27)||'ist','Some  Song','Some'||char(27)||']0;x'||char(7)||'  Song',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore on v2 db: %v", err)
	}
	defer st.Close()
	if v, _ := st.SchemaVersion(); v != len(migrations) {
		t.Fatalf("user_version=%d, want %d", v, len(migrations))
	}

	// "Art<ESC>ist" loses ESC and the 'i' it swallowed (a two-byte
	// sequence), so the cleaned artist is "Artst" and the stale key is
	// recomputed from it.
	wantKey, wantArtist, title := NormalizeArtist("Artst"), "Artst", "Some Song"
	var key, artist, raw string
	if err := st.db.QueryRow(`SELECT artist_key, artist FROM loved`).Scan(&key, &artist); err != nil {
		t.Fatal(err)
	}
	if artist != wantArtist || key != wantKey {
		t.Errorf("loved row = (%q, %q), want (%q, %q)", key, artist, wantKey, wantArtist)
	}
	if err := st.db.QueryRow(`SELECT artist, raw FROM tracks_heard`).Scan(&artist, &raw); err != nil {
		t.Fatal(err)
	}
	if raw != "Some Song" {
		t.Errorf("tracks_heard raw = %q, want %q", raw, "Some Song")
	}
	// The point of the migration: the queries the UI runs match cleaned values.
	if ok, err := st.LovedTrackExists(wantKey, title); err != nil || !ok {
		t.Errorf("LovedTrackExists(%q, %q) = %v, %v; want true", wantKey, title, ok, err)
	}
	if n, err := st.TrackPlayCount(wantKey, title); err != nil || n != 1 {
		t.Errorf("TrackPlayCount(%q, %q) = %d, %v; want 1", wantKey, title, n, err)
	}
	if removed, err := st.ForgetLovedTrack(wantKey, title); err != nil || !removed {
		t.Errorf("ForgetLovedTrack(%q, %q) = %v, %v; want true", wantKey, title, removed, err)
	}
}

// Tracks logged while the player still forwarded mpv's URL-basename
// media-title are filename junk; migration 4 removes them while a genuine
// titleless-artist row for the same station survives.
func TestMigrationV4PurgesFilenameTracks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.db")
	db, err := sql.Open("sqlite", "file:"+escapeDSNPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations[0].sql + migrations[1].sql); err != nil {
		t.Fatalf("build v3 schema: %v", err)
	}
	now := time.Now().Unix()
	if _, err := db.Exec(`INSERT INTO stations(uuid, name, url, url_resolved, fetched_at)
		VALUES('s1','Radio','https://h/x/aac-128','https://h/x/aac-128',?)`, now); err != nil {
		t.Fatal(err)
	}
	// Junk: title is the stream's URL basename, no artist.
	if _, err := db.Exec(`INSERT INTO tracks_heard(station_uuid, artist_key, artist, title, raw, heard_at)
		VALUES('s1','','','aac-128','aac-128',?)`, now); err != nil {
		t.Fatal(err)
	}
	// Legit: a real title that simply has no artist metadata.
	if _, err := db.Exec(`INSERT INTO tracks_heard(station_uuid, artist_key, artist, title, raw, heard_at)
		VALUES('s1','','','Live Session','Live Session',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore on v3 db: %v", err)
	}
	defer st.Close()
	if v, _ := st.SchemaVersion(); v != len(migrations) {
		t.Fatalf("user_version=%d, want %d", v, len(migrations))
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM tracks_heard WHERE title='aac-128'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("filename junk row should be purged, %d remain", n)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM tracks_heard WHERE title='Live Session'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("legitimate trackless row should survive, found %d", n)
	}
}

func padInt(i int) string {
	s := strconv.Itoa(i)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}
