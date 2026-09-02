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

/** v0.10.234 — Operator-reported: durum rozeti ("devralındı") olayın NE olduğunu
 * söylemiyordu. Durumun anlamı (reconcile.go §DURUM) ipucu olarak. */
export function statusTitle(status: string): string {
  switch (status) {
    case 'in_progress': return 'Yeni revizyon trafik alıyor; eski revizyon henüz tamamen çekilmedi';
    case 'completed': return 'Yeni revizyon tek aktif revizyon; eskileri çekildi';
    case 'rolled_back': return 'Bu revizyon çekildi ve yerine ÖNCEKİ revizyon geri döndü';
    case 'superseded': return 'Bu revizyon çekildi; yerini önceki değil BAŞKA bir revizyon aldı (devralındı)';
    case 'stalled': return 'Yeni revizyon ilerlemiyor: pod\'lar hazır değil ya da trafik teyit edilmedi';
    default: return '';
  }
}

/** v0.10.234 — Operator-reported: "imaj değiştiyse deployment, değişmediyse
 * rollout (config change)". Değişikliğin TÜRÜ imaj kimliğinden (repo:tag)
 * türetilir; önceki revizyon bilinmiyorsa kıyas yoktur (ilk gözlem). */
export type RolloutChangeKind = 'deployment' | 'config' | 'initial' | 'unknown';
export function rolloutChangeKind(r: Pick<WorkloadRollout, 'image' | 'imageTag' | 'prevImage' | 'prevImageTag' | 'prevRevision'>): RolloutChangeKind {
  if (!r.prevRevision) return 'initial';
  const cur = `${r.image || ''}:${r.imageTag || ''}`;
  const prev = `${r.prevImage || ''}:${r.prevImageTag || ''}`;
  if (cur === ':' && prev === ':') return 'unknown';
  if (cur === ':' || prev === ':') return 'unknown';
  return cur === prev ? 'config' : 'deployment';
}
export function changeKindLabel(k: RolloutChangeKind): string {
  switch (k) {
    case 'deployment': return 'Deployment';
    case 'config': return 'Rollout (config)';
    case 'initial': return 'ilk gözlem';
    default: return 'bilinmiyor';
  }
}
export function changeKindTitle(k: RolloutChangeKind): string {
  switch (k) {
    case 'deployment': return 'İmaj değişti (eski → yeni tag): yeni sürüm yayını';
    case 'config': return 'İmaj aynı, yalnız revizyon değişti: config/env/secret/replica değişikliği ya da yeniden başlatma';
    case 'initial': return 'Önceki revizyon bilinmiyor — Coremetry bu iş yükünü ilk kez gördü, kıyas yok';
    default: return 'İmaj bilgisi eksik (span\'ler container.image taşımıyor) — tür belirlenemedi';
  }
}
export function changeKindTone(k: RolloutChangeKind): RolloutTone {
  switch (k) {
    case 'deployment': return 'info';
    case 'config': return 'neutral';
    default: return 'neutral';
  }
}
/** Tam imaj kimliği `repo:tag`; ikisi de boşsa '—'. */
export function imageRef(image: string, tag: string): string {
  if (!image && !tag) return '—';
  if (!tag) return image;
  if (!image) return `:${tag}`;
  return `${image}:${tag}`;
}

/** Süre (sn): tamamlandıysa completedAt − startedAt, değilse now − startedAt. */
export function rolloutDurationSec(r: Pick<WorkloadRollout, 'startedAt' | 'completedAt'>, nowMs: number): number {
  const end = r.completedAt && r.completedAt > 0 ? r.completedAt : nowMs;
  return Math.max(0, Math.round((end - r.startedAt) / 1000));
}

/** v0.10.211 — Traces pivot filtreleri TÜRE göre: Deployment'ta revizyon bir
 * ReplicaSet adıdır; STS/DS'de revizyon imaj TAG'idir (MV vekili), replicaset
 * filtresi hiçbir span'e denk gelmezdi — workload adı + imaj tag'iyle süz. */
export function rolloutTracesFilters(r: Pick<WorkloadRollout, 'kind' | 'namespace' | 'workload' | 'revision'>): { k: string; op: string; v: string[] }[] {
  const ns = { k: 'resource.k8s.namespace.name', op: '=', v: [r.namespace] };
  if (r.kind === 'StatefulSet' || r.kind === 'DaemonSet') {
    const kindKey = r.kind === 'StatefulSet' ? 'resource.k8s.statefulset.name' : 'resource.k8s.daemonset.name';
    return [{ k: kindKey, op: '=', v: [r.workload] }, { k: 'resource.container.image.tag', op: '=', v: [r.revision] }, ns];
  }
  return [{ k: 'resource.k8s.replicaset.name', op: '=', v: [r.revision] }, ns];
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

