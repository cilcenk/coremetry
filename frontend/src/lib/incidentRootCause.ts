import type { RootCauseSummary } from '@/lib/types';

// incidentRootCause.ts — v0.10.698 (Dynatrace paritesi #1, dilim B).
//
// Incident listesi kolonu ve detay çipi aynı metni basar; üretici tek.
// Sunucu eşiği (0.05) zaten uyguladı: alan varsa gösterilir, yoksa "—".
// Yüzde yuvarlama RootCauseRibbon ile aynı (Math.round).
export function incidentRootCauseLabel(rc: RootCauseSummary | undefined | null): { suspect: string; pct: string } | null {
  if (!rc || rc.topSuspect === '') return null;
  const pct = Math.round(Math.max(0, Math.min(1, rc.confidence)) * 100) + '%';
  return { suspect: rc.topSuspect, pct };
}

// Sıralama anahtarı: güven; alan yoksa en alta (-1).
export function incidentRootCauseSort(rc: RootCauseSummary | undefined | null): number {
  return rc && rc.topSuspect !== '' ? rc.confidence : -1;
}
