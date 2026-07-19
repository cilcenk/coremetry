package chstore

import "testing"

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
		got := seriesRatePoints(buckets, vals, "rate", 0)
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
		got := seriesRatePoints(buckets, vals, "rate", 0)
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
		got := seriesRatePoints(buckets, vals, "rate", ns(60))
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
		got := seriesRatePoints(buckets, vals, "increase", 0)
		if len(got) != 2 || got[0].value != 5 || got[1].value != 30 {
			t.Errorf("increase = %v; want [5,30] (reset post-value + normal)", got)
		}
	})

	t.Run("increase = ham delta (dt'ye bölme yok)", func(t *testing.T) {
		buckets := []uint64{ns(0), ns(60)}
		vals := []*float64{fp(1000), fp(1090)}
		got := seriesRatePoints(buckets, vals, "increase", 0)
		if len(got) != 1 || got[0].value != 90 {
			t.Errorf("increase = %v; want [90]", got)
		}
	})

	t.Run("null gap: dt son-DOLU'ya göre", func(t *testing.T) {
		buckets := []uint64{ns(0), ns(60), ns(120)}
		vals := []*float64{fp(1000), nil, fp(1120)} // dt=120s delta=120 → 1/s
		got := seriesRatePoints(buckets, vals, "rate", 0)
		if len(got) != 1 || got[0].value != 1 {
			t.Errorf("null-gap rate = %v; want [1] (120/120)", got)
		}
	})

	t.Run("tek nokta → baseline, emit yok", func(t *testing.T) {
		if got := seriesRatePoints([]uint64{ns(0)}, []*float64{fp(5)}, "rate", 0); len(got) != 0 {
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
