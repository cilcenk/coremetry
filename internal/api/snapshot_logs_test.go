package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// v0.8.252 — public trace shares freeze the trace's logs at mint time.
// Pins the capture serializer: empty input keeps the column empty (the
// public route then serves []), the cap truncates without erroring,
// and records round-trip through JSON with their wire field names so
// the public viewer can render them exactly like the logged-in UI.
func TestSnapshotLogsJSON(t *testing.T) {
	if got := snapshotLogsJSON(nil, snapshotLogsMax); got != "" {
		t.Fatalf("nil logs must keep the column empty, got %q", got)
	}
	if got := snapshotLogsJSON([]*logstore.LogRecord{}, snapshotLogsMax); got != "" {
		t.Fatalf("empty logs must keep the column empty, got %q", got)
	}

	mk := func(n int) []*logstore.LogRecord {
		out := make([]*logstore.LogRecord, n)
		for i := range out {
			out[i] = &logstore.LogRecord{ServiceName: "checkout", Body: "payment ok", SeverityText: "INFO"}
		}
		return out
	}

	// Round-trip: valid JSON array, wire field names, all rows kept.
	raw := snapshotLogsJSON(mk(3), snapshotLogsMax)
	var back []map[string]any
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("snapshot not valid JSON: %v", err)
	}
	if len(back) != 3 {
		t.Fatalf("round-trip count = %d, want 3", len(back))
	}
	if !strings.Contains(raw, `"checkout"`) || !strings.Contains(raw, "payment ok") {
		t.Fatalf("wire payload missing fields: %s", raw)
	}

	// Cap: 600 in, snapshotLogsMax out — truncated, never an error.
	raw = snapshotLogsJSON(mk(600), snapshotLogsMax)
	back = nil
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("capped snapshot not valid JSON: %v", err)
	}
	if len(back) != snapshotLogsMax {
		t.Fatalf("cap = %d rows, want %d", len(back), snapshotLogsMax)
	}
}
