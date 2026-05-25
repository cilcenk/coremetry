// Package api handler for SLO autocreate (v0.5.147). Scans
// recent telemetry, picks the busiest N services, and stamps a
// baseline-grounded availability + latency SLO for each one the
// install doesn't already have. Admin-only, audit-logged, idempotent.
package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// autoSLOSuggestion is one (service, kind) pair the autocreate
// pass either created or would create in dry-run mode. The UI
// renders this as a preview row before the operator confirms.
type autoSLOSuggestion struct {
	Service     string  `json:"service"`
	SLIType     string  `json:"sliType"`
	Target      float64 `json:"target"`
	ThresholdMs float64 `json:"thresholdMs,omitempty"`
	WindowDays  uint16  `json:"windowDays"`
	BaselineSLI float64 `json:"baselineSli,omitempty"` // measured availability — 0..1
	BaselineMs  float64 `json:"baselineMs,omitempty"`  // measured p99
	Reason      string  `json:"reason"`
	Created     bool    `json:"created"`
	Skipped     string  `json:"skipped,omitempty"` // populated when we left an existing SLO alone
}

// autoSLO scans the last 7d of spans to derive a baseline per
// service, then proposes (and optionally writes) an availability +
// latency SLO for each. Heuristics:
//
//   • Availability target = round_down(measured_sli - 0.5%) to the
//     nearest 0.05% boundary, but never below 99% and never above
//     99.95%. Captures "current real reliability minus a small buffer"
//     so the SLO isn't already breached on day one.
//
//   • Latency threshold = measured_p99 × 1.5, rounded up to the
//     nearest 50 ms band. Generous enough that a normal day passes;
//     tight enough that real regressions trip it.
//
//   • Rolling window = 30 days — the bank-style default.
//
//   • Skip rule: never overwrite an existing SLO for the same
//     (service, sliType). If the operator wants a tighter SLO,
//     they edit the existing one — auto-create is for stamping
//     defaults, not for tuning.
//
// dry_run=1 returns suggestions without writing; default writes
// each non-skipped suggestion and audits one row per operator click.
func (s *Server) autoCreateSLOs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	dryRun := q.Get("dry_run") == "1"
	limit := parseInt(q.Get("limit"), 30)
	if limit > 200 {
		limit = 200
	}

	// Snapshot the existing (service, sliType) set so we can skip
	// duplicates in one pass.
	existing := map[string]bool{}
	cur, err := s.store.ListSLOs(r.Context())
	if err == nil {
		for _, o := range cur {
			existing[o.Service+"|"+o.SLIType] = true
		}
	}

	// Pull top-N services by recent traffic from the existing
	// services endpoint. 7-day window gives enough signal even
	// for low-traffic services that fire once a day. Uses the
	// pre-aggregated service summary MV under the hood — cheap.
	svcs, err := s.store.GetServices(r.Context(), 7*24*time.Hour, time.Time{}, time.Time{})
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(svcs) == 0 {
		writeJSON(w, map[string]any{"suggestions": []autoSLOSuggestion{}})
		return
	}
	if len(svcs) > limit {
		svcs = svcs[:limit]
	}

	out := make([]autoSLOSuggestion, 0, 2*len(svcs))
	for _, svc := range svcs {
		// Availability suggestion
		if !existing[svc.Name+"|"+chstore.SLITypeAvailability] && svc.SpanCount > 0 {
			availSLI := math.Max(0, math.Min(1, 1-svc.ErrorRate))
			target := availabilityTarget(availSLI)
			sg := autoSLOSuggestion{
				Service: svc.Name, SLIType: chstore.SLITypeAvailability,
				Target: target, WindowDays: 30,
				BaselineSLI: availSLI,
				Reason: fmt.Sprintf("Measured %.3f%% over 7d → target %.2f%% with buffer",
					availSLI*100, target*100),
			}
			out = append(out, sg)
		} else if existing[svc.Name+"|"+chstore.SLITypeAvailability] {
			out = append(out, autoSLOSuggestion{
				Service: svc.Name, SLIType: chstore.SLITypeAvailability,
				Skipped: "existing SLO not overwritten",
			})
		}
		// Latency suggestion
		if !existing[svc.Name+"|"+chstore.SLITypeLatency] && svc.P99Ms > 0 {
			threshold := latencyThreshold(svc.P99Ms)
			sg := autoSLOSuggestion{
				Service: svc.Name, SLIType: chstore.SLITypeLatency,
				Target: 0.99, WindowDays: 30,
				ThresholdMs: threshold,
				BaselineMs:  svc.P99Ms,
				Reason: fmt.Sprintf("Measured p99 %.1fms over 7d → threshold %.0fms (≈1.5×, rounded to 50ms band)",
					svc.P99Ms, threshold),
			}
			out = append(out, sg)
		} else if existing[svc.Name+"|"+chstore.SLITypeLatency] {
			out = append(out, autoSLOSuggestion{
				Service: svc.Name, SLIType: chstore.SLITypeLatency,
				Skipped: "existing SLO not overwritten",
			})
		}
	}

	if !dryRun {
		for i := range out {
			sg := &out[i]
			if sg.Skipped != "" {
				continue
			}
			id := newID(8)
			o := chstore.SLO{
				ID: id, Name: defaultSLOName(sg.Service, sg.SLIType),
				Service: sg.Service, SLIType: sg.SLIType, Target: sg.Target,
				WindowDays: sg.WindowDays, ThresholdMs: sg.ThresholdMs,
				CreatedAt: time.Now().UnixNano(),
			}
			if err := s.store.UpsertSLO(r.Context(), o); err != nil {
				continue
			}
			sg.Created = true
		}
		s.audit(r, "slo.autocreate", "slo", "",
			fmt.Sprintf(`{"suggestions":%d,"created":%d,"dryRun":false}`,
				len(out), countCreated(out)))
	}
	writeJSON(w, map[string]any{
		"suggestions": out,
		"dryRun":      dryRun,
	})
}

// availabilityTarget rounds the measured SLI down to a clean
// percent value, with a 0.5% buffer so the new SLO isn't already
// in the red on day one. Floor 99%, ceiling 99.95% — beyond that
// the math gets brittle for low-traffic services.
func availabilityTarget(sli float64) float64 {
	if sli <= 0 {
		return 0.99
	}
	withBuffer := sli - 0.005
	// Round down to nearest 0.05% (0.0005) so values like
	// 0.99876 → 0.998.
	target := math.Floor(withBuffer*2000) / 2000
	if target < 0.99 {
		target = 0.99
	}
	if target > 0.9995 {
		target = 0.9995
	}
	return target
}

// latencyThreshold rounds (p99 × 1.5) UP to a 50-ms boundary so
// the SLO threshold reads cleanly on a dashboard (e.g. 450ms not
// 437.2ms). Floor 100ms so a 1ms-p99 internal service still gets
// a non-trivial budget.
func latencyThreshold(p99 float64) float64 {
	base := p99 * 1.5
	rounded := math.Ceil(base/50) * 50
	if rounded < 100 {
		rounded = 100
	}
	return rounded
}

// defaultSLOName produces a human-readable rule name that the
// operator can edit later. Mirrors the manual-create form's
// "Service availability" / "Service latency" convention so the
// /slos list reads uniformly across auto + hand-written rules.
func defaultSLOName(service, sliType string) string {
	switch sliType {
	case chstore.SLITypeAvailability:
		return service + " availability"
	case chstore.SLITypeLatency:
		return service + " latency"
	}
	return service + " " + sliType
}

func countCreated(s []autoSLOSuggestion) int {
	n := 0
	for _, sg := range s {
		if sg.Created {
			n++
		}
	}
	return n
}

// Anchor unused imports for golangci-lint; keeps the auth +
// context aliases honest if future edits drop the references.
var _ = strings.TrimSpace
var _ context.Context
