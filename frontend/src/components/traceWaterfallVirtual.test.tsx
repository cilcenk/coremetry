// @vitest-environment jsdom
// traceWaterfallVirtual.test.tsx — v0.10.278 (Dilim 1d): 400+ satırda pencere
// sanallaştırma (DOM'da yalnız görünür + overscan satır), altında tüm satırlar;
// 150+ satırda trace haritası; küçük trace'te harita yok.
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { TraceWaterfall } from './TraceWaterfall';
import type { SpanRow } from '@/lib/types';

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
function flat(n: number): SpanRow[] {
  const out = [span({ spanId: 'root', name: 'GET /x', serviceName: 'api', startTime: 0, endTime: n * 1_000_000 + 10_000_000 })];
  for (let i = 0; i < n; i++) out.push(span({ spanId: `c${i}`, parentSpanId: 'root', name: `op${i}`, startTime: i * 1_000_000, endTime: i * 1_000_000 + 900_000 }));
  return out;
}

describe('TraceWaterfall × sanal satırlar + harita', () => {
  it('1000 satır: DOM\'da satırların yalnız bir kısmı, kap toplam yüksekliği taşır, harita var', () => {
    act(() => { root.render(<TraceWaterfall spans={flat(1000)} selectedId={null} onSelect={() => {}} />); });
    const n = host.querySelectorAll('.wf-row').length;
    expect(n).toBeGreaterThan(0);
    expect(n).toBeLessThan(200);
    const rowsEl = host.querySelector('.wf-rows') as HTMLElement;
    expect(rowsEl.style.position).toBe('relative');
    expect(parseInt(rowsEl.style.height, 10)).toBeGreaterThan(1000 * 20);
    expect(host.querySelector('.tm-wrap')).not.toBeNull();
    expect(host.querySelector('.wf-row[data-index]')).not.toBeNull();
  });
  it('küçük trace: tüm satırlar DOM\'da, harita ve sanal kap yok', () => {
    act(() => { root.render(<TraceWaterfall spans={flat(5)} selectedId={null} onSelect={() => {}} />); });
    expect(host.querySelectorAll('.wf-row').length).toBe(6);
    expect(host.querySelector('.tm-wrap')).toBeNull();
    expect((host.querySelector('.wf-rows') as HTMLElement).style.height).toBe('');
  });
  it('200 satır: harita var ama sanal değil (content-visibility yolu)', () => {
    act(() => { root.render(<TraceWaterfall spans={flat(200)} selectedId={null} onSelect={() => {}} />); });
    expect(host.querySelector('.tm-wrap')).not.toBeNull();
    expect(host.querySelectorAll('.wf-row').length).toBe(201);
  });
});

// v0.10.324 (operatör, prod: 1065 span'lik trace kesiliyordu) — kaynak pini:
// pencere sanallaştırıcısı YOK; gerçek kaydırma kabı bulunur ve scrollMargin
// o kabın içindeki ofsettir. (Pencere kaydırması bu uygulamada yok:
// #content / .tc-wf overflow:auto — globals.css.)
import { readFileSync } from 'node:fs';
import { resolve as pathResolve } from 'node:path';
describe('şelale sanallaştırıcı kaydırma kabı (v0.10.324)', () => {
  const src = readFileSync(pathResolve(__dirname, 'TraceWaterfall.tsx'), 'utf8');
  it('useWindowVirtualizer kullanılmaz; getScrollElement + findScrollParent', () => {
    // Kapı kendi metnini ısırmasın (yorum adı anabilir): ÇAĞRI ve IMPORT yok.
    expect(src).not.toMatch(/useWindowVirtualizer\s*[(<]/);
    expect(src).not.toMatch(/import \{[^}]*useWindowVirtualizer[^}]*\} from '@tanstack\/react-virtual'/);
    expect(src).toContain('getScrollElement:');
    expect(src).toContain('findScrollParent(listRef.current)');
    expect(src).toContain('offsetWithinScrollParent(listRef.current, sp)');
  });
});
