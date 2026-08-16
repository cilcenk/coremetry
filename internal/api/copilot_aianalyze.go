package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
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
- Girdide KIRILIM (CHANNEL_CODE/FUNCTION_CODE) ya da "Örnek <alan>: <değer>"
  satırı varsa, en az bir kanıt maddesinde ve mümkünse bir öneride bunları
  AYNEN geçir. Sebebi: "hata oranı %14" bir gözlemdir, "hata mobile-app
  kanalında %74 ve örnek request_id 8f3c-…" bir BAŞLANGIÇ NOKTASIDIR —
  operatör onu alıp kendi log'una, kaydına, çağrı merkezine gider.
  Bu değerler sana verildi; UYDURMA, yalnız verilenleri kullan.
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
CHANNEL_CODE kırılımı (en çok hata üreten önce): mobile-app (820 çağrı, 610 hata, %74.4), internet-banking (410 çağrı, 21 hata, %5.1)
Örnek request_id: 8f3c-4a2b-91de, c7b1-05fa-3e22

ÖRNEK ÇIKTI:
{
  "ozet": "payment-service son 30 dakikada ciddi bozulma yaşıyor. error %0.40'tan %8.30'a, p99 210ms'den 1850ms'ye çıktı. Artış v1.4.0 deploy'u ile başladı.",
  "olasi_neden": "v1.4.0 deploy'u sonrası ledger-service çağrılarında SQLTimeoutException; downstream DB lock contention p99'u ~9x artırdı.",
  "kanit": ["error_rate %0.40 → %8.30", "p99 210ms → 1850ms", "SQLTimeoutException ×980 baskın hata", "v1.4.0 deploy 12 dk önce", "hata mobile-app kanalında yoğun: %74.4 (610/820), internet-banking %5.1", "örnek request_id: 8f3c-4a2b-91de"],
  "oneriler": ["v1.4.0'ı geri al veya ledger-service DB bağlantı havuzunu incele", "8f3c-4a2b-91de request_id'siyle mobile-app kanalının log'una bak"],
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
	// v0.9.580 (operatör: "örnek request_id, CHANNEL_CODE değerlerini
	// de söylesin") — cevabı EYLEME dönüştüren somut kimlikler.
	// "Hata oranı %14" bir gözlem; "örnek request_id: 8f3c…, en çok
	// hata üreten CHANNEL_CODE: 0012" bir başlangıç noktası: operatör
	// onu alıp kendi log'una, kaydına, çağrı merkezine gider.
	Business    map[string][]chstore.BusinessSlice `json:"business,omitempty"`
	Correlation []chstore.CorrelationSample        `json:"correlation,omitempty"`
}

// aiAnalyzeResponse is the endpoint payload.
type aiAnalyzeResponse struct {
	Analysis  *serviceAnalysis  `json:"analysis"`
	Context   *aiServiceContext `json:"context"`
	Raw       string            `json:"raw"`
	Parsed    bool              `json:"parsed"`
	PostCheck *aiPostCheck      `json:"postCheck"`
	Cached    bool              `json:"cached"`

	// CorrelationLinks (v0.9.655, operatör: "Request Id bulduğunda
	// prod'ta log izleme linkini de versin parametrik olarak") —
	// örnek korelasyon kimliklerinden DIŞ log sistemine köprüler.
	//
	// Cevap METNİNE değil ayrı bir alana konuyor ve bu bilinçli: metin
	// modelden geçiyor, bir URL'yi modele emanet etmek onu bozma riski.
	// Burası sunucuda deterministik üretiliyor — "Kaynak:" alt
	// bilgisiyle aynı ilke.
	//
	// Şablon yapılandırılmamışsa alan HİÇ gönderilmiyor: kırık bir
	// link, link yokluğundan kötüdür.
	CorrelationLinks []guidedAnswerLink `json:"correlationLinks,omitempty"`

	// ExchangeID (v0.9.593) — bu CEVABIN kimliği; 👍/👎 bununla
	// POST /api/ai/feedback'e gider.
	//
	// Panel operatöre "Bu analiz yararlı mıydı?" diye soruyor ve
	// tıklayınca "Teşekkürler." yazıyordu — ama hiçbir yere
	// yazmıyordu. Kimlik o ölü affordance'ı gerçek yapan parça.
	//
	// Önbelleğe ALINAN gövdenin parçası ve bu doğru: kimlik cevabı
	// tanımlar, isteği değil. Aynı cevabı iki operatör oylarsa
	// ai_feedback dedup'ı gereği son yazan kazanır (RCA yolundaki
	// kabulün aynısı).
	ExchangeID string `json:"exchangeId,omitempty"`
}

type aiPostCheck struct {
	Verified        bool     `json:"verified"`
	UnknownServices []string `json:"unknownServices"`
	Note            string   `json:"note"`
}

// copilotAnalyzeService runs the per-service single-shot analysis. Read-only;
// any authenticated user. Cached in Redis for 5 minutes per (service, rangeS).
func (s *Server) copilotAnalyzeService(w http.ResponseWriter, r *http.Request) {
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

	// v0.9.593 — cevaba kimlik. copilotExplainJSON ctx'teki kimliği
	// taşır (ai_observability.go), böylece ai_calls satırı ve
	// operatörün oyu AYNI anahtarda buluşur.
	exchangeID := newRandID(16)
	r = r.WithContext(copilot.WithMeta(r.Context(),
		copilot.CallMeta{ExchangeID: exchangeID}))

	// Single-shot through the /ai-attributed wrapper (CLAUDE.md: never call
	// s.copilot.Explain direct). Provider-neutral — uses the configured model.
	raw, err := s.copilotExplainJSON(r, serviceAnalysisPrompt, snapshot, serviceAnalysisSchema())
	if err != nil {
		writeErr(w, err)
		return
	}
	parsed := parseServiceAnalysis(raw)
	resp := aiAnalyzeResponse{
		Analysis:   parsed,
		Context:    cx,
		Raw:        raw,
		Parsed:     parsed != nil,
		ExchangeID: exchangeID,
	}
	if parsed != nil {
		resp.PostCheck = postCheckServiceAnalysis(parsed, cx)
	}
	// v0.9.655 — korelasyon kimliklerinden dış log sistemine köprüler.
	// Şablon system_settings'te; yoksa alan hiç gönderilmiyor.
	if cx != nil && len(cx.Correlation) > 0 {
		// Ortam servis adının SONEKİNDEN çözülüyor (-int/-uat/-prep);
		// soneksiz ad prod demek ve "default" şablonunu alıyor.
		resp.CorrelationLinks = correlationLinks(cx.Correlation, service, s.correlationLinkTemplates(r.Context()))
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
	// v0.9.597 — dizi alanları BOŞ DİLİM olarak ilklenir, nil olarak DEĞİL.
	//
	// Go'da nil dilim JSON'a `null` serileşir. TS tipi ise
	// `deploys: AiDeploy[]` diyor — yani NULLABLE DEĞİL. Sözleşme
	// yalandı ve yalan sessiz değildi: ContextView `ctx.deploys.length`
	// okuyor, null'da TypeError atıyor ve route ErrorBoundary'si TÜM
	// Servis Overview sayfasını hata ekranıyla değiştiriyordu — yalnız
	// AI kartını değil.
	//
	// Tetikleyici NADİR DEĞİL, NORMAL: penceresinde deploy olmayan bir
	// servis. 30 dakikalık varsayılan pencerede servislerin çoğu böyle.
	// Aynı hat hatasız serviste topErrors, izole serviste upstream/
	// downstream için de geçerliydi.
	//
	// omitempty ÇÖZÜM DEĞİL: alanı tamamen düşürür, TS'te `undefined`
	// olur ve `.length` yine patlar — daha sinsi bir biçimde.
	cx := &aiServiceContext{
		Service: service, RangeS: int64(span.Seconds()),
		TopErrors: []aiErrCount{}, Deploys: []aiDeploy{},
		Upstream: []string{}, Downstream: []string{},
	}

	// RED — current window + the immediately-preceding baseline window.
	if rows, err := s.store.GetServiceSummary5m(ctx, service, from, to); err == nil {
		cx.Current = aggRED(rows, span.Seconds())
	}
	if rows, err := s.store.GetServiceSummary5m(ctx, service, from.Add(-span), from); err == nil {
		cx.Baseline = aggRED(rows, span.Seconds())
	}

	// v0.9.580 — iş boyutu kırılımı + örnek istek kimlikleri. İkisi de
	// soft-fail: eksik olmaları cevabı engellemez, yalnız somutluğunu
	// azaltır (aynı desen: RED, deploys, topology).
	for _, key := range []string{"CHANNEL_CODE", "FUNCTION_CODE"} {
		if sl, err := s.store.BusinessBreakdown(ctx, service, key, from, to); err == nil && len(sl) > 0 {
			if cx.Business == nil {
				cx.Business = map[string][]chstore.BusinessSlice{}
			}
			cx.Business[key] = sl
		}
	}
	if cs, err := s.store.SampleCorrelationIDs(ctx, service, from, to); err == nil {
		cx.Correlation = cs
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
	// v0.9.580 — somut kimlikler. Model bunları YORUMLAR, üretmez:
	// değerler doğrudan ClickHouse'tan geliyor ve prompt'a olduğu gibi
	// basılıyor, yani uydurulamaz.
	for _, key := range []string{"CHANNEL_CODE", "FUNCTION_CODE"} {
		sl := cx.Business[key]
		if len(sl) == 0 {
			continue
		}
		var parts []string
		for i, v := range sl {
			if i >= 5 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s (%d çağrı, %d hata, %%%.1f)",
				v.Value, v.Calls, v.Errors, v.ErrPct))
		}
		fmt.Fprintf(&b, "%s kırılımı (en çok hata üreten önce): %s\n", key, strings.Join(parts, ", "))
	}
	for _, c := range cx.Correlation {
		fmt.Fprintf(&b, "Örnek %s: %s\n", c.Key, strings.Join(c.Values, ", "))
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

// ── Operation-level context (v0.9.184) — the operasyon twin of the
// service context. CoSRE answers "GET /orders nasıl" / "bu operasyonun
// durumu" by scoping RED to a single span name. All numbers come from
// operation_summary_5m via ONE GetOperationSummaryCompared call, which
// carries both the current window AND the prior-equal-window baseline
// (Prior* fields) — so unlike buildServiceContext we don't run two
// aggregate reads. No raw spans leave the server. ───────────────────

type aiOperationContext struct {
	Service     string     `json:"service"`
	Operation   string     `json:"operation"`
	RangeS      int64      `json:"rangeS"`
	Current     aiRED      `json:"current"`
	Baseline    aiRED      `json:"baseline"`
	HasBaseline bool       `json:"hasBaseline"`
	Apdex       float64    `json:"apdex"`
	Deploys     []aiDeploy `json:"deploys"`
}

// buildOperationContext summarises a single (service, operation) RED
// window + its baseline from operation_summary_5m. operation is the raw
// span name (normalized=false) so it matches the frontend `?op=` value
// and the chart's `name = "..."` DSL.
func (s *Server) buildOperationContext(ctx context.Context, service, operation string, from, to time.Time) *aiOperationContext {
	span := to.Sub(from)
	winSec := span.Seconds()
	cx := &aiOperationContext{Service: service, Operation: operation, RangeS: int64(winSec)}

	// env "" — AI analysis explains the whole service, env-agnostic.
	rows, err := s.store.GetOperationSummaryCompared(ctx, service, 0, from, to, false, "")
	if err != nil {
		return cx
	}
	var row *chstore.OperationSummary
	for i := range rows {
		if rows[i].Name == operation {
			row = &rows[i]
			break
		}
	}
	if row == nil {
		return cx
	}
	cx.Current = aiRED{
		Spans: row.SpanCount, ErrorCount: row.ErrorCount, ErrorRate: row.ErrorRate,
		AvgMs: row.AvgMs, P50Ms: row.P50Ms, P95Ms: row.P95Ms, P99Ms: row.P99Ms,
	}
	if winSec > 0 {
		cx.Current.Rate = float64(row.SpanCount) / winSec
	}
	cx.Apdex = row.Apdex
	if row.HasPrior {
		cx.HasBaseline = true
		cx.Baseline = aiRED{
			Spans: row.PriorSpanCount, ErrorCount: row.PriorErrorCount, ErrorRate: row.PriorErrorRate,
			AvgMs: row.PriorAvgMs, P50Ms: row.PriorP50Ms, P95Ms: row.PriorP95Ms, P99Ms: row.PriorP99Ms,
		}
		// Baseline.Rate KASITLI olarak set edilmez (v0.9.187): prior
		// pencere GetOperationSummaryCompared'da sınır bucket'ı atıldığı
		// için current ile aynı winSec'e bölmek asimetrik/yanıltıcı olur;
		// renderOperationSnapshot baseline rate göstermiyor zaten.
	}
	// Deploy markers are service-level (a deploy touches every operation).
	if deps, derr := s.store.GetServiceDeploys(ctx, service, from, to); derr == nil {
		for _, d := range deps {
			cx.Deploys = append(cx.Deploys, aiDeploy{Version: d.Version, TimeUnixNs: d.TimeUnixNs})
		}
	}
	return cx
}

// renderOperationSnapshot mirrors renderServiceSnapshot for a single
// operation (compact Turkish; field order/labels affect the narration).
func renderOperationSnapshot(cx *aiOperationContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Operasyon: %s / %s (son %d dakika)\n", cx.Service, cx.Operation, cx.RangeS/60)
	c := cx.Current
	fmt.Fprintf(&b, "RED: rate=%.2f req/s, error=%.2f%% (%d hata), p50=%.0fms, p95=%.0fms, p99=%.0fms, apdex=%.2f\n",
		c.Rate, c.ErrorRate, c.ErrorCount, c.P50Ms, c.P95Ms, c.P99Ms, cx.Apdex)
	if cx.HasBaseline {
		bl := cx.Baseline
		fmt.Fprintf(&b, "Baseline (önceki %d dk): error=%.2f%%, p99=%.0fms, p50=%.0fms\n",
			cx.RangeS/60, bl.ErrorRate, bl.P99Ms, bl.P50Ms)
	} else {
		b.WriteString("Baseline: önceki pencerede bu operasyon için veri yok.\n")
	}
	if len(cx.Deploys) > 0 {
		var parts []string
		for _, d := range cx.Deploys {
			parts = append(parts, d.Version)
		}
		fmt.Fprintf(&b, "Deploy(lar) (servis geneli): %s\n", strings.Join(parts, ", "))
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
	// v0.9.597 — MODELİN atladığı diziler de boş dilime normalize
	// edilir. Yukarıdaki bağlam alanları sunucu kaynaklıydı; bunlar
	// model kaynaklı ama sonuç AYNI: nil → `null` → frontend'de
	// `a.kanit.length` TypeError → sayfa ErrorBoundary'ye düşer.
	//
	// Şema bu alanları required kılıyor ama şema YALNIZ OpenAI-uyumlu
	// yolda gönderiliyor; anthropic sağlayıcıda ya da json_schema
	// reddedilip merdiven json_object'e düştüğünde tek güvence
	// prompt'un ricası kalıyor. Bir modelin ricayı tutacağına
	// güvenmek, çökmeyi modele emanet etmektir.
	if a.Kanit == nil {
		a.Kanit = []string{}
	}
	if a.Oneriler == nil {
		a.Oneriler = []string{}
	}
	return &a
}

// serviceTokenRe + nonServiceHyphenated v0.9.559'da entity_scan.go'ya
// TAŞINDI: RCA verdict yüzeyi de aynı taramayı kullanıyor ve ikinci bir
// kopya yazmak, birinin diğerinden sessizce ayrışması demekti.

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
		// v0.9.598 — hata METNİ de gösterildi. Type boşsa etikete
		// mesaj basılıyor (renderServiceSnapshot), yani içindeki bir
		// servis adı modele gösterilmiş demektir.
		addShownTokens(known, e.Type, e.Message)
	}
	// v0.9.598 — v0.9.580'in prompt'a bastığı SOMUT KİMLİKLER.
	//
	// Snapshot bunları basıyordu ama bilinen küme bilmiyordu, yani
	// model kendisine verdiğimiz bir CHANNEL_CODE'u alıntıladığında
	// kalkan "uydurma" diyordu. v0.9.580'in kendi yorumu bunun neden
	// imkânsız olduğunu zaten yazıyor ("değerler doğrudan
	// ClickHouse'tan geliyor, uydurulamaz") — eksik olan, o bilgiyi
	// kalkana da söylemekti.
	for _, sl := range cx.Business {
		for _, v := range sl {
			addShownTokens(known, v.Value)
		}
	}
	for _, c := range cx.Correlation {
		addShownTokens(known, c.Key)
		addShownTokens(known, c.Values...)
	}

	// v0.9.559 — tarama entity_scan.go'daki paylaşılan yardımcıya
	// indi. Alan listesi ve sırası AYNI; davranış değişmedi.
	texts := []string{a.Ozet, a.OlasiNeden}
	texts = append(texts, a.Kanit...)
	texts = append(texts, a.Oneriler...)
	unknown := scanUnknownEntities(known, texts...)

	pc := &aiPostCheck{Verified: len(unknown) == 0, UnknownServices: unknown}
	if !pc.Verified {
		pc.Note = "Girdi verisinde olmayan servis adı/adları geçiyor — doğrulanamadı."
	}
	return pc
}
