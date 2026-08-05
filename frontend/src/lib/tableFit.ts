// Tablo sığma bütçesi (v0.9.662).
//
// `.table-wrap.is-fit` iç kaydırma konteynerini KALDIRIYOR — yapışkan tablo
// başlığı ancak öyle mümkün (globals.css'teki uzun açıklama). Bedeli şu:
// sığmayan bir tablo artık kendi kutusunda kaydırılmıyor, TAŞMASINI SAYFAYA
// SIZDIRIYOR. Operatör bunu "tablolar kaymış" diye bildirdi (v0.9.660).
//
// Yani `is-fit` bir stil tercihi değil, bir İDDİA: "bu tablo sığıyor".
// İddianın ölçülebilir olması gerekiyor, yoksa kolonlar zamanla eklenir ve
// bir gün sessizce ekranı aşar — Users tablosunda tam olarak bu oldu
// (v0.8.403 seen, v0.8.450 lastLogin, custom role… her biri masum).
//
// FIT_CONTENT_BUDGET — hedef içerik genişliği. 1920 ekran @125% Windows
// ölçeklemesi (≈1536 CSS px) eksi kenar çubuğu ve sayfa dolgusu; operatörün
// v0.9.660 ekran görüntüsünden ölçüldü. Daha dar bir pencere yine yatay
// kaydırır — bütçe bir GARANTİ değil, tasarım hedefi. Ama ölçülmemiş bir
// hedeften sonsuz kez iyi.
export const FIT_CONTENT_BUDGET = 1240;

// fixedBudget — bir kolon kümesinin SABİT genişlik toplamı.
//
// `flex` kolonlar sayılmıyor: onlar artanı emiyor. Ama tam da bu yüzden
// sabit toplam bütçeyi doldurursa flex kolon 0'a çöker ve kolon ekrandan
// TAMAMEN kaybolur (v0.9.660'ta Team kolonu). Yani sabit toplamı ölçmek
// hem taşmayı hem çökmeyi aynı anda kapıyor.
export function fixedBudget(
  cols: readonly { width?: number; flex?: boolean }[],
): number {
  return cols.filter(c => !c.flex).reduce((n, c) => n + (c.width ?? 0), 0);
}
