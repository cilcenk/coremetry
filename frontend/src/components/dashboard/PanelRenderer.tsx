import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import type {
  Panel, MetricPanelConfig, SpanMetricPanelConfig, StatPanelConfig, GaugePanelConfig, MarkdownPanelConfig,
  HeatmapPanelConfig, HistogramResult, LatencyHeatmap as HeatmapData, PromqlPanelConfig,
  SpanMetricSeries, TimeRange, PanelHeight, TopNPanelConfig, PanelVizType,
} from '@/lib/types';
import { timeRangeToNs, substituteVars } from '@/lib/utils';
import { fmtSmart } from '@/lib/chartFmt';
import { lazy, Suspense } from 'react';

// v0.9.758 (operatör "önerinle devam" — Grafana deneyimi #1) — dashboard
// LINE panelleri CorePanel'de: "her yerde aynı grafik"in son büyük
// istisnası buydu; Explore'dan pinlenen panel artık pinlendiği gibi
// görünür.
//
// v0.9.796 (mockup onaylı) — BAR ve ALAN da bu hattan geçiyor. Ayrı SVG
// motorunda kalmalarının görünür bedeli vardı: eski SVG motorunda drag-zoom
// YOK, imleç senkronu YOK, lejant istatistiği YOK. Aynı dashboard'da bir
// çizgi panelini sürükleyip yakınlaştırmak çalışırken yanındaki bar paneli
// hiç tepki vermiyordu. Geçişle üçü birden GELİYOR — mockup'ın vaadi ise
// imleç-altı değer satırının kalkıp bilginin tooltip (üst-8, Shift+tık
// sabitler) + istatistik lejantına taşınması.
//
// v0.9.808 — YIĞIN AİLESİ de bu hattan geçiyor: stacked-area CorePanel'in
// 'stacked'ına, stacked-bar yeni 'stacked-bars' markına düşer. Beş markın
// beşi de v2'de, yani dashboard'da artık iki farklı grafik motoru YOK.
//
// v0.9.844 — MOTOR KAÇIŞ DALI ve onunla birlikte eski SVG motoru
// (DashboardViz.tsx) SİLİNDİ (operatör onayı: "eski motoru tamamen
// sök"). Bu fonksiyonda artık dallanma YOK: her mark, her panel tipi,
// tek motor.
// stat/gauge/heatmap/markdown hâlâ kapsam dışı — onlar zaman serisi değil.
const DashCorePanelLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));
// v0.9.946 (D3/Ö25) — MLC'nin kullandığı SAF katlama çekirdeği; ikinci
// bir kopya oran birimlerindeki ortalama/toplam ayrımını bir gün
// kaybederdi (foldTopN.ts sözleşmesi).
import { foldTopN, foldNote, isOthersSeries } from '@/lib/chart/foldTopN';

function DashChart({ series, viz = 'line', unit, syncKey, onZoom, onZoomReset, storageKey, height }: {
  series: import('@/lib/types').SpanMetricSeries[];
  // v0.9.808 — dashboard'ın BEŞ markı da buraya gelir; ayıklama yok.
  viz?: PanelVizType;
  unit?: string;
  syncKey?: string;
  onZoom?: (f: number, t: number) => void;
  onZoomReset?: () => void;
  storageKey: string;
  // v0.9.778 — resolved pixel height (panelChartHeight of Panel.height).
  // Absent → the pre-v0.9.778 280.
  height?: number;
}) {
  const h = height ?? panelChartHeight();
  // v0.9.946 (UX denetimi D3 / Ö25) — "others" KATLAMASI dashboard'a da.
  //
  // foldTopN v0.9.807'de MultiLineChart adaptöründe kalmıştı; DashChart
  // her seriyi çiziyordu. Group-by'lı yüksek-kardinaliteli bir panel
  // (route kırılımı, pod kırılımı) okunmaz bir spagetti + N satırlık
  // lejant oluyordu — ve DAHA KÖTÜSÜ: kırpma OLMADIĞI için dürüstlük
  // notu da yoktu, yani operatör panelin her şeyi gösterip
  // göstermediğini bilemiyordu. Aynı sorgu Explore'da 8 seri + "+N
  // katlandı" notu, dashboard'da 60 çizgi olarak görünüyordu.
  //
  // AYNI saf çekirdek (ikinci bir kopya, oran birimlerinde toplama/
  // ortalama ayrımını bir gün kaybederdi). ≤8 seride foldTopN girdiyi
  // AYNEN döndürür — mevcut panellerin çıktısı bayt-bayt aynı.
  const folded = foldTopN(series, unit);
  const note = foldNote(series.length);
  return (
    <Suspense fallback={<div style={{ height: h, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
      <DashCorePanelLazy
        title=""
        storageKey={storageKey}
        height={h}
        unit={unit}
        viz={toCoreViz(viz)}
        // Kırpma SESSİZ OLAMAZ: notu vermeden katlamak, spagettiyi
        // "temiz panel" sanmakla aynı sınıfa girerdi.
        note={note}
        items={folded.map((s0, i) => ({
          name: s0.groupKey?.length ? s0.groupKey.join(' · ') : `seri ${i + 1}`,
          // Katlanan kuyruk 'muted' rolde: uzun kuyruk sessiz gri kalır
          // (MLC yolundaki davranışın aynısı, seriesRoleColor üzerinden).
          role: isOthersSeries(s0) ? ('muted' as const) : ('data' as const),
          series: [s0],
        }))}
        syncKey={syncKey}
        onZoom={onZoom} onZoomReset={onZoomReset}
      />
    </Suspense>
  );
}
import { LatencyHeatmap } from '../LatencyHeatmap';
import { histogramResultToHeatmap } from './histogramHeatmap';
// v0.9.947 (D4/Ö24) — pano heatmap'inin jest kapısı + pivot linki.
import { heatmapPivotable, heatmapTracesHref } from './heatmapPivot';
import { HeatmapCellExemplars } from '@/components/HeatmapCellExemplars';
import { Spinner } from '../Spinner';
import { effectivePanelStep } from './panelStep';
import { usePanelWidth } from './usePanelWidth';
// v0.9.778 — band palette + the S/M/L pixel map. Shared with PanelEditor so
// the editor's swatch and the panel's paint can never drift apart.
import { THRESHOLD_COLOURS, thresholdTint, panelBoxHeight, panelChartHeight } from './panelChrome';
// v0.9.781 — Top-N bar panel's pure core (single-bucket step, limit clamp,
// row reduction, "+N more"). Lives in its own module so the arithmetic is
// unit-tested without mounting React.
import { topNStep, topNWindowSec, clampTopNLimit, topNRowValue, topNMoreLabel, topNRowFilters } from './topN';
// v0.9.796 — mark → motor kararı tek saf yerde (üç panel de aynı çekirdeği
// çağırır; kopya dallanma v0.9.790'da metric panelini geride bırakmıştı).
import { toCoreViz } from './panelViz';
import { tracesPivotHref } from '@/lib/pivotHref';
import { serviceHref } from '@/lib/serviceHref';
import { encodeFilters } from '@/lib/urlState';
import { Link } from 'react-router-dom';

// PanelRenderer dispatches on panel.type. Self-contained — fetches its
// own data, re-fetches when `range` changes. Errors are surfaced inline
// instead of crashing the whole dashboard.
// PanelDataOverride lets a parent (Dashboard.tsx) pre-fetch
// all panels' data in one bundle round-trip and pass each
// result down so the individual panel components skip their
// own fetch. When series is null AND error is undefined the
// override is treated as "not yet bundled — fall through to
// the panel's own fetch path" so partial bundles don't
// blank out the entire grid.
export type PanelDataOverride = {
  series?: SpanMetricSeries[] | null;
  // v0.9.459 (dürüstlük A1b) — 50k satır tavanı: alfabetik-son seriler
  // eksik olabilir; panel köşesinde ⚠ ipucu.
  rowsCapped?: boolean;
  error?: string;
} | undefined;

// CapHint — v0.9.459: 50k satır tavanı dolduğunda panel köşesi ⚠.
// Şerit değil ipucu: dashboard panelleri yoğun, tooltip nedeni anlatır.
function CapHint() {
  return (
    <span title="Sorgu 50k satır tavanına çarptı — seriler grup anahtarına göre ALFABETİK kesildi; geç harfli seriler eksik olabilir. Pencereyi daralt, adımı büyüt ya da filtre ekle."
      style={{
        position: 'absolute', top: 6, right: 8, zIndex: 2, cursor: 'help',
        fontSize: 11, color: 'var(--warn)',
      }}>⚠ kesik</span>
  );
}

export function PanelRenderer({ panel, range, vars, syncKey, onZoom, onZoomReset, refreshTick, dataOverride }: {
  panel: Panel;
  range: TimeRange;
  // Resolved values for the dashboard's variables (Grafana-style
  // ${name} references in DSL / service / groupBy fields). Empty
  // values cause the referenced predicate line to drop, so a panel
  // with `service.name = "${service}"` and no service picked behaves
  // as "no service filter" rather than failing.
  vars?: Record<string, string>;
  // Cursor-sync key. When set (one key per dashboard), every panel
  // on the page hovers in lockstep — Datadog / Grafana dashboard
  // pattern that turns 8 disconnected charts into one view.
  syncKey?: string;
  // onZoom — drag-to-zoom callback from the underlying chart.
  // Parent (Dashboard.tsx) re-points the global TimeRange so
  // every panel re-fetches for the new window. Receives unix
  // seconds.
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  // Grafana-parite M1 — çift-tık: Dashboard.tsx zoom geri-yığınını pop
  // eder (chart çizen panellere aynen iletilir).
  onZoomReset?: () => void;
  // v0.9.779 — auto-refresh sayacı (Dashboard.tsx). Her artış, KENDİ
  // fetch'ini yapan panelin effect'ini yeniden koşturur. Bundle'dan
  // beslenen metric / spanmetric panelleri için de bağımlılıkta duruyor:
  // orada override'ı yeniden uygulamaktan başka bir şey yapmaz (istek
  // yok), ama panel promql modundaysa ya da bundle o paneli
  // kapsamıyorsa gerçek refetch olur. Tazelemeyen bir "yeniliyorum"
  // düğmesi bırakmamanın tek yolu bu.
  refreshTick?: number;
  // Pre-fetched data from the dashboard bundle endpoint. When
  // provided, MetricPanel / SpanMetricPanel use it instead of
  // firing their own /api/{metrics,spans}/metric round trip.
  // Stat + markdown panels stay independent — they have their
  // own time-window semantics (stat doubles the window for the
  // prior-period delta) or no data at all.
  dataOverride?: PanelDataOverride;
}) {
  // v0.6.20 — per-panel time range override. When the panel has
  // its own rangeOverride set, it takes precedence over the
  // dashboard's Topbar range. A panel with a 60-day baseline can
  // sit beside a 15-min incident chart on the same dashboard.
  // dataOverride only applies when the panel is using the
  // dashboard's window (otherwise the bundled fetch was for the
  // wrong range); panels with their own override fall back to
  // their independent fetch.
  const effectiveRange = panel.rangeOverride ?? range;
  const effectiveDataOverride = panel.rangeOverride ? undefined : dataOverride;
  // v0.9.778 — S/M/L travels as a COMPONENT PROP, never inside `config`:
  // StatPanel / GaugePanel / HeatmapPanel key their fetch effect on
  // JSON.stringify(cfg), so a height living in config would fire a fresh
  // ClickHouse query every time the operator resized a tile.
  const h = panel.height;
  switch (panel.type) {
    case 'metric':
      return <MetricPanel cfg={applyVarsToMetric(panel.config as MetricPanelConfig, vars)} range={effectiveRange} syncKey={syncKey} onZoom={onZoom} onZoomReset={onZoomReset} refreshTick={refreshTick} dataOverride={effectiveDataOverride} height={h} />;
    case 'spanmetric':
      return <SpanMetricPanel cfg={applyVarsToSpan(panel.config as SpanMetricPanelConfig, vars)} range={effectiveRange} syncKey={syncKey} onZoom={onZoom} onZoomReset={onZoomReset} refreshTick={refreshTick} dataOverride={effectiveDataOverride} height={h} />;
    case 'stat':
      return <StatPanel cfg={applyVarsToStat(panel.config as StatPanelConfig, vars)} range={effectiveRange} refreshTick={refreshTick} height={h} />;
    case 'gauge':
      return <GaugePanel cfg={applyVarsToGauge(panel.config as GaugePanelConfig, vars)} range={effectiveRange} refreshTick={refreshTick} height={h} />;
    case 'heatmap':
      return <HeatmapPanel cfg={applyVarsToHeatmap(panel.config as HeatmapPanelConfig, vars)} range={effectiveRange} refreshTick={refreshTick} height={h} />;
    case 'promql':
      return <PromqlPanel cfg={applyVarsToPromql(panel.config as PromqlPanelConfig, vars)} range={effectiveRange} syncKey={syncKey} onZoom={onZoom} onZoomReset={onZoomReset} refreshTick={refreshTick} height={h} />;
    // v0.9.781 — non-temporal (ranked bars, no time axis): no syncKey, no
    // onZoom — there is no cursor to sync and no x-range to drag. Carries
    // panelId instead so the dashboard's panel-actions menu can anchor to it
    // (frontend-dashboard-panel skill, step 9).
    case 'topn':
      return <TopNPanel cfg={applyVarsToTopN(panel.config as TopNPanelConfig, vars)} range={effectiveRange} refreshTick={refreshTick} height={h} panelId={panel.id} />;
    case 'markdown':
      // Markdown flows with its text — no fixed height to size.
      return <MarkdownPanel cfg={panel.config as MarkdownPanelConfig} />;
    case 'row':
      // Row markers are layout-only; the dashboard page intercepts them
      // before they get here. This branch is a defensive no-op so a
      // rogue render path doesn't crash the page.
      return null;
    default:
      return <PanelError msg={`Unknown panel type: ${(panel as Panel).type}`} height={panelChartHeight(h)} />;
  }
}

// Variable substitution per panel type. Each function returns a new
// config with ${name} expanded against `vars` in the relevant fields.

function expand(s: string | undefined, vars?: Record<string, string>): string | undefined {
  if (!s || !vars) return s;
  return substituteVars(s, vars);
}

export function applyVarsToMetric(cfg: MetricPanelConfig, vars?: Record<string, string>): MetricPanelConfig {
  if (!vars) return cfg;
  return {
    ...cfg,
    metricName: expand(cfg.metricName, vars) ?? '',
    service:    expand(cfg.service, vars),
    groupBy:    expand(cfg.groupBy, vars),
    filters:    expand(cfg.filters, vars),
    // PromQL mode uses the PromQL-aware expansion (matcher-strip on empty var).
    promql:     cfg.promql ? expandPromqlVars(cfg.promql, vars) : cfg.promql,
  };
}

export function applyVarsToSpan(cfg: SpanMetricPanelConfig, vars?: Record<string, string>): SpanMetricPanelConfig {
  if (!vars) return cfg;
  return {
    ...cfg,
    dsl:     expand(cfg.dsl, vars),
    groupBy: expand(cfg.groupBy, vars),
    filters: expand(cfg.filters, vars),
  };
}

function applyVarsToStat(cfg: StatPanelConfig, vars?: Record<string, string>): StatPanelConfig {
  if (!vars) return cfg;
  if (cfg.source === 'metric') {
    return { ...cfg, metric: cfg.metric ? applyVarsToMetric(cfg.metric, vars) : cfg.metric };
  }
  return { ...cfg, span: cfg.span ? applyVarsToSpan(cfg.span, vars) : cfg.span };
}

// v0.6.19 — same logic as applyVarsToStat; gauge shares the
// metric/span source pattern. Separate function so future
// gauge-only fields (min/max/thresholds) can pick up variable
// substitution without contaminating the Stat code path.
function applyVarsToGauge(cfg: GaugePanelConfig, vars?: Record<string, string>): GaugePanelConfig {
  if (!vars) return cfg;
  if (cfg.source === 'metric') {
    return { ...cfg, metric: cfg.metric ? applyVarsToMetric(cfg.metric, vars) : cfg.metric };
  }
  return { ...cfg, span: cfg.span ? applyVarsToSpan(cfg.span, vars) : cfg.span };
}

// v0.9.109 (C2) — expand ${vars} in the heatmap's metric/service/filters,
// same contract as the metric panel (empty var → the shared `expand` helper
// drops the predicate rather than producing service.name = "").
function applyVarsToHeatmap(cfg: HeatmapPanelConfig, vars?: Record<string, string>): HeatmapPanelConfig {
  if (!vars) return cfg;
  return {
    ...cfg,
    metricName: expand(cfg.metricName, vars) ?? '',
    service:    expand(cfg.service, vars),
    filters:    expand(cfg.filters, vars),
  };
}

// v0.9.117 (F4) — expand ${vars} inside a PromQL query. NOT the shared
// line-based `expand` (substituteVars): that DROPS any line whose vars all
// resolve empty, which for a single-line PromQL expression deletes the whole
// query (review MAJOR, v0.9.118). Instead:
//   1. A label matcher whose value IS an empty/unset variable is STRIPPED, so
//      an "(all)"/cleared dashboard variable selects everything — a literal
//      label="" would match only empty-label series, not all.
//   2. Remaining ${var} tokens are substituted in place.
//   3. Dangling commas from a stripped first/last matcher are tidied.
// expandPromqlVars — the PromQL-aware ${var} expansion shared by the PromQL
// panel and the metric panel's PromQL mode (see applyVarsToPromql's rationale).
export function expandPromqlVars(query: string, vars?: Record<string, string>): string {
  if (!vars || !query) return query;
  let q = query.replace(
    /,?\s*[\w.]+\s*(?:=~|!~|=|!=)\s*"\$\{([^}]+)\}"/g,
    (m, name: string) => {
      const v = vars[name];
      return v != null && v !== '' ? m.replace('${' + name + '}', v) : '';
    },
  );
  q = q.replace(/\$\{([^}]+)\}/g, (_, name: string) => vars[name] ?? '');
  return q.replace(/\{\s*,/g, '{').replace(/,\s*\}/g, '}').replace(/,\s*,/g, ',');
}

export function applyVarsToPromql(cfg: PromqlPanelConfig, vars?: Record<string, string>): PromqlPanelConfig {
  if (!vars || !cfg.query) return cfg;
  return { ...cfg, query: expandPromqlVars(cfg.query, vars) };
}

// v0.9.781 — the Top-N panel speaks the same span-query vocabulary as the
// spanmetric panel (dsl + groupBy + filters), so it expands the same three
// fields through the same shared `expand`: an empty variable DROPS its DSL
// line rather than producing service.name = "".
export function applyVarsToTopN(cfg: TopNPanelConfig, vars?: Record<string, string>): TopNPanelConfig {
  if (!vars) return cfg;
  return {
    ...cfg,
    dsl:     expand(cfg.dsl, vars),
    groupBy: expand(cfg.groupBy, vars) ?? '',
    filters: expand(cfg.filters, vars),
  };
}

// ── Metric line chart ───────────────────────────────────────────────────────

function MetricPanel({ cfg, range, syncKey, onZoom, onZoomReset, refreshTick, dataOverride, height }: {
  cfg: MetricPanelConfig; range: TimeRange; syncKey?: string;
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  onZoomReset?: () => void;
  refreshTick?: number;
  dataOverride?: PanelDataOverride;
  height?: PanelHeight;
}) {
  const boxPx = panelChartHeight(height);
  const [series, setSeries] = useState<SpanMetricSeries[] | null | undefined>(undefined);
  const [capped, setCapped] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // GRAN-C (v0.8.248) — width-aware auto step. widthPx is the panel's OWN
  // container bucket (panels share a 4-col grid, so #content is the wrong
  // yardstick); null until the layout pass measures it.
  const { ref, widthPx } = usePanelWidth();

  // If the parent has supplied bundled data, route it through
  // local state shape and skip the per-panel fetch entirely.
  // Falls through to own-fetch when dataOverride is undefined
  // OR when the bundle returned neither series nor error for
  // this panel (e.g. the panel was added after the bundle
  // request was built — rare but real during edit flow).
  // PromQL mode (v0.9.121) — the panel is driven by a raw PromQL query instead
  // of the builder (config carries a `promql` string, even if empty while the
  // operator is switching modes). Own-fetch (the dashboard bundle only
  // prefetches builder metrics); takes precedence over any stale override.
  const promqlMode = cfg.promql !== undefined;
  const hasOverride = !promqlMode && dataOverride && (dataOverride.series !== undefined || dataOverride.error);
  useEffect(() => {
    if (promqlMode) {
      if (!cfg.promql!.trim()) { setSeries(undefined); setError('Configure a PromQL query'); return; }
      const { from, to } = timeRangeToNs(range);
      const step = effectivePanelStep(cfg.step, (to - from) / 1e9, widthPx);
      if (step === null) return;
      setSeries(undefined); setError(null);
      api.metricPromql({ query: cfg.promql!, from, to, step })
        .then(s => setSeries(s ?? [])).catch(e => setError(e.message));
      return;
    }
    if (hasOverride) {
      if (dataOverride!.error) {
        setSeries(undefined);
        setError(dataOverride!.error);
      } else {
        setSeries(dataOverride!.series ?? []);
        setCapped(dataOverride!.rowsCapped ?? false);
        setError(null);
      }
      return;
    }
    if (!cfg.metricName) { setError('Configure a metric name'); return; }
    // GRAN-C — cfg.step > 0 (operator-pinned) passes through; auto resolves
    // against the measured panel width. null = not measured yet → defer this
    // fetch one beat rather than firing at a guessed width. widthPx sits in
    // the deps so the request (and its server-side cache key, which hashes
    // the step param) tracks bucket crossings — no stale-step reuse.
    const { from, to } = timeRangeToNs(range);
    const step = effectivePanelStep(cfg.step, (to - from) / 1e9, widthPx);
    if (step === null) return;
    setSeries(undefined); setError(null);
    api.metricQueryFull({
      name: cfg.metricName, service: cfg.service, agg: cfg.agg,
      // v0.9.566 — FİLTRELER. Bu çağrı cfg.filters'ı geçirmiyordu:
      // panel config'inde duruyor, toplu (bundle) yol da geçirmiyordu
      // (api.go metric dalı), yani filtre HİÇBİR yoldan SQL'e inmiyordu.
      // Sonuç boş panel değil, sessizce YANLIŞ SAYI — bir
      // jvm.memory.type="heap" filtresi uygulanmayınca panel heap +
      // non-heap toplamını "heap" diye çiziyordu.
      filters: cfg.filters,
      groupBy: cfg.groupBy, from, to, step,
    }).then(r => { setSeries(r?.series ?? []); setCapped(r?.rowsCapped ?? false); })
      .catch(e => setError(e.message));
    // refreshTick: v0.9.779 — auto-refresh. Override'lı yolda yalnız
    // aynı veriyi yeniden uygular (istek yok); promql modunda ve bundle
    // kapsamayan panelde gerçek refetch.
  }, [JSON.stringify(cfg), range, hasOverride, JSON.stringify(dataOverride), widthPx, refreshTick]);

  if (error) return <PanelError msg={error} height={boxPx} />;
  // v0.9.790 — markı da dispatch et. Bu dal v0.9.786'ya kadar KOŞULSUZ
  // DashLineChart çiziyordu: spanmetric ve promql panelleri viz'e göre
  // dallanırken metric paneli dallanmıyordu, yani Explore'dan bars/area
  // pinleyen operatörün config'indeki mark okunacak bir yer bulamıyordu.
  // Dallanma spanmetric dalının birebir ikizi (aşağıda), unit dahil.
  const viz = cfg.viz ?? 'line';
  return (
    <div ref={ref} style={{ position: 'relative' }}>
      {capped && <CapHint />}
      {series === undefined ? <PanelLoading height={boxPx} />
        : !series || series.length === 0 ? <PanelEmpty height={boxPx} />
        // Madde 4 sweep — cfg.unit eksene/tooltip'e iner (promql paneli
        // pariteli; yokluğu = eski birimsiz davranış).
        // v0.9.808 — mark ayıklaması KALKTI: beş mark da DashChart'a gider.
        // v0.9.844 — DashChart'ın içindeki eski-motor dalı da yok; tek yol.
        : <DashChart series={series} viz={viz} unit={cfg.unit} syncKey={syncKey} onZoom={onZoom} onZoomReset={onZoomReset} storageKey={`dash-m-${cfg.metricName}`} height={boxPx} />}
    </div>
  );
}

// ── Metric histogram heatmap (C2, v0.9.109) ─────────────────────────────────
// The first dashboard surface for the metric-histogram path: fetches
// /api/metrics/histogram (bounds + per-time bucket counts), adapts to the
// LatencyHeatmap viz. Global distribution (no agg/groupBy — a heatmap blends
// the whole distribution). Width-aware auto step like the metric panels.
function HeatmapPanel({ cfg, range, refreshTick, height }: {
  cfg: HeatmapPanelConfig; range: TimeRange; refreshTick?: number; height?: PanelHeight;
}) {
  const boxPx = panelChartHeight(height);
  const [data, setData] = useState<HeatmapData | null | undefined>(undefined);
  const [honesty, setHonesty] = useState<{ skipped: number; rowCapped: boolean }>({ skipped: 0, rowCapped: false });
  const [error, setError] = useState<string | null>(null);
  const { ref, widthPx } = usePanelWidth();
  // v0.9.947 (D4/Ö24) — jest durumu. Kapı için gerekçe render'da.
  const pivotable = heatmapPivotable(cfg.unit);
  const [cellExemplar, setCellExemplar] = useState<{
    timeNs: number; lowDurMs: number; highDurMs: number; count: number;
    exemplarTraceId?: string;
  } | null>(null);
  const [boxSel, setBoxSel] = useState<{
    timeFromNs: number; timeToNs: number; lowDurMs: number; highDurMs: number; count: number;
  } | null>(null);

  useEffect(() => {
    if (!cfg.metricName) { setError('Configure a metric name'); return; }
    const { from, to } = timeRangeToNs(range);
    const step = effectivePanelStep(cfg.step, (to - from) / 1e9, widthPx);
    if (step === null) return; // panel width not measured yet — defer
    setData(undefined); setError(null);
    api.metricHistogram({
      name: cfg.metricName, service: cfg.service,
      filters: cfg.filters, from, to, step,
    })
      .then(r => {
        setData(r ? histogramResultToHeatmap(r, cfg.unit) : null);
        // v0.9.473 (dürüstlük A13) — backend'in dürüst alanları yere
        // düşmesin: skipped (uyumsuz bucket düzenli seriler hariç) +
        // rowCapped (sağ kenar kesik olabilir).
        setHonesty({ skipped: r?.skipped ?? 0, rowCapped: r?.rowCapped ?? false });
      })
      .catch(e => setError(e.message));
    // refreshTick: v0.9.779 — auto-refresh (bundle DIŞI panel).
  }, [JSON.stringify(cfg), range, widthPx, refreshTick]);

  if (error) return <PanelError msg={error} height={boxPx} />;
  return (
    <div ref={ref} style={{ position: 'relative' }}>
      {(honesty.skipped > 0 || honesty.rowCapped) && (
        <span style={{ position: 'absolute', top: 6, right: 8, zIndex: 2, cursor: 'help', fontSize: 11, color: 'var(--warn)' }}
          title={[
            honesty.rowCapped ? 'Satır tavanı (200k) doldu — pencerenin SAĞ kenarı kesik olabilir; "trafik düştü" gibi okuma.' : '',
            honesty.skipped > 0 ? `${honesty.skipped} seri uyumsuz bucket düzeni nedeniyle hariç tutuldu (yanlış kovaya toplamak yerine).` : '',
          ].filter(Boolean).join(' ')}>
          ⚠ {honesty.rowCapped ? 'kesik' : ''}{honesty.rowCapped && honesty.skipped > 0 ? ' · ' : ''}{honesty.skipped > 0 ? `${honesty.skipped} seri hariç` : ''}
        </span>
      )}
      {data === undefined ? <PanelLoading height={boxPx} />
        : !data || data.maxCount === 0 ? <PanelEmpty height={boxPx} />
        : (
          <>
            {/* v0.9.947 (UX denetimi D4 / Ö24) — panel artık SALT-OKUNUR
                DEĞİL. Aynı görselleştirme Service ve Explore'da hem
                örnek-trace tıkı hem kutu seçimi taşıyordu; panoda hiçbir
                jest yoktu.

                Jestler KOŞULSUZ bağlanmadı (heatmapPivot.ts): pano paneli
                bir METRİK HİSTOGRAMI çiziyor, span süresi değil.
                `jvm_memory_bytes` histogramında bir hücreye tıklayıp
                "süresi 2–4 ms olan trace'ler" listelemek boş modal değil
                YANLIŞ modal olurdu. Kapı birimde: yalnız süre birimli
                histogramlar pivot taşır. */}
            <LatencyHeatmap data={data} height={boxPx}
              onCellClick={pivotable ? setCellExemplar : undefined}
              onBoxSelect={pivotable ? setBoxSel : undefined} />
            {pivotable && (
              <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 4 }}>
                tek hücre = örnek trace · sürükle = zaman × gecikme bandı seç
              </div>
            )}
            {boxSel && (
              <div style={{
                display: 'flex', gap: 10, alignItems: 'center', marginTop: 6,
                border: '1px solid var(--accent)', borderRadius: 'var(--radius)',
                padding: '4px 10px', fontSize: 12, width: 'fit-content',
                background: 'var(--bg1)',
              }}>
                <b>{boxSel.count.toLocaleString()} örnek</b>
                <Link to={heatmapTracesHref(boxSel, cfg.service)}
                  style={{ textDecoration: 'none' }}>Traces →</Link>
                <span onClick={() => setBoxSel(null)}
                  style={{ cursor: 'pointer', color: 'var(--text3)' }}>✕</span>
              </div>
            )}
            {cellExemplar && (
              <HeatmapCellExemplars
                cell={cellExemplar}
                exemplarTraceId={cellExemplar.exemplarTraceId}
                // Kova genişliği ızgaranın KENDİSİNDEN; modal aynı
                // pencereyi arar, tahmin etmez.
                bucketWidthNs={data.times.length >= 2 ? data.times[1] - data.times[0] : 60 * 1e9}
                // Panelin kendi kapsamı modale de iner: modal, tıklanan
                // yüzeyle tutarlı olmak zorunda.
                filters={cfg.service ? [{ k: 'service.name', op: '=', v: [cfg.service] }] : []}
                onClose={() => setCellExemplar(null)} />
            )}
          </>
        )}
    </div>
  );
}

// ── PromQL panel (F4, v0.9.117) ─────────────────────────────────────────────
// A dashboard chart driven by a raw PromQL query (/api/metrics/promql, the
// Phase 1-3 engine). Own-fetch, width-aware step, standard loading/empty/error
// states; a parse/eval error surfaces inline (the backend message).
function PromqlPanel({ cfg, range, syncKey, onZoom, onZoomReset, refreshTick, height }: {
  cfg: PromqlPanelConfig; range: TimeRange; syncKey?: string;
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  onZoomReset?: () => void;
  refreshTick?: number;
  height?: PanelHeight;
}) {
  const boxPx = panelChartHeight(height);
  const [series, setSeries] = useState<SpanMetricSeries[] | null | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);
  const { ref, widthPx } = usePanelWidth();

  // Debounce the (free-text) query so editing it in the panel editor doesn't
  // fire a CH-backed fetch per keystroke (review MINOR, v0.9.118). Starts equal
  // to cfg.query so the first paint fetches immediately; only edits wait 400ms.
  const [debouncedQuery, setDebouncedQuery] = useState(cfg.query);
  useEffect(() => {
    const t = window.setTimeout(() => setDebouncedQuery(cfg.query), 400);
    return () => window.clearTimeout(t);
  }, [cfg.query]);

  useEffect(() => {
    if (!debouncedQuery || !debouncedQuery.trim()) {
      setSeries(undefined);
      setError('Configure a PromQL query');
      return;
    }
    const { from, to } = timeRangeToNs(range);
    const step = effectivePanelStep(cfg.step, (to - from) / 1e9, widthPx);
    if (step === null) return; // panel width not measured yet — defer
    setSeries(undefined);
    setError(null);
    api.metricPromql({ query: debouncedQuery, from, to, step })
      .then(s => setSeries(s ?? []))
      .catch(e => setError(e.message));
    // refreshTick: v0.9.779 — auto-refresh (bundle DIŞI panel).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debouncedQuery, cfg.step, range, widthPx, refreshTick]);

  if (error) return <PanelError msg={error} height={boxPx} />;
  const viz = cfg.viz ?? 'line';
  return (
    <div ref={ref}>
      {series === undefined ? <PanelLoading height={boxPx} />
        : !series || series.length === 0 ? <PanelEmpty height={boxPx} />
        : <DashChart series={series} viz={viz} unit={cfg.unit} syncKey={syncKey} onZoom={onZoom} onZoomReset={onZoomReset} storageKey={`dash-q-${cfg.query.slice(0, 60)}`} height={boxPx} />}
    </div>
  );
}

// ── Top-N bars (v0.9.781) ───────────────────────────────────────────────────
// Ranked horizontal bars over the WHOLE window — Datadog's Top List shape.
// Non-temporal: no cursor sync, no drag-zoom, no bundle (the dashboard bundle
// only speaks 'metric' | 'spanMetric', and api.go's dashboardData would reject
// an unknown 'topn' request), so this panel owns its fetch like heatmap and
// promql do.
//
// The single-bucket step is the whole trick — see topN.ts for why anything
// else silently lies about p99 / error_rate.
function TopNPanel({ cfg, range, refreshTick, height, panelId }: {
  cfg: TopNPanelConfig; range: TimeRange; refreshTick?: number;
  height?: PanelHeight; panelId?: string;
}) {
  const boxPx = panelBoxHeight(height);
  const [rows, setRows] = useState<{ key: string[]; value: number | null }[] | undefined>(undefined);
  const [more, setMore] = useState<string | null>(null);
  const [capped, setCapped] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const limit = clampTopNLimit(cfg.limit);

  useEffect(() => {
    if (!cfg.agg) { setError('Configure an aggregation'); return; }
    if (!cfg.groupBy || !cfg.groupBy.trim()) { setError('Configure a group-by key'); return; }
    // timeRangeToNs ONLY inside the effect (v0.5.184) — bare in JSX it ticks
    // now() on every render and refetches forever.
    const { from, to } = timeRangeToNs(range);
    const step = topNStep(from, to);
    const windowSec = topNWindowSec(from, to);
    setRows(undefined); setError(null);
    api.spanMetricTopN({
      agg: cfg.agg, field: cfg.field, groupBy: cfg.groupBy,
      filters: cfg.filters, dsl: cfg.dsl,
      from, to, step,
    }).then(r => {
      const series = r?.series ?? [];
      // The server ranks by area only WHEN it trimmed; an untrimmed response
      // arrives in group-key alphabetical order, so the ranking is ours to do.
      const ranked = series
        .map(s => ({ key: s.groupKey, value: topNRowValue(s.points, cfg.agg, step, windowSec) }))
        .sort((a, b) => Math.abs(b.value ?? -Infinity) - Math.abs(a.value ?? -Infinity));
      const shown = ranked.slice(0, limit);
      setRows(shown);
      setMore(topNMoreLabel(shown.length, series.length, r?.totalSeries));
      setCapped(r?.rowsCapped ?? false);
    }).catch(e => setError(e.message));
    // refreshTick: v0.9.779 — auto-refresh (bundle DIŞI panel).
  }, [JSON.stringify(cfg), range, limit, refreshTick]);

  if (error) return <PanelError msg={error} height={boxPx} />;
  if (rows === undefined) return <PanelLoading height={boxPx} />;
  if (rows.length === 0) return <PanelEmpty height={boxPx} />;

  // Scale to the LARGEST VISIBLE bar, not to the untrimmed maximum: the panel
  // shows `limit` rows, so scaling to a row nobody can see would squash every
  // bar for no reason.
  const max = Math.max(...rows.map(r => Math.abs(r.value ?? 0)), 0);

  return (
    <div data-panel-id={panelId}
      style={{ position: 'relative', height: boxPx, overflowY: 'auto', padding: '4px 10px' }}>
      {capped && <CapHint />}
      {rows.map((r, i) => (
        <TopNRow key={i} row={r} max={max} cfg={cfg} range={range} />
      ))}
      {more && (
        <div style={{ fontSize: 11, color: 'var(--text3)', padding: '4px 2px 2px' }}
          title="Sunucu yüksek kardinaliteli group-by'ı en büyük 50 seriye kırpar; bu satır kalanı sayar.">
          {more}
        </div>
      )}
    </div>
  );
}

// TopNRow — one bar. Label (mono, ellipsised, full value in title) over a
// track+fill pair, value right-aligned. Colours are tokens / color-mix so the
// bar re-resolves per theme (dark / light / redhat) like everything else.
function TopNRow({ row, max, cfg, range }: {
  row: { key: string[]; value: number | null };
  max: number;
  cfg: TopNPanelConfig;
  range: TimeRange;
}) {
  const label = row.key.length ? row.key.join(' · ') : '(empty)';
  const share = max > 0 && row.value != null ? Math.abs(row.value) / max : 0;
  const href = topNRowHref(cfg, row.key, range);
  const body = (
    <>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 3 }}>
        <span className="mono" title={label}
          style={{
            fontSize: 11, color: 'var(--text)', flex: 1, minWidth: 0,
            overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
          }}>{label}</span>
        <span className="mono" title={row.value != null ? String(row.value) : 'no single-bucket value'}
          style={{ fontSize: 11, color: 'var(--text2)', flexShrink: 0 }}>
          {row.value != null ? fmtSmart(row.value, cfg.unit) : '—'}
        </span>
      </div>
      <div style={{ height: 7, borderRadius: 2, background: 'var(--bg2)' }}>
        <div style={{
          height: '100%', borderRadius: 2,
          width: `${Math.max(4, share * 100)}%`,
          background: 'linear-gradient(90deg, var(--teal, #2dd4bf), color-mix(in srgb, var(--teal, #2dd4bf) 35%, transparent))',
        }} />
      </div>
    </>
  );
  if (!href) return <div style={{ padding: '5px 0' }}>{body}</div>;
  return (
    <Link to={href} style={{ display: 'block', padding: '5px 0', textDecoration: 'none' }}>
      {body}
    </Link>
  );
}

// topNRowHref — the row's pivot, or null when the operator left linkTo at
// 'none'. Never guessed: 'service' reads the FIRST group-by value as a
// service name, 'traces' rebuilds the row's exact population as span filters
// (topNRowFilters) and carries the window, which tracesPivotHref requires
// precisely so a pivot can't silently re-ask the question over another hour.
function topNRowHref(cfg: TopNPanelConfig, groupKey: string[], range: TimeRange): string | null {
  const mode = cfg.linkTo ?? 'none';
  if (mode === 'none' || groupKey.length === 0) return null;
  if (mode === 'service') {
    if (!groupKey[0]) return null;
    // v0.9.967 — the window was already in this function's signature, used
    // by the sibling 'traces' branch two lines down (where pivotHref makes
    // it mandatory) and simply dropped here. A dashboard is the surface
    // most likely to be viewed on a brushed window.
    return serviceHref(groupKey[0], { range });
  }
  const filters = topNRowFilters(cfg.groupBy, groupKey);
  return tracesPivotHref({
    window: range,
    filters: filters.length ? encodeFilters(filters) : undefined,
  });
}

// ── Span metric line chart ──────────────────────────────────────────────────

function SpanMetricPanel({ cfg, range, syncKey, onZoom, onZoomReset, refreshTick, dataOverride, height }: {
  cfg: SpanMetricPanelConfig; range: TimeRange; syncKey?: string;
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  onZoomReset?: () => void;
  refreshTick?: number;
  dataOverride?: PanelDataOverride;
  height?: PanelHeight;
}) {
  const boxPx = panelChartHeight(height);
  const [series, setSeries] = useState<SpanMetricSeries[] | null | undefined>(undefined);
  const [capped, setCapped] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // GRAN-C (v0.8.248) — same width-aware auto step as MetricPanel above.
  const { ref, widthPx } = usePanelWidth();

  const hasOverride = dataOverride && (dataOverride.series !== undefined || dataOverride.error);
  useEffect(() => {
    if (hasOverride) {
      if (dataOverride!.error) {
        setSeries(undefined);
        setError(dataOverride!.error);
      } else {
        setSeries(dataOverride!.series ?? []);
        setCapped(dataOverride!.rowsCapped ?? false);
        setError(null);
      }
      return;
    }
    if (!cfg.agg) { setError('Configure an aggregation'); return; }
    const { from, to } = timeRangeToNs(range);
    const step = effectivePanelStep(cfg.step, (to - from) / 1e9, widthPx);
    if (step === null) return; // panel width not measured yet — defer
    setSeries(undefined); setError(null);
    api.spanMetricTopN({
      agg: cfg.agg, field: cfg.field, groupBy: cfg.groupBy,
      filters: cfg.filters, dsl: cfg.dsl,
      from, to, step,
    }).then(r => { setSeries(r?.series ?? []); setCapped(r?.rowsCapped ?? false); })
      .catch(e => setError(e.message));
    // refreshTick: v0.9.779 — auto-refresh. Override'lı yolda istek yok
    // (aynı veri yeniden uygulanır); bundle kapsamayan panelde refetch.
  }, [JSON.stringify(cfg), range, hasOverride, JSON.stringify(dataOverride), widthPx, refreshTick]);

  if (error) return <PanelError msg={error} height={boxPx} />;
  // Dispatch on the configured viz. v0.9.808'den beri BEŞ mark da v2
  // motoruna (CorePanel — uPlot + tooltip + istatistik lejantı +
  // zoom/senkron) gider; ad eşlemesi tek saf yerde (panelViz.toCoreViz).
  const viz = cfg.viz ?? 'line';
  return (
    <div ref={ref} style={{ position: 'relative' }}>
      {capped && <CapHint />}
      {series === undefined ? <PanelLoading height={boxPx} />
        : !series || series.length === 0 ? <PanelEmpty height={boxPx} />
        // Madde 4 sweep — cfg.unit grafiğe iner. Eski SVG motoru o taramada
        // "ayrı motor" diye kapsam dışı bırakılmıştı (madde 13
        // notu) ama motor farkı bir GEREKÇE değildi: bileşen unit'i
        // zaten alıyor ve hem y-ekseni etiketlerinde hem hover
        // okumasında fmtSmart'a veriyor. Geçilmeyince aynı panelin
        // çizgi hali "142 ms", bar hali çıplak "142" yazıyordu.
        : <DashChart series={series} viz={viz} unit={cfg.unit} syncKey={syncKey} onZoom={onZoom} onZoomReset={onZoomReset} storageKey={`dash-s-${cfg.agg}-${cfg.groupBy ?? ''}`} height={boxPx} />}
    </div>
  );
}

// ── Single value with prior-period delta + sparkline ──────────────────────
//
// Datadog / New Relic stat-tile pattern: big number, small
// trendline underneath, "+12.3% vs prior 15m" delta chip
// coloured by direction-vs-better. The previous tile showed a
// raw decimal with no context — an operator looking at "234.56"
// can't tell if that's normal or a regression.
//
// Implementation: fetch the doubled time window in one query,
// split the points into two halves on the time midpoint. The
// recent half feeds the displayed value + sparkline; the older
// half computes the prior baseline. One round trip, no extra
// API surface.

function StatPanel({ cfg, range, refreshTick, height }: {
  cfg: StatPanelConfig; range: TimeRange; refreshTick?: number; height?: PanelHeight;
}) {
  const boxPx = panelBoxHeight(height);
  const [value, setValue] = useState<number | null | undefined>(undefined);
  const [prior, setPrior] = useState<number | null>(null);
  const [points, setPoints] = useState<{ time: number; value: number }[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setValue(undefined); setPrior(null); setError(null);
    // Fetch DOUBLE the visible range so we have an equal-sized
    // prior period to compare against. The midpoint splits
    // recent (the operator's actual window) from prior.
    const { from, to } = timeRangeToNs(range);
    const span = to - from;
    const extendedFrom = from - span;
    const promise = cfg.source === 'spanmetric'
      ? api.spanMetric({
          agg: cfg.span?.agg ?? 'count', field: cfg.span?.field,
          groupBy: cfg.span?.groupBy, filters: cfg.span?.filters, dsl: cfg.span?.dsl,
          from: extendedFrom, to, step: cfg.span?.step,
        })
      : api.metricQuery({
          name: cfg.metric?.metricName ?? '', service: cfg.metric?.service,
          agg: cfg.metric?.agg, groupBy: cfg.metric?.groupBy,
          from: extendedFrom, to, step: cfg.metric?.step,
        });
    promise
      .then(s => {
        const flat = (s ?? []).flatMap(x => x.points);
        flat.sort((a, b) => a.time - b.time);
        // Split on the time midpoint between extended start
        // and end. Some buckets may straddle the midpoint;
        // we err on the side of "later" so the recent half
        // owns any boundary point.
        const recent = flat.filter(p => p.time >= from);
        const priorPts = flat.filter(p => p.time < from);
        setPoints(recent);
        setValue(recent.length > 0 ? recent[recent.length - 1].value : null);
        setPrior(priorPts.length > 0 ? mean(priorPts.map(p => p.value)) : null);
      })
      .catch(e => setError(e.message));
    // refreshTick: v0.9.779 — auto-refresh (bundle DIŞI panel). Bunu
    // atlamak, çevresindeki grafikler ilerlerken donuk kalan bir stat
    // kutusu bırakırdı — en aldatıcı hâli.
  }, [JSON.stringify(cfg), range, refreshTick]);

  if (error) return <PanelError msg={error} height={boxPx} />;
  if (value === undefined) return <PanelLoading height={boxPx} />;

  const agg = cfg.source === 'spanmetric' ? (cfg.span?.agg ?? '') : (cfg.metric?.agg ?? '');
  const display = formatStatValue(value, cfg.unit, cfg.decimals);
  // Delta vs prior — only when we have both numbers AND the
  // prior wasn't zero (avoid Infinity/-100% noise on rare
  // empty earlier windows).
  const delta = (value !== null && prior !== null && prior !== 0)
    ? ((value - prior) / Math.abs(prior)) * 100
    : null;
  const tone = deltaTone(agg, delta);

  // v0.5.486 — threshold band lookup. Picks the highest
  // threshold whose `value` is ≤ the current value. Bands are
  // operator-defined per panel via PanelEditor.
  const band = pickThresholdBand(value, cfg.thresholds);
  const colorMode = cfg.colorMode ?? 'none';
  const bandColour = band ? THRESHOLD_COLOURS[band.color] : null;
  const valueColour = colorMode === 'value' && bandColour
    ? bandColour
    : 'var(--accent2)';
  const bgTint = colorMode === 'background' && bandColour
    ? thresholdTint(bandColour)
    : 'transparent';

  return (
    <div style={{
      display: 'flex', flexDirection: 'column',
      alignItems: 'center', justifyContent: 'center',
      height: boxPx, gap: 6,
      background: bgTint,
      // v0.9.778 — band stripe down the left edge. Independent of colorMode:
      // thresholds configured at all = the tile carries its status even when
      // the operator kept the number and the background neutral, so a wall of
      // stat tiles is scannable without reading a single number. No band
      // (no thresholds, or the value sits under the lowest floor) = no
      // border at all, i.e. the pre-v0.9.778 look — NOT a grey stripe, which
      // would read as "a status exists and it is muted".
      ...(bandColour ? { borderLeft: `3px solid ${bandColour}` } : null),
      borderRadius: 6,
      transition: 'background 120ms ease, border-color 120ms ease',
    }}>
      <div style={{ fontSize: 42, fontWeight: 600, color: valueColour, lineHeight: 1.05,
        transition: 'color 120ms ease' }}>
        {display}
      </div>
      {/* Delta chip — colour-coded by direction-vs-better.
          Aggs where lower-is-better (latency / errors) flip
          red on increase; rate / count etc. stay neutral. */}
      {delta !== null && (
        <div style={{
          fontSize: 12,
          color: tone === 'good' ? 'var(--ok)'
               : tone === 'bad'  ? 'var(--err)'
               : 'var(--text2)',
          fontFamily: 'ui-monospace, monospace',
          display: 'inline-flex', alignItems: 'center', gap: 4,
        }}
             title="Δ vs same-length prior window">
          {delta > 0.05 ? '▲' : delta < -0.05 ? '▼' : '·'}
          {' '}
          {delta > 0 ? '+' : ''}{Math.abs(delta) >= 100 ? delta.toFixed(0) : delta.toFixed(1)}%
          <span style={{ color: 'var(--text3)' }}>vs prior</span>
        </div>
      )}
      {points.length > 1 && (
        <Sparkline points={points} tone={tone} />
      )}
    </div>
  );
}

// ── Gauge panel (v0.6.19) ───────────────────────────────────────
// Semicircle dial — Grafana-style. Coloured threshold zones run
// along the arc; a single tick + a centred big number show the
// current value. Best for bounded metrics where the operator
// wants "where am I in the safe / warning / breached bands" at
// a glance (CPU %, SLO budget %, queue cap %).
//
// Same data fetch as StatPanel — picks the last point of the
// recent half-window. No prior-period overlay (the gauge's
// visual job is "current state", not "trend"; the Stat panel
// covers the trend story).
function GaugePanel({ cfg, range, refreshTick, height }: {
  cfg: GaugePanelConfig; range: TimeRange; refreshTick?: number; height?: PanelHeight;
}) {
  const boxPx = panelBoxHeight(height);
  const [value, setValue] = useState<number | null | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setValue(undefined); setError(null);
    const { from, to } = timeRangeToNs(range);
    const promise = cfg.source === 'spanmetric'
      ? api.spanMetric({
          agg: cfg.span?.agg ?? 'count', field: cfg.span?.field,
          groupBy: cfg.span?.groupBy, filters: cfg.span?.filters, dsl: cfg.span?.dsl,
          from, to, step: cfg.span?.step,
        })
      : api.metricQuery({
          name: cfg.metric?.metricName ?? '', service: cfg.metric?.service,
          agg: cfg.metric?.agg, groupBy: cfg.metric?.groupBy,
          from, to, step: cfg.metric?.step,
        });
    promise
      .then(s => {
        const flat = (s ?? []).flatMap(x => x.points);
        flat.sort((a, b) => a.time - b.time);
        setValue(flat.length > 0 ? flat[flat.length - 1].value : null);
      })
      .catch(e => setError(e.message));
    // refreshTick: v0.9.779 — auto-refresh (bundle DIŞI panel).
  }, [JSON.stringify(cfg), range, refreshTick]);

  if (error) return <PanelError msg={error} height={boxPx} />;
  if (value === undefined) return <PanelLoading height={boxPx} />;

  const min = cfg.min ?? 0;
  const max = cfg.max ?? 100;
  const safeVal = value ?? min;
  // Clamp to [min, max] for the arc geometry — out-of-range
  // values still display the raw number, but the needle sits
  // at the bounds.
  const clamped = Math.max(min, Math.min(max, safeVal));
  // SVG geometry: 200×120 viewBox, centre at (100, 100), radius
  // 80, semicircle from 180° (left) sweeping to 360° (right) —
  // i.e. the top half.
  const cx = 100, cy = 100, radius = 80;
  const trackW = 18;
  const valueAngle = valueToAngle(clamped, min, max);
  // Threshold band painter: each contiguous (start, end) range
  // gets an arc segment painted in its colour. Falls back to a
  // neutral track when no thresholds are set.
  const segs = computeGaugeSegments(cfg.thresholds, min, max);
  const band = pickThresholdBand(safeVal, cfg.thresholds);
  const valueColour = band ? THRESHOLD_COLOURS[band.color] : 'var(--accent2)';
  const display = formatStatValue(value, cfg.unit, cfg.decimals);

  return (
    <div style={{
      display: 'flex', flexDirection: 'column',
      alignItems: 'center', justifyContent: 'center',
      height: boxPx, gap: 4,
    }}>
      <svg width={220} height={130} viewBox="0 0 200 120">
        {/* Background track — paints the full arc in a soft
            neutral so empty space past the threshold zones
            still reads as a gauge, not a partial band. */}
        <path d={arcPath(cx, cy, radius, 180, 360)}
              fill="none"
              stroke="var(--bg2)"
              strokeWidth={trackW}
              strokeLinecap="butt" />
        {/* Threshold band segments — coloured zones. */}
        {segs.map((s, i) => (
          <path key={i}
                d={arcPath(cx, cy, radius,
                  valueToAngle(s.from, min, max),
                  valueToAngle(s.to, min, max))}
                fill="none"
                stroke={THRESHOLD_COLOURS[s.color]}
                strokeOpacity={0.6}
                strokeWidth={trackW}
                strokeLinecap="butt" />
        ))}
        {/* Current-value tick — narrow rectangle at the needle
            angle, anchored at the inner edge of the track. */}
        <line {...tickAt(cx, cy, radius, trackW, valueAngle)}
              stroke={valueColour}
              strokeWidth={3}
              strokeLinecap="round" />
        {/* Min / max axis labels under the arc ends. */}
        <text x={cx - radius} y={cy + 14} textAnchor="middle"
              fontSize={10} fill="var(--text3)">{fmtBound(min)}</text>
        <text x={cx + radius} y={cy + 14} textAnchor="middle"
              fontSize={10} fill="var(--text3)">{fmtBound(max)}</text>
      </svg>
      <div style={{
        fontSize: 28, fontWeight: 600, color: valueColour,
        marginTop: -22, lineHeight: 1,
      }}>
        {display}
      </div>
    </div>
  );
}

// valueToAngle — map [min, max] to the gauge's 180°→360° sweep.
// SVG angle convention: 0° = right, 90° = down. The semicircle
// occupies the top half so we use 180° (left) → 270° (top) →
// 360° (right).
function valueToAngle(v: number, min: number, max: number): number {
  if (max <= min) return 180;
  const t = (v - min) / (max - min);
  return 180 + t * 180;
}

// arcPath — SVG path string for an arc from startAngle to endAngle
// at the given radius, centred at (cx, cy). Angles in degrees,
// SVG convention.
function arcPath(cx: number, cy: number, r: number, startAngle: number, endAngle: number): string {
  if (Math.abs(endAngle - startAngle) < 0.01) return '';
  const start = polarToCart(cx, cy, r, startAngle);
  const end   = polarToCart(cx, cy, r, endAngle);
  const large = Math.abs(endAngle - startAngle) > 180 ? 1 : 0;
  return `M ${start.x} ${start.y} A ${r} ${r} 0 ${large} 1 ${end.x} ${end.y}`;
}

function polarToCart(cx: number, cy: number, r: number, angleDeg: number): { x: number; y: number } {
  const rad = (angleDeg * Math.PI) / 180;
  return { x: cx + r * Math.cos(rad), y: cy + r * Math.sin(rad) };
}

// tickAt — line2 endpoints for a needle at the given angle,
// drawn from the inner edge of the track to the outer edge so
// the tick visually intersects the band it represents.
function tickAt(cx: number, cy: number, r: number, trackW: number, angle: number) {
  const inner = polarToCart(cx, cy, r - trackW / 2, angle);
  const outer = polarToCart(cx, cy, r + trackW / 2, angle);
  return { x1: inner.x, y1: inner.y, x2: outer.x, y2: outer.y };
}

// computeGaugeSegments — for the threshold list, return a series
// of arc segments {from, to, color}. Each band starts at its
// `value` and runs to the next band's value (or max). The
// implicit "below the lowest threshold" zone keeps the neutral
// track colour (no segment).
type GaugeSeg = { from: number; to: number; color: 'green' | 'amber' | 'red' };
function computeGaugeSegments(
  thresholds: { value: number; color: 'green' | 'amber' | 'red' }[] | undefined,
  min: number,
  max: number,
): GaugeSeg[] {
  if (!thresholds || thresholds.length === 0) return [];
  // Sort + clamp into the [min, max] window.
  const sorted = [...thresholds]
    .map(t => ({ ...t, value: Math.max(min, Math.min(max, t.value)) }))
    .sort((a, b) => a.value - b.value);
  const segs: GaugeSeg[] = [];
  for (let i = 0; i < sorted.length; i++) {
    const start = sorted[i].value;
    const end = i + 1 < sorted.length ? sorted[i + 1].value : max;
    if (end > start) segs.push({ from: start, to: end, color: sorted[i].color });
  }
  return segs;
}

function fmtBound(v: number): string {
  if (Math.abs(v) >= 1000) return (v / 1000).toFixed(1) + 'k';
  return String(v);
}

// formatStatValue — uses fmtSmart when we have a unit and the
// caller didn't pin decimals; otherwise honour the explicit
// decimals (preserving the old contract for stat tiles that
// were tuned to a specific precision).
function formatStatValue(value: number | null, unit: string | undefined, decimals: number | undefined): React.ReactNode {
  if (value === null || !isFinite(value as number)) return '—';
  // If unit is a known-smart kind (ms, %, rps, etc.), defer to
  // fmtSmart for the auto-promotion (ms→s past 1k, etc.).
  if (unit) {
    return fmtSmart(value, unit);
  }
  const d = decimals ?? 2;
  return value.toFixed(d);
}

// deltaTone — direction-vs-better classifier. For aggs where
// lower is the goal (p50/p99/avg/max/error_rate/errors), an
// increase is "bad" → red. For traffic-shape aggs (rate /
// count / sum), there's no clear direction → neutral.
type Tone = 'good' | 'bad' | 'neutral';
function deltaTone(agg: string, delta: number | null): Tone {
  if (delta === null || Math.abs(delta) < 0.5) return 'neutral';
  const lowerIsBetter = /^(p\d+|avg|max|min|error_rate|errors)$/.test(agg);
  if (!lowerIsBetter) return 'neutral';
  return delta > 0 ? 'bad' : 'good';
}

// v0.5.486 — threshold band lookup for the Stat panel. Picks
// the highest threshold whose `value` is ≤ the current value.
// Configuration shape: [{value: 0, color: 'green'}, {value: 80,
// color: 'amber'}, {value: 95, color: 'red'}].
//   value=72 → green
//   value=92 → amber
//   value=99 → red
// Returns null when no thresholds are configured OR the value
// is below the lowest band's floor.
function pickThresholdBand(
  value: number | null,
  thresholds?: { value: number; color: 'green' | 'amber' | 'red' }[],
): { value: number; color: 'green' | 'amber' | 'red' } | null {
  if (value === null || !thresholds || thresholds.length === 0) return null;
  const sorted = [...thresholds].sort((a, b) => a.value - b.value);
  let pick = null as null | { value: number; color: 'green' | 'amber' | 'red' };
  for (const t of sorted) {
    if (value >= t.value) pick = t;
  }
  return pick;
}

function mean(arr: number[]): number {
  if (arr.length === 0) return 0;
  let s = 0;
  for (const v of arr) s += v;
  return s / arr.length;
}

// Sparkline tints to match the delta tone — a bad-trending
// stat gets a red sparkline, a good-trending one gets green.
// Neutral keeps the standard accent so traffic charts read
// like the rest of the page.
function Sparkline({ points, tone = 'neutral' }: {
  points: { time: number; value: number }[];
  tone?: Tone;
}) {
  const w = 200, h = 40;
  const xs = points.map(p => p.time);
  const ys = points.map(p => p.value);
  const xmin = Math.min(...xs), xmax = Math.max(...xs);
  const ymin = Math.min(...ys), ymax = Math.max(...ys);
  const xr = xmax - xmin || 1, yr = ymax - ymin || 1;
  const path = points.map((p, i) => {
    const x = ((p.time - xmin) / xr) * w;
    const y = h - ((p.value - ymin) / yr) * h;
    return `${i === 0 ? 'M' : 'L'} ${x.toFixed(1)} ${y.toFixed(1)}`;
  }).join(' ');
  // Build a fill area that extends to the bottom of the spark
  // so the sparkline reads as an area chart, not a thin line —
  // visually closer to Datadog's stat tiles.
  const areaPath = path + ` L ${w} ${h} L 0 ${h} Z`;
  const stroke = tone === 'good' ? 'var(--ok)' : tone === 'bad' ? 'var(--err)' : 'var(--accent)';
  const fill   = tone === 'good' ? 'rgba(63,185,80,0.15)'
              : tone === 'bad'  ? 'rgba(248,81,73,0.15)'
              : 'color-mix(in srgb, var(--accent) 12%, transparent)';
  return (
    <svg width={w} height={h} style={{ display: 'block' }}>
      <path d={areaPath} fill={fill} stroke="none" />
      <path d={path} fill="none" stroke={stroke} strokeWidth={1.5} />
    </svg>
  );
}

// ── Markdown (subset — bold/italic/code/links via simple regex) ─────────────

function MarkdownPanel({ cfg }: { cfg: MarkdownPanelConfig }) {
  // Tiny renderer: bold **, italic *, inline `code`, [links](url), and \n→<br>.
  // Full markdown would need a library — overkill for one-off panel notes.
  const html = (cfg.text ?? '')
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<b>$1</b>')
    .replace(/\*([^*]+)\*/g, '<i>$1</i>')
    .replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>')
    .replace(/\n/g, '<br>');
  return (
    <div style={{ padding: 12, color: 'var(--text)', fontSize: 13, lineHeight: 1.5 }}
         dangerouslySetInnerHTML={{ __html: html }} />
  );
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// v0.9.778 — the three placeholder states take the SAME pixel height as the
// panel they stand in for. They were hard-coded 220 while a chart body was
// 280, so every dashboard load already shifted 60px when the data arrived;
// with S/M/L in play the mismatch would have grown to 180px on a tall chart.
// Absent height → 220, i.e. the old constant.
function PanelLoading({ height }: { height?: number }) {
  return <div style={{ height: height ?? panelBoxHeight(), display: 'grid', placeItems: 'center' }}><Spinner /></div>;
}
function PanelEmpty({ height }: { height?: number }) {
  return <div style={{ height: height ?? panelBoxHeight(), display: 'grid', placeItems: 'center', color: 'var(--text3)', fontSize: 13 }}>No data</div>;
}
function PanelError({ msg, height }: { msg: string; height?: number }) {
  return (
    <div style={{ height: height ?? panelBoxHeight(), display: 'grid', placeItems: 'center', padding: 12 }}>
      <div style={{ color: 'var(--err)', fontSize: 12, textAlign: 'center' }}>⚠ {msg}</div>
    </div>
  );
}
