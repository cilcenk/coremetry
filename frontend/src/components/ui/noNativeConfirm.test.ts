// Kapı A′ — native `confirm()` YASAK (v0.9.1009, etkileşim denetimi M6b).
//
// ORİJİNAL DURUM: `window.confirm` bu deponun tartışmasız ev deseniydi —
// 25 dosyada 27 çağrı. Hiçbiri "yanlış" yazılmamıştı; sadece doğru olanı
// yoktu. Atom geldiğine göre (v0.9.1008) tek eksik, geri dönüşün
// KAPATILMASI: bir sonraki yazar en kısa yolu seçerse desen birkaç
// sürümde yeniden doğar ve 27 çağrı geri gelir.
//
// Kapı iki kurallı:
//   1. `window.confirm(` — HİÇBİR YERDE. İstisna listesi yok, çünkü
//      meşru bir kullanımı yok: tema dışı, OS dilinde, escLayer'a
//      görünmez.
//   2. Çıplak `confirm(` — yalnız `useConfirm()` çağıran dosyalarda
//      (yani gölgelenen hook). Hook'suz bir dosyada `confirm(`
//      GLOBALDIR, tanım gereği.
//
// Üçüncü kural gölgelemenin kendi tuzağını kapatıyor: hook `confirm`
// adını aldığı için `if (!confirm({…}))` TİP AÇISINDAN GEÇERLİDİR ve
// bir Promise'i boolean gibi okur — Promise her zaman truthy olduğu
// için diyalog açılır ama karar HİÇ beklenmez ve `!truthy === false`
// olduğu için yıkıcı yol SESSİZCE koşar. `tsc` bunu göremez. Bu yüzden
// üretim kodunda her `confirm(` çağrısı `await` ile başlamak zorunda.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { stripTsComments } from '../../styles/zLayers.test';

const SRC = resolve(__dirname, '..', '..');

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (e.name === 'node_modules' || e.name === 'dist') continue;
    const p = join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (/\.tsx?$/.test(e.name)) out.push(p);
  }
  return out;
}

// Yorumlar SOYULUYOR — bu dosyanın kendi düzyazısı, ConfirmDialog'un
// gerekçe bloğu ve `lib/queries/*` içindeki açıklamalar hepsi dizeyi
// anıyor. Soymadan yazılmış bir yasak taraması, yasağı AÇIKLAYAN her
// yorumu ihlal sanardı (bu depoda yedi kez ısıran tuzak).
const FILES = walk(SRC).map(p => ({
  rel: p.slice(SRC.length + 1),
  src: stripTsComments(readFileSync(p, 'utf8')),
}));

describe('K6 — native confirm() geri gelemez', () => {
  it('tarama gerçekten dosya görüyor', () => {
    expect(FILES.length).toBeGreaterThan(300);
    // Atom hâlâ yerinde mi: kapı, yerine geçecek şey olmadan anlamsız.
    expect(FILES.some(f => f.rel === 'components/ui/ConfirmDialog.tsx')).toBe(true);
  });

  it('window.confirm HİÇBİR yerde yok — istisna listesi de yok', () => {
    const bad = FILES.filter(f => /(?<![\w.])window\.confirm\s*\(/.test(f.src)).map(f => f.rel);
    expect(bad, 'tarayıcı diyaloğu tema dışı, OS dilinde ve escLayer’a görünmez').toEqual([]);
  });

  it('çıplak confirm( yalnız useConfirm() çağıran dosyalarda', () => {
    // Test dosyaları kapsam DIŞI ve bu bilinçli: bir test operatöre
    // diyalog GÖSTERMEZ, ve kardeş kapı `destructiveConfirm.test.ts`
    // kuralı REGEX LİTERALİ olarak yazmak zorunda (`/confirm\s*\(/`).
    // Kuralı ifade eden metni ihlal saymak, bu depoda yedi kez ısıran
    // "kapı kendi düzyazısını kural sanıyor" tuzağının aynısı olurdu.
    // `window.confirm` yasağı (yukarıda) test dosyalarını da kapsıyor.
    const bad = FILES
      .filter(f => !/\.test\.tsx?$/.test(f.rel))
      .filter(f => /(?<![\w.])confirm\s*\(/.test(f.src) && !/useConfirm\s*\(/.test(f.src))
      .map(f => f.rel);
    expect(bad, 'hook’suz bir dosyada `confirm(` tanım gereği GLOBALDIR').toEqual([]);
  });

  it('üretim kodunda her confirm( çağrısı AWAIT ediliyor', () => {
    // Gölgelemenin tuzağı: `if (!confirm({…}))` derlenir, Promise
    // truthy'dir, karar beklenmez ve yıkıcı yol sessizce koşar.
    const bad: string[] = [];
    for (const f of FILES) {
      if (/\.test\.tsx?$/.test(f.rel)) continue;      // sözleşme testi `void … .then()` kullanıyor
      if (f.rel === 'components/ui/ConfirmDialog.tsx') continue;  // atomun kendi tanımı
      const lines = f.src.split('\n');
      lines.forEach((l, i) => {
        for (const m of l.matchAll(/(?<![\w.])confirm\s*\(/g)) {
          const before = l.slice(0, m.index).trimEnd();
          if (!/\bawait$/.test(before)) bad.push(`${f.rel}:${i + 1}`);
        }
      });
    }
    expect(bad, 'await’siz bir confirm() kararı BEKLEMEDEN yıkıcı yolu koşturur').toEqual([]);
  });
});
