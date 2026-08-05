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
func metricThroughputCacheKey(service, metric, jobLabel string, from, to time.Time) string {
	return fmt.Sprintf("svc-metric-tput:%s:%s:%s:%s", service, metric, jobLabel, cacheBucket(from, to))
}

func metricThroughputPlan(service, metric, jobLabel string, from, to time.Time) (string, chstore.MetricQueryFilter) {
	pattern := chstore.JobServiceRegex(service)
	key := metricThroughputCacheKey(service, metric, jobLabel, from, to)
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

	// v0.9.671 — BOŞ BIRAK. v0.9.668'de burada varsayılana çevriliyordu
	// ve resolveThroughputMetric'e hep "açıkça istendi" gibi giriyordu:
	// aday listesi HİÇ denenmedi. Operatörün ekran görüntüsü bunu
	// kanıtladı — tanılama "Denenen adlar: <tek ad>" yazıyordu, oysa
	// aradığı http.server.request.duration önerilerin İÇİNDEYDİ.
	metric := strings.TrimSpace(r.URL.Query().Get("metric"))
	// jobLabel de BOŞ KALIYOR (aynı sebep): doldurulursa aşağıdaki
	// etiket-adayı döngüsü tek elemana iner ve `name` hiç denenmez.
	// Bu, v0.9.668'de metrik adında yaptığım hatanın etiket ikizi —
	// aynı gün, aynı biçimde iki kez.
	jobLabel := strings.TrimSpace(r.URL.Query().Get("jobLabel"))

	key := metricThroughputCacheKey(name, metric, jobLabel, from, to)
	pattern := chstore.JobServiceRegex(name)
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
		// sessizce boş dönüyordu.
		instrument := s.store.MetricInstrument(ctx, resolved, name)
		if instrument == "" {
			// Servis kapsamında satır yok (kimlik başka etikette
			// olabilir) — kapsamsız prob'a düş.
			instrument = s.store.MetricInstrument(ctx, resolved, "")
		}
		out["instrument"] = instrument
		rate := s.store.QueryMetricRate
		if instrument == "histogram" {
			rate = s.store.QueryMetricCountRate
		} else if instrument != "sum" {
			out["unsupportedInstrument"] = true
			return out, nil
		}

		// 3) KİMLİK ETİKETİNİ ARA.
		//
		// v0.9.671 (operatör-bildirimi): kimlik her kurulumda `job`da
		// değil — o kurulumda `name` etiketinde. Etiket adayları sırayla
		// deneniyor; eşleşme TAM DEĞER üzerinden olduğu için fazladan
		// aday yanlış eşleşme üretmiyor.
		labels := identityLabelCandidates(jobLabel)
		triedLabels := make([]string, 0, len(labels)+1)
		var matched *chstore.MetricQueryFilter
		for _, lb := range labels {
			triedLabels = append(triedLabels, lb)
			_, f := metricThroughputPlan(name, resolved, lb, from, to)
			ser, err := rate(ctx, f, "rate")
			if err != nil {
				return nil, err
			}
			if len(ser) > 0 {
				out["series"] = ser
				out["matched"] = len(ser)
				out["matchedBy"] = lb
				mf := f
				matched = &mf
				break
			}
		}

		// 4) Etiketlerin hiçbiri tutmadı → service_name KOLONUNA düş.
		//
		// Prometheus dünyasında kimlik bir etikette; OTLP dünyasında
		// kaynak özniteliğinden gelen service_name kolonunda.
		if matched == nil {
			_, base := metricThroughputPlan(name, resolved, chstore.JobLabelDefault, from, to)
			// v0.9.678 — KOLON DA İKİ BİÇİM DENİYOR.
			//
			// Operatörün sorusu ("Coremetry ingest ederken env kesiyor
			// olabilir mi?") bu boşluğu açığa çıkardı. Cevap hayır —
			// ingest birebir yazıyor (attrsToArrays) — ama tam bu yüzden
			// metriğin service_name'i EKSİZ olabiliyor: OTel'in doğru
			// yolu servis adını eksiz tutup ortamı ayrı bir kaynak
			// özniteliğinde taşımak (res_keys'te deployment.environment
			// GERÇEKTEN var). Coremetry'nin servis listesi ise trace'ten
			// gelen EKLİ adı gösteriyor.
			//
			// Etiket tarafı bunu v0.9.673'te çözmüştü (regex iki biçimi
			// de kabul ediyor); kolon tarafı TAM ADDA kalmıştı ve
			// MetricQueryFilter.Service tam eşleşme yapıyor.
			for _, at := range serviceNameAttempts(name) {
				triedLabels = append(triedLabels, at.Label())
				svcFilter := base
				svcFilter.Filters = at.Filters
				svcFilter.Service = at.Service
				svcSeries, err2 := rate(ctx, svcFilter, "rate")
				if err2 != nil || len(svcSeries) == 0 {
					continue
				}
				out["series"] = svcSeries
				out["matched"] = len(svcSeries)
				out["matchedBy"] = at.Label()
				out["envAmbiguous"] = at.EnvAmbiguous
				mf := svcFilter
				matched = &mf
				break
			}
		}
		out["triedLabels"] = triedLabels

		// 5) GECİKME — aynı eşleşen filtreden (operatör: "response time
		// için de bir panel yapabilir misin").
		//
		// Çözümlemeyi (ad → instrument → kimlik etiketi) TEKRARLAMIYOR:
		// throughput hangi seriyi bulduysa gecikme de onu okuyor. İki
		// panelin farklı serilere bakması, kıyaslamayı sessizce anlamsız
		// kılardı.
		//
		// Yalnız HISTOGRAM: yüzdelikler kova sınırlarından geliyor. Bir
		// sayaçta gecikme diye bir şey yok.
		if matched != nil && instrument == "histogram" {
			s.attachMetricLatency(ctx, out, *matched, resolved, name)
		}

		if matched != nil {
			return out, nil
		}
		out["matched"] = 0

		// 5) Hiçbiri tutmadı — gerçek etiket değerlerini göster.
		// Operatör Coremetry'nin servis adıyla metriğin taşıdığı değeri
		// yan yana görsün.
		probeLabel := jobLabel
		if probeLabel == "" {
			probeLabel = chstore.JobLabelDefault
		}
		vals, err := s.store.MetricLabelValues(ctx, resolved, probeLabel, to.Sub(from))
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
	cands := throughputMetricCandidates(explicit, s.throughputMetricName(ctx))
	for _, c := range cands {
		if ok, err := s.store.MetricExists(ctx, c); err == nil && ok {
			return c, cands
		}
	}
	return "", cands
}

// throughputMetricCandidates / identityLabelCandidates — aday listesi
// kurulumu. SAF, ve ayrı durmalarının tek sebebi bu:
//
// AYNI HATAYI BİR GÜNDE İKİ KEZ YAPTIM. Boş bir girdiyi aday
// döngüsünden ÖNCE varsayılana çevirince döngü tek elemana iniyor ve
// liste hiç denenmiyor:
//   - v0.9.668, metrik adı — operatörün ekran görüntüsü kanıtladı:
//     "Denenen adlar: <tek ad>", oysa aradığı ad önerilerin içindeydi.
//   - v0.9.671'i yazarken aynısını jobLabel'da tekrarladım.
//
// İkisi de sessiz: özellik çalışıyor görünür, yalnız hiçbir şey bulmaz.
// Kapı burada.
func throughputMetricCandidates(explicit, configured string) []string {
	if explicit != "" {
		return []string{explicit} // operatör açıkça istedi — başkasını deneme
	}
	out := []string{configured}
	for _, c := range chstore.ThroughputMetricCandidates {
		if c != configured {
			out = append(out, c)
		}
	}
	return out
}

func identityLabelCandidates(explicit string) []string {
	if explicit != "" {
		return []string{explicit}
	}
	return chstore.ServiceIdentityLabels
}

// attachMetricLatency — histogram yüzdeliklerini (P50/P95/P99) cevaba
// ekler, MİLİSANİYEYE çevirerek.
//
// v0.9.676. Birim çevirisi şart: span türevli Response time paneli ms
// çiziyor, OTel semconv duration metriği ise SANİYE. Çevirmezsek iki
// panel 1000× farklı görünür — operatör ya hata sanar ya da daha
// kötüsü, sanmaz ve yanlış sayıya güvenir.
//
// BİRİM BİLİNMİYORSA ÖLÇEKLENMİYOR ve bu cevapta SÖYLENİYOR
// (latencyUnitKnown=false). Tahmin etmek yazı-tura; yanlış ölçekli bir
// grafik, ölçeksiz olandan kötüdür.
//
// Hata YUTULUYOR: gecikme bir ek: throughput cevabı onsuz da geçerli.
func (s *Server) attachMetricLatency(ctx context.Context, out map[string]any, f chstore.MetricQueryFilter, metric, service string) {
	unit := s.store.MetricUnit(ctx, metric, service)
	if unit == "" {
		unit = s.store.MetricUnit(ctx, metric, "")
	}
	scale, known := chstore.LatencyScaleToMs(unit)
	out["latencyUnit"] = unit
	out["latencyUnitKnown"] = known

	lat := map[string][]chstore.SpanMetricSeries{}
	for _, agg := range []string{"p50", "p95", "p99"} {
		ser, err := s.store.QueryMetricHistogramPercentile(ctx, f, agg)
		if err != nil || len(ser) == 0 {
			continue
		}
		if scale != 1 {
			for i := range ser {
				for j := range ser[i].Points {
					ser[i].Points[j].Value *= scale
				}
			}
		}
		lat[agg] = ser
	}
	if len(lat) > 0 {
		out["latency"] = lat
	}
}

// svcAttempt — service_name kolonu üzerinden TEK bir deneme.
type svcAttempt struct {
	Service string
	Filters []chstore.FilterExpr // ortam kısıtı (olabilir)
	// EnvAmbiguous: ortam AYRIŞTIRILAMADI. Eşleşen seri birden çok
	// ortamın verisini taşıyor olabilir — çağıran bunu SÖYLEMEK zorunda.
	EnvAmbiguous bool
}

func (a svcAttempt) Label() string {
	l := "service_name=" + a.Service
	switch {
	case len(a.Filters) > 0:
		l += " +" + a.Filters[0].Key
	case a.EnvAmbiguous:
		l += " (ortam kısıtsız)"
	}
	return l
}

// serviceNameAttempts — service_name kolonu denemeleri, GÜVEN SIRASIYLA.
//
// v0.9.679. Operatörün SQL çıktısı belirleyiciydi: metric_points'te 1494
// servis var ve HEPSİ EKSİZ (bsa-chequenotes-notespayment,
// bsa-creditcard-ccfinancial…), oysa Coremetry'nin servis listesi
// trace'ten gelen EKLİ adı gösteriyor (...-uat). Eşleşme ancak eksiz
// adla kurulabiliyor.
//
// SESSİZ TEHLİKE: "bsa-deposit-uat" ve "bsa-deposit-prod" ikisi de
// "bsa-deposit"e iniyor. Aynı kurulum birden çok ortam taşıyorsa eksiz
// eşleşme onları BİRLEŞTİRİR — uat sayfasında prod trafiği görünür ve
// sayı makul olduğu için kimse fark etmez.
//
// Bu yüzden eksiz ada düşerken ÖNCE ortamla kısıtlanıyor. İki semconv
// yazımı da deneniyor: deployment.environment.name (≥1.27) ve
// deployment.environment (eski) — ikisi de yerel res_keys'te GERÇEKTEN
// var, yani hangisinin geleceği kuruluma bağlı.
//
// Hiçbiri tutmazsa kısıtsız deneme yapılıyor ama EnvAmbiguous ile
// İŞARETLENİYOR. Veri göstermek sessizce yanlış ortamı göstermekten
// iyi — yeter ki söylensin.
//
// SAF ve TEST EDİLDİ: bu "aday listesi" mantığını bugün üç kez elle
// yazdım, ikisini batırdım (v0.9.668 metrik adı, v0.9.671 jobLabel).
func serviceNameAttempts(service string) []svcAttempt {
	if service == "" {
		return nil
	}
	out := []svcAttempt{{Service: service}}
	stripped := chstore.StripEnvSuffix(service)
	if stripped == service || stripped == "" {
		return out
	}
	env := strings.TrimPrefix(service[len(stripped):], "-")
	for _, key := range []string{
		"resource.deployment.environment.name",
		"resource.deployment.environment",
	} {
		out = append(out, svcAttempt{
			Service: stripped,
			Filters: []chstore.FilterExpr{{Key: key, Op: "=", Values: []string{env}}},
		})
	}
	return append(out, svcAttempt{Service: stripped, EnvAmbiguous: true})
}
