package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.9.275 — every DISTINCT over metric_points must be bounded.
//
// MetricLabelValues shipped with none of the three bounds its sibling
// MetricAttrKeys carries ten lines above it. The LIMIT it did have bounds
// nothing that matters: ClickHouse computes the DISTINCT over the whole window
// BEFORE applying LIMIT, so cost scales with the window, not with the answer.
// On a 1000-service install a shared metric (jvm.memory.used) means a multi-GB
// Array(String) read.
//
// The guard scans for the SHAPE rather than pinning one function, so the next
// label-suggestion endpoint cannot repeat it. It reads a window of source AFTER
// each `SELECT DISTINCT`, because these queries are built two different ways —
// one backtick literal (MetricLabelValues) and one string concatenation
// (MetricAttrKeys) — and a guard that only understood one of them would have
// missed the very function that was broken.
func TestMetricPointsDistinctReadsAreBounded(t *testing.T) {
	src, err := os.ReadFile("metricquery.go")
	if err != nil {
		t.Fatalf("read metricquery.go: %v", err)
	}
	s := string(src)

	const window = 700 // enough to span the longest concatenated builder here
	sites := 0
	for i := 0; ; {
		j := strings.Index(s[i:], "SELECT DISTINCT")
		if j < 0 {
			break
		}
		start := i + j
		end := start + window
		if end > len(s) {
			end = len(s)
		}
		body := s[start:end]
		i = start + len("SELECT DISTINCT")

		if !strings.Contains(body, "metric_points") {
			continue // a DISTINCT over some other table is not this guard's business
		}
		sites++
		// Name the enclosing function for a readable failure.
		fn := "?"
		if k := strings.LastIndex(s[:start], "func (s *Store) "); k >= 0 {
			rest := s[k+len("func (s *Store) "):]
			if p := strings.IndexByte(rest, '('); p > 0 {
				fn = rest[:p]
			}
		}

		for _, want := range []struct{ frag, why string }{
			{"max_execution_time", "LIMIT does NOT bound a DISTINCT — it is computed over the whole window first"},
			{"time >= ?", "no lower time bound"},
			{"time <= ?", "no upper bound; prunes nothing today, but the siblings must read the same"},
			{"LIMIT", "no row cap on the returned set"},
		} {
			if !strings.Contains(body, want.frag) {
				t.Errorf("%s: unbounded DISTINCT over metric_points — missing %s\n  %s",
					fn, want.frag, want.why)
			}
		}
	}

	if sites < 2 {
		t.Fatalf("expected at least 2 DISTINCT metric_points sites, found %d — the shape moved "+
			"and this guard is no longer aimed at anything", sites)
	}
}
