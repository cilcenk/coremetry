package chstore

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
)

// Distributed-ClickHouse helpers. The application code reads and
// writes to logical table names like `spans` or `logs`. In single-
// node mode those names are concrete MergeTree tables. In cluster
// mode they are `Distributed` tables that fan inserts and queries
// out to per-shard `<name>_local` `ReplicatedMergeTree` tables.
// Either way, the rest of the app uses the un-suffixed name.
//
// `clusterMode()` returns true when the operator has set
// clickhouse.cluster_name in config; everything else flows from
// that single switch.

// clusterMode reports whether Distributed-CH schema should be used.
func (s *Store) clusterMode() bool { return strings.TrimSpace(s.cfg.ClusterName) != "" }

// onCluster appends an `ON CLUSTER <name>` clause when in cluster
// mode, otherwise returns "". Insert it right after the table /
// view name in any DDL statement so the same definition flows to
// every node in the cluster atomically.
func (s *Store) onCluster() string {
	if !s.clusterMode() {
		return ""
	}
	return " ON CLUSTER `" + s.cfg.ClusterName + "`"
}

// engine returns the storage-engine clause for a high-volume table:
//
//	single-node: `<base>()`           (e.g. MergeTree())
//	cluster:     `Replicated<base>(zk_path, replica)`
//
// `name` is used as the trailing path component in ZK so each table
// gets its own coordination znode. The {shard} / {replica} macros
// are resolved by ClickHouse from the per-server `macros` config
// (operator must define those macros on each replica's <macros>
// section in config.xml).
func (s *Store) engine(base, name string) string {
	if !s.clusterMode() {
		return base + "()"
	}
	prefix := strings.TrimRight(s.cfg.ReplicaPath, "/")
	if prefix == "" {
		prefix = "/clickhouse/tables"
	}
	return fmt.Sprintf("Replicated%s('%s/{shard}/%s', '{replica}')",
		base, prefix, name)
}

// shardKey returns the SQL expression placed in `Distributed(...,
// shard_key)`. Default `rand()` distributes evenly with no
// locality; an operator can override to e.g. `cityHash64(trace_id)`
// so spans belonging to the same trace co-locate on one shard
// (faster `GROUP BY trace_id`, slightly less even row-per-shard).
//
// Deprecated: callers should use shardKeyFor(tableName) instead so
// trace-locality expressions don't get applied to tables that
// lack a `trace_id` column. Kept as a thin wrapper for any non-
// table-aware caller.
func (s *Store) shardKey() string {
	if k := strings.TrimSpace(s.cfg.ShardKey); k != "" {
		return k
	}
	return "rand()"
}

// tablesWithoutTraceID — high-volume tables + MVs whose CH
// schema does NOT project a `trace_id` column. When the
// operator configures COREMETRY_CH_SHARD_KEY with a
// trace_id-referencing expression (e.g. `cityHash64(trace_id)`),
// applying it uniformly to every Distributed wrapper errors
// with CH 47 (UNKNOWN_IDENTIFIER) on these — they get rand()
// instead.
//
// v0.5.425 — expanded to cover every MV that doesn't project
// trace_id. Operator-reported: v0.5.418 only listed the two
// raw tables (metric_points, profiles), missed every MV. Even
// with v0.5.422 fixing the defaultShardPolicy entries, the env
// override path bypassed those defaults and tried to apply
// `cityHash64(trace_id)` to trace_summary_1d / service_summary_5m
// / db_*_summary / topology_edges_5m, none of which project
// trace_id, → migration crash.
//
// trace_id IS projected by: spans, logs, trace_summary_5m.
// Every other high-volume / sharded MV omits it.
var tablesWithoutTraceID = map[string]bool{
	// Raw tables.
	"metric_points":        true,
	"profiles":             true,
	// MVs — none of these project trace_id in their SELECT.
	"trace_summary_1d":     true,  // only (day, trace_count_state)
	"service_summary_5m":   true,
	"db_summary_5m":        true,
	"db_caller_summary_5m": true,
	"topology_edges_5m":    true,
	"topology_op_edges_5m": true,
	// v0.5.435 — MVs/aggregates that join highVolumeTables in
	// this release. Same projection rule applies: none of them
	// project trace_id in their SELECT, so the env-override
	// shard-key path must fall back to rand() for these.
	// trace_service_index_5m is the only newly-sharded MV that
	// DOES project trace_id, so it's intentionally absent here.
	"operation_summary_5m":         true,
	"spanmetrics_calls_5m":         true,
	"spanmetrics_hist_5m":          true,
	"spanmetrics_duration_5m":      true,
	"messaging_summary_5m":         true,
	"messaging_caller_summary_5m":  true,
	"service_callers_5m":           true,
	"topology_root_flows_5m":       true,
}

// defaultShardPolicy — v0.5.419. Per-table shard expressions matching
// the Datadog / Honeycomb architectural pattern. Used when the
// operator hasn't set COREMETRY_CH_SHARD_KEY explicitly (env path
// remains an override that wins for every table — back-compat).
//
// Rationale per table:
//   - spans / logs / metric_points / profiles → service_name:
//     primary reads are service-filtered (/services, /service/X,
//     /endpoints, /topology). Co-locating by service means each
//     query hits ONE shard instead of fanning out to N.
//   - trace_summary_5m / trace_summary_1d → trace_id: trace
//     lookup table; primary read is `WHERE trace_id=X`. trace_id
//     locality means the lookup is a single-shard scan.
//   - service_summary_5m / db_summary_5m / db_caller_summary_5m
//     → service_name: aggregate per-service rollups, same logic
//     as the spans table.
//   - topology_edges_5m / topology_op_edges_5m → parent_service:
//     the read API filters by parent_service ("services I depend
//     on"); cluster-local locality preserves that.
//
// Tables not in this map fall back to rand() — even distribution,
// no locality. Safe default for tables where read patterns aren't
// dominantly filter-by-one-column.
//
// At billion-span scale + 1000+ services, the per-table policy
// compounds: every service-filtered query saves N-1 shard hits;
// every trace-id lookup saves N-1 shard hits. Network + CPU +
// coordinator-memory all drop proportionally.
var defaultShardPolicy = map[string]string{
	"spans":                "cityHash64(service_name)",
	"logs":                 "cityHash64(service_name)",
	"metric_points":        "cityHash64(service_name)",
	"profiles":             "cityHash64(service_name)",
	"trace_summary_5m":     "cityHash64(trace_id)",
	// v0.5.422 — trace_summary_1d MV columns are only `day` +
	// `trace_count_state` (HLL state); trace_id isn't projected.
	// Shard by day so read patterns (per-day uniqMerge) land
	// locally. Bucketing by day is also low-cardinality so this
	// is effectively a per-day write distribution.
	"trace_summary_1d":     "cityHash64(day)",
	"service_summary_5m":   "cityHash64(service_name)",
	"topology_edges_5m":    "cityHash64(parent_service)",
	"topology_op_edges_5m": "cityHash64(parent_service)",
	// v0.5.422 — db_summary_5m doesn't project service_name (it
	// aggregates per db_system / instance / db_name only —
	// caller-aware variant lives in db_caller_summary_5m). Shard
	// by db_system; ORDER BY already leads with db_system so
	// reads filtered by it land on one shard.
	"db_summary_5m":        "cityHash64(db_system)",
	"db_caller_summary_5m": "cityHash64(service_name)",
	// v0.5.435 — remaining sharded MVs/aggregates. For MVs the
	// shard key is largely decorative (auto-triggered writes land
	// local, reads always fan-out via Distributed) but is honest
	// about the dominant filter — picks the column the read path
	// or ORDER BY leads with. For service_callers_5m and
	// topology_root_flows_5m (regular tables INSERTed by the
	// topology aggregator) the shard key DOES matter — chosen for
	// locality with the upstream sharded source (spans on
	// cityHash64(service_name)).
	"trace_service_index_5m":        "cityHash64(service_name)",
	"operation_summary_5m":          "cityHash64(service_name)",
	"spanmetrics_calls_5m":          "cityHash64(service_name)",
	"spanmetrics_hist_5m":           "cityHash64(service_name)",
	"spanmetrics_duration_5m":       "cityHash64(service_name)",
	// messaging_*: destination is the highest-cardinality dim
	// (msg_system is ~5 values, cluster is ~10). Reads from
	// /messaging filter by (msg_system, destination) — both
	// land cleanly under destination-locality.
	"messaging_summary_5m":          "cityHash64(destination)",
	"messaging_caller_summary_5m":   "cityHash64(service_name)",
	"service_callers_5m":            "cityHash64(service)",
	"topology_root_flows_5m":        "cityHash64(root_service)",
}

// shardKeyFor returns the shard expression for a specific table.
// Resolution order (highest wins):
//
//  1. cfg.ShardKey env — when set, applies to ALL tables (legacy
//     uniform behaviour). Falls back to rand() for tables whose
//     schema doesn't carry the referenced column (currently the
//     trace_id-missing check, see tablesWithoutTraceID).
//
//  2. defaultShardPolicy[name] — Datadog-style per-table default.
//     Optimises for the dominant read pattern of each table.
//
//  3. rand() — last resort. Even distribution, no locality.
//
// v0.5.418 — initial bug-fix: fall back to rand() when the
// configured trace_id expression hits a table without that
// column.
// v0.5.419 — extended to per-table policy. Operator-asked: at
// billion-span scale + 1000+ services, the architectural
// optimisation is worth a one-time RESET_SCHEMA.
// ensureDistributedWrappers — v0.5.421. Boot-time reconciliation
// for the cluster-mode Distributed wrappers. Scans system.tables
// and, for every table that has a `<name>_local` flavour but is
// missing its bare `<name>` wrapper, emits the CREATE TABLE …
// ENGINE = Distributed(…) statement. Closes the failure mode
// where a prior mid-migration crash (network blip, ZK lag) left
// the local in place but the wrapper missing — every read
// against the bare name would otherwise 500 with UNKNOWN_TABLE
// until the operator manually rebuilt the wrapper.
//
// No-op in standalone mode (no wrappers in the model).
// Idempotent — uses CREATE TABLE IF NOT EXISTS so a healthy
// install does zero writes; each candidate table costs a single
// system.tables lookup.
func (s *Store) ensureDistributedWrappers(ctx context.Context) error {
	if !s.clusterMode() {
		return nil
	}
	// Set of tables that should have wrappers: highVolumeTables
	// (sharded base + MV) PLUS any extra entries in the
	// defaultShardPolicy map (covers trace_summary_* which is
	// MV-only and not in highVolumeTables).
	candidates := make(map[string]bool, len(highVolumeTables)+len(defaultShardPolicy))
	for n := range highVolumeTables {
		candidates[n] = true
	}
	for n := range defaultShardPolicy {
		candidates[n] = true
	}

	// Pull existing tables + engines in one round-trip. Engine is
	// needed for the v0.5.434 in-place migration path — we only
	// rename Replicated*/AggregatingMergeTree tables to `_local`,
	// never a Distributed table (a Distributed at the bare name
	// with no `_local` behind it is an orphan wrapper and gets
	// skipped + logged, not rewritten).
	existing := make(map[string]string) // name → engine
	rows, err := s.conn.Query(ctx, `
		SELECT name, engine FROM system.tables
		WHERE database = currentDatabase()`)
	if err != nil {
		return fmt.Errorf("list tables: %w", err)
	}
	for rows.Next() {
		var n, engine string
		if err := rows.Scan(&n, &engine); err != nil {
			rows.Close()
			return err
		}
		existing[n] = engine
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	wrappersCreated := 0
	wrappersMigrated := 0
	on := s.onCluster()
	for name := range candidates {
		localName := name + "_local"
		_, bareExists := existing[name]
		_, localExists := existing[localName]

		// Both halves present → wrapper is healthy, nothing to do.
		if bareExists && localExists {
			continue
		}
		// Neither exists → table not in this schema's version, skip.
		if !bareExists && !localExists {
			continue
		}

		// Local present, wrapper missing → original v0.5.421 repair.
		if localExists && !bareExists {
			stmt := fmt.Sprintf(
				"CREATE TABLE IF NOT EXISTS %s%s AS %s ENGINE = Distributed(`%s`, currentDatabase(), %s, %s)",
				name, on, localName, s.cfg.ClusterName, localName, s.shardKeyFor(name))
			if err := s.conn.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("repair wrapper %s: %w", name, err)
			}
			log.Printf("[chstore] reconcile: created missing Distributed wrapper for %s", name)
			wrappersCreated++
			continue
		}

		// v0.5.434 — bare exists, no _local. Three sub-cases:
		//
		//  1. bare is Distributed → orphan wrapper (no backing
		//     local on this database). Shouldn't happen normally;
		//     skip + warn so the operator can investigate via
		//     SHOW CREATE TABLE without us silently mutating it.
		//
		//  2. bare is Replicated* / AggregatingMergeTree etc. →
		//     this is the migration case the operator hits when
		//     a previously-bare table joins highVolumeTables in
		//     a new release (e.g. trace_summary_5m, db_summary_5m
		//     family in v0.5.426). Rename bare → _local and
		//     create the Distributed wrapper at the now-vacated
		//     bare name. Data is already shard-local under the
		//     ZK `{shard}` path macro, so the rename doesn't
		//     duplicate or move rows — the wrapper just enables
		//     cluster-wide reads via fan-out.
		//
		//  3. anything else (plain MergeTree → standalone install
		//     that mounted cluster-mode env vars without rerunning
		//     migrations) → skip; CREATE TABLE on the Distributed
		//     wrapper would error against the non-Replicated local.
		engine := existing[name]
		switch {
		case engine == "Distributed":
			log.Printf("[chstore] reconcile: %s exists as Distributed but no %s_local — orphan wrapper, skipping (operator: SHOW CREATE TABLE %s to investigate)",
				name, name, name)
			continue
		case !strings.HasPrefix(engine, "Replicated"):
			log.Printf("[chstore] reconcile: %s exists with engine=%s (not Replicated*) — skipping migration; this database isn't running cluster-mode schema",
				name, engine)
			continue
		}

		// Rename + wrap. Both run on the cluster so every replica
		// updates its metadata in lockstep.
		//
		// Boot-time race: two pods booting simultaneously both
		// detect bare-exists / no-_local and both issue RENAME.
		// The second loses to UNKNOWN_TABLE (CH code 60). Re-poll
		// system.tables after a rename failure: if a peer beat us
		// (bare gone, _local now present) treat as success and fall
		// through to the idempotent CREATE TABLE IF NOT EXISTS
		// wrap. Otherwise skip + log.
		renameStmt := fmt.Sprintf("RENAME TABLE %s TO %s%s", name, localName, on)
		if err := s.conn.Exec(ctx, renameStmt); err != nil {
			beatByPeer, perr := s.peerRenamedTable(ctx, name, localName)
			if perr != nil {
				log.Printf("[chstore] reconcile: rename %s → %s failed (%v) and post-check errored (%v) — skipping", name, localName, err, perr)
				continue
			}
			if !beatByPeer {
				log.Printf("[chstore] reconcile: rename %s → %s failed: %v", name, localName, err)
				continue
			}
			// Peer did the rename; fall through to wrap-create.
		}
		wrapStmt := fmt.Sprintf(
			"CREATE TABLE IF NOT EXISTS %s%s AS %s ENGINE = Distributed(`%s`, currentDatabase(), %s, %s)",
			name, on, localName, s.cfg.ClusterName, localName, s.shardKeyFor(name))
		if err := s.conn.Exec(ctx, wrapStmt); err != nil {
			// Half-migrated: rename succeeded, wrap didn't. Next
			// boot will see _local without bare and run the
			// original repair path — self-heals.
			log.Printf("[chstore] reconcile: wrap %s after rename failed (will retry next boot): %v", name, err)
			continue
		}
		log.Printf("[chstore] reconcile: migrated %s → %s_local + Distributed wrapper (engine was %s)",
			name, name, engine)
		wrappersMigrated++
	}
	if wrappersCreated > 0 || wrappersMigrated > 0 {
		log.Printf("[chstore] reconcile: %d wrapper(s) created, %d table(s) migrated to _local",
			wrappersCreated, wrappersMigrated)
	}
	return nil
}

// healBrokenReplicatedTables — v0.5.437. Recovers from the
// "table-exists-but-no-ZK-replica" state that the v0.5.349
// fallback-chain probe bug in v0.5.435 left behind on existing
// cluster installs.
//
// Symptom: a Replicated*MergeTree table is present in
// system.tables but absent from system.replicas — the metadata
// got created (CREATE TABLE succeeded locally) but the ZK
// replica znode (`/clickhouse/tables/<path>/replicas/<replica>`)
// either never registered or was wiped by a racing DROP+CREATE.
// Subsequent MV-trigger pushes to such a table fail with
// "Transaction failed (no node)". Under CH defaults an MV-push
// failure aborts the source INSERT too, so the whole ingest
// pipeline grinds to a halt — no new rows land in spans_local,
// every downstream MV stays empty for the recent window.
//
// Fix: DROP the broken table SYNC (waits for ZK cleanup), then
// let the subsequent migrate() pass's CREATE TABLE IF NOT EXISTS
// rebuild it from scratch with a fresh ZK registration.
//
// Safety: scoped to highVolumeTables `_local` names only. Admin
// data (users, alert_rules, system_settings, dashboards, …)
// lives in non-_local tables and is never touched by this path
// even if it somehow lands in broken state. The _local tables
// are MVs/aggregates that rebuild from upstream sources on next
// INSERT — same trade-off as the v0.5.349 fallback-chain
// migration's "past 5-min buckets will be dropped" note.
//
// Single-node mode is a no-op (no ZK to inspect, no _local
// tables in the schema).
func (s *Store) healBrokenReplicatedTables(ctx context.Context) error {
	if !s.clusterMode() {
		return nil
	}
	healCandidates := make(map[string]bool, len(highVolumeTables))
	for n := range highVolumeTables {
		healCandidates[n+"_local"] = true
	}

	// system.replicas lists tables where ZK registration completed.
	// A Replicated*MergeTree in system.tables that's missing from
	// system.replicas is in the broken state we want to heal.
	rows, err := s.conn.Query(ctx, `
		SELECT t.name, t.engine
		FROM system.tables AS t
		LEFT JOIN (
			SELECT table FROM system.replicas
			WHERE database = currentDatabase()
		) AS r ON r.table = t.name
		WHERE t.database = currentDatabase()
		  AND t.engine LIKE 'Replicated%'
		  AND r.table = ''
		SETTINGS join_use_nulls = 0`)
	if err != nil {
		return fmt.Errorf("scan for broken replicated tables: %w", err)
	}
	type broken struct{ name, engine string }
	var brokens []broken
	for rows.Next() {
		var b broken
		if err := rows.Scan(&b.name, &b.engine); err != nil {
			rows.Close()
			return err
		}
		brokens = append(brokens, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(brokens) == 0 {
		return nil
	}

	on := s.onCluster()
	healed := 0
	skipped := 0
	for _, b := range brokens {
		if !healCandidates[b.name] {
			// Admin-data preservation — log loudly so the operator
			// knows something's off, but don't auto-drop.
			log.Printf("[chstore] self-heal: skipping %s (engine=%s, not in HighVolumeTables._local set — admin data preserved; manual recovery required)", b.name, b.engine)
			skipped++
			continue
		}
		log.Printf("[chstore] self-heal: dropping broken %s (engine=%s, not registered in system.replicas) — migrate will rebuild on next pass", b.name, b.engine)
		stmt := "DROP TABLE IF EXISTS " + b.name + on + " SYNC"
		if err := s.conn.Exec(ctx, stmt); err != nil {
			// Don't fail boot — log + continue so other broken
			// tables can still be cleared. On next boot the still-
			// broken table will be re-detected.
			log.Printf("[chstore] self-heal: drop %s failed: %v", b.name, err)
			continue
		}
		healed++
	}
	log.Printf("[chstore] self-heal: %d table(s) healed, %d skipped (admin-data)", healed, skipped)
	return nil
}

// peerRenamedTable — v0.5.434. After a RENAME TABLE failure during
// boot-time reconciliation, check whether a peer pod beat us to it:
// the bare name should now be gone AND `<name>_local` should be
// present. Returns (true, nil) when that's the case so the caller
// can treat the rename as already-done and proceed to the idempotent
// wrap-create. Returns (false, nil) when the bare table is still
// there (the rename failed for some other reason — caller skips).
func (s *Store) peerRenamedTable(ctx context.Context, bareName, localName string) (bool, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT name FROM system.tables
		WHERE database = currentDatabase()
		  AND name IN (?, ?)`, bareName, localName)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return false, err
		}
		seen[n] = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return !seen[bareName] && seen[localName], nil
}

// ShardPolicy returns the resolved (table → expression) map for
// every table that gets a Distributed wrapper in cluster mode.
// Exposed for /admin/clickhouse so the operator can audit which
// shard expression each table actually got — saves them from
// `SHOW CREATE TABLE` round-trips in CH.
func (s *Store) ShardPolicy() map[string]string {
	out := map[string]string{}
	if !s.clusterMode() {
		return out
	}
	// Iterate every high-volume table + every MV variant we know
	// about so the resolved map matches what the migration loop
	// would write.
	for name := range highVolumeTables {
		out[name] = s.shardKeyFor(name)
	}
	for name := range defaultShardPolicy {
		// defaultShardPolicy covers MVs that aren't in
		// highVolumeTables (e.g. trace_summary_*); union them in.
		if _, ok := out[name]; !ok {
			out[name] = s.shardKeyFor(name)
		}
	}
	return out
}

func (s *Store) shardKeyFor(name string) string {
	if envExpr := strings.TrimSpace(s.cfg.ShardKey); envExpr != "" {
		// Env override — applies to all tables, with the
		// trace_id-missing safety fallback to keep the
		// migration from erroring out.
		if tablesWithoutTraceID[name] && strings.Contains(envExpr, "trace_id") {
			return "rand()"
		}
		return envExpr
	}
	if expr, ok := defaultShardPolicy[name]; ok {
		return expr
	}
	return "rand()"
}

// LocalTableName returns the `<name>_local` flavour when in
// cluster mode, or the bare name otherwise. Used inside
// Replicated*MergeTree CREATE statements; the `<name>` itself is
// reserved for the Distributed wrapper. Also exported so external
// callers (chmigrate, ad-hoc tools) can resolve the per-shard
// table name without re-implementing the suffix logic.
func (s *Store) LocalTableName(name string) string {
	if !s.clusterMode() {
		return name
	}
	return name + "_local"
}

// highVolumeTables are the tables sharded across the cluster — they
// get `_local` Replicated*MergeTree per node plus a Distributed
// wrapper at the unsuffixed name. Materialized-view bodies that
// SELECT from these names are rewritten to read the `_local`
// flavour on the same node, so each shard's MV stays local.
//
// Other tables (admin / config) are Replicated for consistency
// across replicas but not sharded; the application reads them by
// the bare name and CH ZK keeps the copies in sync.
var highVolumeTables = map[string]bool{
	// Base tables — sharded directly.
	"spans":         true,
	"logs":          true,
	"metric_points": true,
	"profiles":      true,
	// Materialized views feeding off the high-volume base tables —
	// each shard aggregates its own slice, the Distributed wrapper
	// fans queries out across shards and merges.
	"service_summary_5m": true,
	"trace_summary_1d":   true,
	// v0.5.426 — bug-fix: defaultShardPolicy was extended to these
	// in v0.5.419 but highVolumeTables wasn't, so adaptDDL never
	// renamed them to `_local` and the Distributed wrapper was
	// never emitted. ensureDistributedWrappers then skipped them
	// (it requires the `_local` flavour to exist before creating
	// the bare wrapper), leaving the operator with 6/11 wrappers
	// on a fresh cluster install. db_summary_5m / db_caller_summary_5m
	// / trace_summary_5m are MVs reading `FROM spans` — the MV
	// transform in adaptDDL rewrites that to `FROM spans_local`
	// so each shard's MV aggregates its own slice. topology_edges_5m
	// / topology_op_edges_5m are regular tables populated by the
	// batch correlator's INSERTs, which now route through the
	// Distributed wrapper and shard by parent_service.
	"trace_summary_5m":     true,
	"topology_edges_5m":    true,
	"topology_op_edges_5m": true,
	"db_summary_5m":        true,
	"db_caller_summary_5m": true,
	// v0.5.435 — remaining sharded MVs/aggregates the post-v0.5.426
	// /scale-audit revealed. Same gap shape: bare-name Replicated
	// per shard, no Distributed wrapper → cluster reads silently
	// returned one shard's view. Most critical is
	// trace_service_index_5m, which is Stage 1 of the MV fast-path
	// behind /traces — service-filtered listings were missing
	// every shard's trace_ids except the connected one. The
	// spanmetrics_* / messaging_* / operation_summary trio drove
	// the same per-shard incompleteness on /spanmetrics,
	// /messaging, and /service/X/operations. service_callers_5m
	// and topology_root_flows_5m are aggregator-INSERTed tables
	// where the INSERT-via-wrapper distribution + read fan-out
	// both matter.
	"trace_service_index_5m":        true,
	"operation_summary_5m":          true,
	"spanmetrics_calls_5m":          true,
	"spanmetrics_hist_5m":           true,
	"spanmetrics_duration_5m":       true,
	"messaging_summary_5m":          true,
	"messaging_caller_summary_5m":   true,
	"service_callers_5m":            true,
	"topology_root_flows_5m":        true,
}

// adaptDDL rewrites a DDL statement for the current mode.
//
// Single-node mode: returns the input unchanged in a 1-element
// slice. Existing behaviour is preserved exactly.
//
// Cluster mode: returns 1 or 2 statements:
//   - For an admin table: the original CREATE/ALTER with
//     ON CLUSTER injected and the engine swapped to its
//     `Replicated*` counterpart (e.g. ReplacingMergeTree →
//     ReplicatedReplacingMergeTree).
//   - For a high-volume table: the same transform, but the
//     primary table is renamed to `<name>_local`; a second
//     statement is emitted creating `<name>` as a Distributed
//     wrapper over the per-shard locals.
//   - For a materialized view feeding a high-volume table:
//     the view itself is renamed to `<name>_local`, its FROM
//     clause is rewritten to the matching `_local`, the engine
//     becomes Replicated, and a Distributed wrapper is emitted
//     so callers can still read from the un-suffixed name.
//
// The transformer is regex-based against the SQL strings we
// control in store.go — every production CREATE TABLE in this
// codebase uses one of:
//
//	ENGINE = MergeTree()
//	ENGINE = ReplacingMergeTree(version)
//	ENGINE = AggregatingMergeTree           (no parens)
//
// so the regex never sees user input.
func (s *Store) adaptDDL(sql string) []string {
	if !s.clusterMode() {
		return []string{sql}
	}

	on := s.onCluster()
	zkPrefix := strings.TrimRight(s.cfg.ReplicaPath, "/")
	if zkPrefix == "" {
		zkPrefix = "/clickhouse/tables"
	}

	// Find the table or MV name being created / altered.
	name, kind := identifyDDLTarget(sql)
	if name == "" {
		// Unknown shape — pass through untouched. Safe fallback;
		// the worst case is the statement runs on the connected
		// node only, same as single-node behaviour.
		return []string{sql}
	}
	hv := highVolumeTables[name]
	target := name
	if hv {
		target = name + "_local"
	}

	rewritten := sql

	// 1. Inject ON CLUSTER + (for high-volume) rename to _local.
	switch kind {
	case "table":
		rewritten = reCreateTable.ReplaceAllString(rewritten,
			"${1}"+target+on+" ")
	case "altertable":
		rewritten = reAlterTable.ReplaceAllString(rewritten,
			"${1}"+target+on+" ")
	case "mv":
		// MV: rewrite the view name AND the FROM <high-vol>
		// references in the SELECT body so each shard's MV
		// reads from its own local Replicated source.
		rewritten = reCreateMV.ReplaceAllString(rewritten,
			"${1}"+target+on+" ")
		for src := range highVolumeTables {
			pat := regexp.MustCompile(`(?i)\bFROM\s+` + regexp.QuoteMeta(src) + `\b`)
			rewritten = pat.ReplaceAllString(rewritten, "FROM "+src+"_local")
		}
	}

	// 2. Swap MergeTree-family engines to their Replicated
	// counterparts. Only on CREATE statements (ALTER doesn't
	// have an ENGINE clause).
	if kind == "table" || kind == "mv" {
		rewritten = reEngine.ReplaceAllStringFunc(rewritten, func(m string) string {
			parts := reEngine.FindStringSubmatch(m)
			base := parts[1]
			argList := strings.TrimSpace(parts[2]) // includes parens or empty
			zkPath := fmt.Sprintf("'%s/{shard}/%s', '{replica}'", zkPrefix, name)
			// Splice the ZK args in front of any existing args.
			var newArgs string
			switch {
			case argList == "" || argList == "()":
				newArgs = "(" + zkPath + ")"
			default:
				inside := strings.TrimSuffix(strings.TrimPrefix(argList, "("), ")")
				inside = strings.TrimSpace(inside)
				if inside == "" {
					newArgs = "(" + zkPath + ")"
				} else {
					newArgs = "(" + zkPath + ", " + inside + ")"
				}
			}
			return "ENGINE = Replicated" + base + newArgs
		})
	}

	out := []string{rewritten}

	// 3. For high-volume CREATE TABLE / CREATE MATERIALIZED VIEW,
	// emit the Distributed wrapper at the un-suffixed name.
	if hv && (kind == "table" || kind == "mv") {
		wrapper := fmt.Sprintf(
			"CREATE TABLE IF NOT EXISTS %s%s AS %s ENGINE = Distributed(`%s`, currentDatabase(), %s, %s)",
			name, on, target, s.cfg.ClusterName, target, s.shardKeyFor(name))
		out = append(out, wrapper)
	}

	// 4. v0.5.362 — for a high-volume ALTER that changes the
	// column list, the rewritten step 1 above only updates the
	// `_local` ReplicatedMergeTree. The Distributed wrapper
	// keeps its own column list and stays behind; INSERT/SELECT
	// against the bare name then errors with "no such column …".
	// ClickHouse's Distributed engine accepts ADD COLUMN /
	// DROP COLUMN / MODIFY COLUMN, so emit the same ALTER
	// against the un-suffixed name when one of those clauses
	// is present. Other ALTERs (ADD INDEX, MODIFY TTL,
	// MATERIALIZE) Distributed legitimately doesn't carry —
	// skip the wrapper for those.
	if hv && kind == "altertable" && reAlterColList.MatchString(sql) {
		bareAlter := reAlterTable.ReplaceAllString(sql, "${1}"+name+on+" ")
		out = append(out, bareAlter)
	}
	return out
}

// execDDL adapts a DDL statement for cluster mode and runs every
// resulting fragment in order. In single-node mode this is a thin
// wrapper around conn.Exec. Errors include the (truncated) SQL of
// the failing fragment so a regex-induced bug isn't a silent
// production-only failure.
//
// v0.5.384 — cluster-mode retry on transient READONLY (CH error
// 242). After RESET_SCHEMA's DROP DATABASE ON CLUSTER, the
// follow-up CREATE TABLE can land while ZooKeeper still has
// half-cleaned znodes from the dropped Replicated*MergeTree
// engines. CH then reports the new table as "in read only mode"
// until ZK converges (typically <30s). Without retry, boot
// crashes. With retry, the migration self-heals.
func (s *Store) execDDL(ctx context.Context, sql string) error {
	for _, frag := range s.adaptDDL(sql) {
		if err := s.execWithReadonlyRetry(ctx, frag); err != nil {
			return fmt.Errorf("ddl exec: %w\nSQL: %.200s", err, frag)
		}
	}
	return nil
}

// execWithReadonlyRetry runs one DDL fragment, retrying on the
// CH "Table is in read only mode" (code 242) signature with
// exponential backoff. Single-node mode never hits this path —
// the error only happens on Replicated*MergeTree engines while
// ZK catches up post-DROP.
func (s *Store) execWithReadonlyRetry(ctx context.Context, frag string) error {
	const maxAttempts = 6
	backoff := 2 * time.Second
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		err := s.conn.Exec(ctx, frag)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isReadonlyTransient(err) {
			return err
		}
		// Honour ctx cancellation between sleeps.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return lastErr
}

// isReadonlyTransient matches the CH READONLY (code 242) error
// signature. The text is stable across 22.x → 24.x.
func isReadonlyTransient(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Table is in read only mode") ||
		strings.Contains(msg, "code: 242")
}

// isClusterUnsupportedAlter recognises the ClickHouse error
// signature for an ALTER that the Distributed engine refuses to
// proxy. Callers softening this into a warning need a precise
// match so we don't accidentally swallow unrelated DDL failures
// (column type mismatch, permission denial, etc.).
//
// CH reports it as code 48 "NOT_IMPLEMENTED" with the literal
// "is not supported by storage Distributed" tail — that string
// is stable across at least 22.x → 24.x server versions.
func isClusterUnsupportedAlter(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// CH error 48 — ADD_INDEX / DROP_INDEX etc. ("... is not supported by
	// storage Distributed"). CH error 36 — MODIFY TTL ("Engine Distributed
	// doesn't support TTL clause"). Both fire when Coremetry points at an
	// EXTERNAL Distributed `spans` but chstore.cluster_name is unset, so
	// adaptDDL can't rewrite the ALTER to <table>_local ON CLUSTER. These
	// are storage-engine limitations on the Distributed wrapper, not real
	// failures — the caller skips + logs and the operator applies them on
	// the per-shard local tables (or sets cluster_name).
	return strings.Contains(msg, "is not supported by storage Distributed") ||
		strings.Contains(msg, "Distributed doesn't support TTL")
}

// identifyDDLTarget pulls the target table/view name out of a DDL
// string. Returns (name, "table"|"altertable"|"mv") on a match,
// ("", "") otherwise so the caller can pass through untouched.
func identifyDDLTarget(sql string) (string, string) {
	if m := reCreateTable.FindStringSubmatch(sql); m != nil {
		return m[2], "table"
	}
	if m := reAlterTable.FindStringSubmatch(sql); m != nil {
		return m[2], "altertable"
	}
	if m := reCreateMV.FindStringSubmatch(sql); m != nil {
		return m[2], "mv"
	}
	return "", ""
}

var (
	reCreateTable = regexp.MustCompile(`(?is)^(\s*CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+)([A-Za-z_][A-Za-z0-9_]*)\b`)
	reAlterTable  = regexp.MustCompile(`(?is)^(\s*ALTER\s+TABLE\s+)([A-Za-z_][A-Za-z0-9_]*)\b`)
	reCreateMV    = regexp.MustCompile(`(?is)^(\s*CREATE\s+MATERIALIZED\s+VIEW\s+IF\s+NOT\s+EXISTS\s+)([A-Za-z_][A-Za-z0-9_]*)\b`)
	// Engine clause: ENGINE = <BaseEngine>(args?)  — args optional.
	// Captures the base engine name and the argument list including
	// surrounding parens (or empty when the engine is parenless).
	reEngine = regexp.MustCompile(`(?i)ENGINE\s*=\s*(MergeTree|ReplacingMergeTree|AggregatingMergeTree|SummingMergeTree)\s*(\([^)]*\))?`)

	// reAlterColList detects whether an ALTER touches the column
	// list (ADD/DROP/MODIFY/RENAME COLUMN). The Distributed engine
	// supports these — index / TTL / MATERIALIZE alterations are
	// out of scope and stay scoped to the _local table.
	reAlterColList = regexp.MustCompile(`(?i)\b(?:ADD|DROP|MODIFY|RENAME)\s+COLUMN\b`)
)
