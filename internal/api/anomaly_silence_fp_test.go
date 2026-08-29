package api

// anomaly_silence_fp_test.go — v0.10.162: susturma parmak izi kanonik sha1
// (chstore.FingerprintAnomaly) olmalı; düz `kind|pattern|service` metni
// tüketicilerle (api.go getTraceOpAnomalies/getLogPatternAnomalies,
// evaluator muted[ev.ID]) asla eşleşmiyordu.

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestSilenceFingerprintCanonical(t *testing.T) {
	raw := "trace_op|POST /v1/charges|payments-orchestrator"
	got := silenceFingerprint(raw, "trace_op", "POST /v1/charges", "payments-orchestrator")
	want := chstore.FingerprintAnomaly("trace_op", "POST /v1/charges", "payments-orchestrator")
	if got != want || got == raw {
		t.Fatalf("fingerprint = %q, want canonical sha1 %q", got, want)
	}
	// desensiz (Cmd-K gibi) çağıran gönderdiğini korur
	if got := silenceFingerprint("abc123", "trace_op", "", "svc"); got != "abc123" {
		t.Fatalf("patternless silence must keep raw fingerprint, got %q", got)
	}
}
