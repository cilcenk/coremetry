// github_test.go — FAZ 1.2 (AI Assistant tasarımı §4-Faz1).
//
// Bu yolun iki kırılganlığı var:
//
//   - Entegrasyon header'ları (Editor-Version, Editor-Plugin-Version,
//     Copilot-Integration-Id, User-Agent) kapı bekçisi: eksik olan 403
//     alır. Editör kimliği gibi göründükleri için "gereksiz" diye
//     silinmeye açıklar.
//   - Config.APIKey burada OTURUM jetonudur, operatörün OAuth jetonu
//     (ghu_…) değil. Takas Service'te kaldı (DURUM: önbellek + son
//     kullanma); transport çözülmüş jetonu Bearer'a basar.
package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const githubOK = `{"choices":[{"message":{"content":"the answer"}}],"usage":{"prompt_tokens":11,"completion_tokens":22}}`

func TestDoGitHub_GoldenRequestBody(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name     string
		cfg      Config
		req      Request
		wantMT   float64
		wantTemp *float64
		wantMdl  string
	}{
		{
			name:     "varsayılan tuning (4096 / 0.2)",
			cfg:      Config{APIKey: "tid=sess-tok", Model: "gpt-4o"},
			req:      Request{Model: "gpt-4o", MaxTokens: 4096, Temperature: f(0.2), System: "sys", User: "usr"},
			wantMT:   4096,
			wantTemp: f(0.2),
			wantMdl:  "gpt-4o",
		},
		{
			name:     "operatör ezmesi (8192 / 0.9)",
			cfg:      Config{APIKey: "tid=sess-tok", Model: "gpt-4o"},
			req:      Request{Model: "gpt-4o", MaxTokens: 8192, Temperature: f(0.9), System: "sys", User: "usr"},
			wantMT:   8192,
			wantTemp: f(0.9),
			wantMdl:  "gpt-4o",
		},
		{
			name:     "temperature nil ⇒ alan hiç gönderilmez",
			cfg:      Config{APIKey: "tid=sess-tok", Model: "gpt-4o"},
			req:      Request{MaxTokens: 4096, System: "sys", User: "usr"},
			wantMT:   4096,
			wantTemp: nil,
			wantMdl:  "gpt-4o",
		},
		{
			name:     "model + bütçe boş ⇒ sağlayıcı varsayılanları",
			cfg:      Config{APIKey: "tid=sess-tok"},
			req:      Request{System: "sys", User: "usr"},
			wantMT:   4096,
			wantTemp: nil,
			wantMdl:  "gpt-4o",
		},
		{
			name:     "baseURL yok sayılır — Copilot edge'i sabit",
			cfg:      Config{APIKey: "tid=sess-tok", Model: "gpt-4o", BaseURL: "http://eski-uc.invalid/v1"},
			req:      Request{MaxTokens: 4096, Temperature: f(0.2), System: "sys", User: "usr"},
			wantMT:   4096,
			wantTemp: f(0.2),
			wantMdl:  "gpt-4o",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := &captureRT{body: githubOK}
			cfg := tc.cfg
			cfg.HTTPClient = newCaptureClient(rt)

			resp, err := DoGitHub(context.Background(), cfg, tc.req)
			if err != nil {
				t.Fatalf("DoGitHub: %v", err)
			}
			if resp.Text != "the answer" || resp.InputTokens != 11 || resp.OutputTokens != 22 {
				t.Errorf("Response = %+v, want {the answer 11 22}", resp)
			}
			if len(rt.bodies) != 1 {
				t.Fatalf("captured %d bodies, want 1", len(rt.bodies))
			}
			got, req := rt.bodies[0], rt.reqs[0]

			if got := req.URL.String(); got != "https://api.githubcopilot.com/chat/completions" {
				t.Errorf("URL = %s, want the fixed Copilot edge", got)
			}
			if req.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", req.Method)
			}
			// Kapı bekçisi header'lar — eksik olan 403 alır.
			for h, want := range map[string]string{
				"Content-Type":           "application/json",
				"Authorization":          "Bearer " + tc.cfg.APIKey,
				"Editor-Version":         "vscode/1.85.0",
				"Editor-Plugin-Version":  "copilot-chat/0.12.0",
				"Copilot-Integration-Id": "vscode-chat",
				"User-Agent":             "GithubCopilot/1.155.0",
			} {
				if got := req.Header.Get(h); got != want {
					t.Errorf("%s = %q, want %q", h, got, want)
				}
			}
			// api-key ikizi openai-compat'a özgü; buraya sızmamalı.
			if got := req.Header.Get("api-key"); got != "" {
				t.Errorf("api-key = %q — Copilot edge'inde yeri yok", got)
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
			msgs, ok := got["messages"].([]any)
			if !ok || len(msgs) != 2 {
				t.Fatalf("messages = %v, want 2 öğe", got["messages"])
			}
			for i, w := range []struct{ role, content string }{
				{"system", tc.req.System}, {"user", tc.req.User},
			} {
				m, _ := msgs[i].(map[string]any)
				if m["role"] != w.role || m["content"] != w.content {
					t.Errorf("messages[%d] = %v, want {%s %s}", i, m, w.role, w.content)
				}
			}
			if _, has := got["stream"]; has {
				t.Errorf("buffered yolda stream alanı olmamalı: %v", got["stream"])
			}
		})
	}
}

// TestParseGitHubChat — Copilot yanıtı KASITLI olarak openai-compat'ın
// kurtarma zincirinden geçmez: edge reasoning_content/reasoning ya da
// <think> üretmiyor, ve zinciri buraya bağlamak BAŞKA bir boş-yanıt
// hata metnini bu yola sokardı. Taşıma davranış değiştirmez.
func TestParseGitHubChat(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantText string
		wantIn   int
		wantOut  int
		wantErr  string
	}{
		{
			name:     "normal content",
			body:     githubOK,
			wantText: "the answer",
			wantIn:   11, wantOut: 22,
		},
		{
			// Salvage YOK: <think> soyulmaz. Bugünkü davranış pinli —
			// değiştirmek ayrı bir ürün kararı.
			name:     "<think> bloğu OLDUĞU GİBİ geçer (salvage uygulanmaz)",
			body:     `{"choices":[{"message":{"content":"<think>düşünce</think>cevap"}}]}`,
			wantText: "<think>düşünce</think>cevap",
		},
		{
			name:    "choices yok",
			body:    `{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":0}}`,
			wantIn:  3,
			wantErr: "github copilot: empty response",
		},
		{
			name:    "JSON değil",
			body:    `not json at all`,
			wantErr: "decode github copilot response: invalid character 'o' in literal null (expecting 'u')",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := ParseGitHubChat([]byte(tc.body))
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

func TestDoGitHub_ErrorSemantics(t *testing.T) {
	base := Config{APIKey: "tid=sess-tok", Model: "gpt-4o"}
	req := Request{MaxTokens: 4096, System: "s", User: "u"}

	t.Run("≥300 gövdeyi taşır (429 kelimesi kota kesicisinin girdisi)", func(t *testing.T) {
		rt := &captureRT{status: 429, body: `{"error":"quota exceeded"}`}
		cfg := base
		cfg.HTTPClient = newCaptureClient(rt)
		_, err := DoGitHub(context.Background(), cfg, req)
		want := `github copilot 429: {"error":"quota exceeded"}`
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
		_, err := DoGitHub(context.Background(), cfg, req)
		if err == nil || !strings.Contains(err.Error(), "github copilot call:") {
			t.Fatalf("got %v, want 'github copilot call: …' sarmalı", err)
		}
	})

	t.Run("nil HTTPClient sessizce varsayılana düşmez", func(t *testing.T) {
		if _, err := DoGitHub(context.Background(), base, req); err == nil ||
			!strings.Contains(err.Error(), "nil HTTPClient") {
			t.Fatalf("got %v, want nil-client hatası", err)
		}
	})

	t.Run("JSONLevel>0 açıkça reddedilir, istek gitmez", func(t *testing.T) {
		rt := &captureRT{}
		cfg := base
		cfg.HTTPClient = newCaptureClient(rt)
		jr := req
		jr.JSONLevel = JSONSchema
		if _, err := DoGitHub(context.Background(), cfg, jr); err == nil {
			t.Fatal("hata bekleniyordu")
		}
		if len(rt.reqs) != 0 {
			t.Fatalf("desteklenmeyen basamakta yine de istek gitti (%d)", len(rt.reqs))
		}
	})
}
