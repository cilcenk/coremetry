package assemble

// v0.9.1187 (AI Faz 4.5, K3) — geçmiş bütçesi tablo testi.
//
// Eski davranış yalnız "son 40 tur"du ve 40 sayısı BOYUT hakkında hiçbir
// şey söylemiyor: yapıştırılmış bir log yığını ya da modelin uzun cevapları
// 40 turu rahatça bir 2B modelin bağlamının üstüne çıkarıyordu. Belirtisi
// kırılma değil, TAZE KANITIN eski konuşma tarafından bağlamdan atılmasıydı
// — yani sessiz ve yanlış cevap.
//
// Buradaki iddiaların hepsi köşe: bütçe tam dolduğunda, tek tur bütçeyi
// aştığında, sayı tavanı rune tavanından önce ısırdığında ne olduğu. Orta
// vakalar zaten çalışır; bu fonksiyon yalnız köşelerde yanlış olabilir.

import (
	"strings"
	"testing"
)

func TestClampHistory(t *testing.T) {
	cases := []struct {
		name        string
		lens        []int
		maxTurns    int
		maxRunes    int
		wantKeep    int
		wantTrimmed int
	}{
		{"boş geçmiş", nil, 40, 6000, 0, 0},
		{"her şey sığar", []int{100, 100, 100}, 40, 6000, 3, 0},
		{
			// Bütçe TAM doluyor — sınırda kırpma yok.
			"bütçe tam oturur", []int{2000, 2000, 2000}, 40, 6000, 3, 0,
		},
		{
			// Bir rune fazla: EN ESKİ düşer, yeniler kalır.
			"bir rune taşar → en eski düşer", []int{2001, 2000, 2000}, 40, 6000, 2, 1,
		},
		{
			// SAYI tavanı rune tavanından önce ısırır.
			"sayı tavanı önce ısırır", []int{1, 1, 1, 1, 1}, 2, 6000, 2, 3,
		},
		{
			// EN AZ BİR TUR köşesi: son tur tek başına bütçeyi aşsa da kalır.
			// Düşürmek, cevaplanacak SORUYU silmek olurdu.
			"dev tek tur yine de kalır", []int{99999}, 40, 6000, 1, 0,
		},
		{
			// Son tur bütçeyi aştı → yalnız o kalır, öncekiler düşer.
			"dev son tur öncekileri yutar", []int{100, 100, 99999}, 40, 6000, 1, 2,
		},
		{
			// Negatif/bozuk uzunluk 0 sayılır (savunmacı; panik yerine).
			"negatif uzunluk sıfır sayılır", []int{-5, 10}, 40, 6000, 2, 0,
		},
		{"sıfır maxTurns tüm turlara izin verir", []int{1, 1, 1}, 0, 6000, 3, 0},
		{"sıfır maxRunes varsayılana düşer", []int{1, 1}, 40, 0, 2, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keep, trimmed := ClampHistory(c.lens, c.maxTurns, c.maxRunes)
			if keep != c.wantKeep || trimmed != c.wantTrimmed {
				t.Errorf("ClampHistory(%v, %d, %d) = (keep %d, trimmed %d), "+
					"beklenen (%d, %d)", c.lens, c.maxTurns, c.maxRunes,
					keep, trimmed, c.wantKeep, c.wantTrimmed)
			}
			// Değişmez: keep+trimmed her zaman girdi uzunluğu — hiçbir tur
			// iki kez sayılmaz ve hiçbiri kaybolmaz.
			if keep+trimmed != len(c.lens) {
				t.Errorf("keep(%d)+trimmed(%d) != len(%d)", keep, trimmed, len(c.lens))
			}
		})
	}
}

// TestClampHistoryKeepsNewest — kesim KUYRUKTAN alınır, baştan değil.
// Baştan almak aktif soruyu düşürürdü; bu, "kaç tur" kararının hangi
// UÇTAN sayıldığını çiviliyor.
func TestClampHistoryKeepsNewest(t *testing.T) {
	// En eski tur devasa, yeniler küçük: bütçe yenileri almalı.
	keep, trimmed := ClampHistory([]int{9000, 10, 10, 10}, 40, 6000)
	if keep != 3 || trimmed != 1 {
		t.Fatalf("keep=%d trimmed=%d — en yeni 3 tur kalmalıydı", keep, trimmed)
	}
}

// TestClampHistoryDeterministic — aynı girdi her zaman aynı kesim.
// Zamana ya da rastgeleliğe bağlı bir bütçe, "dün çalışıyordu" sınıfı
// hataların kaynağı olurdu.
func TestClampHistoryDeterministic(t *testing.T) {
	lens := []int{500, 1500, 2500, 3500, 100}
	first, firstTrim := ClampHistory(lens, 40, 6000)
	for i := 0; i < 50; i++ {
		if k, tr := ClampHistory(lens, 40, 6000); k != first || tr != firstTrim {
			t.Fatalf("deterministik değil: (%d,%d) vs (%d,%d)", k, tr, first, firstTrim)
		}
	}
}

func TestRuneLens(t *testing.T) {
	// Çok baytlı karakterler BAYT değil RUNE sayılır — Türkçe metinde
	// bayt saymak bütçeyi ~1.5× dar gösterirdi.
	got := RuneLens([]string{"", "abc", "şğüöçİ"})
	want := []int{0, 3, 6}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("RuneLens[%d] = %d, beklenen %d", i, got[i], want[i])
		}
	}
}

func TestTrimNote(t *testing.T) {
	if TrimNoteIfNeeded(0) != "" {
		t.Error("kırpma yokken işaret üretilmemeli")
	}
	note := TrimNoteIfNeeded(3)
	if note == "" || !HasTrimNote(note) {
		t.Error("kırpma varken işaret üretilmeli ve tanınmalı")
	}
	if HasTrimNote("düz bir cevap metni") {
		t.Error("işaretsiz metin işaretli sayılmamalı")
	}
	// İşaret modele ne YAPMAMASI gerektiğini söylemeli — yalnız "kırpıldı"
	// demek, modelin eski turlara atıf yapmasını engellemez.
	if !strings.Contains(note, "atıfta bulunma") {
		t.Errorf("işaret davranış talimatı taşımalı: %q", note)
	}
}
