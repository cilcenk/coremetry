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
  // v0.7.32 — for a collapsed broadcast queue node (a kafka topic with
  // >threshold distinct consumers, e.g. cache.refresh), the real consumer
  // count its fan-out was hidden behind. UI shows "→ N services (broadcast)"
  // on the node instead of N edges; only set on collapsed queue nodes.
  broadcastFanout?: number;
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
  // v0.7.32 — number of broadcast queue topics whose consumer fan-out was
  // collapsed by default. >0 → the UI shows a "N broadcast topics collapsed —
  // show" toggle that flips ?broadcast=show to reveal the full mesh.
  broadcastCollapsed?: number;
}

// OTel-native service graph (v0.8.10 — topology rebuild). One compact
// {nodes,edges} payload from /api/servicegraph, built server-side off the
// topology_edges_5m MV (no raw-span scan). Node kind is decoded from the MV's
// structured node_kind (db.system/messaging.system origin) — the client never
// does the old "db:h2" prefix-strip. Consumed by the canonical ServiceGraph.
export type GraphNodeKind = 'service' | 'database' | 'queue' | 'external' | 'internal';
export interface GraphNode {
  id: string;          // canonical id (raw MV name, e.g. "payments" / "db:h2")
  name: string;        // display name, prefix-decoded
  kind: GraphNodeKind;
  system?: string;     // db.system / messaging.system
  dbName?: string;     // db.name (schema/instance) — database nodes only
  env?: string;
  calls: number;
  errors: number;
  errorRate: number;   // (errors/calls)*100 — health color
  rate: number;        // calls per minute over the window — node-size encoding
  // v0.9.367 — 'outbound' = giriş-servis fallback'i: enstrümante çağıranı
  // yok, sayılar bağımlılıklarının döndürdükleri. UI etiketi ayırır.
  callsBasis?: 'inbound' | 'outbound';
  // v0.9.1026 — kuyruk düğümünün messaging cluster'ı (yalnız kind==='queue').
  // /messaging çekmecesinin kimliği (system, cluster, destination) üçlüsü;
  // bu alan gelmeden GERÇEK derin link kurulamıyordu.
  //
  // undefined/boş MEŞRU bir hâl (kolonun inmediği kurulum, v0.9.1025
  // öncesi kovalar, kuyruk olmayan düğüm) ve '(default)' diye
  // TAMAMLANMAMALI: çok-cluster kurulumda uydurma bir cluster çekmeceyi
  // sessizce BOŞ açar (v0.9.973). O hâlde katalog köprüsü kullanılır.
  cluster?: string;
}
export interface GraphEdge {
  source: string;
  target: string;
  calls: number;
  errors: number;
  errorRate: number;
  rate: number;        // calls per minute over the window
  avgMs: number;
  p99Ms: number;
  protocol?: string;   // http | grpc | db | kafka — SpanKind proxy
}
export interface ServiceGraphResponse {
  nodes: GraphNode[];
  edges: GraphEdge[];
  scope: string;       // 'global' | 'neighborhood'
  focus?: string;
  // v0.8.295 (global render budget): shownNodes < totalNodes ⇒ the server
  // trimmed the global graph to the topN heaviest — "showing X of Y".
  totalNodes?: number;
  shownNodes?: number;
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
  // v0.7.39 — total distinct flows in the window (the list is capped at ?top).
  // >flows.length → UI shows "showing N of M flows — raise top".
  totalFlows?: number;
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
  // v0.9.262 — same TDigest state as p99, indices 1 and 2. Optional: a warm
  // cached payload from a pre-v0.9.262 backend lacks them mid-rolling-deploy,
  // and receiver-discovered rows (source='receiver') have no quantiles at all.
  p50DurationMs?: number;
  p95DurationMs?: number;
  p99DurationMs: number;
  // v0.9.433 — ?compare=prior ikiz-pencere sayaçları; yalnız prior
  // ikizi eşleşen satırlarda gelir (omitempty sözleşmesi).
  priorSpanCount?: number;
  priorErrorCount?: number;
  priorAvgMs?: number;
  priorP50Ms?: number;
  priorP99Ms?: number;
  callers: string[];
  // Source: empty / 'spans' = derived from application traffic
  // (the default). 'receiver' = discovered via an OpenTelemetry
  // database receiver (e.g. oracledb) with no application spans
  // yet — RED stats are zero, drill-down opens the receiver
  // panel directly.
  source?: 'spans' | 'receiver';
}

// DBTrendPoint — one 5-minute bucket of a database's RED trend,
// aligned to the db_summary_5m time_bucket grid. t is unix ns at
// the bucket start. rps is spans/sec (span_count / 300), errorRate
// is 0..100, p99Ms is the merged p99 in milliseconds.
export interface DBTrendPoint {
  t: number;          // unix ns — bucket start
  rps: number;        // call rate: span_count / 300
  errorRate: number;  // 0..100
  p99Ms: number;      // p99 duration, ms
}

// DBTrend — per-row sparkline (#1) + latest-bucket health snapshot
// (#6) for the /databases + /messaging overview grid. Keyed
// identically to DBInstance / the DepRow join key:
// (dbSystem, instance, dbName, cluster). cluster is empty for
// DB rows (no cluster dimension); it rides the shape so the same
// type can serve the messaging grid join. The component joins
// trends → rows by matching (system, instance, dbName).
//
// points is ascending-time (one entry per 5-minute bucket the
// window covers). The cur* fields are the latest non-empty
// bucket's snapshot — the per-row gauge source.
export interface DBTrend {
  dbSystem: string;
  instance: string;
  dbName: string;
  cluster: string;
  points: DBTrendPoint[];
  // v0.9.820 — cur* artık son TAM kovadan. Eskiden son kovaydı ve canlı
  // bir pencerede o kova DOLUYOR: rozet her yenilemede "trafik durdu /
  // gecikme düzeldi" diye parlıyor, operatör kendi kendine düzelen bir
  // sistem görüyordu.
  curRps: number;
  curErrorRate: number;  // 0..100
  curP99Ms: number;
  /** Pencerede tek bir TAM kova bile yoktu — rozet dolmakta olandan. */
  curFromPartial?: boolean;
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
  // v0.9.273 — p50 completes the grid; it was missed when p95 landed, so the
  // drawer showed three percentiles in its aggregate strip and two per row.
  p50DurationMs?: number;
  // v0.9.263 — p95 off the same merge. Optional: BOTH producer queries
  // (databases + messaging callers) project it, but a warm cached
  // payload from an older backend will not.
  p95DurationMs?: number;
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
  // v0.9.263 — same merge as p99, indices 1 and 2. Optional for the
  // same rolling-deploy reason as MessagingDetail below.
  p50DurationMs?: number;
  p95DurationMs?: number;
  p99DurationMs: number;
  callers: DBCallerBreakdown[];
  topOps: DBOpStat[];
}

// DBWaitLock (v0.8.391) v0.9.852'de SİLİNDİ — /api/databases/waitlock
// ucu, chstore okuyucusu ve WaitLockStrip bileşeniyle birlikte. Operatör:
// "wait lock'ı kaldırabilirsin — db metriklerini almıyorum". Geri dönüş
// git geçmişinden.

export interface MessagingDetail {
  system: string;
  cluster: string;
  destination: string;
  // v0.9.973 — cluster GÖNDERİLMEDİ, sunucu "(default)" VARSAYDI.
  // Cluster yüklemi tam eşitlik olduğu için çok-cluster kurulumda bu
  // varsayım, canlı bir topic için SIFIRLANMIŞ çekmece üretir — ve
  // sıfırlanmış çekmece "bu topic boşta" ile ayırt edilemez. Bayrak
  // ikisini ayırt ettirir. omitempty: yokluğu "varsayım yok" demek.
  assumedCluster?: boolean;
  spanCount: number;
  errorCount: number;
  errorRate: number;
  avgDurationMs: number;
  // v0.9.263 — same merge as p99. Optional: a pre-v0.9.263 backend
  // omits them mid-rolling-deploy, and the drawer renders "—".
  p50DurationMs?: number;
  p95DurationMs?: number;
  p99DurationMs: number;
  callers: DBCallerBreakdown[];
  topOps: DBOpStat[];
  // v0.8.364 (Stage-2 M1) — per-5-min produce/consume counts from
  // messaging_caller_summary_5m (kind × time_bucket dimensions).
  // Optional so a stale pre-M1 cached payload can't crash the
  // drawer mid-rolling-deploy.
  series?: MsgKindPoint[];
  // v0.8.372 (Stage-2 M2) — span_links-correlated end-to-end
  // produce→consume latency. Absent when the backend read failed
  // (or on a stale pre-M2 cached payload); present with
  // linkless=true when no links correlated in the window, so the
  // drawer can say "SDKs aren't emitting span links" instead of a
  // misleading 0ms.
  e2e?: MsgE2E;
}

// MsgKindPoint — one 5-minute bucket of the messaging drawer's
// produce/consume series (v0.8.364). timeS = bucket start, unix s.
export interface MsgKindPoint {
  timeS: number;
  produceCount: number;
  consumeCount: number;
}

// MsgE2E — end-to-end produce→consume latency for one messaging
// destination (v0.8.372, Stage-2 M2). Correlated via span_links:
// consumer spans link back to the producer span of the message they
// processed; lag = consumer start − producer end, clamped ≥ 0
// server-side (clock skew). slowest* carry the drawer's one exemplar
// pivot (→ /trace?id=<consumer trace>).
export interface MsgE2E {
  count: number;
  p50Ms: number;
  p95Ms: number;
  p99Ms: number;
  // True when zero pairs correlated in the window — the SDKs aren't
  // emitting messaging span links (honest empty state, not "0ms").
  linkless?: boolean;
  series: MsgE2EPoint[];
  slowestLagMs?: number;
  slowestConsumerTraceId?: string;
  slowestProducerTraceId?: string;
}

// MsgE2EPoint — one 5-minute bucket of the e2e series: correlated
// pair count + the bucket's average lag in ms (v0.8.372).
export interface MsgE2EPoint {
  timeS: number;
  count: number;
  avgMs: number;
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
  // topSQL — engine-authoritative heaviest statements from
  // pg_stat_statements (receiver-side parity with Oracle's V$SQL
  // TopSQL). Same row shape as OracleMetrics.topSQL so the
  // shared TopSQLTable renders all three engines. Empty when the
  // operator hasn't enabled the pg_stat_statements scrape.
  topSQL: { sql: string; elapsedSec: number; executions: number; avgElapsedMs: number }[];
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
  // topSQL — engine-authoritative heaviest statements from
  // performance_schema (events_statements_summary_by_digest).
  // Same row shape as OracleMetrics.topSQL so the shared
  // TopSQLTable renders it. Empty when the operator hasn't
  // enabled the performance_schema statement scrape.
  topSQL: { sql: string; elapsedSec: number; executions: number; avgElapsedMs: number }[];
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
  // v0.8.364 (Stage-2 M1) — full quantile grid off the existing
  // TDigest state (0.5/0.95/0.99). Optional: a warm cached payload
  // from a pre-M1 backend may lack them mid-rolling-deploy.
  p50DurationMs?: number;
  p95DurationMs?: number;
  // v0.8.364 — producer/consumer split (raw window counts; the
  // page divides by window minutes for the /min columns). Spans of
  // other kinds count toward spanCount but neither bucket.
  produceCount?: number;
  consumeCount?: number;
  produceErrors?: number;
  consumeErrors?: number;
  // v0.9.816 — gecikme ayrışması. Satırın tek P95'i üretici ve tüketici
  // span'lerini TEK dağılımda topluyordu; bunlar farklı işler (publish
  // vs process) ve karışık p95 yavaş tüketiciyi hızlı üreticinin içinde
  // saklıyordu. Kaynak messaging_caller_summary_5m'in zaten taşıdığı
  // TDigest state — ana MV'ye dokunulmadı, ek tur atılmadı.
  // omitempty: 0 ms ölçüm değil ölçüm YOKLUĞU → alan düşer, hücre '—'.
  produceP95Ms?: number;
  consumeP95Ms?: number;
  // Prior equal-length window — present only with compare=prior.
  priorSpanCount?: number;
  priorErrorCount?: number;
  priorProduceCount?: number;
  priorConsumeCount?: number;
  priorAvgMs?: number;
  priorP50Ms?: number;
  priorP99Ms?: number;
  callers: string[];
}

// MessagingOverview — /api/messaging'in zarfı (v0.9.813).
//
// Uç ÇIPLAK DİZİ döndürüyordu ve bir dizi "kesildim" diyemez: sunucu
// tarafındaki LIMIT 200 tamamen görünmez bir kesme noktasıydı, 1000
// topic'li bir kurulumda operatör 200 satır görüp listeyi TAM sanıyordu.
// rowsCapped o kesmeyi İLAN eder; rowLimit sayıyı taşır ki UI şeridi
// 200'ü hardcode etmesin.
export interface MessagingOverview {
  rows: MessagingInstance[];
  rowsCapped?: boolean;
  rowLimit?: number;
}

// DatabasesOverview — /api/databases zarfı (v0.9.821).
//
// ZARF NEDEN: eski uç ÇIPLAK DİZİ döndürüyordu ve bir dizi "kesildim"
// diyemez. Bu sayfada ÜÇ ayrı kesme noktası var (satırlar, receiver
// keşfi, çağıranlar) ve üçü de tamamen görünmezdi: tavan kadar satır
// dönen bir sayfa "estate'in tamamı bu" diye okunuyordu.
export interface DatabasesOverview {
  rows: DBInstance[];
  /** Satır okuması tavana dayandı — liste EKSİK olabilir. */
  rowsCapped?: boolean;
  rowLimit?: number;
  /** Receiver keşfi motor başına tavana dayandı. */
  receiversCapped?: boolean;
  receiverLimit?: number;
  /** "" = MV (varsayılan), "raw" = ham spans (env filtresi). */
  source?: string;
  /** Receiver paneli neden hiç doldurulmadı ("env"). Boş = doldu. */
  receiversSkipped?: string;
}

// BreakdownPoint — one bucket of the Elastic-APM-style "span
// breakdown" stacked-area chart. Cumulative ms of duration
// grouped by span category for the service detail page.
export interface BreakdownPoint {
  time: number;                   // unix ns (bucket start)
  kinds: Record<string, number>;  // category → ms summed in bucket
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

// RootCause — the assembled "what changed / likely cause" bundle for one
// Problem (v0.7.51 backend, v0.7.52 panel). The /api/problems/{id}/rootcause
// endpoint orchestrates signals that already exist but were scattered across
// pages — recent deploy, correlated service changes, dimension bubble-up,
// blast radius, an exemplar trace — into ONE cached read so the triage drawer
// shows a single root-cause surface. Every sub-field is best-effort: a partial
// bundle still helps triage, so the panel renders whatever is present.
export interface RootCause {
  problemId: string;
  service: string;
  metric: string;
  startedAt: number;          // unix ns
  fromNs: number;             // analysis window start (= startedAt)
  toNs: number;               // analysis window end (clamped 10m..1h)
  recentDeploy?: {
    version: string;
    timeUnixNs: number;
    ageSeconds: number;
  };
  correlations: ChangedService[];   // always present (possibly empty)
  blastRadius?: BlastRadius;
  // v0.9.1063 — hata problemlerinde aynı-pencere hata alt-kümesi;
  // gecikme/diğer ailelerde ZAMAN-KAYDIRMALI kıyas (baseline = önceki
  // eş-boy pencere).
  bubbleUp?: BubbleUpResult;
  exemplar?: SpanExemplar;
  // v0.9.1066 (Faz 3.1) — sentezleyicinin kalıcı hipotezi tek pakette
  // (adaylar+gerekçeler, deploy etkisi, temsilî trace, Deep + "neye
  // bakıldı" izi). Yok = worker henüz sentezlemedi.
  hypothesis?: RootCauseHypothesis;
}

// AnomalyRootCause — the anomaly-anchored sibling of RootCause. The
// /api/anomalies/{id}/rootcause endpoint embeds the SAME root-cause fan-out
// (deploy / correlations / blast-radius / bubble-up / exemplar) and stamps the
// AnomalyEvent anchor (id / kind / pattern) instead of a Problem. The
// RootCauseRibbon fetches this ON EXPAND for an anomaly row (the collapsed chip
// rides the list summary — no fetch on mount). Mirrors the Go AnomalyRootCause.
export interface AnomalyRootCause extends RootCause {
  anomalyId: string;
  anomalyKind: string;   // log_pattern | log_template_new | trace_op
  pattern: string;       // log pattern name OR operation name (trace_op)
}

// ── Root-cause hypothesis (rc #2/#3) ────────────────────────────────────────
// The PERSISTED, pre-computed root-cause ranking the worker synthesizes per
// anchor and the /anomalies + /problems lists join as a compact summary.

// ScoredCause — one ranked candidate cause. Mirrors the Go chstore.ScoredCause
// (and correlator.ScoredCause): service + blended score + hop distance + the
// optional propagation path + the human "why this rank" reason line.
export interface ScoredCause {
  service: string;
  score: number;
  hops: number;
  path?: string[];
  reason?: string;
}

// RootCauseHypothesis — the full persisted ranking for one anchor (mirrors Go
// chstore.RootCauseHypothesis). Not fetched directly by the ribbon; the expand
// reads the live /rootcause fan-out (AnomalyRootCause) instead. Defined here so
// the shape is documented + available if a future surface reads the raw row.
export interface RootCauseHypothesis {
  anchorKind: string;          // "anomaly" | "problem"
  anchorId: string;
  service: string;
  computedAt: number;          // unix ns
  topSuspect: string;          // "" = no clear cause
  topScore: number;
  confidence: number;          // 0..1
  candidates: ScoredCause[];
  recentDeploy?: {
    version: string;
    timeUnixNs: number;
    ageSeconds: number;
    // v0.9.1059 — deploy'un önce/sonra RED kıyası (yalnız hipotez yolu).
    impact?: DeployImpact;
  };
  // v0.9.1057 — anchor penceresinin temsilî trace'i.
  exemplarTraceId?: string;
  // v0.9.1066 — P1/deploy soruşturmasının kanıtı + denetim izi.
  deep?: DeepEvidence;
  version: number;
}

// CheckedSignal — "neye bakıldı" denetim izinin bir satırı (mirrors Go
// chstore.CheckedSignal). found=false bir kanıt DEĞİLDİR: bakıldı ve
// bulunamadı demektir — UI bu asimetriyi korumak zorunda.
export interface CheckedSignal {
  family: string;
  found: boolean;
  detail: string;
  records: number;
}

// DeepEvidence — P1/deploy soruşturmasının topladığı kanıt paketi
// (mirrors Go chstore.DeepEvidence; yalnız panelin okuduğu alanlar —
// aile dizileri büyük ve şimdilik yalnız checked + sayımlar çizilir).
export interface DeepEvidence {
  checked?: CheckedSignal[];
  exceptions?: unknown[];
  templates?: unknown[];
  heap?: unknown[];
  gcPause?: unknown[];
  slowOps?: unknown[];
  business?: Record<string, unknown[]>;
  codeMeaning?: Record<string, string>;
}

// ShiftSummary — GET /api/shift cevabı (v0.9.1072, Faz 3.2). Üç blok
// tek okumada; pencere sunucu-rung'lu (8h/12h/24h).
export interface ShiftSummary {
  windowSec: number;
  fromNs: number;
  toNs: number;
  problems: Problem[];               // pencerede açılan + çözülen (enriched)
  worsened: ChangedService[];        // pencere vs önceki eş-boy pencere
  newExceptions: ExceptionGroup[];   // first_seen pencerede (≤20)
  newExceptionsTotal: number;        // kesme ifşası
  problemsTotal: number;             // v0.9.1073 — kesme ifşası (≤100 gösterilir)
}

// RootCauseSummary — the COMPACT slice each /anomalies + /problems list row
// carries (mirrors Go chstore.RootCauseSummary) so the collapsed ribbon renders
// "Root cause: <suspect> (NN%)" without a per-row fetch. Backend omits it when
// the worker hasn't synthesized a hypothesis for the anchor (→ honest "no clear
// cause yet" ribbon).
export interface RootCauseSummary {
  topSuspect: string;
  topScore: number;
  confidence: number;          // 0..1 — low/zero ⇒ muted honest state
}

// RootCauseExplain — the optional Copilot PROSE narration on top of the
// deterministic ranking (rc #4). The ✨ Explain button in the expanded ribbon
// fetches this lazily on click (Copilot calls cost — never on mount/expand);
// the backend renders the PERSISTED hypothesis into 2-4 advisory sentences via
// s.copilotExplain. 404 when no hypothesis is synthesized yet (honest "no
// narration available" state — never fabricated).
export interface RootCauseExplain {
  /** v0.9.559 — null OLABİLİR: model hakemi yanıt üretemediyse sunucu
   *  prose'u DOLDURMAZ, deterministik cümleyi verdict.summary'ye yazar.
   *  Böylece "anlatım yok" dalı ulaşılabilir kalır; yedek cümleyi
   *  gerçek anlatımla aynı kutuda çizmek operatörü yanıltırdı. */
  prose: string | null;
  /** Kalkanlardan geçmiş yapılandırılmış karar (v0.9.559).
   *  Yoksa eski davranış: yalnız prose. */
  verdict?: RCAVerdict;
  /** v0.9.592 — bu CEVABIN kimliği; 👍/👎 bununla
   *  POST /api/ai/feedback'e gider (v0.8.399'dan beri çalışan ray).
   *
   *  Yoksa derecelendirme affordance'ı HİÇ ÇİZİLMEZ: 30dk'lık önbellek
   *  penceresi yüzünden eski bir gövde kimliksiz gelebilir ve tıklanınca
   *  hiçbir yere yazmayan bir düğme göstermek, bu dilimin düzeltmek için
   *  var olduğu hatanın ta kendisi olurdu. */
  exchangeId?: string;
}

/** RCA verdict — deterministik kalkanlardan geçmiş kök-neden kararı.
 *  Tasarım: docs/cosre-verdict-design.md */
export interface RCAVerdict {
  verdict: 'root_cause_identified' | 'probable_cause' | 'insufficient_evidence';
  title: string;
  summary: string;
  rootCause: {
    entity: string;
    failure_mode: string;
    trigger: string;
    latent_weakness: string;
    evidence: string[];
  };
  causalChain?: { entity: string; effect: string; evidence: string[] }[];
  rejectedHypotheses?: { hypothesis: string; refuted_by: string[]; reason: string }[];
  missingEvidence?: string[];
  remediation?: { kind: 'mitigate' | 'fix'; action: string; target: string; risk: 'low' | 'medium' | 'high' }[];
  /** Tavanlanmış nihai güven. */
  confidence: number;
  /** Modelin kendi beyanı — tavanın ne kadar indirdiği görünsün diye ayrı. */
  modelConfidence: number;
  /** Deterministik korelasyon motorunun güveni. Üçü FARKLI şeyler. */
  hypothesisConfidence: number;
  /** Atıf yapılan kanıtların SUNUCU metni (model metni değil). */
  evidence?: { id: string; kind: 'E' | 'N'; entity?: string; text: string }[];
  impact?: {
    entity: string;
    anchorService?: string;
    /** null = ÖLÇEMEDİK (sıfır DEĞİL). */
    errorCount: number | null;
    requestCount: number | null;
    errorShare: number | null;
    windowFromNs: number;
    windowToNs: number;
    note?: string;
  };
  shields: {
    /** false ⇒ deterministik düşüş. UI'DA GÖSTERİLMELİ, yoksa sahte bir
     *  "kanıt yetersiz" gerçeğinden ayırt edilemez. */
    parsed: boolean;
    repaired?: boolean;
    rejectedEvidence?: string[];
    unknownEntities?: string[];
    refutationInvalid?: boolean;
    confidenceCapped?: boolean;
    notes?: string[];
  };
}

// ── Correlated Signals (task #6) ────────────────────────────────────────────
// One pivot surface: given any single signal (trace / log / metric) the
// /api/correlate/context endpoint assembles the correlated OTHER two —
// trace ↔ logs ↔ metrics — joined on trace_id → service.name → time-window.
// This is synthesis over existing reads (mirrors RootCause's bundle-fan-out),
// not new capability. Drawer-first (no /correlate route in v1).
//
// HONESTY: the join key the drawer surfaces tells the operator which join the
// bundle used — exact (`trace_id`), a real representative `exemplar` (the
// metric→trace pivot for latency/error: the actual slow/error trace from the
// spanmetrics rollup), or the genuinely-fuzzy `service+window` (throughput/count
// metric, no representative span). The metric anchor is no longer deferred.
export type CorrelationKind = 'trace' | 'log' | 'metric';

// PivotAnchor — discriminated union on `kind`, the shape the drawer is opened
// with. Each variant carries enough context to derive the other two lenses.
export type PivotAnchor =
  | { kind: 'trace'; traceId: string; service?: string; fromNs?: number; toNs?: number }
  | { kind: 'log'; traceId?: string; service?: string; tsNs: number; fromNs?: number; toNs?: number }
  | { kind: 'metric'; service: string; tsNs?: number; metricKind?: 'error' | 'latency' | 'throughput'; fromNs?: number; toNs?: number };

// CorrelationAnchor — what the operator pivoted FROM, echoed back by the
// backend with the resolved window + the strongest join key it actually used.
export interface CorrelationAnchor {
  kind: CorrelationKind;
  traceId?: string;
  service?: string;
  tsNs?: number;
  fromNs: number;
  toNs: number;
  // joinKey, rendered as a visible chip by the drawer:
  //   'trace_id'       = exact cross-signal join (no time fuzz)
  //   'exemplar'       = a real representative trace (metric→trace pivot for
  //                      latency/error — exact-enough to pivot into)
  //   'service+window' = genuinely-fuzzy fallback (throughput/count metric)
  joinKey: 'trace_id' | 'exemplar' | 'service+window';
}

// CorrelationTrace — condensed trace lens (the timeline mini-waterfall reads
// `spans`; the header reads the scalars). Same shape TracePeekDrawer derives,
// but computed server-side so the drawer is one round-trip.
export interface CorrelationTrace {
  traceId: string;
  rootName: string;
  service: string;
  durationMs: number;
  spanCount: number;
  services: string[];
  errSpans: number;
  startTimeNs: number;
  endTimeNs: number;
  spans: SpanRow[];          // capped server-side
}

// CorrelationContext — the assembled pivot bundle. Every lens is best-effort:
// `logs`/`metrics` are always present (possibly empty); `trace`/`exemplar` are
// omitted when not derivable. Mirrors RootCause's partial-bundle posture.
export interface CorrelationContext {
  anchor: CorrelationAnchor;
  trace?: CorrelationTrace;
  logs: LogRow[];                 // trace_id join when present, else service+window
  metrics: SpanMetricSeries[];    // anchor service RED series (rate / error_rate / p99)
  exemplar?: SpanExemplar;        // metric anchor: a REAL representative trace to pivot INTO (rollup slow/error exemplar, raw-span fallback)
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
  // wf — master on/off toggle, DISTINCT from hasKey. Unchecking +
  // saving disables the Copilot (stops the background explainer,
  // hides AI affordances, 503s AI endpoints) WITHOUT clearing the
  // stored key. Backend defaults a missing field to true.
  enabled?: boolean;
}
export interface AISettingsInput {
  provider: AIProvider;
  apiKey: string;
  model?: string;
  baseUrl?: string;
  skipTls?: boolean;
  // wf — see AISettings.enabled. Always sent by the Settings form;
  // omitting it defaults to true on the backend (*bool nil⇒true).
  enabled?: boolean;
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

// Azure DevOps Server / TFS connection (v0.9.829). Tempo secret
// contract: the PAT never round-trips (hasPat is the stored
// indicator), and an empty `pat` on submit preserves the stored one.
//
// flavor picks the api-version: azure-devops-server → 6.0,
// tfs → 4.1, auto → probe. detectedFlavor/detectedApiVersion
// report what the last successful probe actually spoke; they are
// in-memory on the server and never persisted, so they may be
// absent until someone hits "Test connection".
//
// v0.9.830 — repoPrefixes / branchOrder: the service→repo naming
// convention the source-window fetcher applies. Unlike the PAT these
// are NOT secrets and DO round-trip; the snapshot echoes them
// RESOLVED, i.e. the bundled defaults appear when nothing was saved.
// AICodeContext (v0.9.831) — "Kodu da incele" isteğinin yanıtındaki
// kaynak-kod KÜNYESİ. Kodun KENDİSİ burada YOKTUR ve olmamalıdır:
// kaynak modele gider, tarayıcıya değil. Buradaki alanlar yalnız
// cevabın altındaki kaynak satırını ("core-service / release,
// 2 dosya") ve kod bulunamadığında gösterilen dürüst notu besler.
//
// files boş + reason dolu = kod okunamadı; UI bunu cevabın BAŞINDA
// tek satır olarak söyler, çünkü "kodu da incele" kutusunu işaretleyip
// kodsuz bir cevap almak sessizce yanıltıcıdır.
export interface AICodeContext {
  repo?: string;
  branch?: string;
  /** 'pin' = service_metadata.repository, 'convention' = önek/ek soyma. */
  source?: string;
  files?: { path: string; fromLine: number; toLine: number }[];
  reason?: string;
}

export type DevOpsFlavor = 'auto' | 'azure-devops-server' | 'tfs';
export interface DevOpsSnapshot {
  baseUrl: string;
  collection?: string;
  project?: string;
  username?: string;
  hasPat: boolean;
  flavor?: DevOpsFlavor;
  insecureSkipVerify?: boolean;
  detectedFlavor?: DevOpsFlavor;
  detectedApiVersion?: string;
  repoPrefixes?: string[];
  branchOrder?: string[];
}
export interface DevOpsSettingsInput {
  baseUrl: string;
  collection?: string;
  project?: string;
  username?: string;
  pat?: string;
  flavor?: DevOpsFlavor;
  insecureSkipVerify?: boolean;
  repoPrefixes?: string[];
  branchOrder?: string[];
}
export interface DevOpsTestResult {
  ok: boolean;
  detectedFlavor?: DevOpsFlavor;
  apiVersion?: string;
  projectCount: number;
  // True when a project name was supplied and the project lookup
  // actually ran — so the UI can say what it verified rather than
  // implying more than it checked.
  projectChecked?: boolean;
  error?: string;
}

// Thanos multi-cluster config (v0.8.577, audit: docs/audit/
// thanos-multicluster-metrics-audit.md). Snapshot masks tokens
// per cluster (hasToken); input's empty token preserves the
// stored one server-side, matched by cluster NAME. name is the
// APM join key — must equal the k8s.cluster.name /
// openshift.cluster.name value spans report.
export type ThanosAuthType = 'none' | 'bearer';
export interface ThanosClusterSnapshot {
  name: string;
  url: string;
  authType?: ThanosAuthType;
  hasToken: boolean;
  namespaceFilter?: string;
  insecureSkipVerify?: boolean;
  enabled: boolean;
}
export interface ThanosSnapshot {
  clusters: ThanosClusterSnapshot[];
}
export interface ThanosClusterInput {
  name: string;
  url: string;
  authType?: ThanosAuthType;
  token?: string;
  namespaceFilter?: string;
  insecureSkipVerify?: boolean;
  enabled: boolean;
}
export interface ThanosSettingsInput {
  clusters: ThanosClusterInput[];
}

// One (cluster, namespace, pod) sample from a remote cluster's
// Thanos Querier. CPU is CORES (not the 0-1 ratio HostRow uses);
// pct fields are 0/absent when the cluster doesn't expose
// kube-state-metrics limits (HostRow.MemPct "0 = unknown"
// contract).
export interface ClusterPodRow {
  cluster: string;
  namespace: string;
  pod: string;
  cpuCores: number;
  memBytes: number;
  cpuPct?: number;
  memPct?: number;
  // v0.8.580 — request-based axis (provisioning accuracy); can
  // exceed 100 by design (overshoot IS the signal). Absent =
  // requests not exposed on the cluster.
  cpuPctOfReq?: number;
  memPctOfReq?: number;
  // v0.9.3 — raw limit/request values for threshold reference
  // lines (absolute cores/bytes; absent = unknown).
  cpuLimitCores?: number;
  memLimitBytes?: number;
  cpuRequestCores?: number;
  memRequestBytes?: number;
  // v0.9.10 — pod network rate (cAdvisor; absent = not exposed).
  netInBps?: number;
  netOutBps?: number;
  // v0.9.12 — Coremetry service match (host_name = pod bridge);
  // absent = uninstrumented / infra pod / ambiguous.
  service?: string;
  // v0.9.37 (B4) — faz + restart (best-effort; absent = kube-state yok).
  phase?: string;
  restarts?: number;
  // v0.9.371 — restart SERİSİ yok (KSM yok / 1000-seri parse tavanı):
  // 0 değil BİLİNMİYOR; UI '—' çizer. restarts artık gerçek 0'da da gelir.
  restartsUnknown?: boolean;
}
// v0.9.3 — multi-pod trend serisi (top-10, sunucu keser).
export interface ClusterPodSeriesTrend {
  pod: string;
  trend: ClusterPodTrendPoint[];
}
export interface ClusterPodsTrendResponse {
  cluster: string;
  namespace: string;
  pods: ClusterPodSeriesTrend[] | null;
  totalPods: number;
}
// Minute-bucket trend point (HostTrendPoint bucket contract:
// unix SECONDS on minute boundaries).
export interface ClusterPodTrendPoint {
  bucket: number;
  cpuCores: number;
  memBytes: number;
}
export interface ClusterPodsResponse {
  cluster: string;
  pods: ClusterPodRow[] | null;
  count: number;
  // v0.9.369 — sunucu topk(500) tavanına dayandı: liste cluster'ın tamamı
  // değil, istemci süzmesi "yok" sonucunu kanıtlamaz.
  truncated?: boolean;
}
// v0.8.583 — node CPU/memory (dar kapsam). node = kube_node_info
// eşleşirse gerçek ad, yoksa instance (ip:port). Pct'ler kendi
// paydalarına oran; cpuPct çekirdek sayısı best-effort'una bağlı
// (0/absent = bilinmiyor).
export interface ClusterNodeRow {
  cluster: string;
  node: string;
  cpuCores: number;
  memBytes: number;
  cpuPct?: number;
  memPct?: number;
  // v0.9.32 — node rolü (master/control-plane/worker); heatmap dot
  // rengi. B4'te kube_node_role/labels'tan doldurulur, o gelene dek
  // absent (nötr dot).
  role?: string;
  // v0.9.10 — node network rate (node-exporter; absent = not exposed).
  netInBps?: number;
  netOutBps?: number;
}
export interface ClusterNodesResponse {
  cluster: string;
  nodes: ClusterNodeRow[] | null;
  count: number;
}
// v0.8.588 — namespace rollup satırı (ayrı sorgudan; pod topk
// kesmesinden bağımsız TAM toplamlar).
export interface ClusterNamespaceRow {
  cluster: string;
  namespace: string;
  pods?: number;
  cpuCores: number;
  memBytes: number;
  // v0.9.37 (B4) — restart toplamı + failing pod (best-effort).
  restarts?: number;
  failing?: number;
}
export interface ClusterNamespacesResponse {
  cluster: string;
  namespaces: ClusterNamespaceRow[] | null;
  count: number;
}
// v0.8.587 — genel görünüm kartı (skaler özet; alanlar best-effort:
// tenancy-kısıtlı token'da node alanları 0/absent kalabilir).
export interface ClusterSummary {
  cluster: string;
  nodes?: number;
  pods?: number;
  cpuUsedCores?: number;
  memUsedBytes?: number;
  netInBps?: number;
  netOutBps?: number;
  // v0.9.30 (design handoff B1) — kapasite (%), pod-fazı (donut),
  // firing-alert sayısı. Best-effort: yoksa alan absent, UI gizler.
  cpuCapacityCores?: number;
  memCapacityBytes?: number;
  podsRunning?: number;
  podsPending?: number;
  podsFailed?: number;
  alertsCritical?: number;
  alertsWarning?: number;
}
// v0.9.23 — namespace içi iş yükü rollup satırı (Deployment/STS/DS;
// "(unassigned)" = eşlenemeyen pod'lar). podNames pod tablosunun
// ?deployment= süzgecinin üyelik kaynağı.
export interface ClusterDeploymentRow {
  cluster: string;
  namespace: string;
  deployment: string;
  pods: number;
  cpuCores: number;
  memBytes: number;
  podNames: string[];
  // v0.9.39 — KSM replicas/status (best-effort; status boş = aile yok,
  // ready/desired yalnız status doluyken anlamlı).
  desiredReplicas: number;
  readyReplicas: number;
  status?: string;
}
export interface ClusterDeploymentsResponse {
  cluster: string;
  namespace: string;
  deployments: ClusterDeploymentRow[] | null;
  count: number;
}
// v0.9.36 — firing alert (panel). ageSec best-effort (0 = bilinmiyor).
export interface ClusterAlertRow {
  alertName: string;
  severity: string;
  namespace?: string;
  pod?: string;
  ageSec?: number;
}
export interface ClusterAlertsResponse {
  cluster: string;
  alerts: ClusterAlertRow[] | null;
  count: number;
}
// v0.9.50 — deployment-kapsamlı CPU/Mem trendi (Service→Infra §8).
export interface ClusterDeployTrendResponse {
  cluster: string;
  namespace: string;
  deployment: string;
  metric: 'cpu' | 'mem';
  byPod: boolean;
  series: ClusterNamedSeries[] | null;
  // v0.9.539 — kesme ÖNCESİ pod sayısı; yalnız kesme olduğunda gelir.
  // MetricArea "N / M pod" rozetini bununla çizer (operator-reported:
  // "17 pod var ama 7 tane gösteriyor" — kesme sessizdi).
  totalSeries?: number;
}
// v0.9.534 — Service→Infrastructure "Router / HAProxy" (OpenShift router
// backend metrikleri; seri adı = route). kind cache anahtarına girdiği
// için sunucuda üç değere sabitli.
export interface ClusterHaproxyTrendResponse {
  cluster: string;
  namespace: string;
  kind: '2xx' | '5xx' | 'latency';
  series: ClusterNamedSeries[] | null;
}
// v0.9.140/144 — Service→Infrastructure JBoss/JVM JMX (Thanos auto-discovery).
// metric = ham keşfedilmiş ad (jvm_*/jboss_*).
export interface ClusterJMXMetricsResponse {
  cluster: string;
  namespace: string;
  deployment: string;
  metrics: string[];
}
export interface ClusterJMXTrendResponse {
  cluster: string;
  namespace: string;
  deployment: string;
  metric: string;
  byPod: boolean;
  series: ClusterNamedSeries[] | null;
  // v0.9.370 — kesme ÖNCESİ toplam seri; series.length'ten büyükse
  // "By pod" top-8'e kesilmiştir ve UI bunu söyler.
  seriesTotal?: number;
}
// v0.9.35 — cluster/per-node kaynak trendi (Overview CPU/Mem area).
export interface ClusterNamedSeries {
  name: string;
  points: { bucket: number; value: number }[];
}
export interface ClusterResourceTrendResponse {
  cluster: string;
  metric: string;
  byNode: boolean;
  series: ClusterNamedSeries[] | null;
}
// v0.9.10 — cluster toplam ağ hızı trendi (Overview throughput).
export interface ClusterNetTrendPoint {
  bucket: number;
  inBps: number;
  outBps: number;
}
export interface ClusterNetworkTrendResponse {
  cluster: string;
  trend: ClusterNetTrendPoint[] | null;
}
export interface ClusterPodDetail {
  cluster: string;
  namespace: string;
  pod: string;
  trend: ClusterPodTrendPoint[] | null;
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
// v0.9.321 — 'incident' joins the union. A declared Incident is the one
// triage object a HUMAN created on purpose, and it was the only source the
// merged queue never showed: an operator working from /inbox could miss an
// open incident entirely while the sidebar's own /incidents badge counted it.
export type InboxKind = 'problem' | 'exception' | 'httperror' | 'anomaly' | 'incident';
export interface InboxItem {
  id: string;             // composite "<kind>:<nativeId>"
  kind: InboxKind;
  source: string;         // "Alert rule" | "Exception" | "Anomaly" | "Incident"
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
  // v0.9.255 — enrichment sonuçları. Backend bunları ZATEN hesaplıyordu
  // (EnrichProblemsWithRunbooks / WithDeploys, poll başına üç CH turu) ama
  // satıra kopyalamıyordu: sorgu faturalanıp cevap çöpe gidiyordu.
  runbookUrl?: string;
  recentDeploy?: { service: string; version: string; timeUnixNs: number };
  // v0.9.530 — arka plan işçilerinin proaktif kök-sebep cümlesi.
  // Sunucuda 240 bayta kırpılır (satırın işi tarama; tam metin detay
  // yüzeyinde). aiSummaryAt olmadan çizilmez — özet tek yazımlık ama
  // satırın gövdesi değişmeye devam eder, yaşsız çıkarım taze görünür.
  aiSummary?: string;
  aiSummaryAt?: number; // unix ns, 0/absent = özet yok
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
  incident?: { id: string; severity: string; status: string };
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
  // v0.8.527 — dosya/env referansları (grup senkron audit kararı):
  // doluysa inline caCert / bindPassword'u EZER. Değer yolun kendisidir,
  // sır değil — sanitize edilmez, geri döner.
  caFile?: string;
  bindPasswordFile?: string;
  bindPasswordEnv?: string;
  bindDN: string;
  bindPassword: string;
  baseDN: string;
  userSearchFilter: string;
  userAttribute: string;
  emailAttribute: string;
  displayAttribute: string;
  // v0.8.430 — users.team kaynağı: '' = department→ou; 'dn-ou' = DN'deki
  // en derin OU; başka değer = o attribute (legacy zincir fallback).
  teamAttribute?: string;
  // v0.8.434 — kaynak değerden ekip çıkarımı: ilk yakalama grubu ekip
  // olur (ör. displayName "…ÜNVAN-Ekip" için `-([^-]+)$`); eşleşme
  // yoksa ekip BOŞ kalır (kompozit sızmaz), geçersiz desen yok sayılır.
  teamRegex?: string;
  groupSearchBase: string;
  groupFilter: string;
  // Workaround toggle for AD's MaxValRange / MaxReceiveBuffer
  // caps — drops memberOf from the user-search attrs so the
  // separate group search is authoritative. Required when
  // senior users with thousands of nested groups can't log in.
  skipMemberOfFetch?: boolean;
  defaultRole: Role;
  groupRoleMap: LDAPGroupRoleMapping[];
  // v0.8.527 — periyodik AD grup→üye senkronu yapılandırması.
  groupSync?: LDAPGroupSyncConfig;
}
// v0.8.527 — LDAP/AD grup senkron ayarları (mevcut ldap blob'unun içinde).
export interface LDAPGroupSyncConfig {
  enabled: boolean;
  syncInterval: string;   // '30m'
  timeout: string;        // '60s'
  pageSize: number;       // 500
  usersBaseDN: string;
  userFilter: string;     // '(objectClass=user)'
  userNameAttribute: string; // 'sAMAccountName'
  groupsBaseDN: string;
  groupFilter: string;    // '(objectClass=group)'
  includePrefixes: string[];
  excludePrefixes: string[];
  maxGroupMembers: number; // 50000
}
// v0.8.527 — grup senkron durum özeti (GET /api/admin/ldap/groupsync).
export interface LDAPGroupSyncStats {
  groups: number; users: number; pages: number; truncated: number;
  tombstoned: number; matched: number; totalAlias: number;
  matchRatio: number; durationMs: number;
}
export interface LDAPGroupSyncGroupSummary { uid: string; cn: string; dn: string; memberCount: number; }
export interface LDAPGroupSyncSummary {
  configured: boolean;
  enabled: boolean;
  interval: string;
  synced: boolean;
  syncedAt?: string;
  groups: LDAPGroupSyncGroupSummary[];
  stats: LDAPGroupSyncStats;
}
// v0.8.527 — dry-run önizleme (GET /api/admin/ldap/groupsync/preview).
export interface LDAPGroupSyncPreviewGroup { uid: string; cn: string; dn: string; memberCount: number; sampleMembers: string[]; }
export interface LDAPGroupSyncPreview {
  totalGroupsInScope: number;
  sampledGroups: number;
  groups: LDAPGroupSyncPreviewGroup[];
  matched: number;
  totalAliases: number;
  matchRatio: number;
  warning?: string;
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
  // feedbackCount / thumbsUpRate (v0.8.399) — thumbs up/down verdicts
  // merged from ai_feedback; omitempty server-side, so absent = no
  // ratings in the window (thumbsUpRate only meaningful when
  // feedbackCount > 0; an omitted rate with count > 0 means 0%).
  bySurface: Array<{ surface: string; calls: number; errorRate: number; avgMs: number; feedbackCount?: number; thumbsUpRate?: number }>;
  byProvider: Array<{ provider: string; model: string; calls: number; inputTokens: number; outputTokens: number }>;
}

// AI cost rates (v0.5.167). USD per 1M tokens, per model.
// Bundled defaults live frontend-side (see lib/ai-rates.ts);
// admins can override via /api/ai/rates which the UI merges
// over the bundle. Local-model endpoints stay at 0/0 = free.
// NegativeFeedbackCall (v0.9.423) — 👎 madenciliği satırı: düşük
// puanlı cevabın yüzeyi + soru/cevap örnekleri.
export interface NegativeFeedbackCall {
  surface: string;
  createdAt: number; // unix ns
  userEmail?: string;
  prompt: string;
  response?: string;
}

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

export type MonitorType = 'http' | 'tcp' | 'ssl-cert' | 'keyword' | 'heartbeat';

export interface Monitor {
  id: string;
  name: string;
  type: MonitorType;
  url?: string;               // http + keyword
  method?: string;
  expectedStatus?: number;
  timeoutSec?: number;
  intervalSec: number;        // active probe period or heartbeat grace window
  enabled: boolean;
  heartbeatToken?: string;    // returned by the API on heartbeat-type monitors
  target?: string;            // tcp + ssl-cert (host:port)
  certWarnDays?: number;      // ssl-cert warn threshold (days), default 14
  keyword?: string;           // keyword type: substring asserted in the body
  keywordInvert?: boolean;    // keyword type: must NOT contain
  createdAt: number;
}

export interface MonitorResult {
  monitorId: string;
  time: number;               // unix ns
  status: 'up' | 'down' | 'degraded';
  latencyMs: number;
  httpCode?: number;
  message?: string;
  detail?: number;            // type-specific number (ssl-cert: days remaining)
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
  // Call-rate buckets over the same window as the aggregate — up to
  // chstore.SparklineBuckets (120) slots; the MV-grain floor makes
  // short windows ship fewer, real slots, so derive the axis from
  // array length (M4 granular sparklines). Rendered inline in
  // the table as a small SVG so the operator can spot a slow-burn
  // vs. spike pattern without leaving the page.
  sparkline?: number[];
  // v0.5.392 — companion error + p99 sparklines on the same
  // bucket grid. Drives the per-row metric drill-in modal on
  // the service detail page; both are optional (older backends
  // / raw-spans path may omit them).
  errorsSparkline?: number[];
  p99Sparkline?: number[];
  // v0.9.60 (Elastic-parity Operations) — latency hücresinin
  // percentile-seçicili sparkline'ı + compare=prior alanları.
  avgSparkline?: number[];
  p50Sparkline?: number[];
  p95Sparkline?: number[];
  hasPrior?: boolean;
  priorSpanCount?: number;
  priorErrorCount?: number;
  priorErrorRate?: number;
  priorAvgDurationMs?: number;
  priorP50DurationMs?: number;
  priorP95DurationMs?: number;
  priorP99DurationMs?: number;
  priorSparkline?: number[];
  priorErrorsSparkline?: number[];
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
  // v0.8.369 (Dynatrace-style sort): present when a non-time sort was
  // ranked within the newest-N recency slice instead of the whole
  // window — the UI shows a "ranked within newest N" hint. Absent =
  // exact/global ordering.
  rankedWithinRecent?: number;
  // v0.9.297 — present when the backend could NOT afford the requested
  // window and halved it to answer at all. The rows below describe
  // [narrowedFromNs, to], not the range the operator picked; a top-N
  // over a smaller window is a different answer, not a slower one.
  narrowedFromNs?: number;
}

// FAZ 2 (traces attribute columns) — response of the phase-2-only
// GET /api/traces?traceIds= enrichment call: attribute values keyed by
// trace id, then by requested attribute key. Every requested key is
// present per trace ('' when the trace doesn't carry it) so the client
// can mark it fetched and never refetch in a loop.
export interface TracesExtrasResponse {
  extras: Record<string, Record<string, string>>;
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
  // v0.9.457 (dürüstlük A2) — 50k span tavanı doldu; spanTotal MV
  // stub'ından gerçek sayı (best-effort, yoksa yalnız capped bilinir).
  spanCapped?: boolean;
  spanTotal?: number;
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
  // origin (v0.8.407, frontend-only) — 'span-event' marks a pseudo
  // log row synthesized from an OTel span EVENT (exception /
  // log-bridge record) already loaded with the trace from ClickHouse.
  // Never set on backend rows; <LogTable> renders a small chip so
  // operators can tell the two sources apart when merged.
  origin?: 'span-event';
}

export interface LogsResponse {
  total: number;
  logs: LogRow[];
  // nextCursor = opaque keyset cursor for the next page. Empty /
  // omitted (Go `omitempty`) on the last page — the UI stops
  // paging when it's absent. Pass it back verbatim as LogsParams.after.
  nextCursor?: string;
  // v0.8.332 (pivot Phase 3) — the trace-logs path (?traceId=) degrades to
  // HTTP 200 {degraded:true, reason} + empty lists instead of a 5xx when the
  // log backend is slow/unreachable (api_logs.go, pivot Phase 2). The Trace
  // Logs tab renders a warning chip; the tab never blocks.
  degraded?: boolean;
  reason?: string;
  // v0.8.400 (env-separation Phase 4) — the ES backend's honest signal
  // that ?env= was requested but NO environment field resolved in the
  // mapping (self-discovery came up empty and none is configured). The
  // results are env-UNFILTERED; /logs renders a warning chip instead of
  // silently implying a narrowed view (v0.8.398 honesty pattern).
  envUnapplied?: boolean;
  // ── Honesty envelope (v0.9.288), ES backend only ────────────────
  // partial — ES hit its 10s SOFT timeout or lost shards, so it
  // returned what it had computed. Every count here is a subset. At
  // 10B docs/day this is the realistic outcome of a heavy search, not
  // an edge case, and it used to be presented as a complete answer.
  partial?: boolean;
  // shardsFailed — how many shards did not answer.
  shardsFailed?: number;
  // totalIsLowerBound — `total` is "at least", not "exactly". ES is
  // asked for track_total_hits: 10000 and answers relation "gte" past
  // that cap. The identical field from the CH backend IS exact, so the
  // label has to say which one it is.
  totalIsLowerBound?: boolean;
}

// /api/notifications/log (v0.8.247 backend, v0.8.263 UI) — one sent
// notification (email / Slack / Teams / Zoom / webhook / …) as the
// worker's notify funnel recorded it. Target carries the real
// recipient (full fidelity per operator policy); webhook URLs are
// stored host-only because the full URL embeds a live credential.
export interface NotificationLogEntry {
  id: string;
  sentAt: number;       // unix ns
  channelKind: string;  // email|slack|mattermost|teams|zoomchat|webhook|whatsapp
  channelName: string;
  target: string;
  subject: string;
  bodyPreview: string;
  relatedKind: string;  // problem|test|runbook|incident|alert|monitor|…
  relatedId: string;
  ok: boolean;
  error: string;
}

// /api/logs/fieldstats (v0.8.255) — top values of one field in the
// current slice, for the fields-panel accordion. total = docs the
// top values were drawn from (buckets + remainder) → % denominators.
export interface LogFieldStats {
  field: string;
  total: number;
  values: { value: string; count: number }[];
  // v0.8.350 (HA 🟡6) — slow/unreachable log backend degrades to HTTP 200
  // {degraded:true, reason} + empty values instead of a 5xx, same contract
  // as LogsResponse (v0.8.332). The accordion renders its empty state.
  degraded?: boolean;
  reason?: string;
}

export interface MetricInfo {
  name: string;
  description: string;
  unit: string;
  type: string;
  // v0.9.833 — katalog satırı zenginliği. İkisi de metric_catalog
  // MV'sinden şema değişikliği OLMADAN geliyor (last_seen zaten
  // HAVING'de okunuyordu, service_name MV'nin ilk ORDER BY kolonu).
  // `?:` — v0.9.833 öncesi bir sunucuya bakan arayüz alanları
  // görmez; kolonlar o durumda "—" basar, sıfır basmaz.
  lastSeenNs?: number;
  // Sorgunun kapsamındaki farklı servis sayısı. Servis filtresi
  // açıkken tanım gereği 1'dir (satır ZATEN o çift), o yüzden kolon
  // servis seçiliyken gizlenir.
  serviceCount?: number;
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
  // v0.9.238 — which roles the answering pod runs. Absent on pre-v0.9.238
  // servers, so every consumer must treat `undefined` as "unknown", never
  // as "false" (main.tsx's RUM gate fails open on it).
  roles?: { ingest: boolean; api: boolean };
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

// FilterGroup — grouped AND/OR boolean builder (v0.8.x trace-query gap-2).
// Additive, default-off upgrade over the flat conjunction-only FilterExpr[]
// path: lets an operator express `(http.status >= 500 OR db.system = oracle)
// AND env = prod`. Sent to the backend as the `filterGroup` query param
// (JSON), which SUPERSEDES the legacy `filters` param when present.
//
// Depth cap: v1 supports exactly ONE level of nested `groups`. A flat-AND
// group — `{ join: 'AND', filters: <leaves> }` with no `groups` — is treated
// byte-identically to the legacy FilterExpr[] by the backend, so the default
// flat-chip-row render keeps every existing saved view / shared URL working.
export type FilterJoin = 'AND' | 'OR';

export interface FilterGroup {
  join: FilterJoin;
  filters: FilterExpr[];
  groups?: FilterGroup[]; // ≤1 level of nesting in v1
}

// ── Span metrics (Tempo span-metrics + Dynatrace MDA) ────────────────────────

export type SpanAgg =
  | 'count' | 'rate' | 'per_min' | 'errors' | 'error_rate' | 'apdex'
  | 'avg' | 'sum' | 'min' | 'max'
  | 'p50' | 'p90' | 'p95' | 'p99' | 'p999'
  // band (v0.8.411) — the whole p50/p90/p95/p99 percentile band from
  // ONE resolver call (agg=band); four series per group key, the
  // quantile label folded into groupKey's last element.
  | 'band';

// ServiceMetricThroughput — servis throughput'unu METRİKTEN okuma
// (v0.9.665, operatör isteği). Prometheus biçimli sayaç metriği; servis
// kimliği `job` etiketinin son bölümünde (`<namespace>/<servis>`).
//
// Cevap boş bir seriyle YETİNMİYOR: metrik kurulumda var mı, hangi `job`
// değerleri mevcut, hangi desen denendi — hepsi dönüyor. Boş bir grafik
// "metrik yok" ile "desen tutmadı"yı aynı gösterirdi.
export interface ServiceMetricThroughput {
  service: string;
  metric: string;
  jobLabel: string;
  pattern: string;
  metricExists: boolean;
  matched?: number;
  series?: SpanMetricSeries[];
  // Yalnız eşleşme YOKKEN dolu: kurulumda gerçekten bulunan job değerleri
  // ve onlardan çözülen servis adları.
  sampleJobs?: string[];
  sampleServices?: string[];
  // Yalnız metrik BULUNAMADIĞINDA dolu: katalogda yakın adlar. Prometheus
  // ve OTLP aynı ölçümü farklı adlandırıyor, doğru adı aramayı operatöre
  // yıkmamak için.
  suggestions?: string[];
  // v0.9.668 — çözüm süreci görünür olsun: hangi adlar denendi, hangisi
  // tuttu, instrument ne, eşleşme job'dan mı service_name'den mi geldi.
  tried?: string[];
  instrument?: string;
  matchedBy?: string;
  // v0.9.671 — hangi kimlik etiketleri denendi (job/service/name/kolon).
  triedLabels?: string[];
  // v0.9.682 — denenen adaylardan HANGİLERİ bu kurulumda gerçekten var.
  // "anahtar yok" ile "anahtar var, değer tutmadı" bambaşka eylemler
  // gerektiriyor; boş bir grafik ikisini de aynı gösteriyordu.
  presentKeys?: string[];
  // v0.9.683 — teknik olarak PATLAYAN adaylar. "Eşleşme yok" ile
  // "sorgu hata verdi" bambaşka şeyler; ikincisi sessiz kalırsa
  // operatör sonsuza kadar deseni kovalar.
  candidateErrors?: string[];
  // v0.9.774 — çözülen metriğin OTLP birimi ("s" / "ms" / …).
  //
  // v0.9.676-773 arası burada latency/latencyUnit/latencyUnitKnown/
  // latencyDiag vardı: uç histogram KOVALARINDAN P50/P95/P99 hesaplayıp
  // ms'ye çevirerek gönderiyordu. Prod'da metric_points satırları
  // bucket_counts taşıyıp bucket_bounds taşımadığı için o yol sessizce
  // boş dönüyordu. Panel artık Explore'un çalışan avg yolundan
  // (/api/metrics/query) besleniyor; bu uçtan yalnız KİMLİK isteniyor —
  // metriğin adı ve birimi. Ölçekleme yok: birim display processor'a
  // gidiyor (dataFrame.ts sözleşmesi).
  metricUnit?: string;
  // v0.9.679 — eşleşme ORTAMLA ayrıştırılamadı. Metrik tarafında servis
  // adı eksiz olduğu için `-uat`/`-prod` aynı ada iniyor; ortam kısıtı
  // tutmazsa seri birden çok ortamın verisini taşıyor OLABİLİR.
  // Sessiz kalmamalı: sayı makul göründüğü için kimse fark etmez.
  envAmbiguous?: boolean;
  unsupportedInstrument?: boolean;
}

export interface SpanMetricSeries {
  groupKey: string[];                  // raw tuple, joined for label
  points: { time: number; value: number }[]; // time = unix nanoseconds
}

// SpanMetricResult — /api/spans/metric envelope (v0.8.x). The backend trims a
// high-cardinality groupBy to the top ≤TOP_N_MAX series by area (the exact set
// the UI renders) to keep the wire payload small. `totalSeries` is the pre-trim
// count so PanelStack's "+N more" stays accurate; it is OMITTED when no trim
// happened — consumers default it to `series.length`. The resolver + batch
// paths return the bare series slice and never set totalSeries, so it stays
// optional and they keep working unchanged.
export interface SpanMetricResult {
  series: SpanMetricSeries[];
  totalSeries?: number;
  // v0.9.458 (dürüstlük A1) — 50k satır tavanı doldu: alfabetik-son
  // seriler eksik olabilir (top-N kırpmasından AYRI sinyal).
  rowsCapped?: boolean;
}

// v0.8.53 ("every metric is a doorway" D4) — result of server-side descriptor
// resolution (/api/metrics/resolve). `tier` reports which store served it
// (1s|10s|1m for spanmetrics, trace_summary_5m for tracemetrics, spans for the
// dual-read fallback) so the UI can surface the resolution if it wants.
export interface MetricExemplar {
  time: number;                        // bucket start, unix nanoseconds
  groupKey: string[];                  // matches the series it annotates
  slowTraceId?: string;
  errorTraceId?: string;
  // v0.9.313 (brief N1) — the entry span's kind on the RPC surface
  // ("server" for gRPC, "consumer" for a queue). Absent on the HTTP
  // surface, where the kind is implied by the route.
  kind?: string;
}

// AI per-service analysis (POST /api/copilot/analyze-service, v0.8.85+). The
// server summarises the service's signals; the operator-configured model returns
// this strict-JSON verdict. Turkish field names match the prompt contract.
export interface ServiceAnalysisVerdict {
  ozet: string;
  olasi_neden: string;
  kanit: string[];
  oneriler: string[];
  guven: 'yuksek' | 'orta' | 'dusuk';
}
export interface AiRED {
  spans: number; rate: number; errorRate: number; errorCount: number;
  avgMs: number; p50Ms: number; p95Ms: number; p99Ms: number;
}
export interface AiErrCount { type: string; message: string; service: string; count: number; sampleTraceId: string; }
export interface AiDeploy { version: string; timeUnixNs: number; }
export interface AiServiceContext {
  service: string; rangeS: number;
  current: AiRED; baseline: AiRED;
  topErrors: AiErrCount[]; deploys: AiDeploy[];
  upstream: string[]; downstream: string[];
}
export interface AiPostCheck { verified: boolean; unknownServices: string[]; note: string; }
export interface ServiceAnalysisResponse {
  analysis: ServiceAnalysisVerdict | null;
  context: AiServiceContext | null;
  raw: string;
  parsed: boolean;
  postCheck: AiPostCheck | null;
  cached: boolean;
  /** v0.9.593 — bu CEVABIN kimliği; 👍/👎 bununla
   *  POST /api/ai/feedback'e gider. Yoksa affordance ÇİZİLMEZ. */
  exchangeId?: string;
  /** v0.9.655 — örnek request_id'lerden dış log sistemine köprüler.
   *  Şablon yapılandırılmamışsa alan HİÇ gelmez (kırık link, link
   *  yokluğundan kötüdür). Ortam servis adının sonekinden çözülür. */
  correlationLinks?: { label: string; href: string }[];
}

/** v0.9.594 — RCA hakem motorunun kalite özeti (GET /api/ai/rca-quality).
 *  Kardeş /ai uçları TRANSPORT sağlığını ölçüyor (kaç çağrı, kaç hata,
 *  kaç token); bu, cevabın KENDİSİNE bakan tek ölçüm. */
export interface RCAVerdictQuality {
  total: number;
  rootCauseIdentified: number;
  probableCause: number;
  insufficientEvidence: number;
  /** Model şemaya uymadı → deterministik düşüş. Bu karar MODELİN değil bizim. */
  unparsed: number;
  /** Bir onarım turu gerekti (```json çitleri, önsöz). Kalite sinyali. */
  repaired: number;
  /** En az bir kalkan devreye girdi — model uydurulmuş kanıt/varlık kullandı. */
  shielded: number;
  avgConfidence: number;
  thumbsUp: number;
  thumbsDown: number;
}

/** v0.9.613 — dağıtık DDL kuyruğu teşhisi (GET /api/admin/clickhouse/ddl-queue).
 *  Verdict DAVRANIŞTAN türer (chstore/ddl_queue_health.go başlığı):
 *  kuyruk dolu + host geride = worker takılı (restart çözer);
 *  kuyruk dolu + kimse geride değil = worker'lar girdileri ATLIYOR
 *  (hostname/IP uyuşmazlığı). */
export interface DDLQueueHealth {
  clusterMode: boolean;
  verdict: 'healthy' | 'worker_stuck' | 'worker_skipping' | 'unreachable' | 'probe_failed' | 'single_node';
  detail: string;
  stuckCount: number;
  /** Sayım probe'u düştü — stuckCount alt sınır ("en az N"). */
  stuckCountApprox?: boolean;
  oldestAgeSeconds?: number;
  queueHead?: number;
  hosts?: { host: string; processed: number; behind: number }[];
  unreachableHosts?: string[];
  entries?: { entry: string; host: string; status: string; ageSeconds: number; query: string }[];
  queueHosts?: string[];
  clusterHosts?: string[];
  probeErrors?: string[];
  generated: number;
}

/** v0.9.770 — rollup kurulum sihirbazı (/api/admin/rollup/*).
 *  0001 (dar span zinciri) + 0003 (metrik zinciri) migration'ları
 *  operatörün elle SQL koşmasına gerek kalmadan uygulanır. OTOMATİK
 *  DEĞİL: boot'ta asla koşmaz, tek tetikleyici admin'in butonu.
 *  Gerekçe: internal/chstore/rollup_admin.go başlığı.
 *
 *  v0.9.777 — 'route' (0008: endpoint kırılımlı metrik zinciri) AYRI bir
 *  hedef. 'both' bilerek 0001+0003 olarak KALDI: bugüne kadar "Her ikisi"ni
 *  seçmiş bir operatörün geri-alma düğmesi, hiç kurmadığı bir zinciri
 *  düşürmeye kalkmamalı. */
export type RollupTarget = 'narrow' | 'metrics' | 'route' | 'both';

/** Tek bir DDL ifadesinin sonucu. Head = ifadenin ilk 90 karakteri. */
export interface RollupStmtResult {
  head: string;
  ok: boolean;
  err?: string;
}

/** Kurulum ÖNCESİ hüküm. Supported=false iken "Kur" butonu KAPALI —
 *  yarım uygulanmış bir zincir hiç kurulmamış olandan kötüdür. */
export interface RollupPreflightResult {
  /** system.clusters'taki adlar. UI <select>'e döker; elle yazdırmıyoruz
   *  çünkü yanlış ad DDL'i kuyrukta süresiz bekletir (v0.9.613). */
  clusters: string[];
  /** Coremetry'nin kendi yapılandırmasındaki ad — ön-seçili gelir. */
  suggestedCluster?: string;
  spansLocal: boolean;
  metricPointsLocal: boolean;
  /** Zincirin TABANI zaten kurulu mu (DDL'ler IF NOT EXISTS). */
  narrowInstalled: boolean;
  metricsInstalled: boolean;
  /** 0008 (route kırılımlı metrik zinciri) tabanı var mı — v0.9.777. */
  routeInstalled: boolean;
  /** "tablo.kolon" biçiminde; boş = tam. */
  missingColumns: string[];
  probeErrors?: string[];
  supported: boolean;
  detail: string;
  generated: number;
}

/** Tek bir rollup tablosunun canlı durumu. minTsMs=0 → boş ya da okunamadı. */
export interface RollupTableStatus {
  table: string;
  family: 'narrow' | 'metrics' | 'route';
  exists: boolean;
  rows: number;
  minTsMs: number;
  err?: string;
}

export interface RollupStatusResult {
  cluster: string;
  tables: RollupTableStatus[];
  generated: number;
}

/** apply / rollback yanıtı — ifade-ifade sonuç + toplu hüküm. */
export interface RollupActionResult {
  statements: RollupStmtResult[];
  ok: boolean;
}

export interface MetricResolveResult {
  series: SpanMetricSeries[];
  tier: string;
  stepSeconds: number;
  // v0.9.809 (dürüstlük) — 50k satır tavanı doldu: ORDER BY gk alfabetik
  // olduğundan geç harfli seriler KOMPLE düşmüş olabilir. Kardeş zarflar
  // (SpanMetricResult, /api/metrics/query) bunu v0.9.458'den beri
  // taşıyordu; resolver yolu şeritsizdi. totalSeries YOK ve olmayacak:
  // bu yolda top-N kırpması hiç yaşanmıyor, tavan ısırdığında da gerçek
  // toplam sorgudan bilinemiyor (bkz. chstore/metricresolve.go).
  rowsCapped?: boolean;
  exemplars?: MetricExemplar[];
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
  // v0.9.473 (dürüstlük A13) — 200k satır tavanı doldu: pencerenin SAĞ
  // kenarı kesik olabilir ("trafik düştü" yanılsaması).
  rowCapped?: boolean;
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
  // Imported ES Watcher definition (Faz-1). When set the evaluator
  // runs the watcher path instead of the metric/log-query paths;
  // metric === 'watcher' by convention. Stored verbatim server-side.
  watcherJson?: string;
  createdAt: number;
}

// ── ES Watcher import (Faz-1) ───────────────────────────────────────────────
// POST /api/watchers/import — dry-run returns the mapping report only;
// live import adds imported/ruleId. Findings mirror internal/watcher.

export type WatcherSupport = 'supported' | 'partial' | 'unsupported';

export interface WatcherFinding {
  field: string;
  status: WatcherSupport;
  reason: string;
}

// Projection preview — the exact fields the imported rule will run with.
export interface WatcherRulePreview {
  name: string;
  comparator: string;
  threshold: number;
  windowSec: number;
  cooldownSec: number;
}

export interface WatcherImportReport {
  findings: WatcherFinding[];
  enabled: boolean;
  disabledReason?: string;
  rule: WatcherRulePreview;
}

export interface WatcherImportResult {
  report: WatcherImportReport;
  imported?: boolean;
  ruleId?: string;
}

// ── /watchers page (v0.9.196) ───────────────────────────────────────────────
// GET /api/watchers/summary — rule_id → rollup, ONE bounded problems
// GROUP BY for the whole fleet (never per-row history calls).
export interface WatcherSummaryEntry {
  lastFire: number;   // unix ns of the newest fire; 0 = never fired
  fires24h: number;   // problems opened in the trailing 24h
  openNow: boolean;   // an open/acknowledged problem exists right now
  // M4 granular sparklines — the same trailing-24h fires split into 24
  // one-hour slots, oldest→newest (slot 23 = the last hour). Absent
  // for rules that never fired; the list cell degrades to the count.
  firesHourly?: number[];
  // Structural can't-run reason recomputed from the stored definition
  // for DISABLED rules (script condition, no executable search, …).
  // Absent for enabled rules and for hand-disabled runnable watches.
  disabledReason?: string;
}

// GET /api/watchers/{id}/history — one rule's drawer timeline: recent
// problems (fire = startedAt, resolve = resolvedAt) + the notification
// rows recorded for those problem ids (related_kind='watcher').
export interface WatcherHistory {
  problems: Problem[];
  notifications: NotificationLogEntry[];
  // v0.9.197 — bu watcher'ın bir fire'ı ŞU AN hangi kanallara giderdi
  // (enabled + minSeverity + match-rule süzgeçlerinden geçenler).
  channels: WatcherChannelInfo[];
}

export interface WatcherChannelInfo {
  name: string;
  kind: string;
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
  notifyOnComplete?: boolean;  // fire a completion notification (v0.7.7)
  notifyChannels?: string[];   // which channel TYPES (email/slack/teams/zoomchat/webhook/whatsapp); empty = email (v0.7.22)
}

export type RunbookExecStatus =
  | 'running' | 'waiting_for_user' | 'completed' | 'failed' | 'cancelled';
export type RunbookStepStatus =
  | 'pending' | 'running' | 'waiting_for_user' | 'completed' | 'skipped' | 'failed';

// StepState is a step's snapshot + live status within an execution. Steps
// are frozen at execution start (snapshot-on-start) so template edits never
// rewrite a historical run — this IS the audit trail.
export interface RunbookStepState {
  stepId: string;
  order: number;
  kind: RunbookStepKind;
  title: string;
  instructions?: string;
  status: RunbookStepStatus;
  by?: string;        // user (manual) or agent id (automated)
  note?: string;
  output?: string;    // stdout / returnValue / HTTP body
  error?: string;
  startedAt?: number;
  endedAt?: number;
}

export interface RunbookExecution {
  id: string;
  runbookId: string;
  titleSnapshot: string;
  status: RunbookExecStatus;
  startedBy?: string;
  startedAt: number;
  completedAt?: number;
  problemId?: string;
  stepStates: RunbookStepState[];
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

// v0.9.550 — evaluator kalp atışı. Boş bir Problems sayfasının
// "sorun yok" mu yoksa "evaluator ölü" mü olduğunu ayırt eder.
// status='unknown' ÖLÇEMEDİK demektir; asla iyi haber olarak
// gösterilmemeli (backend: internal/api/evaluator_health.go).
export interface EvaluatorHealth {
  status: 'ok' | 'stale' | 'failing' | 'unknown';
  reason: string;
  /** Son tikten bu yana geçen saniye; unknown iken -1. Sunucu hesaplar
   *  (tarayıcı saati kaymış olabilir). */
  ageSec: number;
  durationMs: number;
  rules: number;
  opened: number;
  resolved: number;
  err?: string;
  version?: string;
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
  // v0.9.403 — runtime pod alarmının pod kimliği (401: service artık
  // birleşik ad taşımaz); diğer üreticilerde boş.
  pod?: string;
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
  // Owning team + SRE/reliability team for the firing service,
  // read-time enriched from the service catalog (NOT stored on the
  // problems row — a catalog edit reflects on the next refresh).
  // Empty when the service has no catalog entry. Powers the
  // owner/SRE team filters on /problems (v0.8.290), mirroring the
  // inbox + Services pattern.
  ownerTeam?: string;
  sreTeam?: string;
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
  // Root-cause ribbon summary (rc #3) — the worker's persisted top-suspect
  // for this problem, joined at read time by the /problems list handler.
  // Absent until the worker synthesizes a hypothesis (→ honest "no clear
  // cause yet" ribbon). Powers RootCauseRibbon's collapsed chip with no
  // per-row fetch; the expand reads the full /rootcause fan-out.
  rootCause?: RootCauseSummary;
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
  // v0.9.415 — ExceptionExplainer'ın proaktif kök-sebep özeti (P1
  // gruplara arka planda dolar); boş/yok = henüz üretilmedi.
  aiSummary?: string;
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

// One time-bucket of the "occurrences over time" histogram on the
// problem detail page — a real server-side, gap-filled COUNT (v0.8.309),
// not derived from sampled timestamps.
export interface OccurrencePoint {
  time: number;          // unix ns, bucket start
  count: number;
}

// ── Settings + notifications ─────────────────────────────────────────────────

// TeamAliases (v0.9.427) — LDAP↔telemetri takım adı eşleme tablosu:
// alias → kanonik ad ("dijitalsy" → "SY-Dijital Bankacılık").
export interface TeamAliases { aliases: Record<string, string> }

// TeamContacts (v0.8.429) — problem-open → team e-mail routing config.
// Contacts maps a catalog team name (owner/SRE) to address(es);
// comma-separated values fan out to multiple recipients.
export interface TeamContacts {
  enabled: boolean;
  minSeverity?: 'info' | 'warning' | 'critical';
  contacts: Record<string, string>;
}

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
    // v0.9.828 — en düşük triyaj basamağı. Boş = hepsi. "P2" seçilen
    // kanal P1 ve P2 alır, P3 almaz (BU BASAMAK VE ÜSTÜ).
    //
    // minSeverity'den AYRI: ciddiyet "ne kadar kötü", öncelik "ne kadar
    // acil" diyor ve ikisi ayrışabiliyor — bir critical problem P2
    // olabilir, bir monitor DOWN ise tam kayıp olduğu için P1'dir.
    minPriority?: 'P1' | 'P2' | 'P3' | '';
  };
  // Type-specific union. Optional fields keep the existing email/slack/
  // webhook callers happy; new channels (mattermost shares slack's
  // shape; whatsapp adds Twilio creds) only fill the fields they need.
  config: {
    recipients?: string[];   // email + whatsapp 'to' list
    webhookUrl?: string;     // slack / mattermost / teams (legacy zoomchat for migration only)
    url?: string;            // generic webhook
    headers?: Record<string, string>; // webhook (v0.8.445) — özel başlıklar (write-only; geri echo edilmez)
    bodyTemplate?: string;   // webhook (v0.8.445) — opsiyonel Go template gövde
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

export type PanelType = 'metric' | 'spanmetric' | 'stat' | 'gauge' | 'markdown' | 'row' | 'heatmap' | 'promql' | 'topn';
export type PanelWidth = 1 | 2 | 3 | 4;  // 1=quarter … 4=full (12-col grid)
// v0.9.778 — panel body height. Three rungs rather than a free pixel number:
// a dashboard reads as a grid, and arbitrary heights turn it into a ragged
// wall. The pixel map lives in components/dashboard/panelChrome.ts (one
// constant, two families — a chart needs more room than a number tile).
export type PanelHeight = 's' | 'm' | 'l';

// Each panel type has a different config shape. Kept as a tagged union so
// the renderer can switch on `type` exhaustively.
export interface MetricPanelConfig {
  metricName: string;
  service?: string;
  agg?: string;            // avg | sum | p95 | p99 | …
  groupBy?: string;        // comma-sep keys
  // v0.9.121 (F4) — when set, the panel is driven by this raw PromQL query
  // (/api/metrics/promql) INSTEAD of the metricName/agg/groupBy builder; the
  // editor toggles Builder ↔ PromQL. Empty/absent = builder mode (unchanged).
  promql?: string;
  // Bucket seconds. Absent/0 = auto — width-aware since GRAN-C (v0.8.248):
  // resolved from the panel's pixel budget (panelStep.ts), floored by the
  // backend's min-step clamp (v0.8.243). Optional on purpose: dashboards
  // saved before v0.8.248 have no step field and decode straight to auto.
  step?: number;
  filters?: string;        // JSON FilterExpr[]
  // Madde 4 sweep — y-ekseni/tooltip birimi ("ms", "%", "rps", "bytes"…).
  // PanelRenderer MultiLineChart'a geçirir (eskiden yalnız promql panelinde
  // vardı); Metrics builder'ın "Add to dashboard"u metriğin katalog
  // birimiyle doldurur. Yokluğu = birimsiz (eski davranış).
  unit?: string;
  // v0.9.790 — çizim markı. SpanMetricPanelConfig.viz'in ikizi ve aynı
  // sözleşme: YOKLUĞU = 'line'. `?:` bilinçli — bugüne dek kaydedilmiş her
  // metric paneli viz alanı taşımıyor ve alansız config'in çizgi çizmesi
  // gerekiyor; alan zorunlu olsaydı eski dashboard'lar decode edilemezdi.
  //
  // v0.9.786'da viz yalnız spanmetric için taşınmıştı çünkü BURASI yoktu:
  // Explore'da bars/area seçip metric-kaynaklı bir sorguyu pinleyen operatör
  // çizgi paneli alıyordu. Alan hem builder hem PromQL modunda geçerli —
  // MetricPanel'in çizim dalı ikisinde de aynı (cfg.unit gibi).
  viz?: PanelVizType;
}
export interface SpanMetricPanelConfig {
  agg: string;             // count | error_rate | p95 | …
  field?: string;          // duration_ms (default) or attribute
  groupBy?: string;
  dsl?: string;            // multi-line DSL (AND-joined)
  filters?: string;        // JSON FilterExpr[]
  step?: number;           // bucket seconds; absent/0 = width-aware auto (see MetricPanelConfig.step)
  // Madde 4 sweep — y-ekseni/tooltip birimi (MetricPanelConfig.unit ikizi).
  unit?: string;
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

// v0.9.109 (C2) — Heatmap panel. Renders an explicit/exponential-
// histogram METRIC's latency distribution as a time×bucket density grid
// (reuses LatencyHeatmap viz + the /api/metrics/histogram machine that F3
// works on — the first dashboard surface for the metric histogram path).
// Global distribution (no agg/groupBy — a heatmap blends the whole
// distribution, PromQL histogram-heatmap default). Bucket bounds come
// from the metric's own explicit bounds; the y-axis is the metric unit.
export interface HeatmapPanelConfig {
  metricName: string;      // histogram-instrument metric (e.g. http.server.duration)
  service?: string;
  unit?: string;           // bounds unit for the y-axis label ('ms' default; 's' → ×1000)
  step?: number;           // bucket seconds; absent/0 = width-aware auto (see MetricPanelConfig.step)
  filters?: string;        // JSON FilterExpr[]
}

// v0.9.117 (F4) — PromQL panel. A dashboard chart driven by a raw PromQL
// query against the OTel metric store (/api/metrics/promql, Phases 1-3) —
// Grafana-style. Renders line/area like the metric panel. Dashboard variables
// (${service}) expand into the query at render time.
export interface PromqlPanelConfig {
  query: string;
  unit?: string;           // y-axis unit override (ms | % | rps | free text)
  step?: number;           // bucket seconds; absent/0 = width-aware auto
  viz?: PanelVizType;      // line (default) / bar / area / stacked
}

// v0.9.781 — Top-N bar panel. The "which N are worst right now" surface every
// APM has (Datadog's Top List, Grafana's Bar gauge): one horizontal bar per
// group, ranked by the aggregation over the WHOLE window — not a time series.
//
// Same data source as the spanmetric panel (/api/spans/metric), but asked a
// different way: the panel requests a bucket size that collapses the window
// into ONE bucket (components/dashboard/topN.ts `topNStep`), so each series
// carries exactly one point and that point is the exact window aggregate. Any
// other framing ranks partial buckets, which is a silent lie on p99 /
// error_rate; the rationale + the live-ClickHouse evidence live in topN.ts.
//
// `limit` is capped at the server's own 50-series trim; `linkTo` is opt-in and
// deliberately offers only pivots that can be built EXACTLY from the row's
// group-key tuple.
export interface TopNPanelConfig {
  agg: string;             // count | error_rate | p95 | … (SpanMetricPanelConfig twin)
  field?: string;          // duration_ms (default) or attribute
  groupBy: string;         // comma-sep keys; the bars ARE these groups
  dsl?: string;            // multi-line DSL (AND-joined)
  filters?: string;        // JSON FilterExpr[]
  unit?: string;           // value formatting ('ms' | '%' | 'rps' | free text)
  limit?: number;          // rows to render; clamped to ≤50 (server trim)
  // Row click target. 'service' expects the FIRST group-by key to be a
  // service name; 'traces' rebuilds the row's exact population as span
  // filters. 'none' (default) leaves the row unclickable rather than
  // guessing a destination.
  linkTo?: 'none' | 'service' | 'traces';
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
  // v0.9.773 — optional operator note ("what does this panel actually
  // measure / when should I care"). Rendered as a hoverable ⓘ next to the
  // title, never as body text — a dashboard is scanned, not read. Optional
  // on purpose: every panel saved before v0.9.773 has no field and decodes
  // unchanged. The backend stores panels as opaque JSON
  // (chstore/dashboard.go), so this round-trips with no migration.
  description?: string;
  width: PanelWidth;
  // v0.9.778 — optional body height (s / m / l). Deliberately NOT in the
  // per-type config: StatPanel's fetch effect keys on JSON.stringify(cfg), so
  // a height stored there would re-run the ClickHouse query on every resize.
  // Absent → 'm', which is the pre-v0.9.778 hard-coded 220 / 280 — every
  // dashboard saved before this release decodes byte-identical.
  height?: PanelHeight;
  // v0.6.20 — optional per-panel time-range override
  // (Grafana-parity). When set, this panel's data fetch ignores
  // the dashboard-level Topbar range and uses this preset
  // instead. Useful for "60-day baseline" tiles sitting next to
  // a "last 15min" incident chart on the same dashboard.
  // undefined / missing → fall back to the dashboard's range.
  rangeOverride?: TimeRange;
  config: MetricPanelConfig | SpanMetricPanelConfig | StatPanelConfig | GaugePanelConfig | MarkdownPanelConfig | RowPanelConfig | HeatmapPanelConfig | PromqlPanelConfig | TopNPanelConfig;
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
  // v0.9.759 — 'database': /api/databases kataloğundan (system/instance/
  // dbName) ad seçici; DB sayısı küçük küme (≤~10) → düz select ev kuralı.
  type: 'service' | 'database' | 'custom';
  options?: string[];    // custom-type only
  defaultValue?: string; // empty → "all" / no override
}

export interface DashboardSummary {
  id: string;
  name: string;
  description: string;
  // v0.9.780 — panoya ait, PAYLAŞILAN serbest-metin etiketler. Liste
  // yanıtında da geliyor (panels/variables'ın aksine: etiketler küçük
  // ve /dashboards tablosu onları çiziyor). Eski kayıtlarda yok → `?`.
  tags?: string[];
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
  // v0.8.356 — MV-backed columns (spanmetrics_1m): true window
  // quantiles + req/min throughput. Optional so a mid-rolling-
  // deploy page against an older backend renders "—" instead of
  // crashing on undefined.toFixed.
  p50Ms?: number;
  // v0.9.305 — the percentile between "typical" and "tail". The MV
  // already produced it; the raw path's quantile family was widened
  // to expose it. Optional for the same rolling-deploy reason.
  p90Ms?: number;
  p95Ms?: number;
  reqPerMin?: number;
  // v0.9.310 (brief N3) — the slowest / worst-error trace for this
  // (service, route, window), resolved from the MV's argMax exemplar
  // states. ABSENT means "no exemplar in this window", not "none
  // exists": the states are forward-only and the error one is empty
  // for a healthy window. Render no link rather than a placeholder.
  slowTraceId?: string;
  errorTraceId?: string;
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

// EndpointsListResponse — v0.9.812. /api/endpoints artık çıplak dizi
// değil zarf döndürüyor: "kötüleşenler önce" (sort=p99Delta) sıralaması
// SUNUCUDA, çağrıya göre ilk `pool` aday üzerinde yapılır ve bu gerçeğin
// UI'ya taşınacak bir yeri yoktu — sayfa onu sabit "~1000" metniyle
// TAHMİN ediyordu, havuz limit'e göre değiştiği anda da yalan oluyordu.
export interface EndpointsListResponse {
  rows: EndpointRow[] | null;
  /** Sıralama evreninin boyutu (en çok çağrılan ilk N). Yalnız p99Delta. */
  pool?: number;
  /**
   * Havuz tasarım niyetinden (limit×5) kısıldı VE gerçekten doldu:
   * evrenin dışında kalan endpoint'ler var, liste eksik olabilir.
   * Havuza her şey sığdıysa gelmez — false alarm basmak, bu zarfın
   * kaldırmak için var olduğu sessiz yanlışın aynısı olurdu.
   */
  poolCapped?: boolean;
}

// EndpointWhereTheTimeGoes — v0.9.311 (brief N4). Where one route's
// latency actually goes, plus who calls it.
//
// SAMPLED, not measured: no MV carries route→downstream, so the
// backend walks the route's SLOWEST traces in the window and splits
// their time. sampledFrom is the trace count behind every number here
// and the UI is required to show it — a share derived from 200 traces
// must never read as a window-wide measurement.
export interface EndpointEdge {
  name: string;
  /** service | db | messaging | self — drives the icon, no re-derivation. */
  kind: string;
  calls: number;
  avgMs: number;
  p99Ms: number;
  errors: number;
  /** Total ms this edge accounts for across the sample. */
  shareMs: number;
}

export interface EndpointDownstream {
  /** Direct children of the route's span — these sum to totalMs. */
  downstream: EndpointEdge[];
  callers: EndpointEdge[];
  /**
   * Database / broker time at ANY depth, deliberately OUTSIDE the
   * share arithmetic: a grandchild's time is already inside its
   * parent's, so listing it as a sibling would double-count the same
   * milliseconds. Rendered as a nested breakdown, labelled as one.
   */
  backends: EndpointEdge[];
  sampledFrom: number;
  /** The sampled entry spans' total duration — the share denominator. */
  totalMs: number;
}

// EndpointCallers — v0.9.839. "Who calls this endpoint", the panel the
// operator asked for on the /endpoint page in the /databases caller
// table's exact shape (service · calls · error % · p95 · impact).
//
// SAMPLED BY CONSTRUCTION, like EndpointDownstream: no MV carries the
// pair (route, caller) — service_callers_5m keys on the receiving
// SERVICE and topology_op_edges_5m keys on the span NAME, which is not
// this page's identity. The backend reads a bounded, unbiased sample of
// the route's entry spans and resolves their parents.
export interface EndpointCaller {
  service: string;
  calls: number;
  errors: number;
  errorRate: number;
  /** p95 of THIS route's duration when called by this service. */
  p95Ms: number;
  shareMs: number;
  sharePct: number;
}

export interface EndpointCallersResponse {
  callers: EndpointCaller[] | null;
  /** Entry spans behind every number above. */
  sampledSpans: number;
  /**
   * true = the window held MORE entry spans than were read, so these
   * are estimates. false = the sample IS the window, and the UI must
   * not label a complete answer "sampled".
   */
  sampled: boolean;
  /** Sampled entry spans with no parent at all — entered from outside. */
  directEntries: number;
  /**
   * Sampled entry spans whose parent id resolved to nothing. "We could
   * not see the caller" — deliberately NOT folded into directEntries,
   * which is a different and much stronger claim.
   */
  unresolved: number;
  /** The sample's total entry duration — the share denominator. */
  totalMs: number;
}

// EndpointDetail — v0.8.360 (Stage-2 slice E2). One payload for the
// /endpoints detail drawer, mirroring internal/api/endpoints_detail.go's
// endpointDetailPayload. Sections are NULL-TOLERANT by contract: a
// failed backend section arrives as null and the drawer renders the
// rest — never gate the whole drawer on one section.
export interface EndpointDetail {
  service: string;
  path: string;
  fromNs: number;
  toNs: number;
  // 1-D latency distribution over the heatmap's log-scale duration
  // bins (bin = upper bound in ms). samplingRate < 1 ⇒ counts are
  // extrapolated from a trace-ID sample (>1h windows).
  histogram: {
    bins: number[];
    counts: number[];
    samplingRate?: number;
  } | null;
  // Per-status-CODE counts + the class rollup the table pills use.
  statusBreakdown: {
    http2xx: number;
    http3xx: number;
    http4xx: number;
    http5xx: number;
    codes: Record<string, number>;
  } | null;
  topExceptions: EndpointExceptionRow[] | null;
  failingTraces: EndpointFailingTrace[] | null;
  exemplars: { slowTraceId?: string; errorTraceId?: string } | null;
}

// One exception type observed on the endpoint's spans; fingerprint is
// the inbox group id for the /problems?exception= deep link.
export interface EndpointExceptionRow {
  type: string;
  message: string;
  fingerprint: string;
  count: number;
  lastSeenNs: number;
}

// One failing trace on the endpoint (direct /trace?id= pivot).
// durationMs is the worst ENDPOINT-span duration inside the trace.
export interface EndpointFailingTrace {
  traceId: string;
  durationMs: number;
  spanName: string;
  statusMsg?: string;
  httpStatus?: number;
  errorSpans: number;
  timeNs: number;
}

// EndpointSplitResponse — v0.8.360. Top-10 values of one whitelisted
// attribute with RED each (the drawer's split-by section).
export interface EndpointSplitResponse {
  by: string;
  values: EndpointSplitValue[];
}

export interface EndpointSplitValue {
  value: string;
  calls: number;
  errors: number;
  errorRate: number;
  avgMs: number;
  p99Ms: number;
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
  // Call-rate sparkline across the window (variable length —
  // derive the axis from the array). Used by the Span Metrics
  // table to render an inline mini-chart per row so the operator
  // sees the shape of traffic without opening the full /metrics
  // chart.
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

// ServiceInstance — one pod/host emitting metrics for a service, the
// per-pod row in the Overview "Instances" card. cpuPct is 0-100; memPct is
// 0-100 only when the runtime reports a memory limit (JVM), else 0 (the UI
// gauges memory relative to the busiest pod).
export interface ServiceInstance {
  id: string;        // host_name (pod identity)
  zone: string;      // availability zone, '' if absent
  cpuPct: number;    // 0-100
  memBytes: number;  // latest RSS / used bytes
  memPct: number;    // 0-100, or 0 when no limit reported
  up: boolean;       // saw a sample within the freshness window
  lastSeen: number;  // unix ns
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
  // v0.9.936 added `behavior_change` — davranış motorunun (haftanın
  // saati baseline'ı, 28 gün) bulduğu KALICI kayma. Diğerlerinden farkı:
  // `sample` alanı serbest metin değil, BehaviorChangeDetails JSON'u.
  kind: 'log_pattern' | 'trace_op' | 'elastic_ml' | 'log_template_new' | 'behavior_change';
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
  // Root-cause ribbon summary (rc #3) — the worker's persisted top-suspect
  // for this anomaly, joined at read time by the /anomalies events handler.
  // Absent until synthesized (→ honest "no clear cause yet" ribbon). Powers
  // RootCauseRibbon's collapsed chip; the expand reads the full
  // /anomalies/{id}/rootcause fan-out.
  rootCause?: RootCauseSummary;
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
  // v0.8.297 — dominant db.name for a db node's system (best-effort
  // enrichment on both the sampled and MV paths); the pill sub-line
  // shows it so "oracle" reads "oracle · COREBANK".
  dbName?: string;
  // True when the diff endpoint reports this node didn't exist
  // in the baseline window (e.g. yesterday's same slot). Pulses
  // green in the graph + flagged "NEW" in the changes panel.
  isNew?: boolean;
  // k8s/openshift cluster this service ran in during the
  // sampled window. Read-time enriched server-side. Empty
  // for SDKs that don't ship cluster resource attrs;
  // "multi" when the service spans more than one cluster.
  cluster?: string;
  // v0.8.383 — deployment environment chip (deploy_env-led derive,
  // v0.8.380). Carried only on the MV-backed paths (serviceGraph
  // adapters); the sampled /api/service-map response doesn't compute
  // it, so it stays undefined there and no chip renders. A service
  // live in several envs carries the edge-dominant value — strict
  // per-env node separation is a deferred slice (env-separation §5).
  env?: string;
}

export interface ServiceMapEdge {
  caller: string;
  callee: string;
  traceCount: number;
  spanCount: number;
  errorCount: number;
  isNew?: boolean;
  // v0.8.281 — per-edge RED, present ONLY on the MV path (the focus view's
  // serviceGraphAdapter enriches them from GraphEdge; the sampled global
  // /api/service-map never computes latency, so they stay undefined there
  // and the renderer draws no label). errorRate follows the ServiceMap shape
  // convention: a 0..1 fraction, like ServiceMapNode.errorRate.
  rate?: number;      // calls per minute over the window
  errorRate?: number; // 0..1
  avgMs?: number;
  p99Ms?: number;
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
  // Overview top-N cap (v0.8.215). totalNodes = services in the full sampled
  // graph; shownNodes = how many survived the cap. shownNodes < totalNodes ⇒
  // the map is the heaviest subgraph, not the whole truth — UI shows "X of Y".
  totalNodes?: number;
  shownNodes?: number;
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
  // v0.9.289 (operator ask) — capacity of the volumes ClickHouse writes
  // to, from system.disks. Distinct from `tables` above: that says how
  // much room Coremetry's data OCCUPIES, this says how much is LEFT,
  // including everything else on the same filesystem. Retention only
  // means something against the second number. Absent when the
  // credential cannot read system.disks — the panel hides, the page
  // does not break.
  disks?: {
    host?: string;   // set only on a cluster() fan-out
    name: string;
    path: string;
    totalBytes: number;
    freeBytes: number;
    // free space minus what in-flight merges/inserts already claimed —
    // the honest "can I write another part right now" figure
    unreservedBytes: number;
    // operator-configured reserve CH refuses to dip into
    keepFreeBytes: number;
  }[];
  // v0.9.290 (operator ask) — live per-node pressure, from
  // system.asynchronous_metrics / system.metrics / system.server_settings.
  // In-memory counters, so the read is independent of data volume.
  servers?: {
    host?: string;   // set only on a cluster() fan-out
    osMemoryTotal: number;
    osMemoryAvailable: number;
    memoryResident: number;   // the ClickHouse process's RSS
    memoryTracking: number;   // what CH's own allocator accounting sees
    // The two ceilings that produce a code-241 "Query memory limit
    // exceeded". 0 = unlimited. Surfaced because that error quotes a
    // number the operator otherwise has to go find on the node.
    maxServerMemory: number;
    maxQueryMemory: number;
    // configuredQueryMemory (v0.9.975) — the per-query cap BEFORE the
    // server-ratio clamp. Larger than maxQueryMemory means the
    // configured value exceeded the node's own ceiling and could never
    // have fired: CH hits its server-wide OvercommitTracker first, and
    // that kills a VICTIM query rather than the greedy one. Invisible
    // until it kills something innocent, so the panel says it out loud.
    configuredQueryMemory: number;
    // queryMemoryProbeFailed (v0.9.984) — the boot probe never read the
    // server ceiling, so the per-query numbers were NOT proportioned.
    // Different from "nothing needed clamping": with no measurement
    // there is nothing to clamp against, so the clamp warning stays
    // silent even when the configured cap sits above this node's
    // ceiling and can therefore never fire.
    queryMemoryProbeFailed?: boolean;
    // Normalised per core (1.0 = every core saturated). Sampled
    // independently, so the three can sum past 1.0 — show them
    // separately rather than as one clamped total.
    cpuUser: number;
    cpuSystem: number;
    cpuIoWait: number;
    loadAvg1: number;
    runningQueries: number;
    runningMerges: number;
    uptimeSec: number;
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
  // OTLP metric-exemplar ingest totals (cumulative since process start,
  // v0.8.328; UI card v0.8.431 — audit Faz A). droppedNoTrace is the
  // require-trace-context policy gate: INTENTIONAL, not loss.
  exemplars?: {
    ingested: number;
    droppedNoTrace: number;
    // v0.8.433 (Faz C) — per-series×minute ingest cap drops; 0 unless armed.
    droppedCapped?: number;
  };
  // Cumulative ingest data-loss counters since process start (v0.8.x).
  // queueFull = receiver buffer overflow; writeFailed = ClickHouse insert
  // errored and the batch was dropped (not retried). pipeline = records
  // discarded by an operator drop/sample rule (INTENTIONAL, not loss;
  // shown separately from the loss alarm) — v0.8.282.
  drops: {
    spansQueueFull: number;
    logsQueueFull: number;
    metricsQueueFull: number;
    spansWriteFailed: number;
    logsWriteFailed: number;
    metricsWriteFailed: number;
    spansPipeline: number;
    logsPipeline: number;
    metricsPipeline: number;
  };
  // Config/boot conditions that silently degrade reads (v0.8.211).
  health: {
    // `spans` is an external Distributed table but COREMETRY_CH_CLUSTER_NAME is
    // unset → MV insert-triggers never fire, summary MVs stay EMPTY, reads
    // return no/partial results.
    externalDistributedSpansUnset: boolean;
    // The cluster the external `spans` fans to — set COREMETRY_CH_CLUSTER_NAME
    // to this to fix the empty-MV state.
    suggestedClusterName?: string;
    // Redis configured but unreachable → always-leader fallback. In a multi-pod
    // deployment every pod becomes leader → duplicate alerts/notifications.
    lockDegraded: boolean;
  };
  // v0.9.936 — davranış motorunun kendi ölçümü. Motorun tek pahalı yanı
  // 28 GÜNLÜK bir MV taraması; süresi görünmezse "vidaları sıkmalı
  // mıyım" sorusunun cevabı da yok.
  //
  // OPSİYONEL: bu sürümden eski bir backend'e bakan bir tarayıcı sekmesi
  // alanı hiç görmez. Motor bu pod'da koşmuyorsa (COREMETRY_MODE=api, ya
  // da lider başka pod) sayaçlar SIFIR olur ve bu doğru cevaptır.
  behavior?: {
    ticks: number;            // süreç başından beri koşan tarama sayısı
    candidates: number;       // yazılan aday (tavandan SONRA)
    lastUnix: number;         // son taramanın bitiş anı (0 = hiç koşmadı)
    lastDurationMs: number;   // son taramanın toplam süresi
    // v0.9.957 — bütçenin KIRILIMI. Toplam süre tek başına ne
    // yapılacağını söylemiyor: yük 28 günlük MV sorgusundaysa vidalar,
    // olay yazımındaysa toplu yazım hattı sorumludur. v0.9.936'nın 25.6
    // saniyesi bu kırılım olmadığı için "MV pahalı" diye okunmuştu;
    // ölçünce ~20 saniyenin YAZIMDA olduğu çıktı.
    //
    // OPSİYONEL: bu sürümden eski bir backend alanı hiç döndürmez.
    lastQueryMs?: number;
    lastWriteMs?: number;
    lastCandidates: number;
    lastServices: number;
    // Yetersiz geçmiş yüzünden atlanan kova sayısı. SESSİZLİĞİN
    // GEREKÇESİ: motor aday üretmiyorsa bunun "her şey normal" mi yoksa
    // "henüz öğrenecek kadar geçmiş yok" mu olduğunu başka hiçbir ekran
    // söyleyemez.
    lastScarceBuckets?: number;
    // Son taramanın hatası ("" / yok = temiz). Sessiz kapanma bu depoda
    // tekrarlayan hata sınıfı — motor bir CH hatasıyla hiç aday
    // üretmiyor olabilir ve başka hiçbir ekran bunu söylemez.
    lastError?: string;
    lastErrorAtNs?: number; // v0.9.1077 — istisnanın zamanı (yaş etiketi)
  };
  // v0.9.985 — dağıtık kipte Distributed tabloların spool derinliği.
  //
  // NEDEN VAR: Distributed motoru INSERT'i diske spool'layıp HEMEN OK
  // döner; asıl gönderim arka planda *_local'a olur. O gönderici
  // takıldığında uygulama katmanı "yazdım" sanır ve veri hiç inmez —
  // 2026-08-12'de lokal küme 3s39d boyunca tek span yazamazken
  // spans_write_failed 0, spans_accepted tırmanıyordu. Cevap yalnız
  // system.distribution_queue'da.
  //
  // OPSİYONEL: tek-düğüm kurulumunda alan HİÇ GELMEZ (orada Distributed
  // tablo da spool da yok) → panel çizilmez. Eski bir backend de
  // döndürmez.
  distributionQueue?: {
    // measured=false → ölçüm YAPILAMADI. "Temiz" ile karıştırılmamalı:
    // düşen bir probe de files=0 gösterir (v0.9.984 fail-open dersi).
    measured: boolean;
    // v0.9.986 — küme geneli okuma düştü, sayılar YALNIZ bu düğümün
    // spool'u. Yaklaşıklık itiraf edilir; sessiz kırpma "hepsi bu" diye
    // okunurdu. (Fan-out tam da ölçmek istediğimiz arızada yavaşlıyor.)
    partial?: boolean;
    probeError?: string;
    files: number;
    bytes: number;
    brokenFiles: number;
    errorCount: number;
    tables?: {
      table: string;
      files: number;
      bytes: number;
      // CH'nin gönderemeyip kalıcı olarak kenara koyduğu dosyalar —
      // bir daha denenmez, yani gerçek veri kaybı.
      brokenFiles: number;
      // Sunucu açılışından beri KÜMÜLATİF; tek başına "şu an bozuk"
      // demek değildir.
      errorCount: number;
      lastError?: string;
      lastErrorAtNs?: number; // v0.9.1077 — istisnanın zamanı (yaş etiketi)
    }[];
    generated: number;
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
  // v0.8.436 — deriver-filled logical namespace (service.namespace /
  // k8s.namespace.name); flow-graph gruplandırmanın veri kaynağı.
  namespace?: string;
  // v0.9.25 — deriver-filled workload adı (k8s.deployment.name);
  // Servis→Cluster pivotunun &deployment= hassasiyeti.
  deployment?: string;
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
  // v0.9.393 — hücrenin temsilci trace_id'si (en yavaş span'in trace'i;
  // '' = boş). Tık→trace garantisi + ◆ overlay bunun üstünden.
  exemplars?: string[][];
  maxCount: number;
  // v0.9.110 (C2 review fix) — when the TOP bin is a +Inf overflow (a
  // histogram's ">highest explicit bound" bucket), its durationBins entry is
  // synthetic. Set true so the viz labels it "> {prev}" / "+∞" instead of
  // asserting a fabricated finite ceiling ("≤ 1.5s") — the top band lights up
  // during a latency incident and the axis is the only magnitude cue.
  overflowTop?: boolean;
  // Noun for the per-cell count in the tooltip ('spans' default for the
  // span-derived heatmap; 'samples' for a metric-histogram heatmap).
  countNoun?: string;
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

// ChartAnnotation (v0.8.284, A7) — one operator-event marker rendered as a
// vertical annotation line on a uPlot time-series chart. Produced by
// annotationsInWindow (lib/chartAnnotations.ts) from /api/operator-events and
// consumed by the TimeSeriesPanel draw hook. timeUnixNs is unix nanoseconds;
// kind (deploy|config|incident|maintenance|custom) drives the marker colour.
export interface ChartAnnotation {
  timeUnixNs: number;
  kind: string;
  label: string;
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
  // v0.9.272 — the actual database (Oracle service name / SID, PostgreSQL or
  // MongoDB db name). The DB column showed dbSystem, i.e. the engine word, so
  // every Oracle row read "oracle" while the real databases are named
  // COREBANK / CARDS / DWH. dbNameCount > 1 means this (service, statement)
  // pair touched more than one database in the window and dbName is one of
  // them — grouping still folds db_name, so the count is shown rather than
  // silently picking a winner.
  dbName: string;
  dbNameCount: number;
  avgMs: number;
  // v0.9.265 — P50 separates "slow for everyone" from "slow in the tail",
  // which avg alone cannot: avg is dragged upward by the same tail. All
  // three producing queries project it (see the alignment guard in
  // internal/chstore/quantile_ordinal_test.go).
  p50Ms: number;
  p95Ms: number;
  p99Ms: number;
  maxMs: number;
  errorCount: number;
  totalMs: number;
  // stmtHash — persistent statement identity (v0.8.375, Stage-2 D1):
  // spans.db_stmt_hash as a DECIMAL STRING (a uint64 in a JSON number
  // loses precision past 2^53). The statement detail drawer keys on it.
  // Optional: absent on responses served from a pre-D1 cache entry, so
  // every consumer must degrade to "no detail link", never render a
  // link to `undefined`.
  //
  // v0.9.963 (UX denetimi G1-b) — moved up from SlowQueryRow. The
  // per-service DB panel fills this same interface and had no identity,
  // so its rows dead-ended at /traces.
  stmtHash?: string;
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
// called (render as a progress chip); `delta` = a live answer token
// chunk (v0.8.404, guided path only — append into the pending bubble);
// `answer` = final prose (REPLACES any streamed deltas — it is the
// source of truth, so old backends that never send delta and new
// backends falling back to buffered both render identically);
// `error` = failure; `done` = stream closed.
// exchangeId (v0.8.399) — server-minted correlation key for the
// thumbs up/down feedback POST; optional for rolling-deploy safety
// (an old backend's answer events lack it → thumbs row just hides).
// RagSource (v0.8.438) — RAG cevabının kaynak atfı: doküman adı +
// chunk sırası (+ url kaynağında sayfa adresi).
export interface RagSource {
  doc: string;
  ref?: string;
  chunk: number;
  score: number;
}

// ChatAnswerLink (v0.9.419) — guided cevabın altındaki derin-link çipi;
// sunucu rotadan deterministik üretir (LLM biçimlemesine güvenilmez).
export interface ChatAnswerLink { label: string; href: string }

// ChatTurn (v0.9.479) — ekranda çizilen bir sohbet turu: wire shape'i
// (ChatMessage) + yalnız UI'ın bildiği alanlar. İKİ yüzey paylaşır —
// global CoSRE penceresi (CopilotChat) ve AI çekmecesi içindeki sohbet
// (AIDrawer) — bu yüzden bileşen dosyasında değil burada yaşar.
export interface ChatTurn extends ChatMessage {
  steps?: string[];
  pending?: boolean;
  error?: string;
  exchangeId?: string;
  verdict?: 1 | -1;
  sources?: RagSource[];
  // v0.9.411 — sunucunun rotadan türettiği konuya-duyarlı takip
  // önerileri; varsa statik liste yerine bunlar çizilir.
  suggestions?: string[];
  // v0.9.419 — rotadan türetilen derin-link çipleri.
  links?: ChatAnswerLink[];
}

export type ChatStreamEvent =
  | { kind: 'step'; tool: string; args: string }
  | { kind: 'delta'; text: string }
  // suggestions (v0.9.411) — guided cevabın rotasından türetilen
  // konuya-duyarlı takip önerileri; yoksa frontend statik listesine düşer.
  // links (v0.9.419) — rotadan türetilen deterministik derin linkler.
  | { kind: 'answer'; text: string; exchangeId?: string; sources?: RagSource[]; suggestions?: string[]; links?: ChatAnswerLink[] }
  | { kind: 'error'; error: string }
  | { kind: 'done'; ok: boolean };

// RAG doküman katalog satırı + config görünümü (v0.8.438).
export interface RagDocument {
  docId: string;
  docName: string;
  source: string;
  sourceRef?: string;
  uploadedBy?: string;
  chunks: number;
  bytes: number;
  updatedAt: number;
}
// APIToken (v0.8.444) — harici agent platformları için servis kimliği.
// Düz token yalnız create yanıtında bir kez görünür.
export interface APIToken {
  id: string;
  name: string;
  role: string;
  prefix: string;
  createdBy: string;
  createdAt: number;
  revoked: boolean;
}

export interface RagConfigView {
  endpoint: string;
  model: string;
  enabled: boolean;
  topK?: number;
  hasKey: boolean;
  // v0.9.23 — self-signed embedding/wiki endpoint'leri için (sır değil).
  insecureSkipVerify?: boolean;
  // v0.8.442 — wiki/URL kaynakları (authHeader asla geri dönmez).
  // v0.8.451 — Basic auth: username görünür döner, password yalnız
  // "********" varlık sentineli olarak (on-prem Azure DevOps; PAT'te
  // username boş bırakılır).
  sources?: { url: string; authHeader?: string; username?: string; password?: string }[];
}

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

// Rollout (v0.8.x) — one detected pod-churn event: a time bucket
// where the service's active instance set turned over (old pods
// gone + new in) = a rollout / restart. Replaces version-bump deploy
// markers when service.version is constant. `impact` reuses the same
// before/after RED shape as a version deploy.
export interface Rollout {
  timeUnixNs: number;
  // v0.8.405 — "deploy" (version changed) vs "restart" (pods replaced
  // at the SAME version: reschedule/crash/HPA wave). Deploy chips and
  // markers key on "deploy"; restarts render muted.
  kind?: 'deploy' | 'restart';
  podsAdded: number;
  podsRemoved: number;
  activePods: number;
  addedPods?: string[];
  removedPods?: string[];
  versionBefore?: string;
  versionAfter?: string;
  impact?: DeployImpact | null;
}
// v0.9.435 — filo Deploys/Rollouts geçmişi (/deploys sayfası).
export interface RecentDeployEntry {
  service: string;
  version: string;
  firstSeenNs: number;
  spanCount: number;
  // v0.9.436 — en yeni N deploy için önce/sonra RED deltası (opsiyonel).
  impact?: DeployImpact;
}
export interface FleetRollout extends Rollout { service: string }
export interface DeploysHistoryResponse {
  deploys: RecentDeployEntry[];
  rollouts: FleetRollout[];
  scannedServices: number;
  candidateCapped: boolean;
  deploysTruncated?: boolean;
  rolloutWindowClamped?: boolean;
  rolloutScanErrors?: number;
  impactComputed?: number;
}

export interface RolloutsResult {
  service: string;
  rollouts: Rollout[];
  // versionConstant — the effective service.version never changed
  // across the window; the UI hides the version chip so "1.0.0"
  // isn't rendered on every surface.
  versionConstant: boolean;
  // instancesTracked — false when the service emits no pod identity
  // (k8s.pod.name / service.instance.id / host.name), so churn
  // can't be computed.
  instancesTracked: boolean;
}

// SlowQueryRow — same as DBQueryStat plus the originating
// service. Drives the global slow-query catalog (v0.5.165) on
// /databases/slow-queries — operator-facing answer to "what
// query class is burning the most DB time across the whole
// install?".
export interface SlowQueryRow extends DBQueryStat {
  service: string;
  // stmtHash is inherited from DBQueryStat since v0.9.963 — both catalogs
  // key the statement detail drawer off the same field.
}

// DBStmtDetail — v0.8.378 (Stage-2 slice D2). One payload for the
// /slow-queries statement detail drawer (/api/databases/statements/
// detail): window summary, 5m-grain trend, per-service caller breakdown,
// true exemplar trace pivots. Every section is null when its backend
// read failed — the drawer renders per-section fallbacks, never blanks.
export interface DBStmtSummary {
  sampleStatement: string;
  dbSystem: string;
  dbName: string;
  calls: number;
  errors: number;
  totalMs: number;
  avgMs: number;
  p95Ms: number;
  p99Ms: number;
  maxMs: number;
  // Prior-window values — present only on ?compare=prior responses
  // (Endpoints v0.5.404 pattern). Absent = statement is NEW.
  priorCalls?: number;
  priorErrors?: number;
  priorAvgMs?: number;
  priorP95Ms?: number;
}

export interface DBStmtTrendPoint {
  tsNs: number;
  calls: number;
  errors: number;
  avgMs: number;
  p95Ms: number;
}

export interface DBStmtCaller {
  service: string;
  calls: number;
  errors: number;
  avgMs: number;
  p95Ms: number;
  totalMs: number;
  priorCalls?: number;
  priorErrors?: number;
  priorAvgMs?: number;
  priorP95Ms?: number;
}

export interface DBStmtDetail {
  // The v0.8.375 stmt_hash echoed back as a decimal string.
  stmtHash: string;
  // '?'-normalized display form (re-derived from the bucket sample);
  // absent when the summary section missed — fall back to the row.
  statement?: string;
  fromNs: number;
  toNs: number;
  summary: DBStmtSummary | null;
  trend: DBStmtTrendPoint[] | null;
  // Bucket width of the trend series in seconds (5m-grain multiple) —
  // densify sparse buckets against [fromNs, toNs] with this step.
  trendBucketSec?: number;
  callers: DBStmtCaller[] | null;
  exemplars: { slowTraceId?: string; errorTraceId?: string } | null;
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

// v0.8.332 (pivot Phase 3) — a REAL OTLP exemplar from GET /api/exemplars:
// the SDK-recorded {value, trace} sample attached to a metric data point at
// ingest (pivot Phase 1), as opposed to the span-DERIVED SpanExemplar /
// MetricExemplar above. `ts` is unix ns; `attrs` are the exemplar's filtered
// attributes (Go omitempty).
export interface OtlpExemplar {
  ts: number;
  value: number;
  traceId: string;
  spanId: string;
  attrs?: Record<string, string>;
  // v0.8.432 (audit Faz B) — /api/exemplars/by-series items carry the
  // chart series' groupKey so grouped charts attribute each ◆ to the
  // right line; absent on the legacy single-series endpoint.
  groupKey?: string[];
}

// v0.8.332 — one OTel span-link row from GET /api/traces/{id}/links
// (chstore.SpanLink JSON verbatim). Both directions project the same
// columns: an OUTGOING row belongs to the viewed trace (linkedTraceId is the
// other trace); an INCOMING row belongs to the OTHER trace (traceId is the
// other trace, linkedTraceId is the one being viewed).
export interface SpanLink {
  traceId: string;
  spanId: string;
  linkedTraceId: string;
  linkedSpanId: string;
  timeUnixNs: number;
  serviceName: string;
  attrs?: Record<string, string>;
}

export interface TraceLinks {
  outgoing: SpanLink[];
  incoming: SpanLink[];
}

// v0.8.232 — UI-managed logstore (Settings → Elasticsearch). Snapshot
// is the secret-free GET view; input's empty password/apiKey preserves
// the stored value (tempo-token contract).
export interface ESLogstoreFieldMap {
  timestamp?: string;
  traceId?: string;
  spanId?: string;
  service?: string;
  body?: string;
  severityNo?: string;
  severityTx?: string;
  // v0.8.400 — deployment-environment field for the ?env= filter.
  // Empty = self-discover via a cached field_caps over the candidate
  // shapes (backend es_env_field.go).
  env?: string;
}

export interface ESLogstoreSnapshot {
  backend: 'clickhouse' | 'elasticsearch';
  addresses: string[];
  username: string;
  hasPassword: boolean;
  hasApiKey: boolean;
  insecureSkipVerify: boolean;
  index: string;
  indexTemplate: string;
  fields: ESLogstoreFieldMap;
  source: 'env' | 'ui'; // env/YAML bootstrap vs persisted UI override
}

export interface ESLogstoreInput {
  backend: string;
  addresses: string[];
  username?: string;
  password?: string;
  apiKey?: string;
  insecureSkipVerify?: boolean;
  index?: string;
  indexTemplate?: string;
  fields?: ESLogstoreFieldMap;
}

// v0.8.230 — one failed ES query captured by the logstore diagnostics
// ring (backend `recordQueryError`). `query` is the exact request body
// Coremetry sent (truncated at 4 KiB) so the operator can replay it
// with curl against their cluster.
export interface ESQueryError {
  at: number;     // unix ms
  op: string;     // search | tail search | histogram | msearch count-patterns | eql search
  index: string;  // resolved concrete index list the query targeted
  query: string;
  status: number; // HTTP status; 0 = transport error (ES unreachable)
  error: string;
}

// v0.8.348 — pivot Phase 1c: logstore trace-context SELF-DISCOVERY
// (GET /api/admin/logstore/trace-context). The system verifies its OWN
// configured backend: trace-id field mapping verdict + 24h coverage.
// One candidate trace-id field shape as the backend maps it.
export interface TraceContextField {
  name: string;
  types: string[];      // mapping types found (sorted); empty = absent
  searchable: boolean;
  aggregatable: boolean;
  configured: boolean;  // the operator-configured TraceID field
}

export interface TraceContextServiceCoverage {
  service: string;
  total: number;
  withTrace: number;
}

export interface TraceContextReport {
  available: boolean;
  reason?: string;      // set on failure; may accompany available:true when only coverage failed
  effectiveField: string;
  effectiveType: string; // 'keyword' | 'text' | … | 'absent' (ES); 'String' (CH)
  pivotReady: boolean;
  fields: TraceContextField[];
  windowHours: number;
  total: number;
  withTrace: number;
  services: TraceContextServiceCoverage[];
}

export interface TraceContextPayload {
  backend: string;
  report: TraceContextReport;
}

// Result of the admin "purge telemetry data" factory-reset.
export interface PurgeResult {
  tablesPurged: string[];
  skipped?: string[]; // absent on this install (e.g. op_group MV)
  errors?: string[];  // per-table failures (best-effort: purge continued)
}

// v0.8.446 — /external third-party API inventory (Wave 3 / A1).
// Rows derive from topology_edges_5m external edges (client spans
// with a peer.service); display/category come from the server-side
// vendor catalogue and are absent for unrecognised hosts.
export interface ExternalHost {
  host: string;
  display?: string;
  category?: string;
  callers: number;
  callerNames: string[];
  calls: number;
  errors: number;
  errorRate: number;
  avgMs: number;
  p99Ms: number;
  topLabels: string[];
}

export interface ExternalCaller {
  service: string;
  calls: number;
  errors: number;
  errorRate: number;
  avgMs: number;
  p99Ms: number;
  topLabels: string[];
}

// One 5-minute bucket of a host's RED trend; bucket = unix seconds.
export interface ExternalTrendPoint {
  bucket: number;
  calls: number;
  errors: number;
  avgMs: number;
  p99Ms: number;
}

export interface ExternalHostDetail {
  host: string;
  display?: string;
  category?: string;
  callers: ExternalCaller[];
  trend: ExternalTrendPoint[];
}

// v0.8.449 — /hosts inventory (Wave 3 / A4): one row per host/pod
// emitting metrics in the window; the global sibling of the Service
// Overview Instances card.
export interface HostRow {
  host: string;
  zone?: string;
  services: string[];
  cpuPct: number;
  memBytes: number;
  memPct: number; // 0 when no memory limit is reported
  up: boolean;
  lastSeen: number; // unix ns
}

export interface HostServiceRow {
  service: string;
  cpuPct: number;
  memBytes: number;
  lastSeen: number;
}

// One minute of a host's trend; bucket = unix seconds.
export interface HostTrendPoint {
  bucket: number;
  cpuPct: number;
  memBytes: number;
}

export interface HostDetail {
  host: string;
  zone?: string;
  services: HostServiceRow[];
  trend: HostTrendPoint[];
}

// AnnotationItem — v0.9.394/395 annotation şeridi olay modeli
// (backend api/annotation_routes.go ile birebir).
export interface AnnotationItem {
  ts: number; // unix ns
  kind: 'deploy' | 'rollout' | 'alert_fired' | 'alert_resolved' | 'anomaly' | 'event';
  title: string;
  service?: string;
  targetType?: 'problem' | 'anomaly' | 'event' | 'rollout';
  targetId?: string;
  link?: string;
}
export interface AnnotationsResponse {
  items: AnnotationItem[] | null;
  truncated: boolean;
}

// v0.9.638 — /traces "Toplamı göster" sayısı.
//
// reason DOLU ise sayı YOK ve sebebi var: bazı şekiller (süre filtresi,
// servis+post-agg, MV'yi kapatan filtreler) trace_summary_5m'de ucuza
// sayılamıyor. "Yanlış sayı, sayı yokluğundan kötüdür" ilkesinin
// devamı — pahalı bir sayı da dürüst bir retten kötüdür.
//
// atLeast: tavana değildi, gerçek sayı DAHA BÜYÜK ("10.000+").
export interface TraceCountResponse {
  value: number;
  atLeast: boolean;
  reason?: 'raw-path-filter' | 'duration-filter' | 'service+filter';
}

// v0.9.657 — dış log sistemi köprü şablonları (v0.9.655 backend'i).
//
// Ortam → URL şablonu. "default" soneksiz (prod) servisler için; int/uat/
// prep servis adının SONEKİNDEN çözülüyor. Şablon {value} yer tutucusunu
// taşımak zorunda — backend doğruluyor.
export interface CorrelationLinkSettings {
  templates: Record<string, string>;
  placeholder: string;
  envs: string[];
}

// v0.9.775 — exception triyaj basamağının pencereleri (backend:
// chstore.ExceptionTriageConfig / system_settings key "exception_triage").
//
// Sabit olarak üç kez öteledikten sonra (v0.9.627, v0.9.699, 2026-08-08)
// operatörün eline verildi: "ne kadar taze hâlâ acildir" filoya ve nöbet
// devrine göre değişiyor, kodda tahmin edilecek bir şey değil.
export interface ExceptionTriageConfig {
  // Patlamanın P1 kaldığı tazelik penceresi (saat). Aynı pencere,
  // patlama olmayan ama ≥100 hacimli grupların P2 kapısı için de
  // kullanılır — iki kapı ayrı sabitlere bağlıysa satır P2'yi atlayıp
  // P3'e düşüyor (v0.9.699).
  p1FreshHours: number;
  // Patlamanın P2 ("bugün") kaldığı pencere; sonrası P3. p1FreshHours'tan
  // küçük olamaz.
  p2SameDayHours: number;
  // Yeni olay görmeyen açık/ack'li bir grubun kendiliğinden resolved'a
  // geçmesi için gereken sessizlik (saat).
  staleResolveHours: number;
}

// v0.9.838 — ALERT PROBLEMİ öncelik merdiveninin vidaları (backend:
// chstore.ProblemPriorityConfig / system_settings key "problem_priority").
//
// ExceptionTriageConfig'ten AYRI hat: o exception gruplarının, bu alert
// kurallarının (threshold + SLO burn) P1/P2/P3 basamağı. Operatör-
// bildirimli: "hâlâ çok fazla alert rule'dan P1 geliyor" — prod'da 29
// critical'in 22'si P1'di. Varsayılanlar v0.9.838 öncesi sabitlerin
// birebir aynısı; bu sürüm davranış değil VİDA getirdi.
export interface ProblemPriorityConfig {
  // Büyük ihlal kapısı: değer eşiğin bu kadar katına çıktığında
  // (">" kuralları) ya da bu kadar katı altına düştüğünde ("<" kuralları,
  // oran ters çevrilir) ihlal büyük sayılır. critical + büyük ihlal = P1,
  // warning + büyük ihlal = P2. Varsayılan 2.0, alt sınır 1.1.
  bigBreachRatio: number;
  // Bir critical problem bu kadar saattir AÇIKSA tek başına P1'e terfi
  // eder. Varsayılan 4. 0 = terfi tamamen kapalı.
  staleCriticalHours: number;
}

// v0.9.1036 — failure-rate (%) SLO eşiği (backend:
// chstore.FailureSLOConfig / system_settings key "failure_slo").
//
// Latency SLO'sunun eksik ikizi: hata-oranı grafiğinde yatay bir eşik
// çizgisi görebilmek bugüne dek o servis için elle bir *availability*
// SLO'su açmayı gerektiriyordu. Bu blob filo-geneli bir varsayılan (%1)
// + servis başına override taşıyor. PARALEL ŞEMA DEĞİL: gerçek bir
// availability SLO'su varsa çizgi ondan gelir ve bu blob konuşmaz —
// çözümlemenin tek yeri lib/failureSlo.ts.
export interface FailureSLOConfig {
  // Filo geneli varsayılan, YÜZDE (1 = %1). 0 = varsayılan çizgi yok.
  defaultPct: number;
  // Servis adı → yüzde. Varsayılanı ezer.
  overrides?: Record<string, number>;
}

// v0.9.797 — metrik route dışlama kuralı (backend:
// chstore.MetricExclusionRule / system_settings key "metric_exclusions").
//
// Healthcheck / probe route'ları grafiklerden düşürmek için. İki kademe:
// okuma filtresi HER ZAMAN (geçmiş dahil, geri alınabilir) ve opsiyonel
// ingest drop'u (kural başına çekbox — yazılmayan datapoint geri gelmez).
export interface MetricExclusionRule {
  // Tam metrik adı ya da '*' (her metrik).
  metric: string;
  // Bugün yalnız 'http.route'. Alan modelde: genişletme bir şema
  // değişikliği değil bir doğrulama gevşetmesi olsun.
  attrKey?: string;
  // RE2 deseni, ANKORSUZ: '/health' yolun herhangi bir yerinde eşleşir.
  // Tam eşleşme için ^...$ yazılır.
  pattern: string;
  // Datapoint hiç yazılmasın. Türetilmiş bir Pipeline kuralı olarak
  // uygulanır (tek drop motoru) — Settings → Pipeline'da da görünür.
  dropAtIngest?: boolean;
}

export interface MetricExclusions {
  rules: MetricExclusionRule[];
}

// v0.9.800 — anomali dedektörünün İZLEDİĞİ metrik seti (backend:
// chstore.AnomalyTrackedConfig / system_settings key "anomaly_tracked").
//
// Anahtarlar metrik ADLARI (error_rate / p99_ms / request_rate), alan
// adları değil — o yüzden snake_case: aynı kimlikler alarm kurallarında
// da bu yazımla geçiyor (alerts/constants.ts). Sunucu her zaman kanonik
// üçlüyü döndürür; bilinmeyen anahtar okuma yolunda düşürülür.
//
// Varsayılan: error_rate + p99_ms açık, request_rate KAPALI (operatör
// 2026-08-09: request_rate anomalileri false-pozitif).
export type AnomalyTrackedConfig = Record<string, boolean>;

// v0.9.826 — anomali dedektörünün EŞİKLERİ (backend:
// chstore.AnomalySensitivityConfig / system_settings key
// "anomaly_sensitivity").
//
// anomaly_tracked'in kardeşi: o hangi metriğin ÖLÇÜLECEĞİNİ ayarlar,
// bu ölçülenin ne zaman OLAY sayılacağını.
//
// Beş alan da BİRLİKTE "bu sapma bir olay mı" sorusunu cevaplıyor ama
// farklı boşlukları kapatıyorlar; biri diğerinin yerine geçmez. 0 =
// vida kapalı (meşru bir istek); negatif değer sunucuda varsayılana
// döner.
export interface AnomalyMetricSensitivity {
  // Göreli değişim tabanı: |current-median|/|median|.
  floorPct: number;
  // Mutlak DEĞER tabanı — current bunun altındaysa yükseliş yönlü
  // anomali açılmaz. Düşüşleri etkilemez.
  absFloor: number;
  // Mutlak FARK tabanı — |current-median| bunun altındaysa açılmaz.
  minAbsDelta: number;
  // MAD'in alt sınırı, MAX olarak uygulanır. Sıkı baseline'da z'nin
  // patlamasını engeller.
  minMAD: number;
  // Hacim kapısı (istek/sn) — son bucket bunun altındaysa AÇILMAZ.
  // Çözülmeye uygulanmaz.
  minBaselineRate: number;
}

export interface AnomalySensitivityConfig {
  metrics: Record<string, AnomalyMetricSensitivity>;
  // Açılmak için üst üste ateşlemesi gereken 5-dk bucket sayısı.
  dwellBuckets: number;
  // Bu |z|'nin üstü critical. Dedektör YALNIZ critical verdict'te
  // Problem açtığı için (v0.9.193) bu fiilen açılma eşiğidir.
  criticalZ: number;
  // v0.9.827 — dedektörün açtığı metrik problemi otomatik incident'a
  // bağlansın mı?
  //
  // OPSİYONEL çünkü backend'de *bool: bu sürümden ESKİ settings
  // satırlarında alan hiç yok ve "yok" = bugünkü davranış (bağla).
  // Bu yüzden okuma daima `!== false` ile yapılmalı, `=== true` ile
  // DEĞİL — ikincisi eski satırları sessizce kapalı gösterirdi.
  attachToIncident?: boolean;
  // v0.9.936 — davranış motoru AŞAMA 1'in vidaları. Üstteki alanlar
  // ANİ sapmayı (5-dk pencere, 24s geçmiş) ayarlıyor; bu bölüm KALICI
  // davranış değişimini (haftanın saati baseline'ı, 28 gün).
  //
  // OPSİYONEL çünkü bu sürümden ESKİ settings satırlarında alan yok;
  // sunucu Normalize'da varsayılanlarla dolduruyor, ama tip GET'in
  // döndürebileceği her şekli kabul etmeli.
  behavior?: AnomalyBehaviorConfig;
}

// v0.9.936 — davranış motorunun eşikleri (backend:
// chstore.AnomalyBehaviorConfig).
//
// Ani-sapma vidalarından AYRI olması bilinçli: "şu an sıçradı mı" ile
// "bu servis artık başka türlü mü davranıyor" aynı eşikle
// cevaplanmaz. Bir rejim kayması 1.5× ile gerçektir ve 6σ'ya hiç
// ulaşmayabilir.
export interface AnomalyBehaviorConfig {
  // Motor koşsun mu? OPSİYONEL çünkü backend'de *bool ve varsayılan
  // AÇIK — okuma daima `!== false` ile yapılmalı (attachToIncident ile
  // aynı tuzak).
  enabled?: boolean;
  // Mevsimsel sapmanın açılma eşiği (robust z, σ).
  seasonalZ: number;
  // Rejim kaymasının açılma oranı (× baseline medyanı). Düşüş
  // tarafında karşılığı 1/regimeRatio.
  regimeRatio: number;
  // Kaç ardışık 5-dk dilimin AYNI yönde ateşlemesi gerektiği.
  dwellSeasonal: number;
  dwellRegime: number;
  // Fırtına koruması: tik başına yazılacak EN GÜÇLÜ aday sayısı.
  maxCandidatesPerTick: number;
  // v0.9.957 — örnek-kıtlığı kapısının iki boyutu. OPSİYONEL çünkü bu
  // sürümden ESKİ settings satırlarında alan yok; sunucu Normalize'da
  // varsayılanlarla dolduruyor.
  //
  // minSamplesPerBucket : kova başına asgari 5-dk örneği (vars. 12).
  // minBucketRepeats    : kovanın kaç FARKLI GÜNDEN geldiği (vars. 3).
  //
  // İkisi AYRI soru: 24 örnek bol görünür ama hepsi iki günden
  // geliyorsa mevsimsel yayılım n=2'den kestiriliyor demektir ve z
  // patlar. Ölçülmüş vaka: lokal 9 günlük geçmişte tek tikte 178 aday.
  minSamplesPerBucket?: number;
  minBucketRepeats?: number;
}

// BehaviorChangeDetails — `behavior_change` kindli bir AnomalyEvent'in
// `sample` alanındaki JSON. Backend: internal/anomaly.behaviorDetails.
//
// Neden `sample`: yeni kolon/tablo açmamak için (invariant #5 ile aynı
// duruş). Alan zaten "tespit anında yakalanan kanıt" demek; log/trace
// kindlerinde serbest metin, burada yapılandırılmış kanıt.
//
// Ayrıştırma DAİMA try/catch ile: eski bir satır ya da elle düzenlenmiş
// bir kayıt geçerli JSON olmayabilir ve çekmece ham metne düşmeli,
// patlamamalı.
export interface BehaviorChangeDetails {
  metric: string;
  signal: 'seasonal' | 'regime';
  direction: 'up' | 'down';
  ratio: number;
  z: number;
  baseline: number;
  current: number;
  unit: string;
  hourOfWeek: number;   // 0..167, UTC, pazartesi=0
  dwell: number;        // kaç 5-dk dilim sürdü
  onsetNs: number;      // kaymanın başlangıcı
  deploy?: { version: string; ageSeconds: number };
}

// ── ServiceCharts AI çekmecesi (v0.9.1031, onaylı mockup) ──────────────
// /api/copilot/explain-charts yanıtı. Anlatım (explanation) ile KANIT
// (signals) AYRI yollardan gelir: sinyaller CH'den deterministik toplanır,
// yalnız düzyazı LLM'den. Model hata verse bile tablo doğru kalır.

export interface ChartDeploySignal {
  timeUnixNs: number;
  /** "deploy" (sürüm değişti) | "restart" (aynı sürüm, pod değişti) */
  kind: string;
  versionBefore?: string;
  versionAfter?: string;
  podsReplaced: number;
}

export interface ChartProblemSignal {
  id: string;
  title: string;
  severity: string;
  priority?: string;
  startedAt: number;
  metric?: string;
  value: number;
  threshold: number;
}

export interface ChartAnomalySignal {
  id: string;
  kind: string;
  pattern: string;
  startedAt: number;
  peakRatio: number;
  status: string;
}

/** Bir operasyonun pencere vs bir-önceki-eş-pencere değişimi. */
export interface OpDelta {
  name: string;
  calls: number;
  /** cur.p95 / prior.p95 — 1 = değişim yok, 0 = ölçülemedi (asla Infinity). */
  p95Ratio: number;
  /** Hata oranı farkı YÜZDE PUANI (backend ErrorRate 0..100 ölçeğinde). */
  errDeltaPp: number;
  /** Önceki pencerede hiç görülmemiş operasyon. */
  isNew?: boolean;
}

export interface ServiceChartsSignals {
  deploy?: ChartDeploySignal;
  problems?: ChartProblemSignal[];
  anomalies?: ChartAnomalySignal[];
  opDeltas?: OpDelta[];
  /** "En kötü N" listesine girmeyen operasyon sayısı ("diğer M: değişim yok"). */
  otherOps: number;
}

export interface ServiceChartsExplain {
  explanation: string;
  scope: string;
  signals: ServiceChartsSignals;
  /**
   * Anlatım üretilemedi (kota/timeout/sağlayıcı hatası). İstek yine de
   * 200 döner ve `signals` doludur — kanıt anlatımdan bağımsızdır
   * (v0.9.1034).
   */
  error?: string;
}
