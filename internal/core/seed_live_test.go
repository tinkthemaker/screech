//go:build live

package core

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Live smoke test against the real radio-browser directory. Skipped by
// default; run with: go test ./internal/core -run Live -v -tags live
func TestLiveSeedResolution(t *testing.T) {
	if testing.Short() {
		t.Skip("live directory test")
	}
	c, err := Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for _, q := range []string{"corridos", "peso pluma", "tito double p", "regional mexican"} {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		res, ok := c.Seed(ctx, q, time.Now())
		cancel()
		if !ok {
			t.Errorf("seed %q: no resolution", q)
			continue
		}
		t.Logf("seed %-20q -> kind=%-7s label=%-30s station=%q reason=%q",
			q, res.Kind, res.Label, res.Pick.Station.Name, res.Pick.Reason)
	}
}
