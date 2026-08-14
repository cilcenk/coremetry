// @vitest-environment jsdom
//
// pageControlsCollapse — D6.4 kapısı (v0.9.1001).
//
// Ne çiviliyor: ≤640px'te filtre barının katlanması + MASAÜSTÜNDE
// hiçbir şeyin değişmemesi.
//
// Neden jsdom ve neden gerçek mount: bu değişimin tek gerçek riski
// "geniş ekranda DOM birebir kalsın" sözü. O sözü kaynak taramasıyla
// ölçmenin yolu yok — `narrow` bir çalışma zamanı dalı. Kapı bu yüzden
// iki eşikte de gerçekten render ediyor ve DOM'u sayıyor.
//
// İkinci risk SAYAÇ: "Filtreler (N)" yalan söylerse operatör panelde
// olmayan bir filtreyi arar. Sayı DOM'dan geliyor (`input/select/
// textarea`), yani testin ölçtüğü şey operatörün göreceği şeyle aynı
// kaynaktan.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { PageControls, leadChildIndex, isInteractiveChild, hasLeadMark } from './PageControls';
import { topEscLayer, escLayerDepth, __resetEscLayers } from '@/lib/escLayer';

// ── matchMedia sahtesi ────────────────────────────────────────────────
// `useIsNarrow` yokluğunda GENİŞ ekran varsayıyor (useNarrow.ts), yani
// mock'suz her mount testi masaüstü dalını görür. Eşik testleri için
// sahteyi elle kuruyoruz.
function setViewport(narrow: boolean) {
  window.matchMedia = ((q: string) => ({
    matches: narrow,
    media: q,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function render(ui: React.ReactElement) {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => { root!.render(ui); });
}

const bar = () => host!.querySelector('.controls') as HTMLDivElement;
const moreBtn = () => host!.querySelector('.pc-more') as HTMLButtonElement | null;
const panel = () => host!.querySelector('.pc-panel') as HTMLDivElement | null;
// DisclosureButton glifi (▸/▾) etiketin parçası değil, anatomi işareti.
const label = () => moreBtn()?.textContent?.replace(/[▸▾]/g, '').trim() ?? '';

beforeEach(() => { __resetEscLayers(); });
afterEach(() => {
  act(() => { root?.unmount(); });
  host?.remove();
  root = null; host = null;
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

// ── SAF kural: ana yüzeyde kim kalır ──────────────────────────────────
function Picker() { return <input placeholder="p" />; }

describe('D6.4 — lead çocuk seçimi (saf, tablo)', () => {
  const CASES: Array<{ ad: string; kids: React.ReactNode[]; beklenen: number }> = [
    { ad: 'ilk çocuk zaten arama kutusu',
      kids: [<input key="a" />, <select key="b" />], beklenen: 0 },
    { ad: 'bilgilendirme span\'i atlanır',
      kids: [<span key="a">açıklama</span>, <select key="b" />], beklenen: 1 },
    { ad: 'kap div (ör. .segmented) atlanır, picker seçilir',
      kids: [<div key="a" className="segmented" />, <Picker key="b" />], beklenen: 1 },
    { ad: 'Fragment bir GRUP, kontrol değil — atlanır',
      kids: [<span key="a">x</span>, <>{null}</>, <Picker key="c" />], beklenen: 2 },
    { ad: 'label (checkbox sarmalı) bir kontroldür',
      kids: [<span key="a">x</span>, <label key="b"><input type="checkbox" /></label>], beklenen: 1 },
    { ad: 'hiç kontrol yoksa ilk çocuk',
      kids: [<span key="a">x</span>, <span key="b">y</span>], beklenen: 0 },
    { ad: 'boş liste',
      kids: [], beklenen: -1 },
  ];
  for (const c of CASES) {
    it(c.ad, () => { expect(leadChildIndex(c.kids)).toBe(c.beklenen); });
  }

  it('metin düğümü kontrol sayılmaz', () => {
    expect(isInteractiveChild('düz metin')).toBe(false);
    expect(isInteractiveChild(42)).toBe(false);
    expect(isInteractiveChild(null)).toBe(false);
  });

  // ── M2 (v0.9.1004) — AÇIK işaret ───────────────────────────────────
  //
  // Heuristiğin ölçülmüş sınırı: PICKER ≠ SAYFANIN ARAMASI. `/logs`ta
  // ilk etkileşimli çocuk ServicePicker, sayfanın kimliği olan KQL
  // kutusu onun arkasında kalıyordu.
  const MARK_CASES: Array<{ ad: string; kids: React.ReactNode[]; beklenen: number }> = [
    { ad: 'işaretli çocuk, kendinden ÖNCEKİ kontrolü yener',
      kids: [<Picker key="a" />, <input key="b" data-pc-lead />], beklenen: 1 },
    { ad: 'işaret bileşende de okunur (host eleman şartı yok)',
      kids: [<input key="a" />, <Picker key="b" data-pc-lead />], beklenen: 1 },
    { ad: 'işaret YOKSA davranış bit-bit eski hâl',
      kids: [<Picker key="a" />, <input key="b" />], beklenen: 0 },
    { ad: 'data-pc-lead={false} işaret SAYILMAZ (koşullu işaretleme)',
      kids: [<Picker key="a" />, <input key="b" data-pc-lead={false} />], beklenen: 0 },
    { ad: 'iki işaret varsa ilki kazanır — sessiz belirsizlik yok',
      kids: [<span key="a">x</span>, <input key="b" data-pc-lead />, <input key="c" data-pc-lead />],
      beklenen: 1 },
  ];
  for (const c of MARK_CASES) {
    it(c.ad, () => { expect(leadChildIndex(c.kids)).toBe(c.beklenen); });
  }

  it('hasLeadMark element OLMAYAN düğümlerde patlamıyor', () => {
    expect(hasLeadMark('metin')).toBe(false);
    expect(hasLeadMark(null)).toBe(false);
    expect(hasLeadMark(7)).toBe(false);
  });
});

// ── Masaüstü: DOM BİREBİR ─────────────────────────────────────────────
describe('D6.4 — >640px\'te hiçbir şey değişmiyor', () => {
  beforeEach(() => setViewport(false));

  it('katlama düğmesi ve paneli YOK', () => {
    render(
      <PageControls>
        <input placeholder="ara" />
        <select aria-label="s"><option>a</option></select>
        <span>bilgi</span>
      </PageControls>,
    );
    expect(moreBtn(), 'masaüstünde katlama düğmesi basılmış').toBeNull();
    expect(panel(), 'masaüstünde panel basılmış').toBeNull();
  });

  it('kap tek bir <div class="controls"> ve çocuklar SIRAYLA doğrudan içinde', () => {
    render(
      <PageControls sticky>
        <input placeholder="ara" />
        <select aria-label="s"><option>a</option></select>
        <span>bilgi</span>
      </PageControls>,
    );
    expect(bar().className).toBe('controls is-sticky');
    expect([...bar().children].map(c => c.tagName)).toEqual(['INPUT', 'SELECT', 'SPAN']);
  });
});

// ── Telefon: katlama ──────────────────────────────────────────────────
describe('D6.4 — ≤640px\'te katlanıyor', () => {
  beforeEach(() => setViewport(true));

  function renderBar() {
    render(
      <PageControls sticky>
        <input placeholder="ara" />
        <select aria-label="küme"><option>a</option></select>
        <input placeholder="min ms" type="number" />
        <span>bilgilendirme metni</span>
        <a href="https://example.invalid/db">← Database overview</a>
      </PageControls>,
    );
  }

  it('lead barda kalıyor, kalanlar panelde ve AYNI SIRADA', () => {
    renderBar();
    // Bar: lead + düğme + panel.
    expect([...bar().children].map(c => c.tagName))
      .toEqual(['INPUT', 'BUTTON', 'DIV']);
    expect((bar().children[0] as HTMLInputElement).placeholder).toBe('ara');
    expect([...panel()!.children].map(c => c.tagName))
      .toEqual(['SELECT', 'INPUT', 'SPAN', 'A']);
  });

  // Bileşen SAF kuralı gerçekten KULLANIYOR mu. Yukarıdaki barın lead'i
  // zaten 0. index olduğu için `lead = 0` sabitlense bu dosyadaki her
  // mount testi yeşil kalırdı (mutasyonla ölçüldü, v0.9.1001) — saf
  // fonksiyonun test edilmiş olması onun BAĞLANDIĞI anlamına gelmiyor.
  it('lead 0. index DEĞİLKEN de doğru çocuk barda kalıyor', () => {
    render(
      <PageControls>
        <span>Service Level Objectives — açıklama</span>
        <select aria-label="durum"><option>a</option></select>
        <input placeholder="ara" />
      </PageControls>,
    );
    expect([...bar().children].map(c => c.tagName)).toEqual(['SELECT', 'BUTTON', 'DIV']);
    expect([...panel()!.children].map(c => c.tagName)).toEqual(['SPAN', 'INPUT']);
  });

  // M2 (v0.9.1004) — BAĞLANMA testi, saf kural testi değil.
  //
  // Bu barın şekli /logs'un birebir şekli: picker ÖNCE, sayfanın asıl
  // araması SONRA. `hasLeadMark` yalnız tabloda test edilseydi,
  // `leadChildIndex`in ilk döngüsünü silen bir mutasyon (yani işaretin
  // hiç okunmaması) tabloyu kırar ama BİLEŞENİN onu kullandığını
  // ölçmezdi. Burada işaretli çocuğun gerçekten YÜZEYDE kaldığı ve
  // picker'ın panele indiği görülüyor.
  it('İŞARETLİ çocuk barda kalıyor, picker panele iniyor (/logs şekli)', () => {
    render(
      <PageControls sticky>
        <Picker />
        <select aria-label="küme"><option>a</option></select>
        <input placeholder="Search… (KQL)" data-pc-lead />
        <input placeholder="Trace ID" />
      </PageControls>,
    );
    expect([...bar().children].map(c => c.tagName)).toEqual(['INPUT', 'BUTTON', 'DIV']);
    expect((bar().children[0] as HTMLInputElement).placeholder).toBe('Search… (KQL)');
    // Kalanlar SIRAYI koruyor — işaret yalnız hangisinin yüzeyde
    // kalacağını değiştiriyor, filtrelerin dizilimini değil.
    expect([...panel()!.children].map(c => (c as HTMLElement).tagName))
      .toEqual(['INPUT', 'SELECT', 'INPUT']);
  });

  it('sayaç yalnız GERÇEK form alanlarını sayıyor (span/link sayılmaz)', () => {
    renderBar();
    // Panelde select + number input = 2. Bilgilendirme span'i ve link
    // sayılsaydı 4 yazardı — operatör panelde iki alan bulup "diğer iki
    // filtre nerede" diye arardı.
    expect(label()).toBe('Filtreler (2)');
  });

  it('sayılacak alan yoksa SAYI GÖSTERİLMİYOR', () => {
    render(
      <PageControls>
        <input placeholder="ara" />
        <span>bilgi</span>
        <a href="https://example.invalid/x">link</a>
      </PageControls>,
    );
    expect(label()).toBe('Filtreler');
  });

  it('katlanacak bir şey yoksa düğme HİÇ basılmıyor', () => {
    render(<PageControls><input placeholder="ara" /></PageControls>);
    expect(moreBtn()).toBeNull();
  });

  it('panel kapalıyken çocuklar MOUNT kalıyor (state kaybı yok)', () => {
    renderBar();
    expect(panel()!.className).toBe('pc-panel');
    expect(panel()!.querySelectorAll('select').length).toBe(1);
  });

  it('düğme paneli açıp kapatıyor, ARIA durumu izliyor', () => {
    renderBar();
    expect(moreBtn()!.getAttribute('aria-expanded')).toBe('false');
    act(() => { moreBtn()!.click(); });
    expect(moreBtn()!.getAttribute('aria-expanded')).toBe('true');
    expect(panel()!.className).toBe('pc-panel is-open');
    expect(moreBtn()!.getAttribute('aria-controls')).toBe(panel()!.id);
    act(() => { moreBtn()!.click(); });
    expect(panel()!.className).toBe('pc-panel');
  });

  it('yapışkanlık katlanmış barda da duruyor', () => {
    renderBar();
    expect(bar().className).toBe('controls is-sticky is-collapsible');
  });

  // Esc TEK document dinleyicisinden (keyboard.ts) geçiyor ve o dinleyici
  // yığının TEPESİNİ soruyor. Birim testinde ölçülebilen — ve gerçekten
  // kırılabilen — sözleşme bu: katman yalnız AÇIKKEN yığında olmalı.
  it('Esc katmanı yalnız panel açıkken yığında', () => {
    renderBar();
    expect(escLayerDepth()).toBe(0);
    act(() => { moreBtn()!.click(); });
    expect(escLayerDepth()).toBe(1);
    act(() => { topEscLayer()!(); });
    expect(panel()!.className).toBe('pc-panel');
    expect(escLayerDepth()).toBe(0);
  });

  it('dışarı tık kapatıyor, içeri tık KAPATMIYOR', () => {
    renderBar();
    act(() => { moreBtn()!.click(); });
    act(() => {
      panel()!.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    });
    expect(panel()!.className, 'panelin içine tıklamak paneli kapattı').toBe('pc-panel is-open');
    act(() => {
      document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    });
    expect(panel()!.className).toBe('pc-panel');
  });
});

// ── CSS karşılıkları ──────────────────────────────────────────────────
// jsdom stil UYGULAMIYOR: yukarıdaki mount testleri sınıf adlarını
// doğruluyor, o sınıfların bir şey YAPTIĞINI değil. Sınıflar ölü kalırsa
// panel telefonda kapalıyken de görünür durur ve hiçbir test kırılmaz.
describe('D6.4 — sınıfların CSS karşılığı var ve doğru katmanda', () => {
  const CSS = readFileSync(resolve(__dirname, '../../styles/globals.css'), 'utf8');
  const phone = () => {
    const m = /@media \(max-width: 640px\) \{([\s\S]*)/.exec(CSS);
    expect(m, '640px telefon katmanı kayboldu').toBeTruthy();
    return m![1].replace(/\/\*[\s\S]*?\*\//g, '');
  };

  it('kurallar TELEFON katmanında — masaüstünde tek piksel değişmiyor', () => {
    const desktop = CSS.slice(0, CSS.indexOf('@media (max-width: 640px)'))
      .replace(/\/\*[\s\S]*?\*\//g, '');
    for (const sel of ['.pc-panel', '.pc-more', '.is-collapsible']) {
      expect(desktop, `${sel} masaüstü katmanına sızmış`).not.toContain(sel);
    }
  });

  it('panel kapalıyken gizli, açıkken POPOVER', () => {
    const l = phone();
    expect(l).toMatch(/\.pc-panel \{ display: none; \}/);
    expect(l).toMatch(/\.pc-panel\.is-open \{[^}]*position: absolute/);
    // Yapışkan kademelerin (2-5) ve tablo başlığının üstünde olmalı.
    expect(l).toMatch(/\.pc-panel\.is-open \{[^}]*z-index: var\(--z-dropdown\)/);
  });

  // v0.9.1001'in en sinsi tuzağı: çıplak bir
  // `.controls.is-collapsible { position: relative }` kuralı
  // `.controls.is-sticky`yi (aynı özgüllük, daha erken) EZER ve yapışkan
  // bar dar ekranda sessizce yapışkanlığını kaybeder.
  it('konumlandırma sticky barı EZMİYOR', () => {
    expect(phone()).toMatch(/\.controls\.is-collapsible:not\(\.is-sticky\) \{ position: relative; \}/);
    expect(phone(), 'koşulsuz relative kuralı sticky\'yi ezer')
      .not.toMatch(/\.controls\.is-collapsible \{[^}]*position: relative/);
  });

  it('katlama düğmesi dokunma hedefi 36px (D2.4)', () => {
    expect(phone()).toMatch(/\.btn-disclose\.dsc-row\.pc-more \{[^}]*min-height: 36px/);
  });
});
