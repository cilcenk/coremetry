package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cilcenk/coremetry/internal/acache"
	agentpkg "github.com/cilcenk/coremetry/internal/agent"
	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/api"
	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chmigrate"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/cluster"
	"github.com/cilcenk/coremetry/internal/config"
	"github.com/cilcenk/coremetry/internal/consumer"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/rag"
	"github.com/cilcenk/coremetry/internal/correlator"
	"github.com/cilcenk/coremetry/internal/elasticml"
	"github.com/cilcenk/coremetry/internal/evaluator"
	"github.com/cilcenk/coremetry/internal/ldap"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/mcp"
	"github.com/cilcenk/coremetry/internal/mcptools"
	"github.com/cilcenk/coremetry/internal/monitor"
	"github.com/cilcenk/coremetry/internal/notify"
	"github.com/cilcenk/coremetry/internal/otlp"
	"github.com/cilcenk/coremetry/internal/pipeline"
	"github.com/cilcenk/coremetry/internal/selfobs"
	"github.com/cilcenk/coremetry/internal/sse"
	"github.com/cilcenk/coremetry/internal/templater"
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

// buildAcachePolicy resolves the autocomplete-cache cardinality policy
// (v0.8.80). Operators tune which attribute keys keep ranked values vs. an
// approximate HLL count via two optional CSV envs; unset → the OTel-shaped
// DefaultPolicy. New attributes are a config change, never a code change.
//
//	COREMETRY_ACACHE_LOWCARD_KEYS  — keep top-N ranked values (e.g. http.route)
//	COREMETRY_ACACHE_HIGHCARD_KEYS — HLL count only (e.g. k8s.pod.name)
func buildAcachePolicy() acache.Policy {
	low := splitCSVEnv("COREMETRY_ACACHE_LOWCARD_KEYS")
	high := splitCSVEnv("COREMETRY_ACACHE_HIGHCARD_KEYS")
	if len(low) == 0 && len(high) == 0 {
		return acache.DefaultPolicy()
	}
	return acache.NewStaticPolicy(low, high, acache.CardHLL)
}

// splitCSVEnv reads a comma-separated env var into a trimmed, empties-dropped
// slice (nil when unset).
func splitCSVEnv(name string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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

// runCHSubcommand backs `coremetry ch [--config <path>] "<SQL>"`: load the
// config, run the query against the configured ClickHouse, print tab-separated
// rows, exit. Never starts the server / migrations.
func runCHSubcommand(args []string) {
	fs := flag.NewFlagSet("ch", flag.ExitOnError)
	cfgPath := fs.String("config", "config.yaml", "path to config file")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, `usage: coremetry ch [--config <path>] "<SQL>"`)
		os.Exit(2)
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("ch: load config: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := chstore.RunQuery(ctx, cfg.ClickHouse, strings.Join(rest, " "), os.Stdout); err != nil {
		log.Fatalf("ch: %v", err)
	}
}

func main() {
	// `coremetry ch "<SQL>"` — run a one-off query against the CONFIGURED
	// ClickHouse from inside the pod and print tab-separated rows, then exit.
	// A built-in alternative to bundling a 460 MB clickhouse-client: it reuses
	// the running config's hosts/auth/database, so on an external CH the
	// operator doesn't re-enter the seed list or credentials. Intercepted
	// before flag parsing because `ch` is a positional subcommand, not a flag.
	//   kubectl exec deploy/coremetry -- ./coremetry ch "SELECT count() FROM spans"
	if len(os.Args) > 1 && os.Args[1] == "ch" {
		runCHSubcommand(os.Args[2:])
		return
	}

	cfgPath := flag.String("config", "config.yaml", "Path to config file")

	// Phase-2 migration flags. When --migrate-from is set, run a
	// one-shot day-partitioned bulk copy from a remote single-node
	// CH into this Coremetry's own ClickHouse (typically the new
	// cluster), then exit. The web server is NOT started.
	migrateFrom := flag.String("migrate-from", "", "Source ClickHouse addr for one-shot data migration (e.g. 'old-ch:9000'). When set, runs migration and exits.")
	migrateDB := flag.String("migrate-db", "coremetry", "Source CH database for migration")
	migrateUser := flag.String("migrate-user", "default", "Source CH user for migration")
	migratePass := flag.String("migrate-pass", "", "Source CH password for migration")
	migrateTables := flag.String("migrate-tables", "spans,logs,metric_points,profiles", "Comma-separated tables to migrate")
	migrateDays := flag.Int("migrate-days", 30, "Number of trailing days to migrate")
	// Init-container mode: run schema migration only and exit.
	// Designed for multi-replica deployments where every web pod
	// trying to migrate concurrently causes ZK / DDL races. Run
	// this once as a Job or initContainer; web pods boot with
	// COREMETRY_SKIP_MIGRATE=1 to avoid re-running.
	migrateOnly := flag.Bool("migrate-only", false, "Run ClickHouse schema migration only, then exit. Use as a Kubernetes initContainer / one-shot Job before web pods roll out.")
	// DESTRUCTIVE: drops the configured CH database (every table:
	// spans, logs, metrics, dashboards, audit log, anomaly history,
	// users, …) and exits. Designed for the Helm pre-install /
	// pre-upgrade hook so an operator pointing at an existing
	// external CH can re-deploy "as if from scratch". Honours
	// COREMETRY_CH_RESET_SCHEMA=1 as an env-var alias for ergonomic
	// container args. Idempotent — DROP DATABASE IF EXISTS.
	resetSchema := flag.Bool("reset-schema", false, "DROP the configured CH database and exit. Destroys ALL coremetry data — use only for fresh install hooks.")
	flag.Parse()
	// The --reset-schema FLAG is a one-shot Job (drop + exit; the Helm
	// pre-install hook). The COREMETRY_CH_RESET_SCHEMA ENV is set on the
	// long-running Deployment, so it must drop THEN boot normally to recreate
	// — exiting there just restarts into another drop (CrashLoopBackOff, DB
	// never recreated; v0.8.207 operator-reported). resetViaEnv distinguishes
	// the two so the env path falls through to chstore.New().
	resetViaEnv := false
	if v := os.Getenv("COREMETRY_CH_RESET_SCHEMA"); v == "1" || v == "true" {
		*resetSchema = true
		resetViaEnv = true
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
	} else if mode.name == "all" && strings.TrimSpace(os.Getenv("COREMETRY_SELF_OBS_OTLP_ENDPOINT")) == "" {
		// v0.8.218 — self-observability ON by default in monolithic mode: when
		// the operator sets no endpoint, point it at the pod's OWN OTLP receiver
		// so a single-binary install observes itself with zero config. Only in
		// `all` mode (which runs the receiver locally); api/worker-only pods have
		// no local receiver and pure-ingest is excluded above. The default
		// sampler (0.1) bounds the tiny self-loop where tracing the ingest INSERT
		// re-emits a span. Operators ship to an external collector by setting the
		// env explicitly.
		_ = os.Setenv("COREMETRY_SELF_OBS_OTLP_ENDPOINT", selfObsDefaultEndpoint(cfg.Listen.GRPC))
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
		if resetExitsAfterDrop(resetViaEnv) {
			// --reset-schema flag = one-shot Job: drop + exit, the regular
			// Deployment pod boots next and recreates the schema.
			return
		}
		// COREMETRY_CH_RESET_SCHEMA env on a long-running Deployment: fall
		// through to chstore.New() below so THIS pod recreates the schema and
		// runs. Exiting here would just restart into another drop forever.
		log.Printf("[reset-schema] database dropped — continuing boot to recreate it. " +
			"⚠️  REMOVE COREMETRY_CH_RESET_SCHEMA now, or every pod restart will wipe all data.")
	}

	// ── ClickHouse ────────────────────────────────────────────────────────────
	// v0.8.353 (HA audit yellow) — bounded connect retry instead of instant
	// Fatalf. A CH outage used to CrashLoopBackOff every pod: after the
	// outage ended, kubelet's backoff (up to 5m) kept the fleet dark for
	// minutes MORE, and with maxUnavailable:0 a concurrent rollout could
	// leave zero ready pods (the otelcol zero-addresses shape). We retry
	// inside the startupProbe budget (10s×30 = 5m, v0.8.339) and leave
	// ~1m of headroom for schema migrations after the connect succeeds.
	var store *chstore.Store
	{
		deadline := time.Now().Add(4 * time.Minute)
		attempt := 0
		for {
			attempt++
			var cherr error
			store, cherr = chstore.New(cfg.ClickHouse, cfg.Retention)
			if cherr == nil {
				if attempt > 1 {
					log.Printf("[chstore] connected after %d attempts", attempt)
				}
				break
			}
			if time.Now().After(deadline) {
				log.Fatalf("clickhouse (gave up after %d attempts over 4m): %v\n\nMake sure ClickHouse is running:\n  docker run -d --name coremetry-ch -p 9000:9000 -p 8123:8123 clickhouse/clickhouse-server:24.8-alpine", attempt, cherr)
			}
			log.Printf("[chstore] connect attempt %d failed (%v) — retrying in 10s", attempt, cherr)
			time.Sleep(10 * time.Second)
		}
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
		// v0.8.355 (HA 🟡#1) — byte budget alongside the item cap: fat
		// log bodies during a CH stall used to grow 5×500k items into
		// multi-GB and OOMKill the pod (losing EVERYTHING uncounted).
		ByteBudget: int64(cfg.Ingestion.ByteBudgetMB) * 1024 * 1024,
	}
	spanConsumer := consumer.NewSized("spans", opts, chstore.SpanApproxBytes, store.InsertSpans)
	logConsumer := consumer.NewSized("logs", opts, chstore.LogApproxBytes, store.InsertLogs)
	metricConsumer := consumer.NewSized("metrics", opts, chstore.MetricPointApproxBytes, store.InsertMetrics)
	// v0.8.328 — OTLP metric exemplars (cross-signal pivot). Same batching
	// machinery + flush cadence as the metric rows they arrived with.
	exemplarConsumer := consumer.NewSized("exemplars", opts, chstore.ExemplarRowApproxBytes, store.InsertExemplars)
	// v0.8.329 — OTel span links (cross-signal pivot Phase 1b). Same batching
	// machinery + flush cadence as the spans they arrived with; rows land in
	// span_links and fan into span_links_reverse via its MV.
	spanLinkConsumer := consumer.NewSized("span_links", opts, chstore.SpanLinkRowApproxBytes, store.InsertSpanLinks)
	// v0.8.336 (HA audit H1) — consumers run on their OWN context, NOT the
	// signal ctx: the drain must begin only AFTER the OTLP servers stop
	// accepting. On the signal ctx the loops started draining at SIGTERM
	// while gRPC/HTTP kept ACKing new Exports into channels nobody read —
	// silent, uncounted loss on every deploy.
	pipeCtx, pipeCancel := context.WithCancel(context.Background())
	defer pipeCancel()
	if mode.ingest {
		spanConsumer.Start(pipeCtx)
		logConsumer.Start(pipeCtx)
		metricConsumer.Start(pipeCtx)
		exemplarConsumer.Start(pipeCtx)
		spanLinkConsumer.Start(pipeCtx)
	}

	// ── OTLP ingester ─────────────────────────────────────────────────────────
	// grpcHandle stays nil on non-ingest roles; GRPCHandle.Shutdown is
	// nil-safe, so the ordered teardown below needs no role branching.
	var grpcHandle *otlp.GRPCHandle
	ing := otlp.NewIngester(spanConsumer, logConsumer, metricConsumer)
	ing.SetExemplars(exemplarConsumer)
	// require_trace_context default true — a stored exemplar exists to be
	// clicked through to its trace (v0.8.328).
	ing.SetExemplarPolicy(cfg.Exemplars.RequireTraceContext)
	ing.SetExemplarCap(cfg.Exemplars.MaxPerSeriesPerMinute)
	// v0.8.329 — span links: no policy knob (an empty/all-zero linked trace
	// id is malformed per OTel spec, always dropped + counted).
	ing.SetSpanLinks(spanLinkConsumer)

	// ── Autocomplete cache (acache, v0.8.80) ──────────────────────────────────
	// Redis-backed picker facets (service / operation / attribute-value names)
	// populated from the ingest path and read by the picker endpoints in
	// microseconds. Empty redis.url → a no-op store (pickers stay on CH). The
	// flusher only runs where spans land (ingest / all); api pods read-only.
	acStore, acErr := acache.NewStoreFromURL(cfg.Redis.URL, acache.Options{Policy: buildAcachePolicy()})
	if acErr != nil {
		log.Printf("[acache] %v — autocomplete cache disabled", acErr)
	} else if acStore.Enabled() {
		log.Printf("[acache] ready — picker autocomplete served from Redis")
	}
	ing.SetAutocomplete(acStore)
	if mode.ingest {
		acStore.Start(ctx)
	}

	// ── Ingest-time pipeline (v0.5.263; in-binary head sampling removed
	// v0.8.73) ──
	// Operator-defined drop / enrich rules. pipeline + gRPC live on ingest
	// pods only; api/worker pods don't see incoming spans. Coremetry now
	// stores 100% of the spans it RECEIVES — sampling, when wanted, is done
	// at the collector (the sample-to-Coremetry / 100%-to-Tempo split).
	pipelineEng := pipeline.New()
	if mode.ingest {
		// Loads its rule set from system_settings at boot; admin PUTs through
		// /api/admin/pipeline-rules re-persist + immediately apply. Load
		// failure is non-fatal (empty rule set → engine is a no-op).
		if err := pipelineEng.LoadPersisted(ctx, store); err != nil {
			log.Printf("[pipeline] load persisted: %v", err)
		}
		go pipelineEng.StartConfigRefresh(ctx, store, 30*time.Second)
		pipelineEng.LogStats()
		ing.SetPipeline(pipelineEng)

		// ── gRPC server ───────────────────────────────────────────────
		var gerr error
		grpcHandle, gerr = otlp.StartGRPC(cfg.Listen.GRPC, ing)
		if gerr != nil {
			// A dead OTLP listener on an ingest pod is not a degraded
			// mode — fail loud so the orchestrator restarts us.
			log.Fatalf("[grpc] %v", gerr)
		}
	}

	// ── Cache + distributed lock (optional) ───────────────────────────────────
	// When redis.url is empty we get a Noop pair: zero caching + an
	// always-leader lock so background workers still run on this single
	// instance.
	cacheImpl, lockImpl := cache.NewNoop()
	redisConnected := false
	if cfg.Redis.URL != "" {
		c, l, err := cache.New(cfg.Redis.URL)
		if err != nil {
			log.Printf("[cache] redis unavailable — running without cache + always-leader: %v", err)
		} else {
			cacheImpl, lockImpl = c, l
			redisConnected = true
			log.Printf("[cache] redis ready at %s", cfg.Redis.URL)
		}
	}
	// v0.8.341 (H4) — switchable indirection, wired ALWAYS. Every consumer
	// below (LeaderHolders, evaluator, anomaly, retention, SSE bridge,
	// cluster membership, api.Server) captures its Cache/Lock at
	// construction; wrapping here lets the Redis re-probe (started after srv
	// exists, below) hot-swap the real impls in after a failed boot ping —
	// pre-fix, a 3s Redis blip at boot condemned the pod to Noop
	// always-leader for its LIFETIME (N pods × duplicate workers, dead L2).
	// Healthy boots wrap the real impl from the start: behavior is
	// unchanged, cost is one atomic pointer load per call.
	cacheSw := cache.NewSwitchableCache(cacheImpl)
	lockSw := cache.NewSwitchableLock(lockImpl)
	cacheImpl, lockImpl = cacheSw, lockSw
	// v0.8.212 — multi-pod HA hazard: Redis was configured (operator wants a
	// distributed leader lock) but the connection failed, so this pod runs the
	// always-leader Noop. In a multi-pod deployment EVERY pod becomes leader →
	// alerts / notifications / topology aggregation / retention run DUPLICATED.
	// Loud boot warning + a /admin/stats flag (SetLockDegraded below).
	lockDegraded := isLockDegraded(cfg.Redis.URL != "", redisConnected)
	if lockDegraded {
		log.Printf("[leader] WARNING: COREMETRY_REDIS_URL is set but Redis is unreachable — " +
			"this pod runs the ALWAYS-LEADER fallback lock. If you run more than one replica, " +
			"background jobs (alerts, notifications, aggregation, retention) are DUPLICATED across " +
			"pods. Restore Redis so exactly one pod holds leadership.")
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
	// v0.8.354 — sustain/cooldown stamps write-through to Redis so a leader
	// failover (or every maxUnavailable:0 deploy) no longer resets ForSec
	// clocks / punches CooldownSec holes. cacheImpl is the Switchable, so
	// the v0.8.344 Noop→Redis hot-swap applies transparently.
	evalr.SetStampCache(cacheImpl)
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

		// ── Service team deriver (owner/sre team from span attrs) ───────
		go runServiceTeamDeriver(ctx, store, lockImpl)

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
	logsInner, err := buildLogStore(cfg, store)
	esBootDegraded := false
	if err != nil {
		// v0.8.347 (HA audit H8) — an unreachable external ES used to be
		// log.Fatalf in EVERY mode: any pod restart during an ES outage
		// (liveness kill, node drain, rollout) turned "logs are down"
		// into "the entire APM is down". ES is an optional READ backend —
		// boot degraded on the CH logstore instead and let the retry
		// loop below (plus any UI settings save) restore ES when it
		// answers again.
		if cfg.Logs.Backend == "elasticsearch" {
			log.Printf("[logs] ELASTICSEARCH UNREACHABLE AT BOOT (%v) — starting DEGRADED on the ClickHouse logstore; will retry ES every 30s", err)
			logsInner = logstore.NewCH(store)
			esBootDegraded = true
		} else {
			log.Fatalf("logs backend: %v", err)
		}
	}
	// v0.8.232 — every consumer holds the Switchable so an admin save on
	// Settings → Elasticsearch swaps the live backend for all of them
	// (API, evaluator, anomaly recorder, templater) without a restart.
	// Env/YAML seeds the manager (the tab shows the effective config);
	// LoadPersisted below overlays any UI-saved blob on top — UI wins.
	logsStore := logstore.NewSwitchable(logsInner)
	logsMgr := logstore.NewESManager(logsStore, logstore.NewCH(store),
		newNamespaceResolver(store), esSettingsFromConfig(cfg))
	if err := logsMgr.LoadPersisted(ctx, store); err != nil {
		log.Printf("[logs] persisted settings load failed (env config stays active): %v", err)
	}
	if esBootDegraded {
		// Retry the env-seeded ES until it answers, then hot-swap it in —
		// UNLESS something else (UI save via ESManager, persisted-blob
		// refresh) already restored an ES backend in the meantime.
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
				if logsStore.Backend() == "elasticsearch" {
					log.Printf("[logs] ES already restored by settings — boot-retry loop exiting")
					return
				}
				retry, rerr := buildLogStore(cfg, store)
				if rerr != nil {
					continue // still down; stay degraded on CH
				}
				logsStore.Swap(retry)
				log.Printf("[logs] ELASTICSEARCH RECOVERED — logs backend restored from the degraded CH fallback")
				return
			}
		}()
	}
	go logsMgr.StartConfigRefresh(ctx, store, 30*time.Second)
	log.Printf("[logs] read backend: %s", logsStore.Backend())

	// Wire the log backend into the evaluator so saved-search
	// log alerts (rules with LogQuery != "") can count matches.
	evalr.SetLogs(logsStore)

	// v0.5.488 — anomaly recorder + Drain templater are worker-role
	// background jobs. They write into anomaly_events / log_templates
	// which api pods read from. Splitting the write side off the
	// read fleet means an api-pod hot-reload doesn't blink the
	// detector clock.
	// v0.8.227 — the log-anomaly side (recorder + Drain templater) is the
	// only worker job that QUERIES the logstore on a tick. At billion-doc ES
	// scale those curated-pattern _msearch / significant_text / sample pulls
	// are the bulk of Coremetry's ES read traffic, so the operator can switch
	// them off with COREMETRY_LOG_ANOMALY_ENABLED=false. Metric anomaly
	// detection (anomaly.New, CH-backed) is unaffected — it never touches ES.
	if mode.worker && cfg.Background.LogAnomalyEnabled {
		// ── Anomaly recorder (v0.5.241 — needs logsStore) ───────────────
		// Persists log-pattern + trace-op detections into anomaly_events.
		// log-pattern detector runs through the logstore abstraction so
		// it works against whichever backend is wired (CH or ES); ES
		// path uses _msearch so all curated patterns ship in one HTTP
		// round-trip even at billion-log scale.
		anomaly.NewRecorder(store, logsStore, cfg.Background.AnomalyRecordInterval, cfg.Background.AnomalyRecordBackfill, lockImpl).Start(ctx)

		// ── Drain-3 log template puller (v0.5.244) ───────────────────────
		go templater.New(store, logsStore, 5*time.Minute, 1000, lockImpl).Start(ctx)
	} else if mode.worker {
		log.Printf("[anomaly] log-pattern recorder + Drain templater DISABLED (COREMETRY_LOG_ANOMALY_ENABLED=false) — no periodic logstore queries; metric anomaly detection unaffected")
	}

	// ── AI Copilot (optional) ────────────────────────────────────────────────
	// Always created — env vars are the boot-time default, DB overrides
	// (saved via Settings → AI Copilot) win on top. Configured() returns
	// false when no key is set; the UI hides the buttons in that case.
	copilotSvc := copilot.New(cfg.AI.Provider, cfg.AI.APIKey, cfg.AI.Model)

	// ── RAG (v0.8.438) — doküman soru-cevap; embedding endpoint'i
	// Settings → AI'dan girilene dek sessizce kapalı. ──────────────
	ragSvc := rag.New()
	// BaseURL is provider-specific (only "openai" reads it). Apply
	// the env-default before LoadPersisted so runtime overrides
	// from /api/settings/ai still win on top.
	// v0.5.360: SkipTLS env default is false — operator opts in
	// per-deployment via Settings → AI Copilot for self-hosted
	// LLMs behind an enterprise CA.
	// wf — env-default enables AI; the persisted blob's "enabled"
	// (LoadPersisted, applied next) overlays on top, so an operator
	// who disabled AI in Settings stays disabled across a restart.
	copilotSvc.Configure(cfg.AI.Provider, cfg.AI.APIKey, cfg.AI.Model, cfg.AI.BaseURL, false, true)
	if err := copilotSvc.LoadPersisted(ctx, store); err != nil {
	if err := ragSvc.LoadPersisted(ctx, store); err != nil {
		log.Printf("[rag] load persisted: %v", err)
	}
	go ragSvc.StartConfigRefresh(ctx, store, 30*time.Second)
		log.Printf("[copilot] load persisted config: %v", err)
	}
	go copilotSvc.StartConfigRefresh(ctx, store, 30*time.Second)
	if copilotSvc.Active() {
		p, m, b, _, _, _ := copilotSvc.Snapshot()
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

	// ── Root-cause synthesizer (rc #2, v0.8.x) ───────────────────────────────
	// Every 30s, pre-computes + persists a ranked root-cause hypothesis per
	// anchor (recent anomalies + critical open problems) via the PURE
	// correlator.Synthesize fuser over the SAME bounded cross-signal evidence
	// the explainer + on-demand /rootcause fan-out use. /anomalies + /problems
	// then render a "Root cause: <suspect> (NN%)" ribbon (rc #3) with no
	// per-row synthesis. Leader-gated + batched like the explainer; no Copilot
	// dependency (deterministic ranking only). v0.8.x — worker-only.
	if mode.worker {
		go anomaly.NewRootCauseSynthesizer(store, lockImpl).Start(ctx)
	}

	// ── HTTP server (OTLP + API + UI) ─────────────────────────────────────────
	// Cluster membership service (v0.5.253) — per-pod heartbeat
	// + member listing for /admin/cluster. Always created (Noop
	// cache → single-member view), so the admin page works the
	// same in dev as in a 10-pod K8s deployment.
	clusterSvc := cluster.New(cacheImpl, Version)
	go clusterSvc.Start(ctx)
	log.Printf("[cluster] pod id %s", clusterSvc.MyID())

	srv := api.NewServer(cfg.Listen.HTTP, ing, store, logsStore, webFS, authSvc, oidcSvc, ldapSvc, cacheImpl, notifier, copilotSvc, bus)
	srv.SetRAG(ragSvc)
	// v0.8.442 — wiki/URL kaynaklarının 30 dk'lık leader-gated senkronu.
	go srv.StartRAGSync(ctx, lockImpl)

	// v0.8.346 (HA audit H6) — the role guard the old comment only claimed:
	// OTLP HTTP routes 501 off the ingest role (a collector mis-pointed at
	// an api pod used to get its Exports 200-OK'd into channels nobody
	// drains), and the dependency-cache warmer runs on api pods only.
	srv.SetRoles(mode.ingest, mode.api)
	srv.SetLockDegraded(lockDegraded) // v0.8.212 — duplicate-worker HA warning on /admin/stats
	// v0.8.341 (H4) — Redis configured but down at boot: background re-probe
	// until it answers, then hot-swap the real cache+lock into the
	// switchables every consumer already holds. On recovery:
	//   - lockDegraded clears on /admin/stats (no pod restart needed);
	//   - LeaderHolders demote off the always-leader Noop on their next
	//     heartbeat and race the REAL lock — the fleet converges from
	//     N leaders to exactly 1 per worker within one refresh cadence;
	//   - the SSE bridge + L1-invalidation subscribers are re-kicked (their
	//     boot-time Subscribe latched onto the Noop's never-delivering
	//     channel; re-calling opens a real Redis subscription — the stale
	//     goroutines idle harmlessly until shutdown).
	// Probe stops after the first success — steady-state Redis failures
	// surface per-call, as they always have.
	if lockDegraded {
		cache.StartRedisReprobe(ctx, 15*time.Second, func() (cache.Cache, cache.Lock, error) {
			return cache.New(cfg.Redis.URL)
		}, cacheSw, lockSw, func() {
			srv.SetLockDegraded(false)
			bus.StartBridge(ctx)
			srv.StartCacheInvalidation(ctx)
		})
	}
	srv.SetLogstoreESManager(logsMgr) // v0.8.232 — Settings → Elasticsearch (UI-managed logs backend)
	srv.SetCluster(clusterSvc)
	srv.SetAutocomplete(acStore) // v0.8.80 — picker fast path; nil-safe, falls back to CH
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
	// putTempoSettings / etc. on one pod evicts the cached
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
	// /api/health + /api/version stay on every role so Kubernetes
	// probes have something to hit (the OTLP-route role guard is
	// wired via srv.SetRoles above, v0.8.346).
	go func() {
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[http] %v", err)
		}
	}()

	log.Println("┌──────────────────────────────────────────────┐")
	log.Printf("│         Coremetry APM    — ready (%s)        │", mode.name)
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
	// v0.8.336 (HA audit H1) — ordered teardown. Sequence matters:
	//   1. Stop ACCEPTING: gRPC GracefulStop (GOAWAY to collectors,
	//      in-flight Exports finish, bounded 10s) + HTTP Shutdown. Before
	//      this fix the servers ran until process exit, ACKing post-
	//      SIGTERM Exports into channels nobody drained — and the abrupt
	//      gRPC cut is the client-side trigger of the otelcol
	//      zero-addresses wedge.
	//   2. THEN cancel the pipeline ctx and Stop ALL FIVE consumers —
	//      exemplars + span_links used to be skipped entirely, racing
	//      their final flush against process exit.
	grpcHandle.Shutdown(10 * time.Second)
	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := srv.Shutdown(sctx); err != nil {
		log.Printf("[http] shutdown: %v", err)
	}
	scancel()
	pipeCancel()
	if mode.ingest {
		spanConsumer.Stop()
		logConsumer.Stop()
		metricConsumer.Stop()
		exemplarConsumer.Stop()
		spanLinkConsumer.Stop()
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
		store, err := logstore.NewES(logstore.ESConfig{
			Addresses:          es.Addresses,
			Username:           es.Username,
			Password:           es.Password,
			APIKey:             es.APIKey,
			InsecureSkipVerify: es.InsecureSkipVerify,
			Index:              es.Index,
			IndexTemplate:      es.IndexTemplate,
			// v0.8.228 — operator-configurable document field map. Empty
			// members fall back to the logstore ECS-ish defaults via
			// ESConfig.defaults(); set them to match the local mapping
			// (e.g. trace_id / @timestamp / message) without re-indexing.
			Fields: logstore.ESFieldMap{
				Timestamp:  es.Fields.Timestamp,
				TraceID:    es.Fields.TraceID,
				SpanID:     es.Fields.SpanID,
				Service:    es.Fields.Service,
				Body:       es.Fields.Message,
				SeverityTx: es.Fields.SeverityText,
				SeverityNo: es.Fields.SeverityNumber,
				Env:        es.Fields.Environment, // v0.8.400 — "" = self-discover
			},
		})
		if err != nil {
			return nil, err
		}
		// v0.8.231 — service→namespace resolver for the {namespace}
		// index-template placeholder, backed by the same span resource
		// attrs the topology soft-clustering uses. TTL-cached so the
		// hot /logs path costs one CH query per 5 minutes, not per
		// request.
		store.NamespaceResolver = newNamespaceResolver(ch)
		return store, nil
	default:
		return nil, fmt.Errorf("unknown logs backend %q (want clickhouse|elasticsearch)", cfg.Logs.Backend)
	}
}

// esSettingsFromConfig maps the env/YAML logs config to the manager's
// boot seed (v0.8.232) so Settings → Elasticsearch shows the effective
// config before any UI save, and an empty secret on PUT preserves the
// env-supplied credential the same way it preserves a UI-saved one.
func esSettingsFromConfig(cfg *config.Config) logstore.ESSettings {
	es := cfg.Logs.Elasticsearch
	backend := cfg.Logs.Backend
	if backend == "" {
		backend = "clickhouse"
	}
	return logstore.ESSettings{
		Backend:            backend,
		Addresses:          es.Addresses,
		Username:           es.Username,
		Password:           es.Password,
		APIKey:             es.APIKey,
		InsecureSkipVerify: es.InsecureSkipVerify,
		Index:              es.Index,
		IndexTemplate:      es.IndexTemplate,
		Fields: logstore.ESFieldMap{
			Timestamp:  es.Fields.Timestamp,
			TraceID:    es.Fields.TraceID,
			SpanID:     es.Fields.SpanID,
			Service:    es.Fields.Service,
			Body:       es.Fields.Message,
			SeverityTx: es.Fields.SeverityText,
			SeverityNo: es.Fields.SeverityNumber,
			Env:        es.Fields.Environment, // v0.8.400 — "" = self-discover
		},
	}
}

// newNamespaceResolver returns a TTL-cached service→namespace lookup
// (5 min) over chstore.GetServiceNamespaces for the ES index-template
// {namespace} placeholder (v0.8.231). 24h attribution window so a
// service quiet for a few hours keeps resolving; the query itself is
// bounded (GROUP BY over LowCardinality service_name, LIMIT 5000,
// max_execution_time 10). On a CH error the previous map is kept and
// the retry waits out the TTL — a resolver hiccup must never hammer CH
// per log query. Unknown service → "" (logstore substitutes "*").
func newNamespaceResolver(ch *chstore.Store) func(context.Context, string) string {
	var (
		mu      sync.Mutex
		m       map[string]string
		fetched time.Time
	)
	return func(ctx context.Context, service string) string {
		mu.Lock()
		defer mu.Unlock()
		if m == nil || time.Since(fetched) > 5*time.Minute {
			nm, err := ch.GetServiceNamespaces(ctx, 24*time.Hour)
			if err != nil {
				log.Printf("[logstore-es] namespace resolve failed (index template uses * until retry): %v", err)
			} else {
				m = nm
			}
			fetched = time.Now()
		}
		return m[service]
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
	// v0.6.24 — auto-resolve any open/acknowledged group whose last
	// occurrence is older than this threshold (the "Resolved tab stays
	// empty forever" fix). Window + rationale live next to exResolveGrace
	// in chstore so both exception-lifecycle timings are co-located +
	// unit-tested. v0.8.x: 14d → 24h (operator: transitions too slow).
	const staleHorizon = chstore.DefaultExceptionStaleHorizon
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

// runServiceTeamDeriver periodically derives each service's owner team
// (ug-team / ug_team) and sre team (sy-team / sy_team) from its span/resource
// attributes and fills the EMPTY service_metadata.owner_team / sre_team fields
// (manual catalog edits win; all other metadata is preserved — the upsert is a
// full-row replace, so PopulateServiceTeamsFromSpans read-merge-writes).
// Leader-gated so it runs once cluster-wide. Populates the /services team
// dropdowns, the catalog chips, and notify team routing automatically.
func runServiceTeamDeriver(ctx context.Context, store *chstore.Store, lock cache.Lock) {
	const lockKey = "coremetry:lock:service-team-deriver"
	const interval = 10 * time.Minute
	const window = 3 * time.Hour
	leader := cache.NewLeaderHolder(lock, lockKey, cache.LeaderTTL(interval))
	leader.Start(ctx)
	tick := func() {
		if !leader.IsLeader() {
			return
		}
		n, err := store.PopulateServiceTeamsFromSpans(ctx, window)
		if err != nil {
			log.Printf("[team-derive] %v", err)
			return
		}
		if n > 0 {
			log.Printf("[team-derive] filled owner/sre team for %d service(s) from span attrs", n)
		}
		// v0.8.436 — namespace rides the same leader-gated tick (one
		// bounded spans scan each; same ownership/pin semantics).
		nn, err := store.PopulateServiceNamespacesFromSpans(ctx, window)
		if err != nil {
			log.Printf("[ns-derive] %v", err)
			return
		}
		if nn > 0 {
			log.Printf("[ns-derive] filled namespace for %d service(s) from span attrs", nn)
		}
	}
	tick() // immediate on boot (idempotent; non-leader skips)
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

// resetExitsAfterDrop decides whether the process exits after a schema reset.
// Pure so it's unit-tested. The --reset-schema FLAG path is a one-shot Job
// (drop + exit; the next Deployment pod recreates). The
// COREMETRY_CH_RESET_SCHEMA ENV path is set on the long-running Deployment, so
// it must NOT exit — it falls through to chstore.New() and recreates the
// schema in the same boot. Exiting on the env path restarts into another drop
// forever (CrashLoopBackOff, DB never recreated — v0.8.207 incident).
func resetExitsAfterDrop(viaEnv bool) bool {
	return !viaEnv
}

// isLockDegraded reports the multi-pod HA hazard: Redis was configured (the
// operator wants a distributed leader lock) but the connection failed, so the
// pod runs the always-leader Noop lock. Pure so the warning condition is
// unit-tested. NOT degraded when Redis was never configured (single-pod /
// dev — always-leader is correct there) nor when Redis connected. v0.8.212.
func isLockDegraded(redisConfigured, redisConnected bool) bool {
	return redisConfigured && !redisConnected
}

// selfObsDefaultEndpoint turns the gRPC listen address into the self-observability
// OTLP target on localhost — ":4317" → "localhost:4317", "0.0.0.0:4317" →
// "localhost:4317". Pure so the default-on wiring is unit-tested. v0.8.218.
func selfObsDefaultEndpoint(grpcListen string) string {
	port := "4317"
	if i := strings.LastIndex(grpcListen, ":"); i >= 0 && i+1 < len(grpcListen) {
		port = grpcListen[i+1:]
	}
	return "localhost:" + port
}

// shouldWriteBootstrapAdmin decides whether seedInitialAdmin writes the admin
// row. Pure so it's unit-tested. Normally we only seed when the users table is
// EMPTY (seed-once; UI password rotation then survives restarts). When `force`
// is set the env creds are authoritative — we reconcile the bootstrap admin's
// password on every boot regardless of existing rows. force is true when
// COREMETRY_ADMIN_RESET is set (operator recovering a locked-out admin or
// managing creds via a secret) OR when demo mode is on (the published demo
// creds must stay valid so the login page can auto-sign-in — otherwise a stale
// DB password breaks the "open straight in" demo flow).
func shouldWriteBootstrapAdmin(userCount int64, force bool) bool {
	return userCount == 0 || force
}

// seedInitialAdmin creates the bootstrap admin when the users table is empty,
// or — when COREMETRY_ADMIN_RESET or demo mode is set — force-reconciles its
// password from the env on every boot. The id is derived from the email
// (bootstrapAdminID) so the reset UPSERTs the SAME row, never a duplicate.
func seedInitialAdmin(ctx context.Context, store *chstore.Store, ac config.AuthConfig) error {
	if ac.InitialAdmin == "" || ac.InitialPassword == "" {
		return nil
	}
	// Demo mode publishes these creds for auto-login, so they must match the DB.
	force := ac.AdminReset || ac.DemoMode
	n, err := store.CountUsers(ctx)
	if err != nil {
		return err
	}
	if !shouldWriteBootstrapAdmin(n, force) {
		return nil
	}
	hash, err := auth.HashPassword(ac.InitialPassword)
	if err != nil {
		return err
	}
	// v0.8.224 — on a force reconcile, reuse the EXISTING admin row's id
	// (resolved by EMAIL) so a pre-v0.7.3 install — whose admin was seeded with
	// a RANDOM id, never migrated — gets its password updated IN PLACE rather
	// than a SECOND row keyed by the deterministic bootstrapAdminID. users is
	// ReplacingMergeTree dedup-by-id, so two ids = two admin rows for one email,
	// and GetUserByEmail (FINAL, LIMIT 1, no ORDER BY) could then keep returning
	// the stale-password row — the exact duplicate-admin symptom v0.7.3 killed.
	adminID := bootstrapAdminID(ac.InitialAdmin)
	if force {
		if existing, lookupErr := store.GetUserByEmail(ctx, ac.InitialAdmin); lookupErr == nil && existing != nil && existing.ID != "" {
			adminID = existing.ID
		}
	}
	u := chstore.User{
		ID:           adminID,
		Email:        ac.InitialAdmin,
		PasswordHash: hash,
		Role:         auth.RoleAdmin,
	}
	if err := store.UpsertUser(ctx, u); err != nil {
		return err
	}
	switch {
	case n > 0 && ac.DemoMode:
		log.Printf("[auth] demo mode — reconciled demo admin %q password from env so the login page can auto-sign-in", ac.InitialAdmin)
	case n > 0 && ac.AdminReset:
		log.Printf("[auth] COREMETRY_ADMIN_RESET — reconciled admin %q password from env "+
			"(remove the flag once you can log in, or keep it for env-managed creds)", ac.InitialAdmin)
	default:
		log.Printf("[auth] seeded initial admin %q (change the password via API after first login)", ac.InitialAdmin)
	}
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

	to := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
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
		ExchangeID:     c.ExchangeID,
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
