// compareParam.ts — v0.10.311 (chart-layer audit Dilim 2.3): /service
// "Compare to:" seçimi URL'de yaşar (?compare=prior|24h|7d) — link taşır,
// Copy link / SavedViews birebir. localStorage yalnız TOHUM: URL'de anahtar
// yokken saklanan tercih okunur ve URL'ye yazılır (useUrlRange / env oturum
// yapışkanlığı emsali). Sözlük ContextBar'ın compare= anahtarıyla AYNI:
// liste sayfalarındaki 'prior' = buradaki 'prev' (eş-boy önceki pencere);
// 24h/7d yalnız /service'te anlamlı (contextParams.ts başlığı). Saf; React yok.
import type { SpanMetricSeries } from '@/lib/types';
import type { CorePanelMultiItem } from '@/components/chart/corePanelEntry';

export type CompareMode = 'off' | '24h' | '7d' | 'prev';

/** parseCompareParam — URL değeri → mod; anahtar YOK/boş → null (tohum okunur); çöp → null. */
export function parseCompareParam(raw: string | null | undefined): CompareMode | null {
  const v = (raw ?? '').trim().toLowerCase();
  if (v === '') return null;
  if (v === 'off' || v === '0') return 'off';
  if (v === 'prior' || v === '1' || v === 'prev') return 'prev';
  if (v === '24h' || v === '7d') return v;
  return null;
}

/** encodeCompareParam — mod → URL değeri; 'off' → '' (anahtar silinir), 'prev' → 'prior'. */
export function encodeCompareParam(m: CompareMode): string {
  switch (m) {
    case 'off': return '';
    case 'prev': return 'prior';
    default: return m;
  }
}

/** parseStoredCompare — localStorage tohumu; tanınmayan → 'off'. */
export function parseStoredCompare(raw: string | null | undefined): CompareMode {
  return raw === '24h' || raw === '7d' || raw === 'prev' ? raw : 'off';
}

/** compareOffsetNs — hayalet serinin kaydırma miktarı (ns); 'off' → 0. */
export function compareOffsetNs(m: CompareMode, fromNs: number, toNs: number): number {
  switch (m) {
    case '24h': return 24 * 3600 * 1e9;
    case '7d': return 7 * 24 * 3600 * 1e9;
    case 'prev': return toNs - fromNs;
    default: return 0;
  }
}

/** shiftSeriesNs — önceki-dönem serisini bugünün eksenine bindirir (+offset). */
export function shiftSeriesNs(series: SpanMetricSeries[], offsetNs: number): SpanMetricSeries[] {
  if (!offsetNs) return series;
  return series.map(s0 => ({ ...s0, points: s0.points.map(pt => ({ ...pt, time: pt.time + offsetNs })) }));
}

/**
 * ghostItemsFrom — v0.10.315: CorePanelMulti `ghostItems` girdisi. Adlar
 * korunur (CorePanelMulti "(önceki)" ekini kendisi basar), rol 'muted',
 * zamanlar kaydırılmış. offset 0 / boş → undefined (prop hiç geçmez).
 */
export function ghostItemsFrom(items: CorePanelMultiItem[] | undefined, offsetNs: number): CorePanelMultiItem[] | undefined {
  if (!items?.length || offsetNs <= 0) return undefined;
  const out = items
    .filter(it => it.series.some(s0 => s0.points.length > 0))
    .map(it => ({ name: it.name, role: 'muted' as const, series: shiftSeriesNs(it.series, offsetNs) }));
  return out.length ? out : undefined;
}
