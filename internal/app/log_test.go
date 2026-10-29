package app

import (
	"os"
	"path/filepath"
	"testing"
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
