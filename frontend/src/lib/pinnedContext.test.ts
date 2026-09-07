import { describe, it, expect } from 'vitest';
import { hasPinnableContext, pinLabelTR, legacyFromPinned } from './pinnedContext';
import type { PageContext } from './types';

// v0.10.540 — pin yardımcıları: sabitlenebilirlik, çip etiketi, eski alan türetimi.
describe('pinnedContext', () => {
  it('bağlamsız sayfa / boş bağlam sabitlenemez', () => {
    expect(hasPinnableContext(null)).toBe(false);
    expect(hasPinnableContext({ page: 'settings', path: '/settings', service: 'x' })).toBe(false);
    expect(hasPinnableContext({ page: 'traces', path: '/traces' })).toBe(false);
    expect(hasPinnableContext({ page: 'traces', path: '/traces', cluster: 'c1' })).toBe(true);
    expect(hasPinnableContext({ page: 'problems', path: '/problems', problemId: 'p1' })).toBe(true);
  });
  it('etiket boş boyutu yazmaz, sırası sabit', () => {
    const ctx: PageContext = { page: 'traces', path: '/traces', service: 'api', cluster: 'c1', namespace: 'pay', timeRange: { preset: '6h' }, activeFilters: [{ k: 'status', op: '=', v: ['error'] }] };
    expect(pinLabelTR(ctx)).toBe('traces · api · c1/pay · 6h · 1 filtre');
    expect(pinLabelTR({ page: 'problems', path: '/problems', problemId: 'p1' })).toBe('problems · problem p1');
    expect(pinLabelTR({ page: 'trace', path: '/trace', traceId: 'abcdef0123456789', timeRange: { preset: 'custom', fromMs: 1, toMs: 2 } })).toBe('trace · trace abcdef01… · özel aralık');
  });
  it('eski düz alanlar pinden: göreli preset süreye, custom bitiş anına', () => {
    expect(legacyFromPinned({ page: 'service', path: '/service', service: 'api', operation: 'GET /x', env: 'prod', timeRange: { preset: '1h' } }))
      .toEqual({ service: 'api', operation: 'GET /x', env: 'prod', rangeS: 3600 });
    expect(legacyFromPinned({ page: 'trace', path: '/trace', traceId: 't1', timeRange: { preset: 'custom', fromMs: 1_000_000, toMs: 4_600_000 } }))
      .toEqual({ trace: 't1', rangeS: 3600, toMs: 4_600_000 });
    expect(legacyFromPinned({ page: 'traces', path: '/traces', cluster: 'c1' })).toEqual({});
  });
});
