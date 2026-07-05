import { describe, expect, it } from 'vitest';
import { annotationsInWindow } from './chartAnnotations';

// v0.8.284 — chart annotation overlay (Uptrace-inspired A7). annotationsInWindow
// is the pure windowing/dedup/cap step feeding the uPlot canvas draw hook that
// overlays operator events (deploy/config/incident/maintenance) on the
// metric/Explore charts. It runs at overlay-composition time (bounds the marker
// count + collapses sub-pixel clusters); the draw hook re-clamps to the live
// zoom scale. All timestamps are unix NANOSECONDS end-to-end (matches
// api.listEvents + serviceDeploys). Table pins: window clamp (inclusive
// boundaries), out-of-window drop, per-kind sub-gap dedup (distinct kinds at the
// same instant both survive), most-recent cap, ascending sort, junk filtering.

const S = 1_000_000_000; // 1s in ns
const ev = (time: number, kind = 'deploy', label = 'x') => ({ time, kind, label });

describe('annotationsInWindow', () => {
  it('returns [] for empty/undefined input', () => {
    expect(annotationsInWindow(undefined, 0, 100 * S)).toEqual([]);
    expect(annotationsInWindow(null, 0, 100 * S)).toEqual([]);
    expect(annotationsInWindow([], 0, 100 * S)).toEqual([]);
  });

  it('returns [] for an invalid window (to <= from)', () => {
    expect(annotationsInWindow([ev(5 * S)], 100 * S, 100 * S)).toEqual([]);
    expect(annotationsInWindow([ev(5 * S)], 100 * S, 50 * S)).toEqual([]);
  });

  it('drops events outside [from,to] and keeps inclusive boundaries', () => {
    const out = annotationsInWindow(
      [ev(9 * S), ev(10 * S), ev(50 * S), ev(90 * S), ev(91 * S)],
      10 * S, 90 * S,
      { minGapNs: 0 },
    );
    expect(out.map(a => a.timeUnixNs)).toEqual([10 * S, 50 * S, 90 * S]);
  });

  it('sorts ascending regardless of input order', () => {
    const out = annotationsInWindow(
      [ev(30 * S), ev(10 * S), ev(20 * S)],
      0, 100 * S, { minGapNs: 0 },
    );
    expect(out.map(a => a.timeUnixNs)).toEqual([10 * S, 20 * S, 30 * S]);
  });

  it('collapses near-coincident SAME-kind markers, keeping the earliest', () => {
    const out = annotationsInWindow(
      [ev(10 * S, 'deploy'), ev(10 * S + 1000, 'deploy'), ev(80 * S, 'deploy')],
      0, 100 * S, { minGapNs: S },
    );
    expect(out.map(a => a.timeUnixNs)).toEqual([10 * S, 80 * S]);
  });

  it('keeps DISTINCT kinds at the same instant (dedup is per-kind)', () => {
    const out = annotationsInWindow(
      [ev(10 * S, 'deploy'), ev(10 * S, 'incident')],
      0, 100 * S, { minGapNs: S },
    );
    expect(out.map(a => a.kind).sort()).toEqual(['deploy', 'incident']);
    expect(out.length).toBe(2);
  });

  it('caps to the most-recent `max` after windowing', () => {
    const raw = Array.from({ length: 10 }, (_, i) => ev((i + 1) * S, `k${i}`));
    const out = annotationsInWindow(raw, 0, 100 * S, { minGapNs: 0, max: 3 });
    expect(out.map(a => a.timeUnixNs)).toEqual([8 * S, 9 * S, 10 * S]);
  });

  it('filters non-finite / malformed timestamps', () => {
    const raw = [
      ev(10 * S),
      { time: NaN, kind: 'deploy', label: 'nan' },
      { time: Infinity, kind: 'deploy', label: 'inf' },
      ev(20 * S),
    ];
    const out = annotationsInWindow(raw, 0, 100 * S, { minGapNs: 0 });
    expect(out.map(a => a.timeUnixNs)).toEqual([10 * S, 20 * S]);
  });

  it('defaults kind to "custom" and label to "" when missing', () => {
    const out = annotationsInWindow(
      [{ time: 10 * S } as { time: number; kind?: string; label?: string }],
      0, 100 * S, { minGapNs: 0 },
    );
    expect(out[0].kind).toBe('custom');
    expect(out[0].label).toBe('');
  });
});
