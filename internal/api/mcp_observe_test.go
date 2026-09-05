package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
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
	// v0.10.430 — otelhttp SERVER span'ının çocuğu: INTERNAL, giriş-span
	// nüfusuna girmez; ad attribute'u türe göre (URI mcp.tool'a girmez).
	for _, sp := range spans {
		if sp.SpanKind != trace.SpanKindInternal {
			t.Fatalf("%s kind %v, want INTERNAL", sp.Name, sp.SpanKind)
		}
	}
	if b := attrMap(spans[1].Attributes); b["mcp.resource"].AsString() != "coremetry://services" || b["mcp.tool"].AsString() != "" {
		t.Fatalf("kaynak adı mcp.resource'ta olmalı, mcp.tool boş: %v", b)
	}
	if mcpNameAttr("prompt") != "mcp.prompt" || mcpNameAttr("tool") != "mcp.tool" || mcpNameAttr("x") != "mcp.tool" {
		t.Fatal("mcpNameAttr")
	}
}

// v0.10.430 — M1'in audit yarısı DAVRANIŞLA: ctx'teki istek + kimlik →
// mcp.tool.call satırı auditQ'ya düşer; kaynak okuması satır üretmez;
// kimliksiz istek sessizce yazmaz (s.audit sözleşmesi) ama span kalır.
func TestMCPObserveAuditRow(t *testing.T) {
	s, exp := bareSpanServer(t)
	s.auditQ = make(chan chstore.AuditEntry, 4)
	req := httptest.NewRequest("POST", "/api/mcp", nil)
	req.RemoteAddr = "10.1.2.3:4444"
	req = req.WithContext(auth.ContextWithClaims(req.Context(), &auth.Claims{UserID: "token:cmk_1", Email: "agent@example.test", Role: auth.RoleViewer}))
	var got *http.Request
	withMCPRequest(func(_ http.ResponseWriter, r *http.Request) { got = r })(httptest.NewRecorder(), req)
	ctx, done := s.mcpObserve(got.Context(), "tool", "search_traces", json.RawMessage(`{"service":"checkout"}`))
	done(mcp.CallOutcome{ResultBytes: 9})
	_, done2 := s.mcpObserve(ctx, "resource", "coremetry://services", nil)
	done2(mcp.CallOutcome{ResultBytes: 1})
	select {
	case e := <-s.auditQ:
		if e.Action != "mcp.tool.call" || e.TargetKind != "mcp_tool" || e.TargetID != "search_traces" || e.ActorID != "token:cmk_1" || !strings.HasPrefix(e.IP, "10.1.2.3") {
			t.Fatalf("audit satırı: %+v", e)
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(e.Details), &d); err != nil || d["ok"] != true || d["resultBytes"] != float64(9) || d["transport"] != "mcp-inbound" {
			t.Fatalf("detay: %s (%v)", e.Details, err)
		}
	default:
		t.Fatal("araç çağrısı audit satırı üretmeli")
	}
	select {
	case e := <-s.auditQ:
		t.Fatalf("kaynak okuması audit üretmemeli: %+v", e)
	default:
	}
	// Kimliksiz: span var, audit yok.
	anon := httptest.NewRequest("POST", "/api/mcp", nil)
	_, done3 := s.mcpObserve(context.WithValue(anon.Context(), mcpRequestKey{}, anon), "tool", "list_services", nil)
	done3(mcp.CallOutcome{})
	select {
	case e := <-s.auditQ:
		t.Fatalf("kimliksiz çağrı audit üretmemeli: %+v", e)
	default:
	}
	if n := len(exp.GetSpans()); n != 3 {
		t.Fatalf("üç span: %d", n)
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
