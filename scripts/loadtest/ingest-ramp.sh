#!/usr/bin/env bash
#
# ingest-ramp.sh — ramp the Go demo's OTLP ingest up to a target rate to
# stress the write path (CH RSS, async_insert batching, the 15-MV fan-out).
#
# The single knob is cmd/demo's -rps flag (scenarios/sec). Each scenario is a
# multi-span trace (~10 spans avg across the ~45-service mesh), and the live
# diurnal/incident model (cmd/demo/realism.go) multiplies it by 0.28..1.0+ —
# so EFFECTIVE spans/sec ≈ rps × ~10 × rateFactor. Watch the REAL number on
# the Spans-ingest "rate/sec" in watch.sh, not the nominal -rps.
#
# SAFETY — this script RAMPS in steps and is STOPPABLE:
#   * It walks rps up one STEP at a time, holding each level for HOLD seconds,
#     so you can watch CH RSS / queue depth climb and abort before an OOM
#     rather than slamming the cluster at the top rate instantly.
#   * Each demo process is launched with -duration so it self-terminates even
#     if this script's trap is bypassed (kill -9, lost SSH). A forgotten run
#     can't firehose overnight.
#   * Ctrl-C (SIGINT) or SIGTERM kills the current demo child and exits.
#   * To stop EVERYTHING from another shell:  scripts/loadtest/stop.sh
#
# Two modes:
#   binary  (default) — `go run ./cmd/demo` or a prebuilt ./demo, one process
#                       per step, pointed at -endpoint. Best for local
#                       docker-compose where you can run go on the host.
#   k8s              — `kubectl scale`/`set env` the go-demo Deployment's rps.
#                       Best for a minikube / cluster install where the demo
#                       runs as a pod (charts/coremetry goDemo.rps).
#
# Usage:
#   scripts/loadtest/ingest-ramp.sh [options]
#
# Options (env or flag form):
#   -e, --endpoint URL    OTLP/HTTP endpoint        (default $ENDPOINT or
#                         http://localhost:14318 — compose host port for the
#                         collector; use http://localhost:8088 to bypass the
#                         collector and hit Coremetry's direct OTLP/HTTP)
#   -t, --target  RPS     top scenarios/sec to ramp to      (default 200)
#   -s, --start   RPS     starting scenarios/sec             (default 20)
#       --step    RPS     rps increment per ramp level       (default 20)
#       --hold    SEC     seconds to hold each level         (default 120)
#       --soak    SEC     extra seconds to hold at target    (default 600)
#   -m, --mode    MODE    binary | k8s                       (default binary)
#       --bin     PATH    prebuilt demo binary (binary mode; else `go run`)
#       --release NAME    helm release name  (k8s mode)      (default coremetry)
#   -n, --namespace NS    k8s namespace      (k8s mode)      (default default)
#
# Example — local compose, ramp 20→300 rps in +20 steps, 2-min holds:
#   scripts/loadtest/ingest-ramp.sh -e http://localhost:14318 -s 20 -t 300 --step 20 --hold 120
#
# Example — minikube pod, push goDemo to 150 rps:
#   scripts/loadtest/ingest-ramp.sh -m k8s -t 150 --release coremetry -n default
#
set -euo pipefail

ENDPOINT="${ENDPOINT:-http://localhost:14318}"
TARGET=200
START=20
STEP=20
HOLD=120
SOAK=600
MODE=binary
BIN=""
RELEASE=coremetry
NAMESPACE=default

while [[ $# -gt 0 ]]; do
  case "$1" in
    -e|--endpoint)  ENDPOINT="$2"; shift 2;;
    -t|--target)    TARGET="$2";   shift 2;;
    -s|--start)     START="$2";    shift 2;;
    --step)         STEP="$2";     shift 2;;
    --hold)         HOLD="$2";     shift 2;;
    --soak)         SOAK="$2";     shift 2;;
    -m|--mode)      MODE="$2";     shift 2;;
    --bin)          BIN="$2";      shift 2;;
    --release)      RELEASE="$2";  shift 2;;
    -n|--namespace) NAMESPACE="$2"; shift 2;;
    -h|--help)      sed -n '2,70p' "$0"; exit 0;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHILD_PID=""

cleanup() {
  if [[ -n "$CHILD_PID" ]] && kill -0 "$CHILD_PID" 2>/dev/null; then
    echo "[ramp] stopping demo child pid $CHILD_PID"
    kill -TERM "$CHILD_PID" 2>/dev/null || true
    wait "$CHILD_PID" 2>/dev/null || true
  fi
}
trap cleanup INT TERM EXIT

run_level_binary() {
  local rps="$1" secs="$2"
  echo "[ramp] level: ${rps} rps for ${secs}s  (effective spans/sec ≈ ${rps}×~10×diurnal)"
  # -duration self-limits the process so even a lost trap can't leave it
  # firehosing; +5s slack so this script controls the cadence, the flag is
  # the dead-man switch.
  local dur=$(( secs + 5 ))
  if [[ -n "$BIN" ]]; then
    "$BIN" -endpoint "$ENDPOINT" -rps "$rps" -duration "${dur}s" &
  else
    ( cd "$REPO_ROOT" && go run ./cmd/demo -endpoint "$ENDPOINT" -rps "$rps" -duration "${dur}s" ) &
  fi
  CHILD_PID=$!
  # Hold the level. We sleep in 1s ticks so a Ctrl-C is responsive.
  local i=0
  while [[ $i -lt $secs ]]; do
    if ! kill -0 "$CHILD_PID" 2>/dev/null; then
      echo "[ramp] demo child exited early — aborting ramp" >&2
      return 1
    fi
    sleep 1; i=$((i+1))
  done
  kill -TERM "$CHILD_PID" 2>/dev/null || true
  wait "$CHILD_PID" 2>/dev/null || true
  CHILD_PID=""
}

set_level_k8s() {
  local rps="$1" secs="$2"
  echo "[ramp] level: ${rps} rps for ${secs}s  (k8s: patching ${RELEASE}-go-demo)"
  # The go-demo Deployment passes rps as a CLI arg, not an env var, so the
  # cleanest in-place change is to patch the container arg. We append/replace
  # a DEMO_RPS env the entrypoint does NOT read by default — so instead patch
  # the args array. The deployment name follows coremetry.fullname-go-demo.
  local dep
  dep="$(kubectl -n "$NAMESPACE" get deploy -l app.kubernetes.io/component=go-demo \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  if [[ -z "$dep" ]]; then dep="${RELEASE}-go-demo"; fi
  # Replace the value that follows the "-rps" arg in container 0.
  kubectl -n "$NAMESPACE" patch deploy "$dep" --type=json -p "$(cat <<JSON
[{"op":"replace","path":"/spec/template/spec/containers/0/args",
  "value":["-endpoint","\$(OTLP_ENDPOINT)","-rps","${rps}"]}]
JSON
)" 2>/dev/null \
    || kubectl -n "$NAMESPACE" set env deploy/"$dep" DEMO_RPS="$rps" >/dev/null
  kubectl -n "$NAMESPACE" rollout status deploy/"$dep" --timeout=60s >/dev/null || true
  sleep "$secs"
}

echo "[ramp] mode=$MODE endpoint=$ENDPOINT  start=$START target=$TARGET step=$STEP hold=${HOLD}s soak=${SOAK}s"
echo "[ramp] watch ingest health in another shell:  scripts/loadtest/watch.sh -u <api-url>"
echo "[ramp] STOP at any time:  Ctrl-C here, or scripts/loadtest/stop.sh"

rps=$START
while [[ "$rps" -le "$TARGET" ]]; do
  if [[ "$MODE" == "k8s" ]]; then
    set_level_k8s "$rps" "$HOLD"
  else
    run_level_binary "$rps" "$HOLD" || break
  fi
  if [[ "$rps" -eq "$TARGET" ]]; then break; fi
  rps=$(( rps + STEP ))
  if [[ "$rps" -gt "$TARGET" ]]; then rps="$TARGET"; fi
done

echo "[ramp] soaking at $TARGET rps for ${SOAK}s"
if [[ "$MODE" == "k8s" ]]; then
  set_level_k8s "$TARGET" "$SOAK"
  echo "[ramp] done — demo pod LEFT at $TARGET rps. Reset with:"
  echo "       helm upgrade ... --set goDemo.rps=3   (or scripts/loadtest/stop.sh -m k8s)"
else
  run_level_binary "$TARGET" "$SOAK" || true
  echo "[ramp] done — all demo children stopped."
fi
