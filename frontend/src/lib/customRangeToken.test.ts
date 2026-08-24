import { describe, it, expect } from 'vitest';
import { encodeRange, decodeRange, windowRangeParam } from './urlState';
import { timeRangeToNs } from './utils';
import { tracesPivotHref, operationTracesHref, statementTracesHref } from './pivotHref';
import type { TimeRange } from './types';

// ─────────────────────────────────────────────────────────────────────────────
// v0.9.1355 — çıplak `custom` token'ı: sessizce 24 SAAT.
//
// Zincir, üç dosyanın her biri kendi içinde makulken kuruluyordu:
//
//   1. encodeRange({preset:'custom'}) — sınır yoksa `custom:` dalına GİREMEZ
//      ve `r.preset`i, yani çıplak `'custom'` literalini basar
//      (urlState.ts:9-14).
//   2. decodeRange('custom') — `'custom:'` önekiyle başlamadığı için bilinmeyen
//      preset sayılır ve `{preset:'custom'}` olarak GERİ verilir. Token bu
//      yüzden GEÇERLİ görünür: uygulama onu üretiyor VE geri okuyabiliyor.
//   3. timeRangeToNs({preset:'custom'}) — `fromMs && toMs` yok, dolayısıyla
//      custom dalına giremez ve `PRESET_SECONDS['custom'] ?? 86400` ile
//      24 SAATE çözülür (utils.ts:17-25).
//
// Sonuç: adres çubuğunda kendinden emin bir `?range=custom` dururken sayfa
// bambaşka bir pencere çizer. Ev kuralı bunu açıkça yasaklıyor (urlState.ts:23-27,
// serviceHref.ts:67, logsUrl.ts:91): REDDEDİLEN bir pencere token yazmaz.
//
// Düzeltme İKİ uçta birden, çünkü tek uç yetmiyor:
//   • yalnız kodlama kapatılırsa elle yazılmış/paylaşılmış bir `?range=custom`
//     aynı 24 saati üretmeye devam ederdi;
//   • yalnız çözme kapatılırsa üretici hâlâ adres çubuğuna yalan yazardı.
// ─────────────────────────────────────────────────────────────────────────────

const STICKY: TimeRange = { preset: '30m' };
const S = (h: string) => new URLSearchParams(h.slice(h.indexOf('?') + 1));

describe('çıplak custom — token nereden geliyor', () => {
  // Zincirin 1. halkası KASITLI olarak yerinde bırakıldı: encodeRange bu
  // deponun ham hecelemesi ve ~30 çağıranı `range=${encodeRange(r)}` diye
  // dize içine gömüyor. Orada '' döndürmek `range=` (boş) yazdırırdı — aynı
  // yalanın başka bir hecesi. Kapı bu yüzden ÜRETİCİDE ve OKUYUCUDA.
  it('encodeRange sınırsız custom için hâlâ çıplak literali basar', () => {
    expect(encodeRange({ preset: 'custom' })).toBe('custom');
    expect(encodeRange({ preset: 'custom', fromMs: 5 })).toBe('custom');
    expect(encodeRange({ preset: 'custom', toMs: 5 })).toBe('custom');
  });

  it('ve o literal timeRangeToNs\'te 24 SAATE çözülür — zararın kaynağı', () => {
    const { from, to } = timeRangeToNs({ preset: 'custom' });
    expect(to - from).toBe(86_400 * 1e9);
  });
});

describe('windowRangeParam — sınırsız custom REDDEDİLİR', () => {
  it.each([
    ['sınır yok', { preset: 'custom' }],
    ['yalnız fromMs', { preset: 'custom', fromMs: 1_700_000_000_000 }],
    ['yalnız toMs', { preset: 'custom', toMs: 1_700_000_060_000 }],
    ['fromMs 0', { preset: 'custom', fromMs: 0, toMs: 1_700_000_060_000 }],
    ['toMs 0', { preset: 'custom', fromMs: 1_700_000_000_000, toMs: 0 }],
  ] as Array<[string, TimeRange]>)('%s → \'\' (param hiç yazılmaz)', (_l, r) => {
    expect(windowRangeParam(r)).toBe('');
  });

  // Kapının FAZLA geniş olmadığının kontrolü.
  it('düzgün custom ve normal presetler DOKUNULMAZ', () => {
    expect(windowRangeParam({ preset: 'custom', fromMs: 1_700_000_000_000, toMs: 1_700_000_060_000 }))
      .toBe('custom:1700000000000-1700000060000');
    expect(windowRangeParam({ preset: '6h' })).toBe('6h');
    expect(windowRangeParam({ fromNs: 1_700_000_000_000_000_000, toNs: 1_700_000_060_000_000_000 }))
      .toBe('custom:1700000000000-1700000060000');
  });
});

describe('decodeRange — çıplak custom fallback\'e düşer', () => {
  it('elle yazılmış ?range=custom sticky pencereyi verir, sahte custom\'ı DEĞİL', () => {
    expect(decodeRange('custom', STICKY)).toEqual(STICKY);
  });

  // Asıl sözleşme uçtan uca: eskiden bu satır 24 saat üretiyordu.
  it('uçtan uca: ?range=custom artık 24 saate ÇÖZÜLMEZ', () => {
    const { from, to } = timeRangeToNs(decodeRange('custom', STICKY));
    expect(to - from).toBe(1800 * 1e9);   // 30m — sticky
    expect(to - from).not.toBe(86_400 * 1e9);
  });

  it('düzgün custom: token\'ı hâlâ çözer', () => {
    expect(decodeRange('custom:1700000000000-1700000060000', STICKY))
      .toEqual({ preset: 'custom', fromMs: 1_700_000_000_000, toMs: 1_700_000_060_000 });
  });

  it('bozuk custom: token\'ı zaten fallback\'e düşüyordu — değişmedi', () => {
    for (const bad of ['custom:', 'custom:abc-def', 'custom:0-0', 'custom:200-100']) {
      expect(decodeRange(bad, STICKY), bad).toEqual(STICKY);
    }
  });

  // KAPSAM KARARI, açıkça çivilenmiş. Tanınmayan DİĞER token'lar aynen
  // geçmeye devam ediyor: shareUrl.test.ts:74 (`?range=90m`) o toleransı
  // sınanmış davranış olarak tutuyor ve genişletmek ayrı bir karar.
  // Ayrım keyfi değil: `90m` görünür biçimde YABANCI bir token, `custom`
  // ise uygulamanın kendi ürettiği ve geri okuyabildiği için GEÇERLİ
  // görünüyor — sessizliği yapan tam olarak bu tutarlılık.
  it('tanınmayan diğer token\'lar KASITLI olarak aynen geçer', () => {
    expect(decodeRange('90m', STICKY)).toEqual({ preset: '90m' });
    expect(decodeRange('customish', STICKY)).toEqual({ preset: 'customish' });
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// /traces ailesi. v0.9.1355'e kadar pivotHref.ts, windowRangeParam'ın ELLE
// YAZILMIŞ bir kopyasını taşıyordu (floor/ceil doğru, KABUL kuralı yok), yani
// deponun "tek pencere→range üreticisi" ilkesinin tamamen DIŞINDAYDI.
// ─────────────────────────────────────────────────────────────────────────────
describe('tracesPivotHref — reddedilen pencerede param yok', () => {
  it('sınırsız custom: range YAZILMAZ, diğer paramlar KAYBOLMAZ', () => {
    const p = S(tracesPivotHref({ window: { preset: 'custom' }, service: 'checkout' }));
    expect(p.get('range')).toBeNull();
    expect(p.has('range')).toBe(false);   // boş `range=` de yazılmıyor
    expect(p.get('service')).toBe('checkout');
    expect(p.get('rootOnly')).toBe('false');
  });

  it('epoch-altı / ters ns penceresi de reddedilir (kopyada bu kural YOKTU)', () => {
    expect(S(tracesPivotHref({ window: { fromNs: -1e9, toNs: 5e9 } })).has('range')).toBe(false);
    expect(S(tracesPivotHref({ window: { fromNs: 6e9, toNs: 5e9 } })).has('range')).toBe(false);
    expect(S(tracesPivotHref({ window: { fromNs: 5e9, toNs: 5e9 } })).has('range')).toBe(false);
  });

  // Türev üreticiler kuralı MİRAS ALIR — hepsi tracesPivotHref üzerinden
  // geçiyor, ki kuralın tek dosyada kalmasının bütün amacı bu.
  it('türev üreticiler de miras alır', () => {
    expect(S(operationTracesHref({ window: { preset: 'custom' }, operation: 'GET /cart' })).has('range'))
      .toBe(false);
    expect(S(statementTracesHref({ window: { preset: 'custom' }, statement: 'SELECT 1' })).has('range'))
      .toBe(false);
  });

  it('düzgün pencerelerde hiçbir şey değişmedi', () => {
    expect(S(tracesPivotHref({ window: { preset: '6h' } })).get('range')).toBe('6h');
    expect(S(tracesPivotHref({
      window: { preset: 'custom', fromMs: 1_700_000_000_000, toMs: 1_700_000_060_000 },
    })).get('range')).toBe('custom:1700000000000-1700000060000');
  });
});
