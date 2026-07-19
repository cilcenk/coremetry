import { describe, it, expect } from 'vitest';
import { histogramResultToHeatmap } from './histogramHeatmap';
import type { HistogramResult } from '@/lib/types';

// C2 (v0.9.109) — HistogramResult → LatencyHeatmap adapter. The two mismatches
// it bridges (the +Inf overflow bin, and seconds→ms bound scaling) are exactly
// where a wrong grid would silently mislabel an operator's latency density.

function hr(partial: Partial<HistogramResult>): HistogramResult {
  return { bounds: [], times: [], counts: [], p50: [], p95: [], p99: [], skipped: 0, ...partial };
}

describe('histogramResultToHeatmap', () => {
  it('appends a synthetic overflow bin above the top bound (counts len = bounds+1)', () => {
    // 3 bounds → counts columns have 4 entries (last = +Inf overflow).
    const r = histogramResultToHeatmap(hr({
      bounds: [1, 2, 5],
      times: [100, 200],
      counts: [[10, 5, 2, 1], [0, 1, 3, 0]],
    }));
    // overflow = top + (top - prev) = 5 + (5-2) = 8
    expect(r.durationBins).toEqual([1, 2, 5, 8]);
    expect(r.counts).toEqual([[10, 5, 2, 1], [0, 1, 3, 0]]);
    expect(r.times).toEqual([100, 200]);
    expect(r.maxCount).toBe(10);
  });

  it('scales seconds-valued bounds ×1000 to ms', () => {
    const r = histogramResultToHeatmap(hr({
      bounds: [0.1, 0.5, 1],
      times: [1],
      counts: [[3, 4, 2, 1]],
    }), 's');
    // [100, 500, 1000, overflow]; overflow = 1000 + (1000-500) = 1500
    expect(r.durationBins).toEqual([100, 500, 1000, 1500]);
    expect(r.maxCount).toBe(4);
  });

  it('leaves ms bounds unscaled (default + explicit)', () => {
    const base = hr({ bounds: [10, 20], times: [1], counts: [[1, 2, 3]] });
    expect(histogramResultToHeatmap(base).durationBins).toEqual([10, 20, 30]);
    expect(histogramResultToHeatmap(base, 'ms').durationBins).toEqual([10, 20, 30]);
  });

  it('single bound → 2× overflow', () => {
    const r = histogramResultToHeatmap(hr({ bounds: [50], times: [1], counts: [[7, 2]] }));
    expect(r.durationBins).toEqual([50, 100]);
    expect(r.maxCount).toBe(7);
  });

  it('empty histogram → single bin, zero maxCount (panel shows empty)', () => {
    const r = histogramResultToHeatmap(hr({}));
    expect(r.durationBins).toEqual([1]);
    expect(r.counts).toEqual([]);
    expect(r.maxCount).toBe(0);
    expect(r.times).toEqual([]);
  });

  it('tolerates ragged/short count rows without throwing (missing → 0)', () => {
    const r = histogramResultToHeatmap(hr({
      bounds: [1, 2],
      times: [1],
      counts: [[5]], // short row; bins expects 3 entries
    }));
    expect(r.counts).toEqual([[5, 0, 0]]);
    expect(r.maxCount).toBe(5);
  });
});
