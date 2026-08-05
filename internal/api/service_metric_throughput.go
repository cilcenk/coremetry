// v0.9.665 — servis throughput'unu METRİKTEN okuma.
//
// Operatör: "service overview throughput için ekrandaki metrikten
// job=abc/cm-put-service şeklinde Service ismi / sonrası olacak şekilde
// ayarlayabilir misin, metricten okusun bakalım bir. Oluştursun görelim."
//
// Bugün Overview'ın throughput'u SPAN türevli (spanMetricBatch →
// service_summary_5m, giriş-span ilkesi). Bu uç ikinci bir kaynak
// veriyor: Prometheus biçimli bir sayaç metriği, servis kimliği `job`
// etiketinin son bölümünde.
//
// TANILAMA UCUN ASIL İŞİ. Operatörün Grafana'sı PROMETHEUS'tan okuyor;
// o metriğin Coremetry'ye de girdiği garanti DEĞİL (collector onu OTLP
// ile iletiyor mu, adı korunuyor mu — buradan görülemez). Bu yüzden uç
// boş bir seri döndürüp susmuyor: metrik var mı, hangi `job` değerleri
// mevcut, desen ne — hepsini söylüyor. Böylece "veri yok" ile "desen
// tutmadı" ayırt edilebiliyor; boş bir grafik ikisini de aynı gösterirdi.
package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// throughputMetricSettingKey — ayarlanabilir metrik adı. Her kurulumun
// metrik adı aynı değil (collector'ın Prometheus çevirisi son eki
// değiştirebiliyor), o yüzden koda GÖMÜLMÜYOR.
const throughputMetricSettingKey = "service.throughput_metric"

// metricThroughputSampleJobs — eşleşme yokken gösterilecek örnek `job`
// değeri sayısı. Amaç operatörün gerçek değerleri GÖRMESİ; tam liste
// binlerce satır olabilir.
const metricThroughputSampleJobs = 12

// throughputMetricName — ayardan metrik adı, yoksa varsayılan.
func (s *Server) throughputMetricName(ctx context.Context) string {
	b, err := s.store.GetSetting(ctx, throughputMetricSettingKey)
	if err == nil && len(b) > 0 {
		if n := strings.TrimSpace(strings.Trim(string(b), `"`)); n != "" {
			return n
		}
	}
	return chstore.ThroughputMetricDefault
}

// metricThroughputPlan — istekten (önbellek anahtarı, CH filtresi).
//
// SAF, ve ayrı durmasının sebebi test edilebilirlik: Server.store somut
// bir *chstore.Store, yani handler'a sahte store enjekte edilemiyor ve
// uçtan uca test canlı ClickHouse ister. Karar mantığı burada durursa
// CH olmadan çivilenebiliyor — trace_count.go'daki traceCountPlan ile
// aynı kalıp.
//
// Çivilenen iki şey:
//   - ÖNBELLEK ANAHTARI TÜM GİRDİLERİ taşımalı (CLAUDE.md sert kısıtı).
//     metric ve jobLabel sorgudan geliyor; anahtardan düşerlerse farklı
//     metrikler birbirinin sonucunu okur — v0.5.187 çapraz-zehirlenme
//     sınıfı.
//   - Filtre operatörü `=~` olmalı. `=` olsaydı desen düz metin olarak
//     aranır ve HİÇBİR job eşleşmezdi; grafik sessizce boş kalırdı.
func metricThroughputPlan(service, metric, jobLabel string, from, to time.Time) (string, chstore.MetricQueryFilter) {
	pattern := chstore.JobServiceRegex(service)
	key := fmt.Sprintf("svc-metric-tput:%s:%s:%s:%s", service, metric, jobLabel, cacheBucket(from, to))
	return key, chstore.MetricQueryFilter{
		Name:        metric,
		Filters:     []chstore.FilterExpr{{Key: jobLabel, Op: "=~", Values: []string{pattern}}},
		Aggregation: "sum",
		From:        from,
		To:          to,
	}
}

// getServiceMetricThroughput — GET /api/services/{name}/metric-throughput
//
// `?metric=` ile ad geçici olarak ezilebiliyor: operatör doğru adı
// ararken her denemede ayar kaydetmek zorunda kalmasın.
func (s *Server) getServiceMetricThroughput(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	from, to := parseFromTo(r, time.Hour)

	metric := strings.TrimSpace(r.URL.Query().Get("metric"))
	if metric == "" {
		metric = s.throughputMetricName(r.Context())
	}
	jobLabel := strings.TrimSpace(r.URL.Query().Get("jobLabel"))
	if jobLabel == "" {
		jobLabel = chstore.JobLabelDefault
	}
	key, filter := metricThroughputPlan(name, metric, jobLabel, from, to)
	pattern := filter.Filters[0].Values[0]
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		out := map[string]any{
			"metric":   metric,
			"jobLabel": jobLabel,
			"pattern":  pattern,
			"service":  name,
		}

		// 1) ADI ÇÖZ. Tek bir varsayılan yetmiyor: aynı ölçüm kuruluma
		// göre beş farklı adla geliyor (OTel semconv sürüm değiştirdi,
		// Micrometer kendi adını koyuyor, Prometheus çevirisi sonek
		// ekliyor). Operatöre "doğru adı bul ve yaz" demek yerine sunucu
		// sırayla deniyor — operatör-bildirimi v0.9.668'in çözdüğü şey.
		resolved, tried := s.resolveThroughputMetric(ctx, metric)
		out["tried"] = tried
		if resolved == "" {
			out["metricExists"] = false
			out["suggestions"] = s.suggestMetricNames(ctx, metric)
			return out, nil
		}
		out["metric"] = resolved
		out["metricExists"] = true

		// 2) INSTRUMENT belirle — hangi KOLON rate'lenecek.
		//
		// Sayaçta ölçüm `value`da, histogramda `count` kolonunda (gözlem
		// sayısı). OTel'in HTTP server metriği çoğu kurulumda HİSTOGRAM;
		// v0.9.665 sabit 'sum' okuduğu için tam da en yaygın durumda
		// sessizce boş dönüyordu ve tanılama kutusu operatörü yanlış
		// yöne — deseni düzeltmeye — gönderiyordu.
		instrument := s.store.MetricInstrument(ctx, resolved, "")
		out["instrument"] = instrument
		rate := s.store.QueryMetricRate
		if instrument == "histogram" {
			rate = s.store.QueryMetricCountRate
		} else if instrument != "sum" {
			// gauge / bilinmeyen: rate anlamsız. Sessizce boş seri
			// döndürmek yerine sebebi söyle.
			out["unsupportedInstrument"] = true
			return out, nil
		}

		_, filter := metricThroughputPlan(name, resolved, jobLabel, from, to)
		series, err := rate(ctx, filter, "rate")
		if err != nil {
			return nil, err
		}
		if len(series) > 0 {
			out["series"] = series
			out["matched"] = len(series)
			out["matchedBy"] = jobLabel
			return out, nil
		}

		// 3) `job` TUTMADI → service_name'e düş.
		//
		// Prometheus dünyasında kimlik `job` etiketinde; OTLP dünyasında
		// kaynak özniteliğinden gelen service_name'de. Operatörün
		// ekranındaki metrik Prometheus'tan geliyordu ama Coremetry'ye
		// giren OTel adlı metrik büyük olasılıkla job TAŞIMIYOR — o
		// durumda job desenini kovalamak sonsuza kadar boş döner.
		svcFilter := filter
		svcFilter.Filters = nil
		svcFilter.Service = name
		if svcSeries, err2 := rate(ctx, svcFilter, "rate"); err2 == nil && len(svcSeries) > 0 {
			out["series"] = svcSeries
			out["matched"] = len(svcSeries)
			out["matchedBy"] = "service_name"
			return out, nil
		}
		out["matched"] = 0

		// 4) İkisi de tutmadı — gerçek `job` değerlerini göster. Desen mi
		// yanlış, etiket adı mı başka, yoksa metrik job taşımıyor mu:
		// üçü ayırt edilebilsin.
		vals, err := s.store.MetricLabelValues(ctx, resolved, jobLabel, to.Sub(from))
		if err == nil {
			if len(vals) > metricThroughputSampleJobs {
				vals = vals[:metricThroughputSampleJobs]
			}
			out["sampleJobs"] = vals
			svcs := make([]string, 0, len(vals))
			for _, v := range vals {
				if svc := chstore.ServiceFromJobLabel(v); svc != "" {
					svcs = append(svcs, svc)
				}
			}
			out["sampleServices"] = svcs
		}
		return out, nil
	})
}

// suggestMetricNames — istenen ad yokken katalogdan yakın adaylar.
//
// Adın ayırt edici parçalarıyla (MetricNameProbeTokens) katalogda arama
// yapıyor. Katalog sorgusu, ham metric_points taramasının prod'da
// aştığı maliyeti taşımıyor (v0.8.396 hızlı yolu).
//
// Hata YUTULUYOR: öneri bir kolaylık, cevabın kendisi değil. Katalog
// erişilemezse ana tanılama ("metrik yok") yine de dönmeli.
func (s *Server) suggestMetricNames(ctx context.Context, want string) []string {
	seen := map[string]bool{want: true}
	var out []string
	for _, tok := range chstore.MetricNameProbeTokens(want) {
		rows, _, err := s.store.ListMetricNames(ctx, "", tok, 8, 0)
		if err != nil {
			continue
		}
		for _, m := range rows {
			if seen[m.Name] {
				continue
			}
			seen[m.Name] = true
			out = append(out, m.Name)
			if len(out) >= 10 {
				return out
			}
		}
	}
	return out
}

// resolveThroughputMetric — kullanılacak metrik adı + denenen adlar.
//
// Öncelik: açıkça istenen ad > ayardaki ad > aday listesinden İLK VAR
// OLAN. Denenen adlar da dönüyor: hiçbiri yoksa operatör neyin
// arandığını görsün, "yok" cevabı sağır kalmasın.
func (s *Server) resolveThroughputMetric(ctx context.Context, explicit string) (string, []string) {
	var cands []string
	if explicit != "" {
		cands = []string{explicit}
	} else {
		cands = append(cands, s.throughputMetricName(ctx))
		for _, c := range chstore.ThroughputMetricCandidates {
			if c != cands[0] {
				cands = append(cands, c)
			}
		}
	}
	for _, c := range cands {
		if ok, err := s.store.MetricExists(ctx, c); err == nil && ok {
			return c, cands
		}
	}
	return "", cands
}
