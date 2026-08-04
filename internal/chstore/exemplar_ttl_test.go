// v0.9.620 — exemplar TTL'i inner tabloyu KÜME GENELİNDE çözmeli.
//
// Operator-reported (prod): her boot'ta altı satır code 60
// (UNKNOWN_TABLE), hep aynı host için:
//
//	exemplar column TTL on spanmetrics_1m_local.slow_exemplar_state
//	not applied: code: 60 … Could not find table: .inner_id.<uuid>
//
// Sebep bir VARSAYIMDI ve kodun kendi yorumunda yazılıydı:
// ".inner_id.<uuid> adı shard'lar arasında sabit (Atomic DB, ON CLUSTER
// create tek uuid yayar)". Sağlıklı kümede doğru — lokal 2 düğümde
// uuid'ler birebir aynı ÖLÇÜLDÜ — ama GARANTİ değil: bir host sonradan
// eklenmiş ya da MV'yi yerel yeniden yaratmışsa kendi uuid'sini taşır.
//
// Sonuç sessizdi: TTL o host'ta HİÇ uygulanmıyor, exemplar kolonları
// süresiz büyüyor. Log "may outlive their traces" diyordu ama sebebi
// söylemiyordu.
package chstore

import (
	"strings"
	"testing"
)

// TestExemplarTTLResolvesInnerClusterWide — KONUM sözleşmesi.
//
// Kaynak taraması, çünkü kırılma bir çağrı seçimi: tek-host çözücüye
// geri dönülürse ayrışmış kümede TTL yine sessizce uygulanmaz ve
// hiçbir davranış testi bunu yakalamaz (tek düğümde ikisi de çalışır).
func TestExemplarTTLResolvesInnerClusterWide(t *testing.T) {
	src := storeSourceNoCommentsFile(t, "retention.go")
	i := strings.Index(src, "func (s *Store) applyExemplarColTTL(")
	if i < 0 {
		t.Fatal("applyExemplarColTTL bulunamadı — test bayatladı")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	if !strings.Contains(body, "mvInnerTablesCluster(") {
		t.Error("inner tablo küme genelinde çözülmüyor — koordinatörün uuid'si " +
			"ON CLUSTER'a gömülürse ayrışmış host'ta TTL HİÇ uygulanmaz ve " +
			"exemplar kolonları süresiz büyür")
	}
	if strings.Contains(body, "s.mvInnerTable(ctx") {
		t.Error("tek-host çözücüye geri dönülmüş")
	}
	// Tip çözümü de küme geneli olmalı: ayrışmada koordinatör başka
	// host'un inner tablosunu system.columns'ta GÖRMEZ, tip çözülemez
	// ve TTL o uuid için hiç denenmez — ayrışma kendi düzeltmesini
	// engeller.
	if !strings.Contains(body, "columnTypeCluster(") {
		t.Error("kolon tipi küme genelinde çözülmüyor — ayrışmış uuid için tip " +
			"bulunamaz ve ALTER hiç denenmez")
	}
}

// TestExemplarTTLStmtNamesInnerTable — MV adı KULLANILAMAZ.
//
// Canlı ClickHouse 24.8'de doğrulandı:
//
//	ALTER TABLE <mv> MODIFY COLUMN … TTL …
//	→ Code: 36. Engine MaterializedView doesn't support TTL clause.
//
// Yani ".inner_id.<uuid>" adını kullanmak bir tercih değil, tek yol.
// Biri "daha temiz" diye MV adına çevirmeye kalkarsa bu test durdurur.
func TestExemplarTTLStmtNamesInnerTable(t *testing.T) {
	stmt := exemplarColTTLStmt(".inner_id.abc", "", "slow_exemplar_state",
		"AggregateFunction(argMax, String, DateTime64(9))", "time_bucket + INTERVAL 7 DAY")
	if !strings.Contains(stmt, ".inner_id.abc") {
		t.Errorf("ALTER inner tabloyu adlandırmıyor: %s\n\nMV adı ClickHouse "+
			"tarafından REDDEDİLİR (code 36, canlıda doğrulandı).", stmt)
	}
	if !strings.Contains(stmt, "TTL") {
		t.Errorf("TTL yan tümcesi yok: %s", stmt)
	}
}
