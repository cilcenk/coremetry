package chstore

import (
	"strings"
	"testing"
)

// influx_status_test.go — v0.10.223 (Influx D2 durum ucu). CLAUDE.md CH
// kuralı: metric_points sorgusu zaman sınırlı WHERE + LIMIT +
// max_execution_time taşır; kaynak listesi bind-arg (SQL'e ad gömülmez);
// yalnız dış metrikler (`ext:` öneki) sayılır — bir kaynağın adı yanlışlıkla
// gerçek bir servisle çakışsa bile o servisin OTLP metrikleri karışmaz.

func TestInfluxIngestStatusSQL_Bounds(t *testing.T) {
	sql := influxIngestStatusSQL(3)
	for _, needle := range []string{
		"FROM metric_points",
		"service_name IN (?,?,?)",
		"metric LIKE 'ext:%'",
		"time >= now() - INTERVAL 1 HOUR",
		"GROUP BY service_name",
		"LIMIT 100",
		"max_execution_time = 10",
	} {
		if !strings.Contains(sql, needle) {
			t.Fatalf("status SQL lost %q:\n%s", needle, sql)
		}
	}
	if strings.Contains(sql, "coremetry.") {
		t.Fatalf("telemetry tables are referenced UNQUALIFIED:\n%s", sql)
	}
	if strings.Contains(influxIngestStatusSQL(1), "?,?") {
		t.Fatalf("single source → single placeholder")
	}
}
