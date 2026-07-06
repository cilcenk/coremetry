package evaluator

import (
	"testing"
	"time"
)

// v0.8.309 — regression: the critical-page storm. Two refresh paths
// (db-capacity `open && hasOpen` and anomaly promotion's existing-open
// branch) recomputed Severity from the LIVE gauge every tick, silently
// undoing what escalateStaleProblems had raised. Each tick then repeated:
// refresh resets critical→warning → escalation sees warning + open≥30m →
// bumps to critical AND re-fires SendProblemAlert. A tablespace parked at
// 87% paged the critical channels every 60s for hours.
//
// Contract of effectiveSeverity: a refresh may lower severity only down to
// the AGE-BASED escalation floor — the same thresholds nextSeverity uses —
// so once a problem's age has escalated it, no gauge recompute can dip
// below that tier and the escalation sweep never finds a mismatch to
// re-fire on.
func TestEffectiveSeverity(t *testing.T) {
	cases := []struct {
		name     string
		computed string
		openFor  time.Duration
		want     string
	}{
		{"young warning stays warning", "warning", 10 * time.Minute, "warning"},
		{"warning past the 30m floor reads critical", "warning", 35 * time.Minute, "critical"},
		{"warning exactly at the floor reads critical", "warning", 30 * time.Minute, "critical"},
		{"young info stays info", "info", 5 * time.Minute, "info"},
		{"info past 15m floors to warning", "info", 20 * time.Minute, "warning"},
		{"info past 30m floors to critical", "info", 45 * time.Minute, "critical"},
		{"critical is already the ceiling", "critical", 5 * time.Hour, "critical"},
		{"case-insensitive like nextSeverity", "Warning", 40 * time.Minute, "critical"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := effectiveSeverity(c.computed, c.openFor); got != c.want {
				t.Fatalf("effectiveSeverity(%q, %s) = %q, want %q", c.computed, c.openFor, got, c.want)
			}
		})
	}
}
