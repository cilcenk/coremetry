import type { FleetRollout, RecentDeployEntry } from './types';

// deploysTimeline (v0.9.435) — /deploys sayfasının SAF satır kurucusu.
// İki kaynağı tek çizelgeye indirir ve verify bulgusunu çözer: sürüm
// değiştiren TEK gerçek olay iki kaynakta da iz bırakır (GetDeploys'ta
// ilk-görülme + churn'de Kind='deploy') — eşleşen çift TEK "deploy"
// satırında birleşir (pod-churn kanıtı deploy satırına eklenir; zaman
// olarak GERÇEK span zamanı esas, churn'ün 5-dk bucket tabanı değil).
// Eşleşme: aynı servis + rollout.versionAfter === deploy.version +
// |Δt| ≤ MERGE_TOLERANCE. Eşleşmeyen churn'ler kendi satırında kalır
// (rollout = sürümlü churn'ün deploy kaydı yoksa; restart = sürümsüz).

export const MERGE_TOLERANCE_NS = 10 * 60 * 1e9; // 10 dk

export type TimelineRow = {
  timeNs: number;
  kind: 'deploy' | 'rollout' | 'restart';
  service: string;
  version: string;
  podsDelta: string;
  podsChurn?: number; // sayısal sıralama anahtarı (added+removed)
  podsTitle?: string;
  spanCount?: number;
};

function churnBits(ro: FleetRollout): Pick<TimelineRow, 'podsDelta' | 'podsChurn' | 'podsTitle'> {
  return {
    podsDelta: `+${ro.podsAdded}/−${ro.podsRemoved} (aktif ${ro.activePods})`,
    podsChurn: ro.podsAdded + ro.podsRemoved,
    podsTitle: [
      ro.addedPods?.length ? `Yeni: ${ro.addedPods.join(', ')}` : '',
      ro.removedPods?.length ? `Giden: ${ro.removedPods.join(', ')}` : '',
    ].filter(Boolean).join(' · ') || undefined,
  };
}

export function buildDeployTimeline(
  deploys: RecentDeployEntry[],
  rollouts: FleetRollout[],
  kindFilter: string,
): TimelineRow[] {
  const usedRollout = new Set<number>();
  const out: TimelineRow[] = [];

  for (const dep of deploys) {
    const row: TimelineRow = {
      timeNs: dep.firstSeenNs, kind: 'deploy', service: dep.service,
      version: dep.version, podsDelta: '—', spanCount: dep.spanCount,
    };
    // Aynı olayın churn ikizini ara ve deploy satırına kat.
    for (let i = 0; i < rollouts.length; i++) {
      if (usedRollout.has(i)) continue;
      const ro = rollouts[i];
      if (ro.service !== dep.service) continue;
      if (ro.kind !== 'deploy') continue;
      if ((ro.versionAfter ?? '') !== dep.version) continue;
      if (Math.abs(ro.timeUnixNs - dep.firstSeenNs) > MERGE_TOLERANCE_NS) continue;
      usedRollout.add(i);
      Object.assign(row, churnBits(ro));
      if (ro.versionBefore && ro.versionBefore !== dep.version) {
        row.version = `${ro.versionBefore} → ${dep.version}`;
      }
      break;
    }
    out.push(row);
  }

  rollouts.forEach((ro, i) => {
    if (usedRollout.has(i)) return;
    const kind = ro.kind === 'deploy' ? 'rollout' : 'restart';
    const ver = ro.versionBefore && ro.versionAfter && ro.versionBefore !== ro.versionAfter
      ? `${ro.versionBefore} → ${ro.versionAfter}`
      : (ro.versionAfter || ro.versionBefore || '—');
    out.push({
      timeNs: ro.timeUnixNs, kind, service: ro.service, version: ver, ...churnBits(ro),
    });
  });

  const filtered = kindFilter ? out.filter(r => r.kind === kindFilter) : out;
  filtered.sort((a, b) => b.timeNs - a.timeNs);
  return filtered;
}
