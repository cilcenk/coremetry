// @vitest-environment jsdom
// traceWaterfallLinks.test.tsx — v0.10.274 (Dilim 1a): linkedSpanIds verilen
// span satırında ⛓ rozeti çizilir, diğerlerinde çizilmez; prop yoksa hiç yok.
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { TraceWaterfall } from './TraceWaterfall';
import type { SpanRow } from '@/lib/types';

let host: HTMLDivElement;
let root: Root;
class NoopResizeObserver { observe() {} unobserve() {} disconnect() {} }
beforeEach(() => {
  if (!('ResizeObserver' in globalThis)) {
    (globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = NoopResizeObserver;
  }
  host = document.createElement('div'); document.body.appendChild(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); });

function span(p: Partial<SpanRow> & { spanId: string }): SpanRow {
  return { traceId: 't1', parentSpanId: '', serviceName: 'orders', name: 'op', startTime: 0, endTime: 10_000_000, statusCode: 'ok', kind: 'client', attributes: {}, ...p } as unknown as SpanRow;
}
const SPANS = [span({ spanId: 'root', name: 'GET /x', startTime: 0, endTime: 100_000_000 }), span({ spanId: 'child', parentSpanId: 'root', name: 'publish', startTime: 5_000_000, endTime: 8_000_000 })];

describe('TraceWaterfall × span link rozeti', () => {
  it('linkedSpanIds içindeki satırda ⛓, diğerinde yok', () => {
    act(() => { root.render(<TraceWaterfall spans={SPANS} selectedId={null} onSelect={() => {}} linkedSpanIds={new Set(['child'])} />); });
    const badges = host.querySelectorAll('.wf-link');
    expect(badges.length).toBe(1);
    expect(badges[0].closest('.wf-row')?.textContent).toContain('publish');
  });
  it('prop verilmezse rozet yok', () => {
    act(() => { root.render(<TraceWaterfall spans={SPANS} selectedId={null} onSelect={() => {}} />); });
    expect(host.querySelectorAll('.wf-link').length).toBe(0);
  });
});
