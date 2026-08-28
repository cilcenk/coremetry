// v0.10.135 — DETAY SAYFALARI sözleşmeleri: bağlam korunur (range + at),
// aynı pod adı iki cluster'da iki farklı link, ölü kayıt 'gone'.
import { describe, it, expect } from 'vitest';
import { entityHref, entityLiveness } from './entityHref';
import { windowRangeParam } from './urlState';

const podA = { type: 'pod' as const, id: 'pod:c-a/pay/api-1', name: 'api-1', namespace: 'pay', clusterId: 'c-a' };
const podB = { ...podA, id: 'pod:c-b/pay/api-1', clusterId: 'c-b' };
const node = { type: 'node' as const, id: 'node:c-a/w1', name: 'w1', clusterId: 'c-a' };

function params(href: string): URLSearchParams {
  return new URLSearchParams(href.slice(href.indexOf('?') + 1));
}

describe('entityHref', () => {
  it('same pod name in two clusters → two different links carrying the cluster', () => {
    const a = entityHref(podA), b = entityHref(podB);
    expect(a).not.toBe(b);
    expect(a.startsWith('/pod?')).toBe(true);
    expect(params(a).get('cluster')).toBe('c-a');
    expect(params(b).get('cluster')).toBe('c-b');
    expect(params(a).get('namespace')).toBe('pay');
    expect(params(a).get('pod')).toBe('api-1');
  });
  it('preserves range and at across the pivot (pod + generic)', () => {
    const r = { fromNs: 1_700_000_000_000_000_000, toNs: 1_700_003_600_000_000_000 };
    const p = params(entityHref(podA, { range: r, at: 1_700_001_000_000, clusterName: 'prod-eu' }));
    expect(p.get('range')).toBe(windowRangeParam(r));
    expect(p.get('at')).toBe('1700001000000');
    expect(p.get('cluster')).toBe('prod-eu');
    const n = params(entityHref(node, { range: '6h', at: 5 }));
    expect(n.get('id')).toBe('node:c-a/w1');
    expect(n.get('range')).toBe('6h');
    expect(n.get('at')).toBe('5');
  });
  it('cluster → /clusters by name; others → /entity by id; no range param when absent', () => {
    expect(entityHref({ type: 'cluster', id: 'cluster:c-a', name: 'prod-eu', clusterId: 'c-a' })).toBe('/clusters?cluster=prod-eu');
    const h = entityHref(node);
    expect(h).toBe('/entity?id=node%3Ac-a%2Fw1');
  });
});

describe('entityLiveness', () => {
  it('closed lifetime → gone; open+stale → stale; open → live', () => {
    expect(entityLiveness({ validTo: '2026-08-28T10:00:00Z' })).toBe('gone');
    expect(entityLiveness({ validTo: '2026-08-28T10:00:00Z', stale: true })).toBe('gone');
    expect(entityLiveness({ stale: true })).toBe('stale');
    expect(entityLiveness({})).toBe('live');
  });
});
