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

  // v0.9.1365 — ONDALIK BASAMAK KUSURU (regresyon).
  //
  // Sayı deseni `(\d+)\b` idi. `fontSize: 10.5` üzerinde `\d+` açgözlü
  // olarak "10"u alıyor, `\b` de "0" ile "." arasında SAĞLANIYOR — yani
  // 10.5px, merdivenin 10px rung'u (`--fs-2xs`) sanılıp işaretleniyordu.
  // Oysa 10.5 merdivende YOK; kapının kendi cümlesi "merdivene DENK ham
  // sayı". Yanlış pozitifin bedeli görünürdü: bir atomu ui/'ye taşımanın
  // tek yolu 10.5'i 10'a yuvarlamak, yani kapıyı memnun etmek için
  // GÖRSEL bir değişiklik yapmak olurdu.
  //
  // Düzeltme ondalık kısmı da tüketiyor. Yön güvenli: daha kesin ayrıştırma
  // eşleşmeyi yalnız AZALTABİLİR (10.5 artık 10 okunmuyor); tam sayılar
  // (`11`, `11.0`) aynen yakalanmaya devam ediyor — aşağıdaki tabloda ikisi
  // de var.
  const NUM = '(\\d+(?:\\.\\d+)?)';

  /** Bir kaynak metnindeki merdivene DENK ham geometri sayıları. */
  function ladderHits(text: string, spVals: Set<number | null>, fsVals: Set<number | null>): string[] {
    const out: string[] = [];
    text
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .split('\n')
      .map(l => l.replace(/^\s*\/\/.*$/, ''))
      .forEach((line, i) => {
        const re = new RegExp(`${PROP}:\\s*${NUM}`, 'g');
        let m: RegExpExecArray | null;
        while ((m = re.exec(line))) {
          const n = Number(m[2]);
          const ladder = m[1] === 'fontSize' ? fsVals : spVals;
          if (ladder.has(n)) out.push(`${i + 1} ${m[1]}: ${m[2]} → merdiven rung'u var`);
        }
      });
    return out;
  }

  it.each(files)('%s', file => {
    const spVals = new Set(SP.map(px));
    const fsVals = new Set(FS.map(px));
    const bad = ladderHits(readFileSync(join(UI, file), 'utf8'), spVals, fsVals)
      .map(h => `${file}:${h}`);
    expect(bad).toEqual([]);
  });

  describe('sayı ayrıştırma — v0.9.1365 ondalık regresyonu', () => {
    const sp = new Set<number | null>([2, 4, 6, 8, 10, 12, 16, 24]);
    const fs = new Set<number | null>([9, 10, 11, 12, 13, 14, 18]);
    const hits = (t: string) => ladderHits(t, sp, fs);
    it.each([
      // [kaynak, işaretlenmeli mi, neden]
      ['fontSize: 10.5,', false, '10.5 merdivende YOK — kusurun ta kendisi'],
      ['fontSize: 11.5,', false, '11.5 merdivende yok'],
      ['fontSize: 10,', true, 'tam sayı 10 = --fs-2xs'],
      ['fontSize: 11,', true, 'tam sayı 11 = --fs-xs'],
      ['fontSize: 11.0,', true, '11.0 sayısal olarak 11'],
      ['gap: 8,', true, '8 = --sp-4'],
      ['gap: 8.5,', false, '8.5 merdivende yok'],
      ['padding: 80,', false, '80 merdivende yok — ön-ek eşleşmesi olmamalı'],
      ['gap: 7,', false, '7 merdivende yok'],
      ['width: 11,', false, 'width geometri listesinde değil (bilinçli)'],
    ])('%s', (src, flagged) => {
      expect(hits(src).length > 0).toBe(flagged);
    });
  });
});
