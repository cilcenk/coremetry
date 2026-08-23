package chstore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// 0009 state birleştirme sihirbazının store tarafı.
//
// Emsal: RollupPreflight / RollupApply / RollupStatus üçlüsü
// (internal/chstore/rollup_admin.go). Aynı ayrım: ön kontrol hiçbir şey
// yazmaz, uygulama tablo tablo ilerler ve İLK HATADA DURUR.
//
// BOOT'TA ASLA KOŞMAZ — ev kuralı (v0.9.613): ON CLUSTER DDL'i N pod
// yarıştırırsa dağıtık DDL kuyruğu tıkanır. Yalnız operatör tıklamasıyla.
//
// ADIM 2 sihirbazda tek ifadeye iniyor:
//     INSERT INTO <t>_unified SELECT * FROM cluster(<küme>, db, <t>)
// `cluster()` shard başına BİR replika okuduğu için bölünmüş bir tabloda
// tam olarak shard'ların birleşimini verir. Bu kestirme YALNIZ tablo
// gerçekten bölünmüşken doğru — kapı clusterReadSafe(), gerekçesi orada.

// StateUnifyMacro — bir host'un makro üçlüsü.
type StateUnifyMacro struct {
	Host    string `json:"host"`
	Shard   string `json:"shard"`
	Replica string `json:"replica"`
	Uniq    string `json:"uniq"`
}

// StateUnifyHostCount — tablo × host satır sayısı (bölünmenin kanıtı).
type StateUnifyHostCount struct {
	Host string `json:"host"`
	Rows uint64 `json:"rows"`
}

// StateUnifyTable — bir state tablosunun göç öncesi fotoğrafı.
type StateUnifyTable struct {
	Name          string `json:"name"`
	Engine        string `json:"engine"`
	Rows          uint64 `json:"rows"`
	Split         bool   `json:"split"`
	DistinctPaths int    `json:"distinctPaths"`
	// Shards — kümenin shard sayısı, tablo satırına TAŞINIR: çift-sayım
	// kapısının gerekçesini (cluster() kaç katına çıkarırdı) tablo
	// bağlamında yazabilmek için.
	Shards     int                   `json:"shards"`
	ZKPath     string                `json:"zkPath"`
	Hosts      []StateUnifyHostCount `json:"hosts"`
	HasOld     bool                  `json:"hasOld"`
	HasUnified bool                  `json:"hasUnified"`
	CatchUp    string                `json:"catchUp"`
	Blocked    string                `json:"blocked,omitempty"`

	// DDL üreticiden gelir; FE'ye gönderilmez (uzun ve gereksiz).
	DDL string `json:"-"`
}

// StateUnifyPreflightResult — ön kontrol ekranının tamamı.
type StateUnifyPreflightResult struct {
	Cluster      string            `json:"cluster"`
	Clusters     []string          `json:"clusters"`
	Database     string            `json:"database"`
	Shards       int               `json:"shards"`
	Hosts        int               `json:"hosts"`
	Macros       []StateUnifyMacro `json:"macros"`
	MacrosUnique bool              `json:"macrosUnique"`
	Tables       []StateUnifyTable `json:"tables"`
	SplitCount   int               `json:"splitCount"`
	DoneCount    int               `json:"doneCount"`
	Supported    bool              `json:"supported"`
	Detail       string            `json:"detail"`
	Generated    int64             `json:"generated"`
}

// StateUnifyStep — tek bir ifadenin sonucu (ilerleme ekranı için).
type StateUnifyStep struct {
	Step string `json:"step"`
	OK   bool   `json:"ok"`
	Note string `json:"note,omitempty"`
	Err  string `json:"err,omitempty"`
}

// StateUnifyTableResult — bir tablonun göç sonucu.
type StateUnifyTableResult struct {
	Table      string           `json:"table"`
	OK         bool             `json:"ok"`
	Rows       uint64           `json:"rows"`
	FinalRows  uint64           `json:"finalRows"`
	DurationMs int64            `json:"durationMs"`
	CatchUp    string           `json:"catchUp"`
	Steps      []StateUnifyStep `json:"steps"`
	Err        string           `json:"err,omitempty"`
}

// stateUnifyGeneratorSQL — göç dosyasının ADIM 1 üreticisi.
//
// Şema ELLE YAZILMAZ: bu sorgu her state tablosunun CANLI DDL'ini alır,
// adına `_unified` ekler, ON CLUSTER enjekte eder ve ZK yolunun
// `/{shard}/` segmentini `/state/`, replika adını `{shard}-{replica}`
// yapar. Böylece şema store.go ile ıraksayamaz.
//
// Metin migrations/0009_state_unify.sql ile AYNI olmalı; küme adı
// dışındaki her fark testte kırmızı yanar
// (TestStateUnifyGeneratorMatchesMigration).
func stateUnifyGeneratorSQL(cluster string) string {
	return `SELECT replaceOne(
         replaceOne(create_table_query,
                    concat('CREATE TABLE ', database, '.', name),
                    concat('CREATE TABLE ', database, '.', name, '_unified ON CLUSTER ` + cluster + `')),
         concat('/{shard}/', name, '\', \'{replica}\''),
         concat('/state/',  name, '\', \'{shard}-{replica}\'')
       ) || ';' AS ddl
FROM system.tables
WHERE database = currentDatabase()
  AND engine LIKE 'Replicated%'
  AND name NOT LIKE '.inner%'
  AND name NOT LIKE '%\_local'
  AND name NOT LIKE '%\_old'
  AND name NOT LIKE '%\_unified'
ORDER BY total_rows ASC, name ASC`
}

// stateUnifyTableFromDDL — üretilen DDL satırından tablo adını çıkarır.
// Sıra üreticinin sırasıdır (küçükten büyüğe), o yüzden ad DDL'den
// türetilir; ayrı bir sorgu sırayı bozardı.
func stateUnifyTableFromDDL(ddl string) string {
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
	name := strings.TrimSuffix(rest, "_unified")
	if name == "" || strings.ContainsAny(name, "`'\" ;(") {
		return ""
	}
	return name
}

// StateUnifyPreflight — ADIM 0. HİÇBİR ŞEY YAZMAZ.
func (s *Store) StateUnifyPreflight(ctx context.Context) (StateUnifyPreflightResult, error) {
	res := StateUnifyPreflightResult{
		Cluster:   strings.TrimSpace(s.cfg.ClusterName),
		Database:  s.DatabaseName(),
		Generated: time.Now().UnixMilli(),
		Tables:    []StateUnifyTable{},
		Macros:    []StateUnifyMacro{},
		Clusters:  []string{},
	}

	rows, err := s.conn.Query(ctx, "SELECT DISTINCT cluster FROM system.clusters ORDER BY cluster LIMIT 100")
	if err == nil {
		for rows.Next() {
			var c string
			if rows.Scan(&c) == nil {
				res.Clusters = append(res.Clusters, c)
			}
		}
		rows.Close()
	}

	if res.Cluster == "" {
		res.Detail = "Küme adı ayarlı değil (COREMETRY_CH_CLUSTER_NAME). Bu göç yalnız dağıtık kurulumda gerekir; tek-node kurulumda state tabloları zaten tek yerdedir."
		return res, nil
	}
	cq := quoteCHIdent(res.Cluster)

	// Küme şekli.
	//
	// uniqExact()/count() UInt64 döner ve clickhouse-go bunu *int'e
	// TARAMAZ ("converting UInt64 to *int is unsupported") — canlı küme
	// testi yakaladı; birim testlerinde görünmez, çünkü sorgu koşmaz.
	var shards, hosts uint64
	if err := s.conn.QueryRow(ctx,
		"SELECT uniqExact(shard_num), count() FROM system.clusters WHERE cluster = "+cq,
	).Scan(&shards, &hosts); err != nil {
		res.Detail = "Küme topolojisi okunamadı: " + err.Error()
		return res, nil
	}
	res.Shards, res.Hosts = int(shards), int(hosts)
	if res.Shards == 0 {
		res.Detail = fmt.Sprintf("'%s' kümesi system.clusters'ta yok.", res.Cluster)
		return res, nil
	}

	// Makrolar — '{shard}-{replica}' benzersiz mi? Birleşik yolda iki host
	// aynı replika adını iddia ederse ikincisi REPLICA_ALREADY_EXISTS alır.
	res.MacrosUnique = true
	seen := map[string]bool{}
	mrows, err := s.conn.Query(ctx,
		"SELECT hostName() AS h, anyIf(substitution, macro = 'shard') AS s, anyIf(substitution, macro = 'replica') AS r "+
			"FROM clusterAllReplicas("+cq+", system.macros) GROUP BY h ORDER BY h")
	if err != nil {
		res.Detail = "Makrolar okunamadı: " + err.Error()
		return res, nil
	}
	for mrows.Next() {
		var m StateUnifyMacro
		if mrows.Scan(&m.Host, &m.Shard, &m.Replica) != nil {
			continue
		}
		m.Uniq = m.Shard + "-" + m.Replica
		if m.Shard == "" || m.Replica == "" || seen[m.Uniq] {
			res.MacrosUnique = false
		}
		seen[m.Uniq] = true
		res.Macros = append(res.Macros, m)
	}
	mrows.Close()

	// ADIM 1 üreticisi — tablo listesi + DDL, küçükten büyüğe.
	drows, err := s.conn.Query(ctx, stateUnifyGeneratorSQL(res.Cluster))
	if err != nil {
		res.Detail = "ADIM 1 üreticisi koşulamadı: " + err.Error()
		return res, nil
	}
	var order []StateUnifyTable
	for drows.Next() {
		var ddl string
		if drows.Scan(&ddl) != nil {
			continue
		}
		name := stateUnifyTableFromDDL(ddl)
		if name == "" {
			continue
		}
		order = append(order, StateUnifyTable{Name: name, DDL: strings.TrimSuffix(strings.TrimSpace(ddl), ";")})
	}
	drows.Close()
	if len(order) == 0 {
		res.Detail = "Üretici hiç state tablosu döndürmedi — bu veritabanında Replicated state tablosu yok."
		return res, nil
	}

	// Motor + satır
	engines := map[string]string{}
	rowsOf := map[string]uint64{}
	if er, err := s.conn.Query(ctx,
		"SELECT name, engine, coalesce(total_rows, 0) FROM system.tables WHERE database = currentDatabase() AND engine LIKE 'Replicated%'"); err == nil {
		for er.Next() {
			var n, e string
			var r uint64
			if er.Scan(&n, &e, &r) == nil {
				engines[n] = e
				rowsOf[n] = r
			}
		}
		er.Close()
	}

	// `_old` / `_unified` artıkları
	suffixed := map[string]bool{}
	if sr, err := s.conn.Query(ctx,
		`SELECT name FROM system.tables WHERE database = currentDatabase() AND (name LIKE '%\_old' OR name LIKE '%\_unified')`); err == nil {
		for sr.Next() {
			var n string
			if sr.Scan(&n) == nil {
				suffixed[n] = true
			}
		}
		sr.Close()
	}

	// Replikasyon şekli — BÖLÜNMÜŞ MÜ sorusunun ölçülen cevabı.
	type pathInfo struct {
		distinct int
		path     string
	}
	paths := map[string]pathInfo{}
	if pr, err := s.conn.Query(ctx,
		"SELECT table, uniqExact(zookeeper_path), any(zookeeper_path) FROM clusterAllReplicas("+cq+
			", system.replicas) WHERE database = currentDatabase() GROUP BY table"); err == nil {
		for pr.Next() {
			var t, p string
			var d uint64
			if pr.Scan(&t, &d, &p) == nil {
				paths[t] = pathInfo{distinct: int(d), path: p}
			}
		}
		pr.Close()
	}

	// Host başına satır — bölünmenin operatöre GÖSTERİLEN kanıtı.
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

	for i := range order {
		t := &order[i]
		t.Engine = engines[t.Name]
		t.Rows = rowsOf[t.Name]
		t.Hosts = hostCounts[t.Name]
		t.HasOld = suffixed[t.Name+"_old"]
		t.HasUnified = suffixed[t.Name+"_unified"]
		if sp, ok := stateCatchUp(t.Name); ok {
			t.CatchUp = "sınırlı (" + strings.Join(sp.Key, ",") + ")"
		} else if strings.Contains(t.Engine, "ReplacingMergeTree") {
			t.CatchUp = "tam (RMT)"
		} else {
			t.CatchUp = "YOK"
		}
		pi := paths[t.Name]
		t.DistinctPaths = pi.distinct
		t.ZKPath = pi.path
		// BÖLÜNMÜŞ MÜ — ölçülen replikasyon şeklinden türetilir, asla
		// varsayılmaz. Aynı kapı `cluster()` kestirmesini de korur.
		t.Shards = res.Shards
		t.Split = clusterReadSafe(pi.distinct, res.Shards)
		if t.Split {
			res.SplitCount++
		} else {
			res.DoneCount++
			if pi.distinct > 1 {
				t.Blocked = fmt.Sprintf("%d farklı zookeeper_path ama küme %d shard — kısmen göç etmiş, elle incele", pi.distinct, res.Shards)
			}
		}
		if t.HasOld && t.Split {
			t.Blocked = "hâlâ bölünmüş ama '_old' kardeşi var — yarıda kesilmiş RENAME, elle incele"
		}
	}
	res.Tables = order

	switch {
	case !res.MacrosUnique:
		res.Detail = "'{shard}-{replica}' host'lar arasında benzersiz DEĞİL (ya da bir makro tanımsız). Birleşik yolda iki host aynı replika adını iddia eder ve ikincisi REPLICA_ALREADY_EXISTS alır. Makroları düzeltmeden göç başlatma."
	case res.SplitCount == 0:
		res.Detail = fmt.Sprintf("Bölünmüş tablo yok — %d state tablosunun hepsi zaten tek replikasyon grubunda. Göç tamamlanmış.", len(res.Tables))
	default:
		blocked := 0
		for _, t := range res.Tables {
			if t.Blocked != "" {
				blocked++
			}
		}
		if blocked > 0 {
			res.Detail = fmt.Sprintf("%d tablo bölünmüş ama %d tanesi tutarsız durumda — önce onları elle incele.", res.SplitCount, blocked)
		} else {
			res.Supported = true
			res.Detail = fmt.Sprintf("%d tablo bölünmüş, %d tablo zaten birleşik. Göç küçükten büyüğe tablo tablo koşacak; her tablodan sonra dört host aynı sayıyı vermezse DURUR.", res.SplitCount, res.DoneCount)
		}
	}
	return res, nil
}

// StateUnifyMigrateTable — TEK tablonun göçü: ADIM 1 → 2 → 3c kesim →
// 3 (RENAME) → 3b/3c yakalama → ADIM 4 doğrulama.
//
// Hata durumunda kalan tablolara DOKUNULMAZ (çağıran döngüyü durdurur).
func (s *Store) StateUnifyMigrateTable(ctx context.Context, cluster string, t StateUnifyTable) StateUnifyTableResult {
	started := time.Now()
	out := StateUnifyTableResult{Table: t.Name, CatchUp: t.CatchUp}
	cq := quoteCHIdent(cluster)
	db := backtickIdent(s.DatabaseName())
	name := backtickIdent(t.Name)
	uni := backtickIdent(t.Name + "_unified")
	old := backtickIdent(t.Name + "_old")

	fail := func(step string, err error) StateUnifyTableResult {
		out.Steps = append(out.Steps, StateUnifyStep{Step: step, Err: err.Error()})
		out.Err = err.Error()
		out.DurationMs = time.Since(started).Milliseconds()
		return out
	}
	pass := func(step, note string) {
		out.Steps = append(out.Steps, StateUnifyStep{Step: step, OK: true, Note: note})
	}

	// ÇİFT SAYIM KAPISI. `cluster()` yalnız tablo GERÇEKTEN bölünmüşken
	// shard'ların birleşimini verir; birleşik bir tabloda aynı verinin
	// replikalarını toplar ve satırları shard sayısı kadar KATLAR.
	if !t.Split {
		err := fmt.Errorf("'%s' bölünmüş değil (%d farklı zookeeper_path, küme %d shard) — cluster() okuması bu tabloda satırları %d katına çıkarırdı, INSERT atılmadı",
			t.Name, t.DistinctPaths, t.Shards, t.Shards)
		return fail("çift-sayım kapısı", err)
	}
	if t.Blocked != "" {
		return fail("tutarlılık kapısı", fmt.Errorf("%s: %s", t.Name, t.Blocked))
	}

	// ADIM 1 — `_unified`. Yarım kalmış koşuda zaten duruyor olabilir.
	if !t.HasUnified {
		if t.DDL == "" {
			return fail("ADIM 1", fmt.Errorf("%s için üretilmiş DDL yok", t.Name))
		}
		if err := s.conn.Exec(ctx, t.DDL); err != nil {
			return fail("ADIM 1", err)
		}
		pass("ADIM 1", "birleşik yolda "+t.Name+"_unified kuruldu")
	} else {
		pass("ADIM 1", "'_unified' zaten duruyordu, yeniden kurulmadı")
	}

	// ADIM 2 — tek ifade. cluster() shard başına BİR replika okur.
	src := fmt.Sprintf("cluster(%s, currentDatabase(), %s)", cq, name)
	if err := s.conn.Exec(ctx, fmt.Sprintf("INSERT INTO %s.%s SELECT * FROM %s", db, uni, src)); err != nil {
		return fail("ADIM 2", err)
	}
	pass("ADIM 2", "shard'ların birleşimi cluster() ile kopyalandı")

	// ADIM 3c kesim noktası — RENAME'den ÖNCE okunmalı. Sonra okunsaydı
	// uygulamanın yeni yazdıkları max'ı ileri iter ve pencere daralırdı.
	sp, hasCatchUp := stateCatchUp(t.Name)
	var cut string
	if hasCatchUp {
		if err := s.conn.QueryRow(ctx, stateCatchUpCutSQL(sp, t.Name+"_unified")).Scan(&cut); err != nil {
			cut = ""
		}
	}

	// ADIM 3 — atomik takas.
	if err := s.conn.Exec(ctx, fmt.Sprintf(
		"RENAME TABLE %s.%s TO %s.%s, %s.%s TO %s.%s ON CLUSTER %s",
		db, name, db, old, db, uni, db, name, backtickIdent(cluster))); err != nil {
		return fail("ADIM 3", err)
	}
	pass("ADIM 3", t.Name+" → "+t.Name+"_old (yedek DURUYOR)")

	// ADIM 3b / 3c — yakalama.
	oldSrc := fmt.Sprintf("cluster(%s, currentDatabase(), %s)", cq, old)
	switch {
	case strings.Contains(t.Engine, "ReplacingMergeTree"):
		if err := s.conn.Exec(ctx, fmt.Sprintf("INSERT INTO %s.%s SELECT * FROM %s", db, name, oldSrc)); err != nil {
			return fail("ADIM 3b", err)
		}
		pass("ADIM 3b", "tam yakalama (RMT, idempotent)")
	case hasCatchUp && cut != "":
		var n, u uint64
		if err := s.conn.QueryRow(ctx, stateCatchUpProbeFromSQL(sp, oldSrc), cut).Scan(&n, &u); err != nil {
			out.CatchUp = "YAPILAMADI — tekillik probu hata verdi: " + err.Error()
			pass("ADIM 3c", out.CatchUp)
			break
		}
		switch {
		case n == 0:
			out.CatchUp = "gerek yok — pencere boş"
			pass("ADIM 3c", out.CatchUp)
		case n != u:
			// Anti-join aynı anahtarlı satırların İKİSİNİ birden düşürürdü:
			// yakalama, önlemeye çalıştığı kaybı kendisi üretirdi.
			out.CatchUp = fmt.Sprintf("YAPILAMADI — (%s) pencerede tekil değil (%d satır / %d tekil); bu tabloda saniyelik boşluk OLABİLİR, %s_old'da duruyor",
				strings.Join(sp.Key, ","), n, u, t.Name)
			pass("ADIM 3c", out.CatchUp)
		default:
			if err := s.conn.Exec(ctx, stateCatchUpInsertFromSQL(sp, t.Name, oldSrc), cut, cut); err != nil {
				return fail("ADIM 3c", err)
			}
			out.CatchUp = fmt.Sprintf("sınırlı yakalama koştu — pencere %d satır, anahtar (%s) tekil", n, strings.Join(sp.Key, ","))
			pass("ADIM 3c", out.CatchUp)
		}
	default:
		out.CatchUp = "YOK — bu tabloda @catchup sözleşmesi yok; ADIM 2/3 arası satırlar " + t.Name + "_old'da kaldı"
		pass("ADIM 3c", out.CatchUp)
	}

	// ADIM 4 — doğrulama. İki şart: tek /state/ yolu VE host'lar eşit.
	// Yalnız sayı eşitliği yetmez; 0 satırlı tablo göçten önce de sonra da
	// her host'ta 0 verir, yani hiçbir şey kanıtlamaz.
	var distinct, hosts uint64
	var zk string
	if err := s.conn.QueryRow(ctx,
		"SELECT uniqExact(zookeeper_path), any(zookeeper_path), count() FROM clusterAllReplicas("+cq+
			", system.replicas) WHERE database = currentDatabase() AND table = "+quoteCHIdent(t.Name),
	).Scan(&distinct, &zk, &hosts); err != nil {
		return fail("ADIM 4a", err)
	}
	if distinct != 1 || strings.Contains(zk, "/{shard}/") {
		return fail("ADIM 4a", fmt.Errorf("%s hâlâ %d replikasyon grubunda (%s)", t.Name, distinct, zk))
	}
	var uniqCounts, minRows, seenHosts uint64
	if err := s.conn.QueryRow(ctx, fmt.Sprintf(
		"SELECT uniqExact(c), min(c), count() FROM (SELECT hostName() AS h, count() AS c FROM clusterAllReplicas(%s, currentDatabase(), %s) GROUP BY h)",
		cq, name)).Scan(&uniqCounts, &minRows, &seenHosts); err != nil {
		return fail("ADIM 4b", err)
	}
	if uniqCounts != 1 {
		return fail("ADIM 4b", fmt.Errorf("%s: host'lar farklı sayı veriyor (%d farklı değer, %d host) — geri al: RENAME TABLE %s TO %s_unified, %s_old TO %s ON CLUSTER %s",
			t.Name, uniqCounts, seenHosts, t.Name, t.Name, t.Name, t.Name, cluster))
	}
	out.Rows = minRows
	out.FinalRows = minRows
	if strings.Contains(t.Engine, "ReplacingMergeTree") {
		// 3b yakalaması birleşme olana kadar ham count()'u şişik bırakır.
		var fr uint64
		if s.conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s.%s FINAL", db, name)).Scan(&fr) == nil {
			out.FinalRows = fr
		}
	}
	pass("ADIM 4", fmt.Sprintf("%d host da tek /state/ yolunda, %d satır", seenHosts, minRows))

	out.OK = true
	out.DurationMs = time.Since(started).Milliseconds()
	return out
}

// StateUnifyDropBackups — ADIM 5. AYRI ve AÇIK eylem; asla göçün parçası
// olarak koşmaz. Geri dönüşü YOKTUR, o yüzden çağıran her tablonun
// birleşik olduğunu ÖNCE doğrular.
func (s *Store) StateUnifyDropBackups(ctx context.Context, cluster string, tables []string) []StateUnifyStep {
	steps := make([]StateUnifyStep, 0, len(tables))
	cq := quoteCHIdent(cluster)
	sorted := append([]string(nil), tables...)
	sort.Strings(sorted)
	for _, t := range sorted {
		if t == "" || strings.ContainsAny(t, "`'\" ;()") {
			steps = append(steps, StateUnifyStep{Step: t, Err: "geçersiz tablo adı"})
			continue
		}
		// Yedeği düşürmeden önce CANLI tablonun birleşik olduğunu doğrula.
		var distinct uint64
		var zk string
		if err := s.conn.QueryRow(ctx,
			"SELECT uniqExact(zookeeper_path), any(zookeeper_path) FROM clusterAllReplicas("+cq+
				", system.replicas) WHERE database = currentDatabase() AND table = "+quoteCHIdent(t),
		).Scan(&distinct, &zk); err != nil {
			steps = append(steps, StateUnifyStep{Step: t, Err: "doğrulama okunamadı: " + err.Error()})
			continue
		}
		if distinct != 1 || strings.Contains(zk, "/{shard}/") {
			steps = append(steps, StateUnifyStep{Step: t, Err: fmt.Sprintf("canlı tablo birleşik değil (%d yol) — yedek DÜŞÜRÜLMEDİ", distinct)})
			continue
		}
		if err := s.conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s.%s ON CLUSTER %s SYNC",
			backtickIdent(s.DatabaseName()), backtickIdent(t+"_old"), backtickIdent(cluster))); err != nil {
			steps = append(steps, StateUnifyStep{Step: t, Err: err.Error()})
			continue
		}
		steps = append(steps, StateUnifyStep{Step: t, OK: true, Note: t + "_old düşürüldü"})
	}
	return steps
}
