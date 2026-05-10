import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import type {
  Panel, MetricPanelConfig, SpanMetricPanelConfig, StatPanelConfig, MarkdownPanelConfig,
  SpanMetricSeries, TimeRange,
} from '@/lib/types';
import { timeRangeToNs, substituteVars } from '@/lib/utils';
import { MultiLineChart } from '../MultiLineChart';
import { Spinner } from '../Spinner';

// PanelRenderer dispatches on panel.type. Self-contained — fetches its
// own data, re-fetches when `range` changes. Errors are surfaced inline
// instead of crashing the whole dashboard.
export function PanelRenderer({ panel, range, vars, syncKey }: {
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
}) {
  switch (panel.type) {
    case 'metric':
      return <MetricPanel cfg={applyVarsToMetric(panel.config as MetricPanelConfig, vars)} range={range} syncKey={syncKey} />;
    case 'spanmetric':
      return <SpanMetricPanel cfg={applyVarsToSpan(panel.config as SpanMetricPanelConfig, vars)} range={range} syncKey={syncKey} />;
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

function applyVarsToMetric(cfg: MetricPanelConfig, vars?: Record<string, string>): MetricPanelConfig {
  if (!vars) return cfg;
  return {
    ...cfg,
    metricName: expand(cfg.metricName, vars) ?? '',
    service:    expand(cfg.service, vars),
    groupBy:    expand(cfg.groupBy, vars),
    filters:    expand(cfg.filters, vars),
  };
}

function applyVarsToSpan(cfg: SpanMetricPanelConfig, vars?: Record<string, string>): SpanMetricPanelConfig {
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

function MetricPanel({ cfg, range, syncKey }: { cfg: MetricPanelConfig; range: TimeRange; syncKey?: string }) {
  const [series, setSeries] = useState<SpanMetricSeries[] | null | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!cfg.metricName) { setError('Configure a metric name'); return; }
    setSeries(undefined); setError(null);
    const { from, to } = timeRangeToNs(range);
    api.metricQuery({
      name: cfg.metricName, service: cfg.service, agg: cfg.agg,
      groupBy: cfg.groupBy, from, to, step: cfg.step,
    }).then(s => setSeries(s ?? [])).catch(e => setError(e.message));
  }, [JSON.stringify(cfg), range]);

  if (error) return <PanelError msg={error} />;
  if (series === undefined) return <PanelLoading />;
  if (!series || series.length === 0) return <PanelEmpty />;
  return <MultiLineChart series={series} height={280} syncKey={syncKey} />;
}

// ── Span metric line chart ──────────────────────────────────────────────────

function SpanMetricPanel({ cfg, range, syncKey }: { cfg: SpanMetricPanelConfig; range: TimeRange; syncKey?: string }) {
  const [series, setSeries] = useState<SpanMetricSeries[] | null | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!cfg.agg) { setError('Configure an aggregation'); return; }
    setSeries(undefined); setError(null);
    const { from, to } = timeRangeToNs(range);
    api.spanMetric({
      agg: cfg.agg, field: cfg.field, groupBy: cfg.groupBy,
      filters: cfg.filters, dsl: cfg.dsl,
      from, to, step: cfg.step,
    }).then(s => setSeries(s ?? [])).catch(e => setError(e.message));
  }, [JSON.stringify(cfg), range]);

  if (error) return <PanelError msg={error} />;
  if (series === undefined) return <PanelLoading />;
  if (!series || series.length === 0) return <PanelEmpty />;
  return <MultiLineChart series={series} height={280} syncKey={syncKey} />;
}

// ── Single value (last point of the series, with a sparkline) ───────────────

function StatPanel({ cfg, range }: { cfg: StatPanelConfig; range: TimeRange }) {
  const [value, setValue] = useState<number | null | undefined>(undefined);
  const [points, setPoints] = useState<{ time: number; value: number }[]>([]);
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
        setPoints(flat);
        setValue(flat.length > 0 ? flat[flat.length - 1].value : null);
      })
      .catch(e => setError(e.message));
  }, [JSON.stringify(cfg), range]);

  if (error) return <PanelError msg={error} />;
  if (value === undefined) return <PanelLoading />;

  const decimals = cfg.decimals ?? 2;
  const display = value === null ? '—'
    : isFinite(value) ? value.toFixed(decimals)
    : String(value);

  return (
    <div style={{
      display: 'flex', flexDirection: 'column',
      alignItems: 'center', justifyContent: 'center',
      height: 220, gap: 4,
    }}>
      <div style={{ fontSize: 42, fontWeight: 600, color: 'var(--accent2)' }}>
        {display}
        {cfg.unit && (
          <span style={{ fontSize: 18, marginLeft: 6, color: 'var(--text2)' }}>{cfg.unit}</span>
        )}
      </div>
      {points.length > 1 && (
        <Sparkline points={points} />
      )}
    </div>
  );
}

function Sparkline({ points }: { points: { time: number; value: number }[] }) {
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
  return (
    <svg width={w} height={h} style={{ display: 'block' }}>
      <path d={path} fill="none" stroke="var(--accent)" strokeWidth={1.5} />
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
