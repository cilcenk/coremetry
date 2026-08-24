import { describe, it, expect } from 'vitest';
import { readdirSync, statSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

// sectionAtomsGate — v0.9.1364. Bir detay-bölümü atomunun İKİNCİ bir yerel
// tanımının doğmasını engelleyen kaynak taraması.
//
// NEDEN VAR. Bu depodaki ölçülmüş kopya aileleri (`Stat`/`HeaderStat` ×8,
// `Field` ×7, ikincil buton ×4 yazım) tek bir mekanizmayla doğdu: birileri
// "bu zaten var mı" diye baktı, BULAMADI ve yenisini yazdı. `tsc` iki ayrı
// dosyadaki iki ayrı `function PanelTitle` için tamamen sessiz — ikisi de
// geçerli. `eslint`in böyle bir kuralı yok. `make audit` frontend okumuyor.
// Yani bu kapıyı kaldırırsanız kopyayı görecek hiçbir şey kalmaz.
//
// ── DERS 1: TEK YAZIM TARAYAN KAPI İKİZİ MUAF TUTAR (v0.9.1285/1286) ─────
//
// `function X(` bir bileşeni tanımlamanın YALNIZCA BİR yolu. `const X = (`
// ve `const X: React.FC` de aynı şeyi yapar ve tek-yazımlı bir grep onları
// sessizce geçirir. Bu yüzden dedektör bir yazım LİSTESİ kullanıyor ve
// liste kendi meta-testine sahip: yeni bir yazım eklendiğinde onun EKSİKLİĞİ
// görünür olsun diye. Meta-test olmadan liste kendi düzyazısını doğrular.
//
// ── DERS 2: VARLIK DEĞİL, YOKLUK ÖLÇÜLÜR (v0.9.1334) ────────────────────
//
// "atom barrel'dan import EDİLİYOR mu" diye soran bir kapı,
// `import { X } from '@/components/ui'` satırını yazıp yanına kendi
// kopyasını koyan bir dosyayla tatmin olur. Bu kapı tersini ve daha
// güçlüsünü soruyor: atom, EVİNİN DIŞINDA TANIMLANMIYOR. Bunu sağlarken
// kopya yazmanın bir yolu yok, çünkü kopya yazmak tanımlamayı gerektirir.
const SRC = resolve(__dirname, '../..');

/**
 * Tek evi olan bölüm atomları. `home` deponun köküne (src/) göre.
 *
 * Liste büyüdükçe kapı kendiliğinden genişler; yeni bir atom terfi
 * ettiğinde tek satır eklenir.
 */
const ATOMS: { name: string; home: string; since: string }[] = [
  {
    name: 'SectionUnavailable',
    home: 'components/ui/SectionUnavailable.tsx',
    since: 'v0.9.1364 — endpoints/detailSections + slowqueries/StmtDetailDrawer '
      + 'nüshaları BAYT BAYT aynıydı, tek eve çekildi.',
  },
];

/**
 * Bir ismin BİLEŞEN OLARAK TANIMLANDIĞI her yazım.
 *
 * `import`/`export {}` satırları kasten EŞLEŞMEZ: onlar atomu kullanmanın
 * yolu, kopyalamanın değil. Eşleşmesi gereken şey bir GÖVDE bağlamak.
 */
// ── DERS 3: AYNALI LİSTE TEK BİR SED'LE BİRLİKTE KÜÇÜLÜR (v0.9.1358) ────
//
// İlk taslakta örnekler AYRI bir `samples` dizisiydi ve "iki liste senkron
// mu" diye soran bir meta-test vardı. MUTASYONLA ÖLÇÜLDÜ: bir yazımı
// SPELLINGS'ten silen tek bir düzenleme örneği de sildiği için kapı YEŞİL
// kaldı ve kapsama sessizce daraldı. Bu yüzden örnek artık girdinin KENDİ
// alanı — tek gövde, aynalanacak ikiz yok. Kardeş kapı
// (`lib/traceLogsLinkGate.test.ts`) bugün hâlâ ayna desenini kullanıyor;
// aynı mutasyon orada da ısırmaz.
//
// Silmeyi GÖRÜNÜR kılan şey ise `PINNED_IDS`: bir yazımı kaldırmak artık
// İKİ ayrı yerde bilinçli düzenleme gerektirir, tek bir kaydırma değil.
const SPELLINGS: { id: string; re: (n: string) => RegExp; what: string; sample: string }[] = [
  { id: 'fn', re: n => new RegExp(`function\\s+${n}\\s*[(<]`), what: 'function bildirimi (function X( / function X<T>()',
    sample: 'function Zed({ a }: P) { return null; }' },
  { id: 'const-arrow', re: n => new RegExp(`const\\s+${n}\\s*=\\s*[({]`), what: 'const ok-fonksiyon (const X = (props) => …)',
    sample: 'const Zed = ({ a }: P) => null;' },
  { id: 'const-typed', re: n => new RegExp(`const\\s+${n}\\s*:\\s*`), what: 'tipli const (const X: React.FC = …)',
    sample: 'const Zed: React.FC<P> = () => null;' },
  { id: 'let', re: n => new RegExp(`let\\s+${n}\\s*=`), what: 'let ataması (let X = …)',
    sample: 'let Zed = () => null;' },
  { id: 'var', re: n => new RegExp(`var\\s+${n}\\s*=`), what: 'var ataması (var X = …)',
    sample: 'var Zed = () => null;' },
  { id: 'class', re: n => new RegExp(`class\\s+${n}\\s`), what: 'sınıf bileşeni (class X extends …)',
    sample: 'class Zed extends React.Component {}' },
];

/** Kapsama çıpası — bir yazımı düşürmek bu listeyi de düzenlemeyi gerektirir. */
const PINNED_IDS = ['fn', 'const-arrow', 'const-typed', 'let', 'var', 'class'] as const;

/** Bir kaynak metninde atomun tanımlandığı yazımlar. */
function definitionsIn(src: string, name: string): string[] {
  const body = src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n');
  return SPELLINGS.filter(s => s.re(name).test(body)).map(s => s.what);
}

const TEST_MARK = '.' + 'test' + '.';
function sourceFiles(dir: string, rel = ''): string[] {
  const out: string[] = [];
  for (const e of readdirSync(dir)) {
    const abs = join(dir, e);
    const r = rel ? `${rel}/${e}` : e;
    if (statSync(abs).isDirectory()) { out.push(...sourceFiles(abs, r)); continue; }
    if (!/\.tsx?$/.test(e) || e.includes(TEST_MARK)) continue;
    out.push(r);
  }
  return out;
}

describe('sectionAtomsGate', () => {
  const files = sourceFiles(SRC);

  it('tarama gerçekten kaynak buluyor (kapının kendisi boş küme değil)', () => {
    expect(files.length).toBeGreaterThan(200);
  });

  for (const atom of ATOMS) {
    it(`${atom.name} yalnız ${atom.home} içinde TANIMLANIYOR`, () => {
      expect(files).toContain(atom.home);
      const strays: string[] = [];
      for (const f of files) {
        if (f === atom.home) continue;
        const hits = definitionsIn(readFileSync(join(SRC, f), 'utf8'), atom.name);
        for (const h of hits) strays.push(`${f} — ${h}`);
      }
      expect(strays, `${atom.name} kopyası (${atom.since}):\n  ${strays.join('\n  ')}`)
        .toEqual([]);
    });

    it(`${atom.name} evinde GERÇEKTEN tanımlı (ev satırı bayat değil)`, () => {
      const hits = definitionsIn(readFileSync(join(SRC, atom.home), 'utf8'), atom.name);
      expect(hits.length).toBeGreaterThan(0);
    });
  }

  // META-TEST — yazım listesinin ISIRDIĞINI kanıtlar. Bunsuz liste
  // kendi düzyazısını doğrular: hiçbiri eşleşmeyen bir regex kümesi de
  // "kopya yok" der.
  describe('yazım listesi ısırıyor', () => {
    it('kapsama çıpası tutuyor — hiçbir yazım sessizce düşürülmedi', () => {
      expect(SPELLINGS.map(s => s.id).sort()).toEqual([...PINNED_IDS].sort());
    });
    for (const s of SPELLINGS) {
      it(`yakalar: ${s.what}`, () => {
        expect(definitionsIn(s.sample, 'Zed')).toContain(s.what);
      });
    }
    it('generic function bildirimini de yakalar', () => {
      expect(definitionsIn('function Zed<T>(p: T) { return null; }', 'Zed').length).toBeGreaterThan(0);
    });
    // NEGATİF KONTROL — import/export/kullanım satırları TANIM DEĞİLDİR.
    // Bu olmadan kapı her tüketiciyi kopya sanardı ve kimse onu barrel'dan
    // import edemezdi.
    it('import / export / JSX kullanımını TANIM SAYMAZ', () => {
      for (const line of [
        `import { Zed } from '@/components/ui';`,
        `export { Zed } from './Zed';`,
        `return <Zed what="Trend" />;`,
        `{!x && <Zed what="Distribution" />}`,
      ]) {
        expect(definitionsIn(line, 'Zed'), line).toEqual([]);
      }
    });
    it('yorumdaki tanımı SAYMAZ (yorum soyma çalışıyor)', () => {
      expect(definitionsIn('// function Zed() {}\n/* const Zed = () => null; */', 'Zed')).toEqual([]);
    });
    it('başka bir ismi Zed sanmaz', () => {
      expect(definitionsIn('function ZedExtra() {}\nfunction NotZed() {}', 'Zed')).toEqual([]);
    });
  });
});
