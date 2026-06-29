package chstore

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/cilcenk/coremetry/internal/config"
)

// ResetSchema drops the configured ClickHouse database in its
// entirety so the next chstore.New() boot rebuilds it from the
// migration sequence. Designed for the helm pre-install /
// pre-upgrade hook in destructive-reset deployments — operators
// who want "deploy fresh, even though I'm pointing at an
// existing external CH" get a clean slate without manually
// running DROP statements.
//
// Cluster-aware: when cfg.ClusterName is set, the DROP includes
// ON CLUSTER so every replica drops in lock-step. The SYNC
// modifier waits for the server-side detach to complete so the
// follow-up CREATE DATABASE in chstore.New() doesn't race a
// still-pending background drop.
//
// Idempotent — `IF EXISTS` means re-running on an already-
// dropped namespace is a no-op, not a failure. That matters for
// helm hooks that may fire on every upgrade.
//
// SAFETY: this deletes EVERY table the app has ever created,
// including audit logs, dashboards, anomaly history, retention
// overrides — basically the whole product state. The wrapper
// flag in helm makes this opt-in and warns loudly. Do not call
// this from any normal startup path.
func ResetSchema(ctx context.Context, cfg config.CHConfig) error {
	hosts := cfg.Hosts()
	if len(hosts) == 0 {
		return fmt.Errorf("clickhouse addr is required")
	}

	dialTimeout := 5 * time.Second
	if d, err := time.ParseDuration(cfg.DialTimeout); err == nil && d > 0 {
		dialTimeout = d
	}

	var tlsCfg *clickhouse.Options
	_ = tlsCfg // placeholder so the diff stays focused; TLS is set below

	// Connect to the `default` database (we cannot connect to a
	// database we are about to drop). Same retry envelope as
	// chstore.New() so a freshly-spun-up CH StatefulSet can come
	// online while the helm hook is starting.
	open := func() (clickhouse.Conn, error) {
		c, err := clickhouse.Open(&clickhouse.Options{
			Addr: hosts,
			Auth: clickhouse.Auth{
				Database: "default",
				Username: cfg.Username,
				Password: cfg.Password,
			},
			DialTimeout: dialTimeout,
		})
		if err != nil {
			return nil, err
		}
		if err := c.Ping(ctx); err != nil {
			c.Close()
			return nil, err
		}
		return c, nil
	}

	const attempts = 24
	var conn clickhouse.Conn
	var lastErr error
	for i := 0; i < attempts; i++ {
		if c, err := open(); err == nil {
			conn = c
			lastErr = nil
			break
		} else {
			lastErr = err
			log.Printf("[reset-schema] waiting for ClickHouse at %s (%d/%d): %v", cfg.Addr, i+1, attempts, err)
			time.Sleep(5 * time.Second)
		}
	}
	if lastErr != nil {
		return fmt.Errorf("connect after retries: %w", lastErr)
	}
	defer conn.Close()

	onCluster := ""
	if name := strings.TrimSpace(cfg.ClusterName); name != "" {
		onCluster = " ON CLUSTER `" + name + "`"
	}
	// v0.5.382 — operator-reported: 171 GB partition tripped CH's
	// `max_table_size_to_drop` guard (default 50 GB) on
	// COREMETRY_CH_RESET_SCHEMA=1 boots after long-running
	// installs. The guard exists to protect against an
	// accidental DROP TABLE; on an explicit RESET path we
	// intentionally want everything gone. Override both
	// max_table_size_to_drop AND max_partition_size_to_drop
	// to 0 (CH-speak for "no upper bound") so the DROP
	// proceeds regardless of accumulated volume.
	// SYNC waits for the detach so the follow-up CREATE
	// DATABASE in chstore.New() doesn't race a pending drop.
	stmt := fmt.Sprintf(
		"DROP DATABASE IF EXISTS `%s`%s SYNC "+
			"SETTINGS max_table_size_to_drop = 0, "+
			"max_partition_size_to_drop = 0",
		cfg.Database, onCluster)
	log.Printf("[reset-schema] %s", stmt)
	if err := conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	log.Printf("[reset-schema] database %q dropped — next boot will recreate the schema", cfg.Database)

	// v0.8.208 — on a Distributed cluster, DROP DATABASE only cleans the
	// ZooKeeper znodes of Replicated tables that still exist LOCALLY on the
	// node it runs through. A node whose local replica was already gone (a
	// partial earlier reset, a removed/re-added node, an aborted boot) leaves
	// an ORPHAN znode at <replica_path>/<shard>/<table> carrying the old table
	// metadata. The next fresh CREATE TABLE then trips
	// METADATA_MISMATCH (code 342) — e.g. the stored skip-index set differs —
	// and boot crashes. Operator-reported on an external 4-node cluster.
	// Sweep those orphan znodes so "reset = truly from scratch".
	if name := strings.TrimSpace(cfg.ClusterName); name != "" {
		cleanOrphanReplicatedZnodes(ctx, conn, cfg)
	}
	return nil
}

// cleanOrphanReplicatedZnodes removes leftover ZooKeeper znodes for Coremetry's
// sharded Replicated tables after a database drop, so a fresh schema create
// can't hit METADATA_MISMATCH against stale metadata. Best-effort: a cluster
// may run with system.zookeeper access disabled, or a path may already be
// clean — every failure is logged and skipped, never fatal (the reset already
// dropped the data; this is belt-and-suspenders).
//
// SAFETY (v0.8.222): the znode path is <replica_path>/<shard>/<table> with NO
// database/uuid component, and the sweep matches purely by table NAME. On the
// SHARED default prefix "/clickhouse/tables" a co-tenant (another Coremetry DB
// or a foreign app) whose Replicated table happens to share a name — spans,
// logs, … — would have its LIVE replica dropped. We can't tell those apart by
// name, so the sweep runs ONLY when the operator set a DEDICATED replica_path
// (not the shared default). On the default we skip + tell them to set
// COREMETRY_CH_REPLICA_PATH (e.g. /clickhouse/tables/coremetry) — which they
// should anyway, since two apps can't share a bare table-name path without
// colliding.
// zkSweepEnabled reports whether the orphan-znode sweep may run for a given
// replica_path. It refuses the SHARED default (empty or "/clickhouse/tables")
// where a name-only match could DROP REPLICA a co-tenant's table. Pure so the
// safety gate is unit-tested. v0.8.222.
func zkSweepEnabled(replicaPath string) bool {
	p := strings.TrimRight(replicaPath, "/")
	return p != "" && p != "/clickhouse/tables"
}

func cleanOrphanReplicatedZnodes(ctx context.Context, conn clickhouse.Conn, cfg config.CHConfig) {
	if !zkSweepEnabled(cfg.ReplicaPath) {
		log.Printf("[reset-schema] zk orphan sweep SKIPPED — replica_path is the shared default " +
			"\"/clickhouse/tables\". On a shared/external cluster, sweeping it by table-name could " +
			"DROP REPLICA a co-tenant's live table. Set COREMETRY_CH_REPLICA_PATH to a dedicated prefix " +
			"(e.g. /clickhouse/tables/coremetry) to enable safe orphan cleanup.")
		return
	}
	zkPrefix := strings.TrimRight(cfg.ReplicaPath, "/")

	// children of the prefix = per-shard dirs ({shard} macro values, e.g. 01/02)
	shards, err := zkChildren(ctx, conn, zkPrefix)
	if err != nil {
		log.Printf("[reset-schema] zk orphan sweep: cannot list %q (%v) — skipping (data already dropped)", zkPrefix, err)
		return
	}
	dropped := 0
	for _, shard := range shards {
		shardPath := zkPrefix + "/" + shard
		tables, err := zkChildren(ctx, conn, shardPath)
		if err != nil {
			continue
		}
		for _, tbl := range tables {
			if !highVolumeTables[tbl] {
				continue // only Coremetry's sharded Replicated tables
			}
			tablePath := shardPath + "/" + tbl
			replicas, err := zkChildren(ctx, conn, tablePath+"/replicas")
			if err != nil || len(replicas) == 0 {
				continue
			}
			for _, rep := range replicas {
				// FROM ZKPATH targets the znode directly, so it works even
				// when no local table references it (the orphan case).
				stmt := dropReplicaZkStmt(rep, tablePath)
				if err := conn.Exec(ctx, stmt); err != nil {
					log.Printf("[reset-schema] zk orphan sweep: %s failed: %v", stmt, err)
					continue
				}
				dropped++
			}
			log.Printf("[reset-schema] zk orphan sweep: cleared stale replicas of %s", tablePath)
		}
	}
	log.Printf("[reset-schema] zk orphan sweep complete — %d stale replica znode(s) removed under %s", dropped, zkPrefix)
}

// dropReplicaZkStmt builds the SYSTEM DROP REPLICA … FROM ZKPATH statement that
// removes one replica's metadata from a Replicated table's ZooKeeper path. The
// ZKPATH form is what makes orphan cleanup possible — it targets the znode
// directly, with no local table required. Single quotes in the replica name are
// escaped so a hostile/odd macro value can't break out of the literal.
func dropReplicaZkStmt(replica, tablePath string) string {
	return fmt.Sprintf("SYSTEM DROP REPLICA '%s' FROM ZKPATH '%s'",
		strings.ReplaceAll(replica, "'", "''"), tablePath)
}

// zkChildren lists the immediate child node names of a ZooKeeper path via
// system.zookeeper. Returns an error if the path doesn't exist (ZNONODE) so the
// caller can skip — that's the normal "already clean" signal.
func zkChildren(ctx context.Context, conn clickhouse.Conn, path string) ([]string, error) {
	rows, err := conn.Query(ctx, "SELECT name FROM system.zookeeper WHERE path = ?", path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
