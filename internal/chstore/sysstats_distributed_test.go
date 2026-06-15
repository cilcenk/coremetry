package chstore

import "testing"

// v0.8.165 — on an external Distributed cluster system.parts reports the
// per-shard <table>_local labels, not the bare names. The all-time switch
// in GetSystemStats matches the BARE names, so without normalisation
// SpansAllTime/LogsAllTime/MetricsAllTime/ProfilesAllTime all read 0 and
// the /admin/stats capacity view showed "0 all-time spans" on a populated
// cluster. This pins the _local strip + the all-time mapping that depends
// on it: a spans_local-labelled row must surface under "spans" and feed
// SpansAllTime.
func TestSysStats_LocalLabelsNormalizeAndMapAllTime(t *testing.T) {
	if got := normalizeStorageTableName("spans_local"); got != "spans" {
		t.Fatalf("spans_local must normalise to spans, got %q", got)
	}
	if got := normalizeStorageTableName("service_summary_5m_local"); got != "service_summary_5m" {
		t.Fatalf("MV _local table must strip its suffix, got %q", got)
	}
	if got := normalizeStorageTableName("spans"); got != "spans" {
		t.Fatalf("a bare single-node name must pass through unchanged, got %q", got)
	}

	// Simulate the scan loop: raw distributed labels, normalised in place,
	// then mapped to the all-time totals.
	raw := []TableStat{
		{Table: "spans_local", Rows: 7},
		{Table: "logs_local", Rows: 3},
		{Table: "metric_points_local", Rows: 11},
		{Table: "profiles_local", Rows: 2},
	}
	for i := range raw {
		raw[i].Table = normalizeStorageTableName(raw[i].Table)
	}
	if raw[0].Table != "spans" {
		t.Fatalf("normalised label must be the bare name for the storage panel, got %q", raw[0].Table)
	}
	spans, logs, metrics, profiles := allTimeRowCounts(raw)
	if spans != 7 || logs != 3 || metrics != 11 || profiles != 2 {
		t.Fatalf("all-time counts mismatched: spans=%d logs=%d metrics=%d profiles=%d (want 7/3/11/2)",
			spans, logs, metrics, profiles)
	}
}
