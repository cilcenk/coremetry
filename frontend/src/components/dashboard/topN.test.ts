import { describe, it, expect } from 'vitest';
import {
  topNStep, topNWindowSec, clampTopNLimit, topNRowValue, topNMoreLabel,
  topNRowFilters, TOPN_SERVER_CAP,
} from './topN';
import { applyVarsToTopN } from './PanelRenderer';

// v0.9.781 — the Top-N panel's honesty tests.
//
// The panel's whole correctness rests on ONE property: the request must
// collapse the window into a single ClickHouse bucket, because the server
// ranks series by area (Σ|value|) and a multi-bucket ratio aggregation makes
// that sum meaningless — two partial p99s summed is neither the window's p99
// nor a legitimate ranking weight.
//
// The naive "step = window seconds" does NOT give one bucket:
// toStartOfInterval is EPOCH-aligned, so a window that doesn't start on a
// step boundary straddles two bins. Verified against live ClickHouse before
// this code was written — every series came back with npoints=2 at
// step=window, and npoints=1 once the step was raised to an aligning value.

const NS = 1e9;

/** The invariant the panel depends on: both ends floor into the SAME bin. */
function singleBucket(fromNs: number, toNs: number, step: number): boolean {
  return Math.floor(fromNs / NS / step) === Math.floor(toNs / NS / step);
}

describe('topNStep — single-bucket guarantee', () => {
  // The exact window used for the live-ClickHouse probe (2026-08-08 00:22 UTC,
  // a 1h window that straddles a day boundary — the awkward case).
  const probeFrom = 1786144968 * NS;
  const probeTo = 1786148568 * NS;

  it('step=window is NOT enough — the naive choice straddles two buckets', () => {
    // This is the bug the panel would have shipped: 3600s buckets over a
    // window starting at :23 past the hour split into two partial bins.
    expect(singleBucket(probeFrom, probeTo, 3600)).toBe(false);
  });

  it('returns a step that puts the whole window in one bucket', () => {
    const step = topNStep(probeFrom, probeTo);
    expect(singleBucket(probeFrom, probeTo, step)).toBe(true);
  });

  it('holds for every preset window length, at an awkward offset', () => {
    // 5m … 30d, all starting mid-bucket.
    for (const windowSec of [300, 900, 3600, 6 * 3600, 24 * 3600, 7 * 24 * 3600, 30 * 24 * 3600]) {
      const from = 1786144968 * NS;
      const to = from + windowSec * NS;
      const step = topNStep(from, to);
      expect(singleBucket(from, to, step), `window=${windowSec} step=${step}`).toBe(true);
      expect(step).toBeGreaterThanOrEqual(windowSec);
    }
  });

  it('a boundary-aligned window still needs the raise — `to` lands on the NEXT bin', () => {
    // from exactly on an hour boundary, to exactly on the next one: floor(to)
    // is already the following bucket, so a naive step=window splits it.
    const from = 1786118400 * NS;      // multiple of 3600
    const to = from + 3600 * NS;
    expect(singleBucket(from, to, 3600)).toBe(false);
    expect(singleBucket(from, to, topNStep(from, to))).toBe(true);
  });

  it('stays on the 300s grid for ≥5m windows so the MV / rollup fast paths remain reachable', () => {
    // pickNarrowRollupTier + the MV fast-paths require step % baseSec == 0;
    // an off-grid step silently drops every Top-N panel onto raw `spans`.
    for (const windowSec of [300, 900, 3600, 24 * 3600]) {
      const from = 1786144968 * NS;
      const step = topNStep(from, from + windowSec * NS);
      expect(step % 300, `window=${windowSec} step=${step}`).toBe(0);
    }
  });

  it('sub-5m windows keep their exact length as the base (no 300s inflation)', () => {
    const from = 1786144800 * NS;      // multiple of 60
    const step = topNStep(from, from + 60 * NS);
    expect(step % 60).toBe(0);
    expect(singleBucket(from, from + 60 * NS, step)).toBe(true);
  });

  it('terminates on a degenerate (zero-length) window', () => {
    const from = 1786144968 * NS;
    expect(topNStep(from, from)).toBeGreaterThan(0);
  });
});

describe('topNWindowSec', () => {
  it('converts the ns window to seconds', () => {
    expect(topNWindowSec(0, 3600 * NS)).toBe(3600);
  });
  it('never returns 0 (it is a divisor)', () => {
    expect(topNWindowSec(5, 5)).toBe(1);
  });
});

describe('clampTopNLimit', () => {
  it('never exceeds the server-side 50-series trim', () => {
    // Asking for 100 would render a "top 100" that is really a top 50 with a
    // silently missing tail.
    expect(clampTopNLimit(100)).toBe(TOPN_SERVER_CAP);
    expect(clampTopNLimit(51)).toBe(TOPN_SERVER_CAP);
  });
  it('passes sane values through', () => {
    expect(clampTopNLimit(5)).toBe(5);
    expect(clampTopNLimit(20)).toBe(20);
  });
  it('defaults an absent / nonsense limit to 10', () => {
    expect(clampTopNLimit(undefined)).toBe(10);
    expect(clampTopNLimit(0)).toBe(10);
    expect(clampTopNLimit(-3)).toBe(10);
    expect(clampTopNLimit(NaN)).toBe(10);
  });
  it('floors a fractional limit', () => {
    expect(clampTopNLimit(10.7)).toBe(10);
  });
});

describe('topNRowValue', () => {
  const pt = (value: number) => ({ time: 0, value });

  it('single bucket = the exact window aggregate, passed through', () => {
    expect(topNRowValue([pt(2104.2)], 'p99', 57600, 3600)).toBeCloseTo(2104.2);
    expect(topNRowValue([pt(2259)], 'count', 57600, 3600)).toBe(2259);
    expect(topNRowValue([pt(12.025)], 'error_rate', 57600, 3600)).toBeCloseTo(12.025);
  });

  it('rate is renormalised from per-step to per-window', () => {
    // The backend computes rate as count/step (rollup_fastpath rateDiv). With
    // a step raised to 48× the window, the raw number reads 48× too small —
    // live probe: api-gateway showed 0.01307 rps for 2259 calls in an hour,
    // whose true rate is 0.6275.
    const v = topNRowValue([pt(0.013072)], 'rate', 172800, 3600);
    expect(v).toBeCloseTo(0.6275, 3);
  });

  it('empty series has no value', () => {
    expect(topNRowValue([], 'count', 3600, 3600)).toBeNull();
  });

  it('defensive: additive aggs still reduce EXACTLY across buckets', () => {
    // Cannot happen while topNStep feeds the request, but each bucket only
    // holds in-window spans, so summing counts stays exact.
    expect(topNRowValue([pt(1099), pt(1114)], 'count', 3600, 3600)).toBe(2213);
    expect(topNRowValue([pt(2), pt(3)], 'errors', 3600, 3600)).toBe(5);
    expect(topNRowValue([pt(1.5), pt(2.5)], 'sum', 3600, 3600)).toBe(4);
  });

  it('defensive: ratio aggs return null rather than a confident wrong number', () => {
    // Summing or averaging two partial p99s is the silent lie this panel
    // exists to avoid — "—" is the honest render.
    expect(topNRowValue([pt(16914.8), pt(8172.8)], 'p99', 3600, 3600)).toBeNull();
    expect(topNRowValue([pt(5), pt(9)], 'error_rate', 3600, 3600)).toBeNull();
    expect(topNRowValue([pt(5), pt(9)], 'avg', 3600, 3600)).toBeNull();
    expect(topNRowValue([pt(5), pt(9)], 'max', 3600, 3600)).toBeNull();
  });

  it('agg matching is case-insensitive', () => {
    expect(topNRowValue([pt(1), pt(2)], 'COUNT', 3600, 3600)).toBe(3);
  });
});

describe('topNMoreLabel', () => {
  it('counts what the server trimmed away, not just what we sliced', () => {
    // 100 groups exist, the server shipped 50, the panel renders 10.
    expect(topNMoreLabel(10, 50, 100)).toBe('+90 more');
  });
  it('falls back to the received count when no trim happened', () => {
    expect(topNMoreLabel(10, 42, undefined)).toBe('+32 more');
  });
  it('returns null when nothing is hidden — no "+0 more" that reads as a bug', () => {
    expect(topNMoreLabel(10, 10, undefined)).toBeNull();
    expect(topNMoreLabel(50, 50, 50)).toBeNull();
  });
});

describe('topNRowFilters', () => {
  it('pairs each group-by key with this row s value', () => {
    expect(topNRowFilters('service.name, http.route', ['checkout', '/api/orders']))
      .toEqual([
        { k: 'service.name', op: '=', v: ['checkout'] },
        { k: 'http.route', op: '=', v: ['/api/orders'] },
      ]);
  });
  it('tolerates a shorter group key than key list', () => {
    expect(topNRowFilters('service.name, http.route', ['checkout']))
      .toEqual([{ k: 'service.name', op: '=', v: ['checkout'] }]);
  });
  it('no group-by = no filters (the pivot degrades to a plain window link)', () => {
    expect(topNRowFilters('', ['x'])).toEqual([]);
    expect(topNRowFilters(undefined, ['x'])).toEqual([]);
  });
});

describe('applyVarsToTopN', () => {
  const cfg = (over: Partial<Parameters<typeof applyVarsToTopN>[0]> = {}) =>
    ({ agg: 'p99', groupBy: 'service.name', ...over }) as Parameters<typeof applyVarsToTopN>[0];

  it('expands variables in dsl / groupBy / filters', () => {
    const r = applyVarsToTopN(
      cfg({ dsl: 'service.name = "${service}"', groupBy: '${dim}', filters: '[{"k":"env","op":"=","v":["${env}"]}]' }),
      { service: 'checkout', dim: 'http.route', env: 'prod' },
    );
    expect(r.dsl).toBe('service.name = "checkout"');
    expect(r.groupBy).toBe('http.route');
    expect(r.filters).toContain('prod');
  });

  it('an empty variable DROPS its DSL line instead of matching the empty string', () => {
    // service.name = "" would match nothing; "no service picked" must mean
    // "no service filter".
    const r = applyVarsToTopN(
      cfg({ dsl: 'duration > 100ms\nservice.name = "${service}"' }),
      { service: '' },
    );
    expect(r.dsl).toBe('duration > 100ms');
  });

  it('is a no-op without vars', () => {
    const c = cfg({ dsl: 'x = "${y}"' });
    expect(applyVarsToTopN(c, undefined)).toBe(c);
  });

  it('never turns groupBy into undefined (it is a required field)', () => {
    const r = applyVarsToTopN(cfg({ groupBy: '${dim}' }), { dim: '' });
    expect(typeof r.groupBy).toBe('string');
  });
});
