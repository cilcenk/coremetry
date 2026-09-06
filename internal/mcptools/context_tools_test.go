package mcptools

import (
	"context"
	"encoding/json"
	"testing"
)

// v0.10.478 (Faz 4, F4-1) — bağlam tool'ları: konuşma yoksa dürüst hata; kapanışlarla gidiş-dönüş.

func TestContextToolsWithoutConversation(t *testing.T) {
	d := Deps{}
	for name, h := range map[string]func(context.Context, json.RawMessage) (any, error){
		"set": setContextTool(d).Handler, "get": getContextTool(d).Handler, "clear": clearContextTool(d).Handler,
	} {
		if _, err := h(context.Background(), json.RawMessage(`{"service":"x"}`)); err == nil {
			t.Errorf("%s: konuşma yokken hata beklenir", name)
		}
	}
}

func TestContextToolsRoundTrip(t *testing.T) {
	state := map[string]any{}
	d := Deps{
		CtxGet: func(ctx context.Context) (map[string]any, error) { return state, nil },
		CtxSet: func(ctx context.Context, patch map[string]any) (map[string]any, error) {
			for k, v := range patch {
				state[k] = v
			}
			return state, nil
		},
		CtxClear: func(ctx context.Context, fields []string) (map[string]any, error) {
			if len(fields) == 0 {
				state = map[string]any{}
			}
			for _, f := range fields {
				delete(state, f)
			}
			return state, nil
		},
	}
	if _, err := setContextTool(d).Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("boş yama hata")
	}
	out, err := setContextTool(d).Handler(context.Background(), json.RawMessage(`{"service":"checkout","range_s":3600}`))
	if err != nil || out.(map[string]any)["context"].(map[string]any)["service"] != "checkout" {
		t.Fatalf("set: %v %v", out, err)
	}
	out, _ = getContextTool(d).Handler(context.Background(), nil)
	if out.(map[string]any)["context"].(map[string]any)["range_s"] != float64(3600) {
		t.Fatalf("get: %v", out)
	}
	out, _ = clearContextTool(d).Handler(context.Background(), json.RawMessage(`{"fields":["service"]}`))
	if m := out.(map[string]any)["context"].(map[string]any); m["service"] != nil || m["range_s"] == nil {
		t.Fatalf("clear kısmi: %v", m)
	}
	out, _ = clearContextTool(d).Handler(context.Background(), json.RawMessage(`{}`))
	if len(out.(map[string]any)["context"].(map[string]any)) != 0 {
		t.Fatalf("clear hepsi: %v", out)
	}
}
