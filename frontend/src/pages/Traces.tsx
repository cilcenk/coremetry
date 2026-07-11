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
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { IconSearch } from '@/components/icons';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { OperationPicker } from '@/components/OperationPicker';
import { ServicePicker } from '@/components/ServicePicker';
import { FilterBuilder } from '@/components/FilterBuilder';
import { FilterGroupBuilder } from '@/components/FilterGroupBuilder';
import { RelationBuilder } from '@/components/RelationBuilder';
import { Button } from '@/components/ui/Button';
import { Pager } from '@/components/Pager';
import { ColumnManager } from '@/components/ColumnManager';
import { VirtualTable } from '@/components/ui/VirtualTable';
import { useDataTable } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { api } from '@/lib/api';
import { useUrlRange } from '@/lib/useUrlRange';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { tsDateTime, timeRangeToNs, fmtNum, fmtFixed } from '@/lib/utils';
import { encodeRange, encodeFilters, decodeFilters, encodeFilterGroup, decodeFilterGroup, buildQuery } from '@/lib/urlState';
import { parseHavingParam, encodeHavingParam, HAVING_METRICS, HAVING_OPS, type HavingRow, type HavingMetric, type HavingOp } from '@/lib/havingParam';
import type { TracesResponse, TraceRow, TimeRange, SortColumn, SortOrder, AggregateRow, FilterExpr, FilterGroup, SpanMetricSeries, RelationFilter, RelationKind } from '@/lib/types';

import { VolumeChart } from '@/components/traces/VolumeChart';
import { LatencyScatter } from '@/components/traces/LatencyScatter';
import { MiniWaterfall } from '@/components/traces/MiniWaterfall';
import { ShapesView } from '@/components/traces/ShapesView';
import { SvcBadge, DurationBar, QuickChip, svcColor, fmtDur } from '@/components/traces/shared';

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

type View = 'list' | 'aggregate' | 'shapes' | 'relations';
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
// Shared value-suggestion seeds for the advanced filter builders (flat +
// grouped). Hoisted so both render paths use the identical hints.
const FILTER_SUGGESTED_VALUES: Record<string, string[]> = {
  'kind': ['internal', 'server', 'client', 'producer', 'consumer'],
  'status_code': ['ok', 'error', 'unset'],
  'http.method': ['GET', 'POST', 'PUT', 'DELETE', 'PATCH'],
  'db.system': ['postgresql', 'mysql', 'redis', 'mongodb', 'elasticsearch'],
};
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

// HeaderStat — one mono stat in the header group right of the Volume|Latency
// toggle (TOTAL · ERRORS · ERR RATE · P99 MAX). Replaces the deleted standalone
// RED panel; `tone` colours the value (err → red, warn → amber).
function HeaderStat({ label, value, tone }: { label: string; value: string; tone?: 'err' | 'warn' }) {
  const color = tone === 'err' ? 'var(--err)' : tone === 'warn' ? 'var(--warn)' : 'var(--text)';
  return (
    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', minWidth: 44 }}>
      <span style={{
        fontSize: 14, fontWeight: 700, color, lineHeight: 1.1,
        fontVariantNumeric: 'tabular-nums', fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      }}>{value}</span>
      <span style={{ fontSize: 9, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.5px' }}>{label}</span>
    </div>
  );
}

function TracesPageInner() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();

  const [range, setRange] = useUrlRange('30m');
  // Global env filter (v0.8.383) — written by the Topbar EnvPicker,
  // consumed here as a first-class server param on the list/aggregate
  // fetches (+ volume strip + CSV). /traces is the Phase-1 consumer;
  // it applies to List + Aggregated — Relations/Shapes follow with
  // env-separation Phase 2+.
  const [env] = useUrlEnv();
  const [view, setView] = useState<View>(() => {
    const v = searchParams.get('view');
    return v === 'aggregate' || v === 'shapes' || v === 'relations' ? v : 'list';
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
  // v0.8.453 (B2-c) — genel HAVING koşulları. URL-first (?having=,
  // codec lib/havingParam.ts); post-aggregate olduğundan MV fast-path
  // hızını korur. Fetch + URL, 250ms debounce'lu kopyayı okur (sayfanın
  // draft auto-apply sözleşmesi) — değer alanına "1500" yazmak tuş
  // başına ayrı cache-key'li CH sorgusu ateşlemesin (review bulgusu;
  // operatör şartı: performans).
  const [having, setHaving] = useState<HavingRow[]>(() => parseHavingParam(searchParams.get('having')));
  const [debouncedHaving, setDebouncedHaving] = useState<HavingRow[]>(having);
  useEffect(() => {
    const t = setTimeout(() => setDebouncedHaving(having), 250);
    return () => clearTimeout(t);
  }, [having]);

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
  // v0.8.x gap-2 — grouped AND/OR builder. null = flat chip mode (the DEFAULT,
  // and what every existing saved view / shared URL decodes to). Non-null only
  // when the URL carries a real OR / nested `filterGroup`, or when the operator
  // toggles grouped mode on. When grouped, `advGroup` is the source of truth
  // and `filterGroup` supersedes `filters` server-side (flat-AND is byte-
  // identical, so the round-trip never changes a flat query's results).
  const [advGroup, setAdvGroup] = useState<FilterGroup | null>(() => decodeFilterGroup(searchParams.get('filterGroup')));
  // grouped mode is active whenever a FilterGroup is mounted. The encoded form
  // is '' for a flat-AND group, so a grouped session that the operator empties
  // back to flat-AND naturally falls back to the legacy `filters=` param.
  const grouped = advGroup !== null;
  const advGroupParam = useMemo(() => encodeFilterGroup(advGroup), [advGroup]);
  const [extraCols, setExtraCols] = useState<string[]>(
    () => (searchParams.get('cols') ?? '').split(',').map(s => s.trim()).filter(Boolean));

  // Relations view (Gap 3) — structural query state. URL-reflected so a
  // shared link reproduces the parent/child predicates + kind + direct flag.
  const [relation, setRelation] = useState<RelationFilter>(() => {
    const parseSet = (raw: string | null): FilterExpr[] => {
      if (!raw) return [];
      try { const v = JSON.parse(raw); return Array.isArray(v) ? v : []; } catch { return []; }
    };
    const k = searchParams.get('relKind');
    const kind: RelationKind = k === 'descendant-of' || k === 'sequence' ? k : 'child-of';
    return {
      parent: parseSet(searchParams.get('relParent')),
      child: parseSet(searchParams.get('relChild')),
      kind,
      direct: searchParams.get('relDirect') === 'true',
    };
  });
  // relNonce bumps on "Run" so the relation fetch re-fires even when the
  // predicate sets are unchanged (operator re-runs after a data window shift).
  const [relNonce, setRelNonce] = useState(0);
  const [relErr, setRelErr] = useState<string | null>(null);

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
  // v0.8.478 (perf dalga-3) — refetch'te ekran boşalmaz: önceki sonuç
  // solgunlaştırılarak ekranda kalır (keepPreviousData semantiği),
  // skeleton yalnız İLK yüklemede. dataRef effect'lere dep eklemeden
  // "elimde veri var mı" sorusunu cevaplar.
  const [refreshing, setRefreshing] = useState(false);
  const dataRef = useRef<TracesResponse | null | undefined>(undefined);
  dataRef.current = data;
  const [aggRefreshing, setAggRefreshing] = useState(false);
  const aggRef = useRef<AggregateRow[] | null | undefined>(undefined);
  const [agg, setAgg] = useState<AggregateRow[] | null | undefined>(undefined);
  aggRef.current = agg;
  const [listErr, setListErr] = useState<string | null>(null);
  const [retryNonce, setRetryNonce] = useState(0);
  const [showTotal, setShowTotal] = useState(false);

  // ── State → URL (replaceState; restores filters/sort/page on back). ──────────
  // `range` is included via encodeRange so the URL stays the single source of
  // truth even when useUrlRange's own writer and this effect both touch it.
  useEffect(() => {
    const qs = buildQuery([
      ['range',    encodeRange(range)],
      // Global env filter rides this page's URL like range does — this
      // effect rebuilds the whole query string, so omitting it here
      // would wipe the Topbar picker's ?env= on any local state write
      // (v0.8.383; the useUrlEnv localStorage mirror would survive, but
      // the URL must stay the shareable source of truth).
      ['env',      env],
      ['view',     view !== 'list' ? view : ''],
      ['viz',      viz !== 'volume' ? viz : ''],
      ['sort',     sort !== 'time' ? sort : ''],
      ['order',    order !== 'desc' ? order : ''],
      ['page',     page > 0 ? page : ''],
      ['groupBy',  view === 'aggregate' && groupBy !== 'operation' ? groupBy : ''],
      ['groupAttr', view === 'aggregate' && groupBy === 'attr' ? groupAttr : ''],
      ['aggSort',  view === 'aggregate' && aggSort !== 'count' ? aggSort : ''],
      ['aggOrder', view === 'aggregate' && aggOrder !== 'desc' ? aggOrder : ''],
      ['having',   view === 'aggregate' ? encodeHavingParam(debouncedHaving) : ''],
      ['service',  filter.service],
      ['search',   filter.search],
      ['traceId',  filter.traceId],
      ['minMs',    filter.minMs],
      ['maxMs',    filter.maxMs],
      ['hasError', filter.hasError ? 'true' : ''],
      ['rootOnly', filter.rootOnly ? '' : 'false'],
      ['services', filter.requireServices.join(',')],
      // Grouped (OR / nested) → filterGroup param; flat → legacy filters param.
      // Never both: a non-empty filterGroup suppresses filters so the URL has a
      // single source of truth and the backend's prefer-filterGroup rule is moot.
      ['filters',  advGroupParam ? '' : encodeFilters(advFilters)],
      ['filterGroup', advGroupParam],
      ['cols',     extraCols.join(',')],
      ['relKind',   view === 'relations' && relation.kind !== 'child-of' ? relation.kind : ''],
      ['relDirect', view === 'relations' && relation.direct ? 'true' : ''],
      ['relParent', view === 'relations' && relation.parent.length ? JSON.stringify(relation.parent) : ''],
      ['relChild',  view === 'relations' && relation.child.length ? JSON.stringify(relation.child) : ''],
    ]);
    const target = qs ? `?${qs}` : '';
    if (typeof window !== 'undefined' && target !== window.location.search) {
      navigate(`/traces${target}`, { preventScrollReset: true, replace: true });
    }
  }, [range, env, view, viz, sort, order, page, groupBy, groupAttr, aggSort, aggOrder, debouncedHaving, filter, advFilters, advGroupParam, extraCols, relation, navigate]);

  // ── List fetch ───────────────────────────────────────────────────────────
  const listRangeNs = useMemo(() => timeRangeToNs(range), [range]);
  useEffect(() => {
    if (view !== 'list') return;
    // Önceki sayfa/sıralama/filtre sonucu ekranda kalır; yalnız ilk
    // yüklemede skeleton (v0.8.478).
    if (dataRef.current && dataRef.current.traces?.length) {
      setRefreshing(true);
    } else {
      setData(undefined);
    }
    setListErr(null);
    // v0.8.300 (quality bar S3) — stale-overwrite guard, same pattern as the
    // volume-strip effect below.
    let cancelled = false;
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
      // Global Topbar env filter (v0.8.383) — first-class param so it
      // composes with filters AND filterGroup server-side.
      env: env || undefined,
      services: filter.requireServices.length ? filter.requireServices : undefined,
      // Grouped builder supersedes the flat filters when an OR/nested group is
      // active; flat-AND encodes to '' so the legacy filters path stays in use.
      filterGroup: advGroupParam || undefined,
      filters: advGroupParam ? undefined : (advFilters.length ? JSON.stringify(advFilters) : undefined),
      extraAttrs: extraCols.length ? extraCols.join(',') : undefined,
      count: showTotal && !tid ? 'exact' : 'skip',
    }).then(d => { if (!cancelled) { setData(d); setRefreshing(false); } }).catch((e: unknown) => {
      if (cancelled) return;
      setListErr(e instanceof Error ? e.message : 'Request failed');
      setData(null);
      setRefreshing(false);
    });
    return () => { cancelled = true; };
  }, [view, listRangeNs, sort, order, page, filter, env, advFilters, advGroupParam, extraCols, showTotal, retryNonce]);

  // ── Relations fetch (Gap 3) ────────────────────────────────────────────────
  // Structural self-join over raw spans. Fires only in relations view, and
  // only on an explicit Run (relNonce) or a range/sort change — NOT on every
  // predicate keystroke (the self-join is the codebase's most expensive read;
  // we never fire it implicitly). Result rows land in the SAME `data` state
  // the list table renders, so the result list is byte-identical to List view.
  const relRangeNs = useMemo(() => timeRangeToNs(range), [range]);
  useEffect(() => {
    if (view !== 'relations') return;
    // Nothing to run until the operator has entered at least one predicate.
    if (relation.parent.length === 0 && relation.child.length === 0) {
      setData(null);
      setRelErr(null);
      return;
    }
    if (dataRef.current && dataRef.current.traces?.length) {
      setRefreshing(true);
    } else {
      setData(undefined);
    }
    setRelErr(null);
    const { from, to } = relRangeNs;
    let cancelled = false;
    api.tracesByRelation({
      parent: relation.parent,
      child: relation.child,
      kind: relation.kind,
      direct: relation.direct,
      from, to, limit: 50, sort, order,
    }).then(res => {
      if (cancelled) return;
      // Adapt RelationResponse → TracesResponse so the shared list render
      // (gated on view === 'list' || 'relations') consumes it unchanged.
      setData({ traces: res.traces ?? [], hasMore: res.hasMore });
    }).catch((e: unknown) => {
      if (cancelled) return;
      setRelErr(e instanceof Error ? e.message : 'Relation query failed');
      setData(null);
    });
    return () => { cancelled = true; };
  // relation.parent/child are intentionally NOT deps — the query only re-runs
  // on explicit Run (relNonce) or range/sort change, never per keystroke.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [view, relRangeNs, sort, order, relation.kind, relation.direct, relNonce]);

  // v0.8.72 — TRUE span volume over the selected window (not the 50-row table
  // page). Aggregated count/errors/p99 per ~30 buckets, mirroring the table's
  // filter (service→dsl, search, advFilters), so the header chart + the
  // TOTAL/ERRORS/P99 stats reflect REAL traffic. The table still
  // carries the drill-in sample.
  const [volSeries, setVolSeries] = useState<{
    count: SpanMetricSeries[] | null;
    errors: SpanMetricSeries[] | null;
    p50: SpanMetricSeries[] | null;
  } | null>(null);
  useEffect(() => {
    if (view !== 'list') return;
    const { from, to } = listRangeNs;
    const windowSec = Math.max(60, Math.round((to - from) / 1e9));
    let step = Math.round(windowSec / 30);
    if (step < 1) step = 1;
    if (step > 300) step = 300;
    // The header volume chart rides /api/spans/metric, which is a flat-filters
    // surface (filterGroup is a /traces + /aggregate + /facets capability in
    // v0.8.x gap-2 — spanMetric isn't wired for it). When a grouped OR/nested
    // filter is active we therefore omit the flat filters here rather than send
    // a misleading partial predicate; the table + aggregate below still apply
    // the full group. The chart reflects the service/search context only.
    // v0.8.383 — the env context ALWAYS rides the chart's flat filters
    // (spanMetric's filter compiler maps deployment.environment →
    // deploy_env): env is global context like service/search, not part
    // of the operator's ad-hoc predicate group, so it applies even in
    // grouped mode where the group itself is omitted (see above).
    const chartFilters: FilterExpr[] = (!grouped && advFilters.length) ? [...advFilters] : [];
    if (env) chartFilters.push({ k: 'deployment.environment', op: '=', v: [env] });
    const common = {
      from, to, step,
      search: filter.search || undefined,
      filters: chartFilters.length ? JSON.stringify(chartFilters) : undefined,
      dsl: filter.service ? `service.name = "${filter.service.replace(/"/g, '\\"')}"` : undefined,
    };
    let cancelled = false;
    Promise.all([
      api.spanMetric({ ...common, agg: 'count' }),
      api.spanMetric({ ...common, agg: 'errors' }),
      api.spanMetric({ ...common, agg: 'p50', field: 'duration_ms' }),
    ])
      .then(([count, errors, p50]) => { if (!cancelled) setVolSeries({ count, errors, p50 }); })
      .catch(() => { if (!cancelled) setVolSeries(null); });
    return () => { cancelled = true; };
  }, [view, listRangeNs, filter.service, filter.search, env, advFilters, grouped]);

  // ── Aggregate fetch ──────────────────────────────────────────────────────
  const aggRangeNs = useMemo(() => timeRangeToNs(range), [range]);
  useEffect(() => {
    if (view !== 'aggregate') return;
    if (aggRef.current && aggRef.current.length) {
      setAggRefreshing(true);
    } else {
      setAgg(undefined);
    }
    let cancelled = false; // v0.8.300 — stale-overwrite guard
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
      // Global Topbar env filter (v0.8.383).
      env: env || undefined,
      filterGroup: advGroupParam || undefined,
      filters: advGroupParam ? undefined : (advFilters.length ? JSON.stringify(advFilters) : undefined),
      having: debouncedHaving.length ? encodeHavingParam(debouncedHaving) : undefined,
    }).then(a => { if (!cancelled) { setAgg(a); setAggRefreshing(false); } })
      .catch(() => { if (!cancelled) { setAgg(null); setAggRefreshing(false); } });
    return () => { cancelled = true; };
  }, [view, aggRangeNs, groupBy, groupAttr, aggSort, aggOrder, debouncedHaving, filter, env, advFilters, advGroupParam]);

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
    setAdvFilters([]); setAdvGroup(null); setQuick(null); setExpanded(null);
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

  // Header RED stats over the live filtered rows (the stat group right of the
  // Volume|Latency toggle). Replaces the deleted standalone RED panel — the
  // filtered Rate/Errors/Duration numbers ride here + in the table.
  // Header TOTAL/ERRORS/ERR RATE/P99 MAX — derived from the TRUE-volume series
  // (whole window), so they describe real traffic rather than the 50-row page.
  const headerStats = useMemo(() => {
    const cPts = volSeries?.count?.[0]?.points ?? [];
    const eMap = new Map((volSeries?.errors?.[0]?.points ?? []).map(p => [p.time, p.value]));
    const pPts = volSeries?.p50?.[0]?.points ?? [];
    let total = 0, err = 0, p50Max = 0;
    for (const p of cPts) { total += p.value; err += eMap.get(p.time) ?? 0; }
    for (const p of pPts) if (p.value > p50Max) p50Max = p.value;
    return { total, err, errRate: total > 0 ? (err / total) * 100 : 0, p50Max };
  }, [volSeries]);

  // Reset transient state on a new query / page.
  useEffect(() => { setExpanded(null); }, [page, filter, advFilters, advGroupParam, range, view]);
  useEffect(() => { if (quick) setQuick(null); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [filter, advFilters, advGroupParam, range, view]);

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
            <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 8, flexWrap: 'wrap' }}>
              <div className="segmented">
                <button className={viz === 'volume' ? 'active' : ''} onClick={() => setViz('volume')}>Volume</button>
                <button className={viz === 'latency' ? 'active' : ''} onClick={() => setViz('latency')}>Latency</button>
              </div>
              <span style={{ fontSize: 11, color: 'var(--text2)', fontWeight: 700, letterSpacing: '0.5px', textTransform: 'uppercase' }}>
                {viz === 'volume' ? 'Span volume' : 'Latency distribution'}
              </span>
              {brushPrev && (
                <Button variant="secondary" size="sm" onClick={clearBrush} title="Restore the previous time range">
                  Clear selection ✕
                </Button>
              )}
              {/* RED stat group — mono, right-aligned, over the filtered rows. */}
              <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 18, flexWrap: 'wrap' }}>
                <HeaderStat label="TOTAL" value={fmtNum(headerStats.total)} />
                <HeaderStat label="ERRORS" value={fmtNum(headerStats.err)} tone={headerStats.err > 0 ? 'err' : undefined} />
                <HeaderStat label="ERR RATE" value={`${headerStats.errRate.toFixed(2)}%`} tone={headerStats.errRate > 0 ? 'err' : undefined} />
                <HeaderStat label="P50 MAX" value={headerStats.p50Max ? fmtDur(headerStats.p50Max) : '—'} tone="warn" />
              </div>
            </div>

            {data === undefined ? (
              <div style={{ background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 8, padding: 12, marginBottom: 10, height: 192, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
                <Spinner />
              </div>
            ) : viz === 'volume' ? (
              // slimmer + recedes — it's the brush/overview "tool", not the
              // headline chart; the RED strip below carries the filtered numbers.
              <VolumeChart count={volSeries?.count ?? null} errors={volSeries?.errors ?? null} p50={volSeries?.p50 ?? null} height={140} onBrush={applyBrush} />
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
            <button onClick={() => setView('relations')} className={view === 'relations' ? 'active' : ''}
              title="Structural query — find traces by span relationships: child-of / descendant-of / sequence (e.g. frontend → payment direct child)">
              Relations
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
              {/* v0.8.453 (B2-c) — genel HAVING: grup metriği eşiği.
                  Post-aggregate (MV fast-path hızını korur); koşullar
                  AND'lenir, URL'de ?having= taşınır. */}
              {having.map((h, i) => (
                <span key={i} style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                  <span style={{ color: 'var(--text3)', fontSize: 11, fontWeight: 700 }}>
                    {i === 0 ? 'HAVING' : 'AND'}
                  </span>
                  <select value={h.metric} style={{ fontSize: 12 }}
                    onChange={e => setHaving(p => p.map((x, j) =>
                      j === i ? { ...x, metric: e.target.value as HavingMetric } : x))}>
                    {HAVING_METRICS.map(m => <option key={m.value} value={m.value}>{m.label}</option>)}
                  </select>
                  <select value={h.op} style={{ fontSize: 12, width: 52 }}
                    onChange={e => setHaving(p => p.map((x, j) =>
                      j === i ? { ...x, op: e.target.value as HavingOp } : x))}>
                    {HAVING_OPS.map(o => <option key={o} value={o}>{o}</option>)}
                  </select>
                  <input type="number" value={Number.isFinite(h.value) ? h.value : 0}
                    onChange={e => setHaving(p => p.map((x, j) =>
                      j === i ? { ...x, value: Number(e.target.value) } : x))}
                    style={{ width: 76, fontSize: 12 }} />
                  <button className="sec" type="button" aria-label="Koşulu kaldır"
                    style={{ fontSize: 11, padding: '2px 6px' }}
                    onClick={() => setHaving(p => p.filter((_, j) => j !== i))}>✕</button>
                </span>
              ))}
              {having.length < 8 && (
                <button className="sec" type="button"
                  style={{ fontSize: 11, padding: '3px 8px' }}
                  title='Grup metriği eşiği ekle — ör. "Error % > 1 AND P95 ms > 500"'
                  onClick={() => setHaving(p => [...p, { metric: 'errorRate', op: '>', value: 1 }])}>
                  {having.length === 0 ? '＋ Having' : '＋'}
                </button>
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

        {/* Relations view — structural query builder replaces the flat filter
            row. Drives the bounded self-join (GET /api/traces/relations). */}
        {view === 'relations' && (
          <RelationBuilder
            value={relation}
            onChange={setRelation}
            onRun={() => setRelNonce(n => n + 1)}
            running={data === undefined}
          />
        )}

        {/* Filters — hidden in relations view (RelationBuilder owns the query). */}
        {view !== 'relations' && (
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
              if (env) p.set('env', env); // v0.8.383 — export matches the on-screen env filter
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
        )}

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

        {/* Advanced filters — flat chip row by default; the operator can switch
            to the grouped AND/OR builder for (A OR B) AND C queries (gap-2).
            flat→grouped seeds the group's top-level leaves from the current
            chips; grouped→flat flattens them back (OR / nested structure has no
            flat representation). The whole block is hidden in the relations
            view (gap-3), which has its own predicate builders. v0.8.x. */}
        {view !== 'relations' && (
          <>
          <div className="row gap-2" style={{ alignItems: 'center', justifyContent: 'flex-end', marginBottom: -4 }}>
            {!grouped ? (
              <Button variant="ghost" size="sm"
                title="Switch to the grouped AND/OR builder for (A OR B) AND C style queries"
                onClick={() => setAdvGroup({ join: 'AND', filters: advFilters })}>
                ⊞ Group filters (AND/OR)
              </Button>
            ) : (
              <Button variant="ghost" size="sm"
                title="Back to the flat filter chips (drops any OR / nested groups)"
                onClick={() => {
                  setAdvFilters((advGroup?.filters ?? []).filter(f => f.k && f.k.trim()));
                  setAdvGroup(null);
                }}>
                ⊟ Flatten to chips
              </Button>
            )}
          </div>
          {!grouped ? (
            <FilterBuilder value={advFilters} onChange={setAdvFilters}
              suggestedValues={FILTER_SUGGESTED_VALUES} />
          ) : (
            <FilterGroupBuilder value={advGroup ?? { join: 'AND', filters: [] }}
              onChange={setAdvGroup}
              suggestedValues={FILTER_SUGGESTED_VALUES} />
          )}
          </>
        )}

        {/* List + Relations views share the result table (Relations populates
            the same `data` state, so the render below is identical). */}
        {(view === 'list' || view === 'relations') && data === undefined && <TableSkeleton rows={10} cols={7} />}
        {view === 'list' && listErr && (
          <Empty icon="⚠" title="Query failed">
            <p>The trace query errored or timed out. Try a narrower time range, then retry.</p>
            <p className="mono" style={{ fontSize: 12, color: 'var(--text2)', wordBreak: 'break-word', margin: '8px 0' }}>{listErr}</p>
            <Button variant="secondary" size="sm" onClick={() => setRetryNonce(n => n + 1)}>↻ Retry</Button>
          </Empty>
        )}
        {view === 'relations' && relErr && (
          <Empty icon="⚠" title="Relation query failed">
            <p>The structural self-join errored or timed out. Try a narrower time range or fewer predicates, then re-run.</p>
            <p className="mono" style={{ fontSize: 12, color: 'var(--text2)', wordBreak: 'break-word', margin: '8px 0' }}>{relErr}</p>
            <Button variant="secondary" size="sm" onClick={() => setRelNonce(n => n + 1)}>↻ Retry</Button>
          </Empty>
        )}
        {view === 'relations' && !relErr && data === null && relation.parent.length === 0 && relation.child.length === 0 && (
          <Empty icon="⮑" title="Build a structural query">
            <p style={{ color: 'var(--text2)' }}>
              Add a predicate to the parent and/or child builder above, pick a relation
              kind (child-of / descendant-of / sequence), then Run. Example:
              parent <code>service.name = frontend</code>, child <code>service.name = payment</code>,
              kind <b>child of</b> finds traces where frontend directly calls payment.
            </p>
          </Empty>
        )}
        {view === 'relations' && !relErr && data && traces.length === 0 && (
          <Empty icon="∅" title="No traces match this relationship">
            <p style={{ color: 'var(--text2)' }}>
              No traces in this window where the parent/child predicates hold under
              the chosen relation. Widen the time range, relax a predicate, or switch
              to a looser kind (descendant-of / sequence).
            </p>
          </Empty>
        )}
        {view === 'list' && !listErr && data && traces.length === 0 && (
          <TracesEmpty service={filter.service} search={filter.search} range={range} onSwitchView={() => setView('aggregate')} />
        )}
        {(view === 'list' || view === 'relations') && data && traces.length > 0 && (
          <div style={{ opacity: refreshing ? 0.55 : 1, transition: 'opacity 120ms' }}
            aria-busy={refreshing}>
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
                  {/* v0.8.369 — Dynatrace-style honesty hint: non-time
                      sorts rank within the newest-N slice, not the
                      whole window. */}
                  {data?.rankedWithinRecent ? (
                    <span title={`For speed, ${sort} ranks the newest ${data.rankedWithinRecent.toLocaleString()} traces in the window — an older trace beyond that slice won't appear. Sort by time for the full window.`}
                      style={{ marginLeft: 6, color: 'var(--text3)' }}>
                      · ranked within newest {data.rankedWithinRecent.toLocaleString()}
                    </span>
                  ) : null}
                </>
              } />
          </div>
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
          <div style={{ opacity: aggRefreshing ? 0.55 : 1, transition: 'opacity 120ms' }} aria-busy={aggRefreshing}>
          <AggregateTable agg={agg} groupBy={groupBy} groupAttr={groupAttr}
            aggSort={aggSort} aggOrder={aggOrder} onSort={toggleAggSort}
            onDrill={(a) => {
              if (groupBy === 'service') { setFilter({ ...filter, service: a.groupKey }); setDraft({ ...draft, service: a.groupKey }); }
              else if (groupBy === 'operation') { setFilter({ ...filter, search: a.groupKey, service: a.groupExtra ?? filter.service }); setDraft({ ...draft, search: a.groupKey, service: a.groupExtra ?? draft.service }); }
              else if (a.groupExtra) { setFilter({ ...filter, service: a.groupExtra }); setDraft({ ...draft, service: a.groupExtra }); }
              setView('list'); setPage(0);
            }} />
          </div>
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
                  <td className="mono" style={{ textAlign: 'right' }}><span className={`badge ${errCls}`}>{fmtFixed(a.errorRate, 2)}%</span></td>
                  <td className="mono" style={{ textAlign: 'right' }}>{fmtFixed(a.avgMs, 1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{fmtFixed(a.p50Ms, 1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{fmtFixed(a.p95Ms, 1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{fmtFixed(a.p99Ms, 1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{fmtFixed(a.maxMs, 1)}ms</td>
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
          <>Try widening the time range, dropping the service or search filter, or unticking "Root traces". If even an unfiltered query is empty, check ingest health at <Link to="/system/stats" style={{ color: 'var(--accent2)' }}>system stats</Link>.</>
        )}
      </div>
    </Empty>
  );
}
