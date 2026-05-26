import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import type {
  Panel, MetricPanelConfig, SpanMetricPanelConfig, StatPanelConfig, MarkdownPanelConfig,
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
  switch (panel.type) {
    case 'metric':
      return <MetricPanel cfg={applyVarsToMetric(panel.config as MetricPanelConfig, vars)} range={range} syncKey={syncKey} onZoom={onZoom} dataOverride={dataOverride} />;
    case 'spanmetric':
      return <SpanMetricPanel cfg={applyVarsToSpan(panel.config as SpanMetricPanelConfig, vars)} range={range} syncKey={syncKey} onZoom={onZoom} dataOverride={dataOverride} />;
    case 'stat':
      return <StatPanel cfg={applyVarsToStat(panel.config as StatPanelConfig, vars)} range={range} />;
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
