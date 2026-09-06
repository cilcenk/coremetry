// stripStat.ts — /traces şeridindeki yanıt-süresi çizgisinin İSTATİSTİĞİ
// (v0.10.513, operatör: "Median yerine p95 olsun default. Seçilebilir
// olabilir, çok yer kaplamasın. Expand'e gerek yok, default shrink kalsın").
//
// Neden p95 varsayılan: median kuyruğu saklar (%10 istek 3 s'ye çıksa
// median kıpırdamaz); Dynatrace median + "slowest 10%" çifti çizer,
// Datadog p50…p99 birlikte. Şeridin işi "şu an sapma var mı" — p95 hem
// SLO eşiğiyle konuşur hem 2 dk kovada (≈40 örnek) p99 kadar gürültülü
// değildir (p99 orada = en yavaş tek trace). p99 yine seçilebilir; seçim
// URL'de (`?rt=`), varsayılan URL'ye yazılmaz. SAF.

export type StripStat = 'p50' | 'p95' | 'p99';
export const STRIP_STATS: readonly StripStat[] = ['p50', 'p95', 'p99'];
export const STRIP_STAT_DEFAULT: StripStat = 'p95';

/** URL/ham değer → istatistik; tanınmayan/boş → varsayılan (p95). */
export function parseStripStat(raw: string | null | undefined): StripStat {
  return raw === 'p50' || raw === 'p95' || raw === 'p99' ? raw : STRIP_STAT_DEFAULT;
}

/** Lejant etiketi: p50 "median" diye okunur (Dynatrace dili), diğerleri adıyla. */
export function stripStatLabel(stat: StripStat): string {
  return stat === 'p50' ? 'median' : stat;
}

/** Başlık istatistiği etiketi ("P95 MAX"): penceredeki en yüksek kova. */
export function stripStatHeaderLabel(stat: StripStat): string {
  return `${stat === 'p50' ? 'MEDIAN' : stat.toUpperCase()} MAX`;
}
