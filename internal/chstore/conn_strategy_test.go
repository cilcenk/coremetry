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
	// v0.9.496 — okuma havuzu eklendi, sayı 1'den 2'ye BİLİNÇLİ olarak
	// çıktı (ingest + read). 3'e çıkarsa yeni bir havuz gelmiş demektir
	// ve o havuzun hangi trafiği taşıdığı bu testte gerekçelendirilmeli;
	// 1'e düşerse dilimlerden biri geri alınmış demektir.
	if !strings.Contains(src, "readOpts.ConnOpenStrategy = clickhouse.ConnOpenRoundRobin") {
		t.Error("okuma havuzu RoundRobin değil — analitik SELECT koordinasyonu yine tek node'da birikir (v0.9.496 gerilemesi)")
	}
	if strings.Count(src, "ConnOpenStrategy") != 2 {
		t.Error("store.go'da tam 2 ConnOpenStrategy ataması beklenir (ingest + read havuzları); ana bağlantı stratejisiz kalmalı")
	}
}

// telemetryReadConn'un çağrı yüzeyi: yalnız Distributed sarmalayıcı /
// MV okuyan dosyalar. Dilim dilim büyüyecek liste — yeni bir dosya
// eklenirken o dosyanın HİÇBİR state tablosu okumadığı doğrulanmalı,
// yoksa RoundRobin her çağrıda başka node'un kopyasına düşer ve
// v0.9.486'nın /users tutarsızlığı geri gelir.
func TestTelemetryReadConnCallSurface(t *testing.T) {
	allowed := map[string]bool{
		"store.go":   true, // tanım + fallback
		"summary.go": true, // service_summary_5m / operation_summary_5m / spans (v0.9.496 dilim 1)
		// v0.9.497 dilim 2 — üçü de SAF telemetri (aşağıdaki testle pinli):
		"repo.go":         true, // spans / logs / metric_points / trace_*_5m / topology_edges_5m
		"topology.go":     true, // topology_*_5m / service_summary_5m / spans / root_traces
		"dependencies.go": true, // db_*_summary_5m / messaging_*_summary_5m / metric_points / spans
		// BİLİNÇLİ DIŞARIDA: problem.go (alert_rules + problems) ve
		// incident.go (incidents/incident_events/incident_problems) STATE
		// tablosu okuyor — ReplacingMergeTree + FINAL, her kurulumda
		// replicate DEĞİL. Bu dosyalar dosya bazında taşınamaz; taşınacaksa
		// fonksiyon fonksiyon ayrıştırılmalı.
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
		if strings.Contains(string(b), "telemetryReadConn") {
			t.Errorf("%s: telemetryReadConn çağrısı — RoundRobin okuma havuzu yalnız telemetri SELECT'leri için; state tabloları in-order ana bağlantıda kalmalı (v0.9.486)", f)
		}
	}
}

// v0.9.504 — TelemetryReadConn() paket DIŞI erişimci. Aynı sözleşme
// chstore dışında da geçerli olmalı, ama dosya-yüzeyi testi yalnız bu
// dizini tarıyordu. Bu test internal/ altındaki TÜM paketleri tarar:
// havuzu kullanan her paket bilinçli beyaz listede olmalı VE hiçbir state
// tablosu okumamalı.
func TestTelemetryReadConnPackageSurface(t *testing.T) {
	allowedPkgs := map[string]bool{
		"anomaly":   true, // spans + service_summary_5m — saf telemetri (v0.9.504)
		"evaluator": true, // spans + service_summary_5m + operation_summary_5m
	}
	stateTables := []string{
		"FROM users", "FROM teams", "FROM system_settings", "FROM alert_rules",
		"FROM saved_views", "FROM dashboards", "FROM problems", "FROM audit_events",
		"FROM incidents", "FROM incident_events", "FROM incident_problems",
	}
	files, err := filepath.Glob("../*/*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		pkg := filepath.Base(filepath.Dir(f))
		if pkg == "chstore" {
			continue // kendi dizini; dosya-yüzeyi testi onu ayrıca kapsıyor
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if !strings.Contains(src, "TelemetryReadConn") {
			continue
		}
		if !allowedPkgs[pkg] {
			t.Errorf("%s: paket %q RoundRobin okuma havuzunu kullanıyor ama beyaz listede değil — o paketin HİÇBİR state tablosu okumadığı doğrulanmadan eklenmemeli (v0.9.486)", f, pkg)
			continue
		}
		for _, tbl := range stateTables {
			if strings.Contains(src, tbl) {
				t.Errorf("%s: %q — bu paket RoundRobin okuma havuzunu kullanıyor, state tablosu okuyamaz (v0.9.486 /users tutarsızlığı)", f, tbl)
			}
		}
	}
}

// Beyaz listedeki dosyalar GERÇEKTEN state tablosu okumamalı. Yukarıdaki
// test yeni dosyaların havuza sızmasını engelliyor; bu test ise izin
// verilmiş dosyaya sonradan bir state okuması EKLENMESİNİ yakalıyor —
// asıl sinsi olan bu.
func TestTelemetryReadFilesTouchNoStateTables(t *testing.T) {
	stateTables := []string{
		"FROM users", "FROM teams", "FROM system_settings", "FROM alert_rules",
		"FROM saved_views", "FROM dashboards", "FROM problems", "FROM audit_events",
		"FROM incidents", "FROM incident_events", "FROM incident_problems",
		"FROM anomaly_events", "FROM service_metadata", "FROM ai_calls",
	}
	for _, f := range []string{"summary.go", "repo.go", "topology.go", "dependencies.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, tbl := range stateTables {
			if strings.Contains(src, tbl) {
				t.Errorf("%s: %q — bu dosya RoundRobin okuma havuzunu kullanıyor, state tablosu okuyamaz (v0.9.486 /users tutarsızlığı)", f, tbl)
			}
		}
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
