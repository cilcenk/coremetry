package copilot

// v0.10.397 — CoSRE denetimi O1: sohbet satırlarının duration_ms'i 0'dı;
// RecordUsage artık başlangıç zamanını alır. Sıfır zaman = ölçülmemiş (0).

import (
	"context"
	"testing"
	"time"
)

func TestRecordUsageCarriesDuration(t *testing.T) {
	s := New("openai", "kd", "m")
	rec := &captureRecorder{}
	s.SetRecorder(rec)
	ctx := WithMeta(context.Background(), CallMeta{Surface: "chat"})
	// RecordCall ayrı goroutine'de koşar; kısa bir bekleme ile okunur.
	wait := func(want func(CallRecord) bool) CallRecord {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if r := rec.last(); want(r) {
				return r
			}
			time.Sleep(5 * time.Millisecond)
		}
		return rec.last()
	}
	s.RecordUsage(ctx, time.Now().Add(-1500*time.Millisecond), 10, 20, "ok", "", "p", "r")
	if got := wait(func(r CallRecord) bool { return r.InputTokens == 10 }).DurationMs; got < 1500 || got > 60_000 {
		t.Fatalf("DurationMs = %d, want ≥1500", got)
	}
	s.RecordUsage(ctx, time.Time{}, 1, 1, "ok", "", "p", "r")
	if got := wait(func(r CallRecord) bool { return r.InputTokens == 1 }).DurationMs; got != 0 {
		t.Fatalf("zero start must record 0, got %d", got)
	}
}
