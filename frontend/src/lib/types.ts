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

// One row of the system status grid on /status. Mirrors the
// componentStatus / systemStatus types in internal/api.
// ── Incident management ──────────────────────────────────────────────────────

export type IncidentStatus = 'open' | 'acknowledged' | 'resolved';

export interface Incident {
  id: string;
  title: string;
  severity: 'info' | 'warning' | 'critical';
  status: IncidentStatus;
  service?: string;
  summary?: string;
  assignee?: string;
  postmortem?: string;
  startedAt: number;
  ackAt?: number;
  resolvedAt?: number;
  updatedAt: number;
}

export interface IncidentEvent {
  incidentId: string;
  time: number;
  kind: 'created' | 'ack' | 'resolved' | 'note' | 'problem_attached' | 'problem_resolved';
  actor?: string;
  body?: string;
  refId?: string;
}

// ── Runtime settings ─────────────────────────────────────────────────────────

// Data-retention override per signal, expressed as "<n>h" or "<n>d".
// Empty / unset field = preserve the existing value (config default
// or prior override). Server validates the format on PUT.
export interface RetentionSpec {
  spans?: string;
  logs?: string;
  metrics?: string;
  profiles?: string;
}

// AI Copilot config edited from Settings. apiKey is write-only — the
// GET response never includes it; hasKey is the masked indicator.
export type AIProvider = 'anthropic' | 'github';
export interface AISettings {
  provider: AIProvider;
  model: string;
  hasKey: boolean;
}
export interface AISettingsInput {
  provider: AIProvider;
  apiKey: string;
  model?: string;
}

// Role hierarchy used everywhere. `editor` was introduced for the
// LDAP enterprise rollout — admin/users/system-settings stay admin-
// only, dashboards/monitors/alerts/incidents are open to editor too.
export type Role = 'admin' | 'editor' | 'viewer';

// LDAP / AD enterprise auth — config edited from Settings, persisted
// in system_settings. BindPassword is sent as the literal string
// "__SET__" by the GET endpoint when one is saved (so the form can
// show a masked placeholder); leaving the field empty on PUT keeps
// the saved value.
export interface LDAPGroupRoleMapping {
  group: string;
  role: Role;
}
export interface LDAPConfig {
  enabled: boolean;
  host: string;
  port: number;
  useTLS: boolean;
  startTLS: boolean;
  skipVerify: boolean;
  caCert?: string;
  bindDN: string;
  bindPassword: string;
  baseDN: string;
  userSearchFilter: string;
  userAttribute: string;
  emailAttribute: string;
  displayAttribute: string;
  groupSearchBase: string;
  groupFilter: string;
  defaultRole: Role;
  groupRoleMap: LDAPGroupRoleMapping[];
}
export interface LDAPDirectoryUser {
  dn: string;
  username: string;
  email: string;
  displayName: string;
  groups?: string[];
}

// ── Public status page (admin types) ─────────────────────────────────────────

export interface StatusPageConfig {
  title: string;
  description?: string;
  supportUrl?: string;
}

export interface StatusComponent {
  id: string;
  name: string;
  description?: string;
  monitorId?: string;
  serviceName?: string;
  displayOrder: number;
  createdAt: number;
}

export interface StatusSubscriber {
  id: string;
  email: string;
  verified: boolean;
  createdAt: number;
}

// ── Synthetic monitoring ─────────────────────────────────────────────────────

export interface Monitor {
  id: string;
  name: string;
  type: 'http' | 'heartbeat';
  url?: string;
  method?: string;
  expectedStatus?: number;
  timeoutSec?: number;
  intervalSec: number;        // probe period (http) or grace window (heartbeat)
  enabled: boolean;
  heartbeatToken?: string;    // returned by the API on heartbeat-type monitors
  createdAt: number;
}

export interface MonitorResult {
  monitorId: string;
  time: number;               // unix ns
  status: 'up' | 'down' | 'degraded';
  latencyMs: number;
  httpCode?: number;
  message?: string;
}

// Per-monitor rollup over the last 1h / 24h windows. Returned by
// the list endpoint so the page can render uptime % + avg latency
// next to each card without a per-row round-trip. Missing on a
// monitor that hasn't produced a probe in the last 24h.
export interface MonitorStats {
  uptime1h: number;        // 0..100
  uptime24h: number;       // 0..100
  avgLatencyMs1h: number;
  avgLatencyMs24h: number;
  probes24h: number;       // sample size for the 24h numbers
}

// List API rolls the latest result + stats into the row so the list
// page renders without an extra round-trip per monitor.
export interface MonitorRow extends Monitor {
  lastResult?: MonitorResult;
  stats?: MonitorStats;
}

export type ComponentHealth = 'operational' | 'degraded' | 'outage';
export interface StatusComponent {
  name: string;
  status: ComponentHealth;
  message?: string;
  latencyMs?: number;
  // Free-form extras shown alongside the row — version, address, db
  // name, queue depth, etc. Values are strings so the UI doesn't need
  // per-component formatting logic.
  info?: Record<string, string>;
  // Per-second ingest rate; only set on ingest queue components.
  ratePerSec?: number;
}
export interface SystemStatus {
  status: ComponentHealth;
  checkedAt: string;       // RFC 3339
  components: StatusComponent[];
}

// One row of the per-operation aggregate on the service detail page.
// Matches chstore.OperationSummary.
export interface OperationSummary {
  name: string;
  spanCount: number;
  errorCount: number;
  errorRate: number;
  avgDurationMs: number;
  p50DurationMs: number;
  p95DurationMs: number;
  p99DurationMs: number;
  apdex: number;
}

// One 5-minute bucket from the service_summary_5m MV — used to render
// the sparkline thumbnails next to each service row.
export interface SparklineBucket {
  t: number;       // unix ns (bucket start)
  spans: number;
  errs: number;
  avgMs: number;
  p99Ms: number;
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
  // User-requested attribute values (one per `extraAttrs` query
  // param key). Missing/empty values surface as ""; the UI renders
  // them as "—" so empty rows still align visually.
  extras?: Record<string, string>;
}

export interface TracesResponse {
  // Absent in the default ("skip") count mode — clients should treat
  // missing-or-undefined as "unknown" and rely on `hasMore` for paging.
  total?: number;
  traces: TraceRow[];
  // True when the backend pulled Limit+1 rows and the extra row was
  // dropped — i.e. "there's at least one more page after this one".
  hasMore?: boolean;
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

// ── Errors Inbox ────────────────────────────────────────────────────────────

export type ExceptionGroupState =
  | 'new'
  | 'acknowledged'
  | 'resolved'
  | 'regressed'    // auto-flipped from resolved when it occurs again
  | 'ignored';

export interface ExceptionGroup {
  fingerprint: string;
  type: string;
  message: string;
  service: string;
  state: ExceptionGroupState;
  assignee: string;       // user id; '' = unassigned
  firstSeen: number;      // unix ns
  lastSeen: number;       // unix ns
  resolvedAt?: number;    // unix ns, present only when state was/is resolved
  occurrences: number;
  notes: string;
}

export interface ExceptionSample {
  traceId: string;
  spanId: string;
  time: number;          // unix ns
  message: string;       // per-sample exception message — varies within a group
  stacktrace: string;    // raw, may be empty
  spanName: string;      // operation that errored
  statusMsg: string;
}

// ── Settings + notifications ─────────────────────────────────────────────────

export interface SMTPSettings {
  host: string;
  port: number;
  username: string;
  password: string;       // sentinel "********" on read; empty on submit = keep existing
  from: string;
  fromName: string;
  startTLS: boolean;
  skipVerify: boolean;
  configured?: boolean;   // server-side derived
}

export type ChannelType = 'email' | 'slack' | 'mattermost' | 'webhook' | 'whatsapp';

export interface NotificationChannel {
  id: string;
  name: string;
  type: ChannelType;
  // Type-specific union. Optional fields keep the existing email/slack/
  // webhook callers happy; new channels (mattermost shares slack's
  // shape; whatsapp adds Twilio creds) only fill the fields they need.
  config: {
    recipients?: string[];   // email + whatsapp 'to' list
    webhookUrl?: string;     // slack + mattermost
    url?: string;            // generic webhook
    accountSid?: string;     // whatsapp (Twilio)
    authToken?: string;      // whatsapp (Twilio)
    from?: string;           // whatsapp sender (with or without 'whatsapp:' prefix)
    to?: string[];           // whatsapp recipient list
  };
  enabled: boolean;
  minSeverity: 'info' | 'warning' | 'critical';
  createdAt: number;
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
  perMin: number; // traces per minute (Uptrace-style perMin(count()))
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

export type PanelType = 'metric' | 'spanmetric' | 'stat' | 'markdown' | 'row';
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
// Row panels are pure layout markers — they start a new (collapsible)
// row group. Title comes from Panel.title; no extra config needed.
export interface RowPanelConfig {
  collapsed?: boolean;
}

export interface Panel {
  id: string;
  type: PanelType;
  title: string;
  width: PanelWidth;
  config: MetricPanelConfig | SpanMetricPanelConfig | StatPanelConfig | MarkdownPanelConfig | RowPanelConfig;
}

// DashboardVariable — Grafana-style variable. Referenced as ${name} in
// any panel's DSL / service / groupBy / metricName field. Substituted at
// render time with the picker's current value.
//
// Types:
//   - service  populated from /api/service-names; UI is a service picker.
//   - custom   options array; UI is a dropdown of those values.
export interface DashboardVariable {
  name: string;          // e.g. "service" — used as ${service} in panels
  label?: string;        // display label (default: name)
  type: 'service' | 'custom';
  options?: string[];    // custom-type only
  defaultValue?: string; // empty → "all" / no override
}

export interface DashboardSummary {
  id: string;
  name: string;
  description: string;
  createdAt: number;
  updatedAt: number;
}
export interface Dashboard extends DashboardSummary {
  // Optional because list responses skip the heavy fields; only
  // the single-dashboard endpoint guarantees them. Renderer
  // normalises via normalizePanels().
  panels?: Panel[];
  variables?: DashboardVariable[];
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

// One node in the multi-trace path-aggregated structure tree
// returned by GET /api/services/{name}/structure. Each node
// represents a unique `(parent_path → service → operation)` triple
// observed across the sampled traces; siblings repeating the exact
// same triple collapse into a single row carrying count + avg/max
// duration + error count.
// Per-operation error anomaly — a (service, operation) tuple
// that is either failing for the first time in the window or
// whose error count just doubled.
export interface TraceOpAnomaly {
  service: string;
  operation: string;
  kind: 'new_error' | 'error_spike';
  currentErrors: number;
  baselineErrors: number;
  ratio: number;
  sampleTraceId: string;
  lastSeenNs: number;
}

// One curated log-shape anomaly — either brand new in the
// detection window or up 2x+ over baseline. Pattern + regex
// match the server-side definitions in internal/anomaly/log_patterns.go.
export interface LogPatternAnomaly {
  pattern: string;        // human-readable name
  regex: string;          // re2 used for matching
  kind: 'new' | 'spike';
  currentCount: number;
  baselineCount: number;
  ratio: number;
  service: string;
  sample: string;
  lastSeenNs: number;
}

// One entry in the service-level neighbours response — a single
// upstream caller or downstream callee of the inspected service.
export interface NeighborStat {
  service: string;
  traceCount: number;
  spanCount: number;
}

// One row of the inbound-callers backtrace — a unique
// (caller service × caller pod / instance × client IP × user agent)
// combination calling the inspected service over the window.
export interface CallerRow {
  callerService: string;
  callerHost: string;
  callerInstance: string;
  clientAddress: string;
  userAgent: string;
  calls: number;
  errors: number;
  errorRate: number;
  avgMs: number;
  p50Ms: number;
  p95Ms: number;
  p99Ms: number;
  lastSeenNs: number;
}

// Meta-observability snapshot — what /admin/stats renders. All
// fields are optional so a partial / lagging payload still parses.
export interface SystemStats {
  snapshot: {
    spans24h: number;
    spans7d: number;
    spansAllTime: number;
    errors24h: number;
    logs24h: number;
    logsAllTime: number;
    metrics24h: number;
    metricsAllTime: number;
    profiles24h: number;
    profilesAllTime: number;
    services24h: number;
    operations24h: number;
    totalDiskBytes: number;
  };
  tables: {
    table: string;
    rows: number;
    bytesOnDisk: number;
    compressedBytes: number;
    uncompressedBytes: number;
    parts: number;
    oldestNs: number;
    newestNs: number;
  }[];
  history: {
    day: string;
    spans: number;
    errors: number;
    traces: number;
    services: number;
  }[];
  ingest: {
    spansPerSec: number;
    logsPerSec: number;
    metricsPerSec: number;
  };
}

export interface AggSpanNode {
  service: string;
  operation: string;
  kind?: string;
  count: number;
  avgMs: number;
  maxMs: number;
  errorCount: number;
  avgStartMs: number;
  children?: AggSpanNode[];
}
