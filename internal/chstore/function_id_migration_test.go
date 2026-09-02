package chstore

// function_id_migration_test.go — v0.10.233 (traces audit D2): 0013
// migrasyonu ile promoted_attr.go BİREBİR aynı ifadeyi taşır (0011
// sözleşmesi). Ayrışırlarsa app-managed ve operatör-managed kurulumlar
// aynı anahtarı farklı okur — ve boot probe'u prod kolonunu "bozuk"
// sayıp DROP+ADD onarımı tetiklemeye kalkar (dış Distributed'da atlanır,
// ama tek-node prod'da kolonu yeniden yazar).

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/migrations"
)

func TestFunctionIDMigrationMatchesStore(t *testing.T) {
	raw, err := migrations.FS.ReadFile("0013_function_id.sql")
	if err != nil {
		t.Fatalf("0013 gömülü değil: %v", err)
	}
	sql := string(raw)
	var a *promotedAttr
	for i := range promotedAttrs {
		if promotedAttrs[i].col == "attr_function_id" {
			a = &promotedAttrs[i]
		}
	}
	if a == nil {
		t.Fatal("attr_function_id promotedAttrs'ta yok")
	}
	colDDL := "ADD COLUMN IF NOT EXISTS attr_function_id " + a.colType() + "\n  MATERIALIZED " + promotedAttrExprFor(*a) + ";"
	if n := strings.Count(sql, colDDL); n != 2 {
		t.Fatalf("kolon DDL'i spans_local + spans için birebir aynı ifadeyle 2 kez geçmeli, %d kez geçiyor:\n%s", n, colDDL)
	}
	local := strings.Index(sql, "ALTER TABLE spans_local ON CLUSTER uptrace_all\n  ADD COLUMN IF NOT EXISTS attr_function_id")
	wrapper := strings.Index(sql, "ALTER TABLE spans ON CLUSTER uptrace_all\n  ADD COLUMN IF NOT EXISTS attr_function_id")
	idx := strings.Index(sql, "ADD INDEX IF NOT EXISTS idx_attr_function_id attr_function_id TYPE set(0) GRANULARITY 4")
	if local == -1 || wrapper == -1 || idx == -1 || !(local < wrapper && wrapper < idx) {
		t.Fatalf("sıra spans_local → spans → index olmalı (local=%d wrapper=%d idx=%d)", local, wrapper, idx)
	}
	if !strings.Contains(sql, "MATERIALIZE COLUMN attr_function_id") || !strings.Contains(sql, "-- ALTER TABLE spans_local ON CLUSTER uptrace_all MATERIALIZE COLUMN") {
		t.Fatal("MATERIALIZE COLUMN opsiyonel ve YORUMLU kalmalı (operatör kararı, mesai dışı)")
	}
	rb, err := migrations.FS.ReadFile("0013_function_id_rollback.sql")
	if err != nil {
		t.Fatalf("rollback gömülü değil: %v", err)
	}
	r := string(rb)
	di, dc := strings.Index(r, "DROP INDEX IF EXISTS idx_attr_function_id"), strings.Index(r, "DROP COLUMN IF EXISTS attr_function_id")
	if di == -1 || dc == -1 || di > dc {
		t.Fatalf("rollback: DROP INDEX, DROP COLUMN'dan ÖNCE (Code 47): idx=%d col=%d", di, dc)
	}
}
