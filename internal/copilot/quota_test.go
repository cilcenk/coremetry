package copilot

// v0.9.200 — provider-kota devre kesici (operator-reported: test ortamında
// Gemini free-tier 429'u; arka-plan explainer kotayı tüketip interaktif
// analizi açlığa bırakıyordu). isQuotaErr sınıflandırması + kesici pencere
// davranışı pinli: kota-olmayan hatalar kesiciyi ASLA açmaz.

import (
	"errors"
	"testing"
	"time"
)

func TestIsQuotaErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"gemini 429 quota", errors.New(`openai-compat 429: {"error":{"code":429,"message":"You exceeded your current quota"}}`), true},
		{"openai rate limit", errors.New("Rate limit reached for gpt-4"), true},
		{"google resource exhausted", errors.New("rpc error: code = ResourceExhausted desc = RESOURCE_EXHAUSTED"), true},
		{"too many requests", errors.New("HTTP 429 Too Many Requests"), true},
		{"timeout is NOT quota", errors.New("context deadline exceeded"), false},
		{"5xx is NOT quota", errors.New("openai-compat 500: internal error"), false},
		{"connection refused NOT quota", errors.New("dial tcp: connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isQuotaErr(tc.err); got != tc.want {
				t.Fatalf("isQuotaErr=%v want %v (%v)", got, tc.want, tc.err)
			}
		})
	}
}

func TestQuotaBackoffWindow(t *testing.T) {
	s := &Service{}
	if s.QuotaBackoffActive() {
		t.Fatal("fresh service must not be in backoff")
	}
	s.noteProviderError(errors.New("timeout")) // kota değil → kesici kapalı
	if s.QuotaBackoffActive() {
		t.Fatal("non-quota error must not arm the breaker")
	}
	s.noteProviderError(errors.New("provider 429: quota exceeded"))
	if !s.QuotaBackoffActive() {
		t.Fatal("quota error must arm the breaker")
	}
	// Pencere ~1 saat ileri damgalanır.
	s.mu.RLock()
	until := s.quotaUntil
	s.mu.RUnlock()
	if d := time.Until(until); d < 55*time.Minute || d > 65*time.Minute {
		t.Fatalf("backoff window ≈1h bekleniyordu, got %v", d)
	}
	// Süre geçince kendiliğinden kapanır.
	s.mu.Lock()
	s.quotaUntil = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if s.QuotaBackoffActive() {
		t.Fatal("expired window must auto-close")
	}
}
