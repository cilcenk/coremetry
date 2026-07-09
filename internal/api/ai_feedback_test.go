package api

import (
	"encoding/hex"
	"strings"
	"testing"
)

// v0.8.399 — AI answer feedback (thumbs up/down). Pins the pure
// request gate for POST /api/ai/feedback: verdict ∈ {-1, 1},
// exchangeId non-empty and ≤ 64 chars, and — the integration
// contract — every id the chat handler mints (newRandID(16), the
// value emitted in the SSE answer event) must pass the gate, or
// legitimate thumbs clicks would 400.
func TestValidateAIFeedback(t *testing.T) {
	long := strings.Repeat("a", aiFeedbackMaxIDLen+1)
	exact := strings.Repeat("b", aiFeedbackMaxIDLen)
	cases := []struct {
		name       string
		exchangeID string
		verdict    int8
		wantErr    bool
	}{
		{"thumbs up", "0123456789abcdef0123456789abcdef", 1, false},
		{"thumbs down", "0123456789abcdef0123456789abcdef", -1, false},
		{"empty id", "", 1, true},
		{"whitespace id", "   ", 1, true},
		{"id at max length ok", exact, -1, false},
		{"id over max length", long, 1, true},
		{"verdict zero", "0123456789abcdef", 0, true},
		{"verdict two", "0123456789abcdef", 2, true},
		{"verdict minus two", "0123456789abcdef", -2, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAIFeedback(tc.exchangeID, tc.verdict)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateAIFeedback(%q, %d) err=%v, wantErr=%t",
					tc.exchangeID, tc.verdict, err, tc.wantErr)
			}
		})
	}
}

// v0.8.399 — server-minted exchange ids (the only ids a well-behaved
// client ever posts back) always pass validation and are hex, so the
// answer-event → feedback-POST loop can't reject its own ids.
func TestAIFeedbackAcceptsMintedExchangeIDs(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := newRandID(16)
		if len(id) != 32 {
			t.Fatalf("newRandID(16) len = %d, want 32", len(id))
		}
		if _, err := hex.DecodeString(id); err != nil {
			t.Fatalf("newRandID(16) = %q is not hex: %v", id, err)
		}
		if err := validateAIFeedback(id, 1); err != nil {
			t.Fatalf("minted id %q rejected: %v", id, err)
		}
	}
}
