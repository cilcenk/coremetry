import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { Zap, ChevronRight, ChevronDown } from 'lucide-react';
import { Topbar } from '@/components/Topbar';
import { Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { ServicePicker } from '@/components/ServicePicker';
import { Sparkline } from '@/components/Sparkline';
import { api } from '@/lib/api';
import { useEndpoints, useClusters } from '@/lib/queries';
import { timeRangeToNs, rangeToSince, fmtNum } from '@/lib/utils';
import { encodeRange } from '@/lib/urlState';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { usePageZoomRange } from '@/lib/chart/usePageZoomRange';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { stickyLeftOffsets } from '@/lib/dataTable';
import { TrendDelta } from '@/components/TrendDelta';
import { endpointDetailHref, legacyEndpointTarget } from '@/pages/endpoints/endpointParam';
import { tracesLink } from '@/pages/endpoints/links';
import { serviceHref } from '@/lib/serviceHref';
import { parseColsParam, formatColsParam } from '@/pages/endpoints/endpointCols';
import { ColumnToggle } from '@/pages/endpoints/ColumnToggle';
import {
  EXP_PARAM, LIST_NEW_LABEL, LIST_NEW_TITLE,
  decodeExpandedParam, decodeEndpointRowKey, encodeExpandedParam,
  endpointP99Delta, endpointRowKey, endpointsSourceNote, listedTotals,
  toggleExpanded,
} from '@/lib/endpointHonesty';
import { SearchField } from '@/components/ui/SearchField';
import type { DataTableColumn } from '@/lib/dataTable';
import type { EndpointRow } from '@/lib/types';
import { traceHref } from '@/lib/traceHref';
import { PageControls } from '@/components/ui/PageControls';
import { Button } from '@/components/ui/Button';
import { IconButton } from '@/components/ui/IconButton';
import { PageShell } from '@/components/ui/PageShell';

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
  { id: 'service',   label: 'Service',    sortValue: r => r.service,   naturalDir: 'asc', width: 150, stickyLeft: true },
  // v0.9.313 — the header reads "Path" on the HTTP surface and
  // "Operation" on the RPC one: the column carries http.route there and
  // the span NAME here, and calling a gRPC method a "path" would be a
  // small lie repeated on every row.
  { id: 'path',      label: 'Path',       sortValue: r => r.path,      naturalDir: 'asc', width: 260, stickyLeft: true },
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
  // v0.9.761 (mockup: "kötüleşenler önce") — P99 kötüleşme yüzdesi;
  // sunucu sıralaması aday-havuzu desenli (çağrıya göre ilk ~1000
  // içinde en çok kötüleşenler; not tabloda).
  { id: 'p99Delta',  label: 'P99 Δ',      sortValue: r => (r.priorP99Ms ?? 0) > 0 ? (r.p99Ms - (r.priorP99Ms ?? 0)) / (r.priorP99Ms ?? 1) : -1, numeric: true, width: 84 },
  // v0.9.305 — Spread = p99/p50. Purely derived, zero query cost, and
  // it answers a question no single percentile does: is this endpoint
  // slow for EVERYONE (spread near 1, the whole distribution moved) or
  // does it have a heavy TAIL (spread high, most calls fine)? Those two
  // have different causes and different fixes.
  { id: 'spread',    label: 'Spread',     sortValue: spreadOf, numeric: true, width: 76 },
  { id: 'trend',     label: 'Trend',      width: 104 },
  // v0.8.573 — pinned to the right edge: the 14-column table overflows
  // laptop widths and the horizontal scrollbar sits below 2000 rows, so
  // the trailing drill-through was effectively invisible (operator
  // report). 64px also clipped "view →" (content+padding ≈ 66px).
  // v0.9.310 — widened from 76: the cell now carries the two exemplar
  // jumps beside "view →". 76px was already the fix for clipping that
  // one link (v0.8.573), so three affordances need the room.
  { id: 'traces',    label: 'Traces',     width: 118, stickyRight: true },
  { id: 'impact',    label: 'Impact',     sortValue: impactOf, headerHidden: true },
];

// Sort ids the backend ORDER BY whitelist accepts — anything else
// (stale localStorage from the pre-v0.8.356 schema, hand-edited
// URLs) falls back to calls DESC before it reaches the fetch.
const SORT_KEYS = [
  'service', 'path', 'calls', 'errors', 'errorRate',
  'avgMs', 'p50Ms', 'p90Ms', 'p95Ms', 'p99Ms', 'reqPerMin', 'impact',
  'p99Delta', // v0.9.761 — sunucuda aday-havuzu sıralaması
] as const;
// v0.9.761 (mockup onayı) — varsayılan sıralama "kötüleşenler önce":
// sayfayı açma sebebi neredeyse hep "ne bozuldu". Hacim bir tık uzakta.
const DEFAULT_ENDPOINTS_SORT = { id: 'p99Delta', dir: 'desc' as const };

// Visible-column universe for the ?cols= codec (v0.8.574) — every
// rendered column; headerHidden `impact` is sort-only and can't be
// hidden (it has no header to hide).
const ALL_COL_IDS = ENDPOINT_COLS.filter(c => !c.headerHidden).map(c => c.id);

// v0.9.642 — VARSAYILAN kolon kümesi (operatör-bildirimli: "Endpoints
// tablosunda çok kolon olduğu için içerik sızıyor").
//
// 16 kolon ~1664px sürüyor; tipik dizüstünde içerik alanı ~1180px, yani
// tablo yatay kayıyordu. Daraltma HİÇBİR ŞEY SİLMİYOR — gizlenenler
// ColumnManager'da bir tık ötede ve ?cols= ile hâlâ adreslenebilir,
// sıralama da çalışmaya devam ediyor (serverSort görünürlükten bağımsız).
//
// Varsayılan dışı bırakılanlar ve gerekçeleri:
//   avgMs      P50/P95/P99 varken en az bilgilendirici olan; ortalama
//              uç değerlerden sapıyor
//   p90Ms      dört persentil bir fazla — P50 ortayı, P95/P99 kuyruğu
//   spread     türetilmiş (p99/p50); değerli ama varsayılan olmak zorunda değil
//   errors     mutlak sayı artık Error rate hücresinde ("%0,4 · 12")
//   reqPerMin  Calls hücresinde ("1,2M · 340/dk")
const DEFAULT_COL_IDS = ALL_COL_IDS.filter(
  id => !['avgMs', 'p90Ms', 'spread', 'errors', 'reqPerMin'].includes(id));

export default function EndpointsPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  // Global time window (UX#2) — URL-persisted + carried across pages.
  // v0.9.429 — zoom-yığını paylaşılan usePageZoomRange hook'unda
  // (üstelik handler'lar artık useCallback-stabil — buradaki inline
  // kopya panel alt-ağacını her render'da yeniden çiziyordu).
  const { range, setRange } = usePageZoomRange('30m');
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
  // compare (v0.5.404 → v0.9.761): prior-window comparison artık
  // VARSAYILAN AÇIK (mockup onayı: Δ chip'leri ilk bakışta). ?compare=0
  // kapatır; eski ?compare=prior linkleri de "açık" okur. Maliyet notu:
  // prior taraması 30s cache arkasında.
  const compare = params.get('compare') !== '0';
  const setCompare = (v: boolean) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.delete('compare'); else next.set('compare', '0');
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
  // v0.9.313 (brief N1) — which inbound surface the table lists.
  // In the URL (replace:true) so a saved view and a copied link come
  // back on the same tab; one-way reads are the recurring bug class
  // here (v0.8.253/256/265/267).
  const entry: 'http' | 'rpc' = params.get('entry') === 'rpc' ? 'rpc' : 'http';
  const setEntry = (v: 'http' | 'rpc') => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v === 'rpc') next.set('entry', 'rpc'); else next.delete('entry');
    return next;
  }, { replace: true });

  // v0.5.406 — row expansion + per-service dependency cache.
  // Clicking the "▶" chevron on a row reveals a strip showing
  // which services this endpoint's service typically calls
  // downstream. Cached per-service so expanding 3 endpoints of
  // the same service hits the network once.
  //
  // v0.9.818 — açık satırlar URL'de (?exp=). Bunlar ekranda DURAN bir
  // seçimdi ama yalnız bileşen state'inde yaşıyordu: kopyalanan link
  // şeritleri kapalı açıyor, SavedViewsBar da kapalı bir görünüm
  // kaydediyordu — yani kaydedilen şey ekrandaki şey değildi (v0.8.253
  // sınıfı). YEREL AYNA YOK: okuma doğrudan params'tan, yazma
  // replace:true, dolayısıyla sig-guard'a da gerek yok.
  const expandedRows = useMemo(
    () => decodeExpandedParam(params.get(EXP_PARAM)), [params]);
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
  const onToggleExpand = (rowKey: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    const v = encodeExpandedParam(toggleExpanded(decodeExpandedParam(prev.get(EXP_PARAM)), rowKey));
    if (v) next.set(EXP_PARAM, v); else next.delete(EXP_PARAM);
    return next;
  }, { replace: true });

  // v0.9.818 — bağımlılık getirmesi ARTIK tıka değil, AÇIK KÜMEYE bağlı.
  // Tıka bağlıyken ?exp= taşıyan bir derin link şeritleri açıyor ama
  // hiçbirini doldurmuyordu: kalıcı "Loading …" — kapalıdan beter, çünkü
  // bir şey geleceğini vaat ediyor. Efekt hem tıkı hem derin-linki hem de
  // pencere değişimini (dep.since anahtarın parçası) tek yoldan karşılar.
  //
  // v0.9.257 — önbellek anahtarı (service, window); yalnız service olsaydı
  // operatörün İLK açtığı pencere o servise oturur, range değişince diğer
  // paneller tazelenirken bu şerit eski sayıları göstermeye devam ederdi.
  useEffect(() => {
    const wanted = new Set<string>();
    for (const key of expandedRows) {
      const ref = decodeEndpointRowKey(key);
      if (ref) wanted.add(ref.service);
    }
    for (const svc of wanted) {
      const ck = `${svc}@${dep.since}`;
      if (depsByService[ck]) continue;
      api.serviceNeighbors(svc, dep.since, 100, false)
        .then(r => setDepsByService(prev => prev[ck] ? prev : ({
          ...prev, [ck]: { deps: r?.downstream ?? [], sampledFrom: r?.sampledFrom ?? 0 },
        })))
        .catch(() => setDepsByService(prev => prev[ck] ? prev : ({
          ...prev, [ck]: { deps: [], sampledFrom: 0 },
        })));
    }
  }, [expandedRows, dep.since, depsByService]);
  // v0.9.839 — ÇEKMECE VE MODAL EMEKLİ. Satır tıkı artık /endpoint TAM
  // SAYFASINA gidiyor (operatör kararı 2026-08-09). İki kaplama görünüm
  // (620px çekmece + sparkline modalı) aynı endpoint'in hikâyesini ikiye
  // bölüyordu, ikisi aynı anda açılamıyordu ve hiçbirinde tabloya yer
  // yoktu. Sayfa hepsini tam genişlikte taşıyor.
  //
  // ESKİ DERİN LİNKLER (?endpoint= / ?detail=) yeni sayfaya yönlenir:
  // saved_views satırları ve postmortem'lere yapıştırılmış bağlantılar
  // kırılmasın. Bu bir GERİYE-UYUM ŞİMİ DEĞİL — arkasında eski yüzeyler
  // yok, tek yönlendirme efekti var ve bu paramları artık kimse YAZMIYOR.
  useEffect(() => {
    const target = legacyEndpointTarget(params.toString());
    if (target) navigate(target, { replace: true });
  }, [params, navigate]);

  // openEndpointPage — satır tıkının, klavye Enter/o'sunun ve sparkline
  // tıkının TEK hedefi. Kapsam (range/env/cluster/compare/entry) pivotla
  // birlikte gider: bir satır env=uat altında okunduysa açılan sayfa da
  // aynı soruyu sormalı (v0.9.307 kuralı).
  const openEndpointPage = (r: EndpointRow) => navigate(endpointDetailHref(
    { service: r.service, path: r.path, sig: bySignature },
    {
      range: encodeRange(range),
      env: env || undefined,
      cluster: cluster || undefined,
      compare,
      entry: entry === 'rpc' ? 'rpc' : undefined,
    },
  ));

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
    () => parseColsParam(params.get('cols'), ALL_COL_IDS, DEFAULT_COL_IDS),
    [params]);
  const setVisibleCols = (s: Set<string>) => setParams(prev => {
    const next = new URLSearchParams(prev);
    const v = formatColsParam(s, ALL_COL_IDS, DEFAULT_COL_IDS);
    if (v) next.set('cols', v); else next.delete('cols');
    return next;
  }, { replace: true });
  // The hook sees only visible columns (+ the sort-only headerHidden
  // impact) so DataTableColgroup/Head stay aligned with the body cells.
  // Sorting by a now-hidden column keeps working: serverSort forwards
  // the persisted sort id to the fetch regardless of visibility —
  // same contract as the never-rendered impact column.
  const visibleColumns = useMemo(
    () => ENDPOINT_COLS
      .filter(c => c.headerHidden || visibleCols.has(c.id))
      // v0.9.642 — tek servise filtreliyken Service kolonu her satırda
      // AYNI değeri taşıyor; bilgi zaten filtrenin kendisinde duruyor.
      // 150px, tablonun en geniş ikinci kolonu.
      .filter(c => !(c.id === 'service' && service !== ''))
      // v0.9.313 — "Path" is http.route; on the RPC surface the same
      // column carries the span NAME, and calling a gRPC method a path
      // would be a small lie repeated on every row.
      .map(c => (c.id === 'path' && entry === 'rpc' ? { ...c, label: 'Operation' } : c)),
    [visibleCols, entry, service]);
  // onOpen + searchRef wire the app-wide keyboard nav: j/k select a
  // row, Enter/o open its service detail, "/" focuses the path filter.
  const dt = useDataTable<EndpointRow>({
    storageKey: 'endpoints',
    columns: visibleColumns,
    rows: tableRows,
    serverSort: true,
    initialSort: DEFAULT_ENDPOINTS_SORT,
    // v0.9.839 — Enter/o goes where the CLICK goes. It used to open the
    // row's SERVICE, so the mouse and the keyboard answered two
    // different questions from the same selected row; the drill-down
    // now has a page of its own and both affordances land on it.
    onOpen: openEndpointPage,
    searchRef,
  });
  // v0.9.1256 — sola sabit kimlik kolonlarının kümülatif ofsetleri
  // (görünür kolonlar + resize'lı genişliklerle; saf çekirdek).
  const leftOffs = stickyLeftOffsets(dt.visibleColumns, dt.colWidths, 120);

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
    entry: entry === 'rpc' ? 'rpc' : undefined, // v0.9.313
  });
  // v0.9.812 — zarf: satırlar + sıralama havuzunun gerçeği.
  // v0.9.818 — MEMO. Üçlü koşul her render'da YENİ bir dizi kimliği
  // üretiyordu; `rows`'a bağlı her useMemo (KPI toplamları) fiilen her
  // render'da yeniden koşardı. Kimlik artık yalnız sorgu durumu
  // değiştiğinde değişir.
  const rows: EndpointRow[] | null | undefined = useMemo(
    () => (rowsQ.isPending ? undefined : rowsQ.isError ? null : rowsQ.data?.rows ?? []),
    [rowsQ.isPending, rowsQ.isError, rowsQ.data]);
  const pool = rowsQ.data?.pool ?? 0;
  const poolCapped = rowsQ.data?.poolCapped === true;
  useEffect(() => { setTableRows(rowsQ.data?.rows ?? []); }, [rowsQ.data]);

  // Cluster picker options — mirror Services page so symmetry is
  // intuitive for operators landing here after filtering there.
  const clustersQ = useClusters(from, to);
  const clusterOptions = clustersQ.data ?? [];

  // v0.9.818 — KPI'lar GÖRÜNEN satırların toplamı ve etiketleri artık
  // bunu söylüyor. Sayılar zaten hep buydu; yalanı etiket söylüyordu
  // ("Total calls" = filo iddiası, oysa okuma top-N + filtreli).
  const listed = useMemo(() => listedTotals(rows ?? []), [rows]);
  // Kaynak notu: env/cluster seçiliyken okuma MV'den ham spans'e düşer
  // ve POPÜLASYON değişir (endpointHonesty.endpointsSourceNote).
  const sourceNote = useMemo(() => endpointsSourceNote(cluster, env), [cluster, env]);

  return (
    <>
      <Topbar title="Endpoints" range={range} onRangeChange={setRange} envApplies />
      <PageShell>
        {/* v0.9.308 (brief N6c) — the page's whole selection already
            lives in the URL: service, search, cluster, limit, compare,
            shape, ?cols=, ?endpoint= and the s_endpoints sort pair.
            Two comments in this file name SavedViewsBar as the REASON
            that was done — the bar itself was simply never mounted.
            Persistence rides the existing saved_views(page='…') table,
            so this is one import and one line: no new schema, no new
            endpoint, no new state. */}
        <SavedViewsBar page="endpoints" />
        {/* v0.9.313 (brief N1) — the table filtered `http_route != ''`
            from day one, so gRPC servers and queue consumers were not
            merely unlisted, they were INVISIBLE and nothing said so.
            An operator read "these are all my entry points" and got an
            incomplete truth. Two tabs, one MV: `name` and `kind` were
            already dimensions on it, they simply never reached the
            projection. */}
        <div className="tab-strip" style={{ marginBottom: 10, fontSize: 12 }}>
          <button className={entry === 'http' ? 'active' : ''}
            onClick={() => setEntry('http')}
            title="Inbound requests carrying an http.route — the table as it has always been.">
            HTTP
          </button>
          <button className={entry === 'rpc' ? 'active' : ''}
            onClick={() => setEntry('rpc')}
            title="Inbound spans WITHOUT an http.route: gRPC servers and message consumers, keyed on the span name. These never appeared on this page before.">
            RPC &amp; Messaging
          </button>
        </div>
        <PageControls sticky>
          <ServicePicker value={service} onChange={setService}
            placeholder="Filter by service…" width={200} />
          {/* v0.9.1004 (M2/O5) — dar ekranda yüzeyde kalan kontrol
              sayfanın araması olsun; işaretsizken ServicePicker
              seçiliyor ve yol filtresi popover'ın arkasına düşüyordu. */}
          <SearchField ref={searchRef} value={search} data-pc-lead
            onChange={v => { setSearch(v); setSearchParam(v); }}
            placeholder="Filter by path (substring)…"
            aria-label="Filter endpoints by path"
            hint="/" width={280} />
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
                {/* v0.9.812 — havuz boyutu artık SUNUCUDAN gelir. Sabit
                    "~1000" metni v0.9.762'den beri yanlıştı: havuz limit
                    ile birlikte büyüyor. */}
                {sortBy === 'p99Delta' && pool > 0 && (
                  <span style={{ color: 'var(--text3)', marginLeft: 6 }}
                        title={`Kötüleşme sıralaması, çağrıya göre ilk ${pool} endpoint içinde hesaplanır — düşük hacimli uç noktalar havuz dışında kalabilir`}>
                    (aday havuzu: en çok çağrılan {fmtNum(pool)})
                  </span>
                )}
                {(rows.length >= limit || poolCapped) && (
                  <span style={{ color: 'var(--warn)', marginLeft: 6 }}
                        title={poolCapped
                          ? `Sıralama havuzu ${pool} aday ile sınırlı — havuz doldu, dışında kalan endpoint'ler var`
                          : 'Result hit the limit — long-tail endpoints may be hidden'}>
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
          <Button size="sm"
            variant={dt.sort.id === 'impact' ? 'accent' : 'secondary'}
            aria-pressed={dt.sort.id === 'impact'}
            onClick={() => dt.setSort({ id: 'impact', dir: 'desc' })}
            title="Sort by composite impact (calls × p99 × (1+errorRate)) — fix-me-first list"
            leftIcon={<Zap size={12} strokeWidth={1.75} />}>
            Worst by impact
          </Button>
          <ColumnToggle
            columns={ENDPOINT_COLS.filter(c => !c.headerHidden).map(c => ({ id: c.id, label: c.label }))}
            visible={visibleCols}
            onChange={setVisibleCols} />
        </PageControls>

        {/* v0.9.812 — sıralama havuzu şeridi. "Kötüleşenler önce"
            sıralaması delta'yı SQL'de hesaplayamaz: sunucu çağrıya göre
            ilk `pool` adayı çeker, prior'la birleştirir, delta'ya göre
            sıralar. Havuz dolduysa evrenin dışında kalan endpoint'ler
            VARDIR ve bunu söylemeyen bir liste "en çok kötüleşenler
            bunlar" diye okunur. Küçük ipucu metni her zaman görünür;
            bu şerit yalnız havuz gerçekten dolduğunda çıkar. */}
        {poolCapped && (
          <div style={{
            padding: '8px 12px', marginBottom: 12, fontSize: 12,
            border: '1px solid var(--border)', borderLeft: '3px solid var(--warn)',
            borderRadius: 6, background: 'var(--bg2)', color: 'var(--text2)',
            lineHeight: 1.5,
          }}>
            Sıralama havuzu <b>{fmtNum(pool)}</b> aday ile sınırlı — liste eksik
            olabilir. Kötüleşme sıralaması, pencerede en çok çağrılan ilk{' '}
            {fmtNum(pool)} endpoint içinde hesaplanır; daha düşük hacimli bir uç
            nokta kötüleşse bile bu tabloya giremez. Daralt (servis / yol filtresi)
            ya da başka bir kolona göre sırala.
          </div>
        )}

        {rows === undefined && <TableSkeleton cols={8} wideFirst />}
        {rows === null && (
          <Empty icon="⚠" title="Failed to load endpoints">
            {/* v0.9.313 — the RPC surface needs the spanmetrics MV,
                which a cluster or env filter disables. The backend
                refuses that combination EXPLICITLY rather than running
                the HTTP raw query and rendering an empty table under an
                "RPC & Messaging" heading — "you have no gRPC entry
                points" when the truth is "this cannot be answered".
                Surface the reason here so the refusal is actionable. */}
            {entry === 'rpc' && (cluster || env)
              ? <>The <b>RPC &amp; Messaging</b> tab needs the spanmetrics rollups, which the
                  {cluster ? ' cluster' : ''}{cluster && env ? ' and' : ''}{env ? ' env' : ''} filter
                  disables. Clear it to list non-HTTP entry points, or switch back to HTTP.</>
              : 'The backend /api/endpoints request errored.'}
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
            {/* v0.9.834 — v0.9.818/819'un KPI şeridi + üç grafiği
                KALDIRILDI (operatör: bu metrikler gereksiz). Sayfa
                yeniden tablo-önce: aşağıdaki "listelenen toplam" satırı
                ve satır içi sparkline'lar duruyor, /api/endpoints/series
                ve okuyucusu ise silindi. */}
            {/* Listelenen çağrı / hata — KAPSAM SADECE GÖRÜNEN SATIRLAR
                (limit dışı endpoint'ler burada YOK). Cümle bunu açıkça
                "Listelenen N satırın toplamı" diye kuruyor: v0.9.818'de
                kapattığımız kapsam yalanı, tam da bu sayıyı filo geneli
                gibi okutmaktı. */}
            <div style={{ marginBottom: 12, fontSize: 11.5, color: 'var(--text3)' }}
              title={`Oran pencere TOPLAMLARINDAN: ${fmtNum(listed.errors)} hata / ${fmtNum(listed.calls)} çağrı. Satır oranlarının ortalaması DEĞİL.`}>
              Listelenen {fmtNum(listed.rows)} satırın toplamı:{' '}
              <b style={{ color: 'var(--text2)' }}>{fmtNum(listed.calls)}</b> çağrı ·{' '}
              <b style={{
                color: listed.errorRate >= 5 ? 'var(--err)'
                  : listed.errorRate >= 1 ? 'var(--warn)' : 'var(--text2)',
              }}>{fmtNum(listed.errors)}</b> hata ({listed.errorRate.toFixed(2)}%)
            </div>
            <div className="table-wrap">
              <table style={{ tableLayout: 'fixed', width: '100%' }}>
                <DataTableColgroup dt={dt} leading={[22]} />
                <DataTableHead dt={dt} leading={<th style={{ width: 22 }} />} />
                <tbody>
                  {dt.sortedRows.map((r, i) => {
                    const errCls = r.errorRate >= 5 ? 'b-err' : r.errorRate >= 1 ? 'b-warn' : 'b-ok';
                    // v0.9.818 — genişletme anahtarı artık KARARLI ve
                    // URL-güvenli: eskiden satır INDEKSİNİ taşıyordu, yani
                    // sıralama/filtre değişince açık şerit başka bir satıra
                    // atlıyordu (ve URL'e yazılamazdı). Kimlik MV grenidir:
                    // (service, path).
                    const rowKey = endpointRowKey(r.service, r.path);
                    const isExpanded = expandedRows.has(rowKey);
                    return (
                      <React.Fragment key={`${rowKey}|${i}`}>
                      <tr {...dt.rowProps(i)}
                        onMouseEnter={() => dt.nav.setSelected(i)}
                        onClick={e => {
                          // v0.9.839 — row click NAVIGATES to the full
                          // endpoint page (was: the 620px drawer).
                          // Links / buttons inside the row (service
                          // link, expander, sparkline, traces →) keep
                          // their own affordances.
                          if ((e.target as HTMLElement).closest('a, button')) return;
                          openEndpointPage(r);
                        }}
                        title="Open the endpoint detail page (RED series, latency distribution, callers, failing traces)"
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
                          <IconButton
                            variant="bare" size="xs"
                            onClick={() => onToggleExpand(rowKey)}
                            // v0.9.887 — `aria-expanded` ilk kez burada: bu
                            // bir açılır-kapanır tetik ve ekran okuyucu
                            // bugüne dek durumunu HİÇ duyurmuyordu.
                            aria-expanded={isExpanded}
                            aria-label={isExpanded
                              ? 'Hide downstream dependencies'
                              : 'Show downstream dependencies'}
                            title={isExpanded
                              ? 'Hide downstream dependencies'
                              : 'Show services / dbs this endpoint\'s service typically calls'}
                            icon={isExpanded
                              ? <ChevronDown size={13} strokeWidth={1.75} />
                              : <ChevronRight size={13} strokeWidth={1.75} />} />
                        </td>
                        {/* v0.8.574 — every data cell renders only when
                            its column is visible (?cols=); order stays in
                            lockstep with ENDPOINT_COLS so the colgroup and
                            body never misalign. */}
                        {visibleCols.has('service') && <td className="sticky-left" style={{ left: leftOffs['service'] }}>
                          <Link to={serviceHref(r.service, { range, env })}
                                style={{ fontFamily: 'monospace', fontSize: 12 }}>
                            {r.service}
                          </Link>
                        </td>}
                        {visibleCols.has('path') && <td className="mono sticky-left" style={{ fontSize: 12, left: leftOffs['path'] }} title={r.path}>
                          {r.path}
                        </td>}
                        {visibleCols.has('method') && <td className="mono" style={{ fontSize: 11, color: 'var(--text2)' }}>
                          {r.method || '—'}
                        </td>}
                        {visibleCols.has('calls') && <td className="num mono">
                          {fmtNum(r.calls)}
                          {/* v0.9.642 — Req/min varsayılandan çıktı; hızı
                              BURADA taşıyoruz ki bilgi kaybolmasın. Ayrı
                              kolon hâlâ ColumnManager'da (o zaman burada
                              tekrar etmiyor). */}
                          {!visibleCols.has('reqPerMin') && r.reqPerMin != null && (
                            <span style={{ color: 'var(--text3)', marginLeft: 6 }}>
                              · {fmtRate(r.reqPerMin)}
                            </span>
                          )}
                          {compare && <TrendDelta cur={r.calls} prior={r.priorCalls} kind="neutral" />}
                        </td>}
                        {visibleCols.has('errors') && <td className="num mono">
                          {fmtNum(r.errors)}
                          {compare && <TrendDelta cur={r.errors} prior={r.priorErrors} kind="lowerBetter" />}
                        </td>}
                        {visibleCols.has('errorRate') && <td className="num mono">
                          <span className={`badge ${errCls}`}>{r.errorRate.toFixed(2)}%</span>
                          {/* v0.9.642 — Errors varsayılandan çıktı; mutlak
                              sayı BURADA. Oran tek başına ölçek saklıyor:
                              %50 iki çağrının biri de olabilir, 2M'nin
                              1M'i de. */}
                          {!visibleCols.has('errors') && r.errors > 0 && (
                            <span style={{ color: 'var(--text3)', marginLeft: 6 }}>
                              · {fmtNum(r.errors)}
                            </span>
                          )}
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
                        {visibleCols.has('p99Delta') && <td className="num mono">
                          {(() => {
                            // v0.9.818 — "YENİ" YALANDI. Prior okuma da
                            // top-N (api.go getEndpoints prior taraması aynı
                            // limit/havuzla koşuyor), yani prior alanının boş
                            // olması "bu endpoint önceki pencerede YOKTU"
                            // demek değil; "önceki pencerenin LİSTESİNE
                            // girmemiş" demek. İkisi çok farklı: birincisi
                            // yeni bir deploy, ikincisi sadece sıralama.
                            const d = endpointP99Delta(r.p99Ms, r.priorP99Ms);
                            if (d.kind === 'listNew') {
                              return <span style={{ color: 'var(--text3)' }}
                                title={LIST_NEW_TITLE}>{LIST_NEW_LABEL}</span>;
                            }
                            const cls = d.pct > 5 ? 'var(--err)' : d.pct < -5 ? 'var(--ok)' : 'var(--text3)';
                            return <b style={{ color: cls }}>{d.pct > 0 ? '▲' : d.pct < 0 ? '▼' : ''}{Math.abs(d.pct).toFixed(0)}%</b>;
                          })()}
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
                            // v0.9.839 — the sparkline modal is retired;
                            // its three RED charts are the first thing
                            // on the detail page, so this goes to the
                            // same place the row does.
                            onClick={() => openEndpointPage(r)}
                            title="Open the endpoint detail page — calls / errors / p99 on a time axis"
                            className="btn-bare"
                          >
                            <Sparkline values={r.sparkline ?? []}
                              width={88} height={22}
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
                          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                            {/* v0.9.1257 (operatör: "Traces butonu çok
                                belirgin değil, View yazıyor sadece") —
                                a.sec düğme anatomisi (v0.9.1210 kuralı). */}
                            <Link to={tracesLink(r, range, env, cluster)} className="sec"
                                  style={{ fontSize: 11, padding: '2px 8px' }}>
                              Traces →
                            </Link>
                            {/* v0.9.310 (brief N3) — jump STRAIGHT to the
                                slowest / worst-error trace for this route.
                                "view →" lands on a filtered list the
                                operator must then re-scan by eye; these two
                                ids are already in the MV's argMax exemplar
                                states, so the row read costs nothing extra
                                to carry them.

                                Rendered only when present. Empty means "no
                                exemplar in this window" — forward-only
                                states, a healthy window, or the raw
                                cluster/env path which has no states at all
                                — and a disabled placeholder would imply a
                                trace exists that we won't show. */}
                            {r.slowTraceId && (
                              <Link to={traceHref(r.slowTraceId, { pageRange: range })}
                                    title="Open the SLOWEST trace of this endpoint in the selected window. Falls outside the window? The trace may have aged past span retention — the MV keeps exemplars longer than the raw spans."
                                    style={{ fontSize: 11, color: 'var(--warn)', textDecoration: 'none' }}>
                                ⚡
                              </Link>
                            )}
                            {r.errorTraceId && (
                              <Link to={traceHref(r.errorTraceId, { pageRange: range })}
                                    title="Open the slowest ERRORED trace of this endpoint in the selected window."
                                    style={{ fontSize: 11, color: 'var(--err)', textDecoration: 'none' }}>
                                ✖
                              </Link>
                            )}
                          </span>
                        </td>}
                      </tr>
                      {isExpanded && (
                        <tr>
                          <td />
                          {/* v0.8.574 — span every VISIBLE data column
                              (?cols= can hide any of the 14). */}
                          <td colSpan={visibleCols.size} style={{ background: 'var(--bg0)', padding: '8px 14px' }}>
                            <DependencyStrip
                              service={r.service} range={range}
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
            {/* v0.9.818 — KAYNAK DEĞİŞİMİ ARTIK GÖRÜNÜR. env/cluster
                seçiliyken okuma spanmetrics MV'sinden HAM spans'e düşüyor
                (EndpointsQuery.forcesRaw) ve dönen POPÜLASYON değişiyor:
                rotasız client span'leri listeye katılıyor, exemplar
                kısayolları ise hiç gelmiyor. Bugüne dek sayfa bunu
                söylemiyordu; aynı filtreyi açıp kapatan operatör satır
                sayısının neden oynadığını ve ⚡/✖ ikonlarının neden
                kaybolduğunu tahmin etmek zorundaydı. Not YALNIZ gerçekten
                ham yoldayken çıkar — her sayfada duran bir uyarı, hiçbir
                sayfada okunmayan bir uyarıdır. */}
            {sourceNote && (
              <div style={{
                marginTop: 10, padding: '7px 11px', fontSize: 11.5,
                border: '1px solid var(--border)', borderLeft: '3px solid var(--accent)',
                borderRadius: 6, background: 'var(--bg2)', color: 'var(--text2)',
                lineHeight: 1.5,
              }}>{sourceNote}</div>
            )}
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
      </PageShell>
    </>
  );
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
function DependencyStrip({ service, window: win, capped, entry, range }: {
  service: string;
  // v0.9.967 — the PAGE window, for the dep pills' service links.
  //
  // Deliberately NOT `win`: that is a Go duration string (rangeToSince's
  // clamped value, e.g. '30m') and several of its values — '10m', '20m' —
  // are not PRESET_SECONDS keys. decodeRange accepts any non-custom string
  // as a preset, so emitting one would land the destination on the 86400s
  // FALLBACK: a link that says 10 minutes and shows 24 hours.
  range: import('@/lib/types').TimeRange;
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
            to={serviceHref(d.service, { range })}
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

// KPI karosu v0.9.819'da EndpointsSummary'ye taşınmıştı; v0.9.834'te
// şeridin tamamı KALDIRILDI (operatör: bu metrikler gereksiz). Sayfa-
// yerel bir kopya geri getirilmiyor — kaldırılan bir yüzeyin arkasına
// bırakılan "küçük" bir nüsha, kaldırma kararını sessizce geri alır.
