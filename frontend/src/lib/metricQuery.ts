import type { TimeRange } from './types';

// metricQuery.ts — "Every metric is a doorway." The canonical typed
// descriptor that every metric panel/KPI carries and the Metric Explorer
// consumes. The SAME object that draws a panel is the object the explorer
// opens, so there are no per-page click handlers: a panel exposes its
// MetricQuery, the affordance turns it into a deep link, the explorer decodes
// it back. Resolution descriptor→ClickHouse lives server-side (one place);
// this module is the contract + the lossless URL codec.

export type MetricSource = 'spanmetrics' | 'tracemetrics';
export type MetricAgg =
  | 'rate' | 'count' | 'sum' | 'avg'
  | 'p50' | 'p90' | 'p95' | 'p99'
  | 'error_rate' | 'errors';
export type MetricUnit = 'rps' | 'ms' | '%' | 'count';
export type MetricViz = 'line' | 'area' | 'bar' | 'stat' | 'heatmap' | 'topN';

// MetricQuery is the descriptor. Kept self-contained in this contract module
// (its codec + helpers live here); panels import it rather than re-deriving a
// shape per page.
export interface MetricQuery {
  source: MetricSource;             // which pipeline feeds it
  metric: string;                   // calls_total | duration_milliseconds_bucket | traces_total …
  agg: MetricAgg;
  unit: MetricUnit;
  filters: Record<string, string>;  // service.name, span.kind, http.route, status, deployment.environment …
  groupBy?: string[];               // split series
  viz: MetricViz;
  step?: string;                    // 'auto' | '30s' | '1m' …
  range?: TimeRange;                // inherits the page's global picker unless overridden
}

// defaultUnit picks the natural unit for an aggregation so a panel author only
// has to state metric+agg; the explorer + formatters read unit from here.
export function defaultUnit(agg: MetricAgg): MetricUnit {
  switch (agg) {
    case 'rate':
      return 'rps';
    case 'error_rate':
      return '%';
    case 'avg':
    case 'p50':
    case 'p90':
    case 'p95':
    case 'p99':
      return 'ms';
    default:
      return 'count';
  }
}

// metricQuery is the single constructor/normalizer — every panel builds its
// descriptor through this so defaults (source/unit/viz/filters) are uniform.
export function metricQuery(
  p: Partial<MetricQuery> & Pick<MetricQuery, 'metric' | 'agg'>,
): MetricQuery {
  return {
    source: p.source ?? 'spanmetrics',
    metric: p.metric,
    agg: p.agg,
    unit: p.unit ?? defaultUnit(p.agg),
    filters: p.filters ?? {},
    groupBy: p.groupBy,
    viz: p.viz ?? 'line',
    step: p.step,
    range: p.range,
  };
}

// ── Lossless URL codec ──────────────────────────────────────────────────────
// The descriptor rides the URL as ?m=<base64url(JSON)> so a deep link restores
// the exact panel/explore state (descriptor + range + viz) for sharing. UTF-8
// safe (service names can be non-ASCII).

export function encodeMetricQuery(mq: MetricQuery): string {
  return base64urlEncode(JSON.stringify(mq));
}

export function decodeMetricQuery(s: string | null | undefined): MetricQuery | null {
  if (!s) return null;
  try {
    const mq = JSON.parse(base64urlDecode(s)) as MetricQuery;
    // Minimal shape validation — the two load-bearing fields must be strings.
    if (!mq || typeof mq.metric !== 'string' || typeof mq.agg !== 'string') return null;
    if (!mq.filters || typeof mq.filters !== 'object') mq.filters = {};
    if (!mq.source) mq.source = 'spanmetrics';
    if (!mq.viz) mq.viz = 'line';
    if (!mq.unit) mq.unit = defaultUnit(mq.agg);
    return mq;
  } catch {
    return null;
  }
}

// describeMetricQuery renders a read-only PromQL-style expression for the
// descriptor — `agg(metric{filters}) [by (groupBy)]`. Display-only: the "View
// query" affordance shows it so the operator can read/copy what a panel maps to
// without learning Coremetry's internal DSL. NOT a parser input (the URL codec
// is the lossless round-trip); this is the human-legible projection.
export function describeMetricQuery(mq: MetricQuery): string {
  // filters render as a `{k="v", …}` matcher block, keys sorted for a stable
  // string (two panels with the same filter set read identically).
  const filterEntries = Object.entries(mq.filters ?? {})
    .filter(([, v]) => v !== '' && v != null)
    .sort(([a], [b]) => a.localeCompare(b));
  const matcher = filterEntries.length
    ? `{${filterEntries.map(([k, v]) => `${k}="${v}"`).join(', ')}}`
    : '';
  const expr = `${mq.agg}(${mq.metric}${matcher})`;
  const by = mq.groupBy && mq.groupBy.length
    ? ` by (${mq.groupBy.join(', ')})`
    : '';
  return expr + by;
}

// metricExploreHref is the canonical "open this metric in the explorer" link.
// The reusable panel affordance + every click-to-explore uses it; the Explorer
// route decodes ?m= back into its builder.
export function metricExploreHref(mq: MetricQuery): string {
  return `/explore?m=${encodeMetricQuery(mq)}`;
}

function base64urlEncode(s: string): string {
  const bytes = new TextEncoder().encode(s);
  let bin = '';
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function base64urlDecode(s: string): string {
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/');
  const bin = atob(b64);
  const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
  return new TextDecoder().decode(bytes);
}
