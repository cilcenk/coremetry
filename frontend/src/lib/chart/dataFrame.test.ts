// @vitest-environment jsdom
//
// Diğer tüm testler node ortamında (vitest.config.ts gerekçesi: saf, hızlı).
// Bu dosya İSTİSNA: @grafana/data modül yüklenirken window/localStorage'a
// dokunuyor (utils/store.ts). Global env çevrilmedi — config'in kendi
// yorumundaki plan bu: ihtiyaç duyan dosya jsdom'u kendisi ister.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { resolve } from 'node:path';
import { guessDecimals, roundDecimals } from '@grafana/data';
import {
  axisTickPlan, seriesExtent, paddedExtent, decimalsForScaledIncr, displayScaleOf,
  scaleRefTick,
} from './axisSize';
import {
  spanSeriesToFrames, framesToAligned, maxPointsForWidth, stepSecondsFor,
  chartTheme, NS_PER_MS,
} from './dataFrame';
import { decimalsForIncr } from './axisSize';
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
// v0.9.799 — EKSEN ONDALIĞI (operatör: "failure rate panelinde her tick
// '0 req/s'").
//
// @grafana/ui'nin eksen sarmalayıcısı tick ARTIŞINDAN ondalık türetip
// formatValue(v, decimals) diye çağırıyor; CorePanel o ikinci parametreyi
// yutuyordu. display processor'ın sözleşmesi burada kanıtlanır:
// `adjacentDecimals` verildiğinde ondalık artışa uyar ve sondaki sıfırlar
// kırpılır — küçük aralık okunur olur, büyük aralık bugünkü hâlinde kalır.
//
// İKİ BİRİM DE test edilir (feedback-unit-mixing-needs-both-branches):
// throughput panelleri 'reqps', latency panelleri 'ms'.
// ---------------------------------------------------------------------------
describe('display processor ondalığı (v0.9.799)', () => {
  function fmt(unit: string, value: number, decimals?: number): string {
    const [f] = spanSeriesToFrames([series([{ time: 60e9, value: 1 }])], { unit }, theme);
    const d = f.fields[1].display!(value, decimals);
    return `${d.text}${d.suffix ?? ''}`;
  }

  it('reqps · küçük aralık: ondalık VERİLİNCE tick okunur olur', () => {
    // decimalsForIncr(0.05) === 2 → CorePanel bunu geçiriyor.
    expect(fmt('reqps', 0.05, 2)).toBe('0.05 req/s');
    expect(fmt('reqps', 0.1, 2)).toBe('0.1 req/s');
    expect(fmt('reqps', 0.15, 2)).toBe('0.15 req/s');
    // Ondalıksız (v0.9.798 davranışı) gereksiz uzun: kırpılan eksende
    // operatörün gördüğü "0 req/s" tam buradan çıkıyordu.
    expect(fmt('reqps', 0.05)).toBe('0.0500 req/s');
  });

  it('reqps · büyük aralık: ondalık 0 → tam sayı, birim korunur', () => {
    expect(fmt('reqps', 100)).toBe('100 req/s');
    expect(fmt('reqps', 300)).toBe('300 req/s');
  });

  it('ms · her iki uçta da doğru (birim ÖLÇEKLEMESİ köprünün işi)', () => {
    expect(fmt('ms', 0.05, 2)).toBe('0.05 ms');
    expect(fmt('ms', 300)).toBe('300 ms');
    expect(fmt('ms', 1042)).toBe('1.04 s');
  });

  // v0.9.801 — OPERATÖR RAPORUNUN SAYISI. Explore'da
  // avg(http.server.request.duration) tooltip/lejant/eksende "0.348"
  // basıyordu; birim panele akınca aynı ham değer "348 ms" olur.
  //
  // ELLE ÖLÇEKLEME YOK: köprü 0.348'i olduğu gibi saklar, ×1000'i
  // @grafana/data'nın 's' formatlayıcısı yapar. Bu test o sözleşmenin
  // kanıtı — bir gün biri "ms'e çevirelim" diye çarpma eklerse ham
  // değer kontrolü (aşağıda) düşer.
  //
  // İKİ BİRİM DALI DA (feedback-unit-mixing-needs-both-branches): prod
  // semconv 'http.server.request.duration' SANİYE, lokal demo
  // 'http.server.duration' MİLİSANİYE basıyor — ikisi de canlı.
  it("süre metriği · 's' dalı: 0.348 → 348 ms (ölçekleme processor'ın)", () => {
    const [f] = spanSeriesToFrames(
      [series([{ time: 60e9, value: 0.348 }])], { unit: 's' }, theme);
    expect((f.fields[1].values as number[])[0]).toBe(0.348); // ham değer BOZULMAZ
    const d = f.fields[1].display!(0.348);
    expect(`${d.text}${d.suffix ?? ''}`).toBe('348 ms');
  });

  it("süre metriği · 'ms' dalı: 348 → 348 ms (aynı okuma)", () => {
    const [f] = spanSeriesToFrames(
      [series([{ time: 60e9, value: 348 }])], { unit: 'ms' }, theme);
    expect((f.fields[1].values as number[])[0]).toBe(348);
    const d = f.fields[1].display!(348);
    expect(`${d.text}${d.suffix ?? ''}`).toBe('348 ms');
  });

  it('birim YOKSA çıplak sayı — bu, hatanın kendisiydi', () => {
    // Regresyon çapası: tohum yolu birimi taşımazsa operatörün gördüğü
    // ekran TAM OLARAK budur. Testin varlığı "birimsiz = çıplak" olgusunu
    // sabitler; düzeltme birimin AKMASINDA, formatlayıcıda değil.
    expect(fmt('', 0.348)).toBe('0.348');
  });

  it("'s' dalı alt-saniyenin dışında da doğru okur", () => {
    expect(fmt('s', 1.5)).toBe('1.50 s');
    expect(fmt('s', 0.0005)).toBe('500 µs');
  });

  // SAPMA KAPISI: axisSize.decimalsForIncr, @grafana/ui'nin eksen
  // sarmalayıcısındaki guessDecimals(roundDecimals(incr, 6)) ifadesinin
  // ikizi (o kod bize kapalı, oluk hesabı için aynı sayıya ihtiyacımız
  // var). İkiz olduğu SÖYLENMEZ, kanıtlanır — ayrışırlarsa oluk bir
  // etikete, eksen başka bir etikete göre kurulur.
  it('decimalsForIncr ≡ guessDecimals(roundDecimals(incr, 6))', () => {
    for (const incr of [100, 50, 10, 1, 0.5, 0.2, 0.1, 0.05, 0.02, 0.001, 0.0001, 2.5, 0]) {
      expect(decimalsForIncr(incr), `incr=${incr}`)
        .toBe(guessDecimals(roundDecimals(incr, 6)));
    }
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

// ── v0.9.1368 regresyonu — ölçekli eksende tick'ler AYRIŞIR ──────────────
//
// Operatör-bildirimi: "Clusters altında memory hep aynı değeri
// gösteriyor." Dört y tick'i de "6.85 TiB", tooltip de "6.85 TiB", çizgi
// ise gözle görülür oynuyordu.
//
// Bu test SAF çekirdeği değil GERÇEK YOLU koşar: @grafana/data'nın
// binaryPrefix('B') display processor'ı + bizim tick planımız + ondalık
// kuralımız. Saf birim testi (axisSize.test.ts) kuralın kendisini
// çiviler; bu test kuralın DOĞRU BİÇİMLENDİRİCİYE bağlandığını çiviler —
// ikisi ayrı kusur sınıfı (v0.9.1363 dersi: parametre olarak geçen
// sonda kablolama testsiz kalır).
describe('v0.9.1368 — tick etiketleri ölçekli birimde ayrışır', () => {
  const TiB = 1024 ** 4;
  const GiB = 1024 ** 3;

  /** Panelin gerçek eksen etiketlerini üretir (CorePanel'in yaptığı sıra). */
  function axisLabels(values: number[], unit: string, heightPx = 180): string[] {
    const frames = spanSeriesToFrames(
      [{ groupKey: ['pod'], points: values.map((v, i) => ({ time: i * 60e9, value: v })) }],
      { unit },
    );
    const disp = frames[0].fields[1].display!;
    const ext = seriesExtent([values]);
    const [lo, hi] = paddedExtent(ext!);
    const plan = axisTickPlan(lo, hi, Math.max(40, heightPx - 34), false);
    const ref = scaleRefTick(plan.ticks);
    const dec = decimalsForScaledIncr(plan.incr, displayScaleOf(ref, disp(ref).text));
    return plan.ticks.map(v => {
      const d = disp(v, dec > 0 ? dec : undefined);
      return `${d.text}${d.suffix ?? ''}`;
    });
  }

  it('6.85 TiB civarında ~10 GiB genişliğinde bant — her tick FARKLI', () => {
    const base = 6.85 * TiB;
    const vals = [base, base + 3 * GiB, base + 6 * GiB, base + 10 * GiB];
    const labels = axisLabels(vals, 'bytes');
    expect(labels.length).toBeGreaterThan(1);
    expect(new Set(labels).size).toBe(labels.length);
    // ve gerçekten TiB okunuyor (birim kaybolmadı)
    expect(labels.every(l => l.endsWith('TiB'))).toBe(true);
  });

  it('BUG İSPATI: büyüklükten türeyen ondalık (2) aynı dizeyi üretirdi', () => {
    const base = 6.85 * TiB;
    const frames = spanSeriesToFrames(
      [{ groupKey: ['pod'], points: [{ time: 0, value: base }] }], { unit: 'bytes' });
    const disp = frames[0].fields[1].display!;
    // Eski yol: ondalık VERİLMEZ → @grafana/data büyüklükten türetir
    // (getDecimalsForValue, tavan 2) → 6.85 TiB'de ≈10 GiB çözünürlük.
    const old = [base, base + 2 * GiB, base + 4 * GiB]
      .map(v => { const d = disp(v); return `${d.text}${d.suffix ?? ''}`; });
    expect(old).toEqual(['6.85 TiB', '6.85 TiB', '6.85 TiB']); // ← operatörün gördüğü
    // Yeni yol AYNI değerleri ayrıştırır.
    const fixed = axisLabels([base, base + 2 * GiB, base + 4 * GiB], 'bytes');
    expect(new Set(fixed).size).toBe(fixed.length);
  });

  it('geniş bant (0 → 8 TiB) gereksiz ondalık ÜRETMEZ', () => {
    const labels = axisLabels([0, 2 * TiB, 5 * TiB, 8 * TiB], 'bytes');
    expect(new Set(labels).size).toBe(labels.length);
    // Ondalık SAYININ kendisinden sayılır — birim eki dahil edilirse
    // "1.82 TiB" altı ondalıklı görünür (bu testin ilk hâlinin kusuru).
    const decimalsOf = (l: string) => (/^-?\d+\.(\d+)/.exec(l)?.[1] ?? '').length;
    expect(Math.max(...labels.map(decimalsOf))).toBeLessThanOrEqual(2);
  });

  it('ölçeksiz birim (req/s) bugünkü davranışta kalır', () => {
    const labels = axisLabels([0, 100, 200, 300], 'reqps');
    expect(new Set(labels).size).toBe(labels.length);
    expect(labels.every(l => !l.includes('.'))).toBe(true);
  });

  it('ms→s ölçeğinde dar bant da ayrışır', () => {
    const labels = axisLabels([1000, 1005, 1010, 1015], 'ms');
    expect(new Set(labels).size).toBe(labels.length);
  });

  // Ölçek EN BÜYÜK tick'ten okunmalı. Biçimlendirici birimi DEĞER BAŞINA
  // seçiyor, yani bir eksen "994 ms" ile "1 s"i aynı anda taşıyabilir.
  // Ölçek en küçük tick'ten okunsaydı burada ms (×1) bulunur, adım büyük
  // görünür ve ondalık 0 çıkardı — dört etiketin ÜÇÜ çakışırdı. Bu vaka
  // mutasyon turunda `plan.ticks[0]` sapmasının ISIRMADIĞINI gösterdiği
  // için eklendi (v0.9.1368 self-review).
  it('birim sınırını AŞAN bant — ölçek en büyük tick\'ten okunur', () => {
    const labels = axisLabels([995, 1000, 1005, 1010], 'ms');
    expect(new Set(labels).size).toBe(labels.length);
  });
});
