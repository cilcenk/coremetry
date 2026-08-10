// @vitest-environment jsdom
//
// DisclosureButton.contract.test.tsx — v0.9.898 (Dalga 3, P6).
//
// mB3'ün altı sitesi bu atoma taşınıyor. İki sözleşme çivileniyor,
// ikisi de "ekranda bir şey bozulmuyor, sadece yanlış" ailesinden:
//
//   1. `aria-expanded` HER ZAMAN — elle kurulmuş altı siteden BEŞİNDE
//      yoktu. Ekran okuyucu bir başlığın açılıp kapandığını hiç
//      duymuyordu; sadece adsız bir "buton" vardı.
//   2. TEK GLİF ÇİFTİ ▸/▾ (operatör kararı 2026-08-10) — depoda iki
//      dil vardı (▶▼ ↔ ▸▾) ve aynı ekranda görünebiliyorlardı.
//
// KAPSAM DÜRÜSTÇE: DOM sözleşmesi test ediliyor, GÖRÜNÜM değil.
// Sınıfların CSS karşılığını `primitiveClasses.test.ts` yokluyor —
// section anatomisinin alt kenarlığı `[aria-expanded="true"]`
// seçicisinden geldiği için o kural oradaki tarama kapsamında.

import { describe, it, expect, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactNode } from 'react';
import { DisclosureButton } from './DisclosureButton';

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

const btn   = (el: HTMLElement) => el.querySelector('button')!;
const glyph = (el: HTMLElement) => el.querySelector('.dsc-glyph')!.textContent;
const cls   = (node: ReactNode) => btn(render(node)).className.split(' ');

describe('DisclosureButton — ARIA sözleşmesi', () => {
  // Ailenin VAROLUŞ sebebi: altı siteden beşinde bu yoktu.
  it.each([[true, 'true'], [false, 'false']] as const)(
    'expanded=%s → aria-expanded="%s"', (expanded, expected) => {
      expect(btn(render(<DisclosureButton expanded={expanded}>S</DisclosureButton>))
        .getAttribute('aria-expanded')).toBe(expected);
    });

  it('never omits aria-expanded — it is not optional in this atom', () => {
    const b = btn(render(<DisclosureButton expanded={false} anatomy="section">S</DisclosureButton>));
    expect(b.hasAttribute('aria-expanded')).toBe(true);
  });

  // Glif çift okunmasın: "▸ Series" değil, "Series".
  it('hides the glyph from the accessibility tree', () => {
    const el = render(<DisclosureButton expanded>S</DisclosureButton>);
    expect(el.querySelector('.dsc-glyph')!.getAttribute('aria-hidden')).toBe('true');
  });

  it('defaults to type="button" (never an accidental form submit)', () => {
    expect((btn(render(<DisclosureButton expanded={false}>S</DisclosureButton>)) as HTMLButtonElement).type)
      .toBe('button');
  });
});

describe('DisclosureButton — tek glif çifti', () => {
  it('collapsed → ▸ , expanded → ▾', () => {
    expect(glyph(render(<DisclosureButton expanded={false}>S</DisclosureButton>))).toBe('▸');
    act(() => { root?.unmount(); }); host?.remove();
    expect(glyph(render(<DisclosureButton expanded>S</DisclosureButton>))).toBe('▾');
  });

  // Terk edilen dialekt. İmza parçalardan kuruluyor ki bu dosyanın kendi
  // düzyazısı bir kaynak taramasına yem olmasın ('hour'+'12' dersi).
  it('never emits the abandoned ▶/▼ dialect', () => {
    const OLD_OPEN = String.fromCodePoint(0x25bc);   // ▼
    const OLD_SHUT = String.fromCodePoint(0x25b6);   // ▶
    for (const expanded of [true, false]) {
      const g = glyph(render(<DisclosureButton expanded={expanded}>S</DisclosureButton>));
      expect(g).not.toBe(OLD_OPEN);
      expect(g).not.toBe(OLD_SHUT);
      act(() => { root?.unmount(); }); host?.remove();
    }
  });

  it('the glyph is a sibling of the label, not wrapped around it', () => {
    const el = render(<DisclosureButton expanded>Series (4)</DisclosureButton>);
    expect(btn(el).textContent).toBe('▾Series (4)');
    expect(el.querySelector('.dsc-glyph')!.textContent).toBe('▾');
  });
});

describe('DisclosureButton — iki anatomi', () => {
  it('always emits the .btn-disclose base class', () => {
    expect(cls(<DisclosureButton expanded>S</DisclosureButton>)).toContain('btn-disclose');
  });

  it('defaults to the row anatomy', () => {
    expect(cls(<DisclosureButton expanded>S</DisclosureButton>)).toContain('dsc-row');
  });

  it.each([['row', 'dsc-row'], ['section', 'dsc-section']] as const)(
    'anatomy=%s → .%s', (anatomy, expected) => {
      expect(cls(<DisclosureButton expanded anatomy={anatomy}>S</DisclosureButton>)).toContain(expected);
    });

  // Üçüncü bir anatomi eklenirse bu iddia onu yakalamaz — ama iki
  // anatominin BİRBİRİNİ dışlaması sözleşmenin parçası: bir başlık aynı
  // anda hem satır-içi hem tam-genişlik olamaz.
  it('the two anatomies are mutually exclusive', () => {
    expect(cls(<DisclosureButton expanded anatomy="section">S</DisclosureButton>)).not.toContain('dsc-row');
    expect(cls(<DisclosureButton expanded anatomy="row">S</DisclosureButton>)).not.toContain('dsc-section');
  });

  it('a caller className MERGES', () => {
    expect(cls(<DisclosureButton expanded className="mono">S</DisclosureButton>))
      .toEqual(expect.arrayContaining(['btn-disclose', 'dsc-row', 'mono']));
  });
});

describe('DisclosureButton — davranış', () => {
  it('forwards clicks', () => {
    let n = 0;
    const el = render(<DisclosureButton expanded={false} onClick={() => n++}>S</DisclosureButton>);
    act(() => { btn(el).click(); });
    expect(n).toBe(1);
  });

  it('passes through title / aria-controls / data-*', () => {
    const b = btn(render(
      <DisclosureButton expanded title="tip" aria-controls="x" data-testid="q">S</DisclosureButton>,
    ));
    expect(b.getAttribute('title')).toBe('tip');
    expect(b.getAttribute('aria-controls')).toBe('x');
    expect(b.getAttribute('data-testid')).toBe('q');
  });
});
