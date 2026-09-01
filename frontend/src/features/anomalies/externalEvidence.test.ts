// v0.10.230 (Influx D5) — externalEvidence saf çekirdeği.
import { describe, it, expect } from 'vitest';
import { traceRows, evidenceCounts, pickSeries, labelKeys, labelValues, toChart, sevTone } from './externalEvidence';
import type { DeepEvidence } from '@/lib/types';

const deep: DeepEvidence = {
  external: {
    source: 'ggfail', query: 'tfail_adet', labels: { operation_code: 'OP1', ERRORCODE: 'E1' },
    current: 60, median: 5, mad: 1, z: 37, windowFromNs: 0, windowToNs: 1, rows: 4, invalidIds: 1, updatedNs: 1,
    spanSummary: [
      { traceId: 'b', startNs: 20, durationNs: 5, spans: 2, errorSpans: 1, rootService: 'w' },
      { traceId: 'c', startNs: 10, durationNs: 1, spans: 1, errorSpans: 0 },
    ],
  },
  traceIds: ['b', 'a'],
  affectedPods: [{ pod: 'p', count: 2, lastSeenNs: 1 }],
  logSignatures: [{ hash: 'h', template: 't', count: 3, severity: 'ERROR', sample: 's', traceCount: 2 }],
};

describe('traceRows', () => {
  it('liste sırasını korur, özeti olmayan id missing kalır, kırpılmış özet de gelir', () => {
    const rows = traceRows(deep);
    expect(rows.map(r => r.traceId)).toEqual(['b', 'a', 'c']);
    expect(rows[0].missing).toBeUndefined();
    expect(rows[0].rootService).toBe('w');
    expect(rows[1].missing).toBe(true);
  });
  it('kanıt yoksa boş', () => {
    expect(traceRows(undefined)).toEqual([]);
    expect(traceRows({})).toEqual([]);
  });
});

describe('evidenceCounts', () => {
  it('sayımları ve geçersiz id dürüstlüğünü taşır', () => {
    expect(evidenceCounts(deep)).toEqual({ traces: 2, withSpans: 2, pods: 1, signatures: 1, rows: 4, invalid: 1 });
    expect(evidenceCounts(undefined).traces).toBe(0);
  });
});

describe('pickSeries / labels / toChart', () => {
  it('groupKey sıralı birebir eşleşen seriyi seçer, yoksa null', () => {
    const series = [
      { groupKey: ['OP1', 'E2'], points: [] },
      { groupKey: ['OP1', 'E1'], points: [{ time: 2e9, value: 3 }, { time: 1e9, value: 1 }] },
    ];
    expect(pickSeries(series, ['OP1', 'E1'])?.points.length).toBe(2);
    expect(pickSeries(series, ['OP1'])).toBeNull();
    expect(pickSeries(null, ['OP1', 'E1'])).toBeNull();
    // filtreli (groupBy'sız) sorgu: tek seri, groupKey null/boş
    const single = [{ groupKey: null as unknown as string[], points: [{ time: 1e9, value: 2 }] }];
    expect(pickSeries(single, [])?.points.length).toBe(1);
  });
  it('etiket anahtarları alfabetik, değerler aynı sırada', () => {
    expect(labelKeys(deep.external!.labels)).toEqual(['ERRORCODE', 'operation_code']);
    expect(labelValues(deep.external!.labels)).toEqual(['E1', 'OP1']);
  });
  it('toChart ns→s ve zaman sırası; boş seri null', () => {
    const c = toChart({ groupKey: [], points: [{ time: 2e9, value: 3 }, { time: 1e9, value: 1 }] });
    expect(c).toEqual({ times: [1, 2], data: [1, 3] });
    expect(toChart({ groupKey: [], points: [] })).toBeNull();
    expect(toChart(null)).toBeNull();
  });
  it('sevTone', () => {
    expect(sevTone('ERROR')).toBe('b-err');
    expect(sevTone('warn')).toBe('b-warn');
    expect(sevTone('INFO')).toBe('b-gray');
  });
});
