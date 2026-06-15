package api

import (
	"testing"
	"time"

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

// v0.8.x (anomaly → root-cause, release #1) — the anomaly-anchored fan-out
// maps AnomalyEvent.Kind to an exemplar trace kind, and that same error-ness
// gate decides whether dimension bubble-up runs (trace_op → error spans;
// log anomalies aren't a status subset, so bubble-up is skipped). Pin the
// mapping over EVERY recorder-emitted kind (recorder.go: log_pattern,
// log_template_new, trace_op) so a new kind can't silently fall into the
// wrong exemplar branch (CLAUDE.md #11).
func TestExemplarKindForAnomaly(t *testing.T) {
	tests := []struct {
		kind string
		want chstore.ExemplarKind
	}{
		{"trace_op", chstore.ExemplarError},     // error-ratio anomaly → erroring trace
		{"TRACE_OP", chstore.ExemplarError},     // case-insensitive
		{" trace_op ", chstore.ExemplarError},   // trimmed
		{"log_pattern", chstore.ExemplarAny},    // log anomaly → any representative trace
		{"log_template_new", chstore.ExemplarAny},
		{"", chstore.ExemplarAny},               // unknown / empty defaults to any
		{"future_kind", chstore.ExemplarAny},    // forward-compat default
	}
	for _, tt := range tests {
		if got := exemplarKindForAnomaly(tt.kind); got != tt.want {
			t.Errorf("exemplarKindForAnomaly(%q) = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

// v0.8.x (anomaly → root-cause, release #1) — the analysis window is derived
// from the anchor (Problem.StartedAt/ResolvedAt OR AnomalyEvent.StartedAt/
// LastSeen) and clamped to [10m, 1h]: ≥10m so a just-fired anchor has
// comparison context, ≤1h so the bubbleup/exemplar span scans stay bounded.
// `end` always moves relative to `started` (the window begins at the anchor's
// start), which also makes a LastSeen < StartedAt clock-skew row well-formed.
// Table-drives every branch so a regression in the clamp can't ship silently.
func TestBoundAnalysisWindow(t *testing.T) {
	base := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		started     time.Time
		end         time.Time
		wantStarted time.Time
		wantEnd     time.Time
	}{
		{
			name:        "sub-10m floored up to 10m from start",
			started:     base,
			end:         base.Add(3 * time.Minute),
			wantStarted: base,
			wantEnd:     base.Add(10 * time.Minute),
		},
		{
			name:        "exactly 10m unchanged",
			started:     base,
			end:         base.Add(10 * time.Minute),
			wantStarted: base,
			wantEnd:     base.Add(10 * time.Minute),
		},
		{
			name:        "in-range 30m unchanged",
			started:     base,
			end:         base.Add(30 * time.Minute),
			wantStarted: base,
			wantEnd:     base.Add(30 * time.Minute),
		},
		{
			name:        "exactly 1h unchanged",
			started:     base,
			end:         base.Add(time.Hour),
			wantStarted: base,
			wantEnd:     base.Add(time.Hour),
		},
		{
			name:        "over-1h capped to 1h from start",
			started:     base,
			end:         base.Add(6 * time.Hour),
			wantStarted: base,
			wantEnd:     base.Add(time.Hour),
		},
		{
			name:        "LastSeen before StartedAt (clock skew) floored to 10m",
			started:     base,
			end:         base.Add(-5 * time.Minute),
			wantStarted: base,
			wantEnd:     base.Add(10 * time.Minute),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStarted, gotEnd := boundAnalysisWindow(tt.started, tt.end)
			if !gotStarted.Equal(tt.wantStarted) {
				t.Errorf("started = %v, want %v", gotStarted, tt.wantStarted)
			}
			if !gotEnd.Equal(tt.wantEnd) {
				t.Errorf("end = %v, want %v", gotEnd, tt.wantEnd)
			}
		})
	}
}
