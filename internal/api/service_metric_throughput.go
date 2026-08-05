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
	pattern := chstore.JobServiceRegex(name)

	// Önbellek anahtarı TÜM girdileri taşıyor (CLAUDE.md sert kısıtı):
	// metrik adı ve etiket sorgudan gelebiliyor, dışarıda bırakılırsa
	// farklı metrikler birbirinin sonucunu okur.
	key := fmt.Sprintf("svc-metric-tput:%s:%s:%s:%s", name, metric, jobLabel, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		out := map[string]any{
			"metric":   metric,
			"jobLabel": jobLabel,
			"pattern":  pattern,
			"service":  name,
		}

		// 1) Metrik Coremetry'de var mı? Cevap "hayır" ise geri kalan her
		// şey gürültü — collector onu iletmiyor demektir.
		exists, err := s.store.MetricExists(ctx, metric)
		if err != nil {
			return nil, err
		}
		out["metricExists"] = exists
		if !exists {
			// Ad tutmadı — ama ölçüm başka bir adla İÇERİDE olabilir.
			// Prometheus ve OTLP aynı şeyi farklı adlandırıyor
			// (`http_server_request_duration_seconds_count` ↔
			// `http.server.request.duration`). Operatöre "yok" deyip
			// bırakmak, aramayı ona yıkmak olurdu.
			out["suggestions"] = s.suggestMetricNames(ctx, metric)
			return out, nil
		}

		// 2) Seri: sayaç olduğu için RATE. QueryMetricRate cumulative/delta
		// ayrımını ve sayaç sıfırlanmalarını zaten işliyor.
		series, err := s.store.QueryMetricRate(ctx, chstore.MetricQueryFilter{
			Name:        metric,
			Filters:     []chstore.FilterExpr{{Key: jobLabel, Op: "=~", Values: []string{pattern}}},
			Aggregation: "sum",
			From:        from,
			To:          to,
		}, "rate")
		if err != nil {
			return nil, err
		}
		out["series"] = series
		out["matched"] = len(series)

		// 3) Eşleşme YOKSA gerçek `job` değerlerini göster. Bu, ucun en
		// değerli çıktısı: desen mi tutmadı, etiket adı mı başka, yoksa
		// metrik bu kurulumda job taşımıyor mu — üçü ayırt edilebilsin.
		if len(series) == 0 {
			vals, err := s.store.MetricLabelValues(ctx, metric, jobLabel, to.Sub(from))
			if err == nil {
				if len(vals) > metricThroughputSampleJobs {
					vals = vals[:metricThroughputSampleJobs]
				}
				out["sampleJobs"] = vals
				// Örnek değerlerden ÇÖZÜLEN servis adları: operatör
				// Coremetry'nin servis adıyla job'ın son bölümünün
				// gerçekten aynı olup olmadığını tek bakışta görsün.
				svcs := make([]string, 0, len(vals))
				for _, v := range vals {
					if svc := chstore.ServiceFromJobLabel(v); svc != "" {
						svcs = append(svcs, svc)
					}
				}
				out["sampleServices"] = svcs
			}
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
