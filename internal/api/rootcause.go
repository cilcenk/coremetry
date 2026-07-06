package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
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

// AnomalyRootCause is the anomaly-anchored sibling of RootCause (v0.8.x).
// It embeds the SAME RootCause fan-out result so /anomalies and /problems
// share one rendering path later, and stamps the anchor from the
// AnomalyEvent (id/kind/pattern/service) instead of a Problem. Read-only.
type AnomalyRootCause struct {
	RootCause
	AnomalyID   string `json:"anomalyId"`
	AnomalyKind string `json:"anomalyKind"` // log_pattern | log_template_new | trace_op
	Pattern     string `json:"pattern"`     // log pattern name OR operation name (trace_op)
}

// boundAnalysisWindow clamps an anchor's [started, end] to the same
// [10m, 1h] envelope getProblemRootCause uses: ≥10m so a just-fired
// anchor has comparison context, ≤1h so the bubbleup/exemplar span
// scans stay cheap no matter how long it has been open. Pure — the
// table-driven test in rootcause_test.go exercises the sub-10m,
// in-range, and over-1h branches. `end` is moved relative to `started`
// (never the reverse) so the window always begins at the anchor's start.
func boundAnalysisWindow(started, end time.Time) (time.Time, time.Time) {
	if end.Sub(started) < 10*time.Minute {
		end = started.Add(10 * time.Minute)
	}
	if end.Sub(started) > time.Hour {
		end = started.Add(time.Hour)
	}
	return started, end
}

// exemplarKindForAnomaly maps an AnomalyEvent.Kind to the exemplar trace
// that best illustrates it. A trace_op event is an error-ratio anomaly
// (the recorder sets CurrentCount = error count), so an erroring trace is
// the representative one. Log anomalies (log_pattern, log_template_new)
// aren't tied to a span status, so any representative trace on the service
// is fine. Pure — table-driven tested over every recorder kind.
func exemplarKindForAnomaly(kind string) chstore.ExemplarKind {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "trace_op":
		return chstore.ExemplarError
	default: // log_pattern, log_template_new, anything unknown
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
	started, end = boundAnalysisWindow(started, end)
	windowSec := int(end.Sub(started).Seconds())

	key := fmt.Sprintf("rootcause:%s:%d", id, end.Truncate(time.Minute).Unix())
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		out := RootCause{
			ProblemID: p.ID, Service: p.Service, Metric: p.Metric,
			StartedAt:    p.StartedAt,
			FromNs:       started.UnixNano(),
			ToNs:         end.UnixNano(),
			Correlations: []chstore.ChangedService{},
		}
		// Recent deploy — reuse the same enrichment the /problems list uses.
		if enr := s.store.EnrichProblemsWithDeploys(ctx, []chstore.Problem{*p}, 30*time.Minute); len(enr) == 1 {
			out.RecentDeploy = enr[0].RecentDeploy
		}

		var wg sync.WaitGroup
		// (a) Correlations — services moving together around the problem start.
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cs, e := s.store.GetCorrelatedChanges(ctx, started, windowSec, windowSec*4); e == nil {
				out.Correlations = cs
			}
		}()
		// (b) Blast radius — who calls this service + how many are cascading.
		wg.Add(1)
		go func() {
			defer wg.Done()
			if br, e := s.store.GetServiceBlastRadius(ctx, p.Service, end.Sub(started)); e == nil {
				out.BlastRadius = &br
			}
		}()
		// (c) Exemplar — one representative bad trace for the metric.
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ex, e := s.store.FindExemplar(ctx, chstore.ExemplarReq{
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
				if bu, e := s.store.BubbleUp(ctx, baseline, selection, started, end); e == nil {
					out.BubbleUp = bu
				}
			}()
		}
		wg.Wait()
		return out, nil
	})
}

// getAnomalyRootCause assembles the same root-cause bundle as
// getProblemRootCause, but anchored on an AnomalyEvent instead of a
// Problem (v0.8.x — release #1 of the anomaly → root-cause feature).
// The window is derived from the event: service = AnomalyEvent.Service,
// from = StartedAt, to = LastSeen, clamped to the SAME [10m, 1h] envelope
// via boundAnalysisWindow. Read-only, open like /api/anomalies and
// getProblemRootCause (no write, no audit). Same parallel soft-fail
// fan-out — each sub-read degrades to a nil/empty field rather than
// failing the bundle. Cached 60s keyed on the event id + the window-end
// minute so an active anomaly's view refreshes minute-to-minute while
// concurrent triage clicks share the trip.
func (s *Server) getAnomalyRootCause(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "anomaly id required", http.StatusBadRequest)
		return
	}
	// Load outside the cache so a missing event is a clean 404 (not a cached
	// empty bundle). anomaly_events is a small ReplacingMergeTree read with
	// FINAL — cheap, no time-bound needed (id is the PK).
	ev, err := s.store.GetAnomalyEvent(r.Context(), id, 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	if ev == nil {
		http.Error(w, "anomaly not found", http.StatusNotFound)
		return
	}

	started := time.Unix(0, ev.StartedAt)
	end := time.Unix(0, ev.LastSeen)
	// LastSeen can equal or (on a clock skew) precede StartedAt for a
	// just-recorded event; boundAnalysisWindow floors the span to 10m from
	// the start, so the window is always well-formed [started, started+≥10m].
	started, end = boundAnalysisWindow(started, end)
	windowSec := int(end.Sub(started).Seconds())
	exKind := exemplarKindForAnomaly(ev.Kind)

	key := fmt.Sprintf("anomaly-rootcause:%s:%d", id, end.Truncate(time.Minute).Unix())
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		out := AnomalyRootCause{
			RootCause: RootCause{
				ProblemID: "", // anomaly-anchored — no parent Problem
				Service:   ev.Service,
				Metric:    ev.Kind, // shared-render label: the anomaly kind
				StartedAt: ev.StartedAt,
				FromNs:    started.UnixNano(),
				ToNs:      end.UnixNano(),
				Correlations: []chstore.ChangedService{},
			},
			AnomalyID:   ev.ID,
			AnomalyKind: ev.Kind,
			Pattern:     ev.Pattern,
		}
		// Recent deploy — reuse the SAME enrichment the /anomalies list uses.
		if enr := s.store.EnrichAnomaliesWithDeploys(ctx, []chstore.AnomalyEvent{*ev}, 30*time.Minute); len(enr) == 1 {
			out.RecentDeploy = enr[0].RecentDeploy
		}

		var wg sync.WaitGroup
		// (a) Correlations — services moving together around the anomaly start.
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cs, e := s.store.GetCorrelatedChanges(ctx, started, windowSec, windowSec*4); e == nil {
				out.Correlations = cs
			}
		}()
		// (b) Blast radius — who calls this service + how many are cascading.
		wg.Add(1)
		go func() {
			defer wg.Done()
			if br, e := s.store.GetServiceBlastRadius(ctx, ev.Service, end.Sub(started)); e == nil {
				out.BlastRadius = &br
			}
		}()
		// (c) Exemplar — one representative bad trace. A trace_op event already
		// carries the precise representative trace id (recorder sets
		// Sample = SampleTraceID); prefer it directly — it's THE trace that
		// drove the anomaly, no scan. Fall back to FindExemplar (scoped to the
		// op for trace_op via Pattern) when the sample is empty / for log kinds.
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ev.Kind == "trace_op" && strings.TrimSpace(ev.Sample) != "" {
				out.Exemplar = &chstore.Exemplar{TraceID: ev.Sample, Service: ev.Service, Name: ev.Pattern}
				return
			}
			op := ""
			if ev.Kind == "trace_op" {
				op = ev.Pattern // scope the exemplar to the anomalous operation
			}
			if ex, e := s.store.FindExemplar(ctx, chstore.ExemplarReq{
				Service: ev.Service, Operation: op, From: started, To: end, Kind: exKind,
			}); e == nil {
				out.Exemplar = ex
			}
		}()
		// (d) Dimension bubble-up — only for trace_op anomalies, where the
		// erroring-span subset of the service is a clean FilterExpr selection
		// (same shape as getProblemRootCause's error branch). Log anomalies
		// (log_pattern / log_template_new) aren't a span-status subset, so the
		// bubble-up is skipped — the exemplar + correlations cover them.
		if exKind == chstore.ExemplarError {
			wg.Add(1)
			go func() {
				defer wg.Done()
				baseline := []chstore.FilterExpr{{Key: "service.name", Op: "=", Values: []string{ev.Service}}}
				selection := []chstore.FilterExpr{
					{Key: "service.name", Op: "=", Values: []string{ev.Service}},
					{Key: "status_code", Op: "=", Values: []string{"error"}},
				}
				if bu, e := s.store.BubbleUp(ctx, baseline, selection, started, end); e == nil {
					out.BubbleUp = bu
				}
			}()
		}
		wg.Wait()
		return out, nil
	})
}

// buildRootCausePrompt renders a persisted RootCauseHypothesis into the
// compact user prompt the narration model consumes (rc #4). PURE — no I/O,
// table-driven tested in rootcause_test.go over the no-suspect, deploy-led,
// and propagation-led shapes so the prompt the model sees stays stable. It
// flattens the SAME ranked evidence the deterministic ribbon already shows:
// the anchor context, the top suspect + score + confidence, every ranked
// candidate with its score / hop distance / Reason line, and the recent
// deploy the fuser weighted. It deliberately does NOT re-rank or add signal —
// the model narrates what the worker already computed. The candidate list is
// capped at the top 8 so a pathological fan-out can't balloon the prompt.
func buildRootCausePrompt(h *chstore.RootCauseHypothesis) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Anchor: %s\n", h.AnchorKind)
	fmt.Fprintf(&b, "Service: %s\n", h.Service)
	if h.TopSuspect != "" {
		fmt.Fprintf(&b, "Top suspect: %s (score %.1f)\n", h.TopSuspect, h.TopScore)
	} else {
		b.WriteString("Top suspect: (none — no single cause stood out)\n")
	}
	fmt.Fprintf(&b, "Confidence: %.0f%%\n", h.Confidence*100)
	if h.RecentDeploy != nil {
		fmt.Fprintf(&b, "Recent deploy: service.version=%s first seen %s before onset\n",
			h.RecentDeploy.Version, fmtDeployAge(h.RecentDeploy.AgeSeconds))
	}
	if len(h.Candidates) == 0 {
		b.WriteString("Ranked candidates: (none)\n")
		return b.String()
	}
	b.WriteString("Ranked candidates (best first):\n")
	const maxCands = 8
	for i, c := range h.Candidates {
		if i >= maxCands {
			break
		}
		hops := ""
		if c.Hops > 0 {
			hops = fmt.Sprintf(", %d hop(s)", c.Hops)
		}
		reason := strings.TrimSpace(c.Reason)
		if reason == "" {
			reason = "no reason recorded"
		}
		fmt.Fprintf(&b, "  %d. %s (score %.1f%s) — %s\n", i+1, c.Service, c.Score, hops, reason)
	}
	return b.String()
}

// fmtDeployAge — compact "6m" / "2h" / "3d" age for the deploy line in the
// narration prompt. Mirrors the frontend fmtAgo so the prose age phrasing
// matches what the ribbon shows.
func fmtDeployAge(sec int64) string {
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm", sec/60)
	case sec < 86400:
		return fmt.Sprintf("%dh", sec/3600)
	default:
		return fmt.Sprintf("%dd", sec/86400)
	}
}

// rootCauseExplainProse is the shared body of the two narration handlers:
// it loads the PERSISTED hypothesis for the anchor (never re-synthesizes —
// the worker owns synthesis), refuses to fabricate when none exists, routes
// the compact prompt through s.copilotExplain (the /ai-attribution wrapper,
// NEVER copilot.Explain direct — make audit CHECK 4), and caches the prose
// keyed on the anchor id + the hypothesis VERSION so a re-synthesis (which
// bumps the version) invalidates the cache and we never serve stale prose for
// a changed ranking. Read-only, viewer-readable; no audit (the copilotExplain
// wrapper records the ai_calls row for /ai attribution).
func (s *Server) rootCauseExplainProse(w http.ResponseWriter, r *http.Request, anchorKind string) {
	if !s.copilot.Active() {
		http.Error(w, "AI copilot not available (disabled or not configured)", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "anchor id required", http.StatusBadRequest)
		return
	}
	// Load OUTSIDE the cache so a missing hypothesis is an honest 404 (not a
	// cached empty body) and so the cache key can include the version — we must
	// read the row to know it. Cheap: small FINAL state-table read keyed on the
	// (anchor_kind, anchor_id) ORDER BY.
	h, err := s.store.GetHypothesis(r.Context(), anchorKind, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if h == nil {
		// No persisted hypothesis yet — do NOT fabricate one. The worker
		// synthesizes on a tick; the operator sees an honest empty state.
		http.Error(w, "no hypothesis synthesized for this anchor yet", http.StatusNotFound)
		return
	}

	// Cache keyed on anchor id + hypothesis VERSION: a re-synthesis bumps the
	// version (ReplacingMergeTree DEFAULT stamps a monotonic ns), so the key
	// changes and we never serve prose for a stale ranking. Copilot calls are
	// expensive — a 10m TTL lets concurrent triage clicks share one trip while
	// the version guarantees freshness on re-rank.
	key := fmt.Sprintf("rootcause-explain:%s:%s:%d", anchorKind, id, h.Version)
	s.serveCached(w, r, key, 10*time.Minute, func(ctx context.Context) (any, error) {
		prose, err := s.copilotExplain(r, copilot.SystemPromptRootCauseNarration(), buildRootCausePrompt(h))
		if err != nil {
			return nil, err
		}
		return map[string]string{"prose": prose}, nil
	})
}

// getAnomalyRootCauseExplain narrates the PERSISTED anomaly hypothesis as
// operator-readable prose (rc #4). GET /api/anomalies/{id}/rootcause/explain —
// opt-in (the frontend ✨ Explain button fetches it lazily on click, never on
// mount/expand: Copilot calls cost). Viewer-readable; no audit.
func (s *Server) getAnomalyRootCauseExplain(w http.ResponseWriter, r *http.Request) {
	s.rootCauseExplainProse(w, r, "anomaly")
}

// getProblemRootCauseExplain is the problem-anchored sibling — same builder,
// same wrapper, same version-keyed cache, only the anchor kind differs. The
// hypothesis for a problem is synthesized by the SAME worker tick, so the
// prompt + prose path are identical. GET /api/problems/{id}/rootcause/explain.
func (s *Server) getProblemRootCauseExplain(w http.ResponseWriter, r *http.Request) {
	s.rootCauseExplainProse(w, r, "problem")
}
