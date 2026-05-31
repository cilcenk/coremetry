package api

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// RootCause is the assembled "what changed / likely cause" bundle for one
// Problem (v0.7.51). It orchestrates signals that already exist but were
// scattered — recent deploy, correlated service changes, dimension bubble-up,
// blast radius, an exemplar trace — into a single cached read so the Problem
// triage drawer shows one root-cause surface instead of the operator hopping
// across pages. Read-only.
type RootCause struct {
	ProblemID    string                   `json:"problemId"`
	Service      string                   `json:"service"`
	Metric       string                   `json:"metric"`
	StartedAt    int64                    `json:"startedAt"`
	FromNs       int64                    `json:"fromNs"`
	ToNs         int64                    `json:"toNs"`
	RecentDeploy *chstore.RecentDeploy    `json:"recentDeploy,omitempty"`
	Correlations []chstore.ChangedService `json:"correlations"`
	BlastRadius  *chstore.BlastRadius     `json:"blastRadius,omitempty"`
	BubbleUp     *chstore.BubbleUpResult  `json:"bubbleUp,omitempty"`
	Exemplar     *chstore.Exemplar        `json:"exemplar,omitempty"`
}

// exemplarKindForMetric maps a problem's breached metric to the exemplar trace
// that best illustrates it: error_rate → an erroring trace, latency → a slow
// trace, everything else → any representative trace.
func exemplarKindForMetric(metric string) chstore.ExemplarKind {
	m := strings.ToLower(metric)
	switch {
	case strings.Contains(m, "error"):
		return chstore.ExemplarError
	case strings.Contains(m, "p99") || strings.Contains(m, "p95") ||
		strings.Contains(m, "latency") || strings.Contains(m, "duration") || strings.Contains(m, "ms"):
		return chstore.ExemplarSlow
	default:
		return chstore.ExemplarAny
	}
}

// getProblemRootCause assembles the root-cause bundle for one problem. Read-only,
// open like /api/problems. Fans out to the existing correlation/blast/bubbleup/
// exemplar reads in PARALLEL (the goroutines write disjoint fields of `out`, so
// no shared-word race); each sub-read SOFT-FAILS to a nil/empty field rather
// than failing the whole bundle — a partial root-cause view still helps triage.
// Cached 60s keyed on problem id + the window-end minute (so an open problem's
// view refreshes minute-to-minute while concurrent triage clicks share the trip).
func (s *Server) getProblemRootCause(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "problem id required", http.StatusBadRequest)
		return
	}
	// Load outside the cache so a missing problem is a clean 404 (not a cached
	// empty bundle). The problems table is small + FINAL — cheap.
	p, err := s.store.GetProblem(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if p == nil {
		http.Error(w, "problem not found", http.StatusNotFound)
		return
	}

	started := time.Unix(0, p.StartedAt)
	end := time.Now()
	if p.ResolvedAt != nil {
		end = time.Unix(0, *p.ResolvedAt)
	}
	// Bound the analysis window: ≥10m of context so a just-fired problem has
	// something to compare, ≤1h so the bubbleup/exemplar span scans stay cheap
	// no matter how long it has been open.
	if end.Sub(started) < 10*time.Minute {
		end = started.Add(10 * time.Minute)
	}
	if end.Sub(started) > time.Hour {
		end = started.Add(time.Hour)
	}
	windowSec := int(end.Sub(started).Seconds())

	key := fmt.Sprintf("rootcause:%s:%d", id, end.Truncate(time.Minute).Unix())
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		out := RootCause{
			ProblemID: p.ID, Service: p.Service, Metric: p.Metric,
			StartedAt:    p.StartedAt,
			FromNs:       started.UnixNano(),
			ToNs:         end.UnixNano(),
			Correlations: []chstore.ChangedService{},
		}
		// Recent deploy — reuse the same enrichment the /problems list uses.
		if enr := s.store.EnrichProblemsWithDeploys(r.Context(), []chstore.Problem{*p}, 30*time.Minute); len(enr) == 1 {
			out.RecentDeploy = enr[0].RecentDeploy
		}

		var wg sync.WaitGroup
		// (a) Correlations — services moving together around the problem start.
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cs, e := s.store.GetCorrelatedChanges(r.Context(), started, windowSec, windowSec*4); e == nil {
				out.Correlations = cs
			}
		}()
		// (b) Blast radius — who calls this service + how many are cascading.
		wg.Add(1)
		go func() {
			defer wg.Done()
			if br, e := s.store.GetServiceBlastRadius(r.Context(), p.Service, end.Sub(started)); e == nil {
				out.BlastRadius = &br
			}
		}()
		// (c) Exemplar — one representative bad trace for the metric.
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ex, e := s.store.FindExemplar(r.Context(), chstore.ExemplarReq{
				Service: p.Service, From: started, To: end,
				Kind: exemplarKindForMetric(p.Metric),
			}); e == nil {
				out.Exemplar = ex
			}
		}()
		// (d) Dimension bubble-up — only for ERROR problems, where "which
		// route/host/version concentrates the errors" is well-defined:
		// selection = erroring spans of the service, baseline = all its spans
		// in the same window. (Latency "slow" isn't a clean FilterExpr subset;
		// the exemplar covers that case.)
		if exemplarKindForMetric(p.Metric) == chstore.ExemplarError {
			wg.Add(1)
			go func() {
				defer wg.Done()
				baseline := []chstore.FilterExpr{{Key: "service.name", Op: "=", Values: []string{p.Service}}}
				selection := []chstore.FilterExpr{
					{Key: "service.name", Op: "=", Values: []string{p.Service}},
					{Key: "status_code", Op: "=", Values: []string{"error"}},
				}
				if bu, e := s.store.BubbleUp(r.Context(), baseline, selection, started, end); e == nil {
					out.BubbleUp = bu
				}
			}()
		}
		wg.Wait()
		return out, nil
	})
}
