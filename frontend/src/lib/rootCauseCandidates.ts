import type { ChangedService, RootCauseHypothesis } from '@/lib/types';

// rootCauseCandidates.ts — v0.10.700 (Dynatrace paritesi #2, dilim 1).
//
// Ribbon'un genişletilmiş "Ranked candidates" listesi bugüne dek YALNIZ canlı
// fan-out'un correlations'ını çiziyordu; işçinin kalıcı hipotez adayları
// (hop, path, kind, gerekçe, zamansal çarpan) hiç görünmüyordu. Kalıcı
// hipotez varsa onu çiz (operatör kararı 2026-09-12), yoksa eski yol.
// Skor ölçekleri farklı: hipotez 0..1 (yüzde), correlations serbest sayı.

export interface RibbonCandidate {
  service: string;
  scoreLabel: string;
  hops: number;
  reason?: string;
  kind?: string;
  temporalReason?: string;
  source: 'hypothesis' | 'live';
}

export function pctLabel(f: number): string {
  return `${Math.round(Math.max(0, Math.min(1, f)) * 100)}%`;
}

export function ribbonCandidates(rc: {
  service: string;
  correlations?: ChangedService[] | null;
  hypothesis?: RootCauseHypothesis | null;
}): RibbonCandidate[] {
  const hyp = rc.hypothesis?.candidates ?? [];
  if (hyp.length > 0) {
    return hyp
      .filter(c => c.service && c.service !== rc.service)
      .map(c => ({
        service: c.service,
        scoreLabel: pctLabel(c.score),
        hops: c.hops,
        reason: c.reason,
        kind: c.kind,
        temporalReason: c.temporalReason,
        source: 'hypothesis' as const,
      }));
  }
  return (rc.correlations ?? [])
    .filter(c => c.service && c.service !== rc.service)
    .map(c => ({
      service: c.service,
      scoreLabel: String(Math.round(c.score)),
      hops: 0,
      reason: c.reasons?.[0],
      source: 'live' as const,
    }))
    .sort((a, b) => Number(b.scoreLabel) - Number(a.scoreLabel));
}
