import { describe, it, expect } from 'vitest';
import { podDetailPath } from './podDetailPath';

// v0.9.152 — regression guard: a pod drill MUST forward the incoming ?range so
// a brushed/absolute incident window survives the drill into /pod (the review
// finding). Also pins minimal-link cleanliness (empty fields omitted).

const parse = (href: string) => {
  expect(href.startsWith('/pod?')).toBe(true);
  return new URLSearchParams(href.slice('/pod?'.length));
};

describe('podDetailPath', () => {
  it('forwards a brushed custom range (the review bug)', () => {
    const q = parse(podDetailPath({ pod: 'app-1', service: 'svc', range: 'custom:100-200' }));
    expect(q.get('range')).toBe('custom:100-200');
  });

  it('forwards a relative range preset', () => {
    const q = parse(podDetailPath({ pod: 'app-1', range: '6h' }));
    expect(q.get('range')).toBe('6h');
  });

  it('omits range when null or empty (nothing to forward)', () => {
    expect(parse(podDetailPath({ pod: 'app-1', range: null })).has('range')).toBe(false);
    expect(parse(podDetailPath({ pod: 'app-1', range: '' })).has('range')).toBe(false);
    expect(parse(podDetailPath({ pod: 'app-1' })).has('range')).toBe(false);
  });

  it('always carries the pod; omits other empty fields (minimal link)', () => {
    const q = parse(podDetailPath({ pod: 'app-1', service: '', cluster: '', deploy: '' }));
    expect(q.get('pod')).toBe('app-1');
    expect(q.has('service')).toBe(false);
    expect(q.has('cluster')).toBe(false);
    expect(q.has('deploy')).toBe(false);
  });

  it('emits full context + from marker when provided', () => {
    const q = parse(podDetailPath({
      cluster: 'ocpma', namespace: 'prod', pod: 'app-7d9f-x2',
      service: 'checkout', deploy: 'app', range: '1h', from: 'metrics',
    }));
    expect(q.get('cluster')).toBe('ocpma');
    expect(q.get('namespace')).toBe('prod');
    expect(q.get('pod')).toBe('app-7d9f-x2');
    expect(q.get('service')).toBe('checkout');
    expect(q.get('deploy')).toBe('app');
    expect(q.get('range')).toBe('1h');
    expect(q.get('from')).toBe('metrics');
  });

  it('percent-encodes special chars in values', () => {
    const q = parse(podDetailPath({ pod: 'app/weird pod', service: 'a&b' }));
    expect(q.get('pod')).toBe('app/weird pod');
    expect(q.get('service')).toBe('a&b');
  });
});
