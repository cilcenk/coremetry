package chstore

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// v0.9.665 — `job` etiketi eşleştirmesi.
//
// Desen kullanıcı girdisinden üretilip doğrudan bir CH match()'e giriyor.
// İki sessiz hata sınıfı var ve ikisi de burada çivileniyor: YANLIŞ
// servisi eşlemek (ekstra trafik doğru servisin grafiğine karışır) ve
// hiç eşlememek (grafik boş kalır, sebebi görünmez).

func TestJobServiceRegexMatchesNamespacedAndBare(t *testing.T) {
	re := regexp.MustCompile(JobServiceRegex("cm-put-service"))
	for _, job := range []string{
		"content-manager-prod/cm-put-service", // operatörün gerçek değeri
		"cm-put-service",                      // öneksiz kurulum
		"a/b/cm-put-service",                  // çok parçalı önek
	} {
		if !re.MatchString(job) {
			t.Errorf("%q eşleşmeliydi", job)
		}
	}
}

// EN ÖNEMLİSİ: son bölüm TAM eşleşmeli. Gevşek bir desen komşu servisin
// trafiğini içeri alır ve sonuç makul göründüğü için fark edilmez.
func TestJobServiceRegexRejectsPartialSuffix(t *testing.T) {
	re := regexp.MustCompile(JobServiceRegex("cm-put-service"))
	for _, job := range []string{
		"ns/legacy-cm-put-service", // önek YOK, ad uzantısı — başka servis
		"ns/cm-put-service-v2",     // sonek — başka servis
		"ns/cm-put",                // kısaltma
		"ns/cm-put-service/extra",  // son bölüm başka
	} {
		if re.MatchString(job) {
			t.Errorf("%q eşleşmemeliydi — yanlış servisin trafiği karışır", job)
		}
	}
}

// Regex metakarakterleri KAÇIRILMALI. Kaçışsız "." herhangi bir karaktere
// uyar; "cm.put" deseni "cmXput"u da eşlerdi.
func TestJobServiceRegexEscapesMetacharacters(t *testing.T) {
	re := regexp.MustCompile(JobServiceRegex("cm.put+service"))
	if !re.MatchString("ns/cm.put+service") {
		t.Error("düz metin eşleşmeliydi")
	}
	if re.MatchString("ns/cmXputXservice") {
		t.Error("kaçışsız metakarakter yanlış eşleşme üretti")
	}
}

// Desen HER ZAMAN geçerli bir regex olmalı: geçersizse CH sorgusu
// patlar ve operatör grafiği hiç göremez.
func TestJobServiceRegexAlwaysCompiles(t *testing.T) {
	for _, svc := range []string{
		"a", "a-b", "a.b", "a+b", "a*b", "a(b", "a[b", "a\\b", "a|b", "a$b", "a^b", "",
	} {
		if _, err := regexp.Compile(JobServiceRegex(svc)); err != nil {
			t.Errorf("%q: desen derlenmedi: %v", svc, err)
		}
	}
}

func TestServiceFromJobLabel(t *testing.T) {
	cases := map[string]string{
		"content-manager-prod/cm-put-service": "cm-put-service",
		"cm-put-service":                      "cm-put-service", // öneksiz
		"a/b/c":                               "c",              // son bölüm
		"  ns/svc  ":                          "svc",            // boşluk kırpılır
		"ns/":                                 "",               // çağıran atlamalı
		"":                                    "",
	}
	for in, want := range cases {
		if got := ServiceFromJobLabel(in); got != want {
			t.Errorf("ServiceFromJobLabel(%q) = %q, beklenen %q", in, got, want)
		}
	}
}

// Gidiş-dönüş: bir job değerinden çıkarılan servis, o job'ı geri
// eşlemeli. İki fonksiyon ayrışırsa liste bir servisi gösterir ama
// grafiği boş gelir.
func TestJobRoundTrip(t *testing.T) {
	for _, job := range []string{
		"content-manager-prod/cm-put-service",
		"cm-put-service",
		"a/b/c",
	} {
		svc := ServiceFromJobLabel(job)
		if !regexp.MustCompile(JobServiceRegex(svc)).MatchString(job) {
			t.Errorf("%q → servis %q → geri eşleşmedi", job, svc)
		}
	}
}

// v0.9.665 — ad bulunamadığında "bunu mu demek istediniz".
//
// Prometheus ve OTLP aynı ölçümü farklı adlandırıyor; operatörün
// Grafana'sındaki ad Coremetry'de birebir olmayabilir.
func TestMetricNameProbeTokens(t *testing.T) {
	cases := map[string][]string{
		// v0.9.668 — "server" AYIRT EDİCİ ve ilk sırada olmalı.
		//
		// İlk sürümde jenerik sayılıp atılmıştı. Operatörün prod
		// ekranındaki sonuç: önerilerin TAMAMI http.client.* geldi,
		// aradığı http.server.* hiç görünmedi. Bir HTTP metriğinde
		// server/client, adı ayıran en belirleyici parça.
		"http_server_request_duration_seconds_count": {"server", "request"},
		"http.server.request.duration":               {"server", "request"},
		"http.client.request.duration":               {"client", "request"},
		// Birim/toplama ekleri hâlâ atılıyor: "seconds"/"count"/"total"
		// katalogda binlerce satır eşler.
		"http_server_seconds_total": {"server"},
		"":                          nil,
		// Kısa parçalar (<3) atlanıyor.
		"db_a_query_time": {"query", "time"},
	}
	for in, want := range cases {
		got := MetricNameProbeTokens(in)
		if len(got) != len(want) {
			t.Errorf("%q → %v, beklenen %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%q → %v, beklenen %v", in, got, want)
				break
			}
		}
	}
}

// ASIL REGRESYON (operatör-bildirimi, prod): server metriği aranırken
// öneriler client metrikleriyle dolmuştu. Ayırt edici parça korunmazsa
// aynı şey tekrarlanır.
func TestMetricNameProbeKeepsServerClientDiscriminator(t *testing.T) {
	for name, side := range map[string]string{
		"http.server.request.duration": "server",
		"http.client.request.duration": "client",
	} {
		toks := MetricNameProbeTokens(name)
		if len(toks) == 0 || toks[0] != side {
			t.Errorf("%q → %v; %q İLK parça olmalı, yoksa öneriler karşı tarafla dolar", name, toks, side)
		}
	}
}

// Aday listesi throughput için gerçek adları taşımalı ve OTel semconv
// önce gelmeli — ilk VAR OLAN kazanıyor.
func TestThroughputMetricCandidateOrder(t *testing.T) {
	if len(ThroughputMetricCandidates) < 3 {
		t.Fatal("aday listesi fazla dar")
	}
	if ThroughputMetricCandidates[0] != "http.server.request.duration" {
		t.Errorf("en yeni OTel semconv başta olmalı, alınan %q", ThroughputMetricCandidates[0])
	}
	// Varsayılan da listede olmalı, yoksa ayar bir adayı atlar.
	var found bool
	for _, c := range ThroughputMetricCandidates {
		if c == ThroughputMetricDefault {
			found = true
		}
	}
	if !found {
		t.Errorf("varsayılan %q aday listesinde yok", ThroughputMetricDefault)
	}
}

// En çok İKİ parça: her parça ayrı bir katalog sorgusu.
func TestMetricNameProbeTokensCapped(t *testing.T) {
	got := MetricNameProbeTokens("alpha_beta_gamma_delta_epsilon")
	if len(got) > 2 {
		t.Errorf("en çok 2 parça olmalı, alınan %v", got)
	}
}

// v0.9.673 — OPERATÖRÜN GERÇEK DEĞERLERİ.
//
// Meslektaşının Prometheus çıktısı: job = "<namespace>/<deployment>" ve
// deployment kısmı ortam ekini TAŞIYOR:
//
//	deposit/bsa-deposit-commondeposithesapsl-uat
//
// Ama Metric Explorer ekran görüntüsündeki `name` etiketi ekSİZ:
//
//	bsa-chatbot-ai-integration      (servis: ...-uat)
//
// Aynı kurulumda iki biçim birden. Tek biçimi aramak diğerini kaçırır.
func TestJobServiceRegexMatchesBothEnvSuffixForms(t *testing.T) {
	// (1) Ek TAŞIYAN job değeri — namespace önekli.
	re := regexp.MustCompile(JobServiceRegex("bsa-deposit-commondeposithesapsl-uat"))
	for _, v := range []string{
		"deposit/bsa-deposit-commondeposithesapsl-uat", // operatörün gerçek job'ı
		"bsa-deposit-commondeposithesapsl-uat",         // öneksiz
		"bsa-deposit-commondeposithesapsl",             // ek soyulmuş (name etiketi biçimi)
	} {
		if !re.MatchString(v) {
			t.Errorf("%q eşleşmeliydi", v)
		}
	}

	// (2) Servis adı ekli, etiket eksiz — ekran görüntüsündeki durum.
	re2 := regexp.MustCompile(JobServiceRegex("bsa-chatbot-ai-integration-uat"))
	if !re2.MatchString("bsa-chatbot-ai-integration") {
		t.Error("eksiz `name` değeri eşleşmeliydi — v0.9.672'ye kadar kaçıyordu")
	}
}

// Gevşemedik: ek alternatifi eklemek KOMŞU servisleri içeri almamalı.
func TestJobServiceRegexStillRejectsNeighbours(t *testing.T) {
	re := regexp.MustCompile(JobServiceRegex("bsa-deposit-commondeposithesapsl-uat"))
	for _, v := range []string{
		"deposit/legacy-bsa-deposit-commondeposithesapsl-uat", // ad uzantısı
		"deposit/bsa-deposit-commondeposithesapsl-uat-v2",     // sonek
		"deposit/bsa-deposit-commondeposithesapsl-prod",       // BAŞKA ortam
		"deposit/bsa-deposit",                                 // kısaltma
	} {
		if re.MatchString(v) {
			t.Errorf("%q eşleşmemeliydi — yanlış servisin trafiği karışır", v)
		}
	}
}

func TestStripEnvSuffix(t *testing.T) {
	cases := map[string]string{
		"bsa-deposit-commondeposithesapsl-uat": "bsa-deposit-commondeposithesapsl",
		"svc-prod":                             "svc",
		"svc-int":                              "svc",
		"svc-prep":                             "svc",
		"checkout":                             "checkout", // ek yok
		"-uat":                                 "-uat",     // adın tamamı ek → dokunma
		"printer":                              "printer",  // alt dize, sonek değil
	}
	for in, want := range cases {
		if got := StripEnvSuffix(in); got != want {
			t.Errorf("StripEnvSuffix(%q) = %q, beklenen %q", in, got, want)
		}
	}
}

// DİLLER/PAKETLER ARASI AYNA. chstore, logstore'u import edemiyor; liste
// ayrışırsa aynı servis bir yüzeyde bulunup diğerinde bulunamaz.
func TestEnvSuffixesMirrorLogstore(t *testing.T) {
	b, err := os.ReadFile("../logstore/env_suffix.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoLineComments(string(b))
	i := strings.Index(src, "logEnvSuffixes = []string{")
	if i < 0 {
		t.Fatal("logEnvSuffixes bulunamadı — ayna kaynağı taşınmış olabilir")
	}
	block := src[i : strings.Index(src[i:], "}")+i]
	for _, suf := range EnvSuffixes {
		if !strings.Contains(block, `"`+suf+`"`) {
			t.Errorf("%q logstore listesinde YOK — ayna ayrışmış", suf)
		}
	}
	// Ters yön: logstore'da olup burada olmayan.
	for _, m := range regexp.MustCompile(`"(-[a-z]+)"`).FindAllStringSubmatch(block, -1) {
		var found bool
		for _, suf := range EnvSuffixes {
			if suf == m[1] {
				found = true
			}
		}
		if !found {
			t.Errorf("%q logstore'da var, chstore'da YOK — ayna ayrışmış", m[1])
		}
	}
}

// v0.9.676 — birim çevirisi. Span türevli panel ms, metrik saniye.
// Çevirmezsek iki panel 1000× farklı görünür.
func TestLatencyScaleToMs(t *testing.T) {
	cases := []struct {
		unit  string
		scale float64
		ok    bool
	}{
		{"s", 1000, true}, {"S", 1000, true}, {"seconds", 1000, true},
		{" s ", 1000, true}, // boşluk kırpılır
		{"ms", 1, true}, {"milliseconds", 1, true},
		{"us", 0.001, true}, {"µs", 0.001, true},
		{"ns", 0.000001, true},
		// BİLİNMEYEN ÇEVRİLMEZ: tahmin yazı-tura, yanlış ölçekli grafik
		// ölçeksizden kötü.
		{"", 1, false}, {"By", 1, false}, {"requests", 1, false},
	}
	for _, c := range cases {
		got, ok := LatencyScaleToMs(c.unit)
		if got != c.scale || ok != c.ok {
			t.Errorf("LatencyScaleToMs(%q) = (%v, %v), beklenen (%v, %v)", c.unit, got, ok, c.scale, c.ok)
		}
	}
}
