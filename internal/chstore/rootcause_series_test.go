package chstore

import (
	"math"
	"strings"
	"testing"
	"time"
)

// rootcause_series_test.go — v0.10.700. Zamansal çarpanın seri okuması:
// MV + sınırlar + tavanlar (anomaly_batch_test.go kalıbı) ve grid hizası
// (eksik kova NaN, aralık dışı düşer).
func TestBuildErrorRateSeriesQueryShape(t *testing.T) {
	sql := buildErrorRateSeriesQuery(3)
	for label, sub := range map[string]string{
		"MV read":              "FROM service_summary_5m",
		"IN list":              "service_name IN (?, ?, ?)",
		"lower bound":          "time_bucket >= ?",
		"upper bound":          "time_bucket < ?",
		"per-service cap":      "LIMIT 300 BY service_name",
		"overall cap":          "LIMIT 5000",
		"execution-time bound": "max_execution_time = 10",
		"deterministic order":  "ORDER BY service_name, t",
		"error rate pct":       "countMerge(error_count_state) / nullIf(countMerge(span_count_state), 0) * 100",
	} {
		if !strings.Contains(sql, sub) {
			t.Errorf("%s eksik: %q", label, sub)
		}
	}
	if strings.Contains(sql, "FROM spans") {
		t.Error("ham spans taranmamalı")
	}
	if strings.Count(sql, "?") != 5 {
		t.Errorf("3 servis + 2 sınır = 5 yer tutucu, %d", strings.Count(sql, "?"))
	}
}

func TestBoundSeriesServices(t *testing.T) {
	in := []string{" a ", "", "b", "a", "c"}
	got := boundSeriesServices(in)
	if strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("tekrarsız/boşsuz/sıralı: %v", got)
	}
	many := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		many = append(many, "s"+string(rune('a'+i)))
	}
	if n := len(boundSeriesServices(many)); n != temporalSeriesMaxServices {
		t.Fatalf("tavan %d, %d", temporalSeriesMaxServices, n)
	}
}

func TestAlignSeriesGrid(t *testing.T) {
	start := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	end := start.Add(20 * time.Minute) // 4 kova
	pts := map[string]map[int64]float64{
		"a": {start.Unix(): 1, start.Add(10 * time.Minute).Unix(): 3, start.Add(25 * time.Minute).Unix(): 99, start.Add(-5 * time.Minute).Unix(): 98},
	}
	got := alignSeriesGrid([]string{"a", "b"}, pts, start, end)
	a := got["a"]
	if len(a) != 4 || a[0] != 1 || !math.IsNaN(a[1]) || a[2] != 3 || !math.IsNaN(a[3]) {
		t.Fatalf("grid hizası: %v", a)
	}
	b := got["b"]
	if len(b) != 4 || !math.IsNaN(b[0]) || !math.IsNaN(b[3]) {
		t.Fatalf("noktasız servis tamamen NaN olmalı: %v", b)
	}
	if len(alignSeriesGrid([]string{"a"}, pts, end, start)) != 0 {
		t.Fatal("ters pencere boş")
	}
}
