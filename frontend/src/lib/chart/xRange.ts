// xRangePinned — x-scale aralık kararı.
//
// v0.9.93 GERİ ALMA (operatör prod raporu): v0.9.83'te x-ekseni SORGU
// penceresine ([from, now]) sabitleniyordu. Prod'da veri ingest gecikmesi
// + seyrek scrape yüzünden son bucket `now`'un birkaç dakika gerisinde
// kalıyor; eksen yine de `now`'a uzayınca veri "belirli bir alana"
// sıkışıyor, sağ tarafta boş şerit + o boşlukta hover'da nokta yok gibi
// görünüyordu ("son 1 saat dediğimde metrikler arası boşluklar,
// sadece belirli bir alanı gösteriyor"). Operatör eski VERİYE-FİT
// davranışını istiyor.
//
// Artık her zaman veriye fit (uPlot'un mn/mx'i = veri uçları). Pin
// parametresi + plumbing şimdilik inert bırakıldı (temiz kaldırma ayrı
// bir sadeleştirme); imza korunuyor ki dört bileşenin range fn'i ve
// tüketici prop'ları geçerli kalsın. Stopped-service görünürlüğü
// gerekirse ileride ÖLÇÜLEREN, eşikli bir yaklaşımla geri gelebilir.
export interface XPin {
  from: number; // unix saniye
  to: number;
}

export function xRangePinned(
  _times: ReadonlyArray<number>,
  _pin: XPin | null | undefined,
  reqMin: number,
  reqMax: number,
): [number, number] {
  return [reqMin, reqMax];
}
