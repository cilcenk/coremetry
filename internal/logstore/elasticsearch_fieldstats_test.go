package logstore

import "testing"

// v0.8.255 — /api/logs/fieldstats (Discover fields-panel accordion).
// The operator's standing constraint on this feature was "don't grow
// Elastic API usage": the endpoint is expand-triggered + 60s-cached,
// and the query body itself must carry the same cost guards the
// v0.8.3 histogram incident taught us (size:0, soft timeout, no
// total-hits tracking, bounded terms agg). These assertions pin
// those guards so a future edit can't silently regress the body
// into an expensive shape.
func TestBuildFieldStatsBody_CostGuards(t *testing.T) {
	query := map[string]any{"match_all": map[string]any{}}
	body := buildFieldStatsBody(query, "kubernetes.container_name.keyword", 5, "10s")

	if got := body["size"]; got != 0 {
		t.Fatalf("size = %v, want 0 (agg-only request must not fetch hits)", got)
	}
	if got := body["track_total_hits"]; got != false {
		t.Fatalf("track_total_hits = %v, want false", got)
	}
	if got := body["timeout"]; got != "10s" {
		t.Fatalf("timeout = %v, want the ES soft-timeout", got)
	}

	aggs, ok := body["aggs"].(map[string]any)
	if !ok {
		t.Fatalf("no aggs map: %#v", body)
	}
	vals, ok := aggs["vals"].(map[string]any)
	if !ok {
		t.Fatalf("no vals agg: %#v", aggs)
	}
	terms, ok := vals["terms"].(map[string]any)
	if !ok {
		t.Fatalf("vals is not a terms agg: %#v", vals)
	}
	if got := terms["field"]; got != "kubernetes.container_name.keyword" {
		t.Fatalf("terms.field = %v", got)
	}
	if got := terms["size"]; got != 5 {
		t.Fatalf("terms.size = %v, want 5 (top-5 accordion, never unbounded)", got)
	}
	if got := terms["shard_size"]; got != 50 {
		t.Fatalf("terms.shard_size = %v, want size*10", got)
	}
	if _, hasHighlight := body["highlight"]; hasHighlight {
		t.Fatalf("body must not request highlighting")
	}
}
