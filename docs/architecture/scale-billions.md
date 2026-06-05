# Scaling Coremetry to Billions of Spans + Metrics / Day

**Status:** design — target architecture + migration plan
**Governing principle:** _the UI must NEVER scan raw `spans` / `logs` / `metric_points` on a user request. It reads pre-computed, bounded, downsampled results. Raw access is an explicit, time-bounded, on-demand drill-down — never a page load or a poll._

This is an architecture review against that principle, grounded in the **current** codebase (not a greenfield design). Coremetry is already ~70 % of the way there: raw-table DDL, 12 materialized views, tuned `async_insert`, OTLP type/temporality handling, and a singleflight + SWR cache shield all exist. This document is honest about what's built, isolates the real gaps, and gives DDL + a phased, UX-preserving migration.

Provenance of each ClickHouse recommendation is labelled `official` / `derived` / `field` per the architecture-advisor convention.

---

## 0. Workload summary

| | |
|---|---|
| **Workload** | observability / APM (OTel-native: spans, metrics, logs) |
| **Latency target** | `/api/*` p99 < 200 ms warm / < 1 s cold; hot reads (`/services`, `/problems`, `/health`) < 50 ms warm; heatmap < 3 s ≤6 h |
| **Data shape** | append-only, time-ordered, billions of spans/day, 1000s services, 10 000s operations; metrics + logs at comparable volume |
| **Primary query patterns** | service-scoped RED over a window; topology edges; trace list + single-trace waterfall; metric time-series; log search + histogram; correlation by `trace_id` |
| **Operational constraints** | **single binary** (`COREMETRY_MODE=all\|ingest\|api\|worker`); CH + Redis mandatory, ES/Tempo optional; **single-tenant** (no multi-tenant features — memory `feedback-no-multitenant`); uPlot-only charts; SSE bus for push (v0.6.3); "one container, not a kubernetes opera" |

---

## 1. Target architecture

```
┌──────────────┐   per-node       ┌──────────────────┐   trace-aware LB     ┌─────────────────────┐
│  App + SDK   │   OTLP/gRPC      │  AGENT collector │   loadbalancing-     │  GATEWAY collector  │
│  (EDOT/raw)  ├─────────────────▶│  memory_limiter  ├──exporter (traceID)─▶│  TAIL SAMPLING      │
└──────────────┘                  │  resource, batch │                      │  keep ALL errors+   │
                                  └──────────────────┘                      │  slow + ~5% rest    │
                                                                            └──────────┬──────────┘
                                                                                       │ OTLP
                                        ┌──────────────────────────────────────────────┼───────────────┐
                                        │                                              │               │ 100%
                                        ▼ (opt-in spike buffer)                        ▼               ▼
                                 ┌──────────────┐                          ┌───────────────────┐  ┌─────────┐
                                 │  Redpanda    │  COREMETRY_INGEST_       │ Coremetry INGEST  │  │  Tempo  │
                                 │  (Kafka API, ├─SOURCE=kafka────────────▶│ OTLP recv +       │  │ (100%,  │
                                 │  no ZK)      │                          │ async_insert      │  │ fallback)│
                                 └──────────────┘                          └─────────┬─────────┘  └─────────┘
                                                                                     │ async_insert (10MB/1s)
                                                  ┌──────────────────────────────────┼─────────────────────┐
                                                  ▼                                  ▼                      ▼
                                          ┌───────────────┐  insert-trigger  ┌────────────────┐   ┌──────────────┐
                                          │ RAW MergeTree │─────MVs─────────▶│ ROLLUPS        │   │ Elasticsearch│
                                          │ spans / logs /│                  │ AggregatingMT  │   │ (logs, opt., │
                                          │ metric_points │◀── drill-down ───│ 1m/5m + dims + │   │  ILM tiers)  │
                                          │ (bounded)     │    only          │ exemplars      │   └──────┬───────┘
                                          └───────────────┘                  └───────┬────────┘          │
                                                                                     │                   │
                                          ┌──────────────────────────────────────────┴───────────────────┴──────┐
                                          │  QUERY SERVICE  (internal/api — the shield, COREMETRY_MODE=api)       │
                                          │  • bounds enforcer (reject unbounded / over-window)                   │
                                          │  • granularity router  (window → 1m / 5m-state-merge / drill-down)    │
                                          │  • bounded output (≤2k uniform buckets; top-N + "other")             │
                                          │  • L0 singleflight + L1/L2 Redis SWR cache  (EXISTS)                  │
                                          │  • per-user concurrency caps + rate limit + async job/poll           │
                                          │  • SSE / NDJSON streaming for big result sets                        │
                                          └──────────────────────────────────────────┬──────────────────────────┘
                                                                                     │ bounded JSON / SSE
                                                                                     ▼
                                          ┌──────────────────────────────────────────────────────────────────────┐
                                          │  FRONTEND  (React + uPlot, react-query)                               │
                                          │  charts read rollup granularity matched to range; tables page;        │
                                          │  poll ≥10s + pause on document.hidden; raw = explicit drill-down      │
                                          └──────────────────────────────────────────────────────────────────────┘
```

The flow the principle demands: **ingest → (buffer) → store raw → roll up at write time → read rollups behind the shield → render bounded.** Raw is reachable only through the shield's drill-down lane, always time-bounded + `LIMIT` + `max_execution_time`.

---

## 2. Current state vs. target — what's built, what's the gap

| Layer | Already in place | Gap to close |
|---|---|---|
| **Ingest tiers** | agent collector: `memory_limiter`, `resource`, `batch` (`otel-collector-config*.yaml`) | no **gateway tier**, no `loadbalancingexporter` |
| **Sampling** | head `probabilistic_sampler/coremetry`; 100 %→Tempo | **no tail sampling** → Coremetry's store loses errors/slow it would keep |
| **Stream buffer** | `async_insert` 10 MB / 1 s coalesce (`repo.go:asyncInsertCtx`) | no disk-backed buffer for multi-minute spikes / replay / multi-sink |
| **Raw storage** | MergeTree, `PARTITION BY toDate(time)`, `ORDER BY (service_name, …, time)`, TTL, LowCardinality, ZSTD/Delta/Gorilla/T64, skip indexes | add **projections** for the 2–3 hot non-prefix filters; verify TTL→cheap-disk move |
| **Rollups** | 12 MVs @ **5 m** (service/operation/db/messaging/trace summaries, spanmetrics_*) | only 5 m → **no sub-5 m**, **no dimensional** (cluster/env/host), **no generic OTLP-metric** rollup, **no exemplar** in rollup |
| **OTLP metrics** | type (`instrument`) + temporality (`delta`/`cumulative`) + histogram `bucket_bounds`/`bucket_counts` | generic metrics read **raw** `metric_points` (only spanmetrics rolled up) |
| **Exemplars** | on-demand `FindExemplar` raw-spans `LIMIT 1` (fast, sparse) | not embedded in rollup → still one raw touch per chart-jump |
| **Logs (ES)** | PIT + `search_after`, `min_doc_count:1`, `request_cache`, `timeout`, `track_total_hits:false` (v0.8.3); significant_text/MLT removed (v0.8.8); live-tail @ 10 s | **no default time-bound** on `Search()`; `CountPatterns`/`EQLSearch` **no timeout**; no ILM/rollup/force_merge plan in-repo; CH-vs-ES split undecided |
| **Query shield** | L0 singleflight + L1/L2 Redis SWR (`cache.go`); `max_execution_time`; `LIMIT`; auto-step ~120–180 buckets (`metricquery.go`) | no **central bounds-enforcer/rejector**, no **per-user concurrency cap / rate limit**, no **async job+poll**, no **granularity router** abstraction |
| **Frontend** | poll ≥10 s + `document.hidden` pause; `useDataTable` virtualize; `refetchOnWindowFocus:false`+`staleTime` (Logs) | apply react-query hardening uniformly; ensure every chart reads rollup, not raw |

**Bottleneck today:** the raw-spans/metric_points fallbacks the migration must remove (§6), plus the missing tail sampling that makes the Coremetry store both *larger than it needs to be* and *missing the errors/slow it most needs*.

---

## 3. Ingestion — OTel-native pipeline

### 3.1 Agent → gateway tiers (close the gateway gap)

Keep agents per-node (already configured). Add a **gateway tier** because tail sampling requires every span of a trace to land on the same collector instance:

```yaml
# AGENT (per node) — receive, protect, batch, route by trace
exporters:
  loadbalancing:
    routing_key: traceID            # all spans of a trace → same gateway
    protocol: { otlp: { tls: { insecure: true } } }
    resolver: { dns: { hostname: coremetry-gateway, port: 4317 } }
processors:
  memory_limiter: { check_interval: 1s, limit_percentage: 80, spike_limit_percentage: 25 }
  batch:          { timeout: 5s, send_batch_size: 8192, send_batch_max_size: 16384 }
service:
  pipelines:
    traces: { receivers: [otlp], processors: [memory_limiter, resource, batch], exporters: [loadbalancing] }
```

```yaml
# GATEWAY (replicated, scale-out) — tail-sample, fan out to Coremetry + Tempo
processors:
  tail_sampling:
    decision_wait: 10s
    num_traces: 200000
    policies:
      - { name: errors,  type: status_code, status_code: { status_codes: [ERROR] } }      # keep ALL errors
      - { name: slow,    type: latency,     latency: { threshold_ms: 1000 } }              # keep ALL slow
      - { name: rest,    type: probabilistic, probabilistic: { sampling_percentage: 5 } }  # low ratio of the rest
      - { name: cap,     type: rate_limiting, rate_limiting: { spans_per_second: 50000 } } # safety valve
service:
  pipelines:
    traces/coremetry: { receivers: [otlp], processors: [memory_limiter, resource, tail_sampling, batch], exporters: [otlp/coremetry] }
    traces/tempo:     { receivers: [otlp], processors: [memory_limiter, resource, batch],                 exporters: [otlp/tempo] }   # 100% fallback (decision log v0.5.208/220)
```

**Why tail over the current head `probabilistic_sampler`:** head sampling is blind — it drops errors and slow traces at the same rate as everything else, so Coremetry's own store is missing exactly the traces an operator opens it to find. Tail keeps 100 % of errors + slow + a low ratio of the rest. Tempo still holds 100 % for the long-tail trace-by-id fallback. This is a **policy upgrade**, not new storage. (`field` — collector config; OTel-spec-aligned.)

### 3.2 Streaming buffer (Kafka/Redpanda) — opt-in, not default

Today `async_insert` (`wait=1`, 10 MB / 1 s coalesce) **is** the write buffer and it's tuned (v0.5.346 — do not retune). It absorbs sub-second bursts and surfaces insert errors synchronously. For most installs that is sufficient, and adding Kafka would betray the "one container" ethos.

**Decision:** keep `async_insert` as the default for `monolithic` and `distributed` modes. Offer an **opt-in Redpanda tier** (`COREMETRY_INGEST_SOURCE=otlp|kafka`) for billion+/day installs that need:

- **multi-minute spike absorption** beyond what CH can ingest live (disk-backed log decouples producer from CH),
- **zero-loss on CH outage** (collector→Redpanda→consumer retries without backpressuring the SDK),
- **independent consumer scaling / replay / reprocessing** (re-derive a rollup without re-ingesting),
- **multi-sink fan-out** (CH + a second consumer).

Redpanda (Kafka API, single binary, **no ZooKeeper**) joins the *optional-dependency* tier alongside ES/Tempo — consistent with "CH + Redis + optional ES/Tempo," not a new control plane. In `kafka` mode the OTLP receiver produces to a topic and `COREMETRY_MODE=ingest` workers consume → `async_insert` → CH. Backpressure is the consumer lag, observable on `/admin/stats`. (`field` — workload-dependent; default off.)

> **Recommendation:** ship the gateway + tail sampling first (high value, no new dependency). Treat Redpanda as a documented, off-by-default scale escape hatch — turn it on only when `/admin/stats` shows `async_insert` queueing or CH write saturation under sustained load.

---

## 4. Storage / modeling — ClickHouse

### 4.1 Raw tables — already correct, two additions

`spans` / `logs` / `metric_points` already satisfy the brief: `MergeTree`, `PARTITION BY toDate(time)`, `ORDER BY (service_name, …, time)`, `TTL toDate(time) + INTERVAL N DAY`, aggressive `LowCardinality`, ZSTD/Delta/Gorilla/T64 codecs, and skip indexes (`bloom_filter` on `trace_id`, `set` on `name`, `tokenbf_v1` on `body`). **Do not re-derive these.** Two additions:

1. **Projections for the 2–3 hot non-prefix filters.** Service-prefix pruning is already optimal; a projection covers the recurring "by `http_route`" / "by `status_code`" scans without a second ORDER BY. (`official` — projections; applies to new parts only, old data unindexed until merge.)
   ```sql
   ALTER TABLE spans ADD PROJECTION p_route_time
     (SELECT * ORDER BY (http_route, time));     -- materialize on new parts
   ```
2. **Tiered TTL to cheap storage** (verify in prod): move warm partitions to a cheaper volume before drop, instead of drop-only.
   ```sql
   TTL toDate(time) + INTERVAL 2  DAY  TO VOLUME 'cold',
       toDate(time) + INTERVAL 30 DAY  DELETE;
   ```
   The proactive `StartRetentionEnforcer` (`DROP PARTITION` hourly, v0.5.320) stays for immediate disk reclaim. (`official` — storage policies / TTL moves.)

> **Add a `cluster` column at ingest.** `ListClusters` derives cluster from attrs; the `/services?cluster=` path can't use the MV because the dimension isn't materialized → it falls back to raw spans. Promote `cluster LowCardinality(String)` (from `k8s.cluster.name` / a configured resource attr) to a first-class column so it can enter a dimensional rollup (§4.3). (`derived`.)

### 4.2 Rollups — the key insight: **states are mergeable, so coarser is free**

The brief asks for 1 m / 5 m / 1 h rollups. **You do not need three tables.** `AggregateFunction` states (`quantilesState`, `countState`, `sumState`) are *composable*: merging twelve 5 m states across an hour yields the exact 1 h aggregate. So:

- **Coarse (≥5 m, incl. 1 h / 7 d / 30 d): read the existing 5 m MVs and `…Merge()` across the wider window at read time. Already possible today — just enforce it on every read path.** No 1 h table.
- **Fine (<5 m, single service/op): a bounded raw-spans query is acceptable** — partition + service-prefix pruned, `LIMIT`, `max_execution_time`. This is the principle's sanctioned drill-down lane.
- **Fine (<5 m, global / all-services): add a 1 m tier** with short retention for the global short-window RED view only.

```sql
-- NEW: 1-minute global RED, 7-day TTL. Serves the 30m/1h "all services" view
-- without a raw scan; coarser windows still come from the 5m MV via read-time merge.
CREATE MATERIALIZED VIEW service_summary_1m
ENGINE = AggregatingMergeTree
PARTITION BY toDate(time_bucket)
ORDER BY (service_name, time_bucket)
TTL toDate(time_bucket) + INTERVAL 7 DAY
AS SELECT
  service_name,
  toStartOfInterval(time, INTERVAL 1 MINUTE)            AS time_bucket,
  countState()                                          AS span_count_state,
  countIfState(status_code = 'error')                   AS error_count_state,
  sumState(duration)                                    AS duration_sum_state,
  quantilesTDigestState(0.5, 0.95, 0.99)(duration)      AS duration_q_state
FROM spans
GROUP BY service_name, time_bucket;
```

> Note: existing MVs use `quantilesState` (exact t-digest variant under the hood for the merge path). For any **new** rollup prefer `quantilesTDigestState` explicitly — ≤2 % error, bounded memory at billions of rows (`official` — quantile functions; anti-pattern: bare `quantile()` past ~1 M rows).

**Read-time coarsening (the pattern every chart uses):**
```sql
-- 7-day view from 5m states, bucketed to 1h — no raw spans, no 1h table
SELECT toStartOfInterval(time_bucket, INTERVAL 1 HOUR)                   AS t,
       countMerge(span_count_state)                                     AS spans,
       countMerge(error_count_state)                                    AS errs,
       arrayElement(quantilesMerge(0.95)(duration_q_state), 1) / 1e6    AS p95_ms
FROM service_summary_5m
WHERE service_name = ? AND time_bucket >= ? AND time_bucket < ?
GROUP BY t ORDER BY t
SETTINGS max_execution_time = 10;
```
(`derived`, high confidence — AggregateFunction state mergeability is core AggregatingMergeTree behavior.)

### 4.3 Dimensional rollups — kill the raw-spans fallbacks

The raw-scan offenders on `/services?cluster=` and the service-detail panels exist because the 5 m summaries have no cluster/env/host dimension. Add **one** dimensional RED rollup rather than widening the hot `service_summary_5m` (widening its ORDER BY is a destructive drop+recreate per the migration checklist):

```sql
CREATE MATERIALIZED VIEW service_dim_summary_5m
ENGINE = AggregatingMergeTree
PARTITION BY toDate(time_bucket)
ORDER BY (service_name, cluster, deploy_env, host_name, time_bucket)   -- filter-first ordering
TTL toDate(time_bucket) + INTERVAL 30 DAY
AS SELECT
  service_name, cluster, deploy_env, host_name,
  toStartOfInterval(time, INTERVAL 5 MINUTE)            AS time_bucket,
  countState()                                          AS span_count_state,
  countIfState(status_code = 'error')                   AS error_count_state,
  sumState(duration)                                    AS duration_sum_state,
  quantilesTDigestState(0.5,0.95,0.99)(duration)        AS duration_q_state
FROM spans
GROUP BY service_name, cluster, deploy_env, host_name, time_bucket;
```
`/services?cluster=X` and the service-detail cluster-breakdown / instances panels now read this MV with a prefix-pruned WHERE — no raw spans. Cardinality is governed: cluster/env/host are all LowCardinality with bounded distinct counts. (`derived`.)

### 4.4 Topology / service-to-service edges — already pre-aggregated, finish the swap

`topology_edges_5m`, `service_callers_5m`, `topology_op_edges_5m`, `topology_root_flows_5m` exist (ReplacingMergeTree, hand-computed by the worker). The remaining raw scans are `/topology` operation-BFS (`internal/api/topology.go`) and `/service-map` (`service_map.go`) which **sample raw traces at request time**. Migrate both to read the edge tables (they already hold the parent→child structure); fall back to a bounded sampled raw query only for an explicit "rebuild graph now" action. (`derived` — matches the v0.5.108 "JOIN spans = bug, move to 5 m MV" lesson.)

### 4.5 OTLP metrics — respect type/temporality (mostly done) + generic rollup (the gap)

Type (`gauge`/`sum`/`histogram`/`exp_histogram`/`summary`) and temporality (`delta`/`cumulative`) are already stored, and histograms carry `bucket_bounds`/`bucket_counts`. Two pieces remain:

1. **Delta-convert cumulative at the rollup, not at every read.** Cumulative counters must be `runningDifference`-d per series before bucketing; doing it at read time over raw points is the cost. Roll it into the generic rollup below.
2. **Generic metric rollup** — only `spanmetrics_*` are materialized; `/metrics`, `/explore`, infra/instances panels read **raw** `metric_points`. Add a bounded-cardinality rollup:

```sql
-- Generic per-(service, metric) rollup. NOT keyed by full attribute set (that explodes
-- cardinality); full-label queries stay an explicit bounded drill-down on raw metric_points.
CREATE MATERIALIZED VIEW metric_rollup_1m
ENGINE = AggregatingMergeTree
PARTITION BY toDate(time_bucket)
ORDER BY (service_name, metric, time_bucket)
TTL toDate(time_bucket) + INTERVAL 14 DAY
AS SELECT
  service_name, metric,
  toStartOfInterval(time, INTERVAL 1 MINUTE)   AS time_bucket,
  anyLast(instrument)                          AS instrument,
  anyLast(temporality)                         AS temporality,
  countState()                                 AS point_count_state,
  sumState(value)                              AS sum_state,    -- gauges/sums
  avgState(value)                              AS avg_state,
  minState(value)                              AS min_state,
  maxState(value)                              AS max_state,
  quantilesTDigestState(0.5,0.95,0.99)(value)  AS q_state
FROM metric_points
WHERE length(bucket_counts) = 0                 -- scalar metrics; histograms keep their own path
GROUP BY service_name, metric, time_bucket;
```

The metric query engine routes to `metric_rollup_1m` when the query groups only by service/metric over a window; queries that filter/group by **arbitrary labels** drop to a bounded raw `metric_points` drill-down (the `metricquery.go` auto-step already caps output at ~120–180 buckets — keep that, it's the right shape, uniform buckets beat LTTB for metrics). Histograms continue through `spanmetrics_hist_5m` / `spanmetrics_duration_5m`. Cardinality governance: rollup dimension set is **service + metric only** by default; promoting a label to a rollup dimension is an explicit, cardinality-reviewed config change (the `LowCardinality` counter-cardinality rule). (`derived`.)

### 4.6 Exemplars — embed a sampled `trace_id` per bucket

`FindExemplar` is fast (sparse, `ORDER BY duration DESC LIMIT 1`) but it's still a raw-spans touch per chart-jump. Embed the exemplar in the summary states so the chart jumps with zero raw access:

```sql
-- add to service_summary_* / operation_summary_* via drop+recreate (migration checklist):
argMaxState(trace_id, duration)                                   AS slow_exemplar_state,   -- slowest trace in bucket
argMaxIfState(trace_id, duration, status_code = 'error')          AS error_exemplar_state;  -- worst error trace
```
Read with `argMaxMerge`. Keep `FindExemplar` as the fallback for buckets older than rollup retention or for ad-hoc filters. (`official` — `argMax` state; the brief's "sampled trace_id per metric bucket.")

---

## 5. Logs — Elasticsearch (stop hammering it)

ES is the primary logstore at 10 B+ logs/day (memory `project-elastic-scale`). The hot paths are already hardened (v0.8.3 `Histogram` bounding; v0.8.8 removal of `significant_text` + `more_like_this`). Remaining work is operational (ILM/rollup) + three guard gaps + the CH-vs-ES split.

### 5.1 ILM (hot → warm → cold → frozen) + rollover

Operator-managed (external ES), shipped as a documented policy + surfaced on `/admin/elastic` (already reads `_ilm/explain`):

| Phase | Age | Actions |
|---|---|---|
| **hot** | 0–2 d | primary writes; `rollover` at `max_age 1d` / `max_primary_shard_size 50gb` / `max_docs`; index.refresh_interval 30s |
| **warm** | 2–7 d | `forcemerge max_num_segments:1`; `shrink`; `readonly`; `allocate` to warm nodes; replicas→1 |
| **cold** | 7–30 d | `searchable_snapshot` (partial); `allocate` to cold nodes |
| **frozen** | 30–90 d | `searchable_snapshot` (full, object store); query via `async_search` only |
| **delete** | >90 d | `delete` |

`force_merge` read-only warm indices to one segment makes search cheaper and smaller. (`official` — ES ILM.)

### 5.2 Historical rollups

For windows beyond `hot+warm`, the log **histogram** panel should read a pre-aggregated rollup index (`logs_rollup_1h`: `date_histogram` × `service` × `severity`) instead of running `date_histogram` over cold/frozen raw docs. Build via the ES rollup/transform job (operator-scheduled); the histogram endpoint routes to the rollup index when `from` is older than the hot+warm boundary. (`official` — ES rollups/transforms.)

### 5.3 Three remaining query-cost guards

| Guard | Where | Fix |
|---|---|---|
| **Default time-bound** | `Search()` (`elasticsearch.go:738`) — no min window when `from/to` absent | inject `from := now-24h` when unbounded (CH sibling already does); reject "all time" without explicit ack |
| **`CountPatterns` timeout** | `elasticsearch.go:1182` — no per-pattern `timeout` (CH path has `max_execution_time:5s`) | add `timeout` to each `_msearch` sub-body; mirror CH's 5 s |
| **`EQLSearch` timeout / async** | `elasticsearch.go:321` — relies only on the 30 s handler deadline | add ES-side `timeout`; route to `async_search` for long windows |

Plus, broadly: time-bounded queries only, required filters, **PIT + `search_after`** (exists — never deep `from/size`), `async_search` for cold/frozen, **doc-value-only** fields + `field_caps`, `request_cache` on agg-only requests (exists on `Histogram`). Correlation by `trace_id` stays an **indexed term lookup**, never an aggregation.

### 5.4 CH-vs-ES split — recommendation

The dual-write already exists (collector logs pipeline → `coremetry` + `elasticsearch`) and `logstore.Store` abstracts both behind `COREMETRY_LOGS_BACKEND`.

> **Recommend a tiered split, operator-configurable:**
> - **High-volume *structured* app logs with known fields → ClickHouse** `logs` table. It already has the `tokenbf_v1` body skip-index and the cheap `Histogram` MV pattern; structured filter + histogram + `trace_id` correlation are all cheaper and cost-bounded in CH than in ES, and it removes that volume from the ES bill entirely.
> - **Full-text / relevance / fuzzy search and the long tail → Elasticsearch.** Keep ES for what it's uniquely good at; let ILM age it to frozen.
>
> This makes CH the warm store for the structured majority and ES the searchable archive for the full-text minority — directly shrinking the "ad-hoc heavy agg per page view" surface. It's a routing-policy change, not new code: the backends and the dual-write are already there.

---

## 6. Query / API layer — the shield (`internal/api`, `COREMETRY_MODE=api`)

Stays inside the single binary — the "query service" is the API role, hardened, **not** a new microservice. What exists vs. what to add:

**Exists:** `s.serveCached` with **L0 singleflight + L1/L2 Redis SWR** (`cache.go`); `max_execution_time` + `LIMIT` on raw queries; auto-step bucketing to ~120–180 points (`metricquery.go`); cache keys hash all inputs (sorted + FNV, v0.5.187).

**Add — the query-service contract:**

```
Every read endpoint MUST:
  1. BOUND        require a time range; reject missing/over-max-window (or demand explicit ack).
  2. ROUTE        window → tier:  <5m global → 1m MV | ≥5m → 5m MV (read-time merge for coarser)
                  | single-service <5m or arbitrary-label → bounded raw drill-down.
  3. CAP OUTPUT   ≤ ~2k uniform buckets (toStartOfInterval step); group-bys → top-N + "other".
  4. CACHE        s.serveCached (L0 singleflight + L1/L2 SWR) keyed by normalized query+range. [EXISTS]
  5. LIMIT USER   per-user/role/query-class concurrency semaphore + token-bucket rate limit.   [NEW]
  6. ASYNC        windows/scans beyond a threshold → submit job, return id, poll; result→Redis. [NEW]
  7. STREAM       big result sets → NDJSON / SSE (reuse the v0.6.3 SSE bus).                    [NEW]
  8. REJECT       a central bounded(q) validator refuses unbounded / un-routable queries.       [NEW]
```

Notes:
- **Single-tenant** (memory `feedback-no-multitenant`): "per-tenant quotas" is reframed as **per-user / per-session / per-query-class** caps — the goal is "one operator can't melt the cluster," not tenancy isolation. A `query-class` (cheap-rollup vs. raw-drill-down vs. async-export) gets its own concurrency budget.
- **Granularity router** is a thin helper (`tierForWindow(range) → (table, step)`) every chart calls — it's the code embodiment of §4.2.
- **Reject, don't silently truncate.** An over-window or un-routable query returns 400 with the bound it violated, not a partial result that reads as complete.

---

## 7. Frontend — consume cheap, never raw

Mostly hardening of patterns already present:

- **Charts request the rollup granularity matching the range** (1 m for 30 m, 5 m for a day, 1 h-merged for 7 d) via the granularity router — never raw points. The metric path already auto-steps; extend the same discipline to every RED/topology chart so none reads raw.
- **Tables request a page**, not the set — `useDataTable` + server pagination + `content-visibility` already exist; keep them on every >100-row table.
- **react-query uniformly:** long `staleTime`, `keepPreviousData`, `refetchOnWindowFocus:false`, **pause polling on `document.hidden`** (CLAUDE.md hard constraint), `refetchInterval ≥ 10s` (5 s only for `/health`). Logs already models this — make it the default everywhere.
- **Debounce + `AbortController`** on filter/range edits so a fast typer doesn't fan out N in-flight CH queries.
- **Drill-down is explicit and time-bounded:** opening a full trace / raw span list / arbitrary-label metric is a deliberate action that hits the raw drill-down lane — never the default page load. `timeRangeToNs(range)` stays inside `useEffect`/`useMemo` (v0.5.184).

---

## 8. Phased migration — move each screen off raw scans, UX unchanged

Each phase is an independent `v0.8.X`/`v0.9.X` release (aggressive cadence, memory `feedback-release-cadence-aggressive`); UX is byte-for-byte identical — only the data source under each panel changes. Order = highest raw-scan-cost first.

| Phase | Release | Screen / endpoint (raw today) | Move to | New artifact |
|---|---|---|---|---|
| **P0** | v0.8.x | Gateway tier + **tail sampling** | collector config | `otel-collector-config*.yaml` gateway pipeline |
| **P1** | v0.8.x | `/services?cluster=` → `GetServicesFilteredIn` (raw spans) | `service_dim_summary_5m` | §4.3 MV + `cluster` column |
| **P2** | v0.8.x | `/service` detail: cluster-breakdown, instances, blast-radius (raw spans/metric_points) | `service_dim_summary_5m` + `metric_rollup_1m` | §4.3 / §4.5 |
| **P3** | v0.8.x | `/metrics`, `/explore` metrics, infra panels → `GetMetricPoints` (raw metric_points) | `metric_rollup_1m`; raw only for arbitrary-label drill-down | §4.5 MV + router |
| **P4** | v0.8.x | `/topology` op-BFS + `/service-map` (raw trace sampling) | `topology_op_edges_5m` / `topology_edges_5m` | §4.4 read-path swap |
| **P5** | v0.8.x | `/traces` <5 m / filtered → raw spans for RED context | `service_summary_1m`; raw only for trace-list + waterfall (sanctioned drill-down) | §4.2 1 m MV |
| **P6** | v0.8.x | Chart "jump to exemplar" → `FindExemplar` raw `LIMIT 1` | `*_exemplar_state` in summary MVs | §4.6 |
| **P7** | v0.8.x | ES: default time-bound + `CountPatterns`/`EQL` timeout; ILM policy + `logs_rollup_1h` | guards + rollup index | §5 |
| **P8** | v0.9.x | Query shield: bounds-enforcer, per-user caps, async job/poll, granularity router helper | `internal/api` | §6 |
| **P9** | v0.9.x | Opt-in Redpanda ingest tier (`COREMETRY_INGEST_SOURCE=kafka`) | ingest mode | §3.2 |

Each phase ships with: the MV migration (drop+recreate where state columns change — migration checklist), a `make audit`-clean diff, and a regression test (CLAUDE.md gate). No backwards-compat shim — flip the read path, delete the raw query.

---

## 9. How we measure success

**Per-screen ClickHouse cost (the headline metric).** Before/after each phase, from `system.query_log`:

```sql
SELECT normalized_query_hash,
       count()                                  AS calls,
       sum(read_rows)                           AS rows_read,
       round(avg(query_duration_ms))            AS avg_ms,
       round(quantile(0.95)(query_duration_ms)) AS p95_ms,
       formatReadableSize(sum(read_bytes))      AS bytes_read,
       round(sum(ProfileEvents['OSCPUVirtualTimeMicroseconds'])/1e6, 1) AS cpu_s
FROM system.query_log
WHERE type = 'QueryFinish'
  AND event_time >= now() - INTERVAL 1 HOUR
  AND query LIKE concat('%', {screen_marker:String}, '%')   -- tag each endpoint's SQL with a /* screen:X */ comment
GROUP BY normalized_query_hash
ORDER BY rows_read DESC;
```

**Success target:** `read_rows` per screen drops from *O(spans in window)* to *O(rollup rows in window)* — typically **2–4 orders of magnitude** (millions/billions → thousands). Tag each endpoint's SQL with a `/* screen:<name> */` comment so the join is trivial.

**ES query cost:** `_nodes/stats/indices/search` (`query_total`, `query_time_in_millis`, `scroll_*`), `search.fetch_time`, and the `_tasks` API for long `async_search`; before/after the ILM + default-bound + rollup changes. Target: zero unbounded `date_histogram` over frozen indices; p95 log-search latency flat as the index grows.

**API latency:** existing `/admin/stats` per-endpoint p99 + Redis hit rate; assert the budgets in §0 hold (hot reads < 50 ms warm) and the cache hit-rate rises as rollups make upstream cheaper.

**Cost / retention tradeoffs to track:**

| Knob | Cost ↓ | Fidelity / latency tradeoff |
|---|---|---|
| Tail sampling 5 % rest | CH span volume ↓ ~10–20× | full fidelity for errors/slow; sampled rest (Tempo has 100 %) |
| 5 m base + read-time merge | no extra MV storage | coarse views pay a small read-merge; sub-5 m global needs the 1 m MV (7 d TTL) |
| `metric_rollup_1m` (service+metric only) | `/metrics` `read_rows` ↓ orders of magnitude | arbitrary-label queries fall to bounded raw drill-down |
| ES → CH for structured logs | ES bill ↓; CH cost bounded by `tokenbf_v1` | lose ES relevance scoring on that slice (kept for full-text) |
| ILM hot→frozen + force_merge | storage $/GB ↓; warm search faster | frozen search is `async_search`-only (seconds, not ms) |
| Tiered TTL (TO VOLUME) | hot disk ↓ | cold-partition reads slower |

**Definition of done:** every screen in §8 reads a rollup or a bounded drill-down (verified by the `system.query_log` `read_rows` drop), `make audit` CHECK 6 (raw-spans-without-bounds) is clean, and no normal page load or poll touches raw `spans` / `metric_points` / unbounded ES aggs.
