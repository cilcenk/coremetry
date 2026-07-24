package notify

// v0.9.196 — /watchers surface, dilim ①: notifications fanned out for
// a watcher-opened problem must land in notification_log with
// related_kind="watcher" (previously every problem send logged
// "problem", making watcher sends indistinguishable on /events and
// unjoinable from the /watchers history drawer). The classifier keys
// off Problem.Metric, which evaluateWatcher→settleCountAlert stamps
// "watcher" at open time — no rule lookup on the notify path.

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestProblemRelatedKind(t *testing.T) {
	cases := []struct {
		name   string
		metric string
		want   string
	}{
		{"watcher problem", "watcher", "watcher"},
		{"metric rule problem", "error_rate", "problem"},
		{"log query watcher (saved search) stays problem", "log_query", "problem"},
		{"slo burn", "slo_burn", "problem"},
		{"empty metric (synthetic/test problems)", "", "problem"},
		{"case sensitive — Watcher is not the evaluator constant", "Watcher", "problem"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := problemRelatedKind(chstore.Problem{Metric: tc.metric})
			if got != tc.want {
				t.Fatalf("problemRelatedKind(metric=%q) = %q, want %q", tc.metric, got, tc.want)
			}
		})
	}
}
