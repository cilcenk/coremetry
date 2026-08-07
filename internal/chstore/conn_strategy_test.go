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
//  1. Ana bağlantı stratejisiz açılır (driver varsayılanı ConnOpenInOrder)
//     → state okuma/yazmaları hep aynı node'da, v481 öncesi tutarlılık.
//  2. RoundRobin YALNIZ ingest havuzundadır → v481'in gerçek amacı
//     (insert koordinasyonunun 4 node'a dağılması) korunur.
//  3. ingestWriteConn() yalnız yüksek hacimli telemetri INSERT dosyalarında
//     çağrılır — bir state tablosu yazımı bu havuza kayarsa test patlar.
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

	// v0.9.505 — RoundRobin TEK BAŞINA yükü dağıtmaz: bağlantı açılışını
	// dağıtır, sorguyu değil. Bağlantı ömrü kısaltılmazsa açılışta düştüğü
	// host'u sürücü varsayılanı olan 1 SAAT boyunca taşır ve Go havuzunun
	// LIFO yeniden kullanımı trafiği birkaç sıcak bağlantıya yığar.
	// Ölçüldü: v0.9.504 sonrası lokalde giriş sorgularının %83'ü hâlâ tek
	// node'daydı. İki havuzun da kısa ömrü bu yüzden sözleşmenin parçası.
	for _, pool := range []string{"ingestOpts", "readOpts"} {
		if !strings.Contains(src, pool+".ConnMaxLifetime = roundRobinConnLifetime") {
			t.Errorf("%s.ConnMaxLifetime kısaltılmamış — bağlantılar açıldıkları host'ta 1 saat çakılı kalır, RoundRobin kağıt üzerinde kalır (v0.9.505)", pool)
		}
	}
	// Ana bağlantı bilinçli olarak uzun ömürlü: zaten in-order, hep ilk
	// host'a gidiyor, çevrimden kazanacağı bir şey yok.
	if !strings.Contains(src, "ConnMaxLifetime: time.Hour") {
		t.Error("ana bağlantının 1 saatlik ömrü kaldırılmış — in-order havuzda çevrim gereksiz bağlantı çöpü üretir")
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
		"repo.go":              true, // spans / logs / metric_points / trace_*_5m / topology_edges_5m
		"topology.go":          true, // topology_*_5m / service_summary_5m / spans / root_traces
		"dependencies.go":      true, // db_*_summary_5m / messaging_*_summary_5m / metric_points / spans
		"problem_telemetry.go": true, // spans — problem.go'dan ayrılan telemetri yarısı (v0.9.507)
		// v0.9.712 — SAF telemetri: rollup_metrics_* (metric_points'in
		// AggregatingMergeTree türevi) + coverage min(ts) probu. State yok.
		"metric_rollup_read.go": true,
		// v0.9.751 — histogram rollup okuyucusu: rollup_metrics_* telemetri
		// SELECT'i, RoundRobin havuzu doğru adres.
		"metric_rollup_hist_read.go": true,
		// v0.9.777 — 0008 route tier'ı. Tek FROM'u rollup_metrics_route_*
		// (AggregatingMergeTree telemetri rollup'ı, state DEĞİL); coverage
		// probu da aynı tablolara min(ts) atıyor.
		"metric_rollup_route_read.go": true,
		// v0.9.580 — SAF telemetri: tek FROM'u spans. State tablosu
		// okumuyor (aşağıdaki FROM testi de pinliyor).
		"correlation_ids.go": true, // spans'ten örnek request_id/correlation_id'ler
		// v0.9.508 dilim 5 — yedisi de saf telemetri, FROM listeleri tek tek doğrulandı:
		"deploys.go":          true, // service_version_5m / spans
		"oracle.go":           true, // metric_points
		"profile.go":          true, // profiles (yazma yarısı ingest havuzunda)
		"spanmetric.go":       true, // service_summary_5m / operation_summary_5m / spans
		"dbstmt_detail.go":    true, // db_statement_summary_5m / spans
		"db_capacity.go":      true, // metric_points
		"endpoints_detail.go": true, // spans
		"business_dims.go":    true, // spans — kanal/fonksiyon kodu kırılımı (v0.9.511)
		"trace_count.go":      true, // trace_summary_5m / trace_service_index_5m — tavanlı sayım (v0.9.638)
		// TAŞINMAZ ÜÇÜNCÜ SINIF: sysstats.go + cluster.go system.* okuyor.
		// Bunlar NODE-LOKAL tablolar; RoundRobin'e verilirse disk/utilizasyon
		// panelleri her çağrıda BAŞKA node'u raporlar (SQL konsolunun in-order
		// tutulma gerekçesiyle aynı).
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
	for _, f := range []string{
		"summary.go", "repo.go", "topology.go", "dependencies.go", "problem_telemetry.go",
		"deploys.go", "oracle.go", "profile.go", "spanmetric.go", "dbstmt_detail.go",
		"db_capacity.go", "endpoints_detail.go", "business_dims.go",
	} {
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
