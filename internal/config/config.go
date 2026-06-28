package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// splitCSV trims whitespace and drops empty entries.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

type Config struct {
	Listen     ListenConfig    `yaml:"listen"`
	ClickHouse CHConfig        `yaml:"clickhouse"`
	Retention  RetentionConfig `yaml:"retention"`
	Ingestion  IngestionConfig `yaml:"ingestion"`
	Auth       AuthConfig      `yaml:"auth"`
	Redis      RedisConfig     `yaml:"redis"`
	Logs       LogsConfig      `yaml:"logs"`
	AI         AIConfig        `yaml:"ai"`
	Background BackgroundConfig `yaml:"background"`
	// PublicURL is the operator-facing base URL of this
	// Coremetry deployment (e.g. https://coremetry.bank.local).
	// Notification bodies (Slack / Teams / Zoom / email /
	// generic webhook) include deep-links to the relevant
	// problem / anomaly / incident detail when set. Empty
	// disables the linking — back-compat for deployments
	// where the URL isn't reachable from the recipient's
	// network.
	PublicURL string `yaml:"public_url"`
}

// BackgroundConfig controls the cadence of every internal worker
// loop — anomaly detector, recorder, status probes, etc. Default
// values match what was hard-coded in main.go pre-v0.4.95. Tuning
// these matters at scale: a 2-min detector tick on a busy stack
// adds up to non-trivial CH load, and operators on slow CH
// clusters want to back off; operators on demo deployments
// often want to crank them down so anomalies surface quickly.
//
// Each field is a duration; zero means "use the default". The
// defaults() function applies them so a config file that names
// the section but omits a knob still gets the sensible value.
type BackgroundConfig struct {
	// Interval between anomaly-detector sweeps. Default 2m.
	AnomalyInterval time.Duration `yaml:"anomaly_interval"`
	// Recorder cadence + backfill window. Default 1m / 5m.
	AnomalyRecordInterval time.Duration `yaml:"anomaly_record_interval"`
	AnomalyRecordBackfill time.Duration `yaml:"anomaly_record_backfill"`
	// SMTP settings refresh TTL. Default 30s — short so the
	// operator's Settings change is picked up on the next
	// alert send without a restart.
	SMTPCacheTTL time.Duration `yaml:"smtp_cache_ttl"`
	// Status probe ceiling — hard timeout on /api/status so a
	// stuck CH/Redis client driver doesn't park the goroutine
	// forever. Default 5s, well above the per-probe 2s timeout.
	StatusProbeTimeout time.Duration `yaml:"status_probe_timeout"`
}

// AIConfig wires the optional AI Copilot. Three providers supported:
//   - "anthropic" (default): APIKey is an `sk-ant-…` key from the
//     Anthropic console.
//   - "github":              APIKey is a GitHub OAuth token (`ghu_…`)
//     with Copilot access; Coremetry exchanges it for a session token
//     and calls api.githubcopilot.com (OpenAI-compatible).
//   - "openai":              Any OpenAI-compatible /v1/chat/completions
//     endpoint. Drives self-hosted local LLMs (Ollama, LM Studio,
//     vLLM, llama.cpp server) and the real OpenAI API. Set BaseURL
//     to your local endpoint (e.g. http://ollama:11434/v1) and
//     APIKey is optional for endpoints that don't gate on it.
//
// Env config is the boot-time default; the admin Settings tab can
// override at runtime, persisted to system_settings.
type AIConfig struct {
	Provider string `yaml:"provider"` // env: COREMETRY_AI_PROVIDER (anthropic|github|openai)
	APIKey   string `yaml:"api_key"`  // env: COREMETRY_AI_API_KEY
	Model    string `yaml:"model"`    // env: COREMETRY_AI_MODEL
	BaseURL  string `yaml:"base_url"` // env: COREMETRY_AI_BASE_URL (openai provider only)
}

// LogsConfig picks which read backend serves /api/logs. Ingest still
// always writes to ClickHouse — this only changes the read path so an
// operator can point Coremetry at an external ES that their existing
// shipping pipeline already populates, without re-indexing.
//
//	backend: clickhouse  → query the local CH `logs` table (default)
//	backend: elasticsearch → query Elasticsearch via Elastic.Addresses
type LogsConfig struct {
	Backend       string   `yaml:"backend"` // "clickhouse" (default) | "elasticsearch"
	Elasticsearch ESConfig `yaml:"elasticsearch"`
}

// ESConfig mirrors logstore.ESConfig. Kept here so the config package
// stays the single source of truth for env-var bindings; main.go copies
// these fields into the logstore.ESConfig at construction time.
type ESConfig struct {
	Addresses          []string `yaml:"addresses"`
	Username           string   `yaml:"username"`
	Password           string   `yaml:"password"`
	APIKey             string   `yaml:"api_key"`
	InsecureSkipVerify bool     `yaml:"insecure_skip_verify"`
	Index              string   `yaml:"index"`
	// MLEnabled turns on the v0.5.120 read-only ML anomaly job
	// poller — Coremetry calls /_ml/anomaly_detectors and ingests
	// significant records into anomaly_events so existing Elastic
	// ML jobs surface on the /anomalies page alongside the native
	// detectors. Read-only against Elastic.
	MLEnabled bool `yaml:"ml_enabled"`
	// MLMinScore overrides the per-record score threshold (default
	// 75 — Elastic's "critical" band; lower brings more noise).
	MLMinScore float64 `yaml:"ml_min_score"`
}

// RedisConfig is fully optional. When URL is empty Coremetry runs in
// single-instance mode: no cache (always misses) and the in-process
// "always-leader" lock — meaning background workers always run.
type RedisConfig struct {
	URL string `yaml:"url"` // e.g. "redis://localhost:6379/0"
}

// AuthConfig drives JWT + initial-admin bootstrap. The HMAC secret is
// generated on first run if neither config nor env supply one — but that
// rotates every restart and invalidates sessions, so production deployments
// should set it explicitly.
type AuthConfig struct {
	JWTSecret       string        `yaml:"jwt_secret"`        // HS256 key — set via COREMETRY_JWT_SECRET in prod
	TokenTTL        time.Duration `yaml:"token_ttl"`         // session lifetime (default 24h)
	InitialAdmin    string        `yaml:"initial_admin"`     // email — seeded if users table is empty
	InitialPassword string        `yaml:"initial_password"`  // bcrypted on first boot
	// AdminReset (COREMETRY_ADMIN_RESET) makes the env creds authoritative for
	// the bootstrap admin: when true, InitialAdmin's password is reconciled
	// from InitialPassword on EVERY boot, even if the users table already has
	// rows. Set once to recover a locked-out admin (then remove), or leave on
	// for GitOps installs where the secret is the source of truth. Default off
	// preserves the seed-once behaviour (UI password rotation survives restart).
	AdminReset      bool          `yaml:"admin_reset"`
	OIDC            OIDCConfig    `yaml:"oidc"`
	TrustedHeader   TrustedHeaderConfig `yaml:"trusted_header"`

	// Demo only — when true, /api/auth/config exposes the initial admin
	// credentials so the login page can pre-fill them. NEVER enable in
	// production: anyone hitting /api/auth/config gets the admin password.
	DemoMode bool `yaml:"demo_mode"`
}

// TrustedHeaderConfig wires the oauth2-proxy / IAP / Cloudflare Access
// pattern: an upstream proxy validates SSO and sets identity headers
// on the request, and Coremetry trusts them without re-doing the
// OIDC dance itself. Common for banks running oauth2-proxy + Dex /
// Keycloak in front of every internal service so each app doesn't
// need its own OIDC client registration.
//
// SECURITY: this mode is only safe behind a proxy that strips +
// re-injects the headers. Anyone able to send a raw request to
// Coremetry on a network where the proxy isn't enforced could
// spoof `X-Auth-Request-Email: admin@bank.com` and become admin.
// TrustedProxies enforces source-IP gating; we refuse to honour
// the headers from any other source.
type TrustedHeaderConfig struct {
	Enabled       bool     `yaml:"enabled"`
	// Header names — defaults match oauth2-proxy (which is the
	// dominant implementation in the OAuth-proxy-fronts-app
	// pattern). Operators using a different proxy override.
	EmailHeader   string   `yaml:"email_header"`     // default "X-Auth-Request-Email"
	UserHeader    string   `yaml:"user_header"`      // default "X-Auth-Request-User"
	GroupsHeader  string   `yaml:"groups_header"`    // default "X-Auth-Request-Groups"
	// AutoProvision: first-sight user lands in the users table
	// with DefaultRole. Without it, an unmapped email returns
	// 403 — admins pre-create accounts.
	AutoProvision bool     `yaml:"auto_provision"`
	DefaultRole   string   `yaml:"default_role"`     // default "viewer"
	// TrustedProxies — CIDR blocks the headers are accepted
	// from. Required when Enabled is true; an empty list with
	// Enabled=true is a config error (the validate step refuses
	// to boot to keep operators from accidentally opening a
	// header-spoofing hole).
	TrustedProxies []string `yaml:"trusted_proxies"`
}

// OIDCConfig is fully optional — when Enabled is false, only local
// username/password auth is offered. Local auth is never disabled, even
// when OIDC is on, so admins always have a fallback path.
type OIDCConfig struct {
	Enabled        bool     `yaml:"enabled"`
	IssuerURL      string   `yaml:"issuer_url"`        // e.g. https://accounts.google.com
	ClientID       string   `yaml:"client_id"`
	ClientSecret   string   `yaml:"client_secret"`
	RedirectURL    string   `yaml:"redirect_url"`      // public URL of /api/auth/oidc/callback
	Scopes         []string `yaml:"scopes"`            // default: ["openid", "email", "profile"]
	DisplayName    string   `yaml:"display_name"`      // shown on login button (default: "SSO")
	DefaultRole    string   `yaml:"default_role"`      // role for first-time OIDC users (default: viewer)
	AllowedDomains []string `yaml:"allowed_domains"`   // optional email-domain whitelist (e.g. ["acme.com"])
}

type ListenConfig struct {
	HTTP string `yaml:"http"`
	GRPC string `yaml:"grpc"`
}

// CHConfig describes the ClickHouse connection. `Addr` accepts either
// a single endpoint or a comma-separated list of seeds (e.g.
// "ch1.example:9440,ch2.example:9440,ch3.example:9440,ch4.example:9440") — driver
// load-balances + fails over across them, so an external N-node
// cluster can be configured without an upstream LB.
//
// `Secure` toggles native-TLS (port 9440); leave false for plain
// 9000.  `InsecureSkipVerify` is the escape hatch for self-signed
// internal certs — set false in production once a CA bundle is in
// place.
type CHConfig struct {
	Addr               string `yaml:"addr"`                 // comma-separated for cluster
	Database           string `yaml:"database"`
	Username           string `yaml:"username"`
	Password           string `yaml:"password"`
	Secure             bool   `yaml:"secure"`               // native-TLS (9440)
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"` // self-signed CA escape hatch
	MaxOpenConns       int    `yaml:"max_open_conns"`
	DialTimeout        string `yaml:"dial_timeout"`

	// ClusterName turns on Distributed-CH mode. When non-empty:
	//   - All DDL gets `ON CLUSTER <ClusterName>` so schema is
	//     applied across every node in the named ZK-coordinated
	//     cluster (cluster definition lives in CH's remote_servers).
	//   - High-volume tables (spans, logs, metric_points, profiles)
	//     are created as `<name>_local` ReplicatedMergeTree on each
	//     shard plus a `<name>` Distributed wrapper that fans out
	//     inserts and queries.
	//   - Materialized views feed the *_local tables; their
	//     Distributed wrappers re-export.
	// When empty (default), single-node MergeTree behaviour is
	// preserved exactly — existing deployments keep working.
	ClusterName string `yaml:"cluster_name"`
	// ReplicaPath is the ZooKeeper / Keeper prefix used for
	// ReplicatedMergeTree path argument. {shard}/{replica} macros
	// are appended automatically. Default: "/clickhouse/tables".
	ReplicaPath string `yaml:"replica_path"`
	// ShardKey: the SQL expression used as the Distributed shard
	// key. Defaults to "rand()" — even distribution, no locality
	// guarantee. Set to "cityHash64(trace_id)" if you want all
	// spans of a trace to land on the same shard (faster joins,
	// at the cost of slightly less even rows-per-shard).
	ShardKey string `yaml:"shard_key"`
	// AllowUnsetCluster (COREMETRY_CH_ALLOW_UNSET_CLUSTER) is the escape hatch
	// for the genuinely-broken external-Distributed-unset state: `spans` is an
	// external Distributed table but ClusterName is empty, so Coremetry can't
	// own spans_local — MV insert-triggers never fire (empty dashboards) and
	// op_group ALTERs can't reach the shards. By default boot HARD-ERRORS there
	// with the cluster to set; set this true to run in degraded mode anyway
	// (raw-spans reads only). v0.8.213.
	AllowUnsetCluster bool `yaml:"allow_unset_cluster"`
}

// Hosts splits Addr on commas and trims surrounding whitespace, so
// the chart can hand us a single env var with multiple seeds.
func (c CHConfig) Hosts() []string { return splitCSV(c.Addr) }

type RetentionConfig struct {
	SpansDays   int `yaml:"spans_days"`
	LogsDays    int `yaml:"logs_days"`
	MetricsDays int `yaml:"metrics_days"`
}

type IngestionConfig struct {
	BatchSize     int           `yaml:"batch_size"`
	BufferSize    int           `yaml:"buffer_size"`
	FlushInterval time.Duration `yaml:"flush_interval"`
	Workers       int           `yaml:"workers"`
}

var defaults = Config{
	Listen: ListenConfig{HTTP: ":8088", GRPC: ":4317"},
	ClickHouse: CHConfig{
		Addr: "127.0.0.1:9000", Database: "coremetry",
		// MaxOpenConns 0 = "derive from the ingest flush fan-out" — see
		// resolveMaxOpenConns. A non-zero value here would shadow that
		// derivation; the v0.8.205 prod incident was exactly that (a
		// hardcoded 10 starved 24+ flushers of connections).
		Username: "default", MaxOpenConns: 0, DialTimeout: "5s",
	},
	Retention:  RetentionConfig{SpansDays: 30, LogsDays: 30, MetricsDays: 7},
	// Defaults tuned for production-grade ingest at ~1B spans/day
	// (12k/sec average, 50k/sec burst). Workers parallelise the CH
	// insert path so a 200ms stall on one flush doesn't queue up
	// behind it. BufferSize 500k gives ~10s of burst headroom even
	// when all workers are mid-flush.
	Ingestion:  IngestionConfig{BatchSize: 10_000, BufferSize: 500_000, FlushInterval: 2 * time.Second, Workers: 8},
	Auth: AuthConfig{
		TokenTTL:        24 * time.Hour,
		InitialAdmin:    "admin@coremetry.local",
		InitialPassword: "admin",
	},
	Background: BackgroundConfig{
		AnomalyInterval:       2 * time.Minute,
		AnomalyRecordInterval: 1 * time.Minute,
		AnomalyRecordBackfill: 5 * time.Minute,
		SMTPCacheTTL:          30 * time.Second,
		StatusProbeTimeout:    5 * time.Second,
	},
}

// applyBackgroundDefaults fills zero-valued duration fields on
// the loaded BackgroundConfig with their canonical defaults.
// Called after YAML parse so a partial config (e.g. only
// AnomalyInterval set) keeps reasonable values for the rest.
func applyBackgroundDefaults(b *BackgroundConfig) {
	if b.AnomalyInterval == 0 {
		b.AnomalyInterval = defaults.Background.AnomalyInterval
	}
	if b.AnomalyRecordInterval == 0 {
		b.AnomalyRecordInterval = defaults.Background.AnomalyRecordInterval
	}
	if b.AnomalyRecordBackfill == 0 {
		b.AnomalyRecordBackfill = defaults.Background.AnomalyRecordBackfill
	}
	if b.SMTPCacheTTL == 0 {
		b.SMTPCacheTTL = defaults.Background.SMTPCacheTTL
	}
	if b.StatusProbeTimeout == 0 {
		b.StatusProbeTimeout = defaults.Background.StatusProbeTimeout
	}
}

func Load(path string) (*Config, error) {
	cfg := defaults
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, err
		}
	}

	// Environment variable overrides (Docker / k8s friendly)
	if v := os.Getenv("COREMETRY_CH_ADDR"); v != "" {
		cfg.ClickHouse.Addr = v
	}
	if v := os.Getenv("COREMETRY_CH_DATABASE"); v != "" {
		cfg.ClickHouse.Database = v
	}
	if v := os.Getenv("COREMETRY_CH_USERNAME"); v != "" {
		cfg.ClickHouse.Username = v
	}
	if v := os.Getenv("COREMETRY_CH_PASSWORD"); v != "" {
		cfg.ClickHouse.Password = v
	}
	if v := os.Getenv("COREMETRY_CH_SECURE"); v == "true" || v == "1" {
		cfg.ClickHouse.Secure = true
	}
	if v := os.Getenv("COREMETRY_CH_INSECURE_SKIP_VERIFY"); v == "true" || v == "1" {
		cfg.ClickHouse.InsecureSkipVerify = true
	}
	// Distributed-CH overrides — env-only deployment shouldn't
	// have to ship a config.yaml just to flip cluster mode on.
	if v := os.Getenv("COREMETRY_CH_CLUSTER_NAME"); v != "" {
		cfg.ClickHouse.ClusterName = v
	}
	if v := os.Getenv("COREMETRY_CH_REPLICA_PATH"); v != "" {
		cfg.ClickHouse.ReplicaPath = v
	}
	if v := os.Getenv("COREMETRY_CH_SHARD_KEY"); v != "" {
		cfg.ClickHouse.ShardKey = v
	}
	// CH connection pool — env knob (v0.8.205). The pool must exceed the
	// ingest flush fan-out (3 signals × Ingestion.Workers goroutines, each
	// holding a conn during INSERT) plus read-path headroom, or flushers
	// starve each other → "acquire conn timeout" + dropped batches. Leave
	// UNSET to auto-derive from Workers (resolveMaxOpenConns); set explicitly
	// only to cap against the CH server's own max_connections.
	if v := os.Getenv("COREMETRY_CH_MAX_OPEN_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.ClickHouse.MaxOpenConns = n
		}
	}
	if v := os.Getenv("COREMETRY_CH_DIAL_TIMEOUT"); v != "" {
		if _, err := time.ParseDuration(v); err == nil {
			cfg.ClickHouse.DialTimeout = v
		}
	}
	if v := os.Getenv("COREMETRY_CH_ALLOW_UNSET_CLUSTER"); v == "true" || v == "1" {
		cfg.ClickHouse.AllowUnsetCluster = true
	}
	if v := os.Getenv("COREMETRY_HTTP_ADDR"); v != "" {
		cfg.Listen.HTTP = v
	}
	// COREMETRY_PUBLIC_URL — deployment's externally-reachable
	// base URL. Notification bodies include deep links when set.
	// e.g. https://coremetry.bank.local
	if v := os.Getenv("COREMETRY_PUBLIC_URL"); v != "" {
		cfg.PublicURL = v
	}
	if v := os.Getenv("COREMETRY_GRPC_ADDR"); v != "" {
		cfg.Listen.GRPC = v
	}
	// Ingest capacity (v0.8.204) — tunable WITHOUT a config file, for installs
	// where apps push OTLP straight to :4317. The gRPC receiver returns
	// ResourceExhausted ("buffer full") when the queue can't drain to CH fast
	// enough; raise the buffer (burst headroom, default 500k) and/or workers
	// (parallel CH inserts, default 8) here. If the buffer keeps filling, CH is
	// the bottleneck — scale CH, don't just grow the buffer.
	if v := os.Getenv("COREMETRY_INGEST_BUFFER_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Ingestion.BufferSize = n
		}
	}
	if v := os.Getenv("COREMETRY_INGEST_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Ingestion.Workers = n
		}
	}
	if v := os.Getenv("COREMETRY_INGEST_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Ingestion.BatchSize = n
		}
	}
	if v := os.Getenv("COREMETRY_INGEST_FLUSH_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Ingestion.FlushInterval = d
		}
	}
	if v := os.Getenv("COREMETRY_JWT_SECRET"); v != "" {
		cfg.Auth.JWTSecret = v
	}
	if v := os.Getenv("COREMETRY_INITIAL_ADMIN"); v != "" {
		cfg.Auth.InitialAdmin = v
	}
	if v := os.Getenv("COREMETRY_INITIAL_PASSWORD"); v != "" {
		cfg.Auth.InitialPassword = v
	}
	if v := os.Getenv("COREMETRY_OIDC_ENABLED"); v == "true" || v == "1" {
		cfg.Auth.OIDC.Enabled = true
	}
	if v := os.Getenv("COREMETRY_OIDC_ISSUER_URL"); v != "" {
		cfg.Auth.OIDC.IssuerURL = v
	}
	if v := os.Getenv("COREMETRY_OIDC_CLIENT_ID"); v != "" {
		cfg.Auth.OIDC.ClientID = v
	}
	if v := os.Getenv("COREMETRY_OIDC_CLIENT_SECRET"); v != "" {
		cfg.Auth.OIDC.ClientSecret = v
	}
	if v := os.Getenv("COREMETRY_OIDC_REDIRECT_URL"); v != "" {
		cfg.Auth.OIDC.RedirectURL = v
	}
	// Trusted-header auth env overrides — typical Helm shape is
	// to set TRUSTED_HEADER_ENABLED + TRUSTED_PROXIES from
	// values.yaml so the proxy CIDR is part of the deployment
	// definition, not buried in a ConfigMap.
	if v := os.Getenv("COREMETRY_TRUSTED_HEADER_ENABLED"); v == "true" || v == "1" {
		cfg.Auth.TrustedHeader.Enabled = true
	}
	if v := os.Getenv("COREMETRY_TRUSTED_HEADER_EMAIL"); v != "" {
		cfg.Auth.TrustedHeader.EmailHeader = v
	}
	if v := os.Getenv("COREMETRY_TRUSTED_HEADER_USER"); v != "" {
		cfg.Auth.TrustedHeader.UserHeader = v
	}
	if v := os.Getenv("COREMETRY_TRUSTED_HEADER_GROUPS"); v != "" {
		cfg.Auth.TrustedHeader.GroupsHeader = v
	}
	if v := os.Getenv("COREMETRY_TRUSTED_HEADER_AUTO_PROVISION"); v == "true" || v == "1" {
		cfg.Auth.TrustedHeader.AutoProvision = true
	}
	if v := os.Getenv("COREMETRY_TRUSTED_HEADER_DEFAULT_ROLE"); v != "" {
		cfg.Auth.TrustedHeader.DefaultRole = v
	}
	if v := os.Getenv("COREMETRY_TRUSTED_PROXIES"); v != "" {
		cfg.Auth.TrustedHeader.TrustedProxies = splitCSV(v)
	}
	if v := os.Getenv("COREMETRY_DEMO_MODE"); v == "true" || v == "1" {
		cfg.Auth.DemoMode = true
	}
	if v := os.Getenv("COREMETRY_ADMIN_RESET"); v == "true" || v == "1" {
		cfg.Auth.AdminReset = true
	}
	if v := os.Getenv("COREMETRY_REDIS_URL"); v != "" {
		cfg.Redis.URL = v
	}
	if v := os.Getenv("COREMETRY_LOGS_BACKEND"); v != "" {
		cfg.Logs.Backend = v
	}
	if v := os.Getenv("COREMETRY_ES_ADDRESSES"); v != "" {
		// Comma-separated list, e.g. "http://es-0:9200,http://es-1:9200".
		cfg.Logs.Elasticsearch.Addresses = splitCSV(v)
	}
	if v := os.Getenv("COREMETRY_ES_USERNAME"); v != "" {
		cfg.Logs.Elasticsearch.Username = v
	}
	if v := os.Getenv("COREMETRY_ES_PASSWORD"); v != "" {
		cfg.Logs.Elasticsearch.Password = v
	}
	if v := os.Getenv("COREMETRY_ES_API_KEY"); v != "" {
		cfg.Logs.Elasticsearch.APIKey = v
	}
	if v := os.Getenv("COREMETRY_ES_INDEX"); v != "" {
		cfg.Logs.Elasticsearch.Index = v
	}
	if v := os.Getenv("COREMETRY_ES_INSECURE"); v == "true" || v == "1" {
		cfg.Logs.Elasticsearch.InsecureSkipVerify = true
	}
	if v := os.Getenv("COREMETRY_ES_ML_ENABLED"); v == "true" || v == "1" {
		cfg.Logs.Elasticsearch.MLEnabled = true
	}
	if v := os.Getenv("COREMETRY_ES_ML_MIN_SCORE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.Logs.Elasticsearch.MLMinScore = f
		}
	}
	if v := os.Getenv("COREMETRY_AI_PROVIDER"); v != "" {
		cfg.AI.Provider = v
	}
	if v := os.Getenv("COREMETRY_AI_API_KEY"); v != "" {
		cfg.AI.APIKey = v
	}
	if v := os.Getenv("COREMETRY_AI_MODEL"); v != "" {
		cfg.AI.Model = v
	}
	if v := os.Getenv("COREMETRY_AI_BASE_URL"); v != "" {
		cfg.AI.BaseURL = v
	}
	if cfg.Auth.TokenTTL == 0 {
		cfg.Auth.TokenTTL = defaults.Auth.TokenTTL
	}
	if cfg.Auth.OIDC.Enabled {
		if len(cfg.Auth.OIDC.Scopes) == 0 {
			cfg.Auth.OIDC.Scopes = []string{"openid", "email", "profile"}
		}
		if cfg.Auth.OIDC.DisplayName == "" {
			cfg.Auth.OIDC.DisplayName = "SSO"
		}
		if cfg.Auth.OIDC.DefaultRole == "" {
			cfg.Auth.OIDC.DefaultRole = "viewer"
		}
	}
	// apply defaults for zero values
	if cfg.Ingestion.BatchSize == 0 {
		cfg.Ingestion.BatchSize = defaults.Ingestion.BatchSize
	}
	if cfg.Ingestion.BufferSize == 0 {
		cfg.Ingestion.BufferSize = defaults.Ingestion.BufferSize
	}
	if cfg.Ingestion.FlushInterval == 0 {
		cfg.Ingestion.FlushInterval = defaults.Ingestion.FlushInterval
	}
	if cfg.Ingestion.Workers == 0 {
		cfg.Ingestion.Workers = defaults.Ingestion.Workers
	}
	if cfg.Retention.SpansDays == 0 {
		cfg.Retention.SpansDays = defaults.Retention.SpansDays
	}
	if cfg.Retention.LogsDays == 0 {
		cfg.Retention.LogsDays = defaults.Retention.LogsDays
	}
	if cfg.Retention.MetricsDays == 0 {
		cfg.Retention.MetricsDays = defaults.Retention.MetricsDays
	}
	// CH pool sizing — MUST run AFTER Ingestion.Workers is settled so the
	// fan-out math uses the effective worker count (v0.8.205). An unset pool
	// (0) derives to 3*workers + read headroom; an explicit value is honored
	// but warned about when it's below the fan-out (the operator may be
	// capping against CH max_connections, or may have a footgun).
	fanout := 3 * cfg.Ingestion.Workers
	cfg.ClickHouse.MaxOpenConns = resolveMaxOpenConns(cfg.ClickHouse.MaxOpenConns, cfg.Ingestion.Workers)
	if cfg.ClickHouse.MaxOpenConns < fanout {
		log.Printf("[config] WARNING: ch.max_open_conns=%d is below the ingest flush fan-out "+
			"(3 signals × %d workers = %d). Flushers will contend for connections under load → "+
			"'acquire conn timeout' + dropped batches. Raise COREMETRY_CH_MAX_OPEN_CONNS or lower COREMETRY_INGEST_WORKERS.",
			cfg.ClickHouse.MaxOpenConns, cfg.Ingestion.Workers, fanout)
	}
	applyBackgroundDefaults(&cfg.Background)
	return &cfg, nil
}

// resolveMaxOpenConns sizes the ClickHouse connection pool to the ingest
// flush fan-out. Each of the 3 signal consumers (spans / logs / metrics)
// runs `workers` flusher goroutines, and every flusher holds a pool
// connection for the duration of its INSERT — sharing the pool with all
// read-path queries. If the pool is smaller than that fan-out the flushers
// starve each other (and the reads), surfacing as the clickhouse-go
// "acquire conn timeout" error and dropped batches.
//
// v0.8.205 prod incident: the config default of 10 shadowed the intended
// sizing, so 3×8=24 flushers fought over 10 connections even at default
// Workers; bumping Workers to 16 made it 48-vs-10 and ~every flush was lost.
//
// An explicit operator value (COREMETRY_CH_MAX_OPEN_CONNS / yaml
// max_open_conns) is honored as-is — the operator may be capping against
// their CH server's max_connections ceiling. When unset (0), derive
// 3*workers plus 8 connections of read-path headroom.
func resolveMaxOpenConns(configured, workers int) int {
	if configured > 0 {
		return configured
	}
	if workers <= 0 {
		workers = 8
	}
	return 3*workers + 8
}
