import { describe, expect, it } from 'vitest';
import { STEP_RUNGS, stepForWidth, quantizeWidth } from './chartStep';

// GRAN-A (v0.8.245) — Grafana-style width-aware step. stepForWidth picks the
// step a chart should REQUEST for its pixel budget (~2px/point, 120–720
// points, snapped UP to a rung); the backend min-step clamp (v0.8.243) is the
// safety floor, so this table pins the request side only. quantizeWidth is
// the refetch-churn guard: the 200px bucket (not the raw width) feeds
// stepForWidth, so its bucketing IS the cache-key contract.

const HOUR = 3600;
const DAY = 86400;

describe('stepForWidth — window × width table', () => {
  const cases: Array<[desc: string, rangeSec: number, widthPx: number, want: number]> = [
    // 1h: 1400px → 700 points → raw ≈5.14s, snaps UP past the 5s rung to 10s;
    // 1600px hits the 720-point cap → raw exactly 5s → the 5s rung itself.
    ['1h @ 1400px', HOUR, 1400, 10],
    ['1h @ 1600px', HOUR, 1600, 5],
    // 1h @ 600px → 300 points → raw 12s → 15s (the design's laptop half-pane).
    ['1h @ 600px', HOUR, 600, 15],
    // 24h: same cap boundary — 700 points → raw ≈123.4s → 300s; 720 → 120s.
    ['24h @ 1400px', DAY, 1400, 300],
    ['24h @ 1600px', DAY, 1600, 120],
    // 5m on a wide monitor → raw ≈0.43s → the 1s floor rung. The backend
    // min-step clamp (v0.8.243) lifts this to the metric's export interval.
    ['5m @ 1400px', 300, 1400, 1],
    // Narrow widths clamp to 120 points — never coarser than the backend's
    // old ~120-point auto ladder (100px would be 50 points unclamped).
    ['1h @ 100px', HOUR, 100, 30],
    // raw exactly on a rung returns that rung (>= contract, not >).
    ['30m @ 240px (raw=15)', 1800, 240, 15],
    // 90d on the narrowest bucket → raw 38880s → the 12h rung.
    ['90d @ 400px', 90 * DAY, 400, 43200],
  ];
  it.each(cases)('%s → %ds', (_desc, rangeSec, widthPx, want) => {
    expect(stepForWidth(rangeSec, widthPx)).toBe(want);
  });

  it('beyond the 1d rung → whole multiples of 1d (dev-window contract)', () => {
    // 2y @ 720 points → raw 87600s > 86400 → ceil to 2d.
    expect(stepForWidth(2 * 365 * DAY, 2400)).toBe(2 * DAY);
    // 10y @ 400px (200 points) → raw 1576800s → ceil(18.25) → 19d.
    expect(stepForWidth(10 * 365 * DAY, 400)).toBe(19 * DAY);
  });

  it('up-rounding contract: result is a rung >= raw, and no smaller rung covers raw', () => {
    for (const rangeSec of [60, 300, HOUR, 6 * HOUR, DAY, 7 * DAY, 30 * DAY]) {
      for (const widthPx of [400, 600, 800, 1200, 1600, 2400]) {
        const step = stepForWidth(rangeSec, widthPx);
        const targetPoints = Math.min(720, Math.max(120, Math.floor(widthPx / 2)));
        const raw = rangeSec / targetPoints;
        expect(step).toBeGreaterThanOrEqual(raw);          // never over the point budget
        const idx = STEP_RUNGS.indexOf(step);
        if (idx > 0) expect(STEP_RUNGS[idx - 1]).toBeLessThan(raw); // FIRST covering rung
        if (idx === -1) expect(step % DAY).toBe(0);        // past the ladder → 1d multiple
      }
    }
  });

  it('degenerate window (<=0 / non-finite) → smallest rung, no throw', () => {
    expect(stepForWidth(0, 1200)).toBe(1);
    expect(stepForWidth(-60, 1200)).toBe(1);
    expect(stepForWidth(NaN, 1200)).toBe(1);
  });
});

describe('quantizeWidth — 200px buckets, [400, 2400] clamp', () => {
  const cases: Array<[px: number, want: number]> = [
    [1234, 1200],  // rounds to the nearest bucket…
    [1300, 1400],  // …ties round up (Math.round)
    [800, 800],    // bucket-exact widths are stable
    [250, 400],    // below the floor → 400
    [100, 400],
    [5000, 2400],  // above the ceiling → 2400
  ];
  it.each(cases)('quantizeWidth(%d) = %d', (px, want) => {
    expect(quantizeWidth(px)).toBe(want);
  });

  it('non-finite input → 1200 fallback (matches useContentWidth)', () => {
    expect(quantizeWidth(NaN)).toBe(1200);
    expect(quantizeWidth(Infinity)).toBe(1200);
  });

  it('every bucket is a multiple of 200 within the clamp', () => {
    for (let px = 0; px <= 3000; px += 37) {
      const w = quantizeWidth(px);
      expect(w % 200).toBe(0);
      expect(w).toBeGreaterThanOrEqual(400);
      expect(w).toBeLessThanOrEqual(2400);
    }
  });
});
