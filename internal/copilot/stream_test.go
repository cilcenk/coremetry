package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// v0.8.404 — token streaming for the guided narration call with a
// transparent runtime fallback (vLLM stream support unverified — the
// code adapts, never assumes).
//
// FAZ 1.3 (v0.9.1125) — bu dosya artık YALNIZ POLİTİKAYI test ediyor:
// karar önbelleği ve StreamText'in uçtan uca geri düşüşü. Saf katman
// (SSE biriktiricileri + karar tablosu) internal/ai/provider'a taşındı
// ve testleri de oraya gitti (provider/stream_test.go) — kapsam
// bölündü, DÜŞMEDİ.
//
// Burada kalanlar:
//  1. (provider,baseURL,model) karar önbelleği + Configure'da sıfırlama,
//  2. StreamText uçtan uca: gerçek SSE akışı, stream:true'ya 400 dönen
//     uç (BİR buffered tekrar + kararın önbelleğe yazılması), bayrağı
//     yutup 200+JSON dönen geçit (tek atış çözümlenir, ÇİFT FATURA yok),
//     anında EOF (bir kez düş, karar YAZMA),
//  3. anthropic yolunun aynı politikası (sabit uç olduğu için enjekte
//     edilmiş taşımayla).

// ─── Verdict cache + reset-on-Configure ─────────────────────────────

func TestStreamVerdictCacheResetOnConfigure(t *testing.T) {
	s := New("openai", "", "m1")
	s.Configure("openai", "", "m1", "http://vllm:8000/v1", false, true)
	if s.streamKnownUnsupported("openai", "http://vllm:8000/v1", "m1") {
		t.Fatal("fresh service must not have a verdict")
	}
	s.markStreamUnsupported("openai", "http://vllm:8000/v1", "m1")
	if !s.streamKnownUnsupported("openai", "http://vllm:8000/v1", "m1") {
		t.Fatal("verdict not cached")
	}
	// The key hashes ALL inputs — a different model on the same base
	// must NOT inherit the verdict.
	if s.streamKnownUnsupported("openai", "http://vllm:8000/v1", "m2") {
		t.Fatal("verdict leaked across models")
	}
	// Configure (any settings write) resets — the endpoint may have
	// changed underneath the same knobs.
	s.Configure("openai", "", "m1", "http://vllm:8000/v1", false, true)
	if s.streamKnownUnsupported("openai", "http://vllm:8000/v1", "m1") {
		t.Fatal("Configure must reset the verdict cache")
	}
}

// ─── StreamText end-to-end (httptest) ───────────────────────────────

// requestWantsStream decodes a captured request body and reports the
// "stream" flag.
func requestWantsStream(t *testing.T, body []byte) bool {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	b, _ := m["stream"].(bool)
	return b
}

func TestStreamTextOpenAISSE(t *testing.T) {
	var reqBodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reqBodies = append(reqBodies, b)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"choices":[{"delta":{"content":"canlı "}}]}`,
			`{"choices":[{"delta":{"content":"akış"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":3}}`,
			`[DONE]`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
	}))
	defer srv.Close()
	s := New("openai", "", "test-model")
	s.Configure("openai", "", "test-model", srv.URL, false, true)

	var deltas []string
	out, err := s.StreamText(context.Background(), "sys", "user", func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("StreamText: %v", err)
	}
	if out != "canlı akış" {
		t.Fatalf("out = %q; want the full streamed text", out)
	}
	if got := strings.Join(deltas, "|"); got != "canlı |akış" {
		t.Fatalf("deltas = %q; want them live, in order", got)
	}
	if len(reqBodies) != 1 || !requestWantsStream(t, reqBodies[0]) {
		t.Fatalf("want exactly 1 request with stream:true; got %d", len(reqBodies))
	}
}

func TestStreamTextOpenAIFallbackOn400CachesVerdict(t *testing.T) {
	// A vLLM-build-style endpoint: 400 on stream:true, fine buffered.
	var reqBodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reqBodies = append(reqBodies, b)
		w.Header().Set("Content-Type", "application/json")
		if requestWantsStream(t, b) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"stream is not supported"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"buffered cevap"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer srv.Close()
	s := New("openai", "", "test-model")
	s.Configure("openai", "", "test-model", srv.URL, false, true)

	// Call 1: stream probe 400s → transparent buffered retry, SAME
	// answer contract, zero deltas.
	var deltas []string
	out, err := s.StreamText(context.Background(), "sys", "user", func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("StreamText with fallback: %v", err)
	}
	if out != "buffered cevap" || len(deltas) != 0 {
		t.Fatalf("out=%q deltas=%d; want the buffered answer with zero deltas", out, len(deltas))
	}
	if len(reqBodies) != 2 || !requestWantsStream(t, reqBodies[0]) || requestWantsStream(t, reqBodies[1]) {
		t.Fatalf("want probe(stream:true)+retry(buffered); got %d requests", len(reqBodies))
	}
	// Geri düşülen çağrı Explain'in TA KENDİSİ olmalı: aynı gövde
	// şekli, aynı bütçe. (Faz 1.3 sonrası ikisi de provider'ın tek
	// yazılışından çıkıyor; bu, o zincirin kopmadığının pini.)
	var buffered map[string]any
	if err := json.Unmarshal(reqBodies[1], &buffered); err != nil {
		t.Fatalf("buffered body: %v", err)
	}
	if _, has := buffered["stream_options"]; has {
		t.Fatalf("buffered tekrar akış alanlarını taşıyor: %v", buffered)
	}
	if buffered["max_tokens"] != float64(4096) {
		t.Fatalf("buffered tekrarın bütçesi = %v; want 4096", buffered["max_tokens"])
	}

	// Call 2: the verdict is cached — NO re-probe, one buffered call.
	out, err = s.StreamText(context.Background(), "sys", "user", nil)
	if err != nil || out != "buffered cevap" {
		t.Fatalf("cached-verdict call: out=%q err=%v", out, err)
	}
	if len(reqBodies) != 3 || requestWantsStream(t, reqBodies[2]) {
		t.Fatalf("cached verdict must skip the stream probe; got %d requests, last stream=%v",
			len(reqBodies), requestWantsStream(t, reqBodies[len(reqBodies)-1]))
	}
}

func TestStreamTextOpenAI200JSONParsedOneShot(t *testing.T) {
	// A gateway that silently ignores stream:true and answers 200+JSON:
	// the body IS the completion — it must be parsed directly (no
	// second, double-billed request) and the verdict cached.
	var nReqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nReqs++
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"tek atış"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`))
	}))
	defer srv.Close()
	s := New("openai", "", "test-model")
	s.Configure("openai", "", "test-model", srv.URL, false, true)

	out, err := s.StreamText(context.Background(), "sys", "user", nil)
	if err != nil || out != "tek atış" {
		t.Fatalf("out=%q err=%v; want the one-shot body parsed", out, err)
	}
	if nReqs != 1 {
		t.Fatalf("nReqs = %d; a 200+JSON answer must NOT trigger a second billed call", nReqs)
	}
	if !s.streamKnownUnsupported("openai", srv.URL, "test-model") {
		t.Fatal("200+JSON is deterministic — verdict must be cached")
	}
}

func TestStreamTextOpenAIImmediateEOFFallsBackOnce(t *testing.T) {
	// SSE headers but the body dies before ANY event — first-byte
	// failure: one buffered retry, verdict NOT cached (could be
	// transient).
	var nStream, nBuffered int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if requestWantsStream(t, b) {
			nStream++
			w.Header().Set("Content-Type", "text/event-stream")
			return // immediate EOF, zero events
		}
		nBuffered++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"kurtarıldı"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	s := New("openai", "", "test-model")
	s.Configure("openai", "", "test-model", srv.URL, false, true)

	out, err := s.StreamText(context.Background(), "sys", "user", nil)
	if err != nil || out != "kurtarıldı" {
		t.Fatalf("out=%q err=%v; want the buffered rescue", out, err)
	}
	if nStream != 1 || nBuffered != 1 {
		t.Fatalf("requests stream=%d buffered=%d; want 1+1", nStream, nBuffered)
	}
	if s.streamKnownUnsupported("openai", srv.URL, "test-model") {
		t.Fatal("immediate EOF is ambiguous/transient — verdict must NOT be cached")
	}
	// Next call re-probes (no cached verdict).
	if _, err := s.StreamText(context.Background(), "sys", "user", nil); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if nStream != 2 {
		t.Fatalf("second call must re-probe the stream; nStream=%d", nStream)
	}
}

// ─── Anthropic policy (fixed host ⇒ injected transport) ─────────────

// anthropicStreamRT — stream:true isteğine ne döneceğini vakadan vakaya
// değiştiren taşıma. Anthropic ucu SABİT olduğu için httptest yerine
// s.cli'ye enjekte ediliyor (parityRT ile aynı teknik).
type anthropicStreamRT struct {
	streamStatus int    // stream:true isteğine dönülecek statü (0 = 200)
	streamCT     string // ve content-type
	streamBody   string
	nStream      int
	nBuffered    int
}

func (a *anthropicStreamRT) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(req.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	wantsStream, _ := m["stream"].(bool)
	resp := func(status int, ct, body string) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{ct}},
			Body:       io.NopCloser(bytes.NewReader([]byte(body))),
			Request:    req,
		}, nil
	}
	if wantsStream {
		a.nStream++
		st := a.streamStatus
		if st == 0 {
			st = 200
		}
		return resp(st, a.streamCT, a.streamBody)
	}
	a.nBuffered++
	return resp(200, "application/json",
		`{"content":[{"type":"text","text":"buffered cevap"}],"usage":{"input_tokens":3,"output_tokens":2}}`)
}

func newAnthropicService(t *testing.T, rt http.RoundTripper) *Service {
	t.Helper()
	s := New("anthropic", "sk-ant", "claude-x")
	s.Configure("anthropic", "sk-ant", "claude-x", "", false, true)
	s.mu.Lock()
	s.cli = &http.Client{Transport: rt}
	s.mu.Unlock()
	return s
}

func TestStreamTextAnthropicSSE(t *testing.T) {
	rt := &anthropicStreamRT{streamCT: "text/event-stream", streamBody: strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":25}}}`,
		`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"gizli"}}`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Sorun "}}`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"redis."}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":15}}`,
		"",
	}, "\n\n")}
	s := newAnthropicService(t, rt)

	var deltas []string
	out, err := s.StreamText(context.Background(), "sys", "user", func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("StreamText: %v", err)
	}
	if out != "Sorun redis." || strings.Join(deltas, "|") != "Sorun |redis." {
		t.Fatalf("out=%q deltas=%v; düşünce bloğu akmamalı, metin akmalı", out, deltas)
	}
	if rt.nStream != 1 || rt.nBuffered != 0 {
		t.Fatalf("istek sayısı stream=%d buffered=%d; want 1+0", rt.nStream, rt.nBuffered)
	}
}

func TestStreamTextAnthropicFallbackOn400CachesVerdict(t *testing.T) {
	rt := &anthropicStreamRT{streamStatus: 400, streamCT: "application/json",
		streamBody: `{"type":"error","error":{"message":"stream unsupported"}}`}
	s := newAnthropicService(t, rt)

	out, err := s.StreamText(context.Background(), "sys", "user", nil)
	if err != nil || out != "buffered cevap" {
		t.Fatalf("out=%q err=%v; want the buffered rescue", out, err)
	}
	if rt.nStream != 1 || rt.nBuffered != 1 {
		t.Fatalf("istek sayısı stream=%d buffered=%d; want probe+retry", rt.nStream, rt.nBuffered)
	}
	// Anahtar boş baseURL ile yazılır (anthropic ucu sabittir).
	if !s.streamKnownUnsupported(ProviderAnthropic, "", "claude-x") {
		t.Fatal("400 kesin reddi — karar önbelleğe yazılmalıydı")
	}
	// İkinci çağrı yoklamayı ATLAR.
	if _, err := s.StreamText(context.Background(), "sys", "user", nil); err != nil {
		t.Fatalf("ikinci çağrı: %v", err)
	}
	if rt.nStream != 1 || rt.nBuffered != 2 {
		t.Fatalf("önbellekli karar yoklamayı atlamalı: stream=%d buffered=%d", rt.nStream, rt.nBuffered)
	}
}

func TestStreamTextAnthropic200JSONParsedOneShot(t *testing.T) {
	// Bayrağı yutup tek atış JSON dönen bir vekil: gövde ZATEN cevap.
	rt := &anthropicStreamRT{streamCT: "application/json",
		streamBody: `{"content":[{"type":"text","text":"tek atış"}],"usage":{"input_tokens":4,"output_tokens":2}}`}
	s := newAnthropicService(t, rt)

	out, err := s.StreamText(context.Background(), "sys", "user", nil)
	if err != nil || out != "tek atış" {
		t.Fatalf("out=%q err=%v; want the one-shot body parsed", out, err)
	}
	if rt.nStream != 1 || rt.nBuffered != 0 {
		t.Fatalf("200+JSON ikinci faturalı çağrı YAPMAMALI: stream=%d buffered=%d", rt.nStream, rt.nBuffered)
	}
	if !s.streamKnownUnsupported(ProviderAnthropic, "", "claude-x") {
		t.Fatal("200+JSON kesin — karar önbelleğe yazılmalıydı")
	}
}
