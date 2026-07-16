package anomaly

import (
	"context"
	"log"
	"math"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/correlator"
)

// RootCauseSynthesizer is the leader-gated background worker that pre-computes
// + persists a root-cause hypothesis per anchor (rc #2 of the anomaly →
// root-cause feature). Same shape as ProblemExplainer: a per-tick Redis-leader
// lock, a bounded batch, one shared evidence fetch per tick. For each candidate
// anchor (a recent/active AnomalyEvent OR a high-severity open Problem) it
// builds the SAME cross-signal EvidenceBundle the explainer + the on-demand
// /rootcause fan-out use, distils it into correlator.SynthesisInput, calls the
// PURE correlator.Synthesize fuser, and upserts the ranked hypothesis. The
// /anomalies + /problems ribbon (rc #3) then reads the persisted row with no
// per-request synthesis.
//
// Bounded by construction: gatherEvidenceInputs runs ONCE per tick (four
// already-bounded reads, shared across the batch — NO new unbounded CH query);
// the anchor lists are capped (top-N by severity / recency); the fuser is pure.
// Leader-gated so multi-pod runs don't duplicate-write. Worker/all modes only
// (wired next to the explainer in main.go) — ingest/api pods don't run it.
const synthesizerLockKey = "rootcause-synthesizer:lock"

const (
	// synthesizerBatch caps how many anchors one tick synthesizes — like the
	// explainer's critical-only batch, this keeps a thundering herd of opens
	// from blowing the tick's CH budget. Higher than the explainer's 16
	// because Synthesize is pure (no LLM round-trip) — the only cost is the
	// shared evidence fetch + the per-anchor upsert.
	synthesizerBatch = 64
	// synthesizerRecentAnomaly — only anomalies whose last_seen is within this
	// window are worth a hypothesis; older ones have cleared and nobody is
	// triaging them.
	synthesizerRecentAnomaly = 30 * time.Minute
)

type RootCauseSynthesizer struct {
	store    *chstore.Store
	lock     cache.Lock
	leader   *cache.LeaderHolder
	interval time.Duration
	batch    int
}

func NewRootCauseSynthesizer(store *chstore.Store, lock cache.Lock) *RootCauseSynthesizer {
	interval := 30 * time.Second
	return &RootCauseSynthesizer{
		store:    store,
		lock:     lock,
		leader:   cache.NewLeaderHolder(lock, synthesizerLockKey, cache.LeaderTTL(interval)),
		interval: interval,
		batch:    synthesizerBatch,
	}
}

// Start runs the synthesis loop until ctx is cancelled. Initial tick fires
// immediately so an anchor that opened during pod startup gets a hypothesis
// without waiting a full interval (same as the explainer).
func (s *RootCauseSynthesizer) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.leader.Start(ctx)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	s.tickIfLeader(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tickIfLeader(ctx)
		}
	}
}

func (s *RootCauseSynthesizer) tickIfLeader(ctx context.Context) {
	if !s.leader.IsLeader() {
		return
	}
	s.run(ctx)
}

// run gathers the shared evidence once, then synthesizes + upserts a hypothesis
// for each candidate anchor up to the batch cap. Anomalies first (the feature's
// primary anchor), then high-severity open problems, sharing the SAME evidence
// inputs so the tick costs four bounded reads total regardless of anchor count.
func (s *RootCauseSynthesizer) run(ctx context.Context) {
	now := time.Now()
	in := gatherEvidenceInputs(ctx, s.store)

	done := 0

	// Anchor 1 — recent / active anomalies. ListAnomalyEvents is bounded by
	// last_seen >= sinceNs + LIMIT; we reuse the SinceNs the evidence fetch
	// already uses, but cap to the synthesizer's own recency window so a 60m
	// evidence window doesn't drag in already-cleared anomalies.
	events, err := s.store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{
		SinceNs: now.Add(-synthesizerRecentAnomaly).UnixNano(),
		Limit:   s.batch,
	})
	if err != nil {
		log.Printf("[rootcause-synth] list anomalies: %v", err)
	}
	for _, ev := range events {
		if done >= s.batch {
			break
		}
		h := correlator.Synthesize(
			"anomaly", ev.ID, ev.Service, now.UnixNano(),
			synthInputForAnomaly(ev, in),
		)
		if err := s.store.UpsertHypothesis(ctx, h); err != nil {
			log.Printf("[rootcause-synth] upsert anomaly %s: %v", ev.ID, err)
			continue
		}
		done++
	}

	// Anchor 2 — high-severity (critical) open problems, same as the
	// explainer's batch. buildEvidenceBundle is the existing pure fuser over
	// the shared inputs; we distil its bundle into the synthesis input.
	if done < s.batch {
		problems, err := s.store.ListProblems(ctx, chstore.ProblemFilter{
			Status:   "open",
			Severity: "critical",
			Limit:    s.batch,
		})
		if err != nil {
			log.Printf("[rootcause-synth] list problems: %v", err)
		}
		for _, p := range problems {
			if done >= s.batch {
				break
			}
			bundle := buildEvidenceBundle(p, in)
			h := correlator.Synthesize(
				"problem", p.ID, p.Service, now.UnixNano(),
				synthInputForProblem(p, bundle),
			)
			if err := s.store.UpsertHypothesis(ctx, h); err != nil {
				log.Printf("[rootcause-synth] upsert problem %s: %v", p.ID, err)
				continue
			}
			done++
		}
	}

	if done > 0 {
		log.Printf("[rootcause-synth] synthesized %d hypothesis/es", done)
	}
}

// synthInputForProblem distils a problem's EvidenceBundle into the pure fuser's
// input: the deploy (with a freshness fraction relative to the lookback
// window), the propagation-ranked neighbour suspects (best first, already
// scored by buildEvidenceBundle), and the co-firing same-service problems'
// services. Pure helper — no store, no ctx.
func synthInputForProblem(p chstore.Problem, b EvidenceBundle) correlator.SynthesisInput {
	in := correlator.SynthesisInput{
		Deploy:        deployFromEntry(b.Deploy, p.StartedAt),
		FreshnessFrac: freshnessFrac(b.Deploy, p.StartedAt),
	}
	for _, nb := range b.Neighbors {
		in.Neighbours = append(in.Neighbours, correlator.ScoredCause{
			Service: nb.Problem.Service,
			Score:   nb.Score,
			Hops:    nb.Hops,
		})
	}
	for _, cp := range b.CoFiring {
		in.CoFiringServices = append(in.CoFiringServices, cp.Service)
	}
	// v0.8.571 — the bundle collected these since day one; the synthesis
	// never saw them. buildEvidenceBundle already filtered to same-service
	// + active, so this is a straight mapping. Ratio = the stronger of the
	// current and peak readings (a spike that just cooled still evidences).
	for _, sig := range b.Signals {
		in.Signals = append(in.Signals, correlator.SignalEvidence{
			Kind:    sig.Kind,
			Pattern: sig.Pattern,
			Ratio:   math.Max(sig.CurrentRatio, sig.PeakRatio),
		})
	}
	return in
}

// synthInputForAnomaly assembles the SAME synthesis input for an anomaly anchor
// directly from the shared evidence inputs — there is no Problem-shaped bundle
// for an anomaly, so we mirror buildEvidenceBundle's deploy + propagation +
// co-firing logic against the AnomalyEvent's service/onset. Pure helper.
func synthInputForAnomaly(ev chstore.AnomalyEvent, in evidenceInputs) correlator.SynthesisInput {
	out := correlator.SynthesisInput{}

	// Deploy of THIS service whose first-seen lands in the lookback window
	// before onset — the most recent such is the prime "what changed". Same
	// selection rule buildEvidenceBundle applies to a Problem.
	lo := ev.StartedAt - evidenceDeployLookback.Nanoseconds()
	var deploy *chstore.RecentDeployEntry
	for i := range in.deploys {
		d := &in.deploys[i]
		if d.Service != ev.Service || d.FirstSeenNs < lo || d.FirstSeenNs > ev.StartedAt {
			continue
		}
		if deploy == nil || d.FirstSeenNs > deploy.FirstSeenNs {
			deploy = d
		}
	}
	out.Deploy = deployFromEntry(deploy, ev.StartedAt)
	out.FreshnessFrac = freshnessFrac(deploy, ev.StartedAt)

	// Propagation-ranked downstream suspects over the weighted graph, anchored
	// on the anomaly's service. Same scorer the bundle uses. Only neighbours
	// that ALSO carry an open problem corroborate, but for the hypothesis we
	// surface the propagation suspects directly (the fuser drops zero-score
	// ones) so a downstream source shows even without its own open Problem.
	for _, sc := range correlator.RankRootCausesFromEdges(in.weightedAdjacency, ev.Service) {
		if sc.Service == "" || sc.Score <= 0 {
			continue
		}
		out.Neighbours = append(out.Neighbours, sc)
	}

	// Co-firing — other OPEN problems on the SAME service as the anomaly.
	for _, op := range in.openProblems {
		if op.Service == ev.Service {
			out.CoFiringServices = append(out.CoFiringServices, op.Service)
		}
	}
	// Signals (v0.8.571) — other active anomalies on the same service.
	// ID != ev.ID excludes the anchor itself (the mirror of the problem
	// path's op.ID exclusion in buildEvidenceBundle): an anomaly must not
	// corroborate its own hypothesis.
	for _, sig := range in.events {
		if sig.Service != ev.Service || sig.Status != "active" || sig.ID == ev.ID {
			continue
		}
		out.Signals = append(out.Signals, correlator.SignalEvidence{
			Kind:    sig.Kind,
			Pattern: sig.Pattern,
			Ratio:   math.Max(sig.CurrentRatio, sig.PeakRatio),
		})
	}
	return out
}

// deployFromEntry converts the evidence bundle's *RecentDeployEntry into the
// *RecentDeploy shape the hypothesis persists (the same compact deploy signal
// the /problems + /anomalies lists already attach). AgeSeconds is positive when
// the deploy landed before onset (the typical correlate-with-incident case),
// computed from the anchor's onset minus the deploy's first-seen. Returns nil
// for a nil entry. Pure.
func deployFromEntry(d *chstore.RecentDeployEntry, onsetNs int64) *chstore.RecentDeploy {
	if d == nil {
		return nil
	}
	return &chstore.RecentDeploy{
		Version:    d.Version,
		TimeUnixNs: d.FirstSeenNs,
		AgeSeconds: (onsetNs - d.FirstSeenNs) / int64(time.Second),
	}
}

// freshnessFrac maps how recently a deploy landed before onset into [0,1] for
// the fuser's deploy freshness bonus: 1 = right at onset, 0 = at the far edge
// of the evidenceDeployLookback window (or no deploy). A deploy that somehow
// post-dates onset (clock skew) clamps to 1 (treated as maximally fresh, since
// it is still the closest-in-time change). Pure.
func freshnessFrac(d *chstore.RecentDeployEntry, onsetNs int64) float64 {
	if d == nil {
		return 0
	}
	lookback := float64(evidenceDeployLookback.Nanoseconds())
	if lookback <= 0 {
		return 0
	}
	ageNs := float64(onsetNs - d.FirstSeenNs) // >0 when deploy precedes onset
	frac := 1 - ageNs/lookback
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	return frac
}
