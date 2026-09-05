package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
)

// v0.10.426 — M1: gözlem kancası kapıdan sonra, handler'dan önce; dönen ctx
// handler'a ulaşır; üç çıkışta da done (başarı: bayt, handler hatası:
// sınıf); bilinmeyen araç ve kapı reddi gözlemlenmez.
type observeRec struct {
	mu    sync.Mutex
	calls []string
	outs  []CallOutcome
}

type obsKey struct{}

func (o *observeRec) hook(ctx context.Context, kind, name string, _ json.RawMessage) (context.Context, func(CallOutcome)) {
	o.mu.Lock()
	o.calls = append(o.calls, kind+":"+name)
	o.mu.Unlock()
	return context.WithValue(ctx, obsKey{}, true), func(c CallOutcome) {
		o.mu.Lock()
		o.outs = append(o.outs, c)
		o.mu.Unlock()
	}
}

func TestObserverWrapsToolExecution(t *testing.T) {
	srv, ts := testServer(t)
	var sawCtx bool
	srv.RegisterTool(Tool{
		Name: "ctx_tool", Description: "t", InputSchema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
			sawCtx = ctx.Value(obsKey{}) == true
			return map[string]any{"ok": true}, nil
		},
	})
	srv.RegisterTool(Tool{
		Name: "boom_tool", Description: "t", InputSchema: map[string]any{"type": "object"},
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("dial tcp: connection refused")
		},
	})
	rec := &observeRec{}
	srv.SetObserver(rec.hook)

	_, out := postStreamable(t, ts, rpc("tools/call", 1, `{"name":"ctx_tool","arguments":{}}`))
	if out["error"] != nil || !sawCtx {
		t.Fatalf("gözlem ctx'i handler'a ulaşmalı: %v sawCtx=%v", out, sawCtx)
	}
	_, out = postStreamable(t, ts, rpc("tools/call", 2, `{"name":"boom_tool","arguments":{}}`))
	if out["error"] != nil {
		t.Fatalf("handler hatası JSON-RPC başarı + isError olmalı: %v", out)
	}
	postStreamable(t, ts, rpc("tools/call", 3, `{"name":"ghost"}`))
	srv.SetCallGate(func(context.Context, GateCall) error { return errors.New("rate limited") })
	postStreamable(t, ts, rpc("tools/call", 4, `{"name":"ctx_tool","arguments":{}}`))

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if strings.Join(rec.calls, ",") != "tool:ctx_tool,tool:boom_tool" {
		t.Fatalf("gözlenen çağrılar: %v (bilinmeyen araç ve kapı reddi gözlenmez)", rec.calls)
	}
	if len(rec.outs) != 2 || rec.outs[0].Err != nil || rec.outs[0].ResultBytes == 0 {
		t.Fatalf("başarı sonucu: %+v", rec.outs)
	}
	if rec.outs[1].Err == nil || rec.outs[1].ErrorClass == "" {
		t.Fatalf("hata sonucu sınıf taşımalı: %+v", rec.outs[1])
	}
}

// v0.10.430 — prompts/get BAYT yazar (mesaj sayısı değil); kaynak/prompt
// hatası sınıf taşır; istemci iptali "cancelled" (timeout değil).
func TestObserverPromptBytesAndResourceErrorClass(t *testing.T) {
	srv, ts := testServer(t)
	srv.RegisterPrompt(Prompt{Name: "two_msgs", Description: "t",
		Renderer: func(context.Context, map[string]string) ([]PromptMessage, error) {
			return []PromptMessage{
				{Role: "system", Content: PromptContent{Type: "text", Text: strings.Repeat("s", 4000)}},
				{Role: "user", Content: PromptContent{Type: "text", Text: strings.Repeat("u", 96)}},
			}, nil
		}})
	srv.RegisterResource(Resource{URI: "coremetry://boom", Name: "boom", MimeType: "text/plain",
		Reader: func(context.Context, string) (string, error) { return "", errors.New("dial tcp: connection refused") }})
	srv.RegisterTool(Tool{Name: "cancel_tool", Description: "t", InputSchema: map[string]any{"type": "object"},
		Handler: func(context.Context, json.RawMessage) (any, error) { return nil, context.Canceled }})
	rec := &observeRec{}
	srv.SetObserver(rec.hook)
	postStreamable(t, ts, rpc("prompts/get", 1, `{"name":"two_msgs","arguments":{}}`))
	postStreamable(t, ts, rpc("resources/read", 2, `{"uri":"coremetry://boom"}`))
	postStreamable(t, ts, rpc("tools/call", 3, `{"name":"cancel_tool","arguments":{}}`))
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.outs) != 3 {
		t.Fatalf("üç gözlem: %+v", rec.outs)
	}
	if rec.outs[0].Err != nil || rec.outs[0].ResultBytes != 4096 || rec.outs[0].ErrorClass != "" {
		t.Fatalf("prompt: bayt 4096, sınıf boş: %+v", rec.outs[0])
	}
	if rec.outs[1].Err == nil || rec.outs[1].ErrorClass != ToolErrBackendUnavailable {
		t.Fatalf("kaynak hatası sınıf taşımalı: %+v", rec.outs[1])
	}
	if rec.outs[2].ErrorClass != ToolErrCancelled {
		t.Fatalf("iptal cancelled sınıfı: %+v", rec.outs[2])
	}
	if promptMessagesBytes(nil) != 0 || outcomeErrorClass(nil) != "" {
		t.Fatal("nil güvenli yardımcılar")
	}
}

// Dört yürütme yolu da kancayı çağırır (runGate ilkesi: yeni yol unutamaz).
func TestEveryExecutionPathObserves(t *testing.T) {
	b, err := os.ReadFile("mcp.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if n := strings.Count(src, "s.beginObserve("); n != 4 {
		t.Fatalf("beginObserve 4 yolda olmalı (tool, resource×2, prompt), %d", n)
	}
	if strings.Count(src, "s.runGate(") != 4 {
		t.Fatal("runGate sayısı değişti — gözlem eşlemesini güncelle")
	}
	if !strings.Contains(src, "tool.Handler(tctx, p.Arguments)") {
		t.Fatal("araç handler'ı gözlem ctx'inden türeyen bütçeli ctx ile çağrılmalı")
	}
	// v0.10.430 — dört çıkış da sınıfı aynı yardımcıdan alır.
	if n := strings.Count(src, "ErrorClass: outcomeErrorClass(err)"); n != 5 {
		t.Fatalf("beş done() çıkışı (tool×2, resource×2, prompt) outcomeErrorClass taşımalı, %d", n)
	}
}
