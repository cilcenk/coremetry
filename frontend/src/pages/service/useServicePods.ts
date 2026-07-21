import { useMemo } from 'react';
import { useQuery, useQueries } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { useServicesMetadata } from '@/lib/queries';
import { timeRangeToNs } from '@/lib/utils';
import { podMatchesService } from '@/pages/clusters/podWorkload';
import type { ClusterPodRow, TimeRange } from '@/lib/types';

// dominantNamespace — eşleşen pod'ların en sık namespace'i (v0.9.56):
// metadata ns türetilememişse grafik/JMX sorgularının namespace parametresi
// buradan gelir (yedek modda pod'lar zaten ada göre eşleşti).
function dominantNamespace(rows: ClusterPodRow[]): string {
  const counts = new Map<string, number>();
  for (const r of rows) {
    if (r.namespace) counts.set(r.namespace, (counts.get(r.namespace) ?? 0) + 1);
  }
  let best = '', n = 0;
  for (const [ns, c] of counts) {
    if (c > n || (c === n && ns < best)) { best = ns; n = c; }
  }
  return best;
}

// useServicePods (v0.9.158) — servisin Thanos pod envanteri, hem Infrastructure
// hem yeni Pods sekmesince paylaşılan VERİ KATMANI. Önceden ServiceInfraTab'ın
// içindeydi; Pods sekmesi de aynı eşleşmeye (rows/effNs/effDeploy) ihtiyaç
// duyduğundan hook'a çıkarıldı (fetch'ler cache-paylaşımlı, tekrar istek yok).
//
// Cluster keşfi TÜM etkin Thanos kaynaklarını tarar (v0.9.138); span-türetimi
// cluster SEÇİMİNDE kullanılmaz — hangi cluster'da pod var precise
// pod-eşleşmesi (podMatchesService, v0.9.130) belirler. Grafik parametreleri
// yedek modda da dolu: deploy yoksa servis adı, ns yoksa baskın namespace.
export function useServicePods(service: string, range: TimeRange) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  const metaQ = useServicesMetadata();
  const ns = metaQ.data?.[service]?.namespace ?? '';
  const deploy = metaQ.data?.[service]?.deployment ?? '';

  const sourcesQ = useQuery({
    queryKey: ['cluster-sources'],
    queryFn: () => api.clusterSources(),
    staleTime: 300_000,
  });
  const matched = useMemo(() => sourcesQ.data?.clusters ?? [], [sourcesQ.data]);

  const depQs = useQueries({
    queries: matched.map(c => ({
      queryKey: ['cluster-deployments', c, ns],
      queryFn: () => api.clusterDeployments(c, ns),
      staleTime: 60_000, retry: 1, enabled: ns !== '',
    })),
  });
  const podQs = useQueries({
    queries: matched.map(c => ({
      queryKey: ['cluster-pods', c],
      queryFn: () => api.clusterPods(c),
      staleTime: 60_000, retry: 1,
    })),
  });

  // Pod eşleşme (podMatchesService, testli): ns süzgeci + deploy varken podSet
  // ÜYELİĞİ ⋃ "<deploy>-" prefix VEYA yedek modda isim-eşitliği. Bilinçli
  // memo'suz: useQueries kimliği her render değişir, tarama ≤ birkaç bin satır.
  const rows: ClusterPodRow[] = [];
  matched.forEach((c, i) => {
    const depRow = deploy
      ? (depQs[i]?.data?.deployments ?? []).find(d => d.deployment === deploy)
      : undefined;
    const podSet = depRow ? new Set(depRow.podNames) : null;
    for (const p of podQs[i]?.data?.pods ?? []) {
      if (podMatchesService(p, { service, deploy, ns, podNames: podSet })) rows.push(p);
    }
  });
  const clustersWithPods = [...new Set(rows.map(r => r.cluster))];
  const effDeploy = deploy || service;
  const effNs = ns || dominantNamespace(rows);

  // Sunucu 6h clamp'i — Clusters Overview'la aynı dürüstlük (v0.9.21).
  const { cFrom, cTo, clamped } = useMemo(() => {
    const sixH = 6 * 3600 * 1e9;
    if (to - from > sixH) return { cFrom: to - sixH, cTo: to, clamped: true };
    return { cFrom: from, cTo: to, clamped: false };
  }, [from, to]);

  // Gate bit'leri (her iki sekme aynı boş/yükleniyor durumlarını gösterir).
  const sourcesPending = sourcesQ.isPending;
  const noClusters = (sourcesQ.data?.clusters ?? []).length === 0;
  const podsPending = podQs.some(q => q.isPending);

  return {
    metaQ, ns, deploy, matched, rows, clustersWithPods,
    effNs, effDeploy, from, to, cFrom, cTo, clamped,
    sourcesPending, noClusters, podsPending,
  };
}
