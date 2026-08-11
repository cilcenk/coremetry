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
import { snapSince, attrKeySince, attrKeyWindowParams, ATTR_KEY_RUNGS, ATTR_KEY_SNAP_NS } from './attrKeyWindow';
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
// v0.9.969 — üretici adı attrKeySince → attrKeyWindowParams oldu (Ö15:
// mutlak pencere). KUSUR AYNI: attrKeyWindowParams da timeRangeToNs üstünden
// Date.now() okuyor, üstelik artık custom pencerede NESNE döndürüyor — çıplak
// bir çağrı hem her render'da yeni referans üretir hem de dep listelerini
// bozar. Kapı silinmedi, ADI güncellendi; ikisi de taranıyor ki eski şekil
// geri sızarsa da yakalansın.
// ─────────────────────────────────────────────────────────────────────────────
// attrKeyWindowParams — MUTLAK pencere (v0.9.969, UX denetimi Ö15).
//
// v0.9.953 pencerenin UZUNLUĞUNU düzeltti; KONUMUNU düzeltemezdi çünkü
// `since` yalnız "son N" diyebiliyor. Dün öğlen 30 dakikalık bir ani
// yükselişe zoom'layan operatörün anahtar önerileri SON 30 dakikadan
// geliyordu: daha dar bir cevap değil, BAŞKA bir cevap — ve sessizce.
//
// İki tarafı birden çiviliyoruz: göreli preset'ler `since`te KALMALI (aynı
// preset'e bakan herkes tek cache girdisini paylaşsın diye), custom pencere
// mutlak sınırlara ÇIKMALI ve sınırlar 5 dk ızgarasına oturmalı (v0.8.270:
// ham pencere = her fırça oynamasında yeni cache anahtarı).
// ─────────────────────────────────────────────────────────────────────────────
describe('attrKeyWindowParams — göreli vs mutlak', () => {
  const MIN = 60 * 1000;

  it('preset ve range-siz çağrı `since` yolunda KALIR', () => {
    // Bunlar gerçekten now-çapalı; mutlağa çevirmek her sekmeye kendi cache
    // anahtarını verirdi.
    expect(attrKeyWindowParams(undefined)).toEqual({ since: '1h' });
    expect(attrKeyWindowParams(null)).toEqual({ since: '1h' });
    for (const preset of ['15m', '6h', '24h', '7d', '30d']) {
      const w = attrKeyWindowParams({ preset } as TimeRange);
      expect('since' in w, preset).toBe(true);
      expect(w).toEqual({ since: attrKeySince({ preset } as TimeRange) });
    }
  });

  it('custom pencere MUTLAK sınırlara çıkar', () => {
    const fromMs = Date.UTC(2026, 0, 2, 10, 0, 0);
    const toMs = Date.UTC(2026, 0, 2, 10, 30, 0);
    expect(attrKeyWindowParams({ preset: 'custom', fromMs, toMs } as TimeRange))
      .toEqual({ fromNs: fromMs * 1e6, toNs: toMs * 1e6 });
  });

  it('kenarlar 5 dk ızgarasına oturur — from AŞAĞI, to YUKARI', () => {
    // Pencere asla DARALMAZ: daraltmak, operatörün baktığı aralığın ucundaki
    // anahtarı öneriden düşürmek olurdu.
    const fromMs = Date.UTC(2026, 0, 2, 10, 3, 27);
    const toMs = Date.UTC(2026, 0, 2, 10, 31, 9);
    const w = attrKeyWindowParams({ preset: 'custom', fromMs, toMs } as TimeRange) as { fromNs: number; toNs: number };
    expect(w.fromNs).toBe(Date.UTC(2026, 0, 2, 10, 0, 0) * 1e6);
    expect(w.toNs).toBe(Date.UTC(2026, 0, 2, 10, 35, 0) * 1e6);
    expect(w.fromNs).toBeLessThanOrEqual(fromMs * 1e6);
    expect(w.toNs).toBeGreaterThanOrEqual(toMs * 1e6);
    expect(w.fromNs % ATTR_KEY_SNAP_NS).toBe(0);
    expect(w.toNs % ATTR_KEY_SNAP_NS).toBe(0);
  });

  it('fırça JİTTER\'ı cache anahtarını DEĞİŞTİRMEZ (v0.8.270)', () => {
    // Aynı 5 dk kutusu içinde kalan üç farklı fırça → tek anahtar.
    const base = Date.UTC(2026, 0, 2, 10, 1, 0);
    const keys = new Set([0, 37 * 1000, 2 * MIN].map(d =>
      JSON.stringify(attrKeyWindowParams({
        preset: 'custom', fromMs: base + d, toMs: base + d + 20 * MIN,
      } as TimeRange))));
    expect(keys.size).toBe(1);
  });

  it('30 günlük tavan BAŞTAN kırpar, sondan değil', () => {
    // Operatörün baktığı pencerenin EN YENİ ucu her zaman kapsamda kalmalı.
    const toMs = Date.UTC(2026, 3, 1, 0, 0, 0);
    const fromMs = toMs - 90 * 24 * 3600 * 1000;
    const w = attrKeyWindowParams({ preset: 'custom', fromMs, toMs } as TimeRange) as { fromNs: number; toNs: number };
    expect(w.toNs).toBe(toMs * 1e6);
    expect(w.toNs - w.fromNs).toBe(30 * 24 * 3600 * 1e9);
  });

  it('bozuk custom aralık göreli forma DÜŞER — uydurmaz', () => {
    for (const r of [
      { preset: 'custom' },                                   // sınırsız
      { preset: 'custom', fromMs: 0, toMs: 1000 },            // epoch başı
      { preset: 'custom', fromMs: 2000, toMs: 2000 },         // sıfır genişlik
      { preset: 'custom', fromMs: 3000, toMs: 2000 },         // ters
    ]) {
      const w = attrKeyWindowParams(r as TimeRange);
      expect('since' in w, JSON.stringify(r)).toBe(true);
    }
  });

  it('üretilen anahtar sayısı SINIRLI kalır — 5 dk kutusu başına bir', () => {
    // Bir saatlik sürüklemede en fazla 13 farklı kutu ucu; sınırsız bir
    // pencere burada yüzlerce anahtar üretirdi.
    const base = Date.UTC(2026, 0, 2, 10, 0, 0);
    const keys = new Set<string>();
    for (let m = 0; m <= 60; m++) {
      keys.add(JSON.stringify(attrKeyWindowParams({
        preset: 'custom', fromMs: base, toMs: base + m * MIN,
      } as TimeRange)));
    }
    expect(keys.size).toBeLessThanOrEqual(13);
  });
});

describe('pencere üreticisi çağrıları memolu (v0.9.956/969)', () => {
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
      for (const m of src.matchAll(/attrKeyWindowParams\(|attrKeySince\(/g)) {
        // Çağrıdan geriye 220 karakter: useMemo/useEffect sarmalayıcısı
        // ya da bir ok fonksiyonu gövdesi bu pencerede görünür.
        const before = src.slice(Math.max(0, m.index! - 220), m.index!);
        if (!/useMemo\(|useEffect\(/.test(before)) bare.push(`${rel}@${m.index}`);
      }
    }
    expect(bare,
      'Pencere üreticisi timeRangeToNs → Date.now() okuyor; çıplak çağrı v0.5.184 sonsuz-refetch şekli.')
      .toEqual([]);
  });

  it('en az bir gerçek tüketici taranıyor — kapı boşa dönmesin', () => {
    const total = CONSUMERS.reduce((n, rel) =>
      n + (readFileSync(join(SRC, rel), 'utf8').match(/attrKeyWindowParams\(|attrKeySince\(/g) ?? []).length, 0);
    expect(total).toBeGreaterThanOrEqual(CONSUMERS.length);
  });
});
