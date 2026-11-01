package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLogBuffersUntilOpen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "screech.log")
	os.WriteFile(p, []byte("previous run\n"), 0o600)
	l := NewLog(p)
	l.Linef("one %d", 1)
	if b, _ := os.ReadFile(p); string(b) != "previous run\n" {
		t.Fatalf("log touched before Open: %q", b)
	}
	l.Open()
	l.Linef("two")
	l.Close()
	if b, _ := os.ReadFile(p); string(b) != "one 1\ntwo\n" {
		t.Fatalf("got %q, want buffered line then live line, old run truncated", b)
	}
}

// The player logs from its own goroutines while the faces log from theirs and
// the app closes the log at exit; none of that may tear a line, write one
// twice, or race (run under -race).
func TestLogIsSafeForConcurrentUse(t *testing.T) {
	p := filepath.Join(t.TempDir(), "screech.log")
	l := NewLog(p)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				l.Linef("writer %d line %d", g, i)
			}
		}()
	}
	// Opened while they write, and closed while they still do.
	wg.Add(1)
	go func() {
		defer wg.Done()
		l.Open()
		l.Open()
		time.Sleep(2 * time.Millisecond)
		l.Close()
		l.Close()
	}()
	wg.Wait()

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		var g, i int
		if n, err := fmt.Sscanf(line, "writer %d line %d", &g, &i); n != 2 || err != nil || line != fmt.Sprintf("writer %d line %d", g, i) {
			t.Fatalf("a line was torn: %q", line)
		}
		if seen[line] {
			t.Fatalf("a line was written twice: %q", line)
		}
		seen[line] = true
	}
}

// A line written after Close goes to no file and breaks nothing.
func TestLogAfterCloseIsHarmless(t *testing.T) {
	p := filepath.Join(t.TempDir(), "screech.log")
	l := NewLog(p)
	l.Open()
	l.Linef("before")
	l.Close()
	l.Linef("after")
	if b, _ := os.ReadFile(p); string(b) != "before\n" {
		t.Fatalf("log = %q, want only the line written while open", b)
	}
}
