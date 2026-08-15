package chstore

import (
	"strings"
	"testing"
)

// v0.9.1062 (Faz 2.1 / K2) regresyon pini — skorlama/reasons mantığı TEK
// saf fonksiyonda: ham-spans okuyucusu da MV okuyucusu da
// scoreChangedService'ten geçer. Bu tablo skor formülünü, reasons
// kapılarını ve "değişim yoksa satır yok" kuralını mühürler — iki
// okuyucudan birinin sessizce kendi kopyasını türetmesi (v0.9.4
// trendSeries dersi: iki tutarsız implementasyon) burada patlar.
func TestScoreChangedService(t *testing.T) {
	t.Run("hata sıçraması: skor errAbs*4, reason error-rate", func(t *testing.T) {
		// err %1→%6 (=5 puan), rate/p99 sabit → skor = 5*4 = 20.
		c, ok := scoreChangedService("svc", 1000, 1000, 10, 60, 100, 100, 100, 100)
		if !ok {
			t.Fatal("kayda değer değişim atlandı")
		}
		if c.Score < 19.9 || c.Score > 20.1 {
			t.Fatalf("score=%.2f, want ~20", c.Score)
		}
		if len(c.Reasons) != 1 || !strings.Contains(c.Reasons[0], "error rate") {
			t.Fatalf("reasons=%v", c.Reasons)
		}
	})

	t.Run("değişimsiz servis listeye girmez", func(t *testing.T) {
		if _, ok := scoreChangedService("svc", 1000, 1000, 10, 10, 100, 100, 100, 100); ok {
			t.Fatal("değişimsiz satır ok=true döndü — liste şişer")
		}
	})

	t.Run("düşük hacimde rate/p99 reason kapısı kapalı (>100 span şartı)", func(t *testing.T) {
		// %50 rate düşüşü ama toplam 90 span → reason yok → satır yok.
		if _, ok := scoreChangedService("svc", 60, 30, 0, 0, 100, 100, 60, 60); ok {
			t.Fatal("düşük hacim gürültüsü listeye girdi")
		}
	})

	t.Run("p99 patlaması reason + soft-cap 200", func(t *testing.T) {
		// p99 100→1000ms (+900%), 200'e kırpılır → skor 100.
		c, ok := scoreChangedService("svc", 1000, 1000, 0, 0, 100, 1000, 100, 100)
		if !ok || c.Score < 99.9 || c.Score > 100.1 {
			t.Fatalf("ok=%v score=%.2f, want ~100", ok, c.Score)
		}
		if !strings.Contains(strings.Join(c.Reasons, ";"), "P99") {
			t.Fatalf("P99 reason yok: %v", c.Reasons)
		}
	})

	t.Run("rank: skor desc + 20 tavanı", func(t *testing.T) {
		var in []ChangedService
		for i := 0; i < 25; i++ {
			in = append(in, ChangedService{Service: "s", Score: float64(i)})
		}
		out := rankChangedServices(in)
		if len(out) != 20 || out[0].Score != 24 || out[19].Score != 5 {
			t.Fatalf("rank bozuk: len=%d first=%.0f last=%.0f", len(out), out[0].Score, out[19].Score)
		}
	})
}
