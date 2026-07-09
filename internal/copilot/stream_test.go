package copilot

import (
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
// code adapts, never assumes). These tests pin:
//   1. the pure SSE chunk parser (content deltas, the vLLM
//      reasoning-delta shape, inline <think> gating, [DONE], malformed
//      line skip, usage extraction from the final chunk),
//   2. the fallback decision table (status / content-type cases),
//   3. the per-(provider,baseURL,model) verdict cache incl. its
//      reset-on-Configure contract,
//   4. StreamText end-to-end against httptest servers: real SSE
//      streaming, a 400-on-stream:true endpoint (buffered retry ONCE +
//      cached verdict), and a 200+JSON endpoint that ignores the flag
//      (body parsed one-shot, no double-billed retry).

// feedAll pushes raw SSE lines through the accumulator, collecting the
// live deltas exactly as StreamText's scan loop would.
func feedAll(a *openAIStreamAccum, lines []string) []string {
	var deltas []string
	for _, l := range lines {
		if d := a.feed(l); d != "" {
			deltas = append(deltas, d)
		}
	}
	return deltas
}

func TestOpenAIStreamAccumContentDeltas(t *testing.T) {
	a := &openAIStreamAccum{}
	deltas := feedAll(a, []string{
		`event: chunk`, // framing line — ignored
		``,             // blank separator — ignored
		`: keepalive comment`,
		`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
		`data: {"choices":[{"delta":{"content":"Hel"}}]}`,
		`data: not-json at all`, // malformed — skipped, never aborts
		`data: {"choices":[{"delta":{"content":"lo "}}]}`,
		`data: {"choices":[{"delta":{"content":"world"},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":42,"completion_tokens":7}}`, // include_usage final chunk
		`data: [DONE]`,
	})
	if got := strings.Join(deltas, "|"); got != "Hel|lo |world" {
		t.Fatalf("deltas = %q; want Hel|lo |world", got)
	}
	if !a.done || !a.sawData {
		t.Fatalf("done=%v sawData=%v; want both true", a.done, a.sawData)
	}
	if a.inTokens != 42 || a.outTokens != 7 {
		t.Fatalf("usage = (%d,%d); want (42,7) from the final chunk", a.inTokens, a.outTokens)
	}
	final, trailing, err := a.finishOpenAI()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if final != "Hello world" || trailing != "" {
		t.Fatalf("final=%q trailing=%q; want \"Hello world\" and no trailing (content streamed live)", final, trailing)
	}
}

func TestOpenAIStreamAccumReasoningOnlyStream(t *testing.T) {
	// The vLLM --reasoning-parser shape: every token in
	// delta.reasoning_content (or delta.reasoning), content never set.
	// NOTHING streams live; finish emits the salvaged answer as ONE
	// trailing delta — the v0.8.384 fallback, streamed.
	for _, field := range []string{"reasoning_content", "reasoning"} {
		t.Run(field, func(t *testing.T) {
			a := &openAIStreamAccum{}
			deltas := feedAll(a, []string{
				fmt.Sprintf(`data: {"choices":[{"delta":{"%s":"Merhaba! "}}]}`, field),
				fmt.Sprintf(`data: {"choices":[{"delta":{"%s":"Sorun payment-service."}}]}`, field),
				`data: [DONE]`,
			})
			if len(deltas) != 0 {
				t.Fatalf("reasoning must buffer silently; streamed %q", deltas)
			}
			final, trailing, err := a.finishOpenAI()
			if err != nil {
				t.Fatalf("finish: %v", err)
			}
			want := "Merhaba! Sorun payment-service."
			if final != want || trailing != want {
				t.Fatalf("final=%q trailing=%q; want both %q (one final delta)", final, trailing, want)
			}
		})
	}
}

func TestOpenAIStreamAccumInlineThinkGate(t *testing.T) {
	// A reasoning model WITHOUT a server-side parser inlines
	// <think>…</think> in content — the chain-of-thought must not
	// stream, the post-think answer must.
	a := &openAIStreamAccum{}
	deltas := feedAll(a, []string{
		`data: {"choices":[{"delta":{"content":"<th"}}]}`, // ambiguous prefix — held
		`data: {"choices":[{"delta":{"content":"ink>let me ponder"}}]}`,
		`data: {"choices":[{"delta":{"content":" the trace</think>The "}}]}`,
		`data: {"choices":[{"delta":{"content":"answer."}}]}`,
		`data: [DONE]`,
	})
	if got := strings.Join(deltas, "|"); got != "The |answer." {
		t.Fatalf("deltas = %q; want the post-think tail only", got)
	}
	final, trailing, err := a.finishOpenAI()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if final != "The answer." || trailing != "" {
		t.Fatalf("final=%q trailing=%q; want \"The answer.\" with no trailing", final, trailing)
	}
}

func TestOpenAIStreamAccumNonThinkPrefixFlushes(t *testing.T) {
	// "<p..." disambiguates as NOT <think> — the held prefix flushes.
	a := &openAIStreamAccum{}
	deltas := feedAll(a, []string{
		`data: {"choices":[{"delta":{"content":"<"}}]}`, // held (could become <think>)
		`data: {"choices":[{"delta":{"content":"p99 rose"}}]}`,
		`data: [DONE]`,
	})
	if got := strings.Join(deltas, "|"); got != "<p99 rose" {
		t.Fatalf("deltas = %q; want the flushed \"<p99 rose\"", got)
	}
}

func TestOpenAIStreamAccumThinkOnlySalvage(t *testing.T) {
	// Only a think block, no tail → nothing streams; finish salvages
	// the inside-think text as the one trailing delta.
	a := &openAIStreamAccum{}
	deltas := feedAll(a, []string{
		`data: {"choices":[{"delta":{"content":"<think>The checkout span holds an Oracle row lock."}}]}`,
		`data: {"choices":[{"delta":{"content":"</think>"}}]}`,
		`data: [DONE]`,
	})
	if len(deltas) != 0 {
		t.Fatalf("think-only content must not stream; got %q", deltas)
	}
	final, trailing, err := a.finishOpenAI()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	want := "The checkout span holds an Oracle row lock."
	if final != want || trailing != want {
		t.Fatalf("final=%q trailing=%q; want the salvaged reasoning as one delta", final, trailing)
	}
}

func TestOpenAIStreamAccumLengthBudgetError(t *testing.T) {
	a := &openAIStreamAccum{}
	feedAll(a, []string{
		`data: {"choices":[{"delta":{"reasoning_content":""},"finish_reason":"length"}]}`,
		`data: [DONE]`,
	})
	_, _, err := a.finishOpenAI()
	if err == nil {
		t.Fatal("expected the token-budget error for an empty length-terminated stream")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "budget") && !strings.Contains(err.Error(), "max_tokens") {
		t.Fatalf("error %q should mention the token budget / max_tokens", err.Error())
	}
}

func TestAnthropicStreamAccum(t *testing.T) {
	a := &anthropicStreamAccum{}
	var deltas []string
	for _, l := range []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":25}}}`,
		`data: {"type":"ping"}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`, // buffered
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Sorun "}}`,
		`data: not json`, // skipped
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"redis."}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}`,
		`data: {"type":"message_stop"}`,
	} {
		if d := a.feed(l); d != "" {
			deltas = append(deltas, d)
		}
	}
	if got := strings.Join(deltas, "|"); got != "Sorun |redis." {
		t.Fatalf("deltas = %q; want text_delta content only (thinking buffered)", got)
	}
	if a.inTokens != 25 || a.outTokens != 15 {
		t.Fatalf("usage = (%d,%d); want (25,15)", a.inTokens, a.outTokens)
	}
	final, trailing, err := a.finishAnthropic()
	if err != nil || final != "Sorun redis." || trailing != "" {
		t.Fatalf("finish = (%q,%q,%v); want (\"Sorun redis.\",\"\",nil)", final, trailing, err)
	}
}

func TestAnthropicStreamAccumErrorEvent(t *testing.T) {
	a := &anthropicStreamAccum{}
	a.feed(`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	if _, _, err := a.finishAnthropic(); err == nil || !strings.Contains(err.Error(), "Overloaded") {
		t.Fatalf("err = %v; want the stream error surfaced", err)
	}
}

// ─── Fallback decision table ────────────────────────────────────────

func TestClassifyStreamResponse(t *testing.T) {
	cases := []struct {
		name   string
		status int
		ct     string
		want   streamVerdict
	}{
		{"200 SSE streams", 200, "text/event-stream", verdictStream},
		{"200 SSE with charset streams", 200, "text/event-stream; charset=utf-8", verdictStream},
		{"200 JSON = server ignored stream:true, parse one-shot", 200, "application/json", verdictParseBuffered},
		{"200 no content-type = parse one-shot", 200, "", verdictParseBuffered},
		{"400 = deterministic rejection, cache", 400, "application/json", verdictFallbackCache},
		{"404 = wrong route, cache", 404, "text/plain", verdictFallbackCache},
		{"405 = method rejected, cache", 405, "", verdictFallbackCache},
		{"415 = media type rejected, cache", 415, "", verdictFallbackCache},
		{"422 = body rejected, cache", 422, "application/json", verdictFallbackCache},
		{"501 = not implemented, cache", 501, "", verdictFallbackCache},
		{"401 auth = fallback once, never cache", 401, "application/json", verdictFallbackOnce},
		{"403 = fallback once, never cache", 403, "", verdictFallbackOnce},
		{"429 quota (Gemini) = fallback once, never cache", 429, "application/json", verdictFallbackOnce},
		{"500 = transient, fallback once", 500, "", verdictFallbackOnce},
		{"503 = transient, fallback once", 503, "text/html", verdictFallbackOnce},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyStreamResponse(c.status, c.ct); got != c.want {
				t.Fatalf("classifyStreamResponse(%d, %q) = %v; want %v", c.status, c.ct, got, c.want)
			}
		})
	}
}

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
