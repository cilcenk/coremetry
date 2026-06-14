# ClickHouse Cluster Mode

Coremetry runs against a single ClickHouse node by default. For
multi-billion-spans/day workloads it can run against a sharded /
replicated cluster with **zero code changes** — just a config knob.

## When to use it

| Scale (spans/day) | Setup |
|---|---|
| < 1B | Single CH node with the ingest defaults from v0.2.59 (8 workers, 500k buffer, async_insert). |
| 1-5B | Single beefy CH node, NVMe storage. Already battle-tested at this band. |
| 5-20B | 2-shard cluster with replication-2. Coremetry cluster mode. |
| 20B+  | 4+ shard cluster, replication-2 or 3 per shard. |

Cluster mode also unlocks **HA**: replicated tables survive a node loss.
That's a separate reason to enable it even at moderate scale.

## Memory sizing — the MV aggregation working set

The binding constraint per CH node is **RAM, not disk or row count**.
Coremetry feeds spans into ~12 materialized views that build
`quantilesState` / `argMaxState` aggregate states *per insert block*
(`service_summary_5m`, `operation_summary_5m`, `spanmetrics_{1s,10s,1m}`,
`topology_*`, `db_*`, `trace_summary_*`). That aggregation working set —
plus async_insert buffers and the background merges of those state parts —
is what consumes memory under sustained ingest.

Empirically (2026-06-14 local ingest-load test, single node): ~233–413
spans/s (10–17× a quiet baseline) pinned a 3 GiB node at its ceiling; the
node held (ClickHouse self-limits + async_insert buffering — it did **not**
OOM-crash and recorded no `MEMORY_LIMIT_EXCEEDED`), but had near-zero
headroom. Read-path memory is separately bounded (MV reads are flat — see
[[project-scale-test-result]]). **Budget RAM per node for the *insert* MV
working set, not the query path.** When one node's ingest working set
approaches its limit, scale **horizontally** (add a shard) rather than only
vertically — each shard's MVs aggregate only its slice of the spans, so the
working set distributes. More bundled-single-node RAM only moves the knee.

## How Coremetry adapts the schema

When `clickhouse.cluster_name` is set, every DDL Coremetry runs is
rewritten:

- **High-volume tables** (`spans`, `logs`, `metric_points`, `profiles`)
  become `<name>_local` `ReplicatedMergeTree` per shard, with a
  `Distributed` wrapper at the un-suffixed name. Application code
  reads/writes the un-suffixed name; CH handles the fan-out.

- **Materialized views** feeding the high-volume tables
  (`service_summary_5m`, `trace_summary_1d`) follow the same pattern.
  Each shard's MV reads from its local source and merges via the
  Distributed wrapper at query time.

- **Admin tables** (`users`, `alert_rules`, `dashboards`, …) become
  Replicated but not sharded — they're small, and replication keeps
  every Coremetry instance seeing the same control-plane state.

- All `CREATE` / `ALTER` statements gain `ON CLUSTER <name>` so
  schema changes propagate atomically.

The application code reads/writes the same logical names in both
modes. Single-node operation is preserved exactly when
`cluster_name` is empty.

## Operator setup

### 1. Deploy a CH cluster + ZooKeeper / Keeper

A minimum 2-shard, 2-replica setup needs 4 CH nodes plus a 3-node
ZK or Keeper quorum. Use the official `clickhouse-keeper` (built-in)
to avoid running ZK separately.

### 2. Define the cluster on every CH node

In each CH node's `config.xml` (or via override):

```xml
<remote_servers>
  <coremetry_cluster>
    <shard>
      <internal_replication>true</internal_replication>
      <replica><host>ch-1</host><port>9000</port></replica>
      <replica><host>ch-2</host><port>9000</port></replica>
    </shard>
    <shard>
      <internal_replication>true</internal_replication>
      <replica><host>ch-3</host><port>9000</port></replica>
      <replica><host>ch-4</host><port>9000</port></replica>
    </shard>
  </coremetry_cluster>
</remote_servers>
```

`internal_replication=true` is critical: it tells the Distributed
table to write to ONE replica per shard and let
`ReplicatedMergeTree` handle replica-to-replica sync via ZK. Without
it you'd write the same row twice.

### 3. Define `{shard}` / `{replica}` macros per node

Each CH node needs its own `macros` config so the Replicated
engine's `'/path/{shard}/{replica}'` argument resolves to a unique
ZK znode:

```xml
<!-- on ch-1 -->
<macros>
  <shard>01</shard>
  <replica>ch-1</replica>
</macros>

<!-- on ch-2 -->
<macros>
  <shard>01</shard>
  <replica>ch-2</replica>
</macros>

<!-- on ch-3 -->
<macros>
  <shard>02</shard>
  <replica>ch-3</replica>
</macros>
<!-- ch-4: shard=02, replica=ch-4 -->
```

### 4. Point Coremetry at any one CH host

```yaml
clickhouse:
  addr: "ch-1:9000"
  database: "coremetry"
  username: "default"

  # Turn on distributed schema. Below this, no app-level changes.
  cluster_name: "coremetry_cluster"
  replica_path: "/clickhouse/tables"  # default
  shard_key: "rand()"                  # default
```

`addr` can be comma-separated for HA — Coremetry will round-robin
across the listed hosts.

### 5. (Optional) Co-locate spans of one trace on one shard

The default `shard_key: "rand()"` distributes rows uniformly. If
your hot queries do per-trace joins, override:

```yaml
clickhouse:
  shard_key: "cityHash64(trace_id)"
```

Now every span of a trace lands on the same shard — the trace
detail page reads from one node instead of fanning out.
Trade-off: row-per-shard is less even (a hot trace can grow).

## Migrating an existing single-node install

Coremetry ships a one-shot migration mode that copies historical
data from a populated single-node CH into the new cluster's
Distributed tables, day by day. Idempotent — re-runs skip
already-finished partitions automatically.

### Runbook (zero-downtime cutover)

**1. Stand up the new cluster + Coremetry**

Bring up the new CH cluster (4-node sharded as above) plus a
fresh Coremetry instance pointed at it with `cluster_name` set.
Tables come up empty as `Distributed` over `*_local`.

**2. Dual-write at the OTel collector layer**

Add a second OTLP exporter on every collector so live traffic
fans out to both the old single-node Coremetry and the new
cluster. This is a safe no-rollback step — old keeps serving,
new starts collecting.

```yaml
# OTel collector config snippet
exporters:
  otlp/old:
    endpoint: old-coremetry:4317
  otlp/new:
    endpoint: new-coremetry:4317
service:
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [otlp/old, otlp/new]
```

**3. Backfill historical data**

On the new Coremetry, run the one-shot migration:

```bash
coremetry --config /path/to/config.yaml \
  --migrate-from old-ch:9000 \
  --migrate-db   coremetry \
  --migrate-user default \
  --migrate-pass <old-ch-password> \
  --migrate-tables spans,logs,metric_points,profiles \
  --migrate-days 30
```

When `--migrate-from` is set, Coremetry runs migration mode and
exits — it does NOT start the web server. Output:

```
[migrate] 4 table(s) × 30 day(s) = 120 operations
[migrate] spans: 2026-04-09..2026-05-09
[migrate] spans 2026-04-09: copied 1837412 rows
[migrate] spans 2026-04-10: copied 1923105 rows
...
[migrate] done — destination ready, you can now cut traffic over
```

The bulk copy runs server-side via ClickHouse's `remote()` table
function — data moves directly between CH instances; the
Coremetry process only issues the SQL statement.

If the run is interrupted, just re-run with the same flags. Each
day starts with a count-comparison; matching counts are skipped
without re-copying.

**4. Verify counts match**

```sql
-- On the new cluster, against the Distributed wrapper:
SELECT toDate(time) AS day, count() AS rows
FROM coremetry.spans
WHERE time >= today() - 30
GROUP BY day ORDER BY day DESC LIMIT 10;

-- Compare against the old single-node:
-- (run the same query on old-ch, expect identical row counts)
```

**5. Cut traffic over**

Drop the `otlp/old` exporter from every collector — fresh
ingest now lands only on the new cluster. The old Coremetry can
sit idle for a grace period before being torn down.

### Schema migrations as an init-container

For multi-replica deployments, having every web pod's
`chstore.New()` race to apply schema changes is risky on
cluster setups — concurrent `ON CLUSTER … ReplicatedMergeTree`
DDL hits ZooKeeper / Keeper at the same time and one or more
replicas can fail with `znode already exists` or partial
schema state.

Coremetry exposes a one-shot mode for exactly this case:

```bash
coremetry --migrate-only
```

`--migrate-only` runs `chstore.New()` (which creates / alters
every table + MV via the `ON CLUSTER` path), then exits 0.
Designed for:

- **Kubernetes initContainer**: blocks the web pod from
  starting until the migration completes once.
- **Pre-deploy Job**: standalone Job in your CI/CD that
  succeeds before the rolling update begins.

Example k8s pattern:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: coremetry
spec:
  template:
    spec:
      initContainers:
        - name: migrate
          image: coremetry:v0.2.68
          args: ["--migrate-only"]
          envFrom:
            - configMapRef: { name: coremetry-config }
            - secretRef:    { name: coremetry-secrets }
      containers:
        - name: coremetry
          image: coremetry:v0.2.68
          # …web container starts only after migrate exits 0
```

The web pods that follow STILL run their own `chstore.New()`
on startup — the migrations are idempotent
(`CREATE TABLE IF NOT EXISTS`, `ADD COLUMN IF NOT EXISTS`,
`ADD INDEX IF NOT EXISTS`), so concurrent no-ops are safe.
The init-container path just guarantees the *first* run
happens once, single-writer.

### Coexistence rules

- During the dual-write window, both Coremetry instances see the
  same live spans but the new cluster also sees migrated history.
- `cluster_name` only affects schema creation; it does NOT
  change query behaviour. The new Coremetry queries Distributed
  tables transparently while the old one queries MergeTree.
- Cache (Redis) is keyed by request shape, not data shape, so
  there's no migration concern on that side.

## Verifying it's working

After Coremetry's first run against the cluster:

```sql
-- Check the schema actually came up everywhere:
SELECT host_name, table FROM system.tables
WHERE database = 'coremetry' AND table = 'spans_local';
-- Should return one row per replica node.

-- Check the Distributed wrapper:
SELECT engine_full FROM system.tables
WHERE database = 'coremetry' AND table = 'spans';
-- Should be: Distributed('coremetry_cluster', 'coremetry', 'spans_local', rand())

-- Inserts should go through the wrapper:
SELECT count() FROM spans WHERE time >= now() - INTERVAL 1 MINUTE;
-- Returns the merged count across all shards.
```
