import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import type {
  Panel, MetricPanelConfig, SpanMetricPanelConfig, StatPanelConfig, GaugePanelConfig, MarkdownPanelConfig,
  SpanMetricSeries, TimeRange,
} from '@/lib/types';
import { timeRangeToNs, substituteVars } from '@/lib/utils';
import { fmtSmart } from '@/lib/chartFmt';
import { MultiLineChart } from '../MultiLineChart';
import { DashboardViz } from '../DashboardViz';
import { Spinner } from '../Spinner';

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
  error?: string;
} | undefined;

export function PanelRenderer({ panel, range, vars, syncKey, onZoom, dataOverride }: {
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
  switch (panel.type) {
    case 'metric':
      return <MetricPanel cfg={applyVarsToMetric(panel.config as MetricPanelConfig, vars)} range={effectiveRange} syncKey={syncKey} onZoom={onZoom} dataOverride={effectiveDataOverride} />;
    case 'spanmetric':
      return <SpanMetricPanel cfg={applyVarsToSpan(panel.config as SpanMetricPanelConfig, vars)} range={effectiveRange} syncKey={syncKey} onZoom={onZoom} dataOverride={effectiveDataOverride} />;
    case 'stat':
      return <StatPanel cfg={applyVarsToStat(panel.config as StatPanelConfig, vars)} range={effectiveRange} />;
    case 'gauge':
      return <GaugePanel cfg={applyVarsToGauge(panel.config as GaugePanelConfig, vars)} range={effectiveRange} />;
    case 'markdown':
      return <MarkdownPanel cfg={panel.config as MarkdownPanelConfig} />;
    case 'row':
      // Row markers are layout-only; the dashboard page intercepts them
      // before they get here. This branch is a defensive no-op so a
      // rogue render path doesn't crash the page.
      return null;
    default:
      return <PanelError msg={`Unknown panel type: ${(panel as Panel).type}`} />;
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

// ── Metric line chart ───────────────────────────────────────────────────────

function MetricPanel({ cfg, range, syncKey, onZoom, dataOverride }: {
  cfg: MetricPanelConfig; range: TimeRange; syncKey?: string;
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  dataOverride?: PanelDataOverride;
}) {
  const [series, setSeries] = useState<SpanMetricSeries[] | null | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);

  // If the parent has supplied bundled data, route it through
  // local state shape and skip the per-panel fetch entirely.
  // Falls through to own-fetch when dataOverride is undefined
  // OR when the bundle returned neither series nor error for
  // this panel (e.g. the panel was added after the bundle
  // request was built — rare but real during edit flow).
  const hasOverride = dataOverride && (dataOverride.series !== undefined || dataOverride.error);
  useEffect(() => {
    if (hasOverride) {
      if (dataOverride!.error) {
        setSeries(undefined);
        setError(dataOverride!.error);
      } else {
        setSeries(dataOverride!.series ?? []);
        setError(null);
      }
      return;
    }
    if (!cfg.metricName) { setError('Configure a metric name'); return; }
    setSeries(undefined); setError(null);
    const { from, to } = timeRangeToNs(range);
    api.metricQuery({
      name: cfg.metricName, service: cfg.service, agg: cfg.agg,
      groupBy: cfg.groupBy, from, to, step: cfg.step,
    }).then(s => setSeries(s ?? [])).catch(e => setError(e.message));
  }, [JSON.stringify(cfg), range, hasOverride, JSON.stringify(dataOverride)]);

  if (error) return <PanelError msg={error} />;
  if (series === undefined) return <PanelLoading />;
  if (!series || series.length === 0) return <PanelEmpty />;
  return <MultiLineChart series={series} height={280} syncKey={syncKey} onZoom={onZoom} />;
}

// ── Span metric line chart ──────────────────────────────────────────────────

function SpanMetricPanel({ cfg, range, syncKey, onZoom, dataOverride }: {
  cfg: SpanMetricPanelConfig; range: TimeRange; syncKey?: string;
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  dataOverride?: PanelDataOverride;
}) {
  const [series, setSeries] = useState<SpanMetricSeries[] | null | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);

  const hasOverride = dataOverride && (dataOverride.series !== undefined || dataOverride.error);
  useEffect(() => {
    if (hasOverride) {
      if (dataOverride!.error) {
        setSeries(undefined);
        setError(dataOverride!.error);
      } else {
        setSeries(dataOverride!.series ?? []);
        setError(null);
      }
      return;
    }
    if (!cfg.agg) { setError('Configure an aggregation'); return; }
    setSeries(undefined); setError(null);
    const { from, to } = timeRangeToNs(range);
    api.spanMetric({
      agg: cfg.agg, field: cfg.field, groupBy: cfg.groupBy,
      filters: cfg.filters, dsl: cfg.dsl,
      from, to, step: cfg.step,
    }).then(s => setSeries(s ?? [])).catch(e => setError(e.message));
  }, [JSON.stringify(cfg), range, hasOverride, JSON.stringify(dataOverride)]);

  if (error) return <PanelError msg={error} />;
  if (series === undefined) return <PanelLoading />;
  if (!series || series.length === 0) return <PanelEmpty />;
  // Dispatch on the configured viz. 'line' (default) keeps the
  // existing uPlot multi-line path; everything else routes
  // through the small SVG-based DashboardViz component.
  const viz = cfg.viz ?? 'line';
  if (viz === 'line') {
    return <MultiLineChart series={series} height={280} syncKey={syncKey} onZoom={onZoom} />;
  }
  return <DashboardViz series={series} viz={viz} height={280} />;
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

function StatPanel({ cfg, range }: { cfg: StatPanelConfig; range: TimeRange }) {
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
  }, [JSON.stringify(cfg), range]);

  if (error) return <PanelError msg={error} />;
  if (value === undefined) return <PanelLoading />;

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
  const thresholdHex = band ? THRESHOLD_COLOURS[band.color] : null;
  const valueColour = colorMode === 'value' && thresholdHex
    ? thresholdHex
    : 'var(--accent2)';
  const bgTint = colorMode === 'background' && thresholdHex
    ? hexToRgba(thresholdHex, 0.12)
    : 'transparent';

  return (
    <div style={{
      display: 'flex', flexDirection: 'column',
      alignItems: 'center', justifyContent: 'center',
      height: 220, gap: 6,
      background: bgTint,
      borderRadius: 6,
      transition: 'background 120ms ease',
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
function GaugePanel({ cfg, range }: { cfg: GaugePanelConfig; range: TimeRange }) {
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
  }, [JSON.stringify(cfg), range]);

  if (error) return <PanelError msg={error} />;
  if (value === undefined) return <PanelLoading />;

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
      height: 220, gap: 4,
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

const THRESHOLD_COLOURS: Record<'green' | 'amber' | 'red', string> = {
  green: 'rgb(46,160,67)',
  amber: 'rgb(217,119,6)',
  red:   'rgb(220,38,38)',
};

function hexToRgba(rgb: string, alpha: number): string {
  // Accepts our `rgb(r,g,b)` tokens; pulls digits, formats rgba.
  const m = rgb.match(/(\d+)\s*,\s*(\d+)\s*,\s*(\d+)/);
  if (!m) return rgb;
  return `rgba(${m[1]},${m[2]},${m[3]},${alpha})`;
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
              : 'rgba(56,139,253,0.12)';
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

function PanelLoading() {
  return <div style={{ height: 220, display: 'grid', placeItems: 'center' }}><Spinner /></div>;
}
function PanelEmpty() {
  return <div style={{ height: 220, display: 'grid', placeItems: 'center', color: 'var(--text3)', fontSize: 13 }}>No data</div>;
}
function PanelError({ msg }: { msg: string }) {
  return (
    <div style={{ height: 220, display: 'grid', placeItems: 'center', padding: 12 }}>
      <div style={{ color: 'var(--err)', fontSize: 12, textAlign: 'center' }}>⚠ {msg}</div>
    </div>
  );
}
