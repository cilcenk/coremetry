package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// Per-entity AI analysis (v0.8.85). SINGLE-SHOT, provider-neutral — the model
// is WHATEVER the operator configured in Settings (no built-in/local model).
// The server does all the filtering / aggregation / correlation in Go and feeds
// the model only a small, clean SUMMARY (RED + baseline + top errors + deploys +
// neighbours) — never raw spans/logs. The model is purely the language layer:
// it reads the Turkish summary and returns a strict Turkish JSON verdict
// {ozet, olasi_neden, kanit[], oneriler[], guven}. Embedded as an
// "AI ile analiz et" button on a service / incident / error-group (NOT logs).

// serviceAnalysisPrompt — operator-authored Turkish analyst instruction with
// ONE full few-shot (Turkish summary → Turkish structured output). The model
// must answer ONLY in the JSON shape below.
const serviceAnalysisPrompt = `Sen Coremetry'nin servis analiz motorusun. Sana TEK bir servisin
özetlenmiş observability verisi verilir (RED metrikleri, baseline karşılaştırması,
en sık hatalar, deploy işaretçileri, bağımlı servisler). Görevin: bu veriye
dayanarak servisin durumunu yorumlamak ve kök-neden + öneri üretmek.

KURALLAR:
- Sadece VERİLEN veriye dayan. Veride olmayan servis adı veya sayı UYDURMA.
- Her zaman Türkçe yanıt ver.
- latency, span, deadlock, timeout, p99 gibi teknik terimleri ÇEVİRME.
- "kanit" maddeleri verideki somut metrik/sayıya atıfta bulunsun.
- Veri yetersizse "guven" değerini "dusuk" yap ve bunu özet'te belirt.
- Çıktıyı SADECE aşağıdaki JSON formatında ver, başka hiçbir şey yazma.

ÇIKTI FORMATI:
{
  "ozet": "<2-3 cümle servis durumu>",
  "olasi_neden": "<en olası kök neden>",
  "kanit": ["<somut metrik/sayı kanıtı>", "..."],
  "oneriler": ["<aksiyon 1>", "<aksiyon 2>"],
  "guven": "yuksek" | "orta" | "dusuk"
}

ÖRNEK GİRDİ:
Servis: payment-service (son 30 dakika)
RED: rate=42.0 req/s, error=8.30% (1240 hata), p50=85ms, p95=410ms, p99=1850ms
Baseline (önceki 30 dk): error=0.40%, p99=210ms
En sık hatalar: SQLTimeoutException ×980, HttpServerErrorException ×210
Deploy: v1.4.0 (12 dk önce)
Bağımlılıklar: downstream → ledger-service, auth-service

ÖRNEK ÇIKTI:
{
  "ozet": "payment-service son 30 dakikada ciddi bozulma yaşıyor. error %0.40'tan %8.30'a, p99 210ms'den 1850ms'ye çıktı. Artış v1.4.0 deploy'u ile başladı.",
  "olasi_neden": "v1.4.0 deploy'u sonrası ledger-service çağrılarında SQLTimeoutException; downstream DB lock contention p99'u ~9x artırdı.",
  "kanit": ["error_rate %0.40 → %8.30", "p99 210ms → 1850ms", "SQLTimeoutException ×980 baskın hata", "v1.4.0 deploy 12 dk önce"],
  "oneriler": ["v1.4.0'ı geri al veya ledger-service DB bağlantı havuzunu incele", "SQLTimeoutException örnek trace'ini aç ve yavaş sorguyu bul"],
  "guven": "yuksek"
}`

// serviceAnalysis mirrors the required JSON so the server validates + hands the
// frontend a typed object (raw text as fallback).
type serviceAnalysis struct {
	Ozet       string   `json:"ozet"`
	OlasiNeden string   `json:"olasi_neden"`
	Kanit      []string `json:"kanit"`
	Oneriler   []string `json:"oneriler"`
	Guven      string   `json:"guven"`
}

// ── Context (the summary the model sees) — also returned to the UI for the
// "Bağlamı gör" panel and used for the post-check. ───────────────────────────

type aiRED struct {
	Spans      uint64  `json:"spans"`
	Rate       float64 `json:"rate"` // req/s
	ErrorRate  float64 `json:"errorRate"`
	ErrorCount uint64  `json:"errorCount"`
	AvgMs      float64 `json:"avgMs"`
	P50Ms      float64 `json:"p50Ms"`
	P95Ms      float64 `json:"p95Ms"`
	P99Ms      float64 `json:"p99Ms"`
}

type aiErrCount struct {
	Type          string `json:"type"`
	Message       string `json:"message"`
	Service       string `json:"service"`
	Count         uint64 `json:"count"`
	SampleTraceID string `json:"sampleTraceId"`
}

type aiDeploy struct {
	Version    string `json:"version"`
	TimeUnixNs int64  `json:"timeUnixNs"`
}

type aiServiceContext struct {
	Service    string       `json:"service"`
	RangeS     int64        `json:"rangeS"`
	Current    aiRED        `json:"current"`
	Baseline   aiRED        `json:"baseline"`
	TopErrors  []aiErrCount `json:"topErrors"`
	Deploys    []aiDeploy   `json:"deploys"`
	Upstream   []string     `json:"upstream"`
	Downstream []string     `json:"downstream"`
}

// aiAnalyzeResponse is the endpoint payload.
type aiAnalyzeResponse struct {
	Analysis  *serviceAnalysis  `json:"analysis"`
	Context   *aiServiceContext `json:"context"`
	Raw       string            `json:"raw"`
	Parsed    bool              `json:"parsed"`
	PostCheck *aiPostCheck      `json:"postCheck"`
	Cached    bool              `json:"cached"`
}

type aiPostCheck struct {
	Verified        bool     `json:"verified"`
	UnknownServices []string `json:"unknownServices"`
	Note            string   `json:"note"`
}

// copilotAnalyzeService runs the per-service single-shot analysis. Read-only;
// any authenticated user. Cached in Redis for 5 minutes per (service, rangeS).
func (s *Server) copilotAnalyzeService(w http.ResponseWriter, r *http.Request) {
	if s.copilot == nil || !s.copilot.Active() {
		http.Error(w, `{"error":"AI copilot not available (disabled or not configured)"}`, http.StatusServiceUnavailable)
		return
	}
	service := strings.TrimSpace(r.URL.Query().Get("service"))
	if service == "" {
		http.Error(w, `{"error":"service required"}`, http.StatusBadRequest)
		return
	}
	rangeS := int64(parseInt(r.URL.Query().Get("rangeS"), 1800))
	if rangeS <= 0 || rangeS > 7*24*3600 {
		rangeS = 1800
	}

	// Cache: short TTL per (service, rangeS). Stale is acceptable (Redis pure
	// cache). ?refresh=1 bypasses the read.
	cacheKey := fmt.Sprintf("aianalyze:svc=%s:r=%d", service, rangeS)
	if r.URL.Query().Get("refresh") != "1" {
		if b, ok, _ := s.cache.Get(r.Context(), cacheKey); ok && len(b) > 0 {
			var cached aiAnalyzeResponse
			if json.Unmarshal(b, &cached) == nil {
				cached.Cached = true
				writeJSON(w, cached)
				return
			}
		}
	}

	to := time.Now()
	from := to.Add(-time.Duration(rangeS) * time.Second)

	cx := s.buildServiceContext(r.Context(), service, from, to)
	if cx.Current.Spans == 0 {
		writeJSON(w, aiAnalyzeResponse{Context: cx, Parsed: false, Raw: "", Analysis: nil})
		return
	}
	snapshot := renderServiceSnapshot(cx)

	// Single-shot through the /ai-attributed wrapper (CLAUDE.md: never call
	// s.copilot.Explain direct). Provider-neutral — uses the configured model.
	raw, err := s.copilotExplain(r, serviceAnalysisPrompt, snapshot)
	if err != nil {
		writeErr(w, err)
		return
	}
	parsed := parseServiceAnalysis(raw)
	resp := aiAnalyzeResponse{
		Analysis: parsed,
		Context:  cx,
		Raw:      raw,
		Parsed:   parsed != nil,
	}
	if parsed != nil {
		resp.PostCheck = postCheckServiceAnalysis(parsed, cx)
	}

	if b, err := json.Marshal(resp); err == nil {
		_ = s.cache.Set(r.Context(), cacheKey, b, 5*time.Minute)
	}
	writeJSON(w, resp)
}

// buildServiceContext gathers + SUMMARISES the per-service signals. All numbers
// are aggregates; no raw spans/logs leave the server.
func (s *Server) buildServiceContext(ctx context.Context, service string, from, to time.Time) *aiServiceContext {
	span := to.Sub(from)
	cx := &aiServiceContext{Service: service, RangeS: int64(span.Seconds())}

	// RED — current window + the immediately-preceding baseline window.
	if rows, err := s.store.GetServiceSummary5m(ctx, service, from, to); err == nil {
		cx.Current = aggRED(rows, span.Seconds())
	}
	if rows, err := s.store.GetServiceSummary5m(ctx, service, from.Add(-span), from); err == nil {
		cx.Baseline = aggRED(rows, span.Seconds())
	}

	// Top error messages (most-frequent first, top 5).
	if errs, err := s.store.GetExceptions(ctx, chstore.ExceptionFilter{
		Service: service, GroupBy: "full", From: from, To: to, Limit: 50,
	}); err == nil {
		sort.Slice(errs, func(i, j int) bool { return errs[i].Count > errs[j].Count })
		for i, e := range errs {
			if i >= 5 {
				break
			}
			cx.TopErrors = append(cx.TopErrors, aiErrCount{
				Type: e.Type, Message: e.Message, Service: e.Service, Count: e.Count, SampleTraceID: e.SampleTraceID,
			})
		}
	}

	// Deploy markers in window.
	if deps, err := s.store.GetServiceDeploys(ctx, service, from, to); err == nil {
		for _, d := range deps {
			cx.Deploys = append(cx.Deploys, aiDeploy{Version: d.Version, TimeUnixNs: d.TimeUnixNs})
		}
	}

	// Dependency chain — 1-hop upstream callers + downstream callees.
	if up, down, _, _, err := s.store.ServiceNeighbors(ctx, service, span, 50); err == nil {
		for _, n := range up {
			cx.Upstream = append(cx.Upstream, n.Service)
		}
		for _, n := range down {
			cx.Downstream = append(cx.Downstream, n.Service)
		}
	}
	return cx
}

// aggRED collapses the 5-min buckets into one window RED summary. Percentiles
// are span-weighted means of the per-bucket percentiles — an approximation, but
// the right granularity for a language-layer summary.
func aggRED(rows []chstore.ServiceSummaryRow, windowSec float64) aiRED {
	var red aiRED
	var wP50, wP95, wP99, wAvg float64
	for _, r := range rows {
		red.Spans += r.SpanCount
		red.ErrorCount += r.ErrorCount
		w := float64(r.SpanCount)
		wP50 += r.P50Ms * w
		wP95 += r.P95Ms * w
		wP99 += r.P99Ms * w
		wAvg += r.AvgMs * w
	}
	if red.Spans > 0 {
		f := float64(red.Spans)
		red.ErrorRate = float64(red.ErrorCount) / f * 100
		red.P50Ms = wP50 / f
		red.P95Ms = wP95 / f
		red.P99Ms = wP99 / f
		red.AvgMs = wAvg / f
	}
	if windowSec > 0 {
		red.Rate = float64(red.Spans) / windowSec
	}
	return red
}

// renderServiceSnapshot formats the context as the compact Turkish summary the
// model reasons over (human-readable; field order + labels affect the output).
func renderServiceSnapshot(cx *aiServiceContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Servis: %s (son %d dakika)\n", cx.Service, cx.RangeS/60)
	c := cx.Current
	fmt.Fprintf(&b, "RED: rate=%.1f req/s, error=%.2f%% (%d hata), p50=%.0fms, p95=%.0fms, p99=%.0fms\n",
		c.Rate, c.ErrorRate, c.ErrorCount, c.P50Ms, c.P95Ms, c.P99Ms)
	bl := cx.Baseline
	if bl.Spans > 0 {
		fmt.Fprintf(&b, "Baseline (önceki %d dk): error=%.2f%%, p99=%.0fms, p50=%.0fms\n",
			cx.RangeS/60, bl.ErrorRate, bl.P99Ms, bl.P50Ms)
	} else {
		b.WriteString("Baseline: önceki pencerede veri yok.\n")
	}
	if len(cx.TopErrors) > 0 {
		b.WriteString("En sık hatalar: ")
		var parts []string
		for _, e := range cx.TopErrors {
			label := e.Type
			if label == "" {
				label = e.Message
			}
			parts = append(parts, fmt.Sprintf("%s ×%d", label, e.Count))
		}
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString("\n")
	}
	if len(cx.Deploys) > 0 {
		var parts []string
		for _, d := range cx.Deploys {
			parts = append(parts, d.Version)
		}
		fmt.Fprintf(&b, "Deploy(lar): %s\n", strings.Join(parts, ", "))
	}
	if len(cx.Upstream) > 0 {
		fmt.Fprintf(&b, "Upstream (çağıranlar): %s\n", strings.Join(cx.Upstream, ", "))
	}
	if len(cx.Downstream) > 0 {
		fmt.Fprintf(&b, "Downstream (bağımlılıklar): %s\n", strings.Join(cx.Downstream, ", "))
	}
	return b.String()
}

// parseServiceAnalysis tolerantly extracts the JSON verdict (same fence-stripping
// as the system analysis). Returns nil on unparseable output.
func parseServiceAnalysis(raw string) *serviceAnalysis {
	t := strings.TrimSpace(raw)
	if strings.HasPrefix(t, "```") {
		t = strings.TrimPrefix(t, "```json")
		t = strings.TrimPrefix(t, "```")
		t = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "```"))
	}
	i := strings.Index(t, "{")
	j := strings.LastIndex(t, "}")
	if i < 0 || j <= i {
		return nil
	}
	var a serviceAnalysis
	if err := json.Unmarshal([]byte(t[i:j+1]), &a); err != nil {
		return nil
	}
	return &a
}

// serviceTokenRe matches hyphenated lowercase service-style identifiers
// (payment-service, openbanking-gateway, mobile-bff). ascii-only, so Turkish
// words with diacritics (kök-neden) never match.
var serviceTokenRe = regexp.MustCompile(`[a-z][a-z0-9]+(?:-[a-z0-9]+)+`)

// nonServiceHyphenated are common hyphenated technical terms that look like a
// service token but aren't — excluded from the hallucination check.
var nonServiceHyphenated = map[string]bool{
	"error-rate": true, "error-count": true, "request-rate": true, "response-time": true,
	"root-cause": true, "status-code": true, "time-range": true, "real-time": true,
	"end-to-end": true, "two-phase": true, "p99-latency": true, "p50-latency": true,
	"baseline-window": true, "trace-id": true, "span-id": true,
}

// postCheckServiceAnalysis verifies the model didn't invent a service name: any
// service-style token in the output that isn't in the gathered context (the
// only data the model saw) is flagged "doğrulanamadı". Numbers are constrained
// by the prompt ("veride olmayan sayı uydurma") and the small snapshot surface.
func postCheckServiceAnalysis(a *serviceAnalysis, cx *aiServiceContext) *aiPostCheck {
	known := map[string]bool{}
	add := func(v string) {
		if v != "" {
			known[strings.ToLower(v)] = true
		}
	}
	add(cx.Service)
	for _, n := range cx.Upstream {
		add(n)
	}
	for _, n := range cx.Downstream {
		add(n)
	}
	for _, d := range cx.Deploys {
		add(d.Version)
	}
	for _, e := range cx.TopErrors {
		add(e.Service)
	}

	seen := map[string]bool{}
	var unknown []string
	scan := func(text string) {
		for _, m := range serviceTokenRe.FindAllString(strings.ToLower(text), -1) {
			if known[m] || seen[m] || nonServiceHyphenated[m] {
				continue
			}
			seen[m] = true
			unknown = append(unknown, m)
		}
	}
	scan(a.Ozet)
	scan(a.OlasiNeden)
	for _, k := range a.Kanit {
		scan(k)
	}
	for _, o := range a.Oneriler {
		scan(o)
	}

	pc := &aiPostCheck{Verified: len(unknown) == 0, UnknownServices: unknown}
	if !pc.Verified {
		pc.Note = "Girdi verisinde olmayan servis adı/adları geçiyor — doğrulanamadı."
	}
	return pc
}
