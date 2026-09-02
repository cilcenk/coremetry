// @vitest-environment jsdom
// LogPatternsPanel.contract.test.tsx — v0.10.298: satırlar, dürüst altbilgi
// (örnek/tavan/toplam), "Ara" → türetilmiş sorgu; kapalıyken fetch YOK.
import { describe, it, expect, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';

const { logsPatterns } = vi.hoisted(() => ({
  logsPatterns: vi.fn(async (..._a: unknown[]) => ({
  groups: [
    { hash: 'a', template: 'connection refused to <x> after <x>ms', count: 120, sample: 'connection refused to 10.0.0.1 after 15ms', severity: 17, severityText: 'ERROR', firstSeen: 1, lastSeen: Date.now() * 1e6, services: ['api', 'worker'], serviceCount: 3, query: '"connection refused to" AND "after"' },
    { hash: 'b', template: 'boot ok', count: 3, sample: 'boot ok', severity: 9, severityText: 'INFO', firstSeen: 1, lastSeen: Date.now() * 1e6, services: ['api'], serviceCount: 1, query: '"boot ok"' },
  ],
  sampled: 2000, total: 48000, cap: 2000, truncated: true, distinct: 2,
})),
}));

vi.mock('@/lib/api', () => ({ api: { logsPatterns } }));

import { LogPatternsPanel, agoLabel } from './LogPatternsPanel';

let host: HTMLDivElement | null = null; let root: Root | null = null;
function render(node: ReactNode): HTMLElement {
  host = document.createElement('div'); document.body.appendChild(host); root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  // useDataTable router hook'u kullanır (URL kalıcılığı) → MemoryRouter.
  act(() => { root!.render(<MemoryRouter><QueryClientProvider client={qc}>{node}</QueryClientProvider></MemoryRouter>); });
  return host;
}
afterEach(() => { act(() => { root?.unmount(); }); host?.remove(); root = null; host = null; logsPatterns.mockClear(); });

describe('LogPatternsPanel', () => {
  it('açıkken satırları ve dürüst altbilgiyi çizer; Ara türetilmiş sorguyu verir', async () => {
    const onSearch = vi.fn();
    const el = render(<LogPatternsPanel params={{ service: 'api', from: 1, to: 2 }} open onSearch={onSearch} />);
    await act(async () => { await new Promise(r => setTimeout(r, 30)); });
    expect(logsPatterns).toHaveBeenCalledTimes(1);
    expect(el.querySelectorAll('tr.lp-row').length).toBe(2);
    // Sayı biçimi test ortamının yereline bağlı (2.000 / 2,000) — yalnız şekil.
    expect(el.textContent).toMatch(/2[.,]000 örnek satır \(tavan 2[.,]000\)/);
    expect(el.textContent).toMatch(/pencere toplamı 48[.,]000/);
    expect(el.querySelector('tr.lp-row td')!.textContent).toContain('connection refused to <x>');
    act(() => { (el.querySelector('tr.lp-row .lp-search') as HTMLButtonElement).click(); });
    expect(onSearch).toHaveBeenCalledWith('"connection refused to" AND "after"');
  });
  it('kapalıyken hiç fetch etmez ve DOM boş', async () => {
    const el = render(<LogPatternsPanel params={{ from: 1, to: 2 }} open={false} onSearch={() => {}} />);
    await act(async () => { await new Promise(r => setTimeout(r, 20)); });
    expect(logsPatterns).not.toHaveBeenCalled();
    expect(el.querySelector('.lp-panel')).toBeNull();
  });
  it('agoLabel basamakları', () => {
    const now = 1_000_000_000_000;
    expect(agoLabel((now - 5_000) * 1e6, now)).toBe('5 sn önce');
    expect(agoLabel((now - 120_000) * 1e6, now)).toBe('2 dk önce');
    expect(agoLabel((now - 7_200_000) * 1e6, now)).toBe('2 sa önce');
    expect(agoLabel((now - 172_800_000) * 1e6, now)).toBe('2 g önce');
  });
});
