import { describe, it, expect } from 'vitest';
import { resolvePodCluster } from './podResolve';
import type { ClusterPodRow } from '@/lib/types';

// v0.9.154 — regression guard for the /pod cluster/namespace resolver. The
// adversarial review confirmed that treating the drill's namespace as a HARD
// filter defeated the Metrics-tab integration when service.namespace (logical)
// diverged from the k8s namespace. These pin the two-pass behaviour.

const row = (cluster: string, namespace: string, pod: string): ClusterPodRow =>
  ({ cluster, namespace, pod, cpuCores: 0, memBytes: 0 } as ClusterPodRow);

describe('resolvePodCluster', () => {
  it('finds the pod via exact namespace when it matches (disambiguates)', () => {
    const r = resolvePodCluster(
      ['a', 'b'],
      [[row('a', 'prod', 'svc-1')], [row('b', 'prod', 'svc-1')]],
      'svc-1', 'prod',
    );
    // pass 1 returns the first cluster whose row matches ns — here 'a'.
    expect(r.cluster).toBe('a');
    expect(r.namespace).toBe('prod');
    expect(r.row?.pod).toBe('svc-1');
  });

  it('falls back to pod-only when the passed ns is LOGICAL (≠ k8s ns) — the review bug', () => {
    // Metrics drill passes service.namespace 'ecommerce'; the pod actually
    // lives in k8s ns 'prod'. Hard-filtering on 'ecommerce' would find nothing;
    // the fallback must still resolve the pod (and report its real k8s ns).
    const r = resolvePodCluster(
      ['a', 'b'],
      [[], [row('b', 'prod', 'checkout-7d9f-x2')]],
      'checkout-7d9f-x2', 'ecommerce',
    );
    expect(r.cluster).toBe('b');
    expect(r.namespace).toBe('prod'); // real k8s ns from the found row, not the logical one
    expect(r.row).toBeTruthy();
  });

  it('resolves pod-only when namespace is blank', () => {
    const r = resolvePodCluster(['a'], [[row('a', 'prod', 'p1')]], 'p1', '');
    expect(r.cluster).toBe('a');
    expect(r.row?.pod).toBe('p1');
  });

  it('prefers the ns-matching cluster over a same-name pod in another cluster', () => {
    // kafka-0 exists in both clusters; ns disambiguates to the right one.
    const r = resolvePodCluster(
      ['a', 'b'],
      [[row('a', 'kafka', 'kafka-0')], [row('b', 'messaging', 'kafka-0')]],
      'kafka-0', 'messaging',
    );
    expect(r.cluster).toBe('b');
  });

  it('returns the fallback cluster (no row) when the pod is not yet loaded', () => {
    // Infra drill passes ?cluster=a; clusterPods still empty → keep cluster
    // visible so the Infra section renders while the row fills in.
    const r = resolvePodCluster(['a'], [undefined], 'p1', 'prod', 'a');
    expect(r.cluster).toBe('a');
    expect(r.row).toBeUndefined();
  });

  it('empty everything → empty cluster, no row', () => {
    const r = resolvePodCluster([], [], 'p1', '');
    expect(r.cluster).toBe('');
    expect(r.row).toBeUndefined();
  });
});
