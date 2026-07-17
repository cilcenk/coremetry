#!/usr/bin/env bash
#
# seed-deployment-report-problems.sh — inserts a varied batch of fake
# open/resolved Problems directly into ClickHouse for manually testing
# /deployment-report locally. See docs/DEPLOYMENT-REPORT-TESTING.md.
#
# Seeds ~14 problems across many services, severities, and metrics,
# spread around a fake "deploy" timestamp:
#   - most are AFTER the deploy and still open  -> should APPEAR in the report
#   - one is BEFORE the deploy (still open)      -> should be EXCLUDED
#   - one is AFTER the deploy but resolved       -> should be EXCLUDED
# That mix lets you eyeball both the inclusion gate and a report with
# real variety (multiple services, severities, P1/P2/P3 priorities)
# in one shot, instead of hand-inserting one row at a time.
#
# Requires: docker (running the coremetry-clickhouse container), python3.
#
# Usage:
#   scripts/seed-deployment-report-problems.sh            # seed
#   scripts/seed-deployment-report-problems.sh --clean     # delete seeded rows
#
# Config (env, with defaults):
#   CH_CONTAINER     ClickHouse container name   (default coremetry-clickhouse)
#   CH_DATABASE      ClickHouse database         (default coremetry)
#   DEPLOY_AGO_MIN   minutes ago the fake deploy "succeeded" (default 120 = 2h)

set -euo pipefail

CH_CONTAINER="${CH_CONTAINER:-coremetry-clickhouse}"
CH_DATABASE="${CH_DATABASE:-coremetry}"
DEPLOY_AGO_MIN="${DEPLOY_AGO_MIN:-120}"

command -v docker  >/dev/null || { echo "FATAL: docker not found"; exit 1; }
command -v python3 >/dev/null || { echo "FATAL: python3 not found"; exit 1; }

ch() {
  docker exec "$CH_CONTAINER" clickhouse-client --database="$CH_DATABASE" --query "$1" < /dev/null
}

if [[ "${1:-}" == "--clean" ]]; then
  echo "Deleting all rows with id LIKE 'demo-problem-%' from $CH_DATABASE.problems ..."
  ch "ALTER TABLE problems DELETE WHERE id LIKE 'demo-problem-%'"
  echo "Done (ALTER DELETE is async — give it a few seconds before re-querying)."
  exit 0
fi

# id | service | severity | rule_name | metric | value | threshold | offset_min_from_deploy | status
# offset_min_from_deploy: minutes AFTER the deploy the problem started.
# Negative = started BEFORE the deploy (exclusion sanity check).
ROWS=$(cat <<'EOF'
demo-problem-01|auth-service|critical|High error rate|error_rate|18.0|5.0|5|open
demo-problem-02|fraud-service|critical|Fraud score latency spike|p99_ms|1200|400|12|open
demo-problem-03|payment-init-api|warning|Elevated P99 latency|p99_ms|650|400|20|open
demo-problem-04|entitlements-service|warning|Request rate drop|request_rate|2.0|10.0|8|open
demo-problem-05|preferences-service|info|Cache miss ratio elevated|error_count|340|200|45|open
demo-problem-06|reconciliation-service|critical|Reconciliation failures|error_count|58|20|3|open
demo-problem-07|archive-service|warning|Archive job error rate|error_rate|6.5|5.0|15|open
demo-problem-08|audit-trail-service|warning|Audit write latency|avg_ms|340|200|6|open
demo-problem-09|device-registry|critical|Device lookup errors|error_rate|40.0|5.0|60|open
demo-problem-10|case-manager|critical|Case write failures|error_count|21|8|30|open
demo-problem-11|liquidity-manager|warning|Liquidity check latency|p99_ms|900|600|18|open
demo-problem-12|sanctions-screening-v2|critical|Screening timeout rate|error_rate|15.0|5.0|9|open
demo-problem-13|account-info-api|critical|Timeout rate spike (pre-deploy, must be excluded)|error_rate|22.0|5.0|-10|open
demo-problem-14|key-rotation-service|critical|Key rotation failures (resolved, must be excluded)|error_count|12|3.0|25|resolved
EOF
)

echo "Deploy timestamp: ${DEPLOY_AGO_MIN} minutes ago"
echo

# Compute the deploy epoch + each row's absolute started_at via python3
# (portable across BSD/GNU date, which disagree on offset-arithmetic flags).
read -r DEPLOY_EPOCH_NS DEPLOY_LOCAL DEPLOY_UTC < <(python3 -c "
import time
now = time.time()
deploy = now - ${DEPLOY_AGO_MIN} * 60
print(int(deploy * 1e9), time.strftime('%Y-%m-%dT%H:%M', time.localtime(deploy)), time.strftime('%Y-%m-%d %H:%M:%S UTC', time.gmtime(deploy)))
")

echo "Fake deploy time : $DEPLOY_UTC"
echo "  since (ns)     : $DEPLOY_EPOCH_NS"
echo "  UI picker value: $DEPLOY_LOCAL   (paste into the 'Deployment succeeded at' field, your local timezone)"
echo

printf '%-16s %-24s %-9s %-45s %6s\n' "ID" "SERVICE" "SEVERITY" "RULE" "EXPECT"
printf '%s\n' "----------------------------------------------------------------------------------------------------"

while IFS='|' read -r id service severity rule metric value threshold offset status; do
  [[ -z "$id" ]] && continue
  started_at=$(python3 -c "
import time
deploy = time.time() - ${DEPLOY_AGO_MIN} * 60
started = deploy + ${offset} * 60
print(time.strftime('%Y-%m-%d %H:%M:%S', time.gmtime(started)))
")
  if [[ "$status" == "resolved" ]]; then
    ch "
      INSERT INTO problems (id, rule_id, rule_name, severity, service, metric,
                             value, threshold, status, description, started_at, resolved_at)
      VALUES ('$id', 'demo-rule-$id', '$rule', '$severity', '$service', '$metric',
              $value, $threshold, '$status', 'Seeded by seed-deployment-report-problems.sh',
              toDateTime64('$started_at', 9), now64(9))
    " > /dev/null
  else
    ch "
      INSERT INTO problems (id, rule_id, rule_name, severity, service, metric,
                             value, threshold, status, description, started_at)
      VALUES ('$id', 'demo-rule-$id', '$rule', '$severity', '$service', '$metric',
              $value, $threshold, '$status', 'Seeded by seed-deployment-report-problems.sh',
              toDateTime64('$started_at', 9))
    " > /dev/null
  fi

  expect="IN"
  [[ "$offset" -lt 0 ]] && expect="OUT (pre-deploy)"
  [[ "$status" == "resolved" ]] && expect="OUT (resolved)"
  printf '%-16s %-24s %-9s %-45s %6s\n' "$id" "$service" "$severity" "$rule" "$expect"
done <<< "$ROWS"

echo
echo "Seeded 14 problems (12 should appear in the report, 2 should not)."
echo
echo "Test it:"
echo "  UI:   http://localhost:8088/deployment-report  ->  'Deployment succeeded at' = $DEPLOY_LOCAL"
echo "  curl: curl -s \"http://localhost:8088/api/deployment-report?since=${DEPLOY_EPOCH_NS}&refresh=1\" \\"
echo "          -H \"Authorization: Bearer \$TOKEN\" | python3 -m json.tool"
echo
echo "Clean up afterwards:"
echo "  scripts/seed-deployment-report-problems.sh --clean"
