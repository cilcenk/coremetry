# Demo traffic generators — shared realism model

Coremetry ships two self-contained workloads that feed a fresh
install believable telemetry. They are what an operator sees on first
boot, so they must look like a real bank — not flat synthetic noise.
(A third, the Spring Boot `java-demo/`, was removed by operator
request in v0.9.1106.)

| Workload | Path | What it is |
|---|---|---|
| **Go demo** | `cmd/demo/` | Pure synthetic OTLP generator. Hand-builds traces/logs/metrics for a ~45-service retail-banking mesh (Oracle core, Kafka, polyglot runtimes) and POSTs OTLP/protobuf straight to the collector. No real services run. |
| **JBoss demo** | `jboss-demo/` | JAX-RS/JBoss app instrumented zero-code by the OTel **javaagent**, driven by `LoadDriver`. |

## Shared realism model (v0.8.x) — keep all workloads honest

Real production traffic is **not** a flat line with fixed error odds and
uniform latency. The Go demo (`cmd/demo/realism.go`) carries the load
model so metrics, traces and logs tell one coherent story:

1. **Diurnal curve + spikes.** A 0.28→1.0 business-day multiplier
   (overnight trough, ~10:00 peak, ~19:00 bump) plus organic
   micro-spikes scales the emission rate. The demo genuinely slows
   overnight and surges at the morning peak. Drives the Poisson
   inter-arrival gap in the driver loop.
2. **Incidents.** Every few minutes a 1–4 min degradation window starts
   (`oracle-row-lock-contention`, `jvm-gc-pause-storm`,
   `downstream-dependency-degraded`, `noisy-neighbor-cpu-steal`) that
   raises latency AND error rate together, then recovers on its own.
3. **Log-normal latency.** `dur()`
   samples a right-skewed distribution — dense body near the floor, long
   p99 tail — scaled by the live latency factor, so saturation shows up
   as a coordinated latency rise across every hop.
4. **Correlated errors.** `rollFail(pct)`
   folds the incident error-bump into each per-hop failure roll,
   so failures CLUSTER during an incident instead of being uniform.
   Error logs also spike in density and carry an `incident` attribute.
5. **Real histogram buckets.** Duration histograms emit explicit
   `ExplicitBounds` + `BucketCounts` (`latencyBounds` in Go;
   `OTEL_EXPORTER_OTLP_METRICS_DEFAULT_HISTOGRAM_AGGREGATION=explicit_bucket_histogram`
   for the javaagent-driven jboss-demo) so the backend computes real p50/p90/p95/p99 —
   not just min/max/avg.
6. **Richer saturation metrics.** Per-service connection-pool
   usage/max/utilization, cache hit-ratio, in-flight + queued requests,
   GC pause, host CPU/mem, and Kafka consumer lag — all load-correlated
   (Go: `flush()` gauge loop).

7. **Load-bound N+1 fan-out (v0.9.1284).** `MeshPortfolioValuation`
   (`portfolio-service`, `cmd/demo/mesh.go`) emits a Hibernate/JPA
   1 + N: one `SELECT positions`, then N identical
   `SELECT instrument_prices` client spans. N is **not** a constant —
   `nPlusOneRepeats(L.latencyFactor(), jitter)` gives ~8 at rest and
   climbs to the 40 ceiling while an incident saturates the mesh, with a
   floor of 6 so the repeat chip (threshold 5) is always live. It is the
   local fixture for the v0.9.1277 N+1 family: the waterfall repeat
   chip, the ×N sibling grouping, and Explore's
   `?result=repeats&groupBy=db.statement`.

**Rule:** any new demo scenario or metric must read from the load model
(`L`) rather than rolling its own fixed probability or
uniform latency, or it will visibly desync from the rest of the data.
