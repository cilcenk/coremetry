// tuning_test.go — v0.9.1120 (AI Assistant Faz 0.4): operator-tunable
// LLM call parameters (maxTokens / temperature / timeoutS).
//
// THE regression this guards is a PARITY BUG that shipped for ~1000
// releases: the completion budget was lifted 1024 → 4096 for the
// openai-compat paths (v0.8.138 / v0.8.393) but the lift never reached
//   - explainAnthropic            (provider_calls.go)
//   - explainGitHub               (provider_calls.go)
//   - streamAnthropicWithUsage    (stream.go)
//
// so an operator on Anthropic — the provider most likely to be paid
// per token — got explanations silently truncated at 1024 tokens,
// worst of all on the STREAMING path where a truncated answer just
// stops and looks finished. temperature had the mirror-image gap: 0.2
// on openai/github, ABSENT from every anthropic body (provider default
// ≈1.0), so the same question answered differently depending on which
// provider the install happened to be pointed at.
//
// TestRequestBodyTuning_AllProviders is the pin: it captures the REAL
// request body of every provider path through an injected
// RoundTripper and asserts the budget + temperature that actually go
// on the wire. It is deliberately body-level rather than a source grep
// — a source pin would pass the day someone reintroduces a literal
// through a different spelling.
package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ─── persisted round-trip ───────────────────────────────────────────

// TestPersistedTuning_Decode pins the "unset ⇒ default" contract on the
// blob itself, including the LEGACY blob (no tuning fields at all) —
// the same idiom the `enabled` *bool established: an upgrade must not
// change behaviour for an install that never touched these knobs.
func TestPersistedTuning_Decode(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name     string
		blob     string
		wantMT   int
		wantTemp *float64
		wantTO   int
	}{
		{
			name:   "legacy blob without tuning fields decodes to defaults",
			blob:   `{"provider":"anthropic","apiKey":"sk-x","model":"claude-sonnet-4-6"}`,
			wantMT: 0, wantTemp: nil, wantTO: 0,
		},
		{
			name:   "legacy blob with enabled but no tuning",
			blob:   `{"provider":"openai","apiKey":"","model":"m","baseUrl":"http://x/v1","skipTls":true,"enabled":false}`,
			wantMT: 0, wantTemp: nil, wantTO: 0,
		},
		{
			name:   "all three set",
			blob:   `{"provider":"anthropic","maxTokens":8192,"temperature":0.7,"timeoutS":300}`,
			wantMT: 8192, wantTemp: f(0.7), wantTO: 300,
		},
		{
			name: "temperature 0 is a VALUE, not absence",
			blob: `{"provider":"anthropic","temperature":0}`,
			// The pointer must be non-nil: a plain float64 field could
			// not tell "operator asked for fully deterministic" apart
			// from "operator said nothing".
			wantMT: 0, wantTemp: f(0), wantTO: 0,
		},
		{
			name:   "partial — only the timeout raised",
			blob:   `{"provider":"openai","timeoutS":600}`,
			wantMT: 0, wantTemp: nil, wantTO: 600,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var p persisted
			if err := json.Unmarshal([]byte(tc.blob), &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if p.MaxTokens != tc.wantMT {
				t.Errorf("MaxTokens = %d, want %d", p.MaxTokens, tc.wantMT)
			}
			if p.TimeoutS != tc.wantTO {
				t.Errorf("TimeoutS = %d, want %d", p.TimeoutS, tc.wantTO)
			}
			switch {
			case tc.wantTemp == nil && p.Temperature != nil:
				t.Errorf("Temperature = %v, want nil (unset)", *p.Temperature)
			case tc.wantTemp != nil && p.Temperature == nil:
				t.Errorf("Temperature = nil, want %v", *tc.wantTemp)
			case tc.wantTemp != nil && *p.Temperature != *tc.wantTemp:
				t.Errorf("Temperature = %v, want %v", *p.Temperature, *tc.wantTemp)
			}
		})
	}
}

// TestPersistedTuning_RoundTrip pins SavePersisted → JSON →
// LoadPersisted for the knobs, and — the important half — that the
// UNSET state stays unset on disk (omitempty) instead of being frozen
// into the blob as today's defaults.
func TestPersistedTuning_RoundTrip(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name         string
		mt           int
		temp         *float64
		to           int
		wantOnDisk   []string // substrings the stored JSON MUST contain
		wantAbsent   []string // substrings it must NOT contain
		wantEffTok   int
		wantEffTemp  float64
		wantEffTimeo time.Duration
	}{
		{
			name: "unset stays absent on disk and runs the defaults",
			mt:   0, temp: nil, to: 0,
			wantAbsent:  []string{"maxTokens", "temperature", "timeoutS"},
			wantEffTok:  4096,
			wantEffTemp: 0.2,
			// The default is what buildCopilotHTTPClient has always used.
			wantEffTimeo: 180 * time.Second,
		},
		{
			name: "all three persisted and applied",
			mt:   8192, temp: f(0.9), to: 45,
			wantOnDisk:   []string{`"maxTokens":8192`, `"temperature":0.9`, `"timeoutS":45`},
			wantEffTok:   8192,
			wantEffTemp:  0.9,
			wantEffTimeo: 45 * time.Second,
		},
		{
			name: "temperature 0 survives the round trip",
			mt:   0, temp: f(0), to: 0,
			// omitempty on a *float64 keys off the POINTER, not the
			// value, so an explicit 0 is written.
			wantOnDisk:   []string{`"temperature":0`},
			wantEffTok:   4096,
			wantEffTemp:  0,
			wantEffTimeo: 180 * time.Second,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemStore()
			saver := New("anthropic", "", "")
			if err := saver.SavePersisted(context.Background(), store,
				"anthropic", "sk-ant", "claude-sonnet-4-6", "", false, true,
				tc.mt, tc.temp, tc.to, nil, ""); err != nil {
				t.Fatalf("SavePersisted: %v", err)
			}
			disk := string(store.m[settingsKey])
			for _, want := range tc.wantOnDisk {
				if !strings.Contains(disk, want) {
					t.Errorf("stored blob missing %s; got %s", want, disk)
				}
			}
			for _, bad := range tc.wantAbsent {
				if strings.Contains(disk, bad) {
					t.Errorf("stored blob should not pin %s (unset must stay unset); got %s", bad, disk)
				}
			}

			// A fresh Service must reach the same effective values from
			// the blob alone — this is the cross-pod path
			// (StartConfigRefresh → LoadPersisted).
			loaded := New("anthropic", "", "")
			if err := loaded.LoadPersisted(context.Background(), store); err != nil {
				t.Fatalf("LoadPersisted: %v", err)
			}
			if got := loaded.tuneMaxTokens(); got != tc.wantEffTok {
				t.Errorf("tuneMaxTokens() = %d, want %d", got, tc.wantEffTok)
			}
			if got, ok := loaded.tuneTemperature(); !ok || got != tc.wantEffTemp {
				t.Errorf("tuneTemperature() = (%v,%v), want (%v,true)", got, ok, tc.wantEffTemp)
			}
			if got := loaded.clientTimeout(); got != tc.wantEffTimeo {
				t.Errorf("clientTimeout() = %v, want %v", got, tc.wantEffTimeo)
			}
		})
	}
}

// ─── getters ────────────────────────────────────────────────────────

func TestTuningGetters(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name     string
		mt       int
		temp     *float64
		to       int
		wantTok  int
		wantTemp float64
		wantOK   bool
		wantTimo time.Duration
	}{
		{"all unset → defaults", 0, nil, 0, 4096, 0.2, true, 180 * time.Second},
		{"override wins", 2048, f(1.0), 30, 2048, 1.0, true, 30 * time.Second},
		{"temperature 0 is an override, not absence", 0, f(0), 0, 4096, 0, true, 180 * time.Second},
		{"negative/nonsense falls back", -5, nil, -1, 4096, 0.2, true, 180 * time.Second},
		{"one knob set does not disturb the others", 0, nil, 600, 4096, 0.2, true, 600 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New("anthropic", "k", "m")
			s.ConfigureTuning(tc.mt, tc.temp, tc.to)
			if got := s.tuneMaxTokens(); got != tc.wantTok {
				t.Errorf("tuneMaxTokens() = %d, want %d", got, tc.wantTok)
			}
			gotT, ok := s.tuneTemperature()
			if ok != tc.wantOK || gotT != tc.wantTemp {
				t.Errorf("tuneTemperature() = (%v,%v), want (%v,%v)", gotT, ok, tc.wantTemp, tc.wantOK)
			}
			if got := s.clientTimeout(); got != tc.wantTimo {
				t.Errorf("clientTimeout() = %v, want %v", got, tc.wantTimo)
			}
		})
	}
}

func TestValidateTuning(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name    string
		mt      int
		temp    *float64
		to      int
		wantErr bool
	}{
		{"all unset is always valid", 0, nil, 0, false},
		{"lower bounds", 256, f(0), 10, false},
		{"upper bounds", 32768, f(2), 600, false},
		{"maxTokens below floor", 255, nil, 0, true},
		{"maxTokens above ceiling", 32769, nil, 0, true},
		{"maxTokens negative", -1, nil, 0, true},
		{"temperature below floor", 0, f(-0.1), 0, true},
		{"temperature above ceiling", 0, f(2.1), 0, true},
		{"timeout below floor", 0, nil, 9, true},
		{"timeout above ceiling", 0, nil, 601, true},
		{"timeout negative", 0, nil, -1, true},
		{"typical operator values", 8192, f(0.4), 300, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTuning(tc.mt, tc.temp, tc.to)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateTuning(%d,%v,%d) err = %v, wantErr = %v", tc.mt, tc.temp, tc.to, err, tc.wantErr)
			}
		})
	}
}

// ─── the parity pin: what actually goes on the wire ─────────────────

// captureRT records every outbound request body and answers with a
// canned, provider-shaped 200. Injected straight into s.cli (white-box,
// same package) because the anthropic and github paths hard-code their
// API host and can't be pointed at an httptest server.
type captureRT struct{ bodies []map[string]any }

func (c *captureRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil && m != nil {
			c.bodies = append(c.bodies, m)
		}
	}
	body := `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`
	switch {
	case strings.Contains(req.URL.Host, "api.github.com"):
		// GitHub Copilot session-token exchange (GET, no body).
		body = `{"token":"tid=sess","expires_at":99999999999}`
	case strings.Contains(req.URL.Host, "api.anthropic.com"):
		body = `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":2}}`
	}
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		Request:    req,
	}, nil
}

// TestRequestBodyTuning_AllProviders is the regression pin for the
// 1024 parity bug (v0.9.1120). Every provider path must put the SAME
// budget and the SAME temperature on the wire — the default (4096 /
// 0.2) when nothing is configured, and the operator's values when they
// are. Failure mode it catches: a request builder holding its own
// literal again.
func TestRequestBodyTuning_AllProviders(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	// Each case exercises one request builder end-to-end.
	paths := []struct {
		name     string
		provider string
		baseURL  string
		run      func(s *Service)
	}{
		{"openai explain", ProviderOpenAI, "http://llm.invalid/v1", func(s *Service) {
			_, _, _, _ = s.explainOpenAI(context.Background(), "sys", "user")
		}},
		{"openai stream", ProviderOpenAI, "http://llm.invalid/v1", func(s *Service) {
			_, _, _, _ = s.streamOpenAIWithUsage(context.Background(), "sys", "user", nil)
		}},
		{"openai chat (tools)", ProviderOpenAI, "http://llm.invalid/v1", func(s *Service) {
			_, _ = s.chatOpenAIWithTools(context.Background(), "sys",
				[]ChatMessage{{Role: "user", Text: "hi"}}, nil)
		}},
		{"anthropic explain", ProviderAnthropic, "", func(s *Service) {
			_, _, _, _ = s.explainAnthropic(context.Background(), "sys", "user")
		}},
		{"anthropic stream", ProviderAnthropic, "", func(s *Service) {
			_, _, _, _ = s.streamAnthropicWithUsage(context.Background(), "sys", "user", nil)
		}},
		{"anthropic chat (tools)", ProviderAnthropic, "", func(s *Service) {
			_, _ = s.chatAnthropicWithTools(context.Background(), "sys",
				[]ChatMessage{{Role: "user", Text: "hi"}}, nil)
		}},
		{"github explain", ProviderGitHub, "", func(s *Service) {
			_, _, _, _ = s.explainGitHub(context.Background(), "sys", "user")
		}},
	}
	tunings := []struct {
		name     string
		mt       int
		temp     *float64
		wantTok  float64
		wantTemp float64
	}{
		{"defaults", 0, nil, 4096, 0.2},
		{"operator override", 8192, f(0.9), 8192, 0.9},
		{"deterministic (temperature 0)", 0, f(0), 4096, 0},
	}
	for _, tn := range tunings {
		for _, p := range paths {
			t.Run(tn.name+"/"+p.name, func(t *testing.T) {
				s := New(p.provider, "key", "model-x")
				s.Configure(p.provider, "key", "model-x", p.baseURL, false, true)
				s.ConfigureTuning(tn.mt, tn.temp, 0)
				rt := &captureRT{}
				s.mu.Lock()
				s.cli = &http.Client{Transport: rt}
				s.mu.Unlock()

				p.run(s)

				if len(rt.bodies) == 0 {
					t.Fatalf("no request body captured — the path never called the provider")
				}
				// Assert on EVERY captured body: a streaming path that
				// falls back to the buffered twin issues a second request,
				// and that one must carry the same knobs.
				for i, b := range rt.bodies {
					mt, ok := b["max_tokens"].(float64)
					if !ok {
						t.Fatalf("body[%d] has no numeric max_tokens: %v", i, b)
					}
					if mt != tn.wantTok {
						t.Errorf("body[%d] max_tokens = %v, want %v", i, mt, tn.wantTok)
					}
					// v0.10.253 (prompt audit D1): Anthropic gövdesine temperature
					// HİÇ binmez (Opus 4.7+/5, Sonnet 5, Fable'da 400); openai-compat
					// ve github yolları tuning'i taşımaya devam eder.
					if p.provider == "anthropic" {
						if tv, has := b["temperature"]; has {
							t.Errorf("body[%d] anthropic temperature taşıyor (%v) — D1: gönderilmez", i, tv)
						}
						continue
					}
					tv, ok := b["temperature"].(float64)
					if !ok {
						t.Fatalf("body[%d] has no numeric temperature: %v", i, b)
					}
					if tv != tn.wantTemp {
						t.Errorf("body[%d] temperature = %v, want %v", i, tv, tn.wantTemp)
					}
				}
			})
		}
	}
}

// ─── client rebuild ─────────────────────────────────────────────────

// TestClientRebuildOnTuningChange pins that a timeout change actually
// REACHES the shared http.Client. Without it the setting would be
// accepted, echoed back by GET /api/settings/ai, and silently not
// applied — the fail-open-silently-unapplies failure mode.
func TestClientRebuildOnTuningChange(t *testing.T) {
	s := New("openai", "", "m")
	if got := s.httpClient().Timeout; got != 180*time.Second {
		t.Fatalf("fresh Service timeout = %v, want 180s", got)
	}

	s.ConfigureTuning(0, nil, 45)
	if got := s.httpClient().Timeout; got != 45*time.Second {
		t.Fatalf("after ConfigureTuning(45) timeout = %v, want 45s", got)
	}

	// Idempotent: re-applying the same value must not churn the client
	// (StartConfigRefresh re-applies the blob every 30s).
	before := s.httpClient()
	s.ConfigureTuning(0, nil, 45)
	if s.httpClient() != before {
		t.Errorf("re-applying an unchanged timeout rebuilt the client")
	}

	// Back to unset ⇒ back to the default.
	s.ConfigureTuning(0, nil, 0)
	if got := s.httpClient().Timeout; got != 180*time.Second {
		t.Errorf("clearing the override left timeout = %v, want 180s", got)
	}

	// A credential change must PRESERVE the tuning timeout: Configure
	// and ConfigureTuning write the same client, and whichever runs
	// second must not clobber the other's input.
	s.ConfigureTuning(0, nil, 300)
	s.Configure("openai", "k", "m", "http://x/v1", true, true)
	if got := s.httpClient().Timeout; got != 300*time.Second {
		t.Errorf("Configure() reset the tuned timeout to %v, want 300s", got)
	}
	// …and the TLS-skip flip must have survived the tuning rebuild.
	if !skipTLSOf(s.httpClient()) {
		t.Errorf("skipTLS lost after Configure(); the rebuild used a stale flag")
	}
	s.ConfigureTuning(0, nil, 60)
	if !skipTLSOf(s.httpClient()) {
		t.Errorf("a tuning-only rebuild dropped skipTLS")
	}
}

func skipTLSOf(c *http.Client) bool {
	tr, ok := c.Transport.(*http.Transport)
	return ok && tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify
}
