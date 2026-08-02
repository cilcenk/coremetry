package api

import (
	"strings"
	"testing"
)

// v0.9.540 — /admin/clickhouse "in-flight merges" paneli HİÇ satır
// döndürmüyordu ve kimse fark etmedi.
//
// Kök sebep: sorgu `merged_rows_bytes` seçiyordu; system.merges'te
// böyle bir kolon YOK (CH zemin gerçeği: bytes_read_uncompressed,
// bytes_written_uncompressed, rows_read, rows_written, …). Her çağrı
// UNKNOWN_IDENTIFIER veriyor, `if err == nil` guard'ı hatayı yutuyor,
// panel sessizce boş kalıyordu.
//
// Sessizliğin bedeli: boş liste operatöre "merge yok" diye okunur —
// oysa ÖLÇÜM HİÇ YAPILMAMIŞTI. Tam da "hangi node merge'e boğulmuş"
// sorusunu sorduğumuz panelde.
//
// Bu testler sorgu METNİNİ pinler (canlı CH olmadan koşabilmesi için).
// Kolon adlarının gerçekliği CH'ye karşı bir kez doğrulandı; test
// buradan geriye kaymayı yakalar.

func TestMergesQueryUsesRealColumns(t *testing.T) {
	q := inFlightMergesQuery("")

	// Var olmayan kolon geri gelmesin.
	if strings.Contains(q, "merged_rows_bytes") {
		t.Error("merged_rows_bytes system.merges'te YOK — v0.9.540 gerilemesi")
	}
	// Doğru kolon: merge'in ÜRETTİĞİ hacim (MergedSize'ın kastettiği).
	if !strings.Contains(q, "bytes_written_uncompressed") {
		t.Errorf("bytes_written_uncompressed kullanılmalı:\n%s", q)
	}
	// Diğer kolonların hepsi gerçek (aynı doğrulamadan geçti).
	for _, col := range []string{"database", "table", "elapsed", "progress", "rows_read"} {
		if !strings.Contains(q, col) {
			t.Errorf("%q kolonu kayıp:\n%s", col, q)
		}
	}
	// Sınırlar (CLAUDE.md sert kısıtı).
	if !strings.Contains(q, "LIMIT") || !strings.Contains(q, "max_execution_time") {
		t.Errorf("LIMIT + max_execution_time şart:\n%s", q)
	}
}

// Küme yapılandırılmışsa okuma TÜM replikalara gitmeli. cluster()
// shard başına TEK replika okur → 2 shard × 2 replika kurulumda panel
// 4 yerine 2 node gösterir (v0.9.454 operatör bug'ı, koordinatör
// panelinin de aynı gerekçeyle clusterAllReplicas kullanmasının sebebi).
func TestMergesQueryClusterWide(t *testing.T) {
	q := inFlightMergesQuery("prod_cluster")

	if !strings.Contains(q, "clusterAllReplicas('prod_cluster', system.merges)") {
		t.Errorf("küme varken clusterAllReplicas kullanılmalı:\n%s", q)
	}
	if strings.Contains(q, "cluster('") {
		t.Error("cluster() DEĞİL — shard başına tek replika okur, node'ların yarısı kaybolur")
	}
	if !strings.Contains(q, "hostName()") {
		t.Errorf("host kolonu şart — panelin cevapladığı soru 'HANGİ node':\n%s", q)
	}
}

// Küme adı boşken tek node okunur ama host kolonu YİNE gelir (bağlı
// olunan node'un adı) — tip ve UI tek şekille çalışsın.
func TestMergesQueryStandalone(t *testing.T) {
	q := inFlightMergesQuery("")
	if strings.Contains(q, "clusterAllReplicas") {
		t.Errorf("küme adı boşken sarmalayıcı olmamalı:\n%s", q)
	}
	if !strings.Contains(q, "FROM system.merges") {
		t.Errorf("düz system.merges okunmalı:\n%s", q)
	}
	if !strings.Contains(q, "hostName()") {
		t.Error("standalone'da da host kolonu gelmeli — tek şekil")
	}
}

// Küme adı SQL'e gömülüyor; boşluk kırpılmalı ki yapılandırmadaki
// kazara boşluk sorguyu bozmasın.
func TestMergesQueryTrimsClusterName(t *testing.T) {
	if q := inFlightMergesQuery("  prod_cluster  "); !strings.Contains(q, "'prod_cluster'") {
		t.Errorf("küme adı kırpılmalı:\n%s", q)
	}
}
