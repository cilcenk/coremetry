import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import {
  ROUTE_TOP_N, routeMoreNote, topRoutesByArea, metricUnitToGrafana, metricAvgToMs,
} from './routeSeries';
import type { SpanMetricSeries } from '@/lib/types';

// v0.9.774 — panelin iki saf kararı: kırpma notu + birim eşlemesi.

function ser(route: string, values: number[]): SpanMetricSeries {
  return {
    groupKey: [route],
    points: values.map((v, i) => ({ time: (i + 1) * 60e9, value: v })),
  };
}

describe('routeMoreNote', () => {
  const cases: { name: string; total: number; shown: number; capped: boolean; want: string | null }[] = [
    { name: 'kırpma yok → not yok', total: 3, shown: 3, capped: false, want: null },
    {
      name: 'kırpıldı → sayılar + ölçüt',
      total: 14, shown: 10, capped: false,
      want: '10 seri · +4 daha (alan bazlı: Σ|değer| en yüksek 10)',
    },
    {
      name: 'kırpıldı + satır tavanı → ikisi de',
      total: 14, shown: 10, capped: true,
      want: '10 seri · +4 daha (alan bazlı: Σ|değer| en yüksek 10) · ⚠ satır tavanı doldu — liste eksik olabilir',
    },
    {
      name: 'kırpma yok ama satır tavanı → yalnız tavan',
      total: 2, shown: 2, capped: true,
      want: '⚠ satır tavanı doldu — liste eksik olabilir',
    },
  ];
  for (const c of cases) {
    it(c.name, () => {
      expect(routeMoreNote(c.total, c.shown, 10, c.capped)).toBe(c.want);
    });
  }

  it('negatif fark üretmez (sunucu total < shown bildirirse)', () => {
    expect(routeMoreNote(1, 3, 10, false)).toBeNull();
  });
});

describe('topRoutesByArea', () => {
  const many = [
    ser('/a', [1, 1]),      // alan 2
    ser('/b', [100, 100]),  // alan 200
    ser('/c', [10, 10]),    // alan 20
  ];

  it('alana göre azalan sıralar', () => {
    const { items } = topRoutesByArea(many);
    expect(items.map(i => i.name)).toEqual(['/b', '/c', '/a']);
  });

  it('cap kadar seri çizer, gerisini nota yazar', () => {
    const { items, note } = topRoutesByArea(many, 2);
    expect(items).toHaveLength(2);
    expect(items.map(i => i.name)).toEqual(['/b', '/c']);
    expect(note).toBe('2 seri · +1 daha (alan bazlı: Σ|değer| en yüksek 2)');
  });

  it('rowsCapped notu taşınır', () => {
    const { note } = topRoutesByArea(many, 3, true);
    expect(note).toBe('⚠ satır tavanı doldu — liste eksik olabilir');
  });

  it('boş/null giriş → boş item, not yok', () => {
    expect(topRoutesByArea(null)).toEqual({ items: [], note: null });
    expect(topRoutesByArea([])).toEqual({ items: [], note: null });
  });

  it('item şekli CorePanelMulti sözleşmesi: tek seri, data rolü', () => {
    const { items } = topRoutesByArea([ser('/x', [5])]);
    expect(items[0]).toEqual({ name: '/x', role: 'data', series: [ser('/x', [5])] });
  });

  it('varsayılan cap 10', () => {
    expect(ROUTE_TOP_N).toBe(10);
    const wide = Array.from({ length: 25 }, (_, i) => ser(`/r${i}`, [i + 1]));
    expect(topRoutesByArea(wide).items).toHaveLength(10);
  });

  it('adsız groupKey lejantta görünmez satır bırakmaz', () => {
    const { items } = topRoutesByArea([{ groupKey: [''], points: [{ time: 1, value: 1 }] }]);
    expect(items[0].name).toBe('(adsız)');
  });
});

// Unit-mixing kuralı: HER dal. Prod 's', lokal 'ms' üretiyor — ikisi de
// canlı yol, biri sessizce bozulursa eksen 1000× yanlış okunur.
describe('metricUnitToGrafana', () => {
  const cases: { in: string | undefined; want: 's' | 'ms' | undefined }[] = [
    { in: 's', want: 's' },
    { in: 'S', want: 's' },
    { in: ' s ', want: 's' },
    { in: 'sec', want: 's' },
    { in: 'secs', want: 's' },
    { in: 'second', want: 's' },
    { in: 'seconds', want: 's' },
    { in: 'ms', want: 'ms' },
    { in: 'MS', want: 'ms' },
    { in: 'millisecond', want: 'ms' },
    { in: 'milliseconds', want: 'ms' },
    // TANINMAYAN → undefined (tahmin YOK).
    { in: '', want: undefined },
    { in: '   ', want: undefined },
    { in: undefined, want: undefined },
    { in: 'By', want: undefined },
    { in: 'requests', want: undefined },
    { in: 'us', want: undefined },
    { in: 'ns', want: undefined },
  ];
  for (const c of cases) {
    it(`${JSON.stringify(c.in)} → ${String(c.want)}`, () => {
      expect(metricUnitToGrafana(c.in)).toBe(c.want);
    });
  }
});

// v0.9.799 — withTotalPrefix testleri SİLİNDİ: fonksiyonun tek
// tüketicisi panelin "Toplam" çizgisiydi, o da kaldırıldı (operatör
// netleştirmesi). Ölü kod için ölü test bırakmıyoruz.

// ── metricAvgToMs (v0.9.798) ────────────────────────────────────────────
//
// HER İKİ BİRİM DALI da test edilir — bu kod tabanının kayıtlı dersi
// (feedback-unit-mixing-needs-both-branches): prod 's', lokal 'ms'
// üretiyor, yani eksen-dışı dal gerçekten canlıda çalışıyor.

describe('metricAvgToMs', () => {
  it("ms → aynen", () => {
    expect(metricAvgToMs(42.5, 'ms')).toBe(42.5);
    expect(metricAvgToMs(42.5, 'milliseconds')).toBe(42.5);
  });
  it("s → ×1000 (PROD dalı)", () => {
    expect(metricAvgToMs(0.0425, 's')).toBeCloseTo(42.5, 6);
    expect(metricAvgToMs(1, 'seconds')).toBe(1000);
  });
  it('tanınmayan birim → null (TAHMİN YOK, çağıran span\'e düşer)', () => {
    expect(metricAvgToMs(42.5, undefined)).toBeNull();
    expect(metricAvgToMs(42.5, '')).toBeNull();
    expect(metricAvgToMs(42.5, 'By')).toBeNull();
    expect(metricAvgToMs(42.5, 'us')).toBeNull();
  });
  it('değer yok / sonlu değil → null', () => {
    expect(metricAvgToMs(null, 'ms')).toBeNull();
    expect(metricAvgToMs(undefined, 'ms')).toBeNull();
    expect(metricAvgToMs(NaN, 'ms')).toBeNull();
    expect(metricAvgToMs(Infinity, 's')).toBeNull();
  });
  it('0 meşru bir değer (null değil)', () => {
    expect(metricAvgToMs(0, 'ms')).toBe(0);
    expect(metricAvgToMs(0, 's')).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// v0.9.799 — "Toplam" büyük grafiklerden KALKTI + karo kaynak birliği.
//
// v0.9.798 Toplam'ı hem üst KPI karolarına hem büyük panellere koydu;
// operatörün kastı YALNIZ karolardı ("kalın çizgili hali kötü, eski hali
// daha iyiydi"). Panel v0.9.796'daki saf route kırılımına döndü, karolar
// aynen kaldı. Kapılar iki şeyi çiviler: (a) çizginin gerçekten kalktığı,
// (b) karoların DEĞER/SPARKLINE/DELTA üçlüsünün TEK kaynaktan geldiği —
// karışık kaynak, operatörün "hangisine bakayım" sorusunu geri getirir.
// ---------------------------------------------------------------------------
describe('Service Overview — Toplam çizgisi ve karo kaynak birliği (v0.9.799)', () => {
  const src = readFileSync(
    resolve(__dirname, '../Overview.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  it('büyük paneller SAF route kırılımı — Toplam item\'ı ve vurgu YOK', () => {
    expect(src).not.toMatch(/\bemphasis\b/);
    expect(src).not.toMatch(/name: 'Toplam'/);
    expect(src).not.toMatch(/withTotalPrefix/);
    // Response time paneli doğrudan kırpma çıktısını çiziyor.
    expect(src).toMatch(/items=\{rtView\.items\}/);
    // Kırpma notu yine sade.
    expect(src).toMatch(/rtView\.note,/);
  });

  it('grupsuz sorgu YAŞIYOR — karo onu okuyor (panelde değil)', () => {
    expect(src).toMatch(/'service-rt-avg-total'/);
    expect(src).toMatch(/const rtTotalSeries = rtTotalQ\.data\?\.series\?\.\[0\];/);
    // Panelin yükleme kapısı artık grupsuz sorguyu BEKLEMİYOR.
    expect(src).toMatch(/loading=\{metricTputQ\.isLoading \|\| rtAvgQ\.isLoading\}/);
  });

  it('🔴 Response time karosu: değer + sparkline + delta AYNI metrik serisinden', () => {
    expect(src).toMatch(/val=\{rtFromMetric \? \(rtAvgMsNow as number\)\.toFixed\(0\) : p99Ms\.toFixed\(0\)\}/);
    expect(src).toMatch(/spark=\{rtFromMetric \? rtAvgMsSeries : vals\(lat\?\.p99\)\}/);
    expect(src).toMatch(/delta=\{computeDelta\(rtFromMetric \? rtAvgMsSeries : vals\(lat\?\.p99\)\)\}/);
  });

  it('🔴 Throughput karosu: değer + sparkline + delta AYNI metrik serisinden', () => {
    expect(src).toMatch(/spark=\{tputFromMetric \? metricRpsSeries : vals\(lat\?\.rate\)\}/);
    expect(src).toMatch(/delta=\{computeDelta\(tputFromMetric \? metricRpsSeries : vals\(lat\?\.rate\)\)\}/);
    // Değer de aynı seriden (son nokta).
    expect(src).toMatch(/const metricRpsNow = metricRpsSeries\.slice\(-1\)\[0\];/);
  });

  it('düşüş KARO BAŞINA ayrı — tek bayrak ikisini kilitlemez', () => {
    expect(src).toMatch(/const rtFromMetric = rtAvgMsNow != null;/);
    expect(src).toMatch(/const tputFromMetric = metricRpsNow != null;/);
    // Ve başlık kaynağı SÖYLÜYOR (dürüstlük deseni).
    expect(src).toMatch(/'Response time · P99 \(span\)'/);
    expect(src).toMatch(/'Throughput \(span\)'/);
  });
});
