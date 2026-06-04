import { useEffect, useMemo, useRef, useState, Suspense, Fragment } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useTableNav } from '@/lib/useTableNav';
import { reconcileColOrder, moveColumn } from '@/lib/tableColumns';
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
import { Button } from '@/components/ui/Button';
import { Pager } from '@/components/Pager';
import { ColumnManager } from '@/components/ColumnManager';
import { api } from '@/lib/api';
import { tsDateTime, tsShort, timeRangeToNs, fmtNum, rowClickHandlers } from '@/lib/utils';
import { seriesColor } from '@/lib/chartFmt';
import { encodeRange, decodeRange, encodeFilters, decodeFilters, buildQuery } from '@/lib/urlState';
import type { TracesResponse, TraceRow, TimeRange, SortColumn, SortOrder, AggregateRow, FilterExpr, SpanRow } from '@/lib/types';

// Shared service-color map — same hash the topology graph + trace
// waterfall use (chartFmt.seriesColor), so a service keeps ONE colour
// across every surface. The badge / duration-bar / scatter-dot / mini-
// waterfall all read this so the operator's eye doesn't recalibrate.
const svcColor = (name: string) => seriesColor(name || 'unknown');
// color-mix a service colour into a faint badge background that stays
// legible in both themes (the prototype's `color-mix(... 14%, transparent)`).
const svcBadgeBg = (name: string) => `color-mix(in srgb, ${svcColor(name)} 16%, transparent)`;

// Duration → token colour. Errors always red; otherwise green/amber/red
// by absolute latency. Mirrors the prototype's durColor() but on tokens.
function durColor(ms: number, err: boolean): string {
  if (err) return 'var(--err)';
  if (ms > 1000) return 'var(--err)';
  if (ms > 400)  return 'var(--warn)';
  return 'var(--ok)';
}

// Attribute keys offered by the "+ Add filter" menu. Each appends a
// FilterExpr to advFilters (which already narrows server-side + rides
// the URL via encodeFilters), so the chip really filters and is shareable.
const QUICK_ATTR_KEYS = [
  'http.status_code', 'http.route', 'rpc.method', 'db.system',
  'banking.account_id', 'k8s.pod.name', 'cloud.region',
];
// Sensible default value when an attribute chip is first added; the
// operator edits it via the FilterBuilder chip below.
const QUICK_ATTR_DEFAULT: Record<string, string> = {
  'http.status_code': '500',
  'db.system': 'oracle',
};

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

// v0.7.47 — reorderable + resizable Traces list columns. The fixed columns and
// the operator's attribute columns share one ordered, resizable model; order +
// widths persist to localStorage. The pure order math lives in lib/tableColumns.
const TRACE_FIXED_COLS = ['time', 'service', 'operation', 'duration', 'spans', 'status'] as const;
const TRACE_COL_LABEL: Record<string, string> = {
  time: 'Time', service: 'Service', operation: 'Operation',
  duration: 'Duration', spans: 'Spans', status: 'Status',
};
const TRACE_COL_W: Record<string, number> = {
  time: 168, service: 130, operation: 280, duration: 104, spans: 72, status: 84,
};
const TRACE_ATTR_W = 160;
const traceColIsFixed = (id: string) => (TRACE_FIXED_COLS as readonly string[]).includes(id);
const traceColWidth = (id: string, widths: Record<string, number>) =>
  widths[id] ?? TRACE_COL_W[id] ?? TRACE_ATTR_W;

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
  // v0.7.47 — reorderable + resizable list columns. colOrder unifies the fixed
  // + attribute columns into one ordered list; colWidths holds per-column px.
  // Both persist to localStorage; reconcileColOrder keeps colOrder valid as
  // attribute columns are added/removed.
  const [colOrder, setColOrder] = useState<string[]>(() => {
    try { const s = localStorage.getItem('traces.colOrder'); if (s) return JSON.parse(s) as string[]; } catch { /* ignore */ }
    return [...TRACE_FIXED_COLS];
  });
  const [colWidths, setColWidths] = useState<Record<string, number>>(() => {
    try { const s = localStorage.getItem('traces.colWidths'); if (s) return JSON.parse(s) as Record<string, number>; } catch { /* ignore */ }
    return {};
  });
  const dragColRef = useRef<string | null>(null);

  // ── Power-explorer UI state (v0.7.112) ───────────────────────────────────────
  // Header viz mode: 'volume' (the stacked-bar + p99-line histogram) or
  // 'latency' (a duration-vs-time scatter built from the live trace rows).
  // Rides the URL so a shared link restores the chosen visualisation.
  const [viz, setViz] = useState<'volume' | 'latency'>(() =>
    searchParams.get('viz') === 'latency' ? 'latency' : 'volume');
  // Quick-filter chip: 'err' | 'slow' | a service name. Narrows the
  // CURRENTLY-fetched page client-side (cheap, instant) — the heavy
  // filters stay in the server query.
  const [quick, setQuick] = useState<string | null>(null);
  // Inline-expanded trace row → mini-waterfall preview.
  const [expanded, setExpanded] = useState<string | null>(null);
  // "+ Add filter" attribute menu open/close.
  const [addFilterOpen, setAddFilterOpen] = useState(false);
  const addFilterRef = useRef<HTMLDivElement>(null);
  // Scatter brush narrows the page range to a sub-window; we stash the
  // pre-brush range so the "clear selection" chip can restore it.
  const [brushPrev, setBrushPrev] = useState<TimeRange | null>(null);
  const [data, setData] = useState<TracesResponse | null | undefined>(undefined);
  const [agg, setAgg] = useState<AggregateRow[] | null | undefined>(undefined);
  // v0.7.90 — surface a failed/timed-out trace query instead of
  // swallowing it. Before, .catch set data=null which rendered a BLANK
  // list area (or, on a 200-empty, the misleading "No traces found"),
  // so an operator couldn't tell "no data" from "the query errored /
  // timed out". listErr holds the message; retryNonce re-fires the fetch.
  const [listErr, setListErr] = useState<string | null>(null);
  const [retryNonce, setRetryNonce] = useState(0);

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
      // v0.7.112 — header viz (volume default); only emit when latency.
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
  }, [range, view, viz, sort, order, page, groupBy, groupAttr, aggSort, aggOrder, filter, advFilters, navigate]);

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
    setListErr(null);
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
    }).then(d => { setData(d); }).catch((e: unknown) => {
      // Distinguish error from empty: keep data=null (so the skeleton
      // stops) but record the message so the UI shows a retryable error
      // panel, not "No traces found". A CH-heavy window can exceed
      // max_execution_time → 500; the operator must SEE that.
      setListErr(e instanceof Error ? e.message : 'Request failed');
      setData(null);
    });
  }, [view, range, sort, order, page, filter, advFilters, extraCols, showTotal, retryNonce]);

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
    setAdvFilters([]); setQuick(null); setExpanded(null);
  };
  const toggleSort = (col: SortColumn) => {
    if (sort === col) setOrder(order === 'desc' ? 'asc' : 'desc');
    else { setSort(col); setOrder(col === 'service' || col === 'operation' ? 'asc' : 'desc'); }
    setPage(0);
  };

  // v0.7.47 — keep colOrder valid as attribute columns come/go, and persist
  // order + widths to localStorage.
  useEffect(() => {
    setColOrder(o => {
      const r = reconcileColOrder(o, [...TRACE_FIXED_COLS], extraCols);
      return r.length === o.length && r.every((x, i) => x === o[i]) ? o : r;
    });
  }, [extraCols]);
  useEffect(() => {
    try { localStorage.setItem('traces.colOrder', JSON.stringify(colOrder)); } catch { /* ignore */ }
  }, [colOrder]);
  useEffect(() => {
    try { localStorage.setItem('traces.colWidths', JSON.stringify(colWidths)); } catch { /* ignore */ }
  }, [colWidths]);

  // Column resize — drag the right-edge handle (min 48px). Listeners on window
  // so the drag continues outside the th.
  const startColResize = (id: string, e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    const startX = e.clientX;
    const startW = traceColWidth(id, colWidths);
    const onMove = (ev: MouseEvent) => {
      const w = Math.max(48, Math.round(startW + (ev.clientX - startX)));
      setColWidths(prev => ({ ...prev, [id]: w }));
    };
    const onUp = () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
  };

  // Column reorder — drop a dragged column immediately before the target.
  const onColDrop = (targetId: string) => {
    const d = dragColRef.current;
    dragColRef.current = null;
    if (d) setColOrder(o => moveColumn(o, d, targetId));
  };

  // Close the "+ Add filter" attribute menu on an outside click.
  useEffect(() => {
    if (!addFilterOpen) return;
    const onDoc = (e: MouseEvent) => {
      if (!addFilterRef.current?.contains(e.target as Node)) setAddFilterOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [addFilterOpen]);

  // Append an attribute filter to advFilters. advFilters already drives
  // the server query AND rides the URL (encodeFilters), so the chip both
  // narrows results and survives a refresh / share.
  const addAttrFilter = (k: string) => {
    if (advFilters.some(f => f.k === k)) { setAddFilterOpen(false); return; }
    setAdvFilters([...advFilters, { k, op: '=', v: [QUICK_ATTR_DEFAULT[k] ?? ''] }]);
    setAddFilterOpen(false);
    setPage(0);
  };

  // Per-column cell content for a trace row (rendered in colOrder).
  // visibleMax scales the Duration bar to the slowest row currently shown.
  const renderTraceCell = (id: string, t: TraceRow, visibleMax: number) => {
    switch (id) {
      case 'time':      return <span className="mono">{tsDateTime(t.startTime)}</span>;
      case 'service':   return <SvcBadge name={t.serviceName} />;
      case 'operation': return <span title={t.rootName}>{t.rootName || '—'}</span>;
      case 'duration':  return <DurationBar ms={t.durationMs} err={t.hasError} max={visibleMax} />;
      case 'spans':     return <>{t.spanCount}</>;
      case 'status':    return t.hasError
        ? <span className="badge b-err">ERROR</span>
        : <span className="badge b-ok">OK</span>;
      default: {
        const v = t.extras?.[id] ?? '';
        return <span className="mono" style={{ fontSize: 11, color: v ? 'var(--text2)' : 'var(--text3)' }} title={v || ''}>{v || '—'}</span>;
      }
    }
  };
  const toggleAggSort = (col: AggSort) => {
    if (aggSort === col) setAggOrder(aggOrder === 'desc' ? 'asc' : 'desc');
    else { setAggSort(col); setAggOrder(AGG_NATURAL[col]); }
  };

  const traces = data?.traces ?? [];
  const total = data?.total;             // undefined when count was skipped
  const hasMore = data?.hasMore ?? false;

  // Quick-filter chips narrow the CURRENT page client-side (instant; the
  // server query already applied the heavy filters). 'err' → errors only,
  // 'slow' → >1s, anything else → that service name. Top services for the
  // per-service chips come from the live rows so the colours match.
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
    if (quick === 'err')  return traces.filter(t => t.hasError);
    if (quick === 'slow') return traces.filter(t => t.durationMs > 1000);
    return traces.filter(t => t.serviceName === quick);
  }, [traces, quick]);
  // Slowest visible row → scales every Duration bar.
  const visibleMax = useMemo(
    () => displayRows.reduce((m, t) => Math.max(m, t.durationMs), 0),
    [displayRows]);

  // j/k row navigation — Enter / o opens the trace detail
  // page. Only registers when we're in `view === 'list'`
  // since aggregate view groups rows differently. Bound to the
  // quick-filtered rows so j/k matches what's on screen.
  const tableNav = useTableNav<typeof displayRows[number]>(
    view === 'list' ? displayRows : [],
    {
      pageId: 'traces-list',
      onOpen: (t) => navigate(`/trace?id=${t.traceId}`),
    },
  );

  // A fresh page / new query invalidates the inline preview (the
  // expanded trace may no longer be present) and any stale quick chip
  // selection that filtered the old result set.
  useEffect(() => { setExpanded(null); }, [page, filter, advFilters, range, view]);
  useEffect(() => { if (quick) setQuick(null); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [filter, advFilters, range, view]);

  // openTrace centralises navigation so the scatter, the mini-waterfall
  // link, and j/k Enter all open the trace the same way.
  const openTrace = (t: TraceRow) => navigate(`/trace?id=${t.traceId}`);

  // Scatter drag-brush → narrow the page range to the brushed window.
  // Same mechanism as the histogram's onZoom: real server-side filtering,
  // already URL-reflected via encodeRange. We remember the prior range so
  // the "clear selection" chip restores it.
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
      <Topbar title="Traces"
        range={range}
        onRangeChange={(r) => { setBrushPrev(null); setRange(r); }} />
      <div id="content">
        <SavedViewsBar page="traces" />

        {/* Header visualisation — Volume / Latency toggle (v0.7.112).
            VOLUME reuses the production span-volume histogram (stacked
            ok+error bars + p99 line + TOTAL/ERRORS/ERROR RATE/P99 MAX
            stats). LATENCY is a duration-vs-time scatter built from the
            live trace rows: hover→tooltip, click→open, drag→brush a time
            window that narrows the table (clearable chip + URL state). */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 8 }}>
          <div className="segmented">
            <button className={viz === 'volume' ? 'active' : ''}
              onClick={() => setViz('volume')}>Volume</button>
            <button className={viz === 'latency' ? 'active' : ''}
              onClick={() => setViz('latency')}>Latency</button>
          </div>
          {brushPrev && (
            <button className="sec" style={{ fontSize: 11 }} onClick={clearBrush}
              title="Restore the previous time range">
              Clear selection ✕
            </button>
          )}
        </div>

        {viz === 'volume' ? (
          // v0.5.322 — drag-select on the histogram narrows the page
          // TimeRange to a custom (from, to). Same pattern as ServiceCharts
          // onZoom in /service detail; stash prior range for the chip.
          <TraceVolumeHistogram range={range} dsl={histogramDSL} filters={histogramFilters}
            search={filter.search?.trim() || undefined}
            onZoom={(fromUnixSec, toUnixSec) =>
              applyBrush(Math.round(fromUnixSec * 1000), Math.round(toUnixSec * 1000))} />
        ) : (
          <div style={{
            background: 'var(--bg2)', border: '1px solid var(--border)',
            borderRadius: 8, padding: 12, marginBottom: 10,
          }}>
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
            {view === 'list' && data === undefined ? (
              <div style={{ height: 168, display: 'flex', alignItems: 'center', justifyContent: 'center' }}><Spinner /></div>
            ) : (
              <LatencyScatter rows={displayRows} onOpen={openTrace}
                onBrush={applyBrush} />
            )}
          </div>
        )}

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

          {/* "+ Add filter" — append a span/resource attribute predicate.
              Each pick adds a FilterExpr to advFilters (server-narrowing +
              URL-reflected), surfaced below as an editable / removable chip
              in the FilterBuilder. */}
          <div ref={addFilterRef} style={{ position: 'relative' }}>
            <button type="button" className="sec"
              style={{ borderStyle: 'dashed', fontSize: 11.5 }}
              onClick={() => setAddFilterOpen(o => !o)}>+ Add filter</button>
            {addFilterOpen && (
              <div style={{
                position: 'absolute', top: 'calc(100% + 4px)', left: 0, zIndex: 60,
                minWidth: 210, background: 'var(--bg2)', border: '1px solid var(--border)',
                borderRadius: 6, boxShadow: '0 8px 24px rgba(0,0,0,0.30)', padding: 6,
              }}>
                <div style={{ fontSize: 10, color: 'var(--text3)', padding: '4px 8px', fontWeight: 700, letterSpacing: '0.4px' }}>
                  ATTRIBUTE
                </div>
                {QUICK_ATTR_KEYS.map(k => {
                  const already = advFilters.some(f => f.k === k);
                  return (
                    <div key={k}
                      onClick={() => !already && addAttrFilter(k)}
                      style={{
                        padding: '6px 8px', fontSize: 11.5, borderRadius: 4,
                        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                        cursor: already ? 'default' : 'pointer',
                        color: already ? 'var(--text3)' : 'var(--text)',
                      }}
                      onMouseEnter={e => { if (!already) e.currentTarget.style.background = 'var(--bg3)'; }}
                      onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}>
                      {k}{already ? ' ✓' : ''}
                    </div>
                  );
                })}
              </div>
            )}
          </div>

          {/* Row count — N of M on the current page (quick chip narrows N). */}
          {view === 'list' && data && (
            <span style={{ marginLeft: 'auto', fontSize: 11.5, color: 'var(--text3)' }}>
              {displayRows.length} of {traces.length} traces
            </span>
          )}
        </div>

        {/* Quick-filter chips — Errors / Slow / per-top-service (each
            carrying that service's colour from the shared map). Narrow the
            current page instantly; click again to clear. */}
        {view === 'list' && traces.length > 0 && (
          <div style={{ display: 'flex', gap: 7, flexWrap: 'wrap', alignItems: 'center', marginBottom: 8 }}>
            <QuickChip active={quick === 'err'} onClick={() => setQuick(quick === 'err' ? null : 'err')} tone="err">
              Errors {errCount}
            </QuickChip>
            <QuickChip active={quick === 'slow'} onClick={() => setQuick(quick === 'slow' ? null : 'slow')}>
              Slow &gt;1s
            </QuickChip>
            {topSvcs.map(s => (
              <QuickChip key={s} active={quick === s} dot={svcColor(s)}
                onClick={() => setQuick(quick === s ? null : s)}>
                {s}
              </QuickChip>
            ))}
          </div>
        )}

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
        {view === 'list' && listErr && (
          <Empty icon="⚠" title="Query failed">
            <p>The trace query errored or timed out. Try a narrower time range, then retry —
              the span-volume histogram above can still load even when the list query is too
              heavy.</p>
            <p className="mono" style={{ fontSize: 12, color: 'var(--text2)', wordBreak: 'break-word', margin: '8px 0' }}>{listErr}</p>
            <Button variant="secondary" size="sm" onClick={() => setRetryNonce(n => n + 1)}>↻ Retry</Button>
          </Empty>
        )}
        {view === 'list' && !listErr && data && traces.length === 0 && (
          <TracesEmpty
            service={filter.service}
            search={filter.search}
            range={range}
            onSwitchView={() => setView('aggregate')} />
        )}
        {view === 'list' && data && traces.length > 0 && (
          <>
            <div className="table-wrap">
              {/* v0.7.47 — table-layout:fixed + colgroup so per-column widths
                  (drag the right edge to resize) actually stick; columns also
                  reorder via the ⠿ grip. Order + widths persist to localStorage.
                  Fixed columns stay sortable (click the label); attribute
                  columns keep their ✕ remove. */}
              <table style={{ tableLayout: 'fixed', width: '100%' }}>
                <colgroup>
                  <col style={{ width: 30 }} />{/* expand chevron */}
                  {colOrder.map(id => <col key={id} style={{ width: traceColWidth(id, colWidths) }} />)}
                  <col style={{ width: 110 }} />{/* +Column gutter */}
                </colgroup>
                <thead><tr>
                  <th style={{ width: 30 }} />{/* expand chevron */}
                  {colOrder.map(id => {
                    const fixed = traceColIsFixed(id);
                    return (
                      <th key={id}
                          onDragOver={e => e.preventDefault()}
                          onDrop={e => { e.preventDefault(); onColDrop(id); }}
                          className={fixed ? `sortable${sort === id ? ' sorted' : ''}` : undefined}
                          style={{ position: 'relative', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                        <span draggable
                              onDragStart={() => { dragColRef.current = id; }}
                              onDragEnd={() => { dragColRef.current = null; }}
                              title="Drag to reorder"
                              style={{ cursor: 'grab', marginRight: 5, color: 'var(--text3)', userSelect: 'none' }}>⠿</span>
                        {fixed ? (
                          <span onClick={() => toggleSort(id as SortColumn)} style={{ cursor: 'pointer' }}>
                            {TRACE_COL_LABEL[id]}
                            <span className="sort-arrow">{sort === id ? (order === 'desc' ? '▼' : '▲') : '↕'}</span>
                          </span>
                        ) : (
                          <>
                            <span style={{ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', fontSize: 11 }}>{id}</span>
                            <button type="button" title="Remove column"
                              onClick={() => setExtraCols(extraCols.filter(c => c !== id))}
                              style={{ marginLeft: 6, padding: '0 4px', fontSize: 10, lineHeight: 1, background: 'transparent', border: 'none', color: 'var(--text3)', cursor: 'pointer' }}>×</button>
                          </>
                        )}
                        <span onMouseDown={e => startColResize(id, e)}
                              onClick={e => e.stopPropagation()}
                              title="Drag to resize"
                              style={{ position: 'absolute', top: 0, right: 0, width: 6, height: '100%', cursor: 'col-resize', userSelect: 'none' }} />
                      </th>
                    );
                  })}
                  <th style={{ whiteSpace: 'nowrap' }}>
                    <ColumnManager
                      cols={extraCols}
                      onAdd={k => { if (!extraCols.includes(k) && extraCols.length < 8) setExtraCols([...extraCols, k]); }} />
                  </th>
                </tr></thead>
                <tbody>
                  {displayRows.map((t, i) => {
                    const isOpen = expanded === t.traceId;
                    return (
                    <Fragment key={t.traceId}>
                    <tr
                        data-row-idx={i}
                        className={tableNav.selected === i ? 'row-selected' : undefined}
                        style={t.hasError ? { background: 'color-mix(in srgb, var(--err) 8%, transparent)' } : undefined}
                        onMouseEnter={() => {
                          tableNav.setSelected(i);
                          // Hover-prefetch the trace's spans (server-cached 5m)
                          // so the row click lands on a HIT. Deduped via a ref.
                          prefetchTrace(t.traceId);
                        }}
                        {...rowClickHandlers(`/trace?id=${t.traceId}`,
                                             () => navigate(`/trace?id=${t.traceId}`))}>
                      {/* Expand chevron — toggles the inline mini-waterfall
                          without navigating (full row click still opens the
                          trace detail). */}
                      <td onClick={(e) => { e.stopPropagation(); setExpanded(isOpen ? null : t.traceId); }}
                          style={{ textAlign: 'center', cursor: 'pointer', color: 'var(--text3)', userSelect: 'none' }}
                          title={isOpen ? 'Collapse preview' : 'Preview spans'}>
                        {isOpen ? '▾' : '▸'}
                      </td>
                      {colOrder.map(id => (
                        <td key={id}
                            style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                          {renderTraceCell(id, t, visibleMax)}
                        </td>
                      ))}
                      {/* Filler cell aligning with the "+ Column" header. */}
                      <td />
                    </tr>
                    {isOpen && (
                      <tr>
                        <td colSpan={colOrder.length + 2} style={{ padding: 0 }}>
                          <MiniWaterfall traceId={t.traceId}
                            fallbackService={t.serviceName}
                            onOpen={() => openTrace(t)} />
                        </td>
                      </tr>
                    )}
                    </Fragment>
                    );
                  })}
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

// SortHeader removed in v0.7.47 — the list view now renders its headers inline
// (reorder grip + resize handle + sort click). The aggregate view uses AggHeader.

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

// QuickChip — a clickable pill for the quick-filter row. `dot` paints a
// leading service-colour swatch; `tone="err"` reads the error count red.
function QuickChip({ active, onClick, children, dot, tone }: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
  dot?: string;
  tone?: 'err';
}) {
  return (
    <button type="button" onClick={onClick}
      style={{
        display: 'inline-flex', alignItems: 'center', gap: 6,
        padding: '3px 10px', borderRadius: 20, fontSize: 11.5, cursor: 'pointer',
        border: `1px solid ${active ? 'var(--accent)' : 'var(--border)'}`,
        background: active ? 'color-mix(in srgb, var(--accent) 14%, transparent)' : 'var(--bg2)',
        color: active ? 'var(--accent2)' : tone === 'err' ? 'var(--err)' : 'var(--text2)',
        fontWeight: 600, whiteSpace: 'nowrap',
      }}>
      {dot && <span style={{ width: 7, height: 7, borderRadius: 7, background: dot, flex: 'none' }} />}
      {children}
    </button>
  );
}

function SvcBadge({ name }: { name: string }) {
  // Service-coloured badge — same per-service hue as the topology graph
  // + trace waterfall (svcColor), so the operator's eye keeps a stable
  // colour→service mapping across every surface.
  return (
    <span style={{
      fontSize: 11, padding: '1px 7px', borderRadius: 4,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      background: svcBadgeBg(name), color: svcColor(name),
      whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
      maxWidth: '100%', display: 'inline-block', verticalAlign: 'bottom',
    }} title={name || 'unknown'}>
      {name || 'unknown'}
    </span>
  );
}

// DurationBar — value label + a track-bar scaled to the slowest visible
// row, coloured green/amber/red by latency (red if the trace errored).
// Reuses the .ov-minibar token track from globals.css.
function DurationBar({ ms, err, max }: { ms: number; err: boolean; max: number }) {
  const pct = max > 0 ? Math.max(2, Math.min(100, (ms / max) * 100)) : 0;
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
      <span className="mono" style={{ minWidth: 58, color: err ? 'var(--err)' : 'var(--text)' }}>
        {ms >= 1000 ? `${(ms / 1000).toFixed(2)}s` : `${ms.toFixed(2)}ms`}
      </span>
      <span className="ov-minibar" style={{ maxWidth: 110 }}>
        <i style={{ width: `${pct}%`, background: durColor(ms, err) }} />
      </span>
    </div>
  );
}

// LatencyScatter — duration-vs-time scatter built from the LIVE trace
// rows (no fabricated data). x = start time, y = duration (log scale),
// colour = status (ok = accent, error = red). Hover → tooltip; click →
// open the trace; drag → brush a time window that narrows the page range.
// Hand-rolled SVG on tokens (uPlot has no first-class per-point click /
// brush-to-range affordance; the prototype's scatter is SVG too).
function LatencyScatter({ rows, onOpen, onBrush }: {
  rows: TraceRow[];
  onOpen: (t: TraceRow) => void;
  onBrush: (fromMs: number, toMs: number) => void;
}) {
  const SW = 1000, SH = 168;
  const svgRef = useRef<SVGSVGElement>(null);
  const dragRef = useRef<number | null>(null);
  const [bx, setBx] = useState<{ a: number; b: number } | null>(null);
  const [hover, setHover] = useState<{ t: TraceRow; x: number; y: number } | null>(null);

  // Time + duration domains from the real rows. Guard the empty case.
  const { t0, t1, maxDur } = useMemo(() => {
    if (rows.length === 0) return { t0: 0, t1: 1, maxDur: 1 };
    let lo = Infinity, hi = -Infinity, md = 0;
    for (const r of rows) {
      const tms = r.startTime / 1e6;
      if (tms < lo) lo = tms;
      if (tms > hi) hi = tms;
      if (r.durationMs > md) md = r.durationMs;
    }
    if (hi === lo) hi = lo + 1;
    return { t0: lo, t1: hi, maxDur: Math.max(md, 1) };
  }, [rows]);

  const sx = (tms: number) => 10 + ((tms - t0) / (t1 - t0)) * (SW - 20);
  const sy = (d: number) => {
    const lv = Math.log10(d + 1), mx = Math.log10(maxDur + 1) || 1;
    return SH - 16 - (lv / mx) * (SH - 26);
  };
  const pxToView = (clientX: number) => {
    const r = svgRef.current?.getBoundingClientRect();
    if (!r) return 0;
    return ((clientX - r.left) / r.width) * SW;
  };

  const onDown = (e: React.MouseEvent) => {
    const x = pxToView(e.clientX);
    dragRef.current = x;
    setBx({ a: x, b: x });
  };
  const onMove = (e: React.MouseEvent) => {
    if (dragRef.current == null) return;
    setBx(b => (b ? { ...b, b: pxToView(e.clientX) } : b));
  };
  const onUp = (e: React.MouseEvent) => {
    if (dragRef.current == null) return;
    const a = dragRef.current, b = pxToView(e.clientX);
    dragRef.current = null;
    setBx(null);
    if (Math.abs(b - a) > 6) {
      // Convert the two view-space x's back to time (unix ms).
      const lo = t0 + ((Math.min(a, b) - 10) / (SW - 20)) * (t1 - t0);
      const hi = t0 + ((Math.max(a, b) - 10) / (SW - 20)) * (t1 - t0);
      onBrush(Math.round(lo), Math.round(hi));
    }
  };

  if (rows.length === 0) {
    return (
      <div style={{ height: SH, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text3)', fontSize: 12 }}>
        No traces in view to plot.
      </div>
    );
  }

  return (
    <div style={{ position: 'relative', height: SH }}>
      <svg ref={svgRef} viewBox={`0 0 ${SW} ${SH}`} width="100%" height={SH}
        preserveAspectRatio="none"
        style={{ display: 'block', cursor: 'crosshair' }}
        onMouseDown={onDown} onMouseMove={onMove} onMouseUp={onUp}
        onMouseLeave={(e) => { onUp(e); setHover(null); }}>
        {/* log-scale gridlines at 25/50/75/100% of max duration */}
        {[0.25, 0.5, 0.75, 1].map((f, i) => (
          <line key={i} x1="10" x2={SW - 10} y1={sy(maxDur * f)} y2={sy(maxDur * f)}
            stroke="var(--border)" strokeWidth="1" strokeDasharray="3 4" vectorEffect="non-scaling-stroke" />
        ))}
        {rows.map((r) => (
          <circle key={r.traceId}
            cx={sx(r.startTime / 1e6)} cy={sy(r.durationMs)}
            r={r.hasError ? 3.6 : 2.8}
            fill={r.hasError ? 'var(--err)' : 'var(--accent)'}
            opacity={r.hasError ? 0.95 : 0.55}
            style={{ cursor: 'pointer' }}
            onMouseEnter={() => setHover({ t: r, x: sx(r.startTime / 1e6), y: sy(r.durationMs) })}
            onMouseDown={(e) => e.stopPropagation()}
            onClick={(e) => { e.stopPropagation(); onOpen(r); }} />
        ))}
        {bx && (
          <rect x={Math.min(bx.a, bx.b)} y="0" width={Math.abs(bx.b - bx.a)} height={SH}
            fill="var(--accent)" opacity="0.12" stroke="var(--accent)" strokeWidth="1"
            vectorEffect="non-scaling-stroke" />
        )}
      </svg>
      {/* y-axis duration ticks (log scale) */}
      {[1000, 100, 10].filter(v => v <= maxDur * 1.4).map((v, i) => (
        <div key={i} className="mono" style={{
          position: 'absolute', right: 4, top: `${(sy(v) / SH) * 100}%`,
          transform: 'translateY(-50%)', fontSize: 9, color: 'var(--text3)',
          background: 'var(--bg2)', padding: '0 3px',
        }}>{v >= 1000 ? '1s' : `${v}ms`}</div>
      ))}
      {hover && (
        <div style={{
          position: 'absolute', pointerEvents: 'none', zIndex: 5,
          left: `min(${(hover.x / SW) * 100}%, calc(100% - 220px))`,
          top: `${(hover.y / SH) * 100}%`, transform: 'translate(10px, -50%)',
          background: 'var(--bg2)', border: '1px solid var(--border)',
          borderRadius: 4, padding: '6px 9px', fontSize: 11, color: 'var(--text)',
          whiteSpace: 'nowrap', boxShadow: '0 4px 14px rgba(0,0,0,0.25)',
        }}>
          <div style={{ fontWeight: 600, marginBottom: 2 }}>{hover.t.rootName || '—'}</div>
          <div style={{ color: 'var(--text2)' }}>{hover.t.serviceName}</div>
          <div className="mono">{hover.t.durationMs >= 1000 ? `${(hover.t.durationMs / 1000).toFixed(2)}s` : `${hover.t.durationMs.toFixed(2)}ms`} · {tsShort(hover.t.startTime)}</div>
          <div style={{ marginTop: 2 }}>
            <span className={`badge ${hover.t.hasError ? 'b-err' : 'b-ok'}`} style={{ fontSize: 9 }}>
              {hover.t.hasError ? 'ERROR' : 'OK'}
            </span>
          </div>
        </div>
      )}
    </div>
  );
}

// MiniWaterfall — inline preview of a trace's top spans on row-expand.
// Fetches the real /api/traces/{id} (server-cached 5m; usually already
// warmed by the hover-prefetch), then renders up to 8 spans as service-
// coloured bars positioned by start/duration within the root span.
function MiniWaterfall({ traceId, fallbackService, onOpen }: {
  traceId: string;
  fallbackService: string;
  onOpen: () => void;
}) {
  const [spans, setSpans] = useState<SpanRow[] | null | undefined>(undefined);
  useEffect(() => {
    let cancelled = false;
    setSpans(undefined);
    api.trace(traceId)
      .then(d => { if (!cancelled) setSpans(d?.spans ?? []); })
      .catch(() => { if (!cancelled) setSpans(null); });
    return () => { cancelled = true; };
  }, [traceId]);

  // Derive a compact view: sort by start, take the root window, show the
  // top-N longest spans (so the preview isn't dominated by tiny leaves).
  const view = useMemo(() => {
    if (!spans || spans.length === 0) return null;
    let t0 = Infinity, t1 = -Infinity;
    for (const s of spans) {
      if (s.startTime < t0) t0 = s.startTime;
      if (s.endTime > t1) t1 = s.endTime;
    }
    const total = Math.max(1, t1 - t0);
    const top = [...spans].sort((a, b) => b.durationMs - a.durationMs).slice(0, 8)
      .sort((a, b) => a.startTime - b.startTime);
    return { t0, total, top };
  }, [spans]);

  return (
    <div style={{ padding: '8px 14px 12px 40px', background: 'var(--bg1)' }}>
      {spans === undefined && (
        <div style={{ fontSize: 11, color: 'var(--text3)', padding: '4px 0' }}>Loading spans…</div>
      )}
      {spans === null && (
        <div style={{ fontSize: 11, color: 'var(--err)', padding: '4px 0' }}>Could not load spans for this trace.</div>
      )}
      {spans && spans.length === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text3)', padding: '4px 0' }}>
          No span detail (trace may have aged out of raw retention).
        </div>
      )}
      {view && view.top.map((s, i) => {
        const left = ((s.startTime - view.t0) / view.total) * 100;
        const width = Math.max(1.5, (Math.max(0, s.endTime - s.startTime) / view.total) * 100);
        const err = s.statusCode === 'error';
        return (
          <div key={s.spanId || i} style={{ display: 'grid', gridTemplateColumns: '220px 1fr', alignItems: 'center', height: 22, gap: 10 }}>
            <div style={{ display: 'flex', gap: 6, alignItems: 'center', overflow: 'hidden' }}>
              <span style={{ width: 6, height: 6, borderRadius: 6, background: svcColor(s.serviceName || fallbackService), flex: 'none' }} />
              <span className="mono" style={{ fontSize: 10.5, color: 'var(--text2)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                {(s.serviceName || fallbackService)} · {s.name}
              </span>
            </div>
            <div style={{ position: 'relative', height: 12 }}>
              <div style={{
                position: 'absolute', left: `${left}%`, width: `${width}%`, height: 12,
                borderRadius: 3, background: err ? 'var(--err)' : svcColor(s.serviceName || fallbackService),
                opacity: 0.85,
              }} title={`${s.serviceName} · ${s.name} · ${s.durationMs.toFixed(2)}ms`} />
            </div>
          </div>
        );
      })}
      <div style={{ marginTop: 6 }}>
        <a href={`/trace?id=${traceId}`}
          onClick={(e) => { e.preventDefault(); e.stopPropagation(); onOpen(); }}
          style={{ color: 'var(--accent2)', fontSize: 11.5, fontWeight: 600, textDecoration: 'none' }}>
          Open trace →
        </a>
      </div>
    </div>
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
