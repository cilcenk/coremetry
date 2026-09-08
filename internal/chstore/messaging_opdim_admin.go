package chstore

// messaging_opdim_admin.go — messaging_summary_5m `operation` BOYUTU
// YERİNDE GEÇİŞ SİHİRBAZI (v0.10.564).
//
// Neden var: v0.10.563 boot geçişi (store.go `mvDimMigrations` döngüsü)
// kolonu bulamazsa MV'yi DROP + RECREATE eder — dürüst ve küme-güvenli
// ama 90 GÜNLÜK messaging 5-dakikalık kovalarını SİLER (MV yalnız ileriye
// doğru yeniden dolar). Operatör deploy'dan ÖNCE bu sihirbazı koşarsa
// kolon boot'tan önce var olur, boot probe'u no-op'a düşer ve geçmiş
// KALIR.
//
// Yordam (reference-ch-inplace-mv-column-add; CH 24.8'de v0.8.52
// `trace_summary_5m` ile doğrulandı):
//
//	1. Combined MV'ye ADD COLUMN olmaz (kod 48) — kolon DEPO tablosuna
//	   (`.inner_id.<uuid>`, düz AggregatingMergeTree) eklenir.
//	2. MODIFY ORDER BY AYNI ALTER'da olmak ZORUNDA — iki nedenle. (i)
//	   `operation` sıralama anahtarına girmezse AggregatingMergeTree
//	   birleşmesi farklı operasyonları TEK satıra çökertir ve boyut
//	   SESSİZCE yok olur. (ii) CH `MODIFY ORDER BY`'a yalnız AYNI sorguda
//	   eklenen kolonu kabul eder ("Existing column … is used in the
//	   expression that was added to the sorting key"), yani "önce kolon,
//	   sonra anahtar" diye bölünen bir geçiş bir daha TAMAMLANAMAZ —
//	   kolonu DROP edip baştan başlamak gerekir.
//	3. `ALTER TABLE <mv> MODIFY QUERY <yeni SELECT>` — depo kolonu artık
//	   var, MV yeni SELECT'i kabul eder; mevcut satırlar korunur.
//	4. KÜME KİPİ (v0.10.563 dersi): çıplak ad bir Distributed SARMALAYICI
//	   ve KENDİ kolon listesini taşır. `_local` kolonu kazanır, sarmalayıcı
//	   KAZANMAZ → çıplak addan `SELECT operation` CH kod 47 verir ve boot
//	   probe'u (çıplak ada bakar) kolonu göremediği için MV'yi YİNE düşürür,
//	   yani yerinde geçiş boşa gider. Bu yüzden sarmalayıcıya da
//	   `ALTER TABLE … ADD COLUMN` basılır (Distributed ADD/DROP/MODIFY
//	   COLUMN kabul eder — adaptDDL adım 4 aynı şeyi yapıyor).
//
// GERİ ALMA YOK ve bilinçli: `MODIFY ORDER BY` yalnız anahtarın SONUNA
// ekleyebilir, geri alamaz (CH sıralama anahtarını daraltmaz); MODIFY
// QUERY'yi eski SELECT'e döndürmek de anlamsız — kolon ve anahtar zaten
// genişlemiş durumda kalır. Yanlış giderse çıkış yolu boot'un
// DROP+RECREATE'i, yani "geçmişi kaybet" senaryosudur.
//
// BİLİNEN AYRIŞMA: yerinde geçen kurulumun sıralama anahtarı
// (…, destination, time_bucket, operation) olur; taze DDL ise
// (…, destination, operation, time_bucket) yaratır. Okuma sonuçları aynı
// ((msg_system, cluster, destination) öneki korunuyor), ama iki kurulumun
// `sorting_key`'i BİRBİRİNDEN FARKLI görünür — bu beklenen durumdur,
// durum kartında da böyle raporlanır.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	messagingOpDimMV  = "messaging_summary_5m"
	messagingOpDimCol = "operation"
	// messagingOpDimKey — MODIFY ORDER BY hedefi. `operation` SONA gelir:
	// CH yalnız anahtarın sonuna kolon eklemeye izin verir.
	messagingOpDimKey = "(msg_system, cluster, destination, time_bucket, operation)"
	// messagingOpDimAnchor — ADD COLUMN'un AFTER çapası. Yoksa ALTER düşer.
	messagingOpDimAnchor = "destination"
	// messagingOpDimQueryMark — MV'nin create_table_query'sinde yeni SELECT'i
	// ayırt eden token (eski SELECT'te yok; ORDER BY'daki `operation`
	// kelimesiyle karışmaz).
	messagingOpDimQueryMark = "AS operation"
)

// MessagingOpDimStatusResult — sihirbaz durum kartı.
type MessagingOpDimStatusResult struct {
	Cluster string   `json:"cluster"`
	MV      string   `json:"mv"`    // depo adı (küme kipinde `_local`)
	Inner   []string `json:"inner"` // `.inner_id.<uuid>` listesi

	Objects []EntityLayerObjectStatus `json:"objects"`

	// Dört sözleşme parçası; hepsi TAM olmadan geçiş bitmiş sayılmaz.
	InnerColumn       bool `json:"innerColumn"`       // (a) depo tablosunda kolon
	KeyHasOperation   bool `json:"keyHasOperation"`   // (b) depo sorting_key'inde
	QueryHasOperation bool `json:"queryHasOperation"` // (c) MV SELECT'inde
	MVColumn          bool `json:"mvColumn"`          // (d) ÇIPLAK adın kolon listesinde

	// BootWouldDrop — boot probe'u ÇIPLAK ada bakar; orada kolon eksikse
	// bir sonraki deploy MV'yi DROP+RECREATE eder ve geçmiş gider.
	BootWouldDrop bool `json:"bootWouldDrop"`
	// Divergent — host'lar arasında birden çok inner uuid (ON CLUSTER
	// create yayılmamış); ALTER'lar uuid başına ayrı ayrı basılır.
	Divergent bool `json:"divergent"`

	State     string `json:"state"` // done | partial | missing | unknown
	Detail    string `json:"detail"`
	Generated int64  `json:"generated"`
}

// MessagingOpDimPreflightResult — "bu kurulum yerinde geçişi kaldırır mı".
type MessagingOpDimPreflightResult struct {
	Clusters         []string `json:"clusters"`
	SuggestedCluster string   `json:"suggestedCluster,omitempty"`
	Supported        bool     `json:"supported"`
	Detail           string   `json:"detail"`
	MV               string   `json:"mv"`
	Inner            []string `json:"inner"`
	Divergent        bool     `json:"divergent"`
	AlreadyDone      bool     `json:"alreadyDone"`
	// Statements — apply'ın koşacağı TAM SQL. Önizleme; operatör basmadan
	// önce görür (0011/0013 sihirbazlarında olmayan, burada şart olan şey:
	// SELECT metni store.go kataloğundan geliyor, elle yazılmıyor).
	Statements  []string `json:"statements"`
	ProbeErrors []string `json:"probeErrors,omitempty"`
	Generated   int64    `json:"generated"`
}

// ───────────────────────── saf yardımcılar ─────────────────────────

// messagingOpDimSelectRe — kanonik CREATE'in gövdesini ayıran ilk
// `AS SELECT`. Boşluk/yeni satır serbest; `AS operation` gibi gövde-içi
// alias'larla karışmaz çünkü ardından SELECT gelmesi şart.
var messagingOpDimSelectRe = regexp.MustCompile(`(?is)\bAS\s+(SELECT\b)`)

// messagingOpDimSelect — `adapt` edilmiş DDL listesinden MV olanını seçer
// ve `AS SELECT`'ten SONRASINI döndürür. SAF.
//
// Neden liste: küme kipinde adaptDDL İKİ ifade döner (MV + Distributed
// sarmalayıcı); sarmalayıcıda SELECT yok, MV'de var. Tek-node'da tek
// eleman gelir. "İlk elemanı al" demek küme kipinde sessizce yanlış
// ifadeyi seçme riskiydi.
func messagingOpDimSelect(adapted []string) (string, error) {
	for _, stmt := range adapted {
		if !strings.Contains(strings.ToUpper(stmt), "CREATE MATERIALIZED VIEW") {
			continue
		}
		loc := messagingOpDimSelectRe.FindStringSubmatchIndex(stmt)
		if loc == nil {
			continue
		}
		return strings.TrimSpace(stmt[loc[2]:]), nil
	}
	return "", fmt.Errorf("kanonik %s DDL'inde `AS SELECT` bulunamadı", messagingOpDimMV)
}

// messagingOpDimOnCluster — sihirbaz seçiminden ON CLUSTER metni. SAF.
// Boş cluster (tek-node) → "". Geçersiz ad → hata: ad DDL'e HAM giriyor.
func messagingOpDimOnCluster(cluster string) (string, error) {
	c := strings.TrimSpace(cluster)
	if c == "" {
		return "", nil
	}
	if !validRolloutLayerCluster(c) {
		return "", fmt.Errorf("cluster adı geçersiz %q — yalnız harf/rakam/_ . - (≤64)", c)
	}
	return " ON CLUSTER `" + c + "`", nil
}

// messagingOpDimStatements — yerinde geçişin TAM ifade listesi. SAF; testli.
//
// Sıra bilinçli ve tersine çevrilemez:
//
//  1. depo tablosu (uuid başına): ADD COLUMN + MODIFY ORDER BY — kolon
//     olmadan MODIFY QUERY kod 47 verir.
//  2. MV: MODIFY QUERY — `_local` artık `operation`'ı YAYINLAR.
//  3. Distributed sarmalayıcı: ADD COLUMN — çıplak addan okuma ve boot
//     probe'u ancak bundan sonra doğru cevap verir. 2'den ÖNCE basılırsa
//     sarmalayıcı olmayan bir kolonu ilan eder.
//
// `*Done` bayrakları TAMAMLANMIŞ parçaları atlamak içindir. DİKKAT:
// birleşik inner ALTER'ı "yarım" bir tabloya (kolon var, anahtar yok)
// yeniden basmak ÇALIŞMAZ — CH mevcut bir kolonu sıralama anahtarına
// kabul etmez; ön kontrol o durumu ayrı bir hata olarak bildirir
// (messagingOpDimHalfDone).
func messagingOpDimStatements(
	onCluster, mvStorage, wrapper string,
	inners []string,
	canonicalDDL string,
	adapt func(string) []string,
	innerDone map[string]bool,
	queryDone, wrapperDone bool,
) ([]string, error) {
	out := []string{}
	for _, inner := range inners {
		if innerDone[inner] {
			continue
		}
		out = append(out, "ALTER TABLE `"+inner+"`"+onCluster+
			" ADD COLUMN IF NOT EXISTS "+messagingOpDimCol+" String DEFAULT '' AFTER "+messagingOpDimAnchor+
			", MODIFY ORDER BY "+messagingOpDimKey)
	}
	if !queryDone {
		sel, err := messagingOpDimSelect(adapt(canonicalDDL))
		if err != nil {
			return nil, err
		}
		out = append(out, "ALTER TABLE "+mvStorage+onCluster+" MODIFY QUERY "+sel)
	}
	if wrapper != "" && !wrapperDone {
		out = append(out, "ALTER TABLE "+wrapper+onCluster+
			" ADD COLUMN IF NOT EXISTS "+messagingOpDimCol+" String DEFAULT '' AFTER "+messagingOpDimAnchor)
	}
	return out, nil
}

// messagingOpDimHalfDone — YENİDEN KOŞULAMAZ yarım durum: kolon eklenmiş
// ama sıralama anahtarına girmemiş. SAF; testli.
//
// Neden ayrı bir hüküm: ClickHouse `MODIFY ORDER BY`'a YALNIZ aynı ALTER
// içinde eklenen kolonu kabul eder ("Existing column … is used in the
// expression that was added to the sorting key"). Yani ADD COLUMN'u tek
// başına basmış bir kurulumda bizim birleşik ALTER'ımız da düşer —
// `ADD COLUMN IF NOT EXISTS` no-op'a düşer, MODIFY ORDER BY mevcut kolonu
// görür ve reddedilir. Çıkış yolu kolonu DROP edip yeniden koşmak; bunu
// operatöre ön kontrolde söylemek, apply ortasında ham CH hatası vermekten
// iyidir.
func messagingOpDimHalfDone(hasCol, keyHasCol bool) bool { return hasCol && !keyHasCol }

// messagingOpDimState — nesne durumlarından tek hüküm. SAF; testli.
// Tek bir "unknown" bütün kartı unknown yapar: probe hatası varken
// "missing" demek operatörü gereksiz bir DDL'e iter.
func messagingOpDimState(states []string) string {
	if len(states) == 0 {
		return "unknown"
	}
	ok, missing := 0, 0
	for _, st := range states {
		switch st {
		case "unknown":
			return "unknown"
		case "ok":
			ok++
		case "missing":
			missing++
		}
	}
	switch {
	case ok == len(states):
		return "done"
	case missing == len(states):
		return "missing"
	default:
		return "partial"
	}
}

// ───────────────────────── probe'lar ─────────────────────────

// messagingOpDimClusterFn — clusterAllReplicas(<küme>, system.<tbl>) ya da
// tek-node'da düz `system.<tbl>`.
func (s *Store) messagingOpDimSrc(sysTable string) string {
	if !s.clusterMode() {
		return "system." + sysTable
	}
	return fmt.Sprintf("clusterAllReplicas(`%s`, system.%s)",
		strings.ReplaceAll(s.cfg.ClusterName, "`", ""), sysTable)
}

func (s *Store) messagingOpDimCount(ctx context.Context, sql string, args ...any) (int, error) {
	var n uint64
	settings := " SETTINGS max_execution_time = 10"
	if s.clusterMode() {
		settings = " SETTINGS max_execution_time = 10, skip_unavailable_shards = 1"
	}
	if err := s.conn.QueryRow(ctx, sql+settings, args...).Scan(&n); err != nil {
		return 0, err
	}
	return int(n), nil
}

// messagingOpDimHosts — kümedeki host sayısı (tek-node = 1).
func (s *Store) messagingOpDimHosts(ctx context.Context) int {
	if !s.clusterMode() {
		return 1
	}
	var n uint64
	if err := s.conn.QueryRow(ctx, `SELECT count() FROM system.clusters WHERE cluster = ?`,
		strings.TrimSpace(s.cfg.ClusterName)).Scan(&n); err == nil && n > 0 {
		return int(n)
	}
	return 1
}

// messagingOpDimInnerKeyOK — hangi depo tablosunun sorting_key'i HER HOST'ta
// `operation` taşıyor (uuid → bool). İkinci dönüş "probe çalıştı mı":
// başarısız bir probe'u "anahtar yok" saymak, kolonu olan bir kurulumu
// yanlışlıkla YARIM (messagingOpDimHalfDone) ilan ederdi.
//
// DISTINCT değil count(): küme kipinde bir host anahtarı almış diğeri
// almamışsa DISTINCT "tamam" derdi ve eksik host sonsuza dek eksik
// kalırdı. count() < hosts → tamamlanmamış say.
func (s *Store) messagingOpDimInnerKeyOK(ctx context.Context, inners []string, hosts int) (map[string]bool, bool) {
	out := map[string]bool{}
	if len(inners) == 0 {
		return out, true
	}
	settings := " SETTINGS max_execution_time = 10"
	if s.clusterMode() {
		settings = " SETTINGS max_execution_time = 10, skip_unavailable_shards = 1"
	}
	rows, err := s.conn.Query(ctx, `
		SELECT name, count() AS c FROM `+s.messagingOpDimSrc("tables")+`
		WHERE database = currentDatabase() AND name IN (?) AND position(sorting_key, ?) > 0
		GROUP BY name`+settings, inners, messagingOpDimCol)
	if err != nil {
		return out, false
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		var c uint64
		if rows.Scan(&n, &c) == nil && n != "" && int(c) >= hosts {
			out[n] = true
		}
	}
	return out, true
}

// MessagingOpDimStatus — dört sözleşme parçasının host başına durumu.
func (s *Store) MessagingOpDimStatus(ctx context.Context) (MessagingOpDimStatusResult, error) {
	mvStorage := s.mvStorageName(messagingOpDimMV)
	out := MessagingOpDimStatusResult{
		Cluster:   strings.TrimSpace(s.cfg.ClusterName),
		MV:        mvStorage,
		Inner:     []string{},
		Objects:   []EntityLayerObjectStatus{},
		Generated: time.Now().Unix(),
	}
	hosts := s.messagingOpDimHosts(ctx)
	inners := s.mvInnerTablesCluster(ctx, mvStorage)
	out.Inner = append(out.Inner, inners...)
	out.Divergent = len(inners) > 1

	wrapperKind := "mv"
	if s.clusterMode() {
		wrapperKind = "distributed"
	}
	innerLabel := strings.Join(inners, ", ")
	if innerLabel == "" {
		innerLabel = "(çözülemedi)"
	}

	add := func(o EntityLayerObject, have int, err error) string {
		st := EntityLayerObjectStatus{EntityLayerObject: o, Hosts: hosts}
		if err != nil {
			st.Err = err.Error()
			st.State = "unknown"
		} else {
			st.HaveHosts = have
			st.State = entityLayerObjectState(have, hosts)
		}
		out.Objects = append(out.Objects, st)
		return st.State
	}

	var states []string
	colSrc, tblSrc := s.messagingOpDimSrc("columns"), s.messagingOpDimSrc("tables")

	// (a) depo tablosunda kolon. Backtick YOK: system.columns.table düz ad.
	if len(inners) == 0 {
		states = append(states, add(EntityLayerObject{Name: messagingOpDimCol, Kind: "column", Table: innerLabel},
			0, fmt.Errorf("depo tablosu (`.inner_id.<uuid>`) çözülemedi — MV yok ya da TO-table biçiminde")))
		states = append(states, add(EntityLayerObject{Name: "sorting_key ⊃ " + messagingOpDimCol, Kind: "table", Table: innerLabel},
			0, fmt.Errorf("depo tablosu çözülemedi")))
	} else {
		n, err := s.messagingOpDimCount(ctx, `
			SELECT count() FROM `+colSrc+`
			WHERE database = currentDatabase() AND table IN (?) AND name = ?`, inners, messagingOpDimCol)
		st := add(EntityLayerObject{Name: messagingOpDimCol, Kind: "column", Table: innerLabel}, n, err)
		out.InnerColumn = st == "ok"
		states = append(states, st)

		// (b) sıralama anahtarı. `operation` alt dize olarak aranır: bu
		// tablonun anahtarında başka `operation*` kolonu yok.
		kn, kerr := s.messagingOpDimCount(ctx, `
			SELECT count() FROM `+tblSrc+`
			WHERE database = currentDatabase() AND name IN (?) AND position(sorting_key, ?) > 0`,
			inners, messagingOpDimCol)
		kst := add(EntityLayerObject{Name: "sorting_key ⊃ " + messagingOpDimCol, Kind: "table", Table: innerLabel}, kn, kerr)
		out.KeyHasOperation = kst == "ok"
		states = append(states, kst)
	}

	// (c) MV'nin SELECT'i yeni mi (create_table_query ⊃ "AS operation").
	qn, qerr := s.messagingOpDimCount(ctx, `
		SELECT count() FROM `+tblSrc+`
		WHERE database = currentDatabase() AND name = ? AND engine = 'MaterializedView'
		  AND position(create_table_query, ?) > 0`, mvStorage, messagingOpDimQueryMark)
	qst := add(EntityLayerObject{Name: "MODIFY QUERY (" + messagingOpDimQueryMark + ")", Kind: "mv", Table: mvStorage}, qn, qerr)
	out.QueryHasOperation = qst == "ok"
	states = append(states, qst)

	// (d) ÇIPLAK ad — küme kipinde Distributed sarmalayıcı. BOOT PROBE'UNUN
	// BAKTIĞI YER: burada kolon yoksa deploy MV'yi düşürür.
	dn, derr := s.messagingOpDimCount(ctx, `
		SELECT count() FROM `+colSrc+`
		WHERE database = currentDatabase() AND table = ? AND name = ?`, messagingOpDimMV, messagingOpDimCol)
	dst := add(EntityLayerObject{Name: messagingOpDimCol, Kind: wrapperKind, Table: messagingOpDimMV}, dn, derr)
	out.MVColumn = dst == "ok"
	states = append(states, dst)
	out.BootWouldDrop = dst != "ok"

	out.State = messagingOpDimState(states)
	switch {
	case out.State == "unknown":
		out.Detail = "probe hatası — küme erişilemiyor ya da MV yok; ifadeler basılmadan önce ön kontrolü çalıştırın"
	case out.State == "done":
		out.Detail = "yerinde geçiş TAM: kolon, sıralama anahtarı, MV sorgusu ve çıplak ad hazır — boot geçişi no-op, geçmiş korunur"
	case out.Divergent:
		out.Detail = "eksik parça var VE depo tablosu host'lar arasında AYRIŞMIŞ (birden çok uuid) — her uuid için ayrı ALTER basılır, o uuid'nin bulunmadığı host'ta hata beklenir"
	default:
		out.Detail = "eksik parça var — deploy şu an MV'yi DROP+RECREATE eder ve 90 günlük messaging kovaları gider; deploy'dan ÖNCE uygulayın"
	}
	return out, nil
}

// MessagingOpDimPreflight — küme listesi, depo tablosu, üretilecek TAM SQL.
func (s *Store) MessagingOpDimPreflight(ctx context.Context, cluster string) (MessagingOpDimPreflightResult, error) {
	mvStorage := s.mvStorageName(messagingOpDimMV)
	out := MessagingOpDimPreflightResult{
		Clusters:         []string{},
		SuggestedCluster: strings.TrimSpace(s.cfg.ClusterName),
		MV:               mvStorage,
		Inner:            []string{},
		Statements:       []string{},
		Generated:        time.Now().Unix(),
	}
	if rows, err := s.conn.Query(ctx, `SELECT DISTINCT cluster FROM system.clusters ORDER BY cluster LIMIT 100`); err != nil {
		out.ProbeErrors = append(out.ProbeErrors, "system.clusters: "+err.Error())
	} else {
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err == nil && c != "" {
				out.Clusters = append(out.Clusters, c)
			}
		}
		rows.Close()
	}

	canonical := canonicalMVDDL(messagingOpDimMV)
	if canonical == "" {
		out.Supported = false
		out.Detail = "kanonik " + messagingOpDimMV + " DDL'i katalogda yok (canonicalMVs) — sürüm uyumsuz"
		return out, nil
	}

	// Küme kipinde ON CLUSTER hedefi ZORUNLU; tek-node'da yok sayılır.
	chosen := ""
	if s.clusterMode() {
		chosen = strings.TrimSpace(cluster)
		if chosen == "" {
			chosen = strings.TrimSpace(s.cfg.ClusterName)
		}
	}
	onCluster, err := messagingOpDimOnCluster(chosen)
	if err != nil {
		out.Supported = false
		out.Detail = err.Error()
		return out, nil
	}
	if s.clusterMode() && chosen != strings.TrimSpace(s.cfg.ClusterName) {
		out.ProbeErrors = append(out.ProbeErrors,
			fmt.Sprintf("seçilen küme %q Coremetry'nin yapılandırdığı %q ile aynı değil — probe'lar yapılandırılan küme üzerinde koştu",
				chosen, strings.TrimSpace(s.cfg.ClusterName)))
	}

	inners := s.mvInnerTablesCluster(ctx, mvStorage)
	out.Inner = append(out.Inner, inners...)
	out.Divergent = len(inners) > 1
	if len(inners) == 0 {
		out.Supported = false
		out.Detail = mvStorage + " için depo tablosu (`.inner_id.<uuid>`) çözülemedi — MV hiç yok ya da TO-table biçiminde; yerinde geçiş uygulanamaz"
		return out, nil
	}

	// AFTER çapası + kolon durumu + SIRALAMA ANAHTARI, uuid başına.
	//
	// innerDone İKİ koşulu birden ister. Yalnız kolona bakmak yarım kalmış
	// bir elle geçişte (ADD COLUMN basılmış, MODIFY ORDER BY basılmamış)
	// ALTER'ı atlar ve anahtar SONSUZA DEK eksik kalırdı — tam da boyutun
	// sessizce yok olduğu durum. Bu durum ayrıca YENİDEN KOŞULAMAZ (CH
	// mevcut kolonu sıralama anahtarına almaz) — ayrı hata olarak bildirilir.
	keyOK, keyProbeOK := s.messagingOpDimInnerKeyOK(ctx, inners, s.messagingOpDimHosts(ctx))
	if !keyProbeOK {
		out.ProbeErrors = append(out.ProbeErrors, "sıralama anahtarı okunamadı (system.tables) — yarım kalmış geçiş ayırt edilemez")
	}
	innerDone := map[string]bool{}
	for _, inner := range inners {
		types, ok := s.columnTypeCluster(ctx, inner, []string{messagingOpDimAnchor, messagingOpDimCol})
		if !ok {
			out.ProbeErrors = append(out.ProbeErrors, "kolon tipleri okunamadı: "+inner)
			continue
		}
		if _, has := types[messagingOpDimAnchor]; !has {
			out.ProbeErrors = append(out.ProbeErrors,
				fmt.Sprintf("%s: `%s` çapa kolonu yok — `AFTER %s` düşer", inner, messagingOpDimAnchor, messagingOpDimAnchor))
		}
		_, hasCol := types[messagingOpDimCol]
		if keyProbeOK && messagingOpDimHalfDone(hasCol, keyOK[inner]) {
			out.ProbeErrors = append(out.ProbeErrors, fmt.Sprintf(
				"%s: `%s` kolonu VAR ama sıralama anahtarında YOK — yarım kalmış geçiş. CH mevcut bir kolonu sıralama anahtarına EKLEYEMEZ (yalnız aynı ALTER'da eklenen kolonu), yani ifadeyi yeniden basmak da düşer. Çıkış yolu: ALTER TABLE `%s` DROP COLUMN %s, sonra sihirbazı yeniden koşun",
				inner, messagingOpDimCol, inner, messagingOpDimCol))
			continue
		}
		if hasCol && keyOK[inner] {
			innerDone[inner] = true
		}
	}

	st, serr := s.MessagingOpDimStatus(ctx)
	if serr != nil {
		out.ProbeErrors = append(out.ProbeErrors, "durum: "+serr.Error())
	}
	wrapper := ""
	if s.clusterMode() {
		wrapper = messagingOpDimMV
	}

	stmts, err := messagingOpDimStatements(onCluster, mvStorage, wrapper, inners,
		canonical, s.adaptDDL, innerDone, st.QueryHasOperation, st.MVColumn)
	if err != nil {
		out.Supported = false
		out.Detail = err.Error()
		return out, nil
	}
	out.Statements = stmts
	out.AlreadyDone = len(stmts) == 0 && st.State == "done"
	out.Supported = true
	switch {
	case out.AlreadyDone:
		out.Detail = "zaten uygulanmış — boot geçişi no-op, geçmiş korunur"
	case out.Divergent:
		out.Detail = fmt.Sprintf("uygulanabilir (%d ifade); DİKKAT: depo tablosu AYRIŞMIŞ (%d uuid) — her uuid için ayrı ALTER basılır, o uuid'nin bulunmadığı host'ta hata beklenir ve ilk hatada durulur",
			len(stmts), len(inners))
	default:
		out.Detail = fmt.Sprintf("uygulanabilir (%d ifade) — geri alma YOK: MODIFY ORDER BY anahtarı yalnız genişletir", len(stmts))
	}
	return out, nil
}

// MessagingOpDimApply — ön kontrolü KENDİ koşar (istemciye güvenmez), sonra
// ilk hatada durur. Geri alma yok; dosya başlığındaki gerekçe.
func (s *Store) MessagingOpDimApply(ctx context.Context, cluster string) []RollupStmtResult {
	pre, err := s.MessagingOpDimPreflight(ctx, cluster)
	if err != nil {
		return []RollupStmtResult{{Head: "ön koşul", Err: err.Error()}}
	}
	if !pre.Supported {
		return []RollupStmtResult{{Head: "ön koşul", Err: pre.Detail}}
	}
	if pre.AlreadyDone || len(pre.Statements) == 0 {
		return []RollupStmtResult{{Head: "zaten uygulanmış — kolon, sıralama anahtarı ve MV sorgusu yerinde", OK: true}}
	}
	return s.execStmtsStopOnError(ctx, pre.Statements)
}
