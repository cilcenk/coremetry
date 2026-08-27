package chstore

// trace_entry_service_test.go — v0.10.97, operatör-raporlu "iframe
// trace'leri": mobil web/iframe telemetrisi service.name'siz span'i
// trace'in MUTLAK köküne koyunca /traces listesi servis filtresine
// rağmen "unknown" basıyordu. Görüntü artık giriş-span ilkesiyle EN
// ERKEN server/consumer span'in servisine düşüyor.

import (
	"os"
	"strings"
	"testing"
)

// İki dal da sürülür — probe bir AYARDIR ve "bugün doğru" ayara asılı
// doğruluk sayılmaz ([[feedback-correctness-held-by-a-setting]]).
func TestTraceDisplaySvcExprBothBranches(t *testing.T) {
	off := &Store{hasTraceEntrySvcCol: false}
	if got := off.traceDisplaySvcExpr(); got != "argMaxIfMerge(root_service_state)" {
		t.Fatalf("probe kapalıyken eski zincir bit-bit dönmeli: %q", got)
	}
	on := &Store{hasTraceEntrySvcCol: true}
	expr := on.traceDisplaySvcExpr()
	for _, must := range []string{
		"argMinIfMerge(entry_service_state)",
		"= 'unknown'",
		"argMaxIfMerge(root_service_state)",
	} {
		if !strings.Contains(expr, must) {
			t.Errorf("açık dalda eksik: %q (ifade: %q)", must, expr)
		}
	}
	// Giriş state'i BOŞKEN (eski bucket) kök aynen dönmeli — if koşulu
	// entry != '' şartını taşımalı.
	if !strings.Contains(expr, "argMinIfMerge(entry_service_state) != ''") {
		t.Error("boş giriş-state'te köke düşme şartı yok — eski bucket'lar boş servis basar")
	}
}

func TestEntryServiceContractSites(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	// DDL: giriş-span yüklemi server+consumer VE unknown-dışı; argMIN
	// (İLK giriş — argMax son span'i seçerdi).
	ddl := read("store.go")
	for _, must := range []string{
		"argMinIfState(service_name, time,",
		"(kind = 'server' OR kind = 'consumer')",
		"AND service_name != '' AND service_name != 'unknown') AS entry_service_state",
	} {
		if !strings.Contains(ddl, must) {
			t.Errorf("MV DDL'inde eksik: %q", must)
		}
	}
	// Dağıtık ikinci yarı: cluster upgrade'i BARE sarmalayıcıyı da
	// düşürmeli — düşürmezse yeni kolonu okuyan her sorgu UNKNOWN_COLUMN
	// ([[feedback-distributed-column-safety]]; bu sınıf prod'u iki kez kırdı).
	if !strings.Contains(ddl, `"DROP TABLE IF EXISTS trace_summary_5m"+s.onCluster()`) {
		t.Error("entry_service migration'ı bayat Distributed sarmalayıcıyı düşürmüyor")
	}
	// Görüntü zinciri TEK yazımdan beş sitede: liste + facet key/extra +
	// aggregate iç SELECT (repo.go 4) ve stub (1). Sayı iddiaya çevrildi
	// ([[feedback-fixes-have-second-halves]]); tracemetric BİLİNÇLİ dışarıda
	// (metrik gruplaması ayrı karar) — oraya sızarsa bu sayım kırılır.
	n := strings.Count(read("repo.go"), "s.traceDisplaySvcExpr()") +
		strings.Count(read("trace_stub.go"), "s.traceDisplaySvcExpr()")
	if n != 5 {
		t.Errorf("traceDisplaySvcExpr %d sitede, 5 olmalı (liste, facet×2, aggregate, stub)", n)
	}
	if strings.Contains(read("tracemetric.go"), "traceDisplaySvcExpr") {
		t.Error("görüntü zinciri tracemetric'e sızmış — metrik gruplaması bilinçli olarak kök-servis")
	}
}
