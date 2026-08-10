// radiusTokens — Dalga 4 / MK1 kapısı (v0.9.908)
//
// Ne çiviliyor: `globals.css`te köşe yarıçapının LİTERAL px ile
// yazılmadığı ve rung merdiveninin her temada artan sırada durduğu.
//
// Neden önemli: redhat teması "squarer corners" kimliğini YALNIZ
// `--radius-*` token'ları üzerinden uyguluyor (PF 2/3/4/8 vs Primer
// 4/6/8/12). Bir kurala `border-radius: 8px` yazıldığı anda o yüzey
// üç temada da Primer yuvarlaklığında kalır — tema seçimi o yüzeyde
// sessizce ETKİSİZ olur. Ekranda hiçbir şey "bozulmaz", sadece marka
// kimliği bir kutuda çalışmaz; tsc/eslint/audit üçü de görmez.
//
// KAPSAM DIŞI, bilinçli: TSX satır-içi `borderRadius` (557 site / 180
// dosya). Dalga 4 buna girmiyor, dolayısıyla satır-içi stille çizilmiş
// yüzeylerde redhat köşe kimliği HÂLÂ çalışmıyor. Bu bir eksiklik
// olarak kayda geçti, kapı onu yanlışlıkla "kapandı" göstermesin diye
// burada yazılı.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const CSS = readFileSync(resolve(__dirname, 'globals.css'), 'utf8');

function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '));
}

const RUNGS = ['--radius-xs', '--radius-sm', '--radius', '--radius-lg'] as const;

// Rung DEĞERİ okuma: `--radius:` ile `--radius-sm:` karışmasın diye
// ad sınırı gerekiyor. `-` regex'te kelime sınırı olmadığından `\b`
// KULLANILAMAZ (v0.9.894 dersi) — `(?![\w-])` şart.
function rungPx(block: string, name: string): number | null {
  const m = new RegExp(`${name}(?![\\w-])\\s*:\\s*(\\d+)px`).exec(block);
  return m ? Number(m[1]) : null;
}

describe('MK1 — radius rung merdiveni', () => {
  const light = CSS.indexOf('[data-theme="light"]');
  const redhatStart = CSS.indexOf('[data-theme="redhat"]');
  const themes: Record<string, string> = {
    dark: CSS.slice(CSS.indexOf(':root'), light),
    redhat: CSS.slice(redhatStart, CSS.indexOf('[data-theme="redhat"] body')),
  };

  it('dört rung da :root ve redhat temasında tanımlı', () => {
    for (const t of RUNGS) {
      expect(rungPx(themes.dark, t), `${t} :root'ta yok`).not.toBeNull();
      expect(rungPx(themes.redhat, t), `${t} redhat'te yok`).not.toBeNull();
    }
  });

  it('merdiven her temada ARTAN (xs < sm < base < lg)', () => {
    for (const [name, block] of Object.entries(themes)) {
      const vals = RUNGS.map(r => rungPx(block, r)!);
      expect(vals, `${name} merdiveni bozuk: ${vals.join(' < ')}`)
        .toEqual([...vals].sort((a, b) => a - b));
      expect(new Set(vals).size, `${name}: iki rung aynı değerde — biri ölü`).toBe(vals.length);
    }
  });

  it('redhat her rungda Primer\'dan KARE (kimliğin tek uygulama noktası)', () => {
    for (const r of RUNGS) {
      expect(rungPx(themes.redhat, r)!, `${r} redhat'te daha kare değil`)
        .toBeLessThan(rungPx(themes.dark, r)!);
    }
  });

  it('rung değerlerine denk gelen px literali kural gövdelerinde kalmadı', () => {
    // Yalnız TAM eşleşen tek değerler. 3px/5px/10px/50%/999px gibi
    // rung'a denk gelmeyenler bilinçli olarak literal kalıyor: bunları
    // en yakın rung'a çekmek dark/light'ta köşeleri BÜYÜTÜRDÜ.
    const swept = new Set(RUNGS.map(r => `${rungPx(themes.dark, r)}px`));
    const bad: string[] = [];
    stripComments(CSS).split('\n').forEach((l, i) => {
      const m = /border-radius:\s*([^;}\n]+)/.exec(l);
      if (m && swept.has(m[1].trim())) bad.push(`globals.css:${i + 1} ${l.trim().slice(0, 90)}`);
    });
    expect(bad).toEqual([]);
  });
});
