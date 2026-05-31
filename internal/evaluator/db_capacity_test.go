package evaluator

import "testing"

// Feature #5 — DB capacity/saturation alerting off receiver gauges.
// capacityDecision is the pure threshold core that turns a (usage, limit)
// gauge pair (or a raw rate) into an open/severity decision. Pinning it
// here per CLAUDE.md #11 so the 90% crit / 85% warn boundaries + the
// rate-check branch can't silently drift — a broken evaluator pages
// everyone, so the threshold logic is the one piece that MUST stay exact.
func TestCapacityDecision(t *testing.T) {
	const eps = 1e-9
	tests := []struct {
		name     string
		usage    float64
		limit    float64
		rate     bool
		wantOpen bool
		wantSev  string
		wantPct  float64
	}{
		// ── usage/limit pairs (90% crit / 85% warn) ─────────────────
		{"well under threshold", 50, 100, false, false, "", 50},
		{"just under warn (84.9%)", 84.9, 100, false, false, "", 84.9},
		{"exactly at warn (85%)", 85, 100, false, true, "warning", 85},
		{"between warn and crit (88%)", 88, 100, false, true, "warning", 88},
		{"just under crit (89.9%)", 89.9, 100, false, true, "warning", 89.9},
		{"exactly at crit (90%)", 90, 100, false, true, "critical", 90},
		{"over crit (92%)", 92, 100, false, true, "critical", 92},
		{"full (100%)", 100, 100, false, true, "critical", 100},
		// Limit guards — a non-positive cap can't yield a saturation %.
		{"zero limit → no open", 50, 0, false, false, "", 0},
		{"negative limit → no open", 50, -1, false, false, "", 0},
		// Realistic Oracle tablespace numbers (bytes).
		{"tablespace 92% by bytes", 9.2e9, 1e10, false, true, "critical", 92},

		// ── rate checks (any positive rate is critical) ─────────────
		{"no evictions", 0, 0, true, false, "", 0},
		{"some evictions", 14.3, 0, true, true, "critical", 14.3},
		{"tiny eviction rate", 0.01, 0, true, true, "critical", 0.01},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			open, sev, pct := capacityDecision(tt.usage, tt.limit, tt.rate)
			if open != tt.wantOpen || sev != tt.wantSev {
				t.Errorf("capacityDecision(%v,%v,rate=%v) = (open=%v sev=%q), want (open=%v sev=%q)",
					tt.usage, tt.limit, tt.rate, open, sev, tt.wantOpen, tt.wantSev)
			}
			if pct < tt.wantPct-eps || pct > tt.wantPct+eps {
				t.Errorf("capacityDecision(%v,%v,rate=%v) pct = %v, want %v",
					tt.usage, tt.limit, tt.rate, pct, tt.wantPct)
			}
		})
	}
}

// capacityReason ships with every Problem so the oncall sees the rule
// ("ORACLE tablespace SYSAUX at 92% on corebank-scan.prod"). Pin the
// format for both the percentage and raw-rate flavours + the
// dimensioned/undimensioned subkey branches.
func TestCapacityReason(t *testing.T) {
	tests := []struct {
		name     string
		check    capacityCheck
		instance string
		subkey   string
		pct      float64
		want     string
	}{
		{"oracle tablespace (dimensioned)",
			capacityCheck{dbsys: "ORACLE", label: "tablespace"},
			"corebank-scan.prod", "SYSAUX", 92,
			"ORACLE tablespace SYSAUX at 92% on corebank-scan.prod"},
		{"oracle sessions (undimensioned)",
			capacityCheck{dbsys: "ORACLE", label: "sessions"},
			"corebank-scan.prod", "", 90,
			"ORACLE sessions at 90% on corebank-scan.prod"},
		{"postgres connections",
			capacityCheck{dbsys: "POSTGRES", label: "connections"},
			"pg-eu-1", "", 87,
			"POSTGRES connections at 87% on pg-eu-1"},
		{"redis evictions (rate)",
			capacityCheck{dbsys: "REDIS", label: "key evictions", rate: true},
			"cache-01", "", 14.3,
			"REDIS key evictions at 14.3/s on cache-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := capacityReason(tt.check, tt.instance, tt.subkey, tt.pct); got != tt.want {
				t.Errorf("capacityReason = %q, want %q", got, tt.want)
			}
		})
	}
}

// Dedup keys must be STABLE — a re-fire of the same (instance, check[,
// subkey]) has to land on the same rule_id + Problem id so the
// ReplacingMergeTree collapses it instead of opening a duplicate. The
// service column carries the instance (+subkey) so FindOpenProblem(rule,
// service) returns exactly one open row.
func TestCapacityDedupKeys(t *testing.T) {
	if got := capacityRuleID("oracle-tablespace"); got != "db-capacity:oracle-tablespace" {
		t.Errorf("capacityRuleID = %q", got)
	}
	// Dimensioned: id + service both carry the subkey.
	if got := capacityProblemID("oracle-tablespace", "corebank.prod", "SYSAUX"); got != "db-capacity:oracle-tablespace:corebank.prod:SYSAUX" {
		t.Errorf("capacityProblemID(dimensioned) = %q", got)
	}
	if got := capacityService("corebank.prod", "SYSAUX"); got != "corebank.prod·SYSAUX" {
		t.Errorf("capacityService(dimensioned) = %q", got)
	}
	// Undimensioned: no trailing separators.
	if got := capacityProblemID("oracle-sessions", "corebank.prod", ""); got != "db-capacity:oracle-sessions:corebank.prod" {
		t.Errorf("capacityProblemID(undimensioned) = %q", got)
	}
	if got := capacityService("corebank.prod", ""); got != "corebank.prod" {
		t.Errorf("capacityService(undimensioned) = %q", got)
	}
}
