package api

// chat_span.go — v0.10.425 (CoSRE denetimi O2): ajan döngüsünün OTel
// span'ları. Eskiden sohbetin araç zinciri yalnız SSE'ye düşüyordu; tek
// AI span'ı (ai.explain) yalnız dört *http.Request tabanlı sarmalayıcıda
// açılıyordu, ctx tabanlı üç ikiz (guided/drawer/rag/intent yolları) ve
// döngünün kendisi span'sızdı — CH sorguları ve dış MCP çağrıları öksüz
// kök span'lardı. "Sohbet neden 40 sn sürdü" cevaplanamıyordu.
//
// Ağaç:
//
//	ai.chat (alışveriş; exchange_id ile ai_calls satırına bağlanır)
//	├─ ai.explain (guided/drawer/rag/intent kademelerinin tek model çağrısı)
//	├─ ai.chat.turn (serbest döngü turu; gen_ai token'ları)
//	├─ ai.tool (araç çağrısı; ad, köken, bayt, ok — ARG/ÇIKTI gövdesi YOK)
//	│   └─ clickhouse.query / mcpclient.call (mevcut span'lar, artık çocuk)
//	└─ …
//
// Sözleşme ai_span.go ile aynı: yalnız sayılar ve sınıflar; hata metni
// selfobs.SafeAttr'dan geçer (geçersiz UTF-8 tüm OTLP yığınını düşürür);
// self-obs kapalıyken tracer noop — bedel sıfır. Deferred end: dört
// erken-dönüş kademesi var, aksi hâlde span sızar.

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/selfobs"
)

// chatSpan — ai.chat kök span'ı + sayaçlar.
type chatSpan struct {
	s      *Server
	span   trace.Span
	tierV  string
	rounds int
	tools  int
	in     uint32
	out    uint32
	err    error
}

// beginChatSpan — alışverişin kök span'ı. Dönen ctx'i KULLAN: kademeler,
// turlar, araçlar ve CH sorguları onun altında iç içe geçer.
func (s *Server) beginChatSpan(ctx context.Context, exchangeID string) (context.Context, *chatSpan) {
	meta := copilot.MetaFromContext(ctx)
	ctx, span := s.tracerOrDefault().Start(ctx, "ai.chat",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("coremetry.ai.surface", meta.Surface),
			attribute.String("coremetry.ai.exchange_id", exchangeID),
		))
	return ctx, &chatSpan{s: s, span: span, tierV: "loop"}
}

// tier — hangi kademe cevapladı (guided | drawer | rag | intent | loop).
func (c *chatSpan) tier(name string, ok bool) {
	c.tierV = name
	if !ok {
		c.err = fmt.Errorf("%s kademesi başarısız", name)
	}
}

// finish — serbest döngünün toplamları; RecordUsage ile AYNI değerler
// (span ile ai_calls satırı ayrışamaz).
func (c *chatSpan) finish(in, out uint32, err error) {
	c.in, c.out, c.err = in, out, err
}

// end — defer ile; dört erken dönüşte de kapanır.
func (c *chatSpan) end() {
	c.span.SetAttributes(
		attribute.String("coremetry.ai.chat.tier", c.tierV),
		attribute.Int("coremetry.ai.chat.rounds", c.rounds),
		attribute.Int("coremetry.ai.chat.tools", c.tools),
		attribute.Int("gen_ai.usage.input_tokens", int(c.in)),
		attribute.Int("gen_ai.usage.output_tokens", int(c.out)),
		attribute.String("coremetry.ai.status", statusOf(c.err)),
	)
	if c.err != nil {
		c.span.RecordError(fmt.Errorf("%s", selfobs.SafeAttr(c.err.Error())))
		c.span.SetStatus(codes.Error, "chat failed")
	}
	c.span.End()
}

// turn — bir ChatWithTools turu (ai.chat.turn). İki çağrı yeri de bunu
// kullanır (döngü + tur tavanı; chat_roundcap_parity sınıfı).
func (c *chatSpan) turn(ctx context.Context, round int, retry bool) (context.Context, func(in, out uint32, err error)) {
	c.rounds++
	ctx, span := c.s.tracerOrDefault().Start(ctx, "ai.chat.turn",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.Int("coremetry.ai.turn.round", round),
			attribute.Bool("coremetry.ai.turn.retry", retry),
		))
	return ctx, func(in, out uint32, err error) {
		span.SetAttributes(
			attribute.Int("gen_ai.usage.input_tokens", int(in)),
			attribute.Int("gen_ai.usage.output_tokens", int(out)),
			attribute.String("coremetry.ai.status", statusOf(err)),
		)
		if err != nil {
			span.RecordError(fmt.Errorf("%s", selfobs.SafeAttr(err.Error())))
			span.SetStatus(codes.Error, "turn failed")
		}
		span.End()
	}
}

// tool — gerçekten ÇALIŞAN bir araç çağrısı (ai.tool). Bilinmeyen araç ve
// tekrar koruması yürütülmez, span da açılmaz (v0.10.53 "çalışmayan çağrı
// kanıt değildir" kuralı). Arg/çıktı gövdesi span'a GİRMEZ — yalnız bayt.
func (c *chatSpan) tool(ctx context.Context, name string, external bool) (context.Context, func(bytes int, isErr bool)) {
	c.tools++
	origin := "native"
	if external {
		origin = "external"
	}
	ctx, span := c.s.tracerOrDefault().Start(ctx, "ai.tool",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("coremetry.ai.tool.name", name),
			attribute.String("coremetry.ai.tool.origin", origin),
		))
	return ctx, func(bytes int, isErr bool) {
		span.SetAttributes(
			attribute.Int("coremetry.ai.tool.bytes", bytes),
			attribute.Bool("coremetry.ai.tool.ok", !isErr),
		)
		if isErr {
			span.SetStatus(codes.Error, "tool failed")
		}
		span.End()
	}
}

// beginExplainSpanCtx — ai.explain'in ctx tabanlı ikizi: kanıt paketi yok
// (istek yok), surface meta'dan; Observe kancası token/süre/sağlayıcıyı
// yazar. Guided/drawer/rag/intent kademeleri ve SWR arka plan tazelemesi
// buradan geçer; sohbet altında çocuk, aksi hâlde kök span.
func (s *Server) beginExplainSpanCtx(ctx context.Context) (context.Context, func(error)) {
	meta := copilot.MetaFromContext(ctx)
	ctx, span := s.tracerOrDefault().Start(ctx, "ai.explain",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("coremetry.ai.surface", meta.Surface)))
	prev := meta.Observe
	meta.Observe = func(u copilot.Usage) {
		span.SetAttributes(
			attribute.String("gen_ai.system", u.Provider),
			attribute.String("gen_ai.request.model", u.Model),
			attribute.Int("gen_ai.usage.input_tokens", int(u.InputTokens)),
			attribute.Int("gen_ai.usage.output_tokens", int(u.OutputTokens)),
			attribute.Int("coremetry.ai.duration_ms", int(u.DurationMs)),
			attribute.String("coremetry.ai.status", u.Status),
		)
		if prev != nil {
			prev(u)
		}
	}
	ctx = copilot.WithMeta(ctx, meta)
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(fmt.Errorf("%s", selfobs.SafeAttr(err.Error())))
			span.SetStatus(codes.Error, "explain failed")
			span.SetAttributes(attribute.String("coremetry.ai.status", "error"))
		}
		span.End()
	}
}

func statusOf(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}
