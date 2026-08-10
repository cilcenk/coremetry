// @vitest-environment jsdom
//
// Button.contract.test.tsx — v0.9.880 (tutarlılık denetimi Dalga 2, T2).
//
// NEDEN ŞİMDİ: Dalga 2, ~33 dosyada ~60 çıplak `<button>`u bu atoma
// taşıyor. Taşımadan önceki taban gerçek şuydu — depoda 181 test dosyası
// var ve **Button'un tek testi yok**; butonun sınıfını, tipini ya da
// disabled davranışını doğrulayan hiçbir şey yok. Yani dalga, otomatik
// güvenlik ağı SIFIRken başlayacaktı.
//
// `tsc` bu dalganın gerçek hatalarının HİÇBİRİNİ yakalamaz. Üçü de tip
// açısından kusursuz kod üretir:
//   • `<button type="submit">` → `<Button>` yazıldığında atomun varsayılanı
//     `type="button"` olduğu için submit SESSİZCE kaybolur ve form Enter'a
//     ölü kalır. (Depodaki tek çıplak submit: PublicStatus abone formu.)
//   • variant/size → sınıf eşlemesi kayarsa buton görsel olarak başka bir
//     şeye dönüşür; hiçbir test kızarmaz.
//   • çağıranın `className`'i atomunkini EZERSE (birleştirmek yerine)
//     `.sec`/`.sm` düşer — yine sessiz.
//
// Bu dosya sözleşmeyi çiviliyor, dalganın 33 dosyasını değil: tek atom,
// bütün göç. Kapsam dürüstçe: DOM sözleşmesi test ediliyor, GÖRÜNÜM değil —
// sınıfların CSS'te ne yaptığını jsdom söyleyemez (globals.css yüklenmiyor).

import { describe, it, expect, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactNode } from 'react';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { Button } from './Button';

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

describe('Button — type attribute', () => {
  // R1. Bir formun içindeki çıplak `<button>` HTML'de VARSAYILAN OLARAK
  // submit'tir; atomun varsayılanı ise `button`. Mekanik bir taşımada bu
  // fark, formun Enter'a ve tıka ölü kalması demektir.
  it('defaults to type="button", not the HTML submit default', () => {
    expect(btn(render(<Button>Go</Button>)).type).toBe('button');
  });

  it('honours an explicit type="submit" (the form path must stay reachable)', () => {
    expect(btn(render(<Button type="submit">Subscribe</Button>)).type).toBe('submit');
  });

  it('a submit Button inside a form actually submits it', () => {
    let submitted = 0;
    const el = render(
      <form onSubmit={e => { e.preventDefault(); submitted++; }}>
        <Button type="submit">Subscribe</Button>
      </form>,
    );
    act(() => { btn(el).click(); });
    expect(submitted).toBe(1);
  });

  it('a default Button inside a form does NOT submit it', () => {
    let submitted = 0;
    const el = render(
      <form onSubmit={e => { e.preventDefault(); submitted++; }}>
        <Button>Cancel</Button>
      </form>,
    );
    act(() => { btn(el).click(); });
    expect(submitted).toBe(0);
  });
});

describe('Button — variant/size class mapping', () => {
  // Eşleme globals.css'in BUGÜNKÜ kurallarına bağlı: primary ve md
  // sınıfsızdır (element-seviyesi `button` kuralı onları boyar), geri
  // kalanı tek kelimelik değiştirici basar.
  const cls = (node: ReactNode) => btn(render(node)).className;

  it('primary + md emit no class at all (the element-level rule paints them)', () => {
    expect(cls(<Button>x</Button>)).toBe('');
  });

  it.each([
    ['secondary', 'sec'],
    ['danger', 'danger'],
    ['ghost', 'ghost'],
    ['accent', 'accent'],
    ['ghost-danger', 'ghost-danger'],
  ] as const)('variant=%s → .%s', (variant, expected) => {
    expect(cls(<Button variant={variant}>x</Button>).split(' ')).toContain(expected);
  });

  it.each([['xs', 'xs'], ['sm', 'sm'], ['lg', 'lg']] as const)('size=%s → .%s', (size, expected) => {
    expect(cls(<Button size={size}>x</Button>).split(' ')).toContain(expected);
  });

  // v0.9.884. `ghost-danger` tek bir sınıf TOKEN'ı — `.danger`ın soluk bir
  // kardeşi değil. Eğer `'ghost danger'` diye İKİ token basılsaydı, dolu
  // kırmızı `button.danger` kuralı devreye girer ve "sessiz yıkıcı"nın bütün
  // anlamı kaybolurdu. Ayrım tam da bu yüzden yoklanıyor.
  it('ghost-danger does NOT also emit the solid .danger class', () => {
    expect(cls(<Button variant="ghost-danger">x</Button>).split(' ')).not.toContain('danger');
  });

  it('variant and size compose (secondary + sm keeps both)', () => {
    const c = cls(<Button variant="secondary" size="sm">x</Button>).split(' ');
    expect(c).toContain('sec');
    expect(c).toContain('sm');
  });

  // Göçlerin yarısı çağıranın mevcut `className`'ini korumak zorunda
  // (`.trp-btn`, `.live-on`, yerleşim yardımcıları). Ezme = sessiz kayıp.
  it('a caller className MERGES with the atom classes, never replaces them', () => {
    const c = cls(<Button variant="secondary" size="sm" className="trp-btn">x</Button>).split(' ');
    expect(c).toEqual(expect.arrayContaining(['sec', 'sm', 'trp-btn']));
  });

  it('a caller className survives on a primary md button', () => {
    expect(cls(<Button className="mine">x</Button>)).toBe('mine');
  });
});

describe('Button — loading / disabled', () => {
  // R9 + T4. `loading` bir DAVRANIŞ değişikliği: çift-tık koruması. Dalga
  // 2'nin W2.2 dilimi tam da bunun için var — ProblemDetail'in Resolve/
  // Acknowledge butonlarında bugün hiçbir koruma yok, ikinci tık ikinci
  // PUT + ikinci audit girdisi üretiyor.
  it('loading disables the button', () => {
    expect(btn(render(<Button loading>Save</Button>)).disabled).toBe(true);
  });

  it('loading sets aria-busy for screen readers', () => {
    expect(btn(render(<Button loading>Save</Button>)).getAttribute('aria-busy')).toBe('true');
  });

  it('an idle button carries no aria-busy attribute at all', () => {
    expect(btn(render(<Button>Save</Button>)).hasAttribute('aria-busy')).toBe(false);
  });

  it('a loading button swallows clicks (the double-submit guard)', () => {
    let clicks = 0;
    const el = render(<Button loading onClick={() => clicks++}>Save</Button>);
    act(() => { btn(el).click(); });
    expect(clicks).toBe(0);
  });

  it('disabled swallows clicks even without loading', () => {
    let clicks = 0;
    const el = render(<Button disabled onClick={() => clicks++}>Save</Button>);
    act(() => { btn(el).click(); });
    expect(clicks).toBe(0);
  });

  it('an enabled button forwards clicks', () => {
    let clicks = 0;
    const el = render(<Button onClick={() => clicks++}>Save</Button>);
    act(() => { btn(el).click(); });
    expect(clicks).toBe(1);
  });

  it('the label stays readable while loading (spinner is additive)', () => {
    expect(btn(render(<Button loading>Save</Button>)).textContent).toContain('Save');
  });

  // v0.9.884 (R7). Gösterge statik `…`ten `.spinner.sm`e geçti. Bu, KOD
  // DÜZENLENMEDEN ~15 mevcut çağrı sitesinin görünümünü değiştiren bir
  // karar; sözleşmeyi burada çiviliyoruz ki geri kayması sessiz olmasın.
  it('the loading indicator is the sized spinner, not a text glyph', () => {
    const b = btn(render(<Button loading>Save</Button>));
    const spinner = b.querySelector('.spinner');
    expect(spinner).not.toBeNull();
    expect(spinner!.className).toContain('sm');
    expect(b.textContent).not.toContain('…');
  });

  it('the spinner is hidden from screen readers (aria-busy already says it)', () => {
    const spinner = btn(render(<Button loading>Save</Button>)).querySelector('.spinner')!;
    expect(spinner.getAttribute('aria-hidden')).toBe('true');
  });
});

describe('Button — icons and passthrough', () => {
  // R2. Atom children'ı `<span className="row gap-2">` ile SARAR. Göç
  // edilen bazı siteler bu sarmalayıcıya duyarlı (flex:1 span, ::before
  // taşıyan sınıflar) — sarmalayıcının varlığı sözleşmenin parçası.
  it('wraps children in the row/gap layout span', () => {
    const inner = btn(render(<Button>x</Button>)).firstElementChild;
    expect(inner?.className).toBe('row gap-2');
  });

  it('renders leftIcon before and rightIcon after the label', () => {
    const el = render(<Button leftIcon={<i>L</i>} rightIcon={<i>R</i>}>Mid</Button>);
    expect(btn(el).textContent).toBe('LMidR');
  });

  it('forwards arbitrary button attributes (title, aria-label, data-*)', () => {
    const b = btn(render(
      <Button title="tip" aria-label="Zoom out" data-testid="z">x</Button>,
    ));
    expect(b.getAttribute('title')).toBe('tip');
    expect(b.getAttribute('aria-label')).toBe('Zoom out');
    expect(b.getAttribute('data-testid')).toBe('z');
  });
});

// ── MB3 (v0.9.925) — yüklenen buton SOLMAZ ─────────────────────────────
// Atom `loading` iken `disabled` de basıyor (tıkı yutmak için). Element
// seviyesindeki `button:disabled { opacity: .4 }` bu yüzden spinner'ı
// %40 opaklıkta çiziyordu: tam da "çalışıyorum" demesi gereken anda
// gösterge en zor görüleni oluyordu. `aria-busy` "kullanılamaz" ile
// "şu an çalışıyor"u ayıran tek işaret; ikisi aynı görünmemeli.
describe('MB3 — loading görünürlüğü', () => {
  it('aria-busy istisnası CSS\'te tanımlı', () => {
    const css = readFileSync(
      resolve(__dirname, '..', '..', 'styles', 'globals.css'), 'utf8');
    const SEL = 'button[aria-busy=' + '"true"]:disabled';
    const m = new RegExp(`${SEL.replace(/[[\]]/g, m => '\\' + m)}\\s*\\{[^}]*opacity:\\s*1`).exec(css);
    expect(m, 'yüklenen buton hâlâ %40 opaklıkta soluyor').not.toBeNull();
  });

  it('taban solma kuralı hâlâ var (istisna hâlâ gerekli)', () => {
    const css = readFileSync(
      resolve(__dirname, '..', '..', 'styles', 'globals.css'), 'utf8');
    expect(css.includes('button:disabled { opacity: .4;')).toBe(true);
  });
});
