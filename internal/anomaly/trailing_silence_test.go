package anomaly

import "testing"

// v0.9.449 (hacim denetimi L3 "donmuş kuyruk") — GROUP BY yalnız veri
// olan bucket'ları döndürür; susan servisin son dolu bucket'ı "şimdi"
// muamelesi görüyor, saatler önceki spike anomali last_seen'ini 24
// saate dek taze tutuyordu. Kuyruk sıfır dolgusu gerçek sessizliğin
// dürüst temsili: current=0 → z sönümlenir → event 10dk'da clear →
// v0.9.444 resolve pass'i terfi problemini kapatır.
func TestPadTrailingSilence(t *testing.T) {
	const step = int64(300)
	base := int64(1_753_900_800) // 5-dk grid'e hizalı
	cases := []struct {
		name    string
		series  []float64
		lastT   int64
		upper   int64
		wantLen int
	}{
		{"susmamış servis: son bucket pencere ucunda → dolgu yok",
			[]float64{1, 2, 3}, base, base + step, 3},
		{"1 saat sessizlik → 11 sıfır (upper exclusive)",
			[]float64{1, 2, 3}, base, base + 12*step, 3 + 11},
		{"tek bucket sessizlik → 0 dolgu (last+step == upper)",
			[]float64{5}, base, base + step, 1},
		{"iki bucket → 1 sıfır",
			[]float64{5}, base, base + 2*step, 2},
		{"boş seri → dokunma",
			nil, 0, base + 12*step, 0},
		{"lastT yok (0) → dokunma",
			[]float64{1}, 0, base + 12*step, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := padTrailingSilence(tc.series, tc.lastT, tc.upper)
			if len(got) != tc.wantLen {
				t.Fatalf("len = %d, want %d", len(got), tc.wantLen)
			}
			for i := len(tc.series); i < len(got); i++ {
				if got[i] != 0 {
					t.Errorf("dolgu[%d] = %v, want 0", i, got[i])
				}
			}
		})
	}
}
