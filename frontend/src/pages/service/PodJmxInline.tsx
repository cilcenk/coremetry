import { useMemo } from 'react';
import { useQuery, useQueries } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { Card } from '@/components/ui';
import { Spinner } from '@/components/Spinner';
import { MultiLineChart } from '@/components/MultiLineChart';
import { namedSeriesToSeries } from '@/pages/clusters/trendSeries';

// PodJmxInline (v0.9.155) — Infrastructure açılır-grup tasarımında (operatör
// onaylı mock A) bir pod satırı açılınca YERİNDE gösterilen JVM/JBoss JMX
// paneli: keşfedilen her jvm_/jboss_ metriği bu pod'a filtreli (clusterJmxTrend
// pod arg, v0.9.149; byPod=false → tek pod, jboss datasource'a gruplanır).
// Kendi keşfini yapar ama query anahtarı ServiceInfraTab'ınkiyle AYNI
// (['jmx-metrics', cluster, ns, deploy]) → aynı cluster'da ek fetch yok.
// "Tam detay → /pod" tam sayfaya gider (onFull). Fetch yalnız açılınca
// (bileşen ancak o zaman mount olur = fetch-on-expand).
export function PodJmxInline({ cluster, ns, deploy, pod, cFrom, cTo, onFull }: {
  cluster: string;
  ns: string;
  deploy: string;
  pod: string;
  cFrom: number;
  cTo: number;
  onFull: () => void;
}) {
  const metricsQ = useQuery({
    queryKey: ['jmx-metrics', cluster, ns, deploy],
    queryFn: () => api.clusterJmxMetrics(cluster, ns, deploy),
    staleTime: 60_000, retry: 1, enabled: !!cluster && !!ns && !!deploy,
  });
  const metrics = useMemo(() => metricsQ.data?.metrics ?? [], [metricsQ.data]);
  const panelQs = useQueries({
    queries: metrics.map(m => ({
      queryKey: ['jmx-trend', cluster, ns, deploy, m, false, pod, cFrom, cTo],
      queryFn: () => api.clusterJmxTrend(cluster, ns, deploy, m, false, cFrom, cTo, pod),
      staleTime: 60_000, retry: 1,
    })),
  });
  const anyData = panelQs.some(q => (q.data?.series?.length ?? 0) > 0);
  const loading = metricsQ.isLoading || (metrics.length > 0 && panelQs.some(q => q.isLoading) && !anyData);

  return (
    <div style={{
      padding: '12px 16px 14px', borderLeft: '2px solid var(--accent)',
      background: 'var(--panel2, var(--bg))',
    }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 10 }}>
        <span style={{ fontSize: 12, fontWeight: 600, color: 'var(--text2)' }}>
          JVM / JBoss (JMX) · <span className="mono">{pod}</span>
        </span>
        <button type="button" onClick={onFull} title="Tam pod detayı (RED + infra + JMX)"
          style={{
            all: 'unset', cursor: 'pointer', fontSize: 12, color: 'var(--accent2)',
            border: '1px solid var(--border)', borderRadius: 6, padding: '3px 10px',
          }}>
          Tam detay → /pod
        </button>
      </div>
      {metricsQ.isSuccess && metrics.length === 0 ? (
        <div style={{ fontSize: 12, color: 'var(--text3)' }}>Bu servis için Thanos'ta JMX metriği keşfedilmedi.</div>
      ) : loading ? (
        <Spinner />
      ) : !anyData ? (
        <div style={{ fontSize: 12, color: 'var(--text3)' }}>Bu pod için JMX serisi yok (pencerede veri gelmemiş olabilir).</div>
      ) : (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(0, 1fr))', gap: 12 }}>
          {metrics.map((m, i) => {
            const data = panelQs[i]?.data?.series;
            if (!data || data.length === 0) return null;
            const unit = m.includes('bytes') ? 'bytes' : m.includes('seconds') ? 's' : undefined;
            const isJboss = m.startsWith('jboss_');
            return (
              <Card key={m} header={m}>
                {/* Madde 4 sweep — pod'un JMX panelleri ortak crosshair
                    grubu (Pod.tsx JMX bölümüyle aynı anahtar). */}
                <MultiLineChart series={namedSeriesToSeries(data, m)} height={130}
                  unit={unit} maxSeries={isJboss ? 40 : undefined}
                  syncKey={`podjmx:${pod}`} />
              </Card>
            );
          })}
        </div>
      )}
    </div>
  );
}
