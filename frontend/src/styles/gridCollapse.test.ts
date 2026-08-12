// gridCollapse — dar ekran denetimi D5 kapısı (v0.9.983)
//
// Ne çiviliyor: üç kritik yolculuğun (A: bildirim → problem detayı,
// B: trace görüntüleme, C: log araması) dar ekran dallarının MEVCUT
// olduğu — ve daha önemlisi, çöken ızgaraların satır-içi stile GERİ
// DÖNMEDİĞİ.
//
// Neden bu ikinci şart kapının asıl işi: `gridTemplateColumns` satır içi
// yazıldığında `@media` onu YENEMEZ (satır-içi stilin özgüllüğü yok,
// kaskadın en üstünde). Yani biri "şu ızgarayı burada ayarlayayım" diye
// değeri tekrar bileşene taşırsa dar ekran çökmesi SESSİZCE ölür:
// tsc memnun, test yeşil, masaüstü aynı, telefonda iki kolon geri gelir.
// Bu kapı tam olarak o geri dönüşü yakalıyor.
//
// A yolculuğu operatörün telefonda EN SIK gireceği yol: alarm bildirimi
// linki (PUBLIC_URL + /problems?problem=<id>).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, join } from 'node:path';

const SRC = resolve(__dirname, '..');
const CSS = readFileSync(join(SRC, 'styles/globals.css'), 'utf8');

function phoneLayer(): string {
  const m = /@media \(max-width: 640px\) \{([\s\S]*)/.exec(CSS);
  expect(m, '640px telefon katmanı kayboldu').toBeTruthy();
  return m![1];
}

function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '))
    .split('\n').map(l => (/^\s*\/\//.test(l) ? '' : l)).join('\n');
}

describe('D5 — kritik yolculukların dar ekran dalları', () => {
  // ── Yolculuk A: bildirim → problem detayı ──────────────────────────
  it('ProblemDetail ızgaraları CSS\'te, satır içinde DEĞİL', () => {
    const src = stripComments(readFileSync(join(SRC, 'features/anomalies/ProblemDetail.tsx'), 'utf8'));
    expect(src, 'ızgara satır içine geri taşınmış — @media onu yenemez')
      .not.toMatch(/gridTemplateColumns:\s*'1\.[45]fr 1fr'/);
    expect(src).toContain('className="pd-cols pd-cols-14"');
    expect(src).toContain('className="pd-cols pd-cols-15"');
  });

  it('masaüstü oranları AYNEN korunmuş', () => {
    // Sınıfa taşımak bir yeniden tasarım DEĞİL: değerler birebir aynı.
    expect(CSS).toMatch(/\.pd-cols-14 \{ grid-template-columns: 1\.4fr 1fr; gap: 16px; \}/);
    expect(CSS).toMatch(/\.pd-cols-15 \{ grid-template-columns: 1\.5fr 1fr; gap: 14px;/);
  });

  it('<640px\'te üç ızgara da tek kolona iniyor', () => {
    expect(phoneLayer()).toMatch(/\.pd-cols-14, \.pd-cols-15, \.lcm-row \{ grid-template-columns: 1fr; \}/);
  });

  // ── Yolculuk B: trace şelalesi ─────────────────────────────────────
  it('şelale isim kolonu SAF fonksiyondan geliyor (tablo testli)', () => {
    const src = stripComments(readFileSync(join(SRC, 'components/TraceWaterfall.tsx'), 'utf8'));
    expect(src).toContain("from '@/lib/traceNameCol'");
    expect(src).toContain('nameColWidth(containerWidth, maxDepth)');
    // Hesabın bileşene geri kopyalanması = testin kapsamı dışına çıkması.
    expect(src, 'genişlik hesabı bileşene geri kopyalanmış — tablo testi artık ölçmüyor')
      .not.toMatch(/const depthMin = Math\.min/);
  });

  it('boyutlandırma tutamağı dokunmayı da kabul ediyor (B2)', () => {
    const src = stripComments(readFileSync(join(SRC, 'components/TraceWaterfall.tsx'), 'utf8'));
    expect(src).toContain('onPointerDown={onResizeStart}');
    expect(src, 'fare-yalnız sürükleme geri gelmiş — dokunmayla düzeltilemez')
      .not.toContain('onMouseDown={onResizeStart}');
    expect(src).toContain("addEventListener('pointermove'");
    expect(src).toContain("addEventListener('pointercancel'");
  });

  it('#td-outer dar ekranda viewport aritmetiğinden kurtuluyor (B3)', () => {
    const layer = phoneLayer();
    expect(layer).toMatch(/#td-outer \{ height: auto; \}/);
    // Alt tepsi (55vh) açıkken şelalenin son satırları örtülüyordu.
    expect(layer).toMatch(/#td-outer:has\(#span-panel\) #td-wf \{ padding-bottom: 5\d vh| padding-bottom: 57vh/);
  });

  // ── Yolculuk C: log araması ────────────────────────────────────────
  it('log mesajı telefonda SARIYOR (C3)', () => {
    // `tbody td { white-space: nowrap }` her hücreyi tek satıra
    // kilitliyor; log tablosunda bu doğrudan "mesaj okunmuyor" demek.
    expect(phoneLayer()).toMatch(/\.logtbl-dense > tbody > tr > td \{[^}]*white-space: normal/);
  });

  it('LogContextModal satırı sınıfa taşınmış (C7)', () => {
    const src = stripComments(readFileSync(join(SRC, 'components/LogContextModal.tsx'), 'utf8'));
    expect(src, '5 sabit kolon = 290px; 366px ekranda mesaja 76px kalır')
      .not.toContain("gridTemplateColumns: '60px 50px 110px 1fr 70px'");
    expect(src).toContain('className="lcm-row"');
    expect(CSS).toMatch(/\.lcm-row \{[\s\S]{0,120}grid-template-columns: 60px 50px 110px 1fr 70px/);
  });
});
