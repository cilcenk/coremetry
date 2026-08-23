// greeting.ts — v0.9.528. CoSRE sohbetinin açılış karşılaması.
//
// Operatör isteği: "login olduktan sonra kim olduğunu anlayıp ismiyle
// hitap etsin, 'sana nasıl yardımcı olabilirim' desin."
//
// İKİ TASARIM KARARI:
//
//  1. Karşılama LLM ÇAĞRISI YAPMAZ. Deterministik, anında ve her
//     açılışta aynı. CoSRE ilkesiyle de tutarlı: kod karar verir,
//     model anlatır. Bir karşılama cümlesi için 26B modeli beklemek
//     hem yavaş hem de "3 açık P1" gibi bir sayıyı uydurma riski.
//
//  2. Cümle CANLI. "Merhaba Fatih" tek başına kibar ama boş; asıl
//     değer operatörün daha soruyu yazmadan durumu görmesi. Sayı
//     zaten sohbet penceresinin poll ettiği veriden gelir.

import { agoTR } from '@/lib/utils';

export interface GreetingP1 {
  service?: string;
  startedAt: number; // unix ns
}

/** Hitap satırı. Ad yoksa isimsiz — uydurma ada göre hitap etmeyiz. */
export function greetHello(firstName?: string): string {
  const n = (firstName || '').trim();
  return n ? `Merhaba ${n}.` : 'Merhaba 👋';
}

/**
 * Durum satırı. `undefined` = henüz yüklenmedi; o hâlde BOŞ döner —
 * yüklenirken "açık P1 yok" demek yanlış bir iddia olurdu ve operatör
 * onu okuyup sohbeti kapatabilir.
 */
export function greetStatus(p1s: GreetingP1[] | undefined, nowMs = Date.now()): string {
  if (!p1s) return '';
  if (p1s.length === 0) return 'Şu an açık P1 yok — sistem sakin görünüyor.';

  const newest = newestP1(p1s);
  const ago = agoTR(newest.startedAt, nowMs);
  const svc = (newest.service || '').trim();

  if (p1s.length === 1) {
    return svc ? `1 açık P1 var: ${svc}, ${ago}.` : `1 açık P1 var — ${ago}.`;
  }
  return svc
    ? `${p1s.length} açık P1 var — en yenisi ${svc}, ${ago}.`
    : `${p1s.length} açık P1 var — en yenisi ${ago}.`;
}

/** En yeni P1 — sıralamaya GÜVENMEZ, çağıran listeyi nasıl aldıysa. */
export function newestP1(p1s: GreetingP1[]): GreetingP1 {
  return p1s.reduce((a, b) => (b.startedAt > a.startedAt ? b : a));
}


/**
 * Türkçe göreli zaman. v0.9.1317'de lib/utils.ts'e TAŞINDI (orada
 * fmtDurShort/fmtAgoNs/tsRel ile aynı evde; v0.8.463 konsolidasyonu).
 * Buradan re-export ediliyor: bu modülün kendi kullanımı (greetStatus)
 * ve greeting.test.ts'in './greeting' importu aynen çalışsın diye.
 */
export { agoTR };
