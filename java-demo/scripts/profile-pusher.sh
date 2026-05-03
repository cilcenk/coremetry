#!/bin/sh
# Continuously sample the JVM with async-profiler, push pprof bytes to Qmetry.
# This is the zero-code profiling hook — the Java code knows nothing about it.
set -u

SERVICE=${OTEL_SERVICE_NAME:-java-demo}
QMETRY_URL=${QMETRY_URL:-http://qmetry:4318}
INTERVAL=${PROFILE_INTERVAL_SEC:-15}
WINDOW=${PROFILE_WINDOW_SEC:-5}
ENGINE=${PROFILE_ENGINE:-itimer}   # itimer works without perf_event_paranoid tuning

ASPROF=/opt/async-profiler/bin/asprof
TMPFILE=/tmp/profile.txt   # collapsed-stacks text format

echo "[profiler] waiting for JVM to come up..."
# Give Spring Boot ~25s before first capture (startup is heavy)
sleep 25
PID=""
for i in $(seq 1 30); do
  PID=$(pgrep -f "demo.jar" | head -n1 || true)
  [ -n "$PID" ] && break
  sleep 1
done
if [ -z "$PID" ]; then
  echo "[profiler] could not find JVM, exiting"
  exit 0
fi
echo "[profiler] attached to JVM PID=$PID — engine=$ENGINE window=${WINDOW}s interval=${INTERVAL}s"

while true; do
  if ! kill -0 "$PID" 2>/dev/null; then
    echo "[profiler] JVM gone, exiting"; exit 0
  fi

  START_NS=$(date +%s%N)
  if "$ASPROF" -e "$ENGINE" -d "$WINDOW" -o collapsed -f "$TMPFILE" "$PID" >/tmp/asprof.log 2>&1; then
    SIZE=$(wc -c < "$TMPFILE" 2>/dev/null || echo 0)
    if [ "$SIZE" -gt 0 ]; then
      DUR_NS=$((WINDOW * 1000000000))
      HTTP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$QMETRY_URL/v1/profiles" \
        -H "Content-Type: text/plain" \
        -H "X-Qmetry-Service: $SERVICE" \
        -H "X-Qmetry-Profile-Type: cpu" \
        -H "X-Qmetry-Start-Time-Ns: $START_NS" \
        -H "X-Qmetry-Duration-Ns: $DUR_NS" \
        --data-binary @"$TMPFILE")
      echo "[profiler] pushed cpu profile ($SIZE bytes, http=$HTTP)"
    else
      echo "[profiler] empty profile (no samples this window)"
    fi
  else
    echo "[profiler] asprof failed:"; tail -2 /tmp/asprof.log
  fi

  # Sleep the remainder of the cycle
  REM=$((INTERVAL - WINDOW))
  [ "$REM" -gt 0 ] && sleep "$REM"
done
