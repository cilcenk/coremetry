package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// 0010 partition sökme sihirbazının store tarafı.
// migrations/0010_state_repartition.sql'in yordamını koşar.
//
// Emsal: state_unify_admin.go (0009). Aynı ayrım — ön kontrol hiçbir şey
// yazmaz, uygulama tablo tablo ilerler ve İLK HATADA DURUR.
//
// BOOT'TA ASLA KOŞMAZ (ev kuralı v0.9.613): ON CLUSTER DDL'i N pod
// yarıştırırsa dağıtık DDL kuyruğu tıkanır. Yalnız admin tıklamasıyla.
//
// ═══ 0009'DAN ÜÇ AYRIM — üçü de bilinçli ═══
//
// F1. `cluster()` KESTİRMESİ YOK. 0009'un ADIM 2'si
//     `SELECT * FROM cluster(...)` yazıyordu çünkü tablo o an shard'lara
//     BÖLÜNMÜŞTÜ. 0010, 0009'un ARDINDAN koşar — tablolar artık TEK
//     replikasyon grubunda. Aynı ifade burada satırları shard sayısı
//     kadar KATLARDI (ölçüldü, v0.9.1309: birleşik `problems` 4808+4808
//     → cluster() 9616). Bu yüzden 0010'un kapısı 0009'unkinin TERSİ:
//     distinctPaths == 1 (bkz. stateRepartSingleGroup) ve INSERT düz
//     yerel tablodan okur.
//
// F2. İKİ AŞAMA. RENAME znode'u TAŞIMAZ: AŞAMA A yeni tabloyu geçici
//     `/state/<ad>_repart` yoluna kurar; kanonik yol ancak `_old`
//     düşünce boşalır, AŞAMA B onu geri alır. Bu yüzden ADIM 5 (DROP)
//     ile AŞAMA B AYNI eylemde koşar — ayrı koşulurlarsa kanonik yol
//     boşta kalır ve ara durum kısıtı gereksiz yere uzar.
//
// F3. AŞAMA A ile ADIM 5 ARASINDAKİ KAPI BİR HÜKÜM. 0009'da yanlış
//     sonuç ANINDA görünürdü (satır sayısı tutmaz, Inbox boşalır).
//     Burada değil: 0010 dedup DAVRANIŞINI değiştiriyor, yanlışlık ancak
//     bir id'nin started_at'i kaydığında — GÜNLER sonra — yüzeye çıkar.
//     ".sql" 7 gün öneriyor. Bir düğme bu hükmü VEREMEZ; sihirbaz
//     ADIM 4'ün TAZE ölçümünü düğmenin yanına basar ve operatör onaylar.

// stateRepartTables — 0010'un dokunduğu iki tablo. Göç dosyasındaki
// `name IN (…)` ile aynı olmalı; testte kırmızı yanar.
var stateRepartTables = []string{"problems", "anomaly_events"}

// stateRepartSingleGroup — 0010'un okuma kapısı. 0009'un
// clusterReadSafe()'inin TERSİ ve gerekçesi de ters:
//
//	0009: distinctPaths == shardCount → shard'lar ayrık, cluster() birleştirir
//	0010: distinctPaths == 1          → tek grup, düz yerel okuma yeterli
//
// distinctPaths > 1 ise 0009 uygulanmamış (ya da yarım) demektir; o
// durumda 0010'un düz INSERT'i yalnız bağlanılan shard'ın dilimini
// kopyalar ve geri kalanını RENAME ile ERİŞİLMEZ kılardı.
func stateRepartSingleGroup(distinctPaths int) bool { return distinctPaths == 1 }

// stateRepartGeneratorSQL — göç dosyasının AŞAMA A · ADIM 1 üreticisi.
//
// Şema ELLE YAZILMAZ. Prod'un `problems` tablosu store.go'nun taban
// DDL'inde OLMAYAN kolonlar taşıyor (ai_summary, ai_summary_at,
// comparator) ve v0.9.1338 `kind`i ekledi; elle yazılmış bir şema bu
// dördünü SESSİZCE düşürürdü. Bu sorgu CANLI create_table_query'i alır,
// adına `_repart` ekler, ON CLUSTER enjekte eder, ZK yoluna `_repart`
// koyar ve ` PARTITION BY <anahtar>` cümlesini SÖKER.
//
// Metin migrations/0010_state_repartition.sql ile AYNI olmalı; küme adı
// dışındaki her fark testte kırmızı yanar
// (TestStateRepartGeneratorsMatchMigration).
func stateRepartGeneratorSQL(cluster string) string {
	return `SELECT replaceOne(
         replaceOne(
           replaceOne(create_table_query,
                      concat('CREATE TABLE ', database, '.', name),
                      concat('CREATE TABLE ', database, '.', name,
                             '_repart ON CLUSTER ` + cluster + `')),
           concat('/state/', name, '\', \''),
           concat('/state/', name, '_repart\', \'')),
         concat(' PARTITION BY ', partition_key, ' '),
         ' '
       ) || ';' AS ddl
FROM system.tables
WHERE database = currentDatabase()
  AND name IN ('problems', 'anomaly_events')
ORDER BY total_rows ASC, name ASC`
}

// stateRepartPathfixGeneratorSQL — göç dosyasının AŞAMA B · B1 üreticisi.
// Kaynak tablo ZATEN partition'sızdır; tek iş ZK yolundan `_repart`
// ekini sökmek.
func stateRepartPathfixGeneratorSQL(cluster string) string {
	return `SELECT replaceOne(
         replaceOne(create_table_query,
                    concat('CREATE TABLE ', database, '.', name),
                    concat('CREATE TABLE ', database, '.', name,
                           '_pathfix ON CLUSTER ` + cluster + `')),
         concat('/state/', name, '_repart\', \''),
         concat('/state/', name, '\', \'')
       ) || ';' AS ddl
FROM system.tables
WHERE database = currentDatabase()
  AND name IN ('problems', 'anomaly_events')
ORDER BY total_rows ASC, name ASC`
}

// stateRepartTableFromDDL — üretilen DDL satırından tablo adını çıkarır.
// Sıra üreticinin sırasıdır (küçükten büyüğe); ayrı bir sorgu sırayı
// bozardı.
func stateRepartTableFromDDL(ddl, suffix string) string {
	const p = "CREATE TABLE "
	i := strings.Index(ddl, p)
	if i < 0 {
		return ""
	}
	rest := ddl[i+len(p):]
	if j := strings.IndexByte(rest, ' '); j >= 0 {
		rest = rest[:j]
	}
	if j := strings.LastIndexByte(rest, '.'); j >= 0 {
		rest = rest[j+1:]
	}
	if !strings.HasSuffix(rest, suffix) {
		return ""
	}
	name := strings.TrimSuffix(rest, suffix)
	if name == "" || strings.ContainsAny(name, "`'\" ;(") {
		return ""
	}
	return name
}

// stateRepartDDLWant — üretilen DDL'den beklenen şekil.
//
// "ZK yolunda ŞU segment GÖRÜNMESİN" diye AYRI bir alan YOK ve bu
// ölçülmüş bir karar: mutasyon testi öyle bir kapının HİÇBİR girdide
// ısırmadığını gösterdi. Bir tablonun DDL'inde tek bir ZK yolu vardır,
// yani "doğru segment var mı" kontrolü "yanlış segment yok mu"yu zaten
// kapsar — ikinci kapı yalnız gate'i olduğundan güçlü GÖSTERİRDİ.
type stateRepartDDLWant struct {
	Table   string // "problems"
	Suffix  string // "_repart" | "_pathfix"
	Cluster string // "uptrace_all"
	ZKName  string // ZK yolunun son segmenti: "problems_repart" | "problems"
}

// stateRepartCheckDDL — üretilen DDL'i ÇALIŞTIRMADAN ÖNCE doğrular.
//
// Göç dosyası bu doğrulamayı operatörün GÖZÜNE bırakıyor ("⚠ DOĞRULAMA:
// 2 satır çıkmalı; `_repart` GEÇMELİ, `PARTITION BY` GEÇMEMELİ").
// Sihirbazda göz yok — replaceOne'ın tutmadığı bir DDL sessizce
// PARTITION BY'lı bir kopya kurar ve göç HİÇBİR ŞEY DÜZELTMEZ ama
// başarılı görünür. Bu yüzden kapı koda taşınır.
func stateRepartCheckDDL(ddl string, w stateRepartDDLWant) error {
	d := strings.TrimSpace(ddl)
	switch {
	case d == "":
		return fmt.Errorf("üretici %s için boş DDL döndürdü", w.Table)
	case !strings.HasPrefix(d, "CREATE TABLE "):
		return fmt.Errorf("%s: DDL 'CREATE TABLE' ile başlamıyor", w.Table)
	case !strings.Contains(d, w.Table+w.Suffix+" ON CLUSTER "+w.Cluster):
		return fmt.Errorf("%s: ad/küme enjeksiyonu tutmadı — '%s%s ON CLUSTER %s' yok",
			w.Table, w.Table, w.Suffix, w.Cluster)
	case strings.Contains(d, "PARTITION BY"):
		return fmt.Errorf("%s: PARTITION BY hâlâ duruyor — replaceOne tutmadı, bu DDL göçü ANLAMSIZ kılar", w.Table)
	case !strings.Contains(d, "ReplicatedReplacingMergeTree("):
		return fmt.Errorf("%s: motor ReplicatedReplacingMergeTree değil", w.Table)
	case !strings.Contains(d, "/state/"+w.ZKName+"', '"):
		return fmt.Errorf("%s: ZK yolu '/state/%s' değil — replaceOne tutmadı", w.Table, w.ZKName)
	case !strings.Contains(d, "ORDER BY id"):
		return fmt.Errorf("%s: ORDER BY id kaybolmuş — dedup anahtarı DEĞİŞMEMELİ", w.Table)
	}
	return nil
}

// stateRepartFinalSentence — ADIM 0e / 4a çiftinin operatöre YAZILAN
// cümlesi. Saf fonksiyon: iki sayı → tek cümle. SQL okumadan anlaşılsın.
func stateRepartFinalSentence(table string, def, noMerge uint64) string {
	switch {
	case noMerge > def:
		return fmt.Sprintf(
			"%s: FARKLI (%d ↔ %d, +%d). Kusur CANLI — `do_not_merge_across_partitions_select_final = 1` açıldığı an %d BAYAT satır servis edilir. Göç ACİL.",
			table, def, noMerge, noMerge-def, noMerge-def)
	case noMerge < def:
		return fmt.Sprintf(
			"%s: BEKLENMEDİK (%d ↔ %d). Ayarı açmak satır SAYISINI DÜŞÜRÜYOR; bu iki sorgunun ölçtüğü şey değil. Göçü başlatmadan elle incele.",
			table, def, noMerge)
	default:
		return fmt.Sprintf(
			"%s: EŞİT (%d). Kusur bu veride BUGÜN ısırmıyor — ama started_at kayan İLK id'de ısırır ve o ayar açıldığı an bayat satır görünür. Göç gerekli, aciliyeti düşük.",
			table, def)
	}
}

// StateRepartTable — bir state tablosunun 0010 fotoğrafı.
type StateRepartTable struct {
	Name         string `json:"name"`
	Engine       string `json:"engine"`
	Rows         uint64 `json:"rows"`
	PartitionKey string `json:"partitionKey"`
	SortingKey   string `json:"sortingKey"`
	ZKPath       string `json:"zkPath"`
	// DistinctPaths — küme genelinde kaç replikasyon grubu. 0010'un
	// önkoşulu 1'dir (0009 uygulanmış demektir).
	DistinctPaths int                   `json:"distinctPaths"`
	Hosts         []StateUnifyHostCount `json:"hosts"`

	// ADIM 0d — FİZİKSEL bölünme (FINAL YOK): kaç id birden fazla
	// gün-partition'ında duruyor.
	IDs      uint64 `json:"ids"`
	SplitIDs uint64 `json:"splitIds"`

	// ADIM 0e / 4a — kusurun CANLI kanıtı. İkisi eşitse kusur bugün
	// ısırmıyor; noMerge büyükse ısırıyor.
	RowsFinal   uint64 `json:"rowsFinal"`
	RowsNoMerge uint64 `json:"rowsNoMerge"`
	FinalNote   string `json:"finalNote"`

	HasOld        bool `json:"hasOld"`
	HasRepart     bool `json:"hasRepart"`
	HasPathfix    bool `json:"hasPathfix"`
	HasPathfixOld bool `json:"hasPathfixOld"`

	// Stage — bu tablonun 0010 üzerindeki YERİ. Ölçülür, varsayılmaz.
	//   "A"       partition duruyor → AŞAMA A gerekli
	//   "B"       partition yok ama ZK yolu `_repart` → ADIM 5 + AŞAMA B
	//   "cleanup" yol kanonik, `_pathfix_old` yedeği duruyor
	//   "done"    bitti
	Stage   string `json:"stage"`
	Blocked string `json:"blocked,omitempty"`

	// Üreticiden gelir; FE'ye gönderilmez (uzun ve gereksiz).
	DDL        string `json:"-"`
	PathfixDDL string `json:"-"`
}

// StateRepartPreflightResult — ön kontrol ekranının tamamı.
type StateRepartPreflightResult struct {
	Cluster  string `json:"cluster"`
	Database string `json:"database"`
	Shards   int    `json:"shards"`
	Hosts    int    `json:"hosts"`

	Tables []StateRepartTable `json:"tables"`

	// Stage — kümenin bütünü için sıradaki aşama (en geride kalan tablo
	// belirler).
	Stage string `json:"stage"`

	// İki AYRI kapı. Supported = AŞAMA A koşulabilir.
	// FinalizeReady = ADIM 5 + AŞAMA B koşulabilir (yıkıcı; operatör
	// onayı ayrıca şart).
	Supported     bool `json:"supported"`
	FinalizeReady bool `json:"finalizeReady"`
	CleanupReady  bool `json:"cleanupReady"`

	Detail    string `json:"detail"`
	Generated int64  `json:"generated"`

	// Üç durumlu ölçümler (0009 ile aynı sözleşme: SIFIR = bilinmiyor).
	TopologyVerdict StateUnifyVerdict `json:"topologyVerdict"`
	UnifiedVerdict  StateUnifyVerdict `json:"unifiedVerdict"` // 0b — 0009 uygulanmış mı
	HostsVerdict    StateUnifyVerdict `json:"hostsVerdict"`   // 0c — dört host aynı sayıyı mı görüyor
	DefectVerdict   StateUnifyVerdict `json:"defectVerdict"`  // 0e — kusur bugün ısırıyor mu
}

// Normalize — nil dilim JSON'da `null` olur ve FE `.map()` üstünde
// patlar; React hata sınırı yalnız paneli değil SAYFAYI alır (v0.9.1315).
func (r *StateRepartPreflightResult) Normalize() {
	if r.Tables == nil {
		r.Tables = []StateRepartTable{}
	}
	for i := range r.Tables {
		if r.Tables[i].Hosts == nil {
			r.Tables[i].Hosts = []StateUnifyHostCount{}
		}
	}
}

// StateRepartTableResult — bir tablonun göç sonucu.
type StateRepartTableResult struct {
	Table      string           `json:"table"`
	Phase      string           `json:"phase"`
	OK         bool             `json:"ok"`
	RowsBefore uint64           `json:"rowsBefore"`
	RowsAfter  uint64           `json:"rowsAfter"`
	DurationMs int64            `json:"durationMs"`
	Steps      []StateUnifyStep `json:"steps"`
	Err        string           `json:"err,omitempty"`
}

// ── ön kontrol ────────────────────────────────────────────────────

// Sunucu tavanı 30 OLAMAZ: store.go'daki ReadTimeout 30s ve clickhouse-go
// onu okuma fazı başına BİR KEZ kurar — soket önce ölür, operatör kodu
// 159 yerine anlamsız bir "i/o timeout" görür (v0.9.280 kapısı,
// query_budget_test.go). 25 = GetNoisyRules'un bütçesi.
const stateRepartExecGuard = " SETTINGS max_execution_time = 25"

// stateRepartNoMergeGuard — ADIM 0e / 4a'nın ayarı. TEK yerde yazılı:
// ön kontrol ile doğrulama aynı ölçümü yapmalı, iki yazılış ıraksarsa
// ekrandaki sayı ile kapının ölçtüğü sayı farklı olur.
const stateRepartNoMergeGuard = " SETTINGS do_not_merge_across_partitions_select_final = 1, max_execution_time = 25"

// StateRepartPreflight — ADIM 0. HİÇBİR ŞEY YAZMAZ.
func (s *Store) StateRepartPreflight(ctx context.Context) (StateRepartPreflightResult, error) {
	res := StateRepartPreflightResult{
		Cluster:   strings.TrimSpace(s.cfg.ClusterName),
		Database:  s.DatabaseName(),
		Generated: time.Now().UnixMilli(),
		Tables:    []StateRepartTable{},
		Stage:     "A",
	}

	if res.Cluster == "" {
		// 0009'un mesajını KOPYALAMA. Orada tek-node kurulumda göç
		// GEREKMİYORDU (state tabloları zaten tek yerde). 0010'da kusur
		// tek-node'da da AYNEN var — ReplacingMergeTree partition İÇİNDE
		// dedup eder, node sayısıyla ilgisi yok. Yalnız YORDAM farklı
		// (ON CLUSTER yok), o yüzden sihirbaz koşmaz.
		res.Detail = "Küme adı ayarlı değil (COREMETRY_CH_CLUSTER_NAME). Bu sihirbaz ON CLUSTER yordamını koşar ve tek-node kurulumda çalışmaz. DİKKAT: 0010'un düzelttiği kusur tek-node'da da AYNEN vardır — migrations/0010_state_repartition.sql'in P3 notunu izleyerek clickhouse-client ile elle koştur."
		return res, nil
	}
	cq := quoteCHIdent(res.Cluster)
	db := backtickIdent(res.Database)

	// ADIM 0a — küme şekli. uniqExact()/count() UInt64 döner ve
	// clickhouse-go bunu *int'e TARAMAZ (canlı küme testi yakaladı).
	var shards, hosts uint64
	if err := s.conn.QueryRow(ctx,
		"SELECT uniqExact(shard_num), count() FROM system.clusters WHERE cluster = "+cq,
	).Scan(&shards, &hosts); err != nil {
		res.Detail = "Küme topolojisi okunamadı: " + err.Error()
		return res, nil
	}
	res.Shards, res.Hosts = int(shards), int(hosts)
	res.TopologyVerdict = VerdictOK
	if res.Shards == 0 {
		res.TopologyVerdict = VerdictBad
		res.Detail = fmt.Sprintf("'%s' kümesi system.clusters'ta yok.", res.Cluster)
		return res, nil
	}

	// AŞAMA A üreticisi — DDL + sıra (küçükten büyüğe).
	byName := map[string]*StateRepartTable{}
	var order []StateRepartTable
	drows, err := s.conn.Query(ctx, stateRepartGeneratorSQL(res.Cluster))
	if err != nil {
		res.Detail = "AŞAMA A üreticisi koşulamadı: " + err.Error()
		return res, nil
	}
	for drows.Next() {
		var ddl string
		if drows.Scan(&ddl) != nil {
			continue
		}
		name := stateRepartTableFromDDL(ddl, "_repart")
		if name == "" {
			continue
		}
		order = append(order, StateRepartTable{
			Name: name,
			DDL:  strings.TrimSuffix(strings.TrimSpace(ddl), ";"),
		})
	}
	drows.Close()
	if len(order) != len(stateRepartTables) {
		// SIFIR/EKSİK SATIR BELİRSİZDİR, "tablo yok" DEĞİL: bağlanılan
		// node yeniden başlıyorsa Replicated tablolar henüz iliştirilmemiş
		// olabilir ve sorgu hata VERMEDEN eksik döner (v0.9.1313 dersi).
		res.Detail = fmt.Sprintf("Beklenen %d state tablosundan %d tanesi görünüyor. Ya bağlanılan node henüz hazır değil (yeniden başlıyor olabilir), ya da şema beklenenden farklı. Göç bloklu.",
			len(stateRepartTables), len(order))
		return res, nil
	}

	// AŞAMA B üreticisi. AŞAMA A'dan ÖNCE koşarsa anlamsız bir DDL
	// üretir (kaynak hâlâ partition'lı); o yüzden yalnız stage=="B" olan
	// tabloda DOĞRULANIR, burada sadece toplanır.
	pfDDL := map[string]string{}
	if prows, err := s.conn.Query(ctx, stateRepartPathfixGeneratorSQL(res.Cluster)); err == nil {
		for prows.Next() {
			var ddl string
			if prows.Scan(&ddl) != nil {
				continue
			}
			if n := stateRepartTableFromDDL(ddl, "_pathfix"); n != "" {
				pfDDL[n] = strings.TrimSuffix(strings.TrimSpace(ddl), ";")
			}
		}
		prows.Close()
	}

	for i := range order {
		byName[order[i].Name] = &order[i]
		order[i].PathfixDDL = pfDDL[order[i].Name]
	}

	// ADIM 0b — şema fotoğrafı (partition/sorting/motor/satır).
	if er, err := s.conn.Query(ctx,
		"SELECT name, engine, coalesce(total_rows, 0), partition_key, sorting_key "+
			"FROM system.tables WHERE database = currentDatabase() AND name IN "+stateRepartNameList()); err == nil {
		for er.Next() {
			var n, e, pk, sk string
			var r uint64
			if er.Scan(&n, &e, &r, &pk, &sk) != nil {
				continue
			}
			if t := byName[n]; t != nil {
				t.Engine, t.Rows, t.PartitionKey, t.SortingKey = e, r, pk, sk
			}
		}
		er.Close()
	}

	// Yan tablolar (_old / _repart / _pathfix / _pathfix_old artıkları).
	suffixed := map[string]bool{}
	if sr, err := s.conn.Query(ctx,
		`SELECT name FROM system.tables WHERE database = currentDatabase() `+
			`AND (name LIKE '%\_old' OR name LIKE '%\_repart' OR name LIKE '%\_pathfix')`); err == nil {
		for sr.Next() {
			var n string
			if sr.Scan(&n) == nil {
				suffixed[n] = true
			}
		}
		sr.Close()
	}

	// ADIM 0b (ikinci yarı) — replikasyon şekli. 0009 uygulanmış mı?
	type pathInfo struct {
		distinct int
		path     string
	}
	paths := map[string]pathInfo{}
	if pr, err := s.conn.Query(ctx,
		"SELECT table, uniqExact(zookeeper_path), any(zookeeper_path) FROM clusterAllReplicas("+cq+
			", system.replicas) WHERE database = currentDatabase() AND table IN "+stateRepartNameList()+" GROUP BY table"); err == nil {
		for pr.Next() {
			var t, p string
			var d uint64
			if pr.Scan(&t, &d, &p) == nil {
				paths[t] = pathInfo{distinct: int(d), path: p}
			}
		}
		pr.Close()
	}

	// ADIM 0c — host başına satır. Boş bir tablo HİÇ GRUP ÜRETMEZ
	// (v0.9.1315), o yüzden eksik anahtar "ölçülemedi" değil "0 satır"
	// demektir; Hosts boş kalırsa aşağıda hostsKnown false olur.
	hostCounts := map[string][]StateUnifyHostCount{}
	var parts []string
	for _, t := range order {
		parts = append(parts, fmt.Sprintf(
			"SELECT '%s' AS tbl, hostName() AS host, count() AS rows FROM clusterAllReplicas(%s, currentDatabase(), %s) GROUP BY host",
			t.Name, cq, backtickIdent(t.Name)))
	}
	if hr, err := s.conn.Query(ctx, "SELECT tbl, host, rows FROM ("+strings.Join(parts, " UNION ALL ")+") ORDER BY tbl, host"); err == nil {
		for hr.Next() {
			var tbl, host string
			var n uint64
			if hr.Scan(&tbl, &host, &n) == nil {
				hostCounts[tbl] = append(hostCounts[tbl], StateUnifyHostCount{Host: host, Rows: n})
			}
		}
		hr.Close()
	}

	unifiedOK, hostsOK, defectKnown := true, true, true
	for i := range order {
		t := &order[i]
		t.Hosts = hostCounts[t.Name]
		t.HasOld = suffixed[t.Name+"_old"]
		t.HasRepart = suffixed[t.Name+"_repart"]
		t.HasPathfix = suffixed[t.Name+"_pathfix"]
		t.HasPathfixOld = suffixed[t.Name+"_pathfix_old"]

		pi := paths[t.Name]
		t.DistinctPaths = pi.distinct
		t.ZKPath = pi.path

		// 0b HARD STOP — 0009 uygulanmış olmalı.
		if !strings.Contains(pi.path, "/state/") || !stateRepartSingleGroup(pi.distinct) {
			unifiedOK = false
			t.Blocked = fmt.Sprintf("0009 uygulanmamış görünüyor: zookeeper_path '%s', %d replikasyon grubu (1 bekleniyor). Önce state birleştirme sihirbazını koştur.",
				pi.path, pi.distinct)
		}

		// 0c HARD STOP — dört host aynı sayıyı görmeli.
		if len(t.Hosts) == 0 {
			hostsOK = false
			if t.Blocked == "" {
				t.Blocked = "host başına satır sayısı okunamadı — küme yarım olabilir."
			}
		} else {
			for _, h := range t.Hosts {
				if h.Rows != t.Hosts[0].Rows {
					hostsOK = false
					if t.Blocked == "" {
						t.Blocked = "host'lar farklı satır sayısı veriyor — replikasyon yarım. Göç bloklu."
					}
					break
				}
			}
		}

		// ADIM 0d — FİZİKSEL bölünme.
		if err := s.conn.QueryRow(ctx, fmt.Sprintf(
			"SELECT uniqExact(id), countIf(np > 1) FROM (SELECT id, uniqExact(toDate(started_at)) AS np FROM %s.%s GROUP BY id)"+stateRepartExecGuard,
			db, backtickIdent(t.Name))).Scan(&t.IDs, &t.SplitIDs); err != nil {
			t.IDs, t.SplitIDs = 0, 0
		}

		// ADIM 0e — kusurun canlı kanıtı. İki AYRI sorgu: SETTINGS
		// sorgu düzeyindedir, tek ifadede iki farklı ayar olamaz.
		defOK := s.conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s.%s FINAL"+stateRepartExecGuard,
			db, backtickIdent(t.Name))).Scan(&t.RowsFinal) == nil
		nmOK := s.conn.QueryRow(ctx, fmt.Sprintf(
			"SELECT count() FROM %s.%s FINAL"+stateRepartNoMergeGuard,
			db, backtickIdent(t.Name))).Scan(&t.RowsNoMerge) == nil
		if defOK && nmOK {
			t.FinalNote = stateRepartFinalSentence(t.Name, t.RowsFinal, t.RowsNoMerge)
		} else {
			defectKnown = false
			t.FinalNote = t.Name + ": ölçülemedi — FINAL sayımı hata verdi."
		}

		t.Stage = stateRepartStage(t.PartitionKey, t.ZKPath, t.HasPathfixOld)
	}
	res.Tables = order

	if unifiedOK {
		res.UnifiedVerdict = VerdictOK
	} else {
		res.UnifiedVerdict = VerdictBad
	}
	if hostsOK {
		res.HostsVerdict = VerdictOK
	} else {
		res.HostsVerdict = VerdictBad
	}
	if defectKnown {
		res.DefectVerdict = VerdictOK
		for _, t := range res.Tables {
			if t.RowsNoMerge != t.RowsFinal {
				res.DefectVerdict = VerdictBad
			}
		}
	}

	res.Stage = stateRepartOverallStage(res.Tables)
	blocked := 0
	for _, t := range res.Tables {
		if t.Blocked != "" {
			blocked++
		}
	}

	switch {
	case !res.TopologyVerdict.Known() || !res.UnifiedVerdict.Known() || !res.HostsVerdict.Known():
		res.Detail = "Ön kontrol tamamlanamadı — bazı ölçümler okunamadı. Göç bloklu; okunamayan alanlar 'bilinmiyor' olarak işaretlendi."
	case blocked > 0:
		res.Detail = fmt.Sprintf("%d tablo tutarsız durumda — önce onları elle incele. Göç bloklu.", blocked)
	case res.Stage == "A":
		res.Supported = true
		res.Detail = "AŞAMA A koşulabilir: her tablo için partition'sız kopya kurulur, veri taşınır, atomik RENAME yapılır ve doğrulanır. Eski tablo `_old` olarak DURUR — bu aşama hiçbir şey SİLMEZ."
	case res.Stage == "B":
		res.FinalizeReady = true
		res.Detail = "AŞAMA A bitti. Sıradaki adım YIKICI: `_old` yedekleri düşürülür ve kanonik ZK yolu geri alınır. Göç dosyası en az 7 GÜN beklemeyi öneriyor — 0010 dedup DAVRANIŞINI değiştirdiği için yanlışlık ancak bir id'nin started_at'i kaydığında yüzeye çıkar. Aşağıdaki ADIM 4 ölçümü TAZE; yeşilse ve süre dolduysa devam et."
	case res.Stage == "cleanup":
		res.CleanupReady = true
		res.Detail = "AŞAMA B bitti, kanonik ZK yolu geri alındı. Geriye `_pathfix_old` yedekleri kaldı. Uygulamayı bir kez yeniden başlat, boot logunda `(N birleşik, 0 eski)` gördükten sonra bu yedekleri sil."
	default:
		res.Detail = "0010 tamamlanmış: iki tabloda da PARTITION BY yok, ZK yolu kanonik ve yedek kalmadı."
	}
	return res, nil
}

// stateRepartNameList — SQL IN listesi. Tablo adları sabit ve koddan
// gelir; yine de tek yerde üretilir ki liste ile SQL ıraksamasın.
func stateRepartNameList() string {
	q := make([]string, 0, len(stateRepartTables))
	for _, t := range stateRepartTables {
		q = append(q, quoteCHIdent(t))
	}
	return "(" + strings.Join(q, ", ") + ")"
}

// stateRepartStage — bir tablonun 0010 üzerindeki yeri. ÖLÇÜLEN üç
// girdiden türer; hiçbiri varsayılmaz.
func stateRepartStage(partitionKey, zkPath string, hasPathfixOld bool) string {
	if strings.TrimSpace(partitionKey) != "" {
		return "A"
	}
	if strings.Contains(zkPath, "_repart") {
		return "B"
	}
	if hasPathfixOld {
		return "cleanup"
	}
	return "done"
}

// stateRepartOverallStage — en GERİDE kalan tablo aşamayı belirler.
// Yarım kalmış bir koşuda iki tablo farklı aşamada olabilir; sihirbaz
// hep en geridekinden devam eder.
func stateRepartOverallStage(tables []StateRepartTable) string {
	rank := map[string]int{"A": 0, "B": 1, "cleanup": 2, "done": 3}
	best := "done"
	for _, t := range tables {
		if rank[t.Stage] < rank[best] {
			best = t.Stage
		}
	}
	return best
}

// ── AŞAMA A ───────────────────────────────────────────────────────

// StateRepartMigrateTable — AŞAMA A, TEK tablo: ADIM 1 → 2 → 3 → 3b → 4.
//
// Göç dosyasının manuel yolunda ADIM 1 ile ADIM 3 arasında bir PENCERE
// var: aradaki bir deploy `problems`in kolon sayısını değiştirirse
// `INSERT … SELECT *` arity hatası verir. Tek koşuda o pencere YOK.
func (s *Store) StateRepartMigrateTable(ctx context.Context, cluster string, t StateRepartTable) StateRepartTableResult {
	started := time.Now()
	out := StateRepartTableResult{Table: t.Name, Phase: "A"}
	db := backtickIdent(s.DatabaseName())
	name := backtickIdent(t.Name)
	rep := backtickIdent(t.Name + "_repart")
	old := backtickIdent(t.Name + "_old")
	oc := backtickIdent(cluster)

	fail := func(step string, err error) StateRepartTableResult {
		out.Steps = append(out.Steps, StateUnifyStep{Step: step, Err: err.Error()})
		out.Err = err.Error()
		out.DurationMs = time.Since(started).Milliseconds()
		return out
	}
	pass := func(step, note string) {
		out.Steps = append(out.Steps, StateUnifyStep{Step: step, OK: true, Note: note})
	}

	if t.Stage != "A" {
		return fail("aşama kapısı", fmt.Errorf("%s zaten AŞAMA A'yı geçmiş (aşama: %s)", t.Name, t.Stage))
	}
	if t.Blocked != "" {
		return fail("tutarlılık kapısı", fmt.Errorf("%s: %s", t.Name, t.Blocked))
	}
	// TEK GRUP KAPISI — 0009'un çift-sayım kapısının aynadaki hâli.
	if !stateRepartSingleGroup(t.DistinctPaths) {
		return fail("tek-grup kapısı", fmt.Errorf(
			"%s %d replikasyon grubunda (1 bekleniyor) — düz INSERT yalnız bağlanılan shard'ın dilimini kopyalar ve RENAME kalanını ERİŞİLMEZ kılardı; önce 0009",
			t.Name, t.DistinctPaths))
	}

	// ADIM 1 — `_repart`. Yarım kalmış koşuda zaten duruyor olabilir.
	if !t.HasRepart {
		if err := stateRepartCheckDDL(t.DDL, stateRepartDDLWant{
			Table: t.Name, Suffix: "_repart", Cluster: cluster,
			ZKName: t.Name + "_repart",
		}); err != nil {
			return fail("ADIM 1 (DDL kapısı)", err)
		}
		if err := s.conn.Exec(ctx, t.DDL); err != nil {
			return fail("ADIM 1", err)
		}
		pass("ADIM 1", t.Name+"_repart kuruldu (PARTITION BY yok, ORDER BY id aynı)")
	} else {
		pass("ADIM 1", "'_repart' zaten duruyordu, yeniden kurulmadı")
	}

	// ADIM 2 — düz yerel okuma. cluster() KULLANILMAZ (F1).
	if err := s.conn.Exec(ctx, fmt.Sprintf("INSERT INTO %s.%s SELECT * FROM %s.%s", db, rep, db, name)); err != nil {
		return fail("ADIM 2", err)
	}
	pass("ADIM 2", "veri kopyalandı (tek replikasyon grubu, düz okuma)")

	// Takas ÖNCESİ referans sayı.
	var beforeFinal uint64
	if err := s.conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s.%s FINAL"+stateRepartExecGuard, db, name)).Scan(&beforeFinal); err != nil {
		return fail("ADIM 3 (öncesi sayım)", err)
	}
	out.RowsBefore = beforeFinal

	// ADIM 3 — atomik takas.
	if err := s.conn.Exec(ctx, fmt.Sprintf(
		"RENAME TABLE %s.%s TO %s.%s, %s.%s TO %s.%s ON CLUSTER %s",
		db, name, db, old, db, rep, db, name, oc)); err != nil {
		return fail("ADIM 3", err)
	}
	pass("ADIM 3", t.Name+" → "+t.Name+"_old (yedek DURUYOR)")

	// ADIM 3b — yakalama. ReplacingMergeTree'de BEDAVA: aynı satırı
	// tekrar yazmak zararsız, id'ye göre toplanır. 0009'un ADIM 3c
	// anti-join'i burada GEREKSİZ (o, MergeTree beşlisi içindi).
	if err := s.conn.Exec(ctx, fmt.Sprintf("INSERT INTO %s.%s SELECT * FROM %s.%s", db, name, db, old)); err != nil {
		return fail("ADIM 3b", err)
	}
	pass("ADIM 3b", "tam yakalama (RMT, idempotent)")

	if err := s.stateRepartVerify(ctx, cluster, t.Name, beforeFinal, &out, ""); err != nil {
		return fail("ADIM 4", err)
	}

	out.OK = true
	out.DurationMs = time.Since(started).Milliseconds()
	return out
}

// ── ADIM 5 + AŞAMA B ──────────────────────────────────────────────

// StateRepartFinalizeTable — ADIM 5 (DROP `_old`) + AŞAMA B (kanonik ZK
// yolunu geri al). TEK eylem, çünkü kanonik yol ancak `_old` düşünce
// boşalır — RENAME znode'u TAŞIMAZ.
//
// GERİ DÖNÜŞÜ OLAN kısım: AŞAMA B kendi `_pathfix_old` yedeğini bırakır.
// GERİ DÖNÜŞÜ OLMAYAN kısım: `_old` düşer, yani 0010 ÖNCESİNE dönüş
// biter. Canlı veri kaybolmaz — canlı tablo AŞAMA A'dan beri doğru
// şemada ve doğrulanmış durumda.
func (s *Store) StateRepartFinalizeTable(ctx context.Context, cluster string, t StateRepartTable) StateRepartTableResult {
	started := time.Now()
	out := StateRepartTableResult{Table: t.Name, Phase: "B"}
	db := backtickIdent(s.DatabaseName())
	name := backtickIdent(t.Name)
	pf := backtickIdent(t.Name + "_pathfix")
	pfOld := backtickIdent(t.Name + "_pathfix_old")
	old := backtickIdent(t.Name + "_old")
	oc := backtickIdent(cluster)

	fail := func(step string, err error) StateRepartTableResult {
		out.Steps = append(out.Steps, StateUnifyStep{Step: step, Err: err.Error()})
		out.Err = err.Error()
		out.DurationMs = time.Since(started).Milliseconds()
		return out
	}
	pass := func(step, note string) {
		out.Steps = append(out.Steps, StateUnifyStep{Step: step, OK: true, Note: note})
	}

	if t.Stage != "B" {
		return fail("aşama kapısı", fmt.Errorf("%s AŞAMA B'ye hazır değil (aşama: %s)", t.Name, t.Stage))
	}
	if t.Blocked != "" {
		return fail("tutarlılık kapısı", fmt.Errorf("%s: %s", t.Name, t.Blocked))
	}
	// ADIM 4'ün TAZE ölçümü. Ekrandaki fotoğraf bayat olabilir; yıkıcı
	// adımın kapısı taze ölçüme dayanır.
	if t.RowsNoMerge != t.RowsFinal {
		return fail("ADIM 4a kapısı", fmt.Errorf(
			"%s: FINAL sayımları hâlâ ayrışıyor (%d ↔ %d) — AŞAMA A istenen sonucu vermemiş, yedek DÜŞÜRÜLMEZ",
			t.Name, t.RowsFinal, t.RowsNoMerge))
	}
	if err := stateRepartCheckDDL(t.PathfixDDL, stateRepartDDLWant{
		Table: t.Name, Suffix: "_pathfix", Cluster: cluster,
		ZKName: t.Name,
	}); err != nil {
		return fail("B1 (DDL kapısı)", err)
	}

	var beforeFinal uint64
	if err := s.conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s.%s FINAL"+stateRepartExecGuard, db, name)).Scan(&beforeFinal); err != nil {
		return fail("B1 (öncesi sayım)", err)
	}
	out.RowsBefore = beforeFinal

	// ADIM 5 — `_old` düşer. Kanonik ZK yolu ANCAK BURADA boşalır.
	// SYNC olmadan znode temizlenmeden gelen CREATE "Replica already
	// exists" verir (v0.8.190 dersi: boyut koruması da bypass edilir).
	if t.HasOld {
		if err := s.conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s.%s ON CLUSTER %s SYNC%s",
			db, old, oc, purgeGuard)); err != nil {
			return fail("ADIM 5", err)
		}
		pass("ADIM 5", t.Name+"_old düşürüldü — kanonik ZK yolu boşaldı")
	} else {
		pass("ADIM 5", "'_old' zaten yoktu")
	}

	// B1 — `_pathfix`, kanonik yolda.
	if !t.HasPathfix {
		if err := s.conn.Exec(ctx, t.PathfixDDL); err != nil {
			return fail("B1", err)
		}
		pass("B1", t.Name+"_pathfix kanonik yolda kuruldu")
	} else {
		pass("B1", "'_pathfix' zaten duruyordu, yeniden kurulmadı")
	}

	// B2 — kopyala + takas + yakalama.
	if err := s.conn.Exec(ctx, fmt.Sprintf("INSERT INTO %s.%s SELECT * FROM %s.%s", db, pf, db, name)); err != nil {
		return fail("B2", err)
	}
	if err := s.conn.Exec(ctx, fmt.Sprintf(
		"RENAME TABLE %s.%s TO %s.%s, %s.%s TO %s.%s ON CLUSTER %s",
		db, name, db, pfOld, db, pf, db, name, oc)); err != nil {
		return fail("B2 (RENAME)", err)
	}
	if err := s.conn.Exec(ctx, fmt.Sprintf("INSERT INTO %s.%s SELECT * FROM %s.%s", db, name, db, pfOld)); err != nil {
		return fail("B2 (yakalama)", err)
	}
	pass("B2", t.Name+" → "+t.Name+"_pathfix_old (yedek DURUYOR)")

	if err := s.stateRepartVerify(ctx, cluster, t.Name, beforeFinal, &out, "_repart"); err != nil {
		return fail("B3", err)
	}

	out.OK = true
	out.DurationMs = time.Since(started).Milliseconds()
	return out
}

// stateRepartVerify — ADIM 4 / B3. Dört şart:
//
//	(a) partition_key BOŞ, sorting_key 'id'
//	(b) ZK yolu tek ve (AŞAMA B'de) `_repart`sız
//	(c) her host aynı satır sayısını görüyor
//	(d) FINAL satır sayısı KAYBOLMAMIŞ
//
// (d) `>=` ile ölçülür, `==` ile DEĞİL: RENAME'den sonra uygulama yeni
// tabloya yazmaya devam eder, yani sayı ARTABİLİR. `==` istemek meşgul
// bir kurulumda göçü sahte bir hatayla durdururdu.
func (s *Store) stateRepartVerify(ctx context.Context, cluster, table string, beforeFinal uint64, out *StateRepartTableResult, forbidZK string) error {
	cq := quoteCHIdent(cluster)
	db := backtickIdent(s.DatabaseName())
	name := backtickIdent(table)

	var pk, sk string
	if err := s.conn.QueryRow(ctx,
		"SELECT partition_key, sorting_key FROM system.tables WHERE database = currentDatabase() AND name = "+quoteCHIdent(table),
	).Scan(&pk, &sk); err != nil {
		return fmt.Errorf("şema okunamadı: %w", err)
	}
	if strings.TrimSpace(pk) != "" {
		return fmt.Errorf("%s hâlâ partition'lı (%s) — göç TUTMADI", table, pk)
	}
	if strings.TrimSpace(sk) != "id" {
		return fmt.Errorf("%s sorting_key '%s' (id bekleniyor) — dedup anahtarı DEĞİŞMİŞ", table, sk)
	}

	var distinct, hosts uint64
	var zk string
	if err := s.conn.QueryRow(ctx,
		"SELECT uniqExact(zookeeper_path), any(zookeeper_path), count() FROM clusterAllReplicas("+cq+
			", system.replicas) WHERE database = currentDatabase() AND table = "+quoteCHIdent(table),
	).Scan(&distinct, &zk, &hosts); err != nil {
		return fmt.Errorf("replikasyon şekli okunamadı: %w", err)
	}
	if !stateRepartSingleGroup(int(distinct)) {
		return fmt.Errorf("%s %d replikasyon grubunda (%s)", table, distinct, zk)
	}
	if forbidZK != "" && strings.Contains(zk, forbidZK) {
		return fmt.Errorf("%s ZK yolu hâlâ geçici (%s)", table, zk)
	}

	var uniqCounts, minRows, seenHosts uint64
	if err := s.conn.QueryRow(ctx, fmt.Sprintf(
		"SELECT uniqExact(c), min(c), count() FROM (SELECT hostName() AS h, count() AS c FROM clusterAllReplicas(%s, currentDatabase(), %s) GROUP BY h)",
		cq, name)).Scan(&uniqCounts, &minRows, &seenHosts); err != nil {
		return fmt.Errorf("host sayımı okunamadı: %w", err)
	}
	if uniqCounts != 1 {
		return fmt.Errorf("%s: host'lar farklı sayı veriyor (%d farklı değer, %d host)", table, uniqCounts, seenHosts)
	}

	var afterFinal uint64
	if err := s.conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s.%s FINAL"+stateRepartExecGuard, db, name)).Scan(&afterFinal); err != nil {
		return fmt.Errorf("sonrası sayım okunamadı: %w", err)
	}
	out.RowsAfter = afterFinal
	if afterFinal < beforeFinal {
		return fmt.Errorf("%s: FINAL satır sayısı DÜŞTÜ (%d → %d) — veri kaybı, yedek duruyor, geri al",
			table, beforeFinal, afterFinal)
	}

	// 4a — ayar açık/kapalı AYNI sayı. Partition kalmadığı için bu
	// tautolojiye yakındır ve TAM OLARAK o yüzden iyi bir kanıttır:
	// eşitliğin bozulması partition'ın sökülmediği anlamına gelir.
	var noMerge uint64
	if err := s.conn.QueryRow(ctx, fmt.Sprintf(
		"SELECT count() FROM %s.%s FINAL"+stateRepartNoMergeGuard,
		db, name)).Scan(&noMerge); err != nil {
		return fmt.Errorf("4a ölçümü okunamadı: %w", err)
	}
	if noMerge != afterFinal {
		return fmt.Errorf("%s: 4a TUTMADI — varsayılan %d, do_not_merge %d", table, afterFinal, noMerge)
	}

	out.Steps = append(out.Steps, StateUnifyStep{
		Step: "ADIM 4", OK: true,
		Note: fmt.Sprintf("partition yok · ORDER BY id · %d host tek /state/ yolunda · %d satır (4a eşit)", seenHosts, afterFinal),
	})
	return nil
}

// ── temizlik ──────────────────────────────────────────────────────

// StateRepartDropBackups — AŞAMA B'nin `_pathfix_old` yedeklerini
// düşürür. AYRI ve AÇIK eylem; geri dönüşü YOKTUR.
func (s *Store) StateRepartDropBackups(ctx context.Context, cluster string, tables []string) []StateUnifyStep {
	steps := make([]StateUnifyStep, 0, len(tables))
	cq := quoteCHIdent(cluster)
	db := backtickIdent(s.DatabaseName())
	oc := backtickIdent(cluster)
	for _, t := range tables {
		if !stateRepartKnownTable(t) {
			steps = append(steps, StateUnifyStep{Step: t, Err: "0010 bu tabloya dokunmuyor"})
			continue
		}
		// Yedeği düşürmeden önce CANLI tablonun kanonik yolda olduğunu
		// doğrula.
		var distinct uint64
		var zk string
		if err := s.conn.QueryRow(ctx,
			"SELECT uniqExact(zookeeper_path), any(zookeeper_path) FROM clusterAllReplicas("+cq+
				", system.replicas) WHERE database = currentDatabase() AND table = "+quoteCHIdent(t),
		).Scan(&distinct, &zk); err != nil {
			steps = append(steps, StateUnifyStep{Step: t, Err: "doğrulama okunamadı: " + err.Error()})
			continue
		}
		if !stateRepartSingleGroup(int(distinct)) || strings.Contains(zk, "_repart") {
			steps = append(steps, StateUnifyStep{Step: t, Err: fmt.Sprintf(
				"canlı tablo kanonik yolda değil (%d yol, %s) — yedek DÜŞÜRÜLMEDİ", distinct, zk)})
			continue
		}
		if err := s.conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s.%s ON CLUSTER %s SYNC%s",
			db, backtickIdent(t+"_pathfix_old"), oc, purgeGuard)); err != nil {
			steps = append(steps, StateUnifyStep{Step: t, Err: err.Error()})
			continue
		}
		steps = append(steps, StateUnifyStep{Step: t, OK: true, Note: t + "_pathfix_old düşürüldü"})
	}
	return steps
}

// stateRepartKnownTable — allowlist. Tablo adı SQL'e enterpole ediliyor;
// serbest metin kabul etmek DROP yüzeyini açardı.
func stateRepartKnownTable(name string) bool {
	for _, t := range stateRepartTables {
		if t == name {
			return true
		}
	}
	return false
}
