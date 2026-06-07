package chstore

import (
	"strings"
	"testing"
	"time"
)

// metricresolve_test.go — guards the v0.8.51 ("every metric is a doorway"
// Phase D / D2) resolver planner. The grain-selection + agg-formula logic is
// pure so every boundary is exercised without a live ClickHouse. This is the
// [[feedback-unit-mixing-needs-both-branches]] discipline: a metric resolver
// that silently picks the wrong tier (or emits the wrong *Merge for one agg)
// looks fine on the happy path and wrong everywhere else — so every tier
// boundary and every metric+agg combination is asserted here.

func TestMetricAutoStep(t *testing.T) {
	base := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		span time.Duration
		want int
	}{
		{2 * time.Minute, 1},          // ≤2m
		{2*time.Minute + time.Second, 5}, // >2m
		{10 * time.Minute, 5},         // ≤10m
		{10*time.Minute + time.Second, 10},
		{30 * time.Minute, 10},
		{30*time.Minute + time.Second, 30},
		{time.Hour, 30},
		{time.Hour + time.Second, 60},
		{6 * time.Hour, 60},
		{6*time.Hour + time.Second, 300},
		{24 * time.Hour, 300},
		{24*time.Hour + time.Second, 1800},
		{7 * 24 * time.Hour, 1800},
		{7*24*time.Hour + time.Second, 3600},
	}
	for _, c := range cases {
		got := metricAutoStep(base, base.Add(c.span))
		if got != c.want {
			t.Errorf("metricAutoStep(span=%s) = %d, want %d", c.span, got, c.want)
		}
	}
}

func TestSelectMetricTier(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	// Cutover floor well in the past so retention/grain are the binding
	// constraints unless a case overrides `from` to predate it.
	oldCoverage := now.Add(-90 * 24 * time.Hour)

	route := map[string]string{"http.route": "/v1/pay"}
	offDim := map[string]string{"db.system": "postgresql"}

	cases := []struct {
		name      string
		from      time.Duration // relative to now (negative = into the past)
		step      int
		coverage  time.Time
		filters   map[string]string
		groupBy   []string
		wantTier  string // "" → expect fallback (ok=false)
	}{
		{name: "2m window auto→1s", from: -2 * time.Minute, step: 1, coverage: oldCoverage, wantTier: "1s"},
		{name: "30m window step10→10s", from: -30 * time.Minute, step: 10, coverage: oldCoverage, wantTier: "10s"},
		{name: "6h window step60→1m", from: -6 * time.Hour, step: 60, coverage: oldCoverage, wantTier: "1m"},
		{name: "1d window step300→1m", from: -24 * time.Hour, step: 300, coverage: oldCoverage, wantTier: "1m"},
		{name: "7d window step1800→1m", from: -7 * 24 * time.Hour, step: 1800, coverage: oldCoverage, wantTier: "1m"},

		// route predicate: 10s carries http_route, 1s does not.
		{name: "route + step10 → 10s", from: -30 * time.Minute, step: 10, coverage: oldCoverage, filters: route, wantTier: "10s"},
		{name: "route + step1 → fallback (1s lacks route, coarser too coarse)", from: -2 * time.Minute, step: 1, coverage: oldCoverage, filters: route, wantTier: ""},
		{name: "route in groupBy + step60 → 1m", from: -6 * time.Hour, step: 60, coverage: oldCoverage, groupBy: []string{"http.route"}, wantTier: "1m"},

		// off-dimension predicate → only raw spans can answer.
		{name: "off-dim filter → fallback", from: -30 * time.Minute, step: 10, coverage: oldCoverage, filters: offDim, wantTier: ""},
		{name: "off-dim groupBy → fallback", from: -30 * time.Minute, step: 10, coverage: oldCoverage, groupBy: []string{"db.system"}, wantTier: ""},

		// retention horizons.
		{name: "40d window beyond 1m ttl → fallback", from: -40 * 24 * time.Hour, step: 3600, coverage: oldCoverage, wantTier: ""},
		{name: "7h window step1 beyond 1s ttl(6h) → fallback", from: -7 * time.Hour, step: 1, coverage: oldCoverage, wantTier: ""},
		{name: "exactly at 30d ttl edge → 1m", from: -30 * 24 * time.Hour, step: 3600, coverage: oldCoverage, wantTier: "1m"},

		// forward-only cutover: window predates available fine-grain data.
		{name: "window predates cutover → fallback", from: -2 * time.Minute, step: 1, coverage: now.Add(-1 * time.Minute), wantTier: ""},
		{name: "window starts exactly at cutover → 1s", from: -1 * time.Minute, step: 1, coverage: now.Add(-1 * time.Minute), wantTier: "1s"},
	}

	for _, c := range cases {
		from := now.Add(c.from)
		to := now
		tier, ok := selectMetricTier(from, to, c.step, c.coverage, now, c.filters, c.groupBy)
		if c.wantTier == "" {
			if ok {
				t.Errorf("%s: expected fallback, got tier %q", c.name, tier.label)
			}
			continue
		}
		if !ok {
			t.Errorf("%s: expected tier %q, got fallback", c.name, c.wantTier)
			continue
		}
		if tier.label != c.wantTier {
			t.Errorf("%s: tier = %q, want %q", c.name, tier.label, c.wantTier)
		}
	}
}

func TestSpanmetricStateAgg(t *testing.T) {
	const step = 30
	cases := []struct {
		agg      string
		contains string // a load-bearing fragment of the expected CH expression
	}{
		{"count", "countMerge(calls_state)"},
		{"", "countMerge(calls_state)"}, // empty defaults to count
		{"rate", "countMerge(calls_state) / 30.0"},
		{"errors", "countMerge(error_state)"},
		{"error_rate", "100.0 * countMerge(error_state) / nullIf(countMerge(calls_state), 0)"},
		{"sum", "sumMerge(duration_sum_state) / 1e6"},
		{"avg", "sumMerge(duration_sum_state) / nullIf(countMerge(calls_state), 0) / 1e6"},
		{"p50", "(duration_q_state), 1) / 1e6"},
		{"p90", "(duration_q_state), 2) / 1e6"},
		{"p95", "(duration_q_state), 3) / 1e6"},
		{"p99", "(duration_q_state), 4) / 1e6"},
	}
	for _, c := range cases {
		got, err := spanmetricStateAgg(c.agg, step)
		if err != nil {
			t.Errorf("spanmetricStateAgg(%q) unexpected error: %v", c.agg, err)
			continue
		}
		if !strings.Contains(got, c.contains) {
			t.Errorf("spanmetricStateAgg(%q) = %q, want to contain %q", c.agg, got, c.contains)
		}
		// Every projection must be float-safe for the *float64 scanner.
		if !strings.HasPrefix(got, "toNullable(toFloat64(") {
			t.Errorf("spanmetricStateAgg(%q) = %q, missing toNullable(toFloat64 wrap", c.agg, got)
		}
	}
	// Percentile indices must match the stored histogram order (0.5,0.9,0.95,0.99).
	for agg, idx := range map[string]string{"p50": ", 1)", "p90": ", 2)", "p95": ", 3)", "p99": ", 4)"} {
		got, _ := spanmetricStateAgg(agg, step)
		if !strings.Contains(got, "quantilesMerge(0.5, 0.9, 0.95, 0.99)") || !strings.Contains(got, idx) {
			t.Errorf("spanmetricStateAgg(%q) = %q, histogram index/order wrong", agg, got)
		}
	}
	if _, err := spanmetricStateAgg("bogus", step); err == nil {
		t.Error("spanmetricStateAgg(bogus) expected error, got nil")
	}
}

// TestSpanmetricExemplarCols pins the v0.8.51 runtime-gate catch: the error
// exemplar is an argMaxIfState, so it MUST finalize with argMaxIfMerge — not
// argMaxMerge (which CH rejects: "argMax requires two arguments"). A
// string-match test alone wouldn't have caught the original bug, so this
// asserts the corrected finalizers and the prior wrong form's ABSENCE.
func TestSpanmetricExemplarCols(t *testing.T) {
	got := spanmetricExemplarCols()
	if !strings.Contains(got, "argMaxMerge(slow_exemplar_state)") {
		t.Errorf("slow exemplar must finalize via argMaxMerge; got %q", got)
	}
	if !strings.Contains(got, "argMaxIfMerge(error_exemplar_state)") {
		t.Errorf("error exemplar must finalize via argMaxIfMerge (it is an argMaxIfState); got %q", got)
	}
	if strings.Contains(got, "argMaxMerge(error_exemplar_state)") {
		t.Errorf("regression: error exemplar wrongly uses argMaxMerge (CH rejects the If-state); got %q", got)
	}
}

func TestTierDimColumn(t *testing.T) {
	ok := map[string]string{
		"service.name": "service_name", "service_name": "service_name",
		"name": "name", "operation": "name",
		"kind": "kind", "span.kind": "kind",
		"status": "status_code", "status_code": "status_code",
		"http.route": "http_route", "http_route": "http_route",
	}
	for key, want := range ok {
		got, isOK := tierDimColumn(key)
		if !isOK || got != want {
			t.Errorf("tierDimColumn(%q) = (%q,%v), want (%q,true)", key, got, isOK, want)
		}
	}
	for _, key := range []string{"db.system", "host.name", "deployment.environment", "trace.id", "anything"} {
		if _, isOK := tierDimColumn(key); isOK {
			t.Errorf("tierDimColumn(%q) should be off-dimension (ok=false)", key)
		}
	}
}
