// provider_parity_test.go — FAZ 1.1 canary'sinin GÜVENLİK KANITI.
//
// v0.9.112x itibarıyla explain-charts yüzeyi yeni transport'tan
// (internal/ai/provider) geçiyor, diğer 20 ✨ yüzeyi eski
// explainOpenAIWithUsage'dan. İki yol AYNI girdide AYNI şeyi
// üretmezse operatör bunu ancak canlıda, tek bir panelin bozulması
// olarak görür — bu test o farkı ship'ten önce yakalar.
//
// Pin: aynı Service, aynı prompt, aynı tuning ⇒ (a) tel üstündeki
// gövde birebir aynı, (b) header'lar aynı, (c) çözümlenmiş metin ve
// token sayıları aynı. Kaynak-grep değil gövde karşılaştırması:
// v0.9.1120'nin dersi, aynı sabitin farklı yazılışla geri gelmesini
// yalnız gövde testi yakalar.
package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
)

// parityRT — her isteğin gövdesini + seçili header'larını kaydeder ve
// sabit bir yanıt döndürür.
type parityRT struct {
	bodies  []map[string]any
	headers []http.Header
	body    string
}

func (p *parityRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil && m != nil {
			p.bodies = append(p.bodies, m)
		}
	}
	p.headers = append(p.headers, req.Header.Clone())
	body := p.body
	if body == "" {
		body = `{"choices":[{"message":{"content":"<think>düşünce</think>panel açıklaması"},"finish_reason":"stop"}],"usage":{"prompt_tokens":41,"completion_tokens":17}}`
	}
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		Request:    req,
	}, nil
}

// newParityService — gerçek kurucu + Configure, enjekte edilmiş
// transport. httptest yerine RoundTripper: gövdeyi ham hâlde ve
// header'larıyla birlikte görmek istiyoruz.
func newParityService(t *testing.T, mt int, temp *float64) (*Service, *parityRT) {
	t.Helper()
	s := New(ProviderOpenAI, "sk-canary", "model-x")
	s.Configure(ProviderOpenAI, "sk-canary", "model-x", "http://llm.invalid/v1", false, true)
	s.ConfigureTuning(mt, temp, 0)
	rt := &parityRT{}
	s.mu.Lock()
	s.cli = &http.Client{Transport: rt}
	s.mu.Unlock()
	return s, rt
}

// TestExplainCanaryParity_OpenAI — canary yüzeyi (explain-charts,
// YENİ transport) ile herhangi bir başka yüzey (explain-service, ESKİ
// üretici) aynı çağrıda ayırt edilemez olmalı.
func TestExplainCanaryParity_OpenAI(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tunings := []struct {
		name string
		mt   int
		temp *float64
	}{
		{"varsayılan tuning", 0, nil},
		{"operatör ezmesi", 8192, f(0.9)},
		{"deterministik (temperature 0)", 0, f(0)},
	}
	const sys, usr = "sistem promptu", "kullanıcı promptu"

	for _, tn := range tunings {
		t.Run(tn.name, func(t *testing.T) {
			canaryS, canaryRT := newParityService(t, tn.mt, tn.temp)
			legacyS, legacyRT := newParityService(t, tn.mt, tn.temp)

			canaryCtx := WithMeta(context.Background(), CallMeta{Surface: canarySurface, UserID: "u1"})
			legacyCtx := WithMeta(context.Background(), CallMeta{Surface: "explain-service", UserID: "u1"})

			// Kontrol: canary koşulu gerçekten ayrışıyor mu? Bu
			// olmadan test iki kez ESKİ yolu koşup "parite var"
			// diyebilirdi (yeşil-ama-etkisiz mutasyon sınıfı).
			if !canaryProvider(canaryCtx) {
				t.Fatal("canaryProvider(explain-charts) = false — canary hiç kurulmamış")
			}
			if canaryProvider(legacyCtx) {
				t.Fatal("canaryProvider(explain-service) = true — canary tek yüzeyle sınırlı değil")
			}

			canaryOut, canaryErr := canaryS.Explain(canaryCtx, sys, usr)
			legacyOut, legacyErr := legacyS.Explain(legacyCtx, sys, usr)

			if canaryErr != nil || legacyErr != nil {
				t.Fatalf("hata: canary=%v legacy=%v", canaryErr, legacyErr)
			}
			// (c) çözümlenmiş metin — <think> soyulmuş hâliyle.
			if canaryOut != legacyOut {
				t.Errorf("metin ayrıştı:\ncanary=%q\nlegacy=%q", canaryOut, legacyOut)
			}
			if canaryOut != "panel açıklaması" {
				t.Errorf("kurtarma zinciri uygulanmamış: %q", canaryOut)
			}

			// (a) tel üstündeki gövde.
			if len(canaryRT.bodies) != 1 || len(legacyRT.bodies) != 1 {
				t.Fatalf("istek sayısı: canary=%d legacy=%d, ikisi de 1 olmalı",
					len(canaryRT.bodies), len(legacyRT.bodies))
			}
			if !reflect.DeepEqual(canaryRT.bodies[0], legacyRT.bodies[0]) {
				cj, _ := json.Marshal(canaryRT.bodies[0])
				lj, _ := json.Marshal(legacyRT.bodies[0])
				t.Errorf("gövde ayrıştı:\ncanary=%s\nlegacy=%s", cj, lj)
			}

			// (b) header'lar — özellikle v0.8.384 api-key ikizi.
			for _, h := range []string{"Content-Type", "Authorization", "api-key"} {
				c, l := canaryRT.headers[0].Get(h), legacyRT.headers[0].Get(h)
				if c != l {
					t.Errorf("%s ayrıştı: canary=%q legacy=%q", h, c, l)
				}
			}
			if got := canaryRT.headers[0].Get("api-key"); got != "sk-canary" {
				t.Errorf("api-key ikizi kayıp: %q", got)
			}
		})
	}
}

// TestExplainCanaryParity_Errors — hata yolu da ayrışmamalı. Kota
// kesicisi (noteProviderError → isQuotaErr) hata METNİNDE " 429"
// arıyor: yeni transport farklı bir cümle kursaydı kesici sessizce
// silahsız kalırdı (v0.9.200 mekanizması).
func TestExplainCanaryParity_Errors(t *testing.T) {
	newFailing := func(status int, body string) *Service {
		s := New(ProviderOpenAI, "sk-canary", "model-x")
		s.Configure(ProviderOpenAI, "sk-canary", "model-x", "http://llm.invalid/v1", false, true)
		rt := &statusRT{status: status, body: body}
		s.mu.Lock()
		s.cli = &http.Client{Transport: rt}
		s.mu.Unlock()
		return s
	}
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"429 kota", 429, `{"error":"rate limit exceeded"}`},
		{"500 geçici", 500, `upstream boom`},
		{"200 ama boş içerik + length", 200, `{"choices":[{"message":{"content":""},"finish_reason":"length"}]}`},
		{"200 ama tamamen boş", 200, `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canaryS, legacyS := newFailing(tc.status, tc.body), newFailing(tc.status, tc.body)
			_, cErr := canaryS.Explain(WithMeta(context.Background(), CallMeta{Surface: canarySurface}), "s", "u")
			_, lErr := legacyS.Explain(WithMeta(context.Background(), CallMeta{Surface: "explain-service"}), "s", "u")
			if cErr == nil || lErr == nil {
				t.Fatalf("ikisi de hata vermeliydi: canary=%v legacy=%v", cErr, lErr)
			}
			if cErr.Error() != lErr.Error() {
				t.Fatalf("hata metni ayrıştı:\ncanary=%q\nlegacy=%q", cErr.Error(), lErr.Error())
			}
			// Kota kesici iki yolda da AYNI kararı vermeli.
			if canaryS.QuotaBackoffActive() != legacyS.QuotaBackoffActive() {
				t.Errorf("kota kesici ayrıştı: canary=%v legacy=%v",
					canaryS.QuotaBackoffActive(), legacyS.QuotaBackoffActive())
			}
			if tc.status == 429 && !canaryS.QuotaBackoffActive() {
				t.Error("429 canary yolunda kota kesicisini kurmadı")
			}
		})
	}
}

// statusRT — sabit statü + gövde döndüren minimal transport.
type statusRT struct {
	status int
	body   string
}

func (s *statusRT) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: s.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(s.body))),
		Request:    req,
	}, nil
}

// TestCanaryStaysOnOneSurface — canary'nin KAPSAMI. Faz 1.1 sözü
// "tek yüzey"; bu test o sözü yüzey listesi üzerinden pinler. Faz
// 1.2'de kapsam genişlerken bu test de bilinçli olarak güncellenir
// (kapsam sessizce kaymasın diye burada duruyor).
func TestCanaryStaysOnOneSurface(t *testing.T) {
	surfaces := []string{
		"explain-trace", "explain-span", "explain-problem", "explain-incident",
		"explain-anomaly", "explain-service", "explain-shift", "explain-alert-noise",
		"explain-log-patterns", "runbook", "compare-traces", "deploy-impact",
		"explain-slo", "explain-slow-query", "explain-exception", "rootcause-verdict",
		"problem-auto-explain", "exception-auto-explain", "chat-guided", "",
	}
	for _, sf := range surfaces {
		if canaryProvider(WithMeta(context.Background(), CallMeta{Surface: sf})) {
			t.Errorf("surface %q canary'ye girdi — Faz 1.1 kapsamı yalnız %q", sf, canarySurface)
		}
	}
	if !canaryProvider(WithMeta(context.Background(), CallMeta{Surface: canarySurface})) {
		t.Errorf("surface %q canary'ye girmedi", canarySurface)
	}
}

// TestCanaryRespectsJSONMode — savunma koşulu. explain-charts bugün
// saf prose; biri onu WithJSONMode/WithJSONSchema'ya sararsa yeni
// transport (Faz 1.1'de JSON basamağını desteklemiyor) devreye
// GİRMEMELİ. Aksi hâlde kısıt sessizce düşerdi.
func TestCanaryRespectsJSONMode(t *testing.T) {
	s, rt := newParityService(t, 0, nil)
	ctx := WithJSONMode(WithMeta(context.Background(), CallMeta{Surface: canarySurface}))
	if _, err := s.Explain(ctx, "s", "u"); err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if len(rt.bodies) != 1 {
		t.Fatalf("istek sayısı %d, want 1", len(rt.bodies))
	}
	// Eski yol response_format ekler; yeni yol bu dilimde ekleyemez.
	if _, ok := rt.bodies[0]["response_format"]; !ok {
		t.Error("JSON modu istenen çağrı canary'ye kaçtı — response_format kayboldu")
	}
}
