import { useMemo } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { Drawer, DrawerSection, DrawerTrendRow } from '@/components/ui';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtNum, fmtFixed } from '@/lib/utils';
import { useUrlRange } from '@/lib/useUrlRange';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { ExternalHost, ExternalHostDetail, TimeRange } from '@/lib/types';

// /external — third-party API inventory (v0.8.446, SigNoz/Uptrace
// gap-closure Wave 3 / A1). One row per external destination the
// instrumented services called in the window: category badge from
// the server-side vendor catalogue, RED metrics, and which services
// depend on it. Row click opens a URL-first ?host= drawer with the
// 5m trend + per-caller breakdown. Data source is topology_edges_5m
// (node_kind='external') — external identity today is peer.service
// on client spans; the server.address semconv fallback ships as the
// v2 aggregator slice.

// Category → badge tone. Semantic families, not per-vendor colours —
// the operator's eye should land on "payments red-ish, cloud blue-ish"
// groupings, and unknown categories fall through to gray.
const CATEGORY_TONE: Record<string, string> = {
  payments: 'b-err',
  auth: 'b-warn',
  messaging: 'b-info',
  email: 'b-info',
  push: 'b-info',
  sms: 'b-info',
  cloud: 'b-ok',
  cdn: 'b-ok',
  observability: 'b-gray',
  search: 'b-gray',
  ai: 'b-warn',
};

function CategoryBadge({ category }: { category?: string }) {
  if (!category) return <span style={{ color: 'var(--text3)' }}>—</span>;
  return <span className={`badge ${CATEGORY_TONE[category] ?? 'b-gray'}`}>{category}</span>;
}

const EXT_COLS: DataTableColumn<ExternalHost>[] = [
  { id: 'host',      label: 'Host',       sortValue: r => r.display || r.host, naturalDir: 'asc', width: 260 },
  { id: 'category',  label: 'Category',   sortValue: r => r.category ?? '',    naturalDir: 'asc', width: 110 },
  { id: 'calls',     label: 'Calls',      sortValue: r => r.calls,     numeric: true, width: 100 },
  { id: 'rpm',       label: 'Req/min',    sortValue: r => r.calls,     numeric: true, width: 90 },
  { id: 'errorRate', label: 'Error %',    sortValue: r => r.errorRate, numeric: true, width: 90 },
  { id: 'avgMs',     label: 'Avg ms',     sortValue: r => r.avgMs,     numeric: true, width: 90 },
  { id: 'p99Ms',     label: 'P99 ms',     sortValue: r => r.p99Ms,     numeric: true, width: 90 },
  { id: 'callers',   label: 'Callers',    sortValue: r => r.callers,   numeric: true, width: 200 },
];

export default function ExternalPage() {
  const [range, setRange] = useUrlRange('1h');
  // Memoized on range identity — the v0.5.184 incident shape.
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const windowMin = Math.max((to - from) / 60e9, 1);
  const q = useQuery({
    queryKey: ['external', from, to],
    queryFn: () => api.external(from, to),
    staleTime: 30_000,
    placeholderData: keepPreviousData,
  });
  const rows: ExternalHost[] | null | undefined =
    q.isPending ? undefined : q.isError ? null : q.data ?? [];

  // URL-first drawer selection (house rule §4): row click writes
  // ?host= with replace:true preserving foreign params; Esc/✕/overlay
  // clears it. A copied link reopens the same drawer.
  const [params, setParams] = useSearchParams();
  const openHostParam = params.get('host');
  const openHost = (h: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.set('host', h);
    return next;
  }, { replace: true });
  const closeHost = () => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.delete('host');
    return next;
  }, { replace: true });

  const dt = useDataTable<ExternalHost>({
    storageKey: 'external',
    columns: EXT_COLS,
    rows: rows ?? [],
    initialSort: { id: 'calls', dir: 'desc' },
  });

  return (
    <>
      <Topbar title="External APIs" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ color: 'var(--text2)', fontSize: 12, marginBottom: 12 }}>
          Third-party dependencies discovered from outbound client spans
          (<code>peer.service</code>). Click a row for the traffic trend and
          which services depend on it.
        </div>

        {rows === undefined && <TableSkeleton cols={8} wideFirst />}
        {rows === null && <Empty icon="✗" title="Failed to load external APIs" />}
        {rows && rows.length === 0 && (
          <Empty icon="◇" title="No external calls in this window">
            No client spans with a <code>peer.service</code> attribute reached a
            third-party destination. Instrumented outbound HTTP/gRPC calls will
            appear here automatically.
          </Empty>
        )}
        {rows && rows.length > 0 && (
          <div className="table-wrap">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map(r => (
                  <tr key={r.host}
                    onClick={() => openHost(r.host)}
                    style={{
                      cursor: 'pointer',
                      contentVisibility: 'auto',
                      containIntrinsicSize: 'auto 36px',
                    }}>
                    <td>
                      <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12, fontWeight: 500 }}
                        title={r.topLabels.length ? `Top operations:\n${r.topLabels.join('\n')}` : r.host}>
                        {r.display
                          ? <>{r.display} <span style={{ color: 'var(--text3)', fontWeight: 400 }}>({r.host})</span></>
                          : r.host}
                      </span>
                    </td>
                    <td><CategoryBadge category={r.category} /></td>
                    <td className="num mono">{fmtNum(r.calls)}</td>
                    <td className="num mono">{fmtFixed(r.calls / windowMin, 1)}</td>
                    <td className="num mono" style={{
                      color: r.errorRate > 5 ? 'var(--err)'
                        : r.errorRate > 1 ? 'var(--warn)' : 'var(--text3)',
                    }}>{r.errorRate.toFixed(2)}</td>
                    <td className="num mono">{r.avgMs.toFixed(1)}</td>
                    <td className="num mono" style={{
                      color: r.p99Ms > 1000 ? 'var(--err)'
                        : r.p99Ms > 200 ? 'var(--warn)' : undefined,
                    }}>{r.p99Ms.toFixed(0)}</td>
                    <td onClick={e => e.stopPropagation()}>
                      <span style={{ fontSize: 11, color: 'var(--text2)' }}
                        title={r.callerNames.join(', ')}>
                        {r.callers}{' · '}
                        {r.callerNames.slice(0, 3).map((c, i) => (
                          <span key={c}>
                            {i > 0 && ', '}
                            <Link to={`/service?name=${encodeURIComponent(c)}`}
                              style={{ fontSize: 11 }}>{c}</Link>
                          </span>
                        ))}
                        {r.callers > 3 && ` +${r.callers - 3}`}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {openHostParam && (
          <ExternalHostDrawer host={openHostParam} range={range} onClose={closeHost} />
        )}
      </div>
    </>
  );
}

// ExternalHostDrawer — right-side drawer (shell mirrors the
// slow-queries / endpoints drawers: overlay + slide-in, Esc closes).
// Payload fetched on open only (ES/CH-cost discipline: no list
// prefetch); trend rendered with the Sparkline primitive — row-scale
// trends don't need uPlot's crosshair/zoom here.
function ExternalHostDrawer({ host, range, onClose }: {
  host: string;
  range: TimeRange;
  onClose: () => void;
}) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const q = useQuery({
    queryKey: ['external-host', host, from, to],
    queryFn: () => api.externalHost(host, from, to),
    staleTime: 30_000,
  });
  const detail: ExternalHostDetail | null | undefined =
    q.isPending ? undefined : q.isError ? null : q.data;

  // Explore pre-filtered to this destination's client spans — the
  // same DSL deep-link shape DependenciesTable uses.
  const exploreHref = `/explore?dsl=${encodeURIComponent(`peer.service = "${host}"`)}&mode=advanced&result=traces`;

  const trend = detail?.trend ?? [];
  const callsSeries = trend.map(p => p.calls);
  const errorSeries = trend.map(p => p.errors);
  const p99Series = trend.map(p => p.p99Ms);

  return (
    <Drawer onClose={onClose} header={
      <>
        <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 14, fontWeight: 600 }}>
          {detail?.display || host}
        </span>
        <CategoryBadge category={detail?.category} />
      </>
    }>
        {detail?.display && (
          <div style={{ fontSize: 11, color: 'var(--text3)', fontFamily: 'ui-monospace, monospace', marginBottom: 8 }}>
            {host}
          </div>
        )}
        <div style={{ marginBottom: 14 }}>
          <Link to={exploreHref} style={{ fontSize: 12 }}>
            Open matching client spans in Explore →
          </Link>
        </div>

        {detail === undefined && <Spinner />}
        {detail === null && <Empty icon="✗" title="Failed to load host detail" />}
        {detail && (
          <>
            <DrawerSection title="Traffic (5-min buckets)">
              {trend.length === 0 ? (
                <div style={{ fontSize: 12, color: 'var(--text3)' }}>No buckets in this window.</div>
              ) : (
                <div style={{ display: 'grid', gap: 6 }}>
                  <DrawerTrendRow label="Calls" values={callsSeries} color="var(--accent2)" />
                  <DrawerTrendRow label="Errors" values={errorSeries} color="var(--err)" />
                  <DrawerTrendRow label="P99 ms" values={p99Series} color="var(--warn)" />
                </div>
              )}
            </DrawerSection>

            <DrawerSection title={`Calling services (${detail.callers.length})`}>
              {detail.callers.length === 0 ? (
                <div style={{ fontSize: 12, color: 'var(--text3)' }}>No callers in this window.</div>
              ) : (
                <table style={{ width: '100%', fontSize: 12 }}>
                  <thead>
                    <tr style={{ color: 'var(--text3)', fontSize: 11, textAlign: 'left' }}>
                      <th>Service</th>
                      <th className="num">Calls</th>
                      <th className="num">Err %</th>
                      <th className="num">Avg</th>
                      <th className="num">P99</th>
                    </tr>
                  </thead>
                  <tbody>
                    {detail.callers.map(c => (
                      <tr key={c.service}>
                        <td>
                          <Link to={`/service?name=${encodeURIComponent(c.service)}`}
                            title={c.topLabels.join('\n')}
                            style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>
                            {c.service}
                          </Link>
                        </td>
                        <td className="num mono">{fmtNum(c.calls)}</td>
                        <td className="num mono" style={{
                          color: c.errorRate > 5 ? 'var(--err)'
                            : c.errorRate > 1 ? 'var(--warn)' : 'var(--text3)',
                        }}>{c.errorRate.toFixed(2)}</td>
                        <td className="num mono">{c.avgMs.toFixed(1)}</td>
                        <td className="num mono">{c.p99Ms.toFixed(0)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </DrawerSection>
          </>
        )}
    </Drawer>
  );
}
