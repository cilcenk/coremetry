package api

import "testing"

// v0.6.46 — guards the self-observability span-name cardinality
// collapse. The otelhttp span name formatter runs collapseRoute on
// every inbound request path; if it regresses to echoing raw path
// params, the spans table's LowCardinality(String) `name` column
// mints one distinct value per trace_id / service / problem id and
// degrades (CLAUDE.md anti-pattern: high-cardinality into a
// LowCardinality column). These cases pin the route templates.
func TestCollapseRoute(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Static routes pass through untouched.
		{"/api/services", "/api/services"},
		{"/api/problems", "/api/problems"},
		{"/api/health", "/api/health"},
		{"/", "/"},
		{"", ""},

		// trace_id (32 hex) and span_id (16 hex) → :id
		{"/api/traces/7a09a3f455ff571853ec5ef8c04d718c", "/api/traces/:id"},
		{"/api/traces/7a09a3f455ff571853ec5ef8c04d718c/share", "/api/traces/:id/share"},
		{"/api/traces/01bf1097b1018351", "/api/traces/:id"},

		// service name (high-cardinality) after "services" → :svc
		{"/api/services/bank-api-gateway/blast-radius", "/api/services/:svc/blast-radius"},
		{"/api/services/fraud-service", "/api/services/:svc"},

		// numeric ids → :id
		{"/api/dashboards/42", "/api/dashboards/:id"},
		{"/api/incidents/1001/timeline", "/api/incidents/:id/timeline"},

		// uuid-ish (with dashes, hex, ≥8) → :id
		{"/api/slos/3f2504e0-4f89-41d3-9a0c-0305e82c3301", "/api/slos/:id"},

		// short non-hex words are NOT collapsed (static sub-routes).
		{"/api/traces/aggregate", "/api/traces/aggregate"},
		{"/api/spans/heatmap", "/api/spans/heatmap"},
	}
	for _, c := range cases {
		if got := collapseRoute(c.in); got != c.want {
			t.Errorf("collapseRoute(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestIsVolatileSegment(t *testing.T) {
	volatile := []string{
		"7a09a3f455ff571853ec5ef8c04d718c", // trace id
		"01bf1097b1018351",                 // span id
		"42", "1001",                       // numeric
		"3f2504e0-4f89-41d3-9a0c-0305e82c3301", // uuid
		"deadbeefcafe",                     // 12-hex
	}
	for _, s := range volatile {
		if !isVolatileSegment(s) {
			t.Errorf("isVolatileSegment(%q) = false; want true", s)
		}
	}
	static := []string{
		"services", "aggregate", "heatmap", "blast-radius",
		"share", "timeline", "config",
		"abc123", // 6 chars — under the 8-char hex floor, kept
	}
	for _, s := range static {
		if isVolatileSegment(s) {
			t.Errorf("isVolatileSegment(%q) = true; want false", s)
		}
	}
}
