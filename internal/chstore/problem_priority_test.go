package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.8.321 — regression: the P1/P2 "×threshold" reason text printed the RAW
// Value/Threshold ratio, while bigBreach correctly used the FLIPPED ratio
// for below-threshold ("<"/"<=") rules. An uptime rule (value 40 vs
// threshold 99) ranked P1 correctly but the operator-facing tooltip read
// "critical + 0.4x threshold" instead of ~2.5x — an inverted magnitude on
// every less-than rule, serialized into the cached /api/problems payloads.
func TestComputePriorityReasonUsesFlippedRatio(t *testing.T) {
	now := time.Now().UnixNano()
	fresh := now - int64(10*time.Minute) // young problem: no stale-critical path

	t.Run("below-threshold breach reports the flipped magnitude", func(t *testing.T) {
		p := Problem{Severity: "critical", Value: 40, Threshold: 99, Status: "open", StartedAt: fresh}
		pri, reason := computePriority(p, now)
		if pri != "P1" {
			t.Fatalf("priority = %s, want P1", pri)
		}
		if !strings.Contains(reason, "2.5x") {
			t.Fatalf("reason %q must carry the flipped ~2.5x magnitude, not the raw 0.4x", reason)
		}
	})

	t.Run("above-threshold breach text unchanged", func(t *testing.T) {
		p := Problem{Severity: "warning", Value: 30, Threshold: 10, Status: "open", StartedAt: fresh}
		pri, reason := computePriority(p, now)
		if pri != "P2" || !strings.Contains(reason, "3.0x") {
			t.Fatalf("got (%s, %q), want P2 with 3.0x", pri, reason)
		}
	})

	t.Run("zero threshold still falls back to severity alone", func(t *testing.T) {
		p := Problem{Severity: "critical", Value: 5, Threshold: 0, Status: "open", StartedAt: fresh}
		pri, _ := computePriority(p, now)
		if pri != "P2" {
			t.Fatalf("priority = %s, want P2 (no ratio computable)", pri)
		}
	})
}
