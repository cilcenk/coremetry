// v0.9.1308 — state tabloları tek replikasyon grubunda.
//
// ORİJİNAL BELİRTİ: küme kipinde her state tablosu `<prefix>/{shard}/<ad>`
// ZK yoluna kuruluyordu ve Distributed sarmalayıcıları olmadığı için her
// shard AYRI bir replikasyon grubu oluyordu. Uygulama hangi host'a
// bağlanırsa onun dilimini görüyordu:
//
//	LOKAL  problems 4631/191 · anomaly_events 222/60 · alert_rules 8/0
//	PROD   problems 633.236 (bp01/bp02) vs 4.169 (bp03/bp04)
//
// Bu dosya ÜÇ şeyi çiviliyor:
//  1. hangi tablonun hangi yolu aldığı (state vs telemetri),
//  2. telemetri yolunun BİR KARAKTER bile değişmediği,
//  3. split-brain muhafızının karar zinciri.
package chstore

import (
	"fmt"
	"strings"
	"testing"
)

// measuredStateTables — canlı dağıtık kümeden (chc-0/chc-1, 2026-08-23)
// okunan çıplak Replicated tablo listesi:
//
//	SELECT name FROM system.tables
//	WHERE database='coremetry' AND engine LIKE 'Replicated%'
//	  AND name NOT LIKE '.inner%' AND name NOT LIKE '%\_local'
//
// 37 tablo. Türetimin ölçülen gerçekle aynı kümeyi vermesi ŞART: bu
// liste bir "ikinci kayıt" değil, türetimin PİNİ.
var measuredStateTables = []string{
	"ai_calls", "ai_feedback", "alert_rules", "anomaly_events", "anomaly_silences",
	"api_tokens", "audit_log", "dashboards", "events", "exception_groups",
	"incident_events", "incident_problems", "incidents", "ldap_groups", "log_templates",
	"maintenance_windows", "monitor_results", "monitors", "notification_channels",
	"notification_log", "problems", "rag_chunks", "rca_verdicts", "root_cause_hypotheses",
	"runbook_executions", "runbooks", "saved_views", "service_contracts", "service_metadata",
	"slos", "status_page_components", "status_page_config", "status_page_published",
	"status_page_subscribers", "system_settings", "trace_snapshots", "users",
}

// TestStateTableDerivation — türetim ölçülen kümeyi birebir vermeli.
//
// MUTASYON: `defaultShardPolicy`'ye "problems" eklenirse bu test kızarır
// (doğrulandı) — yani state listesi shard kayıtlarından TÜREDİĞİ için
// iki listenin ıraksaması imkânsız.
func TestStateTableDerivation(t *testing.T) {
	for _, n := range measuredStateTables {
		if !stateTableDDL(n, "table") {
			t.Errorf("%s state sayılmadı — shard kayıtlarından birine sızmış olmalı", n)
		}
	}
	// Ters yön: shard'lı telemetri ASLA state olamaz.
	sharded := map[string]bool{}
	for n := range highVolumeTables {
		sharded[n] = true
	}
	for n := range defaultShardPolicy {
		sharded[n] = true
	}
	for n := range tablesWithoutTraceID {
		sharded[n] = true
	}
	for n := range sharded {
		if stateTableDDL(n, "table") {
			t.Errorf("%s TELEMETRİ ama state sayıldı — birleşik gruba shard'lı veri yazardı", n)
		}
	}
	// Ölçülen küme ile shard kayıtları KESİŞMEMELİ.
	for _, n := range measuredStateTables {
		if sharded[n] {
			t.Errorf("%s hem ölçülen state listesinde hem shard kayıtlarında", n)
		}
	}
}

// TestStateTableDDLKindGuard — MV ve ALTER asla state değil.
//
// Bir MV shard-yerel `_local` kaynaktan beslenir; hedefini birleşik
// gruba almak iki shard'ın MV'sini aynı tabloya toplardı. 0009'un
// kapsamı DEĞİL.
func TestStateTableDDLKindGuard(t *testing.T) {
	for _, kind := range []string{"mv", "altertable", ""} {
		if stateTableDDL("problems", kind) {
			t.Errorf("kind=%q için state sayıldı — yalnız CREATE TABLE aday olmalı", kind)
		}
	}
	if stateTableDDL("", "table") {
		t.Error("boş ad state sayıldı")
	}
}

// TestReplicatedArgsTelemetryUnchanged — v0.9.1308 ÖNCESİ biçim.
//
// Telemetri tablolarının ZK yolu bu değişiklikte bir karakter bile
// değişmemeli. Karşılaştırma, adaptDDL'de v0.9.1308'e kadar duran
// format dizesinin BİREBİR kopyasına karşı yapılır.
func TestReplicatedArgsTelemetryUnchanged(t *testing.T) {
	const legacyFmt = "'%s/{shard}/%s', '{replica}'" // v0.9.1308 öncesi adaptDDL:1101
	for _, prefix := range []string{"/clickhouse/tables", "/clickhouse/tables/coremetry"} {
		for name := range highVolumeTables {
			want := fmt.Sprintf(legacyFmt, prefix, name)
			if got := replicatedArgs(prefix, name, false); got != want {
				t.Errorf("%s: %q, eski biçim %q", name, got, want)
			}
		}
	}
}

// TestReplicatedArgsUnified — birleşik yolun tam metni.
//
// MUTASYON: stateReplicaName "{shard}-{replica}" → "{replica}" yapılırsa
// bu test kızarır (doğrulandı). `{replica}` shard başına tekrar ediyorsa
// iki host aynı replika adını iddia eder → REPLICA_ALREADY_EXISTS.
func TestReplicatedArgsUnified(t *testing.T) {
	got := replicatedArgs("/clickhouse/tables", "problems", true)
	want := "'/clickhouse/tables/state/problems', '{shard}-{replica}'"
	if got != want {
		t.Errorf("= %q\nbeklenen %q", got, want)
	}
	if replicatedArgs("/ch/x", "users", true) != "'/ch/x/state/users', '{shard}-{replica}'" {
		t.Errorf("operatör öneki taşınmadı: %q", replicatedArgs("/ch/x", "users", true))
	}
	// Yol {shard} makrosunu İÇERMEMELİ — tek grup olmasının şartı.
	for _, name := range measuredStateTables {
		p := unifiedStatePath("/clickhouse/tables", name)
		if strings.Contains(p, "{shard}") {
			t.Errorf("%s birleşik yolunda {shard} var: %q", name, p)
		}
	}
}

// TestUseUnifiedStatePath — SPLIT-BRAIN muhafızı.
//
// Sessiz bölünmenin imkânsız olduğunu çiviler: kod birleşik yolu yalnız
// ÖNERİR, kümede gözlenen yol her zaman kazanır.
func TestUseUnifiedStatePath(t *testing.T) {
	const pfx = "/clickhouse/tables"
	legacy := func(n string) string { return pfx + "/{shard}/" + n }
	unified := func(n string) string { return pfx + "/state/" + n }

	tests := []struct {
		name string
		obs  stateObservation
		tbl  string
		want bool
	}{
		{
			name: "probe koşmadı → ESKİ yol (mevcut kuruluma dokunma)",
			obs:  stateObservation{},
			tbl:  "problems",
			want: false,
		},
		{
			name: "taze kurulum: hiçbir state tablosu yok → BİRLEŞİK",
			obs:  stateObservation{ok: true, paths: map[string]string{}},
			tbl:  "problems",
			want: true,
		},
		{
			name: "göç ÖNCESİ: tablo eski yolda → ESKİ (komşularına katıl)",
			obs:  stateObservation{ok: true, paths: map[string]string{"problems": legacy("problems")}},
			tbl:  "problems",
			want: false,
		},
		{
			name: "göç SONRASI: tablo birleşik yolda → BİRLEŞİK",
			obs:  stateObservation{ok: true, paths: map[string]string{"problems": unified("problems")}},
			tbl:  "problems",
			want: true,
		},
		{
			// ASIL SENARYO. Yeni node kümeye giriyor, `users` onda yok
			// ama komşularında eski yolda duruyor. Kuşak kararı olmasa
			// yeni node birleşik yola kurar → SESSİZ BÖLÜNME.
			name: "YENİ NODE, göç öncesi küme: tablo yok ama komşular eski yolda → ESKİ",
			obs: stateObservation{ok: true, paths: map[string]string{
				"problems":    legacy("problems"),
				"alert_rules": legacy("alert_rules"),
			}},
			tbl:  "users",
			want: false,
		},
		{
			name: "YENİ NODE, göç sonrası küme: tablo yok, komşular birleşik → BİRLEŞİK",
			obs: stateObservation{ok: true, paths: map[string]string{
				"problems":    unified("problems"),
				"alert_rules": unified("alert_rules"),
			}},
			tbl:  "users",
			want: true,
		},
		{
			// Göç YARIM kalmış: bazıları taşındı, bazıları taşınmadı.
			// Taşınmamış bir tablo hâlâ eski yolda görülür → ona uy.
			name: "yarım göç: taşınmış tablo BİRLEŞİK",
			obs: stateObservation{ok: true, paths: map[string]string{
				"problems": unified("problems"),
				"users":    legacy("users"),
			}},
			tbl:  "problems",
			want: true,
		},
		{
			name: "yarım göç: taşınmamış tablo ESKİ",
			obs: stateObservation{ok: true, paths: map[string]string{
				"problems": unified("problems"),
				"users":    legacy("users"),
			}},
			tbl:  "users",
			want: false,
		},
		{
			// Yarım göçte HİÇ görülmemiş bir tablo: kuşak "göç öncesi"
			// sayılır, çünkü en az bir tablo hâlâ eski yolda.
			name: "yarım göç: görülmemiş tablo ESKİ tarafa düşer",
			obs: stateObservation{ok: true, paths: map[string]string{
				"problems": unified("problems"),
				"users":    legacy("users"),
			}},
			tbl:  "slos",
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := useUnifiedStatePath(tc.obs, pfx, tc.tbl)
			if got != tc.want {
				t.Errorf("= %v (%s), beklenen %v", got, reason, tc.want)
			}
			if reason == "" {
				t.Error("gerekçe boş — log/teşhis değersizleşir")
			}
		})
	}
}

// TestStateProbeTable — probe'un gördüğü GERÇEK CH adları elenmeli.
//
// system.replicas `spans_local` döndürür; shard kayıtlarında o ad yok,
// yani eleme olmasaydı `spans_local` STATE sayılır ve kuşak kararını
// kalıcı olarak "göç öncesi"ne kilitlerdi.
func TestStateProbeTable(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"problems", true},
		{"users", true},
		{"spans_local", false},
		{"metric_points_local", false},
		{"service_callers_5m_local", false},
		{".inner_id.45f12d2e-36ac-4cb4-8bef-5025847af024", false},
		{"spans", false},            // shard'lı telemetri (Distributed sarmalayıcı)
		{"logs", false},             //
		{"problems_old", false},     // 0009 yedeği — eski yolda YAŞAR
		{"problems_unified", false}, // 0009 ara tablosu
	}
	for _, tc := range tests {
		if got := stateProbeTable(tc.name); got != tc.want {
			t.Errorf("stateProbeTable(%q) = %v, beklenen %v", tc.name, got, tc.want)
		}
	}
}
