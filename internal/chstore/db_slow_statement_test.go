package chstore

import (
	"strings"
	"testing"
)

// v0.10.325 — DB yavaş sorgu: SQL sözleşmesi + ayar normalizasyonu.
func TestSlowStatementBucketsSQLContract(t *testing.T) {
	sql := slowStatementBucketsSQL()
	for _, w := range []string{"FROM db_statement_summary_5m", "time_bucket >= ?", "HAVING execs >= ? AND p95_ms >= ?",
		"quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state)", "argMaxMerge(slow_exemplar_state)", "LIMIT 500", "max_execution_time = 10"} {
		if !strings.Contains(sql, w) {
			t.Errorf("%q yok", w)
		}
	}
	if strings.Contains(sql, "coremetry.") {
		t.Error("tablo adı nitelenmemeli")
	}
	if strings.Count(sql, "?") != 3 {
		t.Errorf("3 bind bekleniyor, %d", strings.Count(sql, "?"))
	}
}

func TestNormalizeDBSlowQuery(t *testing.T) {
	d := DefaultDBSlowQuery()
	if !d.Enabled || d.ThresholdMs != 1000 || d.CriticalMs != 5000 || d.MinExecutions != 20 || d.ForBuckets != 2 || d.CooldownSec != 900 {
		t.Fatalf("varsayılanlar spec ile uyuşmuyor: %+v", d)
	}
	n := NormalizeDBSlowQuery(DBSlowQueryConfig{Enabled: true, ThresholdMs: 2000, CriticalMs: 500, ForBuckets: 40, CooldownSec: -1})
	if n.CriticalMs != 2000 || n.ForBuckets != 12 || n.CooldownSec != 0 || n.MinExecutions != 20 {
		t.Errorf("normalize: %+v", n)
	}
	z := NormalizeDBSlowQuery(DBSlowQueryConfig{})
	if z.ThresholdMs != 1000 || z.CriticalMs != 1000 || z.ForBuckets != 2 {
		t.Errorf("sıfır blob varsayılana düşmeli: %+v", z)
	}
}
