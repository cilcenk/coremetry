import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join } from 'node:path';

// testEnvContract — v0.9.1359. CI KIRMIZI, LOKAL YEŞİL.
//
// `useUrlRange.test.ts`'in üç testi CI'da düştü, burada geçiyordu. Sebep
// sızıntı ya da flake DEĞİL, ÇALIŞMA ZAMANI SÜRÜMÜ:
//   · lokal Node v25 → `sessionStorage` YERLEŞİK global (ölçüldü:
//     `node -e "typeof sessionStorage"` → "object")
//   · CI Node 22 (`.github/workflows/ci.yml:23`) → YOK
// `vitest.config.ts:27` ortamı bilinçli `node` (2000+ saf test jsdom
// bedelini ödemesin; şerhi dosya başına opt-in diyor). O yüzden storage
// kullanan bir test, jsdom istemediği sürece Node'un o gün ne taşıdığına
// bağlı kalıyor — yani lokalde HAKKIYLA değil, KAZAYLA geçiyor.
//
// Arızanın en sinsi tarafı: `lib/storage.ts` erişimi try/catch'e sarıyor
// (özel mod / iframe için doğru), bu yüzden eksik global ReferenceError
// vermiyor — yazımlar sessizce no-op oluyor, okumalar null dönüyor. Üç
// test düştü, "null bekleyen" dördüncü test GEÇTİ. Yani belirti de yalan
// söylüyordu.
//
// KURAL: bir test dosyası `sessionStorage`/`localStorage`'ı KULLANIYORSA
// `// @vitest-environment jsdom` docblock'unu taşımak zorunda. jsdom onları
// Node sürümünden bağımsız sağlar.
//
// KAPSAM — neden bu gate "geçiyor" diye kandırılamaz: yalnız KULLANIMI
// arıyoruz, ANMAYI değil. Kaynak-tarayan testler (pageShellAdoption,
// serviceHref, columnLayoutSig…) bu adları okudukları DOSYA İÇERİĞİNDE
// taşıyor ama global'i çalıştırmıyor; onları `readFileSync` varlığıyla
// değil, adın ÇAĞRI biçiminde (`X.getItem`, `X.setItem`, `X.clear`,
// `X.removeItem`) geçmesiyle ayırıyoruz.
const SRC = resolve(__dirname, '..');

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.test\.tsx?$/.test(p)) out.push(p);
  }
  return out;
}

// `sessionStorage.getItem(` gibi ÇAĞRI biçimleri. Salt anma (bir dizede
// ya da yorumda geçen ad) eşleşmez.
const USES_STORAGE = /\b(session|local)Storage\s*\.\s*(get|set|remove)Item\b|\b(session|local)Storage\s*\.\s*clear\b/;

// stripComments — pageShellAdoption.test.ts:58'in karakter makinesi.
// ⚠ İlk yazımımda YOKTU ve gate KENDİNİ yakaladı: bu dosyanın yorumları
// örnek olarak `sessionStorage.getItem(` yazıyor. Naif bir `//.*$` regex'i
// de yetmiyor — depoda İKİ KEZ ısırmış (yorum İÇİNDEKİ `/*` naif soyucuyu
// yutuyor; bir satırda `//`den ÖNCE gerçek kod varsa regex onu da siliyor).
function stripComments(src: string): string {
  let out = '';
  let mode: 'code' | 'line' | 'block' = 'code';
  for (let i = 0; i < src.length; i++) {
    const c = src[i], n = src[i + 1];
    if (mode === 'code') {
      if (c === '/' && n === '/') { mode = 'line'; i++; continue; }
      if (c === '/' && n === '*') { mode = 'block'; i++; continue; }
      out += c;
    } else if (mode === 'line') {
      if (c === '\n') { mode = 'code'; out += c; }
    } else {
      if (c === '*' && n === '/') { mode = 'code'; i++; }
    }
  }
  return out;
}

describe('test ortamı sözleşmesi (v0.9.1359)', () => {
  it('storage global.i KULLANAN her test dosyası jsdom ilan eder', () => {
    const offenders: string[] = [];
    for (const abs of walk(SRC)) {
      const src = readFileSync(abs, 'utf8');
      // Yorumlar SOYULUR — bu dosyanın kendi şerhi örnek olarak
      // `<storage>.getItem(` yazıyor ve soyulmazsa gate KENDİNİ yakalar
      // (ilk yazımda yakaladı). Muafiyet yerine soyma: muaf bir gate,
      // kuralını ihlal edebilen tek dosyayı serbest bırakır.
      if (!USES_STORAGE.test(stripComments(src))) continue;
      // Docblock dosyanın BAŞINDA olmak zorunda (vitest ilk yorum
      // bloğunu okur), o yüzden ilk 12 satıra bakıyoruz.
      if (src.split('\n').slice(0, 12).some(l => l.includes('@vitest-environment jsdom'))) continue;
      offenders.push(abs.slice(SRC.length + 1));
    }
    expect(offenders).toEqual([]);
  });

  it('yüklem ANMAYI değil KULLANIMI yakalar — negatif kontrol', () => {
    // ⚠ Örnekler PARÇADAN kuruluyor, literal yazılmıyor. İlk yazımda
    // literaldiler ve gate KENDİNİ yakaladı — bu dosyanın kaynağı
    // `sessionStorage.setItem` dizisini taşıdığı için. Doğru çare
    // kendine muafiyet yazmak DEĞİL (muaf bir gate, kendi kuralını
    // ihlal edebilen tek dosyayı serbest bırakır); literali kaldırmak.
    const S = 'session' + 'Storage';
    const L = 'local' + 'Storage';
    // Salt anma → ısırmamalı, yoksa çare "açıklamayı silmek" olur.
    expect(USES_STORAGE.test(`const pat = "${L}";`)).toBe(false);
    expect(USES_STORAGE.test(`// ${L}'da tutuluyor`)).toBe(false);
    // Gerçek kullanım → yakalamalı.
    expect(USES_STORAGE.test(`${S}.setItem(k, v)`)).toBe(true);
    expect(USES_STORAGE.test(`${L}.getItem('x')`)).toBe(true);
    expect(USES_STORAGE.test(`${S}.clear()`)).toBe(true);
  });

  it('yürüyüş gerçekten dosya buluyor — boş küme tuzağı', () => {
    // Sıfır dosya tarayan bir gate sessizce yeşil olurdu.
    expect(walk(SRC).length).toBeGreaterThan(200);
  });
});
