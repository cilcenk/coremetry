package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.10.19 — F0.8'in ölçümlü yarısı. /database kimliğinin `instance`
// alanı aslında `peer_service` ve MV aynı peer.service'i paylaşan farklı
// fiziksel adresleri TEK satıra çöküyor. Çökme kasıtlı (topolojide
// çekirdek DB tek düğüm olmalı); kusur SÖYLENMEMESİ.
//
// ÖLÇÜLDÜ (lokal CH, 2 saatlik pencere): oracle/oracle/COREBANK → 2
// adres, oracle/oracle/CARDS → 2, diğer 13 kimlik → 1.
// ⚠ Bu "2" bir DEMO SABİTİ: cmd/demo/main.go:545-546 iki adresi elle
// yazıyor (corebank-scan.prod + corebank-dg.prod) ve peer.service'i
// bilerek "oracle"a çöküyor. Yani sayı fixture, MEKANİZMA değil —
// çökmeyi yapan store.go'daki MV anahtarı.

func TestAddrProbeIsCapped(t *testing.T) {
	if dbAddrProbeCap < 2 {
		t.Fatalf("cap %d — çokluğu gösterecek kadar bile değil", dbAddrProbeCap)
	}
	if !strings.Contains(dbAddrSelectSQL, "groupUniqArray(6)") {
		t.Errorf("agrega sınırsız görünüyor: %q — patolojik kurulumda "+
			"(her pod ayrı adres) bellek yer", dbAddrSelectSQL)
	}
	if strings.Contains(dbAddrSelectSQL, "uniqExact") {
		t.Error("uniqExact adresleri KENDİSİNİ vermez; operatöre 'hangileri' " +
			"gösterilemez ve durum sınırlanmaz")
	}
}

// TestProbeSharesTheStatementPredicate — EN ÖNEMLİ KAPI.
//
// İki sorgu aynı kapsamı anlatmak zorunda. Ayrışırlarsa çekmecenin üst
// yarısı bir kapsamı, adres beyanı başka bir kapsamı anlatır — v0.9.821
// bu dosyada tam olarak bu kusuru düzeltmişti ve aynısını yeniden
// üretmek kolay.
//
// Ayrıca budama: `service_name IN (?)` olmadan bu tarama milyar-span
// ölçeğinde zaman aşımına uğrar (v0.7.35 operatör bildirimi).
func TestProbeSharesTheStatementPredicate(t *testing.T) {
	b, err := os.ReadFile("dependencies.go")
	if err != nil {
		t.Fatalf("dependencies.go okunamadı: %v", err)
	}
	src := stripGoCommentsCH(string(b))

	// Tek yüklem değişkeni: iki sorgu da ONDAN besleniyor.
	if !strings.Contains(src, "out.PhysicalAddrs = s.probeDBAddresses(ctx, whereSQL,") {
		t.Error("prob paylaşılan whereSQL'i kullanmıyor — kapsam ayrışabilir")
	}
	if !strings.Contains(src, "WHERE ` + whereSQL + `") {
		t.Error("ifade taraması paylaşılan whereSQL'i kullanmıyor — kapsam ayrışabilir")
	}
	// Budama, yüklem kurulurken ekleniyor; prob ondan SONRA çağrılmalı,
	// yoksa budanmamış bir taramaya düşer.
	iPrune := strings.Index(src, "whereSQL += ` AND service_name IN (?)`")
	iProbe := strings.Index(src, "s.probeDBAddresses(ctx, whereSQL,")
	if iPrune < 0 {
		t.Fatal("service_name budaması kaybolmuş — v0.7.35 regresyonu")
	}
	if iProbe < iPrune {
		t.Error("prob budama EKLENMEDEN önce çağrılıyor — milyar-span ölçeğinde zaman aşımı")
	}
}

// TestProbeRunsBeforeTheStatementFilter — kapsam inceliği.
//
// `db_statement != ”` ifade taramasına özgü. Adres ise ifadesi olmayan
// db span'lerinde de var; prob o filtreden ÖNCE koşmalı, yoksa adresleri
// eksik sayar ve "1 adres" diyerek tekilliği yanlış yere iddia eder.
func TestProbeRunsBeforeTheStatementFilter(t *testing.T) {
	b, err := os.ReadFile("dependencies.go")
	if err != nil {
		t.Fatalf("okunamadı: %v", err)
	}
	src := stripGoCommentsCH(string(b))
	iProbe := strings.Index(src, "s.probeDBAddresses(ctx, whereSQL,")
	iFilter := strings.Index(src, "AND db_statement != ''")
	if iProbe < 0 || iFilter < 0 {
		t.Fatal("prob ya da ifade filtresi bulunamadı")
	}
	if iProbe > iFilter {
		t.Error("prob db_statement filtresinden SONRA — adresler eksik sayılır")
	}
}

// TestProbeFailsSilent — sessizlik sözleşmesi.
//
// Prob düşerse Probed=false dönmeli. Buradaki tehlike, birinin hata
// dalında `Probed: true` bırakması: o an boş sonuç "tek adres" diye
// okunur ve arayüz TEKİLLİĞİ YANLIŞ YERE iddia eder — hiçbir şey
// dememekten kötü.
func TestProbeFailsSilent(t *testing.T) {
	b, err := os.ReadFile("db_addresses.go")
	if err != nil {
		t.Fatalf("okunamadı: %v", err)
	}
	src := stripGoCommentsCH(string(b))
	if n := strings.Count(src, "return DBPhysicalAddrs{}"); n < 3 {
		t.Errorf("yalnız %d sessiz-düşüş dalı var; sorgu hatası, scan hatası "+
			"ve rows.Err() üçü de kapsanmalı", n)
	}
	if strings.Contains(src, "Probed: true, Capped") &&
		!strings.Contains(src, "out := DBPhysicalAddrs{Probed: true, Capped:") {
		t.Error("Probed yalnız başarılı yolda true olmalı")
	}
	// Kısa tavan: bu bir süs bilgisi, sayfanın omurgası değil.
	if !strings.Contains(src, "max_execution_time = 5") {
		t.Error("prob kısa bir tavan taşımıyor — operatörü bir beyan uğruna bekletir")
	}
}

// TestEmptyAddressIsNotAnAddress — sayım dürüstlüğü.
//
// `server.address` taşımayan span'ler (eski SDK, net.peer.name kullanan
// sürüm) boş dizge üretiyor. Onu adres saymak "2 adres" derken birinin
// boş olması demekti.
func TestEmptyAddressIsNotAnAddress(t *testing.T) {
	b, err := os.ReadFile("db_addresses.go")
	if err != nil {
		t.Fatalf("okunamadı: %v", err)
	}
	if !strings.Contains(stripGoCommentsCH(string(b)), `if a != ""`) {
		t.Error("boş adres eleniyor değil — sayım şişer")
	}
}

func TestPhysicalAddrsCount(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   DBPhysicalAddrs
		want int
	}{
		{"ölçülmedi", DBPhysicalAddrs{}, 0},
		{"tek", DBPhysicalAddrs{Probed: true, Addrs: []string{"a"}}, 1},
		{"iki", DBPhysicalAddrs{Probed: true, Addrs: []string{"a", "b"}}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Count(); got != tc.want {
				t.Errorf("Count() = %d; want %d", got, tc.want)
			}
		})
	}
}
