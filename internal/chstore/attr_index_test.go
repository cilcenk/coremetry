package chstore

import (
	"strings"
	"testing"
)

// attr_index_test.go — v0.10.299: DDL şekli, idempotency, ifade/ayırıcı pinleri.

func TestAttrIndexDDLShapeAndOrder(t *testing.T) {
	c := attrIndexCols[0]
	stmts := attrIndexDDL(c, false, false, false)
	if len(stmts) != 3 {
		t.Fatalf("3 ifade bekleniyordu: %v", stmts)
	}
	if !strings.HasPrefix(stmts[0], "ALTER TABLE spans ADD COLUMN IF NOT EXISTS attr_kvh Array(UInt64) MATERIALIZED arrayMap((k, v) -> cityHash64(concat(k, '\\x1F', v)), attr_keys, attr_values) CODEC(ZSTD(3))") {
		t.Errorf("kolon DDL'i: %s", stmts[0])
	}
	if stmts[1] != "ALTER TABLE spans ADD INDEX IF NOT EXISTS idx_attr_kvh attr_kvh TYPE bloom_filter(0.01) GRANULARITY 4" {
		t.Errorf("kv indeksi: %s", stmts[1])
	}
	if stmts[2] != "ALTER TABLE spans ADD INDEX IF NOT EXISTS idx_attr_keys attr_keys TYPE bloom_filter(0.01) GRANULARITY 4" {
		t.Errorf("anahtar indeksi: %s", stmts[2])
	}
	// Idempotent: hepsi varsa hiç ifade yok; kısmen varsa yalnız eksik.
	if n := len(attrIndexDDL(c, true, true, true)); n != 0 {
		t.Errorf("her şey varken %d ifade", n)
	}
	only := attrIndexDDL(c, true, false, true)
	if len(only) != 1 || !strings.Contains(only[0], "idx_attr_kvh") {
		t.Errorf("yalnız eksik indeks eklenmeli: %v", only)
	}
	// Onarım (DROP) ASLA — tek yazımlı ifade.
	for _, s := range stmts {
		if strings.Contains(s, "DROP") {
			t.Errorf("DROP içermemeli: %s", s)
		}
	}
	// Resource ikizi aynı şekil, kendi dizileri.
	r := attrIndexDDL(attrIndexCols[1], false, false, false)
	if !strings.Contains(r[0], "res_kvh Array(UInt64) MATERIALIZED arrayMap((k, v) -> cityHash64(concat(k, '\\x1F', v)), res_keys, res_values)") {
		t.Errorf("res_kvh DDL'i: %s", r[0])
	}
}

func TestAttrKVSeparatorAndHashSQL(t *testing.T) {
	if AttrKVSep != "\x1f" || len(AttrKVSep) != 1 {
		t.Errorf("ayırıcı 0x1F olmalı: %q", AttrKVSep)
	}
	// SQL literal'i CH'nin çözeceği kaçış: ters bölü + x1F (ham baytı DEĞİL —
	// kaynağa kontrol karakteri sızmasın: feedback-binary-poisoned-source).
	if attrKVSepSQL != `'\x1F'` || strings.ContainsRune(attrKVSepSQL, 0x1f) {
		t.Errorf("SQL ayırıcısı kaçışlı olmalı: %q", attrKVSepSQL)
	}
	if strings.Count(AttrKVHashSQL, "?") != 2 || !strings.HasPrefix(AttrKVHashSQL, "cityHash64(concat(?, '\\x1F', ?))") {
		t.Errorf("hash SQL'i iki bağlama taşımalı: %s", AttrKVHashSQL)
	}
	// Kaynak dosyada ham 0x1F baytı YOK.
	if strings.ContainsRune(attrIndexExpr(attrIndexCols[0]), 0x1f) {
		t.Error("ifade ham kontrol karakteri taşıyor")
	}
}

func TestAttrIndexRegistry(t *testing.T) {
	registerAttrIndex(false)
	if AttrIndexAvailable() {
		t.Error("false kaydı")
	}
	registerAttrIndex(true)
	if !AttrIndexAvailable() {
		t.Error("true kaydı")
	}
	registerAttrIndex(false)
}
