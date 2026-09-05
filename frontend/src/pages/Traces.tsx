// Traces.tsx — the trace explorer (Phase 1 Task B, Tempo/Datadog-grade).
//
// Rebuilt on the Phase-0 perf primitives + the OTel correlation layer:
//   • Header viz: Volume (stacked ok+error bars + p99 line + TOTAL/ERRORS/
//     ERROR RATE/P99 MAX stats) ↔ Latency (duration-vs-time scatter, log y,
//     hover/click/drag-brush). Both derive from the live, filtered rows.
//   • RED-from-traces panel (rate/errors/p99) over the same filtered set.
//   • The trace table renders through VirtualTable (windowed) with a Duration
//     BAR, service-coloured badges, error tints, every cell a real <Link> to
//     the trace (v0.10.216 — middle-click opens a tab, no preview frame),
//     j/k/Enter/"/" keyboard nav.
//   • Quick-filter chips (Errors / Slow>1s / per-top-service), the advanced
//     FilterBuilder ("+ Add filter" → attribute/op/value, with a grouped
//     AND/OR mode), "+ Column" via ColumnManager, full filter row.
//   • Aggregated + Shapes tabs preserved.
//
// Range is the SINGLE-source-of-truth via useUrlRange; timeRangeToNs(range)
// only ever runs inside a useMemo([range]) (the v0.5.184 trap).

import { useEffect, useMemo, useRef, useState, Suspense, Fragment } from 'react';
import { rowActivation } from '@/lib/a11y';
import { attrKeyWindowParams } from '@/lib/attrKeyWindow';
import { Link, useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { IconSearch } from '@/components/icons';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { OperationPicker } from '@/components/OperationPicker';
import { ServicePicker } from '@/components/ServicePicker';
import { FilterQueryBox } from '@/components/FilterQueryBox';
import { FilterGroupBuilder } from '@/components/FilterGroupBuilder';
import { Button } from '@/components/ui/Button';
import { Chip } from '@/components/ui/Chip';
import { Pager } from '@/components/Pager';
import { ColumnManager } from '@/components/ColumnManager';
import { stepForPoints, barPanelMaxDataPoints } from '@/lib/chartStep';
import { VirtualTable } from '@/components/ui/DataTable';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/ui/DataTable';
import type { DataTable } from '@/components/ui/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { formatSortParam } from '@/lib/dataTable';
import { type AggSort, toAggSort, decodeLegacyAggSort } from './traces/aggSort';
import { parseRootOnlyParam, rootOnlyUrlValue, shouldDropRootOnly } from './traces/rootOnlyFallback';
import { api, isCanceled } from '@/lib/api';
import { usePageZoomRange } from '@/lib/chart/usePageZoomRange';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { tsDateTime, tsLong, timeRangeToNs, fmtNum, fmtFixed } from '@/lib/utils';
import { alignTraceWindow } from '@/lib/traceWindow';
import { suggestAttrKey, type AttrKeySuggestion } from '@/lib/attrKeySuggest';
import { traceCountReasonHint } from '@/lib/traceCountReason';
import { lastReachablePage } from '@/lib/traceReach';
import type { TraceCountResponse } from '@/lib/types';
import { encodeRange, encodeFilters, decodeFilters, encodeFilterGroup, decodeFilterGroup, buildQuery, rebuildPreserving } from '@/lib/urlState';
import { parseHavingParam, encodeHavingParam, HAVING_METRICS, HAVING_OPS, type HavingRow, type HavingMetric, type HavingOp } from '@/lib/havingParam';
import { upsertAttrFilter } from '@/lib/aggDrill';
import { useAttributeKeys } from '@/lib/useAttributeKeys';
import { Combobox } from '@/components/Combobox';
import { mergeTraceExtras, missingExtraKeys } from '@/lib/traceExtrasMerge';
// v0.9.841 — kolon SIRASI ve varsayılan attr seti tek yerde, saf ve
// testli (traceColumns.ts). İkisi de karar; mekanik değil.
import { DEFAULT_TRACE_COLUMNS, FIXED_COLS, traceColumnOrder } from '@/lib/traceColumns';
import { useContextParams, type ContextPatch } from '@/hooks/useContextParams';
import { useTablePrefs } from '@/lib/queries/prefs';
import { parseColsParam } from '@/lib/columnModel';
import type { ContextDim } from '@/lib/contextParams';
import { getRaw, setRaw, STORAGE_KEYS } from '@/lib/storage';
import { useAuth } from '@/components/AuthProvider';
import { tracesExplainUrl } from '@/lib/tracesExplainUrl';
import type { TracesParams } from '@/lib/api';
import type { TracesResponse, TraceRow, TimeRange, SortColumn, SortOrder, AggregateRow, FilterExpr, FilterGroup, SpanMetricSeries } from '@/lib/types';
import { traceHref } from '@/lib/traceHref';

import { VolumeChart } from '@/components/traces/VolumeChart';
import { stripScope, volumeUnitFor } from '@/components/traces/volumeSeries';
import { LatencyScatter } from '@/components/traces/LatencyScatter';
import { ShapesView } from '@/components/traces/ShapesView';
import { SvcBadge, DurationBar, fmtDur } from '@/components/traces/shared';
import { PageControls } from '@/components/ui/PageControls';
import { QueryError } from '@/components/QueryError';
import { PageShell } from '@/components/ui/PageShell';
import { useEntityEnabled } from '@/lib/queries';
import { isTraceK8sCol, withK8sColumns, canAddK8sColumns } from '@/lib/traceK8sLinks';
import type { EntityClusterInfo } from '@/lib/types';

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
// v0.10.217 (Dynatrace düzeni) — id'ler sabit, ETİKET değişti:
// operation → "Name" (ilk, baskın kolon), time → "Start time" (en sonda).
const COL_LABEL: Record<string, string> = {
  time: 'Start time', service: 'Service', operation: 'Name',
  duration: 'Duration', spans: 'Spans', status: 'Status',
  // v0.10.251 — kimlik küçük harf (prod yazımı: channel_code), ETİKET
  // operatörün yazımı (audit soru 5, önerilen varsayılan).
  channel_code: 'CHANNEL_CODE', function_code: 'FUNCTION_CODE',
};
// v0.10.251 — ContextBar'ın bu sayfada uyguladığı boyutlar; namespace/compare
// uygulanmıyor (backend FilterExpr yok — audit soru 8 açık) → çubukta devre dışı.
// v0.10.257 (operatör): service sayfanın kendi picker'ında — çubukta İKİ KEZ çizilmez.
const TRACES_CONTEXT_DIMS: ContextDim[] = ['range', 'env', 'cluster'];
const TRACES_CONTEXT_HIDDEN: ContextDim[] = ['service'];
// Default widths are tuned so the fixed columns PLUS the attribute columns
// fit a 1440px laptop without horizontal scroll (v0.9.243 — operator-reported:
// "columns don't fit, I always have to scroll right"). Budget at 1440px:
// ~220 sidebar + ~40 page padding leaves ~1180 for columns (the 30px
// leading row-marker went with the preview column in v0.10.216).
//
// v0.9.1360 — BÜTÇE YENİDEN DAĞITILDI. Operatör `function_id`'yi de
// varsayılan istedi (DEFAULT_TRACE_COLUMNS artık BEŞ öznitelik) ve "kolonlar
// sayfaya sığsın" dedi. Eski değerlerle aritmetik tutmuyordu ve zaten
// tutmuyormuş: şerh "fixed 864 + 2×130 attrs = 1154" diyordu ama varsayılan
// v0.9.841'den beri DÖRT öznitelik taşıyor → 864+30+520 = 1414, yani bütçeyi
// 264px aşıyordu. Yani "sığmıyor" şikâyeti yeni değil, ölçülmemiş bir
// regresyondu.
//
// Yeni dağıtım, hepsi İÇERİĞE bakılarak kısıldı (canlı ekran görüntüsünden):
//   time     168 → 150  "2026-08-24 21:33:32…" zaten kırpılıyor
//   duration 150 → 104  en uzun değer "44.36ms" / "4.14s"
//   spans     72 →  58  tek haneli
//   status    84 →  74  "ERROR" rozeti
//   operation 260 → 210 "log.servi…" zaten kırpık; kimliği service taşıyor
// Toplam kazanç 148px. Yeni sabit toplam 716 + 30 + 5×130 = 1396 — hâlâ
// 1180'in üstünde, AMA v0.9.1334'ün kaba-sığdırması artık CANLI: aşan küme
// minWidth tabanlarına saygılı oransal küçültmeyle sığdırılıyor, yani
// yatay kayma yerine sıkışma. Kısıntı o sıkışmanın okunabilir kalması için.
//
// Her hücre zaten ellipsis + title tooltip taşıyor (globals.css tbody td),
// yani dar olmak "sessizce kayboldu" demek DEĞİL. Genişlikler kullanıcı
// tarafından sürüklenebilir ve tarayıcı başına kalıcı; bunlar yalnız hiç
// sürüklememiş operatörün başlangıç noktası.
const COL_W: Record<string, number> = {
  time: 150, service: 130, operation: 210, duration: 104, spans: 58, status: 74,
  // v0.10.187 (görsel inceleme F11) — K8s kolonları ATTR_W'de kırpılıyordu (pod adı 40+ karakter)
  // v0.10.330 — çip kalkınca düz metin + üç nokta; başlangıç genişlikleri tablo
  // 1440 px'e sığsın diye daraltıldı (sürüklenebilir, tarayıcı başına kalıcı).
  'k8s.pod.name': 220, 'k8s.namespace.name': 130, 'k8s.node.name': 170, cluster: 90,
};
const ATTR_W = 130;
const EXTRA_COLS_LS_KEY = 'traces-extra-cols';
// Shared value-suggestion seeds for the advanced filter builders (flat +
// grouped). Hoisted so both render paths use the identical hints.
// v0.10.264 — hızlı çipler (mockup A); son kullanılanlar tarayıcı-yerel.
const TRACES_QUICK_FILTERS: FilterExpr[] = [
  { k: 'status_code', op: '=', v: ['error'] },
  { k: 'kind', op: '=', v: ['server'] },
  { k: 'http.method', op: '=', v: ['POST'] },
];
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
  // v0.10.219 — satır Link'i listenin O ANKİ URL'sini state olarak taşır;
  // /trace breadcrumb'ı onunla geri döner (lib/traceBackHref.ts).
  const loc = useLocation();
  const [searchParams] = useSearchParams();

  // v0.9.430 — zoom-yığını hook'u; sayfalama her zoom/geri adımında
  // sıfırlanır (onChange; setPage sonradan tanımlı — closure çağrı
  // anında değerlendirilir, TDZ yok).
  const { range, setRange, handleZoom, handleZoomReset, zoomDepth } = usePageZoomRange('30m', () => setPage(0));
  // v0.10.251 — ContextBar (Topbar yuvası). Aralık sahibi usePageZoomRange
  // KALIR (sayfa sıfırlama + zoom); çubuğun set()'i aralığı oraya, kalanı
  // URL'ye (cluster) yönlendirir. İki yazıcı aynı useUrlRange kanalından
  // geçer → hemfikir. v0.10.257: service çubukta YOK (sayfanın picker'ı).
  const ctx = useContextParams({ defaultPreset: '30m', applies: TRACES_CONTEXT_DIMS });
  const ctxForBar = useMemo(() => ({
    ...ctx,
    set: (patch: ContextPatch) => {
      if (patch.range !== undefined) setRange(patch.range);
      const { range: _r, ...rest } = patch;
      if (Object.keys(rest).length) ctx.set(rest);
    },
  }), [ctx, setRange]);
  // Global env filter (v0.8.383) — written by the Topbar EnvPicker,
  // consumed here as a first-class server param on the list/aggregate
  // fetches (+ volume strip + CSV). /traces is the Phase-1 consumer;
  // it applies to List + Aggregated — Relations/Shapes follow with
  // env-separation Phase 2+.
  const [env] = useUrlEnv();
  // v0.9.943 (B3/Ö5) — `?cluster=`. /endpoints ve EndpointDetail'in
  // "Traces →" pivotu bu paramı v0.9.307'den beri yazıyordu; bu sayfa
  // onu OKUMUYORDU, yani cluster=A altında bakılan bir satırın pivotu
  // TÜM cluster'ların trace'lerini listeliyordu — pivot soruyu sessizce
  // GENİŞLETİYORDU.
  //
  // env gibi state'e KOPYALANMIYOR: bu sayfanın kurduğu bir filtre değil,
  // gelen linkin taşıdığı kapsam. A7'nin rebuildPreserving'i sahiplenmediği
  // parametreleri zaten koruyor, yani URL tek kaynak olarak yeterli.
  const clusterScope = searchParams.get('cluster') ?? '';
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
  // v0.9.933 (Ö14) — aggregate anahtar kutusunun keşif kaynağı. Bağlam
  // filtresi VERİLMİYOR: gruplama anahtarı seçilirken operatör henüz
  // daraltmadı, ve dilim-içi liste burada erken bir kısıtlama olurdu.
  // v0.9.953 (F3/Ö14c) — keşif penceresi SAYFANIN aralığından, basamaklı
  // (attrKeyWindow.snapSince). Sabit '1h' iken 7 günlük pencereye bakan
  // operatör son bir saatte görülmemiş bir anahtarı öneride bulamıyordu;
  // ham pencere geçirmek ise sunucunun 60 sn'lik cache'ini hiç ısıtmazdı
  // (v0.8.270).
  const attrWindow = useMemo(() => attrKeyWindowParams(range), [range]);
  const { keys: attrKeys } = useAttributeKeys(undefined, attrWindow);
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
    rootOnly: parseRootOnlyParam(searchParams.get('rootOnly')).rootOnly,
    requireServices: (searchParams.get('services') ?? '').split(',').map(s => s.trim()).filter(Boolean),
  }));
  const [draft, setDraft] = useState(filter);
  // v0.9.1372 — sessiz root geri dönüşü KURULUMU. `?rootOnly=auto` ile gelen
  // pivotlar (endpoint / database) root seçili açılır; liste boş dönerse
  // filtre kendiliğinden düşer. Kurulum tek kullanımlık, aşağıdaki efekt
  // tetiklendiğinde iniyor.
  const [rootOnlyAuto, setRootOnlyAuto] = useState(
    () => parseRootOnlyParam(searchParams.get('rootOnly')).auto);
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
  // v0.10.143 — Kubernetes kolonları/linkleri yalnız entity katmanı açıkken.
  const { enabled: k8sOn, clusters: entityClusters } = useEntityEnabled();
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
  // v0.10.251 — sunucu tercihi (/api/preferences/traces-list, audit §11).
  // Öncelik: URL ?cols= > sunucu > localStorage > varsayılan. URL'de cols
  // yokken sunucu modeli BİR KEZ benimsenir (prefs çözülünce); değişimler
  // debounce'lu PUT ile sunucuya (genişlikler hariç). `cols=` kendi-kendine-
  // yazım yarışı: prefs bekliyorken ve URL'de cols yokken URL effect'i
  // cols yazmaz — aksi hâlde URL > sunucu önceliği sunucu tercihini sonsuza
  // dek erişilmez kılardı.
  const prefs = useTablePrefs('traces-list');
  const urlHadCols = useRef(!!parseColsParam(searchParams.get('cols')));
  const prefsAdopted = useRef(false);
  const colsOwned = prefs.model !== undefined || urlHadCols.current;
  useEffect(() => {
    if (prefs.model === undefined || prefsAdopted.current) return;
    prefsAdopted.current = true;
    if (urlHadCols.current || !prefs.model) return;
    const fixed = new Set<string>(FIXED_COLS);
    const extras = prefs.model.order.filter(id => !fixed.has(id) && !prefs.model!.hidden.includes(id)).slice(0, 8);
    if (extras.length) setExtraCols(extras);
  }, [prefs.model]);
  useEffect(() => {
    if (!prefsAdopted.current) return;
    const fixed = new Set<string>(FIXED_COLS);
    const serverExtras = prefs.model ? prefs.model.order.filter(id => !fixed.has(id)) : null;
    if (serverExtras && serverExtras.join(',') === extraCols.join(',')) return;
    prefs.save({ v: 1, order: traceColumnOrder(extraCols), hidden: [], sig: 'traces-list' });
    // eslint-disable-next-line react-hooks/exhaustive-deps
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


  const [data, setData] = useState<TracesResponse | null | undefined>(undefined);
  const lastListParamsRef = useRef<TracesParams | null>(null);
  const { user: authUser } = useAuth();
  const explainHref = authUser?.role === 'admin' ? tracesExplainUrl(lastListParamsRef.current) : null;
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
    // v0.9.940 (A7) — rebuildPreserving: bu efekt sorgu dizesini SIFIRDAN
    // kuruyor, yani aşağıdaki listede olmayan her parametreyi bir render
    // sonra silerdi. `?env=` (v0.8.383/K4) ve `?s_traces-agg` (v0.9.878/K9)
    // tam olarak böyle kayboldu ve ikisi de tek tek listeye eklenerek
    // yamandı. Artık varsayım tersine: efekt yalnız KENDİ parametrelerine
    // sahip, tanımadığını taşır. İkisi listede KALIYOR — sahiplik ancak
    // adı geçen anahtarı temizleme (boş değer) hakkı verir.
    const qs = rebuildPreserving(window.location.search, [
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
      ['rootOnly', rootOnlyUrlValue(filter.rootOnly)],
      ['services', filter.requireServices.join(',')],
      // Grouped (OR / nested) → filterGroup param; flat → legacy filters param.
      // Never both: a non-empty filterGroup suppresses filters so the URL has a
      // single source of truth and the backend's prefer-filterGroup rule is moot.
      ['filters',  advGroupParam ? '' : encodeFilters(advFilters)],
      ['filterGroup', advGroupParam],
      ['cols',     colsOwned ? extraCols.join(',') : (new URLSearchParams(window.location.search).get('cols') ?? '')],
    ]);
    const target = qs ? `?${qs}` : '';
    if (typeof window !== 'undefined' && target !== window.location.search) {
      navigate(`/traces${target}`, { preventScrollReset: true, replace: true });
    }
  }, [range, env, view, viz, sort, order, page, groupBy, groupAttr, aggSort, aggOrder, debouncedHaving, filter, advFilters, advGroupParam, extraCols, colsOwned, navigate]);

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
    // v0.10.343 — Operator-reported: "Trace ID…" kutusuna function_id yazdı,
    // kutu 32-hex dışını yok sayıp filtresiz liste gösterdi. 32-hex olmayan
    // bir değer KİMLİKTİR (function_id gibi): arama terimi olarak gider,
    // sunucu kimlik-önce yolunu (v0.10.342) koşar. Ham hâli — eşitlik
    // büyük/küçük harf duyarlı, o yüzden lowercase edilmiş `tid` değil.
    const identityTerm = !traceIdExact && filter.traceId.trim() ? filter.traceId.trim() : undefined;
    const useTimeRange = !traceIdExact;
    const { from, to } = useTimeRange ? listRangeNs : { from: undefined, to: undefined };
    const listParams: TracesParams = {
      limit: 50, offset: page * 50, from, to, sort, order,
      service: filter.service || undefined,
      search: filter.search || identityTerm || undefined,
      traceId: traceIdExact,
      minMs: filter.minMs || undefined,
      maxMs: filter.maxMs || undefined,
      hasError: filter.hasError || undefined,
      rootOnly: filter.rootOnly || undefined,
      // Global Topbar env filter (v0.8.383) — first-class param so it
      // composes with filters AND filterGroup server-side.
      env: env || undefined,
      // v0.9.943 (B3) — pivotun taşıdığı cluster kapsamı. Aynı
      // birinci-sınıf gerekçe: FilterRoot düz filtreleri supersede eder.
      cluster: clusterScope || undefined,
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
    };
    // v0.10.328 — boş sonuç ekranındaki "Teşhis (explain)" linki SON isteğin
    // parametrelerini kullanır (URL cerrahisi yok; yalnız admin görür).
    lastListParamsRef.current = listParams;
    api.traces(listParams, ctl.signal).then(d => { if (!cancelled) { setData(d); setRefreshing(false); } }).catch((e: unknown) => {
      // İptal HATA DEĞİL — operatörün kendi eylemi. Yutulmazsa aralık
      // her değiştiğinde ekrana kırmızı bir kutu düşerdi.
      if (cancelled || isCanceled(e)) return;
      setListErr(e instanceof Error ? e.message : 'Request failed');
      setData(null);
      setRefreshing(false);
    });
    return () => { cancelled = true; ctl.abort(); };
  }, [view, listRangeNs, sort, order, page, filter, env, clusterScope, advFilters, advGroupParam, showTotal, retryNonce]);

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
    // v0.10.268 (operatör onayı, mockup A) — şerit GİRİŞ span'ı kapsamlı:
    // istek ≈ trace (Dynatrace "trace count"), medyan = giriş span'ı p50.
    // Dar rollup (service_name, span_kind, status_code) bu şekli MV'den
    // servis eder (rollup_fastpath.go başlığı); attribute filtresi varsa
    // zaten ham yol. Geri alma: bu satır + volumeSeries eksen takası.
    // v0.10.323 (operatör, prod: "db.statement seçildiğinde histogram
    // gelmiyor") — kind kısıtı YALNIZ filtre giriş span'ında yaşayan
    // anahtarlardaysa; db./messaging./… ya da serbest metin varsa şerit
    // eşleşen span'leri sayar (volumeSeries.ts stripScope başlığı).
    if (stripScope(chartFilters, filter.search ?? '') === 'entry') {
      chartFilters.push({ k: 'kind', op: 'IN', v: ['server', 'consumer'] });
    }
    if (env) chartFilters.push({ k: 'deployment.environment', op: '=', v: [env] });
    // v0.9.943 (B3) — hacim şeridi de aynı kapsamı çizmeli, yoksa grafik
    // tablonun saymadığı trace'leri gösterirdi. `cluster` v0.9.942'den beri
    // spans tarafında da well-known bir filtre anahtarı.
    if (clusterScope) chartFilters.push({ k: 'cluster', op: '=', v: [clusterScope] });
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
  }, [view, listRangeNs, filter.service, filter.search, env, clusterScope, advFilters, grouped]);

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
    api.attributeKeys(attrWindow, 500)
      .then(res => {
        if (cancelled) return;
        setAttrSuggestion(suggestAttrKey(key, (res ?? []).map(r => r.key)));
      })
      .catch(() => { /* öneri saf ek fayda — sessizce vazgeç */ });
    return () => { cancelled = true; };
  }, [view, groupBy, groupAttr, agg, attrWindow]);

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
      // v0.9.943 (B3) — toplu görünüm listeyle AYNI kapsamı okur; sekme
      // değiştirmek soruyu genişletemez.
      cluster: clusterScope || undefined,
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
  }, [view, aggRangeNs, groupBy, groupAttr, aggSort, aggOrder, debouncedHaving, filter, env, clusterScope, advFilters, advGroupParam, retryNonce]);

  // apply commits the draft as the live filter (overrideService sidesteps the
  // picker auto-commit race).
  const apply = (overrideService?: string) => {
    const tid = draft.traceId.trim().toLowerCase();
    if (/^[0-9a-f]{32}$/.test(tid)) { navigate(traceHref(tid, { pageRange: range })); return; }
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
    setAdvFilters([]); setAdvGroup(null);
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
      // v0.9.943 (B3) — sayım listeyle AYNI evreni saymak ZORUNDA
      // (v0.9.638 sözleşmesi); cluster düşerse rozet listeden fazlasını
      // söyler.
      cluster: clusterScope || undefined,
      filterGroup: advGroupParam || undefined,
      filters: advGroupParam ? undefined : (advFilters.length ? JSON.stringify(advFilters) : undefined),
    }, ctl.signal)
      .then(r => { if (!cancelled) setCountRes(r); })
      .catch((e: unknown) => { if (!cancelled && !isCanceled(e)) setCountRes(null); });
    return () => { cancelled = true; ctl.abort(); };
  }, [showTotal, view, listRangeNs, filter.service, filter.minMs, filter.maxMs,
      filter.hasError, filter.rootOnly, env, clusterScope, advFilters, advGroupParam]);
  // v0.9.1372 — sessiz geri dönüş. Koşul, aşağıdaki "no traces found" boş
  // durumunun GÖRÜNME koşuluyla aynı: liste görünümü, hata yok, veri geldi,
  // sıfır satır. Operatör o boşluğu görüp root kutusunu elle kaldıracaktı;
  // ürün onun yerine yapıyor ve sonucu gösteriyor — uyarı şeridi YOK
  // (operatör direktifi: "geri dönüş sessiz olsun").
  useEffect(() => {
    if (!shouldDropRootOnly({
      auto: rootOnlyAuto,
      rootOnly: filter.rootOnly,
      loaded: view === 'list' && !!data,
      errored: !!listErr,
      rowCount: traces.length,
    })) return;
    setRootOnlyAuto(false);
    setFilter(f => ({ ...f, rootOnly: false }));
    // Taslak da inmeli, yoksa kutu açık görünür ama sorgu kapalı koşar —
    // ekranın kendi hakkında yalan söylediği hâl.
    setDraft(d => ({ ...d, rootOnly: false }));
  }, [rootOnlyAuto, filter.rootOnly, view, data, listErr, traces.length]);

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
  const volumeUnit = volumeUnitFor(!!filter.service, stripScope(!grouped ? advFilters : [], filter.search ?? ''));
  const headerStats = useMemo(() => {
    const cPts = volSeries?.count?.[0]?.points ?? [];
    const eMap = new Map((volSeries?.errors?.[0]?.points ?? []).map(p => [p.time, p.value]));
    const pPts = volSeries?.p50?.[0]?.points ?? [];
    let total = 0, err = 0, p50Max = 0;
    for (const p of cPts) { total += p.value; err += eMap.get(p.time) ?? 0; }
    for (const p of pPts) if (p.value > p50Max) p50Max = p.value;
    return { total, err, errRate: total > 0 ? (err / total) * 100 : 0, p50Max };
  }, [volSeries]);

  const openTrace = (t: TraceRow) => navigate(traceHref(t.traceId, { pageRange: range }));

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
  // v0.9.841 — sıra artık saf yardımcıda (operatör isteği 2026-08-09:
  // attr kolonları, satırı KİMLİKLEYEN Service/Operation'ın sağına).
  // v0.10.217 — Dynatrace düzeni: Name · Service · <attr kolonları> ·
  // Duration · Status · Spans · Start time (mockup onayı 2026-09-01;
  // attr'ların kimlik alanlarından hemen sonra durması korundu).
  // v0.10.220 — operatör: Start time EN SOLDA; kalan sıra aynen.
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
    // v0.10.251 — sunucu demeti: j son satırda sonraki sayfa, k ilk satırda
    // önceki (onPageBoundary v0.9.1018 — bu sayfa hiç bağlamamıştı).
    server: { page, pageSize: 50, hasMore, onPage: setPage },
    // v0.9.1003 (etkileşim denetimi C3) — `searchRef` BİLEREK verilmiyor.
    // useDataTable, onOpen + searchRef birlikte geldiğinde İKİNCİ bir `/`
    // bindingi kaydediyor (DataTable.tsx:213) ve kısayol yığınının tepesi
    // "en son kaydolan" olduğu için sayfa mount'unda gelen o kayıt,
    // AppShell'de daha önce mount olan GlobalShortcuts'ı yeniyordu.
    // Sonuç: v0.9.951'in düzelttiği bug (yanlış kutu) hedefi değiştirerek
    // yaşıyordu — `/` servis picker'ına değil "Min ms" SAYI kutusuna
    // iniyordu ve ShortcutsHelp de "Focus filter" diye yanlış vaat
    // ediyordu. Bu satır geri gelirse ikisi de geri gelir; kapı
    // components/shortcutSearchTarget.test.ts.
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
      <Topbar title="Traces" context={{ ctx: ctxForBar, envApplies: true, hidden: TRACES_CONTEXT_HIDDEN }} />
      <PageShell>
        {/* v0.9.304 (operatör) — Trace ID araması sayfanın SAĞ ÜSTÜNE,
            zaman aralığı seçicisinin hemen altına taşındı. Filtre satırının
            içinde marginLeft:auto ile duruyordu ve oradaki alanlarla aynı
            şey sanılıyordu; oysa bir trace id ARAMASI değil bir ATLAYIŞTIR
            — diğer her alanı geçersiz kılar ve tek bir trace'e gider. */}
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 8 }}>
            <div className="trace-lookup">
              <span className="tl-icon" aria-hidden><IconSearch size={14} /></span>
              <input placeholder="Trace ID veya kimlik (function_id)…" title="32 karakterlik trace ID doğrudan trace'e gider; başka bir kimlik değeri (function_id gibi) terfi/facet anahtarlarında eşitlikle aranır"
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
              if (clusterScope) p.set('cluster', clusterScope); // v0.9.943 (B3) — aynı gerekçe
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
              {/* v0.10.268 — giriş span'ı sayımı: servisli pencerede trace, servissiz istek. */}
              <HeaderStat label={volumeUnit.toUpperCase()} value={fmtNum(headerStats.total)}
                title="Giriş span'leri (server/consumer): servis seçiliyken istek = trace; servissiz pencerede her hop sayılır." />
              {/* v0.9.222 — scope spelled out. This counts error SPANS
                  across the whole window; the "Errors N" quick-chip below
                  counts error TRACES on the loaded page. Both were right
                  and both said "Errors", so 12.4k sitting a few hundred
                  pixels above 3 read as a broken number. */}
              <HeaderStat label={'ERROR ' + volumeUnit.toUpperCase()} value={fmtNum(headerStats.err)}
                tone={headerStats.err > 0 ? 'err' : undefined}
                title="Seçili pencerenin tamamındaki hatalı giriş span'ı sayısı — yüklü satırlardan bağımsız, gerçek trafiği tarif eder." />
              <HeaderStat label="ERR RATE" value={`${headerStats.errRate.toFixed(2)}%`} tone={headerStats.errRate > 0 ? 'err' : undefined} />
              <HeaderStat label="MEDIAN MAX" value={headerStats.p50Max ? fmtDur(headerStats.p50Max) : '—'} tone="warn"
                title="Giriş span'ı medyan yanıt süresinin penceredeki en yüksek kovası." />
            </>
          );
          return data === undefined ? (
            <div style={{ background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 8, padding: 12, marginBottom: 8,
              // v0.9.301 — the skeleton must match the real card, or the
              // table jumps on every load. Tracks the persisted height.
              height: chartTall ? 282 : 222,
              display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
              <Spinner />
            </div>
          ) : viz === 'volume' ? (
            // slimmer + recedes — it's the brush/overview "tool", not the
            // headline chart; the RED strip below carries the filtered numbers.
            <VolumeChart count={volSeries?.count ?? null} errors={volSeries?.errors ?? null} p50={volSeries?.p50 ?? null}
              // v0.10.268 — Dynatrace ölçeği (mockup A: 200 px); shrink/expand duruyor.
              height={chartTall ? 230 : 170} unit={volumeUnit} onBrush={applyBrush} onZoomReset={clearBrush}
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
              {/* v0.9.933 (UX denetimi Ö14) — anahtar kutusu artık KEŞİF
                  yapıyor. Çıplak bir <input>tü: operatör tam da "hangi
                  attribute'a göre gruplasam?" diye sorarken anahtarı
                  ezberden yazmak zorundaydı, oysa aynı sayfadaki
                  FilterBuilder yirmi satır ötede o listeyi zaten çekiyordu.
                  Yanlış yazımın bedeli sessiz: sorgu koşuyor, boş dönüyor
                  ve aşağıdaki "bu pencerede hiçbir span'de yok" cümlesi
                  "böyle veri yok" diye okunuyor, "adı farklı" diye değil.
                  Liste sunucuda 500'e kapalı — eager katalog değil. */}
              {groupBy === 'attr' && (
                <Combobox value={groupAttr} onChange={setGroupAttr}
                  options={attrKeys}
                  placeholder="attribute key (e.g. user.id)" width={200} />
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

              {/* v0.9.951 (E5/Ö31) — `/` kısayolunun AÇIK hedefi.
                  İşaretsizken GlobalShortcuts "ilk görünür metin
                  kutusu"na düşüyor ve bu sayfada o kutu DOM sırasında
                  "Trace ID…": `/` basan operatör servis aramak isterken
                  trace-id kutusunda buluyordu kendini. Mekanizma
                  v0.5.454'te TAM bu vaka için yazılmıştı (yorumu da
                  öyle diyor) ama işaret hiçbir sayfaya konmamıştı. */}
              <ServicePicker value={draft.service} onChange={v => setDraft({ ...draft, service: v })}
                placeholder="Filter by service…" width={170} onEnter={(v) => apply(v)} shortcutSearch />
              <OperationPicker service={draft.service} value={draft.search}
                onChange={v => setDraft({ ...draft, search: v })}
                placeholder="Operation, or an id (function_id / trace ID)…" width={240} onEnter={() => apply()} />
              <input placeholder="Min ms" value={draft.minMs}
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
            /* v0.10.264 (operatör: "add filter daha kullanışlı" → mockup A onayı) —
               tek satır sorgu kutusu; FilterBuilder Explore/Logs'ta kalır. */
            <FilterQueryBox value={advFilters} onChange={setAdvFilters}
              suggestedValues={FILTER_SUGGESTED_VALUES} quick={TRACES_QUICK_FILTERS}
              recentKey="traces-recent-filters" />
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
          <TracesEmpty service={filter.service} search={filter.search} range={range} onSwitchView={() => setView('aggregate')}
            explainHref={explainHref ?? undefined}
            matchingSpans={data?.emptyDiag?.matchingSpans}
            promotedDiag={data?.emptyDiag}
            identity={data?.identity}
            narrowedFromNs={data?.narrowedFromNs} />
        )}
        {view === 'list' && data && traces.length > 0 && (
          <div style={{ opacity: refreshing ? 0.55 : 1, transition: 'opacity 120ms' }}
            aria-busy={refreshing}>
            {/* v0.10.339 — Operator-reported: terfi kolonu uyuşmazlığı. Bu
                satırlar dizi yolundan geldi (sunucu kolonun yalan söylediğini
                ölçtü ve haritayı askıya aldı); host = onarım hedefi. */}
            {data.emptyDiag?.promotedFallback && (
              <div style={{ marginBottom: 6, padding: '6px 10px', fontSize: 11.5, border: '1px solid var(--border)', borderLeft: '3px solid var(--warn)', borderRadius: 6, background: 'var(--bg2)', color: 'var(--text2)' }}>
                <b>Terfi kolonu uyuşmazlığı:</b> {(data.emptyDiag.promotedKeys ?? []).join(', ')} filtresi kolon yolunda 0 span buldu, dizi yolunda{' '}
                {(data.emptyDiag.promotedHosts ?? []).map(h => `${h.host}: ${h.arr.toLocaleString()}`).join(' · ')}. Liste dizi yoluyla cevaplandı (doğru, yavaş); sunucu haritayı askıya aldı — ilgili replikada kolon şemasını / takılı mutasyonu kontrol et.
              </div>
            )}
            {/* Column toolbar — attribute columns are added via "+ Column"
                (ColumnManager) and removed by their chips. VirtualTable's shared
                header auto-renders the sortable/resizable data columns, so the
                add/remove affordances live here above the table. */}
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', marginBottom: 6 }}>
              <ColumnManager cols={extraCols}
                onAdd={k => { if (!extraCols.includes(k) && extraCols.length < 8) setExtraCols([...extraCols, k]); }} />
              {/* v0.10.143 (DETAY SAYFALARI adım 6) — Kubernetes kolon seti tek tıkla;
                  yalnız entity katmanı açıkken (hücreler entity sayfalarına link olur). */}
              {k8sOn && canAddK8sColumns(extraCols) && (
                <Button type="button" variant="ghost" size="xs" title="k8s.namespace.name · k8s.pod.name · k8s.node.name · cluster kolonlarını ekle (8 kolon tavanı)"
                  onClick={() => setExtraCols(withK8sColumns(extraCols))}>+ K8s columns</Button>
              )}
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
            {/* v0.10.216 (operatör-bildirimli: "trace'e orta tuşla basınca
                yeni sekmede açılsın; frame içinde olmasın, sayfaya entegre
                olsun") — SATIR = LİNK. Her hücre aynı /trace href'ini taşıyan
                bir <Link className="row-link">; orta tık / ⌘-tık / sağ tık
                "yeni sekmede aç" tarayıcının kendi davranışı, JS'te navigate
                yok (eski `td onClick → navigate` DOM'a <a> basmadığı için
                orta tık ölüydü). ▸ ön-izleme kolonu ve satır-altı
                MiniWaterfall kutusu — v0.9.645'in "iç çerçeve yok" ilkesinin
                yarım kalan yarısı — silindi; sol tık tam-sayfa /trace'e gider.
                Klavye yolu aynen: j/k + Enter → useDataTable onOpen
                (openTrace). K8s entity hücreleri KENDİ linklerini taşır —
                <a> içinde <a> geçersiz HTML, o hücreler satır linkine
                sarılmaz (ownLink). */}
            <VirtualTable<TraceRow>
              dt={dt}
              height={44 + displayRows.length * 36}
              rowHeight={36}
              getRowKey={(t) => t.traceId}
              renderRow={(t) => {
                const href = traceHref(t.traceId, { pageRange: range });
                return (
                  <Fragment>
                    {colIds.map(id => {
                      // v0.10.330 — K8s hücreleri artık düz metin (kendi <a>'sı yok) → satır
                      // linkine diğer hücreler gibi sarılır; ölü hücre kalmaz.
                      const ownLink = false;
                      const cell = renderTraceCell(id, t, visibleMax, k8sOn ? { clusters: entityClusters, range } : undefined);
                      return (
                        <td key={id} onMouseEnter={() => prefetchTrace(t.traceId)}
                          className={ownLink ? undefined : 'row-cell'}
                          style={{ background: t.hasError ? 'color-mix(in srgb, var(--err) 8%, transparent)' : undefined }}>
                          {ownLink ? cell : <Link to={href} state={{ from: loc.pathname + loc.search }} className={id === 'operation' ? 'row-link row-link--name' : 'row-link'}>{cell}</Link>}
                        </td>
                      );
                    })}
                  </Fragment>
                );
              }}
            />
            {/* v0.9.638 — total Pager'a GEÇMİYOR. Pager lastPage/atEnd'i
                total'dan türetiyor; tavanlı bir sayı verirsek operatörü
                listenin ULAŞAMAYACAĞI sayfalara yollar (aşama-1 kimlik
                bütçesi 5.000-6.000). Gezinme hasMore üzerinde kalıyor;
                sayı yalnız bir etiket. */}
            {/* v0.9.645 — Pager sayfanın dibine yapışıyor (uzun listede
                "Next" ekran dışında kalıyordu) ve sayı hem KESİN hem
                sunulabilir tavanın içindeyse "Last" çiziliyor.
                Gerekçe + sınır: lib/traceReach.ts */}
            {/* v0.9.1014 — count="skip": Pager'a total GEÇMİYOR (yukarıdaki
                v0.9.638 gerekçesi). Sayı `extras` içinde bir ETİKET olarak
                yaşıyor ve tavanlı olduğunda "+" ile öyle olduğunu söylüyor;
                gezinme hasMore + lastReachablePage üzerinde. Artık bu bir
                yorum değil, tipin kendisi: 'skip' `total`ı YASAKLIYOR. */}
            <Pager mode="offset" count="skip"
              page={page} pageSize={50} hasMore={hasMore} onPage={setPage}
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
                  {/* v0.10.124 — MV boşluğu: ham yoldan okundu, dürüstçe söyle. */}
                  {data?.mvGap && (
                    <span style={{ marginLeft: 8, color: 'var(--warn, #b8860b)' }}
                      title="Bu pencerede özet tablo (trace_summary_5m) boş bir güne değiyor — liste ham span'lerden okundu (daha yavaş). Sistem → ClickHouse → tarihçe geri doldurma o günü doldurunca hızlı yola dönülür.">
                      · MV boşluğu: ham yol
                    </span>
                  )}
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
                  {/* v0.10.342 — kimlik-önce arama tuttu: liste o trace'ler. */}
                  {data?.identity?.hits ? (
                    <span className="badge b-info" style={{ marginLeft: 6, textTransform: 'none', letterSpacing: 0 }}
                      title={`Arama terimi kimlik olarak eşleşti: ${data.identity.matchedKey ?? '?'} = ${filter.search} · ${data.identity.hits.toLocaleString()} trace${data.identity.bounded ? ' (tavan)' : ''}. Denenen anahtarlar: ${data.identity.keys.join(', ')}`}>
                      kimlik: {data.identity.matchedKey ?? '?'}{data.identity.traceId ? '' : ` (${data.identity.hits.toLocaleString()})`}
                      {data.identity.anchorMs ? ` · kimlikteki zaman ${tsLong(data.identity.anchorMs * 1e6)} ±12 s` : ''}
                    </span>
                  ) : null}
                  {data?.rankedWithinRecent ? (
                    <span title={sort === 'time'
                      /* v0.10.265 — servisli + filtreli (hata/kök/süre) liste en yeni N trace içinde süzülür. */
                      ? `For speed, the filter is applied within the newest ${data.rankedWithinRecent.toLocaleString()} traces of the service — narrow the range to reach older ones.`
                      : `For speed, ${sort} ranks the newest ${data.rankedWithinRecent.toLocaleString()} traces in the window — an older trace beyond that slice won't appear. Sort by time for the full window.`}
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
      </PageShell>
    </>
  );
}

// Per-column cell content for a trace row. k8s: v0.10.143 — entity katmanı
// açıkken Kubernetes kolonları entity sayfalarına link (traceK8sLinks).
function renderTraceCell(id: string, t: TraceRow, visibleMax: number, k8s?: { clusters: EntityClusterInfo[]; range: TimeRange }) {
  if (k8s && isTraceK8sCol(id)) {
    const v = t.extras?.[id] ?? '';
    if (!v) return <span className="mono" style={{ color: 'var(--text3)' }}>—</span>;
    // v0.10.330 (operatör, prod): "pod ismi / cluster seçilebilir olmasına
    // gerek yok; çok satır olduğu için sayfada taşıyor". v0.10.143'ün
    // entity-sayfası çipi (a.sec: kutu + 5×14 px dolgu + 13 px) hücreyi
    // iki-üç satıra sarıp tabloyu taşırıyordu. Artık DÜZ tek satır metin,
    // taşan kısım üç nokta, tam değer title'da; entity pivotu trace
    // sayfasında ve satır linkinde duruyor. traceK8sHref korunur (pivot
    // kaynağı başka yüzeylerde), burada çağrılmaz.
    return <span className="mono cell-ellipsis" style={{ color: 'var(--text2)' }} title={v}>{v}</span>;
  }
  switch (id) {
    case 'time':      return <span className="mono">{tsDateTime(t.startTime)}</span>;
    case 'service':   return <SvcBadge name={t.serviceName} />;
    case 'operation': return <span title={t.rootName}>{t.rootName || '—'}</span>;
    case 'duration':  return <DurationBar ms={t.durationMs} err={t.hasError} max={visibleMax} />;
    case 'spans':     return <>{t.spanCount}</>;
    // v0.10.218 (D3) — hata rozetinin yanında hatalı span SAYISI (Dynatrace
    // düzeni); alan yoksa (0 ya da eski önbellek yanıtı) yalnız rozet.
    case 'status':    return t.hasError
      ? <><span className="badge b-err">ERROR</span>{t.errorSpans ? <span className="cell-hint">{t.errorSpans} span</span> : null}</>
      : <span className="badge b-ok">OK</span>;
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
                <tr key={`${a.groupKey}|${a.groupExtra}`} {...rowActivation(() => onDrill(a))} style={{ cursor: 'pointer' }}>
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
function TracesEmpty({ service, search, range, onSwitchView, narrowedFromNs, explainHref, matchingSpans, promotedDiag, identity }: {
  service: string; search: string; range: TimeRange; onSwitchView: () => void;
  // v0.10.339 — terfi kolonu probu (host başına kolon/dizi sayımı) — boş kaldıysa da göster.
  promotedDiag?: TracesResponse['emptyDiag'];
  // v0.10.342 — kimlik-önce arama denendiyse (tek parçalı terim) sonucu.
  identity?: TracesResponse['identity'];
  // v0.10.307 — arka uç pencereyi daralttıysa (kaynak bütçesi) "yok" değil "bakılamadı".
  narrowedFromNs?: number;
  // v0.10.328 — admin: aynı sorgunun ?explain=1 çıktısı (yol + SQL + süre) yeni sekmede.
  explainHref?: string;
  // v0.10.329 — sunucu öz-teşhisi: aynı filtreyle eşleşen span sayısı (boş listede).
  matchingSpans?: number;
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
    <Empty icon="⋮" title={narrowedFromNs ? 'No traces in the shortened window' : 'No traces found'}>
      <div style={{ marginTop: 6, color: 'var(--text2)' }}>
        {narrowedFromNs ? (
          <>
            {/* v0.10.307 — Operator-reported: "3 saat seçince no traces". Sorgu kaynak
                sınırına takılınca arka uç pencereyi kısaltır; bu boş sonuç "yok" değil
                "bakılamadı"dır ve öyle söylenir. */}
            The query hit its resource budget, so the backend only searched from <b>{tsLong(narrowedFromNs)}</b> onward — and found nothing there.
            Older traces in your range were <b>not</b> searched. Narrow the range (e.g. 1h) or simplify the filter; do not read this as "no matching traces".
          </>
        ) : aged ? (
          <>
            <b style={{ color: 'var(--warn)' }}>{mvSpans!.toLocaleString()}</b> spans recorded for <code>{service}</code> in this window via the 5-min MV, but no raw spans match the search. This usually means the span data aged out past the raw-spans TTL while the MV still holds the rollup.{' '}
            <Button variant="secondary" size="sm" onClick={onSwitchView} style={{ marginLeft: 4 }}>Switch to Aggregate view →</Button>
          </>
        ) : (
          <>Try widening the time range, dropping the service or search filter, or turning off the "Root traces" chip. If even an unfiltered query is empty, check ingest health at <Link to="/system/stats" style={{ color: 'var(--accent2)' }}>system stats</Link>.</>
        )}
      </div>
      {identity && (
        <div style={{ marginTop: 10, fontSize: 11, color: 'var(--text3)' }}>
          Kimlik araması: <span className="mono">{search}</span> şu anahtarlarda eşitlikle denendi — {identity.keys.join(', ') || '—'}:{' '}
          {identity.error
            ? <span style={{ color: 'var(--err)' }}>hata: {identity.error}</span>
            : identity.hits > 0 ? `${identity.hits} trace (${identity.matchedKey})` : 'eşleşme yok; alt-dize araması da boş döndü'}.
          {(identity.skipped?.length ?? 0) > 0 && <> Derlenemeyen yazımlar: {identity.skipped!.join(', ')}.</>}
          {identity.anchorMs
            ? <> Kimliğin içindeki zaman <b>{tsLong(identity.anchorMs * 1e6)}</b> (UTC okundu) çapa alındı: ±12 saat penceresinde arandı, seçili aralık kullanılmadı; bulunamadığı için alt-dize taraması yapılmadı. Değerin doğru olduğundan ve verinin saklama süresi içinde olduğundan emin ol.</>
            : <> Çapa yok (değer yyyyMMddHHmmss taşımıyor): seçili aralıkta arandı.</>}
        </div>
      )}
      {promotedDiag?.promotedKeys && (
        <div style={{ marginTop: 10, fontSize: 11, color: 'var(--text3)' }}>
          Terfi kolonu probu ({promotedDiag.promotedKeys.join(', ')}):{' '}
          {promotedDiag.promotedProbeError
            ? <span style={{ color: 'var(--err)' }}>{promotedDiag.promotedProbeError}</span>
            : (promotedDiag.promotedHosts ?? []).length === 0
              ? 'hiçbir host satır döndürmedi'
              : (promotedDiag.promotedHosts ?? []).map(h => `${h.host}: kolon ${h.col.toLocaleString()} / dizi ${h.arr.toLocaleString()}`).join(' · ')}
          {promotedDiag.promotedFallbackError && <> · dizi yolu da başarısız: <span style={{ color: 'var(--err)' }}>{promotedDiag.promotedFallbackError}</span></>}
        </div>
      )}
      {matchingSpans !== undefined && (
        <div style={{ marginTop: 10, fontSize: 11, color: matchingSpans > 0 ? 'var(--warn)' : 'var(--text3)' }}>
          {matchingSpans > 0
            ? <>Diagnostic: <b>{matchingSpans.toLocaleString()}</b> spans in this window match the filter, yet the trace list came back empty — the backend logged the query plan (<span className="mono">[traces] EMPTY list</span>).</>
            : <>Diagnostic: nothing in this window matches search + filters (trace-level: a span matching the search and spans matching each chip, anywhere in the trace) — the data or the predicate, not the list query.</>}
        </div>
      )}
      {explainHref && (
        <div style={{ marginTop: 10, fontSize: 11 }}>
          <a href={explainHref} target="_blank" rel="noreferrer" title="Aynı sorgunun teşhisi: seçilen yol, her ClickHouse adımı, süre, satır (yalnız admin)">
            Teşhis (explain) →
          </a>
        </div>
      )}
    </Empty>
  );
}
