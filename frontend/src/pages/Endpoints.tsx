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
import { timeRangeToNs, rangeToSince, fmtNum } from '@/lib/utils';
import { encodeRange, encodeFilters, buildQuery } from '@/lib/urlState';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { useUrlRange } from '@/lib/useUrlRange';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { pushZoom, popZoom } from '@/lib/chart/zoomHistory';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { TrendDelta } from '@/components/TrendDelta';
import { EndpointDetailDrawer } from '@/pages/endpoints/DetailDrawer';
import { encodeEndpointParam, decodeEndpointParam } from '@/pages/endpoints/endpointParam';
import { parseColsParam, formatColsParam } from '@/pages/endpoints/endpointCols';
import { ColumnToggle } from '@/pages/endpoints/ColumnToggle';
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

// spreadOf — p99 / p50 (v0.9.305). Latency SHAPE, not level.
//
// A single percentile tells you how slow, never how the slowness is
// distributed. Spread near 1 means the whole distribution moved: every
// caller is slow, so look at the dependency, the pool, the node. Spread
// high means most calls are fine and a tail is dragging: look at retries,
// GC pauses, a cold cache, one bad shard. Same p99, opposite causes.
//
// Derived from columns already on the row — zero query cost. Returns 0
// when p50 is missing or zero: a ratio with no denominator sorts to the
// bottom rather than to Infinity.
function spreadOf(r: EndpointRow): number {
  const p50 = r.p50Ms ?? 0;
  if (p50 <= 0) return 0;
  return r.p99Ms / p50;
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
  { id: 'p90Ms',     label: 'P90',        sortValue: r => r.p90Ms ?? 0, numeric: true, width: 72 },
  { id: 'p95Ms',     label: 'P95',        sortValue: r => r.p95Ms ?? 0, numeric: true, width: 72 },
  { id: 'p99Ms',     label: 'P99',        sortValue: r => r.p99Ms,     numeric: true, width: 72 },
  // v0.9.305 — Spread = p99/p50. Purely derived, zero query cost, and
  // it answers a question no single percentile does: is this endpoint
  // slow for EVERYONE (spread near 1, the whole distribution moved) or
  // does it have a heavy TAIL (spread high, most calls fine)? Those two
  // have different causes and different fixes.
  { id: 'spread',    label: 'Spread',     sortValue: spreadOf, numeric: true, width: 76 },
  { id: 'trend',     label: 'Trend',      width: 120 },
  // v0.8.573 — pinned to the right edge: the 14-column table overflows
  // laptop widths and the horizontal scrollbar sits below 2000 rows, so
  // the trailing drill-through was effectively invisible (operator
  // report). 64px also clipped "view →" (content+padding ≈ 66px).
  { id: 'traces',    label: 'Traces',     width: 76, stickyRight: true },
  { id: 'impact',    label: 'Impact',     sortValue: impactOf, headerHidden: true },
];

// Sort ids the backend ORDER BY whitelist accepts — anything else
// (stale localStorage from the pre-v0.8.356 schema, hand-edited
// URLs) falls back to calls DESC before it reaches the fetch.
const SORT_KEYS = [
  'service', 'path', 'calls', 'errors', 'errorRate',
  'avgMs', 'p50Ms', 'p90Ms', 'p95Ms', 'p99Ms', 'reqPerMin', 'impact',
] as const;
const DEFAULT_ENDPOINTS_SORT = { id: 'calls', dir: 'desc' as const };

// Visible-column universe for the ?cols= codec (v0.8.574) — every
// rendered column; headerHidden `impact` is sort-only and can't be
// hidden (it has no header to hide).
const ALL_COL_IDS = ENDPOINT_COLS.filter(c => !c.headerHidden).map(c => c.id);

export default function EndpointsPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  // Global time window (UX#2) — URL-persisted + carried across pages.
  const [range, setRange] = useUrlRange('30m');
  // Madde 4 sweep — modal RED grafiklerinin drag-zoom'u global range'e
  // yazar; çift-tık GERİ-YIĞINI Service.tsx v0.9.199 deseninin birebiri
  // (out-of-band range değişimi yığını geçersizleştirir).
  const zoomStackRef = useRef<TimeRange[]>([]);
  const rangeRef = useRef(range); rangeRef.current = range;
  const zoomWroteRef = useRef(false);
  useEffect(() => {
    if (zoomWroteRef.current) { zoomWroteRef.current = false; return; }
    zoomStackRef.current = [];
  }, [range.preset, range.fromMs, range.toMs]);
  const handleZoom = (fromUnixSec: number, toUnixSec: number) => {
    zoomStackRef.current = pushZoom(zoomStackRef.current, rangeRef.current);
    zoomWroteRef.current = true;
    setRange({
      preset: 'custom',
      fromMs: Math.round(fromUnixSec * 1000),
      toMs: Math.round(toUnixSec * 1000),
    });
  };
  const handleZoomReset = () => {
    const { stack, view } = popZoom(zoomStackRef.current);
    zoomStackRef.current = stack;
    if (view) { zoomWroteRef.current = true; setRange(view); return; }
    if (rangeRef.current.preset === 'custom') { zoomWroteRef.current = true; setRange({ preset: '30m' }); }
  };
  // Global env filter (v0.8.385, env-separation Phase 2) — written by
  // the Topbar EnvPicker. Forwarded to /api/endpoints, where it forces
  // the bounded raw-spans path with a deploy_env conjunct (the
  // spanmetrics_1m MV has no env dim — same trade-off as cluster).
  const [env] = useUrlEnv();
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
  // limit (v0.5.389; v0.9.70 operatör isteği): default 2000 → 100 —
  // sayfanın ilk açılışı yavaştı, top-100 tipik triage'ı karşılar;
  // gerisi açık seçimle. Sabit runglara snap'li — URL sınırsız
  // kardinaliteyi server cache key'ine taşıyamaz (v0.8.270 disiplini).
  // v0.9.70 ayrıca dropdown↔parser uyumsuzluğunu kapatır: 500/1000/
  // 10000 seçimleri parser'da yoktu, sessizce default'a düşüyordu.
  const LIMIT_RUNGS = [100, 500, 1000, 2000, 5000, 10000];
  const limitRaw = Number(params.get('limit') ?? 100);
  const limit = LIMIT_RUNGS.includes(limitRaw) ? limitRaw : 100;
  const setLimit = (v: number) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v !== 100 && LIMIT_RUNGS.includes(v)) next.set('limit', String(v));
    else next.delete('limit');
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
  // v0.9.257 — keyed `${service}@${since}`; value carries sampledFrom so the
  // strip can state how many traces the counts came from instead of implying
  // they are fleet totals.
  const [depsByService, setDepsByService] = useState<Record<string, {
    deps: import('@/lib/types').NeighborStat[]; sampledFrom: number;
  }>>({});
  // Window for the dependency strip. Capped at 24h: this endpoint samples the
  // top-N traces out of raw `spans`, so a 7d/30d selection would run a
  // GROUP BY trace_id across the whole window and hit max_execution_time.
  // `capped` is rendered, so the narrowing is stated rather than silent.
  const dep = rangeToSince(range, 86_400);

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
    // v0.9.257 — cache key is (service, window), not service alone. The old
    // key meant the FIRST window an operator expanded stuck to that service
    // for the rest of the session: changing the page range refetched every
    // other panel but left this strip showing the original numbers, with no
    // spinner and no visible change to hint at it.
    const ck = `${service}@${dep.since}`;
    if (!depsByService[ck]) {
      api.serviceNeighbors(service, dep.since, 100, false)
        .then(r => setDepsByService(prev => ({
          ...prev, [ck]: { deps: r?.downstream ?? [], sampledFrom: r?.sampledFrom ?? 0 },
        })))
        .catch(() => setDepsByService(prev => ({
          ...prev, [ck]: { deps: [], sampledFrom: 0 },
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
  // Column visibility (v0.8.574, audit seçenek 3) — URL is the source
  // of truth (?cols=, Logs contract: absent = all visible) so Copy
  // link / SavedViewsBar reproduce the exact column set. Read directly
  // from params each render + write with replace:true — the same
  // no-local-mirror pattern compare/limit use, so no sig-guard needed.
  // Widths/sort persistence (localStorage, keyed by column id) is
  // untouched: a re-shown column comes back at its remembered width.
  const visibleCols = useMemo(
    () => parseColsParam(params.get('cols'), ALL_COL_IDS),
    [params]);
  const setVisibleCols = (s: Set<string>) => setParams(prev => {
    const next = new URLSearchParams(prev);
    const v = formatColsParam(s, ALL_COL_IDS);
    if (v) next.set('cols', v); else next.delete('cols');
    return next;
  }, { replace: true });
  // The hook sees only visible columns (+ the sort-only headerHidden
  // impact) so DataTableColgroup/Head stay aligned with the body cells.
  // Sorting by a now-hidden column keeps working: serverSort forwards
  // the persisted sort id to the fetch regardless of visibility —
  // same contract as the never-rendered impact column.
  const visibleColumns = useMemo(
    () => ENDPOINT_COLS.filter(c => c.headerHidden || visibleCols.has(c.id)),
    [visibleCols]);
  // onOpen + searchRef wire the app-wide keyboard nav: j/k select a
  // row, Enter/o open its service detail, "/" focuses the path filter.
  const dt = useDataTable<EndpointRow>({
    storageKey: 'endpoints',
    columns: visibleColumns,
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
    // Global Topbar env filter (v0.8.385) — rides the params object,
    // so it's part of the React Query key automatically.
    env: env || undefined,
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

  // v0.9.206 review-fix — modal'daki satırın MEVCUT range'e ait taze
  // kopyası. Zoom range'i yeniden yazınca endpoint taze top-N'den
  // düşebilir (limit varsayılanı 100); o durumda click-time `detail`
  // satırına sessiz geri düşmek, bucketsToSeries'in ESKİ pencere
  // sayaçlarını YENİ eksene yayıp yeni bucket genişliğine bölmesi
  // demekti (30m→5m zoom'da rps 6x şişik, taze veri gibi). Kimlik/
  // başlık bayat satırdan kalabilir; time-series üretimini modal
  // rowIsStale ile keser.
  const freshDetailRow = detail
    ? (rows ?? []).find(x =>
        x.service === detail.service && x.path === detail.path && x.method === detail.method)
    : undefined;

  return (
    <>
      <Topbar title="Endpoints" range={range} onRangeChange={setRange} />
      <div id="content">
        {/* v0.9.308 (brief N6c) — the page's whole selection already
            lives in the URL: service, search, cluster, limit, compare,
            shape, ?cols=, ?endpoint= and the s_endpoints sort pair.
            Two comments in this file name SavedViewsBar as the REASON
            that was done — the bar itself was simply never mounted.
            Persistence rides the existing saved_views(page='…') table,
            so this is one import and one line: no new schema, no new
            endpoint, no new state. */}
        <SavedViewsBar page="endpoints" />
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
            <option value={100}>top 100</option>
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
          <ColumnToggle
            columns={ENDPOINT_COLS.filter(c => !c.headerHidden).map(c => ({ id: c.id, label: c.label }))}
            visible={visibleCols}
            onChange={setVisibleCols} />
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
                        {/* v0.8.574 — every data cell renders only when
                            its column is visible (?cols=); order stays in
                            lockstep with ENDPOINT_COLS so the colgroup and
                            body never misalign. */}
                        {visibleCols.has('service') && <td>
                          <Link to={`/service?name=${encodeURIComponent(r.service)}`}
                                style={{ fontFamily: 'monospace', fontSize: 12 }}>
                            {r.service}
                          </Link>
                        </td>}
                        {visibleCols.has('path') && <td className="mono" style={{ fontSize: 12 }} title={r.path}>
                          {r.path}
                        </td>}
                        {visibleCols.has('method') && <td className="mono" style={{ fontSize: 11, color: 'var(--text2)' }}>
                          {r.method || '—'}
                        </td>}
                        {visibleCols.has('calls') && <td className="num mono">
                          {fmtNum(r.calls)}
                          {compare && <TrendDelta cur={r.calls} prior={r.priorCalls} kind="neutral" />}
                        </td>}
                        {visibleCols.has('errors') && <td className="num mono">
                          {fmtNum(r.errors)}
                          {compare && <TrendDelta cur={r.errors} prior={r.priorErrors} kind="lowerBetter" />}
                        </td>}
                        {visibleCols.has('errorRate') && <td className="num mono">
                          <span className={`badge ${errCls}`}>{r.errorRate.toFixed(2)}%</span>
                        </td>}
                        {visibleCols.has('status') && <td><StatusBreakdown r={r} /></td>}
                        {visibleCols.has('reqPerMin') && <td className="num mono">{fmtRate(r.reqPerMin)}</td>}
                        {visibleCols.has('avgMs') && <td className="num mono">
                          {r.avgMs.toFixed(1)} ms
                          {compare && <TrendDelta cur={r.avgMs} prior={r.priorAvgMs} kind="lowerBetter" />}
                        </td>}
                        {visibleCols.has('p50Ms') && <td className="num mono">{fmtMs(r.p50Ms)}</td>}
                        {visibleCols.has('p90Ms') && <td className="num mono">{fmtMs(r.p90Ms)}</td>}
                        {visibleCols.has('p95Ms') && <td className="num mono">{fmtMs(r.p95Ms)}</td>}
                        {visibleCols.has('p99Ms') && <td className="num mono">
                          {r.p99Ms.toFixed(0)} ms
                          {compare && <TrendDelta cur={r.p99Ms} prior={r.priorP99Ms} kind="lowerBetter" />}
                        </td>}
                        {visibleCols.has('spread') && <td className="num mono"
                          title="p99 ÷ p50 — the SHAPE of the latency, not its level.\nNear 1: the whole distribution moved, every caller is slow (dependency, pool, node).\nHigh: most calls are fine and a tail is dragging (retries, GC, cold cache, one bad shard).\nSame p99, opposite causes.">
                          {(() => {
                            const sp = spreadOf(r);
                            if (sp <= 0) return <span style={{ color: 'var(--text3)' }}>—</span>;
                            // Tone marks SHAPE, never slowness: a fast endpoint
                            // with a heavy tail should still read as tail-heavy.
                            const tone = sp >= 10 ? 'var(--err)' : sp >= 4 ? 'var(--warn)' : 'var(--text2)';
                            return <span style={{ color: tone }}>{sp < 10 ? sp.toFixed(1) : Math.round(sp)}×</span>;
                          })()}
                        </td>}
                        {visibleCols.has('trend') && <td>
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
                        </td>}
                        {visibleCols.has('traces') && <td className="sticky-right"
                            style={{
                              // Sticky cells float over scrolled content —
                              // the err-row tint must be flattened over the
                              // opaque base here (the tr's inline tint is
                              // color-mix over TRANSPARENT and would let
                              // scrolled columns bleed through).
                              background: r.errorRate >= 5
                                ? 'color-mix(in srgb, var(--err) 7%, var(--bg0))'
                                : undefined,
                            }}>
                          {/* /traces filter on (service, search=path).
                              The search field matches span.name OR
                              attrs; combined with rootOnly=false and
                              the service filter, this returns every
                              trace that includes a call on this
                              endpoint. */}
                          <Link to={tracesLink(r, range, env, cluster)}
                                style={{ fontSize: 11, color: 'var(--accent2)' }}>
                            view →
                          </Link>
                        </td>}
                      </tr>
                      {isExpanded && (
                        <tr>
                          <td />
                          {/* v0.8.574 — span every VISIBLE data column
                              (?cols= can hide any of the 14). */}
                          <td colSpan={visibleCols.size} style={{ background: 'var(--bg0)', padding: '8px 14px' }}>
                            <DependencyStrip
                              service={r.service}
                              window={dep.since} capped={dep.capped}
                              entry={depsByService[`${r.service}@${dep.since}`]} />
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
                  applies on the raw path, which the cluster filter (v0.8.356)
                  and the env filter (v0.8.385) both force. */}
              Path source priority: {(cluster || env)
                ? <><code>http.route</code> (templated) → <code>url.path</code> → <code>http.target</code></>
                : <><code>http.route</code> (templated) → <code>http.target</code></>}.
              Server / consumer spans only — outbound client spans count under
              the callee's row. P50/P95/P99 are true window quantiles
              (tdigest).
            </div>
          </>
        )}
        {/* Madde 4 sweep — zoom range'i değiştirince modal'daki satır bayat
            kalmasın: aynı (service,path,method) satırının TAZE kopyası
            rows'tan yeniden bulunur (drawer'ın v0.8.360 find deseni).
            v0.9.206 review-fix — taze kopya YOKSA grafikler bayat satırdan
            üretilmez (rowIsStale); modal açık kalır, kimlik/başlık durur. */}
        <EndpointMetricModal
          row={detail ? freshDetailRow ?? detail : null}
          rowIsStale={!!detail && !freshDetailRow}
          onClose={() => setDetail(null)} range={range}
          env={env} cluster={cluster}
          onZoom={handleZoom} onZoomReset={handleZoomReset} />
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
            env={env}
            cluster={cluster}
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
  row, rowIsStale, onClose, range, env, cluster, onZoom, onZoomReset,
}: {
  row: EndpointRow | null; onClose: () => void; range: TimeRange;
  // v0.9.307 — the scope this modal's row was read under, so its
  // pivots ask the same question the table did.
  env?: string; cluster?: string;
  // v0.9.206 review-fix — true = row, MEVCUT range'in sonuçlarında
  // bulunamayan bayat click-time satırı. Kimlik/başlık ondan çizilir
  // ama time-series ondan ÜRETİLMEZ: bucketsToSeries eski pencere
  // bucket'larını yeni eksene yayıp yeni bucket genişliğine böler
  // (rps pencere oranı kadar şişer).
  rowIsStale?: boolean;
  // Madde 4 sweep — modal grafiklerinde drag-zoom → sayfa range'i,
  // çift-tık → sayfa zoom geri-yığını (MetricTile üzerinden MLC'ye iner).
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  onZoomReset?: () => void;
}) {
  // Hooks must run unconditionally — call useMemo before any
  // early return (React rules-of-hooks).
  const series = useMemo(() => {
    // v0.9.206 review-fix — bayat satırdan seri fabrikasyonu yok;
    // boş seri MetricTile'ın "no data" boş hâlini düşürür.
    if (!row || rowIsStale) return { calls: [], errors: [], p99: [] };
    return bucketsToSeries(row, range);
  }, [row, rowIsStale, range]);

  if (!row) return <Modal open={false} onClose={onClose} />;
  // v0.9.206 review-fix — bayat satırda boş hâl mesajı sebebi söyler.
  const emptyLabel = rowIsStale
    ? 'no data for this endpoint in the zoomed window' : undefined;
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
          unit="rps"
          service={row.service}
          range={range}
          emptyLabel={emptyLabel}
          onZoom={onZoom}
          onZoomReset={onZoomReset}
        />
        <MetricTile
          label="Errors"
          big={fmtNum(row.errors)}
          sub={`${row.errorRate.toFixed(2)}% rate`}
          subCls={errCls}
          series={series.errors}
          unit="%"
          service={row.service}
          range={range}
          emptyLabel={emptyLabel}
          onZoom={onZoom}
          onZoomReset={onZoomReset}
        />
        <MetricTile
          label="P99 latency"
          big={`${row.p99Ms.toFixed(0)} ms`}
          sub={`peak ${maxP99.toFixed(0)} ms · avg ${row.avgMs.toFixed(0)} ms`}
          series={series.p99}
          unit="ms"
          service={row.service}
          range={range}
          emptyLabel={emptyLabel}
          onZoom={onZoom}
          onZoomReset={onZoomReset}
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
          to={tracesLink(row, range, env, cluster)}
          style={{ fontSize: 12, color: 'var(--accent2)' }}
        >
          View traces →
        </Link>
        {/* v0.9.307 (brief N6b) — http.route is a resolver tier
            dimension, so Explore answers this from the spanmetrics
            rollups: zero new queries, and the p99 series arrives
            already grouped by the route this modal is about. */}
        <Link
          to={exploreLink(row, range, 'p99', env, cluster)}
          title="Open this route's p99 in Explore — charted from the metric rollups, where you can add dimensions or compare against another query."
          style={{ fontSize: 12, color: 'var(--accent2)' }}
        >
          Open in Explore →
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
  label, big, sub, subCls, series, unit, service, range, emptyLabel, onZoom, onZoomReset,
}: {
  label: string; big: string; sub: string; subCls?: string;
  series: SpanMetricSeries[]; unit?: string;
  // v0.6.16 — pass service + range so the tile can overlay
  // EventMarkers on its chart. Optional so the component stays
  // usable on pages that don't have an event story yet.
  service?: string; range?: TimeRange;
  // v0.9.206 review-fix — boş hâl mesajı override'ı (bayat-satır hâli).
  emptyLabel?: string;
  // Madde 4 sweep — drag-zoom → sayfa range'i, çift-tık → geri-yığın.
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  onZoomReset?: () => void;
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
            onZoom={onZoom}
            onZoomReset={onZoomReset}
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
        }}>{emptyLabel ?? 'no data in window'}</div>
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
  const bucketSec = bucketNs / 1e9;
  const timeAtBucket = (i: number) => from + bucketNs * i + bucketNs / 2;
  const buildPoints = (arr: number[]) => arr.map((v, i) => ({
    time: timeAtBucket(i),
    value: v,
  }));
  // Madde 4 sweep — Calls/Errors birimsiz HAM SAYAÇTI ("12500" neyin
  // nesi?). Calls per-bucket sayacı bucket genişliğine bölünüp gerçek
  // rps olur; Errors, calls'a oranlanıp error % olur (calls=0 → 0%).
  // Tile başlık istatistikleri (total/peak) ham sayaçtan okumaya devam
  // eder — yalnız chart eksen/tooltip'i orana döner (Datadog dili).
  const rpsPoints = calls.map((v, i) => ({
    time: timeAtBucket(i),
    value: bucketSec > 0 ? v / bucketSec : v,
  }));
  const errPctPoints = errs.map((v, i) => ({
    time: timeAtBucket(i),
    value: (calls[i] ?? 0) > 0 ? (v / calls[i]) * 100 : 0,
  }));
  return {
    calls: calls.length ? [{ groupKey: ['calls'], points: rpsPoints }] : [],
    errors: errs.length ? [{ groupKey: ['errors'], points: errPctPoints }] : [],
    p99: p99s.length ? [{ groupKey: ['p99 ms'], points: buildPoints(p99s) }] : [],
  };
}

// v0.9.307 — env/cluster ride the pivot. Without them a row read
// under env=uat opened an UNFILTERED trace list: the pivot silently
// widened the question it was launched from, which is the same
// silent-scope class v0.9.306 closed inside the drawer.
function tracesLink(r: EndpointRow, range: TimeRange, env?: string, cluster?: string): string {
  return `/traces?service=${encodeURIComponent(r.service)}` +
    `&search=${encodeURIComponent(r.path)}` +
    `&range=${encodeURIComponent(encodeRange(range))}` +
    (env ? `&env=${encodeURIComponent(env)}` : '') +
    (cluster ? `&cluster=${encodeURIComponent(cluster)}` : '') +
    `&view=list&rootOnly=false`;
}

// exploreLink — "Open in Explore →" (v0.9.307, brief N6b).
//
// Zero new queries: http.route is already a resolver tier dimension
// (TIER_DIM_KEYS), so Explore answers this from the spanmetrics
// rollups rather than raw spans. The URL is the SAME legacy
// ?result=metric shape OperationsTable already emits — no new scheme
// invented, and seedFromLegacyParams decodes it unchanged.
//
// env rides along for the same reason it rides tracesLink: a pivot
// must carry the scope it was launched from, or it answers a wider
// question than the one on screen.
function exploreLink(
  r: EndpointRow, range: TimeRange, agg: string, env?: string, cluster?: string,
): string {
  const filters = encodeFilters([
    { k: 'service.name', op: '=', v: [r.service] },
    { k: 'http.route', op: '=', v: [r.path] },
  ]);
  return `/explore?${buildQuery([
    ['range', encodeRange(range)],
    ['filters', filters],
    ['agg', agg],
    ['field', 'duration_ms'],
    ['result', 'metric'],
    ['env', env ?? ''],
    ['cluster', cluster ?? ''],
  ])}`;
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
//
// v0.9.257 — the strip used to hardcode since='1h' while labelling itself
// "last 1h". Self-consistent, but every OTHER panel on the page followed the
// range picker, so an operator on a 24h page read 1h of dependencies and had
// no way to tell. Three things were wrong and all three are props now:
//   · the window is the page range (clamped, see the caller), not a constant;
//   · the counts are call-edges inside a biased sample of the LARGEST traces,
//     not the fleet totals "N spans … in the last 1h" claimed they were;
//   · the per-service cache ignored the window, so the first-expanded range
//     pinned that service's numbers for the rest of the session.
function DependencyStrip({ service, window: win, capped, entry }: {
  service: string;
  // Effective window actually queried, already clamped — always rendered, so
  // it can never drift from the data the way the old hardcoded "1h" did.
  window: string;
  capped: boolean;
  entry?: { deps: import('@/lib/types').NeighborStat[]; sampledFrom: number };
}) {
  if (entry === undefined) {
    return (
      <span style={{ fontSize: 11, color: 'var(--text3)' }}>
        Loading {service}'s dependencies…
      </span>
    );
  }
  const { deps, sampledFrom } = entry;
  if (deps.length === 0) {
    return (
      <span style={{ fontSize: 11, color: 'var(--text3)' }}>
        No downstream calls observed for {service} in the last {win}
        {sampledFrom > 0
          ? ` (sampled ${sampledFrom} trace${sampledFrom === 1 ? '' : 's'}).`
          : '.'}
      </span>
    );
  }
  const top = [...deps].sort((a, b) => b.spanCount - a.spanCount).slice(0, 5);
  return (
    <div style={{
      display: 'flex', flexDirection: 'column', gap: 6,
      fontSize: 11,
    }}>
      {/* v0.9.257 — states the real window AND that the counts are
          sample-scoped. The counts are crossing call-edges seen inside the
          sampled traces, and ServiceNeighbors picks those traces with
          ORDER BY count() DESC — the BIGGEST traces, not a uniform sample.
          "top 5 by span volume" over an unqualified window read as a fleet
          ranking; it is not one. */}
      <div style={{
        fontSize: 10, color: 'var(--text3)',
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>
        {service} → downstream · last {win}{capped && ' (capped)'} · top 5 of {deps.length}
        {sampledFrom > 0 && ` · sampled ${sampledFrom} largest trace${sampledFrom === 1 ? '' : 's'}`}
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
        {top.map((d, i) => (
          <Link key={i}
            to={`/service?name=${encodeURIComponent(d.service)}`}
            title={`${service} → ${d.service}\n${fmtNum(d.spanCount)} call${d.spanCount === 1 ? '' : 's'} across ${fmtNum(d.traceCount)} of the ${sampledFrom} sampled traces (last ${win})`}
            style={{
              display: 'inline-flex', alignItems: 'center', gap: 6,
              padding: '3px 8px', borderRadius: 12,
              background: 'var(--bg2)', border: '1px solid var(--border)',
              color: 'var(--text2)', fontFamily: 'ui-monospace, monospace',
              fontSize: 10, textDecoration: 'none',
            }}>
            <span style={{ fontWeight: 600 }}>{d.service}</span>
            <span style={{ color: 'var(--text3)' }}>{fmtNum(d.spanCount)} calls</span>
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
