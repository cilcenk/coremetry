package chstore

import (
	"context"
	"crypto/tls"
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

	// neighborProvider is the optional 1-hop topology lookup used
	// by AttachProblemToIncident for rule 3 (cluster a new
	// problem into an existing incident on a service that calls
	// or is called by this one). Set via SetNeighborProvider —
	// kept on the store so the three auto-attach call sites
	// (evaluator, anomaly, monitor) don't need to thread it
	// through their constructors.
	neighborProvider NeighborProvider
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

	// Comma-split address — driver round-robins / fails over across
	// the seeds, so a 4-node external cluster can be configured
	// without a separate LB. Falls back to the raw string when no
	// commas are present.
	hosts := cfg.Hosts()
	if len(hosts) == 0 {
		return nil, fmt.Errorf("clickhouse addr is required")
	}
	var tlsCfg *tls.Config
	if cfg.Secure {
		tlsCfg = &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify}
	}
	if cfg.Secure {
		log.Printf("[chstore] connecting to %d host(s) over native TLS (insecure=%v): %v",
			len(hosts), cfg.InsecureSkipVerify, hosts)
	} else if len(hosts) > 1 {
		log.Printf("[chstore] connecting to %d host(s) (driver fail-over enabled): %v", len(hosts), hosts)
	}

	// Create database using default DB connection. CH may still be coming
	// up (Helm-managed StatefulSet, fresh container, etc.) so retry the
	// initial CREATE DATABASE for up to ~2 minutes before giving up.
	var setup driver.Conn
	openSetup := func() error {
		c, err := clickhouse.Open(&clickhouse.Options{
			Addr:        hosts,
			Auth:        clickhouse.Auth{Database: "default", Username: cfg.Username, Password: cfg.Password},
			TLS:         tlsCfg,
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

	// Connect to target database.
	//
	// Per-query memory + execution defaults. Pegged here so every
	// statement issued through this driver inherits them — protects
	// against any single read swallowing the whole CH heap.
	//
	//   max_memory_usage (4 GB)
	//     Hard cap per query. Without it, a runaway GROUP BY /
	//     DISTINCT on a billion-span table can spike to 15-20 GB
	//     and trip the server-wide total — surfacing as
	//     "memory limit (total) exceeded" (CH error 241) and
	//     blocking unrelated reads.
	//
	//   max_bytes_before_external_group_by (1 GB)
	//   max_bytes_before_external_sort     (1 GB)
	//     Spill thresholds. When in-memory state crosses 1 GB,
	//     CH writes to a temp file on disk. Slower but the query
	//     finishes — vs. OOM'ing the whole pool.
	//
	//   distributed_aggregation_memory_efficient
	//     Streams partial aggregates instead of buffering whole
	//     result sets — irrelevant on single-node setups but
	//     harmless and improves big external CH clusters.
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: hosts,
		Auth: clickhouse.Auth{Database: cfg.Database, Username: cfg.Username, Password: cfg.Password},
		TLS:  tlsCfg,
		Compression:     &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
		DialTimeout:     dialTimeout,
		MaxOpenConns:    maxConns,
		MaxIdleConns:    maxConns / 2,
		ConnMaxLifetime: time.Hour,
		Settings: clickhouse.Settings{
			"max_execution_time":                       60,
			"max_memory_usage":                         4_000_000_000,
			"max_bytes_before_external_group_by":       1_000_000_000,
			"max_bytes_before_external_sort":           1_000_000_000,
			"distributed_aggregation_memory_efficient": 1,
		},
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

// Ping reports CH liveness. Used by /api/status — wraps the driver's
// own Ping so we don't expose the driver type to callers.
func (s *Store) Ping(ctx context.Context) error { return s.conn.Ping(ctx) }

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

		// Service catalog metadata — operator-curated per-service
		// info (owner team, oncall channel, runbook URL, repo,
		// description) that the spans table doesn't carry. Joins
		// against service_name as the primary key. Plain
		// ReplacingMergeTree because rows change once per
		// reorg, not per request — version is just for last-
		// write-wins on edit.
		`CREATE TABLE IF NOT EXISTS service_metadata (
			service       String,
			owner_team    String DEFAULT '',
			sre_team      String DEFAULT '',
			description   String DEFAULT '',
			repository    String DEFAULT '',
			runbook_url   String DEFAULT '',
			oncall_url    String DEFAULT '',
			-- chat_channel was renamed from slack_channel — we
			-- preserve the legacy column for upgraded installs
			-- (see ALTER below) and write to the new one going
			-- forward.
			chat_channel  String DEFAULT '',
			slack_channel String DEFAULT '',
			-- custom_links — JSON array of {label, url} entries.
			-- Lets operators bolt on Grafana dashboards, Kibana
			-- searches, internal apps, status pages, etc. per
			-- service without us baking each surface in as a
			-- column.
			custom_links  String DEFAULT '[]',
			updated_at    DateTime64(9) DEFAULT now64(9),
			version       UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY service`,

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
			-- match_rules — JSON predicates that gate delivery
			-- per channel: services / sreTeams / ownerTeams.
			-- Empty / {} means "catch-all"; populated arrays
			-- AND'd against the problem's service catalog.
			match_rules String DEFAULT '{}',
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
			variables    String       DEFAULT '[]' CODEC(ZSTD(3)),
			created_at   DateTime64(9) DEFAULT now64(9),
			updated_at   DateTime64(9) DEFAULT now64(9),
			version      UInt64       DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY id`,
		// Forward-compat: add the variables column to installs that
		// pre-date the Grafana-style variable system.
		`ALTER TABLE dashboards ADD COLUMN IF NOT EXISTS variables String DEFAULT '[]' CODEC(ZSTD(3))`,

		// maintenance_windows: operator-declared time ranges that
		// suppress alert notifications + auto-incident attach.
		// Match-mode is one of:
		//   • service = '*'             → global silence
		//   • service = '<exact name>'  → single-service silence
		//   • service ends with '*'     → prefix match
		// Active while start_at <= now() <= end_at. Problems
		// still open + auto-resolve normally; only the
		// notification fan-out is skipped, so the operator can
		// review what happened during the window via the
		// /anomalies + /incidents pages after the fact.
		`CREATE TABLE IF NOT EXISTS maintenance_windows (
			id          String,
			service     String,                       -- '*', exact, or 'name*' prefix
			severity    LowCardinality(String) DEFAULT '*',  -- '*', 'info', 'warning', 'critical'
			start_at    DateTime64(9),
			end_at      DateTime64(9),
			reason      String        DEFAULT '',
			created_by  String        DEFAULT '',
			created_at  DateTime64(9) DEFAULT now64(9),
			disabled    UInt8         DEFAULT 0,
			version     UInt64        DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY id`,

		// anomaly_silences: per-fingerprint mute. Active while
		// until_at > now(). Silenced anomalies still get recorded
		// in anomaly_events (so the history table shows them) but
		// are suppressed in the live sections + skip notifications.
		`CREATE TABLE IF NOT EXISTS anomaly_silences (
			id           String,
			fingerprint  String,                       -- matches anomaly_events.id
			kind         LowCardinality(String),
			pattern      String,
			service      LowCardinality(String),
			created_by   String,                        -- user email
			created_at   DateTime64(9),
			until_at     DateTime64(9),
			reason       String        DEFAULT '',
			version      UInt64        DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY id`,

		// audit_log: who did what, when. Append-only event stream.
		// Used by admin compliance flow + the /admin/audit page.
		// Partitioned monthly so the TTL (1 year) drops whole
		// partitions instead of mutating rows.
		`CREATE TABLE IF NOT EXISTS audit_log (
			id           String,
			time         DateTime64(9),
			actor_id     String,                        -- user.id
			actor_email  String,
			actor_role   LowCardinality(String),
			action       LowCardinality(String),       -- e.g. "alert_rule.update"
			target_kind  LowCardinality(String),
			target_id    String        DEFAULT '',
			ip           String        DEFAULT '',
			details      String        DEFAULT ''     -- JSON: before/after diff or freeform
		) ENGINE = MergeTree()
		PARTITION BY toYYYYMM(time)
		ORDER BY (time, id)
		TTL toDate(time) + INTERVAL 365 DAY`,

		// saved_views: per-user named query / filter combos for
		// /traces, /logs, /anomalies, /metrics, etc. Stored as
		// the raw URL query string so applying a view = restoring
		// the URL, no schema coupling between server and SPA.
		`CREATE TABLE IF NOT EXISTS saved_views (
			id           String,
			owner_id     String,                        -- user.id; '' = team-shared
			name         String,
			page         LowCardinality(String),       -- "traces" | "logs" | "anomalies" | …
			query_string String,                        -- raw URL search (?from=…&to=…&filter=…)
			pinned       UInt8         DEFAULT 0,       -- pin to sidebar / topbar chip strip
			created_at   DateTime64(9) DEFAULT now64(9),
			version      UInt64        DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY id`,

		// anomaly_events: persistent record of detected log-pattern
		// + trace-op anomalies. ReplacingMergeTree(version) keeps
		// only the latest row per id; the recorder upserts on every
		// detector tick so last_seen advances continuously while a
		// pattern is firing. The /anomalies page derives "active"
		// vs "cleared" status from last_seen freshness in the query
		// layer — no separate sweep needed.
		`CREATE TABLE IF NOT EXISTS anomaly_events (
			id            String,
			kind          LowCardinality(String),     -- log_pattern | trace_op
			pattern       String,                      -- pattern name OR operation name
			service       LowCardinality(String),
			started_at    DateTime64(9),
			last_seen     DateTime64(9),
			peak_ratio    Float64,
			current_ratio Float64,
			current_count UInt64,
			sample        String        DEFAULT '',
			version       UInt64        DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		PARTITION BY toDate(started_at)
		ORDER BY id
		TTL toDate(started_at) + INTERVAL 30 DAY`,

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
			runbook_url  String       DEFAULT '',
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

		// ── Synthetic monitoring ─────────────────────────────────────
		// Definitions for HTTP probes and passive heartbeats. Probed by
		// the runner loop in the background; results stream into
		// monitor_results below for status timeline + uptime% calc.
		`CREATE TABLE IF NOT EXISTS monitors (
			id            String,
			name          String,
			type          LowCardinality(String),         -- http | heartbeat
			-- HTTP-only fields (ignored for heartbeats):
			url           String        DEFAULT '',
			method        LowCardinality(String) DEFAULT 'GET',
			expected_status UInt16      DEFAULT 200,
			timeout_sec   UInt16        DEFAULT 5,
			-- Common:
			interval_sec  UInt32        DEFAULT 60,        -- probe interval (HTTP) or grace window (heartbeat)
			enabled       UInt8         DEFAULT 1,
			-- Heartbeat-only:
			heartbeat_token String      DEFAULT '',        -- random; appears in /api/heartbeats/{token}
			created_at    DateTime64(9) DEFAULT now64(9),
			version       UInt64        DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY id`,

		// One row per probe attempt — keeps a 30d (default) timeline
		// the UI can render as a status bar. status = up | down |
		// degraded; latency_ms = wall-clock probe time. message holds
		// the failure reason for down/degraded rows.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS monitor_results (
			monitor_id  String,
			time        DateTime64(9) CODEC(Delta, ZSTD(3)),
			status      LowCardinality(String),
			latency_ms  Int64         CODEC(T64, ZSTD(3)),
			http_code   UInt16        DEFAULT 0,
			message     String        CODEC(ZSTD(3)),
			INDEX idx_mid monitor_id TYPE bloom_filter(0.01) GRANULARITY 4
		) ENGINE = MergeTree()
		PARTITION BY toDate(time)
		ORDER BY (monitor_id, time)
		TTL toDate(time) + INTERVAL %d DAY
		SETTINGS index_granularity = 8192`, 30),

		// ── Incident management ──────────────────────────────────────
		// One row per declared incident. Multiple Problems / Monitor
		// flips auto-attach to a single Incident when they share the
		// same service + severity within a short window — gives the
		// oncall one place to track the whole event end-to-end.
		`CREATE TABLE IF NOT EXISTS incidents (
			id          String,
			title       String,
			severity    LowCardinality(String),       -- info | warning | critical
			status      LowCardinality(String),       -- open | acknowledged | resolved
			service     LowCardinality(String) DEFAULT '',
			summary     String        DEFAULT '',
			assignee    String        DEFAULT '',
			postmortem  String        DEFAULT '',     -- markdown body, blameless template
			started_at  DateTime64(9),
			ack_at      Nullable(DateTime64(9)),
			resolved_at Nullable(DateTime64(9)),
			updated_at  DateTime64(9) DEFAULT now64(9),
			version     UInt64        DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		PARTITION BY toDate(started_at)
		ORDER BY id`,

		// Append-only timeline of events on each incident — Problem
		// attachments, state changes, manual notes from oncall. Drives
		// the "what happened when" UI on the incident detail page.
		`CREATE TABLE IF NOT EXISTS incident_events (
			incident_id String,
			time        DateTime64(9) DEFAULT now64(9) CODEC(Delta, ZSTD(3)),
			kind        LowCardinality(String),       -- created | ack | resolved | note | problem_attached | problem_resolved
			actor       String        DEFAULT '',     -- user email or 'system'
			body        String        DEFAULT '',
			ref_id      String        DEFAULT '',     -- problem id, etc.
			INDEX idx_iid incident_id TYPE bloom_filter(0.01) GRANULARITY 4
		) ENGINE = MergeTree()
		PARTITION BY toDate(time)
		ORDER BY (incident_id, time)
		SETTINGS index_granularity = 8192`,

		// problem_id → incident_id mapping for auto-grouping.
		// ReplacingMergeTree on problem_id keeps only the latest
		// assignment when a problem gets re-grouped.
		`CREATE TABLE IF NOT EXISTS incident_problems (
			problem_id  String,
			incident_id String,
			attached_at DateTime64(9) DEFAULT now64(9),
			version     UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY problem_id`,

		// ── Runtime settings (key/value, admin-set overrides) ───────
		// Holds anything the operator can change live without a config
		// reload — currently retention TTLs per signal table. Schema
		// is intentionally generic so adding new keys later doesn't
		// need a migration.
		`CREATE TABLE IF NOT EXISTS system_settings (
			key        String,
			value      String,
			updated_at DateTime64(9) DEFAULT now64(9),
			updated_by String        DEFAULT '',
			version    UInt64        DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY key`,
		// Forward-compat: add updated_by to installs that pre-date it.
		// Idempotent — IF NOT EXISTS makes re-running a no-op.
		`ALTER TABLE system_settings ADD COLUMN IF NOT EXISTS updated_by String DEFAULT ''`,

		// ── Public status page ──────────────────────────────────────
		// Single-row config table — operator-customizable header for
		// the public /public-status page. ID always 'default'.
		`CREATE TABLE IF NOT EXISTS status_page_config (
			id          String,
			title       String        DEFAULT 'Service Status',
			description String        DEFAULT '',
			support_url String        DEFAULT '',
			updated_at  DateTime64(9) DEFAULT now64(9),
			version     UInt64        DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY id`,

		// Curated list of components shown on the public status page.
		// Each component derives its current state from EITHER:
		//   - a monitor (HTTP probe / heartbeat) — if monitor_id set
		//   - the absence of open critical incidents on a service —
		//     if service_name set
		// display_order controls top-to-bottom rendering.
		`CREATE TABLE IF NOT EXISTS status_page_components (
			id            String,
			name          String,
			description   String        DEFAULT '',
			monitor_id    String        DEFAULT '',
			service_name  String        DEFAULT '',
			display_order Int32         DEFAULT 0,
			created_at    DateTime64(9) DEFAULT now64(9),
			version       UInt64        DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY id`,

		// Email subscribers — get notified when a public-visible
		// incident opens or resolves on the configured components.
		// 'verified' is reserved for a future double-opt-in flow;
		// today the row is created in the verified state.
		`CREATE TABLE IF NOT EXISTS status_page_subscribers (
			id         String,
			email      String,
			verified   UInt8         DEFAULT 1,
			created_at DateTime64(9) DEFAULT now64(9),
			version    UInt64        DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY email`,

		// Trace snapshots — Grafana-style "share publicly" links for
		// the trace detail page. Each row mints a URL-safe token that
		// resolves to a trace_id, gated by `expires_at`. Public route
		// (no auth) reads by token. Token is the primary key so we can
		// revoke or list per-trace cheaply.
		//
		// TTL drops expired rows during background merges so the table
		// doesn't grow unboundedly with one-off shares. 7-day grace
		// past expires_at leaves forensics room (who minted what for
		// which trace) before the row physically goes away.
		`CREATE TABLE IF NOT EXISTS trace_snapshots (
			token        String,
			trace_id     String,
			created_by   String        DEFAULT '',
			created_at   DateTime64(9) DEFAULT now64(9),
			expires_at   DateTime64(9),
			version      UInt64        DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY token
		TTL toDateTime(expires_at) + INTERVAL 7 DAY`,

		// Marks an existing incident as visible on the public status
		// page. Operator toggles via the admin UI. Kept as a separate
		// table so the existing incidents row schema stays untouched
		// (ALTER on ReplacingMergeTree is awkward).
		`CREATE TABLE IF NOT EXISTS status_page_published (
			incident_id   String,
			published     UInt8         DEFAULT 1,
			public_title  String        DEFAULT '',  -- override the internal title for public display
			public_body   String        DEFAULT '',  -- markdown explanation suitable for end users
			updated_at    DateTime64(9) DEFAULT now64(9),
			version       UInt64        DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY incident_id`,

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

		// Pre-aggregated topology edges (v0.5.108). The service-
		// level topology view used to run a self-join on the spans
		// table per request — at billions-of-spans-per-day scale
		// that's a non-starter. A background aggregator goroutine
		// runs the join once per 5-min bucket and stores results
		// here; API reads from this table instead of spans. 14d
		// retention is plenty for an overview surface; ReplacingMergeTree
		// dedupes re-runs over the same bucket (idempotent backfills).
		`CREATE TABLE IF NOT EXISTS topology_edges_5m (
			time_bucket     DateTime CODEC(DoubleDelta, ZSTD(3)),
			parent_service  LowCardinality(String),
			child_node      String,
			node_kind       LowCardinality(String),  -- service|db|queue|external
			protocol        LowCardinality(String),  -- http|rpc|db|kafka|internal
			top_labels      Array(String),
			distinct_labels UInt32,
			calls           UInt64,
			version         UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		PARTITION BY toDate(time_bucket)
		ORDER BY (time_bucket, parent_service, child_node, node_kind, protocol)
		TTL toDate(time_bucket) + INTERVAL 14 DAY`,
	}

	for _, q := range tables {
		if err := s.execDDL(ctx, q); err != nil {
			return fmt.Errorf("create table: %w", err)
		}
	}

	// In-place column additions for upgrades. ADD COLUMN IF NOT EXISTS is a
	// no-op on fresh installs (column already in CREATE TABLE) and on
	// already-migrated databases.
	//
	// Skip indexes on the spans hot path. The (service_name, time)
	// primary key already drives most queries, but the transport-aware
	// alert metrics filter on `kind`, `db_system`, and `http_status`
	// in additional WHERE predicates. Adding granule-level skip
	// indexes lets ClickHouse drop unrelated 8k-row blocks before
	// reading them — meaningful at 1B spans/day where each daily
	// partition holds ~150k granules.
	//   - idx_kind / idx_db_system: set(0) → bitmap of distinct values
	//     per granule. Filter `kind='server'` or `db_system != ''`
	//     skips most internal-only granules instantly.
	//   - idx_http_status: minmax → granule's status range. Filter
	//     `http_status >= 500` skips granules of pure 200s (the
	//     overwhelming majority).
	// New data benefits immediately; existing data isn't rewritten
	// (MATERIALIZE INDEX is too heavy on a 36B-row table) but ages
	// out via TTL inside the retention window.
	alters := []string{
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS auth_provider LowCardinality(String) DEFAULT 'local'`,
		// team — operator-curated grouping (e.g. "platform-sre",
		// "fraud", "payments"). LowCardinality because each
		// install has tens to low-hundreds of teams; values
		// repeat heavily across the user list.
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS team LowCardinality(String) DEFAULT ''`,
		`ALTER TABLE alert_rules ADD COLUMN IF NOT EXISTS runbook_url String DEFAULT ''`,
		`ALTER TABLE service_metadata ADD COLUMN IF NOT EXISTS sre_team String DEFAULT ''`,
		// chat_channel — successor to slack_channel. Existing
		// rows with a populated slack_channel keep showing it
		// in the UI through the read-time fallback in
		// GetServiceMetadata; new edits write to chat_channel.
		`ALTER TABLE service_metadata ADD COLUMN IF NOT EXISTS chat_channel String DEFAULT ''`,
		// custom_links — operator-bolted-on links per service
		// (Grafana board, Kibana saved search, internal SRE
		// app, status page, etc.). Stored as a JSON array so
		// the schema doesn't grow per surface.
		`ALTER TABLE service_metadata ADD COLUMN IF NOT EXISTS custom_links String DEFAULT '[]'`,
		// Notification routing — one column carrying a JSON
		// blob with predicates: { "services": [...],
		// "sreTeams": [...], "ownerTeams": [...] }. Empty /
		// {} = catch-all. Same shape regardless of channel
		// type so an admin can target any of email / slack /
		// teams / zoomchat / webhook to a specific team.
		`ALTER TABLE notification_channels ADD COLUMN IF NOT EXISTS match_rules String DEFAULT '{}'`,
		// Spans columns needed by v0.5.102+ topology queries. Older
		// installs whose spans table was created before these
		// fields existed in CREATE TABLE would otherwise return
		// "Missing column" on the service-level topology join. All
		// default to '' so existing rows behave as if the field is
		// unset — same as ingestion paths that don't populate them.
		`ALTER TABLE spans ADD COLUMN IF NOT EXISTS kind         LowCardinality(String) DEFAULT 'internal'`,
		`ALTER TABLE spans ADD COLUMN IF NOT EXISTS peer_service LowCardinality(String) DEFAULT ''`,
		`ALTER TABLE spans ADD COLUMN IF NOT EXISTS msg_system   LowCardinality(String) DEFAULT ''`,
		`ALTER TABLE spans ADD COLUMN IF NOT EXISTS rpc_system   LowCardinality(String) DEFAULT ''`,
		`ALTER TABLE spans ADD COLUMN IF NOT EXISTS rpc_method   LowCardinality(String) DEFAULT ''`,
		`ALTER TABLE spans ADD COLUMN IF NOT EXISTS http_method  LowCardinality(String) DEFAULT ''`,
		`ALTER TABLE spans ADD COLUMN IF NOT EXISTS http_route   LowCardinality(String) DEFAULT ''`,
		`ALTER TABLE spans ADD COLUMN IF NOT EXISTS db_system    LowCardinality(String) DEFAULT ''`,
		`ALTER TABLE spans ADD INDEX IF NOT EXISTS idx_kind        kind        TYPE set(0)    GRANULARITY 4`,
		`ALTER TABLE spans ADD INDEX IF NOT EXISTS idx_db_system   db_system   TYPE set(0)    GRANULARITY 4`,
		`ALTER TABLE spans ADD INDEX IF NOT EXISTS idx_http_status http_status TYPE minmax    GRANULARITY 4`,
		// idx_status powers the per-operation error-anomaly
		// detector (anomaly/trace_ops.go) — countIf(status_code='error')
		// over a 5-min window otherwise touches every granule
		// in the slice. status_code is LowCardinality with 3
		// values (ok / error / unset) so a set(0) index is
		// near-zero overhead and lets CH skip granules whose
		// status set doesn't include 'error'.
		`ALTER TABLE spans ADD INDEX IF NOT EXISTS idx_status      status_code TYPE set(0)    GRANULARITY 4`,
		// Apply the trace_snapshots TTL to installs that created
		// the table before v0.5.91. MODIFY TTL is metadata-only;
		// repeated applies are idempotent.
		`ALTER TABLE trace_snapshots MODIFY TTL toDateTime(expires_at) + INTERVAL 7 DAY`,
	}
	for _, q := range alters {
		if err := s.execDDL(ctx, q); err != nil {
			// Skip-index ALTERs against a Distributed engine return
			// CH error 48 ("Alter of type 'ADD_INDEX' is not
			// supported by storage Distributed"). That happens when
			// the operator points Coremetry at a cluster but didn't
			// set chstore.cluster_name, so adaptDDL can't rewrite
			// to <table>_local ON CLUSTER. These indexes are pure
			// query-time optimisations; missing them slows some
			// scans but never breaks correctness, so we log and
			// continue instead of crash-looping the pod. The
			// operator can run them by hand against the per-shard
			// local tables.
			if isClusterUnsupportedAlter(err) {
				log.Printf("[chstore] skip-index alter not supported on Distributed engine (config.clickhouse.cluster_name not set?). Skipping: %.80s", q)
				continue
			}
			// Column ALTERs (auth_provider, runbook_url, etc.) on
			// pre-existing tables that were created by an older
			// version are idempotent via IF NOT EXISTS. A genuine
			// failure here (DDL syntax error, permissions) still
			// crashes — we only soften the specific Distributed
			// limitation.
			return fmt.Errorf("alter table: %w", err)
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

		// operation_summary_5m: per-(service, operation, 5min) pre-
		// aggregation that powers the OperationsTable on the
		// service detail page. Pre-v0.4.99 GetOperationSummary
		// scanned raw spans GROUP BY name over the entire window,
		// which on a billion-spans/day service detail page took
		// ~500ms cold. Reading the MV instead drops it to single-
		// digit ms because the projection is already pre-aggregated
		// by name within each 5-min slot. Same aggregate states as
		// service_summary_5m (count + error + sum/duration +
		// quantiles + apdex satisfied/tolerating) so the read path
		// can compute the same numeric set the raw-spans query
		// produced, just from a much smaller dataset.
		fmt.Sprintf(`CREATE MATERIALIZED VIEW IF NOT EXISTS operation_summary_5m
		 ENGINE = AggregatingMergeTree
		 PARTITION BY toDate(time_bucket)
		 ORDER BY (service_name, name, time_bucket)
		 TTL toDate(time_bucket) + INTERVAL 90 DAY
		 SETTINGS index_granularity = 8192
		 AS SELECT
		   service_name,
		   name,
		   toStartOfInterval(time, INTERVAL 5 MINUTE)  AS time_bucket,
		   countState()                                AS span_count_state,
		   countIfState(status_code = 'error')         AS error_count_state,
		   sumState(duration)                          AS duration_sum_state,
		   quantilesState(0.5, 0.95, 0.99)(duration)   AS duration_q_state,
		   countIfState(duration <= %d)                AS apdex_satisfied_state,
		   countIfState(duration > %d AND duration <= %d) AS apdex_tolerating_state
		 FROM spans
		 GROUP BY service_name, name, time_bucket`, apdexT, apdexT, apdex4T),

		// trace_summary_1d: per-day distinct trace count via HLL.
		// Lets /admin/stats history show traces-per-day without a
		// uniqExact pass over billions of rows. uniqState writes a
		// HLL12 sketch (~2.5 KiB per day per service); merging across
		// 30 days is sub-millisecond.
		`CREATE MATERIALIZED VIEW IF NOT EXISTS trace_summary_1d
		 ENGINE = AggregatingMergeTree
		 PARTITION BY toYYYYMM(day)
		 ORDER BY day
		 TTL day + INTERVAL 365 DAY
		 SETTINGS index_granularity = 8192
		 AS SELECT
		   toDate(time)        AS day,
		   uniqState(trace_id) AS trace_count_state
		 FROM spans
		 GROUP BY day`,

		// db_summary_5m: per-(db_system, peer_service, 5-min) pre-
		// aggregation powering /api/databases. Pre-v0.5.9 every
		// page load issued two raw-spans GROUP BYs over a 1h
		// window — ~40M rows scanned twice on a billion-span/day
		// deployment. Reading the MV instead drops that to
		// thousands of rows. Aggregate states (countState /
		// quantilesState / sumState) compose across partitions
		// so 1h / 6h / 24h all merge sub-millisecond.
		//
		// The COALESCE for "unknown" mirrors the raw query so the
		// MV's instance column is comparable to the raw output —
		// keeps the read path's SQL near-identical.
		`CREATE MATERIALIZED VIEW IF NOT EXISTS db_summary_5m
		 ENGINE = AggregatingMergeTree
		 PARTITION BY toDate(time_bucket)
		 ORDER BY (db_system, instance, time_bucket)
		 TTL toDate(time_bucket) + INTERVAL 90 DAY
		 SETTINGS index_granularity = 8192
		 AS SELECT
		   db_system,
		   coalesce(nullIf(peer_service, ''), 'unknown') AS instance,
		   toStartOfInterval(time, INTERVAL 5 MINUTE)    AS time_bucket,
		   countState()                                  AS span_count_state,
		   countIfState(status_code = 'error')           AS error_count_state,
		   sumState(duration)                            AS duration_sum_state,
		   quantilesState(0.5, 0.95, 0.99)(duration)     AS duration_q_state
		 FROM spans
		 WHERE db_system != ''
		 GROUP BY db_system, instance, time_bucket`,

		// db_caller_summary_5m: per-(db_system, peer_service,
		// service_name, host_name, 5-min) — drives the row-click
		// detail drawer on /databases. host_name carries the
		// resource.host.name = k8s pod name in containerised
		// deployments, which is the resolution the drawer's
		// per-pod breakdown wants.
		`CREATE MATERIALIZED VIEW IF NOT EXISTS db_caller_summary_5m
		 ENGINE = AggregatingMergeTree
		 PARTITION BY toDate(time_bucket)
		 ORDER BY (db_system, instance, service_name, host_name, time_bucket)
		 TTL toDate(time_bucket) + INTERVAL 90 DAY
		 SETTINGS index_granularity = 8192
		 AS SELECT
		   db_system,
		   coalesce(nullIf(peer_service, ''), 'unknown') AS instance,
		   service_name,
		   coalesce(nullIf(host_name, ''), '(unknown)')  AS host_name,
		   toStartOfInterval(time, INTERVAL 5 MINUTE)    AS time_bucket,
		   countState()                                  AS span_count_state,
		   countIfState(status_code = 'error')           AS error_count_state,
		   sumState(duration)                            AS duration_sum_state,
		   quantilesState(0.5, 0.95, 0.99)(duration)     AS duration_q_state
		 FROM spans
		 WHERE db_system != ''
		 GROUP BY db_system, instance, service_name, host_name, time_bucket`,

		// messaging_summary_5m: structural parallel for /api/messaging.
		// Cluster + destination are derived expressions in the source
		// query because the dimension lives in attr_keys/attr_values
		// rather than dedicated columns. We materialise the resolved
		// values so the read path joins on plain string equality.
		`CREATE MATERIALIZED VIEW IF NOT EXISTS messaging_summary_5m
		 ENGINE = AggregatingMergeTree
		 PARTITION BY toDate(time_bucket)
		 ORDER BY (msg_system, cluster, destination, time_bucket)
		 TTL toDate(time_bucket) + INTERVAL 90 DAY
		 SETTINGS index_granularity = 8192
		 AS SELECT
		   msg_system,
		   coalesce(
		     nullIf(attr_values[indexOf(attr_keys, 'server.address')], ''),
		     nullIf(attr_values[indexOf(attr_keys, 'messaging.kafka.bootstrap.servers')], ''),
		     nullIf(attr_values[indexOf(attr_keys, 'messaging.kafka.cluster.name')], ''),
		     '(default)'
		   ) AS cluster,
		   coalesce(
		     nullIf(attr_values[indexOf(attr_keys, 'messaging.destination.name')], ''),
		     nullIf(attr_values[indexOf(attr_keys, 'messaging.destination')], ''),
		     nullIf(peer_service, ''),
		     'unknown'
		   ) AS destination,
		   toStartOfInterval(time, INTERVAL 5 MINUTE) AS time_bucket,
		   countState()                               AS span_count_state,
		   countIfState(status_code = 'error')        AS error_count_state,
		   sumState(duration)                         AS duration_sum_state,
		   quantilesState(0.5, 0.95, 0.99)(duration)  AS duration_q_state
		 FROM spans
		 WHERE msg_system != ''
		 GROUP BY msg_system, cluster, destination, time_bucket`,

		// messaging_caller_summary_5m: per-(msg_system, cluster,
		// destination, service_name, host_name, kind, 5-min). Kind
		// rides the dim so the messaging drawer can split
		// Producers / Consumers without a second pass.
		`CREATE MATERIALIZED VIEW IF NOT EXISTS messaging_caller_summary_5m
		 ENGINE = AggregatingMergeTree
		 PARTITION BY toDate(time_bucket)
		 ORDER BY (msg_system, cluster, destination, service_name, host_name, kind, time_bucket)
		 TTL toDate(time_bucket) + INTERVAL 90 DAY
		 SETTINGS index_granularity = 8192
		 AS SELECT
		   msg_system,
		   coalesce(
		     nullIf(attr_values[indexOf(attr_keys, 'server.address')], ''),
		     nullIf(attr_values[indexOf(attr_keys, 'messaging.kafka.bootstrap.servers')], ''),
		     nullIf(attr_values[indexOf(attr_keys, 'messaging.kafka.cluster.name')], ''),
		     '(default)'
		   ) AS cluster,
		   coalesce(
		     nullIf(attr_values[indexOf(attr_keys, 'messaging.destination.name')], ''),
		     nullIf(attr_values[indexOf(attr_keys, 'messaging.destination')], ''),
		     nullIf(peer_service, ''),
		     'unknown'
		   ) AS destination,
		   service_name,
		   coalesce(nullIf(host_name, ''), '(unknown)') AS host_name,
		   kind,
		   toStartOfInterval(time, INTERVAL 5 MINUTE)   AS time_bucket,
		   countState()                                 AS span_count_state,
		   countIfState(status_code = 'error')          AS error_count_state,
		   sumState(duration)                           AS duration_sum_state,
		   quantilesState(0.5, 0.95, 0.99)(duration)    AS duration_q_state
		 FROM spans
		 WHERE msg_system != ''
		 GROUP BY msg_system, cluster, destination, service_name, host_name, kind, time_bucket`,

		// trace_summary_5m — per-(trace_id, 5min bucket) rollup
		// of everything the /traces list needs: root span info,
		// span count, error flag, duration. The /traces query
		// used to GROUP BY trace_id over raw spans, which on a
		// 7-day window with one service touched 10–100M rows
		// even with the (service_name, time) primary key prune.
		// Reading the MV slashes that to thousands of state rows
		// — sub-second 7-day queries at billion-spans/day scale.
		//
		// A single trace can span multiple 5-min buckets when
		// it's long-running (background jobs / batch ETL); the
		// read path GROUPs BY trace_id across buckets and
		// merges state. The merge is closed under * so 6
		// buckets of one trace produce identical results to
		// scanning the spans directly.
		//
		// argMaxIfState picks the value from the *root* span
		// (parent_id empty/zero) when present, falling back to
		// any span's value via the secondary maxStateIf branch.
		// Traces with no root span (orphans / Tempo-style
		// partials) still get a service name from this fallback
		// instead of rendering as "(unknown)".
		`CREATE MATERIALIZED VIEW IF NOT EXISTS trace_summary_5m
		 ENGINE = AggregatingMergeTree
		 PARTITION BY toDate(time_bucket)
		 ORDER BY (time_bucket, trace_id)
		 TTL toDate(time_bucket) + INTERVAL 90 DAY
		 SETTINGS index_granularity = 8192
		 AS SELECT
		   trace_id,
		   toStartOfInterval(time, INTERVAL 5 MINUTE) AS time_bucket,
		   argMaxIfState(service_name, time,
		     (parent_id = '' OR parent_id = '0000000000000000') AND name != '') AS root_service_state,
		   argMaxIfState(name, time,
		     (parent_id = '' OR parent_id = '0000000000000000') AND name != '') AS root_name_state,
		   minState(time)                            AS trace_start_state,
		   maxState(toUnixTimestamp64Nano(time) + duration) AS trace_end_state,
		   countState()                              AS span_count_state,
		   countIfState(status_code = 'error')       AS error_count_state
		 FROM spans
		 GROUP BY trace_id, time_bucket`,

		// trace_service_index_5m — sparse (service_name,
		// trace_id) mapping. Lets a service-filtered /traces
		// query find the relevant trace_ids without scanning
		// spans. Two-stage read pattern:
		//   1. SELECT trace_id FROM this MV WHERE service_name=?
		//      AND time_bucket >= ? GROUP BY trace_id ORDER BY
		//      latest_bucket DESC LIMIT N — uses the
		//      (service_name, time_bucket) prefix for partition
		//      + sort access.
		//   2. SELECT * FROM trace_summary_5m WHERE
		//      trace_id IN (Stage 1) GROUP BY trace_id — bounded
		//      to N traces, uses the bloom filter on trace_id.
		//
		// Both stages bypass the raw spans table entirely. End-
		// to-end time on a 7-day window with service filter
		// drops from ~30-60s (raw scan) to <1s.
		`CREATE MATERIALIZED VIEW IF NOT EXISTS trace_service_index_5m
		 ENGINE = AggregatingMergeTree
		 PARTITION BY toDate(time_bucket)
		 ORDER BY (service_name, time_bucket, trace_id)
		 TTL toDate(time_bucket) + INTERVAL 90 DAY
		 SETTINGS index_granularity = 8192
		 AS SELECT
		   service_name,
		   toStartOfInterval(time, INTERVAL 5 MINUTE) AS time_bucket,
		   trace_id,
		   countState()                          AS span_count_state,
		   maxState(time)                        AS last_seen_state
		 FROM spans
		 GROUP BY service_name, time_bucket, trace_id`,
	}
	for _, q := range mvs {
		if err := s.execDDL(ctx, q); err != nil {
			return fmt.Errorf("create MV: %w", err)
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
		// In cluster mode the local table is named with a _local
		// suffix, so we drop both flavours; DROP IF EXISTS makes
		// the second a no-op when single-node.
		dropTarget := "service_summary_5m"
		if s.clusterMode() {
			dropTarget = "service_summary_5m_local"
		}
		if err := s.conn.Exec(ctx, "DROP TABLE IF EXISTS "+dropTarget+s.onCluster()); err != nil {
			return fmt.Errorf("drop old MV for upgrade: %w", err)
		}
		// Re-run the create (mvs[0] from above) now that the old one is gone.
		if err := s.execDDL(ctx, mvs[0]); err != nil {
			return fmt.Errorf("recreate MV with apdex: %w", err)
		}
	}
	log.Println("[chstore] migrations complete")
	return nil
}
