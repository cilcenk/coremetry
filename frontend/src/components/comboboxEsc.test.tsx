// @vitest-environment jsdom
//
// v0.9.1021 — Combobox × escLayer katman sözleşmesi (Ö28 sınıfı).
//
// Kural: bir Esc BİR katman kapatır. Liste AÇIKKEN Esc tüketilir
// (defaultPrevented=true → keyboard.ts'in escLayer'ı üst katmanı
// KAPATMAZ); liste KAPALIYKEN tüketilmez (olay katmana akar, Drawer/
// Modal normal kapanır). Düzeltme öncesi açık Combobox'lı bir
// Drawer'da tek Esc ikisini birden kapatıyordu (FilterBuilder canlı
// örnekti) — bu test o gerilemeyi çivileriyor. Neden gerçek mount:
// `open` bir çalışma zamanı dalı; kaynak taramasıyla ölçülemez.
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { Combobox } from './Combobox';

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

function mountBox() {
  act(() => {
    root.render(
      <Combobox value="" onChange={() => {}} options={['alpha', 'beta']}
        placeholder="pick…" />,
    );
  });
  return host.querySelector('input') as HTMLInputElement;
}

// jsdom'da gerçek klavye olayı: dispatchEvent dönüşü false ise
// preventDefault çağrılmıştır (= olay tüketildi).
function pressEsc(el: HTMLElement): { consumed: boolean } {
  let consumed = false;
  act(() => {
    const ev = new KeyboardEvent('keydown', {
      key: 'Escape', bubbles: true, cancelable: true,
    });
    consumed = !el.dispatchEvent(ev);
  });
  return { consumed };
}

function typeToOpen(input: HTMLInputElement, text: string) {
  act(() => {
    input.focus();
    const setter = Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype, 'value')!.set!;
    setter.call(input, text);
    input.dispatchEvent(new Event('input', { bubbles: true }));
  });
}

describe('Combobox Esc katman sözleşmesi', () => {
  it('liste AÇIKKEN Esc tüketilir ve yalnız listeyi kapatır', () => {
    const input = mountBox();
    typeToOpen(input, 'al');
    expect(host.textContent).toContain('alpha'); // liste açık
    const { consumed } = pressEsc(input);
    expect(consumed).toBe(true);                  // defaultPrevented
    expect(host.textContent).not.toContain('alpha'); // liste kapandı
  });

  it('liste KAPALIYKEN Esc tüketilmez — üst katmana akar', () => {
    const input = mountBox();
    const { consumed } = pressEsc(input);
    expect(consumed).toBe(false);
  });
});
