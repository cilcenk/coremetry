import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';

// ─────────────────────────────────────────────────────────────────────────────
// v0.9.1356 — /traces ailesinde el-yapımı `custom:` token'ı KALMASIN.
//
// Üç site pencereyi kendi elleriyle heceliyordu (ProblemDetail "Error traces",
// ServiceLatencyHeatmap kutu seçimi, dashboard heatmapPivot). Üçü de floor/ceil
// kuralını doğru taşıyordu — ve tam olarak bu yüzden zararsız görünüyorlardı:
// eksik olan KABUL kuralıydı. v0.9.1355'te windowRangeParam "decodeRange'in
// reddedeceği pencerede token YAZMA" kuralını öğrendi, ama bir dizeyi elle
// kuran dosya o kuralı hiç görmez. İmza yalnız kendi çağıranlarını korur.
//
// KAPSAM, bilinçli olarak dar ve muafiyetsiz: kural "`/traces?` URL'i kuran
// bir dosya `custom:` token'ını KENDİ hecelemez". Kanonik üreticiler
// (urlState.encodeRange, logsUrl, streams.patternLogWindow) `/traces?` dizesi
// kurmadıkları için doğal olarak kapsam dışında — muafiyet listesi YOK,
// dolayısıyla çürüyecek bir liste de yok.
// ─────────────────────────────────────────────────────────────────────────────

const SRC = join(__dirname, '..');

const walk = (dir: string, rel = ''): string[] => {
  const out: string[] = [];
  for (const e of readdirSync(dir)) {
    const full = join(dir, e);
    const r = rel ? `${rel}/${e}` : e;
    if (statSync(full).isDirectory()) { out.push(...walk(full, r)); continue; }
    if (!/\.tsx?$/.test(e) || /\.test\.tsx?$/.test(e)) continue;
    out.push(r);
  }
  return out;
};

// Yorumdaki bir örnek CANLI site sayılmasın (pivotHref.test.ts / backLink.test.ts
// ile aynı sınıflandırıcı; satır ORTASINDAKİ `/*` yorum başlatmaz).
const strip = (text: string): string => {
  let inBlock = false;
  return text.split('\n').map(l => {
    const t = l.trim();
    const opens = !inBlock && (t.startsWith('/*') || t.startsWith('{/*'));
    const commented = inBlock || opens || t.startsWith('//') || t.startsWith('*');
    if (opens) inBlock = true;
    if (inBlock && t.includes('*/')) inBlock = false;
    return commented ? '' : l;
  }).join('\n');
};

const BUILDS_TRACES_URL = /[`'"]\/traces\?/;

// ⚠ YAZIM LİSTESİ, tek yazım DEĞİL. Bu kapı sınıfının bilinen kör noktası
// ikiz hecelemedir (v0.9.1285/1286): üç siteden biri `&range=custom:` yazıyordu
// ama ProblemDetail `encodeURIComponent(`custom:${…}`)` yazıyordu — tek bir
// `range=custom:` grep'i onu HİÇ görmezdi. Liste aşağıdaki meta-testle
// kanıtlanıyor: her yazımın gerçekten yakalandığı ölçülüyor, varsayılmıyor.
const CUSTOM_SPELLINGS: Array<[string, RegExp]> = [
  ['düz `range=custom:`', /range=custom:/i],
  ['ön-kodlanmış `range=custom%3A`', /range=custom%3A/i],
  ['şablon literal `` `custom:${…}` ``', /[`'"]custom:\$\{/],
  ['bitişik dize `\'custom:\'`', /['"`]custom:['"`]/],
];

const spellingsIn = (src: string) =>
  CUSTOM_SPELLINGS.filter(([, re]) => re.test(src)).map(([label]) => label);

describe('/traces ailesi — el-yapımı `custom:` token\'ı yok', () => {
  const files = walk(SRC);

  it('tarama boşa koşmuyor', () => {
    expect(files.length).toBeGreaterThan(100);
    // En az bir dosya GERÇEKTEN `/traces?` kuruyor olmalı; hiç kalmadıysa
    // kapı anlamsızlaşmıştır ve sessizce yeşil kalmamalı.
    const builders = files.filter(f => BUILDS_TRACES_URL.test(strip(readFileSync(join(SRC, f), 'utf8'))));
    expect(builders.length).toBeGreaterThan(3);
  });

  // Meta-test: listenin gerçekten ölçtüğünü ölç. VARLIK değil, körlük
  // aranıyor — bir yazım listeden düşerse burası kırmızı verir.
  it('yazım listesi dört hecelemenin HEPSİNİ yakalıyor', () => {
    const synthetic: Array<[string, string]> = [
      ['düz `range=custom:`', 'const h = `/traces?a=1&range=custom:${f}-${t}`;'],
      ['ön-kodlanmış `range=custom%3A`', 'const h = "/traces?range=custom%3A1-2";'],
      ['şablon literal `` `custom:${…}` ``', 'q.set("range", `custom:${f}-${t}`);'],
      ['bitişik dize `\'custom:\'`', "q.set('range', 'custom:' + f + '-' + t);"],
    ];
    for (const [label, code] of synthetic) {
      expect(spellingsIn(code), `${label} yakalanmıyor — liste körleşti`)
        .toContain(label);
    }
    // Ve TEMİZ kod yanlış-pozitif üretmiyor (kapı her şeye "ihlal" demiyor).
    expect(spellingsIn('return tracesPivotHref({ window, service });')).toEqual([]);
    expect(spellingsIn('const r = decodeRange(v, fallback);')).toEqual([]);
  });

  it('`/traces?` kuran hiçbir dosya `custom:` hecelemiyor', () => {
    const hits: string[] = [];
    for (const f of files) {
      const src = strip(readFileSync(join(SRC, f), 'utf8'));
      if (!BUILDS_TRACES_URL.test(src)) continue;
      const found = spellingsIn(src);
      if (found.length) hits.push(`${f} → ${found.join(', ')}`);
    }
    expect(hits.sort(), [
      'Bu dosyalar `/traces?` URL\'ini kurarken `custom:` token\'ını KENDİ',
      'heceliyor. Elle kurulan bir token floor/ceil kuralını taşısa bile',
      'KABUL kuralını taşımaz: reddedilecek bir pencerede token basar ve',
      'adres çubuğunda kendinden emin ama yanlış bir pencere gösterir',
      '(v0.9.1355). tracesPivotHref / operationTracesHref / dbTracesHref /',
      'messagingTracesHref / statementTracesHref / slowTracesHref kullan.',
    ].join('\n')).toEqual([]);
  });
});

// Dönüştürülen iki JSX sitesi saf fonksiyon değil, o yüzden kaynak düzeyinde
// çivileniyor (heatmapPivot.test.ts:101 ile aynı kalıp). Üçüncü site
// (heatmapTracesHref) saf ve kendi dosyasında davranışsal olarak test ediliyor.
describe('dönüştürülen siteler aile üreticisini çağırıyor', () => {
  const read = (p: string) => readFileSync(join(SRC, p), 'utf8');

  it('ProblemDetail "Error traces" — tracesPivotHref, probWindow ile', () => {
    const src = read('features/anomalies/ProblemDetail.tsx');
    expect(src).toMatch(/tracesPivotHref\(\{\s*\n?\s*window: probWindow/);
    expect(src).toMatch(/hasError: true/);
    // Ara ms değişkenleri SİLİNDİ: ikisi de Math.round'du ve yuvarlama
    // pencereyi iki uçtan daraltabiliyordu. Geri gelirlerse kusur da gelir.
    expect(src, 'logsFrom/logsTo geri geldi — Math.round daraltması da geri gelir')
      .not.toMatch(/const logsFrom\s*=/);
  });

  it('ServiceLatencyHeatmap kutu seçimi — tracesPivotHref, kutu penceresiyle', () => {
    const src = read('pages/service/ServiceLatencyHeatmap.tsx');
    expect(src).toMatch(/tracesPivotHref\(\{/);
    expect(src).toMatch(/fromNs: boxSel\.timeFromNs, toNs: boxSel\.timeToNs/);
  });
});
