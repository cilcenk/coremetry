// legendVisibility — Grafana-parite #2. Lejant görünürlük çekirdeğinin
// kontratı: toggle/isolate/reset + "hepsi gizliyse hepsini geri getir" kuralı.
// StatsLegend (OVC/TC), TimeSeriesPanel lejantı ve MultiLineChart
// uPlot-lejantı bu semantiğe delege eder — panel-başına kopya yok.

import { describe, it, expect } from 'vitest';
import {
  toggleSeriesVisibility,
  isolateSeriesVisibility,
  resetSeriesVisibility,
} from './legendVisibility';

describe('toggleSeriesVisibility', () => {
  it('hides a visible series', () => {
    expect(toggleSeriesVisibility([true, true, true], 1)).toEqual([true, false, true]);
  });

  it('re-shows a hidden series', () => {
    expect(toggleSeriesVisibility([true, false, true], 1)).toEqual([true, true, true]);
  });

  it('hiding the LAST visible series restores all (never an empty chart)', () => {
    expect(toggleSeriesVisibility([false, true, false], 1)).toEqual([true, true, true]);
  });

  it('single-series chart: toggling it off restores it (same rule)', () => {
    expect(toggleSeriesVisibility([true], 0)).toEqual([true]);
  });

  it('does not mutate the input', () => {
    const vis = [true, true];
    toggleSeriesVisibility(vis, 0);
    expect(vis).toEqual([true, true]);
  });

  it.each([-1, 3])('out-of-range index %i is a no-op copy', (i) => {
    expect(toggleSeriesVisibility([true, false, true], i)).toEqual([true, false, true]);
  });
});

describe('isolateSeriesVisibility', () => {
  it('isolates one series out of all-visible', () => {
    expect(isolateSeriesVisibility([true, true, true], 1)).toEqual([false, true, false]);
  });

  it('isolating the already-isolated series restores all (isolate-toggle)', () => {
    expect(isolateSeriesVisibility([false, true, false], 1)).toEqual([true, true, true]);
  });

  it('isolating a HIDDEN series shows only it (matches MLC/TSP legacy)', () => {
    expect(isolateSeriesVisibility([true, false, true], 1)).toEqual([false, true, false]);
  });

  it('re-isolating switches the isolated series', () => {
    expect(isolateSeriesVisibility([true, false, false], 2)).toEqual([false, false, true]);
  });

  it('does not mutate the input', () => {
    const vis = [true, true];
    isolateSeriesVisibility(vis, 0);
    expect(vis).toEqual([true, true]);
  });

  it.each([-1, 3])('out-of-range index %i is a no-op copy', (i) => {
    expect(isolateSeriesVisibility([true, false, true], i)).toEqual([true, false, true]);
  });
});

describe('resetSeriesVisibility', () => {
  it('returns n trues', () => {
    expect(resetSeriesVisibility(3)).toEqual([true, true, true]);
  });

  it('n=0 → empty; negative clamps to empty', () => {
    expect(resetSeriesVisibility(0)).toEqual([]);
    expect(resetSeriesVisibility(-2)).toEqual([]);
  });
});
