// @vitest-environment jsdom
//
// Menu.contract.test.tsx — v0.9.890 (tutarlılık denetimi Dalga 3, P3).
//
// BB10: aynı dropdown satırı dört ayrı elle kurulumla yaşıyordu. İkisi
// hover'ı JS ile yapıyordu ve bu, kaybı GÖRÜNMEZ olan türden bir hata:
// `onMouseEnter` yalnız FAREYE bakar, klavye focus'unu hiç görmez — yani
// menüde ok tuşlarıyla gezen operatöre hiçbir vurgu verilmiyordu.
// Yalnız ikisinde `role="menuitem"` vardı; diğer iki menü ekran okuyucuya
// menü OLARAK tanıtılmıyordu.

import { describe, it, expect, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactNode } from 'react';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { MenuItem } from './Menu';

let host: HTMLDivElement | null = null;
let root: Root | null = null;

function render(node: ReactNode): HTMLElement {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => { root!.render(node); });
  return host;
}

afterEach(() => {
  act(() => { root?.unmount(); });
  host?.remove();
  root = null; host = null;
});

const btn = (el: HTMLElement) => el.querySelector('button')!;

describe('MenuItem — sözleşme', () => {
  it('always carries role="menuitem" (two of the four copies had none)', () => {
    expect(btn(render(<MenuItem>Duplicate</MenuItem>)).getAttribute('role')).toBe('menuitem');
  });

  it('defaults to type="button"', () => {
    expect(btn(render(<MenuItem>x</MenuItem>)).type).toBe('button');
  });

  it('emits the .menuitem base class', () => {
    expect(btn(render(<MenuItem>x</MenuItem>)).className.split(' ')).toContain('menuitem');
  });

  it('danger adds .mi-danger', () => {
    expect(btn(render(<MenuItem danger>Delete panel</MenuItem>)).className.split(' '))
      .toContain('mi-danger');
  });

  it('a plain item does NOT carry the danger class', () => {
    expect(btn(render(<MenuItem>Duplicate</MenuItem>)).className.split(' '))
      .not.toContain('mi-danger');
  });

  // İkon yuvası SABİT GENİŞLİKTE ve yalnız ikon verilince basılır; bazı
  // satırları girintili bazılarını girintisiz bırakan bir menü bozuk okunur.
  it('renders the fixed-width icon slot only when an icon is given', () => {
    expect(render(<MenuItem>x</MenuItem>).querySelector('.menuitem-icon')).toBeNull();
    expect(render(<MenuItem icon="↗">x</MenuItem>).querySelector('.menuitem-icon')).not.toBeNull();
  });

  it('hides the icon from screen readers (the label already says it)', () => {
    const slot = render(<MenuItem icon="↗">Explore</MenuItem>).querySelector('.menuitem-icon')!;
    expect(slot.getAttribute('aria-hidden')).toBe('true');
  });

  it('forwards clicks', () => {
    let n = 0;
    const el = render(<MenuItem onClick={() => n++}>x</MenuItem>);
    act(() => { btn(el).click(); });
    expect(n).toBe(1);
  });

  // Ailenin varlık sebebini çiviler. Hover CSS'ten gelmek ZORUNDA: bir JS
  // hover handler'ı geri konursa klavye vurgusu yine sessizce ölür.
  // Yorumlar boşaltılıyor — bu dosyanın kendi başlığı `onMouseEnter`
  // kelimesini geçiriyor ve bu depoda "test kendi düzyazısını kural
  // sanıyor" tuzağı defalarca ısırdı.
  it('the atom itself sets no JS hover handler', () => {
    const src = readFileSync(resolve(__dirname, 'Menu.tsx'), 'utf8')
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n');
    const jsHover = 'onMouse' + 'Enter';
    expect(src).not.toContain(jsHover);
  });
});
