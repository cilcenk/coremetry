// @vitest-environment jsdom
//
// actionOrder — K5 kapısı (v0.9.1007, etkileşim denetimi M5).
//
// NE ÇİVİLİYOR: "iptal solda, onay sağda" kuralını.
//
// Denetimin ölçtüğü ikilik bu kuralda en net görülüyor: `.modal-footer`
// CSS'i `justify-content: flex-end` bastığı için 14 modal footer'ının
// 14'ü de uyumlu ve elle yazılmış tek istisna yok. Aynı depoda, atomu
// OLMAYAN form/kart satırlarında 4 satır TERSTİ (onay solda) ve 115
// çok-butonlu kapsayıcının 47'si satır-içi `style={{display:'flex'}}`.
// Yani kural bilgi eksikliğinden değil, hiçbir şeyin ihlali
// yakalamamasından çiğneniyordu.
//
// KAPI İKİ AYAKLI:
//   A) Modal footer'ları — bugün 0 ihlal, KİLİTLENİYOR. 15. çağıran
//      ters yazarsa CSS hiçbir şey söylemez (hiza zorlanıyor, SIRA
//      zorlanmıyor — L8); bu test söyler.
//   B) ActionRow — sıra CSS'e değil YAPIYA yazılı. Mount testi
//      `confirm`in gerçekten en sağda, `destructive`in en solda
//      render edildiğini ölçüyor. Saf bir prop testi bunu ölçmezdi.
import { describe, it, expect, afterEach } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { readFileSync, readdirSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { ActionRow } from './ActionRow';
import { Button } from './Button';
import { stripTsComments } from '../../styles/zLayers.test';

const SRC = resolve(__dirname, '..', '..');

// İptal sözlüğü — depoda fiilen kullanılan yazımlar (TR + EN).
const CANCEL = /^(cancel|vazgeç|vazgec|kapat|close|iptal|dismiss)$/i;

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (e.name === 'node_modules' || e.name === 'dist') continue;
    const p = join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (e.name.endsWith('.tsx')) out.push(p);
  }
  return out;
}

// JSX açılış etiketi tarayıcısı — süslü parantez dengeli.
//
// NEDEN DÜZ REGEX DEĞİL: `<Modal … footer={<>…</>}>` içindeki `>`
// karakterleri etiketi erken kapatır. Denetimin ilk tarayıcısı tam bu
// yüzden render-prop içindeki 46 butonu kaçırmıştı.
function openingTags(src: string, name: string): Array<[number, number, number]> {
  const out: Array<[number, number, number]> = [];
  const re = new RegExp(`<${name}(?=[\\s/>])`, 'g');
  let m: RegExpExecArray | null;
  while ((m = re.exec(src))) {
    let i = m.index + m[0].length, depth = 0;
    while (i < src.length) {
      const c = src[i];
      if (c === '{') depth++;
      else if (c === '}') depth--;
      else if (c === '>' && depth === 0) break;
      i++;
    }
    out.push([m.index, m.index + m[0].length, i]);
  }
  return out;
}

function braceExpr(src: string, from: number): string | null {
  const j = src.indexOf('{', from);
  if (j < 0) return null;
  let depth = 0;
  for (let k = j; k < src.length; k++) {
    if (src[k] === '{') depth++;
    else if (src[k] === '}') { depth--; if (depth === 0) return src.slice(j, k + 1); }
  }
  return null;
}

describe('K5-A — Modal footer’larında SON buton bir iptal değil', () => {
  // Yorumlar SOYULUYOR: bu dosyanın kendi düzyazısı ve hedef
  // dosyalardaki açıklama örnekleri (`footer={…}` diye anlatan
  // yorumlar) aksi hâlde tarayıcıya kod gibi görünürdü.
  const footers: Array<{ file: string; line: number; labels: string[] }> = [];
  for (const p of walk(SRC)) {
    const src = stripTsComments(readFileSync(p, 'utf8'));
    for (const [ts, , a1] of openingTags(src, 'Modal')) {
      const fi = src.indexOf('footer=', ts);
      if (fi < 0 || fi > a1) continue;
      const fe = braceExpr(src, fi);
      if (!fe) continue;
      const labels = openingTags(fe, 'Button')
        .map(([, , b1]) => fe.slice(b1 + 1).split('<')[0].trim());
      footers.push({ file: p.slice(SRC.length + 1), line: src.slice(0, ts).split('\n').length, labels });
    }
  }

  it('tarama gerçekten footer buluyor', () => {
    // Sağlık assert'i. Bugün 14; eşik altında tutuldu ki bir modalın
    // kapanması testi kırmızıya çevirmesin, ama tarama tamamen körelirse
    // (ör. prop adı değişti) haber versin.
    expect(footers.length).toBeGreaterThanOrEqual(10);
    expect(footers.some(f => f.labels.length >= 2)).toBe(true);
  });

  it('hiçbir footer’ın SON butonu iptal sözlüğünde değil', () => {
    const bad = footers
      .filter(f => f.labels.length > 0 && CANCEL.test(f.labels[f.labels.length - 1] ?? ''))
      .map(f => `${f.file}:${f.line} → ${f.labels.join(' | ')}`);
    expect(bad, 'iptal en sağda = kas hafızasının onay beklediği yer').toEqual([]);
  });

  // DÜRÜSTLÜK PAYI — kapının ölçemediği yer.
  //
  // Ternary'li footer'lar (`done ? <Button>Kapat</Button> : <>…</>`)
  // TEK ifade olarak değerlendiriliyor, dal başına değil. Dal başına
  // bakılsaydı PinToDashboardModal'ın "iş bitti" dalı yanlış-pozitif
  // olurdu: orada tek buton var ve o bir kapatma — yani kuralın
  // konusu değil, çünkü kural bir SIRA kuralı ve tek elemanlı bir
  // listenin sırası yoktur. Bu sınır bilinçli.
  it('ternary’li footer TEK ifade sayılıyor (bilinçli sınır)', () => {
    const pin = footers.find(f => f.file.includes('PinToDashboardModal'));
    expect(pin, 'PinToDashboardModal footer’ı taranmalı').toBeTruthy();
    expect(pin!.labels.length).toBeGreaterThan(1);
  });
});

// ── ActionRow: sıra YAPIYA yazılı ─────────────────────────────────────
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
});

describe('K5-B — ActionRow sırayı YAPIDAN alıyor', () => {
  const labels = () => [...host!.querySelectorAll('button')].map(b => b.textContent?.trim());

  it('destructive EN SOLDA, confirm EN SAĞDA — yazım sırası fark etmez', () => {
    // Props'lar bilerek "yanlış" sırada veriliyor: çağıranın yazım
    // sırası sonucu DEĞİŞTİRMEMELİ, rol beyanı belirlemeli.
    render(
      <ActionRow
        confirm={<Button variant="primary">Save</Button>}
        destructive={<Button variant="ghost-danger">Delete</Button>}
        secondary={<Button variant="secondary">Cancel</Button>} />,
    );
    expect(labels()).toEqual(['Delete', 'Cancel', 'Save']);
  });

  it('yıkıcı yuva boşken de iptal/onay sırası korunuyor', () => {
    render(
      <ActionRow
        secondary={<Button variant="secondary">Cancel</Button>}
        confirm={<Button variant="primary">Add</Button>} />,
    );
    expect(labels()).toEqual(['Cancel', 'Add']);
  });

  it('secondary birden fazla olabilir, confirm TEK', () => {
    render(
      <ActionRow
        secondary={<><Button variant="accent">✨ AI</Button><Button variant="secondary">Cancel</Button></>}
        confirm={<Button variant="primary">Save</Button>} />,
    );
    expect(labels()).toEqual(['✨ AI', 'Cancel', 'Save']);
  });

  it('inline kipi yalnız DISPLAY’i değiştiriyor, sırayı değil', () => {
    render(
      <ActionRow inline
        secondary={<Button variant="secondary">Cancel</Button>}
        confirm={<Button variant="primary">Save</Button>} />,
    );
    expect(host!.querySelector('.action-row')!.className).toBe('action-row is-inline');
    expect(labels()).toEqual(['Cancel', 'Save']);
  });

  it('bastığı her sınıfın globals.css karşılığı var', () => {
    // primitiveClasses (v0.9.884) dersinin aynısı: atomun ÇALIŞMA
    // ANINDA bastığı sınıf statik taramanın kör noktası. CSS aynı
    // commit'te gelmezse satır sola yaslı kalır ve düzeltme sessizce
    // hiçbir şey yapmaz.
    const css = readFileSync(join(SRC, 'styles/globals.css'), 'utf8');
    for (const cls of ['.action-row', '.action-row.is-inline', '.action-row-gap']) {
      expect(css.includes(cls + ' '), `${cls} tanımsız`).toBe(true);
    }
    expect(css).toMatch(/\.action-row\s*\{[^}]*justify-content:\s*flex-end/);
  });
});

describe('K5-C — 4 ters satır ATOMA geçti', () => {
  // Bulgu kimliğiyle kayıtlı: bunlar O8'in dört ihlali. Biri elle
  // yazılmış flex satırına geri dönerse kapı haber verir.
  const O8_SITES = [
    'components/FilterBuilder.tsx',
    'components/ServiceCatalogPill.tsx',
    'pages/AdminCatalog.tsx',
    'pages/settings/DangerZoneTab.tsx',
  ];
  for (const rel of O8_SITES) {
    it(`${rel} ActionRow kullanıyor`, () => {
      const src = stripTsComments(readFileSync(join(SRC, rel), 'utf8'));
      expect(src).toMatch(/<ActionRow[\s/>]/);
    });
  }
});
