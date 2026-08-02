// v0.9.555 regresyon testi — MV kova hizalaması.
//
// Bug: service_summary_5m okumaları `WHERE time_bucket >= ?` diyordu ve
// `from` hiç hizalanmıyordu. MV kovaları BAŞLANGIÇLARIYLA etiketli
// olduğu için bu, "from'dan sonra BAŞLAYAN kovalar" anlamına gelir ve
// baştaki kısmi kovayı TAMAMEN eler: from=10:03 ise 10:00 kovası (yani
// 10:00–10:05 arasındaki tüm veri) düşer.
//
// Çağıranlar hizalanmamış pencere veriyor: copilot_aianalyze.go
// `to := time.Now(); from := to.Add(-rangeS)` — keyfi bir an.
//
// Zarar en çok olay incelemesinde: operatör pencereyi olayın başına
// ayarlar, olayın İLK DAKİKALARI görünmez olur ve yüzey tam patlama
// anında "anomali yok" der.
//
// Repo'nun doğru deseni zaten vardı (blast_radius.go:79,
// backtrace.go:133) ama summary.go'ya hiç uygulanmamıştı — bu test
// hizalamanın oradan da kaybolmamasını sağlıyor.
package chstore

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestAlignBucketStart(t *testing.T) {
	cases := []struct {
		name string
		in   time.Time
		want time.Time
	}{
		{
			"kova ortası aşağı iner",
			time.Date(2026, 8, 2, 10, 3, 27, 0, time.UTC),
			time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC),
		},
		{
			"tam kova sınırı DEĞİŞMEZ",
			time.Date(2026, 8, 2, 10, 5, 0, 0, time.UTC),
			time.Date(2026, 8, 2, 10, 5, 0, 0, time.UTC),
		},
		{
			"kovanın son saniyesi",
			time.Date(2026, 8, 2, 10, 9, 59, 0, time.UTC),
			time.Date(2026, 8, 2, 10, 5, 0, 0, time.UTC),
		},
		{
			"saat başı zaten hizalı",
			time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC),
			time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC),
		},
		{
			// Nanosaniye artığı da inmeli — DateTime64(9) bağlarında
			// alt-saniye hassasiyet taşınıyor.
			"nanosaniye artığı",
			time.Date(2026, 8, 2, 10, 7, 0, 123456789, time.UTC),
			time.Date(2026, 8, 2, 10, 5, 0, 0, time.UTC),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := alignBucketStart(c.in); !got.Equal(c.want) {
				t.Errorf("alignBucketStart(%v) = %v, beklenen %v", c.in, got, c.want)
			}
		})
	}
}

// TestAlignBucketNeverMovesForward — hizalama ASLA ileri gitmemeli.
// İleri giden bir hizalama, düzeltilen hatanın daha kötüsünü yapar:
// yalnız baştaki kovayı değil, istenen aralığın başını da kaybeder.
func TestAlignBucketNeverMovesForward(t *testing.T) {
	base := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	for sec := 0; sec < 600; sec += 7 {
		in := base.Add(time.Duration(sec) * time.Second)
		got := alignBucketStart(in)
		if got.After(in) {
			t.Fatalf("alignBucketStart(%v) = %v — İLERİ gitti", in, got)
		}
		if in.Sub(got) >= mvBucketWidth {
			t.Fatalf("alignBucketStart(%v) = %v — bir kovadan fazla geri gitti", in, got)
		}
	}
}

// TestSummaryReadersAlign — dört MV okumasının da hizalamayı çağırdığını
// sabitler. Biri unutulursa o yüzey sessizce olayın başını kaybeder ve
// belirti "veri yok" olarak görünür — yani yanlış yöne baktırır.
func TestSummaryReadersAlign(t *testing.T) {
	b, err := os.ReadFile("summary.go")
	if err != nil {
		t.Fatalf("summary.go okunamadı: %v", err)
	}
	src := string(b)

	// Kova aralığı soran her sorgu hizalanmış from kullanmalı.
	queries := strings.Count(src, "WHERE time_bucket >= ? AND time_bucket <= ?")
	aligns := strings.Count(src, "from = alignBucketStart(from)")
	if aligns < queries {
		t.Errorf("%d adet [from,to] kova sorgusu var ama yalnız %d hizalama — "+
			"hizalanmayan okuma, istenen pencerenin İLK kovasını tamamen eler "+
			"(olay incelemesinde onset görünmez olur)", queries, aligns)
	}
}
