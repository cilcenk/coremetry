package api

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// explain_service_charts_test — ServiceCharts AI çekmecesinin (onaylı
// mockup, Ⓐ+Ⓑ giriş noktaları) KANIT katmanı sözleşmesi.
//
// Neden bu testler: çekmecenin "İlişkili sinyaller" tablosu doğrudan
// selectOpDeltas çıktısını çiziyor ve altına "diğer N: değişim yok"
// yazıyor. Seçim mantığı sessizce kayarsa operatör YANLIŞ operasyonu
// suçlar — model hatası gibi görünür ama hata burada olur.
//
// Ayrıca ErrorRate BİRİMİ pinlenir: chstore.OperationSummary.ErrorRate
// 0..100 ölçeğinde (repo.go:703). Onu 0..1 sanıp 100'le çarpmak
// v0.6.36 sınıfı bir birim-karışımı olurdu ve hata deltası 100× şişerdi.

func op(name string, calls uint64, p95, errRate float64) chstore.OperationSummary {
	return chstore.OperationSummary{
		Name: name, SpanCount: calls, P95Ms: p95, ErrorRate: errRate,
	}
}

func withPrior(o chstore.OperationSummary, p95, errRate float64) chstore.OperationSummary {
	o.HasPrior = true
	o.PriorP95Ms = p95
	o.PriorErrorRate = errRate
	return o
}

func TestSelectOpDeltas(t *testing.T) {
	tests := []struct {
		name       string
		rows       []chstore.OperationSummary
		topN       int
		wantNames  []string
		wantOther  int
		check      func(t *testing.T, got []OpDelta)
	}{
		{
			name:      "boş girdi",
			rows:      nil,
			topN:      3,
			wantNames: nil,
			wantOther: 0,
		},
		{
			name:      "topN sıfır → seçim yok, hepsi diğer",
			rows:      []chstore.OperationSummary{withPrior(op("a", 100, 10, 0), 10, 0)},
			topN:      0,
			wantNames: nil,
			wantOther: 1,
		},
		{
			// İlk pencere / MV boşluğu: kıyas yapılamaz. Her satırı
			// "YENİ" diye raporlamak uydurma olurdu.
			name: "hiç prior yok → karşılaştırma yapılamaz",
			rows: []chstore.OperationSummary{
				op("a", 100, 500, 5), op("b", 50, 200, 0),
			},
			topN:      3,
			wantNames: nil,
			wantOther: 2,
		},
		{
			name: "yalnız bariyeri geçen kötüleşme seçilir",
			rows: []chstore.OperationSummary{
				// p95 3× → seçilir
				withPrior(op("slow", 100, 900, 0), 300, 0),
				// p95 1.05× → bariyerin altında, "değişim yok"
				withPrior(op("steady", 100, 105, 0), 100, 0),
				// hata +2.5pp → seçilir
				withPrior(op("erring", 80, 100, 3.0), 100, 0.5),
			},
			topN:      3,
			wantNames: []string{"slow", "erring"},
			wantOther: 1,
		},
		{
			// Bariyerin ALTINDA kalan iyileşme de seçilmemeli: p95
			// yarıya inen bir operasyon "en kötü" listesine giremez.
			name: "iyileşme seçilmez",
			rows: []chstore.OperationSummary{
				withPrior(op("faster", 100, 50, 0), 200, 0),
			},
			topN:      3,
			wantNames: nil,
			wantOther: 1,
		},
		{
			name: "düşük hacim gürültüsü elenir",
			rows: []chstore.OperationSummary{
				// 2 çağrı, p95 5× — opDeltaMinCalls altında, gürültü.
				withPrior(op("rare", 2, 1000, 0), 200, 0),
				withPrior(op("busy", 500, 400, 0), 200, 0),
			},
			topN:      3,
			wantNames: []string{"busy"},
			// rare listeye giremez ama TOPLAMI tamamlar: 2 satır - 1 seçilen.
			wantOther: 1,
		},
		{
			name: "yeni operasyon işaretlenir ve öne çıkar",
			rows: []chstore.OperationSummary{
				op("brand-new", 40, 300, 0), // HasPrior=false
				withPrior(op("mild", 100, 130, 0), 100, 0),
			},
			topN:      3,
			wantNames: []string{"brand-new", "mild"},
			wantOther: 0,
			check: func(t *testing.T, got []OpDelta) {
				if !got[0].IsNew {
					t.Fatalf("brand-new IsNew=false")
				}
				if got[0].P95Ratio != 0 {
					t.Fatalf("prior'suz operasyonda P95Ratio %v olmalı 0", got[0].P95Ratio)
				}
			},
		},
		{
			name: "topN sınırı + diğer sayısı",
			rows: []chstore.OperationSummary{
				withPrior(op("w1", 100, 1000, 0), 100, 0),
				withPrior(op("w2", 100, 900, 0), 100, 0),
				withPrior(op("w3", 100, 800, 0), 100, 0),
				withPrior(op("w4", 100, 700, 0), 100, 0),
				withPrior(op("calm", 100, 100, 0), 100, 0),
			},
			topN:      3,
			wantNames: []string{"w1", "w2", "w3"},
			// 5 satır - 3 seçilen = 2 (w4 kötüleşmiş OLSA DA "diğer"e
			// düşer; sayı her zaman toplamı tamamlar).
			wantOther: 2,
		},
		{
			// prior p95 = 0 → oran +Inf olurdu. 0 = "ölçülemedi".
			name: "sıfır prior p95 → oran 0, +Inf değil",
			rows: []chstore.OperationSummary{
				withPrior(op("zero", 100, 500, 4.0), 0, 0),
			},
			topN:      3,
			wantNames: []string{"zero"}, // hata deltası bariyeri geçiyor
			wantOther: 0,
			check: func(t *testing.T, got []OpDelta) {
				if got[0].P95Ratio != 0 {
					t.Fatalf("P95Ratio %v olmalı 0 (ölçülemedi)", got[0].P95Ratio)
				}
			},
		},
		{
			// BİRİM PİNİ: ErrorRate 0..100. 3.0 → 0.5 farkı 2.5pp'dir,
			// 250pp değil.
			name: "hata deltası yüzde PUANI",
			rows: []chstore.OperationSummary{
				withPrior(op("e", 100, 100, 3.0), 100, 0.5),
			},
			topN:      1,
			wantNames: []string{"e"},
			wantOther: 0,
			check: func(t *testing.T, got []OpDelta) {
				if d := got[0].ErrDeltaPP; d < 2.49 || d > 2.51 {
					t.Fatalf("ErrDeltaPP = %v, beklenen ~2.5pp", d)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, other := selectOpDeltas(tc.rows, tc.topN)
			if len(got) != len(tc.wantNames) {
				t.Fatalf("seçilen %d (%v), beklenen %d (%v)",
					len(got), names(got), len(tc.wantNames), tc.wantNames)
			}
			for i, w := range tc.wantNames {
				if got[i].Name != w {
					t.Fatalf("sıra[%d] = %q, beklenen %q (tümü: %v)", i, got[i].Name, w, names(got))
				}
			}
			if other != tc.wantOther {
				t.Fatalf("otherCount = %d, beklenen %d", other, tc.wantOther)
			}
			if tc.check != nil && len(got) > 0 {
				tc.check(t, got)
			}
		})
	}
}

// Seçilen + diğer HER ZAMAN satır toplamını vermeli — mockup'ın
// "diğer N: değişim yok" satırı aksi hâlde operasyon kaybeder.
func TestSelectOpDeltasAccountsForEveryRow(t *testing.T) {
	rows := []chstore.OperationSummary{
		withPrior(op("a", 100, 900, 0), 100, 0),
		withPrior(op("b", 100, 120, 0), 100, 0),
		withPrior(op("c", 3, 900, 0), 100, 0), // hacim altı
		op("d", 60, 300, 0),                   // yeni
	}
	for _, topN := range []int{1, 2, 3, 10} {
		got, other := selectOpDeltas(rows, topN)
		if len(got)+other != len(rows) {
			t.Fatalf("topN=%d: %d seçilen + %d diğer = %d, beklenen %d",
				topN, len(got), other, len(got)+other, len(rows))
		}
	}
}

func TestNormalizeChartScope(t *testing.T) {
	cases := map[string]string{
		"":        chartScopeAll,
		"all":     chartScopeAll,
		"rps":     chartScopeRPS,
		"RPS":     chartScopeRPS,
		" err ":   chartScopeErr,
		"dur":     chartScopeDur,
		"garbage": chartScopeAll, // elle düzenlenmiş URL → en geniş kapsam
	}
	for in, want := range cases {
		if got := normalizeChartScope(in); got != want {
			t.Fatalf("normalizeChartScope(%q) = %q, beklenen %q", in, got, want)
		}
	}
	// Her kapsamın bir etiketi OLMALI; etiketsiz kapsam çekmecede boş
	// çip demektir.
	for _, s := range []string{chartScopeAll, chartScopeRPS, chartScopeErr, chartScopeDur} {
		if chartScopeLabel(s) == "" {
			t.Fatalf("kapsam %q etiketsiz", s)
		}
	}
}

func TestBuildServiceChartsUserCarriesEverySignal(t *testing.T) {
	from := time.Unix(1700000000, 0)
	in := serviceChartsInput{
		Service: "payment-service",
		Scope:   chartScopeDur,
		From:    from,
		To:      from.Add(time.Hour),
		P99:     []chartSeriesStat{{Name: "POST /pay", Min: 100, Max: 900, Avg: 400, Current: 748}},
		Signals: ServiceChartsSignals{
			Deploy: &ChartDeploySignal{
				TimeUnixNs: from.Add(30 * time.Minute).UnixNano(),
				Kind:       "deploy", VersionAfter: "v2.4.1", PodsReplaced: 3,
			},
			Problems: []ChartProblemSignal{{
				ID: "4812", Title: "failure-rate", Severity: "critical",
				Metric: "error_rate", Value: 2.9, Threshold: 1.0,
			}},
			Anomalies: []ChartAnomalySignal{{
				Kind: "trace_op", Pattern: "POST /pay", PeakRatio: 3.1, Status: "active",
			}},
			OpDeltas: []OpDelta{{Name: "POST /pay", Calls: 900, P95Ratio: 2.9, ErrDeltaPP: 2.5}},
			OtherOps: 14,
		},
	}
	got := buildServiceChartsUser(in)
	for _, want := range []string{
		"payment-service",
		"P99 latency by operation", // kapsam etiketi
		"v2.4.1",
		"failure-rate",
		"3.1×",
		"2.90×",
		"+2.50pp",
		"diğer 14 operasyon",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt %q içermiyor.\n---\n%s", want, got)
		}
	}
}

// Sinyalsiz pencere: model "sorun yok" diyebilmeli, ama prompt hiçbir
// sinyali UYDURMAMALI — boş bölümler hiç yazılmaz.
func TestBuildServiceChartsUserOmitsEmptySections(t *testing.T) {
	got := buildServiceChartsUser(serviceChartsInput{
		Service: "quiet", Scope: chartScopeAll,
		From: time.Unix(1, 0), To: time.Unix(2, 0),
	})
	for _, absent := range []string{"Deploy/rollout", "Açık problemler", "Anomaliler"} {
		if strings.Contains(got, absent) {
			t.Fatalf("boş pencerede %q bölümü yazılmamalı:\n%s", absent, got)
		}
	}
}

// ratioText: 0 "0×" DEĞİL. Sessiz sıfır, modele "gecikme sıfıra düştü"
// dedirten kaynaktır.
func TestRatioTextZeroIsNotZeroTimes(t *testing.T) {
	if s := ratioText(0); strings.Contains(s, "0×") {
		t.Fatalf("ratioText(0) = %q — sıfır oran '0×' diye anlatılamaz", s)
	}
	if s := ratioText(2.5); s != "2.50×" {
		t.Fatalf("ratioText(2.5) = %q", s)
	}
}

func TestSummarizeSeriesTopNAndDeterminism(t *testing.T) {
	mk := func(name string, vals ...float64) chstore.SpanMetricSeries {
		pts := make([]chstore.SpanMetricPoint, 0, len(vals))
		for i, v := range vals {
			pts = append(pts, chstore.SpanMetricPoint{Time: int64(i), Value: v})
		}
		return chstore.SpanMetricSeries{GroupKey: []string{name}, Points: pts}
	}
	rows := []chstore.SpanMetricSeries{
		mk("small", 1, 1),
		mk("big", 100, 200),
		mk("mid", 10, 20),
		mk("empty"), // noktasız seri düşer
	}
	got := summarizeSeries(rows, 2)
	if len(got) != 2 || got[0].Name != "big" || got[1].Name != "mid" {
		t.Fatalf("summarizeSeries = %+v", got)
	}
	if got[0].Current != 200 || got[0].Min != 100 || got[0].Max != 200 || got[0].Avg != 150 {
		t.Fatalf("big istatistikleri yanlış: %+v", got[0])
	}
	if summarizeSeries(rows, 0) != nil {
		t.Fatalf("topN=0 nil dönmeli")
	}
}

func names(ds []OpDelta) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Name)
	}
	return out
}
