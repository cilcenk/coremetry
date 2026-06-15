package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// Correlated-Signals real metric exemplar (wf, this change) — the metric anchor
// is no longer the over-conservative "fuzzy (service+window)" pivot. For latency
// + error the drawer pivots into a REAL representative exemplar trace (slow /
// error trace from the spanmetrics rollup, raw-span fallback), so:
//   • metricKind maps to the right exemplar flavour (latency→slow, error→error,
//     throughput→any), and
//   • the anchor JoinKey is "exemplar" for latency/error (a real trace) but
//     stays the honest weaker "service+window" for throughput (no single span
//     "is" a count/rate).
//
// This pins both the metricKind→exemplar-kind mapping AND the JoinKey labeling.
// If a future refactor regresses either — e.g. labels throughput "exemplar"
// (over-claiming an exact pivot the differentiator can't back) or maps latency
// to "any" (the wrong representative trace) — these fail. (CLAUDE.md #11.)

func TestCorrelateExemplarKind(t *testing.T) {
	tests := []struct {
		metricKind string
		want       chstore.ExemplarKind
	}{
		{"latency", chstore.ExemplarSlow},
		{"error", chstore.ExemplarError},
		{"throughput", chstore.ExemplarAny},
		{"LATENCY", chstore.ExemplarSlow},  // case-insensitive
		{"Error", chstore.ExemplarError},   // case-insensitive
		{" latency ", chstore.ExemplarSlow}, // trimmed
		{"", chstore.ExemplarAny},          // unknown/empty → any (weak)
		{"count", chstore.ExemplarAny},     // any other label → any
	}
	for _, tt := range tests {
		if got := correlateExemplarKind(tt.metricKind); got != tt.want {
			t.Errorf("correlateExemplarKind(%q) = %q, want %q", tt.metricKind, got, tt.want)
		}
	}
}

func TestJoinKeyFor(t *testing.T) {
	tests := []struct {
		name       string
		traceID    string
		kind       CorrelationKind
		metricKind string
		want       string
	}{
		// A trace_id always wins — exact join, regardless of anchor kind.
		{"trace anchor with id", "c9ea", CorrelateTrace, "", joinTraceID},
		{"metric anchor that somehow has a trace_id", "c9ea", CorrelateMetric, "latency", joinTraceID},
		{"log anchor with id", "c9ea", CorrelateLog, "", joinTraceID},

		// Metric anchor, no trace_id: latency/error → real exemplar.
		{"metric latency", "", CorrelateMetric, "latency", joinExemplar},
		{"metric error", "", CorrelateMetric, "error", joinExemplar},
		{"metric latency uppercase", "", CorrelateMetric, "LATENCY", joinExemplar},

		// Metric anchor, throughput → genuinely fuzzy (no representative span).
		{"metric throughput", "", CorrelateMetric, "throughput", joinServiceWindow},
		{"metric empty kind", "", CorrelateMetric, "", joinServiceWindow},
		{"metric unknown kind", "", CorrelateMetric, "count", joinServiceWindow},

		// Non-metric anchor without a trace_id → service+window fallback.
		{"log anchor no id", "", CorrelateLog, "", joinServiceWindow},
	}
	for _, tt := range tests {
		if got := joinKeyFor(tt.traceID, tt.kind, tt.metricKind); got != tt.want {
			t.Errorf("%s: joinKeyFor(%q, %q, %q) = %q, want %q",
				tt.name, tt.traceID, tt.kind, tt.metricKind, got, tt.want)
		}
	}
}

// metricHasExemplar is the boolean joinKeyFor leans on; pin it directly so the
// "which metric kinds carry a real representative trace" contract is explicit.
func TestMetricHasExemplar(t *testing.T) {
	tests := []struct {
		metricKind string
		want       bool
	}{
		{"latency", true},
		{"error", true},
		{"throughput", false},
		{"", false},
		{"count", false},
	}
	for _, tt := range tests {
		if got := metricHasExemplar(tt.metricKind); got != tt.want {
			t.Errorf("metricHasExemplar(%q) = %v, want %v", tt.metricKind, got, tt.want)
		}
	}
}
