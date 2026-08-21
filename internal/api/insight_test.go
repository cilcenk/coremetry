package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/ai/insight"
	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/logstore"
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
		// v0.9.1137 (Faz 2.4) — iki yeni yuva. Bu iki satır ÖNCE "other"
		// bekliyordu; whitelist artık insight.Kinds()'ten türediği için
		// tür eklemek yüzey etiketini de otomatik açıyor. Elle yazılmış
		// switch kalsaydı iki yüzey sessizce ölçülemez olurdu (v0.9.1067).
		{"/api/insight/log-pattern/Out%20of%20memory", "insight-log-pattern"},
		{"/api/insight/slow-query/12345%7Coracle", "insight-slow-query"},
		// Tür whitelist'li: /ai kırılımı SONLU kalmalı (bilinmeyen tür
		// route'ta 404 olur, buraya düşmez — ikinci kapı yine dursun).
		{"/api/insight/slow-span/x", "other"},
		{"/api/insight/logpattern/x", "other"},
		{"/api/insight", "other"},
		// Komşuların etiketi değişmemeli.
		{"/api/copilot/explain-problem/p1", "explain-problem"},
		{"/api/copilot/draft-postmortem/inc-1", "draft-postmortem"}, // v0.9.1197 Faz 5.4
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
//
// v0.9.1137: "log-pattern" bu listeden ÇIKTI (artık geçerli tür),
// yerine gerçek yazım hataları ve tasarım dokümanının eski adı
// ("slow-span") geldi.
func TestGetInsightUnknownKindIs404(t *testing.T) {
	s := &Server{}
	for _, kind := range []string{"slow-span", "logpattern", "slowquery", "anomaly", "Problem"} {
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

// ════════════════════════════════════════════════════════════════════
// v0.9.1137 (AI Faz 2.4) — log-pattern + slow-query yuvaları.
// ════════════════════════════════════════════════════════════════════

// TestGetInsightHandlesEveryKnownKind — YAPISAL KAPI.
//
// insight.Kinds()'e bir tür eklemek KnownKind'ı (ve dolayısıyla route'un
// 404 kapısını) hemen açar; ayrıştırıcının switch'i geride kalırsa
// getInsight hiçbir dala girmez ve HİÇBİR ŞEY YAZMAZ → 200 + BOŞ gövde.
// FE tarafında bu "insight akışı boş kapandı" hatası olarak görünür,
// yani 404'ten AYIRT EDİLEMEZ bir arıza. Sayıyı kaynaktan sayıyoruz:
// tür eklerken switch'i unutmak kırmızı yanar.
func TestGetInsightHandlesEveryKnownKind(t *testing.T) {
	b, err := os.ReadFile("insight.go")
	if err != nil {
		t.Fatalf("insight.go okunamadı: %v", err)
	}
	body := string(b)
	i := strings.Index(body, "func (s *Server) getInsight(")
	if i < 0 {
		t.Fatal("getInsight bulunamadı — kapıyı yeniden konumlandır")
	}
	block := body[i:]
	if j := strings.Index(block, "\n}\n"); j > 0 {
		block = block[:j]
	}
	if got, want := strings.Count(block, "case insight.Kind"), len(insight.Kinds()); got != want {
		t.Errorf("getInsight %d dal taşıyor ama %d tür kayıtlı (%v) — "+
			"eksik dal 200+BOŞ gövde döndürür", got, want, insight.Kinds())
	}
}

// slow-query kimliği BOZUKsa 400 ve store'a DOKUNULMAZ (nil store'lu
// Server ile koşuyor olması kanıtı). Kabul kuralları FE kodeğiyle aynı;
// tablonun tamamı internal/ai/insight/stmtref_test.go'da.
func TestInsightSlowQueryRejectsBadIDBeforeAnyRead(t *testing.T) {
	s := &Server{}
	for _, id := range []string{"0", "abc", "1|", "1|a|b", "-3", "999999999999999999999"} {
		r := httptest.NewRequest(http.MethodGet, "/api/insight/slow-query/"+id, nil)
		r.SetPathValue("kind", insight.KindSlowQuery)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		s.getInsight(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("id=%q → %d; 400 bekleniyordu", id, w.Code)
		}
	}
}

// boundInsightStmtWindow — HER BİRİM tabloda (v0.6.36 kuralı). Dahil
// olan tuzak: Go'nun time.ParseDuration'ı "d" (gün) TANIMAZ, yani "7d"
// SESSİZCE varsayılana düşer — gün penceresi saat olarak yazılır.
func TestBoundInsightStmtWindow(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
		note string
	}{
		{raw: "", want: time.Hour, note: "boş → varsayılan (katalog aralığı)"},
		{raw: "abc", want: time.Hour, note: "çöp → varsayılan"},
		{raw: "300s", want: 5 * time.Minute, note: "saniye birimi"},
		{raw: "5m", want: 5 * time.Minute},
		{raw: "15m", want: 15 * time.Minute},
		{raw: "1h", want: time.Hour},
		{raw: "6h", want: 6 * time.Hour},
		{raw: "1440m", want: 24 * time.Hour, note: "dakika birimi, gün eşdeğeri"},
		{raw: "168h", want: 7 * 24 * time.Hour, note: "tavan tam üstünde"},
		{raw: "169h", want: 7 * 24 * time.Hour, note: "tavan kelepçesi"},
		{raw: "8760h", want: 7 * 24 * time.Hour},
		{raw: "1m", want: 5 * time.Minute, note: "taban kelepçesi (MV tanesi 5dk)"},
		{raw: "1s", want: 5 * time.Minute},
		{raw: "0s", want: 5 * time.Minute},
		{raw: "-2h", want: 5 * time.Minute, note: "negatif → taban"},
		// TUZAK: "7d" ayrıştırılamaz → parseDuration varsayılana düşer ve
		// kelepçe onu OLDUĞU GİBİ geçirir. 7 GÜN İSTEYEN "168h" yazmalı.
		{raw: "7d", want: time.Hour, note: "Go 'd' birimini tanımaz — sessizce varsayılan"},
		{raw: "30d", want: time.Hour},
	} {
		got := boundInsightStmtWindow(parseDuration(tc.raw, insightStmtWindowDefault))
		if got != tc.want {
			t.Errorf("window=%q → %v; want %v (%s)", tc.raw, got, tc.want, tc.note)
		}
	}
}

// Log deseni penceresi ES rung'larına oturur (v0.8.270): kart açılışları
// anahtar kardinalitesini (ve dolayısıyla ES turlarını) tavanlı tutar.
// VARSAYILAN 5dk ve bu KRİTİK: satırların geldiği uç
// (/api/anomalies/log-patterns, param'sız) 5dk ile koşuyor; kart 30dk
// açsa satırda 1.240, kartta 7.900 görünürdü.
func TestInsightLogPatternWindowSnapsToRungsWithFiveMinuteDefault(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"", 5 * time.Minute},
		{"abc", 5 * time.Minute},
		{"30s", time.Minute},
		{"60s", time.Minute},
		{"5m", 5 * time.Minute},
		{"7m", 15 * time.Minute},
		{"15m", 15 * time.Minute},
		{"30m", 30 * time.Minute},
		{"6h", 30 * time.Minute},
		{"7d", 5 * time.Minute}, // ayrıştırılamaz → varsayılan
	} {
		got := snapAnomalyWindow(parseDuration(tc.raw, 5*time.Minute))
		if got != tc.want {
			t.Errorf("window=%q → %v; want %v", tc.raw, got, tc.want)
		}
	}
}

func TestLogPatternEvidenceProjection(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC).UnixNano()
	a := anomaly.LogPatternAnomaly{
		Pattern: "Out of memory", Regex: `OutOfMemoryError`, Kind: "spike",
		CurrentCount: 1240, BaselineCount: 320, Ratio: 3.875,
		Service: "checkout", Sample: "java.lang.OutOfMemoryError",
		LastSeenNs: now - 120*1e9,
		TopServices: []logstore.PatternServiceHit{
			{Service: "checkout", Count: 900}, {Service: "web", Count: 340},
		},
		Tokens: []string{"outofmemoryerror", "oomkilled"},
	}
	ev := logPatternEvidence(a, 5*time.Minute, now)

	if ev.Pattern != "Out of memory" || ev.Kind != "spike" {
		t.Errorf("kimlik/durum taşınmadı: %+v", ev)
	}
	if ev.CurrentCount != 1240 || ev.BaselineCount != 320 || ev.Ratio != 3.875 {
		t.Errorf("sayılar taşınmadı: %+v", ev)
	}
	// Pencere SANİYE olarak taşınır (birim disiplini: detektör Duration,
	// kanıt saniye, sinyal FmtDurTR).
	if ev.WindowSec != 300 {
		t.Errorf("WindowSec = %d; want 300", ev.WindowSec)
	}
	if ev.NowNs != now || ev.LastSeenNs != a.LastSeenNs {
		t.Errorf("zaman taşınmadı: %+v", ev)
	}
	if len(ev.TopServices) != 2 || ev.TopServices[0].Service != "checkout" ||
		ev.TopServices[0].Count != 900 {
		t.Errorf("servis kırılımı taşınmadı: %+v", ev.TopServices)
	}
	// Tokens LİNK malzemesi — düşerse /logs pivotu yalnız servise
	// daralır ve operatör desenin satırlarını GÖRMEZ (v0.5.306).
	if len(ev.Tokens) != 2 {
		t.Errorf("tokenlar taşınmadı: %+v", ev.Tokens)
	}
	// Regex kanıta GİRMEZ: operatöre gösterilmiyor, modele de gerekmiyor.
	if strings.Contains(ev.Sample, "OutOfMemoryError:") {
		t.Errorf("örnek satır bozuldu: %q", ev.Sample)
	}
	// Boş girdi çökmez.
	if ev2 := logPatternEvidence(anomaly.LogPatternAnomaly{}, time.Minute, now); ev2.WindowSec != 60 {
		t.Errorf("boş girdi projeksiyonu = %+v", ev2)
	}
}

func TestSlowQueryEvidenceProjection(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC).UnixNano()
	from := now - 3600*1e9
	ref, ok := insight.ParseStmtRef("12345|oracle")
	if !ok {
		t.Fatal("kimlik ayrıştırılamadı")
	}
	sum := &chstore.DBStmtSummary{
		SampleStatement: "SELECT * FROM ACCOUNTS WHERE ID = 42",
		DBSystem:        "oracle", DBName: "COREBANK",
		Calls: 12345, Errors: 82, TotalMs: 41200, AvgMs: 3.3,
		P95Ms: 842, P99Ms: 1240, MaxMs: 3100,
	}
	callers := []chstore.DBStmtCaller{
		{Service: "payments-api", Calls: 8100, P95Ms: 902, TotalMs: 30000},
		{Service: "", Calls: 1}, // boş servis adı SÜZÜLÜR
		{Service: "web", Calls: 4245, P95Ms: 400, TotalMs: 11200},
	}
	ev := slowQueryEvidence(ref, sum, callers, "slow1", "err1", from, now, now)

	// Görünüm formu ÖZETİN örneğinden normalize edilir (çekmecenin
	// yaptığının aynısı) — literaller '?' olur.
	if ev.Statement != "SELECT * FROM ACCOUNTS WHERE ID = ?" {
		t.Errorf("normalize ifade = %q", ev.Statement)
	}
	if ev.Sample != sum.SampleStatement {
		t.Errorf("literalli örnek taşınmadı: %q", ev.Sample)
	}
	if ev.StmtParam != "12345|oracle" {
		t.Errorf("kanonik stmt paramı = %q; linkler bunu taşıyor", ev.StmtParam)
	}
	if ev.Calls != 12345 || ev.Errors != 82 || ev.P99Ms != 1240 || ev.MaxMs != 3100 {
		t.Errorf("istatistikler taşınmadı: %+v", ev)
	}
	if len(ev.Callers) != 2 || ev.Callers[0].Service != "payments-api" || ev.Callers[1].Service != "web" {
		t.Errorf("çağıran kırılımı = %+v (boş ad süzülmeli)", ev.Callers)
	}
	if ev.CallersCapped {
		t.Error("3 çağıran tavana dayanmış sayıldı")
	}
	if ev.SlowTraceID != "slow1" || ev.ErrorTraceID != "err1" {
		t.Errorf("exemplar'lar taşınmadı: %+v", ev)
	}
	if ev.FromNs != from || ev.ToNs != now || ev.NowNs != now {
		t.Errorf("pencere taşınmadı: %+v", ev)
	}

	// Kimlikteki motor OTORİTE: özet farklı bir değer taşısa bile
	// operatörün kapsamı kazanır (aynı hash iki motorda görünebilir).
	sum2 := *sum
	sum2.DBSystem = "postgresql"
	if ev2 := slowQueryEvidence(ref, &sum2, nil, "", "", from, now, now); ev2.DBSystem != "oracle" {
		t.Errorf("kimlik kapsamı ezildi: %q", ev2.DBSystem)
	}
	// Motorlar arası katlanmış kimlik (system yok) → özetin değeri.
	foldRef, _ := insight.ParseStmtRef("12345")
	if ev3 := slowQueryEvidence(foldRef, &sum2, nil, "", "", from, now, now); ev3.DBSystem != "postgresql" {
		t.Errorf("katlanmış kimlikte özet motoru kullanılmadı: %q", ev3.DBSystem)
	}
	// Okuma tavanına dayanan çağıran listesi Truncated'ı kaldırır.
	capped := make([]chstore.DBStmtCaller, insightStmtCallerLimit)
	for i := range capped {
		capped[i].Service = string(rune('a' + i))
	}
	if ev4 := slowQueryEvidence(ref, sum, capped, "", "", from, now, now); !ev4.CallersCapped {
		t.Error("tavana dayanan çağıran okuması işaretlenmedi")
	}
}
