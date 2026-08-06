package chstore

import (
	"math"
	"testing"
)

// v0.9.722 — delta yolunda sıfır-doldurma + kayan pencere.
//
// Operatör kıyası (prod ekranları, Grafana vs Coremetry): seyrek delta
// route serileri sıfıra çakılan iğneler gibi görünüyordu. İki kök:
//  1. queryRateDelta yalnız DOLU bucket'ları basıyordu — delta
//     temporality'de boş bucket "veri eksik" değil "SIFIR OLAY"dır
//     (Prometheus increase() boş aralığa 0 der).
//  2. v0.9.718 rollingRate yalnız cumulative yola bağlıydı; delta
//     penceresiz kalmıştı.
// Bu dosya saf çekirdeği (deltaGridFill + rollingRate/increase + /W)
// tablo-güdümlü pinler.

func TestDeltaGridFill(t *testing.T) {
	const step = 20
	stepNs := uint64(step) * 1e9
	from := uint64(1000) * 1e9 // 1000s — kafese hizalı
	to := from + 5*stepNs      // 6 bucket'lık grid (dahil)

	t.Run("seyrek seri tam gride oturur, boşluk=0", func(t *testing.T) {
		byGk := map[string]map[uint64]float64{
			"r": {from + 1*stepNs: 100, from + 4*stepNs: 40},
		}
		grid := deltaGridFill(byGk, []string{"r"}, step, from, to)
		pts := grid["r"]
		if len(pts) != 6 {
			t.Fatalf("grid uzunluğu = %d, 6 bekleniyordu", len(pts))
		}
		want := []float64{0, 100, 0, 0, 40, 0}
		for i, w := range want {
			if pts[i].value != w {
				t.Errorf("pts[%d] = %v, %v bekleniyordu", i, pts[i].value, w)
			}
			if pts[i].bucket != from+uint64(i)*stepNs {
				t.Errorf("pts[%d].bucket kafes dışı", i)
			}
		}
	})

	t.Run("from kafese hizasızsa floor'lanır (toStartOfInterval uyumu)", func(t *testing.T) {
		byGk := map[string]map[uint64]float64{"r": {}}
		grid := deltaGridFill(byGk, []string{"r"}, step, from+3e9, from+2*stepNs)
		pts := grid["r"]
		if len(pts) == 0 || pts[0].bucket != from {
			t.Fatalf("ilk bucket %v, floor(from)=%v bekleniyordu", pts[0].bucket, from)
		}
	})

	t.Run("step=0 koruması", func(t *testing.T) {
		grid := deltaGridFill(map[string]map[uint64]float64{"r": {}}, []string{"r"}, 0, from, from+2e9)
		if len(grid["r"]) == 0 {
			t.Fatal("step=0'da boş grid — 1s'e düşmeliydi")
		}
	})
}

// Uçtan uca semantik: tek 100-olaylık bucket, step=20 win=180.
// Prometheus rate[180s] gibi: patlamayı izleyen 180 sn boyunca
// 100/180 ≈ 0.556 sürekli çizgi, öncesi/sonrası 0 — iğne YOK.
func TestDeltaWindowRateSemantics(t *testing.T) {
	const step, win = 20, 180
	stepNs := uint64(step) * 1e9
	from := uint64(2000) * 1e9
	to := from + 14*stepNs

	byGk := map[string]map[uint64]float64{"r": {from + 2*stepNs: 100}}
	grid := deltaGridFill(byGk, []string{"r"}, step, from, to)
	pts := rollingRate(grid["r"], step, win, "increase")

	sustained := 0
	for _, p := range pts {
		rate := p.value / win
		switch {
		case rate == 0:
			// pencere dışı — doğru
		case math.Abs(rate-100.0/win) < 1e-9:
			sustained++
		default:
			t.Errorf("bucket %d: rate=%v — ne 0 ne 100/W", p.bucket, rate)
		}
	}
	// 180s pencere / 20s step = patlama 9 ardışık bucket'ta görünür.
	if sustained != 9 {
		t.Fatalf("sürekli segment %d bucket, 9 bekleniyordu (win/step)", sustained)
	}
}

// Geriye uyum: RateWindowSec=0 → win=step → rate = değer/step
// (v0.9.722 öncesi davranış); sıfır-doldurma yine de uygulanır.
func TestDeltaWindowFallbackOldRate(t *testing.T) {
	const step = 20
	stepNs := uint64(step) * 1e9
	from := uint64(3000) * 1e9

	byGk := map[string]map[uint64]float64{"r": {from + stepNs: 60}}
	grid := deltaGridFill(byGk, []string{"r"}, step, from, from+3*stepNs)
	win := 0
	if win < step {
		win = step // queryRateDelta'daki clamp
	}
	pts := rollingRate(grid["r"], step, win, "increase")
	for i, p := range pts {
		got := p.value / float64(win)
		want := 0.0
		if i == 1 {
			want = 60.0 / step
		}
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("pts[%d] rate = %v, %v bekleniyordu", i, got, want)
		}
	}
}
