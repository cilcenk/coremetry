package chstore

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/migrations"
)

// attr_index_migration_test.go — v0.10.299: 0014 ile attr_index.go BİREBİR
// aynı ifade (0011/0013 sözleşmesi); sıra local → wrapper → index; rollback
// INDEX → COLUMN.
func TestAttrIndexMigrationMatchesStore(t *testing.T) {
	raw, err := migrations.FS.ReadFile("0014_attr_kvh.sql")
	if err != nil {
		t.Fatalf("0014 gömülü değil: %v", err)
	}
	sql := string(raw)
	for _, c := range attrIndexCols {
		colDDL := "ADD COLUMN IF NOT EXISTS " + c.col + " Array(UInt64)\n  MATERIALIZED " + attrIndexExpr(c) + " CODEC(ZSTD(3));"
		if n := strings.Count(sql, colDDL); n != 2 {
			t.Fatalf("%s kolon DDL'i spans_local + spans için birebir 2 kez geçmeli, %d:\n%s", c.col, n, colDDL)
		}
		local := strings.Index(sql, "ALTER TABLE spans_local ON CLUSTER uptrace_all\n  ADD COLUMN IF NOT EXISTS "+c.col)
		wrapper := strings.Index(sql, "ALTER TABLE spans ON CLUSTER uptrace_all\n  ADD COLUMN IF NOT EXISTS "+c.col)
		idx := strings.Index(sql, "ADD INDEX IF NOT EXISTS "+c.idx+" "+c.col+" TYPE bloom_filter(0.01) GRANULARITY 4")
		kidx := strings.Index(sql, "ADD INDEX IF NOT EXISTS "+c.keysIdx+" "+c.keysCol+" TYPE bloom_filter(0.01) GRANULARITY 4")
		if local == -1 || wrapper == -1 || idx == -1 || kidx == -1 || !(local < wrapper && wrapper < idx) {
			t.Fatalf("%s: sıra spans_local → spans → index olmalı (local=%d wrapper=%d idx=%d keysIdx=%d)", c.col, local, wrapper, idx, kidx)
		}
	}
	if strings.ContainsRune(sql, 0x1f) {
		t.Error("migration ham 0x1F baytı taşıyor — '\\x1F' kaçışı olmalı")
	}
	if !strings.Contains(sql, "-- ALTER TABLE spans_local ON CLUSTER uptrace_all MATERIALIZE INDEX idx_attr_kvh") {
		t.Fatal("MATERIALIZE opsiyonel ve YORUMLU kalmalı (operatör kararı: kademeli)")
	}
	rb, err := migrations.FS.ReadFile("0014_attr_kvh_rollback.sql")
	if err != nil {
		t.Fatalf("rollback gömülü değil: %v", err)
	}
	r := string(rb)
	lastIdx := strings.LastIndex(r, "DROP INDEX")
	firstCol := strings.Index(r, "DROP COLUMN")
	if lastIdx == -1 || firstCol == -1 || lastIdx > firstCol {
		t.Fatalf("rollback: tüm DROP INDEX'ler DROP COLUMN'dan ÖNCE (Code 47): lastIdx=%d firstCol=%d", lastIdx, firstCol)
	}
}
