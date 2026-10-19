package tui

import (
	"strings"
	"testing"

	"github.com/tinkthemaker/screech/internal/session"
)

// The connectivity state machine — drops, strikes, offline retries,
// backend death — lives in internal/session now, with its own tests.
// What remains here is the face's readout of the retry timer.
func TestOfflineStatusReadsRetryingNow(t *testing.T) {
	m := testModel(t)
	m.s.Station = testSeeds()[0]
	m.s.HaveStation = true
	m.s.Phase = session.Offline
	// RetryAt stays zero — a retry is in flight.
	if row := stripANSI(m.reasonRow(60)); !strings.Contains(row, "retrying now") {
		t.Fatalf("status row should read 'retrying now', got %q", row)
	}
}
