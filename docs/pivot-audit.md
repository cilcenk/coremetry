> **TARİHSEL DOKÜMAN — v0.8.328 ÖNCESİ DURUM.** Buradaki "exemplar'lar
> düşürülüyor / tablo yok" bulguları bu audit'in motive ettiği işten
> ÖNCEKİ hali anlatır; pipeline v0.8.328–335'te şipariş edildi. Güncel
> durum + kalan boşluklar: `docs/audits/exemplar-extraction-audit.md`
> (v0.8.431). Satır referansları bayat olabilir.

# Cross-Signal Pivot Audit (Phase 0) — 2026-07-06

Goal: every signal one click from every related signal (trace ↔ log ↔ metric).
Scope per brief: join keys, ingest completeness (CH signals), query-time log
federation. No topology work. This document is read-only findings + the phased
file-level plan; **no code has been written**.

Top-line: the pivot UX shell already exists (`/api/correlate/context` +
CorrelationContextDrawer, Trace Logs tab, log-row→trace links, exemplar ◆ dots
on charts). What's missing is the DATA underneath: real OTLP exemplars and span
links are dropped at ingest, and metric series have no persisted identity.

---

## 1. ClickHouse join-key inventory

### spans (chstore/store.go:420-456)
| Key | Type | Notes |
|---|---|---|
| trace_id | String (hex, lowercase) | `hex.EncodeToString` at otlp/convert.go:268; bloom idx `idx_trace bloom_filter(0.01) G4` (store.go:450) |
| span_id / parent_id | String (hex) | parent collapses nil/all-zero → "" (convert.go:280) |
| time | DateTime64(9) | ns precision; ORDER BY (service_name, time), PARTITION toDate(time) |
| service_name, deploy_env, host_name | LowCardinality columns | promoted at ingest |
| service.instance.id | NOT a column | only in res_keys/res_values arrays; read via `indexOf` (backtrace.go:56) |
| attrs/resource | parallel arrays attr_keys/attr_values + res_keys/res_values | NOT Map |
| events | String JSON blob | kept |
| cluster / op_group | MATERIALIZED / conditional | hasXCol-probed (distributed-safety, store.go:23-55) |

No String-vs-FixedString mismatch exists anywhere: trace_id is hex String in
spans AND ClickHouse logs (store.go:459-460) — pivots are same-type joins.

### metric_points (store.go:1041-1076)
- ORDER BY (service_name, metric, time), PARTITION toDate(time), TTL MetricsDays.
- **CRITICAL FINDING: no fingerprint/series-id column exists.** Series identity
  is implicit, computed per-query: `metricquery.go:79-104` builds group keys as
  `attr_values[indexOf(attr_keys,k)]` expressions and GROUP BYs the tuple.
  There is no formula to "align with" — Phase 1 must INTRODUCE the identity,
  and can standardize it cleanly (no legacy mismatch risk).
- service.name/host.name promoted; `service.instance.id` NOT materialized
  (available at ingest time from the OTLP resource, which is where the
  fingerprint must be computed anyway).
- No exemplar columns/tables. The only "exemplars" today are **span-derived**:
  `spanmetrics_{1s,10s,1m}` MVs carry `argMaxState(trace_id, duration)` slow/
  error exemplar states (store.go:1846-1883), read via `FindExemplarRollup`
  (chstore/exemplar.go:87). These power today's metric→trace pivot and stay —
  OTLP exemplars are additive truth, not a replacement.

### logs in ClickHouse (only when COREMETRY_LOGS_BACKEND=clickhouse)
- trace_id/span_id String DEFAULT '' present (store.go:459-460); OTLP log
  ingest fills them (convert.go:140).
- **GAP: no skip index on trace_id** (only `idx_body tokenbf`); ORDER BY
  (service_name, severity_num, time). Bare `WHERE trace_id=` is a partition
  scan — today mitigated by callers passing a ±1m window (Trace.tsx:57-62).
  Fix: additive `INDEX idx_logs_trace trace_id TYPE bloom_filter(0.01) G4`.

## 2. OTLP ingest drop points (internal/otlp/convert.go)

| Data | Status | Where |
|---|---|---|
| Metric exemplars (Sum/Gauge/Hist/ExpHist) | **DROPPED — `dp.Exemplars` never read** | convertMetric :206-262 |
| Span links | **DROPPED — `sp.Links` never read** | convertSpan :46-110 |
| Span events | kept (JSON blob) | :79-82 |
| Resource attrs | kept (arrays), 2-3 promoted | :31,122,166 |
| Log trace/span ids | kept | :140 |

Drop counters: `Ingester` uses `atomic.Uint64` + accessor + SystemStats
surfacing (http.go:37-119, sysstats.go) — the pattern the new
`exemplars_ingested_total` / `exemplars_dropped_no_trace` counters copy.
(OTLP Summary datapoints carry no exemplars field in the proto — correctly out
of scope.)

## 3. Log federation (Elasticsearch) — largely built

- Interface: `logstore.Store` (logstore.go:219-289) — `Search(Filter)` already
  carries `TraceID`, `TraceIDs []string`, `SpanID`, `From/To`, `SinceNs`.
  Trace→logs and span→logs pivots ride Search today; **no ES types leak**
  above the interface already. The brief's `LogsForTrace/LogsForSpan` land as
  thin convenience methods implemented on top of Search (no new query builders).
- ES client: official go-elasticsearch/v8. Config: env seed → persisted
  `system_settings["logstore_es"]` blob (UI wins) → live `Switchable.Swap`
  (es_config_persist.go — the tempo.Service LoadPersisted template, already
  applied). **The brief's `logs.fields` block already exists**: `ESFieldMap`
  {Timestamp:@timestamp, TraceID:trace.id, SpanID:span.id, Service:
  service.name, SeverityTx:log.level, Body:message} — configurable + persisted
  (elasticsearch.go:217-251). Backend/endpoints/auth/index_pattern likewise
  (ESConfig :181-212, index resolution es_indices.go).
- Query building for trace ids: `traceTermsAny` fans out `term` clauses over
  {configured field, trace.id, trace_id, traceId, TraceId} + body match
  (:2477-2524) — resilient to ECS vs OTel-exporter field shapes.
- Cost guards present per path: capped track_total_hits, soft timeout,
  request_cache off, PIT keep-alive 2m + denial fallback, terminate_after on
  page-1 trace lookups, forward-tail path PIT-less (the v0.8.3-melt kit).
- Timeout semantics: per-path env knobs exist (10s default). The brief's 3s
  per-request timeout + partial-result "backend slow" flag needs a small
  extension: `Search` returns an error today; the trace-view caller must map
  timeout/unreachable → degraded flag instead of failing the tab (Phase 2).
- **⚠ UNVERIFIED — needs your input:** the actual PROD ES index mappings.
  Local minikube ES is scaled to 0; the audit could only verify Coremetry's
  query-side assumptions. Trace→log pivot requires the trace-id field mapped
  `keyword` (a `text` mapping or absence kills the term query silently —
  fan-out clauses just match nothing). **Please provide one sample log
  document (or `GET <index>/_mapping` output) from the real ES** so the audit
  can confirm: trace-id field name + keyword mapping, @timestamp type,
  service/severity field names. The planned "% of logs with trace context"
  exists-aggregation doubles as the runtime detector for this.

## 4. API/MCP pivot surface today

Working pivots: metric→trace (span-derived exemplar via /api/spans/exemplar +
resolver exemplars), trace→logs (/api/logs?traceId= + Trace Logs tab),
log→trace (LogTable trace link + peek drawer), span→service-metrics-window
(/api/spans/metric + drawer redSeries). Partial: span→related-traces is
structural only (/api/traces/relations parent/child) — **no OTel span-link
traversal exists**. The assembled bundle: GET /api/correlate/context
(correlate.go:173) with minute-bucketed FNV cache key (correlate.go:143).

MCP (internal/mcptools): list_services, get_service_health, list_problems,
list_anomalies, search_logs (has trace_id arg), get_trace, query_metric.
**Gaps: no exemplar tool, no correlation-context tool, no linked-traces tool,
no metrics-for-span tool** — the copilot must hand-chain what the UI gets in
one call.

Conventions for new endpoints: NO `registerXxxRoutes()` pattern exists (all
routes in api.go's Start block) and NO route uses `/api/v1/`. See open
questions. Read endpoints are bare (viewer-visible), serveCached with
hash-all-inputs keys + minute-bucketed times, TTL ~30s.

---

## Phased file-level plan

### Phase 1 — ingest completeness (CH only)

**1a. Series fingerprint + exemplars**
- `internal/otlp/fingerprint.go` (new) — `SeriesFingerprint(metricName string,
  dpAttrs []*commonpb.KeyValue, res resourceIdentity) uint64`: xxhash64 over
  `metric_name 0x00 k=v‹0x1F›… 0x00 service.name=… ‹0x1F› service.instance.id=…`
  (dp attrs sorted by key; resource identity = only those two keys, sorted).
  **Hash lib: `cespare/xxhash/v2`** — already in the module graph (CH driver
  dep → zero new dependency), pure-Go, the de-facto Prometheus fingerprint
  hash. zeebo/xxh3 would be a new direct dep for no measurable win at ingest
  rates. In-SQL hashing (cityHash64) rejected: fingerprint must be computable
  in Go at ingest and stable across CH versions.
- `internal/otlp/fingerprint_test.go` (new) — table-driven: attr order
  invariance, 0x1F/0x00 injection resistance, resource-identity isolation, and
  the **consistency test**: one OTLP payload → metric_points row fingerprint ==
  exemplar row fingerprint.
- `internal/otlp/convert.go` — convertMetric: compute fingerprint per data
  point; extract `dp.Exemplars` from Sum/Gauge/Histogram/ExponentialHistogram
  into `ExemplarRow{fingerprint, metric, service, ts, value, traceID hex,
  spanID hex, filteredAttrs map}`; respect `exemplars.require_trace_context`
  (default true) + increment counters either way.
- `internal/otlp/convert_test.go` (new) — real OTLP payload fixtures for all 4
  datapoint types (internal/otlp has zero coverage today; this starts it).
- `internal/chstore/store.go` — migrate(): `metric_points` gains additive
  `series_fingerprint UInt64 DEFAULT 0` (ALTER in `alters` slice, hasXCol
  probe per feedback-distributed-column-safety — this class broke prod twice);
  new `exemplars` table DDL exactly per the brief (Map(LowCardinality(String),
  String) for filtered_attributes; ORDER BY (series_fingerprint, timestamp);
  PARTITION toDate(timestamp); **TTL from `s.ret.SpansDays`** — trace
  retention, not metric). Distributed: add `exemplars` to `highVolumeTables` +
  shard by `cityHash64(toString(series_fingerprint))` in defaultShardPolicy so
  the canonical fingerprint IN (…) query is single-shard-local per series.
- `internal/chstore/retention.go` — SetRetention plans slice: exemplars row
  keyed off `retention.spans` (an exemplar outliving its trace is a dead link).
- `internal/chstore/exemplar_otlp.go` (new) — batched insert + the canonical
  read: `SELECT ts,value,trace_id,span_id FROM exemplars WHERE
  series_fingerprint IN (?) AND timestamp BETWEEN ? AND ? ORDER BY timestamp
  LIMIT 100` (pure PK scan, no JOIN) + metric+service fallback (granule scan;
  minmax skip index on service_name deferred until profiled).
- `internal/config/config.go` — `exemplars.require_trace_context` (default
  true).
- Ingest counters: `exemplars_ingested_total` (per service),
  `exemplars_dropped_no_trace` → Ingester atomics + SystemStats + /admin/stats.

**1b. Span links**
- `internal/otlp/convert.go` — convertSpan: extract `sp.Links`.
- `internal/chstore/store.go` — **storage proposal (decision below)**: new
  `span_links` table:
  `(trace_id String, span_id String, linked_trace_id String, linked_span_id
  String, time DateTime64(9), service_name LC, attr_keys/attr_values arrays)`
  ENGINE MergeTree, PARTITION toDate(time), **ORDER BY (trace_id, time)**,
  bloom idx on linked_trace_id, TTL SpansDays; plus MV
  `span_links_reverse` (same rows) **ORDER BY (linked_trace_id, time)**.
  *Justification from Phase 0 query patterns:* both pivot directions are
  point-lookups by one trace id ("what does this trace link TO" while viewing
  it; "what links TO this trace" for backlinks) — two narrow tables/one MV give
  both as primary-key scans, no full scan, no JOIN. A nested column on `spans`
  was rejected: reverse direction would need a full spans scan or a separate
  index table anyway, and spans is the highest-volume table — link rows are
  ~1-5% of span volume, cheap to duplicate. Distributed: high-volume list +
  shard by cityHash64(trace_id) (reverse MV by linked_trace_id).
- `internal/otlp/convert_test.go` — link extraction fixtures.

**1c. Log backend config block** — already ~exists (ESConfig + ESFieldMap +
`logstore_es` persistence). Delta only: add missing declared fields if any
(severity numeric fallback exists), and the **coverage stat**: on-demand +
periodic ES `exists` aggregation on the configured trace-id field per service
→ new `SystemSnapshot` field ("% of logs with trace context") + /admin/stats
row + ElasticTab hint. Files: `internal/logstore/elasticsearch.go` (one agg
builder w/ track_total_hits:false + timeout), `internal/api/` stats handler,
`frontend/src/pages/AdminStats.tsx`.

### Phase 2 — pivot query layer
- `internal/api/pivot.go` (new) + `registerPivotRoutes(mux *http.ServeMux)`
  called from Start — introduces the register-pattern the brief asks for
  without touching existing routes (api.go gains 1 line).
  - GET exemplars endpoint (`?fingerprints=&from=&to=` + metric/service
    fallback) → {ts, value, trace_id, span_id, attrs}; serveCached 30s,
    hash-all-inputs key (fingerprint set via sorted FNV digest per v0.5.187).
  - GET linked traces (both directions; span_links + reverse MV).
  - GET service metrics around a span window (±15m default, configurable) —
    thin composition over existing QuerySpanMetric/metricquery.
  - <500ms: all reads are primary-key scans by construction (fingerprint IN,
    trace_id =), plus LIMITs + max_execution_time per house rule.
- `internal/logstore/logstore.go` — add `LogsForTrace(ctx, traceID, window)` /
  `LogsForSpan(ctx, traceID, spanID, window)` with default impls delegating to
  Search (keeps ES/CH parity for free); per-request 3s timeout wrapper +
  typed `ErrBackendSlow` so callers degrade to partial results with the
  "log backend slow/unreachable" flag (trace view must never block).
- `internal/api/api_logs.go` — degraded-flag plumbing on the trace-logs path.
- GET trace-summary-for-trace-id already exists (/api/traces/{id}); the
  log-UI entry point reuses it.

### Phase 3 — UI pivots (mostly wiring, shell exists)
- `frontend/src/lib/types.ts` + `lib/api.ts` — exemplar/linked-traces types +
  clients.
- Exemplar dots: TimeSeriesPanel already renders + click-pivots ◆
  (TimeSeriesPanel.tsx:421-641) — feed it REAL OTLP exemplars from the new
  endpoint for catalogue-metric charts (today only span-derived queries get
  them); Explore/MetricsExplorer wiring.
- Trace.tsx — "Linked traces" section (new, from span_links both directions);
  Logs tab gains the degraded-warning chip (never blocks); "Service metrics"
  side panel = existing drawer MetricsLens scoped to span window (extend
  anchor).
- LogTable trace chip → already links + peeks; add span-scroll param to
  /trace?id=…&span=… (Trace view scroll-to-span exists? verify at impl).
- Breadcrumb/time-range preservation: URL-first per house rules (range param
  already carries).
- Dead CSS `.trace-logs-sev` cleaned up opportunistically.

### Phase 4 — MCP parity
- `internal/mcptools/pivots.go` (new) — get_logs_for_trace,
  get_exemplar_traces, get_linked_traces, get_metrics_for_span; Deps closure +
  range_s + clamp conventions (template: listServicesTool); log tools through
  LogStore with the same 3s/degraded semantics.

### Testing (per brief constraints)
- fingerprint consistency + injection tests; convert_test fixtures for all
  datapoint types + links (first coverage in internal/otlp).
- LogStore unit tests against a mocked ES transport: ECS vs OTel field-name
  config variations, timeout→ErrBackendSlow.
- Migration safety: all CH changes additive (new tables + one ALTER), hasXCol
  probes, ON CLUSTER handled by adaptDDL automatically; explicit steps listed
  in each migration comment.

---

## Open questions (blocking, please answer with the review)

1. **Sample ES log document / index mapping** — needed to confirm the trace-id
   field is `keyword`-mapped in prod (see §3 ⚠). Without it the trace→log
   pivot may silently return nothing; the fix would be an ES index-template
   change outside Coremetry.
2. **`/api/v1/` prefix**: the brief says `/api/v1/exemplars`, but zero existing
   routes use a version prefix (everything is `/api/...`). Recommendation:
   stay consistent with `/api/exemplars` unless you want to start versioning
   here deliberately.
3. **Fingerprint scope**: brief fixes resource identity to service.name +
   service.instance.id only — confirmed OK? (Multi-instance series will pivot
   per-instance; per-service rollup queries then use the fallback path.)
4. Span-link table proposal above (table + reverse MV) — approve or prefer the
   nested-column variant despite the reverse-scan cost?

**Stopping here per the deliverable order — no implementation until your
review.**
