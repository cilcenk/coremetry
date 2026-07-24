// legendVisibility.ts (Grafana-parite #2) — lejant tıkı → seri görünürlüğü
// saf çekirdeği. Dört preset'in lejant etkileşimi aynı kurallara delege eder
// ve jest dili TEK (review 8/8 #8 — StatsLegend'in ters eşlemesi düzeltildi):
//   düz tık = isolate (yalnız o seri), Ctrl/Cmd-tık = toggle (gizle/göster)
//   — StatsLegend (OVC/TC) da, TimeSeriesPanel lejantı + MultiLineChart
//   uPlot-lejantı da (yerleşik v0.5.364 sözleşmesi + Grafana kontratı).
// Jest→işlem eşlemesi preset'te; İŞLEM semantiği burada, testli
// (legendVisibility.test.ts). Uygulama tarafı hep uPlot
// setSeries(i+1, {show}) — rebuild yok, zoom/imleç yaşar; y ekseni uPlot'un
// görünür-seriden autoscale'i + zoomlu fast-path'te yRefitScale ile refit olur.
//
// Kural: bir işlem sonucu HİÇBİR seri görünür kalmayacaksa hepsi geri gelir
// ("hepsi gizliyse hepsini geri getir") — boş grafiğe kilitlenmek operatöre
// hiçbir şey söylemez (Grafana da isolate-toggle'da böyle döner).

// toggleSeriesVisibility — i'nin görünürlüğünü çevir; sonuç tamamen boşsa
// hepsini geri getir. Out-of-range i → değişmemiş kopya.
export function toggleSeriesVisibility(vis: readonly boolean[], i: number): boolean[] {
  if (i < 0 || i >= vis.length) return [...vis];
  const next = [...vis];
  next[i] = !next[i];
  if (next.every(v => !v)) return next.map(() => true);
  return next;
}

// isolateSeriesVisibility — yalnız i görünür; i ZATEN tek görünense hepsini
// geri getir (isolate-toggle). Out-of-range i → değişmemiş kopya.
export function isolateSeriesVisibility(vis: readonly boolean[], i: number): boolean[] {
  if (i < 0 || i >= vis.length) return [...vis];
  const onlyThisVisible = vis[i] && vis.every((v, k) => (k === i ? true : !v));
  if (onlyThisVisible) return vis.map(() => true);
  return vis.map((_, k) => k === i);
}

// resetSeriesVisibility — n serinin hepsi görünür (rebuild reset'i).
export function resetSeriesVisibility(n: number): boolean[] {
  return Array.from({ length: Math.max(0, n) }, () => true);
}
