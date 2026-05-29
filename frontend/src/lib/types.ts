export interface Service {
  name: string;
  spanCount: number;
  errorCount: number;
  errorRate: number;
  avgDurationMs: number;
  p99DurationMs: number;
  apdex: number;            // 0..1 user-satisfaction score
  apdexThresholdMs: number; // T (default 200)
  // Auto-scored health badge (v0.5.274). Computed at READ time
  // on the backend from errorRate + open-problem counts;
  // missing on rows where the problem-count lookup failed
  // (renderer treats missing as no badge).
  health?: 'green' | 'yellow' | 'red';
  healthReason?: string;
  openProblems?: number;
}

// Topology view (v0.5.100) — operation-level call graph rooted at
// one service, BFS-bounded by depth. Mirrors api.TopologyResponse.
export interface TopologyNode { id: string; service: string; op: string }
export interface TopologyEdge {
  parentService: string; parentOp: string;
  childService: string;  childOp: string;
  calls: number;
}
export interface TopologyResponse {
  nodes: TopologyNode[];
  edges: TopologyEdge[];
  rootService: string;
  depth: number;
  from: number;
  to: number;
  truncated: boolean;
}

// Service-level topology (v0.5.102) — collapses ops into the
// service node, includes synthetic infra nodes (db, queue, ext)
// and protocol-tagged edges with top endpoint labels.
export type ServiceTopologyNodeKind = 'service' | 'db' | 'queue' | 'external';
export interface ServiceTopologyNode {
  id: string;
  name: string;
  kind: ServiceTopologyNodeKind;
  // v0.5.312 — Phase 2 redux fields. Namespace drives the
  // soft-cluster grouping; health* drive the per-node
  // red/yellow/green ring. All optional + nil-safe.
  namespace?: string;
  health?: '' | 'green' | 'yellow' | 'red';
  healthReason?: string;
  openCritical?: number;
  openWarning?: number;
  // v0.5.409 — known 3rd-party SaaS / cloud annotation. Set
  // by backend external_catalogue lookup for nodes whose peer
  // host matches a recognised vendor (Stripe, Twilio, AWS,
  // Sentry, etc.). UI renders display + category badge in
  // place of the raw hostname.
  extDisplay?: string;
  extKind?: string;
  // v0.5.410 — display-only environment annotation
  // (deployment.environment / service.namespace /
  // k8s.namespace.name). UI renders as a small chip next to
  // the service name on multi-env installs.
  env?: string;
}
export interface ServiceTopologyEdge {
  parentService: string;
  childNode: string;
  nodeKind: ServiceTopologyNodeKind;
  protocol: string;       // "http" | "rpc" | "kafka" | "db" | "internal"
  topLabels: string[];    // up to 5 most-frequent labels
  distinctLabels: number;
  calls: number;
  // v0.5.393 — errors + error-rate on the edge. Drives the tooltip
  // overlay (errors count + percentage) and the red-tinted edge
  // stroke when errorRate ≥ 1%. Backend pipes through from
  // topology_edges_5m.errors (added in v0.5.367).
  errors: number;
  errorRate: number;      // (errors / calls) * 100
  avgMs: number;          // window-wide average latency (ms)
  p99Ms: number;          // conservative window p99 (ms)
  // v0.5.414 — prior-window comparison values. Populated when
  // /api/topology is called with ?compare=prior. Drives the
  // what-changed banner; UI computes the % delta client-side.
  priorCalls?: number;
  priorErrors?: number;
  priorAvgMs?: number;
  priorP99Ms?: number;
  // v0.5.409 — known 3rd-party annotation. Populated by the
  // backend external_catalogue lookup when the node represents
  // a recognised SaaS / cloud endpoint (Stripe, Twilio, AWS,
  // Sentry, etc.). Frontend renders a small category badge.
  extDisplay?: string;    // "Stripe", "SendGrid", "AWS", ...
  extKind?: string;       // "payments" | "messaging" | "email" | "cdn" | "auth" | "cloud" | "observability" | "ai" | ...
}
export interface ServiceTopologyResponse {
  nodes: ServiceTopologyNode[];
  edges: ServiceTopologyEdge[];
  from: number;
  to: number;
  truncated: boolean;
  // v0.6.48 — server-side scoping for thousand-service fabrics.
  // totalServices is the full fabric size before the top-N / focus
  // bound; scoped=true means the returned graph is a bounded subset
  // (so the UI shows a "showing N of M — search/focus to refine"
  // banner). scopeReason describes the bound, e.g. "top-60 by call
  // volume" or "focus: checkout +2 hops".
  totalServices?: number;
  scoped?: boolean;
  scopeReason?: string;
}

// Root-anchored business flows (v0.5.103) — top entry points by
// trace volume; clicking a flow shows its restricted subgraph.
export interface RootFlow {
  rootService: string;
  rootOp: string;
  traceCount: number;
  services: string[];
  // p99 root-span duration in ns over the window (v0.5.156).
  // Omitted when no roots matched the signature (e.g. transient
  // empty bucket). Use ms = p99Ns / 1e6 for display.
  p99Ns?: number;
}
export interface FlowsResponse {
  flows: RootFlow[];
  from: number;
  to: number;
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
  // k8s/openshift clusters the service was active in around
  // the incident — enriched at read time on the server.
  clusters?: string[];
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

// RepeatedSpanRow — one row of the "N+1 / fan-out finder" view.
// Each row is a (trace, group-by-values) pair where the same
// span shape occurred Count times within the same trace.
// Surfaces "I called the same SQL 50× in one request" or
// "ServiceA → ServiceB happened 30× in one trace" patterns.
export interface RepeatedSpanRow {
  traceId: string;
  service: string;
  rootName: string;
  groupValues: string[];
  count: number;
  totalDurationMs: number;
  startedAt: number;
}

// DBInstance — one row of /databases (Dynatrace "Technologies →
// Databases" equivalent). Distinct (db_system, instance) seen in
// span traffic over the window, with RED-metrics + the top-5
// callers. The system + instance discriminate the actual physical
// DB while the callers list answers "which services depend on
// this DB" without leaving the page.
export interface DBInstance {
  system: string;
  instance: string;
  // v0.5.315 — db.name split. One host can serve many DBs;
  // row identity is (system, instance, dbName). 'default'
  // means the OTel SDK didn't emit db.name on this span.
  dbName?: string;
  spanCount: number;
  errorCount: number;
  errorRate: number;
  avgDurationMs: number;
  p99DurationMs: number;
  callers: string[];
  // Source: empty / 'spans' = derived from application traffic
  // (the default). 'receiver' = discovered via an OpenTelemetry
  // database receiver (e.g. oracledb) with no application spans
  // yet — RED stats are zero, drill-down opens the receiver
  // panel directly.
  source?: 'spans' | 'receiver';
}

// DBCallerBreakdown — one row of the per-(service, pod)
// breakdown shown in the DB / messaging detail drawer. Pod is
// the resource.host.name on the calling span — k8s pod name on
// Kubernetes, VM hostname elsewhere.
export interface DBCallerBreakdown {
  service: string;
  pod: string;
  // Role is set only for messaging breakdowns (span.kind:
  // producer / consumer / client / server / internal). Empty
  // string for DB rows since DB spans are always CLIENT.
  role?: string;
  spanCount: number;
  errorCount: number;
  errorRate: number;
  avgDurationMs: number;
  p99DurationMs: number;
}

// DBOpStat — one top-operations row. For DBs the Statement is
// the first 80 chars of db_statement (so unparameterised SQL
// collapses). For messaging it's the span name (publish /
// consume / process).
export interface DBOpStat {
  statement: string;
  count: number;
  avgDurationMs: number;
}

// ServiceClusterStat — one row of the per-cluster RED
// breakdown on the Service detail page. Surfaced only when a
// service's traffic spans more than one cluster.
export interface ServiceClusterStat {
  cluster: string;
  spanCount: number;
  errorCount: number;
  errorRate: number;
  avgDurationMs: number;
  p99DurationMs: number;
}

// DBDetail / MessagingDetail — full payloads for the drawer
// behind a /databases or /messaging row click.
export interface DBDetail {
  system: string;
  instance: string;
  spanCount: number;
  errorCount: number;
  errorRate: number;
  avgDurationMs: number;
  p99DurationMs: number;
  callers: DBCallerBreakdown[];
  topOps: DBOpStat[];
}
export interface MessagingDetail {
  system: string;
  cluster: string;
  destination: string;
  spanCount: number;
  errorCount: number;
  errorRate: number;
  avgDurationMs: number;
  p99DurationMs: number;
  callers: DBCallerBreakdown[];
  topOps: DBOpStat[];
}

// OracleMetrics — payload of /api/databases/oracle. Mirrors the
// oracledb receiver's metric shape: gauges with limit, derived
// per-second rates, and a per-tablespace usage table. When the
// receiver isn't wired up, backend fills these with deterministic
// synthetic values and flips synthetic=true so the UI shows a
// "demo data" badge.
export interface OracleMetrics {
  instance: string;
  // synthetic: previously flagged demo fallback. Removed in
  // v0.5.8 — backend now returns zeros (and status=down) when
  // the receiver isn't shipping. Field kept optional for one
  // release for backwards compat with cached responses.
  synthetic?: boolean;
  windowSeconds: number;
  status: 'up' | 'down';
  sessions:  { usage: number; limit: number; active: number; inactive: number };
  processes: { usage: number; limit: number };
  cpuTimeSec: number;
  pgaMemoryBytes: number;
  sgaMemoryBytes: number;
  logicalReadsPerSec: number;
  physicalReadsPerSec: number;
  cacheHitPct: number;
  hardParsesPerSec: number;
  parseCallsPerSec: number;
  executionsPerSec: number;
  userCommitsPerSec: number;
  userRollbacksPerSec: number;
  transactionsPerSec: number;
  rowLockWaitsPerSec: number;
  waitClasses: { name: string; perSec: number }[];
  topSQL: { sql: string; elapsedSec: number; executions: number; avgElapsedMs: number }[];
  tablespaces: { name: string; usedBytes: number; maxBytes: number; usedPct: number }[];
}

// PostgresMetrics — receiver drill-down for one Postgres
// instance. Sourced from OTel postgresql receiver
// metric_points (`postgresql.*`). Empty receiver = zeros +
// status="down" (no synthetic fallback).
export interface PostgresMetrics {
  instance: string;
  status: 'up' | 'down';
  windowSeconds: number;
  backends: { usage: number; limit: number };
  commitsPerSec: number;
  rollbacksPerSec: number;
  deadlocksPerSec: number;
  blocksReadPerSec: number;
  blocksHitPerSec: number;
  cacheHitPct: number;
  tempFilesPerSec: number;
  tempBytesPerSec: number;
  walAgeSec: number;
  walLagBytes: number;
  replicationDelaySec: number;
  bgwriter: {
    buffersAllocatedPerSec: number;
    buffersCheckpointPerSec: number;
    buffersBgwriterPerSec: number;
    buffersBackendPerSec: number;
  };
  databases: { name: string; sizeBytes: number; commitsPerSec: number;
                rollbacksPerSec: number; backendCount: number }[];
  locks: { mode: string; count: number }[];
}

// MySQLMetrics — receiver drill-down for one MySQL instance.
export interface MySQLMetrics {
  instance: string;
  status: 'up' | 'down';
  windowSeconds: number;
  threads: { connected: number; running: number; createdPerSec: number };
  connections: { usage: number; limit: number };
  questionsPerSec: number;
  slowQueriesPerSec: number;
  rowLockWaitsPerSec: number;
  rowLockTimeSec: number;
  tmpDiskTablesPerSec: number;
  openedTablesPerSec: number;
  bufferPool: {
    pagesData: number; pagesDirty: number; pagesFree: number;
    pagesTotal: number; usagePct: number; dirtyPct: number;
  };
  handlers: {
    readFirstPerSec: number; readKeyPerSec: number;
    readNextPerSec: number; readRndNextPerSec: number; writePerSec: number;
  };
  rowOps: {
    insertPerSec: number; updatePerSec: number;
    deletePerSec: number; selectPerSec: number;
  };
  replicaDelaySec: number;
}

// RedisMetrics — receiver drill-down for one Redis instance.
export interface RedisMetrics {
  instance: string;
  status: 'up' | 'down';
  role: 'master' | 'replica' | 'unknown' | string;
  windowSeconds: number;
  uptimeSec: number;
  clients: {
    connected: number; blocked: number;
    maxInputBufferBytes: number; maxOutputBufferBytes: number;
  };
  memory: {
    usedBytes: number; rssBytes: number; peakBytes: number; maxBytes: number;
    fragmentationRatio: number; luaBytes: number; usagePct: number;
  };
  commandsPerSec: number;
  netInputBytesPerSec: number;
  netOutputBytesPerSec: number;
  keyspaceHitsPerSec: number;
  keyspaceMissesPerSec: number;
  hitRatePct: number;
  keysEvictedPerSec: number;
  keysExpiredPerSec: number;
  replicationLagBytes: number;
  changesSinceLastSave: number;
  slowlogEntries: number;
  connectionsRejectedPerSec: number;
  keyspaces: { name: string; keys: number; expires: number }[];
}

// MessagingInstance — same structure for queues / topics. The
// destination field tries messaging.destination.name first, then
// messaging.destination, then peer.service, then 'unknown'.
export interface MessagingInstance {
  system: string;
  // Physical cluster identifier — bootstrap host /
  // messaging.kafka.cluster.name / "(default)" when no
  // cluster-discriminating attribute is on the span. Allows a
  // single Coremetry to track multiple Kafka / MQ clusters
  // under the same msg_system tag.
  cluster: string;
  destination: string;
  spanCount: number;
  errorCount: number;
  errorRate: number;
  avgDurationMs: number;
  p99DurationMs: number;
  callers: string[];
}

// BreakdownPoint — one bucket of the Elastic-APM-style "span
// breakdown" stacked-area chart. Cumulative ms of duration
// grouped by span category for the service detail page.
export interface BreakdownPoint {
  time: number;                   // unix ns (bucket start)
  kinds: Record<string, number>;  // category → ms summed in bucket
}

// Facet — one tag dimension (service.name, http.route, db.system, …)
// with its top-N values for the current /explore window. Drives the
// trace facets panel: operator scans which tags exist + frequency,
// clicks a value to add it as a filter chip.
export interface Facet {
  key: string;
  distinctValues: number;
  values: FacetValue[];
}
export interface FacetValue {
  value: string;
  count: number;
}

// ChangedService — one row of the causal-correlation report (what
// services moved the most around the time a problem fired). Powers
// the "Why did this fire?" expandable on Problems and the future
// Watchdog-style auto-investigation panel.
export interface ChangedService {
  service: string;
  baselineRate: number;       // spans/sec, baseline window
  currentRate: number;        // spans/sec, current window
  rateDeltaPct: number;
  baselineErrorRate: number;  // 0..1
  currentErrorRate: number;
  errDeltaPct: number;
  baselineP99Ms: number;
  currentP99Ms: number;
  p99DeltaPct: number;
  score: number;
  reasons: string[];          // pre-formatted human bullets, render verbatim
}

// RedisStats matches cache.RedisStats — INFO + DBSIZE snapshot
// rendered on the System page. version=="" means Redis is not
// configured (Noop cache active); the UI shows a "wire it up for HA"
// banner instead of the metrics grid.
export interface RedisStats {
  version: string;
  mode: string;
  uptimeSec: number;
  connectedClients: number;
  keys: number;
  usedMemoryBytes: number;
  usedMemoryPeakBytes: number;
  maxMemoryBytes: number;
  hitRate: number;        // 0..1, keyspace_hits / (hits+misses)
  opsPerSec: number;      // instantaneous_ops_per_sec
  netInputKbps: number;
  netOutputKbps: number;
  evictedKeys: number;
  expiredKeys: number;
}

// CacheStats matches api.CacheStatsSnapshot — per-tier hit
// counters and hottest keys for the multi-tier API cache (L1 +
// Redis + singleflight + SWR). counts keys: HIT-L1, HIT,
// STALE, HIT-LEGACY, MISS, BYPASS.
export interface CacheStats {
  sinceUnixNano: number;
  counts: Record<string, number>;
  topKeys: { key: string; hits: number }[];
  l1Size: number;
  l1Cap: number;
}

// AI Copilot config edited from Settings. apiKey is write-only — the
// GET response never includes it; hasKey is the masked indicator.
// baseUrl is provider-specific (only "openai" reads it) and is the
// non-secret pointer at a self-hosted OpenAI-compatible endpoint
// (Ollama, LM Studio, vLLM, etc.) — echoed back so the form shows
// what's wired.
export type AIProvider = 'anthropic' | 'github' | 'openai';
export interface AISettings {
  provider: AIProvider;
  model: string;
  baseUrl: string;
  hasKey: boolean;
  // v0.5.360 — InsecureSkipVerify on the outbound HTTP client.
  // Operator-opt-in for self-hosted LLMs behind an enterprise
  // CA Go's default trust store doesn't know about.
  skipTls?: boolean;
}
export interface AISettingsInput {
  provider: AIProvider;
  apiKey: string;
  model?: string;
  baseUrl?: string;
  skipTls?: boolean;
}

// External Tempo backend (v0.5.208) — fallback for trace-by-id
// when Coremetry sampled the trace out. GET returns the snapshot
// (no token); PUT saves a new config. Empty `token` on PUT
// preserves the previously stored token so the operator can
// toggle Enabled / change orgId without retyping the key.
export type TempoAuthType = '' | 'none' | 'bearer' | 'basic';
export interface TempoSnapshot {
  enabled: boolean;
  baseUrl: string;
  authType?: TempoAuthType;
  hasToken: boolean;
  username?: string;
  orgId?: string;
  // v0.5.218 — operators with self-signed Tempo certs in POC
  // can flip this on to skip TLS chain verification. Default off.
  insecureSkipVerify?: boolean;
}
export interface TempoSettingsInput {
  enabled: boolean;
  baseUrl: string;
  authType?: TempoAuthType;
  token?: string;
  username?: string;
  orgId?: string;
  insecureSkipVerify?: boolean;
}

// External Kibana deep-link config (v0.5.236). Operator-curated
// link target so Logs page rows can offer an "Open in Kibana
// Discover" jump. Empty / disabled = no link rendered.
export interface KibanaSettings {
  enabled: boolean;
  baseUrl: string;
  // Optional Kibana data view id to pin the Discover panel to a
  // specific index pattern. Empty = let Kibana pick the default.
  dataView?: string;
}

// Unified triage inbox (v0.5.211) — merges Problems + Exception
// groups + Anomaly events into one ranked list with a normalised
// priority bucket so operators stop tab-hopping. Each kind keeps
// its own drill-down ref (only one populated per row).
export type InboxKind = 'problem' | 'exception' | 'anomaly';
export interface InboxItem {
  id: string;             // composite "<kind>:<nativeId>"
  kind: InboxKind;
  source: string;         // "Alert rule" | "Exception" | "Anomaly"
  priority: 'P1' | 'P2' | 'P3';
  priorityReason: string;
  severity: string;
  service: string;
  title: string;
  description: string;
  startedAt: number;
  lastSeen: number;
  assignee?: string;
  // Team chips from service_metadata. OwnerTeam = product
  // owners (auto-assigned on Problem open), SRETeam = on-call
  // group. Either / both can be empty when no catalog row.
  ownerTeam?: string;
  sreTeam?: string;
  status: string;
  clusters?: string[];
  problem?: {
    id: string; ruleId: string; metric: string;
    value: number; threshold: number;
  };
  exception?: {
    fingerprint: string; type: string; message: string;
    occurrences: number;
  };
  anomaly?: {
    id: string; kind: string; pattern: string;
    peakRatio: number; currentRatio: number;
  };
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
  // Workaround toggle for AD's MaxValRange / MaxReceiveBuffer
  // caps — drops memberOf from the user-search attrs so the
  // separate group search is authoritative. Required when
  // senior users with thousands of nested groups can't log in.
  skipMemberOfFetch?: boolean;
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

// AI observability (v0.5.163). One row per Copilot LLM call —
// surfaced on the /ai page with KPIs + timeseries + a drill-in
// modal showing prompt + response samples (capped at 4KB each
// at insert time).
export interface AICall {
  id: string;
  createdAt: number;
  surface: string;
  provider: string;
  model: string;
  baseUrl?: string;
  durationMs: number;
  inputTokens: number;
  outputTokens: number;
  status: 'ok' | 'error';
  errorMsg?: string;
  promptChars: number;
  responseChars: number;
  userId?: string;
  userEmail?: string;
  promptSample?: string;
  responseSample?: string;
}

export interface AIStats {
  totalCalls: number;
  okCalls: number;
  errorCalls: number;
  errorRate: number;
  avgDurationMs: number;
  p50DurationMs: number;
  p99DurationMs: number;
  inputTokens: number;
  outputTokens: number;
  distinctUsers: number;
  bySurface: Array<{ surface: string; calls: number; errorRate: number; avgMs: number }>;
  byProvider: Array<{ provider: string; model: string; calls: number; inputTokens: number; outputTokens: number }>;
}

// AI cost rates (v0.5.167). USD per 1M tokens, per model.
// Bundled defaults live frontend-side (see lib/ai-rates.ts);
// admins can override via /api/ai/rates which the UI merges
// over the bundle. Local-model endpoints stay at 0/0 = free.
export interface AIRate {
  inputPer1M: number;
  outputPer1M: number;
}

export interface AICallsTimePoint {
  time: number;
  calls: number;
  errors: number;
  avgMs: number;
  inputTokens: number;
  outputTokens: number;
}

export interface StatusSubscriber {
  id: string;
  email: string;
  verified: boolean;
  // Unix-ns timestamp of the last confirmation-email send. 0 =
  // never sent (e.g. operator-added verified subscriber).
  confirmSentAt?: number;
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
  // Fixed-length call-rate buckets over the same window as the
  // aggregate (chstore.SparklineBuckets = 30). Rendered inline in
  // the table as a small SVG so the operator can spot a slow-burn
  // vs. spike pattern without leaving the page.
  sparkline?: number[];
  // v0.5.392 — companion error + p99 sparklines on the same
  // 30-bucket grid. Drives the per-row metric drill-in modal on
  // the service detail page; both are optional (older backends
  // / raw-spans path may omit them).
  errorsSparkline?: number[];
  p99Sparkline?: number[];
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
  // v0.5.208 — "clickhouse" when the trace was resolved from
  // Coremetry's own store, "tempo" when it came from the
  // external Tempo backend fallback (Coremetry sampled it out).
  // v0.6.34 — "mv_only" when raw spans aged out past the 30-day
  // TTL but trace_summary_5m still holds the aggregate stats
  // (90-day retention). The frontend renders an honest "trace
  // aged out, only aggregates remain" pane instead of a blank
  // waterfall in that case. `stub` carries the aggregate stats.
  source?: 'clickhouse' | 'tempo' | 'mv_only';
  stub?: {
    rootService: string;
    rootName: string;
    startTimeNs: number;
    endTimeNs: number;
    spanCount: number;
    errorCount: number;
    durationMs: number;
  };
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
  // v0.5.280 — cumulative accepted counters for the Topbar
  // live activity ticker (client computes per-sec delta).
  spans_accepted?: number;
  logs_accepted?: number;
  metrics_accepted?: number;
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

// v0.6.56 — explicit OTel histogram over a window: shared bucket bounds,
// one summed bucket-count vector per time bucket, and p50/p95/p99 estimated
// from those vectors at read time. Drives the /metrics histogram heatmap
// (the avg line can't show the distribution; this does).
export interface HistogramResult {
  bounds: number[];     // explicit upper bounds (len N)
  times: number[];      // ns epoch, one per time bucket
  counts: number[][];   // [timeBucket][bucket] summed (len N+1, last = +Inf)
  p50: number[];
  p95: number[];
  p99: number[];
  skipped: number;      // series dropped for a mismatched bucket layout
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
  // Optional URL to the team's runbook for this rule. When set,
  // a "Runbook ↗" button surfaces on Problem detail / alerts
  // notifications so the oncall lands on the playbook in one
  // click instead of digging through Confluence.
  runbookUrl?: string;
  // Noise-dampening knobs (v0.5.127-129). All default to 0 =
  // legacy fire-immediately behaviour; operators opt in per rule.
  forSec?: number;       // sustained breach gate
  minSamples?: number;   // sample-count floor
  cooldownSec?: number;  // post-resolution silence
  // Saved-search log alert (v0.5.242). When populated, the
  // evaluator counts log matches via the logstore in this
  // window and compares to threshold via comparator — instead
  // of running the span-derived Metric path. Operator-defined
  // anomaly coverage to complement the curated regex detector.
  logQuery?: string;
  createdAt: number;
}

// ── Runbooks (v0.7.0) ───────────────────────────────────────────────────────
// Operator-authored executable procedures (OneUptime model). A Runbook is an
// ordered list of steps; automated steps (http/javascript/bash) run on the
// coremetry-agent, manual/query resolve server-side. See
// docs/runbooks-agent-design.md.
export type RunbookStepKind = 'manual' | 'query' | 'http' | 'javascript' | 'bash';

export interface RunbookStep {
  id: string;
  order: number;
  kind: RunbookStepKind;
  title: string;
  instructions?: string;             // markdown
  expected?: string;                 // expected outcome (manual)
  query?: string;                    // kind=query — CH SQL / Explore DSL
  url?: string;                      // kind=http
  method?: string;                   // kind=http
  headers?: Record<string, string>;  // kind=http
  body?: string;                     // kind=http
  timeoutMs?: number;                // kind=http|bash
  script?: string;                   // kind=javascript
  command?: string;                  // kind=bash
}

export interface Runbook {
  id: string;
  title: string;
  description?: string;              // markdown — the "knowledge"
  steps: RunbookStep[];
  enabled: boolean;
  labels?: string[];
  createdBy?: string;
  createdAt: number;
  updatedAt: number;
}

// Noisy-rules report row (v0.5.131). Pairs a rule's open-rate
// stats with a heuristic suggestion + the current knob values
// so the UI can render a one-click "Apply" affordance.
export interface NoisyRule {
  ruleId: string;
  ruleName: string;
  severity: string;
  openCount: number;
  medianDurSec: number;
  lastFiredNs: number;
  totalDurSec: number;
  suggestion: string;
  suggestedForSec?: number;
  suggestedMinSamples?: number;
  suggestedCooldownSec?: number;
  currentForSec: number;
  currentMinSamples: number;
  currentCooldownSec: number;
}

export interface Problem {
  id: string;
  // Runbook URL — composed at read time on the backend from
  // the firing alert rule (preferred) or the service catalog
  // metadata (fallback). Empty when neither carries one.
  runbookUrl?: string;
  ruleId: string;
  ruleName: string;
  severity: string;
  service: string;
  metric: string;
  value: number;
  threshold: number;
  status: string;       // open | resolved
  description: string;
  // Triage assignee (v0.5.209). Two shapes:
  //   • team name auto-set on open from service_metadata.ownerTeam
  //   • email of an operator after manual claim
  // Empty = unassigned.
  assignee?: string;
  // Priority bucket (v0.5.210) — computed at read time from
  // severity + breach magnitude + deploy proximity. P1 = handle
  // now, P2 = handle today, P3 = handle when convenient. UI
  // filter defaults to "P1 + P2 only" so the inbox surfaces
  // signal first. priorityReason is the short string that
  // explains the bucket pick ("critical + deploy 4m before",
  // "2.5x threshold").
  priority?: 'P1' | 'P2' | 'P3';
  priorityReason?: string;
  startedAt: number;
  resolvedAt?: number;
  // k8s/openshift clusters the firing service was active in
  // around the problem time — read-time enriched.
  clusters?: string[];
  // Most recent service.version deploy observed in the 30 min
  // before this problem opened, or undefined. Surfaced as a
  // "deployed v1.2 · 6m before" tag so operators see the
  // "regression coincides with deploy" pattern instantly.
  recentDeploy?: {
    version: string;
    timeUnixNs: number;
    ageSeconds: number;
  };
  // AI auto-explain summary (v0.5.254) — populated by the
  // background problemExplainer goroutine within ~30s of a critical
  // problem opening. Empty when Copilot isn't configured or the
  // problem hasn't been processed yet. The UI shows a small chip;
  // clicking it expands the full blurb inline.
  aiSummary?: string;
  aiSummaryAt?: number;
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

export type ChannelType = 'email' | 'slack' | 'mattermost' | 'teams' | 'zoomchat' | 'webhook' | 'whatsapp';

export interface NotificationChannel {
  id: string;
  name: string;
  type: ChannelType;
  // Routing predicates — empty / zero-value lists mean
  // "catch-all" (fire for every problem). Populated arrays
  // AND together; e.g. {services:["payments"],sreTeams:["platform"]}
  // = "fire only when the problem is on `payments` AND its
  // catalog SRE team is `platform`". Keeps the channel a
  // first-class routing target — different teams can each
  // wire their own Zoom Chat / email and only see their
  // services' alerts.
  matchRules?: {
    services?: string[];
    sreTeams?: string[];
    ownerTeams?: string[];
    clusters?: string[];
    quietHours?: string;    // "HH:MM-HH:MM"; window may cross midnight
    quietHoursTz?: string;  // IANA tz; empty = UTC
  };
  // Type-specific union. Optional fields keep the existing email/slack/
  // webhook callers happy; new channels (mattermost shares slack's
  // shape; whatsapp adds Twilio creds) only fill the fields they need.
  config: {
    recipients?: string[];   // email + whatsapp 'to' list
    webhookUrl?: string;     // slack / mattermost / teams (legacy zoomchat for migration only)
    url?: string;            // generic webhook
    verificationToken?: string; // legacy zoomchat (kept so old configs still serialise; new flow ignores)
    // Zoom Chat Server-to-Server OAuth fields.
    accountId?: string;      // zoomchat — Zoom account UUID
    clientId?: string;       // zoomchat — OAuth client id from the S2S app
    clientSecret?: string;   // zoomchat — OAuth client secret (write-only; never echoed back)
    channelId?: string;      // zoomchat — JID for the target chat channel
    toContact?: string;      // zoomchat — fallback DM contact email
    apiBaseUrl?: string;     // zoomchat — optional proxy host for api.zoom.us (chat messages)
    oauthBaseUrl?: string;   // zoomchat — optional proxy host for zoom.us (OAuth token)
    insecureSkipVerify?: boolean; // zoomchat — skip TLS cert verification (corp MITM proxies with private CA)
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
  // v0.6.39 — count of TraceCount trace_ids that still have raw
  // spans in the window. Lower than `traceCount` when some traces
  // have aged out of raw `spans` (30d TTL) but still live in
  // trace_summary_5m (90d). The aggregate row shows a chip to
  // make the disparity visible — clicking will drill to those
  // that ARE drillable, the rest only have aggregate stats.
  withRawAvailable: number;
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

export type PanelType = 'metric' | 'spanmetric' | 'stat' | 'gauge' | 'markdown' | 'row';
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
  // Visualization shape. Grafana-style: 'line' is the default,
  // 'bar' / 'stacked-bar' for discrete buckets (good for counts
  // per period), 'area' / 'stacked-area' for cumulative-style
  // breakdown (e.g. % of time spent per category). Stacked
  // variants only meaningful with a group-by.
  viz?: PanelVizType;
}
export type PanelVizType = 'line' | 'bar' | 'stacked-bar' | 'area' | 'stacked-area';
export interface StatPanelConfig {
  source: 'metric' | 'spanmetric';
  metric?: MetricPanelConfig;
  span?: SpanMetricPanelConfig;
  unit?: string;            // ms | % | rps | (free text)
  decimals?: number;
  // v0.5.486 — Grafana-style threshold colouring.
  //
  //   thresholds = [
  //     { value: 0,   color: 'green' },
  //     { value: 80,  color: 'amber' },
  //     { value: 95,  color: 'red'   },
  //   ]
  //
  // current value 92 → amber band (the highest threshold ≤ value
  // wins). When `colorMode` is 'value', the big number text picks
  // up the threshold colour; 'background' tints the whole panel
  // body; 'none' keeps the legacy delta-direction colour only.
  thresholds?: { value: number; color: 'green' | 'amber' | 'red' }[];
  colorMode?: 'none' | 'value' | 'background';
}
// v0.6.19 — Gauge panel. Grafana-parity semicircle dial with
// threshold zones painted along the arc. Best for "% of SLO
// budget consumed", "CPU utilisation", "queue depth vs cap" —
// any bounded number where the operator wants the at-a-glance
// "where am I in the safe / warning / breached bands".
//
// Same data-fetch as StatPanel (source = 'metric' | 'spanmetric'
// + the matching config); the only differences are: min/max
// bounds for the arc, an optional threshold list that paints
// coloured zones along the arc, and the visualisation itself.
export interface GaugePanelConfig {
  source: 'metric' | 'spanmetric';
  metric?: MetricPanelConfig;
  span?: SpanMetricPanelConfig;
  unit?: string;
  decimals?: number;
  min?: number;             // arc start value (default 0)
  max?: number;             // arc end value (default 100)
  // Same shape as StatPanelConfig.thresholds (v0.5.486); the
  // gauge paints each band as an arc segment so the operator
  // sees the green/amber/red zones directly.
  thresholds?: { value: number; color: 'green' | 'amber' | 'red' }[];
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
  // v0.6.20 — optional per-panel time-range override
  // (Grafana-parity). When set, this panel's data fetch ignores
  // the dashboard-level Topbar range and uses this preset
  // instead. Useful for "60-day baseline" tiles sitting next to
  // a "last 15min" incident chart on the same dashboard.
  // undefined / missing → fall back to the dashboard's range.
  rangeOverride?: TimeRange;
  config: MetricPanelConfig | SpanMetricPanelConfig | StatPanelConfig | GaugePanelConfig | MarkdownPanelConfig | RowPanelConfig;
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

// Mirrors profileconv.FrameKind on the backend. Used both for
// per-row badges in the hotspot tables and for the top-level
// breakdown bar (CPU vs Lock vs IO vs Sleep vs GC). Stays in
// sync with frontend/src/lib/flameHotspots.ts:classifyFrame
// and internal/profileconv/profileconv.go:ClassifyFrame.
export type ProfileFrameKind = 'cpu' | 'lock' | 'io' | 'sleep' | 'gc';

export interface ProfileCategoryBreakdown {
  cpu: number;
  lock: number;
  io: number;
  sleep: number;
  gc: number;
}

export interface ProfileDetail {
  meta: ProfileRow;
  flame: FlameNode;
  // Added v0.5.333 — leaf-time split by FrameKind, mirroring
  // Dynatrace's Suspension panel. Optional for forwards-compat
  // (the field is missing on responses from older backends).
  breakdown?: ProfileCategoryBreakdown;
}

// Service-level hotspot aggregation — N profiles in a window
// merged into one virtual flame tree, then rolled up by method.
// The shape mirrors the per-profile hotspots the frontend
// computes locally (flameHotspots.ts) so the same row component
// renders both.
export interface ProfileHotspotRow {
  name: string;
  file?: string;
  line?: number;
  self: number;
  total: number;
  paths: number;
  kind: ProfileFrameKind;
}

export interface ProfileHotspotsResponse {
  service: string;
  profileType: string;
  profilesUsed: number;
  profilesFailed: number;
  totalSamples: number;
  earliest: number; // unix ns; 0 when no profiles
  latest: number;
  hotspots: ProfileHotspotRow[];
  breakdown: ProfileCategoryBreakdown;
}

// Span-window-scoped hotspots — what the trace-detail panel
// asks for when an operator selects a span. Same row shape,
// smaller cap (top 10) since it lives in the side panel.
export interface SpanHotspotsResponse {
  profilesUsed: number;
  profilesFailed: number;
  totalSamples: number;
  hotspots: ProfileHotspotRow[];
  breakdown: ProfileCategoryBreakdown;
}
export type SortOrder = 'asc' | 'desc';

// EndpointRow — per (service, http.route|url.path) RED rollup
// Service attrs surface (v0.5.381). One row per (scope, key)
// combination the operator's SDK emits for a service, with
// occurrence count + sample values. Mirrors backend
// chstore.ServiceAttrRow.
export interface ServiceAttrRow {
  key: string;
  scope: 'span' | 'resource';
  occurrences: number;
  sampleValues: string[];
}

export interface ServiceAttrsResponse {
  service: string;
  attrs: ServiceAttrRow[] | null;
  from: number;
  to: number;
}

// surfaced on /endpoints. Mirrors the backend chstore.EndpointRow
// shape. Path falls back through the four OTel HTTP attribute
// candidates server-side; the row carries the resolved value so
// the UI doesn't repeat the priority logic.
export interface EndpointRow {
  service: string;
  path: string;
  method?: string;
  calls: number;
  errors: number;
  errorRate: number;
  avgMs: number;
  p99Ms: number;
  // v0.5.371 — 30-bucket call-rate sparkline across the
  // requested window. Same shape as OperationSummary.sparkline
  // and the spanmetrics sparkline — the operator learns the
  // mental model once and reads it across surfaces.
  sparkline?: number[];
  // v0.5.387 — companion sparklines aligned to the same 30
  // buckets as `sparkline`. Drives the per-row "✱" drill-in
  // modal that shows all three RED dimensions side-by-side
  // without a second round-trip. Each is 0-padded for buckets
  // that had no spans.
  errorsSparkline?: number[];
  p99Sparkline?: number[];
  // v0.5.403 — HTTP status class counts. Drives the "Status"
  // column on /endpoints so the operator reads 2xx/4xx/5xx
  // distribution without drilling into traces. Zero values
  // when http.status_code attr is missing (non-HTTP endpoints).
  http2xx?: number;
  http3xx?: number;
  http4xx?: number;
  http5xx?: number;
  // v0.5.404 — prior-window comparison values, populated when
  // the caller asked for trend deltas (compare=prior). Frontend
  // derives the % change + colour-coded arrow. Zero when the
  // (service, path) didn't exist in the prior window — UI
  // renders "NEW" instead of dividing by zero.
  priorCalls?: number;
  priorErrors?: number;
  priorAvgMs?: number;
  priorP99Ms?: number;
}

// Span-metrics-derived per-service RED rollup. Source: the
// spanmetrics processor (or compatible Grafana Alloy /
// otelcol pipeline) emits a calls counter + duration
// histogram; the backend aggregates per service_name within
// the window. Surfaced on /span-metrics so operators with a
// pre-existing metric pipeline don't need to wait for the
// span-derived MV.
export interface SpanMetricServiceRow {
  service: string;
  calls: number;
  errors: number;
  errorRate: number;
  avgMs?: number;
  maxMs?: number;
  // v0.5.358 — bucket-derived quantile estimates. The OTLP
  // ingest preserves the explicit bucket bounds + per-bucket
  // counts so the backend can sumForEach across data points
  // and interpolate. Empty when the histogram data point
  // didn't carry bucket arrays (rare; some SDKs send only
  // count/sum/max).
  p50Ms?: number;
  p99Ms?: number;
  // 30-bucket call-rate sparkline across the window. Used by
  // the Span Metrics table to render an inline mini-chart per
  // row so the operator sees the shape of traffic without
  // opening the full /metrics chart.
  sparkline?: number[];
  callsMetric?: string;
  durationMetric?: string;
}

export interface SpanMetricsServicesResponse {
  rows: SpanMetricServiceRow[] | null;
  callsMetric: string;
  durationMetric: string;
  // v0.5.355 — top-N cap surfaced so the UI can render a
  // "showing top N of M services" hint without re-querying
  // for the full count. truncated = the response hit the cap.
  top?: number;
  truncated?: boolean;
}

// One node in the multi-trace path-aggregated structure tree
// returned by GET /api/services/{name}/structure. Each node
// represents a unique `(parent_path → service → operation)` triple
// observed across the sampled traces; siblings repeating the exact
// same triple collapse into a single row carrying count + avg/max
// duration + error count.
// Generic series shape used by the /explore Data Explorer to
// render Line / Bar / Top-N / KPI from any of the three sources
// (spans / metrics / logs). Backends compute the buckets; the
// SPA only normalises into this shape.
export interface ExploreSeries {
  name: string;                          // legend label (group_value or _total)
  points: { t: number; v: number }[];    // unix ns × value
}

// SQL playground response shape.
export interface SQLResult {
  columns: string[];
  rows: unknown[][];
  rowCount: number;
  tookMs: number;
  error?: string;
}

export interface SchemaTable {
  table: string;
  engine: string;
  columns: { name: string; type: string }[];
}

// One curated runtime / process timeseries for the infra
// correlation panel on /service?name=…. Slot is the canonical
// SRE bucket ("cpu" | "memory" | "rps" | "runtime"); source is
// the raw OTel metric the server actually selected (e.g.
// jvm.cpu.recent_utilization for Java, process.runtime.cpu.
// utilization for Go).
export interface InfraMetricSeries {
  metric: string; // canonical slot
  source: string; // raw OTel metric name
  unit: string;
  points: { t: number; v: number }[];
}

// AnomalySilence mutes a single anomaly fingerprint until UntilAt.
// Driven by the Snooze buttons on /anomalies; queryable via the
// page header "X muted" indicator.
export interface AnomalySilence {
  id: string;
  fingerprint: string;
  kind: 'log_pattern' | 'trace_op';
  pattern: string;
  service: string;
  createdBy: string;
  createdAt: number;
  untilAt: number;
  reason: string;
  active: boolean;
}

// AuditEntry — append-only audit row consumed by /admin/audit.
export interface AuditEntry {
  id: string;
  time: number;
  actorId: string;
  actorEmail: string;
  actorRole: string;
  action: string;
  targetKind: string;
  targetId: string;
  ip: string;
  details: string;
}

// SavedView — per-user named filter combo for filter-heavy pages.
export interface SavedView {
  id: string;
  ownerId: string;
  name: string;
  page: string;          // "traces" | "logs" | "anomalies" | …
  queryString: string;
  pinned: boolean;
  createdAt: number;
}

// One row of the anomaly history — every log-pattern + trace-op
// detection the recorder has observed in the requested window.
// Status is derived in the backend query from last_seen freshness:
// "active" while still firing in the last 10 min, "cleared"
// otherwise. Lets the operator answer "did this fire today, even
// if it has stopped".
// v0.6.29 — Service dependency impact ("blast radius"). When an
// open Problem fires on service X, this surfaces the callers
// that depend on X — so the operator sees "this is local" vs
// "this is cascading up the call graph" at a glance.
export interface BlastRadiusCaller {
  service: string;
  calls: number;
  errors: number;
  rps: number;
  errorRate: number;
  hasOpenProblem: boolean;
}
export interface BlastRadius {
  service: string;
  windowSec: number;
  totalCallers: number;
  cascadingCallers: number;
  totalRps: number;
  totalErrorsPerSec: number;
  callers: BlastRadiusCaller[];
}

export interface AnomalyEvent {
  id: string;
  // v0.6.27 added `log_template_new` — Drain-discovered log shape
  // appearing for the first time in the lookback window.
  kind: 'log_pattern' | 'trace_op' | 'elastic_ml' | 'log_template_new';
  pattern: string;
  service: string;
  startedAt: number;     // unix ns — first observation
  lastSeen: number;      // unix ns — most recent observation
  peakRatio: number;
  currentRatio: number;
  currentCount: number;
  sample: string;
  status: 'active' | 'cleared';
  // k8s/openshift clusters where the anomaly's service was
  // active around the detection — read-time enriched.
  clusters?: string[];
  // v0.5.286 — most recent deploy of this service observed
  // in the 30 min preceding startedAt, or absent. Read-time
  // enriched from the v0.5.283 effective-version chain
  // (service.version → image.tag → Helm labels). The page
  // renders a "deployed v1.2.3 · 4m before" chip so the
  // operator can answer "is this a deploy-induced regression?"
  // without leaving /anomalies.
  recentDeploy?: {
    version: string;
    timeUnixNs: number;
    ageSeconds: number;
  };
}

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
// LogTemplate — Drain-3 extracted template persisted in
// chstore.log_templates (v0.5.244). first_seen is sticky on the
// CH upsert so the "what just started firing?" signal stays
// meaningful across restarts. Services is capped at 5 at the
// templater layer.
export interface LogTemplate {
  id: string;
  template: string;
  firstSeen: number;     // unix ns
  lastSeen: number;
  totalCount: number;
  services: string[];
  exceptionType?: string;
  sample: string;
}

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
  // v0.5.287 — per-service breakdown of current-window hits.
  // Top 5, count desc. LogPatternStrip renders this as a
  // rosette under the chip so operators see "fires on these
  // N services" without expanding.
  topServices?: { service: string; count: number }[];
  // v0.5.306 — lowercase body substrings the regex implies.
  // Used by /anomalies + /logs deep-links to build a precise
  // OR query that lands the operator on the actual matching
  // log lines (vs. v0.5.305 which only filtered by service).
  tokens?: string[];
}

// One entry in the service-level neighbours response — a single
// upstream caller or downstream callee of the inspected service.
export interface NeighborStat {
  service: string;
  traceCount: number;
  spanCount: number;
}

// Technology fingerprint of a service. Derived from OTel
// resource attributes on the latest span. Every field is
// optional — many SDKs only set a subset; the badge component
// renders whatever is non-empty.
export interface ServiceRuntime {
  service: string;
  language?: string;        // "go" / "java" / "dotnet" / "nodejs" / "python"
  sdkVersion?: string;
  runtimeName?: string;     // "OpenJDK Runtime Environment" / "go" / ".NET"
  runtimeVersion?: string;  // "21.0.1+12" / "go1.22.5" / "8.0.4"
  runtimeDesc?: string;
  host?: string;
  os?: string;
}

export interface ServiceMapNode {
  service: string;
  spanCount: number;
  errorRate: number;
  // Discriminator for synthesised infrastructure dep nodes.
  // "" / undefined = real OTel service emitting data; "db" =
  // database (subkind = redis / postgresql / oracle …);
  // "queue" = messaging system (subkind = kafka / rabbitmq …);
  // "external" = peer.service'd HTTP target outside the OTel mesh
  // (subkind = peer.service value). Frontend renders the two
  // shapes differently so an operator can tell at a glance
  // whether a node is "your code" or "your dependency".
  kind?: string;
  subkind?: string;
  // True when the diff endpoint reports this node didn't exist
  // in the baseline window (e.g. yesterday's same slot). Pulses
  // green in the graph + flagged "NEW" in the changes panel.
  isNew?: boolean;
  // k8s/openshift cluster this service ran in during the
  // sampled window. Read-time enriched server-side. Empty
  // for SDKs that don't ship cluster resource attrs;
  // "multi" when the service spans more than one cluster.
  cluster?: string;
}

export interface ServiceMapEdge {
  caller: string;
  callee: string;
  traceCount: number;
  spanCount: number;
  errorCount: number;
  isNew?: boolean;
}

export interface ServiceMap {
  nodes: ServiceMapNode[];
  edges: ServiceMapEdge[];
  // Populated only when ?diff=<duration> is requested. Lists the
  // nodes / edges present in the baseline window but missing
  // from the current one — surfaces silently-dropped
  // dependencies before they become an incident.
  removedNodes?: ServiceMapNode[];
  removedEdges?: ServiceMapEdge[];
  sampledFrom: number;
  totalSpans: number;
  // Echoed value of the ?diff param (e.g. "24h") so the UI can
  // label "vs yesterday" / "vs 1h ago" without the page tracking
  // it separately.
  baselineAgo?: string;
}

// CardinalityReport powers /admin/cardinality — answers "what
// is eating my ClickHouse?" Each row carries a bytes / row count
// figure so the operator can correlate the offender with the
// system.parts top-tables view.
export interface CardinalityTopRow {
  name: string;
  rows: number;
}
export interface CardinalityAttrKeyRow {
  key: string;
  distinctValues: number;
  occurrences: number;
  source: string;     // "spans" / "logs" / "metric_points"
}
export interface CardinalityColumnRow {
  table: string;
  column: string;
  compressedBytes: number;
  uncompressedBytes: number;
  compressionRatio: number;
}
export interface CardinalityReport {
  services: CardinalityTopRow[];
  metrics:  CardinalityTopRow[];
  attrKeys: CardinalityAttrKeyRow[];
  columns:  CardinalityColumnRow[];
  generatedAt: number;
}

export interface SamplingSettings {
  default: number;
  services: Record<string, number>;
  alwaysKeepErrors: boolean;
  alwaysKeepRoots: boolean;
  droppedSinceBoot: number;
  tail?: TailSamplingConfig;
  tailStats?: TailStats;
}

export interface TailSamplingConfig {
  enabled: boolean;
  windowSec: number;
  slowMs: number;
  maxTraces: number;
}

export interface TailStats {
  enabled: boolean;
  windowSec: number;
  slowMs: number;
  maxTraces: number;
  openTraces: number;
  flushedSpans: number;
  droppedSpans: number;
  evictedTraces: number;
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

// ServiceMetadata — operator-curated per-service catalog.
// Owner team / oncall / runbook / repo / description; joins
// on service name. Empty fields surface as "not yet curated"
// CTA on the UI rather than 404.
export interface CustomLink {
  label: string;
  url: string;
}

export interface ServiceMetadata {
  service: string;
  ownerTeam?: string;
  // SRE team — platform / reliability owners (often distinct
  // from the product owner team). Surfaces as a second chip
  // on the catalog pill so the oncall knows who to escalate
  // to for infra issues vs feature regressions.
  sreTeam?: string;
  description?: string;
  repository?: string;
  runbookUrl?: string;
  oncallUrl?: string;
  // chatChannel — Zoom Chat / Mattermost / Slack channel for
  // the team. Renamed from slackChannel; the backend back-
  // fills from the legacy column on read so existing curation
  // keeps showing.
  chatChannel?: string;
  // customLinks — operator-bolted-on per-service links
  // (Grafana board, Kibana saved search, Sensei, internal
  // SRE app, status page, etc.). Each renders as an
  // additional chip on the catalog pill.
  customLinks?: CustomLink[];
  updatedAt?: number;
}

// BubbleUp — Honeycomb-style attribute investigator. Compares
// a "selection" subset (e.g. slow / failing spans, a heatmap
// cell) against a "baseline" population and surfaces the
// attribute values over-represented in the selection.
// Score = selection_pct − baseline_pct (range −1..+1, sorted
// desc); positive = over-represented; the top row is the
// "smoking gun" attribute.
export interface BubbleUpValue {
  value: string;
  selectionCount: number;
  baselineCount: number;
  selectionPct: number;  // 0..1
  baselinePct: number;   // 0..1
  score: number;         // −1..+1
}
export interface BubbleUpAttribute {
  key: string;
  values: BubbleUpValue[];
}
export interface BubbleUpResult {
  selectionTotal: number;
  baselineTotal: number;
  attributes: BubbleUpAttribute[];
}

// LatencyHeatmap — Honeycomb-style 2D density grid.
// Counts[time_idx][dur_idx] is the span count in the cell
// formed by the time bucket and the (log-scale) duration bin.
// MaxCount lets the renderer pick a colour scale without a
// full re-scan.
export interface LatencyHeatmap {
  times: number[];          // unix nanoseconds, len = N time buckets
  durationBins: number[];   // upper bound in ms per bin, len = M
  counts: number[][];       // [N][M] grid
  maxCount: number;
  // Fraction of trace IDs the backend actually scanned to
  // produce this heatmap (v0.5.238). 1.0 = full pass; <1.0 =
  // hash-sampled to keep wide-window queries under the
  // 30s execution cap. UI surfaces a "sampled at 10%" tag
  // when this drops below 1 so the operator knows the
  // absolute counts are extrapolated.
  samplingRate?: number;
}

// Deploy — one observed (service, service.version) entry.
// Used to paint dashed vertical "deploy marker" lines on
// metric / latency / error charts so an operator can read at
// a glance whether a regression coincides with a deploy.
export interface Deploy {
  service: string;
  version: string;
  timeUnixNs: number;
  spanCount: number;
}

// DBQueryStat — one row in the database query analyzer panel.
// One per normalised DB statement seen on the service in the
// time window (literals replaced with "?" so a hot query
// doesn't appear as thousands of unique rows). SampleStatement
// keeps a real example so the operator sees what literals
// were involved without losing the aggregation.
export interface DBQueryStat {
  statement: string;
  sampleStatement: string;
  dbSystem: string;
  count: number;
  avgMs: number;
  p95Ms: number;
  p99Ms: number;
  maxMs: number;
  errorCount: number;
  totalMs: number;
}

// In-app AI chatbot (v0.6.53). Conversation is ephemeral — held in
// the CopilotChat component, sent whole to /api/copilot/chat each
// turn. The backend runs an agentic loop over the 7 MCP telemetry
// tools and streams progress via SSE.
//
// ChatMessage mirrors the Go copilot.ChatMessage wire shape: a user
// turn carries `text`; an assistant turn carries `text` and/or the
// tool calls it made (kept so the next request replays full context
// to the model). The UI only ever SENDS role+text (tool plumbing is
// server-internal) but the type allows the richer shape for replay.
export interface ChatMessage {
  role: 'user' | 'assistant';
  text?: string;
}

// One streamed event from the chat SSE. `step` = a tool the model
// called (render as a progress chip); `answer` = final prose;
// `error` = failure; `done` = stream closed.
export type ChatStreamEvent =
  | { kind: 'step'; tool: string; args: string }
  | { kind: 'answer'; text: string }
  | { kind: 'error'; error: string }
  | { kind: 'done'; ok: boolean };

// Deploy impact (v0.5.189) — before/after RED + signed deltas
// for one service.version transition. Powers the "Recent
// deploys" panel on the service detail page.
export interface DeployImpactStats {
  count: number;
  rps: number;
  errorRate: number;  // 0..1
  p99Ms: number;
  avgMs: number;
}
export interface DeployImpact {
  service: string;
  version: string;
  deployTimeNs: number;
  windowSec: number;
  before: DeployImpactStats;
  after: DeployImpactStats;
  p99DeltaPct: number;
  avgDeltaPct: number;
  errorRateDeltaPct: number;
}
export interface DeployHistoryRow {
  deploy: {
    service: string;
    version: string;
    timeUnixNs: number;
    spanCount: number;
  };
  impact: DeployImpact | null;
}

// SlowQueryRow — same as DBQueryStat plus the originating
// service. Drives the global slow-query catalog (v0.5.165) on
// /databases/slow-queries — operator-facing answer to "what
// query class is burning the most DB time across the whole
// install?".
export interface SlowQueryRow extends DBQueryStat {
  service: string;
}

// Exemplar — single representative span looked up to bridge a
// metric chart point to a sample trace (Datadog / Honeycomb /
// Grafana exemplar pattern). Returned by /api/spans/exemplar.
export interface SpanExemplar {
  traceId: string;
  spanId: string;
  service: string;
  name: string;
  durationNs: number;
  statusCode: string;
  timeUnixNs: number;
}
