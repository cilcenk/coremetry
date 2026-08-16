// provider_parity_test.go — FAZ 1.2'nin GÜVENLİK KANITI.
//
// Faz 1.1'de bu dosya iki YOLU birbirine kıyaslıyordu (canary vs eski
// üretici). Faz 1.2'de eski üreticiler silindi, yani kıyaslanacak
// ikinci yol yok — pin artık ALTIN GÖVDE: Service.Explain'in üç
// sağlayıcıda tel üstüne koyduğu gövde + header'lar burada AÇIKÇA
// yazılı ve birebir karşılaştırılıyor.
//
// Neden altın gövde, kaynak-grep değil: v0.9.1120'nin dersi, aynı
// sabitin (max_tokens 1024) başka bir yazılışla geri gelmesini yalnız
// gövde testi yakalar. Silinen üreticiler geri gelemez ama YENİ bir
// alan sessizce düşebilir — anthropic'in sürüm header'ı düşerse o yol
// TAMAMEN kırılır ve bunu ancak canlıda görürüz.
package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// parityRT — her isteğin gövdesini + header'larını kaydeder ve HOST'a
// göre sağlayıcı-şekilli bir 200 döndürür (github jeton takası dahil).
type parityRT struct {
	reqs    []*http.Request
	bodies  []map[string]any
	headers []http.Header
	status  int
	body    string // boşsa host'a göre varsayılan
}

func (p *parityRT) RoundTrip(req *http.Request) (*http.Response, error) {
	p.reqs = append(p.reqs, req)
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil && m != nil {
			p.bodies = append(p.bodies, m)
		}
	}
	p.headers = append(p.headers, req.Header.Clone())

	status, body := p.status, p.body
	if status == 0 {
		status = 200
	}
	switch {
	case strings.Contains(req.URL.Host, "api.github.com"):
		// Jeton takası her zaman başarılı — testin konusu explain
		// gövdesi, takasın hata yolu değil.
		status, body = 200, `{"token":"tid=sess-tok","expires_at":99999999999}`
	case body != "":
	case strings.Contains(req.URL.Host, "api.anthropic.com"):
		body = `{"content":[{"type":"text","text":"panel açıklaması"}],"usage":{"input_tokens":41,"output_tokens":17}}`
	default:
		body = `{"choices":[{"message":{"content":"<think>düşünce</think>panel açıklaması"},"finish_reason":"stop"}],"usage":{"prompt_tokens":41,"completion_tokens":17}}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		Request:    req,
	}, nil
}

// newParityService — gerçek kurucu + Configure, enjekte edilmiş
// transport. httptest yerine RoundTripper: anthropic ve github yolları
// API host'unu SABİT tutuyor (bilinçli — bkz. provider/anthropic.go) ve
// bir test sunucusuna yönlendirilemez.
func newParityService(t *testing.T, prov, baseURL string, mt int, temp *float64) (*Service, *parityRT) {
	t.Helper()
	s := New(prov, "sk-test", "model-x")
	s.Configure(prov, "sk-test", "model-x", baseURL, false, true)
	s.ConfigureTuning(mt, temp, 0)
	rt := &parityRT{}
	s.mu.Lock()
	s.cli = &http.Client{Transport: rt}
	s.mu.Unlock()
	return s, rt
}

// jsonRoundTrip — beklenen gövdeyi JSON'dan geçirir ki sayılar
// float64'e dönsün ve DeepEqual gerçek gövdeyle aynı tipte kıyaslansın.
func jsonRoundTrip(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

const paritySys, parityUsr = "sistem promptu", "kullanıcı promptu"

// ─── altın gövde: openai-compat ─────────────────────────────────────

func TestExplainWireBody_OpenAI(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name     string
		mt       int
		temp     *float64
		wantTok  int
		wantTemp float64
	}{
		{"varsayılan tuning", 0, nil, 4096, 0.2},
		{"operatör ezmesi", 8192, f(0.9), 8192, 0.9},
		{"deterministik (temperature 0)", 0, f(0), 4096, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, rt := newParityService(t, ProviderOpenAI, "http://llm.invalid/v1", tc.mt, tc.temp)
			out, err := s.Explain(WithMeta(context.Background(), CallMeta{Surface: "explain-charts"}), paritySys, parityUsr)
			if err != nil {
				t.Fatalf("Explain: %v", err)
			}
			// Kurtarma zinciri: <think> soyulmuş hâli.
			if out != "panel açıklaması" {
				t.Errorf("metin = %q, want %q", out, "panel açıklaması")
			}
			if len(rt.bodies) != 1 {
				t.Fatalf("istek sayısı %d, want 1", len(rt.bodies))
			}
			want := jsonRoundTrip(t, map[string]any{
				"model":       "model-x",
				"max_tokens":  tc.wantTok,
				"temperature": tc.wantTemp,
				"messages": []map[string]any{
					{"role": "system", "content": paritySys},
					{"role": "user", "content": parityUsr},
				},
			})
			if !reflect.DeepEqual(rt.bodies[0], want) {
				g, _ := json.Marshal(rt.bodies[0])
				w, _ := json.Marshal(want)
				t.Errorf("gövde ayrıştı:\n got %s\nwant %s", g, w)
			}
			if got := rt.reqs[0].URL.String(); got != "http://llm.invalid/v1/chat/completions" {
				t.Errorf("URL = %s", got)
			}
			// v0.8.384 api-key ikizi: Bearer'ı anlamayan self-hosted
			// geçitler bununla doğruluyor.
			for h, want := range map[string]string{
				"Content-Type":  "application/json",
				"Authorization": "Bearer sk-test",
				"api-key":       "sk-test",
			} {
				if got := rt.headers[0].Get(h); got != want {
					t.Errorf("%s = %q, want %q", h, got, want)
				}
			}
		})
	}
}

// ─── altın gövde: anthropic ─────────────────────────────────────────

// TestExplainWireBody_Anthropic — temperature'ın VARLIĞI burada yeni
// (v0.9.1120): bu yol ~1000 sürüm boyunca hiç temperature göndermedi ve
// sabit 1024 max_tokens'la gitti. Alan gövdeden düşerse aynı soru
// sağlayıcıya göre farklı yanıtlanmaya geri döner.
func TestExplainWireBody_Anthropic(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name     string
		mt       int
		temp     *float64
		wantTok  int
		wantTemp float64
	}{
		{"varsayılan tuning", 0, nil, 4096, 0.2},
		{"operatör ezmesi", 8192, f(0.9), 8192, 0.9},
		{"deterministik (temperature 0)", 0, f(0), 4096, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, rt := newParityService(t, ProviderAnthropic, "", tc.mt, tc.temp)
			out, err := s.Explain(WithMeta(context.Background(), CallMeta{Surface: "explain-trace"}), paritySys, parityUsr)
			if err != nil {
				t.Fatalf("Explain: %v", err)
			}
			if out != "panel açıklaması" {
				t.Errorf("metin = %q", out)
			}
			if len(rt.bodies) != 1 {
				t.Fatalf("istek sayısı %d, want 1", len(rt.bodies))
			}
			want := jsonRoundTrip(t, map[string]any{
				"model":       "model-x",
				"max_tokens":  tc.wantTok,
				"temperature": tc.wantTemp,
				"system":      paritySys,
				"messages": []map[string]any{
					{"role": "user", "content": parityUsr},
				},
			})
			if !reflect.DeepEqual(rt.bodies[0], want) {
				g, _ := json.Marshal(rt.bodies[0])
				w, _ := json.Marshal(want)
				t.Errorf("gövde ayrıştı:\n got %s\nwant %s", g, w)
			}
			if got := rt.reqs[0].URL.String(); got != "https://api.anthropic.com/v1/messages" {
				t.Errorf("URL = %s — anthropic ucu SABİT, baseURL okunmaz", got)
			}
			// Anthropic-Version zorunlu: eksikse API 400 döner, yani bu
			// header düşerse anthropic yolu tamamen kırılır.
			for h, want := range map[string]string{
				"Content-Type":      "application/json",
				"X-Api-Key":         "sk-test",
				"Anthropic-Version": "2023-06-01",
			} {
				if got := rt.headers[0].Get(h); got != want {
					t.Errorf("%s = %q, want %q", h, got, want)
				}
			}
			// openai-compat'ın header'ları buraya SIZMAMALI.
			if got := rt.headers[0].Get("Authorization"); got != "" {
				t.Errorf("anthropic isteğinde Authorization = %q, olmamalı", got)
			}
		})
	}
}

// ─── altın gövde: github copilot ────────────────────────────────────

// TestExplainWireBody_GitHub — iki adımlı çağrı: (1) OAuth jetonu →
// oturum jetonu takası (DURUM, Service'te kaldı), (2) explain POST'u
// ÇÖZÜLMÜŞ jetonla. Bearer'da operatörün OAuth jetonu görünürse takas
// atlanmış demektir.
func TestExplainWireBody_GitHub(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name     string
		mt       int
		temp     *float64
		wantTok  int
		wantTemp float64
	}{
		{"varsayılan tuning", 0, nil, 4096, 0.2},
		{"operatör ezmesi", 8192, f(0.9), 8192, 0.9},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, rt := newParityService(t, ProviderGitHub, "", tc.mt, tc.temp)
			out, err := s.Explain(WithMeta(context.Background(), CallMeta{Surface: "explain-problem"}), paritySys, parityUsr)
			if err != nil {
				t.Fatalf("Explain: %v", err)
			}
			// Copilot yolunda kurtarma zinciri YOK — içerik olduğu gibi
			// döner (taşınan davranış, bkz. provider/github.go).
			if out != "<think>düşünce</think>panel açıklaması" {
				t.Errorf("metin = %q — github yolunda salvage uygulanmamalı", out)
			}
			if len(rt.reqs) != 2 {
				t.Fatalf("istek sayısı %d, want 2 (takas + explain)", len(rt.reqs))
			}
			if h := rt.reqs[0].URL.Host; h != "api.github.com" {
				t.Errorf("ilk istek %s, önce jeton takası olmalı", h)
			}
			if got := rt.reqs[1].URL.String(); got != "https://api.githubcopilot.com/chat/completions" {
				t.Errorf("explain URL = %s", got)
			}
			if len(rt.bodies) != 1 {
				t.Fatalf("gövdeli istek sayısı %d, want 1 (takas GET)", len(rt.bodies))
			}
			want := jsonRoundTrip(t, map[string]any{
				"model":       "model-x",
				"max_tokens":  tc.wantTok,
				"temperature": tc.wantTemp,
				"messages": []map[string]any{
					{"role": "system", "content": paritySys},
					{"role": "user", "content": parityUsr},
				},
			})
			if !reflect.DeepEqual(rt.bodies[0], want) {
				g, _ := json.Marshal(rt.bodies[0])
				w, _ := json.Marshal(want)
				t.Errorf("gövde ayrıştı:\n got %s\nwant %s", g, w)
			}
			// Entegrasyon header'ları kapı bekçisi: eksik olan 403 alır.
			for h, want := range map[string]string{
				"Content-Type":           "application/json",
				"Authorization":          "Bearer tid=sess-tok",
				"Editor-Version":         "vscode/1.85.0",
				"Editor-Plugin-Version":  "copilot-chat/0.12.0",
				"Copilot-Integration-Id": "vscode-chat",
				"User-Agent":             "GithubCopilot/1.155.0",
			} {
				if got := rt.headers[1].Get(h); got != want {
					t.Errorf("%s = %q, want %q", h, got, want)
				}
			}
		})
	}
}

// ─── hata semantiği + kota kesicisi ─────────────────────────────────

// TestExplainErrorSemantics_AllProviders — hata METNİ sözleşmedir.
// Kota kesicisi (noteProviderError → isQuotaErr) mesajda " 429"
// arıyor: transport farklı bir cümle kursaydı kesici sessizce silahsız
// kalırdı (v0.9.200 mekanizması).
func TestExplainErrorSemantics_AllProviders(t *testing.T) {
	cases := []struct {
		name     string
		prov     string
		baseURL  string
		status   int
		body     string
		wantErr  string
		wantQuot bool
	}{
		{"openai 429", ProviderOpenAI, "http://llm.invalid/v1", 429, `{"error":"rate limit exceeded"}`,
			`openai-compat 429: {"error":"rate limit exceeded"}`, true},
		{"openai 500", ProviderOpenAI, "http://llm.invalid/v1", 500, `upstream boom`,
			`openai-compat 500: upstream boom`, false},
		{"anthropic 429", ProviderAnthropic, "", 429, `{"type":"error","error":{"type":"rate_limit_error"}}`,
			`anthropic 429: {"type":"error","error":{"type":"rate_limit_error"}}`, true},
		{"anthropic 529 aşırı yük", ProviderAnthropic, "", 529, `overloaded`,
			`anthropic 529: overloaded`, false},
		{"github 429", ProviderGitHub, "", 429, `{"error":"quota exceeded"}`,
			`github copilot 429: {"error":"quota exceeded"}`, true},
		{"github 403 header reddi", ProviderGitHub, "", 403, `access denied`,
			`github copilot 403: access denied`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, rt := newParityService(t, tc.prov, tc.baseURL, 0, nil)
			rt.status, rt.body = tc.status, tc.body
			_, err := s.Explain(WithMeta(context.Background(), CallMeta{Surface: "explain-service"}), "s", "u")
			if err == nil {
				t.Fatal("hata bekleniyordu")
			}
			if err.Error() != tc.wantErr {
				t.Fatalf("hata metni:\n got %q\nwant %q", err.Error(), tc.wantErr)
			}
			if got := s.QuotaBackoffActive(); got != tc.wantQuot {
				t.Errorf("kota kesici = %v, want %v", got, tc.wantQuot)
			}
		})
	}
}

// TestExplainEmptyAnswerParity — boş yanıt teşhisleri de sözleşme:
// operatöre EYLEM söylüyorlar ("raise max_tokens", "[copilot] pod log").
func TestExplainEmptyAnswerParity(t *testing.T) {
	cases := []struct {
		name, body, wantErr string
	}{
		{"length ⇒ bütçe teşhisi",
			`{"choices":[{"message":{"content":""},"finish_reason":"length"}]}`,
			"model returned no answer — token budget exhausted by reasoning; raise max_tokens or disable thinking (e.g. Qwen3 /no_think)"},
		{"tamamen boş ⇒ genel teşhis",
			`{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`,
			"openai-compat: model returned empty content — no answer in content/reasoning. Check the model name + endpoint; a reasoning model may need /no_think. See the [copilot] pod log for the raw response"},
		{"choices yok",
			`{"choices":[]}`,
			"openai-compat: empty response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, rt := newParityService(t, ProviderOpenAI, "http://llm.invalid/v1", 0, nil)
			rt.body = tc.body
			_, err := s.Explain(context.Background(), "s", "u")
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("hata:\n got %v\nwant %q", err, tc.wantErr)
			}
		})
	}
}

// ─── kapsam: yüzey ayrımı KALMADI ───────────────────────────────────

// TestProviderPathCoversEverySurface — Faz 1.1'in kapsam kapısının
// GÜNCELLENMİŞ hâli. O test "yalnız explain-charts yeni yoldan geçsin"
// diyordu; Faz 1.2'de kapsam TÜM yüzeyler, yani sözleşme tersine
// döndü: hiçbir yüzey farklı bir gövde üretmemeli.
//
// Kapı burada duruyor ki kapsam sessizce daralmasın — biri yeniden
// yüzey-şartlı bir dal koyarsa (canary'nin geri gelmesi) gövdeler
// ayrışır ve bu test patlar.
func TestProviderPathCoversEverySurface(t *testing.T) {
	surfaces := []string{
		"explain-charts", "explain-trace", "explain-span", "explain-problem",
		"explain-incident", "explain-anomaly", "explain-service", "explain-shift",
		"explain-alert-noise", "explain-log-patterns", "runbook", "compare-traces",
		"deploy-impact", "explain-slo", "explain-slow-query", "explain-exception",
		"rootcause-verdict", "problem-auto-explain", "exception-auto-explain",
		"chat-guided", "",
	}
	var first map[string]any
	for _, sf := range surfaces {
		s, rt := newParityService(t, ProviderOpenAI, "http://llm.invalid/v1", 0, nil)
		if _, err := s.Explain(WithMeta(context.Background(), CallMeta{Surface: sf}), paritySys, parityUsr); err != nil {
			t.Fatalf("surface %q: %v", sf, err)
		}
		if len(rt.bodies) != 1 {
			t.Fatalf("surface %q: istek sayısı %d, want 1", sf, len(rt.bodies))
		}
		if first == nil {
			first = rt.bodies[0]
			continue
		}
		if !reflect.DeepEqual(rt.bodies[0], first) {
			g, _ := json.Marshal(rt.bodies[0])
			w, _ := json.Marshal(first)
			t.Errorf("surface %q gövdesi ayrıştı — yüzey-şartlı dal geri gelmiş:\n got %s\nwant %s", sf, g, w)
		}
	}
}

// TestJSONModeReachesProvider — Faz 1.1'de bu testin sözleşmesi
// TERSİYDİ: JSON isteyen çağrı yeni transport'a GİRMEMELİ idi (o dilim
// response_format'ı taşımıyordu). Faz 1.2'de merdiven taşındı, yani
// artık aynı çağrı yeni yoldan geçmeli VE kısıtı taşımalı.
func TestJSONModeReachesProvider(t *testing.T) {
	s, rt := newParityService(t, ProviderOpenAI, "http://llm.invalid/v1", 0, nil)
	rt.body = `{"choices":[{"message":{"content":"{\"a\":1}"},"finish_reason":"stop"}]}`
	ctx := WithJSONMode(WithMeta(context.Background(), CallMeta{Surface: "explain-charts"}))
	if _, err := s.Explain(ctx, "s", "u"); err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if len(rt.bodies) != 1 {
		t.Fatalf("istek sayısı %d, want 1", len(rt.bodies))
	}
	rf, ok := rt.bodies[0]["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("response_format kayboldu: %v", rt.bodies[0])
	}
	if rf["type"] != "json_object" {
		t.Errorf("response_format.type = %v, want json_object", rf["type"])
	}
}

// TestJSONModeNotSentToAnthropic — anthropic ve github yollarına
// response_format bugüne kadar HİÇ gönderilmedi; taşıma davranış
// değiştirmez. Bir yüzey WithJSONMode ile gelirse kısıtsız çağrı
// yapılır (hata DEĞİL) — aksi hâlde JSON isteyen 4 yüzey anthropic
// kurulumlarında tamamen kırılırdı.
func TestJSONModeNotSentToAnthropic(t *testing.T) {
	for _, prov := range []string{ProviderAnthropic, ProviderGitHub} {
		t.Run(prov, func(t *testing.T) {
			s, rt := newParityService(t, prov, "", 0, nil)
			ctx := WithJSONSchema(context.Background(), "test", map[string]any{"type": "object"})
			if _, err := s.Explain(ctx, "s", "u"); err != nil {
				t.Fatalf("JSON isteyen çağrı %s üzerinde kırıldı: %v", prov, err)
			}
			if len(rt.bodies) != 1 {
				t.Fatalf("istek sayısı %d, want 1", len(rt.bodies))
			}
			if _, has := rt.bodies[0]["response_format"]; has {
				t.Errorf("%s gövdesine response_format girdi — bu API'de yok", prov)
			}
		})
	}
}
