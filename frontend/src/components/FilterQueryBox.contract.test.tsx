// @vitest-environment jsdom
//
// FilterQueryBox.contract.test.tsx — v0.10.264 (Traces "Add filter" Öneri A).
// jsdom sözleşmesi, @testing-library yok. Kapsam: çipler üç parçalı ve × kaldırır;
// satır içi "anahtar=değer" + Enter filtre ekler ve kutu boşalır; Backspace boş
// kutuda son çipi düzenlemeye alır; anahtar adımı önerileri gözlem sayısıyla
// sıralar; op adımında ∃ değer istemeden ekler. Ağ: api/attribute keşfi mock.
import { describe, it, expect, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router-dom';

vi.mock('@/lib/api', () => ({
  api: { attributeValues: vi.fn(async () => [{ value: 'TRN_TRANSFER_EFT', count: 48200 }, { value: 'TRN_CARD', count: 6000 }]) },
}));
vi.mock('@/lib/useAttributeKeys', () => ({
  useAttributeKeys: () => ({ keys: ['http.route', 'http.method', 'channel_code'], observed: [{ key: 'channel_code', count: 900 }, { key: 'http.route', count: 800 }] }),
}));

import { FilterQueryBox } from './FilterQueryBox';
import type { FilterExpr } from '@/lib/types';

let host: HTMLDivElement | null = null;
let root: Root | null = null;
function render(node: ReactNode): HTMLElement {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => { root!.render(<MemoryRouter>{node}</MemoryRouter>); });
  return host;
}
afterEach(() => {
  act(() => { root?.unmount(); });
  host?.remove();
  root = null; host = null;
});

function setInput(el: HTMLInputElement, text: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  setter.call(el, text);
  el.dispatchEvent(new Event('input', { bubbles: true }));
}
function key(el: HTMLElement, k: string) {
  el.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }));
}

const two: FilterExpr[] = [
  { k: 'http.route', op: '=', v: ['/api/v1/accounts/{id}/balance'] },
  { k: 'channel_code', op: 'IN', v: ['MOBILE', 'WEB'] },
];

describe('FilterQueryBox', () => {
  it('çipler üç parçalı; × ilgili filtreyi kaldırır', () => {
    const onChange = vi.fn();
    const el = render(<FilterQueryBox value={two} onChange={onChange} />);
    const chips = el.querySelectorAll('.fq-chip');
    expect(chips.length).toBe(2);
    expect(chips[1].querySelector('.fq-k')!.textContent).toBe('channel_code');
    expect(chips[1].querySelector('.fq-o')!.textContent).toBe('IN');
    expect(chips[1].querySelector('.fq-v')!.textContent).toContain('MOBILE, WEB');
    act(() => { (chips[0].querySelector('.fq-x') as HTMLButtonElement).click(); });
    expect(onChange).toHaveBeenCalledWith([two[1]]);
  });

  it('satır içi anahtar=değer + Enter filtre ekler, kutu boşalır', () => {
    const onChange = vi.fn();
    const el = render(<FilterQueryBox value={[]} onChange={onChange} />);
    const input = el.querySelector('input.fq-input') as HTMLInputElement;
    act(() => { setInput(input, 'kind=server'); });
    expect(el.querySelector('.fq-pop')).not.toBeNull();
    act(() => { key(input, 'Enter'); });
    expect(onChange).toHaveBeenCalledWith([{ k: 'kind', op: '=', v: ['server'] }]);
    expect((el.querySelector('input.fq-input') as HTMLInputElement).value).toBe('');
    expect(el.querySelector('.fq-pop')).toBeNull();
  });

  it('Backspace boş kutuda son çipi düzenlemeye alır', () => {
    const onChange = vi.fn();
    const el = render(<FilterQueryBox value={two} onChange={onChange} />);
    const input = el.querySelector('input.fq-input') as HTMLInputElement;
    act(() => { key(input, 'Backspace'); });
    expect(onChange).toHaveBeenCalledWith([two[0]]);
    expect((el.querySelector('input.fq-input') as HTMLInputElement).value).toBe('MOBILE, WEB');
    expect(el.querySelector('.fq-steps .on')!.textContent).toContain('Değer');
  });

  it('anahtar önerileri gözlem sayısıyla sıralı; Tab op adımına geçer; ∃ değersiz ekler', () => {
    const onChange = vi.fn();
    const el = render(<FilterQueryBox value={[]} onChange={onChange} />);
    const input = el.querySelector('input.fq-input') as HTMLInputElement;
    act(() => { setInput(input, 'http'); });
    const names = Array.from(el.querySelectorAll('.fq-opt .name')).map(n => n.textContent);
    expect(names[0]).toBe('http.route'); // 800 gözlem > http.method (0)
    act(() => { key(input, 'Tab'); });
    expect(el.querySelector('.fq-steps .on')!.textContent).toContain('Op');
    act(() => { setInput(input, 'exists'); });
    act(() => { key(input, 'Enter'); });
    expect(onChange).toHaveBeenCalledWith([{ k: 'http.route', op: 'EXISTS', v: [] }]);
  });
});
