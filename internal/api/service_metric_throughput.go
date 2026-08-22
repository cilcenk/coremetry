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
	"encoding/json"
	"strconv"
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

// v0.9.1268 — OPERATÖR-BİLDİRİMİ: bu panel YANLIŞ DEPODA arıyordu.
//
// Prod'da metrik backend'i VictoriaMetrics. Servis Overview'unda soldaki
// avg-by-route paneli /api/metrics/query üzerinden gidiyor, yani kaynak
// yönlendiricisinden (s.metricSourceFor) geçiyor ve AYNI metriği çiziyordu.
// Ortadaki bu eşleyici ise doğrudan s.store.* çağırıyordu: ClickHouse'a
// çakılıydı. Beş kimlik etiketi + service_name kolonu ClickHouse'ta boş
// dönüyor, uç de dürüstçe "bu servise eşleşen seri yok" diyordu — doğru
// cevap, YANLIŞ SORU.
//
// metricsource.go'nun başlığı bu dosyayı bilinçli-CH listesinde sayıyordu
// ("sabit-adlı iç okuyucular"). O karar v0.9.1150'de, VM bir metriğin
// yaşadığı TEK yer olabilmeden önce verildi; VM birincil olunca "bilinçli
// CH" kapsam olmaktan çıkıp yanlış-depo hâline geldi. Karar silinmedi,
// bayatladığı yer işaretlendi — aynı gerekçe dql.go için hâlâ geçerli.
//
// Bu dosyadaki HER metrik okuması artık kaynaktan geçiyor: sorgu, ad
// çözümü, instrument, birim VE tanılama. Yarısını yönlendirmek, grafiğin
// bir depoyu ararken notun başka bir deponun etiketlerini anlatması
// demekti — aynı körlüğün kimsenin bakmayacağı hâli.

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
//
// v0.9.797 — `mx` (dışlama seti özeti) EKLENDİ. Bu ucun serisi
// QueryMetricRate'ten geliyor ve o yol artık WHERE'e NOT match ekliyor:
// özet anahtarda olmasaydı kural eklendikten sonra 30 sn boyunca eski
// (dışlanmamış) seriler servis edilirdi. Kuralsız kurulumda değer sabit
// "0", yani anahtar kardinalitesi değişmez.
// v0.9.1039 (env(a) part 2) — env is the LAST arg on both the cache key
// and the plan. It hashes into the key so an env-scoped response never
// cross-poisons the all-env one (v0.5.187); the FILTER stays env-free —
// env is applied as a query-time conjunct by withEnvFilter right before
// each rate() call, so the resolved binding (stored identity) and the
// global picker env compose as an additive AND without double-baking.
// v0.9.1268 — `src` (okunacak BACKEND) anahtara girdi, ve sondaki `env`den
// SONRA değil ÖNCE değil: ayrı bir `:src=` segmenti olarak. Sebep her metrik
// anahtarınınkiyle aynı (metricsource.go): girdi bir AYAR olduğunda da
// v0.5.187 çapraz-zehirlenmesi aynen olur — Settings'te backend değişince
// TTL boyunca eski deponun gövdesi servis edilir, ve iki pod farklı anlarda
// tazelendiğinde birbirine zıt cevap verir. Bu uçta bedeli daha da somut:
// gövde artık `source` alanı taşıyor, yani bayat bir gövde operatöre YANLIŞ
// deponun adını rozetle gösterirdi — düzeltmenin kendisi yalan söylerdi.
//
// Segment eklemek aynı zamanda tüm eski anahtarları bir kez geçersiz kılıyor,
// yani `:v2:` zarf sürümünü ayrıca artırmaya gerek yok: rolling deploy
// sırasında hiçbir eski gövdeye ulaşılamaz.
func metricThroughputCacheKey(service, metric, jobLabel string, from, to time.Time, mdp int, breakdown string, rateWin int, mx, env, src string) string {
	// mdp ANAHTARDA (hash-all-inputs, v0.5.187 sınıfı): farklı nokta
	// bütçeleri farklı adım → farklı sonuç. panelMaxDataPoints kuantalı
	// (200px kova) olduğu için kardinalite sınırlı (v0.8.270 disiplini).
	//
	// :v2: — v0.9.774. GİRDİLER aynı kaldı ama YANIT ZARFI değişti
	// (latency/latencyUnit/latencyUnitKnown/latencyDiag gitti, metricUnit
	// geldi). Sürümsüz anahtar, rolling deploy sırasında eski zarfı yeni
	// koda servis ederdi: panel 30 sn boyunca birimsiz çizerdi. Zarf
	// değişimi anahtar sürümü ister — v0.9.443/458 dersi.
	return fmt.Sprintf("svc-metric-tput:v2:src=%s:%s:%s:%s:%s:mdp%d:bd%s:rw%d:mx%s:env%s", src, service, metric, jobLabel, cacheBucket(from, to), mdp, breakdown, rateWin, mx, env)
}

func metricThroughputPlan(service, metric, jobLabel string, from, to time.Time, mdp int, breakdown string, rateWin int, mx, env, src string) (string, chstore.MetricQueryFilter) {
	pattern := chstore.JobServiceRegex(service)
	key := metricThroughputCacheKey(service, metric, jobLabel, from, to, mdp, breakdown, rateWin, mx, env, src)
	return key, chstore.MetricQueryFilter{
		Name:        metric,
		Filters:     []chstore.FilterExpr{{Key: jobLabel, Op: "=~", Values: []string{pattern}}},
		Aggregation: "sum",
		From:        from,
		To:          to,
		// v0.9.706 (parite dilim 2, px pilotu) — nokta bütçesi. Sunucu
		// desteği v0.9.105'ten beri vardı (piksel-uyarlamalı adım +
		// clampStepToExport tabanı); bunu geçiren İLK yüzey bu. 0 = px
		// bilinmiyor → eski sabit merdiven, davranış değişmez.
		MaxDataPoints: mdp,
		// v0.9.718 (operatör: Grafana by(http_route) referansı) —
		// breakdown=route → seri başına http.route; rateWindow PromQL
		// [W] eşdeğeri yumuşatma (kesikli şikâyeti).
		GroupBy:       breakdownGroupBy(breakdown),
		RateWindowSec: rateWin,
	}
}

// withEnvFilter — global Topbar env picker as an additive AND conjunct on
// a metric read (v0.9.1039). deployment.environment resolves via the
// metric filter compiler (metricPointsWellKnown → metricEnvExpr, which
// coalesces deployment.environment.name ≥1.27 and the older spelling), so
// NO schema change: the metric_points res_keys carry it verbatim.
//
// Composes correctly with the endpoint's own suffix-derived env
// (serviceNameAttempts): on a suffix-less service (checkout) it is the
// only env constraint; on a suffix service (bsa-deposit-uat) it is an
// extra conjunct on top of the suffix env — redundant when they agree,
// honestly empty when they disagree. Copies the slice so a stored/base
// filter is never mutated.
// v0.9.1268 — the conjunct now comes FROM THE SOURCE, and a source is allowed
// to say it cannot express one.
//
// ClickHouse still returns the same `deployment.environment` expr, so its
// behaviour is byte-identical. VictoriaMetrics returns false: a MetricsQL
// matcher names ONE label and the semconv spellings are different labels there,
// so any single choice silently empties the panel on half the installs.
//
// SESSİZ DARALTMA YASAK, and the refusal is the safe half of it. Not applying a
// conjunct shows MORE data than asked; applying the wrong one shows NONE and
// looks exactly like the bug this release fixes. The caller marks the wider
// answer envAmbiguous so the operator is told, rather than being handed a chart
// that narrowed by nothing.
func withEnvFilter(f chstore.MetricQueryFilter, env string, src metricSource) chstore.MetricQueryFilter {
	expr, ok := src.EnvFilterExpr(env)
	if !ok {
		return f
	}
	f.Filters = append(append([]chstore.FilterExpr(nil), f.Filters...), expr)
	return f
}

// breakdownGroupBy — panelin kırılım seçenekleri. Yalnız bilinen değerler:
// keyfi attr'a açmak yüksek-kardinalite kapısı olurdu.
func breakdownGroupBy(breakdown string) []string {
	if breakdown == "route" {
		return []string{"http.route"}
	}
	return nil
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
	// px bütçesi — rollup_routes ile aynı sözleşme: 0 → sabit merdiven,
	// clamp 4000 (üst sınır savunması; FE zaten ≤2000 üretir).
	mdp, _ := strconv.Atoi(r.URL.Query().Get("maxDataPoints"))
	if mdp < 0 {
		mdp = 0
	}
	if mdp > 4000 {
		mdp = 4000
	}
	// v0.9.718 — kırılım + rate penceresi. Pencere verilmezse PromQL
	// alışkanlığına yakın varsayılan: 3×step bandında ama en az 60 sn,
	// en çok 600 sn (Grafana referansı [3m]@1s). step bilinmiyorsa
	// (auto) 180 sn.
	breakdown := strings.TrimSpace(r.URL.Query().Get("breakdown"))
	if breakdown != "" && breakdown != "route" {
		breakdown = "" // bilinmeyen kırılım sessizce kapalı — 400 değil
	}
	rateWin, _ := strconv.Atoi(r.URL.Query().Get("rateWindow"))
	if rateWin <= 0 {
		rateWin = 180
	}
	if rateWin > 600 {
		rateWin = 600
	}

	// v0.9.1039 (env(a) part 2) — global Topbar env picker. Applied as an
	// additive AND conjunct (withEnvFilter) on every rate() below, so the
	// metric-derived Throughput tile+chart narrow with the span RED. env
	// hashes the cache AND binding keys (else env-switch serves a stale
	// binding — operator directive); the conjunct itself is never stored,
	// only layered at query time.
	env := strings.TrimSpace(r.URL.Query().Get("env"))

	// v0.9.1268 — HANGİ DEPO. Bu satır olmadan aşağıdaki her okuma
	// ClickHouse'a gidiyordu; operatörün prod'unda metrik VM'de yaşıyor.
	// Yönlendirici hem ?metricsrc= denemesini hem Settings varsayılanını
	// çözüyor ve dönen DEĞER hem anahtara hem okumaya giriyor — ikisinin
	// ayrışması bu yüzden imkânsız (metricsource.go sözleşmesi).
	src, err := s.metricSourceFor(r)
	if err != nil {
		writeErr(w, err)
		return
	}

	// mx: dışlama seti özeti — seri QueryMetricRate'ten geliyor ve o yol
	// artık kuralları uyguluyor. VM tarafında dışlama kuralları YOK (motor
	// ClickHouse'un WHERE'ine yazıyor), ama özet anahtarda KALIYOR: fazladan
	// anahtarlamak yalnız gereksiz bir soğuk okuma yapar, eksik anahtarlamak
	// yanlış gövde servis eder.
	mx := s.store.MetricExclusions().Digest()
	key := metricThroughputCacheKey(name, metric, jobLabel, from, to, mdp, breakdown, rateWin, mx, env, src.Name())
	pattern := chstore.JobServiceRegex(name)
	// envApplied: kaynak env kısıtını İFADE EDEBİLİYOR mu (VM edemiyor).
	// Edemiyorsa cevap daha GENİŞ olur, daha dar değil — ve bunu söylemek
	// zorundayız, yoksa uat sayfasında prod trafiği sessizce görünür
	// (v0.9.679'un envAmbiguous'ı tam bu sınıf için var).
	_, envApplied := src.EnvFilterExpr(env)
	envWiderThanAsked := env != "" && !envApplied
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		out := map[string]any{
			"metric":   metric,
			"jobLabel": jobLabel,
			"pattern":  pattern,
			"service":  name,
			// v0.9.1268 — HANGİ DEPODA ARANDI. Operatörün ekran görüntüsünde
			// eksik olan tek bilgi buydu: not "eşleşen seri yok" diyordu ama
			// nerede aradığını söylemiyordu, ve yanlış-depo körlüğü tam da bu
			// yüzden görünmez kaldı. Rozet, aynı hatanın bir daha sessiz
			// kalmasını imkânsız kılıyor.
			"source": src.Name(),
			// v0.9.719 — tanılanabilirlik (deploy doğrulamasının bulgusu:
			// parametre yankılanmıyordu, etkisi doğrulanamıyordu).
			"rateWindow": rateWin,
			"breakdown":  breakdown,
		}

		// 0) BAĞ HIZLI YOLU (v0.9.719). Daha önce çözülmüş kimlik varsa
		// keşfi tamamen atla: tek rate sorgusu. Boş dönerse bağ bayat —
		// aşağıdaki tam keşfe düş (ve keşif sonunda yeniden yazılır).
		// ?metric= ile elle ezme keşif ister — bağ atlanır.
		if metric == "" && jobLabel == "" {
			if b := s.loadTputBinding(ctx, name, env, src.Name()); b != nil {
				if b.None {
					// Negatif bağ: son tanılama zarfını aynen döndür —
					// metriksiz servis her 30 sn'de keşif koşturmasın.
					var cached map[string]any
					if json.Unmarshal(b.Diag, &cached) == nil {
						return cached, nil
					}
				} else {
					f := chstore.MetricQueryFilter{
						Name: b.Metric, From: from, To: to,
						Aggregation: "sum", MaxDataPoints: mdp,
						GroupBy:       breakdownGroupBy(breakdown),
						RateWindowSec: rateWin,
						Service:       b.Service,
						Filters:       b.Filters,
					}
					rate := src.QueryMetricRate
					if b.Instrument == "histogram" {
						rate = src.QueryMetricCountRate
					}
					if ser, err := rate(ctx, withEnvFilter(f, env, src), "rate"); err == nil && len(ser) > 0 {
						out["metric"] = b.Metric
						out["rtMetric"] = src.LatencyMetricName(b.Metric)
						out["metricExists"] = true
						out["instrument"] = b.Instrument
						out["series"] = ser
						out["matched"] = len(ser)
						out["matchedBy"] = b.MatchedBy
						out["binding"] = "hit" // tanılama: hızlı yol
						if b.EnvAmbiguous || envWiderThanAsked {
							out["envAmbiguous"] = true
						}
						out["metricUnit"] = s.metricUnitFor(ctx, src, src.LatencyMetricName(b.Metric), name)
						return out, nil
					}
					// bayat bağ → düş ve yeniden keşfet
				}
			}
		}

		// 1) ADI ÇÖZ. Tek bir varsayılan yetmiyor: aynı ölçüm kuruluma
		// göre beş farklı adla geliyor (OTel semconv sürüm değiştirdi,
		// Micrometer kendi adını koyuyor, Prometheus çevirisi sonek
		// ekliyor). Operatöre "doğru adı bul ve yaz" demek yerine sunucu
		// sırayla deniyor — operatör-bildirimi v0.9.668'in çözdüğü şey.
		resolved, tried := s.resolveThroughputMetric(ctx, src, metric)
		out["tried"] = tried
		if resolved == "" {
			out["metricExists"] = false
			out["suggestions"] = s.suggestMetricNames(ctx, src, metric)
			// v0.9.719 — negatif bağ: tanılama zarfı 10 dk saklanır,
			// metriksiz servis her yüklemede keşif koşturmaz. Elle
			// ?metric= ezmesi bağa YAZILMAZ (keşif zaten istenmişti).
			if metric == "" && jobLabel == "" {
				if diag, err := json.Marshal(out); err == nil {
					s.storeTputBinding(ctx, name, env, src.Name(), tputBinding{None: true, Diag: diag})
				}
			}
			return out, nil
		}
		out["metric"] = resolved
		// v0.9.1274 — İKİ AD, İKİ SORU (operatör-bildirimi).
		//
		// `metric` throughput'un çözdüğü seridir ve VM'de bu meşru olarak
		// `…_seconds_count` olabilir: histogramın hızı `rate(_count)`.
		// `rtMetric` ise DEĞER okuyan panellerin (avg / latency) adıdır —
		// aynı ailenin soneksiz hâli. Overview iki panelini de tek "çözülmüş
		// ad" alanından besliyordu; bu yüzden RT panelleri kümülatif bir
		// sayacın ham ortalamasını çizdi ve `_seconds` yüzünden eksende
		// "14.2 weeks" yazdı.
		//
		// Alan tputBinding'e YAZILMIYOR: kural saf (src.LatencyMetricName),
		// uçuşta hesaplanıyor. Bağa yazsaydık kuralın bir sonraki sürümdeki
		// düzeltmesi, önceden yazılmış bağların TTL'i boyunca sessizce eski
		// cevabı servis ederdi — v0.5.187 sınıfının kalıcı-durum hâli.
		//
		// Anahtar sürümü (`:v2:`) BİLEREK artmadı, oysa zarf büyüdü. Kural
		// (v0.9.774) zarf değişiminde sürüm ister çünkü eski gövde yeni koda
		// EKSİK bir alanla ulaşır; burada o eksiklik zararsız: frontend
		// `rtMetric || metric` okuyor, yani bayat bir gövde en fazla 30 sn
		// boyunca düzeltme-öncesi davranışı sürdürür ve kendiliğinden geçer.
		// Sürümü artırmak, tüm servislerin panelini bir kez soğuk okumaya
		// zorlardı — bedeli faydasından büyük.
		out["rtMetric"] = src.LatencyMetricName(resolved)
		out["metricExists"] = true

		// 2) INSTRUMENT belirle — hangi KOLON rate'lenecek.
		//
		// Sayaçta ölçüm `value`da, histogramda `count` kolonunda (gözlem
		// sayısı). OTel'in HTTP server metriği çoğu kurulumda HİSTOGRAM;
		// v0.9.665 sabit 'sum' okuduğu için tam da en yaygın durumda
		// sessizce boş dönüyordu.
		instrument := src.MetricInstrument(ctx, resolved, name)
		if instrument == "" {
			// Servis kapsamında satır yok (kimlik başka etikette
			// olabilir) — kapsamsız prob'a düş.
			instrument = src.MetricInstrument(ctx, resolved, "")
		}
		out["instrument"] = instrument
		rate := src.QueryMetricRate
		if instrument == "histogram" {
			rate = src.QueryMetricCountRate
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
		// v0.9.1268 — adaylar KAYNAKTAN. ClickHouse'un listesi
		// `resource.k8s.deployment.name` gibi OTLP yazımları taşıyor;
		// VictoriaMetrics'te bunlar `k8s_deployment_name` etiketleri ve
		// listede OLMAYAN bir aday daha var: `service_name`. CH'de servis
		// kimliği bir KOLON olduğu için o liste onu hiç içermiyordu, VM'de
		// ise kaynak özniteliği sıradan bir etiket olarak düşüyor ve
		// OTLP-beslemeli kurulumda kimliğin EN OLASI yeri orası.
		labels := identityLabelCandidates(jobLabel, src)
		triedLabels := make([]string, 0, len(labels)+1)
		var matched *chstore.MetricQueryFilter
		var candErrs []string
		for _, lb := range labels {
			triedLabels = append(triedLabels, lb)
			_, f := metricThroughputPlan(name, resolved, lb, from, to, mdp, breakdown, rateWin, mx, env, src.Name())
			ser, err := rate(ctx, withEnvFilter(f, env, src), "rate")
			if err != nil {
				// v0.9.683 — TEK ADAYIN HATASI TÜM CEVABI ÖLDÜRMESİN.
				//
				// Burada `return nil, err` vardı: 5 kimlik adayından
				// herhangi biri patlayınca uç 500 dönüyordu, frontend de
				// hata durumunda hiçbir şey çizmediği için operatör
				// "panel gelmiyor" görüyordu — sebepsiz. Bugün altı kez
				// düzelttiğim sessiz-başarısızlık sınıfının kendi
				// hata yolumdaki hâli.
				//
				// Aday sırayla denenen bir LİSTE; birinin çalışmaması
				// diğerlerini denememek için sebep değil.
				candErrs = append(candErrs, lb+": "+err.Error())
				continue
			}
			if len(ser) > 0 {
				out["series"] = ser
				out["matched"] = len(ser)
				out["matchedBy"] = lb
				mf := f
				matched = &mf
				// v0.9.719 — bağı kalıcıla: sonraki istekler keşifsiz.
				s.storeTputBinding(ctx, name, env, src.Name(), tputBinding{
					Metric: resolved, Instrument: instrument,
					Kind: "label", Label: lb, Filters: f.Filters,
					MatchedBy: lb,
				})
				break
			}
		}

		// 4) Etiketlerin hiçbiri tutmadı → service_name KOLONUNA düş.
		//
		// Prometheus dünyasında kimlik bir etikette; OTLP dünyasında
		// kaynak özniteliğinden gelen service_name kolonunda.
		if matched == nil {
			_, base := metricThroughputPlan(name, resolved, chstore.JobLabelDefault, from, to, mdp, breakdown, rateWin, mx, env, src.Name())
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
				svcSeries, err2 := rate(ctx, withEnvFilter(svcFilter, env, src), "rate")
				if err2 != nil {
					candErrs = append(candErrs, at.Label()+": "+err2.Error())
					continue
				}
				if len(svcSeries) == 0 {
					continue
				}
				out["series"] = svcSeries
				out["matched"] = len(svcSeries)
				out["matchedBy"] = at.Label()
				out["envAmbiguous"] = at.EnvAmbiguous || envWiderThanAsked
				mf := svcFilter
				matched = &mf
				s.storeTputBinding(ctx, name, env, src.Name(), tputBinding{
					Metric: resolved, Instrument: instrument,
					Kind: "svc", Service: at.Service, Filters: at.Filters,
					EnvAmbiguous: at.EnvAmbiguous, MatchedBy: at.Label(),
				})
				break
			}
		}
		out["triedLabels"] = triedLabels
		// v0.9.1268 — kaynak env kısıtını İFADE EDEMİYORSA (VM), eşleşen
		// seri istenen ortamdan GENİŞ olabilir. Etiket yolu bunu kendi
		// başına bilemez — svcAttempt'ın EnvAmbiguous'ı yalnız eksiz-ad
		// düşüşünü kapsıyor — o yüzden işaret burada, iki eşleşme yolunun da
		// üstünde konuyor. Daraltma yapmadan susmak, uat sayfasında prod
		// trafiğini sessizce göstermek olurdu.
		if envWiderThanAsked {
			out["envAmbiguous"] = true
		}
		if matched == nil && metric == "" && jobLabel == "" {
			// v0.9.719 — kimlik bulunamadı: negatif bağ (10 dk).
			if diag, err := json.Marshal(out); err == nil {
				s.storeTputBinding(ctx, name, env, src.Name(), tputBinding{None: true, Diag: diag})
			}
		}
		if len(candErrs) > 0 {
			// Hatalar SESSİZ KALMASIN: bir aday teknik bir sebeple
			// çalışmıyorsa operatör bunu görmeli, "eşleşme yok" ile
			// karıştırmamalı.
			out["candidateErrors"] = candErrs
		}

		// 5) BİRİM — çözülen metriğin OTLP birimi.
		//
		// v0.9.774: burada PANELE ÖZEL bir yüzdelik yolu vardı
		// (attachMetricLatency → histogram kovalarından P50/P95/P99).
		// Prod'da bu yol sessizce boş dönüyordu: metric_points satırları
		// bucket_counts taşıyıp bucket_bounds taşımıyor, sınırsız kovadan
		// yüzdelik hesaplanamıyor. Panel artık Explore'un ÇALIŞAN avg
		// yolundan (GET /api/metrics/query) besleniyor, yani bu uçtan
		// yalnız KİMLİK bilgisi isteniyor: metriğin adı (yukarıda) ve
		// birimi (burada) — çizimi başka bir uç yapıyor.
		//
		// Birim ölçekleme YOK: frontend değeri @grafana/data'nın display
		// processor'ına birimiyle veriyor (dataFrame.ts sözleşmesi:
		// "birim ölçekleme elle yazılmaz"). Eskiden burada ms'ye
		// çevriliyordu çünkü çizim katmanı birim bilmiyordu.
		if matched != nil {
			// v0.9.1274 — birim RT ADINDAN. Bu alanı okuyan tek yüzey
			// Response time paneli/karosu (throughput ekseni sabit "reqps"),
			// yani birim LATENCY ailesini tanımlamalı. VM'de ikisi de "s"
			// çıkar (aile aday listesi `_count`i zaten kapsıyor), CH'de ad
			// değişmediği için sorgu bayt-aynı — ama alanın hangi soruyu
			// yanıtladığı artık kodda yazıyor.
			out["metricUnit"] = s.metricUnitFor(ctx, src, src.LatencyMetricName(resolved), name)
			return out, nil
		}
		out["matched"] = 0

		// 5) Hiçbiri tutmadı — gerçek etiket değerlerini göster.
		// Operatör Coremetry'nin servis adıyla metriğin taşıdığı değeri
		// yan yana görsün.
		// v0.9.682 — HANGİ ADAYLAR KURULUMDA VAR?
		//
		// "denenen adaylar" listesi hangi yolun DENENDİĞİNİ söylüyordu
		// ama hangisinin var olduğunu söylemiyordu. Yerel ölçüm bunun
		// neden şart olduğunu gösterdi: 7 kimlik adayından 5'i yerelde
		// TAM SIFIR (474 bin satırın hiçbirinde yok). O dallar hiç icra
		// edilmiyor ve "boş sonuç" bunu hiç belli etmiyordu.
		//
		// anahtar YOK  → collector o kimliği göndermiyor
		// anahtar VAR  → değer beklediğimizden farklı
		// İkisi bambaşka eylem; boş grafik ikisini de aynı gösterirdi.
		// v0.9.1268 — tanılama da AYNI depodan. Bu satır s.store'da kalsaydı
		// grafik VM'de ararken not ClickHouse'un anahtarlarını anlatırdı;
		// "hiçbir aday yok" cümlesi doğru görünüp yanlış depoyu tarif ederdi.
		out["presentKeys"] = src.MetricPresentKeys(ctx, resolved,
			append(identityLabelCandidates(jobLabel, src), chstore.EnvAttrKeys...), to.Sub(from))

		probeLabel := jobLabel
		if probeLabel == "" {
			probeLabel = chstore.JobLabelDefault
		}
		vals, err := src.MetricLabelValues(ctx, resolved, probeLabel, to.Sub(from))
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
func (s *Server) suggestMetricNames(ctx context.Context, src metricSource, want string) []string {
	seen := map[string]bool{want: true}
	var out []string
	for _, tok := range chstore.MetricNameProbeTokens(want) {
		rows, _, err := src.ListMetricNames(ctx, "", tok, 8, 0)
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
func (s *Server) resolveThroughputMetric(ctx context.Context, src metricSource, explicit string) (string, []string) {
	cands := throughputMetricCandidates(explicit, s.throughputMetricName(ctx))
	for _, c := range cands {
		if ok, err := src.MetricExists(ctx, c); err == nil && ok {
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

// v0.9.1268 — aday listesi KAYNAĞIN. Etiket adları depoya göre farklı
// YAZILIYOR (VM'de `k8s_deployment_name`, CH'de `resource.k8s.deployment.name`)
// ve VM'de listede hiç olmayan bir aday var: `service_name`. Listeyi burada
// sabitlemek, VM kurulumunda beş etiket denemek ve altıncısının — kimliğin
// gerçekten durduğu yerin — hiç denenmemesi demekti.
func identityLabelCandidates(explicit string, src metricSource) []string {
	if explicit != "" {
		return []string{explicit}
	}
	return src.ServiceIdentityLabels()
}

// metricUnitFor — çözülen metriğin OTLP birimi ("s", "ms", "By", …).
//
// v0.9.774. Servis kapsamlı prob önce; boş dönerse kapsamsız (kimlik
// başka bir etikette olabilir — metric_points.service_name yazılmamış
// olsa da metrik var). Bu iki-adımlı düşüş v0.9.676'dan beri
// attachMetricLatency içinde yaşıyordu; yüzdelik yolu silinirken
// KORUNDU çünkü ölçtüğü şey doğru: birimi olmayan bir metriğin ekseni
// de olmaz.
//
// Birim BURADA ÖLÇEKLEMEYE ÇEVRİLMİYOR. Çizim @grafana/data'nın display
// processor'ında; ms/s dönüşümünü elle yazmak dataFrame.ts sözleşmesinin
// açıkça yasakladığı şey. Uç yalnız birimi SÖYLER, panel eksenine basar.
func (s *Server) metricUnitFor(ctx context.Context, src metricSource, metric, service string) string {
	if u := src.MetricUnit(ctx, metric, service); u != "" {
		return u
	}
	return src.MetricUnit(ctx, metric, "")
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
	for _, key := range chstore.EnvAttrKeys {
		out = append(out, svcAttempt{
			Service: stripped,
			// v0.9.681 — TAM eşleşme DEĞİL: operatörün kurulumunda değer
			// `uat` değil `uat-ocpuat` (ortam+küme) olabiliyor. Tam
			// eşleşme böyle bir değerde hiç tutmaz ve kısıt sessizce
			// hiçbir şeyi kısıtlamaz.
			Filters: []chstore.FilterExpr{{Key: key, Op: "=~", Values: []string{chstore.EnvValueRegex(env)}}},
		})
	}
	return append(out, svcAttempt{Service: stripped, EnvAmbiguous: true})
}


// ── v0.9.719 (operatör önerisi: "bir defa keşfet, sonra hızlı gelsin") ──────
//
// Kimlik ÇÖZÜMÜ pahalı: 5 aday metrik + instrument/temporality probları +
// 5 kimlik etiketi × rate denemesi + service_name biçim denemeleri — hepsi
// SIRALI CH sorgusu ve chartsV2 ayrı cache anahtarı ürettiği için ilk v2
// yüklemesi hep soğuktu. Çözülen bağ Redis'e yazılır (pod'lar arası
// paylaşımlı, restart'a dayanıklı); sonraki istekler TEK rate sorgusuna
// iner. Bağ bayatlarsa (boş dönerse) tam keşfe düşülür ve yeniden yazılır
// — yanlışa kilitlenme yok, yalnız hızlanma.
//
// NEGATİF bağ da kısa TTL ile saklanır: metriği olmayan servis her 30
// saniyede tam keşif koşturmasın; 10 dk'da bir tazelenen tanılama yeter.

type tputBinding struct {
	// None=true → son keşif eşleşme bulamadı; Diag son tanılama zarfı.
	None bool            `json:"none,omitempty"`
	Diag json.RawMessage `json:"diag,omitempty"`

	Metric     string `json:"metric"`
	Instrument string `json:"instrument"`
	// Kind: "label" → Filters üzerinden kimlik etiketi; "svc" →
	// serviceNameAttempts biçimi (Service + Filters).
	Kind         string             `json:"kind"`
	Label        string             `json:"label,omitempty"`
	AttemptLabel string             `json:"attemptLabel,omitempty"`
	Service      string             `json:"service,omitempty"`
	Filters      []chstore.FilterExpr `json:"filters,omitempty"`
	EnvAmbiguous bool               `json:"envAmbiguous,omitempty"`
	MatchedBy    string             `json:"matchedBy"`
}

const (
	tputBindTTL    = 12 * time.Hour
	tputBindNegTTL = 10 * time.Minute
)

// v0.9.1039 (env(a) part 2) — env in the binding key. The stored binding
// holds env-INDEPENDENT identity (metric/instrument/label/service + base
// filters, never the env conjunct — that is layered at query time by
// withEnvFilter). The key is still env-scoped so an env switch can never
// serve a stale binding (operator directive); v2 because the key shape
// changed (a v1 all-env binding must not be read as an env-scoped one).
//
// v0.9.1268 — `src` KEY'E GİRDİ, ve v3 oldu. Bağ, çözülmüş bir KİMLİĞİ
// saklıyor: metrik adı, instrument, etiket adı, temel filtreler. Bunların
// hepsi depoya ÖZGÜ — ClickHouse'ta çözülen bir bağ
// `resource.k8s.deployment.name` etiketini ve OTLP yazımlı bir metrik adını
// taşır, VictoriaMetrics'te ise ne o etiket ne o ad vardır. Depo-anahtarsız
// bir bağ, backend değişince öbür deponun kimliğini uygular ve TEK bir rate
// sorgusuna iner: tam keşif atlandığı için hızlı yol sessizce boş döner ve
// bağ "bayat" sayılıp yeniden yazılır — yani her istek en pahalı yolu koşar
// ve panel yine boş kalır. Çapraz-zehirlenmenin (v0.5.187) önbellek değil
// TÜREV-DURUM hâli.
//
// v3, çünkü anahtarın ŞEKLİ değişti: bir v2 (depo-anahtarsız) bağ, depo
// kapsamlı bir bağ gibi okunmamalı.
func tputBindKey(service, env, src string) string {
	return "cm:tputbind:v3:" + src + ":" + service + ":" + env
}

func (s *Server) loadTputBinding(ctx context.Context, service, env, src string) *tputBinding {
	raw, ok, err := s.cache.Get(ctx, tputBindKey(service, env, src))
	if err != nil || !ok {
		return nil
	}
	var b tputBinding
	if json.Unmarshal(raw, &b) != nil {
		return nil
	}
	return &b
}

func (s *Server) storeTputBinding(ctx context.Context, service, env, src string, b tputBinding) {
	ttl := tputBindTTL
	if b.None {
		ttl = tputBindNegTTL
	}
	if raw, err := json.Marshal(b); err == nil {
		_ = s.cache.Set(ctx, tputBindKey(service, env, src), raw, ttl)
	}
}
