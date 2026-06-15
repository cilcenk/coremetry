package api

import (
	"fmt"
	"strings"
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

// v0.8.x rc #4 — the Copilot prose narration flattens a persisted
// RootCauseHypothesis into the user prompt the model narrates. buildRootCausePrompt
// is the pure boundary the regression test pins: it must (a) render the no-suspect
// branch honestly (never imply a cause the ranking didn't find), (b) include the
// recent deploy line with a human age, (c) carry every ranked candidate's score +
// hop + Reason so the model narrates the SAME evidence the ribbon shows, and
// (d) cap the candidate list so a pathological fan-out can't balloon the prompt.
// A regression in any of these silently degrades or misleads the narration.
func TestBuildRootCausePrompt(t *testing.T) {
	t.Run("no suspect — honest empty ranking", func(t *testing.T) {
		got := buildRootCausePrompt(&chstore.RootCauseHypothesis{
			AnchorKind: "anomaly", Service: "checkout", TopSuspect: "",
			TopScore: 0, Confidence: 0,
		})
		if !strings.Contains(got, "Top suspect: (none") {
			t.Errorf("no-suspect branch missing honest (none) marker:\n%s", got)
		}
		if !strings.Contains(got, "Ranked candidates: (none)") {
			t.Errorf("empty candidate list not rendered honestly:\n%s", got)
		}
		if !strings.Contains(got, "Confidence: 0%") {
			t.Errorf("confidence not rendered:\n%s", got)
		}
	})

	t.Run("deploy-led — deploy line with human age", func(t *testing.T) {
		got := buildRootCausePrompt(&chstore.RootCauseHypothesis{
			AnchorKind: "anomaly", Service: "payment-svc",
			TopSuspect: "payment-svc", TopScore: 78.0, Confidence: 0.78,
			RecentDeploy: &chstore.RecentDeploy{Version: "v42", AgeSeconds: 240},
			Candidates: []chstore.ScoredCause{
				{Service: "payment-svc", Score: 78.0, Hops: 0, Reason: "fresh deploy 4m before onset"},
			},
		})
		if !strings.Contains(got, "Top suspect: payment-svc (score 78.0)") {
			t.Errorf("top suspect line wrong:\n%s", got)
		}
		if !strings.Contains(got, "Recent deploy: service.version=v42 first seen 4m before onset") {
			t.Errorf("deploy line / age wrong:\n%s", got)
		}
		if !strings.Contains(got, "fresh deploy 4m before onset") {
			t.Errorf("candidate Reason dropped — model would narrate without evidence:\n%s", got)
		}
		if !strings.Contains(got, "Confidence: 78%") {
			t.Errorf("confidence not rendered as percent:\n%s", got)
		}
	})

	t.Run("propagation-led — hop + reason carried per candidate", func(t *testing.T) {
		got := buildRootCausePrompt(&chstore.RootCauseHypothesis{
			AnchorKind: "problem", Service: "checkout",
			TopSuspect: "oracle-core", TopScore: 0.62, Confidence: 0.41,
			Candidates: []chstore.ScoredCause{
				{Service: "oracle-core", Score: 0.62, Hops: 1, Reason: "downstream error-share 0.62"},
				{Service: "kafka", Score: 0.20, Hops: 2, Reason: "co-firing problem"},
			},
		})
		if !strings.Contains(got, "1. oracle-core (score 0.6, 1 hop(s)) — downstream error-share 0.62") {
			t.Errorf("propagation candidate line wrong:\n%s", got)
		}
		if !strings.Contains(got, "2. kafka (score 0.2, 2 hop(s)) — co-firing problem") {
			t.Errorf("second candidate dropped or mangled:\n%s", got)
		}
	})

	t.Run("candidate list capped at 8", func(t *testing.T) {
		cands := make([]chstore.ScoredCause, 20)
		for i := range cands {
			cands[i] = chstore.ScoredCause{Service: fmt.Sprintf("svc-%02d", i), Score: float64(20 - i)}
		}
		got := buildRootCausePrompt(&chstore.RootCauseHypothesis{
			AnchorKind: "anomaly", Service: "x", TopSuspect: "svc-00",
			Candidates: cands,
		})
		if !strings.Contains(got, "8. svc-07") {
			t.Errorf("8th candidate should render:\n%s", got)
		}
		if strings.Contains(got, "9. svc-08") || strings.Contains(got, "svc-19") {
			t.Errorf("candidate list exceeded the cap of 8:\n%s", got)
		}
	})

	t.Run("empty reason falls back, never a bare dash", func(t *testing.T) {
		got := buildRootCausePrompt(&chstore.RootCauseHypothesis{
			AnchorKind: "anomaly", Service: "x", TopSuspect: "y",
			Candidates: []chstore.ScoredCause{{Service: "y", Score: 1.0, Reason: "   "}},
		})
		if !strings.Contains(got, "— no reason recorded") {
			t.Errorf("blank reason should fall back to a placeholder:\n%s", got)
		}
	})
}

// v0.8.x rc #4 — fmtDeployAge mirrors the frontend fmtAgo so the prose age
// phrasing matches the ribbon. Table-driven over every magnitude branch
// (sec/min/hour/day) — the boundary values are where an off-by-one in the
// divisor would silently shift the age the model reports.
func TestFmtDeployAge(t *testing.T) {
	cases := []struct {
		sec  int64
		want string
	}{
		{0, "0s"},
		{59, "59s"},
		{60, "1m"},
		{3599, "59m"},
		{3600, "1h"},
		{86399, "23h"},
		{86400, "1d"},
		{259200, "3d"},
	}
	for _, c := range cases {
		if got := fmtDeployAge(c.sec); got != c.want {
			t.Errorf("fmtDeployAge(%d) = %q, want %q", c.sec, got, c.want)
		}
	}
}
