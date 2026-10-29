package main

import (
	"bytes"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
)

func TestParseArgsExport(t *testing.T) {
	cases := []struct {
		name string
		args []string
		kind string
		path string
		err  string // substring; "" means no error expected
	}{
		{"loved", []string{"export", "loved"}, "loved", "", ""},
		{"heard", []string{"export", "heard"}, "heard", "", ""},
		{"backup", []string{"export", "backup", "p"}, "backup", "p", ""},
		{"flag inside operands", []string{"export", "backup", "--reduced-motion", "p"}, "backup", "p", ""},
		{"bare export", []string{"export"}, "", "", "needs a kind"},
		{"unknown kind", []string{"export", "nope"}, "", "", `unknown export kind "nope"`},
		{"backup missing path", []string{"export", "backup"}, "", "", "exactly one"},
		{"backup extra path", []string{"export", "backup", "a", "b"}, "", "", "exactly one"},
		{"loved with operand", []string{"export", "loved", "extra"}, "", "", "no further arguments"},
		{"second command", []string{"doctor", "export"}, "", "", "one command"},
		{"command after export", []string{"export", "loved", "doctor"}, "", "", "no further arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inv, err := parseArgs(tc.args)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("parseArgs(%v) err = %v, want %q", tc.args, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseArgs(%v) error: %v", tc.args, err)
			}
			if inv.cmd != cmdExport || inv.exportKind != tc.kind || inv.exportPath != tc.path {
				t.Fatalf("parseArgs(%v) = {%v %q %q}, want export %q %q",
					tc.args, inv.cmd, inv.exportKind, inv.exportPath, tc.kind, tc.path)
			}
		})
	}
}

// seedExportDB builds a database the way the radio writes one: listen,
// titles, loves — no direct store pokes from the cmd package.
func seedExportDB(t *testing.T, dbPath string, keepOpen bool) *core.Core {
	t.Helper()
	c, err := core.Open(dbPath)
	if err != nil {
		t.Fatalf("core.Open: %v", err)
	}
	if !keepOpen {
		defer c.Close()
	}
	now := time.Now()

	pick, err := c.Tune(now)
	if err != nil {
		t.Fatalf("Tune: %v", err)
	}
	uuid := pick.Station.UUID
	c.StartListen(uuid, now)
	c.NoteTitle(`Weird Artist - =HYPERLINK("x")`, now.Add(time.Minute))
	c.Love(now.Add(time.Minute))
	c.NoteTitle(`Plain Artist - Say, "Hi"`, now.Add(2*time.Minute))
	c.Love(now.Add(2 * time.Minute))
	// Another raw in between so the repeat isn't swallowed by the ICY dedupe.
	c.NoteTitle(`Other Artist - Other Track`, now.Add(3*time.Minute))
	c.NoteTitle(`Plain Artist - Say, "Hi"`, now.Add(4*time.Minute))
	c.EndListen(now.Add(5 * time.Minute))

	// A second station so rows differ, plus a track that stays unloved.
	if next, err := c.Tune(now.Add(5 * time.Minute)); err == nil && next.Station.UUID != uuid {
		c.StartListen(next.Station.UUID, now.Add(5*time.Minute))
		c.NoteTitle("Other Artist - Other Track", now.Add(6*time.Minute))
		c.EndListen(now.Add(7 * time.Minute))
	}
	return c
}

func TestExportMissingDatabase(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.db")
	var out bytes.Buffer
	if code := exportData(missing, "loved", "", &out); code != 1 {
		t.Fatalf("exportData on missing db = %d, want 1", code)
	}
}

func TestExportLoved(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	seedExportDB(t, dbPath, false)

	var out bytes.Buffer
	if code := exportData(dbPath, "loved", "", &out); code != 0 {
		t.Fatalf("export loved = %d", code)
	}
	rows, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatalf("parsing CSV: %v\n%s", err, out.String())
	}
	wantHeader := []string{"loved_at", "artist", "title", "station", "station_uuid", "times_loved"}
	if len(rows) < 2 || strings.Join(rows[0], ",") != strings.Join(wantHeader, ",") {
		t.Fatalf("header/rows wrong: %v", rows)
	}
	if len(rows) != 3 {
		t.Fatalf("want header + 2 deduplicated loved rows, got %d rows: %v", len(rows), rows)
	}
	// The deduplicated list is newest first, so the plain row leads.
	if rows[1][2] != `Say, "Hi"` {
		t.Fatalf("first loved row title = %q", rows[1][2])
	}
	if rows[1][5] != "1" {
		t.Fatalf("times_loved = %q, want 1", rows[1][5])
	}
	if _, err := time.Parse(time.RFC3339, rows[1][0]); err != nil {
		t.Fatalf("loved_at %q is not RFC3339", rows[1][0])
	}
	// The injection title keeps its content but gains the guard quote.
	if got := rows[2][2]; got != `'=HYPERLINK("x")` {
		t.Fatalf("formula title = %q, want guarded", got)
	}
}

func TestExportHeard(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	seedExportDB(t, dbPath, false)

	// A row with no title — old or odd feeds do land like this — must not
	// reach the CSV.
	s, err := core.OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := s.InsertTrack("x", "key", "artist", "", "raw", time.Now()); err != nil {
		t.Fatalf("InsertTrack: %v", err)
	}
	s.Close()

	var out bytes.Buffer
	if code := exportData(dbPath, "heard", "", &out); code != 0 {
		t.Fatalf("export heard = %d", code)
	}
	rows, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatalf("parsing CSV: %v\n%s", err, out.String())
	}
	if strings.Join(rows[0], ",") != "heard_at,artist,title,station,station_uuid" {
		t.Fatalf("header = %v", rows[0])
	}
	if len(rows) != 6 { // header + 5 heard rows, dupes kept
		t.Fatalf("want header + 5 rows, got %d: %v", len(rows), rows)
	}
	// Newest first: the second station's track leads.
	if rows[1][2] != "Other Track" {
		t.Fatalf("first heard row = %q, want Other Track", rows[1][2])
	}
	says := 0
	for _, r := range rows[1:] {
		if r[2] == `Say, "Hi"` {
			says++
		}
	}
	if says != 2 {
		t.Fatalf("duplicated hears not preserved: %v", rows)
	}
	for i, r := range rows[1:] {
		if strings.TrimSpace(r[2]) == "" {
			t.Fatalf("row %d has an empty title", i)
		}
	}
}

func TestExportBackup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "t.db")
	open := seedExportDB(t, dbPath, true) // stays open: export must work while playing
	defer open.Close()

	dest := filepath.Join(dir, "b.db")
	var out bytes.Buffer
	if code := exportData(dbPath, "backup", dest, &out); code != 0 {
		t.Fatalf("export backup = %d", code)
	}
	if !strings.HasPrefix(out.String(), "backed up to "+dest) {
		t.Fatalf("output = %q", out.String())
	}
	// The copy opens read-only and carries the same loved set.
	s, err := core.OpenStoreReadOnly(dest)
	if err != nil {
		t.Fatalf("backup not readable: %v", err)
	}
	tracks, err := s.LovedTracks(-1)
	s.Close()
	if err != nil {
		t.Fatalf("reading backup: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("backup loved count = %d, want 2", len(tracks))
	}

	// Refuses to overwrite.
	if code := exportData(dbPath, "backup", dest, &out); code != 1 {
		t.Fatalf("backup over existing = %d, want 1", code)
	}
}

func TestExportBackupCleansPartial(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "t.db")
	seedExportDB(t, dbPath, false)
	// A destination inside a missing directory fails VACUUM INTO; nothing
	// may be left behind.
	dest := filepath.Join(dir, "gone", "b.db")
	var out bytes.Buffer
	if code := exportData(dbPath, "backup", dest, &out); code != 1 {
		t.Fatalf("backup to missing dir = %d, want 1", code)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("partial backup left at %s", dest)
	}
}
