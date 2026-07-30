package chstore

import (
	"strings"
	"testing"
	"time"
)

// rollup_fastpath_test.go — v0.9.412 (Rollup Aşama-3 dilim 1) pinleri:
// dar-rollup uygunluk eşleyici, kademe seçimi (step SABİT — kademe
// step'e uyar), SQL disiplini (bound + LIMIT + max_execution_time) ve
// ham yolla birebir değer semantiği (rollupAggValue).

func fpWindow(d time.Duration) (time.Time, time.Time) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	return now.Add(-d), now
}

func TestNarrowRollupEligible(t *testing.T) {
	from, to := fpWindow(6 * time.Hour)
	base := SpanMetricBatchFilter{
		From: from, To: to, StepSeconds: 60,
		Filters: []FilterExpr{
			{Key: "service.name", Op: "=", Values: []string{"checkout"}},
			{Key: "kind", Op: "IN", Values: []string{"server", "consumer"}},
		},
		Aggs: []SpanMetricAggSpec{
			{Name: "rate", Aggregation: "rate"},
			{Name: "error_rate", Aggregation: "error_rate"},
			{Name: "p99", Aggregation: "p99"},
		},
	}

	// Entry-RED şekli (bu dilimin bütün amacı) → uygun.
	q, ok := narrowRollupEligible(base)
	if !ok || len(q.conjuncts) != 2 {
		t.Fatalf("entry-RED şekli uygun olmalı: ok=%v %+v", ok, q)
	}
	if q.conjuncts[1].col != "span_kind" {
		t.Errorf("kind → span_kind eşlemeli, got %q", q.conjuncts[1].col)
	}

	mut := func(fn func(*SpanMetricBatchFilter)) SpanMetricBatchFilter {
		c := base
		c.Filters = append([]FilterExpr{}, base.Filters...)
		c.Aggs = append([]SpanMetricAggSpec{}, base.Aggs...)
		c.GroupBy = append([]string{}, base.GroupBy...)
		fn(&c)
		return c
	}

	rejects := []struct {
		name string
		f    SpanMetricBatchFilter
	}{
		{"dar-dışı filtre (http.route)", mut(func(f *SpanMetricBatchFilter) {
			f.Filters = append(f.Filters, FilterExpr{Key: "http.route", Op: "=", Values: []string{"/x"}})
		})},
		{"regex op", mut(func(f *SpanMetricBatchFilter) { f.Filters[0].Op = "=~" })},
		{"env filtresi (rollup'ta boyut yok)", mut(func(f *SpanMetricBatchFilter) {
			f.Filters = append(f.Filters, FilterExpr{Key: "deployment.environment", Op: "=", Values: []string{"prod"}})
		})},
		{"p90 (dar state'te yok)", mut(func(f *SpanMetricBatchFilter) {
			f.Aggs = append(f.Aggs, SpanMetricAggSpec{Name: "p90", Aggregation: "p90"})
		})},
		{"apdex", mut(func(f *SpanMetricBatchFilter) {
			f.Aggs = append(f.Aggs, SpanMetricAggSpec{Name: "apdex", Aggregation: "apdex"})
		})},
		{"özel alanlı avg", mut(func(f *SpanMetricBatchFilter) {
			f.Aggs = append(f.Aggs, SpanMetricAggSpec{Name: "a", Aggregation: "avg", Field: "http.status_code"})
		})},
		{"name groupBy", mut(func(f *SpanMetricBatchFilter) { f.GroupBy = []string{"name"} })},
	}
	for _, c := range rejects {
		if _, ok := narrowRollupEligible(c.f); ok {
			t.Errorf("%s: uygun OLMAMALI", c.name)
		}
	}

	// Filtresiz filo-RED + service.name groupBy → uygun (fleet overview).
	fleet := mut(func(f *SpanMetricBatchFilter) {
		f.Filters = nil
		f.GroupBy = []string{"service.name"}
	})
	if q, ok := narrowRollupEligible(fleet); !ok || len(q.groupCols) != 1 || q.groupCols[0] != "service_name" {
		t.Errorf("filo-RED uygun olmalı: ok=%v %+v", ok, q)
	}
}

func TestPickNarrowRollupTier(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		step  int
		age   time.Duration
		want  string // "" = ok=false beklenir
	}{
		// En kaba bölen kademe kazanır (en az satır).
		{"5m step 24h", 300, 24 * time.Hour, "rollup_spans_narrow_5m"},
		{"1h step 30g", 3600, 30 * 24 * time.Hour, "rollup_spans_narrow_1h"},
		// 30s'i yalnız 10s böler; 10s retention 7g.
		{"30s step 6h", 30, 6 * time.Hour, "rollup_spans_narrow_10s"},
		// 30s + 14g: 10s retention'ı kapsamıyor, 1m 30'u bölmüyor → ham yol.
		{"30s step 14g", 30, 14 * 24 * time.Hour, ""},
		// 90s'i 10s böler ama 7g dışı; 1m bölmüyor → ham yol.
		{"90s step 10g", 90, 10 * 24 * time.Hour, ""},
	}
	for _, c := range cases {
		tier, ok := pickNarrowRollupTier(c.step, now.Add(-c.age), now)
		if c.want == "" {
			if ok {
				t.Errorf("%s: ok=false beklenirdi, %s geldi", c.name, tier.table)
			}
			continue
		}
		if !ok || tier.table != c.want {
			t.Errorf("%s: %s/%v, want %s", c.name, tier.table, ok, c.want)
		}
	}
}

func TestNarrowRollupSQL(t *testing.T) {
	from, to := fpWindow(time.Hour)
	q := narrowRollupQuery{
		conjuncts: []narrowConjunct{
			{col: "service_name", values: []string{"checkout"}},
			{col: "span_kind", values: []string{"server", "consumer"}},
		},
	}
	sql, args := narrowRollupSQL("rollup_spans_narrow_1m", 60, q, true, from, to)

	for _, want := range []string{
		"INTERVAL 60 SECOND", "FROM rollup_spans_narrow_1m",
		"service_name = ?", "span_kind IN (?, ?)",
		"quantilesTDigestMerge(0.5, 0.95, 0.99)(q_state)",
		"LIMIT 50000", "max_execution_time = 15",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL %q içermeli:\n%s", want, sql)
		}
	}
	// Bind sırası: from, to, checkout, server, consumer.
	if len(args) != 5 || args[2] != "checkout" || args[4] != "consumer" {
		t.Errorf("args sırası bozuk: %v", args)
	}

	// Quantile istenmeyince merge maliyeti YOK — sabit boş dizi.
	sql2, _ := narrowRollupSQL("rollup_spans_narrow_1m", 60, q, false, from, to)
	if strings.Contains(sql2, "quantilesTDigestMerge") || !strings.Contains(sql2, "CAST([], 'Array(Float64)')") {
		t.Errorf("quantile'sız SQL merge içermemeli:\n%s", sql2)
	}

	// groupBy → gk dizisi.
	q.groupCols = []string{"service_name"}
	sql3, _ := narrowRollupSQL("rollup_spans_narrow_1m", 60, q, false, from, to)
	if !strings.Contains(sql3, "[toString(service_name)] AS gk") {
		t.Errorf("groupBy gk eksik:\n%s", sql3)
	}
}

// Ham yolun aggToSQL semantiğiyle birebir değer eşlemesi.
func TestRollupAggValue(t *testing.T) {
	qs := []float64{100e6, 400e6, 900e6} // ns: p50=100ms p95=400ms p99=900ms
	cases := []struct {
		agg  string
		want float64
	}{
		{"count", 120},
		{"rate", 2},        // 120 / 60s
		{"per_min", 120},   // 2 * 60
		{"errors", 6},
		{"error_rate", 5},  // 100 * 6/120
		{"avg", 2},         // 240e6 ns / 120 / 1e6 = 2ms
		{"sum", 240},       // 240e6 ns → ms
		{"p50", 100},
		{"p95", 400},
		{"p99", 900},
	}
	for _, c := range cases {
		if got := rollupAggValue(c.agg, 120, 6, 240e6, qs, 60); got != c.want {
			t.Errorf("%s = %v, want %v", c.agg, got, c.want)
		}
	}
	// calls=0 koruması (bölme paniği yok).
	if got := rollupAggValue("error_rate", 0, 0, 0, nil, 60); got != 0 {
		t.Errorf("error_rate calls=0 → 0, got %v", got)
	}
}
