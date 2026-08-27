package api

import (
	"strings"
	"testing"
)

// v0.9.543 — node iş dağılımı paneli. Operatör soruşturmasında (prod,
// 2026-08-02) elle koşulan sorgu kalıcı hale getirildi: dbp01'in CPU'su
// 6 saattir 2-3x, ve eşleşen tek sinyal insert dağılımı çıktı
// (CPU 2.76x ↔ insert 2.93x; merge yalnız 1.9x, yani oransız).
//
// Bu testler SQL şeklini pinler — canlı CH olmadan koşabilsin diye saf
// üreteçler ayrıldı (coordinatorQuery deseni).

func TestNodeWorkQueryShape(t *testing.T) {
	q := nodeWorkQuery("")

	// Ölçümün TAMAMI: CPU ile birlikte okunmayan merge süresi yanlış
	// sonuca götürür (merge sayacı CPU değil THREAD-MEŞGUL süresidir,
	// I/O'da bloke geçen süre dahil — CPU'yu aşabilir).
	for _, ev := range []string{
		"OSCPUVirtualTimeMicroseconds", "MergeExecuteMilliseconds",
		"InsertedRows", "ReplicatedPartFetches", "SelectedBytes", "Merge",
	} {
		if !strings.Contains(q, "'"+ev+"'") {
			t.Errorf("%q sayacı sorguda yok:\n%s", ev, q)
		}
	}
	// Uptime ŞART: sayaçlar kümülatif; uptime geriye giderse node
	// yeniden başlamıştır ve istemcinin tabanı geçersizdir. Onsuz
	// restart eden node "iş yapmıyor" görünür.
	if !strings.Contains(q, "uptime()") {
		t.Errorf("uptime okunmalı — restart tespiti buna bağlı:\n%s", q)
	}
	if !strings.Contains(q, "GROUP BY host") || !strings.Contains(q, "hostName()") {
		t.Errorf("host başına gruplama şart:\n%s", q)
	}
	// Sert kısıt (CLAUDE.md).
	if !strings.Contains(q, "LIMIT") || !strings.Contains(q, "max_execution_time") {
		t.Errorf("LIMIT + max_execution_time şart:\n%s", q)
	}
}

// Küme varken TÜM replikalara gidilir. cluster() shard başına TEK
// replika okur → 2 shard x 2 replika kurulumda panel 4 yerine 2 node
// gösterir (v0.9.454 operatör bug'ı; koordinatör paneli de bu yüzden
// clusterAllReplicas kullanıyor).
func TestNodeWorkQueryClusterWide(t *testing.T) {
	q := nodeWorkQuery("uptrace_all")
	if !strings.Contains(q, "clusterAllReplicas('uptrace_all', system.events)") {
		t.Errorf("clusterAllReplicas kullanılmalı:\n%s", q)
	}
	if strings.Contains(q, "cluster('") {
		t.Error("cluster() DEĞİL — node'ların yarısı kaybolur")
	}
}

func TestNodeWorkQueryStandalone(t *testing.T) {
	q := nodeWorkQuery("   ")
	if strings.Contains(q, "clusterAllReplicas") {
		t.Errorf("küme adı boşken sarmalayıcı olmamalı:\n%s", q)
	}
	if !strings.Contains(q, "FROM system.events") {
		t.Errorf("düz system.events okunmalı:\n%s", q)
	}
}

// UNION ALL BİLİNÇLİ olarak yok. İki tuzağı birden doğuruyordu:
//
//  1. `Merge` adı hem system.events'te (kümülatif, başlatılan merge)
//     hem system.metrics'te (anlık, koşan merge) var — düz bir
//     (host,key,value) şeklinde ayırt edilemezler, iki kolon birden
//     bozulur.
//  2. UNION ALL'da `LIMIT` yalnız SON SELECT'e bağlanır; diğer dallar
//     sınırsız kalır.
func TestNodeWorkQueryAvoidsUnion(t *testing.T) {
	q := nodeWorkQuery("uptrace_all")
	if strings.Contains(strings.ToUpper(q), "UNION") {
		t.Error("UNION kullanılmamalı — Merge ad çakışması + LIMIT bağlanma tuzağı")
	}
	if strings.Contains(q, "system.metrics") {
		t.Error("system.metrics okunmuyor — `Merge` adı events'tekiyle çakışır")
	}
}

// Shard etiketleri IP↔hostname eşlemesi GEREKTİRMEDEN çözülmeli:
// her node kendi kaydını is_local ile bildirir, hostName() ile eşleşir.
// Shard bilgisi olmadan dengesizlik yanlış okunur — aynı shard'ın
// replikaları aynı veriyi tutar, farklı shard'lar tanım gereği farklı.
func TestNodeShardQuery(t *testing.T) {
	q := nodeShardQuery("uptrace_all")
	for _, want := range []string{"macro = 'shard'", "macro = 'replica'", "hostName()",
		"clusterAllReplicas('uptrace_all', system.macros)"} {
		if !strings.Contains(q, want) {
			t.Errorf("%q eksik:\n%s", want, q)
		}
	}
	// Küme yoksa shard sorgusu HİÇ koşmamalı — standalone'da
	// system.clusters anlamlı bir cevap vermez.
	if got := nodeShardQuery(""); got != "" {
		t.Errorf("küme adı boşken boş dönmeli, got:\n%s", got)
	}
	if got := nodeShardQuery("  "); got != "" {
		t.Error("yalnız boşluk da boş sayılmalı")
	}
}

// v0.9.544 — v0.9.543 HİÇBİR kümede veri döndüremiyordu ve bunu kimse
// göremiyordu.
//
// Kök sebep: `any(uptime())` UInt32 döner (CH zemin gerçeği:
// TSVWithNamesAndTypes → String/UInt32/UInt64/...), struct alanı ise
// uint64. clickhouse-go'nun UInt32 kolonu *uint64 kabul etmez ve HER
// satırda ColumnConverterError verir. Tarama hatası sessiz `continue`
// ile yutulduğu için sonuç boş liste oluyor ve panel "satır dönmedi"
// diyordu — yani ölçemediğini değil, YANLIŞ ŞEYİ söylüyordu.
//
// İki kapı birden kapatıldı: SQL'de cast + hatanın yüzeye çıkması.
func TestNodeWorkQueryCastsUptime(t *testing.T) {
	for _, cn := range []string{"", "uptrace_all"} {
		q := nodeWorkQuery(cn)
		if !strings.Contains(q, "toUInt64(any(uptime()))") {
			t.Errorf("uptime UInt64'e cast edilmeli — sürücü UInt32'yi uint64'e taramaz:\n%s", q)
		}
		// Çıplak any(uptime()) geri gelmesin.
		if strings.Contains(q, "       any(uptime())") {
			t.Errorf("cast'siz any(uptime()) kalmış (v0.9.543 gerilemesi):\n%s", q)
		}
	}
}

// v0.9.547 — AD EŞLEŞTİRMESİ terk edildi. İki kez patladı:
//
//	is_local             → lokal kümede her iki node da 0 bildiriyor
//	host_name=hostName() → host_name FQDN (chc-0.chc-headless) ya da IP
//	                       (172.31.240.15); hostName() kısa ad
//
// system.macros eşleştirme GEREKTİRMİYOR: her node kendi kimliğini
// kendi yapılandırmasından okuyor. Replicated tablosu olan her kümede
// tanımlıdır (ZooKeeper yolu makrolarla kurulur).
func TestNodeShardQueryUsesMacrosNotNameMatching(t *testing.T) {
	q := nodeShardQuery("uptrace_all")
	if strings.Contains(q, "is_local") || strings.Contains(q, "host_name") {
		t.Errorf("ad eşleştirmesine geri dönülmüş — hiçbir kurulumda güvenilir değil:\n%s", q)
	}
	if !strings.Contains(q, "system.macros") {
		t.Errorf("kaynak system.macros olmalı:\n%s", q)
	}
	// shard sıfır dolgulu string gelir ("01"); parse edilemezse 0 →
	// panel "shard bilinmiyor" der, sessizce yanlış gruplamaz.
	if !strings.Contains(q, "toUInt32OrZero") {
		t.Errorf("shard parse'ı hataya dayanıklı olmalı:\n%s", q)
	}
}
