// pageShellAdoption — DALGA 9 kaynak-kapısı (v0.9.991)
//
// Ne çiviliyor: `id="content"` YALNIZ `components/ui/PageShell.tsx`te
// yazılabilir. Atom geldiğinde elle yazan 43 dosya vardı; aşağıdaki liste
// o günün DONDURULMUŞ hâlinden arta kalandır ve YALNIZ KÜÇÜLEBİLİR.
// Seyir: 43 (991) → 36 (992) → 24 (993) → 13 (994) → 2 (995).
//
// Neden bu kapı ŞART: operatör kademeli geçişi seçti (2026-08-12), yani
// atom geldi ve 43 dosya bir süre elle yazmaya devam etti. Kapısız bir
// kademeli geçişin tek sonucu vardır — atom kullanılmaz, sayı büyür ve
// altı ay sonra geçiş "yarım kalmış bir deneme" diye anılır. Bu geçişte
// öyle OLMADI: liste dört sürümde 43'ten 2'ye indi ve kalan iki dosya
// gecikme değil, açık ürün sorusu (gerekçeler FROZEN'ın üstünde).
// Kapı iki işi birden yapıyor:
//   1. BÜYÜMEYİ durduruyor — yeni bir sayfa `<div id="content">` yazarsa
//      test kırmızıya döner ve doğru cevabı (`<PageShell>`) söyler.
//   2. İLERLEMEYİ ölçüyor — bir dosya geçtiğinde girdisi ÖLÜR ve test
//      "listeden çıkar" diye kırılır. Yani liste mekanik olarak küçülür,
//      kimsenin ayrıca takip etmesi gerekmez.
//
// Neden tsc/eslint yakalayamaz: `id` bir string özniteliği; iki sayfanın
// aynı id'yi basması TİP olarak kusursuz. Bozukluğun belirtisi de sessiz
// — `document.getElementById('content')` hata vermez, İLK düğümü döndürür
// (`useContentWidth` orada 0 okuyup 1200 fallback'ine düşüyordu, v0.9.981).
//
// KARDEŞ KAPI: `components/singleContentId.test.ts` bugünkü GEVŞEK kuralı
// ("aynı return ağacında iki tane olmasın") koruyor. İkisi birlikte
// çalışıyor: o bugünü güvende tutuyor, bu yarına doğru daraltıyor.
// Allowlist boşaldığı gün singleContentId TAM KİLİDE çevrilebilir ve bu
// dosya onun yerine geçer.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join, sep } from 'node:path';

const SRC = resolve(__dirname, '..');
const ATOM = 'components/ui/PageShell.tsx';

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (p.endsWith('.tsx')) out.push(p);
  }
  return out;
}

// Karakter makinesi — naif `//.*$` regex'i YETMİYOR. Bu depoda iki kez
// ısırdı: (a) yorum İÇİNDEKİ `/*` naif soyucuyu yutuyor, (b) bir satırda
// `//`den ÖNCE gerçek kod varsa regex onu da siliyordu. v0.9.991'de
// ölçüldü: naif soyucu `pages/EndpointDetail.tsx`in 2 çağrısını
// KAÇIRDI, yani allowlist eksik doğardı. `singleContentId` ile aynı
// makine — ikisi aynı sayıyı görmek ZORUNDA.
function stripComments(src: string): string {
  let out = '';
  let mode: 'code' | 'line' | 'block' = 'code';
  for (let i = 0; i < src.length; i++) {
    const c = src[i];
    const n = src[i + 1] ?? '';
    if (mode === 'code') {
      if (c === '/' && n === '/') { mode = 'line'; out += '  '; i++; continue; }
      if (c === '/' && n === '*') { mode = 'block'; out += '  '; i++; continue; }
      out += c;
    } else if (mode === 'line') {
      if (c === '\n') { mode = 'code'; out += '\n'; } else out += ' ';
    } else {
      if (c === '*' && n === '/') { mode = 'code'; out += '  '; i++; continue; }
      out += c === '\n' ? '\n' : ' ';
    }
  }
  return out;
}

// DONDURULMUŞ liste — dosya → o dosyanın BUGÜNKÜ `id="content"` sayısı.
// Sayı tavan: aşmak yasak, düşmek serbest, sıfıra inince girdi SİLİNMELİ.
//
// v0.9.995 itibarıyla liste MEKANİK olarak boşaldı: kalan iki dosya
// "henüz sıra gelmedi" değil, GEREKÇELİ ertelemedir. Gerekçeler dosya
// başına ayrı ve ikisi de ürün kararı bekliyor:
//
//   · pages/TraceCompare.tsx — kabın hemen içinde satır içi
//     `height: calc(100vh - 220px)` var (:234). Bu bir `variant="full"`
//     adayı: doğru göç `default` değil, dolgusu sıfırlanmış kap +
//     çocukta `flex:1; min-height:0`. Kalibre edilmiş -220px'i atmak
//     masaüstünde ÖLÇÜLEBİLİR kayma üretir → mockup-first, operatör
//     kararı. `default`a geçirmek de mümkün ama o zaman aynı dosya
//     ikinci kez açılır; tek dokunuş için bekliyor.
//   · features/anomalies/ProblemDetail.tsx — klasik-düzen ŞERHLİ detay
//     yüzeyi (exception/problem detayı; iki redesign reddi var). Kapları
//     ölçüldü ve mekanik: ikisi de çıplak `<div id="content">`, yani
//     `default` göçü DOM'u değiştirmezdi. Şerh "DOM birebir kalsın"
//     dediği için yine de operatörün onayına bırakıldı.
//
// Yani bu liste artık bir GECİKME sayacı değil, iki açık ürün
// sorusunun kaydı. Sıfıra indiği gün `singleContentId` tam kilide
// çevrilebilir ve bu dosya onun yerine geçer.
const FROZEN: Record<string, number> = {
  'features/anomalies/ProblemDetail.tsx': 2,
  'pages/TraceCompare.tsx': 2,
};

// Tavan RAÇET'i. Yukarıdaki liste tek tek de korunuyor ama bu satır
// niyeti tek sayıda tutuyor: bir girdi eklemek isteyen bu sayıyı da
// büyütmek zorunda kalır ve o an diff'te görünür. YALNIZ düşürülebilir.
//
// Seyir: 43 (v0.9.991, donduruldu) → 36 (v0.9.992, 7 pilot geçti)
// → 24 (v0.9.993, 12 düz liste sayfası)
// → 13 (v0.9.994, 11 detay/erken-dönüş sayfası)
// → 2 (v0.9.995, kalan mekanik 11 sayfa).
const CEILING_FILES = 2;

function counts(): Record<string, number> {
  const out: Record<string, number> = {};
  for (const abs of walk(SRC)) {
    const rel = abs.slice(SRC.length + 1).split(sep).join('/');
    if (rel.endsWith('.test.tsx')) continue;
    const n = (stripComments(readFileSync(abs, 'utf8')).match(/id="content"/g) ?? []).length;
    if (n > 0) out[rel] = n;
  }
  return out;
}

describe('D9 — #content yalnız PageShell\'den, allowlist yalnız küçülür', () => {
  const live = counts();

  it('atom kabı gerçekten basıyor', () => {
    expect(live[ATOM], `${ATOM} kabı basmıyor — atom boşa çıkmış`).toBe(1);
  });

  it('allowlist DIŞINDA elle #content yazan dosya yok', () => {
    const strays = Object.keys(live)
      .filter(p => p !== ATOM && FROZEN[p] === undefined)
      .map(p => `${p} (${live[p]}×)`);
    expect(
      strays,
      'yeni sayfa kabı elle yazmış — <div id="content"> yerine <PageShell> kullan; '
      + 'listeye EKLEME yapılmaz, liste yalnız küçülür',
    ).toEqual([]);
  });

  it('dondurulmuş hiçbir dosya sayısını ARTIRMAMIŞ', () => {
    const grew = Object.entries(FROZEN)
      .filter(([p, max]) => (live[p] ?? 0) > max)
      .map(([p, max]) => `${p}: ${live[p]} > donmuş ${max}`);
    expect(grew, 'mevcut sayfa yeni bir #content dalı eklemiş').toEqual([]);
  });

  // Raçet'in ikinci dişlisi: bir dosya PageShell'e geçtiğinde girdisi
  // ÖLÜR ve bu test onu listeden çıkarmaya zorlar. Böylece liste
  // ilerlemenin kendisiyle küçülür.
  it('geçmiş dosyanın ölü girdisi listede kalmamış', () => {
    const dead = Object.keys(FROZEN).filter(p => (live[p] ?? 0) === 0);
    expect(
      dead,
      'bu dosya PageShell\'e geçmiş — FROZEN listesinden SİL ve CEILING_FILES\'ı düşür',
    ).toEqual([]);
  });

  it('tavan raçeti büyümemiş', () => {
    expect(
      Object.keys(FROZEN).length,
      'allowlist büyümüş — kademeli geçişin yönü tek: aşağı',
    ).toBeLessThanOrEqual(CEILING_FILES);
  });
});

describe('D9 — PageShell sözleşmesi', () => {
  // Atomun KODU — yorumları soyulmuş. Soymadan assert etmek bu depoda
  // tekrarlayan bir tuzak: atomun kendi doküman yorumu `padding: 20px`
  // ve `background` sözcüklerini AÇIKLAMAK için içeriyor, çıplak arama
  // onları kural sanıp kapıyı yanlış yere kırmıştı (v0.9.991, ilk koşu).
  const src = stripComments(readFileSync(join(SRC, ATOM), 'utf8'));

  // default varyant BUGÜNKÜ elle yazılmış kapla bit bit aynı DOM
  // üretmek zorunda: fazladan bir className bile `[data-density]` ve
  // ≤640px kurallarının özgüllük sırasını değiştirebilir.
  it('default varyant çıplak <div id="content"> basıyor', () => {
    expect(src).toMatch(/className=\{variant === 'full' \? 'is-full' : undefined\}/);
    expect(src, 'default dalda sabit bir sınıf basılıyor — DOM artık birebir değil')
      .not.toMatch(/className="[^"]*"/);
  });

  it('full varyantın CSS karşılığı VAR (ölü sınıf değil)', () => {
    const css = readFileSync(join(SRC, 'styles/globals.css'), 'utf8');
    expect(css, '#content.is-full kuralı yok — variant="full" hiçbir şey yapmaz')
      .toMatch(/#content\.is-full \{[^}]*padding: 0[^}]*overflow: hidden/);
  });

  // Kural, dosyanın SONUNDA olmak zorunda: `[data-density="…"] #content`
  // ile aynı özgüllükte (1,1,0), kazanan sıralamayla belirleniyor.
  it('full kuralı yoğunluk kurallarından SONRA geliyor', () => {
    const css = readFileSync(join(SRC, 'styles/globals.css'), 'utf8');
    expect(css.indexOf('#content.is-full'))
      .toBeGreaterThan(css.lastIndexOf('[data-density="dense"] #content'));
  });

  // Atom D1/D2'nin CSS kurallarını TEKRARLAMAMALI — çifte kural, iki
  // ayrı yerden çelişebilen tek bir davranış demektir.
  it('atom zemin/dolgu kurallarını tekrarlamıyor', () => {
    expect(src, 'atom kendi dolgusunu yazıyor — kural globals.css\'te olmalı')
      .not.toMatch(/padding:\s*\d/);
    expect(src, 'atom kendi zeminini yazıyor — D1 kuralıyla çelişir')
      .not.toMatch(/background:/);
  });
});
