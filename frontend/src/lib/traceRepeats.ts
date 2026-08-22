// lib/traceRepeats.ts — v0.9.1277 (Dynatrace-parite #6, N+1 görünürlüğü).
//
// SAF: DOM yok, tarih yok, React yok. Trace sayfasının "bu trace'te aynı
// çağrı N kez tekrarlanmış" uyarı çipini besler.
//
// NEDEN AYRI BİR ÇEKİRDEK: N+1 tespiti üç yerde birden yaşıyordu ve üçü
// de birbirini görmüyordu —
//   • backend `/api/spans/repeats` (Explore "Repeats" modu): trace ÜSTÜ,
//     "hangi trace'lerde tekrar var" sorusunu cevaplar;
//   • TraceWaterfall `groupSimilar`: trace İÇİ, ama v0.9.226'dan beri
//     hiçbir çağıranı `true` geçmiyordu;
//   • Trace sayfası: ikisinden de habersizdi.
// Bu dosya üçüncüsünü kapatıyor ve ikinciyi uyandıran tetiği veriyor.
//
// KAPSAM KARARI — grup anahtarı `(serviceName, displaySpanName)` ve
// KARDEŞLİK ARANMAZ. TraceWaterfall'ın `groupSimilar`'ı yalnız aynı
// ebeveynin çocuklarını katlar; buradaki çip ise "trace'in TAMAMINDA bu
// çağrı kaç kez geçti" sorusunu sorar. İkisi bilerek farklı: bir ORM'in
// N+1'i çoğu zaman tek ebeveynin altında toplanır ama her zaman değil
// (araya per-item bir wrapper span giren kütüphanelerde her sorgu kendi
// ebeveynine düşer). Dar tanım seçilseydi çip tam da o vakalarda susardı.
import type { SpanRow } from '@/lib/types';
import { displaySpanName } from '@/lib/utils';

export interface TraceRepeatGroup {
  service: string;
  /** displaySpanName sonucu — şelaledeki satır etiketiyle AYNI dize. */
  name: string;
  count: number;
  /** Grup üyelerinin süre TOPLAMI (ms). Sıralama anahtarı. */
  totalMs: number;
}

// Varsayılan eşik. 5, Explore'un `minRepeats` varsayılanıyla aynı sayı —
// operatör aynı kavramı iki yüzeyde iki farklı sayıyla öğrenmesin diye.
export const REPEAT_MIN_COUNT = 5;

/**
 * Trace'in tamamında ≥minCount kez geçen (service, span adı) gruplarını
 * döndürür. Toplam süreye göre AZALAN sıralı — "en çok zaman yiyen tekrar
 * deseni" ilk sırada. Eşit toplamda sayı, eşit sayıda ad ile deterministik
 * kırılır (sıralama testte çivileniyor).
 */
export function traceRepeatGroups(
  spans: SpanRow[],
  minCount: number = REPEAT_MIN_COUNT,
): TraceRepeatGroup[] {
  if (!spans || spans.length === 0) return [];
  const acc = new Map<string, TraceRepeatGroup>();
  for (const s of spans) {
    const name = displaySpanName(s);
    // \x01 ayırıcı: hem servis adı hem span adı ':' / '|' içerebilir,
    // kontrol karakteri içeremez.
    const key = s.serviceName + '\x01' + name;
    const g = acc.get(key);
    const durMs = Math.max(0, s.endTime - s.startTime) / 1e6;
    if (g) {
      g.count += 1;
      g.totalMs += durMs;
    } else {
      acc.set(key, { service: s.serviceName, name, count: 1, totalMs: durMs });
    }
  }
  return [...acc.values()]
    .filter(g => g.count >= minCount)
    .sort((a, b) => b.totalMs - a.totalMs
      || b.count - a.count
      || a.name.localeCompare(b.name));
}
