package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.9.692 — SÜRÜKLENME İNVARİANTI ARTIK GERÇEK YERİNDE: DDL'de.
//
// Eski test okuma yolunun (clusterColExpr) clusterDeriveExpr'i GÖMMESİNİ
// şart koşuyordu ve gerekçesi yanlıştı: "kolon okuması önce gelmeli ki
// CH kısa devre yapıp indexOf taramasını atlasın". Kısa devre SATIR
// YÜRÜTMESİNİ atlar, KOLON OKUMASINI değil — ölçüldü: 566 B/satır vs
// 8.6 B/satır (~66×).
//
// Sürüklenme korkusu gerçekti ama yeri yanlıştı. Gerçek garanti şu:
// kolon `MATERIALIZED clusterDeriveExpr` olarak tanımlı, ve MATERIALIZED
// kolon onu saklamayan eski parçalarda OKUMA ANINDA hesaplanıyor. Yani
// yeni ve eski parçalar aynı ifadeyi kullanıyor — okuma yolunun ikinci
// bir kopyasını taşımasına gerek yok.
//
// Bu test o garantiyi DDL'de çiviliyor.
func TestClusterMaterializedExprMatchesDerive(t *testing.T) {
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	body := stripGoLineComments(string(src))

	// CREATE: `cluster ... MATERIALIZED %s` + Sprintf argümanı derive olmalı.
	if !strings.Contains(body, "cluster       LowCardinality(String) MATERIALIZED %s") {
		t.Error("CREATE TABLE'da cluster kolonu MATERIALIZED yer-tutucu biçiminde değil")
	}
	if !strings.Contains(body, "`, clusterDeriveExpr,") {
		t.Error("CREATE TABLE Sprintf'i clusterDeriveExpr geçmiyor — yeni/eski parçalar SÜRÜKLENİR")
	}
	// ALTER (sonradan eklenen kurulumlar) da aynı ifadeyi kullanmalı.
	if !strings.Contains(body, "MATERIALIZED ` + clusterDeriveExpr") {
		t.Error("ALTER ADD COLUMN clusterDeriveExpr kullanmıyor — sürüklenme")
	}
}

// PERF İNVARİANTI: okuma yolu DÜZ KOLON okumalı, dizi taraması
// TAŞIMAMALI.
//
// ÖLÇÜLDÜ (chc-0, aynı pencere, aynı çıktı): coalesce fallback
// 137.15 MiB / 247k satır = 566 B/satır · 81 ms; düz kolon 8.71 MiB /
// 1.015M satır = 8.6 B/satır · 11 ms → ~66× bayt. 6 saatte 20.383
// boş-cluster satırın türetmeyle kurtarılanı SIFIR.
func TestClusterColExprIsPlainColumnRead(t *testing.T) {
	if clusterColExpr != "cluster" {
		t.Errorf("okuma yolu düz kolon olmalı, alınan %q", clusterColExpr)
	}
	for _, scan := range []string{"res_values[indexOf", "attr_values[indexOf", "coalesce"} {
		if contains(clusterColExpr, scan) {
			t.Errorf("okuma yolunda %q var — her satırda dizi kolonları diskten okunur (~66× bayt)", scan)
		}
	}
}

// Kolon HİÇ YOKSA (harici Distributed, ALTER uygulanmamış) ham türetmeye
// düşülmeli. v0.8.162'de operatör-bildirimli code 47 olayını bu dal
// çözmüştü; perf düzeltmesi onu kaldırmamalı.
func TestClusterExprFallsBackWhenColumnAbsent(t *testing.T) {
	src, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatal(err)
	}
	body := stripGoLineComments(string(src))
	i := strings.Index(body, "func (s *Store) clusterExpr()")
	if i < 0 {
		t.Fatal("clusterExpr bulunamadı")
	}
	fn := body[i : i+220]
	if !strings.Contains(fn, "s.hasClusterCol") {
		t.Error("hasClusterCol probu düşmüş — harici Distributed yolu kırılır")
	}
	if !strings.Contains(fn, "return clusterDeriveExpr") {
		t.Error("kolon yokken ham türetmeye düşülmüyor")
	}
}

// v0.8.162 — operator-reported (external Distributed cluster, cluster_name
// unset): the cluster warm query spammed code 47 ("Identifier
// '__table1.cluster' cannot be resolved") because clusterColExpr references
// the materialized `cluster` column unconditionally, but on an external
// Distributed `spans` the column never reaches spans_local. clusterExpr()
// now gates the column reference on s.hasClusterCol (probed at boot): when
// the column is absent every cluster query must use the pure derive — which
// references ONLY res_values/attr_values, never the `cluster` column — so it
// resolves against spans_local everywhere.
func TestClusterExpr_DropsColumnRefWhenAbsent(t *testing.T) {
	withCol := (&Store{hasClusterCol: true}).clusterExpr()
	if withCol != clusterColExpr {
		t.Fatalf("hasClusterCol=true must use the column-aware clusterColExpr, got %q", withCol)
	}

	noCol := (&Store{hasClusterCol: false}).clusterExpr()
	if noCol != clusterDeriveExpr {
		t.Fatalf("hasClusterCol=false must use the pure derive, got %q", noCol)
	}
	// The whole point: with the column absent the expression must NOT
	// reference `cluster` at all (that's the code-47 trigger on spans_local).
	if contains(noCol, "nullIf(cluster, '')") || contains(noCol, "cluster, ''") {
		t.Fatal("the no-column expression must not reference the `cluster` " +
			"column — it fails with code 47 on an external Distributed spans_local")
	}
}

func contains(haystack, needle string) bool { return indexOf(haystack, needle) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
