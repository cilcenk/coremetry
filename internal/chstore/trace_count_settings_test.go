package chstore

import (
	"strings"
	"testing"
)

// v0.9.284 — the "approx" trace count carried only max_execution_time
// while the LIST query it counts carried tracesSpillSettings (512 MiB
// external group-by + sort). The asymmetry is the wrong way round: the
// count wraps the SAME window-wide `GROUP BY trace_id`, so it built the
// same multi-GB aggregation state with no spill escape and returned
// code 241 for a page the list would have rendered. /explore pinned
// count=approx on every traces query (Explore.tsx), so on that surface
// it was not an edge case — it was every search.
//
// The invariant this pins: NO trace-count branch may be less
// memory-safe than the list. Table-driven over both shapes.
func TestTraceCountSettingsAlwaysSpill(t *testing.T) {
	cases := []struct {
		name   string
		useHLL bool
	}{
		{"approx / narrow window — no DISTINCT to swap", false},
		{"exact / wide window — HyperLogLog", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := traceCountSettings(tc.useHLL)

			if !strings.Contains(got, tracesSpillSettings) {
				t.Fatalf("count settings must carry the list's spill guard, got %q", got)
			}
			if !strings.Contains(got, "max_execution_time") {
				t.Fatalf("count settings must carry a server cap, got %q", got)
			}
			if !strings.HasPrefix(got, "SETTINGS ") {
				t.Fatalf("settings clause must be appendable to SQL, got %q", got)
			}
			// One SETTINGS keyword only — two would be a syntax error the
			// moment this string is concatenated onto a query.
			if n := strings.Count(got, "SETTINGS"); n != 1 {
				t.Fatalf("expected exactly one SETTINGS keyword, got %d in %q", n, got)
			}
		})
	}
}

// v0.9.284 — which count modes leave the trace_summary_5m fast path
// open. /explore pinned "approx" to render a footer label and thereby
// held this gate shut on every one of its traces searches, so the three
// /traces MV fixes (v0.9.276-278) delivered nothing there. The gate is
// pinned here so a UI label can never close it again.
func TestCountModeAllowsMV(t *testing.T) {
	cases := map[string]bool{
		"skip":   true,  // the /traces default
		"":       true,  // back-compat default, same meaning
		"approx": false, // window-wide GROUP BY beside the list
		"exact":  false, // count(DISTINCT) / HLL over the window
		"SKIP":   false, // modes are compared verbatim, never folded
		"none":   false, // unknown mode must not open the fast path
	}
	for mode, want := range cases {
		if got := countModeAllowsMV(mode); got != want {
			t.Errorf("countModeAllowsMV(%q) = %v, want %v", mode, got, want)
		}
	}
}

// The HLL swap is the ONLY difference between the two shapes — a future
// edit that makes the wide-window branch diverge further (a different
// cap, a dropped guard) shows up here.
func TestTraceCountSettingsHLLIsTheOnlyDifference(t *testing.T) {
	narrow := traceCountSettings(false)
	wide := traceCountSettings(true)

	if strings.Contains(narrow, "count_distinct_implementation") {
		t.Fatalf("the LIMIT-wrapped approx shape has no DISTINCT to reimplement, got %q", narrow)
	}
	if !strings.Contains(wide, "count_distinct_implementation = 'uniq'") {
		t.Fatalf("wide windows must swap count(DISTINCT) for HyperLogLog, got %q", wide)
	}
	if strings.Replace(wide, "count_distinct_implementation = 'uniq', ", "", 1) != narrow {
		t.Fatalf("the two shapes diverge by more than the HLL swap:\n narrow = %q\n wide   = %q", narrow, wide)
	}
}
