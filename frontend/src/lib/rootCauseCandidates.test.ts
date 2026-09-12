import { describe, it, expect } from 'vitest';
import { ribbonCandidates, pctLabel } from './rootCauseCandidates';
import type { ChangedService, RootCauseHypothesis } from '@/lib/types';

// v0.10.700 — ribbon adayları: kalıcı hipotez varsa o (sıra korunur, kendisi
// süzülür, yüzde etiketi, zamansal gerekçe), yoksa canlı correlations.
const corr = (service: string, score: number): ChangedService => ({
  service, baselineRate: 0, currentRate: 0, rateDeltaPct: 0, baselineErrorRate: 0,
  currentErrorRate: 0, errDeltaPct: 0, baselineP99Ms: 0, currentP99Ms: 0, p99DeltaPct: 0,
  score, reasons: [`r-${service}`],
});
const hyp = (cands: RootCauseHypothesis['candidates']): RootCauseHypothesis => ({
  anchorKind: 'problem', anchorId: 'p', service: 'shop-api', computedAt: 0,
  topSuspect: cands[0]?.service ?? '', topScore: 0, confidence: 0, candidates: cands, version: 1,
});

describe('ribbonCandidates', () => {
  it('hipotez adayları öncelikli; kendisi süzülür; yüzde + zamansal gerekçe', () => {
    const got = ribbonCandidates({
      service: 'shop-api',
      correlations: [corr('shop-x', 9)],
      hypothesis: hyp([
        { service: 'shop-db', score: 0.42, hops: 1, reason: 'downstream', temporalReason: 'co-moves (ρ=0.8, leads by 1×5m)', structural: 0.42, temporal: 0.8 },
        { service: 'shop-api', score: 0.3, hops: 0 },
        { service: 'node:w1', score: 0.2, hops: 0, kind: 'node' },
      ]),
    });
    expect(got.map(c => c.service)).toEqual(['shop-db', 'node:w1']);
    expect(got[0]).toMatchObject({ scoreLabel: '42%', hops: 1, temporalReason: 'co-moves (ρ=0.8, leads by 1×5m)', source: 'hypothesis' });
    expect(got[1].kind).toBe('node');
  });
  it('hipotez yoksa canlı correlations, skora göre sıralı, kendisi süzülür', () => {
    const got = ribbonCandidates({ service: 'shop-api', correlations: [corr('shop-api', 99), corr('a', 3), corr('b', 7)] });
    expect(got.map(c => c.service)).toEqual(['b', 'a']);
    expect(got[0]).toMatchObject({ scoreLabel: '7', hops: 0, reason: 'r-b', source: 'live' });
  });
  it('boş hipotez listesi canlıya düşer; hiçbiri yoksa boş', () => {
    expect(ribbonCandidates({ service: 's', correlations: [corr('a', 1)], hypothesis: hyp([]) })[0].service).toBe('a');
    expect(ribbonCandidates({ service: 's' })).toEqual([]);
  });
  it('pctLabel kırpar ve yuvarlar', () => {
    expect(pctLabel(0.746)).toBe('75%');
    expect(pctLabel(1.4)).toBe('100%');
    expect(pctLabel(-1)).toBe('0%');
  });
});
