package chstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// v0.9.486 (operator-reported, prod: "/users her refresh'te farklı sayıda
// kullanıcı — 2, sonra 205") — v0.9.481 RoundRobin'i ANA bağlantıya koydu;
// admin/state tabloları her kurulumda replicate olmadığından her refresh
// farklı node'un kopyasını okudu. Sözleşme bu testle pinli:
//
//   1. Ana bağlantı stratejisiz açılır (driver varsayılanı ConnOpenInOrder)
//      → state okuma/yazmaları hep aynı node'da, v481 öncesi tutarlılık.
//   2. RoundRobin YALNIZ ingest havuzundadır → v481'in gerçek amacı
//      (insert koordinasyonunun 4 node'a dağılması) korunur.
//   3. ingestWriteConn() yalnız yüksek hacimli telemetri INSERT dosyalarında
//      çağrılır — bir state tablosu yazımı bu havuza kayarsa test patlar.
func TestConnStrategySplit(t *testing.T) {
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "ingestOpts.ConnOpenStrategy = clickhouse.ConnOpenRoundRobin") {
		t.Error("ingest havuzu RoundRobin değil — insert koordinasyonu yine tek node'da birikir (v0.9.481 gerilemesi)")
	}
	if strings.Contains(src, "ConnOpenStrategy: clickhouse.ConnOpenRoundRobin") {
		t.Error("ana bağlantı options literal'inde RoundRobin var — state tabloları node-lokal olabilir; /users tutarsızlığı (v0.9.486) geri gelir")
	}
	if strings.Count(src, "ConnOpenStrategy") != 1 {
		t.Error("store.go'da tam 1 ConnOpenStrategy ataması beklenir (yalnız ingest havuzu)")
	}
}

// ingestWriteConn'un çağrı yüzeyi: yalnız Distributed-sarmalı yüksek hacim
// tablolarına yazan dosyalar. Yeni bir dosya bu havuzu kullanacaksa buraya
// bilinçli eklenir — state tabloları (users, system_settings, problems…)
// ASLA (in-order ana bağlantı tutarlılığı bunların tek garantisi).
func TestIngestConnCallSurface(t *testing.T) {
	allowed := map[string]bool{
		"store.go":         true, // tanım + fallback
		"repo.go":          true, // spans / logs / metric_points
		"profile.go":       true, // profiles
		"exemplar_otlp.go": true, // exemplars
		"span_links.go":    true, // span_links
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || allowed[f] {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "ingestWriteConn") {
			t.Errorf("%s: ingestWriteConn çağrısı — RoundRobin havuzu yalnız yüksek hacimli telemetri INSERT'leri için; state tabloları in-order ana bağlantıda kalmalı (v0.9.486)", f)
		}
	}
}
