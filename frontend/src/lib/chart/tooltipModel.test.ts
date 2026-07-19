// tooltipModel — v0.9.101 (Grafana-parity Adım 1). The pure "all series"
// tooltip model: drop empty values, sort by value desc, format via fmtSmart.
// This table pins the contract so every panel's hover tooltip orders + formats
// identically (the whole reason it's shared, not per-panel).

import { describe, it, expect } from 'vitest';
import { sortedTooltipRows, type TooltipItem } from './tooltipModel';

describe('sortedTooltipRows — ordering', () => {
  it('sorts by value DESC by default (hottest series first)', () => {
    const items: TooltipItem[] = [
      { label: 'a', color: '#1', value: 10 },
      { label: 'b', color: '#2', value: 90 },
      { label: 'c', color: '#3', value: 50 },
    ];
    expect(sortedTooltipRows(items).map(r => r.label)).toEqual(['b', 'c', 'a']);
  });

  it('sort:"none" preserves caller order (e.g. p50/p95/p99 ladder)', () => {
    const items: TooltipItem[] = [
      { label: 'p50', color: '#1', value: 5 },
      { label: 'p95', color: '#2', value: 40 },
      { label: 'p99', color: '#3', value: 120 },
    ];
    expect(sortedTooltipRows(items, 'none').map(r => r.label)).toEqual(['p50', 'p95', 'p99']);
  });

  it('is stable for ties (equal values keep input order → no poll reshuffle)', () => {
    const items: TooltipItem[] = [
      { label: 'first', color: '#1', value: 7 },
      { label: 'second', color: '#2', value: 7 },
      { label: 'third', color: '#3', value: 7 },
    ];
    expect(sortedTooltipRows(items).map(r => r.label)).toEqual(['first', 'second', 'third']);
  });
});

describe('sortedTooltipRows — empties dropped', () => {
  it('drops null / undefined / NaN / Infinity values', () => {
    const items: TooltipItem[] = [
      { label: 'ok', color: '#1', value: 3 },
      { label: 'null', color: '#2', value: null },
      { label: 'undef', color: '#3', value: undefined },
      { label: 'nan', color: '#4', value: NaN },
      { label: 'inf', color: '#5', value: Infinity },
    ];
    expect(sortedTooltipRows(items).map(r => r.label)).toEqual(['ok']);
  });

  it('keeps a real zero (0 is a value, not a gap)', () => {
    const items: TooltipItem[] = [
      { label: 'zero', color: '#1', value: 0 },
      { label: 'pos', color: '#2', value: 5 },
    ];
    expect(sortedTooltipRows(items).map(r => r.label)).toEqual(['pos', 'zero']);
  });
});

describe('sortedTooltipRows — fmtSmart units', () => {
  it('formats each value through the shared unit-aware formatter', () => {
    const rows = sortedTooltipRows([
      { label: 'lat', color: '#1', value: 234, unit: 'ms' },
      { label: 'rate', color: '#2', value: 12500, unit: '' },
      { label: 'err', color: '#3', value: 3.4, unit: '%' },
    ]);
    const byLabel = Object.fromEntries(rows.map(r => [r.label, r.text]));
    expect(byLabel.lat).toBe('234ms');
    expect(byLabel.rate).toBe('12.5k');
    expect(byLabel.err).toBe('3.40%'); // fmtSmart: 2 decimals below 10%
  });

  it('handles a unit with a leading space (dual-axis presets pass " ms")', () => {
    const [row] = sortedTooltipRows([{ label: 'p99', color: '#1', value: 1500, unit: ' ms' }]);
    expect(row.text).toBe('1.5s'); // ms auto-promotes to s past 1000
  });

  it('carries the raw value through for bold-nearest callers', () => {
    const [row] = sortedTooltipRows([{ label: 'x', color: '#1', value: 42, unit: 'ms' }]);
    expect(row.value).toBe(42);
  });
});
