package api

// mcp_observe.go — v0.10.426 (CoSRE denetimi M1): gelen MCP yürütmeleri
// için span + audit. Span adı türe göre (mcp.tool.call / mcp.resource.read
// / mcp.prompt.get), otelhttp'nin "POST /api/mcp" span'ının çocuğu;
// handler'ın clickhouse.query span'ları bunun altında — "hangi dış ajan
// bütçeyi yaktı" /traces'te cevaplanır. Audit yalnız TOOL çağrısında
// (kaynak/prompt okuması ucuz ve gürültü olur); satır giden çağrının
// (mcp.call) ikizi: aktör = cmk_ jeton kimliği (token:<id>), argüman
// yalnız 256 rune önizleme (mcpCallAuditDetails ile aynı disiplin).
//
// ai_calls satırı YAZILMAZ: gelen araç çağrısı LLM çağırmaz; sıfır
// sağlayıcılı satır /ai KPI'larını (model başına gecikme, maliyet) bozar.
// s.audit *http.Request ister (kimlik + IP): üç MCP rotası withMCPRequest
// ile isteği ctx'e iliştirir; kimliksiz çağrıda audit sessizce yazılmaz
// (s.audit sözleşmesi) ama SPAN yine kaydedilir.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/mcp"
	"github.com/cilcenk/coremetry/internal/selfobs"
)

type mcpRequestKey struct{}

// withMCPRequest — isteği ctx'e iliştirir; audit satırı kimlik + IP'yi
// oradan okur. Auth middleware mux'un dışında sarar: r zaten claims taşır.
func withMCPRequest(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h(w, r.WithContext(context.WithValue(r.Context(), mcpRequestKey{}, r)))
	}
}

func mcpSpanName(kind string) string {
	switch kind {
	case "tool":
		return "mcp.tool.call"
	case "resource":
		return "mcp.resource.read"
	case "prompt":
		return "mcp.prompt.get"
	}
	return "mcp.call"
}

// mcpToolAuditDetails — sırsız iz: araç, kırpık arg önizlemesi, süre,
// sonuç. Tam arg gövdesi bilinçli yazılmaz (sorgu metni hassas olabilir).
func mcpToolAuditDetails(tool string, args json.RawMessage, dur time.Duration, o mcp.CallOutcome) string {
	preview := strings.TrimSpace(string(args))
	if runes := []rune(preview); len(runes) > 256 {
		preview = string(runes[:256]) + "…"
	}
	b, _ := json.Marshal(map[string]any{
		"tool": tool, "argsPreview": preview, "durationMs": dur.Milliseconds(),
		"ok": o.Err == nil, "errorClass": o.ErrorClass, "resultBytes": o.ResultBytes, "transport": "mcp-inbound",
	})
	return string(b)
}

// mcpObserve — mcp.Observer implementasyonu (api.SetMCP ile bağlanır).
func (s *Server) mcpObserve(ctx context.Context, kind, name string, args json.RawMessage) (context.Context, func(mcp.CallOutcome)) {
	actor := "anonymous"
	if claims := auth.FromContext(ctx); claims != nil && claims.UserID != "" {
		actor = claims.UserID
	}
	ctx, span := s.tracerOrDefault().Start(ctx, mcpSpanName(kind),
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("mcp.kind", kind),
			attribute.String("mcp.tool", name),
			attribute.String("coremetry.mcp.actor", actor),
			attribute.String("coremetry.mcp.direction", "inbound"),
		))
	t0 := time.Now()
	req, _ := ctx.Value(mcpRequestKey{}).(*http.Request)
	return ctx, func(o mcp.CallOutcome) {
		dur := time.Since(t0)
		span.SetAttributes(
			attribute.Int("coremetry.mcp.duration_ms", int(dur.Milliseconds())),
			attribute.Int("coremetry.mcp.result_bytes", o.ResultBytes),
			attribute.String("coremetry.mcp.error_class", o.ErrorClass),
			attribute.String("coremetry.mcp.status", statusOf(o.Err)),
		)
		if o.Err != nil {
			span.RecordError(fmt.Errorf("%s", selfobs.SafeAttr(o.Err.Error())))
			span.SetStatus(codes.Error, "mcp call failed")
		}
		span.End()
		if kind == "tool" && req != nil {
			s.audit(req, "mcp.tool.call", "mcp_tool", name, mcpToolAuditDetails(name, args, dur, o))
		}
	}
}
