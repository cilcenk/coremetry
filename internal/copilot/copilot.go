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
// user prompt. Branches on the configured provider.
func (s *Service) Explain(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if !s.Configured() {
		return "", errors.New("AI copilot not configured (open Settings → AI Copilot)")
	}
	s.mu.RLock()
	provider := s.provider
	s.mu.RUnlock()
	switch provider {
	case ProviderGitHub:
		return s.explainGitHub(ctx, systemPrompt, userPrompt)
	case ProviderOpenAI:
		return s.explainOpenAI(ctx, systemPrompt, userPrompt)
	default:
		return s.explainAnthropic(ctx, systemPrompt, userPrompt)
	}
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

func (s *Service) explainOpenAI(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
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
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := s.cli.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai-compat call: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai-compat %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("decode openai-compat response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("openai-compat: empty response")
	}
	return parsed.Choices[0].Message.Content, nil
}

// ── Anthropic ───────────────────────────────────────────────────────────────

func (s *Service) explainAnthropic(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
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
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Anthropic-Version", "2023-06-01")

	resp, err := s.cli.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic call: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("anthropic %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("decode anthropic response: %w", err)
	}
	var out strings.Builder
	for _, c := range parsed.Content {
		if c.Type == "text" {
			out.WriteString(c.Text)
		}
	}
	return out.String(), nil
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

func (s *Service) explainGitHub(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	sessTok, err := s.githubSessionToken(ctx)
	if err != nil {
		return "", err
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
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sessTok)
	req.Header.Set("Editor-Version", "vscode/1.85.0")
	req.Header.Set("Editor-Plugin-Version", "copilot-chat/0.12.0")
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	req.Header.Set("User-Agent", "GithubCopilot/1.155.0")

	resp, err := s.cli.Do(req)
	if err != nil {
		return "", fmt.Errorf("github copilot call: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("github copilot %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("decode github copilot response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("github copilot: empty response")
	}
	return parsed.Choices[0].Message.Content, nil
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

func SystemPromptTrace() string     { return systemTrace }
func SystemPromptProblem() string   { return systemProblem }
func SystemPromptException() string { return systemException }
