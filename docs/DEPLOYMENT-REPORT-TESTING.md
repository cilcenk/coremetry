# Testing the Deployment Analysis Report locally

How to exercise `/deployment-report` end-to-end on your machine — seed a
fake "deploy", make something break after it, confirm the report picks
it up, confirm it drops off once resolved, and confirm the AI review
button works. Every step below was actually run against a local
ClickHouse + Redis while building this feature; the curl/SQL commands
are copy-pasteable as-is.

## 1. Start the local stack

```bash
docker compose up -d clickhouse redis
go run . 
```

Wait for the boot banner in the logs:

```
┌──────────────────────────────────────────────┐
│         Coremetry APM    — ready (all)        │
│  Web UI + REST API:   http://localhost:8088   │
└──────────────────────────────────────────────┘
```

Confirm `/api/health` returns `"clickhouse":"ok"`:

```bash
curl -s http://localhost:8088/api/health | head -c 200
```

Log in as the bootstrap admin (default `admin@coremetry.local` /
`admin` unless you set `COREMETRY_INITIAL_ADMIN`/`COREMETRY_INITIAL_PASSWORD`)
at `http://localhost:8088/login`, or grab a token for curl testing:

```bash
TOKEN=$(curl -s -X POST http://localhost:8088/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@coremetry.local","password":"admin"}' \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["token"])')
```

## 2. Pick a fake deploy timestamp

Use "30 minutes ago" as your fake deploy time — this doc's SQL below
seeds a problem that started at exactly that point.

```bash
SINCE_NS=$(( $(date +%s) * 1000000000 - 3600000000000 ))   # 1h ago — pick this in the UI's datetime field
```

The report's `since` param is **unix nanoseconds**, not milliseconds —
if you're calling the API directly rather than through the UI's
datetime picker, don't forget the `000000000` suffix.

## 3. Seed an open problem that starts after your deploy

Fastest path — insert directly into ClickHouse's `problems` table
(skips waiting on an alert rule to actually fire):

```bash
docker exec coremetry-clickhouse clickhouse-client --database=coremetry --query "
INSERT INTO problems (id, rule_id, rule_name, severity, service, metric,
                       value, threshold, status, description, started_at)
VALUES ('test-p1', 'rule-error-rate', 'High error rate', 'critical',
        'checkout-service', 'error_rate', 12.5, 5.0, 'open',
        'Error rate spiked after deploy',
        toDateTime64('$(date -u -v-30M +"%Y-%m-%d %H:%M:%S")', 9))
"
```

(On Linux, swap `date -u -v-30M` for `date -u -d '30 minutes ago'`.)

This is 30 minutes ago — inside the 1h `SINCE_NS` window from step 2, so
it should qualify.

Alternative, more realistic path: temporarily lower an alert rule's
threshold in Settings → Alert rules (or use the demo generator's
incident-injection mode, if built — check `go run ./cmd/demo --help`)
and wait for the evaluator to open a real Problem from live traffic.
The SQL insert above is faster for repeatable testing of the report
itself.

**Want more variety in one shot?**
`scripts/seed-deployment-report-problems.sh` seeds 14 problems across
12 services, mixed severities/metrics/priorities, plus one pre-deploy
problem and one resolved-post-deploy problem (both of which should be
EXCLUDED from the report) — good for eyeballing a report with real
shape instead of a single row:

```bash
scripts/seed-deployment-report-problems.sh
# ... prints the exact "since" timestamp to paste into the UI ...
scripts/seed-deployment-report-problems.sh --clean   # tear down afterwards
```

## 4. Confirm the endpoint picks it up

```bash
curl -s "http://localhost:8088/api/deployment-report?since=$SINCE_NS&refresh=1" \
  -H "Authorization: Bearer $TOKEN" | python3 -m json.tool
```

**Expected:** one entry under `services`, `service: "checkout-service"`,
`health: "red"` (critical open problem), the seeded problem under
`problems` with `priority: "P1"` (critical + ≥2x threshold breach).
`anomalies`/`newErrors` are empty/null unless you also did step 6.
`before`/`after` RED stats will be all-zero unless `checkout-service`
has real span traffic in `service_summary_5m` for those windows — that's
expected on a fresh install with no demo traffic.

The `&refresh=1` bypasses the 30s response cache — use it while
iterating so you're not staring at a stale cached miss.

## 5. Confirm it in the UI

Navigate to `/deployment-report` (sidebar → Triage group, or ⌘K →
"Deployment Report"). In "Deployment succeeded at", pick the date/time
from 1 hour ago (your local timezone — the picker converts to the ns
timestamp the backend expects). Click **Generate report**.

**Expected:**
- "Affected services" shows `checkout-service` with a red health badge.
- "Problems since deploy" shows the seeded problem — severity
  `critical`, priority `P1`, rule `High error rate`.
- Clicking **AI review** on that row calls the existing Copilot explain
  endpoint and renders a response inline (skip this check if Copilot
  isn't configured on your local install — `COREMETRY_AI_API_KEY`
  empty — the button self-hides when disabled).
- "Anomalies since deploy" / "New errors since deploy" show empty
  states unless you also did step 6.

## 6. (Optional) Also trigger an anomaly and a new error

- **Anomaly**: cause a latency or error-rate spike on an operation via
  demo traffic (`go run ./cmd/demo`); `/anomalies` should show it as
  `active` within its detector's poll window (a few minutes).
- **New error**: emit an exception with a stack trace/type never seen
  before on `checkout-service`. `/problems` (Exception inbox section)
  should show it with state `new`.

Re-generate the report (same `since`) — both should now appear under
`checkout-service`'s sections.

## 7. Confirm the owner/SRE team filter

The report has the same `?owner=`/`?sre=` team filter as the Problems
inbox — narrows the (already-computed) qualifying service set to
services owned by / on-call'd by a given team, resolved from the
operator-curated `service_metadata` catalog.

Assign a team to one of your seeded services (skip if it already has
one from real catalog data):

```bash
docker exec coremetry-clickhouse clickhouse-client --database=coremetry --query "
INSERT INTO service_metadata (service, owner_team, sre_team)
VALUES ('checkout-service', 'avengers', 'avengers-sre')
"
```

```bash
echo "=== ownerTeam=avengers (expect checkout-service) ==="
curl -s "http://localhost:8088/api/deployment-report?since=$SINCE_NS&ownerTeam=avengers&refresh=1" \
  -H "Authorization: Bearer $TOKEN" | python3 -c "import sys,json; print([s['service'] for s in json.load(sys.stdin)['services']])"

echo "=== ownerTeam=Avengers, different casing (expect same result) ==="
curl -s "http://localhost:8088/api/deployment-report?since=$SINCE_NS&ownerTeam=Avengers&refresh=1" \
  -H "Authorization: Bearer $TOKEN" | python3 -c "import sys,json; print([s['service'] for s in json.load(sys.stdin)['services']])"

echo "=== ownerTeam=some-other-team (expect empty) ==="
curl -s "http://localhost:8088/api/deployment-report?since=$SINCE_NS&ownerTeam=some-other-team&refresh=1" \
  -H "Authorization: Bearer $TOKEN" | python3 -c "import sys,json; print(json.load(sys.stdin)['services'])"
```

**Expected:** the first two calls return `["checkout-service"]` (team
match is case-insensitive), the third returns `[]` — a team with no
matching qualifying service is an empty report, never an unfiltered
one. In the UI, the "All owner teams"/"All SRE teams" dropdowns next
to Generate report populate from the same catalog and re-filter
immediately on pick (no need to click Generate again — only the
deploy timestamp itself requires that).

Clean up the catalog row afterwards:

```bash
docker exec coremetry-clickhouse clickhouse-client --database=coremetry --query \
  "ALTER TABLE service_metadata DELETE WHERE service = 'checkout-service'"
```

## 8. Confirm the inclusion gate — negative case (since too late)

Pick a `since` **after** the problem's `started_at` (e.g. 5 minutes
ago, when the problem started 30 minutes ago):

```bash
SINCE_NS=$(( $(date +%s) * 1000000000 - 300000000000 ))   # 5 min ago
curl -s "http://localhost:8088/api/deployment-report?since=$SINCE_NS&refresh=1" \
  -H "Authorization: Bearer $TOKEN"
```

**Expected:** `"services":[]` — the problem started before this later
`since`, so it no longer qualifies. This confirms the `StartedAt >=
since` filter is doing real work, not just returning everything.

## 9. Confirm the "still exists" gate — negative case (resolved)

Resolve the test problem (upsert with `status='resolved'` — ReplacingMergeTree,
same `id`, higher version wins on the next `FINAL` read):

```bash
docker exec coremetry-clickhouse clickhouse-client --database=coremetry --query "
INSERT INTO problems (id, rule_id, rule_name, severity, service, metric,
                       value, threshold, status, description, started_at, resolved_at)
VALUES ('test-p1', 'rule-error-rate', 'High error rate', 'critical',
        'checkout-service', 'error_rate', 12.5, 5.0, 'resolved',
        'Error rate spiked after deploy',
        toDateTime64('$(date -u -v-30M +"%Y-%m-%d %H:%M:%S")', 9), now64(9))
"
```

Re-run the original 1h-ago `since` query. **Expected:** `"services":[]`
again — the service drops out of the report entirely once its problem
resolves, even though it started after the deploy. This confirms
resolved problems don't keep a service qualifying.

## 10. Confirm no polling / no auto-refresh

Leave the report open (with a still-open problem) and watch the
browser Network tab. **Expected:** one request to
`/api/deployment-report` on Generate, then nothing — unlike the live
dashboards, this is a point-in-time snapshot, not a poller.

## 11. Clean up your test data

```bash
docker exec coremetry-clickhouse clickhouse-client --database=coremetry --query \
  "ALTER TABLE problems DELETE WHERE id = 'test-p1'"
```

(`ALTER ... DELETE` is a mutation — it applies asynchronously; give it
a few seconds before re-querying if you want to confirm it's gone.)
