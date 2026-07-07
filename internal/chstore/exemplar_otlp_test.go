package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.8.328 — series_fingerprint is the second EXPLICIT Go-written ingest
// column to become conditional (after op_group, v0.8.186): on an external
// Distributed metric_points where the ALTER never reached the shards, binding
// it would fail every metric flush with code 16. This test pins the same
// invariants the spans alignment test pins for op_group, for BOTH toggle
// states of hasSeriesFpCol:
//   - value count equals column count (the positional-alignment invariant);
//   - series_fingerprint appears in the statement iff withSeriesFp;
//   - series_fingerprint, when present, is LAST (physical-order contract —
//     the CREATE puts it after temporality via the ALTER).

func TestMetricsInsert_SeriesFingerprintAlignment(t *testing.T) {
	p := &MetricPoint{
		Metric: "app.latency", Instrument: "histogram", Description: "d", Unit: "ms",
		ServiceName: "svc", HostName: "h",
		Time: time.Unix(1, 0), StartTime: time.Unix(0, 0),
		Value: 1.5, Count: 3, SumValue: 4.5, MinValue: 0.1, MaxValue: 3,
		AttrKeys: []string{"route"}, AttrValues: []string{"/x"},
		ResKeys: []string{"service.name"}, ResValues: []string{"svc"},
		BucketBounds: []float64{0.1, 1}, BucketCounts: []uint64{1, 1, 1},
		Temporality: "delta", SeriesFingerprint: 0xDEADBEEF,
	}

	for _, withFp := range []bool{true, false} {
		sql := metricsInsertSQL(withFp)
		args := metricAppendArgs(p, withFp)

		// Column count from the statement's parenthesised list.
		open := strings.Index(sql, "(")
		if open < 0 || !strings.HasSuffix(sql, ")") {
			t.Fatalf("withFp=%v: malformed insert statement: %s", withFp, sql)
		}
		cols := strings.Split(sql[open+1:len(sql)-1], ",")
		if len(cols) != len(args) {
			t.Fatalf("withFp=%v: POSITIONAL MISALIGNMENT — %d columns vs %d values",
				withFp, len(cols), len(args))
		}

		hasFp := strings.Contains(sql, "series_fingerprint")
		if hasFp != withFp {
			t.Fatalf("withFp=%v: series_fingerprint presence in SQL = %v", withFp, hasFp)
		}
		if withFp {
			last := strings.TrimSpace(cols[len(cols)-1])
			if last != "series_fingerprint" {
				t.Fatalf("series_fingerprint must be the LAST column, got %q", last)
			}
			if got, ok := args[len(args)-1].(uint64); !ok || got != 0xDEADBEEF {
				t.Fatalf("last value must be the fingerprint, got %v", args[len(args)-1])
			}
		} else {
			if got, ok := args[len(args)-1].(string); !ok || got != "delta" {
				t.Fatalf("without fp the last value must be temporality, got %v", args[len(args)-1])
			}
		}
	}
}

// clampExemplarLimit bounds every exemplar read (house rule: LIMIT on every
// raw-table query). Default 100, cap 1000.
func TestClampExemplarLimit(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 100}, {-5, 100}, {1, 1}, {100, 100}, {999, 999}, {1000, 1000}, {5000, 1000},
	}
	for _, c := range cases {
		if got := clampExemplarLimit(c.in); got != c.want {
			t.Errorf("clampExemplarLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
