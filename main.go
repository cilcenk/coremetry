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
	"strings"
	"syscall"
	"time"

	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/api"
	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chmigrate"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/config"
	"github.com/cilcenk/coremetry/internal/consumer"
	"github.com/cilcenk/coremetry/internal/correlator"
	"github.com/cilcenk/coremetry/internal/evaluator"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/ldap"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/monitor"
	"github.com/cilcenk/coremetry/internal/notify"
	"github.com/cilcenk/coremetry/internal/otlp"
	"github.com/cilcenk/coremetry/internal/sampling"
	"github.com/cilcenk/coremetry/internal/sse"
	"github.com/cilcenk/coremetry/internal/elasticml"
	"github.com/cilcenk/coremetry/internal/tempo"
	"github.com/cilcenk/coremetry/internal/topology"
)

//go:embed all:frontend/dist
var webFS embed.FS

// Version is stamped at build time via -ldflags="-X main.Version=…".
// The Dockerfile picks up the VERSION build-arg (defaults to a
// `git describe --tags`-style string in CI; "dev" for local
// builds without a tag context). Surfaced on the login page so
// operators can match a running instance to a release tag without
// shelling in.
//
// Resolution order at startup (highest wins):
//
//  1. COREMETRY_VERSION env var — runtime override added in
//     v0.5.170. Useful for operators running a pre-built image
//     that was built without --build-arg VERSION=…; Helm chart
//     can stamp the chart's appVersion here without re-building
//     the image. Always wins so an operator can correct a wrong
//     stamp from outside.
//  2. ldflag (-X main.Version=…) — proper build pipeline.
//  3. /app/VERSION file — Dockerfile RUN line writes this from
//     the ARG so even a forgotten ldflag still surfaces.
//  4. Default "dev".
var Version = "dev"

func init() {
	// Env override wins unconditionally — see resolution order
	// above. Empty / whitespace value is ignored so an explicitly
	// blank env var falls through to the next source.
	if v := strings.TrimSpace(os.Getenv("COREMETRY_VERSION")); v != "" {
		Version = v
		return
	}
	if Version != "" && Version != "dev" {
		return
	}
	// Try a few well-known paths — image puts it at /app/VERSION;
	// CWD-based dev builds may have it next to the binary.
	for _, p := range []string{"/app/VERSION", "VERSION", "VERSION.txt"} {
		if b, err := os.ReadFile(p); err == nil {
			v := strings.TrimSpace(string(b))
			if v != "" {
				Version = v
				return
			}
		}
	}
}

func main() {
	cfgPath := flag.String("config", "config.yaml", "Path to config file")

	// Phase-2 migration flags. When --migrate-from is set, run a
	// one-shot day-partitioned bulk copy from a remote single-node
	// CH into this Coremetry's own ClickHouse (typically the new
	// cluster), then exit. The web server is NOT started.
	migrateFrom     := flag.String("migrate-from", "", "Source ClickHouse addr for one-shot data migration (e.g. 'old-ch:9000'). When set, runs migration and exits.")
	migrateDB       := flag.String("migrate-db", "coremetry", "Source CH database for migration")
	migrateUser     := flag.String("migrate-user", "default", "Source CH user for migration")
	migratePass     := flag.String("migrate-pass", "", "Source CH password for migration")
	migrateTables   := flag.String("migrate-tables", "spans,logs,metric_points,profiles", "Comma-separated tables to migrate")
	migrateDays     := flag.Int("migrate-days", 30, "Number of trailing days to migrate")
	// Init-container mode: run schema migration only and exit.
	// Designed for multi-replica deployments where every web pod
	// trying to migrate concurrently causes ZK / DDL races. Run
	// this once as a Job or initContainer; web pods boot with
	// COREMETRY_SKIP_MIGRATE=1 to avoid re-running.
	migrateOnly     := flag.Bool("migrate-only", false, "Run ClickHouse schema migration only, then exit. Use as a Kubernetes initContainer / one-shot Job before web pods roll out.")
	// DESTRUCTIVE: drops the configured CH database (every table:
	// spans, logs, metrics, dashboards, audit log, anomaly history,
	// users, …) and exits. Designed for the Helm pre-install /
	// pre-upgrade hook so an operator pointing at an existing
	// external CH can re-deploy "as if from scratch". Honours
	// COREMETRY_CH_RESET_SCHEMA=1 as an env-var alias for ergonomic
	// container args. Idempotent — DROP DATABASE IF EXISTS.
	resetSchema     := flag.Bool("reset-schema", false, "DROP the configured CH database and exit. Destroys ALL coremetry data — use only for fresh install hooks.")
	flag.Parse()
	if v := os.Getenv("COREMETRY_CH_RESET_SCHEMA"); v == "1" || v == "true" {
		*resetSchema = true
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Reset-schema runs BEFORE chstore.New() — that constructor
	// opens a connection to the target database and re-runs every
	// migration, which would defeat the whole point of dropping.
	// We open our own setup connection, drop, exit. The next pod
	// (the regular coremetry container, not this Job) will boot
	// normally and recreate everything.
	if *resetSchema {
		log.Printf("[reset-schema] DESTRUCTIVE: about to drop database %q on %s", cfg.ClickHouse.Database, cfg.ClickHouse.Addr)
		if err := chstore.ResetSchema(ctx, cfg.ClickHouse); err != nil {
			log.Fatalf("[reset-schema] %v", err)
		}
		return
	}

	// ── ClickHouse ────────────────────────────────────────────────────────────
	store, err := chstore.New(cfg.ClickHouse, cfg.Retention)
	if err != nil {
		log.Fatalf("clickhouse: %v\n\nMake sure ClickHouse is running:\n  docker run -d --name coremetry-ch -p 9000:9000 -p 8123:8123 clickhouse/clickhouse-server:24.8-alpine", err)
	}
	defer store.Close()

	// Init-container mode: chstore.New() above already ran every
	// schema migration. We're done — exit cleanly so a Kubernetes
	// Job / initContainer can mark itself complete and the web
	// rollout can proceed. Web pods then run with their own
	// chstore.New() but the migrations are idempotent (CREATE
	// TABLE IF NOT EXISTS, ALTER … IF NOT EXISTS) so concurrent
	// no-ops are safe; this flag is the recommended path at scale
	// to avoid ZooKeeper / Keeper DDL races on Replicated tables.
	if *migrateOnly {
		log.Printf("[migrate-only] schema migration complete, exiting")
		return
	}

	// One-shot migration: copy historical data from the source CH
	// into the local cluster, then exit. The destination schema
	// has already been created by chstore.New() above (which ran
	// migrate() — possibly into Distributed-CH mode if cluster_name
	// is set in config).
	if *migrateFrom != "" {
		runMigration(ctx, store, *migrateFrom, *migrateDB, *migrateUser, *migratePass, *migrateTables, *migrateDays)
		return
	}

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

	// ── Trace sampling (head; always-keep-errors + always-keep-roots) ──
	// Built from cfg.Sampling. Empty config = no sampling (keep
	// everything), so existing deployments don't change behaviour
	// on upgrade. Setting `sampling.default: 0.1` in config or
	// COREMETRY_SAMPLING_DEFAULT=0.1 cuts probabilistic span volume
	// 90% while preserving every error + root span. The Sampler is
	// hot-swappable via Reload — admin UI / API can adjust live
	// without a process restart.
	sampler := sampling.New(cfg.Sampling)
	// Tail sampling needs a downstream sink: kept-after-buffering
	// spans flow back through the same span consumer the head path
	// uses. Attach BEFORE LoadPersisted so a persisted tail-enabled
	// config spins up a working tail (not a flush-less no-op tail).
	sampler.AttachFlush(func(sp *chstore.Span) bool { return spanConsumer.Add(sp) })
	if err := sampler.LoadPersisted(ctx, store); err != nil {
		log.Printf("[sampling] load persisted: %v", err)
	}
	ing.SetSampler(sampler)
	{
		s := sampler.Snapshot()
		log.Printf("[sampling] default=%.2f overrides=%d keepErrors=%v keepRoots=%v",
			s.Default, len(s.Services),
			s.AlwaysKeepErrors != nil && *s.AlwaysKeepErrors,
			s.AlwaysKeepRoots != nil && *s.AlwaysKeepRoots)
	}

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
	notifier.SetSMTPCacheTTL(cfg.Background.SMTPCacheTTL)
	// Deep-link base URL — set via COREMETRY_PUBLIC_URL or
	// config.yaml. When configured, every problem / anomaly
	// notification body includes a clickable "Open in
	// Coremetry" link so the recipient lands on the relevant
	// detail page in one tap.
	notifier.SetPublicURL(cfg.PublicURL)

	// ── SSE event broker (in-process pub/sub) ────────────────────────────────
	// Producers: evaluator, anomaly detector. Consumers: every
	// browser tab open on /problems, /anomalies, /incidents.
	// The broker auto-fans events to subscribers; React Query
	// invalidates the relevant cache key when the client receives
	// a kind=problem.* / anomaly.* event, so a state change reaches
	// the UI in <1s instead of waiting up to 30s for the next
	// poll.
	bus := sse.NewBroker()
	notifier.SetEventBus(bus)

	// ── Alert evaluator (background — opens & resolves problems) ─────────────
	evalr := evaluator.New(store, time.Minute, lockImpl, notifier)
	go evalr.Start(ctx)

	// ── Topology correlator (incident auto-clustering) ─────────────────────
	// Builds a 1-hop service adjacency map every 5 min from the
	// service-map sample. The store consults it during
	// AttachProblemToIncident so a payment-service timeout and an
	// upstream api-gateway saturation alert end up under one
	// incident the oncall drives end-to-end, instead of two.
	corr := correlator.New(store)
	go corr.Start(ctx)
	store.SetNeighborProvider(corr)

	// ── Anomaly detector (Watchdog-style baseline check) ─────────────────────
	// Cadence is config-driven (cfg.Background.AnomalyInterval) so
	// operators can dial it down on big CH clusters or crank it up
	// on demo deployments without recompiling.
	go anomaly.New(store, cfg.Background.AnomalyInterval, lockImpl, notifier).Start(ctx)

	// ── Anomaly recorder ─────────────────────────────────────────────────────
	// Persists log-pattern + trace-op detections into anomaly_events
	// so the operator can later answer "did this fire in the last
	// hour, even if it has cleared". Without this the /anomalies
	// page only ever shows the live snapshot.
	anomaly.NewRecorder(store, cfg.Background.AnomalyRecordInterval, cfg.Background.AnomalyRecordBackfill, lockImpl).Start(ctx)

	// ── Synthetic monitor runner (HTTP probes + heartbeat absence) ───────────
	// Lock-gated so HA replicas don't double-probe; emits state-change
	// problems through the same notifier path as alert-rule firings.
	go monitor.New(store, notifier, lockImpl).Start(ctx)

	// ── Exception inbox refresher ────────────────────────────────────────────
	// Scans recent exception events every minute and upserts each distinct
	// (type, message, service) into exception_groups. Leader-gated so
	// multiple replicas don't redo the same scan.
	go runExceptionRefresher(ctx, store, lockImpl)

	// ── Topology aggregator ──────────────────────────────────────────────────
	// Pre-aggregates service-level topology edges into
	// topology_edges_5m every 5 minutes. The /topology view reads
	// from there instead of the spans self-join — at billions of
	// spans/day scale the live path is unworkable. Bootstrap
	// backfills the last 1h so the view is populated immediately.
	topology.New(store, 5*time.Minute, 1*time.Hour, lockImpl).Start(ctx)

	// ── Elastic ML anomaly poller (v0.5.120) ─────────────────────────────────
	// Read-only against Elastic — pulls open anomaly-detection
	// jobs' high-score records and upserts them into anomaly_events
	// so they show up on /anomalies alongside the native detectors.
	// Gated on logs.elasticsearch.ml_enabled to keep installs that
	// haven't opted in untouched.
	if cfg.Logs.Elasticsearch.MLEnabled && len(cfg.Logs.Elasticsearch.Addresses) > 0 {
		mlp, err := elasticml.New(elasticml.Config{
			Addresses:          cfg.Logs.Elasticsearch.Addresses,
			Username:           cfg.Logs.Elasticsearch.Username,
			Password:           cfg.Logs.Elasticsearch.Password,
			APIKey:             cfg.Logs.Elasticsearch.APIKey,
			InsecureSkipVerify: cfg.Logs.Elasticsearch.InsecureSkipVerify,
			MinScore:           cfg.Logs.Elasticsearch.MLMinScore,
		}, store, lockImpl)
		if err != nil {
			log.Printf("[elastic-ml] disabled: %v", err)
		} else {
			mlp.Start(ctx)
			log.Printf("[elastic-ml] polling enabled (min_score=%v)", cfg.Logs.Elasticsearch.MLMinScore)
		}
	}

	// ── Auth (JWT issuer + initial admin seed) ────────────────────────────────
	authSvc := auth.NewService(cfg.Auth.JWTSecret, cfg.Auth.TokenTTL)
	if err := seedInitialAdmin(ctx, store, cfg.Auth); err != nil {
		log.Printf("[auth] seed initial admin: %v", err)
	}
	// ── Trusted-header auth (oauth2-proxy / IAP / Cloudflare Access)
	// When enabled, identity headers from an upstream proxy take
	// the place of OIDC. Refuses to boot if trusted_proxies is
	// empty — header-spoofing hole.
	if cfg.Auth.TrustedHeader.Enabled {
		err := authSvc.EnableTrustedHeader(
			auth.TrustedHeaderOptions{
				Enabled:       true,
				EmailHeader:   cfg.Auth.TrustedHeader.EmailHeader,
				UserHeader:    cfg.Auth.TrustedHeader.UserHeader,
				GroupsHeader:  cfg.Auth.TrustedHeader.GroupsHeader,
				AutoProvision: cfg.Auth.TrustedHeader.AutoProvision,
				DefaultRole:   cfg.Auth.TrustedHeader.DefaultRole,
			},
			cfg.Auth.TrustedHeader.TrustedProxies,
			&trustedHeaderUserStore{store: store},
		)
		if err != nil {
			log.Fatalf("[auth] trusted-header init: %v", err)
		}
	}

	// ── Seed SRE preset dashboards (only on a fresh install) ────────────────
	// Idempotent + non-destructive — checks for any existing dashboard
	// and skips if the table isn't empty. Operator can delete or modify
	// presets freely; we never re-seed on subsequent boots.
	if err := store.SeedPresetDashboards(ctx); err != nil {
		log.Printf("[chstore] seed preset dashboards: %v", err)
	}

	// ── Re-apply admin-set retention overrides ──────────────────────────────
	// The retention TTLs were set at table-create time from config.yaml,
	// but operators can override them live via the UI. Re-running ALTER
	// MODIFY TTL on boot ensures a restart doesn't silently fall back
	// to the config defaults.
	if err := store.ApplyPersistedRetention(ctx); err != nil {
		log.Printf("[chstore] apply persisted retention: %v", err)
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

	// ── AI Copilot (optional) ────────────────────────────────────────────────
	// Always created — env vars are the boot-time default, DB overrides
	// (saved via Settings → AI Copilot) win on top. Configured() returns
	// false when no key is set; the UI hides the buttons in that case.
	copilotSvc := copilot.New(cfg.AI.Provider, cfg.AI.APIKey, cfg.AI.Model)
	// BaseURL is provider-specific (only "openai" reads it). Apply
	// the env-default before LoadPersisted so runtime overrides
	// from /api/settings/ai still win on top.
	copilotSvc.Configure(cfg.AI.Provider, cfg.AI.APIKey, cfg.AI.Model, cfg.AI.BaseURL)
	if err := copilotSvc.LoadPersisted(ctx, store); err != nil {
		log.Printf("[copilot] load persisted config: %v", err)
	}
	if copilotSvc.Configured() {
		p, m, b, _ := copilotSvc.Snapshot()
		if b != "" {
			log.Printf("[copilot] AI explain enabled (provider=%s model=%s baseURL=%s)", p, m, b)
		} else {
			log.Printf("[copilot] AI explain enabled (provider=%s model=%s)", p, m)
		}
	}
	// Wire the AI observability sink (v0.5.163). Every Explain
	// call from now on emits an ai_calls row asynchronously so the
	// operator can see "which Explain button gets clicked, by
	// whom, with what latency / token cost" in the /ai page.
	copilotSvc.SetRecorder(aiCallRecorder{store})

	// ── LDAP / AD enterprise auth (optional) ─────────────────────────────────
	ldapSvc := ldap.New()
	if err := ldapSvc.LoadPersisted(ctx, store); err != nil {
		log.Printf("[ldap] load persisted config: %v", err)
	}
	if ldapSvc.Enabled() {
		c := ldapSvc.Snapshot()
		log.Printf("[ldap] enterprise auth enabled (host=%s:%d tls=%v startTLS=%v baseDN=%s)",
			c.Host, c.Port, c.UseTLS, c.StartTLS, c.BaseDN)
	}

	// ── External Tempo backend (optional fallback for trace-by-id) ───────────
	// Disabled by default — Configured() reports true only after
	// the operator fills in the URL via Settings → Tempo. When
	// enabled, getTrace falls back here on a CH miss so operators
	// running Coremetry at low sampling + Tempo at 100% retention
	// can still resolve long-tail trace IDs in the same /trace URL.
	tempoSvc := tempo.New()
	if err := tempoSvc.LoadPersisted(ctx, store); err != nil {
		log.Printf("[tempo] load persisted config: %v", err)
	}
	if tempoSvc.Configured() {
		t := tempoSvc.Snapshot()
		log.Printf("[tempo] external backend enabled (baseUrl=%s authType=%s orgId=%s)",
			t.BaseURL, t.AuthType, t.OrgID)
	}

	// ── HTTP server (OTLP + API + UI) ─────────────────────────────────────────
	srv := api.NewServer(cfg.Listen.HTTP, ing, store, logsStore, webFS, authSvc, oidcSvc, ldapSvc, cacheImpl, notifier, copilotSvc, sampler, bus)
	srv.SetVersion(Version)
	srv.SetBackgroundConfig(cfg.Background)
	srv.SetTempo(tempoSvc)
	if cfg.Auth.DemoMode {
		// Demo mode auto-signs the visitor in as the configured initial
		// admin so they can poke at every screen, including admin-only
		// surfaces (dashboards, channels, retention). The login page
		// auto-submits these creds — no password prompt.
		srv.EnableDemoMode(cfg.Auth.InitialAdmin, cfg.Auth.InitialPassword)
		log.Printf("[auth] DEMO MODE — login page auto-signs in as %s (admin). DO NOT use in production.", cfg.Auth.InitialAdmin)
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

// runMigration is the --migrate-from one-shot path. Resolves each
// requested table to its local-shard name (so `_local` flavour
// in cluster mode), then runs a day-partitioned bulk copy from
// the remote single-node CH into the destination.
//
// The migrator only logs progress and exits non-zero on first
// failure — operators can re-run with the same range, the
// idempotency check (compare counts per day) skips already-
// finished partitions automatically.
func runMigration(ctx context.Context, store *chstore.Store,
	addr, db, user, pass, tables string, days int,
) {
	if days <= 0 {
		log.Fatalf("[migrate] --migrate-days must be > 0")
	}
	tableList := []string{}
	for _, t := range strings.Split(tables, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tableList = append(tableList, t)
		}
	}
	if len(tableList) == 0 {
		log.Fatalf("[migrate] --migrate-tables empty")
	}

	to := time.Now().UTC().Truncate(24 * time.Hour).AddDate(0, 0, 1)
	from := to.AddDate(0, 0, -days)
	plans := make([]chmigrate.Plan, 0, len(tableList))
	for _, t := range tableList {
		// `profiles` partitions on start_time, not time — every other
		// destination table uses `time` (kept aligned across the
		// schema in store.go). Caller can override via flag if a
		// future table has a different column.
		col := "time"
		if t == "profiles" {
			col = "start_time"
		}
		plans = append(plans, chmigrate.Plan{
			Table: t, TimeCol: col, From: from, To: to,
		})
	}

	mig := &chmigrate.Migrator{
		Conn: store.Conn(),
		Source: chmigrate.SourceConfig{
			Addr: addr, Database: db, Username: user, Password: pass,
		},
		LocalTable: store.LocalTableName,
		Progress: func(day time.Time, p chmigrate.Plan, copied uint64, skipped bool) {
			if skipped {
				log.Printf("[migrate] %s %s: skip (%d rows already present)", p.Table, day.Format("2006-01-02"), copied)
			} else {
				log.Printf("[migrate] %s %s: copied %d rows", p.Table, day.Format("2006-01-02"), copied)
			}
		},
	}
	log.Printf("[migrate] %d table(s) × %d day(s) = %d operations", len(plans), days, len(plans)*days)
	if err := mig.Run(ctx, plans); err != nil {
		log.Fatalf("[migrate] %v", err)
	}
	log.Printf("[migrate] done — destination ready, you can now cut traffic over")
}

// trustedHeaderUserStore adapts *chstore.Store onto the
// auth.UserLookup interface so the auth package doesn't take a
// hard dependency on chstore (and the resulting import cycle).
// The two methods translate the chstore.User shape to/from the
// minimal auth.LookupUser triple the trusted-header path needs.
type trustedHeaderUserStore struct{ store *chstore.Store }

func (s *trustedHeaderUserStore) GetUserByEmail(ctx context.Context, email string) (*auth.LookupUser, error) {
	u, err := s.store.GetUserByEmail(ctx, email)
	if err != nil || u == nil {
		return nil, err
	}
	return &auth.LookupUser{ID: u.ID, Email: u.Email, Role: u.Role}, nil
}

func (s *trustedHeaderUserStore) UpsertUser(ctx context.Context, u auth.LookupUser) error {
	// Auto-provisioned users get no password — OIDC pattern.
	// PasswordHash empty + AuthProvider="trusted" means local
	// password login is refused for the row; logout / re-login
	// always re-hits the trusted-header path.
	return s.store.UpsertUser(ctx, chstore.User{
		ID:           u.ID,
		Email:        u.Email,
		Role:         u.Role,
		AuthProvider: "trusted",
	})
}

// aiCallRecorder is the chstore-backed implementation of
// copilot.Recorder (v0.5.163). Sits in main rather than either
// package so neither chstore nor copilot needs to import the
// other — keeps the dependency graph clean. RecordCall runs on a
// goroutine inside copilot.Service so the user's request returns
// the moment the LLM responds; this method just translates the
// shape and writes the row.
type aiCallRecorder struct {
	store *chstore.Store
}

func (r aiCallRecorder) RecordCall(ctx context.Context, c copilot.CallRecord) {
	row := chstore.AICall{
		CreatedAt:      c.CreatedAt.UnixNano(),
		Surface:        c.Surface,
		Provider:       c.Provider,
		Model:          c.Model,
		BaseURL:        c.BaseURL,
		DurationMs:     c.DurationMs,
		InputTokens:    c.InputTokens,
		OutputTokens:   c.OutputTokens,
		Status:         c.Status,
		ErrorMsg:       c.ErrorMsg,
		PromptChars:    c.PromptChars,
		ResponseChars:  c.ResponseChars,
		UserID:         c.UserID,
		UserEmail:      c.UserEmail,
		PromptSample:   c.PromptSample,
		ResponseSample: c.ResponseSample,
	}
	if err := r.store.InsertAICall(ctx, row); err != nil {
		log.Printf("[ai-obs] insert call: %v", err)
	}
}

