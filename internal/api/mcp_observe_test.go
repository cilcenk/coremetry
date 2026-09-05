package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/mcp"
)

// v0.10.426 — M1: gelen MCP yürütmesi span'ı; attribute'lar sayı/sınıf,
// arg gövdesi yok; hata metni SafeAttr'dan.
func TestMCPObserveSpan(t *testing.T) {
	s, exp := bareSpanServer(t)
	ctx, done := s.mcpObserve(context.Background(), "tool", "search_traces", json.RawMessage(`{"service":"checkout"}`))
	if ctx == nil {
		t.Fatal("ctx")
	}
	done(mcp.CallOutcome{Err: errors.New("deadline \xff"), ErrorClass: "timeout"})
	_, done2 := s.mcpObserve(context.Background(), "resource", "coremetry://services", nil)
	done2(mcp.CallOutcome{ResultBytes: 42})
	spans := exp.GetSpans()
	if len(spans) != 2 || spans[0].Name != "mcp.tool.call" || spans[1].Name != "mcp.resource.read" {
		t.Fatalf("span'lar: %v", spans)
	}
	a := attrMap(spans[0].Attributes)
	if a["mcp.tool"].AsString() != "search_traces" || a["coremetry.mcp.actor"].AsString() != "anonymous" ||
		a["coremetry.mcp.error_class"].AsString() != "timeout" || a["coremetry.mcp.status"].AsString() != "error" ||
		a["coremetry.mcp.direction"].AsString() != "inbound" {
		t.Fatalf("attribute'lar: %v", a)
	}
	for _, kv := range spans[0].Attributes {
		if strings.Contains(kv.Value.AsString(), "checkout") {
			t.Fatal("arg gövdesi span'a girmemeli")
		}
	}
	if b := attrMap(spans[1].Attributes); b["coremetry.mcp.result_bytes"].AsInt64() != 42 || b["coremetry.mcp.status"].AsString() != "ok" {
		t.Fatalf("kaynak span'ı: %v", b)
	}
}

func TestMCPToolAuditDetails(t *testing.T) {
	d := mcpToolAuditDetails("search_traces", json.RawMessage(`{"q":"`+strings.Repeat("x", 400)+`"}`), 0, mcp.CallOutcome{ResultBytes: 7})
	var m map[string]any
	if err := json.Unmarshal([]byte(d), &m); err != nil {
		t.Fatal(err)
	}
	if len([]rune(m["argsPreview"].(string))) > 260 || m["ok"] != true || m["transport"] != "mcp-inbound" {
		t.Fatalf("detay: %v", m)
	}
}

// Kaynak pinleri: kanca SetMCP'de bağlı, üç rota isteği ctx'e iliştirir.
func TestMCPObserverWired(t *testing.T) {
	b, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "m.SetObserver(s.mcpObserve)") {
		t.Fatal("SetMCP gözlem kancasını bağlamıyor")
	}
	if n := strings.Count(src, "withMCPRequest(s.mcp."); n != 3 {
		t.Fatalf("üç MCP rotası da withMCPRequest ile sarılmalı, %d", n)
	}
}
