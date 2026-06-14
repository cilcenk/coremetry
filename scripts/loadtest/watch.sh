#!/usr/bin/env bash
#
# watch.sh — poll the meta-observability surfaces during a load test and append
# one CSV row every INTERVAL seconds, so you can see ingest keeping up (or not),
# CH RSS climbing toward the 2Gi local OOM cliff, and read-path drops.
#
# What it samples each tick:
#   /api/health (PUBLIC, no auth)
#       spans_queued / spans_capacity   — backpressure depth (503 at >=90%)
#       spans_dropped / write_failed     — DATA LOSS (must stay 0)
#       spans_accepted (delta → /sec)    — real accepted spans/sec
#       same for logs + metrics
#   /api/admin/stats  (admin JWT)        — GetSystemStats: ingest rates, total
#                                          disk bytes, per-table rows, drops
#   /api/admin/cache-stats (admin JWT)   — cache tier hit counts (l1/redis/...)
#   docker stats <CH container>          — ClickHouse RSS (the OOM signal;
#                                          [[project-scale-test-result]]: local
#                                          2Gi CH OOMs on MV bulk inserts)
#
# Reference: the prior 7.4x / 114M-span run found MV reads flat + raw-scan
# within budget, but a 2Gi CH OOM'd on MV bulk inserts. WATCH ch_rss_mb: if it
# marches toward your CH memory limit while spans_dropped stays 0, you are
# heading for the same cliff — back the ramp off a step.
#
# Output: a CSV to --out (default loadtest-$(date).csv) AND a live one-line
# summary to the terminal each tick.
#
# SAFETY: read-only polling. Ctrl-C stops it; the CSV is preserved.
#
# Usage:
#   scripts/loadtest/watch.sh [options]
#
# Options:
#   -u, --url URL        API base url   (default $COREMETRY_URL or
#                                        http://localhost:8088)
#   -i, --interval SEC   poll cadence   (default 10; CLAUDE.md min for non-health)
#       --ch-container NAME  docker/podman container running ClickHouse
#                            (default: autodetect a name containing 'clickhouse')
#       --out FILE       CSV output path
#       --no-admin       skip the admin /stats + /cache-stats (health-only;
#                        no login needed)
#   COREMETRY_EMAIL / COREMETRY_PASSWORD  admin creds (default admin@coremetry.local / admin)
#
set -euo pipefail

URL="${COREMETRY_URL:-http://localhost:8088}"
INTERVAL=10
CH_CONTAINER=""
OUT=""
NO_ADMIN=0
EMAIL="${COREMETRY_EMAIL:-admin@coremetry.local}"
PASSWORD="${COREMETRY_PASSWORD:-admin}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -u|--url)        URL="$2"; shift 2;;
    -i|--interval)   INTERVAL="$2"; shift 2;;
    --ch-container)  CH_CONTAINER="$2"; shift 2;;
    --out)           OUT="$2"; shift 2;;
    --no-admin)      NO_ADMIN=1; shift;;
    -h|--help)       sed -n '2,45p' "$0"; exit 0;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done
URL="${URL%/}"
[[ -z "$OUT" ]] && OUT="loadtest-$(date +%Y%m%d-%H%M%S).csv"

# docker or podman?
CTL=""
command -v docker >/dev/null 2>&1 && CTL=docker
[[ -z "$CTL" ]] && command -v podman >/dev/null 2>&1 && CTL=podman

if [[ -z "$CH_CONTAINER" && -n "$CTL" ]]; then
  CH_CONTAINER="$($CTL ps --format '{{.Names}}' 2>/dev/null | grep -i clickhouse | head -1 || true)"
fi
[[ -n "$CH_CONTAINER" ]] && echo "[watch] ClickHouse container: $CH_CONTAINER (via $CTL)" \
                         || echo "[watch] no ClickHouse container detected — ch_rss_mb will be blank"

# jq optional; we degrade to sed if absent.
HAVE_JQ=0; command -v jq >/dev/null 2>&1 && HAVE_JQ=1

jget() {  # json field — jq path or sed key
  local json="$1" key="$2"
  if [[ "$HAVE_JQ" == "1" ]]; then
    printf '%s' "$json" | jq -r "$key // 0" 2>/dev/null || echo 0
  else
    # crude: extract "key":<number>
    printf '%s' "$json" | sed -n "s/.*\"${key##*.}\":\([0-9.]*\).*/\1/p" | head -1
  fi
}

TOKEN=""
if [[ "$NO_ADMIN" == "0" ]]; then
  LOGIN_JSON="$(curl -sS -X POST "$URL/api/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" 2>/dev/null || true)"
  TOKEN="$(printf '%s' "$LOGIN_JSON" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
  if [[ -z "$TOKEN" ]]; then
    echo "[watch] admin login failed — falling back to health-only (--no-admin)." >&2
    NO_ADMIN=1
  fi
fi

ch_rss_mb() {
  [[ -z "$CH_CONTAINER" || -z "$CTL" ]] && { echo ""; return; }
  # MemUsage looks like "412.3MiB / 2GiB"; take the first token, normalise to MB.
  local raw; raw="$($CTL stats --no-stream --format '{{.MemUsage}}' "$CH_CONTAINER" 2>/dev/null | awk '{print $1}')"
  [[ -z "$raw" ]] && { echo ""; return; }
  awk -v v="$raw" 'BEGIN{
    n=v; sub(/[A-Za-z]+$/,"",n);
    if (v ~ /GiB/) printf "%.0f", n*1024;
    else if (v ~ /MiB/) printf "%.0f", n;
    else if (v ~ /KiB/) printf "%.1f", n/1024;
    else printf "%s", n;
  }'
}

header="ts,spans_acc,spans_qd,spans_cap,spans_drop,spans_wfail,logs_acc,logs_drop,metrics_acc,metrics_drop,health_status,ch_rss_mb,cache_l1,cache_redis,cache_miss"
echo "$header" > "$OUT"
echo "[watch] writing $OUT every ${INTERVAL}s — Ctrl-C to stop."
echo "[watch] (delta-based per-sec rates: divide consecutive *_acc by ${INTERVAL})"

trap 'echo; echo "[watch] stopped. CSV: '"$OUT"'"; exit 0' INT TERM

while true; do
  ts="$(date +%s)"
  H="$(curl -sS "$URL/api/health" 2>/dev/null || echo '{}')"
  s_acc="$(jget "$H" '.spans_accepted')";  s_qd="$(jget "$H" '.spans_queued')"
  s_cap="$(jget "$H" '.spans_capacity')";  s_drop="$(jget "$H" '.spans_dropped')"
  s_wf="$(jget "$H" '.spans_write_failed')"
  l_acc="$(jget "$H" '.logs_accepted')";   l_drop="$(jget "$H" '.logs_dropped')"
  m_acc="$(jget "$H" '.metrics_accepted')"; m_drop="$(jget "$H" '.metrics_dropped')"
  if [[ "$HAVE_JQ" == "1" ]]; then status="$(printf '%s' "$H" | jq -r '.status // "?"')"; else
    status="$(printf '%s' "$H" | sed -n 's/.*"status":"\([a-z]*\)".*/\1/p')"; fi

  ch="$(ch_rss_mb)"
  c_l1=""; c_redis=""; c_miss=""
  if [[ "$NO_ADMIN" == "0" ]]; then
    CS="$(curl -sS -H "Authorization: Bearer $TOKEN" "$URL/api/admin/cache-stats" 2>/dev/null || echo '{}')"
    if [[ "$HAVE_JQ" == "1" ]]; then
      c_l1="$(printf '%s' "$CS"   | jq -r '.counts.l1 // 0')"
      c_redis="$(printf '%s' "$CS" | jq -r '.counts.redis // 0')"
      c_miss="$(printf '%s' "$CS"  | jq -r '.counts.miss // 0')"
    fi
  fi

  echo "${ts},${s_acc:-0},${s_qd:-0},${s_cap:-0},${s_drop:-0},${s_wf:-0},${l_acc:-0},${l_drop:-0},${m_acc:-0},${m_drop:-0},${status:-?},${ch},${c_l1},${c_redis},${c_miss}" >> "$OUT"
  printf '[watch] %s  status=%-10s spans_q=%s/%s drop=%s wfail=%s  ch_rss=%sMB\n' \
    "$(date +%H:%M:%S)" "${status:-?}" "${s_qd:-0}" "${s_cap:-0}" "${s_drop:-0}" "${s_wf:-0}" "${ch:-?}"

  sleep "$INTERVAL"
done
