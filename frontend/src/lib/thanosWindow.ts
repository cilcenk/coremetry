// thanosWindow — /clusters trend uçlarının pencere TAVANI (istemci yarısı).
//
// v0.9.1370 (operatör-bildirimi: "Infrastructure'da hangi aralığı
// seçersem seçeyim hep aynı zamanı gösteriyor — 6 saate kadar takip
// ediyor, 6 saat ve üstünde son 6 saati gösteriyor").
//
// NEDEN İSTEMCİDE DE VAR: sunucu pencereyi sessizce kelepçeliyor
// (internal/api/thanos_handlers.go, clampThanosWindow). v0.9.21 aynı
// kelepçeyi istemciye taşıdı ki eksen ve panel BAŞLIĞI gerçeği söylesin
// — geniş sayfa range'inde "son 6h"lik veriyi 7 günlük eksene yaymak
// yanıltıcıydı. Yani bu kopya bir dürüstlük aracı, süs değil.
//
// AYNALI KURAL TEK GÖVDE İSTER: tavan üç sayfada (Infrastructure, Pods,
// Clusters Overview) satır satır kopyalanmıştı. Tek gövdeye indi ve
// sunucuyla AYRIŞMASI bir Go testiyle kapatıldı
// (internal/api/thanos_window_test.go — sayıyı bu dosyadan okur).
// Sabitin adı o kapının sözleşmesidir: yeniden adlandırılırsa test
// açıkça kırılır ve sessizce ayrışma olmaz.

/** Tavan, SAAT cinsinden. Go tarafı bu sayıyı kaynaktan okur. */
export const THANOS_MAX_WINDOW_HOURS = 24;

/** Tavan, nanosaniye — sayfa aralıkları ns taşıyor (timeRangeToNs). */
export const THANOS_MAX_WINDOW_NS = THANOS_MAX_WINDOW_HOURS * 3600 * 1e9;

/** Başlıklarda görünen etiket ("… (last 24h)"). Sabitten TÜRETİLİR:
 *  elle yazılsaydı tavan değişince başlık yalan söylerdi — bu kusur
 *  sınıfının ta kendisi. */
export const THANOS_MAX_WINDOW_LABEL = `${THANOS_MAX_WINDOW_HOURS}h`;

export interface ClampedWindow {
  /** Kelepçelenmiş başlangıç (ns). */
  cFrom: number;
  /** Bitiş — HER ZAMAN girdideki `to` (ns). */
  cTo: number;
  /** Kelepçe uygulandı mı? Panel başlığı bunu ifşa eder. */
  clamped: boolean;
}

/** clampThanosWindow — SPAN'i kelepçeler, ÇAPAYI değil.
 *
 *  Dönen aralık her zaman `to`da biter: operatör geçmişte bir pencere
 *  fırçaladıysa O pencerenin son 24 saatini görür, "şimdinin son 24
 *  saati"ne fırlatılmaz. Bu ayrım sessizce kaybolursa grafik dolu
 *  görünür ama yanlış zamanı gösterir — fark edilmesi en zor kusur.
 *
 *  SAF: `now()` okumaz, bu yüzden render sırasında çağrılması güvenli
 *  değildir demiyoruz — ama çağıranlar yine de useMemo içinden
 *  çağırıyor, çünkü GİRDİSİ timeRangeToNs'ten geliyor (v0.5.184). */
export function clampThanosWindow(from: number, to: number): ClampedWindow {
  if (to - from > THANOS_MAX_WINDOW_NS) {
    return { cFrom: to - THANOS_MAX_WINDOW_NS, cTo: to, clamped: true };
  }
  return { cFrom: from, cTo: to, clamped: false };
}

/** clampSuffix — panel başlığının dürüstlük eki. Kelepçe yoksa boş
 *  dize, yani başlık hiç uzamaz. */
export function clampSuffix(clamped: boolean): string {
  return clamped ? ` (last ${THANOS_MAX_WINDOW_LABEL})` : '';
}
