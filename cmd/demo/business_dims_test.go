package main

import (
	"fmt"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// v0.9.629 — bu testler DEMO'yu değil, demonun KAPSAMINI koruyor.
//
// v0.9.621-628'de kapatılan iki hata sınıfı lokalde yapısal olarak
// görünemiyordu: hiçbir demo üreteci channel_code/function_code
// yaymıyordu ve hepsi eski semconv adlarını basıyordu. Bir hatanın
// kendini gösterememesi en pahalı hâli — hangi denetim koşarsa koşsun
// veri yoksa bulunamaz.

// Kanal kodu prod'un yazımıyla, yani KÜÇÜK harf olmalı. Büyük harf
// yazılsaydı v0.9.621'in düzelttiği uyuşmazlık lokalde yine görünmezdi.
func TestBusinessDimsUseLowercaseKeys(t *testing.T) {
	got := withBusinessDims(nil, "030101", "F1234")
	if _, ok := got["channel_code"]; !ok {
		t.Fatalf("küçük harf 'channel_code' bekleniyordu: %v", got)
	}
	if _, ok := got["CHANNEL_CODE"]; ok {
		t.Fatal("BÜYÜK harf yazım demo'ya sızmış — prod küçük harf yazıyor")
	}
}

// Kanal dağılımı ÇARPIK olmalı: düz uniform sekiz eşit çubuk kırılım
// panelini anlamsız kılar ve LowCardinality kolonun gerçek davranışını
// yansıtmaz.
func TestChannelDistributionIsSkewed(t *testing.T) {
	counts := map[string]int{}
	for i := 0; i < 20000; i++ {
		counts[pickChannelCode()]++
	}
	if len(counts) < 5 {
		t.Fatalf("kuyruk kanalları hiç seçilmiyor, görülen: %v", counts)
	}
	top := counts["030101"]
	tail := counts["070707"]
	if top <= tail*3 {
		t.Fatalf("dağılım çarpık değil: en hakim %d, en seyrek %d", top, tail)
	}
}

// function_code AYRI bir kardinalite sınıfı: rollup tasarımında düz
// String (LowCardinality DEĞİL). Demo tek haneli bir küme üretirse
// lokalde "her iki boyut da düşük kardinaliteli" görünür ve rollup'ın
// kardinalite kararı hiç sınanmaz.
func TestFunctionCodeHasHigherCardinality(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 5000; i++ {
		seen[pickFunctionCode()] = true
	}
	if len(seen) < 100 {
		t.Fatalf("function_code kardinalitesi çok düşük: %d farklı değer", len(seen))
	}
}

// Filo KARIŞIK konuşmalı: hepsi modern olsaydı eski yol, hiçbiri
// olmasaydı yeni yol sınanmazdı. v0.9.628 ikisini de tanımak zorunda.
func TestFleetSpeaksBothSemconvDialects(t *testing.T) {
	svcs := []string{
		"api-gateway", "checkout", "ledger-service", "transfer-service",
		"mobile-bff", "fraud-detector", "notification", "inventory",
		"auth-service", "reporting", "search", "cart",
	}
	modern, legacy := 0, 0
	for _, s := range svcs {
		if serviceSpeaksModernSemconv(s) {
			modern++
		} else {
			legacy++
		}
	}
	if modern == 0 || legacy == 0 {
		t.Fatalf("filo tek lehçe konuşuyor: modern=%d legacy=%d", modern, legacy)
	}
}

// Seçim DETERMİNİSTİK: aynı servis her trace'te aynı lehçeyi konuşmalı.
// Rastgele olsaydı gerçek bir dağıtımda olmayan bir şey üretirdi ve
// hata ayıklarken yanıltırdı.
func TestSemconvDialectIsStablePerService(t *testing.T) {
	first := serviceSpeaksModernSemconv("checkout")
	for i := 0; i < 50; i++ {
		if serviceSpeaksModernSemconv("checkout") != first {
			t.Fatal("aynı servis için lehçe kararı değişiyor")
		}
	}
}

func TestApplySemconvDialectRenamesOnlyKeys(t *testing.T) {
	// Modern konuşan bir servis bul. Atlamak YOK: atlanan test hiçbir
	// şeyi korumuyor — hash deterministik olduğu için yeterince aday
	// denenirse mutlaka bulunur.
	svc := ""
	for i := 0; i < 200 && svc == ""; i++ {
		if cand := fmt.Sprintf("svc-%d", i); serviceSpeaksModernSemconv(cand) {
			svc = cand
		}
	}
	if svc == "" {
		t.Fatal("200 adayda modern konuşan servis yok — hash dağılımı bozuk")
	}

	in := []*commonpb.KeyValue{
		kvStr("http.method", "GET"),
		kvStr("db.statement", "SELECT 1"),
		kvStr("channel_code", "030101"), // eşlemede yok → dokunulmamalı
	}
	out := applySemconvDialect(svc, in)

	byKey := map[string]string{}
	for _, kv := range out {
		byKey[kv.Key] = kv.Value.GetStringValue()
	}
	if byKey["http.request.method"] != "GET" {
		t.Errorf("http.method yeniden adlandırılmadı: %v", byKey)
	}
	if byKey["db.query.text"] != "SELECT 1" {
		t.Errorf("db.statement yeniden adlandırılmadı: %v", byKey)
	}
	if byKey["channel_code"] != "030101" {
		t.Errorf("eşlemede olmayan anahtar değişmiş: %v", byKey)
	}
	// Çağıranın dilimi MUTASYONA UĞRAMAMALI — kv(...) haritaları çağrı
	// yerlerinde paylaşılabiliyor.
	if in[0].Key != "http.method" {
		t.Fatalf("girdi mutasyona uğradı: %s", in[0].Key)
	}
}
