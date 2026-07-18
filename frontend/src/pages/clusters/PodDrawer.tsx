import { useMemo } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Drawer, DrawerSection } from '@/components/ui';
import { ThanosTrendPanel } from '@/pages/clusters/TrendPanel';
import { PromQLList } from '@/pages/clusters/PromQLList';
import { promQuote } from '@/pages/clusters/promQuote';
import { fmtCores } from '@/pages/clusters/thresholds';
import { timeRangeToNs, fmtBytes } from '@/lib/utils';
import type { ClusterPodRow, TimeRange } from '@/lib/types';

// PodDrawer — tek pod'un anlık kırılımı + dakika-bucket CPU/memory
// trendi + referans PromQL'leri. v0.9.51'de Clusters.tsx'ten çıkarıldı
// (design handoff §7+§8): Clusters pod tablosu ve Service→Infrastructure
// sekmesi AYNI drawer'ı kullanır; clustersLink=true Servis tarafında
// "Open in Clusters →" pivotunu ekler.

// TREND_WINDOWS — drawer'ın ?tw= yerel pencere seçenekleri ('' = sayfa
// range'i). URL'de yaşar: drawer link'le paylaşılınca pencere korunur.
export const TREND_WINDOWS = [
  { key: '', label: 'Page range' },
  { key: '15m', label: '15m', ns: 15 * 60 * 1e9 },
  { key: '1h', label: '1h', ns: 3600 * 1e9 },
  { key: '6h', label: '6h', ns: 6 * 3600 * 1e9 },
] as const;

export function PodDrawer({ cluster, namespace, pod, row, range, onClose, clustersLink }: {
  cluster: string;
  namespace: string;
  pod: string;
  row?: ClusterPodRow;
  range: TimeRange;
  onClose: () => void;
  clustersLink?: boolean;
}) {
  // Adaptif pencere: ?tw= URL'de (kaynak-of-truth), sayfa range'inden
  // bağımsız — AnomalyDetailDrawer'ın chartRange yaklaşımının
  // kullanıcı-seçimli hali. Date.now() memo'su yalnız tw değişince
  // koşar (timeRangeToNs'in kurulu semantiğiyle aynı).
  const [params, setParams] = useSearchParams();
  const tw = params.get('tw') ?? '';
  const setTw = (v: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('tw', v); else next.delete('tw');
    return next;
  }, { replace: true });
  const { from, to } = useMemo(() => {
    const w = TREND_WINDOWS.find(x => x.key === tw);
    if (w && 'ns' in w && w.ns) {
      const now = Date.now() * 1e6;
      return { from: now - w.ns, to: now };
    }
    return timeRangeToNs(range);
  }, [range, tw]);

  return (
    <Drawer onClose={onClose} header={
      <>
        <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 14, fontWeight: 600 }}>
          {pod}
        </span>
        <span className="badge b-gray" title="namespace">{namespace}</span>
        <span className="badge b-gray" title="cluster">{cluster}</span>
        {clustersLink && (
          <Link to={`/clusters?cluster=${encodeURIComponent(cluster)}&section=pods` +
            `&namespace=${encodeURIComponent(namespace)}` +
            `&pod=${encodeURIComponent(`${cluster}|${namespace}|${pod}`)}`}
            style={{ fontSize: 11, color: 'var(--accent)', whiteSpace: 'nowrap', marginLeft: 4 }}
            title="Open this pod on the Clusters page">
            Open in Clusters →
          </Link>
        )}
      </>
    }>
      {/* v0.8.580 — iki eksenli anlık kırılım (limit + request). */}
      {row && (
        <DrawerSection title="Current">
          <table style={{ width: '100%', fontSize: 12 }}>
            <thead>
              <tr style={{ color: 'var(--text3)', fontSize: 11, textAlign: 'left' }}>
                <th></th><th className="num">Usage</th>
                <th className="num">of limit</th><th className="num">of request</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td>CPU</td>
                <td className="num mono">{fmtCores(row.cpuCores)}</td>
                <td className="num mono">{row.cpuPct ? `${row.cpuPct.toFixed(0)}%` : '—'}</td>
                <td className="num mono" style={{
                  color: (row.cpuPctOfReq ?? 0) > 100 ? 'var(--warn)' : undefined,
                }}>{row.cpuPctOfReq ? `${row.cpuPctOfReq.toFixed(0)}%` : '—'}</td>
              </tr>
              <tr>
                <td>Memory</td>
                <td className="num mono">{fmtBytes(row.memBytes)}</td>
                <td className="num mono">{row.memPct ? `${row.memPct.toFixed(0)}%` : '—'}</td>
                <td className="num mono" style={{
                  color: (row.memPctOfReq ?? 0) > 100 ? 'var(--warn)' : undefined,
                }}>{row.memPctOfReq ? `${row.memPctOfReq.toFixed(0)}%` : '—'}</td>
              </tr>
            </tbody>
          </table>
        </DrawerSection>
      )}
      {/* v0.9.4 — Sparkline yerine tam MultiLineChart paneli
          (trend-upgrade audit T2): eksen + hover + limit/request
          threshold çizgileri. */}
      <DrawerSection title="Trend (per minute)">
        <div style={{ marginBottom: 8 }}>
          <select value={tw} onChange={e => setTw(e.target.value)}
            style={{ fontSize: 11 }}
            title="Trend window — independent of the page range">
            {TREND_WINDOWS.map(w => (
              <option key={w.key} value={w.key}>{w.label}</option>
            ))}
          </select>
        </div>
        <ThanosTrendPanel cluster={cluster} namespace={namespace} pod={pod}
          row={row} fromNs={from} toNs={to} />
      </DrawerSection>
      {/* v0.9.40 (handoff §7) — pod'a özel referans PromQL'ler. */}
      <DrawerSection title="Prometheus queries">
        <PromQLList queries={[
          ['CPU (cores)', `rate(container_cpu_usage_seconds_total{cluster="${promQuote(cluster)}",namespace="${promQuote(namespace)}",pod="${promQuote(pod)}"}[5m])`],
          ['Working-set memory', `container_memory_working_set_bytes{cluster="${promQuote(cluster)}",namespace="${promQuote(namespace)}",pod="${promQuote(pod)}"}`],
        ]} />
      </DrawerSection>
    </Drawer>
  );
}
