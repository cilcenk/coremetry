package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// v0.10.536 — Executor sıra sözleşmesi: bilinmeyen/tekrar yürütülmez ve
// audit'e girmez; kapsam reddi ToolErrorJSON; hata ToolErrorJSON; başarı
// JSON; span bayt+ok ile kapanır; bütçe aşımı hata sınıfına düşer.

type denyScope struct{}

func (denyScope) Constrain(_ context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if name == "search_traces" {
		return nil, errors.New("scope: namespace dışında")
	}
	return args, nil
}

func TestExecutorContract(t *testing.T) {
	var spans []string
	var audits []string
	hooks := Hooks{
		Span: func(ctx context.Context, name string, ext bool) (context.Context, func(int, bool)) {
			return ctx, func(b int, isErr bool) {
				spans = append(spans, name+":"+map[bool]string{true: "ext", false: "nat"}[ext]+":"+map[bool]string{true: "err", false: "ok"}[isErr])
			}
		},
		Audit: func(name string, _ json.RawMessage, _ time.Duration, err error, bytes int) {
			audits = append(audits, name+":"+map[bool]string{true: "err", false: "ok"}[err != nil])
		},
	}
	byName := map[string]Handler{
		"list_problems": func(_ context.Context, _ json.RawMessage) (any, error) { return map[string]any{"n": 2}, nil },
		"boom": func(_ context.Context, _ json.RawMessage) (any, error) {
			return nil, errors.New("code: 159, Timeout exceeded")
		},
		"slow": func(ctx context.Context, _ json.RawMessage) (any, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
				return "late", nil
			}
		},
		"search_traces": func(_ context.Context, _ json.RawMessage) (any, error) { return "x", nil },
	}
	x := NewExecutor(byName, map[string]bool{"boom": true}, hooks).WithScope(denyScope{}).WithBudget(20 * time.Millisecond)
	ctx := context.Background()

	if oc := x.Call(ctx, "nope", nil); oc.Executed || !oc.IsError || oc.Kind != "unknown" || !strings.Contains(oc.Content, `unknown tool "nope"`) {
		t.Fatalf("bilinmeyen: %+v", oc)
	}
	oc := x.Call(ctx, "list_problems", json.RawMessage(`{"a":1,"b":2}`))
	if !oc.Executed || oc.IsError || oc.Kind != "ok" || oc.Content != `{"n":2}` {
		t.Fatalf("başarı: %+v", oc)
	}
	// Anahtar sırası farklı → aynı çağrı → tekrar (yürütülmez, audit yok).
	if oc := x.Call(ctx, "list_problems", json.RawMessage(`{"b":2,"a":1}`)); oc.Executed || oc.Kind != "repeated" || oc.Content != RepeatedCallJSON {
		t.Fatalf("tekrar: %+v", oc)
	}
	if oc := x.Call(ctx, "list_problems", json.RawMessage(`{"a":9}`)); !oc.Executed || oc.Kind != "ok" {
		t.Fatalf("farklı argüman yürütülür: %+v", oc)
	}
	oc = x.Call(ctx, "boom", nil)
	if !oc.Executed || !oc.IsError || oc.Kind != "error" || !strings.Contains(oc.Content, `"retryable":true`) || !strings.Contains(oc.Content, `"hint"`) {
		t.Fatalf("hata sözleşmesi: %+v", oc)
	}
	oc = x.Call(ctx, "slow", nil)
	if !oc.IsError || oc.Kind != "error" || oc.Duration > 500*time.Millisecond {
		t.Fatalf("bütçe aşımı hata sınıfı: %+v", oc)
	}
	if oc := x.Call(ctx, "search_traces", json.RawMessage(`{}`)); oc.Executed || oc.Kind != "scope" || !oc.IsError || !strings.Contains(oc.Content, `"error"`) {
		t.Fatalf("kapsam reddi yürütülmez, ToolErrorJSON: %+v", oc)
	}
	wantSpans := []string{"list_problems:nat:ok", "list_problems:nat:ok", "boom:ext:err", "slow:nat:err"}
	if strings.Join(spans, ",") != strings.Join(wantSpans, ",") {
		t.Fatalf("span'lar %v, want %v", spans, wantSpans)
	}
	wantAudit := []string{"list_problems:ok", "list_problems:ok", "boom:err", "slow:err"}
	if strings.Join(audits, ",") != strings.Join(wantAudit, ",") {
		t.Fatalf("audit %v, want %v (bilinmeyen/tekrar/kapsam yürütülmedi, satır yok)", audits, wantAudit)
	}
	if !x.Known("boom") || x.Known("nope") {
		t.Fatal("Known")
	}
}

func TestRepeatKeyCanonical(t *testing.T) {
	seen := map[string]bool{}
	if MarkRepeatedCall(seen, "t", json.RawMessage(`{"x":1,"y":2}`)) {
		t.Fatal("ilk çağrı tekrar değil")
	}
	if !MarkRepeatedCall(seen, "t", json.RawMessage(`{"y":2,"x":1}`)) {
		t.Fatal("anahtar sırası farkı aynı çağrıdır")
	}
	if MarkRepeatedCall(seen, "t", json.RawMessage(`{"x":9}`)) || MarkRepeatedCall(seen, "t2", json.RawMessage(`{"x":1,"y":2}`)) {
		t.Fatal("farklı argüman / farklı tool yeni çağrıdır")
	}
	if MarkRepeatedCall(seen, "raw", json.RawMessage(`not json`)) || !MarkRepeatedCall(seen, "raw", json.RawMessage(` not json `)) {
		t.Fatal("çözülemeyen argüman ham hâliyle (kırpılmış) anahtarlanır")
	}
	for _, f := range []string{`"error"`, `"retryable"`, `"hint"`} {
		if !strings.Contains(RepeatedCallJSON, f) {
			t.Errorf("RepeatedCallJSON %s taşımalı", f)
		}
	}
}
