package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.7.51 — the root-cause bundle maps a problem's breached metric to the
// exemplar trace kind, and that same error-ness gate decides whether dimension
// bubble-up runs (selection = error spans). Pin the mapping (CLAUDE.md #11).
func TestExemplarKindForMetric(t *testing.T) {
	tests := []struct {
		metric string
		want   chstore.ExemplarKind
	}{
		{"error_rate", chstore.ExemplarError},
		{"errors", chstore.ExemplarError},
		{"5xx_error_pct", chstore.ExemplarError},
		{"ERROR_RATE", chstore.ExemplarError}, // case-insensitive
		{"p99_ms", chstore.ExemplarSlow},
		{"p95_latency", chstore.ExemplarSlow},
		{"duration_ms", chstore.ExemplarSlow},
		{"latency", chstore.ExemplarSlow},
		{"request_rate", chstore.ExemplarAny},
		{"throughput", chstore.ExemplarAny},
		{"", chstore.ExemplarAny},
	}
	for _, tt := range tests {
		if got := exemplarKindForMetric(tt.metric); got != tt.want {
			t.Errorf("exemplarKindForMetric(%q) = %q, want %q", tt.metric, got, tt.want)
		}
	}
}
