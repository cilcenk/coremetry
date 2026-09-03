package chstore

import (
	"strings"
	"testing"
)

// attr_index_admin_test.go — v0.10.306: gömülü 0014 → ifade listesi (sıra,
// küme adı, yorumlu ADIM 6 dışarıda), rollback sırası, MATERIALIZE listesi,
// nesne listesi, saf karar tablosu.

func TestAttrIndexStatementsFromEmbedded(t *testing.T) {
	stmts, err := attrIndexStatements("my_cluster")
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 8 {
		t.Fatalf("0014 = 2 kolon ×2 tablo + 4 indeks = 8 ifade; got %d:\n%s", len(stmts), strings.Join(stmts, "\n"))
	}
	for i, s := range stmts {
		if strings.Contains(s, "uptrace_all") || !strings.Contains(s, "ON CLUSTER my_cluster") {
			t.Errorf("ifade %d küme adı: %s", i, s)
		}
		if strings.Contains(s, "MATERIALIZE COLUMN") || strings.Contains(s, "MATERIALIZE INDEX") {
			t.Errorf("ADIM 6 yorumlu kalmalı, ifadeye girmemeli: %s", s)
		}
	}
	// Sıra: spans_local kolonlar → spans kolonlar → indeksler.
	if !strings.Contains(stmts[0], "spans_local") || !strings.Contains(stmts[0], "attr_kvh") ||
		!strings.Contains(stmts[2], "ALTER TABLE spans ON CLUSTER") ||
		!strings.Contains(stmts[4], "ADD INDEX") || !strings.Contains(stmts[7], "ADD INDEX") {
		t.Errorf("sıra: %v", stmts)
	}
	rb, err := attrIndexRollbackStatements("my_cluster")
	if err != nil {
		t.Fatal(err)
	}
	if len(rb) != 8 {
		t.Fatalf("rollback 4 DROP INDEX + 4 DROP COLUMN = 8; got %d", len(rb))
	}
	for i := 0; i < 4; i++ {
		if !strings.Contains(rb[i], "DROP INDEX") {
			t.Errorf("rollback %d önce DROP INDEX olmalı (Code 47): %s", i, rb[i])
		}
	}
	for i := 4; i < 8; i++ {
		if !strings.Contains(rb[i], "DROP COLUMN") {
			t.Errorf("rollback %d DROP COLUMN olmalı: %s", i, rb[i])
		}
	}
	mat := attrIndexMaterializeStatements("c1")
	if len(mat) != 4 || !strings.Contains(mat[0], "MATERIALIZE COLUMN attr_kvh") || !strings.Contains(mat[3], "MATERIALIZE INDEX idx_res_kvh") {
		t.Errorf("materialize: %v", mat)
	}
	if n := len(AttrIndexObjects()); n != 8 {
		t.Errorf("nesne sayısı %d; 2×(kolon+distributed+2 indeks)=8", n)
	}
}

func TestAttrIndexDecision(t *testing.T) {
	for _, tc := range []struct {
		boot, local bool
		clusters    int
		want        bool
		detail      string
	}{
		{true, true, 1, false, "uygulama yönetimli"},
		{false, false, 1, false, "spans_local yok"},
		{false, true, 0, false, "system.clusters boş"},
		{false, true, 1, true, "uygulanabilir"},
	} {
		got, d := attrIndexDecision(tc.boot, tc.local, tc.clusters)
		if got != tc.want || !strings.Contains(d, tc.detail) {
			t.Errorf("(%v,%v,%d) = %v %q; want %v %q", tc.boot, tc.local, tc.clusters, got, d, tc.want, tc.detail)
		}
	}
}
