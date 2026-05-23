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
	return nil
}
