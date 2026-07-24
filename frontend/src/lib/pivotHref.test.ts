import { describe, it, expect } from 'vitest';
import { tracesPivotHref } from './pivotHref';
import { decodeRange } from './urlState';

// v0.9.213 — the cross-signal pivot into /traces dropped its time window in
// four separate places. /traces then answered over the operator's sticky
// range and rendered an empty list, which reads as "no such traces" instead
// of "wrong window". These tests pin the one property that made the bug
// possible: the window is never optional and always survives the round trip.
const params = (href: string) => new URLSearchParams(href.slice(href.indexOf('?') + 1));

describe('tracesPivotHref', () => {
  it('always emits a range — preset window', () => {
    const p = params(tracesPivotHref({ window: { preset: '6h' }, service: 'checkout' }));
    expect(p.get('range')).toBe('6h');
    expect(p.get('service')).toBe('checkout');
  });

  it('always emits a range — absolute ns window', () => {
    const href = tracesPivotHref({
      window: { fromNs: 1_700_000_000_000_000_000, toNs: 1_700_000_900_000_000_000 },
      service: 'checkout',
    });
    const range = params(href).get('range');
    expect(range).toBe('custom:1700000000000-1700000900000');
    expect(decodeRange(range, { preset: '30m' })).toEqual({
      preset: 'custom', fromMs: 1_700_000_000_000, toMs: 1_700_000_900_000,
    });
  });

  it('never narrows the requested window when ns→ms rounds', () => {
    // from floors, to ceils: the encoded window must CONTAIN the request.
    const fromNs = 1_700_000_000_000_999_999;
    const toNs = 1_700_000_900_000_000_001;
    const r = decodeRange(params(tracesPivotHref({ window: { fromNs, toNs } })).get('range'), { preset: '30m' });
    expect(r.preset).toBe('custom');
    expect(r.fromMs! * 1e6).toBeLessThanOrEqual(fromNs);
    expect(r.toMs! * 1e6).toBeGreaterThanOrEqual(toNs);
  });

  it('defaults rootOnly to false — error spans and hops are mid-trace', () => {
    expect(params(tracesPivotHref({ window: { preset: '1h' } })).get('rootOnly')).toBe('false');
    expect(params(tracesPivotHref({ window: { preset: '1h' }, rootOnly: true })).get('rootOnly')).toBe('true');
  });

  it('multi-service co-occurrence wins over single service', () => {
    const p = params(tracesPivotHref({
      window: { preset: '1h' }, service: 'ignored', services: ['gateway', 'checkout'],
    }));
    expect(p.get('services')).toBe('gateway,checkout');
    expect(p.get('service')).toBeNull();
  });

  it('carries hasError, search, filters and view when asked', () => {
    const p = params(tracesPivotHref({
      window: { preset: '1h' }, service: 'db-svc',
      hasError: true, search: '/api/cart', filters: '[{"k":"db.statement","op":"LIKE","v":["SELECT"]}]',
      view: 'list',
    }));
    expect(p.get('hasError')).toBe('true');
    expect(p.get('search')).toBe('/api/cart');
    expect(p.get('filters')).toBe('[{"k":"db.statement","op":"LIKE","v":["SELECT"]}]');
    expect(p.get('view')).toBe('list');
  });

  it('omits absent optionals rather than emitting empty params', () => {
    const p = params(tracesPivotHref({ window: { preset: '1h' }, service: 'checkout' }));
    expect(p.get('hasError')).toBeNull();
    expect(p.get('search')).toBeNull();
    expect(p.get('filters')).toBeNull();
    expect(p.get('view')).toBeNull();
  });

  it('URL-encodes service names and search text', () => {
    const href = tracesPivotHref({
      window: { preset: '1h' }, service: 'checkout/v2 svc', search: 'GET /a b?c=1',
    });
    // Round-trips through URLSearchParams — no raw spaces or & in the href.
    expect(href).not.toMatch(/service=checkout\/v2 svc/);
    const p = params(href);
    expect(p.get('service')).toBe('checkout/v2 svc');
    expect(p.get('search')).toBe('GET /a b?c=1');
  });
});
