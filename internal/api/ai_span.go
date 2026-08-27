package api

import (
	"context"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/devops"
	"github.com/cilcenk/coremetry/internal/selfobs"
)

// ai_span.go — ✨ Explain başına OTel span'ı (v0.10.114, operatör spec'i
// 2026-08-28 "Gözlemlenebilirlik" ekseni).
//
// Neden: bir Explain çağrısının modele NE gittiği bugüne dek yalnız
// ai_calls satırının 4 KB'lık örneğinde ve CodeContext.Reason prozunda
// yaşıyordu — "kaç frame çözüldü, kaç pencere gitti, kod kırpıldı mı,
// kaç token" sorusu SAYI olarak hiçbir yerde yoktu. Span, Coremetry'nin
// kendi self-telemetry hattına (selfobs → kendi OTLP alıcısı → spans)
// düşer; /traces?service=coremetry-monolithic ile okunur, hata span'ı
// olarak dogfood döngüsüne girer ([[feedback-selftelemetry-dogfood]]).
//
// Attribute sözlüğü (tek yazım, testler pinler):
//
//	coremetry.ai.surface            explain-exception | explain-trace | …
//	coremetry.ai.status             ok | error
//	gen_ai.system / gen_ai.request.model       sağlayıcı / model (semconv)
//	gen_ai.usage.input_tokens / output_tokens  token (semconv)
//	coremetry.ai.duration_ms        sağlayıcı turu
//	coremetry.ai.code.requested     "Kodu da incele" işaretli miydi
//	coremetry.ai.code.outcome       devops.CodeOutcome sınıfı
//	coremetry.ai.code.frames_total / candidates / fetched / resolved /
//	                    missed / untried        FetchStats sayıları
//	coremetry.ai.code.windows       bütçe SONRASI modele giden pencere
//	coremetry.ai.context.types      "code,sql" — modele giden kanıt türleri
//	coremetry.ai.context.trimmed    bütçe kırpması/düşmesi oldu mu
//
// Kod GÖVDESİ hiçbir attribute'a girmez (ai_calls maskesiyle aynı
// sözleşme): yalnız sayılar ve sınıflar.

type explainEvidenceKey struct{}

// explainEvidence — bir Explain'e giden kanıt paketi (kod + şema).
type explainEvidence struct {
	Code   devops.CodeContext
	Schema schemaEvidence
}

// withExplainEvidence — kanıtı isteğe iliştirir; sarmalayıcı span'a
// stamplar. Kod istenmeyen yüzeylerde çağrılmaz → requested=false.
func withExplainEvidence(r *http.Request, cc devops.CodeContext, se schemaEvidence) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), explainEvidenceKey{}, explainEvidence{Code: cc, Schema: se}))
}

// tracerOrDefault — enjekte edilmiş tracer yoksa selfobs (prod'da OTLP,
// kapalıyken noop — her iki hâlde de güvenli).
func (s *Server) tracerOrDefault() trace.Tracer {
	if s != nil && s.tracer != nil {
		return s.tracer
	}
	return selfobs.Tracer()
}

// beginExplainSpan — span'ı açar, CallMeta.Observe'u token/süre için
// bağlar, kod kanıtını stamplar; dönen done(err) span'ı kapatır. ctx,
// meta'sı KURULMUŞ bir bağlamdır (WithMeta çağrılmış) — surface oradan.
func (s *Server) beginExplainSpan(r *http.Request, ctx context.Context) (context.Context, func(error)) {
	meta := copilot.MetaFromContext(ctx)
	ctx, span := s.tracerOrDefault().Start(ctx, "ai.explain",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("coremetry.ai.surface", meta.Surface)))
	if ev, ok := r.Context().Value(explainEvidenceKey{}).(explainEvidence); ok {
		span.SetAttributes(codeEvidenceAttrs(ev.Code)...)
		span.SetAttributes(
			attribute.Int("coremetry.ai.schema.columns", ev.Schema.Columns),
			attribute.Bool("coremetry.ai.schema.signal", ev.Schema.Signal),
		)
		if ev.Schema.Block != "" {
			span.SetAttributes(attribute.String("coremetry.ai.context.types", contextTypes(ev.Code)+",schema"))
		}
	} else {
		span.SetAttributes(attribute.Bool("coremetry.ai.code.requested", false))
	}
	meta.Observe = func(u copilot.Usage) {
		span.SetAttributes(
			attribute.String("gen_ai.system", u.Provider),
			attribute.String("gen_ai.request.model", u.Model),
			attribute.Int("gen_ai.usage.input_tokens", int(u.InputTokens)),
			attribute.Int("gen_ai.usage.output_tokens", int(u.OutputTokens)),
			attribute.Int("coremetry.ai.duration_ms", int(u.DurationMs)),
			attribute.String("coremetry.ai.status", u.Status),
		)
	}
	ctx = copilot.WithMeta(ctx, meta)
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "explain failed")
			span.SetAttributes(attribute.String("coremetry.ai.status", "error"))
		}
		span.End()
	}
}

// contextTypes — modele giden kod-kanıt türleri ("code,sql" / "code-missing").
func contextTypes(cc devops.CodeContext) string {
	types := make([]string, 0, 3)
	code, sql := 0, 0
	for _, w := range cc.Windows {
		if w.Resource {
			sql++
		} else {
			code++
		}
	}
	if code > 0 {
		types = append(types, "code")
	}
	if sql > 0 {
		types = append(types, "sql")
	}
	if len(cc.Windows) == 0 {
		types = append(types, "code-missing")
	}
	return strings.Join(types, ",")
}

// codeEvidenceAttrs — CodeContext → attribute listesi. Saf; tablo-testli.
func codeEvidenceAttrs(cc devops.CodeContext) []attribute.KeyValue {
	st := cc.Stats
	types := make([]string, 0, 3)
	code, sql := 0, 0
	for _, w := range cc.Windows {
		if w.Resource {
			sql++
		} else {
			code++
		}
	}
	if code > 0 {
		types = append(types, "code")
	}
	if sql > 0 {
		types = append(types, "sql")
	}
	if len(cc.Windows) == 0 {
		types = append(types, "code-missing")
	}
	return []attribute.KeyValue{
		attribute.Bool("coremetry.ai.code.requested", true),
		attribute.String("coremetry.ai.code.outcome", string(cc.Outcome)),
		attribute.Int("coremetry.ai.code.frames_total", st.FramesTotal),
		attribute.Int("coremetry.ai.code.candidates", st.Candidates),
		attribute.Int("coremetry.ai.code.fetched", st.Fetched),
		attribute.Int("coremetry.ai.code.resolved", st.Resolved),
		attribute.Int("coremetry.ai.code.missed", st.Missed),
		attribute.Int("coremetry.ai.code.untried", st.Untried),
		attribute.Int("coremetry.ai.code.windows", len(cc.Windows)),
		attribute.String("coremetry.ai.context.types", strings.Join(types, ",")),
		attribute.Bool("coremetry.ai.context.trimmed", cc.Trimmed != ""),
	}
}
