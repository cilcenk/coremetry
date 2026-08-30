// rolloutRow.ts — v0.10.201 (ROLLOUTS Faz 4): satır kimliği, upsert
// (updated_at monotonluğu — ReplacingMergeTree'nin istemci ikizi), durum
// tonu, süre, kısa revizyon. Saf; rolloutRow.test.ts pinler.
import type { WorkloadRollout } from './types';

export function rolloutKey(r: Pick<WorkloadRollout, 'clusterId' | 'namespace' | 'workload' | 'revision' | 'startedAt'>): string {
  return `${r.clusterId}|${r.namespace}|${r.workload}|${r.revision}|${r.startedAt}`;
}

/** Gelen satır mevcut olanı yalnız updatedAt DAHA BÜYÜKSE ezer; yeni kimlik eklenir. Sıra korunmaz (çağıran sıralar). */
export function upsertRollouts(rows: WorkloadRollout[], incoming: WorkloadRollout[]): WorkloadRollout[] {
  if (incoming.length === 0) return rows;
  const idx = new Map<string, number>();
  rows.forEach((r, i) => idx.set(rolloutKey(r), i));
  const out = rows.slice();
  for (const inc of incoming) {
    const k = rolloutKey(inc);
    const i = idx.get(k);
    if (i === undefined) { idx.set(k, out.length); out.push(inc); continue; }
    if ((inc.updatedAt ?? 0) > (out[i].updatedAt ?? 0)) out[i] = inc;
  }
  return out;
}

export type RolloutTone = 'info' | 'success' | 'danger' | 'warning' | 'neutral';

export function statusTone(status: string): RolloutTone {
  switch (status) {
    case 'in_progress': return 'info';
    case 'completed': return 'success';
    case 'rolled_back': return 'danger';
    case 'superseded': return 'neutral';
    case 'stalled': return 'warning';
    default: return 'neutral';
  }
}

export function statusLabel(status: string): string {
  switch (status) {
    case 'in_progress': return 'sürüyor';
    case 'completed': return 'tamamlandı';
    case 'rolled_back': return 'geri alındı';
    case 'superseded': return 'devralındı';
    case 'stalled': return 'takıldı';
    default: return status || '—';
  }
}

/** Süre (sn): tamamlandıysa completedAt − startedAt, değilse now − startedAt. */
export function rolloutDurationSec(r: Pick<WorkloadRollout, 'startedAt' | 'completedAt'>, nowMs: number): number {
  const end = r.completedAt && r.completedAt > 0 ? r.completedAt : nowMs;
  return Math.max(0, Math.round((end - r.startedAt) / 1000));
}

/** `<workload>-<hash>` → `<hash>`; önek yoksa olduğu gibi. */
export function shortRevision(revision: string, workload: string): string {
  if (workload && revision.startsWith(workload + '-')) return revision.slice(workload.length + 1);
  return revision;
}

/** İmaj diff etiketi: tag değiştiyse `eski → yeni`, aynıysa tek; imaj yoksa '—'. */
export function imageDiff(r: Pick<WorkloadRollout, 'imageTag' | 'prevImageTag'>): string {
  const cur = r.imageTag || '';
  const prev = r.prevImageTag || '';
  if (!cur && !prev) return '—';
  if (prev && prev !== cur) return `${prev} → ${cur || '?'}`;
  return cur || prev;
}

// ── Çekmece URL codec'i (v0.10.203) — rolloutHref.test.ts pinler ──────────
export interface RolloutIdParam { clusterId: string; namespace: string; workload: string; revision: string; startedAt: number }

// Ayraç '|': encodeURIComponent onu %7C'ye kaçırır ('~' KAÇMAZ — unreserved;
// '~' ayraçlı ilk sürüm workload'daki '~' ile bölünüyordu, rolloutHref.test).
export function encodeRolloutParam(r: RolloutIdParam): string {
  return [r.clusterId, r.namespace, r.workload, r.revision, String(r.startedAt)].map(encodeURIComponent).join('|');
}

/** Bozuk/eksik token → null (çekmece açılmaz; sayfa kırılmaz). */
export function decodeRolloutParam(s: string | null): RolloutIdParam | null {
  if (!s) return null;
  const parts = s.split('|');
  if (parts.length !== 5) return null;
  try {
    const [clusterId, namespace, workload, revision, ts] = parts.map(decodeURIComponent);
    const startedAt = Number(ts);
    if (!clusterId || !workload || !revision || !Number.isFinite(startedAt) || startedAt <= 0) return null;
    return { clusterId, namespace, workload, revision, startedAt };
  } catch {
    return null;
  }
}

