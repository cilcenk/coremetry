package correlator

import (
	"math"
	"sort"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// Root-cause propagation scoring (Faz 6, v0.8.68). On the directed
// weighted graph Faz 5 produces, rank a triggering service's downstream
// dependencies by how likely each is the SOURCE of the trigger's
// failure — the Datadog-Watchdog / Dynatrace-Davis "I found a problem,
// here's the probable cause" heuristic, which is Coremetry's intended
// differentiator (see project-correlation-differentiator).
//
// Model — STRUCTURAL error-propagation, not temporal correlation. With
// only per-edge aggregate error counts (the MV gives volume, not
// per-request causality), the conditional "P(failure originates at D |
// the trigger S is failing)" is approximated by D's SHARE of S's
// downstream error volume:
//
//	share(S→D) = errors(S→D) / Σ_x errors(S→x)   (x ≠ S; self-edges excluded)
//
// extended transitively with a per-hop decay so a 2-hop dependency
// (S→D→E) counts less than a direct one:
//
//	score(D)          = share(S→D)
//	score(E via S→D→E) = decay · share(S→D) · share(D→E)
//
// Capped at propagationMaxHops ("decayed 2-hop") and cycle-guarded with
// an on-path set so S→D→S can't loop. When a service is reachable by
// more than one path, the STRONGEST single path wins (max, not sum) —
// "the best single explanation", and it keeps the score in [0,1].
//
// A truer temporal conditional probability (per-5-min-bucket caller vs
// callee error-series correlation) is a deliberate future refinement;
// the structural score is the data-appropriate first cut and consumes
// the Faz 5 graph directly with no extra query.
const (
	// propagationDecay weights each extra hop — a 2-hop suspect carries
	// half the score the same error share would at 1 hop.
	propagationDecay = 0.5
	// propagationMaxHops bounds the transitive walk (decayed 2-hop).
	propagationMaxHops = 2
)

// ScoredCause is one root-cause candidate for a triggering service: the
// suspect dependency, its propagation score in [0,1], the hop distance,
// and the path it was reached by (trigger … candidate).
type ScoredCause struct {
	Service string
	Score   float64
	Hops    int
	Path    []string
}

// RootCauseRank scores the live graph's downstream candidates for svc,
// best first. Read-locked snapshot — safe to call concurrently with the
// refresh loop.
func (c *Correlator) RootCauseRank(svc string) []ScoredCause {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return rankRootCauses(c.out, svc)
}

// RankRootCausesFromEdges scores a one-off weighted edge list without a
// live Correlator — the anomaly fusion path uses this so its evidence
// bundle stays a pure, store-free assembly (it already holds the edges).
// Reuses buildGraph so the directed-edge construction has one home.
func RankRootCausesFromEdges(edges []chstore.ServiceEdgePair, trigger string) []ScoredCause {
	out, _ := buildGraph(edges)
	return rankRootCauses(out, trigger)
}

// rankRootCauses is the pure scorer over an out-adjacency map. The
// returned slice is sorted score desc, then hops asc, then service asc
// (a total, deterministic order — map iteration is not).
func rankRootCauses(out map[string]map[string]EdgeStat, trigger string) []ScoredCause {
	best := map[string]ScoredCause{}    // candidate → its highest-scoring reach
	onPath := map[string]bool{trigger: true}

	var walk func(node string, hop int, sharesProduct float64, path []string)
	walk = func(node string, hop int, sharesProduct float64, path []string) {
		if hop > propagationMaxHops {
			return
		}
		row := out[node]
		if len(row) == 0 {
			return
		}
		// Denominator: node's downstream error volume, excluding self-edges
		// (a service calling itself is part of its OWN failure, not a
		// downstream propagation, and would dilute real deps' shares).
		var totalErr uint64
		for m, st := range row {
			if m == node {
				continue
			}
			totalErr += st.Errors
		}
		if totalErr == 0 {
			return // no error signal flows downstream from here
		}
		for m, st := range row {
			if m == node || onPath[m] {
				continue // self-edge or cycle back onto the current path
			}
			share := float64(st.Errors) / float64(totalErr)
			if share == 0 {
				continue
			}
			prod := sharesProduct * share
			contribution := prod * math.Pow(propagationDecay, float64(hop-1))
			next := append(append([]string{}, path...), m)
			cand := ScoredCause{Service: m, Score: contribution, Hops: hop, Path: next}
			if cur, ok := best[m]; !ok || betterCause(cand, cur) {
				best[m] = cand
			}
			onPath[m] = true
			walk(m, hop+1, prod, next)
			onPath[m] = false
		}
	}
	walk(trigger, 1, 1.0, []string{trigger})

	res := make([]ScoredCause, 0, len(best))
	for _, sc := range best {
		res = append(res, sc)
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].Score != res[j].Score {
			return res[i].Score > res[j].Score
		}
		if res[i].Hops != res[j].Hops {
			return res[i].Hops < res[j].Hops
		}
		return res[i].Service < res[j].Service
	})
	return res
}

// causeScoreEps — two propagation scores within this are treated as a TIE. The
// same float ops produce bit-identical scores for a given path in both the DFS
// and the brute-force oracle, but a tie can still arise when the SAME service
// is reachable by two different paths whose scores coincide (e.g. a direct
// 1-hop edge and a 2-hop path that multiply out to the same value). Kept equal
// to the oracle's comparison epsilon so "best reach" is defined identically on
// both sides.
const causeScoreEps = 1e-9

// betterCause is the single definition of "is reach a a better explanation for
// this service than reach b". Higher score wins; on a score TIE the SHORTER
// path wins (a direct cause beats a transitive one); on a hop tie the
// lexicographically smaller path wins. That total order makes the chosen
// Hops/Path deterministic regardless of map-iteration order — without it, an
// exact score tie between a 1-hop and a 2-hop reach recorded whichever the map
// happened to visit first.
func betterCause(a, b ScoredCause) bool {
	if math.Abs(a.Score-b.Score) > causeScoreEps {
		return a.Score > b.Score
	}
	if a.Hops != b.Hops {
		return a.Hops < b.Hops
	}
	return pathLess(a.Path, b.Path)
}

// pathLess compares two paths lexicographically (shorter is "less" when one is
// a prefix of the other).
func pathLess(a, b []string) bool {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}
