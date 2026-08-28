package chstore

import (
	"strings"
	"testing"
)

// v0.10.134 — Operator-reported (prod): "cluster eşleşme için sihirbaz —
// 0011 MV'yi görmedim". 0011 yalnız elle uygulanan bir dosyaydı; Admin →
// ClickHouse sihirbazına adım olarak girer (rollup/0009 emsali): durum
// (host başına kolon/index/tablo/MV), ön kontrol (küme, kapsama,
// kardinalite kapısı), uygula (gömülü 0011, ON CLUSTER token'ı gerçek
// küme adıyla), geri al (yalnız MV'ler — yazımı keser, veri kalır).
//
// Sözleşme (saf): gömülü 0011 parse edilir; ifade SIRASI dosyadaki kritik
// sırayı korur (spans_local kolonları → spans sarmalayıcı → index'ler →
// state tabloları → MV'ler); küme token'ı hiçbir ifadede kalmaz; nesne
// listesi durum probe'unun taradığı her adı içerir.

func TestEntityLayerStatementsOrderAndCluster(t *testing.T) {
	stmts, err := entityLayerStatements("prodcluster")
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) < 12 {
		t.Fatalf("0011 en az 12 ifade taşımalı (3+3 kolon, 2 index, 3 tablo, 4 MV/sarmalayıcı), alınan %d", len(stmts))
	}
	heads := make([]string, len(stmts))
	for i, s := range stmts {
		heads[i] = stmtHead(s)
		if strings.Contains(s, "uptrace_all") {
			t.Fatalf("küme token'ı kaldı: %s", heads[i])
		}
		if !strings.Contains(s, "ON CLUSTER prodcluster") {
			t.Fatalf("her ifade ON CLUSTER taşımalı: %s", heads[i])
		}
	}
	idx := func(sub string) int {
		for i, s := range stmts {
			if strings.Contains(s, sub) {
				return i
			}
		}
		return -1
	}
	firstCol := idx("ALTER TABLE spans_local ON CLUSTER prodcluster\n  ADD COLUMN IF NOT EXISTS k8s_namespace")
	wrapperCol := idx("ALTER TABLE spans ON CLUSTER prodcluster\n  ADD COLUMN IF NOT EXISTS k8s_namespace")
	index := idx("ADD INDEX IF NOT EXISTS idx_k8s_pod")
	table := idx("CREATE TABLE IF NOT EXISTS entities ON CLUSTER")
	mv := idx("CREATE MATERIALIZED VIEW IF NOT EXISTS entity_seen_1m_local")
	dist := idx("CREATE TABLE IF NOT EXISTS entity_seen_1m ON CLUSTER")
	for name, i := range map[string]int{"kolon": firstCol, "sarmalayıcı": wrapperCol, "index": index, "tablo": table, "mv": mv, "distributed": dist} {
		if i < 0 {
			t.Fatalf("%s ifadesi bulunamadı; başlıklar: %v", name, heads)
		}
	}
	if !(firstCol < wrapperCol && wrapperCol < index && index < table && table < mv && mv < dist) {
		t.Fatalf("sıra bozuk (kolon→sarmalayıcı→index→tablo→mv→distributed): col=%d wrap=%d idx=%d tbl=%d mv=%d dist=%d", firstCol, wrapperCol, index, table, mv, dist)
	}
}

func TestEntityLayerObjectsCoverEveryDDLTarget(t *testing.T) {
	objs := EntityLayerObjects()
	want := map[string]string{
		"k8s_namespace": "column", "k8s_pod": "column", "k8s_node": "column",
		"idx_k8s_pod": "index", "idx_k8s_node": "index",
		"entities": "table", "entity_relations": "table", "entity_sync_runs": "table",
		"entity_seen_1m_local": "mv", "entity_seen_5m_local": "mv",
		"entity_seen_1m": "distributed", "entity_seen_5m": "distributed",
	}
	seen := map[string]bool{}
	for _, o := range objs {
		k, ok := want[o.Name]
		if !ok {
			t.Errorf("beklenmeyen nesne %q", o.Name)
			continue
		}
		if o.Kind != k {
			t.Errorf("%s türü %q, beklenen %q", o.Name, o.Kind, k)
		}
		seen[o.Name] = true
	}
	for n := range want {
		if !seen[n] {
			t.Errorf("nesne listesinde eksik: %s", n)
		}
	}
}

func TestEntityLayerObjectState(t *testing.T) {
	cases := []struct {
		have, total int
		want        string
	}{
		{0, 4, "missing"}, {4, 4, "ok"}, {2, 4, "partial"}, {0, 0, "unknown"},
	}
	for _, c := range cases {
		if got := entityLayerObjectState(c.have, c.total); got != c.want {
			t.Errorf("(%d/%d) %q, beklenen %q", c.have, c.total, got, c.want)
		}
	}
}

func TestEntityLayerRollbackDropsOnlyMVs(t *testing.T) {
	stmts := entityLayerRollbackStatements("c1")
	if len(stmts) != 4 {
		t.Fatalf("geri alma yalnız iki MV + iki sarmalayıcı düşürmeli (4), alınan %d", len(stmts))
	}
	for _, s := range stmts {
		if !strings.HasPrefix(s, "DROP TABLE IF EXISTS entity_seen_") || !strings.Contains(s, "ON CLUSTER c1") {
			t.Fatalf("beklenmeyen geri alma ifadesi: %s", s)
		}
		// Atomic DB'de tembel DROP znode'u 8 dk bırakır → sonraki CREATE 253 (ölçüldü).
		if !strings.HasSuffix(s, " SYNC") {
			t.Fatalf("geri alma DROP'u SYNC olmalı: %s", s)
		}
		if strings.Contains(s, "entities") || strings.Contains(s, "spans") {
			t.Fatalf("geri alma tablo/kolon düşürmemeli: %s", s)
		}
	}
}
