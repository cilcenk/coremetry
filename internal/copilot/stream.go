// stream.go (v0.8.404) — token streaming for the one-shot narration
// call, with a TRANSPARENT runtime fallback to the buffered path.
//
// StreamText is the streaming twin of Explain: same answer contract
// (full text + error return, one self-recorded ai_calls row), plus an
// onDelta callback fired per content chunk so the API layer can relay
// live tokens over its SSE stream. It covers the GUIDED chat path's
// single tool-less call — the clean streaming case.
//
// Deliberately OUT of scope this slice:
//   - The free tool loop (ChatWithTools) stays buffered. Tool-call
//     streaming is a different beast: deltas interleave partial
//     tool_call JSON fragments that must be reassembled per index
//     before anything is executable, the answer text may arrive in
//     multiple assistant turns, and provider extras (Gemini
//     thought_signature, v0.8.373) ride on the reassembled call. None
//     of that buys the operator visible latency wins — the loop's time
//     goes to tool execution rounds, not narration.
//   - GitHub Copilot: the session-token exchange + integration-header
//     dance has no verified streaming contract; it uses the buffered
//     call (zero deltas — the caller's final answer event still lands).
//
// FALLBACK (the critical part — vLLM stream support is UNVERIFIED on
// the primary target, so the code adapts instead of assuming): when
// the stream:true request fails at CONNECT/first-byte — non-200,
// non-SSE content-type, immediate EOF before any event, JSON error
// body — we transparently retry ONCE with the existing buffered call
// and log "[copilot] stream unsupported, buffered fallback". A
// deterministic rejection additionally caches an "unsupported" verdict
// per (provider,baseURL,model) so subsequent guided calls skip the
// probe; Configure resets the cache. Mid-stream failures (after data
// has flowed) do NOT fall back — deltas already reached the client.
package copilot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// StreamText runs a single system+user narration call, streaming
// answer tokens through onDelta as they arrive. Returns the FULL final
// text — identical to what Explain would have returned — so the caller
// keeps its existing "answer is the source of truth" contract; the
// deltas are a pure progressive-rendering bonus. onDelta may be nil.
// Reasoning output (delta.reasoning_content / delta.reasoning /
// inline <think> blocks / Anthropic thinking_delta) is buffered
// silently and never streamed; if the model emits ONLY reasoning, the
// salvaged answer (v0.8.384 chain) is emitted as one final delta.
func (s *Service) StreamText(ctx context.Context, systemPrompt, userPrompt string, onDelta func(string)) (string, error) {
	if !s.Active() {
		return "", errors.New("AI copilot not available (disabled or not configured — open Settings → AI Copilot)")
	}
	s.mu.RLock()
	provider, model, baseURL := s.provider, s.model, s.baseURL
	s.mu.RUnlock()

	started := time.Now()
	var (
		out          string
		err          error
		inputTokens  uint32
		outputTokens uint32
	)
	switch provider {
	case ProviderOpenAI:
		out, inputTokens, outputTokens, err = s.streamOpenAIWithUsage(ctx, systemPrompt, userPrompt, onDelta)
	case ProviderGitHub:
		// No streaming twin this slice (see the package comment) —
		// buffered call, zero deltas, same answer contract.
		out, inputTokens, outputTokens, err = s.explainGitHubWithUsage(ctx, systemPrompt, userPrompt)
	default: // anthropic
		out, inputTokens, outputTokens, err = s.streamAnthropicWithUsage(ctx, systemPrompt, userPrompt, onDelta)
	}
	s.recordNarration(ctx, started, provider, model, baseURL, systemPrompt, userPrompt, out, inputTokens, outputTokens, err)
	return out, err
}

// ─── Streaming-support verdict cache ────────────────────────────────

// streamVerdictKey — the cache key hashes ALL inputs that select an
// endpoint behaviour (provider + baseURL + model), never a subset:
// the same vLLM base can host a streamable and a non-streamable model.
func streamVerdictKey(provider, baseURL, model string) string {
	return provider + "\x00" + baseURL + "\x00" + model
}

func (s *Service) streamKnownUnsupported(provider, baseURL, model string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.streamUnsupported[streamVerdictKey(provider, baseURL, model)]
}

func (s *Service) markStreamUnsupported(provider, baseURL, model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streamUnsupported == nil {
		s.streamUnsupported = map[string]bool{}
	}
	s.streamUnsupported[streamVerdictKey(provider, baseURL, model)] = true
}

// ─── Fallback decision table (pure, table-tested) ───────────────────

type streamVerdict int

const (
	// verdictStream — 200 + text/event-stream: consume the SSE stream.
	verdictStream streamVerdict = iota
	// verdictParseBuffered — 200 + non-SSE body: the server ignored
	// stream:true and answered one-shot. The body IS the completion —
	// parse it directly (no double-billed retry) and cache unsupported.
	verdictParseBuffered
	// verdictFallbackCache — deterministic rejection of the stream
	// flag (some vLLM builds 400 on stream:true): buffered retry ONCE
	// and cache the unsupported verdict.
	verdictFallbackCache
	// verdictFallbackOnce — transient or non-stream-specific failure
	// (429 quota, 5xx, auth): buffered retry ONCE but do NOT cache —
	// the endpoint may well support streaming when it recovers.
	verdictFallbackOnce
)

// classifyStreamResponse maps the response HEAD of a stream:true probe
// to a verdict. Only statuses that unambiguously mean "this request
// shape is not accepted" cache the unsupported verdict; everything
// transient falls back for THIS call only and re-probes next time.
func classifyStreamResponse(status int, contentType string) streamVerdict {
	if status >= 200 && status < 300 {
		if strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
			return verdictStream
		}
		return verdictParseBuffered
	}
	switch status {
	case 400, 404, 405, 415, 422, 501:
		return verdictFallbackCache
	}
	return verdictFallbackOnce
}

// ─── SSE line plumbing ──────────────────────────────────────────────

// sseDataPayload extracts the payload of an SSE "data:" line. Framing
// lines (blank, "event:", "id:", ":" comments) return ok=false.
func sseDataPayload(line string) (string, bool) {
	line = strings.TrimSuffix(line, "\r")
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	return strings.TrimSpace(line[len("data:"):]), true
}

// scanSSE feeds an SSE body line-by-line into fn. Returns the read
// error (nil on clean EOF). 1MB line cap — individual SSE chunks are
// a few tokens; anything bigger is a broken server.
func scanSSE(r io.Reader, fn func(string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		fn(sc.Text())
	}
	return sc.Err()
}

// ─── OpenAI-compat stream accumulator (pure, table-tested) ──────────

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// openAIStreamAccum accumulates one OpenAI-compat SSE stream.
// feed() consumes raw lines and returns the content delta to emit
// ("" = nothing to stream: framing, reasoning, held think-block).
type openAIStreamAccum struct {
	content   strings.Builder // ALL raw content deltas, for the final salvage
	reasoning strings.Builder // delta.reasoning_content / delta.reasoning — buffered, never streamed
	gateBuf   strings.Builder // held content while the inline-<think> prefix is undecided
	gateOpen  bool            // content cleared for live emission
	inThink   bool            // buffering an inline <think>…</think> block
	emitted   bool            // at least one delta was handed out
	sawData   bool            // at least one parseable data event (first-byte-failure detector)
	done      bool            // saw [DONE]
	finish    string          // last non-empty finish_reason
	inTokens  uint32          // usage from the final chunk, when present
	outTokens uint32
}

// feed parses one SSE line. Malformed data lines are skipped — a
// stream must never abort on one bad chunk.
func (a *openAIStreamAccum) feed(line string) string {
	payload, ok := sseDataPayload(line)
	if !ok {
		return ""
	}
	if payload == "[DONE]" {
		a.done = true
		return ""
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"` // vLLM --reasoning-parser shape
				Reasoning        string `json:"reasoning"`         // v0.8.384 gateway shape
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     uint32 `json:"prompt_tokens"`
			CompletionTokens uint32 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return "" // malformed line — skip
	}
	a.sawData = true
	if chunk.Usage != nil {
		if chunk.Usage.PromptTokens > 0 {
			a.inTokens = chunk.Usage.PromptTokens
		}
		if chunk.Usage.CompletionTokens > 0 {
			a.outTokens = chunk.Usage.CompletionTokens
		}
	}
	if len(chunk.Choices) == 0 {
		return "" // usage-only final chunk (stream_options.include_usage)
	}
	c := chunk.Choices[0]
	if c.FinishReason != "" {
		a.finish = c.FinishReason
	}
	if c.Delta.ReasoningContent != "" {
		a.reasoning.WriteString(c.Delta.ReasoningContent)
	}
	if c.Delta.Reasoning != "" {
		a.reasoning.WriteString(c.Delta.Reasoning)
	}
	if c.Delta.Content == "" {
		return ""
	}
	a.content.WriteString(c.Delta.Content)
	return a.gate(c.Delta.Content)
}

// gate suppresses a LEADING inline <think>…</think> block from the
// live delta stream (a vLLM without --reasoning-parser inlines the
// chain-of-thought in content). Content is held until the prefix is
// disambiguated: not-a-think-block → flush and stream everything
// after; think-block → hold silently until the close tag, then stream
// the tail. The held/raw content is still in a.content, so the final
// answer salvage sees it either way.
func (a *openAIStreamAccum) gate(d string) string {
	if a.gateOpen {
		a.emitted = true
		return d
	}
	a.gateBuf.WriteString(d)
	buf := a.gateBuf.String()
	if !a.inThink {
		trimmed := strings.TrimLeft(buf, " \t\r\n")
		switch {
		case trimmed == "":
			return "" // whitespace only so far — keep holding
		case len(trimmed) < len(thinkOpen) && strings.HasPrefix(thinkOpen, trimmed):
			return "" // could still become "<think>" — keep holding
		case !strings.HasPrefix(trimmed, thinkOpen):
			a.gateOpen = true
			a.gateBuf.Reset()
			a.emitted = true
			return buf // not a think block — flush everything held
		}
		a.inThink = true
	}
	if i := strings.Index(buf, thinkClose); i >= 0 {
		a.gateOpen, a.inThink = true, false
		a.gateBuf.Reset()
		after := buf[i+len(thinkClose):]
		if after != "" {
			a.emitted = true
		}
		return after
	}
	return "" // still inside the think block
}

// finishOpenAI resolves the final answer via the v0.8.384 salvage
// chain (content after </think> → reasoning fields → inside-think
// text) and the trailing delta still owed to the client — non-empty
// exactly when NOTHING streamed live (reasoning-only stream, or a
// think block with no tail): the whole salvaged answer goes out as
// one final delta.
func (a *openAIStreamAccum) finishOpenAI() (final, trailing string, err error) {
	final = stripThinking(a.content.String())
	if final == "" {
		final = stripThinking(a.reasoning.String())
	}
	if final == "" {
		final = thinkingContent(a.content.String())
	}
	if final == "" {
		if a.finish == "length" {
			return "", "", errors.New("model returned no answer — token budget exhausted by reasoning; raise max_tokens or disable thinking (e.g. Qwen3 /no_think)")
		}
		return "", "", errors.New("openai-compat stream: model returned empty content — no answer in content/reasoning")
	}
	if !a.emitted {
		trailing = final
	}
	return final, trailing, nil
}

// ─── Anthropic stream accumulator (pure, table-tested) ──────────────

// anthropicStreamAccum accumulates a Messages stream. Anthropic tags
// every data payload with a "type" field, so event: lines are not
// needed for dispatch. text_delta streams; thinking_delta buffers.
type anthropicStreamAccum struct {
	content   strings.Builder
	reasoning strings.Builder // thinking_delta — buffered, never streamed
	emitted   bool
	sawData   bool
	errMsg    string // error event payload
	inTokens  uint32
	outTokens uint32
}

func (a *anthropicStreamAccum) feed(line string) string {
	payload, ok := sseDataPayload(line)
	if !ok {
		return ""
	}
	var ev struct {
		Type    string `json:"type"`
		Message *struct {
			Usage struct {
				InputTokens uint32 `json:"input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Delta *struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"delta"`
		Usage *struct {
			OutputTokens uint32 `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		return "" // malformed line — skip
	}
	a.sawData = true
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			a.inTokens = ev.Message.Usage.InputTokens
		}
	case "content_block_delta":
		if ev.Delta == nil {
			return ""
		}
		switch ev.Delta.Type {
		case "text_delta":
			if ev.Delta.Text != "" {
				a.content.WriteString(ev.Delta.Text)
				a.emitted = true
				return ev.Delta.Text
			}
		case "thinking_delta":
			a.reasoning.WriteString(ev.Delta.Thinking)
		}
	case "message_delta":
		if ev.Usage != nil {
			a.outTokens = ev.Usage.OutputTokens
		}
	case "error":
		if ev.Error != nil {
			a.errMsg = ev.Error.Message
		}
	}
	return ""
}

func (a *anthropicStreamAccum) finishAnthropic() (final, trailing string, err error) {
	if a.errMsg != "" {
		return "", "", fmt.Errorf("anthropic stream error: %s", a.errMsg)
	}
	final = strings.TrimSpace(a.content.String())
	if final == "" {
		// Thinking-only stream — same salvage posture as openai-compat.
		final = stripThinking(strings.TrimSpace(a.reasoning.String()))
	}
	if final == "" {
		return "", "", errors.New("anthropic stream: empty response")
	}
	if !a.emitted {
		trailing = final
	}
	return final, trailing, nil
}

// ─── OpenAI-compat streaming call ───────────────────────────────────

func (s *Service) streamOpenAIWithUsage(ctx context.Context, systemPrompt, userPrompt string, onDelta func(string)) (string, uint32, uint32, error) {
	s.mu.RLock()
	apiKey, model, base := s.apiKey, s.model, s.baseURL
	s.mu.RUnlock()
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	if s.streamKnownUnsupported(ProviderOpenAI, base, model) {
		// Known-unsupported endpoint: no re-probe, straight buffered.
		return s.explainOpenAIWithUsage(ctx, systemPrompt, userPrompt)
	}
	url := strings.TrimRight(base, "/") + "/chat/completions"
	body := map[string]any{
		"model":       model,
		"max_tokens":  openAICompletionTokens,
		"temperature": 0.2,
		"stream":      true,
		// Usage arrives in the final chunk. vLLM + OpenAI + Gemini's
		// compat layer honour include_usage; a server that rejects it
		// lands in the same 400→buffered fallback as one rejecting
		// stream:true — answers stay correct either way.
		"stream_options": map[string]any{"include_usage": true},
		"messages": []map[string]any{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("api-key", apiKey) // v0.8.384 gateway shape
	}
	resp, err := s.cli.Do(req)
	if err != nil {
		// CONNECT failure. Retry once buffered (a truly dead endpoint
		// fails there too and surfaces normally); not cached — could
		// be transient.
		log.Printf("[copilot] stream unsupported, buffered fallback (openai-compat connect: %v)", err)
		return s.explainOpenAIWithUsage(ctx, systemPrompt, userPrompt)
	}
	defer resp.Body.Close()

	switch classifyStreamResponse(resp.StatusCode, resp.Header.Get("Content-Type")) {
	case verdictParseBuffered:
		s.markStreamUnsupported(ProviderOpenAI, base, model)
		log.Printf("[copilot] stream unsupported, buffered fallback (openai-compat 200 %s — parsing one-shot body, verdict cached)", resp.Header.Get("Content-Type"))
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return parseOpenAIChatResponse(respBody)
	case verdictFallbackCache:
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		s.markStreamUnsupported(ProviderOpenAI, base, model)
		log.Printf("[copilot] stream unsupported, buffered fallback (openai-compat %d: %.200s — verdict cached)", resp.StatusCode, strings.TrimSpace(string(respBody)))
		return s.explainOpenAIWithUsage(ctx, systemPrompt, userPrompt)
	case verdictFallbackOnce:
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		log.Printf("[copilot] stream unsupported, buffered fallback (openai-compat %d transient: %.200s)", resp.StatusCode, strings.TrimSpace(string(respBody)))
		return s.explainOpenAIWithUsage(ctx, systemPrompt, userPrompt)
	}

	// verdictStream — consume it.
	acc := &openAIStreamAccum{}
	scanErr := scanSSE(resp.Body, func(line string) {
		if d := acc.feed(line); d != "" && onDelta != nil {
			onDelta(d)
		}
	})
	if !acc.sawData {
		// SSE headers but the body died before ANY event (immediate
		// EOF / instant error) — that's still first-byte territory:
		// one buffered retry, no verdict cached.
		log.Printf("[copilot] stream unsupported, buffered fallback (openai-compat empty stream, read err: %v)", scanErr)
		return s.explainOpenAIWithUsage(ctx, systemPrompt, userPrompt)
	}
	if scanErr != nil {
		// Mid-stream break AFTER data flowed — no fallback (deltas
		// already reached the client); surface the error.
		return "", acc.inTokens, acc.outTokens, fmt.Errorf("openai-compat stream read: %w", scanErr)
	}
	final, trailing, ferr := acc.finishOpenAI()
	if ferr != nil {
		return "", acc.inTokens, acc.outTokens, ferr
	}
	if trailing != "" && onDelta != nil {
		onDelta(trailing)
	}
	return final, acc.inTokens, acc.outTokens, nil
}

// ─── Anthropic streaming call ───────────────────────────────────────

func (s *Service) streamAnthropicWithUsage(ctx context.Context, systemPrompt, userPrompt string, onDelta func(string)) (string, uint32, uint32, error) {
	s.mu.RLock()
	apiKey, model := s.apiKey, s.model
	s.mu.RUnlock()
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	// baseURL is not consulted by the anthropic provider (fixed API
	// host) — key on the empty string so the verdict stays coherent.
	if s.streamKnownUnsupported(ProviderAnthropic, "", model) {
		return s.explainAnthropicWithUsage(ctx, systemPrompt, userPrompt)
	}
	body := map[string]any{
		"model":      model,
		"max_tokens": 1024,
		"system":     systemPrompt,
		"stream":     true,
		"messages": []map[string]any{
			{"role": "user", "content": userPrompt},
		},
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.anthropic.com/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return "", 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Anthropic-Version", "2023-06-01")

	resp, err := s.cli.Do(req)
	if err != nil {
		log.Printf("[copilot] stream unsupported, buffered fallback (anthropic connect: %v)", err)
		return s.explainAnthropicWithUsage(ctx, systemPrompt, userPrompt)
	}
	defer resp.Body.Close()

	switch classifyStreamResponse(resp.StatusCode, resp.Header.Get("Content-Type")) {
	case verdictParseBuffered:
		s.markStreamUnsupported(ProviderAnthropic, "", model)
		log.Printf("[copilot] stream unsupported, buffered fallback (anthropic 200 %s — parsing one-shot body, verdict cached)", resp.Header.Get("Content-Type"))
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return parseAnthropicResponse(respBody)
	case verdictFallbackCache:
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		s.markStreamUnsupported(ProviderAnthropic, "", model)
		log.Printf("[copilot] stream unsupported, buffered fallback (anthropic %d: %.200s — verdict cached)", resp.StatusCode, strings.TrimSpace(string(respBody)))
		return s.explainAnthropicWithUsage(ctx, systemPrompt, userPrompt)
	case verdictFallbackOnce:
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		log.Printf("[copilot] stream unsupported, buffered fallback (anthropic %d transient: %.200s)", resp.StatusCode, strings.TrimSpace(string(respBody)))
		return s.explainAnthropicWithUsage(ctx, systemPrompt, userPrompt)
	}

	acc := &anthropicStreamAccum{}
	scanErr := scanSSE(resp.Body, func(line string) {
		if d := acc.feed(line); d != "" && onDelta != nil {
			onDelta(d)
		}
	})
	if !acc.sawData {
		log.Printf("[copilot] stream unsupported, buffered fallback (anthropic empty stream, read err: %v)", scanErr)
		return s.explainAnthropicWithUsage(ctx, systemPrompt, userPrompt)
	}
	if scanErr != nil {
		return "", acc.inTokens, acc.outTokens, fmt.Errorf("anthropic stream read: %w", scanErr)
	}
	final, trailing, ferr := acc.finishAnthropic()
	if ferr != nil {
		return "", acc.inTokens, acc.outTokens, ferr
	}
	if trailing != "" && onDelta != nil {
		onDelta(trailing)
	}
	return final, acc.inTokens, acc.outTokens, nil
}
