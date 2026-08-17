package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/ai/insight"
	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// insight_test.go — v0.9.1129 (AI Faz 2.1).
//
// Kartın sözleşmesinde üç şey SESSİZ kırılır, üçü de burada pinlendi:
//
//  1. `signals` çerçevesi ÖNCE düşmeli. Sonra düşerse kartın tüm fikri
//     (deterministik yarı prose'u beklemez) kaybolur ve kimse fark
//     etmez — panel yalnız "biraz daha yavaş" görünür.
//  2. AI KAPALIYKEN cevap gelmeli ve LLM'e HİÇ gidilmemeli. 503 dönmek
//     ya da boş kart çizmek, mockup sözleşmesinin ihlali.
//  3. Yüzey etiketi. /ai kırılımı "other"a çökerse kartın maliyeti ve
//     kalitesi ölçülemez hâle gelir (v0.9.1067 ile aynı sınıf).
//
// SSE çözümleyici + sahte sağlayıcı copilot_explain_stream_test.go'dan
// (aynı paket): ikinci bir taklit sağlayıcı yazmıyoruz.

// disabledCopilotServer — kimlikleri YAPILANDIRILMIŞ ama toggle'ı KAPALI
// bir kurulum. AI-off testi için nil copilot'tan daha güçlü: sağlayıcı
// erişilebilir durumda, yani "hiç çağrılmadı" assert'i gerçekten bir şey
// söylüyor.
func disabledCopilotServer(t *testing.T, p *streamingProvider) *Server {
	t.Helper()
	cop := copilot.New(copilot.ProviderOpenAI, "test-key", "gemma4")
	cop.Configure(copilot.ProviderOpenAI, "test-key", "gemma4", p.srv.URL, false, false)
	if cop.Active() {
		t.Fatal("copilot kapalı olmalıydı")
	}
	return &Server{copilot: cop}
}

func insightReq(stream bool) *http.Request {
	path := "/api/insight/problem/p1"
	if stream {
		path += "?stream=1"
	}
	return httptest.NewRequest(http.MethodGet, path, nil)
}

// sampleInsightResponse — deterministik yarı (handler'ın kanıt
// montajından bağımsız; o parça ayrıca projeksiyon testleriyle kapalı).
func sampleInsightResponse() insight.Response {
	return insight.Response{
		Signals: []insight.Signal{
			{Kind: insight.SignalProblem, Label: "Şiddet", Value: "kritik", Severity: insight.SevErr},
			{Kind: insight.SignalDeploy, Label: "Deploy", Value: "v2 · 4dk önce", Severity: insight.SevWarn},
		},
		Links:  []insight.Link{{Label: "Servis", Href: "/service?name=checkout&range=custom%3A1-2"}},
		Charts: []insight.ChartSpec{{Title: "checkout · p95", Service: "checkout", Agg: "p95", RangeS: 900}},
	}
}

// ── 1. çerçeve sırası: signals → delta* → answer → done ─────────────

func TestDeliverInsightStreamFrameSequence(t *testing.T) {
	p := newStreamingProvider(t, &streamingProvider{deltas: []string{"Kök ", "neden: ", "redis."}})
	s := explainStreamServer(t, p)

	r := insightReq(true)
	w := httptest.NewRecorder()
	s.deliverInsight(w, r, sampleInsightResponse(), "sys", "user")

	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q; akan kipte SSE olmalı", ct)
	}
	frames := parseSSE(t, w.Body.String())
	if len(frames) != 6 {
		t.Fatalf("çerçeve sayısı = %d (%v); signals + delta×3 + answer + done bekleniyordu",
			len(frames), frames)
	}

	// (a) İLK çerçeve signals — deterministik yarı prose'u BEKLEMEZ.
	if frames[0].event != "signals" {
		t.Fatalf("ilk çerçeve = %q; signals olmalı (kartın tüm fikri bu)", frames[0].event)
	}
	sig := frames[0].data
	if prose, ok := sig["prose"].(string); !ok || prose != "" {
		t.Errorf("signals çerçevesi prose taşıyor: %v", sig["prose"])
	}
	if arr, _ := sig["signals"].([]any); len(arr) != 2 {
		t.Errorf("signals çerçevesinde 2 sinyal bekleniyordu: %v", sig["signals"])
	}
	if arr, _ := sig["links"].([]any); len(arr) != 1 {
		t.Errorf("signals çerçevesinde 1 link bekleniyordu: %v", sig["links"])
	}
	if arr, _ := sig["charts"].([]any); len(arr) != 1 {
		t.Errorf("signals çerçevesinde 1 chart bekleniyordu: %v", sig["charts"])
	}
	if off, _ := sig["aiOff"].(bool); off {
		t.Error("AI aktifken aiOff true geldi")
	}
	if xid, _ := sig["exchangeId"].(string); xid == "" {
		t.Error("signals çerçevesinde exchangeId yok — 👍/👎 rayı kartın açılışında bağlanıyor")
	}
	if m, _ := sig["model"].(string); m != "gemma4" {
		t.Errorf("signals.model = %q; aktif model taşınmalı (çip)", m)
	}

	// (b) prose token token.
	for i, want := range []string{"Kök ", "neden: ", "redis."} {
		f := frames[1+i]
		if f.event != "delta" || f.data["text"] != want {
			t.Fatalf("çerçeve %d = %+v; delta %q bekleniyordu", 1+i, f, want)
		}
	}

	// (c) answer — metin alanı `text` (FE okuyucusunun bildiği şekil) ve
	// signals çerçevesindeki AYNI exchangeId.
	ans := frames[4]
	if ans.event != "answer" || ans.data["text"] != "Kök neden: redis." {
		t.Fatalf("answer çerçevesi = %+v", ans)
	}
	if ans.data["exchangeId"] != sig["exchangeId"] {
		t.Errorf("exchangeId çerçeveler arasında değişti: %v vs %v",
			sig["exchangeId"], ans.data["exchangeId"])
	}
	if frames[5].event != "done" {
		t.Fatalf("son çerçeve = %q", frames[5].event)
	}
	if ok, _ := frames[5].data["ok"].(bool); !ok {
		t.Error("done.ok = false")
	}
}

// ── 2. AI kapalı: signals → done, LLM'e HİÇ gidilmez ────────────────

func TestDeliverInsightAIOffSkipsLLMEntirely(t *testing.T) {
	p := newStreamingProvider(t, &streamingProvider{deltas: []string{"olmamalı"}})
	s := disabledCopilotServer(t, p)

	r := insightReq(true)
	w := httptest.NewRecorder()
	s.deliverInsight(w, r, sampleInsightResponse(), "sys", "user")

	frames := parseSSE(t, w.Body.String())
	if len(frames) != 2 {
		t.Fatalf("çerçeve sayısı = %d (%v); yalnız signals + done bekleniyordu", len(frames), frames)
	}
	if frames[0].event != "signals" || frames[1].event != "done" {
		t.Fatalf("çerçeve sırası = %q → %q", frames[0].event, frames[1].event)
	}
	// done.ok TRUE: AI'nın yapılandırılmamış olması ARIZA DEĞİL. false
	// dönmek FE'de hata durumu çizdirir ve kart deterministik yarısını
	// gösterdiği hâlde "başarısız" görünür.
	if ok, _ := frames[1].data["ok"].(bool); !ok {
		t.Error("AI kapalıyken done.ok = false; yapılandırılmamış olmak hata değil")
	}
	sig := frames[0].data
	if off, _ := sig["aiOff"].(bool); !off {
		t.Error("aiOff bayrağı yok — FE prose yokluğunu ARIZA sanar")
	}
	if arr, _ := sig["signals"].([]any); len(arr) != 2 {
		t.Errorf("AI kapalıyken sinyaller EKSİKSİZ dönmeli: %v", sig["signals"])
	}
	if arr, _ := sig["links"].([]any); len(arr) != 1 {
		t.Errorf("AI kapalıyken linkler EKSİKSİZ dönmeli: %v", sig["links"])
	}
	if xid, _ := sig["exchangeId"].(string); xid != "" {
		t.Errorf("AI kapalıyken exchangeId = %q; oylanacak model cevabı yok", xid)
	}
	if m, _ := sig["model"].(string); m != "" {
		t.Errorf("AI kapalıyken model = %q sızdı", m)
	}
	// MUTASYON KAPISI: sağlayıcıya tek istek bile gitmemeli.
	if p.nStream != 0 || p.nBuffered != 0 {
		t.Fatalf("AI kapalıyken sağlayıcı çağrıldı (stream=%d buffered=%d)", p.nStream, p.nBuffered)
	}
}

// ── 3. buffered kip ─────────────────────────────────────────────────

func TestDeliverInsightBufferedMode(t *testing.T) {
	p := newStreamingProvider(t, &streamingProvider{deltas: []string{"tek ", "parça"}})
	s := explainStreamServer(t, p)

	r := insightReq(false)
	w := httptest.NewRecorder()
	s.deliverInsight(w, r, sampleInsightResponse(), "sys", "user")

	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q; bayraksız istek JSON almalı", ct)
	}
	var got insight.Response
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("gövde çözülemedi: %v (%s)", err, w.Body.String())
	}
	if got.Prose != "tek parça" {
		t.Errorf("prose = %q; buffered kipte DOLU dönmeli", got.Prose)
	}
	if len(got.Signals) != 2 || len(got.Links) != 1 || len(got.Charts) != 1 {
		t.Errorf("deterministik yarı eksik: %+v", got)
	}
	if got.ExchangeID == "" || got.Model != "gemma4" {
		t.Errorf("meta alanları eksik: xid=%q model=%q", got.ExchangeID, got.Model)
	}
	if got.AIOff {
		t.Error("AI aktifken aiOff true")
	}
	if p.nStream != 0 {
		t.Errorf("bayraksız istek akış yoklaması yaptı (%d)", p.nStream)
	}
}

func TestDeliverInsightBufferedAIOff(t *testing.T) {
	p := newStreamingProvider(t, &streamingProvider{deltas: []string{"olmamalı"}})
	s := disabledCopilotServer(t, p)

	w := httptest.NewRecorder()
	s.deliverInsight(w, insightReq(false), sampleInsightResponse(), "sys", "user")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; AI kapalı olmak 503 DEĞİL (uç bilerek requireCopilot'suz)", w.Code)
	}
	var got insight.Response
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("gövde çözülemedi: %v", err)
	}
	if !got.AIOff || got.Prose != "" || len(got.Signals) != 2 {
		t.Errorf("AI kapalı buffered gövde = %+v", got)
	}
	if p.nStream != 0 || p.nBuffered != 0 {
		t.Fatalf("AI kapalıyken sağlayıcı çağrıldı (stream=%d buffered=%d)", p.nStream, p.nBuffered)
	}
}

// Boş kanıt: diziler JSON'da `[]`, asla `null` (v0.9.836 sınıfı — FE
// `.map` çağırıyor).
func TestDeliverInsightNeverEmitsNullSlices(t *testing.T) {
	p := newStreamingProvider(t, &streamingProvider{deltas: []string{"x"}})
	s := disabledCopilotServer(t, p)

	w := httptest.NewRecorder()
	s.deliverInsight(w, insightReq(false), insight.Response{}, "sys", "user")
	if body := w.Body.String(); !strings.Contains(body, `"signals":[]`) ||
		!strings.Contains(body, `"links":[]`) {
		t.Errorf("boş gövde null dizi taşıyor: %s", body)
	}

	w2 := httptest.NewRecorder()
	s.deliverInsight(w2, insightReq(true), insight.Response{}, "sys", "user")
	frames := parseSSE(t, w2.Body.String())
	if len(frames) == 0 {
		t.Fatal("çerçeve yok")
	}
	if _, ok := frames[0].data["signals"].([]any); !ok {
		t.Errorf("signals çerçevesinde dizi yok: %v", frames[0].data["signals"])
	}
}

// ── 4. akış BAŞLADIKTAN sonraki hata: çerçeve, statü değil ──────────

// deliverExplain'de ilk bayt üretimden SONRA yazılır, bu yüzden üretim
// hatası GERÇEK bir HTTP hatası olabiliyor. Kartta `signals` ÖNCE
// düşüyor (bilinçli sapma), dolayısıyla hata artık `error` çerçevesi.
// Deterministik yarı yine operatöre ULAŞMIŞ olur — asıl kazanç bu.
func TestDeliverInsightStreamErrorKeepsSignals(t *testing.T) {
	p := newStreamingProvider(t, &streamingProvider{fail: true})
	s := explainStreamServer(t, p)

	w := httptest.NewRecorder()
	s.deliverInsight(w, insightReq(true), sampleInsightResponse(), "sys", "user")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; signals düştükten sonra statü değiştirilemez", w.Code)
	}
	frames := parseSSE(t, w.Body.String())
	if len(frames) != 3 {
		t.Fatalf("çerçeve sayısı = %d (%v); signals + error + done bekleniyordu", len(frames), frames)
	}
	if frames[0].event != "signals" {
		t.Errorf("LLM hatası deterministik yarıyı düşürdü: %+v", frames)
	}
	if frames[1].event != "error" {
		t.Errorf("2. çerçeve = %q; error bekleniyordu", frames[1].event)
	}
	if ok, _ := frames[2].data["ok"].(bool); ok {
		t.Error("hatalı akışta done.ok = true")
	}
}

// Buffered kip explain sözleşmesini KORUR: hata = HTTP hatası.
func TestDeliverInsightBufferedErrorIsHTTPError(t *testing.T) {
	p := newStreamingProvider(t, &streamingProvider{fail: true})
	s := explainStreamServer(t, p)

	w := httptest.NewRecorder()
	s.deliverInsight(w, insightReq(false), sampleInsightResponse(), "sys", "user")
	if w.Code == http.StatusOK {
		t.Fatal("buffered kipte LLM hatası 200 döndü — FE retry davranışı bozulur")
	}
}

// Flush edemeyen writer (proxy sarımı) → sessizce buffered.
func TestDeliverInsightNonFlusherFallsBackToBuffered(t *testing.T) {
	p := newStreamingProvider(t, &streamingProvider{deltas: []string{"a", "b"}})
	s := explainStreamServer(t, p)

	rec := httptest.NewRecorder()
	s.deliverInsight(nonFlusher{rec}, insightReq(true), sampleInsightResponse(), "sys", "user")
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q; flush edemeyen writer JSON almalı", ct)
	}
	var got insight.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("gövde çözülemedi: %v", err)
	}
	if got.Prose != "ab" {
		t.Errorf("prose = %q", got.Prose)
	}
}

// ── 5. yüzey etiketi ────────────────────────────────────────────────

func TestAISurfaceFromInsightPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/api/insight/exception/abc123", "insight-exception"},
		{"/api/insight/problem/prob-42", "insight-problem"},
		// Tür whitelist'li: /ai kırılımı SONLU kalmalı (bilinmeyen tür
		// route'ta 404 olur, buraya düşmez — ikinci kapı yine dursun).
		{"/api/insight/log-pattern/x", "other"},
		{"/api/insight", "other"},
		// Komşuların etiketi değişmemeli.
		{"/api/copilot/explain-problem/p1", "explain-problem"},
		{"/api/admin/clickhouse/optimize-query", "ch-optimize"},
		{"/api/problems", "other"},
	}
	for _, tc := range cases {
		if got := aiSurfaceFromPath(tc.path); got != tc.want {
			t.Errorf("aiSurfaceFromPath(%q) = %q; want %q", tc.path, got, tc.want)
		}
	}
}

// ── 6. route kaydı + tür kapısı ─────────────────────────────────────

func TestInsightRouteRegisteredAndPathValuesBind(t *testing.T) {
	mux := http.NewServeMux()
	(&Server{}).registerRoutes(mux)

	_, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, "/api/insight/problem/p1", nil))
	if !strings.Contains(pattern, "/api/insight/{kind}/{id}") {
		t.Fatalf("eşleşen kalıp = %q; GET /api/insight/{kind}/{id} bekleniyordu", pattern)
	}
	// POST bu ucu bulmamalı (okuma ucu).
	if _, p := mux.Handler(httptest.NewRequest(http.MethodPost, "/api/insight/problem/p1", nil)); strings.Contains(p, "/api/insight/") {
		t.Errorf("POST /api/insight eşleşti (%q); uç yalnız GET", p)
	}
}

// Bilinmeyen tür 404 ve store'a DOKUNMADAN — nil store'lu Server ile
// koşuyor olması bunun kanıtı (dokunsa panic ederdi).
func TestGetInsightUnknownKindIs404(t *testing.T) {
	s := &Server{}
	for _, kind := range []string{"log-pattern", "slow-span", "anomaly", "Problem"} {
		r := httptest.NewRequest(http.MethodGet, "/api/insight/"+kind+"/x", nil)
		r.SetPathValue("kind", kind)
		r.SetPathValue("id", "x")
		w := httptest.NewRecorder()
		s.getInsight(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("kind=%q → %d; 404 bekleniyordu", kind, w.Code)
		}
	}
}

func TestGetInsightEmptyIDIs400(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/api/insight/problem/", nil)
	r.SetPathValue("kind", "problem")
	r.SetPathValue("id", "  ")
	w := httptest.NewRecorder()
	s.getInsight(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("boş id → %d; 400 bekleniyordu", w.Code)
	}
}

// ── 7. kanıt dönüşümleri (saf) ──────────────────────────────────────

func TestExceptionEvidenceProjection(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC).UnixNano()
	g := &chstore.ExceptionGroup{
		Fingerprint: "fp1", Type: "NPE", Service: "checkout", State: "open",
		Occurrences: 1240, FirstSeen: now - 3*24*3600*1e9, LastSeen: now - 300*1e9,
	}
	in := anomaly.ExceptionExplainInput{
		TraceID: "trace-1",
		Trend:   &anomaly.ExceptionTrend{Total: 1300, Last24: 380, Peak: 91},
		Deploys: []anomaly.NearbyDeploy{
			{Version: "v1", OffsetSec: 900},
			{Version: "v2", OffsetSec: 30, After: true},
		},
	}
	ev := exceptionEvidence(g, in, now)

	if ev.Occurrences != 1240 {
		t.Errorf("occurrences = %d; grubun SAYACI otorite (trend kovaları sapabilir)", ev.Occurrences)
	}
	if ev.Last24 != 380 || ev.PeakCount != 91 {
		t.Errorf("trend kırılımı taşınmadı: %+v", ev)
	}
	if ev.TraceID != "trace-1" || ev.NowNs != now {
		t.Errorf("kimlik/zaman taşınmadı: %+v", ev)
	}
	if len(ev.Deploys) != 2 || ev.Deploys[0].After || !ev.Deploys[1].After {
		t.Errorf("deploy YÖNÜ taşınmadı: %+v", ev.Deploys)
	}
	// Grubun sayacı 0 ise (eski satır) trend toplamına düşülür.
	g2 := *g
	g2.Occurrences = 0
	if ev2 := exceptionEvidence(&g2, in, now); ev2.Occurrences != 1300 {
		t.Errorf("sayaç 0 iken trend toplamına düşülmedi: %d", ev2.Occurrences)
	}
	// Trend/deploy yokluğu çökmemeli.
	if ev3 := exceptionEvidence(g, anomaly.ExceptionExplainInput{}, now); ev3.Last24 != 0 || len(ev3.Deploys) != 0 {
		t.Errorf("boş girdide projeksiyon = %+v", ev3)
	}
}

func TestProblemEvidenceProjection(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC).UnixNano()
	from, to := now-3600*1e9, now
	p := &chstore.Problem{
		ID: "p1", Service: "checkout", Metric: "error_rate", Severity: "critical",
		Priority: "P1", PriorityReason: "kritik", Comparator: ">", Status: "open",
		Value: 12.5, Threshold: 5, StartedAt: now - 2*3600*1e9,
		RecentDeploy: &chstore.RecentDeploy{Version: "listeden", TimeUnixNs: now, AgeSeconds: 600},
	}
	hyp := &chstore.RootCauseHypothesis{
		TopSuspect: "payments-api", Confidence: 0.72,
		Candidates: []chstore.ScoredCause{
			{Service: "payments-api"}, {Service: "redis"}, {Service: "postgres"},
		},
		// Hipotez yolunun deploy'u ÖLÇÜLMÜŞ etki taşır → listeden geleni
		// yenmeli (v0.9.1059: Impact'i yalnız bu yol doldurur).
		RecentDeploy: &chstore.RecentDeploy{Version: "hipotezden", AgeSeconds: 240,
			Impact: &chstore.DeployImpact{P99DeltaPct: 34, ErrorRateDeltaPct: 2.4}},
		Deep: &chstore.DeepEvidence{SlowOps: []chstore.OperationSummary{
			{Name: "POST /pay", P95Ms: 842, ErrorRate: 6.5},
		}},
	}
	blast := &chstore.BlastRadius{TotalCallers: 12, CascadingCallers: 3,
		Callers: []chstore.BlastRadiusCaller{{Service: "web"}, {Service: "bff"}, {Service: ""}}}

	ev := problemEvidence(p, hyp, blast, from, to, now)

	if ev.FromNs != from || ev.ToNs != to || ev.NowNs != now {
		t.Errorf("pencere taşınmadı: %+v", ev)
	}
	if ev.Deploy == nil || ev.Deploy.Version != "hipotezden" || !ev.Deploy.HasImpact {
		t.Errorf("ölçülmüş etkili deploy kazanmadı: %+v", ev.Deploy)
	}
	if ev.Deploy.P99DeltaPct != 34 || ev.Deploy.ErrDeltaPP != 2.4 {
		t.Errorf("etki değerleri taşınmadı: %+v", ev.Deploy)
	}
	if ev.Hyp == nil || ev.Hyp.TopSuspect != "payments-api" {
		t.Fatalf("hipotez taşınmadı: %+v", ev.Hyp)
	}
	// Top suspect "diğer adaylar" listesinde TEKRAR ETMEZ.
	for _, c := range ev.Hyp.Candidates {
		if c == "payments-api" {
			t.Error("top suspect diğer adaylar arasında tekrarlandı")
		}
	}
	if ev.Hyp.Others != len(ev.Hyp.Candidates) {
		t.Errorf("Others = %d; suspect DIŞINDAKİ aday sayısı olmalı (%d)",
			ev.Hyp.Others, len(ev.Hyp.Candidates))
	}
	if ev.SlowOp == nil || ev.SlowOp.Name != "POST /pay" {
		t.Errorf("DeepEvidence en yavaş operasyonu taşınmadı: %+v", ev.SlowOp)
	}
	if ev.Blast == nil || ev.Blast.TotalCallers != 12 || len(ev.Blast.TopCallers) != 2 {
		t.Errorf("blast projeksiyonu = %+v (boş servis adı süzülmeli)", ev.Blast)
	}

	// Hipotez/blast YOKken: liste deploy'u kullanılır, kalanlar nil.
	ev2 := problemEvidence(p, nil, nil, from, to, now)
	if ev2.Deploy == nil || ev2.Deploy.Version != "listeden" || ev2.Deploy.HasImpact {
		t.Errorf("hipotezsiz deploy = %+v", ev2.Deploy)
	}
	if ev2.Hyp != nil || ev2.Blast != nil || ev2.SlowOp != nil {
		t.Errorf("kanıt yokken alanlar nil olmalı: %+v", ev2)
	}
	// Çözülmüş problem damgası.
	res := now - 600*1e9
	p3 := *p
	p3.ResolvedAt = &res
	if ev3 := problemEvidence(&p3, nil, nil, from, to, now); ev3.ResolvedNs != res {
		t.Errorf("resolvedAt taşınmadı: %d", ev3.ResolvedNs)
	}
}
