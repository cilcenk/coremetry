// noFixedPxWidth — dar ekran denetimi D6.2 kapısı (v0.9.988)
//
// Ne çiviliyor: ekranı kaplayan bir katman (modal / çekmece) taşıyan bir
// dosyada, satır-içi bir stil nesnesi GÖRÜNTÜ ALANINA kelepçelenmemiş
// bir piksel genişliği bildiremez. `width: 720` telefonda diyaloğun
// üçte ikisini ekran dışında bırakır ve `place-items: center` onu iki
// yandan EŞİT taşırdığı için kapatma affordance'ı da görünmez olur —
// kullanıcı sayfada kilitli kalır.
//
// Neden beş kapı da bunu göremiyordu: `tsc` bir CSS değerine bakmaz,
// eslint satır-içi stile bakmaz, `undefinedCssRefs` yalnız TANIMSIZ
// `var(--x)` arar, `make audit`in CSS kuralı yok, jsdom düzen
// HESAPLAMAZ. "Ekranda taşıyor" ailesinin tamamı sessiz.
//
// KAPSAM NEDEN DOSYA SEVİYESİNDE: taşan öğe arka fonun KENDİSİ değil,
// KARDEŞİ — `position:'fixed'` arka fonda, `width: 720` diyalog
// `<div>`inde ve bunlar AYRI stil nesneleri. İlk taslak yalnız aynı
// nesneye bakıyordu ve mutasyon denemesinde (720 geri kondu) YEŞİL
// KALDI. Yani "aynı nesne" kapsamı, korumak istediği kusuru hiç
// görmüyordu. Dosya kapsamı bunu kapatıyor; bedeli, aynı dosyadaki
// alakasız küçük genişlikler — onları 320px eşiği eliyor.
//
// İKİ DEYİM DE KABUL: `width: 'min(720px, calc(100vw - 32px))'` ve
// `width: 720, maxWidth: '94vw'`. İkisi de aynı garantiyi veriyor
// (öğe görüntü alanını aşamaz); kapı MEKANİZMAYI çiviliyor, sözdizimini
// değil — depoda ikisi de canlı ve birini diğerine çevirmek bedava
// regresyon riski olurdu.
//
// Kapı MUTASYONLA doğrulandı: `HeatmapCellExemplars.tsx`e kelepçesiz
// `width: 720` geri konduğunda kırmızıya döndüğü görüldükten sonra
// gemiye alındı.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, resolve } from 'node:path';

const SRC = resolve(__dirname, '..');

function tsxFiles(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    if (e === 'node_modules') continue;
    const p = join(dir, e);
    if (statSync(p).isDirectory()) tsxFiles(p, out);
    else if (/\.tsx$/.test(e) && !/\.test\.tsx$/.test(e)) out.push(p);
  }
  return out;
}

// styleObjects — `style={{ … }}` bloklarını PARANTEZ DENGELEYEREK
// çıkarır. Regex'le "ilk `}}`ye kadar" almak yanlış olurdu: iç içe
// nesneler (`style={{ a: { b: 1 } }}`) bloğu erken kapatır ve kapı,
// gövdesini hiç görmediği bir paneli yeşil geçerdi.
export function styleObjects(src: string): { start: number; body: string }[] {
  const out: { start: number; body: string }[] = [];
  const MARK = 'style={{';
  let i = 0;
  while ((i = src.indexOf(MARK, i)) !== -1) {
    let depth = 0;
    let j = i + MARK.length - 2; // ilk `{`in üstünde
    for (; j < src.length; j++) {
      const ch = src[j];
      if (ch === '{') depth++;
      else if (ch === '}') {
        depth--;
        if (depth === 0) break;
      }
    }
    out.push({ start: i, body: src.slice(i, j + 1) });
    i = j + 1;
  }
  return out;
}

// Bir dosya "ekranı kaplayan katman" taşıyor mu: modal/çekmece z rungu.
// (`--z-modal`, `--z-modal-nested`, `--z-drawer`, `--z-drawer-panel`.)
const OVERLAY_FILE = /zIndex:\s*['"]?var\(--z-(modal|drawer)/;

// Değerin KENDİSİ kelepçeli mi.
const CLAMPED_VALUE = /min\(|calc\(|vw\b|%|var\(/;
// Kardeş `maxWidth` görüntü alanına kelepçeliyor mu.
const CLAMPING_MAX = /maxWidth:\s*[^,}\n]*(vw\b|%|calc\(|min\()/;

// TABAN: 320px — en dar telefon görüntü alanı. Bunun ALTINDAKİ sabit bir
// genişlik hiçbir cihazda taşamaz, dolayısıyla bulgu değildir. Kapı bir
// EŞİK kullanıyor, izin listesi değil: `CopilotChat`in 48px'lik yüzen
// başlatma butonu gibi öğeler gerekçeye göre kendiliğinden dışarıda
// kalıyor (izin listeleri bayatlar, eşik bayatlamaz).
const SAFE_MAX_PX = 320;

// SABİT SAYI OLMAYAN değerler (`width: computedWidth`) statik olarak
// yargılanamaz — kapının bilinen kör noktası. Bugün tek örneği
// `Sidebar.tsx` ve o zaten dar ekranda off-canvas moda geçip genişliği
// tamamen devre dışı bırakıyor.
function fixedPx(v: string): number | null {
  const m = v.match(/^(\d+(?:\.\d+)?)$/) ?? v.match(/^['"](\d+(?:\.\d+)?)px['"]$/);
  return m ? Number(m[1]) : null;
}

function lineOf(src: string, idx: number): number {
  return src.slice(0, idx).split('\n').length;
}

function findings(): string[] {
  const bad: string[] = [];
  for (const p of tsxFiles(SRC)) {
    const src = readFileSync(p, 'utf8');
    if (!OVERLAY_FILE.test(src)) continue;
    for (const { start, body } of styleObjects(src)) {
      const m = body.match(/(?:^|[\s,{])width:\s*([^,}\n]+)/);
      if (!m) continue;
      const v = m[1].trim().replace(/,$/, '');
      if (CLAMPED_VALUE.test(v)) continue;
      if (CLAMPING_MAX.test(body)) continue;
      const px = fixedPx(v);
      if (px === null || px <= SAFE_MAX_PX) continue;
      bad.push(`${p.slice(SRC.length + 1)}:${lineOf(src, start)} width: ${v}`);
    }
  }
  return bad;
}

describe('D6.2 — ekranı kaplayan katmanlar görüntü alanına kelepçeli', () => {
  it('kelepçesiz px genişlikli panel kalmadı', () => {
    expect(
      findings(),
      "sabit px genişlikli overlay paneli — width: 'min(Npx, calc(100vw - 32px))' ya da maxWidth: '94vw' ekle",
    ).toEqual([]);
  });

  // Kapının KÖR koşmadığının kanıtı (v0.9.980 dersi — yeşil bir kapı
  // "kural tutuyor" ya da "tarama hiçbir şey aramıyor" demektir; ikisi
  // ayrılmadan kapı sayılmaz).
  it('tarama gerçekten overlay dosyası görüyor (kör değil)', () => {
    const overlays = tsxFiles(SRC).filter(p => OVERLAY_FILE.test(readFileSync(p, 'utf8')));
    expect(overlays.length, 'hiç overlay dosyası bulunamadı — tarama bozuk').toBeGreaterThan(5);
  });

  it('paranteze duyarlı çıkarıcı iç içe nesnede erken kapanmıyor', () => {
    const src = `<div style={{ position: 'fixed', a: { b: 1 }, width: 720 }} />`;
    const [o] = styleObjects(src);
    expect(o.body).toContain('width: 720');
  });
});
