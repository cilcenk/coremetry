package chstore

// external_seasonal_test.go — v0.10.231 (Influx D6): mevsimsel okuma SQL
// şekli ve bind sırası. Şekil iddiaları: zaman sınırı, anahtar öneki
// (service_name + metric), gün sınıfı, gece-yarısı sarmalı uzaklık,
// LIMIT BY + LIMIT + max_execution_time, metric_points (MV değil — dış
// seri 1-dk gauge, MV'de yok).

import (
	"strings"
	"testing"
	"time"
)

func TestExternalSeasonalSQL_Shape(t *testing.T) {
	sql := externalSeasonalSQL("[attr_values[indexOf(attr_keys, ?)]]")
	for _, want := range []string{
		"FROM metric_points",
		"service_name = ? AND metric = ?",
		"time >= ? AND time < ?",
		"'saturday'", "'sunday'", "'weekday'",
		"86400 - abs(",
		"LIMIT 2000 BY key",
		"LIMIT 400000",
		"max_execution_time = 10",
		"toStartOfMinute(time)",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL lacks %q:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "coremetry.") {
		t.Error("telemetry table must be unqualified")
	}
}

func TestExternalSeasonalArgs_Order(t *testing.T) {
	cutoff := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	upper := cutoff.Add(24 * time.Hour)
	req := ExternalSeasonalReq{Metric: "ext:x", Service: "ggfail", Cutoff: cutoff, Upper: upper, Class: "weekday", TargetSod: 36000, RadiusSec: 900}
	got := externalSeasonalArgs(req, []any{"OPERATIONCODE", "ERRORCODE"})
	want := []any{"OPERATIONCODE", "ERRORCODE", "ggfail", "ext:x", cutoff, upper, "weekday", 36000, 36000, 900}
	if len(got) != len(want) {
		t.Fatalf("len %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d: %v want %v", i, got[i], want[i])
		}
	}
}
