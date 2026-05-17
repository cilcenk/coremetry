import type {
  Service, ServiceEdge, TracesResponse, TraceDetailResponse,
  LogsResponse, MetricInfo, MetricPoint, HealthInfo, SortColumn, SortOrder,
  ProfileRow, ProfileDetail, AggregateRow, SpanMetricSeries,
  AlertRule, Problem, ServiceEdgeStats, Exception,
  Dashboard, DashboardSummary, SLO, SLORow, SLOStatus,
  SMTPSettings, NotificationChannel,
  ExceptionGroup, ExceptionGroupState, ExceptionSample,
  SparklineBucket, OperationSummary,
  SystemStatus,
  Monitor, MonitorResult, MonitorRow,
  Incident, IncidentEvent,
  StatusPageConfig, StatusComponent, StatusSubscriber,
  RetentionSpec,
  AISettings, AISettingsInput,
  Role, LDAPConfig, LDAPDirectoryUser,
} from './types';

// Empty base = same origin (works in production where Go serves both UI and API).
// In dev, Next.js rewrites /api/* to http://localhost:8088 (see next.config.mjs).
const API_BASE = import.meta.env.VITE_API_BASE ?? '';

// Subclass so callers (and the AuthProvider) can detect "session expired"
// without string-matching error messages.
export class UnauthorizedError extends Error {
  constructor(msg = 'unauthorized') { super(msg); this.name = 'UnauthorizedError'; }
}

let onUnauthorized: (() => void) | null = null;
export function setUnauthorizedHandler(fn: (() => void) | null) {
  onUnauthorized = fn;
}

// Default per-request timeout. CH queries that hit the
// max_execution_time guard return an error quickly, but a hung
// upstream (network blip, CH cluster restart) would otherwise
// leave the fetch pending forever — the UI surfaces this as a
// spinner that never resolves. 60s is generous enough for the
// heaviest read on prod scale (raw spans scan with filters) and
// short enough that the operator gets a real error instead of
// a stuck page. Callers that need longer can pass their own
// signal via init.
const DEFAULT_REQUEST_TIMEOUT_MS = 60_000;

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let signal = init?.signal;
  let abortTimer: ReturnType<typeof setTimeout> | undefined;
  if (!signal) {
    const ctl = new AbortController();
    signal = ctl.signal;
    abortTimer = setTimeout(() => ctl.abort(), DEFAULT_REQUEST_TIMEOUT_MS);
  }
  try {
    const r = await fetch(API_BASE + path, { credentials: 'include', ...init, signal });
    if (r.status === 401) {
      onUnauthorized?.();
      throw new UnauthorizedError(await r.text().catch(() => 'unauthorized'));
    }
    if (!r.ok) throw new Error(`HTTP ${r.status}: ${await r.text()}`);
    // 204 / empty bodies → undefined
    const ct = r.headers.get('content-type') ?? '';
    if (!ct.includes('application/json')) return undefined as unknown as T;
    return await (r.json() as Promise<T>);
  } catch (err) {
    if ((err as Error)?.name === 'AbortError') {
      throw new Error(`Request timed out after ${DEFAULT_REQUEST_TIMEOUT_MS / 1000}s — try a narrower time range or fewer filters`);
    }
    throw err;
  } finally {
    if (abortTimer) clearTimeout(abortTimer);
  }
}

async function get<T>(path: string): Promise<T> { return request<T>(path); }

export interface RangeParams { from: number; to: number }

export const api = {
  // `name` is an optional case-insensitive substring filter applied
  // server-side BEFORE the limit clamp, so a service in the long
  // tail still surfaces when the user types it into the picker.
  // Backwards-compatible flat-array surface — unwraps the paged
  // response shape introduced for /services. Existing callers
  // (autocomplete pickers, slos, alerts, …) keep their array
  // contract.
  services: async (r: RangeParams, limit?: number, name?: string): Promise<Service[] | null> => {
    const resp = await get<{ services: Service[]; hasMore: boolean } | null>(
      `/api/services?${qs({ ...r, limit, name })}`);
    return resp ? resp.services : null;
  },
  // Page-aware variant — returns the full {services, hasMore,
  // offset, limit} payload so /services can drive prev/next.
  servicesPage: (r: RangeParams, opts: {
    limit?: number; offset?: number; name?: string;
    sort?: string; dir?: 'asc' | 'desc';
    // Catalog-driven team filters. Backend resolves the team
    // → service-name allowlist via the service_metadata
    // table; downstream spans query stays a microsecond
    // partition-pruned operation.
    ownerTeam?: string; sreTeam?: string;
    // Cluster filter (k8s.cluster.name / openshift.cluster.name
    // / cluster). Setting this forces the raw-span scan path
    // on the backend since the service MV doesn't carry the
    // cluster dim.
    cluster?: string;
  } = {}) =>
    get<{
      services: Service[];
      hasMore: boolean;
      offset: number;
      limit: number;
    }>(`/api/services?${qs({ ...r, ...opts })}`),

  // List distinct clusters seen in the window. Drives the
  // cluster-filter dropdown on /services and per-cluster
  // selector on the service detail page.
  clusters: (fromNs: number, toNs: number) =>
    get<{ clusters: string[] }>(`/api/clusters?from=${fromNs}&to=${toNs}`),
  // Per-cluster RED breakdown for one service. Used by the
  // Service detail page when traffic spans 2+ clusters.
  serviceClusters: (svc: string, fromNs: number, toNs: number) =>
    get<{ clusters: import('./types').ServiceClusterStat[] } | null>(
      `/api/services/${encodeURIComponent(svc)}/clusters?from=${fromNs}&to=${toNs}`),
  // Coremetry meta-observability snapshot — drives /admin/stats.
  systemStats: () =>
    get<import('./types').SystemStats>('/api/admin/system-stats'),

  // Cardinality / cost report — drives /admin/cardinality. 5-min
  // server cache so a refresh-spamming admin doesn't trigger the
  // attribute-key uniqExact scan repeatedly.
  cardinality: () =>
    get<import('./types').CardinalityReport>('/api/admin/cardinality'),

  // Multi-trace path-aggregated structure for a service. Returns a
  // tree of (service, operation, count, avgMs, maxMs, errorCount)
  // nodes — Grafana-Drilldown style. Each unique `(parent_path,
  // service, displayName)` triple appears once with `×N` for tight
  // loops / fan-outs.
  serviceStructure: (svc: string, since = '1h', samples = 50, internalOnly = false) =>
    get<{
      service: string;
      roots?: import('./types').AggSpanNode[];
      sampledFrom: number;
      totalSpans: number;
      internalOnly?: boolean;
    }>(`/api/services/${encodeURIComponent(svc)}/structure?since=${since}&samples=${samples}${internalOnly ? '&internal=true' : ''}`),

  // Service-level upstream / downstream neighbours derived from
  // sampled trace topology. No peer.service heuristic — purely
  // parent/child edge analysis. Pass refresh=true to bypass the
  // 1h cache when the operator knows the topology has shifted
  // (new service / pod / route just deployed).
  serviceNeighbors: (svc: string, since = '1h', samples = 50, refresh = false) =>
    get<{
      service: string;
      upstream?: import('./types').NeighborStat[];
      downstream?: import('./types').NeighborStat[];
      sampledFrom: number;
      totalSpans: number;
    }>(`/api/services/${encodeURIComponent(svc)}/neighbors?since=${since}&samples=${samples}${refresh ? '&refresh=1' : ''}`),

  // Curated runtime / process timeseries (cpu / memory / rps /
  // runtime) for the inspected service's pods. Powers the infra
  // correlation panel on /service?name=…. 30s server-side cache.
  serviceInfraMetrics: (svc: string, since = '15m') =>
    get<import('./types').InfraMetricSeries[]>(
      `/api/services/${encodeURIComponent(svc)}/infra?since=${since}`),

  // Technology fingerprint — language, SDK version, runtime
  // name + version, host, OS. Server-cached 5 min; UI shows a
  // small "Java OpenJDK 21" / "Go 1.22" badge above the infra
  // panel.
  serviceRuntime: (svc: string) =>
    get<import('./types').ServiceRuntime>(
      `/api/services/${encodeURIComponent(svc)}/runtime`),

  // Batch runtime fingerprints — { [serviceName]: ServiceRuntime }.
  // Single CH query (argMax) on the backend; replaces the
  // N-services × N-requests fan-out that a per-row badge on
  // the /services listing would otherwise trigger.
  allServiceRuntimes: () =>
    get<Record<string, import('./types').ServiceRuntime>>(
      '/api/services-runtimes'),

  // Global service-level topology graph — nodes + directed edges
  // derived from sampled recent traces. Powers the /service-map
  // page; 30s server-side cache.
  serviceMap: (since = '15m', samples = 200, diff?: string) => {
    // diff is an optional "compare-to" duration (e.g. "24h"). When
    // set, the backend returns the current topology with new /
    // removed nodes/edges flagged against that baseline window.
    const qs = `since=${since}&samples=${samples}` + (diff ? `&diff=${diff}` : '');
    return get<import('./types').ServiceMap>(`/api/service-map?${qs}`);
  },

  // Topology — operation-level BFS rooted at one service, depth-
  // bounded. Drives the /topology page; mirrors the backend at
  // internal/api/topology.go.
  topology: (params: { root: string; root_op?: string; depth?: number; from?: number; to?: number }) =>
    get<import('./types').TopologyResponse>(`/api/topology?${qs(params)}`),
  // Ops list for a given root service (powers the op picker on
  // the operation deep-dive view).
  topologyOps: (params: { service: string; from?: number; to?: number }) =>
    get<{ ops: string[] | null }>(`/api/topology/ops?${qs(params)}`),
  // Per-instance breakdown for one infra edge (v0.5.142). 60s
  // server cache; UI fetches lazily when the operator opens the
  // edge detail panel for a db/queue edge.
  topologyEdgeInstances: (params: { parent: string; system: string; kind: 'db' | 'queue'; from?: number; to?: number }) =>
    get<{ instances: Array<{ instance: string; calls: number; avgMs: number; p99Ms: number }> }>(
      `/api/topology/edge/instances?${qs(params)}`),
  topologyDrawIOURL: (params: { root: string; depth?: number; from?: number; to?: number }) =>
    `/api/topology/drawio?${qs(params)}`,
  // Service-level topology (v0.5.102) — full backend graph with
  // protocol/method labels + infra nodes. No depth bound; the
  // service fabric is generally small enough to draw whole.
  serviceTopology: (params: { from?: number; to?: number }) =>
    get<import('./types').ServiceTopologyResponse>(`/api/topology/service?${qs(params)}`),
  serviceTopologyDrawIOURL: (params: { from?: number; to?: number }) =>
    `/api/topology/service/drawio?${qs(params)}`,
  // Per-flow draw.io export (v0.5.145). Same XML shape as the
  // service-level export, restricted to the one flow's traces.
  flowTopologyDrawIOURL: (params: { root_service: string; root_op: string; from?: number; to?: number }) =>
    `/api/topology/flow/drawio?${qs(params)}`,
  // Root-anchored business flows (v0.5.103) — top entry points
  // by trace volume + the subgraph for one flow.
  topologyFlows: (params: { top?: number; from?: number; to?: number }) =>
    get<import('./types').FlowsResponse>(`/api/topology/flows?${qs(params)}`),
  topologyFlow: (params: { root_service: string; root_op: string; from?: number; to?: number }) =>
    get<import('./types').ServiceTopologyResponse>(`/api/topology/flow?${qs(params)}`),

  // Inbound-callers backtrace — Dynatrace-style consumer view.
  // Returns a row per (caller service × pod/instance × client IP ×
  // user-agent) with RED stats so the operator can pinpoint who
  // is driving load / errors. Range can be passed either as ?since
  // or as absolute from/to (ns).
  serviceBacktrace: (svc: string, opts: {
    since?: string;
    from?: number;
    to?: number;
    limit?: number;
  } = {}) => {
    const qs = new URLSearchParams();
    if (opts.since) qs.set('since', opts.since);
    if (opts.from)  qs.set('from', String(opts.from));
    if (opts.to)    qs.set('to', String(opts.to));
    if (opts.limit) qs.set('limit', String(opts.limit));
    return get<{
      service: string;
      callers?: import('./types').CallerRow[];
      from: number;
      to: number;
    }>(`/api/services/${encodeURIComponent(svc)}/backtrace?${qs.toString()}`);
  },

  // services: comma-separated allow-list — server caps to 200 to keep
  // the payload small even on 10k+ service installs.
  serviceSparklines: (r: RangeParams, services?: string[]) =>
    get<Record<string, SparklineBucket[]> | null>(
      `/api/services/sparklines?${qs({ ...r, services: services?.join(',') })}`),
  serviceNames: (q?: string, limit = 200, offset = 0) =>
    get<{ names: string[]; total: number; hasMore: boolean }>(
      `/api/service-names?${qs({ q, limit, offset })}`),
  // Operations picker counterpart (v0.5.180). Service filter
  // recommended at scale — a global op list across 10k services
  // is past the point of being useful in a dropdown.
  operationNames: (service?: string, q?: string, limit = 200, offset = 0) =>
    get<{ names: string[]; total: number; hasMore: boolean }>(
      `/api/operation-names?${qs({ service, q, limit, offset })}`),
  // Metric names with server-side search (v0.5.181). When q
  // or limit/offset is present, the response shape switches to
  // {names: MetricInfo[], total, hasMore} for the
  // MetricNamePicker. The legacy api.metricNames() (no extra
  // params) still returns the old MetricInfo[] shape.
  metricNamesSearch: (service: string, q?: string, limit = 200, offset = 0) =>
    get<{ names: MetricInfo[]; total: number; hasMore: boolean }>(
      `/api/metrics/names?${qs({ service, q, limit, offset })}`),
  // Distinct attribute keys observed on recent spans — drives the
  // FilterBuilder autocomplete so custom attrs (function_code etc.)
  // surface as suggestions in addition to the hardcoded list.
  attributeKeys: (since = '1h', limit = 500) =>
    get<{ scope: 'span' | 'resource'; key: string; count: number }[] | null>(
      `/api/attribute-keys?since=${since}&limit=${limit}`),
  // Top-N values observed for a single attribute key. Powers the
  // FilterBuilder value autocomplete; cached server-side 60s with
  // a Redis fast-path (so 100 SREs opening the picker run 1 CH
  // query, not 100).
  // Optional `q` for server-side substring search on the
  // value (v0.5.182). Without it the picker is stuck on the
  // top-200 by count; with it, an operator hunting a long-tail
  // value (specific http.url, db.statement fragment) can find
  // it without scrolling.
  attributeValues: (key: string, since = '1h', limit = 200, q?: string) =>
    get<{ value: string; count: number }[] | null>(
      `/api/attribute-values?${qs({ key, since, limit, q })}`),
  operations: (service: string, r: RangeParams) =>
    get<string[] | null>(`/api/operations?${qs({ ...r, service })}`),

  traces:    (params: TracesParams)  => get<TracesResponse>(`/api/traces?${qs(params)}`),
  tracesAggregate: (params: AggregateParams) =>
    get<AggregateRow[] | null>(`/api/traces/aggregate?${qs(params)}`),
  trace:     (id: string)            => get<TraceDetailResponse>(`/api/traces/${id}`),

  logs:      (params: LogsParams)    => get<LogsResponse>(`/api/logs?${qs(params)}`),

  metricNames: (service: string)     => get<MetricInfo[] | null>(`/api/metrics/names${service ? '?service=' + encodeURIComponent(service) : ''}`),
  metrics:     (params: MetricsParams) => get<MetricPoint[] | null>(`/api/metrics?${qs(params)}`),

  // Logs timeseries — Histogram aggregation routed through
  // whichever backend is configured (CH or external ES). Powers
  // the Logs source on /explore. 30s server-side cache.
  logsTimeseries: (params: {
    service?: string;
    search?: string;
    from?: number;
    to?: number;
    severity?: number;
    traceId?: string;
    bucketSec?: number;
    groupBy?: string;
  }) =>
    get<{ name: string; points: { t: number; v: number }[] }[]>(
      `/api/logs/timeseries?${qs(params)}`),

  health: ()                         => get<HealthInfo>(`/api/health`),
  status: ()                         => get<SystemStatus>(`/api/status`),
  // Build-tag — unauthenticated, so the login page can render it
  // before the operator has a session.
  version: ()                        => get<{ version: string }>(`/api/version`),

  // Log-pattern anomalies (ORA-, OOM, NPE, deadlock, panic, …) —
  // curated SRE-grade signal patterns that are either brand new
  // in the window or up 2x+ over baseline. Backed by a 60s cache
  // on the server side; no need to debounce client requests.
  logPatternAnomalies: () =>
    get<import('./types').LogPatternAnomaly[]>(`/api/anomalies/log-patterns`),
  traceOpAnomalies:    () =>
    get<import('./types').TraceOpAnomaly[]>(`/api/anomalies/trace-ops`),
  metricAnomalies:     () =>
    get<import('./types').Problem[]>(`/api/anomalies/metric`),
  // Persistent anomaly history — every log-pattern + trace-op
  // detection the recorder has observed in the requested window
  // (default 24h). Each row carries an "active" or "cleared"
  // status, so the operator can tell at a glance whether an
  // event is ongoing or has subsided. Backed by the
  // anomaly_events ReplacingMergeTree.
  anomalyEvents:       (since = '24h', limit = 200) =>
    get<import('./types').AnomalyEvent[]>(`/api/anomalies/events?since=${since}&limit=${limit}`),

  // Anomaly silences (mute / snooze).
  anomalySilences:     () =>
    get<import('./types').AnomalySilence[]>(`/api/anomalies/silences`),
  createAnomalySilence: (body: {
    fingerprint: string; kind: string; pattern: string;
    service: string; reason?: string; durationSec: number;
  }) =>
    request<import('./types').AnomalySilence>(`/api/anomalies/silences`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  deleteAnomalySilence: (id: string) =>
    request<void>(`/api/anomalies/silences/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  bulkDeleteAnomalySilences: (ids: string[]) =>
    request<{ deleted: number }>(`/api/anomalies/silences/bulk-delete`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ids }),
    }),

  // SQL playground — admin only, read-only by enforcement.
  sqlSchema: () =>
    get<import('./types').SchemaTable[]>(`/api/admin/sql/schema`),
  sqlQuery: (query: string) =>
    request<import('./types').SQLResult>(`/api/admin/sql/query`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ query }),
    }),
  // Elastic SQL (v0.5.138). Same response shape as sqlQuery so the
  // playground's table renderer is single-codepath. 400 with a
  // useful body when the logs backend isn't ES.
  elasticSqlQuery: (query: string, fetchSize = 1000) =>
    request<import('./types').SQLResult>(`/api/admin/sql/elastic`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ query, fetchSize }),
    }),

  // Audit log (admin-only read).
  auditLog: (since = '24h', filters: { actor?: string; action?: string; target?: string } = {}) =>
    get<import('./types').AuditEntry[]>(`/api/admin/audit?${qs({ since, ...filters })}`),
  // Alert-tuning noisy-rules report (v0.5.131). Cached server-
  // side 5 min so a burst of operators viewing it during morning
  // triage doesn't re-run the GROUP BY.
  alertTuningNoisyRules: (since = '24h', limit = 30) =>
    get<{
      rules: Array<import('./types').NoisyRule>;
      from: number; to: number; sinceSec: number;
    }>(`/api/admin/alert-tuning/noisy-rules?${qs({ since, limit })}`),
  // Logs field discovery (v0.5.136). Returns the searchable field
  // paths the configured logs backend knows about. Empty array
  // for ClickHouse (shape is fixed); ES backend returns the
  // mapping leaves. Server caches 60s.
  logsFields: () => get<{ fields: string[]; backend: string }>(
    `/api/logs/fields`),
  // Similar-traces lookup (v0.5.141). more_like_this on the
  // configured body field + trace.id terms aggregation. ES-only;
  // CH backend returns 400.
  logsSimilarTraces: (text: string, limit = 50) =>
    request<{ traces: Array<{ traceId: string; count: number }> }>(
      `/api/logs/similar`,
      {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ text, limit }),
      }),

  // Topology exclude list (v0.5.176) — services muted from
  // diagrams to keep hub-like infra (config server, identity,
  // service mesh) from cluttering every render.
  topologyExclude: () =>
    get<{ services: string[] }>(`/api/topology/exclude`),
  putTopologyExclude: (services: string[]) =>
    request<{ services: string[] }>(`/api/topology/exclude`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ services }),
    }),

  // Service dependency contracts (v0.5.191) — admin-only.
  listContracts: () =>
    get<import('./types').ServiceContract[]>(`/api/contracts`),
  upsertContract: (c: Partial<import('./types').ServiceContract>) =>
    request<import('./types').ServiceContract>(`/api/contracts`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(c),
    }),
  deleteContract: (id: string) =>
    request<void>(`/api/contracts/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  contractViolations: (windowMinutes = 30) =>
    get<import('./types').ContractViolation[]>(
      `/api/contracts/violations?${qs({ windowMinutes })}`),

  // Recent deploys + impact deltas for a service (v0.5.189).
  // One round-trip; backend computes before/after RED for each
  // deploy via partition-pruned spans queries.
  deployHistory: (service: string, limit = 5, windowSec = 600) =>
    get<import('./types').DeployHistoryRow[]>(
      `/api/services/${encodeURIComponent(service)}/deploy-history?${qs({ limit, windowSec })}`),

  // Slow-query AI explain (v0.5.171). Body matches the row
  // shape so the backend doesn't have to re-query CH — frontend
  // already has stats + sample on hand.
  copilotExplainSlowQuery: (body: {
    service: string; statement: string; sampleStatement: string;
    dbSystem: string; count: number;
    avgMs: number; p95Ms: number; p99Ms: number; maxMs: number;
    errorCount: number;
  }) =>
    request<{ explanation: string }>(`/api/copilot/explain-slow-query`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),

  // Global slow-query catalog (v0.5.165). One row per
  // (service, normalised statement) ordered by total wall-clock
  // time. Optional db_system narrows to one engine.
  slowQueries: (params: { from?: number; to?: number; db_system?: string; limit?: number }) =>
    get<import('./types').SlowQueryRow[]>(`/api/databases/slow-queries?${qs(params)}`),

  // AI observability (v0.5.163). Admin-only read endpoints.
  aiCalls: (params: { surface?: string; provider?: string; status?: string; from?: number; to?: number; limit?: number }) =>
    get<import('./types').AICall[]>(`/api/ai/calls?${qs(params)}`),
  aiCall: (id: string) =>
    get<import('./types').AICall>(`/api/ai/calls/${encodeURIComponent(id)}`),
  aiStats: (params: { from?: number; to?: number }) =>
    get<import('./types').AIStats>(`/api/ai/stats?${qs(params)}`),
  aiSeries: (params: { from?: number; to?: number }) =>
    get<import('./types').AICallsTimePoint[]>(`/api/ai/series?${qs(params)}`),
  aiRates: () =>
    get<Record<string, import('./types').AIRate>>(`/api/ai/rates`),
  putAIRates: (rates: Record<string, import('./types').AIRate>) =>
    request<Record<string, import('./types').AIRate>>(`/api/ai/rates`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(rates),
    }),

  // Saved views (per-user named filter combos).
  savedViews: (page: string) =>
    get<import('./types').SavedView[]>(`/api/views?page=${encodeURIComponent(page)}`),
  createSavedView: (body: {
    name: string; page: string; queryString: string;
    pinned?: boolean; shared?: boolean;
  }) =>
    request<import('./types').SavedView>(`/api/views`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  deleteSavedView: (id: string) =>
    request<void>(`/api/views/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  // Runtime settings: data retention
  getRetention: () => get<RetentionSpec>(`/api/settings/retention`),
  putRetention: (sp: RetentionSpec) =>
    request<RetentionSpec>(`/api/settings/retention`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(sp),
    }),

  // Runtime settings: anomaly auto-promotion thresholds.
  // Server-side defaults match the legacy v0.5.59 constants
  // so an install that never PUTs the endpoint keeps the
  // pre-tunable behaviour.
  getAnomalyPromotion: () =>
    get<{ enabled: boolean; minPeakRatio: number; minSustainedSec: number; minCount: number }>(
      `/api/settings/anomaly-promotion`),
  putAnomalyPromotion: (c: {
    enabled: boolean; minPeakRatio: number; minSustainedSec: number; minCount: number;
  }) =>
    request<typeof c>(`/api/settings/anomaly-promotion`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(c),
    }),

  // Runtime settings: AI Copilot
  // spanBreakdown — Elastic-APM-style "where does this service
  // spend its time?" stacked-area data. Per-bucket cumulative ms
  // grouped by span category (db / queue / http / kind).
  // /databases overview — one row per (db_system, instance) over
  // the window. Drives the Dynatrace-style Databases page.
  databases: (fromNs: number, toNs: number) =>
    get<import('./types').DBInstance[] | null>(
      `/api/databases?from=${fromNs}&to=${toNs}`),
  // /messaging overview — parallel shape for queues / topics
  // (Kafka / RabbitMQ / IBM MQ / NATS / etc.).
  messaging: (fromNs: number, toNs: number) =>
    get<import('./types').MessagingInstance[] | null>(
      `/api/messaging?from=${fromNs}&to=${toNs}`),
  // Detail drawers — per-(service, pod) caller breakdown + top
  // operations for one (system, instance) tuple. Drives the
  // row-click drawer on /databases and /messaging.
  databaseDetail: (system: string, instance: string, fromNs: number, toNs: number) =>
    get<import('./types').DBDetail | null>(
      `/api/databases/detail?system=${encodeURIComponent(system)}&instance=${encodeURIComponent(instance)}&from=${fromNs}&to=${toNs}`),
  // cluster is the bootstrap host / messaging.kafka.cluster.name
  // — defaults to "(default)" when the SPA doesn't supply one.
  // Multi-cluster Kafka / MQ deployments need it set so the
  // drawer scopes to the correct physical cluster.
  messagingDetail: (system: string, cluster: string, destination: string, fromNs: number, toNs: number) =>
    get<import('./types').MessagingDetail | null>(
      `/api/messaging/detail?system=${encodeURIComponent(system)}&cluster=${encodeURIComponent(cluster)}&destination=${encodeURIComponent(destination)}&from=${fromNs}&to=${toNs}`),
  // Oracle DB receiver drill-down — sessions, processes, cumulative
  // counter rates, tablespace usage. Backend falls back to
  // deterministic synthetic data when the oracledb receiver
  // isn't wired (Synthetic=true in the payload).
  oracleMetrics: (instance: string, fromNs: number, toNs: number) =>
    get<import('./types').OracleMetrics | null>(
      `/api/databases/oracle?instance=${encodeURIComponent(instance)}&from=${fromNs}&to=${toNs}`),
  postgresMetrics: (instance: string, fromNs: number, toNs: number) =>
    get<import('./types').PostgresMetrics | null>(
      `/api/databases/postgres?instance=${encodeURIComponent(instance)}&from=${fromNs}&to=${toNs}`),
  mysqlMetrics: (instance: string, fromNs: number, toNs: number) =>
    get<import('./types').MySQLMetrics | null>(
      `/api/databases/mysql?instance=${encodeURIComponent(instance)}&from=${fromNs}&to=${toNs}`),
  redisMetrics: (instance: string, fromNs: number, toNs: number) =>
    get<import('./types').RedisMetrics | null>(
      `/api/databases/redis?instance=${encodeURIComponent(instance)}&from=${fromNs}&to=${toNs}`),
  spanBreakdown: (service: string, fromNs: number, toNs: number) =>
    get<import('./types').BreakdownPoint[] | null>(
      `/api/services/${encodeURIComponent(service)}/span-breakdown?from=${fromNs}&to=${toNs}`),
  // spanFacets — Datadog-style trace tag explorer: top-N values per
  // well-known facet column over (window + DSL filter). Drives the
  // /explore facets panel; click a value adds it as a filter chip.
  // spanRepeats — N+1 / fan-out finder. Picks per-(trace, group-by)
  // count + filters HAVING count >= minRepeats. Drives the
  // Explore "Repeats" result mode.
  spanRepeats: (params: {
    from: number; to: number; dsl?: string; filters?: string;
    groupBy?: string[]; minRepeats?: number; limit?: number;
  }) => {
    const q = new URLSearchParams();
    q.set('from', String(params.from));
    q.set('to',   String(params.to));
    if (params.dsl) q.set('dsl', params.dsl);
    if (params.filters) q.set('filters', params.filters);
    if (params.groupBy && params.groupBy.length) q.set('groupBy', params.groupBy.join(','));
    if (params.minRepeats) q.set('minRepeats', String(params.minRepeats));
    if (params.limit) q.set('limit', String(params.limit));
    return get<import('./types').RepeatedSpanRow[] | null>(`/api/spans/repeats?${q}`);
  },
  spanFacets: (params: { from: number; to: number; dsl?: string; filters?: string; topValues?: number }) => {
    const q = new URLSearchParams();
    q.set('from', String(params.from));
    q.set('to', String(params.to));
    if (params.dsl) q.set('dsl', params.dsl);
    if (params.filters) q.set('filters', params.filters);
    if (params.topValues) q.set('topValues', String(params.topValues));
    return get<import('./types').Facet[] | null>(`/api/spans/facets?${q}`);
  },
  redisStats: () =>
    get<import('./types').RedisStats>(`/api/admin/redis-stats`),
  cacheStats: () =>
    get<import('./types').CacheStats>(`/api/admin/cache-stats`),
  // Causal correlations — ranked services that changed the most
  // around `atUnixNs`. Drives the "Why did this fire?" panel on
  // Problem rows. windowSec defaults to 10 min, baselineSec to
  // 4× window if not passed.
  correlations: (atUnixNs: number, windowSec?: number, baselineSec?: number) => {
    const qs = new URLSearchParams({ at: String(atUnixNs) });
    if (windowSec) qs.set('windowSec', String(windowSec));
    if (baselineSec) qs.set('baselineSec', String(baselineSec));
    return get<import('./types').ChangedService[] | null>(`/api/correlations?${qs}`);
  },
  // Branding overlay — public GET (login page reads pre-auth),
  // admin-only PUT. The save endpoint accepts up to 256 KB so a
  // pasted logo data URI fits.
  getBranding: () =>
    get<import('./branding').BrandingSettings>(`/api/branding`),
  putBranding: (b: import('./branding').BrandingSettings) =>
    request<import('./branding').BrandingSettings>('/api/branding', {
      method: 'PUT',
      body: JSON.stringify(b),
    }),
  getAISettings: () => get<AISettings>(`/api/settings/ai`),
  putAISettings: (s: AISettingsInput) =>
    request<AISettings>(`/api/settings/ai`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(s),
    }),

  // Runtime settings: LDAP / AD enterprise auth
  getLDAPSettings: () => get<LDAPConfig>(`/api/settings/ldap`),
  putLDAPSettings: (c: LDAPConfig) =>
    request<LDAPConfig>(`/api/settings/ldap`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(c),
    }),
  testLDAPConnection: (draft?: LDAPConfig) =>
    request<{ ok: boolean; error?: string }>(`/api/settings/ldap/test`, {
      method: 'POST',
      headers: draft ? { 'Content-Type': 'application/json' } : undefined,
      body: draft ? JSON.stringify(draft) : undefined,
    }),
  searchLDAPUsers: (q: string, limit = 25) =>
    get<{ users: LDAPDirectoryUser[] | null }>(`/api/settings/ldap/search?q=${encodeURIComponent(q)}&limit=${limit}`),
  provisionLDAPUser: (email: string, role: Role) =>
    request<AuthUser>(`/api/users/from-ldap`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email, role }),
    }),

  // Public trace snapshot — Grafana-style "share publicly" link.
  // POST mints a token; the GET form is on the public page (no
  // auth) so we don't need a client method for the read side.
  shareTrace: (id: string, ttlHours = 24) =>
    request<{ token: string; url: string; expiresAt: number }>(
      `/api/traces/${id}/share?ttlHours=${ttlHours}`,
      { method: 'POST' }),
  listTraceShares: (id: string) =>
    get<Array<{ token: string; traceId: string; createdBy: string; createdAt: number; expiresAt: number }> | null>(
      `/api/traces/${id}/shares`),
  revokeTraceShare: (token: string) =>
    request<{ status: string }>(`/api/traces/share/${token}`, { method: 'DELETE' }),

  // AI Copilot
  copilotConfig:         () => get<{ enabled: boolean }>(`/api/copilot/config`),
  copilotExplainTrace:   (id: string) =>
    request<{ explanation: string }>(`/api/copilot/explain-trace/${id}`, { method: 'POST' }),
  // Per-span explain (v0.5.144). Backend pulls target span +
  // parent + children + error siblings for a focused prompt.
  copilotExplainSpan:    (traceId: string, spanId: string) =>
    request<{ explanation: string }>(
      `/api/copilot/explain-span/${encodeURIComponent(traceId)}?span=${encodeURIComponent(spanId)}`,
      { method: 'POST' }),
  copilotExplainProblem: (id: string) =>
    request<{ explanation: string }>(`/api/copilot/explain-problem/${id}`, { method: 'POST' }),
  copilotExplainIncident: (id: string) =>
    request<{ explanation: string }>(`/api/copilot/explain-incident/${id}`, { method: 'POST' }),
  copilotExplainAnomaly: (id: string) =>
    request<{ explanation: string }>(`/api/copilot/explain-anomaly/${id}`, { method: 'POST' }),
  copilotExplainServiceHealth: (service: string, fromNs: number, toNs: number) =>
    request<{ explanation: string }>(
      `/api/copilot/explain-service?service=${encodeURIComponent(service)}&from=${fromNs}&to=${toNs}`,
      { method: 'POST' }),
  copilotRunbook: (id: string) =>
    request<{ explanation: string; similarCount: number }>(
      `/api/copilot/runbook/${id}`, { method: 'POST' }),
  copilotCompareTraces: (aId: string, bId: string) =>
    request<{ explanation: string }>(`/api/copilot/compare-traces`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ aId, bId }),
    }),
  acknowledgeProblems: (ids: string[]) =>
    request<{ acknowledged: number }>(`/api/problems/acknowledge`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ids }),
    }),
  copilotExplainSLO: (id: string) =>
    request<{
      explanation: string;
      status: import('./types').SLOStatus | null;
      fastBurn: number;
      slowBurn: number;
    }>(`/api/copilot/explain-slo/${id}`, { method: 'POST' }),
  copilotDeployImpact: (body: {
    service: string; version: string;
    deployTimeNs: number; windowSec?: number;
  }) =>
    request<{
      explanation: string;
      before: { count: number; rps: number; errorRate: number; p99Ms: number; avgMs: number };
      after:  { count: number; rps: number; errorRate: number; p99Ms: number; avgMs: number };
      newOps: string[];
    }>(`/api/copilot/deploy-impact`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  copilotSuggestServiceTags: (service: string) =>
    request<{
      suggestions: {
        ownerTeam?:   string;
        sreTeam?:     string;
        description?: string;
        criticality?: string;
        confidence?:  string;
        reasoning?:   string;
      } | null;
      raw?: string;
      note?: string;
    }>(`/api/copilot/suggest-service-tags?service=${encodeURIComponent(service)}`,
       { method: 'POST' }),

  // Public status page admin
  statusPageGetConfig:    () => get<StatusPageConfig>(`/api/status-page/config`),
  statusPagePutConfig:    (c: StatusPageConfig) =>
    request<StatusPageConfig>(`/api/status-page/config`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(c) }),
  statusPageListComponents: () => get<StatusComponent[] | null>(`/api/status-page/components`),
  statusPageCreateComponent: (c: Partial<StatusComponent>) =>
    request<StatusComponent>(`/api/status-page/components`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(c) }),
  statusPageUpdateComponent: (id: string, c: Partial<StatusComponent>) =>
    request<StatusComponent>(`/api/status-page/components/${id}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(c) }),
  statusPageDeleteComponent: (id: string) =>
    request<void>(`/api/status-page/components/${id}`, { method: 'DELETE' }),
  statusPageListSubscribers: () => get<StatusSubscriber[] | null>(`/api/status-page/subscribers`),
  statusPageDeleteSubscriber: (email: string) =>
    request<void>(`/api/status-page/subscribers?email=${encodeURIComponent(email)}`, { method: 'DELETE' }),

  // Incident management
  listIncidents:    (params?: { status?: string; service?: string; severity?: string; limit?: number }) =>
    get<Incident[] | null>(`/api/incidents?${qs(params ?? {})}`),
  getIncident:      (id: string) => get<Incident>(`/api/incidents/${id}`),
  createIncident:   (i: Partial<Incident>) =>
    request<Incident>(`/api/incidents`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(i) }),
  updateIncident:   (id: string, i: Partial<Incident>) =>
    request<Incident>(`/api/incidents/${id}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(i) }),
  ackIncident:      (id: string) =>
    request<Incident>(`/api/incidents/${id}/ack`, { method: 'POST' }),
  resolveIncident:  (id: string) =>
    request<Incident>(`/api/incidents/${id}/resolve`, { method: 'POST' }),
  addIncidentNote:  (id: string, text: string) =>
    request<void>(`/api/incidents/${id}/note`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ text }) }),
  incidentTimeline: (id: string) => get<IncidentEvent[] | null>(`/api/incidents/${id}/timeline`),
  incidentProblems: (id: string) => get<string[] | null>(`/api/incidents/${id}/problems`),

  // Synthetic monitoring
  listMonitors:    ()              => get<MonitorRow[] | null>(`/api/monitors`),
  getMonitor:      (id: string)    => get<Monitor>(`/api/monitors/${id}`),
  createMonitor:   (m: Partial<Monitor>) =>
    request<Monitor>(`/api/monitors`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(m),
    }),
  updateMonitor:   (id: string, m: Partial<Monitor>) =>
    request<Monitor>(`/api/monitors/${id}`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(m),
    }),
  deleteMonitor:   (id: string) =>
    request<void>(`/api/monitors/${id}`, { method: 'DELETE' }),
  monitorTimeline: (id: string, limit = 200) =>
    get<MonitorResult[] | null>(`/api/monitors/${id}/timeline?limit=${limit}`),

  spanMetric: (params: SpanMetricParams) =>
    get<SpanMetricSeries[] | null>(`/api/spans/metric?${qs(params)}`),

  // dashboardData — N panel requests in one HTTP round trip.
  // Server fans out to CH in parallel goroutines and returns
  // results keyed by request id. Each panel's underlying
  // store query still hits its own L1 + Redis cache so the
  // warm path is unchanged; the win is the network + the
  // server-side parallelism instead of N serial fetches
  // capped by the browser's concurrent-connection limit.
  dashboardData: (body: {
    from: number; to: number;
    requests: Array<{
      id: string; type: 'metric' | 'spanMetric';
      name?: string; service?: string;
      agg?: string; field?: string;
      groupBy?: string[]; step?: number;
      filters?: string; dsl?: string;
    }>;
  }) =>
    request<Record<string, { series?: SpanMetricSeries[] | null; error?: string }>>(
      `/api/dashboards/data`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          from: body.from,
          to: body.to,
          // server takes filters as raw JSON; if a request
          // already has a stringified JSON pass it through,
          // else send undefined.
          requests: body.requests.map(r => ({
            ...r,
            filters: r.filters ? JSON.parse(r.filters) : undefined,
          })),
        }),
      }),

  // spanMetricBatch — N aggregations over the SAME span
  // selection in one CH pass. Used by Service detail charts
  // (rate + error_rate + p99 share a WHERE) to drop cold-load
  // time from 3× to 1× a single-agg query. Returns a map
  // keyed by spec.name so callers address each series
  // without inspecting types.
  spanMetricBatch: (body: {
    from?: number; to?: number; step?: number;
    groupBy?: string[];
    filters?: string;
    dsl?: string;
    aggs: { name: string; agg: string; field?: string }[];
  }) =>
    request<Record<string, SpanMetricSeries[] | null>>('/api/spans/metric-batch', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        from:    body.from,
        to:      body.to,
        step:    body.step,
        groupBy: body.groupBy,
        // server expects filters as raw JSON; we pass through
        // the same shape the GET endpoint does.
        filters: body.filters ? JSON.parse(body.filters) : undefined,
        dsl:     body.dsl,
        aggs:    body.aggs,
      }),
    }),

  // 2D latency density grid. Same filter shape as spanMetric
  // — a heatmap toggle on /explore swaps between "line trend"
  // and "density" without re-typing the predicate.
  spanHeatmap: (params: {
    from?: number; to?: number; filters?: string; dsl?: string; buckets?: number;
  }) =>
    get<import('./types').LatencyHeatmap>(`/api/spans/heatmap?${qs(params)}`),

  // BubbleUp — attribute divergence between selection and
  // baseline. `filters`/`dsl` define the baseline population;
  // `selFilters`/`selDsl` narrow it to the selection subset.
  spanBubbleUp: (params: {
    from?: number; to?: number;
    filters?: string; dsl?: string;
    selFilters?: string; selDsl?: string;
  }) =>
    get<import('./types').BubbleUpResult>(`/api/spans/bubbleup?${qs(params)}`),

  // /api/services/{name}/db-queries — top normalised DB
  // statements for a service in a time window. Powers the
  // DB query analyzer panel on /service.
  serviceDBQueries: (svc: string, params: { from?: number; to?: number; limit?: number }) =>
    get<import('./types').DBQueryStat[] | null>(
      `/api/services/${encodeURIComponent(svc)}/db-queries?${qs(params)}`),

  // /api/services/{name}/deploys — first-seen timestamps for
  // every service.version emitted in the window. Drives the
  // dashed deploy-marker overlay on charts.
  serviceDeploys: (svc: string, params: { from?: number; to?: number }) =>
    get<import('./types').Deploy[] | null>(
      `/api/services/${encodeURIComponent(svc)}/deploys?${qs(params)}`),

  // Service catalog — per-service owner / oncall / runbook /
  // repo metadata. Empty rows return as `{ service }` only
  // (no special 404 path — the UI renders an "Add metadata"
  // CTA inline).
  serviceMetadata: (svc: string) =>
    get<import('./types').ServiceMetadata>(
      `/api/services/${encodeURIComponent(svc)}/metadata`),
  putServiceMetadata: (svc: string, m: import('./types').ServiceMetadata) =>
    request<void>(`/api/services/${encodeURIComponent(svc)}/metadata`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(m),
    }),
  servicesMetadata: () =>
    get<Record<string, import('./types').ServiceMetadata>>(
      `/api/services-metadata`),

  // Exemplar lookup — picks a representative trace for a metric
  // chart point. 404 means "no span matched the bucket" (the
  // user clicked outside the actual data window) — swallow it
  // and return null so the caller can show a neutral toast
  // without surfacing a scary HTTP error.
  spanExemplar: async (params: {
    service: string; op?: string; from: number; to: number;
    kind?: 'slow' | 'error' | 'any';
  }): Promise<import('./types').SpanExemplar | null> => {
    try {
      return await get<import('./types').SpanExemplar>(`/api/spans/exemplar?${qs(params)}`);
    } catch (e) {
      if (e instanceof Error && e.message.startsWith('HTTP 404')) return null;
      throw e;
    }
  },

  metricQuery: (params: MetricQueryParams) =>
    get<SpanMetricSeries[] | null>(`/api/metrics/query?${qs(params)}`),
  metricLabels: (metric: string, key: string, since = '24h') =>
    get<string[] | null>(`/api/metrics/labels?metric=${encodeURIComponent(metric)}&key=${encodeURIComponent(key)}&since=${since}`),

  profiles:        (params: ProfilesParams) => get<ProfileRow[] | null>(`/api/profiles?${qs(params)}`),
  profile:         (id: string)             => get<ProfileDetail>(`/api/profiles/${id}`),
  profilesForSpan: (service: string, startNs: number, endNs: number) =>
    get<ProfileRow[] | null>(`/api/profiles/by-span?service=${encodeURIComponent(service)}&start=${startNs}&end=${endNs}`),

  serviceOperations: (svc: string, r: RangeParams) =>
    get<OperationSummary[] | null>(`/api/services/${encodeURIComponent(svc)}/operations?${qs(r)}`),
  // serviceBundle — single round trip that returns the three
  // panels the Service detail mount needs (KPI summary,
  // recent problems, operations table). Server fans out to
  // CH in parallel goroutines; cached 15s. Replaces the
  // legacy three-call Promise.all on Service.tsx mount.
  serviceBundle: (svc: string, r: RangeParams) =>
    get<{
      service:    Service | null;
      problems:   import('./types').Problem[] | null;
      operations: OperationSummary[] | null;
      deploys:    import('./types').Deploy[] | null;
    }>(`/api/services/${encodeURIComponent(svc)}/bundle?${qs(r)}`),
  serviceCallers: (svc: string, since: string) =>
    get<ServiceEdgeStats[] | null>(`/api/services/${encodeURIComponent(svc)}/callers?since=${since}`),
  serviceCallees: (svc: string, since: string) =>
    get<ServiceEdgeStats[] | null>(`/api/services/${encodeURIComponent(svc)}/callees?since=${since}`),

  exceptions: (params: { service?: string; groupBy?: string; from?: number; to?: number; limit?: number }) =>
    get<Exception[] | null>(`/api/exceptions?${qs(params)}`),

  // Errors Inbox (state-tracked exception groups). v0.5.95 switched
  // the response shape from a bare array to { items, total, limit,
  // offset } so the UI can paginate without losing the global count.
  exceptionGroups: (params: { state?: string; service?: string; assignee?: string; limit?: number; offset?: number }) =>
    get<{ items: ExceptionGroup[]; total: number; limit: number; offset: number }>(`/api/exception-groups?${qs(params)}`),
  exceptionGroupSamples: (fingerprint: string, limit = 10) =>
    get<ExceptionSample[] | null>(`/api/exception-groups/${fingerprint}/samples?limit=${limit}`),
  setExceptionGroupState: (fingerprint: string, state: ExceptionGroupState) =>
    request<void>(`/api/exception-groups/${fingerprint}/state`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ state }),
    }),
  assignExceptionGroup: (fingerprint: string, assignee: string) =>
    request<void>(`/api/exception-groups/${fingerprint}/assign`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ assignee }),
    }),

  problems: (params: { status?: string; service?: string; severity?: string; limit?: number }) =>
    get<Problem[] | null>(`/api/problems?${qs(params)}`),

  // ── SLOs ─────────────────────────────────────────────────────────────────
  listSLOs: () => get<SLORow[] | null>('/api/slos'),
  getSLO:   (id: string) => get<SLO>(`/api/slos/${id}`),
  sloStatus: (id: string) => get<SLOStatus>(`/api/slos/${id}/status`),
  // Per-day burn-rate timeseries — drives the sparkline on
  // /slos (v0.5.150). 60s server cache.
  sloBurnSeries: (id: string, days = 7) =>
    get<{
      series: Array<{ time: number; total: number; good: number; burnRate: number }>;
      days: number;
    }>(`/api/slos/${id}/burn-series?days=${days}`),
  createSLO: (o: Omit<SLO, 'id' | 'createdAt'>) =>
    request<SLO>('/api/slos', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(o),
    }),
  deleteSLO: (id: string) =>
    request<void>(`/api/slos/${id}`, { method: 'DELETE' }),
  // Auto-create SLOs from 7d telemetry baseline (v0.5.147).
  // dryRun=true returns suggestions without writing; default
  // commits each non-skipped suggestion + audits the operation.
  autocreateSLOs: (dryRun: boolean) =>
    request<{
      suggestions: Array<{
        service: string;
        sliType: string;
        target: number;
        thresholdMs?: number;
        windowDays: number;
        baselineSli?: number;
        baselineMs?: number;
        reason: string;
        created: boolean;
        skipped?: string;
      }>;
      dryRun: boolean;
    }>(`/api/slos/autocreate${dryRun ? '?dry_run=1' : ''}`, { method: 'POST' }),

  // ── Dashboards ───────────────────────────────────────────────────────────
  listDashboards: () => get<DashboardSummary[] | null>('/api/dashboards'),
  getDashboard:   (id: string) => get<Dashboard>(`/api/dashboards/${id}`),
  createDashboard: (d: Omit<Dashboard, 'id' | 'createdAt' | 'updatedAt'>) =>
    request<Dashboard>('/api/dashboards', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(d),
    }),
  updateDashboard: (id: string, d: Omit<Dashboard, 'id' | 'createdAt' | 'updatedAt'>) =>
    request<Dashboard>(`/api/dashboards/${id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(d),
    }),
  deleteDashboard: (id: string) =>
    request<void>(`/api/dashboards/${id}`, { method: 'DELETE' }),

  alertRules: () => get<AlertRule[] | null>('/api/alert-rules'),
  alertBaseline: (params: { service?: string; metric: string; comparator?: string }) => {
    const qs = new URLSearchParams();
    if (params.service)    qs.set('service',    params.service);
    qs.set('metric', params.metric);
    if (params.comparator) qs.set('comparator', params.comparator);
    return get<{
      metric: string; service: string;
      p50: number; p95: number; p99: number;
      max: number; mean: number;
      sampleCount: number; windowSec: number;
      suggestedWarning: number; suggestedCritical: number;
    }>(`/api/alert-rules/baseline?${qs.toString()}`);
  },
  createAlertRule: (rule: Partial<AlertRule>) =>
    request<AlertRule>('/api/alert-rules', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(rule),
    }),
  updateAlertRule: (id: string, rule: Partial<AlertRule>) =>
    request<AlertRule>(`/api/alert-rules/${id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(rule),
    }),
  deleteAlertRule: (id: string) =>
    request<void>(`/api/alert-rules/${id}`, { method: 'DELETE' }),
  enableAlertRule: (id: string) =>
    request<void>(`/api/alert-rules/${id}/enable`, { method: 'POST' }),
  disableAlertRule: (id: string) =>
    request<void>(`/api/alert-rules/${id}/disable`, { method: 'POST' }),

  // ── Auth ─────────────────────────────────────────────────────────────────
  authConfig: () => get<AuthConfigResponse>('/api/auth/config'),
  login: (email: string, password: string) =>
    request<LoginResponse>('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email, password }),
    }),
  logout: () => request<void>('/api/auth/logout', { method: 'POST' }),
  me:     () => request<AuthUser>('/api/auth/me'),
  changeOwnPassword: (currentPassword: string, newPassword: string) =>
    request<void>('/api/auth/password', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ currentPassword, newPassword }),
    }),

  // ── Settings + notification channels (admin) ─────────────────────────────
  getSMTP:    () => get<SMTPSettings>('/api/settings/smtp'),
  putSMTP:    (s: SMTPSettings) =>
    request<SMTPSettings>('/api/settings/smtp', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(s),
    }),
  testSMTP:   (recipient: string) =>
    request<{ status: string }>('/api/settings/smtp/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ recipient }),
    }),
  getSampling: () => get<import('./types').SamplingSettings>('/api/settings/sampling'),
  putSampling: (s: Omit<import('./types').SamplingSettings, 'droppedSinceBoot'>) =>
    request<import('./types').SamplingSettings>('/api/settings/sampling', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(s),
    }),
  listChannels:  () => get<NotificationChannel[] | null>('/api/channels'),
  createChannel: (c: Omit<NotificationChannel, 'id' | 'createdAt'>) =>
    request<NotificationChannel>('/api/channels', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(c),
    }),
  updateChannel: (id: string, c: Omit<NotificationChannel, 'id' | 'createdAt'>) =>
    request<NotificationChannel>(`/api/channels/${id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(c),
    }),
  deleteChannel: (id: string) =>
    request<void>(`/api/channels/${id}`, { method: 'DELETE' }),
  testChannel:   (id: string) =>
    request<{ status: string }>(`/api/channels/${id}/test`, { method: 'POST' }),

  // ── User management (admin) ──────────────────────────────────────────────
  listUsers: () => get<UserRow[] | null>('/api/users'),
  // List active users whose team matches. Returns a slim
  // directory shape (no password hash, no auth provider) and
  // is open to any authenticated user (not admin-gated).
  // Used by the team chips on the Service detail page so an
  // operator can see who's on the owning team in a popover.
  usersByTeam: (team: string) =>
    get<{ id: string; email: string; role: string; team: string }[] | null>(
      `/api/users/by-team?team=${encodeURIComponent(team)}`),

  // Maintenance windows — admin-only CRUD. While active,
  // notifications matching (service, severity) are silenced;
  // problems still open + auto-resolve as usual so the
  // post-window timeline review is intact.
  listMaintenanceWindows: (includeDisabled = false) =>
    get<MaintenanceWindow[] | null>(
      `/api/maintenance-windows${includeDisabled ? '?all=1' : ''}`),
  createMaintenanceWindow: (body: {
    service: string; severity?: string;
    startAt: number; endAt: number; reason?: string;
  }) =>
    request<MaintenanceWindow>('/api/maintenance-windows', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  deleteMaintenanceWindow: (id: string) =>
    request<void>(`/api/maintenance-windows/${id}`, { method: 'DELETE' }),
  createUser: (email: string, password: string, role: Role, team?: string) =>
    request<AuthUser>('/api/users', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email, password, role, team: team ?? '' }),
    }),
  deleteUser: (id: string) =>
    request<void>(`/api/users/${id}`, { method: 'DELETE' }),
  // setUserRole flips a user to admin / editor / viewer.
  // Server refuses to demote the last admin so the system
  // can't lock itself out.
  setUserRole: (id: string, role: Role) =>
    request<AuthUser>(`/api/users/${id}/role`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ role }),
    }),
  // setUserTeam updates the team label. Empty string clears.
  setUserTeam: (id: string, team: string) =>
    request<{ team: string }>(`/api/users/${id}/team`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ team }),
    }),
  resetUserPassword: (id: string, password: string) =>
    request<void>(`/api/users/${id}/password`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password }),
    }),
};

export interface MaintenanceWindow {
  id: string;
  service: string;         // '*', exact name, or 'name*' prefix
  severity: string;        // '*' | 'info' | 'warning' | 'critical'
  startAt: number;         // unix ns
  endAt: number;           // unix ns
  reason: string;
  createdBy: string;
  createdAt: number;
  disabled: boolean;
}

export interface AuthUser { id: string; email: string; role: string; }
export interface UserRow extends AuthUser {
  disabled: boolean;
  authProvider: string;  // 'local' | 'oidc'
  team: string;          // free-text grouping label, '' when unassigned
  createdAt: number;     // unix ns
}
export interface LoginResponse {
  token: string;
  expiresAt: number;
  user: AuthUser;
}
export interface AuthConfigResponse {
  local: { enabled: boolean };
  oidc:  { enabled: boolean; displayName?: string };
  demo?: { enabled: boolean; email?: string; password?: string };
  ldap?: { enabled: boolean };
}

export interface MetricQueryParams {
  name: string;
  service?: string;
  filters?: string;     // JSON FilterExpr[]
  groupBy?: string;     // comma-sep
  agg?: string;         // avg | sum | min | max | last | p50 | p95 | p99
  from?: number;
  to?: number;
  step?: number;
}

export interface SpanMetricParams {
  agg: string;          // count | error_rate | p95 | …
  field?: string;       // duration_ms (default), or any attribute name
  groupBy?: string;     // comma-separated group keys
  filters?: string;     // JSON-encoded FilterExpr[]
  dsl?: string;         // multi-line DSL (AND-joined with `filters`)
  from?: number;
  to?: number;
  step?: number;        // bucket size in seconds (auto if omitted)
}

// Aggregation grouping dimensions accepted by /api/traces/aggregate.
// Mirrors the server-side whitelist; anything else is rejected.
export type AggregateGroup =
  | 'operation' | 'service' | 'kind' | 'status'
  | 'http_method' | 'http_route' | 'http_status'
  | 'host' | 'deploy_env' | 'scope';

export interface AggregateParams {
  groupBy?: AggregateGroup;
  // groupAttr overrides groupBy with a custom attribute key
  // (e.g. 'user.id', 'tenant', 'order.id'). Server sanitises.
  groupAttr?: string;
  service?: string;
  search?: string;
  hasError?: boolean;
  minMs?: number | string;
  maxMs?: number | string;
  from?: number;
  to?: number;
  filters?: string;     // JSON-encoded FilterExpr[]
  sort?: string;
  order?: SortOrder;
  limit?: number;
}

export interface ProfilesParams {
  service?: string;
  type?: string;
  from?: number;
  to?: number;
  limit?: number;
}

export interface TracesParams {
  service?: string;
  search?: string;
  traceId?: string;
  hasError?: boolean;
  // rootOnly hides traces whose root span never landed (only sub-
  // spans ingested) — drives the "Root traces" checkbox on /traces.
  rootOnly?: boolean;
  // requireServices: trace must contain spans from every listed
  // service. Lets the backtrace drill-in scope the trace list to
  // (caller × callee) co-occurrences instead of all traces emitted
  // by either side.
  services?: string[];
  minMs?: number | string;
  maxMs?: number | string;
  from?: number;
  to?: number;
  filters?: string;     // JSON-encoded FilterExpr[]
  dsl?: string;         // multi-line DSL (AND-joined with `filters`)
  sort?: SortColumn;
  order?: SortOrder;
  limit?: number;
  offset?: number;
  // count = "skip" (default — fast, no DISTINCT) | "approx" | "exact".
  // The UI defaults to "skip" and surfaces a "Show total" link for the
  // user to opt into the expensive count when they want it.
  count?: 'skip' | 'approx' | 'exact';
  // Comma-separated user-selected attribute keys whose values should
  // be projected into TraceRow.extras. Bounded to 8 cols server-side.
  extraAttrs?: string;
}

export interface LogsParams {
  service?: string;
  search?: string;
  severity?: number;
  traceId?: string;
  spanId?: string;
  from?: number;
  to?: number;
  limit?: number;
  offset?: number;
}

export interface MetricsParams {
  name: string;
  service?: string;
  from?: number;
  to?: number;
  limit?: number;
}

function qs(params: Record<string, unknown> | object): string {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '' || v === false) continue;
    u.set(k, String(v));
  }
  return u.toString();
}
