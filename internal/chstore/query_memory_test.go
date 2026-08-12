package chstore

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// v0.9.975 — MEASURED: Coremetry's per-query memory caps (4 GB driver
// default, 8 GB embedded in five SQL strings) were LARGER than the
// server's own max_server_memory_usage (3006477107 B = 2.8 GiB on the
// local 3584Mi ClickHouse). A cap above the server total can never
// fire: the query burns through the server-wide budget first, so what
// trips is ClickHouse's OvercommitTracker — which does NOT kill the
// query that caused the pressure, it picks a VICTIM by overcommit
// ratio. 5.4% of behaviour-engine ticks (21/389) died with code 241
// while holding ~25 MiB themselves.
//
// The invariant this pins: a per-query ceiling is min(requested,
// fraction × serverMax), and an unreadable serverMax changes NOTHING.

func TestClampQueryMemory(t *testing.T) {
	const serverMax = 3_006_477_107 // the measured local ceiling, 2.8 GiB

	cases := []struct {
		name      string
		requested int64
		serverMax int64
		fraction  float64
		want      int64
	}{
		// ── fail-open: the probe could not read the ceiling ──────────
		{"serverMax unread — driver default passes through untouched",
			defaultQueryMemory, 0, 0.6, defaultQueryMemory},
		{"serverMax unread — the 8 GB heavy-scan request survives too",
			heavyScanMemory, 0, 0.6, heavyScanMemory},
		{"serverMax negative (garbage scan) is treated as unread",
			defaultQueryMemory, -1, 0.6, defaultQueryMemory},

		// ── under the ratio: caller's number wins ────────────────────
		{"small request stays exactly as asked",
			200_000_000, serverMax, 0.6, 200_000_000},
		{"request one byte under the ratio is untouched",
			1_803_886_264 - 1, serverMax, 0.6, 1_803_886_263},

		// ── over the ratio: this is the bug being fixed ──────────────
		{"4 GB driver default on a 2.8 GiB server clamps to 60%",
			defaultQueryMemory, serverMax, 0.6, 1_803_886_264},
		{"8 GB embedded literal on a 2.8 GiB server clamps to the SAME 60%",
			heavyScanMemory, serverMax, 0.6, 1_803_886_264},
		{"a 12 GB operator override still cannot exceed the server",
			12_000_000_000, serverMax, 0.6, 1_803_886_264},

		// ── fraction normalisation ───────────────────────────────────
		{"fraction 0 means unset — default 0.6, NOT the 0.1 floor",
			defaultQueryMemory, serverMax, 0, 1_803_886_264},
		{"fraction below the floor is raised to 0.1",
			defaultQueryMemory, serverMax, 0.01, 300_647_710},
		{"fraction above the ceiling is lowered to 0.9",
			defaultQueryMemory, serverMax, 5, 2_705_829_396},
		{"negative fraction falls back to the default",
			defaultQueryMemory, serverMax, -0.5, 1_803_886_264},
		{"NaN fraction falls back to the default",
			defaultQueryMemory, serverMax, math.NaN(), 1_803_886_264},
		{"+Inf fraction is capped at 0.9, never unlimited",
			defaultQueryMemory, serverMax, math.Inf(1), 2_705_829_396},

		// ── zero is UNLIMITED to ClickHouse, never a clamp target ────
		{"requested 0 (caller has no opinion) passes through",
			0, serverMax, 0.6, 0},
		{"a 1-byte server ceiling must not clamp to 0 = unlimited",
			defaultQueryMemory, 1, 0.6, defaultQueryMemory},

		// ── a bigger server does not shrink anything ─────────────────
		{"64 GB node leaves the 8 GB heavy-scan request alone",
			heavyScanMemory, 64_000_000_000, 0.6, heavyScanMemory},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clampQueryMemory(tc.requested, tc.serverMax, tc.fraction)
			if got != tc.want {
				t.Fatalf("clampQueryMemory(%d, %d, %v) = %d, want %d",
					tc.requested, tc.serverMax, tc.fraction, got, tc.want)
			}
			// The universal invariants, checked on every row: a clamp
			// never RAISES a request, and never produces the unlimited
			// sentinel out of a bounded one.
			if tc.requested > 0 && got > tc.requested {
				t.Fatalf("clamp raised the request: %d > %d", got, tc.requested)
			}
			if tc.requested > 0 && got == 0 {
				t.Fatal("clamped to 0 — ClickHouse reads that as UNLIMITED")
			}
		})
	}
}

// The spill thresholds ride a TIGHTER ratio so a query starts writing
// to disk before it reaches the hard cap. The trap this pins: a spill
// threshold at or ABOVE the cap can never fire, so the query OOMs
// having never spilled — which is exactly the failure the spill
// settings were added (v0.8.70, v0.8.392) to prevent.
func TestClampSpillMemory(t *testing.T) {
	const serverMax = 3_006_477_107

	cases := []struct {
		name      string
		requested int64
		serverMax int64
		fraction  float64
		want      int64
	}{
		{"serverMax unread — 1 GB default spill unchanged",
			defaultExternalGroupBy, 0, 0.6, defaultExternalGroupBy},
		// The 1 GB shipped default is 33% of a 2.8 GiB node — it too
		// sat above the ratio, so the spill fired later than intended.
		{"1 GB default is 33% of a 2.8 GiB node — clamped to 25%",
			defaultExternalGroupBy, serverMax, 0.6, 751_619_276},
		{"the 2 GiB heavy-scan spill clamps to 25%",
			heavyScanSpillBytes, serverMax, 0.6, 751_619_276},
		{"a tight 0.1 query fraction drags the spill DOWN with it — " +
			"a threshold above the cap could never fire",
			heavyScanSpillBytes, serverMax, 0.1, 300_647_710},
		{"a generous 0.9 query fraction does NOT raise the spill past 25%",
			heavyScanSpillBytes, serverMax, 0.9, 751_619_276},
		{"64 GB node leaves the 2 GiB spill request alone",
			heavyScanSpillBytes, 64_000_000_000, 0.6, heavyScanSpillBytes},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clampSpillMemory(tc.requested, tc.serverMax, tc.fraction)
			if got != tc.want {
				t.Fatalf("clampSpillMemory(%d, %d, %v) = %d, want %d",
					tc.requested, tc.serverMax, tc.fraction, got, tc.want)
			}
			// The load-bearing relation: spill <= hard cap, always.
			cap := clampQueryMemory(tc.requested, tc.serverMax, tc.fraction)
			if cap > 0 && got > cap {
				t.Fatalf("spill %d above hard cap %d — it can never fire", got, cap)
			}
		})
	}
}

// resolveQueryMemory is the boot decision in one place: which of
// cfg-vs-default is the request, what the clamp does to it, and whether
// the operator gets a WARNING. Table-driven so both warning branches
// are covered without starting a process.
func TestResolveQueryMemory(t *testing.T) {
	const serverMax = 3_006_477_107
	const sixty = 1_803_886_264

	cases := []struct {
		name                           string
		cfgMax, cfgGroupBy, cfgSort    int64
		serverMax                      int64
		fraction                       float64
		wantMax, wantGroupBy, wantSort int64
		wantClamped                    bool
	}{
		{
			name:      "unconfigured on a 2.8 GiB node — the shipped default is the thing that was broken",
			serverMax: serverMax, fraction: 0,
			wantMax: sixty, wantGroupBy: 751_619_276, wantSort: 751_619_276,
			wantClamped: true,
		},
		{
			name:      "unconfigured, server ceiling unreadable — byte-identical to pre-v0.9.975",
			serverMax: 0, fraction: 0,
			wantMax: defaultQueryMemory, wantGroupBy: defaultExternalGroupBy, wantSort: defaultExternalSort,
			wantClamped: false,
		},
		{
			name:   "operator asked for LESS than the ratio — the operator still wins",
			cfgMax: 500_000_000, serverMax: serverMax, fraction: 0.6,
			wantMax: 500_000_000, wantGroupBy: 751_619_276, wantSort: 751_619_276,
			wantClamped: false,
		},
		{
			name:   "operator asked for MORE than the server has — clamped and flagged",
			cfgMax: 12_000_000_000, serverMax: serverMax, fraction: 0.6,
			wantMax: sixty, wantGroupBy: 751_619_276, wantSort: 751_619_276,
			wantClamped: true,
		},
		{
			name:   "big node, big override — nothing is touched",
			cfgMax: 12_000_000_000, cfgGroupBy: 4_000_000_000, cfgSort: 4_000_000_000,
			serverMax: 64_000_000_000, fraction: 0.6,
			wantMax: 12_000_000_000, wantGroupBy: 4_000_000_000, wantSort: 4_000_000_000,
			wantClamped: false,
		},
		{
			name:       "oversized spill overrides get the 25% ratio, not the 60% one",
			cfgGroupBy: 4_000_000_000, cfgSort: 4_000_000_000,
			serverMax: serverMax, fraction: 0.6,
			wantMax: sixty, wantGroupBy: 751_619_276, wantSort: 751_619_276,
			wantClamped: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveQueryMemory(tc.cfgMax, tc.cfgGroupBy, tc.cfgSort, tc.serverMax, tc.fraction)
			if got.MaxMemory != tc.wantMax {
				t.Errorf("MaxMemory = %d, want %d", got.MaxMemory, tc.wantMax)
			}
			if got.GroupBy != tc.wantGroupBy {
				t.Errorf("GroupBy = %d, want %d", got.GroupBy, tc.wantGroupBy)
			}
			if got.Sort != tc.wantSort {
				t.Errorf("Sort = %d, want %d", got.Sort, tc.wantSort)
			}
			if got.Clamped() != tc.wantClamped {
				t.Errorf("Clamped() = %v, want %v (configured %d, effective %d)",
					got.Clamped(), tc.wantClamped, got.Configured, got.MaxMemory)
			}
			// A plan must never hand ClickHouse the unlimited sentinel.
			if got.MaxMemory <= 0 || got.GroupBy <= 0 || got.Sort <= 0 {
				t.Fatalf("zero/negative limit in plan %+v — 0 means UNLIMITED to CH", got)
			}
		})
	}
}

// A Store built without New (every SQL-shape test, plus tooling) must
// render EXACTLY the pre-v0.9.975 bytes: the zero-value plan has
// ServerMax 0, which is the fail-open path.
func TestZeroValueStoreFailsOpen(t *testing.T) {
	var s Store
	if got := s.queryMemory(heavyScanMemory); got != heavyScanMemory {
		t.Errorf("zero-value Store clamped %d to %d — SQL-shape tests would drift", heavyScanMemory, got)
	}
	if got := s.spillMemory(heavyScanSpillBytes); got != heavyScanSpillBytes {
		t.Errorf("zero-value Store clamped spill %d to %d", heavyScanSpillBytes, got)
	}
	// EffectiveQueryMemory is the exception: it feeds a Settings map
	// directly, where 0 would mean UNLIMITED rather than "unset".
	if got := s.EffectiveQueryMemory(); got != defaultQueryMemory {
		t.Errorf("EffectiveQueryMemory() = %d, want the built-in default %d (0 = unlimited to CH)",
			got, defaultQueryMemory)
	}
	if got := s.ConfiguredQueryMemory(); got != defaultQueryMemory {
		t.Errorf("ConfiguredQueryMemory() = %d, want %d", got, defaultQueryMemory)
	}
	// And a clamped Store must actually clamp.
	s.memPlan = resolveQueryMemory(0, 0, 0, 3_006_477_107, 0.6)
	if got := s.queryMemory(heavyScanMemory); got != 1_803_886_264 {
		t.Errorf("clamped Store: queryMemory(%d) = %d, want 1803886264", heavyScanMemory, got)
	}
	if !strings.Contains(s.queryMemSetting(heavyScanMemory), "max_memory_usage = 1803886264") {
		t.Errorf("queryMemSetting rendered %q", s.queryMemSetting(heavyScanMemory))
	}
}

// Source gate: no SQL string anywhere under internal/ may pin its own
// byte count next to this setting name again.
//
// This is the regression that actually shipped — the driver default was
// fixable in one place, but five SQL strings carried private 4 GB / 8 GB
// copies that no config, env var or clamp could reach. A future edit
// that adds a sixth fails here instead of in production.
//
// The signature is assembled from fragments so the gate cannot match
// its own source text.
func TestNoEmbeddedQueryMemoryLiterals(t *testing.T) {
	needle := regexp.MustCompile(`max_memory` + `_usage\s*=\s*[0-9]`)

	root := filepath.Join("..", "..", "internal")
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Test files are excluded on purpose: pin tests legitimately
		// name the expected rendered bytes (TestHeavyScanSpill), and a
		// gate that flags its own assertions is a gate nobody keeps.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			// Comments describe the incident; they are not what CH
			// executes. Only live code can re-introduce the bug.
			if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "//") ||
				strings.HasPrefix(trimmed, "--") {
				continue
			}
			if needle.MatchString(line) {
				offenders = append(offenders, filepath.ToSlash(path)+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(offenders) > 0 {
		t.Fatalf("hardcoded per-query memory ceiling(s) found — route them through "+
			"Store.queryMemSetting so the server-ratio clamp can reach them (v0.9.975):\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
