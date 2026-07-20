import { describe, it, expect } from 'vitest';
import { smoothWindow, smoothValues, smoothPoints } from './runtimeSmooth';

describe('smoothWindow', () => {
  it('does not smooth too-few / sparse series', () => {
    expect(smoothWindow(0)).toBe(1);
    expect(smoothWindow(7)).toBe(1);
  });
  it('is odd and clamped to [3,41]', () => {
    expect(smoothWindow(8)).toBe(3);      // round(8/12)=1 → floor 3
    expect(smoothWindow(60)).toBe(5);     // round(60/12)=5
    expect(smoothWindow(170)).toBe(15);   // round(170/12)=14 → +1 odd
    expect(smoothWindow(100000)).toBe(41); // cap
    // always odd
    for (const n of [8, 15, 60, 170, 360, 1440, 100000]) {
      expect(smoothWindow(n) % 2).toBe(1);
    }
  });
  it('effective window ≈ span/12 regardless of cadence (K×spacing property)', () => {
    // 1h window: 21s cadence → ~171 pts; 60s cadence → 60 pts. Both target
    // an effective time-window ≈ span/12 = 300s.
    const span = 3600;
    for (const cadence of [21, 60]) {
      const n = Math.round(span / cadence);
      const k = smoothWindow(n);
      const effWindow = k * (span / n); // K × spacing
      // within a factor of the floor/cap; both land near 5min for a 1h view.
      expect(effWindow).toBeGreaterThan(120);
      expect(effWindow).toBeLessThan(700);
    }
  });
});

describe('smoothValues', () => {
  it('k<=1 is a passthrough copy (not the same ref)', () => {
    const v = [1, 2, 3];
    const out = smoothValues(v, 1);
    expect(out).toEqual([1, 2, 3]);
    expect(out).not.toBe(v);
  });
  it('centered mean dampens a sawtooth toward its mean', () => {
    // pure sawtooth around 100: raw adjacent swing = 40, smoothed ≪ that.
    const raw = [80, 120, 80, 120, 80, 120, 80, 120, 80];
    const out = smoothValues(raw, 3) as number[];
    // interior points average 3 → much closer to 100 than ±20 extremes
    for (let i = 1; i < out.length - 1; i++) {
      expect(Math.abs(out[i] - 100)).toBeLessThan(20);
    }
  });
  it('skips nulls in the window; all-null window → null', () => {
    const out = smoothValues([null, null, null], 3);
    expect(out).toEqual([null, null, null]);
    const mixed = smoothValues([10, null, 20], 3) as (number | null)[];
    // centered k=3 (h=1): i=0 sees [10,null]→10, i=1 sees [10,null,20]→15,
    // i=2 sees [null,20]→20. null contributes nothing.
    expect(mixed[0]).toBeCloseTo(10);
    expect(mixed[1]).toBeCloseTo(15);
    expect(mixed[2]).toBeCloseTo(20);
  });
  it('leaves a constant (e.g. heap limit ref line) unchanged', () => {
    const out = smoothValues([512, 512, 512, 512, 512], 3);
    expect(out).toEqual([512, 512, 512, 512, 512]);
  });
  it('reduces variance monotonically-ish vs raw on noisy data', () => {
    const raw = [10, 90, 20, 80, 30, 70, 40, 60, 50, 55, 45, 65, 35, 75, 25, 85];
    const variance = (a: number[]) => {
      const m = a.reduce((s, x) => s + x, 0) / a.length;
      return a.reduce((s, x) => s + (x - m) ** 2, 0) / a.length;
    };
    const out = smoothValues(raw, 5) as number[];
    expect(variance(out)).toBeLessThan(variance(raw));
  });
});

describe('smoothPoints', () => {
  it('preserves timestamps and length', () => {
    const pts = Array.from({ length: 60 }, (_, i) => ({ time: i * 1e9, value: i % 2 ? 80 : 120 }));
    const out = smoothPoints(pts);
    expect(out).toHaveLength(60);
    expect(out.map(p => p.time)).toEqual(pts.map(p => p.time));
  });
  it('short series (<8 pts) passes through untouched', () => {
    const pts = [{ time: 0, value: 10 }, { time: 1, value: 90 }, { time: 2, value: 10 }];
    expect(smoothPoints(pts)).toBe(pts);
  });
});
