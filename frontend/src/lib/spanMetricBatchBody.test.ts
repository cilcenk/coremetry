// v0.10.524 — operatör (prod): "2.4M span diyor ama 5 trace"; DevTools:
// metric-batch gövdesinde `search` yok. api.spanMetricBatch gövdeyi alan
// alan kuruyordu ve search / rootOnly / hasError hiç yazılmıyordu — tip
// kabul ediyor, tel'e gitmiyordu (v0.9.601 ve v0.10.484 "tested but
// unreachable"). Bu test GERÇEK gövdeyi (fetch stub) doğrular; kaynak pini
// değil.
import { describe, it, expect, vi, afterEach } from 'vitest';
import { api } from './api';

afterEach(() => { vi.unstubAllGlobals(); });

describe('api.spanMetricBatch gövdesi', () => {
  it('search / rootOnly / hasError tel\'e gider; filters JSON olarak açılır', async () => {
    const calls: { url: string; body: unknown }[] = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, body: JSON.parse(String(init?.body)) });
      return new Response(JSON.stringify({ stepSeconds: 60, series: {} }), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }));
    await api.spanMetricBatch({
      from: 1, to: 2, step: 60,
      search: '/BSAWEB/x/execute', rootOnly: true, hasError: true,
      filters: JSON.stringify([{ k: 'kind', op: 'IN', v: ['server'] }]),
      dsl: 'service.name = "svc"',
      aggs: [{ name: 'count', agg: 'count' }],
    });
    expect(calls.length).toBe(1);
    expect(calls[0].url).toContain('/api/spans/metric-batch');
    const b = calls[0].body as Record<string, unknown>;
    expect(b.search).toBe('/BSAWEB/x/execute');
    expect(b.rootOnly).toBe(true);
    expect(b.hasError).toBe(true);
    expect(b.filters).toEqual([{ k: 'kind', op: 'IN', v: ['server'] }]);
    expect(b.dsl).toBe('service.name = "svc"');
    expect(b.aggs).toEqual([{ name: 'count', agg: 'count' }]);
  });
  it('bayraklar verilmeyince gövdede yer almaz (undefined düşer)', async () => {
    const calls: unknown[] = [];
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      calls.push(JSON.parse(String(init?.body)));
      return new Response(JSON.stringify({ stepSeconds: 60, series: {} }), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }));
    await api.spanMetricBatch({ from: 1, to: 2, step: 60, aggs: [] });
    const b = calls[0] as Record<string, unknown>;
    expect('search' in b).toBe(false);
    expect('rootOnly' in b).toBe(false);
    expect('hasError' in b).toBe(false);
  });
});
