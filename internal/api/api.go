package api

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"golang.org/x/sync/singleflight"

	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/cluster"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/ldap"
	"github.com/cilcenk/coremetry/internal/notify"
	"github.com/cilcenk/coremetry/internal/otlp"
	"github.com/cilcenk/coremetry/internal/pipeline"
	"github.com/cilcenk/coremetry/internal/config"
	"github.com/cilcenk/coremetry/internal/profileconv"
	"github.com/cilcenk/coremetry/internal/templater"
	"github.com/cilcenk/coremetry/internal/sampling"
	"github.com/cilcenk/coremetry/internal/mcp"
	"github.com/cilcenk/coremetry/internal/sse"
	"github.com/cilcenk/coremetry/internal/tempo"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type Server struct {
	addr        string
	store       *chstore.Store
	logs        logstore.Store    // read-side abstraction; CH or external ES
	ing         *otlp.Ingester
	webFS       embed.FS
	auth        *auth.Service
	oidc        *auth.OIDCService // nil when SSO disabled
	ldap        *ldap.Service     // always set; Enabled() reports config presence
	cache       cache.Cache       // Noop when Redis isn't configured
	// sf dedupes concurrent upstream calls when a hot cache
	// key misses or enters the SWR refresh window — see
	// cache.go for the multi-tier read path.
	sf          singleflight.Group
	// l1 is the in-process front tier ahead of Redis. Short
	// per-entry TTL (≤5s); catches same-node burst traffic
	// without crossing the network. Sized at 1024 entries —
	// enough for every distinct cache key the API exposes,
	// generous on memory because each entry stores marshaled
	// JSON not raw objects.
	l1          *l1Cache
	// stats records per-tier hit counts and hottest keys so
	// the System page can show whether the multi-tier cache
	// is doing useful work. Exposed via /api/admin/cache-stats.
	stats       *cacheStats
	notify      *notify.Notifier
	copilot     *copilot.Service  // nil when AI key not configured
	sampler     *sampling.Sampler // always set; Snapshot() reports current ratios
	bus         *sse.Broker       // in-process SSE pub/sub for live UI updates
	// tempo is the external Tempo backend (v0.5.208). When
	// configured, getTrace falls back to Tempo on a CH miss so
	// operators running Coremetry at 5% sampling + Tempo at
	// 100% can still resolve long-tail trace IDs in the same
	// /trace?id= URL. nil-safe — every accessor on the service
	// short-circuits when the receiver is nil.
	tempo       *tempo.Service

	// cluster — per-pod heartbeat / membership service (v0.5.253).
	// Always non-nil when Set; the service degenerates to a single-
	// pod view when Redis is absent so handlers don't need to nil-
	// check before calling Members.
	cluster     *cluster.Service

	// pipeline — ingest-time drop / enrich rule engine (v0.5.263).
	// Admin-managed via Settings → Pipeline. May be nil before
	// SetPipeline is called from main(); admin handlers nil-check
	// and return 503.
	pipeline    *pipeline.Engine

	// mcp — Model Context Protocol server (v0.6.4). Exposes
	// Coremetry's tools / resources / prompts to external LLM
	// clients (Claude Desktop, Anthropic API tool-calling,
	// internal copilots). nil-safe: routes are only registered
	// when SetMCP is called from main(). HTTP+SSE transport on
	// /api/mcp/sse + /api/mcp/messages; auth via the existing
	// JWT middleware so role-based access carries into MCP.
	mcp         *mcp.Server

	// Demo deployments only — when true, /api/auth/config returns
	// initial admin credentials so the login page can pre-fill them.
	demoMode      bool
	demoEmail     string
	demoPassword  string

	// Last sample of each ingest queue's accepted counter, used to
	// compute per-second rate on /api/status. Mutex covers the map +
	// the time/value pair atomically; status calls are infrequent so
	// contention is irrelevant.
	rateMu      sync.Mutex
	rateSamples map[string]rateSample

	// Public status-page subscribe rate limit (v0.5.158). Keys are
	// client IPs (remote addr after stripping the :port); values
	// are the unix-second timestamp of the most recent accepted
	// subscribe. Cleared opportunistically on each call; the map
	// stays bounded by the number of distinct subscribers/hour
	// times the window. Lock-protected because the public route
	// has no auth gate.
	subRateMu sync.Mutex
	subRateBy map[string]int64

	// version is the build-time release tag stamped via -ldflags.
	// Surfaced unauthenticated on /api/version so the login page
	// can show it before the operator has a session.
	version string

	// background carries the cadence + timeout knobs (status
	// probe ceiling, SMTP cache TTL, anomaly intervals) lifted
	// out of magic-number land into config in v0.4.95.
	// Optional — zero values fall through to a 5s probe ceiling
	// so the field doesn't need to be set for unit tests that
	// don't touch /api/status.
	background config.BackgroundConfig

	// auditQ + auditDropCount — v0.5.339 async audit writer.
	// Mutation handlers push rows on auditQ; the StartAuditDrainer
	// goroutine batches them into a single CH INSERT every
	// 200ms (or when the channel fills, whichever fires first).
	// auditDropCount tracks rows dropped due to a saturated
	// channel — visible on /admin/cache-stats so the operator
	// can see if the drainer is keeping up.
	auditQ         chan chstore.AuditEntry
	auditDropCount atomic.Int64

	// v0.5.439 — last-good system.clusters snapshot. The
	// /admin/clickhouse topology probe times out intermittently
	// on busy CH (context canceled past the 8s ceiling); without
	// caching, every transient failure flashes the "probe failed"
	// warning banner even though the topology hasn't actually
	// changed. The cache lets a stale-but-fresh-enough result
	// fill in for a failed probe so the operator sees the green
	// cluster banner with a small "stale: N min" pill instead of
	// a warning. Hard misconfig (truly empty system.clusters)
	// still flips to the red banner — only TRANSIENT probe
	// failures are masked.
	clusterNodesMu     sync.RWMutex
	lastClusterNodes   []CHClusterNode
	lastClusterAt      time.Time
}

// SetBackgroundConfig wires the cadence/timeout knobs to the
// Server. Called once from main() after Load() so the status
// probe respects the configured ceiling.
func (s *Server) SetBackgroundConfig(b config.BackgroundConfig) {
	if b.StatusProbeTimeout == 0 {
		b.StatusProbeTimeout = 5 * time.Second
	}
	s.background = b
}

// SetVersion records the build-time tag. Called once from main();
// safe to call before Start() since /api/version is only consulted
// by SPA after the server is listening.
func (s *Server) SetVersion(v string) {
	s.version = v
}

// SetTempo wires the external Tempo client. Always called from
// main() with a non-nil service — Configured() reports whether the
// operator has actually filled in the settings.
func (s *Server) SetTempo(t *tempo.Service) {
	s.tempo = t
}

// SetCluster wires the per-pod heartbeat / membership service
// (v0.5.253). Always called from main() — the service degenerates
// to a single-pod view when Redis isn't configured so handlers
// don't need to nil-check before calling Members.
func (s *Server) SetCluster(c *cluster.Service) {
	s.cluster = c
}

// SetMCP wires the Model Context Protocol server (v0.6.4). Called
// once from main() after the api.Server is constructed. nil is
// valid — leaves the /api/mcp/* routes unregistered.
func (s *Server) SetMCP(m *mcp.Server) {
	s.mcp = m
}

type rateSample struct {
	at    time.Time
	count int64
}

func NewServer(addr string, ing *otlp.Ingester, store *chstore.Store, logs logstore.Store, webFS embed.FS, authSvc *auth.Service, oidcSvc *auth.OIDCService, ldapSvc *ldap.Service, c cache.Cache, n *notify.Notifier, cop *copilot.Service, smp *sampling.Sampler, bus *sse.Broker) *Server {
	return &Server{
		addr: addr, store: store, logs: logs, ing: ing, webFS: webFS,
		auth: authSvc, oidc: oidcSvc, ldap: ldapSvc, cache: c, notify: n, copilot: cop,
		sampler: smp, bus: bus,
		rateSamples: map[string]rateSample{},
		l1:          newL1Cache(1024),
		stats:       newCacheStats(),
		subRateBy:   map[string]int64{},
		// Buffered audit channel. 1024 entries ≈ 30s of headroom
		// at the highest sustained admin-mutation rate we've
		// observed (bulk alert-rule import ~30/s). Channel-full
		// fallback path is logged + counted, never blocks.
		auditQ: make(chan chstore.AuditEntry, 1024),
	}
}

// StartAuditDrainer runs the batched audit-write loop until ctx
// is cancelled. Triggers a flush when either the channel hits
// 64 pending entries or the 200ms tick elapses — whichever
// comes first. Errors are logged but don't tear down the
// drainer; the next tick reattempts.
func (s *Server) StartAuditDrainer(ctx context.Context) {
	go func() {
		const (
			flushSize     = 64
			flushInterval = 200 * time.Millisecond
		)
		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()
		buf := make([]chstore.AuditEntry, 0, flushSize)
		flush := func() {
			if len(buf) == 0 {
				return
			}
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := s.store.AppendAuditBatch(fctx, buf); err != nil {
				log.Printf("[audit] flush %d entries: %v", len(buf), err)
			}
			cancel()
			buf = buf[:0]
		}
		for {
			select {
			case <-ctx.Done():
				// Drain any pending entries on shutdown so an
				// admin's last action doesn't vanish during a
				// graceful exit.
				for {
					select {
					case e := <-s.auditQ:
						buf = append(buf, e)
					default:
						flush()
						return
					}
				}
			case e := <-s.auditQ:
				buf = append(buf, e)
				if len(buf) >= flushSize {
					flush()
				}
			case <-ticker.C:
				flush()
			}
		}
	}()
	log.Printf("[audit] async drainer online — batch=64, interval=200ms")
}

// EnableDemoMode wires the demo credentials returned by /api/auth/config.
// Loud no-op when called with empty credentials so a misconfigured demo
// flag doesn't silently expose nothing.
func (s *Server) EnableDemoMode(email, password string) {
	s.demoMode = true
	s.demoEmail = email
	s.demoPassword = password
}

// editorRoles is the role bundle used by RequireAnyRole on routes
// that admin + editor may both use (dashboards, monitors, alerts,
// incidents, exception triage). Admin-only routes (user mgmt, system
// settings, channels, status page) keep RequireRole for clarity.
var editorRoles = []string{auth.RoleAdmin, auth.RoleEditor}

func (s *Server) Start() error {
	mux := http.NewServeMux()

	// OTLP HTTP
	otlpHandler := otlp.HTTPHandler(s.ing)
	mux.Handle("POST /v1/traces", otlpHandler)
	mux.Handle("POST /v1/logs", otlpHandler)
	mux.Handle("POST /v1/metrics", otlpHandler)

	// Profile ingest (custom — not OTLP, raw pprof bytes)
	mux.HandleFunc("POST /v1/profiles", s.ingestProfile)
	// Pyroscope-compatible ingest path (v0.5.347). Lets
	// existing Grafana Alloy / Pyroscope agents pointed at
	// http://coremetry:8088/ingest publish CPU + alloc + lock
	// profiles without a Coremetry-specific exporter. The
	// handler maps the Pyroscope query-string convention
	// (?name=app.cpu{tags}&from=...&until=...) onto our
	// internal Profile shape, then defers to InsertProfile.
	mux.HandleFunc("POST /ingest", s.pyroscopeIngest)

	// REST API
	mux.HandleFunc("GET /api/services", s.getServices)
	mux.HandleFunc("GET /api/clusters", s.getClusters)
	mux.HandleFunc("GET /api/admin/system-stats", s.getSystemStats)
	// v0.5.328 — ClickHouse self-stats: slow queries, in-flight
	// merges, part-count hotspots, replication queue lag. Admin
	// only — same gate the rest of /admin/* uses.
	mux.HandleFunc("GET /api/admin/clickhouse", auth.RequireRole(auth.RoleAdmin, s.getClickHouseHealth))
	// v0.6.8 — admin-only CH query AI optimizer. Operator pastes
	// raw SQL; server runs it through the Copilot with the
	// SystemPromptCHQueryOptimize template that knows the MV
	// catalogue + the hard-constraint checklist. Returns
	// {optimized, explanation}.
	mux.HandleFunc("POST /api/admin/clickhouse/optimize-query", auth.RequireRole(auth.RoleAdmin, s.copilotOptimizeCHQuery))
	mux.HandleFunc("GET /api/correlations",       s.getCorrelations)
	mux.HandleFunc("GET /api/admin/redis-stats",  s.getRedisStats)
	mux.HandleFunc("GET /api/admin/cache-stats",  auth.RequireRole(auth.RoleAdmin, s.getCacheStats))
	mux.HandleFunc("GET /api/admin/cardinality",  auth.RequireRole(auth.RoleAdmin, s.getCardinality))
	// SSE event stream — long-lived connection, fans out
	// problem.* / anomaly.* events from the in-process bus.
	// EventSource sends the auth cookie automatically since
	// it's same-origin; the global auth middleware enforces
	// session validity on connection establishment.
	if s.bus != nil {
		mux.Handle("GET /api/events", sse.Handler(s.bus))
	}
	// v0.6.4 — Model Context Protocol endpoints. SSE channel for
	// the server→client stream (responses + notifications); POST
	// channel for the client→server JSON-RPC requests. Both gated
	// by the same auth middleware as the rest of /api/*.
	if s.mcp != nil {
		mux.HandleFunc("GET /api/mcp/sse",       s.mcp.HandleSSE)
		mux.HandleFunc("POST /api/mcp/messages", s.mcp.HandleMessage)
	}
	mux.HandleFunc("GET /api/services/{name}/bundle",    s.getServiceBundle)
	mux.HandleFunc("GET /api/services/{name}/structure", s.getServiceStructure)
	mux.HandleFunc("GET /api/services/{name}/clusters",  s.getServiceClusterBreakdown)
	mux.HandleFunc("GET /api/services/{name}/neighbors", s.getServiceNeighbors)
	// v0.6.29 — Service dependency impact scorer ("blast
	// radius"). Open Problem on service X → which OTHER
	// services are seeing broken calls because they invoke X?
	// Surfaced as a chip on the /problems row + tooltip with
	// top N cascading callers.
	mux.HandleFunc("GET /api/services/{name}/blast-radius", s.getServiceBlastRadius)
	mux.HandleFunc("GET /api/topology",                  s.getTopology)
	mux.HandleFunc("GET /api/topology/ops",              s.getTopologyOps)
	mux.HandleFunc("GET /api/topology/service",          s.getServiceTopology)
	// Topology mute (v0.5.176/177): read open to any auth'd user
	// so viewers see the current muted state on the service
	// detail chip; mutation gated to admin+editor — viewer can
	// look, not touch.
	mux.HandleFunc("GET /api/topology/exclude",          s.getTopologyExclude)
	mux.HandleFunc("PUT /api/topology/exclude",          auth.RequireAnyRole(editorRoles, s.putTopologyExclude))
	mux.HandleFunc("GET /api/topology/flows",            s.getRootFlows)
	mux.HandleFunc("GET /api/topology/flow",             s.getFlowTopology)
	mux.HandleFunc("GET /api/topology/drawio",           s.exportTopologyDrawIO)
	mux.HandleFunc("GET /api/topology/edge/instances",   s.getTopologyEdgeInstances)
	mux.HandleFunc("POST /api/slos/autocreate",          auth.RequireRole(auth.RoleAdmin, s.autoCreateSLOs))
	mux.HandleFunc("GET /api/topology/service/drawio",   s.exportServiceTopologyDrawIO)
	mux.HandleFunc("GET /api/topology/flow/drawio",      s.exportFlowTopologyDrawIO)
	mux.HandleFunc("GET /api/service-map", s.getServiceMap)
	mux.HandleFunc("GET /api/endpoints",  s.getEndpoints)
	mux.HandleFunc("GET /api/services/{name}/attrs", s.getServiceAttrs)
	mux.HandleFunc("GET /api/databases",  s.getDatabases)
	mux.HandleFunc("GET /api/databases/detail", s.getDatabaseDetail)
	// Cross-service slow-query catalog (v0.5.165). One row per
	// (service, normalised statement) ordered by total wall-clock
	// time — what's actually worth optimising globally.
	mux.HandleFunc("GET /api/databases/slow-queries", s.getSlowQueriesGlobal)
	mux.HandleFunc("GET /api/databases/oracle",     s.getOracleMetrics)
	mux.HandleFunc("GET /api/databases/postgres",   s.getPostgresMetrics)
	mux.HandleFunc("GET /api/databases/mysql",      s.getMySQLMetrics)
	mux.HandleFunc("GET /api/databases/redis",      s.getRedisMetrics)
	mux.HandleFunc("GET /api/messaging",  s.getMessaging)
	mux.HandleFunc("GET /api/messaging/detail", s.getMessagingDetail)
	mux.HandleFunc("GET /api/services/{name}/backtrace", s.getServiceBacktrace)
	mux.HandleFunc("GET /api/services/{name}/infra",     s.getServiceInfraMetrics)
	mux.HandleFunc("GET /api/services/{name}/runtime",   s.getServiceRuntime)
	mux.HandleFunc("GET /api/services/{name}/db-queries", s.getServiceDBQueries)
	mux.HandleFunc("GET /api/services/{name}/deploys", s.getServiceDeploys)
	// Deploy history with impact deltas — drives the Service
	// detail page's "Recent deploys" panel (v0.5.189).
	mux.HandleFunc("GET /api/services/{name}/deploy-history", s.getDeployHistory)
	mux.HandleFunc("GET /api/services/{name}/metadata", s.getServiceMetadata)
	mux.HandleFunc("PUT /api/services/{name}/metadata", auth.RequireAnyRole(editorRoles, s.putServiceMetadata))
	mux.HandleFunc("GET /api/services-metadata", s.listServiceMetadata)
	mux.HandleFunc("GET /api/services-runtimes",         s.getAllServiceRuntimes)
	mux.HandleFunc("GET /api/services/graph", s.getServiceGraph)
	mux.HandleFunc("GET /api/services/sparklines", s.getServiceSparklines)
	mux.HandleFunc("GET /api/service-names",       s.getServiceNames)
	mux.HandleFunc("GET /api/operation-names",     s.getOperationNames)
	mux.HandleFunc("GET /api/attribute-keys",      s.getAttributeKeys)
	mux.HandleFunc("GET /api/attribute-values",    s.getAttributeValues)
	mux.HandleFunc("GET /api/operations", s.getOperations)
	mux.HandleFunc("GET /api/traces", s.getTraces)
	// CSV export — same filter shape as /api/traces but streams
	// the result as a downloadable CSV. Cap raised to 10k rows
	// (vs the UI's 50/page) since auditors / postmortem flows
	// usually want a fuller slice. Content-Disposition is set
	// so the browser triggers a download rather than rendering.
	mux.HandleFunc("GET /api/traces/export.csv", s.exportTracesCSV)
	mux.HandleFunc("GET /api/traces/aggregate", s.getTraceAggregate)
	// v0.5.264 — trace shape clustering. Groups traces by their
	// sorted-unique (service, operation) fingerprint; surfaces
	// the dominant call-pattern cohorts. Sample-based so the
	// query stays under the 30s ceiling at billion-span scale.
	mux.HandleFunc("GET /api/traces/shapes",    s.getTraceShapes)
	// v0.5.265 — Unified query language (DQL-lite). Operator
	// admin-types pipe-shape queries; backend compiles to a
	// chstore Plan + executes via the same hot path the UI
	// builders use.
	mux.HandleFunc("POST /api/query/run",       auth.RequireRole(auth.RoleAdmin, s.runDQL))
	mux.HandleFunc("GET /api/traces/{id}", s.getTrace)
	// Public-share endpoints — POST mints a token (editor+ only;
	// viewers can read traces but not externalise them through a
	// public link), GET resolves without auth (auth middleware's
	// SkipPath allowlist lets it through), DELETE revokes (editor+
	// so a viewer can't nuke other operators' active shares).
	mux.HandleFunc("POST /api/traces/{id}/share",       auth.RequireAnyRole(editorRoles, s.createTraceSnapshot))
	mux.HandleFunc("GET  /api/traces/{id}/shares",      s.listTraceSnapshots)
	mux.HandleFunc("DELETE /api/traces/share/{token}",  auth.RequireAnyRole(editorRoles, s.revokeTraceSnapshot))
	mux.HandleFunc("GET  /api/public/trace/{token}", s.getPublicTrace)
	mux.HandleFunc("GET /api/logs", s.getLogs)
	mux.HandleFunc("GET /api/logs/timeseries", s.getLogsTimeseries)
	mux.HandleFunc("GET /api/logs/fields",     s.getLogsFields)
	// v0.5.464 — field-aware autocomplete on the /logs search
	// box. ES _terms_enum on keyword subfields for sub-ms prefix
	// lookups; CH backend returns [] (handler degrades silently).
	mux.HandleFunc("GET /api/logs/field-values", s.getLogsFieldValues)
	// v0.5.468 — EQL sequence detection. POST body carries the
	// EQL expression + time window + size cap. CH backend
	// returns an explicit "unsupported" error; the frontend
	// hides the panel when the logs backend reports non-ES.
	mux.HandleFunc("POST /api/logs/eql", auth.RequireAnyRole(editorRoles, s.runLogsEQL))
	// v0.5.402 — surrounding context (±N logs around a pivot ts).
	// Datadog Context tab equivalent. Two parallel logstore.Search
	// calls (before / after) so the operator sees what was emitted
	// either side of the log they're investigating.
	mux.HandleFunc("GET /api/logs/context",    s.getLogsContext)
	// v0.5.243 — unsupervised "what tokens are statistically
	// rare in the current window vs baseline" pass. ES-only
	// (CH returns empty); 60s cache fronts the expensive agg.
	mux.HandleFunc("GET /api/logs/patterns",   s.getLogsSignificantPatterns)
	// v0.5.244 — Drain-extracted log template ledger. Persistent
	// templates with sticky first_seen so the operator can ask
	// "what shape just started appearing?".
	mux.HandleFunc("GET /api/logs/templates",  s.getLogsTemplates)
	mux.HandleFunc("POST /api/logs/similar",   s.getLogsSimilarTraces)
	mux.HandleFunc("GET /api/metrics/names", s.getMetricNames)
	mux.HandleFunc("GET /api/metrics", s.getMetrics)
	mux.HandleFunc("GET /api/metrics/query", s.queryMetric)
	mux.HandleFunc("GET /api/metrics/histogram", s.getMetricHistogram)
	// v0.5.350 — span-metrics-derived per-service RED. Lets
	// operators whose collectors emit spanmetrics (traces.
	// spanmetrics.calls.total / duration) see the metric
	// stream as a first-class APM surface, side-by-side with
	// the span-derived RED on /services.
	mux.HandleFunc("GET /api/spanmetrics/services", s.getSpanMetricsByService)
	mux.HandleFunc("GET /api/metrics/labels", s.getMetricLabelValues)
	mux.HandleFunc("GET /api/spans/metric", s.spanMetric)
	mux.HandleFunc("POST /api/spans/metric-batch", s.spanMetricBatch)
	mux.HandleFunc("POST /api/dashboards/data",    s.dashboardsData)
	mux.HandleFunc("GET /api/spans/facets", s.spanFacets)
	mux.HandleFunc("GET /api/spans/repeats", s.spanRepeats)
	mux.HandleFunc("GET /api/spans/exemplar", s.spanExemplar)
	mux.HandleFunc("GET /api/spans/heatmap", s.spanHeatmap)
	mux.HandleFunc("GET /api/spans/bubbleup", s.spanBubbleUp)
	mux.HandleFunc("GET /api/profiles", s.listProfiles)
	mux.HandleFunc("GET /api/profiles/by-span", s.profilesForSpan)
	mux.HandleFunc("GET /api/profiles/by-span/hotspots", s.profileHotspotsForSpan)
	mux.HandleFunc("GET /api/profiles/hotspots", s.profileHotspots)
	mux.HandleFunc("GET /api/profiles/{id}", s.getProfile)
	mux.HandleFunc("GET    /api/exceptions",               s.listExceptions)
	// Errors Inbox — stateful exception groups (read = any, write = admin)
	mux.HandleFunc("GET    /api/exception-groups",                s.listExceptionGroups)
	mux.HandleFunc("GET    /api/exception-groups/{fp}/samples",   s.getExceptionGroupSamples)
	mux.HandleFunc("POST   /api/exception-groups/{fp}/state",     auth.RequireAnyRole(editorRoles, s.setExceptionGroupState))
	mux.HandleFunc("POST   /api/exception-groups/{fp}/assign",    auth.RequireAnyRole(editorRoles, s.assignExceptionGroup))
	mux.HandleFunc("GET    /api/services/{name}/operations", s.svcOperationSummary)
	mux.HandleFunc("GET    /api/services/{name}/span-breakdown", s.svcSpanBreakdown)
	mux.HandleFunc("GET    /api/services/{name}/callers",  s.svcCallers)
	mux.HandleFunc("GET    /api/services/{name}/callees",  s.svcCallees)
	mux.HandleFunc("GET    /api/problems",                  s.listProblems)
	mux.HandleFunc("GET    /api/problems/count",            s.countProblems)
	mux.HandleFunc("GET    /api/problems/buckets",          s.listProblemBuckets)
	mux.HandleFunc("POST   /api/problems/acknowledge",      auth.RequireAnyRole(editorRoles, s.acknowledgeProblems))
	mux.HandleFunc("PATCH  /api/problems/{id}/assignee",    auth.RequireAnyRole(editorRoles, s.setProblemAssignee))
	// Unified triage inbox (v0.5.211) — merges Problems +
	// Exception groups + Anomaly events with a normalised
	// priority blend so operators stop tab-hopping.
	mux.HandleFunc("GET    /api/inbox",                     s.inbox)
	mux.HandleFunc("GET    /api/alert-rules",               s.listAlertRules)
	mux.HandleFunc("POST   /api/alert-rules",               auth.RequireAnyRole(editorRoles, s.createAlertRule))
	mux.HandleFunc("PUT    /api/alert-rules/{id}",          auth.RequireAnyRole(editorRoles, s.updateAlertRule))
	mux.HandleFunc("DELETE /api/alert-rules/{id}",          auth.RequireAnyRole(editorRoles, s.deleteAlertRule))
	mux.HandleFunc("POST   /api/alert-rules/{id}/enable",   auth.RequireAnyRole(editorRoles, s.enableAlertRule))
	mux.HandleFunc("POST   /api/alert-rules/{id}/disable",  auth.RequireAnyRole(editorRoles, s.disableAlertRule))
	mux.HandleFunc("GET    /api/alert-rules/baseline",      auth.RequireAnyRole(editorRoles, s.getAlertBaseline))
	// Runbooks (v0.7.0) — operator-authored executable procedures.
	// GET list/detail open so viewers see them read-only (invariant #7);
	// every write gated to editor+. Executions + agent dispatch land next.
	mux.HandleFunc("GET    /api/runbooks",                  s.listRunbooks)
	mux.HandleFunc("POST   /api/runbooks",                  auth.RequireAnyRole(editorRoles, s.createRunbook))
	mux.HandleFunc("GET    /api/runbooks/{id}",             s.getRunbook)
	mux.HandleFunc("PUT    /api/runbooks/{id}",             auth.RequireAnyRole(editorRoles, s.updateRunbook))
	mux.HandleFunc("DELETE /api/runbooks/{id}",             auth.RequireAnyRole(editorRoles, s.deleteRunbook))
	mux.HandleFunc("POST   /api/runbooks/{id}/enable",      auth.RequireAnyRole(editorRoles, s.enableRunbook))
	mux.HandleFunc("POST   /api/runbooks/{id}/disable",     auth.RequireAnyRole(editorRoles, s.disableRunbook))
	// Runbook executions (v0.7.0) — a run is the audit record. List/detail
	// open (read-only audit for viewers); start/step/cancel gated to editor+.
	// Literal /executions segments out-rank the {id} wildcard in the Go 1.22
	// mux, so they match before /api/runbooks/{id}.
	mux.HandleFunc("POST   /api/runbooks/{id}/execute",                   auth.RequireAnyRole(editorRoles, s.executeRunbook))
	mux.HandleFunc("GET    /api/runbooks/executions",                    s.listExecutions)
	mux.HandleFunc("GET    /api/runbooks/executions/{id}",               s.getExecution)
	mux.HandleFunc("POST   /api/runbooks/executions/{id}/steps/{stepId}", auth.RequireAnyRole(editorRoles, s.execStepAction))
	mux.HandleFunc("POST   /api/runbooks/executions/{id}/cancel",         auth.RequireAnyRole(editorRoles, s.cancelExecution))
	mux.HandleFunc("GET /api/health", s.getHealth)
	// Alias for the k8s readinessProbe convention. Same body +
	// same 503-on-overload behaviour so a `httpGet: { path:
	// /healthz }` probe pulls overloaded pods out of the
	// Service endpoints set automatically.
	mux.HandleFunc("GET /healthz", s.getHealth)
	mux.HandleFunc("GET /api/version", s.getVersion)
	// Branding overlay — public GET so the login page can read it
	// before authentication; PUT is admin-only and capped at 256KB
	// to keep the logo data URI from bloating the settings table.
	mux.HandleFunc("GET /api/branding", s.getBranding)
	mux.HandleFunc("PUT /api/branding", auth.RequireRole(auth.RoleAdmin, s.putBranding))
	mux.HandleFunc("GET /api/anomalies/log-patterns", s.getLogPatternAnomalies)
	mux.HandleFunc("GET /api/anomalies/trace-ops",    s.getTraceOpAnomalies)
	mux.HandleFunc("GET /api/anomalies/metric",       s.getMetricAnomalies)
	mux.HandleFunc("GET /api/anomalies/events",       s.getAnomalyEvents)
	// Cmd-K palette autocomplete for the "silence anomaly" action
	// (v0.5.459). Editor-gated since the only useful next step is
	// creating a silence, and that's editor-gated too.
	mux.HandleFunc("GET /api/anomalies/active",       auth.RequireAnyRole(editorRoles, s.listActiveAnomalies))
	// Anomaly silencing — editor+ can mute/unmute (invariant #7:
	// viewer = read-only everywhere). A silence suppresses a signal
	// for EVERY operator, so it's a state mutation, not a personal
	// preference. GET stays open so viewers see the muted strip
	// read-only; the frontend hides the mute/unmute buttons for
	// viewers (features/anomalies/streams.tsx) so they never click
	// into a 403.
	mux.HandleFunc("GET    /api/anomalies/silences",    s.listAnomalySilences)
	mux.HandleFunc("POST   /api/anomalies/silences",    auth.RequireAnyRole(editorRoles, s.createAnomalySilence))
	mux.HandleFunc("DELETE /api/anomalies/silences/{id}", auth.RequireAnyRole(editorRoles, s.deleteAnomalySilence))
	mux.HandleFunc("POST   /api/anomalies/silences/bulk-delete", auth.RequireAnyRole(editorRoles, s.bulkDeleteAnomalySilences))
	// Audit log — admin-only read.
	mux.HandleFunc("GET /api/admin/audit",            s.listAuditLog)
	mux.HandleFunc("GET /api/admin/alert-tuning/noisy-rules", s.alertTuningNoisyRules)
	mux.HandleFunc("GET /api/admin/audit/export",     s.exportAuditLog)
	// Config export/import — admin-only. GET streams the full
	// operator-set state as a JSON file; POST replays it back.
	// Targets fresh installs (clean-install bootstrap) and DR
	// drills (move config between dev/staging/prod). Excludes
	// runtime data (spans, problems, audit_log, ...). See
	// config_iox.go for the table catalogue + merge semantics.
	mux.HandleFunc("GET  /api/admin/config/export", auth.RequireRole(auth.RoleAdmin, s.exportConfig))
	mux.HandleFunc("POST /api/admin/config/import", auth.RequireRole(auth.RoleAdmin, s.importConfig))
	// Diff is read-only — takes the same file payload as import,
	// returns {willAdd, willOverwrite, unchanged, onlyInDB} per
	// table. UI surfaces it as a "Preview diff" affordance so the
	// operator confirms before triggering the actual replay.
	mux.HandleFunc("POST /api/admin/config/diff",   auth.RequireRole(auth.RoleAdmin, s.diffConfig))
	// SQL playground — admin only; readonly=2 + 60s cap on the
	// CH side, allow-list of SELECT/WITH/SHOW/DESCRIBE/EXPLAIN
	// on the application side.
	// v0.5.297 — admin-only route gates. Both handlers had inline
	// auth checks, but the middleware wrapper is the convention
	// across the rest of the admin surface (catches the route at
	// registration time so a future refactor can't accidentally
	// drop the inline check).
	mux.HandleFunc("POST /api/admin/sql/query",       auth.RequireRole(auth.RoleAdmin, s.execSQL))
	mux.HandleFunc("GET  /api/admin/sql/schema",      auth.RequireRole(auth.RoleAdmin, s.sqlSchema))
	mux.HandleFunc("POST /api/admin/sql/elastic",     auth.RequireRole(auth.RoleAdmin, s.execElasticSQL))
	// v0.5.466 — ES index inventory: name, docs, size, health,
	// ILM phase/policy. Admin-only (read of cluster metadata).
	mux.HandleFunc("GET  /api/admin/elastic/indices", auth.RequireRole(auth.RoleAdmin, s.adminElasticIndices))
	// v0.5.467 — Kibana saved-search interop. Export streams an
	// .ndjson the operator can import into Kibana Discover;
	// import accepts the same format and turns each saved search
	// into a Coremetry /logs saved_view. Admin-gated.
	mux.HandleFunc("GET  /api/admin/elastic/saved-search-export", auth.RequireRole(auth.RoleAdmin, s.exportSavedViewsToKibana))
	mux.HandleFunc("POST /api/admin/elastic/saved-search-import", auth.RequireRole(auth.RoleAdmin, s.importSavedViewsFromKibana))
	// v0.5.476 — operator events ("deploy v1.2.3", "config
	// change", "incident start"). Vertical markers on every
	// time-series chart. List is open to any signed-in role;
	// create + delete are editor+ since they mutate.
	// v0.5.482 — moved off /api/events (was colliding with the
	// existing SSE stream at line 320). /api/operator-events is
	// explicit anyway.
	mux.HandleFunc("GET    /api/operator-events",      s.listEvents)
	mux.HandleFunc("POST   /api/operator-events",      auth.RequireAnyRole(editorRoles, s.createEvent))
	mux.HandleFunc("DELETE /api/operator-events/{id}", auth.RequireAnyRole(editorRoles, s.deleteEvent))
	// Saved views — per-user CRUD (server scopes by session).
	mux.HandleFunc("GET    /api/views",     s.listSavedViews)
	mux.HandleFunc("POST   /api/views",     s.createSavedView)
	mux.HandleFunc("DELETE /api/views/{id}", s.deleteSavedView)
	// AI observability (v0.5.163) — admin-only since prompts and
	// response samples can contain telemetry the viewer role might
	// not otherwise have access to. Tighten further (per-team) if
	// the operator surface ever sends third-party data into prompts.
	mux.HandleFunc("GET /api/ai/calls",      auth.RequireRole(auth.RoleAdmin, s.listAICalls))
	mux.HandleFunc("GET /api/ai/calls/{id}", auth.RequireRole(auth.RoleAdmin, s.getAICall))
	mux.HandleFunc("GET /api/ai/stats",      auth.RequireRole(auth.RoleAdmin, s.aiStats))
	mux.HandleFunc("GET /api/ai/series",     auth.RequireRole(auth.RoleAdmin, s.aiSeries))
	mux.HandleFunc("GET /api/ai/rates",      auth.RequireRole(auth.RoleAdmin, s.getAIRates))
	mux.HandleFunc("PUT /api/ai/rates",      auth.RequireRole(auth.RoleAdmin, s.putAIRates))
	mux.HandleFunc("GET /api/status", s.getStatus)

	// ── Public status page ────────────────────────────────────────
	// Read-only unauth: anyone with the URL can see status + subscribe.
	mux.HandleFunc("GET  /api/public-status",            s.publicStatus)
	mux.HandleFunc("POST /api/public-status/subscribe",  s.publicStatusSubscribe)
	mux.HandleFunc("GET  /api/public-status/confirm",    s.publicStatusConfirm)
	// Admin-gated config + subscriber management.
	mux.HandleFunc("GET    /api/status-page/config",       auth.RequireRole(auth.RoleAdmin, s.statusPageGetConfig))
	mux.HandleFunc("PUT    /api/status-page/config",       auth.RequireRole(auth.RoleAdmin, s.statusPagePutConfig))
	mux.HandleFunc("GET    /api/status-page/components",   auth.RequireRole(auth.RoleAdmin, s.statusPageListComponents))
	mux.HandleFunc("POST   /api/status-page/components",   auth.RequireRole(auth.RoleAdmin, s.statusPageCreateComponent))
	mux.HandleFunc("PUT    /api/status-page/components/{id}", auth.RequireRole(auth.RoleAdmin, s.statusPageUpdateComponent))
	mux.HandleFunc("DELETE /api/status-page/components/{id}", auth.RequireRole(auth.RoleAdmin, s.statusPageDeleteComponent))
	mux.HandleFunc("GET    /api/status-page/subscribers",  auth.RequireRole(auth.RoleAdmin, s.statusPageListSubscribers))
	mux.HandleFunc("DELETE /api/status-page/subscribers",  auth.RequireRole(auth.RoleAdmin, s.statusPageDeleteSubscriber))
	mux.HandleFunc("PUT    /api/status-page/incidents/{id}/publish", auth.RequireRole(auth.RoleAdmin, s.statusPagePublishIncident))

	// ── Runtime settings (admin) ───────────────────────────────────
	mux.HandleFunc("GET /api/settings/retention", auth.RequireRole(auth.RoleAdmin, s.getRetention))
	mux.HandleFunc("PUT /api/settings/retention", auth.RequireRole(auth.RoleAdmin, s.putRetention))
	mux.HandleFunc("GET /api/settings/anomaly-promotion", auth.RequireRole(auth.RoleAdmin, s.getAnomalyPromotion))
	mux.HandleFunc("PUT /api/settings/anomaly-promotion", auth.RequireRole(auth.RoleAdmin, s.putAnomalyPromotion))
	mux.HandleFunc("GET /api/settings/ai",        auth.RequireRole(auth.RoleAdmin, s.getAISettings))
	mux.HandleFunc("PUT /api/settings/ai",        auth.RequireRole(auth.RoleAdmin, s.putAISettings))
	// External Tempo backend — admin-only because the token grants
	// read access to every trace in the operator's Tempo cluster.
	mux.HandleFunc("GET /api/settings/tempo",     auth.RequireRole(auth.RoleAdmin, s.getTempoSettings))
	mux.HandleFunc("PUT /api/settings/tempo",     auth.RequireRole(auth.RoleAdmin, s.putTempoSettings))
	// External Kibana deep-link config (v0.5.236). GET is open
	// to any signed-in user — the Logs page renders the link
	// for everyone; only the admin can change the base URL.
	mux.HandleFunc("GET /api/settings/kibana",    s.getKibanaSettings)
	mux.HandleFunc("PUT /api/settings/kibana",    auth.RequireRole(auth.RoleAdmin, s.putKibanaSettings))
	mux.HandleFunc("GET  /api/settings/ldap",        auth.RequireRole(auth.RoleAdmin, s.getLDAPSettings))
	mux.HandleFunc("PUT  /api/settings/ldap",        auth.RequireRole(auth.RoleAdmin, s.putLDAPSettings))
	mux.HandleFunc("POST /api/settings/ldap/test",   auth.RequireRole(auth.RoleAdmin, s.testLDAPConnection))
	mux.HandleFunc("GET  /api/settings/ldap/search", auth.RequireRole(auth.RoleAdmin, s.searchLDAPUsers))
	mux.HandleFunc("POST /api/users/from-ldap",      auth.RequireRole(auth.RoleAdmin, s.provisionLDAPUser))

	// ── AI Copilot ─────────────────────────────────────────────────
	mux.HandleFunc("GET    /api/copilot/config",            s.copilotConfig)
	// v0.6.53 — in-app agentic chatbot. SSE stream; any authenticated
	// user (the 7 telemetry tools it calls are all read-only).
	mux.HandleFunc("POST   /api/copilot/chat",             s.copilotChat)
	mux.HandleFunc("POST   /api/copilot/explain-trace/{id}", s.copilotExplainTrace)
	// v0.5.255 — natural-language → DSL filter converter. /explore
	// gets a "✦ Natural language" input that feeds this endpoint.
	mux.HandleFunc("POST   /api/copilot/nl-to-query",       s.copilotNLToQuery)
	mux.HandleFunc("POST   /api/copilot/explain-span/{traceId}", s.copilotExplainSpan)
	mux.HandleFunc("POST   /api/copilot/explain-problem/{id}", s.copilotExplainProblem)
	mux.HandleFunc("POST   /api/copilot/explain-incident/{id}", s.copilotExplainIncident)
	mux.HandleFunc("POST   /api/copilot/explain-anomaly/{id}", s.copilotExplainAnomaly)
	mux.HandleFunc("POST   /api/copilot/explain-service",      s.copilotExplainServiceHealth)
	mux.HandleFunc("POST   /api/copilot/runbook/{id}",         s.copilotRunbook)
	mux.HandleFunc("POST   /api/copilot/compare-traces",       s.copilotCompareTraces)
	mux.HandleFunc("POST   /api/copilot/deploy-impact",        s.copilotDeployImpact)
	mux.HandleFunc("POST   /api/copilot/explain-slo/{id}",     s.copilotExplainSLO)
	mux.HandleFunc("POST   /api/copilot/explain-slow-query",   s.copilotExplainSlowQuery)
	mux.HandleFunc("POST   /api/copilot/suggest-service-tags", auth.RequireAnyRole(editorRoles, s.copilotSuggestServiceTags))

	// ── Incident management ───────────────────────────────────────
	mux.HandleFunc("GET    /api/incidents",                 s.listIncidents)
	mux.HandleFunc("POST   /api/incidents",                 auth.RequireAnyRole(editorRoles, s.createIncident))
	mux.HandleFunc("GET    /api/incidents/{id}",            s.getIncident)
	mux.HandleFunc("PUT    /api/incidents/{id}",            auth.RequireAnyRole(editorRoles, s.updateIncident))
	mux.HandleFunc("POST   /api/incidents/{id}/ack",        auth.RequireAnyRole(editorRoles, s.ackIncident))
	mux.HandleFunc("POST   /api/incidents/{id}/resolve",    auth.RequireAnyRole(editorRoles, s.resolveIncident))
	mux.HandleFunc("POST   /api/incidents/{id}/note",       auth.RequireAnyRole(editorRoles, s.addIncidentNote))
	mux.HandleFunc("GET    /api/incidents/{id}/timeline",   s.incidentTimeline)
	mux.HandleFunc("GET    /api/incidents/{id}/problems",   s.incidentProblems)

	// ── Synthetic monitoring ───────────────────────────────────────
	mux.HandleFunc("GET    /api/monitors",                s.listMonitors)
	mux.HandleFunc("POST   /api/monitors",                auth.RequireAnyRole(editorRoles, s.createMonitor))
	mux.HandleFunc("GET    /api/monitors/{id}",           s.getMonitor)
	mux.HandleFunc("PUT    /api/monitors/{id}",           auth.RequireAnyRole(editorRoles, s.updateMonitor))
	mux.HandleFunc("DELETE /api/monitors/{id}",           auth.RequireAnyRole(editorRoles, s.deleteMonitor))
	mux.HandleFunc("GET    /api/monitors/{id}/timeline",  s.monitorTimeline)
	// Heartbeat ingest — no auth so cron jobs / batch jobs can hit
	// it directly with curl. The token in the URL is the security
	// boundary (random 32-char hex per monitor).
	mux.HandleFunc("POST /api/heartbeats/{token}", s.acceptHeartbeat)
	mux.HandleFunc("GET  /api/heartbeats/{token}", s.acceptHeartbeat) // GET for `curl` and uptime trackers that only do GET

	// Auth
	mux.HandleFunc("GET  /api/auth/config",   s.authConfig)
	mux.HandleFunc("POST /api/auth/login",    s.login)
	mux.HandleFunc("POST /api/auth/logout",   s.logout)
	mux.HandleFunc("GET  /api/auth/me",       s.me)
	mux.HandleFunc("POST /api/auth/password", s.changeOwnPassword)
	mux.HandleFunc("GET  /api/auth/oidc/start",    s.oidcStart)
	mux.HandleFunc("GET  /api/auth/oidc/callback", s.oidcCallback)

	// SLOs (read = any user, write = admin)
	mux.HandleFunc("GET    /api/slos",            s.listSLOs)
	mux.HandleFunc("GET    /api/slos/{id}",       s.getSLO)
	mux.HandleFunc("GET    /api/slos/{id}/status", s.sloStatus)
	mux.HandleFunc("GET    /api/slos/{id}/burn-series", s.sloBurnSeries)
	// v0.6.30 — SLO burn-down forecast. Projects when the error
	// budget will be exhausted at the current short-window burn
	// rate. /slos list page surfaces "breaches in <24h" chip.
	mux.HandleFunc("GET    /api/slos/{id}/forecast", s.sloForecast)
	mux.HandleFunc("POST   /api/slos",            auth.RequireAnyRole(editorRoles, s.createSLO))
	mux.HandleFunc("DELETE /api/slos/{id}",       auth.RequireAnyRole(editorRoles, s.deleteSLO))

	// Dashboards (read = any user, write = admin)
	mux.HandleFunc("GET    /api/dashboards",      s.listDashboards)
	mux.HandleFunc("GET    /api/dashboards/{id}", s.getDashboard)
	mux.HandleFunc("POST   /api/dashboards",      auth.RequireAnyRole(editorRoles, s.createDashboard))
	mux.HandleFunc("PUT    /api/dashboards/{id}", auth.RequireAnyRole(editorRoles, s.updateDashboard))
	mux.HandleFunc("DELETE /api/dashboards/{id}", auth.RequireAnyRole(editorRoles, s.deleteDashboard))

	// Settings + notification channels (admin only)
	mux.HandleFunc("GET    /api/settings/smtp",       auth.RequireRole(auth.RoleAdmin, s.getSMTPSettings))
	mux.HandleFunc("PUT    /api/settings/smtp",       auth.RequireRole(auth.RoleAdmin, s.putSMTPSettings))
	mux.HandleFunc("POST   /api/settings/smtp/test",  auth.RequireRole(auth.RoleAdmin, s.testSMTPSettings))
	mux.HandleFunc("GET    /api/channels",            auth.RequireRole(auth.RoleAdmin, s.listChannels))
	mux.HandleFunc("POST   /api/channels",            auth.RequireRole(auth.RoleAdmin, s.createChannel))
	mux.HandleFunc("PUT    /api/channels/{id}",       auth.RequireRole(auth.RoleAdmin, s.updateChannel))
	mux.HandleFunc("DELETE /api/channels/{id}",       auth.RequireRole(auth.RoleAdmin, s.deleteChannel))
	mux.HandleFunc("POST   /api/channels/{id}/test",  auth.RequireRole(auth.RoleAdmin, s.testChannel))
	// Maintenance windows — admin-only CRUD. While an active
	// window matches a problem's (service, severity), the
	// notifier skips the live fan-out. Problems still open +
	// auto-resolve so the post-window timeline review is
	// intact; only the channel spam is suppressed.
	mux.HandleFunc("GET    /api/maintenance-windows",      auth.RequireRole(auth.RoleAdmin, s.listMaintenanceWindows))
	mux.HandleFunc("POST   /api/maintenance-windows",      auth.RequireRole(auth.RoleAdmin, s.createMaintenanceWindow))
	mux.HandleFunc("DELETE /api/maintenance-windows/{id}", auth.RequireRole(auth.RoleAdmin, s.deleteMaintenanceWindow))
	// Zoom-specific helper: list channels the configured S2S
	// OAuth app can see, so admins pick a Channel ID from a
	// searchable list instead of pasting JIDs by hand. POST
	// because we need the un-redacted client secret in the body
	// for un-saved configurations (and we don't want secrets in
	// the URL query string).
	mux.HandleFunc("POST   /api/channels/zoom/list-channels",
		auth.RequireRole(auth.RoleAdmin, s.listZoomChannels))
	// v0.5.316 — Operator-reported: viewers/editors were
	// getting auto-logged out because the SamplingChip on
	// /service called this read-only endpoint speculatively
	// and the admin-only gate returned 401 → frontend
	// onUnauthorized → forced logout. GET is now any
	// authenticated role (read-only chip surfaces for everyone);
	// PUT stays admin-only.
	mux.HandleFunc("GET    /api/settings/sampling",   auth.RequireAnyRole([]string{auth.RoleAdmin, auth.RoleEditor, auth.RoleViewer}, s.getSamplingSettings))
	mux.HandleFunc("PUT    /api/settings/sampling",   auth.RequireRole(auth.RoleAdmin, s.putSamplingSettings))

	// User management (admin only)
	mux.HandleFunc("GET    /api/users",                  auth.RequireRole(auth.RoleAdmin, s.listUsers))
	// Per-team membership lookup — readable by any
	// authenticated user (not admin-only). Drives the
	// owner-team / SRE-team chip popover on /service?name=…
	// Returns email + role + team only; password hash is
	// never serialised regardless.
	mux.HandleFunc("GET    /api/users/by-team",          s.listUsersByTeam)
	mux.HandleFunc("POST   /api/users",                  auth.RequireRole(auth.RoleAdmin, s.createUser))
	mux.HandleFunc("DELETE /api/users/{id}",             auth.RequireRole(auth.RoleAdmin, s.deleteUser))
	mux.HandleFunc("POST   /api/users/{id}/password",    auth.RequireRole(auth.RoleAdmin, s.resetUserPassword))
	mux.HandleFunc("PUT    /api/users/{id}/role",        auth.RequireRole(auth.RoleAdmin, s.setUserRole))
	mux.HandleFunc("PUT    /api/users/{id}/team",        auth.RequireRole(auth.RoleAdmin, s.setUserTeam))
	mux.HandleFunc("PUT    /api/users/{id}/custom-role", auth.RequireRole(auth.RoleAdmin, s.setUserCustomRole))

	// Custom roles — operator-defined viewer subsets (v0.5.251).
	// Page list is sourced from a single backend registry so the
	// sidebar + checkbox grid + custom-role pages share IDs.
	mux.HandleFunc("GET    /api/admin/custom-roles",      auth.RequireRole(auth.RoleAdmin, s.listCustomRoles))
	mux.HandleFunc("POST   /api/admin/custom-roles",      auth.RequireRole(auth.RoleAdmin, s.upsertCustomRole))
	mux.HandleFunc("DELETE /api/admin/custom-roles/{name}", auth.RequireRole(auth.RoleAdmin, s.deleteCustomRole))
	mux.HandleFunc("GET    /api/admin/pages",             auth.RequireRole(auth.RoleAdmin, s.listAvailablePages))

	// Cluster membership — multi-pod HA visibility (v0.5.253).
	// Single round-trip: SCAN coremetry:pod:* in Redis + MGET. Empty
	// list (no Redis) falls back to a single-pod view.
	mux.HandleFunc("GET    /api/admin/cluster",           auth.RequireRole(auth.RoleAdmin, s.listClusterMembers))
	// v0.5.277 — "what changed" banner data. Open problem
	// counts + recent service.version transitions. Cheap
	// (15s cache); polled from the global AppShell.
	mux.HandleFunc("GET    /api/recent-changes",          s.getRecentChanges)

	// Ingest pipeline (v0.5.263) — admin-managed drop / enrich
	// rules applied before the sampler. List + upsert + delete.
	mux.HandleFunc("GET    /api/admin/pipeline-rules",      auth.RequireRole(auth.RoleAdmin, s.listPipelineRules))
	mux.HandleFunc("POST   /api/admin/pipeline-rules",      auth.RequireRole(auth.RoleAdmin, s.upsertPipelineRule))
	mux.HandleFunc("DELETE /api/admin/pipeline-rules/{id}", auth.RequireRole(auth.RoleAdmin, s.deletePipelineRule))

	// Tempo-compatible API (Grafana datasource integration)
	s.registerTempoRoutes(mux)

	// Web UI (embedded Vite static SPA). Custom handler instead of
	// http.FileServer so we can:
	//   • Serve `<path>/index.html` directly without a 301 to `<path>/`
	//     (the redirect breaks behind some OpenShift / load-balancer
	//     setups where X-Forwarded-Proto isn't propagated and the
	//     redirect Location ends up with the wrong scheme).
	//   • Provide a SPA fallback for deep-links and unknown routes by
	//     serving the root index.html — react-router then mounts the
	//     right page from the current URL.
	sub, _ := fs.Sub(s.webFS, "frontend/dist")
	mux.Handle("/", spaHandler(sub))

	// Pre-warm the dependencies caches in the background. The
	// /databases and /messaging pages run two heavy GROUP BYs
	// against billions of spans; doing that synchronously on the
	// first operator's request gives them a 3-5s wait. We refresh
	// the default-window (1h) result every 25s so the cache (TTL
	// 30s) is always hot. Operators using a non-default range
	// (15m, 24h, custom) still pay the cold-load cost — but the
	// morning-triage default lands instantly.
	go s.warmDependenciesCache()

	log.Printf("[api] HTTP listening on %s", s.addr)
	// v0.6.42 — self-observability. otelhttp wraps the outer-most
	// handler so every inbound request gets a server span tagged
	// with route + status + duration. Frontend's `traceparent`
	// header (lib/otel.ts) is extracted by otelhttp's
	// W3C propagator, so a UI click → server-span chain joins on
	// /trace/{id}. Goes OUTSIDE cors + auth middleware so the span
	// captures the entire request lifecycle (including 401s).
	handler := otelhttp.NewHandler(cors(s.auth.Middleware(mux)),
		"coremetry-api",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			// "GET /api/traces/:id" — method + cardinality-collapsed
			// path. v0.6.46 — the raw r.URL.Path includes path params
			// (trace IDs, service names, problem IDs), so naming the
			// span after it would mint one distinct span name per ID.
			// The spans table's `name` column is LowCardinality(String)
			// — unbounded distinct names degrade it (CLAUDE.md anti-
			// pattern). collapseRoute() rewrites the volatile segments
			// to :id so the span name stays a bounded route template.
			return r.Method + " " + collapseRoute(r.URL.Path)
		}),
	)
	return http.ListenAndServe(s.addr, handler)
}

// warmDependenciesCache primes the hottest read endpoints on
// a 25s loop so morning-triage requests always land on a
// warm cache. Each warm pass writes through storeCached(),
// which uses the same envelope shape as live request writes
// — so the SWR read path treats warmed entries identically.
//
// Endpoints chosen for warming are the ones operators hit
// first after login (services list, clusters dropdown,
// service metadata) plus the slow CH queries
// (databases / messaging dependency views).
//
// Errors are logged but don't stop the loop — a transient
// CH blip shouldn't disable warming permanently.
func (s *Server) warmDependenciesCache() {
	const (
		tick      = 25 * time.Second
		ttl       = 30 * time.Second
		warmWin   = time.Hour
		queryBudg = 20 * time.Second
	)
	// Delay first refresh so we don't compete with boot-time DDL
	// migrations for CH bandwidth on a cold start.
	time.Sleep(5 * time.Second)
	warm := func(label, key string, useTTL time.Duration, fn func(ctx context.Context) (any, error), perCallBudget ...time.Duration) {
		// v0.5.461 — per-call budget override. Default queryBudg
		// (20s) covers every warmer except ES significant_text
		// on billion-doc indices; that one gets its own deadline
		// via the same COREMETRY_LOGS_PATTERNS_DEADLINE env the
		// handler reads (passed through from the call site).
		budget := queryBudg
		if len(perCallBudget) > 0 && perCallBudget[0] > 0 {
			budget = perCallBudget[0]
		}
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		// Skip the upstream call when the cache already holds a
		// fresh envelope (within the soft TTL). Stale-but-
		// usable entries still get refreshed so the next read
		// gets a fresh hit.
		if raw, ok, _ := s.cache.Get(ctx, key); ok {
			if written, _, envOK := unwrapEnvelope(raw); envOK && time.Since(written) < useTTL {
				return
			}
		}
		v, err := fn(ctx)
		if err != nil {
			// v0.5.461 — quiet on context deadline. The live
			// handler is graceful (returns timedOut:true + caches
			// it for 60s) so the operator's panel still renders;
			// a warmer that can't fit in budget shouldn't spam
			// logs every tick. Other errors (backend outage,
			// schema mismatch) stay loud — they're actionable.
			if errors.Is(err, context.DeadlineExceeded) ||
				strings.Contains(err.Error(), "context deadline exceeded") {
				return
			}
			log.Printf("[warm] %s: %v", label, err)
			return
		}
		body, err := json.Marshal(v)
		if err != nil {
			log.Printf("[warm] %s marshal: %v", label, err)
			return
		}
		s.storeCached(ctx, key, body, useTTL)
	}
	// Logs-patterns warmer budget. Same env override as the
	// handler, slightly higher default (25s vs 15s) so a single
	// cold-tick still has room before the live read inherits a
	// cold cache.
	logsPatternsBudget := 25 * time.Second
	if v := strings.TrimSpace(os.Getenv("COREMETRY_LOGS_PATTERNS_DEADLINE")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			logsPatternsBudget = d
		}
	}
	for {
		to := time.Now()
		from := to.Add(-warmWin)
		warm("databases", "databases:"+cacheBucket(from, to), ttl,
			func(ctx context.Context) (any, error) { return s.store.GetDatabases(ctx, from, to) })
		warm("messaging", "messaging:"+cacheBucket(from, to), ttl,
			func(ctx context.Context) (any, error) { return s.store.GetMessaging(ctx, from, to) })
		// Services list — the page operators open first after
		// login. Use the same key shape as listServices so the
		// live handler picks up the warm entry on its next call.
		warm("clusters", "clusters:"+cacheBucket(from.Add(-23*time.Hour), to), 60*time.Second,
			func(ctx context.Context) (any, error) {
				names, err := s.store.ListClusters(ctx, from.Add(-23*time.Hour), to)
				if err != nil {
					return nil, err
				}
				return map[string]any{"clusters": names}, nil
			})
		warm("services-metadata", "services-metadata", 60*time.Second,
			func(ctx context.Context) (any, error) {
				return s.store.ListServiceMetadata(ctx)
			})
		// /services landing page — first page, default sort
		// (errorRate desc), no filters, default 15-min window.
		// Same key shape as getServices builds via cacheBucket,
		// so the operator's very first click after each warmer
		// tick lands on a HIT-L1 instead of waiting for the MV
		// scan. The bucket also extends slightly into the
		// future so the next request a few seconds later still
		// matches the same key.
		landingFrom := to.Add(-15 * time.Minute)
		landingKey := fmt.Sprintf(
			"services:mv=true:limit=50:offset=0:bucket=%s:name=:sort=errorRate:dir=desc:ot=:st=:cl=",
			cacheBucket(landingFrom, to))
		warm("services-landing", landingKey, 30*time.Second,
			func(ctx context.Context) (any, error) {
				list, err := s.store.GetServicesAggFiltered(ctx,
					landingFrom, to, "", "errorRate", "desc", 50, 0)
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"services": list,
					"hasMore":  len(list) == 50,
					"offset":   0,
				}, nil
			})
		// v0.5.375 — pre-warm /api/logs/patterns. The
		// LivePatternsPanel polls every 30s with fixed params
		// (window=15m, baseline=24h, topN=25). Cold ES
		// significant_text is the slowest call in the panel
		// at billion-doc indices (often 1-2s p99); warming
		// keeps the first /logs visit on a HIT-L1 path. CH
		// backend's equivalent (sample-based foreground vs
		// baseline scoring) is also non-trivial — same cache
		// key serves both, this warmer transparently primes
		// whichever backend is wired.
		const (
			logsCurWindow = 15 * time.Minute
			logsBaseline  = 24 * time.Hour
			logsTopN      = 25
			// 60s matches the live serveCached soft TTL on
			// the handler. Warmer ticks at 25s so the entry
			// stays in the fresh window between ticks.
			logsTTL = 60 * time.Second
		)
		nowLogs := time.Now()
		curStart := nowLogs.Add(-logsCurWindow)
		baseStart := curStart.Add(-logsBaseline)
		logsKey := fmt.Sprintf("logs-significant:cur=%s:bg=%s:topN=%d",
			logsCurWindow, logsBaseline, logsTopN)
		warm("logs-patterns", logsKey, logsTTL,
			func(ctx context.Context) (any, error) {
				hits, err := s.logs.SignificantPatterns(ctx, curStart, baseStart, nowLogs, logsTopN)
				if err != nil {
					return nil, err
				}
				if hits == nil {
					hits = []logstore.SignificantPattern{}
				}
				// Same opaque-id filter as the live handler;
				// keep the warmed payload byte-identical so
				// the next live read can serve directly
				// without re-marshaling.
				filtered := hits[:0]
				for _, h := range hits {
					if templater.LooksLikeOpaqueID(h.Token) {
						continue
					}
					filtered = append(filtered, h)
				}
				hits = filtered
				return map[string]any{
					"backend":  s.logs.Backend(),
					"window":   logsCurWindow.String(),
					"baseline": logsBaseline.String(),
					"patterns": hits,
				}, nil
			}, logsPatternsBudget)
		time.Sleep(tick)
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// getClusters returns the distinct k8s / openshift cluster names
// observed in the requested window. Drives the cluster-filter
// dropdown on /services + the per-cluster selector on
// /service?name=. Cached 60s — cluster set changes rarely.
func (s *Server) getClusters(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, 24*time.Hour)
	key := "clusters:" + cacheBucket(from, to)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		names, err := s.store.ListClusters(r.Context(), from, to)
		if err != nil {
			return nil, err
		}
		return map[string]any{"clusters": names}, nil
	})
}

func (s *Server) getServices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), 24*time.Hour)
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	// Page-based pagination — default page size 50, capped at 500.
	// The UI pages forward through these chunks; there's no
	// 'Load all' bump anymore because at 10k+ services it stalls
	// the browser and isn't useful.
	limit := parseInt(q.Get("limit"), 50)
	if limit < 1 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset := parseInt(q.Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}
	// Optional case-insensitive substring match on service_name.
	// Drives the Services-page filter dropdown: when the operator types
	// in the picker, the query searches ALL services that contain the
	// typed substring across pages.
	nameMatch := strings.TrimSpace(q.Get("name"))
	// Server-side sort. Without this, the client would sort the
	// limited page locally — fine at 50 services, broken at
	// 1000+ where every page-load fixes the same first 50 in
	// place. The chstore layer whitelists the column → CH ORDER
	// BY mapping; everything else falls back to span-count desc.
	sort := strings.TrimSpace(q.Get("sort"))
	dir := strings.TrimSpace(q.Get("dir"))
	// Optional team filters — narrow the listing to services
	// whose service_metadata row matches the requested owner
	// or SRE team. Resolved here against the catalog so the
	// downstream spans query gets a fixed `service_name IN
	// (...)` allowlist (microsecond-level filter); avoids a
	// per-query JOIN against the catalog.
	ownerTeam := strings.TrimSpace(q.Get("ownerTeam"))
	sreTeam := strings.TrimSpace(q.Get("sreTeam"))
	// Optional cluster filter — narrows to spans whose
	// k8s.cluster.name / openshift.cluster.name / cluster
	// resource-attr matches. Empty = no constraint.
	cluster := strings.TrimSpace(q.Get("cluster"))
	if from.IsZero() {
		from = time.Now().Add(-since)
	}
	if to.IsZero() {
		to = time.Now()
	}
	// MV-backed fast path. The MV writes a row every 5 minutes, so windows
	// shorter than ~5 min would return empty — fall through to the raw
	// scan in that case (small window = small scan, also fast).
	// MV path doesn't carry a cluster dimension — force raw
	// scan when a cluster filter is set. Trade-off: full scan
	// over the window, but bounded by the filter so the cost
	// stays proportional to the chosen cluster's traffic.
	useMV := to.Sub(from) >= 5*time.Minute && cluster == ""
	// Bucket from/to to 30s alignment for the cache key. The
	// raw `q.Get("from")` is wall-clock ns and changes every
	// request, so the legacy key was effectively per-request
	// — cache HIT rate ≈ 0 even at 30s TTL. cacheBucket
	// aligns timestamps to the same 30s window every operator
	// hitting in that window sees, so back-to-back clicks
	// land on a HIT instead of a cold MV scan.
	// v0.7.44 — opt-in distinct-service total for the Services page First/Last
	// pager. Default OFF so the hot path stays count-free (the count(DISTINCT)
	// is the slowest part — see the limit+1 hasMore trick below).
	withTotal := q.Get("withTotal") == "1"
	bucket := cacheBucket(from, to)
	key := fmt.Sprintf("services:mv=%t:limit=%d:offset=%d:bucket=%s:name=%s:sort=%s:dir=%s:ot=%s:st=%s:cl=%s:wt=%t",
		useMV, limit, offset, bucket, nameMatch, sort, dir, ownerTeam, sreTeam, cluster, withTotal)
	// 30s cache. The 5m-MV-backed query is already sub-second on
	// 10k+ services, but 30s collapses every page-flip and tab
	// switch in a session into one CH round-trip per (page,
	// filter, range). Refresh button on the page can ?refresh=1.
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		// Resolve team filters → service-name allowlist via
		// the catalog. Bounded by catalog size (~thousands),
		// not span volume; effectively free.
		var serviceIn []string
		if ownerTeam != "" || sreTeam != "" {
			catalog, cerr := s.store.ListServiceMetadata(r.Context())
			if cerr != nil {
				return nil, fmt.Errorf("catalog: %w", cerr)
			}
			for name, md := range catalog {
				if ownerTeam != "" && md.OwnerTeam != ownerTeam {
					continue
				}
				if sreTeam != "" && md.SRETeam != sreTeam {
					continue
				}
				serviceIn = append(serviceIn, name)
			}
			if len(serviceIn) == 0 {
				// No catalog rows match the requested team
				// — return empty without firing the spans
				// query.
				return map[string]any{
					"services": []chstore.ServiceSummary{},
					"hasMore":  false,
					"offset":   offset,
					"limit":    limit,
				}, nil
			}
		}
		// Fetch limit+1 so we can report hasMore without paying a
		// separate count(DISTINCT) — at 10k+ services a count is
		// the slowest part of the page.
		probeLimit := limit + 1
		var rows []chstore.ServiceSummary
		var err error
		if useMV {
			rows, err = s.store.GetServicesAggFilteredIn(r.Context(), from, to, nameMatch, serviceIn, sort, dir, probeLimit, offset)
		} else {
			rows, err = s.store.GetServicesFilteredIn(r.Context(), since, from, to, nameMatch, serviceIn, sort, dir, probeLimit, offset, cluster)
		}
		if err != nil {
			return nil, err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		// v0.5.274 — auto-score health per service from
		// errorRate + open-problem counts. Single FINAL scan
		// over the problems table; bounded by status=open so
		// the row count is tiny.
		counts, perr := s.store.GetOpenProblemCountsByService(r.Context())
		if perr == nil {
			for i := range rows {
				c := counts[rows[i].Name]
				rows[i].OpenProblems = c.Critical + c.Warning + c.Info
				rows[i].Health, rows[i].HealthReason = scoreHealth(&rows[i], c)
			}
		}
		resp := map[string]any{
			"services": rows,
			"hasMore":  hasMore,
			"offset":   offset,
			"limit":    limit,
		}
		// v0.7.44 — opt-in distinct-service count for the First/Last pager. MV
		// path only (cheap uniqExact over service_summary_5m); on the raw/cluster
		// path total stays absent and the UI degrades to First + Next/Prev.
		if withTotal && useMV {
			if total, terr := s.store.CountServicesAgg(r.Context(), from, to, nameMatch, serviceIn); terr == nil {
				resp["total"] = total
			}
		}
		return resp, nil
	})
}

// scoreHealth (v0.5.274) — Datadog-Watchdog-style red/yellow/
// green verdict per service. Blend rules, in order:
//
//   RED:    1+ open critical problem
//        OR errorRate > 5%
//   YELLOW: 1+ open warning problem
//        OR errorRate > 1%
//        OR 1+ open info problem
//   GREEN:  otherwise
//
// Operator sees the firing rule in HealthReason so the badge
// is auditable. Reason text is short — meant for a tooltip.
func scoreHealth(svc *chstore.ServiceSummary, c chstore.OpenProblemCounts) (string, string) {
	if c.Critical > 0 {
		return "red", fmt.Sprintf("%d open critical", c.Critical)
	}
	if svc.ErrorRate > 5 {
		return "red", fmt.Sprintf("error rate %.1f%%", svc.ErrorRate)
	}
	if c.Warning > 0 {
		return "yellow", fmt.Sprintf("%d open warning", c.Warning)
	}
	if svc.ErrorRate > 1 {
		return "yellow", fmt.Sprintf("error rate %.1f%%", svc.ErrorRate)
	}
	if c.Info > 0 {
		return "yellow", fmt.Sprintf("%d open info", c.Info)
	}
	return "green", ""
}

// getSystemStats returns the meta-observability snapshot — today's
// volume KPIs, per-table storage, 30-day history, live ingest rate.
// Backed by ClickHouse system.parts metadata + the 5m span aggregate
// MV so it stays sub-second even at 40M traces / day. Cached 60s.
func (s *Server) getSystemStats(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "system-stats", 60*time.Second, func() (any, error) {
		return s.store.GetSystemStats(r.Context())
	})
}

// getCorrelations is the "what changed around this time?" causal-AIOps
// endpoint. Pass `at` (unix ns), `windowSec` (default 600), `baselineSec`
// (default 4× window). Returns a ranked list of services whose RED
// metrics swung the most between the baseline and current windows —
// the operator's "what else moved when this fired" view.
//
// Cached 30s keyed on the parameters. The query is bounded by HAVING
// gates + LIMIT 500 so even at 100k services the worst case is sub-
// second; 30s cache amortises the cost across a triage session where
// the operator clicks "Why?" multiple times in a row.
func (s *Server) getCorrelations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	at := parseTime(q.Get("at"))
	if at.IsZero() {
		at = time.Now().Add(-10 * time.Minute)
	}
	windowSec := parseInt(q.Get("windowSec"), 600)
	baselineSec := parseInt(q.Get("baselineSec"), windowSec*4)

	// v0.5.297 — bucket `at` to the minute so concurrent
	// triage clicks landing in the same 60s share the cache
	// round-trip. Pre-bucket the key used raw nanoseconds →
	// near-zero hit rate across operators clicking "Why?" in
	// the same incident. 30s cache TTL × 60s bucket means a
	// second click within the minute always hits Redis.
	atBucketed := at.Truncate(time.Minute).Unix()
	key := fmt.Sprintf("correlate:at=%d:w=%d:b=%d", atBucketed, windowSec, baselineSec)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetCorrelatedChanges(r.Context(), at, windowSec, baselineSec)
	})
}

// getRedisStats surfaces Redis INFO + DBSIZE on the System page so
// operators see hit-rate, ops/sec, evictions, key count, memory at a
// glance alongside the ClickHouse + ingest figures. Returns a zero-
// valued Stats struct (no error) when Redis isn't configured — the UI
// renders a "not configured" banner rather than an error tile.
//
// Cached 5s — the ops/sec gauge needs to feel live during incident
// response. Cheap query (single INFO + DBSIZE round-trip).
func (s *Server) getRedisStats(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "redis-stats", 5*time.Second, func() (any, error) {
		if s.cache == nil {
			return cache.RedisStats{}, nil
		}
		return s.cache.Stats(r.Context())
	})
}

// getCacheStats surfaces the multi-tier cache (L1 + Redis +
// singleflight + SWR) effectiveness counters: per-tier hit
// totals since process start, the hottest 20 cache keys, and
// the in-process L1 fill ratio. Drives the API-cache panel on
// AdminStats so the operator can verify the tier is doing
// useful work post-deploy.
//
// NOT itself cached — we'd be polluting the stats with its
// own access pattern. Admin-only because it exposes the
// internal cache key shape (mild info leak in multi-tenant
// deployments).
func (s *Server) getCacheStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.stats.snapshot(s.l1))
}

// getCardinality returns the meta-observability "what's eating my
// CH" report — top services / metrics / attribute keys / columns
// by row count or storage bytes. Cached 5 min: the cardinality
// surface changes on the order of deploys, not seconds, and the
// queries (especially the attribute-key uniqExact) are heavier
// than the per-table system.parts read. Admin-only because the
// result exposes which services / metric names exist (mild
// information leak in multi-tenant deployments).
func (s *Server) getCardinality(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "cardinality", 5*time.Minute, func() (any, error) {
		return s.store.GetCardinality(r.Context())
	})
}

// getServiceStructure returns a Grafana-Drilldown-style multi-trace
// path-aggregated tree across the most recent N traces involving
// `name`. Spans with the same `(parent_path, service, displayName)`
// triple collapse into a single node carrying count + avg/max
// duration + error count, so a tight loop or fan-out shows up once
// with `×N` rather than as N visually identical bars.
// getServiceClusterBreakdown returns per-cluster RED stats for
// one service. Cached 30s on the (service, window) tuple so
// quick range toggles share one query.
func (s *Server) getServiceClusterBreakdown(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("service-clusters:%s:%s", name, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		rows, err := s.store.GetServiceClusterBreakdown(r.Context(), name, from, to)
		if err != nil {
			return nil, err
		}
		return map[string]any{"clusters": rows}, nil
	})
}

// getServiceBundle fans out the THREE queries the Service
// detail page used to fire from the frontend on mount —
// services list (just to find this one's KPI row), problems
// for the service, and the operation summary — and returns
// them as a single JSON response. Cuts the network round-
// trip count from 3 → 1 and pulls the CH-side latencies into
// parallel goroutines (vs sequential awaits at billion-span
// scale where each query is the cold-cache cost).
//
// Cached 15s with SWR so subsequent operator clicks land
// instantly. Failure of any single sub-query degrades that
// field to null in the response (matches the frontend's
// existing tolerance for partial loads) instead of failing
// the whole bundle.
//
// Heatmap / RED charts / cluster breakdown stay separate —
// they have their own time-window semantics (compare period,
// lazy mount) the bundle can't bake in without coupling.
func (s *Server) getServiceBundle(w http.ResponseWriter, r *http.Request) {
	svc := r.PathValue("name")
	if svc == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), 24*time.Hour)
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	if from.IsZero() {
		from = time.Now().Add(-since)
	}
	if to.IsZero() {
		to = time.Now()
	}

	key := fmt.Sprintf("svc-bundle:svc=%s:since=%s:from=%s:to=%s",
		svc, q.Get("since"), q.Get("from"), q.Get("to"))
	// 60s soft TTL — every slot on this bundle is operator-
	// curated (catalog, problems, operations summary, deploys)
	// rather than per-span data; minute-grain freshness is
	// indistinguishable to a human and the SWR tier from
	// v0.5.36 extends stale-but-usable to 3 min. Combined
	// with the v0.5.66 hover-prefetch this means most
	// operator clicks within their session land on a HIT-L1.
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		type result struct {
			Service    *chstore.ServiceSummary    `json:"service"`
			Problems   []chstore.Problem          `json:"problems"`
			Operations []chstore.OperationSummary `json:"operations"`
			Deploys    []chstore.Deploy           `json:"deploys"`
		}
		out := &result{}

		// Fan out in parallel — each branch sets its slot of
		// the result struct under its own goroutine. Errors
		// log + leave the field at zero value (nil slice or
		// pointer) so a single CH blip on one query doesn't
		// blank the whole panel. WaitGroup keeps the
		// goroutines bounded to this request's lifetime.
		var wg sync.WaitGroup
		wg.Add(4)

		go func() {
			defer wg.Done()
			list, err := s.store.GetServicesAggFiltered(r.Context(), from, to,
				svc, "spanCount", "desc", 1, 0)
			if err != nil {
				log.Printf("[svc-bundle] services %s: %v", svc, err)
				return
			}
			for i := range list {
				if list[i].Name == svc {
					out.Service = &list[i]
					return
				}
			}
		}()

		go func() {
			defer wg.Done()
			probs, err := s.store.ListProblems(r.Context(), chstore.ProblemFilter{
				Service: svc,
				Limit:   50,
			})
			if err != nil {
				log.Printf("[svc-bundle] problems %s: %v", svc, err)
				return
			}
			out.Problems = probs
		}()

		go func() {
			defer wg.Done()
			ops, err := s.store.GetOperationSummary(r.Context(), svc, since, from, to)
			if err != nil {
				log.Printf("[svc-bundle] operations %s: %v", svc, err)
				return
			}
			out.Operations = ops
		}()

		go func() {
			defer wg.Done()
			deps, err := s.store.GetServiceDeploys(r.Context(), svc, from, to)
			if err != nil {
				log.Printf("[svc-bundle] deploys %s: %v", svc, err)
				return
			}
			out.Deploys = deps
		}()

		wg.Wait()
		return out, nil
	})
}

func (s *Server) getServiceStructure(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "service name required", http.StatusBadRequest); return
	}
	since := parseDuration(r.URL.Query().Get("since"), time.Hour)
	samples := parseInt(r.URL.Query().Get("samples"), 50)
	internalOnly := r.URL.Query().Get("internal") == "true"

	// 1h cache. Service call structure shifts on a deploy / weekly /
	// monthly cadence, not minute-by-minute, so an hour-stale tree
	// is functionally identical to a fresh one for the operator's
	// purposes. The underlying top-N-traces + bulk-attribute fetch
	// is the heaviest single query on the service detail page;
	// caching for 1h collapses an entire user session into a single
	// CH round-trip. A range or sample-count change still misses.
	key := fmt.Sprintf("service-structure:svc=%s:since=%s:samples=%d:int=%t",
		name, since, samples, internalOnly)
	s.serveCached(w, r, key, time.Hour, func() (any, error) {
		roots, totalSpans, sampledFrom, err := s.store.AggregateServiceStructure(
			r.Context(), name, since, samples, internalOnly)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"service":      name,
			"roots":        roots,
			"sampledFrom":  sampledFrom,
			"totalSpans":   totalSpans,
			"internalOnly": internalOnly,
		}, nil
	})
}

// getServiceInfraMetrics returns curated runtime/process timeseries
// (cpu / memory / rps / runtime-specific) for the inspected
// service's pods. Powers the infra correlation panel on the
// /service?name=… page so an SRE can see "p99 spiked at 14:32 →
// pod CPU went to 95% at the same moment" in one glance.
//
// Performance: single CH query filtered by (service_name, time)
// primary-key prefix + LowCardinality `metric IN (…)` over the
// curated source list. Bucket size auto-scales to ~30 buckets
// across the window. 30s cache absorbs the page reloads.
func (s *Server) getServiceInfraMetrics(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
		return
	}
	since := parseDuration(r.URL.Query().Get("since"), 15*time.Minute)
	key := fmt.Sprintf("infra-metrics:svc=%s:since=%s", name, since)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetInfraMetrics(r.Context(), name, since, 0)
	})
}

// getServiceRuntime returns the technology fingerprint
// (language, SDK version, runtime name + version, host, OS)
// for a service, derived from the latest span's resource
// attributes. Powers the small "Java OpenJDK 21" / "Go 1.22"
// badge above the infra panel on /service?name=… so the
// operator immediately sees what stack they're investigating.
//
// Cached 5 min — the runtime changes only on deploy, no point
// re-reading per page load. The CH lookup is microsecond-fast
// even uncached (one row, partition-pruned + primary-key
// prefix on service_name).
func (s *Server) getServiceRuntime(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
		return
	}
	key := fmt.Sprintf("service-runtime:svc=%s", name)
	s.serveCached(w, r, key, 5*time.Minute, func() (any, error) {
		return s.store.GetServiceRuntime(r.Context(), name)
	})
}

// getAllServiceRuntimes returns the runtime fingerprint map
// for every service with recent traffic. Powers the runtime
// badge on the /services listing — one CH query (argMax over
// the last hour, grouped by service_name) replaces N
// per-service requests the listing would otherwise fan out.
//
// Cached 5 min — runtime changes only on deploy.
func (s *Server) getAllServiceRuntimes(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "all-service-runtimes", 5*time.Minute, func() (any, error) {
		return s.store.GetAllServiceRuntimes(r.Context())
	})
}

// getServiceMetadata returns the catalog row for one service
// (owner team, oncall channel, runbook URL, repo, etc.).
// Missing rows surface as 200 with an empty payload — the
// frontend renders an "Add metadata" CTA inline rather than
// distinguishing "404" from "no curation yet".
func (s *Server) getServiceMetadata(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
		return
	}
	m, err := s.store.GetServiceMetadata(r.Context(), name)
	if err != nil {
		writeErr(w, err)
		return
	}
	if m == nil {
		writeJSON(w, chstore.ServiceMetadata{Service: name})
		return
	}
	writeJSON(w, m)
}

// putServiceMetadata edits the catalog row. Editor+ role
// required (same as alert-rule edits). Body is the full
// ServiceMetadata payload; missing fields clear the column —
// last-write-wins via ReplacingMergeTree.
func (s *Server) putServiceMetadata(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
		return
	}
	var m chstore.ServiceMetadata
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	m.Service = name
	if err := s.store.UpsertServiceMetadata(r.Context(), m); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "service_metadata.update", "service_metadata", name, "")
	// v0.5.337 — invalidate the catalog cache on every peer so
	// the Owner-team chip on /services reflects the edit in
	// <50ms instead of waiting out the 60s soft TTL.
	// v0.5.344 — also invalidate svc-bundle:* (the service
	// detail page's combined fetch carries the catalog row).
	s.cacheInvalidate(r.Context(), "services-metadata")
	s.cacheInvalidatePrefix(r.Context(), "svc-bundle:")
	w.WriteHeader(http.StatusNoContent)
}

// listServiceMetadata returns every catalog row in one shot —
// drives the owner-team chip on the /services list without
// fanning out N requests at billion-span scale.
func (s *Server) listServiceMetadata(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "services-metadata", 60*time.Second, func() (any, error) {
		return s.store.ListServiceMetadata(r.Context())
	})
}

// getServiceDBQueries returns the top normalised DB statements
// for a service in a time window — Datadog-DBM style "where
// is my query time going" view. Backed by a single CH GROUP
// BY over normalised db_statement; sub-2s at billion-span
// scale because the dataset is already filtered to spans that
// actually touched a database.
//
// Cached 60s. The view is meant for ad-hoc investigation,
// not realtime — a one-minute cache trades perfect freshness
// for not re-running the regex GROUP BY on every panel
// expand.
// getServiceDeploys returns the distinct service.version
// timestamps for a service in a time window — drives the
// dashed vertical "deploy markers" the chart overlay paints
// on latency / error / volume charts. Cached 60s for the same
// reason as db-queries: the view is for ad-hoc investigation,
// not realtime.
func (s *Server) getServiceDeploys(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	if from.IsZero() || to.IsZero() {
		to = time.Now()
		from = to.Add(-1 * time.Hour)
	}
	key := fmt.Sprintf("service-deploys:svc=%s:from=%d:to=%d",
		name, from.UnixNano(), to.UnixNano())
	s.serveCached(w, r, key, time.Minute, func() (any, error) {
		return s.store.GetServiceDeploys(r.Context(), name, from, to)
	})
}

// getDeployHistory returns the last N deploys of a service +
// each one's before/after RED diff in one round-trip
// (v0.5.189). Powers the "Recent deploys" panel on the Service
// detail page — answers "did the last few releases regress
// p99 / error rate?" without N+1 client requests.
//
// Continuous benchmarking: as soon as a deploy completes,
// operators want a clear "clean / regression / partial" signal.
// The AI Copilot "Explain deploy" path covers the prose answer;
// this endpoint covers the raw numbers + delta so the panel
// renders instantly while the AI explain is opt-in.
//
// Defaults: last 5 deploys over a 24h lookback window; 10-min
// before/after windows for each impact compute. The impact
// queries are cheap (countIf + quantileIf on the partition-
// pruned spans table) — 5 deploys × 1 query each = sub-second
// at billion-span scale, then cached 60s.
func (s *Server) getDeployHistory(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	limit := parseInt(q.Get("limit"), 5)
	if limit <= 0 || limit > 30 {
		limit = 5
	}
	lookbackHours := parseInt(q.Get("lookbackHours"), 24)
	if lookbackHours <= 0 || lookbackHours > 30*24 {
		lookbackHours = 24
	}
	windowSec := parseInt(q.Get("windowSec"), 600)
	if windowSec <= 0 || windowSec > 6*3600 {
		windowSec = 600
	}
	to := time.Now()
	from := to.Add(-time.Duration(lookbackHours) * time.Hour)

	key := fmt.Sprintf("deploy-history:svc=%s:lim=%d:back=%d:win=%d",
		name, limit, lookbackHours, windowSec)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		deploys, err := s.store.GetServiceDeploys(r.Context(), name, from, to)
		if err != nil {
			return nil, err
		}
		// GetServiceDeploys returns ascending by first-seen — for
		// "recent" we want the newest at index 0, so reverse +
		// truncate. Skip deploys with very small span counts —
		// stragglers, not real deploys.
		type historyRow struct {
			Deploy chstore.Deploy        `json:"deploy"`
			Impact *chstore.DeployImpact `json:"impact"`
		}
		out := []historyRow{}
		for i := len(deploys) - 1; i >= 0 && len(out) < limit; i-- {
			d := deploys[i]
			if d.SpanCount < 5 {
				// straggler instance, not a real deploy
				continue
			}
			imp, err := s.store.ComputeDeployImpact(
				r.Context(), name, d.Version, d.TimeUnixNs, windowSec)
			if err != nil {
				// Best-effort — failed impact compute doesn't
				// hide the deploy itself; UI shows "—" deltas.
				log.Printf("[deploy-history] impact %s/%s: %v", name, d.Version, err)
				out = append(out, historyRow{Deploy: d, Impact: nil})
				continue
			}
			out = append(out, historyRow{Deploy: d, Impact: imp})
		}
		return out, nil
	})
}

func (s *Server) getServiceDBQueries(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	if from.IsZero() || to.IsZero() {
		// Sensible default: last 15 minutes. The /service
		// detail page sets explicit `from`/`to` from its
		// own range picker; the default just keeps the
		// endpoint useful for direct API use.
		to = time.Now()
		from = to.Add(-15 * time.Minute)
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	key := fmt.Sprintf("service-db-queries:svc=%s:from=%d:to=%d:limit=%d",
		name, from.UnixNano(), to.UnixNano(), limit)
	s.serveCached(w, r, key, time.Minute, func() (any, error) {
		return s.store.GetTopDBQueries(r.Context(), name, from, to, limit)
	})
}

// getSlowQueriesGlobal — cross-service slow-query catalog
// (v0.5.165). One row per (service, normalized statement) pair
// ordered by total wall-clock time. Filterable by db_system.
// 60s cache because operators tend to refresh this page when
// hunting "what's hot today" and the underlying GROUP BY is
// expensive at billion-span scale.
func (s *Server) getSlowQueriesGlobal(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, time.Hour)
	q := r.URL.Query()
	dbSystem := strings.TrimSpace(q.Get("db_system"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	key := fmt.Sprintf("slow-queries-global:from=%d:to=%d:sys=%s:limit=%d",
		from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute),
		dbSystem, limit)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		return s.store.GetSlowQueriesGlobal(r.Context(), from, to, dbSystem, limit)
	})
}

// getServiceBacktrace returns the inbound-callers detail for `name`
// over the requested window — Dynatrace-style consumer view. Each
// row is a unique (caller service × caller host/instance × client
// IP × user-agent) combination with RED stats, so the operator can
// pinpoint which client is driving traffic / errors.
//
// 60s cache: the underlying self-join is the heaviest query in the
// /service drill-down stack and the page is read-only / dashboard-
// like. Pod identity / IP set is stable on a session timescale; a
// minute-stale row is fine for "who's hammering me right now".
func (s *Server) getServiceBacktrace(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "service name required", http.StatusBadRequest); return
	}
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), time.Hour)
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	if from.IsZero() {
		from = time.Now().Add(-since)
	}
	if to.IsZero() {
		to = time.Now()
	}
	limit := parseInt(q.Get("limit"), 100)

	key := fmt.Sprintf("service-backtrace:svc=%s:since=%s:from=%s:to=%s:limit=%d",
		name, q.Get("since"), q.Get("from"), q.Get("to"), limit)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		// v0.5.368 — read from service_callers_5m MV. The
		// raw-spans ServiceCallers() runs a self-join that's
		// unviable at billion-span scale; topology aggregator
		// now pre-aggregates the rollup every 5 min so reads
		// stay sub-second regardless of fleet size.
		rows, err := s.store.ReadServiceCallersAgg(r.Context(), name, from, to, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"service": name,
			"callers": rows,
			"from":    from.UnixNano(),
			"to":      to.UnixNano(),
		}, nil
	})
}

// getServiceNeighbors returns the service-level upstream / downstream
// neighbours of `name` derived from sampled trace topology — the
// callers that invoke this service and the services it calls in
// turn. No peer.service heuristic; pure parent/child edge analysis
// over the recent N traces.
//
// Result is cached for 1h — the upstream / downstream service set
// shifts on a deploy / weekly / monthly cadence, not within a
// session, so an hour-stale list is functionally identical to a
// fresh one. The underlying query (top-N traces + bulk span fetch)
// is otherwise the most expensive thing on the service detail page.
// Cache key folds in service / since / samples so a range or
// sample-count change still misses.
// getServiceBlastRadius (v0.6.29) — caller-side impact summary
// for an inspected service. Reads service_callers_5m FINAL,
// rolls up per (caller_service) instead of per (caller_service,
// host, instance) so the chip-level UX shows the cleanest
// "↘ N svcs · M rps". Cascade flag joined in from the problems
// table so callers WITH their own open problem float to the
// top of the list.
//
// Cached 60s — the window is rate-of-the-fleet measurement
// where second-level freshness adds no operator value; tab-
// switching between problems repeatedly should hit the cache.
func (s *Server) getServiceBlastRadius(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
		return
	}
	since := parseDuration(r.URL.Query().Get("since"), time.Hour)
	if since > 24*time.Hour {
		since = 24 * time.Hour
	}
	key := fmt.Sprintf("service-blast-radius:svc=%s:since=%s", name, since)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		return s.store.GetServiceBlastRadius(r.Context(), name, since)
	})
}

func (s *Server) getServiceNeighbors(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "service name required", http.StatusBadRequest); return
	}
	since := parseDuration(r.URL.Query().Get("since"), time.Hour)
	samples := parseInt(r.URL.Query().Get("samples"), 50)

	key := fmt.Sprintf("service-neighbors:svc=%s:since=%s:samples=%d",
		name, since, samples)
	s.serveCached(w, r, key, time.Hour, func() (any, error) {
		upstream, downstream, sampledFrom, totalSpans, err := s.store.ServiceNeighbors(
			r.Context(), name, since, samples)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"service":     name,
			"upstream":    upstream,
			"downstream":  downstream,
			"sampledFrom": sampledFrom,
			"totalSpans":  totalSpans,
		}, nil
	})
}

// getServiceMap returns the global service-level topology graph
// (nodes + directed edges) derived from sampled recent traces.
// Mirrors the per-service ServiceNeighbors handler but globally —
// no anchor service. Result is cached for 30s: the topology
// changes on the order of deploys, not seconds, but operators
// will hit the page repeatedly while reading it; a 30s cache
// kills the redundant CH cost without making the view feel
// stale during an active investigation.
// getDatabases returns one row per (db_system, instance) tuple
// observed in span traffic over the supplied window. Drives the
// /databases overview — Dynatrace's "Technologies → Databases"
// equivalent. Cached 30s on the window so a fleet of operators
// scanning the page during morning triage doesn't hammer CH with
// duplicate GROUP BYs.
// getServiceAttrs — v0.5.381. Surfaces the per-key + sample-value
// distribution of attrs the operator's spans actually emit for a
// service. Answers "what attrs is my SDK putting on these spans"
// without forcing the operator to open a single trace and squint.
// 60s serveCached because the attr-shape rarely changes across
// a window — pinning the sample bounds (5K spans) keeps the scan
// fast even at billion-span/day scale.
func (s *Server) getServiceAttrs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	from, to := parseFromTo(r, time.Hour)
	top := parseInt(q.Get("top"), 50)
	samples := parseInt(q.Get("samples"), 5)
	key := fmt.Sprintf("service-attrs:svc=%s:from=%s:to=%s:top=%d:s=%d",
		name, cacheBucket(from, to), cacheBucket(from, to), top, samples)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		rows, err := s.store.GetServiceAttrs(r.Context(), name, from, to, top, samples)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"service": name,
			"attrs":   rows,
			"from":    from.UnixNano(),
			"to":      to.UnixNano(),
		}, nil
	})
}

// getEndpoints — v0.5.365. Per-endpoint RED rollup driven from
// the OTel http.route / url.path attributes on server / consumer
// spans. Operator-asked: a /services-like top-level view, but
// keyed on the inbound request path instead of the service. Top
// N by call volume — high-cardinality concrete paths (IDs that
// slipped past the http.route fallback) still surface but
// natural drop-off keeps the table scannable.
func (s *Server) getEndpoints(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to := parseFromTo(r, time.Hour)
	service := q.Get("service")
	search := q.Get("search")
	cluster := q.Get("cluster")
	limit := parseInt(q.Get("limit"), 500)
	// v0.5.389 — operator-reported: top-N was undercounting the
	// long tail. v0.5.395 — raised to 10000 + frontend exposes
	// an "All (10000)" option for the long-tail-heavy installs.
	// Payload is still bounded (10000 × 3 sparkline series × 30
	// buckets ≈ 7-8MB JSON worst case) but the operator opts
	// into the larger fetch by picking the explicit option, so
	// it can't sneak up on a casual page load.
	if limit > 10000 {
		limit = 10000
	}
	// v0.5.404 — optional prior-window comparison. When set to
	// "prior", a second GetEndpoints runs against the immediately-
	// preceding equal-length window (e.g. last 1h → previous 1h)
	// and the values are merged onto the current rows by
	// (service, path) key. Drives the "trend deltas" arrows on
	// the table. Opt-in (doubles CH cost) so the default page-
	// load stays a single scan.
	compare := q.Get("compare") == "prior"
	key := fmt.Sprintf("endpoints:%s:%s:%s:%s:%d:cmp=%v", cacheBucket(from, to), service, search, cluster, limit, compare)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		rows, err := s.store.GetEndpoints(r.Context(), from, to, service, search, cluster, limit)
		if err != nil {
			return nil, err
		}
		if !compare || len(rows) == 0 {
			return rows, nil
		}
		// Prior window: same length, shifted back by exactly the
		// window width so the comparison stays apples-to-apples.
		dur := to.Sub(from)
		priorFrom := from.Add(-dur)
		priorTo := from
		priorRows, err := s.store.GetEndpoints(r.Context(), priorFrom, priorTo, service, search, cluster, limit)
		if err != nil {
			// Prior failure is non-fatal — return current rows
			// without trends rather than 500'ing the page.
			return rows, nil
		}
		// Index prior by (service, path) so we don't do an O(N²)
		// linear scan when merging.
		type key struct{ svc, path string }
		idx := make(map[key]*chstore.EndpointRow, len(priorRows))
		for i := range priorRows {
			idx[key{priorRows[i].Service, priorRows[i].Path}] = &priorRows[i]
		}
		for i := range rows {
			if p, ok := idx[key{rows[i].Service, rows[i].Path}]; ok {
				rows[i].PriorCalls = p.Calls
				rows[i].PriorErrors = p.Errors
				rows[i].PriorAvgMs = p.AvgMs
				rows[i].PriorP99Ms = p.P99Ms
			}
		}
		return rows, nil
	})
}

func (s *Server) getDatabases(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, time.Hour)
	key := "databases:" + cacheBucket(from, to)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetDatabases(r.Context(), from, to)
	})
}

// getDatabaseDetail returns the drawer payload for one
// (db_system, instance) pair — per-(service, pod) caller
// breakdown plus the top db_statement prefixes. Cached 30s.
// Distinct cache keys per (system, instance, window) so the
// row click is sub-100ms warm cache.
func (s *Server) getDatabaseDetail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	system := q.Get("system")
	instance := q.Get("instance")
	if system == "" {
		http.Error(w, `{"error":"system required"}`, http.StatusBadRequest)
		return
	}
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("db-detail:%s:%s:%s", system, instance, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetDatabaseDetail(r.Context(), system, instance, from, to)
	})
}

// getOracleMetrics returns the OracleDB-receiver drill-down for
// one instance — sessions/processes utilisation, cumulative
// counter rates, tablespace usage. Falls back to deterministic
// synthetic data (flagged Synthetic=true in the payload) when
// no oracledb.* metric_points exist in the window so the panel
// still renders during integration setup.
func (s *Server) getOracleMetrics(w http.ResponseWriter, r *http.Request) {
	instance := r.URL.Query().Get("instance")
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("oracle:%s:%s", instance, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetOracleMetrics(r.Context(), instance, from, to)
	})
}

// getPostgresMetrics serves the Postgres receiver drill-down
// for the row-click drawer on /databases. Mirrors getOracleMetrics:
// 30s cache TTL bucketed to a 30s grid so morning-triage hits
// share one query trip even with rolling time windows.
func (s *Server) getPostgresMetrics(w http.ResponseWriter, r *http.Request) {
	instance := r.URL.Query().Get("instance")
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("postgres:%s:%s", instance, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetPostgresMetrics(r.Context(), instance, from, to)
	})
}

// getMySQLMetrics — MySQL receiver drill-down (buffer pool /
// threads / row-lock / slow queries / handlers / replica lag).
func (s *Server) getMySQLMetrics(w http.ResponseWriter, r *http.Request) {
	instance := r.URL.Query().Get("instance")
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("mysql:%s:%s", instance, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetMySQLMetrics(r.Context(), instance, from, to)
	})
}

// getRedisMetrics — Redis receiver drill-down (clients / memory /
// commands / hit rate / per-keyspace / replication / role).
func (s *Server) getRedisMetrics(w http.ResponseWriter, r *http.Request) {
	instance := r.URL.Query().Get("instance")
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("redis:%s:%s", instance, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetRedisMetrics(r.Context(), instance, from, to)
	})
}

// getMessaging is the parallel handler for queues / topics
// (Kafka / RabbitMQ / IBM MQ / etc.). Same caching semantics.
func (s *Server) getMessaging(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, time.Hour)
	key := "messaging:" + cacheBucket(from, to)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetMessaging(r.Context(), from, to)
	})
}

// getMessagingDetail is the parallel handler for queues /
// topics. Takes ?system=&cluster=&destination=&from=&to=. The
// cluster query param defaults to "(default)" for single-
// cluster deployments where the SPA hasn't been updated yet —
// matches the clusterExpr fallback in the store.
func (s *Server) getMessagingDetail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	system := q.Get("system")
	dest := q.Get("destination")
	cluster := q.Get("cluster")
	if cluster == "" {
		cluster = "(default)"
	}
	if system == "" {
		http.Error(w, `{"error":"system required"}`, http.StatusBadRequest)
		return
	}
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("msg-detail:%s:%s:%s:%s", system, cluster, dest, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetMessagingDetail(r.Context(), system, cluster, dest, from, to)
	})
}

// cacheBucket returns a key fragment with from/to snapped to a
// 30-second grid. Stops the dependencies / messaging caches
// from missing on every page load because the relative-time
// preset (`?range=1h`) produces a brand-new `to=now` value at
// nanosecond resolution every request. Two operators hitting
// the page within the same 30s window now share one upstream
// query instead of forcing two full CH scans.
//
// The cost is up to 30s of staleness on the to boundary — same
// staleness we already accept via the 30s TTL on the cache
// itself, so this is purely upside.
func cacheBucket(from, to time.Time) string {
	const grid = 30 * time.Second
	fb := from.Truncate(grid).UnixNano()
	tb := to.Truncate(grid).UnixNano()
	return fmt.Sprintf("%d_%d", fb, tb)
}

// parseFromTo unpacks the standard ?from=&to= unix-ns pair with
// a `default` window when either is missing. Both handlers above
// share the same shape so we lift the helper rather than
// repeating the parse-with-defaults dance per endpoint.
func parseFromTo(r *http.Request, defaultWindow time.Duration) (time.Time, time.Time) {
	q := r.URL.Query()
	to := parseTime(q.Get("to"))
	if to.IsZero() {
		to = time.Now()
	}
	from := parseTime(q.Get("from"))
	if from.IsZero() {
		from = to.Add(-defaultWindow)
	}
	return from, to
}

func (s *Server) getServiceMap(w http.ResponseWriter, r *http.Request) {
	since := parseDuration(r.URL.Query().Get("since"), 15*time.Minute)
	samples := parseInt(r.URL.Query().Get("samples"), 200)
	// `?diff=24h` enables baseline-vs-current topology diffing. New
	// services / dependencies show up flagged on the response and
	// disappeared ones populate RemovedNodes / RemovedEdges. Empty
	// or unparseable → no diff.
	diffStr := r.URL.Query().Get("diff")
	diff := parseDuration(diffStr, 0)

	key := fmt.Sprintf("service-map:since=%s:samples=%d:diff=%s", since, samples, diffStr)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		if diff > 0 {
			return s.store.GetServiceMapWithDiff(r.Context(), since, samples, diff, diffStr)
		}
		return s.store.GetServiceMap(r.Context(), since, samples)
	})
}

// getServiceSparklines returns one bucket array per service for the
// requested window, sourced from the 5-minute summary MV. Used by the
// services list to render thumbnail charts next to each row without a
// separate round-trip per service.
//
// Response shape:
//   { "<service>": [ { "t": <unix ns>, "spans": N, "errs": N,
//                      "avgMs": F, "p99Ms": F }, ... ] }
func (s *Server) getServiceSparklines(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), 24*time.Hour)
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	if from.IsZero() {
		from = time.Now().Add(-since)
	}
	if to.IsZero() {
		to = time.Now()
	}
	// Caller passes the visible service names (comma-separated). Without
	// this filter the response is one bucket array per service across the
	// full window — at 10k+ services that's multi-MB of JSON the browser
	// has to parse just to render 50 thumbnails. The cap (200) is a hard
	// safety net; the UI normally sends 50.
	wantSvcs := strings.Split(q.Get("services"), ",")
	cleaned := wantSvcs[:0]
	for _, s := range wantSvcs {
		if s = strings.TrimSpace(s); s != "" {
			cleaned = append(cleaned, s)
		}
		if len(cleaned) >= 200 {
			break
		}
	}
	wantSvcs = cleaned
	key := fmt.Sprintf("services-spark:from=%s:to=%s:svcs=%s",
		q.Get("from"), q.Get("to"), strings.Join(wantSvcs, ","))
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		rows, err := s.store.GetServiceSummary5mFor(r.Context(), wantSvcs, from, to)
		if err != nil {
			return nil, err
		}
		// Group by service. Map insertion preserves nothing on Go's side,
		// but the front-end indexes by name so order doesn't matter.
		out := map[string][]map[string]any{}
		for _, b := range rows {
			out[b.Service] = append(out[b.Service], map[string]any{
				"t":     b.BucketStart,
				"spans": b.SpanCount,
				"errs":  b.ErrorCount,
				"avgMs": b.AvgMs,
				"p99Ms": b.P99Ms,
			})
		}
		return out, nil
	})
}

// getServiceNames is the lookup endpoint behind every service-name picker
// in the UI. Distinct service names from the MV (cheap — already
// per-service grouped), with wildcard substring search and paging.
//
// Why a dedicated endpoint instead of /api/services?
//   /api/services is top-N capped (50 by default) so the dashboard can
//   render in sub-second on 10k-service installs. That cap leaks into
//   any picker that scraped the names from the same response — users
//   couldn't pick a less-busy service. This endpoint exists purely for
//   pickers and never aggregates anything beyond DISTINCT.
//
// Wildcard:
//   - "pay"     → substring match (LIKE '%pay%'), case-insensitive
//   - "pay*"    → prefix match (LIKE 'pay%')
//   - "*pay"    → suffix match (LIKE '%pay')
//   - "*pay*"   → explicit substring (same as plain "pay")
//   - "p?y"     → '?' becomes single-char wildcard (LIKE 'p_y')
// getOperationNames — operations-picker counterpart to
// getServiceNames (v0.5.180). Server-side substring / wildcard
// search over operation_summary_5m so a 10k-operation service
// stays usable in a dropdown. Same wildcard semantics as
// /api/service-names: bare query = substring, `*` and `?`
// honoured.
func (s *Server) getOperationNames(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := strings.TrimSpace(q.Get("service"))
	pattern := strings.TrimSpace(q.Get("q"))
	limit := parseInt(q.Get("limit"), 200)
	if limit > 1000 {
		limit = 1000
	}
	offset := parseInt(q.Get("offset"), 0)
	key := fmt.Sprintf("op-names:svc=%s:q=%s:limit=%d:offset=%d", service, pattern, limit, offset)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		names, total, err := s.store.ListOperationNames(r.Context(), service, pattern, limit, offset)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"names":   names,
			"total":   total,
			"hasMore": offset+len(names) < total,
		}, nil
	})
}

func (s *Server) getServiceNames(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pattern := strings.TrimSpace(q.Get("q"))
	limit := parseInt(q.Get("limit"), 200)
	if limit > 1000 {
		limit = 1000
	}
	offset := parseInt(q.Get("offset"), 0)

	key := fmt.Sprintf("svc-names:q=%s:limit=%d:offset=%d", pattern, limit, offset)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		names, total, err := s.store.ListServiceNames(r.Context(), pattern, limit, offset)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"names":   names,
			"total":   total,
			"hasMore": offset+len(names) < total,
		}, nil
	})
}

// getAttributeKeys returns the distinct attribute keys observed on
// recent spans, both span- and resource-scoped, with usage counts.
// Drives the FilterBuilder autocomplete so the operator's custom
// attributes (function_code, channel_code, ...) surface as picker
// suggestions instead of relying on the hardcoded list.
//
// Query window defaults to the last 1h (cheap on the indexed time
// column) and the result is capped server-side at 500 keys total.
// Cached 60s — distinct-keys tend not to change minute-to-minute.
func (s *Server) getAttributeKeys(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), time.Hour)
	limit := parseInt(q.Get("limit"), 500)
	if limit > 1000 {
		limit = 1000
	}
	// v0.5.261 — context-aware attribute suggester. When the
	// operator already has a filter set in /explore, the
	// dropdown should show attribute keys with data UNDER those
	// filters, not the global top-N. Filters arrive as a
	// JSON-encoded FilterExpr[] under `filters`; empty / missing
	// keeps the old global-scan behaviour.
	rawFilters := q.Get("filters")
	filters := parseFilters(rawFilters)

	key := fmt.Sprintf("attr-keys:since=%s:limit=%d:f=%s",
		q.Get("since"), limit, rawFilters)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		// Filter-derived WHERE fragment via the public chstore helper so we
		// don't reach into the package's internal whereClause type. AND-merged
		// with the time floor inside attributeKeysSQL for each union branch.
		filterSQL, filterArgs := chstore.BuildFilterWhere(filters)
		extra := ""
		if filterSQL != "" {
			extra = " AND " + strings.TrimPrefix(filterSQL, "WHERE ")
		}
		// v0.7.30 — sample the inner scan (see attributeKeysSQL). At billions of
		// spans the old full-window arrayJoin timed out (max_execution_time=30
		// → error → empty), and the Traces "Add column" picker showed "no more
		// attribute keys to add". The sample makes it O(sample), not O(window).
		sqlText := attributeKeysSQL(extra, attrKeysSampleRows)
		// Args layout: span(time, filter-args...), resource(time, filter-args...), limit
		secs := int64(since.Seconds())
		args := []any{secs}
		args = append(args, filterArgs...)
		args = append(args, secs)
		args = append(args, filterArgs...)
		args = append(args, limit)

		rows, err := s.store.Conn().Query(r.Context(), sqlText, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		type keyRow struct {
			Scope string `json:"scope"` // span | resource
			Key   string `json:"key"`
			Count uint64 `json:"count"`
		}
		out := []keyRow{}
		for rows.Next() {
			var r keyRow
			if err := rows.Scan(&r.Scope, &r.Key, &r.Count); err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, rows.Err()
	})
}

// attrKeysSampleRows bounds the per-branch inner scan in attributeKeysSQL.
// 200k recent spans is enough to surface essentially every attribute KEY
// (keys are low-cardinality and broadly present, unlike values) while keeping
// the query O(sample) instead of O(window) at billion-span scale.
const attrKeysSampleRows = 200_000

// attributeKeysSQL builds the distinct-attribute-keys discovery query that
// drives the Traces "Add column" picker + FilterBuilder autocomplete.
//
// v0.7.30 — Operator-reported: at billions of spans the previous query
// arrayJoin'd attr_keys/res_keys across the ENTIRE time window and blew past
// max_execution_time=30, returning an error → the picker showed "no more
// attribute keys to add". Each union branch now SAMPLES the inner scan with a
// LIMIT before the arrayJoin, so the cost is bounded by the sample size rather
// than the (unbounded) span count in the window. The resulting counts are
// sample-relative — used only to order keys by rough popularity, never as exact
// usage figures. `extra` is the optional filter fragment, already prefixed with
// " AND " (empty when no /explore filter is set).
func attributeKeysSQL(extra string, sampleRows int) string {
	return fmt.Sprintf(`
		SELECT scope, k, count() AS c FROM (
			SELECT 'span' AS scope, arrayJoin(attr_keys) AS k
			FROM (
				SELECT attr_keys FROM coremetry.spans
				WHERE time >= now() - toIntervalSecond(?)%s
				LIMIT %d
			)
			UNION ALL
			SELECT 'resource' AS scope, arrayJoin(res_keys) AS k
			FROM (
				SELECT res_keys FROM coremetry.spans
				WHERE time >= now() - toIntervalSecond(?)%s
				LIMIT %d
			)
		)
		GROUP BY scope, k
		ORDER BY c DESC
		LIMIT ?
		SETTINGS max_execution_time = 30`, extra, sampleRows, extra, sampleRows)
}

// getAttributeValues returns the most-frequent values observed for a
// given attribute key over a recent time window. Powers the
// FilterBuilder value autocomplete: as soon as the operator picks an
// attribute they get a real top-N value list, not a blank field.
//
// Two paths depending on the key:
//   1. Well-known semconv key with a dedicated structured column
//      (http.method → http_method, db.system → db_system, etc.) →
//      query the LowCardinality column directly. O(rows) but the
//      compressed column reads cheap.
//   2. Anything else → array index lookup
//      attr_values[indexOf(attr_keys, ?)] (or res_values for the
//      resource-scoped scope).
//
// Result is cached 60s in the same Redis layer the rest of the cheap-
// fan-out endpoints use, keyed by `key:since:limit` — so 100 SREs
// opening the same FilterBuilder generate one CH scan, not 100.
func (s *Server) getAttributeValues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rawKey := strings.TrimSpace(q.Get("key"))
	if rawKey == "" {
		http.Error(w, "key required", http.StatusBadRequest); return
	}
	if !isSafeAttrKey(strings.TrimPrefix(strings.TrimPrefix(rawKey, "resource."), "span.")) {
		http.Error(w, "invalid key", http.StatusBadRequest); return
	}
	since := parseDuration(q.Get("since"), time.Hour)
	limit := parseInt(q.Get("limit"), 200)
	if limit > 1000 { limit = 1000 }
	// v0.5.182 — optional `q` query for server-side substring /
	// wildcard search on the value. Without this filter, high-
	// cardinality attribute keys (http.url, db.statement) were
	// stuck with whatever the top-200 by count happened to be;
	// operators couldn't pick a long-tail value from the picker.
	pattern := strings.TrimSpace(q.Get("q"))
	var likeFilter string
	if pattern != "" {
		like := strings.NewReplacer(`*`, `%`, `?`, `_`).Replace(pattern)
		if !strings.ContainsAny(pattern, "*?") {
			like = "%" + like + "%"
		}
		likeFilter = like
	}

	cacheKey := fmt.Sprintf("attr-values:%s:since=%s:limit=%d:q=%s", rawKey, q.Get("since"), limit, pattern)
	s.serveCached(w, r, cacheKey, 60*time.Second, func() (any, error) {
		// Decide projection. The HTTP-layer attribute-key picker is
		// allowed to send `resource.X` and `span.X` prefixes; strip
		// them to map to the underlying column / array.
		scope := "span"
		key := rawKey
		switch {
		case strings.HasPrefix(rawKey, "resource."):
			scope = "resource"
			key = strings.TrimPrefix(rawKey, "resource.")
		case strings.HasPrefix(rawKey, "span."):
			key = strings.TrimPrefix(rawKey, "span.")
		}

		var sql string
		var args []any
		// Optional WHERE / HAVING fragment for the q filter — same
		// shape regardless of which projection path we take. We
		// apply it in HAVING for both paths so the alias `v` is
		// always in scope (WHERE doesn't see SELECT aliases).
		havingQ := ""
		if likeFilter != "" {
			havingQ = " AND v ILIKE ?"
		}
		if col, ok := chstore.WellKnownTraceCol[key]; ok && scope == "span" {
			// Structured-column fast path. Column is already
			// LowCardinality so the GROUP BY is cheap; cast to String
			// for uniform output even when the column is numeric.
			sql = fmt.Sprintf(`
				SELECT toString(%s) AS v, count() AS c
				FROM coremetry.spans
				WHERE time >= now() - toIntervalSecond(?)
				  AND %s != ''
				GROUP BY v
				HAVING 1=1%s
				ORDER BY c DESC
				LIMIT ?
				SETTINGS max_execution_time = 30`, col, col, havingQ)
			args = []any{int64(since.Seconds())}
			if likeFilter != "" {
				args = append(args, likeFilter)
			}
			args = append(args, limit)
		} else {
			// Array-lookup path. `has(...)` short-circuits the lookup
			// so rows that don't have the key contribute nothing to
			// the GROUP BY.
			arrKeys, arrVals := "attr_keys", "attr_values"
			if scope == "resource" {
				arrKeys, arrVals = "res_keys", "res_values"
			}
			sql = fmt.Sprintf(`
				SELECT %s[indexOf(%s, ?)] AS v, count() AS c
				FROM coremetry.spans
				WHERE time >= now() - toIntervalSecond(?)
				  AND has(%s, ?)
				GROUP BY v
				HAVING v != ''%s
				ORDER BY c DESC
				LIMIT ?
				SETTINGS max_execution_time = 30`, arrVals, arrKeys, arrKeys, havingQ)
			args = []any{key, int64(since.Seconds()), key}
			if likeFilter != "" {
				args = append(args, likeFilter)
			}
			args = append(args, limit)
		}

		rows, err := s.store.Conn().Query(r.Context(), sql, args...)
		if err != nil { return nil, err }
		defer rows.Close()
		type valRow struct {
			Value string `json:"value"`
			Count uint64 `json:"count"`
		}
		out := []valRow{}
		for rows.Next() {
			var v valRow
			if err := rows.Scan(&v.Value, &v.Count); err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, rows.Err()
	})
}

func (s *Server) getServiceGraph(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// service is required: the full-topology view is unusable past
	// ~500 services (force layout collapses, browser stalls), so
	// /api/services/graph now refuses to compute it. The /graph
	// page always sends a selected service; any caller without one
	// gets a 400 instead of a 30-second multi-thousand-edge query.
	service := q.Get("service")
	if service == "" {
		http.Error(w, "service query param required", http.StatusBadRequest)
		return
	}
	since := parseDuration(q.Get("since"), time.Hour)
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	// Cap the response to the top-N highest-traffic edges for the
	// requested service. Even one well-connected hub can fan out to
	// hundreds of edges; default tops out at 300, override via ?topN=.
	topN := parseInt(q.Get("topN"), 300)
	if topN < 1   { topN = 300 }
	if topN > 5000 { topN = 5000 }

	// 30s cache. The per-service neighbourhood is small and changes
	// only when new edges form; a 30-second window collapses every
	// re-render of the same selection into one CH round-trip.
	key := fmt.Sprintf("service-graph:svc=%s:since=%s:from=%s:to=%s:topN=%d",
		service, q.Get("since"), q.Get("from"), q.Get("to"), topN)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetServiceGraphTopN(r.Context(), service, since, from, to, topN)
	})
}

func (s *Server) getOperations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), 24*time.Hour)
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	key := fmt.Sprintf("operations:svc=%s:since=%s:from=%s:to=%s",
		q.Get("service"), q.Get("since"), q.Get("from"), q.Get("to"))
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetOperations(r.Context(), q.Get("service"), since, from, to)
	})
}

func (s *Server) getTraces(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := chstore.TraceFilter{
		Service:  q.Get("service"),
		Search:   q.Get("search"),
		TraceID:  strings.ToLower(strings.TrimSpace(q.Get("traceId"))),
		From:     parseTime(q.Get("from")),
		To:       parseTime(q.Get("to")),
		HasError: q.Get("hasError") == "true",
		RootOnly: q.Get("rootOnly") == "true",
		// services=A,B,…  — every listed service must appear in the
		// trace. Drives the backtrace 'Traces' drill-in so the user
		// gets only traces where caller × callee actually co-occur.
		// Cap at 8 so a malicious URL can't blow up the HAVING.
		RequireServices: func() []string {
			raw := q.Get("services")
			if raw == "" {
				return nil
			}
			out := []string{}
			for _, s := range strings.Split(raw, ",") {
				s = strings.TrimSpace(s)
				if s == "" {
					continue
				}
				out = append(out, s)
				if len(out) >= 8 {
					break
				}
			}
			return out
		}(),
		MinMs:    parseFloat(q.Get("minMs")),
		MaxMs:    parseFloat(q.Get("maxMs")),
		AttrKey:  q.Get("attrKey"),
		AttrVal:  q.Get("attrVal"),
		Sort:     q.Get("sort"),
		Order:    q.Get("order"),
		Limit:    parseInt(q.Get("limit"), 50),
		Offset:   parseInt(q.Get("offset"), 0),
	}
	filters, ferr := parseFiltersAndDSL(q.Get("filters"), q.Get("dsl"))
	if ferr != nil {
		http.Error(w, "invalid query DSL: "+ferr.Error(), http.StatusBadRequest)
		return
	}
	f.Filters = filters
	// Extra attribute columns. Comma-separated keys requested by the
	// /traces UI's column manager. Strict allow-list on characters
	// (alphanumeric + . _ -) so even though the value flows in as a
	// `?` param, no surprises end up in user-visible output. Cap at
	// 8 columns to keep the SELECT projection bounded.
	if extras := q.Get("extraAttrs"); extras != "" {
		for _, k := range strings.Split(extras, ",") {
			k = strings.TrimSpace(k)
			if k == "" || !isSafeAttrKey(k) {
				continue
			}
			f.ExtraAttrs = append(f.ExtraAttrs, k)
			if len(f.ExtraAttrs) >= 8 {
				break
			}
		}
	}
	// count mode — opt-in for the expensive count(DISTINCT trace_id):
	//   skip   (default) — no count; UI shows ">=N+1" when the page is full
	//   approx           — count over top N+1 trace_ids only (fast)
	//   exact            — full scan (the legacy behaviour, 30s+ at scale)
	f.CountMode = q.Get("count")
	if f.CountMode == "" {
		f.CountMode = "skip"
	}
	traces, total, hasMore, err := s.store.GetTraces(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := map[string]interface{}{"traces": traces, "hasMore": hasMore}
	// Only emit `total` when the caller actually computed one — clients
	// distinguish "unknown total" from "zero total" by the field's
	// presence, not its value.
	if f.CountMode != "skip" {
		resp["total"] = total
	}
	writeJSON(w, resp)
}

// exportTracesCSV serves the current /traces filter set as a
// downloadable CSV. Two operator workflows ask for this on
// every install:
//   • postmortem authors who want to paste rows into the
//     incident doc / postmortem template
//   • auditors who need a flat artifact of "what hit service
//     X over the last 24h"
//
// Same filter shape as /api/traces (we just rebuild the
// TraceFilter the same way) so the operator can configure
// the view in the UI then click Download and get exactly
// what's on screen — no separate query syntax to learn.
//
// Row cap is 10k (vs the UI's 50/page) — auditors usually
// want a fuller slice, but we stop short of a full table
// dump to keep the response sub-30s on a billion-span
// table.
func (s *Server) exportTracesCSV(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt(q.Get("limit"), 10000)
	if limit < 1 {
		limit = 10000
	}
	if limit > 50000 {
		limit = 50000
	}
	f := chstore.TraceFilter{
		Service:  q.Get("service"),
		Search:   q.Get("search"),
		TraceID:  strings.ToLower(strings.TrimSpace(q.Get("traceId"))),
		From:     parseTime(q.Get("from")),
		To:       parseTime(q.Get("to")),
		HasError: q.Get("hasError") == "true",
		RootOnly: q.Get("rootOnly") == "true",
		MinMs:    parseFloat(q.Get("minMs")),
		MaxMs:    parseFloat(q.Get("maxMs")),
		AttrKey:  q.Get("attrKey"),
		AttrVal:  q.Get("attrVal"),
		Sort:     q.Get("sort"),
		Order:    q.Get("order"),
		Limit:    limit,
		Offset:   0,
		CountMode: "skip",
	}
	filters, ferr := parseFiltersAndDSL(q.Get("filters"), q.Get("dsl"))
	if ferr != nil {
		http.Error(w, "invalid query DSL: "+ferr.Error(), http.StatusBadRequest)
		return
	}
	f.Filters = filters
	if extras := q.Get("extraAttrs"); extras != "" {
		for _, k := range strings.Split(extras, ",") {
			k = strings.TrimSpace(k)
			if k == "" || !isSafeAttrKey(k) {
				continue
			}
			f.ExtraAttrs = append(f.ExtraAttrs, k)
			if len(f.ExtraAttrs) >= 8 {
				break
			}
		}
	}

	rows, _, _, err := s.store.GetTraces(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}

	fname := fmt.Sprintf("coremetry-traces-%s.csv", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+fname+`"`)
	w.Header().Set("Cache-Control", "no-store")

	cw := csv.NewWriter(w)
	header := []string{
		"trace_id", "started_at", "duration_ms", "span_count",
		"root_service", "root_operation", "has_error",
	}
	for _, k := range f.ExtraAttrs {
		header = append(header, k)
	}
	if err := cw.Write(header); err != nil {
		return
	}
	for _, t := range rows {
		hasErr := "false"
		if t.HasError {
			hasErr = "true"
		}
		rec := []string{
			t.TraceID,
			time.Unix(0, t.StartTime).UTC().Format(time.RFC3339Nano),
			fmt.Sprintf("%.3f", t.DurationMs),
			fmt.Sprintf("%d", t.SpanCount),
			t.ServiceName,
			t.RootName,
			hasErr,
		}
		for _, k := range f.ExtraAttrs {
			rec = append(rec, t.Extras[k])
		}
		if err := cw.Write(rec); err != nil {
			return
		}
	}
	cw.Flush()
}

func (s *Server) getTraceAggregate(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// Custom attribute group-by key — strict allow-list keeps the
	// value safe to flow into the SQL via a `?` parameter (the
	// groupExpr also threads it as a parameter, never inlined).
	groupAttr := strings.TrimSpace(q.Get("groupAttr"))
	if groupAttr != "" && !isSafeAttrKey(groupAttr) {
		groupAttr = ""
	}
	f := chstore.AggregateFilter{
		GroupBy:   q.Get("groupBy"),
		GroupAttr: groupAttr,
		Service:   q.Get("service"),
		Search:    q.Get("search"),
		From:      parseTime(q.Get("from")),
		To:        parseTime(q.Get("to")),
		HasError:  q.Get("hasError") == "true",
		MinMs:     parseFloat(q.Get("minMs")),
		MaxMs:     parseFloat(q.Get("maxMs")),
		Sort:      q.Get("sort"),
		Order:     q.Get("order"),
		Limit:     parseInt(q.Get("limit"), 100),
	}
	filters, ferr := parseFiltersAndDSL(q.Get("filters"), q.Get("dsl"))
	if ferr != nil {
		http.Error(w, "invalid query DSL: "+ferr.Error(), http.StatusBadRequest)
		return
	}
	f.Filters = filters

	// 15s cache. /traces aggregated tab is the default landing
	// view; sort / group toggles re-call this and tend to repeat
	// the same predicate. Filter set goes through the raw query
	// string so the key is stable across distinct callers.
	key := fmt.Sprintf("traces-agg:%s", r.URL.RawQuery)
	s.serveCached(w, r, key, 15*time.Second, func() (any, error) {
		return s.store.GetTraceAggregate(r.Context(), f)
	})
}

func (s *Server) getTrace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "trace id required", http.StatusBadRequest); return
	}
	// 30s cache. A trace is immutable once stored, but a
	// just-flipped Tempo backend setting should rescue stale
	// "not found" entries within a short window — hence the
	// short TTL. `source` distinguishes CH-resident vs Tempo-
	// fallback so the frontend can banner-tag the result.
	key := "trace:" + id
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		spans, err := s.store.GetTrace(r.Context(), id)
		if err != nil {
			return nil, err
		}
		if len(spans) > 0 {
			return map[string]any{"traceId": id, "spans": spans, "source": "clickhouse"}, nil
		}
		// CH miss → Tempo fallback. Skip cleanly when Tempo isn't
		// configured. Errors from Tempo don't fail the whole
		// request — the operator gets the same empty result they
		// would have without the fallback, and a [tempo] log line
		// fingers the misconfig.
		if s.tempo != nil && s.tempo.Configured() {
			tspans, terr := s.tempo.LookupTrace(r.Context(), id)
			if terr != nil {
				log.Printf("[tempo] lookup %q: %v", id, terr)
			} else if len(tspans) > 0 {
				return map[string]any{"traceId": id, "spans": tspans, "source": "tempo"}, nil
			}
		}
		// v0.6.34 — operator-reported: /traces aggregate view
		// showed traces, clicking them opened an empty detail.
		// Root cause: trace_summary_5m MV has 90-day TTL while
		// raw `spans` has 30-day. Past day-30 the aggregate row
		// survives but the detail data is gone — and we returned
		// an opaque empty array. Check if the MV still holds the
		// trace and surface an honest "aged out of raw spans"
		// hint with the aggregate stats so the operator gets
		// SOMETHING useful instead of a blank pane.
		if stub, ok := s.store.GetTraceAggregateStub(r.Context(), id); ok {
			return map[string]any{
				"traceId":  id,
				"spans":    []any{},
				"source":   "mv_only",
				"stub":     stub,
			}, nil
		}
		return map[string]any{"traceId": id, "spans": spans, "source": "clickhouse"}, nil
	})
}

// createTraceSnapshot mints a public-share token for the requested
// trace. Editor / admin only (route-gated) — a viewer can read
// traces in the UI but should not be able to externalise them
// through a public link with no further auth. The public viewer
// endpoint is gated only by token possession + expiry. Default
// lifetime 24h; client can pass `?ttlHours=N` (capped to 30 days)
// for longer-lived shares — vendors / support tickets routinely
// need a working link past the 1-week mark.
func (s *Server) createTraceSnapshot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "trace id required", http.StatusBadRequest); return
	}
	// Validate the trace actually exists before issuing a token —
	// stops users from minting share links for typos.
	spans, err := s.store.GetTrace(r.Context(), id)
	if err != nil { writeErr(w, err); return }
	if len(spans) == 0 {
		http.Error(w, "trace not found", http.StatusNotFound); return
	}
	ttlHours := parseInt(r.URL.Query().Get("ttlHours"), 24)
	if ttlHours <= 0      { ttlHours = 24 }
	if ttlHours > 24*30   { ttlHours = 24 * 30 }

	c := auth.FromContext(r.Context())
	creator := ""
	if c != nil {
		creator = c.Email
	}
	snap := chstore.TraceSnapshot{
		TraceID:   id,
		CreatedBy: creator,
		ExpiresAt: time.Now().Add(time.Duration(ttlHours) * time.Hour).UnixNano(),
	}
	snap.Token = chstore.NewSnapshotToken()
	if err := s.store.CreateTraceSnapshot(r.Context(), snap); err != nil {
		writeErr(w, err); return
	}
	// Build the absolute URL the client should hand out. Honour the
	// X-Forwarded-Proto / X-Forwarded-Host headers so proxy / Route
	// /Ingress setups produce the public-facing host, not the
	// in-cluster one.
	scheme := "http"
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		scheme = v
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if v := r.Header.Get("X-Forwarded-Host"); v != "" {
		host = v
	}
	publicURL := fmt.Sprintf("%s://%s/public/trace?token=%s", scheme, host, snap.Token)
	s.audit(r, "trace_snapshot.create", "trace_snapshot", snap.Token,
		fmt.Sprintf(`{"traceId":%q,"ttlHours":%d}`, id, ttlHours))
	writeJSON(w, map[string]any{
		"token":     snap.Token,
		"url":       publicURL,
		"expiresAt": snap.ExpiresAt,
	})
}

// listTraceSnapshots returns the active (unexpired) public-share
// links for a trace. Used by the share popover so the operator
// sees what's already out there before minting another one —
// avoids duplicate links for the same trace and lets them
// revoke leaks.
func (s *Server) listTraceSnapshots(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "trace id required", http.StatusBadRequest)
		return
	}
	snaps, err := s.store.ListTraceSnapshots(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, snaps)
}

// revokeTraceSnapshot invalidates a share link immediately by
// setting its expires_at to now. The public viewer's expiry
// gate already returns 404 for expired tokens, so the next
// request from anyone holding the link gets the "Snapshot not
// found or expired" empty state. Editor / admin only (route-
// gated) — without this a viewer could nuke other operators'
// active shares, and the audit log only fingers "some viewer
// did it" after the support call has already failed.
func (s *Server) revokeTraceSnapshot(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.Error(w, "token required", http.StatusBadRequest)
		return
	}
	if err := s.store.RevokeTraceSnapshot(r.Context(), token); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "trace_snapshot.revoke", "trace_snapshot", token, "")
	writeJSON(w, map[string]string{"status": "revoked"})
}

// getPublicTrace resolves a public-share token and returns the
// associated trace's span list. No auth — token possession + non-
// expiry is the boundary. 404 covers both "no such token" and
// "expired" so we don't help an attacker enumerate.
func (s *Server) getPublicTrace(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	snap, err := s.store.GetTraceSnapshot(r.Context(), token)
	if err != nil { writeErr(w, err); return }
	if snap == nil {
		http.Error(w, "snapshot not found or expired", http.StatusNotFound); return
	}
	spans, err := s.store.GetTrace(r.Context(), snap.TraceID)
	if err != nil { writeErr(w, err); return }
	writeJSON(w, map[string]any{
		"traceId":   snap.TraceID,
		"spans":     spans,
		"expiresAt": snap.ExpiresAt,
		"createdBy": snap.CreatedBy,
	})
}

func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sev, _ := strconv.Atoi(q.Get("severity"))
	f := logstore.Filter{
		Service:     q.Get("service"),
		Cluster:     q.Get("cluster"),
		Search:      q.Get("search"),
		From:        parseTime(q.Get("from")),
		To:          parseTime(q.Get("to")),
		SeverityMin: uint8(sev),
		TraceID:     q.Get("traceId"),
		SpanID:      q.Get("spanId"),
		Limit:       parseInt(q.Get("limit"), 100),
		Offset:      parseInt(q.Get("offset"), 0),
	}
	page, err := s.logs.Search(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"total": page.Total, "logs": page.Logs})
}

// getLogsFields surfaces the searchable field paths discovered
// from the logs backend's index mapping. Used by the /logs UI's
// "available fields" hint so the operator sees what they can
// filter by without guessing. Only the Elasticsearch backend
// implements ListFields; ClickHouse returns an empty list (its
// shape is fixed and the UI already knows it).
//
// Cached 60s — mappings rarely change and an open /logs tab
// re-fetches on focus.
func (s *Server) getLogsFields(w http.ResponseWriter, r *http.Request) {
	type fielder interface {
		ListFields(ctx context.Context) ([]string, error)
	}
	s.serveCached(w, r, "logs-fields", 60*time.Second, func() (any, error) {
		f, ok := s.logs.(fielder)
		if !ok {
			return map[string]any{"fields": []string{}, "backend": s.logs.Backend()}, nil
		}
		fields, err := f.ListFields(r.Context())
		if err != nil {
			return nil, err
		}
		if fields == nil {
			fields = []string{}
		}
		return map[string]any{"fields": fields, "backend": s.logs.Backend()}, nil
	})
}

// getLogsFieldValues returns top values of a single keyword
// field matching a typed prefix (v0.5.464). Powers the /logs
// search box autocomplete that fires when the operator types
// "service.name:" — the dropdown then shows real service names
// from the indexed data. Sub-ms latency via ES _terms_enum on
// keyword subfields; CH backend returns an empty list.
//
// Cached briefly (30s) — values change slowly and the operator
// will type new prefixes in rapid succession (each is a fresh
// cache key).
// runLogsEQL executes an Elastic Event Query Language sequence
// search against the configured ES logs backend. Editor-gated —
// EQL can return arbitrary field shapes from the index and we
// want admin/editor accountability via audit. CH backend
// surfaces the "not supported" error from CHStore.EQLSearch;
// the frontend hides the panel on non-ES backends. v0.5.468.
//
// Not cached — EQL queries are operator-driven ad-hoc; caching
// per-keystroke iterations would just churn Redis without
// helping anyone.
func (s *Server) runLogsEQL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query string `json:"query"`
		From  int64  `json:"fromMs"`
		To    int64  `json:"toMs"`
		Size  int    `json:"size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	q := logstore.EQLQuery{
		Query: body.Query,
		Size:  body.Size,
	}
	if body.From > 0 {
		q.From = time.UnixMilli(body.From)
	}
	if body.To > 0 {
		q.To = time.UnixMilli(body.To)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	seqs, err := s.logs.EQLSearch(ctx, q)
	if err != nil {
		writeJSON(w, map[string]any{
			"sequences": []logstore.EQLSequence{},
			"error":     err.Error(),
		})
		return
	}
	if seqs == nil {
		seqs = []logstore.EQLSequence{}
	}
	s.audit(r, "logs.eql_run", "logs", "",
		fmt.Sprintf(`{"len":%d,"size":%d}`, len(body.Query), body.Size))
	writeJSON(w, map[string]any{
		"sequences": seqs,
		"backend":   s.logs.Backend(),
	})
}

// adminElasticIndices returns per-index doc count, size, health,
// and ILM lifecycle (policy + phase) for the configured logs
// backend's index pattern. Powers /admin/elastic. CH backend
// returns nil — the frontend renders a "logs backend isn't
// Elasticsearch" empty state instead of crashing. v0.5.466.
//
// Admin-only — exposes cluster metadata that's broadly fine to
// share but not interesting to a viewer. 30s cache: indices
// don't churn often.
func (s *Server) adminElasticIndices(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "admin-elastic-indices", 30*time.Second, func() (any, error) {
		idx, err := s.logs.Indices(r.Context())
		if err != nil {
			return nil, err
		}
		if idx == nil {
			idx = []logstore.IndexInfo{}
		}
		return map[string]any{
			"backend": s.logs.Backend(),
			"indices": idx,
		}, nil
	})
}

func (s *Server) getLogsFieldValues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	field := strings.TrimSpace(q.Get("field"))
	prefix := q.Get("q")
	limit := parseInt(q.Get("limit"), 20)
	if field == "" {
		writeJSON(w, map[string]any{"values": []string{}})
		return
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	key := fmt.Sprintf("logs-field-values:%s:%s:%d", field, prefix, limit)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		vals, err := s.logs.FieldValues(r.Context(), field, prefix, limit)
		if err != nil {
			// Don't 500 on bad field names — the autocomplete UI
			// already tolerates an empty list, and a typed
			// "lkjasdf:" shouldn't surface as a red banner.
			return map[string]any{"values": []string{}}, nil
		}
		if vals == nil {
			vals = []string{}
		}
		return map[string]any{"values": vals}, nil
	})
}

// getLogsSignificantPatterns runs the ES `significant_text`
// aggregation over (curStart, now) using the (baseStart, curStart)
// window as the baseline — surfaces tokens that just got rare-
// vs-usual. CH backend returns an empty list (no native
// equivalent at billion-row scale). 60s server cache fronts the
// expensive agg so a /logs reload hits Redis, not ES.
func (s *Server) getLogsSignificantPatterns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	curWindow := parseDuration(q.Get("window"), 15*time.Minute)
	if curWindow > time.Hour {
		curWindow = time.Hour
	}
	baseline := parseDuration(q.Get("baseline"), 24*time.Hour)
	if baseline > 24*time.Hour {
		baseline = 24 * time.Hour
	}
	if baseline <= curWindow {
		// Baseline must outweigh the cur window or the rare-vs-
		// baseline math is degenerate. Floor at 4x the cur
		// window so a 15min cur sees at least an hour of
		// background.
		baseline = 4 * curWindow
	}
	topN := parseInt(q.Get("topN"), 25)
	if topN <= 0 || topN > 100 {
		topN = 25
	}
	now := time.Now()
	curStart := now.Add(-curWindow)
	baseStart := curStart.Add(-baseline)

	key := fmt.Sprintf("logs-significant:cur=%s:bg=%s:topN=%d",
		curWindow, baseline, topN)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		// v0.5.390 — handler-level deadline. The ES backend now
		// soft-timeouts at 10s on its own (body timeout param),
		// so this 15s bound is defence in depth — covers the CH
		// backend (no native timeout knob) and any network
		// stall on the ES coordinator round-trip. Without it,
		// a wedged ES would keep the request open until the
		// frontend's 60s fetch budget cancels it, surfacing as
		// "context deadline exceeded" from the operator's view.
		// v0.5.424 — env-tunable handler deadline. Default 15s
		// works for most installs; billion-doc production can
		// bump to 25-45s. Should be < the frontend's 60s fetch
		// budget so the browser doesn't time out first.
		hd := 15 * time.Second
		if v := strings.TrimSpace(os.Getenv("COREMETRY_LOGS_PATTERNS_DEADLINE")); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				hd = d
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), hd)
		defer cancel()
		hits, err := s.logs.SignificantPatterns(ctx, curStart, baseStart, now, topN)
		// Graceful timeout: serve empty patterns + a hint flag
		// so the panel can render "still computing" rather than
		// the red error state. The 60s server cache means the
		// next poll lands on a fresh attempt, not the same
		// stuck call.
		if err != nil && (errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded")) {
			return map[string]any{
				"backend":   s.logs.Backend(),
				"window":    curWindow.String(),
				"baseline":  baseline.String(),
				"patterns":  []logstore.SignificantPattern{},
				"timedOut":  true,
			}, nil
		}
		if err != nil {
			return nil, err
		}
		if hits == nil {
			hits = []logstore.SignificantPattern{}
		}
		// v0.5.336 — drop opaque per-request ids from the live
		// patterns panel. JWT fragments and long base64 session
		// ids score statistically high (they appear in many
		// docs that share a request boundary) but tell the
		// operator nothing readable. Filter once at the API
		// layer so both ES + CH backends benefit.
		filtered := hits[:0]
		for _, h := range hits {
			if templater.LooksLikeOpaqueID(h.Token) {
				continue
			}
			filtered = append(filtered, h)
		}
		hits = filtered
		return map[string]any{
			"backend":   s.logs.Backend(),
			"window":    curWindow.String(),
			"baseline":  baseline.String(),
			"patterns":  hits,
		}, nil
	})
}

// getLogsTemplates surfaces the Drain-extracted log templates
// persisted by the templater puller. Sortable by first_seen
// (newest templates land first → "what just started firing?"),
// last_seen (recent activity) or total_count (busiest).
// 30s server cache fronts the read so the panel refresh is
// cheap.
func (s *Server) getLogsTemplates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sortBy := strings.TrimSpace(q.Get("sort"))
	if sortBy == "" {
		sortBy = "first_seen"
	}
	sinceDur := parseDuration(q.Get("since"), 24*time.Hour)
	limit := parseInt(q.Get("limit"), 50)
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	since := time.Now().Add(-sinceDur).UnixNano()

	key := fmt.Sprintf("logs-templates:sort=%s:since=%s:limit=%d",
		sortBy, sinceDur, limit)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		rows, err := s.store.ListLogTemplates(r.Context(), chstore.ListLogTemplatesFilter{
			SinceNs: since,
			SortBy:  sortBy,
			Limit:   limit,
		})
		if err != nil {
			return nil, err
		}
		if rows == nil {
			rows = []chstore.LogTemplate{}
		}
		return rows, nil
	})
}

// getLogsSimilarTraces bucket-counts trace IDs whose logs are
// pattern-similar to a seed text (more_like_this on the body
// field). POST body shape: {"text":"...","limit":50}. Returns
// {traces:[{traceId,count}]} sorted descending by count.
// Read-only against Elasticsearch; ClickHouse backend gets a
// clean 400 (no similar-text support).
func (s *Server) getLogsSimilarTraces(w http.ResponseWriter, r *http.Request) {
	type similarRunner interface {
		SimilarTraces(ctx context.Context, seed string, limit int) ([]logstore.SimilarTrace, error)
	}
	runner, ok := s.logs.(similarRunner)
	if !ok {
		http.Error(w, `{"error":"similar-logs lookup requires the Elasticsearch backend"}`, http.StatusBadRequest)
		return
	}
	var body struct {
		Text  string `json:"text"`
		Limit int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	body.Text = strings.TrimSpace(body.Text)
	if body.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	traces, err := runner.SimilarTraces(r.Context(), body.Text, body.Limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	if traces == nil {
		traces = []logstore.SimilarTrace{}
	}
	writeJSON(w, map[string]any{"traces": traces})
}

// getLogsContext returns ±N logs around a pivot timestamp, scoped
// to the same service. Datadog Context tab equivalent — operator
// clicks a log line, sees what was emitted just before and just
// after, without leaving /logs to rebuild a time-bounded filter
// by hand. Two parallel logstore.Search calls; one for the
// before window (DESC limit n) and one for the after window
// (ASC limit n). 30-minute symmetric window — wide enough to
// catch a slow incident, narrow enough that the search stays
// sub-second on a busy index.
func (s *Server) getLogsContext(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ts, _ := strconv.ParseInt(q.Get("ts"), 10, 64)
	if ts <= 0 {
		http.Error(w, "ts (unix ns) required", http.StatusBadRequest)
		return
	}
	service := q.Get("service")
	n := parseInt(q.Get("n"), 50)
	if n > 200 {
		n = 200
	}
	if n < 1 {
		n = 50
	}
	pivot := time.Unix(0, ts)
	// 30 minute symmetric window. Each Search returns the N most-
	// recent (before) or oldest (after) logs in its half-window —
	// at ingest pressure both halves rarely hit the cap, but the
	// hard ceiling keeps the round-trip bounded.
	beforeF := logstore.Filter{
		Service: service,
		From:    pivot.Add(-30 * time.Minute),
		To:      pivot,
		Limit:   n,
	}
	afterF := logstore.Filter{
		Service: service,
		From:    pivot,
		To:      pivot.Add(30 * time.Minute),
		Limit:   n,
	}
	key := fmt.Sprintf("logs-context:ts=%d:svc=%s:n=%d", ts, service, n)
	s.serveCached(w, r, key, 15*time.Second, func() (any, error) {
		beforePage, err := s.logs.Search(r.Context(), beforeF)
		if err != nil {
			return nil, err
		}
		afterPage, err := s.logs.Search(r.Context(), afterF)
		if err != nil {
			return nil, err
		}
		before := beforePage.Logs
		after := afterPage.Logs
		if before == nil {
			before = []*logstore.LogRecord{}
		}
		if after == nil {
			after = []*logstore.LogRecord{}
		}
		return map[string]any{
			"pivotTs": ts,
			"service": service,
			"before":  before,
			"after":   after,
		}, nil
	})
}

// getLogsTimeseries powers the Logs source on the Data Explorer
// page. Returns one time-bucketed series per groupBy value (e.g.
// per service / per severity), routed through whichever logstore
// backend is wired in (CH or external ES). 30s cache absorbs
// chart-rerender bursts as the operator tweaks dimensions.
func (s *Server) getLogsTimeseries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sev, _ := strconv.Atoi(q.Get("severity"))
	f := logstore.Filter{
		Service:     q.Get("service"),
		Search:      q.Get("search"),
		From:        parseTime(q.Get("from")),
		To:          parseTime(q.Get("to")),
		SeverityMin: uint8(sev),
		TraceID:     q.Get("traceId"),
	}
	bucketSec := parseInt(q.Get("bucketSec"), 30)
	// v0.5.259 — was: floor 5s. Lowered to 1s so the operator can
	// see per-second log surges on a short window. ES + CH both
	// handle 1s date_histogram fine; the cost is bucket count
	// (max 86400 if operator picks 1s on a 24h window — uncached
	// but bounded).
	if bucketSec < 1 {
		bucketSec = 1
	}
	if bucketSec > 86400 {
		bucketSec = 86400
	}
	groupBy := strings.TrimSpace(q.Get("groupBy"))
	key := fmt.Sprintf("logs-ts:svc=%s:sev=%d:trace=%s:from=%s:to=%s:b=%d:g=%s:q=%s",
		f.Service, f.SeverityMin, f.TraceID, q.Get("from"), q.Get("to"),
		bucketSec, groupBy, q.Get("search"))
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.logs.Histogram(r.Context(), f, bucketSec, groupBy)
	})
}

// getMetricNames now accepts q + limit + offset (v0.5.181) for
// the MetricNamePicker debounced search. Legacy callers that
// pass no params get the unlimited behaviour the response used
// to have. New shape is {names, total, hasMore}; the response
// type is conditional on the presence of `q` so older clients
// (just calling /api/metrics/names?service=X) still receive a
// plain MetricInfo[] body — no breaking change for the existing
// /metrics page until it migrates to the new picker.
func (s *Server) getMetricNames(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	svc := q.Get("service")
	pattern := strings.TrimSpace(q.Get("q"))
	limit := parseInt(q.Get("limit"), 0)
	offset := parseInt(q.Get("offset"), 0)
	// Old shape — no pagination params → return MetricInfo[]
	// like pre-v0.5.181 callers expect.
	if pattern == "" && limit == 0 && offset == 0 {
		s.serveCached(w, r, "metric-names:svc="+svc, 60*time.Second, func() (any, error) {
			return s.store.GetMetricNames(r.Context(), svc)
		})
		return
	}
	if limit > 1000 {
		limit = 1000
	}
	key := fmt.Sprintf("metric-names:svc=%s:q=%s:limit=%d:offset=%d", svc, pattern, limit, offset)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		names, total, err := s.store.ListMetricNames(r.Context(), svc, pattern, limit, offset)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"names":   names,
			"total":   total,
			"hasMore": offset+len(names) < total,
		}, nil
	})
}

func (s *Server) getMetrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pts, err := s.store.GetMetricPoints(r.Context(),
		q.Get("name"), q.Get("service"),
		parseTime(q.Get("from")), parseTime(q.Get("to")),
		parseInt(q.Get("limit"), 500),
	)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, pts)
}

// ── Metric query (multi-series, attribute filters, group-by) ─────────────────

func (s *Server) queryMetric(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	step, _ := strconv.Atoi(q.Get("step"))
	groupBy := splitNonEmpty(q.Get("groupBy"), ',')
	series, err := s.store.QueryMetric(r.Context(), chstore.MetricQueryFilter{
		Name:        q.Get("name"),
		Service:     q.Get("service"),
		Filters:     parseFilters(q.Get("filters")),
		GroupBy:     groupBy,
		Aggregation: q.Get("agg"),
		From:        parseTime(q.Get("from")),
		To:          parseTime(q.Get("to")),
		StepSeconds: step,
	})
	if err != nil { writeErr(w, err); return }
	writeJSON(w, series)
}

// getMetricHistogram renders an explicit-histogram metric as a time×bucket
// heatmap + per-bucket p50/p95/p99 (v0.6.56). Cached 30s; the key carries
// every result-affecting input — the window is bucketed to the minute so
// concurrent polls share a round-trip, and the full filter string is in the
// key (no length-only collapse, cf. v0.5.187).
func (s *Server) getMetricHistogram(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	step, _ := strconv.Atoi(q.Get("step"))
	name := q.Get("name")
	svc := q.Get("service")
	filtersRaw := q.Get("filters")
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	key := fmt.Sprintf("metric-hist:name=%s:svc=%s:step=%d:f=%s:from=%d:to=%d",
		name, svc, step, filtersRaw, from.Unix()/60, to.Unix()/60)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.QueryMetricHistogram(r.Context(), chstore.MetricQueryFilter{
			Name:        name,
			Service:     svc,
			Filters:     parseFilters(filtersRaw),
			From:        from,
			To:          to,
			StepSeconds: step,
		})
	})
}

func (s *Server) getMetricLabelValues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), 24*time.Hour)
	vals, err := s.store.MetricLabelValues(r.Context(), q.Get("metric"), q.Get("key"), since)
	if err != nil { writeErr(w, err); return }
	writeJSON(w, vals)
}

// SpanMetricServiceRow is one row of the span-metric-derived
// service overview — call volume + error fraction in the
// window, optionally augmented with histogram-derived latency
// once we wire that. Returned by /api/spanmetrics/services.
type SpanMetricServiceRow struct {
	Service    string  `json:"service"`
	Calls      uint64  `json:"calls"`
	Errors     uint64  `json:"errors"`
	ErrorRate  float64 `json:"errorRate"`
	AvgMs      float64 `json:"avgMs,omitempty"`
	MaxMs      float64 `json:"maxMs,omitempty"`
	P50Ms      float64 `json:"p50Ms,omitempty"`
	P99Ms      float64 `json:"p99Ms,omitempty"`
	// Inline call-rate sparkline — 30 buckets evenly spread
	// across the requested window. Float so a SVG renderer can
	// scale by max() without integer truncation; counts are
	// integers in the source data but we already pay the
	// float math in the aggregation step.
	Sparkline  []float64 `json:"sparkline,omitempty"`
	// Source metric names this row aggregated — surfaced to
	// the UI so the operator can confirm which spanmetrics
	// processor variant their collector is emitting.
	CallsMetric    string `json:"callsMetric,omitempty"`
	DurationMetric string `json:"durationMetric,omitempty"`
}

// getSpanMetricsByService reads the spanmetrics-processor
// metric stream (calls counter + duration histogram) and
// produces a per-service rollup that mirrors /api/services'
// RED table. Lets operators whose collectors emit spanmetrics
// see them as a first-class APM surface even when the SDK
// path isn't traced. Cached 30s.
//
// Metric naming covered (in priority order — first match wins):
//   • traces.spanmetrics.calls.total  +  traces.spanmetrics.duration.*
//   • traces_spanmetrics_calls_total  +  traces_spanmetrics_duration_*
//   • spanmetrics.calls               +  spanmetrics.duration
//   • calls                           +  duration
// Status filter: status.code = 'STATUS_CODE_ERROR' (canonical
// OTel) — matches the spanmetrics processor's emitted attr.
func (s *Server) getSpanMetricsByService(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	// v0.5.355 — Operator-reported scale: at 10k+ services
	// the sparkline + duration aggregates over the full set
	// took 10s+ to render. Default to top-200 services by
	// call volume; operator can override via ?top= up to 1000.
	top := parseInt(q.Get("top"), 200)
	if top > 1000 {
		top = 1000
	}
	if top < 10 {
		top = 10
	}
	// ?spark=0 disables the sparkline aggregation entirely
	// for the fastest possible load (drops one heavy GROUP
	// BY pass). Default ON.
	wantSparkline := q.Get("spark") != "0"
	key := fmt.Sprintf("spanmetrics-services:from=%d:to=%d:top=%d:spark=%v",
		from.Truncate(time.Minute).UnixNano(),
		to.Truncate(time.Minute).UnixNano(),
		top, wantSparkline)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		// Probe the metric catalogue for the call/duration
		// names actually flowing. One round-trip; tiny because
		// metric column is LowCardinality.
		var callsMetric, durationMetric string
		probeRows, err := s.store.Conn().Query(r.Context(), `
			SELECT DISTINCT metric
			FROM metric_points
			WHERE time >= ? AND time <= ?
			  AND (
			    positionUTF8(metric, 'spanmetrics') > 0
			    OR metric IN ('calls', 'duration')
			  )
			LIMIT 50
			SETTINGS max_execution_time = 5`, from, to)
		if err == nil {
			defer probeRows.Close()
			var names []string
			for probeRows.Next() {
				var n string
				if probeRows.Scan(&n) == nil {
					names = append(names, n)
				}
			}
			callsMetric, durationMetric = pickSpanMetricNames(names)
		}
		if callsMetric == "" {
			// No span-metric stream detected — empty result is
			// the correct response (clients render a setup-
			// instructions empty state).
			return map[string]any{
				"rows":           []SpanMetricServiceRow{},
				"callsMetric":    "",
				"durationMetric": "",
			}, nil
		}
		// Stage 1 — top-N services by call volume, read from
		// spanmetrics_calls_5m. v0.5.357: replaces the raw
		// metric_points scan with an AggregatingMergeTree MV
		// pre-aggregated per (service, 5min). At 10k+ services
		// the MV is ~5min-window-bucketed = sub-second even on
		// the full set; the top-N cap stays as a sanity bound.
		// time_bucket is 5-min aligned so we bucket-down the
		// `from` to include the bucket overlapping the window
		// (same alignment as service_summary_5m reads).
		bucketStart := from.Truncate(5 * time.Minute)
		topRows, err := s.store.Conn().Query(r.Context(), `
			SELECT service_name,
			       sumMerge(calls_state)  AS calls,
			       sumMerge(errors_state) AS errors
			FROM spanmetrics_calls_5m
			WHERE time_bucket >= ? AND time_bucket <= ?
			GROUP BY service_name
			ORDER BY calls DESC
			LIMIT ?
			SETTINGS max_execution_time = 10`,
			bucketStart, to, top)
		if err != nil {
			return nil, fmt.Errorf("spanmetrics top: %w", err)
		}
		defer topRows.Close()
		var out []SpanMetricServiceRow
		services := make([]string, 0, top)
		for topRows.Next() {
			var row SpanMetricServiceRow
			if err := topRows.Scan(&row.Service, &row.Calls, &row.Errors); err != nil {
				continue
			}
			if row.Calls > 0 {
				row.ErrorRate = float64(row.Errors) / float64(row.Calls) * 100
			}
			row.CallsMetric = callsMetric
			row.DurationMetric = durationMetric
			out = append(out, row)
			services = append(services, row.Service)
		}
		if len(out) == 0 {
			return map[string]any{
				"rows":           out,
				"callsMetric":    callsMetric,
				"durationMetric": durationMetric,
				"top":            top,
				"truncated":      false,
			}, nil
		}
		// Build the IN-list arg block; share across stages 2/3.
		holders := strings.Repeat("?,", len(services))
		holders = holders[:len(holders)-1]
		svcArgs := make([]any, 0, len(services))
		for _, s := range services {
			svcArgs = append(svcArgs, s)
		}

		// Stage 2 — sparkline. v0.5.357: reads pre-aggregated
		// 5-min buckets from spanmetrics_calls_5m. Since the
		// MV is already 5-min bucketed we re-bucket at read
		// time (intDiv on toUnixTimestamp(time_bucket)) into
		// sparkBuckets bins across the window. For windows
		// shorter than 30×5min=150min the bin width snaps to
		// the MV's 5-min granularity (multiple bins map to
		// one MV bucket — coalesce handles it).
		if wantSparkline {
			const sparkBuckets = 30
			bucketNs := (to.UnixNano() - from.UnixNano()) / int64(sparkBuckets)
			if bucketNs <= 0 {
				bucketNs = 1
			}
			sparkArgs := []any{from.UnixNano(), bucketNs, bucketStart, to}
			sparkArgs = append(sparkArgs, svcArgs...)
			sparkArgs = append(sparkArgs, sparkBuckets)
			sparkRows, serr := s.store.Conn().Query(r.Context(), `
				WITH per_bin AS (
				  SELECT service_name,
				         intDiv(toUnixTimestamp(time_bucket) * 1000000000 - ?, ?) AS b,
				         sumMerge(calls_state)                       AS bv
				  FROM spanmetrics_calls_5m
				  WHERE time_bucket >= ? AND time_bucket <= ?
				    AND service_name IN (`+holders+`)
				  GROUP BY service_name, b
				)
				SELECT service_name,
				       arrayMap(i ->
				         toFloat64(coalesce(arrayElement(b_vals, indexOf(b_idx, i)), 0)),
				         range(0, ?)
				       )                                        AS sparkline
				FROM (
				  SELECT service_name,
				         groupArray(b)                          AS b_idx,
				         groupArray(bv)                         AS b_vals
				  FROM per_bin
				  GROUP BY service_name
				)
				SETTINGS max_execution_time = 10`, sparkArgs...)
			if serr == nil {
				defer sparkRows.Close()
				sparkMap := make(map[string][]float64, len(out))
				for sparkRows.Next() {
					var svc string
					var sl []float64
					if err := sparkRows.Scan(&svc, &sl); err == nil {
						sparkMap[svc] = sl
					}
				}
				for i := range out {
					if sl, ok := sparkMap[out[i].Service]; ok {
						out[i].Sparkline = sl
					}
				}
			}
		}

		// Stage 3 — duration aggregate. v0.5.357: reads from
		// spanmetrics_duration_5m. avgMs = ΣsumMerge / ΣcountMerge,
		// maxMs = maxMerge. ×1000 converts spanmetrics-default
		// seconds to ms.
		if durationMetric != "" {
			durArgs := []any{bucketStart, to}
			durArgs = append(durArgs, svcArgs...)
			durRows, derr := s.store.Conn().Query(r.Context(), `
				SELECT service_name,
				       sumMerge(sum_state) / nullIf(sumMerge(count_state), 0) AS avg_s,
				       maxMerge(max_state)                                    AS max_s
				FROM spanmetrics_duration_5m
				WHERE time_bucket >= ? AND time_bucket <= ?
				  AND service_name IN (`+holders+`)
				GROUP BY service_name
				SETTINGS max_execution_time = 8`, durArgs...)
			if derr == nil {
				defer durRows.Close()
				durMap := make(map[string]struct{ Avg, Max float64 }, len(out))
				for durRows.Next() {
					var svc string
					var avg, mx *float64
					if err := durRows.Scan(&svc, &avg, &mx); err != nil {
						continue
					}
					a, m := 0.0, 0.0
					if avg != nil {
						a = *avg
					}
					if mx != nil {
						m = *mx
					}
					durMap[svc] = struct{ Avg, Max float64 }{a, m}
				}
				for i := range out {
					if d, ok := durMap[out[i].Service]; ok {
						out[i].AvgMs = d.Avg * 1000
						out[i].MaxMs = d.Max * 1000
					}
				}
			}
		}
		// Stage 4 — quantile estimation from histogram buckets.
		// v0.5.359: reads from spanmetrics_hist_5m (the MV
		// added in v0.5.359). anyMerge picks the prevailing
		// bucket layout per (service, 5min); sumMapMerge
		// element-wise sums the per-bucket counts via the
		// (key→value) map state. CH driver returns the merged
		// map as a tuple of two parallel arrays (keys[],
		// values[]); we reconstruct the dense counts array
		// from those.
		if durationMetric != "" {
			qArgs := []any{bucketStart, to}
			qArgs = append(qArgs, svcArgs...)
			qRows, qerr := s.store.Conn().Query(r.Context(), `
				SELECT service_name,
				       anyMerge(bounds_state)    AS bounds,
				       sumMapMerge(counts_state) AS counts_map
				FROM spanmetrics_hist_5m
				WHERE time_bucket >= ? AND time_bucket <= ?
				  AND service_name IN (`+holders+`)
				GROUP BY service_name
				SETTINGS max_execution_time = 8`, qArgs...)
			if qerr == nil {
				defer qRows.Close()
				type qRow struct{ P50, P99 float64 }
				qMap := make(map[string]qRow, len(out))
				for qRows.Next() {
					var svc string
					var bounds []float64
					// sumMap(Array(UInt32), Array(UInt64)) →
					// Tuple(Array(UInt32), Array(UInt64)). The
					// clickhouse-go driver surfaces that as a
					// []any of two parallel typed arrays.
					var tup []any
					if err := qRows.Scan(&svc, &bounds, &tup); err != nil {
						continue
					}
					counts := densifyBucketMap(tup)
					if len(bounds) == 0 || len(counts) == 0 {
						continue
					}
					qMap[svc] = qRow{
						P50: histQuantile(bounds, counts, 0.50) * 1000,
						P99: histQuantile(bounds, counts, 0.99) * 1000,
					}
				}
				for i := range out {
					if q, ok := qMap[out[i].Service]; ok {
						out[i].P50Ms = q.P50
						out[i].P99Ms = q.P99
					}
				}
			}
		}

		// Surface the cap so the frontend can render a hint
		// when truncation kicked in. truncated=true when the
		// top-N pass returned exactly `top` rows — there might
		// be more services not shown.
		return map[string]any{
			"rows":           out,
			"callsMetric":    callsMetric,
			"durationMetric": durationMetric,
			"top":            top,
			"truncated":      len(out) >= top,
		}, nil
	})
}

// densifyBucketMap turns the (keys[], values[]) tuple
// returned by sumMapMerge back into a dense bucket_counts
// array indexed 0..maxKey. sumMap drops zero-count buckets,
// so this fills them back in so the histQuantile walk works
// against a contiguous index. v0.5.359 — used by the span
// metrics quantile stage.
func densifyBucketMap(tup []any) []uint64 {
	if len(tup) != 2 {
		return nil
	}
	keys, _ := tup[0].([]uint32)
	vals, _ := tup[1].([]uint64)
	if len(keys) == 0 || len(keys) != len(vals) {
		return nil
	}
	var maxKey uint32
	for _, k := range keys {
		if k > maxKey {
			maxKey = k
		}
	}
	dense := make([]uint64, maxKey+1)
	for i, k := range keys {
		dense[k] = vals[i]
	}
	return dense
}

// histQuantile estimates the q-th quantile from an OTel
// histogram's bucket layout. v0.5.358 — used by the span
// metrics page to derive p50/p99 from the bucket bounds +
// per-bucket counts that the OTLP histogram ingest now
// preserves.
//
// Layout:
//   • bounds = [b0, b1, ..., bN-1]   — N explicit upper bounds
//   • counts = [c0, c1, ..., cN]     — N+1 buckets total
//                                       (counts[i] = count of
//                                        observations ≤ bounds[i];
//                                        counts[N] = +Inf bucket)
//
// Computation:
//   1. total = Σ counts
//   2. target = total × q
//   3. Walk buckets accumulating counts; find first i where
//      cumulative ≥ target.
//   4. Linear-interpolate inside the bucket: lower bound is
//      bounds[i-1] (or 0 for the first bucket); upper is
//      bounds[i]; fraction = (target - prev_cumulative) / c.
//   5. The +Inf bucket (i == N) returns bounds[N-1] as a
//      conservative best estimate — caller's MaxMs covers the
//      "actually higher than the last finite bound" case.
func histQuantile(bounds []float64, counts []uint64, q float64) float64 {
	if len(counts) == 0 || q <= 0 || q > 1 {
		return 0
	}
	var total uint64
	for _, c := range counts {
		total += c
	}
	if total == 0 {
		return 0
	}
	target := float64(total) * q
	var cum uint64
	for i, c := range counts {
		cum += c
		if float64(cum) >= target {
			// +Inf bucket — no upper bound to interpolate to.
			if i >= len(bounds) {
				if len(bounds) > 0 {
					return bounds[len(bounds)-1]
				}
				return 0
			}
			prevCum := float64(cum) - float64(c)
			lower := 0.0
			if i > 0 {
				lower = bounds[i-1]
			}
			upper := bounds[i]
			if c == 0 {
				return upper
			}
			frac := (target - prevCum) / float64(c)
			return lower + frac*(upper-lower)
		}
	}
	return 0
}

// pickSpanMetricNames picks the call-counter + duration metric
// names from a list of candidate metric names. Returns ("", "")
// when nothing matches. Priority order matches the spanmetrics
// processor naming conventions across versions.
func pickSpanMetricNames(names []string) (callsM, durationM string) {
	preferenceCalls := []string{
		"traces.spanmetrics.calls.total",
		"traces_spanmetrics_calls_total",
		"spanmetrics.calls",
		"spanmetrics_calls_total",
		"calls",
	}
	preferenceDur := []string{
		"traces.spanmetrics.duration",
		"traces.spanmetrics.duration.seconds.sum",
		"traces_spanmetrics_duration",
		"spanmetrics.duration",
		"duration",
	}
	for _, want := range preferenceCalls {
		for _, n := range names {
			if n == want {
				callsM = n
				break
			}
		}
		if callsM != "" {
			break
		}
	}
	for _, want := range preferenceDur {
		for _, n := range names {
			if n == want {
				durationM = n
				break
			}
		}
		if durationM != "" {
			break
		}
	}
	return
}

// ── Span metrics (Tempo span-metrics generator + Dynatrace MDA) ──────────────

// spanFacets returns top-N distinct values for each well-known tag
// column over the supplied window + DSL filter. Powers the trace
// facets sidebar on /explore — operator scans which tags are heavy
// and clicks a value to add it as a filter. Datadog's "trace tag
// explorer" pattern.
//
// Cached 30s on (filters, window, topValues) so an operator pivoting
// through facet clicks doesn't pay the per-facet COUNT GROUP BY each
// time. Cheap on its own (LowCardinality dicts on every column),
// but the savings compound across N facet clicks.
func (s *Server) spanFacets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filters, err := parseFiltersAndDSL(q.Get("filters"), q.Get("dsl"))
	if err != nil {
		http.Error(w, "invalid query DSL: "+err.Error(), http.StatusBadRequest)
		return
	}
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		from = to.Add(-1 * time.Hour)
	}
	topValues := parseInt(q.Get("topValues"), 8)
	key := fmt.Sprintf("facets:%s:%d:%d:%d",
		q.Get("dsl")+"|"+q.Get("filters"),
		from.UnixNano(), to.UnixNano(), topValues)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetSpanFacets(r.Context(), filters, from, to, topValues)
	})
}

// spanRepeats — "find requests where the same span shape happened
// N+ times" view. Powers the Explore "Repeats" result mode: the
// operator picks a group-by (db.statement, name+peer.service,
// http.route, …) plus a minimum repeat count, gets back a ranked
// list of (trace_id, group-by-values, count). Classic N+1 query
// detector + chatty-RPC finder.
//
// Cached 30s per param tuple; the underlying GROUP BY is partition-
// pruned + bounded at LIMIT 200 inside the store.
func (s *Server) spanRepeats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filters, err := parseFiltersAndDSL(q.Get("filters"), q.Get("dsl"))
	if err != nil {
		http.Error(w, "invalid query DSL: "+err.Error(), http.StatusBadRequest)
		return
	}
	from, to := parseFromTo(r, time.Hour)
	groupBy := splitNonEmpty(q.Get("groupBy"), ',')
	minRepeats := parseInt(q.Get("minRepeats"), 5)
	limit := parseInt(q.Get("limit"), 200)
	key := fmt.Sprintf("repeats:%s:%s:%s:%d:%d:%d:%d",
		q.Get("dsl"), q.Get("filters"), strings.Join(groupBy, ","),
		minRepeats, limit, from.UnixNano(), to.UnixNano())
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.QueryRepeatedSpans(r.Context(), chstore.RepeatedSpanFilter{
			Filters: filters, GroupBy: groupBy, MinRepeats: minRepeats,
			From: from, To: to, Limit: limit,
		})
	})
}

func (s *Server) spanMetric(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	groupBy := splitNonEmpty(q.Get("groupBy"), ',')
	step, _ := strconv.Atoi(q.Get("step"))
	filters, err := parseFiltersAndDSL(q.Get("filters"), q.Get("dsl"))
	if err != nil {
		http.Error(w, "invalid query DSL: "+err.Error(), http.StatusBadRequest)
		return
	}
	f := chstore.SpanMetricFilter{
		Filters:     filters,
		Aggregation: q.Get("agg"),
		Field:       q.Get("field"),
		GroupBy:     groupBy,
		From:        parseTime(q.Get("from")),
		To:          parseTime(q.Get("to")),
		StepSeconds: step,
		Search:      q.Get("search"), // v0.6.32 — push search down to histogram
	}
	series, err := s.store.QuerySpanMetric(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, series)
}

// spanMetricBatch runs N aggregations over the same span
// selection in a single CH query. The Service detail page's
// RED chart row used to fire 3 separate /api/spans/metric
// calls (rate, error_rate, p99) — each scanning the same
// spans, just running a different aggregation. This endpoint
// collapses all three into one pass, dropping cold-cache
// time from ~3× to ~1× single. Compare-period fires the same
// reduction on the second chart row.
//
// Request body shape:
//
//	{
//	  "from":  <unix ns>, "to":  <unix ns>,
//	  "step":  <seconds | 0 for auto>,
//	  "groupBy": ["name", ...],
//	  "filters": [{"key":"service.name","op":"=","values":["foo"]}, ...],
//	  "dsl":   "service.name = 'foo'",   // optional, OR with filters
//	  "aggs": [
//	    {"name":"rate",       "agg":"rate"},
//	    {"name":"error_rate", "agg":"error_rate"},
//	    {"name":"p99",        "agg":"p99", "field":"duration_ms"}
//	  ]
//	}
//
// Response:
//
//	{ "rate": [SpanMetricSeries…],
//	  "error_rate": […],
//	  "p99": […] }
func (s *Server) spanMetricBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From    int64    `json:"from"`
		To      int64    `json:"to"`
		Step    int      `json:"step"`
		GroupBy []string `json:"groupBy"`
		Filters json.RawMessage `json:"filters"`
		DSL     string   `json:"dsl"`
		Aggs    []struct {
			Name string `json:"name"`
			Agg  string `json:"agg"`
			Field string `json:"field"`
		} `json:"aggs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.Aggs) == 0 {
		http.Error(w, "at least one agg required", http.StatusBadRequest)
		return
	}
	filters, err := parseFiltersAndDSL(string(body.Filters), body.DSL)
	if err != nil {
		http.Error(w, "invalid query DSL: "+err.Error(), http.StatusBadRequest)
		return
	}
	specs := make([]chstore.SpanMetricAggSpec, len(body.Aggs))
	for i, a := range body.Aggs {
		specs[i] = chstore.SpanMetricAggSpec{
			Name:        a.Name,
			Aggregation: a.Agg,
			Field:       a.Field,
		}
	}
	f := chstore.SpanMetricBatchFilter{
		Filters:     filters,
		GroupBy:     body.GroupBy,
		From:        time.Unix(0, body.From),
		To:          time.Unix(0, body.To),
		StepSeconds: body.Step,
		Aggs:        specs,
	}
	out, err := s.store.QuerySpanMetricMulti(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out)
}

// dashboardsData multiplexes N panel data requests into one
// HTTP round trip. Each request describes ONE dashboard
// panel's underlying query (metric_points read, span_metric
// aggregation, or a stat-tile probe); the handler dispatches
// each to the appropriate store method in its own goroutine,
// collects results into a map keyed by the request id, and
// returns them all at once.
//
// Why this exists: a 10-panel dashboard used to fire 10
// separate /api/metrics/query or /api/spans/metric round
// trips on mount. With HTTP/1.1 the browser caps at ~6
// concurrent connections so the last 4 panels stalled.
// Even on HTTP/2 the TLS / cookie / cors overhead per
// request hurt at billion-span scale. One bundle endpoint
// gives the frontend parallel server-side dispatch + one
// network round trip; the underlying store helpers still
// hit their own L1 / Redis caches so the warm path is
// unchanged.
//
// Per-panel error stays per-panel: a CH blip on one query
// returns { error } in that slot, the others render fine.
//
// Request body:
//
//	{ "from": <unix ns>, "to": <unix ns>,
//	  "requests": [
//	    { "id": "p1", "type": "metric",
//	      "name": "...", "service": "...", "agg": "...",
//	      "groupBy": [...], "step": 60 },
//	    { "id": "p2", "type": "spanMetric",
//	      "agg": "p99", "field": "duration_ms",
//	      "filters": "<json>", "dsl": "...",
//	      "groupBy": [...], "step": 60 },
//	  ] }
//
// Response:
//
//	{ "p1": { "series": [...], "error": null },
//	  "p2": { "series": [...], "error": "..." } }
func (s *Server) dashboardsData(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From     int64 `json:"from"`
		To       int64 `json:"to"`
		Requests []struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			// metric
			Name    string `json:"name"`
			Service string `json:"service"`
			// shared
			Agg     string   `json:"agg"`
			Field   string   `json:"field"`
			GroupBy []string `json:"groupBy"`
			Step    int      `json:"step"`
			// span metric
			Filters json.RawMessage `json:"filters"`
			DSL     string          `json:"dsl"`
		} `json:"requests"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.Requests) == 0 {
		writeJSON(w, map[string]any{})
		return
	}
	if len(body.Requests) > 50 {
		http.Error(w, "too many requests (max 50 per bundle)", http.StatusBadRequest)
		return
	}
	from := time.Unix(0, body.From)
	to := time.Unix(0, body.To)

	type slot struct {
		Series []chstore.SpanMetricSeries `json:"series,omitempty"`
		Error  string                     `json:"error,omitempty"`
	}
	out := make(map[string]*slot, len(body.Requests))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, req := range body.Requests {
		req := req
		out[req.ID] = &slot{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			var series []chstore.SpanMetricSeries
			var err error
			switch req.Type {
			case "metric":
				// metric_points read — same shape the
				// /api/metrics/query handler builds.
				series, err = s.store.QueryMetric(r.Context(), chstore.MetricQueryFilter{
					Name:        req.Name,
					Service:     req.Service,
					Aggregation: req.Agg,
					GroupBy:     req.GroupBy,
					From:        from,
					To:          to,
					StepSeconds: req.Step,
				})
			case "spanMetric":
				filters, ferr := parseFiltersAndDSL(string(req.Filters), req.DSL)
				if ferr != nil {
					err = ferr
					break
				}
				series, err = s.store.QuerySpanMetric(r.Context(), chstore.SpanMetricFilter{
					Filters:     filters,
					Aggregation: req.Agg,
					Field:       req.Field,
					GroupBy:     req.GroupBy,
					From:        from,
					To:          to,
					StepSeconds: req.Step,
				})
			default:
				err = fmt.Errorf("unknown panel type %q", req.Type)
			}
			mu.Lock()
			if err != nil {
				out[req.ID].Error = err.Error()
			} else {
				out[req.ID].Series = series
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	writeJSON(w, out)
}

// spanHeatmap — Honeycomb-style 2D latency density. Returns a
// fixed (time × log-duration) grid of span counts so the
// frontend can render the cells as a colour heatmap without
// post-processing. Same filter shape as /api/spans/metric so
// a chart on /explore can swap its visualisation between
// "line trend" and "heatmap" against the same predicate.
func (s *Server) spanHeatmap(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filters, err := parseFiltersAndDSL(q.Get("filters"), q.Get("dsl"))
	if err != nil {
		http.Error(w, "invalid query DSL: "+err.Error(), http.StatusBadRequest)
		return
	}
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	if from.IsZero() || to.IsZero() {
		to = time.Now()
		from = to.Add(-15 * time.Minute)
	}
	timeBuckets, _ := strconv.Atoi(q.Get("buckets"))
	// Cache 30s — same posture as /api/services and the
	// other heavy span aggregates. Two operators on the same
	// dashboard or the same operator scrolling between viz
	// modes hit the cached payload instead of re-firing the
	// CH GROUP BY at billion-span scale.
	key := fmt.Sprintf("span-heatmap:from=%d:to=%d:buckets=%d:filters=%s:dsl=%s",
		from.UnixNano(), to.UnixNano(), timeBuckets,
		q.Get("filters"), q.Get("dsl"))
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetLatencyHeatmap(r.Context(), filters, from, to, timeBuckets)
	})
}

// spanBubbleUp — Honeycomb-style "what's special about THESE
// spans" attribute investigator. Two predicate sets:
//   • baseline (?filters / ?dsl) — the wider population
//   • selection (?selFilters / ?selDsl) — narrower subset
// CH counts both sides for each (attr_key, attr_value) pair
// and the score = selection_pct - baseline_pct surfaces the
// values over-represented in the selection.
//
// 60s server cache — investigation pattern is "click → look
// → tweak", not realtime; the cache collapses sequential
// pivots in a single session into one round-trip.
func (s *Server) spanBubbleUp(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	baseline, err := parseFiltersAndDSL(q.Get("filters"), q.Get("dsl"))
	if err != nil {
		http.Error(w, "invalid baseline DSL: "+err.Error(), http.StatusBadRequest)
		return
	}
	selection, err := parseFiltersAndDSL(q.Get("selFilters"), q.Get("selDsl"))
	if err != nil {
		http.Error(w, "invalid selection DSL: "+err.Error(), http.StatusBadRequest)
		return
	}
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	if from.IsZero() || to.IsZero() {
		to = time.Now()
		from = to.Add(-15 * time.Minute)
	}
	key := fmt.Sprintf("span-bubbleup:from=%d:to=%d:f=%s:d=%s:sf=%s:sd=%s",
		from.UnixNano(), to.UnixNano(),
		q.Get("filters"), q.Get("dsl"),
		q.Get("selFilters"), q.Get("selDsl"))
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		return s.store.BubbleUp(r.Context(), baseline, selection, from, to)
	})
}

// spanExemplar — drill from a metric chart point to a sample
// trace. Query: ?service=…&op=…&from=…&to=…&kind=slow|error|any
// Returns 200 with the exemplar payload, or 404 when no span
// matched the bucket (a bucket can have a non-zero count but
// the user clicked outside the actual data window — clean
// not-found is friendlier than a confusing empty 200 body).
func (s *Server) spanExemplar(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := chstore.ExemplarKind(q.Get("kind"))
	if kind == "" {
		kind = chstore.ExemplarSlow
	}
	ex, err := s.store.FindExemplar(r.Context(), chstore.ExemplarReq{
		Service:   q.Get("service"),
		Operation: q.Get("op"),
		From:      parseTime(q.Get("from")),
		To:        parseTime(q.Get("to")),
		Kind:      kind,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	if ex == nil {
		http.Error(w, "no exemplar found", http.StatusNotFound)
		return
	}
	writeJSON(w, ex)
}

func splitNonEmpty(s string, sep rune) []string {
	if s == "" {
		return nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == sep })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ── Profiling ─────────────────────────────────────────────────────────────────

// pyroscopeIngest accepts the Pyroscope agent's wire format —
// query-string driven, pprof body — and writes via the same
// InsertProfile path as /v1/profiles. v0.5.347.
//
// Wire format (compatible with Grafana Alloy / pyroscope OSS
// agent / pyroscope-rs / Python's pyroscope.io SDK):
//
//   POST /ingest?name=<app>.<profileType>{tag=val,...}
//                &from=<unix-sec>&until=<unix-sec>
//                &spyName=<spy>&sampleRate=<hz>&format=pprof
//   Content-Type: binary/octet-stream
//   Body: pprof bytes (gzipped or plain)
//
// `name` carries both service AND profile type — the trailing
// `.cpu` / `.alloc_objects` / `.lock` etc. fragment maps onto
// our string profile_type column. Unknown profile types fall
// through as-is so the operator's tooling sees what they sent.
//
// The optional `pyroscope.app.host` / `app.host` tag is read
// off the {tags} suffix and used as host_name. Everything else
// drops onto the floor (Pyroscope tags are a labels concept we
// don't preserve on the Profile row; the pprof body already
// carries label_set inline if the SDK provided it).
func (s *Server) pyroscopeIngest(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	name := q.Get("name")
	// Pyroscope's "app{tags}" string. Split on the first "{"
	// to peel off the tag block (we discard the tag values for
	// now — they're a labels concept the Profile schema
	// doesn't preserve).
	appPart := name
	tagPart := ""
	if i := strings.IndexByte(name, '{'); i >= 0 {
		appPart = name[:i]
		// Trim trailing "}" if present.
		tagPart = strings.TrimSuffix(name[i+1:], "}")
	}
	// Trailing ".cpu" / ".lock" / "alloc_objects" etc. is the
	// profile type. Default to "cpu" when omitted.
	service := appPart
	ptype := "cpu"
	if dot := strings.LastIndexByte(appPart, '.'); dot > 0 {
		service = appPart[:dot]
		ptype = appPart[dot+1:]
	}
	if service == "" {
		service = "unknown"
	}
	// Optional host from tags — Pyroscope agents emit
	// `pyroscope.app.host` or just `host` per the SDK
	// convention. First match wins.
	host := r.Header.Get("X-Coremetry-Host")
	if host == "" && tagPart != "" {
		for _, kv := range strings.Split(tagPart, ",") {
			eq := strings.IndexByte(kv, '=')
			if eq <= 0 {
				continue
			}
			k := strings.TrimSpace(kv[:eq])
			v := strings.Trim(strings.TrimSpace(kv[eq+1:]), `"`)
			if k == "host" || k == "pyroscope.app.host" || k == "instance" {
				host = v
				break
			}
		}
	}
	// Window: Pyroscope expresses [from, until) in unix
	// seconds. We carry start_time as nanoseconds.
	fromSec, _ := strconv.ParseInt(q.Get("from"), 10, 64)
	untilSec, _ := strconv.ParseInt(q.Get("until"), 10, 64)
	startNs := fromSec * 1e9
	if startNs == 0 {
		startNs = time.Now().UnixNano()
	}
	durNs := int64(0)
	if untilSec > fromSec && fromSec > 0 {
		durNs = (untilSec - fromSec) * 1e9
	}

	cnt, _ := profileconv.SampleCount(body)
	id := newID(16)
	p := &chstore.Profile{
		ProfileID:   id,
		ServiceName: service,
		HostName:    host,
		ProfileType: ptype,
		StartTime:   time.Unix(0, startNs).UTC(),
		DurationNs:  durNs,
		PprofData:   body,
		SampleCount: uint32(cnt),
	}
	if err := s.store.InsertProfile(r.Context(), p); err != nil {
		writeErr(w, err)
		return
	}
	// Pyroscope's agent expects an empty 200 — no JSON body.
	w.WriteHeader(http.StatusOK)
}

func (s *Server) ingestProfile(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	service := r.Header.Get("X-Coremetry-Service")
	if service == "" {
		service = "unknown"
	}
	host := r.Header.Get("X-Coremetry-Host")
	ptype := r.Header.Get("X-Coremetry-Profile-Type")
	if ptype == "" {
		ptype = "cpu"
	}
	durNs, _ := strconv.ParseInt(r.Header.Get("X-Coremetry-Duration-Ns"), 10, 64)
	startNs, _ := strconv.ParseInt(r.Header.Get("X-Coremetry-Start-Time-Ns"), 10, 64)
	if startNs == 0 {
		startNs = time.Now().UnixNano()
	}

	cnt, _ := profileconv.SampleCount(body)

	id := newID(16)
	p := &chstore.Profile{
		ProfileID:   id,
		ServiceName: service,
		HostName:    host,
		ProfileType: ptype,
		StartTime:   time.Unix(0, startNs).UTC(),
		DurationNs:  durNs,
		PprofData:   body,
		SampleCount: uint32(cnt),
	}
	if err := s.store.InsertProfile(r.Context(), p); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"profileId": id})
}

func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := chstore.ProfileFilter{
		Service:     q.Get("service"),
		ProfileType: q.Get("type"),
		From:        parseTime(q.Get("from")),
		To:          parseTime(q.Get("to")),
		Limit:       parseInt(q.Get("limit"), 100),
	}
	rows, err := s.store.ListProfiles(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	data, meta, err := s.store.GetProfileBytes(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	flame, err := profileconv.BuildFlameAuto(data)
	if err != nil {
		writeErr(w, err)
		return
	}
	breakdown := profileconv.FlameCategoryBreakdown(flame)
	writeJSON(w, map[string]interface{}{
		"meta":      meta,
		"flame":     flame,
		"breakdown": breakdown,
	})
}

// profileHotspots aggregates every profile matching the
// (service, type, window) filter into a single virtual flame
// tree, then rolls it up by function name. Returns the top
// rows so the UI never has to download N pprof blobs across a
// 1-hour window (which can be tens of MB raw). Cached 60s
// because in-window aggregates only shift when a new profile
// lands.
func (s *Server) profileHotspots(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := q.Get("service")
	ptype := q.Get("type")
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	limit := parseInt(q.Get("limit"), 200) // profile-count cap
	top := parseInt(q.Get("top"), 100)     // returned-hotspot cap
	// v0.5.340 — hard upper bound. Even a generous "limit=500"
	// in the query string caps at 500; a runaway operator
	// can't fan out 10000 pprof parses on a single request.
	if limit > 500 {
		limit = 500
	}
	if service == "" {
		http.Error(w, "service param required", http.StatusBadRequest)
		return
	}
	// Bucket time inputs to the minute so concurrent requests
	// within the same minute share one CH round-trip.
	fromKey, toKey := from.Truncate(time.Minute), to.Truncate(time.Minute)
	key := fmt.Sprintf("profile-hotspots:%s:%s:%d:%d:%d:%d",
		service, ptype, fromKey.UnixNano(), toKey.UnixNano(), limit, top)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		merged := &chstore.FlameNode{Name: "root"}
		parsed, failed := 0, 0
		var earliest, latest time.Time
		// v0.5.340 — streaming aggregation. Each pprof is parsed +
		// merged + discarded inside the callback so RAM peak ≈
		// one payload instead of N × payload size. At 200 × 1MB
		// the old slice approach burned ~200MB per request;
		// streaming holds it to ~1MB peak.
		err := s.store.IterateProfilePayloads(r.Context(), chstore.ProfileFilter{
			Service:     service,
			ProfileType: ptype,
			From:        from,
			To:          to,
			Limit:       limit,
		}, func(p chstore.ProfilePayload) error {
			flame, perr := profileconv.BuildFlameAuto(p.Bytes)
			if perr != nil || flame == nil {
				failed++
				return nil
			}
			profileconv.MergeFlame(merged, flame)
			parsed++
			if earliest.IsZero() || p.StartTime.Before(earliest) {
				earliest = p.StartTime
			}
			if p.StartTime.After(latest) {
				latest = p.StartTime
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		hotspots := profileconv.FlameToHotspots(merged)
		// Sort by self desc, cap to top.
		sort.Slice(hotspots, func(i, j int) bool {
			return hotspots[i].Self > hotspots[j].Self
		})
		if len(hotspots) > top {
			hotspots = hotspots[:top]
		}
		breakdown := profileconv.FlameCategoryBreakdown(merged)
		return map[string]any{
			"service":      service,
			"profileType":  ptype,
			"profilesUsed": parsed,
			"profilesFailed": failed,
			"totalSamples": merged.Value,
			"earliest":     earliest.UnixNano(),
			"latest":       latest.UnixNano(),
			"hotspots":     hotspots,
			"breakdown":    breakdown,
		}, nil
	})
}

// profileHotspotsForSpan returns the aggregated method
// hotspots + leaf-time breakdown across every profile that
// overlapped a span's window. Lets the trace-detail panel show
// "where did this span's time actually go" without forcing the
// operator to open one of the linked profiles. Cached 30s
// keyed on (service, start, end) — span windows are immutable.
func (s *Server) profileHotspotsForSpan(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	startNs, _ := strconv.ParseInt(q.Get("start"), 10, 64)
	endNs, _ := strconv.ParseInt(q.Get("end"), 10, 64)
	service := q.Get("service")
	top := parseInt(q.Get("top"), 10)
	if service == "" || startNs == 0 || endNs == 0 {
		http.Error(w, "service, start, end query params required", http.StatusBadRequest)
		return
	}
	key := fmt.Sprintf("profile-hotspots-byspan:%s:%d:%d:%d", service, startNs, endNs, top)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		merged := &chstore.FlameNode{Name: "root"}
		parsed, failed := 0, 0
		// v0.5.340 — single CH round-trip + streaming parse.
		// Replaces the prior N+1 pattern (FindProfilesForSpan +
		// GetProfileBytes per row) and the per-request RAM
		// double-buffer.
		err := s.store.IterateProfilesForSpan(r.Context(), service,
			time.Unix(0, startNs), time.Unix(0, endNs),
			func(p chstore.ProfilePayload) error {
				flame, perr := profileconv.BuildFlameAuto(p.Bytes)
				if perr != nil || flame == nil {
					failed++
					return nil
				}
				profileconv.MergeFlame(merged, flame)
				parsed++
				return nil
			})
		if err != nil {
			return nil, err
		}
		hotspots := profileconv.FlameToHotspots(merged)
		sort.Slice(hotspots, func(i, j int) bool {
			return hotspots[i].Self > hotspots[j].Self
		})
		if len(hotspots) > top {
			hotspots = hotspots[:top]
		}
		breakdown := profileconv.FlameCategoryBreakdown(merged)
		return map[string]any{
			"profilesUsed":   parsed,
			"profilesFailed": failed,
			"totalSamples":   merged.Value,
			"hotspots":       hotspots,
			"breakdown":      breakdown,
		}, nil
	})
}

func (s *Server) profilesForSpan(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	startNs, _ := strconv.ParseInt(q.Get("start"), 10, 64)
	endNs, _ := strconv.ParseInt(q.Get("end"), 10, 64)
	if startNs == 0 || endNs == 0 {
		http.Error(w, "missing start/end query params", http.StatusBadRequest)
		return
	}
	rows, err := s.store.FindProfilesForSpan(r.Context(),
		q.Get("service"),
		time.Unix(0, startNs), time.Unix(0, endNs))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func newID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ── Auth ─────────────────────────────────────────────────────────────────────

// sanitizePassword strips paste-mangle artifacts that would
// otherwise silently break bcrypt/LDAP compare. v0.5.291 —
// Operator-reported regression: LDAP users hitting "invalid
// credentials" with the right password again. The v0.5.87 fix
// covered soft-hyphen + ASCII whitespace but not the broader
// invisible-character set that survives paste from Word / PDF /
// rendered HTML.
//
// What we strip:
//   • Edge whitespace via unicode.IsSpace — covers NBSP (U+00A0),
//     narrow NBSP (U+202F), and the 17 other Unicode space
//     characters Go's TrimSpace already handles.
//   • Anywhere in string:
//       U+00AD  soft-hyphen          (invisible, conditional break)
//       U+200B  zero-width space     (Word / PDF copy)
//       U+200C  zero-width non-joiner
//       U+200D  zero-width joiner
//       U+2060  word joiner          (invisible)
//       U+FEFF  zero-width no-break / BOM (common in Word copy)
//
// We deliberately do NOT rewrite visible homoglyphs (en/em-dash
// for hyphen, smart quotes, etc.) — those could be intentional
// characters in a password and the login screen's i18n hint
// already tells users to retype if their paste looks suspicious.
func sanitizePassword(p string) string {
	orig := p
	p = strings.TrimFunc(p, unicode.IsSpace)
	p = strings.Map(func(r rune) rune {
		switch r {
		case 0x00AD, // soft-hyphen
			0x200B, 0x200C, 0x200D, // zero-width space / non-joiner / joiner
			0x2060,                  // word joiner
			0xFEFF:                  // zero-width no-break (BOM)
			return -1
		}
		return r
	}, p)
	if p != orig {
		log.Printf("[auth] password sanitized — submitted len=%d, after strip=%d (likely paste-mangle: invisible/zero-width chars or non-ASCII whitespace)",
			len(orig), len(p))
	}
	return p
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	body.Password = sanitizePassword(body.Password)
	if body.Email == "" || body.Password == "" {
		http.Error(w, "email and password required", http.StatusBadRequest)
		return
	}

	// Two-pass auth:
	//   (1) Local bcrypt — bootstrap admin + any user with a saved
	//       password hash. Always tried first so the bootstrap admin
	//       keeps a path in even when LDAP breaks.
	//   (2) LDAP — when enabled and local didn't match. Lets domain
	//       users sign in with their AD creds; first successful LDAP
	//       login auto-provisions a row in our users table (role
	//       resolved via group→role mapping).
	user, err := s.store.GetUserByEmail(r.Context(), body.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	authed := user != nil && user.PasswordHash != "" && auth.CheckPassword(user.PasswordHash, body.Password)

	if !authed && s.ldap.Enabled() {
		log.Printf("[auth] local check failed for %q — falling back to LDAP", body.Email)
		ldapUser, lerr := s.loginViaLDAP(r.Context(), body.Email, body.Password, user)
		if lerr == nil {
			user = ldapUser
			authed = true
			log.Printf("[auth] LDAP login OK for %q (role=%s, dn-trail in [ldap] lines above)", user.Email, user.Role)
		} else {
			log.Printf("[auth] LDAP login FAILED for %q: %v", body.Email, lerr)
		}
	} else if !authed {
		log.Printf("[auth] local check failed for %q (LDAP disabled)", body.Email)
	}

	if !authed || user == nil {
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}
	tok, exp, err := s.auth.Issue(user.ID, user.Email, user.Role)
	if err != nil {
		writeErr(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
		MaxAge:   int(s.auth.TTL().Seconds()),
	})
	writeJSON(w, map[string]interface{}{
		"token":     tok,
		"expiresAt": exp.UnixNano(),
		"user":      s.userPayload(user),
	})
}

// userPayload builds the small JSON shape returned by /api/auth/login,
// /api/auth/me, and the user-mgmt endpoints. Resolves the custom-role
// pointer to its concrete `pages` list at serialise time so the SPA
// doesn't need a second fetch on every navigation. Returns a flat
// map (not a struct) because the field set varies — customRole +
// customRolePages only appear when the user has a valid pointer.
func (s *Server) userPayload(u *chstore.User) map[string]any {
	out := map[string]any{
		"id":    u.ID,
		"email": u.Email,
		"role":  u.Role,
	}
	// Only viewers get a custom-role restriction. Defensive guard
	// mirrors UpsertUser — a stale pointer on an admin/editor row
	// should be ignored, never enforced.
	if u.Role == auth.RoleViewer && u.CustomRole != "" {
		pages := s.auth.CustomRolePages(u.CustomRole)
		if pages != nil {
			out["customRole"] = u.CustomRole
			out["customRolePages"] = pages
		}
	}
	return out
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: auth.CookieName, Value: "", Path: "/",
		HttpOnly: true, MaxAge: -1,
	})
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	c := auth.FromContext(r.Context())
	if c == nil {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return
	}
	// Hydrate from the store so customRole + customRolePages reach
	// the SPA on every page load. The JWT only carries the base
	// role; custom-role assignments live in the row.
	u, err := s.store.GetUserByID(r.Context(), c.UserID)
	if err != nil || u == nil {
		// Fall back to claim data — keeps /api/auth/me honest when
		// the row is temporarily unreachable. Custom-role gating
		// will simply not apply this round.
		writeJSON(w, map[string]any{
			"id": c.UserID, "email": c.Email, "role": c.Role,
		})
		return
	}
	writeJSON(w, s.userPayload(u))
}

// loginViaLDAP runs the directory bind for the entered credentials,
// resolves a Coremetry role from the user's groups (via the saved
// group→role mapping), and ensures a row exists in the users table —
// either updating the existing one or auto-provisioning a fresh one.
//
// Returns the persisted *chstore.User so the caller can issue a JWT
// directly. Pre-existing rows keep their stored role unless a group
// mapping bumps them up to a higher privilege (mapping is the
// "source of truth" for org-managed users).
func (s *Server) loginViaLDAP(ctx context.Context, email, password string, existing *chstore.User) (*chstore.User, error) {
	// Try the user-entered string both as a username and (if it looks
	// like an email) as the local part. The configured user filter
	// usually matches sAMAccountName, so handing it `cenk@corp.example.com`
	// won't bind — but `cenk` will. Splitting on '@' covers both.
	usernames := []string{email}
	if at := strings.IndexByte(email, '@'); at > 0 {
		usernames = append(usernames, email[:at])
	}
	var (
		res *ldap.AuthResult
		err error
	)
	for _, u := range usernames {
		res, err = s.ldap.Authenticate(ctx, u, password)
		if err == nil && res != nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, errors.New("ldap auth: no result")
	}

	// Email — prefer the directory's mail attribute, fall back to the
	// entered identifier so we always have a non-empty key.
	finalEmail := strings.ToLower(strings.TrimSpace(res.User.Email))
	if finalEmail == "" {
		finalEmail = email
	}

	// Provisioning logic — three branches:
	//   (a) NEW user (no existing row): auto-provision with the role
	//       resolved from group mapping; falls back to RoleViewer when
	//       no mapping matches and the configured defaultRole is empty.
	//       This is the "first-time domain user lands in the app" path.
	//   (b) Existing LDAP-provider row: re-apply the role from group
	//       mapping every login so AD promotions/demotions take effect.
	//   (c) Existing local-provider row converting to LDAP: keep the
	//       admin's manually-set role — that's a deliberate override.
	if existing == nil {
		existing, _ = s.store.GetUserByEmail(ctx, finalEmail)
	}
	role := res.Role
	if existing == nil {
		// First-time login. role already from group mapping; if the
		// directory user has no group match AND the admin hasn't set
		// a defaultRole, mapRole() returns "" and we fall through to
		// the IsValidRole guard below.
	} else if existing.AuthProvider == "ldap" && existing.Role != "" {
		// Existing LDAP user — refresh role from current AD groups.
		// (Empty branch body — `role` already holds res.Role.)
	} else if existing.AuthProvider != "ldap" && existing.Role != "" {
		// Local user, manually pinned by admin — preserve their role.
		role = existing.Role
	}
	if !auth.IsValidRole(role) {
		role = auth.RoleViewer
	}

	u := chstore.User{
		Email:        finalEmail,
		Role:         role,
		AuthProvider: "ldap",
	}
	firstLogin := existing == nil
	if existing != nil {
		u.ID = existing.ID
		u.CreatedAt = existing.CreatedAt
	} else {
		idBytes := make([]byte, 8)
		_, _ = rand.Read(idBytes)
		u.ID = hex.EncodeToString(idBytes)
	}
	if err := s.store.UpsertUser(ctx, u); err != nil {
		return nil, fmt.Errorf("provision ldap user: %w", err)
	}
	if firstLogin {
		log.Printf("[auth] first-time LDAP login auto-provisioned user %q as %s (id=%s)", finalEmail, role, u.ID)
	} else if existing != nil && existing.Role != role {
		log.Printf("[auth] LDAP login refreshed role for %q: %s → %s", finalEmail, existing.Role, role)
	}
	// Re-read so callers get the canonical row (CreatedAt set by CH on
	// first insert via DEFAULT now()).
	saved, err := s.store.GetUserByEmail(ctx, finalEmail)
	if err != nil || saved == nil {
		return &u, nil
	}
	return saved, nil
}

// ── LDAP settings (admin) ────────────────────────────────────────────────────

// getLDAPSettings returns the saved LDAP config minus the bind
// password (Sanitize() replaces it with the "__SET__" sentinel so the
// UI can show "saved — leave empty to keep current").
func (s *Server) getLDAPSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.ldap.Snapshot())
}

// putLDAPSettings saves a new LDAP config and updates the live
// service. An empty BindPassword preserves the saved one (matches the
// UI's "leave empty" affordance).
func (s *Server) putLDAPSettings(w http.ResponseWriter, r *http.Request) {
	var c ldap.Config
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest); return
	}
	// Drop the "password is set" sentinel — the field's empty value
	// from the UI means "keep current", which Service.SavePersisted
	// already handles.
	if c.BindPassword == "__SET__" {
		c.BindPassword = ""
	}
	for _, m := range c.GroupRoleMap {
		if m.Role != "" && !auth.IsValidRole(m.Role) {
			http.Error(w, "invalid role in mapping: "+m.Role, http.StatusBadRequest); return
		}
	}
	if c.DefaultRole != "" && !auth.IsValidRole(c.DefaultRole) {
		http.Error(w, "invalid defaultRole", http.StatusBadRequest); return
	}
	if err := s.ldap.SavePersisted(r.Context(), s.store, c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError); return
	}
	s.publishConfigReload(r.Context(), "ldap")
	// Audit row carries non-secret config shape only — bind password
	// and the directory host's credentials never go into audit_log.
	details, _ := json.Marshal(map[string]any{
		"enabled": c.Enabled, "host": c.Host, "port": c.Port,
		"defaultRole": c.DefaultRole, "groupRoleMapCount": len(c.GroupRoleMap),
	})
	s.audit(r, "settings.ldap.update", "settings", "ldap", string(details))
	writeJSON(w, s.ldap.Snapshot())
}

// testLDAPConnection runs the dial+bind probe against either the
// saved config (empty body) or a draft (PUT-shape body) — that lets
// the UI verify creds before saving.
func (s *Server) testLDAPConnection(w http.ResponseWriter, r *http.Request) {
	var draft *ldap.Config
	if r.ContentLength > 0 {
		var c ldap.Config
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest); return
		}
		if c.BindPassword == "__SET__" {
			c.BindPassword = ""
		}
		draft = &c
	}
	if err := s.ldap.TestConnection(r.Context(), draft); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// searchLDAPUsers proxies the admin's "find a user to provision"
// query into the directory. Returned entries are not yet provisioned;
// the caller posts to /api/users/from-ldap to actually create a row.
func (s *Server) searchLDAPUsers(w http.ResponseWriter, r *http.Request) {
	if !s.ldap.Enabled() {
		http.Error(w, "ldap not enabled", http.StatusBadRequest); return
	}
	q := r.URL.Query().Get("q")
	limit := 25
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscan(v, &limit)
	}
	users, err := s.ldap.Search(r.Context(), q, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError); return
	}
	writeJSON(w, map[string]any{"users": users})
}

// provisionLDAPUser inserts a row in the users table for an LDAP
// directory entry, with a role chosen by the admin. This is the "pre-
// provision before first login" path — useful when group→role
// mapping isn't enough or you want to grant admin to a single user
// that doesn't belong to the AD admin group.
func (s *Server) provisionLDAPUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest); return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if body.Email == "" {
		http.Error(w, "email required", http.StatusBadRequest); return
	}
	if !auth.IsValidRole(body.Role) {
		http.Error(w, "role must be admin, editor or viewer", http.StatusBadRequest); return
	}
	existing, _ := s.store.GetUserByEmail(r.Context(), body.Email)
	u := chstore.User{
		Email:        body.Email,
		Role:         body.Role,
		AuthProvider: "ldap",
	}
	if existing != nil {
		u.ID = existing.ID
		u.CreatedAt = existing.CreatedAt
		// Preserve any password hash on a converting user — they may
		// have been local before. After a successful LDAP login the
		// hash becomes irrelevant but keeping it costs nothing.
		u.PasswordHash = existing.PasswordHash
	} else {
		idBytes := make([]byte, 8)
		_, _ = rand.Read(idBytes)
		u.ID = hex.EncodeToString(idBytes)
	}
	if err := s.store.UpsertUser(r.Context(), u); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError); return
	}
	saved, _ := s.store.GetUserByEmail(r.Context(), body.Email)
	if saved == nil {
		saved = &u
	}
	writeJSON(w, saved)
}

// authConfig describes which auth methods the UI should offer. Public on
// purpose — the login page must be able to call it before the user signs in.
//
// When demo mode is on the response also includes the bootstrap admin
// credentials so the login form can pre-fill them. That is intentionally
// public — anyone with the demo URL is meant to log in as the demo user.
func (s *Server) authConfig(w http.ResponseWriter, r *http.Request) {
	// Don't cache — admin LDAP/AI/demo edits should reflect on the
	// login page within a single round-trip, not after a 60s wait.
	resp := map[string]interface{}{
		"local": map[string]bool{"enabled": true},
		"oidc":  map[string]interface{}{"enabled": false},
		"demo":  map[string]interface{}{"enabled": false},
		"ldap":  map[string]interface{}{"enabled": false},
	}
	if s.oidc.Enabled() {
		resp["oidc"] = map[string]interface{}{
			"enabled":     true,
			"displayName": s.oidc.DisplayName(),
		}
	}
	if s.demoMode {
		resp["demo"] = map[string]interface{}{
			"enabled":  true,
			"email":    s.demoEmail,
			"password": s.demoPassword,
		}
	}
	if s.ldap.Enabled() {
		resp["ldap"] = map[string]interface{}{"enabled": true}
	}
	writeJSON(w, resp)
}

// ── OIDC sign-in ─────────────────────────────────────────────────────────────

const (
	oidcStateCookie    = "coremetry_oidc_state"
	oidcNonceCookie    = "coremetry_oidc_nonce"
	oidcVerifierCookie = "coremetry_oidc_verifier"
)

func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	if !s.oidc.Enabled() {
		http.Error(w, "oidc not configured", http.StatusNotFound)
		return
	}
	state := auth.RandomURLToken(16)
	nonce := auth.RandomURLToken(16)
	verifier := auth.RandomURLToken(32)
	challenge := auth.PKCEChallenge(verifier)

	// Short-lived HttpOnly cookies — the IdP round-trip should take seconds.
	for _, c := range []*http.Cookie{
		setOIDCCookie(oidcStateCookie, state),
		setOIDCCookie(oidcNonceCookie, nonce),
		setOIDCCookie(oidcVerifierCookie, verifier),
	} {
		http.SetCookie(w, c)
	}
	http.Redirect(w, r, s.oidc.AuthURL(state, nonce, challenge), http.StatusFound)
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if !s.oidc.Enabled() {
		http.Error(w, "oidc not configured", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	if errParam := q.Get("error"); errParam != "" {
		s.oidcFail(w, r, errParam+": "+q.Get("error_description"))
		return
	}
	code := q.Get("code")
	stateGot := q.Get("state")
	if code == "" || stateGot == "" {
		s.oidcFail(w, r, "missing code or state")
		return
	}

	// Pull and immediately clear the in-flight cookies.
	stateWant, _ := r.Cookie(oidcStateCookie)
	nonce, _ := r.Cookie(oidcNonceCookie)
	verifier, _ := r.Cookie(oidcVerifierCookie)
	for _, name := range []string{oidcStateCookie, oidcNonceCookie, oidcVerifierCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	}
	if stateWant == nil || stateWant.Value != stateGot {
		s.oidcFail(w, r, "state mismatch — possible CSRF or stale tab")
		return
	}
	if nonce == nil || verifier == nil {
		s.oidcFail(w, r, "session cookies expired — try again")
		return
	}

	claims, err := s.oidc.Exchange(r.Context(), code, verifier.Value, nonce.Value)
	if err != nil {
		log.Printf("[oidc] callback: %v", err)
		s.oidcFail(w, r, err.Error())
		return
	}

	// Lookup or auto-provision. Email is the identity key; existing local
	// users with the same email are kept (they can use either method).
	email := strings.ToLower(claims.Email)
	user, err := s.store.GetUserByEmail(r.Context(), email)
	if err != nil {
		s.oidcFail(w, r, err.Error())
		return
	}
	if user == nil {
		role := s.oidc.DefaultRole()
		if role != auth.RoleAdmin {
			role = auth.RoleViewer
		}
		user = &chstore.User{
			ID:           newID(8),
			Email:        email,
			PasswordHash: "", // OIDC-only — local login impossible until an admin sets a password
			Role:         role,
			AuthProvider: "oidc",
		}
		if err := s.store.UpsertUser(r.Context(), *user); err != nil {
			s.oidcFail(w, r, err.Error())
			return
		}
		log.Printf("[oidc] auto-provisioned user %q (role=%s)", email, role)
	}

	tok, exp, err := s.auth.Issue(user.ID, user.Email, user.Role)
	if err != nil {
		s.oidcFail(w, r, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
		MaxAge:   int(s.auth.TTL().Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

// oidcFail renders the failure on the login page so the user gets a hint
// instead of a blank API response.
func (s *Server) oidcFail(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/login/?error="+url.QueryEscape(msg), http.StatusFound)
}

func setOIDCCookie(name, value string) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/api/auth/oidc/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600, // 10 min — IdP round-trip cap
	}
}

// changeOwnPassword lets the logged-in user rotate their own password.
// Requires the current password as proof of presence.
func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request) {
	c := auth.FromContext(r.Context())
	if c == nil {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return
	}
	var body struct {
		Current string `json:"currentPassword"`
		New     string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if len(body.New) < 6 {
		http.Error(w, `{"error":"password must be at least 6 characters"}`, http.StatusBadRequest)
		return
	}
	user, err := s.store.GetUserByID(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if user == nil || !auth.CheckPassword(user.PasswordHash, body.Current) {
		http.Error(w, `{"error":"current password is incorrect"}`, http.StatusUnauthorized)
		return
	}
	hash, err := auth.HashPassword(body.New)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.store.UpdatePassword(r.Context(), c.UserID, hash); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// ── SMTP + notification channels (admin only) ───────────────────────────────

// maskedSMTP returns the persisted settings with the password redacted —
// the GET endpoint must never echo the real password back to the browser
// (it'd be visible to anyone with browser dev tools open).
func maskedSMTP(s notify.SMTPSettings) map[string]any {
	masked := ""
	if s.Password != "" {
		masked = "********"
	}
	return map[string]any{
		"host": s.Host, "port": s.Port, "username": s.Username,
		"password": masked, "from": s.From, "fromName": s.FromName,
		"startTLS": s.StartTLS, "skipVerify": s.SkipVerify,
		"configured": s.Configured(),
	}
}

// getSamplingSettings returns the live in-memory sampler config —
// what the ingester is actually applying right now (post-Reload),
// not just whatever was persisted last. Includes both stages
// (head ratios + tail buffer counters) so the admin can see the
// policy is taking effect without watching ingest rates.
func (s *Server) getSamplingSettings(w http.ResponseWriter, r *http.Request) {
	if s.sampler == nil {
		writeJSON(w, map[string]any{"default": 1.0, "services": map[string]float64{}})
		return
	}
	snap := s.sampler.Snapshot()
	body := map[string]any{
		"default":          snap.Default,
		"services":         snap.Services,
		"alwaysKeepErrors": snap.AlwaysKeepErrors != nil && *snap.AlwaysKeepErrors,
		"alwaysKeepRoots":  snap.AlwaysKeepRoots != nil && *snap.AlwaysKeepRoots,
		"droppedSinceBoot": s.ing.SpansSampledOut(),
	}
	body["tail"] = snap.Tail
	if t := s.sampler.Tail(); t != nil {
		body["tailStats"] = t.Stats()
	}
	writeJSON(w, body)
}

// putSamplingSettings persists the new policy and hot-reloads the
// in-memory sampler so the next span is judged against it. No
// process restart needed; great for trying low ratios while
// watching ingest cost in real time.
func (s *Server) putSamplingSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Default          float64            `json:"default"`
		Services         map[string]float64 `json:"services"`
		AlwaysKeepErrors *bool              `json:"alwaysKeepErrors"`
		AlwaysKeepRoots  *bool              `json:"alwaysKeepRoots"`
		Tail             *struct {
			Enabled   bool `json:"enabled"`
			WindowSec int  `json:"windowSec"`
			SlowMs    int  `json:"slowMs"`
			MaxTraces int  `json:"maxTraces"`
		} `json:"tail"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	cfg := samplingConfigFromBody(body.Default, body.Services, body.AlwaysKeepErrors, body.AlwaysKeepRoots)
	if body.Tail != nil {
		cfg.Tail = config.TailSamplingConfig{
			Enabled:   body.Tail.Enabled,
			WindowSec: body.Tail.WindowSec,
			SlowMs:    body.Tail.SlowMs,
			MaxTraces: body.Tail.MaxTraces,
		}
	}
	if s.sampler != nil {
		defer s.publishConfigReload(r.Context(), "sampling")
		if err := s.sampler.SavePersisted(r.Context(), s.store, cfg); err != nil {
			writeErr(w, err)
			return
		}
	}
	details, _ := json.Marshal(map[string]any{
		"default": cfg.Default, "serviceCount": len(cfg.Services),
		"tailEnabled": cfg.Tail.Enabled,
	})
	s.audit(r, "settings.sampling.update", "settings", "sampling", string(details))
	s.getSamplingSettings(w, r)
}

// samplingConfigFromBody is split out so the put handler can
// build a SamplingConfig without circularly importing main.go's
// view of the world. Pass-through with a nil-safe wrapping for
// the bool overrides (so the admin UI can leave them undefined
// to inherit defaults).
func samplingConfigFromBody(def float64, svc map[string]float64, keepErr, keepRoot *bool) config.SamplingConfig {
	return config.SamplingConfig{
		Default:          def,
		Services:         svc,
		AlwaysKeepErrors: keepErr,
		AlwaysKeepRoots:  keepRoot,
	}
}

// keep the sampling import referenced explicitly — the field
// declaration above pulls it in as a Server type, but Go's
// unused-import check is package-scoped and the field type alone
// counts as use, so this comment is the human marker rather than
// a compiler need. (The build was failing earlier because
// sampling.Config doesn't exist; the actual config type is
// config.SamplingConfig.)
var _ = sampling.New

func (s *Server) getSMTPSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.notify.SMTP(r.Context())
	writeJSON(w, maskedSMTP(cfg))
}

func (s *Server) putSMTPSettings(w http.ResponseWriter, r *http.Request) {
	var body notify.SMTPSettings
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	// Empty / sentinel password = "keep the existing one" so admins can
	// edit the host/port without re-entering credentials each time.
	if body.Password == "" || body.Password == "********" {
		body.Password = s.notify.SMTP(r.Context()).Password
	}
	if err := s.notify.SaveSMTP(r.Context(), body); err != nil {
		writeErr(w, err)
		return
	}
	// Password never enters audit_log — only the connection shape.
	details, _ := json.Marshal(map[string]any{
		"host": body.Host, "port": body.Port, "username": body.Username,
		"from": body.From, "startTLS": body.StartTLS,
	})
	s.audit(r, "settings.smtp.update", "settings", "smtp", string(details))
	writeJSON(w, maskedSMTP(body))
}

func (s *Server) testSMTPSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Recipient string `json:"recipient"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Recipient == "" {
		http.Error(w, `{"error":"recipient required"}`, http.StatusBadRequest)
		return
	}
	cfgRaw, _ := json.Marshal(notify.EmailChannelConfig{Recipients: []string{body.Recipient}})
	tmp := chstore.NotificationChannel{
		ID: "ad-hoc-test", Name: "Ad-hoc test", Type: "email",
		Config: cfgRaw, Enabled: true, MinSeverity: "info",
	}
	if err := s.notify.SendTest(r.Context(), tmp); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"status": "sent"})
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListChannels(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	for i := range out {
		out[i].Config = redactSecrets(out[i].Type, out[i].Config)
	}
	writeJSON(w, out)
}

// redactSecrets strips fields the UI should never see — Zoom
// client_secret, Twilio auth_token, generic "password" / "token"
// keys — from the channel config blob before it's serialised
// back to the browser. Edit flow handles the "leave secret
// empty to keep saved value" UX in updateChannel.
//
// Operates on the raw json.RawMessage by round-tripping through
// a map; the config schema is unstructured by design (each
// channel type has its own shape) so this is the cleanest way
// to redact without per-type code paths.
func redactSecrets(channelType string, raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	// Type-specific redactions first.
	switch channelType {
	case "zoomchat":
		delete(m, "clientSecret")
		delete(m, "verificationToken") // legacy field
	case "whatsapp":
		delete(m, "authToken")
	}
	// Generic safety net — any field named "password", "secret",
	// "token", or "apiKey" gets stripped regardless of channel
	// type so a future addition can't accidentally leak.
	for k := range m {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "password") ||
			strings.Contains(lk, "secret") ||
			strings.Contains(lk, "token") ||
			strings.Contains(lk, "apikey") {
			delete(m, k)
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request) {
	var c chstore.NotificationChannel
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if c.Name == "" || c.Type == "" {
		http.Error(w, `{"error":"name and type required"}`, http.StatusBadRequest)
		return
	}
	c.ID = newID(8)
	if err := s.store.UpsertChannel(r.Context(), c); err != nil {
		writeErr(w, err)
		return
	}
	// Audit row carries name + type only; the channel's Config blob
	// holds the secret and never enters audit_log.
	details, _ := json.Marshal(map[string]any{"name": c.Name, "type": c.Type})
	s.audit(r, "notification_channel.create", "notification_channel", c.ID, string(details))
	writeJSON(w, c)
}

func (s *Server) updateChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.store.GetChannel(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if existing == nil {
		http.Error(w, `{"error":"channel not found"}`, http.StatusNotFound)
		return
	}
	var c chstore.NotificationChannel
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	c.ID = id
	c.CreatedAt = existing.CreatedAt
	// Preserve write-only secrets when the operator leaves them
	// blank on edit — UI never sees them after save, so an empty
	// field in the update body means "keep what's stored", not
	// "clear it". Without this the first edit after a save wipes
	// the secret silently. Per-type list mirrors redactSecrets().
	c.Config = mergeSecrets(c.Type, existing.Config, c.Config)
	if err := s.store.UpsertChannel(r.Context(), c); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"name": c.Name, "type": c.Type})
	s.audit(r, "notification_channel.update", "notification_channel", c.ID, string(details))
	c.Config = redactSecrets(c.Type, c.Config)
	writeJSON(w, c)
}

// mergeSecrets fills missing/empty secret fields in the incoming
// update body from the existing saved record. Keeps the
// "leave-blank-to-keep" UX without per-field plumbing on the
// frontend. Symmetric with redactSecrets — what gets redacted on
// read gets merged on write.
func mergeSecrets(channelType string, existing, incoming json.RawMessage) json.RawMessage {
	if len(existing) == 0 {
		return incoming
	}
	var inMap, exMap map[string]any
	if err := json.Unmarshal(incoming, &inMap); err != nil {
		return incoming
	}
	if err := json.Unmarshal(existing, &exMap); err != nil {
		return incoming
	}
	carry := func(field string) {
		if v, _ := inMap[field].(string); v != "" {
			return // operator typed a new value — use it
		}
		if v, ok := exMap[field]; ok {
			inMap[field] = v
		}
	}
	switch channelType {
	case "zoomchat":
		carry("clientSecret")
	case "whatsapp":
		carry("authToken")
	}
	// Generic catch-all for anything that smells secret.
	for k := range exMap {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "password") ||
			strings.Contains(lk, "secret") ||
			strings.Contains(lk, "token") ||
			strings.Contains(lk, "apikey") {
			carry(k)
		}
	}
	out, err := json.Marshal(inMap)
	if err != nil {
		return incoming
	}
	return out
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteChannel(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "notification_channel.delete", "notification_channel", id, "")
	writeJSON(w, map[string]string{"status": "ok"})
}

// listZoomChannels powers the Settings page's "List my channels"
// helper. Body shape mirrors what the form already collects:
//   - inline credentials (accountId / clientId / clientSecret /
//     optional oauthBaseURL / apiBaseURL) for an un-saved
//     channel the admin is still configuring, OR
//   - channelId of an already-saved Zoom Chat channel — we
//     read its stored secrets server-side so the admin doesn't
//     need to re-type the redacted client_secret.
//
// Walks Zoom's pagination up to 1000 channels and returns the
// full list (id / jid / name / type). Frontend renders a
// searchable picker on top. Read-only operation; no state
// changes regardless of which path executes.
func (s *Server) listZoomChannels(w http.ResponseWriter, r *http.Request) {
	var body struct {
		// "Edit existing channel" path.
		ChannelID string `json:"existingChannelId,omitempty"`
		// "Configuring new channel" path — inline credentials.
		AccountID          string `json:"accountId,omitempty"`
		ClientID           string `json:"clientId,omitempty"`
		ClientSecret       string `json:"clientSecret,omitempty"`
		OAuthBaseURL       string `json:"oauthBaseUrl,omitempty"`
		APIBaseURL         string `json:"apiBaseUrl,omitempty"`
		InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}

	accID, cliID, cliSec := body.AccountID, body.ClientID, body.ClientSecret
	oauthBase, apiBase := body.OAuthBaseURL, body.APIBaseURL
	skipVerify := body.InsecureSkipVerify

	// Existing-channel path: look up stored config so the admin
	// doesn't have to re-type the redacted clientSecret.
	if body.ChannelID != "" {
		c, err := s.store.GetChannel(r.Context(), body.ChannelID)
		if err != nil {
			writeErr(w, err)
			return
		}
		if c == nil || c.Type != "zoomchat" {
			http.Error(w, `{"error":"zoom channel not found"}`, http.StatusNotFound)
			return
		}
		var cfg struct {
			AccountID          string `json:"accountId"`
			ClientID           string `json:"clientId"`
			ClientSecret       string `json:"clientSecret"`
			OAuthBaseURL       string `json:"oauthBaseUrl"`
			APIBaseURL         string `json:"apiBaseUrl"`
			InsecureSkipVerify bool   `json:"insecureSkipVerify"`
		}
		if err := json.Unmarshal(c.Config, &cfg); err == nil {
			if accID == "" {
				accID = cfg.AccountID
			}
			if cliID == "" {
				cliID = cfg.ClientID
			}
			if cliSec == "" {
				cliSec = cfg.ClientSecret
			}
			if oauthBase == "" {
				oauthBase = cfg.OAuthBaseURL
			}
			if apiBase == "" {
				apiBase = cfg.APIBaseURL
			}
			// Body-supplied skipVerify wins so an admin can flip the
			// setting on for one probe without persisting it yet.
			if !skipVerify {
				skipVerify = cfg.InsecureSkipVerify
			}
		}
	}

	if accID == "" || cliID == "" || cliSec == "" {
		http.Error(w, `{"error":"accountId, clientId and clientSecret required (or pass existingChannelId to reuse a saved channel's credentials)"}`, http.StatusBadRequest)
		return
	}

	// Cap wall-clock so an unresponsive Zoom doesn't hang the
	// admin's request — the picker can show a clean timeout
	// message and the admin can retry.
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	channels, err := s.notify.ListZoomChannels(ctx, accID, cliID, cliSec, oauthBase, apiBase, skipVerify)
	if err != nil {
		// Even on error we may have a partial list (page cap or
		// late-page failure). Return both so the UI can render
		// what we have alongside the warning.
		http.Error(w, fmt.Sprintf(`{"error":%q,"channels":%s}`,
			err.Error(), mustMarshal(channels)), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"channels": channels})
}

func mustMarshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetChannel(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if c == nil {
		http.Error(w, `{"error":"channel not found"}`, http.StatusNotFound)
		return
	}
	if err := s.notify.SendTest(r.Context(), *c); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"status": "sent"})
}

// ── Maintenance windows ─────────────────────────────────────────────────────
//
// Admin-only CRUD over an operator-declared time range during
// which alert notifications are suppressed. The evaluator
// checks the windows on each firing; problems still open +
// auto-resolve as usual, only the live channel fan-out
// (Slack / email / Zoom / etc.) is skipped. Post-window the
// /anomalies + /incidents pages still show the full timeline,
// so review of "what happened during the deploy?" works
// normally.

func (s *Server) listMaintenanceWindows(w http.ResponseWriter, r *http.Request) {
	includeDisabled := r.URL.Query().Get("all") == "1"
	rows, err := s.store.ListMaintenanceWindows(r.Context(), includeDisabled)
	if err != nil {
		writeErr(w, err)
		return
	}
	if rows == nil {
		rows = []chstore.MaintenanceWindow{}
	}
	writeJSON(w, rows)
}

func (s *Server) createMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Service  string `json:"service"`
		Severity string `json:"severity"`
		StartAt  int64  `json:"startAt"`
		EndAt    int64  `json:"endAt"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	body.Service = strings.TrimSpace(body.Service)
	if body.Service == "" {
		http.Error(w, `{"error":"service required (use '*' for global)"}`, http.StatusBadRequest)
		return
	}
	if body.EndAt <= body.StartAt {
		http.Error(w, `{"error":"endAt must be after startAt"}`, http.StatusBadRequest)
		return
	}
	if body.Severity == "" {
		body.Severity = "*"
	}
	creator := ""
	if u := auth.FromContext(r.Context()); u != nil {
		creator = u.Email
	}
	mw := chstore.MaintenanceWindow{
		ID:        newID(10),
		Service:   body.Service,
		Severity:  body.Severity,
		StartAt:   body.StartAt,
		EndAt:     body.EndAt,
		Reason:    body.Reason,
		CreatedBy: creator,
	}
	if err := s.store.UpsertMaintenanceWindow(r.Context(), mw); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, mw)
}

func (s *Server) deleteMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteMaintenanceWindow(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── User management (admin only) ─────────────────────────────────────────────

// listUsersByTeam returns the active Coremetry users whose
// team label matches ?team=. Powers the owner-team / SRE-team
// chip popover on /service?name=… so an operator can see who
// to ping without leaving the page. Read-only directory data
// (email + role + team) — never includes hashes or auth
// provider details.
func (s *Server) listUsersByTeam(w http.ResponseWriter, r *http.Request) {
	team := strings.TrimSpace(r.URL.Query().Get("team"))
	if team == "" {
		writeJSON(w, []map[string]any{})
		return
	}
	users, err := s.store.ListUsersByTeam(r.Context(), team)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		out = append(out, map[string]any{
			"id":    u.ID,
			"email": u.Email,
			"role":  u.Role,
			"team":  u.Team,
		})
	}
	writeJSON(w, out)
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	// Strip password hashes before sending — defence in depth even though
	// the User struct already json:"-"s them.
	out := make([]map[string]interface{}, 0, len(users))
	for _, u := range users {
		provider := u.AuthProvider
		if provider == "" {
			provider = "local"
		}
		row := map[string]interface{}{
			"id": u.ID, "email": u.Email, "role": u.Role,
			"disabled":     u.Disabled,
			"authProvider": provider,
			"team":         u.Team,
			"createdAt":    u.CreatedAt,
		}
		// Surface customRole + resolved pages for the admin Users
		// page. Same defensive guard as userPayload: only viewers
		// with a valid pointer get the fields populated.
		if u.Role == auth.RoleViewer && u.CustomRole != "" {
			if pages := s.auth.CustomRolePages(u.CustomRole); pages != nil {
				row["customRole"] = u.CustomRole
				row["customRolePages"] = pages
			}
		}
		out = append(out, row)
	}
	writeJSON(w, out)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
		Team     string `json:"team"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	body.Team = strings.TrimSpace(body.Team)
	if body.Email == "" || len(body.Password) < 6 {
		http.Error(w, `{"error":"email and password (>=6 chars) required"}`, http.StatusBadRequest)
		return
	}
	if body.Role != auth.RoleAdmin && body.Role != auth.RoleEditor && body.Role != auth.RoleViewer {
		// Pre-v0.4.89 this fell through to viewer for `editor`
		// too — the role exists in auth.go + the SPA UI but the
		// guard was missing editor. Operators saw their editor
		// pick silently downgrade on save.
		body.Role = auth.RoleViewer
	}
	existing, err := s.store.GetUserByEmail(r.Context(), body.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	if existing != nil {
		http.Error(w, `{"error":"email already exists"}`, http.StatusConflict)
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeErr(w, err)
		return
	}
	u := chstore.User{
		ID:           newID(8),
		Email:        body.Email,
		PasswordHash: hash,
		Role:         body.Role,
		Team:         body.Team,
	}
	if err := s.store.UpsertUser(r.Context(), u); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"email": u.Email, "role": u.Role, "team": u.Team})
	s.audit(r, "user.create", "user", u.ID, string(details))
	writeJSON(w, map[string]interface{}{
		"id": u.ID, "email": u.Email, "role": u.Role, "team": u.Team,
	})
}

// setUserTeam updates the team label on a user. Empty body
// team clears the assignment so the SPA can use the same
// endpoint for both rename and unassign flows.
func (s *Server) setUserTeam(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Team string `json:"team"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	body.Team = strings.TrimSpace(body.Team)
	if err := s.store.SetUserTeam(r.Context(), id, body.Team); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"team": body.Team})
	s.audit(r, "user.set_team", "user", id, string(details))
	writeJSON(w, map[string]string{"team": body.Team})
}

// setUserRole flips a user's role to one of admin / editor /
// viewer. Same guard the deleteUser handler uses — refuses to
// demote the last admin so the system never locks itself out.
// Operators trying to demote themselves are allowed (handing
// off admin to a teammate is a legitimate flow) but only when
// there's another admin still in place.
func (s *Server) setUserRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	role := strings.TrimSpace(body.Role)
	if role != auth.RoleAdmin && role != auth.RoleEditor && role != auth.RoleViewer {
		http.Error(w, `{"error":"role must be admin, editor, or viewer"}`, http.StatusBadRequest)
		return
	}
	target, err := s.store.GetUserByID(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if target == nil {
		http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
		return
	}
	// No-op fast path — keeps audit log + cache from churning
	// on the typical "operator clicked the same value" race.
	if target.Role == role {
		writeJSON(w, map[string]any{"id": target.ID, "email": target.Email, "role": target.Role})
		return
	}
	// Last-admin guard: refuse if this change would leave the
	// system without any admin. Same shape as deleteUser's
	// CountAdmins gate.
	if target.Role == auth.RoleAdmin && role != auth.RoleAdmin {
		n, err := s.store.CountAdmins(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		if n <= 1 {
			http.Error(w, `{"error":"cannot demote the last admin"}`, http.StatusBadRequest)
			return
		}
	}
	prevRole := target.Role
	target.Role = role
	if err := s.store.UpsertUser(r.Context(), *target); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"email": target.Email, "from": prevRole, "to": role})
	s.audit(r, "user.set_role", "user", target.ID, string(details))
	writeJSON(w, map[string]any{"id": target.ID, "email": target.Email, "role": target.Role})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	caller := auth.FromContext(r.Context())
	if caller != nil && caller.UserID == id {
		http.Error(w, `{"error":"cannot delete your own account"}`, http.StatusBadRequest)
		return
	}
	target, err := s.store.GetUserByID(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if target == nil {
		http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
		return
	}
	// Refuse to remove the last remaining admin — locks the system.
	if target.Role == auth.RoleAdmin {
		n, err := s.store.CountAdmins(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		if n <= 1 {
			http.Error(w, `{"error":"cannot remove the last admin"}`, http.StatusBadRequest)
			return
		}
	}
	if err := s.store.DisableUser(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"email": target.Email, "role": target.Role})
	s.audit(r, "user.delete", "user", id, string(details))
	writeJSON(w, map[string]string{"status": "ok"})
}

// resetUserPassword lets an admin set a new password for any user without
// supplying the current one — used by the user-management screen.
func (s *Server) resetUserPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if len(body.Password) < 6 {
		http.Error(w, `{"error":"password must be at least 6 characters"}`, http.StatusBadRequest)
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.store.UpdatePassword(r.Context(), id, hash); err != nil {
		writeErr(w, err)
		return
	}
	// Audit row carries no password material — just that someone
	// admin-reset this user's credential.
	s.audit(r, "user.reset_password", "user", id, "")
	writeJSON(w, map[string]string{"status": "ok"})
}

// ── Exceptions / Errors page ─────────────────────────────────────────────────

// ── Errors Inbox ─────────────────────────────────────────────────────────────

func (s *Server) listExceptionGroups(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := chstore.ExceptionGroupFilter{
		State:    q.Get("state"),
		Service:  q.Get("service"),
		Assignee: q.Get("assignee"),
		Limit:    parseInt(q.Get("limit"), 50),
		Offset:   parseInt(q.Get("offset"), 0),
	}
	items, err := s.store.ListExceptionGroups(r.Context(), f)
	if err != nil { writeErr(w, err); return }
	total, err := s.store.CountExceptionGroups(r.Context(), f)
	if err != nil { writeErr(w, err); return }
	// `items` can be nil from the store on an empty page — serialise
	// as [] so the frontend never has to null-guard the array.
	if items == nil {
		items = []chstore.ExceptionGroup{}
	}
	writeJSON(w, map[string]any{
		"items":  items,
		"total":  total,
		"limit":  f.Limit,
		"offset": f.Offset,
	})
}

func (s *Server) getExceptionGroupSamples(w http.ResponseWriter, r *http.Request) {
	limit := parseInt(r.URL.Query().Get("limit"), 10)
	out, err := s.store.GetExceptionGroupSamples(r.Context(), r.PathValue("fp"), limit)
	if err != nil { writeErr(w, err); return }
	writeJSON(w, out)
}

func (s *Server) setExceptionGroupState(w http.ResponseWriter, r *http.Request) {
	var body struct{ State string `json:"state"` }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest); return
	}
	switch body.State {
	case chstore.ExStateNew, chstore.ExStateAcknowledged,
		chstore.ExStateResolved, chstore.ExStateIgnored:
		// ok
	default:
		http.Error(w, `{"error":"invalid state"}`, http.StatusBadRequest); return
	}
	if err := s.store.SetExceptionGroupState(r.Context(), r.PathValue("fp"), body.State); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) assignExceptionGroup(w http.ResponseWriter, r *http.Request) {
	var body struct{ Assignee string `json:"assignee"` }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest); return
	}
	if err := s.store.AssignExceptionGroup(r.Context(), r.PathValue("fp"), body.Assignee); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) listExceptions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, err := s.store.GetExceptions(r.Context(), chstore.ExceptionFilter{
		Service: q.Get("service"),
		GroupBy: q.Get("groupBy"),
		From:    parseTime(q.Get("from")),
		To:      parseTime(q.Get("to")),
		Limit:   parseInt(q.Get("limit"), 100),
	})
	if err != nil { writeErr(w, err); return }
	writeJSON(w, rows)
}

// ── Service backtrace ────────────────────────────────────────────────────────

// svcSpanBreakdown returns time-bucketed cumulative duration per
// span category (db / queue / http / client / server / internal)
// for a single service. Drives Elastic-APM-style "where does this
// service spend its time?" stacked-area chart on the service
// detail page. Cached 30s — the surrounding page re-renders on
// every range change and an SRE typically clicks through 4-5
// services during triage.
func (s *Server) svcSpanBreakdown(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	svc := r.PathValue("name")
	to := parseTime(q.Get("to"))
	if to.IsZero() {
		to = time.Now()
	}
	from := parseTime(q.Get("from"))
	if from.IsZero() {
		from = to.Add(-1 * time.Hour)
	}
	key := fmt.Sprintf("svc-breakdown:svc=%s:from=%d:to=%d", svc, from.UnixNano(), to.UnixNano())
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetSpanBreakdown(r.Context(), svc, from, to)
	})
}

// svcOperationSummary returns per-operation aggregates for a single
// service. Drives the Operations table on the service detail page.
func (s *Server) svcOperationSummary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	svc := r.PathValue("name")
	since := parseDuration(q.Get("since"), 24*time.Hour)
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	key := fmt.Sprintf("svc-ops:svc=%s:since=%s:from=%s:to=%s",
		svc, q.Get("since"), q.Get("from"), q.Get("to"))
	// 30s TTL — operation set changes on deploys (minutes
	// apart), not seconds. With the SWR tier in cache.go, a
	// 30s soft TTL still gives 90s of stale-but-usable
	// fallback before a hard miss; net effect is the
	// operator never sees a cold-load delay on this endpoint
	// during normal traffic. Pre-v0.5.58 this was 15s which
	// half-the-time forced an upstream re-fetch the operator
	// would never notice if it was stale.
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetOperationSummary(r.Context(), svc, since, from, to)
	})
}

func (s *Server) svcCallers(w http.ResponseWriter, r *http.Request) {
	since := parseDuration(r.URL.Query().Get("since"), 24*time.Hour)
	out, err := s.store.CallersOf(r.Context(), r.PathValue("name"), since)
	if err != nil { writeErr(w, err); return }
	writeJSON(w, out)
}

func (s *Server) svcCallees(w http.ResponseWriter, r *http.Request) {
	since := parseDuration(r.URL.Query().Get("since"), 24*time.Hour)
	out, err := s.store.CalleesOf(r.Context(), r.PathValue("name"), since)
	if err != nil { writeErr(w, err); return }
	writeJSON(w, out)
}

// ── Public status page ──────────────────────────────────────────────────────
//
// Single read endpoint /api/public-status returns the entire payload
// the public page needs in one shot: page config + each component's
// current state + 90-day uptime bar + recent published incidents.
// Cached 30s — high-traffic status pages can hammer this and we don't
// want to thrash the underlying queries.

type publicComponentRow struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"` // operational | degraded | outage | unknown
	Message     string    `json:"message,omitempty"`
	UptimeDays  []float64 `json:"uptimeDays,omitempty"` // 90 ratios; -1 = no data
}

type publicIncidentRow struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Body       string  `json:"body,omitempty"`
	Status     string  `json:"status"`
	Severity   string  `json:"severity"`
	StartedAt  int64   `json:"startedAt"`
	ResolvedAt *int64  `json:"resolvedAt,omitempty"`
}

type publicStatusResp struct {
	Title       string                `json:"title"`
	Description string                `json:"description,omitempty"`
	SupportURL  string                `json:"supportUrl,omitempty"`
	Status      string                `json:"status"` // worst-of components
	CheckedAt   string                `json:"checkedAt"`
	Components  []publicComponentRow  `json:"components"`
	Incidents   []publicIncidentRow   `json:"incidents"`
}

func (s *Server) publicStatus(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "public-status", 30*time.Second, func() (any, error) {
		return s.collectPublicStatus(r.Context())
	})
}

func (s *Server) collectPublicStatus(ctx context.Context) (publicStatusResp, error) {
	cfg, err := s.store.GetStatusPageConfig(ctx)
	if err != nil {
		return publicStatusResp{}, err
	}
	comps, err := s.store.ListStatusComponents(ctx)
	if err != nil {
		return publicStatusResp{}, err
	}
	monStatus, _ := s.store.LastMonitorStatus(ctx)
	openProblems, _ := s.store.ListProblems(ctx, chstore.ProblemFilter{Status: "open", Limit: 500})
	openByService := map[string]chstore.Problem{}
	for _, p := range openProblems {
		// Track the worst severity per service
		prev, ok := openByService[p.Service]
		if !ok || severityRank(p.Severity) > severityRank(prev.Severity) {
			openByService[p.Service] = p
		}
	}

	rank := map[string]int{"operational": 0, "degraded": 1, "outage": 2}
	worst := "operational"
	rows := make([]publicComponentRow, 0, len(comps))
	for _, c := range comps {
		row := publicComponentRow{ID: c.ID, Name: c.Name, Description: c.Description, Status: "operational"}
		if c.MonitorID != "" {
			if last, ok := monStatus[c.MonitorID]; ok {
				switch last.Status {
				case "up":
					row.Status = "operational"
				case "down":
					row.Status = "outage"
					row.Message = last.Message
				case "degraded":
					row.Status = "degraded"
					row.Message = last.Message
				default:
					row.Status = "unknown"
				}
			} else {
				row.Status = "unknown"
				row.Message = "no probe data yet"
			}
			// 90-day uptime bar — same source the operator picked.
			if up, err := s.store.ComponentUptime(ctx, c.MonitorID, 90); err == nil {
				row.UptimeDays = up
			}
		} else if c.ServiceName != "" {
			if p, ok := openByService[c.ServiceName]; ok {
				switch p.Severity {
				case "critical":
					row.Status = "outage"
				default:
					row.Status = "degraded"
				}
				row.Message = p.RuleName
			}
		}
		if rank[row.Status] > rank[worst] {
			worst = row.Status
		}
		rows = append(rows, row)
	}

	// Recent published incidents.
	pubIncidents, _, err := s.store.ListPublishedIncidents(ctx, 30)
	if err != nil {
		return publicStatusResp{}, err
	}
	incRows := make([]publicIncidentRow, 0, len(pubIncidents))
	for _, i := range pubIncidents {
		incRows = append(incRows, publicIncidentRow{
			ID: i.ID, Title: i.Title, Body: i.Summary,
			Status: i.Status, Severity: i.Severity,
			StartedAt: i.StartedAt, ResolvedAt: i.ResolvedAt,
		})
	}

	return publicStatusResp{
		Title:       cfg.Title,
		Description: cfg.Description,
		SupportURL:  cfg.SupportURL,
		Status:      worst,
		CheckedAt:   time.Now().UTC().Format(time.RFC3339),
		Components:  rows,
		Incidents:   incRows,
	}, nil
}

func severityRank(sev string) int {
	switch strings.ToLower(sev) {
	case "critical":
		return 2
	case "warning":
		return 1
	default:
		return 0
	}
}

// publicStatusSubscribe — public, unauth POST. Records the email
// as unverified, mints a confirm token, and (if SMTP is wired up)
// delivers a confirmation link. v0.5.158 promoted this from
// instant-verified to double-opt-in so an attacker can't enrol
// arbitrary email addresses into the operator's notification
// stream. Response shape never leaks "does this email exist"
// (the same `status:pending-confirmation` is returned regardless
// of whether the row was new or re-issued), to avoid email
// enumeration through timing or response variance.
//
// Per-IP rate limit: 1 accepted call per 60s. Above that we
// return 429 immediately — a real subscriber would never refresh
// the page that fast, and the limit is small enough to make
// brute-force token guessing or signup flooding uneconomical.
func (s *Server) publicStatusSubscribe(w http.ResponseWriter, r *http.Request) {
	if !s.allowSubscribeRate(clientIP(r)) {
		http.Error(w, "rate limited — try again in a minute", http.StatusTooManyRequests)
		return
	}
	var body struct{ Email string `json:"email"` }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest); return
	}
	email := strings.TrimSpace(strings.ToLower(body.Email))
	if email == "" || !strings.ContainsRune(email, '@') {
		http.Error(w, "valid email required", http.StatusBadRequest); return
	}
	token, err := s.store.AddStatusSubscriber(r.Context(), email)
	if err != nil {
		writeErr(w, err); return
	}
	// If token came back empty, the row was already verified —
	// we still respond OK so the caller can't tell.
	if token != "" {
		if err := s.sendStatusConfirmation(r, email, token); err != nil {
			// SMTP not configured / transient failure — log + tell
			// the user the operator will reach out manually. We do
			// NOT roll back the row; the token sits in CH and the
			// operator can resend from the admin UI later.
			log.Printf("[status-page] confirm mail to %s failed: %v", email, err)
			writeJSON(w, map[string]string{
				"status":  "pending-confirmation",
				"message": "We've recorded your email. Email delivery isn't yet configured on this status page — the operator will reach out to confirm.",
			})
			return
		}
	}
	writeJSON(w, map[string]string{
		"status":  "pending-confirmation",
		"message": "Check your inbox for a confirmation link. The subscription is inactive until you click it.",
	})
}

// publicStatusConfirm — public, unauth GET. Consumes the
// confirmation token and flips the row to verified. Renders a
// plain HTML thank-you page rather than JSON because the
// audience for this URL is the human clicking the link, not the
// SPA.
func (s *Server) publicStatusConfirm(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	email, err := s.store.ConfirmStatusSubscriber(r.Context(), token)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err != nil || email == "" {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, statusConfirmHTML("Link not valid",
			"This confirmation link has expired or has already been used. If you didn't receive the original email, please re-subscribe."))
		return
	}
	fmt.Fprint(w, statusConfirmHTML("You're subscribed",
		"Thanks — "+html.EscapeString(email)+" will now receive updates when an incident opens or resolves. You can unsubscribe by replying to any incident email."))
}

// allowSubscribeRate gates publicStatusSubscribe at one accept
// per IP per 60s. Returns true if the call is allowed (and
// records the timestamp); false to reject. Map is pruned of
// stale entries opportunistically so it doesn't leak memory
// under a long-running flood.
func (s *Server) allowSubscribeRate(ip string) bool {
	if ip == "" {
		return true // can't rate-limit unknown
	}
	now := time.Now().Unix()
	s.subRateMu.Lock()
	defer s.subRateMu.Unlock()
	// Prune entries older than the window so the map stays
	// bounded under sustained traffic.
	for k, t := range s.subRateBy {
		if now-t > 60 {
			delete(s.subRateBy, k)
		}
	}
	if last, ok := s.subRateBy[ip]; ok && now-last < 60 {
		return false
	}
	s.subRateBy[ip] = now
	return true
}

// sendStatusConfirmation composes + dispatches the
// confirmation email through the operator's configured SMTP.
// Builds the URL from the request scheme + host so the link
// always resolves on whatever interface the operator surfaces
// the status page on.
func (s *Server) sendStatusConfirmation(r *http.Request, email, token string) error {
	if s.notify == nil {
		return fmt.Errorf("notifier not configured")
	}
	scheme := "http"
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		scheme = v
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if v := r.Header.Get("X-Forwarded-Host"); v != "" {
		host = v
	}
	link := fmt.Sprintf("%s://%s/api/public-status/confirm?token=%s", scheme, host, token)
	subject := "Confirm your status page subscription"
	body := "Click the link below to confirm your subscription to status page updates.\n\n" +
		link + "\n\n" +
		"If you didn't request this, you can ignore this message — your address won't receive further mail."
	return s.notify.SendMail(r.Context(), []string{email}, subject, body)
}

func statusConfirmHTML(heading, body string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><title>` +
		html.EscapeString(heading) + `</title>
<style>
  body { font-family: -apple-system, system-ui, sans-serif; max-width: 480px; margin: 80px auto; padding: 0 16px; color: #1f2328; }
  h1 { font-size: 22px; margin: 0 0 12px; }
  p { font-size: 14px; line-height: 1.6; color: #4b5563; }
</style></head><body>
<h1>` + html.EscapeString(heading) + `</h1>
<p>` + body + `</p>
</body></html>`
}

// ── Public status page admin ────────────────────────────────────────────────

func (s *Server) statusPageGetConfig(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetStatusPageConfig(r.Context())
	if err != nil { writeErr(w, err); return }
	writeJSON(w, c)
}
func (s *Server) statusPagePutConfig(w http.ResponseWriter, r *http.Request) {
	var c chstore.StatusPageConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest); return
	}
	if err := s.store.UpsertStatusPageConfig(r.Context(), c); err != nil {
		writeErr(w, err); return
	}
	s.audit(r, "status_page.config_update", "status_page", "", "")
	s.cacheInvalidate(r.Context(), "public-status")
	writeJSON(w, c)
}
func (s *Server) statusPageListComponents(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListStatusComponents(r.Context())
	if err != nil { writeErr(w, err); return }
	writeJSON(w, rows)
}
func (s *Server) statusPageCreateComponent(w http.ResponseWriter, r *http.Request) {
	var c chstore.StatusComponent
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest); return
	}
	c.ID = ""
	if err := s.store.UpsertStatusComponent(r.Context(), &c); err != nil { writeErr(w, err); return }
	s.audit(r, "status_page.component_create", "status_page", c.ID, c.Name)
	s.cacheInvalidate(r.Context(), "public-status")
	writeJSON(w, c)
}
func (s *Server) statusPageUpdateComponent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var c chstore.StatusComponent
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest); return
	}
	c.ID = id
	if err := s.store.UpsertStatusComponent(r.Context(), &c); err != nil { writeErr(w, err); return }
	s.audit(r, "status_page.component_update", "status_page", id, c.Name)
	s.cacheInvalidate(r.Context(), "public-status")
	writeJSON(w, c)
}
func (s *Server) statusPageDeleteComponent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteStatusComponent(r.Context(), id); err != nil {
		writeErr(w, err); return
	}
	s.audit(r, "status_page.component_delete", "status_page", id, "")
	s.cacheInvalidate(r.Context(), "public-status")
	writeJSON(w, map[string]string{"status": "deleted"})
}
func (s *Server) statusPageListSubscribers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListStatusSubscribers(r.Context())
	if err != nil { writeErr(w, err); return }
	writeJSON(w, rows)
}
func (s *Server) statusPageDeleteSubscriber(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("email")
	if email == "" { http.Error(w, "email required", http.StatusBadRequest); return }
	if err := s.store.RemoveStatusSubscriber(r.Context(), email); err != nil { writeErr(w, err); return }
	s.audit(r, "status_page.subscriber_delete", "status_page", email, "")
	writeJSON(w, map[string]string{"status": "deleted"})
}
func (s *Server) statusPagePublishIncident(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p chstore.PublishedIncident
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest); return
	}
	p.IncidentID = id
	if err := s.store.SetIncidentPublished(r.Context(), p); err != nil { writeErr(w, err); return }
	s.audit(r, "status_page.incident_publish", "status_page", id, fmt.Sprintf("published=%v", p.Published))
	s.cacheInvalidate(r.Context(), "public-status")
	writeJSON(w, p)
}

// ── Runtime settings: data retention ────────────────────────────────────────

// getRetention returns the live override values (or empty fields when
// no override is set, in which case the table is on whatever TTL the
// config-file default seeded). The UI renders empty fields as
// placeholders showing the config defaults.
func (s *Server) getRetention(w http.ResponseWriter, r *http.Request) {
	sp, err := s.store.GetRetention(r.Context())
	if err != nil { writeErr(w, err); return }
	writeJSON(w, sp)
}

// putRetention applies new TTLs via ALTER TABLE and persists the new
// values to system_settings so a restart picks them up.
func (s *Server) putRetention(w http.ResponseWriter, r *http.Request) {
	var sp chstore.RetentionSpec
	if err := json.NewDecoder(r.Body).Decode(&sp); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest); return
	}
	if err := s.store.SetRetention(r.Context(), sp, actorOf(r)); err != nil {
		writeErr(w, err); return
	}
	s.audit(r, "retention.update", "retention", "", retentionDetails(sp))
	// v0.7.28 — kick an immediate enforcer sweep so REDUCING retention reclaims
	// old day-partitions within seconds instead of waiting for the worker's
	// hourly tick. ALTER MODIFY TTL runs with materialize_ttl_after_modify=0 (so
	// it does NOT re-evaluate existing parts on the spot — that would block the
	// request at scale), and CH's merge-based TTL drop can lag hours; without
	// this sweep an operator who drops retention from 30d→1d keeps seeing the
	// old spans until the next hourly worker pass. Detached context: the request
	// ctx is canceled once we respond (the v0.7.12 context-canceled lesson).
	// DROP PARTITION is idempotent + metadata-only, so a one-shot from the API
	// pod is safe even though the periodic sweep is worker-leader-gated.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := s.store.EnforceRetention(ctx); err != nil {
			log.Printf("[retention] post-apply sweep: %v", err)
		}
	}()
	writeJSON(w, sp)
}

// retentionDetails builds the audit-log detail string for a retention change —
// only the fields the operator actually set (empty fields preserve the prior
// value, so they're not part of this mutation).
func retentionDetails(sp chstore.RetentionSpec) string {
	var parts []string
	if sp.Spans != "" {
		parts = append(parts, "spans="+sp.Spans)
	}
	if sp.Logs != "" {
		parts = append(parts, "logs="+sp.Logs)
	}
	if sp.Metrics != "" {
		parts = append(parts, "metrics="+sp.Metrics)
	}
	if sp.Profiles != "" {
		parts = append(parts, "profiles="+sp.Profiles)
	}
	return strings.Join(parts, " ")
}

// getAnomalyPromotion returns the live anomaly auto-promotion
// config (peak ratio / sustained / count thresholds + enable
// flag). Defaults are baked in chstore so a never-edited
// install gets the v0.5.59 behaviour back from the GET.
func (s *Server) getAnomalyPromotion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.GetAnomalyPromotion(r.Context()))
}

// putAnomalyPromotion validates + persists a new config and
// the next evaluator tick picks it up on its own (the sweep
// reads the store fresh each pass). Bounds-check the
// thresholds so a typo doesn't disable the feature
// silently — fail loud rather than save zero values that
// patch back to defaults on next read.
func (s *Server) putAnomalyPromotion(w http.ResponseWriter, r *http.Request) {
	var c chstore.AnomalyPromotionConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if c.MinPeakRatio < 1 || c.MinPeakRatio > 1000 {
		http.Error(w, "minPeakRatio must be between 1 and 1000", http.StatusBadRequest)
		return
	}
	if c.MinSustainedSec < 60 || c.MinSustainedSec > 24*3600 {
		http.Error(w, "minSustainedSec must be between 60 and 86400", http.StatusBadRequest)
		return
	}
	if c.MinCount == 0 || c.MinCount > 1_000_000 {
		http.Error(w, "minCount must be between 1 and 1000000", http.StatusBadRequest)
		return
	}
	if err := s.store.SaveAnomalyPromotion(r.Context(), c); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, c)
}

// ── AI Copilot ──────────────────────────────────────────────────────────────

// copilotConfig surfaces whether the feature is enabled — UI uses this
// to show or hide the "AI explain" buttons. Doesn't leak the key.
func (s *Server) copilotConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"enabled": s.copilot.Configured()})
}

// getAISettings returns the current Copilot config minus the actual
// key. UI uses it to drive the editable form. We expose
// {provider, model, baseUrl, hasKey} — the secret never round-trips.
// baseUrl is non-secret (operators put it in their Helm values), so
// we echo it so the UI can show "currently pointing at <local
// endpoint>" without operator memorisation.
func (s *Server) getAISettings(w http.ResponseWriter, r *http.Request) {
	provider, model, baseURL, hasKey, skipTLS := s.copilot.Snapshot()
	writeJSON(w, map[string]any{
		"provider": provider,
		"model":    model,
		"baseUrl":  baseURL,
		"hasKey":   hasKey,
		"skipTls":  skipTLS,
	})
}

// putAISettings saves a new provider + key (+ optional model + base
// URL) and updates the live service. Body shape matches the GET
// response sans hasKey, plus an apiKey field. An empty apiKey + non-
// "openai" provider legitimately disables the feature (UI's "remove
// key" path); the openai provider with a baseUrl but no key is the
// "local Ollama, no auth" config.
func (s *Server) putAISettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider string `json:"provider"`
		APIKey   string `json:"apiKey"`
		Model    string `json:"model"`
		BaseURL  string `json:"baseUrl"`
		SkipTLS  bool   `json:"skipTls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest); return
	}
	if in.Provider != "" &&
		in.Provider != copilot.ProviderAnthropic &&
		in.Provider != copilot.ProviderGitHub &&
		in.Provider != copilot.ProviderOpenAI {
		http.Error(w, "provider must be 'anthropic', 'github' or 'openai'", http.StatusBadRequest); return
	}
	if err := s.copilot.SavePersisted(r.Context(), s.store, in.Provider, in.APIKey, in.Model, in.BaseURL, in.SkipTLS); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError); return
	}
	s.publishConfigReload(r.Context(), "ai")
	provider, model, baseURL, hasKey, skipTLS := s.copilot.Snapshot()
	// apiKey itself never enters audit_log; hasKey is the only
	// secret-adjacent bit and it's already part of the public GET.
	details, _ := json.Marshal(map[string]any{
		"provider": provider, "model": model, "baseUrl": baseURL, "hasKey": hasKey, "skipTls": skipTLS,
	})
	s.audit(r, "settings.ai.update", "settings", "ai", string(details))
	writeJSON(w, map[string]any{
		"provider": provider,
		"model":    model,
		"baseUrl":  baseURL,
		"hasKey":   hasKey,
		"skipTls":  skipTLS,
	})
}

// copilotExplainTrace fetches the spans for a trace, builds a compact
// JSON description, and asks the model for an SRE-flavoured summary.
// Heavy lifting (gathering context) happens server-side so the
// browser doesn't ship trace data back to the API just to ship it on
// to Anthropic.
func (s *Server) copilotExplainTrace(w http.ResponseWriter, r *http.Request) {
	if !s.copilot.Configured() {
		http.Error(w, "AI copilot not configured", http.StatusServiceUnavailable); return
	}
	id := r.PathValue("id")
	spans, err := s.store.GetTrace(r.Context(), id)
	if err != nil { writeErr(w, err); return }
	if len(spans) == 0 {
		http.Error(w, "trace not found", http.StatusNotFound); return
	}
	// Compact each span — full attribute maps blow the prompt for big
	// traces. Keep just the fields a senior engineer would want.
	type lite struct {
		Name       string  `json:"name"`
		Service    string  `json:"service"`
		Kind       string  `json:"kind"`
		ParentSpan string  `json:"parent,omitempty"`
		SpanID     string  `json:"id"`
		DurationMs float64 `json:"durMs"`
		Status     string  `json:"status,omitempty"`
		StatusMsg  string  `json:"statusMsg,omitempty"`
	}
	cap := 100
	if len(spans) > cap {
		spans = spans[:cap] // bound the prompt; large traces still get a useful summary from the head
	}
	compact := make([]lite, 0, len(spans))
	for _, sp := range spans {
		dur := float64(sp.EndTime-sp.StartTime) / 1e6
		l := lite{Name: sp.Name, Service: sp.ServiceName, Kind: sp.Kind,
			ParentSpan: sp.ParentSpanID, SpanID: sp.SpanID, DurationMs: dur}
		if sp.StatusCode == "error" {
			l.Status = "error"
			l.StatusMsg = sp.StatusMessage
		}
		compact = append(compact, l)
	}
	payload, _ := json.Marshal(compact)
	user := fmt.Sprintf("Trace %s with %d spans:\n```json\n%s\n```", id, len(compact), string(payload))
	out, err := s.copilotExplain(r, copilot.SystemPromptTrace(), user)
	if err != nil { writeErr(w, err); return }
	writeJSON(w, map[string]string{"explanation": out})
}

// copilotExplainSpan focuses the LLM on ONE span instead of the
// whole trace: target span + parent + direct children + any
// error spans in the same trace. Tighter prompt + cheaper round-
// trip than re-summarising the entire waterfall. Works with any
// configured backend — Anthropic, OpenAI, or a local LLM via
// OpenAI-compatible base URL (Ollama / vLLM / LM Studio).
func (s *Server) copilotExplainSpan(w http.ResponseWriter, r *http.Request) {
	if !s.copilot.Configured() {
		http.Error(w, "AI copilot not configured", http.StatusServiceUnavailable); return
	}
	traceID := r.PathValue("traceId")
	spanID := strings.TrimSpace(r.URL.Query().Get("span"))
	if spanID == "" {
		http.Error(w, "span query param required", http.StatusBadRequest); return
	}
	spans, err := s.store.GetTrace(r.Context(), traceID)
	if err != nil { writeErr(w, err); return }
	if len(spans) == 0 {
		http.Error(w, "trace not found", http.StatusNotFound); return
	}
	// Locate target + build the neighbourhood subset. O(n) on a
	// trace's span list which is bounded by the existing 100-span
	// cap on the trace-fetch path.
	var target *chstore.SpanRow
	for i := range spans {
		if spans[i].SpanID == spanID {
			target = &spans[i]
			break
		}
	}
	if target == nil {
		http.Error(w, "span not found in trace", http.StatusNotFound); return
	}
	// Keep: target, its parent (if any), direct children, any
	// error spans elsewhere in the trace (high signal for "why
	// did this fail" hints). Dedupe via span-id set.
	keep := map[string]bool{target.SpanID: true}
	if target.ParentSpanID != "" {
		keep[target.ParentSpanID] = true
	}
	for i := range spans {
		sp := &spans[i]
		if sp.ParentSpanID == target.SpanID {
			keep[sp.SpanID] = true
		}
		if sp.StatusCode == "error" {
			keep[sp.SpanID] = true
		}
	}
	type lite struct {
		Role       string  `json:"role"` // target | parent | child | error-elsewhere
		Name       string  `json:"name"`
		Service    string  `json:"service"`
		Kind       string  `json:"kind"`
		SpanID     string  `json:"id"`
		ParentID   string  `json:"parent,omitempty"`
		DurationMs float64 `json:"durMs"`
		Status     string  `json:"status,omitempty"`
		StatusMsg  string  `json:"statusMsg,omitempty"`
	}
	role := func(sp *chstore.SpanRow) string {
		switch {
		case sp.SpanID == target.SpanID:
			return "target"
		case sp.SpanID == target.ParentSpanID:
			return "parent"
		case sp.ParentSpanID == target.SpanID:
			return "child"
		default:
			return "error-elsewhere"
		}
	}
	compact := make([]lite, 0, len(keep))
	for i := range spans {
		sp := &spans[i]
		if !keep[sp.SpanID] {
			continue
		}
		dur := float64(sp.EndTime-sp.StartTime) / 1e6
		l := lite{
			Role: role(sp), Name: sp.Name, Service: sp.ServiceName,
			Kind: sp.Kind, SpanID: sp.SpanID, ParentID: sp.ParentSpanID,
			DurationMs: dur,
		}
		if sp.StatusCode == "error" {
			l.Status = "error"
			l.StatusMsg = sp.StatusMessage
		}
		compact = append(compact, l)
	}
	payload, _ := json.Marshal(compact)
	user := fmt.Sprintf("Span %s (target) in trace %s — %d spans in context:\n```json\n%s\n```",
		spanID, traceID, len(compact), string(payload))
	out, err := s.copilotExplain(r, copilot.SystemPromptSpan(), user)
	if err != nil { writeErr(w, err); return }
	writeJSON(w, map[string]string{"explanation": out})
}

// copilotExplainProblem fetches a Problem and asks the model for a
// likely-cause + first-action summary. Useful on a fresh page during
// an incident.
func (s *Server) copilotExplainProblem(w http.ResponseWriter, r *http.Request) {
	if !s.copilot.Configured() {
		http.Error(w, "AI copilot not configured", http.StatusServiceUnavailable); return
	}
	id := r.PathValue("id")
	probs, err := s.store.ListProblems(r.Context(), chstore.ProblemFilter{Limit: 1000})
	if err != nil { writeErr(w, err); return }
	var p *chstore.Problem
	for i := range probs {
		if probs[i].ID == id {
			p = &probs[i]; break
		}
	}
	if p == nil {
		http.Error(w, "problem not found", http.StatusNotFound); return
	}
	// Attach deploy correlation for the explain prompt — the
	// "regression right after a deploy" pattern is the single
	// highest-signal cause hint the model should consider first.
	enriched := s.store.EnrichProblemsWithDeploys(r.Context(),
		[]chstore.Problem{*p}, 30*time.Minute)
	p = &enriched[0]
	user := fmt.Sprintf(
		"Service: %s\nMetric: %s\nValue: %.2f (threshold %.2f)\nSeverity: %s\nRule: %s\nDescription: %s",
		p.Service, p.Metric, p.Value, p.Threshold, p.Severity, p.RuleName, p.Description,
	)
	if p.RecentDeploy != nil {
		user += fmt.Sprintf(
			"\n\nRecent deploy: service.version=%q first seen %d seconds before this problem opened. Consider whether this regression coincides with that deploy.",
			p.RecentDeploy.Version, p.RecentDeploy.AgeSeconds)
	}
	// v0.6.54 — multi-signal root-cause correlation. Beyond the
	// deploy hint, gather the service's topology neighbours, error-
	// trace exemplars, and significant log patterns around the
	// problem's open time so the model ranks causes from evidence
	// instead of metric-shape priors alone. All bounded (top-5,
	// windowed) and best-effort — a failed signal lookup just omits
	// that block rather than failing the explain. Only meaningful
	// when the problem is scoped to a concrete service.
	if p.Service != "" {
		user += s.problemCorrelationContext(r.Context(), p)
	}
	out, err := s.copilotExplain(r, copilot.SystemPromptProblem(), user)
	if err != nil { writeErr(w, err); return }
	writeJSON(w, map[string]string{"explanation": out})
}

// problemCorrelationContext gathers the multi-signal evidence the
// root-cause prompt reasons over (v0.6.54): 1-hop topology
// neighbours, error-trace exemplars, and significant log patterns
// around the problem's open time. All bounded (top-5, windowed) and
// best-effort — any signal that errors or comes up empty is simply
// omitted so a flaky lookup never blocks the explain. Window is
// [open-30m, open+5m]; the leading 30m captures the run-up, the
// trailing 5m the immediate aftermath.
func (s *Server) problemCorrelationContext(ctx context.Context, p *chstore.Problem) string {
	var b strings.Builder
	open := time.Unix(0, p.StartedAt)
	from, to := open.Add(-30*time.Minute), open.Add(5*time.Minute)

	// 1) Topology neighbours — who calls / is called by this service.
	// Direction is the key root-cause hint: a callee erroring points
	// downstream, a caller spike points upstream.
	if up, down, _, _, err := s.store.ServiceNeighbors(ctx, p.Service, 35*time.Minute, 50); err == nil && (len(up) > 0 || len(down) > 0) {
		b.WriteString("\n\nTopology neighbours (from trace structure):")
		if len(up) > 0 {
			b.WriteString("\n  callers: " + topNeighborNames(up, 5))
		}
		if len(down) > 0 {
			b.WriteString("\n  callees: " + topNeighborNames(down, 5))
		}
	}

	// 2) Error-trace exemplars for this service in the window.
	if traces, _, _, terr := s.store.GetTraces(ctx, chstore.TraceFilter{
		Service: p.Service, HasError: true, From: from, To: to,
		Limit: 5, Sort: "time", Order: "desc", CountMode: "skip",
	}); terr == nil && len(traces) > 0 {
		b.WriteString("\n\nError-trace exemplars (this service, in window):")
		for _, t := range traces {
			b.WriteString(fmt.Sprintf("\n  %s — %.0fms, %d spans", t.RootName, t.DurationMs, t.SpanCount))
		}
	}

	// 3) Significant log patterns in the window. Unsupervised
	// significant_text is index-global (not service-scoped), so we
	// label it honestly — it still surfaces "what tokens spiked
	// during this problem's window" which the model can weigh.
	if s.logs != nil {
		if pats, lerr := s.logs.SignificantPatterns(ctx, from, from.Add(-6*time.Hour), to, 5); lerr == nil && len(pats) > 0 {
			b.WriteString("\n\nSignificant log patterns in the window (across services):")
			for _, pt := range pats {
				b.WriteString(fmt.Sprintf("\n  %q (%d in window vs %d baseline)", pt.Token, pt.DocCount, pt.BgCount))
			}
		}
	}
	return b.String()
}

// topNeighborNames renders the top-N neighbours by span volume as a
// compact "svc(N sp), …" line for the root-cause prompt.
func topNeighborNames(ns []chstore.NeighborStat, n int) string {
	sorted := append([]chstore.NeighborStat(nil), ns...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].SpanCount > sorted[j].SpanCount })
	parts := make([]string, 0, n)
	for i, x := range sorted {
		if i >= n {
			break
		}
		parts = append(parts, fmt.Sprintf("%s(%d sp)", x.Service, x.SpanCount))
	}
	return strings.Join(parts, ", ")
}

// copilotExplainIncident fetches an Incident (plus its attached
// problems for context) and asks the model for a SEV-grade
// triage summary: what's happening, blast radius, first three
// coordination actions, escalate-or-not call. Distinct from
// explain-problem because incidents bundle multiple firings —
// the LLM needs the wider scope to call escalation correctly.
func (s *Server) copilotExplainIncident(w http.ResponseWriter, r *http.Request) {
	if !s.copilot.Configured() {
		http.Error(w, "AI copilot not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	inc, err := s.store.GetIncident(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if inc == nil {
		http.Error(w, "incident not found", http.StatusNotFound)
		return
	}
	// Pull attached problems so the prompt carries the actual
	// signals that opened the incident, not just the title.
	// Two-stage lookup: IncidentProblems returns IDs only;
	// resolve full rows via a bounded ListProblems pass.
	// Capped at 8 to keep the prompt size reasonable.
	pIDs, _ := s.store.IncidentProblems(r.Context(), id)
	wantIDs := map[string]bool{}
	for i, pid := range pIDs {
		if i >= 8 {
			break
		}
		wantIDs[pid] = true
	}
	var probs []chstore.Problem
	if len(wantIDs) > 0 {
		all, _ := s.store.ListProblems(r.Context(), chstore.ProblemFilter{Limit: 2000})
		for i := range all {
			if wantIDs[all[i].ID] {
				probs = append(probs, all[i])
			}
		}
	}
	var probLines string
	for _, p := range probs {
		probLines += fmt.Sprintf(
			"  • [%s] %s — %s: value=%.2f threshold=%.2f\n",
			strings.ToUpper(p.Severity), p.Service, p.RuleName, p.Value, p.Threshold)
	}
	user := fmt.Sprintf(
		"Incident: %s\nService: %s\nSeverity: %s\nStatus: %s\nSummary: %s\nAttached problems (%d):\n%s",
		inc.Title, inc.Service, inc.Severity, inc.Status, inc.Summary, len(probs), probLines,
	)
	out, err := s.copilotExplain(r, copilot.SystemPromptIncident(), user)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"explanation": out})
}

// copilotExplainAnomaly handles log-pattern / trace-op anomaly
// rows. Different model prompt — anomalies are soft signals
// ("something changed") so the response should help an
// operator decide whether to act, not assume something is
// broken.
func (s *Server) copilotExplainAnomaly(w http.ResponseWriter, r *http.Request) {
	if !s.copilot.Configured() {
		http.Error(w, "AI copilot not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	// ListAnomalyEvents is bounded; for a single-id lookup we
	// scan the recent window. Anomalies have short retention
	// (30d) so the universe is small.
	events, err := s.store.ListAnomalyEvents(r.Context(), chstore.ListAnomalyEventsFilter{
		SinceNs: time.Now().Add(-30 * 24 * time.Hour).UnixNano(),
		Limit:   2000,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	var ev *chstore.AnomalyEvent
	for i := range events {
		if events[i].ID == id {
			ev = &events[i]
			break
		}
	}
	if ev == nil {
		http.Error(w, "anomaly not found", http.StatusNotFound)
		return
	}
	user := fmt.Sprintf(
		"Anomaly kind: %s\nPattern: %s\nService: %s\nPeak ratio: %.2fx baseline\nCurrent ratio: %.2fx\nCurrent count: %d\nSample: %s",
		ev.Kind, ev.Pattern, ev.Service,
		ev.PeakRatio, ev.CurrentRatio, ev.CurrentCount,
		truncate(ev.Sample, 600),
	)
	out, err := s.copilotExplain(r, copilot.SystemPromptAnomaly(), user)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"explanation": out})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// copilotExplainServiceHealth feeds the three RED series
// (RPS / error rate / P99 latency) for one service + the
// window's deploy markers + currently-open problems to the
// LLM and asks for a "is this healthy right now?" triage
// summary. Operator hits this when staring at a chart and
// wants a sanity-check — distinct from explain-problem
// (which fires on a specific alert) because the chart may
// look fine and the answer should say so plainly.
func (s *Server) copilotExplainServiceHealth(w http.ResponseWriter, r *http.Request) {
	if !s.copilot.Configured() {
		http.Error(w, "AI copilot not configured", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	service := strings.TrimSpace(q.Get("service"))
	if service == "" {
		http.Error(w, `{"error":"service required"}`, http.StatusBadRequest)
		return
	}
	from, to := parseFromTo(r, time.Hour)
	// Service-name predicate as a structured filter — the
	// SpanMetricFilter API takes []FilterExpr, not a DSL
	// string. One filter chip is all we need here.
	svcFilter := []chstore.FilterExpr{{Key: "service.name", Op: "=", Values: []string{service}}}
	// Three parallel queries, same shape the SPA fires for
	// the live chart. Bounded by the chosen window; the MV
	// fast path covers any range ≥ 5min.
	rps, _ := s.store.QuerySpanMetric(r.Context(), chstore.SpanMetricFilter{
		Aggregation: "rate", Filters: svcFilter, From: from, To: to, GroupBy: []string{"name"},
	})
	errs, _ := s.store.QuerySpanMetric(r.Context(), chstore.SpanMetricFilter{
		Aggregation: "error_rate", Filters: svcFilter, From: from, To: to, GroupBy: []string{"name"},
	})
	p99, _ := s.store.QuerySpanMetric(r.Context(), chstore.SpanMetricFilter{
		Aggregation: "p99", Field: "duration_ms", Filters: svcFilter, From: from, To: to, GroupBy: []string{"name"},
	})

	// Compact the series into a small JSON blob the LLM can
	// reason about — full point lists are token-heavy and
	// the model only needs the shape (min / max / current /
	// trend) per operation. We pick the top-3 operations by
	// rate so a noisy 50-operation service doesn't dominate
	// the prompt.
	type seriesSummary struct {
		Name    string  `json:"name"`
		Min     float64 `json:"min"`
		Max     float64 `json:"max"`
		Avg     float64 `json:"avg"`
		Current float64 `json:"current"`
	}
	summarize := func(rows []chstore.SpanMetricSeries, topN int) []seriesSummary {
		type bag struct {
			name string
			sum  float64
			cnt  int
			mn   float64
			mx   float64
			last float64
		}
		bags := []bag{}
		for _, s := range rows {
			b := bag{name: strings.Join(s.GroupKey, " / "), mn: 1e300}
			for _, p := range s.Points {
				b.sum += p.Value
				b.cnt++
				if p.Value < b.mn {
					b.mn = p.Value
				}
				if p.Value > b.mx {
					b.mx = p.Value
				}
				b.last = p.Value
			}
			if b.cnt > 0 {
				bags = append(bags, b)
			}
		}
		// Sort by sum desc, top N.
		for i := 0; i < len(bags); i++ {
			for j := i + 1; j < len(bags); j++ {
				if bags[j].sum > bags[i].sum {
					bags[i], bags[j] = bags[j], bags[i]
				}
			}
		}
		if len(bags) > topN {
			bags = bags[:topN]
		}
		out := make([]seriesSummary, 0, len(bags))
		for _, b := range bags {
			avg := 0.0
			if b.cnt > 0 {
				avg = b.sum / float64(b.cnt)
			}
			out = append(out, seriesSummary{
				Name: b.name, Min: b.mn, Max: b.mx, Avg: avg, Current: b.last,
			})
		}
		return out
	}

	// Active problems for context.
	probs, _ := s.store.ListProblems(r.Context(), chstore.ProblemFilter{
		Service: service, Status: "open", Limit: 20,
	})
	var probLines string
	for _, p := range probs {
		probLines += fmt.Sprintf("  • [%s] %s — %s: value=%.2f threshold=%.2f\n",
			strings.ToUpper(p.Severity), p.RuleName, p.Metric, p.Value, p.Threshold)
	}

	payload, _ := json.Marshal(map[string]any{
		"service":              service,
		"window":               fmt.Sprintf("%s → %s", from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339)),
		"rpsByOperation":       summarize(rps, 3),
		"errorRateByOperation": summarize(errs, 3),
		"p99MsByOperation":     summarize(p99, 3),
		"openProblems":         len(probs),
	})
	user := fmt.Sprintf("Service health snapshot:\n%s\n%s",
		string(payload),
		func() string {
			if probLines == "" {
				return ""
			}
			return "Active problems:\n" + probLines
		}(),
	)
	out, err := s.copilotExplain(r, copilot.SystemPromptServiceHealth(), user)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"explanation": out})
}

// copilotRunbook generates a numbered, actionable runbook for
// an open Problem, anchored in past resolved instances of the
// same rule on the same service. Distinct from
// explain-problem — explain gives context bullets, runbook
// gives an executable checklist. The past-resolutions context
// (time-to-resolve, value at fire) lets the model bias step
// order: if similar issues consistently resolved in <5 min,
// lead with the fastest path that worked before; if they
// dragged past 30 min, surface escalation up front.
//
// Caps at 8 past instances to keep the prompt bounded — the
// most-recent 8 carry the freshest pattern.
func (s *Server) copilotRunbook(w http.ResponseWriter, r *http.Request) {
	if !s.copilot.Configured() {
		http.Error(w, "AI copilot not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	probs, err := s.store.ListProblems(r.Context(), chstore.ProblemFilter{Limit: 1000})
	if err != nil {
		writeErr(w, err)
		return
	}
	var p *chstore.Problem
	for i := range probs {
		if probs[i].ID == id {
			p = &probs[i]
			break
		}
	}
	if p == nil {
		http.Error(w, "problem not found", http.StatusNotFound)
		return
	}
	similar, _ := s.store.FindSimilarResolvedProblems(r.Context(), p.Service, p.RuleID, 8)
	// Deploy correlation — same enrichment the problem list
	// shows, threaded into the runbook prompt so the model
	// can lead with "rollback the recent deploy" when the
	// timing is suspicious.
	enriched := s.store.EnrichProblemsWithDeploys(r.Context(),
		[]chstore.Problem{*p}, 30*time.Minute)
	p = &enriched[0]

	var sb strings.Builder
	fmt.Fprintf(&sb, "Current problem (status %s):\n", p.Status)
	fmt.Fprintf(&sb,
		"  Service: %s\n  Metric: %s\n  Value: %.2f (threshold %.2f)\n  Severity: %s\n  Rule: %s\n  Description: %s\n",
		p.Service, p.Metric, p.Value, p.Threshold, p.Severity, p.RuleName, p.Description)
	if p.RecentDeploy != nil {
		fmt.Fprintf(&sb,
			"\nRecent deploy: service.version=%q first seen %d seconds before this problem opened — strong signal that step 1 should be \"check / roll back this deploy\".\n",
			p.RecentDeploy.Version, p.RecentDeploy.AgeSeconds)
	}

	if len(similar) > 0 {
		fmt.Fprintf(&sb, "\nPast resolved instances of this rule on this service (%d, most recent first):\n", len(similar))
		var totalTTR time.Duration
		var ttrSeen int
		for _, sp := range similar {
			ttr := "unknown"
			if sp.ResolvedAt != nil {
				d := time.Unix(0, *sp.ResolvedAt).Sub(time.Unix(0, sp.StartedAt))
				ttr = d.Round(time.Minute).String()
				totalTTR += d
				ttrSeen++
			}
			fmt.Fprintf(&sb,
				"  • opened %s — peak value %.2f (sev %s) — resolved in %s\n",
				time.Unix(0, sp.StartedAt).Format("2006-01-02 15:04"),
				sp.Value, sp.Severity, ttr,
			)
		}
		if ttrSeen > 0 {
			avg := totalTTR / time.Duration(ttrSeen)
			fmt.Fprintf(&sb,
				"\nAverage time-to-resolve across past instances: %s\n",
				avg.Round(time.Minute))
		}
	} else {
		sb.WriteString("\nNo past resolved instances of this exact rule on this service — use first-principles reasoning grounded in the metric + service.\n")
	}

	out, err := s.copilotExplain(r, copilot.SystemPromptRunbook(), sb.String())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"explanation":  out,
		"similarCount": len(similar),
	})
}

// copilotCompareTraces takes two trace IDs, computes a
// structured diff (root summaries, per-shared-operation
// latency delta, services-only-in-one, error footprint), and
// asks the model to explain WHY the two diverged. Tailored
// for the typical incident workflow "today's slow trace vs
// yesterday's fast one" — the model surfaces the single
// biggest contributor without re-narrating the raw diff.
//
// Failure modes:
//   • Either trace not found → 404 with the missing id.
//   • Both traces identical (same ID typed twice) → still
//     valid; the model will say "essentially the same".
func (s *Server) copilotCompareTraces(w http.ResponseWriter, r *http.Request) {
	if !s.copilot.Configured() {
		http.Error(w, "AI copilot not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		AID string `json:"aId"`
		BID string `json:"bId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	aID := strings.TrimSpace(strings.ToLower(body.AID))
	bID := strings.TrimSpace(strings.ToLower(body.BID))
	if aID == "" || bID == "" {
		http.Error(w, "both aId and bId are required", http.StatusBadRequest)
		return
	}

	aSpans, err := s.store.GetTrace(r.Context(), aID)
	if err != nil || len(aSpans) == 0 {
		http.Error(w, "trace A not found: "+aID, http.StatusNotFound)
		return
	}
	bSpans, err := s.store.GetTrace(r.Context(), bID)
	if err != nil || len(bSpans) == 0 {
		http.Error(w, "trace B not found: "+bID, http.StatusNotFound)
		return
	}

	user := buildCompareTracesPrompt(aID, aSpans, bID, bSpans)
	out, err := s.copilotExplain(r,
		copilot.SystemPromptCompareTraces(), user)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"explanation": out})
}

// copilotDeployImpact compares before/after windows around a
// specific service.version transition and asks the model
// whether the deploy was clean, degraded a signal, or
// introduced a regression. Drives the "Explain latest deploy"
// button on the Service detail page — the post-deploy
// "should I walk away?" check that operators kept running
// manually by eyeballing the RED chart shoulders.
//
// Body:
//
//	{ "service": "...", "version": "v1.2.3",
//	  "deployTimeNs": <unix ns>,
//	  "windowSec":    <seconds; default 600> }
//
// Returns { explanation, before, after }. Frontend renders
// the explanation as the headline and the raw before/after
// numbers as a small chip row so the operator can
// fact-check the model.
func (s *Server) copilotDeployImpact(w http.ResponseWriter, r *http.Request) {
	if !s.copilot.Configured() {
		http.Error(w, "AI copilot not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Service      string `json:"service"`
		Version      string `json:"version"`
		DeployTimeNs int64  `json:"deployTimeNs"`
		WindowSec    int    `json:"windowSec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Service == "" || body.DeployTimeNs == 0 {
		http.Error(w, "service and deployTimeNs are required", http.StatusBadRequest)
		return
	}
	if body.WindowSec <= 0 {
		body.WindowSec = 600 // 10 min default — covers a typical canary bake
	}
	if body.WindowSec > 6*3600 {
		body.WindowSec = 6 * 3600
	}

	deployT := time.Unix(0, body.DeployTimeNs)
	beforeStart := deployT.Add(-time.Duration(body.WindowSec) * time.Second)
	afterEnd := deployT.Add(time.Duration(body.WindowSec) * time.Second)

	type stats struct {
		Count      uint64  `json:"count"`
		RPS        float64 `json:"rps"`
		ErrorRate  float64 `json:"errorRate"`
		P99Ms      float64 `json:"p99Ms"`
		AvgMs      float64 `json:"avgMs"`
	}
	// One CH pass with quantileIf gates so we get before +
	// after side-by-side without two scans.
	row := s.store.Conn().QueryRow(r.Context(), `
		SELECT
		  countIf(time < ?)                                        AS bef_count,
		  countIf(time >= ?)                                       AS aft_count,
		  countIf(time < ?  AND status_code = 'error')             AS bef_err,
		  countIf(time >= ? AND status_code = 'error')             AS aft_err,
		  quantileIf(0.99)(duration, time < ?)  / 1e6              AS bef_p99,
		  quantileIf(0.99)(duration, time >= ?) / 1e6              AS aft_p99,
		  avgIf(duration,            time < ?)  / 1e6              AS bef_avg,
		  avgIf(duration,            time >= ?) / 1e6              AS aft_avg
		FROM spans
		WHERE service_name = ? AND time >= ? AND time <= ?
		SETTINGS max_execution_time = 15,
		         optimize_skip_unused_shards = 1`,
		deployT, deployT,
		deployT, deployT,
		deployT, deployT,
		deployT, deployT,
		body.Service, beforeStart, afterEnd)

	var befCount, aftCount, befErr, aftErr uint64
	var befP99, aftP99, befAvg, aftAvg float64
	if err := row.Scan(&befCount, &aftCount, &befErr, &aftErr,
		&befP99, &aftP99, &befAvg, &aftAvg); err != nil {
		writeErr(w, err)
		return
	}
	mkStats := func(c, e uint64, p99, avg float64) stats {
		out := stats{
			Count: c,
			RPS:   float64(c) / float64(body.WindowSec),
			P99Ms: p99,
			AvgMs: avg,
		}
		if c > 0 {
			out.ErrorRate = float64(e) / float64(c) * 100
		}
		return out
	}
	before := mkStats(befCount, befErr, befP99, befAvg)
	after := mkStats(aftCount, aftErr, aftP99, aftAvg)

	// New operations — the set that appeared in `after` but
	// not in `before`. Capped at 10 for the prompt; the chip
	// row also shows the count.
	opsRows, err := s.store.Conn().Query(r.Context(), `
		WITH
		  groupArrayIf(name, time < ? AND name != '')   AS bef_ops,
		  groupArrayIf(name, time >= ? AND name != '')  AS aft_ops
		SELECT arrayDistinct(arrayFilter(x -> NOT has(bef_ops, x), aft_ops)) AS new_ops
		FROM spans
		WHERE service_name = ? AND time >= ? AND time <= ?
		SETTINGS max_execution_time = 10,
		         optimize_skip_unused_shards = 1`,
		deployT, deployT, body.Service, beforeStart, afterEnd)
	var newOps []string
	if err == nil {
		defer opsRows.Close()
		if opsRows.Next() {
			_ = opsRows.Scan(&newOps)
		}
	}
	if len(newOps) > 10 {
		newOps = newOps[:10]
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Service: %s\n", body.Service)
	if body.Version != "" {
		fmt.Fprintf(&sb, "Deploy version: %s\n", body.Version)
	}
	fmt.Fprintf(&sb, "Deploy time: %s (windows ±%ds)\n\n",
		deployT.UTC().Format(time.RFC3339), body.WindowSec)
	fmt.Fprintf(&sb,
		"Before: %d spans @ %.2f rps, %.2f%% errors, p99 %.0fms, avg %.0fms\n",
		before.Count, before.RPS, before.ErrorRate, before.P99Ms, before.AvgMs)
	fmt.Fprintf(&sb,
		"After:  %d spans @ %.2f rps, %.2f%% errors, p99 %.0fms, avg %.0fms\n",
		after.Count, after.RPS, after.ErrorRate, after.P99Ms, after.AvgMs)
	if before.P99Ms > 0 {
		fmt.Fprintf(&sb, "P99 delta: %+.0fms (%+.1f%%)\n",
			after.P99Ms-before.P99Ms,
			(after.P99Ms-before.P99Ms)/before.P99Ms*100)
	}
	if before.ErrorRate > 0 || after.ErrorRate > 0 {
		fmt.Fprintf(&sb, "Error-rate delta: %+.2f pp\n",
			after.ErrorRate-before.ErrorRate)
	}
	if len(newOps) > 0 {
		sb.WriteString("New operations (after only):\n")
		for _, op := range newOps {
			fmt.Fprintf(&sb, "  • %s\n", op)
		}
	}

	out, err := s.copilotExplain(r,
		copilot.SystemPromptDeployImpact(), sb.String())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"explanation": out,
		"before":      before,
		"after":       after,
		"newOps":      newOps,
	})
}

// copilotExplainSLO drives the "Explain burn" button on the
// /slos page. Fetches the SLO definition + current SLI status
// + fast/slow burn-rate samples (5 min and 1 hr — the
// Google SRE Workbook multi-burn-rate windows) and asks the
// model whether the budget is on track, burning fast, or
// already breached.
func (s *Server) copilotExplainSLO(w http.ResponseWriter, r *http.Request) {
	if !s.copilot.Configured() {
		http.Error(w, "AI copilot not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	slo, err := s.store.GetSLO(r.Context(), id)
	if err != nil || slo == nil {
		http.Error(w, "SLO not found", http.StatusNotFound)
		return
	}
	status, _ := s.store.ComputeSLOStatus(r.Context(), *slo)
	// Two-window burn samples — Workbook multi-burn-rate
	// pattern. Fast window catches sudden cliff drops; slow
	// window catches steady drift that wouldn't trip the fast
	// alarm but eats the budget over hours.
	fastRate, fastTotal, _ := s.store.ComputeSLOBurnRate(r.Context(), *slo, 5*time.Minute)
	slowRate, slowTotal, _ := s.store.ComputeSLOBurnRate(r.Context(), *slo, 1*time.Hour)

	var sb strings.Builder
	fmt.Fprintf(&sb, "SLO: %s (service=%s)\n", slo.Name, slo.Service)
	switch slo.SLIType {
	case "latency":
		fmt.Fprintf(&sb, "Type: latency, threshold=%.0fms\n", slo.ThresholdMs)
	default:
		fmt.Fprintf(&sb, "Type: %s\n", slo.SLIType)
	}
	fmt.Fprintf(&sb, "Target: %.3f%% over %d-day rolling window\n",
		slo.Target*100, slo.WindowDays)
	if slo.Operation != "" {
		fmt.Fprintf(&sb, "Scope: operation=%q\n", slo.Operation)
	}
	if status != nil {
		fmt.Fprintf(&sb,
			"Current: SLI=%.3f%% (%d good / %d total) · budget remaining=%.2f%% · long-window burn=%.2f · healthy=%v\n",
			status.SLI*100, status.Good, status.Total,
			status.BudgetRemaining*100, status.BurnRate, status.Healthy)
	}
	fmt.Fprintf(&sb, "Fast burn (5 min): rate=%.2f, n=%d\n", fastRate, fastTotal)
	fmt.Fprintf(&sb, "Slow burn (1 hr):  rate=%.2f, n=%d\n", slowRate, slowTotal)

	out, err := s.copilotExplain(r,
		copilot.SystemPromptSLOBurn(), sb.String())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"explanation": out,
		"status":      status,
		"fastBurn":    fastRate,
		"slowBurn":    slowRate,
	})
}

// copilotExplainSlowQuery drives the "✨ Explain" button on a
// row in the /databases/slow-queries catalog (v0.5.171). Takes
// the normalised statement + a real sample + DB engine + the
// aggregate stats and asks the model for the most likely
// performance hazard + one concrete remediation.
//
// The body is operator-supplied (frontend already has all this
// data on the row) so the handler is a thin shaper — no
// re-query of the spans table needed.
func (s *Server) copilotExplainSlowQuery(w http.ResponseWriter, r *http.Request) {
	if !s.copilot.Configured() {
		http.Error(w, "AI copilot not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Service         string  `json:"service"`
		Statement       string  `json:"statement"`
		SampleStatement string  `json:"sampleStatement"`
		DBSystem        string  `json:"dbSystem"`
		Count           int     `json:"count"`
		AvgMs           float64 `json:"avgMs"`
		P95Ms           float64 `json:"p95Ms"`
		P99Ms           float64 `json:"p99Ms"`
		MaxMs           float64 `json:"maxMs"`
		ErrorCount      int     `json:"errorCount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Statement) == "" {
		http.Error(w, "statement required", http.StatusBadRequest)
		return
	}
	// Cap submitted SQL — keeps the prompt cheap and avoids
	// blowing up the ai_calls row when an operator triggers
	// Explain on a 30KB ORM-emitted blob.
	cap := func(s string, n int) string {
		if len(s) > n {
			return s[:n] + "… [truncated]"
		}
		return s
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Service: %s\n", body.Service)
	fmt.Fprintf(&sb, "DB engine: %s\n", body.DBSystem)
	fmt.Fprintf(&sb, "Calls in window: %d\n", body.Count)
	if body.ErrorCount > 0 {
		fmt.Fprintf(&sb, "Errors: %d (%.1f%%)\n", body.ErrorCount,
			float64(body.ErrorCount)*100/float64(maxInt(1, body.Count)))
	}
	fmt.Fprintf(&sb, "Latency: avg=%.1fms · p95=%.0fms · p99=%.0fms · max=%.0fms\n",
		body.AvgMs, body.P95Ms, body.P99Ms, body.MaxMs)
	totalSec := body.AvgMs * float64(body.Count) / 1000
	fmt.Fprintf(&sb, "Total wall-clock time spent in this query class: %.1fs\n", totalSec)
	sb.WriteString("\nNormalized statement (literals replaced with ?):\n")
	sb.WriteString(cap(body.Statement, 4000))
	if body.SampleStatement != "" && body.SampleStatement != body.Statement {
		sb.WriteString("\n\nReal sample with literals:\n")
		sb.WriteString(cap(body.SampleStatement, 4000))
	}

	out, err := s.copilotExplain(r, copilot.SystemPromptSlowQuery(), sb.String())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"explanation": out})
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// buildCompareTracesPrompt produces the structured-diff user
// prompt the model consumes. Kept in api.go (not copilot/) so
// the spans-table shape (SpanRow fields) doesn't leak into
// the copilot package's dependencies.
func buildCompareTracesPrompt(aID string, aSpans []chstore.SpanRow,
	bID string, bSpans []chstore.SpanRow) string {

	summarise := func(traceID string, spans []chstore.SpanRow) (svc, op string, durMs float64, errCount int) {
		minT := int64(0)
		maxT := int64(0)
		for i, sp := range spans {
			if i == 0 || sp.StartTime < minT {
				minT = sp.StartTime
			}
			if sp.EndTime > maxT {
				maxT = sp.EndTime
			}
			if sp.StatusCode == "error" {
				errCount++
			}
			if sp.ParentSpanID == "" || sp.ParentSpanID == "0000000000000000" {
				svc = sp.ServiceName
				op = sp.Name
			}
		}
		durMs = float64(maxT-minT) / 1e6
		_ = traceID
		return
	}

	// Per-(service, op) summary across each trace — used for
	// the latency-delta ranking AND the "only in A / only in
	// B" diff.
	type key struct{ svc, op string }
	type bucket struct {
		count    int
		totalMs  float64
		maxMs    float64
		hasError bool
	}
	bucketsOf := func(spans []chstore.SpanRow) map[key]*bucket {
		out := map[key]*bucket{}
		for _, sp := range spans {
			k := key{sp.ServiceName, sp.Name}
			b := out[k]
			if b == nil {
				b = &bucket{}
				out[k] = b
			}
			b.count++
			d := float64(sp.EndTime-sp.StartTime) / 1e6
			b.totalMs += d
			if d > b.maxMs {
				b.maxMs = d
			}
			if sp.StatusCode == "error" {
				b.hasError = true
			}
		}
		return out
	}
	aB := bucketsOf(aSpans)
	bB := bucketsOf(bSpans)

	type delta struct {
		k       key
		aMs, bMs float64
		dMs     float64
	}
	var deltas []delta
	for k, ab := range aB {
		if bb, ok := bB[k]; ok {
			deltas = append(deltas, delta{k, ab.totalMs, bb.totalMs, bb.totalMs - ab.totalMs})
		}
	}
	sort.Slice(deltas, func(i, j int) bool {
		return abs(deltas[i].dMs) > abs(deltas[j].dMs)
	})
	if len(deltas) > 10 {
		deltas = deltas[:10]
	}

	// Only-in-A and only-in-B — the structural diff, not just
	// timing. Cap each list at 10 to keep the prompt bounded.
	var onlyA, onlyB []string
	for k, ab := range aB {
		if _, in := bB[k]; !in {
			onlyA = append(onlyA, fmt.Sprintf("%s · %s (count=%d, totalMs=%.0f)",
				k.svc, k.op, ab.count, ab.totalMs))
		}
	}
	for k, bb := range bB {
		if _, in := aB[k]; !in {
			onlyB = append(onlyB, fmt.Sprintf("%s · %s (count=%d, totalMs=%.0f)",
				k.svc, k.op, bb.count, bb.totalMs))
		}
	}
	if len(onlyA) > 10 {
		onlyA = onlyA[:10]
	}
	if len(onlyB) > 10 {
		onlyB = onlyB[:10]
	}

	aSvc, aOp, aDur, aErr := summarise(aID, aSpans)
	bSvc, bOp, bDur, bErr := summarise(bID, bSpans)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Trace A: id=%s root=%s/%s spans=%d duration=%.0fms errors=%d\n",
		aID, aSvc, aOp, len(aSpans), aDur, aErr)
	fmt.Fprintf(&sb, "Trace B: id=%s root=%s/%s spans=%d duration=%.0fms errors=%d\n",
		bID, bSvc, bOp, len(bSpans), bDur, bErr)
	if aDur > 0 {
		pct := (bDur - aDur) / aDur * 100
		fmt.Fprintf(&sb, "Wall-clock delta: B vs A = %+.0fms (%+.1f%%)\n", bDur-aDur, pct)
	}

	if len(deltas) > 0 {
		sb.WriteString("\nTop operations by absolute latency delta (B - A):\n")
		for _, d := range deltas {
			fmt.Fprintf(&sb, "  • %s · %s — A=%.0fms B=%.0fms (Δ %+.0fms)\n",
				d.k.svc, d.k.op, d.aMs, d.bMs, d.dMs)
		}
	}
	if len(onlyA) > 0 {
		sb.WriteString("\nOnly in trace A:\n")
		for _, s := range onlyA {
			fmt.Fprintf(&sb, "  • %s\n", s)
		}
	}
	if len(onlyB) > 0 {
		sb.WriteString("\nOnly in trace B:\n")
		for _, s := range onlyB {
			fmt.Fprintf(&sb, "  • %s\n", s)
		}
	}
	return sb.String()
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// copilotSuggestServiceTags assembles a compact fingerprint of
// one service (runtime, top operations, downstream callees,
// cluster footprint, recent deploys) and asks the model to
// propose catalog entries — owner team, SRE team, one-line
// description, criticality. Result is a single JSON object the
// AdminCatalog UI can pre-fill into the edit form before the
// operator saves.
//
// Editor-role gated (same as PUT /api/services/{name}/metadata)
// — non-editors can read the catalog but can't apply
// suggestions, so they don't need to call this endpoint.
//
// Failure modes:
//   • Service has no recent spans → fingerprint is sparse but
//     the model still produces a sensible "low confidence"
//     guess from the name alone.
//   • Model returns prose instead of JSON → we surface a
//     "no suggestions available" rather than poisoning the
//     form with non-JSON garbage.
func (s *Server) copilotSuggestServiceTags(w http.ResponseWriter, r *http.Request) {
	if !s.copilot.Configured() {
		http.Error(w, "AI copilot not configured", http.StatusServiceUnavailable)
		return
	}
	service := strings.TrimSpace(r.URL.Query().Get("service"))
	if service == "" {
		http.Error(w, "service required", http.StatusBadRequest)
		return
	}

	// Pull fingerprint signals in parallel — none of them depend
	// on each other and each is one bounded CH query. Soft-fail
	// per signal so a CH blip on (say) the deploys query doesn't
	// erase the whole prompt.
	type sig struct {
		runtime  *chstore.ServiceRuntime
		clusters []string
		callees  []chstore.ServiceEdgeStats
		deploys  []chstore.Deploy
		ops      []string
	}
	var got sig
	since := 24 * time.Hour
	got.runtime, _ = s.store.GetServiceRuntime(r.Context(), service)
	if m, err := s.store.GetServiceClusterMap(r.Context(), since); err == nil {
		got.clusters = m[service]
	}
	got.callees, _ = s.store.CalleesOf(r.Context(), service, since)
	if len(got.callees) > 5 {
		got.callees = got.callees[:5]
	}
	got.deploys, _ = s.store.GetServiceDeploys(r.Context(),
		service, time.Now().Add(-since), time.Now())
	if len(got.deploys) > 3 {
		got.deploys = got.deploys[:3]
	}
	// Top span operations — direct CH read, cheap because
	// service_name prefixes the spans primary key.
	if rows, err := s.store.Conn().Query(r.Context(), `
		SELECT name, count() AS c
		FROM spans
		WHERE service_name = ? AND time >= now() - INTERVAL 24 HOUR
		GROUP BY name ORDER BY c DESC LIMIT 10
		SETTINGS max_execution_time = 5`, service); err == nil {
		defer rows.Close()
		for rows.Next() {
			var name string
			var c uint64
			if err := rows.Scan(&name, &c); err == nil && name != "" {
				got.ops = append(got.ops, name)
			}
		}
	}

	// Build the user prompt — compact JSON-ish so the model
	// sees structured signals without token bloat.
	var sb strings.Builder
	fmt.Fprintf(&sb, "service: %q\n", service)
	if got.runtime != nil {
		fmt.Fprintf(&sb, "runtime: language=%q sdk=%q runtime=%q os=%q host=%q\n",
			got.runtime.Language, got.runtime.SDKVersion,
			got.runtime.RuntimeName, got.runtime.OS, got.runtime.Host)
	}
	if len(got.clusters) > 0 {
		fmt.Fprintf(&sb, "clusters: %v\n", got.clusters)
	}
	if len(got.ops) > 0 {
		fmt.Fprintf(&sb, "top_operations (24h): %v\n", got.ops)
	}
	if len(got.callees) > 0 {
		sb.WriteString("downstream_callees:\n")
		for _, c := range got.callees {
			fmt.Fprintf(&sb, "  - %s (calls=%d err=%.1f%% p99=%.0fms)\n",
				c.Service, c.Calls, c.ErrorRate, c.P99Ms)
		}
	}
	if len(got.deploys) > 0 {
		sb.WriteString("recent_versions: ")
		for i, d := range got.deploys {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(d.Version)
		}
		sb.WriteString("\n")
	}

	out, err := s.copilotExplain(r,
		copilot.SystemPromptServiceTags(), sb.String())
	if err != nil {
		writeErr(w, err)
		return
	}
	// Parse the model's JSON envelope. Find the first { and
	// last } — defensive against the model adding markdown
	// fences despite the "NO preamble" instruction.
	parsed := extractServiceTagsJSON(out)
	if parsed == nil {
		writeJSON(w, map[string]any{
			"suggestions": nil,
			"raw":         out,
			"note":        "Model didn't return a JSON object — review the raw text manually.",
		})
		return
	}
	writeJSON(w, map[string]any{"suggestions": parsed})
}

// extractServiceTagsJSON pulls the first {...} block out of a
// model reply and decodes it as a free-shape map. Tolerates
// markdown ``` fences and stray trailing prose. nil when the
// reply has no recognisable JSON object.
func extractServiceTagsJSON(s string) map[string]any {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s[start:end+1]), &m); err != nil {
		return nil
	}
	return m
}

// ── Incident management ─────────────────────────────────────────────────────

func (s *Server) listIncidents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, err := s.store.ListIncidents(r.Context(), chstore.IncidentFilter{
		Status:   q.Get("status"),
		Service:  q.Get("service"),
		Severity: q.Get("severity"),
		Limit:    parseInt(q.Get("limit"), 200),
	})
	if err != nil { writeErr(w, err); return }
	rows = s.store.EnrichIncidentsWithClusters(r.Context(), rows, time.Hour)
	writeJSON(w, rows)
}

func (s *Server) getIncident(w http.ResponseWriter, r *http.Request) {
	inc, err := s.store.GetIncident(r.Context(), r.PathValue("id"))
	if err != nil { writeErr(w, err); return }
	if inc == nil { http.Error(w, "not found", http.StatusNotFound); return }
	writeJSON(w, inc)
}

func (s *Server) createIncident(w http.ResponseWriter, r *http.Request) {
	var inc chstore.Incident
	if err := json.NewDecoder(r.Body).Decode(&inc); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest); return
	}
	if inc.Title == "" {
		http.Error(w, "title required", http.StatusBadRequest); return
	}
	inc.ID = ""
	if err := s.store.UpsertIncident(r.Context(), &inc); err != nil {
		writeErr(w, err); return
	}
	actor := actorOf(r)
	_ = s.store.AppendIncidentEvent(r.Context(), chstore.IncidentEvent{
		IncidentID: inc.ID, Kind: "created", Actor: actor,
		Body: "Manually created",
	})
	writeJSON(w, inc)
}

func (s *Server) updateIncident(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var inc chstore.Incident
	if err := json.NewDecoder(r.Body).Decode(&inc); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest); return
	}
	inc.ID = id
	if err := s.store.UpsertIncident(r.Context(), &inc); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, inc)
}

func (s *Server) ackIncident(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inc, err := s.store.GetIncident(r.Context(), id)
	if err != nil { writeErr(w, err); return }
	if inc == nil { http.Error(w, "not found", http.StatusNotFound); return }
	now := time.Now().UnixNano()
	inc.Status = "acknowledged"
	inc.AckAt = &now
	actor := actorOf(r)
	if inc.Assignee == "" {
		inc.Assignee = actor
	}
	if err := s.store.UpsertIncident(r.Context(), inc); err != nil {
		writeErr(w, err); return
	}
	_ = s.store.AppendIncidentEvent(r.Context(), chstore.IncidentEvent{
		IncidentID: id, Kind: "ack", Actor: actor, Body: "Incident acknowledged",
	})
	writeJSON(w, inc)
}

func (s *Server) resolveIncident(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inc, err := s.store.GetIncident(r.Context(), id)
	if err != nil { writeErr(w, err); return }
	if inc == nil { http.Error(w, "not found", http.StatusNotFound); return }
	now := time.Now().UnixNano()
	inc.Status = "resolved"
	inc.ResolvedAt = &now
	if err := s.store.UpsertIncident(r.Context(), inc); err != nil {
		writeErr(w, err); return
	}
	_ = s.store.AppendIncidentEvent(r.Context(), chstore.IncidentEvent{
		IncidentID: id, Kind: "resolved", Actor: actorOf(r), Body: "Incident resolved",
	})
	writeJSON(w, inc)
}

func (s *Server) addIncidentNote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct{ Text string `json:"text"` }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest); return
	}
	if body.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest); return
	}
	if err := s.store.AppendIncidentEvent(r.Context(), chstore.IncidentEvent{
		IncidentID: id, Kind: "note", Actor: actorOf(r), Body: body.Text,
	}); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) incidentTimeline(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.IncidentTimeline(r.Context(), r.PathValue("id"))
	if err != nil { writeErr(w, err); return }
	writeJSON(w, rows)
}

func (s *Server) incidentProblems(w http.ResponseWriter, r *http.Request) {
	ids, err := s.store.IncidentProblems(r.Context(), r.PathValue("id"))
	if err != nil { writeErr(w, err); return }
	writeJSON(w, ids)
}

// actorOf returns the email of the authenticated user for audit
// fields, or "anonymous" when the call somehow reached a handler
// without auth context (shouldn't happen for the /api/incidents/*
// admin-gated routes, but safe default).
func actorOf(r *http.Request) string {
	if c := auth.FromContext(r.Context()); c != nil && c.Email != "" {
		return c.Email
	}
	return "anonymous"
}

// ── Synthetic monitors ──────────────────────────────────────────────────────

func (s *Server) listMonitors(w http.ResponseWriter, r *http.Request) {
	monitors, err := s.store.ListMonitors(r.Context())
	if err != nil { writeErr(w, err); return }
	last, err := s.store.LastMonitorStatus(r.Context())
	if err != nil { writeErr(w, err); return }
	// Single rollup query for uptime % + avg latency over 1h/24h —
	// cheaper than the alternative (browser fetches 500-row
	// timelines per monitor and computes client-side). Empty map on
	// error so the list keeps rendering.
	stats, err := s.store.MonitorStatsAll(r.Context())
	if err != nil {
		log.Printf("[api] monitor stats: %v", err)
		stats = map[string]chstore.MonitorStats{}
	}
	// Combine definition + last status + 1h/24h rollups in the
	// response so the list page renders without a per-row roundtrip.
	type row struct {
		chstore.Monitor
		LastResult *chstore.MonitorResult `json:"lastResult,omitempty"`
		Stats      *chstore.MonitorStats  `json:"stats,omitempty"`
	}
	out := make([]row, 0, len(monitors))
	for _, m := range monitors {
		r := row{Monitor: m}
		if lr, ok := last[m.ID]; ok {
			r.LastResult = &lr
		}
		if st, ok := stats[m.ID]; ok {
			r.Stats = &st
		}
		out = append(out, r)
	}
	writeJSON(w, out)
}

func (s *Server) getMonitor(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.GetMonitor(r.Context(), r.PathValue("id"))
	if err != nil { writeErr(w, err); return }
	if m == nil { http.Error(w, "not found", http.StatusNotFound); return }
	writeJSON(w, m)
}

func (s *Server) createMonitor(w http.ResponseWriter, r *http.Request) {
	var m chstore.Monitor
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest); return
	}
	if m.Name == "" { http.Error(w, "name required", http.StatusBadRequest); return }
	if m.Type != "http" && m.Type != "heartbeat" {
		http.Error(w, "type must be http or heartbeat", http.StatusBadRequest); return
	}
	if m.Type == "http" && m.URL == "" {
		http.Error(w, "url required for http monitor", http.StatusBadRequest); return
	}
	m.ID = "" // force new ID
	if err := s.store.UpsertMonitor(r.Context(), &m); err != nil {
		writeErr(w, err); return
	}
	// UpsertMonitor stamped the new id + heartbeat token onto m;
	// echo it back directly. Re-reading via FINAL would race the
	// MergeTree merge cycle and sometimes return null.
	writeJSON(w, m)
}

func (s *Server) updateMonitor(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var m chstore.Monitor
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest); return
	}
	m.ID = id
	if err := s.store.UpsertMonitor(r.Context(), &m); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, m)
}

func (s *Server) deleteMonitor(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteMonitor(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

func (s *Server) monitorTimeline(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit := parseInt(r.URL.Query().Get("limit"), 500)
	rows, err := s.store.MonitorTimeline(r.Context(), id, limit)
	if err != nil { writeErr(w, err); return }
	writeJSON(w, rows)
}

// acceptHeartbeat is the unauth'd ingest endpoint. The token in the
// URL is matched against the heartbeat_token column on a monitor; if
// it matches, an "up" result is recorded AND any open Problem for that
// monitor is resolved synchronously (the runner only watches for
// absence so it never sees the up→down transition on its own).
func (s *Server) acceptHeartbeat(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	m, err := s.store.GetMonitorByToken(r.Context(), token)
	if err != nil { writeErr(w, err); return }
	if m == nil {
		// Don't leak whether the token is valid — same response shape
		// as a successful beat. Cheap defense against token enumeration.
		writeJSON(w, map[string]string{"status": "ok"})
		return
	}
	_ = s.store.InsertMonitorResult(r.Context(), chstore.MonitorResult{
		MonitorID: m.ID, Status: "up", Message: "heartbeat received",
	})
	// Auto-resolve any open Problem the runner opened for this monitor.
	// Runner ticks every 5s; without this synchronous resolution, the
	// alert would clear on the next tick (a 0-5s lag) AND no
	// notification would fire because runner rate-limits to state
	// changes only.
	if open, err := s.store.FindOpenProblem(r.Context(), "monitor:"+m.ID, m.Name); err == nil && open != nil {
		open.Status = "resolved"
		now := time.Now().UnixNano()
		open.ResolvedAt = &now
		_ = s.store.UpsertProblem(r.Context(), *open)
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// ── Problems & alert rules ───────────────────────────────────────────────────

// countProblems — sidebar-badge-only endpoint. Returns just
// {count: N} for the matching filter; cheap COUNT(*) query.
// Sidebar previously fetched up to 200 problems and counted
// the array, which capped the displayed badge at 200 — bad UX
// on installs with 200+ open problems. 5s TTL matches the list
// endpoint's cache so the badge and the list stay coherent.
func (s *Server) countProblems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := chstore.ProblemFilter{
		Status: q.Get("status"), Service: q.Get("service"),
		Severity: q.Get("severity"),
	}
	key := fmt.Sprintf("problems-count:status=%s:svc=%s:sev=%s",
		f.Status, f.Service, f.Severity)
	s.serveCached(w, r, key, 5*time.Second, func() (any, error) {
		n, err := s.store.CountProblems(r.Context(), f)
		if err != nil {
			return nil, err
		}
		return map[string]any{"count": n}, nil
	})
}

// listProblemBuckets — chip-label backing endpoint. Returns the
// per-severity and per-priority breakdown for a given status/service
// scope, IGNORING the severity and priority chip selections so the
// operator can see "P3 (12)" before clicking the P3 chip back on
// (v0.5.479 — restores the chip-count UX that v0.5.448 broke when
// priority filtering moved server-side). Limit is generous (2000)
// because the problems table is state-shaped and stays small even
// at scale; an install with 2000+ open problems has bigger issues
// than a UI count.
func (s *Server) listProblemBuckets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := chstore.ProblemFilter{
		Status: q.Get("status"), Service: q.Get("service"),
		Limit: 2000,
	}
	key := fmt.Sprintf("problems-buckets:status=%s:svc=%s", f.Status, f.Service)
	s.serveCached(w, r, key, 5*time.Second, func() (any, error) {
		probs, err := s.store.ListProblems(r.Context(), f)
		if err != nil {
			return nil, err
		}
		// Priority depends on RecentDeploy (fresh-deploy bump), so
		// the deploys enrich must run before the priority enrich.
		// Runbooks/clusters enrichment skipped — buckets don't need
		// either, and skipping the CH round-trips keeps this cheap.
		probs = s.store.EnrichProblemsWithDeploys(r.Context(), probs, 30*time.Minute)
		probs = chstore.EnrichProblemsWithPriority(probs)
		sev := map[string]int{"critical": 0, "warning": 0, "info": 0}
		prio := map[string]int{"P1": 0, "P2": 0, "P3": 0}
		for _, p := range probs {
			sev[p.Severity]++
			bucket := p.Priority
			if bucket == "" {
				bucket = "P3"
			}
			prio[bucket]++
		}
		return map[string]any{
			"severity": sev,
			"priority": prio,
			"total":    len(probs),
		}, nil
	})
}

func (s *Server) listProblems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// Priority — comma-separated P1/P2/P3 subset. Filtered post-enrich
	// because priority is computed at read time (v0.5.210), not a CH
	// column. Empty = no filter, behave as before.
	var prios []string
	if raw := strings.TrimSpace(q.Get("priority")); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				prios = append(prios, p)
			}
		}
	}
	f := chstore.ProblemFilter{
		Status: q.Get("status"), Service: q.Get("service"),
		Severity: q.Get("severity"), Priority: prios,
		Limit: parseInt(q.Get("limit"), 100),
	}
	// Sidebar polls this endpoint per user every 30s — at 100 logged-in
	// users that's 3 RPS just for the badge. 5s TTL collapses the load.
	// Priority set hashed via excludeKeyDigest (sorted + FNV) so two
	// distinct subsets cannot collide on the cache key — cf. v0.5.187.
	prioMap := make(map[string]bool, len(prios))
	for _, p := range prios {
		prioMap[p] = true
	}
	key := fmt.Sprintf("problems:status=%s:svc=%s:sev=%s:prio=%s:limit=%d",
		f.Status, f.Service, f.Severity, excludeKeyDigest(prioMap), f.Limit)
	s.serveCached(w, r, key, 5*time.Second, func() (any, error) {
		probs, err := s.store.ListProblems(r.Context(), f)
		if err != nil {
			return nil, err
		}
		// Resolve runbook URLs at read time — pulled from
		// the firing alert rule (preferred) or service
		// catalog metadata (fallback). Computed here, not
		// persisted on the problems table, so an
		// operator who edits a runbook URL sees existing
		// open problems pick up the new link on the next
		// refresh.
		probs = s.store.EnrichProblemsWithRunbooks(r.Context(), probs)
		// Cluster chips — same read-time pattern. One batch
		// CH query for the service→clusters map, soft-fails
		// silently on error so a transient blip doesn't
		// blank the page.
		probs = s.store.EnrichProblemsWithClusters(r.Context(), probs, time.Hour)
		// Deploy correlation — attach the most recent
		// service.version deploy within 30 min before each
		// problem fired. UI renders a "deployed vX.Y · 6m
		// before" tag so the operator immediately sees the
		// classic "regression coincided with deploy" pattern
		// without scrolling to the deploy markers panel.
		// 30 min covers most "rollout finished, started
		// receiving traffic, broke things" windows; longer
		// causal chains stay invisible to keep the signal
		// strong.
		probs = s.store.EnrichProblemsWithDeploys(r.Context(), probs, 30*time.Minute)
		// Priority bucket (v0.5.210) — pure function over the
		// already-enriched values, no CH round-trip. Runs last
		// so it can read RecentDeploy + value/threshold +
		// status in their final form. Recomputed on every read
		// so a worsening metric or fresh deploy reranks
		// instantly without rewriting the problems row.
		probs = chstore.EnrichProblemsWithPriority(probs)
		// Priority chip filter — applied AFTER enrich because
		// Problem.Priority is populated by EnrichProblemsWithPriority,
		// not stored on the CH row. Default "P3" matches the
		// frontend's fallback for un-bucketed rows so the chip
		// behaviour is consistent across read and render.
		if len(prios) > 0 {
			keep := make([]chstore.Problem, 0, len(probs))
			for _, p := range probs {
				bucket := p.Priority
				if bucket == "" {
					bucket = "P3"
				}
				if prioMap[bucket] {
					keep = append(keep, p)
				}
			}
			probs = keep
		}
		return probs, nil
	})
}

// acknowledgeProblems flips a batch of problems to
// status=acknowledged. Notifier checks for that status and
// skips channel fan-out so subsequent evaluator refreshes
// don't re-page on the same problem; the row stays "in
// flight" in the UI until the evaluator auto-resolves it
// (threshold no longer breached) or the operator manually
// edits the rule.
//
// Editor-role gated; each successful call is appended to the
// audit log so "who silenced this overnight" is answerable.
// setProblemAssignee — manual claim / reassign. Editor + admin
// only (route-gated). PATCH body: {"assignee": "<email or team>"}.
// Empty string clears the assignee — same upsert path. Audit
// log entry on every successful patch so the timeline shows
// who claimed what.
func (s *Server) setProblemAssignee(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "problem id required", http.StatusBadRequest)
		return
	}
	var body struct {
		Assignee string `json:"assignee"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	body.Assignee = strings.TrimSpace(body.Assignee)
	if err := s.store.SetProblemAssignee(r.Context(), id, body.Assignee); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"id": id, "assignee": body.Assignee})
	s.audit(r, "problem.assign", "problem", id, string(details))
	writeJSON(w, map[string]any{"id": id, "assignee": body.Assignee})
}

func (s *Server) acknowledgeProblems(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.IDs) == 0 {
		http.Error(w, "ids list required", http.StatusBadRequest)
		return
	}
	if len(body.IDs) > 200 {
		http.Error(w, "max 200 ids per bulk acknowledge", http.StatusBadRequest)
		return
	}
	n, err := s.store.AcknowledgeProblems(r.Context(), body.IDs, actorOf(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	// Audit: one entry per call (not per id) — the IDs go in
	// details so a reviewer can correlate against the problem
	// table without bloating the audit_log row count.
	details, _ := json.Marshal(map[string]any{"ids": body.IDs, "acknowledged": n})
	s.audit(r, "problem.acknowledge", "problem", "", string(details))
	writeJSON(w, map[string]any{"acknowledged": n})
}

func (s *Server) listAlertRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.ListAlertRules(r.Context())
	if err != nil { writeErr(w, err); return }
	writeJSON(w, rules)
}

func (s *Server) createAlertRule(w http.ResponseWriter, r *http.Request) {
	var rule chstore.AlertRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest); return
	}
	if rule.ID == "" {
		rule.ID = newID(8)
	}
	rule.BuiltIn = false
	rule.CreatedAt = time.Now().UnixNano()
	if err := s.store.UpsertAlertRule(r.Context(), rule); err != nil {
		writeErr(w, err); return
	}
	details, _ := json.Marshal(map[string]any{"name": rule.Name, "service": rule.Service, "metric": rule.Metric})
	s.audit(r, "alert_rule.create", "alert_rule", rule.ID, string(details))
	writeJSON(w, rule)
}

// updateAlertRule edits an existing rule (including built-ins). The
// `builtIn` flag and original `createdAt` are preserved server-side
// so the UI can't accidentally re-flag a built-in as user-created or
// reset its history. Built-in rules can be renamed and re-tuned —
// they're scaffolding, not contracts.
func (s *Server) updateAlertRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.store.GetAlertRule(r.Context(), id)
	if err != nil || existing == nil {
		http.Error(w, `{"error":"rule not found"}`, http.StatusNotFound); return
	}
	var rule chstore.AlertRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest); return
	}
	rule.ID = existing.ID
	rule.BuiltIn = existing.BuiltIn
	rule.CreatedAt = existing.CreatedAt
	if err := s.store.UpsertAlertRule(r.Context(), rule); err != nil {
		writeErr(w, err); return
	}
	details, _ := json.Marshal(map[string]any{"name": rule.Name, "service": rule.Service, "metric": rule.Metric})
	s.audit(r, "alert_rule.update", "alert_rule", rule.ID, string(details))
	writeJSON(w, rule)
}

func (s *Server) deleteAlertRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteAlertRule(r.Context(), id); err != nil {
		writeErr(w, err); return
	}
	s.audit(r, "alert_rule.delete", "alert_rule", id, "")
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) enableAlertRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.SetAlertRuleEnabled(r.Context(), id, true); err != nil {
		writeErr(w, err); return
	}
	s.audit(r, "alert_rule.enable", "alert_rule", id, "")
	writeJSON(w, map[string]string{"status": "ok"})
}

// disableAlertRule is the soft-disable counterpart of
// deleteAlertRule (v0.5.175 — DELETE now hard-removes the row).
// Used by the noisy-rules bulk action where the operator wants
// the rule out of the firing path but still wants the
// definition kept around for an easy re-enable if the silence
// turns out to be wrong.
func (s *Server) disableAlertRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.SetAlertRuleEnabled(r.Context(), id, false); err != nil {
		writeErr(w, err); return
	}
	s.audit(r, "alert_rule.disable", "alert_rule", id, "")
	writeJSON(w, map[string]string{"status": "ok"})
}

// getAlertBaseline computes recent percentile distribution
// for the requested service + metric so the alert-rule editor
// can pre-fill a sane threshold instead of making the operator
// guess. Lookback defaults to 7d (capped server-side); the
// service param is optional — empty returns a global
// baseline.
//
// Drives the ✨ "suggest threshold" panel next to the rule
// form's threshold input. Pure stats, no LLM round-trip, so
// the response lands in ms.
//
// Response also carries `suggestedWarning` / `suggestedCritical`
// — interpreted relative to the comparator the operator
// picked:
//   • `>`  / `>=` → high values trip (p95 → warn, p99 → crit)
//   • `<`  / `<=` → low values trip (5×p99/100 capped, 0.01 fallback)
// Operator can ignore them and use the raw percentiles too.
//
// Cached 5 min per (service, metric, comparator) tuple — the
// distribution doesn't shift by the second.
func (s *Server) getAlertBaseline(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	metric := strings.TrimSpace(q.Get("metric"))
	if metric == "" {
		http.Error(w, "metric required", http.StatusBadRequest)
		return
	}
	service := strings.TrimSpace(q.Get("service"))
	comparator := strings.TrimSpace(q.Get("comparator"))
	if comparator == "" {
		comparator = ">"
	}
	cacheKey := fmt.Sprintf("alert-baseline:%s:%s:%s", service, metric, comparator)
	s.serveCached(w, r, cacheKey, 5*time.Minute, func() (any, error) {
		b, err := s.store.GetMetricBaseline(r.Context(), service, metric, 7*24*time.Hour)
		if err != nil {
			return nil, err
		}
		warn, crit := suggestThresholds(b, comparator)
		return map[string]any{
			"metric":            b.Metric,
			"service":           b.Service,
			"p50":               b.P50,
			"p95":               b.P95,
			"p99":               b.P99,
			"max":               b.Max,
			"mean":              b.Mean,
			"sampleCount":       b.SampleCount,
			"windowSec":         b.WindowSec,
			"suggestedWarning":  warn,
			"suggestedCritical": crit,
		}, nil
	})
}

// suggestThresholds picks "safe to page on" levels from the
// distribution, biased by the comparator the operator chose.
//
//   • `>` / `>=` (alert on spike): warn at p95, crit at p99 —
//     fires when ~5% / ~1% of samples already crossed. Pure
//     percentile floors keep operators from setting "1 / 5"
//     thresholds that page nightly because the actual baseline
//     is 800ms / 8%.
//   • `<` / `<=` (alert on drop): warn at p5, crit at p1
//     approximated as max(0.01, value/2 / value/5) — fires
//     when traffic genuinely fell below the floor instead of
//     just dipping at a quiet hour.
//   • Round to a reasonable precision so the threshold input
//     doesn't end up at 412.7345 ms.
func suggestThresholds(b *chstore.MetricBaseline, comparator string) (warn, crit float64) {
	switch comparator {
	case "<", "<=":
		// Low-side: floor at 1% of the typical value to avoid
		// "alert when traffic = 0" being meaningless during
		// known quiet windows. Operator can tighten manually.
		warn = b.P50 * 0.20
		crit = b.P50 * 0.05
		if crit < 0.01 {
			crit = 0.01
		}
	default:
		// High-side: lean on the actual distribution.
		warn = b.P95
		crit = b.P99
	}
	return roundThreshold(warn), roundThreshold(crit)
}

// roundThreshold drops absurd decimals from a float so the UI
// shows "420 ms" not "419.7833... ms".
func roundThreshold(v float64) float64 {
	switch {
	case v >= 1000:
		return float64(int64(v/10+0.5)) * 10
	case v >= 100:
		return float64(int64(v + 0.5))
	case v >= 10:
		return float64(int64(v*10+0.5)) / 10
	default:
		return float64(int64(v*100+0.5)) / 100
	}
}

// ── Dashboards ───────────────────────────────────────────────────────────────

func (s *Server) listDashboards(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListDashboards(r.Context())
	if err != nil { writeErr(w, err); return }
	writeJSON(w, rows)
}

func (s *Server) getDashboard(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.GetDashboard(r.Context(), r.PathValue("id"))
	if err != nil { writeErr(w, err); return }
	if d == nil {
		http.Error(w, `{"error":"dashboard not found"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, d)
}

func (s *Server) createDashboard(w http.ResponseWriter, r *http.Request) {
	var d chstore.Dashboard
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest); return
	}
	if d.Name == "" {
		http.Error(w, `{"error":"name required"}`, http.StatusBadRequest); return
	}
	d.ID = newID(8)
	if err := s.store.UpsertDashboard(r.Context(), d); err != nil {
		writeErr(w, err); return
	}
	details, _ := json.Marshal(map[string]any{"name": d.Name})
	s.audit(r, "dashboard.create", "dashboard", d.ID, string(details))
	writeJSON(w, d)
}

func (s *Server) updateDashboard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.store.GetDashboard(r.Context(), id)
	if err != nil { writeErr(w, err); return }
	if existing == nil {
		http.Error(w, `{"error":"dashboard not found"}`, http.StatusNotFound); return
	}
	var d chstore.Dashboard
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest); return
	}
	d.ID = id
	d.CreatedAt = existing.CreatedAt
	if err := s.store.UpsertDashboard(r.Context(), d); err != nil {
		writeErr(w, err); return
	}
	details, _ := json.Marshal(map[string]any{"name": d.Name})
	s.audit(r, "dashboard.update", "dashboard", d.ID, string(details))
	writeJSON(w, d)
}

func (s *Server) deleteDashboard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteDashboard(r.Context(), id); err != nil {
		writeErr(w, err); return
	}
	s.audit(r, "dashboard.delete", "dashboard", id, "")
	writeJSON(w, map[string]string{"status": "ok"})
}

// ── SLOs ─────────────────────────────────────────────────────────────────────

func (s *Server) listSLOs(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListSLOs(r.Context())
	if err != nil { writeErr(w, err); return }
	// For the list page, pre-compute status alongside each SLO so the UI
	// can show health badges without N round-trips.
	type row struct {
		chstore.SLO
		Status *chstore.SLOStatus `json:"status,omitempty"`
	}
	rows := make([]row, 0, len(out))
	for _, o := range out {
		st, err := s.store.ComputeSLOStatus(r.Context(), o)
		if err != nil {
			log.Printf("[slo] status %s: %v", o.ID, err)
		}
		rows = append(rows, row{SLO: o, Status: st})
	}
	writeJSON(w, rows)
}

func (s *Server) getSLO(w http.ResponseWriter, r *http.Request) {
	o, err := s.store.GetSLO(r.Context(), r.PathValue("id"))
	if err != nil { writeErr(w, err); return }
	if o == nil {
		http.Error(w, `{"error":"slo not found"}`, http.StatusNotFound); return
	}
	writeJSON(w, o)
}

func (s *Server) sloStatus(w http.ResponseWriter, r *http.Request) {
	o, err := s.store.GetSLO(r.Context(), r.PathValue("id"))
	if err != nil { writeErr(w, err); return }
	if o == nil {
		http.Error(w, `{"error":"slo not found"}`, http.StatusNotFound); return
	}
	st, err := s.store.ComputeSLOStatus(r.Context(), *o)
	if err != nil { writeErr(w, err); return }
	writeJSON(w, st)
}

// sloForecast (v0.6.30) — burn-down projection. Cached 60s on
// (id, burnWindow) so /slos polling doesn't fan out per-row
// CH reads on every tab refresh. ComputeSLOForecast itself runs
// TWO short queries (status + short-window burn rate) — the
// cache wrapper collapses them across consecutive operator
// page-loads in a session.
func (s *Server) sloForecast(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	burnWindow := parseDuration(r.URL.Query().Get("window"), time.Hour)
	// Cap the burn window so a hallucinated ?window=720h doesn't
	// blow the budget on a backfill-shaped query.
	if burnWindow > 24*time.Hour {
		burnWindow = 24 * time.Hour
	}
	key := fmt.Sprintf("slo-forecast:%s:%s", id, burnWindow)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		o, err := s.store.GetSLO(r.Context(), id)
		if err != nil {
			return nil, err
		}
		if o == nil {
			return nil, fmt.Errorf("slo not found")
		}
		return s.store.ComputeSLOForecast(r.Context(), *o, burnWindow)
	})
}

// sloBurnSeries serves the per-day burn-rate timeseries that
// drives the /slos sparkline (v0.5.150). Cached 60s on (id, days)
// — sparkline doesn't need real-time accuracy and the GROUP BY
// over a 7d service-slice is cheap but not free.
func (s *Server) sloBurnSeries(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	days := parseInt(r.URL.Query().Get("days"), 7)
	key := fmt.Sprintf("slo-burn-series:%s:%d", id, days)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		o, err := s.store.GetSLO(r.Context(), id)
		if err != nil {
			return nil, err
		}
		if o == nil {
			return nil, fmt.Errorf("slo not found")
		}
		series, err := s.store.ComputeSLOBurnSeries(r.Context(), *o, days)
		if err != nil {
			return nil, err
		}
		if series == nil {
			series = []chstore.BurnPoint{}
		}
		return map[string]any{
			"series": series,
			"days":   days,
		}, nil
	})
}

func (s *Server) createSLO(w http.ResponseWriter, r *http.Request) {
	var o chstore.SLO
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest); return
	}
	if o.Name == "" || o.Service == "" || o.SLIType == "" {
		http.Error(w, `{"error":"name, service and sliType required"}`, http.StatusBadRequest); return
	}
	if o.Target <= 0 || o.Target >= 1 {
		http.Error(w, `{"error":"target must be a fraction between 0 and 1 (e.g. 0.99)"}`, http.StatusBadRequest); return
	}
	if o.SLIType == chstore.SLITypeLatency && o.ThresholdMs <= 0 {
		http.Error(w, `{"error":"thresholdMs required for latency SLIs"}`, http.StatusBadRequest); return
	}
	o.ID = newID(8)
	if err := s.store.UpsertSLO(r.Context(), o); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, o)
}

func (s *Server) deleteSLO(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSLO(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) getHealth(w http.ResponseWriter, r *http.Request) {
	// v0.5.280 — accepted counters added so the Topbar live
	// activity ticker can derive per-second rates client-side
	// (delta of cumulative ÷ elapsed).
	// v0.5.342 — overload signalling. If any ingest queue is
	// past the overload threshold (≥90% full) or the
	// drop-counter is climbing, return 503 so an upstream LB
	// (envoy "outlier_detection", k8s readinessProbe) can take
	// this pod out of rotation until it drains. The body still
	// renders for operators inspecting curl output.
	spansLen, spansCap := s.ing.Spans.QueueLen(), s.ing.Spans.Capacity()
	logsLen, logsCap := s.ing.Logs.QueueLen(), s.ing.Logs.Capacity()
	metricsLen, metricsCap := s.ing.Metrics.QueueLen(), s.ing.Metrics.Capacity()
	overloaded := isOverloaded(spansLen, spansCap) ||
		isOverloaded(logsLen, logsCap) ||
		isOverloaded(metricsLen, metricsCap)
	load := "ok"
	if overloaded {
		load = "overloaded"
	} else if isDegraded(spansLen, spansCap) ||
		isDegraded(logsLen, logsCap) ||
		isDegraded(metricsLen, metricsCap) {
		load = "degraded"
	}
	body := map[string]interface{}{
		"status":           load,
		"spans_queued":     spansLen,
		"logs_queued":      logsLen,
		"metrics_queued":   metricsLen,
		"spans_capacity":   spansCap,
		"logs_capacity":    logsCap,
		"metrics_capacity": metricsCap,
		"spans_dropped":    s.ing.Spans.Dropped(),
		"spans_accepted":   s.ing.Spans.Accepted(),
		"logs_accepted":    s.ing.Logs.Accepted(),
		"metrics_accepted": s.ing.Metrics.Accepted(),
	}
	w.Header().Set("Content-Type", "application/json")
	if overloaded {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(body)
}

// isOverloaded returns true when the queue is past 90% capacity.
// At that point the consumer is falling behind the producer and
// the soft back-pressure (channel buffering) is about to flip
// into hard drops. Pulling the pod out of LB rotation here gives
// the queue a chance to drain before the drop counter climbs.
func isOverloaded(depth, cap int) bool {
	if cap <= 0 {
		return false
	}
	return depth*10 >= cap*9
}

// isDegraded is the lighter signal — 70% full. Lets the body
// surface "degraded" to admin dashboards without taking the
// pod out of rotation yet.
func isDegraded(depth, cap int) bool {
	if cap <= 0 {
		return false
	}
	return depth*10 >= cap*7
}

// getVersion is the unauthenticated build-tag endpoint the login
// page calls to render the running release. Returns "dev" when
// the binary was built without a -X main.Version stamp.
// getBranding returns the admin-customised branding overlay
// (logo + strings + primary color). Public — the login page
// fetches this before the operator has a session so the bank's
// logo + login title can render on first paint. An empty struct
// is a valid response (no overrides saved yet); the SPA falls
// back to the bundled Coremetry defaults in that case.
func (s *Server) getBranding(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.GetBranding(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, b)
}

// putBranding overwrites the saved overlay. Admin-gated by the
// mux. Capped at 256 KB to keep a misconfigured logo upload
// from bloating system_settings — 256 KB is plenty for the
// ~30 KB PNGs operators typically paste in.
func (s *Server) putBranding(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	var b chstore.BrandingSettings
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, "invalid body (or > 256 KB): "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.PutBranding(r.Context(), b); err != nil {
		writeErr(w, err)
		return
	}
	// v0.5.325 — Scale-audit gap: every admin mutation writes an
	// audit row. Branding edits change customer-visible logos +
	// link/footer copy, so the compliance trail matters.
	details, _ := json.Marshal(map[string]any{
		"appName":      b.AppName,
		"loginTitle":   b.LoginTitle,
		"footerText":   b.FooterText,
		"language":     b.Language,
		"primaryColor": b.PrimaryColor,
		"logoBytes":    len(b.LogoDataURI),
	})
	s.audit(r, "settings.branding.update", "settings", "branding", string(details))
	writeJSON(w, b)
}

func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	v := s.version
	if v == "" {
		v = "dev"
	}
	writeJSON(w, map[string]string{"version": v})
}

// getTraceOpAnomalies pinpoints per-(service, operation) error
// spikes — operations that are either failing for the first time
// in the window or whose error count just doubled. Sits beside
// the metric anomaly stream on /anomalies so the SRE sees both
// "service X is bad" and "the specific endpoint that's bad". 60s
// cached; the underlying CH query is partition-pruned to 2× the
// window so cost is bounded at scale.
func (s *Server) getTraceOpAnomalies(w http.ResponseWriter, r *http.Request) {
	window := parseDuration(r.URL.Query().Get("window"), 5*time.Minute)
	key := fmt.Sprintf("anomaly:trace-ops:window=%s", window)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		hits, err := anomaly.DetectTraceOpAnomalies(r.Context(), s.store, window)
		if err != nil {
			return nil, err
		}
		muted, _ := s.store.ActiveSilencedFingerprints(r.Context())
		out := hits[:0]
		for _, a := range hits {
			fp := chstore.FingerprintAnomaly("trace_op", a.Operation, a.Service)
			if !muted[fp] {
				out = append(out, a)
			}
		}
		return out, nil
	})
}

// getAnomalyEvents returns the persistent history: every
// log-pattern + trace-op anomaly the recorder has observed in
// the requested window, currently-active and cleared alike.
// Status is computed in CH from last_seen freshness so a single
// query covers both. Default window is 24h. Cached 30s — the
// recorder upserts every 60s, so a 30s freshness ceiling means
// at most one tick of staleness.
func (s *Server) getAnomalyEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), 24*time.Hour)
	limit := parseInt(q.Get("limit"), 200)
	key := fmt.Sprintf("anomaly:events:since=%s:limit=%d", since, limit)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		rows, err := s.store.ListAnomalyEvents(r.Context(), chstore.ListAnomalyEventsFilter{
			SinceNs: time.Now().Add(-since).UnixNano(),
			Limit:   limit,
		})
		if err != nil {
			return nil, err
		}
		rows = s.store.EnrichAnomaliesWithClusters(r.Context(), rows, time.Hour)
		// v0.5.286 — attach the most recent deploy per service
		// in the 30 min preceding each event's startedAt so the
		// /anomalies page can answer "did this break because of a
		// deploy?" without a context switch. Uses the v0.5.283
		// effective-version chain (Helm labels, image tags) so
		// installs with no service.version still correlate.
		rows = s.store.EnrichAnomaliesWithDeploys(r.Context(), rows, 30*time.Minute)
		return rows, nil
	})
}

// getMetricAnomalies returns the open Problems whose rule_id
// starts with "anomaly:" — i.e. the entries opened by the
// background z-score detector against service-level metrics
// (error_rate, p99, request_rate). The /anomalies page uses this
// to surface the metric-side signal alongside log-pattern and
// trace-op anomalies. No cache: ListProblems already hits the
// indexed (status, started_at) primary key on the small problems
// table — sub-millisecond.
func (s *Server) getMetricAnomalies(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListProblems(r.Context(), chstore.ProblemFilter{
		Status:       "open",
		RuleIDPrefix: "anomaly:",
		Limit:        100,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

// getLogPatternAnomalies surfaces SRE-grade log-shape anomalies:
// curated production failure patterns (Oracle ORA-, OOM, NPE,
// deadlock, panic, TLS, connection-refused, etc.) that are either
// brand new in the window or up 2x+ over baseline. Per-pattern
// regex evaluation runs on the raw `logs` table; results are 60s
// cached so reloading /anomalies hits Redis. The default 5-minute
// window matches the cadence of the metric anomaly detector so
// both signals tell a coherent "what changed in the last 5 min"
// story.
func (s *Server) getLogPatternAnomalies(w http.ResponseWriter, r *http.Request) {
	window := parseDuration(r.URL.Query().Get("window"), 5*time.Minute)
	key := fmt.Sprintf("anomaly:log-patterns:window=%s", window)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		// v0.5.241 — pass the logstore (not chstore) so the
		// detector runs against whichever backend is wired:
		// CH path = match() + tokenbf prefilter; ES path =
		// query_string token-OR.
		hits, err := anomaly.DetectLogPatterns(r.Context(), s.logs, window)
		if err != nil {
			return nil, err
		}
		// Drop silenced fingerprints — operator has muted them
		// explicitly. They still get persisted into anomaly_events
		// by the recorder so history shows them with status.
		muted, _ := s.store.ActiveSilencedFingerprints(r.Context())
		out := hits[:0]
		for _, a := range hits {
			fp := chstore.FingerprintAnomaly("log_pattern", a.Pattern, a.Service)
			if !muted[fp] {
				out = append(out, a)
			}
		}
		return out, nil
	})
}

// getStatus drives the public-style status page: probes every dependency
// in parallel with a 2s per-component timeout, returns a structured
// list of components with operational | degraded | outage. Overall
// status is the worst component status, the same way statuspage.io and
// other industry-standard pages compute it.
//
// Cached for 5s — the /status page polls every 30s but a refresh-spammy
// user shouldn't be able to hammer the underlying systems with probes.
func (s *Server) getStatus(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "system-status", 5*time.Second, func() (any, error) {
		return s.collectStatus(r.Context()), nil
	})
}

type componentStatus struct {
	Name      string            `json:"name"`
	Status    string            `json:"status"` // operational | degraded | outage
	Message   string            `json:"message,omitempty"`
	LatencyMs int64             `json:"latencyMs,omitempty"`
	// Free-form key/value extras shown on the row — version, address,
	// db name, queue depth, ingest rate, etc. Kept loose so each
	// component can surface what's relevant without bloating the type.
	Info map[string]string `json:"info,omitempty"`
	// Per-second rate (only set on ingest queue components).
	RatePerSec float64 `json:"ratePerSec,omitempty"`
}

type systemStatus struct {
	Status      string             `json:"status"` // worst of the components
	CheckedAt   string             `json:"checkedAt"`
	Components  []componentStatus  `json:"components"`
}

func (s *Server) collectStatus(ctx context.Context) systemStatus {
	// 2s budget per probe — enough for a remote ping over a laggy
	// network, short enough that a single dead dependency can't make
	// the whole page hang. Each probe is its own goroutine so total
	// page latency = max(probe times) not sum.
	type result struct {
		idx int
		c   componentStatus
	}

	chProbe := func() componentStatus {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		start := time.Now()
		c := componentStatus{Name: "ClickHouse", Info: map[string]string{}}
		if err := s.store.Ping(pctx); err != nil {
			c.LatencyMs = time.Since(start).Milliseconds()
			c.Status = "outage"
			c.Message = err.Error()
			return c
		}
		// Liveness OK — surface version + connection details so the
		// operator can verify they're hitting the right cluster.
		var version, db, uptime string
		_ = s.store.Conn().QueryRow(pctx, `SELECT version(), currentDatabase(), formatReadableTimeDelta(uptime())`).Scan(&version, &db, &uptime)
		c.LatencyMs = time.Since(start).Milliseconds()
		c.Status = "operational"
		if version != "" {
			c.Info["version"] = version
		}
		if db != "" {
			c.Info["database"] = db
		}
		if uptime != "" {
			c.Info["uptime"] = uptime
		}
		return c
	}

	cacheProbe := func() componentStatus {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		start := time.Now()
		c := componentStatus{Name: "Cache (Redis)", Info: map[string]string{}}
		if err := s.cache.Ping(pctx); err != nil {
			c.LatencyMs = time.Since(start).Milliseconds()
			c.Status = "outage"
			c.Message = err.Error()
			return c
		}
		c.LatencyMs = time.Since(start).Milliseconds()
		c.Status = "operational"
		// The cache.Cache interface doesn't expose backend details
		// (kept it minimal). Surface the configured mode at least.
		switch s.cache.(type) {
		case interface{ Info(context.Context) (map[string]string, error) }:
			// hook for a future redis impl that exposes Info; not used today
		}
		c.Info["mode"] = "active"
		return c
	}

	logsProbe := func() componentStatus {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		start := time.Now()
		c := componentStatus{Name: "Logs backend"}
		if err := s.logs.Ping(pctx); err != nil {
			c.LatencyMs = time.Since(start).Milliseconds()
			c.Status = "outage"
			c.Message = err.Error()
			return c
		}
		c.LatencyMs = time.Since(start).Milliseconds()
		c.Status = "operational"
		c.Info = map[string]string{"backend": s.logs.Backend()}
		return c
	}

	probes := []func() componentStatus{chProbe, cacheProbe, logsProbe}
	out := make([]componentStatus, len(probes))
	// Hard ceiling on the whole probe pass — each individual
	// probe has its own 2s context timeout, but a misbehaving
	// driver could still hang. Without a parent deadline the
	// receive loop below would block indefinitely. Pre-v0.4.95
	// audit flagged this as a potential goroutine leak source;
	// the buffered channel + per-probe ctx already prevented a
	// true leak, but a stuck status endpoint blocking on every
	// caller poll is its own problem.
	probeCtx, probeCancel := context.WithTimeout(ctx, s.background.StatusProbeTimeout)
	defer probeCancel()
	results := make(chan result, len(probes))
	for i, p := range probes {
		i, p := i, p
		go func() { results <- result{i, p()} }()
	}
	// Select on probeCtx.Done so a caller disconnect / parent
	// cancel returns the partial result we have rather than
	// stalling on a probe that won't finish. Slots not received
	// stay at zero-value (Name=""), which the caller filters out.
	received := 0
	for received < len(probes) {
		select {
		case r := <-results:
			out[r.idx] = r.c
			received++
		case <-probeCtx.Done():
			received = len(probes) // bail out of the loop
		}
	}

	// API + Ingest queues with per-second rates sampled across status
	// invocations (delta of accepted counter / wall-clock delta).
	out = append(out,
		componentStatus{Name: "HTTP API", Status: "operational"},
		s.queueStatusWithRate("Spans ingest",   s.ing.Spans),
		s.queueStatusWithRate("Logs ingest",    s.ing.Logs),
		s.queueStatusWithRate("Metrics ingest", s.ing.Metrics),
	)

	rank := map[string]int{"operational": 0, "degraded": 1, "outage": 2}
	worst := "operational"
	for _, c := range out {
		if rank[c.Status] > rank[worst] {
			worst = c.Status
		}
	}
	return systemStatus{
		Status:     worst,
		CheckedAt:  time.Now().UTC().Format(time.RFC3339),
		Components: out,
	}
}

// queueStatusWithRate wraps the older queueStatus helper, additionally
// computing a per-second ingest rate as the delta of the accepted
// counter divided by wall-clock elapsed since the last sample.
//
// First sample after process start has no baseline so rate is reported
// as 0; subsequent samples are accurate.
type counter interface {
	QueueLen() int
	Capacity() int
	Dropped() int64
	Accepted() int64
}

func (s *Server) queueStatusWithRate(name string, q counter) componentStatus {
	c := queueStatus(name, q)
	now := time.Now()
	cur := q.Accepted()

	s.rateMu.Lock()
	prev, ok := s.rateSamples[name]
	s.rateSamples[name] = rateSample{at: now, count: cur}
	s.rateMu.Unlock()

	if ok {
		dt := now.Sub(prev.at).Seconds()
		if dt > 0 {
			rate := float64(cur-prev.count) / dt
			if rate < 0 {
				rate = 0 // counter shouldn't go backwards, but defensive
			}
			c.RatePerSec = rate
		}
	}
	if c.Info == nil {
		c.Info = map[string]string{}
	}
	c.Info["queue"] = fmt.Sprintf("%d / %d", q.QueueLen(), q.Capacity())
	if q.Dropped() > 0 {
		c.Info["dropped"] = fmt.Sprintf("%d", q.Dropped())
	}
	return c
}

// queueStatus inspects an ingest queue and reports degraded once it's
// past 80% full, outage once it's past 95% (where back-pressure starts
// dropping records). Capacity is read off the consumer so the status
// stays accurate when the operator tunes BufferSize via config.
func queueStatus(name string, q interface{ QueueLen() int; Capacity() int; Dropped() int64 }) componentStatus {
	cap := q.Capacity()
	depth := q.QueueLen()
	dropped := q.Dropped()
	c := componentStatus{Name: name, Status: "operational"}
	if dropped > 0 {
		c.Status = "outage"
		c.Message = fmt.Sprintf("dropped %d records — queue full", dropped)
		return c
	}
	if cap > 0 {
		if depth > cap*95/100 {
			c.Status = "outage"
			c.Message = fmt.Sprintf("queue %d / %d (>95%%)", depth, cap)
		} else if depth > cap*80/100 {
			c.Status = "degraded"
			c.Message = fmt.Sprintf("queue %d / %d (>80%%)", depth, cap)
		} else if depth > 0 {
			c.Message = fmt.Sprintf("queue %d / %d", depth, cap)
		}
	}
	return c
}

// ── Middleware & helpers ───────────────────────────────────────────────────────

// spaHandler serves the embedded Vite static SPA. Two behaviours
// that diverge from the stdlib http.FileServer:
//
//  1. Legacy fallback for the previous Next.js layout: if a
//     request resolves to a directory containing `index.html`
//     (the Next.js `output: 'export'` shape), serve the inner
//     index without a 301 redirect. Vite's flat dist/ doesn't
//     produce per-route directories, but the code stays defensive
//     against any future packaging variant.
//
//  2. For an unknown path that isn't a static asset (no file
//     extension) spaHandler falls back to the root `index.html`
//     so react-router can mount the right page from the URL.
//     This is the primary path for the Vite SPA — deep-links
//     like /services or /admin/sql exist only as routes in the
//     React Router config, never as files on disk.
func spaHandler(root fs.FS) http.Handler {
	const indexHTML = "index.html"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip the leading "/" — fs.FS paths are relative.
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = indexHTML
		}

		// Try the literal path first — covers static assets like
		// /_next/static/chunks/foo.js and the explicit /404.html.
		if f, err := root.Open(p); err == nil {
			defer f.Close()
			info, _ := f.Stat()
			if info != nil && !info.IsDir() {
				http.ServeFileFS(w, r, root, p)
				return
			}
			// It's a directory — try its index.html.
			if idx := path.Join(p, indexHTML); fileExists(root, idx) {
				http.ServeFileFS(w, r, root, idx)
				return
			}
		}

		// Path not found as-is; check if it maps to a Next.js
		// trailingSlash-style directory index (e.g. /logs → logs/index.html).
		if idx := path.Join(p, indexHTML); fileExists(root, idx) {
			http.ServeFileFS(w, r, root, idx)
			return
		}

		// SPA fallback: anything without a file extension gets the root
		// index.html so the client router can take over. Two carve-outs:
		//
		//   • Paths with file extensions are real 404s — no point
		//     shipping HTML for a missing /favicon.png or /chunks/foo.js.
		//
		//   • /api/* and /v1/* must NEVER fall back to HTML. Mux only
		//     reaches this handler for genuinely unmatched API paths
		//     (typo, removed route); the API contract is "JSON or
		//     error", and silently returning HTML to a fetch caller
		//     causes JSON.parse failures and downstream state corruption
		//     (we hit this — broke the auth-redirect loop on /logs).
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
			http.NotFound(w, r)
			return
		}
		if path.Ext(p) == "" && fileExists(root, indexHTML) {
			http.ServeFileFS(w, r, root, indexHTML)
			return
		}
		http.NotFound(w, r)
	})
}

func fileExists(root fs.FS, p string) bool {
	f, err := root.Open(p)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reflect the origin so credentialed requests (cookies) work; the
		// wildcard '*' is invalid with Allow-Credentials: true.
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	sanitizeFloats(v) // v0.5.303 — scrub NaN/Inf before encode
	json.NewEncoder(w).Encode(v)
}

// serveCached lives in cache.go — multi-tier (L1 + Redis +
// singleflight + SWR). Kept here as a pointer for grep.

// statusClientClosedRequest is nginx's non-standard 499 — the client closed
// the connection before the server responded. Go has no stdlib constant.
const statusClientClosedRequest = 499

func writeErr(w http.ResponseWriter, err error) {
	// Client hung up (browser navigated away / React Query superseded an
	// in-flight poll) — the request context died, so the error the handler
	// bubbled up is context.Canceled, NOT a server failure. Emit 499 and skip
	// the body (the client is gone) and the error log. Counting these as 5xx
	// inflated coremetry-api's self-obs error_rate and tripped false anomalies;
	// logging them spammed "[api] error: context canceled". context.Deadline-
	// Exceeded (a real server-side timeout) is NOT context.Canceled, so it
	// still falls through to the 500 path. v0.7.13.
	if errors.Is(err, context.Canceled) {
		w.WriteHeader(statusClientClosedRequest)
		return
	}
	log.Printf("[api] error: %v", err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// parseFilters decodes the JSON-encoded `filters` query parameter — a list
// of {k, op, v} objects — into the typed FilterExpr slice consumed by the
// query layer. Empty / malformed → no filters applied.
func parseFilters(raw string) []chstore.FilterExpr {
	if raw == "" {
		return nil
	}
	var out []chstore.FilterExpr
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		log.Printf("[api] filters parse: %v", err)
		return nil
	}
	return out
}

// parseFiltersAndDSL merges the JSON `filters` param with a free-form `dsl`
// param. Both are optional; conditions from each are AND-joined.
// A DSL parse error is surfaced as a 4xx via the returned error.
func parseFiltersAndDSL(jsonFilters, dsl string) ([]chstore.FilterExpr, error) {
	out := parseFilters(jsonFilters)
	if strings.TrimSpace(dsl) == "" {
		return out, nil
	}
	parsed, err := chstore.ParseDSL(dsl)
	if err != nil {
		return nil, err
	}
	return append(out, parsed...), nil
}

// isSafeAttrKey allows only OTel-style attribute keys (alphanum + dot,
// underscore, dash). Everything else — quotes, slashes, whitespace,
// SQL meta — is rejected so the key can flow safely into a CH `?`
// parameter without surprises in logs or UI rendering.
func isSafeAttrKey(s string) bool {
	if s == "" || len(s) > 96 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.' || r == '_' || r == '-':
			// ok
		default:
			return false
		}
	}
	return true
}

func parseInt(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// collapseRoute rewrites volatile path segments (trace IDs, span
// IDs, service names, numeric IDs) to bounded placeholders so the
// otelhttp span name stays a route TEMPLATE rather than one name
// per ID. Keeps the spans table's LowCardinality(String) `name`
// column bounded (v0.6.46 — see the WithSpanNameFormatter call in
// Run() for the cardinality rationale).
//
// Rules, applied per `/`-segment:
//   - segment right after a name-keyed collection ("services") →
//     ":svc" (service names are high-cardinality: 1000s)
//   - hex string ≥ 8 chars (trace_id 32, span_id 16) → ":id"
//   - all-digit segment → ":id"
//   - everything else kept verbatim (static route words)
//
// Pure + allocation-light: most API paths have ≤4 segments.
func collapseRoute(p string) string {
	if p == "" || p == "/" {
		return p
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if s == "" {
			continue
		}
		if i > 0 && segs[i-1] == "services" {
			segs[i] = ":svc"
			continue
		}
		if isVolatileSegment(s) {
			segs[i] = ":id"
		}
	}
	return strings.Join(segs, "/")
}

func isVolatileSegment(s string) bool {
	if len(s) == 0 {
		return false
	}
	allDigit := true
	for _, r := range s {
		if r < '0' || r > '9' {
			allDigit = false
			break
		}
	}
	if allDigit {
		return true
	}
	// Hex ≥ 8 chars catches trace IDs (32), span IDs (16), and
	// UUID-ish tokens (with or without dashes). Dashes allowed so a
	// canonical UUID still collapses.
	if len(s) >= 8 {
		for _, r := range s {
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex && r != '-' {
				return false
			}
		}
		return true
	}
	return false
}

// parseTime converts a nanosecond-epoch string to time.Time
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	ns, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

func parseDuration(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return def
}
