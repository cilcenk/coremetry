package chstore

import (
	"os"
	"strings"
	"testing"
	"time"
)

// v0.9.712 — aile-C okuyucu uygunluk tablosu.
//
// En kritik satırlar CUMULATIVE'inkiler: 0003 şeması attr serilerini
// birleştirir, argMax çok-serili sayaçta "son yazan seri"yi seçer —
// oradan rate KURULAMAZ. Bir refactor bu satırları yeşilletmeye
// çalışırsa şemayı da değiştirmek zorunda (0008 adayı, spec notu).

func TestMetricRollupPlan(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	base := MetricQueryFilter{
		Name: "jvm.memory.used", Service: "billing",
		From: now.Add(-2 * time.Hour), To: now, StepSeconds: 60,
	}
	mod := func(f MetricQueryFilter, m func(*MetricQueryFilter)) MetricQueryFilter { m(&f); return f }

	cases := []struct {
		name        string
		f           MetricQueryFilter
		instrument  string
		temporality string
		wantTable   string // "" = ham
	}{
		{"gauge avg 60s → 1m", base, "gauge", "", "rollup_metrics_1m"},
		{"gauge, step 300 → 5m", mod(base, func(f *MetricQueryFilter) { f.StepSeconds = 300 }), "gauge", "", "rollup_metrics_5m"},
		{"gauge, step 3600 → 1h (kaba kademe kazanır)", mod(base, func(f *MetricQueryFilter) { f.StepSeconds = 3600 }), "gauge", "", "rollup_metrics_1h"},
		{"delta sum → 1m", mod(base, func(f *MetricQueryFilter) { f.Aggregation = "sum" }), "sum", "delta", "rollup_metrics_1m"},

		// ── HAM kalması ZORUNLU olanlar ──
		{"CUMULATIVE ASLA", mod(base, func(f *MetricQueryFilter) { f.Aggregation = "rate" }), "sum", "cumulative", ""},
		{"cumulative sum agg'i bile ASLA", mod(base, func(f *MetricQueryFilter) { f.Aggregation = "sum" }), "sum", "cumulative", ""},
		{"histogram bu dilimde ham", base, "histogram", "", ""},
		{"attr filtresi → ham (rollup attr taşımıyor)", mod(base, func(f *MetricQueryFilter) {
			f.Filters = []FilterExpr{{Key: "route", Op: "=", Values: []string{"/x"}}}
		}), "gauge", "", ""},
		{"groupBy → ham", mod(base, func(f *MetricQueryFilter) { f.GroupBy = []string{"route"} }), "gauge", "", ""},
		{"instance scoping → ham", mod(base, func(f *MetricQueryFilter) { f.Instance = "db1"; f.Engine = "oracle" }), "gauge", "", ""},
		{"bölünmeyen step (90s) → ham", mod(base, func(f *MetricQueryFilter) { f.StepSeconds = 90 }), "gauge", "", ""},
		{"auto-step (0) bu dilimde ham", mod(base, func(f *MetricQueryFilter) { f.StepSeconds = 0 }), "gauge", "", ""},
		{"gauge p95 → ham (value-quantile noktalar ister)", mod(base, func(f *MetricQueryFilter) { f.Aggregation = "p95" }), "gauge", "", ""},
		{"TTL dışı pencere → ham", mod(base, func(f *MetricQueryFilter) {
			f.From = now.Add(-20 * 24 * time.Hour)
		}), "gauge", "", ""}, // 20g: 1m TTL(14g) aşıldı, step 60 5m'i bölmüyor→... 60%300!=0 → ham
		{"TTL dışı ama 5m bölünür → 5m", mod(base, func(f *MetricQueryFilter) {
			f.From = now.Add(-20 * 24 * time.Hour)
			f.StepSeconds = 300
		}), "gauge", "", "rollup_metrics_5m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, ok := metricRollupPlan(tc.f, tc.instrument, tc.temporality, now, nil)
			got := ""
			if ok {
				got = tr.table
			}
			if got != tc.wantTable {
				t.Fatalf("tablo = %q, beklenen %q", got, tc.wantTable)
			}
		})
	}
}

// Kademe sabitleri 0003 migration'la HİZALI kalmalı — migration SQL'i
// tek gerçek; Go sabitleri ondan saparsa okuma yanlış TTL varsayar.
func TestMetricRollupTiersMatchMigration(t *testing.T) {
	sql := readMigration(t, "0003_rollup_metrics.sql")
	for _, tr := range metricRollupTiers {
		if !containsAll(sql, "CREATE TABLE IF NOT EXISTS "+tr.table+"_local") {
			t.Errorf("%s migration'da yok", tr.table)
		}
	}
	for tbl, ttl := range map[string]string{
		"rollup_metrics_1m_local": "TTL ts + INTERVAL 14 DAY",
		"rollup_metrics_5m_local": "TTL ts + INTERVAL 90 DAY",
		"rollup_metrics_1h_local": "TTL ts + INTERVAL 13 MONTH",
	} {
		if !containsAll(sql, tbl, ttl) {
			t.Errorf("%s için %q migration'da bulunamadı", tbl, ttl)
		}
	}
}

func readMigration(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../migrations/" + name)
	if err != nil {
		t.Fatalf("%s okunamadı: %v", name, err)
	}
	return string(b)
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// v0.10.261 (CDV-5) — auto-step çözümü rollup planlarından ÖNCE gelmeli;
// aksi hâlde StepSeconds=0 her planı "ham"a düşürür (kaynak sırası pini).
func TestAutoStepResolvedBeforeRollupPlans(t *testing.T) {
	src, err := os.ReadFile("metricquery.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	fn := body[strings.Index(body, "func (s *Store) QueryMetric("):]
	step := strings.Index(fn, "f.StepSeconds = metricAutoStepPx(")
	route := strings.Index(fn, "s.tryMetricRollupRoute(ctx, f)")
	plan := strings.Index(fn, "metricRollupPlan(f, instrument, temporality")
	if step < 0 || route < 0 || plan < 0 {
		t.Fatalf("çapalar bulunamadı: step=%d route=%d plan=%d", step, route, plan)
	}
	if step > route || step > plan {
		t.Fatalf("auto-step (%d) rollup planlarından (%d, %d) SONRA çözülüyor — planlar StepSeconds=0 görür", step, route, plan)
	}
}
