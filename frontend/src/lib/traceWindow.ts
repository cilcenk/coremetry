// v0.9.636 — /traces'te grafik ile tablo AYNI pencereyi göstermiyordu.
//
// Backend, ham trace listesinin alt sınırını 5 dakikaya AŞAĞI yuvarlıyor
// (internal/chstore/repo.go buildGetTracesWhere, v0.5.356). Bu KASITLI
// ve DOĞRU: operatör "Service detail'de bir operasyona tıklayınca
// /traces 0 sonuç dönüyor" demişti, sebep operation_summary_5m'in
// `time_bucket >= winStart.Truncate(5min)` okuması ve ham yolun katı
// `time >= winStart` kullanmasıydı — kova-örtüşme bölgesindeki tek
// eşleşen span'e sahip trace gizleniyordu.
//
// Hacim şeridi (/api/spans/metric-batch) ise bu genişletmeyi YAPMIYOR.
// Sonuç: tabloda, grafiğin sol kenarının SOLUNDA kalan satırlar
// görünüyor. Operatör grafikte bir sıçrama görüp tabloya bakıyor ve
// sayılar tutmuyor.
//
// ÇÖZÜM ŞEKLİ: listeyi DARALTMAK yanlış olurdu — v0.5.356'yı geri
// getirirdi. Doğrusu pencereyi TEK yerde hizalayıp aynı sayıyı üç
// tüketiciye birden vermek: liste, hacim şeridi ve x ekseni
// (Traces.tsx'te üçü de listRangeNs'ten besleniyor).
//
// Truncate IDEMPOTENT olduğu için hizalanmış bir `from` göndermek
// backend'deki yuvarlamayı no-op'a çeviriyor; yani backend'e
// dokunmadan üç yüzey de aynı pencereyi görüyor.
//
// BEDELİ, açıkça: seçili aralık 5 dakikaya kadar SOLA kayar. "Son 15
// dk" fiilen "son 15-20 dk" olur ve x ekseninin sol kenarı seçilen
// andan önce başlar. Bu, bugün ZATEN tabloda olan davranış — değişen
// tek şey grafiğin de artık aynı şeyi göstermesi, yani tutarsızlık
// yerine dürüstlük.

/** 5 dakikalık kova, nanosaniye. */
const BUCKET_NS = 5 * 60 * 1e9;

/**
 * alignTraceWindow — pencere alt sınırını backend'in yuvarlamasıyla
 * BİREBİR aynı şekilde hizalar.
 *
 * Go tarafı (buildGetTracesWhere):
 *
 *     fromAligned := f.From
 *     if !f.To.IsZero() && f.To.Sub(f.From) > 5*time.Minute {
 *         fromAligned = f.From.Truncate(5 * time.Minute)
 *     }
 *
 * >5dk KAPISI korunuyor: dar pencerelerde 5dk'lık genişletme operatörün
 * algıladığı aralığa hükmederdi (15 dakikada %33). İki taraf ayrışırsa
 * hizalama işe yaramaz — bu yüzden kapı da birebir kopyalandı.
 *
 * Kayan nokta notu: unix-ns (~1.75e18) float64'te ~256ns çözünürlükle
 * temsil ediliyor. 5 dakikalık hizalama için bu tamamen önemsiz;
 * Math.floor kova sınırını doğru buluyor.
 */
export function alignTraceWindow(fromNs: number, toNs: number): { from: number; to: number } {
  if (!Number.isFinite(fromNs) || !Number.isFinite(toNs)) return { from: fromNs, to: toNs };
  if (toNs - fromNs <= BUCKET_NS) return { from: fromNs, to: toNs };
  return { from: Math.floor(fromNs / BUCKET_NS) * BUCKET_NS, to: toNs };
}
