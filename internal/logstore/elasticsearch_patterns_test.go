package logstore

import "testing"

// v0.8.164 — every per-pattern CountPatterns _msearch subquery is a
// size:0 aggregation, the same shape that drove the v0.8.3 histogram
// incident (api-pod CPU climb → restart) on external ES. The _msearch
// API can't carry a request-level timeout, so each subquery body MUST
// carry its own `timeout` plus `track_total_hits:false` — at parity with
// Search / searchForward / buildHistogramBody. This pins both guards so a
// future edit can't silently drop them and reopen the unbounded-fan-out
// gap. CH's sibling is bounded by max_execution_time; only the ES path
// regressed.
func TestPatternCountBody_CarriesCostGuards(t *testing.T) {
	body := patternCountBody(
		`body:"tok"`, "message", "@timestamp", "service.name",
		"2026-06-15T00:00:00Z", "2026-06-15T09:00:00Z", "2026-06-15T10:00:00Z",
		"10s")

	if got, ok := body["timeout"].(string); !ok || got != "10s" {
		t.Fatalf("subquery body must carry the per-request timeout, got %v", body["timeout"])
	}
	if tth, ok := body["track_total_hits"].(bool); !ok || tth != false {
		t.Fatalf("subquery body must set track_total_hits:false, got %v", body["track_total_hits"])
	}
	if sz, ok := body["size"].(int); !ok || sz != 0 {
		t.Fatalf("subquery must stay size:0 (aggregation only), got %v", body["size"])
	}
	// Sanity: the windowed aggs that drive the detector are still present.
	aggs, ok := body["aggs"].(map[string]any)
	if !ok {
		t.Fatal("body lost its aggs")
	}
	if _, ok := aggs["cur_window"]; !ok {
		t.Fatal("body lost the cur_window agg")
	}
	if _, ok := aggs["base_window"]; !ok {
		t.Fatal("body lost the base_window agg")
	}
}
