import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import { useDebouncedValue } from '@/lib/perf/useDebouncedValue';
import type { TimeRange, MetricInfo, SpanMetricSeries } from '@/lib/types';
import { TimeSeriesPanel, type TSSeries } from '@/components/viz/TimeSeriesPanel';
import { HistogramHeatmap } from '@/components/HistogramHeatmap';
import { Sparkline } from '@/components/Sparkline';
import { Spinner, Empty } from '@/components/Spinner';

// MetricsExplorer (v0.7.115 → v0.8 Phase 1A) — the design-handoff metrics
// explorer that replaces the bare query-builder default: a left catalog
// (server-side search + group facets + scrollable list), a large focused
// Grafana-grade chart for the selected metric (value + vs-prior delta + deploy
// markers, drag-zoom, synced hover), and a 2-column sparkline grid of the other
// metrics (click to focus). Catalog = the metric registry via the SERVER-SIDE
// search endpoint (api.metricNamesSearch — NOT the eager api.metricNames('')
// full-catalogue load, which is fatal at 10k+ distinct names, scale-audit #10).
// Every chart binds to api.metricQuery. The advanced query-builder stays one
// toggle away (see Metrics.tsx).

type MGroup = 'http' | 'rpc' | 'runtime' | 'db' | 'messaging' | 'other';
const GROUPS: { key: MGroup | 'all'; label: string }[] = [
  { key: 'all', label: 'All' }, { key: 'http', label: 'HTTP' }, { key: 'rpc', label: 'RPC' },
  { key: 'runtime', label: 'Runtime' }, { key: 'db', label: 'Database' }, { key: 'messaging', label: 'Messaging' },
];

// Classify a metric into a facet group by its OTel name prefix.
function metricGroup(name: string): MGroup {
  const n = name.toLowerCase();
  if (n.startsWith('http')) return 'http';
  if (n.startsWith('rpc')) return 'rpc';
  if (n.startsWith('db') || n.startsWith('database') || /(redis|oracle|postgres|mysql|mongo)/.test(n)) return 'db';
  if (n.startsWith('messaging') || /(kafka|rabbit|queue|consumer)/.test(n)) return 'messaging';
  if (/^(jvm|process|go\.|system|runtime|dotnet|nodejs|python)/.test(n)) return 'runtime';
  return 'other';
}

function vals(s?: SpanMetricSeries[] | null): number[] {
  return s && s[0] ? s[0].points.map(p => p.value) : [];
}
function fmtVal(v: number): string {
  if (!isFinite(v)) return '—';
  if (Math.abs(v) >= 1000) return fmtNum(Math.round(v));
  return v.toFixed(Math.abs(v) < 10 ? 2 : 1);
}
// Trend delta vs the prior window — first-third mean vs last-third mean.
function computeDelta(arr: number[]): { pct: string; dir: 'up' | 'down' | 'flat' } | null {
  if (arr.length < 6) return null;
  const third = Math.max(1, Math.floor(arr.length / 3));
  const mean = (xs: number[]) => xs.reduce((a, b) => a + b, 0) / (xs.length || 1);
  const prev = mean(arr.slice(0, third));
  const cur = mean(arr.slice(-third));
  if (prev === 0) return null;
  const d = ((cur - prev) / prev) * 100;
  return { pct: Math.abs(d).toFixed(1), dir: d > 0.5 ? 'up' : d < -0.5 ? 'down' : 'flat' };
}

export function MetricsExplorer({ range }: { range: TimeRange }) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const [search, setSearch] = useState('');
  const [facet, setFacet] = useState<MGroup | 'all'>('all');
  const [picked, setPicked] = useState('');

  // SERVER-SIDE search (scale-audit #10). Debounced so a typing burst fires one
  // request, bounded to 200 rows server-side. The eager api.metricNames('')
  // pulled the entire catalogue to the client — fatal at 10k+ metric names.
  const dq = useDebouncedValue(search.trim(), 250);
  const catalogQ = useQuery({
    queryKey: ['metric-catalog', dq],
    queryFn: () => api.metricNamesSearch('', dq || undefined, 200, 0),
    staleTime: 60_000,
  });
  const catalog = useMemo<MetricInfo[]>(() => catalogQ.data?.names ?? [], [catalogQ.data]);
  const hasMore = catalogQ.data?.hasMore ?? false;

  // Facet counts over the (server-bounded) result set. Labelled "in this page"
  // implicitly — at 200-row bound this is the searchable working set, not a
  // global census (which we deliberately don't compute at scale).
  const counts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const m of catalog) { const g = metricGroup(m.name); c[g] = (c[g] ?? 0) + 1; }
    return c;
  }, [catalog]);

  // The substring is already applied server-side via dq; the facet narrows the
  // bounded result. (Substring kept here only for mid-debounce snappiness.)
  const filtered = useMemo(() => catalog.filter(m =>
    (facet === 'all' || metricGroup(m.name) === facet) &&
    (!search || m.name.toLowerCase().includes(search.toLowerCase())),
  ), [catalog, facet, search]);

  // Focused metric = the operator's pick, else the first in the filtered list.
  const active = picked && filtered.some(m => m.name === picked) ? picked : (filtered[0]?.name ?? '');
  const activeMeta = catalog.find(m => m.name === active);
  // OTLP explicit/exponential histogram instruments carry a distribution the
  // avg line throws away — render those as the bucket heatmap instead (reusing
  // the same HistogramHeatmap the advanced builder uses). MetricInfo.type
  // mirrors the OTel instrument kind.
  const isHistogram = activeMeta?.type === 'histogram';

  const seriesQ = useQuery({
    queryKey: ['metric-series', active, from, to],
    queryFn: () => api.metricQuery({ name: active, agg: 'avg', from, to }),
    enabled: !!active && !isHistogram,
    staleTime: 30_000,
  });
  const histQ = useQuery({
    queryKey: ['metric-hist', active, from, to],
    queryFn: () => api.metricHistogram({ name: active, from, to }),
    enabled: !!active && isHistogram,
    staleTime: 30_000,
  });
  const focusVals = vals(seriesQ.data);
  const delta = computeDelta(focusVals);
  const lastVal = focusVals.length ? focusVals[focusVals.length - 1] : NaN;

  // MEMOISE the series literal handed to TimeSeriesPanel (scale-audit #4). A
  // fresh array/object each render destroyed + recreated the uPlot instance
  // every render (the chart deps on `series` identity). Keyed on the query's
  // data identity + the active metric/unit so it only changes when the data
  // actually changes.
  const focusSeries = useMemo<TSSeries[]>(() => {
    const pts = seriesQ.data?.[0]?.points ?? [];
    if (pts.length < 2 || !active) return [];
    return [{
      label: active,
      color: 'var(--accent)',
      unit: activeMeta?.unit || undefined,
      points: pts.map(p => ({ time: p.time, value: p.value })),
    }];
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [seriesQ.data, active, activeMeta?.unit]);

  return (
    <div className="metrics-explorer">
      {/* ── Left catalog ──────────────────────────────────────────── */}
      <div className="card metric-catalog">
        <div style={{ padding: 10 }}>
          <input className="field" placeholder="Search metrics…" value={search}
            onChange={e => setSearch(e.target.value)} style={{ width: '100%' }} />
          <div className="ov-logbar" style={{ marginTop: 8, gap: 4, marginBottom: 0 }}>
            {GROUPS.map(g => (
              <span key={g.key} className={'ov-facet' + (facet === g.key ? ' on' : '')} onClick={() => setFacet(g.key)}>
                {g.label}{g.key !== 'all' && <span className="n">{counts[g.key] ?? 0}</span>}
              </span>
            ))}
          </div>
        </div>
        <div className="metric-list">
          {catalogQ.isLoading ? <div style={{ padding: 16 }}><Spinner /></div>
            : filtered.length === 0 ? <div style={{ padding: 16, color: 'var(--text2)', fontSize: 12 }}>No metrics match.</div>
            : <>
              {filtered.map(m => (
                <button key={m.name} className={'metric-row' + (m.name === active ? ' active' : '')} onClick={() => setPicked(m.name)}>
                  <div className="mono metric-row-name" title={m.name}>{m.name}</div>
                  <div className="metric-row-meta">{m.unit || '·'} · {m.type}</div>
                </button>
              ))}
              {hasMore && <div style={{ padding: '8px 12px', color: 'var(--text3)', fontSize: 11 }}>More results — refine your search…</div>}
            </>}
        </div>
      </div>

      {/* ── Focused chart + sparkline grid ────────────────────────── */}
      <div>
        <div className="card ov-mb">
          <div className="ov-card-h">
            <h3 className="mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{active || '—'}</h3>
            {activeMeta && <span className="ov-sub">{activeMeta.unit || '·'} · {activeMeta.type}{activeMeta.description ? ` · ${activeMeta.description}` : ''}</span>}
          </div>
          <div className="ov-card-b">
            {/* Histograms have no single "current value" — the distribution is
                the story, shown in the heatmap below. The big-number + delta
                only make sense for scalar (gauge/sum) instruments. */}
            {!isHistogram && (
              <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, marginBottom: 10 }}>
                <span style={{ fontSize: 27, fontWeight: 700, fontVariantNumeric: 'tabular-nums' }}>
                  {fmtVal(lastVal)}{activeMeta?.unit ? <span style={{ fontSize: 14, color: 'var(--text2)', marginLeft: 4 }}>{activeMeta.unit}</span> : null}
                </span>
                {delta && (
                  <span className={`ov-delta ${delta.dir}`}>
                    {delta.dir === 'up' ? '▲' : delta.dir === 'down' ? '▼' : '—'} {delta.pct}%
                    <span style={{ color: 'var(--text3)', fontWeight: 500 }}>vs prior</span>
                  </span>
                )}
              </div>
            )}
            {isHistogram ? (
              histQ.isLoading ? (
                <div style={{ height: 260, display: 'grid', placeItems: 'center' }}><Spinner /></div>
              ) : !histQ.data || !histQ.data.bounds || histQ.data.bounds.length === 0 ? (
                <div style={{ height: 260, display: 'grid', placeItems: 'center' }}>
                  <Empty icon="📊" title="No histogram buckets">
                    This metric has no explicit-bucket histogram data in the selected window.
                  </Empty>
                </div>
              ) : (
                <HistogramHeatmap data={histQ.data} mode="heatmap" unit={activeMeta?.unit || 'ms'} height={260} />
              )
            ) : seriesQ.isLoading ? (
              <div style={{ height: 260, display: 'grid', placeItems: 'center' }}><Spinner /></div>
            ) : focusSeries.length === 0 ? (
              <div style={{ height: 260, display: 'grid', placeItems: 'center' }}>
                <Empty icon="∿" title={active ? 'No data in this window' : 'No metrics yet'} />
              </div>
            ) : (
              <TimeSeriesPanel series={focusSeries} height={260} mode="area" syncKey="metrics-explorer" hideLegend />
            )}
          </div>
        </div>

        {/* 2-column sparkline grid of the other metrics */}
        <div className="metric-spark-grid">
          {filtered.filter(m => m.name !== active).slice(0, 12).map(m => (
            <MetricSparkCard key={m.name} metric={m} from={from} to={to} onPick={() => setPicked(m.name)} />
          ))}
        </div>
      </div>
    </div>
  );
}

function MetricSparkCard({ metric, from, to, onPick }: {
  metric: MetricInfo; from: number; to: number; onPick: () => void;
}) {
  const q = useQuery({
    queryKey: ['metric-spark', metric.name, from, to],
    queryFn: () => api.metricQuery({ name: metric.name, agg: 'avg', from, to }),
    staleTime: 30_000,
  });
  const v = vals(q.data);
  const last = v.length ? v[v.length - 1] : NaN;
  return (
    <button className="card metric-spark" onClick={onPick}>
      <div className="mono metric-spark-name" title={metric.name}>{metric.name}</div>
      <div className="metric-spark-row">
        <span className="metric-spark-val">{fmtVal(last)}{metric.unit ? <span className="metric-spark-unit">{metric.unit}</span> : null}</span>
        <span className="metric-spark-chart">
          {v.length > 1 ? <Sparkline values={v} width={120} height={28} color="var(--accent)" /> : <span style={{ fontSize: 10, color: 'var(--text3)' }}>no data</span>}
        </span>
      </div>
    </button>
  );
}
