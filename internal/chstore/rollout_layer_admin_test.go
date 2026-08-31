package chstore

import (
	"context"
	"strings"
	"testing"
)

// rollout_layer_admin_test.go — v0.10.197 sözleşmesi (rollout_layer_admin.go
// başlığı; entity_layer_admin_test.go aynası). Gömülü 0012 parse edilir;
// ifade SIRASI kritik sırayı korur (spans_local kolonları → spans
// sarmalayıcı → index'ler → state tabloları → MV → distributed); küme
// token'ı hiçbir ifadede kalmaz; her ifade ON CLUSTER taşır; withMV=false
// MV + sarmalayıcıyı düşürür; nesne listesi her DDL hedefini kapsar.

func TestRolloutLayerStatementsOrderAndCluster(t *testing.T) {
	stmts, err := rolloutLayerStatements("prodcluster", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) < 23 {
		t.Fatalf("0012 en az 23 ifade taşımalı (6+6 kolon, 7 index, 2 tablo, MV + sarmalayıcı), alınan %d", len(stmts))
	}
	for _, s := range stmts {
		if strings.Contains(s, "uptrace_all") {
			t.Fatalf("küme token'ı kaldı: %s", stmtHead(s))
		}
		if !strings.Contains(s, "ON CLUSTER prodcluster") {
			t.Fatalf("her ifade ON CLUSTER taşımalı: %s", stmtHead(s))
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
	firstCol := idx("ALTER TABLE spans_local ON CLUSTER prodcluster\n  ADD COLUMN IF NOT EXISTS k8s_deployment")
	wrapperCol := idx("ALTER TABLE spans ON CLUSTER prodcluster\n  ADD COLUMN IF NOT EXISTS k8s_deployment")
	index := idx("ADD INDEX IF NOT EXISTS idx_k8s_replicaset")
	table := idx("CREATE TABLE IF NOT EXISTS workload_rollouts ON CLUSTER")
	mv := idx("CREATE MATERIALIZED VIEW IF NOT EXISTS workload_revision_activity_1m_local")
	dist := idx("CREATE TABLE IF NOT EXISTS workload_revision_activity_1m ON CLUSTER")
	for name, i := range map[string]int{"kolon": firstCol, "sarmalayıcı": wrapperCol, "index": index, "tablo": table, "mv": mv, "distributed": dist} {
		if i < 0 {
			t.Fatalf("%s ifadesi bulunamadı", name)
		}
	}
	if !(firstCol < wrapperCol && wrapperCol < index && index < table && table < mv && mv < dist) {
		t.Fatalf("sıra bozuk (kolon→sarmalayıcı→index→tablo→mv→distributed): col=%d wrap=%d idx=%d tbl=%d mv=%d dist=%d", firstCol, wrapperCol, index, table, mv, dist)
	}
	// MV state tabloyla aynı şekli okur: store.go şablonuyla aynı kolon kümesi
	mvStmt := stmts[mv]
	for _, want := range []string{"FROM spans_local", "if(k8s_replicaset != '', k8s_replicaset, container_image_tag)   AS revision", "anyLastSimpleState(container_image_tag)", "service_name,", "WHERE workload != '' AND revision != ''"} {
		if !strings.Contains(mvStmt, want) {
			t.Fatalf("0012 MV'de eksik: %s", want)
		}
	}
	if strings.Contains(mvStmt, "indexOf(") {
		t.Fatal("0012 MV dizi araması yapmamalı (terfi kolonu okur)")
	}
	// Faz 1a: MV'siz uygulama
	noMV, _ := rolloutLayerStatements("prodcluster", false)
	if len(noMV) != len(stmts)-2 {
		t.Fatalf("withMV=false MV + sarmalayıcıyı düşürmeli: %d vs %d", len(noMV), len(stmts))
	}
	for _, s := range noMV {
		if strings.Contains(s, "workload_revision_activity_1m") {
			t.Fatalf("withMV=false'ta MV ifadesi kaldı: %s", stmtHead(s))
		}
	}
}

func TestRolloutLayerObjectsCoverEveryDDLTarget(t *testing.T) {
	stmts, err := rolloutLayerStatements("c", true)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(stmts, "\n")
	for _, o := range RolloutLayerObjects() {
		var needle string
		switch o.Kind {
		case "column":
			needle = "ADD COLUMN IF NOT EXISTS " + o.Name + " "
		case "index":
			needle = "ADD INDEX IF NOT EXISTS " + o.Name + " "
		case "table", "distributed":
			needle = "CREATE TABLE IF NOT EXISTS " + o.Name + " ON CLUSTER"
		case "mv":
			needle = "CREATE MATERIALIZED VIEW IF NOT EXISTS " + o.Name + " ON CLUSTER"
		}
		if !strings.Contains(all, needle) {
			t.Errorf("nesne listesindeki %s (%s) 0012'de yok", o.Name, o.Kind)
		}
	}
	// ters yön: dosyadaki her CREATE/ADD COLUMN/ADD INDEX hedefi listede
	for _, s := range stmts {
		for _, pre := range []string{"ADD COLUMN IF NOT EXISTS ", "ADD INDEX IF NOT EXISTS ", "CREATE TABLE IF NOT EXISTS ", "CREATE MATERIALIZED VIEW IF NOT EXISTS "} {
			if i := strings.Index(s, pre); i >= 0 {
				name := strings.Fields(s[i+len(pre):])[0]
				found := false
				for _, o := range RolloutLayerObjects() {
					if o.Name == name {
						found = true
					}
				}
				if !found {
					t.Errorf("0012 hedefi nesne listesinde yok: %s", name)
				}
			}
		}
	}
}

func TestRolloutLayerMVGate(t *testing.T) {
	c := func(name string, sampled uint64, rs float64) RolloutLayerClusterCoverage {
		return RolloutLayerClusterCoverage{Cluster: name, Sampled: sampled, ReplicaSet: rs}
	}
	if !rolloutLayerMVGateOK([]RolloutLayerClusterCoverage{c("a", 100, 0.99), c("b", 50, 0.96)}, 0.95) {
		t.Fatal("iki cluster eşik üstünde → açık olmalı")
	}
	// 2026-08-30 dersi: bir cluster namespace/replicaset basmıyor → kapı KAPALI
	if rolloutLayerMVGateOK([]RolloutLayerClusterCoverage{c("a", 100, 0.99), c("b", 50, 0.0)}, 0.95) {
		t.Fatal("bir cluster eşik altında → kapalı olmalı")
	}
	if rolloutLayerMVGateOK(nil, 0.95) || rolloutLayerMVGateOK([]RolloutLayerClusterCoverage{c("a", 0, 0)}, 0.95) {
		t.Fatal("örneklemsiz → kapalı")
	}
	// İnceleme B2: VAR olan (Total>0) ama örneklemde GÖRÜLMEYEN cluster
	// "ölçemedim"dir → atlanmaz, kapatır.
	if rolloutLayerMVGateOK([]RolloutLayerClusterCoverage{c("a", 100, 0.99), {Cluster: "b", Total: 5000}}, 0.95) {
		t.Fatal("örneklemsiz cluster kapıyı kapatmalı")
	}
	// '' = cluster'sız (k8s dışı) trafik: görünür ama kapıya girmez.
	if !rolloutLayerMVGateOK([]RolloutLayerClusterCoverage{c("a", 100, 0.99), c("", 300, 0.0)}, 0.95) {
		t.Fatal("cluster'sız satır kapıyı kapatmamalı")
	}
	if rolloutLayerMVGateOK([]RolloutLayerClusterCoverage{c("", 300, 1.0)}, 0.95) {
		t.Fatal("yalnız cluster'sız trafik → kapalı (adı olan cluster yok)")
	}
}

// TestValidRolloutLayerCluster — inceleme S5: ad DDL'e ham giriyor.
func TestValidRolloutLayerCluster(t *testing.T) {
	for name, ok := range map[string]bool{"uptrace_all": true, "prod-ch.1": true, "a": true,
		"": false, "x; DROP TABLE spans": false, "a b": false, "ç": false, "`x`": false} {
		if got := validRolloutLayerCluster(name); got != ok {
			t.Errorf("%q → %v, want %v", name, got, ok)
		}
	}
	if r := (&Store{}).RolloutLayerApply(context.Background(), "x;y", false); len(r) != 1 || r[0].Err == "" {
		t.Fatalf("geçersiz ad DDL'e ulaşmamalı: %+v", r)
	}
	if r := (&Store{}).RolloutLayerRollback(context.Background(), "x y"); len(r) != 1 || r[0].Err == "" {
		t.Fatalf("geçersiz ad rollback'e ulaşmamalı: %+v", r)
	}
}

func TestRolloutLayerRollbackDropsOnlyMV(t *testing.T) {
	stmts := rolloutLayerRollbackStatements("c1")
	if len(stmts) != 2 {
		t.Fatalf("geri alma yalnız MV + sarmalayıcı (2), alınan %d", len(stmts))
	}
	for _, s := range stmts {
		if !strings.HasPrefix(s, "DROP TABLE IF EXISTS workload_revision_activity_1m") || !strings.HasSuffix(s, " SYNC") || !strings.Contains(s, "ON CLUSTER c1") {
			t.Fatalf("beklenmeyen geri alma ifadesi: %s", s)
		}
	}
}
