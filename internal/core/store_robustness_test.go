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

func padInt(i int) string {
	s := strconv.Itoa(i)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}
