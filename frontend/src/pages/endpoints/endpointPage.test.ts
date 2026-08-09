import { describe, it, expect } from 'vitest';
import {
  endpointDetailHref, legacyEndpointTarget, parseEndpointPageRef,
  endpointSearchHint, encodeEndpointParam,
} from './endpointParam';

// endpointPage.test.ts — v0.9.839. Pins the /endpoint page's URL
// contract now that the drawer and the sparkline modal are retired and
// a row click NAVIGATES.
//
// Three things are load-bearing and each has a way of failing silently:
//
//  1. the href must carry the SCOPE (range/env/cluster/compare/entry).
//     A pivot that drops env answers a wider question than the one it
//     was launched from — the v0.9.306 class, which cost a release to
//     find because both screens looked plausible.
//  2. the legacy `?endpoint=` / `?detail=` deep links must resolve.
//     saved_views rows and pasted postmortem links still carry them.
//  3. endpointSearchHint must never produce a filter that EXCLUDES the
//     row it is narrowing toward. In signature mode the collapsed path
//     (/orders/:id) is a substring of no raw path, so passing it whole
//     would make a live endpoint look like it had no data.

describe('endpointDetailHref', () => {
  it('carries identity and omits an empty scope', () => {
    const href = endpointDetailHref({ service: 'checkout', path: '/orders', sig: false });
    const p = new URLSearchParams(href.split('?')[1]);
    expect(href.startsWith('/endpoint?')).toBe(true);
    expect(p.get('service')).toBe('checkout');
    expect(p.get('path')).toBe('/orders');
    // Absent, not empty: an explicitly empty `env=` reads as "all
    // environments" elsewhere in the app and would WIDEN the scope
    // while looking like it narrowed it.
    expect(p.has('env')).toBe(false);
    expect(p.has('cluster')).toBe(false);
    expect(p.has('sig')).toBe(false);
    expect(p.has('compare')).toBe(false);
    expect(p.has('entry')).toBe(false);
  });

  it('carries every scope field when present', () => {
    const href = endpointDetailHref(
      { service: 'checkout', path: '/orders/:id', sig: true },
      { range: '30m', env: 'uat', cluster: 'eu-1', compare: true, entry: 'rpc' },
    );
    const p = new URLSearchParams(href.split('?')[1]);
    expect(p.get('sig')).toBe('1');
    expect(p.get('range')).toBe('30m');
    expect(p.get('env')).toBe('uat');
    expect(p.get('cluster')).toBe('eu-1');
    expect(p.get('compare')).toBe('1');
    expect(p.get('entry')).toBe('rpc');
  });

  it('survives hostile path characters through a full round-trip', () => {
    const ref = { service: 'a service', path: '/x?y=1&z=2#frag', sig: false };
    const href = endpointDetailHref(ref);
    expect(parseEndpointPageRef(href.split('?')[1])).toEqual(ref);
  });
});

describe('legacyEndpointTarget', () => {
  it('redirects the drawer param', () => {
    const raw = encodeEndpointParam({ service: 'checkout', path: '/orders', sig: false });
    const t = legacyEndpointTarget(`endpoint=${encodeURIComponent(raw)}&range=1h`);
    expect(t).not.toBeNull();
    const p = new URLSearchParams(t!.split('?')[1]);
    expect(p.get('service')).toBe('checkout');
    expect(p.get('path')).toBe('/orders');
    expect(p.get('range')).toBe('1h');
  });

  it('redirects the modal param, keeping signature mode', () => {
    const raw = encodeEndpointParam({ service: 'checkout', path: '/orders/:id', sig: true });
    const t = legacyEndpointTarget(`detail=${encodeURIComponent(raw)}&env=uat&cluster=eu-1&compare=1`);
    const p = new URLSearchParams(t!.split('?')[1]);
    expect(p.get('sig')).toBe('1');
    expect(p.get('env')).toBe('uat');
    expect(p.get('cluster')).toBe('eu-1');
    expect(p.get('compare')).toBe('1');
  });

  it('prefers ?endpoint= when a stale URL somehow carries both', () => {
    const a = encodeEndpointParam({ service: 'a', path: '/a', sig: false });
    const b = encodeEndpointParam({ service: 'b', path: '/b', sig: false });
    const t = legacyEndpointTarget(
      `endpoint=${encodeURIComponent(a)}&detail=${encodeURIComponent(b)}`);
    expect(new URLSearchParams(t!.split('?')[1]).get('service')).toBe('a');
  });

  it('returns null when there is nothing to redirect', () => {
    expect(legacyEndpointTarget('')).toBeNull();
    expect(legacyEndpointTarget('service=checkout&range=1h')).toBeNull();
  });

  it('returns null on a malformed codec instead of navigating to junk', () => {
    expect(legacyEndpointTarget('endpoint=onlyonefield')).toBeNull();
    expect(legacyEndpointTarget('detail=%E0%A4%A')).toBeNull();
  });
});

describe('parseEndpointPageRef', () => {
  it('needs BOTH identity fields', () => {
    expect(parseEndpointPageRef('service=checkout')).toBeNull();
    expect(parseEndpointPageRef('path=/orders')).toBeNull();
    expect(parseEndpointPageRef('')).toBeNull();
  });

  it('reads sig only from the exact flag', () => {
    expect(parseEndpointPageRef('service=a&path=/b&sig=1')?.sig).toBe(true);
    expect(parseEndpointPageRef('service=a&path=/b&sig=true')?.sig).toBe(false);
    expect(parseEndpointPageRef('service=a&path=/b')?.sig).toBe(false);
  });
});

describe('endpointSearchHint', () => {
  const cases: Array<[string, string, boolean, string]> = [
    ['raw path narrows exactly',        '/orders/8421',      false, '/orders/8421'],
    ['shape keeps the literal prefix',  '/orders/:id',       true,  '/orders/'],
    ['shape stops at the FIRST hole',   '/a/:id/b/:sub',     true,  '/a/'],
    ['shape with no hole is literal',   '/health',           true,  '/health'],
    ['raw path with a colon segment',   '/orders/:id',       false, '/orders/:id'],
    ['root shape yields the root',      '/:id',              true,  '/'],
  ];
  for (const [name, path, sig, want] of cases) {
    it(name, () => {
      expect(endpointSearchHint(path, sig)).toBe(want);
    });
  }

  it('never returns something the matching raw paths lack', () => {
    // The whole point: the hint must be a SUBSTRING of every raw path
    // the shape covers, or the narrowing hides the row it is looking for.
    const hint = endpointSearchHint('/orders/:id/items', true);
    for (const raw of ['/orders/1/items', '/orders/abc-99/items', '/orders/x/items']) {
      expect(raw.includes(hint)).toBe(true);
    }
  });
});
