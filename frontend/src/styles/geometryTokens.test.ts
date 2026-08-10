// geometryTokens — Dalga 4 / mK4 + mK5 kapısı (v0.9.909)
//
// Ne çiviliyor: spacing (`--sp-1..8`) ve font-size (`--fs-3xs..xl`)
// merdivenlerinin TANIMLI ve ARTAN olduğu + `components/ui/` içindeki
// atomların merdivene denk gelen ham sayı yazmadığı.
//
// KAPSAM, bilinçli olarak dar (operatör kararı): tanım + `components/ui/`
// içi kullanım. Sayfa süpürmesi PLAN DIŞI — globals.css'te 96 farklı
// padding değeri var ve hepsini tek dalgada normalize etmek görsel bir
// yeniden tasarım olurdu, mekanik tutarlılık işi değil. Bu kapı o işi
// yapılmış gibi göstermesin diye sınırı burada yazılı: `components/ui/`
// dışına BAKMIYOR.
//
// Neden merdiven ölçülen dağılımdan türetildi: uydurulmuş bir ölçek
// deponun ritmine oturmaz, ilk çağrı yerinde terk edilir ve geriye iki
// paralel ölçek kalır — token'sız olmaktan daha kötü bir durum.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { resolve, join } from 'node:path';

const STYLES = resolve(__dirname);
const UI = resolve(__dirname, '..', 'components', 'ui');
const CSS = readFileSync(join(STYLES, 'globals.css'), 'utf8');

const SP = ['--sp-1', '--sp-2', '--sp-3', '--sp-4', '--sp-5', '--sp-6', '--sp-7', '--sp-8'];
const FS = ['--fs-3xs', '--fs-2xs', '--fs-xs', '--fs-sm', '--fs-md', '--fs-lg', '--fs-xl'];

// `-` regex'te kelime sınırı DEĞİL: `\b--sp-1\b` `--sp-10`un içinde de
// eşleşirdi. Sınır `(?![\w-])` olmak zorunda (v0.9.894 dersi).
function px(name: string): number | null {
  const m = new RegExp(`${name}(?![\\w-])\\s*:\\s*([\\d.]+)px`).exec(CSS);
  return m ? Number(m[1]) : null;
}

describe('mK4/mK5 — geometri merdivenleri', () => {
  it.each([['spacing', SP], ['font-size', FS]] as const)('%s merdiveni tanımlı ve ARTAN', (_label, rungs) => {
    const vals = rungs.map(r => {
      const v = px(r);
      expect(v, `${r} tanımlı değil`).not.toBeNull();
      return v!;
    });
    expect(vals).toEqual([...vals].sort((a, b) => a - b));
    expect(new Set(vals).size, 'iki rung aynı değerde — biri ölü token').toBe(vals.length);
  });
});

describe('components/ui — atomlarda merdivene denk ham sayı yok', () => {
  const files = readdirSync(UI).filter(f => f.endsWith('.tsx') && !f.includes('.test.'));

  // Yalnız GEOMETRİ özellikleri. `width`/`height` kasten dışarıda: bir
  // ikonun 14px'i ya da bir etiketin 60px sütun genişliği boşluk ölçeği
  // değil, o yüzeyin kendi ölçüsü.
  const PROP = '(fontSize|gap|padding|paddingTop|paddingBottom|paddingLeft|paddingRight'
    + '|margin|marginTop|marginBottom|marginLeft|marginRight)';

  it.each(files)('%s', file => {
    const spVals = new Set(SP.map(px));
    const fsVals = new Set(FS.map(px));
    const src = readFileSync(join(UI, file), 'utf8')
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .split('\n')
      .map(l => l.replace(/^\s*\/\/.*$/, ''));

    const bad: string[] = [];
    src.forEach((line, i) => {
      const re = new RegExp(`${PROP}:\\s*(\\d+)\\b`, 'g');
      let m: RegExpExecArray | null;
      while ((m = re.exec(line))) {
        const n = Number(m[2]);
        const ladder = m[1] === 'fontSize' ? fsVals : spVals;
        if (ladder.has(n)) bad.push(`${file}:${i + 1} ${m[1]}: ${n} → merdiven rung'u var`);
      }
    });
    expect(bad).toEqual([]);
  });
});
