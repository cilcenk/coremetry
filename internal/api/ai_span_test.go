package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/devops"
)

// ai_span_test.go — v0.10.114. HER EXPLAIN ÇAĞRISI BİR SPAN.
//
// Operatör spec'i (2026-08-28): "her Explain çağrısı için span üret —
// çözülen/çözülemeyen frame sayısı, eklenen bağlam türleri, token
// kullanımı attribute olarak". Kod GÖVDESİ span'a girmez.

func attrMap(kvs []attribute.KeyValue) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	for _, kv := range kvs {
		m[string(kv.Key)] = kv.Value
	}
	return m
}

func spanServer(t *testing.T, fp *fakeProvider) (*Server, *tracetest.InMemoryExporter) {
	t.Helper()
	s := codeServer(t, fp, newCapRecorder())
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })
	s.tracer = tp.Tracer("test")
	return s, exp
}

func TestExplainSpanCarriesEvidenceAndTokens(t *testing.T) {
	fp := newFakeProvider(t, false)
	s, exp := spanServer(t, fp)
	cc := devops.CodeContext{
		Repo: "core-service", Outcome: devops.CodePartial, Trimmed: "kod bütçesi (4000 karakter) doldu — 1 pencere düştü",
		Stats: devops.FetchStats{FramesTotal: 12, Candidates: 6, Fetched: 3, Resolved: 3, Missed: 2, Untried: 1, Dupes: 1},
		Windows: []devops.CodeWindow{
			{Path: "/src/A.java", Frame: "com.x.A.m(A.java:12)", Line: 12, FromLine: 10, ToLine: 14, Content: "12| throw new X(\"SECRET_BODY\");"},
			{Path: "/src/M.xml", Resource: true, FromLine: 11, ToLine: 16, Frame: "statement id: q", Content: "11| <select id=\"q\">"},
		},
	}
	r := httptest.NewRequest(http.MethodPost, "/api/copilot/explain-exception/fp1", nil)
	if _, err := s.copilotExplainCode(r, copilot.SystemPromptException(), copilot.SystemPromptExceptionWithCode(), "Exception GRUBU: x", cc); err != nil {
		t.Fatal(err)
	}
	spans := exp.GetSpans()
	if len(spans) != 1 || spans[0].Name != "ai.explain" {
		t.Fatalf("span sayısı/adı: %d %v", len(spans), spans)
	}
	a := attrMap(spans[0].Attributes)
	want := map[string]any{
		"coremetry.ai.surface":           "explain-exception",
		"coremetry.ai.status":            "ok",
		"coremetry.ai.code.requested":    true,
		"coremetry.ai.code.outcome":      "partial",
		"coremetry.ai.code.frames_total": int64(12),
		"coremetry.ai.code.candidates":   int64(6),
		"coremetry.ai.code.fetched":      int64(3),
		"coremetry.ai.code.resolved":     int64(3),
		"coremetry.ai.code.missed":       int64(2),
		"coremetry.ai.code.untried":      int64(1),
		"coremetry.ai.code.windows":      int64(2),
		"coremetry.ai.context.types":     "code,sql",
		"coremetry.ai.context.trimmed":   true,
		"gen_ai.usage.input_tokens":      int64(10),
		"gen_ai.usage.output_tokens":     int64(5),
		"gen_ai.request.model":           "gemma4",
		"gen_ai.system":                  string(copilot.ProviderOpenAI),
	}
	for k, v := range want {
		got, ok := a[k]
		if !ok {
			t.Errorf("attribute yok: %s", k)
			continue
		}
		if got.AsInterface() != v {
			t.Errorf("%s = %v, istenen %v", k, got.AsInterface(), v)
		}
	}
	if _, ok := a["coremetry.ai.duration_ms"]; !ok {
		t.Error("süre attribute'u yok")
	}
	// Kod gövdesi span'a SIZMAZ.
	for k, v := range a {
		if s, ok := v.AsInterface().(string); ok && (s == "SECRET_BODY" || len(s) > 200) {
			t.Errorf("span attribute'u kod/uzun metin taşıyor: %s=%q", k, s)
		}
	}
}

func TestExplainSpanWithoutCodeAndOnError(t *testing.T) {
	fp := newFakeProvider(t, false)
	s, exp := spanServer(t, fp)
	r := httptest.NewRequest(http.MethodPost, "/api/copilot/explain-problem/p1", nil)
	if _, err := s.copilotExplain(r, copilot.SystemPromptProblem(), "PROBLEM: x"); err != nil {
		t.Fatal(err)
	}
	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("span=%d", len(spans))
	}
	a := attrMap(spans[0].Attributes)
	if a["coremetry.ai.code.requested"].AsBool() || a["coremetry.ai.surface"].AsString() != "explain-problem" {
		t.Errorf("kodsuz yüzey attribute'ları: %v", a)
	}
	if _, ok := a["coremetry.ai.code.frames_total"]; ok {
		t.Error("kod istenmeyen yüzeyde frame sayıları yazılmış")
	}

	// Hata: kapalı copilot → Explain hata döner → span Error.
	exp.Reset()
	s2, exp2 := spanServer(t, fp)
	s2.copilot = copilot.New(copilot.ProviderOpenAI, "", "gemma4") // yapılandırılmamış → Active=false
	_, err := s2.copilotExplain(r, copilot.SystemPromptProblem(), "PROBLEM: x")
	if err == nil {
		t.Fatal("kapalı copilot hata vermedi")
	}
	sp := exp2.GetSpans()
	if len(sp) != 1 || sp[0].Status.Code != codes.Error || attrMap(sp[0].Attributes)["coremetry.ai.status"].AsString() != "error" {
		t.Errorf("hata span'ı: %+v", sp)
	}
}

func TestCodeEvidenceAttrsMissing(t *testing.T) {
	a := attrMap(codeEvidenceAttrs(devops.CodeContext{Outcome: devops.CodeTreeMiss, Reason: "ağaçta yok"}))
	if a["coremetry.ai.context.types"].AsString() != "code-missing" || a["coremetry.ai.code.windows"].AsInt64() != 0 || a["coremetry.ai.code.outcome"].AsString() != "tree-miss" {
		t.Errorf("ıska attribute'ları: %v", a)
	}
}
