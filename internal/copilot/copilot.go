// Package copilot wraps an LLM Messages/Chat API to produce
// natural-language explanations of telemetry artifacts — trace flame,
// open Problem, exception group.
//
// Three providers supported:
//   - "anthropic": Anthropic Messages API (api.anthropic.com).
//   - "github":    GitHub Copilot Chat (api.githubcopilot.com). The
//     caller's API key is a GitHub OAuth token (`ghu_…`)
//     which we exchange for a short-lived Copilot session
//     token (cached + auto-refreshed).
//   - "openai":    Any OpenAI-compatible /v1/chat/completions endpoint.
//     Drives self-hosted local LLMs (Ollama, LM Studio,
//     vLLM, llama.cpp server, LocalAI, OpenWebUI) AND
//     the real OpenAI API. Banks running Coremetry
//     air-gapped want this so traces / problems never
//     leave the perimeter for explanation. APIKey is
//     optional for local endpoints that don't gate on
//     it (Ollama default).
//
// The Service is configurable at runtime — admins can flip provider
// or rotate keys via the Settings UI without restarting Coremetry.
//
// SİSTEM PROMPT'LARI BU DOSYADA DEĞİL — hepsi prompts.go'da (Faz 1.6,
// v0.9.1128). Bu dosya Service + config + politika (kota, JSON kipi,
// tuning, persist) tutar. Prompt eklemek/değiştirmek için prompts.go'ya
// bak; prompt'u api paketinde tanımlamak yapısal kapıya takılır
// (prompt_language_test.go).
package copilot

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
	baseURL string
	// skipTLS — when true the embedded http.Client uses an
	// InsecureSkipVerify TLS config. v0.5.360: operator-requested
	// for the same self-hosted enterprise-CA case the Tempo +
	// LDAP integrations already handle (Coremetry behind an
	// internal cert the OS trust store doesn't know about). Off
	// by default; toggled via the Settings UI.
	skipTLS bool

	// quotaUntil (v0.9.200) — provider-quota devre kesici. Bir çağrı
	// 429/quota hatası döndüğünde 1 saat ileri damgalanır; ARKA PLAN
	// tüketicileri (problem-explainer) QuotaBackoffActive'ken tick'i
	// sessizce atlar ki kalan kota interaktif çağrılara (analiz, CoSRE)
	// kalsın. İnteraktif yollar ETKİLENMEZ — denerler; başarılı olursa
	// kota dönmüş demektir ve pencere sonunda kesici kendiliğinden
	// kapanır. Kota-olmayan hatalar (timeout, 5xx) kesiciyi AÇMAZ.
	quotaUntil time.Time

	// enabled — master on/off switch INDEPENDENT of whether creds
	// are stored (wf: enable/disable toggle). Configured() means
	// "has creds"; Active() means "enabled AND configured". The
	// operator flips this off to STOP the background ProblemExplainer
	// hammering the provider + hide the AI affordances WITHOUT
	// clearing the stored key (re-enabling is one click). A fresh
	// Service is enabled by default; a persisted blob without the
	// "enabled" field decodes as enabled (the *bool nil⇒true rule).
	enabled bool

	// ── LLM call tuning (v0.9.1120, Faz 0.4) ───────────────────────
	//
	// Operator-configurable knobs that used to be hard-coded literals
	// scattered across eight request builders. All three are
	// "unset ⇒ use the default" (0 / nil), so a legacy `ai_copilot`
	// blob that predates the fields keeps today's behaviour. Read via
	// tuneMaxTokens / tuneTemperature / clientTimeout — never
	// directly, so the default lives in exactly one place.
	//
	// The literals they replaced were INCONSISTENT: max_tokens was
	// 4096 on the openai-compat paths but still 1024 on anthropic
	// explain, github explain and anthropic stream (the v0.8.138 /
	// v0.8.393 budget lift never reached them), and temperature was
	// 0.2 on openai/github but ABSENT from every anthropic body
	// (provider default ~1.0). Both gaps close here.
	maxTokens int
	// temperature is a POINTER for the same reason `enabled` is:
	// 0 is a VALID temperature (fully deterministic), so a plain
	// float64 could not distinguish "operator asked for 0" from
	// "operator said nothing". nil ⇒ default.
	temperature *float64
	// timeoutS overrides the http.Client timeout. Changing it REBUILDS
	// the client (see rebuildClientLocked) — an http.Client's Timeout
	// is read at request start, but the shared client is swapped
	// wholesale so in-flight calls keep the deadline they started with.
	timeoutS int

	// GitHub session token cache. We exchange ghu_ → session token
	// once and reuse until ~30s before the server-stated expiry.
	ghSessTok string
	ghSessExp time.Time

	// streamUnsupported (v0.8.404) — in-proc per-(provider,baseURL,
	// model) verdict cache for token streaming. When a stream:true
	// probe is deterministically rejected (some vLLM builds 400 on
	// it; some gateways answer 200+JSON ignoring the flag) the
	// endpoint is marked here so every subsequent guided call goes
	// straight to the buffered path instead of re-probing. Reset on
	// Configure — a provider/model/URL change invalidates the verdict.
	streamUnsupported map[string]bool

	// jsonModeUnsupported (v0.9.517) — streamUnsupported'un JSON-mod
	// ikizi. Katı JSON isteyen yüzeyler response_format ile çağırır;
	// bazı sunucular (eski OpenAI-uyumlu proxy'ler, kimi Ollama sürümleri)
	// bu alanı 400'ler. İlk ret kaydedilir ve o uç için bir daha
	// denenmez — her çağrıda yeniden yoklamak boşuna gecikme ve
	// çift faturalandırma olurdu. Configure'da sıfırlanır.
	jsonModeUnsupported map[string]bool

	// jsonSchemaUnsupported (v0.9.527) — merdivenin üst basamağı.
	// `json_schema` `json_object`'ten kesinlikle daha az yaygın: şemayı
	// destekleyemeyen bir uç genellikle object'i destekler. Bu yüzden iki
	// ayrı karar tutulur — şemayı reddeden uçta object'e düşeriz,
	// ikisini birden kaybetmeyiz. Configure'da sıfırlanır.
	jsonSchemaUnsupported map[string]bool

	cli *http.Client
	// cliSkipTLS records which skipTLS value the LIVE client was built
	// with. Before v0.9.1120 the rebuild condition compared the
	// incoming argument against s.skipTLS, which only worked because
	// Configure was the single writer. Now that ConfigureTuning can
	// also rebuild, the client's own provenance has to be tracked —
	// otherwise a tuning-only change would silently rebuild the client
	// with the wrong TLS mode.
	cliSkipTLS bool

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
	CreatedAt time.Time
	Surface   string
	// ExchangeID (v0.8.399) — correlation key for operator feedback:
	// the chat handler mints one id per exchange, emits it to the UI
	// in the SSE answer event, and threads it here via CallMeta so a
	// thumbs up/down (ai_feedback row) can be joined back to the
	// ai_calls row it rates. Empty for surfaces that don't emit one.
	// Provider-agnostic — pure correlation plumbing, no LLM coupling.
	ExchangeID     string
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
	// ExchangeID — see CallRecord.ExchangeID (v0.8.399). Carried in
	// ctx so both the RecordUsage path (free chat loop) and the
	// Explain self-recording path (guided chat) stamp the same id
	// without new parameters on either call chain.
	ExchangeID string
	// PromptLogOverride (v0.9.831) — what ai_calls.prompt_sample
	// records INSTEAD of the real prompt. Empty = record the real one
	// (every surface but one).
	//
	// Exists for exactly one reason: the "Kodu da incele" path puts
	// the customer's SOURCE CODE in the prompt. That code has to reach
	// the model, but it has no business being copied into a telemetry
	// table that /ai renders, ClickHouse retains and an export dumps.
	// The caller substitutes a `[kod: repo/dosya:aralık · N satır]`
	// summary here, so the trail still says which file was consulted
	// without storing the file.
	//
	// Deliberately affects the SAMPLE only — PromptChars keeps
	// counting the REAL prompt. A masked size would understate the
	// call's cost, and the /ai page's whole job is telling the truth
	// about cost.
	PromptLogOverride string
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
		// A fresh Service is enabled by default — the operator only
		// flips it off explicitly via Settings (wf: enable/disable).
		enabled: true,
		// Local LLMs (Ollama loading a 70B model, llama.cpp on CPU)
		// can take 60+ seconds for a first generation. The client
		// timeout (180s) matches the cold-load worst case.
		// v0.5.360 — transport built via buildCopilotHTTPClient so
		// the TLS-skip flag has a single creation site.
		// v0.9.1120 — timeout is a parameter now; a fresh Service has no
		// override so it gets defaultTimeout (the same 180s).
		cli: buildCopilotHTTPClient(false, defaultTimeout),
	}
}

// Configure swaps live credentials. Used by PUT /api/settings/ai.
// Empty apiKey legitimately disables the feature — Configured() flips
// to false and the UI hides the buttons. baseURL is only consulted
// by the "openai" provider; ignored for anthropic/github so a stale
// value persisted from a previous selection doesn't leak.
// v0.5.360: skipTLS rebuilds the http.Client transport when it
// flips; otherwise the existing client is kept (its 180s timeout
// matches the local-LLM use case).
// wf: enabled is the master on/off switch — set here so it lives
// behind the same lock as the creds and can't tear with an
// in-flight Active()/Explain.
func (s *Service) Configure(provider, apiKey, model, baseURL string, skipTLS, enabled bool) {
	if provider == "" {
		provider = ProviderAnthropic
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Provider or key changed → drop any cached GitHub session token.
	if s.provider != provider || s.apiKey != apiKey {
		s.ghSessTok, s.ghSessExp = "", time.Time{}
	}
	s.provider, s.apiKey, s.model, s.baseURL, s.skipTLS = provider, apiKey, model, baseURL, skipTLS
	s.enabled = enabled
	// v0.9.1120 — the rebuild decision moved into rebuildClientLocked
	// so BOTH inputs (skipTLS, timeout) are compared in one place. It
	// runs AFTER s.skipTLS is assigned: the helper compares the live
	// fields against the live client, not against arguments.
	s.rebuildClientLocked()
	// v0.8.404 — the streaming-support verdicts were probed against
	// the OLD endpoint config; a swap must re-probe.
	s.streamUnsupported = nil
	s.jsonModeUnsupported = nil
	s.jsonSchemaUnsupported = nil
}

// ── LLM call tuning ─────────────────────────────────────────────────────────
//
// v0.9.1120 (Faz 0.4) — the three knobs every provider body needs.
// Defaults live HERE and nowhere else; the request builders call the
// getters so a change is one edit, and an operator override is one
// settings blob field.

const (
	// defaultMaxTokens — the completion budget every provider gets
	// unless the operator overrides it. Aliases openAICompletionTokens
	// (4096) because that constant already carries the reasoning-model
	// rationale; the point of the alias is that anthropic and github
	// now share it instead of their own 1024.
	defaultMaxTokens = openAICompletionTokens
	// defaultTemperature — 0.2 was already the openai/github literal.
	// Applying it to anthropic too is a deliberate BEHAVIOUR CHANGE
	// (provider default ≈1.0 → 0.2): an APM explanation should be
	// reproducible, not creative.
	defaultTemperature = 0.2
	// defaultTimeout — local LLMs (Ollama loading a 70B model,
	// llama.cpp on CPU) can take 60+ seconds for a first generation.
	defaultTimeout = 180 * time.Second
)

// Accepted override ranges. Guard rails, not opinions — they exist so
// a typo can't burn a quota or wedge a request, and every value inside
// them is the operator's call.
const (
	// Below ~256 tokens no explanation fits (the prompts alone budget
	// for structured output); above 32768 no model Coremetry targets
	// accepts the value and a stray zero would be expensive.
	minMaxTokens = 256
	maxMaxTokens = 32768
	// 0..2 is the openai-compat range. Anthropic caps at 1 and 400s
	// above it — we let the PROVIDER reject that rather than guessing
	// here which provider the operator is pointed at, because the knob
	// is stored once and the provider can change under it.
	minTemperature = 0.0
	maxTemperature = 2.0
	// Under 10s a local-LLM cold load (Ollama pulling a 70B into RAM)
	// can't finish; over 600s the request outlives every reverse proxy
	// and ingress in front of Coremetry, so the timeout would be a lie.
	minTimeoutS = 10
	maxTimeoutS = 600
)

// ValidateTuning checks operator-supplied knob values. Zero / nil means
// "use the default" for each field INDEPENDENTLY and is always valid —
// that is how an older client (and the Reset button) says "unset".
// Pure so the settings handler stays a thin shell over a tested rule.
func ValidateTuning(maxTokens int, temperature *float64, timeoutS int) error {
	if maxTokens != 0 && (maxTokens < minMaxTokens || maxTokens > maxMaxTokens) {
		return fmt.Errorf("maxTokens must be 0 (default) or between %d and %d", minMaxTokens, maxMaxTokens)
	}
	if temperature != nil && (*temperature < minTemperature || *temperature > maxTemperature) {
		return fmt.Errorf("temperature must be omitted (default) or between %g and %g", minTemperature, maxTemperature)
	}
	if timeoutS != 0 && (timeoutS < minTimeoutS || timeoutS > maxTimeoutS) {
		return fmt.Errorf("timeoutS must be 0 (default) or between %d and %d", minTimeoutS, maxTimeoutS)
	}
	return nil
}

// ConfigureTuning applies the LLM call knobs. Deliberately SEPARATE
// from Configure: the credential path has six callers' worth of
// history and a settled signature, and the two are set from the same
// blob anyway (LoadPersisted calls both). Zero / nil means "use the
// default" for each field independently.
func (s *Service) ConfigureTuning(maxTokens int, temperature *float64, timeoutS int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxTokens, s.temperature, s.timeoutS = maxTokens, temperature, timeoutS
	// A timeout change must reach the shared client — otherwise the
	// setting is accepted, echoed back by GET, and quietly not applied
	// (the fail-open-silently-unapplies class).
	s.rebuildClientLocked()
}

// tuneMaxTokens — completion budget for a request body. Override or
// 4096.
//
// FAZ 1.3 sonrası kilit ALAN bu üçlünün (tuneMaxTokens /
// tuneTemperature / clientTimeout) prod çağıranı kalmadı: her istek
// callSnapshot'ın TEK RLock'undan geçiyor ve *Locked ikizleri
// kullanılıyor. Üçlü, efektif değerin ("ezme yoksa varsayılan")
// dışarıdan okunabilir tek yolu olarak duruyor — tuning_test.go
// blob→efektif değer zincirini bunlarla pinliyor. Silinirse o pin
// Service'in içine elle kilit alarak uzanmak zorunda kalırdı.
func (s *Service) tuneMaxTokens() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tuneMaxTokensLocked()
}

// tuneMaxTokensLocked / tuneTemperatureLocked — lock-free halves,
// mirroring clientTimeout/clientTimeoutLocked. A caller that already
// holds s.mu (the provider snapshot, v0.9.1123 Faz 1.1) uses these:
// re-entering an RWMutex for a read deadlocks once a writer is queued.
// Splitting rather than duplicating the defaulting logic is deliberate
// — a second copy of "override or default" is exactly how the 1024
// budget drifted for ~1000 releases (v0.9.1120).
func (s *Service) tuneMaxTokensLocked() int {
	if s.maxTokens > 0 {
		return s.maxTokens
	}
	return defaultMaxTokens
}

// tuneTemperature — (value, include). The bool exists so a future
// "send no temperature at all" (some endpoints reject it alongside
// reasoning modes) is expressible without touching seven call sites;
// today it is always true.
func (s *Service) tuneTemperature() (float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tuneTemperatureLocked()
}

func (s *Service) tuneTemperatureLocked() (float64, bool) {
	if s.temperature != nil {
		return *s.temperature, true
	}
	return defaultTemperature, true
}

// clientTimeout — http.Client timeout. Override or 180s.
func (s *Service) clientTimeout() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.clientTimeoutLocked()
}

// clientTimeoutLocked is the lock-free half — callers already holding
// s.mu (Configure, ConfigureTuning) use this to avoid re-entering an
// RWMutex, which deadlocks when a writer is queued.
func (s *Service) clientTimeoutLocked() time.Duration {
	if s.timeoutS > 0 {
		return time.Duration(s.timeoutS) * time.Second
	}
	return defaultTimeout
}

// rebuildClientLocked swaps the shared http.Client when either input
// it bakes in — TLS-skip or timeout — no longer matches the live
// config. Caller must hold s.mu for writing. Cheap and idempotent:
// with nothing changed it does nothing, so both Configure and
// ConfigureTuning can call it unconditionally.
func (s *Service) rebuildClientLocked() {
	want := s.clientTimeoutLocked()
	if s.cli != nil && s.cli.Timeout == want && s.cliSkipTLS == s.skipTLS {
		return
	}
	s.cli = buildCopilotHTTPClient(s.skipTLS, want)
	s.cliSkipTLS = s.skipTLS
}

// httpClient is the read side of s.cli. Every request path goes
// through it: rebuildClientLocked WRITES the field under the lock, so
// a bare `s.cli.Do(...)` would be a data race against a config
// refresh (StartConfigRefresh re-applies the blob every 30s).
func (s *Service) httpClient() *http.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cli
}

// buildCopilotHTTPClient — mirrors the Tempo / LDAP pattern. When
// skipTLS is true the transport runs with InsecureSkipVerify;
// useful for self-hosted LLMs behind an enterprise-CA that Go's
// default trust store doesn't know about. The timeout defaults to
// 180s (local-LLM cold-load worst case, Ollama loading a 70B model)
// and is operator-tunable since v0.9.1120.
func buildCopilotHTTPClient(skipTLS bool, timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if skipTLS {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
	}
}

// Snapshot returns the current configuration. The apiKey is masked
// (only "set" / "unset" matters to the UI) — full key is never echoed.
// baseURL is non-secret (operators put it in their Helm values), so
// we echo it back so the Settings page can show what's wired up.
// v0.5.360 — skipTLS surfaced so the UI checkbox reflects what's
// actually live.
// wf — enabled surfaced so getAISettings can drive the Settings
// toggle independently of whether a key is stored.
func (s *Service) Snapshot() (provider, model, baseURL string, hasKey, skipTLS, enabled bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.provider, s.model, s.baseURL, s.apiKey != "", s.skipTLS, s.enabled
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

// Active reports whether the Copilot is BOTH enabled AND configured.
// This is the gate for any path that actually calls the provider —
// the background ProblemExplainer, Explain/ChatWithTools, the AI-usage
// HTTP endpoints, and the UI feature-flag (/api/copilot/config).
//
// Distinct from Configured(): when the operator flips "Enable AI
// Copilot" OFF in Settings we KEEP the stored creds (Configured()
// stays true so the Settings form still renders) but Active() goes
// false — the background explainer stops hammering the provider, the
// AI affordances hide, and the AI endpoints 503. Re-enabling is one
// click (no key to re-paste). wf.
//
// Inlines Configured()'s logic under a single RLock so the enabled
// gate and the cred check read a consistent snapshot.
func (s *Service) Active() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.enabled {
		return false
	}
	if s.apiKey != "" {
		return true
	}
	return s.provider == ProviderOpenAI && s.baseURL != ""
}

// ActiveModel returns the configured model id — but ONLY when the
// Copilot is Active. Kapalı ya da kimliksiz bir kurulumda boş döner.
//
// v0.9.1036+ — AI çekmecesinin model çipi bunu okuyor. Ayrı bir metot
// olmasının nedeni, "yalnız aktifken sızar" kuralının TEK yerde
// yaşaması: handler'da `if Active() { Snapshot() }` yazmak kuralı
// çağrı noktasına dağıtırdı ve Snapshot() nil-güvenli DEĞİL (Active()
// öyle) — s.copilot hiç yapılandırılmamışsa nil'dir.
//
// Model adı sır değildir (operatör Helm values'ına yazıyor) ama
// baseURL/apiKey öyle: bu metot yalnız modeli döndürür, ikisini de
// taşımaz.
func (s *Service) ActiveModel() string {
	if !s.Active() {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.model
}

// Explain runs a single Messages/Chat call with the given system +
// user prompt. Branches on the configured provider. v0.5.162 wraps
// the dispatch with the AI-observability recorder so every call
// emits an ai_calls row regardless of success — recording happens
// on a goroutine so the user doesn't pay ingest cost in their
// request path.
func (s *Service) Explain(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
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
	// FAZ 1.2 — üç dal da internal/ai/provider'dan geçiyor; yüzey
	// ayrımı YOK (Faz 1.1'in tek-yüzey canary'si ve üç eski üretici
	// birlikte silindi). Gövdeleri kuran kod provider_calls.go'da.
	switch provider {
	case ProviderGitHub:
		out, inputTokens, outputTokens, err = s.explainGitHub(ctx, systemPrompt, userPrompt)
	case ProviderOpenAI:
		out, inputTokens, outputTokens, err = s.explainOpenAI(ctx, systemPrompt, userPrompt)
	default:
		out, inputTokens, outputTokens, err = s.explainAnthropic(ctx, systemPrompt, userPrompt)
	}

	s.recordNarration(ctx, started, provider, model, baseURL, systemPrompt, userPrompt, out, inputTokens, outputTokens, err)
	s.noteProviderError(err)
	return out, err
}

// isQuotaErr — sağlayıcı kota/hız-limiti hatası mı? (Gemini free tier
// "429 ... quota", OpenAI "rate limit", Google RESOURCE_EXHAUSTED.)
// Saf + table-tested; timeout/5xx gibi geçici hatalar kota SAYILMAZ.
func isQuotaErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, kw := range []string{" 429", "429:", "quota", "rate limit", "rate_limit", "resource_exhausted", "too many requests"} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// noteProviderError arms the quota circuit-breaker on a quota-class
// error (v0.9.200). 1h pencere: free-tier günlük kotalar dakikalarla
// düzelmez; interaktif çağrılar yine de denediği için erken dönüş
// operatörü bekletmez.
func (s *Service) noteProviderError(err error) {
	if !isQuotaErr(err) {
		return
	}
	s.mu.Lock()
	if time.Now().After(s.quotaUntil) {
		s.quotaUntil = time.Now().Add(time.Hour)
		log.Printf("[copilot] provider quota hit (429) — background AI consumers paused for 1h so interactive calls keep the remaining quota")
	}
	s.mu.Unlock()
}

// QuotaBackoffActive reports whether the quota circuit-breaker window
// is open. Background consumers (problem-explainer) skip their tick
// while true; interactive surfaces ignore it.
func (s *Service) QuotaBackoffActive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return time.Now().Before(s.quotaUntil)
}

// recordNarration emits the single ai_calls row for a one-shot
// narration call. Shared by Explain and StreamText (v0.8.404) so the
// streaming twin records under exactly the same contract — one row
// per call, fire-and-forget, errors land with status="error".
func (s *Service) recordNarration(ctx context.Context, started time.Time,
	provider, model, baseURL, systemPrompt, userPrompt, out string,
	inputTokens, outputTokens uint32, err error) {
	if s.recorder == nil {
		return
	}
	meta := MetaFromContext(ctx)
	fullPrompt := systemPrompt + "\n\n" + userPrompt
	// v0.9.831 — the recorded SAMPLE may be a masked copy (source
	// code stripped, see CallMeta.PromptLogOverride). PromptChars
	// below stays on fullPrompt: the row must report what the call
	// actually cost.
	logPrompt := fullPrompt
	if meta.PromptLogOverride != "" {
		logPrompt = meta.PromptLogOverride
	}
	rec := CallRecord{
		CreatedAt:      started,
		Surface:        meta.Surface,
		ExchangeID:     meta.ExchangeID,
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
		PromptSample:   truncForSample(logPrompt),
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

// ── JSON modu: response_format merdiveni ────────────────────────────────────
//
// v0.9.1124 (Faz 1.2) — buffered istek ÜRETİCİLERİ artık burada değil,
// internal/ai/provider'da; Service'te merdivenin DURUM tutan yarısı
// kaldı: hangi basamak denenir, reddedilince ne olur, karar hangi uç
// için önbelleklenir. Çağrı yolu provider_calls.go.

// openAICompletionTokens caps the OpenAI-compatible completion budget.
// Reasoning models (Qwen3, deepseek-r1, …) spend tokens on a thinking phase
// before emitting the answer; at 1024 they often finished mid-thought
// (finish_reason "length", empty content), so we give them room.
const openAICompletionTokens = 4096

// jsonModeKey — bağlamda "bu çağrı katı JSON istiyor" bayrağı. Bağlam
// üzerinden taşınıyor (CallMeta deseninin aynısı) ki dört yüzeyin ve
// aradaki sarmalayıcıların imzaları değişmesin.
type jsonModeKey struct{}
type jsonSchemaKey struct{}

// jsonLevel — response_format merdiveninin basamağı. Yüksek basamak
// daha çok garanti, daha az sunucu desteği. Reddedilen basamak bir alta
// düşer; en alt basamak kısıtsız çağrıdır ve her zaman çalışır.
type jsonLevel int

const (
	jsonNone   jsonLevel = iota // response_format yok
	jsonObject                  // {"type":"json_object"} — "geçerli JSON üret"
	jsonSchema                  // {"type":"json_schema", …} — "BU şekli üret"
)

func (l jsonLevel) String() string {
	switch l {
	case jsonSchema:
		return "json_schema"
	case jsonObject:
		return "json_object"
	}
	return "kısıtsız"
}

// jsonSchemaSpec — bir yüzeyin beklediği çıktı şekli.
type jsonSchemaSpec struct {
	Name   string
	Schema map[string]any
}

// WithJSONMode — modelden KATI JSON isteyen çağrılar için.
//
// response_format sunucu tarafında çözümlemeyi kısıtlar: model JSON
// DIŞINA çıkamaz. İyi bir modelde bile değerli — JSON kaçakları nadir
// ama sessiz, ve bu yüzeyler post-check ile temizlemeye çalışıyordu.
// Kısıt o sınıfı tamamen kapatıyor. Desteklemeyen uçta sessizce eski
// davranışa düşer (bir kez yoklanır, karar önbelleklenir).
func WithJSONMode(ctx context.Context) context.Context {
	return context.WithValue(ctx, jsonModeKey{}, jsonObject)
}

// WithJSONSchema — çıktının ŞEKLİNİ de dayatan üst basamak (v0.9.527).
//
// `json_object` yalnız "geçerli JSON" der; alan eksik olabilir, tip
// kayabilir, enum dışı değer gelebilir. `json_schema` çözümlemeyi
// şemaya kilitler — asıl kazanç ENUM: bugün sunucu tarafında elle
// temizlenen alanlar (filtre operatörü, aralık ön-ayarı, güven
// seviyesi) modelin üretebileceği küme dışına çıkar.
//
// ÖNEMLİ: sunucu-tarafı doğrulama bunun yerine GEÇMEZ. Şema desteği
// yoklamayla kapanmış olabilir, basamak düşmüş olabilir, uç eski
// olabilir — üç durumda da yanıt şemasız gelir. Şema kaliteyi
// yükseltir, doğrulamanın yerini almaz.
//
// Şema OpenAI `strict` kurallarına uygun olmalı: her object'te
// `additionalProperties: false` ve tüm anahtarlar `required`.
func WithJSONSchema(ctx context.Context, name string, schema map[string]any) context.Context {
	ctx = context.WithValue(ctx, jsonSchemaKey{}, jsonSchemaSpec{Name: name, Schema: schema})
	return context.WithValue(ctx, jsonModeKey{}, jsonSchema)
}

func jsonLevelRequested(ctx context.Context) jsonLevel {
	v, _ := ctx.Value(jsonModeKey{}).(jsonLevel)
	return v
}

func jsonSchemaFrom(ctx context.Context) (jsonSchemaSpec, bool) {
	v, ok := ctx.Value(jsonSchemaKey{}).(jsonSchemaSpec)
	return v, ok && v.Name != "" && len(v.Schema) > 0
}

// jsonModeVerdictStatus — bu HTTP statüsü "response_format'ı anlamadım"
// demek için kullanılabilir mi?
//
// v0.9.526 — operatör-bildirimli değil, kod okurken bulundu: v0.9.517
// HERHANGİ bir ≥300 yanıtı yetenek kararı sayıyordu. Geçici bir 500,
// bir 429 rate-limit ya da rolling-restart penceresindeki 503, o uç
// için JSON modunu süreç ömrü boyunca kapatıyordu. Sessiz yetenek
// kaybı: kimse hata görmez, sadece JSON kaçakları geri gelir.
//
// Sadece "isteği anlamadım/işleyemedim" ailesi karar verebilir:
//
//	400 — bilinmeyen parametrede en yaygın yanıt
//	422 — FastAPI/vLLM gövde doğrulama hatası (tam bu sınıf)
//	501 — açıkça "uygulanmadı"
//
// 404 KASITLI olarak dışarıda: rota/model bulunamadı demek, JSON
// moduyla ilgisi yok — yapılandırma hatasını yetenek kararına
// çevirmek teşhisi saklardı. 5xx ve 429 zaten geçici.
func jsonModeVerdictStatus(code int) bool {
	switch code {
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusNotImplemented:
		return true
	}
	return false
}

func (s *Service) jsonModeBlocked(provider, baseURL, model string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.jsonModeUnsupported[streamVerdictKey(provider, baseURL, model)]
}

func (s *Service) markJSONModeUnsupported(provider, baseURL, model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jsonModeUnsupported == nil {
		s.jsonModeUnsupported = map[string]bool{}
	}
	s.jsonModeUnsupported[streamVerdictKey(provider, baseURL, model)] = true
}

func (s *Service) jsonSchemaBlocked(provider, baseURL, model string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.jsonSchemaUnsupported[streamVerdictKey(provider, baseURL, model)]
}

func (s *Service) markJSONSchemaUnsupported(provider, baseURL, model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jsonSchemaUnsupported == nil {
		s.jsonSchemaUnsupported = map[string]bool{}
	}
	s.jsonSchemaUnsupported[streamVerdictKey(provider, baseURL, model)] = true
}

// markLevelUnsupported — reddedilen basamağı kaydeder. Yalnız KANIT
// varken çağrılır (bir alt basamak gerçekten başardığında).
func (s *Service) markLevelUnsupported(lvl jsonLevel, provider, baseURL, model string) {
	switch lvl {
	case jsonSchema:
		s.markJSONSchemaUnsupported(provider, baseURL, model)
	case jsonObject:
		s.markJSONModeUnsupported(provider, baseURL, model)
	}
}

// ── GitHub Copilot: oturum jetonu takası ────────────────────────────────────
//
// İki adımlı çağrının DURUM tutan yarısı burada kaldı: operatörün
// GitHub OAuth jetonunu (apiKey, ghu_…) copilot_internal/v2/token
// üzerinden kısa ömürlü bir oturum jetonuyla takas eder ve sunucunun
// bildirdiği son kullanmadan ~30s öncesine kadar önbellekte tutar.
// İkinci adım (api.githubcopilot.com'a POST) internal/ai/provider'da
// (DoGitHub); çözülmüş jeton oraya Config.APIKey ile gider.

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

	resp, err := s.httpClient().Do(req)
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
	// v0.5.360 — omitempty so legacy blobs decode without the
	// field, leaving skipTLS=false (current default).
	SkipTLS bool `json:"skipTls,omitempty"`
	// wf — Enabled is a POINTER so a legacy blob saved BEFORE this
	// field existed decodes as nil, which LoadPersisted treats as
	// "enabled" (nil⇒true). A non-pointer bool would decode as
	// false and silently disable AI for every existing install on
	// upgrade. omitempty keeps the JSON clean when nil.
	Enabled *bool `json:"enabled,omitempty"`
	// v0.9.1120 (Faz 0.4) — LLM call tuning. All three follow the
	// Enabled idiom: a blob written before these fields existed decodes
	// to the zero value, and the zero value MEANS "use the default", so
	// every existing install keeps today's behaviour on upgrade.
	// Temperature is a pointer because 0 is a legitimate setting;
	// MaxTokens/TimeoutS can use omitempty ints because 0 tokens and a
	// 0s timeout are both meaningless, so 0 is free to mean "unset".
	MaxTokens   int      `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TimeoutS    int      `json:"timeoutS,omitempty"`
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
	// wf — backward-compat: a blob saved before the "enabled" field
	// existed (p.Enabled == nil) loads as enabled=true. Existing
	// installs keep AI on across the upgrade; only an explicit
	// "enabled":false from the Settings toggle disables it.
	enabled := p.Enabled == nil || *p.Enabled
	s.Configure(p.Provider, p.APIKey, p.Model, p.BaseURL, p.SkipTLS, enabled)
	// v0.9.1120 — tuning rides the SAME load, so the 30s
	// StartConfigRefresh poll propagates a knob change across pods
	// exactly like a credential change. A legacy blob leaves all three
	// at zero, which the getters read as "default".
	s.ConfigureTuning(p.MaxTokens, p.Temperature, p.TimeoutS)
	return nil
}

// StartConfigRefresh — v0.5.324. Background poll: keeps the
// in-memory Copilot config in sync with the shared persisted
// blob across pods. interval ≤ 0 → 30s.
func (s *Service) StartConfigRefresh(ctx context.Context, store SettingsStore, interval time.Duration) {
	if s == nil || store == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.LoadPersisted(ctx, store); err != nil {
				log.Printf("[copilot] config refresh: %v", err)
			}
		}
	}
}

// SavePersisted writes new credentials to system_settings AND updates
// the live Service. Called by PUT /api/settings/ai.
// v0.5.360 — skipTLS plumbed through end-to-end.
// wf — enabled persisted as a pointer (&enabled) so the round-trip is
// explicit; once SavePersisted has run the blob always carries the
// field. The disable-without-clearing-creds path is just enabled=false
// with the apiKey left untouched.
// v0.9.1120 — maxTokens/temperature/timeoutS join the blob. They are
// parameters rather than a read-modify-write of the stored blob on
// purpose: the Settings form PUTs the whole AI config, so a two-step
// write would open a lost-update window between two pods. 0 / nil for
// any of them persists as "absent" (omitempty) = use the default.
func (s *Service) SavePersisted(ctx context.Context, store SettingsStore, provider, apiKey, model, baseURL string, skipTLS, enabled bool, maxTokens int, temperature *float64, timeoutS int) error {
	raw, err := json.Marshal(persisted{
		Provider: provider, APIKey: apiKey, Model: model, BaseURL: baseURL,
		SkipTLS: skipTLS, Enabled: &enabled,
		MaxTokens: maxTokens, Temperature: temperature, TimeoutS: timeoutS,
	})
	if err != nil {
		return err
	}
	if err := store.PutSetting(ctx, settingsKey, raw); err != nil {
		return err
	}
	s.Configure(provider, apiKey, model, baseURL, skipTLS, enabled)
	s.ConfigureTuning(maxTokens, temperature, timeoutS)
	return nil
}

// TuningSnapshot returns the operator OVERRIDES, not the effective
// values: 0 / nil means "no override, running the default". The
// Settings GET echoes exactly this so the form can render the default
// as placeholder text instead of pinning it as an explicit value —
// otherwise merely opening and saving Settings would freeze today's
// defaults into the blob forever.
func (s *Service) TuningSnapshot() (maxTokens int, temperature *float64, timeoutS int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maxTokens, s.temperature, s.timeoutS
}
