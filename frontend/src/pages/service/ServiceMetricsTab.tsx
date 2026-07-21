import { useMemo, useState } from 'react';
import { useQuery, useQueries } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { RuntimeCharts } from './RuntimeCharts';
import { MetricArea } from '@/pages/clusters/MetricArea';
import { timeRangeToNs } from '@/lib/utils';
import { Spinner, Empty } from '@/components/Spinner';
import type { TimeRange, JMXMetricKey } from '@/lib/types';

// ServiceMetricsTab (v0.9.139/140/143) — servis detayının "Metrics" sekmesi.
//
// Faz 1 (v0.9.139): Overview'dan taşınan OTel dil-runtime çizelgeleri
// (RuntimeCharts — JVM heap/GC/threads · .NET · Go, kaynağı CH).
//
// Faz 2 (v0.9.140, selector v0.9.143): Thanos'tan JBoss 8.x / JVM JMX
// panelleri (Prometheus JMX exporter). JMX serileri `job` label'ında
// SERVICE adını, `host_name` label'ında POD'u taşır (operatör 2026-07-21)
// → doğrudan `job=~"^<service>$"` ile filtrelenir; namespace/deploy
// metadata'sına gerek YOK. Infra sekmesiyle aynı keşif prensibi (v0.9.138):
// TÜM etkin Thanos kaynakları heap-probe'lanır, servisin JMX'i DÖNEN
// cluster'lar çip olur.

interface JmxPanel {
  key: JMXMetricKey;
  title: string;
  unit?: string;
  byLabel: string;    // toggle sağ şıkkı — JVM "By pod", datasource "By pool"
  byDefault: boolean; // varsayılan per-seri (pod/pool) mı
}

const JMX_PANELS: JmxPanel[] = [
  { key: 'heap',      title: 'JVM Heap used',        unit: 'bytes', byLabel: 'By pod',  byDefault: true },
  { key: 'nonheap',   title: 'JVM Non-heap used',    unit: 'bytes', byLabel: 'By pod',  byDefault: true },
  { key: 'gc',        title: 'GC time /s',           unit: 's',     byLabel: 'By pod',  byDefault: true },
  { key: 'threads',   title: 'JVM Threads',          byLabel: 'By pod',  byDefault: true },
  { key: 'ds_inuse',  title: 'XA-datasource in-use', byLabel: 'By pool', byDefault: true },
  { key: 'ds_active', title: 'XA-datasource active', byLabel: 'By pool', byDefault: true },
];

export function ServiceMetricsTab({ service, range, onZoom }: {
  service: string;
  range: TimeRange;
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
}) {
  // from/to unix NS — RuntimeCharts parent ile aynı RQ anahtarını paylaşır.
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  const sourcesQ = useQuery({
    queryKey: ['cluster-sources'],
    queryFn: () => api.clusterSources(),
    staleTime: 300_000,
  });
  const sources = useMemo(() => sourcesQ.data?.clusters ?? [], [sourcesQ.data]);

  // Sunucu 6h clamp'i (Infra/Clusters ile aynı dürüstlük).
  const { cFrom, cTo, clamped } = useMemo(() => {
    const sixH = 6 * 3600 * 1e9;
    if (to - from > sixH) return { cFrom: to - sixH, cTo: to, clamped: true };
    return { cFrom: from, cTo: to, clamped: false };
  }, [from, to]);

  // Keşif probe'u: cluster başına heap (total) — seri dönen cluster'da
  // servisin JBoss JMX'i var (job=~"^<service>$"). Kaynak sayısı Settings'te
  // sınırlı (bounded); 60s cache'li.
  const probeQs = useQueries({
    queries: sources.map(c => ({
      queryKey: ['jmx-trend', c, service, 'heap', false, cFrom, cTo],
      queryFn: () => api.clusterJmxTrend(c, service, 'heap', false, cFrom, cTo),
      staleTime: 60_000, retry: 1,
    })),
  });
  const probing = probeQs.some(q => q.isPending);
  const clustersWithJmx = useMemo(
    () => sources.filter((_c, i) => (probeQs[i]?.data?.series?.length ?? 0) > 0),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [sources, probeQs.map(q => q.data?.series?.length ?? 0).join(',')],
  );

  const [jcluster, setJcluster] = useState('');
  const chartCluster = jcluster || clustersWithJmx[0] || '';

  // Panel başına per-seri (By pod/pool) toggle — chart'ı rebuild etmeden.
  const [byMap, setByMap] = useState<Record<string, boolean>>({});
  const isBy = (p: JmxPanel) => byMap[p.key] ?? p.byDefault;
  const setBy = (k: string, v: boolean) => setByMap(m => ({ ...m, [k]: v }));

  const panelQs = useQueries({
    queries: JMX_PANELS.map(p => ({
      queryKey: ['jmx-trend', chartCluster, service, p.key, isBy(p), cFrom, cTo],
      queryFn: () => api.clusterJmxTrend(chartCluster, service, p.key, isBy(p), cFrom, cTo),
      staleTime: 60_000, retry: 1, enabled: chartCluster !== '',
    })),
  });

  return (
    <div>
      <RuntimeCharts service={service} from={from} to={to} onZoom={onZoom} />

      {/* ── JBoss (JMX) · Thanos ─────────────────────────────────────── */}
      <h3 style={{ fontSize: 13, margin: '20px 0 4px' }}>JBoss (JMX) · Thanos</h3>
      {/* Yükleme guard'ı (adversarial review) — sources pending'ken yanlış
          terminal boş-durum flash'ini önler; RuntimeCharts üstte zaten
          render oldu, yalnız bu bölüm bekler. */}
      {sourcesQ.isPending ? (
        <Spinner />
      ) : sources.length === 0 ? (
        <Empty icon="▦" title="No Thanos clusters configured">
          Add a remote cluster under Settings → Remote clusters to see JBoss/JVM JMX metrics here.
        </Empty>
      ) : probing && clustersWithJmx.length === 0 ? (
        <Spinner />
      ) : clustersWithJmx.length === 0 ? (
        <Empty icon="▦" title="No JBoss JMX series">
          No <span className="mono">jvm_*</span> / <span className="mono">jboss_*</span> series matched{' '}
          <span className="mono">{'job=~"^' + service + '$"'}</span> on any Thanos cluster. Check the JMX
          exporter is scraping this service (service carried as <span className="mono">job</span>, pod as{' '}
          <span className="mono">host_name</span>).
        </Empty>
      ) : (
        <>
          {/* Cluster çipleri — yalnız JMX serisi olan cluster'lar. */}
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', margin: '8px 0 12px' }}>
            {clustersWithJmx.map(c => {
              const active = chartCluster === c;
              return (
                <button key={c} type="button"
                  onClick={() => setJcluster(active ? '' : c)}
                  title={active ? 'Click to clear' : 'Filter to this cluster'}
                  style={{
                    all: 'unset', cursor: 'pointer', padding: '4px 10px', borderRadius: 14,
                    border: `1px solid ${active ? 'var(--accent)' : 'var(--border)'}`,
                    background: active ? 'var(--accent-soft)' : 'var(--bg2)', fontSize: 12,
                  }}>
                  <span className="mono" style={{ fontWeight: 600 }}>{c}</span>
                </button>
              );
            })}
          </div>

          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14 }}>
            {JMX_PANELS.map((p, i) => {
              const data = panelQs[i]?.data?.series;
              // Metrik ailesi bu cluster'da yoksa görünmez-düşer (MetricArea null).
              if (!data || data.length === 0) return null;
              return (
                <MetricArea key={p.key}
                  title={`${p.title} · ${chartCluster}${clamped ? ' (last 6h)' : ''}`}
                  byLabel={p.byLabel} by={isBy(p)} onToggle={v => setBy(p.key, v)}
                  series={data} seriesName={p.title} unit={p.unit} onZoom={onZoom} />
              );
            })}
          </div>
        </>
      )}
    </div>
  );
}
