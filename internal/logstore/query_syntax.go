package logstore

import "regexp"

// query_syntax.go — hangi arama yazımını hangi arka ucun ANLADIĞI
// (v0.9.1384).
//
// İki arka uç arama metnini farklı ele alıyor ve fark SESSİZ:
//
//   • Elasticsearch: metin `query_string`e giriyor, yani `level:error`
//     ya da `service.name:"checkout"` GERÇEK bir alan sorgusu.
//   • ClickHouse (VARSAYILAN arka uç): metin gövdede aranıyor — liste
//     yolunda `body LIKE '%<metin>%'`, histogram yolunda
//     `multiSearchAnyCaseInsensitive(body, [<metin>])`. Yani
//     `service.name:"checkout"` dizgesi log GÖVDESİNDE birebir aranıyor.
//
// Canlı ölçüm (lokal CH 24.8, 24s pencere, 41.315 satır, tek servis):
//     service_name = 'x'                 →  858 satır
//     body LIKE '%x%'                    →  366 satır
//     body LIKE '%service.name:"x"%'     →    0 satır
// ve tüm tabloda `countIf(body LIKE '%service.name%') = 0`. Alan yazımı
// CH'de yalnız "sonuç yok" değil, YAPISAL OLARAK eşleşemez.
//
// Bu, bir arama kutusunda can sıkıcı; bir ALARMDA tehlikeli. Kayıtlı
// arama alarmı (`evaluateLogQuery`) tam bu alandan geçiyor: alan yazımlı
// bir kural CH'de daima 0 sayar, eşiği asla aşmaz, hata da vermez —
// operatör kapsandığını sanır. Sessizce ateşlenmeyen bir alarm, hiç
// kurulmamış bir alarmdan kötüdür.

// fieldQueryRe — `alan:değer` yazımı.
//
// Kısıtlar hep bir yanlış-pozitifi kesiyor:
//   • `[a-zA-Z]` başlangıç → `12:30` (saat) alan sorgusu sayılmaz.
//   • `(?:[\w.-]*)` → `service.name`, `http_status`, `k8s-pod` yakalanır.
//   • `:` ardından `[^\s/]` ŞART → `ERROR: boom` (iki nokta + boşluk,
//     düz metin) ve `http://host` (şema) dışarıda kalır.
var fieldQueryRe = regexp.MustCompile(`\b[a-zA-Z][\w.-]*:[^\s/]`)

// quotedRe — tırnak içi parçalar. Bunlar LİTERAL ifadedir; içindeki iki
// nokta bir alan sorgusu değil, aranan metnin kendisidir
// (`"connection refused: timeout"` gibi).
var quotedRe = regexp.MustCompile(`"[^"]*"`)

// LooksLikeFieldQuery — metin `alan:değer` yazımı içeriyor mu?
//
// Amaç bir KQL AYRIŞTIRICISI değil; yalnız "bu metin ClickHouse'da
// sessizce sıfır dönecek bir şey içeriyor mu" sorusuna cevap vermek.
// O yüzden kasten TUTUCU: emin olamadığında false döner, çünkü yanlış
// pozitif operatörün geçerli bir aramasını reddetmek demek.
func LooksLikeFieldQuery(s string) bool {
	// Tırnaklı parçalar BOŞLUKLA DEĞİL, boşluk olmayan bir yer tutucuyla
	// değiştiriliyor. Fark taşıyıcı: `service.name:"checkout"` içinde
	// DEĞER tırnaklı, yani tırnağı boşluğa çevirmek `service.name:` +
	// boşluk bırakır ve regex'in "iki noktadan sonra boşluk olamaz"
	// kuralı onu düz metin sanar. İlk yazımım tam bu yüzden en önemli
	// vakayı — kodun kendi şerhinin ÖNERDİĞİ yazımı — kaçırdı.
	// `_` hem boşluk hem `/` değil, dolayısıyla değer yerine geçiyor;
	// tırnağın İÇİNDEKİ iki nokta ise onunla birlikte yok oluyor.
	return fieldQueryRe.MatchString(quotedRe.ReplaceAllString(s, "_"))
}

// BackendUnderstandsFieldQuery — bu arka uç alan yazımını ayrıştırır mı?
//
// `Backend()` dizgesi üzerinden, çünkü çağıranların (API katmanı,
// evaluator) elinde olan tek kimlik bu ve arka uç ÇALIŞMA ZAMANINDA
// değişebiliyor (Settings toggle + boot degrade). Bir sabite bakmak
// "bugün doğru" bir cevap verirdi.
//
// v0.10.279 — ClickHouse da anlıyor: arama metni logql AST'sine ayrışıp
// gerçek kolon/res-array yüklemine derleniyor (chstore/log_query_compile.go).
// Yukarıdaki ölçüm o günün gerçeğiydi; tarihçe olarak duruyor. Bilinmeyen
// arka uç için varsayım yine "anlamıyor" (yanlış yön burada ucuz).
func BackendUnderstandsFieldQuery(backend string) bool {
	return backend == "elasticsearch" || backend == "clickhouse"
}
