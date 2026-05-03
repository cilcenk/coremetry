export interface Service {
  name: string;
  spanCount: number;
  errorCount: number;
  errorRate: number;
  avgDurationMs: number;
  p99DurationMs: number;
  apdex: number;            // 0..1 user-satisfaction score
  apdexThresholdMs: number; // T (default 200)
}

export interface Exception {
  type: string;
  message: string;
  service: string;
  count: number;
  lastSeen: number;         // unix nanoseconds
  sampleTraceId: string;
  sampleSpanId: string;
}

export interface ServiceEdge {
  source: string;
  target: string;
  callCount: number;
  errorRate: number;
  avgMs: number;
}

export interface TraceRow {
  traceId: string;
  rootName: string;
  serviceName: string;
  startTime: number;     // unix nanoseconds
  durationMs: number;
  spanCount: number;
  hasError: boolean;
}

export interface TracesResponse {
  total: number;
  traces: TraceRow[];
}

export interface SpanEvent {
  name: string;
  timeNano: number;
  attributes: Record<string, string>;
}

export interface SpanRow {
  traceId: string;
  spanId: string;
  parentSpanId: string;
  name: string;
  kind: string;
  serviceName: string;
  hostName: string;
  startTime: number;     // unix nanoseconds
  endTime: number;       // unix nanoseconds
  durationMs: number;
  statusCode: string;    // 'ok' | 'error' | 'unset'
  statusMessage: string;
  attributes: Record<string, string>;
  resourceAttributes: Record<string, string>;
  events: SpanEvent[] | null;
  scopeName: string;
  dbSystem?: string;
  dbStatement?: string;
  httpMethod?: string;
  httpRoute?: string;
  httpStatus?: number;
  peerService?: string;
}

export interface TraceDetailResponse {
  traceId: string;
  spans: SpanRow[];
}

export interface LogRow {
  id: number;
  timestamp: number;     // unix nanoseconds
  severity: number;
  severityText: string;
  body: string;
  serviceName: string;
  traceId: string;
  spanId: string;
  attributes: Record<string, string>;
  resourceAttributes: Record<string, string>;
}

export interface LogsResponse {
  total: number;
  logs: LogRow[];
}

export interface MetricInfo {
  name: string;
  description: string;
  unit: string;
  type: string;
}

export interface MetricPoint {
  time: number;
  value: number;
  count: number;
  sum: number;
  attrs: string;
}

export interface HealthInfo {
  status: string;
  spans_queued: number;
  logs_queued: number;
  metrics_queued: number;
  spans_dropped: number;
}

export type SortColumn = 'time' | 'duration' | 'spans' | 'service' | 'operation' | 'status';

// ── Advanced filter expressions ─────────────────────────────────────────────

export type FilterOp =
  | '=' | '!='
  | 'LIKE' | 'NOT LIKE'
  | 'IN' | 'NOT IN'
  | '>' | '>=' | '<' | '<='
  | 'EXISTS' | 'NOT EXISTS';

export interface FilterExpr {
  k: string;        // attribute key — well-known or custom
  op: FilterOp;
  v: string[];      // single value for most ops, multiple for IN/NOT IN
}

// ── Span metrics (Tempo span-metrics + Dynatrace MDA) ────────────────────────

export type SpanAgg =
  | 'count' | 'rate' | 'errors' | 'error_rate'
  | 'avg' | 'sum' | 'min' | 'max'
  | 'p50' | 'p90' | 'p95' | 'p99' | 'p999';

export interface SpanMetricSeries {
  groupKey: string[];                  // raw tuple, joined for label
  points: { time: number; value: number }[]; // time = unix nanoseconds
}

// ── Alerts & Problems ───────────────────────────────────────────────────────

export interface AlertRule {
  id: string;
  name: string;
  service: string;
  metric: string;
  comparator: string;
  threshold: number;
  windowSec: number;
  severity: string;     // info | warning | critical
  enabled: boolean;
  builtIn: boolean;
  createdAt: number;
}

export interface Problem {
  id: string;
  ruleId: string;
  ruleName: string;
  severity: string;
  service: string;
  metric: string;
  value: number;
  threshold: number;
  status: string;       // open | resolved
  description: string;
  startedAt: number;
  resolvedAt?: number;
}

export interface ServiceEdgeStats {
  service: string;
  calls: number;
  errorRate: number;
  avgMs: number;
  p99Ms: number;
}

// ── Time range ───────────────────────────────────────────────────────────────
//
// `preset` is one of the strings in PRESET_SECONDS (lib/utils.ts) — '1h',
// '24h', etc. — OR the literal 'custom' to indicate fromMs/toMs are set.

export interface TimeRange {
  preset: string;
  fromMs?: number;   // unix ms (only when preset === 'custom')
  toMs?: number;     // unix ms
}

// ── Aggregation ──────────────────────────────────────────────────────────────

export interface AggregateRow {
  groupKey: string;
  groupExtra?: string;
  traceCount: number;
  errorCount: number;
  errorRate: number;
  avgMs: number;
  p50Ms: number;
  p95Ms: number;
  p99Ms: number;
  maxMs: number;
  lastSeen: number; // unix nanoseconds
}

// ── SLO ─────────────────────────────────────────────────────────────────────

export type SLIType = 'availability' | 'latency';

export interface SLO {
  id: string;
  name: string;
  service: string;
  sliType: SLIType;
  target: number;        // 0..1
  windowDays: number;
  thresholdMs: number;   // latency only
  operation: string;     // optional span-name filter
  createdAt: number;
}
export interface SLOStatus {
  total: number;
  good: number;
  bad: number;
  sli: number;
  budgetRemaining: number; // 0..1
  burnRate: number;        // > 1 means consuming faster than budget allows
  healthy: boolean;
}
export interface SLORow extends SLO {
  status?: SLOStatus | null;
}

// ── Dashboards ───────────────────────────────────────────────────────────────

export type PanelType = 'metric' | 'spanmetric' | 'stat' | 'markdown';
export type PanelWidth = 1 | 2 | 3 | 4;  // 1=quarter … 4=full (12-col grid)

// Each panel type has a different config shape. Kept as a tagged union so
// the renderer can switch on `type` exhaustively.
export interface MetricPanelConfig {
  metricName: string;
  service?: string;
  agg?: string;            // avg | sum | p95 | p99 | …
  groupBy?: string;        // comma-sep keys
  step?: number;           // bucket seconds (auto if 0)
  filters?: string;        // JSON FilterExpr[]
}
export interface SpanMetricPanelConfig {
  agg: string;             // count | error_rate | p95 | …
  field?: string;          // duration_ms (default) or attribute
  groupBy?: string;
  dsl?: string;            // multi-line DSL (AND-joined)
  filters?: string;        // JSON FilterExpr[]
  step?: number;
}
export interface StatPanelConfig {
  source: 'metric' | 'spanmetric';
  metric?: MetricPanelConfig;
  span?: SpanMetricPanelConfig;
  unit?: string;            // ms | % | rps | (free text)
  decimals?: number;
}
export interface MarkdownPanelConfig {
  text: string;
}

export interface Panel {
  id: string;
  type: PanelType;
  title: string;
  width: PanelWidth;
  config: MetricPanelConfig | SpanMetricPanelConfig | StatPanelConfig | MarkdownPanelConfig;
}

export interface DashboardSummary {
  id: string;
  name: string;
  description: string;
  createdAt: number;
  updatedAt: number;
}
export interface Dashboard extends DashboardSummary {
  panels: Panel[];
}

// ── Profiling ────────────────────────────────────────────────────────────────

export interface ProfileRow {
  profileId: string;
  serviceName: string;
  hostName: string;
  profileType: string;     // "cpu" | "heap" | ...
  startTime: number;       // unix nanoseconds
  durationMs: number;
  sampleCount: number;
}

export interface FlameNode {
  name: string;
  file?: string;
  line?: number;
  value: number;
  self?: number;
  children?: FlameNode[];
}

export interface ProfileDetail {
  meta: ProfileRow;
  flame: FlameNode;
}
export type SortOrder = 'asc' | 'desc';
