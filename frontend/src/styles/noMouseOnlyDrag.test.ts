// noMouseOnlyDrag — dar ekran denetimi D6.5 kapısı (v0.9.988)
//
// Ne çiviliyor: bir SÜRÜKLEME jesti `mousemove`/`mouseup` üzerinden
// kurulamaz. Dokunmatik bir cihazda tarayıcı bir TAP'in ardından
// sentetik `mousedown`/`mouseup` üretir ama SÜRÜKLEME için `mousemove`
// dizisi ÜRETMEZ. Yani `window.addEventListener('mousemove', …)` ile
// yazılmış her tutamak telefonda/tablette ÖLÜDÜR — ve bunu hiçbir tip
// kontrolü, lint kuralı ya da jsdom testi göremez, çünkü kod tamamen
// geçerlidir ve masaüstünde kusursuz çalışır.
//
// İMZA DAR SEÇİLDİ: yasak olan `onMouseDown` DEĞİL, `mousemove`/
// `mouseup` GLOBAL DİNLEYİCİSİ. Depodaki `onMouseDown` çağrılarının
// çoğu sürükleme değil — combobox seçenekleri `e.preventDefault()` ile
// input blur'unu engelliyor, Modal arka fonu kapatma tıkını yakalıyor,
// `lib/utils.ts` orta tık için. Bunların hepsi dokunmada sentetik
// olaylarla ÇALIŞIR; onları yasaklamak kapıyı gürültüye boğar ve gerçek
// bulguyu saklardı.
//
// Kapı MUTASYONLA doğrulandı: `components/DataTable.tsx`teki
// `pointermove` geri `mousemove` yapıldığında kırmızıya döndüğü
// görüldükten sonra gemiye alındı.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { stripTsComments } from './zLayers.test';

const SRC = resolve(__dirname, '..');

function srcFiles(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    if (e === 'node_modules') continue;
    const p = join(dir, e);
    if (statSync(p).isDirectory()) srcFiles(p, out);
    else if (/\.tsx?$/.test(e) && !/\.test\.tsx?$/.test(e)) out.push(p);
  }
  return out;
}

// GEREKÇEYE göre anahtarlanmış muafiyetler (satıra göre DEĞİL —
// v0.9.887 dersi: bir import satırı eklenince satır numaraları kayar).
const ALLOWLIST = new Map<string, string>([
  [
    'components/ServiceMapGraph.tsx',
    // Pan jesti ORTA TIK ya da Shift/Alt+sol tık ile kapılı
    // (`e.button !== 1 && !(e.button === 0 && (e.shiftKey || e.altKey))`).
    // Dokunmatik bir cihazda ne orta tık ne değiştirici tuş var, yani
    // jest zaten erişilemez; Pointer'a çevirmek tek bayt kazandırmaz.
    // Dokunmatikte harita gezinmesi ayrı bir ÜRÜN kararı (pinch/pan),
    // bu dalganın kapsamı değil.
    'pan yalnız orta tık / değiştirici+sol tık ile açılıyor — dokunmada erişilemez jest',
  ],
  [
    'components/Sidebar.tsx',
    // Tutamak `!effCollapsed && !isMobile` koşuluyla render ediliyor ve
    // `onResizeStart` de `isMobile` kontrolüyle erken dönüyor: dar
    // ekranda kenar çubuğu off-canvas, genişliği ayarlanamaz.
    'resizer dar ekranda hiç render edilmiyor (off-canvas mod)',
  ],
]);

describe('D6.5 — sürükleme jestleri Pointer Events üstünde', () => {
  it('global mousemove/mouseup dinleyicisi kalmadı', () => {
    // İmza çalışma anında kuruluyor ki bu dosyanın kendi düzyazısı
    // eşleşmesin (depoda yedi kez ısıran tuzak).
    const RE = new RegExp(`addEventListener\\(\\s*['"](${'mouse'}move|${'mouse'}up)['"]`);
    const bad: string[] = [];
    for (const p of srcFiles(SRC)) {
      const rel = p.slice(SRC.length + 1);
      if (ALLOWLIST.has(rel)) continue;
      const src = stripTsComments(readFileSync(p, 'utf8'));
      src.split('\n').forEach((l, i) => {
        if (RE.test(l)) bad.push(`${rel}:${i + 1} ${l.trim().slice(0, 80)}`);
      });
    }
    expect(
      bad,
      'sürükleme mouse olaylarına bağlı — dokunmatik cihazda ÖLÜ; pointermove/pointerup/pointercancel kullan',
    ).toEqual([]);
  });

  it('Pointer sürüklemesi pointercancel de dinliyor (asılı kalan sürükleme yok)', () => {
    const MOVE = new RegExp(`addEventListener\\(\\s*['"]${'pointer'}move['"]`);
    const CANCEL = new RegExp(`addEventListener\\(\\s*['"]${'pointer'}cancel['"]`);
    const bad: string[] = [];
    for (const p of srcFiles(SRC)) {
      const src = stripTsComments(readFileSync(p, 'utf8'));
      if (MOVE.test(src) && !CANCEL.test(src)) bad.push(p.slice(SRC.length + 1));
    }
    expect(
      bad,
      'pointermove var ama pointercancel yok — tarayıcı jesti devralınca sürükleme asılı kalır',
    ).toEqual([]);
  });

  it('paylaşılan kolon tutamağı jest kilidini bildiriyor', () => {
    // `touch-action: none` olmadan Pointer sürüklemesi dokunmada YİNE
    // çalışmaz: tarayıcı ilk parmak hareketinde jesti kaydırma olarak
    // devralır. JS doğru, davranış yanlış — tam olarak sessiz sınıf.
    const css = readFileSync(join(__dirname, 'globals.css'), 'utf8');
    const rule = css.match(/\.col-resize-handle\s*\{[^}]*\}/);
    expect(rule?.[0], '.col-resize-handle kuralı bulunamadı').toBeTruthy();
    expect(rule![0]).toMatch(/touch-action:\s*none/);
  });

  it('tarama kör değil — pointermove kullanan dosya gerçekten görülüyor', () => {
    const MOVE = new RegExp(`addEventListener\\(\\s*['"]${'pointer'}move['"]`);
    const seen = srcFiles(SRC).filter(p => MOVE.test(stripTsComments(readFileSync(p, 'utf8'))));
    expect(seen.length, 'hiç pointermove bulunamadı — tarama bozuk').toBeGreaterThan(0);
  });
});
