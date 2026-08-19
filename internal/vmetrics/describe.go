package vmetrics

// v0.9.1180 — VM katalog satırlarına BİRİM ve TİP.
//
// Neden: ListMetricNames şimdiye dek yalnız `Name` dolduruyordu, Unit ve Type
// boş gidiyordu. İki sonucu vardı ve ikincisi tehlikeliydi:
//
//  1. Birimsiz seri. VM'in canlı ailesi Prometheus-adlı ve birim ADIN İÇİNDE
//     (`http_server_request_duration_seconds`). Birim taşınmayınca panel
//     "0.25" yazıyor; operatör bunun saniye mi milisaniye mi olduğunu
//     bilemiyor. CH yolu aynı ölçümü ms olarak döndürdüğü için iki backend
//     yan yana iki farklı sayı gösteriyor ve hiçbiri hangisi olduğunu
//     söylemiyor.
//  2. FE şablon kaydı (metricTemplates.ts) "HTTP server latency" için
//     `unit: 'ms'` öneriyor. Şablonları Prometheus yazımlarını tanıyacak
//     hâle getirmek — ki gereklidir — birim taşınmadan yapılırsa SANİYELİK
//     veriye "ms" damgalar: 0.25 s, "0.25ms" diye okunur. Bin kat yanlış.
//     Bu yüzden iki düzeltme aynı sürümde gider.
//
// Dönüşüm YAPILMIYOR, birim TAŞINIYOR. VM Prometheus dünyasının deposudur ve
// oradaki sözleşme "birim adın son ekidir"; sessizce saniyeyi milisaniyeye
// çevirmek `_bytes`/`_ratio` ailelerini de bozacak bir kural gerektirirdi ve
// kaynağı hakkında yalan söylemeyen backend duruşuyla (sessiz CH fallback'i
// yok) çelişirdi. fmtSmart `s` birimini zaten biliyor ve 0.25 s'yi "250ms"
// diye yazar — yani operatör doğru sayıyı okunur biçimde görür.

import "strings"

// displayUnitBySuffix — ad son eki → fmtSmart'ın ANLADIĞI birim dizesi.
//
// names.go'daki `promUnitSuffixes` ile karıştırılmamalı: o, YAZIM üretir
// (bir OTel adına hangi ekin ekleneceği); bu, yazımı OKUR (bir addan hangi
// GÖSTERİM biriminin çıkacağı). Ters yönler, bu yüzden listeler de aynı
// değil.
//
// Kasten dar: yalnız hem OTel'in Prometheus köprüsünün ürettiği hem de
// frontend/src/lib/chartFmt.ts'in tanıdığı ekler var. Tanınmayan bir birim
// dizesi üretmek (`_ratio` → "1" gibi) fmtSmart'ta "0.25 1" gibi bir çıktı
// verirdi; boş bırakmak bugünkü davranıştır ve daha dürüsttür.
//
// `_ratio` ve `_percent` BİLEREK YOK: ratio 0-1, percent 0-100 aralığında ve
// ikisini de "%" ye eşlemek yüz kat hataya davetiye. Ratio'yu birimsiz
// bırakmak yanlış bir ölçek göstermekten iyidir.
var displayUnitBySuffix = []struct{ suffix, unit string }{
	{"_seconds", "s"},
	{"_milliseconds", "ms"},
	{"_bytes", "B"},
}

// describeMetricName — bir Prometheus/VM seri adından (birim, tip) çıkarır.
// SAF, tablo-testli. Bilinmeyen her şey için boş dize döner; boş dize
// "bilmiyorum" demektir ve bugünkü davranışın aynısıdır.
//
// Tip tahmini iki ekle SINIRLI:
//
//	_bucket → histogram (histogram parçası olduğu kesin)
//	_total  → sum       (Prometheus sayaç sözleşmesi)
//
// `_count` ve `_sum` BİLEREK tahmin edilmiyor. names.go'nun uzun uzun
// anlattığı belirsizlik burada da geçerli: `queue_message_count` gerçek bir
// sayaç olabilir, bir histogramın sayı serisi de. Yanlış tip, FE'de yanlış
// varsayılan agregasyon demek (histogram → p99, sayaç → sum) — tahminsiz
// bırakmak, yanlış tahmin etmekten ucuz.
func describeMetricName(name string) (unit, typ string) {
	n := strings.TrimSpace(name)
	if n == "" {
		return "", ""
	}

	// Tip: parça eki adın EN SONUNDA olur.
	switch {
	case strings.HasSuffix(n, bucketSuffix):
		typ = "histogram"
	case strings.HasSuffix(n, counterSuffix):
		typ = "sum"
	}

	// Birim: parça ekini (varsa) soyduktan SONRA bakılır — gerçek yazım
	// `…_seconds_bucket`, `…_bucket_seconds` diye bir şey yok (names.go'nun
	// kompozisyon sırası kuralı).
	base := n
	for _, suf := range []string{bucketSuffix, histogramSumSuffix, histogramCountSuffix, counterSuffix} {
		if strings.HasSuffix(base, suf) {
			base = strings.TrimSuffix(base, suf)
			break
		}
	}
	for _, u := range displayUnitBySuffix {
		if strings.HasSuffix(base, u.suffix) {
			return u.unit, typ
		}
	}
	return "", typ
}
