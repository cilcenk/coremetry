package anomaly

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// Phase 7 — cross-signal fusion. The three metric detectors, trace_ops and
// log_patterns each fire independently and write their own row. When several
// fire for the SAME service in the SAME window that's not N unrelated problems
// — it's ONE incident with mutually-corroborating evidence. Fusion collects
// every open signal around a Problem so the explainer can hand the Copilot a
// corroborated picture (co-firing problems, active log/trace anomalies + a
// sample, a deploy that lines up in time, and neighbouring services that are
// also unhealthy) instead of the bare Problem row.
//
// Evidence is gathered at explain time from already-recorded store state
// (no schema migration, no detector re-run); an empty bundle leaves the
// explainer's prior behaviour unchanged (backward-compatible).

const (
	// evidenceDeployLookback — a deploy of the same service this long before
	// the anomaly onset is treated as the prime "what changed" suspect.
	evidenceDeployLookback = 30 * time.Minute
	// evidenceWindow — how far back recorded signals/edges count as part of
	// the same incident.
	evidenceWindow = 60 * time.Minute
	// maxEvidenceItems caps each rendered list so the Copilot user-context
	// stays inside the token budget.
	maxEvidenceItems = 5
)

// EvidenceBundle is the corroborating signal set for one triggering Problem.
type EvidenceBundle struct {
	Problem    chstore.Problem            // the triggering problem
	CoFiring   []chstore.Problem          // other OPEN problems on the SAME service
	Signals    []chstore.AnomalyEvent     // active log_pattern / trace_op anomalies on the service
	Deploy     *chstore.RecentDeployEntry // a deploy of the service just before onset
	Neighbors  []NeighborProblem          // open problems on direct topology neighbours
	Confidence int                        // distinct corroborating evidence types present (incl. the trigger)
}

// NeighborProblem is an open problem on a service adjacent to the trigger,
// annotated with the call direction relative to the triggering service.
type NeighborProblem struct {
	Problem   chstore.Problem
	Direction string // "calls" (trigger → neighbour) | "called_by" (neighbour → trigger)
}

// evidenceInputs is the store-side state fused for an incident. Fetched ONCE
// per explainer tick (shared across the batch) so a tick costs four bounded
// reads, not four per problem.
type evidenceInputs struct {
	openProblems []chstore.Problem
	events       []chstore.AnomalyEvent
	deploys      []chstore.RecentDeployEntry
	adjacency    []chstore.ServiceEdgePair
}

// gatherEvidenceInputs reads the four evidence sources. Best-effort: any read
// error degrades to a smaller bundle rather than failing the explain.
func gatherEvidenceInputs(ctx context.Context, store *chstore.Store) evidenceInputs {
	var in evidenceInputs
	if ps, err := store.ListProblems(ctx, chstore.ProblemFilter{Status: "open", Limit: 500}); err == nil {
		in.openProblems = ps
	}
	if ev, err := store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{
		SinceNs: time.Now().Add(-evidenceWindow).UnixNano(),
		Limit:   500,
	}); err == nil {
		in.events = ev
	}
	if d, err := store.GetRecentDeploys(ctx, evidenceDeployLookback, 200); err == nil {
		in.deploys = d
	}
	if a, err := store.GetServiceAdjacency(ctx, evidenceWindow); err == nil {
		in.adjacency = a
	}
	return in
}

// buildEvidenceBundle is the PURE assembly of the bundle from already-fetched
// inputs — store-free so the fusion grouping + confidence scoring is unit-
// testable without a ClickHouse-backed store.
func buildEvidenceBundle(p chstore.Problem, in evidenceInputs) EvidenceBundle {
	b := EvidenceBundle{Problem: p}

	// Direct topology neighbours of the triggering service + the direction.
	dir := map[string]string{}
	for _, e := range in.adjacency {
		if e.Caller == p.Service && e.Callee != p.Service {
			dir[e.Callee] = "calls"
		}
		if e.Callee == p.Service && e.Caller != p.Service {
			if _, ok := dir[e.Caller]; !ok {
				dir[e.Caller] = "called_by"
			}
		}
	}

	for _, op := range in.openProblems {
		if op.ID == p.ID {
			continue
		}
		switch {
		case op.Service == p.Service:
			b.CoFiring = append(b.CoFiring, op)
		case dir[op.Service] != "":
			b.Neighbors = append(b.Neighbors, NeighborProblem{Problem: op, Direction: dir[op.Service]})
		}
	}

	for _, ev := range in.events {
		if ev.Service == p.Service && ev.Status == "active" {
			b.Signals = append(b.Signals, ev)
		}
	}

	// Deploy of THIS service whose first-seen lands in the lookback window
	// before onset — the most recent such is the prime "what changed".
	lo := p.StartedAt - evidenceDeployLookback.Nanoseconds()
	for i := range in.deploys {
		d := &in.deploys[i]
		if d.Service != p.Service || d.FirstSeenNs < lo || d.FirstSeenNs > p.StartedAt {
			continue
		}
		if b.Deploy == nil || d.FirstSeenNs > b.Deploy.FirstSeenNs {
			b.Deploy = d
		}
	}

	// Confidence = number of distinct corroborating evidence TYPES present,
	// including the triggering problem itself. n>1 → a corroborated incident.
	b.Confidence = 1
	if len(b.CoFiring) > 0 {
		b.Confidence++
	}
	if len(b.Signals) > 0 {
		b.Confidence++
	}
	if b.Deploy != nil {
		b.Confidence++
	}
	if len(b.Neighbors) > 0 {
		b.Confidence++
	}
	return b
}

// renderEvidence appends a compact, token-bounded evidence section to the
// Copilot user-context. A bundle with no corroboration (confidence 1) appends
// nothing, so the explainer's behaviour is unchanged when nothing lines up.
func renderEvidence(sb *strings.Builder, b EvidenceBundle) {
	if b.Confidence <= 1 {
		return
	}
	fmt.Fprintf(sb, "\nCorrelated evidence (confidence %d/5 — likely ONE incident):\n", b.Confidence)
	if b.Deploy != nil {
		mins := (b.Problem.StartedAt - b.Deploy.FirstSeenNs) / int64(time.Minute)
		fmt.Fprintf(sb, "- DEPLOY (prime 'what changed' suspect): %s %s deployed %dm before onset\n",
			b.Deploy.Service, b.Deploy.Version, mins)
	}
	if n := len(b.CoFiring); n > 0 {
		parts := make([]string, 0, n)
		for i, cp := range b.CoFiring {
			if i >= maxEvidenceItems {
				parts = append(parts, fmt.Sprintf("+%d more", n-maxEvidenceItems))
				break
			}
			parts = append(parts, fmt.Sprintf("%s (%s)", cp.RuleName, cp.Metric))
		}
		fmt.Fprintf(sb, "- Same-service problems also open (%d): %s\n", n, strings.Join(parts, "; "))
	}
	if n := len(b.Signals); n > 0 {
		fmt.Fprintf(sb, "- Co-firing log/trace anomalies on this service (%d):\n", n)
		for i, ev := range b.Signals {
			if i >= maxEvidenceItems {
				fmt.Fprintf(sb, "  …and %d more\n", n-maxEvidenceItems)
				break
			}
			line := fmt.Sprintf("  · %s: %s", ev.Kind, ev.Pattern)
			if s := strings.TrimSpace(ev.Sample); s != "" {
				if len(s) > 120 {
					s = s[:120] + "…"
				}
				line += " — " + s
			}
			fmt.Fprintln(sb, line)
		}
	}
	if n := len(b.Neighbors); n > 0 {
		parts := make([]string, 0, n)
		for i, np := range b.Neighbors {
			if i >= maxEvidenceItems {
				parts = append(parts, fmt.Sprintf("+%d more", n-maxEvidenceItems))
				break
			}
			parts = append(parts, fmt.Sprintf("%s (%s, %s)", np.Problem.Service, np.Direction, np.Problem.RuleName))
		}
		fmt.Fprintf(sb, "- Unhealthy topology neighbours (%d): %s\n", n, strings.Join(parts, "; "))
	}
}
