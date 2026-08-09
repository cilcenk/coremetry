// Chart build signatures (v0.8.520/531) — the pure seam behind each chart
// engine's "rebuild vs setData" decision.
//
// A signature is a STABLE string of every input that, when it changes, forces
// a full uPlot re-create: series structure (count/labels/order), axis units,
// height, log scale, cursor-sync key, drag-zoom / bucket-click PRESENCE, and
// the deploy/threshold/region overlays. Two calls that differ ONLY in series
// data-point values (the 30s poll refresh) produce an IDENTICAL signature, so
// the chart rides the `u.setData()` fast-path instead of destroy()+new
// uPlot() — no canvas flicker, no lost cursor/zoom/isolation.
//
// Deliberately NOT in a signature (each handled without a rebuild):
//   • series data points + x values → the setData() fast-path itself.
//   • theme        → useThemeTick counter; a separate dep so a toggle
//                    re-resolves the CSS-var colors (theme change MUST rebuild).
//
// v0.9.844 — `chartBuildSignature` + ChartBuildSigInput / ChartSigDeploy /
// ChartSigThreshold SİLİNDİ. Tek tüketicileri MultiLineChart'ın kendi uPlot
// gövdesiydi; o gövde eski motorla birlikte söküldü ve MLC artık CorePanel'e
// köprü kuran bir adaptör (kendi build effect'i yok, imzalayacak bir şey de).
// ChartSigRegion / ChartSigColorThreshold KALDI — onlar üç imzanın ORTAK
// digest tipleri, MLC'ye özel değildi.
//
// Keeping these pure + exported lets a vitest table assert the exact contract:
// data-only change → same signature (fast-path); any structural/option change
// → different signature (rebuild). See chartBuildSig.test.ts.

// Grafana-parite M3 — problem/anomali x-bölgesi (lib/chart/overlays.ts
// ChartTimeRegion). Draw hook'lar build anında kablolanır ve bölge DİZİSİNİ
// closure'larlar; değer değişimi (yeni problem penceresi) rebuild ister.
// Deploys/thresholds gibi DEĞERE göre digest edilir — her render'da taze ama
// aynı-değerli dizi geçiren çağıran fast-path'i bozmaz.
export interface ChartSigRegion {
  fromSec: number;
  toSec: number;
  color?: string;
  label?: string;
}
const regionsDigest = (rs?: ChartSigRegion[]) =>
  (rs ?? []).map(r => [r.fromSec, r.toSec, r.color ?? '', r.label ?? '']);

// Grafana-parite M3 — OVC/TC'nin yeni renk-tabanlı threshold prop'u
// (overlays.ts ChartThreshold; TSP zaten aynı şekli kendi imzasında taşır).
export interface ChartSigColorThreshold {
  value: number;
  label?: string;
  color?: string;
}
const colorThresholdsDigest = (ts?: ChartSigColorThreshold[]) =>
  (ts ?? []).map(t => [t.value, t.label ?? '', t.color ?? '']);

// ─────────────────────────────────────────────────────────────────────────────
// timeChartBuildSignature (v0.8.531 — perf #5/#15 follow-up) — the same
// rebuild-vs-setData seam, for <TimeChart> (charts/TimeChart.tsx: the bar/line/
// area primitive VolumeChart + ProblemDetail occurrences draw on).
//
// TimeChart's option-affecting inputs are its per-series SHAPE (key/label/
// colour token/type/axis/width), the two axis units, deploy vlines, sync key,
// and — the churn culprit — the PRESENCE of onBrush / fmtLeft / fmtRight / fmtX.
// Callers hand fresh arrows each render (VolumeChart: `fmtRight={v=>…}`); the
// live functions are read through refs, so only their PRESENCE (which flips an
// axis-formatter or the drag affordance) belongs here. Series POINT VALUES and
// the x `times` array are deliberately absent — they ride u.setData(). The
// y-scale re-fits on the fast-path because TimeChart derives its y range +
// splits from the live scale (not a build-time constant). `renderable` (≥2 x
// points) flips the sig so an empty→data transition creates the plot.
export interface TimeChartSigSeries {
  key: string;
  label: string;
  color: string;      // a CSS var() token, resolved to hex at draw time
  type: string;       // 'bar' | 'line' | 'area'
  axis?: string;      // 'left' | 'right'
  width?: number;
}
export interface TimeChartSigInput {
  series: TimeChartSigSeries[];
  height: number;
  leftUnit?: string;
  rightUnit?: string;
  deployMarkers?: number[];
  syncKey?: string;
  hasBrush: boolean;
  hasFmtLeft: boolean;
  hasFmtRight: boolean;
  hasFmtX: boolean;
  renderable: boolean;
  // Grafana-parite M3 — threshold çizgileri + problem/anomali x-bölgeleri.
  // Overlay plugin build anında dizileri closure'lar; değer değişimi rebuild.
  thresholds?: ChartSigColorThreshold[];
  regions?: ChartSigRegion[];
}
export function timeChartBuildSignature(p: TimeChartSigInput): string {
  return JSON.stringify([
    p.series.map(s => [s.key, s.label, s.color, s.type, s.axis ?? 'left', s.width ?? 0]),
    p.height,
    p.leftUnit ?? '',
    p.rightUnit ?? '',
    p.deployMarkers ?? [],
    p.syncKey ?? '',
    p.hasBrush, p.hasFmtLeft, p.hasFmtRight, p.hasFmtX,
    p.renderable,
    colorThresholdsDigest(p.thresholds),
    regionsDigest(p.regions),
  ]);
}

// ─────────────────────────────────────────────────────────────────────────────
// overviewChartBuildSignature (v0.8.531) — the seam for the compact service-
// Overview RED chart (pages/service/charts/OverviewChart.tsx; also Incident).
// Option inputs: per-series label + colour token, render mode (line/area/
// stacked — stacked/area change the fill + band structure), the y unit, height,
// and the deploy marker (time + label). Point VALUES ride setData; the y range
// + splits re-fit from the live scale on the fast-path exactly as the rebuild
// did. `renderable` (≥2 x points) covers empty→data.
export interface OverviewChartSigInput {
  series: { label: string; color: string }[];
  height: number;
  mode?: string;
  unit?: string;
  deployAtSec?: number | null;
  deployLabel?: string;
  renderable: boolean;
  // v0.8.534 — drag-zoom PRESENCE: the setSelect hook + cursor.drag are
  // wired at build time only when onZoom is passed, so a none→some
  // transition must rebuild.
  hasZoom?: boolean;
  // Grafana-parite M1 — cursor.sync build anında kablolanır (MLC/TC/TSP
  // emsali): key değişimi/gelişi rebuild ister.
  syncKey?: string;
  // Grafana-parite M3 — threshold çizgileri + problem/anomali x-bölgeleri.
  thresholds?: ChartSigColorThreshold[];
  regions?: ChartSigRegion[];
}
export function overviewChartBuildSignature(p: OverviewChartSigInput): string {
  return JSON.stringify([
    p.series.map(s => [s.label, s.color]),
    p.height,
    p.mode ?? 'line',
    p.unit ?? '',
    p.deployAtSec ?? null,
    p.deployLabel ?? '',
    p.renderable,
    !!p.hasZoom,
    p.syncKey ?? '',
    colorThresholdsDigest(p.thresholds),
    regionsDigest(p.regions),
  ]);
}

// ─────────────────────────────────────────────────────────────────────────────
// timeSeriesPanelBuildSignature (v0.8.531) — the seam for <TimeSeriesPanel>
// (viz/TimeSeriesPanel.tsx: the Grafana-grade Explore primitive with dual axis,
// stacked/bars, deploy + event annotations, thresholds, and exemplar ◆). Option
// inputs: per-series label/colour/axis/unit/dash, render mode, log scale, sync
// key, drag-zoom PRESENCE, height, and the deploy/event/threshold overlays by
// VALUE. Two data-derived-but-structural fields also live here:
//   • hasExemplars — the `draw`/click hooks are only REGISTERED when some
//     series carries exemplars at build; a none→some transition must rebuild to
//     wire them (their VALUES then ride refs, redrawn on setData).
//   • pointsTier   — series `points.show`/`size` is baked at init from the point
//     count (≤100 / ≤300 / more); bucket it so crossing a threshold rebuilds,
//     but a steady poll (same tier) does not.
// Series POINT VALUES + exemplar positions ride setData/refs. `renderable`
// covers the empty→data transition (series present but 0 x points).
export interface TSPSigSeries {
  label: string;
  color?: string;
  axis?: string;
  unit?: string;
  dash?: number[];
}
export interface TSPBuildSigInput {
  series: TSPSigSeries[];
  mode: string;
  logScale?: boolean;
  syncKey?: string;
  hasZoom: boolean;
  height: number;
  deploys?: number[];
  events?: { timeUnixNs: number; kind: string; label?: string }[];
  thresholds?: { value: number; label?: string; color?: string }[];
  // Grafana-parite M3 — problem/anomali x-bölgeleri (draw hook kaydı +
  // closure'ı build anında; değer değişimi rebuild ister).
  regions?: ChartSigRegion[];
  hasExemplars: boolean;
  pointsTier: number;
  renderable: boolean;
}
export function timeSeriesPanelBuildSignature(p: TSPBuildSigInput): string {
  return JSON.stringify([
    p.series.map(s => [s.label, s.color ?? '', s.axis ?? 'left', s.unit ?? '', s.dash ?? []]),
    p.mode,
    !!p.logScale,
    p.syncKey ?? '',
    p.hasZoom,
    p.height,
    p.deploys ?? [],
    (p.events ?? []).map(e => [e.timeUnixNs, e.kind, e.label ?? '']),
    (p.thresholds ?? []).map(t => [t.value, t.label ?? '', t.color ?? '']),
    p.hasExemplars,
    p.pointsTier,
    p.renderable,
    regionsDigest(p.regions),
  ]);
}
