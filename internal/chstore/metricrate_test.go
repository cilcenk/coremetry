package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.9.106 (F2) — reset-korumalı rate/increase çekirdeği. Bozuk rate operatöre
// yanlış "hız" gösterir; reset + gerçek-dt + seed-lookback semantiği EXACT olmalı
// (adversarial review'da 3 major bug bulundu — bunlar o fix'lerin kontratı).

func fp(v float64) *float64 { return &v }

func TestResetSafeDelta(t *testing.T) {
	cases := []struct{ prev, cur, want float64 }{
		{100, 110, 10},   // düz artış
		{100, 100, 0},    // sabit
		{120, 5, 5},      // reset (cur<prev) → post-reset değer
		{0, 42, 42},      // sıfırdan
	}
	for _, c := range cases {
		if got := resetSafeDelta(c.prev, c.cur); got != c.want {
			t.Errorf("resetSafeDelta(%v,%v) = %v; want %v", c.prev, c.cur, got, c.want)
		}
	}
}

// ns yardımcı: saniye → ns bucket.
func ns(sec uint64) uint64 { return sec * 1_000_000_000 }

func TestSeriesRatePoints(t *testing.T) {
	// step=60s senaryoları. buckets ns, vals kümülatif.
	t.Run("düz counter: rate = sabit hız (baseline emit yok)", func(t *testing.T) {
		buckets := []uint64{ns(0), ns(60), ns(120)}
		vals := []*float64{fp(1000), fp(1060), fp(1120)} // +60/60s = 1/s
		got := seriesRatePoints(buckets, vals, "rate", 0, true)
		// İlk (ns0) baseline → emit yok; ns60,ns120 rate=1/s.
		if len(got) != 2 {
			t.Fatalf("emit sayısı = %d; want 2 (%v)", len(got), got)
		}
		if got[0].value != 1 || got[1].value != 1 {
			t.Errorf("rate = %v; want [1,1]", got)
		}
	})

	t.Run("GAP → over-division YOK (gerçek dt'ye böl)", func(t *testing.T) {
		// review-fix #2: t60/t120 eksik; t180'de 1240. Gerçek hız 1/s.
		// vals=[1000,1240] dt=180s delta=240 → 240/180 = 1.33/s (spike DEĞİL).
		// Sabit /60'a bölseydi 240/60=4/s (3× spike) — o bug.
		buckets := []uint64{ns(0), ns(180)}
		vals := []*float64{fp(1000), fp(1240)}
		got := seriesRatePoints(buckets, vals, "rate", 0, true)
		if len(got) != 1 {
			t.Fatalf("emit = %d; want 1", len(got))
		}
		if want := 240.0 / 180.0; got[0].value != want {
			t.Errorf("gap rate = %v; want %v (dt=180s, delta=240)", got[0].value, want)
		}
	})

	t.Run("SEED lookback: sol-kenar sahte-sıfır YOK", func(t *testing.T) {
		// review-fix #4: pencere From=ns60; ns0 seed. dropBefore=ns60.
		// ns0 primer (emit yok), ns60 gerçek delta (1000→1060=60/60=1/s), ns120 aynı.
		buckets := []uint64{ns(0), ns(60), ns(120)}
		vals := []*float64{fp(1000), fp(1060), fp(1120)}
		got := seriesRatePoints(buckets, vals, "rate", ns(60), true)
		if len(got) != 2 {
			t.Fatalf("emit = %d; want 2 (ns0 seed atılır)", len(got))
		}
		if got[0].bucket != ns(60) || got[0].value != 1 {
			t.Errorf("ilk emit = %+v; want bucket=ns60 value=1 (sahte-sıfır DEĞİL)", got[0])
		}
	})

	t.Run("reset ortada: post-reset değer artış", func(t *testing.T) {
		buckets := []uint64{ns(0), ns(60), ns(120)}
		vals := []*float64{fp(100), fp(5), fp(35)} // reset@60: 5; sonra 35-5=30
		got := seriesRatePoints(buckets, vals, "increase", 0, true)
		if len(got) != 2 || got[0].value != 5 || got[1].value != 30 {
			t.Errorf("increase = %v; want [5,30] (reset post-value + normal)", got)
		}
	})

	t.Run("increase = ham delta (dt'ye bölme yok)", func(t *testing.T) {
		buckets := []uint64{ns(0), ns(60)}
		vals := []*float64{fp(1000), fp(1090)}
		got := seriesRatePoints(buckets, vals, "increase", 0, true)
		if len(got) != 1 || got[0].value != 90 {
			t.Errorf("increase = %v; want [90]", got)
		}
	})

	t.Run("null gap: dt son-DOLU'ya göre", func(t *testing.T) {
		buckets := []uint64{ns(0), ns(60), ns(120)}
		vals := []*float64{fp(1000), nil, fp(1120)} // dt=120s delta=120 → 1/s
		got := seriesRatePoints(buckets, vals, "rate", 0, true)
		if len(got) != 1 || got[0].value != 1 {
			t.Errorf("null-gap rate = %v; want [1] (120/120)", got)
		}
	})

	t.Run("tek nokta → baseline, emit yok", func(t *testing.T) {
		if got := seriesRatePoints([]uint64{ns(0)}, []*float64{fp(5)}, "rate", 0, true); len(got) != 0 {
			t.Errorf("tek nokta emit = %v; want boş", got)
		}
	})
}

func TestIsRateableInstrument(t *testing.T) {
	if !isRateableInstrument("sum") {
		t.Error("sum (counter) rateable olmalı")
	}
	for _, i := range []string{"gauge", "histogram", ""} {
		if isRateableInstrument(i) {
			t.Errorf("%q rateable OLMAMALI", i)
		}
	}
}

// v0.9.714 (parite koşumu bulgusu) — is_monotonic=0 cumulative artık
// SESSİZCE BOŞ değil: düz türev (reset-telafisiz). Negatif MEŞRU —
// up-down counter'ın anlamı bu. Telafi yalnız monotonikte: telafi
// non-monotonikte her düşüşü zirveye çevirirdi (v0.9.106'nın korkusu
// yerindeydi, cevabı yanlıştı).
func TestSeriesRatePointsNonMonotonic(t *testing.T) {
	buckets := []uint64{0, ns(60), ns(120)}
	v1, v2, v3 := 100.0, 40.0, 90.0 // düşüş + çıkış (active_requests deseni)
	vals := []*float64{&v1, &v2, &v3}

	t.Run("monotonic=false: düşüş NEGATİF türev, zirve yok", func(t *testing.T) {
		got := seriesRatePoints(buckets, vals, "increase", 0, false)
		if len(got) != 2 {
			t.Fatalf("nokta = %d", len(got))
		}
		if got[0].value != -60 { // 40-100
			t.Fatalf("düşüş delta = %v, beklenen -60 (telafi SIZMIŞ olabilir)", got[0].value)
		}
		if got[1].value != 50 {
			t.Fatalf("çıkış delta = %v", got[1].value)
		}
	})

	t.Run("monotonic=true: aynı veri reset sayılır (eski sözleşme durur)", func(t *testing.T) {
		got := seriesRatePoints(buckets, vals, "increase", 0, true)
		if got[0].value != 40 { // resetSafeDelta: cur<prev → cur
			t.Fatalf("reset telafisi = %v, beklenen 40", got[0].value)
		}
	})
}

func TestMonoSelectExpr(t *testing.T) {
	if monoSelectExpr(true) != "toUInt8(max(is_monotonic))" {
		t.Fatal("kolonlu ifade değişti")
	}
	if monoSelectExpr(false) != "toUInt8(1)" {
		t.Fatal("kolonsuz geri düşüş değişti — eski dünya monotonik varsaymalı")
	}
}

// SQL kapısı KALKTI pini: filtre geri gelirse 0'lar yine sessiz boşa döner.
func TestCumulativeRateDoesNotFilterMonotonic(t *testing.T) {
	b, err := os.ReadFile("metricrate.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoLineComments(string(b))
	if strings.Contains(src, `add("is_monotonic = 1")`) {
		t.Error("is_monotonic=1 filtresi geri gelmiş — v0.9.714 sessiz-boş sınıfını yeniden açar")
	}
	if !strings.Contains(src, "MonoExpr:") {
		t.Error("MonoExpr bağlanmamış — mono bayrağı SELECT'e taşınmalı")
	}
}

// v0.9.718 (parite: "kesikli" şikâyeti) — rollingRate = PromQL rate([W]).
// Grafana referansı [3m] pencereyle pürüzsüz; penceresiz per-bucket delta
// aynı veride testere üretiyordu.
func TestRollingRate(t *testing.T) {
	mk := func(vals ...float64) []ratePoint {
		out := make([]ratePoint, len(vals))
		for i, v := range vals {
			out[i] = ratePoint{bucket: uint64(i) * ns(10), value: v}
		}
		return out
	}

	t.Run("pencere<=step: girdi aynen (eski davranış)", func(t *testing.T) {
		in := mk(1, 2, 3)
		out := rollingRate(in, 10, 10, "rate")
		for i := range in {
			if out[i] != in[i] {
				t.Fatalf("nokta %d değişti", i)
			}
		}
	})

	t.Run("testere → düz: 0/6/0/6 rate'i 30s pencerede yumuşar", func(t *testing.T) {
		// step=10s, artışlar (rate×dt): 0,60,0,60,0,60 → 30s pencerede
		// toplam ~60-120/30 bandına oturur; uçlar arası fark testere
		// halinden KÜÇÜK olmalı.
		in := mk(0, 6, 0, 6, 0, 6)
		out := rollingRate(in, 10, 30, "rate")
		mx, mn := out[2].value, out[2].value
		for _, p := range out[2:] { // pencere dolduktan sonra
			if p.value > mx {
				mx = p.value
			}
			if p.value < mn {
				mn = p.value
			}
		}
		if mx-mn >= 6 {
			t.Fatalf("yumuşama yok: min=%v max=%v (testere aynen duruyor)", mn, mx)
		}
		if mn <= 0 {
			t.Fatalf("pencereli rate 0'a düşmemeli (artış var): mn=%v", mn)
		}
	})

	t.Run("increase modu: pencere TOPLAMI, W'ye bölünmez", func(t *testing.T) {
		in := mk(10, 10, 10) // increase per bucket
		out := rollingRate(in, 10, 30, "increase")
		if out[2].value != 30 {
			t.Fatalf("30s pencere artışı = %v, beklenen 30", out[2].value)
		}
	})

	t.Run("boşluklu seride pencere kayar, patlamaz", func(t *testing.T) {
		in := []ratePoint{
			{bucket: 0, value: 6},
			{bucket: ns(60), value: 6}, // 50s boşluk
		}
		out := rollingRate(in, 10, 30, "rate")
		if len(out) != 2 || out[1].value <= 0 {
			t.Fatalf("boşluk sonrası nokta: %+v", out)
		}
	})
}
