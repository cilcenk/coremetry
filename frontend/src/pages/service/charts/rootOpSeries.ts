// rootOpSeries — giriş operasyonu serilerinin AD ve SIRALAMA çekirdeği.
//
// v0.9.484'te Service Overview "Response time" kartının operasyon kırılımı
// için yazıldı (operatör onayı: "root spanler için multichart").
//
// v0.9.845 — BU DOSYA ARTIK BİR PANELE AİT DEĞİL. Kırılım paneli v0.9.736'da
// operatör düzeniyle görünümden kalkmıştı; hook'u (useRootOpLatency) ve çizgi
// projeksiyonu (buildRootOpLines) v0.9.844'te, v2 projeksiyonu
// (buildRootOpItems + RootOpItem/RootOpItems) ile indeks-paleti
// (rootOpColorAt + ROOT_OP_COLORS) ve etiketleyicisi (rootOpLineLabel) bu
// sürümde söküldü — hepsi tüketicisizdi. Geriye-uyum şimi bırakmıyoruz.
//
// Renk artık BİLEREK burada değil: v2'de renk CorePanel'in işi (seriesRole
// sözleşmesi — aynı operasyon her yüzeyde aynı renk), index-palet değil.
//
// Hayatta kalan üçlü — rootOpName / rootOpArea / rankRootOps — tek tüketicisi
// routeSeries (Response time · by route) olan SAF sıralama çekirdeği:
// "en yoğun N seriyi ALAN (Σ|değer|) ölçütüyle seç, eşitlikte ada göre".
// Explore'un panel slotu (PanelStack "Biggest-by-area win the panel slots")
// aynı ölçütü kullanıyor — üçüncü bir sıralama fikri icat etmiyoruz.
//
// Dosya SAF: fetch yok, React yok.

import type { SpanMetricSeries } from '@/lib/types';

// rankRootOps'un VARSAYILAN kapı. 5: kartın yüksekliği ~150px ve lejant
// tablosu varsayılan kapalı (v0.9.483) — daha fazlası okunmuyor. routeSeries
// kendi kapını (ROUTE_TOP_N = 10) açıkça geçiyor.
export const ROOT_OP_TOP_N = 5;

// groupBy=name tek boyutlu; yine de groupKey dizisi geliyor. İlk boş
// olmayan eleman = operasyon adı. Hepsi boşsa "(adsız)" — CH'de name=''
// olan span'ler var (enstrümantasyon hatası) ve boş etiket lejantta
// görünmez satır olurdu.
// v0.10.369 — `fallback`: route kırılımı boş anahtarı "(adsız)" diye
// değil, NE olduğunu söyleyerek etiketler (routeSeries.ROUTE_UNLABELLED).
export function rootOpName(groupKey: readonly string[] | undefined, fallback = '(adsız)'): string {
  const hit = (groupKey ?? []).find(k => k != null && k !== '');
  return hit ?? fallback;
}

/** Alan (|değer| toplamı) — PanelStack'in top-N ölçütünün aynısı. P95
 *  serisinde alan ≈ "sürekli ve yüksek gecikme", yani operatörün bu kırılıma
 *  bakarken aradığı şey. */
export function rootOpArea(s: SpanMetricSeries): number {
  return (s.points ?? []).reduce((a, p) => a + Math.abs(p.value ?? 0), 0);
}

/** Alana göre azalan sırala + kap. Eşitlikte ada göre (deterministik
 *  render: aynı veri → aynı sıra, dolayısıyla aynı renk ataması). */
export function rankRootOps(series: readonly SpanMetricSeries[], cap = ROOT_OP_TOP_N): SpanMetricSeries[] {
  return [...series]
    .map(s => ({ s, area: rootOpArea(s) }))
    .sort((a, b) => (b.area - a.area) || rootOpName(a.s.groupKey).localeCompare(rootOpName(b.s.groupKey)))
    .slice(0, Math.max(0, cap))
    .map(x => x.s);
}
