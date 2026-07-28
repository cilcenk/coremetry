package evaluator

import (
	"os"
	"strings"
	"testing"
)

// anomaly_mute_test.go — v0.9.337.
//
// Found by the anomaly feedback-loop design round (all three designs were
// rejected; this was the one thing worth shipping out of it).
//
// Muting an anomaly writes a row into anomaly_silences that ONLY two list
// endpoints ever read (api.go:10132 getTraceOpAnomalies, :10236
// getLogPatternAnomalies). promoteStrongAnomalies never consulted it, so the
// gesture hid the fingerprint from two lists and changed nothing else: it
// still became a Problem, still attached to an incident, still paged. The
// operator gave an explicit instruction and the loudest path ignored it.
//
// This is deliberately NOT the feedback loop that round was asked to design.
// There is no history, no score, no decay, nothing learned — one operator
// gesture with an expiry the store already enforces. That is why it is safe
// where all three learned designs were not: they inferred suppression from
// past behaviour and could therefore silence an incident nobody asked to
// silence.
func TestPromotionHonoursMute(t *testing.T) {
	src := mustReadEvaluatorSource(t, "evaluator.go")

	i := strings.Index(src, "func (e *Evaluator) promoteStrongAnomalies")
	if i < 0 {
		t.Fatal("promoteStrongAnomalies not found")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j > 0 {
		body = body[:j]
	}

	if !strings.Contains(body, "ActiveSilencedFingerprints(ctx)") {
		t.Error("the promotion sweep must consult the mute list — otherwise muting only hides the row from two lists while it keeps paging")
	}
	if !strings.Contains(body, "muted[ev.ID]") {
		t.Error("the mute must be matched on ev.ID — that IS the fingerprint the silence was written under (recorder.go builds it with FingerprintAnomaly)")
	}
	// Failing CLOSED here would let a ClickHouse blip silently stop every
	// promotion. The worst case of failing open is noise the operator
	// already has; the worst case of failing closed is a missed page.
	if !strings.Contains(body, "muted = nil") {
		t.Error("a silence-list error must fail OPEN (promote unfiltered), never suppress")
	}
	// A suppressed promotion is a decision, and "why did this not page" has
	// to be answerable from the logs.
	if !strings.Contains(body, "SKIPPED (muted)") {
		t.Error("a mute-suppressed promotion must be logged, not silent")
	}
}

func mustReadEvaluatorSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
