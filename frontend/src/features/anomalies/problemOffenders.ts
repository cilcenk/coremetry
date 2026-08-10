// problemOffenders — v0.9.962 (UX denetimi G5 / Ö8). Problem detayının
// "hangi endpoint yavaşlattı / hata verdi" sorusunu SAYFADA cevaplaması
// için gereken iki saf parça.
//
// ─── Hangi boşluğu kapatıyor ─────────────────────────────────────────
// Alarm servis seviyesinde geliyor. Hangi operasyonun sorumlu olduğunu
// öğrenmek için operatör /service'e geçip pencereyi ELLE kurup
// Operations tablosunu sıralamak zorundaydı — denetimin "ideal yol
// farkının en büyük parçası" dediği adım. Veri zaten var ve zaten
// MV'den geliyor (/api/services/{svc}/operations); eksik olan yalnız
// problem penceresiyle sorulmasıydı. YENİ BACKEND UCU YOK.

import type { OperationSummary } from '@/lib/types';

/** Açık problem penceresinin basamağı (ms). 60 sn: bir 5-dk MV kovasının
 *  altında kalır, yani basamak veriyi kabalaştırmaz; sadece anahtarı
 *  sabitler. */
export const PROBLEM_WINDOW_RUNG_MS = 60_000;

/**
 * problemWindowNs — problem penceresi, SORGU için güvenli hâliyle.
 *
 * ─── Neden basamak (v0.9.962'nin tek zor kararı) ────────────────────
 * Bileşen pencerenin üst ucunu `problem.resolvedAt || Date.now()` diye
 * hesaplıyor. LİNKLER için bu doğru (tıklama anında hesaplanır), ama
 * SORGU anahtarı için felakettir: açık bir problemde `now` her render'da
 * değişir, React Query her seferinde yeni bir anahtar görür ve
 * durmadan yeniden çeker; sunucu tarafında da serveCached anahtarı
 * sınırsız sayıda ayrı değere dağılır, yani önbellek fiilen kapanır.
 * Bu deponun tekrar eden sınıfı — v0.5.184'ün `timeRangeToNs`i render'da
 * `now()` tıklatıyordu, aynı hastalık.
 *
 * Çözüm pencereyi DARALTMAK değil (o veriyi değiştirirdi), üst ucu
 * yukarı doğru basamağa yuvarlamak: aynı dakika içindeki her render
 * AYNI anahtarı üretir, veri kapsamı asla küçülmez.
 *
 * Kapalı problemde yuvarlama YOK: `resolvedAt` zaten sabit bir sayı ve
 * onu oynatmak problemin gerçek sınırını yalanlardı.
 */
export function problemWindowNs(
  startedAtNs: number, resolvedAtNs: number | undefined, nowMs: number,
  rungMs: number = PROBLEM_WINDOW_RUNG_MS,
): { fromNs: number; toNs: number } {
  const fromNs = startedAtNs - 60 * 60 * 1e9; // 1 saat öncesi — bileşenle aynı
  if (resolvedAtNs) {
    return { fromNs, toNs: resolvedAtNs + 10 * 60 * 1e9 };
  }
  // Açık: `now`u basamağa YUKARI yuvarla, sonra aynı 10 dk payı.
  const rungMsUp = Math.ceil(nowMs / rungMs) * rungMs;
  return { fromNs, toNs: rungMsUp * 1e6 + 10 * 60 * 1e9 };
}

/**
 * topOffenders — pencereye düşen operasyonların "en çok katkı veren"
 * ilk N'i.
 *
 * SIRALAMA ÖLÇÜTÜ TOPLAM ZAMAN (spanCount × avgDurationMs), p99 değil.
 * Gerekçe: p99 sıralaması saniyede bir kez çağrılan bir yönetim ucunu
 * en tepeye koyar ve o uç servisin gecikmesini AÇIKLAMAZ. Operatörün
 * sorduğu şey "servisin zamanı nereye gidiyor"; cevabı toplam zamandır.
 * Aynı ölçüt Overview'un DB kartında da kullanılıyor (Time/req × çağrı).
 *
 * Eşitlikte ada göre — iki render aynı listeyi aynı sırayla vermeli.
 */
export function topOffenders(ops: OperationSummary[], n = 5): OperationSummary[] {
  const total = (o: OperationSummary) => o.spanCount * o.avgDurationMs;
  return [...ops]
    .sort((a, b) => (total(b) - total(a)) || a.name.localeCompare(b.name))
    .slice(0, n);
}
