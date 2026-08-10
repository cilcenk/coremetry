// @vitest-environment jsdom
//
// Chip.contract.test.tsx — v0.9.894 (tutarlılık denetimi Dalga 3, P5).
//
// mB2 + MB6 ailelerinin 12+ sitesi bu atoma taşınıyor. İki sözleşme
// çivileniyor:
//
//   1. RADIUS İKİ RUNG (operatör kararı 2026-08-10) — `pill` (999) ve
//      varsayılan `var(--radius-sm)`. Depodaki 14/8/3 sapmaları bu ikisine
//      normalize edildi; üçüncü bir rung eklemek sapmayı geri getirir.
//   2. `onRemove` ANATOMİSİ — `<button>` içine `<button>` konulamaz, o
//      yüzden × verildiğinde kök bir sarmalayıcıya döner. Bu, testin
//      korumadığı hâlde sessizce bozulabilecek bir yapı: geçersiz HTML
//      tarayıcıda hata vermez, sadece tıklama hedefi çöker.
//
// KAPSAM DÜRÜSTÇE: DOM sözleşmesi test ediliyor, GÖRÜNÜM değil.
// globals.css jsdom'a yüklenmiyor; sınıfların CSS'te karşılığı olduğunu
// `primitiveClasses.test.ts` ayrı olarak yokluyor.

import { describe, it, expect, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactNode } from 'react';
import { Chip } from './Chip';

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

const chip = (el: HTMLElement) => el.querySelector('.btn-chip') as HTMLElement;
const cls  = (node: ReactNode) => chip(render(node)).className.split(' ');

describe('Chip — sınıf eşlemesi', () => {
  it('always emits the .btn-chip base class', () => {
    expect(cls(<Chip>x</Chip>)).toContain('btn-chip');
  });

  // ÇAKIŞMA KORUMASI. `.chip` bu depoda ZATEN var: ProblemDetail ve
  // Incident sayfalarının statik key/value meta rozeti (radius 20px,
  // bg0), sekiz tüketici. Atom o adı basmaya başlarsa iki detay
  // sayfasının meta satırı sessizce yeniden boyanır — ve
  // `primitiveClasses` bunu göremez, çünkü sınıf CSS'te TANIMLI.
  // İmza parçalardan kuruluyor ki bu yorum kendi iddiasını doğrulamasın.
  it('never emits the pre-existing house class (.chip meta pill)', () => {
    const HOUSE = 'ch' + 'ip';
    expect(cls(<Chip pill active tone="accent" size="xs">x</Chip>)).not.toContain(HOUSE);
  });

  // `ib-*` dersinin aynısı: paylaşılan kısa adlar element-seviyesi
  // `button.sm { padding: 3px 9px }` (özgüllük 0,1,1) kuralına yakalanır.
  it('never emits the shared Button variant/size names', () => {
    const c = cls(<Chip size="sm" tone="accent">x</Chip>);
    expect(c).not.toContain('sm');
    expect(c).not.toContain('accent');
  });

  it('defaults to neutral + sm + square (no pill)', () => {
    const c = cls(<Chip>x</Chip>);
    expect(c).toContain('ch-sm');
    expect(c).not.toContain('ch-pill');
    expect(c).not.toContain('ch-accent');
  });

  it.each([['neutral', null], ['accent', 'ch-accent']] as const)(
    'tone=%s → %s', (tone, expected) => {
      const c = cls(<Chip tone={tone}>x</Chip>);
      if (expected) expect(c).toContain(expected);
      else expect(c.filter(x => x.startsWith('ch-') && x !== 'ch-sm')).toEqual([]);
    });

  it.each([['xs', 'ch-xs'], ['sm', 'ch-sm']] as const)('size=%s → .%s', (size, expected) => {
    expect(cls(<Chip size={size}>x</Chip>)).toContain(expected);
  });

  // Radius sözleşmesinin TAMAMI bu iki iddiada: pill VAR ya da YOK.
  // Üçüncü bir rung eklenirse (ör. `radius="md"`) bu ikili yalanlanmaz —
  // ama `.btn-chip` kuralı --radius-sm'i tek varsayılan olarak tuttuğu
  // sürece kaçak bir literal radius CSS'e giremez.
  it('pill → .ch-pill, and its absence is the --radius-sm default', () => {
    expect(cls(<Chip pill>x</Chip>)).toContain('ch-pill');
    expect(cls(<Chip pill={false}>x</Chip>)).not.toContain('ch-pill');
  });

  it('a caller className MERGES (the ch-dashed modifier depends on this)', () => {
    expect(cls(<Chip className="ch-dashed mono">x</Chip>))
      .toEqual(expect.arrayContaining(['btn-chip', 'ch-dashed', 'mono']));
  });
});

describe('Chip — toggle semantiği', () => {
  it('defaults to type="button" (never an accidental form submit)', () => {
    expect((chip(render(<Chip>x</Chip>)) as HTMLButtonElement).type).toBe('button');
  });

  // `active` verilmeyen bir çip TOGGLE DEĞİLDİR — `aria-pressed="false"`
  // basmak onu ekran okuyucuya basılmamış bir anahtar diye tanıtır.
  it('emits no aria-pressed unless the caller opts into toggle semantics', () => {
    expect(chip(render(<Chip>x</Chip>)).hasAttribute('aria-pressed')).toBe(false);
  });

  it.each([[true, 'true'], [false, 'false']] as const)(
    'active=%s → aria-pressed="%s"', (active, expected) => {
      expect(chip(render(<Chip active={active}>x</Chip>)).getAttribute('aria-pressed')).toBe(expected);
    });

  it('active adds .active on top of tone/size', () => {
    expect(cls(<Chip active tone="accent">x</Chip>))
      .toEqual(expect.arrayContaining(['btn-chip', 'ch-accent', 'ch-sm', 'active']));
  });
});

describe('Chip — davranış', () => {
  it('forwards clicks and hands over the real event', () => {
    let got: unknown = null;
    const el = render(<Chip onClick={e => { got = e; }}>x</Chip>);
    act(() => { (chip(el) as HTMLButtonElement).click(); });
    expect(typeof (got as { stopPropagation?: unknown }).stopPropagation).toBe('function');
  });

  it('disabled swallows clicks', () => {
    let n = 0;
    const el = render(<Chip disabled onClick={() => n++}>x</Chip>);
    act(() => { (chip(el) as HTMLButtonElement).click(); });
    expect(n).toBe(0);
  });

  it('passes through title / data-* / aria-*', () => {
    const c = chip(render(<Chip title="tip" data-testid="q" aria-label="lbl">x</Chip>));
    expect(c.getAttribute('title')).toBe('tip');
    expect(c.getAttribute('data-testid')).toBe('q');
    expect(c.getAttribute('aria-label')).toBe('lbl');
  });
});

describe('Chip — onRemove anatomisi', () => {
  // Geçersiz iç içe buton tarayıcıda HATA VERMEZ; DOM'u sessizce yeniden
  // düzenler ve dıştaki tıklama hedefi çöker. Bu yüzden yapı test ediliyor.
  it('never nests a button inside a button', () => {
    const el = render(<Chip onRemove={() => {}} onClick={() => {}}>x</Chip>);
    expect(el.querySelector('button button')).toBeNull();
  });

  it('with onRemove the root becomes a span wrapper, still .btn-chip', () => {
    const el = render(<Chip onRemove={() => {}}>x</Chip>);
    expect(chip(el).tagName).toBe('SPAN');
    expect(chip(el).className.split(' ')).toContain('ch-static');
  });

  // Statik çip (Inbox/Traces): etiket TIKLANAMAZ, o yüzden buton DEĞİL.
  // Buton yapmak sahte affordance olurdu.
  it('a static chip renders its label as a span — exactly one button (the ×)', () => {
    const el = render(<Chip onRemove={() => {}}>service: api</Chip>);
    expect(el.querySelectorAll('button')).toHaveLength(1);
    expect(el.querySelector('.btn-chip-label')!.tagName).toBe('SPAN');
  });

  it('an actionable chip renders its label as a button (label + ×)', () => {
    const el = render(<Chip onRemove={() => {}} onClick={() => {}}>preset</Chip>);
    expect(el.querySelectorAll('button')).toHaveLength(2);
    expect(el.querySelector('.btn-chip-label')!.tagName).toBe('BUTTON');
  });

  it('the × carries an accessible name and hides the glyph', () => {
    const el = render(<Chip onRemove={() => {}} removeLabel="Remove service filter">s</Chip>);
    const x = el.querySelector('.btn-chip-x')!;
    expect(x.getAttribute('aria-label')).toBe('Remove service filter');
    expect(x.querySelector('[aria-hidden="true"]')).not.toBeNull();
  });

  // Ailenin taşındığı sitelerin çoğu tıklanabilir bir satırın İÇİNDE
  // duruyor. × satırı açmamalı — bu yüzden atom stopPropagation'ı
  // kendisi yapıyor, her çağıranın hatırlamasına bırakmıyor.
  it('clicking × calls onRemove and never the chip onClick', () => {
    let removed = 0, clicked = 0;
    const el = render(<Chip onRemove={() => removed++} onClick={() => clicked++}>x</Chip>);
    act(() => { (el.querySelector('.btn-chip-x') as HTMLButtonElement).click(); });
    expect(removed).toBe(1);
    expect(clicked).toBe(0);
  });

  it('title follows the interactive element, or the wrapper when static', () => {
    const stat = render(<Chip onRemove={() => {}} title="wrap">x</Chip>);
    expect(chip(stat).getAttribute('title')).toBe('wrap');
    act(() => { root?.unmount(); }); host?.remove();
    const act1 = render(<Chip onRemove={() => {}} onClick={() => {}} title="lbl">x</Chip>);
    expect(chip(act1).hasAttribute('title')).toBe(false);
    expect(act1.querySelector('.btn-chip-label')!.getAttribute('title')).toBe('lbl');
  });
});
