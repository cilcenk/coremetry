package correlator

import (
	"fmt"
	"sort"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// Automatic anomaly → root-cause synthesis (rc #2). Synthesize is the PURE
// fuser: it takes the evidence the worker already gathered (a deploy that lines
// up in time, the propagation-ranked downstream neighbours, and the count of
// co-firing same-service problems) and collapses them into ONE ranked,
// confidence-weighted hypothesis. No I/O, no store, no ctx — the worker does
// the bounded CH reads, this just ranks. Deterministic + total-ordered so the
// same evidence always produces the same hypothesis (table-driven tested).
//
// Why a separate fuser from anomaly.buildEvidenceBundle: the bundle is the raw
// corroboration set ("here is everything that lines up"); Synthesize is the
// JUDGEMENT on top of it ("here is what most likely CAUSED it, ranked"). Keeping
// it in correlator (alongside the propagation scorer it consumes) means the
// ranking logic lives next to the 0.5^hops error-share model it builds on, and
// stays import-cycle-free (anomaly imports correlator, never the reverse — so
// the worker destructures its EvidenceBundle into the primitive inputs below).
//
// ── Ranking weights ─────────────────────────────────────────────────────────
// The priority order is deploy ≫ propagation-ranked neighbour ≫ co-firing,
// reflecting how an SRE actually triages: "what CHANGED?" first (a deploy is
// the single most actionable, most-often-correct suspect), then "what
// downstream is the structural source?" (the propagation share), then "what
// else is unhappy on this same service?" (co-firing — corroboration, rarely the
// root). The blended score keeps these tiers separated so a deploy always
// outranks a propagation suspect, which always outranks a co-firing problem,
// regardless of the raw propagation magnitude:
//
//	deploy candidate  : score = deployBaseScore + freshnessBonus   (∈ [0.80, 0.95])
//	propagation cand. : score = propTierBase * propagation.Score   (∈ (0, 0.70])
//	co-firing cand.   : score = cofiringScore                      (= 0.20, flat)
//
// Confidence (0..1) is a blend of HOW MANY independent evidence types lined up
// (the fusion.go confidence model — more corroboration = more confident) AND
// how strong the top signal is (a fresh deploy or a high propagation share
// lifts it). Empty evidence → 0 confidence + empty candidates + empty suspect
// (an honest "no clear cause"), never a fabricated guess.
const (
	// deployBaseScore — a same-service deploy in the lookback window is the
	// strongest single signal; it floors above any propagation suspect.
	deployBaseScore = 0.80
	// deployFreshnessBonusMax — added on top of the base, scaled by how
	// recently the deploy landed before onset (a deploy 1m before onset is a
	// stronger suspect than one 29m before). Caps the deploy tier at 0.95.
	deployFreshnessBonusMax = 0.15
	// propTierBase — scales propagation scores (∈ [0,1]) into the tier BELOW
	// the deploy floor, so even a share-1.0 downstream suspect (0.70) ranks
	// under a deploy (≥0.80) but above co-firing (0.20).
	propTierBase = 0.70
	// cofiringScore — flat weight for a co-firing same-service problem.
	// Corroborates, rarely causes — lowest tier.
	cofiringScore = 0.20
)

// Confidence model weights — the blend of breadth (distinct evidence types) and
// strength (top candidate score). Sum of the two coefficients is the cap, so a
// hypothesis with maximal breadth AND a maximal-strength top signal approaches
// 1.0; thin evidence stays honestly low.
const (
	// confidenceBreadthWeight — contribution from evidence breadth. The
	// breadth fraction is (distinctTypes / maxEvidenceTypes), matching the
	// fusion.go "distinct corroborating types" confidence intuition.
	confidenceBreadthWeight = 0.5
	// confidenceStrengthWeight — contribution from the top candidate's score
	// (already in [0,1] after the tiering above).
	confidenceStrengthWeight = 0.5
	// maxEvidenceTypes — deploy, propagation neighbours, co-firing. Three
	// independent corroboration channels feed the breadth fraction.
	maxEvidenceTypes = 3
)

// SynthesisInput is the evidence the fuser ranks. The worker fills this by
// destructuring its anomaly.EvidenceBundle (deploy + neighbours + co-firing)
// for the anchor's service — Synthesize itself touches no store.
type SynthesisInput struct {
	// Deploy — a same-service deploy that landed in the lookback window before
	// onset, or nil. FreshnessFrac ∈ [0,1] says how close to onset it landed
	// (1 = right at onset, 0 = at the far edge of the lookback window); the
	// worker computes it from (onset - deployTime) / lookback.
	Deploy        *chstore.RecentDeploy
	FreshnessFrac float64
	// Neighbours — the propagation-ranked downstream suspects (best first),
	// straight from RankRootCausesFromEdges. Score ∈ [0,1] is the error-share,
	// hop-decayed. Empty when nothing downstream carries error volume.
	Neighbours []ScoredCause
	// CoFiringServices — distinct OTHER services with an open problem co-firing
	// on the SAME anchor service's incident. In practice these are same-service
	// (so usually the anchor service itself) — the worker passes the service
	// label per co-firing problem so the candidate carries a name. Deduped +
	// sorted by the fuser for determinism.
	CoFiringServices []string
}

// Synthesize fuses the evidence into ONE ranked, confidence-weighted
// hypothesis. anchorKind/anchorID/service stamp the anchor; computedAtNs stamps
// when (the worker passes a single now() so a batch tick shares one timestamp).
// Returns a chstore.RootCauseHypothesis ready to UpsertHypothesis — the worker
// does no further shaping.
//
// Determinism: candidates are built tier-by-tier (deploy, then propagation in
// the order RankRootCausesFromEdges already total-orders, then co-firing sorted
// by name), then a STABLE sort by score desc with a Service-name tie-break, so
// identical evidence always yields byte-identical output (the table-driven test
// asserts the tie-break).
func Synthesize(
	anchorKind, anchorID, service string,
	computedAtNs int64,
	in SynthesisInput,
) chstore.RootCauseHypothesis {
	h := chstore.RootCauseHypothesis{
		AnchorKind: anchorKind,
		AnchorID:   anchorID,
		Service:    service,
		ComputedAt: computedAtNs,
		Candidates: []chstore.ScoredCause{},
	}

	var cands []chstore.ScoredCause
	distinctTypes := 0

	// Tier 1 — the deploy. Strongest single signal; floors above propagation.
	if in.Deploy != nil {
		distinctTypes++
		frac := in.FreshnessFrac
		if frac < 0 {
			frac = 0
		}
		if frac > 1 {
			frac = 1
		}
		score := deployBaseScore + deployFreshnessBonusMax*frac
		ageMin := in.Deploy.AgeSeconds / 60
		h.RecentDeploy = in.Deploy
		cands = append(cands, chstore.ScoredCause{
			Service: service, // a deploy of the anchor's OWN service is the suspect
			Score:   score,
			Hops:    0,
			Path:    []string{service},
			Reason: fmt.Sprintf("deployed %s %dm before onset — prime 'what changed' suspect",
				in.Deploy.Version, ageMin),
		})
	}

	// Tier 2 — propagation-ranked downstream neighbours. Already best-first.
	if len(in.Neighbours) > 0 {
		distinctTypes++
		for _, nb := range in.Neighbours {
			if nb.Service == "" || nb.Score <= 0 {
				continue
			}
			cands = append(cands, chstore.ScoredCause{
				Service: nb.Service,
				Score:   propTierBase * nb.Score,
				Hops:    nb.Hops,
				Path:    nb.Path,
				Reason: fmt.Sprintf("downstream dependency — %.0f%% of error share, %d-hop",
					nb.Score*100, nb.Hops),
			})
		}
	}

	// Tier 3 — co-firing same-service problems. Corroboration, lowest tier.
	// Dedup + sort the service labels so the candidate order is deterministic
	// and we don't emit two co-firing candidates for the same service.
	if len(in.CoFiringServices) > 0 {
		distinctTypes++
		seen := map[string]bool{}
		coServices := make([]string, 0, len(in.CoFiringServices))
		for _, sv := range in.CoFiringServices {
			if sv == "" || seen[sv] {
				continue
			}
			seen[sv] = true
			coServices = append(coServices, sv)
		}
		sort.Strings(coServices)
		for _, sv := range coServices {
			cands = append(cands, chstore.ScoredCause{
				Service: sv,
				Score:   cofiringScore,
				Hops:    0,
				Path:    []string{sv},
				Reason:  "co-firing problem on the same incident — corroborating, not necessarily the cause",
			})
		}
	}

	// Total order: score desc, then Service name asc. Stable so equal-score
	// candidates keep their tier-insertion order before the name tie-break
	// settles it deterministically.
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Score != cands[j].Score {
			return cands[i].Score > cands[j].Score
		}
		return cands[i].Service < cands[j].Service
	})
	if len(cands) > 0 {
		h.Candidates = cands
		h.TopSuspect = cands[0].Service
		h.TopScore = cands[0].Score
	}

	// Confidence = breadth (distinct evidence types) blended with the top
	// signal's strength. Zero evidence → zero confidence, honestly.
	breadth := float64(distinctTypes) / float64(maxEvidenceTypes)
	if breadth > 1 {
		breadth = 1
	}
	h.Confidence = confidenceBreadthWeight*breadth + confidenceStrengthWeight*h.TopScore
	if h.Confidence > 1 {
		h.Confidence = 1
	}
	return h
}
