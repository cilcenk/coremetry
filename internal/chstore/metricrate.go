package chstore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// metricrate.go (F2, v0.9.106 — PromQL rate/increase parity) — OTLP kümülatif
// counter'lardan (instrument='sum', temporality='cumulative') reset-korumalı
// per-saniye hız (rate) ve pencere-artışı (increase). Roadmap F2
// (scratchpad/metrics-chart-parity-roadmap.md).
//
// TASARIM (Go-side delta): SQL yalnız per-(SERİ, bucket) SON kümülatif değeri
// (argMax(value,time)) getirir; delta + reset-telafisi + kullanıcı-groupBy'a
// yeniden-toplama Go'da yapılır. Neden Go-side, SQL window-fn değil:
//   - ClickHouse runningDifference()/neighbor() her DATA-BLOK sınırında state
//     sıfırlar → çok-bloklu seride yanlış delta (issues #6353/#10334). Go-side
//     bu sınıfı TAMAMEN atlar (lagInFrame OVER PARTITION doğru olurdu ama
//     Go-side hem daha test-edilebilir hem fingerprint=0 fallback'i temiz).
//   - SERİ kimliği series_fingerprint (xxhash64, otlp). Prod-distributed'da
//     cluster_name unset ise kolon DEFAULT 0 kalır (store.go:2029) → per-satır
//     COALESCE ile (fingerprint!=0 ? fp : synthetic attr-hash) fallback; ASLA
//     sessizce tüm serileri tek partition'a çökertip yanlış delta üretme.
//   - rate PER SERİ hesaplanır, SONRA kullanıcı-groupBy'a toplanır (PromQL
//     `sum(rate(counter)) by(label)` semantiği).
//
// VictoriaMetrics gerekçesiyle EXTRAPOLASYON ATLANIR: bucket'lar zaten delta;
// yavaş integer counter'da extrapole kesirli/yanıltıcı sonuç verir.

// resetSafeDelta — Prometheus reset-korumalı per-interval artış: cur < prev
// (counter restart) ise post-reset değeri (cur) artış say; aksi halde cur-prev.
// Telescoping ile toplam = (last-first) + Σ(reset'lerdeki düzeltme) — Prometheus
// counterCorrection'la aynı. Tek-örnekli reset atfı doğru (prev_max+cur ÇİFT
// sayardı).
func resetSafeDelta(prev, cur float64) float64 {
	if cur < prev {
		return cur
	}
	return cur - prev
}

type ratePoint struct {
	bucket uint64 // unix ns
	value  float64
}

// seriesRatePoints — TEK serinin (buckets ns-artan, vals kümülatif; nil=gap)
// per-bucket rate/increase'ini üretir. Review-fix'leri (v0.9.106):
//   - GERÇEK dt: delta / (bucket_i - prevBucket_i) saniye — sabit step'e
//     bölmek gap'te (eksik bucket) rate'i FAZLA gösteriyordu (over-division
//     spike). mode="rate" → delta/dt_sn; "increase" → ham delta.
//   - SEED lookback: ilk DOLU örnek prev'i primer ama emit EDİLMEZ. dropBefore
//     (< orijinal From) seed bölgesi — pencere-öncesi bir bucket çekilip prev
//     primer, böylece pencere-içi İLK bucket gerçek delta alır (sol-kenar
//     sahte-sıfır kalkar, PromQL lookback semantiği). Seed yoksa (gerçekten
//     yeni seri) ilk pencere-içi bucket baseline'dır — doğru.
func seriesRatePoints(buckets []uint64, vals []*float64, mode string, dropBeforeNs uint64, monotonic bool) []ratePoint {
	var out []ratePoint
	havePrev := false
	var prevV float64
	var prevB uint64
	for i := range buckets {
		if vals[i] == nil {
			continue
		}
		cur := *vals[i]
		curB := buckets[i]
		if !havePrev {
			prevV, prevB, havePrev = cur, curB, true
			continue // baseline: primer, emit yok
		}
		// v0.9.714 — telafi YALNIZ monotonikte (gerekçe: SQL kapısı yorumu).
		delta := cur - prevV
		if monotonic {
			delta = resetSafeDelta(prevV, cur)
		}
		dtSec := float64(curB-prevB) / 1e9
		prevV, prevB = cur, curB
		if curB < dropBeforeNs {
			continue // seed bölgesi — primer ama emit yok
		}
		val := delta
		if mode == "rate" && dtSec > 0 {
			val = delta / dtSec
		}
		out = append(out, ratePoint{bucket: curB, value: val})
	}
	return out
}

// isRateableInstrument — rate/increase yalnız MONOTONIC COUNTER'da (sum) anlamlı.
// gauge (anlık) / histogram (dağılım) reddedilir.
func isRateableInstrument(instrument string) bool {
	return instrument == "sum"
}

// metricSeriesKeyExpr — per-seri PARTITION anahtarı SQL ifadesi. series_
// fingerprint (xxhash64) birincil; 0 ise (prod-distributed cluster_name unset,
// store.go:2029, ya da eski data) sıralı attr-set + service üzerinden
// cityHash64 synthetic'e DÜŞER — asla tüm serileri tek anahtara çökertme.
// hasFp=false (kolon şardlara hiç ulaşmamış) → doğrudan synthetic.
func metricSeriesKeyExpr(hasFp bool) string {
	// SeriesFingerprint (otlp/fingerprint.go:51) kimliği: metric + sorted
	// (dpAttrs) + service.instance.id + service.name. Synthetic fallback AYNI
	// kimliği kurmalı — service.instance.id (res_values) ATLANIRSA farklı
	// pod'lar (aynı dpAttr + service.name, farklı instance) tek sk'ya çöker →
	// pod'lar arası bağımsız-reset'li kümülatif counter'lar karışıp YANLIŞ
	// delta üretir. metric zaten WHERE'de sabit; service.instance.id +
	// service.name + sıralı dpAttr yeterli.
	synthetic := `cityHash64(concat(` +
		`service_name, '||', ` +
		`res_values[indexOf(res_keys, 'service.instance.id')], '||', ` +
		`arrayStringConcat(arraySort(arrayMap((k, v) -> concat(k, '=', v), attr_keys, attr_values)), ',')))`
	if hasFp {
		return `toString(if(series_fingerprint != 0, series_fingerprint, ` + synthetic + `))`
	}
	return `toString(` + synthetic + `)`
}

// metricTemporality — metriğin OTLP aggregation temporality'sini probe'lar
// ('cumulative' | 'delta' | ”). Boş/bilinmeyen → çağıran cumulative sayar
// (OTLP default). Bounded (max_execution_time=3, time-pruned).
func (s *Store) metricTemporality(ctx context.Context, name, service string) string {
	return s.metricTemporalityFiltered(ctx, name, service, nil)
}

// temporalityProbeWhere — temporality probunun WHERE'i (saf, testli).
//
// v0.9.669 (v0.9.668'de AÇTIĞIM hata) — prob, ana sorgunun BAKTIĞI
// satır kümesine bakmalı.
//
// Olay: metrik throughput'un `job` yolu MetricQueryFilter'ı Service'siz
// kuruyor (kimlik job etiketinde, service_name'de değil). Prob da
// Service'siz çalışınca `any(temporality)` TÜM servislere bakıyordu —
// ve http.server.duration gerçekten KARIŞIK: yerel veride 6 servis
// delta, 1 servis (coremetry-monolithic) cumulative.
//
// any() hangi tarafı seçerse diğeri yanlış hesaplanıyordu:
//
//	delta seçilirse      → cumulative seri per-bucket toplanır, ŞİŞER
//	                       (mx 656 vs delta'da ≤7).
//	cumulative seçilirse → delta seriye cross-bucket delta uygulanır,
//	                       sıfıra yakın ÇÖP.
//
// Üstelik any() deterministik değil: part/thread sırasına bağlı, part
// birleşmesiyle sessizce dönebilir. Grafik bir gün kendiliğinden
// yanlışa geçerdi.
//
// Filtreler proba da inince prob, sorgunun eşlediği serilerle AYNI
// kümeye bakıyor. Filtresiz çağıranlarda davranış DEĞİŞMİYOR.
func temporalityProbeWhere(name, service string, from, to time.Time, filters []FilterExpr) whereClause {
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

func (s *Store) metricTemporalityFiltered(ctx context.Context, name, service string, filters []FilterExpr) string {
	to := time.Now()
	from := to.Add(-metricIvProbeWindow)
	wc := temporalityProbeWhere(name, service, from, to, filters)
	q := `SELECT any(temporality) FROM metric_points ` + wc.sql() +
		` SETTINGS max_execution_time = 3`
	var temp string
	if err := s.conn.QueryRow(ctx, q, wc.args...).Scan(&temp); err != nil {
		return ""
	}
	return temp
}

// QueryMetricRate — PromQL rate()/increase() muadili (F2). mode: "rate"
// (per-saniye) | "increase" (pencere-artışı, ham delta toplamı). Yalnız
// counter (instrument='sum'); gauge/histogram boş döner. Cumulative
// temporality → per-seri reset-korumalı cross-bucket delta; delta temporality
// → per-bucket sum (değer zaten interval-artışı). Aynı step disiplini
// (metricAutoStepPx + clampStepToExport). Sonuç SpanMetricSeries — UI aynı.
// rateSource — rate'in HANGİ instrument'ten ve HANGİ kolondan okunacağı.
//
// v0.9.668 (operatör-bildirimi): "Coremetry'de sanırım
// http.server.request.duration şeklinde çıkıyor metric ismi. O yüzden
// overview'de çıkmıyor."
//
// Teşhis daha derindi: ad yalnız ilk engeldi. QueryMetricRate
// instrument='sum' sabitini taşıyordu, yani bir HISTOGRAM verildiğinde
// sessizce BOŞ dönüyordu. Throughput'un histogramdaki karşılığı
// `count` kolonu (gözlem sayısı); `value` orada anlamsız.
//
// OTel HTTP server metriği çoğu kurulumda histogram
// (http.server.duration / http.server.request.duration), yani sabit
// 'sum' bu yolun en yaygın durumu KAÇIRMASI demekti.
type rateSource struct {
	instrument string // metric_points.instrument
	valueExpr  string // rate'lenecek kolon
}

// rateSourceCounter — klasik sayaç: ölçüm `value`da.
var rateSourceCounter = rateSource{instrument: "sum", valueExpr: "value"}

// rateSourceHistogramCount — histogramın GÖZLEM SAYISI. Süre histogramı
// için bu tam olarak istek sayısıdır, yani throughput.
//
// v0.9.686 — TİP TUZAĞI: `count` UInt64, `value` ise Float64. Go tarafı
// ikisini de *float64'e tarıyor, yani histogram yolu ScanRow'da
// patlıyordu: "(v) converting UInt64 to *float64". SQL kurucusu artık
// değeri KOŞULSUZ toFloat64 ile sarıyor — kaynak hangi kolon olursa
// olsun `v` Float64 döner. Sarmalama kurucuda, kaynak tanımında DEĞİL:
// yeni bir rateSource eklendiğinde tipi düşünmek gerekmesin.
var rateSourceHistogramCount = rateSource{instrument: "histogram", valueExpr: "count"}

// QueryMetricRate — sayaç yolu (geriye dönük imza korunuyor).
func (s *Store) QueryMetricRate(ctx context.Context, f MetricQueryFilter, mode string) ([]SpanMetricSeries, error) {
	return s.queryRateFrom(ctx, f, mode, rateSourceCounter)
}

// QueryMetricCountRate — histogram gözlem sayısının rate'i.
func (s *Store) QueryMetricCountRate(ctx context.Context, f MetricQueryFilter, mode string) ([]SpanMetricSeries, error) {
	return s.queryRateFrom(ctx, f, mode, rateSourceHistogramCount)
}

// MetricPresentKeys — verilen anahtarlardan HANGİLERİ bu metrikte
// gerçekten var (attr_keys ya da res_keys içinde).
//
// v0.9.682. Tanılamanın eksik yarısı buydu: "denenen adaylar" listesi
// hangi yolun DENENDİĞİNİ söylüyordu ama hangisinin kurulumda VAR
// olduğunu söylemiyordu. İkisi bambaşka eylem gerektiriyor:
//
//	anahtar YOK      → collector o kimliği hiç göndermiyor
//	anahtar VAR ama eşleşmedi → değer beklediğimizden farklı
//
// Boş bir grafik ikisini de aynı gösteriyordu.
//
// Tek sorgu, anahtar başına countIf — N ayrı tur değil. `resource.`
// öneki soyulup res_keys'e, önek yoksa attr_keys'e bakılıyor (filtre
// tarafındaki çözümlemenin aynısı).
func (s *Store) MetricPresentKeys(ctx context.Context, metric string, keys []string, since time.Duration) []string {
	if len(keys) == 0 {
		return nil
	}
	to := time.Now()
	from := to.Add(-since)
	sel := make([]string, 0, len(keys))
	args := []any{}
	for i, k := range keys {
		col, name := "attr_keys", k
		if strings.HasPrefix(k, "resource.") {
			col, name = "res_keys", strings.TrimPrefix(k, "resource.")
		}
		sel = append(sel, fmt.Sprintf("countIf(has(%s, ?)) AS k%d", col, i))
		args = append(args, name)
	}
	args = append(args, metric, from, to)
	q := "SELECT " + strings.Join(sel, ", ") +
		" FROM metric_points WHERE metric = ? AND time >= ? AND time <= ?" +
		" SETTINGS max_execution_time = 5"

	counts := make([]uint64, len(keys))
	dest := make([]any, len(keys))
	for i := range counts {
		dest[i] = &counts[i]
	}
	if err := s.conn.QueryRow(ctx, q, args...).Scan(dest...); err != nil {
		return nil
	}
	var out []string
	for i, k := range keys {
		if counts[i] > 0 {
			out = append(out, k)
		}
	}
	return out
}

// MetricUnit — bir metriğin OTLP birimi ("s", "ms", "By", …).
// MetricInstrument ile aynı kalıp: kısa, sınırlı prob.
func (s *Store) MetricUnit(ctx context.Context, name, service string) string {
	to := time.Now()
	from := to.Add(-metricIvProbeWindow)
	q := `SELECT any(unit) FROM metric_points WHERE metric = ? AND time >= ? AND time <= ?`
	args := []any{name, from, to}
	if service != "" {
		q += ` AND service_name = ?`
		args = append(args, service)
	}
	q += ` SETTINGS max_execution_time = 3`
	var u string
	if err := s.conn.QueryRow(ctx, q, args...).Scan(&u); err != nil {
		return ""
	}
	return u
}

// MetricInstrument — bir metriğin instrument türü ("sum"/"histogram"/
// "gauge"/""). metricTemporality ile aynı kalıp: kısa, sınırlı prob.
func (s *Store) MetricInstrument(ctx context.Context, name, service string) string {
	to := time.Now()
	from := to.Add(-metricIvProbeWindow)
	q := `SELECT any(instrument) FROM metric_points WHERE metric = ? AND time >= ? AND time <= ?`
	args := []any{name, from, to}
	if service != "" {
		q += ` AND service_name = ?`
		args = append(args, service)
	}
	q += ` SETTINGS max_execution_time = 3`
	var inst string
	if err := s.conn.QueryRow(ctx, q, args...).Scan(&inst); err != nil {
		return ""
	}
	return inst
}

func (s *Store) queryRateFrom(ctx context.Context, f MetricQueryFilter, mode string, src rateSource) ([]SpanMetricSeries, error) {
	if f.Name == "" {
		return nil, fmt.Errorf("metric name required")
	}
	if mode != "rate" && mode != "increase" {
		return nil, fmt.Errorf("unknown rate mode %q", mode)
	}

	now := time.Now()
	if f.To.IsZero() {
		f.To = now
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-24 * time.Hour)
	}
	if f.StepSeconds <= 0 {
		f.StepSeconds = metricAutoStepPx(f.From, f.To, f.MaxDataPoints)
	}
	// v0.9.687 — filtreler proba da iniyor. Kapsamsız prob, eşleşen
	// servisin gerçek yayım aralığı yerine TÜM servislerin p90'ını
	// veriyordu; dar pencerede clamp devreye girmeyince oran şişiyor ve
	// grafik düz bir çizgiye iniyordu (operatör-bildirimi: 30 dk doğru,
	// 5 dk bozuk).
	// v0.9.689 — taban, grafiğin ne çizdiğine bağlı: GroupBy YOKSA tüm
	// seriler tek çizgide toplanıyor, ince adım delik üretmez →
	// havuzlanmış tempo. GroupBy VARSA her çizgi kendi serisine bağlı →
	// seri başına kantil.
	if iv := s.metricExportIntervalFiltered(ctx, f.Name, f.Service, f.Filters, len(f.GroupBy) > 0); iv > 0 {
		f.StepSeconds = clampStepToExport(f.StepSeconds, iv)
	}
	step := f.StepSeconds
	if step <= 0 {
		step = 60
	}

	// Delta temporality: değer zaten per-interval artışı → per-bucket sum;
	// cumulative (OTLP default; probe boşsa varsay) → per-seri cross-bucket delta.
	isDelta := s.metricTemporalityFiltered(ctx, f.Name, f.Service, f.Filters) == "delta"

	// Cumulative: pencere-öncesi bir SEED bucket (From-step) çek ki pencere-içi
	// İLK bucket gerçek delta alsın (sol-kenar sahte-sıfır kalkar, PromQL
	// lookback). Delta yolu seed'e ihtiyaç duymaz. originalFromNs seed'i emit
	// dışı tutar.
	lowerBound := f.From
	originalFromNs := uint64(f.From.UnixNano())
	if !isDelta {
		lowerBound = f.From.Add(-time.Duration(step) * time.Second)
	}

	// WHERE: metric + service + window + instrument='sum' (counter) + filtreler.
	var wc whereClause
	wc.add("metric = ?", f.Name)
	if f.Service != "" {
		wc.add("service_name = ?", f.Service)
	}
	wc.add("time >= ?", lowerBound)
	wc.add("time <= ?", f.To)
	// v0.9.668 — HANGİ instrument, HANGİ kolon.
	//
	// Sayaçta ölçüm `value`da; histogramda `count` kolonunda (gözlem
	// SAYISI). Throughput bir histogramdan okunacaksa doğru kolon
	// `count` — `value` histogramda anlamsız ve sessizce 0 döner.
	wc.add("instrument = ?", src.instrument)
	// v0.9.714 (parite koşumu bulgusu) — v0.9.106 kapısı is_monotonic=0'ı
	// FİLTRELİYORDU ve UpDownCounter'a rate soran SESSİZCE BOŞ grafik
	// alıyordu (cgo.calls vakası; Go SDK bazı runtime sayaçlarını açıkça
	// non-monotonik bildirir). Prometheus aynı seriye cevap verir. Yeni
	// davranış: 0'ları da OKU, ama delta'yı RESET-TELAFİSİZ hesapla (düz
	// türev; negatif MEŞRU — up-down'ın anlamı bu). Reset telafisi yalnız
	// monotonikte: telafi non-monotonikte her düşüşü zirveye çevirirdi —
	// v0.9.106'nın korkusu YERİNDEYDİ, cevabı yanlıştı (boş yerine doğru
	// tanım). Monotoniklik SELECT'e taşındı (seri-başı max: karışık
	// damgalı seri pratikte tek damga; max=1 → telafili, temkinli taraf).
	// Kolon yoksa (external-distributed) eski best-effort duruş.
	ApplyMetricFilters(&wc, f.Filters)

	// Kullanıcı groupBy ifadesi (yeniden-toplama anahtarı).
	groupSelect := "[]::Array(String)"
	if len(f.GroupBy) > 0 {
		parts := make([]string, len(f.GroupBy))
		var groupArgs []any
		for i, k := range f.GroupBy {
			expr, args := groupKeyExprMetric(k)
			parts[i] = expr
			groupArgs = append(groupArgs, args...)
		}
		groupSelect = "[" + strings.Join(parts, ", ") + "]"
		wc.args = append(groupArgs, wc.args...)
	}

	if isDelta {
		return s.queryRateDelta(ctx, wc, groupSelect, step, mode, src)
	}
	return s.queryRateCumulative(ctx, wc, groupSelect, step, mode, originalFromNs, src)
}

// queryRateCumulative — per-(seri, bucket) SON kümülatif değeri çeker; Go'da
// per-seri reset-korumalı delta (scalarSeriesDelta) + kullanıcı-groupBy'a
// yeniden-toplama. CH bounds korunur (LIMIT + max_execution_time + time WHERE).
func (s *Store) queryRateCumulative(ctx context.Context, wc whereClause, groupSelect string, step int, mode string, originalFromNs uint64, src rateSource) ([]SpanMetricSeries, error) {
	sql := buildRateCumulativeSQL(rateSQLParams{
		Step:      step,
		SeriesKey: metricSeriesKeyExpr(s.hasSeriesFpCol),
		GroupExpr: groupSelect,
		ValueExpr: src.valueExpr,
		MonoExpr:  monoSelectExpr(s.hasIsMonotonicCol), // v0.9.714 (gerekçe: SQL kapı yorumu)
		Where:     wc.sql(),
	})

	rows, err := s.conn.Query(ctx, sql, wc.args...)
	if err != nil {
		return nil, fmt.Errorf("rate query: %w", err)
	}
	defer rows.Close()

	// Per seri (sk): zaman-sıralı (bucket, cumulative-value) + gk. ORDER BY
	// sk,bucket olduğundan her sk'nin satırları zaten sıralı gelir.
	type skSeries struct {
		gk      []string
		buckets []uint64
		vals    []*float64
		mono    bool
	}
	bySk := map[string]*skSeries{}
	var skOrder []string
	for rows.Next() {
		var bucket uint64
		var sk string
		var gk []string
		var v *float64
		var mono uint8
		if err := rows.Scan(&bucket, &sk, &gk, &v, &mono); err != nil {
			return nil, err
		}
		ss := bySk[sk]
		if ss == nil {
			ss = &skSeries{gk: gk}
			bySk[sk] = ss
			skOrder = append(skOrder, sk)
		}
		if mono == 1 {
			ss.mono = true
		}
		ss.buckets = append(ss.buckets, bucket)
		ss.vals = append(ss.vals, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Per-seri rate/increase (gerçek dt + seed lookback) → kullanıcı-groupBy'a
	// bucket bazında topla. rate ZATEN per-seri /dt yapıldı (sum(rate) =
	// Σ delta_i/dt_i, dt seri-başı değişebilir; delta'ları toplayıp step'e
	// bölmek YANLIŞ olurdu — review-fix #2).
	byGk := map[string]map[uint64]float64{}
	gkKeys := map[string][]string{}
	var gkOrder []string
	for _, sk := range skOrder {
		ss := bySk[sk]
		pts := seriesRatePoints(ss.buckets, ss.vals, mode, originalFromNs, ss.mono)
		gkKey := strings.Join(ss.gk, "\x00")
		acc := byGk[gkKey]
		if acc == nil {
			acc = map[uint64]float64{}
			byGk[gkKey] = acc
			gkKeys[gkKey] = ss.gk
			gkOrder = append(gkOrder, gkKey)
		}
		for _, rp := range pts {
			acc[rp.bucket] += rp.value
		}
	}

	return buildRateSeries(byGk, gkKeys, gkOrder, 1), nil
}

// queryRateDelta — delta-temporality counter: değer zaten per-interval artışı,
// per-(gk, bucket) sumOrNull yeter (cross-bucket delta YOK).
func (s *Store) queryRateDelta(ctx context.Context, wc whereClause, groupSelect string, step int, mode string, src rateSource) ([]SpanMetricSeries, error) {
	sql := buildRateDeltaSQL(rateSQLParams{
		Step:      step,
		GroupExpr: groupSelect,
		ValueExpr: src.valueExpr,
		Where:     wc.sql(),
	})

	rows, err := s.conn.Query(ctx, sql, wc.args...)
	if err != nil {
		return nil, fmt.Errorf("rate(delta) query: %w", err)
	}
	defer rows.Close()

	byGk := map[string]map[uint64]float64{}
	gkKeys := map[string][]string{}
	var gkOrder []string
	for rows.Next() {
		var bucket uint64
		var gk []string
		var v *float64
		if err := rows.Scan(&bucket, &gk, &v); err != nil {
			return nil, err
		}
		if v == nil {
			continue
		}
		gkKey := strings.Join(gk, "\x00")
		acc := byGk[gkKey]
		if acc == nil {
			acc = map[uint64]float64{}
			byGk[gkKey] = acc
			gkKeys[gkKey] = gk
			gkOrder = append(gkOrder, gkKey)
		}
		acc[bucket] += *v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Delta counter: değer zaten bucket'ın per-interval artışı. rate → /step
	// (delta bucket'ları düzenli, gap-over-division sorunu YOK — review notu);
	// increase → ham. Cumulative yol /dt'yi seriesRatePoints'te zaten yaptı.
	divBy := 1.0
	if mode == "rate" {
		divBy = float64(step)
	}
	return buildRateSeries(byGk, gkKeys, gkOrder, divBy), nil
}

// buildRateSeries — bucket-bazlı (ZATEN nihai) değerleri SpanMetricSeries'e
// çevirir; divBy ile böler (cumulative=1 çünkü /dt seriesRatePoints'te
// yapıldı; delta-rate=step; delta-increase=1).
func buildRateSeries(byGk map[string]map[uint64]float64, gkKeys map[string][]string, gkOrder []string, divBy float64) []SpanMetricSeries {
	if divBy == 0 {
		divBy = 1
	}
	out := make([]SpanMetricSeries, 0, len(gkOrder))
	for _, gkKey := range gkOrder {
		acc := byGk[gkKey]
		buckets := make([]uint64, 0, len(acc))
		for b := range acc {
			buckets = append(buckets, b)
		}
		sort.Slice(buckets, func(i, j int) bool { return buckets[i] < buckets[j] })
		pts := make([]SpanMetricPoint, 0, len(buckets))
		for _, b := range buckets {
			pts = append(pts, SpanMetricPoint{Time: int64(b), Value: acc[b] / divBy})
		}
		out = append(out, SpanMetricSeries{GroupKey: gkKeys[gkKey], Points: pts})
	}
	return out
}

// buildRateCumulativeSQL / buildRateDeltaSQL — rate sorgularının SQL'i.
//
// v0.9.685 — SAF FONKSİYONA ÇIKARILDI, ve sebebi bir hata:
//
// v0.9.668'de değer kolonunu parametrik yaptım (sayaçta `value`,
// histogramda `count`) ve fmt.Sprintf argümanlarını YANLIŞ SIRAYA
// koydum. Şablon sırası sk → gk → değer iken argümanlar
// değer → sk → gk gidiyordu, yani sorgu şu hâle geliyordu:
//
//	count AS sk,                              ← UInt64
//	toString(cityHash64(...)) AS gk,
//	argMaxOrNull([]::Array(String), time) AS v
//
// Sonuç: `clickhouse [ScanRow]: (sk) converting UInt64 to *string`.
// HER İKİ YOL da (cumulative ve delta) kırıktı ve ON ALTI SÜRÜM boyunca
// fark edilmedi — çünkü ucu bir kez bile çalıştırmadım; yerelde auth,
// prod'da erişim yok. Operatörün ekran görüntüsündeki tanılama kutusu
// (v0.9.683) hatayı yüzeye çıkarana kadar sessizdi.
//
// Bu yüzden SQL artık ADLANDIRILMIŞ parametrelerle kuruluyor ve testi
// her yer tutucunun DOĞRU ifadeyi aldığını çiviliyor. Konumsal
// fmt.Sprintf, bu şablon gibi aynı tipte dört %s taşıyan yerlerde
// derleyicinin yakalayamadığı bir hata sınıfı.
// rateSQLParams — ADLANDIRILMIŞ alanlar, ve bu bir düzeltme değil bir
// TASARIM kararı.
//
// İlk düzeltmemde konumsal argümanları doğru sıraya koyup kurucunun
// yer tutucu sırasını test etmiştim. TEST HATAYI YAKALAMADI: kurucuyu
// kendi argümanlarıyla çağırıyordu, oysa hata ÇAĞRI YERİNDEYDİ. Yani
// kapı yanlış şeyi ölçüyordu — bugün altı kez düzelttiğim sınıfın
// testime bulaşmış hâli.
//
// Struct ile hata TESPİT EDİLEBİLİR olmaktan çıkıp İMKÂNSIZ oluyor:
// aynı tipte dört string'i yanlış sıraya koymak artık derleme hatası
// değil ama alan adları sayesinde okurken görünür, ve sessizce
// kaymaları mümkün değil.
type rateSQLParams struct {
	Step      int
	SeriesKey string // yalnız cumulative
	// v0.9.714 — seri monotonikliği SELECT'e taşındı (0'lar artık okunuyor;
	// delta telafisi Go'da bu bayrağa göre). Kolon yoksa "toUInt8(1)".
	MonoExpr string
	GroupExpr string
	ValueExpr string
	Where     string
}

func buildRateCumulativeSQL(p rateSQLParams) string {
	return fmt.Sprintf(`
		SELECT
		    toUnixTimestamp(toStartOfInterval(time, INTERVAL %d SECOND)) * 1000000000 AS bucket,
		    %s AS sk,
		    %s AS gk,
		    argMaxOrNull(toFloat64(%s), time) AS v,
		    %s AS mono
		FROM metric_points
		%s
		GROUP BY bucket, sk, gk
		ORDER BY sk, bucket
		LIMIT 50000
		SETTINGS max_execution_time = 25`, p.Step, p.SeriesKey, p.GroupExpr, p.ValueExpr, p.MonoExpr, p.Where)
}

func buildRateDeltaSQL(p rateSQLParams) string {
	return fmt.Sprintf(`
		SELECT
		    toUnixTimestamp(toStartOfInterval(time, INTERVAL %d SECOND)) * 1000000000 AS bucket,
		    %s AS gk,
		    sumOrNull(toFloat64(%s)) AS v
		FROM metric_points
		%s
		GROUP BY bucket, gk
		ORDER BY gk, bucket
		LIMIT 50000
		SETTINGS max_execution_time = 25`, p.Step, p.GroupExpr, p.ValueExpr, p.Where)
}

// monoSelectExpr — cumulative SELECT'inin mono kolonu. SAF (testli).
func monoSelectExpr(hasCol bool) string {
	if hasCol {
		return "toUInt8(max(is_monotonic))"
	}
	return "toUInt8(1)"
}
