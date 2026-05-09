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

Coremetry's auto-migration only creates tables that don't already
exist. To upgrade from a populated single-node instance to
cluster mode:

1. Spin up the new CH cluster + Coremetry pointed at it (with
   `cluster_name` set). Tables come up empty as Distributed.
2. Use `INSERT INTO new.spans_local SELECT * FROM old.spans` per
   shard from a one-shot job, or `clickhouse-copier` for the bulk
   transfer.
3. Cut traffic over by repointing the OTel collectors at the new
   coremetry instance.

Live in-place migration (without downtime) requires running both
clusters dual-writing for a window — out of scope here.

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
