package chstore

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"
)

// v0.8.243 — granularity slice B (operator: "metric charts aren't as
// smooth as Grafana"). Requesting buckets FINER than a metric's actual
// export cadence doesn't add resolution — it adds holes: a 10s-exported
// gauge drawn at step=1s is 90% empty buckets rendered as sawtooth or
// gaps. Grafana solves this with $__rate_interval (never below the
// scrape interval); Coremetry's equivalent is this observed-export-
// interval clamp: the effective step never drops below what the metric
// actually ships.

// metricIvEntry is one cached probe result. iv == 0 means "couldn't
// infer" (young/sparse metric, probe error) → no clamp applied.
type metricIvEntry struct {
	at time.Time
	iv int
}

const (
	metricIvTTL         = 60 * time.Second
	metricIvProbeWindow = 10 * time.Minute
	// metricIvMinPoints — below this many points the interval estimate
	// is noise (a metric that just started reporting); don't clamp.
	metricIvMinPoints = 5
	// metricIvMaxSeconds — an inferred interval above this is treated
	// as "effectively sparse"; clamping a chart to >1h buckets would
	// fight the operator, and such metrics look fine unclamped.
	metricIvMaxSeconds = 3600
)

// exportIntervalQuantile — clamp tabanı = metriğin YAYIM TİKİ.
//
// v0.9.688 — v0.9.672'DE YANLIŞ BÜYÜKLÜĞÜ OPTİMİZE ETMİŞİM, geri
// alınıyor. Operatör: "Client 1 sn, prometheus 60s tutuyormuş, ancak
// metrics 1 sn" — yani veri saniyede bir geliyor ama grafik dakikalık
// çözünürlükte çiziliyordu.
//
// ÖLÇÜM (yerel, http.server.duration, 20 dk) iki büyüklüğü ayırdı:
//
//	seri BAŞINA aralık p90        : 137.5 s   ← v0.9.672'nin ölçtüğü
//	GERÇEK yayım tiki (farklı damga): 4.24 s   ← olması gereken
//	                                  → 32× fark
//
// FARKIN SEBEBİ: delta temporality'de bir seri YALNIZ o aralıkta gözlem
// varsa nokta üretir. Nadir bir route (attr kombinasyonu) 137 saniyede
// bir nokta verir — ama bu METRİĞİN YAYIM ARALIĞI DEĞİL, O ROUTE'UN
// TRAFİK SEYREKLİĞİ. Seri başına aralığın yüksek kantili seyrekliği
// ölçüyor, tiki değil.
//
// v0.9.672 "delikleri kapatmak" için p90'a çekmişti. Delikler gerçekten
// azaldı — ama seyrek serilerin deliği GERÇEK veri yokluğu; onu adımı
// 32× kabalaştırarak "kapatmak", herkesin çözünürlüğünü yok etmek
// demek. Yanlış takas.
//
// P10 SEÇİLDİ, min değil: tik yoğun serilerden okunur, ama tek bir
// anormal seri (örn. yeni doğmuş pod, iki noktalı seri) tabanı sıfıra
// çekmesin. Düşük kantil, yoğun uçtan robust bir okuma.
//
// KABUL EDİLEN SONUÇ: seyrek seriler yine delikli çizilir. Bu DOĞRU —
// o dilimlerde gerçekten veri yok, ve olmayan veriyi kalın kovalara
// yayarak gizlemek bu kod tabanının reddettiği şey.
func exportIntervalQuantile(ivSec float64, series uint64) int {
	if series == 0 || ivSec <= 0 {
		return 0
	}
	iv := int(math.Round(ivSec))
	if iv < 1 {
		iv = 1
	}
	if iv > metricIvMaxSeconds {
		return 0
	}
	return iv
}

// clampStepToExport lifts a requested step to the metric's observed
// export interval. Only ever RAISES the step — a coarse request stays
// coarse. Pure.
func clampStepToExport(step, exportIv int) int {
	if exportIv > step {
		return exportIv
	}
	return step
}

// metricExportIntervalQuantileSQL — v0.9.672. Seri BAŞINA aralığı
// hesaplar ve P90'ını döndürür (+ seri sayısı).
//
// CH sınırları korunuyor (CLAUDE.md sert kısıtı): zaman-sınırlı WHERE,
// iç sorguda LIMIT, max_execution_time. Alt sorgu seri sayısıyla
// sınırlı — metric_points'in kendisi taranmıyor, GROUP BY sonucu.
func metricExportIntervalQuantileSQL(inner whereClause) string {
	return `
		SELECT quantileExact(0.1)(iv) AS iv90, count() AS series FROM (
			SELECT count() AS cnt,
			       dateDiff('second', min(time), max(time)) AS spanSec,
			       spanSec / greatest(cnt - 1, 1) AS iv
			FROM metric_points
			` + inner.sql() + `
			GROUP BY service_name, host_name, attr_values
			HAVING cnt >= ` + strconv.Itoa(metricIvMinPoints) + ` AND spanSec > 0
			LIMIT 20000
		)
		SETTINGS max_execution_time = 5`
}

// metricExportIntervalPooledSQL — HAVUZLANMIŞ varış temposu: metriğin
// bir bütün olarak ne sıklıkta nokta ürettiği (farklı zaman damgaları).
//
// v0.9.689 — DOĞRU TABAN, GRAFİĞİN NE ÇİZDİĞİNE BAĞLI. Bu ayrım
// ölçümle ortaya çıktı:
//
//	havuzlanmış tempo (farklı damga) :  4.24 s
//	seri başına aralık p10           : 41.18 s   → 9.7× fark
//
// Sebep: 20 dk'lık pencerede 282 farklı damgaya 301 seri düşüyor
// (damga başına ~5.7). Seriler KAYMALI yayımlıyor, yani hiçbir serinin
// kendi temposu havuzlanmış tempoya eşit değil — ve olmamalı.
//
// HANGİSİ DOĞRU:
//   - TOPLULAŞTIRILMIŞ çizgi (GroupBy YOK): her kovaya birçok seri
//     katkı veriyor, ince adım delik ÜRETMEZ → havuzlanmış tempo.
//     Service Overview'ın metrik panelleri bu şekilde (Aggregation:sum,
//     GroupBy boş).
//   - SERİ BAŞINA çizgi (GroupBy VAR): her çizgi kendi serisinin
//     temposuna bağlı → seri başına düşük kantil.
//
// v0.9.688 ikisini ayırmadan tek bir ölçüye bağlıydı; operatörün
// "client 1 sn ama metrics dakikalık" gözlemi bu ayrımın eksikliğiydi.
func metricExportIntervalPooledSQL(inner whereClause) string {
	return `
		SELECT
		    dateDiff('second', min(time), max(time)) / greatest(uniqExact(time) - 1, 1) AS iv90,
		    count() AS series
		FROM metric_points
		` + inner.sql() + `
		SETTINGS max_execution_time = 5`
}

// exportIntervalProbeWhere — probun WHERE'i (saf, testli).
//
// v0.9.687 — FİLTRELER PROBA DA İNİYOR. Bu, v0.9.669'un temporality
// düzeltmesiyle AYNI SINIF: orada düzelttim, burada unuttum.
//
// Operatör-bildirimi: metrik throughput paneli 30 dakikalık pencerede
// DOĞRU, 5 dakikalıkta düz bir çizgi ve değerler ~20× yüksek.
//
// Mekanizma: adım metriğin dışa-aktarım aralığına yükseltiliyor
// (clampStepToExport) ama aralık TÜM servislerin serilerinden
// hesaplanıyordu. Eşleşen servis 60s'de bir yayarken tüm-metrik p90'ı
// küçük çıkıyor → clamp devreye girmiyor → dar pencerede auto-step
// 5s'e iniyor → 60s'de biriken delta 5s'e bölünüyor (oran ~12× şişer)
// ve kovaların çoğu boş kaldığı için grafik tek düz parçaya iner.
//
// GENİŞ pencerede auto-step zaten büyük olduğu için clamp'in devreye
// girmemesi FARK ETMİYORDU — hata yalnız dar pencerede görünür. 30
// dakikada doğru, 5 dakikada bozuk olmasının sebebi tam olarak bu.
func exportIntervalProbeWhere(name, service string, from, to time.Time, filters []FilterExpr) whereClause {
	var wc whereClause
	wc.add("metric = ?", name)
	wc.add("time >= ?", from)
	wc.add("time <= ?", to)
	if service != "" {
		wc.add("service_name = ?", service)
	}
	ApplyMetricFilters(&wc, filters)
	return wc
}

// exportIntervalCacheKey — prob önbelleği anahtarı.
//
// FİLTRELERİ DE TAŞIMAK ZORUNDA (CLAUDE.md sert kısıtı): aynı metrik +
// servis için farklı filtreler farklı aralık verir; anahtar onları
// ayırmazsa biri diğerinin değerini okur. v0.5.187 çapraz-zehirlenme
// sınıfı, ve buradaki bedeli YANLIŞ ÖLÇEKLİ bir grafik — yani sessiz.
func exportIntervalCacheKey(name, service string, filters []FilterExpr) string {
	var b strings.Builder
	b.WriteString(name)
	b.WriteString("\x00")
	b.WriteString(service)
	for _, f := range filters {
		b.WriteString("\x00")
		b.WriteString(f.Key)
		b.WriteString(f.Op)
		for _, v := range f.Values {
			b.WriteString("\x1f")
			b.WriteString(v)
		}
	}
	return b.String()
}

// metricExportInterval returns the cached/probed export interval for a
// metric (optionally service-scoped — a metric can ship at different
// cadences per service). 0 = unknown → caller applies no clamp; a
// probe failure must never break the chart read.
func (s *Store) metricExportInterval(ctx context.Context, name, service string) int {
	return s.metricExportIntervalFiltered(ctx, name, service, nil, true)
}

// metricExportIntervalFiltered — v0.9.687 (filtre kapsamı) + v0.9.689
// (grouped ayrımı).
//
// grouped=false → TOPLULAŞTIRILMIŞ çizgi: taban havuzlanmış tempo.
// grouped=true  → SERİ BAŞINA çizgi: taban seri başına düşük kantil.
//
// Varsayılan (eski çağıranlar) grouped=true, yani davranış değişmiyor;
// yalnız GroupBy'sız sorgular yeni, daha ince tabana geçiyor.
func (s *Store) metricExportIntervalFiltered(ctx context.Context, name, service string, filters []FilterExpr, grouped bool) int {
	key := exportIntervalCacheKey(name, service, filters)
	if grouped {
		key += "\x00g"
	}
	s.metricIvMu.RLock()
	if e, ok := s.metricIv[key]; ok && time.Since(e.at) < metricIvTTL {
		s.metricIvMu.RUnlock()
		return e.iv
	}
	s.metricIvMu.RUnlock()

	to := time.Now()
	from := to.Add(-metricIvProbeWindow)
	wc := exportIntervalProbeWhere(name, service, from, to, filters)
	sql := metricExportIntervalPooledSQL(wc)
	if grouped {
		sql = metricExportIntervalQuantileSQL(wc)
	}
	iv := 0
	var iv90 float64
	var series uint64
	if err := s.conn.QueryRow(ctx, sql, wc.args...).Scan(&iv90, &series); err == nil {
		iv = exportIntervalQuantile(iv90, series)
	}

	s.metricIvMu.Lock()
	if s.metricIv == nil {
		s.metricIv = map[string]metricIvEntry{}
	}
	s.metricIv[key] = metricIvEntry{at: time.Now(), iv: iv}
	s.metricIvMu.Unlock()
	return iv
}
