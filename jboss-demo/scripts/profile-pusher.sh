#!/bin/sh
# Continuously sample the JVM with async-profiler, push pprof bytes to Coremetry.
# Same wallclock pattern as java-demo — independent of the in-JVM JFR /
# Pyroscope agents so the demo lights up both /v1/profiles AND the
# Pyroscope UI without one starving the other.
set -u

SERVICE=${OTEL_SERVICE_NAME:-jboss-demo}
COREMETRY_URL=${COREMETRY_URL:-http://coremetry:8088}
INTERVAL=${PROFILE_INTERVAL_SEC:-15}
WINDOW=${PROFILE_WINDOW_SEC:-10}
ENGINE=${PROFILE_ENGINE:-wall}

ASPROF=/opt/async-profiler/bin/asprof
TMPFILE=/tmp/profile.txt

echo "[profiler] waiting for WildFly JVM to come up..."
# WildFly's startup is heavier than Spring Boot — give it 45s
# before the first capture and allow the discovery loop to keep
# trying for ~60s in case the env is slow.
sleep 45
PID=""
for i in $(seq 1 60); do
  PID=$(pgrep -f "jboss-modules" | head -n1 || true)
  [ -n "$PID" ] && break
  sleep 1
done
if [ -z "$PID" ]; then
  echo "[profiler] could not find JVM, exiting"
  exit 0
fi
echo "[profiler] attached to JVM PID=$PID engine=$ENGINE window=${WINDOW}s interval=${INTERVAL}s"

while true; do
  if ! kill -0 "$PID" 2>/dev/null; then
    echo "[profiler] JVM gone, exiting"; exit 0
  fi

  START_NS=$(date +%s%N)
  if "$ASPROF" -e "$ENGINE" -d "$WINDOW" -o collapsed -f "$TMPFILE" "$PID" >/tmp/asprof.log 2>&1; then
    SIZE=$(wc -c < "$TMPFILE" 2>/dev/null || echo 0)
    if [ "$SIZE" -gt 0 ]; then
      DUR_NS=$((WINDOW * 1000000000))
      HTTP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$COREMETRY_URL/v1/profiles" \
        -H "Content-Type: text/plain" \
        -H "X-Coremetry-Service: $SERVICE" \
        -H "X-Coremetry-Profile-Type: cpu" \
        -H "X-Coremetry-Start-Time-Ns: $START_NS" \
        -H "X-Coremetry-Duration-Ns: $DUR_NS" \
        --data-binary @"$TMPFILE")
      echo "[profiler] pushed cpu profile ($SIZE bytes, http=$HTTP)"
    else
      echo "[profiler] empty profile (no samples this window)"
    fi
  else
    echo "[profiler] asprof failed:"; tail -2 /tmp/asprof.log
  fi

  REM=$((INTERVAL - WINDOW))
  [ "$REM" -gt 0 ] && sleep "$REM"
done
