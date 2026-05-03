package chstore

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/cenk/qmetry/internal/config"
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

	// Create database using default DB connection
	setup, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{cfg.Addr},
		Auth:        clickhouse.Auth{Database: "default", Username: cfg.Username, Password: cfg.Password},
		DialTimeout: dialTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("setup connect: %w", err)
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
	log.Println("[chstore] migrations complete")
	return nil
}
