// openai_test.go — FAZ 1.1 (AI Assistant tasarımı §4-Faz1).
//
// Bu paket 8 istek üreticisinin ilkini devralıyor. Devralmanın tek
// kabul ölçütü TEL ÜSTÜNDEKİ ŞEKİL: gövde, header ve kurtarma zinciri
// copilot.go'daki ikiziyle birebir aynı olmalı. Kaynak-grep'i değil,
// gerçek istek gövdesi pinleniyor — v0.9.1120'nin dersi: aynı sabitin
// başka bir yazılışla geri gelmesini yalnız gövde testi yakalar.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// captureRT — giden isteği kaydeder, sabit bir 200 döndürür.
// internal/copilot/tuning_test.go'daki teknikle aynı.
type captureRT struct {
	reqs   []*http.Request
	bodies []map[string]any
	status int
	body   string
	err    error
}

func (c *captureRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if c.err != nil {
		return nil, c.err
	}
	c.reqs = append(c.reqs, req)
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil && m != nil {
			c.bodies = append(c.bodies, m)
		}
	}
	st, body := c.status, c.body
	if st == 0 {
		st = 200
	}
	if body == "" {
		body = `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":22}}`
	}
	return &http.Response{
		StatusCode: st,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		Request:    req,
	}, nil
}

func newCaptureClient(rt *captureRT) *http.Client { return &http.Client{Transport: rt} }

// ─── golden request body ────────────────────────────────────────────

// TestDoOpenAI_GoldenRequestBody pins the wire shape the openai-compat
// path has shipped since v0.8.x: /chat/completions under the operator's
// baseURL, model + max_tokens + [system,user] messages, temperature only
// when the caller supplies one, NO response_format on the prose path,
// and the Bearer + api-key twin headers (v0.8.384).
func TestDoOpenAI_GoldenRequestBody(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name     string
		cfg      Config
		req      Request
		wantURL  string
		wantMT   float64
		wantTemp *float64 // nil = gövdede OLMAMALI
		wantAuth bool
		wantMdl  string
	}{
		{
			name:     "varsayılan tuning (4096 / 0.2) — copilot.Service'in gönderdiği hâl",
			cfg:      Config{BaseURL: "http://llm.invalid/v1", APIKey: "k", Model: "model-x"},
			req:      Request{Model: "model-x", MaxTokens: 4096, Temperature: f(0.2), System: "sys", User: "usr"},
			wantURL:  "http://llm.invalid/v1/chat/completions",
			wantMT:   4096,
			wantTemp: f(0.2),
			wantAuth: true,
			wantMdl:  "model-x",
		},
		{
			name:     "operatör ezmesi (8192 / 0.9)",
			cfg:      Config{BaseURL: "http://llm.invalid/v1", APIKey: "k", Model: "model-x"},
			req:      Request{Model: "model-x", MaxTokens: 8192, Temperature: f(0.9), System: "sys", User: "usr"},
			wantURL:  "http://llm.invalid/v1/chat/completions",
			wantMT:   8192,
			wantTemp: f(0.9),
			wantAuth: true,
			wantMdl:  "model-x",
		},
		{
			// temperature 0 bir DEĞER, yokluk değil — *float64 tam
			// bunun için (v0.9.1120).
			name:     "deterministik: temperature=0 işaretçisi gövdeye 0 olarak biner",
			cfg:      Config{BaseURL: "http://llm.invalid/v1", APIKey: "k", Model: "model-x"},
			req:      Request{Model: "model-x", MaxTokens: 4096, Temperature: f(0), System: "sys", User: "usr"},
			wantURL:  "http://llm.invalid/v1/chat/completions",
			wantMT:   4096,
			wantTemp: f(0),
			wantAuth: true,
			wantMdl:  "model-x",
		},
		{
			name:     "temperature nil ⇒ alan hiç gönderilmez",
			cfg:      Config{BaseURL: "http://llm.invalid/v1", APIKey: "k", Model: "model-x"},
			req:      Request{Model: "model-x", MaxTokens: 4096, System: "sys", User: "usr"},
			wantURL:  "http://llm.invalid/v1/chat/completions",
			wantMT:   4096,
			wantTemp: nil,
			wantAuth: true,
			wantMdl:  "model-x",
		},
		{
			// Ollama varsayılanı: anahtarsız yerel uç. Header hiç
			// gönderilmez ki auth arayan geçit temiz 401 dönebilsin.
			name:     "anahtarsız yerel uç: Authorization/api-key YOK",
			cfg:      Config{BaseURL: "http://ollama:11434/v1", Model: "llama3.1"},
			req:      Request{Model: "llama3.1", MaxTokens: 4096, Temperature: f(0.2), System: "sys", User: "usr"},
			wantURL:  "http://ollama:11434/v1/chat/completions",
			wantMT:   4096,
			wantTemp: f(0.2),
			wantAuth: false,
			wantMdl:  "llama3.1",
		},
		{
			name:     "sondaki eğik çizgi tekrarlanmaz",
			cfg:      Config{BaseURL: "http://llm.invalid/v1/", APIKey: "k", Model: "m"},
			req:      Request{MaxTokens: 4096, Temperature: f(0.2), System: "s", User: "u"},
			wantURL:  "http://llm.invalid/v1/chat/completions",
			wantMT:   4096,
			wantTemp: f(0.2),
			wantAuth: true,
			wantMdl:  "m", // Request.Model boş ⇒ Config.Model
		},
		{
			name:     "baseURL + model boş ⇒ sağlayıcı varsayılanları",
			cfg:      Config{APIKey: "k"},
			req:      Request{System: "s", User: "u"},
			wantURL:  "https://api.openai.com/v1/chat/completions",
			wantMT:   4096, // MaxTokens<=0 ⇒ paket varsayılanı
			wantTemp: nil,
			wantAuth: true,
			wantMdl:  "gpt-4o-mini",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := &captureRT{}
			cfg := tc.cfg
			cfg.HTTPClient = newCaptureClient(rt)

			resp, err := DoOpenAI(context.Background(), cfg, tc.req)
			if err != nil {
				t.Fatalf("DoOpenAI: %v", err)
			}
			if resp.Text != "ok" || resp.InputTokens != 11 || resp.OutputTokens != 22 {
				t.Errorf("Response = %+v, want {ok 11 22}", resp)
			}
			if len(rt.bodies) != 1 {
				t.Fatalf("captured %d bodies, want 1", len(rt.bodies))
			}
			got, req := rt.bodies[0], rt.reqs[0]

			if req.URL.String() != tc.wantURL {
				t.Errorf("URL = %s, want %s", req.URL.String(), tc.wantURL)
			}
			if req.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", req.Method)
			}
			if ct := req.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			// api-key ikizi: Bearer'ı anlamayan self-hosted geçitler
			// bununla doğruluyor (v0.8.384). İkisi de ya var ya yok.
			gotAuth := req.Header.Get("Authorization")
			gotAPIKey := req.Header.Get("api-key")
			if tc.wantAuth {
				if gotAuth != "Bearer "+tc.cfg.APIKey {
					t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer "+tc.cfg.APIKey)
				}
				if gotAPIKey != tc.cfg.APIKey {
					t.Errorf("api-key = %q, want %q (vLLM/KServe ikizi)", gotAPIKey, tc.cfg.APIKey)
				}
			} else if gotAuth != "" || gotAPIKey != "" {
				t.Errorf("anahtarsız uçta header sızdı: Authorization=%q api-key=%q", gotAuth, gotAPIKey)
			}

			if m, _ := got["model"].(string); m != tc.wantMdl {
				t.Errorf("model = %v, want %s", got["model"], tc.wantMdl)
			}
			mt, ok := got["max_tokens"].(float64)
			if !ok {
				t.Fatalf("gövdede sayısal max_tokens yok: %v", got)
			}
			if mt != tc.wantMT {
				t.Errorf("max_tokens = %v, want %v", mt, tc.wantMT)
			}
			tv, has := got["temperature"]
			switch {
			case tc.wantTemp == nil && has:
				t.Errorf("temperature gövdede olmamalıydı; got %v", tv)
			case tc.wantTemp != nil && !has:
				t.Errorf("temperature eksik; want %v", *tc.wantTemp)
			case tc.wantTemp != nil && tv.(float64) != *tc.wantTemp:
				t.Errorf("temperature = %v, want %v", tv, *tc.wantTemp)
			}
			if _, has := got["response_format"]; has {
				t.Errorf("prose yolunda response_format olmamalı: %v", got["response_format"])
			}
			if _, has := got["stream"]; has {
				t.Errorf("buffered yolda stream alanı olmamalı: %v", got["stream"])
			}
			msgs, ok := got["messages"].([]any)
			if !ok || len(msgs) != 2 {
				t.Fatalf("messages = %v, want 2 öğe", got["messages"])
			}
			want := []struct{ role, content string }{
				{"system", tc.req.System},
				{"user", tc.req.User},
			}
			for i, w := range want {
				m, _ := msgs[i].(map[string]any)
				if m["role"] != w.role || m["content"] != w.content {
					t.Errorf("messages[%d] = %v, want {%s %s}", i, m, w.role, w.content)
				}
			}
		})
	}
}

// ─── kurtarma zinciri ───────────────────────────────────────────────

// TestParseOpenAIChat_SalvageChain ports the v0.8.138→155→384 cases:
// a local reasoning model can put the answer in content, in
// reasoning_content, in reasoning, or ONLY inside <think>…</think>;
// and when the budget runs out mid-thought the operator needs the
// actionable "raise max_tokens" diagnosis, not a blank panel.
func TestParseOpenAIChat_SalvageChain(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantText string
		wantIn   int
		wantOut  int
		wantErr  string // boş = hata yok; doluysa TAM eşleşme
	}{
		{
			name:     "normal content",
			body:     `{"choices":[{"message":{"content":"the answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":7}}`,
			wantText: "the answer",
			wantIn:   5, wantOut: 7,
		},
		{
			name:     "<think> bloğu soyulur, sonrası kalır",
			body:     `{"choices":[{"message":{"content":"<think>uzun uzun düşünce</think>\n\nkısa cevap"},"finish_reason":"stop"}]}`,
			wantText: "kısa cevap",
		},
		{
			name:     "content boş + reasoning_content (deepseek-r1/Qwen3)",
			body:     `{"choices":[{"message":{"content":"","reasoning_content":"reasoning'deki cevap"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
			wantText: "reasoning'deki cevap",
			wantIn:   1, wantOut: 2,
		},
		{
			name:     "content boş + reasoning (vLLM ≥0.24 yazılışı)",
			body:     `{"choices":[{"message":{"content":"","reasoning":"öteki alandaki cevap"},"finish_reason":"stop"}]}`,
			wantText: "öteki alandaki cevap",
		},
		{
			name:     "yalnız think bloğu — içi kurtarılır",
			body:     `{"choices":[{"message":{"content":"<think>düşünce cevabın ta kendisi</think>"},"finish_reason":"stop"}]}`,
			wantText: "düşünce cevabın ta kendisi",
		},
		{
			// KAPANMAMIŞ blok: </think> yok ⇒ StripThinking hiçbir şey
			// soymaz ve metin OLDUĞU GİBİ geçer — açılış etiketi dahil.
			// Bu, copilot.go'daki ikizinin bugünkü davranışı; parity
			// dilimi davranış DEĞİŞTİRMEZ, taşır. (Etiketin UI'ya
			// sızması ayrı bir ürün kararı — buraya iliştirilmedi.)
			name:     "kapanmamış think bloğu olduğu gibi geçer (eski davranış pinli)",
			body:     `{"choices":[{"message":{"content":"<think>yarım kalmış düşünce"},"finish_reason":"length"}]}`,
			wantText: "<think>yarım kalmış düşünce",
		},
		{
			name:    "finish_reason=length + hiç içerik ⇒ bütçe teşhisi",
			body:    `{"choices":[{"message":{"content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":9,"completion_tokens":4096}}`,
			wantIn:  9,
			wantOut: 4096,
			wantErr: "model returned no answer — token budget exhausted by reasoning; raise max_tokens or disable thinking (e.g. Qwen3 /no_think)",
		},
		{
			name:    "tamamen boş ⇒ genel teşhis",
			body:    `{"choices":[{"message":{"content":"   "},"finish_reason":"stop"}]}`,
			wantErr: "openai-compat: model returned empty content — no answer in content/reasoning. Check the model name + endpoint; a reasoning model may need /no_think. See the [copilot] pod log for the raw response",
		},
		{
			name:    "choices yok",
			body:    `{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":0}}`,
			wantIn:  3,
			wantErr: "openai-compat: empty response",
		},
		{
			name:    "JSON değil",
			body:    `not json at all`,
			wantErr: "decode openai-compat response: invalid character 'o' in literal null (expecting 'u')",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := ParseOpenAIChat([]byte(tc.body))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("beklenmedik hata: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("hata bekleniyordu (%q), yanıt: %+v", tc.wantErr, resp)
				}
				if err.Error() != tc.wantErr {
					t.Fatalf("hata metni:\n got %q\nwant %q", err.Error(), tc.wantErr)
				}
			}
			// v0.10.37 — kurtarılan DÜŞÜNCE artık işaretli çıkıyor
			// (nihai cevaptan ayırt edilebilsin). Beklenen gövde AYNI;
			// karşılaştırma işareti soyarak yapılıyor ki testin niyeti
			// (kurtarma çalışıyor mu) korunsun.
			gotText := strings.TrimPrefix(resp.Text, SalvagedThinkingPrefix)
			if gotText != tc.wantText {
				t.Errorf("Text = %q, want %q", gotText, tc.wantText)
			}
			// Token sayıları hata hâlinde de taşınmalı — /ai satırı
			// başarısız çağrının maliyetini de göstermeli.
			if resp.InputTokens != tc.wantIn || resp.OutputTokens != tc.wantOut {
				t.Errorf("usage = (%d,%d), want (%d,%d)", resp.InputTokens, resp.OutputTokens, tc.wantIn, tc.wantOut)
			}
		})
	}
}

func TestStripThinkingAndThinkingContent(t *testing.T) {
	tests := []struct {
		name, in, wantStrip, wantInside string
	}{
		{"düz metin", "plain answer", "plain answer", ""},
		{"baştaki blok soyulur", "<think>reasoning</think>the answer", "the answer", "reasoning"},
		{"boşluk kırpılır", "  <think>x</think>   spaced   ", "spaced", "x"},
		{"yalnız düşünce", "<think>all thinking</think>", "", "all thinking"},
		{"boş girdi", "", "", ""},
		{"iç içe/çoklu blok — SON kapanış esas", "<think>a</think>ara<think>b</think>son", "son", "a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripThinking(tc.in); got != tc.wantStrip {
				t.Errorf("StripThinking(%q) = %q, want %q", tc.in, got, tc.wantStrip)
			}
			if got := ThinkingContent(tc.in); got != tc.wantInside {
				t.Errorf("ThinkingContent(%q) = %q, want %q", tc.in, got, tc.wantInside)
			}
		})
	}
}

// ─── hata semantiği ─────────────────────────────────────────────────

// TestDoOpenAI_ErrorSemantics pins the strings copilot.go has always
// produced — the quota circuit-breaker (isQuotaErr) keys off " 429"
// appearing in the message, so a reworded error would silently disarm
// it (v0.9.200).
func TestDoOpenAI_ErrorSemantics(t *testing.T) {
	base := Config{BaseURL: "http://llm.invalid/v1", APIKey: "k", Model: "m"}
	req := Request{MaxTokens: 4096, System: "s", User: "u"}

	t.Run("≥300 gövdeyi taşır (429 kelimesi kota kesicisinin girdisi)", func(t *testing.T) {
		rt := &captureRT{status: 429, body: `{"error":"rate limit exceeded"}`}
		cfg := base
		cfg.HTTPClient = newCaptureClient(rt)
		_, err := DoOpenAI(context.Background(), cfg, req)
		if err == nil {
			t.Fatal("hata bekleniyordu")
		}
		want := `openai-compat 429: {"error":"rate limit exceeded"}`
		if err.Error() != want {
			t.Fatalf("got %q, want %q", err.Error(), want)
		}
	})

	t.Run("taşıma hatası sarmalanır", func(t *testing.T) {
		boom := errors.New("dial tcp: connection refused")
		rt := &captureRT{err: boom}
		cfg := base
		cfg.HTTPClient = newCaptureClient(rt)
		_, err := DoOpenAI(context.Background(), cfg, req)
		if err == nil || !strings.Contains(err.Error(), "openai-compat call:") {
			t.Fatalf("got %v, want 'openai-compat call: …' sarmalı", err)
		}
	})

	t.Run("nil HTTPClient sessizce varsayılana düşmez", func(t *testing.T) {
		_, err := DoOpenAI(context.Background(), base, req)
		if err == nil || !strings.Contains(err.Error(), "nil HTTPClient") {
			t.Fatalf("got %v, want nil-client hatası", err)
		}
	})

	t.Run("≥300 HTTPError tipiyle döner (merdivenin statü girdisi)", func(t *testing.T) {
		rt := &captureRT{status: 400, body: `{"error":"unknown field response_format"}`}
		cfg := base
		cfg.HTTPClient = newCaptureClient(rt)
		_, err := DoOpenAI(context.Background(), cfg, req)
		var he *HTTPError
		if !errors.As(err, &he) {
			t.Fatalf("hata %T, want *HTTPError — Service statüyü metinden ayıklamak zorunda kalır", err)
		}
		if he.Status != 400 || he.Provider != labelOpenAI {
			t.Errorf("HTTPError = %+v, want {openai-compat 400 …}", he)
		}
	})
}

// ─── JSON merdiveni: gövde şekilleri ────────────────────────────────

// TestDoOpenAI_ResponseFormatLadder — merdivenin ÜÇ basamağının tel
// üstündeki şekli (v0.9.517/527). Basamağı Service seçer; burada
// pinlenen, seçilen basamağın gövdeye NASIL bindiği.
func TestDoOpenAI_ResponseFormatLadder(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"ozet": map[string]any{"type": "string"}},
		"required":             []any{"ozet"},
		"additionalProperties": false,
	}
	t.Run("JSONPlain ⇒ alan hiç yok", func(t *testing.T) {
		got := ladderBody(t, Request{JSONLevel: JSONPlain, System: "s", User: "u"})
		if _, has := got["response_format"]; has {
			t.Errorf("prose çağrısına response_format girdi: %v", got["response_format"])
		}
	})
	t.Run("JSONObject ⇒ {\"type\":\"json_object\"}", func(t *testing.T) {
		got := ladderBody(t, Request{JSONLevel: JSONObject, System: "s", User: "u"})
		rf, _ := got["response_format"].(map[string]any)
		if rf == nil || rf["type"] != "json_object" || len(rf) != 1 {
			t.Errorf("response_format = %v, want {type:json_object}", got["response_format"])
		}
	})
	t.Run("JSONSchema ⇒ name+schema+strict", func(t *testing.T) {
		got := ladderBody(t, Request{JSONLevel: JSONSchema, JSONSchemaName: "nl-to-query",
			JSONSchema: schema, System: "s", User: "u"})
		rf, _ := got["response_format"].(map[string]any)
		if rf == nil || rf["type"] != "json_schema" {
			t.Fatalf("response_format = %v", got["response_format"])
		}
		js, _ := rf["json_schema"].(map[string]any)
		if js == nil {
			t.Fatal("json_schema gövdesi yok")
		}
		if js["name"] != "nl-to-query" {
			t.Errorf("name = %v, want nl-to-query", js["name"])
		}
		// strict:true olmadan şema yalnız bir ÖNERİ olur — enum kazancı
		// (v0.9.527'nin asıl gerekçesi) sessizce kaybolurdu.
		if js["strict"] != true {
			t.Errorf("strict = %v, want true", js["strict"])
		}
		if _, ok := js["schema"].(map[string]any); !ok {
			t.Error("schema gövdesi taşınmamış")
		}
	})

	// Fail-closed: geçersiz basamak ya da şemasız json_schema İSTEK
	// GÖNDERMEDEN hata döner. Sessizce kısıtsız çağrıya düşmek, isteyen
	// yüzeyin garantisini haber vermeden kaybettirirdi.
	bad := []struct {
		name string
		req  Request
	}{
		{"şemasız JSONSchema", Request{JSONLevel: JSONSchema, System: "s", User: "u"}},
		{"adsız JSONSchema", Request{JSONLevel: JSONSchema, JSONSchema: schema, System: "s", User: "u"}},
		{"bilinmeyen basamak", Request{JSONLevel: 7, System: "s", User: "u"}},
		{"negatif basamak", Request{JSONLevel: -1, System: "s", User: "u"}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			rt := &captureRT{}
			cfg := Config{BaseURL: "http://llm.invalid/v1", APIKey: "k", Model: "m", HTTPClient: newCaptureClient(rt)}
			if _, err := DoOpenAI(context.Background(), cfg, tc.req); err == nil {
				t.Fatal("hata bekleniyordu")
			}
			if len(rt.reqs) != 0 {
				t.Fatalf("geçersiz basamakta yine de istek gitti (%d) — sessiz kısıt kaybı", len(rt.reqs))
			}
		})
	}
}

func ladderBody(t *testing.T, req Request) map[string]any {
	t.Helper()
	rt := &captureRT{}
	cfg := Config{BaseURL: "http://llm.invalid/v1", APIKey: "k", Model: "m", HTTPClient: newCaptureClient(rt)}
	if _, err := DoOpenAI(context.Background(), cfg, req); err != nil {
		t.Fatalf("DoOpenAI: %v", err)
	}
	if len(rt.bodies) != 1 {
		t.Fatalf("captured %d bodies, want 1", len(rt.bodies))
	}
	return rt.bodies[0]
}
