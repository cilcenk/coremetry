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
