// Package copilot wraps an LLM Messages/Chat API to produce
// natural-language explanations of telemetry artifacts — trace flame,
// open Problem, exception group.
//
// Three providers supported:
//   - "anthropic": Anthropic Messages API (api.anthropic.com).
//   - "github":    GitHub Copilot Chat (api.githubcopilot.com). The
//                  caller's API key is a GitHub OAuth token (`ghu_…`)
//                  which we exchange for a short-lived Copilot session
//                  token (cached + auto-refreshed).
//   - "openai":    Any OpenAI-compatible /v1/chat/completions endpoint.
//                  Drives self-hosted local LLMs (Ollama, LM Studio,
//                  vLLM, llama.cpp server, LocalAI, OpenWebUI) AND
//                  the real OpenAI API. Banks running Coremetry
//                  air-gapped want this so traces / problems never
//                  leave the perimeter for explanation. APIKey is
//                  optional for local endpoints that don't gate on
//                  it (Ollama default).
//
// The Service is configurable at runtime — admins can flip provider
// or rotate keys via the Settings UI without restarting Coremetry.
package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	ProviderAnthropic = "anthropic"
	ProviderGitHub    = "github"
	ProviderOpenAI    = "openai"
)

// Service is the small surface other packages call into.
//
// Internals are guarded by mu so PUT /api/settings/ai can swap creds
// while Explain calls are in flight.
type Service struct {
	mu       sync.RWMutex
	provider string
	apiKey   string
	model    string
	// baseURL is used by the "openai" provider for OpenAI-compatible
	// endpoints. Empty → default https://api.openai.com/v1 (real
	// OpenAI). Examples for self-hosted: http://ollama:11434/v1,
	// http://lmstudio:1234/v1, http://vllm:8000/v1.
	baseURL  string

	// GitHub session token cache. We exchange ghu_ → session token
	// once and reuse until ~30s before the server-stated expiry.
	ghSessTok string
	ghSessExp time.Time

	cli *http.Client

	// recorder is the AI-observability sink (v0.5.162). Set once at
	// startup via SetRecorder. Nil = recording disabled (tests, or
	// minimal binary). Recording runs on its own goroutine so user-
	// facing latency isn't impacted by ingest cost.
	recorder Recorder
}

// Recorder is the sink for the Coremetry-native AI observability
// pipeline. Implemented by a thin adapter around chstore.Store
// (kept in package api to avoid copilot→chstore import dependency).
// Every Explain call emits exactly one CallRecord regardless of
// success — errors show up in /ai with status="error" so the
// operator sees broken provider configs without grepping logs.
type Recorder interface {
	RecordCall(ctx context.Context, c CallRecord)
}

// CallRecord captures one LLM round-trip. CreatedAt is set by the
// Explain wrapper at call start; DurationMs measured at return.
// Token counts come from the provider response when available
// (OpenAI + Anthropic both ship usage data; some Ollama versions
// don't — those stay 0).
type CallRecord struct {
	CreatedAt      time.Time
	Surface        string
	Provider       string
	Model          string
	BaseURL        string
	DurationMs     uint32
	InputTokens    uint32
	OutputTokens   uint32
	Status         string
	ErrorMsg       string
	PromptChars    uint32
	ResponseChars  uint32
	UserID         string
	UserEmail      string
	PromptSample   string
	ResponseSample string
}

// metaKey is the unexported type for ctx.WithValue lookups so other
// packages can't accidentally collide.
type metaKey struct{}

// CallMeta is attribution data the API layer stashes in ctx before
// calling Explain — surface (which Copilot endpoint), userID/email
// for "who triggered this call" filtering on the /ai page.
type CallMeta struct {
	Surface   string
	UserID    string
	UserEmail string
}

// WithMeta returns ctx tagged with the given CallMeta. The api
// package's copilotExplain wrapper uses this to attribute every
// LLM call to the surface that produced it.
func WithMeta(ctx context.Context, m CallMeta) context.Context {
	return context.WithValue(ctx, metaKey{}, m)
}

// MetaFromContext is the read side. Returns the zero CallMeta when
// no tag is present so callers can treat it as "unknown".
func MetaFromContext(ctx context.Context) CallMeta {
	if v, ok := ctx.Value(metaKey{}).(CallMeta); ok {
		return v
	}
	return CallMeta{}
}

// SetRecorder wires the observability sink. Nil disables it. Safe
// to call before the Service is in use (single goroutine at boot).
func (s *Service) SetRecorder(r Recorder) {
	if s == nil {
		return
	}
	s.recorder = r
}

// New always returns a Service. When apiKey is empty Configured()
// reports false and callers branch off — that's the dormant state
// before the operator pastes a key in Settings.
func New(provider, apiKey, model string) *Service {
	if provider == "" {
		provider = ProviderAnthropic
	}
	return &Service{
		provider: provider,
		apiKey:   apiKey,
		model:    model,
		// Local LLMs (Ollama loading a 70B model, llama.cpp on CPU)
		// can take 60+ seconds for a first generation. Bump the
		// client timeout to match — the request itself bounded by
		// max_tokens still keeps the worst case under 2-3 minutes.
		cli: &http.Client{Timeout: 180 * time.Second},
	}
}

// Configure swaps live credentials. Used by PUT /api/settings/ai.
// Empty apiKey legitimately disables the feature — Configured() flips
// to false and the UI hides the buttons. baseURL is only consulted
// by the "openai" provider; ignored for anthropic/github so a stale
// value persisted from a previous selection doesn't leak.
func (s *Service) Configure(provider, apiKey, model, baseURL string) {
	if provider == "" {
		provider = ProviderAnthropic
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Provider or key changed → drop any cached GitHub session token.
	if s.provider != provider || s.apiKey != apiKey {
		s.ghSessTok, s.ghSessExp = "", time.Time{}
	}
	s.provider, s.apiKey, s.model, s.baseURL = provider, apiKey, model, baseURL
}

// Snapshot returns the current configuration. The apiKey is masked
// (only "set" / "unset" matters to the UI) — full key is never echoed.
// baseURL is non-secret (operators put it in their Helm values), so
// we echo it back so the Settings page can show what's wired up.
func (s *Service) Snapshot() (provider, model, baseURL string, hasKey bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.provider, s.model, s.baseURL, s.apiKey != ""
}

// Configured reports whether the service has credentials. The "openai"
// provider with an empty key is allowed when baseURL points at a
// local endpoint that doesn't gate on auth (Ollama default config) —
// the caller's request just goes through with no Authorization header.
func (s *Service) Configured() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.apiKey != "" {
		return true
	}
	// Local OpenAI-compat endpoints often run without auth — having
	// a base URL alone is enough.
	return s.provider == ProviderOpenAI && s.baseURL != ""
}

// Explain runs a single Messages/Chat call with the given system +
// user prompt. Branches on the configured provider. v0.5.162 wraps
// the dispatch with the AI-observability recorder so every call
// emits an ai_calls row regardless of success — recording happens
// on a goroutine so the user doesn't pay ingest cost in their
// request path.
func (s *Service) Explain(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if !s.Configured() {
		return "", errors.New("AI copilot not configured (open Settings → AI Copilot)")
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
	case ProviderGitHub:
		out, inputTokens, outputTokens, err = s.explainGitHubWithUsage(ctx, systemPrompt, userPrompt)
	case ProviderOpenAI:
		out, inputTokens, outputTokens, err = s.explainOpenAIWithUsage(ctx, systemPrompt, userPrompt)
	default:
		out, inputTokens, outputTokens, err = s.explainAnthropicWithUsage(ctx, systemPrompt, userPrompt)
	}

	if s.recorder != nil {
		meta := MetaFromContext(ctx)
		fullPrompt := systemPrompt + "\n\n" + userPrompt
		rec := CallRecord{
			CreatedAt:      started,
			Surface:        meta.Surface,
			Provider:       provider,
			Model:          model,
			BaseURL:        baseURL,
			DurationMs:     uint32(time.Since(started).Milliseconds()),
			InputTokens:    inputTokens,
			OutputTokens:   outputTokens,
			Status:         "ok",
			PromptChars:    uint32(len(fullPrompt)),
			ResponseChars:  uint32(len(out)),
			UserID:         meta.UserID,
			UserEmail:      meta.UserEmail,
			PromptSample:   truncForSample(fullPrompt),
			ResponseSample: truncForSample(out),
		}
		if err != nil {
			rec.Status = "error"
			rec.ErrorMsg = truncErr(err.Error())
		}
		// Fire-and-forget recording so the user gets their response
		// the moment the LLM returns — CH ingest can take 5-20ms.
		go func(r Recorder, rec CallRecord) {
			// Bounded ctx so a stuck CH ingest can't pin a goroutine.
			rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r.RecordCall(rctx, rec)
		}(s.recorder, rec)
	}
	return out, err
}

// truncForSample caps prompt/response samples at 4KB so a runaway
// prompt doesn't bloat the ai_calls row. CH ZSTD on the column
// handles the rest.
func truncForSample(s string) string {
	const cap = 4096
	if len(s) <= cap {
		return s
	}
	return s[:cap]
}

func truncErr(s string) string {
	if len(s) > 512 {
		return s[:512]
	}
	return s
}

// ── OpenAI-compatible (real OpenAI + Ollama / LM Studio / vLLM …) ───────────
//
// Ships a plain /v1/chat/completions request. Auth header is omitted
// when apiKey is empty so local endpoints that don't gate on it
// (Ollama default) just work — every gateway that DOES gate ignores
// a missing header and answers with a clean 401, which we surface.
//
// baseURL must include the /v1 prefix (or whatever the local endpoint
// uses) — e.g. http://ollama:11434/v1. We append /chat/completions.

// explainOpenAIWithUsage runs the OpenAI-compat call and parses
// the `usage` field for the AI observability recorder. Some
// local endpoints (older Ollama, vLLM) omit usage; those return
// 0 tokens and the recorder writes the row anyway with what it
// has (the latency + status are still useful).
func (s *Service) explainOpenAIWithUsage(ctx context.Context, systemPrompt, userPrompt string) (string, uint32, uint32, error) {
	s.mu.RLock()
	apiKey, model, base := s.apiKey, s.model, s.baseURL
	s.mu.RUnlock()
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if model == "" {
		// Reasonable default; operator typically overrides per
		// endpoint (`llama3.1`, `qwen2.5-coder`, `gpt-4o-mini`, …).
		model = "gpt-4o-mini"
	}
	url := strings.TrimRight(base, "/") + "/chat/completions"
	body := map[string]any{
		"model":       model,
		"max_tokens":  1024,
		"temperature": 0.2,
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
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := s.cli.Do(req)
	if err != nil {
		return "", 0, 0, fmt.Errorf("openai-compat call: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", 0, 0, fmt.Errorf("openai-compat %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     uint32 `json:"prompt_tokens"`
			CompletionTokens uint32 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", 0, 0, fmt.Errorf("decode openai-compat response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens,
			errors.New("openai-compat: empty response")
	}
	return parsed.Choices[0].Message.Content,
		parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens, nil
}

// ── Anthropic ───────────────────────────────────────────────────────────────

func (s *Service) explainAnthropicWithUsage(ctx context.Context, systemPrompt, userPrompt string) (string, uint32, uint32, error) {
	s.mu.RLock()
	apiKey, model := s.apiKey, s.model
	s.mu.RUnlock()
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	body := map[string]any{
		"model":      model,
		"max_tokens": 1024,
		"system":     systemPrompt,
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
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Anthropic-Version", "2023-06-01")

	resp, err := s.cli.Do(req)
	if err != nil {
		return "", 0, 0, fmt.Errorf("anthropic call: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", 0, 0, fmt.Errorf("anthropic %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  uint32 `json:"input_tokens"`
			OutputTokens uint32 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", 0, 0, fmt.Errorf("decode anthropic response: %w", err)
	}
	var out strings.Builder
	for _, c := range parsed.Content {
		if c.Type == "text" {
			out.WriteString(c.Text)
		}
	}
	return out.String(), parsed.Usage.InputTokens, parsed.Usage.OutputTokens, nil
}

// ── GitHub Copilot ──────────────────────────────────────────────────────────
//
// Two-step call:
//   1. Exchange the user's GitHub OAuth token (apiKey, ghu_…) for a
//      short-lived Copilot session token via copilot_internal/v2/token.
//      We cache it until ~30s before its server-stated expiry.
//   2. POST OpenAI-compat chat/completions to api.githubcopilot.com
//      with that session token as Bearer + the integration headers
//      Copilot's edge expects.

func (s *Service) explainGitHubWithUsage(ctx context.Context, systemPrompt, userPrompt string) (string, uint32, uint32, error) {
	sessTok, err := s.githubSessionToken(ctx)
	if err != nil {
		return "", 0, 0, err
	}
	s.mu.RLock()
	model := s.model
	s.mu.RUnlock()
	if model == "" {
		model = "gpt-4o"
	}
	body := map[string]any{
		"model":       model,
		"max_tokens":  1024,
		"temperature": 0.2,
		"messages": []map[string]any{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.githubcopilot.com/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sessTok)
	req.Header.Set("Editor-Version", "vscode/1.85.0")
	req.Header.Set("Editor-Plugin-Version", "copilot-chat/0.12.0")
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	req.Header.Set("User-Agent", "GithubCopilot/1.155.0")

	resp, err := s.cli.Do(req)
	if err != nil {
		return "", 0, 0, fmt.Errorf("github copilot call: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", 0, 0, fmt.Errorf("github copilot %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     uint32 `json:"prompt_tokens"`
			CompletionTokens uint32 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", 0, 0, fmt.Errorf("decode github copilot response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens,
			errors.New("github copilot: empty response")
	}
	return parsed.Choices[0].Message.Content,
		parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens, nil
}

// githubSessionToken returns a valid Copilot session token, refreshing
// from api.github.com when the cached one is missing or near expiry.
func (s *Service) githubSessionToken(ctx context.Context) (string, error) {
	s.mu.RLock()
	tok, exp := s.ghSessTok, s.ghSessExp
	s.mu.RUnlock()
	if tok != "" && time.Until(exp) > 30*time.Second {
		return tok, nil
	}

	s.mu.RLock()
	apiKey := s.apiKey
	s.mu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/copilot_internal/v2/token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "token "+apiKey)
	req.Header.Set("Editor-Version", "vscode/1.85.0")
	req.Header.Set("Editor-Plugin-Version", "copilot-chat/0.12.0")
	req.Header.Set("User-Agent", "GithubCopilot/1.155.0")

	resp, err := s.cli.Do(req)
	if err != nil {
		return "", fmt.Errorf("github token exchange: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("github token exchange %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode github token: %w", err)
	}
	if parsed.Token == "" {
		return "", errors.New("github token exchange: empty token (OAuth token missing Copilot access?)")
	}
	expiry := time.Unix(parsed.ExpiresAt, 0)
	if parsed.ExpiresAt == 0 {
		// Fallback for shape changes — assume 25 minutes.
		expiry = time.Now().Add(25 * time.Minute)
	}
	s.mu.Lock()
	s.ghSessTok = parsed.Token
	s.ghSessExp = expiry
	s.mu.Unlock()
	return parsed.Token, nil
}

// ── Persistence ─────────────────────────────────────────────────────────────
//
// Runtime overrides are stored in system_settings under "ai_copilot".
// Boot order: env defaults → DB overlay (LoadPersisted) → live calls
// to Configure() update both memory and DB.

const settingsKey = "ai_copilot"

type persisted struct {
	Provider string `json:"provider"`
	APIKey   string `json:"apiKey"`
	Model    string `json:"model"`
	BaseURL  string `json:"baseUrl,omitempty"`
}

// SettingsStore is the small slice of *chstore.Store we need —
// declared as an interface here so this package doesn't import chstore
// (which would cycle through callers).
type SettingsStore interface {
	GetSetting(ctx context.Context, key string) ([]byte, error)
	PutSetting(ctx context.Context, key string, value []byte) error
}

// LoadPersisted reads any DB-saved override and applies it. Silently
// skips when nothing's saved — env defaults stay in effect.
func (s *Service) LoadPersisted(ctx context.Context, store SettingsStore) error {
	raw, err := store.GetSetting(ctx, settingsKey)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var p persisted
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	s.Configure(p.Provider, p.APIKey, p.Model, p.BaseURL)
	return nil
}

// SavePersisted writes new credentials to system_settings AND updates
// the live Service. Called by PUT /api/settings/ai.
func (s *Service) SavePersisted(ctx context.Context, store SettingsStore, provider, apiKey, model, baseURL string) error {
	raw, err := json.Marshal(persisted{Provider: provider, APIKey: apiKey, Model: model, BaseURL: baseURL})
	if err != nil {
		return err
	}
	if err := store.PutSetting(ctx, settingsKey, raw); err != nil {
		return err
	}
	s.Configure(provider, apiKey, model, baseURL)
	return nil
}

// ── Prompt helpers (pre-baked so handlers don't have to compose) ────────────

const systemTrace = `You are a senior SRE assistant inside an APM tool. Given a JSON
representation of a single distributed trace (a list of spans with
service, name, parent, duration, status), explain in 4-8 short bullet
points: (1) the user-facing operation this trace represents, (2) the
slowest span and what fraction of total time it consumed, (3) where
errors are concentrated if any, (4) the most plausible root cause hint
the operator should investigate next.

Be terse and concrete — the operator is reading this on a pager call.
No preamble, no headers — just the bullets.`

// systemSpan — focused per-span explain (v0.5.144). Inputs are
// the target span + parent + immediate children + any error
// siblings in the same trace. Operator already knows what the
// whole trace does; they want "why is THIS step slow / failing".
const systemSpan = `You are a senior SRE assistant inside an APM tool. The operator
has highlighted ONE span in a distributed trace and wants to know
why specifically this step is slow or failing. The JSON you receive
carries the target span plus its parent + its direct children +
any error spans in the same trace.

Answer in 3-6 short bullets: (1) one-line description of what this
span is doing, (2) where the time goes (self vs. waiting on
children — call it out by service + name), (3) any error chain
visible in the context, (4) one or two concrete next-step
suggestions for an oncall.

Be terse and direct — operator is reading this on a pager call.
No preamble, no headers — just the bullets.`

const systemProblem = `You are a senior SRE assistant inside an APM tool. The operator
just opened a Problem (an alert that fired). Given the rule + service +
metric value, explain in 3-5 short bullet points: (1) what the alert
actually means in plain language, (2) the most likely causes ranked
by probability for this metric, (3) the first three things the
operator should check.

Be terse — this lands on a pager call. No preamble.`

const systemException = `You are a senior SRE assistant inside an APM tool. Given a code
exception (type, message, stacktrace, service), explain in 3-5
bullets: (1) what the exception class typically means, (2) the most
likely cause given the call site shown in the stacktrace, (3) the
fix hint or first investigation step.

Be terse and direct — the operator is debugging in real time.`

// systemIncident — used when the operator hits "Explain" on an
// incident detail or row. Incidents are higher-level than
// problems: they bundle multiple firings + a timeline; the
// model should reason about the WHOLE event rather than a
// single rule firing.
const systemIncident = `You are a senior SRE assistant inside an APM tool. The operator
opened an Incident — a grouped event that bundles one or more
related Problems + observations. Given the incident's title,
service, severity, timeline summary, and any attached problems,
explain in 3-5 bullets: (1) what's happening in plain language,
(2) the most plausible blast radius (services / clusters /
customers likely affected), (3) the first three coordination /
investigation actions for the oncall, (4) a one-line "should this
escalate to SEV-1?" call when severity warrants.

Be terse — this lands on a pager call. No preamble, no headers.`

// systemAnomaly — used on log-pattern / trace-op anomaly
// events. Different shape than Problem (no rule fired; pattern
// just exceeded baseline).
const systemAnomaly = `You are a senior SRE assistant inside an APM tool. The operator
opened an Anomaly — a pattern that started occurring more often
than its baseline. The signal isn't a hard alert; it's a
"something has changed" notice. Given the pattern, service, and
ratio, explain in 3-4 bullets: (1) what this anomaly pattern
typically indicates, (2) whether this kind of pattern is usually
benign or actionable, (3) the first thing to look at to confirm
intent vs incident, (4) one related metric/log query to run
next.

Be terse — operator triage context. No preamble.`

// systemServiceHealth — used when the operator hits "Explain
// service health" on a Service detail page. The model gets the
// three RED time-series (RPS, error rate, P99 latency), any
// recent deploys, and any active problems, and is asked to
// answer "is this service healthy right now and what should
// I look at first if it's not".
//
// Distinct from systemProblem because there may not be an
// alert firing — operator just wants a sanity-check on the
// chart shape. Wording biases the model toward "looks fine"
// vs "investigate X" rather than always-assuming-broken.
const systemServiceHealth = `You are a senior SRE assistant inside an APM tool. The operator
is looking at the live RED charts for one service and wants a
quick "is this healthy?" read. Given throughput / error rate /
P99 latency series over the window (with deploy markers + any
active problems), respond in 3-5 bullets:

  (1) one-line "looks healthy" / "warning signs" / "actively
      degraded" headline,
  (2) the most notable shape in the data (spike, ramp,
      bimodal, drift, flatline) if any,
  (3) likely cause hints anchored to the actual numbers shown
      (correlate with deploys / problems when relevant),
  (4) the first 2-3 things the operator should check.

Be terse and grounded in the numbers — no preamble, no
hedging like "without more context". If the data really does
look healthy, say so plainly.`

// systemRunbook — used when the operator hits "Suggest
// runbook" on an open Problem. Distinct from explain-problem:
// explain gives 3-5 bullets of context, runbook is a
// numbered, actionable step-list anchored in past resolved
// instances of the same rule on the same service. The model
// gets time-to-resolve from each past instance so it can lead
// with low-effort steps when similar problems resolved fast,
// or jump straight to escalation when they took >30 min.
const systemRunbook = `You are a senior SRE assistant inside an APM tool. The operator
just opened a Problem and wants an executable runbook — not an
explanation, an actual numbered checklist they can work through
on the pager call. Past resolved instances of the SAME rule on
the SAME service are attached with their time-to-resolve; use
that signal to bias the order of steps.

Produce 5-8 numbered steps, each one a concrete action:

  1. First triage check — the most-likely culprit given metric
     + service + past patterns. Name the actual dashboard,
     log query, or kubectl command.
  2-6. Follow-up checks in priority order. Reference real
     things to look at: pod names, db connection pool, GC
     pauses, downstream callee, deploy markers, feature
     flag toggles — whatever the metric + past instances
     point to.
  7. Escalation criteria — exactly when to wake a domain
     expert (e.g. "if step 4 shows GC > 2s, page Java
     platform").
  8. Verification — how to confirm the fix landed (specific
     metric returning to baseline within N minutes).

Rules:
  • If past similar problems consistently resolved in <5 min,
    lead with the fastest path that worked before.
  • If past instances took >30 min or escalated severity,
    surface escalation early (step 2 or 3, not last).
  • Every step must be specific to THIS service / metric.
    Generic "check logs" is a fail.
  • No preamble. No "Here's a runbook:". Just the numbered
    list, one short paragraph per step max.`

func SystemPromptTrace() string         { return systemTrace }
func SystemPromptSpan() string          { return systemSpan }
func SystemPromptProblem() string       { return systemProblem }
func SystemPromptException() string     { return systemException }
func SystemPromptIncident() string      { return systemIncident }
func SystemPromptAnomaly() string       { return systemAnomaly }
func SystemPromptServiceHealth() string { return systemServiceHealth }
func SystemPromptRunbook() string       { return systemRunbook }

// systemCompareTraces — used when the operator hits "Compare
// with…" on a trace detail page and supplies a second trace
// ID. The prompt receives a precomputed structured diff
// (both root summaries, per-shared-operation latency delta,
// services present in one but not the other, error span set
// diff) and explains in plain language WHY the two traces
// diverged. Designed for the typical incident workflow
// "today's slow trace vs yesterday's fast one" — the model
// should call out the single biggest contributor to the
// difference, not enumerate everything.
const systemCompareTraces = `You are a senior SRE assistant inside an APM tool. The
operator picked two traces (A and B) and asked WHY they
differ. You receive a structured diff of the two traces:
root summaries, top operations ranked by latency delta,
services present in one trace but not the other, and the
error footprint of each.

Respond in 3-5 short bullets:
  (1) one-line headline: which trace is slower / broken and
      by how much (% or ms),
  (2) the single biggest contributor to the difference —
      the slowest delta operation or the missing service,
      named explicitly,
  (3) the most plausible root cause hint anchored to the
      diff data (deploy, downstream call, cold cache,
      database lock, retry storm…),
  (4) optional: one-line "investigate next" pointer to the
      service or operation the operator should open.

Be terse and concrete. Don't restate the raw diff — the
operator already saw it. Don't hedge ("without more
context"). If the two traces are essentially the same,
say so plainly.`

func SystemPromptCompareTraces() string { return systemCompareTraces }

// systemDeployImpact — used when the operator hits "Explain
// latest deploy" on a service detail page. The prompt
// receives a before/after RED-metric diff anchored on a
// specific service.version transition + the new operations
// that appeared after the deploy, and explains in plain
// language whether the deploy was clean, degraded one signal,
// or introduced a regression. Designed for the
// post-deploy "is this safe to walk away from?" check.
const systemDeployImpact = `You are a senior SRE assistant inside an APM tool. The
operator deployed version X of a service and wants to know
the impact. You receive RED metrics (rate, error_rate,
P99 latency) over equal-length windows before and after the
first-seen timestamp of the deploy, plus the set of
operations that appeared in the after-window but not the
before-window.

Respond in 3-5 short bullets:
  (1) one-line headline: "clean deploy", "minor regression
      on X metric", or "rollback candidate — Y is broken",
  (2) the single metric with the biggest delta — name it
      with the absolute delta and the % change,
  (3) if new operations appeared, the most likely one to
      be the culprit (high-volume, error-heavy, or both),
  (4) recommended next step: keep deployed, watch X, or
      roll back. Anchor it to the data.

Be terse and grounded in the numbers. Don't speculate
beyond the diff data. If everything looks healthy, say
"clean deploy" plainly.`

func SystemPromptDeployImpact() string { return systemDeployImpact }

// systemSLOBurn — used when the operator hits "Explain burn"
// on a breached / burning SLO row. The prompt receives the
// SLO definition + current status (SLI, budget remaining,
// burn rate over fast + slow windows) and explains what to
// look at first. Distinct from explain-problem because an
// SLO breach is a multi-hour / multi-day signal that the
// budget is being consumed — the answer should anchor on
// trajectory (will the budget last the rolling window?) not
// on a single firing.
const systemSLOBurn = `You are a senior SRE assistant inside an APM tool. The
operator opened an SLO that's either breached or burning
fast. You receive the SLO definition (service, target,
window in days, optional operation scope, latency SLI's
ms threshold), the current status (SLI %, budget
remaining, burn rate), and the fast+slow burn-rate samples
from the v0.5.x burn evaluator.

Respond in 3-5 short bullets:
  (1) one-line headline: "budget on track", "burning fast —
      Y hours to exhaustion", or "already breached, recovery
      in N hours assuming current SLI".
  (2) primary driver: latency or availability — name the
      number that's off.
  (3) recommended first investigation: open the service
      page / look at deploy markers in the burn window /
      check the operation scope if one is set.
  (4) optional: escalation guidance if the burn rate >=10
      (Google SRE Workbook critical multi-burn-rate alarm).

Be terse and grounded in the numbers. Don't hedge ("without
more context"). If the burn rate < 1 say "budget on track"
plainly even when the operator clicked the button.`

func SystemPromptSLOBurn() string { return systemSLOBurn }

// systemServiceTags — used when the operator hits "AI suggest"
// on a row in the service catalog editor. Given the service's
// runtime fingerprint, sample operations, callees, and cluster
// names, the model proposes owner team / SRE team / one-line
// description / criticality.
//
// The reply MUST be a single JSON object so the UI can pre-fill
// the edit form directly. Any prose outside the object trips
// JSON parsing and the operator just sees "no suggestions" —
// safer than letting bad output land in the live form.
const systemServiceTags = `You are a senior platform engineer onboarding into a new
distributed system. Given a single service's name, runtime
fingerprint, top operations, downstream dependencies, and
cluster footprint, propose a curation entry for the service
catalog.

Output a SINGLE JSON object with these fields (omit / empty
when you can't reasonably infer):

  {
    "ownerTeam":    "<short slug or team handle>",
    "sreTeam":      "<short slug — platform / infra team>",
    "description":  "<one-line plain-English purpose>",
    "criticality":  "<tier1 | tier2 | tier3>",
    "confidence":   "<high | medium | low>",
    "reasoning":    "<one short sentence: what signal drove the call>"
  }

Inference rules:

  • Service name + operation patterns dominate the team
    guess. "payments-api" with operations like "POST /charge"
    is payments-domain; "auth-svc" with "/login /refresh"
    is identity / platform-auth.
  • Strong DB dependency on a single domain (Postgres
    "orders" schema, Kafka topic "payments.*") narrows
    further.
  • Public-traffic services (api-gateway, bff-*, frontend
    egress) → tier1 by default unless evidence says
    otherwise.
  • Internal-only backends with no upstream callers AND
    low span volume → tier3.
  • Java / Spring naming patterns hint at typical bank
    org structures; Go services often platform / infra.
  • confidence=high only when at least two signals agree.

Never make up team slugs you can't justify from the data.
Empty fields beat fabricated ones — the operator reviews
the suggestion before saving.

NO preamble, NO trailing prose. Just the JSON object.`

func SystemPromptServiceTags() string   { return systemServiceTags }
