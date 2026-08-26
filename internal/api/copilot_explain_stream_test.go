package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/copilot"
)

// copilot_explain_stream_test.go — v0.9.1127 (AI Faz 1.5).
//
// Üç şeyi pinliyor:
//
//  1. ÇERÇEVE SIRASI ve ŞEKLİ. Frontend'in elle yazılmış SSE okuyucusu
//     copilot_drawer.go'nun şekline göre yazıldı; explain akan yolu o
//     şekilden saparsa FE sessizce hiçbir şey çizmez (istisna yok, hata
//     yok — boş panel). Sıra: delta* → answer → done.
//  2. BUFFERED KİPİN DEĞİŞMEDİĞİ. Akan varyantın bedeli, bayraksız
//     istemcinin gördüğü tek bir bayt bile olmamalı.
//  3. KAYNAK PİNİ. ai_routes.go'daki her POST /api/copilot/ ucu ya
//     deliverExplain'e bağlı ya da gerekçeli "ertelendi" listesinde —
//     9. yüzey sessizce buffered kalamaz (ai_routes_test.go'nun
//     requireCopilot pini ile aynı disiplin).

// ── sahte sağlayıcı: OpenAI-uyumlu SSE ──────────────────────────────

// streamingProvider — stream:true isteğine SSE, aksi hâlde tek atış JSON
// dönen sahte uç. `bufferedOnly` bir vLLM build'ini taklit eder: akış
// isteğini 400'ler, buffered çağrıya normal cevap verir (StreamText'in
// ŞEFFAF geri düşüşü — sıfır delta, tam metin).
type streamingProvider struct {
	srv          *httptest.Server
	deltas       []string
	bufferedOnly bool
	fail         bool // her iki yolda da 500 — ilk bayttan ÖNCE hata
	nStream      int
	nBuffered    int
}

func newStreamingProvider(t *testing.T, p *streamingProvider) *streamingProvider {
	t.Helper()
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		wantsStream, _ := m["stream"].(bool)
		if p.fail {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"model kotası doldu"}}`))
			return
		}
		if wantsStream {
			p.nStream++
			if p.bufferedOnly {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"stream is not supported"}}`))
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for _, d := range p.deltas {
				chunk, _ := json.Marshal(map[string]any{
					"choices": []any{map[string]any{"delta": map[string]any{"content": d}}},
				})
				fmt.Fprintf(w, "data: %s\n\n", chunk)
			}
			fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		p.nBuffered++
		w.Header().Set("Content-Type", "application/json")
		out, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"content": strings.Join(p.deltas, "")},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 3},
		})
		_, _ = w.Write(out)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// explainStreamServer — s.copilot'u sahte sağlayıcıya bağlı bir Server.
// (copilot_code_test.go'daki codeServer emsali.)
func explainStreamServer(t *testing.T, p *streamingProvider) *Server {
	t.Helper()
	cop := copilot.New(copilot.ProviderOpenAI, "test-key", "gemma4")
	cop.Configure(copilot.ProviderOpenAI, "test-key", "gemma4", p.srv.URL, false, true)
	if !cop.Active() {
		t.Fatal("sahte copilot aktif değil")
	}
	return &Server{copilot: cop}
}

// ── SSE çözümleyici (testin kendi okuyucusu) ────────────────────────

type sseFrame struct {
	event string
	data  map[string]any
}

func parseSSE(t *testing.T, raw string) []sseFrame {
	t.Helper()
	var out []sseFrame
	for _, block := range strings.Split(strings.TrimSpace(raw), "\n\n") {
		f := sseFrame{event: "message"}
		var data string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				f.event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data += strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
		if data == "" {
			continue
		}
		if err := json.Unmarshal([]byte(data), &f.data); err != nil {
			t.Fatalf("çerçeve JSON değil: %q (%v)", data, err)
		}
		out = append(out, f)
	}
	return out
}

func explainReq(stream bool) *http.Request {
	path := "/api/copilot/explain-problem/p1"
	if stream {
		path += "?stream=1"
	}
	return httptest.NewRequest(http.MethodPost, path, nil)
}

// ── 1. çerçeve sırası ───────────────────────────────────────────────

func TestDeliverExplainStreamFrameSequence(t *testing.T) {
	p := newStreamingProvider(t, &streamingProvider{deltas: []string{"Kök ", "neden: ", "redis."}})
	s := explainStreamServer(t, p)

	r := explainReq(true)
	w := httptest.NewRecorder()
	s.deliverExplain(w, r, "xid-abc", map[string]any{"similarCount": 2},
		s.explainPrompt(r, "sys", "user"), "")

	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q; akan kipte SSE olmalı", ct)
	}
	frames := parseSSE(t, w.Body.String())
	if len(frames) != 5 {
		t.Fatalf("çerçeve sayısı = %d (%v); delta×3 + answer + done bekleniyordu", len(frames), frames)
	}
	for i, want := range []string{"Kök ", "neden: ", "redis."} {
		if frames[i].event != "delta" {
			t.Fatalf("çerçeve %d = %q; delta bekleniyordu", i, frames[i].event)
		}
		if got := frames[i].data["text"]; got != want {
			t.Fatalf("delta %d = %v; want %q", i, got, want)
		}
	}

	ans := frames[3]
	if ans.event != "answer" {
		t.Fatalf("4. çerçeve = %q; answer bekleniyordu", ans.event)
	}
	// Metin alanı `text` — copilot_drawer.go'nun şekli. `explanation`
	// yazılırsa FE okuyucusu boş cevap çizer.
	if ans.data["text"] != "Kök neden: redis." {
		t.Fatalf("answer.text = %v; deltaların BİRLEŞİMİ olmalı", ans.data["text"])
	}
	// exchangeId olmadan 👍/👎 rayı kopar (v0.9.1121 zinciri).
	if xid, _ := ans.data["exchangeId"].(string); xid != "xid-abc" {
		t.Fatalf("answer.exchangeId = %q; handler'ın kimliği taşınmalı", xid)
	}
	// Ekstra alanlar da answer çerçevesine biner — akan kipte
	// kaybolurlarsa "N geçmiş çözüm" / kanıt kutulaması sessizce düşer.
	if sc, _ := ans.data["similarCount"].(float64); sc != 2 {
		t.Fatalf("answer.similarCount = %v; ekstra alanlar answer çerçevesine binmeli", ans.data["similarCount"])
	}

	if frames[4].event != "done" {
		t.Fatalf("son çerçeve = %q; done bekleniyordu", frames[4].event)
	}
	if ok, _ := frames[4].data["ok"].(bool); !ok {
		t.Fatal("done.ok = false; başarılı akışta true olmalı")
	}
}

// ── 2. buffered kip değişmedi ───────────────────────────────────────

func TestDeliverExplainBufferedUnchanged(t *testing.T) {
	p := newStreamingProvider(t, &streamingProvider{deltas: []string{"tek ", "parça"}})
	s := explainStreamServer(t, p)

	r := explainReq(false)
	w := httptest.NewRecorder()
	s.deliverExplain(w, r, "xid-1", map[string]any{"similarCount": 0},
		s.explainPrompt(r, "sys", "user"), "")

	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q; bayraksız istek JSON almalı", ct)
	}
	// Gövde v0.9.1126'daki anahtar kümesiyle bayt bayt aynı olmalı.
	if got := strings.TrimSpace(w.Body.String()); got != `{"exchangeId":"xid-1","explanation":"tek parça","similarCount":0}` {
		t.Fatalf("buffered gövde = %s", got)
	}
	if p.nStream != 0 {
		t.Fatalf("bayraksız istek akış yoklaması yaptı (%d); buffered yol dokunulmamalı", p.nStream)
	}
}

// nonFlusher — Flush edemeyen ResponseWriter (ara katman/proxy sarımı).
// Akış imkânsızsa istek SESSİZCE buffered cevaplanmalı: yarım yazılmış,
// asla flush edilmeyen bir SSE gövdesi istemciyi asar.
type nonFlusher struct{ http.ResponseWriter }

func TestDeliverExplainNonFlusherFallsBackToBuffered(t *testing.T) {
	p := newStreamingProvider(t, &streamingProvider{deltas: []string{"a", "b"}})
	s := explainStreamServer(t, p)

	r := explainReq(true)
	rec := httptest.NewRecorder()
	s.deliverExplain(nonFlusher{rec}, r, "xid-2", nil, s.explainPrompt(r, "sys", "user"), "")

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q; flush edemeyen writer'da JSON'a düşmeli", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("gövde JSON değil: %v", err)
	}
	if body["explanation"] != "ab" {
		t.Fatalf("explanation = %v", body["explanation"])
	}
}

// ── 3. şeffaf buffered geri düşüşü (StreamText sözleşmesi) ──────────

// Akıyamayan uç: StreamText buffered ikize düşer, SIFIR delta üretir ve
// tam metni döner. Akan kip bu durumda da `answer` çerçevesini basmak
// ZORUNDA — aksi hâlde vLLM'in akış desteklemeyen build'inde ✨ Explain
// hiçbir şey çizmez (sessiz boş panel).
func TestDeliverExplainTransparentBufferedFallback(t *testing.T) {
	p := newStreamingProvider(t, &streamingProvider{
		deltas: []string{"buffered ", "cevap"}, bufferedOnly: true,
	})
	s := explainStreamServer(t, p)

	r := explainReq(true)
	w := httptest.NewRecorder()
	s.deliverExplain(w, r, "xid-3", nil, s.explainPrompt(r, "sys", "user"), "")

	frames := parseSSE(t, w.Body.String())
	if len(frames) != 2 {
		t.Fatalf("çerçeve sayısı = %d (%v); yalnız answer + done bekleniyordu", len(frames), frames)
	}
	if frames[0].event != "answer" || frames[0].data["text"] != "buffered cevap" {
		t.Fatalf("answer çerçevesi = %+v; geri düşen çağrının TAM metnini taşımalı", frames[0])
	}
	if xid, _ := frames[0].data["exchangeId"].(string); xid != "xid-3" {
		t.Fatalf("geri düşen yolda exchangeId kayboldu: %q", xid)
	}
	if p.nStream != 1 || p.nBuffered != 1 {
		t.Fatalf("istek sayısı stream=%d buffered=%d; yoklama + tek geri düşüş bekleniyordu", p.nStream, p.nBuffered)
	}
}

// ── 4. ilk bayttan ÖNCEKİ hata gerçek HTTP hatasıdır ────────────────

func TestDeliverExplainErrorBeforeFirstByteStaysHTTPError(t *testing.T) {
	p := newStreamingProvider(t, &streamingProvider{fail: true})
	s := explainStreamServer(t, p)

	r := explainReq(true)
	w := httptest.NewRecorder()
	s.deliverExplain(w, r, "xid-4", nil, s.explainPrompt(r, "sys", "user"), "")

	if w.Code == http.StatusOK {
		t.Fatal("ilk bayttan önceki hata 200 döndü; SSE içine gizlenmiş hata FE'nin retry davranışını bozar")
	}
	if strings.Contains(w.Body.String(), "event: ") {
		t.Fatalf("hata gövdesi SSE çerçevesi taşıyor: %s", w.Body.String())
	}
}

// ── 5. ?stream=1 çözümlemesi ────────────────────────────────────────

func TestExplainWantsStream(t *testing.T) {
	cases := []struct {
		q    string
		want bool
	}{
		{"", false},
		{"?stream=1", true},
		{"?stream=true", true},
		{"?stream=yes", true},
		{"?stream=0", false},
		{"?stream=", false},
		{"?span=abc", false},
		{"?span=abc&stream=1", true},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodPost, "/api/copilot/explain-span/t1"+tc.q, nil)
		if got := explainWantsStream(r); got != tc.want {
			t.Errorf("explainWantsStream(%q) = %v; want %v", tc.q, got, tc.want)
		}
	}
	if explainWantsStream(nil) {
		t.Error("nil istek true döndü")
	}
}

// ── 6. kaynak pini: 9. yüzey sessizce buffered kalamaz ──────────────

// explainStreamWired — deliverExplain'e bağlı tek-atış ✨ handler'ları.
var explainStreamWired = []string{
	"copilotExplainTrace",
	"copilotExplainSpan",
	"copilotExplainProblem",
	"copilotExplainIncident",
	"copilotExplainAnomaly",
	"copilotExplainServiceHealth",
	"copilotExplainException",
	"copilotRunbook",
	"runbookUpdateSuggest", // v0.9.1198 Faz 5.5
}

// explainStreamDeferred — bilinçli olarak buffered kalan POST
// /api/copilot/ uçları + GEREKÇE. Bu slice CopilotExplain'in jenerik
// gövdesinden akan yüzeyleri kapsıyor; aşağıdakiler kendi küçük
// panellerinde/farklı sözleşmelerde yaşıyor ve ayrı dilimde bağlanacak.
var explainStreamDeferred = map[string]string{
	"copilotChat":           "zaten SSE (kendi akışı)",
	"copilotAnalyzeService": "ajanik döngü, prose değil",
	"copilotNLToQuery":      "JSON kipi — akan token anlamsız",
	"copilotExplainCharts":  "sunucu-cache'li yanıt (cache + akış ayrı dilim)",
	"explainShift":          "bağımsız küçük panel — Faz 1.5 takibi",
	"explainAlertNoise":     "bağımsız küçük panel — Faz 1.5 takibi",
	"explainLogPatterns":    "bağımsız küçük panel — Faz 1.5 takibi",
	"copilotCompareTraces":  "bağımsız küçük panel — Faz 1.5 takibi",
	"copilotDeployImpact":   "bağımsız küçük panel — Faz 1.5 takibi",
	"copilotExplainSLO":     "bağımsız küçük panel — Faz 1.5 takibi",
	// copilotExplainSlowQuery SİLİNDİ — v0.9.1209: FE tüketicisi v0.9.1137'de
	// insight kartına devredilmişti, uç 72 gün sıfır-çağrılı yaşadı.
	"copilotSuggestServiceTags": "JSON kipi — prose değil",
	// Faz 5.4 — çıktı chat balonuna değil postmortem EDİTÖRÜNE (textarea
	// taslağı) düşer; parça parça akıtmak düzenleme imlecini bozar,
	// operatör zaten tam taslağı bekleyip düzenler. Bilinçli buffered.
	"draftPostmortem": "taslak textarea'ya tek parça düşer — akış editörü bozar",
}

var explainRouteHandlerRE = regexp.MustCompile(`s\.([A-Za-z0-9_]+)\)*\s*\)\s*$`)

// TestExplainStreamRouteCoverage — ai_routes.go'ya eklenen HER POST
// /api/copilot/ ucu ya akan yola bağlı ya da gerekçeli erteleme
// listesinde. Yeni bir ✨ ucu ikisine de girmezse burada kırmızı yanar:
// "ekledim ama akmıyor" sessiz kalamaz.
func TestExplainStreamRouteCoverage(t *testing.T) {
	b, err := os.ReadFile("ai_routes.go")
	if err != nil {
		t.Fatalf("ai_routes.go okunamadı: %v", err)
	}
	wired := map[string]bool{}
	for _, h := range explainStreamWired {
		wired[h] = true
	}
	seen := 0
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.Contains(line, `"POST   /api/copilot/`) {
			continue
		}
		code := line
		if i := strings.Index(code, "//"); i >= 0 {
			code = code[:i]
		}
		m := explainRouteHandlerRE.FindStringSubmatch(strings.TrimSpace(code))
		if m == nil {
			t.Errorf("handler adı çıkarılamadı: %s", strings.TrimSpace(line))
			continue
		}
		seen++
		h := m[1]
		if !wired[h] && explainStreamDeferred[h] == "" {
			t.Errorf("yeni ✨ ucu %q: deliverExplain'e bağla ya da gerekçesiyle "+
				"explainStreamDeferred'a ekle", h)
		}
	}
	if seen < len(explainStreamWired)+len(explainStreamDeferred) {
		t.Errorf("ai_routes.go'da %d POST copilot ucu bulundu; liste %d bekliyor — "+
			"route taşındıysa bu pini güncelle", seen, len(explainStreamWired)+len(explainStreamDeferred))
	}
}

// TestExplainStreamHandlersUseDeliverExplain — bağlı sayılan her
// handler'ın GÖVDESİ gerçekten deliverExplain çağırmalı. Listeye ad
// eklemek yetmez; v0.9.1121'in dersi: "bağlandı" sanılan ray, çağrı
// yoksa ölü affordance'tır.
func TestExplainStreamHandlersUseDeliverExplain(t *testing.T) {
	bodies := serverFuncBodies(t)
	for _, h := range explainStreamWired {
		body, ok := bodies[h]
		if !ok {
			t.Errorf("handler %q pakette bulunamadı (yeniden adlandırıldı mı?)", h)
			continue
		}
		if !strings.Contains(body, "s.deliverExplain(") {
			t.Errorf("%s deliverExplain çağırmıyor — akan kip o uçta ÖLÜ", h)
		}
		// Eski çıkış yolu kalıntısı: iki çıkış = biri sessizce kazanır.
		if strings.Contains(body, `writeJSON(w, map[string]any{
			"explanation"`) || strings.Contains(body, `"explanation": out`) {
			t.Errorf("%s hâlâ elle explanation gövdesi yazıyor", h)
		}
	}
}

// serverFuncBodies — paketteki `func (s *Server) NAME(` gövdelerini
// kaba biçimde ayıklar (kaynak pini için yeterli; go/ast'a gerek yok).
func serverFuncBodies(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	head := regexp.MustCompile(`^func \(s \*Server\) ([A-Za-z0-9_]+)\(`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", f, err)
		}
		lines := strings.Split(string(b), "\n")
		for i := 0; i < len(lines); i++ {
			m := head.FindStringSubmatch(lines[i])
			if m == nil {
				continue
			}
			var sb strings.Builder
			for j := i + 1; j < len(lines); j++ {
				if lines[j] == "}" {
					break
				}
				sb.WriteString(lines[j])
				sb.WriteString("\n")
			}
			out[m[1]] = sb.String()
		}
	}
	return out
}
