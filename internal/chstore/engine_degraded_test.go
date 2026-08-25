package chstore

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// v0.10.11 — kusurun kendisi HATA VERMİYORDU (yutulan alt-sorgu hatası →
// sıfırlarla dolu, inandırıcı bir KPI ızgarası). Düzeltmenin çekirdeği de
// bu yüzden CH'siz, saf ve tablo-testli: bir gün biri `step`i
// atlarsa/yutarsa, testin görmesi gerek.

func TestDegradeTracker(t *testing.T) {
	t.Run("hepsi başarılı — bozulma YOK", func(t *testing.T) {
		var d degradeTracker
		for _, n := range []string{"gauges", "sessions", "waits"} {
			if !d.step(n, nil) {
				t.Errorf("%s: step hatasız çağrıda false döndü", n)
			}
		}
		if h := d.health(); h.Degraded || h.DegradedReason != "" {
			t.Errorf("bozulma yokken health = %+v", h)
		}
	})

	t.Run("tek düşüş — oran ve ad taşınır", func(t *testing.T) {
		var d degradeTracker
		d.step("gauges", nil)
		if d.step("sessions", errors.New("ch timeout")) {
			t.Error("hatalı adımda step true döndü")
		}
		d.step("waits", nil)
		h := d.health()
		if !h.Degraded {
			t.Fatal("bir okuma düştü ama Degraded false")
		}
		for _, want := range []string{"1/3", "sessions", "EKSİK"} {
			if !strings.Contains(h.DegradedReason, want) {
				t.Errorf("sebep %q içinde %q yok", h.DegradedReason, want)
			}
		}
	})

	// ⚠ ANAHTAR SÖZLEŞME. Sebep dizgesi operatör ekranına gidiyor; alttaki
	// hata metni bağlantı dizgesi, SQL ya da host adı taşıyabilir. Teşhis
	// kanalı sızıntı kanalı olmamalı — v0.10.4'te zayıf anahtar için
	// verdiğim kararın aynısı.
	t.Run("ham hata metni SIZMAZ", func(t *testing.T) {
		var d degradeTracker
		d.step("gauges", errors.New("dial tcp 10.1.2.3:9000: connection refused"))
		r := d.health().DegradedReason
		for _, leak := range []string{"10.1.2.3", "dial tcp", "connection refused"} {
			if strings.Contains(r, leak) {
				t.Errorf("sebep ham hatadan %q sızdırıyor: %q", leak, r)
			}
		}
		if !strings.Contains(r, "gauges") {
			t.Errorf("sebep hangi okumanın düştüğünü söylemiyor: %q", r)
		}
	})

	t.Run("çok düşüş — ad listesi SINIRLI, kalan sayılır", func(t *testing.T) {
		var d degradeTracker
		for _, n := range []string{"a", "b", "c", "d", "e"} {
			d.step(n, errors.New("x"))
		}
		r := d.health().DegradedReason
		if !strings.Contains(r, "5/5") {
			t.Errorf("oran yok: %q", r)
		}
		// İlk üç ad + "+2": bir arayüz şeridine on beş ad basmak,
		// operatörün okumayacağı bir duvar olurdu.
		if !strings.Contains(r, "+2") {
			t.Errorf("kalan sayılmamış: %q", r)
		}
		if strings.Contains(r, "e") && strings.Contains(r, "d") {
			t.Errorf("sınır uygulanmamış, tüm adlar basılmış: %q", r)
		}
	})

	t.Run("sıfır adım — bozulma YOK (boş küme tuzağı)", func(t *testing.T) {
		// Hiç okuma yapılmadıysa "0/0 düştü" demek yanlış olurdu.
		var d degradeTracker
		if h := d.health(); h.Degraded {
			t.Errorf("hiç adım yokken Degraded true: %+v", h)
		}
	})
}

func TestJoinUpTo(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		n    int
		want string
	}{
		{"sınırın altında hepsi", []string{"a", "b"}, 3, "a, b"},
		{"sınırda hepsi", []string{"a", "b", "c"}, 3, "a, b, c"},
		{"sınırın üstünde kırpılır", []string{"a", "b", "c", "d"}, 3, "a, b, c +1"},
		{"tek", []string{"a"}, 3, "a"},
		{"boş", nil, 3, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := joinUpTo(tc.in, tc.n); got != tc.want {
				t.Errorf("joinUpTo(%v, %d) = %q; want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

// TestEveryEngineReaderReportsHealth — saf çekirdek yeşil ama BAĞLANTI
// pinli değilse kusur yerinde kalır.
//
// Bu testi kendi mutasyon denetimim doğurdu: `out.EngineHealth =
// deg.health()` satırını oracle.go'dan sildiğimde yukarıdaki tablo
// testleri YEŞİL kaldı. İzleyici kusursuz çalışıyor, yalnız kimse
// sonucunu okumuyor — düzeltilmiş görünen, düzeltilmemiş bir kod.
//
// Kaynak taraması, çünkü dört okuyucu da canlı ClickHouse istiyor;
// korunması gereken şey ise DAVRANIŞ değil, KABLOLAMA.
func TestEveryEngineReaderReportsHealth(t *testing.T) {
	for _, eng := range []string{"oracle", "postgres", "mysql", "redis"} {
		t.Run(eng, func(t *testing.T) {
			b, err := os.ReadFile(eng + ".go")
			if err != nil {
				t.Fatalf("%s.go okunamadı: %v", eng, err)
			}
			src := string(b)
			if !strings.Contains(src, "var deg degradeTracker") {
				t.Errorf("%s.go izleyiciyi KURMUYOR — alt-sorgu hataları yine yutuluyor", eng)
			}
			if !strings.Contains(src, "out.EngineHealth = deg.health()") {
				t.Errorf("%s.go izleyiciyi kuruyor ama SONUCUNU YAZMIYOR — "+
					"bozulma tespit ediliyor, hiçbir yere ulaşmıyor", eng)
			}
			// Her okuyucunun en az üç alt-sorgusu var; birini bile
			// atlamak o okumanın sessizce yutulmaya devam etmesi demek.
			if n := strings.Count(src, "deg.step("); n < 3 {
				t.Errorf("%s.go yalnız %d okuma izliyor — alt-sorgu sayısından az", eng, n)
			}
		})
	}
}
