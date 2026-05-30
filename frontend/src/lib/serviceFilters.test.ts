import { describe, expect, it } from 'vitest';
import { passesLocalDisplayFilters, type LocalDisplayFilters } from './serviceFilters';

// v0.7.29 — Operator-reported: Services page showed "no services" while typing
// and only the Search button brought results. Root cause was a client-side
// name filter over the loaded 50-row page. The fix moves name/team filtering
// fully server-side; this helper keeps only the numeric/errors refinements.
// The headline regression assertion: it must NOT filter by service name.

type Row = Parameters<typeof passesLocalDisplayFilters>[0];
const svc = (over: Partial<Row>): Row => ({
  errorCount: 0, errorRate: 0, spanCount: 1000, p99DurationMs: 50, ...over,
});
const NONE: LocalDisplayFilters = { errorsOnly: false, minSpans: NaN, minP99: NaN };

describe('passesLocalDisplayFilters', () => {
  it('passes everything when no refinement is set', () => {
    expect(passesLocalDisplayFilters(svc({}), NONE)).toBe(true);
  });

  it('errorsOnly drops zero-error services, keeps erroring ones', () => {
    expect(passesLocalDisplayFilters(svc({ errorCount: 0, errorRate: 0 }), { ...NONE, errorsOnly: true })).toBe(false);
    expect(passesLocalDisplayFilters(svc({ errorCount: 3 }), { ...NONE, errorsOnly: true })).toBe(true);
    expect(passesLocalDisplayFilters(svc({ errorRate: 0.1 }), { ...NONE, errorsOnly: true })).toBe(true);
  });

  it('minSpans drops services below the threshold (NaN = no filter)', () => {
    expect(passesLocalDisplayFilters(svc({ spanCount: 100 }), { ...NONE, minSpans: 500 })).toBe(false);
    expect(passesLocalDisplayFilters(svc({ spanCount: 900 }), { ...NONE, minSpans: 500 })).toBe(true);
    expect(passesLocalDisplayFilters(svc({ spanCount: 1 }), NONE)).toBe(true); // NaN threshold ignored
  });

  it('minP99 drops services below the threshold', () => {
    expect(passesLocalDisplayFilters(svc({ p99DurationMs: 20 }), { ...NONE, minP99: 100 })).toBe(false);
    expect(passesLocalDisplayFilters(svc({ p99DurationMs: 250 }), { ...NONE, minP99: 100 })).toBe(true);
  });

  it('regression: does NOT filter by service name (name is server-side now)', () => {
    // The Pick type omits `name` entirely, so a name filter here wouldn't even
    // compile — this test documents the intent: two services that pass the same
    // numeric filters are both kept regardless of what they'd be named.
    expect(passesLocalDisplayFilters(svc({ spanCount: 1000 }), { ...NONE, minSpans: 500 })).toBe(true);
    expect(passesLocalDisplayFilters(svc({ spanCount: 1000 }), { ...NONE, minSpans: 500 })).toBe(true);
  });
});
