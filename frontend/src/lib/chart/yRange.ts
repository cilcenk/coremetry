import type uPlot from 'uplot';

// yRangeHeadroom — canlı veri ekstremumundan türetilen y-aralığı:
// 0-tabanlı, %10 üst boşluk, taban 1. Bir FONKSİYON olmasının sebebi
// u.setData()'nın fast-path'te ekseni yeniden fit etmesi (build-time
// sabit max yerine). Stacked modda uPlot'un auto dataMax'i en üst
// kümülatif çizgidir — eski açık "max of cum[last]" ile birebir eşleşir.
//
// v0.9.75 (chart-consolidation Adım 0) — OverviewChart.yRange ile
// TimeChart.yRange BYTE-IDENTICAL'dı; tek kopya.
export function yRangeHeadroom(_u: uPlot, _min: number, max: number): [number, number] {
  return [0, max > 0 ? max * 1.1 : 1];
}
