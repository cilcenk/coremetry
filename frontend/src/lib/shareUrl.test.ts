// shareUrl — paylaşım linkinin zaman penceresi (v0.9.1280)
//
// Ne çiviliyor: bir incident linki ertesi gün AYNI pencereyi göstermeli.
// `?range=1h` göreli olduğu için açan kişinin saatine göre kayıyordu; kanıt
// değeri sessizce buharlaşıyordu.
//
// Neden bu vakalar birlikte: dönüşüm iki yönde de bozulabilir. Hiç
// dondurmamak bugünkü bug. Her şeye dokunmak yeni bug ailesi: range
// kullanmayan sayfaya param enjekte etmek, zaten mutlak olan linki yeniden
// yazmak, tanınmayan bir token'dan kendinden emin bir pencere uydurmak.
//
// Zaman ENJEKTE ediliyor (nowMs argümanı) — fake timer yok, deterministik.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve as resolvePath } from 'node:path';
import { absoluteShareHref } from './shareUrl';
import { decodeRange } from './urlState';
import { PRESET_SECONDS } from './utils';

// 2025-08-12T14:40:00Z — sabit, okunur, saniye sınırında.
const NOW = 1_755_000_000_000;
const BASE = 'https://coremetry.internal/explore';

describe('absoluteShareHref — göreli preset mutlaklaşır', () => {
  it('range=1h → custom:<from>-<to>, diğer paramlar yerinde', () => {
    expect(absoluteShareHref(`${BASE}?range=1h&source=traces`, NOW)).toBe(
      `${BASE}?range=custom:1754996400000-1755000000000&source=traces`,
    );
  });

  it('range ilk param değilse de dönüşür, sıra korunur', () => {
    expect(absoluteShareHref(`${BASE}?source=traces&range=30m&group=service`, NOW)).toBe(
      `${BASE}?source=traces&range=custom:1754998200000-1755000000000&group=service`,
    );
  });

  // Değer+birim disiplini (v0.6.36 sınıfı): 11 rungun HEPSİ sınanır, biri
  // sessizce 24h'e düşerse burada kızarır.
  it.each(Object.entries(PRESET_SECONDS))('preset %s → doğru açıklık', (preset, secs) => {
    const out = absoluteShareHref(`${BASE}?range=${preset}`, NOW);
    const token = new URL(out).searchParams.get('range') ?? '';
    expect(token).toBe(`custom:${NOW - secs * 1000}-${NOW}`);
    // Okuyucu tarafı da kabul etmeli — reddedilen bir token adres çubuğunda
    // kendinden emin durur ama sayfa sticky pencereyi çizer.
    expect(decodeRange(token, { preset: '1h' })).toEqual({
      preset: 'custom', fromMs: NOW - secs * 1000, toMs: NOW,
    });
  });

  it('encoded JSON paramlar bayt-aynı kalır (yeniden kodlama yok)', () => {
    const filters = 'filters=%5B%7B%22k%22%3A%22svc%22%2C%22v%22%3A%22api%22%7D%5D';
    expect(absoluteShareHref(`${BASE}?range=6h&${filters}&tab=spans`, NOW)).toBe(
      `${BASE}?range=custom:1754978400000-1755000000000&${filters}&tab=spans`,
    );
  });

  it('fragment korunur', () => {
    expect(absoluteShareHref(`${BASE}?range=1h#panel-2`, NOW)).toBe(
      `${BASE}?range=custom:1754996400000-1755000000000#panel-2`,
    );
  });
});

describe('absoluteShareHref — dokunulmayanlar bayt-AYNI döner', () => {
  const untouched: Array<[string, string]> = [
    // Zaten mutlak: yeniden yazmak yalnız yazımı değiştirirdi.
    ['zaten custom', `${BASE}?range=custom:1700000000000-1700003600000&tab=spans`],
    // navHref'in ürettiği yazım — %3A. Erken dönüş kalkarsa bu satır kızarır.
    ['zaten custom, %3A yazımı', `${BASE}?range=custom%3A1700000000000-1700003600000`],
    // Range okumayan sayfa (Trace detay): param ENJEKTE etme.
    ['range yok', 'https://coremetry.internal/trace/8f1c2d?span=aa11'],
    ['query hiç yok', 'https://coremetry.internal/trace/8f1c2d'],
    // Tanınmayan preset: resolveRangeMs bunu sessizce 24h yapar, biz yapmayız.
    ['tanınmayan preset', `${BASE}?range=90m&source=traces`],
    ['boş range', `${BASE}?range=&source=traces`],
    // Object.prototype anahtarı — `in` ile yazılsaydı geçerdi.
    ['prototip anahtarı', `${BASE}?range=constructor`],
    // Bozuk custom: decodeRange de reddeder, biz de uydurmayız.
    ['bozuk custom', `${BASE}?range=custom:abc-def`],
    ['ters custom', `${BASE}?range=custom:200-100`],
    // Adı range ile başlayan BAŞKA param (önek çakışması dersi).
    ['rangeMode paramı', `${BASE}?rangeMode=relative&source=traces`],
  ];
  it.each(untouched)('%s', (_name, href) => {
    expect(absoluteShareHref(href, NOW)).toBe(href);
  });

  it('nowMs geçersizse hiçbir şey yapma', () => {
    const href = `${BASE}?range=1h`;
    expect(absoluteShareHref(href, Number.NaN)).toBe(href);
  });
});

// Bağlantı kapısı: saf çekirdek doğru olsa da ShareButton onu ÇAĞIRMAZSA
// operatör hâlâ göreli link paylaşır (v0.9.1280'in asıl teslimatı bu).
describe('ShareButton bağlantısı', () => {
  const src = readFileSync(
    resolvePath(__dirname, '..', 'components', 'ShareButton.tsx'), 'utf8',
  );
  it('kopyalanan href absoluteShareHref üzerinden geçiyor', () => {
    expect(src).toContain('absoluteShareHref(window.location.href');
  });
  it('ham location.href artık panoya gitmiyor', () => {
    expect(src).not.toContain('copyToClipboard(window.location.href)');
  });
});
