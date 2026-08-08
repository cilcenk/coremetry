// seriesWindow — MV-kovalı seri uçlarının ORTAK ZARF yardımcıları
// (v0.9.819). Sunucu tarafındaki ikizi internal/chstore/series_window.go.
//
// /api/endpoints/series ve /api/databases/series aynı iki gerçeği taşır ve
// aynı iki şekilde çizer; mantık burada tek nüsha, tablo-güdümlü test
// altında.

import type { SeriesWindow } from './types';

/**
 * splitPartialTail — DOLMAKTA olan son kovayı serinin geri kalanından
 * ayırır.
 *
 * NEDEN: canlı bir pencerede en son kova henüz tamamlanmamıştır; sayısı
 * düşük gelir ve grafik her yenilemede sonu aşağı kıvrılan sahte bir
 * DÜŞÜŞ çizer. Operatör bunu "trafik azaldı" diye okur ve olmayan bir
 * olayı kovalar.
 *
 * Nokta ATILMAZ (veri gerçek, sadece eksik) — ayrı bir KESİKLİ seri
 * olarak çizilir. Kesikli çizgi bu kod tabanında zaten "bu ölçüm
 * diğerlerinden farklı" demek (v0.9.793 ghost/formül kanalı), yani yeni
 * bir görsel dil icat edilmiyor.
 *
 * tail, son SOLID noktayı da içerir: kesikli parça boşlukta başlamasın,
 * çizgi kopmasın.
 */
export function splitPartialTail<T>(
  points: T[], partial?: boolean,
): { solid: T[]; tail: T[] } {
  if (!partial || points.length < 2) return { solid: points, tail: [] };
  return { solid: points.slice(0, -1), tail: points.slice(-2) };
}

/**
 * coveredRangeNote — kapsanan aralık istenenden ERKEN başlıyorsa bunu
 * söyleyen cümle; başlamıyorsa null.
 *
 * MV kovaları BAŞLANGIÇLARIYLA etiketli: from=10:03 istendiğinde sunucu
 * 10:00 kovasından başlamak zorunda (yoksa 3 dakikalık trafik sessizce
 * düşerdi). Grafiğin sol kenarı bu yüzden seçilen pencereden önce
 * başlayabiliyor ve bu, açıklanmadığında "eksen yanlış" gibi görünüyor.
 */
export function coveredRangeNote(
  w: Pick<SeriesWindow, 'coveredFromNs' | 'bucketSeconds'> | undefined,
  requestedFromNs: number,
): string | null {
  if (!w || !w.coveredFromNs || !(w.coveredFromNs < requestedFromNs)) return null;
  const backSec = Math.round((requestedFromNs - w.coveredFromNs) / 1e9);
  if (backSec <= 0) return null;
  return `Seri, seçilen pencereden ${backSec} sn önce başlıyor: kovalar ` +
    `${w.bucketSeconds} sn'lik ızgaraya oturuyor ve baştaki kısmi kova ` +
    'atılsaydı o aralıktaki trafik sessizce kaybolurdu.';
}

/**
 * partialBucketNote — son kova dolmaktaysa açıklama, değilse null.
 */
export function partialBucketNote(w: Pick<SeriesWindow, 'partialLastBucket' | 'bucketSeconds'> | undefined): string | null {
  if (!w?.partialLastBucket) return null;
  return `Son nokta KESİKLİ: içinde bulunduğumuz ${w.bucketSeconds} sn'lik ` +
    'kova hâlâ doluyor, yani sayısı düşük görünür — gerçek bir düşüş değil.';
}
