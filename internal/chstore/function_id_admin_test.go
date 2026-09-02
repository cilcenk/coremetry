package chstore

// function_id_admin_test.go — v0.10.252 0013 sihirbazı sözleşmesi: gömülü
// dosyadan TAM 3 uygulama ifadesi (kolon spans_local + sarmalayıcı + set
// index; MATERIALIZE yok — o ayrı eylem), küme adı yerine konur, rollback
// INDEX → spans → spans_local sırası (Code 47), ifade metni promoted_attr.go
// ile aynı iki yazımı okur.

import (
	"strings"
	"testing"
)

func TestFunctionIDStatements(t *testing.T) {
	stmts, err := functionIDStatements("prod_cluster")
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 3 {
		t.Fatalf("3 ifade bekleniyordu, %d:\n%s", len(stmts), strings.Join(stmts, "\n---\n"))
	}
	for i, s := range stmts {
		if !strings.Contains(s, "ON CLUSTER prod_cluster") || strings.Contains(s, "uptrace_all") {
			t.Errorf("ifade %d küme adı değişmemiş: %s", i, s)
		}
		if strings.Contains(s, "MATERIALIZE COLUMN") {
			t.Errorf("ifade %d MATERIALIZE taşıyor — ayrı eylem olmalı", i)
		}
	}
	if !strings.Contains(stmts[0], "ALTER TABLE spans_local") || !strings.Contains(stmts[0], "ADD COLUMN IF NOT EXISTS attr_function_id String") {
		t.Errorf("ADIM 1: %s", stmts[0])
	}
	if !strings.Contains(stmts[1], "ALTER TABLE spans ON CLUSTER") || !strings.Contains(stmts[1], "ADD COLUMN IF NOT EXISTS attr_function_id") {
		t.Errorf("ADIM 2: %s", stmts[1])
	}
	if !strings.Contains(stmts[2], "ADD INDEX IF NOT EXISTS idx_attr_function_id attr_function_id TYPE set(0)") {
		t.Errorf("ADIM 4: %s", stmts[2])
	}
	for _, key := range []string{"'function_id'", "'FUNCTION_ID'"} {
		if !strings.Contains(stmts[0], key) {
			t.Errorf("kolon ifadesi %s yazımını okumuyor (v0.9.626 sınıfı)", key)
		}
	}
}

func TestFunctionIDRollbackAndMaterialize(t *testing.T) {
	stmts, err := functionIDRollbackStatements("c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 3 {
		t.Fatalf("rollback 3 ifade, %d", len(stmts))
	}
	if !strings.Contains(stmts[0], "DROP INDEX") || !strings.Contains(stmts[1], "ALTER TABLE spans ON CLUSTER c1 DROP COLUMN") || !strings.Contains(stmts[2], "ALTER TABLE spans_local ON CLUSTER c1 DROP COLUMN") {
		t.Errorf("rollback sırası INDEX → spans → spans_local olmalı:\n%s", strings.Join(stmts, "\n"))
	}
	if got := functionIDMaterializeStatement("c1"); got != "ALTER TABLE spans_local ON CLUSTER c1 MATERIALIZE COLUMN attr_function_id" {
		t.Errorf("materialize: %s", got)
	}
	objs := FunctionIDColumnObjects()
	if len(objs) != 3 || objs[0].Kind != "column" || objs[1].Kind != "distributed" || objs[2].Kind != "index" {
		t.Errorf("nesneler: %+v", objs)
	}
}
