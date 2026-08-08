package chstore

import (
	"encoding/json"
	"reflect"
	"testing"
)

// v0.9.800 — anomali motorunun izlediği metrik seti operatör ayarı
// oldu ve request_rate VARSAYILAN OLARAK KAPALI (operatör 2026-08-09:
// request_rate anomalileri false-pozitif).
//
// Bu bir DAVRANIŞ DEĞİŞİKLİĞİ, yani varsayılanın kendisi teste
// pinlenmeli: birinin "üç metrik de açık olmalı" diye varsayılanı geri
// açması sessiz bir regresyon olurdu (operatör aynı gürültüyü tekrar
// alır ve nedenini ayar sayfasında göremez, çünkü ayar hâlâ orada
// duruyor olur).

// TestDefaultAnomalyTrackedPinsRequestRateOff — varsayılan set.
func TestDefaultAnomalyTrackedPinsRequestRateOff(t *testing.T) {
	d := DefaultAnomalyTracked()
	want := map[string]bool{"error_rate": true, "p99_ms": true, "request_rate": false}
	for m, w := range want {
		if got := d[m]; got != w {
			t.Errorf("DefaultAnomalyTracked()[%q] = %v, want %v", m, got, w)
		}
	}
	if len(d) != len(AnomalyTrackedMetrics) {
		t.Errorf("varsayılan set %d anahtar taşıyor, kanonik liste %d: %v",
			len(d), len(AnomalyTrackedMetrics), d)
	}
	// Enabled() kanonik sırada ve request_rate'siz.
	if got := d.Enabled(); !reflect.DeepEqual(got, []string{"error_rate", "p99_ms"}) {
		t.Errorf("Default().Enabled() = %v, want [error_rate p99_ms]", got)
	}
}

// TestNormalizeAnomalyTracked — okuma yolunun tablo testi: bilinmeyen
// anahtar düşer, eksik anahtar varsayılanını alır, hepsi-kapalı set
// varsayılana kelepçelenir.
func TestNormalizeAnomalyTracked(t *testing.T) {
	cases := []struct {
		name string
		in   AnomalyTrackedConfig
		want AnomalyTrackedConfig
	}{
		{
			"boş blob → varsayılan",
			AnomalyTrackedConfig{},
			DefaultAnomalyTracked(),
		},
		{
			"nil blob → varsayılan",
			nil,
			DefaultAnomalyTracked(),
		},
		{
			"operatör request_rate'i açtı",
			AnomalyTrackedConfig{"error_rate": true, "p99_ms": true, "request_rate": true},
			AnomalyTrackedConfig{"error_rate": true, "p99_ms": true, "request_rate": true},
		},
		{
			"operatör p99'u kapattı — açık false korunur",
			AnomalyTrackedConfig{"error_rate": true, "p99_ms": false, "request_rate": false},
			AnomalyTrackedConfig{"error_rate": true, "p99_ms": false, "request_rate": false},
		},
		{
			"bilinmeyen anahtar düşer",
			AnomalyTrackedConfig{"error_rate": true, "p99_ms": false, "request_rate": false, "cpu_pct": true},
			AnomalyTrackedConfig{"error_rate": true, "p99_ms": false, "request_rate": false},
		},
		{
			"eksik anahtar varsayılanını alır (bu sürümden eski satır)",
			AnomalyTrackedConfig{"error_rate": false},
			AnomalyTrackedConfig{"error_rate": false, "p99_ms": true, "request_rate": false},
		},
		{
			"hepsi kapalı → varsayılana kelepçelenir (sessiz kapanma yok)",
			AnomalyTrackedConfig{"error_rate": false, "p99_ms": false, "request_rate": false},
			DefaultAnomalyTracked(),
		},
		{
			"yalnız bilinmeyen anahtarlar → hepsi kapalı değil, varsayılan doldurur",
			AnomalyTrackedConfig{"cpu_pct": true, "rss_mb": true},
			DefaultAnomalyTracked(),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NormalizeAnomalyTracked(c.in)
			if !reflect.DeepEqual(map[string]bool(got), map[string]bool(c.want)) {
				t.Errorf("NormalizeAnomalyTracked(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestFillAnomalyTrackedKeepsAllOff — API sınırının gördüğü hâl:
// Fill KELEPÇELEMEZ, yoksa "hepsi kapalı" isteği sessizce varsayılana
// dönüşür ve operatör kaydettiğinden başka bir şey görürdü. 400'ü
// üreten kapı bu ayrım.
func TestFillAnomalyTrackedKeepsAllOff(t *testing.T) {
	in := AnomalyTrackedConfig{"error_rate": false, "p99_ms": false, "request_rate": false}
	filled := FillAnomalyTracked(in)
	if filled.Any() {
		t.Fatalf("FillAnomalyTracked(hepsi kapalı).Any() = true, want false — API 400 üretemezdi")
	}
	if len(filled.Enabled()) == 0 {
		// Enabled() Normalize üzerinden gider: kelepçe orada devrede.
		t.Errorf("Enabled() boş döndü; okuma yolu varsayılana düşmeliydi")
	}
	// Kanonik listedeki her anahtar dolmuş olmalı.
	for _, m := range AnomalyTrackedMetrics {
		if _, ok := filled[m]; !ok {
			t.Errorf("FillAnomalyTracked %q anahtarını doldurmadı: %v", m, filled)
		}
	}
}

// TestAnomalyTrackedJSONShape — blob'un diskteki şekli düz map:
// {"error_rate":true,"p99_ms":true,"request_rate":false}. Şekil
// değişirse eski satırlar okunamaz hâle gelirdi.
func TestAnomalyTrackedJSONShape(t *testing.T) {
	raw, err := json.Marshal(DefaultAnomalyTracked())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back AnomalyTrackedConfig
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if !reflect.DeepEqual(map[string]bool(back), map[string]bool(DefaultAnomalyTracked())) {
		t.Errorf("round-trip %s = %v", raw, back)
	}
	// Elle yazılmış bir satır da okunmalı.
	var handEdited AnomalyTrackedConfig
	if err := json.Unmarshal([]byte(`{"error_rate":true,"p99_ms":true,"request_rate":true}`), &handEdited); err != nil {
		t.Fatalf("hand-edited unmarshal: %v", err)
	}
	if got := handEdited.Enabled(); len(got) != 3 {
		t.Errorf("elle açılmış üçlü set Enabled() = %v, want 3 metrik", got)
	}
}

// TestStoreAnomalyTrackedPublish — atomic yayının nil-güvenliği:
// hiç hidrate edilmemiş bir Store varsayılanı verir (hidrasyondan
// önceki ilk anomali tiki bu daldan geçer), yayınlanan set geri
// okunur.
func TestStoreAnomalyTrackedPublish(t *testing.T) {
	var s Store
	if got := s.AnomalyTracked().Enabled(); !reflect.DeepEqual(got, []string{"error_rate", "p99_ms"}) {
		t.Errorf("hidrate edilmemiş Store.AnomalyTracked().Enabled() = %v, want [error_rate p99_ms]", got)
	}
	s.SetAnomalyTracked(AnomalyTrackedConfig{"error_rate": true, "p99_ms": true, "request_rate": true})
	if got := s.AnomalyTracked().Enabled(); len(got) != 3 {
		t.Errorf("yayından sonra Enabled() = %v, want 3 metrik", got)
	}
	// Kelepçe yayında da geçerli: hepsi kapalı → varsayılan.
	s.SetAnomalyTracked(AnomalyTrackedConfig{"error_rate": false, "p99_ms": false, "request_rate": false})
	if got := s.AnomalyTracked().Enabled(); !reflect.DeepEqual(got, []string{"error_rate", "p99_ms"}) {
		t.Errorf("hepsi-kapalı yayını sonrası Enabled() = %v, want varsayılan", got)
	}
}
