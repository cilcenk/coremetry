// @vitest-environment jsdom
//
// navArbitration — j/k sahiplik arbitrajı kapısı (v0.9.928)
//
// Ne çiviliyor: aynı ekranda iki gezinilebilir tablo varken j/k'nın
// SON ETKİLEŞİM alan tabloya düştüğü — "son mount olan"a değil.
//
// Neden kapı gerekiyor: bu ailenin tamamı SESSİZ. Yanlış tablo seçim
// alırken ekranda hiçbir şey "bozulmuyor"; operatör j'ye basıyor ve
// baktığı tabloda hiçbir şey olmuyor. tsc tip-doğru, eslint sessiz,
// jsdom ise gerçek tık/odak sırasını kendiliğinden üretmiyor. Arbitraj
// bu yüzden SAF bir fonksiyona (`pickOwner`) çekildi ve burada tablo
// sürümlü sınanıyor; DOM tarafı ayrıca gerçek jsdom olaylarıyla.
import { describe, it, expect, beforeEach } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import {
  pickOwner,
  scopeFromTarget,
  noteInteraction,
  getActiveNavScope,
  setActiveNavScope,
  NAV_SCOPE_ATTR,
} from './navScope';

function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '))
    .replace(/^\s*\/\/.*$/gm, '');
}

const NAV = stripComments(readFileSync(resolve(__dirname, 'useTableNav.ts'), 'utf8'));
const KBD = stripComments(readFileSync(resolve(__dirname, 'keyboard.ts'), 'utf8'));
const DT = stripComments(readFileSync(resolve(__dirname, '..', 'components', 'ui', 'DataTable', 'DataTable.tsx'), 'utf8'));
const SERVICES = stripComments(readFileSync(resolve(__dirname, '..', 'pages', 'Services.tsx'), 'utf8'));
const LOGTABLE = stripComments(readFileSync(resolve(__dirname, '..', 'components', 'LogTable.tsx'), 'utf8'));

interface B { id: string; scope?: string }
const A1: B = { id: 'a1', scope: 'alpha' };
const A2: B = { id: 'a2', scope: 'alpha' };
const B1: B = { id: 'b1', scope: 'beta' };
const GLOBAL: B = { id: 'global' };

describe('pickOwner — saf arbitraj', () => {
  const cases: Array<{ name: string; stack: B[]; active: string | null; want: string | undefined }> = [
    { name: 'boş yığın → sahip yok', stack: [], active: 'alpha', want: undefined },
    { name: 'tek kayıt, etkileşim yok → o kayıt', stack: [A1], active: null, want: 'a1' },
    // v0.9.926 davranışı: etkileşim yokken tepe kazanır (son mount).
    { name: 'etkileşim YOKSA tepe (son mount) kazanır', stack: [A1, B1], active: null, want: 'b1' },
    // ASIL KURAL: son etkileşim mount sırasını EZER.
    { name: 'aktif kapsam alttaysa yine O kazanır', stack: [A1, B1], active: 'alpha', want: 'a1' },
    { name: 'aktif kapsam tepedeyse doğal olarak o', stack: [A1, B1], active: 'beta', want: 'b1' },
    // Kendi kendini onarma: aktif tablo unmount olunca yığında kalmaz.
    { name: 'aktif kapsam yığında YOKSA tepeye düşer', stack: [A1, B1], active: 'gamma', want: 'b1' },
    // Aynı tablo re-register olursa (deps değişti) en TAZE kayıt kazanmalı;
    // eskisi hâlâ yığında duruyor olabilir.
    { name: 'aynı kapsamın iki kaydından EN SON kaydolan', stack: [A1, B1, A2], active: 'alpha', want: 'a2' },
    // Kapsamsız (global) binding'ler arbitrajın dışında kalır.
    { name: 'kapsamsız binding aktif kapsamla eşleşmez', stack: [GLOBAL], active: 'alpha', want: 'global' },
    { name: 'kapsamlı kayıt kapsamsız tepeyi geçer', stack: [A1, GLOBAL], active: 'alpha', want: 'a1' },
  ];
  for (const c of cases) {
    it(c.name, () => {
      expect(pickOwner(c.stack, c.active)?.id).toBe(c.want);
    });
  }

  it('yığını MUTASYONA uğratmıyor', () => {
    const stack = [A1, B1];
    pickOwner(stack, 'alpha');
    expect(stack.map(s => s.id)).toEqual(['a1', 'b1']);
  });
});

describe('scopeFromTarget — DOM kimliği', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
    setActiveNavScope(null);
  });

  it('tık satırın İÇİNDEKİ hücreye gelse de kimliği buluyor', () => {
    document.body.innerHTML =
      `<table><tbody><tr ${NAV_SCOPE_ATTR}="alpha" data-row-idx="0">` +
      `<td><span id="badge">x</span></td></tr></tbody></table>`;
    expect(scopeFromTarget(document.getElementById('badge'))).toBe('alpha');
  });

  it('başlık tıkı da kimlik taşıyor (sıralama bir etkileşimdir)', () => {
    document.body.innerHTML =
      `<table><thead ${NAV_SCOPE_ATTR}="beta"><tr><th id="h">P95<span id="arrow">▲</span></th></tr></thead></table>`;
    expect(scopeFromTarget(document.getElementById('arrow'))).toBe('beta');
  });

  it('kapsam dışı eleman → null', () => {
    document.body.innerHTML = `<div id="chip">filter</div>`;
    expect(scopeFromTarget(document.getElementById('chip'))).toBe(null);
  });

  it('null / DOM olmayan hedef patlamıyor', () => {
    expect(scopeFromTarget(null)).toBe(null);
    expect(scopeFromTarget({} as EventTarget)).toBe(null);
  });
});

describe('noteInteraction — yapışkanlık', () => {
  beforeEach(() => {
    document.body.innerHTML =
      `<table><tbody><tr id="ra" ${NAV_SCOPE_ATTR}="alpha"><td id="ca">a</td></tr></tbody></table>` +
      `<table><tbody><tr id="rb" ${NAV_SCOPE_ATTR}="beta"><td id="cb">b</td></tr></tbody></table>` +
      `<button id="outside">Apply</button>`;
    setActiveNavScope(null);
  });

  it('kapsam içi etkileşim sahibi değiştiriyor', () => {
    expect(noteInteraction(document.getElementById('cb'))).toBe(true);
    expect(getActiveNavScope()).toBe('beta');
    expect(noteInteraction(document.getElementById('ca'))).toBe(true);
    expect(getActiveNavScope()).toBe('alpha');
  });

  it('kapsam DIŞI etkileşim sahibi DEĞİŞTİRMİYOR', () => {
    noteInteraction(document.getElementById('cb'));
    // Filtre çipi / zaman aralığı / boşluk — operatör hâlâ beta ile çalışıyor.
    expect(noteInteraction(document.getElementById('outside'))).toBe(false);
    expect(getActiveNavScope()).toBe('beta');
  });

  it('aynı kapsama tekrar dokunmak değişiklik saymıyor', () => {
    noteInteraction(document.getElementById('ca'));
    expect(noteInteraction(document.getElementById('ra'))).toBe(false);
    expect(getActiveNavScope()).toBe('alpha');
  });

  it('j/k belgeye geldiğinde (target = body) sahiplik korunuyor', () => {
    noteInteraction(document.getElementById('ca'));
    expect(noteInteraction(document.body)).toBe(false);
    expect(getActiveNavScope()).toBe('alpha');
  });
});

// Kaynak-tarama kilitleri: yukarıdaki saf çekirdek doğru olsa bile
// çağrı zinciri bir yerde kopuksa yetenek "var görünen yok" olur
// (v0.9.660 sınıfı). Her halka ayrı ayrı aranıyor.
describe('arbitraj zinciri bağlı', () => {
  it('defter sahibi pickOwner ile seçiyor', () => {
    expect(KBD).toContain('pickOwner(stack, getActiveNavScope())');
  });

  it('üç etkileşim yolu da besleniyor (tık · odak · klavye)', () => {
    expect(KBD).toContain("addEventListener('pointerdown'");
    expect(KBD).toContain("addEventListener('focusin'");
    expect(KBD).toContain('noteInteraction(e.target)');
  });

  it('tık/odak YAKALAMA fazında dinleniyor (stopPropagation yutmasın)', () => {
    // ProblemsSection'ın hücreleri e.stopPropagation() çağırıyor; kabarma
    // fazında dinlenseydi o satırlara yapılan tık sahipliği DEĞİŞTİRMEZDİ.
    expect(/addEventListener\('pointerdown',[\s\S]{0,120}?,\s*true\)/.test(KBD)).toBe(true);
    expect(/addEventListener\('focusin',[\s\S]{0,120}?,\s*true\)/.test(KBD)).toBe(true);
  });

  it('useTableNav HER binding\'e kapsam basıyor', () => {
    // Tek tek yazmak yerine map: sekizin birinde unutulursa o tuş
    // arbitrajın dışında kalır ve yarım-bozuk bir durum doğar.
    expect(NAV).toContain('scope: options.pageId');
    expect(NAV).toContain('scoped([');
  });

  it('useTableNav kimliği tüketiciye geri veriyor', () => {
    expect(NAV).toContain('pageId: options.pageId');
  });

  it('DataTableHead kimliği <thead>e basıyor', () => {
    expect(DT).toContain('storageKey');
    expect(DT).toContain('<thead data-table-id={dt.storageKey}>');
  });

  it('kendi <tr>sini basan iki tablo da kimlik damgalıyor', () => {
    // v0.9.926 kapsamlı oto-kaydırmayı getirdi ama damga yalnız
    // useDataTable rowProps'undaydı: Services ve LogTable kendi
    // satırlarını basıyor, yani o iki sayfada kaydırma SESSİZCE ölüydü.
    expect(SERVICES).toContain('data-table-id={tableNav.pageId}');
    expect(LOGTABLE).toContain('data-table-id={tableId}');
  });
});
