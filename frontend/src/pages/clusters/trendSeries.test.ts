import { describe, expect, it } from 'vitest';
import { limitThresholds, netTrendToSeries, thanosPodSeriesToSeries, thanosTrendToSeries, shortSeriesLabel } from './trendSeries';

// v0.9.4 — Thanos→MultiLineChart dönüşüm sözleşmeleri: saniye→ns,
// boş trend → boş seri, pod sırası korunur, 0-limit çizgi üretmez.

const T = [
  { bucket: 1784271060, cpuCores: 0.5, memBytes: 100 },
  { bucket: 1784271120, cpuCores: 0.7, memBytes: 200 },
];

describe('thanosTrendToSeries', () => {
  it('converts buckets (s) to time (ns) and picks the axis', () => {
    const s = thanosTrendToSeries(T, 'CPU', t => t.cpuCores);
    expect(s).toHaveLength(1);
    expect(s[0].groupKey).toEqual(['CPU']);
    expect(s[0].points[0]).toEqual({ time: 1784271060 * 1e9, value: 0.5 });
    expect(s[0].points[1].value).toBe(0.7);
  });

  it('empty trend → empty series (chart renders its own empty state)', () => {
    expect(thanosTrendToSeries([], 'CPU', t => t.cpuCores)).toEqual([]);
  });
});

describe('thanosPodSeriesToSeries', () => {
  it('one series per pod, server order preserved, empty pods dropped', () => {
    const s = thanosPodSeriesToSeries([
      { pod: 'busy', trend: T },
      { pod: 'gone', trend: [] },
      { pod: 'idle', trend: [T[0]] },
    ], t => t.memBytes);
    expect(s.map(x => x.groupKey[0])).toEqual(['busy', 'idle']);
    expect(s[0].points[1].value).toBe(200);
  });
});

describe('limitThresholds', () => {
  it('limit=err, request=warn', () => {
    expect(limitThresholds(2, 1)).toEqual([
      { value: 2, label: 'limit', severity: 'err' },
      { value: 1, label: 'request', severity: 'warn' },
    ]);
  });

  it('0/undefined draws nothing (unknown contract)', () => {
    expect(limitThresholds(0, undefined)).toEqual([]);
  });

  it('unit rides the label', () => {
    expect(limitThresholds(2, 0, 'cores')[0].label).toBe('limit (cores)');
  });
});

describe('netTrendToSeries', () => {
  it('two series (in/out), seconds to ns', () => {
    const s = netTrendToSeries([{ bucket: 1784271060, inBps: 100, outBps: 50 }]);
    expect(s.map(x => x.groupKey[0])).toEqual(['Net in (B/s)', 'Net out (B/s)']);
    expect(s[0].points[0]).toEqual({ time: 1784271060 * 1e9, value: 100 });
    expect(s[1].points[0].value).toBe(50);
  });
  it('empty trend → empty', () => {
    expect(netTrendToSeries([])).toEqual([]);
  });
});

// v0.9.539 — operatör: "lejantta isim gösterimleri Grafana gibi
// olabilir mi?" Grafana'da seri adları kısa ("lckhsdbp04"); bizde tam
// pod adı geliyordu (41 karakter) ve lejant üç sütuna taşıp grafiği
// aşağı itiyordu. Ortak önek (deployment adı) HER seride aynı olduğu
// için ayırt edici değil.
describe('shortSeriesLabel (v0.9.539)', () => {
  const dep = 'mobile-crm-dashboard-bff';

  it('ortak deployment öneki atılır — geriye ayırt edici kısım kalır', () => {
    expect(shortSeriesLabel('mobile-crm-dashboard-bff-57bdc7975b-59c8q', 'CPU', dep))
      .toBe('57bdc7975b-59c8q');
  });

  it('önek verilmezse tam ad (eski davranış bayt-bayt)', () => {
    expect(shortSeriesLabel('mobile-crm-dashboard-bff-57bdc7975b-59c8q', 'CPU'))
      .toBe('mobile-crm-dashboard-bff-57bdc7975b-59c8q');
  });

  it('önek YALNIZ tam segment sınırında atılır — kısmi ad çakışması kırpmaz', () => {
    // "mobile-crm" öneki "mobile-crmx-..." pod'unu kırparsa etiket yalan olur.
    expect(shortSeriesLabel('mobile-crmx-abc-123', 'CPU', 'mobile-crm'))
      .toBe('mobile-crmx-abc-123');
  });

  it('oneagent varyantı kendi kimliğini korur', () => {
    expect(shortSeriesLabel('mobile-crm-dashboard-bff-oneagent-d4487ff99-rkr9s', 'CPU', dep))
      .toBe('oneagent-d4487ff99-rkr9s');
  });

  it('ad boşsa fallback (Total modunda tek seri)', () => {
    expect(shortSeriesLabel('', 'CPU', dep)).toBe('CPU');
    expect(shortSeriesLabel('   ', 'Memory')).toBe('Memory');
  });

  it('kırpma sonrası boş kalırsa ham ad korunur — etiketsiz seri ayırt edilemez', () => {
    expect(shortSeriesLabel('svc-', 'CPU', 'svc')).toBe('svc-');
    expect(shortSeriesLabel('svc', 'CPU', 'svc')).toBe('svc');
  });
});
