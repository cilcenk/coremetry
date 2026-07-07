package evaluator

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.8.342 (HA audit H9) — regression: a saved-search (LogQuery) rule with
// the by-design service="" was fanned through the wildcard expansion into
// one IDENTICAL ES search per recent service (1000 services = 1000
// searches per tick; an ES brownout burned the timeout sequentially and
// stalled ALL alerting). Log-query rules are hoisted to a single call in
// evaluateAll and must never reach the per-service expansion; span-metric
// rules keep their exact expansion semantics.
func TestRuleEvalTargets(t *testing.T) {
	svcs := []string{"a", "b", "c"}

	t.Run("pinned service evaluates once against it", func(t *testing.T) {
		got := ruleEvalTargets(chstore.AlertRule{Service: "payments"}, svcs)
		if len(got) != 1 || got[0] != "payments" {
			t.Fatalf("got %v, want [payments]", got)
		}
	})

	t.Run("wildcard span-metric rule fans to every recent service", func(t *testing.T) {
		got := ruleEvalTargets(chstore.AlertRule{}, svcs)
		if len(got) != 3 {
			t.Fatalf("got %v, want all 3 services", got)
		}
	})

	t.Run("empty catalogue yields no targets (no phantom eval)", func(t *testing.T) {
		if got := ruleEvalTargets(chstore.AlertRule{}, nil); len(got) != 0 {
			t.Fatalf("got %v, want none", got)
		}
	})
}
