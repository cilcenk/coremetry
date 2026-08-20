// v0.9.627 — patlama HIZI: exception önceliğinin eksik boyutu.
//
// Operator-reported: tek servisten 12 dakikada 11.260 olay üreten bir
// exception grubu P2 göründü.
//
//	com.ibm.msg.client.jakarta.jms.DetailedInvalidDestinationException
//	service   bsa-cashmanagement-cashflow-prod
//	first     04.08.2026 12:49:31
//	last      04.08.2026 13:01:36
//	toplam    11.260          → ~938 olay/dakika
//
// exceptionPriority'nin P1 kapısı TEK bir koşuldu: "son 5 dakika içinde
// görülmüş VE toplam ≥ 500". Patlama 13:01'de bitti, operatör 13:22'de
// baktı — 21 dakikalık yaş kapıyı kapattı ve satır P2'ye düştü.
//
// İki ayrı eksik vardı:
//
//  1. TAZELİK PENCERESİ ÇOK DAR. P1 "şimdi ilgilen" demek; yirmi dakika
//     önce biten 11 binlik bir patlama hâlâ şimdi ilgilenilmesi gereken
//     bir şey. Olayın bitmesi etkisinin bittiği anlamına gelmiyor.
//  2. HIZ HİÇ ÖLÇÜLMÜYORDU. 11.260 ile 110 arasındaki fark yalnız bir
//     eşik karşılaştırmasına giriyordu, birim zamana değil. Üç günde
//     birikmiş 11.260 ile on iki dakikada patlayan 11.260 aynı kovaya
//     düşüyordu — halbuki ilki kronik, ikincisi olay.
//
// Hız, elimizde ZATEN olan iki alandan türüyor: first_seen ve last_seen.
// v0.9.524'ün dersi burada da geçerli — sahip OLMADIĞIMIZ pencereli bir
// sayıyı uydurmuyoruz; "12 dakikada 11.260" birebir doğru bir cümle,
// "son 5 dakikada 11.260" ise yalan olurdu.
package api

import (
	"fmt"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// exceptionBurstMinRate — P1 için dakikadaki en az olay.
//
// 200 seçildi: gürültülü ama sağlıklı bir servisin ürettiği tekrarlayan
// uyarı tipik olarak bu mertebenin altında kalıyor; 200/dk'yı aşan bir
// exception grubu neredeyse her zaman kırılmış bir bağımlılık ya da bir
// yeniden-deneme fırtınası demek. Operatörün ölçtüğü olay ~938/dk.
const exceptionBurstMinRate = 200.0

// exceptionBurstMinTotal — hız ne olursa olsun gereken taban hacim.
//
// Hız tek başına yeterli değil: 5 saniyede 20 olay da 240/dk eder ama
// P1 değildir. Taban, kısa ömürlü küçük grupların hız kapısından
// sızmasını engelliyor.
const exceptionBurstMinTotal = 1000

// exceptionBurstFloor — ömür bundan kısaysa bu değer kullanılır.
//
// first_seen == last_seen olan bir grup (tek bir toplu yazımda gelen
// olaylar) sıfıra bölme ya da sonsuz hız üretirdi. Taban, "bir anda
// gelen N olay" ile "bir dakikada gelen N olay"ı aynı sayar — hız
// abartılmıyor, olduğundan BÜYÜK gösterilmiyor.
const exceptionBurstFloor = time.Minute

// exceptionBurstRate — grubun kendi ömrü üzerinden dakikadaki olay.
// SAF (tablo testli).
func exceptionBurstRate(occurrences uint64, firstSeen, lastSeen int64) float64 {
	span := time.Duration(lastSeen - firstSeen)
	if span < exceptionBurstFloor {
		span = exceptionBurstFloor
	}
	return float64(occurrences) / span.Minutes()
}

// exceptionIsBurst — bu grup hacim VE hız olarak P1 eşiğini aşıyor mu?
// SAF (tablo testli).
//
// Çağıran ayrıca tazelik kapısını (son 1 saat) uygular: bu fonksiyon
// "ne kadar şiddetli" sorusuna bakar, "hâlâ güncel mi" sorusuna değil.
// v0.9.1188 — eşikler artık AYARDAN (exception_triage). Gömülü sabitler
// aşağıda varsayılan olarak duruyor ama tek okuyucusu chstore'un
// DefaultExceptionTriage'ı; bu fonksiyon config'e bakar.
func exceptionIsBurst(occurrences uint64, firstSeen, lastSeen int64, cfg chstore.ExceptionTriageConfig) bool {
	cfg = chstore.NormalizeExceptionTriage(cfg)
	if occurrences < uint64(cfg.BurstMinTotal) {
		return false
	}
	return exceptionBurstRate(occurrences, firstSeen, lastSeen) >= cfg.BurstMinRate
}

// shortDur — insan okuyacak kısa süre ("12dk", "1sa 5dk", "45sn").
// Öncelik gerekçesinde geçiyor, yani operatörün gördüğü metin. SAF.
func shortDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.0fsn", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%.0fdk", d.Minutes())
	default:
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("%dsa", h)
		}
		return fmt.Sprintf("%dsa %ddk", h, m)
	}
}
