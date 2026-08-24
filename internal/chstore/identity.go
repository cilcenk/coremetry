package chstore

import "strings"

// identity.go — PAYLAŞILAN KİMLİK İFADELERİ (v0.9.1318, entity-model A3).
//
// Bu dosyanın tek işi var: aynı fiziksel şeyi adlandıran SQL zincirleri
// TEK yerde yaşasın. Kimlik ifadesi çoğaldığında derleyici susar, testler
// yeşil kalır ve arıza yalnız ÜRÜNDE görünür — hep aynı şekilde: bir
// yüzey satırı gösterir, ötekinin o satıra kurduğu link HİÇBİR ŞEYLE
// eşleşmez. Hata değil, CEVAPSIZLIK (v0.9.973'ün teşhis ettiği sınıf).
//
// Ölçülmüş emsaller — yeniden yazmadan önce oku:
//
//   - v0.9.274: /databases çekmecesinin ham taraması `peer_service = ?`
//     ile daralıyordu, yani altı basamağın BİRİ ile. MV'nin altıncı
//     basamaktan adlandırdığı `clickhouse` satırı için o yüklem 0 span
//     eşledi, gerçek kimlik 4659 eşledi. Çekmece MV'den gelen span
//     sayısının yanında BOŞ bir Top-statements tablosu gösterdi.
//   - v0.9.821: satır kimliği (system, instance, db_name) üçlüsüyken
//     çekmece yalnız (system, instance) soruyordu; aynı host'taki her
//     veritabanı AYNI çekmeceyi açtı.
//   - v0.9.1318 (bu dosya): topoloji MV'si db düğümünü ÜÇ basamaklı bir
//     zincirle adlandırıyordu, db_summary_5m ise ALTI ile. Lokal ölçüm:
//     `clickhouse` için üç-basamaklı zincir BOŞ verdi (düğüm düz
//     `db:clickhouse` oldu), altı-basamaklı zincir `coremetry-monolithic`
//     verdi. splitDbNodeName '@' göremediği için null döndü — düğümden
//     /database'e link KURULAMADI.
//
// Kural: bir kimlik zincirini ikinci kez YAZMA, buradan İTHAL ET.
// Yeni bir tüketici zinciri farklı yazarsa identity_test.go kırılır.

// ─── Topoloji düğüm KİMLİĞİ (v0.9.1327) ─────────────────────────────
//
// topology_edges_5m'in `parent_service` / `child_node` kolonları İKİ
// AYRI şey taşıyabilir: düz bir servis adı, ya da agregatörün yazdığı
// önekli bir altyapı düğümü (`db:`, `queue:`, `ext:` — topology.go'nun
// üç pass'i). Önek TEK otoriter kaynaktır.
//
// `node_kind` bu soruyu CEVAPLAMAZ ve cevaplarmış gibi okumak bu
// repoda iki kez bug üretti. Kolon üç pass'te de ÇOCUK düğümün
// kind'ıdır:
//
//	cross-service pass  child = servis            → 'service'
//	infra pass          child = db/queue/external → kind_out
//	queue→consumer pass child = tüketici servis   → 'service'  (DOĞRU)
//
// Üçüncü satır kritik: o kenarın EBEVEYNİ bir kuyruk düğümü
// (`queue:<sys>:<topic>`) ama satırın node_kind'ı 'service' — çünkü
// kolon çocuğu tarif ediyor. Yani `WHERE node_kind='service'` yazan
// bir okuma, ebeveyni HİÇ kısıtlamaz ve kuyruk ID'lerini servis
// sanarak içeri alır. v0.9.1028 aynı yanılgıyı grafik tarafında
// düzeltti (servicegraph.go nodeKindFromID gerekçesi); bu blok o
// kararı verinin en alt katmanına taşıyor ki her okuyucu aynı kuralı
// paylaşsın.
//
// TEK TABLO: bu liste v0.9.1029'da internal/api'de yaşıyordu ve
// agregatörün yazdığı önekleri ORADAN aynalıyordu. Yazıcı bu paket
// olduğuna göre sözlüğün evi de burası.
const (
	NodeKindService  = "service"
	NodeKindDB       = "db"
	NodeKindQueue    = "queue"
	NodeKindExternal = "external"
)

// TopologyNodeIDPrefixes — agregatörün yazdığı önekler ve MV
// sözlüğündeki node_kind karşılıkları. Sıra önemsiz (önekler
// birbirinin öneki değil) ama liste TEK nüsha olmak zorunda.
var TopologyNodeIDPrefixes = []struct{ Prefix, Kind string }{
	{"db:", NodeKindDB},
	{"queue:", NodeKindQueue},
	{"ext:", NodeKindExternal},
}

// TopologyNodeIdentity — bir düğüm ID'sinin KIND'ı ve önekten soyulmuş
// GÖSTERİM ADI. Öneksiz ID'de kind BOŞ döner ("bilmiyorum") — düz bir
// servis adı ile öneki tanınmayan bir ID arasındaki farkı çağıran
// bilir, bu fonksiyon uydurmaz.
//
// Kind ve ad BİRLİKTE dönüyor çünkü ayrı türetildiklerinde ayrışıyorlar
// (v0.9.1029'un ölçtüğü kusur: aynı düğüm hem kind:"service" hem ham
// `queue:kafka:api.usage` adı taşıyordu).
func TopologyNodeIdentity(id string) (kind, name string) {
	for _, p := range TopologyNodeIDPrefixes {
		if strings.HasPrefix(id, p.Prefix) {
			return p.Kind, id[len(p.Prefix):]
		}
	}
	return "", id
}

// TopologyEndpointKind — bir kenar UCUNUN kind'ı. Öneksiz = servis:
// agregatörün üç pass'i de düz `service_name` yazdığında gerçekten bir
// servis yazıyor, ve altyapı düğümlerinin HEPSİ önekli.
func TopologyEndpointKind(id string) string {
	if k, _ := TopologyNodeIdentity(id); k != "" {
		return k
	}
	return NodeKindService
}

// ─── Problem ÖZNE kimliği (v0.9.1338, entity-model Faz 4b) ──────────
//
// DBSubjectID bir veritabanı örneğini `problems.service` kolonunda
// taşınabilir TEK bir dizgiye kodlar. Biçim topoloji MV yazıcısının
// düğüm adıyla BİREBİR aynı — `db:<system>@<instance>` — ve öneki
// TopologyNodeIDPrefixes'ten ALIR, ikinci kez YAZMAZ.
//
// ⚠️ ÖLÇÜLMÜŞ UYARI — `instance` bileşeni HANGİ kimlik uzayından?
//
// Biçim topoloji ile aynı, KİMLİK UZAYI aynı DEĞİL ve bu bilinçli:
//
//	topoloji / db_summary_5m / db_caller_summary_5m  → HAT A (span türevi)
//	    instance = dbInstanceExpr, yani ilk basamak peer_service.
//	problems (bu fonksiyonun bugünkü tek üreticisi)  → HAT B (receiver metriği)
//	    instance = db_capacity.go'daki instanceExpr, yani `instance` attr'ı.
//
// Lokal ölçüm (2026-08-24, 24s pencere):
//
//	HAT B receiver instance'ları : corebank-scan.prod, corebank-dg.prod
//	HAT A db_caller_summary_5m   : instance='oracle' (peer_service kazandı)
//	İKİSİNİN KESİŞİMİ            : 0 satır
//	    (SELECT count() FROM db_caller_summary_5m WHERE instance IN
//	     ('corebank-scan.prod','corebank-dg.prod') OR host_name IN (…) → 0)
//
// Yani fiziksel adres HAT A'da KAYBOLUYOR: aynı span'ın
// `server.address` attr'ı `corebank-scan.prod:1521` iken dbInstanceExpr
// birinci basamakta `peer_service='oracle'` görüp orada duruyor.
// (host_name kolonu da köprü DEĞİL — o ÇAĞIRANIN pod'u:
// `casemgr-prod-2-r5993`.)
//
// Sonuç ve KURAL: bu ID'yi HAT A tablolarına ad eşitliğiyle JOIN ETME.
// Bir gün gerekirse köprü ayrı bir dilimdir (server.address host kısmı
// ya da operatör eşlemesi) ve ÖLÇÜLEREK kurulur. Bugün bu ID'nin tek
// sözü şudur: "öznem `<instance>` adlı `<system>` veritabanıdır" —
// bir servis olmadığını söyleyebilmesi zaten bugünkü sessiz çıkmaz
// linkten iyidir.
//
// system KÜÇÜK HARFE indiriliyor: capacityCheck.dbsys 'ORACLE' yazıyor,
// tüm span tarafı 'oracle'. İki yazım aynı veritabanı için iki ayrı özne
// üretirdi.
//
// Boş bileşen → boş ID ("kodlayamıyorum"). Çağıran o zaman bugünkü ham
// değeri yazar, yani ÇÖZÜLMEMİŞ DAL BAYT-BAYT bugünküyle aynı kalır.
func DBSubjectID(system, instance string) string {
	system = strings.ToLower(strings.TrimSpace(system))
	instance = strings.TrimSpace(instance)
	if system == "" || instance == "" {
		return ""
	}
	return dbNodePrefix() + system + "@" + instance
}

// ParseDBSubjectID — DBSubjectID'nin tersi. ok=false: bu dizgi bir db
// öznesi DEĞİL (öneksiz, ya da '@' taşımıyor, ya da bir yarısı boş).
//
// Öneki TopologyNodeIdentity çözüyor — `db:` dizgisi burada İKİNCİ KEZ
// YAZILMIYOR. Ayırıcı İLK '@': system hiçbir zaman '@' taşımaz, instance
// (bir host adı) teorik olarak taşıyabilir, o yüzden fazlası instance'a
// kalır ve gidiş-dönüş bozulmaz.
func ParseDBSubjectID(id string) (system, instance string, ok bool) {
	kind, rest := TopologyNodeIdentity(id)
	if kind != NodeKindDB {
		return "", "", false
	}
	at := strings.Index(rest, "@")
	if at <= 0 || at == len(rest)-1 {
		return "", "", false
	}
	return rest[:at], rest[at+1:], true
}

// dbNodePrefix — `db:` öneki, sözlükten OKUNUR. Sabiti burada tekrar
// yazmak TopologyNodeIDPrefixes'i tek-kaynak olmaktan çıkarırdı.
func dbNodePrefix() string {
	for _, p := range TopologyNodeIDPrefixes {
		if p.Kind == NodeKindDB {
			return p.Prefix
		}
	}
	return "" // ulaşılamaz: sözlükte db girdisi var (identity_test pinliyor)
}

// nsIdentityKeys — bir servisin NAMESPACE'ini çözen anahtar zinciri,
// SIRASIYLA. Sıra sözleşmedir: coalesce İLK boş-olmayanı alır, yani
// basamak kaydırmak namespace'i SESSİZCE yeniden adlandırır.
//
// v0.9.1318 — bu zincir daha önce İKİ AYRI sözlükte yaşıyordu ve ilk iki
// basamak TERS sıradaydı:
//
//	service_namespaces.go : k8s.namespace.name → service.namespace → …  (yalnız res_keys)
//	service_metadata.go   : service.namespace → k8s.namespace.name → …  (res_keys + attr_keys)
//
// Yani HER İKİ anahtarı da yayınlayan bir servis, /service-map'in
// namespace kutularında bir ada, /services facet'inde BAŞKA bir ada
// sahipti. Üstelik service_namespaces.go'nun yorumu zinciri
// "deriveNamespaceSQL ile aynı sözlük" diye tarif ediyordu —
// deriveNamespaceSQL diye bir şey repoda HİÇ YOKTU; yorum boşluğu
// gösteren bir sözleşme iddiasıydı.
//
// KANONİK SIRA = service.namespace ÖNCE. Üç gerekçe:
//
//  1. Semantik: `service.namespace` tanımı GEREĞİ bu sorunun cevabıdır —
//     semconv onu `service.name` için bir namespace olarak tanımlar,
//     (service.namespace, service.name) ikilisi servisin MANTIKSAL
//     kimliğidir. `k8s.namespace.name` başka bir soruyu cevaplar (bu pod
//     hangi k8s nesne-namespace'inde yaşıyor) ve yalnızca KORELEDİR.
//  2. Kaynak: `service.namespace` operatörün SDK'da bilerek bildirdiği
//     bir değerdir; `k8s.namespace.name` k8sattributes processor'ın
//     otomatik enjekte ettiği bir değerdir. Bilinçli bildirim otomatik
//     enjeksiyonu yener — cluster zincirinin açık `k8s.cluster.name`i
//     çıplak `cluster`dan önde tutmasıyla aynı ruh (v0.5.471 deseni).
//  3. Patlama yarıçapı: service_metadata'nın türetimi KALICI bir kolonu
//     (namespace_auto) yazar; operatör onu /services'te görür ve elle
//     pinleyebilir. service_namespaces.go ise okuma-anı görsel bir
//     zenginleştirmedir. Kalıcı olanı ephemeral olana uydurmak hiçbir
//     satırı yeniden yazmaz; tersi namespace_auto'yu topluca değiştirirdi.
//
// Not: "standart anahtarlar önde" kuralı İKİ sırayla da sağlanıyordu —
// `k8s.namespace.name` de standart bir semconv anahtarıdır. Eski yorum
// kendisiyle çelişmiyordu; asıl kusur iki standart anahtarın BİRBİRİNE
// karşı hiç sıralanmamış olmasıydı.
//
// kubernetes.* varyantları OpenShift cluster-logging / legacy pipeline
// yazımlarıdır ve standart anahtarların ARKASINDA kalır (v0.9.53 B2
// kararı). Düşerlerse mapping OpenShift filosunda sessizce boşalır.
var nsIdentityKeys = []string{
	"service.namespace",
	"k8s.namespace.name",
	"kubernetes.namespace.name",
	"kubernetes.namespace_name",
}

// namespaceExpr — nsIdentityKeys'ten üretilen TEK namespace ifadesi.
// Önce res_keys basamakları (kaynak kapsamı), sonra attr_keys
// basamakları (span kapsamı) — resource scope span scope'tan ÖNCE
// gelir, çünkü namespace bir SÜREÇ özelliğidir, tekil bir çağrının
// değil.
//
// coalesce+nullIf biçimi BİLİNÇLİ (v0.9.1318): eski service_metadata
// yazımı `multiIf(has(res_keys,k), res_values[…], …)` idi ve anahtar
// VAR ama değeri BOŞ olduğunda boş dize döndürüp sonraki basamakları
// MASKELİYORDU. `service.namespace` anahtarını boş yayınlayan bir servis, dolu bir
// `k8s.namespace.name` taşısa bile namespace'siz görünüyordu.
// coalesce+nullIf boşu atlar; bu yüzden iki biçim BİRLEŞTİRİLİRKEN
// coalesce kazandı — kayıp üreten biçim değil.
//
// Terminal boş dize — "namespace bilinmiyor". 'unknown' gibi bir sentinel
// BİLEREK yok: çağıranların ikisi de boşu "kayıt üretme" diye okuyor
// (GetServiceNamespaces boş ns'i map'e koymaz, marginalModes boş değeri
// saymaz). Sentinel eklemek her namespace'siz servisi sahte bir
// "unknown" kutusunda toplardı.
func namespaceExpr() string {
	var b strings.Builder
	b.WriteString("coalesce(\n")
	for _, k := range nsIdentityKeys {
		b.WriteString("\t\t         nullIf(res_values[indexOf(res_keys, '" + k + "')], ''),\n")
	}
	for _, k := range nsIdentityKeys {
		b.WriteString("\t\t         nullIf(attr_values[indexOf(attr_keys, '" + k + "')], ''),\n")
	}
	b.WriteString("\t\t         ''\n\t\t       )")
	return b.String()
}

// namespaceHasGuard — namespaceExpr'in WHERE ön-elemesi, AYNI anahtar
// listesinden üretilir. Ayrı yazılırsa ifade bir anahtarı okur ama guard
// o anahtarı taşıyan satırları eler; sonuç sessizce eksik namespace'tir.
// Parantezsiz döner — çağıran daha büyük bir OR listesine gömebilsin.
func namespaceHasGuard() string {
	parts := make([]string, 0, len(nsIdentityKeys)*2)
	for _, k := range nsIdentityKeys {
		parts = append(parts, "has(res_keys, '"+k+"')")
	}
	for _, k := range nsIdentityKeys {
		parts = append(parts, "has(attr_keys, '"+k+"')")
	}
	return strings.Join(parts, " OR ")
}

// msgClusterExpr is the shared CH expression for resolving a
// MESSAGING cluster identifier. Kept as a constant so the
// overview, detail, and callers query all use the exact same
// fallback chain — different expressions would silently group
// the same physical cluster into different rows in different
// views.
//
// Deliberately NOT falling back to peer_service because that
// column is also the destination's last-resort source; using
// it for both would conflate "I don't know the cluster" with
// "I don't know the destination" into one bucket.
//
// v0.9.1318 — `clusterExpr` idi, `msgClusterExpr` oldu. Aynı pakette
// `func (s *Store) clusterExpr()` (repo.go) VARDI ve o TAMAMEN BAŞKA bir
// şeyi çözüyor: k8s/infra cluster'ı (terfi edilmiş `cluster` kolonu +
// res/attr türetme yedeği). Go bu ikisinin bir arada yaşamasına izin
// verir — biri paket sabiti, öteki *Store metodu — yani derleyici
// UYARMAZ. Yeni bir sorgu yazıp `clusterExpr` yazan biri, k8s cluster'ı
// istediğini sanarken SESSİZCE messaging zincirini alıyordu. Sabit
// yeniden adlandırıldı çünkü metodun çağrı sayısı çok daha fazla ve
// operatöre görünen "cluster" kavramı odur.
const msgClusterExpr = `coalesce(
	nullIf(attr_values[indexOf(attr_keys, 'server.address')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'messaging.kafka.bootstrap.servers')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'messaging.kafka.cluster.name')], ''),
	'(default)'
)`

// dbInstanceExpr reproduces db_summary_5m's own `instance` identity on RAW
// spans, verbatim (store.go db_summary_5m DDL). Kept as a shared constant for
// the same reason msgClusterExpr above is: a raw query that spells this chain
// differently resolves the SAME physical database to a DIFFERENT name, and the
// two halves of one drawer then disagree.
//
// v0.9.274 — the Top-statements scan used to AND `peer_service = ?` here, one
// rung of the six. Any instance the MV named from a later rung matched zero
// spans, so the drawer showed a span count and callers (both MV-sourced, hence
// coalesced) beside an EMPTY Top statements table. Measured live: the
// clickhouse row's predicate matched 0 spans where the real identity matched
// 4659. Same defect as the dead row link fixed in v0.9.268, on the backend.
//
// The 'unknown' case needs no special branch: when every rung is empty the
// expression yields the literal 'unknown', which is exactly what the MV stores.
const dbInstanceExpr = `coalesce(
	nullIf(peer_service, ''),
	nullIf(attr_values[indexOf(attr_keys, 'server.address')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'net.peer.name')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'db.host')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'db.name')], ''),
	nullIf(service_name, ''),
	'unknown'
)`

// dbNameExpr — db_summary_5m'in `db_name` kimliğinin HAM SPANS ikizi,
// birebir (store.go db_summary_5m DDL'i). dbInstanceExpr ile aynı
// gerekçe: bu ifadeyi farklı yazan bir ham sorgu, AYNI mantıksal
// veritabanını FARKLI bir adla çözer ve tablonun iki yarısı birbiriyle
// çelişir. 'default' düşüşü de MV ile aynı — db.name yayınlamayan bir
// instrumentasyon her iki yolda da 'default' kovasına düşer.
const dbNameExpr = `coalesce(nullIf(attr_values[indexOf(attr_keys, 'db.name')], ''), 'default')`
