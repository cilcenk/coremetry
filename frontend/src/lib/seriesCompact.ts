// seriesCompact.ts — v0.10.186 (perf P5 «nokta kodlaması»): dashboard bundle
// slot'unun SÜTUNSAL kodlamasını tüketicilerin beklediği {time,value}[]
// satırlarına açar. Sunucu sözleşmesi internal/api/series_compact.go:
//   - enc === SERIES_ENC ('col2') ise `cols` dolu, `series` null;
//   - seri düzenliyse {t0,step,v[]} (time = t0 + i·step — step tam saniye
//     olduğu için double aritmetiği tam, test 2^53 üstünde pinler),
//     düzensizse {t[],v[]} açık zamanlar.
// İstemci opt-in: api.dashboardData gövdeye enc:'col' yazar (eski sunucu
// alanı yok sayar, düz şekil döner → çözücü dokunmaz). Bilinmeyen enc →
// `series` anahtarı SİLİNİR ki panel kendi fetch'ine düşsün (null ≠
// undefined: PanelRenderer null'u «üzerine yaz» sayar, boş panel çizerdi).
// v0.10.189 — adımlı şekilde v[i] === null = DELİK (ızgaralı seyrek seri,
// sunucu OBEB adımı + ≥%20 kapsama ile seçer): nokta ÜRETİLMEZ, i ilerler
// (zaman t0 + i·step ızgarada kalır). enc 'col' → 'col2': eski sekme yeni
// şekli bilmez → series silinir → panel kendi fetch'ine düşer.
// Tek çözüm noktası api.dashboardData — tüketiciler dokunulmaz.
import type { SpanMetricSeries } from './types';

/** Tel sözleşmesi sürümü — sunucu seriesEncColumnar ile aynı; şekil değişince İKİSİ birlikte artar (189: col → col2). */
export const SERIES_ENC = 'col2';

export interface CompactSeries { groupKey: string[]; t0?: number; step?: number; t?: number[]; v: (number | null)[] }

export function expandCompactSeries(c: CompactSeries): SpanMetricSeries {
  const t0 = c.t0 ?? 0, step = c.step ?? 0;
  const points: SpanMetricSeries['points'] = [];
  for (let i = 0; i < c.v.length; i++) {
    const value = c.v[i];
    if (value == null) continue; // delik
    points.push({ time: c.t ? c.t[i] : t0 + i * step, value });
  }
  return { groupKey: c.groupKey, points };
}

export interface BundleSlotWire {
  series?: SpanMetricSeries[] | null;
  enc?: string;
  cols?: CompactSeries[];
  [k: string]: unknown;
}

/** Slot kodlanmışsa açar (enc/cols düşer); bilinmeyen enc → series anahtarı silinir; düz slot aynen. */
export function decodeBundleSlot<T extends BundleSlotWire>(slot: T): T {
  if (!slot.enc) return slot;
  const { enc, cols, ...rest } = slot;
  if (enc === SERIES_ENC && cols) return { ...rest, series: cols.map(expandCompactSeries) } as T;
  delete (rest as { series?: unknown }).series;
  return rest as T;
}

export function decodeBundle<T extends BundleSlotWire>(bundle: Record<string, T>): Record<string, T> {
  const out: Record<string, T> = {};
  for (const [k, v] of Object.entries(bundle)) out[k] = decodeBundleSlot(v);
  return out;
}
