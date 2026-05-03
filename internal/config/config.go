package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen     ListenConfig    `yaml:"listen"`
	ClickHouse CHConfig        `yaml:"clickhouse"`
	Retention  RetentionConfig `yaml:"retention"`
	Ingestion  IngestionConfig `yaml:"ingestion"`
	Auth       AuthConfig      `yaml:"auth"`
	Redis      RedisConfig     `yaml:"redis"`
}

// RedisConfig is fully optional. When URL is empty Qmetry runs in
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
	JWTSecret       string        `yaml:"jwt_secret"`        // HS256 key — set via QMETRY_JWT_SECRET in prod
	TokenTTL        time.Duration `yaml:"token_ttl"`         // session lifetime (default 24h)
	InitialAdmin    string        `yaml:"initial_admin"`     // email — seeded if users table is empty
	InitialPassword string        `yaml:"initial_password"`  // bcrypted on first boot
	OIDC            OIDCConfig    `yaml:"oidc"`
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

type CHConfig struct {
	Addr         string `yaml:"addr"`
	Database     string `yaml:"database"`
	Username     string `yaml:"username"`
	Password     string `yaml:"password"`
	MaxOpenConns int    `yaml:"max_open_conns"`
	DialTimeout  string `yaml:"dial_timeout"`
}

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
		Addr: "127.0.0.1:9000", Database: "qmetry",
		Username: "default", MaxOpenConns: 10, DialTimeout: "5s",
	},
	Retention:  RetentionConfig{SpansDays: 30, LogsDays: 30, MetricsDays: 7},
	Ingestion:  IngestionConfig{BatchSize: 10_000, BufferSize: 100_000, FlushInterval: 5 * time.Second, Workers: 4},
	Auth: AuthConfig{
		TokenTTL:        24 * time.Hour,
		InitialAdmin:    "admin@qmetry.local",
		InitialPassword: "admin",
	},
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
	if v := os.Getenv("QMETRY_CH_ADDR"); v != "" {
		cfg.ClickHouse.Addr = v
	}
	if v := os.Getenv("QMETRY_CH_DATABASE"); v != "" {
		cfg.ClickHouse.Database = v
	}
	if v := os.Getenv("QMETRY_CH_USERNAME"); v != "" {
		cfg.ClickHouse.Username = v
	}
	if v := os.Getenv("QMETRY_CH_PASSWORD"); v != "" {
		cfg.ClickHouse.Password = v
	}
	if v := os.Getenv("QMETRY_HTTP_ADDR"); v != "" {
		cfg.Listen.HTTP = v
	}
	if v := os.Getenv("QMETRY_GRPC_ADDR"); v != "" {
		cfg.Listen.GRPC = v
	}
	if v := os.Getenv("QMETRY_JWT_SECRET"); v != "" {
		cfg.Auth.JWTSecret = v
	}
	if v := os.Getenv("QMETRY_INITIAL_ADMIN"); v != "" {
		cfg.Auth.InitialAdmin = v
	}
	if v := os.Getenv("QMETRY_INITIAL_PASSWORD"); v != "" {
		cfg.Auth.InitialPassword = v
	}
	if v := os.Getenv("QMETRY_OIDC_ENABLED"); v == "true" || v == "1" {
		cfg.Auth.OIDC.Enabled = true
	}
	if v := os.Getenv("QMETRY_OIDC_ISSUER_URL"); v != "" {
		cfg.Auth.OIDC.IssuerURL = v
	}
	if v := os.Getenv("QMETRY_OIDC_CLIENT_ID"); v != "" {
		cfg.Auth.OIDC.ClientID = v
	}
	if v := os.Getenv("QMETRY_OIDC_CLIENT_SECRET"); v != "" {
		cfg.Auth.OIDC.ClientSecret = v
	}
	if v := os.Getenv("QMETRY_OIDC_REDIRECT_URL"); v != "" {
		cfg.Auth.OIDC.RedirectURL = v
	}
	if v := os.Getenv("QMETRY_REDIS_URL"); v != "" {
		cfg.Redis.URL = v
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
	return &cfg, nil
}
