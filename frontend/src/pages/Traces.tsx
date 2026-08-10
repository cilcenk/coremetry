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
//   • Quick-filter chips (Errors / Slow>1s / per-top-service), the advanced
//     FilterBuilder ("+ Add filter" → attribute/op/value, with a grouped
//     AND/OR mode), "+ Column" via ColumnManager, full filter row.
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
import { Button } from '@/components/ui/Button';
import { Chip } from '@/components/ui/Chip';
import { Pager } from '@/components/Pager';
import { ColumnManager } from '@/components/ColumnManager';
import { stepForPoints, barPanelMaxDataPoints } from '@/lib/chartStep';
import { VirtualTable } from '@/components/ui/VirtualTable';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTable } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { formatSortParam } from '@/lib/dataTable';
import { type AggSort, toAggSort, decodeLegacyAggSort } from './traces/aggSort';
import { api, isCanceled } from '@/lib/api';
import { usePageZoomRange } from '@/lib/chart/usePageZoomRange';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { tsDateTime, tsLong, timeRangeToNs, fmtNum, fmtFixed } from '@/lib/utils';
import { alignTraceWindow } from '@/lib/traceWindow';
import { suggestAttrKey, type AttrKeySuggestion } from '@/lib/attrKeySuggest';
import { traceCountReasonHint } from '@/lib/traceCountReason';
import { lastReachablePage } from '@/lib/traceReach';
import type { TraceCountResponse } from '@/lib/types';
import { encodeRange, encodeFilters, decodeFilters, encodeFilterGroup, decodeFilterGroup, buildQuery } from '@/lib/urlState';
import { parseHavingParam, encodeHavingParam, HAVING_METRICS, HAVING_OPS, type HavingRow, type HavingMetric, type HavingOp } from '@/lib/havingParam';
import { upsertAttrFilter } from '@/lib/aggDrill';
import { mergeTraceExtras, missingExtraKeys } from '@/lib/traceExtrasMerge';
// v0.9.841 — kolon SIRASI ve varsayılan attr seti tek yerde, saf ve
// testli (traceColumns.ts). İkisi de karar; mekanik değil.
import { DEFAULT_TRACE_COLUMNS, traceColumnOrder } from '@/lib/traceColumns';
import { getRaw, setRaw, STORAGE_KEYS } from '@/lib/storage';
import type { TracesResponse, TraceRow, TimeRange, SortColumn, SortOrder, AggregateRow, FilterExpr, FilterGroup, SpanMetricSeries } from '@/lib/types';

import { VolumeChart } from '@/components/traces/VolumeChart';
import { LatencyScatter } from '@/components/traces/LatencyScatter';
import { MiniWaterfall } from '@/components/traces/MiniWaterfall';
import { ShapesView } from '@/components/traces/ShapesView';
import { SvcBadge, DurationBar, fmtDur } from '@/components/traces/shared';
import { PageControls } from '@/components/ui/PageControls';
import { QueryError } from '@/components/QueryError';

// v0.9.304 (operatör) — 'relations' kaldırıldı. Yapısal self-join
// sorgusu ham spans üzerinde koşuyordu, yani sayfadaki en pahalı okuma
// yoluydu ve kullanılmıyordu.
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

// v0.9.878 — AGG_NATURAL SİLİNDİ: doğal yön artık kolon tanımındaki
// `naturalDir` (name: 'asc', sayısal kolonlar varsayılan 'desc').

// Fixed list columns. The trace list is SERVER-paged (50/page), so per the
// useDataTable contract it keeps its SERVER sort (header click → server sort)
// and adopts only the resize half of the primitive. We give the data columns
// no `sortValue` (client-sorting a 50-row server page would scramble server
// order); the header click routes to the server sort below.
const COL_LABEL: Record<string, string> = {
  time: 'Time', service: 'Service', operation: 'Operation',
  duration: 'Duration', spans: 'Spans', status: 'Status',
};
// Default widths are tuned so the fixed columns PLUS two attribute columns
// fit a 1440px laptop without horizontal scroll (v0.9.243 — operator-reported:
// "columns don't fit, I always have to scroll right"). Budget at 1440px:
// ~220 sidebar + ~40 page padding leaves ~1180. Fixed 864 + 30 leading row-
// marker + 2×130 attrs = 1154. Every cell already ellipsises with a title
// tooltip (globals.css tbody td), so narrower never means "silently lost".
// Widths are user-resizable and persist per browser; these are only the
// starting points for an operator who has never dragged a column edge.
const COL_W: Record<string, number> = {
  time: 168, service: 130, operation: 260, duration: 150, spans: 72, status: 84,
};
const ATTR_W = 130;
const EXTRA_COLS_LS_KEY = 'traces-extra-cols';
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
function HeaderStat({ label, value, tone, title }: { label: string; value: string; tone?: 'err' | 'warn'; title?: string }) {
  const color = tone === 'err' ? 'var(--err)' : tone === 'warn' ? 'var(--warn)' : 'var(--text)';
  return (
    <div title={title} style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', minWidth: 44 }}>
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

  // v0.9.430 — zoom-yığını hook'u; sayfalama her zoom/geri adımında
  // sıfırlanır (onChange; setPage sonradan tanımlı — closure çağrı
  // anında değerlendirilir, TDZ yok).
  const { range, setRange, handleZoom, handleZoomReset, zoomDepth } = usePageZoomRange('30m', () => setPage(0));
  // Global env filter (v0.8.383) — written by the Topbar EnvPicker,
  // consumed here as a first-class server param on the list/aggregate
  // fetches (+ volume strip + CSV). /traces is the Phase-1 consumer;
  // it applies to List + Aggregated — Relations/Shapes follow with
  // env-separation Phase 2+.
  const [env] = useUrlEnv();
  const [view, setView] = useState<View>(() => {
    const v = searchParams.get('view');
    // Kayıtlı bir görünüm ?view=relations taşıyorsa listeye düşer —
    // ölü bir sekmeye değil.
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
    // v0.9.78 — Operator-reported: default OFF. A root-only default hid every
    // non-root operation pick (DB call, internal-service span…), so a fresh
    // /traces + service/operation returned zero rows. URL stays source of
    // truth: an explicit ?rootOnly=true keeps it on; ?rootOnly=false (the
    // existing deep-links) and absence both mean off.
    rootOnly: searchParams.get('rootOnly') === 'true',
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
  const [extraCols, setExtraCols] = useState<string[]>(() => {
    // URL ?cols= wins; then the persisted per-browser selection; then
    // DEFAULT_TRACE_COLUMNS (v0.9.841 — no longer empty; see the
    // constant for the two operator decisions behind it). This
    // precedence chain is UNCHANGED: the URL-write effect below
    // re-serialises whichever source won, so the URL stays the
    // shareable source of truth after mount.
    const url = (searchParams.get('cols') ?? '').split(',').map(s => s.trim()).filter(Boolean);
    if (url.length) return url;
    try {
      const stored: unknown = JSON.parse(localStorage.getItem(EXTRA_COLS_LS_KEY) ?? 'null');
      if (Array.isArray(stored)) {
        const cols = stored.filter((c): c is string => typeof c === 'string' && c !== '');
        if (cols.length) return cols.slice(0, 8);
      }
    } catch { /* corrupt storage → default */ }
    return DEFAULT_TRACE_COLUMNS;
  });
  // Persist every column-selection change (add AND remove) so the next
  // fresh /traces visit restores it without a URL.
  useEffect(() => {
    try { localStorage.setItem(EXTRA_COLS_LS_KEY, JSON.stringify(extraCols)); } catch { /* private mode */ }
  }, [extraCols]);


  // Header viz mode + interaction state.
  // v0.9.301 — overview-chart height, persisted. Slim is the default:
  // Dynatrace keeps this strip thin because the TABLE is the page, and
  // the operator reported the same thing ("traceler az satır çıkıyor").
  const [chartTall, setChartTall] = useState(
    () => getRaw(STORAGE_KEYS.tracesChartTall) === '1');
  const toggleChartTall = () => {
    setChartTall(v => { setRaw(STORAGE_KEYS.tracesChartTall, v ? '0' : '1'); return !v; });
  };
  const [viz, setViz] = useState<'volume' | 'latency'>(() => searchParams.get('viz') === 'latency' ? 'latency' : 'volume');
  const [expanded, setExpanded] = useState<string | null>(null);
  const filterInputRef = useRef<HTMLInputElement>(null);


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
  // v0.9.858 (UX denetimi K6) — aggregate sorgusunun hata metni. Liste
  // dalının listErr'ının kardeşi; ikisi de aynı Retry nonce'ını kullanır.
  const [aggErr, setAggErr] = useState<string | null>(null);
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
      // v0.9.878 (tutarlılık denetimi BT6, risk R1) — aggregate sıralaması
      // artık primitifin KENDİ kanalından gidiyor: `s_traces-agg`.
      //
      // Bu effect sorgu dizesini SIFIRDAN kuruyor, yani listede olmayan her
      // parametreyi bir render sonra SİLER. Primitif `s_traces-agg`i kendisi
      // yazıyor; burada üretmezsek yazdığı an siliniyor ve paylaşılan
      // sıralama linki sessizce kayboluyor — alıcı kendi localStorage'ıyla
      // açar, BAŞLIK p99 der ama sunucu count'a göre sıralar.
      //
      // Tek kaynak aggSort/aggOrder durumu; primitif onu okuyor, biz onu
      // yazıyoruz. Eski `?aggSort=`/`?aggOrder=` linkleri artık ÜRETİLMİYOR
      // ama decodeLegacyAggSort köprüsüyle hâlâ OKUNUYOR.
      ['s_traces-agg', view === 'aggregate'
        ? (formatSortParam({ id: aggSort, dir: aggOrder }) ?? '')
        : ''],
      ['having',   view === 'aggregate' ? encodeHavingParam(debouncedHaving) : ''],
      ['service',  filter.service],
      ['search',   filter.search],
      ['traceId',  filter.traceId],
      ['minMs',    filter.minMs],
      ['maxMs',    filter.maxMs],
      ['hasError', filter.hasError ? 'true' : ''],
      // Default is OFF now, so only serialize when explicitly ON. buildQuery
      // drops '' → a fresh (off) session keeps the URL clean; ?rootOnly=true
      // round-trips back to the reader above (=== 'true').
      ['rootOnly', filter.rootOnly ? 'true' : ''],
      ['services', filter.requireServices.join(',')],
      // Grouped (OR / nested) → filterGroup param; flat → legacy filters param.
      // Never both: a non-empty filterGroup suppresses filters so the URL has a
      // single source of truth and the backend's prefer-filterGroup rule is moot.
      ['filters',  advGroupParam ? '' : encodeFilters(advFilters)],
      ['filterGroup', advGroupParam],
      ['cols',     extraCols.join(',')],
    ]);
    const target = qs ? `?${qs}` : '';
    if (typeof window !== 'undefined' && target !== window.location.search) {
      navigate(`/traces${target}`, { preventScrollReset: true, replace: true });
    }
  }, [range, env, view, viz, sort, order, page, groupBy, groupAttr, aggSort, aggOrder, debouncedHaving, filter, advFilters, advGroupParam, extraCols, navigate]);

  // ── List fetch ───────────────────────────────────────────────────────────
  // v0.9.636 — pencere TEK yerde hizalanıyor: liste, hacim şeridi ve
  // x ekseni üçü de buradan besleniyor, dolayısıyla üçü de AYNI
  // pencereyi görüyor. Gerekçe + bedel: lib/traceWindow.ts.
  const listRangeNs = useMemo(() => {
    const r = timeRangeToNs(range);
    return alignTraceWindow(r.from, r.to);
  }, [range]);
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
    //
    // v0.9.603 (traces D2) — bayrağın YANINDA gerçek iptal.
    //
    // Bayrak tek başına YANITI atıyordu, İSTEĞİ değil: aralığı hızlı
    // değiştiren operatör ClickHouse'ta üst üste binen sorgular
    // bırakıyordu ve her biri max_execution_time'a kadar koşuyordu.
    // Bayrak yine gerekli (iptal yarışı kazanmayabilir); ikisi birlikte.
    let cancelled = false;
    const ctl = new AbortController();
    // Only a FULL 32-hex trace id is honoured server-side (prefix search
    // removed v0.9.82 — startsWith defeats the trace_id bloom index and runs
    // unbounded). A partial id is ignored here so the normal time-bounded
    // list still renders; a complete id navigates away via apply().
    const tid = filter.traceId.trim().toLowerCase();
    const traceIdExact = /^[0-9a-f]{32}$/.test(tid) ? tid : undefined;
    const useTimeRange = !traceIdExact;
    const { from, to } = useTimeRange ? listRangeNs : { from: undefined, to: undefined };
    api.traces({
      limit: 50, offset: page * 50, from, to, sort, order,
      service: filter.service || undefined,
      search: filter.search || undefined,
      traceId: traceIdExact,
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
      // FAZ 2 — the list fetch is ALWAYS narrow (no extraAttrs): attribute
      // columns arrive via the phase-2 enrichment effect below, so a column
      // toggle never re-runs this (window-wide) query. extraCols is
      // deliberately NOT a dep here for the same reason.
      // v0.9.638 — liste ARTIK sayım istemiyor. ?count=exact tek başına
      // countModeAllowsMV'yi kapatıp listeyi ham spans yoluna düşürüyordu
      // (çift ceza). Sayı ayrı endpoint'ten geliyor; liste SQL'i bayt bayt
      // aynı kaldığı için "toplamı göster" listeyi MV'de BIRAKIYOR.
      count: 'skip',
    }, ctl.signal).then(d => { if (!cancelled) { setData(d); setRefreshing(false); } }).catch((e: unknown) => {
      // İptal HATA DEĞİL — operatörün kendi eylemi. Yutulmazsa aralık
      // her değiştiğinde ekrana kırmızı bir kutu düşerdi.
      if (cancelled || isCanceled(e)) return;
      setListErr(e instanceof Error ? e.message : 'Request failed');
      setData(null);
      setRefreshing(false);
    });
    return () => { cancelled = true; ctl.abort(); };
  }, [view, listRangeNs, sort, order, page, filter, env, advFilters, advGroupParam, showTotal, retryNonce]);

  // ── Extras enrichment (FAZ 2 — docs/audit/traces-attribute-columns.md
  // §6B). Fires when the page rows are in and attribute columns are
  // selected: ONE light call fetches the still-missing keys for exactly the
  // visible trace ids, time-bounded by the rows' REAL min/max timestamps
  // (the server pads the upper bound). Column REMOVAL never fetches — the
  // column simply leaves colIds. mergeTraceExtras stamps every requested
  // key ('' fallback), so missingExtraKeys converges to [] and this effect
  // can never re-fire in a loop.
  useEffect(() => {
    if (view !== 'list') return;
    if (!extraCols.length) return;
    const rows = data?.traces;
    if (!rows?.length) return;
    const missing = missingExtraKeys(rows, extraCols);
    if (!missing.length) return;
    let from = Infinity, to = -Infinity;
    for (const r of rows) {
      if (r.startTime < from) from = r.startTime;
      const end = r.startTime + r.durationMs * 1e6;
      if (end > to) to = end;
    }
    let cancelled = false;
    // v0.9.195 review-fix: istek kapsamındaki id seti closure'da sabitlenir —
    // bayat bir yanıt, bu arada DEĞİŞMİŞ bir sayfanın satırlarını asla
    // fetched-empty ('') damgalayamaz (kapsam-dışı satırlar dokunulmaz kalır,
    // sonraki turda yeniden istenir).
    const requestedIds = new Set(rows.map(r => r.traceId));
    api.tracesExtras({
      traceIds: rows.map(r => r.traceId).join(','),
      extraAttrs: missing.join(','),
      // -1ms guard: startTime ns exceed Number's integer precision, so the
      // rounded `from` could land a hair above the true earliest span.
      from: Math.floor(from - 1e6),
      to: Math.ceil(to),
    }).then(res => {
      if (cancelled) return;
      setData(d => d ? { ...d, traces: mergeTraceExtras(d.traces, missing, res.extras ?? {}, requestedIds) } : d);
    }).catch(() => {
      // Non-fatal: cells keep their "—" placeholder; the next list load retries.
    });
    return () => { cancelled = true; };
  }, [view, data, extraCols]);


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
    // v0.9.707 (parite dilim 3) — sabit 30 kova, 1100px şeritte 0.03
    // nokta/px demekti (taban çizgisinin en kötü ihlalcisi). Bütçe artık
    // piksel-türevi + rung-kuantalı; step sunucu cache anahtarına sınırlı
    // kardinaliteyle biner.
    // v0.9.715 (operatör: "barlar çok küçülmüş") — bar bütçesi: ~12px/bar.
    const step = stepForPoints(windowSec, barPanelMaxDataPoints(1));
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
    const ctl = new AbortController();
    // v0.9.601 — üç ayrı /api/spans/metric yerine TEK metric-batch.
    //
    // Endpoint tam bu iş için vardı (api.go:735, yorumu: "dropping
    // cold-cache time from ~3× to ~1×") ve servis detay sayfası
    // kullanıyordu; /traces kullanmıyordu. Üç sorgu AYNI WHERE'i
    // paylaşıyor, yani ClickHouse aynı span kümesini üç kez tarıyordu.
    //
    // search bu turda batch yüzeyine eklendi: olmadan geçseydik arama
    // sessizce düşer, grafik filtrelenmemiş hacmi gösterirken tablo
    // filtreli sonucu gösterirdi.
    api.spanMetricBatch({
      from: common.from, to: common.to, step: common.step,
      search: common.search, filters: common.filters, dsl: common.dsl,
      aggs: [
        { name: 'count', agg: 'count' },
        { name: 'errors', agg: 'errors' },
        { name: 'p50', agg: 'p50', field: 'duration_ms' },
      ],
    }, ctl.signal)
      .then(r => {
        if (cancelled) return;
        setVolSeries({
          count: r.series.count ?? [],
          errors: r.series.errors ?? [],
          p50: r.series.p50 ?? [],
        });
      })
      .catch((e: unknown) => { if (!cancelled && !isCanceled(e)) setVolSeries(null); });
    return () => { cancelled = true; ctl.abort(); };
  }, [view, listRangeNs, filter.service, filter.search, env, advFilters, grouped]);

  // v0.9.637 — anahtar önerisi YALNIZ boş sonuçta çekilir. CLAUDE.md
  // ES/CH maliyet disiplini: liste boyunca prefetch yok, poll yok —
  // boş kırılım nadir bir durum, o an bir kez sormak makul.
  const [attrSuggestion, setAttrSuggestion] = useState<AttrKeySuggestion | null>(null);
  useEffect(() => {
    setAttrSuggestion(null);
    if (view !== 'aggregate' || groupBy !== 'attr') return;
    const key = groupAttr.trim();
    if (!key || !agg || agg.length > 0) return;
    let cancelled = false;
    api.attributeKeys('1h', 500)
      .then(res => {
        if (cancelled) return;
        setAttrSuggestion(suggestAttrKey(key, (res ?? []).map(r => r.key)));
      })
      .catch(() => { /* öneri saf ek fayda — sessizce vazgeç */ });
    return () => { cancelled = true; };
  }, [view, groupBy, groupAttr, agg]);

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
    }).then(a => { if (!cancelled) { setAgg(a); setAggErr(null); setAggRefreshing(false); } })
      // v0.9.858 (UX denetimi K6) — agg null HİÇBİR render dalına
      // girmiyordu: sorgu hatası BOŞ EKRAN demekti (spinner yok, mesaj yok).
      .catch(e => {
        if (cancelled) return;
        setAgg(null); setAggRefreshing(false);
        setAggErr(e instanceof Error ? e.message : String(e));
      });
    return () => { cancelled = true; };
  }, [view, aggRangeNs, groupBy, groupAttr, aggSort, aggOrder, debouncedHaving, filter, env, advFilters, advGroupParam, retryNonce]);

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
    const empty = { service: '', search: '', traceId: '', minMs: '', maxMs: '', hasError: false, rootOnly: false, requireServices: [] as string[] };
    setDraft(empty); setFilter(empty); setPage(0);
    setAdvFilters([]); setAdvGroup(null); setExpanded(null);
  };
  // v0.9.878 (tutarlılık denetimi BT6) — aggregate tablosu paylaşılan
  // primitife, `serverSort` kipinde. Sıralama bir görüntüleme tercihi DEĞİL:
  // `sort` API'ye gidiyor ve backend LIMIT 200 ile en ağır grupları
  // döndürüyor, yani sıralama HANGİ 200 SATIRIN geldiğini belirliyor.
  // serverSort kipinde primitif satırları yeniden sıralamaz; sayfa yeni
  // sırayla re-fetch eder (Services'in v0.8.251 emsali).
  //
  // Kolon KÜMESİ groupBy'a bağlı (Service kolonu yalnız groupBy !== 'service'
  // iken var), bu yüzden memo. storageKey SABİT tutuldu: sıralama paylaşılan
  // bir link parametresi, groupBy'a göre adı değişirse link taşınamaz.
  // Genişlikler zaten columnLayoutSig ile korunuyor — Service kolonu gelip
  // gidince imza değişir ve bayat genişlikler DÜŞER (istenen davranış).
  const aggCols = useMemo<DataTableColumn<AggregateRow>[]>(() => [
    { id: 'name', label: groupLabel(groupBy, groupAttr), sortValue: () => '', naturalDir: 'asc', flex: true },
    // Service sıralanabilir DEĞİL (sunucu bu eksende sıralamıyor) — sortValue
    // yok, dolayısıyla başlık tıklanmaz. Eski elle <th>Service</th> ile aynı.
    ...(groupBy !== 'service'
      ? [{ id: 'service', label: 'Service', width: 170 } as DataTableColumn<AggregateRow>]
      : []),
    { id: 'count',     label: 'Traces',  sortValue: () => 0, numeric: true, width: 130 },
    { id: 'perMin',    label: 'Per min', sortValue: () => 0, numeric: true, width: 100 },
    { id: 'errorRate', label: 'Error %', sortValue: () => 0, numeric: true, width: 100 },
    { id: 'avg',       label: 'Avg',     sortValue: () => 0, numeric: true, width: 95 },
    { id: 'p50',       label: 'P50',     sortValue: () => 0, numeric: true, width: 95 },
    { id: 'p95',       label: 'P95',     sortValue: () => 0, numeric: true, width: 95 },
    { id: 'p99',       label: 'P99',     sortValue: () => 0, numeric: true, width: 95 },
    { id: 'max',       label: 'Max',     sortValue: () => 0, numeric: true, width: 95 },
  ], [groupBy, groupAttr]);

  // Eski link köprüsü: `?aggSort=p99&aggOrder=asc`. Önceliği `s_traces-agg`in
  // ALTINDA, localStorage'ın ÜSTÜNDE — paylaşılan linkin niyeti alıcının
  // kişisel varsayılanını yenmeli (primitifin resolveInitialSort sözleşmesi).
  const legacyAggSort = useMemo(
    () => decodeLegacyAggSort(searchParams.get('aggSort'), searchParams.get('aggOrder')),
    // Yalnız İLK okuma anlamlı; searchParams her yazımda değişiyor ve
    // bağımlılığa koymak köprüyü canlı bir kanala çevirirdi.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    []);

  const aggDt = useDataTable<AggregateRow>({
    storageKey: 'traces-agg',
    columns: aggCols,
    rows: agg ?? [],
    serverSort: true,
    initialSort: { id: aggSort, dir: aggOrder },
    urlSortFallback: legacyAggSort,
    onSortChange: s => {
      const id = toAggSort(s.id);
      // Tanınmayan id sunucuya gitmemeli (400). Primitif yalnız kendi
      // kolonlarını üretir, ama URL'den gelen çöp de bu yolu kullanıyor.
      if (!id) return;
      setAggSort(id);
      setAggOrder(s.dir);
    },
  });

  const traces = data?.traces ?? [];
  // v0.9.638 — tavanlı sayım, AYRI istek. Yalnız operatör "toplamı göster"
  // dediğinde koşuyor; sayfa değiştikçe DEĞİŞMEDİĞİ için sayfalama turları
  // 20sn önbelleğe biniyor (eski davranış her offset'te yeniden ödüyordu).
  const [countRes, setCountRes] = useState<TraceCountResponse | null>(null);
  useEffect(() => {
    if (!showTotal || view !== 'list') { setCountRes(null); return; }
    const ctl = new AbortController();
    let cancelled = false;
    const { from, to } = listRangeNs;
    api.tracesCount({
      limit: 50, offset: 0, from, to,
      service: filter.service || undefined,
      minMs: filter.minMs || undefined,
      maxMs: filter.maxMs || undefined,
      hasError: filter.hasError || undefined,
      rootOnly: filter.rootOnly || undefined,
      env: env || undefined,
      filterGroup: advGroupParam || undefined,
      filters: advGroupParam ? undefined : (advFilters.length ? JSON.stringify(advFilters) : undefined),
    }, ctl.signal)
      .then(r => { if (!cancelled) setCountRes(r); })
      .catch((e: unknown) => { if (!cancelled && !isCanceled(e)) setCountRes(null); });
    return () => { cancelled = true; ctl.abort(); };
  }, [showTotal, view, listRangeNs, filter.service, filter.minMs, filter.maxMs,
      filter.hasError, filter.rootOnly, env, advFilters, advGroupParam]);
  const hasMore = data?.hasMore ?? false;

  // Quick-filter chips narrow the CURRENT page client-side (instant).
  // v0.9.304 — istemci-taraflı hızlı kısayol şeridi kaldırıldı, yani
  // görüntülenen satırlar artık her zaman sunucunun döndürdükleri.
  const displayRows = traces;
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

  const openTrace = (t: TraceRow) => navigate(`/trace?id=${t.traceId}`);

  // v0.9.430 — TAM geri-yığını (audit kriteri 1): eski tek-slot
  // brushPrev yalnız İLK brush öncesini tutuyordu, ardışık zoom
  // adımları kayboluyordu. Artık N brush → N çift-tık LIFO geri sarar;
  // sayfalama her iki yönde sıfırlanır (hook onChange, v0.7.81 kuralı).
  const applyBrush = (fromMs: number, toMs: number) => {
    if (toMs - fromMs < 1) return;
    handleZoom(fromMs / 1000, toMs / 1000);
  };
  const clearBrush = handleZoomReset;

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
  // v0.9.542 (operatör) — özel attribute kolonları TIME ile SERVICE
  // ARASINA giriyor, en sağa değil. Gerekçe: channel_code /
  // function_code operatörün satırı TANIMLAMAK için okuduğu alanlar
  // (hangi kanal, hangi işlem) — servis/operasyondan ÖNCE gelmeleri
  // okuma sırasına uyuyor. En sağda kaldıklarında yatay kaydırmanın
  // ardında kalıyorlardı.
  //
  // Sıra tek yerden değişiyor: hem başlık (DataTableHead) hem hücreler
  // (renderTraceCell) colIds üzerinden çiziliyor, yani ikisi ayrışamaz.
  // Genişlik/sıralama durumu id'ye bağlı olduğu için kalıcı ayarlar
  // (localStorage) bu değişiklikten etkilenmiyor.
  // v0.9.841 — sıra artık saf yardımcıda: Time · Service · Operation ·
  // <attr kolonları> · Duration · Spans · Status (operatör isteği
  // 2026-08-09). Attr kolonları eskiden Time'ın hemen ARKASINDAYDI ve
  // satırı KİMLİKLEYEN iki alanı (Service, Operation) dört attr'ın
  // sağına itiyordu.
  const colIds = useMemo(() => traceColumnOrder(extraCols), [extraCols]);
  const columns: DataTableColumn<TraceRow>[] = useMemo(() =>
    colIds.map(id => {
      const server = SERVER_SORTABLE[id];
      return {
        id,
        label: COL_LABEL[id] ?? id,
        width: COL_W[id] ?? ATTR_W,
        // v0.9.542 (operatör: "boşluklar güzel durmuyor, fit olsun") —
        // artan genişliği Operation emsin. table-layout:fixed artanı
        // aksi hâlde TÜM kolonlara serpiyor ve tablo dağılmış görünüyor
        // (v0.9.501 Trend kolonu sınıfı). Emici olarak Operation seçildi:
        // içerik olarak en uzun alan o ("INSERT db0p.TUK_TURUN_SEPET_
        // HEADER" gibi), yani fazladan piksel en çok orada işe yarıyor.
        flex: id === 'operation',
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
      {/* v0.9.430 — Topbar seçimi out-of-band: hook yığını kendisi
          geçersizleştirir, elle temizlik gerekmez. */}
      <Topbar title="Traces" range={range} onRangeChange={setRange} envApplies />
      <div id="content">
        {/* v0.9.304 (operatör) — Trace ID araması sayfanın SAĞ ÜSTÜNE,
            zaman aralığı seçicisinin hemen altına taşındı. Filtre satırının
            içinde marginLeft:auto ile duruyordu ve oradaki alanlarla aynı
            şey sanılıyordu; oysa bir trace id ARAMASI değil bir ATLAYIŞTIR
            — diğer her alanı geçersiz kılar ve tek bir trace'e gider. */}
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 8 }}>
            <div className="trace-lookup">
              <span className="tl-icon" aria-hidden><IconSearch size={14} /></span>
              <input placeholder="Trace ID…" title="Paste a full 32-character trace ID"
                value={draft.traceId}
                onChange={e => setDraft({ ...draft, traceId: e.target.value })}
                onKeyDown={e => e.key === 'Enter' && apply()} />
              {draft.traceId && (
                <button className="tl-clear" type="button" title="Clear"
                  onClick={() => { setDraft({ ...draft, traceId: '' }); setFilter({ ...filter, traceId: '' }); }}>✕</button>
              )}
              <button className="tl-go" type="button" onClick={() => apply()}>Go</button>
            </div>
        </div>

        {/* v0.9.304 (operatör) — "Errors N / Slow >1s / servis pill'leri"
            şeridi TAMAMEN kaldırıldı; Reset ve CSV buraya, "Save current
            view" ile aynı hizaya taşındı. O şerit bir satır yüksekliği
            yiyordu ve içindekiler zaten istemci-taraflı kısayollardı —
            sunucu filtreleri v0.9.303'te Search satırına gitmişti, geriye
            kalan iki sayfa-seviyesi eylem için ayrı bir satır tutmanın
            gerekçesi kalmamıştı. */}
        <SavedViewsBar page="traces" right={
          <>
            <Button variant="secondary" size="sm" onClick={reset}>Reset</Button>
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
          </>
        } />

        {/* Header viz — Volume / Latency toggle (list view only; both derive
            from the live, filtered list rows).

            v0.9.246 (onaylı düzen sadeleştirmesi, Seçenek A) — bu blok
            önce KARTIN ÜSTÜNDE ayrı bir satırdı. Grafiği kontrol eden
            anahtar ve o grafiğin istatistikleri artık kartın kendi başlık
            şeridinde: Grafana/Datadog'un panel-başlığı deseni, ve trace
            tablosu ~37px yukarı geliyor. Tek düğüm olarak kurulup iki
            grafik dalına da veriliyor — kip değişince şerit kaymıyor. */}
        {view === 'list' && (() => {
          const vizToggle = (
            <>
              <div className="segmented">
                <button className={viz === 'volume' ? 'active' : ''} onClick={() => setViz('volume')}>Volume</button>
                <button className={viz === 'latency' ? 'active' : ''} onClick={() => setViz('latency')}>Latency</button>
              </div>
              {zoomDepth > 0 && (
                <Button variant="secondary" size="sm" onClick={clearBrush}
                  title="Zoom back one step (double-click on the chart does the same)">
                  ⤺ Zoom back{zoomDepth > 1 ? ` (${zoomDepth})` : ''}
                </Button>
              )}
            </>
          );
          // RED stat group — mono, right-aligned, over the filtered rows.
          const vizStats = (
            <>
              <HeaderStat label="TOTAL" value={fmtNum(headerStats.total)} />
              {/* v0.9.222 — scope spelled out. This counts error SPANS
                  across the whole window; the "Errors N" quick-chip below
                  counts error TRACES on the loaded page. Both were right
                  and both said "Errors", so 12.4k sitting a few hundred
                  pixels above 3 read as a broken number. */}
              <HeaderStat label="ERROR SPANS" value={fmtNum(headerStats.err)}
                tone={headerStats.err > 0 ? 'err' : undefined}
                title="Seçili pencerenin tamamındaki hatalı SPAN sayısı — yüklü satırlardan bağımsız, gerçek trafiği tarif eder." />
              <HeaderStat label="ERR RATE" value={`${headerStats.errRate.toFixed(2)}%`} tone={headerStats.errRate > 0 ? 'err' : undefined} />
              <HeaderStat label="P50 MAX" value={headerStats.p50Max ? fmtDur(headerStats.p50Max) : '—'} tone="warn" />
            </>
          );
          return data === undefined ? (
            <div style={{ background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 8, padding: 12, marginBottom: 8,
              // v0.9.301 — the skeleton must match the real card, or the
              // table jumps on every load. Tracks the persisted height.
              height: chartTall ? 192 : 152,
              display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
              <Spinner />
            </div>
          ) : viz === 'volume' ? (
            // slimmer + recedes — it's the brush/overview "tool", not the
            // headline chart; the RED strip below carries the filtered numbers.
            <VolumeChart count={volSeries?.count ?? null} errors={volSeries?.errors ?? null} p50={volSeries?.p50 ?? null}
              height={chartTall ? 140 : 100} onBrush={applyBrush} onZoomReset={clearBrush}
              xRange={{ from: listRangeNs.from / 1e9, to: listRangeNs.to / 1e9 }}
              header={vizToggle}
              headerRight={<>{vizStats}
                {/* v0.9.301 — the chart's own size control, in its header
                    strip. The comment two lines up has always said this is
                    the brush/overview TOOL, not the headline chart; at
                    140px it did not behave like one and the trace table —
                    the thing the page is for — started below the fold. */}
                <button type="button" onClick={toggleChartTall}
                  title={chartTall
                    ? 'Shrink the overview chart so more traces fit on screen'
                    : 'Expand the overview chart'}
                  style={{
                    background: 'none', border: 'none', cursor: 'pointer',
                    color: 'var(--text3)', fontSize: 11, padding: '0 2px', marginLeft: 4,
                  }}>{chartTall ? '⌃ shrink' : '⌄ expand'}</button>
              </>} />
          ) : (
            <div style={{ background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 8, padding: 12, marginBottom: 10 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 8, padding: '0 2px', flexWrap: 'wrap' }}>
                {vizToggle}
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 10, color: 'var(--text3)' }}>
                  <span style={{ width: 8, height: 8, background: 'var(--accent)', borderRadius: 8 }} /> ok
                  <span style={{ width: 8, height: 8, background: 'var(--err)', borderRadius: 8, marginLeft: 8 }} /> error
                  <span style={{ marginLeft: 8 }}>· drag to brush · y = duration (log)</span>
                </span>
                <span style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 18, flexWrap: 'wrap' }}>
                  {vizStats}
                </span>
              </div>
              <LatencyScatter rows={displayRows} onOpen={openTrace} onBrush={applyBrush} onZoomReset={clearBrush} />
            </div>
          );
        })()}

        {/* View toggle + query fields + trace-id lookup — ONE row (v0.9.246,
            onaylı Seçenek A). Görünüm kipi ve sorgu alanları iki ayrı
            `.controls` sırasındaydı; ikisi de "hangi trace'ler" sorusunu
            yanıtladığı için tek şeritte topluluyor (~43px kazanç). `.controls`
            zaten flex-wrap, dar ekranda ikinci satıra kırılır. */}
        <PageControls sticky style={{ marginBottom: 8, alignItems: 'center' }}>
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
                  <Button variant="secondary" size="sm" aria-label="Koşulu kaldır"
                    onClick={() => setHaving(p => p.filter((_, j) => j !== i))}>✕</Button>
                </span>
              ))}
              {having.length < 8 && (
                <Button variant="secondary" size="sm"
                  title='Grup metriği eşiği ekle — ör. "Error % > 1 AND P95 ms > 500"'
                  onClick={() => setHaving(p => [...p, { metric: 'errorRate', op: '>', value: 1 }])}>
                  {having.length === 0 ? '＋ Having' : '＋'}
                </Button>
              )}
            </>
          )}

              <ServicePicker value={draft.service} onChange={v => setDraft({ ...draft, service: v })}
                placeholder="Service…" width={170} onEnter={(v) => apply(v)} />
              <OperationPicker service={draft.service} value={draft.search}
                onChange={v => setDraft({ ...draft, search: v })}
                placeholder="Operation…" width={240} onEnter={() => apply()} />
              <input ref={filterInputRef} placeholder="Min ms" value={draft.minMs}
                onChange={e => setDraft({ ...draft, minMs: e.target.value })} type="number" style={{ width: 72 }} />
              <input placeholder="Max ms" value={draft.maxMs}
                onChange={e => setDraft({ ...draft, maxMs: e.target.value })} type="number" style={{ width: 72 }} />
              {/* v0.9.303 (operatör) — Errors only / Root traces artık
                  Search'ün SOLUNDA, kendi satırlarında değil. İkisi de
                  SUNUCU filtresi, yani tam olarak yanlarındaki alanlarla
                  aynı şeyi yapıyorlar: sorguyu yeniden çalıştırırlar. Ayrı
                  bir şeritte durmaları onları istemci-taraflı hızlı
                  kısayollarla aynı görsel dile sokuyordu ve bir satır
                  yüksekliği yiyordu. */}
              <label style={{ display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 11.5, cursor: 'pointer', whiteSpace: 'nowrap' }}
                title="Yalnız hatalı trace'ler. SUNUCU filtresi — seçili pencerenin tamamına uygulanır ve sorguyu yeniden çalıştırır.">
                <input type="checkbox" checked={draft.hasError}
                  onChange={() => setDraft({ ...draft, hasError: !draft.hasError })} />
                <span style={{ color: draft.hasError ? 'var(--err)' : 'var(--text2)' }}>Errors</span>
              </label>
              <label style={{ display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 11.5, cursor: 'pointer', whiteSpace: 'nowrap' }}
                title="Kök span'i depoya düşmüş trace'ler — yarım trace'leri gizler. SUNUCU filtresi.">
                <input type="checkbox" checked={draft.rootOnly}
                  onChange={() => setDraft({ ...draft, rootOnly: !draft.rootOnly })} />
                <span style={{ color: draft.rootOnly ? 'var(--accent2)' : 'var(--text2)' }}>Root</span>
              </label>
              <Button variant="primary" size="sm" onClick={() => apply()}>Search</Button>
        </PageControls>



        {/* requireServices banner. */}
        {filter.requireServices.length > 0 && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', padding: '8px 12px', marginBottom: 8, background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12 }}>
            <span style={{ color: 'var(--text2)', fontWeight: 600 }}>Trace must include:</span>
            {filter.requireServices.map((s) => (
              <Chip key={s} className="mono" removeLabel={`Remove ${s} from the required services`}
                onRemove={() => setFilter({ ...filter, requireServices: filter.requireServices.filter(x => x !== s) })}>
                {s}
              </Chip>
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
            flat representation). v0.8.x. */}
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
              {/* v0.9.645 — operatör-bildirimli: "traceleri iframe içinde gibi
                aşağıya çekmek yerine farklı bir çözüm olabilir mi?"

                560px'lik tavan, SUNUCU TARAFINDA 50 satıra sayfalanmış bir
                listeyi ~15 satırlık bir kutuya sıkıştırıyordu: 35 satır iç
                kaydırma çubuğunun ardında kalıyor ve sayfa iki ayrı dikey
                eksen taşıyordu. Operatörün kendi kuralı bunu yasaklıyor
                ("iç çerçeve yok"), ve uygulamadaki tek ihlal burasıydı —
                diğer 20+ tablo 10.000 satıra kadar content-visibility ile
                iç çerçevesiz idare ediyor.

                Tavan kalktı, yükseklik İÇERİĞE eşit: dikey kaydırma
                çubuğu kaybolur (kaydıracak bir şey yok), sayfa tek eksene
                döner. Sanallaştırma makinesi yerinde kalıyor — 50 satırda
                hiçbir şeyi pencerelemiyor ama diğer çağıranlar (Inbox,
                Incidents) onu kullanmaya devam ediyor.

                YATAY kaydırma tabloda KALIYOR: sayfaya taşımak, geniş
                tabloda #content'i yana kaydırır. */}
            <VirtualTable<TraceRow>
              dt={dt}
              height={44 + displayRows.length * 36}
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
            {/* v0.9.638 — total Pager'a GEÇMİYOR. Pager lastPage/atEnd'i
                total'dan türetiyor; tavanlı bir sayı verirsek operatörü
                listenin ULAŞAMAYACAĞI sayfalara yollar (aşama-1 kimlik
                bütçesi 5.000-6.000). Gezinme hasMore üzerinde kalıyor;
                sayı yalnız bir etiket. */}
            {/* v0.9.645 — Pager sayfanın dibine yapışıyor (uzun listede
                "Next" ekran dışında kalıyordu) ve sayı hem KESİN hem
                sunulabilir tavanın içindeyse "Last" çiziliyor.
                Gerekçe + sınır: lib/traceReach.ts */}
            <Pager page={page} pageSize={50} hasMore={hasMore} onPage={setPage}
              stickyBottom
              lastReachablePage={lastReachablePage(countRes?.value, countRes?.atLeast ?? false, 50)}
              extras={
                <>
                  {countRes?.reason ? (
                    <span title={traceCountReasonHint(countRes.reason)}>
                      showing {traces.length}{hasMore ? '+' : ''} · toplam sayılamıyor
                    </span>
                  ) : countRes ? (
                    <span title={countRes.atLeast
                      ? `Sayım ${countRes.value.toLocaleString()} trace'te durduruldu — gerçek sayı daha büyük.`
                      : 'Bu pencerede eşleşen trace sayısı.'}>
                      {countRes.value.toLocaleString()}{countRes.atLeast ? '+' : ''} total
                    </span>
                  ) : (
                    <>showing {traces.length}{hasMore ? '+' : ''}{' · '}
                      <a href="#" onClick={e => { e.preventDefault(); setShowTotal(true); }}
                        title="Tavanlı sayım — MV'den okur, listeyi yavaşlatmaz">Show total</a>
                    </>
                  )}
                  {' · '}sorted by <b>{sort}</b> {order}
                  {/* v0.8.369 — Dynatrace-style honesty hint: non-time
                      sorts rank within the newest-N slice, not the
                      whole window. */}
                  {/* v0.9.297 — the backend could not afford the window
                      the operator picked and halved it. Loud, not a
                      footnote: these rows answer a DIFFERENT question. */}
                  {data?.narrowedFromNs ? (
                    <span className="badge b-err" style={{ marginLeft: 6 }}
                      title={'This query ran out of memory or time over the range you selected, so the backend answered over a shorter, more recent window instead of failing.\nThe list below is NOT your full range — narrow the range or add a filter for an answer that covers it.'}>
                      ⚠ shortened to {tsLong(data.narrowedFromNs)} →
                    </span>
                  ) : null}
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
              {/* v0.9.637 — yanlış yazılmış anahtar SESSİZCE boş tablo
                  veriyordu: "bu attribute yok" ile "yazımı yanlış" ayırt
                  edilemiyordu. Sorgu harf DUYARLI kalıyor (bilinçli, bkz.
                  lib/attrKeySuggest.ts); açıklanan yalnız boşluk. */}
              {attrSuggestion ? (
                <>
                  <b>{groupAttr}</b> bu pencerede hiçbir span'de yok.{' '}
                  {attrSuggestion.reason === 'case'
                    ? 'Yalnız harf düzeni farklı olan bir anahtar var:'
                    : 'Şuna benzer bir anahtar var:'}{' '}
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setGroupAttr(attrSuggestion.key)}
                  >{attrSuggestion.key}</Button>
                  {' '}— attribute anahtarları harf duyarlıdır.
                </>
              ) : (
                'The aggregate view needs at least one trace to group. Switch to the Traces tab to confirm there are matching rows, or widen the time range.'
              )}
            </div>
          </Empty>
        )}
        {view === 'aggregate' && agg === null && (
          <QueryError message={aggErr} onRetry={() => setRetryNonce(n => n + 1)}>
            The aggregate query errored or timed out. Try a narrower time range
            or fewer groups, then retry.
          </QueryError>
        )}
        {view === 'aggregate' && agg && agg.length > 0 && (
          <div style={{ opacity: aggRefreshing ? 0.55 : 1, transition: 'opacity 120ms' }} aria-busy={aggRefreshing}>
          <AggregateTable agg={agg} groupBy={groupBy} dt={aggDt}
            onDrill={(a) => {
              if (groupBy === 'service') { setFilter({ ...filter, service: a.groupKey }); setDraft({ ...draft, service: a.groupKey }); }
              else if (groupBy === 'operation') { setFilter({ ...filter, search: a.groupKey, service: a.groupExtra ?? filter.service }); setDraft({ ...draft, search: a.groupKey, service: a.groupExtra ?? draft.service }); }
              else {
                // v0.9.856 (UX denetimi K11) — attribute grubunda tıklanan
                // DEĞER düşüyordu: yalnız servis taşınıyor, liste o servisin
                // TÜM trace'lerini gösteriyordu. Satır sayısı grup sayısıyla
                // tutmuyor, kullanıcı "X100'ün trace'leri bunlar" sanıyordu —
                // sessizce GENİŞLEYEN soru. Değer artık FilterBuilder çipine
                // dönüşüyor: URL'de görünür, kaldırılabilir, paylaşılabilir.
                if (groupBy === 'attr') {
                  if (grouped) setAdvGroup(g => (g ? { ...g, filters: upsertAttrFilter(g.filters, groupAttr, a.groupKey) } : g));
                  else setAdvFilters(f => upsertAttrFilter(f, groupAttr, a.groupKey));
                }
                if (a.groupExtra) { setFilter({ ...filter, service: a.groupExtra }); setDraft({ ...draft, service: a.groupExtra }); }
              }
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
      // Attribute-column values render at the SAME size as every other cell
      // (v0.9.243 — operator-reported: channel_code / function_code looked
      // smaller than OPERATION). `.mono` and `table` are both 12px; the old
      // inline fontSize:11 was the only thing making these cells odd.
      const v = t.extras?.[id] ?? '';
      return <span className="mono" style={{ color: v ? 'var(--text2)' : 'var(--text3)' }} title={v || ''}>{v || '—'}</span>;
    }
  }
}

// v0.9.878 — AggHeader SİLİNDİ. Elle basılan sıralanabilir başlık artık
// DataTableHead'in işi; bırakılsaydı iki başlık anatomisi ve iki ok glifi
// yan yana yaşardı (bu dalganın kapatmaya çalıştığı şeyin ta kendisi).

function AggregateTable({ agg, groupBy, dt, onDrill }: {
  agg: AggregateRow[]; groupBy: GroupBy;
  // v0.9.878 — sıralama durumu artık primitifte; sayfa `dt`yi kuruyor
  // (serverSort kipi) ve buraya veriyor. aggSort/aggOrder/onSort propları
  // düştü: üçü de dt.sort ve dt.toggleSort'un kopyasıydı.
  dt: DataTable<AggregateRow>;
  onDrill: (a: AggregateRow) => void;
}) {
  return (
    <>
      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} />
          <DataTableHead dt={dt} />
          <tbody>
            {dt.sortedRows.map(a => {
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
        {agg.length} groups · grouped by <b style={{ color: 'var(--accent2)' }}>{groupBy}</b> · sorted by <b>{dt.sort.id ?? 'count'}</b> {dt.sort.dir} · click a row to drill down
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
          <>Try widening the time range, dropping the service or search filter, or turning off the "Root traces" chip. If even an unfiltered query is empty, check ingest health at <Link to="/system/stats" style={{ color: 'var(--accent2)' }}>system stats</Link>.</>
        )}
      </div>
    </Empty>
  );
}
