package chstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// identity_test.go — v0.9.1318 (entity-model A3).
//
// Bu testlerin işi, identity.go'daki zincirleri SIRAYLA çivilemek.
// Gerekçe tek cümle: paylaşılan bir sabit, herhangi bir refactor'ın
// sessizce yeniden sıralayabildiği yerde AYNI bug'ın daha dolaylı
// hâlidir. coalesce İLK boş-olmayanı alır — bir basamak kaydırmak,
// düşmek ya da araya girmek kimliği YENİDEN ADLANDIRIR ve bu ne
// derleme ne çalışma hatası verir; yalnız bir yüzeyin linki başka bir
// yüzeyin satırıyla eşleşmez olur.
//
// Terminal düşüşler (`''` / `'unknown'` / `'(default)'` / `'default'`)
// de pinlidir: her biri bir çağıranın BAĞLI OLDUĞU gerçek bir davranış.
// dbInstanceExpr'in 'unknown'ı MV'nin sakladığı literaldir; namespace'in
// '' terminali "kayıt üretme" demektir; msgClusterExpr'in '(default)'ı
// tek-cluster kurulumunda çekmece köprüsünün ta kendisidir.

// quotedRungChain — bir kimlik ifadesinin basamaklarını SIRAYLA doğrular.
//
// Üç şeyi birden ölçer, çünkü üçü ayrı arıza:
//   - eksik basamak  → o basamaktan adlandırılan kimlik hiçbir şeyle eşleşmez
//   - kaymış basamak → kimlik SESSİZCE yeniden adlandırılır
//   - fazla basamak  → zincir MV'den ayrışır (rung sayısı da sözleşme)
func quotedRungChain(t *testing.T, name, expr string, rungs []string, terminal string) {
	t.Helper()
	prev := -1
	for i, r := range rungs {
		at := strings.Index(expr, r)
		if at < 0 {
			t.Errorf("%s: %d. basamak DÜŞMÜŞ: %s\n"+
				"bu basamaktan adlandırılan her kimlik artık başka bir ada çözülür", name, i, r)
			continue
		}
		if at <= prev {
			t.Errorf("%s: basamak sırası bozulmuş (%s, %d. sırada olmalıydı)\n"+
				"coalesce İLK boş-olmayanı alır — sıra değişimi yeniden adlandırmadır", name, r, i)
		}
		prev = at
	}
	// Basamak SAYISI da sözleşme: araya sokulan yeni bir nullIf, yukarıdaki
	// sıra kontrolünden GEÇER (mevcut basamaklar hâlâ artan sırada) ama
	// zinciri MV'den ayırır. Bu yüzden ayrıca sayılıyor.
	if got, want := strings.Count(expr, "nullIf("), len(rungs); got != want {
		t.Errorf("%s: %d nullIf basamağı var, %d bekleniyordu — zincire basamak eklenmiş\n"+
			"veya çıkarılmış; MV ikizi de aynı şekilde değişmediyse iki yol ayrıştı", name, got, want)
	}
	if !strings.HasSuffix(strings.TrimSpace(expr), terminal+"\n)") &&
		!strings.HasSuffix(strings.TrimSpace(expr), terminal+")") &&
		!strings.Contains(expr, terminal+"\n") {
		t.Errorf("%s: terminal düşüş %s DEĞİL — terminal bir çağıranın bağlı olduğu\n"+
			"gerçek davranıştır, sessizce değiştirilemez: %q", name, terminal, expr)
	}
}

// TestNamespaceChainOrderPinned — Ç8'in ana pini.
//
// KANONİK SIRA: service.namespace ÖNCE (identity.go'daki üç gerekçe).
// res_keys basamaklarının TAMAMI attr_keys basamaklarından önce gelir —
// namespace bir SÜREÇ özelliğidir, tekil bir çağrının değil.
func TestNamespaceChainOrderPinned(t *testing.T) {
	expr := namespaceExpr()
	var rungs []string
	for _, k := range []string{
		"service.namespace", "k8s.namespace.name",
		"kubernetes.namespace.name", "kubernetes.namespace_name",
	} {
		rungs = append(rungs, "nullIf(res_values[indexOf(res_keys, '"+k+"')], '')")
	}
	for _, k := range []string{
		"service.namespace", "k8s.namespace.name",
		"kubernetes.namespace.name", "kubernetes.namespace_name",
	} {
		rungs = append(rungs, "nullIf(attr_values[indexOf(attr_keys, '"+k+"')], '')")
	}
	quotedRungChain(t, "namespaceExpr", expr, rungs, "''")

	// Sentinel YOKLUĞU da sözleşme: 'unknown' gibi bir terminal, namespace
	// yayınlamayan HER servisi sahte bir kutuda toplardı ve iki çağıran da
	// (GetServiceNamespaces / marginalModes) boşu "kayıt üretme" diye
	// okuyor.
	for _, forbidden := range []string{"'unknown'", "'(default)'", "'default'"} {
		if strings.Contains(expr, forbidden) {
			t.Errorf("namespaceExpr %s sentineline düşüyor — namespace'siz servisler\n"+
				"sahte bir kutuda toplanır; terminal '' OLMALI", forbidden)
		}
	}
}

// TestNamespaceGuardMirrorsChain — guard ile ifade AYNI sözlükten.
//
// Ayrı yazılırlarsa arıza sinsi: ifade bir anahtarı okur ama WHERE o
// anahtarı taşıyan satırları eler, sonuç sessizce EKSİK namespace olur.
func TestNamespaceGuardMirrorsChain(t *testing.T) {
	guard := namespaceHasGuard()
	prev := -1
	for _, scope := range []string{"res_keys", "attr_keys"} {
		for _, k := range nsIdentityKeys {
			frag := "has(" + scope + ", '" + k + "')"
			at := strings.Index(guard, frag)
			if at < 0 {
				t.Errorf("guard %s ön-elemesini kaybetmiş — ifade bu anahtarı okuyor\n"+
					"ama guard onu taşıyan satırları ELİYOR", frag)
				continue
			}
			if at <= prev {
				t.Errorf("guard sırası ifadenin sırasından ayrışmış: %s", frag)
			}
			prev = at
		}
	}
	if got, want := strings.Count(guard, "has("), len(nsIdentityKeys)*2; got != want {
		t.Errorf("guard %d has() taşıyor, %d bekleniyordu — guard ile zincir ayrıştı", got, want)
	}
}

// TestNamespaceSingleSource — Ç8'in ASIL sözleşmesi: iki çağıran da
// paylaşılan üreticiden besleniyor, elle yazılmış ikizi YOK.
func TestNamespaceSingleSource(t *testing.T) {
	for _, c := range []struct{ name, sql string }{
		{"serviceNamespacesSQL", serviceNamespacesSQL},
		{"deriveMetadataAllSQL", deriveMetadataAllSQL},
	} {
		if !strings.Contains(c.sql, namespaceExpr()) {
			t.Errorf("%s namespaceExpr() ÇIKTISINI gömmüyor — kendi sözlüğünü yazıyor demektir;\n"+
				"v0.9.1318 öncesi tam olarak bu yüzden iki yüzey iki farklı namespace gösteriyordu", c.name)
		}
		if !strings.Contains(c.sql, namespaceHasGuard()) {
			t.Errorf("%s namespaceHasGuard() çıktısını gömmüyor", c.name)
		}
	}
}

// TestNoHandWrittenNamespaceChain — İKİZ-YAZIM KAPISI.
//
// Bir kapı yalnız TEK yazımı ararsa, ikinci yazım muaf kalır (bu deponun
// tekrar eden ders sınıfı). Bu yüzden burada aranan şey bir isim değil,
// ANAHTARIN KENDİSİ: chstore kaynağında elle yazılmış bir
// indexOf(..., '<namespace anahtarı>') kalmışsa, o bir üçüncü sözlüktür.
//
// Muafiyet listesi BİLİNÇLİ ve gerekçeli — namespace anahtarlarını
// namespace ÇÖZMEK için değil, başka bir soruyu cevaplamak için okuyan
// yerler.
func TestNoHandWrittenNamespaceChain(t *testing.T) {
	// topoEnvChainSQL namespace anahtarlarını ENV yedeği olarak okuyor
	// (deployment.environment yoksa namespace'i env sayan v0.5.410
	// kararı). Farklı SORU, dolayısıyla farklı zincir — ve o zincirin
	// başında deploy_env + deployment.environment.* basamakları var.
	allowed := map[string]bool{"topology.go": true}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("paket dizini okunamadı: %v", err)
	}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") || n == "identity.go" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(".", n))
		if err != nil {
			t.Fatalf("%s okunamadı: %v", n, err)
		}
		src := string(b)
		for _, k := range nsIdentityKeys {
			for _, scope := range []string{"res_keys", "attr_keys"} {
				frag := "indexOf(" + scope + ", '" + k + "')"
				if strings.Contains(src, frag) && !allowed[n] {
					t.Errorf("%s elle yazılmış namespace basamağı taşıyor: %s\n"+
						"identity.go'nun namespaceExpr()'ini kullan — üçüncü bir sözlük,\n"+
						"v0.9.1318'in kapattığı iki-sözlük arızasını geri getirir", n, frag)
				}
			}
		}
	}
}

// TestMsgClusterExprOrderPinned — Ç9'un zincir yarısı.
func TestMsgClusterExprOrderPinned(t *testing.T) {
	quotedRungChain(t, "msgClusterExpr", msgClusterExpr, []string{
		"nullIf(attr_values[indexOf(attr_keys, 'server.address')], '')",
		"nullIf(attr_values[indexOf(attr_keys, 'messaging.kafka.bootstrap.servers')], '')",
		"nullIf(attr_values[indexOf(attr_keys, 'messaging.kafka.cluster.name')], '')",
	}, "'(default)'")

	// peer_service BİLİNÇLİ dışarıda: o kolon destination'ın da son çare
	// kaynağı; ikisi için birden kullanmak "cluster'ı bilmiyorum" ile
	// "hedefi bilmiyorum"u tek kovada toplardı.
	if strings.Contains(msgClusterExpr, "peer_service") {
		t.Error("msgClusterExpr peer_service'e düşmüş — cluster ile destination\n" +
			"bilinmezliği tek kovada toplanır (sabitin kuruluş gerekçesi)")
	}
}

// TestClusterExprShadowingResolved — Ç9'un ASIL derdi.
//
// Paket sabiti `clusterExpr` ile `func (s *Store) clusterExpr()` aynı
// pakette yaşıyordu. Go buna izin verir (biri sabit, öteki metot), yani
// DERLEYİCİ UYARMAZ: yeni sorgu yazıp `clusterExpr` diye yazan biri k8s
// cluster'ı istediğini sanarken messaging zincirini alıyordu.
//
// Bu test adın geri gelmesini engelliyor. Metodun adı KORUNUYOR — çağrı
// sayısı çok daha fazla ve operatöre görünen "cluster" kavramı odur.
func TestClusterExprShadowingResolved(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("paket dizini okunamadı: %v", err)
	}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(".", n))
		if err != nil {
			t.Fatalf("%s okunamadı: %v", n, err)
		}
		for _, decl := range []string{"const clusterExpr", "var clusterExpr"} {
			if strings.Contains(string(b), decl) {
				t.Errorf("%s: %q geri gelmiş — bu ad *Store.clusterExpr() metoduyla\n"+
					"GÖLGELEŞİR ve Go bunu uyarmaz; messaging zinciri için msgClusterExpr kullan", n, decl)
			}
		}
	}
	// Metot YAŞAMALI: k8s/infra cluster'ının tek çözüm yeri o.
	b, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatalf("repo.go okunamadı: %v", err)
	}
	if !strings.Contains(string(b), "func (s *Store) clusterExpr()") {
		t.Error("repo.go'daki *Store.clusterExpr() metodu kaybolmuş — k8s cluster\n" +
			"çözümü ile hasClusterCol probu ona bağlı (v0.8.162)")
	}
}

// TestDbInstanceExprOrderPinned — Ç2/Ç10'un zincir pini.
//
// quantile_ordinal_test.go zinciri store.go'daki MV DDL'ine karşı
// doğruluyor; buradaki pin SIRAYI ve basamak SAYISINI sabitin KENDİ
// üzerinde ölçüyor. İkisi farklı mutasyonları yakalar: MV ile sabit
// BİRLİKTE yeniden sıralanırsa oradaki test geçer, buradaki kırılır.
func TestDbInstanceExprOrderPinned(t *testing.T) {
	quotedRungChain(t, "dbInstanceExpr", dbInstanceExpr, []string{
		"nullIf(peer_service, '')",
		"nullIf(attr_values[indexOf(attr_keys, 'server.address')], '')",
		"nullIf(attr_values[indexOf(attr_keys, 'net.peer.name')], '')",
		"nullIf(attr_values[indexOf(attr_keys, 'db.host')], '')",
		"nullIf(attr_values[indexOf(attr_keys, 'db.name')], '')",
		"nullIf(service_name, '')",
	}, "'unknown'")

	quotedRungChain(t, "dbNameExpr", dbNameExpr, []string{
		"nullIf(attr_values[indexOf(attr_keys, 'db.name')], '')",
	}, "'default'")
}

// TestDbNodeNamingBoundToSharedIdentity — Ç10'un İKİZ-YAZIM KAPISI.
//
// topology.go'da `db:` düğüm adı ÜÇ yerde kuruluyor. Bir kapı yalnız
// birini ölçseydi, öteki iki yazım muaf kalırdı. Bu yüzden test SİTE
// SAYISINI da pinliyor: dördüncü bir `concat('db:'` belirirse kırılır ve
// yazarı sınıflandırmak zorunda kalır.
func TestDbNodeNamingBoundToSharedIdentity(t *testing.T) {
	b, err := os.ReadFile("topology.go")
	if err != nil {
		t.Fatalf("topology.go okunamadı: %v", err)
	}
	src := string(b)

	const sites = 3
	if got := strings.Count(src, "concat('db:'"); got != sites {
		t.Fatalf("topology.go %d yerde db: düğümü kuruyor, %d bekleniyordu.\n"+
			"YENİ bir site eklendiyse: MV yazıcısı mı (dbInstanceExpr ŞART, yoksa\n"+
			"düğümden /database'e link kurulamaz) yoksa ad-hoc okuma yolu mu\n"+
			"(düz biçim kabul) — sınıflandır ve bu testi güncelle", got, sites)
	}

	// (1) MV YAZICISI — topology_edges_5m'e giden tek site. Bu düğüm adı
	// FocusedNeighborhood'un splitDbNodeName'ine yem oluyor ve oradan
	// /database'e (db_summary_5m satırına) link kuruluyor. Instance
	// taşımayan bir ad = null href = çıkmaz düğüm (v0.9.1318 ölçümü).
	if !strings.Contains(src, "concat('db:',    db_system, '@', db_instance)") {
		t.Error("MV yazıcısının db düğümü db_instance taşımıyor — düz db:<system>\n" +
			"adı splitDbNodeName'de null döner ve düğüm ÇIKMAZ olur")
	}
	// KAYNAK METNİ aranıyor, genişlemiş DEĞER değil: aranan şey tam olarak
	// sabitin İTHAL EDİLDİĞİ — zincirin gövdesi oraya kopyalanmış olsaydı
	// (yani dördüncü bir elle-yazım doğsaydı) bu kapı ısırır.
	if !strings.Contains(src, "if(db_system != '', "+"`+dbInstanceExpr+`"+", '') AS db_instance") {
		t.Error("db_instance alias'ı paylaşılan dbInstanceExpr'den GELMİYOR —\n" +
			"üç basamaklı infra_host'a geri dönmüş olabilir; o zincir\n" +
			"db_summary_5m'in altı basamağıyla ayrışır (ölçüldü: clickhouse\n" +
			"düğümü '' vs coremetry-monolithic)")
	}
	// infra_host db dalında ARTIK kullanılmamalı; queue/external'de kalıyor.
	if strings.Contains(src, "concat('db:',    db_system, '@', infra_host)") {
		t.Error("db düğümü hâlâ infra_host'tan adlandırılıyor — Ç10 geri gelmiş")
	}

	// (2)+(3) AD-HOC OKUMA YOLLARI — GetFlowTopology ve GetTopologyEdges.
	// Bunlar düz `db:<system>` üretiyor ve BİLİNÇLİ öyle bırakıldı:
	// doğrulandı ki çıktıları (TopologyResponse.childNode) frontend'de
	// hiçbir detay linkine yem olmuyor — splitDbNodeName'in tek tüketicisi
	// FocusedNeighborhood ve o /api/servicegraph okuyor, yani (1)'i.
	// Bu muafiyet linke yem OLMADIKLARI sürece geçerli; olurlarsa aynı
	// çıkmaz-link sınıfı orada da doğar.
	if got := strings.Count(src, "concat('db:',    db_system),"); got != 2 {
		t.Errorf("düz db: siteleri %d, 2 bekleniyordu — muafiyetin dayanağı\n"+
			"bu iki yolun link kurmuyor olması; sayı değiştiyse gerekçeyi\n"+
			"yeniden doğrula", got)
	}
}

// TestEdgeInstancesDbUsesSharedIdentity — Ç10'un dördüncü yazımı.
//
// "Şu db düğümünün instance'ları" paneli, açıldığı düğümle AYNI kimliği
// çözmeli. Önceden tek basamaklı peer_service zinciriyle çözüyordu:
// peer_service'i boş bırakan kurulumda panel tek bir 'unknown' kovası
// gösterirken MV o instance'ı adıyla biliyordu.
func TestEdgeInstancesDbUsesSharedIdentity(t *testing.T) {
	b, err := os.ReadFile("topology.go")
	if err != nil {
		t.Fatalf("topology.go okunamadı: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, `sysCol, instanceExpr = "db_system", dbInstanceExpr`) {
		t.Error("GetEdgeInstances'ın db dalı paylaşılan dbInstanceExpr'i kullanmıyor —\n" +
			"panel, açıldığı düğümden BAŞKA bir instance kümesi gösterir")
	}
	// queue dalı BİLİNÇLİ ayrı: kuyruk kenarının instance'ı broker'dır,
	// dbInstanceExpr'in db.host/db.name basamakları orada anlamsız.
	if !strings.Contains(src, `sysCol, instanceExpr = "msg_system", `+"`coalesce(nullIf(peer_service, ''), 'unknown')`") {
		t.Error("queue dalı beklenen broker zincirini kullanmıyor — dbInstanceExpr'e\n" +
			"kaydırılmış olabilir; o zincir kuyruk için YANLIŞ kimlik üretir")
	}
}

// ─── dbEntityID parite sözleşmesi (v0.9.1361, F3.1) ─────────────────
//
// Emsal db_stmt_hash (dbstmt.go:23-44 "THE PARITY CONTRACT") — ama ders
// HASH DEĞİL: aynı normalizasyonun İKİ motorda bayt-bayt aynı çıktıyı
// vermesi ve bunun CANLI veriden yakalanmış vektörlerle pinlenmesi.
// Burada iki "motor" iki KİMLİK HATTIDIR: HAT A portu adresin içinde
// taşır (server.address = corebank-scan.prod:1521), HAT B ayrı taşır
// ya da hiç taşımaz. Fonksiyonun sözü: aynı fiziksel adres hangi hattan
// gelirse gelsin AYNI kimliğe iner.
//
// Vektörlerin KAYNAĞI her satırda yazılı. Prod ölçümünden (2026-08-24,
// 1s pencere) gelen gerçek şekiller ile testin ihtiyacı için üretilmiş
// ŞEKİLLER ayrı işaretli — ölçülmemiş bir değeri ölçülmüş gibi
// göstermek bu dosyanın kapatmaya çalıştığı arıza sınıfının kendisi.

func TestDBEntityIDVectors(t *testing.T) {
	cases := []struct {
		name               string
		system, host, port string
		want               string
	}{
		// ── ÖLÇÜLMÜŞ ŞEKİLLER (prod oracle: 78 ayrı server.address,
		//    çözünürlük 0,99996; adres portu İÇİNDE taşıyor) ──
		{"HAT A oracle, port adrese yapışık", "oracle", "corebank-scan.prod:1521", "",
			"db:oracle@corebank-scan.prod:1521"},
		{"HAT A ikinci fiziksel oracle", "oracle", "corebank-dg.prod:1521", "",
			"db:oracle@corebank-dg.prod:1521"},
		// HAT B receiver instance'ı PORTSUZ gelir (identity.go'daki
		// 2026-08-24 lokal ölçümü: corebank-scan.prod / corebank-dg.prod).
		{"HAT B oracle, port ayrı", "oracle", "corebank-scan.prod", "1521",
			"db:oracle@corebank-scan.prod:1521"},
		{"HAT B oracle, port hiç yok", "oracle", "corebank-scan.prod", "",
			"db:oracle@corebank-scan.prod"},
		// capacityCheck.dbsys BÜYÜK harf yazıyor; adres de büyük gelebilir.
		{"büyük harf system + adres", "ORACLE", "COREBANK-SCAN.PROD:1521", "",
			"db:oracle@corebank-scan.prod:1521"},
		// §3.6'nın ilacı: POSTGRES ile postgresql tek kimliğe iner.
		{"postgres alias + boşluk", "  PostGres  ", " pg-01.prod ", " 5432 ",
			"db:postgresql@pg-01.prod:5432"},
		// FQDN KISALTILMAZ: ayırt edici bilgi son ekte ve önekte.
		{"db2 uzun FQDN kısaltılmaz", "db2", "db2gw-04.corebank.prod", "50000",
			"db:db2@db2gw-04.corebank.prod:50000"},
		// couchbase ÖLÇÜLDÜ: server.address %0, net.peer.name %48,36 —
		// yani 2. basamak o motor için TEK kaynak. Ad bir ŞEKİL, ölçülen
		// değer değil; ölçülen olgu basamağın yük taşıdığıdır.
		{"couchbase 2. basamaktan (ŞEKİL)", "couchbase", "cbnode-1.prod", "",
			"db:couchbase@cbnode-1.prod"},
		// clickhouse ÖLÇÜLDÜ: üç basamak da %0 — Coremetry'nin KENDİ
		// self-telemetrisi. Çözülemeyen dal NORMAL bir yol, kenar vaka
		// değil: boş ID → çağıran bugünkü ham değeri bayt-bayt yazar.
		{"clickhouse çözülemez (self-telemetri)", "clickhouse", "", "", ""},

		// ── ŞEKİLLER: bölme kuralının bozmaması gerekenler ──
		// Çıplak IPv6: son ':'ten sonrası "1" ve TAMAMEN RAKAM, yani
		// birinci kapı GEÇİLİR. İkinci kapı olmasa adres 2001:db8: + port
		// 1 diye bölünürdü — "son ':'ten böl"ün klasik off-by-one'ı.
		{"IPv6 çıplak, bölünmez", "postgresql", "2001:db8::1", "",
			"db:postgresql@2001:db8::1"},
		{"IPv6 kısa çıplak, bölünmez", "redis", "::1", "", "db:redis@::1"},
		{"IPv6 parantezli portsuz", "redis", "[::1]", "", "db:redis@::1"},
		{"IPv6 parantezli + port", "postgresql", "[2001:db8::1]:5432", "",
			"db:postgresql@[2001:db8::1]:5432"},
		{"IPv6 + ayrı port", "postgresql", "2001:db8::1", "5432",
			"db:postgresql@[2001:db8::1]:5432"},
		// Bu satır MUTASYONLA kazanıldı: "son ':'ten böl" yerine "İLK
		// ':'ten böl" yazan bir sürüm yukarıdaki IPv6 vektörlerinin
		// HİÇBİRİNDE ısırmıyor, çünkü parantezli-portlu biçim bölünmeyen
		// sürümün SABİT NOKTASI (bölünmeyince host aynen geri yazılıyor).
		// Ayrışma ancak ÇATIŞAN bir port argümanıyla görünür oluyor:
		// yanlış sürüm gömülü portu göremediği için argümanı kullanır ve
		// host'u İKİNCİ KEZ parantezler.
		{"IPv6 parantezli + port, argüman çatışır", "postgresql", "[::1]:5432", "6432",
			"db:postgresql@[::1]:5432"},
		{"çıplak hostname, ':' yok", "mssql", "sqlnode.prod", "",
			"db:mssql@sqlnode.prod"},
		// Sondaki ':' port DEĞİLDİR (boş dizge "hepsi rakam" sayılamaz) —
		// ve host bayt-bayt korunur, kırpılmaz.
		{"sondaki ':' port değil", "mssql", "sqlnode.prod:", "",
			"db:mssql@sqlnode.prod:"},
		{"port rakam değil, düşer", "oracle", "h.prod", "abc", "db:oracle@h.prod"},
		// Gömülü port ayrı argümanı YENER: host ile gömülü portu TEK bir
		// instrumentasyon çağrısı yazdı.
		{"gömülü port argümanı yener", "oracle", "h.prod:1521", "1522",
			"db:oracle@h.prod:1521"},

		// ── kodlanamayan demetler → boş ID ──
		{"host yok, yalnız port", "oracle", ":1521", "", ""},
		{"boş system", "", "corebank-scan.prod", "", ""},
		{"boş host", "oracle", "", "1521", ""},
		{"yalnız boşluk", "   ", "   ", "  ", ""},
		{"boş parantez", "oracle", "[]", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dbEntityID(tc.system, tc.host, tc.port)
			if got != tc.want {
				t.Fatalf("dbEntityID(%q,%q,%q) = %q, %q bekleniyordu",
					tc.system, tc.host, tc.port, got, tc.want)
			}
		})
	}
}

// TestDBEntityIDHatParity — SÖZLEŞMENİN KENDİSİ.
//
// Aynı fiziksel adres HAT A'dan (port adrese yapışık) ve HAT B'den
// (port ayrı) geldiğinde AYNI kimliğe inmeli. Bu eşitlik bozulursa
// fonksiyon bir kimlik değil, iki kimlik üretiyor demektir ve F3.2/F3.3
// üzerine bir köprü kuramaz.
func TestDBEntityIDHatParity(t *testing.T) {
	pairs := []struct {
		name             string
		aSys, aHost, aPt string
		bSys, bHost, bPt string
	}{
		{"oracle: yapışık vs ayrı",
			"oracle", "corebank-scan.prod:1521", "",
			"oracle", "corebank-scan.prod", "1521"},
		{"oracle: yazım + hat farkı birlikte",
			"ORACLE", " corebank-scan.prod:1521 ", "",
			"oracle", "COREBANK-SCAN.PROD", "1521"},
		{"postgres: alias + hat farkı birlikte",
			"POSTGRES", "pg-01.prod:5432", "",
			"postgresql", "pg-01.prod", "5432"},
		{"IPv6: parantezli-yapışık vs çıplak-ayrı",
			"postgresql", "[2001:db8::1]:5432", "",
			"postgresql", "2001:db8::1", "5432"},
	}
	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			a := dbEntityID(p.aSys, p.aHost, p.aPt)
			b := dbEntityID(p.bSys, p.bHost, p.bPt)
			if a == "" {
				t.Fatalf("HAT A tarafı boş ID verdi — vektör yanlış kurulmuş")
			}
			if a != b {
				t.Fatalf("parite BOZUK:\n  HAT A (%q,%q,%q) = %q\n  HAT B (%q,%q,%q) = %q",
					p.aSys, p.aHost, p.aPt, a, p.bSys, p.bHost, p.bPt, b)
			}
		})
	}
}

// TestDBEntityIDPortFreeFormBridgesHats — AÇIK KALAN SORUYU çivileyen
// test.
//
// HAT B receiver instance'ı bugün PORTSUZ (ölçülü: corebank-scan.prod).
// HAT A adresi portu İÇİNDE taşıyor. Yani port kimliğe girdiği sürece
// iki hat AYNI veritabanı için AYRI kimlik üretir — bu dilim bunu
// çözmüyor ve çözüyormuş gibi de yapmıyor.
//
// Köprünün ilkel taşı: adresi splitDBAddress'ten geçir, host yarısını
// port'suz kodla. port'a "" GEÇMEK YETMEZ — yapışık port yine kazanır,
// ve bu tam olarak "yanlış görünmeyen" türden bir hatadır.
func TestDBEntityIDPortFreeFormBridgesHats(t *testing.T) {
	const hatA = "corebank-scan.prod:1521" // server.address (HAT A)
	const hatB = "corebank-scan.prod"      // receiver instance (HAT B)

	withPort := dbEntityID("oracle", hatA, "")
	portless := dbEntityID("oracle", hatB, "")
	if withPort == portless {
		t.Fatalf("port kimliğe GİRMİYOR — aynı host'taki iki dinleyici tek\n" +
			"kimliğe çökerdi (§3.1'in günahı). Karar bilinçliydi: port girer")
	}
	if got := dbEntityID("oracle", hatA, ""); got != "db:oracle@corebank-scan.prod:1521" {
		t.Fatalf("HAT A kimliği %q", got)
	}

	// port'a "" geçmek port'u DÜŞÜRMEZ:
	if dbEntityID("oracle", hatA, "") == portless {
		t.Fatal("beklenmedik")
	}
	// Köprü biçimi: önce böl, sonra host yarısını kodla.
	h, p := splitDBAddress(hatA)
	if p != "1521" {
		t.Fatalf("splitDBAddress portu ayıramadı: %q", p)
	}
	if bridged := dbEntityID("oracle", h, ""); bridged != portless {
		t.Fatalf("port-suz biçim HAT B ile buluşmuyor: %q vs %q", bridged, portless)
	}
}

// TestDBEntityIDIsIdempotent — çıktının instance yarısı fonksiyona geri
// verildiğinde AYNI kimlik çıkmalı.
//
// Neden ŞART: kimlik bir kolona/URL'e yazılıp geri okunacak. IPv6
// host'u port varken parantezlemezsek çıktı (::1:5432) kendi bölme
// kuralına yem olamaz ve ikinci turda BAŞKA bir kimliğe düşer. Ayrıca
// biçimin ParseDBSubjectID ile uyumlu kaldığını da ölçer.
func TestDBEntityIDIsIdempotent(t *testing.T) {
	for _, addr := range []string{
		"corebank-scan.prod:1521", "corebank-dg.prod", "[2001:db8::1]:5432",
		"2001:db8::1", "::1", "[::1]", "sqlnode.prod:", "db2gw-04.corebank.prod",
	} {
		t.Run(addr, func(t *testing.T) {
			id := dbEntityID("oracle", addr, "")
			if id == "" {
				t.Fatalf("vektör boş ID verdi: %q", addr)
			}
			sys, inst, ok := ParseDBSubjectID(id)
			if !ok {
				t.Fatalf("%q ParseDBSubjectID ile çözülemedi — biçim db özne\n"+
					"biçiminden ayrışmış", id)
			}
			if again := dbEntityID(sys, inst, ""); again != id {
				t.Fatalf("idempotent DEĞİL: %q → %q", id, again)
			}
		})
	}
}

// TestDBEntityHostChainPinned — ÖLÇÜMÜN kendisi test hâlinde.
//
// peer_service zincirde YOK: prod'da oracle için 78 ayrı server.address
// varken YALNIZ 4 ayrı peer_service ölçüldü (2026-08-24). peer_service'i
// zincire koymak 78 fiziksel örneği 4 ada çökertirdi.
//
// Sıra da sözleşme: 1. basamak neredeyse her şeyi çözüyor, 2. basamak
// couchbase için TEK kaynak (%48,36), 3. basamak sekiz motorda da %0.
func TestDBEntityHostChainPinned(t *testing.T) {
	want := []string{"server.address", "net.peer.name", "db.host"}
	if len(dbEntityHostKeys) != len(want) {
		t.Fatalf("zincir %d basamak, %d bekleniyordu: %v",
			len(dbEntityHostKeys), len(want), dbEntityHostKeys)
	}
	for i, k := range want {
		if dbEntityHostKeys[i] != k {
			t.Errorf("%d. basamak %q, %q bekleniyordu — sıra kaydırmak kimliği\n"+
				"SESSİZCE yeniden adlandırır", i, dbEntityHostKeys[i], k)
		}
	}
	for _, k := range dbEntityHostKeys {
		if strings.Contains(k, "peer") && k != "net.peer.name" {
			t.Errorf("zincire %q girmiş — peer_service ÖLÇÜLEREK dışarıda\n"+
				"bırakıldı (78 adres → 4 ad)", k)
		}
		if k == "peer_service" {
			t.Error("peer_service zincire girmiş")
		}
	}
}

// TestDBEngineAliasMapIsMeasuredOnly — alias haritası ÖLÇÜLMÜŞ
// ayrışmaları taşır, spekülatif eşleme taşımaz.
//
// postgres→postgresql VAR: db_capacity.go POSTGRES yazıyor, span tarafı
// postgresql; §3.6'nın ölçülmüş kusuru bu.
//
// mariadb→mysql YOK: `mariadb` backend Go kaynağında hiç geçmiyor,
// prod ölçümünde mysql/mariadb span'i yok, ve frontend mariadb'yi AYRI
// bir motor sayıyor. Kanıtsız eşleme iki motoru tek kimlik uzayında
// birleştirirdi.
func TestDBEngineAliasMapIsMeasuredOnly(t *testing.T) {
	if got := normalizeDBSystem("  POSTGRES "); got != "postgresql" {
		t.Errorf("normalizeDBSystem(POSTGRES) = %q, postgresql bekleniyordu —\n"+
			"§3.6'nın kusuru geri gelmiş", got)
	}
	if got := normalizeDBSystem("postgresql"); got != "postgresql" {
		t.Errorf("kanonik yazım değişmiş: %q", got)
	}
	if got := normalizeDBSystem("ORACLE"); got != "oracle" {
		t.Errorf("ToLower yeterken haritaya girmiş olabilir: %q", got)
	}
	if _, ok := dbEngineAliases["mariadb"]; ok {
		t.Error("mariadb alias'ı eklenmiş — backend kaynağında `mariadb` YOK,\n" +
			"prod ölçümünde mysql/mariadb span'i YOK ve semconv ikisini AYRI\n" +
			"db.system değeri sayıyor. Kanıt geldiyse bu testi ölçümle güncelle")
	}
	if got := normalizeDBSystem("MariaDB"); got != "mariadb" {
		t.Errorf("mariadb %q'ye eşlenmiş — iki motor tek kimliğe çöker", got)
	}

	// §3.6'nın kusuru DBSubjectID'de HÂLÂ AÇIK ve bu bilinçli: onu
	// aliaslamak canlı problems.service satırlarını yeniden adlandırır
	// (F1.1'in işi, ayrı dilim). Fark burada GÖRÜNÜR duruyor ki F1.1
	// kapatırken bu testi bilerek güncellemek zorunda kalsın.
	if DBSubjectID("POSTGRES", "pg-01.prod") != "db:postgres@pg-01.prod" {
		t.Error("DBSubjectID alias haritasına bağlanmış — bu F1.1'dir ve AÇIK\n" +
			"problemlerin service alanını yeniden adlandırır; ayrı dilim olarak\n" +
			"ölçülmeli (db_capacity.go:448-450 bu sınıfı biliyor)")
	}
	if dbEntityID("POSTGRES", "pg-01.prod", "") != "db:postgresql@pg-01.prod" {
		t.Error("dbEntityID alias haritasını KULLANMIYOR")
	}
}

// dbEntityIDCodeRefs — bir Go kaynağındaki dbEntityID/DBEntityID KOD
// referanslarının sayısı. Yorum satırları elenir (bu dosya ve identity.go
// fonksiyonu yorumda ONLARCA kez anıyor) ve arama BÜYÜK-KÜÇÜK HARF
// DUYARSIZ: tek yazım arayan bir kapı, ikizini muaf tutar — bu deponun
// tekrar eden ders sınıfı.
func dbEntityIDCodeRefs(src string) int {
	n := 0
	for _, line := range strings.Split(src, "\n") {
		code := line
		if i := strings.Index(code, "//"); i >= 0 {
			code = code[:i]
		}
		n += strings.Count(strings.ToLower(code), "dbentityid")
	}
	return n
}

// TestDBEntityIDIsWiredNowhere — F3.1'in KENDİ sözleşmesi.
//
// Plan birebir: "Fonksiyon yazılır, testlenir, hiçbir okuma yoluna
// bağlanmaz." Bağlama işi F3.2/F3.3'tür ve biri KARIŞIK-KİMLİK göç
// penceresi taşır (§3.9: 90 günlük MV boyunca aynı grafikte iki kimlik,
// topolojide 14 gün iki düğüm, kayıtlı görünümler sessizce boşalır).
// Yani bu kapı bir üslup tercihi değil, patlama yarıçapı kapısı.
//
// F3.2 geldiğinde bu test KIRILIR — doğru davranış budur: yazarı
// testi bilerek silmek/dönüştürmek zorunda kalır ve §3.9'un
// "kaydedilmiş görünümler ne olacak" sorusunu cevaplamadan geçemez.
func TestDBEntityIDIsWiredNowhere(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("paket dizini okunamadı: %v", err)
	}
	seen := false
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(".", n))
		if err != nil {
			t.Fatalf("%s okunamadı: %v", n, err)
		}
		refs := dbEntityIDCodeRefs(string(b))
		if n == "identity.go" {
			seen = true
			if refs != 1 {
				t.Errorf("identity.go'da %d kod referansı var, 1 bekleniyordu\n"+
					"(yalnız func bildirimi). Fazlası = fonksiyon bir okuma yoluna\n"+
					"bağlanmış demektir; F3.1 bağlamaz", refs)
			}
			continue
		}
		if refs != 0 {
			t.Errorf("%s dbEntityID'yi çağırıyor (%d kez) — F3.1 hiçbir okuma\n"+
				"yoluna bağlanmaz. Bu F3.2/F3.3 ise testi bilerek güncelle", n, refs)
		}
	}
	if !seen {
		t.Fatal("identity.go taranmadı — kapı kör kalmış olabilir")
	}
}

// TestDBEntityIDCodeRefsDetects — YUKARIDAKİ KAPININ NEGATİF KONTROLÜ.
//
// Kapının kendisi ölçülmezse "0 bulundu" ile "hiç aramadım" ayırt
// edilemez. Bu deponun ölçülmüş dersi: bir kapı yalnız VARLIĞI değil
// YOKLUĞU da kanıtlamalı.
func TestDBEntityIDCodeRefsDetects(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want int
	}{
		{"düz çağrı", "\tid := dbEntityID(sys, host, port)\n", 1},
		{"dışa açık ikiz yazım", "\tid := DBEntityID(sys, host, port)\n", 1},
		{"fonksiyon değeri olarak", "\tf := dbEntityID\n", 1},
		{"yorumda anılıyor", "// dbEntityID bir gün bağlanacak\n", 0},
		{"satır sonu yorumu", "\tx := 1 // dbEntityID\n", 0},
		{"iki çağrı tek satırda", "a, b := dbEntityID(x), dbEntityID(y)\n", 2},
		{"alakasız", "\tk := dbEntityHostKeys[0]\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dbEntityIDCodeRefs(tc.src); got != tc.want {
				t.Fatalf("dbEntityIDCodeRefs(%q) = %d, %d bekleniyordu", tc.src, got, tc.want)
			}
		})
	}
}
