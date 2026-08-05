package chstore

import (
	"regexp"
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
		// Operatörün ekranındaki ad: birim/toplama ekleri ve http/server
		// atılıyor, geriye ayırt edici olanlar kalıyor.
		"http_server_request_duration_seconds_count": {"request", "duration"},
		"http.server.request.duration":               {"request", "duration"},
		// Tamamen jenerik ad → önerecek bir şey yok. Boş dönmek, katalogun
		// tamamını döndürmekten iyidir.
		"http_server_seconds_total": nil,
		"":                          nil,
		// Kısa parçalar (<3) atlanıyor: "db" gibi tokenlar katalogda
		// binlerce satır eşler.
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

// En çok İKİ parça: her parça ayrı bir katalog sorgusu.
func TestMetricNameProbeTokensCapped(t *testing.T) {
	got := MetricNameProbeTokens("alpha_beta_gamma_delta_epsilon")
	if len(got) > 2 {
		t.Errorf("en çok 2 parça olmalı, alınan %v", got)
	}
}
