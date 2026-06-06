# Coremetry Performance Engagement — Phase 1 Baseline & Bottleneck Ranking

**Date:** 2026-06-06 · **Build:** distributed v0.8.28 on local minikube (44 services, go-demo + java-demo traffic, ES logstore, CH+Redis) · **Scope:** speed + runtime perf (mission focus 1 & 2). Error-detection (3) and anomaly-detection (4) accuracy get their own baseline at Phase 4/5 kickoff — this phase is latency/payload/bundle/render only, as the mission's Phase-1 brief specifies.

> **No code was changed. This is the review artifact gating Phase 2.**

---

## 0. How to read these numbers (important caveat)

The measurement env is **local minikube at ~44 services**, not billion-span scale. So:

- The **warm / MV-eligible / cached path is excellent** here (single-digit ms) — that is *not* where the problems are.
- The audit findings are **cliffs that appear behind filters or at scale** — exactly the paths a 44-service synthetic env *cannot* fully exercise. This is the documented **CH-vs-ES divergence** ("fine locally, melts the test env"): ES cost-guard gaps (F-R3) and raw-spans fallbacks (F-R1/R4) won't bite locally but dominate at 10B docs/day.
- Two findings are **already visible even locally**: the 2.5 MB uncached `spans/metric` payload and the 900 KB `services/sparklines` payload.

Treat the cold raw-path latencies below as **lower bounds**; they degrade super-linearly with span volume because the offending queries lack `max_execution_time` / top-N / caching.

---

## 1. Live baseline measurements (this env)

Each endpoint hit twice (cold → warm), authenticated, via the `:8090` api port-forward.

| Endpoint | Cold TTFB | Warm TTFB | Payload | Path quality |
|---|---|---|---|---|
| `/api/health` | — | 19 ms | small | ✅ (5s budget) |
| `/api/services?range=1h` | 6 ms | **6 ms** | 9.2 KB | ✅ MV+cache, < 50 ms hot budget |
| `/api/servicegraph?range=1h` (global) | 22 ms | 6 ms | 30 KB | ✅ MV `topology_edges_5m` |
| `/api/operations?range=1h` | 34 ms | 5 ms | 5 KB | ✅ |
| `/api/spans/heatmap?range=1h` | 55 ms | 24 ms | 5.8 KB | ✅ (auto-sample) |
| `/api/metrics/names` | 95 ms | 6 ms | 14 KB | ✅ |
| `/api/traces?from=&to=` (ns) | 48 ms | — | 19 KB | ✅ MV happy path |
| `/api/logs?range=1h` | 81 ms | 23 ms | 106 KB | ES, uncached (see F-R3) |
| `/api/logs/timeseries?range=1h` | 172 ms | 7 ms | 235 KB | cached 30s; largish payload |
| `/api/services/sparklines?range=1h` | 262 ms | 8 ms | **900 KB** | ⚠️ huge payload (warm-cached) |
| `/api/spans/metric` p99 raw (no filter) | **509 ms** | — | **2.5 MB** | 🔴 uncached + unbounded + huge |
| `/api/spans/metric` p99 + dsl filter | 460 ms | — | 2.4 MB | 🔴 uncached raw path |
| `/api/traces?range=6h` (no from/to) | 870 ms | — | **HTTP 500** | ⚠️ param-contract gap (range= unsupported) |

Headline: the cache/MV layer is doing its job on the read-happy path. The cost is concentrated in **(a) uncached raw-spans analytics (`spans/metric`, filtered `traces`), (b) oversized JSON payloads, (c) the ingest write path under burst, and (d) ES cost-guard gaps invisible at this scale.**

---

## 2. Hot-path map: ingestion → ClickHouse → API → react-query → render

### Ingestion (write)
```
OTLP gRPC :4317 / HTTP :4318  (32MB max msg; one goroutine/request; no MaxConcurrentStreams)
  → ConvertTraces/Logs/Metrics  (SYNCHRONOUS in request goroutine — decode+transform on critical path)
       per span: heap-alloc row · attrsToArrays (2 fresh []string) · 8× linear attr scans · json.Marshal(events) ALWAYS
  → Ingester.addSpan → pipeline.AcceptSpan → sampler.Decide (FNV alloc/span) OR tail.Add (global mutex/span)
  → consumer.Consumer[T]  chan(500k) → batch(10k / 2s) → Workers=8 flushers
  → Store.InsertSpans/Logs/Metrics  asyncInsertCtx(✅ all 3, no bypass) → PrepareBatch/Send on s.conn pool (MaxOpenConns=10)
  → CH spans / logs / metric_points
```
### Read (API → render)
| Page | Endpoint | Handler | Source | Bounded? | Cached? |
|---|---|---|---|---|---|
| Services | `/api/services` | getServices | `service_summary_5m` | ✅ | ✅ 60s |
| Service detail | `/api/services/{n}/bundle` | getServiceBundle | MVs + raw fallback | ✅ | ✅ 60s |
| Topology (svc) | `/api/topology/service`, `/api/servicegraph` | getServiceTopology / getOtelServiceGraph | `topology_edges_5m FINAL` | ✅ | ✅ 30-60s |
| Traces | `/api/traces` | getTraces | `trace_summary_5m` **only if no filter**; else raw `GROUP BY trace_id` | partial | 🔴 **no** |
| Explore | `/api/spans/metric` | spanMetric | MV **only if step≥300s & no filter**; else raw `spans` | 🔴 **no `max_execution_time`** | 🔴 **no** |
| Explore | `/api/spans/heatmap` | spanHeatmap | raw `spans` (auto-sample) | ✅ | partial |
| Logs | `/api/logs` | getLogs | ES `Search` | PIT cap | 🔴 **no** |
| Logs | `/api/logs/timeseries` | getLogsTimeseries | ES histogram (has guards) | ✅ | ✅ 30s |

Frontend render: routes are code-split (`React.lazy` + Suspense), `manualChunks` split vendor/router/tanstack/charts/otel; first-load ~**154 KB gz** (vendor 98 + entry 30 + tanstack 20 + router 5). No `timeRangeToNs` render-traps, no sub-10s polling, no missing `document.hidden`, pickers server-side, tables virtualized-or-`content-visibility`. **No hard-constraint violations.**

---

## 3. Prioritized bottleneck table (impact × 1/effort)

Prefix: **R** = read/CH · **W** = write/ingest · **F** = frontend. Rank is the recommended PR order.

| # | Bottleneck | Evidence (file:line) | User/operator impact | Effort | Phase |
|---|---|---|---|---|---|
| 1 | **`spans/metric` raw path: no `max_execution_time`, exact `quantile()`, uncached, 2.5 MB payload** | `chstore/spanmetric.go:139-148,828-836,962-971`; `api.go:3551` (no serveCached) | Default Explore chart + Service charts; uncapped CH wall-time at scale; 2.5 MB/req live | **S** | 2 |
| 2 | **CH conn pool `MaxOpenConns=10` vs 24 flusher goroutines** | `chstore/store.go:37-40,150-151`; `main.go:245-258` | Insert serialization ceiling under burst → `/api/health` 503 → LB eviction even when CH isn't saturated | **S** | 2 |
| 3 | **`json.Marshal(events)` per span, unconditional (even 0 events)** | `otel/convert.go:73,364-377` | ~1B needless marshals+allocs/day → GC/CPU ceiling on ingest pods | **S** | 2 |
| 4 | **ES `/api/logs`: `track_total_hits:true` always, no `timeout`, no `request_cache`, uncached** | `logstore/elasticsearch.go:837,807-808`; `api_logs.go:41` | Full match-count + uncapped wall-time at 10B docs/day; the CH-vs-ES divergence — invisible locally | **S–M** | 2 |
| 5 | **`/api/traces` uncached + raw `GROUP BY trace_id` on any filter/search/DSL** | `api.go:2554`; `repo.go:1177-1191,1383-1407` | Common filtered-trace workflow scans raw spans to 60s ceiling, no warm path | **M** | 2 |
| 6 | **Per-span transform allocs: 2 `[]string` + 8 linear attr scans, `out` not pre-sized** | `otel/convert.go:22,58,61-69,100,144,297-308` | Decode/transform CPU+GC dominant at billion-span scale | **M** | 2 |
| 7 | **Tail sampler: global `sync.Mutex` on every `Add` + full-map sweep under same lock** | `sampling/tail.go:42,107-146,172-193` | With tail sampling on (bank-scale cfg) ingest collapses to one-core-serialized + 1s spikes | **M** | 2 |
| 8 | **Oversized payloads: `spans/metric` 2.5 MB, `services/sparklines` 900 KB** | live; `api.go` sparklines/spanMetric serializers | Bandwidth + JS parse + render cost on heavy pages | **M** | 2/3 |
| 9 | **Logs/metrics drops silent — not on `/api/health`, `Add` bool ignored** | `otel/grpc.go:67-87`; `otel/http.go:135-159`; `api.go:8109-8121` | Silent log/metric loss during incidents; collector gets no retry signal | **S** | 2/4 |
| 10 | **Browser OTel SDK boots every session (25.66 KB gz + wraps every fetch/click)** | `lib/browserOtel.ts`; `main.tsx:21` | TTFI cost on fresh tab; per-interaction span overhead; no runtime opt-out | **S** | 3 |
| 11 | **`dagre` (graph layout) in always-loaded `vendor` chunk, used only by topology** | `vite.config.ts:50-58`; `ServiceGraph.tsx:2` | Every cold first paint ships topology layout code (98 KB gz vendor) | **S** | 3 |
| 12 | **`/api/servicegraph` global scope serializes the entire graph — no server-side top-N** | `servicegraph.go:107-113,199-212` | Large JSON + heavy client layout at thousands of services (v0.6.48 regression shape) | **M** | 2/3 |
| 13 | **`/admin/stats` IngestRates raw `count()` on logs+metrics instead of in-mem counters** | `chstore/sysstats.go:238-249` | Adds CH read load during burst (competes with #2) — v0.5.319 shape | **S** | 2 |
| 14 | **Table gaps: DependenciesTable databases table no `content-visibility`; Explore 5000-row table not virtualized** | `DependenciesTable.tsx:261-266`; `Explore.tsx:894,1368-1430` | Jank on wide Service Dependencies tab + Explore at high trace limit | **S–M** | 3 |
| 15 | **Sampler `Decide` allocates FNV hasher + `[]byte(traceID)` per span under RLock** | `sampling/sampling.go:108-136,301-305` | Per-span GC churn on hot route when head sampling active | **S** | 2 |

**Also flagged (robustness, not perf):** `/api/traces?range=…` with no `from/to` returns HTTP 500 (`buildGetTracesWhere` repo.go:1062-1083 leaves the query time-unbounded; the parse path errors). Frontend always sends ns `from/to`, so not live-firing — but one bad caller (MCP tool/script) from a full-partition scan. Effort S.

---

## 4. What's already good (don't "fix")

- **Service & Topology reads** are MV-routed, bounded (`LIMIT` + `max_execution_time`), and cached. No action.
- **async_insert** (10MB/1s, `wait=1`) is applied to all 3 ingest INSERTs — **no bypass** (v0.5.346 locked, leave it).
- **Cache-key discipline**, server-side pickers, `document.hidden` gating, react-query global-service-graph dedup, prefetch-on-hover — all clean. No hard-constraint violations found.
- **Code-splitting** is healthy; routes lazy-load.

## 5. Needs a scale load-test to confirm (can't measure at 44 services)

1. `spans/metric` raw-path p99 + CH `query_log` at real volume (#1).
2. ES `took` / shard CPU for broad `/api/logs` with `track_total_hits:true` vs `10000` (#4).
3. Flusher block-time on pool acquisition vs CH async-flush rate → size `MaxOpenConns`/`Workers` (#2).
4. `pprof` alloc profile under synthetic OTLP load to rank #3/#6/#15 by measured bytes.
5. Tail-sampler mutex wait under burst with tail enabled (#7) — demote if most installs are head-only.
6. `/admin/cache-stats` hit ratios to quantify the win from adding caches (#1/#4/#5).

## 6. Recommended Phase-2 PR sequence (one bottleneck per PR, each with before→after)

S-effort, high-impact first: **#1 → #2 → #3 → #9 → #13 → #15**, then structural **#4 → #5 → #6 → #7 → #12**. Frontend (#10, #11, #14) can interleave as Phase 3. Each PR ships with a benchmark/test and an EXPLAIN or curl before→after per the mission constraints.

---

## 7. Ops note — monolithic minikube switch is blocked

The requested switch to monolithic mode could **not** be performed: the session permission policy auto-denies every mutating command (`helm upgrade`, `docker build`, `minikube image load`, `kubectl delete/scale`). The cluster is currently healthy **distributed v0.8.28** (not a wedged release). Phase-1 did not need monolithic — measurements above are topology-independent. To unblock, add to `.claude/settings.json` allow-list: `helm list/history/uninstall/upgrade/status *`, `docker build *`, `minikube image load *`, `kubectl delete/scale/apply/exec *`.
