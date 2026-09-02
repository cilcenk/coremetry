package api

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// metrics_compare_test.go — v0.10.294 (audit §7.5 madde 3): saf çekirdek,
// Server'sız; eşik cmd/paritycheck ile aynı (1e-9).

func ser(gk string, pts ...float64) chstore.SpanMetricSeries {
	s := chstore.SpanMetricSeries{GroupKey: []string{gk}}
	for i, v := range pts {
		s.Points = append(s.Points, chstore.SpanMetricPoint{Time: int64(i) * 60e9, Value: v})
	}
	return s
}

func TestCompareMetricSeries(t *testing.T) {
	tol := metricCompareTolerance
	for _, tc := range []struct {
		name      string
		a, b      []chstore.SpanMetricSeries
		class     string
		matched   int
		mismatch  int
		onlyA     int
		onlyB     int
		firstMisT int64
	}{
		{"bire bir", []chstore.SpanMetricSeries{ser("x", 1, 2, 3)}, []chstore.SpanMetricSeries{ser("x", 1, 2, 3)}, "identical", 1, 0, 0, 0, 0},
		{"kayan nokta gürültüsü tolere", []chstore.SpanMetricSeries{ser("x", 1, 2, 3)}, []chstore.SpanMetricSeries{ser("x", 1, 2*(1+1e-12), 3)}, "tolerated", 1, 0, 0, 0, 0},
		{"1e-6 sapma mismatch", []chstore.SpanMetricSeries{ser("x", 1, 2, 3)}, []chstore.SpanMetricSeries{ser("x", 1, 2*(1+1e-6), 3)}, "mismatch", 1, 1, 0, 0, 60e9},
		{"sıfır ↔ sıfır özdeş; sıfır ↔ 1e-12 mismatch (mutlak)", []chstore.SpanMetricSeries{ser("x", 0, 0)}, []chstore.SpanMetricSeries{ser("x", 0, 1e-12)}, "mismatch", 1, 1, 0, 0, 60e9},
		{"seri yalnız A'da", []chstore.SpanMetricSeries{ser("x", 1), ser("y", 2)}, []chstore.SpanMetricSeries{ser("x", 1)}, "mismatch", 1, 0, 1, 0, 0},
		{"seri yalnız B'de", []chstore.SpanMetricSeries{ser("x", 1)}, []chstore.SpanMetricSeries{ser("x", 1), ser("z", 5)}, "mismatch", 1, 0, 0, 1, 0},
		{"kafes hizasız: B'de fazladan zaman", []chstore.SpanMetricSeries{ser("x", 1, 2)}, []chstore.SpanMetricSeries{ser("x", 1, 2, 3)}, "mismatch", 1, 0, 0, 1, 0},
		{"boş ↔ boş", nil, nil, "identical", 0, 0, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := compareMetricSeries(tc.a, tc.b, tol)
			if rep.Class != tc.class {
				t.Errorf("class %q; want %q (%+v)", rep.Class, tc.class, rep.Series)
			}
			if rep.Matched != tc.matched {
				t.Errorf("matched %d; want %d", rep.Matched, tc.matched)
			}
			var mism, onlyA, onlyB int
			var firstT int64
			for _, s := range rep.Series {
				mism += s.Mismatches
				onlyA += s.OnlyA
				onlyB += s.OnlyB
				if s.FirstMismatchTime != 0 && (firstT == 0 || s.FirstMismatchTime < firstT) {
					firstT = s.FirstMismatchTime
				}
			}
			if mism != tc.mismatch || onlyA != tc.onlyA || onlyB != tc.onlyB || firstT != tc.firstMisT {
				t.Errorf("mismatch=%d onlyA=%d onlyB=%d first=%d; want %d/%d/%d/%d", mism, onlyA, onlyB, firstT, tc.mismatch, tc.onlyA, tc.onlyB, tc.firstMisT)
			}
			// En kötü sınıf listede en üstte (operatör ilk satıra bakar).
			for i := 1; i < len(rep.Series); i++ {
				if classRank(rep.Series[i-1].Class) < classRank(rep.Series[i].Class) {
					t.Errorf("sıralama: %q %q", rep.Series[i-1].Class, rep.Series[i].Class)
				}
			}
		})
	}
}

func TestMetricsCompareRouteIsRegistryBacked(t *testing.T) {
	b, err := os.ReadFile("metrics_routes.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `registerRoutesExtra("metrics"`) || !strings.Contains(string(b), `"GET /api/metrics/compare"`) {
		t.Error("compare ucu deftere kayıtlı değil")
	}
	api := readAPISourceNoComments(t, "api.go")
	if strings.Contains(api, "/api/metrics/compare") || strings.Contains(api, "registerMetricsRoutes(") {
		t.Error("api.go büyümemeli — kayıt defter üzerinden")
	}
}
