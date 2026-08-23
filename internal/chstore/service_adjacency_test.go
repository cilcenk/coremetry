package chstore

import (
	"os"
	"strings"
	"testing"
)

// service_adjacency_test.go — v0.9.1327 (entity-model A4).
//
// ─── Hangi kusuru çiviliyor ──────────────────────────────────────────
// Korelatörün komşuluk grafiği `topology_edges_5m`ten okunuyordu ve iki
// okuma da `WHERE … AND node_kind = 'service'` taşıyordu
// (service_adjacency.go:62, :122). O tek yüklem AYNI ANDA iki karşıt
// kusur üretiyordu, çünkü `node_kind` sorulan soruyu cevaplamıyor:
//
//	(a) ÇOCUK tarafında fazla eliyordu — db / queue / external düğümleri
//	    grafiğe hiç girmiyordu, yani paylaşılan bir veritabanı ortak
//	    neden olarak ADLANDIRILAMIYORDU.
//	(b) EBEVEYN tarafında hiç elemiyordu — `node_kind` üç yazıcı
//	    pass'inde de ÇOCUĞUN kind'ı; queue→consumer pass'inde çocuk
//	    gerçekten bir servistir (damga DOĞRU) ama ebeveyn
//	    `queue:<sys>:<topic>`. Yüklem, yanlış tiplenen tek düğümü içeri
//	    alıp doğru tiplenenleri eliyordu.
//
// ─── Brief'in TERS teşhisi (bilerek UYGULANMADI) ─────────────────────
// Hem plan (`docs/plans/cross-page-context-2026-08-23.md`) hem denetim
// (`docs/audit/entity-model-audit-2026-08-23.md:427`) (b)'yi bir YAZICI
// kusuru sanıyor ve "queue düğümünün node_kind='service' damgası
// düzeltilsin" diyor. Düzeltilseydi:
//
//   - buildServiceGraph'ın `tgt := ensure(e.ChildNode, nodeKindToOTel(
//     e.NodeKind))` satırı TÜKETİCİ SERVİSİ kuyruk düğümü olarak
//     çizerdi (öneksiz id nodeKindFromID'yi tetiklemez, ipucu ezilmez),
//   - ve 14 günlük bir karışık-damga penceresi açılırdı (TTL,
//     store.go:1954) — v0.9.1326'nın konusu olan sınıfın aynısı.
//
// Yazıcı DOĞRU yazıyor; yanlış olan sorulan soruydu. Bu yüzden bu dilim
// MV'ye hiç dokunmadı ve geçiş penceresi YOK. Aşağıdaki
// TestQueueConsumerPassStampsChildKind o kararı çiviliyor ki bir sonraki
// tur planın metnine bakıp yazıcıyı "düzeltmesin".

// TestTopologyNodeIdentity — önek sözlüğünün tablo-testi.
func TestTopologyNodeIdentity(t *testing.T) {
	cases := []struct {
		id       string
		wantKind string
		wantName string
	}{
		// Agregatörün üç yazımı (chstore/topology.go).
		{"db:oracle@oracle", NodeKindDB, "oracle@oracle"},
		{"db:clickhouse", NodeKindDB, "clickhouse"}, // v0.9.1318 öncesi düz yazım
		{"queue:kafka:payment.settled", NodeKindQueue, "kafka:payment.settled"},
		{"queue:rabbitmq@broker-1", NodeKindQueue, "rabbitmq@broker-1"},
		{"queue:sqs", NodeKindQueue, "sqs"},
		{"ext:api.stripe.com", NodeKindExternal, "api.stripe.com"},
		// Öneksiz = kind BİLİNMİYOR. Fonksiyon uydurmuyor; ad aynen döner.
		{"payment-service", "", "payment-service"},
		{"", "", ""},
		// Öneke BENZEYEN ama olmayan adlar sızmamalı.
		{"database:pg", "", "database:pg"},
		{"dbsomething", "", "dbsomething"},
		{"queued-jobs", "", "queued-jobs"},
	}
	for _, c := range cases {
		k, n := TopologyNodeIdentity(c.id)
		if k != c.wantKind || n != c.wantName {
			t.Errorf("TopologyNodeIdentity(%q) = (%q, %q), beklenen (%q, %q)",
				c.id, k, n, c.wantKind, c.wantName)
		}
	}
}

// TestTopologyEndpointKind — öneksiz ID'nin varsayılanı SERVİS.
func TestTopologyEndpointKind(t *testing.T) {
	cases := map[string]string{
		"payment-service":             NodeKindService,
		"":                            NodeKindService,
		"db:oracle@oracle":            NodeKindDB,
		"queue:kafka:payment.settled": NodeKindQueue,
		"ext:api.stripe.com":          NodeKindExternal,
	}
	for id, want := range cases {
		if got := TopologyEndpointKind(id); got != want {
			t.Errorf("TopologyEndpointKind(%q) = %q, beklenen %q", id, got, want)
		}
	}
}

// TestTypeEdgeCoversBothEndpoints — (b)'nin ta kendisi.
//
// Damga İKİ uca da vurulmak zorunda. Yalnız çocuğa vurulsaydı kuyruk
// EBEVEYNİ hâlâ servis görünürdü ve bu dilim (b)'yi hiç kapatmazdı —
// yani "kind alanı eklendi" ile "kusur kapandı" AYNI ŞEY DEĞİL.
func TestTypeEdgeCoversBothEndpoints(t *testing.T) {
	cases := []struct {
		name                   string
		caller, callee         string
		wantCallerK, wantCallK string
	}{
		{
			// cross-service pass — iki uç da servis (bugünkü tek kabul edilen hâl).
			name: "servis→servis", caller: "checkout", callee: "payments",
			wantCallerK: NodeKindService, wantCallK: NodeKindService,
		},
		{
			// infra pass — (a): bu kenar ESKİDEN HİÇ OKUNMUYORDU.
			name: "servis→db", caller: "orders", callee: "db:oracle@oracle",
			wantCallerK: NodeKindService, wantCallK: NodeKindDB,
		},
		{
			// infra pass, producer yarısı.
			name: "servis→queue", caller: "orders", callee: "queue:kafka:payment.settled",
			wantCallerK: NodeKindService, wantCallK: NodeKindQueue,
		},
		{
			name: "servis→external", caller: "orders", callee: "ext:api.stripe.com",
			wantCallerK: NodeKindService, wantCallK: NodeKindExternal,
		},
		{
			// queue→consumer pass — (b). Satırın node_kind'ı 'service' ve
			// bu DOĞRU (çocuk gerçekten servis); yanlış olan, o damgayı
			// EBEVEYN hakkında bir iddia sanmaktı.
			name: "queue→tüketici", caller: "queue:kafka:payment.settled", callee: "notifier",
			wantCallerK: NodeKindQueue, wantCallK: NodeKindService,
		},
	}
	for _, c := range cases {
		e := ServiceEdgePair{Caller: c.caller, Callee: c.callee}
		typeEdge(&e)
		if e.CallerKind != c.wantCallerK || e.CalleeKind != c.wantCallK {
			t.Errorf("%s: typeEdge(%q→%q) = (%q, %q), beklenen (%q, %q)",
				c.name, c.caller, c.callee, e.CallerKind, e.CalleeKind,
				c.wantCallerK, c.wantCallK)
		}
	}
}

// TestAdjacencyReadsAreNotNodeKindFiltered — SQL kapısı.
//
// Kaynak taranıyor çünkü kusur bir SQL DİZESİNDE yaşıyordu: tsc de
// go vet de göremez, ve yüklemi geri koymak tek kelimelik bir
// "temizlik" gibi görünür. İki uç da ayrı ayrı ölçülüyor —
// `feedback-gate-single-spelling`: tek yazımı arayan bir kapı
// ikizini muaf tutar.
func TestAdjacencyReadsAreNotNodeKindFiltered(t *testing.T) {
	b, err := os.ReadFile("service_adjacency.go")
	if err != nil {
		t.Fatalf("service_adjacency.go okunamadı: %v", err)
	}
	src := stripLineComments(string(b))

	for _, spelling := range []string{
		"node_kind = 'service'",
		"node_kind='service'",
		"node_kind ='service'",
		"node_kind= 'service'",
	} {
		if strings.Contains(src, spelling) {
			t.Errorf("adjacency okuması %q yüklemini geri getirmiş.\n"+
				"O yüklem AYNI ANDA iki karşıt kusur üretir: db/queue/external\n"+
				"ÇOCUKLARI eler (korelatör ortak nedeni adlandıramaz) ve kuyruk\n"+
				"EBEVEYNLERİNİ elemez (node_kind çocuğun kind'ıdır) — bkz. dosya\n"+
				"başı şerh ve identity.go'nun topoloji düğüm kimliği bloğu",
				spelling)
		}
	}

	// İki okuma da damgayı VURUYOR mu — filtreyi kaldırıp tiplemeyi
	// unutmak, kuyruk sızıntısını (b) olduğu gibi bırakırdı.
	if got := strings.Count(src, "typeEdge(&e)"); got != 2 {
		t.Errorf("typeEdge %d yerde çağrılıyor, 2 bekleniyordu "+
			"(GetServiceAdjacency + GetServiceAdjacencyWeighted). "+
			"Damgalanmayan bir okuma, kuyruk düğümünü servis diye döndürür", got)
	}

	// ── Kesme GUARD'ları (v0.9.1327 ile GELDİ, o yüzden burada) ──────
	//
	// Genişleme her altyapı kenarını AYNI kapağın altına sokuyor: aynı
	// mesh'te satır sayısı artıyor, yani kesme daha erken devreye
	// giriyor. Filtre bir yüklem olarak gitti ama getirdiği iki koruma
	// (yükseltilmiş kapak + deterministik sıra) SQL dizesinde yaşıyor ve
	// hiçbir tip denetimi görmüyor. Mutasyon testi bunları ilk turda
	// PİNSİZ buldu — "eklediğim korumayı kimse tutmuyor" hâli.
	if strings.Count(src, "LIMIT 20000") != 2 {
		t.Error("iki adjacency okumasının kapağı da LIMIT 20000 olmalı.\n" +
			"10000'e düşürmek, A4 genişlemesinden sonra kenarların bir\n" +
			"kısmını sessizce düşürür — ve düşen kenar korelatörün tükettiği\n" +
			"hata sinyalini taşıyan kenar olabilir")
	}
	// Ağırlıksız okumanın sıralayacak bir ağırlığı yok; grup anahtarına
	// göre sıralamak kesmeyi hiç değilse TEKRARLANABİLİR yapıyor. Sırasız
	// LIMIT her tazelemede BAŞKA bir alt küme döndürür ve komşuluk grafiği
	// 5 dakikada bir sebepsiz değişir.
	if !strings.Contains(src, "ORDER BY parent_service, child_node") {
		t.Error("ağırlıksız adjacency okumasının deterministik ORDER BY'ı yok —\n" +
			"sırasız LIMIT her tazelemede farklı bir alt küme döndürür")
	}
	if !strings.Contains(src, "ORDER BY errors DESC, calls DESC") {
		t.Error("ağırlıklı adjacency okuması hata-hacmi sıralamasını kaybetmiş —\n" +
			"kesme artık en yüksek hatalı kenarı koruduğunu garanti etmiyor")
	}
}

// TestQueueConsumerPassStampsChildKind — YAZICIYI DEĞİŞTİRME kapısı.
//
// Plan ve denetim metni bu satırı bir bug diye işaretliyor; değil.
// `node_kind` üç pass'te de ÇOCUĞUN kind'ı, ve queue→consumer
// kenarının çocuğu (`service_name`) gerçekten bir servis. 'queue'
// yazmak buildServiceGraph'ta tüketici servisi kuyruk olarak çizer
// (öneksiz id çağıranın ipucunu EZMEZ, servicegraph.go nodeKindFromID)
// ve üstüne 14 günlük karışık-damga penceresi açar (TTL store.go:1954).
func TestQueueConsumerPassStampsChildKind(t *testing.T) {
	b, err := os.ReadFile("topology.go")
	if err != nil {
		t.Fatalf("topology.go okunamadı: %v", err)
	}
	src := stripLineComments(string(b))

	// queue_source ebeveyn, service_name çocuk, damga 'service'.
	if !strings.Contains(src, "queue_source                                        AS parent_service") {
		t.Fatal("queue→consumer pass'in ebeveyni queue_source değil — bu test bayat")
	}
	if !strings.Contains(src, "'service'                                           AS node_kind") {
		t.Error("queue→consumer pass'in node_kind damgası 'service' DEĞİL.\n" +
			"Değiştirilmişse geri al: kolon ÇOCUĞUN kind'ıdır ve o kenarın\n" +
			"çocuğu (service_name) gerçekten bir servistir. Kuyruk EBEVEYNİ\n" +
			"okuma tarafında ID önekinden tiplenir (typeEdge), damgadan değil.")
	}
}

// stripLineComments — `--` (SQL) ve `//` (Go) satır yorumlarını boşaltır.
// Şerh metinleri aranan dizeleri KELİMESİ KELİMESİNE anıyor; ayıklanmazsa
// kapı kendi gerekçesini bulup yanlış-pozitif verir.
//
// Blok yorumu bilerek İŞLENMİYOR: bu iki dosyada `/* */` yok ve naif bir
// blok soyucu `//` içindeki `/*`'ı yutar (`feedback-comment-stripper-traps`).
func stripLineComments(src string) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "--") {
			out = append(out, "")
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}
