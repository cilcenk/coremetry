// Package api — AI observability endpoints (v0.5.163). The
// /ai page is the Coremetry-native counterpart to Langfuse: every
// Copilot Explain call lands as a row in the ai_calls CH table
// and these endpoints surface it as KPIs / timeseries / a recent-
// calls table. No external service involvement: prompts + samples
// stay inside the customer's CH cluster.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/ai/insight"
	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// AIRate is the per-model price quote used by the /ai cost
// estimate (v0.5.167). USD per 1M tokens, separately for input
// and output. Stored as a JSON blob in system_settings["ai.rates"]
// so admins can override the bundled defaults without code change.
// Local-model endpoints (Ollama, vLLM, LM Studio) should
// configure 0/0 — that's the default shape for any model not
// in the table.
type AIRate struct {
	InputPer1M  float64 `json:"inputPer1M"`
	OutputPer1M float64 `json:"outputPer1M"`
}

// aiRatesKey persists per-model price overrides keyed by the
// model string Copilot reports (gpt-4o-mini, claude-sonnet-4-6,
// llama3.1:8b, etc.). Operator-set entries win over the bundled
// table. Frontend reads this on the /ai page to compute cost
// at render time.
const aiRatesKey = "ai.rates"

// getAIRates returns the operator-set rate overrides (may be
// empty). UI merges with bundled defaults client-side so a new
// install shows reasonable estimates immediately.
func (s *Server) getAIRates(w http.ResponseWriter, r *http.Request) {
	raw, err := s.store.GetSetting(r.Context(), aiRatesKey)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := map[string]AIRate{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			// Corrupt blob — fall back to empty rather than error,
			// the operator can re-save from the UI.
			out = map[string]AIRate{}
		}
	}
	writeJSON(w, out)
}

// putAIRates replaces the entire rate map. Empty map is valid
// (resets to bundled defaults).
func (s *Server) putAIRates(w http.ResponseWriter, r *http.Request) {
	var body map[string]AIRate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	// Drop entries with both rates zero — that's just the
	// default-zero state and clutters the persisted blob.
	cleaned := map[string]AIRate{}
	for k, v := range body {
		k = strings.TrimSpace(k)
		if k == "" || (v.InputPer1M == 0 && v.OutputPer1M == 0) {
			continue
		}
		cleaned[k] = v
	}
	raw, _ := json.Marshal(cleaned)
	if err := s.store.PutSetting(r.Context(), aiRatesKey, raw); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "settings.ai_rates.update", "settings", "ai.rates",
		fmt.Sprintf(`{"models":%d}`, len(cleaned)))
	writeJSON(w, cleaned)
}

// ── v0.10.533 (CoSRE v2 Faz 2.1) — TEK attribution kurucu ─────────────────
//
// Yedi sarmalayıcı (buffered/stream × path-surface/explicit-surface ×
// plain/json/masked) aynı karar matrisini elle 7 kez kuruyordu ve
// "ExchangeID'yi ctx'ten TAŞI" satırı üç yerde kopyaydı — v0.9.593'ün
// düzelttiği "aynı karar iki yerde yazılmıştı" hatasının kalıbı. Artık
// matris `aiCall`'da tek kez yazılı; sarmalayıcılar imzalarını korur (42
// çağrı noktası dokunulmadı) ve yalnız seçeneği söyler.
//
// Sözleşme (ai_call_matrix_test.go pinler):
//   • r != nil → surface istek path'inden (+ whitelist'li ?src=), kullanıcı
//     auth claims'ten, ExchangeID ctx'teki meta'dan TAŞINIR (withExchange
//     rayı), Shield yüzeye göre tohumlanır — explainCallCtx.
//   • r == nil → meta ctx'ten (sohbet handler'ı kurmuştur), yalnız surface
//     ezilir; Shield yoksa yüzey kapılı varsayılan.
//   • json → WithJSONMode / WithJSONSchema(surface, schema).
//   • logUser → ai_calls örneği maskeli (PromptLogOverride), PromptChars
//     gerçek prompt'tan sayılır.
//   • onDelta → StreamText (akamayan uçta şeffaf buffered düşüş), yoksa Explain.
//   • Span: r varsa ai.explain + kanıt attr'ları, yoksa ctx tabanlı ai.explain.
type aiCallOpts struct {
	surface string
	json    bool
	schema  map[string]any
	logUser string
	onDelta func(string)
}

func (s *Server) aiCall(ctx context.Context, r *http.Request, system, user string, o aiCallOpts) (string, error) {
	surface := o.surface
	if r != nil {
		ctx = s.explainCallCtx(r)
	}
	meta := copilot.MetaFromContext(ctx)
	if surface == "" {
		surface = meta.Surface
	}
	meta.Surface = surface
	if meta.Shield == nil || r != nil {
		meta.Shield = s.aiShieldFor(ctx, surface) // v0.10.421 (E6); v0.10.431 tohumlu + yüzey kapısı
	}
	if o.logUser != "" && o.logUser != user {
		// v0.9.831 — "Kodu da incele": kaynak kodu modele gider, ai_calls'a gitmez.
		meta.PromptLogOverride = system + "\n\n" + o.logUser
	}
	if o.json {
		// v0.9.517/527 — katı JSON; şema varsa o şekle kilit.
		if len(o.schema) > 0 {
			ctx = copilot.WithJSONSchema(ctx, surface, o.schema)
		} else {
			ctx = copilot.WithJSONMode(ctx)
		}
	}
	ctx = copilot.WithMeta(ctx, meta)
	var done func(error)
	if r != nil {
		ctx, done = s.beginExplainSpan(r, ctx)
	} else {
		ctx, done = s.beginExplainSpanCtx(ctx) // v0.10.425 (O2)
	}
	var out string
	var err error
	if o.onDelta != nil {
		out, err = s.copilot.StreamText(ctx, system, user, o.onDelta)
	} else {
		out, err = s.copilot.Explain(ctx, system, user)
	}
	done(err)
	return out, err
}

// copilotExplain — surface path'ten, kullanıcı claims'ten; make audit
// CHECK 4 her Copilot rotasını bu aileden geçirir (doğrudan
// s.copilot.Explain ai_calls satırını atlar).
func (s *Server) copilotExplain(r *http.Request, system, user string) (string, error) {
	return s.aiCall(r.Context(), r, system, user, aiCallOpts{})
}

// copilotExplainStream (v0.9.1127) — copilotExplain'in akan ikizi; aynı
// atıf, cevap token token onDelta'dan.
func (s *Server) copilotExplainStream(r *http.Request, system, user string, onDelta func(string)) (string, error) {
	return s.aiCall(r.Context(), r, system, user, aiCallOpts{onDelta: onDelta})
}

// copilotExplainMasked (v0.9.831) — ai_calls örneği logUser'dan (kod maskesi).
func (s *Server) copilotExplainMasked(r *http.Request, system, user, logUser string) (string, error) {
	return s.aiCall(r.Context(), r, system, user, aiCallOpts{logUser: logUser})
}

// copilotExplainJSON (v0.9.517) — katı JSON; schema (v0.9.527) verilirse o şekle.
func (s *Server) copilotExplainJSON(r *http.Request, system, user string, schema map[string]any) (string, error) {
	return s.aiCall(r.Context(), r, system, user, aiCallOpts{json: true, schema: schema})
}

// copilotExplainSurface (v0.8.397) — surface path'ten türemeyen çağıranlar
// (guided sohbet "chat-guided"); meta ctx'te kurulu, yalnız surface ezilir.
func (s *Server) copilotExplainSurface(ctx context.Context, surface, system, user string) (string, error) {
	return s.aiCall(ctx, nil, system, user, aiCallOpts{surface: surface})
}

// copilotStreamSurface (v0.8.404) — copilotExplainSurface'in akan ikizi.
func (s *Server) copilotStreamSurface(ctx context.Context, surface, system, user string, onDelta func(string)) (string, error) {
	return s.aiCall(ctx, nil, system, user, aiCallOpts{surface: surface, onDelta: onDelta})
}

// explainCallCtx — tek-atış ✨ çağrısının atıf bağlamı: surface (istek
// path'inden), kullanıcı kimliği (auth claims) ve çağıranın ctx'e koyduğu
// exchange kimliği.
//
// v0.9.1119 (Faz 0.3) — v0.9.593'ün JSON varyantına getirdiği taşıma
// buraya da: çağıran ctx'e exchange kimliği koyduysa (withExchange)
// ai_calls satırına biner ve tek-atış prose yüzeyleri de oylanabilir
// olur. O satırın YOKLUĞU, 15 yüzeyin 👍/👎 alamamasının tek sebebiydi.
func (s *Server) explainCallCtx(r *http.Request) context.Context {
	c := auth.FromContext(r.Context())
	uid, email := "", ""
	if c != nil {
		uid, email = c.UserID, c.Email
	}
	surface := aiSurfaceFromRequest(r)
	return copilot.WithMeta(r.Context(), copilot.CallMeta{
		Surface:    surface,
		UserID:     uid,
		UserEmail:  email,
		ExchangeID: copilot.MetaFromContext(r.Context()).ExchangeID,
		Shield:     s.aiShieldFor(r.Context(), surface), // v0.10.421 (E6); v0.10.431 tohumlu
	})
}

// withExchange — tek-atış ✨ yüzeyinin geri bildirim rayı (v0.9.1119,
// Faz 0.3). İsteğe taze bir exchange kimliği iliştirir; sarmalayıcılar
// ctx'teki kimliği ai_calls'a taşır, handler AYNI kimliği yanıtına
// koyar ("exchangeId") → FE 👍/👎'ı postAIFeedback'e bağlar (v0.8.399
// rayı). Kimlik minting'i chat ile aynı üreticiden (newRandID).
func withExchange(r *http.Request) (*http.Request, string) {
	xid := newRandID(16)
	return r.WithContext(copilot.WithMeta(r.Context(),
		copilot.CallMeta{ExchangeID: xid})), xid
}

// aiSurfaceFromPath maps the request path to a short stable
// surface label for grouping. Every /api/copilot/* endpoint has
// a unique path so we just take the last segment with the
// leading verb stripped — `/api/copilot/explain-span` → "explain-
// span", `/api/copilot/explain-slo/{id}` → "explain-slo", etc.
// Unknown paths collapse to "other" so the /ai breakdown stays
// finite.
// aiSurfaceSrcAllowed — v0.10.432 (CoSRE router boşlukları D8): `?src=`
// ile yüzey etiketine eklenen SONEK ("explain-trace:nudge"). Whitelist'li
// ki /ai kırılımı sonlu kalsın (insight türü whitelist'iyle aynı gerekçe);
// yalnız explain-* yüzeylerinde. "nudge" = trace ilk açılış baloncuğundan
// tetiklenen explain — operatör "kendiliğinden önerilen açıklama ne kadar
// tıklanıyor, kaça mal oluyor"u /ai'da ayrı görsün.
var aiSurfaceSrcAllowed = map[string]bool{"nudge": true, "chat": true} // chat — v0.10.460: sohbetten açılan Explain çekmecesi

// aiSurfaceFromRequest — path'ten türeyen etiket + whitelist'li src soneki.
func aiSurfaceFromRequest(r *http.Request) string {
	base := aiSurfaceFromPath(r.URL.Path)
	if src := r.URL.Query().Get("src"); src != "" && aiSurfaceSrcAllowed[src] && strings.HasPrefix(base, "explain-") {
		return base + ":" + src
	}
	return base
}

func aiSurfaceFromPath(p string) string {
	// v0.9.1067 (Faz 3.6 / Q8) — /api/copilot/* dışındaki tek AI ucu:
	// CH sorgu optimizasyonu /api/admin/clickhouse/optimize-query'de
	// yaşıyor ve /ai'da "other" olarak toplanıyordu — kalite kıyası
	// yüzeysiz kalıyordu.
	if strings.HasSuffix(strings.Trim(p, "/"), "admin/clickhouse/optimize-query") {
		return "ch-optimize"
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	// v0.9.1129 (Faz 2.1) — insight kartı /api/copilot/ DIŞINDA yaşıyor
	// (AI kapalıyken de cevap veren tek uç; gerekçe insight.go). Yüzey
	// etiketi yine path'ten türüyor, ama tür WHITELIST'li: /ai kırılımı
	// sonlu kalmalı. Bilinmeyen tür route'ta 404 olur, yani buraya
	// normalde hiç düşmez — ikinci kapı ucuz ve kalıcı.
	//
	// v0.9.1137 (Faz 2.4) — whitelist artık insight.KnownKind'dan TÜRÜYOR,
	// elle yazılmış bir switch'ten değil. Sebep somut: 2.4'te iki tür
	// eklendi ve elle yazılmış liste onları görmeseydi iki yeni yüzey
	// sessizce "other"a düşerdi (v0.9.1067'nin ta kendisi — ölçülemeyen
	// maliyet). Tek kaynak = route'un tanıdığı küme.
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "insight" {
		if insight.KnownKind(parts[2]) {
			return "insight-" + parts[2]
		}
		return "other"
	}
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "copilot" {
		return "other"
	}
	seg := parts[2]
	// Drop trailing dynamic segments — anything past the verb is
	// an id or filter, not part of the surface name.
	return seg
}

// listAICalls — paginated recent-calls table on the /ai page.
// Filters: surface / provider / status / time range. Default
// window 24h.
func (s *Server) listAICalls(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := chstore.ListAICallsParams{
		Surface:  q.Get("surface"),
		Provider: q.Get("provider"),
		Status:   q.Get("status"),
		Limit:    parseInt(q.Get("limit"), 100),
	}
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	if !from.IsZero() {
		p.From = from
	}
	if !to.IsZero() {
		p.To = to
	}
	rows, err := s.store.ListAICalls(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	if rows == nil {
		rows = []chstore.AICall{}
	}
	writeJSON(w, rows)
}

// getAICall — single-call drill-in. Operator opens this from the
// list to see prompt + response in full (well, up to the 4KB
// sample cap applied at insert time).
func (s *Server) getAICall(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetAICall(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if c == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, c)
}

// aiStats — overview KPIs + per-surface + per-provider breakdown.
// Cached 30s; the /ai page polls this every minute and a heavy
// install with 30 operators on it shouldn't hit CH 30 times for
// the same numbers.
func (s *Server) aiStats(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, 24*time.Hour)
	// v0.10.409 — yanıt şekli değişti (byErrorClass/avgTtftMs/extended) ve
	// dolum boot-zamanı bayrağına bağlı: ikisi de anahtarda, deploy sonrası
	// bayat şekil servis edilmez.
	key := fmt.Sprintf("ai-stats:v421:from=%d:to=%d:ext=%t",
		from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute), chstore.AICallsExtended())
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.ComputeAIStats(ctx, from, to)
	})
}

// aiRCAQuality — kök-neden hakem motorunun kalitesi (v0.9.594).
//
// /ai sayfası bugün TRANSPORT sağlığını ölçüyor: kaç çağrı, kaç hata,
// kaç token, ne kadar gecikme. Hiçbiri "cevap DOĞRU MUYDU" sorusuna
// dokunmuyor — bir model sürekli 200 dönüp sürekli saçmalayabilir ve
// o panelde mükemmel görünür.
//
// Bu uç o boşluğun RCA yüzeyindeki karşılığı. Üç ayrı soruyu birden
// cevaplıyor ve üçü FARKLI şeyler:
//
//	kararın DAĞILIMI  — kaçı kök neden gösterdi, kaçı "kanıt yetersiz"
//	MOTORUN sağlığı   — model kaç kez çözümlenemedi, kalkanlar kaç kez girdi
//	OPERATÖRÜN yargısı — 👍/👎
//
// Bir tanesine bakmak yanıltır: yüksek insufficient_evidence oranı
// modelin zayıflığı DEĞİL, kanıtın yetersizliği olabilir; yüksek
// kalkan oranı modelin uydurduğunu söyler ve bu bambaşka bir arıza.
//
// Aynı 30sn önbellek + admin kapısı: kardeş uçlarla tek davranış.
func (s *Server) aiRCAQuality(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, 24*time.Hour)
	// v0.10.410 — şekil sürümü (calibration alanı): deploy sonrası bayat
	// alan-sız JSON önbellekten servis edilmez.
	key := fmt.Sprintf("ai-rca-quality:v410:from=%d:to=%d",
		from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.RCAVerdictQualityStats(ctx, from, to)
	})
}

// aiSeries — volume / errors / latency / token timeseries for the
// /ai page line chart. Bucket size is derived from the window
// length so a 24h window has 5-min buckets, a 7d window has 1h
// buckets, etc.
func (s *Server) aiSeries(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, 24*time.Hour)
	// Aim for ~120 points so the line chart looks dense but the
	// SVG stays responsive.
	bucketSec := int(to.Sub(from).Seconds() / 120)
	if bucketSec < 60 {
		bucketSec = 60
	}
	key := fmt.Sprintf("ai-series:from=%d:to=%d:b=%d",
		from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute), bucketSec)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.AICallsTimeseries(ctx, from, to, bucketSec)
	})
}

// GET /api/ai/router-gaps?days=7 — v0.9.549.
//
// CoSRE'nin guided router'ının YAKALAYAMADIĞI sorular. Serbest tool
// döngüsüne düşen her soru ai_calls'a surface='chat' ile yazılıyor ve
// prompt_sample kullanıcının sorusunun kendisi — yani rapor saf bir
// okuma, yeni kayıt gerekmiyor.
//
// Değeri: "sıradaki intent ne olmalı" sorusu bugüne kadar sezgiyle
// cevaplanıyordu. Bu liste onu ölçüye bağlıyor.
func (s *Server) aiRouterGaps(w http.ResponseWriter, r *http.Request) {
	// Gün sayısı SABİT basamaklardan — cache anahtarına giriyor,
	// serbest değer kardinaliteyi patlatır (v0.8.270).
	days := 7
	switch r.URL.Query().Get("days") {
	case "1":
		days = 1
	case "30":
		days = 30
	}
	key := fmt.Sprintf("ai:router-gaps:v1:d=%d", days)
	s.serveCached(w, r, key, 5*time.Minute, func(ctx context.Context) (any, error) {
		gaps, err := s.store.RouterGaps(ctx, time.Duration(days)*24*time.Hour, 50)
		if err != nil {
			return nil, err
		}
		var total uint64
		for _, g := range gaps {
			total += g.Count
		}
		return map[string]any{
			"gaps": gaps, "days": days, "totalFallbacks": total,
			"generatedAt": time.Now().UnixNano(),
		}, nil
	})
}

// copilotExplainJSONSurface (v0.9.559) — copilotExplainJSON'un ctx
// taşıyan ikizi.
//
// Neden gerekli: serveCached'in fn'i İKİ yolda koşar ve SWR arka plan
// tazelemesinde istek çoktan bitmiştir (cache.go refreshKey taze bir
// context.Background() verir). `r` alan bir sarmalayıcı o yolda ölü
// bir context kullanır ve her tazeleme context.Canceled ile düşer —
// v0.9.557'de tam bu hata düzeltildi, aynısını JSON yolunda
// tekrarlamamak için bu varyant var.
//
// Meta ctx'ten gelir; yalnız surface ve JSON kipi burada set edilir.
func (s *Server) copilotExplainJSONSurface(ctx context.Context, surface, system, user string, schema map[string]any) (string, error) {
	return s.aiCall(ctx, nil, system, user, aiCallOpts{surface: surface, json: true, schema: schema})
}
