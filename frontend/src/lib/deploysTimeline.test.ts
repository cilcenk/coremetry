import { describe, expect, it } from 'vitest';
import { buildDeployTimeline, MERGE_TOLERANCE_NS } from './deploysTimeline';
import type { FleetRollout, RecentDeployEntry } from './types';

// v0.9.435 pinleri — verify bulgularının kalıcı kilidi: (1) sürümlü tek
// olay iki kaynakta iz bırakır → TEK deploy satırında birleşir (çift
// satır yok), zaman GERÇEK span zamanı; (2) eşleşmeyen churn kendi
// satırında; (3) podsChurn sayısal sıralama anahtarı; (4) kind filtresi.
// NOT: zaman sabitleri ≥60e9 ns aralıklı — 1e18 mertebesinde JS float
// ±1 ns'yi temsil edemez (AnnotationLane testinin aynı dersi).

const dep = (service: string, version: string, t: number): RecentDeployEntry =>
  ({ service, version, firstSeenNs: t, spanCount: 100 });
const ro = (service: string, kind: 'deploy' | 'restart', t: number,
  va?: string, vb?: string): FleetRollout => ({
  service, kind, timeUnixNs: t, podsAdded: 3, podsRemoved: 2, activePods: 3,
  addedPods: ['p-new-1'], removedPods: ['p-old-1'],
  versionAfter: va, versionBefore: vb,
} as FleetRollout);

const T = 1_000_000_000_000_000_000;

describe('buildDeployTimeline', () => {
  it('sürümlü olayın deploy+churn çifti TEK satırda birleşir', () => {
    const rows = buildDeployTimeline(
      [dep('checkout', 'v2', T)],
      [ro('checkout', 'deploy', T - 3 * 60 * 1e9, 'v2', 'v1')],
      '',
    );
    expect(rows).toHaveLength(1);
    expect(rows[0].kind).toBe('deploy');
    expect(rows[0].timeNs).toBe(T);            // gerçek span zamanı, bucket tabanı değil
    expect(rows[0].version).toBe('v1 → v2');   // churn'ün önce-sürümü katılır
    expect(rows[0].podsChurn).toBe(5);
    expect(rows[0].podsDelta).toContain('+3/−2');
  });

  it('tolerans dışı ya da farklı sürümlü churn birleşMEZ', () => {
    const far = buildDeployTimeline(
      [dep('checkout', 'v2', T)],
      [ro('checkout', 'deploy', T - MERGE_TOLERANCE_NS - 60e9, 'v2', 'v1')],
      '',
    );
    expect(far).toHaveLength(2);
    const otherVer = buildDeployTimeline(
      [dep('checkout', 'v2', T)],
      [ro('checkout', 'deploy', T, 'v3', 'v1')],
      '',
    );
    expect(otherVer).toHaveLength(2);
  });

  it('restart kendi satırında; kind filtresi çalışır', () => {
    const rows = buildDeployTimeline(
      [dep('a', 'v1', T)],
      [ro('b', 'restart', T - 1)],
      '',
    );
    expect(rows.map(r => r.kind).sort()).toEqual(['deploy', 'restart']);
    expect(buildDeployTimeline([dep('a', 'v1', T)], [ro('b', 'restart', T - 1)], 'restart'))
      .toHaveLength(1);
  });

  it('zaman desc sıralı; deploy satırında podsChurn undefined (sıralama anahtarı ayrı)', () => {
    const rows = buildDeployTimeline(
      [dep('a', 'v1', T - 60e9), dep('b', 'v1', T)],
      [],
      '',
    );
    expect(rows[0].service).toBe('b');
    expect(rows[0].podsChurn).toBeUndefined();
  });
});
