#!/usr/bin/env bash
#
# read-load.sh — concurrent read load against Coremetry's hot endpoints, to
# measure p99 vs the CLAUDE.md budgets while ingest-ramp.sh stresses the write
# path. Mirrors what a roomful of operators clicking around during an incident
# does to the API + serveCached + ClickHouse MV reads.
#
# Hot endpoints exercised (the ones in CLAUDE.md's perf budgets):
#   GET /api/services         hot (<50ms p99 warm)   service_summary_5m MV
#   GET /api/problems         hot (<50ms p99 warm)   problems RMT FINAL
#   GET /api/spans/metric     heaviest cached read   span MV / raw-spans fallback
#   GET /api/traces           server-paged trace list
#   GET /api/logs             logstore search (CH or ES)
#   GET /api/spans/heatmap    <3s for <=6h window    (optional, --heatmap)
#
# AUTH: every /api/* read except /api/health needs a JWT. This script logs in
# once (POST /api/auth/login) with the bootstrap admin and reuses the bearer
# token. Override creds with COREMETRY_EMAIL / COREMETRY_PASSWORD.
#
# TIME WINDOWS: /api/spans/metric, /api/traces, /api/logs take from/to as
# UNIX-NANOSECOND integers (parseTime in internal/api/api.go accepts ONLY a
# plain int64 ns — NOT relative "now-1h" tokens). This script computes a
# trailing window in ns at startup. Note: a fixed window means serveCached
# warms after the first call (30s span-metric TTL, 15s logs TTL) — that is
# REALISTIC (operators sit on a range) and is exactly the "warm p99" the
# budgets target. Pass --fresh-window to recompute the window every run so you
# measure COLD reads (<1s budget) instead.
#
# DRIVER: prefers `hey` (github.com/rakyll/hey), then `vegeta`, else a bash
# curl loop that reports a coarse p50/p95/max. hey/vegeta give true p99 —
# install one for budget-grade numbers:
#   go install github.com/rakyll/hey@latest
#
# SAFETY: read-only. Bounded by --duration and --concurrency. Ctrl-C stops it.
#
# Usage:
#   scripts/loadtest/read-load.sh [options]
#
# Options:
#   -u, --url URL         API base url     (default $COREMETRY_URL or
#                                           http://localhost:8088)
#   -c, --concurrency N   concurrent workers per endpoint     (default 20)
#   -d, --duration SEC    seconds to run each endpoint         (default 60)
#   -w, --window MIN      trailing query window, minutes       (default 60)
#       --service NAME    service to filter traces/metric/logs (default "" = all)
#       --heatmap         also hammer /api/spans/heatmap (heavy)
#       --fresh-window    recompute ns window each request (cold reads)
#
# Example — 20 workers, 2-min run, 1h window, against compose:
#   scripts/loadtest/read-load.sh -u http://localhost:8088 -c 20 -d 120
#
set -euo pipefail

URL="${COREMETRY_URL:-http://localhost:8088}"
CONC=20
DUR=60
WINMIN=60
SERVICE=""
HEATMAP=0
FRESH=0
EMAIL="${COREMETRY_EMAIL:-admin@coremetry.local}"
PASSWORD="${COREMETRY_PASSWORD:-admin}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -u|--url)         URL="$2"; shift 2;;
    -c|--concurrency) CONC="$2"; shift 2;;
    -d|--duration)    DUR="$2"; shift 2;;
    -w|--window)      WINMIN="$2"; shift 2;;
    --service)        SERVICE="$2"; shift 2;;
    --heatmap)        HEATMAP=1; shift;;
    --fresh-window)   FRESH=1; shift;;
    -h|--help)        sed -n '2,60p' "$0"; exit 0;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done

URL="${URL%/}"

# ── now() in ns and the trailing window ─────────────────────────────────────
now_ns() {
  # GNU date supports %N; macOS/BSD date does not — fall back to python/perl.
  local n
  n="$(date +%s%N 2>/dev/null || true)"
  if [[ "$n" == *N* || -z "$n" ]]; then
    n="$(python3 -c 'import time;print(time.time_ns())' 2>/dev/null \
       || perl -MTime::HiRes=time -e 'printf("%d", time()*1e9)')"
  fi
  echo "$n"
}
TO_NS="$(now_ns)"
FROM_NS=$(( TO_NS - WINMIN*60*1000000000 ))

# ── login → bearer token ────────────────────────────────────────────────────
echo "[read] logging in as $EMAIL @ $URL"
LOGIN_JSON="$(curl -sS -X POST "$URL/api/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}")" || {
    echo "[read] login request failed — is $URL reachable?" >&2; exit 1; }
TOKEN="$(printf '%s' "$LOGIN_JSON" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
if [[ -z "$TOKEN" ]]; then
  echo "[read] login failed: $LOGIN_JSON" >&2
  echo "[read] set COREMETRY_EMAIL / COREMETRY_PASSWORD if you changed the admin creds." >&2
  exit 1
fi
AUTH=( -H "Authorization: Bearer $TOKEN" )
echo "[read] authenticated."

svc_q=""
[[ -n "$SERVICE" ]] && svc_q="&service=$SERVICE"

# Build the endpoint URL list. from/to are baked at startup (warm) unless
# --fresh-window (each driver call recomputes — only meaningful for the bash
# fallback; hey/vegeta hold a fixed target, which is the warm case).
build_targets() {
  local to from
  to="$TO_NS"; from="$FROM_NS"
  if [[ "$FRESH" == "1" ]]; then to="$(now_ns)"; from=$(( to - WINMIN*60*1000000000 )); fi
  TARGETS=(
    "services|$URL/api/services"
    "problems|$URL/api/problems"
    "span-metric|$URL/api/spans/metric?agg=rate&groupBy=name&from=$from&to=$to&step=60${svc_q}"
    "traces|$URL/api/traces?from=$from&to=$to&limit=50&sort=duration&order=desc${svc_q}"
    "logs|$URL/api/logs?from=$from&to=$to&limit=100${svc_q}"
  )
  if [[ "$HEATMAP" == "1" ]]; then
    TARGETS+=( "heatmap|$URL/api/spans/heatmap?from=$from&to=$to${svc_q}" )
  fi
}
build_targets

DRIVER=""
if command -v hey >/dev/null 2>&1;    then DRIVER=hey;
elif command -v vegeta >/dev/null 2>&1; then DRIVER=vegeta;
else DRIVER=curl; fi
echo "[read] driver=$DRIVER concurrency=$CONC duration=${DUR}s window=${WINMIN}m"
echo "[read] window ns: from=$FROM_NS to=$TO_NS"
[[ "$DRIVER" == "curl" ]] && echo "[read] NOTE: bash curl fallback reports coarse p50/p95/max — install 'hey' for true p99."

run_hey() {     # name url
  echo "── $1 ──────────────────────────────────────"
  hey -z "${DUR}s" -c "$CONC" -H "Authorization: Bearer $TOKEN" "$2" \
    | grep -E 'Requests/sec|Total:|Average:|Slowest:|99%|95%|50%|Status code' || true
}
run_vegeta() {  # name url
  echo "── $1 ──────────────────────────────────────"
  echo "GET $2" | vegeta attack -duration="${DUR}s" -rate=0 -max-workers="$CONC" \
    -header "Authorization: Bearer $TOKEN" \
    | vegeta report -type=text | grep -E 'Requests|Latencies|Success|Status' || true
}
run_curl() {    # name url — coarse fallback
  echo "── $1 ──────────────────────────────────────"
  local end=$(( $(date +%s) + DUR )) tmp; tmp="$(mktemp)"
  # CONC parallel loops appending request times (ms) until the deadline.
  for ((k=0;k<CONC;k++)); do
    (
      while [[ $(date +%s) -lt $end ]]; do
        local u="$2"
        [[ "$FRESH" == "1" ]] && { local t f; t="$(now_ns)"; f=$(( t - WINMIN*60*1000000000 )); u="${u//from=$FROM_NS/from=$f}"; u="${u//to=$TO_NS/to=$t}"; }
        curl -sS -o /dev/null -w '%{time_total}\n' "${AUTH[@]}" "$u" 2>/dev/null
      done
    ) >> "$tmp" &
  done
  wait
  awk '{ms=$1*1000; a[NR]=ms; s+=ms} END{
    n=NR; if(n==0){print "  no samples"; exit}
    asort(a);
    printf "  n=%d  mean=%.0fms  p50=%.0fms  p95=%.0fms  max=%.0fms\n",
      n, s/n, a[int(n*0.50)], a[int(n*0.95)], a[n]
  }' "$tmp"
  rm -f "$tmp"
}

trap 'echo; echo "[read] interrupted — stopping"; exit 0' INT TERM

for t in "${TARGETS[@]}"; do
  name="${t%%|*}"; url="${t#*|}"
  case "$DRIVER" in
    hey)    run_hey    "$name" "$url";;
    vegeta) run_vegeta "$name" "$url";;
    curl)   run_curl   "$name" "$url";;
  esac
done

echo
echo "[read] done. Compare against CLAUDE.md budgets:"
echo "  /api/services, /api/problems  p99 < 50ms  (warm)"
echo "  /api/spans/metric, /api/traces, /api/logs  p99 < 200ms warm / < 1s cold"
echo "  /api/spans/heatmap  < 3s for <=6h window"
