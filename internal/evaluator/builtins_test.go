package evaluator

import (
	"strings"
	"testing"
)

// v0.8.262 — built-in alert rule hardening (operator request:
// "gerçek alertler oluşsun, default gelsin"). Every default rule
// must ship with the full anti-noise kit — MinSamples (v0.5.128),
// ForSec (v0.5.126), CooldownSec (v0.5.129) — and the two-tier
// shape must stay coherent: for each surface the warning tier sits
// below the critical threshold with an equal-or-longer window.
// These invariants pin the slice so a future edit can't silently
// ship a default rule that flaps on a 3-span window again.
func TestBuiltinRules_HardeningInvariants(t *testing.T) {
	if len(builtins) == 0 {
		t.Fatal("builtins slice is empty — fresh installs would ship no default alerts")
	}

	deprecated := make(map[string]bool)
	for _, id := range deprecatedBuiltinIDs {
		deprecated[id] = true
	}

	seen := make(map[string]bool)
	for _, r := range builtins {
		if r.ID == "" || !strings.HasPrefix(r.ID, "builtin-") {
			t.Errorf("%q: builtin ids must carry the builtin- prefix", r.ID)
		}
		if seen[r.ID] {
			t.Errorf("%s: duplicate builtin id", r.ID)
		}
		seen[r.ID] = true
		if deprecated[r.ID] {
			t.Errorf("%s: id is also in deprecatedBuiltinIDs — boot would seed then disable it", r.ID)
		}
		if !r.BuiltIn || !r.Enabled {
			t.Errorf("%s: builtins must ship BuiltIn+Enabled (got builtIn=%v enabled=%v)", r.ID, r.BuiltIn, r.Enabled)
		}
		if r.Severity != "critical" && r.Severity != "warning" {
			t.Errorf("%s: unexpected severity %q", r.ID, r.Severity)
		}
		if r.Threshold <= 0 || r.WindowSec == 0 {
			t.Errorf("%s: threshold/window must be positive", r.ID)
		}
		// The anti-noise kit — the whole point of the hardening.
		if r.MinSamples == 0 {
			t.Errorf("%s: MinSamples=0 — a 1-request window could fire this rule", r.ID)
		}
		if r.ForSec == 0 {
			t.Errorf("%s: ForSec=0 — a single spiky bucket would open a problem", r.ID)
		}
		if r.CooldownSec == 0 {
			t.Errorf("%s: CooldownSec=0 — boundary jitter would flap re-opens", r.ID)
		}
		// Rate/percentile metrics must actually be gated: the
		// MinSamples gate only applies to sample-dependent metrics.
		if !metricNeedsSampleFloor(r.Metric) {
			t.Errorf("%s: metric %q is not sample-floor gated — MinSamples would be ignored", r.ID, r.Metric)
		}
	}

	// Two-tier coherence per surface: warning below critical,
	// warning window ≥ critical window.
	surfaces := []struct {
		name     string
		critical string
		warning  string
	}{
		{"error_rate", "builtin-error-rate-15pct", "builtin-warn-error-rate-5pct"},
		{"http_p99", "builtin-http-p99-5s", "builtin-warn-http-p99-3s"},
		{"db_p99", "builtin-db-p99-5s", "builtin-warn-db-p99-2500ms"},
		{"mq_consume_p99", "builtin-mq-consume-p99-2m", "builtin-warn-mq-consume-p99-30s"},
	}
	byID := make(map[string]int)
	for i, r := range builtins {
		byID[r.ID] = i
	}
	for _, s := range surfaces {
		ci, cok := byID[s.critical]
		wi, wok := byID[s.warning]
		if !cok || !wok {
			t.Errorf("%s: surface must ship BOTH tiers (critical=%v warning=%v)", s.name, cok, wok)
			continue
		}
		crit, warn := builtins[ci], builtins[wi]
		if crit.Severity != "critical" || warn.Severity != "warning" {
			t.Errorf("%s: tier severities inverted", s.name)
		}
		if crit.Metric != warn.Metric {
			t.Errorf("%s: tiers watch different metrics (%q vs %q)", s.name, crit.Metric, warn.Metric)
		}
		if warn.Threshold >= crit.Threshold {
			t.Errorf("%s: warning threshold %.0f must sit below critical %.0f", s.name, warn.Threshold, crit.Threshold)
		}
		if warn.WindowSec < crit.WindowSec {
			t.Errorf("%s: warning window %ds must be ≥ critical window %ds (sustained tier)", s.name, warn.WindowSec, crit.WindowSec)
		}
	}
}
