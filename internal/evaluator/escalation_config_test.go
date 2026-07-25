package evaluator

import (
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.248 regression pin — the age-escalation windows were hard-coded
// 15 min / 30 min. Operator-reported: tightening anomaly promotion to
// get fewer pages barely helped, because whatever did get through
// escalated ITSELF to critical half an hour later and re-fired the
// notify channel on the way.
//
// What must hold forever after:
//  1. Disabled means disabled — no severity ever changes, at any age.
//     This is the whole point of the switch; a stray escalation with
//     it off is the bug that would make the setting a lie.
//  2. The windows actually come from config, not the old constants.
//  3. An `info` Problem can never reach critical before it has passed
//     the warning window, however the config is shaped.
func TestNextSeverityUsesConfig(t *testing.T) {
	def := chstore.DefaultProblemEscalation()

	t.Run("disabled never escalates", func(t *testing.T) {
		off := chstore.ProblemEscalationConfig{
			Enabled: false, InfoToWarningSec: 60, WarningToCriticalSec: 120,
		}
		for _, sev := range []string{"info", "warning", "critical"} {
			for _, age := range []time.Duration{0, time.Hour, 30 * 24 * time.Hour} {
				if got := nextSeverity(sev, age, off); got != "" {
					t.Errorf("nextSeverity(%q, %s, disabled) = %q, want \"\"", sev, age, got)
				}
			}
		}
	})

	t.Run("defaults reproduce the old 15/30 ladder", func(t *testing.T) {
		for _, tc := range []struct {
			sev  string
			age  time.Duration
			want string
		}{
			{"info", 14 * time.Minute, ""},
			{"info", 15 * time.Minute, "warning"},
			{"info", 29 * time.Minute, "warning"},
			{"info", 30 * time.Minute, "critical"},
			{"warning", 29 * time.Minute, ""},
			{"warning", 30 * time.Minute, "critical"},
			{"critical", 100 * time.Hour, ""},
		} {
			if got := nextSeverity(tc.sev, tc.age, def); got != tc.want {
				t.Errorf("nextSeverity(%q, %s) = %q, want %q", tc.sev, tc.age, got, tc.want)
			}
		}
	})

	t.Run("custom windows are honoured", func(t *testing.T) {
		// A fleet where "open for an hour" is normal.
		slow := chstore.ProblemEscalationConfig{
			Enabled: true, InfoToWarningSec: 4 * 3600, WarningToCriticalSec: 12 * 3600,
		}
		if got := nextSeverity("warning", 40*time.Minute, slow); got != "" {
			t.Errorf("40min under a 12h window escalated to %q — windows not read from config", got)
		}
		if got := nextSeverity("warning", 12*time.Hour, slow); got != "critical" {
			t.Errorf("12h under a 12h window = %q, want critical", got)
		}
		if got := nextSeverity("info", 4*time.Hour, slow); got != "warning" {
			t.Errorf("4h under a 4h window = %q, want warning", got)
		}
	})

	t.Run("info never skips warning", func(t *testing.T) {
		// Deliberately inverted config — Normalize must reorder it so
		// the two windows collapse rather than letting info jump to
		// critical while it is still below the warning window.
		bad := chstore.ProblemEscalationConfig{
			Enabled: true, InfoToWarningSec: 3600, WarningToCriticalSec: 60,
		}
		n := chstore.NormalizeProblemEscalation(bad)
		if n.WarningToCriticalSec < n.InfoToWarningSec {
			t.Fatalf("normalize left critical (%ds) before warning (%ds)",
				n.WarningToCriticalSec, n.InfoToWarningSec)
		}
		// Below both windows: nothing happens.
		if got := nextSeverity("info", 30*time.Minute, bad); got != "" {
			t.Errorf("info at 30min under an inverted config = %q, want \"\"", got)
		}
	})

	t.Run("zero windows fall back to defaults, not instant escalation", func(t *testing.T) {
		// An `int` can't tell "absent" from "explicit 0"; 0 must mean
		// the default, otherwise a partial PUT would page instantly.
		zero := chstore.ProblemEscalationConfig{Enabled: true}
		if got := nextSeverity("warning", time.Second, zero); got != "" {
			t.Errorf("1s with zero windows = %q, want \"\" (0 must mean default)", got)
		}
		if got := nextSeverity("warning", 30*time.Minute, zero); got != "critical" {
			t.Errorf("30min with zero windows = %q, want critical (default ladder)", got)
		}
	})
}
