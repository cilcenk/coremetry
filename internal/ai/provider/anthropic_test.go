// anthropic_test.go — FAZ 1.2 (AI Assistant tasarımı §4-Faz1).
//
// Devralmanın kabul ölçütü TEL ÜSTÜNDEKİ ŞEKİL. Bu yolda iki şey
// kırılgan:
//
//   - Anthropic-Version header'ı ZORUNLU. Düşerse API 400 döner ve
//     anthropic kurulumlarında AI tamamen ölür — kaynak okuyarak fark
//     edilmesi zor, gövde testiyle imkânsız değil.
//   - temperature'ın VARLIĞI v0.9.1120'den beri yeni. ~1000 sürüm
//     boyunca bu gövdede hiç temperature yoktu ve max_tokens sabit
//     1024'tü; alan sessizce düşerse aynı soru sağlayıcıya göre farklı
//     yanıtlanmaya geri döner.
package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const anthropicOK = `{"content":[{"type":"text","text":"the answer"}],"usage":{"input_tokens":11,"output_tokens":22}}`

func TestDoAnthropic_GoldenRequestBody(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name     string
		cfg      Config
		req      Request
		wantMT   float64
		wantTemp *float64 // nil = gövdede OLMAMALI
		wantMdl  string
	}{
		{
			name:     "varsayılan tuning (4096 / 0.2) — copilot.Service'in gönderdiği hâl",
			cfg:      Config{APIKey: "sk-ant", Model: "claude-x"},
			req:      Request{Model: "claude-x", MaxTokens: 4096, Temperature: f(0.2), System: "sys", User: "usr"},
			wantMT:   4096,
			wantTemp: f(0.2),
			wantMdl:  "claude-x",
		},
		{
			name:     "operatör ezmesi (8192 / 0.9)",
			cfg:      Config{APIKey: "sk-ant", Model: "claude-x"},
			req:      Request{Model: "claude-x", MaxTokens: 8192, Temperature: f(0.9), System: "sys", User: "usr"},
			wantMT:   8192,
			wantTemp: f(0.9),
			wantMdl:  "claude-x",
		},
		{
			name:     "deterministik: temperature=0 gövdeye 0 olarak biner",
			cfg:      Config{APIKey: "sk-ant", Model: "claude-x"},
			req:      Request{Model: "claude-x", MaxTokens: 4096, Temperature: f(0), System: "sys", User: "usr"},
			wantMT:   4096,
			wantTemp: f(0),
			wantMdl:  "claude-x",
		},
		{
			name:     "temperature nil ⇒ alan hiç gönderilmez",
			cfg:      Config{APIKey: "sk-ant", Model: "claude-x"},
			req:      Request{Model: "claude-x", MaxTokens: 4096, System: "sys", User: "usr"},
			wantMT:   4096,
			wantTemp: nil,
			wantMdl:  "claude-x",
		},
		{
			name:     "model boş ⇒ sağlayıcı varsayılanı, bütçe boş ⇒ 4096",
			cfg:      Config{APIKey: "sk-ant"},
			req:      Request{System: "sys", User: "usr"},
			wantMT:   4096,
			wantTemp: nil,
			wantMdl:  "claude-sonnet-4-6",
		},
		{
			// baseURL bu yolda OKUNMAZ: Anthropic'in tek host'u var ve
			// operatörün baseURL alanı openai-compat uçları için. Bayat
			// bir baseURL'e anahtar göndermek olurdu.
			name:     "baseURL yok sayılır — uç sabit",
			cfg:      Config{APIKey: "sk-ant", Model: "claude-x", BaseURL: "http://eski-uc.invalid/v1"},
			req:      Request{MaxTokens: 4096, Temperature: f(0.2), System: "sys", User: "usr"},
			wantMT:   4096,
			wantTemp: f(0.2),
			wantMdl:  "claude-x",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := &captureRT{body: anthropicOK}
			cfg := tc.cfg
			cfg.HTTPClient = newCaptureClient(rt)

			resp, err := DoAnthropic(context.Background(), cfg, tc.req)
			if err != nil {
				t.Fatalf("DoAnthropic: %v", err)
			}
			if resp.Text != "the answer" || resp.InputTokens != 11 || resp.OutputTokens != 22 {
				t.Errorf("Response = %+v, want {the answer 11 22}", resp)
			}
			if len(rt.bodies) != 1 {
				t.Fatalf("captured %d bodies, want 1", len(rt.bodies))
			}
			got, req := rt.bodies[0], rt.reqs[0]

			if got := req.URL.String(); got != "https://api.anthropic.com/v1/messages" {
				t.Errorf("URL = %s, want the fixed Messages endpoint", got)
			}
			if req.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", req.Method)
			}
			// Zorunlu header üçlüsü.
			for h, want := range map[string]string{
				"Content-Type":      "application/json",
				"X-Api-Key":         tc.cfg.APIKey,
				"Anthropic-Version": "2023-06-01",
			} {
				if got := req.Header.Get(h); got != want {
					t.Errorf("%s = %q, want %q", h, got, want)
				}
			}
			// openai-compat header'ları buraya sızmamalı.
			if got := req.Header.Get("Authorization"); got != "" {
				t.Errorf("Authorization = %q — anthropic X-API-Key kullanır", got)
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
				t.Errorf("temperature eksik; want %v (v0.9.1120)", *tc.wantTemp)
			case tc.wantTemp != nil && tv.(float64) != *tc.wantTemp:
				t.Errorf("temperature = %v, want %v", tv, *tc.wantTemp)
			}
			// Anthropic'te system AYRI bir alan, messages'ın içinde değil.
			if got["system"] != tc.req.System {
				t.Errorf("system = %v, want %q", got["system"], tc.req.System)
			}
			msgs, ok := got["messages"].([]any)
			if !ok || len(msgs) != 1 {
				t.Fatalf("messages = %v, want tek user öğesi", got["messages"])
			}
			m, _ := msgs[0].(map[string]any)
			if m["role"] != "user" || m["content"] != tc.req.User {
				t.Errorf("messages[0] = %v, want {user %q}", m, tc.req.User)
			}
			if _, has := got["stream"]; has {
				t.Errorf("buffered yolda stream alanı olmamalı: %v", got["stream"])
			}
			if _, has := got["response_format"]; has {
				t.Errorf("anthropic gövdesinde response_format olmamalı: %v", got["response_format"])
			}
		})
	}
}

// TestParseAnthropic — content[] birden çok text bloğu taşıyabilir ve
// text OLMAYAN bloklar atlanır. Boş metin HATA DEĞİL: openai-compat'ın
// kurtarma zincirinin anthropic karşılığı yok ve olmadı (taşınan
// davranış).
func TestParseAnthropic(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantText string
		wantIn   int
		wantOut  int
		wantErr  string
	}{
		{
			name:     "tek text bloğu",
			body:     anthropicOK,
			wantText: "the answer",
			wantIn:   11, wantOut: 22,
		},
		{
			name:     "çok bloklu yanıt birleştirilir",
			body:     `{"content":[{"type":"text","text":"birinci "},{"type":"text","text":"ikinci"}],"usage":{"input_tokens":1,"output_tokens":2}}`,
			wantText: "birinci ikinci",
			wantIn:   1, wantOut: 2,
		},
		{
			name:     "text olmayan bloklar atlanır",
			body:     `{"content":[{"type":"thinking","text":"düşünce"},{"type":"text","text":"cevap"}]}`,
			wantText: "cevap",
		},
		{
			name:     "boş content ⇒ boş metin, hata YOK (taşınan davranış)",
			body:     `{"content":[],"usage":{"input_tokens":3,"output_tokens":0}}`,
			wantText: "",
			wantIn:   3,
		},
		{
			name:    "JSON değil",
			body:    `not json at all`,
			wantErr: "decode anthropic response: invalid character 'o' in literal null (expecting 'u')",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := ParseAnthropic([]byte(tc.body))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("beklenmedik hata: %v", err)
				}
			} else if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("hata:\n got %v\nwant %q", err, tc.wantErr)
			}
			if resp.Text != tc.wantText {
				t.Errorf("Text = %q, want %q", resp.Text, tc.wantText)
			}
			if resp.InputTokens != tc.wantIn || resp.OutputTokens != tc.wantOut {
				t.Errorf("usage = (%d,%d), want (%d,%d)", resp.InputTokens, resp.OutputTokens, tc.wantIn, tc.wantOut)
			}
		})
	}
}

func TestDoAnthropic_ErrorSemantics(t *testing.T) {
	base := Config{APIKey: "sk-ant", Model: "claude-x"}
	req := Request{MaxTokens: 4096, System: "s", User: "u"}

	t.Run("≥300 gövdeyi taşır (429 kelimesi kota kesicisinin girdisi)", func(t *testing.T) {
		rt := &captureRT{status: 429, body: `{"type":"error","error":{"type":"rate_limit_error"}}`}
		cfg := base
		cfg.HTTPClient = newCaptureClient(rt)
		_, err := DoAnthropic(context.Background(), cfg, req)
		want := `anthropic 429: {"type":"error","error":{"type":"rate_limit_error"}}`
		if err == nil || err.Error() != want {
			t.Fatalf("got %v, want %q", err, want)
		}
		var he *HTTPError
		if !errors.As(err, &he) || he.Status != 429 {
			t.Fatalf("HTTPError tipli olmalı: %T", err)
		}
	})

	t.Run("taşıma hatası sarmalanır", func(t *testing.T) {
		rt := &captureRT{err: errors.New("dial tcp: connection refused")}
		cfg := base
		cfg.HTTPClient = newCaptureClient(rt)
		_, err := DoAnthropic(context.Background(), cfg, req)
		if err == nil || !strings.Contains(err.Error(), "anthropic call:") {
			t.Fatalf("got %v, want 'anthropic call: …' sarmalı", err)
		}
	})

	t.Run("nil HTTPClient sessizce varsayılana düşmez", func(t *testing.T) {
		if _, err := DoAnthropic(context.Background(), base, req); err == nil ||
			!strings.Contains(err.Error(), "nil HTTPClient") {
			t.Fatalf("got %v, want nil-client hatası", err)
		}
	})

	// JSON basamağı bu API'de yok. Sessizce yok saymak yerine AÇIK hata:
	// çağıran (Service) basamağı düşürmekle yükümlü ve bugün öyle yapıyor.
	t.Run("JSONLevel>0 açıkça reddedilir, istek gitmez", func(t *testing.T) {
		rt := &captureRT{}
		cfg := base
		cfg.HTTPClient = newCaptureClient(rt)
		jr := req
		jr.JSONLevel = JSONObject
		if _, err := DoAnthropic(context.Background(), cfg, jr); err == nil {
			t.Fatal("hata bekleniyordu")
		}
		if len(rt.reqs) != 0 {
			t.Fatalf("desteklenmeyen basamakta yine de istek gitti (%d)", len(rt.reqs))
		}
	})
}
