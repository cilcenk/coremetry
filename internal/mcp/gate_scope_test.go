package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// v0.9.1136 (AI Faz 3.1, K7) — kapı KAPSAMI + rol kodu.
//
// Olay: v0.9.14'te kapı yalnız tools/call'a takılıydı. resources/read
// ve prompts/get AYNI chstore okumalarını yapıyor (resource Reader'ları
// GetServicesFiltered/ListProblems/GetTrace, prompt Renderer'ları
// in-app ✨ Explain gövdesi) ama kapıdan HİÇ geçmiyordu: bir tool
// rol/rate ile reddedilirken eşdeğer URI ve prompt yan kapıları
// serbestti. Ayrıca kapı imzası (ctx, tool string) rol bilgisi
// TAŞIMIYORDU — mcp.go:27-33 yorumu "roller REST'te olduğu gibi MCP'de
// de geçerli" diyordu, yanlıştı.
//
// Bu testler üç şeyi çiviler: (1) üç yürütme yolunun hepsi kapıdan
// geçer, (2) çözülmüş MinRole kapıya iner, (3) rol reddi (Denied)
// -32001, rate reddi -32000 — model "bekle" ile "asla"yı ayırt edebilir.

// gateRecorder — kapıya inen çağrıları kaydeder; opsiyonel red döner.
type gateRecorder struct {
	calls []GateCall
	deny  error
}

func (g *gateRecorder) gate(_ context.Context, c GateCall) error {
	g.calls = append(g.calls, c)
	return g.deny
}

// authzServer — tool + resource + template + prompt kayıtlı, her biri
// kendi MinRole'üyle; Reader/Renderer/Handler koştuğunu SAYAR (kapı
// gerçekten gövdeyi engelliyor mu?).
type authzCounters struct{ tool, resource, template, prompt int }

func authzServer(t *testing.T) (*Server, *httptest.Server, *authzCounters) {
	t.Helper()
	n := &authzCounters{}
	srv := New("coremetry-test", "v0.0.0-test")
	srv.RegisterTool(Tool{
		Name:        "viewer_tool",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(context.Context, json.RawMessage) (any, error) {
			n.tool++
			return map[string]any{"ok": true}, nil
		},
	})
	srv.RegisterTool(Tool{
		Name:        "editor_tool",
		MinRole:     "editor",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(context.Context, json.RawMessage) (any, error) {
			n.tool++
			return map[string]any{"ok": true}, nil
		},
	})
	srv.RegisterResource(Resource{
		URI:     "coremetry://secret",
		MinRole: "editor",
		Reader: func(context.Context, string) (string, error) {
			n.resource++
			return `{"secret":true}`, nil
		},
	})
	srv.RegisterResourceTemplate(ResourceTemplate{
		URITemplate: "coremetry://secret/{id}",
		MinRole:     "admin",
		Reader: func(context.Context, string) (string, error) {
			n.template++
			return `{"secret":true}`, nil
		},
	})
	srv.RegisterPrompt(Prompt{
		Name:      "secret_prompt",
		MinRole:   "editor",
		Arguments: []PromptArgument{{Name: "id", Required: true}},
		Renderer: func(context.Context, map[string]string) ([]PromptMessage, error) {
			n.prompt++
			return []PromptMessage{{Role: "user", Content: PromptContent{Type: "text", Text: "hi"}}}, nil
		},
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/mcp", srv.HandleStreamable)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return srv, ts, n
}

func errCode(t *testing.T, out map[string]any) float64 {
	t.Helper()
	e, _ := out["error"].(map[string]any)
	if e == nil {
		t.Fatalf("hata bekleniyordu, got %v", out)
	}
	code, _ := e["code"].(float64)
	return code
}

func errMsg(t *testing.T, out map[string]any) string {
	t.Helper()
	e, _ := out["error"].(map[string]any)
	if e == nil {
		t.Fatalf("hata bekleniyordu, got %v", out)
	}
	msg, _ := e["message"].(string)
	return msg
}

// Üç yürütme yolunun HEPSİ kapıdan geçer ve çözülmüş MinRole'ü taşır.
func TestGateCoversToolResourceAndPrompt(t *testing.T) {
	srv, ts, _ := authzServer(t)
	g := &gateRecorder{}
	srv.SetCallGate(g.gate)

	postStreamable(t, ts, rpc("tools/call", 1, `{"name":"editor_tool","arguments":{}}`))
	postStreamable(t, ts, rpc("resources/read", 2, `{"uri":"coremetry://secret"}`))
	postStreamable(t, ts, rpc("resources/read", 3, `{"uri":"coremetry://secret/abc"}`))
	postStreamable(t, ts, rpc("prompts/get", 4, `{"name":"secret_prompt","arguments":{"id":"x"}}`))

	want := []GateCall{
		{Kind: "tool", Name: "editor_tool", MinRole: "editor"},
		{Kind: "resource", Name: "coremetry://secret", MinRole: "editor"},
		{Kind: "resource", Name: "coremetry://secret/abc", MinRole: "admin"},
		{Kind: "prompt", Name: "secret_prompt", MinRole: "editor"},
	}
	if len(g.calls) != len(want) {
		t.Fatalf("kapı %d kez çağrıldı, %d beklendi (yeni yol kapıya takılmamış?): %+v",
			len(g.calls), len(want), g.calls)
	}
	for i, w := range want {
		if g.calls[i] != w {
			t.Errorf("kapı çağrısı %d: got %+v, want %+v", i, g.calls[i], w)
		}
	}
}

// Rol reddi -32001 (Denied), rate reddi -32000; gövde HİÇ koşmaz.
func TestGateDenialCodesAndBodySuppression(t *testing.T) {
	cases := []struct {
		name     string
		deny     error
		wantCode float64
	}{
		{"rol reddi", Denied("tool %q requires the %q role", "editor_tool", "editor"), ErrForbidden},
		{"rate reddi", errors.New("rate limited: retry in 12s"), ErrRateLimited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, ts, n := authzServer(t)
			srv.SetCallGate(func(context.Context, GateCall) error { return tc.deny })

			_, out := postStreamable(t, ts, rpc("tools/call", 1, `{"name":"editor_tool","arguments":{}}`))
			if got := errCode(t, out); got != tc.wantCode {
				t.Errorf("tools/call kodu %v, want %v", got, tc.wantCode)
			}
			_, out = postStreamable(t, ts, rpc("resources/read", 2, `{"uri":"coremetry://secret"}`))
			if got := errCode(t, out); got != tc.wantCode {
				t.Errorf("resources/read kodu %v, want %v", got, tc.wantCode)
			}
			_, out = postStreamable(t, ts, rpc("resources/read", 3, `{"uri":"coremetry://secret/abc"}`))
			if got := errCode(t, out); got != tc.wantCode {
				t.Errorf("resources/read (template) kodu %v, want %v", got, tc.wantCode)
			}
			_, out = postStreamable(t, ts, rpc("prompts/get", 4, `{"name":"secret_prompt","arguments":{"id":"x"}}`))
			if got := errCode(t, out); got != tc.wantCode {
				t.Errorf("prompts/get kodu %v, want %v", got, tc.wantCode)
			}
			if *n != (authzCounters{}) {
				t.Fatalf("kapı RED derken gövde koştu: %+v", *n)
			}
		})
	}
}

// Red metni modele okunur gitmeli: gereken rol ADIYLA görünür.
func TestGateDenialMessageReachesClient(t *testing.T) {
	srv, ts, _ := authzServer(t)
	srv.SetCallGate(func(_ context.Context, c GateCall) error {
		return Denied("%s %q requires the %q role or higher; this identity is %q", c.Kind, c.Name, c.MinRole, "viewer")
	})
	_, out := postStreamable(t, ts, rpc("prompts/get", 1, `{"name":"secret_prompt","arguments":{"id":"x"}}`))
	msg := errMsg(t, out)
	for _, want := range []string{"prompt", "secret_prompt", "editor", "viewer"} {
		if !strings.Contains(msg, want) {
			t.Errorf("red metni %q içermiyor: %q", want, msg)
		}
	}
}

// Kapı ÇÖZÜMDEN SONRA: bilinmeyen tool/URI/prompt -32601 kalır ve
// kapıyı (dolayısıyla rate bütçesini) hiç görmez.
func TestGateNotConsultedForUnknownEntries(t *testing.T) {
	srv, ts, _ := authzServer(t)
	g := &gateRecorder{}
	srv.SetCallGate(g.gate)

	for _, req := range [][]byte{
		rpc("tools/call", 1, `{"name":"ghost"}`),
		rpc("resources/read", 2, `{"uri":"coremetry://ghost"}`),
		rpc("prompts/get", 3, `{"name":"ghost"}`),
	} {
		_, out := postStreamable(t, ts, req)
		if got := errCode(t, out); got != ErrMethodNotFound {
			t.Errorf("bilinmeyen girdi kodu %v, want %v: %v", got, float64(ErrMethodNotFound), out)
		}
	}
	if len(g.calls) != 0 {
		t.Fatalf("kapı bilinmeyen girdiler için çağrıldı: %+v", g.calls)
	}
}

// Yetki, ARG doğrulamasından önce: eksik zorunlu arg'la gelen yetkisiz
// istemci "hangi arg eksik" bilgisini almadan reddedilir.
func TestGatePrecedesPromptArgValidation(t *testing.T) {
	srv, ts, n := authzServer(t)
	srv.SetCallGate(func(context.Context, GateCall) error { return Denied("nope") })

	_, out := postStreamable(t, ts, rpc("prompts/get", 1, `{"name":"secret_prompt"}`))
	if got := errCode(t, out); got != ErrForbidden {
		t.Fatalf("kod %v, want %v (arg doğrulaması yetkiden ÖNCE koşmuş)", got, float64(ErrForbidden))
	}
	if n.prompt != 0 {
		t.Fatalf("renderer koştu")
	}
}

// Keşif yolları kapı dışı kalır (ucuz, veri okumaz) — sözleşme pini.
func TestDiscoveryMethodsStayUngated(t *testing.T) {
	srv, ts, _ := authzServer(t)
	g := &gateRecorder{}
	srv.SetCallGate(g.gate)

	for _, m := range []string{"ping", "tools/list", "resources/list", "resources/templates/list", "prompts/list"} {
		if _, out := postStreamable(t, ts, rpc(m, 1, "")); out["error"] != nil {
			t.Fatalf("%s hata döndü: %v", m, out)
		}
	}
	if len(g.calls) != 0 {
		t.Fatalf("keşif yolları kapıya girdi: %+v", g.calls)
	}
}
