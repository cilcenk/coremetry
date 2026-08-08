package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.9.751 — histogram yüzdelikleri rollup'tan. Pinler: uygunluk
// (delta histogram + filtresiz; cumulative ASLA), SQL disiplini,
// kanonik-bounds atlama politikası (ham yol metrichist.go ile birebir).

func histF(step int, ageHours float64) MetricQueryFilter {
	now := time.Now()
	return MetricQueryFilter{
		Name: "http.server.request.duration", Service: "svc",
		From: now.Add(-time.Duration(ageHours * float64(time.Hour))), To: now,
		StepSeconds: step,
	}
}

func TestMetricRollupHistPlan(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		f         MetricQueryFilter
		inst, tmp string
		wantTable string
		ok        bool
	}{
		{"delta histogram 3h/60s → 1m kademesi", histF(60, 3), "histogram", "delta", "rollup_metrics_1m", true},
		{"delta histogram 30d/3600s → 1h", histF(3600, 30*24), "histogram", "delta", "rollup_metrics_1h", true},
		{"CUMULATIVE histogram ASLA", histF(60, 3), "histogram", "cumulative", "", false},
		{"gauge değil", histF(60, 3), "gauge", "delta", "", false},
		{"auto-step (0) ham", histF(0, 3), "histogram", "delta", "", false},
		{"kafese uymayan step ham", histF(45, 3), "histogram", "delta", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tier, ok := metricRollupHistPlan(c.f, c.inst, c.tmp, now, nil)
			if ok != c.ok || (ok && tier.table != c.wantTable) {
				t.Fatalf("plan = (%q,%v), (%q,%v) bekleniyordu", tier.table, ok, c.wantTable, c.ok)
			}
		})
	}
	t.Run("groupBy/filtre/instance ham", func(t *testing.T) {
		f := histF(60, 3)
		f.GroupBy = []string{"http.route"}
		if _, ok := metricRollupHistPlan(f, "histogram", "delta", now, nil); ok {
			t.Fatal("groupBy'lı okuma rollup'a girmemeli")
		}
		f = histF(60, 3)
		f.Filters = []FilterExpr{{Key: "k", Op: "=", Values: []string{"v"}}}
		if _, ok := metricRollupHistPlan(f, "histogram", "delta", now, nil); ok {
			t.Fatal("filtreli okuma rollup'a girmemeli")
		}
	})
}

func TestBuildRollupHistSQLDiscipline(t *testing.T) {
	sql := buildRollupHistSQL("rollup_metrics_1m", 60)
	for _, want := range []string{
		"GROUP BY bucket, bucket_bounds", // sumForEach yalnız aynı bounds içinde
		"sumForEachMerge(hist_counts)",
		"LIMIT 50000",
		"max_execution_time = 15",
		"ts >= ? AND ts <= ?",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL'de %q yok", want)
		}
	}
}

func TestFoldRollupHistCanonicalPolicy(t *testing.T) {
	b1 := []float64{0.1, 1, 10}
	b2 := []float64{0.5, 5}
	// v0.9.754 güncellemesi: kanonik artık BASKIN bounds (gözlem
	// ağırlıklı) — b1 toplam 20 gözlemle baskın, b2 (5) atlanır.
	rows := []histRollupRow{
		{Bucket: 1e9, Bounds: b1, Counts: []uint64{10, 0, 0, 0}}, // p50 ilk kovada
		{Bucket: 2e9, Bounds: b2, Counts: []uint64{5, 0, 0}},     // azınlık bounds → atla
		{Bucket: 3e9, Bounds: b1, Counts: []uint64{0, 0, 0, 0}},  // toplam 0 → gap
		{Bucket: 4e9, Bounds: b1, Counts: []uint64{0, 10, 0, 0}}, // p50 ikinci kovada
	}
	pts, skipped := foldRollupHist(rows, 0.5)
	if skipped != 1 {
		t.Fatalf("skipped = %d, 1 bekleniyordu (farklı bounds ham politikası)", skipped)
	}
	if len(pts) != 2 || pts[0].Time != 1e9 || pts[1].Time != 4e9 {
		t.Fatalf("pts = %+v — atlanan + gap yanlış", pts)
	}
	if !(pts[0].Value <= 0.1 && pts[1].Value > 0.1 && pts[1].Value <= 1) {
		t.Fatalf("percentile kovaları yanlış: %v / %v", pts[0].Value, pts[1].Value)
	}
	t.Run("hiç bounds yoksa boş", func(t *testing.T) {
		pts, _ := foldRollupHist([]histRollupRow{{Bucket: 1, Counts: []uint64{1}}}, 0.5)
		if pts != nil {
			t.Fatal("bounds'suz satırlardan nokta üretilmemeli")
		}
	})
}

// v0.9.754 — kanonik = BASKIN bounds (operatör: 749 sonrası RT·metrik
// boş). İlk seri azınlık bounds-set'indeyse eski ilk-görülen politikası
// kalan her şeyi atlayıp paneli boşaltıyordu; baskın politika çoğunluğu
// seçer, uyumsuzları atlamaya devam eder.
func TestDominantBoundsOutlierFirst(t *testing.T) {
	minority := []float64{0.5, 5}
	majority := []float64{0.1, 1, 10}
	got := dominantBounds([]boundsWeight{
		{Bounds: minority, Weight: 3}, // İLK görülen ama azınlık
		{Bounds: majority, Weight: 40},
		{Bounds: majority, Weight: 60},
	})
	if !boundsEqual(got, majority) {
		t.Fatalf("dominantBounds = %v, çoğunluk %v bekleniyordu", got, majority)
	}
	// Eşitlikte ilk görülen (deterministik).
	got = dominantBounds([]boundsWeight{
		{Bounds: minority, Weight: 10},
		{Bounds: majority, Weight: 10},
	})
	if !boundsEqual(got, minority) {
		t.Fatal("eşitlikte ilk görülen kazanmalı")
	}
}

func TestFoldRollupHistDominantPolicy(t *testing.T) {
	minority := []float64{0.5, 5}
	majority := []float64{0.1, 1, 10}
	rows := []histRollupRow{
		{Bucket: 1e9, Bounds: minority, Counts: []uint64{3, 0, 0}}, // ilk ama azınlık → atla
		{Bucket: 2e9, Bounds: majority, Counts: []uint64{50, 0, 0, 0}},
		{Bucket: 3e9, Bounds: majority, Counts: []uint64{60, 0, 0, 0}},
	}
	pts, skipped := foldRollupHist(rows, 0.5)
	if skipped != 1 || len(pts) != 2 {
		t.Fatalf("skipped=%d pts=%d — baskın politika ilk-azınlığı atlamalıydı", skipped, len(pts))
	}
}

// v0.9.765 — quantile penceresi (Grafana histogram_quantile(rate[3m])
// paritesi): delta kova dizileri k-pencereli toplanır; W<=step eski
// davranış (k=1).
func TestHistWindowK(t *testing.T) {
	if k := histWindowK(180, 20); k != 9 {
		t.Fatalf("180/20 = %d, 9 bekleniyordu", k)
	}
	if k := histWindowK(180, 300); k != 1 {
		t.Fatal("W<=step pencere açmamalı")
	}
	if k := histWindowK(0, 20); k != 1 {
		t.Fatal("W=0 pencere açmamalı")
	}
	if k := histWindowK(600, 10); k != 30 {
		t.Fatal("k tavanı 30 uygulanmalı")
	}
}

func TestSlidingSumCounts(t *testing.T) {
	in := [][]uint64{{1, 0}, {2, 1}, {0, 3}, {4, 0}}
	out := slidingSumCounts(in, 2)
	want := [][]uint64{{1, 0}, {3, 1}, {2, 4}, {4, 3}}
	for i := range want {
		for b := range want[i] {
			if out[i][b] != want[i][b] {
				t.Fatalf("out[%d][%d] = %d, %d bekleniyordu", i, b, out[i][b], want[i][b])
			}
		}
	}
	// Girdi mutasyona uğramamalı.
	if in[1][0] != 2 {
		t.Fatal("girdi dizisi mutasyona uğradı")
	}
}

// v0.9.768 — çoklu-q TEK tarama. tryRollupHistogramQuantiles satırları BİR
// KEZ çekip aynı küme üstünde q başına foldRollupHist koşuyor. Bu ancak fold
// salt-okursa doğru: satırları mutasyona uğratsaydı ikinci ve üçüncü yüzdelik
// bozulmuş dağılım görürdü — sessiz ve YALNIZ çoklu yolda görünen bir hata.
// Pin: (a) satırlar dokunulmamış kalır, (b) her q'nun sonucu taze satırlarla
// tek başına katlandığındakiyle birebir aynıdır.
func TestFoldRollupHistMultiQNoMutation(t *testing.T) {
	mkRows := func() []histRollupRow {
		b := []float64{0.1, 1, 10}
		return []histRollupRow{
			{Bucket: 1e9, Bounds: b, Counts: []uint64{90, 8, 2, 0}},
			{Bucket: 2e9, Bounds: b, Counts: []uint64{0, 0, 0, 0}}, // gap
			{Bucket: 3e9, Bounds: b, Counts: []uint64{10, 80, 9, 1}},
		}
	}
	qs := []float64{0.50, 0.95, 0.99}

	// Çoklu yolun yaptığı: TEK satır kümesi, q başına katlama.
	shared := mkRows()
	got := make([][]SpanMetricPoint, len(qs))
	for i, q := range qs {
		got[i], _ = foldRollupHist(shared, q)
	}

	// (a) Satırlar dokunulmadı.
	for i, r := range mkRows() {
		for b := range r.Counts {
			if shared[i].Counts[b] != r.Counts[b] {
				t.Fatalf("satır %d kova %d mutasyona uğradı: %d ≠ %d",
					i, b, shared[i].Counts[b], r.Counts[b])
			}
		}
	}

	// (b) Tekil (taze satır) = çoklu[i], nokta nokta.
	for i, q := range qs {
		want, _ := foldRollupHist(mkRows(), q)
		if len(got[i]) != len(want) {
			t.Fatalf("q=%g: %d nokta, %d bekleniyordu", q, len(got[i]), len(want))
		}
		for j := range want {
			if got[i][j].Time != want[j].Time || got[i][j].Value != want[j].Value {
				t.Fatalf("q=%g nokta %d: %+v ≠ %+v", q, j, got[i][j], want[j])
			}
		}
		// Gap semantiği q'dan bağımsız: toplam-0 bucket hiçbir q'da
		// nokta üretmez.
		if len(got[i]) != 2 {
			t.Fatalf("q=%g: %d nokta — toplam-0 bucket gap kalmalıydı", q, len(got[i]))
		}
	}

	// Testin gerçekten yüzdelik ayırt ettiğini kanıtla (üç q aynı çıksaydı
	// yukarıdaki eşitlikler boş yere geçerdi).
	if !(got[0][0].Value < got[2][0].Value) {
		t.Fatalf("p50 (%v) p99'dan (%v) küçük olmalı", got[0][0].Value, got[2][0].Value)
	}
}

// v0.9.768 — GLOBAL yolun aynı invaryantı: pencere (slidingSumCounts) bir kez
// kurulur, her q AYNI `counts` dizisinden okur. percentileFromBuckets bu
// diziyi mutasyona uğratırsa ikinci q bozuk dağılım görür.
func TestPercentileFromBucketsNoMutation(t *testing.T) {
	bounds := []float64{0.1, 1, 10}
	counts := []uint64{90, 8, 2, 0}
	snapshot := append([]uint64(nil), counts...)
	qs := []float64{0.50, 0.95, 0.99}
	vals := make([]float64, len(qs))
	for i, q := range qs {
		vals[i] = percentileFromBuckets(bounds, counts, q)
	}
	for i := range snapshot {
		if counts[i] != snapshot[i] {
			t.Fatalf("kova %d mutasyona uğradı: %d ≠ %d", i, counts[i], snapshot[i])
		}
	}
	// İkinci tur birebir aynı değerleri vermeli (paylaşılan dizi).
	for i, q := range qs {
		if v := percentileFromBuckets(bounds, counts, q); v != vals[i] {
			t.Fatalf("q=%g ikinci turda %v, %v bekleniyordu", q, v, vals[i])
		}
	}
}
