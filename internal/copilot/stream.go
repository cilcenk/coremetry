// stream.go (v0.8.404) — token streaming for the one-shot narration
// call, with a TRANSPARENT runtime fallback to the buffered path.
//
// StreamText is the streaming twin of Explain: same answer contract
// (full text + error return, one self-recorded ai_calls row), plus an
// onDelta callback fired per content chunk so the API layer can relay
// live tokens over its SSE stream. It covers the GUIDED chat path's
// single tool-less call — the clean streaming case.
//
// FAZ 1.3 (v0.9.1125) — bu dosya artık POLİTİKA. İstek gövdesi, SSE
// çözümlemesi ve yanıt-başı sınıflandırması internal/ai/provider'da
// (stream.go); burada kalan tek şey KARARLAR:
//   - bilinen-desteklemez uçta yoklamayı hiç yapma,
//   - taşımadan gelen *StreamFallbackError'a bakıp buffered ikize düş,
//   - kararı (provider,baseURL,model) anahtarıyla önbelleğe yaz,
//   - operatörün gördüğü log satırını yaz,
//   - ai_calls satırını + kota kesicisini işlet.
//
// Deliberately OUT of scope this slice:
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
	"context"
	"errors"
	"log"
	"strings"
	"time"

	// aiprov — tek transport. Takma ad, bu dosyadaki
	// `provider, model, baseURL := s.provider, …` yerel değişkeninin
	// paket adını gölgelememesi için.
	aiprov "github.com/cilcenk/coremetry/internal/ai/provider"
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
	if !s.activeFor(ctx, true) { // v0.10.175 — çözülen profilin kimliği (#1)
		return "", errors.New("AI copilot not available (disabled or not configured — open Settings → AI Copilot)")
	}
	provider, model, baseURL := s.profileIdentity(ctx) // v0.10.175

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
		out, inputTokens, outputTokens, err = s.explainGitHub(ctx, systemPrompt, userPrompt)
	default: // anthropic
		out, inputTokens, outputTokens, err = s.streamAnthropicWithUsage(ctx, systemPrompt, userPrompt, onDelta)
	}
	s.recordNarration(ctx, started, provider, model, baseURL, systemPrompt, userPrompt, out, inputTokens, outputTokens, err)
	s.noteProviderError(err) // v0.9.200 — kota devre-kesici (Explain ile aynı)
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

// ─── Fallback policy (the DECISION half) ────────────────────────────

// streamFallback turns a transport-side "no stream" report into an
// answer. ONE writing for both providers on purpose: this whole phase
// exists because the same decision written twice drifts (the 1024
// budget lived in three builders for ~1000 releases).
//
// parseOneShot is only reached on VerdictParseBuffered, where the
// server answered the stream request with a complete one-shot body:
// that body IS the completion, so it is parsed instead of paying for a
// second call (v0.8.404).
func (s *Service) streamFallback(fe *aiprov.StreamFallbackError, provider, baseURL, model string,
	parseOneShot func([]byte) (string, uint32, uint32, error),
	buffered func() (string, uint32, uint32, error)) (string, uint32, uint32, error) {

	switch fe.Stage {
	case aiprov.StageConnect:
		log.Printf("[copilot] stream unsupported, buffered fallback (%s connect: %v)", fe.Provider, fe.Err)
		return buffered()
	case aiprov.StageEmptyStream:
		log.Printf("[copilot] stream unsupported, buffered fallback (%s empty stream, read err: %v)", fe.Provider, fe.Err)
		return buffered()
	}
	switch fe.Verdict {
	case aiprov.VerdictParseBuffered:
		s.markStreamUnsupported(provider, baseURL, model)
		log.Printf("[copilot] stream unsupported, buffered fallback (%s %d %s — parsing one-shot body, verdict cached)",
			fe.Provider, fe.Status, fe.ContentType)
		return parseOneShot(fe.Body)
	case aiprov.VerdictFallbackCache:
		s.markStreamUnsupported(provider, baseURL, model)
		log.Printf("[copilot] stream unsupported, buffered fallback (%s %d: %.200s — verdict cached)",
			fe.Provider, fe.Status, strings.TrimSpace(string(fe.Body)))
	default: // VerdictFallbackOnce — geçici, karar YAZILMAZ
		log.Printf("[copilot] stream unsupported, buffered fallback (%s %d transient: %.200s)",
			fe.Provider, fe.Status, strings.TrimSpace(string(fe.Body)))
	}
	return buffered()
}

// ─── OpenAI-compat streaming (policy shell) ─────────────────────────

func (s *Service) streamOpenAIWithUsage(ctx context.Context, systemPrompt, userPrompt string, onDelta func(string)) (string, uint32, uint32, error) {
	cfg, req, _, base, model := s.callSnapshot(ctx)
	req.System, req.User = systemPrompt, userPrompt
	// Verdict anahtarı VARSAYILANLARI UYGULANMIŞ hâli kullanır — aynı uç
	// bir çağrıda boş model, bir çağrıda "gpt-4o-mini" olarak
	// anahtarlanırsa karar kaybolur (explainOpenAI ile aynı gerekçe).
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	if s.streamKnownUnsupported(ProviderOpenAI, base, model) {
		// Known-unsupported endpoint: no re-probe, straight buffered.
		return s.explainOpenAI(ctx, systemPrompt, userPrompt)
	}

	resp, err := aiprov.StreamOpenAI(ctx, cfg, req, onDelta)
	var fe *aiprov.StreamFallbackError
	if errors.As(err, &fe) {
		return s.streamFallback(fe, ProviderOpenAI, base, model, parseBufferedOpenAI,
			func() (string, uint32, uint32, error) {
				return s.explainOpenAI(ctx, systemPrompt, userPrompt)
			})
	}
	// Mid-stream hatası ve boş-cevap hatası token sayılarını TAŞIR
	// (başarısız çağrı da faturalıdır ve /ai satırı maliyeti gösterir).
	return resp.Text, clampTokens(resp.InputTokens), clampTokens(resp.OutputTokens), err
}

// ─── Anthropic streaming (policy shell) ─────────────────────────────

func (s *Service) streamAnthropicWithUsage(ctx context.Context, systemPrompt, userPrompt string, onDelta func(string)) (string, uint32, uint32, error) {
	cfg, req, _, _, model := s.callSnapshot(ctx)
	req.System, req.User = systemPrompt, userPrompt
	if model == "" {
		model = aiprov.DefaultAnthropicModel // v0.10.253 — tek kaynak (D5)
	}
	// baseURL is not consulted by the anthropic provider (fixed API
	// host) — key on the empty string so the verdict stays coherent.
	if s.streamKnownUnsupported(ProviderAnthropic, "", model) {
		return s.explainAnthropic(ctx, systemPrompt, userPrompt)
	}

	resp, err := aiprov.StreamAnthropic(ctx, cfg, req, onDelta)
	var fe *aiprov.StreamFallbackError
	if errors.As(err, &fe) {
		return s.streamFallback(fe, ProviderAnthropic, "", model, parseBufferedAnthropic,
			func() (string, uint32, uint32, error) {
				return s.explainAnthropic(ctx, systemPrompt, userPrompt)
			})
	}
	return resp.Text, clampTokens(resp.InputTokens), clampTokens(resp.OutputTokens), err
}
