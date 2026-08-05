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
	return "^(.*/)?" + regexp.QuoteMeta(strings.TrimSpace(service)) + "$"
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
var metricNameGenericTokens = map[string]bool{
	"http": true, "server": true, "client": true,
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
