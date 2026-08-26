package chstore

// engine_rate_window.go — kümülatif sayaç oranlarının PAYDASI
// (v0.10.14).
//
// Dört motor okuyucusu da kümülatif sayaçları şöyle orana çeviriyordu:
//
//     (max(value) - min(value)) / windowSec
//
// PAY, verinin GERÇEKTEN kapsadığı aralığı ölçüyor: `max`/`min` yalnız
// pencerede BULUNAN noktalar üzerinden hesaplanıyor. PAYDA ise operatörün
// İSTEDİĞİ pencerenin tamamı. İkisi aynı şey değil ve veri pencereden
// kısa olduğu her durumda oran SESSİZCE DÜŞÜK çıkıyor.
//
// Ne zaman kısa olur: metrik saklama süresi pencereden dar olduğunda,
// receiver yeni açıldığında, ya da operatör 30 günlük bir pencere seçip
// elde yalnız birkaç günlük veri olduğunda. Sonuncusu en sık: kaydırmalı
// uzun pencerelerde her `*PerSec` alanı gerçeğin bir kesri.
//
// Kusur SESSİZ, çünkü sayı makul görünüyor — yalnız yanlış. Bir SRE
// "saniyede 40 mantıksal okuma" görüp normal sayar, gerçek 170 iken.
//
// DÜZELTME: payda da GÖZLENEN aralık olsun — `min(time)`/`max(time)`.
// Böylece pay ve payda aynı veri kümesini tarif eder.
//
// ── BÜYÜKLÜK ÖLÇÜLDÜ (v0.10.49) ─────────────────────────────────────────
//
// Bu dosyanın testleri SQL'in ŞEKLİNİ pinliyor, aritmetiğini değil — ve
// lokal veri düzeltmenin ısırdığı durumu HİÇ içermiyor: demo üreteci
// kesintisiz yayıyor, 114 metriğin 114'ü de pencereyi %99,9 kapsıyor
// ([[feedback-local-data-is-a-fixture]]). Yani "lokalde doğru görünüyor"
// bu düzeltme hakkında hiçbir şey söylemiyor.
//
// Canlı CH'de sentetik seyrek seriyle ölçüldü (3600 sn'lik pencerede
// yalnız 300 sn gözlenen, gerçek hızı 2,0/sn olan kümülatif sayaç):
//
//	gözlenen payda (bu kod) → 2,000   ✓ gerçeğe eşit
//	istenen payda  (eskisi) → 0,167   ✗ 12× DÜŞÜK
//
// Yani kusurun bedeli pencere/gözlem oranı kadar: 30 günlük pencerede 2
// günlük veri = 15× düşük. "Makul görünen yanlış sayı" tam bu.
//
// ⚠ TEK NOKTA TUZAĞI: pencerede tek örnek varsa `min(time) == max(time)`
// ve aralık 0'dır. `greatest(..., 1)` bunu 1 saniyeye sabitliyor; o
// durumda pay da 0 olduğu için oran 0 çıkar — NULL döndürüp her çağrı
// yerinde NULL-işleme eklemektense doğru cevabı doğrudan vermek.
//
// ⚠ GERİ ÇARPIM: bazı alanlar oranı tekrar toplama çeviriyor
// (`CPUTimeSec = rate * pencere`). O çarpım AYNI aralığı kullanmak
// zorunda, yoksa payda düzeltilirken toplam sessizce yanlışlanır —
// bu yüzden sorgular gözlenen aralığı ÇAĞIRANA DA döndürüyor.

// observedSpanSQL — oranın paydası: verinin gerçekten kapsadığı saniye.
//
// Tek yerde tanımlı, çünkü dört motorun bu ifadeyi kopyalaması ıraksama
// davetiyesiydi — bu oturumun tekrar eden dersi.
const observedSpanSQL = `greatest(dateDiff('second', min(time), max(time)), 1)`

// rateSelectSQL — `(max-min) / gözlenen_aralık` seçimi + aralığın
// kendisi. Çağıran ikinci kolonu geri çarpım için kullanır.
// ⚠ observed_sec Float64'e ZORLANIYOR (v0.10.51). `dateDiff` Int64 döner ve
// `greatest(Int64, UInt8)` de Int64'tür; çağıran onu `float64`'e tararsa
// clickhouse-go tip hatası verir. Kolonu eklediğim ilk hâlde (v0.10.15) tam
// bu oldu ve hata `if err := rows.Scan(...); err == nil` dalında SESSİZCE
// yutulduğu için BÜTÜN Oracle oranları 0 döndü — veri yerindeyken.
//
// Cast SQL tarafında, çağıran tarafta değil: kolon tek yerde tanımlanıyor,
// tipi de orada sabitlenmeli. Her tüketicinin doğru Go tipini hatırlaması
// beklenirse biri unutur ve aynı SESSİZ sıfır geri gelir.
const rateSelectSQL = `(max(value) - min(value)) / ` + observedSpanSQL +
	` AS rate, toFloat64(` + observedSpanSQL + `) AS observed_sec`

// ── Kapı: payda TEK yerden gelmeli ──────────────────────────────────────
//
// Bu düzeltmeyi yaparken payda ile bind argümanı BİR SORGUDA ayrıştı:
// `?` yer tutucusunu kaldırdım ama `windowSec` bind'i yerinde kaldı,
// yani bütün bind sırası kaydı. `go build` bunu GÖREMEZ — SQL seviyesinde
// bir kusur, ancak çalışma zamanında patlar. İki sorguda tersi oldu:
// bind'i kaldırdım ama `?` yerinde kaldı.
//
// Kapı `engine_rate_window_test.go`'da: kümülatif oran hesaplayan hiçbir
// sorgu `/ ?` yazamaz.
