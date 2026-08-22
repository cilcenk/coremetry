package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
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
	// Hypothesis (v0.9.1066, Faz 3.1 / K6+K7) — sentezleyicinin kalıcı
	// hipotezi TEK pakette: adaylar+gerekçeler, deploy (ölçülmüş
	// etkisiyle), temsilî trace ve Deep (P1/deploy soruşturması +
	// "neye bakıldı" denetim izi). Eskiden ÜÇ paralel hat vardı ve
	// hiçbir yüzey üçünü birlikte göstermiyordu; en zengin otomatik
	// kanıt (DeepEvidence) operatöre hiç inmiyordu. nil = worker henüz
	// sentezlemedi (soft-fail).
	Hypothesis *chstore.RootCauseHypothesis `json:"hypothesis,omitempty"`
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

// rootcauseCacheKey — v0.9.1082 regresyon yüzeyi: anahtar YALNIZ
// problemin kimliğinden türetilir, saatten ASLA. Saat-türevli bir
// bileşen (eski end.Truncate(minute)) hesap süresi TTL'e yaklaştığında
// cache'i ebedi soğuğa düşürür.
func rootcauseCacheKey(id string, startedNs int64, resolvedNs *int64) string {
	res := int64(0)
	if resolvedNs != nil {
		res = *resolvedNs
	}
	return fmt.Sprintf("rootcause:%s:%d:%d", id, startedNs, res)
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
	case "trace_op_latency": // v0.9.1064 — gecikme sıçraması → en yavaş trace
		return chstore.ExemplarSlow
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

	// v0.9.1082 — anahtar SABİT (id + started + resolved), dakika DEĞİL.
	// Eski anahtar end.Truncate(minute) taşıyordu; hesap ~40s sürünce her
	// dakika yeni anahtar = EBEDİ SOĞUK — serveCached'in stale-while-
	// revalidate mekanizması (v0.8.471) hiç devreye giremiyordu (ölçüm
	// 2026-08-16: aynı probleme ardışık iki tık 34s + 45s). Tazelik artık
	// TTL+stale'in işi; pencere (end=now) closure İÇİNDE hesaplanır ki
	// arka plan tazelemesi donmuş bir end kullanmasın.
	key := rootcauseCacheKey(id, p.StartedAt, p.ResolvedAt)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
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
			// cs != nil ŞART: nil atamak handler'ın başta koyduğu `[]`
			// zarfını EZER ve JSON `"correlations": null` çıkar; panel
			// `.correlations.filter` derken çöker (v0.9.836, bubbleUp
			// ile aynı sınıf).
			// v0.9.1062 (Faz 2.1) — MV sürümü: pencereler ≥5dk, aggregate
			// sabit (invariant #3); ham spans taraması tık-yolundan kalktı.
			if cs, e := s.store.GetCorrelatedChangesMV(ctx, started, windowSec, windowSec*4); e == nil && cs != nil {
				out.Correlations = cs
			}
		}()
		// (b) Blast radius — who calls this service + how many are cascading.
		wg.Add(1)
		go func() {
			defer wg.Done()
			// v0.9.1047 (Faz 0.1) — problemin GERÇEK penceresi geçer;
			// süre geçmek çözülmüş problemde son N dakikayı okutuyordu.
			if br, e := s.store.GetServiceBlastRadius(ctx, p.Service, started, end); e == nil {
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
		// (e) Hipotez — kalıcı satırdan tek FINAL okuma (v0.9.1066).
		wg.Add(1)
		go func() {
			defer wg.Done()
			if h, e := s.store.GetHypothesis(ctx, "problem", p.ID); e == nil && h != nil {
				out.Hypothesis = h
			}
		}()
		// (d) Dimension bubble-up. HATA problemlerinde aynı-pencere
		// alt-küme kıyası (selection = hatalı span'ler — eski davranış
		// bayt-bayt). v0.9.1063 (Faz 2.2 / K3): GECİKME/diğer ailelerde
		// artık ZAMAN-KAYDIRMALI kıyas koşar — baseline ÖNCEKİ eş-boy
		// pencere, selection incident penceresi, filtre iki tarafta aynı
		// (yalnız servis): "hangi attribute değeri önceki pencereye göre
		// patladı". "Slow temiz FilterExpr alt-kümesi değil" engeli buydu;
		// kıyası alt-kümeyle değil pencereyle kurunca ortadan kalkıyor.
		wg.Add(1)
		go func() {
			defer wg.Done()
			baseline := []chstore.FilterExpr{{Key: "service.name", Op: "=", Values: []string{p.Service}}}
			if exemplarKindForMetric(p.Metric) == chstore.ExemplarError {
				selection := []chstore.FilterExpr{
					{Key: "service.name", Op: "=", Values: []string{p.Service}},
					{Key: "status_code", Op: "=", Values: []string{"error"}},
				}
				if bu, e := s.store.BubbleUp(ctx, baseline, selection, started, end, started, end); e == nil {
					out.BubbleUp = bu
				}
				return
			}
			priorFrom := started.Add(-end.Sub(started))
			if bu, e := s.store.BubbleUp(ctx, baseline, nil, priorFrom, started, started, end); e == nil {
				out.BubbleUp = bu
			}
		}()
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

	// v0.9.1082 — anahtar SABİT (id + started); problem ucundaki dakika-
	// anahtarı düzeltmesinin ikizi. Eski anahtar LastSeen dakikasını
	// taşıyordu: AKTİF anomalide dedektör LastSeen'i her geçişte ilerletir,
	// anahtar döner, ~40s'lik hesap her tıkta soğuk koşardı. LastSeen artık
	// closure İÇİNDE taze okunur (arka plan tazelemesi de güncel pencereyi
	// görsün); okuma düşerse yakalanan satır kullanılır.
	key := fmt.Sprintf("anomaly-rootcause:%s:%d", id, ev.StartedAt)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		if fresh, e := s.store.GetAnomalyEvent(ctx, id, 0); e == nil && fresh != nil {
			ev = fresh
		}
		started := time.Unix(0, ev.StartedAt)
		end := time.Unix(0, ev.LastSeen)
		// LastSeen can equal or (on a clock skew) precede StartedAt for a
		// just-recorded event; boundAnalysisWindow floors the span to 10m from
		// the start, so the window is always well-formed [started, started+≥10m].
		started, end = boundAnalysisWindow(started, end)
		windowSec := int(end.Sub(started).Seconds())
		exKind := exemplarKindForAnomaly(ev.Kind)
		out := AnomalyRootCause{
			RootCause: RootCause{
				ProblemID:    "", // anomaly-anchored — no parent Problem
				Service:      ev.Service,
				Metric:       ev.Kind, // shared-render label: the anomaly kind
				StartedAt:    ev.StartedAt,
				FromNs:       started.UnixNano(),
				ToNs:         end.UnixNano(),
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
			// cs != nil ŞART: nil atamak handler'ın başta koyduğu `[]`
			// zarfını EZER ve JSON `"correlations": null` çıkar; panel
			// `.correlations.filter` derken çöker (v0.9.836, bubbleUp
			// ile aynı sınıf).
			// v0.9.1062 (Faz 2.1) — MV sürümü: pencereler ≥5dk, aggregate
			// sabit (invariant #3); ham spans taraması tık-yolundan kalktı.
			if cs, e := s.store.GetCorrelatedChangesMV(ctx, started, windowSec, windowSec*4); e == nil && cs != nil {
				out.Correlations = cs
			}
		}()
		// (b) Blast radius — who calls this service + how many are cascading.
		wg.Add(1)
		go func() {
			defer wg.Done()
			// v0.9.1047 (Faz 0.1) — olayın gerçek penceresi geçer.
			if br, e := s.store.GetServiceBlastRadius(ctx, ev.Service, started, end); e == nil {
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
		// (e) Hipotez — anomali anchor'ının kalıcı satırı (v0.9.1066).
		wg.Add(1)
		go func() {
			defer wg.Done()
			if h, e := s.store.GetHypothesis(ctx, "anomaly", ev.ID); e == nil && h != nil {
				out.Hypothesis = h
			}
		}()
		// (d) Dimension bubble-up. trace_op anomalilerinde aynı-pencere
		// hata alt-kümesi (eski davranış bayt-bayt). v0.9.1063 (Faz 2.2):
		// log anomalileri de artık kapsamda — zaman-kaydırmalı kıyas
		// (baseline önceki eş-boy pencere) span-status alt-kümesi
		// istemez; "bu pencerede hangi attribute patladı" sorusuna
		// log-anchor'da da cevap var.
		wg.Add(1)
		go func() {
			defer wg.Done()
			baseline := []chstore.FilterExpr{{Key: "service.name", Op: "=", Values: []string{ev.Service}}}
			if exKind == chstore.ExemplarError {
				selection := []chstore.FilterExpr{
					{Key: "service.name", Op: "=", Values: []string{ev.Service}},
					{Key: "status_code", Op: "=", Values: []string{"error"}},
				}
				if bu, e := s.store.BubbleUp(ctx, baseline, selection, started, end, started, end); e == nil {
					out.BubbleUp = bu
				}
				return
			}
			priorFrom := started.Add(-end.Sub(started))
			if bu, e := s.store.BubbleUp(ctx, baseline, nil, priorFrom, started, started, end); e == nil {
				out.BubbleUp = bu
			}
		}()
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

	// v0.9.576 — ankorun GERÇEK başlangıcı.
	//
	// Etki penceresi h.ComputedAt'ten kuruluyordu ve o, hipotezin
	// SENTEZLENDİĞİ an — olayın başladığı an DEĞİL. İşçi tik başına
	// koşuyor, yani pencere olayın ilk dakikalarını (en yoğun,
	// tanıya en yakın kısmını) kaçırıyordu; sentez geç kaldıysa
	// pencere tamamen kayıyordu.
	//
	// Ek maliyet küçük: ID'ye anahtarlı tek FINAL state okuması,
	// hipotez okumasıyla aynı sınıf. Çözülemezse 0 geçilir ve
	// rcaImpactWindow zaten tutarsız pencereyi reddeder (ölçüm
	// yapılmaz, uydurulmaz).
	var anchorStartNs int64
	if anchorKind == "problem" {
		if p, err := s.store.GetProblem(r.Context(), id); err == nil && p != nil {
			anchorStartNs = p.StartedAt
		}
	} else if ev, err := s.store.GetAnomalyEvent(r.Context(), id, time.Hour); err == nil && ev != nil {
		anchorStartNs = ev.StartedAt
	}

	// Cache keyed on anchor id + hypothesis VERSION: a re-synthesis bumps the
	// version (ReplacingMergeTree DEFAULT stamps a monotonic ns), so the key
	// changes and we never serve prose for a stale ranking. Copilot calls are
	// expensive — a 10m TTL lets concurrent triage clicks share one trip while
	// the version guarantees freshness on re-rank.
	key := rootcauseExplainCacheKey(anchorKind, id, h.Version)
	// v0.9.557 — kimlik closure DIŞINDA yakalanır.
	//
	// Öncesi: closure `s.copilotExplain(r, ...)` çağırıyordu, yani
	// serveCached'in VERDİĞİ ctx'i yok sayıp isteğin context'ini
	// kullanıyordu. Miss yolunda bu zararsız; SWR ARKA PLAN TAZELEME
	// yolunda değil: refreshKey taze bir context.Background() verir
	// (cache.go:349) çünkü istek çoktan bitmiştir — closure hâlâ ölü
	// r.Context()'i kullanınca her tazeleme context.Canceled ile
	// düşüyordu.
	//
	// Bu hata bu kod tabanında BİR KEZ düzeltilmişti: cache.go:351-354
	// yorumu v0.8.319'da aynı sınıfı anlatıyor ("fn() used to close
	// over the (already cancelled) request context"). Düzeltme
	// serveCached'in kendisine yapılmıştı; bu çağrı noktası ctx'i
	// kullanmadığı için düzeltmenin dışında kalmıştı.
	//
	// Görünen belirti sinsiydi: 10dk TTL + staleFactor 3 ⇒ 10-30dk
	// arası her istek bir tazeleme tetikler, hepsi başarısız olur,
	// bayat metin sunulmaya devam eder ve 30dk dolunca operatör tam
	// bir LLM beklemesi öder. Yani "hızlı ama bayat" ile "yavaş"
	// arasında salınıyordu, hiç düzgün tazelenmiyordu.
	claims := auth.FromContext(r.Context())
	uid, email := "", ""
	if claims != nil {
		uid, email = claims.UserID, claims.Email
	}
	s.serveCached(w, r, key, 10*time.Minute, func(ctx context.Context) (any, error) {
		// v0.9.591 — verdict'e KARARLI BİR KİMLİK.
		//
		// Öncesinde verdict'in hiçbir kimliği yoktu: istek başına
		// üretiliyor, hiçbir yere yazılmıyor, ai_calls.id istemciye
		// dönmüyordu. Dolayısıyla operatör "bu değerlendirme yanlış"
		// diyemiyordu — diyecek yer yoktu.
		//
		// exchangeID depoda ZATEN ÇALIŞAN geri bildirim rayının
		// anahtarı (v0.8.399: ai_calls.exchange_id ↔ ai_feedback,
		// POST /api/ai/feedback, /ai memnuniyet paneli). CallMeta'da
		// alan hep vardı; yalnız chat handler'ı dolduruyordu, bu yol
		// rayın dışında kalmıştı. Yeni ray kurmuyoruz, var olana
		// biniyoruz.
		//
		// Kimlik ÖNBELLEĞİN İÇİNDE basılıyor ve bu şart: serveCached
		// gövdeyi olduğu gibi saklar, yani kimlik gövdeyle birlikte
		// yaşamalı. Dışarıda bassaydık her istek yeni bir kimlik
		// üretir, oysa önbellekten sunulan cevap AYNI cevaptır —
		// kimlik cevabı tanımlar, isteği değil.
		exchangeID := newRandID(16)
		ctx = copilot.WithMeta(ctx, copilot.CallMeta{
			UserID: uid, UserEmail: email, ExchangeID: exchangeID,
		})
		// v0.9.559 — düzyazı anlatım yerine KALKANLI VERDICT.
		//
		// Tek LLM çağrısı: verdict hem yapılandırılmış kararı hem
		// operatörün okuduğu özeti taşıyor. İki ayrı çağrı yapmak
		// (anlatım + verdict) maliyeti ikiye katlar ve ikisinin
		// birbiriyle çelişme ihtimalini açardı.
		//
		// `prose` alanı KORUNUYOR ve nil OLABİLİYOR: frontend'in
		// "anlatım yok" dalı ulaşılabilir kalmalı. Model çözümlenemezse
		// deterministik cümle verdict.summary'ye yazılır, prose'a
		// DEĞİL — aksi hâlde yedek cümle gerçek LLM anlatımıyla aynı
		// kutuda çizilir ve operatör ayırt edemez.
		verdict, prose := s.buildRCAVerdict(ctx, h, anchorStartNs)
		// Kalıcılaştır: operatöre GÖSTERİLEN karar (kalkanlar
		// sonrası). ai_calls.response_sample modelin HAM çıktısını
		// taşıyor — aradaki farkı kalkanlar üretiyor ve o fark
		// hiçbir yerde kalmıyordu.
		//
		// En iyi çaba: yazım hatası cevabı DÜŞÜREMEZ. Operatörün
		// beklediği tanı, bizim muhasebemiz yüzünden kaybolmaz.
		//
		// v0.9.1281 — GÖVDE de kayda giriyor, kaynak 'operator': bu yol
		// YALNIZ ✨ Explain tıklamasında koşar. Otomatik üretim
		// (rca_auto_verdict.go) aynı kaydı 'auto' ile yazar; ikisi
		// kalite ölçümünde ayrılabilsin diye etiketleri ayrı.
		s.recordRCAVerdict(ctx, exchangeID, anchorKind, id,
			chstore.RCAVerdictSourceOperator, h, verdict, prose)
		return rcaExplainResponse{
			Prose: prose, Verdict: verdict, ExchangeID: exchangeID,
		}, nil
	})
}

// ── Kalıcı verdict okuma (v0.9.1281) ─────────────────────────────────

// PersistedRCAVerdict — rca_verdicts satırının operatöre dönen şekli.
//
// RCAVerdict'in KOPYASI DEĞİL ve olmaması bilinçli: kalıcı satır kararın
// ÖZETİNİ taşıyor (enum, imza, güven, kalkan notları, gövde), tam
// yapılandırılmış nesneyi değil (nedensel zincir, reddedilen hipotezler,
// kanıt referansları). Aynı tipi kullanmak, doldurulmamış alanları "yok"
// diye gösterirdi — oysa onlar KAYDEDİLMEDİ. Ayrı tip, farkı görünür
// kılıyor ve frontend'i dürüst bir "kalıcı kayıt" rozetine zorluyor.
type PersistedRCAVerdict struct {
	ExchangeID           string   `json:"exchangeId,omitempty"`
	Verdict              string   `json:"verdict"`
	Body                 string   `json:"body,omitempty"`
	Source               string   `json:"source,omitempty"`
	Service              string   `json:"service,omitempty"`
	RootCauseEntity      string   `json:"rootCauseEntity,omitempty"`
	RootCauseFailureMode string   `json:"rootCauseFailureMode,omitempty"`
	Confidence           float64  `json:"confidence"`
	ShieldNotes          []string `json:"shieldNotes,omitempty"`
	CreatedAt            int64    `json:"createdAt"`
}

// persistedRCAVerdictResponse — bulunamadı DURUMU açıkça taşınır.
//
// 404 yerine found=false: yokluk bu uçta NORMAL hâl (henüz üretilmemiş
// bir ankor), hata değil. 404 dönseydi frontend'in catch dalı gerçek
// hatalarla yokluğu ayırt edemezdi ve önbellek negatif sonucu tutamazdı.
type persistedRCAVerdictResponse struct {
	Found   bool                 `json:"found"`
	Verdict *PersistedRCAVerdict `json:"verdict,omitempty"`
}

// getRootCauseVerdict — bir ankorun EN SON kalıcı verdict kaydı.
// GET /api/rootcause/verdict?anchorKind=problem|anomaly&anchorId=…
//
// ÜRETİMSİZ: model çağırmaz, yalnız satır okur. Bu yüzden A2 kararını
// (kart/panel otomatik LLM ateşlemez) ihlal etmez — ✨ Explain'in
// aksine bu uç bedava.
//
// Var olma sebebi: verdict metni 10dk'lık explain önbelleğinden düştüğü
// anda operatör için hiçbir yerde kalmıyordu. Panel artık ıskada buraya
// düşüyor ve "kalıcı kayıt" olduğunu SÖYLÜYOR.
//
// Viewer-okunur, denetim izi yok (salt okuma — yazan uç değil).
func (s *Server) getRootCauseVerdict(w http.ResponseWriter, r *http.Request) {
	anchorKind := strings.TrimSpace(r.URL.Query().Get("anchorKind"))
	anchorID := strings.TrimSpace(r.URL.Query().Get("anchorId"))
	// Whitelist: anchor_kind LowCardinality bir kolon ve serbest bir dize
	// hem önbellek anahtarını hem kolonun sözlüğünü kirletirdi.
	if anchorKind != "problem" && anchorKind != "anomaly" {
		http.Error(w, "anchorKind must be problem or anomaly", http.StatusBadRequest)
		return
	}
	if anchorID == "" {
		http.Error(w, "anchorId required", http.StatusBadRequest)
		return
	}
	// Anahtar HER İKİ girdiyi de taşıyor (v0.5.187 dersi: uzunluk/sayı
	// digest'i çapraz zehirler). İkisi de sınırlı dize, küme yok — sıralı
	// FNV gerektiren bir girdi bu uçta bulunmuyor.
	key := fmt.Sprintf("rootcause-verdict:%s:%s", anchorKind, anchorID)
	// 60s: satır ya arka plan işçisinin tikiyle (30s) ya da bir ✨
	// tıklamasıyla değişir. Daha uzun bir TTL, yeni inen bir verdict'i
	// operatörden dakikalarca saklardı.
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		rec, err := s.store.LatestRCAVerdictForAnchor(ctx, anchorKind, anchorID)
		if err != nil {
			return nil, err
		}
		if rec == nil {
			return persistedRCAVerdictResponse{Found: false}, nil
		}
		return persistedRCAVerdictResponse{
			Found: true,
			Verdict: &PersistedRCAVerdict{
				ExchangeID:           rec.ExchangeID,
				Verdict:              rec.Verdict,
				Body:                 rec.Body,
				Source:               rec.Source,
				Service:              rec.Service,
				RootCauseEntity:      rec.RCEntity,
				RootCauseFailureMode: rec.RCFailMode,
				Confidence:           rec.Confidence,
				ShieldNotes:          rec.ShieldNotes,
				CreatedAt:            rec.CreatedAt,
			},
		}, nil
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

// rcaExplainResponse — /rootcause/explain gövdesi (v0.9.559).
//
// `prose` sözleşmesi KORUNDU (frontend ham cast ediyor, fazladan alan
// yok sayılır) ama artık POINTER: nil ⇒ JSON'da null ⇒ frontend'in
// dürüst "anlatım yok" dalı çalışır. Eskiden düz string'di ve hep
// doluydu; verdict düşüşünde doldurmaya devam etseydik o dal ölürdü.
type rcaExplainResponse struct {
	Prose   *string     `json:"prose"`
	Verdict *RCAVerdict `json:"verdict,omitempty"`

	// ExchangeID (v0.9.591) — bu CEVABIN kimliği; 👍/👎 bununla
	// POST /api/ai/feedback'e gider.
	//
	// Cevabı tanımlar, isteği değil: önbellekten sunulan aynı cevap
	// aynı kimliği taşır. Sonucu, aynı verdict'i iki operatör oylarsa
	// ai_feedback'in ORDER BY exchange_id dedup'ı gereği SON yazan
	// kazanır. Bilinçli kabul: aynı cevaba tek oy. Düzeltmek ORDER BY
	// değişikliği isterdi, o da yeni tablo + backfill demek.
	ExchangeID string `json:"exchangeId,omitempty"`
}
