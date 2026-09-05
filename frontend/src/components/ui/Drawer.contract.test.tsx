// @vitest-environment jsdom
//
// Drawer.contract.test.tsx — v0.9.927 (tutarlılık denetimi Dalga 6, mK1).
//
// Dalganın TEK gerçek regresyon riski buradaydı: odak taşıma + kaydırma
// kilidi KOŞULSUZ eklenirse `backdrop={false}` kipinin — yani CoSRE
// sohbetinin — varlık gerekçesi BİREBİR tersine döner. O kipin tamamı
// "operatör sohbet açıkken tabloyu kaydırsın, başka bir trace açsın,
// sonra sorusunu yazsın" diye var (v0.9.654 operatör talebi). Odağı
// hapsetmek ve `body{overflow:hidden}` basmak sohbeti, eşlik ettiği işi
// imkânsız kılan bir modale çevirirdi.
//
// `drawerBackdrop.test.ts` bu ekseni HİÇ görmüyor: yalnız `backdrop`
// prop'unun varlığına ve varsayılanına bakıyor. Odak ve scroll
// davranışı gerçek bir mount olmadan sınanamaz — bu yüzden jsdom.
import { describe, it, expect, afterEach } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { Drawer } from './Drawer';

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function render(ui: React.ReactElement) {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => { root!.render(ui); });
}

afterEach(() => {
  act(() => { root?.unmount(); });
  host?.remove();
  root = null; host = null;
  document.body.innerHTML = '';
  document.body.style.overflow = '';
});

describe('Drawer — portal', () => {
  it('panel document.body\'ye taşınıyor, çağıranın ağacına DEĞİL', () => {
    render(<Drawer onClose={() => {}} header="H"><p id="c">x</p></Drawer>);
    const c = document.getElementById('c')!;
    // Çağıranın mount kabı (host) içinde OLMAMALI.
    expect(host!.contains(c)).toBe(false);
    expect(document.body.contains(c)).toBe(true);
  });
});

describe('Drawer — sohbet kipi (backdrop=false) SERBEST kalıyor', () => {
  it('sayfa kaydırması KİLİTLENMİYOR', () => {
    render(<Drawer onClose={() => {}} backdrop={false} header="H">x</Drawer>);
    expect(document.body.style.overflow).not.toBe('hidden');
  });

  it('odak çekmeceye ZORLA taşınmıyor', () => {
    const outside = document.createElement('button');
    outside.id = 'outside';
    document.body.appendChild(outside);
    outside.focus();
    render(
      <Drawer onClose={() => {}} backdrop={false} header="H">
        <button id="inside">i</button>
      </Drawer>,
    );
    act(() => { /* setTimeout(…, 0) kuyruğunun boşalması için */ });
    expect(document.activeElement?.id).toBe('outside');
  });

  it('perde öğesi hiç basılmıyor', () => {
    render(<Drawer onClose={() => {}} backdrop={false} header="H">x</Drawer>);
    const scrims = [...document.querySelectorAll('div')]
      .filter(d => d.style.background.includes('--backdrop'));
    expect(scrims.length).toBe(0);
  });
});

describe('Drawer — inceleme kipi (backdrop=true) modal davranıyor', () => {
  it('sayfa kaydırması kilitleniyor ve kapanışta GERİ VERİLİYOR', () => {
    document.body.style.overflow = 'scroll';
    render(<Drawer onClose={() => {}} header="H">x</Drawer>);
    expect(document.body.style.overflow).toBe('hidden');
    act(() => { root!.unmount(); });
    root = null;
    expect(document.body.style.overflow).toBe('scroll');
  });

  it('perde öğesi basılıyor', () => {
    render(<Drawer onClose={() => {}} header="H">x</Drawer>);
    const scrims = [...document.querySelectorAll('div')]
      .filter(d => d.style.background.includes('--backdrop'));
    expect(scrims.length).toBe(1);
  });
});

// v0.10.389 — dış skill denetimi D2: Drawer rolünü duyurur ve perde
// açıkken Tab'ı hapseder (Modal'ın v0.9.924 sözleşmesi, ortak kanca).
function tabKey(shift = false) {
  document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: shift, bubbles: true }));
}
describe('Drawer — diyalog rolü ve Tab hapsi (v0.10.389)', () => {
  it('perde açıkken role=dialog + aria-modal + aria-label beyan edilir', () => {
    render(<Drawer onClose={() => {}} header="H" ariaLabel="Trace çekmecesi">x</Drawer>);
    const panel = document.querySelector('.drawer-panel') as HTMLElement;
    expect(panel.getAttribute('role')).toBe('dialog');
    expect(panel.getAttribute('aria-modal')).toBe('true');
    expect(panel.getAttribute('aria-label')).toBe('Trace çekmecesi');
  });
  it('sohbet kipinde (backdrop=false) aria-modal YOK — arka plan canlı', () => {
    render(<Drawer onClose={() => {}} header="H" backdrop={false}>x</Drawer>);
    const panel = document.querySelector('.drawer-panel') as HTMLElement;
    expect(panel.getAttribute('role')).toBe('dialog');
    expect(panel.hasAttribute('aria-modal')).toBe(false);
  });
  it('perde açıkken son öğeden Tab çekmecenin başına DÖNER', () => {
    render(
      <Drawer onClose={() => {}} header="H">
        <button id="a">A</button>
        <button id="b">B</button>
      </Drawer>,
    );
    const b = document.getElementById('b') as HTMLElement;
    b.focus();
    act(() => { tabKey(); });
    // Çekmecenin ilk odaklanabilir öğesi kapatma düğmesi (aria-label="Close").
    expect(document.activeElement?.getAttribute('aria-label')).toBe('Close');
  });
  it('sohbet kipinde Tab hapsedilmez', () => {
    render(
      <Drawer onClose={() => {}} header="H" backdrop={false}>
        <button id="a">A</button>
        <button id="b">B</button>
      </Drawer>,
    );
    const b = document.getElementById('b') as HTMLElement;
    b.focus();
    act(() => { tabKey(); });
    expect(document.activeElement).toBe(b); // jsdom Tab'ı taşımaz; hapis olsaydı Close'a atlardı
  });
});
