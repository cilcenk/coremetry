package copilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// v0.8.x — the OpenAI-compatible Explain path didn't handle local reasoning
// models (Qwen3, deepseek-r1, …): they put the answer in reasoning_content
// and/or inline a <think>…</think> block, and the fixed max_tokens=1024 budget
// often filled mid-thought (finish_reason "length", empty content). The empty
// explanation was then swallowed by the frontend's `{text && …}` guard, so the
// user saw neither answer nor error. These tests pin the fix end-to-end on the
// real explainOpenAIWithUsage (white-box, httptest-backed — New/Configure as in
// production, not mocked).

// newOpenAITestService wires a Service at an httptest server returning the given
// OpenAI-compatible JSON body, using the real constructor + Configure.
func newOpenAITestService(t *testing.T, responseBody string) (*Service, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
	s := New("openai", "", "test-model")
	s.Configure("openai", "", "test-model", srv.URL, false)
	return s, srv.Close
}

func TestStripThinking(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"no think block", "plain answer", "plain answer"},
		{"leading think stripped", "<think>reasoning here</think>the answer", "the answer"},
		{"surrounding whitespace trimmed", "  <think>x</think>   spaced   ", "spaced"},
		{"only thinking yields empty", "<think>all thinking</think>", ""},
		{"empty input", "", ""},
		{"keeps content after the final close", "<think>a</think>mid<think>b</think>final", "final"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripThinking(c.in); got != c.want {
				t.Fatalf("stripThinking(%q) = %q; want %q", c.in, got, c.want)
			}
		})
	}
}

func TestExplainOpenAIReasoningContentFallback(t *testing.T) {
	// content empty, answer lives in reasoning_content.
	body := `{"choices":[{"message":{"content":"","reasoning_content":"answer from reasoning field"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":7}}`
	s, done := newOpenAITestService(t, body)
	defer done()
	out, pt, ct, err := s.explainOpenAIWithUsage(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "answer from reasoning field" {
		t.Fatalf("out = %q; want the reasoning_content fallback", out)
	}
	if pt != 5 || ct != 7 {
		t.Fatalf("usage = (%d,%d); want (5,7)", pt, ct)
	}
}

func TestExplainOpenAIStripsThinkBlock(t *testing.T) {
	// content = "<think>…</think>answer" → just the answer.
	body := `{"choices":[{"message":{"content":"<think>pondering the trace</think>real answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4}}`
	s, done := newOpenAITestService(t, body)
	defer done()
	out, _, _, err := s.explainOpenAIWithUsage(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "real answer" {
		t.Fatalf("out = %q; want \"real answer\" (think block stripped)", out)
	}
}

func TestExplainOpenAILengthBudgetError(t *testing.T) {
	// content empty + finish_reason "length" → explanatory budget error.
	body := `{"choices":[{"message":{"content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":9,"completion_tokens":4096}}`
	s, done := newOpenAITestService(t, body)
	defer done()
	out, _, _, err := s.explainOpenAIWithUsage(context.Background(), "sys", "user")
	if err == nil {
		t.Fatalf("expected an error for empty content + finish_reason length; got out=%q", out)
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "budget") && !strings.Contains(msg, "max_tokens") {
		t.Fatalf("error %q should mention the token budget / max_tokens", err.Error())
	}
}

func TestExplainOpenAINormalContent(t *testing.T) {
	// plain content returned verbatim, usage parsed.
	body := `{"choices":[{"message":{"content":"plain answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":22}}`
	s, done := newOpenAITestService(t, body)
	defer done()
	out, pt, ct, err := s.explainOpenAIWithUsage(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "plain answer" {
		t.Fatalf("out = %q; want \"plain answer\"", out)
	}
	if pt != 11 || ct != 22 {
		t.Fatalf("usage = (%d,%d); want (11,22)", pt, ct)
	}
}

// v0.8.x — operator-reported: a local reasoning model returned "model returned
// empty content" 500s on both chat + explain-trace. Cause: the model emitted
// ONLY a <think> block (no post-</think> answer) or used the `reasoning` field.
// These pin the salvage so the answer is recovered instead of failing.
func TestExplainOpenAISalvagesThinkOnlyContent(t *testing.T) {
	// content = "<think>…the answer…</think>" with NOTHING after the close tag.
	body := `{"choices":[{"message":{"content":"<think>The checkout span is slow due to an Oracle row lock.</think>"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4}}`
	s, done := newOpenAITestService(t, body)
	defer done()
	out, _, _, err := s.explainOpenAIWithUsage(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "The checkout span is slow due to an Oracle row lock." {
		t.Fatalf("out = %q; want the salvaged reasoning text", out)
	}
}

func TestExplainOpenAIReasoningFieldFallback(t *testing.T) {
	// content empty, answer in the `reasoning` field (not reasoning_content).
	body := `{"choices":[{"message":{"content":"","reasoning":"answer from the reasoning field"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":7}}`
	s, done := newOpenAITestService(t, body)
	defer done()
	out, _, _, err := s.explainOpenAIWithUsage(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "answer from the reasoning field" {
		t.Fatalf("out = %q; want the reasoning-field fallback", out)
	}
}

func TestExplainOpenAITrulyEmptyStillErrors(t *testing.T) {
	// genuinely nothing anywhere → still a clear error (with the diagnostic hint).
	body := `{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":0}}`
	s, done := newOpenAITestService(t, body)
	defer done()
	out, _, _, err := s.explainOpenAIWithUsage(context.Background(), "sys", "user")
	if err == nil {
		t.Fatalf("expected an error for genuinely empty content; got out=%q", out)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "empty content") {
		t.Fatalf("error %q should mention empty content", err.Error())
	}
}
