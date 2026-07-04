# Demo traffic generators — shared realism model

Coremetry ships three self-contained workloads that feed a fresh
install believable telemetry. They are what an operator sees on first
boot, so they must look like a real bank — not flat synthetic noise.

| Workload | Path | What it is |
|---|---|---|
| **Go demo** | `cmd/demo/` | Pure synthetic OTLP generator. Hand-builds traces/logs/metrics for a ~45-service retail-banking mesh (Oracle core, Kafka, polyglot runtimes) and POSTs OTLP/protobuf straight to the collector. No real services run. |
| **Java demo** | `java-demo/` | Real Spring Boot app instrumented zero-code by the OTel **javaagent**. A `LoadGenerator` drives its own HTTP endpoints; `CoreBankingGateway` emits manual Oracle CLIENT spans (JDBC auto-instr is OFF so synthetic Oracle spans don't get shadowed). |
| **JBoss demo** | `jboss-demo/` | JAX-RS/JBoss app, same javaagent pattern, driven by `LoadDriver`. |

## Shared realism model (v0.8.x) — keep all three honest

Real production traffic is **not** a flat line with fixed error odds and
uniform latency. Both the Go demo (`cmd/demo/realism.go`) and the Java
demo (`com.coremetry.demo.service.DemoLoad`) carry the SAME load model so
metrics, traces and logs tell one coherent story:

1. **Diurnal curve + spikes.** A 0.28→1.0 business-day multiplier
   (overnight trough, ~10:00 peak, ~19:00 bump) plus organic
   micro-spikes scales the emission rate. The demo genuinely slows
   overnight and surges at the morning peak. Go: drives the Poisson
   inter-arrival gap in the driver loop. Java: `DemoLoad.burstCount()`
   governs how many scenarios each `LoadGenerator` tick fires.
2. **Incidents.** Every few minutes a 1–4 min degradation window starts
   (`oracle-row-lock-contention`, `jvm-gc-pause-storm`,
   `downstream-dependency-degraded`, `noisy-neighbor-cpu-steal`) that
   raises latency AND error rate together, then recovers on its own.
3. **Log-normal latency.** `dur()` (Go) / `DemoLoad.sampleMs()` (Java)
   sample a right-skewed distribution — dense body near the floor, long
   p99 tail — scaled by the live latency factor, so saturation shows up
   as a coordinated latency rise across every hop.
4. **Correlated errors.** `rollFail(pct)` (Go) / `DemoLoad.roll(pct)`
   (Java) fold the incident error-bump into each per-hop failure roll,
   so failures CLUSTER during an incident instead of being uniform.
   Error logs also spike in density and carry an `incident` attribute.
5. **Real histogram buckets.** Duration histograms emit explicit
   `ExplicitBounds` + `BucketCounts` (`latencyBounds` in Go;
   `OTEL_EXPORTER_OTLP_METRICS_DEFAULT_HISTOGRAM_AGGREGATION=explicit_bucket_histogram`
   for the Java agent) so the backend computes real p50/p90/p95/p99 —
   not just min/max/avg.
6. **Richer saturation metrics.** Per-service connection-pool
   usage/max/utilization, cache hit-ratio, in-flight + queued requests,
   GC pause, host CPU/mem, and Kafka consumer lag — all load-correlated
   (Go: `flush()` gauge loop; Java: `DemoMetrics` observable gauges +
   business counters via the agent's MeterProvider). Java custom gauges
   are namespaced `demo.*` to avoid colliding with agent-emitted
   semconv metric names (e.g. HikariCP `db.client.connections.usage`).

**Rule:** any new demo scenario or metric must read from the load model
(`L` / `DemoLoad`) rather than rolling its own fixed probability or
uniform latency, or it will visibly desync from the rest of the data.
