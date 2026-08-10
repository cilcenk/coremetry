import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';

// formSubmit.test.ts — v0.9.881 (tutarlılık denetimi Dalga 2, T1).
//
// KAPATTIĞI KAPI BOŞLUĞU — sessiz submit kaybı. HTML'de bir formun içindeki
// argümansız `<button>` VARSAYILAN OLARAK submit'tir; paylaşılan Button
// atomunun varsayılanı ise `type="button"` (Button.tsx, bilinçli seçim:
// atomun çoğu kullanımı form dışında). Yani bir `<button>` → `<Button>`
// göçünde `type="submit"` yazmayı unutmak formu Enter'a VE tıka ölü
// bırakır, ve bunu HİÇBİR kapı görmez: `tsc` temiz, `eslint` temiz,
// `make audit` temiz, ekranda buton hâlâ duruyor.
//
// Dalga 2 bu riski gerçek hâle getirdi: ~60 çıplak buton atoma taşınıyor
// ve depodaki tek çıplak submit — PublicStatus abone formu — o listede.
// Atom-seviyesi sözleşme Button.contract.test.tsx'te; bu dosya ÇAĞIRAN
// tarafını çiviliyor, yani "göç eden kişi type'ı hatırladı mı".
//
// İMZA DAR SEÇİLDİ: yalnız gerçek `<form … onSubmit>` ELEMENTLERİ. İlk
// taslak `onSubmit=` arıyordu ve Logs.tsx'i yakalıyordu — orada onSubmit
// bir arama bileşeninin PROP'u, ortada form yok. Yanlış pozitif bir kapıyı
// izin listesine boğar, izin listesi de kapıyı anlamsızlaştırır.
//
// Ölçülmüş taban (2026-08-10): 30 dosyada gerçek `<form onSubmit>` var,
// 29'unda submit denetimi mevcut. Tek istisna aşağıda, kimliğiyle.

const SRC = resolve(__dirname, '..');

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.tsx$/.test(p)) out.push(p);
  }
  return out;
}

const blankComments = (s: string) =>
  s
    .replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '))
    .split('\n')
    .map(l => l.replace(/\/\/.*$/, ''))
    .join('\n');

// Her girdi BULGU KİMLİĞİ taşır ve bulgunun ömrü kadar yaşar. Kimliksiz
// bir muafiyet, testin kendisini anlamsızlaştırır.
const ALLOWED = new Map<string, string>([
  // Pager "sayfaya git" kutusu: tek input, submit denetimi YOK — Enter
  // (form submit) ve blur AYNI submit() çağrısına gidiyor. Görünür bir
  // buton burada yalnız gürültü olurdu. 2026-08-10'da koddan doğrulandı.
  ['components/Pager.tsx', 'deliberate: Enter + blur both call submit(), no visible control'],
]);

describe('form submit reachability', () => {
  it('every <form onSubmit> ships a submit control', () => {
    const offenders: string[] = [];
    for (const p of walk(SRC)) {
      const rel = relative(SRC, p).split('\\').join('/');
      const src = blankComments(readFileSync(p, 'utf8'));
      // Attribute'lar çok satıra yayılabiliyor, o yüzden `<form` ile
      // kapanış `>` arası taranıyor.
      if (!/<form\b[^>]*\bonSubmit\b/s.test(src)) continue;
      if (ALLOWED.has(rel)) continue;
      // İki geçerli biçim: form içindeki `type="submit"` denetimi ya da
      // formun DIŞINDA duran, `form="<id>"` ile ona bağlanmış bir buton
      // (Modal footer deseni — depoda dokuz yerde kullanılıyor).
      const hasSubmit = /type="submit"/.test(src) || /\bform="[^"]+"/.test(src);
      if (!hasSubmit) offenders.push(rel);
    }
    expect(offenders.join('\n'),
      'a <button> migrated to <Button> loses submit unless type="submit" is carried over').toBe('');
  });

  // Dalga 2'nin göç ettirdiği TEK çıplak submit. Ayrı bir test olarak
  // duruyor çünkü yukarıdaki kural izin listesiyle gevşetilebilir; bu
  // site gevşetilemez — düşerse abone formu sessizce ölür.
  it('PublicStatus subscribe form keeps its submit control', () => {
    const src = blankComments(
      readFileSync(join(SRC, 'pages/PublicStatus.tsx'), 'utf8'),
    );
    expect(/<form\b[^>]*\bonSubmit\b/s.test(src)).toBe(true);
    expect(/type="submit"/.test(src)).toBe(true);
  });
});
