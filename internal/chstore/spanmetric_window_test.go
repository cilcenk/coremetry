package chstore

import "testing"

// v0.9.723 — span-RED kayan penceresi (Grafana/Prometheus rate[W]
// paritesi, operatör ekran kıyası). Saf çekirdekler: spanMetricWindow
// (kafes + tavanlar), spanAggZeroFills (hangi agg bilinen-sıfır),
// zeroFillSpanSeries (grid doldurma).

func TestSpanMetricWindow(t *testing.T) {
	cases := []struct {
		name         string
		win, step    int
		wantW, wantK int
	}{
		{"kapalı: 0", 0, 20, 0, 0},
		{"kapalı: negatif", -5, 20, 0, 0},
		{"kapalı: step>=win", 180, 300, 0, 0},
		{"kapalı: step==win", 60, 60, 0, 0},
		{"tam bölünme: 180/20", 180, 20, 180, 9},
		{"yukarı yuvarlama: 180/25 → 8*25=200", 180, 25, 200, 8},
		{"clamp 600: 900 → 600/60", 900, 60, 600, 10},
		{"k tavanı 30: 600/10 → 60 değil 30", 600, 10, 300, 30},
		{"step=0 koruması", 180, 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, k := spanMetricWindow(c.win, c.step)
			if w != c.wantW || k != c.wantK {
				t.Fatalf("spanMetricWindow(%d,%d) = (%d,%d), (%d,%d) bekleniyordu",
					c.win, c.step, w, k, c.wantW, c.wantK)
			}
		})
	}
}

// raiseStepForWindow — k<=30 tavanı pencereyi DARALTAMAZ: kısa
// aralıkta step tabana yükselir (Grafana min-interval karşılığı),
// böylece effWin her zaman istenen W'yi kapsar ve aynı karttaki
// metrik-türevli çizgiyle (rollingRate, tavansız) ayrışmaz.
func TestRaiseStepForWindow(t *testing.T) {
	cases := []struct {
		name            string
		step, win, want int
	}{
		{"pencere yok: dokunma", 2, 0, 2},
		{"5m görünüm: 2s → 6s tabanı (W=180)", 2, 180, 6},
		{"15m görünüm: 5s → 6s", 5, 180, 6},
		{"30m görünüm: 10s yeterli", 10, 180, 10},
		{"3h görünüm: 20s yeterli", 20, 180, 20},
		{"W clamp 600: taban 20s", 5, 900, 20},
		{"step=0 koruması", 0, 180, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := raiseStepForWindow(c.step, c.win); got != c.want {
				t.Fatalf("raiseStepForWindow(%d,%d) = %d, %d bekleniyordu", c.step, c.win, got, c.want)
			}
			// Değişmez: yükseltilmiş step ile pencere TAM kapsanır.
			if c.win > 0 && c.want > 0 {
				w := c.win
				if w > 600 {
					w = 600
				}
				if eff, k := spanMetricWindow(c.win, c.want); k > 0 && eff < w {
					t.Fatalf("effWin=%d < istenen %d — tavan pencereyi daralttı", eff, w)
				}
			}
		})
	}
}

func TestSpanAggZeroFills(t *testing.T) {
	// Sayım türevleri doldurulur; oran/gecikme DOLDURULMAZ — pencerede
	// istek yoksa error_rate ve pXX tanımsızdır (Prometheus 0/0 → gap).
	for _, a := range []string{"count", "rate", "per_min", "errors", "RATE"} {
		if !spanAggZeroFills(a) {
			t.Errorf("%q doldurulmalıydı", a)
		}
	}
	for _, a := range []string{"error_rate", "avg", "p50", "p95", "p99", "apdex", "sum", "min", "max"} {
		if spanAggZeroFills(a) {
			t.Errorf("%q DOLDURULMAMALIYDI", a)
		}
	}
}

func TestZeroFillSpanSeries(t *testing.T) {
	const step = 20
	stepNs := int64(step) * 1e9
	from := int64(1000) * 1e9
	to := from + 4*stepNs

	t.Run("seyrek seri gride oturur", func(t *testing.T) {
		in := []SpanMetricSeries{{
			GroupKey: []string{"/r"},
			Points:   []SpanMetricPoint{{Time: from + stepNs, Value: 5}},
		}}
		out := zeroFillSpanSeries(in, step, from, to)
		pts := out[0].Points
		if len(pts) != 5 {
			t.Fatalf("grid = %d nokta, 5 bekleniyordu", len(pts))
		}
		want := []float64{0, 5, 0, 0, 0}
		for i, w := range want {
			if pts[i].Value != w || pts[i].Time != from+int64(i)*stepNs {
				t.Errorf("pts[%d] = (%d, %v), (%d, %v) bekleniyordu",
					i, pts[i].Time, pts[i].Value, from+int64(i)*stepNs, w)
			}
		}
		if out[0].GroupKey[0] != "/r" {
			t.Error("GroupKey korunmadı")
		}
	})

	t.Run("hizasız from floor'lanır", func(t *testing.T) {
		in := []SpanMetricSeries{{Points: nil}}
		out := zeroFillSpanSeries(in, step, from+3e9, from+2*stepNs)
		if len(out[0].Points) == 0 || out[0].Points[0].Time != from {
			t.Fatal("ilk bucket floor(from) değil")
		}
	})

	t.Run("step=0 koruması: dokunmaz", func(t *testing.T) {
		in := []SpanMetricSeries{{Points: []SpanMetricPoint{{Time: from, Value: 1}}}}
		out := zeroFillSpanSeries(in, 0, from, to)
		if len(out[0].Points) != 1 {
			t.Fatal("step=0'da seri değişti")
		}
	})

	t.Run("boş liste boş kalır — sıfır trafik serisi icat edilmez", func(t *testing.T) {
		if got := zeroFillSpanSeries(nil, step, from, to); len(got) != 0 {
			t.Fatalf("boş girdiden %d seri çıktı", len(got))
		}
	})
}

// aggToSQL bölen genellemesi: pencereli çağrı W_eff'i bölen olarak
// geçirir — rate = count/W (Prometheus rate ile aynı birim, req/s).
func TestAggToSQLRateDivisor(t *testing.T) {
	got, err := aggToSQL("rate", "duration_ms", 180)
	if err != nil {
		t.Fatal(err)
	}
	want := "toNullable(toFloat64(count() / 180.0))"
	if got != want {
		t.Fatalf("rate(180) = %q, %q bekleniyordu", got, want)
	}
}
