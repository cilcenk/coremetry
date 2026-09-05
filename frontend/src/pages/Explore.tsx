import { Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTablePrefs } from '@/lib/queries/prefs';
import type { CSSProperties } from 'react';
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { FilterQueryBox } from '@/components/FilterQueryBox';
import { HeatmapCellExemplars, type HeatmapCellRef } from '@/components/HeatmapCellExemplars';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { useAuth } from '@/components/AuthProvider';
import { LatencyHeatmap } from '@/components/LatencyHeatmap';
import { BubbleUpPanel } from '@/components/BubbleUpPanel';
import { ShareButton } from '@/components/ShareButton';
import { LogsExplorer } from '@/components/LogsExplorer';
import { MetricsExplorer } from '@/components/MetricsExplorer';
import { CorrelationContextDrawer } from '@/components/CorrelationContextDrawer';
import type { PivotAnchor } from '@/lib/types';
import type { ExploreVizKind } from '@/components/ExploreViz';
import { api } from '@/lib/api';
import { heatmapBucketCount } from '@/lib/chartStep';
import { timeRangeToNs, fmtNum, fmtClock } from '@/lib/utils';
import { raceGuard } from '@/lib/raceGuard';
import { encodeRange, decodeRange, encodeFilters, decodeFilters, buildQuery, rebuildPreserving } from '@/lib/urlState';
import { storedRangeString } from '@/lib/useUrlRange';
import { pushZoom, popZoom } from '@/lib/chart/zoomHistory';
import type { TimeRange, FilterExpr, LatencyHeatmap as Heatmap } from '@/lib/types';
import {
  REPEAT_PRESETS, STEP_OPTIONS,
  type ResultMode, type Source,
} from './explore/presets';
import { NLQueryBox } from './explore/NLQueryBox';
import { TracesResult } from './explore/TracesResult';
import { RepeatsResult } from './explore/RepeatsResult';
import { useQueryHistory } from './explore/useQueryHistory';
import { RecentQueries } from './explore/RecentQueries';
import {
  type BuilderState, type PivotPair, defaultBuilderState, blankQuery, nextLetter,
  duplicateQueryAt,
  produces, effectiveFilters, builderDesc, MAX_QUERIES,
  PANEL_SERIES_CAP, TOP_N_OPTIONS, EXPLORE_COMPARE, compareLabel, vizDisabledFor, queryUnit } from './explore/model';
import { encodeBuilder, seedFromLegacyParams } from './explore/urlCodec';
import {
  hasMeaningfulParams, exploreQuerySig, nextExploreKey, type ExploreKeyState,
} from './explore/exploreRouteKey';
import { useExploreQueries, useExploreOverlays } from './explore/useExploreQueries';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { applyGlobalScope, scopeChips } from './explore/globalScope';
import { metricsNeedingUnit, useMetricUnits, withMetricUnits } from './explore/metricUnits';
import { PanelStack, buildPanels } from './explore/PanelStack';
import { queryToPanel, isPinnable } from './explore/pinToDashboard';
import { PinToDashboardModal } from './explore/PinToDashboardModal';
import { GroupTable } from './explore/GroupTable';
import { pivotQuery, type PivotMode } from './explore/pivotQuery';
import { exploreSourceHref, type SourceTarget } from './explore/sourceHref';
import { SummaryViz } from './explore/SummaryViz';
import { RowsCappedNote } from './explore/RowsCappedNote';
import { heatmapQuerySig } from './explore/heatmapSig';
import { histogramResultToHeatmap } from '@/components/dashboard/histogramHeatmap';
import { QueryRow } from './explore/QueryRow';
import { FormulaRow } from './explore/FormulaRow';
import { VizRail } from './explore/VizRail';
import { SplitByPicker } from './explore/SplitByPicker';
import { Button } from '@/components/ui/Button';
import { PageShell } from '@/components/ui/PageShell';

// Explore (explore-v2 Phase 2) — the metric result mode is now the
// multi-query builder: up to four queries A–D (span signals or catalogue
// metrics) + one formula, rendered as a stack of cursor-synced
// TimeSeriesPanels with a combined group table. Builder state rides ?q=
// (compact JSON; urlCodec.ts); every legacy param shape stays decodable
// forever via seedFromLegacyParams (SavedViews + inbound links).
//
// Traces + repeats result modes keep their pre-v2 console (filter zone,
// presets) and URL shapes — those params remain the canonical form there.
// source=metrics / source=logs keep their dedicated panels until the
// Phase-5 /metrics collapse.

// The pristine builder's ?q= encoding — used to suppress the param entirely
// so a paramless /explore keeps showing the entry cards.
const DEFAULT_Q = encodeBuilder(defaultBuilderState());

function ExploreInner({ onSelfWrite }: {
  // v0.9.805 — "state şu an bu URL'i kodluyor" bildirimi. ExplorePage bunu
  // kullanarak KENDİ yazımımızı dışarıdan gelen bir navigasyondan ayırıyor;
  // olmasaydı sorgu imzasını anahtara katmak her düzenlemede remount
  // tetiklerdi.
  onSelfWrite: (search: string) => void;
}) {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();

  // ── Decode the URL ONCE per mount (the page-level key remounts us on the
  // entry↔workspace boundary; state owns the truth afterwards). ───────────
  const builderSeed = useRef<BuilderState | null | undefined>(undefined);
  if (builderSeed.current === undefined) {
    builderSeed.current = seedFromLegacyParams(searchParams);
  }

  const [source, setSource] = useState<Source>(() => {
    const v = searchParams.get('source');
    return v === 'metrics' || v === 'logs' ? v : 'spans';
  });
  const [resultMode, setResultMode] = useState<ResultMode>(() => {
    const r = searchParams.get('result');
    if (r === 'traces' || r === 'repeats') return r;
    return 'metric';
  });
  const [range, setRange] = useState<TimeRange>(
    () => decodeRange(searchParams.get('range') ?? storedRangeString(), { preset: '30m' }));

  // Legacy viz passthrough for the metrics/logs source panels only — the
  // spans builder has its own ExploreViz inside BuilderState.
  const [legacyViz] = useState<ExploreVizKind>(() => {
    const v = searchParams.get('viz') as ExploreVizKind;
    return ['line', 'bar', 'topN', 'kpi'].includes(v) ? v : 'line';
  });

  // ── Builder state (metric result mode) ───────────────────────────────────
  const [builder, setBuilder] = useState<BuilderState>(
    () => builderSeed.current ?? defaultBuilderState());
  // Debounced copy drives BOTH the fetch fan-out and the ?q= write, so
  // typing in a filter doesn't fire a query per keystroke (plan: 300ms).
  const [debounced, setDebounced] = useState(builder);
  useEffect(() => {
    const t = window.setTimeout(() => setDebounced(builder), 300);
    return () => clearTimeout(t);
  }, [builder]);

  // v0.9.801 — KATALOG BİRİMİ GEÇ DOLDURMA. Picker dışındaki her tohum
  // yolu (metricCatalogueHref, ?metric= legacy dalı, dashboard panelinden
  // geçiş, v0.9.801 öncesi paylaşılmış ?q=) metrik-kaynaklı sorguyu
  // birimsiz kuruyordu; süre metrikleri o yüzden çıplak saniye basıyordu.
  // Halka burada kapanıyor: birimi eksik olan metriklerin katalog birimi
  // çözülüp STATE'e yazılıyor — böylece bir sonraki ?q= yazımı da 'u'
  // taşır ve link birimli paylaşılır. Katalog boşsa alan boş kalır.
  const unitPending = useMemo(() => metricsNeedingUnit(builder), [builder]);
  const resolvedUnits = useMetricUnits(unitPending);
  useEffect(() => {
    // withMetricUnits hiçbir şey değişmediyse AYNI referansı döndürür →
    // setBuilder bail-out eder, döngü yok.
    setBuilder(b => withMetricUnits(b, resolvedUnits));
  }, [resolvedUnits]);

  // Ephemeral interaction state — NOT in the URL (plan state model).
  const [zoomWindow, setZoomWindow] = useState<{ from: number; to: number } | null>(null);
  // Grafana-parite M1 — lokal zoom GERİ-YIĞINI (çift-tık = bir adım geri).
  // zoomWindow URL'e taşınmıyor (o ayrı karar); yığın da aynı efemer sınıfta.
  // zoomWindowRef: TSP'nin setSelect hook'u build anındaki onZoom closure'ını
  // tutar — zoom öncesi pencereyi ref'ten okumak stale-push'u önler.
  const zoomStackRef = useRef<Array<{ from: number; to: number } | null>>([]);
  const zoomWindowRef = useRef(zoomWindow); zoomWindowRef.current = zoomWindow;
  const handlePanelZoom = useCallback((f: number, t: number) => {
    zoomStackRef.current = pushZoom(zoomStackRef.current, zoomWindowRef.current);
    setZoomWindow({ from: f, to: t });
  }, []);
  const handlePanelZoomReset = useCallback(() => {
    const { stack, view } = popZoom(zoomStackRef.current);
    zoomStackRef.current = stack;
    // Yığın boşsa (ya da ilk zoom'un öncesi itilmişse) view null → tam
    // görünüme dön; zoom yokken zaten null → state değişmez (no-op).
    setZoomWindow(view ?? null);
  }, []);
  const [hiddenKeys, setHiddenKeys] = useState<Set<string>>(() => new Set());
  const [focusKey, setFocusKey] = useState<string | null>(null);
  // Correlated Signals (task #6) — the exemplar ◆ click opens the pivot drawer
  // anchored on that trace instead of navigating away to /trace, keeping the
  // operator in Explore. This is the single highest-traffic cross-signal pivot.
  const [correlateAnchor, setCorrelateAnchor] = useState<PivotAnchor | null>(null);
  // v0.8.419 (DE4) — pin-to-dashboard modal state: the Panel converted
  // from a builder query, ready to append to a chosen dashboard.
  const [pinPanel, setPinPanel] = useState<import('@/lib/types').Panel | null>(null);
  // v0.9.236 — the pin button was rendered for EVERY role. A viewer could
  // open the modal, fill it in, and get the editor-gated 403 back as raw
  // text in the error line. Every other mutation surface in the app gates
  // on role (Alerts, Watchers, Slos, Monitors, Dashboards, Incidents,
  // Runbooks, the ⌘K registry); Explore had no useAuth at all. Empty set →
  // PanelStack passes onPin={undefined} → no button.
  const { user } = useAuth();
  const canPin = user?.role === 'admin' || user?.role === 'editor';
  const pinnableLetters = useMemo(
    () => (canPin
      ? new Set(builder.queries.filter(isPinnable).map(q => q.letter))
      : new Set<string>()),
    [builder.queries, canPin]);

  // ── Traces / repeats console state (pre-v2, unchanged shapes) ────────────
  const [filters, setFilters] = useState<FilterExpr[]>(
    () => decodeFilters(searchParams.get('filters')));
  const [mode, setMode] = useState<'builder' | 'advanced'>(
    () => (searchParams.get('mode') === 'advanced' ? 'advanced' : 'builder'));
  const [dsl, setDsl] = useState(() => searchParams.get('dsl') ?? '');
  // v0.9.270 — debounced copy, mirroring the builder's own 300ms above.
  //
  // The textarea fed `dsl` STRAIGHT into the fetch effect's dep list, so every
  // keystroke fired what this app's own handler calls "the heaviest uncached
  // read" (internal/api/api.go:3429). Server-side caching cannot absorb it:
  // the key is "traces:" + r.URL.RawQuery (api.go:3442), so each prefix is a
  // distinct key — a guaranteed MISS, and singleflight has nothing to
  // deduplicate. Worse, ANY filter disqualifies the trace_summary_5m fast
  // path (repo.go:1949 requires len(f.Filters) == 0), so each of those misses
  // is a raw GROUP BY trace_id.
  //
  // The damage is not "one wasted query per character" — it is filling the
  // ClickHouse read pool while an operator types, which is what starves the
  // hot endpoints that share it. The builder was given this exact treatment
  // when it was written; the advanced console predates it (v0.8.113) and was
  // never covered.
  const [dslDebounced, setDslDebounced] = useState(dsl);
  useEffect(() => {
    const t = window.setTimeout(() => setDslDebounced(dsl), 300);
    return () => clearTimeout(t);
  }, [dsl]);
  const [queryError, setQueryError] = useState<string | null>(null);
  // v0.9.867 (tutarlılık denetimi MT1) — queryError YALNIZ DSL sözdizimi
  // hatasını taşır (textarea'nın altındaki alan-seviyesi kutu). Okuma
  // hatasının sunucu metni ayrı tutuluyor, yoksa non-DSL hatalarda
  // atılıyordu ve sonuç alanı sessizce boş kalıyordu.
  const [readErr, setReadErr] = useState<string | null>(null);
  // Retry kaynağı: bu iki okuma elle fetch, react-query refetch'i yok.
  const [retryNonce, setRetryNonce] = useState(0);
  const [repeatGroupBy, setRepeatGroupBy] = useState<string[]>(
    () => (searchParams.get('groupBy') ?? '').split(',').filter(Boolean));
  const [repeatMin, setRepeatMin] = useState(
    () => parseInt(searchParams.get('minRepeats') ?? '5', 10) || 5);
  const [repeats, setRepeats] = useState<import('@/lib/types').RepeatedSpanRow[] | null | undefined>(undefined);
  const [traces, setTraces] = useState<import('@/lib/types').TraceRow[] | null | undefined>(undefined);
  const [traceTotal, setTraceTotal] = useState<number | undefined>(undefined);
  const [traceHasMore, setTraceHasMore] = useState(false);
  // v0.9.284 — the counting mode is a QUERY, not a decoration. It used
  // to be pinned to 'approx' so the footer could render "of ~M", and
  // that single field held the trace_summary_5m fast path shut
  // (repo.go gates the MV on CountMode skip/""): the same unfiltered
  // search /traces answered from the MV, /explore answered from raw
  // spans. Default 'skip'; the exact count is opt-in behind the same
  // "Show total" affordance /traces uses, and lives in the URL so a
  // shared view keeps the choice.
  const [showTotal, setShowTotal] = useState(
    () => searchParams.get('count') === 'exact');
  const [traceLimit, setTraceLimit] = useState(
    () => parseInt(searchParams.get('limit') ?? '50', 10) || 50);
  const [extraCols, setExtraCols] = useState<string[]>(
    () => (searchParams.get('cols') ?? '').split(',').map(s => s.trim()).filter(Boolean));
  // v0.10.320 (DataTable/ContextBar audit dilim 8, Explore) — trace sonuç
  // tablosunun öznitelik kolonları için SUNUCU tercihi (useTablePrefs
  // 'explore-traces'; Traces v0.10.251 ile aynı sözleşme, ayrı anahtar —
  // Explore geçici bir çalışma yüzeyi, Traces'ın tercihini ezmemeli).
  // Öncelik URL cols= (deep link) > sunucu > varsayılan (boş). URL'de cols
  // yokken sunucu modeli BİR KEZ benimsenir; sonrası operatör değişikliği
  // (ColumnManager → setExtraCols) sunucu modelinden farklıysa kaydedilir.
  const prefs = useTablePrefs('explore-traces');
  const urlHadCols = useRef(!!(searchParams.get('cols') ?? '').trim());
  const prefsAdopted = useRef(false);
  useEffect(() => {
    if (prefs.model === undefined || prefsAdopted.current) return;
    prefsAdopted.current = true;
    if (urlHadCols.current || !prefs.model) return;
    const hidden = new Set(prefs.model.hidden);
    const extras = prefs.model.order.filter(id => !hidden.has(id)).slice(0, 8);
    if (extras.length) setExtraCols(extras);
  }, [prefs.model]);
  useEffect(() => {
    if (!prefsAdopted.current) return;
    const server = prefs.model ? prefs.model.order.filter(id => !prefs.model!.hidden.includes(id)) : null;
    if (server && server.join(',') === extraCols.join(',')) return;
    if (!server && extraCols.length === 0) return;
    prefs.save({ v: 1, order: extraCols, hidden: [], sig: 'explore-traces' });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [extraCols]);

  const [services, setServices] = useState<string[]>([]);
  const [heatmap, setHeatmap] = useState<Heatmap | null | undefined>(undefined);
  // v0.9.1202 — metrik-histogram ısı haritasının "neden boş" notu (VM
  // Faz 2 note alanı: "_bucket serisi bulunamadı — histogram olmayabilir").
  const [heatmapNote, setHeatmapNote] = useState<string | null>(null);
  // busy — veri EKRANDA kalırken süren okuma (v0.9.939, C2 keep-previous).
  // `undefined` ilk yükleme, `busy` yenileme; ikisi ayrı sorular ve tek
  // bayrakla anlatılamaz.
  const [busy, setBusy] = useState(false);
  const [cellExemplar, setCellExemplar] = useState<HeatmapCellRef | null>(null);
  // Phase 4.2 — heatmap box-select → BubbleUp. The dragged (time × latency)
  // rectangle becomes the selection; query A's filters/DSL are the baseline.
  const [boxSel, setBoxSel] = useState<
    { timeFromNs: number; timeToNs: number; lowDurMs: number; highDurMs: number; count: number } | null
  >(null);
  // v0.9.1113 (Faz 5 BubbleUp keşfedilebilirliği) — tek-tık hata diff'i:
  // sürükleme öğrenmeden "hatalarda ne farklı?" sorusu. Baseline aynı
  // (sorgu A), seçim status=error; pencere tam sorgu penceresi. Kutu
  // seçimi gibi geçici analiz durumu — bilinçli URL dışı (boxSel emsali).
  const [errDiff, setErrDiff] = useState(false);

  const exploreRange = useMemo(() => timeRangeToNs(range), [range]);
  // v0.9.83 — panellerin x-ekseni sorgu penceresine sabit (madde 2).
  const xRangeSec = useMemo(() => ({ from: exploreRange.from / 1e9, to: exploreRange.to / 1e9 }), [exploreRange]);

  // Recent-queries ring + entry-screen gate (Phase-1 behaviour preserved).
  const { history, save: saveHistory } = useQueryHistory();
  // v0.9.849 — halkadan geri yükleme. Kayıt TAM arama dizesini saklıyor,
  // yani geri dönüş builder state'ini elle kurmak değil o URL'e GİTMEK.
  // ExplorePage'in imza-anahtarı (v0.9.805) bu navigasyonu kendi
  // yazımızdan ayırıp remount tetikliyor; ExploreInner de URL'i mount'ta
  // bir kez okuyor — mekanizma zaten yerindeydi, eksik olan listeydi.
  //
  // PUSH (replace DEĞİL): sayfanın kendi state→URL yazımı geçmişi
  // kirletmemek için replace kullanır, ama bu bir operatör navigasyonudur
  // ve tarayıcının geri düğmesi onu geri alabilmeli.
  const applyHistory = useCallback((search: string) => {
    navigate(`/explore${search}`, { preventScrollReset: true });
  }, [navigate]);
  const [hasParams, setHasParams] = useState(() => hasMeaningfulParams(searchParams));
  const seedNextRef = useRef<string | null>(null);

  // ── State → URL ───────────────────────────────────────────────────────────
  // Metric mode writes the canonical ?q=; traces/repeats and the
  // metrics/logs panels keep writing their legacy shapes (those params ARE
  // the canonical form for those surfaces). replace keeps history clean.
  useEffect(() => {
    let queryEntries: Array<[string, string | number | undefined | null | false]>;
    if (source !== 'spans') {
      queryEntries = [
        ['source', source],
        ['viz', legacyViz !== 'line' ? legacyViz : ''],
        // service/metric passthrough for ServiceInfra deep links — the
        // panels read them at mount; keep them while the panel is up.
        ['service', searchParams.get('service') ?? ''],
        ['metric', searchParams.get('metric') ?? ''],
      ];
    } else if (resultMode === 'metric') {
      // A pristine default builder writes NO params so the paramless
      // /explore entry screen (question cards) survives — the exact
      // old-workspace semantics where all-defaults produced an empty qs.
      const enc = encodeBuilder(debounced);
      queryEntries = [['q', enc !== DEFAULT_Q ? enc : '']];
    } else {
      queryEntries = [
        ['result',  resultMode],
        ['filters', mode === 'builder' ? encodeFilters(filters) : ''],
        ['dsl',     mode === 'advanced' ? dslDebounced : ''],
        ['mode',    mode === 'advanced' ? 'advanced' : ''],
        ['limit',   resultMode === 'traces' && traceLimit !== 50 ? traceLimit : ''],
        // v0.9.284 — the count choice is part of the shared view. Only
        // the non-default is written, so an ordinary URL stays clean and
        // the MV fast path is what a fresh link gets.
        ['count',   resultMode === 'traces' && showTotal ? 'exact' : ''],
        ['cols',    resultMode === 'traces' ? extraCols.join(',') : ''],
        ['groupBy', resultMode === 'repeats' ? repeatGroupBy.join(',') : ''],
        ['minRepeats', resultMode === 'repeats' && repeatMin !== 5 ? repeatMin : ''],
      ];
    }
    // queryQs — SAHİP OLUNAN parametrelerin dizesi; `meaningful` bunun
    // uzunluğundan çıkıyor. rebuildPreserving KULLANILMAZ: yabancı bir
    // parametre (ör. `?ai=`) boş bir sorguyu "anlamlı" gösterir ve
    // paramsız /explore giriş ekranı (soru kartları) kaybolurdu.
    const queryQs = buildQuery(queryEntries);
    // v0.9.940 (A7) — asıl URL yazımı yabancı parametreleri KORUR. Bu
    // efekt dizeyi sıfırdan kuruyordu, yani AI çekmecesinin `?ai=`i bir
    // filtre düzenlemesinde siliniyor, çekmece kendiliğinden kapanıyordu
    // (K4/K9 ile aynı sınıfın üçüncü doğuşu).
    const qs = rebuildPreserving(window.location.search,
      [...queryEntries, ['range', encodeRange(range)]]);
    const next = qs ? `?${qs}` : '';
    // v0.9.805 — navigate'ten ÖNCE ve KOŞULSUZ. Bu satır "ekrandaki state
    // şu URL'e karşılık geliyor" diyor; ExplorePage gelen bir URL'i bununla
    // karşılaştırıp kendi yankımızı remount etmiyor. Koşulsuz, çünkü
    // navigate atlanan durumda da (next zaten adres çubuğundaysa) bildirim
    // güncel olmalı — remount sonrası kanonikleşen URL bunun tipik hâli.
    onSelfWrite(next);
    if (next !== window.location.search) {
      navigate(`/explore${next}`, { preventScrollReset: true, replace: true });
    }
    const meaningful = queryQs.length > 0;
    setHasParams(meaningful);
    // Seed-skip (Phase-1): the first canonical URL of a mount is the seed a
    // card / deep link / saved view produced — only divergence is recorded.
    if (seedNextRef.current === null) {
      seedNextRef.current = next;
    }
    if (meaningful && next !== seedNextRef.current) {
      const desc = source !== 'spans'
        ? `${source} explorer`
        : resultMode === 'metric'
          ? builderDesc(debounced)
          // dslDebounced, not dsl: the history entry must describe the query
          // that actually ran, and this effect now wakes on the debounced
          // value. Reading the live one would label the entry with a keystroke
          // that was never sent.
          : legacyHistoryDesc({ resultMode, mode, dsl: dslDebounced, filters, repeatMin, repeatGroupBy });
      saveHistory(desc, next);
    }
    // searchParams intentionally omitted: it's only read for the
    // metrics/logs passthrough whose values never change while mounted.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [source, resultMode, debounced, filters, dslDebounced, mode, range, traceLimit, showTotal, extraCols, repeatMin, repeatGroupBy, legacyViz, navigate, saveHistory, onSelfWrite]);

  // Service options for the traces/repeats filter suggestions. Gated on
  // hasParams (entry screen fires no workspace fetches — Phase-1 finding).
  // v0.9.134 (scale-audit 2026-07-20 CHECK 1) — bounded to top-500 by
  // traffic: this list is only a SEED for FilterBuilder's service.name
  // autocomplete; the builder already runs a debounced live server search
  // (api.attributeValues, substring-aware) for the long tail, so at 1000s
  // of services an unbounded all-window catalogue was pure waste.
  useEffect(() => {
    if (!hasParams || source !== 'spans' || resultMode === 'metric') return;
    api.services(timeRangeToNs(range), 500)
      .then(s => setServices((s ?? []).map(x => x.name)))
      .catch(() => setServices([]));
  }, [range, hasParams, source, resultMode]);

  // ── Global daraltmalar (UX denetimi B2/K9, v0.9.942) ─────────────────────
  //
  // env: Topbar EnvPicker'ın `?env=`i (global, yapışkan). cluster:
  // EndpointDetail→Explore pivotunun taşıdığı sayfa-yerel `?cluster=`
  // (pages/endpoints/links.ts). İkisi de bugüne dek adres çubuğunda
  // DURUYOR ama sorguya GİRMİYORDU — picker'ın VARLIĞI yalan söylüyordu.
  //
  // searchParams'tan okunuyor, state'e KOPYALANMIYOR: ikisi de bu sayfanın
  // sahip olmadığı paramlar (env'in sahibi Topbar, cluster'ın sahibi gelen
  // link). A7'nin rebuildPreserving'i sayesinde artık silinmiyorlar, o
  // yüzden URL tek kaynak olarak yeterli.
  const [env] = useUrlEnv();
  const clusterScope = searchParams.get('cluster') ?? '';
  const scoped = useMemo(
    () => applyGlobalScope(debounced, env, clusterScope),
    [debounced, env, clusterScope]);

  // ── Builder fan-out (react-query; inactive modes pass from=0 → disabled) ──
  const builderActive = hasParams && source === 'spans' && resultMode === 'metric';
  const builderFrom = builderActive && debounced.viz !== 'heatmap' ? exploreRange.from : 0;
  // D5 — eligible span queries fetch series + exemplars in ONE resolver call;
  // exemplarsByLetter (◆ glyphs) comes from the same hook now. v0.8.332
  // (pivot Phase 3): otlpExemplarsByLetter adds REAL OTLP exemplar ◆ for
  // single-service catalogue-metric queries; clicks ride the same
  // onExemplarClick → CorrelationContextDrawer path as the span-derived ones.
  const {
    byLetter, totalByLetter, cappedByLetter, noteByLetter, stepByLetter,
    exemplarsByLetter, otlpExemplarsByLetter,
    anyLoading, errorByLetter,
    // v0.9.824 — önceki dönem (hayalet). Karşılaştırma kapalıyken üçü de
    // boş/0 döner ve ikinci fan-out HİÇ koşmaz.
    compareByLetter, compareStepByLetter, compareOffsetNs,
  } = useExploreQueries(
    scoped,
    builderFrom,
    exploreRange.to,
  );
  // Phase 3.3 — deploy markers + SLO thresholds for pinned-service queries.
  //
  // `debounced`, `scoped` DEĞİL (v0.9.942): bu kanca yalnız pinnedService /
  // pinnedOperation okuyor ve env/cluster çipleri servis pinini
  // DEĞİŞTİRMİYOR. Kapsanmış state'i vermek aynı deploy/SLO listesini ikinci
  // bir memo kimliği altında yeniden hesaplatırdı, tek kazancı olmadan.
  const overlaysByLetter = useExploreOverlays(debounced, builderFrom, exploreRange.to);
  const panels = useMemo(
    () => buildPanels(debounced, {
      byLetter,
      // v0.9.804 — builderFrom, AYNEN fetch'e giden değer. 0 = fan-out
      // devre dışı (paramsız /explore) ve paneller "idle" olur; bu ayrım
      // olmadan A paneli sonsuza dek dönüyordu.
      from: builderFrom,
      errorByLetter,
      exemplarsByLetter, overlaysByLetter, totalByLetter,
      otlpExemplarsByLetter, cappedByLetter, noteByLetter, stepByLetter,
      compareByLetter, compareStepByLetter, compareOffsetNs,
    }),
    [debounced, byLetter, builderFrom, errorByLetter, exemplarsByLetter,
     overlaysByLetter, totalByLetter, otlpExemplarsByLetter, cappedByLetter, noteByLetter,
     stepByLetter, compareByLetter, compareStepByLetter, compareOffsetNs],
  );
  // Harf başına hata bandı — panellerin İÇİNDEKİ mesajın üstünde, sayfa
  // seviyesinde bir özet. Eskiden yalnız ilk hatayı basıyordu.
  const builderErrors = useMemo(
    () => Object.entries(errorByLetter).sort(([a], [b]) => a.localeCompare(b)),
    [errorByLetter]);
  const anyProduces = debounced.queries.some(produces);

  // Heatmap viz — the LatencyHeatmap path, driven by query A (panel header
  // states it). Gated exactly like the pre-v2 heatmap fetch.
  //
  // v0.9.810 — bağımlılık DARALDI. Eskiden `debounced` (tüm builder state)
  // ve `exploreRange` nesnesi dep'teydi; oysa istek yalnız ÜRETEN İLK
  // SORGUNUN filtrelerini + DSL'ini + pencereyi + bucket sayısını
  // taşıyor. B'nin agg'ini değiştirmek ya da formülü yazmak, girdisine hiç
  // dokunmadan sayfanın en pahalı taramasını (ham spans log-ölçek ızgarası)
  // yeniden tetikliyordu. İmza SAF ve testli (heatmapSig.ts) — bir alan
  // isteğe girerse imzaya da girmek zorunda.
  const heatmapBuckets = heatmapBucketCount(1);
  const heatmapSig = heatmapQuerySig(
    debounced, exploreRange.from, exploreRange.to, heatmapBuckets);
  useEffect(() => {
    // Any query / range / viz change invalidates a pending box-select — the
    // dragged rectangle no longer maps onto the heatmap about to render.
    setBoxSel(null);
    if (!builderActive || debounced.viz !== 'heatmap') return;
    const a = debounced.queries.find(produces);
    if (!a) { setHeatmap(null); return; }
    // v0.9.939 (UX denetimi C2/Ö18) — BLANK YOK. `setHeatmap(undefined)`
    // her ayarlanmış düzenlemede sayfanın EN pahalı sorgusunu (ham spans
    // log-ölçek ızgarası, ≤3s bütçe) sıfırdan çizdiriyordu: operatör her
    // filtre dokunuşunda grafiğini kaybediyor, aynı sayfadaki ÇİZGİ modu
    // ise keepPreviousData ile akıcı kalıyordu. Artık eldeki ızgara
    // ekranda kalır, başlıkta "yenileniyor" izi belirir; ilk yüklemede
    // (veri yok) davranış aynı — spinner.
    setBusy(true);
    // v0.8.300 (quality bar S3) — the debounced builder still fires on every
    // settled edit; without cancellation an older heatmap can land last.
    // v0.9.939 (C3) — bayrağa GERÇEK İPTAL eklendi: superseded tarama
    // aksi halde max_execution_time'a kadar CH'de koşuyordu.
    const g = raceGuard();
    const fs = effectiveFilters(a);
    const { from, to } = exploreRange;
    // v0.9.1202 — metrik-kaynak sorgu A: ısı haritası ham span taraması
    // değil, metriğin KENDİ explicit-histogram kovaları
    // (/api/metrics/histogram — metricsource seam'i, VM Faz 2'den beri
    // sunucuda hazırdı ama Explore'dan erişilemiyordu). Dönüştürücü
    // panolarınkiyle AYNI (histogramResultToHeatmap) — iki yüzey tek
    // le-düzeni yorumu taşır.
    if (a.source === 'metric') {
      if (!a.metric) { setHeatmap(null); setBusy(false); return g.cancel; }
      const stepS = Math.max(1, Math.round((to - from) / 1e9 / heatmapBuckets));
      api.metricHistogram({
        name: a.metric, service: a.scope || undefined,
        filters: fs.length ? JSON.stringify(fs) : undefined,
        from, to, step: stepS,
      })
        .then(h => {
          if (!g.ok()) return;
          setHeatmap(h ? histogramResultToHeatmap(h, a.unit) : null);
          setHeatmapNote(h?.note ?? null);
          setBusy(false);
        })
        .catch(() => { if (!g.ok()) return; setHeatmap(null); setHeatmapNote(null); setBusy(false); });
      return g.cancel;
    }
    setHeatmapNote(null);
    api.spanHeatmap({
      filters: fs.length ? JSON.stringify(fs) : undefined,
      dsl: a.dsl.trim() || undefined,
      // v0.9.707 — sabit 80 → genişlik-türevi (~12px/sütun, 40..240).
      from, to, buckets: heatmapBuckets,
    }, g.signal)
      .then(h => { if (!g.ok()) return; setHeatmap(h ?? null); setBusy(false); })
      .catch(() => { if (!g.ok()) return; setHeatmap(null); setBusy(false); });
    return g.cancel;
    // debounced/exploreRange BİLEREK dep değil: ikisinin de heatmap'e
    // giden parçaları heatmapSig'in İÇİNDE ve effect gövdesi her koşuda
    // güncel değerleri okuyor. Kimliklerini dep'e koymak daraltmayı
    // geçersiz kılardı.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [builderActive, heatmapSig]);

  // ── Traces / repeats fetches (pre-v2 behaviour, scoped to their modes) ───
  useEffect(() => {
    if (!hasParams || source !== 'spans' || resultMode === 'metric') return;
    setQueryError(null);
    setReadErr(null);
    // v0.9.939 (C2/C3) — bayrak + GERÇEK İPTAL (raceGuard) ve BLANK YOK:
    // liste/repeats modları her düzenlemede ekranı boşaltıyordu, çizgi modu
    // boşaltmıyordu. Superseded ham-spans taraması artık iptal ediliyor.
    const g = raceGuard();
    const cancelledRef = { get v() { return !g.ok(); } };
    const { from, to } = timeRangeToNs(range);
    const filterArg = mode === 'builder' && filters.length ? JSON.stringify(filters) : undefined;
    const dslArg    = mode === 'advanced' && dslDebounced.trim() ? dslDebounced : undefined;

    setBusy(true);
    if (resultMode === 'traces') {
      api.traces({
        filters: filterArg, dsl: dslArg,
        from, to,
        sort: 'time', order: 'desc',
        limit: traceLimit,
        count: showTotal ? 'exact' : 'skip',
        extraAttrs: extraCols.length ? extraCols.join(',') : undefined,
      }, g.signal)
        .then(r => {
          if (cancelledRef.v) return;
          setBusy(false);
          setTraces(r.traces ?? []);
          setTraceTotal(r.total);
          setTraceHasMore(r.hasMore ?? false);
        })
        .catch(err => {
          if (cancelledRef.v) return;
          setBusy(false);
          setTraces(null);
          const msg = String(err?.message ?? err);
          // DSL sözdizimi hatası textarea'nın ALTINDA (alan-seviyesi); her
          // okuma hatasının metni ise sonuç alanındaki QueryError'a iner.
          // v0.9.867 öncesi ikincisi ATILIYORDU ve null hiçbir dala girmediği
          // için sonuç alanı bomboş kalıyordu (MT1).
          setQueryError(msg.includes('DSL') ? msg : null);
          setReadErr(msg);
        });
    } else {
      api.spanRepeats({
        filters: filterArg, dsl: dslArg,
        from, to,
        groupBy: repeatGroupBy.length ? repeatGroupBy : ['db.statement'],
        minRepeats: repeatMin,
      }, g.signal)
        .then(r => { if (cancelledRef.v) return; setBusy(false); setRepeats(r ?? []); })
        .catch(err => {
          if (cancelledRef.v) return;
          setBusy(false);
          setRepeats(null);
          const msg = String(err?.message ?? err);
          setQueryError(msg.includes('DSL') ? msg : null);
          setReadErr(msg);
        });
    }
    return g.cancel;
  }, [resultMode, range, filters, dslDebounced, mode, traceLimit, showTotal, extraCols, repeatMin, repeatGroupBy, hasParams, source, retryNonce]);

  // ── Builder mutators ──────────────────────────────────────────────────────
  const setQuery = (i: number, q: BuilderState['queries'][number]) =>
    setBuilder(b => ({ ...b, queries: b.queries.map((x, j) => (j === i ? q : x)) }));
  const addQuery = () => setBuilder(b => {
    const l = nextLetter(b.queries);
    return l ? { ...b, queries: [...b.queries, blankQuery(l)] } : b;
  });
  // v0.9.847 — çoğaltma. Karar SAF (model.duplicateQueryAt): harf tahsisi,
  // konum ve DERİN kopya tek yerde ve tabloyla test'li. Dolu havuzda helper
  // GİRDİYİ AYNEN döndürdüğü için setBuilder bail-out eder (yeni state
  // nesnesi bile kurulmaz), render tetiklenmez.
  const duplicateQuery = (i: number) => setBuilder(b => {
    const queries = duplicateQueryAt(b.queries, i);
    return queries === b.queries ? b : { ...b, queries };
  });
  const removeQuery = (i: number) =>
    setBuilder(b => ({ ...b, queries: b.queries.filter((_, j) => j !== i) }));

  // v0.9.848 — GroupTable satırından pivot. Tablo hangi HARFTEN hangi
  // çiftlerle istendiğini söylüyor; sorguyu bulup düzenlemek sayfanın işi.
  // pivotQuery null döndüğünde (uygulanamaz ya da sonuç bugünkünün AYNISI)
  // state'e dokunulmuyor — aynı çipi silip yeniden eklemek `filters`
  // dizisinin sırasını, dolayısıyla querySignature'ı değiştirir ve veri hiç
  // değişmemişken garanti bir cache MISS + yeni fan-out üretirdi.
  const pivotFromRow = useCallback((
    letter: string, pairs: PivotPair[], mode: PivotMode,
  ) => setBuilder(b => {
    const i = b.queries.findIndex(x => x.letter === letter);
    if (i < 0) return b;
    const next = pivotQuery(b.queries[i], pairs, mode);
    if (!next) return b;
    return { ...b, queries: b.queries.map((x, j) => (j === i ? next : x)) };
  }), []);

  // v0.9.930 — GroupTable satırından KAYNAĞA iniş. Karar saf
  // (explore/sourceHref.ts); burası yalnız harfi sorguya çeviriyor ve
  // pencereyi veriyor. Pencere `range` — panellerin fetch edildiği aralığın
  // ta kendisi; ayrı bir "şimdi" hesaplamak pivotHref'in var olma sebebi
  // olan kaymayı geri getirirdi.
  //
  // Formül satırının (ƒ) tek bir kaynağı YOK: birden çok harfin aritmetiği
  // hangi span kümesine iner sorusunun dürüst cevabı "hiçbirine".
  const sourceTargetFor = useCallback((letter: string, pairs: PivotPair[]): SourceTarget => {
    const q = builder.queries.find(x => x.letter === letter);
    if (!q) {
      return { ok: false, why: 'Formül satırı birden çok sorgunun aritmetiği — tek bir ham kaynağı yok' };
    }
    return exploreSourceHref(q, pairs, range);
  }, [builder.queries, range]);

  const openSourceFromRow = useCallback((letter: string, pairs: PivotPair[]) => {
    const t = sourceTargetFor(letter, pairs);
    if (t.ok) navigate(t.href);
  }, [sourceTargetFor, navigate]);

  // Result-mode switch — entering traces/repeats from the builder carries
  // query A's narrowing along when the legacy console is still empty.
  const switchResultMode = (m: ResultMode) => {
    // Deliberately the LIVE dsl, not the debounced copy: this is an event
    // handler, and "is the console empty?" must answer for what the operator
    // has typed right now, not for what has been sent yet.
    if (m !== 'metric' && resultMode === 'metric' && filters.length === 0 && !dsl.trim()) {
      const a = builder.queries.find(produces);
      if (a) {
        const fs = effectiveFilters(a);
        if (fs.length) setFilters(fs);
        if (a.dsl.trim()) { setDsl(a.dsl); setMode('advanced'); }
      }
    }
    setResultMode(m);
  };

  const toggleHidden = (rowKey: string) => setHiddenKeys(prev => {
    const next = new Set(prev);
    if (next.has(rowKey)) next.delete(rowKey); else next.add(rowKey);
    return next;
  });

  // v0.9.757 — düz tık İZOLE: yalnız tıklanan görünür (CorePanel lejant
  // semantiği). Zaten izoleyse ikinci tık hepsini geri açar. Anahtar
  // evreni panels'ten (tüm seriler), gizli kümesi "diğerleri".
  const isolateHidden = (rowKey: string) => setHiddenKeys(prev => {
    const all: string[] = [];
    for (const p of panels) for (const s of p.series) all.push(`${p.letter}:${s.label}`);
    const others = all.filter(k => k !== rowKey);
    const isIsolated = prev.size === others.length && others.every(k => prev.has(k));
    return isIsolated ? new Set() : new Set(others);
  });

  // "Fetch this window" — promote the visual zoom into the page range so the
  // backend re-buckets at the finer step (plan chart-wrapper addition #1).
  const fetchZoomWindow = () => {
    if (!zoomWindow) return;
    setRange({
      preset: 'custom',
      fromMs: Math.floor(zoomWindow.from * 1000),
      toMs: Math.ceil(zoomWindow.to * 1000),
    });
    // Pencere sayfa aralığına terfi etti — lokal zoom yığını artık eski
    // aralığa işaret eder, temizle (çift-tık bayat pencereye dönmesin).
    zoomStackRef.current = [];
    setZoomWindow(null);
  };

  // ── Query-console zone styling (unchanged visual language) ───────────────
  const ZONE: CSSProperties = {
    display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap',
    padding: '9px 12px', borderTop: '1px solid var(--border)',
  };
  const ZONE_FIRST: CSSProperties = { ...ZONE, borderTop: 'none' };
  const ZONE_LABEL: CSSProperties = {
    width: 64, flexShrink: 0, fontSize: 10.5, fontWeight: 700,
    letterSpacing: '.5px', color: 'var(--text3)', textTransform: 'uppercase',
  };
  const VDIV: CSSProperties = {
    width: 1, alignSelf: 'stretch', background: 'var(--border)', margin: '0 2px',
  };

  // v0.9.562 — GİRİŞ EKRANI KALDIRILDI (operatör: "Explore'da 'hangi
  // neden yavaş' gibi şeyler kullanışsız… ben Dynatrace data explorer
  // gibi düşünmüştüm", ardından "soru kartlarına gerek yok").
  //
  // Paramsız /explore artık doğrudan sorgu builder'ına iniyor. Kartlar
  // bir soru listesiydi; Data Explorer bir ARAÇTIR — operatör metriği
  // seçer, böler, toplar, çizer.
  //
  // Boş durumda HİÇBİR SORGU ATILMIYOR ve bu tesadüf değil: hasParams
  // false iken builderFrom 0 kalıyor (yukarıda), react-query devre dışı.
  // Yani builder görünür ama sessiz — Dynatrace'in boş metrik
  // seçicisiyle aynı davranış. Operatör builder'a dokununca param
  // yazılıyor (setHasParams) ve sorgu o zaman açılıyor. Kartları
  // kaldırmanın bedava olmasının sebebi bu kapının ZATEN doğru yerde
  // durması.

  return (
    <>
      <Topbar title="Explore" range={range} onRangeChange={setRange} envApplies />
      <PageShell>
        {/* v0.9.942 (B2) — GÖRÜNÜR KAPSAM. Enjekte edilen çipler bilerek
            çip satırında değil (silinebilir olsalardı picker'la yalana
            düşerdi), ama GÖRÜNMEZ de olamazlar: EndpointDetail'in aynı
            rozeti ("geldiğin liste bu kapsama daraltılmıştı") burada da
            bulunmalı, yoksa operatör neden az veri gördüğünü bilemez. */}
        {scopeChips(env, clusterScope).length > 0 && (
          <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8 }}>
            Kapsam:{' '}
            {env && <span className="mono">env={env}</span>}
            {env && clusterScope && ' · '}
            {clusterScope && <span className="mono">cluster={clusterScope}</span>}
            {' '}— bu daraltma her sorguya uygulanıyor.
          </div>
        )}
        <div style={{
          background: 'var(--bg2)', border: '1px solid var(--border)',
          borderRadius: 'var(--radius)', marginBottom: 12,
        }}>

          {/* SOURCE zone */}
          <div style={ZONE_FIRST}>
            <span style={ZONE_LABEL}>Source</span>
            <div className="segmented">
              {(['spans', 'metrics', 'logs'] as Source[]).map(s => (
                <button key={s} type="button" onClick={() => setSource(s)}
                  className={source === s ? 'active' : ''}
                  style={{ textTransform: 'capitalize' }}>
                  {s}
                </button>
              ))}
            </div>
            <span style={{ flex: 1 }} />
            {/* v0.9.849 — halka artık GÖRÜNÜR. Kayıtlı görünümlerin yanı,
                çünkü ikisi de "daha önce kurduğum sorguya dön" ailesinden;
                fark kalıcılık (saved_views, paylaşılabilir) ile bu
                tarayıcıya özgü son 4 kayıt arasında. */}
            <RecentQueries history={history} onApply={applyHistory} />
            <SavedViewsBar page="explore" />
            {/* sm: this bar is SavedViewsBar's sm buttons, not bare ones. */}
            <ShareButton size="sm" />
          </div>

          {source === 'spans' && (<>

          {/* SHOW zone — result mode + (metric mode) viz rail + step */}
          <div style={ZONE}>
            <span style={ZONE_LABEL}>Show</span>
            <div className="segmented">
              <button type="button" onClick={() => switchResultMode('traces')}
                className={resultMode === 'traces' ? 'active' : ''}>
                ⋮ Traces
              </button>
              <button type="button" onClick={() => switchResultMode('metric')}
                className={resultMode === 'metric' ? 'active' : ''}>
                ∿ Metric
              </button>
              <button type="button" onClick={() => switchResultMode('repeats')}
                className={resultMode === 'repeats' ? 'active' : ''}
                title="Find traces where the same span shape repeats N+ times (N+1 / chatty-RPC detector)">
                ⟳ Repeats
              </button>
            </div>
            {resultMode === 'metric' && (
              <>
                <span style={VDIV} />
                <VizRail value={builder.viz} onChange={v => setBuilder(b => ({ ...b, viz: v }))}
                  disabled={vizDisabledFor(builder.queries.map(queryUnit))} />
                <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 4 }}>Step:</span>
                <select value={builder.step}
                  onChange={e => setBuilder(b => ({ ...b, step: Number(e.target.value) }))}
                  title="Bucket genişliği — formül hizası için tüm sorgularda ortak">
                  {STEP_OPTIONS.map(o => <option key={o.v} value={o.v}>{o.label}</option>)}
                </select>
                <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 4 }}>Top:</span>
                <select value={builder.topN ?? PANEL_SERIES_CAP}
                  onChange={e => setBuilder(b => ({ ...b, topN: Number(e.target.value) }))}
                  title="En çok alanı kaplayan ilk N seriyi göster (Uptrace top10) — kalan seriler '+N more' olarak gizlenir">
                  {TOP_N_OPTIONS.map(o => <option key={o} value={o}>{o}</option>)}
                </select>
                {/* v0.8.418 (DE3) — log10 y-axis. Dynatrace Data-Explorer
                    affordance for series spanning decades (p50 vs p99, RPS
                    across hot+cold services). Rides ?q= like every other
                    builder knob; only meaningful on the line/area/bars set.

                    v0.9.788 — 'stacked' bu kümeden ÇIKTI. Yığılmış alanda
                    çizilen sayı katmanın kendi değeri değil kümülatif
                    toplamıdır; logaritmik eksende katman KALINLIKLARI
                    değerleriyle orantısını tamamen kaybeder (alttaki 10
                    birim panelin yarısı, üstteki 90 birim kalan yarısı).
                    Okunan şekil veriyi anlatmaz — toggle görünmez, ve
                    aşağıda logScale de stacked'te geçirilmez (bayrak ?q='de
                    kalmış olabilir; görünmeyen bir anahtarın sessizce
                    çalışması daha kötü olurdu). */}
                {(builder.viz === 'line' || builder.viz === 'area' || builder.viz === 'bars') && (
                  <div className="segmented" style={{ marginLeft: 4 }}>
                    <button type="button"
                      onClick={() => setBuilder(b => ({ ...b, logY: !b.logY || undefined }))}
                      aria-pressed={!!builder.logY}
                      className={builder.logY ? 'active' : ''}
                      title="Logaritmik y-ekseni — on/off. Kademeler arası (ör. 5ms p50 ile 3s p99) serileri aynı panelde okunur kılar.">
                      log y
                    </button>
                  </div>
                )}
                {/* v0.9.824 — önceki döneme karşılaştırma. VARSAYILAN KAPALI
                    ve maliyeti başlıkta YAZIYOR: açık her kip, üreten her
                    sorgu için ikinci bir fan-out koşturur. Grafik ailesine
                    özel (heatmap'in hayaleti olmaz — heatmap'te karo başına
                    iki dönem üst üste binmez, mod seçilince şerit çıkmaz). */}
                {builder.viz !== 'heatmap' && (<>
                  <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 4 }}>Karşılaştır:</span>
                  <div className="segmented">
                    <button type="button"
                      onClick={() => setBuilder(b => ({ ...b, cmp: undefined }))}
                      aria-pressed={!builder.cmp}
                      className={!builder.cmp ? 'active' : ''}
                      title="Karşılaştırma kapalı — tek sorgu turu (varsayılan)">
                      Kapalı
                    </button>
                    {EXPLORE_COMPARE.map(m => (
                      <button key={m} type="button"
                        onClick={() => setBuilder(b => ({ ...b, cmp: m }))}
                        aria-pressed={builder.cmp === m}
                        className={builder.cmp === m ? 'active' : ''}
                        title={`${compareLabel(m)} ile karşılaştır — kesikli soluk hayalet çizgiler + Δ %. `
                          + 'Sorgu maliyetini İKİYE KATLAR: her sorgu bir de kaydırılmış pencerede koşar.'}>
                        {m === 'prev' ? 'Önceki' : m}
                      </button>
                    ))}
                  </div>
                </>)}
              </>
            )}
            <span style={{ flex: 1 }} />
          </div>

          {/* ASK zone — NL query box stays in the builder (D5). Applies its
              filter set to query A + the page range. */}
          {resultMode === 'metric' && (
            <div style={ZONE}>
              <span style={ZONE_LABEL}>Ask</span>
              <div style={{ flex: 1, minWidth: 240 }}>
                <NLQueryBox
                  onApply={(nlFilters, preset) => {
                    setBuilder(b => ({
                      ...b,
                      queries: b.queries.map((q, i) =>
                        i === 0 ? { ...q, filters: nlFilters as FilterExpr[] } : q),
                    }));
                    setRange({ preset });
                  }} />
              </div>
            </div>
          )}

          {/* QUERY rows — the A–D builder (metric mode) */}
          {resultMode === 'metric' && (
            <>
              {builder.queries.map((q, i) => (
                <QueryRow key={q.letter} q={q}
                  canRemove={builder.queries.length > 1}
                  canDuplicate={builder.queries.length < MAX_QUERIES}
                  onChange={nq => setQuery(i, nq)}
                  onDuplicate={() => duplicateQuery(i)}
                  onRemove={() => removeQuery(i)} />
              ))}
              <div style={ZONE}>
                <span style={ZONE_LABEL}>Formula</span>
                <FormulaRow value={builder.formula}
                  onChange={f => setBuilder(b => ({ ...b, formula: f }))}
                  letters={builder.queries.filter(produces).map(q => q.letter)} />
                <span style={VDIV} />
                <Button variant="secondary" onClick={addQuery}
                  disabled={builder.queries.length >= MAX_QUERIES}
                  title={builder.queries.length >= MAX_QUERIES ? 'En fazla 4 sorgu (A–D)' : 'Yeni sorgu ekle'}>
                  + Sorgu
                </Button>
              </div>
            </>
          )}

          {/* FILTER zone — traces/repeats console (pre-v2 shape) */}
          {resultMode !== 'metric' && (
            <div style={{ ...ZONE, alignItems: 'flex-start' }}>
              <span style={{ ...ZONE_LABEL, marginTop: 5 }}>Filter</span>
              <div className="segmented" style={{ marginTop: 1 }}>
                <button type="button" onClick={() => setMode('builder')}
                  className={mode === 'builder' ? 'active' : ''}>
                  Builder
                </button>
                <button type="button" onClick={() => setMode('advanced')}
                  className={mode === 'advanced' ? 'active' : ''}>
                  Advanced
                </button>
              </div>
              <div style={{ flex: 1, minWidth: 240 }}>
                {mode === 'builder' && (
                  /* v0.10.270 — Traces'ın tek satır sorgu kutusu (264) Explore'a;
                     FilterBuilder yalnız gruplu (AND/OR) yaprakları için kalır. */
                  <FilterQueryBox value={filters} onChange={setFilters}
                    recentKey="explore-recent-filters"
                    suggestedValues={{
                      'service.name': services,
                      'resource.service.name': services,
                      'kind': ['internal', 'server', 'client', 'producer', 'consumer'],
                      'status_code': ['ok', 'error', 'unset'],
                      'http.method': ['GET', 'POST', 'PUT', 'DELETE', 'PATCH'],
                      'db.system': ['postgresql', 'mysql', 'redis', 'mongodb', 'elasticsearch'],
                    }} />
                )}
                {mode === 'advanced' && (
                  <div className="adv-query">
                    <textarea value={dsl}
                      onChange={e => setDsl(e.target.value)}
                      spellCheck={false}
                      placeholder={`# Examples — one condition per line
duration > 500ms
service.name = "frontend"
http.status_code >= 500
status_code = error
peer.service = "payment-service"
db.system in [postgresql, redis]
exception.type exists
name ~ checkout`}
                      rows={Math.max(4, dsl.split('\n').length + 1)} />
                    {queryError && <div className="trp-error" style={{ marginTop: 6 }}>{queryError}</div>}
                    <div style={{ marginTop: 4, fontSize: 11, color: 'var(--text3)' }}
                      title="One condition per line · operators: = != > >= < <= ~ !~ in [a,b] exists · prefix resource./span. to scope · duration accepts 500ms, 1.5s, 2m">
                      Conditions are AND-joined · prefix with <code>resource.</code> or <code>span.</code> to scope ·
                      <code>duration</code> accepts <code>500ms</code>, <code>1.5s</code>, <code>2m</code>
                    </div>
                  </div>
                )}
              </div>
            </div>
          )}

          {/* RESULT zone — traces mode: limit + "showing N of M". */}
          {resultMode === 'traces' && (
            <div style={ZONE}>
              <span style={ZONE_LABEL}>Result</span>
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>Limit:</span>
              <select value={traceLimit} onChange={e => setTraceLimit(Number(e.target.value))}>
                {[20, 50, 100, 200, 500, 1000, 2000, 5000].map(n => <option key={n} value={n}>{n} traces</option>)}
              </select>
              {traces && traceTotal !== undefined && traceTotal > 0 && (
                <span style={{
                  color: traces.length >= traceLimit && traceTotal > traces.length
                    ? 'var(--err)' : 'var(--text2)',
                  fontSize: 12, fontWeight: 600,
                }}>
                  Showing {fmtNum(traces.length)} of {fmtNum(traceTotal)}
                  {traces.length >= traceLimit && traceTotal > traces.length && (
                    <> — raise limit to see more</>
                  )}
                </span>
              )}
              {/* v0.9.284 — uncounted is the DEFAULT now, so it gets a
                  first-class label instead of rendering nothing. "50+"
                  is what the page actually knows; the count is one
                  click away. */}
              {traces && traces.length > 0 && traceTotal === undefined && (
                <span style={{
                  color: traceHasMore ? 'var(--err)' : 'var(--text2)',
                  fontSize: 12, fontWeight: 600,
                }}>
                  Showing {fmtNum(traces.length)}{traceHasMore ? '+' : ''}
                  {' · '}
                  <a href="#" onClick={e => { e.preventDefault(); setShowTotal(true); }}
                    title="Run an exact count(DISTINCT trace_id) over the window — can be slow at scale">
                    count total
                  </a>
                </span>
              )}
              <span style={{ color: 'var(--text3)', fontSize: 11, marginLeft: 'auto' }}>
                Sorted by start time desc
              </span>
            </div>
          )}

          {/* REPEATS zone — presets + shape key + Min repeats. */}
          {resultMode === 'repeats' && (
            <div style={ZONE}>
              <span style={ZONE_LABEL}>Repeats</span>
              {REPEAT_PRESETS.map(p => {
                const active = p.minRepeats === repeatMin
                  && p.groupBy.length === repeatGroupBy.length
                  && p.groupBy.every((k, i) => repeatGroupBy[i] === k);
                return (
                  <Button key={p.key}
                    title={p.hint}
                    onClick={() => {
                      setRepeatGroupBy(p.groupBy);
                      setRepeatMin(p.minRepeats);
                      if (p.filters && p.filters.length > 0) {
                        const extra = p.filters.filter(pf =>
                          !filters.some(x => x.k === pf.k && x.op === pf.op &&
                                              (x.v?.[0] ?? '') === (pf.v?.[0] ?? '')));
                        if (extra.length > 0) setFilters([...filters, ...extra]);
                      }
                    }}
                    variant={active ? 'primary' : 'secondary'} size="sm">
                    {p.label}
                  </Button>
                );
              })}
              <span style={VDIV} />
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>Shape:</span>
              <SplitByPicker value={repeatGroupBy} onChange={setRepeatGroupBy} />
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>Min repeats:</span>
              <select value={repeatMin} onChange={e => setRepeatMin(Number(e.target.value))}>
                {[2, 3, 5, 10, 20, 50, 100].map(n => <option key={n} value={n}>≥ {n}</option>)}
              </select>
            </div>
          )}

          </>)}
        </div>

        {/* Metrics + Logs source panels (until the Phase-5 collapse). */}
        {source === 'metrics' && (
          <MetricsExplorer range={range}
            viz={legacyViz}
            compare={false}
            initialService={searchParams.get('service') ?? ''}
            initialMetric={searchParams.get('metric') ?? ''} />
        )}
        {source === 'logs' && (
          <LogsExplorer range={range} viz={legacyViz} compare={false} />
        )}

        {source === 'spans' && (
          <div style={{ display: 'flex', gap: 14, alignItems: 'flex-start' }}>
            <div style={{ flex: 1, minWidth: 0 }}>

        {/* ── Metric mode · panel stack ─────────────────────────────────────── */}
        {resultMode === 'metric' && debounced.viz !== 'heatmap' && (
          <>
            {/* v0.9.804 — HER başarısız harf listelenir. Tek satır yalnız
                ilkini basıyordu; iki sorgu birlikte patladığında ikincinin
                sebebi hiçbir yerde görünmüyordu. */}
            {builderErrors.length > 0 && (
              <div className="trp-error" style={{ marginBottom: 10 }}>
                {builderErrors.map(([letter, message]) => (
                  <div key={letter}>Sorgu {letter} hata verdi: {message}</div>
                ))}
              </div>
            )}
            {!anyProduces && (
              <Empty icon="◎" title="Aktif sorgu yok">
                Bir sorguyu aç (harf rozetine tıkla) ya da metric-source sorgusuna bir metrik seç.
              </Empty>
            )}
            {anyProduces && (
              <>
                {zoomWindow && (
                  <div style={{
                    display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8,
                    fontSize: 12, color: 'var(--text2)',
                  }}>
                    <span>🔍 Zoom aktif — tüm paneller senkron</span>
                    <Button variant="secondary" size="sm"
                      onClick={() => { zoomStackRef.current = []; setZoomWindow(null); }}>Sıfırla</Button>
                    <Button variant="secondary" size="sm"
                      onClick={fetchZoomWindow}
                      title="Zoom penceresini sayfa aralığı yap — backend daha ince bucket'larla yeniden sorgular">
                      Bu pencereyi getir →
                    </Button>
                  </div>
                )}
                {panels.length === 0 && anyLoading && <Spinner />}
                {(debounced.viz === 'line' || debounced.viz === 'area' || debounced.viz === 'bars' || debounced.viz === 'stacked') && (
                  <PanelStack panels={panels}
                    viz={debounced.viz}
                    logScale={debounced.viz !== 'stacked' && !!debounced.logY}
                    hiddenKeys={hiddenKeys}
                    focusKey={focusKey}
                    zoomWindow={zoomWindow}
                    xRange={xRangeSec}
                    onZoom={handlePanelZoom}
                    onZoomReset={handlePanelZoomReset}
                    onExemplarClick={(id) => setCorrelateAnchor({ kind: 'trace', traceId: id })}
                    pinnableLetters={pinnableLetters}
                    onPin={letter => {
                      const q = builder.queries.find(x => x.letter === letter);
                      // v0.9.786 — viz de gider. builder.viz (debounced değil):
                      // debounce yalnız SORGU alanları için; mark bir açılır
                      // menü seçimi, operatörün son niyeti anında geçerlidir.
                      const p = q && queryToPanel(q, {
                        step: builder.step || undefined, viz: builder.viz,
                      });
                      if (p) setPinPanel(p);
                    }} />
                )}
                {/* v0.9.809 — satır tavanı şeridi GRAFİKSİZ görünümlerde de.
                    Çizgi ailesinde her QueryPanel kendi başlığında zaten
                    söylüyor; table/stat/toplist/pie'da panel yok, uyarı da
                    yoktu. Şerit yalnız o dallarda çizilir — çizgi ailesinde
                    iki kez söylemek gürültü olurdu. */}
                {debounced.viz !== 'line' && debounced.viz !== 'area'
                  && debounced.viz !== 'bars' && debounced.viz !== 'stacked' && (
                  <RowsCappedNote panels={panels} />
                )}
                {(debounced.viz === 'stat' || debounced.viz === 'toplist' || debounced.viz === 'pie') && (
                  <SummaryViz panels={panels} mode={debounced.viz} />
                )}
                {/* 'table' viz: no primary panel — the GroupTable below IS the view. */}
                <GroupTable panels={panels}
                  hiddenKeys={hiddenKeys}
                  onToggleHidden={toggleHidden}
                  onIsolate={isolateHidden}
                  onFocus={setFocusKey}
                  onPivot={pivotFromRow}
                  sourceTarget={sourceTargetFor}
                  onOpenSource={openSourceFromRow} />
              </>
            )}
          </>
        )}

        {/* v0.9.939 (C2) — YENİLEME İZİ. Sonuç artık her düzenlemede
            boşalmıyor (keep-previous), dolayısıyla operatöre "bu hâlâ ESKİ
            pencerenin cevabı" demenin bir yolu gerekiyor: sessiz bir
            keep-previous, bayat veriyi taze gibi gösterirdi. */}
        {busy && (
          (resultMode === 'metric' && debounced.viz === 'heatmap' && heatmap !== undefined)
          || (resultMode === 'traces' && traces !== undefined)
          || (resultMode === 'repeats' && repeats !== undefined)
        ) && (
          <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 6 }}>
            yenileniyor… (gösterilen sonuç bir önceki sorgudan)
          </div>
        )}

        {/* ── Metric mode · heatmap viz (query A drives it) ────────────────── */}
        {resultMode === 'metric' && debounced.viz === 'heatmap' && (
          <>
            {heatmap === undefined && <Spinner />}
            {heatmap === null && (
              <Empty icon="◎" title="No data for this query">
                {heatmapNote ?? 'Try a wider time range or fewer filters.'}
              </Empty>
            )}
            {heatmap && heatmap.maxCount === 0 && (
              // v0.9.1202 — metrik-histogram yolunda sunucunun "neden boş"
              // notu varsa varsayılan cümlenin yerine geçer (VM: "_bucket
              // serisi bulunamadı — histogram olmayabilir").
              <Empty icon="◎" title={heatmapNote ? 'Bu pencerede örnek yok' : 'No spans matched in this window'}>
                {heatmapNote ?? undefined}
              </Empty>
            )}
            {heatmap && heatmap.maxCount > 0 && (() => {
              // Jest kapısı: hücre tıkı (örnek trace) ve kutu seçimi
              // (BubbleUp) SPAN yüzeyinin kavramları. Metrik histogramında
              // hücre bir örnekleme kovasıdır — "2-4 ms'lik trace'ler"
              // modali yanlış modal olurdu (pano panelinin v0.9.947 kapısı
              // ile aynı gerekçe).
              const aQ = debounced.queries.find(produces);
              const isMetricHeat = aQ?.source === 'metric';
              const noun = heatmap.countNoun ?? 'spans';
              return (
              <div style={{
                background: 'var(--bg1)', border: '1px solid var(--border)',
                borderRadius: 8, padding: 14,
              }}>
                <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 8 }}>
                  {isMetricHeat ? 'Histogram density' : 'Latency density'} · sorgu A filtreleri · {heatmap.times.length} time buckets ×
                  {' '}{heatmap.durationBins.length} {isMetricHeat ? 'bucket bins' : 'log-scale latency bins'}
                  · peak cell {heatmap.maxCount.toLocaleString()} {noun}
                </div>
                <LatencyHeatmap data={heatmap}
                  onCellClick={isMetricHeat ? undefined : (cell) => setCellExemplar({
                    timeNs: cell.timeNs,
                    lowDurMs: cell.lowDurMs,
                    highDurMs: cell.highDurMs,
                    count: cell.count,
                    exemplarTraceId: cell.exemplarTraceId,
                  })}
                  onBoxSelect={isMetricHeat ? undefined : setBoxSel} />
                {!isMetricHeat && (
                <div style={{
                  display: 'flex', alignItems: 'center', gap: 10,
                  fontSize: 10, color: 'var(--text3)', marginTop: 6,
                }}>
                  <span>Tek hücre = örnek trace · sürükle (kutu seç) = BubbleUp</span>
                  {/* v0.9.1113 — Honeycomb çekirdek döngüsünün tek-tık kapısı. */}
                  <Button variant="ghost" size="sm" onClick={() => setErrDiff(v => !v)}
                    title="Bu penceredeki hatalı span'ları aynı sorgunun tamamıyla kıyasla — hangi attribute'lar hatalarda farklı?">
                    Hatalarda ne farklı?
                  </Button>
                </div>
                )}
                {!isMetricHeat && errDiff && (() => {
                  const a = debounced.queries.find(produces);
                  const baseline = a ? effectiveFilters(a) : [];
                  const baselineDsl = a && a.dsl.trim() ? a.dsl : undefined;
                  return (
                    <div style={{ marginTop: 12 }}>
                      <div style={{
                        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
                        fontSize: 11, color: 'var(--text2)',
                      }}>
                        <span>Hata diff'i · status=error vs sorgunun tamamı · tüm pencere</span>
                        <Button variant="ghost" size="sm" onClick={() => setErrDiff(false)}
                          title="Hata diff'ini kapat">✕</Button>
                      </div>
                      <BubbleUpPanel
                        baseline={baseline}
                        baselineDsl={baselineDsl}
                        selection={[{ k: 'status', op: '=', v: ['error'] }]}
                        from={exploreRange.from}
                        to={exploreRange.to}
                        onApplyFilter={(f) => {
                          if (a) {
                            setBuilder(b => ({
                              ...b,
                              queries: b.queries.map(q =>
                                q.letter === a.letter ? { ...q, filters: [...q.filters, f] } : q),
                            }));
                          }
                          setErrDiff(false);
                        }} />
                    </div>
                  );
                })()}
                {boxSel && (() => {
                  const a = debounced.queries.find(produces);
                  const baseline = a ? effectiveFilters(a) : [];
                  const baselineDsl = a && a.dsl.trim() ? a.dsl : undefined;
                  // Latency band → duration_ms filter pair (well-known field →
                  // (duration / 1e6) in chstore/filterexpr.go). Time bounds ride
                  // the BubbleUp from/to. Selection narrows the same-window
                  // baseline to the dragged latency band.
                  const selection: FilterExpr[] = [
                    { k: 'duration_ms', op: '>=', v: [String(Math.max(0, boxSel.lowDurMs))] },
                    { k: 'duration_ms', op: '<=', v: [String(boxSel.highDurMs)] },
                  ];
                  const tFmt = (ns: number) => fmtClock(ns / 1e6);
                  return (
                    <div style={{ marginTop: 12 }}>
                      <div style={{
                        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
                        fontSize: 11, color: 'var(--text2)',
                      }}>
                        <span>
                          Seçim · {fmtNum(boxSel.count)} span · {tFmt(boxSel.timeFromNs)}–{tFmt(boxSel.timeToNs)}
                          {' '}· {boxSel.lowDurMs >= 1 ? Math.round(boxSel.lowDurMs) : boxSel.lowDurMs.toFixed(1)}–{Math.round(boxSel.highDurMs)} ms
                        </span>
                        <Button variant="ghost" size="sm" onClick={() => setBoxSel(null)}
                          title="Seçimi kapat">✕</Button>
                      </div>
                      <BubbleUpPanel
                        baseline={baseline}
                        baselineDsl={baselineDsl}
                        selection={selection}
                        from={boxSel.timeFromNs}
                        to={boxSel.timeToNs}
                        onApplyFilter={(f) => {
                          if (a) {
                            setBuilder(b => ({
                              ...b,
                              queries: b.queries.map(q =>
                                q.letter === a.letter ? { ...q, filters: [...q.filters, f] } : q),
                            }));
                          }
                          setBoxSel(null);
                        }} />
                    </div>
                  );
                })()}
              </div>
              );
            })()}
          </>
        )}

        {/* ── Traces mode ──────────────────────────────────────────────────── */}
        {resultMode === 'traces' && (
          <TracesResult
            traces={traces}
            traceTotal={traceTotal}
            traceHasMore={traceHasMore}
            onShowTotal={() => setShowTotal(true)}
            extraCols={extraCols}
            setExtraCols={setExtraCols}
            // Çift-render koruması: DSL hatası zaten textarea'nın altında
            // harfi harfine gösteriliyor; aynı metni sonuç alanında ikinci
            // kez basmayalım — QueryError genel cümlesiyle kalsın.
            errorText={queryError ? null : readErr}
            onRetry={() => setRetryNonce(n => n + 1)} />
        )}

        {/* ── Repeats mode ─────────────────────────────────────────────────── */}
        {resultMode === 'repeats' && (
          <RepeatsResult
            repeats={repeats}
            repeatMin={repeatMin}
            groupBy={repeatGroupBy}
            // Çift-render koruması: DSL hatası zaten textarea'nın altında
            // harfi harfine gösteriliyor; aynı metni sonuç alanında ikinci
            // kez basmayalım — QueryError genel cümlesiyle kalsın.
            errorText={queryError ? null : readErr}
            onRetry={() => setRetryNonce(n => n + 1)} />
        )}
            </div>
          </div>
        )}

        {/* Heatmap cell-click exemplars modal (query A's filter context). */}
        {cellExemplar && (() => {
          const bucketWidthNs = (heatmap && heatmap.times.length >= 2)
            ? heatmap.times[1] - heatmap.times[0]
            : 60 * 1e9;
          const a = debounced.queries.find(produces);
          return (
            <HeatmapCellExemplars
              cell={cellExemplar}
              exemplarTraceId={cellExemplar.exemplarTraceId}
              bucketWidthNs={bucketWidthNs}
              filters={a ? effectiveFilters(a) : []}
              dsl={a && a.dsl.trim() ? a.dsl : undefined}
              onClose={() => setCellExemplar(null)} />
          );
        })()}

        {/* Correlated Signals pivot drawer (task #6) — anchored on the exemplar
            ◆ the operator clicked. Keeps them in Explore instead of navigating
            to /trace. */}
        <CorrelationContextDrawer
          anchor={correlateAnchor}
          onClose={() => setCorrelateAnchor(null)} />
        {pinPanel && (
          <PinToDashboardModal panel={pinPanel}
            onClose={() => setPinPanel(null)} />
        )}
      </PageShell>
    </>
  );
}

// hasMeaningfulParams — true when the URL carries a real query (any param
// other than `range`). Unchanged from Phase-1 — tanım v0.9.805'te
// exploreRouteKey.ts'e taşındı (remount kararıyla AYNI kural olmak
// zorunda; iki kopya sessizce ayrışırdı).

// legacyHistoryDesc — recent-queries label for the traces/repeats console.
function legacyHistoryDesc(s: {
  resultMode: ResultMode; mode: 'builder' | 'advanced'; dsl: string;
  filters: FilterExpr[]; repeatMin: number; repeatGroupBy: string[];
}): string {
  const where = s.mode === 'advanced'
    ? (s.dsl.trim() ? s.dsl.trim().replace(/\s+/g, ' ').slice(0, 60) : 'all spans')
    : (s.filters.length
        ? s.filters.map(f => `${f.k}${f.op}${(f.v ?? []).join('|')}`).join(' · ').slice(0, 60)
        : 'all spans');
  if (s.resultMode === 'repeats') {
    const shape = s.repeatGroupBy.length ? s.repeatGroupBy.join(' + ') : 'db.statement';
    return `Repeats ≥${s.repeatMin} · ${shape} · ${where}`;
  }
  return `Traces · ${where}`;
}

export default function ExplorePage() {
  // v0.9.805 — anahtar artık yalnız giriş↔workspace SINIRINI değil, SORGUYU
  // da taşıyor. Eskiden çalışan bir Explore'da saved view / derin link
  // uygulamak URL'i değiştiriyor ama anahtar sabit kaldığı için remount
  // olmuyordu: ExploreInner URL'i mount başına bir kez okur, dolayısıyla
  // ekran eski sorguda kalıyor ve ilk düzenlemede state→URL yazımı saved
  // view'i tamamen eziyordu (one-way-read sınıfının 4. vakası).
  //
  // Kendi yazımımız remount ETMEZ: ExploreInner navigate'ten önce URL'ini
  // bildiriyor, nextExploreKey o imzayı görünce anahtarı sabit tutuyor.
  // Karar saf ve tablo-testli (exploreRouteKey.ts).
  const { search } = useLocation();
  const selfWriteSigRef = useRef<string | null>(null);
  const keyRef = useRef<ExploreKeyState | null>(null);
  const onSelfWrite = useCallback((next: string) => {
    selfWriteSigRef.current = exploreQuerySig(next);
  }, []);
  // Render sırasında türetiliyor: aynı search ile ikinci kez çağrılmak
  // sonucu değiştirmez (prev.sig === sig → prev döner), o yüzden StrictMode
  // çift render'ı güvenli.
  keyRef.current = nextExploreKey(keyRef.current, search, selfWriteSigRef.current);
  return (
    <Suspense fallback={<Spinner />}>
      <ExploreInner key={keyRef.current.key} onSelfWrite={onSelfWrite} />
    </Suspense>
  );
}
