package chstore

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/cilcenk/coremetry/internal/config"
)

type Store struct {
	conn driver.Conn
	cfg  config.CHConfig
	ret  config.RetentionConfig
}

func New(cfg config.CHConfig, ret config.RetentionConfig) (*Store, error) {
	dialTimeout, _ := time.ParseDuration(cfg.DialTimeout)
	if dialTimeout == 0 {
		dialTimeout = 5 * time.Second
	}
	maxConns := cfg.MaxOpenConns
	if maxConns == 0 {
		maxConns = 10
	}

	ctx := context.Background()

	// Create database using default DB connection. CH may still be coming
	// up (Helm-managed StatefulSet, fresh container, etc.) so retry the
	// initial CREATE DATABASE for up to ~2 minutes before giving up.
	var setup driver.Conn
	openSetup := func() error {
		c, err := clickhouse.Open(&clickhouse.Options{
			Addr:        []string{cfg.Addr},
			Auth:        clickhouse.Auth{Database: "default", Username: cfg.Username, Password: cfg.Password},
			DialTimeout: dialTimeout,
		})
		if err != nil {
			return err
		}
		if err := c.Ping(ctx); err != nil {
			c.Close()
			return err
		}
		setup = c
		return nil
	}
	{
		const attempts = 24
		var lastErr error
		for i := 0; i < attempts; i++ {
			if err := openSetup(); err == nil {
				lastErr = nil
				break
			} else {
				lastErr = err
				log.Printf("[chstore] waiting for ClickHouse at %s (%d/%d): %v", cfg.Addr, i+1, attempts, err)
				time.Sleep(5 * time.Second)
			}
		}
		if lastErr != nil {
			return nil, fmt.Errorf("setup connect after retries: %w", lastErr)
		}
	}
	if err := setup.Exec(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", cfg.Database)); err != nil {
		setup.Close()
		return nil, fmt.Errorf("create database: %w", err)
	}
	setup.Close()

	// Connect to target database
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{cfg.Addr},
		Auth: clickhouse.Auth{Database: cfg.Database, Username: cfg.Username, Password: cfg.Password},
		Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
		DialTimeout:     dialTimeout,
		MaxOpenConns:    maxConns,
		MaxIdleConns:    maxConns / 2,
		ConnMaxLifetime: time.Hour,
		Settings:        clickhouse.Settings{"max_execution_time": 60},
	})
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}

	s := &Store{conn: conn, cfg: cfg, ret: ret}
	if err := s.migrate(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.conn.Close() }
func (s *Store) Conn() driver.Conn { return s.conn }

func (s *Store) migrate(ctx context.Context) error {
	sd, ld, md := s.ret.SpansDays, s.ret.LogsDays, s.ret.MetricsDays
	if sd == 0 { sd = 30 }
	if ld == 0 { ld = 30 }
	if md == 0 { md = 7 }

	tables := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS spans (
			trace_id      String       CODEC(ZSTD(3)),
			span_id       String       CODEC(ZSTD(3)),
			parent_id     String       DEFAULT '' CODEC(ZSTD(3)),
			name          LowCardinality(String),
			kind          LowCardinality(String) DEFAULT 'internal',
			service_name  LowCardinality(String),
			host_name     LowCardinality(String) DEFAULT '',
			deploy_env    LowCardinality(String) DEFAULT '',
			status_code   LowCardinality(String) DEFAULT 'unset',
			status_msg    String       DEFAULT '',
			time          DateTime64(9) CODEC(Delta, ZSTD(3)),
			duration      Int64        CODEC(T64, ZSTD(3)),
			db_system     LowCardinality(String) DEFAULT '',
			db_statement  String       DEFAULT '',
			http_method   LowCardinality(String) DEFAULT '',
			http_route    LowCardinality(String) DEFAULT '',
			http_status   UInt16       DEFAULT 0,
			rpc_system    LowCardinality(String) DEFAULT '',
			rpc_method    LowCardinality(String) DEFAULT '',
			peer_service  LowCardinality(String) DEFAULT '',
			msg_system    LowCardinality(String) DEFAULT '',
			attr_keys     Array(LowCardinality(String)),
			attr_values   Array(String),
			res_keys      Array(LowCardinality(String)),
			res_values    Array(String),
			events        String       DEFAULT '[]',
			scope_name    LowCardinality(String) DEFAULT '',
			INDEX idx_trace  trace_id  TYPE bloom_filter(0.01) GRANULARITY 4,
			INDEX idx_name   name      TYPE set(0) GRANULARITY 4
		) ENGINE = MergeTree()
		PARTITION BY toDate(time)
		ORDER BY (service_name, time)
		TTL toDate(time) + INTERVAL %d DAY
		SETTINGS index_granularity = 8192`, sd),

		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS logs (
			trace_id      String       DEFAULT '',
			span_id       String       DEFAULT '',
			time          DateTime64(9) CODEC(Delta, ZSTD(3)),
			severity_num  UInt8        DEFAULT 0,
			severity_text LowCardinality(String) DEFAULT '',
			body          String       CODEC(ZSTD(3)),
			service_name  LowCardinality(String),
			host_name     LowCardinality(String) DEFAULT '',
			attr_keys     Array(LowCardinality(String)),
			attr_values   Array(String),
			res_keys      Array(LowCardinality(String)),
			res_values    Array(String),
			scope_name    LowCardinality(String) DEFAULT '',
			INDEX idx_body body TYPE tokenbf_v1(32768, 3, 0) GRANULARITY 4
		) ENGINE = MergeTree()
		PARTITION BY toDate(time)
		ORDER BY (service_name, severity_num, time)
		TTL toDate(time) + INTERVAL %d DAY
		SETTINGS index_granularity = 8192`, ld),

		`CREATE TABLE IF NOT EXISTS users (
			id            String,
			email         String,
			password_hash String,
			role          LowCardinality(String) DEFAULT 'viewer',  -- admin | viewer
			disabled      UInt8        DEFAULT 0,
			auth_provider LowCardinality(String) DEFAULT 'local',   -- local | oidc
			created_at    DateTime64(9) DEFAULT now64(9),
			version       UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY id`,

		`CREATE TABLE IF NOT EXISTS system_settings (
			key        String,
			value      String,                          -- JSON-encoded
			updated_at DateTime64(9) DEFAULT now64(9),
			version    UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY key`,

		`CREATE TABLE IF NOT EXISTS notification_channels (
			id         String,
			name       String,
			type       LowCardinality(String),          -- email | slack | webhook
			config     String,                           -- JSON, schema depends on type
			enabled    UInt8 DEFAULT 1,
			min_severity LowCardinality(String) DEFAULT 'warning',  -- info | warning | critical
			created_at DateTime64(9) DEFAULT now64(9),
			version    UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY id`,

		`CREATE TABLE IF NOT EXISTS exception_groups (
			fingerprint    String,                              -- sha1(type|message|service)
			ex_type        String,
			ex_message     String,
			service        LowCardinality(String),
			state          LowCardinality(String) DEFAULT 'new', -- new | acknowledged | resolved | ignored
			assignee       String       DEFAULT '',              -- user id, or '' for unassigned
			first_seen     DateTime64(9),
			last_seen      DateTime64(9),
			resolved_at    Nullable(DateTime64(9)),
			occurrences    UInt64       DEFAULT 0,
			notes          String       DEFAULT '',
			version        UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY fingerprint`,

		`CREATE TABLE IF NOT EXISTS slos (
			id            String,
			name          String,
			service       LowCardinality(String),
			sli_type      LowCardinality(String),  -- availability | latency
			target        Float64,                  -- e.g. 0.99 for 99%
			window_days   UInt16  DEFAULT 30,
			threshold_ms  Float64 DEFAULT 0,        -- only used by latency SLIs
			operation     String  DEFAULT '',       -- optional span name filter
			created_at    DateTime64(9) DEFAULT now64(9),
			version       UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY id`,

		`CREATE TABLE IF NOT EXISTS dashboards (
			id           String,
			name         String,
			description  String       DEFAULT '',
			panels       String       DEFAULT '[]' CODEC(ZSTD(3)),
			created_at   DateTime64(9) DEFAULT now64(9),
			updated_at   DateTime64(9) DEFAULT now64(9),
			version      UInt64       DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY id`,

		`CREATE TABLE IF NOT EXISTS alert_rules (
			id           String,
			name         String,
			service      String       DEFAULT '',
			metric       LowCardinality(String),
			comparator   LowCardinality(String),
			threshold    Float64,
			window_sec   UInt32,
			severity     LowCardinality(String) DEFAULT 'warning',
			enabled      UInt8        DEFAULT 1,
			built_in     UInt8        DEFAULT 0,
			created_at   DateTime64(9) DEFAULT now64(9),
			version      UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY id`,

		`CREATE TABLE IF NOT EXISTS problems (
			id           String,
			rule_id      String,
			rule_name    String,
			severity     LowCardinality(String),
			service      LowCardinality(String),
			metric       LowCardinality(String),
			value        Float64,
			threshold    Float64,
			status       LowCardinality(String),       -- open | resolved
			description  String,
			started_at   DateTime64(9),
			resolved_at  Nullable(DateTime64(9)),
			updated_at   DateTime64(9) DEFAULT now64(9),
			version      UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		PARTITION BY toDate(started_at)
		ORDER BY id`,

		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS profiles (
			profile_id    String       CODEC(ZSTD(3)),
			service_name  LowCardinality(String),
			host_name     LowCardinality(String) DEFAULT '',
			profile_type  LowCardinality(String),
			start_time    DateTime64(9) CODEC(Delta, ZSTD(3)),
			duration_ns   Int64        CODEC(T64, ZSTD(3)),
			pprof_data    String       CODEC(ZSTD(6)),
			sample_count  UInt32       DEFAULT 0,
			labels_keys   Array(LowCardinality(String)),
			labels_values Array(String),
			INDEX idx_pid profile_id TYPE bloom_filter(0.01) GRANULARITY 4
		) ENGINE = MergeTree()
		PARTITION BY toDate(start_time)
		ORDER BY (service_name, profile_type, start_time)
		TTL toDate(start_time) + INTERVAL %d DAY
		SETTINGS index_granularity = 8192`, md),

		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS metric_points (
			metric        LowCardinality(String),
			instrument    LowCardinality(String),
			description   String       DEFAULT '',
			unit          LowCardinality(String) DEFAULT '',
			service_name  LowCardinality(String),
			host_name     LowCardinality(String) DEFAULT '',
			time          DateTime64(9) CODEC(Delta, ZSTD(3)),
			start_time    DateTime64(9) DEFAULT time CODEC(Delta, ZSTD(3)),
			value         Float64      CODEC(Gorilla, ZSTD(3)),
			count         UInt64       DEFAULT 0,
			sum_value     Float64      DEFAULT 0 CODEC(Gorilla, ZSTD(3)),
			min_value     Float64      DEFAULT 0,
			max_value     Float64      DEFAULT 0,
			attr_keys     Array(LowCardinality(String)),
			attr_values   Array(String),
			res_keys      Array(LowCardinality(String)),
			res_values    Array(String)
		) ENGINE = MergeTree()
		PARTITION BY toDate(time)
		ORDER BY (service_name, metric, time)
		TTL toDate(time) + INTERVAL %d DAY
		SETTINGS index_granularity = 8192`, md),
	}

	for _, q := range tables {
		if err := s.conn.Exec(ctx, q); err != nil {
			return fmt.Errorf("create table: %w\nSQL: %.120s", err, q)
		}
	}

	// In-place column additions for upgrades. ADD COLUMN IF NOT EXISTS is a
	// no-op on fresh installs (column already in CREATE TABLE) and on
	// already-migrated databases.
	alters := []string{
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS auth_provider LowCardinality(String) DEFAULT 'local'`,
	}
	for _, q := range alters {
		if err := s.conn.Exec(ctx, q); err != nil {
			return fmt.Errorf("alter table: %w\nSQL: %.120s", err, q)
		}
	}

	// Materialized views — pre-aggregate the high-volume spans table into
	// summary tables that read paths can hit instead of scanning raw rows.
	// New MVs go here; AggregatingMergeTree lets us combine count/sum/quantile
	// states across partitions cheaply at query time via *Merge() finalisers.
	//
	// service_summary_5m: per-(service, 5min) counts + duration quantiles.
	// Used by /services and the anomaly baseline scan to avoid touching the
	// raw spans table for time-bucketed queries that span hours/days.
	// Apdex thresholds — keep in sync with the raw-spans path in
	// repo.go (GetServices). 200ms satisfied / 800ms tolerating is the
	// industry-standard default; making them MV-baked means /api/services
	// can serve 10s of thousands of services in sub-second time.
	const apdexT = 200 * 1_000_000   // ns
	const apdex4T = 800 * 1_000_000  // ns
	mvs := []string{
		fmt.Sprintf(`CREATE MATERIALIZED VIEW IF NOT EXISTS service_summary_5m
		 ENGINE = AggregatingMergeTree
		 PARTITION BY toDate(time_bucket)
		 ORDER BY (service_name, time_bucket)
		 TTL toDate(time_bucket) + INTERVAL 90 DAY
		 SETTINGS index_granularity = 8192
		 AS SELECT
		   service_name,
		   toStartOfInterval(time, INTERVAL 5 MINUTE)  AS time_bucket,
		   countState()                                AS span_count_state,
		   countIfState(status_code = 'error')         AS error_count_state,
		   sumState(duration)                          AS duration_sum_state,
		   quantilesState(0.5, 0.95, 0.99)(duration)   AS duration_q_state,
		   countIfState(duration <= %d)                AS apdex_satisfied_state,
		   countIfState(duration > %d AND duration <= %d) AS apdex_tolerating_state
		 FROM spans
		 GROUP BY service_name, time_bucket`, apdexT, apdexT, apdex4T),
	}
	for _, q := range mvs {
		if err := s.conn.Exec(ctx, q); err != nil {
			return fmt.Errorf("create MV: %w\nSQL: %.140s", err, q)
		}
	}
	// Forward-compat: ClickHouse doesn't support ADD COLUMN on
	// MaterializedView storage, so on an upgrade from a pre-apdex MV we
	// detect the missing column and drop+recreate. The raw `spans` table
	// still has every source row, so the MV repopulates from new ingest
	// immediately; old buckets are gone but a one-time backfill via
	// `INSERT INTO service_summary_5m SELECT ... FROM spans WHERE
	// time < <cutoff>` can restore them if the operator wants.
	var hasApdex uint8
	probeSQL := `
		SELECT count() > 0
		FROM system.columns
		WHERE database = currentDatabase()
		  AND table    = 'service_summary_5m'
		  AND name     = 'apdex_satisfied_state'`
	if err := s.conn.QueryRow(ctx, probeSQL).Scan(&hasApdex); err == nil && hasApdex == 0 {
		log.Println("[chstore] upgrading service_summary_5m MV (adding apdex states) — past summary buckets will be dropped")
		if err := s.conn.Exec(ctx, `DROP TABLE IF EXISTS service_summary_5m`); err != nil {
			return fmt.Errorf("drop old MV for upgrade: %w", err)
		}
		// Re-run the create (mvs[0] from above) now that the old one is gone.
		if err := s.conn.Exec(ctx, mvs[0]); err != nil {
			return fmt.Errorf("recreate MV with apdex: %w", err)
		}
	}
	log.Println("[chstore] migrations complete")
	return nil
}
