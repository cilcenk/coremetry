// @vitest-environment jsdom
// traceWaterfallAnalysis.test.tsx — v0.10.276 (Dilim 1c): sunucu analizi
// verilince bar içinde öz-süre şeridi ve katlı satırda alt ağaç özeti; analysis
// yokken ikisi de yok; TraceServiceBreakdown services prop'unu tercih eder.
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { TraceWaterfall, TraceServiceBreakdown } from './TraceWaterfall';
import type { SpanRow, TraceAnalysis } from '@/lib/types';

let host: HTMLDivElement; let root: Root;
class NoopResizeObserver { observe() {} unobserve() {} disconnect() {} }
beforeEach(() => {
  if (!('ResizeObserver' in globalThis)) (globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = NoopResizeObserver;
  host = document.createElement('div'); document.body.appendChild(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); });

function span(p: Partial<SpanRow> & { spanId: string }): SpanRow {
  return { traceId: 't1', parentSpanId: '', serviceName: 'orders', name: 'op', startTime: 0, endTime: 10_000_000, statusCode: 'ok', kind: 'client', attributes: {}, ...p } as unknown as SpanRow;
}
const SPANS = [
  span({ spanId: 'root', name: 'GET /x', serviceName: 'api', startTime: 0, endTime: 100_000_000 }),
  span({ spanId: 'c1', parentSpanId: 'root', name: 'db', serviceName: 'db', startTime: 10_000_000, endTime: 50_000_000 }),
  span({ spanId: 'c2', parentSpanId: 'c1', name: 'leaf', serviceName: 'db', startTime: 20_000_000, endTime: 30_000_000, statusCode: 'error' }),
];
const ANALYSIS: TraceAnalysis = {
  v: 1, criticalNs: 150_000_000, criticalIds: ['root', 'c1', 'c2'], rootSpanId: 'root', orphanCount: 0, truncated: false,
  nodes: [
    { spanId: 'root', depth: 0, order: 0, childCount: 1, subtreeCount: 3, subtreeErrors: 1, subtreeNs: 100_000_000, selfNs: 60_000_000, critical: true },
    { spanId: 'c1', parentSpanId: 'root', depth: 1, order: 1, childCount: 1, subtreeCount: 2, subtreeErrors: 1, subtreeNs: 40_000_000, selfNs: 30_000_000, critical: true },
    { spanId: 'c2', parentSpanId: 'c1', depth: 2, order: 2, childCount: 0, subtreeCount: 1, subtreeErrors: 1, subtreeNs: 10_000_000, selfNs: 10_000_000, critical: true },
  ],
  services: [{ service: 'api', spanCount: 1, errorCount: 0, selfNs: 60_000_000, selfPct: 60, entryCount: 1 }, { service: 'db', spanCount: 2, errorCount: 1, selfNs: 40_000_000, selfPct: 40, entryCount: 1 }],
};

describe('TraceWaterfall × sunucu analizi', () => {
  it('bar içinde öz-süre şeridi genişliği selfNs/dur', () => {
    act(() => { root.render(<TraceWaterfall spans={SPANS} selectedId={null} onSelect={() => {}} analysis={ANALYSIS} />); });
    const selfs = host.querySelectorAll('.wf-bar-self');
    expect(selfs.length).toBe(2); // c2 yaprak: self == dur → şerit yok
    expect((selfs[0] as HTMLElement).style.width).toBe('60%');
    expect(host.querySelector('.wf-sub')).toBeNull(); // açık ağaçta özet yok
  });
  it('katlı satırda alt ağaç özeti (N · süre · err)', () => {
    act(() => { root.render(<TraceWaterfall spans={SPANS} selectedId={null} onSelect={() => {}} analysis={ANALYSIS} />); });
    const toggle = host.querySelector('.wf-row .wf-toggle') as HTMLButtonElement;
    act(() => { toggle.click(); });
    const sub = host.querySelector('.wf-sub');
    expect(sub?.textContent).toContain('2');
    expect(sub?.textContent).toContain('1 err');
  });
  it('analysis yokken şerit ve özet yok', () => {
    act(() => { root.render(<TraceWaterfall spans={SPANS} selectedId={null} onSelect={() => {}} />); });
    expect(host.querySelectorAll('.wf-bar-self').length).toBe(0);
  });
  it('TraceServiceBreakdown services prop\'unu tercih eder', () => {
    act(() => { root.render(<TraceServiceBreakdown spans={SPANS} services={ANALYSIS.services} />); });
    expect(host.textContent).toContain('api');
    expect(host.textContent).toContain('db');
    expect(host.textContent?.indexOf('api')).toBeLessThan(host.textContent!.indexOf('db'));
  });
});
