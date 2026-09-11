// @vitest-environment jsdom
// traceWaterfallDetail.test.tsx — v0.10.682 (operatör: "tüm span'ler en
// alttan çıkıyor, aynı span'ın altında değil"). renderDetail: seçili satırın
// İÇİNDE, satırın hemen altında çizilir (Tempo düzeni) — hem düz kipte hem
// sanal kipte (ölçülen eleman satırın kendisi olduğu için yükseklik doğru).
// Yalnız seçili satır detay taşır; detayın içine tık satır seçimini tetiklemez.
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
  return { traceId: 't1', parentSpanId: '', serviceName: 'shop', name: 'op', startTime: 0, endTime: 10_000_000, statusCode: 'ok', kind: 'client', attributes: {}, ...p } as unknown as SpanRow;
}
function flat(n: number): SpanRow[] {
  const out = [span({ spanId: 'root', name: 'GET /x', serviceName: 'shop-gateway', startTime: 0, endTime: n * 1_000_000 + 10_000_000 })];
  for (let i = 0; i < n; i++) out.push(span({ spanId: `c${i}`, parentSpanId: 'root', name: `op${i}`, startTime: i * 1_000_000, endTime: i * 1_000_000 + 900_000 }));
  return out;
}

describe('TraceWaterfall renderDetail (v0.10.682)', () => {
  it('düz kip: detay yalnız seçili satırın içinde', () => {
    act(() => {
      root.render(<TraceWaterfall spans={flat(5)} selectedId="c2" onSelect={() => {}}
        renderDetail={id => <div className="probe">detay:{id}</div>} />);
    });
    const details = host.querySelectorAll('.wf-row-detail');
    expect(details.length).toBe(1);
    const sel = host.querySelector('.wf-row.wf-sel') as HTMLElement;
    expect(sel).not.toBeNull();
    expect(sel.querySelector('.probe')?.textContent).toBe('detay:c2');
    expect(sel.classList.contains('wf-has-detail')).toBe(true);
  });
  it('sanal kip (1000 satır): detay yine seçili satırın içinde', () => {
    act(() => {
      root.render(<TraceWaterfall spans={flat(1000)} selectedId="c1" onSelect={() => {}}
        renderDetail={id => <div className="probe">detay:{id}</div>} />);
    });
    const sel = host.querySelector('.wf-row.wf-sel') as HTMLElement;
    expect(sel).not.toBeNull();
    expect(sel.querySelector('.wf-row-detail .probe')?.textContent).toBe('detay:c1');
  });
  it('detay içine tık satır seçimini tetiklemez; seçim yokken detay yok', () => {
    let selected = 0;
    act(() => {
      root.render(<TraceWaterfall spans={flat(5)} selectedId="c2" onSelect={() => { selected++; }}
        renderDetail={() => <button className="inner" type="button">iç</button>} />);
    });
    act(() => { (host.querySelector('.wf-row-detail .inner') as HTMLElement).click(); });
    expect(selected).toBe(0);
    act(() => { root.render(<TraceWaterfall spans={flat(5)} selectedId={null} onSelect={() => {}} renderDetail={() => <div className="probe" />} />); });
    expect(host.querySelectorAll('.wf-row-detail').length).toBe(0);
  });
});
