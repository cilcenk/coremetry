package main

import (
	"context"
	"crypto/sha256"
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

	agentpkg "github.com/cilcenk/coremetry/internal/agent"
	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/api"
	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chmigrate"
	"github.com/cilcenk/coremetry/internal/cluster"
	"github.com/cilcenk/coremetry/internal/pipeline"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/config"
	"github.com/cilcenk/coremetry/internal/consumer"
	"github.com/cilcenk/coremetry/internal/correlator"
	"github.com/cilcenk/coremetry/internal/evaluator"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/ldap"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/mcp"
	"github.com/cilcenk/coremetry/internal/mcptools"
	"github.com/cilcenk/coremetry/internal/monitor"
	"github.com/cilcenk/coremetry/internal/notify"
	"github.com/cilcenk/coremetry/internal/otlp"
	"github.com/cilcenk/coremetry/internal/sampling"
	"github.com/cilcenk/coremetry/internal/selfobs"
	"github.com/cilcenk/coremetry/internal/sse"
	"github.com/cilcenk/coremetry/internal/elasticml"
	"github.com/cilcenk/coremetry/internal/tempo"
	"github.com/cilcenk/coremetry/internal/templater"
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
//  1. ldflag (-X main.Version=…) — proper build pipeline. This is
//     baked into the binary at compile time so it can't go stale
//     between rebuilds.
//  2. /app/VERSION file — Dockerfile RUN line writes this from
//     the ARG so even a forgotten ldflag still surfaces.
//  3. Default "dev".
//
// v0.5.394 — the COREMETRY_VERSION env var override was removed.
// Operator-reported confusion: a stale env value (in compose .env
// or k8s manifest) would silently mask the actual running binary's
// build tag and the login page + /api/version would report the
// override, not what the image actually is. Removing the override
// makes the version a single source of truth tied to the image
// build itself. Helm operators who want to stamp an alternate
// version can write to /app/VERSION at deploy time via an
// initContainer; the file path is stable.
var Version = "dev"

// v0.5.488 — runMode gates which subsystems boot. Single binary,
// four roles: "all" (default; current monolithic behaviour),
// "ingest" (OTLP receivers + CH writers; no background jobs, no
// admin UI), "api" (HTTP API + SSE; no OTLP, no background
// jobs), "worker" (evaluator + anomaly + topology + notifier; no
// OTLP, no UI, must be replicaCount=1 since these jobs are
// leader-elected via Redis lock but a worker pool of 1 saves the
// lock-contention overhead). Helm chart picks monolithic
// (one Deployment, mode=all) vs distributed (three Deployments
// with mode=ingest/api/worker) via values.yaml.
//
// Older deployments are unchanged: COREMETRY_MODE unset → "all".
type runMode struct {
	ingest, api, worker, agent bool
	name                       string
}

func parseRunMode() runMode {
	m := strings.ToLower(strings.TrimSpace(os.Getenv("COREMETRY_MODE")))
	switch m {
	case "", "all":
		return runMode{ingest: true, api: true, worker: true, agent: true, name: "all"}
	case "ingest":
		return runMode{ingest: true, name: "ingest"}
	case "api":
		return runMode{api: true, name: "api"}
	case "worker":
		return runMode{worker: true, name: "worker"}
	case "agent":
		return runMode{agent: true, name: "agent"}
	default:
		log.Fatalf("invalid COREMETRY_MODE=%q (must be one of: all, ingest, api, worker, agent)", m)
		return runMode{}
	}
}

func init() {
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

	mode := parseRunMode()
	log.Printf("[mode] running as role=%s (ingest=%v api=%v worker=%v)",
		mode.name, mode.ingest, mode.api, mode.worker)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// v0.6.42 — self-observability. Boots an OTel SDK (traces +
	// metrics) when COREMETRY_SELF_OBS_OTLP_ENDPOINT is set,
	// emitting under service.name = "coremetry-<mode>". Frontend
	// traceparent propagation works once the api role has
	// otelhttp.NewHandler wrapping the mux (see api.NewServer
	// below). Skip on ingest mode to avoid self-loop: spans about
	// receiving spans would re-enter the ingester and amplify.
	selfobsMode := mode.name
	if mode.ingest && !mode.api && !mode.worker {
		// pure-ingest pod: leave the SDK off entirely.
		_ = os.Setenv("COREMETRY_SELF_OBS_OTLP_ENDPOINT", "")
	}
	selfobsShutdown := selfobs.Init(ctx, selfobsMode, Version)
	defer func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = selfobsShutdown(sctx)
	}()

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
	// v0.5.488 — only ingest-role pods Start() the OTLP receivers and
	// the associated batch consumers. Consumer objects are still
	// constructed on api/worker pods (with zero-config) so that
	// api.NewServer can take a non-nil Ingester and the stats
	// endpoints (/admin/stats, /api/health queue depths) don't need
	// to nil-check every accessor — they just read 0 on a pod that
	// didn't start the receivers.
	opts := consumer.Options{
		BatchSize:     cfg.Ingestion.BatchSize,
		BufferSize:    cfg.Ingestion.BufferSize,
		FlushInterval: cfg.Ingestion.FlushInterval,
		Workers:       cfg.Ingestion.Workers,
	}
	spanConsumer   := consumer.New("spans",   opts, store.InsertSpans)
	logConsumer    := consumer.New("logs",    opts, store.InsertLogs)
	metricConsumer := consumer.New("metrics", opts, store.InsertMetrics)
	if mode.ingest {
		spanConsumer.Start(ctx)
		logConsumer.Start(ctx)
		metricConsumer.Start(ctx)
	}

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
	// v0.5.488 — sampler + pipeline + gRPC live on ingest pods only.
	// api/worker pods don't see incoming spans so head sampling +
	// drop/enrich rules don't apply to them. The sampler IS still
	// referenced by api.NewServer (for /api/settings/sampling
	// reads) — so we still construct it on api/all, just don't
	// attach it to a non-existent ingester.
	sampler := sampling.New(cfg.Sampling)
	pipelineEng := pipeline.New()
	if mode.ingest {
		// Tail sampling needs a downstream sink: kept-after-buffering
		// spans flow back through the same span consumer the head path
		// uses. Attach BEFORE LoadPersisted so a persisted tail-enabled
		// config spins up a working tail (not a flush-less no-op tail).
		sampler.AttachFlush(func(sp *chstore.Span) bool { return spanConsumer.Add(sp) })
		if err := sampler.LoadPersisted(ctx, store); err != nil {
			log.Printf("[sampling] load persisted: %v", err)
		}
		// v0.5.324 — cross-pod settings sync (see ldap/copilot/tempo/pipeline below).
		go sampler.StartConfigRefresh(ctx, store, 30*time.Second)
		ing.SetSampler(sampler)

		// Ingest-time pipeline (v0.5.263) — operator-defined drop /
		// enrich rules evaluated BEFORE the sampler. Loads its rule
		// set from system_settings at boot; admin PUTs through
		// /api/admin/pipeline-rules re-persist + immediately apply.
		// Load failure is non-fatal (empty rule set → engine is a
		// no-op).
		if err := pipelineEng.LoadPersisted(ctx, store); err != nil {
			log.Printf("[pipeline] load persisted: %v", err)
		}
		go pipelineEng.StartConfigRefresh(ctx, store, 30*time.Second)
		pipelineEng.LogStats()
		ing.SetPipeline(pipelineEng)
		{
			s := sampler.Snapshot()
			log.Printf("[sampling] default=%.2f overrides=%d keepErrors=%v keepRoots=%v",
				s.Default, len(s.Services),
				s.AlwaysKeepErrors != nil && *s.AlwaysKeepErrors,
				s.AlwaysKeepRoots != nil && *s.AlwaysKeepRoots)
		}

		// ── gRPC server ───────────────────────────────────────────────
		go func() {
			if err := otlp.StartGRPC(cfg.Listen.GRPC, ing); err != nil {
				log.Printf("[grpc] %v", err)
			}
		}()
	} else {
		// api/worker: load sampler config snapshot so /api/settings/
		// sampling reads return the current cluster-wide state.
		// LoadPersisted is read-only against system_settings — safe
		// from anywhere.
		if err := sampler.LoadPersisted(ctx, store); err != nil {
			log.Printf("[sampling] load persisted (read-only): %v", err)
		}
		go sampler.StartConfigRefresh(ctx, store, 30*time.Second)
	}

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
	// v0.6.3 — cross-pod SSE bridge. Without this, a worker pod's
	// problem.open event would only reach browsers connected to
	// that same pod (which is never — workers don't take traffic).
	// Passing cacheImpl works for Noop (publishes go to /dev/null,
	// no harm) AND for the Redis-backed cache (real PUBLISH +
	// SUBSCRIBE across the fleet).
	bus.SetBridge(cacheImpl)
	bus.StartBridge(ctx)

	// v0.5.488 — background workers (evaluator/anomaly/correlator/
	// monitor/topology/elastic-ml/exception-refresher) only run on
	// worker-role pods. Each is already lock-gated via Redis so
	// running them on every pod in a monolithic deploy was wasted
	// work; in distributed deploy they live exclusively in the
	// dedicated worker Deployment (replicaCount=1, no leader
	// contention).
	//
	// evalr is hoisted out of the guard so SetLogs below can attach
	// the resolved log store regardless of mode. Non-worker modes
	// construct it but never Start() — keeps the wiring symmetric.
	evalr := evaluator.New(store, time.Minute, lockImpl, notifier)
	if mode.worker {
		// ── Alert evaluator (background — opens & resolves problems) ─────
		go evalr.Start(ctx)

		// ── Topology correlator (incident auto-clustering) ──────────────
		corr := correlator.New(store)
		go corr.Start(ctx)
		store.SetNeighborProvider(corr)

		// ── Anomaly detector (Watchdog-style baseline check) ────────────
		go anomaly.New(store, cfg.Background.AnomalyInterval, lockImpl, notifier).Start(ctx)

		// ── Synthetic monitor runner (HTTP probes + heartbeat absence) ──
		go monitor.New(store, notifier, lockImpl).Start(ctx)

		// ── Exception inbox refresher ───────────────────────────────────
		go runExceptionRefresher(ctx, store, lockImpl)

		// ── Topology aggregator ─────────────────────────────────────────
		topology.New(store, 5*time.Minute, 1*time.Hour, lockImpl).Start(ctx)

		// ── Elastic ML anomaly poller (v0.5.120) ────────────────────────
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
	} else {
		// api/ingest still need the neighbor provider for store reads
		// (e.g. AttachProblemToIncident lookup during writes). Build
		// it but don't Start() the background refresher — reads use
		// the snapshot the worker pod is keeping warm in CH.
		corr := correlator.New(store)
		store.SetNeighborProvider(corr)
	}

	// ── Runbook agent (executes automated runbook steps in an isolated role) ──
	// http/javascript/bash steps run HERE, never in the api/ingest/worker
	// roles. Same store + Redis lock as the worker; the per-execution lock
	// keeps multi-agent claims HA-safe. The all-mode pod runs it too, so
	// monolithic installs execute runbooks without a separate agent pod.
	if mode.agent {
		agentID := os.Getenv("HOSTNAME")
		if agentID == "" {
			agentID = "agent"
		}
		go agentpkg.NewRunner(store, lockImpl, notifier, agentID, 5*time.Second).Start(ctx)
		log.Printf("[agent] runbook automated-step agent enabled (id=%s)", agentID)
	}

	// ── Auth (JWT issuer + initial admin seed) ────────────────────────────────
	authSvc := auth.NewService(cfg.Auth.JWTSecret, cfg.Auth.TokenTTL)
	if err := seedInitialAdmin(ctx, store, cfg.Auth); err != nil {
		log.Printf("[auth] seed initial admin: %v", err)
	}
	seedExampleRunbooks(ctx, store)
	// Custom-role catalog — operator-defined viewer subsets, persisted
	// in system_settings. Failure to load is non-fatal: the service
	// boots with no custom roles and the admin can recreate them later.
	if err := authSvc.LoadPersistedCustomRoles(ctx, store); err != nil {
		log.Printf("[auth] load custom roles: %v", err)
	}
	// v0.5.318 — multi-pod cluster: a role created on pod A
	// wasn't visible on pod B until restart. Background poll
	// closes the gap to ~30s without pub/sub infra.
	go authSvc.StartCustomRoleRefresh(ctx, store, 30*time.Second)
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
	// v0.5.320 — proactive retention enforcer. CH's merge-based
	// TTL drop has a 4h timeout default and won't reclaim disk
	// until partitions are merged. The enforcer DROP PARTITION's
	// directly every 1h for partitions older than the configured
	// horizon. Instant disk reclaim; idempotent on a clean state.
	// v0.5.341 — retention enforcer now Redis-gated. Pre-fix:
	// all replicas ran DROP PARTITION concurrently; CH
	// serialised but the duplicate work + log noise + brief
	// metadata-lock fight added up. Single-instance Noop lock
	// is always-leader, so dev behaviour is unchanged.
	// v0.5.488 — worker-only: this is housekeeping DROP PARTITION,
	// not on any hot path. Living in the worker Deployment is a
	// natural fit.
	if mode.worker {
		go store.StartRetentionEnforcer(ctx, time.Hour, lockImpl)
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

	// Wire the log backend into the evaluator so saved-search
	// log alerts (rules with LogQuery != "") can count matches.
	evalr.SetLogs(logsStore)

	// v0.5.488 — anomaly recorder + Drain templater are worker-role
	// background jobs. They write into anomaly_events / log_templates
	// which api pods read from. Splitting the write side off the
	// read fleet means an api-pod hot-reload doesn't blink the
	// detector clock.
	if mode.worker {
		// ── Anomaly recorder (v0.5.241 — needs logsStore) ───────────────
		// Persists log-pattern + trace-op detections into anomaly_events.
		// log-pattern detector runs through the logstore abstraction so
		// it works against whichever backend is wired (CH or ES); ES
		// path uses _msearch so all curated patterns ship in one HTTP
		// round-trip even at billion-log scale.
		anomaly.NewRecorder(store, logsStore, cfg.Background.AnomalyRecordInterval, cfg.Background.AnomalyRecordBackfill, lockImpl).Start(ctx)

		// ── Drain-3 log template puller (v0.5.244) ───────────────────────
		go templater.New(store, logsStore, 5*time.Minute, 1000, lockImpl).Start(ctx)
	}

	// ── AI Copilot (optional) ────────────────────────────────────────────────
	// Always created — env vars are the boot-time default, DB overrides
	// (saved via Settings → AI Copilot) win on top. Configured() returns
	// false when no key is set; the UI hides the buttons in that case.
	copilotSvc := copilot.New(cfg.AI.Provider, cfg.AI.APIKey, cfg.AI.Model)
	// BaseURL is provider-specific (only "openai" reads it). Apply
	// the env-default before LoadPersisted so runtime overrides
	// from /api/settings/ai still win on top.
	// v0.5.360: SkipTLS env default is false — operator opts in
	// per-deployment via Settings → AI Copilot for self-hosted
	// LLMs behind an enterprise CA.
	copilotSvc.Configure(cfg.AI.Provider, cfg.AI.APIKey, cfg.AI.Model, cfg.AI.BaseURL, false)
	if err := copilotSvc.LoadPersisted(ctx, store); err != nil {
		log.Printf("[copilot] load persisted config: %v", err)
	}
	go copilotSvc.StartConfigRefresh(ctx, store, 30*time.Second)
	if copilotSvc.Configured() {
		p, m, b, _, _ := copilotSvc.Snapshot()
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
	go ldapSvc.StartConfigRefresh(ctx, store, 30*time.Second)
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
	go tempoSvc.StartConfigRefresh(ctx, store, 30*time.Second)
	if tempoSvc.Configured() {
		t := tempoSvc.Snapshot()
		log.Printf("[tempo] external backend enabled (baseUrl=%s authType=%s orgId=%s)",
			t.BaseURL, t.AuthType, t.OrgID)
	}

	// ── Problem AI auto-explainer (v0.5.254) ─────────────────────────────────
	// Every 30s, fills the ai_summary column on newly-opened critical
	// problems via the Copilot. Operator sees "why fired + first
	// checks" pre-baked when they open /problems / /inbox, instead of
	// having to click "✨ Explain" themselves. HA-gated like every
	// other worker so multi-pod runs don't duplicate-call the LLM.
	// Configured()=false (no API key) silently noops the worker.
	// v0.5.488 — worker-only.
	if mode.worker {
		go anomaly.NewProblemExplainer(store, copilotSvc, lockImpl).Start(ctx)
	}

	// ── HTTP server (OTLP + API + UI) ─────────────────────────────────────────
	// Cluster membership service (v0.5.253) — per-pod heartbeat
	// + member listing for /admin/cluster. Always created (Noop
	// cache → single-member view), so the admin page works the
	// same in dev as in a 10-pod K8s deployment.
	clusterSvc := cluster.New(cacheImpl, Version)
	go clusterSvc.Start(ctx)
	log.Printf("[cluster] pod id %s", clusterSvc.MyID())

	srv := api.NewServer(cfg.Listen.HTTP, ing, store, logsStore, webFS, authSvc, oidcSvc, ldapSvc, cacheImpl, notifier, copilotSvc, sampler, bus)
	srv.SetCluster(clusterSvc)
	// v0.6.4 — Model Context Protocol server. Wired on api/all
	// modes only — worker / ingest pods don't take operator
	// traffic so they have no MCP listeners. External LLMs
	// (Claude Desktop, internal copilots) talk to the api fleet
	// the same way browsers do.
	if mode.api {
		mcpSvc := mcp.New("coremetry", Version)
		// v0.6.5 — register the telemetry tools so external LLMs
		// (and our own Copilot) can list_services / search_logs /
		// get_trace / query_metric / list_problems / list_anomalies
		// / get_service_health via tools/call.
		mcptools.Register(mcpSvc, mcptools.Deps{
			Store:    store,
			LogStore: logsStore,
		})
		staticRes, tplRes := mcpSvc.ResourceCount()
		log.Printf("[mcp] server ready (%d tools, %d resources, %d templates, %d prompts)",
			mcpSvc.ToolCount(), staticRes, tplRes, mcpSvc.PromptCount())
		srv.SetMCP(mcpSvc)
	}
	srv.SetPipeline(pipelineEng)
	srv.SetVersion(Version)
	srv.SetBackgroundConfig(cfg.Background)
	srv.SetTempo(tempoSvc)
	// Cross-pod L1 cache invalidation (v0.5.337). Subscribes
	// to the Redis pub/sub channel so a putBranding /
	// putSamplingSettings / etc. on one pod evicts the cached
	// response from EVERY pod's L1 tier within ~50ms — closes
	// the multi-pod staleness gap (was bounded only by the
	// soft TTL, up to 5s + the SWR window).
	srv.StartCacheInvalidation(ctx)
	// Async audit drainer (v0.5.339). Mutation handlers push
	// rows onto a buffered channel; one background goroutine
	// batches them into a single CH INSERT every 200ms. Removes
	// the per-mutation goroutine + single-row insert cost that
	// bottlenecked high-rate admin scripts (bulk alert-rule
	// imports, mass acks).
	srv.StartAuditDrainer(ctx)
	if cfg.Auth.DemoMode {
		// Demo mode auto-signs the visitor in as the configured initial
		// admin so they can poke at every screen, including admin-only
		// surfaces (dashboards, channels, retention). The login page
		// auto-submits these creds — no password prompt.
		srv.EnableDemoMode(cfg.Auth.InitialAdmin, cfg.Auth.InitialPassword)
		log.Printf("[auth] DEMO MODE — login page auto-signs in as %s (admin). DO NOT use in production.", cfg.Auth.InitialAdmin)
	}
	// v0.5.488 — every mode runs the HTTP server. api/all serves the
	// full surface (API + UI + SSE). ingest/worker serve only
	// /api/health + /api/version so Kubernetes readiness probes
	// have something to hit; api.NewServer handles the role guard
	// internally based on the mode label below (passed via env so
	// the api package stays mode-unaware).
	go func() {
		if err := srv.Start(); err != nil {
			log.Fatalf("[http] %v", err)
		}
	}()

	log.Println("┌──────────────────────────────────────────────┐")
	log.Printf( "│         Coremetry APM    — ready (%s)        │", mode.name)
	if mode.ingest {
		log.Printf("│  OTLP/gRPC ingest:    localhost%s         │", cfg.Listen.GRPC)
	}
	if mode.api {
		log.Printf("│  Web UI + REST API:   http://localhost%s   │", cfg.Listen.HTTP)
	} else {
		log.Printf("│  Health-only HTTP:    http://localhost%s   │", cfg.Listen.HTTP)
	}
	log.Println("└──────────────────────────────────────────────┘")

	<-ctx.Done()
	log.Println("Shutting down gracefully...")
	if mode.ingest {
		spanConsumer.Stop()
		logConsumer.Stop()
		metricConsumer.Stop()
	}
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
//
// v0.5.426 — switched from per-tick TryAcquire/Release (alternating
// execution across pods) to true leader designation via
// LeaderHolder. Single pod owns leadership for the worker's
// lifetime + refreshes the lease in the background; ticks just
// check IsLeader and skip when not leader. Other pods sit idle
// instead of executing redundant work.
func runExceptionRefresher(ctx context.Context, store *chstore.Store, lock cache.Lock) {
	const lockKey = "coremetry:lock:exception-refresher"
	const interval = 60 * time.Second
	leader := cache.NewLeaderHolder(lock, lockKey, 90*time.Second)
	leader.Start(ctx)

	// First pass scans the last 24h so an existing install backfills.
	// Subsequent ticks scan a 5-minute trailing window — generous overlap
	// to catch ingest lag, harmless because UpsertExceptionGroup is idempotent.
	since := time.Now().Add(-24 * time.Hour)
	// v0.6.24 — operator-reported: the /problems "Resolved" tab
	// stayed empty forever on installs where nobody clicked Resolve.
	// Auto-resolve any open/acknowledged group whose last occurrence
	// is older than this threshold. 14 days matches Honeycomb's
	// default; Sentry uses 30d which is too lenient for the rate
	// banks file exceptions at. UpsertExceptionGroup's existing
	// regression detector flips a group back to `regressed` if the
	// same exception fires again later — so this is reversible.
	const staleHorizon = 14 * 24 * time.Hour
	// Sweep cadence is generous — the cutoff moves a minute at a
	// time; running this every 6 ticks (6 min) is plenty. Use a
	// modulo on a tick counter rather than a second ticker so the
	// HA leader-holder still gates both paths.
	tickCount := 0
	tick := func() {
		if !leader.IsLeader() {
			return
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

		// Stale-sweep every 6th tick (≈ every 6 min). Hourly
		// would be fine too; 6-min keeps the operator-visible
		// lag tighter for installs that fix a bug today and
		// want the group cleared from the inbox by tomorrow.
		tickCount++
		if tickCount%6 == 0 {
			swept, err := store.AutoResolveStaleExceptionGroups(ctx, staleHorizon)
			if err != nil {
				log.Printf("[errors-inbox] stale auto-resolve: %v", err)
			} else if swept > 0 {
				log.Printf("[errors-inbox] auto-resolved %d stale group(s) (>%v idle)",
					swept, staleHorizon)
			}
		}
	}

	tick() // immediate backfill on boot (idempotent — non-leader skips)
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

// bootstrapAdminID derives a STABLE id for the seeded admin from its email so
// concurrent multi-pod boots (distributed mode) and re-seeds converge on the
// SAME id — ReplacingMergeTree(ORDER BY id) then dedups them to a single row
// instead of leaving N duplicate "admin" rows on /users (operator-reported,
// v0.7.3). Pure — unit-tested in main_test.go.
func bootstrapAdminID(email string) string {
	sum := sha256.Sum256([]byte(email))
	return hex.EncodeToString(sum[:8]) // 16 hex chars — same width as the old random id
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
	u := chstore.User{
		ID:           bootstrapAdminID(ac.InitialAdmin),
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

// exampleRunbooks are the starter runbooks seeded into a FRESH install so the
// /runbooks page demonstrates the feature — and every step kind — out of the
// box. Incident-response themed. Deterministic ids so a concurrent multi-pod
// seed dedups to one copy each (ReplacingMergeTree), same lesson as the
// bootstrap admin. (v0.7.5)
func exampleRunbooks() []chstore.Runbook {
	mk := func(id, title, desc string, steps []chstore.RunbookStep) chstore.Runbook {
		for i := range steps {
			steps[i].ID = fmt.Sprintf("%s-s%d", id, i+1)
			steps[i].Order = i
		}
		return chstore.Runbook{ID: id, Title: title, Description: desc, Steps: steps, Enabled: true, Labels: []string{"example"}, CreatedBy: "system"}
	}
	return []chstore.Runbook{
		mk("example-api-5xx", "API gateway 5xx spike",
			"# When to run\nElevated 5xx error rate on the API gateway.\n\n## Goal\nLocalise the fault, then decide rollback vs scale.",
			[]chstore.RunbookStep{
				{Kind: "manual", Title: "Acknowledge & assess", Instructions: "Open the service's RED dashboard. Is the 5xx **service-wide** or one route? Note the start time and the last deploy."},
				{Kind: "query", Title: "Pull error rate (last 15m)", Instructions: "Confirm the breach and which operations are affected.", Query: "error_rate by operation where service = 'api-gateway'"},
				{Kind: "http", Title: "Page on-call", Instructions: "Notify the on-call channel (edit the URL for your webhook).", Method: "POST", URL: "https://example.com/webhook", Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"text":"API gateway 5xx spike — runbook started"}`},
				{Kind: "manual", Title: "Decide: roll back or scale", Instructions: "If the spike followed a deploy → **roll back**. If it's load-driven → **scale out**. Record the decision here."},
			}),
		mk("example-db-pool", "Database connection pool exhausted",
			"# When to run\nClients see `too many connections` or DB-call timeouts.",
			[]chstore.RunbookStep{
				{Kind: "manual", Title: "Confirm the symptom", Instructions: "Are clients seeing connection timeouts / 'too many connections'?"},
				{Kind: "query", Title: "DB latency & errors", Instructions: "Which service's DB calls are degraded?", Query: "p99_ms by service where db_system != ''"},
				{Kind: "bash", Title: "Restart the offending pod", Instructions: "**Edit the command for your cluster, then remove the leading `echo`.**", Command: "echo 'kubectl rollout restart deploy/<service> -n prod'"},
				{Kind: "manual", Title: "Verify recovery", Instructions: "Connection count back to baseline? Resolve the incident."},
			}),
		mk("example-health-triage", "Service health check & triage",
			"# Quick triage\nProbe an upstream and summarise. Demonstrates the **coremetry-agent** running an `http` step then a `javascript` step automatically.",
			[]chstore.RunbookStep{
				{Kind: "http", Title: "Probe upstream", Instructions: "The agent fetches the upstream and records the status + body.", Method: "GET", URL: "https://example.com"},
				{Kind: "javascript", Title: "Summarise", Instructions: "The agent computes a one-line summary (pure JS — no I/O).", Script: "const healthy = true;\n'triage: upstream ' + (healthy ? 'reachable ✓' : 'DOWN ✗');"},
				{Kind: "manual", Title: "Confirm & close", Instructions: "Eyeball the step outputs above, then close the runbook."},
			}),
	}
}

// seedExampleRunbooks populates the starter runbooks on a FRESH install only
// (no-op once any runbook exists, so operator edits/deletes stick). Idempotent
// across concurrent pods via the deterministic ids in exampleRunbooks.
func seedExampleRunbooks(ctx context.Context, store *chstore.Store) {
	existing, err := store.ListRunbooks(ctx)
	if err != nil {
		log.Printf("[runbooks] seed check: %v", err)
		return
	}
	if len(existing) > 0 {
		return
	}
	rbs := exampleRunbooks()
	for _, rb := range rbs {
		if err := store.UpsertRunbook(ctx, rb); err != nil {
			log.Printf("[runbooks] seed example %q: %v", rb.ID, err)
		}
	}
	log.Printf("[runbooks] seeded %d example runbooks", len(rbs))
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

