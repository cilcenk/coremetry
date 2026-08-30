package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.10.195 sözleşmesi (sample_slices.go): servis kotası pencerenin
// dilimlerine eşit dağılır; toplam kota servis kotasının altına düşmez;
// kısa pencere tek dilim kalır.
func TestSampleSlices(t *testing.T) {
	cases := []struct {
		name       string
		windowSec  int64
		perService int
		bucketSec  int64
		perBucket  int
	}{
		{"1 saat / 400 → 5 dk dilim, 34/dilim (408 ≥ 400)", 3600, 400, 300, 34},
		{"15 dk / 400 → 75 s dilim", 900, 400, 75, 34},
		{"6 saat / 400 → 30 dk dilim", 21600, 400, 1800, 34},
		{"24 saat / 500 (pod envanteri) → 2 saat dilim, 42/dilim", 86400, 500, 7200, 42},
		{"1 dk pencere → tek dilim, kota bölünmez", 60, 400, 60, 400},
		{"2 dk pencere → taban 60 s, 2 dilim", 120, 400, 60, 200},
		{"sıfır pencere → taban dilim, tam kota", 0, 400, 60, 400},
		{"kota 0 → 1'e çekilir", 3600, 0, 300, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, p := sampleSlices(tc.windowSec, tc.perService)
			if b != tc.bucketSec || p != tc.perBucket {
				t.Fatalf("got (%d, %d), want (%d, %d)", b, p, tc.bucketSec, tc.perBucket)
			}
			if tc.windowSec > 0 && tc.perService > 0 {
				slices := tc.windowSec / b
				if slices < 1 {
					slices = 1
				}
				if int64(p)*slices < int64(tc.perService) {
					t.Fatalf("toplam kota %d < servis kotası %d", int64(p)*slices, tc.perService)
				}
			}
		})
	}
}

// TestSampleSlicesWired — kaynak kapısı ([[feedback-tested-but-unreachable]]):
// iki örnekleyen sorgu da kotayı zaman dilimine BÖLÜYOR; `LIMIT n BY
// service_name` tek başına (önek örneklemesi) geri gelmesin.
func TestSampleSlicesWired(t *testing.T) {
	for _, f := range []string{"k8s_coverage.go", "pod_inventory.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if !strings.Contains(src, "BY service_name, toStartOfInterval(time, INTERVAL %d SECOND)") {
			t.Errorf("%s: kota zaman dilimine bölünmüyor (v0.10.195)", f)
		}
		if !strings.Contains(src, "sampleSlices(") {
			t.Errorf("%s: sampleSlices çağrılmıyor", f)
		}
	}
}
