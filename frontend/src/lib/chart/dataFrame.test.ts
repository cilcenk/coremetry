// @vitest-environment jsdom
//
// Diğer tüm testler node ortamında (vitest.config.ts gerekçesi: saf, hızlı).
// Bu dosya İSTİSNA: @grafana/data modül yüklenirken window/localStorage'a
// dokunuyor (utils/store.ts). Global env çevrilmedi — config'in kendi
// yorumundaki plan bu: ihtiyaç duyan dosya jsdom'u kendisi ister.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { resolve } from 'node:path';
import {
  spanSeriesToFrames, framesToAligned, maxPointsForWidth, stepSecondsFor,
  chartTheme, NS_PER_MS,
} from './dataFrame';
import type { SpanMetricSeries } from '@/lib/types';

// dataFrame köprüsünün sözleşme testleri (FAZ 1).
//
// En kritik iki sözleşme:
//   1. EKSİK VERİ NULL — 0'a çevrilmez. gapPolicy'nin varlık sebebi.
//   2. Dönüşüm TEK yerde — başka dosya kendi DataFrame'ini kurmaz.
// İkincisi saf testle korunamaz; dosya sonunda kaynak-tarama kapısı var.

const theme = chartTheme();

function series(points: { time: number; value: number | null }[], key: string[] = ['svc']): SpanMetricSeries {
  // null'u API tipine sokmak için as-cast: gerçek API eksik noktada alanı
  // sayı-dışı bırakabilir; köprü her iki hâli de null'a indirir.
  return { groupKey: key, points: points as SpanMetricSeries['points'] };
}

describe('spanSeriesToFrames', () => {
  it('ns → ms çevirir', () => {
    const [f] = spanSeriesToFrames([series([{ time: 1_700_000_000_000_000_000, value: 1 }])], {}, theme);
    expect((f.fields[0].values as number[])[0]).toBe(1_700_000_000_000_000_000 / NS_PER_MS);
  });

  it('EKSİK VERİ NULL KALIR — sıfıra çevrilmez', () => {
    const [f] = spanSeriesToFrames([series([
      { time: 1e18, value: 5 },
      { time: 2e18, value: null },          // API alanı boş bıraktı
      { time: 3e18, value: NaN as number }, // bozuk sayı da null olmalı
      { time: 4e18, value: 0 },             // GERÇEK sıfır ise sıfır kalır
    ])], {}, theme);
    expect(f.fields[1].values).toEqual([5, null, null, 0]);
  });

  it('groupKey → seri adı; boş groupKey → "value"', () => {
    const [a, b] = spanSeriesToFrames([
      series([], ['payments', 'eu-1']),
      series([], []),
    ], {}, theme);
    expect(a.name).toBe('payments · eu-1');
    expect(b.name).toBe('value');
  });

  it('birim ölçekleme getDisplayProcessor üzerinden — elle çevrim yok', () => {
    const [f] = spanSeriesToFrames([series([{ time: 1e18, value: 1500 }])], { unit: 'ms' }, theme);
    const d = f.fields[1].display!(1500);
    // 1500 ms → "1.5 s": çevrimi @grafana/data yaptı. Köprü 1500'ü
    // DEĞİŞTİRMEDEN sakladı — ham değer bozulmaz, görüntü katmanı çevirir.
    expect((f.fields[1].values as number[])[0]).toBe(1500);
    expect(`${d.text}${d.suffix ?? ''}`).toMatch(/1\.50?\s*s/); // "1.5 s" / "1.50 s"
  });

  it('eşikler FieldConfig\'e iner ve taban adım eklenir', () => {
    const [f] = spanSeriesToFrames([series([{ time: 1e18, value: 1 }])], {
      thresholds: { steps: [{ value: 200, color: 'red' }] },
    }, theme);
    const th = f.fields[1].config.thresholds!;
    expect(th.steps[0].value).toBe(-Infinity); // Grafana modeli taban ister
    expect(th.steps[1]).toMatchObject({ value: 200, color: 'red' });
  });
});

describe('framesToAligned', () => {
  it('aynı kafes: zaman dizisi paylaşılır, kopyalanmaz', () => {
    const frames = spanSeriesToFrames([
      series([{ time: 1e18, value: 1 }, { time: 2e18, value: 2 }], ['a']),
      series([{ time: 1e18, value: 3 }, { time: 2e18, value: 4 }], ['b']),
    ], {}, theme);
    const { data, names } = framesToAligned(frames);
    expect(data[0]).toBe(frames[0].fields[0].values); // referans eşitliği: fast-path
    expect(data[1]).toEqual([1, 2]);
    expect(data[2]).toEqual([3, 4]);
    expect(names).toEqual(['a', 'b']);
  });

  it('farklı kafes: null-DOLGULU birleşim — 0 değil', () => {
    const frames = spanSeriesToFrames([
      series([{ time: 1e18, value: 1 }, { time: 3e18, value: 3 }], ['a']),
      series([{ time: 2e18, value: 2 }], ['b']),
    ], {}, theme);
    const { data } = framesToAligned(frames);
    expect(data[0]).toEqual([1e18 / NS_PER_MS, 2e18 / NS_PER_MS, 3e18 / NS_PER_MS]);
    expect(data[1]).toEqual([1, null, 3]);
    expect(data[2]).toEqual([null, 2, null]);
  });

  it('boş girdi çökmez', () => {
    expect(framesToAligned([])).toEqual({ data: [[]], names: [] });
  });

  // v0.9.704 (self-review 🟠) — REVIEW'IN BİREBİR SENARYOSU. Eski sezgi
  // uzunluk+uçlara bakıyordu: A=[0,60,120,300] ve B=[0,60,240,300] aynı
  // kafes sayılıp B'nin t=240 değeri x=120'ye çizilirdi — çizgi, legend
  // istatistiği ve CSV birden sessizce yanlış. Sunucu seyrek üretiyor
  // (WITH FILL yok); bu vaka kurgu değil, gerçek şekil.
  it('aynı uzunluk + aynı uçlar ama FARKLI iç kafes → birleşim, bindirme değil', () => {
    const mk = (times: number[], vals: number[], key: string) => {
      const f = spanSeriesToFrames([series(
        times.map((t, i) => ({ time: t * NS_PER_MS, value: vals[i] })), [key],
      )], {}, theme)[0];
      return f;
    };
    const A = mk([0, 60, 120, 300], [1, 2, 3, 4], 'a');
    const B = mk([0, 60, 240, 300], [10, 20, 30, 40], 'b');
    const { data } = framesToAligned([A, B]);
    // Birleşik eksen 5 nokta: 0,60,120,240,300
    expect(data[0]).toEqual([0, 60, 120, 240, 300]);
    // B'nin 30 değeri x=240'TA — x=120'de DEĞİL (eski hata oraya koyardı)
    expect(data[2]).toEqual([10, 20, null, 30, 40]);
    expect(data[1]).toEqual([1, 2, 3, null, 4]);
  });

  // v0.9.704 (self-review 🟡) — çok seride opts.name önek olur, kimlik
  // groupKey'den; tek seride ad aynen. Eskisi hepsine aynı adı basıp
  // duplicate React key + tek renk üretiyordu.
  it('opts.name: tek seride ad, çok seride önek', () => {
    const one = spanSeriesToFrames([series([], ['a'])], { name: 'p95' }, theme);
    expect(one[0].name).toBe('p95');
    const two = spanSeriesToFrames(
      [series([], ['a']), series([], ['b'])], { name: 'p95' }, theme);
    expect(two.map(f => f.name)).toEqual(['p95 · a', 'p95 · b']);
  });
});

describe('nokta bütçesi (spec: nokta ≤ piksel)', () => {
  it('bütçe = css genişliği, tabanı 50', () => {
    expect(maxPointsForWidth(800)).toBe(800);
    expect(maxPointsForWidth(20)).toBe(50);
  });

  it('step, nokta sayısını bütçenin altında tutar', () => {
    const from = 0, to = 3600 * 1e9; // 1 saat
    const step = stepSecondsFor(from, to, 600);
    expect(step).toBe(6); // 3600/600
    expect(3600 / step).toBeLessThanOrEqual(600);
  });

  it('dar aralıkta step 1 sn tabanına oturur — sunucu 1 sn altına inmez', () => {
    expect(stepSecondsFor(0, 30 * 1e9, 800)).toBe(1);
  });

  it('küsuratlı bölmede YUKARI yuvarlar — bütçe asla aşılmaz', () => {
    const spanSec = 7000, width = 600;
    const step = stepSecondsFor(0, spanSec * 1e9, width);
    expect(spanSec / step).toBeLessThanOrEqual(maxPointsForWidth(width));
  });
});

// ---------------------------------------------------------------------------
// ÇAĞRI-YERİ KAPISI — "dönüşüm TEK yerde yaşar" sözleşmesi.
//
// Saf testlerin tamamı, bir sayfa kendi DataFrame'ini elle kursa da geçer
// (v0.9.660 resetLayout dersi: yazılmış-ama-bağlanmamış kod). Bu kapı,
// @grafana/data'nın frame-kuran API'lerinin yalnız BU modülden import
// edildiğini tarar.
// ---------------------------------------------------------------------------
describe('köprü tekeli', () => {
  const SRC = resolve(__dirname, '../..');

  function* walk(dir: string): Generator<string> {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const p = resolve(dir, e.name);
      if (e.isDirectory()) yield* walk(p);
      else if (/\.(ts|tsx)$/.test(e.name) && !/\.test\./.test(e.name)) yield p;
    }
  }

  it('getDisplayProcessor/createTheme yalnız dataFrame.ts içinden', () => {
    const offenders: string[] = [];
    for (const f of walk(SRC)) {
      if (f.endsWith('lib/chart/dataFrame.ts')) continue;
      const src = readFileSync(f, 'utf8');
      if (/from '@grafana\/data'/.test(src) && /getDisplayProcessor|createTheme/.test(src)) {
        offenders.push(f);
      }
    }
    expect(offenders, 'köprü dışında frame/display kuran dosya').toEqual([]);
  });
});
