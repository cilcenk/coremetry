// @vitest-environment jsdom
//
// LinkButton.contract.test.tsx — v0.9.889 (tutarlılık denetimi Dalga 3, P4).
//
// MB5'in sekiz sitesi bu atoma iniyor. Sözleşmenin çivilenmesi gereken
// yeri GÖRÜNÜM değil, ANLAM: bu bir <button>, <a> DEĞİL. Beş sitenin
// hiçbiri bir URL'e gitmiyor (filtre temizliyor, sohbet açıyor,
// karşılaştırma açıp kapıyor) ve `<a href="#">` yazmak orta tıkla yeni
// sekme vaadi verirdi — tutulamayan bir vaat.

import { describe, it, expect, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactNode } from 'react';
import { LinkButton } from './LinkButton';

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
const cls = (node: ReactNode) => btn(render(node)).className.split(' ');

describe('LinkButton — anlam sözleşmesi', () => {
  it('renders a real <button>, never an anchor', () => {
    const el = render(<LinkButton>Geri</LinkButton>);
    expect(el.querySelector('a')).toBeNull();
    expect(btn(el).tagName).toBe('BUTTON');
  });

  it('defaults to type="button" (breadcrumbs live inside filter forms)', () => {
    expect(btn(render(<LinkButton>x</LinkButton>)).type).toBe('button');
  });

  it('forwards clicks and keeps the label', () => {
    let n = 0;
    const el = render(<LinkButton onClick={() => n++}>← All clusters</LinkButton>);
    act(() => { btn(el).click(); });
    expect(n).toBe(1);
    expect(btn(el).textContent).toBe('← All clusters');
  });
});

describe('LinkButton — sınıf eşlemesi', () => {
  it('always emits the .btn-link base class', () => {
    expect(cls(<LinkButton>x</LinkButton>)).toContain('btn-link');
  });

  // Varsayılan accent + hover-altçizgi = beş sitenin dördünün bugünkü hâli;
  // ikisi de sınıfsız, yani taban kuralın kendisi.
  it('accent + hover underline are the classless defaults', () => {
    expect(cls(<LinkButton>x</LinkButton>)).toEqual(['btn-link']);
  });

  it.each([['muted', 'lb-muted'], ['accent', undefined]] as const)(
    'tone=%s', (tone, expected) => {
      const c = cls(<LinkButton tone={tone}>x</LinkButton>);
      if (expected) expect(c).toContain(expected);
      else expect(c).toEqual(['btn-link']);
    });

  it.each([['dotted', 'lb-dotted'], ['none', 'lb-plain']] as const)(
    'underline=%s → .%s', (underline, expected) => {
      expect(cls(<LinkButton underline={underline}>x</LinkButton>)).toContain(expected);
    });

  it('tone and underline compose', () => {
    expect(cls(<LinkButton tone="muted" underline="dotted">x</LinkButton>))
      .toEqual(expect.arrayContaining(['btn-link', 'lb-muted', 'lb-dotted']));
  });

  it('a caller className merges', () => {
    expect(cls(<LinkButton className="mine">x</LinkButton>))
      .toEqual(expect.arrayContaining(['btn-link', 'mine']));
  });
});
