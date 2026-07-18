// Cluster görsel eşikleri (v0.9.31, design handoff — README
// "Thresholds (pctColor)"). Tema-farkında CSS var token'ları
// döndürür (globals.css: --err/--warn/--ok); hardcoded hex YOK.

// pctColor — kullanım yüzdesi: >85 err, >65 warn, else ok.
export function pctColor(pct: number): string {
  if (pct > 85) return 'var(--err)';
  if (pct > 65) return 'var(--warn)';
  return 'var(--ok)';
}

// restartColor — restart sayısı: >8 err, >2 warn, else muted.
export function restartColor(n: number): string {
  if (n > 8) return 'var(--err)';
  if (n > 2) return 'var(--warn)';
  return 'var(--text3)';
}

// safePct — payda 0/absent olduğunda güvenli yüzde (0..100), veya
// null (bilinmiyor → çağıran gauge/bar'ı gizler).
export function safePct(used?: number, capacity?: number): number | null {
  if (!capacity || capacity <= 0 || used == null) return null;
  const p = (used / capacity) * 100;
  return p < 0 ? 0 : p > 100 ? 100 : p;
}

// fmtCores — 0.003 → "3m" (millicore okunuşu), 1.25 → "1.25"
// (v0.9.51'de Clusters.tsx'ten taşındı — PodDrawer + Service→Infra
// sekmesi paylaşır).
export function fmtCores(v: number): string {
  if (v < 0.01) return `${Math.round(v * 1000)}m`;
  if (v < 1) return `${(v * 1000).toFixed(0)}m`;
  return v.toFixed(2);
}

// fmtBps — ağ hızı: fmtBytes + '/s' (0 = bilinmiyor → çağıran '—'
// basar). v0.9.51'de Clusters.tsx'ten taşındı (§8 ortak).
import { fmtBytes } from '@/lib/utils';
export function fmtBps(v: number): string {
  return `${fmtBytes(v)}/s`;
}

// podPhaseBadge — kube_pod_status_phase → badge sınıfı (v0.9.37;
// v0.9.51'de taşındı — Clusters tabloları + Service→Infra ortak).
export function podPhaseBadge(phase: string): string {
  switch (phase) {
    case 'Running': return 'b-ok';
    case 'Succeeded': return 'b-info';
    case 'Pending': return 'b-warn';
    case 'Failed': case 'Unknown': return 'b-err';
    default: return 'b-gray';
  }
}
