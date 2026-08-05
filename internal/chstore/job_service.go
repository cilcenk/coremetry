// v0.9.665 — Prometheus `job` etiketinden servis eşleştirme.
//
// Operatör: "service overview throughput için ekrandaki metrikten
// job=abc/cm-put-service şeklinde Service ismi / sonrası olacak şekilde
// ayarlayabilir misin, metricten okusun."
//
// Ortamdaki metrik Prometheus biçiminde geliyor ve servis kimliği
// `job` etiketinde, `<namespace>/<servis>` şeklinde duruyor:
//
//	http_server_request_duration_seconds_count{job="content-manager-prod/cm-put-service"}
//
// Coremetry'nin kendi servis adı ise `cm-put-service`. Yani eşleştirme
// `job` değerinin SON BÖLÜMÜ üzerinden yapılmalı.
//
// NEDEN SAF FONKSİYON: eşleştirme deseni kullanıcı girdisinden (servis
// adı) üretiliyor ve doğrudan bir CH `match()` ifadesine giriyor. Kaçış
// hatası ya sessizce yanlış servisi eşler ya da sorguyu patlatır — ikisi
// de tablo testiyle kapatılacak sınıf.
package chstore

import (
	"regexp"
	"strings"
)

// JobLabelDefault — Prometheus'un servis kimliğini taşıdığı etiket.
const JobLabelDefault = "job"

// ServiceIdentityLabels — servis kimliğini taşıyabilecek etiketler,
// DENEME SIRASIYLA.
//
// v0.9.671 (operatör-bildirimi, ekran görüntüsüyle): "Coremetry acaba
// name bakmıyor mu? Metric explorer'da aynı metriği name'e göre
// filtreleyince görüyorum."
//
// Doğru: o kurulumda kimlik `name` etiketinde
// (name=bsa-chatbot-ai-integration). v0.9.665-670 yalnız `job`
// deniyordu — Prometheus kanonik adı, ama tek doğru değil. Aynı ölçüm
// kuruluma göre farklı etikette taşınıyor.
//
// ÇOK ETİKET DENEMEK GÜVENLİ, çünkü eşleşme TAM DEĞER üzerinden:
// etiketin değeri servis adına (ya da ortam-eki soyulmuş hâline)
// birebir eşit olmalı. Yanlış bir etiket rastgele eşleşme üretemez.
// Gevşek/alt-dize eşleşme olsaydı bu liste tehlikeli olurdu.
var ServiceIdentityLabels = []string{
	"job",     // Prometheus kanonik
	"service", // açık adlandırma
	"name",    // operatörün kurulumu (v0.9.671)
}

// ThroughputMetricDefault — operatörün ekranındaki metrik. Ayarla
// değiştirilebilir; kuruluma göre adı farklı olabilir.
const ThroughputMetricDefault = "http_server_request_duration_seconds_count"

// JobServiceRegex — bir servis adını `job` etiketi desenine çevirir.
//
// `^(.*/)?<servis>$` — hem "ns/servis" hem eksiz "servis" eşleşir.
// İkinci hâli bilinçli: her kurulum job'a namespace önekini koymuyor ve
// önek zorunlu tutulursa o kurulumlarda sessizce boş sonuç dönerdi.
//
// SON BÖLÜM TAM eşleşmeli. `.*servis$` deseydik "cm-put-service" ararken
// "legacy-cm-put-service" de eşleşirdi — yanlış servisin trafiği doğru
// servisin grafiğine karışır ve bunu kimse fark etmez.
//
// QuoteMeta ŞART: servis adları "." ve "-" taşıyor. Kaçışsız "." herhangi
// bir karaktere uyar, yani "cm-put-service" deseni "cmXput-service"i de
// eşlerdi.
func JobServiceRegex(service string) string {
	svc := strings.TrimSpace(service)
	alts := []string{regexp.QuoteMeta(svc)}
	// v0.9.673 (operatör + meslektaşının Prometheus çıktısı) — ORTAM EKİ
	// İKİ YÖNE DE GİDİYOR. Aynı kurulumda her iki biçim birden var:
	//
	//   job  = "deposit/bsa-deposit-commondeposithesapsl-uat"  ← ek VAR
	//   name = "bsa-chatbot-ai-integration"                    ← ek YOK
	//                (servis adı ise bsa-chatbot-ai-integration-uat)
	//
	// Yalnız tam adı aramak ikincisini kaçırıyordu; yalnız eksiz adı
	// aramak birincisini kaçırırdı. İkisi de aday.
	if st := StripEnvSuffix(svc); st != svc && st != "" {
		alts = append(alts, regexp.QuoteMeta(st))
	}
	return "^(.*/)?(" + strings.Join(alts, "|") + ")$"
}

// EnvSuffixes — servis adlarındaki ortam ekleri.
//
// AYNA: internal/logstore/env_suffix.go `logEnvSuffixes` ve frontend
// podWorkload.ts / logFieldAliases.ts ile AYNI küme olmak zorunda.
// Ayrışırlarsa aynı servis bir yüzeyde bulunup diğerinde bulunamaz —
// sessiz ve teşhisi zor. job_service_test.go Go kaynağını okuyup
// eşleşmeyi doğruluyor (chstore logstore'u import edemiyor).
var EnvSuffixes = []string{"-prod", "-int", "-uat", "-prep"}

// StripEnvSuffix — servis adından ortam ekini atar. Ek yoksa ad aynen
// döner. Adın TAMAMI ekse soymaz (boş ad üretmez).
func StripEnvSuffix(service string) string {
	for _, suf := range EnvSuffixes {
		if len(service) > len(suf) && strings.HasSuffix(service, suf) {
			return service[:len(service)-len(suf)]
		}
	}
	return service
}

// ServiceFromJobLabel — `job` değerinden servis adı: son "/" sonrası.
//
// "/" yoksa değerin kendisi servis adıdır (öneksiz kurulum).
// Sondaki "/" bir servis adı üretmez — boş dönerse çağıran atlamalı.
func ServiceFromJobLabel(job string) string {
	job = strings.TrimSpace(job)
	if i := strings.LastIndex(job, "/"); i >= 0 {
		return job[i+1:]
	}
	return job
}

// metricNameGenericTokens — metrik adlarında AYIRT ETMEYEN parçalar.
//
// Prometheus çevirisi ad sonuna birim ve toplama eki koyuyor
// (`..._seconds_count`, `..._total`, `..._bucket`); "http"/"server" ise
// yüzlerce metrikte geçiyor. Bunlarla arama yapmak katalogdaki her şeyi
// döndürür ve öneri işe yaramaz.
// v0.9.668 — "server"/"client" LİSTEDEN ÇIKTI. İlk hâlinde jenerik
// sayılmışlardı; operatörün prod ekranında sonuç şu oldu: önerilerin
// TAMAMI http.client.* geldi ve aradığı http.server.* hiç görünmedi.
// Oysa server/client, bir HTTP metriğini ayırt eden EN belirleyici
// parça — atılacak değil, öncelenecek şey.
var metricNameGenericTokens = map[string]bool{
	"http":    true,
	"seconds": true, "count": true, "total": true, "bucket": true,
	"sum": true, "ms": true, "milliseconds": true, "bytes": true,
}

// MetricNameProbeTokens — bir metrik adından arama için AYIRT EDİCİ
// parçalar çıkarır.
//
// NEDEN GEREKLİ: operatörün Grafana'sı PROMETHEUS'tan okuyor, Coremetry
// ise OTLP alıyor. Aynı ölçüm iki tarafta farklı adlanıyor olabilir —
// `http_server_request_duration_seconds_count` (Prometheus) ile
// `http.server.request.duration` (OTel semconv) aynı şeyin iki yazımı.
// Ad bulunamadığında "bunu mu demek istediniz" diyebilmek için adı
// parçalara ayırıp katalogda aramak gerekiyor.
//
// SAF (tablo testli). En çok iki parça döndürüyor: her parça ayrı bir
// katalog sorgusu demek ve ikiden fazlası öneriyi gürültüye çeviriyor.
func MetricNameProbeTokens(name string) []string {
	name = strings.ToLower(strings.TrimSpace(name))
	raw := strings.FieldsFunc(name, func(r rune) bool {
		return r == '_' || r == '.' || r == '-' || r == '/'
	})
	var out []string
	for _, t := range raw {
		if len(t) < 3 || metricNameGenericTokens[t] {
			continue
		}
		out = append(out, t)
		if len(out) == 2 {
			break
		}
	}
	return out
}

// ThroughputMetricCandidates — throughput için bilinen metrik adları,
// ÖNCELİK SIRASIYLA.
//
// v0.9.668 (operatör-bildirimi): tek bir varsayılan ad yetmiyor. Aynı
// ölçüm kuruluma göre beş farklı adla geliyor — OTel semconv sürüm
// değiştirdi (http.server.duration → http.server.request.duration),
// Micrometer kendi adını koyuyor, Prometheus çevirisi sonek ekliyor.
// Operatöre "doğru adı bul ve ayara yaz" demek yerine sunucu sırayla
// deniyor.
//
// Sıra ÖNEMLİ: en yeni/en yaygın semconv başta. İlk VAR OLAN kazanıyor.
var ThroughputMetricCandidates = []string{
	"http.server.request.duration",               // OTel semconv ≥1.23 (histogram)
	"http.server.duration",                       // OTel semconv <1.23 (histogram)
	"http.server.requests",                       // Micrometer/Spring (counter)
	"http_server_request_duration_seconds_count", // Prometheus çevirisi
	"http_server_requests_seconds_count",         // Micrometer → Prometheus
}

// LatencyScaleToMs — süre biriminden MİLİSANİYE çarpanı.
//
// v0.9.676 (operatör: "response time için de bir panel yapabilir
// misin") — span türevli Response time paneli `ms` çiziyor, metrik ise
// KENDİ biriminde geliyor: OTel semconv `http.server.request.duration`
// SANİYE. Çevirmezsek iki panel 1000× farklı görünür ve operatör bunu
// hata sanır — ya da daha kötüsü, sanmaz ve yanlış sayıya güvenir.
//
// BİLİNMEYEN BİRİM ÇEVRİLMİYOR (ok=false). Tahmin etmek yazı-tura:
// boş birimli bir süre metriği saniye de olabilir milisaniye de.
// Çağıran o durumda ölçeklemeden çizip birimi OLDUĞU GİBİ yazmalı —
// yanlış ölçekli bir grafik, ölçeksiz olandan kötüdür.
func LatencyScaleToMs(unit string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "s", "sec", "secs", "second", "seconds":
		return 1000, true
	case "ms", "millisecond", "milliseconds":
		return 1, true
	case "us", "µs", "microsecond", "microseconds":
		return 0.001, true
	case "ns", "nanosecond", "nanoseconds":
		return 0.000001, true
	}
	return 1, false
}
