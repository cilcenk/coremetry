package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/printer"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/devops"
	"github.com/cilcenk/coremetry/internal/stackparse"
)

// copilot_code_test.go — v0.9.831 "Kodu da incele".
//
// Tüm sahte sunucular JENERİKtir; gerçek bir müşteri host'u, koleksiyonu
// ya da uygulama adı bu depoya girmez.

// ── opsiyonel gövde ──────────────────────────────────────────────

// TestDecodeExplainOptions — GERİYE UYUMLULUK pini. Bu uçlar bugüne
// kadar gövdesiz POST alıyor; boş/eksik/bozuk gövde 400 değil,
// includeCode=false demek.
func TestDecodeExplainOptions(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"gövde yok (mevcut çağıranlar)", "", false},
		{"boş JSON", "{}", false},
		{"yalnız boşluk", "   \n", false},
		{"açıkça false", `{"includeCode":false}`, false},
		{"açıkça true", `{"includeCode":true}`, true},
		{"bilinmeyen alanlarla birlikte", `{"includeCode":true,"foo":1}`, true},
		{"bozuk JSON → varsayılan, 400 DEĞİL", `{"includeCode":`, false},
		{"JSON olmayan gövde", `not json at all`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/copilot/explain-exception/fp1",
				strings.NewReader(tc.body))
			if got := decodeExplainOptions(r).IncludeCode; got != tc.want {
				t.Fatalf("IncludeCode=%v, istenen %v", got, tc.want)
			}
		})
	}
	t.Run("nil body", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/copilot/explain-trace/t1", nil)
		r.Body = nil
		if decodeExplainOptions(r).IncludeCode {
			t.Fatal("nil gövde true döndürdü")
		}
	})
}

// ── taşma teşhisi ────────────────────────────────────────────────

// TestIsContextOverflowErr — 400 ≠ taşma. Yalnız taşmaya işaret eden
// 400'lerde kod yarıya iner; response_format reddi gibi başka bir 400
// yanlış teşhisle ikinci bir çağrı yakmamalı.
func TestIsContextOverflowErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			"vLLM maximum context length",
			errors.New("openai: 400 This model's maximum context length is 8192 tokens. However, you requested 9500"),
			true,
		},
		{
			"Ollama input length exceeds",
			errors.New("copilot: http 400: input length exceeds context length"),
			true,
		},
		{
			"Anthropic prompt is too long",
			errors.New("anthropic 400 invalid_request_error: prompt is too long: 210000 tokens > 200000 maximum"),
			true,
		},
		{"413 request too large", errors.New("http 413: request entity too large"), true},
		{
			// Mevcut JSON-mode merdiveninin 400'ü — TAŞMA DEĞİL.
			"response_format reddi",
			errors.New("openai: 400 unknown parameter: response_format"),
			false,
		},
		{"düz 400", errors.New("http 400: bad request"), false},
		{"401", errors.New("http 401: unauthorized"), false},
		{"500", errors.New("http 500: maximum context length is 8192"), false},
		{"timeout", errors.New("context deadline exceeded"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContextOverflowErr(tc.err); got != tc.want {
				t.Fatalf("isContextOverflowErr=%v, istenen %v (%v)", got, tc.want, tc.err)
			}
		})
	}
}

// ── sahte sağlayıcı + kayıt edici ────────────────────────────────

// capRecorder — ai_calls sink'i yerine geçen kayıt yakalayıcı.
type capRecorder struct {
	mu   sync.Mutex
	recs []copilot.CallRecord
	done chan struct{}
}

func newCapRecorder() *capRecorder { return &capRecorder{done: make(chan struct{}, 8)} }

func (c *capRecorder) RecordCall(_ context.Context, rec copilot.CallRecord) {
	c.mu.Lock()
	c.recs = append(c.recs, rec)
	c.mu.Unlock()
	c.done <- struct{}{}
}

func (c *capRecorder) wait(t *testing.T, n int) []copilot.CallRecord {
	t.Helper()
	for i := 0; i < n; i++ {
		<-c.done
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]copilot.CallRecord(nil), c.recs...)
}

// fakeProvider — OpenAI uyumlu sahte uç. prompts, sağlayıcıya GERÇEKTEN
// giden gövdeleri toplar; overflowFirst true ise ilk çağrı bağlam
// taşması 400'ü döner.
type fakeProvider struct {
	srv           *httptest.Server
	mu            sync.Mutex
	prompts       []string
	overflowFirst bool
	calls         int
}

func newFakeProvider(t *testing.T, overflowFirst bool) *fakeProvider {
	t.Helper()
	f := &fakeProvider{overflowFirst: overflowFirst}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var sb strings.Builder
		for _, m := range body.Messages {
			sb.WriteString(m.Content)
			sb.WriteString("\n")
		}
		f.mu.Lock()
		f.calls++
		n := f.calls
		f.prompts = append(f.prompts, sb.String())
		f.mu.Unlock()

		if f.overflowFirst && n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"This model's maximum context length is 8192 tokens. However, you requested 12000 tokens"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"kök neden: 246. satırdaki null kontrolü eksik"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProvider) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prompts...)
}

// codeServer — s.copilot'u sahte sağlayıcıya bağlı bir Server.
func codeServer(t *testing.T, fp *fakeProvider, rec copilot.Recorder) *Server {
	t.Helper()
	cop := copilot.New(copilot.ProviderOpenAI, "test-key", "gemma4")
	cop.Configure(copilot.ProviderOpenAI, "test-key", "gemma4", fp.srv.URL, false, true)
	cop.SetRecorder(rec)
	if !cop.Active() {
		t.Fatal("sahte copilot aktif değil")
	}
	return &Server{copilot: cop}
}

// ── uçtan uca: sahte TFS + sahte sağlayıcı ───────────────────────

const secretCodeMarker = "SECRET_BUSINESS_RULE_MARKER"

// fetchFakeCodeContext — sahte TFS'ten gerçek FetchCode akışıyla
// (listing → eşleşme → pencere) kod bağlamı üretir.
func fetchFakeCodeContext(t *testing.T) devops.CodeContext {
	t.Helper()
	const path = "/src/main/java/com/example/card/CardDetailBusiness.java"
	files := map[string]string{}
	var sb strings.Builder
	sb.WriteString("package com.example.card;\n")
	for i := 2; i <= 400; i++ {
		if i == 246 {
			fmt.Fprintf(&sb, "    if (hostResponse == null) { throw new HostException(\"%s\"); }\n", secretCodeMarker)
			continue
		}
		// Uzun dolgu satırları BİLİNÇLİ: ±30 satırlık pencere kod
		// bütçesini gerçekten dolduracak boyda olmalı ki yarıya-indirme
		// yolu testte gerçekten tetiklensin.
		fmt.Fprintf(&sb, "    // dolgu satiri %d — bu satir pencere butcesini doldurmak icin uzun tutuldu\n", i)
	}
	files[path] = sb.String()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, q := r.URL.Path, r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(p, "/refs"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]string{{"name": "refs/heads/release"}, {"name": "refs/heads/master"}},
			})
		case strings.HasSuffix(p, "/items") && q.Get("recursionLevel") == "Full":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{{"path": path, "gitObjectType": "blob"}},
			})
		case strings.HasSuffix(p, "/items"):
			_ = json.NewEncoder(w).Encode(map[string]any{"content": files[q.Get("path")]})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"defaultBranch": "refs/heads/master"})
		}
	}))
	t.Cleanup(srv.Close)

	dv := devops.New()
	dv.Configure(devops.Settings{
		BaseURL: srv.URL, Collection: "DefaultCollection", Project: "Payments",
		PAT: "test-pat", Flavor: devops.FlavorServer,
	})
	stack := "\tat deployment.APPWEB.war//com.example.card.CardDetailBusiness.handle(CardDetailBusiness.java:246)\n"
	// v0.9.1183 — üçüncü arg proje ÖNERİSİ (servis önekinden). Burada boş:
	// ayarda Project açıkça verilmiş ve açık ayar öneriyi ezmeli.
	cc := dv.FetchCode(context.Background(), "core-service", devops.ProjectHint{}, stackparse.ParseJava(stack))
	if cc.Empty() {
		t.Fatalf("sahte TFS'ten kod çekilemedi: %s", cc.Reason)
	}
	cc.Source = devops.RepoSourceConvention
	return cc
}

// TestExplainCodeMasksPromptInAICalls — SÖZLEŞMENİN PİNİ.
//
// Sağlayıcıya giden prompt kodu TAŞIR; ai_calls kaydına giren örnek
// TAŞIMAZ, yerine `[kod: repo/dosya:aralık · N satır]` özeti geçer.
// PromptChars ise GERÇEK prompt boyutunu bildirir — maskeli bir boyut
// çağrının maliyetini olduğundan küçük gösterirdi.
// summaryRange — maskeli kayıttaki "[kod: …:FROM-TO · N satır]"
// aralığını okur (v0.9.1239). Aralık artık bütçeye göre kaydığı için
// test sabit sayı yerine KAPSAMA bakıyor.
func summaryRange(t *testing.T, sample, head string) (int, int) {
	t.Helper()
	i := strings.Index(sample, head)
	if i < 0 {
		t.Fatalf("özet başlığı yok: %q", head)
	}
	rest := sample[i+len(head):]
	j := strings.Index(rest, " ")
	if j < 0 {
		t.Fatalf("özet aralığı okunamadı: %q", rest)
	}
	var from, to int
	if _, err := fmt.Sscanf(rest[:j], "%d-%d", &from, &to); err != nil {
		t.Fatalf("özet aralığı ayrıştırılamadı (%q): %v", rest[:j], err)
	}
	return from, to
}

// v0.9.1243 — pin ÜÇ VAKAYA genişledi: tam isabet, kısmi, ıska.
// Üçü BİRLİKTE olmak zorunda, çünkü sözleşme bir varlık değil bir
// AYRIM: ai_calls satırına bakan operatör "kod hiç istenmedi", "geldi",
// "geldi ama eksik" ve "istendi, gelmedi" hâllerini birbirinden
// ayırabilmeli. Yalnız isabet vakasını pinlemek, ıska yarısının sessizce
// işaretsiz kalmasına 1241'e kadar izin veren boşluğun ta kendisiydi.
func TestExplainCodeMasksPromptInAICalls(t *testing.T) {
	// Sahte fikstür bütçeyi doldurur → gerçek akışta KISMİ isabet.
	cc := fetchFakeCodeContext(t)
	if cc.Outcome != devops.CodePartial {
		t.Fatalf("fikstür sınıfı=%q, bu vaka kısmi isabeti pinliyor", cc.Outcome)
	}
	fp := newFakeProvider(t, false)
	rec := newCapRecorder()
	s := codeServer(t, fp, rec)

	r := httptest.NewRequest(http.MethodPost, "/api/copilot/explain-exception/fp1", nil)
	user := "Exception GRUBU:\n```json\n{\"type\":\"HostException\"}\n```"
	out, err := s.copilotExplainCode(r,
		copilot.SystemPromptException(), copilot.SystemPromptExceptionWithCode(), user, cc)
	if err != nil {
		t.Fatalf("explain hata verdi: %v", err)
	}
	if out == "" {
		t.Fatal("boş cevap")
	}

	// (1) Sağlayıcıya giden prompt kodu TAŞIR.
	sent := fp.sent()
	if len(sent) != 1 {
		t.Fatalf("sağlayıcı çağrısı sayısı=%d, istenen 1", len(sent))
	}
	if !strings.Contains(sent[0], secretCodeMarker) {
		t.Fatal("kod sağlayıcıya GİTMEDİ — özelliğin kendisi kırık")
	}
	if !strings.Contains(sent[0], "KOD BAĞLAMI") {
		t.Fatal("kod bağlamı bloğu prompt'ta yok")
	}
	// Kod prompt'u kullanıldı mı? (kodsuz taban değil)
	if !strings.Contains(sent[0], "bu pencerede görünmüyor") {
		t.Error("kod ekli system prompt'u kullanılmamış")
	}

	// (2) ai_calls kaydı kodu TAŞIMAZ.
	recs := rec.wait(t, 1)
	if len(recs) != 1 {
		t.Fatalf("ai_calls kaydı sayısı=%d, istenen 1", len(recs))
	}
	got := recs[0]
	if strings.Contains(got.PromptSample, secretCodeMarker) {
		t.Fatalf("KOD ai_calls kaydına sızdı:\n%s", got.PromptSample)
	}
	if strings.Contains(got.PromptSample, "dolgu 2") || strings.Contains(got.PromptSample, "```java") {
		t.Fatalf("kod gövdesi ai_calls kaydında:\n%s", got.PromptSample)
	}
	// Özet: hangi depo + hangi dosya + hangi aralık + kaç satır.
	// Aralık, bütçe kırpmasından SONRAKİ gerçek aralıktır — kayıt
	// "modele ne gitti"yi söylemeli, ne göndermeyi planladığımızı değil.
	//
	// v0.9.1239 — pin BİLİNÇLİ güncellendi: eskiden başlangıç satırı
	// (216) sabitleniyordu çünkü kırpma pencereyi BAŞTAN kesiyordu.
	// Kırpma artık hata satırını merkezde tutuyor, yani başlangıç
	// bütçeye göre kayıyor; sabitlenecek doğru şey sayı değil
	// SÖZLEŞMEdir: aralık hata satırını (246) İÇERMELİ.
	const codeSummaryHead = "[kod: core-service/src/main/java/com/example/card/CardDetailBusiness.java:"
	if !strings.Contains(got.PromptSample, codeSummaryHead) ||
		!strings.Contains(got.PromptSample, " satır]") {
		t.Fatalf("maskeli kayıtta kaynak özeti yok:\n%s", got.PromptSample)
	}
	if from, to := summaryRange(t, got.PromptSample, codeSummaryHead); from > 246 || to < 246 {
		t.Fatalf("kaydedilen aralık %d-%d hata satırını (246) kapsamıyor", from, to)
	}
	// Kod dışı bağlam kayıtta AYNEN durmalı — maskeleme yalnız kodu alır.
	if !strings.Contains(got.PromptSample, "Exception GRUBU") {
		t.Fatalf("kod dışı bağlam kayıttan düştü:\n%s", got.PromptSample)
	}
	// (3) PromptChars GERÇEK boyut: maskeli örnekten büyük olmalı.
	if int(got.PromptChars) <= len(got.PromptSample) {
		t.Errorf("PromptChars=%d maskeli örnek uzunluğundan (%d) büyük değil — maliyet olduğundan küçük raporlanıyor",
			got.PromptChars, len(got.PromptSample))
	}
	if got.Surface != "explain-exception" {
		t.Errorf("Surface=%q, istenen explain-exception (/ai atıfı)", got.Surface)
	}
	// (4) KISMİ isabet: özetin yanında KAYIP da yazılı. Pencere listesi
	// tek başına "kod geldi" der; bütçenin kestiğini söylemeyen bir
	// kayıt, modelin eksik kanıtla konuştuğunu gizler.
	if !strings.Contains(got.PromptSample, "(kısmi: ") {
		t.Fatalf("kısmi isabette kayıp notu yok:\n%s", got.PromptSample)
	}
	if strings.Contains(got.PromptSample, "[kod alınamadı") {
		t.Fatalf("kod GELDİĞİ hâlde ıska işareti yazılmış:\n%s", got.PromptSample)
	}

	t.Run("tam isabet — özet var, kayıp notu YOK", func(t *testing.T) {
		fp := newFakeProvider(t, false)
		rec := newCapRecorder()
		s := codeServer(t, fp, rec)
		clean := devops.CodeContext{
			Repo: "core-service", Branch: "release", Outcome: devops.CodeOK,
			Windows: []devops.CodeWindow{{
				Path: "/src/A.java", Frame: "com.example.A.handle(A.java:12)",
				Line: 12, FromLine: 10, ToLine: 14,
				Content: "12| throw new HostException(\"" + secretCodeMarker + "\");",
			}},
		}
		r := httptest.NewRequest(http.MethodPost, "/api/copilot/explain-exception/fp1", nil)
		if _, err := s.copilotExplainCode(r,
			copilot.SystemPromptException(), copilot.SystemPromptExceptionWithCode(),
			"Exception GRUBU: x", clean); err != nil {
			t.Fatalf("explain hata verdi: %v", err)
		}
		if !strings.Contains(fp.sent()[0], secretCodeMarker) {
			t.Fatal("kod sağlayıcıya gitmedi")
		}
		sample := rec.wait(t, 1)[0].PromptSample
		if strings.Contains(sample, secretCodeMarker) {
			t.Fatalf("KOD ai_calls kaydına sızdı:\n%s", sample)
		}
		if !strings.Contains(sample, "[kod: core-service/src/A.java:10-14 · 5 satır]") {
			t.Fatalf("özet yok:\n%s", sample)
		}
		if strings.Contains(sample, "kısmi") || strings.Contains(sample, "[kod alınamadı") {
			t.Fatalf("tertemiz isabet kayıplı gösterildi:\n%s", sample)
		}
	})

	t.Run("ıska — [kod alınamadı: sınıf]", func(t *testing.T) {
		fp := newFakeProvider(t, false)
		rec := newCapRecorder()
		s := codeServer(t, fp, rec)
		miss := devops.CodeContext{
			Repo:    "core-service",
			Reason:  "ağaçta eşleşen dosya yok: CardDetailBusiness.java",
			Outcome: devops.CodeTreeMiss,
		}
		r := httptest.NewRequest(http.MethodPost, "/api/copilot/explain-exception/fp1", nil)
		out, err := s.copilotExplainCode(r,
			copilot.SystemPromptException(), copilot.SystemPromptExceptionWithCode(),
			"Exception GRUBU: x", miss)
		if err != nil || out == "" {
			t.Fatalf("fail-open bozuldu: out=%q err=%v", out, err)
		}
		// GERÇEK prompt'a işaret GİRMEZ — model olmayan kanıt hakkında
		// konuşmasın; işaret yalnız kayda aittir.
		if strings.Contains(fp.sent()[0], "[kod alınamadı") {
			t.Fatalf("ıska işareti sağlayıcıya giden prompt'a sızdı:\n%s", fp.sent()[0])
		}
		sample := rec.wait(t, 1)[0].PromptSample
		if !strings.Contains(sample, "[kod alınamadı: tree-miss]") {
			t.Fatalf("ıska işareti kayda yazılmadı — satır 'hiç istenmedi'den ayırt edilemez:\n%s", sample)
		}
		if strings.Contains(sample, "[kod: ") {
			t.Fatalf("kod gelmediği hâlde isabet özeti yazılmış:\n%s", sample)
		}
		if !strings.Contains(sample, "Exception GRUBU") {
			t.Fatalf("kod dışı bağlam kayıttan düştü:\n%s", sample)
		}
	})
}

// TestExplainCodeRetriesHalvedOnOverflow — bağlam taşması 400'ünde kod
// YARIYA inip BİR kez yeniden denenir; ikinci deneme daha kısa kod
// taşır ve cevap üretilir.
func TestExplainCodeRetriesHalvedOnOverflow(t *testing.T) {
	cc := fetchFakeCodeContext(t)
	fp := newFakeProvider(t, true) // ilk çağrı taşma 400'ü
	rec := newCapRecorder()
	s := codeServer(t, fp, rec)

	r := httptest.NewRequest(http.MethodPost, "/api/copilot/explain-exception/fp1", nil)
	out, err := s.copilotExplainCode(r,
		copilot.SystemPromptException(), copilot.SystemPromptExceptionWithCode(), "Exception GRUBU: x", cc)
	if err != nil {
		t.Fatalf("yeniden deneme sonrası hâlâ hata: %v", err)
	}
	if out == "" {
		t.Fatal("boş cevap")
	}

	sent := fp.sent()
	if len(sent) != 2 {
		t.Fatalf("sağlayıcı çağrısı=%d, istenen 2 (bir taşma + bir yeniden deneme)", len(sent))
	}
	if len(sent[1]) >= len(sent[0]) {
		t.Fatalf("ikinci prompt kısalmadı: %d → %d", len(sent[0]), len(sent[1]))
	}
	// Kod hâlâ VAR (tümüyle atılmadı) — yarıya indi.
	if !strings.Contains(sent[1], "KOD BAĞLAMI") {
		t.Error("yeniden denemede kod bağlamı tümüyle düşmüş; istenen YARIYA inmesi")
	}
	// v0.9.1239 — YARIYA İNEN pencere hâlâ HATA SATIRINI taşımalı.
	// Denetim bulgusunun tam senaryosu buydu: Halved() bütçeyi 2000'e
	// indiriyor, baştan kesen kırpma 246'yı pencerenin dışında
	// bırakıyor, başlık ise onu göstermeye devam ediyordu — model
	// göremediği satırdan kök neden uyduruyordu.
	if !strings.Contains(sent[1], ">>> 246| ") {
		t.Fatalf("yarıya inen pencerede işaretli hata satırı yok:\n%s", sent[1])
	}
	if !strings.Contains(sent[1], secretCodeMarker) {
		t.Fatal("yarıya inen pencere hata satırının KODUNU taşımıyor")
	}
	// SADECE BİR kez: üçüncü bir deneme yok.
	recs := rec.wait(t, 2)
	if len(recs) != 2 {
		t.Fatalf("ai_calls kaydı=%d, istenen 2", len(recs))
	}
	// Maskeleme yeniden denemede de geçerli.
	for i, rc := range recs {
		if strings.Contains(rc.PromptSample, secretCodeMarker) {
			t.Fatalf("%d. kayıtta kod sızdı", i+1)
		}
	}
}

// TestExplainCodeFallsBackWhenNoCode — kod bağlamı boşsa KODSUZ
// prompt'la normal yol işler (fail-open); tek çağrı, maskeleme yok.
func TestExplainCodeFallsBackWhenNoCode(t *testing.T) {
	fp := newFakeProvider(t, false)
	rec := newCapRecorder()
	s := codeServer(t, fp, rec)

	r := httptest.NewRequest(http.MethodPost, "/api/copilot/explain-exception/fp1", nil)
	empty := devops.CodeContext{Reason: "depo çözülemedi"}
	out, err := s.copilotExplainCode(r,
		copilot.SystemPromptException(), copilot.SystemPromptExceptionWithCode(), "Exception GRUBU: x", empty)
	if err != nil || out == "" {
		t.Fatalf("fail-open çalışmadı: out=%q err=%v", out, err)
	}
	sent := fp.sent()
	if len(sent) != 1 {
		t.Fatalf("sağlayıcı çağrısı=%d, istenen 1", len(sent))
	}
	if strings.Contains(sent[0], "KOD BAĞLAMI") {
		t.Fatal("kod yokken kod ekli prompt gönderilmiş — modele olmayan kanıt vaat ediliyor")
	}
	recs := rec.wait(t, 1)
	if strings.Contains(recs[0].PromptSample, "[kod:") {
		t.Fatal("kod yokken maskeleme özeti yazılmış")
	}
	// v0.9.1243 — sınıf YOKKEN gerekçe yazılır. Bu dal bilerek ayrı
	// pinleniyor: sınıfsız bir bağlam (elle kurulmuş ya da taksonomi
	// dışı bir çıkış) işaretsiz kalırsa, satır yine "hiç istenmedi"
	// gibi okunur.
	if !strings.Contains(recs[0].PromptSample, "[kod alınamadı: depo çözülemedi]") {
		t.Fatalf("sınıfsız ıskada gerekçe işareti yok:\n%s", recs[0].PromptSample)
	}
}

// TestCodePayload — yanıtın `code` alanı: kod GÖVDESİ tarayıcıya
// gitmez, yalnız hangi dosyanın hangi aralığı okundu.
func TestCodePayload(t *testing.T) {
	cc := devops.CodeContext{
		Repo: "core-service", Branch: "release", Source: devops.RepoSourceConvention,
		Windows: []devops.CodeWindow{{
			Path: "/src/A.java", FromLine: 10, ToLine: 70,
			Content: "10| " + secretCodeMarker,
		}},
	}
	t.Run("istenmediyse alan hiç yok", func(t *testing.T) {
		if codePayload(cc, false) != nil {
			t.Fatal("includeCode=false iken code alanı üretildi")
		}
	})
	t.Run("istendiyse yalnız referans", func(t *testing.T) {
		p := codePayload(cc, true)
		b, _ := json.Marshal(p)
		if strings.Contains(string(b), secretCodeMarker) {
			t.Fatalf("kod gövdesi API yanıtına sızdı: %s", b)
		}
		if p.Repo != "core-service" || p.Branch != "release" || p.Source != devops.RepoSourceConvention {
			t.Fatalf("kaynak satırı bilgisi eksik: %+v", p)
		}
		if len(p.Files) != 1 || p.Files[0].Path != "/src/A.java" ||
			p.Files[0].FromLine != 10 || p.Files[0].ToLine != 70 {
			t.Fatalf("dosya referansı hatalı: %+v", p.Files)
		}
	})
	t.Run("boş bağlamda neden taşınır", func(t *testing.T) {
		p := codePayload(devops.CodeContext{Reason: "depo erişilemedi"}, true)
		if p == nil || p.Reason != "depo erişilemedi" {
			t.Fatalf("dürüst not taşınmadı: %+v", p)
		}
		if len(p.Files) != 0 {
			t.Fatal("boş bağlamda dosya listesi dolu")
		}
	})
}

// TestExplainCodeDropsCodeWhenHalvingCannotShrink — kod zaten bütçenin
// yarısından küçükken taşma geldiyse, kod prompt'un sorunu DEĞİLDİR.
// Yarıya indirmek hiçbir şey kazandırmayacağı için elimizdeki tek kol
// kalır: kodu tümüyle bırakıp kodsuz dene. Operatör cevapsız kalmaz.
func TestExplainCodeDropsCodeWhenHalvingCannotShrink(t *testing.T) {
	fp := newFakeProvider(t, true) // ilk çağrı taşma 400'ü
	rec := newCapRecorder()
	s := codeServer(t, fp, rec)

	// Tek satırlık minik pencere: yarıya indirme onu kırpamaz.
	tiny := devops.CodeContext{
		Repo: "core-service", Branch: "release",
		Windows: []devops.CodeWindow{{
			Path: "/src/A.java", FromLine: 9, ToLine: 9, Content: "9| int x = 1;",
		}},
	}
	r := httptest.NewRequest(http.MethodPost, "/api/copilot/explain-exception/fp1", nil)
	out, err := s.copilotExplainCode(r,
		copilot.SystemPromptException(), copilot.SystemPromptExceptionWithCode(), "Exception GRUBU: x", tiny)
	if err != nil || out == "" {
		t.Fatalf("kodsuz geri düşüş çalışmadı: out=%q err=%v", out, err)
	}
	sent := fp.sent()
	if len(sent) != 2 {
		t.Fatalf("sağlayıcı çağrısı=%d, istenen 2", len(sent))
	}
	if strings.Contains(sent[1], "KOD BAĞLAMI") {
		t.Fatal("ikinci denemede kod hâlâ var — kırpılamayan kod bırakılmalıydı")
	}
	rec.wait(t, 2)
}

// ── katalog pini: HATA ≠ BOŞ (v0.9.1236) ─────────────────────────

// TestPinReadDecision — üç hâl, üç farklı sonuç.
//
// Neden bu ayrım: bu tek adımda fail-open'ın YÖNÜ değişiyor. Operatör
// bir depo pinlediyse, konvansiyonun VAR OLAN ama YANLIŞ bir depoya
// çözüldüğünü zaten biliyor demektir. Pin okunamadığında konvansiyona
// düşmek, tam da operatörün kapattığı hatayı geri açar ve BAŞKA bir
// uygulamanın kaynağını "kanıt" diye modele koyar. Eksik kanıt
// kurtarılır, yanlış kanıt kurtarılamaz.
func TestPinReadDecision(t *testing.T) {
	cases := []struct {
		name      string
		repo      string
		found     bool
		err       error
		wantPin   string
		wantAbort bool
	}{
		{
			name: "pin var", repo: "CashManagement.CashFlow", found: true,
			wantPin: "CashManagement.CashFlow",
		},
		{
			name: "pin var, boşluklu", repo: "  Payments.Core  ", found: true,
			wantPin: "Payments.Core",
		},
		{
			// Satır yok = servis henüz küratörlenmemiş. NORMAL hâl;
			// konvansiyona düşmek doğru.
			name: "satır yok → konvansiyon", found: false,
		},
		{
			// Satır var ama repository boş: operatör diğer alanları
			// doldurmuş, depoyu bilerek boş bırakmış.
			name: "satır var, depo boş → konvansiyon", repo: "", found: true,
		},
		{
			// ASIL DÜZELTME: geçici CH arızası artık "pin yok" gibi
			// okunmuyor.
			name: "okuma hatası → İPTAL", err: errors.New("read: i/o timeout"),
			wantAbort: true,
		},
		{
			name: "okuma hatası, satır da yok → İPTAL", found: false,
			err: errors.New("dial tcp: connection refused"), wantAbort: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pin, abort := pinReadDecision(tc.repo, tc.found, tc.err)
			if pin != tc.wantPin {
				t.Fatalf("pin=%q, istenen %q", pin, tc.wantPin)
			}
			if (abort != "") != tc.wantAbort {
				t.Fatalf("abort=%q, iptal beklendi mi: %v", abort, tc.wantAbort)
			}
			if tc.wantAbort {
				// "katalog" DEĞİL: Türkçe ek ünsüz yumuşatıyor
				// (katalog → kataloğu) ve önek assert'i tutmaz.
				if !strings.Contains(abort, "okunamadı") {
					t.Fatalf("iptal nedeni operatöre ne olduğunu söylemiyor: %q", abort)
				}
				// Hata METNİ taşınmamalı: bu dize ekrana ve AI
				// yüzeyine gidiyor, CH hatası bağlantı dizesini
				// (parola dahil) içerebilir.
				if strings.Contains(abort, tc.err.Error()) {
					t.Fatalf("ham hata metni operatöre taşındı: %q", abort)
				}
			}
		})
	}
}

// TestCodeContextUsesStrictMetadataRead — BAĞLANMA kapısı (v0.9.1236).
//
// pinReadDecision saf ve testli, ama tek başına hiçbir şey kanıtlamaz:
// çağıran yumuşak GetServiceMetadata'ya geri dönerse `err` ASLA dolmaz,
// saf test yemyeşil kalır ve düzeltme kendini sessizce iptal eder. Bu
// kapı, kod bağlamı yolunun HATA KORUYAN kapıyı kullandığını pinler.
//
// Kaynak YORUMSUZ ayrıştırılıyor (go/parser, ParseComments YOK): bir
// yorum satırında geçen "GetServiceMetadata(" kapıyı yanlışlıkla
// patlatmamalı — yorum bir çağıran değildir.
func TestCodeContextUsesStrictMetadataRead(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "copilot_code.go", nil, 0)
	if err != nil {
		t.Fatalf("copilot_code.go ayrıştırılamadı: %v", err)
	}
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, f); err != nil {
		t.Fatalf("yazdırılamadı: %v", err)
	}
	src := buf.String()

	if !strings.Contains(src, "GetServiceMetadataStrict(") {
		t.Fatal("kod bağlamı yolu GetServiceMetadataStrict kullanmıyor — " +
			"pin okuma hatası yeniden sessizce konvansiyona düşer")
	}
	// Önek çakışması tuzağı: "GetServiceMetadata" tek başına Strict'i de
	// yakalar. Paranteziyle aranıyor ki yalnız YUMUŞAK çağrı eşleşsin.
	if strings.Contains(src, "GetServiceMetadata(") {
		t.Fatal("kod bağlamı yolunda yumuşak GetServiceMetadata çağrısı var — " +
			"o kapı her okuma arızasını 'pin yok' diye okur")
	}
}
