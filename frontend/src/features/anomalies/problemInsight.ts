// problemInsight.ts — v0.10.562 SAF: ProblemInsight → şerit hücreleri.
// Bilinmeyen hücre '—' (sebep uydurma yok); zaman yerel HH:MM; süre kısa.
import type { ProblemInsight } from '@/lib/types';
import { fmtEvidenceTime } from '@/lib/chatEvidence';

export interface InsightCell { key: string; label: string; text: string; href?: string; tone?: 'ok' | 'warn' | 'muted' }

export function fmtDurationShort(s: number): string {
  if (!(s > 0)) return '—';
  if (s < 90) return `${Math.round(s)} sn`;
  if (s < 5400) return `${Math.round(s / 60)} dk`;
  if (s < 172800) return `${(s / 3600).toFixed(1)} sa`;
  return `${Math.round(s / 86400)} gün`;
}

export function fmtDeltaTR(deltaS: number): string {
  const a = Math.abs(deltaS);
  const d = a < 90 ? `${Math.round(a)} sn` : a < 5400 ? `${Math.round(a / 60)} dk` : `${(a / 3600).toFixed(1)} sa`;
  return deltaS <= 0 ? `${d} önce` : `${d} sonra`;
}

export function insightCells(ins: ProblemInsight | null | undefined, serviceHref: (svc: string) => string): InsightCell[] {
  if (!ins) return [];
  const suspect: InsightCell = ins.hypothesisComputed && ins.topSuspect
    ? { key: 'suspect', label: 'şüpheli', text: `${ins.topSuspect} (${(ins.confidence ?? 0).toFixed(2)})`, href: serviceHref(ins.topSuspect), tone: 'warn' }
    : { key: 'suspect', label: 'şüpheli', text: ins.hypothesisComputed ? 'net sebep yok' : 'hipotez yok', tone: 'muted' };
  const anomaly: InsightCell = ins.firstAnomaly
    ? { key: 'anomaly', label: 'ilk anomali', text: `${fmtEvidenceTime(ins.firstAnomaly.at)} · ${ins.firstAnomaly.kind}` }
    : { key: 'anomaly', label: 'ilk anomali', text: '—', tone: 'muted' };
  const rollout: InsightCell = ins.rollout
    ? { key: 'rollout', label: 'rollout', text: `${ins.rollout.workload}${ins.rollout.version ? ' → ' + ins.rollout.version : ''} · ${fmtDeltaTR(ins.rollout.deltaS)}`, tone: Math.abs(ins.rollout.deltaS) <= 1800 ? 'warn' : undefined }
    : { key: 'rollout', label: 'rollout', text: '—', tone: 'muted' };
  const similar: InsightCell = ins.similar
    ? { key: 'similar', label: 'daha önce', text: `${ins.similar.count}× · son ${fmtDurationShort(ins.similar.lastDurationS)}${ins.similar.lastAssignee ? ' · ' + ins.similar.lastAssignee : ''}`, href: `/problems?problem=${encodeURIComponent(ins.similar.lastId)}` }
    : { key: 'similar', label: 'daha önce', text: 'ilk kez', tone: 'ok' };
  return [suspect, anomaly, rollout, similar];
}
