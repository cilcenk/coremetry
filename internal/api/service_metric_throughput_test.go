package api

import (
	"strings"
	"testing"
	"time"
	"os"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.665 — metrik türevli throughput planlayıcısı.
//
// Uçtan uca test mümkün DEĞİL: Server.store somut bir *chstore.Store,
// yani sahte store enjekte edilemiyor ve handler canlı ClickHouse ister.
// Bu yüzden hata riski taşıyan karar mantığı saf bir planlayıcıya
// çıkarıldı ve testler oraya bakıyor.

func planFixture(t *testing.T) (string, time.Time, time.Time) {
	t.Helper()
	to := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	return "cm-put-service", to.Add(-time.Hour), to
}

// CLAUDE.md sert kısıtı: önbellek anahtarı TÜM girdileri hash'ler.
// metric ve jobLabel sorgudan geliyor; anahtardan düşerlerse iki farklı
// metrik birbirinin sonucunu okur — v0.5.187 çapraz-zehirlenme sınıfı,
// ve bu sefer operatör YANLIŞ TRAFİK GRAFİĞİ görürdü.
func TestMetricThroughputCacheKeyCoversEveryInput(t *testing.T) {
	svc, from, to := planFixture(t)
	base, _ := metricThroughputPlan(svc, "m1", "job", from, to, 600, "", 180, "0", "", "ch")

	cases := map[string]string{
		"servis":       mustKey(metricThroughputPlan("other-service", "m1", "job", from, to, 600, "", 180, "0", "", "ch")),
		"metrik adı":   mustKey(metricThroughputPlan(svc, "m2", "job", from, to, 600, "", 180, "0", "", "ch")),
		"etiket adı":   mustKey(metricThroughputPlan(svc, "m1", "service_job", from, to, 600, "", 180, "0", "", "ch")),
		"zaman kovası": mustKey(metricThroughputPlan(svc, "m1", "job", from.Add(-48*time.Hour), to, 600, "", 180, "0", "", "ch")),
		// v0.9.706 — nokta bütçesi de girdi: farklı mdp farklı adım üretir,
		// anahtardan düşerse 400px'lik panel 1200px'in sonucunu okur.
		"nokta bütçesi": mustKey(metricThroughputPlan(svc, "m1", "job", from, to, 1200, "", 180, "0", "", "ch")),
		// v0.9.718 — kırılım ve pencere de girdi (hash-all-inputs):
		"kırılım":        mustKey(metricThroughputPlan(svc, "m1", "job", from, to, 600, "route", 180, "0", "", "ch")),
		"rate penceresi": mustKey(metricThroughputPlan(svc, "m1", "job", from, to, 600, "", 300, "0", "", "ch")),
		// v0.9.797 — dışlama seti de girdi: kural eklendiğinde sunucu
		// WHERE'e NOT match ekliyor, yani AYNI sorgu FARKLI seri döndürüyor.
		// Özet anahtardan düşerse kural eklendikten sonra 30 sn boyunca
		// dışlanmamış seriler servis edilir (v0.5.187 sınıfı).
		"dışlama seti": mustKey(metricThroughputPlan(svc, "m1", "job", from, to, 600, "", 180, "deadbeef", "", "ch")),
		// v0.9.1268 — OKUNAN DEPO da girdi. Girdinin kaynağı bir AYAR olsa
		// da çapraz-zehirlenme aynen olur: backend değişince TTL boyunca
		// eski deponun gövdesi servis edilir. Bu uçta bedeli iki katı,
		// çünkü gövde artık `source` alanı taşıyor — bayat gövde operatöre
		// YANLIŞ deponun rozetini gösterirdi, yani bu sürümün eklediği
		// dürüstlük sinyali kendisi yalan söylerdi.
		"okunan depo": mustKey(metricThroughputPlan(svc, "m1", "job", from, to, 600, "", 180, "0", "", "vm")),
	}
	for name, k := range cases {
		if k == base {
			t.Errorf("%s değişti ama önbellek anahtarı AYNI kaldı — çapraz zehirlenme", name)
		}
	}

	// Aynı girdi → aynı anahtar (yoksa önbellek hiç isabet etmez).
	if again, _ := metricThroughputPlan(svc, "m1", "job", from, to, 600, "", 180, "0", "", "ch"); again != base {
		t.Error("aynı girdi farklı anahtar üretti — önbellek asla isabet etmez")
	}
}

func mustKey(k string, _ any) string { return k }

// v0.9.774 (operatör-bildirimi: prod'da "Response time · metrik" paneli
// boş) — REGRESYON KAPISI, iki parça:
//
//  1. ZARF SÜRÜMÜ. Yanıttan latency/latencyUnit/latencyUnitKnown/
//     latencyDiag kalktı, metricUnit geldi. Girdiler DEĞİŞMEDİĞİ için
//     anahtar da değişmezdi ve rolling deploy sırasında yeni kod eski
//     zarfı 30 sn servis ederdi (v0.9.443/458 dersi: zarf değişimi
//     anahtar sürümü ister).
//  2. ÖLÜ YOL. attachMetricLatency ve yazdığı alanlar kaynakta
//     KALMAMALI; panel artık Explore'un avg yolundan besleniyor.
func TestMetricThroughputCacheKeyIsEnvelopeVersioned(t *testing.T) {
	svc, from, to := planFixture(t)
	k, _ := metricThroughputPlan(svc, "m1", "job", from, to, 600, "", 180, "0", "", "ch")
	if !strings.HasPrefix(k, "svc-metric-tput:v2:") {
		t.Errorf("zarf sürümü anahtarda değil: %q", k)
	}
}

func TestMetricLatencyPathIsGone(t *testing.T) {
	raw, err := os.ReadFile("service_metric_throughput.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripAPILineComments(string(raw))
	for _, gone := range []string{
		"attachMetricLatency",
		`out["latency"]`,
		`out["latencyUnit"]`,
		`out["latencyUnitKnown"]`,
		`out["latencyDiag"]`,
		"QueryMetricHistogramQuantiles",
		"HistogramLatencyDiag",
		"LatencyScaleToMs",
	} {
		if strings.Contains(src, gone) {
			t.Errorf("panele özel yüzdelik yolu hâlâ kaynakta: %s", gone)
		}
	}
	// Birim yerine geçti — iki eşleşme yolunda da (bağ hızlı yolu +
	// tam keşif). Biri düşerse panelin ekseni birimsiz kalır.
	if got := strings.Count(src, `out["metricUnit"] = s.metricUnitFor(`); got != 2 {
		t.Errorf("metricUnit %d yerde yazılıyor, beklenen 2 (bağ + keşif)", got)
	}
}

// Operatör `=` kullansaydık desen DÜZ METİN olarak aranır ve hiçbir job
// eşleşmezdi; grafik sessizce boş kalır, sebebi de görünmezdi.
func TestMetricThroughputFilterUsesRegexOperator(t *testing.T) {
	svc, from, to := planFixture(t)
	_, f := metricThroughputPlan(svc, "m1", "job", from, to, 600, "", 180, "0", "", "ch")

	if len(f.Filters) != 1 {
		t.Fatalf("tek filtre bekleniyordu, alınan %d", len(f.Filters))
	}
	fe := f.Filters[0]
	if fe.Op != "=~" {
		t.Errorf("operatör =~ olmalı (CH match()), alınan %q", fe.Op)
	}
	if fe.Key != "job" {
		t.Errorf("etiket adı filtreye geçmeli, alınan %q", fe.Key)
	}
	if len(fe.Values) != 1 || !strings.Contains(fe.Values[0], "cm-put-service") {
		t.Errorf("desen servis adını taşımalı, alınan %v", fe.Values)
	}
	// Desen ÇAPALI olmalı — çapasız hâli komşu servisleri de eşler.
	if !strings.HasPrefix(fe.Values[0], "^") || !strings.HasSuffix(fe.Values[0], "$") {
		t.Errorf("desen ^...$ ile çapalanmalı, alınan %q", fe.Values[0])
	}
}

// Etiket adı sorgudan geliyor; filtreye AKTARILMAZSA her kurulum "job"
// arar ve farklı etiket kullananlarda özellik sessizce çalışmaz.
func TestMetricThroughputHonoursCustomJobLabel(t *testing.T) {
	svc, from, to := planFixture(t)
	_, f := metricThroughputPlan(svc, "m1", "kubernetes_job", from, to, 600, "", 180, "0", "", "ch")
	if f.Filters[0].Key != "kubernetes_job" {
		t.Errorf("özel etiket adı filtreye geçmedi: %q", f.Filters[0].Key)
	}
}

// Pencere ve metrik adı filtreye aynen geçmeli — biri düşerse sorgu
// başka bir şey ölçer.
func TestMetricThroughputFilterCarriesWindowAndName(t *testing.T) {
	svc, from, to := planFixture(t)
	_, f := metricThroughputPlan(svc, "http_requests_total", "job", from, to, 600, "", 180, "0", "", "ch")
	if f.Name != "http_requests_total" {
		t.Errorf("metrik adı: %q", f.Name)
	}
	if !f.From.Equal(from) || !f.To.Equal(to) {
		t.Errorf("pencere aktarılmadı: %v..%v", f.From, f.To)
	}
	// Sayaç toplamı SUM olmalı: avg (varsayılan) çok seriyi ortalar ve
	// toplam trafiği olduğundan küçük gösterirdi.
	if f.Aggregation != "sum" {
		t.Errorf("toplama sum olmalı, alınan %q", f.Aggregation)
	}
}

// v0.9.1039 (env(a) part 2) — the metric-derived Throughput tile+chart
// narrow by the global env picker so they don't stay all-env while the
// span RED narrows (the ikili-hâl the brief forbids). env must hash BOTH
// the cache key (v0.5.187) AND the binding key (else an env switch serves
// a stale binding — operator directive), and it is applied as an additive
// AND conjunct so it composes with the endpoint's suffix-derived env.
func TestMetricThroughputCacheKeyCarriesEnv(t *testing.T) {
	svc, from, to := planFixture(t)
	key := func(env string) string {
		return mustKey(metricThroughputPlan(svc, "m1", "job", from, to, 600, "", 180, "0", env, "ch"))
	}
	uat, prep, all := key("uat"), key("prep"), key("")
	if uat == prep || uat == all || prep == all {
		t.Fatalf("distinct envs must produce distinct keys: uat=%q prep=%q all=%q", uat, prep, all)
	}
	if key("uat") != uat {
		t.Error("cache key unstable for identical env")
	}
}

func TestTputBindKeyCarriesEnv(t *testing.T) {
	uat, prep, all := tputBindKey("svc", "uat", "ch"), tputBindKey("svc", "prep", "ch"), tputBindKey("svc", "", "ch")
	if uat == prep || uat == all || prep == all {
		t.Fatalf("binding key must hash env: uat=%q prep=%q all=%q", uat, prep, all)
	}
	// v3 shape (a v2 source-less binding must not read as source-scoped).
	if !strings.HasPrefix(uat, "cm:tputbind:v3:") {
		t.Errorf("binding key must be v3-versioned: %q", uat)
	}
}

// v0.9.1268 — BAĞ DEPOYA GÖRE AYRIŞMALI, ve bu anahtarın en sert kısmı.
//
// Saklanan şey bir ÖNBELLEK GÖVDESİ değil, çözülmüş bir KİMLİK: metrik adı,
// instrument, kimlik etiketi, temel filtreler. Hepsi depoya özgü —
// ClickHouse'ta çözülmüş bir bağ `resource.k8s.deployment.name` etiketini ve
// OTLP yazımlı bir metrik adını taşır; VictoriaMetrics'te ne o etiket ne o
// ad vardır.
//
// Depo anahtara girmezse hata SESSİZ ve kendini gizleyen türden olur: hızlı
// yol öbür deponun kimliğiyle TEK bir rate sorgusu koşar, boş döner, bağ
// "bayat" sayılıp tam keşif çalışır ve yeniden yazılır. Yani her istek en
// pahalı yolu koşar, panel yine boş kalır, ve hiçbir yerde "yanlış bağ"
// yazmaz — düzeltilen bug'ın önbellek katmanındaki kopyası.
func TestTputBindKeySeparatesSources(t *testing.T) {
	ch, vm := tputBindKey("svc", "uat", "ch"), tputBindKey("svc", "uat", "vm")
	if ch == vm {
		t.Fatalf("bağ anahtarı depoyu ayırmalı: ch=%q vm=%q", ch, vm)
	}
	// Ve depo, servis/env ile YER DEĞİŞTİREMEMELİ: ayrı segment olmasaydı
	// ("…:vm:svc:uat" yerine düz birleştirme) bir servis adı öbür deponun
	// anahtarını taklit edebilirdi.
	if !strings.Contains(ch, ":ch:") || !strings.Contains(vm, ":vm:") {
		t.Errorf("depo ayrı bir anahtar segmenti olmalı: ch=%q vm=%q", ch, vm)
	}
}

// v0.9.1268 — kaynak yönlendiricisi kimlik ETİKETLERİNİ de belirler.
//
// ClickHouse listesi OTLP yazımlarını taşır; VictoriaMetrics'te bunlar
// alt-çizgili etiketlerdir ve listede OLMAYAN bir aday daha vardır:
// `service_name`. CH'de servis kimliği bir KOLON olduğu için o liste onu hiç
// içermiyordu — OTLP-beslemeli bir VM kurulumunda ise kimliğin en olası yeri
// tam orası. Operatörün prod'unda panel bu yüzden boştu.
func TestIdentityLabelCandidatesFollowTheSource(t *testing.T) {
	chLabels := identityLabelCandidates("", chMetricSource{})
	vmLabels := identityLabelCandidates("", vmMetricSource{})

	if len(chLabels) == 0 || len(vmLabels) == 0 {
		t.Fatalf("boş aday listesi: ch=%v vm=%v", chLabels, vmLabels)
	}
	// CH tarafı DEĞİŞMEDİ — bu sürüm ClickHouse kurulumunda hiçbir şeyi
	// oynatmamalı.
	if strings.Join(chLabels, ",") != strings.Join(chstore.ServiceIdentityLabels, ",") {
		t.Errorf("ClickHouse aday listesi değişti: %v", chLabels)
	}
	has := func(ls []string, want string) bool {
		for _, l := range ls {
			if l == want {
				return true
			}
		}
		return false
	}
	// AYIRT EDİCİ ADAY. Bu satır düşerse operatörün bug'ı geri gelir ve
	// hiçbir derleyici uyarmaz.
	if !has(vmLabels, "service_name") {
		t.Errorf("VM aday listesinde service_name yok — OTLP-beslemeli bir VM kurulumunda "+
			"kimliğin en olası yeri orası, ve v0.9.1268'in düzelttiği bug tam buydu: %v", vmLabels)
	}
	// Ve VM listesi OTLP yazımlarını taşımamalı: `resource.k8s.deployment.name`
	// MetricsQL'de geçerli bir etiket adı bile değil, öyle bir aday sessizce
	// hiçbir şey eşlemez.
	for _, l := range vmLabels {
		if strings.Contains(l, ".") {
			t.Errorf("VM aday listesinde noktalı etiket var (%q) — MetricsQL etiket adları "+
				"[a-zA-Z_][a-zA-Z0-9_]*; böyle bir aday sessizce hiç eşleşmez", l)
		}
	}
	// CH listesi service_name'i KAZANMAMALI: orada kimlik bir kolon ve
	// serviceNameAttempts onu ayrı bir düşüşle deniyor; etiket olarak da
	// denemek CH'de ölü bir sorgu turu olurdu.
	if has(chLabels, "service_name") {
		t.Error("service_name ClickHouse etiket adaylarına sızdı — orada kimlik KOLON, " +
			"ve serviceNameAttempts onu zaten deniyor")
	}
}

// withEnvFilter is the query-time conjunct. Empty env is a strict no-op;
// a set env APPENDS deployment.environment (never replaces the identity
// filter) and never mutates the caller's slice (the endpoint reuses a
// `base` filter across several serviceNameAttempts).
func TestWithEnvFilter(t *testing.T) {
	base := chstore.MetricQueryFilter{
		Name:    "http.server.duration",
		Filters: []chstore.FilterExpr{{Key: "job", Op: "=~", Values: []string{"^api-gateway$"}}},
	}

	if got := withEnvFilter(base, "", chMetricSource{}); len(got.Filters) != 1 {
		t.Fatalf("empty env must be a no-op, got %d filters", len(got.Filters))
	}

	out := withEnvFilter(base, "uat", chMetricSource{})
	if len(out.Filters) != 2 {
		t.Fatalf("env must append a conjunct: got %d filters", len(out.Filters))
	}
	if len(base.Filters) != 1 {
		t.Fatal("withEnvFilter mutated the caller's slice — a shared base would leak env across attempts")
	}
	env := out.Filters[1]
	if env.Key != "deployment.environment" || env.Op != "=" || len(env.Values) != 1 || env.Values[0] != "uat" {
		t.Errorf("unexpected env conjunct: %+v", env)
	}
	// The identity filter survives — env is additive, not a replacement.
	if out.Filters[0].Key != "job" {
		t.Errorf("identity filter dropped: %+v", out.Filters[0])
	}
}

// v0.9.1268 — SESSİZ DARALTMA YASAK: bir kaynak env kısıtını ifade
// edemiyorsa, YANLIŞ bir kısıt uydurmaz.
//
// ClickHouse'un filtre derleyicisi `deployment.environment`ı bütün semconv
// yazımlarına açıyor (metricPointsWellKnown), o yüzden tek bir `=` her
// kurulumda tutuyor. MetricsQL'de bunlar farklı ETİKETLER
// (deployment_environment vs deployment_environment_name) ve bir matcher tek
// bir etiket adı yazabiliyor: hangisini seçersek seçelim, kurulumların
// yarısında panel sessizce boşalır — düzeltmeye çalıştığımız semptomun
// birebir aynısı.
//
// Bu yüzden VM tarafı kısıtı UYGULAMIYOR ve cevap envAmbiguous ile
// işaretleniyor. Uygulanmayan kısıt İSTENENDEN GENİŞ cevap verir; yanlış
// uygulanan kısıt HİÇ cevap vermez. İlki söylenebilir, ikincisi görünmez.
func TestWithEnvFilterRefusesWhatItCannotExpress(t *testing.T) {
	base := chstore.MetricQueryFilter{
		Name:    "http.server.request.duration",
		Filters: []chstore.FilterExpr{{Key: "job", Op: "=~", Values: []string{"^api-gateway$"}}},
	}

	out := withEnvFilter(base, "uat", vmMetricSource{})
	if len(out.Filters) != 1 {
		t.Fatalf("VM env kısıtı uygulamamalı (ifade edilemiyor), alınan %d filtre: %+v",
			len(out.Filters), out.Filters)
	}
	if out.Filters[0].Key != "job" {
		t.Errorf("kimlik filtresi düştü: %+v", out.Filters[0])
	}

	// Ve kaynaklar bu konuda AYRIŞMALI — ikisi de reddetseydi ClickHouse'un
	// env daraltması sessizce ölürdü (v0.9.1039 gemiden inerdi).
	_, chOK := chMetricSource{}.EnvFilterExpr("uat")
	_, vmOK := vmMetricSource{}.EnvFilterExpr("uat")
	if !chOK {
		t.Error("ClickHouse env kısıtını ifade EDEBİLİR — reddetmesi v0.9.1039'u geri alır")
	}
	if vmOK {
		t.Error("VM env kısıtını ifade edemiyor; `true` dönmesi tek bir etiket yazımına " +
			"kilitlenip öbür kurulumlarda paneli sessizce boşaltır")
	}
	// Boş env her iki kaynakta da kısıtsız — "hepsi" bir daraltma değildir.
	if _, ok := (chMetricSource{}).EnvFilterExpr(""); ok {
		t.Error("boş env kısıt üretmemeli")
	}
}

// v0.9.1268 — env ifade edilemediğinde cevap İŞARETLENİYOR mu.
//
// Kaynak pini: envWiderThanAsked handler'da hesaplanıp envAmbiguous'a
// bağlanmalı. Bu bağ kopsa panel istenen ortamdan geniş veri çizer ve
// hiçbir şey söylemez — uat sayfasında prod trafiği, makul göründüğü için
// kimse fark etmez (v0.9.679'un adını koyduğu sınıf).
func TestEnvWiderThanAskedIsMarked(t *testing.T) {
	src := readThroughputSource(t)
	if !strings.Contains(src, "_, envApplied := src.EnvFilterExpr(env)") {
		t.Error("handler kaynağa env'i ifade edip edemediğini SORMUYOR")
	}
	if !strings.Contains(src, `envWiderThanAsked := env != "" && !envApplied`) {
		t.Error("env-genişliği hesabı yok — kısıt uygulanmadığında cevap sessizce geniş kalır")
	}
	if n := strings.Count(src, "envWiderThanAsked"); n < 3 {
		t.Errorf("envWiderThanAsked %d yerde (tanım + en az iki eşleşme yolu bekleniyor) — "+
			"bir yol işaretsiz kalırsa o yoldan gelen grafik sessizce geniş olur", n)
	}
}

// v0.9.719 — kimlik BAĞI kaynak pinleri: hızlı yol + iki pozitif + iki
// negatif yazım. Saf test Redis ister; pin, akışın kaynakta durduğunu
// çiviler (bugünün sekiz kez işe yarayan deseni).
func TestTputBindingWiring(t *testing.T) {
	src := readThroughputSource(t)
	for what, want := range map[string]int{
		"s.loadTputBinding(":  1,
		"s.storeTputBinding(": 4, // 2 pozitif eşleşme + 2 negatif çıkış
	} {
		if got := strings.Count(src, what); got != want {
			t.Errorf("%s %d yerde, beklenen %d — bağ akışı eksik/fazla", what, got, want)
		}
	}
	// Elle ?metric= ezmesi bağı NE OKUMALI NE YAZMALI.
	if !strings.Contains(src, `if metric == "" && jobLabel == "" {`) {
		t.Error("bağ hızlı yolu elle-ezme korumasız")
	}
}

// readThroughputSource — the mapper's source with comments stripped, shared by
// every scan below.
//
// The stripping is why the vacuous-pass guard exists in the pin test: a `//`
// line containing `/*` can make the naive block regex swallow real code, and a
// scan over an over-eaten source finds nothing and reports success (the
// zLayers gate ran blind for 69 releases on exactly that).
func readThroughputSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("service_metric_throughput.go")
	if err != nil {
		t.Fatal(err)
	}
	return stripAPILineComments(string(raw))
}

// v0.9.1268 — OPERATÖR-BİLDİRİMİ REGRESYON KAPISI. Bu dosya artık HİÇBİR
// metrik okumasını doğrudan s.store'dan yapmamalı.
//
// Semptom: servis Overview'unda "Throughput · metrik
// (http.server.request.duration)" paneli "bu servise eşleşen seri yok"
// diyordu; metrik VictoriaMetrics'te VARDI ve aynı metriği okuyan soldaki
// avg-by-route paneli çiziyordu. Kök neden: soldaki panel
// /api/metrics/query → s.metricSourceFor(r) yolundan geçiyordu, bu eşleyici
// ise doğrudan s.store.* çağırıyor ve ClickHouse'a çakılı kalıyordu.
//
// Kapı metricsource_test.go'nun api.go üzerindeki pininin İKİZİ ve ayrı
// durmasının sebebi o pinin kapsam notunda yazılı: orası bu dosyayı bilinçli
// olarak MUAF tutuyordu ("sabit-adlı iç okuyucular … bilinçli CH"). O muafiyet
// v0.9.1150'de, VM bir metriğin yaşadığı TEK yer olabilmeden önce verilmişti.
// Muafiyet kalkarken yerine kapı konuyor — yoksa bu dosya, hakkında hiçbir
// tarayıcının fikri olmayan tek metrik yüzeyi olarak kalırdı.
func TestThroughputMapperReadsThroughTheSourceSeam(t *testing.T) {
	src := readThroughputSource(t)

	// Vacuous-pass guard: yorum soyucu gerçek kodu yediyse aşağıdaki tarama
	// hiçbir şey bulamaz ve BAŞARI raporlar.
	for _, marker := range []string{
		"func (s *Server) getServiceMetricThroughput",
		"func (s *Server) resolveThroughputMetric",
		"func (s *Server) metricUnitFor",
	} {
		if !strings.Contains(src, marker) {
			t.Fatalf("yorum soyucu gerçek kodu yedi — %q yok, bu tarama hiçbir şey kanıtlamıyor", marker)
		}
	}

	// Yönlendirici gerçekten çağrılıyor mu.
	if !strings.Contains(src, "s.metricSourceFor(r)") {
		t.Fatal("eşleyici s.metricSourceFor(r) çağırmıyor — panel yine tek depoya çakılı")
	}

	// Ve hiçbir metrik okuması doğrudan store'a gitmiyor. Liste
	// metricsource_test.go'nunkinden GENİŞ: bu yüzeyin ihtiyaçları (rate,
	// instrument, exists, unit, presentKeys) orada yoktu, çünkü orada bu
	// dosya taranmıyordu.
	for _, m := range []string{
		"QueryMetricRate", "QueryMetricCountRate", "MetricInstrument",
		"MetricExists", "MetricUnit", "MetricPresentKeys",
		"MetricLabelValues", "ListMetricNames", "QueryMetric",
	} {
		if strings.Contains(src, "s.store."+m+"(") {
			t.Errorf("s.store.%s( doğrudan çağrılıyor — VictoriaMetrics kurulumunda bu okuma "+
				"YANLIŞ DEPODA arar ve dürüstçe boş döner (v0.9.1268 operatör bug'ı)", m)
		}
	}

	// s.store BÜSBÜTÜN yasak değil: ayar okuması ve dışlama özeti metrik
	// okuması değil, ve ikisi de ClickHouse'ta yaşıyor. Kapının kapsamı
	// "metrik okuması", "store'a hiç dokunma" değil — aksi hâlde bir sonraki
	// ayar okuması kapıyı gevşetmek için sebep olurdu.
	for _, allowed := range []string{"s.store.GetSetting(", "s.store.MetricExclusions()"} {
		if !strings.Contains(src, allowed) {
			t.Errorf("%s kayboldu — kapının kapsamı metrik OKUMASI; ayar/dışlama yolları "+
				"kasıtlı olarak store'da kalıyor", allowed)
		}
	}
}

// v0.9.1268 — cevap HANGİ DEPODA arandığını söylemeli.
//
// Operatörün ekran görüntüsünde eksik olan tek bilgi buydu: not "eşleşen seri
// yok" diyordu ama nerede aradığını yazmıyordu, ve yanlış-depo körlüğü tam
// bu yüzden görünmez kaldı. Rozet olmasa aynı hata bir daha aynı şekilde
// sessiz kalırdı.
func TestThroughputPayloadNamesTheSourceItSearched(t *testing.T) {
	src := readThroughputSource(t)
	if !strings.Contains(src, `"source": src.Name()`) {
		t.Error(`yanıt zarfı ` + "`source`" + ` taşımıyor — operatör "eşleşen seri yok" notunu ` +
			"okurken hangi depoda arandığını göremez, ve v0.9.1268'in bug'ı görünmez kalır")
	}
	// Anahtar da AYNI değerden türemeli: rozet ile okunan depo ayrışırsa
	// bayat bir gövde yanlış deponun adını gösterir.
	if !strings.Contains(src, "src.Name())") {
		t.Error("önbellek/bağ anahtarları src.Name() taşımıyor")
	}
}
