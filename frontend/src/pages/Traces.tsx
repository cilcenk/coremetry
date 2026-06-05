// Traces.tsx — the trace explorer (Phase 1 Task B, Tempo/Datadog-grade).
//
// Rebuilt on the Phase-0 perf primitives + the OTel correlation layer:
//   • Header viz: Volume (stacked ok+error bars + p99 line + TOTAL/ERRORS/
//     ERROR RATE/P99 MAX stats) ↔ Latency (duration-vs-time scatter, log y,
//     hover/click/drag-brush). Both derive from the live, filtered rows.
//   • RED-from-traces panel (rate/errors/p99) over the same filtered set.
//   • The trace table renders through VirtualTable (windowed) with a Duration
//     BAR, service-coloured badges, error tints, row-expand mini-waterfall,
//     j/k/Enter/"/" keyboard nav.
//   • Quick-filter chips (Errors / Slow>1s / per-top-service), "+ Add filter"
//     otel-attr chips, "+ Column" via ColumnManager, full filter row.
//   • Aggregated + Shapes tabs preserved.
//
// Range is the SINGLE-source-of-truth via useUrlRange; timeRangeToNs(range)
// only ever runs inside a useMemo([range]) (the v0.5.184 trap).

import { useEffect, useMemo, useRef, useState, Suspense, Fragment } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { IconSearch } from '@/components/icons';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { OperationPicker } from '@/components/OperationPicker';
import { ServicePicker } from '@/components/ServicePicker';
import { FilterBuilder } from '@/components/FilterBuilder';
import { Button } from '@/components/ui/Button';
import { Pager } from '@/components/Pager';
import { ColumnManager } from '@/components/ColumnManager';
import { VirtualTable } from '@/components/ui/VirtualTable';
import { useDataTable } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { api } from '@/lib/api';
import { useUrlRange } from '@/lib/useUrlRange';
import { tsDateTime, timeRangeToNs, fmtNum } from '@/lib/utils';
import { encodeRange, encodeFilters, decodeFilters, buildQuery } from '@/lib/urlState';
import type { TracesResponse, TraceRow, TimeRange, SortColumn, SortOrder, AggregateRow, FilterExpr } from '@/lib/types';

import { VolumeChart } from '@/components/traces/VolumeChart';
import { LatencyScatter } from '@/components/traces/LatencyScatter';
import { RedFromTraces } from '@/components/traces/RedFromTraces';
import { MiniWaterfall } from '@/components/traces/MiniWaterfall';
import { ShapesView } from '@/components/traces/ShapesView';
import { SvcBadge, DurationBar, QuickChip, svcColor } from '@/components/traces/shared';

// Attribute keys offered by the "+ Add filter" menu. Each appends a FilterExpr
// to advFilters (server-narrowing + URL-reflected). Mirrors the OTel semconv
// keys the task calls out (http.status_code/http.route/rpc.method/db.system/
// k8s.pod.name/cloud.region).
const QUICK_ATTR_KEYS = [
  'http.status_code', 'http.route', 'rpc.method', 'db.system',
  'k8s.pod.name', 'cloud.region',
];
const QUICK_ATTR_DEFAULT: Record<string, string> = {
  'http.status_code': '500',
  'db.system': 'oracle',
};

type View = 'list' | 'aggregate' | 'shapes';
type GroupBy =
  | 'operation' | 'service' | 'kind' | 'status'
  | 'http_method' | 'http_route' | 'http_status'
  | 'host' | 'deploy_env' | 'scope' | 'attr';

const GROUP_OPTIONS: { value: GroupBy; label: string }[] = [
  { value: 'operation',   label: 'Operation' },
  { value: 'service',     label: 'Service' },
  { value: 'kind',        label: 'Kind' },
  { value: 'status',      label: 'Status' },
  { value: 'http_method', label: 'HTTP method' },
  { value: 'http_route',  label: 'HTTP route' },
  { value: 'http_status', label: 'HTTP status' },
  { value: 'host',        label: 'Host' },
  { value: 'deploy_env',  label: 'Deploy env' },
  { value: 'scope',       label: 'Scope' },
  { value: 'attr',        label: 'Attribute…' },
];

type AggSort = 'count' | 'perMin' | 'errorRate' | 'avg' | 'p50' | 'p95' | 'p99' | 'max' | 'name';
const AGG_NATURAL: Record<AggSort, SortOrder> = {
  count: 'desc', perMin: 'desc', errorRate: 'desc', avg: 'desc',
  p50: 'desc', p95: 'desc', p99: 'desc', max: 'desc', name: 'asc',
};

// Fixed list columns. The trace list is SERVER-paged (50/page), so per the
// useDataTable contract it keeps its SERVER sort (header click → server sort)
// and adopts only the resize half of the primitive. We give the data columns
// no `sortValue` (client-sorting a 50-row server page would scramble server
// order); the header click routes to the server sort below.
const FIXED_COLS = ['time', 'service', 'operation', 'duration', 'spans', 'status'] as const;
const COL_LABEL: Record<string, string> = {
  time: 'Time', service: 'Service', operation: 'Operation',
  duration: 'Duration', spans: 'Spans', status: 'Status',
};
const COL_W: Record<string, number> = {
  time: 168, service: 130, operation: 300, duration: 200, spans: 72, status: 84,
};
const ATTR_W = 160;
// Which fixed columns map to a server SortColumn (others aren't server-sortable).
const SERVER_SORTABLE: Partial<Record<string, SortColumn>> = {
  time: 'time', service: 'service', operation: 'operation',
  duration: 'duration', spans: 'spans', status: 'status',
};

// sortAccessor — the client-side sort value matching each server sort column.
// On a server-paged list this is a no-op (the server already returns rows in
// this order), but it keeps the shared primitive's local sort consistent with
// the server order rather than scrambling the page.
function sortAccessor(col: SortColumn): (r: TraceRow) => number | string {
  switch (col) {
    case 'time':      return r => r.startTime;
    case 'duration':  return r => r.durationMs;
    case 'spans':     return r => r.spanCount;
    case 'service':   return r => r.serviceName;
    case 'operation': return r => r.rootName;
    case 'status':    return r => (r.hasError ? 1 : 0);
    default:          return r => r.startTime;
  }
}

function TracesPageInner() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();

  const [range, setRange] = useUrlRange('30m');
  const [view, setView] = useState<View>(() => {
    const v = searchParams.get('view');
    return v === 'aggregate' || v === 'shapes' ? v : 'list';
  });

  // List view server sort.
  const [sort, setSort] = useState<SortColumn>(() => (searchParams.get('sort') as SortColumn) || 'time');
  const [order, setOrder] = useState<SortOrder>(() => (searchParams.get('order') === 'asc' ? 'asc' : 'desc'));
  const [page, setPage] = useState(() => parseInt(searchParams.get('page') ?? '0', 10) || 0);

  // Aggregate view sort + group-by.
  const [groupBy, setGroupBy] = useState<GroupBy>(() => {
    const v = searchParams.get('groupBy') as GroupBy | null;
    return GROUP_OPTIONS.some(o => o.value === v) ? (v as GroupBy) : 'operation';
  });
  const [groupAttr, setGroupAttr] = useState<string>(() => searchParams.get('groupAttr') ?? '');
  const [aggSort, setAggSort] = useState<AggSort>(() => (searchParams.get('aggSort') as AggSort) || 'count');
  const [aggOrder, setAggOrder] = useState<SortOrder>(() => (searchParams.get('aggOrder') === 'asc' ? 'asc' : 'desc'));

  const [filter, setFilter] = useState(() => ({
    service:  searchParams.get('service') ?? '',
    search:   searchParams.get('search')  ?? '',
    traceId:  searchParams.get('traceId') ?? '',
    minMs:    searchParams.get('minMs')   ?? '',
    maxMs:    searchParams.get('maxMs')   ?? '',
    hasError: searchParams.get('hasError') === 'true',
    rootOnly: searchParams.get('rootOnly') !== 'false',
    requireServices: (searchParams.get('services') ?? '').split(',').map(s => s.trim()).filter(Boolean),
  }));
  const [draft, setDraft] = useState(filter);
  const [advFilters, setAdvFilters] = useState<FilterExpr[]>(() => decodeFilters(searchParams.get('filters')));
  const [extraCols, setExtraCols] = useState<string[]>(
    () => (searchParams.get('cols') ?? '').split(',').map(s => s.trim()).filter(Boolean));

  // Header viz mode + interaction state.
  const [viz, setViz] = useState<'volume' | 'latency'>(() => searchParams.get('viz') === 'latency' ? 'latency' : 'volume');
  const [quick, setQuick] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [addFilterOpen, setAddFilterOpen] = useState(false);
  const addFilterRef = useRef<HTMLDivElement>(null);
  const filterInputRef = useRef<HTMLInputElement>(null);
  // Scatter brush narrows the page range; stash the pre-brush range for restore.
  const [brushPrev, setBrushPrev] = useState<TimeRange | null>(null);

  const [data, setData] = useState<TracesResponse | null | undefined>(undefined);
  const [agg, setAgg] = useState<AggregateRow[] | null | undefined>(undefined);
  const [listErr, setListErr] = useState<string | null>(null);
  const [retryNonce, setRetryNonce] = useState(0);
  const [showTotal, setShowTotal] = useState(false);

  // ── State → URL (replaceState; restores filters/sort/page on back). ──────────
  // `range` is included via encodeRange so the URL stays the single source of
  // truth even when useUrlRange's own writer and this effect both touch it.
  useEffect(() => {
    const qs = buildQuery([
      ['range',    encodeRange(range)],
      ['view',     view !== 'list' ? view : ''],
      ['viz',      viz !== 'volume' ? viz : ''],
      ['sort',     sort !== 'time' ? sort : ''],
      ['order',    order !== 'desc' ? order : ''],
      ['page',     page > 0 ? page : ''],
      ['groupBy',  view === 'aggregate' && groupBy !== 'operation' ? groupBy : ''],
      ['groupAttr', view === 'aggregate' && groupBy === 'attr' ? groupAttr : ''],
      ['aggSort',  view === 'aggregate' && aggSort !== 'count' ? aggSort : ''],
      ['aggOrder', view === 'aggregate' && aggOrder !== 'desc' ? aggOrder : ''],
      ['service',  filter.service],
      ['search',   filter.search],
      ['traceId',  filter.traceId],
      ['minMs',    filter.minMs],
      ['maxMs',    filter.maxMs],
      ['hasError', filter.hasError ? 'true' : ''],
      ['rootOnly', filter.rootOnly ? '' : 'false'],
      ['services', filter.requireServices.join(',')],
      ['filters',  encodeFilters(advFilters)],
      ['cols',     extraCols.join(',')],
    ]);
    const target = qs ? `?${qs}` : '';
    if (typeof window !== 'undefined' && target !== window.location.search) {
      navigate(`/traces${target}`, { preventScrollReset: true, replace: true });
    }
  }, [range, view, viz, sort, order, page, groupBy, groupAttr, aggSort, aggOrder, filter, advFilters, extraCols, navigate]);

  // ── List fetch ───────────────────────────────────────────────────────────
  const listRangeNs = useMemo(() => timeRangeToNs(range), [range]);
  useEffect(() => {
    if (view !== 'list') return;
    setData(undefined);
    setListErr(null);
    const tid = filter.traceId.trim().toLowerCase();
    const useTimeRange = tid.length === 0;
    const { from, to } = useTimeRange ? listRangeNs : { from: undefined, to: undefined };
    api.traces({
      limit: 50, offset: page * 50, from, to, sort, order,
      service: filter.service || undefined,
      search: filter.search || undefined,
      traceId: tid || undefined,
      minMs: filter.minMs || undefined,
      maxMs: filter.maxMs || undefined,
      hasError: filter.hasError || undefined,
      rootOnly: filter.rootOnly || undefined,
      services: filter.requireServices.length ? filter.requireServices : undefined,
      filters: advFilters.length ? JSON.stringify(advFilters) : undefined,
      extraAttrs: extraCols.length ? extraCols.join(',') : undefined,
      count: showTotal && !tid ? 'exact' : 'skip',
    }).then(setData).catch((e: unknown) => {
      setListErr(e instanceof Error ? e.message : 'Request failed');
      setData(null);
    });
  }, [view, listRangeNs, sort, order, page, filter, advFilters, extraCols, showTotal, retryNonce]);

  // ── Aggregate fetch ──────────────────────────────────────────────────────
  const aggRangeNs = useMemo(() => timeRangeToNs(range), [range]);
  useEffect(() => {
    if (view !== 'aggregate') return;
    setAgg(undefined);
    const { from, to } = aggRangeNs;
    const safeGroup = groupBy === 'attr' ? 'operation' : groupBy;
    const safeAttr  = groupBy === 'attr' ? groupAttr.trim() : '';
    api.tracesAggregate({
      groupBy: safeGroup, sort: aggSort, order: aggOrder, limit: 200, from, to,
      groupAttr: safeAttr || undefined,
      service: filter.service || undefined,
      search: filter.search || undefined,
      hasError: filter.hasError || undefined,
      minMs: filter.minMs || undefined,
      maxMs: filter.maxMs || undefined,
      filters: advFilters.length ? JSON.stringify(advFilters) : undefined,
    }).then(setAgg).catch(() => setAgg(null));
  }, [view, aggRangeNs, groupBy, groupAttr, aggSort, aggOrder, filter, advFilters]);

  // apply commits the draft as the live filter (overrideService sidesteps the
  // picker auto-commit race).
  const apply = (overrideService?: string) => {
    const tid = draft.traceId.trim().toLowerCase();
    if (/^[0-9a-f]{32}$/.test(tid)) { navigate(`/trace?id=${tid}`); return; }
    const next = overrideService != null ? { ...draft, service: overrideService } : draft;
    setPage(0);
    if (overrideService != null) setDraft(next);
    setFilter(next);
  };
  // Auto-apply 250ms after the last draft edit (Datadog/Honeycomb feel).
  useEffect(() => {
    if (JSON.stringify(draft) === JSON.stringify(filter)) return;
    const t = setTimeout(() => apply(), 250);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draft, filter]);
  const reset = () => {
    const empty = { service: '', search: '', traceId: '', minMs: '', maxMs: '', hasError: false, rootOnly: true, requireServices: [] as string[] };
    setDraft(empty); setFilter(empty); setPage(0);
    setAdvFilters([]); setQuick(null); setExpanded(null);
  };
  const toggleAggSort = (col: AggSort) => {
    if (aggSort === col) setAggOrder(aggOrder === 'desc' ? 'asc' : 'desc');
    else { setAggSort(col); setAggOrder(AGG_NATURAL[col]); }
  };

  // Close the "+ Add filter" menu on outside click.
  useEffect(() => {
    if (!addFilterOpen) return;
    const onDoc = (e: MouseEvent) => { if (!addFilterRef.current?.contains(e.target as Node)) setAddFilterOpen(false); };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [addFilterOpen]);

  const addAttrFilter = (k: string) => {
    if (advFilters.some(f => f.k === k)) { setAddFilterOpen(false); return; }
    setAdvFilters([...advFilters, { k, op: '=', v: [QUICK_ATTR_DEFAULT[k] ?? ''] }]);
    setAddFilterOpen(false);
    setPage(0);
  };

  const traces = data?.traces ?? [];
  const total = data?.total;
  const hasMore = data?.hasMore ?? false;

  // Quick-filter chips narrow the CURRENT page client-side (instant).
  const topSvcs = useMemo(() => {
    const seen: string[] = [];
    for (const t of traces) {
      if (t.serviceName && !seen.includes(t.serviceName)) seen.push(t.serviceName);
      if (seen.length >= 4) break;
    }
    return seen;
  }, [traces]);
  const errCount = useMemo(() => traces.filter(t => t.hasError).length, [traces]);
  const displayRows = useMemo(() => {
    if (!quick) return traces;
    if (quick === 'err') return traces.filter(t => t.hasError);
    if (quick === 'slow') return traces.filter(t => t.durationMs > 1000);
    return traces.filter(t => t.serviceName === quick);
  }, [traces, quick]);
  const visibleMax = useMemo(() => displayRows.reduce((m, t) => Math.max(m, t.durationMs), 0), [displayRows]);

  // Reset transient state on a new query / page.
  useEffect(() => { setExpanded(null); }, [page, filter, advFilters, range, view]);
  useEffect(() => { if (quick) setQuick(null); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [filter, advFilters, range, view]);

  const openTrace = (t: TraceRow) => navigate(`/trace?id=${t.traceId}`);

  // Scatter drag-brush → narrow the page range; remember prior range for restore.
  const applyBrush = (fromMs: number, toMs: number) => {
    if (toMs - fromMs < 1) return;
    setBrushPrev(prev => prev ?? range);
    setRange({ preset: 'custom', fromMs, toMs });
    setPage(0);
  };
  const clearBrush = () => {
    if (brushPrev) setRange(brushPrev);
    setBrushPrev(null);
    setPage(0);
  };

  // Hover-prefetch the trace spans (server-cached 5m) so the row click is a HIT.
  const prefetched = useRef<Set<string>>(new Set());
  const prefetchTrace = (id: string) => {
    if (prefetched.current.has(id)) return;
    prefetched.current.add(id);
    api.trace(id).catch(() => {});
  };

  const exportRangeNs = listRangeNs;

  // ── useDataTable: the shared sortable + resizable + j/k/Enter + "/" focus
  // primitive, rendered through VirtualTable. The list is SERVER-paged, so the
  // header sort drives the SERVER query (we sync dt.sort → sort/order below).
  // The local client sort by the same accessor is a no-op on already-server-
  // sorted rows, so the table never disagrees with the server order. Only the
  // server-sortable fixed columns get a sortValue; attribute columns resize but
  // don't sort (the backend doesn't sort by a projected attr). ──
  const colIds = useMemo(() => [...FIXED_COLS, ...extraCols], [extraCols]);
  const columns: DataTableColumn<TraceRow>[] = useMemo(() =>
    colIds.map(id => {
      const server = SERVER_SORTABLE[id];
      return {
        id,
        label: COL_LABEL[id] ?? id,
        width: COL_W[id] ?? ATTR_W,
        numeric: id === 'spans',
        naturalDir: (id === 'service' || id === 'operation' ? 'asc' : 'desc') as SortOrder,
        sortValue: server ? sortAccessor(server) : undefined,
      };
    }), [colIds]);

  const dt = useDataTable<TraceRow>({
    storageKey: 'traces-list',
    columns,
    rows: displayRows,
    initialSort: { id: sort, dir: order },
    onOpen: (t) => openTrace(t),
    searchRef: filterInputRef,
  });

  // Sync the shared table's sort → the SERVER query. The header click flips
  // dt.sort; we translate it into the server sort/order + reset the page. Guard
  // on a genuine difference so we don't loop with our own initialSort.
  useEffect(() => {
    const id = dt.sort.id;
    if (!id) return;
    const server = SERVER_SORTABLE[id];
    if (!server) return;
    if (server !== sort || dt.sort.dir !== order) {
      setSort(server);
      setOrder(dt.sort.dir);
      setPage(0);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dt.sort.id, dt.sort.dir]);

  return (
    <>
      <Topbar title="Traces" range={range}
        onRangeChange={(r) => { setBrushPrev(null); setRange(r); }} />
      <div id="content">
        <SavedViewsBar page="traces" />

        {/* Header viz — Volume / Latency toggle (list view only; both derive
            from the live, filtered list rows). */}
        {view === 'list' && (
          <>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 8 }}>
              <div className="segmented">
                <button className={viz === 'volume' ? 'active' : ''} onClick={() => setViz('volume')}>Volume</button>
                <button className={viz === 'latency' ? 'active' : ''} onClick={() => setViz('latency')}>Latency</button>
              </div>
              {brushPrev && (
                <Button variant="secondary" size="sm" onClick={clearBrush} title="Restore the previous time range">
                  Clear selection ✕
                </Button>
              )}
            </div>

            {data === undefined ? (
              <div style={{ background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 8, padding: 12, marginBottom: 10, height: 192, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
                <Spinner />
              </div>
            ) : viz === 'volume' ? (
              // slimmer + recedes — it's the brush/overview "tool", not the
              // headline chart; the RED strip below carries the filtered numbers.
              <VolumeChart rows={displayRows} height={120} />
            ) : (
              <div style={{ background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 8, padding: 12, marginBottom: 10 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 14, marginBottom: 8, padding: '0 2px' }}>
                  <span style={{ fontSize: 11, color: 'var(--text2)', fontWeight: 700, letterSpacing: '0.5px', textTransform: 'uppercase' }}>
                    Latency distribution
                  </span>
                  <span style={{ flex: 1 }} />
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 10, color: 'var(--text3)' }}>
                    <span style={{ width: 8, height: 8, background: 'var(--accent)', borderRadius: 8 }} /> ok
                    <span style={{ width: 8, height: 8, background: 'var(--err)', borderRadius: 8, marginLeft: 8 }} /> error
                    <span style={{ marginLeft: 8 }}>· drag to brush a time range · y = duration (log)</span>
                  </span>
                </div>
                <LatencyScatter rows={displayRows} onOpen={openTrace} onBrush={applyBrush} />
              </div>
            )}

            {/* RED-from-traces (current filtered set). */}
            {data && traces.length > 0 && <RedFromTraces rows={displayRows} />}
          </>
        )}

        {/* View toggle + trace-id lookup */}
        <div className="controls" style={{ marginBottom: 8, alignItems: 'center' }}>
          <div className="segmented">
            <button onClick={() => setView('list')} className={view === 'list' ? 'active' : ''}>Traces</button>
            <button onClick={() => setView('aggregate')} className={view === 'aggregate' ? 'active' : ''}>Aggregated</button>
            <button onClick={() => setView('shapes')} className={view === 'shapes' ? 'active' : ''}
              title="Cluster traces by their (service, operation) signature — find dominant call patterns at a glance">
              Shapes
            </button>
          </div>
          {view === 'aggregate' && (
            <>
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>Group by:</span>
              <select value={groupBy} onChange={e => setGroupBy(e.target.value as GroupBy)}>
                {GROUP_OPTIONS.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
              </select>
              {groupBy === 'attr' && (
                <input placeholder="attribute key (e.g. user.id)" value={groupAttr}
                  onChange={e => setGroupAttr(e.target.value)}
                  onKeyDown={e => { if (e.key === 'Enter') (e.target as HTMLInputElement).blur(); }}
                  style={{ width: 200 }} />
              )}
            </>
          )}

          <div className="trace-lookup" style={{ marginLeft: 'auto' }}>
            <span className="tl-icon" aria-hidden><IconSearch size={14} /></span>
            <input placeholder="Trace ID (full or prefix)…" value={draft.traceId}
              onChange={e => setDraft({ ...draft, traceId: e.target.value })}
              onKeyDown={e => e.key === 'Enter' && apply()} />
            {draft.traceId && (
              <button className="tl-clear" type="button" title="Clear"
                onClick={() => { setDraft({ ...draft, traceId: '' }); setFilter({ ...filter, traceId: '' }); }}>✕</button>
            )}
            <button className="tl-go" type="button" onClick={() => apply()}>Go</button>
          </div>
        </div>

        {/* Filters */}
        <div className="controls" data-shortcut-search>
          <ServicePicker value={draft.service} onChange={v => setDraft({ ...draft, service: v })}
            placeholder="Service…" width={170} onEnter={(v) => apply(v)} />
          <OperationPicker service={draft.service} value={draft.search}
            onChange={v => setDraft({ ...draft, search: v })}
            placeholder="Operation…" width={240} onEnter={() => apply()} />
          <input ref={filterInputRef} placeholder="Min ms" value={draft.minMs}
            onChange={e => setDraft({ ...draft, minMs: e.target.value })} type="number" style={{ width: 72 }} />
          <input placeholder="Max ms" value={draft.maxMs}
            onChange={e => setDraft({ ...draft, maxMs: e.target.value })} type="number" style={{ width: 72 }} />
          <label style={{ display: 'flex', alignItems: 'center', gap: 5, color: 'var(--text2)', cursor: 'pointer' }}>
            <input type="checkbox" checked={draft.hasError} onChange={e => setDraft({ ...draft, hasError: e.target.checked })} />
            Errors only
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: 5, color: 'var(--text2)', cursor: 'pointer' }}
            title="Hide partial traces — only show traces whose root span landed in storage">
            <input type="checkbox" checked={draft.rootOnly} onChange={e => setDraft({ ...draft, rootOnly: e.target.checked })} />
            Root traces
          </label>
          <Button variant="primary" size="sm" onClick={() => apply()}>Search</Button>
          <Button variant="secondary" size="sm" onClick={reset}>Reset</Button>

          {/* CSV export — committed filter set. */}
          <a className="sec"
            href={`/api/traces/export.csv?${(() => {
              const { from, to } = exportRangeNs;
              const p = new URLSearchParams();
              p.set('from', String(from)); p.set('to', String(to));
              if (filter.service)  p.set('service',  filter.service);
              if (filter.search)   p.set('search',   filter.search);
              if (filter.traceId)  p.set('traceId',  filter.traceId);
              if (filter.minMs)    p.set('minMs',    filter.minMs);
              if (filter.maxMs)    p.set('maxMs',    filter.maxMs);
              if (filter.hasError) p.set('hasError', 'true');
              if (filter.rootOnly) p.set('rootOnly', 'true');
              if (filter.requireServices.length) p.set('services', filter.requireServices.join(','));
              if (advFilters.length) p.set('filters', JSON.stringify(advFilters));
              if (extraCols.length)  p.set('extraAttrs', extraCols.join(','));
              if (sort)  p.set('sort', sort);
              if (order) p.set('order', order);
              return p.toString();
            })()}`}
            download title="Download up to 10k matching traces as CSV (postmortem / audit use)"
            style={{ padding: '5px 10px', fontSize: 12, textDecoration: 'none', border: '1px solid var(--border)', borderRadius: 4, color: 'var(--accent2)', background: 'var(--bg2)' }}>
            ⬇ CSV
          </a>

          {/* "+ Add filter" otel-attr menu. */}
          <div ref={addFilterRef} style={{ position: 'relative' }}>
            <Button variant="secondary" size="sm" onClick={() => setAddFilterOpen(o => !o)}>+ Add filter</Button>
            {addFilterOpen && (
              <div style={{ position: 'absolute', top: 'calc(100% + 4px)', left: 0, zIndex: 60, minWidth: 210, background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 6, boxShadow: '0 8px 24px rgba(0,0,0,0.30)', padding: 6 }}>
                <div style={{ fontSize: 10, color: 'var(--text3)', padding: '4px 8px', fontWeight: 700, letterSpacing: '0.4px' }}>ATTRIBUTE</div>
                {QUICK_ATTR_KEYS.map(k => {
                  const already = advFilters.some(f => f.k === k);
                  return (
                    <div key={k} onClick={() => !already && addAttrFilter(k)}
                      style={{ padding: '6px 8px', fontSize: 11.5, borderRadius: 4, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', cursor: already ? 'default' : 'pointer', color: already ? 'var(--text3)' : 'var(--text)' }}
                      onMouseEnter={e => { if (!already) e.currentTarget.style.background = 'var(--bg3)'; }}
                      onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}>
                      {k}{already ? ' ✓' : ''}
                    </div>
                  );
                })}
              </div>
            )}
          </div>

          {view === 'list' && data && (
            <span style={{ marginLeft: 'auto', fontSize: 11.5, color: 'var(--text3)' }}>
              {displayRows.length} of {traces.length} traces
            </span>
          )}
        </div>

        {/* Quick-filter chips. */}
        {view === 'list' && traces.length > 0 && (
          <div style={{ display: 'flex', gap: 7, flexWrap: 'wrap', alignItems: 'center', marginBottom: 8 }}>
            <QuickChip active={quick === 'err'} onClick={() => setQuick(quick === 'err' ? null : 'err')} tone="err">
              Errors {errCount}
            </QuickChip>
            <QuickChip active={quick === 'slow'} onClick={() => setQuick(quick === 'slow' ? null : 'slow')}>
              Slow &gt;1s
            </QuickChip>
            {topSvcs.map(s => (
              <QuickChip key={s} active={quick === s} dot={svcColor(s)} onClick={() => setQuick(quick === s ? null : s)}>
                {s}
              </QuickChip>
            ))}
          </div>
        )}

        {/* requireServices banner. */}
        {filter.requireServices.length > 0 && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', padding: '8px 12px', marginBottom: 8, background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12 }}>
            <span style={{ color: 'var(--text2)', fontWeight: 600 }}>Trace must include:</span>
            {filter.requireServices.map((s) => (
              <span key={s} style={{ display: 'inline-flex', alignItems: 'center', gap: 6, padding: '2px 8px', borderRadius: 4, background: 'var(--bg3)', border: '1px solid var(--border)', fontFamily: 'ui-monospace, monospace' }}>
                {s}
                <button type="button" title="Remove"
                  onClick={() => setFilter({ ...filter, requireServices: filter.requireServices.filter(x => x !== s) })}
                  style={{ background: 'transparent', border: 'none', color: 'var(--text3)', cursor: 'pointer', padding: 0, fontSize: 12, lineHeight: 1 }}>×</button>
              </span>
            ))}
            <Button variant="secondary" size="sm" onClick={() => setFilter({ ...filter, requireServices: [] })} style={{ marginLeft: 'auto' }}>
              Clear all
            </Button>
          </div>
        )}

        {/* Advanced filter chips. */}
        <FilterBuilder value={advFilters} onChange={setAdvFilters}
          suggestedValues={{
            'kind': ['internal', 'server', 'client', 'producer', 'consumer'],
            'status_code': ['ok', 'error', 'unset'],
            'http.method': ['GET', 'POST', 'PUT', 'DELETE', 'PATCH'],
            'db.system': ['postgresql', 'mysql', 'redis', 'mongodb', 'elasticsearch'],
          }} />

        {/* List view. */}
        {view === 'list' && data === undefined && <TableSkeleton rows={10} cols={7} />}
        {view === 'list' && listErr && (
          <Empty icon="⚠" title="Query failed">
            <p>The trace query errored or timed out. Try a narrower time range, then retry.</p>
            <p className="mono" style={{ fontSize: 12, color: 'var(--text2)', wordBreak: 'break-word', margin: '8px 0' }}>{listErr}</p>
            <Button variant="secondary" size="sm" onClick={() => setRetryNonce(n => n + 1)}>↻ Retry</Button>
          </Empty>
        )}
        {view === 'list' && !listErr && data && traces.length === 0 && (
          <TracesEmpty service={filter.service} search={filter.search} range={range} onSwitchView={() => setView('aggregate')} />
        )}
        {view === 'list' && data && traces.length > 0 && (
          <>
            {/* Column toolbar — attribute columns are added via "+ Column"
                (ColumnManager) and removed by their chips. VirtualTable's shared
                header auto-renders the sortable/resizable data columns, so the
                add/remove affordances live here above the table. */}
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', marginBottom: 6 }}>
              <ColumnManager cols={extraCols}
                onAdd={k => { if (!extraCols.includes(k) && extraCols.length < 8) setExtraCols([...extraCols, k]); }} />
              {extraCols.map(c => (
                <span key={c} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, padding: '2px 8px', borderRadius: 4, background: 'var(--bg3)', border: '1px solid var(--border)', fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', fontSize: 11 }}>
                  {c}
                  <button type="button" title="Remove column"
                    onClick={() => setExtraCols(extraCols.filter(x => x !== c))}
                    style={{ background: 'transparent', border: 'none', color: 'var(--text3)', cursor: 'pointer', padding: 0, fontSize: 12, lineHeight: 1 }}>×</button>
                </span>
              ))}
            </div>
            <VirtualTable<TraceRow>
              dt={dt}
              height={Math.min(560, 44 + displayRows.length * 36)}
              rowHeight={36}
              leading={[30]}
              getRowKey={(t) => t.traceId}
              leadingHead={<th style={{ width: 30 }} />}
              renderRow={(t) => {
                const isOpen = expanded === t.traceId;
                return (
                  <Fragment>
                    <td onClick={(e) => { e.stopPropagation(); setExpanded(isOpen ? null : t.traceId); }}
                      style={{ textAlign: 'center', cursor: 'pointer', color: 'var(--text3)', userSelect: 'none' }}
                      title={isOpen ? 'Collapse preview' : 'Preview spans'}>
                      {isOpen ? '▾' : '▸'}
                    </td>
                    {colIds.map(id => (
                      <td key={id} onMouseEnter={() => prefetchTrace(t.traceId)}
                        onClick={() => openTrace(t)}
                        style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', cursor: 'pointer', background: t.hasError ? 'color-mix(in srgb, var(--err) 8%, transparent)' : undefined }}>
                        {renderTraceCell(id, t, visibleMax)}
                      </td>
                    ))}
                  </Fragment>
                );
              }}
            />
            {/* Row-expand mini-waterfall (rendered below the table so the
                virtualiser's uniform-height assumption isn't violated). */}
            {expanded && displayRows.some(t => t.traceId === expanded) && (
              <div style={{ border: '1px solid var(--border)', borderTop: 'none', borderRadius: '0 0 6px 6px' }}>
                <MiniWaterfall
                  traceId={expanded}
                  fallbackService={displayRows.find(t => t.traceId === expanded)?.serviceName ?? ''}
                  onOpen={() => { const t = displayRows.find(x => x.traceId === expanded); if (t) openTrace(t); }} />
              </div>
            )}
            <Pager page={page} pageSize={50} total={total} hasMore={hasMore} onPage={setPage}
              extras={
                <>
                  {total !== undefined ? (<>{total.toLocaleString()} total</>) : (
                    <>showing {traces.length}{hasMore ? '+' : ''}{' · '}
                      <a href="#" onClick={e => { e.preventDefault(); setShowTotal(true); }}
                        title="Run an exact count(DISTINCT trace_id) — can be slow at scale">Show total</a>
                    </>
                  )}
                  {' · '}sorted by <b>{sort}</b> {order}
                </>
              } />
          </>
        )}

        {/* Aggregate view. */}
        {view === 'aggregate' && agg === undefined && (
          <Spinner label="Aggregating traces by trace_id…" hint="Reads the trace_summary MV when the window is ≥5min, raw spans otherwise." />
        )}
        {view === 'aggregate' && agg && agg.length === 0 && (
          <Empty icon="∑" title="No groups in this window">
            <div style={{ marginTop: 6, color: 'var(--text2)' }}>
              The aggregate view needs at least one trace to group. Switch to the Traces tab to confirm there are matching rows, or widen the time range.
            </div>
          </Empty>
        )}
        {view === 'aggregate' && agg && agg.length > 0 && (
          <AggregateTable agg={agg} groupBy={groupBy} groupAttr={groupAttr}
            aggSort={aggSort} aggOrder={aggOrder} onSort={toggleAggSort}
            onDrill={(a) => {
              if (groupBy === 'service') { setFilter({ ...filter, service: a.groupKey }); setDraft({ ...draft, service: a.groupKey }); }
              else if (groupBy === 'operation') { setFilter({ ...filter, search: a.groupKey, service: a.groupExtra ?? filter.service }); setDraft({ ...draft, search: a.groupKey, service: a.groupExtra ?? draft.service }); }
              else if (a.groupExtra) { setFilter({ ...filter, service: a.groupExtra }); setDraft({ ...draft, service: a.groupExtra }); }
              setView('list'); setPage(0);
            }} />
        )}

        {/* Shapes view. */}
        {view === 'shapes' && <ShapesView range={range} service={filter.service || undefined} />}
      </div>
    </>
  );
}

// Per-column cell content for a trace row.
function renderTraceCell(id: string, t: TraceRow, visibleMax: number) {
  switch (id) {
    case 'time':      return <span className="mono">{tsDateTime(t.startTime)}</span>;
    case 'service':   return <SvcBadge name={t.serviceName} />;
    case 'operation': return <span title={t.rootName}>{t.rootName || '—'}</span>;
    case 'duration':  return <DurationBar ms={t.durationMs} err={t.hasError} max={visibleMax} />;
    case 'spans':     return <>{t.spanCount}</>;
    case 'status':    return t.hasError ? <span className="badge b-err">ERROR</span> : <span className="badge b-ok">OK</span>;
    default: {
      const v = t.extras?.[id] ?? '';
      return <span className="mono" style={{ fontSize: 11, color: v ? 'var(--text2)' : 'var(--text3)' }} title={v || ''}>{v || '—'}</span>;
    }
  }
}

function AggHeader({ col, label, sort, order, onSort, align }: {
  col: AggSort; label: string; sort: AggSort; order: SortOrder;
  onSort: (c: AggSort) => void; align?: 'left' | 'right';
}) {
  const active = sort === col;
  return (
    <th className={`sortable${active ? ' sorted' : ''}`} onClick={() => onSort(col)} style={{ textAlign: align ?? 'left' }}>
      {label}<span className="sort-arrow">{active ? (order === 'desc' ? '▼' : '▲') : '↕'}</span>
    </th>
  );
}

function AggregateTable({ agg, groupBy, groupAttr, aggSort, aggOrder, onSort, onDrill }: {
  agg: AggregateRow[]; groupBy: GroupBy; groupAttr: string;
  aggSort: AggSort; aggOrder: SortOrder; onSort: (c: AggSort) => void;
  onDrill: (a: AggregateRow) => void;
}) {
  return (
    <>
      <div className="table-wrap">
        <table>
          <thead><tr>
            <AggHeader col="name"      label={groupLabel(groupBy, groupAttr)} sort={aggSort} order={aggOrder} onSort={onSort} />
            {groupBy !== 'service' && <th>Service</th>}
            <AggHeader col="count"     label="Traces"  sort={aggSort} order={aggOrder} onSort={onSort} align="right" />
            <AggHeader col="perMin"    label="Per min" sort={aggSort} order={aggOrder} onSort={onSort} align="right" />
            <AggHeader col="errorRate" label="Error %" sort={aggSort} order={aggOrder} onSort={onSort} align="right" />
            <AggHeader col="avg"       label="Avg"     sort={aggSort} order={aggOrder} onSort={onSort} align="right" />
            <AggHeader col="p50"       label="P50"     sort={aggSort} order={aggOrder} onSort={onSort} align="right" />
            <AggHeader col="p95"       label="P95"     sort={aggSort} order={aggOrder} onSort={onSort} align="right" />
            <AggHeader col="p99"       label="P99"     sort={aggSort} order={aggOrder} onSort={onSort} align="right" />
            <AggHeader col="max"       label="Max"     sort={aggSort} order={aggOrder} onSort={onSort} align="right" />
          </tr></thead>
          <tbody>
            {agg.map(a => {
              const errCls = a.errorRate > 5 ? 'b-err' : a.errorRate > 0 ? 'b-warn' : 'b-ok';
              const drillable = a.withRawAvailable ?? a.traceCount;
              const missingRaw = a.traceCount - drillable;
              return (
                <tr key={`${a.groupKey}|${a.groupExtra}`} onClick={() => onDrill(a)} style={{ cursor: 'pointer' }}>
                  <td><b>{a.groupKey || '—'}</b></td>
                  {groupBy !== 'service' && <td><SvcBadge name={a.groupExtra ?? ''} /></td>}
                  <td className="mono" style={{ textAlign: 'right' }}>
                    {fmtNum(a.traceCount)}
                    {missingRaw > 0 && (
                      <span className="badge b-warn" style={{ marginLeft: 6, fontSize: 10 }}
                        title={`${fmtNum(drillable)} of ${fmtNum(a.traceCount)} traces still have raw span data — older traces aged out of the raw retention window.`}>
                        {fmtNum(drillable)} drillable
                      </span>
                    )}
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }} title="Traces per minute">{fmtPerMin(a.perMin)}</td>
                  <td className="mono" style={{ textAlign: 'right' }}><span className={`badge ${errCls}`}>{a.errorRate.toFixed(2)}%</span></td>
                  <td className="mono" style={{ textAlign: 'right' }}>{a.avgMs.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{a.p50Ms.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{a.p95Ms.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{a.p99Ms.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{a.maxMs.toFixed(1)}ms</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <div style={{ marginTop: 10, fontSize: 12, color: 'var(--text3)' }}>
        {agg.length} groups · grouped by <b style={{ color: 'var(--accent2)' }}>{groupBy}</b> · sorted by <b>{aggSort}</b> {aggOrder} · click a row to drill down
      </div>
    </>
  );
}

function groupLabel(g: GroupBy, attr: string): string {
  if (g === 'attr') return attr ? `Attr · ${attr}` : 'Attribute…';
  return GROUP_OPTIONS.find(o => o.value === g)?.label ?? 'Group';
}

function fmtPerMin(n: number): string {
  if (!n || n < 0) return '0/m';
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k/m`;
  if (n >= 10)   return `${n.toFixed(0)}/m`;
  return `${n.toFixed(2)}/m`;
}

export default function TracesPage() {
  return (
    <Suspense fallback={<Spinner />}>
      <TracesPageInner />
    </Suspense>
  );
}

// TracesEmpty — distinguishes "aged out of raw spans (MV still has it)" from
// "search matched nothing" so the operator gets the right next step.
function TracesEmpty({ service, search, range, onSwitchView }: {
  service: string; search: string; range: TimeRange; onSwitchView: () => void;
}) {
  const [mvSpans, setMvSpans] = useState<number | null | undefined>(undefined);
  const rangeNs = useMemo(() => timeRangeToNs(range), [range]);
  useEffect(() => {
    if (!service) { setMvSpans(null); return; }
    let cancelled = false;
    api.servicesPage(rangeNs, { name: service, limit: 1 })
      .then(d => {
        if (cancelled) return;
        const hit = (d?.services ?? []).find(s => s.name === service);
        setMvSpans(hit ? hit.spanCount : 0);
      })
      .catch(() => { if (!cancelled) setMvSpans(null); });
    return () => { cancelled = true; };
  }, [service, rangeNs]);
  const aged = service && search && (mvSpans ?? 0) > 0;
  return (
    <Empty icon="⋮" title="No traces found">
      <div style={{ marginTop: 6, color: 'var(--text2)' }}>
        {aged ? (
          <>
            <b style={{ color: 'var(--warn)' }}>{mvSpans!.toLocaleString()}</b> spans recorded for <code>{service}</code> in this window via the 5-min MV, but no raw spans match the search. This usually means the span data aged out past the raw-spans TTL while the MV still holds the rollup.{' '}
            <Button variant="secondary" size="sm" onClick={onSwitchView} style={{ marginLeft: 4 }}>Switch to Aggregate view →</Button>
          </>
        ) : (
          <>Try widening the time range, dropping the service or search filter, or unticking "Root traces". If even an unfiltered query is empty, check ingest health at <a href="/status" style={{ color: 'var(--accent2)' }}>/status</a>.</>
        )}
      </div>
    </Empty>
  );
}
