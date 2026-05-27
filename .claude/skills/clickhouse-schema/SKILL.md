---
name: clickhouse-schema
description: Coremetry-specific ClickHouse guardrails — engine choice, MV bypass invariant, ORDER BY/PARTITION rules, async_insert tuning, migration checklist, anti-patterns. Use BEFORE any change that touches internal/chstore/*.go, an SQL string in internal/api/*.go, an evaluator/anomaly/templater query, a CH migration, or a new aggregate read. Triggers on words like "CH schema", "materialized view", "MV", "ALTER TABLE", "ORDER BY", "ReplacingMergeTree", "AggregatingMergeTree", "spans table", "metric_points", "service_summary_5m", or any DDL/DML against a Coremetry CH table.
---

# /clickhouse-schema — Coremetry CH guardrails

Coremetry runs at billions of spans / day, thousands of services,
tens of thousands of operations. Generic ClickHouse advice often
misleads here: at this scale, "just query the spans table" is a
bug, "ALTER UPDATE" is a tombstone, and "Nullable everywhere"
costs disk + decode. This skill captures the Coremetry-specific
rules that aren't on clickhouse.com.

**Read this before any change that adds, modifies, or reads from
a CH table.**

## When to use

- Adding or altering a chstore table (`internal/chstore/store.go`,
  `internal/chmigrate/*.go`)
- Writing a new SQL query in `internal/api/*.go`,
  `internal/evaluator/*.go`, `internal/anomaly/*.go`,
  `internal/topology/*.go`, `internal/templater/*.go`
- Reviewing a `/scale-audit` finding that touched CH
- Adding a new materialized view or aggregate
- Investigating "the query is slow at scale" reports
- Any change that needs the `make audit` CHECK 5 / CHECK 6 lints
  to pass

## Steps

### 1. Engine choice — three categories, three answers

| Data shape | Engine | Examples |
|---|---|---|
| Append-only, billion+ rows, time-bounded | `MergeTree()` + `PARTITION BY toDate(time)` + TTL | `spans`, `logs`, `metric_points`, `profiles` |
| Mutable state, version-tracked, low volume | `ReplacingMergeTree(version)` | `alert_rules`, `problems`, `system_settings`, `dashboards`, `events`, `anomaly_events`, `saved_views` |
| Pre-aggregated rollup over append-only source | `AggregatingMergeTree` + materialized view | `service_summary_5m`, `operation_summary_5m`, `topology_edges_5m`, `topology_root_flows_5m`, `db_summary_5m`, `db_caller_summary_5m` |

**Decision flow:**

- Is the row immutable once written? → `MergeTree()` raw table
- Will the row be updated at the source (operator edits, version
  bump, status flip)? → `ReplacingMergeTree(version)` with
  `version UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))`. Reads
  use `FINAL`.
- Is the query a fixed aggregate shape over an append-only source?
  → MV with `AggregatingMergeTree` + `countState() / sumState() /
  quantilesState(...)`. Reads use `*Merge()` finalisers.

**Don't reach for:**
- `CollapsingMergeTree` — never used in Coremetry; ReplacingMergeTree
  covers our state model.
- `SummingMergeTree` — superseded by `AggregatingMergeTree` for our use.
- `Memory` / `Set` / `Log` — never fit an APM workload at scale.

### 2. MV bypass invariant — the single most important rule

**Per CLAUDE.md "Architectural invariants" #3:** every hot read
endpoint that touches CH MUST use the MV when one exists. Reading
raw `spans` / `logs` / `metric_points` for an aggregate query at
billion-row scale is a bug.

The catalog:

| Read pattern | Use MV | Source table |
|---|---|---|
| Per-service RED metrics over a window | `service_summary_5m` | spans |
| Per-(service, operation) RED | `operation_summary_5m` | spans |
| Service-to-service edges | `topology_edges_5m` | spans |
| Per-(operation, root) flow attribution | `topology_root_flows_5m` | spans |
| DB call summary per service | `db_summary_5m` | spans (with db_system != '') |
| DB callers grouped | `db_caller_summary_5m` | spans (with db_system != '') |
| Service callers (one hop in) | `service_callers_5m` | spans |

**The window is 5min by construction.** If your alert / analytic
needs sub-5min granularity, fall back to raw spans with bounds
(see step 4). For windows ≥ 5min, the MV is mandatory.

**v0.6.12 incident:** the alert evaluator's `measure()` /
`measureCount()` were hitting raw spans every minute for every
service under every rule. Refactored to use service_summary_5m for
windows ≥ 5min via the `useSummaryMV(window)` helper at
`internal/evaluator/evaluator.go`. Anywhere else that runs a
service-scoped RED query on a tick: do the same.

**Read pattern with MV:**
```sql
-- RIGHT — read the MV
SELECT countMerge(span_count_state) AS spans,
       countMerge(error_count_state) AS errs,
       sumMerge(duration_sum_state) / 1e6 AS sum_ms,
       arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
FROM service_summary_5m
WHERE service_name = ? AND time_bucket >= ?
SETTINGS max_execution_time = 10

-- WRONG — raw spans aggregation
SELECT count(), countIf(status_code='error'),
       avg(duration)/1e6, quantile(0.99)(duration)/1e6
FROM spans WHERE service_name = ? AND time >= ?
```

### 3. ORDER BY rule — filters first, then cardinality

Two principles compete in ClickHouse docs:
- "Order columns low-to-high cardinality" (textbook)
- "Prioritize frequently filtered columns" (also textbook)

**For Coremetry, prioritise filters.** Service-scoped queries
dominate every read path. Putting `service_name` first lets CH
prune partitions before scanning, which dwarfs any benefit from
strict cardinality ordering.

Current ORDER BYs that are CORRECT:

```
spans         ORDER BY (service_name, time)
logs          ORDER BY (service_name, severity_num, time)
metric_points ORDER BY (service_name, metric, time)
```

- `service_name` (LowCardinality, ~thousands) is first → service
  filters get tight prefix pruning.
- `severity_num` (UInt8, ~25 values) is second on logs → errors-
  only queries get a second prefix prune within the service.
- `time` (DateTime64, very high cardinality) is last → the natural
  index column for time-bounded windows.

**For state tables (ReplacingMergeTree), ORDER BY = the dedup key
exclusively.** Don't add columns to ORDER BY hoping for better
filter pruning — you'll silently break dedup.

```sql
-- RIGHT — dedup by id, period
ENGINE = ReplacingMergeTree(version)
ORDER BY id

-- WRONG — looks helpful for "filter by service", actually breaks dedup
-- because (id, service) tuples that vary in either column are no
-- longer deduplicated
ENGINE = ReplacingMergeTree(version)
ORDER BY (id, service)
```

**For MV target tables**, the ORDER BY mirrors the GROUP BY in the
MV definition, which in turn drives the read path's WHERE
prefix.

### 4. Raw spans queries — required bounds

Per CLAUDE.md "Hard constraints", every query that touches
`spans` / `logs` / `metric_points` MUST have:

1. **`LIMIT`** — pick a sane number (1000 for ad-hoc, 100 for
   visualisation, 1 for "does at least one exist" checks).
2. **`SETTINGS max_execution_time = N`** — wall-clock cap. 10s
   default for evaluator/anomaly; 30s for ad-hoc explorations; 60s
   only for explicit backfills.
3. **`WHERE` on an indexed prefix** — at minimum `time >= ?`.
   Better: `service_name = ? AND time >= ?` which matches the
   ORDER BY prefix and prunes partitions.

`make audit` CHECK 6 catches naked `FROM spans` without nearby
LIMIT / max_execution_time. Anything that's flagged needs to gain
bounds OR move to an MV.

### 5. PARTITION BY — daily for raw, monthly for low-volume state

- Raw billion-row tables (spans / logs / metric_points / profiles):
  `PARTITION BY toDate(time)`. 30-day TTL × daily = 30 active
  partitions. Stays under the recommended 100-1000 partition count.
- Low-volume state with long retention (`anomaly_events`,
  `service_metadata` history, `audit_events`):
  `PARTITION BY toYYYYMM(...)`. Months are coarse enough that the
  partition count stays bounded; daily would create dozens of
  near-empty partitions.
- TTL boundary aligned to partition boundary. The
  `StartRetentionEnforcer` runs `DROP PARTITION` every 1h instead
  of waiting for CH's merge-based TTL — gives immediate disk
  reclaim (v0.5.320).

**Anti-pattern:** Partitioning by anything that isn't time-aligned
or that has unbounded cardinality. Per-service partitioning on
10k+ services would create 10k+ partitions per day — CH metadata
would melt.

### 6. async_insert config — DO NOT TUNE

v0.5.346 tuned these to known-good values. The wrapper at
`internal/chstore/repo.go::asyncInsertCtx` injects them on every
ingest INSERT:

```go
"async_insert":                  1,
"wait_for_async_insert":         1,
"async_insert_max_data_size":    10_485_760,   // 10MB coalesce
"async_insert_busy_timeout_ms":  1000,         // 1s coalesce window
"async_insert_stale_timeout_ms": 1000,
```

`wait=1` keeps client-side error detection synchronous. Don't
remove this or you'll lose insert errors silently. 10MB coalesce
+ 1s busy timeout is the trade-off for billion-spans/day burst
handling — don't lower without an operator-reported issue + a
plan.

### 7. ReplacingMergeTree version semantics

Every state table uses:

```sql
version UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
```

And **reads use `FINAL`** so the latest version wins on a single
query (don't wait for background merges):

```sql
SELECT ... FROM alert_rules FINAL WHERE id = ?
```

The OPTIMIZE FINAL trap: NEVER schedule `OPTIMIZE … FINAL`
periodically — it does a full table rewrite on every call. CH
will merge in the background; the version column ensures
correctness in the meantime. Reads pay the cost (FINAL = sort-
merge at read time) but the volume is small (hundreds to
thousands of rows on state tables).

### 8. LowCardinality — yes on strings with < 10k distinct

Used aggressively in Coremetry — saves disk + speeds up
GROUP BY:

- ✓ `service_name`, `kind`, `status_code`, `http_method`,
  `db_system`, `rpc_system`, `host_name`, `deploy_env`,
  `severity_text`, `peer_service`, `scope_name`, `metric`,
  `unit`, `instrument`, `name` (operation name)

NOT for:
- ✗ `trace_id`, `span_id`, `parent_id` — high-cardinality
  UUIDs; LowCardinality dict overhead exceeds value
- ✗ `db_statement`, `status_msg`, `body` — free-text strings
- ✗ Float values, durations — wrong category entirely

### 9. Migration checklist (chmigrate + store.go)

When adding or altering a table:

- [ ] `CREATE TABLE IF NOT EXISTS` — every CREATE is idempotent
- [ ] `ALTER TABLE … ADD COLUMN IF NOT EXISTS` — every ALTER is idempotent
- [ ] If altering ORDER BY: you can't. Period. Plan a new table,
  backfill, swap.
- [ ] If adding a column with a default that depends on existing data:
  use a CTE-based backfill query separate from the ALTER. CH won't
  back-populate a `DEFAULT` based on row content.
- [ ] If adding an MV column that's an AggregateFunction state: the
  MV must be DROPPED + RECREATED (and prior buckets reset). See
  `store.go` line ~1670 for the `service_summary_5m` apdex addition
  template — it's a destructive but bounded migration.
- [ ] If adding a skipping index: it only applies to NEW parts.
  Old data scans unindexed until merges rewrite them. Force-merge
  is heavy; usually just wait it out.
- [ ] Test with `chstore.New()` against an empty CH AND against
  a populated one — migrations must be safe both ways.

### 10. Distributed mode considerations

When `cfg.ClusterName != ""`, Coremetry runs against a Distributed
table wrapper + per-shard `*_local` Replicated tables. Schema
authors must:

- Add `ON CLUSTER ?` to CREATE / ALTER on distributed setups.
  `chmigrate` handles this via the cluster wrapper helpers; new
  ad-hoc DDL outside chmigrate must pick it up.
- Use `GLOBAL IN (SELECT ...)` for subqueries — non-GLOBAL `IN
  (SELECT)` runs the inner SELECT per shard, which is silently
  wrong. `make audit` CHECK 5 catches this.
- Use the right LeaderHolder lock when running per-shard
  aggregation — multiple shards racing on the same write target
  causes ReplacingMergeTree drift.

## Anti-patterns

- **Reading raw spans for an aggregate that an MV already
  pre-computes.** The v0.6.12 evaluator bug. Always check the MV
  catalog (step 2) first.
- **`OPTIMIZE TABLE … FINAL` in app code.** Rewrites whole table;
  no useful read-side improvement.
- **`ALTER UPDATE`** for anything that isn't a one-off admin
  correction. Use ReplacingMergeTree(version) + insert a newer
  row.
- **`ALTER DELETE`** for high-frequency state churn. Use a
  tombstone column (`deleted UInt8`) with ReplacingMergeTree
  dedup. /admin/clickhouse's mutations panel (v0.6.22) surfaces
  this when it slips through.
- **Nullable types.** Use a sentinel DEFAULT (`''` for strings,
  `0` for numbers). Nullable adds a per-row bit + decode overhead
  + breaks vectorised paths.
- **`String` where a typed column fits.** UInt16 for http_status,
  Float64 for duration, DateTime64(9) for time. Saves disk +
  speeds up filter evaluation.
- **`quantile()` past ~1M rows.** Use `quantileTDigest` —
  ≤2% error, vastly less memory. The MVs already use
  `quantilesState(...)` which is even better for the merge path.
- **JOIN spans on a hot path.** Always pre-aggregate via MV (the
  v0.5.108 incident). The few JOINs in `topology.go` /
  `backtrace.go` are background or ad-hoc — they should not move
  to per-request hot paths.
- **Forgetting `FINAL` on a ReplacingMergeTree read.** Silently
  reads pre-merge rows; the operator sees a row that was
  "deleted" 30 seconds ago. Always `FINAL`.

## Hard-constraint reminder

From CLAUDE.md, repeated here because they MUST be checked on
every change:

| Constraint | Check |
|---|---|
| ClickHouse query on spans / logs / metric_points | LIMIT + max_execution_time + indexed-time-bounded WHERE |
| Cache key hashes ALL inputs | If you cache the result, sorted+FNV the inputs (no length-only digests) |
| MV bypass | Hot read endpoints MUST use the matching MV |
| Audit entry on every mutation | `s.audit(r, "kind.action", "resource", id, details)` for any state write |
| `make audit` clean | Run before tagging — it gates on 7 CH-shaped checks (cache key, polling, eager picker, copilot wrapper, GLOBAL IN, raw spans bounds, dup routes) |

## Historical incidents — read these before guessing

- **v0.5.108** — topology JOIN spans self-join on hot read. Moved
  to a 5min MV (`topology_edges_5m`). Lesson: JOIN spans at scale =
  bug.
- **v0.5.184** — `timeRangeToNs(range)` in JSX kept ticking `now()`
  every render → infinite refetch. CH side: looked like sustained
  high RPS on /api/services. Frontend bug, CH symptom.
- **v0.5.187** — cache key collapsed a Set to `len=N`, two
  different sets with same length cross-poisoned. Lesson: stable
  digest (sorted + FNV) on every input.
- **v0.5.320** — TTL-based delete was up to 4h late; added
  `StartRetentionEnforcer` for proactive `DROP PARTITION` per hour.
- **v0.5.341** — retention enforcer was racing across replicas
  (CH serialised but log noise + metadata lock fight). Now Redis-
  gated leader-elect.
- **v0.5.346** — async_insert tuning to current values.
- **v0.6.11** — L2 cache `cacheInvalidatePrefix` had a SCAN whose
  result was discarded — never deleted. Added `DelPrefix` on the
  Cache interface; the fakeCache regression test pins the contract.
- **v0.6.12** — evaluator's `measure()` / `measureCount()` were
  hitting raw spans every minute. Refactored to `service_summary_5m`
  for windows ≥ 5min via `useSummaryMV(window)`.

When in doubt, grep `CLAUDE.md` for the version tag in the failure
mode. The decision log at the bottom captures what we tried and
why we landed where we did.

## Don't

- **Don't add a new CH table for what's already in `saved_views`.**
  Per-user state goes into `saved_views(page='<kind>', id,
  owner_id, query_string, ...)`. Adding a new schema per surface
  is the recurring scope creep that `make audit` doesn't catch
  but CLAUDE.md invariant #5 does.
- **Don't add a new MV without checking the catalog.** Six MVs
  exist. If the read can be served from one, use it. If it
  genuinely needs a new aggregate shape, document the why in the
  CREATE comment (look at `service_summary_5m` line ~1200 for the
  template).
- **Don't bypass `make audit` because the finding is "obviously
  fine".** That's the v0.5.187 path — the engineer KNEW the cache
  key was technically lossy but figured the collision space was
  small. It wasn't. Audit-verify-context: read ±10 lines, then
  fix or `.auditignore` with a comment.
