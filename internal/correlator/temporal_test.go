package correlator

import (
	"math"
	"strings"
	"testing"
)

// temporal_test.go — v0.10.700 (parite #2, dilim 1). Zamansal faktörün
// sözleşmesi: birlikte hareket → yüksek, aday önde → lag>0, sonradan
// sıçrayan → ceza, düz seyirci → 0, doğrusal rampa ikizi → 0 (fark sabiti,
// sinyal yok), yetersiz veri → nötr 0.5; deterministik.

// spike — 12 kova: 6 kova gürültülü taban + 6 kova yüksek plato.
func spike() []float64 {
	return []float64{1, 1.1, 0.9, 1, 1.05, 0.95, 5, 6, 5.5, 6.2, 5.8, 6}
}

// shiftLeft — seriyi k kova sola kaydırır (aday önde): cand[i] = s[i+k].
func shiftLeft(s []float64, k int) []float64 {
	out := make([]float64, len(s))
	for i := range s {
		j := i + k
		if j >= len(s) {
			j = len(s) - 1
		}
		out[i] = s[j]
	}
	return out
}

func TestComputeTemporalFactor(t *testing.T) {
	t.Run("özdeş seri → in step, faktör 1", func(t *testing.T) {
		f := ComputeTemporalFactor(spike(), spike())
		if f.Lag != 0 || math.Abs(f.Factor-1) > 1e-9 || !strings.Contains(f.Reason, "in step") {
			t.Fatalf("%+v", f)
		}
	})
	t.Run("aday 1 kova önde → lag 1", func(t *testing.T) {
		f := ComputeTemporalFactor(spike(), shiftLeft(spike(), 1))
		if f.Lag != 1 || f.Factor < 0.9 || !strings.Contains(f.Reason, "leads by 1×5m") {
			t.Fatalf("%+v", f)
		}
	})
	t.Run("aday 2 kova önde → lag 2", func(t *testing.T) {
		f := ComputeTemporalFactor(spike(), shiftLeft(spike(), 2))
		if f.Lag != 2 || f.Factor < 0.8 {
			t.Fatalf("%+v", f)
		}
	})
	t.Run("aday tetikleyiciden SONRA sıçrar → ceza ya da uyum yok, faktör ≤ 0.5", func(t *testing.T) {
		// tetikleyici = aday'ın 1 önde hâli ⇒ aday tetikleyiciden sonra.
		f := ComputeTemporalFactor(shiftLeft(spike(), 1), spike())
		if f.Factor > 0.5 {
			t.Fatalf("sonradan sıçrayan aday ödüllendirildi: %+v", f)
		}
		if !strings.Contains(f.Reason, "after") && !strings.Contains(f.Reason, "no co-movement") {
			t.Fatalf("gerekçe: %+v", f)
		}
	})
	t.Run("düz seyirci → 0 (varyans yok)", func(t *testing.T) {
		flat := make([]float64, 12)
		for i := range flat {
			flat[i] = 2
		}
		f := ComputeTemporalFactor(spike(), flat)
		if f.Factor != 0 || !strings.Contains(f.Reason, "no co-movement") {
			t.Fatalf("%+v", f)
		}
	})
	t.Run("doğrusal rampa ikizi → 0 (fark sabit, sinyal yok)", func(t *testing.T) {
		a, b := make([]float64, 12), make([]float64, 12)
		for i := range a {
			a[i] = float64(i)
			b[i] = float64(i) * 3
		}
		if f := ComputeTemporalFactor(a, b); f.Factor != 0 {
			t.Fatalf("rampa ikizi korelasyon uydurdu: %+v", f)
		}
	})
	t.Run("yetersiz veri → nötr 0.5, lag −1", func(t *testing.T) {
		short := []float64{1, 1, 5, 5, 5}
		f := ComputeTemporalFactor(short, short)
		if f.Factor != TemporalNeutral || f.Lag != -1 || !strings.Contains(f.Reason, "insufficient") {
			t.Fatalf("%+v", f)
		}
		nan := spike()
		for i := 0; i < 7; i++ {
			nan[i] = math.NaN()
		}
		if f := ComputeTemporalFactor(spike(), nan); f.Factor != TemporalNeutral || f.Filled != 5 {
			t.Fatalf("NaN boşlukları dolu sayıldı: %+v", f)
		}
		if f := ComputeTemporalFactor(nil, spike()); f.Factor != TemporalNeutral {
			t.Fatalf("boş seri nötr olmalı: %+v", f)
		}
	})
	t.Run("NaN boşluklu ama yeterli seri ölçülür", func(t *testing.T) {
		c := spike()
		c[2] = math.NaN()
		f := ComputeTemporalFactor(spike(), c)
		if f.Lag == -1 || f.Factor <= 0.5 {
			t.Fatalf("%+v", f)
		}
	})
	t.Run("determinizm", func(t *testing.T) {
		a := ComputeTemporalFactor(spike(), shiftLeft(spike(), 1))
		b := ComputeTemporalFactor(spike(), shiftLeft(spike(), 1))
		if a != b {
			t.Fatalf("%+v vs %+v", a, b)
		}
	})
}

func TestSpearmanRanks(t *testing.T) {
	if r, ok := spearman([]float64{1, 2, 3, 4}, []float64{10, 20, 30, 40}); !ok || math.Abs(r-1) > 1e-9 {
		t.Fatalf("monoton artan → 1: %v %v", r, ok)
	}
	if r, ok := spearman([]float64{1, 2, 3, 4}, []float64{4, 3, 2, 1}); !ok || math.Abs(r+1) > 1e-9 {
		t.Fatalf("ters → −1: %v %v", r, ok)
	}
	if _, ok := spearman([]float64{1, 2}, []float64{1, 2}); ok {
		t.Fatal("2 çift ölçülmemeli")
	}
	if _, ok := spearman([]float64{1, 1, 1}, []float64{1, 2, 3}); ok {
		t.Fatal("sıfır varyans ölçülmemeli")
	}
	got := ranks([]float64{10, 20, 20, 5})
	want := []float64{2, 3.5, 3.5, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("eşit sıralar ortalama olmalı: %v", got)
		}
	}
	if onsetIndex([]float64{0.1, -1, 4, 4, 0.5}) != 2 {
		t.Fatal("onset = en büyük artışın ilk indeksi")
	}
	if onsetIndex([]float64{-1, -2, 0}) != -1 {
		t.Fatal("artış yoksa −1")
	}
}
