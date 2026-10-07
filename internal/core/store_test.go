package core

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A data_dir is whatever the user typed into config.toml. The DSN is a URI,
// so a path carrying '?', '#', or '%' has to survive being embedded in one:
// unescaped, the first '?' turns the rest of the path into query parameters
// and the database quietly opens somewhere else (or not at all).
func TestOpenStoreEscapesPathInDSN(t *testing.T) {
	name := "data #1 100% rock & roll"
	if runtime.GOOS != "windows" {
		name += " ?maybe" // '?' is not a legal Windows filename character
	}
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "screech.db")

	st, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore(%q): %v", path, err)
	}
	if err := st.SetMeta("volume", "42"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The file has to be where the caller asked for it, and reopening it
	// has to find the same database rather than a fresh one.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database not written to the requested path: %v", err)
	}
	st, err = OpenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()
	v, err := st.GetMeta("volume")
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if v != "42" {
		t.Fatalf("reopened a different database: volume=%q", v)
	}
}

func TestTagListMemoizes(t *testing.T) {
	st := Station{Tags: "Ambient, , DRONE ,ambient"}
	first := st.TagList()
	want := []string{"ambient", "drone", "ambient"}
	if len(first) != len(want) {
		t.Fatalf("TagList() = %q, want %q", first, want)
	}
	for i := range want {
		if first[i] != want[i] {
			t.Fatalf("TagList() = %q, want %q", first, want)
		}
	}
	if second := st.TagList(); &second[0] != &first[0] {
		t.Fatal("TagList must reuse the memoized slice")
	}
	if empty := (&Station{}).TagList(); empty != nil {
		t.Fatalf("tagless station should return nil, got %q", empty)
	}
}
