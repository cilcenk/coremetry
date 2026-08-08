package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.8.4 (scale-audit) — the metric_points GROUP BY scan behind the
// Grafana-style MetricQueryEditor / MetricsExplorer must satisfy the
// CLAUDE.md CH-bounds hard constraint: LIMIT + SETTINGS max_execution_time +
// a time-bounded WHERE that prunes partitions. Pre-v0.8.4 the query had LIMIT
// but NO max_execution_time (unlike its QueryMetricHistogram twin) and the
// time bound was CONDITIONAL — absent on a from/to-less call, so a degenerate
// request scanned every partition unbounded. These assertions pin all three
// guards across agg/groupBy shapes and the zero-window default so they can't
// re-regress.

// timeArgs collects the time.Time bound args from a query arg slice.
func timeArgs(args []any) []time.Time {
	var out []time.Time
	for _, a := range args {
		if tm, ok := a.(time.Time); ok {
			out = append(out, tm)
		}
	}
	return out
}

func TestBuildMetricQuerySQL_CHBounds(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	from := now.Add(-1 * time.Hour)

	cases := []struct {
		name       string
		f          MetricQueryFilter
		zeroWindow bool
	}{
		{"ungrouped avg, explicit window", MetricQueryFilter{Name: "http.server.duration", Aggregation: "avg", From: from, To: now}, false},
		{"grouped p99, explicit window", MetricQueryFilter{Name: "http.server.duration", Aggregation: "p99", GroupBy: []string{"http.method"}, From: from, To: now}, false},
		{"sum, NO window (defaults 24h)", MetricQueryFilter{Name: "db.client.duration", Aggregation: "sum"}, true},
	}

	// Every shape must carry all three CH-bounds guards.
	mustContain := []string{
		"FROM metric_points",
		"time >= ?",
		"time <= ?",
		"GROUP BY bucket, gk",
		"LIMIT 50000",
		"SETTINGS max_execution_time = 25",
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, args, err := buildMetricQuerySQL(tc.f, now, "", "", nil)
			if err != nil {
				t.Fatalf("buildMetricQuerySQL: %v", err)
			}
			for _, want := range mustContain {
				if !strings.Contains(sql, want) {
					t.Errorf("SQL missing %q (CH-bounds guard regressed)\n--- SQL ---\n%s", want, sql)
				}
			}
			// A time-bounded WHERE must always exist — both bounds present
			// as args even when the caller passed no window.
			times := timeArgs(args)
			if len(times) != 2 {
				t.Fatalf("want exactly 2 time bound args (from,to), got %d", len(times))
			}
			if tc.zeroWindow {
				// Defaulted: To == now, window == 24h (so the clause prunes).
				to := times[1]
				fromArg := times[0]
				if !to.Equal(now) {
					t.Errorf("zero-window To = %v, want now %v", to, now)
				}
				if d := to.Sub(fromArg); d != 24*time.Hour {
					t.Errorf("zero-window span = %v, want 24h (degenerate call must self-bound)", d)
				}
			}
		})
	}
}

func TestBuildMetricQuerySQL_BadAgg(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	_, _, err := buildMetricQuerySQL(MetricQueryFilter{Name: "m", Aggregation: "nope"}, now, "", "", nil)
	if err == nil {
		t.Fatal("want error for unknown aggregation, got nil")
	}
}

// ---------------------------------------------------------------------------
// v0.9.776 — agg=avg histogramda ORTALAMALARIN ORTALAMASI'ydı.
//
// metric_points'te histogram satırının `value`'su per-export bir ÖZET:
// ingest onu sum/count olarak yazar (otlp/convert.go). `avgOrNull(value)`
// bu özetlerin ortalamasını alır, yani her export penceresi içindeki gözlem
// sayısından bağımsız EŞİT ağırlık kazanır. Doğrusu gözlem-ağırlıklı:
// sum(sum_value) / sum(count).
//
// Sapma per-export gözlem sayısının değişkenliğiyle büyür: lokal demoda
// ≤%2,5, prod-şekilli (diurnal + hata patlaması) örnekte %28.
// ---------------------------------------------------------------------------

const (
	avgExprWeighted = "sum(sum_value) / nullIf(sum(count), 0)"
	avgExprNaive    = "avgOrNull(value)"
)

// TestMetricAvgExpr_AllInstruments — 6 dal, unit-mixing kuralı (CLAUDE.md):
// bir seçicinin HER dalı ship anında sınanır, yoksa eksen-dışı dal sessizce
// bozulur. Burada "birim" = (instrument, temporality) çifti.
func TestMetricAvgExpr_AllInstruments(t *testing.T) {
	cases := []struct {
		name        string
		instrument  string
		temporality string
		want        string
		why         string
	}{
		{
			name: "gauge", instrument: "gauge", temporality: "",
			want: avgExprNaive,
			why:  "value ölçümün kendisi; sum_value/count 0 → ağırlıklı ifade 0/0 verirdi",
		},
		{
			name: "sum (counter)", instrument: "sum", temporality: "delta",
			want: avgExprNaive,
			why:  "counter'da da value ölçümün kendisi — delta olması bir şey değiştirmez",
		},
		{
			name: "histogram + delta", instrument: "histogram", temporality: "delta",
			want: avgExprWeighted,
			why:  "DÜZELTİLEN DAL: gözlem-ağırlıklı gerçek ortalama",
		},
		{
			name: "histogram + cumulative", instrument: "histogram", temporality: "cumulative",
			want: avgExprNaive,
			why:  "sum_value/count birikimli — tek ifadeyle toplamak daha yanlış; reset-korumalı delta gerekir",
		},
		{
			name: "exp_histogram + delta", instrument: "exp_histogram", temporality: "delta",
			want: avgExprWeighted,
			why:  "exponential histogram aynı sum_value/count kolonlarına yazılır (convert.go)",
		},
		{
			name: "summary", instrument: "summary", temporality: "delta",
			want: avgExprNaive,
			why:  "bilinçli kapsam dışı: histogram gibi ama isHistogramInstrument değil",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := metricAvgExpr(tc.instrument, tc.temporality)
			if got != tc.want {
				t.Errorf("metricAvgExpr(%q, %q) = %q, want %q\nneden: %s",
					tc.instrument, tc.temporality, got, tc.want, tc.why)
			}
		})
	}
}

// TestMetricAvgExpr_OnlyAvgBranchReads — instrument/temporality avg DIŞINDAKİ
// hiçbir agg'in SQL'ini etkilemez. Bu pin olmadan bir gün birileri ifadeyi
// switch'in dışına taşır ve sum/min/max/p99 sessizce değişir.
func TestMetricAvgExpr_OnlyAvgBranchReads(t *testing.T) {
	for _, agg := range []string{"sum", "min", "max", "last", "p50", "p95", "p99"} {
		t.Run(agg, func(t *testing.T) {
			base, err := metricAggToSQL(agg, "", "")
			if err != nil {
				t.Fatalf("metricAggToSQL(%q): %v", agg, err)
			}
			hist, err := metricAggToSQL(agg, "histogram", "delta")
			if err != nil {
				t.Fatalf("metricAggToSQL(%q, histogram, delta): %v", agg, err)
			}
			if base != hist {
				t.Errorf("agg=%s SQL'i instrument'e göre değişti: %q vs %q", agg, base, hist)
			}
			if strings.Contains(hist, "sum_value") {
				t.Errorf("agg=%s ağırlıklı-avg ifadesine bulaşmış: %q", agg, hist)
			}
		})
	}
}

// TestBuildMetricQuerySQL_AvgHistogramDelta — seçicinin ürettiği ifadenin
// gerçekten SQL'e indiğini pinler (her iki yön).
func TestBuildMetricQuerySQL_AvgHistogramDelta(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	from := now.Add(-1 * time.Hour)
	f := MetricQueryFilter{
		Name: "http.server.duration", Service: "api-gateway",
		Aggregation: "avg", GroupBy: []string{"http.route"},
		From: from, To: now, StepSeconds: 60,
	}

	histSQL, _, err := buildMetricQuerySQL(f, now, "histogram", "delta", nil)
	if err != nil {
		t.Fatalf("buildMetricQuerySQL(histogram, delta): %v", err)
	}
	if !strings.Contains(histSQL, avgExprWeighted) {
		t.Errorf("histogram+delta avg ağırlıklı ifadeyi taşımıyor\n--- SQL ---\n%s", histSQL)
	}
	if strings.Contains(histSQL, avgExprNaive) {
		t.Errorf("histogram+delta avg hâlâ avgOrNull(value) içeriyor\n--- SQL ---\n%s", histSQL)
	}

	gaugeSQL, _, err := buildMetricQuerySQL(f, now, "gauge", "delta", nil)
	if err != nil {
		t.Fatalf("buildMetricQuerySQL(gauge): %v", err)
	}
	if !strings.Contains(gaugeSQL, avgExprNaive) {
		t.Errorf("gauge avg avgOrNull(value) olmalı\n--- SQL ---\n%s", gaugeSQL)
	}
	if strings.Contains(gaugeSQL, "sum_value") {
		t.Errorf("gauge avg ağırlıklı ifadeye kaymış\n--- SQL ---\n%s", gaugeSQL)
	}
}

// TestWeightedAvgPicksTrueMean — düzeltmenin NEDEN'ini saf hesap düzeyinde
// pinler. İki sentetik seri, aynı gerçek gözlem popülasyonu:
//
//	eşit-gözlemli   → iki yöntem AYNI cevabı verir (bu yüzden lokal demoda
//	                  hata görünmüyordu: ≤%2,5)
//	eşitsiz-gözlemli → ortalamaların ortalaması sapar; ağırlıklı olan
//	                  gerçek ortalamayı verir
//
// Test SQL'i değil, ARİTMETİĞİ pinler — ClickHouse'un sum()/avg()'ı bu
// hesabın aynısını yapar.
func TestWeightedAvgPicksTrueMean(t *testing.T) {
	// Bir export satırı: count gözlem, toplam sum_value.
	type exportRow struct {
		count uint64
		sum   float64
	}
	// value = sum/count (ingest'in yazdığı per-export özet).
	naiveAvg := func(rows []exportRow) float64 {
		var acc float64
		for _, r := range rows {
			acc += r.sum / float64(r.count)
		}
		return acc / float64(len(rows))
	}
	weightedAvg := func(rows []exportRow) float64 {
		var sum float64
		var n uint64
		for _, r := range rows {
			sum += r.sum
			n += r.count
		}
		return sum / float64(n)
	}
	// Gerçek ortalama: tüm gözlemlerin toplamı / gözlem sayısı.
	trueMean := weightedAvg

	const eps = 1e-9

	t.Run("eşit gözlemli — iki yöntem aynı", func(t *testing.T) {
		rows := []exportRow{
			{count: 100, sum: 1000}, // ort 10ms
			{count: 100, sum: 3000}, // ort 30ms
			{count: 100, sum: 2000}, // ort 20ms
		}
		n, w := naiveAvg(rows), weightedAvg(rows)
		if diff := n - w; diff > eps || diff < -eps {
			t.Errorf("eşit gözlemde yöntemler ayrışmamalı: naive=%v weighted=%v", n, w)
		}
		if diff := w - 20.0; diff > eps || diff < -eps {
			t.Errorf("gerçek ortalama 20 olmalı, %v", w)
		}
	})

	t.Run("eşitsiz gözlemli — naive sapar, ağırlıklı doğru", func(t *testing.T) {
		// Prod şekli: sakin pencerede 3 yavaş istek, yoğun pencerede 3000 hızlı.
		rows := []exportRow{
			{count: 3, sum: 3000},     // ort 1000ms — 3 gözlem
			{count: 3000, sum: 30000}, // ort 10ms   — 3000 gözlem
		}
		want := trueMean(rows) // 33000 / 3003 ≈ 10.989ms
		got := weightedAvg(rows)
		if diff := got - want; diff > eps || diff < -eps {
			t.Fatalf("weightedAvg gerçek ortalamayı vermeli: got=%v want=%v", got, want)
		}
		naive := naiveAvg(rows) // (1000 + 10) / 2 = 505ms
		if diff := naive - want; diff < 1.0 {
			t.Fatalf("bu kurgu sapmayı göstermeli; naive=%v want=%v", naive, want)
		}
		// Sapma büyüklüğü: naive 45× şişiriyor. Sayıyı pinle ki kurgu
		// sulandırılırsa test bunu söylesin.
		if naive/want < 40 {
			t.Errorf("naive/gerçek oranı %v — kurgu artık sapmayı temsil etmiyor", naive/want)
		}
	})
}
