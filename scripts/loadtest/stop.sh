#!/usr/bin/env bash
#
# stop.sh — kill switch for the load test. Stops the demo generator(s) so
# ingest drains back to baseline. Safe to run from any shell.
#
#   binary mode (default): SIGTERM every `cmd/demo` / `go run ./cmd/demo`
#                          process this user owns. They flush final metrics
#                          and exit cleanly.
#   k8s mode:              scale the go-demo Deployment back to baseline rps
#                          (or to 0 replicas with --zero).
#
# Usage:
#   scripts/loadtest/stop.sh                 # binary: kill all demo procs
#   scripts/loadtest/stop.sh -m k8s          # k8s: reset goDemo rps to 3
#   scripts/loadtest/stop.sh -m k8s --zero   # k8s: scale go-demo to 0
#
set -euo pipefail

MODE=binary
RELEASE=coremetry
NAMESPACE=default
RESET_RPS=3
ZERO=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    -m|--mode)      MODE="$2"; shift 2;;
    --release)      RELEASE="$2"; shift 2;;
    -n|--namespace) NAMESPACE="$2"; shift 2;;
    --reset-rps)    RESET_RPS="$2"; shift 2;;
    --zero)         ZERO=1; shift;;
    -h|--help)      sed -n '2,20p' "$0"; exit 0;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done

if [[ "$MODE" == "k8s" ]]; then
  dep="$(kubectl -n "$NAMESPACE" get deploy -l app.kubernetes.io/component=go-demo \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  [[ -z "$dep" ]] && dep="${RELEASE}-go-demo"
  if [[ "$ZERO" == "1" ]]; then
    echo "[stop] scaling $dep to 0 replicas"
    kubectl -n "$NAMESPACE" scale deploy/"$dep" --replicas=0
  else
    echo "[stop] resetting $dep to -rps $RESET_RPS"
    kubectl -n "$NAMESPACE" patch deploy "$dep" --type=json -p \
      "[{\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/args\",\"value\":[\"-endpoint\",\"\$(OTLP_ENDPOINT)\",\"-rps\",\"${RESET_RPS}\"]}]" 2>/dev/null \
      || kubectl -n "$NAMESPACE" set env deploy/"$dep" DEMO_RPS="$RESET_RPS"
  fi
  exit 0
fi

# binary mode — find demo processes (the compiled binary OR `go run ./cmd/demo`)
echo "[stop] sending SIGTERM to demo generator processes…"
pids="$(pgrep -f 'cmd/demo' 2>/dev/null || true)"
# also the `go run` wrapper's child named 'demo' or with -rps in argv
pids="$pids $(pgrep -f -- '-rps' 2>/dev/null | head -50 || true)"
pids="$(echo "$pids" | tr ' ' '\n' | grep -E '^[0-9]+$' | sort -u || true)"
if [[ -z "$pids" ]]; then
  echo "[stop] no demo processes found."
  exit 0
fi
echo "$pids" | while read -r p; do
  [[ -z "$p" ]] && continue
  # Don't kill THIS script or pgrep.
  [[ "$p" == "$$" ]] && continue
  echo "  kill -TERM $p ($(ps -o command= -p "$p" 2>/dev/null | cut -c1-60))"
  kill -TERM "$p" 2>/dev/null || true
done
echo "[stop] done. Ingest will drain to baseline within ~10s."
