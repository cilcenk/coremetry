// enabled_test.go — wf: AI Copilot enable/disable toggle.
//
// The toggle lets the operator turn AI Copilot OFF (stops the
// background ProblemExplainer hammering the provider + hides the AI
// affordances + 503s the AI endpoints) WITHOUT clearing the stored
// creds, so re-enabling is one click. Active() == enabled AND
// configured; Configured() still means "has creds" regardless of the
// toggle.
//
// THE regression this guards: a persisted blob saved BEFORE the
// "enabled" field existed must load as enabled=true. The field is a
// *bool precisely so a missing field decodes as nil (⇒true) rather
// than the zero value false — a non-pointer bool would have silently
// disabled AI for every existing install on upgrade. These tests
// would catch that regression (legacy blob → Active() true) and pin
// the enabled=false round-trip.
package copilot

import (
	"context"
	"encoding/json"
	"testing"
)

// memStore is a minimal in-memory SettingsStore for the persistence
// round-trip tests. Keyed by setting name.
type memStore struct{ m map[string][]byte }

func newMemStore() *memStore { return &memStore{m: map[string][]byte{}} }

func (s *memStore) GetSetting(_ context.Context, key string) ([]byte, error) {
	return s.m[key], nil
}
func (s *memStore) PutSetting(_ context.Context, key string, value []byte) error {
	s.m[key] = append([]byte(nil), value...)
	return nil
}

func TestLoadPersisted_EnabledBackwardCompat(t *testing.T) {
	tests := []struct {
		name        string
		blob        string // raw system_settings JSON for "ai_copilot"
		wantEnabled bool   // expected Snapshot() enabled
		wantActive  bool   // expected Active() (enabled AND configured)
	}{
		{
			// THE backward-compat case: a blob written before the
			// "enabled" field existed. nil ⇒ true. Creds present →
			// Active() must be true (AI stays on across upgrade).
			name:        "legacy blob without enabled field, creds present",
			blob:        `{"provider":"anthropic","apiKey":"sk-ant-legacy","model":"claude-sonnet-4-6"}`,
			wantEnabled: true,
			wantActive:  true,
		},
		{
			// Legacy blob, no creds → enabled=true but not configured,
			// so Active() is false (nothing to call).
			name:        "legacy blob without enabled field, no creds",
			blob:        `{"provider":"anthropic","apiKey":"","model":""}`,
			wantEnabled: true,
			wantActive:  false,
		},
		{
			// Explicit enabled:true with creds → Active true.
			name:        "explicit enabled true, creds present",
			blob:        `{"provider":"anthropic","apiKey":"sk-ant-x","enabled":true}`,
			wantEnabled: true,
			wantActive:  true,
		},
		{
			// Explicit enabled:false with creds present → the operator
			// disabled AI WITHOUT clearing the key. Configured() stays
			// true, Active() goes false.
			name:        "explicit enabled false, creds kept",
			blob:        `{"provider":"anthropic","apiKey":"sk-ant-x","enabled":false}`,
			wantEnabled: false,
			wantActive:  false,
		},
		{
			// openai local endpoint, no key, legacy blob → configured
			// via baseURL alone, enabled defaults true → Active true.
			name:        "openai baseURL no key, legacy blob",
			blob:        `{"provider":"openai","apiKey":"","baseUrl":"http://ollama:11434/v1"}`,
			wantEnabled: true,
			wantActive:  true,
		},
		{
			// openai local endpoint disabled → Active false even though
			// baseURL would otherwise make it configured.
			name:        "openai baseURL no key, disabled",
			blob:        `{"provider":"openai","apiKey":"","baseUrl":"http://ollama:11434/v1","enabled":false}`,
			wantEnabled: false,
			wantActive:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemStore()
			if err := store.PutSetting(context.Background(), settingsKey, []byte(tc.blob)); err != nil {
				t.Fatalf("seed store: %v", err)
			}
			// Start from a service that's been explicitly DISABLED so a
			// no-op LoadPersisted couldn't accidentally produce the
			// "enabled" answer. The persisted blob must drive the result.
			s := New("anthropic", "", "")
			s.Configure("anthropic", "", "", "", false, false)
			if err := s.LoadPersisted(context.Background(), store); err != nil {
				t.Fatalf("LoadPersisted: %v", err)
			}
			_, _, _, _, _, gotEnabled := s.Snapshot()
			if gotEnabled != tc.wantEnabled {
				t.Errorf("enabled = %v, want %v", gotEnabled, tc.wantEnabled)
			}
			if got := s.Active(); got != tc.wantActive {
				t.Errorf("Active() = %v, want %v", got, tc.wantActive)
			}
		})
	}
}

// TestSavePersisted_EnabledRoundTrip pins that enabled=false survives a
// SavePersisted → JSON → LoadPersisted cycle AND that the on-disk JSON
// actually carries the field (a *bool with omitempty would drop a true
// pointer-to-false? no — &false is non-nil so it serializes; this guards
// against a future refactor back to a value type that omitempty-drops).
func TestSavePersisted_EnabledRoundTrip(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		enabled := enabled
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			store := newMemStore()
			saver := New("anthropic", "", "")
			// Save creds + the toggle. Note creds stay non-empty even
			// when disabled — that's the whole feature.
			if err := saver.SavePersisted(context.Background(), store,
				"anthropic", "sk-ant-roundtrip", "claude-sonnet-4-6", "", false, enabled); err != nil {
				t.Fatalf("SavePersisted: %v", err)
			}

			// The persisted JSON must carry an explicit "enabled" field
			// for BOTH true and false (pointer to bool, non-nil → always
			// serialized). Decode it to confirm.
			var p persisted
			if err := json.Unmarshal(store.m[settingsKey], &p); err != nil {
				t.Fatalf("unmarshal persisted: %v", err)
			}
			if p.Enabled == nil {
				t.Fatalf("persisted JSON dropped the enabled field; got %s", store.m[settingsKey])
			}
			if *p.Enabled != enabled {
				t.Errorf("persisted enabled = %v, want %v", *p.Enabled, enabled)
			}
			// Creds must be preserved regardless of the toggle.
			if p.APIKey == "" {
				t.Errorf("disable cleared the stored key; want it kept")
			}

			// Load into a fresh service and confirm Active() matches:
			// disabled ⇒ not active even though creds are present.
			loaded := New("anthropic", "", "")
			loaded.Configure("anthropic", "", "", "", false, true) // start enabled to prove the blob drives it
			if err := loaded.LoadPersisted(context.Background(), store); err != nil {
				t.Fatalf("LoadPersisted: %v", err)
			}
			if got := loaded.Active(); got != enabled {
				t.Errorf("after round-trip Active() = %v, want %v", got, enabled)
			}
			// Configured() must stay true either way — creds were kept.
			if !loaded.Configured() {
				t.Errorf("Configured() = false after round-trip; the key should be kept regardless of the toggle")
			}
		})
	}
}
