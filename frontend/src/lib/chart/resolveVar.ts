// resolveVar — bir `var(--token)` CSS değişkenini canvas stroke/fill için
// somut hex/rgb'ye çözer. uPlot 2D canvas'a çizer ve CSS var'larını
// doğrudan okuyamaz, o yüzden token'lar draw zamanında çözülmeli.
//
// v0.9.75 (chart-consolidation Adım 0) — dört chart bileşeninde
// birbirinin AYNISI olan çözümleyicinin TEK kopyası:
//   OverviewChart.cssVar + TimeChart.cssVar (byte-identical) +
//   TimeSeriesPanel.resolveColor (aynı regex, aynı fallback).
// Token değilse (ham renk) olduğu gibi geçer; çözülemezse girdi döner.
export function resolveVar(c: string): string {
  const m = /^var\((--[\w-]+)\)$/.exec(c.trim());
  if (!m) return c;
  return getComputedStyle(document.documentElement).getPropertyValue(m[1]).trim() || c;
}
