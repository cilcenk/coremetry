package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/api"
	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/config"
	"github.com/cilcenk/coremetry/internal/consumer"
	"github.com/cilcenk/coremetry/internal/evaluator"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/notify"
	"github.com/cilcenk/coremetry/internal/otlp"
)

//go:embed all:frontend/out
var webFS embed.FS

func main() {
	cfgPath := flag.String("config", "config.yaml", "Path to config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// ── ClickHouse ────────────────────────────────────────────────────────────
	store, err := chstore.New(cfg.ClickHouse, cfg.Retention)
	if err != nil {
		log.Fatalf("clickhouse: %v\n\nMake sure ClickHouse is running:\n  docker run -d --name coremetry-ch -p 9000:9000 -p 8123:8123 clickhouse/clickhouse-server:24.8-alpine", err)
	}
	defer store.Close()

	// ── Batch consumers ───────────────────────────────────────────────────────
	opts := consumer.Options{
		BatchSize:     cfg.Ingestion.BatchSize,
		BufferSize:    cfg.Ingestion.BufferSize,
		FlushInterval: cfg.Ingestion.FlushInterval,
		Workers:       cfg.Ingestion.Workers,
	}
	spanConsumer   := consumer.New("spans",   opts, store.InsertSpans)
	logConsumer    := consumer.New("logs",    opts, store.InsertLogs)
	metricConsumer := consumer.New("metrics", opts, store.InsertMetrics)

	spanConsumer.Start(ctx)
	logConsumer.Start(ctx)
	metricConsumer.Start(ctx)

	// ── OTLP ingester ─────────────────────────────────────────────────────────
	ing := otlp.NewIngester(spanConsumer, logConsumer, metricConsumer)

	// ── gRPC server ───────────────────────────────────────────────────────────
	go func() {
		if err := otlp.StartGRPC(cfg.Listen.GRPC, ing); err != nil {
			log.Printf("[grpc] %v", err)
		}
	}()

	// ── Cache + distributed lock (optional) ───────────────────────────────────
	// When redis.url is empty we get a Noop pair: zero caching + an
	// always-leader lock so background workers still run on this single
	// instance.
	cacheImpl, lockImpl := cache.NewNoop()
	if cfg.Redis.URL != "" {
		c, l, err := cache.New(cfg.Redis.URL)
		if err != nil {
			log.Printf("[cache] redis unavailable — running without cache + always-leader: %v", err)
		} else {
			cacheImpl, lockImpl = c, l
			log.Printf("[cache] redis ready at %s", cfg.Redis.URL)
		}
	}

	// ── Notifier (SMTP-driven email; slack/webhook stubs) ────────────────────
	notifier := notify.New(store)

	// ── Alert evaluator (background — opens & resolves problems) ─────────────
	evalr := evaluator.New(store, time.Minute, lockImpl, notifier)
	go evalr.Start(ctx)

	// ── Anomaly detector (Watchdog-style baseline check) ─────────────────────
	go anomaly.New(store, 2*time.Minute, lockImpl, notifier).Start(ctx)

	// ── Exception inbox refresher ────────────────────────────────────────────
	// Scans recent exception events every minute and upserts each distinct
	// (type, message, service) into exception_groups. Leader-gated so
	// multiple replicas don't redo the same scan.
	go runExceptionRefresher(ctx, store, lockImpl)

	// ── Auth (JWT issuer + initial admin seed) ────────────────────────────────
	authSvc := auth.NewService(cfg.Auth.JWTSecret, cfg.Auth.TokenTTL)
	if err := seedInitialAdmin(ctx, store, cfg.Auth); err != nil {
		log.Printf("[auth] seed initial admin: %v", err)
	}

	// ── Optional OIDC ─────────────────────────────────────────────────────────
	// Discovery failure is non-fatal: we keep local auth working and surface
	// the issue in the log. Operators can fix config and restart.
	var oidcSvc *auth.OIDCService
	if cfg.Auth.OIDC.Enabled {
		var err error
		oidcSvc, err = auth.NewOIDCService(ctx, cfg.Auth.OIDC)
		if err != nil {
			log.Printf("[auth] OIDC disabled — %v", err)
			oidcSvc = nil
		} else {
			log.Printf("[auth] OIDC ready — issuer=%s display=%q", cfg.Auth.OIDC.IssuerURL, oidcSvc.DisplayName())
		}
	}

	// ── Logs read backend (CH default, ES opt-in) ────────────────────────────
	// Ingest still always writes to CH; this only changes /api/logs's read
	// path. ES failure here is fatal so a misconfigured external cluster
	// surfaces at boot, not at the first user query.
	logsStore, err := buildLogStore(cfg, store)
	if err != nil {
		log.Fatalf("logs backend: %v", err)
	}
	log.Printf("[logs] read backend: %s", logsStore.Backend())

	// ── HTTP server (OTLP + API + UI) ─────────────────────────────────────────
	srv := api.NewServer(cfg.Listen.HTTP, ing, store, logsStore, webFS, authSvc, oidcSvc, cacheImpl, notifier)
	if cfg.Auth.DemoMode {
		srv.EnableDemoMode(cfg.Auth.InitialAdmin, cfg.Auth.InitialPassword)
		log.Printf("[auth] DEMO MODE — login page will display + pre-fill admin credentials. DO NOT use in production.")
	}
	go func() {
		if err := srv.Start(); err != nil {
			log.Fatalf("[http] %v", err)
		}
	}()

	log.Println("┌──────────────────────────────────────────────┐")
	log.Println("│         Coremetry APM       — ready           │")
	log.Printf( "│  OTLP/gRPC ingest:    localhost%s         │", cfg.Listen.GRPC)
	log.Printf( "│  Web UI + REST API:   http://localhost%s   │", cfg.Listen.HTTP)
	log.Println("└──────────────────────────────────────────────┘")

	<-ctx.Done()
	log.Println("Shutting down gracefully...")
	spanConsumer.Stop()
	logConsumer.Stop()
	metricConsumer.Stop()
	log.Println("Bye.")
}

// buildLogStore picks the read backend for /api/logs based on config.
// Default is CH (same table the ingest pipeline writes to). When the
// operator selects "elasticsearch" we wrap the external cluster — pings
// it once at construction so a misconfigured ES surfaces immediately.
func buildLogStore(cfg *config.Config, ch *chstore.Store) (logstore.Store, error) {
	switch cfg.Logs.Backend {
	case "", "clickhouse":
		return logstore.NewCH(ch), nil
	case "elasticsearch":
		es := cfg.Logs.Elasticsearch
		return logstore.NewES(logstore.ESConfig{
			Addresses:          es.Addresses,
			Username:           es.Username,
			Password:           es.Password,
			APIKey:             es.APIKey,
			InsecureSkipVerify: es.InsecureSkipVerify,
			Index:              es.Index,
		})
	default:
		return nil, fmt.Errorf("unknown logs backend %q (want clickhouse|elasticsearch)", cfg.Logs.Backend)
	}
}

// runExceptionRefresher polls recent exception events and keeps the
// exception_groups inbox in sync. Cheap (one CH GROUP BY per minute) and
// safe to run while the user is also driving the inbox UI.
func runExceptionRefresher(ctx context.Context, store *chstore.Store, lock cache.Lock) {
	const lockKey = "coremetry:lock:exception-refresher"
	const interval = 60 * time.Second
	// First pass scans the last 24h so an existing install backfills.
	// Subsequent ticks scan a 5-minute trailing window — generous overlap
	// to catch ingest lag, harmless because UpsertExceptionGroup is idempotent.
	since := time.Now().Add(-24 * time.Hour)
	tick := func() {
		ok, err := lock.TryAcquire(ctx, lockKey, 2*interval)
		if err == nil && !ok {
			return
		}
		if err == nil {
			defer lock.Release(ctx, lockKey)
		}
		n, err := store.RefreshExceptionGroups(ctx, since)
		if err != nil {
			log.Printf("[errors-inbox] refresh: %v", err)
			return
		}
		if n > 0 {
			log.Printf("[errors-inbox] refreshed %d groups", n)
		}
		since = time.Now().Add(-5 * time.Minute)
	}

	tick() // immediate backfill on boot
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// seedInitialAdmin creates the bootstrap admin if the users table is empty.
// Subsequent runs are no-ops, so changing initial_password in config has no
// effect once a user exists — that's intentional, real password rotation
// goes through a future user-management UI.
func seedInitialAdmin(ctx context.Context, store *chstore.Store, ac config.AuthConfig) error {
	if ac.InitialAdmin == "" || ac.InitialPassword == "" {
		return nil
	}
	n, err := store.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	hash, err := auth.HashPassword(ac.InitialPassword)
	if err != nil {
		return err
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	u := chstore.User{
		ID:           hex.EncodeToString(id),
		Email:        ac.InitialAdmin,
		PasswordHash: hash,
		Role:         auth.RoleAdmin,
	}
	if err := store.UpsertUser(ctx, u); err != nil {
		return err
	}
	log.Printf("[auth] seeded initial admin %q (change the password via API after first login)", ac.InitialAdmin)
	return nil
}
