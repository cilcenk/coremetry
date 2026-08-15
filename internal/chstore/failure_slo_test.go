// v0.9.1036 — failure-rate SLO blob'unun normalize kapısı.
//
// Neden test: bu blob elle düzenlenebilir bir system_settings satırıdır
// (operatör ya da bir import), yani okuma yolu HER ZAMAN saçma girdiyle
// karşılaşabilir. Negatif bir yüzde grafiğin ekseninin altına düşen bir
// çizgi çizerdi; 100'ün üstü görünmez bir çizgi; adsız bir override ise
// "kaydettim ama çalışmıyor" sınıfı sessiz bir arıza.
//
// Sıfır-struct dalı ayrıca problem_priority'nin (v0.9.838) dersini
// taşıyor: DefaultPct'te 0 ANLAMLI bir değer ("çizgi yok"), o yüzden
// alan-alan "0 ise varsayılan" YAPILAMAZ — bütün olarak yakalanır.
package chstore

import (
	"reflect"
	"testing"
)

func TestNormalizeFailureSLO(t *testing.T) {
	cases := []struct {
		name string
		in   FailureSLOConfig
		want FailureSLOConfig
	}{
		{
			name: "sıfır struct → varsayılan (hiç doldurulmamış)",
			in:   FailureSLOConfig{},
			want: FailureSLOConfig{DefaultPct: 1},
		},
		{
			name: "açıkça 0 + override → 0 KORUNUR (çizgi kapalı)",
			in:   FailureSLOConfig{DefaultPct: 0, Overrides: map[string]float64{"api": 2}},
			want: FailureSLOConfig{DefaultPct: 0, Overrides: map[string]float64{"api": 2}},
		},
		{
			name: "negatif varsayılan 0'a kelepçelenir",
			in:   FailureSLOConfig{DefaultPct: -5},
			want: FailureSLOConfig{DefaultPct: 0},
		},
		{
			name: "100 üstü varsayılan 100'e kelepçelenir",
			in:   FailureSLOConfig{DefaultPct: 250},
			want: FailureSLOConfig{DefaultPct: 100},
		},
		{
			name: "override'lar da kelepçelenir",
			in: FailureSLOConfig{DefaultPct: 1, Overrides: map[string]float64{
				"a": -3, "b": 500, "c": 2.5,
			}},
			want: FailureSLOConfig{DefaultPct: 1, Overrides: map[string]float64{
				"a": 0, "b": 100, "c": 2.5,
			}},
		},
		{
			name: "adsız / boşluk-adlı override DÜŞER",
			in: FailureSLOConfig{DefaultPct: 1, Overrides: map[string]float64{
				"": 9, "   ": 9, "api": 3,
			}},
			want: FailureSLOConfig{DefaultPct: 1, Overrides: map[string]float64{"api": 3}},
		},
		{
			name: "ad kırpılır (kopyala-yapıştır boşluğu sessiz ıskalardı)",
			in:   FailureSLOConfig{DefaultPct: 1, Overrides: map[string]float64{"  api  ": 3}},
			want: FailureSLOConfig{DefaultPct: 1, Overrides: map[string]float64{"api": 3}},
		},
		{
			name: "yalnız adsız override → harita nil'e düşer, varsayılan korunur",
			in:   FailureSLOConfig{DefaultPct: 4, Overrides: map[string]float64{"": 9}},
			want: FailureSLOConfig{DefaultPct: 4},
		},
		{
			name: "boş harita nil'e normalize olur (JSON'da omitempty)",
			in:   FailureSLOConfig{DefaultPct: 2, Overrides: map[string]float64{}},
			want: FailureSLOConfig{DefaultPct: 2},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NormalizeFailureSLO(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("NormalizeFailureSLO(%+v)\n got = %+v\nwant = %+v", c.in, got, c.want)
			}
		})
	}
}

// Override adları audit satırına giriyor; harita iterasyonu Go'da
// rastgeledir ve rastgele sıralı bir detay iki özdeş kaydı farklı
// gösterirdi.
func TestFailureSLOOverrideServicesSorted(t *testing.T) {
	c := FailureSLOConfig{Overrides: map[string]float64{"zeta": 1, "alpha": 2, "mid": 3}}
	got := FailureSLOOverrideServices(c)
	want := []string{"alpha", "mid", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if FailureSLOOverrideServices(FailureSLOConfig{}) != nil {
		t.Fatal("boş config nil dönmeli")
	}
}
