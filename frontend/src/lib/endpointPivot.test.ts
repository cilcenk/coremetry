import { describe, it, expect } from 'vitest';
import { encodeRange, encodeFilters, buildQuery } from '@/lib/urlState';
import type { TimeRange } from '@/lib/types';

// v0.9.307 (brief N6b) — a pivot must carry the SCOPE it was launched
// from.
//
// v0.9.306 fixed this inside the /endpoints drawer: with env=uat the
// table showed uat numbers while the drawer aggregated every env. The
// links out of the page had the same defect one step further along — a
// row read under env=uat opened an UNFILTERED trace list, so the pivot
// silently widened the question it came from. Same class, different
// exit.
//
// These mirror the two builders in Endpoints.tsx. They exist because
// the failure is invisible: the link works, the page loads, the numbers
// are simply about a different population than the row that was clicked.

const range: TimeRange = { preset: '1h' } as TimeRange;

function tracesLink(service: string, path: string, env?: string, cluster?: string): string {
  return `/traces?service=${encodeURIComponent(service)}` +
    `&search=${encodeURIComponent(path)}` +
    `&range=${encodeURIComponent(encodeRange(range))}` +
    (env ? `&env=${encodeURIComponent(env)}` : '') +
    (cluster ? `&cluster=${encodeURIComponent(cluster)}` : '') +
    `&view=list&rootOnly=false`;
}

function exploreLink(service: string, path: string, agg: string, env?: string, cluster?: string): string {
  const filters = encodeFilters([
    { k: 'service.name', op: '=', v: [service] },
    { k: 'http.route', op: '=', v: [path] },
  ]);
  return `/explore?${buildQuery([
    ['range', encodeRange(range)],
    ['filters', filters],
    ['agg', agg],
    ['field', 'duration_ms'],
    ['result', 'metric'],
    ['env', env ?? ''],
    ['cluster', cluster ?? ''],
  ])}`;
}

describe('endpoints → traces pivot', () => {
  it('carries the env the row was read under', () => {
    expect(tracesLink('checkout', '/api/orders', 'uat')).toContain('env=uat');
  });

  it('carries the cluster', () => {
    expect(tracesLink('checkout', '/api/orders', '', 'eu-west')).toContain('cluster=eu-west');
  });

  it('omits an unset scope rather than sending an empty filter', () => {
    // `env=` explicitly means "all envs" to useUrlEnv, which is NOT the
    // same as "inherit" — writing it blank would pin the target page to
    // all-envs even when the operator had picked one upstream.
    const link = tracesLink('checkout', '/api/orders');
    expect(link).not.toContain('env=');
    expect(link).not.toContain('cluster=');
  });

  it('encodes a route containing slashes and braces', () => {
    const link = tracesLink('checkout', '/api/users/{id}/orders', 'uat');
    expect(link).toContain(encodeURIComponent('/api/users/{id}/orders'));
  });
});

describe('endpoints → explore pivot', () => {
  it('filters on service.name AND http.route, not just the service', () => {
    // Dropping the route would open the WHOLE service's latency — a
    // plausible chart about the wrong thing.
    const link = exploreLink('checkout', '/api/orders', 'p99');
    const filters = decodeURIComponent(new URL(link, 'http://x').searchParams.get('filters') ?? '');
    expect(filters).toContain('service.name');
    expect(filters).toContain('http.route');
    expect(filters).toContain('/api/orders');
  });

  it('carries the scope', () => {
    expect(exploreLink('checkout', '/api/orders', 'p99', 'uat')).toContain('env=uat');
  });

  it('drops empty scope params so the URL stays clean', () => {
    const link = exploreLink('checkout', '/api/orders', 'p99');
    expect(link).not.toContain('env=');
    expect(link).not.toContain('cluster=');
  });

  it('requests the metric result mode Explore decodes', () => {
    const p = new URL(exploreLink('checkout', '/api/orders', 'p99'), 'http://x').searchParams;
    expect(p.get('result')).toBe('metric');
    expect(p.get('field')).toBe('duration_ms');
    expect(p.get('agg')).toBe('p99');
  });
});
