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
