import type {
  PurgeResult,
  Service, ServiceEdge, TracesResponse, TracesExtrasResponse, TraceDetailResponse,
  LogsResponse, LogFieldStats, NotificationLogEntry, MetricInfo, MetricPoint, HealthInfo, SortColumn, SortOrder,
  ProfileRow, ProfileDetail, ProfileHotspotsResponse, SpanHotspotsResponse, AggregateRow, SpanMetricSeries, SpanMetricResult, HistogramResult,
  MetricResolveResult,
  SpanMetricsServicesResponse, EndpointRow, EndpointDetail, EndpointSplitResponse, EndpointDownstream, ServiceAttrsResponse,
  AlertRule, Problem, EvaluatorHealth, WatcherImportResult, WatcherSummaryEntry, WatcherHistory,
  Runbook, RunbookExecution,
  Dashboard, DashboardSummary, SLO, SLORow, SLOStatus,
  SMTPSettings, NotificationChannel,
  ExceptionGroup, ExceptionGroupState, ExceptionSample, OccurrencePoint,
  SparklineBucket, OperationSummary,
  SystemStatus,
  Monitor, MonitorResult, MonitorRow,
  Incident, IncidentEvent,
  StatusPageConfig, StatusComponent, StatusSubscriber,
  RetentionSpec,
  AISettings, AISettingsInput,
  TempoSnapshot, TempoSettingsInput,
  ThanosSnapshot, ThanosSettingsInput, ClusterPodsResponse, ClusterPodDetail,
  ClusterNodesResponse, ClusterSummary, ClusterNamespacesResponse,
  ClusterPodsTrendResponse, ClusterNetworkTrendResponse, ClusterDeploymentsResponse, ClusterResourceTrendResponse, ClusterAlertsResponse, ClusterDeployTrendResponse, ClusterJMXTrendResponse, ClusterJMXMetricsResponse,
  KibanaSettings,
  Role, LDAPConfig, LDAPDirectoryUser,
  FilterExpr,
  ESQueryError, ESLogstoreSnapshot, ESLogstoreInput,
  OtlpExemplar, TraceLinks, TraceCountResponse, CorrelationLinkSettings,
  ExceptionTriageConfig, MetricExclusions, AnomalyTrackedConfig } from './types';
import { encodeMetricQuery, type MetricQuery } from './metricQuery';
// GoDuration — every `since` below is forwarded to Go's time.ParseDuration,
// which has no day unit; see the type's comment in utils.ts.
import type { GoDuration } from './utils';

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

// v0.8.413 — heavy, deliberate admin actions (telemetry purge) may
// legitimately outlive the 60s default; callers pass timeoutMs to
// keep the connection open while the server works.
async function request<T>(path: string, init?: RequestInit & { timeoutMs?: number }): Promise<T> {
  const timeoutMs = init?.timeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS;
  // v0.9.603 (traces D2) — çağıranın signal'i ile timeout BİRLİKTE çalışır.
  //
  // Öncesi: `if (!signal)` — çağıran bir signal verdiği anda 60s timeout
  // KAYBOLUYORDU, yani iptal edilebilirlik kazanmanın bedeli asılı kalma
  // riskiydi. İkisi birbirinin alternatifi değil; biri kullanıcı niyeti,
  // öteki emniyet tavanı.
  //
  // AbortSignal.any() kullanılmadı: tarayıcı desteği yeni (Safari 17.4+)
  // ve bu depo eski istemcileri de destekliyor. Dinleyici deseni her
  // yerde çalışır.
  const ctl = new AbortController();
  let timedOut = false;
  const abortTimer = setTimeout(() => { timedOut = true; ctl.abort(); }, timeoutMs);
  const caller = init?.signal;
  const onCallerAbort = () => ctl.abort();
  if (caller) {
    if (caller.aborted) ctl.abort();
    else caller.addEventListener('abort', onCallerAbort, { once: true });
  }
  const signal = ctl.signal;
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
      // İPTAL ile ZAMAN AŞIMI ayrı şeyler ve ayrı davranmalı.
      //
      // Öncesi ikisi de "Request timed out" oluyordu: operatör aralığı
      // değiştirdiğinde eski istek iptal edilse bile ekrana zaman aşımı
      // hatası düşerdi. Kullanıcının kendi eylemi hata gibi görünmemeli.
      if (!timedOut) throw new CanceledError();
      throw new Error(`Request timed out after ${timeoutMs / 1000}s — try a narrower time range or fewer filters`);
    }
    throw err;
  } finally {
    clearTimeout(abortTimer);
    caller?.removeEventListener('abort', onCallerAbort);
  }
}

/** v0.9.603 — çağıran isteği bilerek iptal etti. Hata DEĞİL: çağıranlar
 *  bunu sessizce yutmalı, yoksa operatörün kendi eylemi ekranda hata
 *  olarak görünür. `isCanceled` ile ayırt edilir. */
export class CanceledError extends Error {
  constructor() { super('request canceled'); this.name = 'CanceledError'; }
}

/** Bir hatanın "çağıran iptal etti" olup olmadığını söyler. */
export function isCanceled(e: unknown): boolean {
  return e instanceof CanceledError || (e as Error)?.name === 'CanceledError';
}

async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  return request<T>(path, signal ? { signal } : undefined);
}

export interface RangeParams { from: number; to: number }

export const api = {
  // `name` is an optional case-insensitive substring filter applied
  // server-side BEFORE the limit clamp, so a service in the long
  // tail still surfaces when the user types it into the picker.
  // Backwards-compatible flat-array surface — unwraps the paged
  // response shape introduced for /services. Existing callers
  // (autocomplete pickers, slos, alerts, …) keep their array
  // contract.
  services: async (r: RangeParams, limit?: number, name?: string, signal?: AbortSignal): Promise<Service[] | null> => {
    const resp = await get<{ services: Service[]; hasMore: boolean } | null>(
      `/api/services?${qs({ ...r, limit, name })}`, signal);
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
    // Global env filter (spans.deploy_env — the Topbar picker,
    // v0.8.385). Same raw-fallback semantics as cluster, but the
    // conjunct is a typed LowCardinality column (cheaper).
    env?: string;
    // Namespace filter (v0.9.189) — derived service namespace
    // (service.namespace / k8s.namespace.name via service_metadata).
    // Resolved server-side into the service-name allowlist like the
    // team filters, so it keeps the MV fast path (unlike cluster/env).
    namespace?: string;
    // v0.7.44 — opt-in distinct-service total for the First/Last pager.
    // Default off keeps the hot path count-free.
    withTotal?: '1';
    // v0.9.345 — display filters, now SERVER-side (HAVING on the grouped
    // aggregates). They used to run in the browser over the 50 rows of the
    // current page, so "Errors only" could empty page 1 while erroring
    // services sat on page 7. Both the MV and raw paths apply them, from one
    // shared predicate builder, so switching paths (by picking a cluster or
    // env) cannot change what they mean.
    errorsOnly?: '1';
    minSpans?: number;
    minP99?: number;
  } = {}) =>
    get<{
      services: Service[];
      hasMore: boolean;
      offset: number;
      limit: number;
      total?: number; // present only when withTotal='1' (MV path)
    }>(`/api/services?${qs({ ...r, ...opts })}`),

  // List distinct clusters seen in the window. Drives the
  // cluster-filter dropdown on /services and per-cluster
  // selector on the service detail page.
  clusters: (fromNs: number, toNs: number) =>
    get<{ clusters: string[] }>(`/api/clusters?from=${fromNs}&to=${toNs}`),
  // Distinct derived namespaces (service_metadata.Namespace) — options
  // for the /services namespace filter (v0.9.189). Catalog-sourced,
  // no span scan; 5-min cached server-side.
  namespaces: (fromNs: number, toNs: number) =>
    get<{ namespaces: string[] }>(`/api/namespaces?from=${fromNs}&to=${toNs}`),
  // Distinct deployment environments (spans.deploy_env) — options for
  // the global Topbar env picker (v0.8.383). Deliberately param-less:
  // the server defaults to a 24h window and clamps the enumeration
  // scan to the most recent hour anyway (env sets are deploy-stable),
  // so every caller shares ONE bounded cache rung.
  // v0.8.389 — optional substring search (?q=) + total for honest
  // truncation labelling; the list is count-ordered server-side.
  environments: (q?: string) =>
    get<{ environments: string[]; total?: number }>(
      `/api/environments${q ? `?q=${encodeURIComponent(q)}` : ''}`),
  // Per-service env list — the Envs chip group on the Service detail
  // header (v0.8.383, the operator's "same mobile-bff in int/uat/prep"
  // case).
  serviceEnvironments: (svc: string, fromNs: number, toNs: number) =>
    get<{ environments: string[] } | null>(
      `/api/services/${encodeURIComponent(svc)}/environments?from=${fromNs}&to=${toNs}`),
  // Per-cluster RED breakdown for one service. Used by the
  // Service detail page when traffic spans 2+ clusters.
  serviceClusters: (svc: string, fromNs: number, toNs: number) =>
    get<{ clusters: import('./types').ServiceClusterStat[] } | null>(
      `/api/services/${encodeURIComponent(svc)}/clusters?from=${fromNs}&to=${toNs}`),
  // Servis throughput'u METRİKTEN (v0.9.665). `metric` boş bırakılırsa
  // ayardaki ad kullanılıyor; operatör doğru adı ararken her denemede
  // ayar kaydetmek zorunda kalmasın diye sorgudan da geçilebiliyor.
  serviceMetricThroughput: (svc: string, fromNs: number, toNs: number, metric?: string, mdp?: number, opts?: { breakdown?: 'route'; rateWindow?: number }) =>
    get<import('./types').ServiceMetricThroughput>(
      `/api/services/${encodeURIComponent(svc)}/metric-throughput?from=${fromNs}&to=${toNs}`
      + (metric ? `&metric=${encodeURIComponent(metric)}` : '')
      // v0.9.706 — nokta bütçesi (parite px pilotu). panelMaxDataPoints
      // kuantalı üretir → sunucu cache anahtarı sınırlı kardinalitede.
      + (mdp ? `&maxDataPoints=${mdp}` : '')
      // v0.9.718 — route kırılımı + PromQL-eşdeğeri rate penceresi.
      + (opts?.breakdown ? `&breakdown=${opts.breakdown}` : '')
      + (opts?.rateWindow ? `&rateWindow=${opts.rateWindow}` : '')),
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
  serviceStructure: (svc: string, since: GoDuration = '1h', samples = 50, internalOnly = false) =>
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
  serviceNeighbors: (svc: string, since: GoDuration = '1h', samples = 50, refresh = false) =>
    get<{
      service: string;
      upstream?: import('./types').NeighborStat[];
      downstream?: import('./types').NeighborStat[];
      sampledFrom: number;
      totalSpans: number;
    }>(`/api/services/${encodeURIComponent(svc)}/neighbors?since=${since}&samples=${samples}${refresh ? '&refresh=1' : ''}`),

  // v0.6.29 — Blast radius for an open Problem. Returns upstream
  // callers + their RPS + cascade-flag (caller has own open
  // problem). Sorted cascading-first, then by calls desc.
  serviceBlastRadius: (svc: string, since: GoDuration = '1h') =>
    get<import('./types').BlastRadius>(
      `/api/services/${encodeURIComponent(svc)}/blast-radius?since=${since}`),

  // Curated runtime / process timeseries (cpu / memory / rps /
  // runtime) for the inspected service's pods. Powers the infra
  // correlation panel on /service?name=…. 30s server-side cache.
  serviceInfraMetrics: (svc: string, since: GoDuration = '15m') =>
    get<import('./types').InfraMetricSeries[]>(
      `/api/services/${encodeURIComponent(svc)}/infra?since=${since}`),

  // Per-pod CPU/memory rows for the Overview "Instances" card — one row
  // per host_name emitting metrics for the service. 30s server-side cache.
  serviceInstances: (svc: string, since: GoDuration = '15m') =>
    get<import('./types').ServiceInstance[] | null>(
      `/api/services/${encodeURIComponent(svc)}/instances?since=${since}`),

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
  serviceMap: (since: GoDuration = '15m', samples = 200, diff?: string, topN = 0, signal?: AbortSignal) => {
    // diff is an optional "compare-to" duration (e.g. "24h"). When
    // set, the backend returns the current topology with new /
    // removed nodes/edges flagged against that baseline window.
    // topN > 0 caps the graph to the heaviest N services (overview mode);
    // 0 = the full sampled graph (default).
    const qs = `since=${since}&samples=${samples}`
      + (diff ? `&diff=${diff}` : '')
      + (topN > 0 ? `&topN=${topN}` : '');
    return get<import('./types').ServiceMap>(`/api/service-map?${qs}`, signal);
  },

  // Topology — operation-level BFS rooted at one service, depth-
  // bounded. Drives the /topology page; mirrors the backend at
  // internal/api/topology.go.
  topology: (params: { root: string; root_op?: string; depth?: number; from?: number; to?: number }) =>
    get<import('./types').TopologyResponse>(`/api/topology?${qs(params)}`),

  // v0.8.10 — OTel-native service graph (topology rebuild). Compact
  // {nodes,edges} from the topology_edges_5m MV; one endpoint serves both the
  // global map (scope=global) and a service neighborhood (focus + scope).
  // hops (v0.8.294, neighborhood only, server-clamped 1..3) walks callers/
  // dependencies server-side so clients stop downloading the global graph.
  // topN (v0.8.295, global only): render budget — the server clamps
  // absent/0/>500 to 500 and reports totalNodes/shownNodes.
  serviceGraph: (params: { focus?: string; scope?: 'neighborhood' | 'global'; hops?: number; topN?: number; from?: number; to?: number }) =>
    get<import('./types').ServiceGraphResponse>(`/api/servicegraph?${qs(params)}`),
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
  // v0.5.310 — `noise` param: 'show' disables the backend noise
  // filter (self-edges, infra ops, sub-0.5% volume) and returns
  // the legacy full graph. Default = filtered.
  serviceTopology: (params: { from?: number; to?: number; noise?: 'show'; compare?: 'prior'; top?: number; focus?: string; hops?: number; broadcast?: 'show' }) =>
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
  // v0.9.638 — /traces "Toplamı göster" sayısı. AYRI endpoint, çünkü
  // ?count=exact liste isteğine binince countModeAllowsMV'yi kapatıp
  // listeyi ham spans yoluna düşürüyordu (çift ceza). Ayrı istek =
  // liste SQL'i bayt bayt aynı = liste MV'de kalıyor.
  //
  // reason dolu ise SAYI YOK: bazı şekiller MV'de ucuza sayılamıyor ve
  // pahalı bir sayı dürüst bir retten kötüdür.
  // v0.9.657 — dış log köprüsü şablonları (admin). Backend doğrulamayı
  // yapıyor: http(s) + {value} şartı ve hangi ortamın hatalı olduğu 400
  // gövdesinde döner.
  getCorrelationLink: () =>
    get<CorrelationLinkSettings>('/api/settings/correlation-link'),
  putCorrelationLink: (templates: Record<string, string>) =>
    request<{ templates: Record<string, string> }>('/api/settings/correlation-link', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ templates }),
    }),

  tracesCount: (params: TracesParams, signal?: AbortSignal) =>
    get<TraceCountResponse>(`/api/traces/count?${qs(params)}`, signal),

  attributeKeys: (since: GoDuration = '1h', limit = 500, filters?: string, filterGroup?: string) => {
    // v0.5.261 — optional filter context. When the operator has
    // active filters in /explore, pass them through so the
    // suggester returns attribute keys with data UNDER those
    // filters (not the global top-N). Empty / undefined keeps
    // the old global-scan behaviour.
    // v0.8.x gap-2 — filterGroup (grouped AND/OR) supersedes `filters`
    // server-side when present; additive, default-off.
    const qsParts = [`since=${since}`, `limit=${limit}`];
    if (filterGroup) qsParts.push(`filterGroup=${encodeURIComponent(filterGroup)}`);
    else if (filters && filters !== '[]') qsParts.push(`filters=${encodeURIComponent(filters)}`);
    return get<{ scope: 'span' | 'resource'; key: string; count: number }[] | null>(
      `/api/attribute-keys?${qsParts.join('&')}`);
  },
  // Top-N values observed for a single attribute key. Powers the
  // FilterBuilder value autocomplete; cached server-side 60s with
  // a Redis fast-path (so 100 SREs opening the picker run 1 CH
  // query, not 100).
  // Optional `q` for server-side substring search on the
  // value (v0.5.182). Without it the picker is stuck on the
  // top-200 by count; with it, an operator hunting a long-tail
  // value (specific http.url, db.statement fragment) can find
  // it without scrolling.
  attributeValues: (key: string, since: GoDuration = '1h', limit = 200, q?: string, range?: { from: number; to: number }) =>
    get<{ value: string; count: number }[] | null>(
      `/api/attribute-values?${qs({ key, since, limit, q, from: range?.from, to: range?.to })}`),
  operations: (service: string, r: RangeParams) =>
    get<string[] | null>(`/api/operations?${qs({ ...r, service })}`),

  // v0.9.603 (traces D2) — signal ile İPTAL EDİLEBİLİR. Sayfanın en pahalı
  // isteği bu; operatör aralığı/filtreyi hızlı değiştirdiğinde eski sorgu
  // ClickHouse'ta koşmaya devam etmemeli.
  traces:    (params: TracesParams, signal?: AbortSignal) =>
    get<TracesResponse>(`/api/traces?${qs(params)}`, signal),
  // FAZ 2 (traces attribute columns) — phase-2-only enrichment: fetch the
  // selected attribute columns for an EXPLICIT page of trace ids, bounded
  // by the visible rows' real min/max timestamps (ns). Same GET /api/traces
  // endpoint; the traceIds param makes the server skip phase-1 entirely and
  // switch the response shape to { extras }. Deliberately a separate method
  // (not a TracesParams field) so the response type stays honest.
  tracesExtras: (p: { traceIds: string; extraAttrs: string; from: number; to: number }) =>
    get<TracesExtrasResponse>(`/api/traces?${qs(p)}`),
  tracesAggregate: (params: AggregateParams) =>
    get<AggregateRow[] | null>(`/api/traces/aggregate?${qs(params)}`),
  // Span-relationship / structural query (Gap 3). Parent + child predicate
  // sets are JSON-encoded into the query string; the backend runs a bounded
  // self-join over raw spans and returns the resolved trace rows.
  trace:     (id: string)            => get<TraceDetailResponse>(`/api/traces/${id}`),

  // v0.8.332 (pivot Phase 3) — real OTLP exemplars for a metric window
  // (GET /api/exemplars, pivot Phase 2). Either a comma-separated
  // `fingerprints` set (PK scan) or a `metric`(+`service`) fallback.
  // 30s server-side cache — client staleTime must stay ≥ that.
  // Grouped-chart variant (v0.8.432): send the chart's own query shape;
  // the server resolves series → fingerprints and tags each item with
  // the series groupKey. 30s server cache — staleTime must stay ≥ that.
  exemplarsBySeries: (params: {
    metric: string; service?: string; groupBy: string[];
    filters?: string; from: number; to: number; limit?: number;
  }) =>
    get<{ items: import('./types').OtlpExemplar[] }>(
      `/api/exemplars/by-series?${qs({
        metric: params.metric, service: params.service,
        groupBy: params.groupBy.join(','), filters: params.filters,
        from: params.from, to: params.to, limit: params.limit,
      })}`),
  exemplars: (params: {
    fingerprints?: string; metric?: string; service?: string;
    from: number; to: number; limit?: number;
  }) =>
    get<{ items: OtlpExemplar[] }>(`/api/exemplars?${qs(params)}`),
  // v0.8.332 — OTel span links for one trace, BOTH directions in one payload
  // (GET /api/traces/{id}/links, pivot Phase 2; 30s server-side cache).
  traceLinks: (id: string) =>
    get<TraceLinks>(`/api/traces/${encodeURIComponent(id)}/links`),

  logs:      (params: LogsParams, signal?: AbortSignal) => get<LogsResponse>(`/api/logs?${qs(params)}`, signal),

  metricNames: (service: string)     => get<MetricInfo[] | null>(`/api/metrics/names${service ? '?service=' + encodeURIComponent(service) : ''}`),
  // v0.9.464 (dürüstlük A12) — zarf {points, truncated}: nokta tavanı
  // dolduğunda pencerenin SON kısmı döner ve UI bunu söyler.
  metrics:     (params: MetricsParams) => get<{ points: MetricPoint[]; truncated: boolean } | null>(`/api/metrics?${qs(params)}`),

  // Logs timeseries — Histogram aggregation routed through
  // whichever backend is configured (CH or external ES). Powers
  // the Logs source on /explore. 30s server-side cache.
  logsTimeseries: (params: {
    service?: string;
    cluster?: string; // v0.9.216 — toolbar cluster select; the table honoured it, this endpoint didn't
    env?: string; // v0.8.400 — global ?env= deployment-environment filter
    search?: string;
    from?: number;
    to?: number;
    severity?: number;
    traceId?: string;
    hasTrace?: boolean; // v0.8.406 — trace-only filter
    bucketSec?: number;
    groupBy?: string;
  }) =>
    get<{ name: string; points: { t: number; v: number }[] }[]>(
      `/api/logs/timeseries?${qs(params)}`),

  // Sent-notification log (v0.8.247 backend / v0.8.263 UI) — the
  // /events Notifications tab. from/to are unix-ns strings like the
  // logs endpoints; kind filters one channel type.
  notificationLog: (params: {
    from?: number; to?: number; kind?: string; limit?: number; offset?: number;
  }) =>
    get<NotificationLogEntry[]>(`/api/notifications/log?${qs(params)}`),

  // Fields-panel accordion (v0.8.255): top-5 values of one field
  // in the current slice, with counts for the % bars. Expand-
  // triggered only — never poll this (60s server-side cache).
  logsFieldStats: (params: {
    field: string;
    service?: string;
    cluster?: string;
    env?: string; // v0.8.400 — global ?env= deployment-environment filter
    search?: string;
    from?: number;
    to?: number;
    severity?: number;
    traceId?: string;
    spanId?: string;
  }) =>
    get<LogFieldStats>(`/api/logs/fieldstats?${qs(params)}`),

  health: ()                         => get<HealthInfo>(`/api/health`),
  status: ()                         => get<SystemStatus>(`/api/status`),
  // Build-tag — unauthenticated, so the login page can render it
  // before the operator has a session.
  // v0.9.344 — priority/severity chip counts over the UNFILTERED problem set.
  //
  // The endpoint has existed since the priority buckets shipped and was never
  // called: the page computed its chip counts from `data`, which is the
  // response to a request that already carried ?priority=. With the default
  // P1+P2 selection the P3 chip therefore read 0 forever, and an operator
  // could not discover that P3 problems existed at all.
  //
  // It deliberately takes no priority param — that is the whole point — and
  // is server-cached 5s on a key shared across viewers.
  problemBuckets: (params: { status?: string; service?: string; env?: string; ownerTeam?: string; sreTeam?: string; cluster?: string } = {}) =>
    get<{ severity: Record<string, number>; priority: Record<string, number>; total: number } | null>(
      `/api/problems/buckets?${qs(params)}`),
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
  // v0.9.465 (dürüstlük A9) — zarf: sayımlar SQL'den, truncated = 200
  // penceresi doldu. anomalyEvent(id): deep-link kurtarma ucu.
  anomalyEvents:       (since: GoDuration = '24h', limit = 200) =>
    get<{ items: import('./types').AnomalyEvent[]; activeTotal: number; clearedTotal: number; truncated: boolean }>(`/api/anomalies/events?since=${since}&limit=${limit}`),
  // v0.9.471 — ?id= sorgu paramı: path-segment hali Go 1.22 ServeMux'ta
  // {id}/rootcause kalıbıyla çakışıp boot'u panic'letiyordu (v465-470).
  anomalyEvent:        (id: string) =>
    get<import('./types').AnomalyEvent | null>(`/api/anomalies/event?id=${encodeURIComponent(id)}`),

  // Active anomalies autocomplete — backs the Cmd-K silence
  // action's first param. Returns slim shape: id (fingerprint),
  // kind, pattern, service, status, and a display label. Editor-
  // gated server-side. v0.5.459.
  activeAnomalies: (q: string, limit = 20) =>
    get<Array<{
      id: string;
      kind: string;
      pattern: string;
      service: string;
      status: string;
      label: string;
    }>>(`/api/anomalies/active?q=${encodeURIComponent(q)}&limit=${limit}`),

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
  auditLog: (since: GoDuration = '24h', filters: { actor?: string; action?: string; target?: string; targetId?: string } = {}) =>
    get<import('./types').AuditEntry[]>(`/api/admin/audit?${qs({ since, ...filters })}`),
  // Alert-tuning noisy-rules report (v0.5.131). Cached server-
  // side 5 min so a burst of operators viewing it during morning
  // triage doesn't re-run the GROUP BY.
  alertTuningNoisyRules: (since: GoDuration = '24h', limit = 30) =>
    get<{
      rules: Array<import('./types').NoisyRule>;
      from: number; to: number; sinceSec: number;
    }>(`/api/admin/alert-tuning/noisy-rules?${qs({ since, limit })}`),
  // Logs field discovery (v0.5.136). Returns the searchable field
  // paths the configured logs backend knows about. Empty array
  // for ClickHouse (shape is fixed); ES backend returns the
  // mapping leaves. Server caches 60s.
  logsFields: () => get<{ fields: string[]; total?: number; backend: string }>(
    `/api/logs/fields`),
  // Top values of one keyword field prefix-matched against `q`.
  // Backs the /logs search box autocomplete (v0.5.464). Returns
  // an empty list on CH backend or invalid field — caller tolerates.
  // v0.9.291 — `since` bounds the term-dictionary walk. Without it the
  // ES backend prefix-scanned every index in retention on every
  // keystroke. Server clamps to 7d and snaps the window to the hour, so
  // passing the page's own range here is a hint, not a contract.
  logsFieldValues: (field: string, q: string, limit = 20, since?: string) =>
    get<{ values: string[] }>(
      `/api/logs/field-values?field=${encodeURIComponent(field)}&q=${encodeURIComponent(q)}&limit=${limit}` +
      (since ? `&since=${encodeURIComponent(since)}` : '')),
  // ES index inventory for /admin/elastic — per-index name, doc
  // count, size, health, ILM phase/policy. CH backend returns
  // empty indices list (page shows "not Elasticsearch" state).
  // v0.5.466.
  adminElasticIndices: () =>
    get<{
      backend: string;
      indices: Array<{
        name: string;
        docCount: number;
        sizeBytes: number;
        health: string;
        ilmPolicy: string;
        ilmPhase: string;
      }>;
    }>(`/api/admin/elastic/indices`),
  // Topology hidden patterns (v0.8.241) — global glob list; matching
  // nodes never render in any topology view. GET any role, PUT editor+.
  getTopologyHidden: () => get<{ patterns: string[] }>(`/api/topology/hidden`),
  putTopologyHidden: (patterns: string[]) =>
    request<{ patterns: string[] }>(`/api/topology/hidden`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ patterns }),
    }),
  // Recent failed ES queries + cumulative counter (v0.8.230). Per-pod
  // in-memory ring; uncached so the panel reflects the error the
  // operator just triggered. CH backend returns an empty list.
  adminElasticErrors: () =>
    get<{ backend: string; queryErrors: number; recentErrors: ESQueryError[] }>(
      `/api/admin/elastic/errors`),
  // Trace-context self-discovery (v0.8.348, pivot Phase 1c) — the
  // backend verifies its OWN configured logstore: trace-id field
  // mapping verdict (keyword ✓ / text ⚠ / absent) + % of last-24h
  // logs carrying trace context, overall and top-50 per service.
  // Server-cached 5m per backend; failures come back as a typed
  // {available:false, reason} report, never a 5xx.
  adminLogstoreTraceContext: () =>
    get<import('./types').TraceContextPayload>(`/api/admin/logstore/trace-context`),
  // v0.8.407 — viewer-safe twin for the Trace page's empty-Logs-tab
  // diagnostics (same cached report, no admin gate).
  logstoreTraceContext: () =>
    get<import('./types').TraceContextPayload>(`/api/logstore/trace-context`),
  // Kibana saved-search interop URLs — used as download / upload
  // anchors in /admin/elastic. v0.5.467.
  kibanaExportURL: () => `/api/admin/elastic/saved-search-export`,
  kibanaImportPost: (ndjson: string) =>
    request<{ imported: number; skipped: number; errors?: string[] }>(
      `/api/admin/elastic/saved-search-import`,
      { method: 'POST', headers: { 'Content-Type': 'application/x-ndjson' }, body: ndjson }),
  // Operator events (v0.5.476) — list + create + delete the
  // vertical markers operators drop on every time-series chart.
  listEvents: (params: { from?: number; to?: number; service?: string; kind?: string; limit?: number } = {}) =>
    get<Array<{
      id: string; kind: string; label: string;
      time: number; service: string; link: string;
      owner: string; createdAt: number;
    }> | null>(`/api/operator-events?${qs(params)}`),
  createEvent: (body: { kind?: string; label: string; time?: number; service?: string; link?: string }) =>
    request<{ id: string }>(`/api/operator-events`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  deleteEvent: (id: string) =>
    request<void>(`/api/operator-events/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  // v0.5.402 — surrounding context (±N logs around a pivot ts).
  // Datadog Context tab equivalent. Two parallel server-side
  // searches (before / after); 30-min symmetric window, capped
  // at n=200 per side.
  logsContext: (params: { ts: number; service?: string; env?: string; n?: number }) =>
    get<{
      pivotTs: number; service: string;
      before: import('./types').LogRow[];
      after:  import('./types').LogRow[];
    }>(`/api/logs/context?${qs(params)}`),


  // Recent deploys + impact deltas for a service (v0.5.189).
  // One round-trip; backend computes before/after RED for each
  // deploy via partition-pruned spans queries.
  // v0.5.308 — lookbackHours threaded through. Backend default
  // was 24h, but DeployHistoryPanel on /service is the natural
  // destination from /deploys (which scans up to 30d). 24h
  // dropped deploys older than yesterday, panel self-hid → the
  // history-→ link looked broken. Default raised to 30d.
  deployHistory: (service: string, limit = 5, windowSec = 600, lookbackHours = 24 * 30) =>
    get<import('./types').DeployHistoryRow[]>(
      `/api/services/${encodeURIComponent(service)}/deploy-history?${qs({ limit, windowSec, lookbackHours })}`),

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
  slowQueries: (params: { from?: number; to?: number; db_system?: string; db_name?: string; limit?: number }) =>
    get<import('./types').SlowQueryRow[]>(`/api/databases/slow-queries?${qs(params)}`),
  // v0.8.378 — statement detail drill-down (Stage-2 slice D2). One
  // payload with per-section null tolerance, keyed on the v0.8.375
  // stmt_hash decimal string; compare=prior adds prior* fields to the
  // summary + callers (Endpoints v0.5.404 pattern).
  dbStmtDetail: (params: { hash: string; system?: string; db?: string; from: number; to: number; compare?: 'prior' }) =>
    get<import('./types').DBStmtDetail>(`/api/databases/statements/detail?${qs(params)}`),

  // AI observability (v0.5.163). Admin-only read endpoints.
  aiCalls: (params: { surface?: string; provider?: string; status?: string; from?: number; to?: number; limit?: number }) =>
    get<import('./types').AICall[]>(`/api/ai/calls?${qs(params)}`),
  aiCall: (id: string) =>
    get<import('./types').AICall>(`/api/ai/calls/${encodeURIComponent(id)}`),
  aiStats: (params: { from?: number; to?: number }) =>
    get<import('./types').AIStats>(`/api/ai/stats?${qs(params)}`),
  aiSeries: (params: { from?: number; to?: number }) =>
    get<import('./types').AICallsTimePoint[]>(`/api/ai/series?${qs(params)}`),
  // v0.8.399 — thumbs up/down on an AI answer. exchangeId comes from
  // the chat SSE answer event; re-posting the same id replaces the
  // verdict (user changed their mind).
  postAIFeedback: (body: { exchangeId: string; verdict: 1 | -1 }) =>
    request<{ ok: boolean }>(`/api/ai/feedback`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  // v0.9.423 — 👎 madenciliği: düşük puanlı cevaplar prompt örnekleriyle.
  // v0.9.549 — guided router'ın yakalayamadığı sorular (serbest tool
  // döngüsüne düşenler). days sabit basamak: cache anahtarına giriyor.
  aiRouterGaps: (days: 1 | 7 | 30 = 7) =>
    get<{
      gaps: Array<{ question: string; count: number; lastAt: number; users: number }>;
      days: number; totalFallbacks: number; generatedAt: number;
    }>(`/api/ai/router-gaps?days=${days}`),
  // v0.9.594 — RCA hakem motorunun kalitesi. Kardeşleri transport
  // sağlığını ölçüyor; bu, cevabın kendisine bakan tek uç.
  // Pencere from/to ile geçer — kardeş aiStats/aiSeries ile AYNI
  // sözleşme. rangeS kullansaydım aynı sayfada iki ayrı pencere
  // kavramı olurdu ve karolar grafikle uyuşmazdı.
  aiRCAQuality: (params: { from?: number; to?: number }) =>
    get<import('./types').RCAVerdictQuality>(`/api/ai/rca-quality?${qs(params)}`),
  aiNegativeFeedback: (rangeS?: number) =>
    get<{ rows: import('./types').NegativeFeedbackCall[]; rangeS: number }>(
      `/api/ai/feedback/negative${rangeS ? `?rangeS=${rangeS}` : ''}`),
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
    get<{ enabled: boolean; minPeakRatio: number; criticalPeakRatio: number; minSustainedSec: number; minCount: number }>(
      `/api/settings/anomaly-promotion`),
  // v0.9.248 — age-based escalation (escalateStaleProblems). Separate
  // key from anomaly promotion because it applies to EVERY Problem,
  // not just promoted anomalies.
  getProblemEscalation: () =>
    get<{ enabled: boolean; infoToWarningSec: number; warningToCriticalSec: number }>(
      `/api/settings/problem-escalation`),
  putProblemEscalation: (c: {
    enabled: boolean; infoToWarningSec: number; warningToCriticalSec: number;
  }) =>
    request<typeof c>(`/api/settings/problem-escalation`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(c),
    }),
  // v0.9.775 — exception triyaj pencereleri. problem-escalation'dan
  // ayrı anahtar: o HER Problem'e uygulanır, bu yalnız exception
  // gruplarının P1/P2/P3 basamağına.
  getExceptionTriage: () =>
    get<ExceptionTriageConfig>(`/api/settings/exception-triage`),
  putExceptionTriage: (c: ExceptionTriageConfig) =>
    request<ExceptionTriageConfig>(`/api/settings/exception-triage`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(c),
    }),
  // v0.9.797 — metrik route dışlamaları. Admin; PUT desenleri sunucuda
  // regexp.Compile ile doğrular (bozuk desen 400) ve dropAtIngest'li
  // kuralların Pipeline ikizlerini senkronlar.
  getMetricExclusions: () =>
    get<MetricExclusions>(`/api/settings/metric-exclusions`),
  putMetricExclusions: (c: MetricExclusions) =>
    request<MetricExclusions>(`/api/settings/metric-exclusions`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(c),
    }),
  // v0.9.800 — anomali dedektörünün izlediği metrik seti. Ayrı anahtar:
  // anomaly-promotion sinyalin Problem'e TERFİSİNİ ayarlar, bu ise
  // sinyalin hiç ÖLÇÜLÜP ölçülmeyeceğini. Sunucu "hepsi kapalı"yı 400
  // ile reddeder (motoru tümden kapatmanın yolu bu vida değil).
  getAnomalyTracked: () =>
    get<AnomalyTrackedConfig>(`/api/settings/anomaly-tracked`),
  putAnomalyTracked: (c: AnomalyTrackedConfig) =>
    request<AnomalyTrackedConfig>(`/api/settings/anomaly-tracked`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(c),
    }),
  putAnomalyPromotion: (c: {
    enabled: boolean; minPeakRatio: number; criticalPeakRatio: number; minSustainedSec: number; minCount: number;
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
  databases: (fromNs: number, toNs: number, compare?: 'prior') =>
    get<import('./types').DBInstance[] | null>(
      `/api/databases?from=${fromNs}&to=${toNs}${compare ? `&compare=${compare}` : ''}`),
  // Per-row RED sparklines + latest-bucket health snapshot for the
  // /databases + /messaging overview grid. One DBTrend per
  // (dbSystem, instance, dbName) — join to the overview rows by
  // (system, instance, dbName). Sourced from db_summary_5m, 30s
  // cached server-side.
  dbTrends: (fromNs: number, toNs: number) =>
    get<import('./types').DBTrend[] | null>(
      `/api/databases/trends?from=${fromNs}&to=${toNs}`),
  // v0.9.434 — /messaging satır-içi trend (dbTrends'in ikizi; DBTrend
  // şekli paylaşılır: dbSystem=msg_system, instance=destination, cluster).
  msgTrends: (fromNs: number, toNs: number) =>
    get<import('./types').DBTrend[] | null>(
      `/api/messaging/trends?from=${fromNs}&to=${toNs}`),
  // /messaging overview — parallel shape for queues / topics
  // (Kafka / RabbitMQ / IBM MQ / NATS / etc.). compare='prior'
  // (v0.8.364) merges the immediately-preceding equal-length
  // window onto each row as prior* fields — opt-in, doubles the
  // backend scan.
  messaging: (fromNs: number, toNs: number, compare?: 'prior') =>
    get<import('./types').MessagingInstance[] | null>(
      `/api/messaging?from=${fromNs}&to=${toNs}${compare ? `&compare=${compare}` : ''}`),
  // /external overview — one row per third-party destination in the
  // window, from topology_edges_5m external edges (v0.8.446, Wave 3 A1).
  external: (fromNs: number, toNs: number) =>
    get<import('./types').ExternalHost[] | null>(
      `/api/external?from=${fromNs}&to=${toNs}`),
  // Drawer payload for one external host — per-caller breakdown +
  // 5m RED trend. Fetched on drawer open only.
  externalHost: (host: string, fromNs: number, toNs: number) =>
    get<import('./types').ExternalHostDetail | null>(
      `/api/external/host?host=${encodeURIComponent(host)}&from=${fromNs}&to=${toNs}`),
  // /hosts inventory — one row per host/pod emitting metrics in the
  // window (v0.8.449, Wave 3 A4). Window clamped to ≤6h server-side.
  hosts: (fromNs: number, toNs: number) =>
    get<import('./types').HostRow[] | null>(
      `/api/hosts?from=${fromNs}&to=${toNs}`),
  // Host drawer — per-service breakdown + per-minute CPU/mem trend.
  hostDetail: (host: string, fromNs: number, toNs: number) =>
    get<import('./types').HostDetail | null>(
      `/api/hosts/detail?host=${encodeURIComponent(host)}&from=${fromNs}&to=${toNs}`),
  // Detail drawers — per-(service, pod) caller breakdown + top
  // operations for one (system, instance) tuple. Drives the
  // row-click drawer on /databases and /messaging.
  databaseDetail: (system: string, instance: string, fromNs: number, toNs: number) =>
    get<import('./types').DBDetail | null>(
      `/api/databases/detail?system=${encodeURIComponent(system)}&instance=${encodeURIComponent(instance)}&from=${fromNs}&to=${toNs}`),
  // Cross-engine waits & locks strip (v0.8.391, Stage-2 D3) — one
  // normalized wait/lock model per (system, instance), fed by
  // whatever the engine's OTel receiver actually emits. Lazy:
  // fetched only when the drawer renders the strip.
  dbWaitLock: (system: string, instance: string, fromNs: number, toNs: number) =>
    get<import('./types').DBWaitLock | null>(
      `/api/databases/waitlock?system=${encodeURIComponent(system)}&instance=${encodeURIComponent(instance)}&from=${fromNs}&to=${toNs}`),
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
  // v0.8.486 — sayfa-üstü duyuru şeridi (admin Settings'ten yönetir).
  getAnnouncement: () =>
    get<import('./announcement').AnnouncementView>(`/api/announcement`),
  putAnnouncement: (a: import('./announcement').AnnouncementView) =>
    request<import('./announcement').AnnouncementView>(`/api/admin/announcement`, {
      method: 'PUT', body: JSON.stringify(a),
    }),
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
  // Root-cause bundle — one cached read assembling deploy / correlations /
  // blast-radius / bubble-up / exemplar for a Problem. Powers the triage
  // drawer's RootCausePanel. Backend clamps the window + soft-fails each
  // sub-signal, so the bundle is always returned (404 only if id unknown).
  problemRootCause: (id: string) =>
    get<import('./types').RootCause>(`/api/problems/${encodeURIComponent(id)}/rootcause`),
  // Anomaly-anchored root-cause bundle (rc #1) — same fan-out as
  // problemRootCause but keyed on an AnomalyEvent's window. The
  // RootCauseRibbon fetches this ON EXPAND for an anomaly row to show the
  // ranked candidates + deploy + exemplar; the collapsed chip rides the list
  // summary (AnomalyEvent.rootCause), so there's NO fetch on mount.
  anomalyRootCause: (id: string) =>
    get<import('./types').AnomalyRootCause>(`/api/anomalies/${encodeURIComponent(id)}/rootcause`),
  // Optional Copilot PROSE narration on top of the deterministic ranking (rc
  // #4). The ✨ Explain button in the expanded ribbon fetches this LAZILY on
  // click — never on mount/expand (Copilot calls cost). Backend reads the
  // PERSISTED hypothesis, routes through s.copilotExplain (/ai attribution), and
  // caches the prose keyed on the hypothesis version. 404 (request throws) when
  // no hypothesis is synthesized yet → the ribbon shows "no narration available".
  rootCauseExplain: (id: string) =>
    get<import('./types').RootCauseExplain>(`/api/anomalies/${encodeURIComponent(id)}/rootcause/explain`),
  problemRootCauseExplain: (id: string) =>
    get<import('./types').RootCauseExplain>(`/api/problems/${encodeURIComponent(id)}/rootcause/explain`),
  // Correlated Signals (task #6) — one cross-signal pivot bundle. Given any
  // anchor (trace / log / metric) the backend assembles the correlated other
  // two (trace ↔ logs ↔ metrics, joined on trace_id → service.name → window),
  // soft-failing each lens. Read-only, open. Drives CorrelationContextDrawer.
  correlateContext: (anchor: import('./types').PivotAnchor) => {
    const p: Record<string, string | number> = { kind: anchor.kind };
    if ('traceId' in anchor && anchor.traceId) p.traceId = anchor.traceId;
    if ('service' in anchor && anchor.service) p.service = anchor.service;
    if ('tsNs' in anchor && anchor.tsNs) p.tsNs = anchor.tsNs;
    if ('metricKind' in anchor && anchor.metricKind) p.metricKind = anchor.metricKind;
    if (anchor.fromNs) p.from = anchor.fromNs;
    if (anchor.toNs) p.to = anchor.toNs;
    return get<import('./types').CorrelationContext>(`/api/correlate/context?${qs(p)}`);
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

  // Runtime settings: external Tempo backend (v0.5.208).
  // GET returns the snapshot (no token); PUT saves a new config.
  // An empty `token` in the PUT body preserves the previously
  // stored token — operators only paste a new one to rotate.
  getTempoSettings: () => get<TempoSnapshot>(`/api/settings/tempo`),
  putTempoSettings: (s: TempoSettingsInput) =>
    request<TempoSnapshot>(`/api/settings/tempo`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(s),
    }),
  // Thanos multi-cluster config (v0.8.577, admin). Tempo contract:
  // GET is masked (per-cluster hasToken), PUT's empty token
  // preserves the stored one (matched by cluster name).
  getThanosSettings: () => get<ThanosSnapshot>(`/api/settings/thanos`),
  putThanosSettings: (s: ThanosSettingsInput) =>
    request<ThanosSnapshot>(`/api/settings/thanos`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(s),
    }),
  // Remote-cluster pod metrics (/clusters yüzeyi). Fan-out is the
  // CALLER's: one request per cluster so each rides its own cache
  // slot and fails independently (audit §6).
  clusterSources: () => get<{ clusters: string[] }>(`/api/clusters/sources`),
  clusterNodes: (cluster: string) =>
    get<ClusterNodesResponse>(`/api/clusters/nodes?cluster=${encodeURIComponent(cluster)}`),
  clusterSummary: (cluster: string) =>
    get<ClusterSummary>(`/api/clusters/summary?cluster=${encodeURIComponent(cluster)}`),
  clusterNamespaces: (cluster: string) =>
    get<ClusterNamespacesResponse>(`/api/clusters/namespaces?cluster=${encodeURIComponent(cluster)}`),
  clusterNamespaceDetail: (cluster: string, namespace: string, fromNs: number, toNs: number) =>
    get<ClusterPodDetail>(`/api/clusters/namespaces/detail?cluster=${encodeURIComponent(cluster)}` +
      `&namespace=${encodeURIComponent(namespace)}&from=${fromNs}&to=${toNs}`),
  clusterDeployments: (cluster: string, namespace: string) =>
    get<ClusterDeploymentsResponse>(`/api/clusters/deployments?cluster=${encodeURIComponent(cluster)}` +
      `&namespace=${encodeURIComponent(namespace)}`),
  clusterAlerts: (cluster: string) =>
    get<ClusterAlertsResponse>(`/api/clusters/alerts?cluster=${encodeURIComponent(cluster)}`),
  clusterResourceTrend: (cluster: string, metric: 'cpu' | 'mem', byNode: boolean, fromNs: number, toNs: number) =>
    get<ClusterResourceTrendResponse>(`/api/clusters/resource-trend?cluster=${encodeURIComponent(cluster)}` +
      `&metric=${metric}&byNode=${byNode ? 1 : 0}&from=${fromNs}&to=${toNs}`),
  // v0.9.50 (handoff §8) — Service→Infra sekmesinin CPU/Mem grafiği.
  // v0.9.546 — netin/netout eklendi (operatör: JVM yokken CPU/Mem/Network
  // grafikleri). Sunucuda beyaz listeli: değer cache anahtarına giriyor.
  clusterDeployTrend: (cluster: string, ns: string, deploy: string, metric: 'cpu' | 'mem' | 'netin' | 'netout', byPod: boolean, fromNs: number, toNs: number) =>
    get<ClusterDeployTrendResponse>(`/api/clusters/deploy-trend?cluster=${encodeURIComponent(cluster)}` +
      `&ns=${encodeURIComponent(ns)}&deploy=${encodeURIComponent(deploy)}` +
      `&metric=${metric}&byPod=${byPod ? 1 : 0}&from=${fromNs}&to=${toNs}`),
  // v0.9.534 — Service→Infra "Router / HAProxy": namespace'in route'larına
  // router gözünden trend (2xx/5xx oranı, backend gecikmesi).
  clusterHaproxyTrend: (cluster: string, ns: string, kind: '2xx' | '5xx' | 'latency', fromNs: number, toNs: number) =>
    get<import('./types').ClusterHaproxyTrendResponse>(`/api/clusters/haproxy-trend?cluster=${encodeURIComponent(cluster)}` +
      `&ns=${encodeURIComponent(ns)}&kind=${kind}&from=${fromNs}&to=${toNs}`),
  // v0.9.144 — Service→Infra JBoss/JVM JMX auto-discovery: servisin bir
  // cluster'da taşıdığı jvm_/jboss_ metrik adları.
  clusterJmxMetrics: (cluster: string, ns: string, deploy: string) =>
    get<ClusterJMXMetricsResponse>(`/api/clusters/jmx-metrics?cluster=${encodeURIComponent(cluster)}` +
      `&ns=${encodeURIComponent(ns)}&deploy=${encodeURIComponent(deploy)}`),
  // Keşfedilen bir JMX metriğinin trendi (metric = ham jvm_*/jboss_* ad).
  // pod dolu ise (Grafana $pod, v0.9.149) sorgu o tek pod'a daralır.
  clusterJmxTrend: (cluster: string, ns: string, deploy: string, metric: string, byPod: boolean, fromNs: number, toNs: number, pod = '') =>
    get<ClusterJMXTrendResponse>(`/api/clusters/jmx-trend?cluster=${encodeURIComponent(cluster)}` +
      `&ns=${encodeURIComponent(ns)}&deploy=${encodeURIComponent(deploy)}` +
      `&metric=${encodeURIComponent(metric)}&byPod=${byPod ? 1 : 0}&from=${fromNs}&to=${toNs}` +
      (pod ? `&pod=${encodeURIComponent(pod)}` : '')),
  clusterNetworkTrend: (cluster: string, fromNs: number, toNs: number) =>
    get<ClusterNetworkTrendResponse>(`/api/clusters/network-trend?cluster=${encodeURIComponent(cluster)}` +
      `&from=${fromNs}&to=${toNs}`),
  clusterNamespacePodsTrend: (cluster: string, namespace: string, fromNs: number, toNs: number) =>
    get<ClusterPodsTrendResponse>(`/api/clusters/namespaces/pods-trend?cluster=${encodeURIComponent(cluster)}` +
      `&namespace=${encodeURIComponent(namespace)}&from=${fromNs}&to=${toNs}`),
  // podRe (v0.9.536, opsiyonel) — hedefli envanter: sunucu pod=~ ile
  // daraltır, topk(500) servisin kendi pod'ları içinde işler. Boş =
  // tüm cluster (/clusters sayfası, eski davranış).
  clusterPods: (cluster: string, podRe?: string) =>
    get<ClusterPodsResponse>(`/api/clusters/pods?cluster=${encodeURIComponent(cluster)}` +
      (podRe ? `&podRe=${encodeURIComponent(podRe)}` : '')),
  clusterPodDetail: (cluster: string, namespace: string, pod: string, fromNs: number, toNs: number) =>
    get<ClusterPodDetail>(`/api/clusters/pods/detail?cluster=${encodeURIComponent(cluster)}` +
      `&namespace=${encodeURIComponent(namespace)}&pod=${encodeURIComponent(pod)}&from=${fromNs}&to=${toNs}`),
  // UI-managed logstore backend (v0.8.232, admin). Test builds +
  // pings a candidate config WITHOUT touching the live backend —
  // the response carries the real ES error for the operator.
  getLogstoreSettings: () => get<ESLogstoreSnapshot>(`/api/settings/logstore`),
  putLogstoreSettings: (s: ESLogstoreInput) =>
    request<ESLogstoreSnapshot>(`/api/settings/logstore`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(s),
    }),
  testLogstoreSettings: (s: ESLogstoreInput) =>
    request<{ ok: boolean; error?: string }>(`/api/settings/logstore/test`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(s),
    }),
  // External Kibana deep-link (v0.5.236). GET is open to every
  // signed-in user so the Logs page can render the link; PUT
  // is admin-only.
  getKibanaSettings: () => get<KibanaSettings>(`/api/settings/kibana`),
  putKibanaSettings: (s: KibanaSettings) =>
    request<KibanaSettings>(`/api/settings/kibana`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(s),
    }),

  // Config export / import — admin-only. Export streams a JSON
  // file (Content-Disposition: attachment). We fetch as blob so
  // the browser saves it with the server-supplied filename, and
  // so the auth cookie / 401 redirect flow stays consistent with
  // every other admin endpoint.
  exportConfig: async (): Promise<void> => {
    const r = await fetch(API_BASE + `/api/admin/config/export`, { credentials: 'include' });
    if (r.status === 401) { onUnauthorized?.(); throw new UnauthorizedError(); }
    if (!r.ok) throw new Error(`HTTP ${r.status}: ${await r.text()}`);
    const blob = await r.blob();
    // Server sends a dated filename via Content-Disposition; the
    // browser fetch API surfaces the header so we honour it.
    let fname = 'coremetry-config.json';
    const dispo = r.headers.get('content-disposition') ?? '';
    const m = /filename="([^"]+)"/.exec(dispo);
    if (m) fname = m[1];
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url; a.download = fname;
    document.body.appendChild(a); a.click();
    setTimeout(() => { URL.revokeObjectURL(url); a.remove(); }, 0);
  },
  importConfig: (file: File, mode: 'merge' | 'replace'): Promise<{
    mode: string; tables: Record<string, number>; rows: number;
    skippedUnknown?: string[];
  }> =>
    request(`/api/admin/config/import?mode=${mode}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: file,
    }),
  // v0.5.396 — dry-run preview before triggering import. Same
  // file payload, read-only on the server; returns per-table
  // {willAdd, willOverwrite, unchanged, onlyInDB} so the
  // operator can confirm scope before replaying anything.
  diffConfig: (file: File): Promise<{
    format: string; version: number;
    exportedAt: string; coremetryVersion?: string;
    tables: Record<string, {
      willAdd: string[];
      willOverwrite: string[];
      unchanged: number;
      onlyInDB: number;
    }>;
  }> =>
    request(`/api/admin/config/diff`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: file,
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
  // v0.8.527 — AD grup senkron: durum özeti (in-memory snapshot; cache'siz),
  // şimdi-senkronla (admin+audit), dry-run önizleme (CH'ye yazmaz).
  getLdapGroupSync: () =>
    get<import('./types').LDAPGroupSyncSummary>(`/api/admin/ldap/groupsync`),
  syncLdapGroupsNow: () =>
    request<import('./types').LDAPGroupSyncSummary>(`/api/admin/ldap/groupsync/sync`, { method: 'POST' }),
  previewLdapGroupSync: () =>
    get<import('./types').LDAPGroupSyncPreview>(`/api/admin/ldap/groupsync/preview`),
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
  // v0.6.53 — agentic chatbot stream. POST + SSE (EventSource is
  // GET-only, so we read the fetch body stream and parse SSE frames
  // by hand). onEvent fires per `event:`/`data:` frame; the promise
  // resolves when the stream closes (the `done` event) or rejects on
  // transport error. abort via the AbortSignal.
  // ── API tokens (v0.8.444) — servis kimlikleri ───────────────────────────
  listAPITokens: () => get<{ tokens: import('./types').APIToken[] }>('/api/admin/api-tokens'),
  createAPIToken: (name: string, role: string) =>
    request<{ token: string; record: import('./types').APIToken }>('/api/admin/api-tokens', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, role }),
    }),
  revokeAPIToken: (id: string) =>
    request<{ ok: boolean }>(`/api/admin/api-tokens/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  // ── RAG (v0.8.438) — doküman soru-cevap yönetimi ────────────────────────
  getRagConfig: () => get<import('./types').RagConfigView>('/api/rag/config'),
  putRagConfig: (c: { endpoint: string; model: string; enabled: boolean; topK?: number; apiKey?: string; insecureSkipVerify?: boolean;
    sources?: { url: string; authHeader?: string }[] }) =>
    request<import('./types').RagConfigView>('/api/rag/config', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(c),
    }),
  listRagDocuments: () =>
    get<{ documents: import('./types').RagDocument[]; ready: boolean }>('/api/rag/documents'),
  uploadRagDocument: (file: File) => {
    const fd = new FormData();
    fd.append('file', file);
    return request<{ docId: string; chunks: number }>('/api/rag/documents', {
      method: 'POST', body: fd,
    });
  },
  // uploadRagText — yapıştırılan metni doküman olarak ekler (backend'in
  // JSON {name,text} yolu; readRAGUpload). OneNote/wiki gibi tarayıcıdan
  // kopyalanan içerik için — dosya/token/connector gerekmez (v0.9.176).
  uploadRagText: (name: string, text: string) =>
    request<{ docId: string; chunks: number }>('/api/rag/documents', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, text }),
    }),
  syncRagSources: () =>
    request<{ sources: number; pages: number; indexed: number; skipped: number; pruned: number; errors?: string[] }>(
      '/api/rag/sync', { method: 'POST' }),
  deleteRagDocument: (id: string) =>
    request<{ ok: boolean }>(`/api/rag/documents/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  copilotChat: async (
    messages: import('./types').ChatMessage[],
    onEvent: (e: import('./types').ChatStreamEvent) => void,
    signal?: AbortSignal,
    // v0.9.164 — context-awareness: bulunulan sayfanın servisi; mesaj servis
    // adı taşımıyorsa guided router bunu varsayılan alır.
    contextService?: string,
    // v0.9.184 — seçili operasyon (?op=); "bu operasyonun durumu" RED'i o
    // span-name'e daraltır (guided operation fallback).
    contextOperation?: string,
    // v0.9.479 — AI çekmecesindeki açıklamanın metni (+ kanıt id'leri).
    // Sunucu bunu narration bloğuna katar ve özneye oturmayan guided
    // rotayı bastırır; ALAN YOKKEN her yol bayt-bayt eski davranışta
    // (internal/api/copilot_drawer.go). Operatör raporu: çekmeceden
    // açılan sohbet ekrandaki exception'ı bilmiyordu.
    contextExplain?: string,
    // v0.9.482 — çekmecenin ÖZNESİ (`?ai=` kodeği: "trace:<id>",
    // "span:<trace>:<span>", "exception:<fp>"). Sunucu bundan ilgili
    // explain'in HAM KANITINI yeniden kurar (trace span'leri + ilişkili
    // loglar) ve anlatıma katar. Operatör raporu: açıklamanın metni
    // takiplere yetmiyordu — "logda ne yazıyor" kör cevaplanıyordu.
    contextSubject?: string,
    // v0.9.529 — EKRANDAKİ zaman aralığı (saniye). Soru açık bir pencere
    // TAŞIMIYORSA sunucu sabit 30dk yerine bunu kullanır: operatör 6
    // saatlik pencereye bakarken "hata oranı ne" diye sorunca cevap
    // baktığı pencereye ait olur. Açık pencere taşıyan soru ("son 24
    // saatte…") bunu EZER — soru her zaman ekrandan güçlüdür.
    contextRangeS?: number,
    // v0.9.537 — EKRANDAKİ trace ID'si (/trace?id=). Mesajda açık
    // 32-hex varsa sunucu onu tercih eder; bu yalnız ID'siz "bu trace
    // neden yavaş" şekilleri için.
    contextTrace?: string,
  ): Promise<void> => {
    const context =
      contextService || contextOperation || contextExplain || contextSubject || contextRangeS || contextTrace
        ? {
            ...(contextService ? { service: contextService } : {}),
            ...(contextOperation ? { operation: contextOperation } : {}),
            ...(contextExplain ? { explain: contextExplain } : {}),
            ...(contextSubject ? { subject: contextSubject } : {}),
            ...(contextRangeS && contextRangeS > 0 ? { rangeS: contextRangeS } : {}),
            ...(contextTrace ? { trace: contextTrace } : {}),
          }
        : undefined;
    const r = await fetch(API_BASE + '/api/copilot/chat', {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(context ? { messages, context } : { messages }),
      signal,
    });
    if (!r.ok || !r.body) {
      throw new Error(`chat failed: ${r.status}`);
    }
    const reader = r.body.getReader();
    const decoder = new TextDecoder();
    let buf = '';
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      // SSE frames are separated by a blank line.
      let sep: number;
      while ((sep = buf.indexOf('\n\n')) !== -1) {
        const frame = buf.slice(0, sep);
        buf = buf.slice(sep + 2);
        let event = 'message';
        let data = '';
        for (const line of frame.split('\n')) {
          if (line.startsWith('event:')) event = line.slice(6).trim();
          else if (line.startsWith('data:')) data += line.slice(5).trim();
        }
        if (!data) continue;
        try {
          const payload = JSON.parse(data);
          onEvent({ kind: event, ...payload } as import('./types').ChatStreamEvent);
        } catch { /* ignore malformed frame */ }
      }
    }
  },
  // analyzeService (v0.8.85) — per-service single-shot AI analysis. The server
  // summarises RED + baseline + top errors + deploys + neighbours and the
  // operator-configured model returns the {ozet, olasi_neden, kanit, oneriler,
  // guven} verdict. refresh bypasses the 5-min Redis cache.
  analyzeService: (service: string, rangeS?: number, refresh?: boolean) =>
    request<import('./types').ServiceAnalysisResponse>(
      `/api/copilot/analyze-service?service=${encodeURIComponent(service)}${rangeS ? `&rangeS=${rangeS}` : ''}${refresh ? '&refresh=1' : ''}`,
      { method: 'POST' },
    ),

  copilotExplainTrace:   (id: string) =>
    request<{ explanation: string; evidenceSpanIds?: string[] }>(`/api/copilot/explain-trace/${id}`, { method: 'POST' }),
  // Per-span explain (v0.5.144). Backend pulls target span +
  // parent + children + error siblings for a focused prompt.
  copilotExplainSpan:    (traceId: string, spanId: string) =>
    request<{ explanation: string }>(
      `/api/copilot/explain-span/${encodeURIComponent(traceId)}?span=${encodeURIComponent(spanId)}`,
      { method: 'POST' }),
  copilotExplainProblem: (id: string) =>
    request<{ explanation: string }>(`/api/copilot/explain-problem/${id}`, { method: 'POST' }),
  // v0.9.414 — exception grubu kök-sebep: backend örnek trace + trace
  // loglarını + deploy penceresini otomatik toplar; kanıt trace/span
  // id'leri deterministik döner (UI örnek satırlarını kutular).
  copilotExplainException: (fingerprint: string) =>
    request<{ explanation: string; evidenceTraceIds?: string[]; evidenceSpanIds?: string[] }>(
      `/api/copilot/explain-exception/${encodeURIComponent(fingerprint)}`, { method: 'POST' }),
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
  // Manual claim / reassign — empty string clears the assignee
  // (back to unassigned). Server audits each call.
  // Unified triage inbox (v0.5.211). Merges Problems +
  // Exception groups + Anomaly events server-side with a
  // common priority blend; returns at most `limit` items.
  inbox: (params: {
    // v0.9.254 — 'ignored' pivotu: susturulmuş exception grupları.
    status?: 'open' | 'all' | 'ignored'; service?: string;
    // v0.9.251 — free-text triage search (service + title + source).
    // `service` stays the narrower service-only filter for older
    // shared links; the page's search box drives this.
    q?: string;
    ownerTeam?: string; sreTeam?: string;
    env?: string; // v0.8.387 — service-scoped, same semantics as /api/problems
    limit?: number;
    // v0.9.319 — server-side sort. Ranking happens over the whole candidate
    // set BEFORE the cap, so the sort decides which rows come back, not just
    // their order; it is part of the cache key server-side for that reason.
    sort?: string; dir?: 'asc' | 'desc';
    // v0.9.320 — occurrence floor; 0 means "show all" and is sent explicitly.
    minOcc?: number;
    // v0.9.330 — server-side facets (csv). Applied before the cap.
    kind?: string; prio?: string;
    // v0.9.525 — first-seen penceresi (2h/24h/7d); boş = hepsi.
    since?: string;
  } = {}) =>
    // v0.9.221 — was a bare array; the page had no way to tell a full queue
    // from the top slice of a truncated one.
    get<{
      items: import('./types').InboxItem[];
      total: number; limit: number; truncated: boolean; scanCapped?: boolean;
      minOcc?: number; hiddenByMinOcc?: number;
      counts?: Record<string, number>;
    } | null>(`/api/inbox?${qs(params)}`),
  // v0.8.288 — the single triage badge total (not-resolved problems + open
  // exception groups + active anomalies). COUNT-only, 10s server cache.
  // v0.9.219 — env-scoped like every other triage read; without it the badge
  // counted all environments while the page it links to showed one.
  inboxCount: (env?: string) =>
    get<{ count: number; problems: number; exceptions: number; httpErrors: number; anomalies: number; incidents: number }>(
      `/api/inbox/count${env ? `?env=${encodeURIComponent(env)}` : ''}`),
  setProblemAssignee: (id: string, assignee: string) =>
    request<{ id: string; assignee: string }>(`/api/problems/${encodeURIComponent(id)}/assignee`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ assignee }),
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
  // v0.9.456 — dürüstlük zarfı: counts SQL'den (null = sayım
  // alınamadı, sayfa-türevine düş), truncated = 200-pencere doldu.
  listIncidents:    (params?: { status?: string; service?: string; severity?: string; limit?: number }) =>
    get<{ items: Incident[]; counts: Record<string, number> | null; truncated: boolean } | null>(`/api/incidents?${qs(params ?? {})}`),
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

  // spanMetric — bare series list for the legacy consumers (RED panel, Traces
  // volume strip, dashboard panels). The endpoint now returns a
  // { series, totalSeries? } envelope (v0.8.x top-N trim); this method unwraps
  // .series so those callers stay on SpanMetricSeries[] | null. Explore, which
  // needs the pre-trim total for its "+N more", uses spanMetricTopN below.
  spanMetric: (params: SpanMetricParams) =>
    get<SpanMetricResult | null>(`/api/spans/metric?${qs(params)}`)
      .then(r => (r ? r.series : null)),

  // spanMetricTopN — full { series, totalSeries? } envelope for the Explore
  // builder. The backend trims a high-cardinality groupBy to the top
  // ≤TOP_N_MAX series by area (the exact set PanelStack renders); totalSeries
  // is the pre-trim count (omitted when no trim happened) so the "+N more"
  // count stays accurate without shipping thousands of series over the wire.
  spanMetricTopN: (params: SpanMetricParams) =>
    get<SpanMetricResult | null>(`/api/spans/metric?${qs(params)}`),

  // resolveMetric — "every metric is a doorway" D4. Resolves a MetricQuery
  // descriptor server-side: the descriptor rides as ?m=<base64url(JSON)> (the
  // SAME codec metricExploreHref uses for deep links) and the backend picks
  // the spanmetrics tier / tracemetrics path. from/to are unix nanoseconds
  // (RangeParams convention). Pass exemplars to get per-bucket slow/error
  // trace_ids for "click a bucket → open the trace".
  resolveMetric: (
    mq: MetricQuery,
    r: RangeParams,
    opts?: { step?: number; exemplars?: boolean },
  ) =>
    get<MetricResolveResult | null>(
      `/api/metrics/resolve?m=${encodeMetricQuery(mq)}&from=${r.from}&to=${r.to}` +
        (opts?.step ? `&step=${opts.step}` : '') +
        (opts?.exemplars ? `&exemplars=1` : ''),
    ),

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
    request<Record<string, { series?: SpanMetricSeries[] | null; rowsCapped?: boolean; error?: string }>>(
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
  // v0.9.391 (grafik-audit Faz B) — yanıt zarfı {series, stepSeconds}:
  // bucket genişliği artık sözleşmede (bar genişliği/gap eşiği tahmin
  // değil); maxDataPoints panel nokta bütçesi (0/yok = sunucu 2000
  // emniyet tavanı). Rollup /api/rollup/red planıyla AYNI kontrat.
  spanMetricBatch: (body: {
    from?: number; to?: number; step?: number;
    maxDataPoints?: number;
    groupBy?: string[];
    filters?: string;
    dsl?: string;
    /** v0.9.601 — serbest metin yüklemi. spanMetric'in ?search='iyle AYNI
     *  alana gider; eksikliği /traces hacim şeridinin bu yüzeye
     *  geçmesini engelliyordu (arama sessizce düşerdi). */
    search?: string;
    /** v0.9.723 — Prometheus rate[W] kayan penceresi (sn); 0/yok = kapalı.
     *  Sunucu step kafesine yuvarlar, [0,600] clamp. Sayım-sınıfı seriler
     *  sıfır-doldurulur; oran/gecikme pencerede istek yoksa boşluk kalır. */
    rateWindow?: number;
    aggs: { name: string; agg: string; field?: string }[];
  }, signal?: AbortSignal) =>
    request<{ stepSeconds: number; series: Record<string, SpanMetricSeries[] | null> }>('/api/spans/metric-batch', {
      signal,
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        from:    body.from,
        to:      body.to,
        step:    body.step,
        maxDataPoints: body.maxDataPoints,
        groupBy: body.groupBy,
        // server expects filters as raw JSON; we pass through
        // the same shape the GET endpoint does.
        filters: body.filters ? JSON.parse(body.filters) : undefined,
        dsl:     body.dsl,
        rateWindow: body.rateWindow,
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

  // /api/services/{name}/rollouts (v0.8.x) — pod-churn rollout
  // events (instance-set turnover) + per-rollout RED impact, plus
  // versionConstant/instancesTracked flags. Replaces the version-
  // based deploy markers when service.version is constant.
  // v0.9.435 — filo Deploys/Rollouts geçmişi.
  deploysHistory: (fromNs: number, toNs: number, service?: string) =>
    get<import('./types').DeploysHistoryResponse>(
      `/api/deploys/history?from=${fromNs}&to=${toNs}${service ? `&service=${encodeURIComponent(service)}` : ''}`),
  serviceRollouts: (svc: string, params: { from?: number; to?: number }) =>
    get<import('./types').RolloutsResult>(
      `/api/services/${encodeURIComponent(svc)}/rollouts?${qs(params)}`),

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

  // v0.9.105 (F1) — default maxDataPoints=1500 (geniş panel px'i yaklaşık;
  // tek sabit → cache-key parçalanmaz). Çağıran açıkça geçerse o kazanır.
  // v0.9.458 — endpoint artık {series, rowsCapped} zarfı döner; bu metot
  // .series'i açar ki 6 eski tüketici SpanMetricSeries[] üzerinde kalsın.
  // Dürüstlük şeridi gerekenler metricQueryFull kullanır.
  metricQuery: (params: MetricQueryParams) =>
    get<{ series: SpanMetricSeries[]; rowsCapped?: boolean } | null>(`/api/metrics/query?${qs({ maxDataPoints: 1500, ...params })}`)
      .then(r => (r ? r.series : null)),
  // v0.9.774 — opsiyonel signal: Service Overview'un RT paneli RQ v5
  // semantiğiyle ({ signal } destructure) çağırıyor, aralık/servis
  // değişince uçuşan istek iptal ediliyor. İptal isCanceled sınıfına
  // uyar (CanceledError) — hata DEĞİL, çağıranın kendi eylemi.
  // Mevcut çağıranlar (Explore, PanelRenderer) argümansız kalır.
  metricQueryFull: (params: MetricQueryParams, signal?: AbortSignal) =>
    get<{ series: SpanMetricSeries[]; rowsCapped?: boolean } | null>(`/api/metrics/query?${qs({ maxDataPoints: 1500, ...params })}`, signal),
  // v0.6.56 — explicit-histogram heatmap + percentile bands. Reuses
  // MetricQueryParams (agg/groupBy ignored server-side for histograms).
  metricHistogram: (params: MetricQueryParams) =>
    get<HistogramResult | null>(`/api/metrics/histogram?${qs(params)}`),
  // v0.9.116 (F4 Phase 5) — PromQL range query. Returns the same
  // SpanMetricSeries[] shape as metricQuery; a 400 body carries the parse
  // error, other errors bubble the eval message.
  metricPromql: (params: { query: string; from?: number; to?: number; step?: number; maxDataPoints?: number }) =>
    get<SpanMetricSeries[] | null>(`/api/metrics/promql?${qs({ maxDataPoints: 1500, ...params })}`),
  // v0.8.356 — sort/dir: server-side global ordering (whitelisted
  // backend-side; ORDER BY runs before the LIMIT so "top by p95" is
  // the true global top-N, not the top-N-by-calls page reordered).
  // env (v0.8.385): the global Topbar picker — like cluster it forces
  // the backend's raw-spans path (spanmetrics_1m has no env dim).
  endpoints: (params: { from: number; to: number; service?: string; search?: string; cluster?: string; env?: string; limit?: number; compare?: 'prior'; groupBy?: 'signature'; sort?: string; dir?: 'asc' | 'desc';
    // v0.9.313 (brief N1) — which inbound surface. Omitted = http, the
    // pre-v0.9.313 table.
    entry?: 'rpc' }, signal?: AbortSignal) =>
    get<EndpointRow[] | null>(`/api/endpoints?${qs(params)}`, signal),
  // v0.8.360 — endpoint detail drill-down (Stage-2 slice E2). One
  // payload with per-section null tolerance; sig=1 marks path as an
  // ID-collapsed signature (the table's "group by shape" mode).
  // v0.9.306 — env/cluster carry the SAME scope the table row was
  // computed under. Without them the drawer aggregated every env for
  // the route while the table showed one: two truths, one screen.
  endpointDetail: (params: { service: string; path: string; from: number; to: number; sig?: '1'; env?: string; cluster?: string }, signal?: AbortSignal) =>
    get<EndpointDetail>(`/api/endpoints/detail?${qs(params)}`, signal),
  // v0.9.311 (brief N4) — "Where the time goes". SAMPLED over the
  // route's slowest traces; two raw-spans passes behind a 60s server
  // cache, so this is fetch-on-open and never polled.
  endpointDownstream: (params: { service: string; path: string; from: number; to: number; sig?: '1'; env?: string; cluster?: string }, signal?: AbortSignal) =>
    get<EndpointDownstream>(`/api/endpoints/downstream?${qs(params)}`, signal),
  // v0.8.360 — split-by: top-10 values of one whitelisted attribute
  // with RED each. `by` must match the backend whitelist
  // (chstore.EndpointSplitDims — mirrored in ENDPOINT_SPLIT_DIMS).
  endpointSplit: (params: { service: string; path: string; by: string; from: number; to: number; sig?: '1'; env?: string; cluster?: string }, signal?: AbortSignal) =>
    get<EndpointSplitResponse>(`/api/endpoints/split?${qs(params)}`, signal),
  serviceAttrs: (service: string, from: number, to: number, opts?: { top?: number; samples?: number }) =>
    get<ServiceAttrsResponse>(
      `/api/services/${encodeURIComponent(service)}/attrs?from=${from}&to=${to}` +
      (opts?.top ? `&top=${opts.top}` : '') +
      (opts?.samples ? `&samples=${opts.samples}` : ''),
    ),
  spanmetricsServices: (from: number, to: number, opts?: { top?: number; spark?: boolean }) => {
    const params = new URLSearchParams();
    params.set('from', String(from));
    params.set('to', String(to));
    if (opts?.top != null) params.set('top', String(opts.top));
    // ?spark=0 disables the sparkline aggregation server-side
    // — the cheapest possible load at high service cardinality.
    if (opts?.spark === false) params.set('spark', '0');
    return get<SpanMetricsServicesResponse>(`/api/spanmetrics/services?${params.toString()}`);
  },
  metricLabels: (metric: string, key: string, since: GoDuration = '24h') =>
    get<string[] | null>(`/api/metrics/labels?metric=${encodeURIComponent(metric)}&key=${encodeURIComponent(key)}&since=${since}`),
  // v0.9.771 — metricLabels'in anahtar yarısı: bir metrikte GÖRÜLMÜŞ datapoint
  // attribute anahtarları. PromQL editöründe `{` yazınca ne yazılabileceğini
  // sunucudan öğrenmek için (sabit LABEL_KEYS listesi tahmin, bu ölçüm).
  metricAttrKeys: (metric: string, service = '', since: GoDuration = '24h') =>
    get<string[] | null>(`/api/metrics/attr-keys?metric=${encodeURIComponent(metric)}&service=${encodeURIComponent(service)}&since=${since}`),

  profiles:        (params: ProfilesParams) => get<ProfileRow[] | null>(`/api/profiles?${qs(params)}`),
  profile:         (id: string)             => get<ProfileDetail>(`/api/profiles/${id}`),
  profilesForSpan: (service: string, startNs: number, endNs: number) =>
    get<ProfileRow[] | null>(`/api/profiles/by-span?service=${encodeURIComponent(service)}&start=${startNs}&end=${endNs}`),
  spanHotspots: (service: string, startNs: number, endNs: number, top = 10) =>
    get<SpanHotspotsResponse>(`/api/profiles/by-span/hotspots?service=${encodeURIComponent(service)}&start=${startNs}&end=${endNs}&top=${top}`),
  profileHotspots: (params: { service: string; type?: string; from: number; to: number; limit?: number; top?: number }) =>
    get<ProfileHotspotsResponse>(`/api/profiles/hotspots?${qs(params)}`),

  // group_id rel C — `normalized` flips the endpoint to its op_group
  // mode: operations are grouped by normalized shape (GET /users/:id)
  // instead of raw name. Same OperationSummary shape — `name` carries
  // the op_group string. Omitted/false = current raw behaviour. The
  // backend's serveCached key already hashes `normalized` (rel B) so
  // the two views don't cross-poison. Default is forward-only: old
  // windows have no op_group yet, so normalized can legitimately be
  // empty (the page renders an honest empty state, not a blank panel).
  serviceOperations: (svc: string, r: RangeParams, normalized = false, compare = false) =>
    get<OperationSummary[] | null>(
      `/api/services/${encodeURIComponent(svc)}/operations?${qs(r)}${normalized ? '&normalized=1' : ''}${compare ? '&compare=prior' : ''}`),
  // serviceBundle — single round trip that returns the three
  // panels the Service detail mount needs (KPI summary,
  // recent problems, operations table). Server fans out to
  // CH in parallel goroutines; cached 15s. Replaces the
  // legacy three-call Promise.all on Service.tsx mount.
  // v0.5.300 — `refresh: true` appends `?refresh=1`, which the
  // server's serveCached middleware honors as "skip cache, force
  // recompute". Used as a one-shot rescue when the page detects
  // an empty operations array on a service that clearly has
  // traffic — see Service.tsx auto-refresh path.
  serviceBundle: (svc: string, r: RangeParams, opts: { refresh?: boolean } = {}) => {
    const params = opts.refresh ? { ...r, refresh: 1 } : r;
    return get<{
      service:    Service | null;
      problems:   import('./types').Problem[] | null;
      operations: OperationSummary[] | null;
      deploys:    import('./types').Deploy[] | null;
      // v0.9.377 (redesign D1) — Overview Top endpoints kartı: giriş
      // span'leri (HTTP + RPC birleşik), calls×avg sıralı top-12.
      // Eski backend'de alan yoktur → UI OpsCard'a düşer.
      endpoints?: import('./types').EndpointRow[] | null;
    }>(`/api/services/${encodeURIComponent(svc)}/bundle?${qs(params)}`);
  },
  // Annotation şeridi — sayfa başına TEK birleşik olay çağrısı
  // (v0.9.394 Ş1; deploy+rollout+alarm tetik/çözülme+anomali+operatör
  // olayları, ts-sıralı, 500 tavan + truncated).
  annotations: (service: string, fromNs: number, toNs: number) =>
    get<import('./types').AnnotationsResponse>(
      `/api/annotations?service=${encodeURIComponent(service)}&from=${fromNs}&to=${toNs}`),

  // Errors Inbox (state-tracked exception groups). v0.5.95 switched
  // the response shape from a bare array to { items, total, limit,
  // offset } so the UI can paginate without losing the global count.
  // sort/dir/q (v0.8.318) — ordering + substring search run server-side
  // across the WHOLE paginated set (whitelisted columns backend-side).
  exceptionGroups: (params: { state?: string; service?: string; assignee?: string; ownerTeam?: string; sreTeam?: string; sort?: string; dir?: string; q?: string; limit?: number; offset?: number;
    // v0.9.315 (operatör) — occurrence floor. One-off exceptions (a
    // single Java socket timeout) rendered rows indistinguishable from
    // sustained outages. Omitted = no floor.
    minOccurrences?: number }) =>
    get<{ items: ExceptionGroup[]; total: number; limit: number; offset: number }>(`/api/exception-groups?${qs(params)}`),
  // getExceptionGroup — point lookup by fingerprint, used to resolve a
  // shared /problems?exc=<fp> link when the group isn't on the
  // requester's currently-loaded page/filter. Throws on 404 (see
  // request()); callers treat any rejection as "not found".
  getExceptionGroup: (fingerprint: string) =>
    get<ExceptionGroup>(`/api/exception-groups/${encodeURIComponent(fingerprint)}`),
  // v0.9.463 (dürüstlük A11) — zarf: scanned/scanCapped ile boş liste
  // "örnek yok" mu "aday penceresi yetmedi" mi ayırt edilir.
  // v0.9.795 — tarama partili; scanned KÜMÜLATİF, scanCapped yalnız 5000
  // aday tavanına çarpınca true, windowExhausted grubun kendi penceresinin
  // sonuna kadar okunduğunu söyler (dürüst-boş).
  exceptionGroupSamples: (fingerprint: string, limit = 10) =>
    get<{ samples: ExceptionSample[]; scanned: number; scanCapped: boolean; windowExhausted?: boolean } | null>(`/api/exception-groups/${fingerprint}/samples?limit=${limit}`),
  exceptionGroupOccurrences: (fingerprint: string) =>
    get<OccurrencePoint[] | null>(`/api/exception-groups/${fingerprint}/occurrences`),
  // v0.9.252 — bulk sibling. One request, one audit row, one set of
  // cache invalidations for a whole triage gesture; returns partial
  // counts so the UI can report "42 acknowledged, 3 failed".
  setExceptionGroupStatesBulk: (fingerprints: string[], state: ExceptionGroupState) =>
    request<{ applied: number; failed: number }>(`/api/exception-groups/state`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ fingerprints, state }),
    }),
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

  // env (v0.8.387) — the global Topbar picker, service-scoped on
  // problems: the server keeps rows whose service ran in the env in
  // the last hour (plus service-less global alerts).
  // v0.9.455 — dürüstlük zarfı: {items,total,truncated}. total=-1 =
  // bilinmiyor (takım/cluster daraltması SQL COUNT'a inemiyor).
  problems: (params: { status?: string; service?: string; severity?: string; priority?: string[]; ownerTeam?: string; sreTeam?: string; env?: string; cluster?: string; limit?: number }) =>
    get<{ items: Problem[]; total: number; truncated: boolean } | null>(`/api/problems?${qs({ ...params, priority: params.priority?.join(',') })}`),
  // v0.5.398 — sidebar-badge count endpoint. Returns just the
  // matching row count, no rows. Replaces the prior approach
  // of fetching limit=200 and counting the array — the badge
  // capped at 200 silently on installs with >200 open problems.
  problemsCount: (params: { status?: string; service?: string; severity?: string; env?: string } = {}) =>
    get<{ count: number }>(`/api/problems/count?${qs(params)}`),
  // v0.9.550 — evaluator kalp atışı (worker pod'u Redis'e yazar,
  // API okur). Filtre YOK: sağlık her filtreden bağımsızdır ve
  // parametre eklemek gereksiz cache parçalanması olurdu.
  evaluatorHealth: () => get<EvaluatorHealth>('/api/problems/evaluator'),

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
  // v0.6.30 — burn-down forecast. window default 1h.
  sloForecast: (id: string, window = '1h') =>
    get<{
      burnRate: number;
      burnWindowSec: number;
      budgetRemaining: number;
      hoursToExhaust: number;
      willBreachWithin24h: boolean;
      safeBurn: boolean;
    }>(`/api/slos/${id}/forecast?window=${window}`),
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
  // ES Watcher import (Faz-1) — `watchText` is the operator's LITERAL
  // textarea paste: the raw string travels verbatim inside a JSON
  // string (review F5 — a client-side parse→stringify round-trip
  // silently rounds integers above 2^53 and reformats the
  // definition). The server normalizes Kibana DevTools
  // """triple-quoted""" strings and stores the normalized JSON.
  // dryRun previews the mapping report without persisting anything.
  importWatcher: (body: { name: string; watchText: string; dryRun: boolean }) =>
    request<WatcherImportResult>('/api/watchers/import', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  // /watchers page (v0.9.196) — per-rule problems rollup for the list
  // (60s server cache) + one rule's fire/notify/resolve history for
  // the drawer (30s server cache; fetch on OPEN only, never per-row).
  watchersSummary: () =>
    get<Record<string, WatcherSummaryEntry>>('/api/watchers/summary'),
  watcherHistory: (id: string) =>
    get<WatcherHistory>(`/api/watchers/${encodeURIComponent(id)}/history`),

  // ── Runbooks (v0.7.0) ──────────────────────────────────────────────────────
  runbooks: () => get<Runbook[] | null>('/api/runbooks'),
  runbook: (id: string) => get<Runbook>(`/api/runbooks/${id}`),
  createRunbook: (rb: Partial<Runbook>) =>
    request<Runbook>('/api/runbooks', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(rb),
    }),
  updateRunbook: (id: string, rb: Partial<Runbook>) =>
    request<Runbook>(`/api/runbooks/${id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(rb),
    }),
  deleteRunbook: (id: string) =>
    request<void>(`/api/runbooks/${id}`, { method: 'DELETE' }),
  enableRunbook: (id: string) =>
    request<void>(`/api/runbooks/${id}/enable`, { method: 'POST' }),
  disableRunbook: (id: string) =>
    request<void>(`/api/runbooks/${id}/disable`, { method: 'POST' }),
  // Runbook executions (v0.7.0)
  executeRunbook: (id: string, problemId?: string) =>
    request<RunbookExecution>(`/api/runbooks/${id}/execute`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(problemId ? { problemId } : {}),
    }),
  runbookExecutions: (params?: { runbookId?: string; status?: string; problemId?: string; limit?: number }) => {
    const qs = new URLSearchParams();
    if (params?.runbookId) qs.set('runbookId', params.runbookId);
    if (params?.status)    qs.set('status', params.status);
    if (params?.problemId) qs.set('problemId', params.problemId);
    if (params?.limit)     qs.set('limit', String(params.limit));
    const q = qs.toString();
    return get<RunbookExecution[] | null>(`/api/runbooks/executions${q ? '?' + q : ''}`);
  },
  runbookExecution: (execId: string) =>
    get<RunbookExecution>(`/api/runbooks/executions/${execId}`),
  runbookStepAction: (execId: string, stepId: string, action: 'complete' | 'skip' | 'fail', note?: string) =>
    request<RunbookExecution>(`/api/runbooks/executions/${execId}/steps/${stepId}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ action, note }),
    }),
  cancelRunbookExecution: (execId: string) =>
    request<RunbookExecution>(`/api/runbooks/executions/${execId}/cancel`, { method: 'POST' }),

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
  ldapInspect: (username: string) =>
    get<{
      dn: string; deepestOu: string; team: string;
      attributes: Record<string, string[]>;
      // v0.8.523 — attribute başına tıkla-seç ekip adayları.
      teamCandidates?: Record<string, { pattern: string; extracted: string; label: string }[]>;
    }>(
      `/api/settings/ldap/inspect?username=${encodeURIComponent(username)}`),
  getTeamContacts: () => get<import('./types').TeamContacts>('/api/settings/team-contacts'),
  // v0.9.427 — LDAP↔telemetri takım alias tablosu.
  getTeamAliases: () => get<import('./types').TeamAliases>('/api/settings/team-aliases'),
  putTeamAliases: (ta: import('./types').TeamAliases) =>
    request<import('./types').TeamAliases>('/api/settings/team-aliases', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(ta),
    }),
  putTeamContacts: (tc: import('./types').TeamContacts) =>
    request<import('./types').TeamContacts>('/api/settings/team-contacts', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(tc),
    }),
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
  // Admin "factory reset" — TRUNCATE all observability data (config preserved).
  // v0.8.413 — a factory reset on a big install runs minutes of ON
  // CLUSTER TRUNCATEs; 5-min client window (the server detaches from
  // the request anyway, so an abort can no longer half-purge).
  purgeTelemetry: () =>
    request<PurgeResult>('/api/admin/purge-telemetry', { method: 'POST', timeoutMs: 300_000 }),
  // setUserCustomRole assigns or clears the custom-role pointer
  // (v0.5.251). Only valid when the base role is viewer; server
  // rejects with 400 otherwise. Empty string clears.
  setUserCustomRole: (id: string, customRole: string) =>
    request<{ customRole: string }>(`/api/users/${id}/custom-role`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ customRole }),
    }),
  // Custom-role catalog (admin only). The /api/admin/pages endpoint
  // is the single source of truth for what page IDs are pickable —
  // the Settings → Roles checkbox grid is populated from it.
  listCustomRoles: () =>
    request<{ roles: CustomRole[] }>(`/api/admin/custom-roles`),
  upsertCustomRole: (role: CustomRole) =>
    request<CustomRole>(`/api/admin/custom-roles`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(role),
    }),
  deleteCustomRole: (name: string) =>
    request<void>(`/api/admin/custom-roles/${encodeURIComponent(name)}`, {
      method: 'DELETE',
    }),
  listAvailablePages: () =>
    request<{ pages: AvailablePage[] }>(`/api/admin/pages`),
  // v0.5.329 — ClickHouse self-stats: slow queries, in-flight
  // merges, part hotspots, replication lag. Powers /admin/clickhouse.
  clickhouseHealth: () =>
    get<{
      slowQueries: Array<{ query: string; elapsedMs: number; memoryMb: number; readRows: number; resultRows: number; eventTimeNs: number; user: string }> | null;
      merges:      Array<{ database: string; table: string; elapsedSec: number; progressPct: number; rowsRead: number; mergedSizeBytes: number }> | null;
      partHotspots: Array<{ database: string; table: string; parts: number; rowsTotal: number; bytesTotal: number }> | null;
      replicationLag?: Array<{ database: string; table: string; queueSize: number; absoluteDelaySec: number }> | null;
      generatedAt: number;
    }>(`/api/admin/clickhouse`),
  // v0.9.494 — koordinatör dağılımı. Hangi CH node'u kaç sorgunun
  // GİRİŞ NOKTASI oldu (system.query_log is_initial_query=1).
  // Taramanın nerede yapıldığını değil, fan-out + merge + final
  // aggregation yükünün nerede biriktiğini ölçer. windowS yalnız
  // sabit basamaklardan gelir (1h/6h/24h) — sunucu cache anahtarına
  // giren her parametrenin kardinalitesi sınırlı olmalı (v0.8.270).
  // v0.9.543 — node iş dağılımı: CPU · merge · insert · fetch host
  // başına, HAM kümülatif. Pencereyi istemci açar (lib/chNodeWork):
  // 5 günlük ortalama son saatlerdeki rejim değişimini seyreltiyor.
  chNodeWork: () =>
    get<{
      nodes: import('./chNodeWork').NodeWorkRaw[];
      mode: 'cluster' | 'standalone';
      source: 'events' | 'none';
      shardsKnown: boolean;
      expectedNodes: number;
      note?: string;
      generatedAt: number;
    }>('/api/admin/clickhouse/nodework'),
  /** v0.9.613 — DDL kuyruğu teşhisi. Verdict + eylem cümlesi sunucudan. */
  chDDLQueue: () => get<import('./types').DDLQueueHealth>('/api/admin/clickhouse/ddl-queue'),

  // ── v0.9.770 — rollup kurulum sihirbazı ────────────────────────
  // Dördü de admin. Okumalar CACHE'SİZ (sunucu tarafında da): "Kur"a
  // bastıktan saniyeler sonra basılan "Yenile" bayat bir gövde
  // döndürürse operatör kurulumun tutmadığını sanar.
  /** Canlı durum: hangi rollup tablosu var, kaç satır, en eski ts. */
  rollupStatus: () =>
    get<import('./types').RollupStatusResult>('/api/admin/rollup/status'),
  /** Ön kontrol — hiçbir şey yazmaz. Supported hükmü + gerekçe. */
  rollupPreflight: () =>
    get<import('./types').RollupPreflightResult>('/api/admin/rollup/preflight'),
  /** DDL'i koşar. İlk hatada durur — kaskad zaten çöker, 12 satır
   *  hata gerçek ilk nedeni gömerdi.
   *  timeoutMs 330s: ON CLUSTER DDL dağıtık kuyruğa girer ve her ifade
   *  distributed_ddl_task_timeout (varsayılan 180s) kadar bekleyebilir.
   *  Sunucu tarafı 5 dk'da keser; istemci ondan SONRA pes etmeli, yoksa
   *  operatör "zaman aşımı" görür ama DDL aslında koşmaya devam eder. */
  rollupApply: (cluster: string, target: import('./types').RollupTarget) =>
    request<import('./types').RollupActionResult>('/api/admin/rollup/apply', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ cluster, target }),
      timeoutMs: 330_000,
    }),
  /** YALNIZ MV'leri düşürür (yazımı kes); tablolar ve veri kalır. */
  rollupRollback: (cluster: string, target: import('./types').RollupTarget) =>
    request<import('./types').RollupActionResult>('/api/admin/rollup/rollback', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ cluster, target }),
      timeoutMs: 210_000,
    }),
  chCoordinators: (windowS: number) =>
    get<{
      nodes: Array<{
        host: string; initial: number; selects: number; inserts: number; other: number;
        readRows: number; memoryMB: number; p50Ms: number; p95Ms: number;
        uptimeS?: number;
      }>;
      mode: 'cluster' | 'standalone';
      windowS: number;
      selectImbalance: number;
      insertImbalance: number;
      initialImbalance: number;
      // v0.9.502 — "query_log" (pencereli) | "events" (açılıştan beri
      // kümülatif) | "none" (ikisi de okunamadı). Prod'da query_log
      // çoğu kurulumda kapalı; UI hangi kaynağı okuduğunu söylemeli
      // yoksa kümülatif sayıyı pencereli sanır.
      source: 'query_log' | 'events' | 'none';
      note?: string;
      generatedAt: number;
    }>(`/api/admin/clickhouse/coordinators?windowS=${windowS}`),
  // Cluster membership — v0.5.253. Lists every replica that
  // wrote a heartbeat in the last 30s. Single-instance mode
  // returns one member; HA returns N. Cheap (single SCAN +
  // MGET); safe to poll at 5-10s in the admin page.
  listClusterMembers: () =>
    request<{ members: ClusterMember[]; selfId: string }>(`/api/admin/cluster`),
  // Pipeline rules — operator-defined drop / enrich applied
  // BEFORE the sampler at OTLP ingest (v0.5.263).
  listPipelineRules: () =>
    request<{ rules: PipelineRule[] }>(`/api/admin/pipeline-rules`),
  upsertPipelineRule: (r: PipelineRule) =>
    request<PipelineRule>(`/api/admin/pipeline-rules`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(r),
    }),
  deletePipelineRule: (id: string) =>
    request<void>(`/api/admin/pipeline-rules/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
  // copilotNLToQuery — v0.5.255 natural-language → DSL converter.
  // Operator types "yesterday's slow checkouts"; we return the
  // filter set + time range the SPA applies to /explore. Server
  // validates ops + presets so a hallucinated payload never
  // reaches the FilterBuilder.
  copilotNLToQuery: (prompt: string) =>
    request<{
      filters: { k: string; op: string; v: string[] }[];
      range: { preset: string };
      explain: string;
      warning?: string;
      raw?: string;
    }>(`/api/copilot/nl-to-query`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ prompt }),
    }),

  // v0.6.8 — admin-only CH query AI optimizer. Operator pastes
  // raw CH SQL; server rewrites it to comply with Coremetry's
  // hard-constraint checklist (MV bypass, LIMIT, settings,
  // time-bounded WHERE) and returns {optimized, explanation}.
  optimizeCHQuery: (query: string) =>
    request<{
      optimized: string;
      explanation: string;
      warning?: string;
      raw?: string;
    }>(`/api/admin/clickhouse/optimize-query`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ query }),
    }),

};

// op — backend'in Op kümesiyle birebir (internal/pipeline/pipeline.go).
// '=~' RE2 ve ANKORSUZ (v0.9.797 motoru); UI'a v0.9.802'de açıldı — motor
// destekliyordu ama dropdown sunmadığı için operatör contains ile idare
// ediyordu.
export type PipelineCondOp = '=' | '!=' | 'contains' | 'startsWith' | 'endsWith' | '=~';

export interface PipelineCondition {
  key: string;
  op: PipelineCondOp;
  value: string;
}

export interface PipelineRule {
  id: string;
  name: string;
  kind: 'drop' | 'enrich' | 'sample';
  signal: 'spans' | 'logs' | 'metrics';
  enabled: boolean;
  when: PipelineCondition;
  // and — EK koşullar; hepsi `when` ile BİRLİKTE sağlanmalı (motor:
  // internal/pipeline/pipeline.go Rule.And, v0.9.797). Türetilmiş
  // "metric-excl-*" kuralları bunu taşır: `when: http.route =~ …` +
  // `and: metric = X`. v0.9.803'e kadar tipte YOKTU, dolayısıyla düzenleme
  // modalı gövdeyi sıfırdan kurarken koşulu sessizce düşürüyor ve tek bir
  // metrik için kurulan dışlamayı BÜTÜN metriklere yayıyordu.
  and?: PipelineCondition[];
  // Enrich rules only — resource attribute key/value pairs to
  // set when the predicate matches. Existing keys are
  // overridden, new keys append.
  setAttributes?: Record<string, string>;
  // Sample rules only — probability in [0, 1] of keeping the
  // span when the predicate matches. 1.0 = always keep
  // (no-op), 0.0 = always drop (use a drop rule instead).
  rate?: number;
}

export interface ClusterMember {
  id: string;          // pod id (hostname + 4-byte hex suffix)
  hostname: string;    // raw $HOSTNAME
  version: string;     // build tag stamped via -ldflags
  startedAt: number;   // unix ns
  lastSeen: number;    // unix ns
  isThisPod: boolean;  // true for the pod that served the request
  leaderLocks?: string[]; // only present when this pod holds active locks
}

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

export interface AuthUser {
  id: string;
  email: string;
  role: string;
  // Custom-role pointer (v0.5.251). Only set when role === 'viewer'
  // AND an admin has assigned an existing custom role. The resolved
  // page list is shipped alongside so the SPA filters the sidebar +
  // route guard without a second fetch.
  customRole?: string;
  customRolePages?: string[];
  // v0.8.238 — true when an LDAP directory photo is stored for this
  // user; gates the <img src=".../photo"> so local/OIDC accounts
  // render the initials fallback without a guaranteed-404 request.
  hasPhoto?: boolean;
  // v0.8.266 — directory identity, refreshed on each LDAP login:
  // displayName → fullName, company/o → org (department/ou lands in
  // the team field on UserRow). Empty for local/OIDC accounts.
  fullName?: string;
  org?: string;
  // v0.9.528 — hitap adı, SUNUCUDA türetilir (internal/api/greeting.go).
  // fullName bileşik olabilir ("Ad Soyad (Bölüm) * ÜNVAN-Ekip") ve
  // ham hâli karşılamada kullanılamaz. Absent = güvenle ad
  // çıkarılamadı; isimsiz karşılamaya düşülür.
  firstName?: string;
}
export interface UserRow extends AuthUser {
  disabled: boolean;
  authProvider: string;  // 'local' | 'oidc'
  team: string;          // free-text grouping label, '' when unassigned
  createdAt: number;     // unix ns
  // v0.8.403 — presence. online = any authenticated API activity in
  // the last 5 minutes (open tabs poll, so logged-in ≈ online).
  // lastSeenAt is only present while the Redis stamp is live (TTL =
  // the online window); absent = never seen / presence unavailable.
  online?: boolean;
  lastSeenAt?: number;   // unix ns
  // v0.8.450 — son başarılı LOGIN anı (kalıcı, users tablosunda;
  // lastSeenAt'in Redis-TTL'li aktivite damgasından farklı). 0 /
  // absent = hiç giriş yapmadı.
  lastLoginAt?: number;  // unix ns
}
export interface CustomRole {
  name: string;
  pages: string[];   // sidebar route paths (e.g. '/inbox')
}
export interface AvailablePage {
  id: string;        // route path / Sidebar href
  label: string;     // i18n key
  group: string;     // group heading i18n key ('' for ungrouped)
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
  // v0.9.279 — DB receiver drill scoping. Not a normal filter: service_name
  // names the RECEIVER, so every instance of an engine shares it; the
  // discriminating key differs per receiver family and the backend compiles
  // the pair into an OR (dbInstanceScopeClause).
  instance?: string;
  engine?: 'oracle' | 'postgresql' | 'mysql' | 'redis';
  filters?: string;     // JSON FilterExpr[]
  groupBy?: string;     // comma-sep
  agg?: string;         // avg | sum | min | max | last | p50 | p95 | p99
  from?: number;
  to?: number;
  step?: number;
  // v0.9.105 (F1 display fidelity) — panel px width ≈ hedef bucket sayısı;
  // step auto iken piksel-adaptif çözünürlük sürer (geniş pencerede 1s
  // verinin ~5s'e inmesi, eski 30s ladder yerine). metricQuery default 1500
  // enjekte eder (cache-dostu sabit); çağıran özel panel genişliği geçebilir.
  maxDataPoints?: number;
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
  // v0.6.32 — free-text search predicate. Same shape as the
  // /traces page's search field; pushed down to span-level
  // WHERE so a histogram's total matches the table's
  // search-narrowed list.
  search?: string;
  // filterGroup — grouped AND/OR builder JSON (v0.8.x gap-2, extended into
  // Explore). When present it SUPERSEDES `filters` server-side; a flat-AND
  // group is byte-identical to the legacy filters path, so passing it is
  // purely additive. Omitted → byte-identical query string + cache key to the
  // pre-group call (qs() drops undefined), so existing callers are untouched.
  filterGroup?: string; // JSON-encoded FilterGroup
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
  // env — global Topbar environment filter (?env=, v0.8.383). First-class
  // param (NOT an injected FilterExpr) so it survives the backend's
  // filterGroup-supersedes-filters rule.
  env?: string;
  filters?: string;     // JSON-encoded FilterExpr[]
  // filterGroup — grouped AND/OR builder JSON (v0.8.x gap-2). When present it
  // SUPERSEDES `filters` server-side; a flat-AND group is byte-identical to
  // the legacy filters path, so passing it is purely additive.
  filterGroup?: string; // JSON-encoded FilterGroup
  sort?: string;
  order?: SortOrder;
  limit?: number;
  // having — v0.8.453 (B2-c): JSON-encoded HavingRow[] (lib/havingParam.ts).
  // Post-aggregate; whitelist sunucuda da doğrulanır (400).
  having?: string;
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
  // env — global Topbar environment filter (?env=, v0.8.383). First-class
  // param (NOT an injected FilterExpr) so it survives the backend's
  // filterGroup-supersedes-filters rule.
  env?: string;
  filters?: string;     // JSON-encoded FilterExpr[]
  // filterGroup — grouped AND/OR builder JSON (v0.8.x gap-2). Supersedes
  // `filters` server-side when present; flat-AND is byte-identical so this is
  // additive and existing callers that omit it are unaffected.
  filterGroup?: string; // JSON-encoded FilterGroup
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
  cluster?: string;  // v0.5.471 — k8s/openshift cluster name
  env?: string;      // v0.8.400 — global ?env= deployment-environment filter
  search?: string;
  severity?: number;
  traceId?: string;
  spanId?: string;
  // hasTrace (v0.8.406) — only logs with a trace correlation
  // (CH trace_id != '' / ES exists on the trace field shapes).
  hasTrace?: boolean;
  from?: number;
  to?: number;
  limit?: number;
  // offset stays for back-compat (honored server-side only when
  // `after` is empty), but the UI no longer drives it — cursor
  // paging replaced the offset pager in v0.7.22.
  offset?: number;
  // after = opaque keyset cursor. Omit/empty for the first page;
  // pass the previous response's nextCursor verbatim to fetch the
  // next page. Backend-owned format (CH base64 / ES search_after);
  // treat as an opaque token. qs() drops it when empty so the
  // first page is a no-cursor request.
  after?: string;
  // asc (v0.9.295) — oldest-first. Both backends have supported the
  // direction since v0.7.83 but only the Context modal ever asked for
  // it. The cursor carries its own direction, so a token minted in one
  // order is dropped rather than replayed in the other — which is why
  // the UI must reset paging when this flips.
  asc?: boolean;
  // paging (v0.9.286) — "I will use nextCursor". The ES backend keeps a
  // Point-in-Time alive whenever it returns a cursor, so a caller that
  // reads one page and walks away pins segment readers for 2 minutes.
  // Set by useLogs (the interactive /logs list, the only surface with a
  // Load-more); the trace drawers, the service Logs tab and span detail
  // deliberately omit it.
  paging?: boolean;
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
