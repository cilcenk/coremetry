import type { ClusterPodRow } from '@/lib/types';

// resolvePodCluster (v0.9.154) — given a pod name and the pod lists of the
// candidate Thanos clusters, find which cluster the pod lives in (+ its row).
//
// Two-pass, because the namespace passed on a /pod drill is NOT always the k8s
// namespace: the Metrics-tab drill carries useServicesMetadata().namespace,
// which the backend derives with service.namespace (an SDK-logical value)
// taking precedence over k8s.namespace.name. Matching that as a HARD filter
// against ClusterPodRow.namespace (the real k8s ns from Thanos/kube-state)
// excludes the correct pod whenever the two diverge — silently killing the
// Infra + JMX sections the drill was meant to show (adversarial review, v0.9.154).
//
// So: pass 1 uses namespace as a DISAMBIGUATOR (prefer the cluster whose row's
// ns matches — resolves same-name pods across clusters correctly); pass 2 falls
// back to pod-name-only so a logical/blank namespace still finds the pod. When
// pass 2 has to break a cross-cluster name tie it takes the first source
// (a known, low-severity limitation for statically-named pods like kafka-0).
export function resolvePodCluster(
  clusters: string[],
  podsByCluster: (ClusterPodRow[] | null | undefined)[],
  pod: string,
  nsParam: string,
  fallbackCluster = '',
): { cluster: string; namespace: string; row: ClusterPodRow | undefined } {
  const scan = (strictNs: boolean) => {
    for (let i = 0; i < clusters.length; i++) {
      const found = (podsByCluster[i] ?? []).find(
        p => p.pod === pod && (!strictNs || !nsParam || p.namespace === nsParam));
      if (found) return { cluster: clusters[i], namespace: found.namespace, row: found };
    }
    return null;
  };
  // fallbackCluster (the ?cluster= param, if any) keeps the Infra/Clusters
  // drill's Infra section visible immediately while clusterPods is still
  // loading — the row fills in once the scan finds it.
  return scan(true) ?? scan(false) ?? { cluster: fallbackCluster, namespace: nsParam, row: undefined };
}
