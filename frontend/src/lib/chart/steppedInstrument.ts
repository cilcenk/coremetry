// isSteppedInstrument — bir OTel metrik instrument tipinin adım (stepped)
// çizimle gösterilmesi gerekip gerekmediği (v0.9.80, uPlot Aşama 2 madde 1).
//
// Scrape edilen gauge/counter değerleri iki ölçüm ARASINDA değişmez —
// düz çizgiyle bağlamak olmayan bir geçiş uydurur (2 ile 5 arasında
// yavaşça artıyormuş gibi). Adım çizim (uPlot.paths.stepped) değeri bir
// sonraki örneğe kadar sabit tutar; gerçek scrape semantiği budur.
//
// Instrument değerleri backend'den MetricInfo.type olarak gelir
// (metric_points.instrument → catalog): 'gauge' | 'sum' | 'histogram'.
//   - gauge  → anlık ölçüm, örnekler arası sabit → STEPPED
//   - sum    → kümülatif counter, örnekler arası sabit → STEPPED
//   - histogram → dağılım (p50/p95 türetilir, sürekli) → smooth
//   - '' / bilinmeyen → smooth (span-metriği rate/latency, dokunma)
export function isSteppedInstrument(type: string | undefined): boolean {
  const t = (type || '').trim().toLowerCase();
  return t === 'gauge' || t === 'sum';
}
