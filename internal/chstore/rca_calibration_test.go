package chstore

import (
	"strings"
	"testing"
)

// v0.10.410 — CoSRE denetimi E4: güven kovaları tek kaynaktan. Sınır
// SQL'de bir, JSON'da başka olsaydı üç kova toplama eşit çıkmaz ve panel
// sessizce yalan söylerdi. Bitişiklik + kapsama + yüklem/sınır uyumu.
func TestRCAConfidenceBucketsContiguous(t *testing.T) {
	bs := rcaConfidenceBuckets()
	if len(bs) != 3 {
		t.Fatalf("3 kova bekleniyor, %d", len(bs))
	}
	if bs[0].Lo != 0 || bs[len(bs)-1].Hi != 1 {
		t.Fatalf("[0,1] kapsanmalı: %v..%v", bs[0].Lo, bs[len(bs)-1].Hi)
	}
	for i := 1; i < len(bs); i++ {
		if bs[i].Lo != bs[i-1].Hi {
			t.Errorf("kova %s alt sınırı %v, önceki üst %v — boşluk/örtüşme", bs[i].Bucket, bs[i].Lo, bs[i-1].Hi)
		}
	}
	// "high" yalnız çürütme-destekli: alt sınır = kalkan tavanı 0.6, HARİÇ.
	if bs[2].Lo != rcaConfBucketHigh || !strings.Contains(bs[2].sqlPred, "> 0.60") {
		t.Errorf("high kovası > 0.60 olmalı: %q", bs[2].sqlPred)
	}
	if !strings.Contains(bs[1].sqlPred, ">= 0.40") || !strings.Contains(bs[1].sqlPred, "<= 0.60") {
		t.Errorf("mid kovası [0.40, 0.60] olmalı: %q", bs[1].sqlPred)
	}
	if !strings.Contains(bs[0].sqlPred, "< 0.40") {
		t.Errorf("low kovası < 0.40 olmalı: %q", bs[0].sqlPred)
	}
	sel := rcaCalibrationSelect()
	if n := strings.Count(sel, "toUInt64(countIf(v.parsed = 1 AND"); n != 9 {
		t.Fatalf("9 kova sütunu bekleniyor (3 kova × n/up/down), %d", n)
	}
	for _, name := range []string{"cal_low_n", "cal_mid_up", "cal_high_down"} {
		if !strings.Contains(sel, name) {
			t.Errorf("sütun %s yok", name)
		}
	}
}
