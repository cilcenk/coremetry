// attrKeyWindow — UX denetimi F3 / Ö14c regresyon testleri (v0.9.953).
//
// ORİJİNAL BELİRTİ: attribute-key keşfi SABİT '1h' penceresinde koşuyordu.
// 7 günlük pencereye bakan operatör, son bir saatte görülmemiş bir
// attribute'u öneri listesinde bulamıyordu.
//
// İKİ TUZAK, ikisi de burada çivili:
//   1. v0.8.270 — pencere HAM geçirilemez: sunucu cache anahtarı
//      `since=<dize>`, serbest bir pencere cache'i hiç ısıtmaz.
//   2. v0.6.36 (birim karışımı) — basamaklar 'd' TAŞIYAMAZ: Go'nun
//      time.ParseDuration'ı 'd'yi tanımaz ve sunucu SESSİZCE varsayılana
//      (1 saat) düşerdi, yani düzeltme hiç uygulanmamış görünürdü.

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { snapSince, attrKeySince, ATTR_KEY_RUNGS } from './attrKeyWindow';
import type { TimeRange } from './types';

describe('snapSince — basamaklar', () => {
  const cases: [number, string][] = [
    [1, '1h'],
    [60, '1h'],
    [3600, '1h'],            // tam sınır AŞAĞIDA kalır
    [3601, '6h'],
    [6 * 3600, '6h'],
    [6 * 3600 + 1, '24h'],
    [24 * 3600, '24h'],
    [24 * 3600 + 1, '168h'],
    [7 * 24 * 3600, '168h'],
    [7 * 24 * 3600 + 1, '720h'],
    [30 * 24 * 3600, '720h'],
    [365 * 24 * 3600, '720h'],   // tavan
  ];
  for (const [sec, want] of cases) {
    it(`${sec}s → ${want}`, () => expect(snapSince(sec)).toBe(want));
  }

  it('bozuk girdi güvenli varsayılana düşer', () => {
    expect(snapSince(0)).toBe('1h');
    expect(snapSince(-5)).toBe('1h');
    expect(snapSince(NaN)).toBe('1h');
    // Infinity gerçek bir pencere DEĞİL — NaN ile aynı sınıf. Tavana
    // (720h) yükseltmek, bozuk bir girdiye retention-geneli bir CH
    // taraması ödetmek olurdu.
    expect(snapSince(Infinity)).toBe('1h');
  });
});

describe('BASAMAK SAYISI SINIRLI — v0.8.270 cache disiplini', () => {
  it('en fazla 5 farklı anahtar üretilir', () => {
    // Serbest bir pencere her dokunuşta yeni bir sunucu cache anahtarı
    // üretir; 60 sn'lik cache hiç ısınmaz ve her keystroke bir CH
    // taraması ödetir.
    const produced = new Set<string>();
    for (let s = 1; s <= 60 * 24 * 3600; s += 997) produced.add(snapSince(s));
    expect(produced.size).toBeLessThanOrEqual(5);
    expect(produced.size).toBe(ATTR_KEY_RUNGS.length);
  });

  it('YUVARLAMA SAF: aynı saniye her zaman aynı basamak', () => {
    expect(snapSince(12345)).toBe(snapSince(12345));
  });

  it('yuvarlama YUKARI — keşif penceresi bakılan pencereyi KAPSAR', () => {
    // Aşağı yuvarlamak "bu aralıkta var ama öneride yok" hâline geri
    // döndürürdü, yani düzeltmeyi tersine çevirirdi.
    const secs = 5 * 3600;
    const rung = ATTR_KEY_RUNGS.find(r => r.since === snapSince(secs))!;
    expect(rung.maxSec).toBeGreaterThanOrEqual(secs);
  });
});

describe('BİRİM TUZAĞI — v0.6.36 sınıfı', () => {
  it("her basamak Go'nun time.ParseDuration'ının kabul ettiği biçimde", () => {
    // 'd' YOK: Go 'd' birimini TANIMAZ. '7d' gönderilseydi sunucu
    // sessizce varsayılana (1 saat) düşerdi ve düzeltme hiç uygulanmamış
    // gibi görünürdü — sessiz, dolayısıyla en pahalı hata biçimi.
    for (const r of ATTR_KEY_RUNGS) {
      expect(r.since, `${r.since} Go biçiminde değil`).toMatch(/^\d+(h|m|s)$/);
      expect(r.since).not.toMatch(/d$/);
    }
  });

  it('gün karşılıkları SAATE çevrilmiş', () => {
    expect(snapSince(7 * 24 * 3600)).toBe('168h');
    expect(snapSince(30 * 24 * 3600)).toBe('720h');
  });

  it('basamaklar ARTAN sırada — ilk-eşleşen taraması doğru çalışsın', () => {
    for (let i = 1; i < ATTR_KEY_RUNGS.length; i++) {
      expect(ATTR_KEY_RUNGS[i].maxSec).toBeGreaterThan(ATTR_KEY_RUNGS[i - 1].maxSec);
    }
  });
});

describe('attrKeySince — TimeRange girişi', () => {
  it('range yoksa eski davranış (1h) — varsayım ÜRETİLMEZ', () => {
    expect(attrKeySince(undefined)).toBe('1h');
    expect(attrKeySince(null)).toBe('1h');
  });

  it('preset pencereler doğru basamağa oturur', () => {
    expect(attrKeySince({ preset: '15m' } as TimeRange)).toBe('1h');
    expect(attrKeySince({ preset: '6h' } as TimeRange)).toBe('6h');
    expect(attrKeySince({ preset: '24h' } as TimeRange)).toBe('24h');
    expect(attrKeySince({ preset: '7d' } as TimeRange)).toBe('168h');
  });

  it('custom (mutlak) pencere de basamaklanır', () => {
    const toMs = Date.now();
    const fromMs = toMs - 3 * 24 * 3600 * 1000;
    expect(attrKeySince({ preset: 'custom', fromMs, toMs } as TimeRange)).toBe('168h');
  });
});

// v0.9.956 — RENDER TUZAĞI KAPISI (v0.5.184 sınıfı).
//
// attrKeySince içeride timeRangeToNs çağırıyor ve o da PRESET aralıklarda
// Date.now() okuyor. Çıplak bir render-gövdesi çağrısı, bu depoda sonsuz
// refetch'in klasik şekli.
//
// v0.9.953'te SplitByPicker tam bu şekli taşıyordu. Bugün zararsızdı
// (snapSince beş basamağa yuvarladığı için dize sabit kalıyor, sorgu
// anahtarı oynamıyor) ama YASAK OLAN ŞEKLİN kendisi: basamak listesi bir
// gün incelirse aynı satır sessizce sonsuz refetch'e döner. Zararsız
// görünen doğru şekil, zararsız görünen yanlış şekilden iyidir.
describe('attrKeySince çağrıları memolu (v0.9.956)', () => {
  const SRC = join(__dirname, '..');
  const CONSUMERS = [
    'pages/Traces.tsx',
    'components/FilterBuilder.tsx',
    'components/ColumnManager.tsx',
    'pages/explore/SplitByPicker.tsx',
  ];

  it('hiçbir çağrı render gövdesinde ÇIPLAK değil', () => {
    const bare: string[] = [];
    for (const rel of CONSUMERS) {
      const src = readFileSync(join(SRC, rel), 'utf8').replace(/\/\/.*$/gm, '');
      for (const m of src.matchAll(/attrKeySince\(/g)) {
        // Çağrıdan geriye 220 karakter: useMemo/useEffect sarmalayıcısı
        // ya da bir ok fonksiyonu gövdesi bu pencerede görünür.
        const before = src.slice(Math.max(0, m.index! - 220), m.index!);
        if (!/useMemo\(|useEffect\(/.test(before)) bare.push(`${rel}@${m.index}`);
      }
    }
    expect(bare,
      'attrKeySince timeRangeToNs → Date.now() okuyor; çıplak çağrı v0.5.184 sonsuz-refetch şekli.')
      .toEqual([]);
  });

  it('en az bir gerçek tüketici taranıyor — kapı boşa dönmesin', () => {
    const total = CONSUMERS.reduce((n, rel) =>
      n + (readFileSync(join(SRC, rel), 'utf8').match(/attrKeySince\(/g) ?? []).length, 0);
    expect(total).toBeGreaterThanOrEqual(CONSUMERS.length);
  });
});
