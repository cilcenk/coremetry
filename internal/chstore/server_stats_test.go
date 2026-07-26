package chstore

import "testing"

// v0.9.290 (operator ask: "can I see the ClickHouse server's memory and
// CPU utilisation too"). The mapping is split out from the query so the
// whole vocabulary is testable without a cluster — the metric names
// ClickHouse exposes shift between versions, and a rename must degrade
// to a missing number, never to a panic or a wrong one.
func TestApplyServerMetric(t *testing.T) {
	var st ServerStat
	for k, v := range map[string]float64{
		"OSMemoryTotal":           12528381952,
		"OSMemoryAvailable":       5573414912,
		"MemoryResident":          2020569088,
		"MemoryTracking":          2111371668,
		"max_server_memory_usage": 3006477107,
		"OSUserTimeNormalized":    0.81,
		"OSSystemTimeNormalized":  0.45,
		"OSIOWaitTimeNormalized":  0.04,
		"LoadAverage1":            2.15,
		"Uptime":                  70701.99,
		"Query":                   6,
		"Merge":                   4,
	} {
		applyServerMetric(&st, k, v)
	}

	if st.OSMemoryTotal != 12528381952 || st.MemoryResident != 2020569088 {
		t.Fatalf("memory counters mis-mapped: %+v", st)
	}
	if st.MaxServerMemory != 3006477107 {
		t.Fatalf("the memory ceiling a code-241 names must be surfaced, got %d", st.MaxServerMemory)
	}
	if st.RunningQueries != 6 || st.RunningMerges != 4 {
		t.Fatalf("activity counters mis-mapped: %+v", st)
	}
	if st.UptimeSec != 70701.99 || st.LoadAvg1 != 2.15 {
		t.Fatalf("float counters must not be truncated: %+v", st)
	}

	// An unknown key is a version difference, not an error.
	before := st
	applyServerMetric(&st, "SomeMetricAddedInCH25", 42)
	if st != before {
		t.Fatal("an unrecognised metric must be ignored, not folded into a field")
	}

	// Sampling artefacts must not underflow the unsigned counters —
	// uint64(-1.0) is ~18 exabytes and would render as such.
	var neg ServerStat
	applyServerMetric(&neg, "MemoryResident", -1)
	if neg.MemoryResident != 0 {
		t.Fatalf("negative reading must clamp to 0, got %d", neg.MemoryResident)
	}
}

func TestServerStatMemoryUsedPct(t *testing.T) {
	cases := []struct {
		name  string
		total uint64
		avail uint64
		want  float64
	}{
		{"half used", 100, 50, 50},
		{"fresh node", 100, 100, 0},
		{"exhausted", 100, 0, 100},
		// A node CH cannot stat reports zeroes. Unknown must read as 0%,
		// not as a fully-committed node that pages someone.
		{"unknown capacity is not full", 0, 0, 0},
		// available > total is a sampling artefact; the naive subtraction
		// underflows to ~18 exabytes and renders as a nonsense percentage.
		{"available exceeding total clamps", 100, 200, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := ServerStat{OSMemoryTotal: tc.total, OSMemoryAvailable: tc.avail}
			if got := st.MemoryUsedPct(); got != tc.want {
				t.Fatalf("MemoryUsedPct() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The three normalised CPU counters are sampled independently over the
// async-metrics interval, so their sum genuinely overshoots 1.0 — the
// live cluster showed user 0.81 + system 0.45 + iowait 0.04 = 1.31
// while load average was only 2.15. The gauge must saturate rather than
// draw a 131%-wide bar, and the UI shows the three components
// separately so the clamp never hides which one is high.
func TestServerStatCPUBusyPct(t *testing.T) {
	cases := []struct {
		name             string
		user, sys, iowat float64
		want             float64
	}{
		{"idle", 0, 0, 0, 0},
		{"quarter busy", 0.20, 0.04, 0.01, 25},
		{"saturated exactly", 0.60, 0.30, 0.10, 100},
		{"observed overshoot clamps", 0.81, 0.45, 0.04, 100},
		{"negative artefact floors", -0.5, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := ServerStat{CPUUser: tc.user, CPUSystem: tc.sys, CPUIOWait: tc.iowat}
			got := st.CPUBusyPct()
			// Tolerance, not equality: 0.60+0.30+0.10 is 0.9999999999999999
			// in float64. That is arithmetic, not a defect — pinning it
			// exactly would be pinning the rounding.
			if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("CPUBusyPct() = %v, want %v", got, tc.want)
			}
			if got < 0 || got > 100 {
				t.Fatalf("CPUBusyPct() = %v is outside 0..100 — the gauge would overflow", got)
			}
		})
	}
}
