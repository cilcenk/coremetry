package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cenk/qmetry/internal/anomaly"
	"github.com/cenk/qmetry/internal/api"
	"github.com/cenk/qmetry/internal/auth"
	"github.com/cenk/qmetry/internal/cache"
	"github.com/cenk/qmetry/internal/chstore"
	"github.com/cenk/qmetry/internal/config"
	"github.com/cenk/qmetry/internal/consumer"
	"github.com/cenk/qmetry/internal/evaluator"
	"github.com/cenk/qmetry/internal/otlp"
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
		log.Fatalf("clickhouse: %v\n\nMake sure ClickHouse is running:\n  docker run -d --name qmetry-ch -p 9000:9000 -p 8123:8123 clickhouse/clickhouse-server:24.8-alpine", err)
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

	// ── Alert evaluator (background — opens & resolves problems) ─────────────
	evalr := evaluator.New(store, time.Minute, lockImpl)
	go evalr.Start(ctx)

	// ── Anomaly detector (Watchdog-style baseline check) ─────────────────────
	go anomaly.New(store, 2*time.Minute, lockImpl).Start(ctx)

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

	// ── HTTP server (OTLP + API + UI) ─────────────────────────────────────────
	srv := api.NewServer(cfg.Listen.HTTP, ing, store, webFS, authSvc, oidcSvc, cacheImpl)
	go func() {
		if err := srv.Start(); err != nil {
			log.Fatalf("[http] %v", err)
		}
	}()

	log.Println("┌──────────────────────────────────────────────┐")
	log.Println("│           Qmetry APM       — ready            │")
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
