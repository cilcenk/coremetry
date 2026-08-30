// anomalyRegions.test.ts — v0.10.162 sözleşmesi (anomalyRegions.ts başlığı).
import { describe, it, expect } from 'vitest';
import { windowAnomalies, anomalyRegions, silencedSet, isSilenced, silenceKey, ANOMALY_KIND_COLOR } from './anomalyRegions';
import type { AnomalyEvent, AnomalySilence } from '@/lib/types';

const S = 1e9;
const ev = (over: Partial<AnomalyEvent>): AnomalyEvent => ({
  id: 'fp1', kind: 'trace_op', pattern: 'POST /v1/charges', service: 'payments-orchestrator',
  startedAt: 1000 * S, lastSeen: 1600 * S, peakRatio: 4.2, currentRatio: 3.1, currentCount: 12, sample: '', status: 'active', ...over,
});

describe('windowAnomalies', () => {
  it('servis + pencere kesişimi, başlangıca göre sıralı', () => {
    const items = [
      ev({ id: 'b', startedAt: 1500 * S, lastSeen: 1700 * S }),
      ev({ id: 'a' }),
      ev({ id: 'other', service: 'api-gateway' }),
      ev({ id: 'old', startedAt: 10 * S, lastSeen: 20 * S }),
      ev({ id: 'future', startedAt: 5000 * S, lastSeen: 5100 * S }),
    ];
    expect(windowAnomalies(items, 'payments-orchestrator', 900 * S, 2000 * S).map(e => e.id)).toEqual(['a', 'b']);
    expect(windowAnomalies(undefined, 'x', 0, 1)).toEqual([]);
    expect(windowAnomalies(items, '', 0, 1e18)).toEqual([]);
  });
});

describe('anomalyRegions', () => {
  it('bant [startedAt, lastSeen], tür rengi, «▮ tür ×tepe» etiketi', () => {
    const r = anomalyRegions([ev({})], new Set(), 900 * S, 2000 * S);
    expect(r).toHaveLength(1);
    expect(r[0]).toMatchObject({ fromSec: 1000, toSec: 1600, color: ANOMALY_KIND_COLOR.trace_op, label: 'trace_op ×4.2' }); // ▮ önekini drawTimeRegions ekler; id v0.10.180 (ayrı test)
  });
  it('anlık anomali en az pencerenin %0.4ü (min 1 s) genişliğinde — sıfır genişlik elenmesin', () => {
    const r = anomalyRegions([ev({ lastSeen: 1000 * S })], new Set(), 0, 10_000 * S);
    expect(r[0].toSec - r[0].fromSec).toBeCloseTo(40, 6);
    expect(r[0].endSec).toBe(1000); // v0.10.182 — gerçek bitiş şişmez
    const r2 = anomalyRegions([ev({ lastSeen: 1000 * S })], new Set(), 0, 100 * S);
    expect(r2[0].toSec - r2[0].fromSec).toBeCloseTo(1, 6);
  });
  it('sessiz → --text3 + «▮ sessiz»; renkler deploy (--purple) ve Problem (--err) ile çakışmaz', () => {
    const r = anomalyRegions([ev({})], silencedSet([{ id: 's', fingerprint: 'fp1', kind: 'trace_op', pattern: '', service: '', createdBy: 'c', createdAt: 0, untilAt: 0, reason: '', active: true } as AnomalySilence]), 0, 1e18);
    expect(r[0]).toMatchObject({ color: 'var(--text3)', label: 'sessiz' });
    for (const c of Object.values(ANOMALY_KIND_COLOR)) {
      expect(c).not.toBe('var(--purple)');
      expect(c).not.toBe('var(--err)');
    }
  });
  it('pasif susturma sayılmaz; kind|pattern|service anahtarı da eşleşir', () => {
    expect(silencedSet([{ fingerprint: 'fp1', active: false } as AnomalySilence]).has('fp1')).toBe(false);
    const e = ev({ id: 'sha' });
    expect(silenceKey(e)).toBe('trace_op|POST /v1/charges|payments-orchestrator');
    expect(isSilenced(e, new Set([silenceKey(e)]))).toBe(true);
    expect(isSilenced(e, new Set(['sha']))).toBe(true);
    expect(isSilenced(e, new Set(['other']))).toBe(false);
  });
});

// v0.10.180 — bölge id'si olayın id'si (banda tık → çekmece)
describe('anomalyRegions id', () => {
  it("her bölge olayın id'sini taşır", () => {
    const ev = { id: 'ev-1', kind: 'trace_op', pattern: 'x', service: 's', startedAt: 1_700_000_000e9, lastSeen: 1_700_000_600e9, peakRatio: 3, state: 'active' } as unknown as Parameters<typeof anomalyRegions>[0][number];
    expect(anomalyRegions([ev], new Set(), 1_699_999_000e9, 1_700_001_000e9)[0].id).toBe('ev-1');
  });
});

// v0.10.181 — «değil» kararı bandı soluk çizer (susturma gibi), etiket «değil»
describe('anomalyRegions verdict', () => {
  it("not_anomaly → --text3 + «değil»; anomaly → normal", () => {
    const base = { id: 'e', kind: 'trace_op', pattern: 'x', service: 's', startedAt: 1_700_000_000e9, lastSeen: 1_700_000_600e9, peakRatio: 3, currentRatio: 1, currentCount: 1, sample: '', status: 'active' } as unknown as Parameters<typeof anomalyRegions>[0][number];
    const no = anomalyRegions([{ ...base, verdict: 'not_anomaly' }], new Set(), 1_699_999_000e9, 1_700_001_000e9)[0];
    expect(no).toMatchObject({ color: 'var(--text3)', label: 'değil' });
    const yes = anomalyRegions([{ ...base, verdict: 'anomaly' }], new Set(), 1_699_999_000e9, 1_700_001_000e9)[0];
    expect(yes.label).toBe('trace_op ×3.0');
  });
});
