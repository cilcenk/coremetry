package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// v0.10.534 — Request.ExtraBody openai-uyumlu gövdeye iner, çekirdek
// anahtarları ezemez; üç gövde (buffered/stream/tools) applyExtraBody çağırır.
func TestExtraBodyMergedIntoOpenAIRequest(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	req := Request{Model: "qwen3-8b", System: "S", User: "U", ExtraBody: map[string]any{
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
		"model":                "HACK",
	}}
	if _, err := DoOpenAI(context.Background(), Config{BaseURL: srv.URL, HTTPClient: srv.Client()}, req); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "qwen3-8b" {
		t.Fatalf("çekirdek anahtar ezildi: %v", got["model"])
	}
	if ctk, ok := got["chat_template_kwargs"].(map[string]any); !ok || ctk["enable_thinking"] != false {
		t.Fatalf("chat_template_kwargs gövdeye inmedi: %v", got)
	}
	for _, f := range []string{"openai.go", "stream.go", "tools.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "applyExtraBody(body, req.ExtraBody)") {
			t.Errorf("%s: openai-uyumlu gövde applyExtraBody çağırmalı", f)
		}
	}
}
