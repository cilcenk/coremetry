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

// v0.9.672 — TABAN ARTIK P90 (operatör-bildirimi: "kesikli çıkıyor").
//
// Eski hâli en YOĞUN seriyi alıyordu. Ölçüm (127 seri, üretim
// sorgusuyla aynı granülerlik): iv_min 27.3s · p50 45.2s · p90 76.1s ·
// max 113.9s. Eski tabanla 1/127 seri (%0.8) kapsanıyordu, yenisiyle
// 115/127 (%90.6).
func TestExportIntervalQuantile(t *testing.T) {
	cases := []struct {
		name   string
		iv     float64
		series uint64
		want   int
	}{
		{"tipik p90 (ölçülen)", 76.1, 127, 76},
		{"tek seri — dejenerasyon doğru yönde", 8, 1, 8},
		{"yuvarlama", 7.6, 10, 8},
		{"seri yok → clamp yok", 30, 0, 0},
		{"sıfır aralık → clamp yok", 0, 5, 0},
		{"negatif → clamp yok", -1, 5, 0},
		{"1s altı tabana çekiliyor", 0.4, 5, 1},
		{"tavan üstü → clamp yok (bayat/seyrek metrik)", float64(metricIvMaxSeconds + 1), 5, 0},
	}
	for _, c := range cases {
		if got := exportIntervalQuantile(c.iv, c.series); got != c.want {
			t.Errorf("%s: exportIntervalQuantile(%v, %d) = %d, beklenen %d", c.name, c.iv, c.series, got, c.want)
		}
	}
}

func TestMetricExportIntervalQuantileSQLBounds(t *testing.T) {
	for _, withSvc := range []bool{false, true} {
		q := metricExportIntervalQuantileSQL(withSvc)
		for _, want := range []string{
			"time >= ?", "time <= ?", // partition-pruning window
			"LIMIT 20000",
			"max_execution_time",
			"GROUP BY service_name, host_name, attr_values",
			// P90 ÖZELLİKLE: p50'ye (45.2s) düşürmek kapsamı yarıya indirir,
			// max'a (113.9s) çıkarmak tek seyrek seriyle tüm grafiği
			// kabalaştırır.
			"quantileExact(0.9)",
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
