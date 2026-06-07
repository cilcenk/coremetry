#!/usr/bin/env bash
#
# demo-health.sh — live demo metric/trace health check.
#
# Runs against a RUNNING Coremetry instance (e.g. after `make docker-up-demo`)
# and verifies that the demo traffic generators are actually producing healthy,
# realistic telemetry: ingest is flowing, a believable number of services have
# data, RED metrics are populated, and p99 latency is computable (which only
# works because the duration histograms now ship explicit buckets).
#
# It does NOT build or run the demo itself — it observes the API. Bring the
# stack up first:  make docker-up-demo
#
# Config (env, with defaults):
#   COREMETRY_URL        base URL                      (default http://localhost:8088)
#   COREMETRY_EMAIL      login email                   (default admin@coremetry.local)
#   COREMETRY_PASSWORD   login password                (default admin)
#   SINCE                lookback window for /services (default 15m)
#   MIN_SERVICES         min services-with-data        (default 10)
#   MAX_ERROR_RATE       max overall error fraction    (default 0.25)
#   MAX_P99_MS           p99 ceiling per service (ms)  (default 8000)
#
# Exit code: 0 = healthy, 1 = one or more checks failed (CI / cron friendly).

set -uo pipefail

URL="${COREMETRY_URL:-http://localhost:8088}"
EMAIL="${COREMETRY_EMAIL:-admin@coremetry.local}"
PASSWORD="${COREMETRY_PASSWORD:-admin}"
SINCE="${SINCE:-15m}"
MIN_SERVICES="${MIN_SERVICES:-10}"
MAX_ERROR_RATE="${MAX_ERROR_RATE:-0.25}"
MAX_P99_MS="${MAX_P99_MS:-8000}"

URL="${URL%/}" # strip trailing slash

command -v curl    >/dev/null || { echo "FATAL: curl not found"; exit 1; }
command -v python3 >/dev/null || { echo "FATAL: python3 not found"; exit 1; }

COOKIES="$(mktemp -t coremetry-health.XXXXXX)"
trap 'rm -f "$COOKIES"' EXIT

ok()   { printf '  \xe2\x9c\x93 %s\n' "$*"; }
fail() { printf '  \xe2\x9c\x97 %s\n' "$*"; FAILED=1; }

FAILED=0

echo "Coremetry demo health — ${URL} (window: ${SINCE})"
echo "──────────────────────────────────────────────"

# 1) Ingest health (unauthenticated endpoint) ────────────────────────────────
HEALTH="$(curl -fsS --max-time 10 "${URL}/api/health" 2>/dev/null || true)"
if [ -z "$HEALTH" ]; then
  fail "Coremetry not reachable at ${URL}/api/health (is the stack up? 'make docker-up-demo')"
  echo "──────────────────────────────────────────────"
  echo "RESULT: UNHEALTHY"
  exit 1
fi

if echo "$HEALTH" | python3 - <<'PY'
import json, sys
h = json.load(sys.stdin)
status = h.get("status", "?")
acc = int(h.get("spans_accepted", 0))
print(f"  status={status}  spans_accepted={acc}  "
      f"metrics_accepted={h.get('metrics_accepted',0)}  "
      f"logs_accepted={h.get('logs_accepted',0)}")
sys.exit(0 if (status in ("ok", "degraded") and acc > 0) else 7)
PY
then ok "ingest flowing"; else fail "ingest unhealthy or no spans accepted"; fi

# 2) Authenticate ────────────────────────────────────────────────────────────
LOGIN_CODE="$(curl -fsS -o /dev/null -w '%{http_code}' --max-time 10 \
  -c "$COOKIES" -H 'Content-Type: application/json' \
  -d "{\"email\":\"${EMAIL}\",\"password\":\"${PASSWORD}\"}" \
  "${URL}/api/auth/login" 2>/dev/null || true)"
if [ "$LOGIN_CODE" != "200" ]; then
  fail "login failed (HTTP ${LOGIN_CODE}) for ${EMAIL} — set COREMETRY_EMAIL/COREMETRY_PASSWORD"
  echo "──────────────────────────────────────────────"
  echo "RESULT: UNHEALTHY"
  exit 1
fi
ok "authenticated as ${EMAIL}"

# 3) Services RED metrics ─────────────────────────────────────────────────────
SERVICES="$(curl -fsS --max-time 20 -b "$COOKIES" \
  "${URL}/api/services?since=${SINCE}&limit=500" 2>/dev/null || true)"
if [ -z "$SERVICES" ]; then
  fail "could not read /api/services"
  echo "──────────────────────────────────────────────"
  echo "RESULT: UNHEALTHY"
  exit 1
fi

if echo "$SERVICES" | python3 - "$MIN_SERVICES" "$MAX_ERROR_RATE" "$MAX_P99_MS" <<'PY'
import json, sys

min_services   = int(sys.argv[1])
max_error_rate = float(sys.argv[2])
max_p99_ms     = float(sys.argv[3])

data = json.load(sys.stdin)
# Response may be a bare list or {"services":[...]} / {"items":[...]}.
if isinstance(data, dict):
    rows = data.get("services") or data.get("items") or data.get("rows") or []
else:
    rows = data

def norm_rate(v):
    v = float(v or 0)
    return v / 100.0 if v > 1.0 else v  # tolerate percent-encoded rates

with_data   = [r for r in rows if int(r.get("spanCount", 0)) > 0]
total_spans = sum(int(r.get("spanCount", 0)) for r in rows)
total_err   = sum(int(r.get("errorCount", 0)) for r in rows)
overall_err = (total_err / total_spans) if total_spans else 0.0
with_p99    = [r for r in with_data if float(r.get("p99DurationMs", 0)) > 0]

worst = sorted(with_data, key=lambda r: norm_rate(r.get("errorRate")), reverse=True)[:5]
slow  = sorted(with_data, key=lambda r: float(r.get("p99DurationMs", 0)), reverse=True)[:5]

print(f"  services with data : {len(with_data)} (min {min_services})")
print(f"  total spans        : {total_spans:,}")
print(f"  overall error rate : {overall_err*100:.2f}% (max {max_error_rate*100:.0f}%)")
print(f"  services w/ p99>0  : {len(with_p99)}/{len(with_data)}")
if slow:
    print("  slowest p99 (ms)   : " +
          ", ".join(f"{r['name']}={float(r.get('p99DurationMs',0)):.0f}" for r in slow))
if worst and norm_rate(worst[0].get("errorRate")) > 0:
    print("  top error rates    : " +
          ", ".join(f"{r['name']}={norm_rate(r.get('errorRate'))*100:.1f}%"
                    for r in worst if norm_rate(r.get('errorRate')) > 0))

problems = []
if len(with_data) < min_services:
    problems.append(f"only {len(with_data)} services have data (< {min_services})")
if overall_err > max_error_rate:
    problems.append(f"overall error rate {overall_err*100:.1f}% exceeds {max_error_rate*100:.0f}%")
if with_data and not with_p99:
    problems.append("no service has a computable p99 — histogram buckets missing?")
over = [r['name'] for r in with_data if float(r.get('p99DurationMs', 0)) > max_p99_ms]
if over:
    problems.append(f"p99 over {max_p99_ms:.0f}ms: {', '.join(over[:5])}")

for p in problems:
    print("  ✗ " + p)
sys.exit(8 if problems else 0)
PY
then ok "services RED metrics healthy"; else fail "services RED metric checks failed"; fi

echo "──────────────────────────────────────────────"
if [ "$FAILED" -eq 0 ]; then
  echo "RESULT: HEALTHY"
  exit 0
else
  echo "RESULT: UNHEALTHY"
  exit 1
fi
