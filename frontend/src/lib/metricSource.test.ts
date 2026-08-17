// metricSource.test.ts — v0.9.1151, VictoriaMetrics deneme modu.
//
// `?metricsrc=vm|ch` pins ONE page's metric reads to one backend without
// touching the install-wide Settings toggle. Three things are pinned here,
// and each of them fails silently rather than loudly:
//
//   1. PARSE — what counts as an override. A too-loose parser would send
//      garbage to the server (400 on a page that used to work); a too-tight
//      one would drop a valid param and read the DEFAULT backend while the
//      operator believed otherwise. The second is the dangerous direction:
//      nothing on screen contradicts it.
//   2. STAMP — the param actually lands on the URL, exactly once, whatever
//      shape the caller's query string had.
//   3. CONTRACT — every metric method in lib/api.ts is stamped. A page that
//      forwards the param on its chart fetch but not its picker fetch would
//      autocomplete names from ClickHouse and query them against
//      VictoriaMetrics: empty series, and the operator concludes VM has no
//      data.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import {
  METRIC_SOURCE_LABELS,
  METRIC_SOURCE_PARAM,
  currentMetricSource,
  parseMetricSource,
  withMetricSource,
} from './metricSource';

describe('parseMetricSource (v0.9.1151)', () => {
  it('reads both accepted values, with or without a leading ?', () => {
    expect(parseMetricSource('?metricsrc=vm')).toBe('vm');
    expect(parseMetricSource('metricsrc=vm')).toBe('vm');
    expect(parseMetricSource('?metricsrc=ch')).toBe('ch');
    // Real page URLs carry a dozen other params; position must not matter.
    expect(parseMetricSource('?range=30m&metricsrc=vm&service=cart')).toBe('vm');
    expect(parseMetricSource('?range=30m&service=cart&metricsrc=ch')).toBe('ch');
  });

  it('absent / blank is "no opinion", not an error', () => {
    // These must all fall through to the server's Settings default. A
    // parser that threw here would break every page that has no param —
    // i.e. all of them.
    expect(parseMetricSource('')).toBeUndefined();
    expect(parseMetricSource('?')).toBeUndefined();
    expect(parseMetricSource('?range=30m')).toBeUndefined();
    expect(parseMetricSource('?metricsrc=')).toBeUndefined();
    // Whitespace-only matches the server's strings.TrimSpace, so a
    // hand-typed URL with a stray space does not become a 400.
    expect(parseMetricSource('?metricsrc=%20%20')).toBeUndefined();
    // Surrounding whitespace on a REAL value is trimmed, not rejected.
    expect(parseMetricSource('?metricsrc=%20vm%20')).toBe('vm');
  });

  it('an unknown value is dropped client-side, so the SERVER answers', () => {
    // Deliberate: the backend owns the accepted set and answers 400 with a
    // message naming it. Rejecting here as well would mean two error
    // surfaces to keep in sync — and this one is the silent kind.
    expect(parseMetricSource('?metricsrc=VM')).toBeUndefined();
    expect(parseMetricSource('?metricsrc=victoria')).toBeUndefined();
    expect(parseMetricSource('?metricsrc=clickhouse')).toBeUndefined();
    expect(parseMetricSource('?metricsrc=vm,ch')).toBeUndefined();
  });
});

describe('withMetricSource (v0.9.1151)', () => {
  it('is a no-op with no override — the default path must be byte-identical', () => {
    // The regression pin. Every metric call on every page goes through
    // this function; if it altered URLs without a param, the whole product
    // would fetch different keys than it did before v0.9.1151.
    const urls = [
      '/api/metrics/names',
      '/api/metrics/names?service=cart&q=jvm',
      '/api/dashboards/data',
    ];
    for (const u of urls) expect(withMetricSource(u, undefined)).toBe(u);
  });

  it('appends with the right separator for both URL shapes', () => {
    expect(withMetricSource('/api/dashboards/data', 'vm'))
      .toBe('/api/dashboards/data?metricsrc=vm');
    expect(withMetricSource('/api/metrics/names?service=cart', 'vm'))
      .toBe('/api/metrics/names?service=cart&metricsrc=vm');
    expect(withMetricSource('/api/metrics/labels?metric=m&key=pod', 'ch'))
      .toBe('/api/metrics/labels?metric=m&key=pod&metricsrc=ch');
    // A trailing `?` (qs() can produce an empty query) must not yield `??`
    // or a stray `&` with nothing before it.
    expect(withMetricSource('/api/metrics/names?', 'vm'))
      .toBe('/api/metrics/names?&metricsrc=vm');
  });

  it('is idempotent — never stamps a second, conflicting value', () => {
    // Go's Query().Get() silently takes the FIRST of a repeated param, so
    // a double stamp would not error, it would pick one at random from the
    // reader's point of view.
    const once = withMetricSource('/api/metrics/names?service=cart', 'vm');
    expect(withMetricSource(once, 'vm')).toBe(once);
    // An explicit param already on the URL wins over the page's.
    expect(withMetricSource('/api/metrics/names?metricsrc=ch', 'vm'))
      .toBe('/api/metrics/names?metricsrc=ch');
  });

  it('the param name matches the server contract', () => {
    // Operators type this by hand and paste it into runbooks. A rename
    // turns every existing trial URL into a default-backend read, with no
    // error anywhere. Mirrors internal/api's TestParamNameIsStable.
    expect(METRIC_SOURCE_PARAM).toBe('metricsrc');
    expect(METRIC_SOURCE_LABELS).toEqual({ vm: 'VictoriaMetrics', ch: 'ClickHouse' });
  });
});

describe('currentMetricSource (v0.9.1151)', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('reads the live page URL', () => {
    vi.stubGlobal('window', { location: { search: '?range=30m&metricsrc=vm' } });
    expect(currentMetricSource()).toBe('vm');
  });

  it('re-reads on every call, so a navigation cannot leave VM pinned', () => {
    // Read at CALL time rather than module load. A cached value would go
    // stale in the DANGEROUS direction: still routing at VictoriaMetrics on
    // a page whose URL no longer says so.
    const win = { location: { search: '?metricsrc=vm' } };
    vi.stubGlobal('window', win);
    expect(currentMetricSource()).toBe('vm');
    win.location.search = '?range=6h';
    expect(currentMetricSource()).toBeUndefined();
  });

  it('survives a windowless environment', () => {
    // api.ts is imported by node-env tests and would otherwise throw on
    // module use.
    vi.stubGlobal('window', undefined);
    expect(currentMetricSource()).toBeUndefined();
  });
});

// ── The contract: every metric method in api.ts is stamped ──────────────────
//
// Scanned from SOURCE rather than exercised one method at a time, because
// the failure this guards is a method ADDED later without the wrapper. A
// per-method test only covers the methods someone remembered to list; the
// scan covers the ones they did not.
describe('lib/api.ts stamps every seam endpoint (v0.9.1151)', () => {
  const src = readFileSync(resolve(__dirname, 'api.ts'), 'utf8');

  // Every metric endpoint behind the backend source seam
  // (internal/api/metricsource.go), plus the dashboards bundle whose
  // "metric" branch reads through the same seam.
  //
  // v0.9.1157 — histogram + promql JOINED the list (VM Faz 2). They were
  // in the negative list below for three releases and the inverse
  // assertion held them there on purpose; widening the seam is what makes
  // stamping them correct rather than decorative.
  //
  // Still deliberately NOT listed: /api/metrics (raw points) and
  // /api/metrics/resolve (the doorway resolver reads ClickHouse directly,
  // outside the seam).
  const seamPaths = [
    '/api/metrics/names',
    '/api/metrics/query',
    '/api/metrics/labels',
    '/api/metrics/attr-keys',
    '/api/metrics/histogram',
    '/api/metrics/promql',
    '/api/dashboards/data',
  ];

  // Every template literal in api.ts whose path is one of the above.
  const literals = [...src.matchAll(/`(\/api\/[a-z/-]+)([^`]*)`/g)];

  it('finds the call sites at all — guards a vacuous pass', () => {
    // If the regex or the file layout changes, the assertions below would
    // scan an empty set and report success (the v0.9.982 lesson: a gate
    // that stopped biting kept passing).
    for (const p of seamPaths) {
      const hits = literals.filter(m => m[1] === p);
      expect(hits.length, `no template literal found for ${p}`).toBeGreaterThan(0);
    }
  });

  it('each seam call site is wrapped in withMetricSource', () => {
    const unwrapped: string[] = [];
    for (const m of literals) {
      if (!seamPaths.includes(m[1])) continue;
      // The wrapper is the immediately-enclosing call, so it sits within a
      // short window before the backtick. Wide enough for `get<Type>(` and
      // a cast, narrow enough that an unrelated earlier call cannot satisfy
      // it.
      const before = src.slice(Math.max(0, m.index! - 60), m.index!);
      if (!before.includes('withMetricSource(')) {
        unwrapped.push(`${m[1]}${m[2].slice(0, 40)}`);
      }
    }
    expect(unwrapped, 'these metric endpoints ignore ?metricsrc= — the page would read the ' +
      'default backend while the operator believed it was reading the other one').toEqual([]);
  });

  it('the non-seam endpoints are deliberately NOT stamped', () => {
    // The inverse assertion, so "stamp everything metric-shaped" cannot
    // creep in unreviewed. A stamped endpoint whose handler does not read
    // the source seam sends a param the server ignores, and the operator
    // reads the chart as VictoriaMetrics data when it came from ClickHouse
    // — the exact confusion the trial badge exists to prevent.
    //
    // v0.9.1157 — histogram + promql LEFT this list when the seam widened
    // to cover them. /api/metrics/resolve stays: the doorway resolver
    // reads ClickHouse rollup tiers directly and has no VM translation, so
    // stamping it would claim coverage that does not exist.
    for (const p of ['/api/metrics/resolve']) {
      for (const m of literals.filter(x => x[1] === p)) {
        const before = src.slice(Math.max(0, m.index! - 60), m.index!);
        expect(before.includes('withMetricSource('),
          `${p} is stamped, but its handler does not read the source seam — ` +
          'either widen the seam (backend) or drop the stamp').toBe(false);
      }
    }
  });

  it('finds the non-seam call sites too — the inverse must not be vacuous', () => {
    // Without this, deleting api.ts's /api/metrics/resolve call (or a regex
    // drift) would make the assertion above iterate an empty set and pass
    // while proving nothing — the v0.9.982 lesson, applied to the negative
    // half of the same gate.
    expect(literals.filter(m => m[1] === '/api/metrics/resolve').length,
      'no /api/metrics/resolve literal found — the inverse assertion scans nothing')
      .toBeGreaterThan(0);
  });
});
