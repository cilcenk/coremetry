import { describe, expect, it } from 'vitest';
import { fmtDeltaTR, fmtDurationShort, insightCells } from './problemInsight';
import type { ProblemInsight } from '@/lib/types';

// v0.10.562 — insight şeridi saf hücreleri.
const base: ProblemInsight = {
  problemId: 'p', service: 's', kind: 'service', status: 'open', startedAt: 1, hypothesisComputed: true,
  topSuspect: 'ledger', confidence: 0.82,
  firstAnomaly: { at: 1_700_000_000_000_000_000, kind: 'trace_op', service: 's' },
  rollout: { workload: 'ledger', version: 'v2.14.0', timeUnixNs: 1, deltaS: -240 },
  similar: { count: 3, lastId: 'old', lastResolvedAt: 1, lastDurationS: 2460, lastAssignee: 'ops-core' },
  note: '',
};

describe('problemInsight', () => {
  it('dört hücre, dolu', () => {
    const cells = insightCells(base, s => `/service?name=${s}`);
    expect(cells.map(c => c.key)).toEqual(['suspect', 'anomaly', 'rollout', 'similar']);
    expect(cells[0]).toMatchObject({ text: 'ledger (0.82)', href: '/service?name=ledger', tone: 'warn' });
    expect(cells[2].text).toBe('ledger → v2.14.0 · 4 dk önce');
    expect(cells[3]).toMatchObject({ text: '3× · son 41 dk · ops-core', href: '/problems?problem=old' });
  });
  it('boş hücreler dürüst', () => {
    const cells = insightCells({ ...base, hypothesisComputed: false, topSuspect: undefined, firstAnomaly: null, rollout: null, similar: null }, s => s);
    expect(cells[0].text).toBe('hipotez yok');
    expect(cells[1].text).toBe('—');
    expect(cells[3]).toMatchObject({ text: 'ilk kez', tone: 'ok' });
    expect(insightCells(null, s => s)).toEqual([]);
  });
  it('süre / delta biçimi', () => {
    expect(fmtDurationShort(45)).toBe('45 sn');
    expect(fmtDurationShort(2460)).toBe('41 dk');
    expect(fmtDurationShort(7200)).toBe('2.0 sa');
    expect(fmtDeltaTR(600)).toBe('10 dk sonra');
    expect(fmtDeltaTR(-30)).toBe('30 sn önce');
  });
});
