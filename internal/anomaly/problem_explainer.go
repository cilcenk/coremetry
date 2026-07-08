package anomaly

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// ProblemExplainer is a background goroutine that fills the
// AISummary column for newly-opened critical problems (v0.5.254).
// Operator opens /problems or /inbox 30s after a fire, sees a
// pre-baked "why fired + first checks" blurb instead of having
// to click "✨ Explain" themselves.
//
// Design notes:
//
//   - HA-gated via a per-tick Redis lock (same pattern as every
//     other Coremetry worker). Multiple replicas don't
//     duplicate-call the Copilot.
//   - Critical severity only by default — info / warning rules
//     are far more numerous and don't justify the AI cost.
//   - Per-tick cap (16 by default) so a thundering herd of
//     opens doesn't burn the Copilot quota in one minute.
//   - Surface = "problem-auto-explain" so the /ai page shows
//     a dedicated row for this background traffic alongside
//     operator-clicked Explain calls.
//   - Gated on AI Copilot Active() — true only when the Copilot
//     is BOTH configured (creds wired) AND enabled (the Settings
//     "Enable AI Copilot" toggle is on). wf: the operator can flip
//     the toggle OFF to stop this background loop from hammering
//     the provider (e.g. a TLS-proxied enterprise cluster spamming
//     "x509: negative serial number" against the GitHub token
//     endpoint) WITHOUT clearing the stored key. Re-enabling
//     resumes the loop on the next tick.
const explainerLockKey = "problem-explainer:lock"
const explainerBatch = 16

type ProblemExplainer struct {
	store    *chstore.Store
	copilot  *copilot.Service
	lock     cache.Lock
	leader   *cache.LeaderHolder // v0.5.429
	interval time.Duration
	batch    int
}

func NewProblemExplainer(store *chstore.Store, cop *copilot.Service, lock cache.Lock) *ProblemExplainer {
	interval := 30 * time.Second
	return &ProblemExplainer{
		store:    store,
		copilot:  cop,
		lock:     lock,
		leader:   cache.NewLeaderHolder(lock, explainerLockKey, cache.LeaderTTL(interval)),
		interval: interval,
		batch:    explainerBatch,
	}
}

// Start runs the explainer loop until ctx is cancelled. Initial
// tick fires immediately so a problem opened during pod startup
// gets explained without waiting a full interval.
func (e *ProblemExplainer) Start(ctx context.Context) {
	if e == nil || e.copilot == nil {
		return
	}
	e.leader.Start(ctx)
	t := time.NewTicker(e.interval)
	defer t.Stop()
	// Initial tick — also serves as a "Copilot config drift catch":
	// if the operator wires an API key mid-day, the first tick will
	// retroactively explain anything still missing a summary.
	e.tickIfLeader(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.tickIfLeader(ctx)
		}
	}
}

func (e *ProblemExplainer) tickIfLeader(ctx context.Context) {
	if !e.copilot.Active() {
		// No API key wired OR the operator disabled AI Copilot in
		// Settings — silently noop. THE fix for the disabled-but-
		// creds-stored case: stops this loop from calling the
		// provider (and spamming x509 errors) the moment the toggle
		// flips off, without waiting for creds to be cleared. wf.
		return
	}
	if !e.leader.IsLeader() {
		return
	}
	e.run(ctx)
}

// run grabs the candidate problems + calls the Copilot per row.
// Critical severity, status=open, ai_summary still empty. Skips
// resolved / acknowledged / info-warning rows — they're either
// already-actioned or low-value-to-explain.
func (e *ProblemExplainer) run(ctx context.Context) {
	problems, err := e.store.ListProblems(ctx, chstore.ProblemFilter{
		Status:   "open",
		Severity: "critical",
		Limit:    200,
	})
	if err != nil {
		log.Printf("[problem-explainer] list: %v", err)
		return
	}
	// Candidate pass first (empty-summary rows up to the batch cap) so an
	// all-cached tick still costs nothing extra — the evidence + hypothesis
	// reads below only fire when there is real work.
	candidates := make([]chstore.Problem, 0, e.batch)
	for _, p := range problems {
		if len(candidates) >= e.batch {
			break
		}
		if strings.TrimSpace(p.AISummary) != "" {
			continue
		}
		candidates = append(candidates, p)
	}
	if len(candidates) == 0 {
		return
	}
	// Phase 7 — fetch the cross-signal evidence inputs ONCE per tick (shared
	// across the batch).
	inputs := gatherEvidenceInputs(ctx, e.store)
	// v0.8.394 (AI audit A1) — batch-read the persisted root-cause hypotheses
	// the RootCauseSynthesizer already computed for these problems (ONE
	// GetHypotheses round-trip, same N+1-free join the /problems ribbon uses)
	// so the prompt can carry the deterministic verdict instead of narrating
	// blind. Best-effort: a read error just degrades to hypothesis-free
	// prompts (pre-fusion behaviour), never blocks the tick.
	ids := make([]string, 0, len(candidates))
	for i := range candidates {
		ids = append(ids, candidates[i].ID)
	}
	hyps, err := e.store.GetHypotheses(ctx, "problem", ids)
	if err != nil {
		log.Printf("[problem-explainer] hypotheses: %v", err)
		hyps = nil
	}
	filled := 0
	for _, p := range candidates {
		var hyp *chstore.RootCauseHypothesis
		if h, ok := hyps[p.ID]; ok {
			hh := h
			hyp = &hh
		}
		summary, err := e.explain(ctx, p, buildEvidenceBundle(p, inputs), hyp)
		if err != nil {
			log.Printf("[problem-explainer] %s: %v", p.ID, err)
			continue
		}
		if strings.TrimSpace(summary) == "" {
			continue
		}
		if err := e.store.UpsertProblemAISummary(ctx, p.ID, summary); err != nil {
			log.Printf("[problem-explainer] write %s: %v", p.ID, err)
			continue
		}
		filled++
	}
	if filled > 0 {
		log.Printf("[problem-explainer] filled %d summary/ies", filled)
	}
}

// explain runs one Problem's prompt through the Copilot with surface
// "problem-auto-explain". Same system prompt as the operator-clicked
// /api/copilot/explain-problem endpoint — keeps the AI's tone consistent
// across surfaces.
func (e *ProblemExplainer) explain(ctx context.Context, p chstore.Problem, bundle EvidenceBundle, hyp *chstore.RootCauseHypothesis) (string, error) {
	// Background context — surface = "problem-auto-explain" so the
	// /ai page can break out the auto-explain volume from operator-
	// clicked Explains.
	ctx = copilot.WithMeta(ctx, copilot.CallMeta{
		Surface:   "problem-auto-explain",
		UserID:    "system",
		UserEmail: "",
	})
	return e.copilot.Explain(ctx, copilot.SystemPromptProblem(), buildProblemPrompt(p, bundle, hyp))
}

// buildProblemPrompt composes the user prompt for one Problem — the bare rule
// facts, the persisted deterministic root-cause hypothesis (v0.8.394, AI audit
// A1) when one exists, then the Phase 7 corroborating evidence. PURE (no
// store, no copilot) so the fusion shape is table-testable
// (rootcause_prompt_test.go): hypothesis absent → the prompt is byte-identical
// to the pre-fusion shape.
func buildProblemPrompt(p chstore.Problem, bundle EvidenceBundle, hyp *chstore.RootCauseHypothesis) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Rule: %s\n", p.RuleName)
	fmt.Fprintf(&sb, "Service: %s\n", p.Service)
	fmt.Fprintf(&sb, "Severity: %s\n", p.Severity)
	fmt.Fprintf(&sb, "Metric: %s\n", p.Metric)
	fmt.Fprintf(&sb, "Value: %.4g (threshold %.4g)\n", p.Value, p.Threshold)
	fmt.Fprintf(&sb, "Started: %s\n", time.Unix(0, p.StartedAt).UTC().Format(time.RFC3339))
	if p.Description != "" {
		fmt.Fprintf(&sb, "Description: %s\n", p.Description)
	}
	// v0.8.394 (AI audit A1) — the deterministic verdict FIRST: the system
	// prompt instructs the model to trust this block as primary evidence and
	// narrate/extend it rather than re-guess the suspect. Renders "" when the
	// synthesizer hasn't produced a clear-suspect hypothesis yet.
	sb.WriteString(HypothesisPromptBlockTR(hyp))
	// Phase 7 — cross-signal fusion: hand the Copilot the corroborating
	// evidence so the summary reads as one incident with a likely root cause,
	// not an isolated metric blip. Empty bundle → nothing added (unchanged).
	renderEvidence(&sb, bundle)
	return sb.String()
}
