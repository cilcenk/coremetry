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
	if got := snapshotLogsJSON(nil, snapshotLogsMax, 0); got != "" {
		t.Fatalf("nil logs must keep the column empty, got %q", got)
	}
	if got := snapshotLogsJSON([]*logstore.LogRecord{}, snapshotLogsMax, 0); got != "" {
		t.Fatalf("empty logs must keep the column empty, got %q", got)
	}

	mk := func(n int) []*logstore.LogRecord {
		out := make([]*logstore.LogRecord, n)
		for i := range out {
			out[i] = &logstore.LogRecord{ServiceName: "checkout", Body: "payment ok", SeverityText: "INFO"}
		}
		return out
	}

	// v0.9.475 — zarf: {logs, total, truncated}. Round-trip + wire adları.
	type env struct {
		Logs      []map[string]any `json:"logs"`
		Total     int64            `json:"total"`
		Truncated bool             `json:"truncated"`
	}
	raw := snapshotLogsJSON(mk(3), snapshotLogsMax, 3)
	var back env
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("snapshot not valid JSON: %v", err)
	}
	if len(back.Logs) != 3 || back.Truncated {
		t.Fatalf("round-trip = %d rows, truncated=%v; want 3, false", len(back.Logs), back.Truncated)
	}
	if !strings.Contains(raw, `"checkout"`) || !strings.Contains(raw, "payment ok") {
		t.Fatalf("wire payload missing fields: %s", raw)
	}

	// Cap: 600 in, snapshotLogsMax out — truncated İŞARETLİ, asla hata
	// değil. Dış alıcının kesikliği bilmesinin tek yolu bu bayrak.
	raw = snapshotLogsJSON(mk(600), snapshotLogsMax, 600)
	back = env{}
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("capped snapshot not valid JSON: %v", err)
	}
	if len(back.Logs) != snapshotLogsMax || !back.Truncated || back.Total != 600 {
		t.Fatalf("cap = %d rows, truncated=%v, total=%d; want %d, true, 600",
			len(back.Logs), back.Truncated, back.Total, snapshotLogsMax)
	}
}
