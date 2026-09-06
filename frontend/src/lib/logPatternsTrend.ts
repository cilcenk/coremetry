// logPatternsTrend — v0.10.508 (log arama denetimi C6): Desenler panelinin
// Δ hücresi. Taban = hemen önceki eşit pencerenin ÖRNEKLEMESİ (tek ES
// sayfası); bu yüzden "yeni" bir kanıt değil ipucudur ("örneklemede yeni").
import type { LogPatternGroup, LogPatternsBaseline } from './types';

export type TrendCell =
  | { kind: 'none' }                     // taban istenmedi / okunamadı
  | { kind: 'new' }                      // önceki örneklemede yok
  | { kind: 'ratio'; ratio: number; up: boolean; flat: boolean };

export function trendCell(g: LogPatternGroup, base: LogPatternsBaseline | undefined): TrendCell {
  if (!base || base.degraded) return { kind: 'none' };
  if (g.new) return { kind: 'new' };
  const r = g.ratio ?? 0;
  if (!(r > 0)) return { kind: 'none' };
  return { kind: 'ratio', ratio: r, up: r >= 2, flat: r > 0.5 && r < 2 };
}

export function trendLabel(c: TrendCell): string {
  if (c.kind === 'new') return 'YENİ';
  if (c.kind === 'ratio') return `×${c.ratio >= 10 ? c.ratio.toFixed(0) : c.ratio.toFixed(1)}`;
  return '—';
}

// trendSortValue — sıralama: yeni en üstte, sonra oran; tabansız 0.
export function trendSortValue(g: LogPatternGroup, base: LogPatternsBaseline | undefined): number {
  const c = trendCell(g, base);
  if (c.kind === 'new') return Number.MAX_SAFE_INTEGER;
  if (c.kind === 'ratio') return c.ratio;
  return 0;
}
