// @vitest-environment jsdom
//
// PanelTitle.contract.test.tsx — v0.9.1365.
//
// NEDEN BU TEST VAR: bu atomun terfisinin SEBEBİ bir yuvanın sessizce
// kaybolmasıydı. `pages/DatabaseDetail.tsx:452` nüshası, kopyalandığı gün
// ikizinin aynısıydı; sonra ikizi `right` yuvasını kazandı ve kopya
// kazanmadı. Hiçbir şey bunu görmedi — `tsc` görmez (iki ayrı geçerli
// bileşen), `eslint` görmez, görsel fark da yok çünkü `right` GEÇİLMEDİĞİNDE
// zaten hiçbir şey çizilmiyor. Yani kayıp, ancak birisi `right` geçmeye
// çalıştığında ortaya çıkardı.
//
// Terfi tek nüshaya indirdi ama kaybın MEKANİZMASINI kapatmadı: yuvayı
// bugün silen bir düzenleme yine sessiz kalırdı, çünkü bugün onu geçen
// çağrı yeri yok. MUTASYONLA ÖLÇÜLDÜ (v0.9.1365): `right` dalı silindiğinde
// tsc + lint + 4751 test yeşil kalıyordu. Bu dosya o boşluğu kapatıyor —
// yuvanın çağrı yeri olmasa da SÖZLEŞMESİ var.
//
// KAPSAM DÜRÜSTÇE: DOM sözleşmesi test ediliyor, GÖRÜNÜM değil. Token'ların
// gerçek px karşılığını `styles/geometryTokens.test.ts` yokluyor.

import { describe, it, expect, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactNode } from 'react';
import { PanelTitle } from './PanelTitle';

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
  host = null; root = null;
});

describe('PanelTitle sözleşmesi', () => {
  it('ad her zaman çizilir', () => {
    const el = render(<PanelTitle>Who calls this</PanelTitle>);
    expect(el.textContent).toBe('Who calls this');
  });

  it('sub — DÜRÜSTLÜK YUVASI — verildiğinde çizilir', () => {
    const el = render(<PanelTitle sub="all postgresql instances">Top statements</PanelTitle>);
    expect(el.textContent).toContain('Top statements');
    expect(el.textContent).toContain('all postgresql instances');
  });

  // Fixture bilerek <a href> DEĞİL: `spaLinks.test.ts` (MT10) test
  // dosyalarını da tarıyor ve SPA rotasına çıplak bir <a href="/logs">
  // — sahte de olsa — o kapıyı kırıyor. Ölçüldü: ilk taslak tam olarak
  // böyle kırdı. Yuvanın sözleşmesi "verilen düğüm geçer", hangi etiket
  // olduğu değil.
  it('right — KAYBOLAN YUVA — verildiğinde çizilir', () => {
    const el = render(<PanelTitle right={<button type="button">Logs</button>}>Who calls this</PanelTitle>);
    expect(el.querySelector('button')?.textContent).toBe('Logs');
  });

  it('right sağa yaslanır (marginLeft:auto) — yuva var ama işlevsizse kayıp devam eder', () => {
    const el = render(<PanelTitle right={<span id="r">R</span>}>Ad</PanelTitle>);
    const slot = el.querySelector('#r')!.parentElement!;
    expect(slot.style.marginLeft).toBe('auto');
  });

  it('verilmeyen yuvalar HİÇ düğüm üretmez — bugünkü çıktı korunuyor', () => {
    // Terfi öncesi sayfa nüshası `right`i hiç tanımıyordu ve iki çağrı yeri
    // de geçmiyor. Boş yuvanın düğüm üretmemesi, "çıktı bit bit aynı"
    // iddiasının ölçülebilir hâli.
    const bare = render(<PanelTitle>Ad</PanelTitle>);
    expect(bare.querySelectorAll('span').length).toBe(1);
  });
});
