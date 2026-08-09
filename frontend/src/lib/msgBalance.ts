// msgBalance.ts — /messaging tablosunun SAF sıralama çekirdeği.
//
// v0.9.835 — msgBalance / MsgBalanceState / MSG_BALANCE_THRESHOLD
// KALDIRILDI. "Denge" kolonu (üretim−tüketim)/üretim oranını çipe
// çeviriyordu, ama bu bir CONSUMER LAG DEĞİLDİ: broker metriği OTel'e
// ingest edilmiyor, oran yalnız aynı penceredeki producer/consumer span
// SAYILARINDAN kuruluyordu. İki taraf ayrı SDK'lardan, ayrı örnekleme
// oranlarıyla ve ayrı kova sınırlarında sayılınca oran anlamsız
// değerlere gidiyor — canlıda "boşalıyor %148" görüldü, yani tüketim
// üretimin 2.5 katı gibi okunan bir sayı. Eşiği genişletmek yerine
// kolon kaldırıldı: doğrusu lag metriğinden kurulur, span oranından
// değil.
//
// Lag metriği (messaging.*consumer.lag) bir gün ingest edilirse kolon
// ONDAN yeniden kurulur; bu dosya o zamana dek yalnız gecikme
// sıralamasını taşıyor.

// msgP99Delta — "kötüleşenler önce" sıralamasının saf çekirdeği
// (v0.9.815; Endpoints v0.9.761'in sortEndpointsByP99Delta ikizi).
//
// prior yoksa null döner ve satır listenin ALTINA, kendi aralarında
// GELDİĞİ SIRAYLA (sunucudan spanCount DESC) düşer — brief'teki
// "prior yoksa spanCount DESC'e düş" davranışı, ayrı bir kod yolu
// olmadan, sortRows'un null-sonda + kararlı sözleşmesinden.
//
// ŞERH: burada ölçülen ŞEY messaging SPAN p99'unun kötüleşmesi, uçtan
// uca (produce→consume) gecikmenin değil. Genel bakış payload'ı E2E
// TAŞIMIYOR (E2E span_links korelasyonu gerektiriyor ve yalnız
// destination başına, drawer'da hesaplanıyor) — dolayısıyla "E2E p95 Δ"
// diye sıralamak, var olmayan bir veriye göre sıralamak olurdu.
export function msgP99Delta(p99Ms: number, priorP99Ms?: number): number | null {
  if (priorP99Ms === undefined || !(priorP99Ms > 0)) return null;
  return (p99Ms - priorP99Ms) / priorP99Ms;
}
