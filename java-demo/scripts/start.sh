#!/bin/sh
set -e
echo "[start] Java demo: launching with OpenTelemetry javaagent (zero-code instrumentation)"

# Background CPU profiler — uses async-profiler, attaches via JVM TI (no app code).
/profile-pusher.sh &

# Foreground: Spring Boot app with the OTel agent injected at JVM startup.
# Everything (traces, metrics, logs) is auto-emitted via -javaagent.
exec java \
  -javaagent:/agent/opentelemetry-javaagent.jar \
  -XX:+UnlockDiagnosticVMOptions \
  -XX:+DebugNonSafepoints \
  -jar /app/demo.jar
