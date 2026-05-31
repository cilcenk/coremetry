import { useEffect, useMemo, useRef, useState, Suspense } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useTableNav } from '@/lib/useTableNav';
import { Topbar } from '@/components/Topbar';
import { TraceShapesView } from '@/components/TraceShapesView';
import { IconSearch } from '@/components/icons';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { Combobox } from '@/components/Combobox';
import { OperationPicker } from '@/components/OperationPicker';
import { ServicePicker } from '@/components/ServicePicker';
import { FilterBuilder } from '@/components/FilterBuilder';
import { TraceVolumeHistogram } from '@/components/TraceVolumeHistogram';
import { Pager } from '@/components/Pager';
import { ColumnManager } from '@/components/ColumnManager';
import { api } from '@/lib/api';
import { tsDateTime, timeRangeToNs, fmtNum, rowClickHandlers } from '@/lib/utils';
import { encodeRange, decodeRange, encodeFilters, decodeFilters, buildQuery } from '@/lib/urlState';
import type { TracesResponse, TimeRange, SortColumn, SortOrder, AggregateRow, FilterExpr } from '@/lib/types';

type View = 'list' | 'aggregate' | 'shapes';
// Group-by dimensions match the server-side whitelist + a special
// 'attr' bucket meaning 'use the custom attribute key in the
// adjacent input'. Adding new dimensions is server-side only —
// frontend just renders one more option.
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

function TracesPageInner() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();

  // All these hydrate from URL on first render so the back button
  // restores filters / sort / page intact after viewing a trace detail.
  const [range, setRange] = useState<TimeRange>(
    // Default to 5 min — matches the SRE "what just happened" entry
    // point for /traces. Users with longer windows tend to switch
    // explicitly anyway; defaulting to 1h was paying for a wide CH
    // scan most visitors didn't actually need.
    () => decodeRange(searchParams.get('range'), { preset: '30m' }));
  // v0.7.37 — Operator preference: the raw Traces list is the default landing
  // tab (arriving at /traces, the operator wants the actual traces first).
  // Aggregated + Shapes are a click away; ?view=aggregate / ?view=shapes force
  // them on demand. Tab order: Traces | Aggregated | Shapes.
  const [view, setView] = useState<View>(() => {
    const v = searchParams.get('view');
    return v === 'aggregate' || v === 'shapes' ? v : 'list';
  });

  // List view sort
  const [sort, setSort] = useState<SortColumn>(
    () => (searchParams.get('sort') as SortColumn) || 'time');
  const [order, setOrder] = useState<SortOrder>(
    () => (searchParams.get('order') === 'asc' ? 'asc' : 'desc'));
  const [page, setPage] = useState(
    () => parseInt(searchParams.get('page') ?? '0', 10) || 0);

  // Aggregate view sort + group-by
  const [groupBy, setGroupBy] = useState<GroupBy>(() => {
    const v = searchParams.get('groupBy') as GroupBy | null;
    return GROUP_OPTIONS.some(o => o.value === v) ? (v as GroupBy) : 'operation';
  });
  // Custom attribute key when groupBy === 'attr'. Persisted in URL
  // so a saved link restores the exact group expression.
  const [groupAttr, setGroupAttr] = useState<string>(() => searchParams.get('groupAttr') ?? '');
  const [aggSort, setAggSort] = useState<AggSort>(
    () => (searchParams.get('aggSort') as AggSort) || 'count');
  const [aggOrder, setAggOrder] = useState<SortOrder>(
    () => (searchParams.get('aggOrder') === 'asc' ? 'asc' : 'desc'));

  const [filter, setFilter] = useState(() => ({
    service:  searchParams.get('service') ?? '',
    search:   searchParams.get('search')  ?? '',
    traceId:  searchParams.get('traceId') ?? '',
    minMs:    searchParams.get('minMs')   ?? '',
    maxMs:    searchParams.get('maxMs')   ?? '',
    hasError: searchParams.get('hasError') === 'true',
    // Default ON: a fresh /traces visit hides Tempo-style "root
    // not available" partial fragments. Operator can untick to
    // see them. URL absent = on; explicit ?rootOnly=false = off.
    rootOnly: searchParams.get('rootOnly') !== 'false',
    // requireServices: trace must include spans from every listed
    // service. Driven by the backtrace 'Traces' drill-in via a
    // ?services=A,B URL param.
    requireServices: (searchParams.get('services') ?? '')
      .split(',').map(s => s.trim()).filter(Boolean),
  }));
  const [draft, setDraft] = useState(filter);
  const [advFilters, setAdvFilters] = useState<FilterExpr[]>(
    () => decodeFilters(searchParams.get('filters')));
  // User-selected attribute columns shown in the list view. Comma-
  // separated in the URL so a saved link / bookmark restores the
  // exact column set. Bounded to 8 server-side.
  const [extraCols, setExtraCols] = useState<string[]>(
    () => (searchParams.get('cols') ?? '').split(',').map(s => s.trim()).filter(Boolean));
  const [data, setData] = useState<TracesResponse | null | undefined>(undefined);
  const [agg, setAgg] = useState<AggregateRow[] | null | undefined>(undefined);

  // The v0.5.64 lazy-load gate was removed in v0.5.72 because
  // operators wanted /traces to look like every other APM —
  // landing immediately on a populated first page, not on a
  // "click to load" placeholder. The MV fast-paths added in
  // v0.5.46 / v0.5.52 + the 30s bucketed cache mean the
  // unfiltered default-window query lands sub-second on
  // billion-span installs.

  // ── State → URL ────────────────────────────────────────────────────────────
  // Mirror everything that affects the rendered list to the URL so the
  // browser back button restores the same filters / sort / page after a
  // trip into /trace/{id}. Uses replaceState so we don't pollute history.
  useEffect(() => {
    const qs = buildQuery([
      ['range',    encodeRange(range)],
      // v0.7.37 — `list` is the default tab now; only emit ?view= when the
      // user switched to aggregate / shapes.
      ['view',     view !== 'list' ? view : ''],
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
      // rootOnly default flipped to ON in v0.2.72 — only emit
      // ?rootOnly=false when the operator explicitly unticks.
      ['rootOnly', filter.rootOnly ? '' : 'false'],
      ['services', filter.requireServices.join(',')],
      ['filters',  encodeFilters(advFilters)],
      ['cols',     extraCols.join(',')],
    ]);
    const next = qs ? `?${qs}` : '';
    if (typeof window !== 'undefined' && next !== window.location.search) {
      navigate(`/traces${next}`, { preventScrollReset: true, replace: true });
    }
  }, [range, view, sort, order, page, groupBy, groupAttr, aggSort, aggOrder, filter, advFilters, navigate]);

  // Autocomplete option lists
  // v0.5.198 — removed the eager api.services() + api.operations()
  // catalogue loads here. FilterBuilder now fetches per-key values
  // server-side via /api/attribute-values?q= (debounced) and the
  // top-level ServicePicker / OperationPicker pickers already
  // server-search. The seed arrays are dead weight at billion-span
  // scale and were the page's biggest TTFI cost on installs with
  // 10k+ services.

  // Total-count opt-in. Off by default for speed at scale (full DISTINCT
  // over a multi-billion-span table can take 10s+); the user can flip it
  // on with the "Show total" affordance in the pager.
  const [showTotal, setShowTotal] = useState(false);

  // ── List fetch ─────────────────────────────────────────────────────────────
  useEffect(() => {
    if (view !== 'list') return;
    setData(undefined);
    const tid = filter.traceId.trim().toLowerCase();
    const useTimeRange = tid.length === 0;
    const { from, to } = useTimeRange ? timeRangeToNs(range) : { from: undefined, to: undefined };
    api.traces({
      limit: 50, offset: page * 50, from, to,
      sort, order,
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
      // "exact" only when the user explicitly asked. Pinned trace IDs
      // skip the toggle — count of 1 is implicit.
      count: showTotal && !tid ? 'exact' : 'skip',
    }).then(setData).catch(() => setData(null));
  }, [view, range, sort, order, page, filter, advFilters, extraCols, showTotal]);

  // ── Aggregate fetch ────────────────────────────────────────────────────────
  useEffect(() => {
    if (view !== 'aggregate') return;
    setAgg(undefined);
    const { from, to } = timeRangeToNs(range);
    // groupBy = 'attr' is the special "use the custom attribute
    // key" sentinel — we collapse it back to 'operation' on the
    // server (default) and pass groupAttr instead, which takes
    // precedence in the backend.
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
  }, [view, range, groupBy, groupAttr, aggSort, aggOrder, filter, advFilters]);

  // apply commits the draft as the live filter. When called
  // with `overrideService` the picked value overrides whatever
  // draft.service currently holds — sidesteps the setState
  // race when ServicePicker auto-commits after a datalist
  // pick (its onChange + onEnter fire in adjacent microtasks
  // before React flushes the draft update).
  const apply = (overrideService?: string) => {
    const tid = draft.traceId.trim().toLowerCase();
    if (/^[0-9a-f]{32}$/.test(tid)) { navigate(`/trace?id=${tid}`); return; }
    const next = overrideService != null
      ? { ...draft, service: overrideService }
      : draft;
    setPage(0);
    if (overrideService != null) setDraft(next);
    setFilter(next);
  };
  // v0.5.442 — auto-apply 250ms after the last draft edit. Operator
  // mental model is "type 'java' and see service=java traces"
  // (Datadog/Honeycomb shape). Before this, the user had to press
  // Enter or click Search and novices stopped at "I typed and
  // nothing happened". 250ms keeps toggles feeling immediate while
  // still coalescing keystrokes during typing. The Search button
  // stays as an explicit hemen-fetch affordance.
  useEffect(() => {
    if (JSON.stringify(draft) === JSON.stringify(filter)) return;
    const t = setTimeout(() => apply(), 250);
    return () => clearTimeout(t);
    // apply reads draft via closure; including it in deps would
    // recreate the timer every render and prevent it from firing.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draft, filter]);
  const reset = () => {
    const empty = {
      service: '', search: '', traceId: '', minMs: '', maxMs: '',
      hasError: false, rootOnly: true, requireServices: [] as string[],
    };
    setDraft(empty); setFilter(empty); setPage(0);
  };
  const toggleSort = (col: SortColumn) => {
    if (sort === col) setOrder(order === 'desc' ? 'asc' : 'desc');
    else { setSort(col); setOrder(col === 'service' || col === 'operation' ? 'asc' : 'desc'); }
    setPage(0);
  };
  const toggleAggSort = (col: AggSort) => {
    if (aggSort === col) setAggOrder(aggOrder === 'desc' ? 'asc' : 'desc');
    else { setAggSort(col); setAggOrder(AGG_NATURAL[col]); }
  };

  const traces = data?.traces ?? [];
  const total = data?.total;             // undefined when count was skipped
  const hasMore = data?.hasMore ?? false;

  // j/k row navigation — Enter / o opens the trace detail
  // page. Only registers when we're in `view === 'list'`
  // since aggregate view groups rows differently.
  const tableNav = useTableNav<typeof traces[number]>(
    view === 'list' ? traces : [],
    {
      pageId: 'traces-list',
      onOpen: (t) => navigate(`/trace?id=${t.traceId}`),
    },
  );

  // Per-session hover-prefetch dedupe. Trace span data is
  // immutable once stored, so a hit during one operator
  // session never goes stale; once a trace has been warmed
  // we don't refire on every mouseenter. /api/traces/{id} is
  // server-cached 5 min so the warm path is essentially free
  // once the first hover lands.
  const prefetchedTraces = useRef<Set<string>>(new Set());
  const prefetchTrace = (id: string) => {
    if (prefetchedTraces.current.has(id)) return;
    prefetchedTraces.current.add(id);
    // Fire-and-forget — response just warms the cache.
    api.trace(id).catch(() => {});
  };

  // Build a DSL string the histogram can use to scope its volume +
  // error-rate query to the same predicate the table is showing. We
  // only fold in the *cheap* filters (service + hasError); free-text
  // search and ms-range filters need a full traces fetch and aren't
  // useful in a histogram anyway.
  const histogramDSL = (() => {
    const lines: string[] = [];
    if (filter.service)  lines.push(`service.name = "${filter.service}"`);
    if (filter.hasError) lines.push(`status_code = "error"`);
    return lines.join('\n');
  })();
  const histogramFilters = advFilters.length ? JSON.stringify(advFilters) : undefined;

  // v0.5.297 — memoise the resolved range tuple so the CSV
  // export link below doesn't reinvoke timeRangeToNs (and
  // therefore Date.now()) on every render. Stable URL =
  // stable <a href>, no flicker.
  const exportRangeNs = useMemo(() => timeRangeToNs(range), [range]);

  return (
    <>
      <Topbar title="Traces" range={range} onRangeChange={setRange} />
      <div id="content">
        <SavedViewsBar page="traces" />
        {/* Span-volume histogram — Datadog/Honeycomb-style stacked
            bars (ok in slate, errors in red) with total / errors /
            error-rate KPI tiles on the right. Reflects the current
            service + advanced-filter scope but ignores free-text
            search and ms-range filters. */}
        {/* v0.5.322 — drag-select on the histogram narrows the
            page TimeRange to a custom (from, to). Same pattern
            as ServiceCharts onZoom in /service detail. */}
        <TraceVolumeHistogram range={range} dsl={histogramDSL} filters={histogramFilters}
          search={filter.search?.trim() || undefined}
          onZoom={(fromUnixSec, toUnixSec) => {
            setRange({
              preset: 'custom',
              fromMs: Math.round(fromUnixSec * 1000),
              toMs:   Math.round(toUnixSec   * 1000),
            });
            setPage(0);
          }} />

        {/* View toggle + dedicated Trace ID lookup on the far right */}
        <div className="controls" style={{ marginBottom: 8, alignItems: 'center' }}>
          <div className="segmented">
            <button onClick={() => setView('list')}
              className={view === 'list' ? 'active' : ''}>
              Traces
            </button>
            <button onClick={() => setView('aggregate')}
              className={view === 'aggregate' ? 'active' : ''}>
              Aggregated
            </button>
            <button onClick={() => setView('shapes')}
              className={view === 'shapes' ? 'active' : ''}
              title="Cluster traces by their (service, operation) signature — find dominant call patterns at a glance">
              Shapes
            </button>
          </div>
          {view === 'aggregate' && (
            <>
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>Group by:</span>
              <select value={groupBy} onChange={e => setGroupBy(e.target.value as GroupBy)}>
                {GROUP_OPTIONS.map(o => (
                  <option key={o.value} value={o.value}>{o.label}</option>
                ))}
              </select>
              {groupBy === 'attr' && (
                <input placeholder="attribute key (e.g. user.id)"
                  value={groupAttr}
                  onChange={e => setGroupAttr(e.target.value)}
                  onKeyDown={e => { if (e.key === 'Enter') (e.target as HTMLInputElement).blur(); }}
                  style={{ width: 200 }} />
              )}
            </>
          )}

          {/* Dedicated trace-id lookup — pinned right, visually separate */}
          <div className="trace-lookup" style={{ marginLeft: 'auto' }}>
            <span className="tl-icon" aria-hidden><IconSearch size={14} /></span>
            <input placeholder="Trace ID (full or prefix)…"
              value={draft.traceId}
              onChange={e => setDraft({ ...draft, traceId: e.target.value })}
              onKeyDown={e => e.key === 'Enter' && apply()} />
            {draft.traceId && (
              <button className="tl-clear" type="button" title="Clear"
                onClick={() => { setDraft({ ...draft, traceId: '' });
                                 setFilter({ ...filter, traceId: '' }); }}>✕</button>
            )}
            <button className="tl-go" type="button" onClick={() => apply()}>Go</button>
          </div>
        </div>

        {/* Filters (shared between views) */}
        {/* data-shortcut-search marks the Service picker as the
            `/`-key target on this page (v0.5.454); without it,
            the global shortcut picks the first DOM input which
            is the right-pinned Trace ID lookup. */}
        <div className="controls" data-shortcut-search>
          <ServicePicker value={draft.service} onChange={v => setDraft({ ...draft, service: v })}
            placeholder="Service…" width={170}
            onEnter={(v) => apply(v)} />
          <OperationPicker service={draft.service}
            value={draft.search} onChange={v => setDraft({ ...draft, search: v })}
            placeholder="Operation…" width={240} onEnter={() => apply()} />
          <input placeholder="Min ms" value={draft.minMs} onChange={e => setDraft({ ...draft, minMs: e.target.value })}
            type="number" style={{ width: 72 }} />
          <input placeholder="Max ms" value={draft.maxMs} onChange={e => setDraft({ ...draft, maxMs: e.target.value })}
            type="number" style={{ width: 72 }} />
          <label style={{ display: 'flex', alignItems: 'center', gap: 5, color: 'var(--text2)', cursor: 'pointer' }}>
            <input type="checkbox" checked={draft.hasError} onChange={e => setDraft({ ...draft, hasError: e.target.checked })} />
            Errors only
          </label>
          <label
            style={{ display: 'flex', alignItems: 'center', gap: 5, color: 'var(--text2)', cursor: 'pointer' }}
            title="Hide partial traces — only show traces whose root span landed in storage">
            <input type="checkbox" checked={draft.rootOnly} onChange={e => setDraft({ ...draft, rootOnly: e.target.checked })} />
            Root traces
          </label>
          <button onClick={() => apply()}>Search</button>
          <button className="sec" onClick={reset}>Reset</button>
          {/* CSV export — same filter set as the live table.
              Pulls up to 10k rows (cap raised from the UI's
              50/page) so postmortem authors + auditors get a
              fuller slice without manually paginating. Build
              the query string from the committed `filter`
              state, not `draft`, so a half-typed change in
              the picker doesn't leak into the download. */}
          <a className="sec"
            href={`/api/traces/export.csv?${(() => {
              const { from, to } = exportRangeNs;
              const p = new URLSearchParams();
              p.set('from', String(from));
              p.set('to', String(to));
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
            download
            title="Download up to 10k matching traces as CSV (postmortem / audit use)"
            style={{
              padding: '5px 10px', fontSize: 12,
              textDecoration: 'none',
              border: '1px solid var(--border)', borderRadius: 4,
              color: 'var(--accent2)', background: 'var(--bg2)',
            }}>
            ⬇ CSV
          </a>
        </div>

        {/* Trace-topology AND requirement: every listed service
            must appear in the trace. Driven by the backtrace
            'Traces' drill-in (e.g. product-service × inventory-
            service) so the user lands on actual co-occurrence
            traces, not all traces emitted by either side. */}
        {filter.requireServices.length > 0 && (
          <div style={{
            display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap',
            padding: '8px 12px', marginBottom: 8,
            background: 'var(--bg2)', border: '1px solid var(--border)',
            borderRadius: 6, fontSize: 12,
          }}>
            <span style={{ color: 'var(--text2)', fontWeight: 600 }}>
              Trace must include:
            </span>
            {filter.requireServices.map((s) => (
              <span key={s} style={{
                display: 'inline-flex', alignItems: 'center', gap: 6,
                padding: '2px 8px', borderRadius: 4,
                background: 'var(--bg3)', border: '1px solid var(--border)',
                fontFamily: 'ui-monospace, monospace',
              }}>
                {s}
                <button type="button" title="Remove"
                  onClick={() => setFilter({
                    ...filter,
                    requireServices: filter.requireServices.filter(x => x !== s),
                  })}
                  style={{
                    background: 'transparent', border: 'none', color: 'var(--text3)',
                    cursor: 'pointer', padding: 0, fontSize: 12, lineHeight: 1,
                  }}>×</button>
              </span>
            ))}
            <button className="sec" type="button"
              onClick={() => setFilter({ ...filter, requireServices: [] })}
              style={{ marginLeft: 'auto', fontSize: 11 }}>
              Clear all
            </button>
          </div>
        )}

        {/* Advanced multi-dimension filter chips (Tempo / Dynatrace style) */}
        <FilterBuilder value={advFilters} onChange={setAdvFilters}
          suggestedValues={{
            // service.name + name keys removed (v0.5.198) — they now
            // server-search via /api/attribute-values?q=. Only
            // small enum-shaped sets stay seed values; FilterBuilder
            // merges these with server results.
            'kind': ['internal', 'server', 'client', 'producer', 'consumer'],
            'status_code': ['ok', 'error', 'unset'],
            'http.method': ['GET', 'POST', 'PUT', 'DELETE', 'PATCH'],
            'db.system': ['postgresql', 'mysql', 'redis', 'mongodb', 'elasticsearch'],
          }} />

        {/* List view */}
        {view === 'list' && data === undefined && <TableSkeleton rows={10} cols={7} />}
        {view === 'list' && data && traces.length === 0 && (
          <TracesEmpty
            service={filter.service}
            search={filter.search}
            range={range}
            onSwitchView={() => setView('aggregate')} />
        )}
        {view === 'list' && data && traces.length > 0 && (
          <>
            <div className="table-wrap">
              <table>
                <thead><tr>
                  <SortHeader col="time"      label="Time"      sort={sort} order={order} onSort={toggleSort} />
                  <SortHeader col="service"   label="Service"   sort={sort} order={order} onSort={toggleSort} />
                  <SortHeader col="operation" label="Operation" sort={sort} order={order} onSort={toggleSort} />
                  <SortHeader col="duration"  label="Duration"  sort={sort} order={order} onSort={toggleSort} />
                  <SortHeader col="spans"     label="Spans"     sort={sort} order={order} onSort={toggleSort} />
                  <SortHeader col="status"    label="Status"    sort={sort} order={order} onSort={toggleSort} />
                  {/* User-added attribute columns. Right-click /
                      ✕ button on each removes; "+ Column" header
                      adds a new one via the manager dropdown. */}
                  {extraCols.map(k => (
                    <th key={k} style={{ position: 'relative', whiteSpace: 'nowrap' }}>
                      <span style={{ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', fontSize: 11 }}>
                        {k}
                      </span>
                      <button type="button" title="Remove column"
                        onClick={() => setExtraCols(extraCols.filter(c => c !== k))}
                        style={{
                          marginLeft: 6, padding: '0 4px', fontSize: 10, lineHeight: 1,
                          background: 'transparent', border: 'none', color: 'var(--text3)',
                          cursor: 'pointer',
                        }}>×</button>
                    </th>
                  ))}
                  <th style={{ width: 1, whiteSpace: 'nowrap' }}>
                    <ColumnManager
                      cols={extraCols}
                      onAdd={k => { if (!extraCols.includes(k) && extraCols.length < 8) setExtraCols([...extraCols, k]); }} />
                  </th>
                </tr></thead>
                <tbody>
                  {traces.map((t, i) => (
                    <tr key={t.traceId}
                        data-row-idx={i}
                        className={tableNav.selected === i ? 'row-selected' : undefined}
                        onMouseEnter={() => {
                          tableNav.setSelected(i);
                          // Hover-prefetch the trace's spans —
                          // /api/traces/{id} is server-cached 5
                          // min, so by the time the operator
                          // clicks the row the trace detail
                          // page's mount fetch lands on a HIT
                          // (L1 first, Redis second). Dedupe via
                          // a ref-held Set so dragging across
                          // 50 rows fires 50 requests once each,
                          // not the same 50 re-fired.
                          prefetchTrace(t.traceId);
                        }}
                        {...rowClickHandlers(`/trace?id=${t.traceId}`,
                                             () => navigate(`/trace?id=${t.traceId}`))}>
                      <td className="mono">{tsDateTime(t.startTime)}</td>
                      <td><SvcBadge name={t.serviceName} /></td>
                      <td title={t.rootName}>{t.rootName || '—'}</td>
                      <td className="mono">{t.durationMs.toFixed(2)}ms</td>
                      <td>{t.spanCount}</td>
                      <td>{t.hasError ? <span className="badge b-err">ERROR</span> : <span className="badge b-ok">OK</span>}</td>
                      {extraCols.map(k => {
                        const v = t.extras?.[k] ?? '';
                        return (
                          <td key={k} className="mono" style={{ fontSize: 11, color: v ? 'var(--text2)' : 'var(--text3)', whiteSpace: 'nowrap', maxWidth: 280, overflow: 'hidden', textOverflow: 'ellipsis' }} title={v || ''}>
                            {v || '—'}
                          </td>
                        );
                      })}
                      {/* Filler cell aligning with the "+ Column"
                          header — keeps the table layout stable. */}
                      <td />
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <Pager
              page={page} pageSize={50}
              total={total}
              hasMore={hasMore}
              onPage={setPage}
              extras={
                <>
                  {total !== undefined ? (
                    <>{total.toLocaleString()} total</>
                  ) : (
                    <>
                      showing {traces.length}{hasMore ? '+' : ''}
                      {' · '}
                      <a href="#" onClick={e => { e.preventDefault(); setShowTotal(true); }}
                         title="Run an exact count(DISTINCT trace_id) — can be slow at scale">
                        Show total
                      </a>
                    </>
                  )}
                  {' · '}sorted by <b>{sort}</b> {order}
                </>
              } />
          </>
        )}

        {/* Aggregate view */}
        {view === 'aggregate' && agg === undefined && (
          <Spinner label="Aggregating traces by trace_id…" hint="Reads the trace_summary MV when the window is ≥5min, raw spans otherwise." />
        )}
        {view === 'aggregate' && agg && agg.length === 0 && (
          <Empty icon="∑" title="No groups in this window">
            <div style={{ marginTop: 6, color: 'var(--text2)' }}>
              The aggregate view needs at least one trace to group. Switch to
              the Traces tab to confirm there are matching rows, or widen the
              time range.
            </div>
          </Empty>
        )}
        {view === 'aggregate' && agg && agg.length > 0 && (
          <>
            <div className="table-wrap">
              <table>
                <thead><tr>
                  <AggHeader col="name"      label={groupLabel(groupBy, groupAttr)} sort={aggSort} order={aggOrder} onSort={toggleAggSort} />
                  {groupBy !== 'service' && <th>Service</th>}
                  <AggHeader col="count"     label="Traces"    sort={aggSort} order={aggOrder} onSort={toggleAggSort} align="right" />
                  <AggHeader col="perMin"    label="Per min"   sort={aggSort} order={aggOrder} onSort={toggleAggSort} align="right" />
                  <AggHeader col="errorRate" label="Error %"   sort={aggSort} order={aggOrder} onSort={toggleAggSort} align="right" />
                  <AggHeader col="avg"       label="Avg"       sort={aggSort} order={aggOrder} onSort={toggleAggSort} align="right" />
                  <AggHeader col="p50"       label="P50"       sort={aggSort} order={aggOrder} onSort={toggleAggSort} align="right" />
                  <AggHeader col="p95"       label="P95"       sort={aggSort} order={aggOrder} onSort={toggleAggSort} align="right" />
                  <AggHeader col="p99"       label="P99"       sort={aggSort} order={aggOrder} onSort={toggleAggSort} align="right" />
                  <AggHeader col="max"       label="Max"       sort={aggSort} order={aggOrder} onSort={toggleAggSort} align="right" />
                </tr></thead>
                <tbody>
                  {agg.map(a => {
                    const errCls = a.errorRate > 5 ? 'b-err' : a.errorRate > 0 ? 'b-warn' : 'b-ok';
                    // v0.6.39 — when aggregate MV (90d) holds
                    // traces that have aged out of raw spans
                    // (default 30d), surface the disparity with a
                    // chip so the operator knows clicking will
                    // reach fewer rows than the count suggests.
                    // missingRaw > 0 means: "of these, only
                    // <withRawAvailable> still have span detail".
                    const totalForRow = a.traceCount;
                    const drillable = a.withRawAvailable ?? a.traceCount;
                    const missingRaw = totalForRow - drillable;
                    // Click row → drill into List view. For
                    // operation/service rows we narrow by the
                    // matching field; for the other dimensions we
                    // narrow by service only (we don't have a
                    // first-class span-attribute filter here yet).
                    const onClick = () => {
                      if (groupBy === 'service') {
                        setFilter({ ...filter, service: a.groupKey });
                        setDraft({ ...draft, service: a.groupKey });
                      } else if (groupBy === 'operation') {
                        setFilter({ ...filter, search: a.groupKey, service: a.groupExtra ?? filter.service });
                        setDraft({ ...draft, search: a.groupKey, service: a.groupExtra ?? draft.service });
                      } else if (a.groupExtra) {
                        setFilter({ ...filter, service: a.groupExtra });
                        setDraft({ ...draft, service: a.groupExtra });
                      }
                      setView('list');
                      setPage(0);
                    };
                    return (
                      <tr key={`${a.groupKey}|${a.groupExtra}`} onClick={onClick}>
                        <td><b>{a.groupKey || '—'}</b></td>
                        {groupBy !== 'service' && <td><SvcBadge name={a.groupExtra ?? ''} /></td>}
                        <td className="mono" style={{ textAlign: 'right' }}>
                          {fmtNum(a.traceCount)}
                          {missingRaw > 0 && (
                            <span
                              className="badge b-warn"
                              style={{ marginLeft: 6, fontSize: 10 }}
                              title={`${fmtNum(drillable)} of ${fmtNum(totalForRow)} traces still have raw span data — older traces aged out of the raw retention window. Click to drill into the drillable subset.`}
                            >{fmtNum(drillable)} drillable</span>
                          )}
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }} title="Traces per minute">
                          {fmtPerMin(a.perMin)}
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          <span className={`badge ${errCls}`}>{a.errorRate.toFixed(2)}%</span>
                        </td>
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
        )}

        {/* v0.5.264 — Trace shape clustering view. Groups
            traces by their sorted-unique (service, operation)
            signature; surfaces dominant call-pattern cohorts.
            Sample-based at 10% so the underlying CH query
            stays under the 30s ceiling. */}
        {view === 'shapes' && (
          <TraceShapesView range={range} service={filter.service || undefined} />
        )}
      </div>
    </>
  );
}

function SortHeader({ col, label, sort, order, onSort }: {
  col: SortColumn; label: string; sort: SortColumn; order: SortOrder; onSort: (c: SortColumn) => void;
}) {
  const active = sort === col;
  return (
    <th className={`sortable${active ? ' sorted' : ''}`} onClick={() => onSort(col)}>
      {label}<span className="sort-arrow">{active ? (order === 'desc' ? '▼' : '▲') : '↕'}</span>
    </th>
  );
}

function AggHeader({ col, label, sort, order, onSort, align }: {
  col: AggSort; label: string; sort: AggSort; order: SortOrder;
  onSort: (c: AggSort) => void;
  align?: 'left' | 'right';
}) {
  const active = sort === col;
  return (
    <th className={`sortable${active ? ' sorted' : ''}`}
        onClick={() => onSort(col)}
        style={{ textAlign: align ?? 'left' }}>
      {label}<span className="sort-arrow">{active ? (order === 'desc' ? '▼' : '▲') : '↕'}</span>
    </th>
  );
}

function SvcBadge({ name }: { name: string }) {
  return (
    <span style={{ fontSize: 11, padding: '1px 6px', background: 'var(--bg3)', borderRadius: 3, fontFamily: 'monospace' }}>
      {name || 'unknown'}
    </span>
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

// v0.6.33 — smarter empty state on /traces. The naive "No traces
// found, try widening" message hid a more useful signal: when
// the service HAS spans in the MV (service_summary_5m) for this
// window, but the search-narrowed raw-spans scan came back zero,
// the operator is hitting either (a) data aged out of raw spans
// past the 30-day TTL while the 90-day MV still holds the rollup,
// or (b) a search that happens not to match any actual span.
//
// In case (a) the right move is to switch to Aggregate view —
// trace_summary_5m still has the data. In case (b) the
// suggestion to widen / drop search still stands.
//
// We tell them apart with one extra MV check via servicesPage,
// then surface the right hint. Same Empty component shape so
// the existing CSS doesn't change.
function TracesEmpty({ service, search, range, onSwitchView }: {
  service: string;
  search: string;
  range: TimeRange;
  onSwitchView: () => void;
}) {
  const [mvSpans, setMvSpans] = useState<number | null | undefined>(undefined);
  useEffect(() => {
    if (!service) { setMvSpans(null); return; }
    let cancelled = false;
    const r = timeRangeToNs(range);
    api.servicesPage(r, { name: service, limit: 1 })
      .then(d => {
        if (cancelled) return;
        const hit = (d?.services ?? []).find(s => s.name === service);
        setMvSpans(hit ? hit.spanCount : 0);
      })
      .catch(() => { if (!cancelled) setMvSpans(null); });
    return () => { cancelled = true; };
  }, [service, range]);
  const aged = service && search && (mvSpans ?? 0) > 0;
  return (
    <Empty icon="⋮" title="No traces found">
      <div style={{ marginTop: 6, color: 'var(--text2)' }}>
        {aged ? (
          <>
            <b style={{ color: 'var(--warn)' }}>{mvSpans!.toLocaleString()}</b> spans
            recorded for <code>{service}</code> in this window via the 5-min MV,
            but no raw spans match the search.
            {' '}This usually means the underlying span data has aged out past the
            raw-spans TTL (default 30d) while the MV (90d retention) still
            holds the rollup. The Aggregate view can show the historical
            shape; full-text search needs raw spans which are gone.{' '}
            <button
              type="button"
              onClick={onSwitchView}
              style={{
                marginLeft: 4, padding: '2px 10px',
                background: 'var(--accent2-bg, rgba(56,139,253,0.10))',
                border: '1px solid var(--accent2)',
                color: 'var(--accent2)', borderRadius: 4,
                cursor: 'pointer', fontSize: 12,
              }}>
              Switch to Aggregate view →
            </button>
          </>
        ) : (
          <>
            Try widening the time range, dropping the service or search filter,
            or unticking "Root traces". If even an unfiltered query is empty,
            check ingest health at <a href="/status" style={{ color: 'var(--accent2)' }}>/status</a>.
          </>
        )}
      </div>
    </Empty>
  );
}
