// N+1 demo scenario contract (v0.9.1284).
//
// Symptom this file guards: the N+1 visibility family shipped in
// v0.9.1277 — the trace-page repeat chip, the ×N sibling grouping and
// Explore's repeats mode — all trigger at 5 repetitions
// (lib/traceRepeats.ts REPEAT_MIN_COUNT / the repeats endpoint's
// minRepeats default), and NO demo trace ever repeated a span name more
// than three times. The whole family was structurally invisible on a
// fresh install. MeshPortfolioValuation is its fixture.
//
// Two contracts are pinned here:
//
//  1. nPlusOneRepeats is LOAD-BOUND, not a constant. docs/DEMO-REALISM.md
//     makes this a rule for every demo knob (a fixed fan-out desyncs from
//     every other signal the way a fixed failure probability would). The
//     table walks the whole curve: at rest, jitter, incident saturation,
//     the ceiling, and the floor. Cutting the latencyFactor term reddens
//     the saturation rows.
//  2. The emitted trace actually carries the signature all three surfaces
//     key on: ≥5 spans sharing (service, span name) AND db.statement,
//     under ONE parent.
package main

import (
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestNPlusOneRepeatsFollowsLoad(t *testing.T) {
	tests := []struct {
		name          string
		latencyFactor float64
		jitter        float64
		want          int
	}{
		// At rest the page is one nominal batch.
		{"idle", 1.0, 0, 8},
		// Jitter is page-size noise, never a mode change.
		{"idle max jitter", 1.0, 1.0, 11},
		{"idle mid jitter", 1.0, 0.5, 10}, // round(8 + 1.5)
		// The four realism.go incident kinds, in ascending severity —
		// these are the rows a de-loaded (constant) fan-out fails.
		{"micro-spike 1.2x", 1.2, 0, 10},
		{"dependency-degraded 1.8x", 1.8, 0, 18},
		{"oracle-row-lock 2.4x", 2.4, 0, 25},
		{"gc-pause-storm 3.2x", 3.2, 0, 34},
		// cpu-steal 4.0x would land at 44 — the ceiling clamps it, and
		// an incident stacked on a micro-spike stays clamped.
		{"cpu-steal 4.0x clamps", 4.0, 0, nPlusOneMax},
		{"stacked 4.8x clamps", 4.8, 1.0, nPlusOneMax},
		// The floor is a PRODUCT guarantee: whatever the load model is
		// retuned to, the demo must stay above the 5 that makes the chip
		// and the repeats mode fire.
		{"sub-nominal load holds the floor", 0.5, 0, nPlusOneMin},
		{"zero load holds the floor", 0, 0, nPlusOneMin},
		// Degenerate inputs are clamped, not propagated.
		{"negative load", -3, 0, nPlusOneMin},
		{"negative jitter", 1.0, -1, 8},
		{"jitter over one", 1.0, 4, 11},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := nPlusOneRepeats(tc.latencyFactor, tc.jitter)
			if got != tc.want {
				t.Errorf("nPlusOneRepeats(%v, %v) = %d, want %d",
					tc.latencyFactor, tc.jitter, got, tc.want)
			}
			if got < 5 {
				t.Errorf("fan-out %d is below the repeat-chip threshold (5) — "+
					"the v0.9.1277 surfaces would be invisible", got)
			}
		})
	}
}

// attrOf reads a span attribute by key, honouring the modern-semconv
// dialect (semconv_mix.go rewrites db.statement → db.query.text for a
// third of the fleet; ingest folds both into the db_statement column).
func attrOf(sp *tracepb.Span, key, modernKey string) (string, bool) {
	get := func(k string) (*commonpb.AnyValue, bool) {
		for _, kv := range sp.Attributes {
			if kv.Key == k {
				return kv.Value, true
			}
		}
		return nil, false
	}
	v, ok := get(key)
	if !ok {
		v, ok = get(modernKey)
	}
	if !ok || v == nil {
		return "", false
	}
	return v.GetStringValue(), true
}

// TestMeshPortfolioNPlusOneSignature drives the REAL chain through the
// production repeat seam and asserts the three-surface signature.
func TestMeshPortfolioNPlusOneSignature(t *testing.T) {
	spec := chainByName(t, "MeshPortfolioValuation")
	tr := buildMeshTraceRoll(spec, neverFail)

	const (
		wantName = "SELECT instrument_prices"
		wantSvc  = "portfolio-service"
	)
	var (
		n       int
		parents = map[string]bool{}
		stmts   = map[string]bool{}
	)
	for _, si := range tr.spans {
		if si.service != wantSvc || si.span.Name != wantName {
			continue
		}
		n++
		parents[string(si.span.ParentSpanId)] = true
		stmt, ok := attrOf(si.span, "db.statement", "db.query.text")
		if !ok {
			t.Fatalf("repeated span %q carries no db.statement — "+
				"/api/spans/repeats groups by exactly that key", wantName)
		}
		stmts[stmt] = true
		if si.span.Kind != tracepb.Span_SPAN_KIND_CLIENT {
			t.Errorf("repeated span kind = %v, want CLIENT", si.span.Kind)
		}
	}

	// The chip / minRepeats threshold. Built through the live seam, so
	// this also proves liveNPlusOneReps is wired to the rep hop.
	if n < 5 {
		t.Fatalf("trace carries %d %q spans, want >= 5 — below the repeat "+
			"chip + Explore minRepeats threshold", n, wantName)
	}
	// ×N sibling grouping folds children of ONE parent: split parents
	// would leave the waterfall drawing N indistinguishable rows.
	if len(parents) != 1 {
		t.Errorf("repeated spans span %d parents, want 1 — the ×N grouping "+
			"only folds siblings", len(parents))
	}
	// One statement, N executions: that is what makes it an N+1 rather
	// than N different queries.
	if len(stmts) != 1 {
		t.Errorf("repeated spans carry %d distinct db.statement values, want 1", len(stmts))
	}

	// The "1" of the 1+N must stay a DIFFERENT statement, or the shape is
	// a fan-out of one query, not a lazy-load N+1.
	var one int
	for _, si := range tr.spans {
		if si.service == wantSvc && si.span.Name == "SELECT positions" {
			one++
			stmt, _ := attrOf(si.span, "db.statement", "db.query.text")
			if stmts[stmt] {
				t.Error("the driving query shares the repeated statement — not a 1+N shape")
			}
		}
	}
	if one != 1 {
		t.Errorf("driving query emitted %d times, want exactly 1", one)
	}
}

// TestMeshPortfolioParentEnvelopesFanOut pins the reason an N+1 is a
// PERFORMANCE bug and not just a shape: the repeated children are
// sequential (each waits for the last), so the parent span stretches to
// cover the whole staircase. A parallel fan-out would not.
func TestMeshPortfolioParentEnvelopesFanOut(t *testing.T) {
	spec := chainByName(t, "MeshPortfolioValuation")
	tr := buildMeshTraceSeams(spec, neverFail, constReps(12))

	var kids []*tracepb.Span
	for _, si := range tr.spans {
		if si.service == "portfolio-service" && si.span.Name == "SELECT instrument_prices" {
			kids = append(kids, si.span)
		}
	}
	if len(kids) != 12 {
		t.Fatalf("got %d repeated spans, want 12 (the pinned seam)", len(kids))
	}
	starts := map[uint64]bool{}
	for _, k := range kids {
		starts[k.StartTimeUnixNano] = true
	}
	if len(starts) != len(kids) {
		t.Errorf("repeated spans share start offsets (%d distinct of %d) — "+
			"a lazy-load N+1 is SEQUENTIAL, not a fan-out", len(starts), len(kids))
	}

	var parent *tracepb.Span
	pid := string(kids[0].ParentSpanId)
	for _, si := range tr.spans {
		if string(si.span.SpanId) == pid {
			parent = si.span
		}
	}
	if parent == nil {
		t.Fatal("repeated spans' parent not in trace")
	}
	last := kids[len(kids)-1]
	if parent.EndTimeUnixNano < last.EndTimeUnixNano {
		t.Errorf("parent ends at %d before its last lazy load ends at %d — "+
			"the fan-out must inflate the caller", parent.EndTimeUnixNano, last.EndTimeUnixNano)
	}
}
