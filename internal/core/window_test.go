package core

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestWindowPlacementUnsavedByDefault(t *testing.T) {
	c := openTestCore(t)
	p := c.WindowPlacement()
	if p.Saved {
		t.Fatalf("fresh DB must report Saved=false: %+v", p)
	}
}

func TestWindowPlacementRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	srv := httptest.NewServer(http.NewServeMux())
	defer srv.Close()
	c, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	pointCoreAt(c, srv)
	want := WindowPlacement{X: -1200, Y: 40, Unfolded: true, Saved: true}
	if err := c.SetWindowPlacement(want); err != nil {
		t.Fatalf("SetWindowPlacement: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	c2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer c2.Close()
	got := c2.WindowPlacement()
	if got != want {
		t.Fatalf("placement did not survive reopen: got %+v want %+v", got, want)
	}
}
