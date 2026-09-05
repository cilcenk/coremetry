package copilot

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type shieldCapture struct {
	mu   sync.Mutex
	recs []CallRecord
}

func (c *shieldCapture) RecordCall(_ context.Context, r CallRecord) {
	c.mu.Lock()
	c.recs = append(c.recs, r)
	c.mu.Unlock()
}

func (c *shieldCapture) last(t *testing.T) CallRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		n := len(c.recs)
		var r CallRecord
		if n > 0 {
			r = c.recs[n-1]
		}
		c.mu.Unlock()
		if n > 0 {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("kayıt gelmedi")
	return CallRecord{}
}

// v0.10.421 — E6: CallMeta.Shield senkron çağrılır, dönen sayı
// ai_calls satırına (ShieldHits) biner; hook yoksa 0. Uçtan uca
// (gerçek Explain yolu, httptest sağlayıcı).
func TestExplainStampsShieldHits(t *testing.T) {
	body := `{"choices":[{"message":{"content":"checkout-svc ghost-service ile konuşuyor"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":7}}`
	s, done := newOpenAITestService(t, body)
	defer done()
	capt := &shieldCapture{}
	s.SetRecorder(capt)

	var gotPrompt string
	ctx := WithMeta(context.Background(), CallMeta{Surface: "t", Shield: func(prompt, answer string) uint8 {
		gotPrompt = prompt
		if strings.Contains(answer, "ghost-service") {
			return 7
		}
		return 0
	}})
	if _, err := s.Explain(ctx, "SYS", "checkout-svc yavaş"); err != nil {
		t.Fatal(err)
	}
	rec := capt.last(t)
	if rec.ShieldHits != 7 {
		t.Fatalf("ShieldHits = %d, want 7", rec.ShieldHits)
	}
	if !strings.Contains(gotPrompt, "SYS") || !strings.Contains(gotPrompt, "checkout-svc yavaş") {
		t.Fatalf("hook TAM prompt'u görmeli (sistem + kullanıcı): %q", gotPrompt)
	}

	capt2 := &shieldCapture{}
	s.SetRecorder(capt2)
	if _, err := s.Explain(WithMeta(context.Background(), CallMeta{Surface: "t"}), "SYS", "u"); err != nil {
		t.Fatal(err)
	}
	if rec := capt2.last(t); rec.ShieldHits != 0 {
		t.Fatalf("hook yokken 0 olmalı, %d", rec.ShieldHits)
	}
}
