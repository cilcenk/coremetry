package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// metric_rollup_route_read.go — 0008 (route kırılımlı metrik tier'ı)
// OKUYUCUSU. v0.9.777.
//
// Neden ayrı bir dosya ve ayrı bir plan fonksiyonu: metricRollupPlan
// (metric_rollup_read.go:63) GROUPLU her sorguyu reddediyor ve bu bilinçli —
// 0003 şeması attr taşımıyor, bir route kırılımını doğru cevaplayamaz.
// O fonksiyona dal EKLEMEK, iki farklı tablonun uygunluk kuralını tek
// gövdeye sıkıştırmak olurdu. metricRollupHistPlan (v0.9.751) aynı nedenle
// kardeş dosyada duruyor; bu üçüncü kardeş.
//
// KAPSAM, dürüstçe: avg / sum / min / max, TEK grup anahtarı `http.route`,
// TEK servis. Yüzdelik YOK (0008 bucket taşımıyor — gerekçe DDL başlığında).
// `last` de YOK: 0008'de last_value kolonu yok, çünkü route kırılımında
// "son değer" seri sayısı kadar argMax state'i demekti ve bu tier'ın işi
// retansiyon, son-değer değil.
//
// PARİTE SÖZLEŞMESİ (bu tier'ın var olma şartı): dönen sayı ham yolun
// döndüğü sayıyla AYNI olmalı, yalnız kaynağı farklı. Bu yüzden agg
// ifadeleri ham yolun karşılıklarının birebir cebirsel eşleniği:
//
//	agg   ham (metric_points)                 rollup (0008)
//	────  ───────────────────────────────────  ──────────────────────────────
//	avg   histogram+delta: sum(sum_value)      sum(sum_value_sum)
//	                       /sum(count)          /sum(obs_count)
//	      diğer: avgOrNull(value)              sum(value_sum)/sum(point_count)
//	sum   sumOrNull(value)                     sum(value_sum)
//	min   minOrNull(value)                     min(value_min)
//	max   maxOrNull(value)                     max(value_max)
//
// avg'ın iki dallı olması v0.9.776 düzeltmesinin ta kendisi: histogramda
// `value` per-export ÖZET'tir, ortalamaların ortalaması yanlıştır. 0008 bu
// yüzden İKİ toplam çifti taşıyor (obs_count/sum_value_sum ve
// point_count/value_sum) — okuyucu enstrümana göre doğru çifti seçer.
//
// Her uygunsuzluk/hata FAIL-OPEN ham yola düşer (family-C ile aynı duruş):
// yavaş cevap, yanlış cevaptan iyidir.

// metricRollupRouteTiers — 0008'in kademeleri. metricRollupTiers ile aynı
// grain/TTL iskeleti, farklı tablolar. Kaba→ince: en az satır okuyan İLK
// uygun kademe kazanır.
var metricRollupRouteTiers = []metricRollupTier{
	{"rollup_metrics_route_1h", 3600, 13 * 30 * 24 * time.Hour},
	{"rollup_metrics_route_5m", 300, 90 * 24 * time.Hour},
	{"rollup_metrics_route_1m", 60, 14 * 24 * time.Hour},
}

// rollupRouteGroupKey — bu tier'ın cevapladığı TEK grup anahtarı.
const rollupRouteGroupKey = "http.route"

// rollupRouteEligibleAggs — 0008'in kolonlarından TÜRETİLEBİLEN agg'ler.
// `last` yok (last_value kolonu yok), p-quantile yok (bucket yok).
var rollupRouteEligibleAggs = map[string]bool{
	"": true, "avg": true, "sum": true, "min": true, "max": true,
}

// promoteServiceFilter — TEK service çipini f.Service kısayoluna terfi
// ettirir. SAF (tablo-testli).
//
// Neden gerekli: Explore/panel yüzeyleri servisi çoğu zaman bir FİLTRE ÇİPİ
// olarak gönderiyor (`service.name = api-gateway`), f.Service alanı olarak
// değil. Rollup planları ise "filtre varsa ham" diyor — yani tam da rollup'a
// en uygun sorgu şekli, kozmetik bir sebeple ham yola düşüyordu. PromQL
// tarafında aynı terfi zaten var (promql/eval.go:432 selectorFilter: `= on
// the service label → the Service shortcut`); bu, onun UI yolundaki ikizi.
//
// KRİTİK: dönen değer bir KOPYA. Çağıran, terfi edilmiş filtreyi YALNIZ
// rollup KARARI için kullanır; ham yola giden f'e DOKUNULMAZ. Ham yolun
// ürettiği SQL bayt-bayt aynı kalmalı (metricquery_test pinleri) — aksi
// halde bu "iyileştirme" sessizce her ham sorgunun WHERE'ini değiştirirdi.
//
// ok=false → hiçbir şey terfi etmedi; f aynen döner (çağıran ayırt etmek
// zorunda kalmasın).
func promoteServiceFilter(f MetricQueryFilter) (MetricQueryFilter, bool) {
	// Zaten Service varsa terfi edecek bir şey yok. İki çip varsa da YOK:
	// ikincisi rollup'ın cevaplayamayacağı bir daraltma olabilir ve onu
	// sessizce düşürmek YANLIŞ SAYI üretirdi (çipi atıp rollup'tan daha
	// geniş bir küme okumak).
	if f.Service != "" || len(f.Filters) != 1 {
		return f, false
	}
	fe := f.Filters[0]
	if fe.Op != "=" || len(fe.Values) != 1 || fe.Values[0] == "" {
		return f, false
	}
	switch fe.Key {
	case "service.name", "service_name", "service", "resource.service.name":
	default:
		return f, false
	}
	out := f
	out.Service = fe.Values[0]
	out.Filters = nil
	return out, true
}

// metricRollupRoutePlan — SAF uygunluk kararı (tablo-testli). ok=false → ham.
//
// f TERFİ EDİLMİŞ hâliyle gelmeli (promoteServiceFilter); instrument ve
// temporality çağırandan gelir (Store zaten çözüyor) — burada CH'ye
// gidilmez, karar saf kalır.
func metricRollupRoutePlan(f MetricQueryFilter, instrument, temporality string, now time.Time) (metricRollupTier, bool) {
	if f.Name == "" || f.Service == "" {
		return metricRollupTier{}, false
	}
	// Filtre kalmışsa ham: 0008 route DIŞINDA hiçbir attr taşımıyor.
	if len(f.Filters) > 0 || f.Instance != "" || f.Engine != "" {
		return metricRollupTier{}, false
	}
	// TAM OLARAK tek grup anahtarı ve o da http.route olmalı. İki anahtarlı
	// bir kırılım (route + status) bu şemada YOK; "route'a göre grupla,
	// gerisini yok say" cevabı yanlış olurdu.
	if len(f.GroupBy) != 1 || f.GroupBy[0] != rollupRouteGroupKey {
		return metricRollupTier{}, false
	}
	if !rollupRouteEligibleAggs[strings.ToLower(f.Aggregation)] {
		return metricRollupTier{}, false
	}
	switch {
	case instrument == "gauge":
		// gauge'da temporality anlamsız — bakılmaz.
	case instrument == "sum":
		// Cumulative ASLA: kümülatif sayaçta bucket toplamı anlamsızdır
		// (metric_rollup_read.go başlığındaki aynı gerekçe).
		if temporality != "delta" {
			return metricRollupTier{}, false
		}
	case isHistogramInstrument(instrument):
		// Cumulative histogramda sum_value/count monoton BİRİKİMLİ; toplamak
		// birikmiş toplamları üst üste toplar (metricAvgExpr'deki gerekçe).
		if temporality != "delta" {
			return metricRollupTier{}, false
		}
	default:
		return metricRollupTier{}, false
	}
	step := int64(f.StepSeconds)
	if step <= 0 {
		return metricRollupTier{}, false // auto-step ham merdivende kalır
	}
	oldest := now.Sub(f.From)
	for _, t := range metricRollupRouteTiers {
		if step%t.grainSec == 0 && oldest <= t.ttl {
			return t, true
		}
	}
	return metricRollupTier{}, false
}

// rollupRouteAggExpr — agg + instrument → 0008 kolonları üstünde SQL.
// SAF (tablo-testli). Parite tablosu dosya başlığında.
func rollupRouteAggExpr(agg, instrument string) (string, bool) {
	switch strings.ToLower(agg) {
	case "", "avg":
		if isHistogramInstrument(instrument) {
			// v0.9.776 ile TUTARLI: gözlem-ağırlıklı ortalama.
			return "sum(sum_value_sum) / nullIf(sum(obs_count), 0)", true
		}
		// gauge/sum: `value` ölçümün kendisi; sum_value/count 0'dır.
		return "sum(value_sum) / nullIf(sum(point_count), 0)", true
	case "sum":
		return "sum(value_sum)", true
	case "min":
		return "min(value_min)", true
	case "max":
		return "max(value_max)", true
	}
	return "", false
}

// buildRollupRouteSQL — SAF (pinli).
//
// Ham yolla PARİTE noktaları, hepsi testli:
//   - bucket NANOSANİYE (ham yol *1000000000 yazıyor; ms/sn karışımı
//     paneli sessizce 1000× kaydırırdı),
//   - gk = [route] tek elemanlı DİZİ — ham yolun groupSelect'i de dizi
//     üretiyor ve seriler strings.Join(gk,"|") ile anahtarlanıyor. Tek
//     string döndürseydik kademe geçişinde lejant/renk değişirdi,
//   - LIMIT SpanMetricRowCap: ham yolla AYNI tavan, dolayısıyla
//     SeriesRowsCapped aynı eşikte "kırpıldı" der,
//   - ORDER BY gk, bucket: ham yolun sırası.
//
// service_key IN (?) — tek servis için bile ÇOKLU aday: prod'da
// service_key = k8s.deployment.name olabilir ve ad ortam ekiyle gelebilir
// (bkz. queryMetricRollupRoute'daki aday üretimi).
//
// instrument + unit WHERE'de: 0008'in ORDER BY'ı bu ikisini taşıyor, yani
// 'ms' ve 's' satırları AYRI. Filtre olmasaydı iki birim tek seride
// toplanırdı — 0003 okuyucusunda açık duran sessiz karışım kapısı burada
// AÇILMIYOR.
func buildRollupRouteSQL(table, aggExpr string) string {
	return fmt.Sprintf(`
		SELECT
			toUnixTimestamp(toStartOfInterval(ts, INTERVAL ? SECOND)) * 1000000000 AS bucket,
			[route] AS gk,
			toNullable(toFloat64(%s)) AS v
		FROM %s
		WHERE metric = ? AND service_key IN (?) AND instrument = ? AND unit = ?
		  AND ts >= ? AND ts <= ?
		GROUP BY bucket, gk
		ORDER BY gk, bucket
		LIMIT %d
		SETTINGS max_execution_time = 10`, aggExpr, table, SpanMetricRowCap)
}

// rollupRouteServiceKeys — WHERE'e girecek service_key adayları. SAF.
//
// service_key MV'de `coalesce(k8s.deployment.name, service_name)`. Lokalde
// ikisi aynı; prod'da deployment adı ortam ekini TAŞIMAYABİLİR
// (payments-int servisi payments deployment'ında koşar). Tek ad aramak o
// kurulumda tier'ı sessizce boş döndürürdü — boş sonuç fail-open ile ham
// yola düşer, yani "yanlış" değil ama tier hiç açılmaz.
func rollupRouteServiceKeys(service string) []string {
	keys := []string{service}
	if st := StripEnvSuffix(service); st != "" && st != service {
		keys = append(keys, st)
	}
	return keys
}

// queryMetricRollupRoute — route kırılımını 0008'den okur. Çok seri döner
// (her route bir seri), ham yolla AYNI SpanMetricSeries şeklinde.
func (s *Store) queryMetricRollupRoute(
	ctx context.Context, f MetricQueryFilter, tr metricRollupTier, instrument, unit string,
) ([]SpanMetricSeries, error) {
	aggExpr, ok := rollupRouteAggExpr(f.Aggregation, instrument)
	if !ok {
		return nil, fmt.Errorf("rollup route: desteklenmeyen agg %q", f.Aggregation)
	}
	rows, err := s.telemetryReadConn().Query(ctx,
		buildRollupRouteSQL(tr.table, aggExpr),
		f.StepSeconds, f.Name, rollupRouteServiceKeys(f.Service), instrument, unit, f.From, f.To)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seriesMap := make(map[string]*SpanMetricSeries)
	var order []string
	for rows.Next() {
		var bucket uint64
		var gk []string
		var val *float64
		if err := rows.Scan(&bucket, &gk, &val); err != nil {
			return nil, err
		}
		if val == nil {
			continue // boş bucket nokta üretmez (ham yolla aynı: gap, 0 değil)
		}
		key := strings.Join(gk, "|")
		ser, ok := seriesMap[key]
		if !ok {
			ser = &SpanMetricSeries{GroupKey: gk}
			seriesMap[key] = ser
			order = append(order, key)
		}
		ser.Points = append(ser.Points, SpanMetricPoint{Time: int64(bucket), Value: *val})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]SpanMetricSeries, 0, len(order))
	for _, k := range order {
		out = append(out, *seriesMap[k])
	}
	return out, nil
}

// rollupRouteShapeOK — planın ENSTRÜMANDAN BAĞIMSIZ yarısı. SAF.
//
// Neden ayrı: enstrüman/temporality öğrenmek CH'ye iki küçük prob demek.
// Şekil tutmuyorsa (ki sorguların ezici çoğunluğunda tutmaz) o probları
// atmanın anlamı yok. metricRollupRoutePlan bu koşulları YENİDEN kontrol
// eder — tek kapı, tek tablo testi; buradaki kopya yalnız erken çıkış.
func rollupRouteShapeOK(f MetricQueryFilter) bool {
	return f.Name != "" && f.Service != "" &&
		len(f.Filters) == 0 && f.Instance == "" && f.Engine == "" &&
		len(f.GroupBy) == 1 && f.GroupBy[0] == rollupRouteGroupKey &&
		rollupRouteEligibleAggs[strings.ToLower(f.Aggregation)] &&
		f.StepSeconds > 0
}

// tryMetricRollupRoute — QueryMetric'in tek çağrı noktası. ok=false →
// çağıran ham yola AYNEN devam eder (f'e dokunulmamıştır).
//
// Enstrüman/temporality probları BURADA atılıyor, QueryMetric'ten
// devralınmıyor. Gerekçe: QueryMetric'in probu f.Service ile koşuyor ve
// route yolunda servis bir ÇİP olabilir (f.Service boş) — o prob tüm
// servisleri kapsar ve `any(instrument)` yanlış servisin türünü
// döndürebilir. Devralsaydık ya yanlış karar verirdik ya da QueryMetric'in
// probunu değiştirip HAM YOLUN avg ifadesini oynatırdık (v0.9.776'nın
// instrument/temporality'si oraya besleniyor) — ikisi de kabul edilemez.
func (s *Store) tryMetricRollupRoute(ctx context.Context, f MetricQueryFilter) ([]SpanMetricSeries, bool) {
	pf, _ := promoteServiceFilter(f)
	if !rollupRouteShapeOK(pf) {
		return nil, false
	}
	instrument := s.metricInstrument(ctx, pf.Name, pf.Service, pf.From, pf.To)
	temporality := ""
	if instrument == "sum" || isHistogramInstrument(instrument) {
		temporality = s.metricTemporalityFiltered(ctx, pf.Name, pf.Service, nil)
	}
	tr, ok := metricRollupRoutePlan(pf, instrument, temporality, time.Now())
	if !ok {
		return nil, false
	}
	// İleri-yalnız cutover: tablo dün kurulduysa geçen haftanın sorgusu
	// hama düşmeli, yoksa panel yarısı boş bir grafik çizer.
	floor, has := s.rollupCoverageFloor(ctx, tr.table)
	if !has || floor.After(pf.From) {
		return nil, false
	}
	// unit probu YALNIZ burada — plan zaten "evet" dedikten sonra. Boş unit
	// ('' basan SDK) meşru bir değer ve WHERE'de aynen eşleşir.
	unit := s.MetricUnit(ctx, pf.Name, pf.Service)
	ser, err := s.queryMetricRollupRoute(ctx, pf, tr, instrument, unit)
	if err != nil || len(ser) == 0 {
		return nil, false // fail-open ham yola
	}
	return ser, true
}
