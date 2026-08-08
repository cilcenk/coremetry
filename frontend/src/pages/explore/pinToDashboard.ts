// pinToDashboard (v0.8.419, Data-Explorer parity DE4) — pure converter
// from an Explore builder query to a dashboard Panel. Dynatrace's
// "pin to dashboard" flow: the operator builds a chart in the
// explorer, pins it, and the dashboard re-renders it live from the
// SAME query semantics — no screenshot, no re-typing.
//
// The mapping is exact because dashboards already speak the two
// Explore source dialects: a catalogue-metric query becomes a
// `metric` panel (MetricPanelConfig) and a span query becomes a
// `spanmetric` panel (SpanMetricPanelConfig); both configs carry the
// same FilterExpr[] JSON the builder holds. Two honest refusals:
//   • formula panels (client-side expression — dashboards have no
//     formula engine) → not convertible (callers hide the pin).
//   • genuine OR/nested filter groups — panel configs carry flat
//     AND filters only; silently flattening would change the data.
//
// v0.9.786 — the mapping also carries the VISUALISATION now. It didn't
// before: an operator who switched Explore to bars/area/stacked and pinned
// got a line panel, with nothing to tell them the mark had been dropped.
// See vizToPanel/vizFromPanel below for the two unions' asymmetries.
import type { BuilderQuery, ExploreViz } from './model';
import { queryDesc } from './model';
import type {
  FilterExpr, MetricPanelConfig, Panel, PanelVizType, SpanMetricPanelConfig,
} from '@/lib/types';

function rid(): string {
  return Math.random().toString(36).slice(2, 10);
}

// ── viz eşlemesi (v0.9.786) ────────────────────────────────────────────────
//
// İki union AYNI alfabeyi konuşmuyor ve bu sessiz bir veri kaybıydı:
//   ExploreViz   line | area | bars | stacked | stat|toplist|pie|table|heatmap
//   PanelVizType line | bar  | stacked-bar | area | stacked-area
//
// Üç dürüst asimetri:
//  1. 'bars' ↔ 'bar' — iki yüzey arasında saf yazım kayması, anlam aynı.
//  2. Explore'un 'stacked'i bir stacked ALAN'dır (TimeSeriesPanel kümülatif
//     çizgiler ARASINA uPlot bandı doldurur; bar path'i yok) — karşılığı
//     'stacked-area', 'stacked-bar' DEĞİL.
//  3. stat/toplist/pie/table/heatmap zaman-serisi markı değil. queryToPanel
//     her zaman bir zaman-serisi paneli üretir; bunlara uydurma bir mark
//     yazmak operatörün pinlediği şey hakkında yalan söylemek olurdu →
//     viz taşınmaz, panel kendi 'line' varsayılanında kalır.
//
// 'line' de undefined döner: alan YOKLUĞU zaten "line" demek (panel
// config'lerinin her yerdeki "absent = default" doktrini, step 0 gibi), ve
// böylece bugüne dek pinlenmiş panellerin config şekli bayt-bayt aynı kalır.
export function vizToPanel(v: ExploreViz | undefined): PanelVizType | undefined {
  switch (v) {
    case 'bars': return 'bar';
    case 'area': return 'area';
    case 'stacked': return 'stacked-area';
    default: return undefined;
  }
}

// vizFromPanel — dönüş bileti (panelToExplore kullanır). TEK tanım burada
// dursun ki iki yön birbirinden ayrı sürüklenmesin — panelToExplore'un
// "bir servis pini tek yerde tanımlanır" kuralının viz hali.
//
// 'stacked-bar'ın Explore ikizi YOK. İki kayıp adaydan birini seçmek
// zorundayız: yığmayı korumak ('stacked' = stacked alan) ya da çubuk markını
// korumak ('bars'). Yığma SEMANTİKTİR — okuyucu toplamı okur; çubuk-mu-alan-mı
// yalnız çizim. O yüzden yığma korunur, mark feda edilir.
export function vizFromPanel(v: PanelVizType | undefined): ExploreViz {
  switch (v) {
    case 'bar': return 'bars';
    case 'area': return 'area';
    case 'stacked-area': return 'stacked';
    case 'stacked-bar': return 'stacked';
    default: return 'line';
  }
}

// isPinnable — cheap gate for the UI affordance (📌 visibility).
export function isPinnable(q: BuilderQuery): boolean {
  if (q.filterGroup) return false;                 // OR/nested — see header
  if (q.source === 'metric' && !q.metric) return false; // nothing picked yet
  return true;
}

// queryToPanel — builds a half-width dashboard Panel from a builder
// query, or null when not pinnable. step 0/undefined stays absent so
// the panel keeps the width-aware auto step (GRAN-C v0.8.248).
export function queryToPanel(
  q: BuilderQuery, opts?: { title?: string; step?: number; viz?: ExploreViz },
): Panel | null {
  if (!isPinnable(q)) return null;
  const title = opts?.title?.trim() || queryDesc(q);
  const step = opts?.step && opts.step > 0 ? opts.step : undefined;
  const groupBy = q.splitBy.length ? q.splitBy.join(',') : undefined;

  if (q.source === 'metric') {
    // viz BURADA taşınmaz — MetricPanelConfig'in viz alanı YOK ve
    // PanelRenderer'ın MetricPanel'i koşulsuz DashLineChart çiziyor
    // (spanmetric ve promql panelleri dispatch ediyor, metric etmiyor).
    // Okunmayacak bir alan yazmak "taşındı" yanılsaması üretirdi; gerçek
    // düzeltme metric panelinin kendi viz desteğidir — ayrı dilim.
    const config: MetricPanelConfig = {
      metricName: q.metric,
      ...(q.scope ? { service: q.scope } : {}),
      ...(q.agg ? { agg: q.agg } : {}),
      ...(groupBy ? { groupBy } : {}),
      ...(step ? { step } : {}),
      ...(q.filters.length ? { filters: JSON.stringify(q.filters) } : {}),
    };
    return { id: rid(), type: 'metric', title, width: 2, config };
  }

  // Span query — the scope slot is a service.name pin in Explore's
  // compiler; fold it into the flat filter list the same way.
  const filters: FilterExpr[] = q.scope
    ? [{ k: 'service.name', op: '=', v: [q.scope] }, ...q.filters]
    : q.filters;
  const viz = vizToPanel(opts?.viz);
  const config: SpanMetricPanelConfig = {
    agg: q.agg,
    ...(q.metric && q.metric !== 'duration_ms' ? { field: q.metric } : {}),
    ...(groupBy ? { groupBy } : {}),
    ...(step ? { step } : {}),
    ...(viz ? { viz } : {}),
    ...(q.dsl.trim() ? { dsl: q.dsl.trim() } : {}),
    ...(filters.length ? { filters: JSON.stringify(filters) } : {}),
  };
  return { id: rid(), type: 'spanmetric', title, width: 2, config };
}
