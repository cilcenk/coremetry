package chstore

// v0.9.1191 — spool runbook'unun chstore yarısı: hedef doğrulama + komut
// metinleri. SYSTEM komutuna giren tek değişken tablo adı; bu testlerin
// işi o adın asla serbest metin olamayacağını çivilemek.

import (
	"strings"
	"testing"
)

func TestCHIdentRe(t *testing.T) {
	valid := []string{"spans", "metric_points", "span_links", "_t", "T2"}
	for _, v := range valid {
		if !chIdentRe.MatchString(v) {
			t.Errorf("%q geçerli sayılmalıydı", v)
		}
	}
	invalid := []string{
		"", " ", "spans;DROP", "sp ans", "spans`", "`spans`",
		"db.spans",          // nokta: veritabanı niteleme burada YASAK
		"spans--x", "spa'n", // yorum/tırnak enjeksiyon adayları
		"1spans", // rakamla başlayamaz
		"öçş",    // ASCII dışı — telemetri tabloları hep ASCII
	}
	for _, v := range invalid {
		if chIdentRe.MatchString(v) {
			t.Errorf("%q REDDEDİLMELİYDİ", v)
		}
	}
}

// TestSpoolCommandsRefuseBadNames — komut fonksiyonları regex'i KENDİ
// içinde de koşar (savunma katmanları bağımsız: API'nin membership
// kontrolü atlansa bile bozuk ad SQL'e ulaşamaz). nil-conn'lu Store{}
// ile çağrılıyor: ad reddedilirse bağlantıya HİÇ dokunulmaz — test tam
// bunu kanıtlar, geçerli adla çağırsak nil conn panik/hata verirdi.
func TestSpoolCommandsRefuseBadNames(t *testing.T) {
	s := &Store{}
	for _, bad := range []string{"spans`; DROP TABLE spans; --", "a b", ""} {
		if err := s.FlushDistributed(nil, bad); err == nil || !strings.Contains(err.Error(), "geçersiz tablo adı") {
			t.Errorf("FlushDistributed(%q) reddetmeliydi: %v", bad, err)
		}
		if err := s.StartDistributedSends(nil, bad); err == nil || !strings.Contains(err.Error(), "geçersiz tablo adı") {
			t.Errorf("StartDistributedSends(%q) reddetmeliydi: %v", bad, err)
		}
	}
}

// TestFlushUsesDedicatedLongConn — FLUSH ana havuzdan KOŞAMAZ: havuzun
// 30 sn'lik ReadTimeout'u (v0.8.340) saatlik senkron flush'ı keser ve
// kopan bağlantı sunucudaki flush'ı da iptal ederdi. Sözleşme: chOpts
// fabrikası yoksa (Store{} kuran testler / bozuk kurulum) flush düğmesi
// dürüstçe "yok" der, sessizce ana havuza DÜŞMEZ.
func TestFlushUsesDedicatedLongConn(t *testing.T) {
	s := &Store{} // chOpts nil
	err := s.FlushDistributed(nil, "spans")
	if err == nil || !strings.Contains(err.Error(), "uzun-işlem bağlantısı") {
		t.Fatalf("chOpts'suz flush açık hata vermeli (ana havuza düşmek 30sn "+
			"ReadTimeout'ta kopmak demek): %v", err)
	}
	if spoolFlushReadTimeout < 12*3600*1e9 {
		t.Error("flush ReadTimeout'u saatler mertebesinde olmalı — 3M dosyalık " +
			"spool'un senkron flush'ı bundan kısa sürede bitmez")
	}
}
