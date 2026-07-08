import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { Zap, ChevronRight, ChevronDown } from 'lucide-react';
import { Topbar } from '@/components/Topbar';
import { Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { ServicePicker } from '@/components/ServicePicker';
import { Sparkline } from '@/components/Sparkline';
import { MultiLineChart } from '@/components/MultiLineChart';
import { EventMarkers } from '@/components/EventMarkers';
import { Modal } from '@/components/ui';
import { api } from '@/lib/api';
import { useEndpoints, useClusters } from '@/lib/queries';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import { encodeRange } from '@/lib/urlState';
import { useUrlRange } from '@/lib/useUrlRange';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { TrendDelta } from '@/components/TrendDelta';
import { EndpointDetailDrawer } from '@/pages/endpoints/DetailDrawer';
import { encodeEndpointParam, decodeEndpointParam } from '@/pages/endpoints/endpointParam';
import type { DataTableColumn } from '@/lib/dataTable';
import type { EndpointRow, TimeRange, SpanMetricSeries } from '@/lib/types';

// /endpoints — operator-asked v0.5.365. Cross-service inbound
// RED rollup keyed on http.route (templated) with url.path /
// http.target fallbacks. Backend resolves the priority chain
// per row so this page only deals with the resolved string.
// Mirrors the /services list ergonomics: search + service
// filter + sortable columns + drill-throughs into /traces and
// /service detail.

// impactOf — composite "fix me first" score blending traffic,
// latency and error rate. Matches the /service Operations table's
// impactOf so the operator's mental model is consistent across
// surfaces:
//
//   impact = calls × p99Ms × (1 + errorRate)
//
// p99Ms surfaces the slow endpoints; calls surfaces the heavy
// ones; (1 + errorRate) doubles the score at 100% error so a
// fully-broken endpoint beats a slow-but-healthy one even at
// equal traffic. Used as the default sort affordance via the
// "Sort by impact" preset button.
//
// v0.8.356 — the sort now runs SERVER-side; the backend's impact
// expression (chstore.endpointsOrderBy) mirrors this formula
// exactly. This accessor stays as the column's sortable marker
// (serverSort mode never invokes it).
function impactOf(r: EndpointRow): number {
  const errFactor = 1 + (r.errorRate / 100);
  return r.calls * r.p99Ms * errFactor;
}

// Columns for the shared sortable + resizable DataTable primitive.
// Body order must match these (non-sortable Method/Status/Trend/Traces
// omit sortValue but still resize). `impact` is headerHidden — the
// "Worst by impact" preset sorts by it without rendering a column.
//
// v0.8.356 — serverSort mode: sortable column ids must stay in
// lockstep with the backend whitelist (chstore.endpointsOrderBy);
// SORT_KEYS below sanitizes stale persisted ids before they reach
// the fetch. New MV-backed columns: Req/min, P50, P95.
const ENDPOINT_COLS: DataTableColumn<EndpointRow>[] = [
  { id: 'service',   label: 'Service',    sortValue: r => r.service,   naturalDir: 'asc', width: 150 },
  { id: 'path',      label: 'Path',       sortValue: r => r.path,      naturalDir: 'asc', width: 260 },
  { id: 'method',    label: 'Method',     width: 68 },
  { id: 'calls',     label: 'Calls',      sortValue: r => r.calls,     numeric: true, width: 84 },
  { id: 'errors',    label: 'Errors',     sortValue: r => r.errors,    numeric: true, width: 76 },
  { id: 'errorRate', label: 'Error rate', sortValue: r => r.errorRate, numeric: true, width: 92 },
  { id: 'status',    label: 'Status',     width: 140 },
  { id: 'reqPerMin', label: 'Req/min',    sortValue: r => r.reqPerMin ?? 0, numeric: true, width: 84 },
  { id: 'avgMs',     label: 'Avg',        sortValue: r => r.avgMs,     numeric: true, width: 78 },
  { id: 'p50Ms',     label: 'P50',        sortValue: r => r.p50Ms ?? 0, numeric: true, width: 72 },
  { id: 'p95Ms',     label: 'P95',        sortValue: r => r.p95Ms ?? 0, numeric: true, width: 72 },
  { id: 'p99Ms',     label: 'P99',        sortValue: r => r.p99Ms,     numeric: true, width: 72 },
  { id: 'trend',     label: 'Trend',      width: 120 },
  { id: 'traces',    label: 'Traces',     width: 64 },
  { id: 'impact',    label: 'Impact',     sortValue: impactOf, headerHidden: true },
];

// Sort ids the backend ORDER BY whitelist accepts — anything else
// (stale localStorage from the pre-v0.8.356 schema, hand-edited
// URLs) falls back to calls DESC before it reaches the fetch.
const SORT_KEYS = [
  'service', 'path', 'calls', 'errors', 'errorRate',
  'avgMs', 'p50Ms', 'p95Ms', 'p99Ms', 'reqPerMin', 'impact',
] as const;
const DEFAULT_ENDPOINTS_SORT = { id: 'calls', dir: 'desc' as const };

export default function EndpointsPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  // Global time window (UX#2) — URL-persisted + carried across pages.
  const [range, setRange] = useUrlRange('30m');
  const [search, setSearch] = useState(() => params.get('search') ?? '');
  // "/" focuses this filter via the shared table keyboard-nav.
  const searchRef = useRef<HTMLInputElement>(null);
  const cluster = params.get('cluster') ?? '';
  const setCluster = (v: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('cluster', v); else next.delete('cluster');
    return next;
  }, { replace: true });
  const service = params.get('service') ?? '';
  const setService = (v: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('service', v); else next.delete('service');
    return next;
  }, { replace: true });
  const setSearchParam = (v: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('search', v); else next.delete('search');
    return next;
  }, { replace: true });

  // v0.8.376 (Stage-2 slice E4) — compare / limit / shape moved into
  // the URL (read directly from params each render + write with
  // replace:true, the same pattern cluster/service above use — no
  // local mirror, so no sig-guard needed) so Copy-link and
  // SavedViewsBar reproduce the exact view.
  //
  // limit (v0.5.389): 2000 default, explicit "top 5000" toggle;
  // backend clamps. Snapped to the two rungs so the URL can't carry
  // an unbounded cardinality into the server cache key.
  const limit = params.get('limit') === '5000' ? 5000 : 2000;
  const setLimit = (v: number) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v === 5000) next.set('limit', '5000'); else next.delete('limit');
    return next;
  }, { replace: true });
  // compare (v0.5.404): prior-window comparison, off by default —
  // doubles backend scan cost, operator opts in.
  const compare = params.get('compare') === 'prior';
  const setCompare = (v: boolean) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('compare', 'prior'); else next.delete('compare');
    return next;
  }, { replace: true });
  // shape (v0.8.x): Uptrace-style "group by shape" — cluster paths
  // carrying IDs (/orders/8421) into a signature (/orders/:id).
  const bySignature = params.get('shape') === '1';
  const setBySignature = (v: boolean) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('shape', '1'); else next.delete('shape');
    return next;
  }, { replace: true });
  // v0.5.406 — row expansion + per-service dependency cache.
  // Clicking the "▶" chevron on a row reveals a strip showing
  // which services this endpoint's service typically calls
  // downstream. Cached per-service so expanding 3 endpoints of
  // the same service hits the network once.
  const [expandedRows, setExpandedRows] = useState<Set<string>>(new Set());
  const [depsByService, setDepsByService] = useState<Record<string, import('@/lib/types').NeighborStat[]>>({});

  // v0.5.417 — toggle a row's dependency strip + lazy-load the
  // downstream neighbours for its service. Cache is per-service
  // so expanding multiple endpoints of the same service hits
  // /api/services/{svc}/neighbors only once.
  const onToggleExpand = (rowKey: string, service: string) => {
    setExpandedRows(prev => {
      const next = new Set(prev);
      if (next.has(rowKey)) next.delete(rowKey); else next.add(rowKey);
      return next;
    });
    // Lazy-fetch dependencies the first time we expand this svc.
    if (!depsByService[service]) {
      api.serviceNeighbors(service, '1h', 100, false)
        .then(r => setDepsByService(prev => ({
          ...prev, [service]: r?.downstream ?? [],
        })))
        .catch(() => setDepsByService(prev => ({
          ...prev, [service]: [],
        })));
    }
  };
  // v0.5.387 — sparkline-click drill-in. Holds the row whose
  // trend was clicked; modal renders the three RED sparklines
  // (calls / errors / p99) side-by-side with their summary
  // stats so the operator confirms "is this endpoint spiky" at
  // a glance, then drills further via the same "view traces"
  // link the table row already exposes.
  const [detail, setDetail] = useState<EndpointRow | null>(null);

  // v0.8.360 — URL-first detail drawer (Stage-2 slice E2). Row click
  // (not the sparkline — that keeps its RED modal above) writes
  // ?endpoint=<svc>|<path>[|sig] with replace:true; Esc/✕/overlay
  // clears it. Copy-link reproduces the exact drill-down: sig rides
  // the param itself, so a link copied in "group by shape" mode keeps
  // aggregating the collapsed route for the recipient.
  const endpointRef = useMemo(
    () => decodeEndpointParam(params.get('endpoint')),
    [params],
  );
  const openEndpoint = (r: EndpointRow) => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.set('endpoint', encodeEndpointParam({
      service: r.service, path: r.path, sig: bySignature,
    }));
    return next;
  }, { replace: true });
  const closeEndpoint = () => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.delete('endpoint');
    return next;
  }, { replace: true });

  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  // v0.8.356 — serverSort mode (v0.8.251 Services / v0.8.318 inbox
  // pattern): the hook owns the header UX, the `s_endpoints` URL
  // param and localStorage persistence; the fetch below forwards the
  // sanitized pair and CH does the ORDER BY before the LIMIT — true
  // global top-N per sort, not the top-N-by-calls page reordered.
  // The hook must precede the query that consumes dt.sort, so its
  // rows come through a state mirror synced from the query result.
  const [tableRows, setTableRows] = useState<EndpointRow[]>([]);
  // onOpen + searchRef wire the app-wide keyboard nav: j/k select a
  // row, Enter/o open its service detail, "/" focuses the path filter.
  const dt = useDataTable<EndpointRow>({
    storageKey: 'endpoints',
    columns: ENDPOINT_COLS,
    rows: tableRows,
    serverSort: true,
    initialSort: DEFAULT_ENDPOINTS_SORT,
    onOpen: r => navigate(`/service?name=${encodeURIComponent(r.service)}`),
    searchRef,
  });
  const sortOk = (SORT_KEYS as readonly string[]).includes(dt.sort.id ?? '');
  const sortBy = sortOk ? (dt.sort.id as string) : DEFAULT_ENDPOINTS_SORT.id;
  const sortDir = sortOk ? dt.sort.dir : DEFAULT_ENDPOINTS_SORT.dir;

  const rowsQ = useEndpoints({
    from, to,
    service: service || undefined,
    search: search.trim() || undefined,
    cluster: cluster || undefined,
    limit,
    compare: compare ? 'prior' : undefined,
    groupBy: bySignature ? 'signature' : undefined,
    sort: sortBy,
    dir: sortDir,
  });
  const rows: EndpointRow[] | null | undefined =
    rowsQ.isPending ? undefined : rowsQ.isError ? null : rowsQ.data ?? [];
  useEffect(() => { setTableRows(rowsQ.data ?? []); }, [rowsQ.data]);

  // Cluster picker options — mirror Services page so symmetry is
  // intuitive for operators landing here after filtering there.
  const clustersQ = useClusters(from, to);
  const clusterOptions = clustersQ.data ?? [];

  const totalCalls = (rows ?? []).reduce((s, r) => s + r.calls, 0);
  const totalErrors = (rows ?? []).reduce((s, r) => s + r.errors, 0);
  const totalErrorRate = totalCalls > 0 ? (totalErrors / totalCalls) * 100 : 0;

  return (
    <>
      <Topbar title="Endpoints" range={range} onRangeChange={setRange} />
      <div id="content">
        <div className="controls" style={{ flexWrap: 'wrap', marginBottom: 12 }}>
          <ServicePicker value={service} onChange={setService}
            placeholder="All services…" width={200} />
          <input ref={searchRef} value={search}
            onChange={e => { setSearch(e.target.value); setSearchParam(e.target.value); }}
            placeholder="Filter by path (substring)…"
            style={{ width: 280, padding: '5px 10px', fontSize: 12,
                     background: 'var(--bg)', color: 'var(--text)',
                     border: '1px solid var(--border)', borderRadius: 4 }} />
          {/* Cluster filter — same source as the Services page so
              an operator who picked a cluster there sees a
              symmetric set here. Hidden when no cluster signal
              is present in the window (resource attr keys
              k8s.cluster.name / openshift.cluster.name / cluster
              unset across all spans). */}
          {clusterOptions.length > 0 && (
            <select value={cluster}
              onChange={e => setCluster(e.target.value)}
              style={{ minWidth: 160 }}
              title={`${clusterOptions.length} cluster${clusterOptions.length === 1 ? '' : 's'} detected`}>
              <option value="">All clusters ({clusterOptions.length})</option>
              {clusterOptions.map(c => (
                <option key={c} value={c}>{c}</option>
              ))}
            </select>
          )}
          <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
            {rows && (
              <>
                Top {fmtNum(rows.length)} by{' '}
                {ENDPOINT_COLS.find(c => c.id === sortBy)?.label.toLowerCase() ?? sortBy}
                {rows.length >= limit && (
                  <span style={{ color: 'var(--warn)', marginLeft: 6 }}
                        title="Result hit the limit — long-tail endpoints may be hidden">
                    (capped)
                  </span>
                )}
              </>
            )}
          </span>
          {/* Limit dropdown — operator pulls the long tail when
              the table is at-cap and they want to see the lower-
              traffic endpoints. Capped at 5000 server-side. */}
          <select value={limit}
            onChange={e => setLimit(Number(e.target.value))}
            style={{ fontSize: 12 }}
            title="Maximum rows returned by the backend">
            <option value={500}>top 500</option>
            <option value={1000}>top 1000</option>
            <option value={2000}>top 2000</option>
            <option value={5000}>top 5000</option>
            <option value={10000}>All (10000)</option>
          </select>
          <label style={{ fontSize: 11, display: 'flex', alignItems: 'center', gap: 4, cursor: 'pointer' }}
            title="Compare current window against the immediately-preceding equal-length window. Adds a second backend scan; off by default.">
            <input type="checkbox"
              checked={compare}
              onChange={e => setCompare(e.target.checked)} />
            Compare vs prior
          </label>
          <label style={{ fontSize: 11, display: 'flex', alignItems: 'center', gap: 4, cursor: 'pointer' }}
            title="Group endpoints by normalized shape: paths carrying IDs (/orders/8421) collapse into one stable group (/orders/:id). p99/error-rate stay exact.">
            <input type="checkbox"
              checked={bySignature}
              onChange={e => setBySignature(e.target.checked)} />
            Group by shape
          </label>
          {/* v0.5.405 — fix-me-first preset. Sorts by composite
              impact (calls × p99 × (1+errorRate)) so high-traffic
              slow + erroring endpoints float to the top. */}
          <button type="button"
            onClick={() => dt.setSort({ id: 'impact', dir: 'desc' })}
            title="Sort by composite impact (calls × p99 × (1+errorRate)) — fix-me-first list"
            style={{
              padding: '3px 8px', fontSize: 11, borderRadius: 4,
              background: dt.sort.id === 'impact' ? 'var(--accent-soft)' : 'var(--bg2)',
              border: '1px solid ' + (dt.sort.id === 'impact' ? 'var(--accent)' : 'var(--border)'),
              color: dt.sort.id === 'impact' ? 'var(--accent2)' : 'var(--text2)',
              cursor: 'pointer',
              display: 'inline-flex', alignItems: 'center', gap: 5,
            }}>
            <Zap size={12} strokeWidth={1.75} /> Worst by impact
          </button>
        </div>

        {rows === undefined && <TableSkeleton cols={8} wideFirst />}
        {rows === null && (
          <Empty icon="⚠" title="Failed to load endpoints">
            The backend /api/endpoints request errored.
          </Empty>
        )}
        {rows && rows.length === 0 && (
          <Empty icon="∅" title="No endpoints in window">
            <div style={{ fontSize: 12, color: 'var(--text2)', maxWidth: 520, marginTop: 8, lineHeight: 1.5 }}>
              No spans with <code>http.route</code> / <code>url.path</code> / <code>http.target</code> attrs
              landed in this window. Try widening the time range, or check
              that your services emit one of those attributes on server-kind spans.
            </div>
          </Empty>
        )}
        {rows && rows.length > 0 && (
          <>
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
              gap: 12, marginBottom: 14,
            }}>
              <KPI label="Endpoints" value={fmtNum(rows.length)} />
              <KPI label="Total calls" value={fmtNum(totalCalls)} />
              <KPI label="Errors" value={fmtNum(totalErrors)}
                   sub={`${totalErrorRate.toFixed(2)}%`}
                   cls={totalErrorRate >= 5 ? 'err' : totalErrorRate >= 1 ? 'warn' : ''} />
            </div>
            <div className="table-wrap">
              <table style={{ tableLayout: 'fixed', width: '100%' }}>
                <DataTableColgroup dt={dt} leading={[22]} />
                <DataTableHead dt={dt} leading={<th style={{ width: 22 }} />} />
                <tbody>
                  {dt.sortedRows.map((r, i) => {
                    const errCls = r.errorRate >= 5 ? 'b-err' : r.errorRate >= 1 ? 'b-warn' : 'b-ok';
                    const rowKey = `${r.service}|${r.path}|${i}`;
                    const isExpanded = expandedRows.has(rowKey);
                    return (
                      <React.Fragment key={rowKey}>
                      <tr {...dt.rowProps(i)}
                        onMouseEnter={() => dt.nav.setSelected(i)}
                        onClick={e => {
                          // v0.8.360 — row click opens the detail
                          // drawer. Links / buttons inside the row
                          // (service link, expander, sparkline,
                          // traces →) keep their own affordances.
                          if ((e.target as HTMLElement).closest('a, button')) return;
                          openEndpoint(r);
                        }}
                        title="Click for endpoint detail (latency distribution, errors, failing traces)"
                        style={{
                          contentVisibility: 'auto', containIntrinsicSize: 'auto 32px',
                          cursor: 'pointer',
                          // Subtle err tint on broken endpoints (prototype cue).
                          background: r.errorRate >= 5
                            ? 'color-mix(in srgb, var(--err) 7%, transparent)'
                            : undefined,
                        }}>
                        <td style={{ width: 22, textAlign: 'center' }}>
                          {/* v0.5.417 — dependency strip expander.
                              Click ▶ → fetches the service's
                              downstream neighbours once (cached
                              per service across rows of same svc)
                              and renders a strip below the row. */}
                          <button type="button"
                            onClick={() => onToggleExpand(rowKey, r.service)}
                            style={{
                              all: 'unset', cursor: 'pointer',
                              color: 'var(--text3)',
                              padding: '0 4px',
                              display: 'inline-flex', alignItems: 'center',
                            }}
                            title={isExpanded
                              ? 'Hide downstream dependencies'
                              : 'Show services / dbs this endpoint\'s service typically calls'}>
                            {isExpanded
                              ? <ChevronDown size={13} strokeWidth={1.75} />
                              : <ChevronRight size={13} strokeWidth={1.75} />}
                          </button>
                        </td>
                        <td>
                          <Link to={`/service?name=${encodeURIComponent(r.service)}`}
                                style={{ fontFamily: 'monospace', fontSize: 12 }}>
                            {r.service}
                          </Link>
                        </td>
                        <td className="mono" style={{ fontSize: 12 }} title={r.path}>
                          {r.path}
                        </td>
                        <td className="mono" style={{ fontSize: 11, color: 'var(--text2)' }}>
                          {r.method || '—'}
                        </td>
                        <td className="num mono">
                          {fmtNum(r.calls)}
                          {compare && <TrendDelta cur={r.calls} prior={r.priorCalls} kind="neutral" />}
                        </td>
                        <td className="num mono">
                          {fmtNum(r.errors)}
                          {compare && <TrendDelta cur={r.errors} prior={r.priorErrors} kind="lowerBetter" />}
                        </td>
                        <td className="num mono">
                          <span className={`badge ${errCls}`}>{r.errorRate.toFixed(2)}%</span>
                        </td>
                        <td><StatusBreakdown r={r} /></td>
                        <td className="num mono">{fmtRate(r.reqPerMin)}</td>
                        <td className="num mono">
                          {r.avgMs.toFixed(1)} ms
                          {compare && <TrendDelta cur={r.avgMs} prior={r.priorAvgMs} kind="lowerBetter" />}
                        </td>
                        <td className="num mono">{fmtMs(r.p50Ms)}</td>
                        <td className="num mono">{fmtMs(r.p95Ms)}</td>
                        <td className="num mono">
                          {r.p99Ms.toFixed(0)} ms
                          {compare && <TrendDelta cur={r.p99Ms} prior={r.priorP99Ms} kind="lowerBetter" />}
                        </td>
                        <td>
                          <button
                            type="button"
                            onClick={() => setDetail(r)}
                            title="Click for calls / errors / p99 detail"
                            style={{
                              background: 'transparent', border: 0, padding: 0,
                              cursor: 'pointer', display: 'inline-block',
                            }}
                          >
                            <Sparkline values={r.sparkline ?? []}
                              width={100} height={22}
                              color={r.errorRate >= 5 ? 'var(--err)' : r.errorRate >= 1 ? 'var(--warn)' : undefined}
                              title={`${r.calls.toLocaleString()} calls — click for detail`} />
                          </button>
                        </td>
                        <td>
                          {/* /traces filter on (service, search=path).
                              The search field matches span.name OR
                              attrs; combined with rootOnly=false and
                              the service filter, this returns every
                              trace that includes a call on this
                              endpoint. */}
                          <Link to={tracesLink(r, range)}
                                style={{ fontSize: 11, color: 'var(--accent2)' }}>
                            view →
                          </Link>
                        </td>
                      </tr>
                      {isExpanded && (
                        <tr>
                          <td />
                          {/* v0.8.356 — 14 body columns (Req/min, P50, P95 added) */}
                          <td colSpan={14} style={{ background: 'var(--bg0)', padding: '8px 14px' }}>
                            <DependencyStrip
                              service={r.service}
                              deps={depsByService[r.service]} />
                          </td>
                        </tr>
                      )}
                      </React.Fragment>
                    );
                  })}
                </tbody>
              </table>
            </div>
            <div style={{ marginTop: 8, fontSize: 11, color: 'var(--text3)' }}>
              {/* v0.8.356 — the default read rides the spanmetrics_1m MV,
                  whose route dimension is filled from http.route with an
                  http.target fallback at ingest; the url.path fallback only
                  applies on the cluster-filtered raw path. */}
              Path source priority: {cluster
                ? <><code>http.route</code> (templated) → <code>url.path</code> → <code>http.target</code></>
                : <><code>http.route</code> (templated) → <code>http.target</code></>}.
              Server / consumer spans only — outbound client spans count under
              the callee's row. P50/P95/P99 are true window quantiles
              (tdigest).
            </div>
          </>
        )}
        <EndpointMetricModal row={detail} onClose={() => setDetail(null)} range={range} />
        {/* v0.8.360 — route-scoped drill-down drawer. row may be
            undefined on a stale deep-link (endpoint not in the loaded
            page) — the drawer soft-falls back and still loads its
            sections from /api/endpoints/detail. */}
        {endpointRef && (
          <EndpointDetailDrawer
            refObj={endpointRef}
            row={(rows ?? []).find(x =>
              x.service === endpointRef.service && x.path === endpointRef.path)}
            range={range}
            compare={compare}
            onClose={closeEndpoint}
          />
        )}
      </div>
    </>
  );
}

// EndpointMetricModal — opens on sparkline click. Renders the
// three RED dimensions (calls, errors, p99) as full uPlot
// time-axis charts so the operator can read tick marks, hover
// for exact values, and visually correlate spikes across all
// three at the same instant via syncKey-linked crosshairs.
// Deep links into /traces and the service detail page close
// the metric → trace loop on the (service, path) tuple.
//
// v0.5.391 — upgraded from three bare Sparklines to MultiLineChart
// instances so the metric view answers "what was happening at
// 14:23" not just "is this endpoint spiky-shaped". Time axis,
// crosshair sync, and tooltip per series — the same uPlot
// affordances the Metrics / Explore pages use.
function EndpointMetricModal({
  row, onClose, range,
}: { row: EndpointRow | null; onClose: () => void; range: TimeRange }) {
  // Hooks must run unconditionally — call useMemo before any
  // early return (React rules-of-hooks).
  const series = useMemo(() => {
    if (!row) return { calls: [], errors: [], p99: [] };
    return bucketsToSeries(row, range);
  }, [row, range]);

  if (!row) return <Modal open={false} onClose={onClose} />;
  const peakCalls = (row.sparkline ?? []).reduce((m, v) => Math.max(m, v), 0);
  const totalErrs = (row.errorsSparkline ?? []).reduce((s, v) => s + v, 0);
  const maxP99 = (row.p99Sparkline ?? []).reduce((m, v) => Math.max(m, v), 0);
  const errCls = row.errorRate >= 5 ? 'err' : row.errorRate >= 1 ? 'warn' : '';
  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={
        <span className="mono" style={{ fontSize: 13 }}>
          {row.method ? row.method + ' ' : ''}{row.path}
          <span style={{ color: 'var(--text3)', marginLeft: 8, fontSize: 11 }}>
            ({row.service})
          </span>
        </span>
      }
    >
      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)',
        gap: 12, marginBottom: 14,
      }}>
        {/* v0.6.16 — EventMarkers overlay on each chart. Per-tile
            absolute overlay reads the same time window the tile's
            MultiLineChart uses; vertical lines anchor incidents
            and deploys to the curve so the operator can spot
            "p99 spike after the 14:23 deploy" without leaving
            the modal. service is row.service — these charts are
            always endpoint-scoped to one service. */}
        <MetricTile
          label="Calls"
          big={fmtNum(row.calls)}
          sub={`peak ${fmtNum(peakCalls)} / bucket`}
          series={series.calls}
          service={row.service}
          range={range}
        />
        <MetricTile
          label="Errors"
          big={fmtNum(row.errors)}
          sub={`${row.errorRate.toFixed(2)}% rate`}
          subCls={errCls}
          series={series.errors}
          service={row.service}
          range={range}
        />
        <MetricTile
          label="P99 latency"
          big={`${row.p99Ms.toFixed(0)} ms`}
          sub={`peak ${maxP99.toFixed(0)} ms · avg ${row.avgMs.toFixed(0)} ms`}
          series={series.p99}
          unit="ms"
          service={row.service}
          range={range}
        />
      </div>
      <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 14 }}>
        Hover any chart to read the bucket value; the crosshair
        syncs across all three so you can correlate calls /
        errors / p99 at the same instant. Total errors in window:
        {' '}<strong>{fmtNum(totalErrs)}</strong>.
      </div>
      <div style={{ display: 'flex', gap: 10 }}>
        <Link
          to={tracesLink(row, range)}
          style={{ fontSize: 12, color: 'var(--accent2)' }}
        >
          View traces →
        </Link>
        <Link
          to={`/service?name=${encodeURIComponent(row.service)}`}
          style={{ fontSize: 12, color: 'var(--accent2)' }}
        >
          Service detail →
        </Link>
      </div>
    </Modal>
  );
}

function MetricTile({
  label, big, sub, subCls, series, unit, service, range,
}: {
  label: string; big: string; sub: string; subCls?: string;
  series: SpanMetricSeries[]; unit?: string;
  // v0.6.16 — pass service + range so the tile can overlay
  // EventMarkers on its chart. Optional so the component stays
  // usable on pages that don't have an event story yet.
  service?: string; range?: TimeRange;
}) {
  // Compute window bounds once; the EventMarkers component
  // already memoises internally, but pre-computing here keeps
  // the prop signature stable across re-renders.
  const bounds = useMemo(() => {
    if (!range) return null;
    return timeRangeToNs(range);
  }, [range]);
  return (
    <div style={{
      padding: '10px 12px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)',
    }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      <div style={{ fontSize: 22, fontWeight: 600, marginBottom: 2 }}>{big}</div>
      <div style={{
        fontSize: 11, marginBottom: 8,
        color: subCls === 'err' ? 'var(--err)' : subCls === 'warn' ? 'var(--warn)' : 'var(--text3)',
      }}>{sub}</div>
      {series.length > 0 && series[0].points.length > 0 ? (
        <div style={{ position: 'relative' }}>
          <MultiLineChart
            series={series}
            unit={unit}
            height={140}
            syncKey="endpoints-detail"
          />
          {bounds && (
            <EventMarkers
              fromNs={bounds.from}
              toNs={bounds.to}
              service={service || undefined}
            />
          )}
        </div>
      ) : (
        <div style={{
          height: 140, display: 'flex', alignItems: 'center',
          justifyContent: 'center', color: 'var(--text3)', fontSize: 11,
        }}>no data in window</div>
      )}
    </div>
  );
}

// bucketsToSeries converts the row's three 30-bucket sparkline
// arrays into SpanMetricSeries shape so MultiLineChart can plot
// them on a time axis. The backend doesn't ship per-bucket
// timestamps (the payload size is bounded that way) so we
// reconstruct them client-side from the page's selected range
// — bucket i sits at the midpoint of its slice of the window.
// Matches the bucketing the backend's per_bucket CTE uses
// (intDiv(time - from, bucketNs)).
function bucketsToSeries(row: EndpointRow, range: TimeRange): {
  calls: SpanMetricSeries[]; errors: SpanMetricSeries[]; p99: SpanMetricSeries[];
} {
  const { from, to } = timeRangeToNs(range);
  const calls = row.sparkline ?? [];
  const errs = row.errorsSparkline ?? [];
  const p99s = row.p99Sparkline ?? [];
  const n = Math.max(calls.length, errs.length, p99s.length);
  if (n === 0 || to <= from) {
    return { calls: [], errors: [], p99: [] };
  }
  const bucketNs = (to - from) / n;
  const timeAtBucket = (i: number) => from + bucketNs * i + bucketNs / 2;
  const buildPoints = (arr: number[]) => arr.map((v, i) => ({
    time: timeAtBucket(i),
    value: v,
  }));
  return {
    calls: calls.length ? [{ groupKey: ['calls'], points: buildPoints(calls) }] : [],
    errors: errs.length ? [{ groupKey: ['errors'], points: buildPoints(errs) }] : [],
    p99: p99s.length ? [{ groupKey: ['p99 ms'], points: buildPoints(p99s) }] : [],
  };
}

function tracesLink(r: EndpointRow, range: TimeRange): string {
  return `/traces?service=${encodeURIComponent(r.service)}` +
    `&search=${encodeURIComponent(r.path)}` +
    `&range=${encodeURIComponent(encodeRange(range))}` +
    `&view=list&rootOnly=false`;
}

// StatusBreakdown — inline 2xx / 3xx / 4xx / 5xx pills for one
// endpoint row. Compact (fits in a narrow column) but explicit:
// the operator reads "is this endpoint throwing 5xx, returning
// 4xx, or just slow?" without drilling into a trace. Pills
// hidden when their count is 0 to keep the cell scannable.
// 3xx only renders when present (rare on most APIs).
function StatusBreakdown({ r }: { r: EndpointRow }) {
  const s2 = r.http2xx ?? 0;
  const s3 = r.http3xx ?? 0;
  const s4 = r.http4xx ?? 0;
  const s5 = r.http5xx ?? 0;
  const total = s2 + s3 + s4 + s5;
  if (total === 0) {
    return <span style={{ color: 'var(--text3)', fontSize: 10 }}>—</span>;
  }
  return (
    <span style={{ display: 'inline-flex', gap: 4 }}>
      {s2 > 0 && (
        <span className="badge b-ok" title={`${s2.toLocaleString()} 2xx responses`}>2xx {compactNum(s2)}</span>
      )}
      {s3 > 0 && (
        <span className="badge b-gray" title={`${s3.toLocaleString()} 3xx redirects`}>3xx {compactNum(s3)}</span>
      )}
      {s4 > 0 && (
        <span className="badge b-warn" title={`${s4.toLocaleString()} 4xx client errors`}>4xx {compactNum(s4)}</span>
      )}
      {s5 > 0 && (
        <span className="badge b-err" title={`${s5.toLocaleString()} 5xx server errors`}>5xx {compactNum(s5)}</span>
      )}
    </span>
  );
}

// DependencyStrip — v0.5.417. Shown when an endpoint row is
// expanded; lists the top 5 downstream services / dbs / queues
// that the endpoint's SERVICE typically calls during the
// window. Best-effort approximation: real per-endpoint
// dependency tracking would require span-level descendant
// traversal (expensive); the service-level neighbours read
// from /api/services/{svc}/neighbors is fast (cached at
// backend) and operationally informative ("this endpoint's
// service hits postgres + redis + payments-api").
function DependencyStrip({ service, deps }: {
  service: string;
  deps?: import('@/lib/types').NeighborStat[];
}) {
  if (deps === undefined) {
    return (
      <span style={{ fontSize: 11, color: 'var(--text3)' }}>
        Loading {service}'s dependencies…
      </span>
    );
  }
  if (deps.length === 0) {
    return (
      <span style={{ fontSize: 11, color: 'var(--text3)' }}>
        No downstream calls observed for {service} in the last 1h.
      </span>
    );
  }
  const top = [...deps].sort((a, b) => b.spanCount - a.spanCount).slice(0, 5);
  return (
    <div style={{
      display: 'flex', flexDirection: 'column', gap: 6,
      fontSize: 11,
    }}>
      <div style={{
        fontSize: 10, color: 'var(--text3)',
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>
        {service} → downstream (last 1h, top 5 by span volume)
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
        {top.map((d, i) => (
          <Link key={i}
            to={`/service?name=${encodeURIComponent(d.service)}`}
            title={`${fmtNum(d.spanCount)} spans across ${fmtNum(d.traceCount)} traces in the last 1h`}
            style={{
              display: 'inline-flex', alignItems: 'center', gap: 6,
              padding: '3px 8px', borderRadius: 12,
              background: 'var(--bg2)', border: '1px solid var(--border)',
              color: 'var(--text2)', fontFamily: 'ui-monospace, monospace',
              fontSize: 10, textDecoration: 'none',
            }}>
            <span style={{ fontWeight: 600 }}>{d.service}</span>
            <span style={{ color: 'var(--text3)' }}>{fmtNum(d.spanCount)} sp</span>
          </Link>
        ))}
        {deps.length > 5 && (
          <Link to={`/topology?focus=${encodeURIComponent(service)}`}
            style={{
              padding: '3px 8px', fontSize: 10,
              color: 'var(--accent2)', textDecoration: 'none',
              alignSelf: 'center',
            }}>
            +{deps.length - 5} more → topology
          </Link>
        )}
      </div>
    </div>
  );
}

// TrendDelta moved to components/TrendDelta.tsx (v0.8.360 → endpoints/, v0.8.362 → components/) so the
// detail drawer's header RED strip shares the exact same delta chip.

// fmtRate — Req/min cell (v0.8.356). Sub-10 rates keep one decimal
// ("3.2") so low-traffic endpoints don't all read "0"; larger rates
// round to locale ints. "—" for a mid-rolling-deploy older backend
// that doesn't ship the field yet.
function fmtRate(v?: number): string {
  if (v === undefined || v === null) return '—';
  return v < 10 ? v.toFixed(1) : fmtNum(Math.round(v));
}

// fmtMs — P50/P95 cells (v0.8.356). One decimal under 10ms (cache
// hits, health checks), whole ms above. "—" when the backend
// predates the field (rolling deploy).
function fmtMs(v?: number): string {
  if (v === undefined || v === null) return '—';
  return (v < 10 ? v.toFixed(1) : v.toFixed(0)) + ' ms';
}

// compactNum — 12345 → "12.3k". Keeps the pill width bounded
// across two orders of magnitude. fmtNum (locale-formatted) would
// blow the column out at 5+ digits.
function compactNum(n: number): string {
  if (n < 1000) return n.toString();
  if (n < 1_000_000) return (n / 1000).toFixed(n < 10_000 ? 1 : 0) + 'k';
  return (n / 1_000_000).toFixed(n < 10_000_000 ? 1 : 0) + 'M';
}

function KPI({ label, value, sub, cls }: { label: string; value: string; sub?: string; cls?: string }) {
  return (
    <div style={{
      padding: '8px 14px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)',
    }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 2 }}>{label}</div>
      <div style={{
        fontSize: 22, fontWeight: 600,
        color: cls === 'err' ? 'var(--err)' : cls === 'warn' ? 'var(--warn)' : 'var(--text)',
      }}>{value}</div>
      {sub && <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>{sub}</div>}
    </div>
  );
}
