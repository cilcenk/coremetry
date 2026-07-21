import { useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useQuery, useQueries } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { MetricArea } from '@/pages/clusters/MetricArea';
import { dsToken, reconcile, applyDsIsolate } from '@/pages/service/jmxSelectors';
import type { ClusterPodRow } from '@/lib/types';

// ServiceJmxPanels (v0.9.158) — servisin Thanos JVM/JBoss JMX panelleri
// (auto-discovery v0.9.144, Pod+DS seçiciler v0.9.149). Eskiden ServiceInfraTab
// içindeydi; operatör "Metrics→Pods, JVM metrikleri panellerde" isteğiyle Pods
// sekmesine taşınırken kendi bileşenine çıkarıldı. Verilen cluster'da
// keşfedilen her jvm_/jboss_ metriği için MetricArea; jpod (backend $pod
// filtresi) + jds (client datasource izolesi) URL kaynak-of-truth. Metrik
// yoksa görünmez-düşer (null).
export function ServiceJmxPanels({ cluster, effNs, effDeploy, cFrom, cTo, clamped, rows, onZoom }: {
  cluster: string;
  effNs: string;
  effDeploy: string;
  cFrom: number;
  cTo: number;
  clamped: boolean;
  rows: ClusterPodRow[];
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
}) {
  const [params, setParams] = useSearchParams();
  const [jmxBy, setJmxBy] = useState<Record<string, boolean>>({});
  const enabled = !!cluster && !!effNs && !!effDeploy;
  const jpod = params.get('jpod') ?? '';
  const jds = params.get('jds') ?? '';
  const setJmxParam = (key: 'jpod' | 'jds', v: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set(key, v); else next.delete(key);
    return next;
  }, { replace: true });

  const jmxMetricsQ = useQuery({
    queryKey: ['jmx-metrics', cluster, effNs, effDeploy],
    queryFn: () => api.clusterJmxMetrics(cluster, effNs, effDeploy),
    staleTime: 60_000, retry: 1, enabled,
  });
  const jmxMetrics = useMemo(() => jmxMetricsQ.data?.metrics ?? [], [jmxMetricsQ.data]);
  // Pod seçici opsiyonları — cluster VE effNs eşleşen pod'lar. effNs süzgeci
  // ŞART (backend namespace="effNs" sabitler); effJpod bayat değeri yok sayar
  // (review #2/#4/#5/#6). jmxSelectors.ts saf mantık + testli.
  const podOptions = [...new Set(
    rows.filter(r => r.cluster === cluster && r.namespace === effNs).map(r => r.pod),
  )].sort();
  const effJpod = reconcile(jpod, podOptions);
  const jmxPanelQs = useQueries({
    queries: jmxMetrics.map(m => {
      const byPod = jmxBy[m] ?? !m.startsWith('jboss_');
      return {
        queryKey: ['jmx-trend', cluster, effNs, effDeploy, m, byPod, effJpod, cFrom, cTo],
        queryFn: () => api.clusterJmxTrend(cluster, effNs, effDeploy, m, byPod, cFrom, cTo, effJpod),
        staleTime: 60_000, retry: 1,
      };
    }),
  });
  const dsOptions = [...new Set(
    jmxMetrics.flatMap((m, i) =>
      m.startsWith('jboss_') ? (jmxPanelQs[i]?.data?.series ?? []).map(s => dsToken(s.name)) : []),
  )].filter(Boolean).sort();
  const effJds = reconcile(jds, dsOptions);

  if (jmxMetrics.length === 0) return null;
  return (
    <>
      <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 12, margin: '18px 0 8px', flexWrap: 'wrap' }}>
        <h3 style={{ fontSize: 13, margin: 0 }}>
          JVM / JBoss (JMX) · <span className="mono">{cluster}</span>
          <span style={{ fontWeight: 400, color: 'var(--text3)' }}> · {jmxMetrics.length} metrics</span>
        </h3>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, fontSize: 11, color: 'var(--text3)' }}>
          {podOptions.length > 0 && (
            <label style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
              Pod
              <select value={effJpod} onChange={e => setJmxParam('jpod', e.target.value)}
                style={{ fontSize: 11, maxWidth: 220 }} title="Grafana $pod — sorguyu tek pod'a daraltır">
                <option value="">All pods</option>
                {podOptions.map(p => <option key={p} value={p}>{p}</option>)}
              </select>
            </label>
          )}
          {dsOptions.length > 0 && (
            <label style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
              Datasource
              <select value={effJds} onChange={e => setJmxParam('jds', e.target.value)}
                style={{ fontSize: 11, maxWidth: 220 }} title="Grafana $datasource — panelleri seçili datasource'a izole eder">
                <option value="">All datasources</option>
                {dsOptions.map(d => <option key={d} value={d}>{d}</option>)}
              </select>
            </label>
          )}
        </div>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14 }}>
        {jmxMetrics.map((m, i) => {
          const data = jmxPanelQs[i]?.data?.series;
          if (!data || data.length === 0) return null;
          const unit = m.includes('bytes') ? 'bytes' : m.includes('seconds') ? 's' : undefined;
          const isJboss = m.startsWith('jboss_');
          const shown = isJboss ? applyDsIsolate(data, effJds) : data;
          if (shown.length === 0) return null;
          return (
            <MetricArea key={m}
              title={`${m}${clamped ? ' (last 6h)' : ''}`}
              byLabel="By pod" totalLabel={isJboss ? 'By datasource' : 'Total'}
              by={jmxBy[m] ?? !isJboss} onToggle={v => setJmxBy(s => ({ ...s, [m]: v }))}
              series={shown} seriesName={m} unit={unit}
              maxSeries={isJboss ? 40 : undefined} onZoom={onZoom} />
          );
        })}
      </div>
    </>
  );
}
