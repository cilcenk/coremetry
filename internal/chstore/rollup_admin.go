// rollup_admin.go — admin-tetiklemeli ROLLUP KURULUM SİHİRBAZI (v0.9.770).
//
// Neden var: 0001 (dar span rollup'ları) + 0003 (metrik rollup'ları)
// prod'da ELLE koşulan SQL dosyalarıydı. Okuma tarafı (rollupselect.go,
// metric_rollup_read.go) çoktan gemide ve tablo yoksa ham yola düşüyor —
// yani rollup'lar "kurulmadıysa yavaş, kurulduysa hızlı" bir katman. Tek
// eksik, operatörün 300 satır DDL'i doğru cluster adıyla, doğru sırada,
// gece yarısı elle yapıştırması gerekiyor olmasıydı.
//
// OTOMATİK DEĞİL — ve bu bilinçli. Boot'ta ASLA koşmaz:
//   · MV kurmak ingest yoluna yazar; bozuk bir MV write_failed üretir ve
//     ingest'i düşürür (v0.8.185/186 sınıfı). Bunun kararı operatörün.
//   · ON CLUSTER DDL dağıtık kuyruğa girer; rolling deploy sırasında
//     N pod'un aynı DDL'i yarıştırması kuyruğu tıkar (v0.9.613 vakası).
// Bu yüzden tek giriş noktası admin'in tıkladığı buton.
//
// Kapsam DIŞI: tek-düğüm kurulumlar. DDL dosyaları `ON CLUSTER` +
// `Distributed(...)` + `Replicated*MergeTree` üzerine kurulu; tek node'da
// bunların hiçbiri anlamlı değil ve _local tablolar da yok. Preflight bunu
// Supported=false ile SÖYLER — sessizce yarım uygulamak en kötü sonuç.
package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/migrations"
)

// rollupClusterToken — DDL dosyalarında yazılı ŞABLON cluster adı.
// 0001/0003 bu adı hem `ON CLUSTER x` hem `Distributed(x, ...)` içinde
// kullanıyor; AdaptRollupDDL ikisini de tek geçişte çevirir.
const rollupClusterToken = "uptrace_all"

// rollupNarrowTables / rollupMetricsTables — 0001 ve 0003'ün ürettiği
// OKUNAN tablolar (Distributed sarmalayıcılar; _local'lar bunların
// altında). Status bu adlar üzerinden rapor verir çünkü okuma katmanı
// (rollupselect.go) da bu adları sorgular.
var rollupNarrowTables = []string{
	"rollup_spans_narrow_10s",
	"rollup_spans_narrow_1m",
	"rollup_spans_narrow_5m",
	"rollup_spans_narrow_1h",
}

var rollupMetricsTables = []string{
	"rollup_metrics_1m",
	"rollup_metrics_5m",
	"rollup_metrics_1h",
}

// rollupRouteTables — 0008: route (endpoint) kırılımlı metrik zinciri.
// 0003'ün kardeşi, halefi DEĞİL: ikisi yan yana yaşar ve okuma katmanı
// sorgu şekline göre birini seçer (metric_rollup_route_read.go).
var rollupRouteTables = []string{
	"rollup_metrics_route_1m",
	"rollup_metrics_route_5m",
	"rollup_metrics_route_1h",
}

// rollupNarrowMVs / rollupMetricsMVs — geri alma hedefleri. YALNIZ
// MV'ler: MV'yi düşürmek yazımı keser (ingest kurtarma), tablolar ve
// içlerindeki veri KALIR. Tabloyu da düşürmek geri alınamaz veri kaybı
// olurdu ve panik anında istenen şey o değil.
//
// Sıra en ince taneden kabaya: 10s MV'si spans_local'dan besleniyor,
// üsttekiler alttakinden. Önce tabanı kesmek kaskadın geri kalanını
// zaten kurutur.
var rollupNarrowMVs = []string{
	"mv_rollup_spans_narrow_10s",
	"mv_rollup_spans_narrow_1m",
	"mv_rollup_spans_narrow_5m",
	"mv_rollup_spans_narrow_1h",
}

var rollupMetricsMVs = []string{
	"mv_rollup_metrics_1m",
	"mv_rollup_metrics_5m",
	"mv_rollup_metrics_1h",
}

var rollupRouteMVs = []string{
	"mv_rollup_metrics_route_1m",
	"mv_rollup_metrics_route_5m",
	"mv_rollup_metrics_route_1h",
}

// rollupRequiredCols — preflight'ın DESCRIBE ile aradığı kolonlar.
// Kaynak: 0001/0003'ün MV SELECT'lerinde GEÇEN kolonlar. Eksik biri
// varsa DDL uygulama ANINDA patlar; preflight bunu ÖNCEDEN söyler ki
// operatör yarım uygulanmış bir zincirle kalmasın.
//
// v0.9.777 — metric_points_local listesi 0008'in MV SELECT'ini de
// kapsayacak şekilde genişledi: attr_keys/attr_values (route_raw),
// res_keys/res_values (service_key), sum_value + count (obs-ağırlıklı
// avg — v0.9.776 düzeltmesinin şeması). Bu satırın varlık sebebi
// yukarıdaki yorum: "preflight yeşil deyip apply'da patlamak YASAK".
var rollupRequiredCols = map[string][]string{
	"spans_local": {"service_name", "kind", "status_code", "duration"},
	"metric_points_local": {
		"metric", "service_name", "instrument", "unit", "value",
		"bucket_bounds", "bucket_counts",
		"attr_keys", "attr_values", "res_keys", "res_values",
		"sum_value", "count",
	},
}

// ─────────────────────────── saf çekirdekler ───────────────────────────

// SplitSQLStatements — DDL metnini tek tek çalıştırılabilir ifadelere
// böler. SAF: canlı CH olmadan test edilebilir.
//
// İki adım, bu sırayla:
//  1. SATIR BAZINDA `--` yorumlarını at. Neden satır bazında ve neden
//     ÖNCE: 0001/0003 başlıklarında `;` içeren yorum satırları var
//     (örnek SQL parçaları). Önce bölseydik yorumun içindeki `;` sahte
//     bir sınır üretirdi.
//  2. `;` ile böl, boşları ele.
//
// Neden tam bir SQL lexer'ı DEĞİL: bu iki dosya YALNIZ CREATE TABLE /
// CREATE MATERIALIZED VIEW içerir — string literal içinde `;` yok,
// `$$` gövdesi yok, `/* */` yok. Genel amaçlı bir bölücü yazmak
// doğrulanamayan bir yüzey açardı; bu bölücü gömülü dosyalarla
// test ediliyor (rollup_admin_test.go).
func SplitSQLStatements(src string) []string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	var out []string
	for _, part := range strings.Split(b.String(), ";") {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// AdaptRollupDDL — şablon cluster adını gerçek küme adıyla değiştirir.
// SAF.
//
// TEK bir ReplaceAll yeterli ve doğru: token hem `ON CLUSTER uptrace_all`
// hem `ENGINE = Distributed(uptrace_all, currentDatabase(), ...)` içinde
// AYNI düz metin olarak geçiyor. İki ayrı regex yazmak (bir ON CLUSTER
// için, bir engine argümanı için) birinin unutulması riskini getirirdi —
// ki 0001'de 8 ON CLUSTER + 4 Distributed argümanı var.
//
// Boş cluster ile çağrılmaz (RollupApply önden reddeder); saflığı bozmamak
// için burada guard yok.
func AdaptRollupDDL(src, cluster string) string {
	return strings.ReplaceAll(src, rollupClusterToken, cluster)
}

// rollupRollbackStatements — geri alma DDL'leri. SAF (test pinliyor).
//
// `SYNC`: DROP'un asenkron değil, dönmeden önce tamamlanması için.
// Panik anında "düşürdüm ama hâlâ yazıyor" en istenmeyen belirsizlik.
// Cluster boşsa ON CLUSTER'sız tek-node biçimi üretilir — sihirbaz
// tek-düğümde kurulum YAPMAZ ama elle kurulmuş bir zincirin geri
// alınmasını engellemenin bir nedeni yok.
func rollupRollbackStatements(cluster, target string) []string {
	var names []string
	switch target {
	case "narrow":
		names = rollupNarrowMVs
	case "metrics":
		names = rollupMetricsMVs
	case "route":
		names = rollupRouteMVs
	default: // "both"
		// v0.9.777 — "both" 0001+0003 olarak KALIR, 0008'i KAPSAMAZ.
		// Geriye uyum: bugüne kadar "Her ikisi"ni seçmiş bir operatörün
		// geri-alma düğmesi, o operatörün hiç kurmadığı bir zinciri de
		// düşürmeye çalışmamalı. 0008 ayrı bir satır ("route").
		names = append(append([]string{}, rollupNarrowMVs...), rollupMetricsMVs...)
	}
	onCluster := ""
	if c := strings.TrimSpace(cluster); c != "" {
		onCluster = " ON CLUSTER " + c
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, "DROP TABLE IF EXISTS "+n+onCluster+" SYNC")
	}
	return out
}

// rollupSources — target → gömülü dosya adları. Sıra ÖNEMLİ: "both"da
// önce span ailesi, sonra metrik ailesi (bağımsızlar, ama hata hâlinde
// operatörün gördüğü sıra deterministik olmalı).
func rollupSources(target string) ([]string, error) {
	switch target {
	case "narrow":
		return []string{"0001_rollup_narrow.sql"}, nil
	case "metrics":
		return []string{"0003_rollup_metrics.sql"}, nil
	case "route":
		return []string{"0008_rollup_metrics_route.sql"}, nil
	case "both":
		return []string{"0001_rollup_narrow.sql", "0003_rollup_metrics.sql"}, nil
	}
	return nil, fmt.Errorf("bilinmeyen hedef %q — narrow | metrics | route | both bekleniyor", target)
}

// stmtHead — sonuç listesinde gösterilecek kısa ifade özeti. Tam DDL
// 40 satır; operatörün listede görmesi gereken tek şey "hangi nesne".
func stmtHead(stmt string) string {
	s := strings.Join(strings.Fields(stmt), " ")
	if len(s) > 90 {
		return s[:90]
	}
	return s
}

// ─────────────────────────── sonuç tipleri ───────────────────────────

// RollupStmtResult — tek bir DDL ifadesinin sonucu.
type RollupStmtResult struct {
	Head string `json:"head"`
	OK   bool   `json:"ok"`
	Err  string `json:"err,omitempty"`
}

// RollupPreflightResult — "bu küme bu DDL'i kaldırır mı?" hükmü.
type RollupPreflightResult struct {
	// Clusters — system.clusters'ta tanımlı adlar. UI bunu <select>'e
	// döker; ELLE yazdırmıyoruz çünkü yanlış cluster adı DDL'i kuyruğa
	// sokup orada süresiz bekletiyor (v0.9.613).
	Clusters []string `json:"clusters"`
	// SuggestedCluster — Coremetry'nin KENDİ yapılandırmasındaki ad.
	// Çoğu kurulumda doğru cevap bu; UI'da ön-seçili gelir.
	SuggestedCluster string `json:"suggestedCluster,omitempty"`

	SpansLocal        bool `json:"spansLocal"`
	MetricPointsLocal bool `json:"metricPointsLocal"`
	// NarrowInstalled / MetricsInstalled — zincirin TABANI zaten var mı.
	// True ise "Kur" bir no-op'tur (DDL'lerin tamamı IF NOT EXISTS) ama
	// operatörün bunu ÖNCEDEN bilmesi gerekir.
	NarrowInstalled  bool `json:"narrowInstalled"`
	MetricsInstalled bool `json:"metricsInstalled"`
	// RouteInstalled — 0008 (route kırılımlı metrik zinciri) tabanı var mı.
	RouteInstalled bool `json:"routeInstalled"`

	// MissingColumns — "tablo.kolon" biçiminde. Boş = tam.
	MissingColumns []string `json:"missingColumns"`
	// ProbeErrors — okunamayan probe'lar. Supported hükmünü
	// SERTLEŞTİRİR: emin olamadığımızda "uygulanabilir" demeyiz.
	ProbeErrors []string `json:"probeErrors,omitempty"`

	Supported bool   `json:"supported"`
	Detail    string `json:"detail"`
	Generated int64  `json:"generated"`
}

// RollupTableStatus — tek bir rollup tablosunun canlı durumu.
type RollupTableStatus struct {
	Table  string `json:"table"`
	Family string `json:"family"` // "narrow" | "metrics" | "route"
	Exists bool   `json:"exists"`
	Rows   uint64 `json:"rows"`
	// MinTsMs — en eski satır (unix ms). 0 = tablo boş ya da okunamadı.
	// Kurulumdan sonra ilk kanıt bu: MV yazıyorsa dakikalar içinde dolar.
	MinTsMs int64  `json:"minTsMs"`
	Err     string `json:"err,omitempty"`
}

// RollupStatusResult — sihirbaz kartının özet satırı.
type RollupStatusResult struct {
	Cluster   string              `json:"cluster"`
	Tables    []RollupTableStatus `json:"tables"`
	Generated int64               `json:"generated"`
}

// ─────────────────────────── store metodları ───────────────────────────

// tableExists — `EXISTS TABLE x`. Ad SABİT bir iç listeden geliyor
// (kullanıcı girdisi değil), bu yüzden interpolasyon güvenli.
func (s *Store) tableExists(ctx context.Context, name string) (bool, error) {
	var v uint8
	if err := s.conn.QueryRow(ctx, "EXISTS TABLE "+name).Scan(&v); err != nil {
		return false, err
	}
	return v == 1, nil
}

// describeCols — DESCRIBE TABLE'ın kolon adları kümesi.
func (s *Store) describeCols(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := s.conn.Query(ctx, "DESCRIBE TABLE "+table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := rows.Columns()
	out := map[string]bool{}
	for rows.Next() {
		// DESCRIBE'ın kolon SAYISI CH sürümüne göre değişir (name, type,
		// default_type, default_expression, comment, codec_expression,
		// ttl_expression, +is_subcolumn). Sabit sayıda hedefe taramak
		// sürüm kırılganlığı olurdu; dönen kolon sayısı kadar string
		// hedef üretip yalnız ilkini (name) kullanıyoruz.
		dest := make([]any, len(cols))
		vals := make([]string, len(cols))
		for i := range vals {
			dest[i] = &vals[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if len(vals) > 0 {
			out[vals[0]] = true
		}
	}
	return out, rows.Err()
}

// RollupPreflight — kurulum ÖNCESİ hüküm. Hiçbir şey yazmaz.
func (s *Store) RollupPreflight(ctx context.Context) (RollupPreflightResult, error) {
	out := RollupPreflightResult{
		Clusters:         []string{},
		MissingColumns:   []string{},
		SuggestedCluster: strings.TrimSpace(s.cfg.ClusterName),
		Generated:        time.Now().UnixMilli(),
	}

	// ── küme listesi ──
	rows, err := s.conn.Query(ctx,
		`SELECT DISTINCT cluster FROM system.clusters ORDER BY cluster LIMIT 100`)
	if err != nil {
		out.ProbeErrors = append(out.ProbeErrors, "system.clusters: "+err.Error())
	} else {
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				out.ProbeErrors = append(out.ProbeErrors, "system.clusters tarama: "+err.Error())
				break
			}
			out.Clusters = append(out.Clusters, c)
		}
		rows.Close()
	}

	// ── tablo varlıkları ──
	probe := func(name string) bool {
		ok, err := s.tableExists(ctx, name)
		if err != nil {
			out.ProbeErrors = append(out.ProbeErrors, name+": "+err.Error())
			return false
		}
		return ok
	}
	out.SpansLocal = probe("spans_local")
	out.MetricPointsLocal = probe("metric_points_local")
	out.NarrowInstalled = probe(rollupNarrowTables[0])
	out.MetricsInstalled = probe(rollupMetricsTables[0])
	out.RouteInstalled = probe(rollupRouteTables[0])

	// ── kolonlar ── (yalnız var olan tablolar için; yoksa DESCRIBE patlar
	// ve "eksik kolon" listesi anlamsız gürültüye döner)
	checkCols := func(table string) {
		cols, err := s.describeCols(ctx, table)
		if err != nil {
			out.ProbeErrors = append(out.ProbeErrors, "DESCRIBE "+table+": "+err.Error())
			return
		}
		for _, want := range rollupRequiredCols[table] {
			if !cols[want] {
				out.MissingColumns = append(out.MissingColumns, table+"."+want)
			}
		}
	}
	if out.SpansLocal {
		checkCols("spans_local")
	}
	if out.MetricPointsLocal {
		checkCols("metric_points_local")
	}

	// ── hüküm ──
	out.Supported = out.SpansLocal && out.MetricPointsLocal &&
		len(out.MissingColumns) == 0 && len(out.Clusters) > 0

	switch {
	case !out.SpansLocal && !out.MetricPointsLocal:
		out.Detail = "Dağıtık şema bulunamadı — manuel uyarlama gerekir. " +
			"0001/0003 `ON CLUSTER` + `Distributed(...)` + `Replicated*MergeTree` " +
			"üzerine kurulu ve kaynak olarak spans_local / metric_points_local " +
			"okuyor; tek düğümlü kurulumda bu tabloların hiçbiri yok. Sihirbaz " +
			"burada durur — yarım uygulanmış bir zincir kurulmamış olandan kötüdür."
	case !out.SpansLocal:
		out.Detail = "spans_local yok — dar span rollup ailesi (0001) uygulanamaz."
	case !out.MetricPointsLocal:
		out.Detail = "metric_points_local yok — metrik rollup ailesi (0003) uygulanamaz."
	case len(out.MissingColumns) > 0:
		out.Detail = "Kaynak tablolarda eksik kolon var (" +
			strings.Join(out.MissingColumns, ", ") + "). MV SELECT'i bu kolonları " +
			"okuyor; eksikken DDL uygulama anında patlar."
	case len(out.Clusters) == 0:
		out.Detail = "system.clusters boş — `ON CLUSTER` DDL'i gönderilecek bir küme " +
			"tanımı yok. CH sunucusunda <remote_servers> bloğunu doğrulayın."
	default:
		out.Detail = "Ön kontrol temiz. DDL'lerin tamamı IF NOT EXISTS — " +
			"zaten kurulu bir zincir üzerinde tekrar koşmak güvenlidir."
		if out.NarrowInstalled || out.MetricsInstalled || out.RouteInstalled {
			out.Detail += " (Zincirin bir bölümü ZATEN kurulu.)"
		}
	}
	// Probe hatası varsa hüküm SERT: okuyamadığımız bir şeyi "tamam"
	// diye raporlamak, tam da bu sihirbazın önlemesi gereken hata.
	if len(out.ProbeErrors) > 0 && out.Supported {
		out.Supported = false
		out.Detail = "Ön kontrol probe'ları eksik tamamlandı (" +
			strings.Join(out.ProbeErrors, " · ") + ") — hüküm verilemedi. Yeniden deneyin."
	}
	return out, nil
}

// RollupApply — DDL'i SIRAYLA koşar. İlk hatada DURUR.
//
// Neden ilk hatada duruyoruz: zincir kaskad. 10s tablosu yaratılamadıysa
// 1m MV'sinin `FROM rollup_spans_narrow_10s_local`'ı zaten patlayacak;
// devam etmek operatöre 12 satır hata döker ve GERÇEK ilk nedeni gömer.
//
// SETTINGS eklemiyoruz: bunlar DDL, `max_execution_time` bir SELECT
// ayarı. ON CLUSTER DDL'in kendi zaman aşımı distributed_ddl_task_timeout.
func (s *Store) RollupApply(ctx context.Context, cluster, target string) []RollupStmtResult {
	c := strings.TrimSpace(cluster)
	if c == "" {
		return []RollupStmtResult{{
			Head: "ön koşul",
			Err:  "cluster adı zorunlu — DDL `ON CLUSTER` yazıyor, boş ad geçersiz SQL üretir",
		}}
	}
	files, err := rollupSources(target)
	if err != nil {
		return []RollupStmtResult{{Head: "ön koşul", Err: err.Error()}}
	}

	var out []RollupStmtResult
	for _, f := range files {
		raw, err := migrations.FS.ReadFile(f)
		if err != nil {
			out = append(out, RollupStmtResult{Head: f, Err: "gömülü dosya okunamadı: " + err.Error()})
			return out
		}
		for _, stmt := range SplitSQLStatements(AdaptRollupDDL(string(raw), c)) {
			r := RollupStmtResult{Head: stmtHead(stmt)}
			if err := s.conn.Exec(ctx, stmt); err != nil {
				r.Err = err.Error()
				out = append(out, r)
				return out
			}
			r.OK = true
			out = append(out, r)
		}
	}
	return out
}

// RollupRollback — YALNIZ MV'leri düşürür. Tablolar ve veri KALIR.
//
// Bu "kurulumu geri al" değil, "yazımı kes" düğmesi: bozuk bir MV
// ingest'i düşürüyorsa (write_failed tırmanıyorsa) tek acil eylem
// yazımı durdurmaktır. Tabloları da düşürmek geri alınamaz veri kaybı
// olur ve panik anında istenen şey o değil — tablolar boş dururken
// zararsız, okuma katmanı zaten boş tabloyu görüp ham yola düşüyor.
func (s *Store) RollupRollback(ctx context.Context, cluster, target string) []RollupStmtResult {
	stmts := rollupRollbackStatements(cluster, target)
	out := make([]RollupStmtResult, 0, len(stmts))
	for _, stmt := range stmts {
		r := RollupStmtResult{Head: stmtHead(stmt)}
		if err := s.conn.Exec(ctx, stmt); err != nil {
			r.Err = err.Error()
			out = append(out, r)
			// Geri almada İLK HATADA DURMUYORUZ (apply'ın tersine):
			// amaç yazımı kesmek ve bir MV'nin düşmemesi diğerlerinin
			// düşmesini engellememeli. Yarım kesilmiş bir kaskad, hiç
			// kesilmemişten iyidir.
			continue
		}
		r.OK = true
		out = append(out, r)
	}
	return out
}

// RollupStatus — canlı durum. Kurulum sonrası ilk kanıt burada okunur:
// satır sayısı artıyorsa MV besliyor demektir.
func (s *Store) RollupStatus(ctx context.Context) (RollupStatusResult, error) {
	out := RollupStatusResult{
		Cluster:   strings.TrimSpace(s.cfg.ClusterName),
		Tables:    []RollupTableStatus{},
		Generated: time.Now().UnixMilli(),
	}
	fams := []struct {
		name   string
		tables []string
	}{
		{"narrow", rollupNarrowTables},
		{"metrics", rollupMetricsTables},
		{"route", rollupRouteTables},
	}
	for _, fam := range fams {
		for _, t := range fam.tables {
			st := RollupTableStatus{Table: t, Family: fam.name}
			ok, err := s.tableExists(ctx, t)
			if err != nil {
				st.Err = err.Error()
				out.Tables = append(out.Tables, st)
				continue
			}
			st.Exists = ok
			if !ok {
				out.Tables = append(out.Tables, st)
				continue
			}
			// min(ts) birincil anahtarın ÖNEKİ değil (ORDER BY
			// service_name ile başlıyor) → tek kolon taraması. 3 sn
			// tavan: bu bir teşhis okuması, kartın açılmasını
			// geciktirmemeli. Aşarsa satır Err ile döner ve kart
			// "okunamadı" der — sıfır göstermez.
			var cnt uint64
			var minTs int64
			err = s.conn.QueryRow(ctx,
				"SELECT count(), toInt64(toUnixTimestamp(min(ts))) FROM "+t+
					" SETTINGS max_execution_time = 3").Scan(&cnt, &minTs)
			if err != nil {
				st.Err = err.Error()
				out.Tables = append(out.Tables, st)
				continue
			}
			st.Rows = cnt
			if cnt > 0 && minTs > 0 {
				st.MinTsMs = minTs * 1000
			}
			out.Tables = append(out.Tables, st)
		}
	}
	return out, nil
}
