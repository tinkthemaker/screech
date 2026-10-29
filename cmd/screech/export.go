package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tinkthemaker/screech/internal/config"
	"github.com/tinkthemaker/screech/internal/core"
)

// runExport resolves the database the same way run and doctor do, then
// hands off. It takes no single-instance lock: export opens the database
// read-only and must work while screech is playing.
func runExport(kind, path string) int {
	_, dbPath, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "screech: config: %v\n", err)
		return 1
	}
	return exportData(dbPath, kind, path, os.Stdout)
}

// exportData runs one export form against an existing database, factored
// out so tests point at a temp file rather than the OS config dir.
func exportData(dbPath, kind, path string, stdout io.Writer) int {
	store, err := core.OpenStoreReadOnly(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "screech: no database yet at %s\n", dbPath)
		} else {
			fmt.Fprintf(os.Stderr, "screech: opening database: %v\n", err)
		}
		return 1
	}
	defer store.Close()

	switch kind {
	case "loved":
		return exportLoved(store, stdout)
	case "heard":
		return exportHeard(store, stdout)
	default: // backup — parseArgs already rejected anything else
		return exportBackup(store, path, stdout)
	}
}

// csvGuard keeps an untrusted cell text in a spreadsheet. Titles and artist
// names come from strangers' servers, and a cell that opens with =, +, -,
// @, tab or CR runs as a formula the moment Excel or Sheets parses it.
func csvGuard(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

func exportLoved(store *core.Store, stdout io.Writer) int {
	tracks, err := store.LovedTracks(-1) // SQLite: a negative LIMIT is no limit
	if err != nil {
		fmt.Fprintf(os.Stderr, "screech: reading loved tracks: %v\n", err)
		return 1
	}
	w := csv.NewWriter(stdout)
	_ = w.Write([]string{"loved_at", "artist", "title", "station", "station_uuid", "times_loved"})
	for _, t := range tracks {
		_ = w.Write([]string{
			t.LovedAt.Local().Format(time.RFC3339),
			csvGuard(t.Artist), csvGuard(t.Title),
			csvGuard(t.StationName), csvGuard(t.StationUUID),
			strconv.Itoa(t.LoveCount),
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		fmt.Fprintf(os.Stderr, "screech: writing CSV: %v\n", err)
		return 1
	}
	return 0
}

func exportHeard(store *core.Store, stdout io.Writer) int {
	tracks, err := store.RecentlyHeard(-1) // every tracks_heard row, newest first
	if err != nil {
		fmt.Fprintf(os.Stderr, "screech: reading heard tracks: %v\n", err)
		return 1
	}
	w := csv.NewWriter(stdout)
	_ = w.Write([]string{"heard_at", "artist", "title", "station", "station_uuid"})
	for _, t := range tracks {
		if strings.TrimSpace(t.Title) == "" {
			continue
		}
		_ = w.Write([]string{
			t.HeardAt.Local().Format(time.RFC3339),
			csvGuard(t.Artist), csvGuard(t.Title),
			csvGuard(t.StationName), csvGuard(t.StationUUID),
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		fmt.Fprintf(os.Stderr, "screech: writing CSV: %v\n", err)
		return 1
	}
	return 0
}

func exportBackup(store *core.Store, path string, stdout io.Writer) int {
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(os.Stderr, "screech: %s already exists; refusing to overwrite\n", path)
		return 1
	}
	if err := store.VacuumInto(path); err != nil {
		// A failed VACUUM INTO can leave a partial file; it is not a
		// valid backup, so it goes rather than masquerading as one.
		_ = os.Remove(path)
		fmt.Fprintf(os.Stderr, "screech: backup failed: %v\n", err)
		return 1
	}
	fi, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "screech: backup failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "backed up to %s (%s)\n", path, formatBytes(fi.Size()))
	return 0
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
