import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { MultiLineChart } from '@/components/MultiLineChart';
import { Spinner, Empty } from '@/components/Spinner';
import { api } from '@/lib/api';
import { limitThresholds, thanosPodSeriesToSeries, thanosTrendToSeries } from './trendSeries';
import type { ClusterPodRow } from '@/lib/types';

// ThanosTrendPanel — Thanos trend grafiklerinin YERLEŞİM-BAĞIMSIZ
// modülü (v0.9.4, trend-upgrade audit §2). Bugün drawer'da, yarın
// sekmeli detay layout'unda AYNI bileşen mount edilir — taşıma sıfır.
//
// CPU ve Memory AYRI iki MultiLineChart (birimler tek eksene
// sığmaz); syncKey crosshair'ı senkronlar (Endpoints modal emsali).
// Threshold katmanı: limit (err) + request (warn) yatay çizgileri —
// yalnız tekil-pod modunda (multi-pod'da pod başına limit karışır).
// Deploy marker'lar T4'te (pod↔servis korelasyonuna kapılı) gelir.
//
// Fetch panel mount'unda (drawer açılışı = fetch-on-open); staleTime
// = sunucu TTL'i. Drag-zoom görünüm keşfidir, fetch tetiklemez
// (audit §2.4 öncelik kararı); pencere (from/to) değişince chart
// yeni veriyle kurulur, zoom doğal sıfırlanır.

export function ThanosTrendPanel({ cluster, namespace, pod, row, fromNs, toNs }: {
  cluster: string;
  namespace: string;
  // pod verilirse tekil-pod modu; verilmezse multi-pod (namespace).
  pod?: string;
  // Tekil modda threshold kaynakları listedeki satırdan gelir
  // (deep-link'te satır yoksa çizgiler atlanır — veri yine çizilir).
  row?: ClusterPodRow;
  fromNs: number;
  toNs: number;
}) {
  const single = !!pod;
  const podQ = useQuery({
    queryKey: ['cluster-pod-detail', cluster, namespace, pod, fromNs, toNs],
    queryFn: () => api.clusterPodDetail(cluster, namespace, pod!, fromNs, toNs),
    staleTime: 60_000,
    enabled: single,
  });
  const multiQ = useQuery({
    queryKey: ['cluster-ns-pods-trend', cluster, namespace, fromNs, toNs],
    queryFn: () => api.clusterNamespacePodsTrend(cluster, namespace, fromNs, toNs),
    staleTime: 60_000,
    enabled: !single,
  });
  const q = single ? podQ : multiQ;

  const { cpuSeries, memSeries, totalPods } = useMemo(() => {
    if (single) {
      const trend = podQ.data?.trend ?? [];
      return {
        cpuSeries: thanosTrendToSeries(trend, 'CPU (cores)', t => t.cpuCores),
        memSeries: thanosTrendToSeries(trend, 'Memory (bytes)', t => t.memBytes),
        totalPods: 0,
      };
    }
    const pods = multiQ.data?.pods ?? [];
    return {
      cpuSeries: thanosPodSeriesToSeries(pods, t => t.cpuCores),
      memSeries: thanosPodSeriesToSeries(pods, t => t.memBytes),
      totalPods: multiQ.data?.totalPods ?? 0,
    };
  }, [single, podQ.data, multiQ.data]);

  if (q.isPending) return <Spinner />;
  if (q.isError) return <Empty icon="✗" title="Failed to load trend" />;
  if (cpuSeries.length === 0 && memSeries.length === 0) {
    return <div style={{ fontSize: 12, color: 'var(--text3)' }}>No samples in this window.</div>;
  }

  const syncKey = `thanos-trend-${cluster}-${namespace}-${pod ?? ''}`;
  const shownPods = cpuSeries.length;
  return (
    <div style={{ display: 'grid', gap: 12 }}>
      {!single && totalPods > shownPods && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          Top {shownPods} of {totalPods} pods by average CPU.
        </div>
      )}
      <div>
        <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 4 }}>CPU (cores)</div>
        <MultiLineChart series={cpuSeries} height={180} syncKey={syncKey}
          thresholds={single ? limitThresholds(row?.cpuLimitCores, row?.cpuRequestCores, 'cores') : undefined} />
      </div>
      <div>
        <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 4 }}>Memory (bytes)</div>
        <MultiLineChart series={memSeries} height={180} syncKey={syncKey}
          thresholds={single ? limitThresholds(row?.memLimitBytes, row?.memRequestBytes, 'bytes') : undefined} />
      </div>
    </div>
  );
}
