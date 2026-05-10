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

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const r = await fetch(API_BASE + path, { credentials: 'include', ...init });
  if (r.status === 401) {
    onUnauthorized?.();
    throw new UnauthorizedError(await r.text().catch(() => 'unauthorized'));
  }
  if (!r.ok) throw new Error(`HTTP ${r.status}: ${await r.text()}`);
  // 204 / empty bodies → undefined
  const ct = r.headers.get('content-type') ?? '';
  if (!ct.includes('application/json')) return undefined as unknown as T;
  return r.json() as Promise<T>;
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
  } = {}) =>
    get<{
      services: Service[];
      hasMore: boolean;
      offset: number;
      limit: number;
    }>(`/api/services?${qs({ ...r, ...opts })}`),
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
  serviceMap: (since = '15m', samples = 200) =>
    get<import('./types').ServiceMap>(
      `/api/service-map?since=${since}&samples=${samples}`),

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
  attributeValues: (key: string, since = '1h', limit = 200) =>
    get<{ value: string; count: number }[] | null>(
      `/api/attribute-values?key=${encodeURIComponent(key)}&since=${since}&limit=${limit}`),
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

  // SQL playground — admin only, read-only by enforcement.
  sqlSchema: () =>
    get<import('./types').SchemaTable[]>(`/api/admin/sql/schema`),
  sqlQuery: (query: string) =>
    request<import('./types').SQLResult>(`/api/admin/sql/query`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ query }),
    }),

  // Audit log (admin-only read).
  auditLog: (since = '24h', filters: { actor?: string; action?: string; target?: string } = {}) =>
    get<import('./types').AuditEntry[]>(`/api/admin/audit?${qs({ since, ...filters })}`),

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

  // Runtime settings: AI Copilot
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

  // AI Copilot
  copilotConfig:         () => get<{ enabled: boolean }>(`/api/copilot/config`),
  copilotExplainTrace:   (id: string) =>
    request<{ explanation: string }>(`/api/copilot/explain-trace/${id}`, { method: 'POST' }),
  copilotExplainProblem: (id: string) =>
    request<{ explanation: string }>(`/api/copilot/explain-problem/${id}`, { method: 'POST' }),

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
  serviceCallers: (svc: string, since: string) =>
    get<ServiceEdgeStats[] | null>(`/api/services/${encodeURIComponent(svc)}/callers?since=${since}`),
  serviceCallees: (svc: string, since: string) =>
    get<ServiceEdgeStats[] | null>(`/api/services/${encodeURIComponent(svc)}/callees?since=${since}`),

  exceptions: (params: { service?: string; groupBy?: string; from?: number; to?: number; limit?: number }) =>
    get<Exception[] | null>(`/api/exceptions?${qs(params)}`),

  // Errors Inbox (state-tracked exception groups)
  exceptionGroups: (params: { state?: string; service?: string; assignee?: string; limit?: number }) =>
    get<ExceptionGroup[] | null>(`/api/exception-groups?${qs(params)}`),
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
  createSLO: (o: Omit<SLO, 'id' | 'createdAt'>) =>
    request<SLO>('/api/slos', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(o),
    }),
  deleteSLO: (id: string) =>
    request<void>(`/api/slos/${id}`, { method: 'DELETE' }),

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
  createUser: (email: string, password: string, role: Role) =>
    request<AuthUser>('/api/users', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email, password, role }),
    }),
  deleteUser: (id: string) =>
    request<void>(`/api/users/${id}`, { method: 'DELETE' }),
  resetUserPassword: (id: string, password: string) =>
    request<void>(`/api/users/${id}/password`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password }),
    }),
};

export interface AuthUser { id: string; email: string; role: string; }
export interface UserRow extends AuthUser {
  disabled: boolean;
  authProvider: string;  // 'local' | 'oidc'
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
