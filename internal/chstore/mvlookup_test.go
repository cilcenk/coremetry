package chstore

import (
	"strings"
	"testing"
)

// mvlookup_test.go — guards the v0.8.52 fix for the index-shift bug. The
// drop+recreate upgrade migrations used to reference the mvs slice by hardcoded
// position (mvs[3]/mvs[4] for the db_summary MVs). Doorway D1 (v0.8.50)
// inserted three spanmetrics tiers near the top of the slice, shifting those
// positions — so a pre-v0.5.327 upgrade would have dropped db_summary_5m and
// recreated the WRONG MV. mvDDLByName looks MVs up by name; this test pins that
// it returns the exact CREATE and never prefix-collides
// (spanmetrics_1 vs spanmetrics_1m/1s; trace_summary_5m vs trace_summary_1d).

func TestMVDDLByName(t *testing.T) {
	// A slice shaped like the real one — spanmetrics tiers BEFORE the db MVs,
	// exactly the ordering that broke the positional lookup.
	mvs := []string{
		"CREATE MATERIALIZED VIEW IF NOT EXISTS service_summary_5m\n ENGINE = AggregatingMergeTree AS SELECT 1",
		"CREATE MATERIALIZED VIEW IF NOT EXISTS spanmetrics_1m\n ENGINE = AggregatingMergeTree AS SELECT 2",
		"CREATE MATERIALIZED VIEW IF NOT EXISTS spanmetrics_10s\n ENGINE = AggregatingMergeTree AS SELECT 3",
		"CREATE MATERIALIZED VIEW IF NOT EXISTS spanmetrics_1s\n ENGINE = AggregatingMergeTree AS SELECT 4",
		"CREATE MATERIALIZED VIEW IF NOT EXISTS trace_summary_1d\n ENGINE = AggregatingMergeTree AS SELECT 5",
		"CREATE MATERIALIZED VIEW IF NOT EXISTS db_summary_5m\n ENGINE = AggregatingMergeTree AS SELECT 6",
		"CREATE MATERIALIZED VIEW IF NOT EXISTS db_caller_summary_5m\n ENGINE = AggregatingMergeTree AS SELECT 7",
		"CREATE MATERIALIZED VIEW IF NOT EXISTS trace_summary_5m\n ENGINE = AggregatingMergeTree AS SELECT 13",
	}

	// Each name must resolve to its OWN CREATE (the marker SELECT number).
	want := map[string]string{
		"service_summary_5m":   "SELECT 1",
		"spanmetrics_1m":       "SELECT 2",
		"spanmetrics_10s":      "SELECT 3",
		"spanmetrics_1s":       "SELECT 4",
		"db_summary_5m":        "SELECT 6",
		"db_caller_summary_5m": "SELECT 7",
		"trace_summary_5m":     "SELECT 13",
	}
	for name, marker := range want {
		got := mvDDLByName(mvs, name)
		if got == "" {
			t.Errorf("mvDDLByName(%q) returned empty", name)
			continue
		}
		if !strings.Contains(got, "IF NOT EXISTS "+name+"\n") {
			t.Errorf("mvDDLByName(%q) returned the wrong CREATE: %q", name, got)
		}
		if !strings.HasSuffix(got, marker) {
			t.Errorf("mvDDLByName(%q) = %q, want marker %q", name, got, marker)
		}
	}

	// Prefix-collision guard: "spanmetrics_1" must NOT match spanmetrics_1m/1s.
	if got := mvDDLByName(mvs, "spanmetrics_1"); got != "" {
		t.Errorf("mvDDLByName(spanmetrics_1) must not prefix-match a tier; got %q", got)
	}
	// Absent name → "".
	if got := mvDDLByName(mvs, "nonexistent_5m"); got != "" {
		t.Errorf("mvDDLByName(nonexistent_5m) = %q, want empty", got)
	}
}
