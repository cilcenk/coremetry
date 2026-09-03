// @vitest-environment jsdom
// TraceFacetsTab.contract.test.tsx — v0.10.303: GET → satırlar + durum
// rozeti; "Facet ekle" → PUT tüm liste; Sil → PUT eksiltilmiş liste; prod
// SQL göster/gizle. Ağ mock.
import { describe, it, expect, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router-dom';

const { getTraceFacets, putTraceFacets } = vi.hoisted(() => {
  const resp = (facets: { key: string; spellings?: string[]; scope?: string; type?: string }[]) => ({
    facets, bootManaged: true, migrationSql: '-- trace facets\nALTER TABLE spans_local ON CLUSTER uptrace_all ...',
    status: facets.map(f => ({ key: f.key, column: 'attr_f_' + f.key, columnExists: f.key === 'tenant', indexExists: f.key === 'tenant', routed: f.key === 'tenant' })),
    note: 'Kolon/indeks DDL\'i gönderildi.',
  });
  return {
    getTraceFacets: vi.fn(async () => resp([{ key: 'tenant', spellings: ['tenant', 'TENANT_ID'] }, { key: 'region', scope: 'resource', type: 'string' }])),
    putTraceFacets: vi.fn(async (facets: { key: string }[]) => resp(facets)),
  };
});
vi.mock('@/lib/api', () => ({ api: { getTraceFacets, putTraceFacets } }));

import { TraceFacetsTab, facetStateLabel } from './TraceFacetsTab';

let host: HTMLDivElement | null = null; let root: Root | null = null;
function render(node: ReactNode): HTMLElement {
  host = document.createElement('div'); document.body.appendChild(host); root = createRoot(host);
  act(() => { root!.render(<MemoryRouter>{node}</MemoryRouter>); });
  return host;
}
afterEach(() => { act(() => { root?.unmount(); }); host?.remove(); root = null; host = null; putTraceFacets.mockClear(); });
function setInput(el: HTMLInputElement, text: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  setter.call(el, text); el.dispatchEvent(new Event('input', { bubbles: true }));
}
const tick = async () => { await act(async () => { await new Promise(r => setTimeout(r, 20)); }); };

describe('TraceFacetsTab', () => {
  it('kayıtlı facet\'leri durumlarıyla listeler', async () => {
    const el = render(<TraceFacetsTab />);
    await tick();
    const rows = el.querySelectorAll('tr.tf-row');
    expect(rows.length).toBe(2);
    expect(el.textContent).toContain('kolona yönleniyor');
    expect(el.textContent).toContain('kolon yok');
    expect(el.textContent).toContain('attr_f_tenant');
  });
  it('Facet ekle → PUT tüm listeyle; Sil → eksiltilmiş liste', async () => {
    const el = render(<TraceFacetsTab />);
    await tick();
    const inputs = el.querySelectorAll('input');
    act(() => { setInput(inputs[0] as HTMLInputElement, 'channel'); });
    act(() => { setInput(inputs[1] as HTMLInputElement, 'CHANNEL, ch'); });
    act(() => { (el.querySelector('.tf-add') as HTMLButtonElement).click(); });
    await tick();
    expect(putTraceFacets).toHaveBeenCalledTimes(1);
    const sent = putTraceFacets.mock.calls[0][0] as { key: string; spellings?: string[] }[];
    expect(sent.map(f => f.key)).toEqual(['tenant', 'region', 'channel']);
    expect(sent[2].spellings).toEqual(['CHANNEL', 'ch']);
    expect(el.querySelectorAll('tr.tf-row').length).toBe(3);
    // Satırlar anahtara göre sıralı (channel, region, tenant) — tenant satırının Sil'i.
    const tenantRow = Array.from(el.querySelectorAll('tr.tf-row')).find(r => r.querySelector('td')?.textContent === 'tenant')!;
    act(() => { (tenantRow.querySelector('.tf-remove') as HTMLButtonElement).click(); });
    await tick();
    const after = putTraceFacets.mock.calls[1][0] as { key: string }[];
    expect(after.length).toBe(2);
    expect(after.find(f => f.key === 'tenant')).toBeUndefined();
  });
  it('prod SQL göster/gizle', async () => {
    const el = render(<TraceFacetsTab />);
    await tick();
    expect(el.querySelector('pre')).toBeNull();
    const btn = Array.from(el.querySelectorAll('button')).find(b => b.textContent?.includes('Prod SQL')) as HTMLButtonElement;
    act(() => { btn.click(); });
    expect(el.querySelector('pre')!.textContent).toContain('ON CLUSTER uptrace_all');
  });
  it('facetStateLabel basamakları', () => {
    expect(facetStateLabel(undefined).tone).toBe('neutral');
    expect(facetStateLabel({ key: 'a', column: 'c', columnExists: true, indexExists: true, routed: true }).tone).toBe('success');
    expect(facetStateLabel({ key: 'a', column: 'c', columnExists: true, indexExists: false, routed: false }).text).toContain('indeks yok');
    expect(facetStateLabel({ key: 'a', column: 'c', columnExists: false, indexExists: false, routed: false }).text).toBe('kolon yok');
  });
});
