package api

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/cilcenk/coremetry/internal/copilot"
)

// chat_span_test.go — v0.10.425 (CoSRE denetimi O2): sohbet ağacı.

func bareSpanServer(t *testing.T) (*Server, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	s := &Server{}
	s.tracer = tp.Tracer("test")
	return s, exp
}

func TestChatSpanTreeShape(t *testing.T) {
	s, exp := bareSpanServer(t)
	ctx := copilot.WithMeta(context.Background(), copilot.CallMeta{Surface: "chat"})
	ctx, cs := s.beginChatSpan(ctx, "xid-1")
	tctx, endTurn := cs.turn(ctx, 0, false)
	endTurn(10, 5, nil)
	_ = tctx
	toolCtx, endTool := cs.tool(ctx, "list_problems", false)
	_ = toolCtx
	endTool(1234, false)
	_, endTool2 := cs.tool(ctx, "ext_search", true)
	endTool2(0, true)
	cs.finish(10, 5, errors.New("sağlayıcı \xff bozuk"))
	cs.end()

	spans := exp.GetSpans()
	// v0.10.430 — hiçbir ai.* span'ı SERVER değil (giriş-span nüfusu).
	for _, sp := range exp.GetSpans() {
		if sp.SpanKind == trace.SpanKindServer || sp.SpanKind == trace.SpanKindConsumer {
			t.Fatalf("%s giriş kind'ı taşımamalı: %v", sp.Name, sp.SpanKind)
		}
	}
	byName := map[string][]tracetest.SpanStub{}
	for _, sp := range spans {
		byName[sp.Name] = append(byName[sp.Name], sp)
	}
	if len(byName["ai.chat"]) != 1 || len(byName["ai.chat.turn"]) != 1 || len(byName["ai.tool"]) != 2 {
		t.Fatalf("ağaç: %v", byName)
	}
	root := byName["ai.chat"][0]
	for _, name := range []string{"ai.chat.turn", "ai.tool"} {
		for _, sp := range byName[name] {
			if sp.Parent.SpanID() != root.SpanContext.SpanID() {
				t.Errorf("%s kökün çocuğu değil", name)
			}
		}
	}
	a := attrMap(root.Attributes)
	if a["coremetry.ai.exchange_id"].AsString() != "xid-1" || a["coremetry.ai.chat.tier"].AsString() != "loop" ||
		a["coremetry.ai.chat.rounds"].AsInt64() != 1 || a["coremetry.ai.chat.tools"].AsInt64() != 2 ||
		a["gen_ai.usage.input_tokens"].AsInt64() != 10 || a["coremetry.ai.status"].AsString() != "error" {
		t.Fatalf("kök attribute'ları: %v", a)
	}
	// Hata metni SafeAttr'dan geçmiş (geçersiz UTF-8 yığını düşürürdü).
	for _, ev := range root.Events {
		for _, kv := range ev.Attributes {
			if !strings.ContainsRune(kv.Value.AsString(), 0xFFFD) && strings.Contains(kv.Value.AsString(), "\xff") {
				t.Fatalf("hata metni temizlenmemiş: %q", kv.Value.AsString())
			}
		}
	}
	tools := byName["ai.tool"]
	ta := attrMap(tools[0].Attributes)
	if ta["coremetry.ai.tool.name"].AsString() != "list_problems" || ta["coremetry.ai.tool.origin"].AsString() != "native" ||
		ta["coremetry.ai.tool.bytes"].AsInt64() != 1234 || !ta["coremetry.ai.tool.ok"].AsBool() {
		t.Fatalf("tool attribute'ları: %v", ta)
	}
	if tb := attrMap(tools[1].Attributes); tb["coremetry.ai.tool.origin"].AsString() != "external" || tb["coremetry.ai.tool.ok"].AsBool() {
		t.Fatalf("dış tool: %v", tb)
	}
	// Sızıntı kapısı: hiçbir attribute arg/çıktı gövdesi taşımaz.
	for _, sp := range spans {
		for _, kv := range sp.Attributes {
			if len(kv.Value.AsString()) > 200 {
				t.Fatalf("%s: uzun attribute %s", sp.Name, kv.Key)
			}
		}
	}
}

// Kademe erken dönüşünde defer'lı end kademe adını ve durumu yazar.
func TestChatSpanTier(t *testing.T) {
	s, exp := bareSpanServer(t)
	_, cs := s.beginChatSpan(copilot.WithMeta(context.Background(), copilot.CallMeta{Surface: "chat"}), "x")
	cs.tier("guided", true)
	cs.end()
	a := attrMap(exp.GetSpans()[0].Attributes)
	if a["coremetry.ai.chat.tier"].AsString() != "guided" || a["coremetry.ai.status"].AsString() != "ok" {
		t.Fatalf("%v", a)
	}
}

// ctx tabanlı sarmalayıcı ai.explain açar; surface meta'dan, token'lar
// Observe kancasından (sahte sağlayıcı {10,5}).
func TestCtxWrapperOpensExplainSpan(t *testing.T) {
	fp := newFakeProvider(t, false)
	s, exp := spanServer(t, fp)
	ctx := copilot.WithMeta(context.Background(), copilot.CallMeta{Surface: "chat"})
	if _, err := s.copilotExplainSurface(ctx, "chat-guided", "sys", "user"); err != nil {
		t.Fatal(err)
	}
	spans := exp.GetSpans()
	if len(spans) != 1 || spans[0].Name != "ai.explain" {
		t.Fatalf("span: %v", spans)
	}
	a := attrMap(spans[0].Attributes)
	if a["coremetry.ai.surface"].AsString() != "chat-guided" || a["gen_ai.usage.input_tokens"].AsInt64() != 10 || a["gen_ai.usage.output_tokens"].AsInt64() != 5 {
		t.Fatalf("attribute'lar: %v", a)
	}
}

// Kaynak pinleri: kök span WithMeta(dctx)'den sonra ve ilk kademeden önce;
// iki ChatWithTools yeri de tur span'ı; araç span'ı runChatTool'u sarar;
// üç ctx sarmalayıcısı da explain span'ı açar.
func TestChatSpanWiring(t *testing.T) {
	src, err := os.ReadFile("copilot_chat.go")
	if err != nil {
		t.Fatal(err)
	}
	b := string(src)
	iMeta := strings.Index(b, "copilot.WithMeta(dctx,")
	iSpan := strings.Index(b, "s.beginChatSpan(ctx, exchangeID)")
	iDefer := strings.Index(b, "defer cspan.end()")
	iGuided := strings.Index(b, "s.copilotChatGuided(")
	if !(iMeta > 0 && iSpan > iMeta && iDefer > iSpan && iGuided > iDefer) {
		t.Fatalf("kök span sırası: meta=%d span=%d defer=%d guided=%d", iMeta, iSpan, iDefer, iGuided)
	}
	if n := strings.Count(b, "cspan.turn(ctx, round,"); n != 2 {
		t.Fatalf("iki ChatWithTools yeri de tur span'ı açmalı, %d", n)
	}
	if n := strings.Count(b, "s.copilot.ChatWithTools("); n != 2 {
		t.Fatalf("ChatWithTools çağrı sayısı değişti (%d) — tur span'ı eşlemesini güncelle", n)
	}
	if !strings.Contains(b, "cspan.tool(ctx, tc.Name, extNames[tc.Name])") || !strings.Contains(b, "runChatTool(tctx, h, tc.Input)") || !strings.Contains(b, "endTool(len(tr.Content), tr.IsError)") {
		t.Fatal("araç span'ı runChatTool'u sarmıyor")
	}
	for _, tier := range []string{`cspan.tier("guided"`, `cspan.tier("drawer"`, `cspan.tier("rag"`, `cspan.tier("intent"`} {
		if !strings.Contains(b, tier) {
			t.Errorf("kademe işareti yok: %s", tier)
		}
	}
	obs, _ := os.ReadFile("ai_observability.go")
	if n := strings.Count(string(obs), "s.beginExplainSpanCtx("); n != 3 {
		t.Fatalf("üç ctx sarmalayıcısı da explain span'ı açmalı, %d", n)
	}
}
