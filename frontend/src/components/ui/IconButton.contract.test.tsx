// @vitest-environment jsdom
//
// IconButton.contract.test.tsx — v0.9.887 (tutarlılık denetimi Dalga 3, P2).
//
// MB4 ailesinin 10+ sitesi bu atoma taşınıyor. Taşınan her site bugün
// `all: 'unset'` + satır-içi padding ile kurulmuş durumda; dolayısıyla
// göçün kazandırdığı şeyler (focus halkası, erişilebilir ad, tutarlı
// hit-alanı) DAVRANIŞSAL kazançlar ve sözleşme olarak çivilenmeleri
// gerekiyor — yoksa bir sonraki düzenlemede sessizce geri kayarlar.
//
// KAPSAM DÜRÜSTÇE: DOM sözleşmesi test ediliyor, GÖRÜNÜM değil.
// globals.css jsdom'a yüklenmiyor; sınıfın CSS'te KARŞILIĞI OLDUĞUNU
// `primitiveClasses.test.ts` ayrı olarak yokluyor. İkisi birlikte
// "sınıf basılıyor" + "sınıf boyanıyor" zincirini kapatıyor.

import { describe, it, expect, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactNode } from 'react';
import { IconButton } from './IconButton';

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

describe('IconButton — erişilebilirlik sözleşmesi', () => {
  // Ailenin VAROLUŞ sebebi. Glif-only bir buton `aria-label` olmadan ekran
  // okuyucuda yalnızca "buton" diye duyurulur; taşınan sitelerin çoğunda
  // (Services pin, Endpoints expander) hiç yoktu.
  it('carries the mandatory aria-label onto the DOM', () => {
    const b = btn(render(<IconButton aria-label="Pin service" icon="★" />));
    expect(b.getAttribute('aria-label')).toBe('Pin service');
  });

  // Glif `aria-hidden` — aksi hâlde "★ Pin service" gibi çift okunurdu.
  it('hides the glyph from the accessibility tree', () => {
    const b = btn(render(<IconButton aria-label="Pin" icon="★" />));
    expect(b.querySelector('.btn-icon-glyph')!.getAttribute('aria-hidden')).toBe('true');
  });

  it('defaults to type="button" (never an accidental form submit)', () => {
    expect(btn(render(<IconButton aria-label="x" icon="x" />)).type).toBe('button');
  });

  // `active` verilmeyen bir buton TOGGLE DEĞİLDİR. `aria-pressed="false"`
  // basmak onu ekran okuyucuya basılmamış bir anahtar gibi tanıtır ve
  // "aç/kapat" beklentisi yaratır — yanlış vaat.
  it('emits no aria-pressed unless the caller opts into toggle semantics', () => {
    expect(btn(render(<IconButton aria-label="x" icon="x" />)).hasAttribute('aria-pressed')).toBe(false);
  });

  it.each([[true, 'true'], [false, 'false']] as const)(
    'active=%s → aria-pressed="%s"', (active, expected) => {
      const b = btn(render(<IconButton aria-label="x" icon="x" active={active} />));
      expect(b.getAttribute('aria-pressed')).toBe(expected);
    });
});

describe('IconButton — sınıf eşlemesi', () => {
  const cls = (node: ReactNode) => btn(render(node)).className.split(' ');

  it('always emits the .btn-icon base class', () => {
    expect(cls(<IconButton aria-label="x" icon="x" />)).toContain('btn-icon');
  });

  // Varsayılanlar `all: unset` sitelerinin bugünkü görünümüne en yakın
  // olanlar: kenarlıksız/zeminsiz (`ghost`) ve 24px kare (`sm`).
  it('defaults to ghost + sm', () => {
    const c = cls(<IconButton aria-label="x" icon="x" />);
    expect(c).toContain('ib-ghost');
    expect(c).toContain('ib-sm');
  });

  it.each([
    ['secondary', 'ib-sec'],
    ['ghost', 'ib-ghost'],
    ['bare', 'ib-bare'],
  ] as const)('variant=%s → .%s', (variant, expected) => {
    expect(cls(<IconButton aria-label="x" icon="x" variant={variant} />)).toContain(expected);
  });

  it.each([
    ['xs', 'ib-xs'], ['sm', 'ib-sm'], ['md', 'ib-md'],
  ] as const)('size=%s → .%s', (size, expected) => {
    expect(cls(<IconButton aria-label="x" icon="x" size={size} />)).toContain(expected);
  });

  // `ib-*` ön eki KASITLI: `sm`/`sec` paylaşılan adları kullanılsaydı
  // element-seviyesi `button.sm { padding: 3px 9px }` kuralı karenin
  // padding:0'ını ezer (özgüllük 0,1,1 > 0,1,0) ve buton dikdörtgen olurdu.
  it('never emits the shared Button size classes (they would rectangle it)', () => {
    const c = cls(<IconButton aria-label="x" icon="x" size="sm" variant="secondary" />);
    expect(c).not.toContain('sm');
    expect(c).not.toContain('sec');
  });

  it('active adds .active on top of the variant/size classes', () => {
    const c = cls(<IconButton aria-label="x" icon="x" active />);
    expect(c).toEqual(expect.arrayContaining(['btn-icon', 'ib-ghost', 'ib-sm', 'active']));
  });

  it('a caller className MERGES (the ib-star modifier depends on this)', () => {
    expect(cls(<IconButton aria-label="x" icon="x" className="ib-star" />))
      .toEqual(expect.arrayContaining(['btn-icon', 'ib-star']));
  });
});

describe('IconButton — davranış', () => {
  it('forwards clicks', () => {
    let n = 0;
    const el = render(<IconButton aria-label="x" icon="x" onClick={() => n++} />);
    act(() => { btn(el).click(); });
    expect(n).toBe(1);
  });

  it('disabled swallows clicks', () => {
    let n = 0;
    const el = render(<IconButton aria-label="x" icon="x" disabled onClick={() => n++} />);
    act(() => { btn(el).click(); });
    expect(n).toBe(0);
  });

  // Satır-içi tablolarda taşınan sitelerin çoğu satırın kendi onClick'ini
  // durdurmak zorunda; olay nesnesinin çağırana ULAŞMASI sözleşmenin parçası.
  it('hands the real event to onClick (stopPropagation must stay reachable)', () => {
    let got: unknown = null;
    const el = render(<IconButton aria-label="x" icon="x" onClick={e => { got = e; }} />);
    act(() => { btn(el).click(); });
    expect(got).not.toBeNull();
    expect(typeof (got as { stopPropagation?: unknown }).stopPropagation).toBe('function');
  });

  it('passes through arbitrary attributes (title, aria-expanded, data-*)', () => {
    const b = btn(render(
      <IconButton aria-label="x" icon="x" title="tip" aria-expanded={true} data-testid="q" />,
    ));
    expect(b.getAttribute('title')).toBe('tip');
    expect(b.getAttribute('aria-expanded')).toBe('true');
    expect(b.getAttribute('data-testid')).toBe('q');
  });
});
