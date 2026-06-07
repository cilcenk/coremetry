#!/bin/sh
set -e
echo "[start] Java demo: launching with OpenTelemetry javaagent (zero-code instrumentation)"

# ── Simulated Oracle core-banking telemetry ──────────────────────────────────
# Turn OFF the agent's JDBC auto-instrumentation so the REAL H2 statements
# don't shadow the synthetic Oracle CLIENT spans that CoreBankingGateway emits
# manually (db.system=oracle, db.name=COREBANK, server.address=corebank-scan…).
# To an operator the trace then looks like a genuine Oracle round-trip; the
# H2/JPA work still happens underneath, just untraced by the agent.
# (Can be overridden from compose/helm by re-exporting the var if you ever
#  want to see the underlying H2 spans for debugging.)
export OTEL_INSTRUMENTATION_JDBC_ENABLED="${OTEL_INSTRUMENTATION_JDBC_ENABLED:-false}"

# ── Richer, more realistic metric/trace export ───────────────────────────────
# Match the Go demo's 10s flush cadence, attach trace exemplars to the
# duration histograms (so a p99 bar links straight to an exemplar trace),
# and turn on the agent's experimental JVM runtime telemetry (richer GC,
# thread, memory-pool, class-loading metrics) so the dashboards have real
# saturation signals to plot. All overridable from compose/helm.
export OTEL_METRIC_EXPORT_INTERVAL="${OTEL_METRIC_EXPORT_INTERVAL:-10000}"
export OTEL_METRICS_EXEMPLAR_FILTER="${OTEL_METRICS_EXEMPLAR_FILTER:-trace_based}"
export OTEL_INSTRUMENTATION_RUNTIME_TELEMETRY_EMIT_EXPERIMENTAL_TELEMETRY="${OTEL_INSTRUMENTATION_RUNTIME_TELEMETRY_EMIT_EXPERIMENTAL_TELEMETRY:-true}"
# Default-bucket histogram for HTTP/RPC durations so p50/p90/p95/p99 are
# computable backend-side (the agent emits explicit-bucket histograms).
export OTEL_EXPORTER_OTLP_METRICS_DEFAULT_HISTOGRAM_AGGREGATION="${OTEL_EXPORTER_OTLP_METRICS_DEFAULT_HISTOGRAM_AGGREGATION:-explicit_bucket_histogram}"

# Background CPU profiler — uses async-profiler, attaches via JVM TI (no app code).
/profile-pusher.sh &

# Foreground: Spring Boot app with the OTel agent injected at JVM startup.
# Everything (traces, metrics, logs) is auto-emitted via -javaagent.
# Stack the Pyroscope agent FIRST (it attaches async-profiler under the
# hood; the OTel agent doesn't conflict because they only race on the
# *event-bus* sampler, not the JVMTI hooks). Pyroscope agent is a no-op
# if PYROSCOPE_SERVER_ADDRESS isn't set, so this is safe in environments
# without Pyroscope wired in.
exec java \
  -javaagent:/agent/pyroscope.jar \
  -javaagent:/agent/opentelemetry-javaagent.jar \
  -XX:+UnlockDiagnosticVMOptions \
  -XX:+DebugNonSafepoints \
  -jar /app/demo.jar
