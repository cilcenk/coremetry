package chstore

import (
	"strings"
	"testing"
)

// v0.8.243 — granularity slice B: the effective chart step never drops
// below the metric's observed export cadence (a 10s-exported gauge at
// step=1s is 90% empty buckets → sawtooth/gaps, the operator's "not as
// smooth as Grafana" complaint's second axis). Pins the interval
// estimator's branches, the raise-only clamp, and the probe SQL's
// CH-bounds contract.

func TestExportIntervalFrom(t *testing.T) {
	cases := []struct {
		name    string
		cnt     uint64
		spanSec int64
		want    int
	}{
		{"classic 10s exporter (60 pts / 590s)", 60, 590, 10},
		{"1s exporter", 600, 599, 1},
		{"15s exporter with jitter", 41, 610, 15},
		{"60s exporter", 11, 600, 60},
		{"young metric (4 pts) → no clamp", 4, 30, 0},
		{"single point → no clamp", 1, 0, 0},
		{"zero span → no clamp", 10, 0, 0},
		{"sub-second flood → floor 1s", 1000, 10, 1},
		{"implausibly sparse (>1h apart) → no clamp", 5, 20000, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := exportIntervalFrom(c.cnt, c.spanSec); got != c.want {
				t.Fatalf("exportIntervalFrom(%d, %d) = %d, want %d", c.cnt, c.spanSec, got, c.want)
			}
		})
	}
}

func TestClampStepToExportRaisesOnly(t *testing.T) {
	if got := clampStepToExport(1, 10); got != 10 {
		t.Fatalf("finer-than-export must clamp up: got %d", got)
	}
	if got := clampStepToExport(300, 10); got != 300 {
		t.Fatalf("coarse request must stay coarse: got %d", got)
	}
	if got := clampStepToExport(30, 0); got != 30 {
		t.Fatalf("unknown interval (0) must not clamp: got %d", got)
	}
}

func TestMetricExportIntervalSQLBounds(t *testing.T) {
	for _, withSvc := range []bool{false, true} {
		q := metricExportIntervalSQL(withSvc)
		for _, want := range []string{
			"time >= ?", "time <= ?", // partition-pruning window
			"LIMIT 1",
			"max_execution_time",
			"GROUP BY service_name, host_name, attr_values",
		} {
			if !strings.Contains(q, want) {
				t.Errorf("withService=%v: missing %q in %s", withSvc, want, q)
			}
		}
		if withSvc != strings.Contains(q, "service_name = ?") {
			t.Errorf("withService=%v: service filter presence wrong", withSvc)
		}
	}
}
