// @vitest-environment jsdom
//
// ContextBar.contract.test.tsx — v0.10.250 (audit §12 test 2). jsdom
// sözleşmesi, @testing-library yok: uygulanmayan boyut devre dışı + ipucu;
// compare çipinin × düğmesi set({compare:''}) çağırır; role="group".
import { describe, it, expect, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ContextBar } from './ContextBar';
import type { ContextParamsResult } from '@/hooks/useContextParams';
import type { ContextDim } from '@/lib/contextParams';

let host: HTMLDivElement | null = null;
let root: Root | null = null;
function render(node: ReactNode): HTMLElement {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, enabled: false } } });
  act(() => { root!.render(<QueryClientProvider client={qc}><MemoryRouter>{node}</MemoryRouter></QueryClientProvider>); });
  return host;
}
afterEach(() => {
  act(() => { root?.unmount(); });
  host?.remove();
  root = null; host = null;
});

function ctxOf(applies: ContextDim[], compare: '' | 'prior' = ''): ContextParamsResult & { set: ReturnType<typeof vi.fn> } {
  const set = vi.fn();
  return {
    params: { range: { preset: '1h' }, env: '', cluster: '', namespace: '', service: '', compare },
    applies: new Set(applies), set, sig: 'x', windowNs: { from: 0, to: 1 },
  };
}

describe('ContextBar', () => {
  it('uygulanmayan boyut devre dışı + ipucu; grup rolü', () => {
    const el = render(<ContextBar ctx={ctxOf(['range', 'env', 'service'])} />);
    expect(el.querySelector('[role="group"][aria-label="Query context"]')).not.toBeNull();
    const cluster = el.querySelector('select[aria-label="Cluster"]') as HTMLSelectElement;
    expect(cluster.disabled).toBe(true);
    expect(cluster.closest('.ctx-field')!.getAttribute('title')).toContain('uygulanmıyor');
    const ns = el.querySelector('input[aria-label="Namespace"]') as HTMLInputElement;
    expect(ns.disabled).toBe(true);
    expect(el.querySelector('button[aria-pressed]')).toBeNull(); // compare uygulanmıyor → çip yok
  });
  it('compare çipi × → set({compare: ""}); uygulanınca cluster seçilebilir', () => {
    const ctx = ctxOf(['range', 'env', 'cluster', 'namespace', 'service', 'compare'], 'prior');
    const el = render(<ContextBar ctx={ctx} />);
    const cluster = el.querySelector('select[aria-label="Cluster"]') as HTMLSelectElement;
    expect(cluster.disabled).toBe(false);
    const remove = el.querySelector('button[aria-label="Karşılaştırmayı kapat"]') as HTMLButtonElement;
    expect(remove).not.toBeNull();
    act(() => { remove.click(); });
    expect(ctx.set).toHaveBeenCalledWith({ compare: '' });
  });
});

describe('ContextBar — zaman seçici sağda (v0.10.255, operatör)', () => {
  it('TimeRangePicker çubuğun SON çocuğu; EnvPicker ondan önce', () => {
    const el = render(<ContextBar ctx={ctxOf(['range', 'env', 'cluster', 'namespace', 'service', 'compare'])} envApplies />);
    const bar = el.querySelector('[role="group"][aria-label="Query context"]')!;
    const last = bar.lastElementChild!;
    expect(last.querySelector('select, button, input')).not.toBeNull();
    expect(last.textContent ?? '').not.toContain('Compare');
    // zaman seçici çubuğun son çocuğu, kapsam kontrolleri ondan önce
    const kids = Array.from(bar.children);
    const idxRange = kids.indexOf(last);
    const idxService = kids.findIndex(k => k.querySelector('[aria-label="Service"], input[placeholder="All services"]'));
    expect(idxService).toBeGreaterThanOrEqual(0);
    expect(idxRange).toBeGreaterThan(idxService);
  });
});
