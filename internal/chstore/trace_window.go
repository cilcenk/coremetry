package chstore

// trace_window.go — /traces okumalarında SEÇİM penceresi ile TOPLAMA
// penceresinin ayrımı (v0.9.1185).
//
// v0.9.823→1175 kova-sınırı taraması, `time_bucket <= to`nun pencereden
// SIFIR veri taşıyan bir kovayı içeri aldığını 44 okumada düzeltti ama
// traces ailesinin 10 sitesini GEREKÇELİ olarak dışarıda bıraktı. Gerekçe
// şuydu: bir trace kovaları AŞAR (trace_slice.go başlığı — bir trace her
// (5dk kova × shard × part) için bir satır tutar) ve stage-2 dur_ms /
// span_count'u yalnız pencere İÇİ kovaların merge'inden hesaplar. Üst
// sınırı `<`e çekmek, sınırı aşan trace'in süresini ve span sayısını DAHA
// çok budardı; `<=` o budamayı kazara bir kova kadar telafi ediyordu.
//
// Kazara telafi bir tasarım değildir. Doğrusu iki pencereyi AYIRMAK:
//
//	SEÇİM   (hangi trace'ler bu pencerede?) → `< to`
//	TOPLAMA (bu trace'in süresi/span sayısı) → `< to + slack`
//
// Neden güvenli: toplama sorguları her zaman `trace_id IN (…)` ile
// kısıtlı (stage-1'in verdiği ≤6000 id, traceStage2MaxIDs). Slack YENİ
// trace getirmez — yalnız SEÇİLMİŞ trace'lerin kaçan kuyruk kovalarını
// getirir. Tek istisna filtresiz aggregate yolu; onun için
// aggWindowEnd'in kelepçesi var.

import "time"

// traceAggSlack — toplama penceresinin seçim penceresinden ne kadar
// SONRAYA uzandığı.
//
// 1 saat: sınırı aşan bir trace'in kuyruğunu yakalamaya fazlasıyla yeter
// (bir HTTP isteği saatlerce sürmez; sürüyorsa süresi zaten ayrı bir
// problem), ve `trace_id IN (…)` kısıtı yüzünden bedeli yalnız birkaç
// fazladan kova taraması.
//
// Neden AYAR DEĞİL: bu sayı trace'lerin fiziksel uzunluğu, operatör
// tercihi değil. Ayar yapmak, cevabı olmayan bir soruyu operatöre sormak
// olurdu ("sistemimdeki en uzun trace kaç dakika?") ve yanlış cevabın
// belirtisi sessizce budanmış süreler olurdu.
const traceAggSlack = time.Hour

// aggWindowEnd — toplama sorgusunun üst sınırı.
//
// `bounded` false ise (sorgu `trace_id IN (…)` ile kısıtlı DEĞİL) slack
// pencere genişliğiyle kelepçelenir. Sebep ölçülebilir: filtresiz
// aggregate yolunda inner sorgu tüm pencereyi GROUP BY yapar, yani slack
// doğrudan taranan kova sayısına biner. 15 dakikalık bir pencerede 1
// saatlik slack, 3 kova yerine 15 kova demek (5×); kelepçeyle 6 kova.
// Kısıtlı yollarda böyle bir maliyet yok, orada tam slack verilir.
//
// SAF, tablo-testli.
func aggWindowEnd(from, to time.Time, bounded bool) time.Time {
	slack := traceAggSlack
	if !bounded {
		if w := to.Sub(from); w > 0 && w < slack {
			slack = w
		}
	}
	return to.Add(slack)
}
