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
	"go.opentelemetry.io/otel/trace"
	"hash/fnv"
	"html"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sync/singleflight"

	"github.com/cilcenk/coremetry/internal/acache"
	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/appschema"
	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/cluster"
	"github.com/cilcenk/coremetry/internal/config"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/devops"
	"github.com/cilcenk/coremetry/internal/entity"
	"github.com/cilcenk/coremetry/internal/influx"
	"github.com/cilcenk/coremetry/internal/ldap"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/mcp"
	"github.com/cilcenk/coremetry/internal/mcpclient"
	"github.com/cilcenk/coremetry/internal/notify"
	"github.com/cilcenk/coremetry/internal/otlp"
	"github.com/cilcenk/coremetry/internal/pipeline"
	"github.com/cilcenk/coremetry/internal/profileconv"
	"github.com/cilcenk/coremetry/internal/promptfmt"
	"github.com/cilcenk/coremetry/internal/rag"
	"github.com/cilcenk/coremetry/internal/rollout"
	"github.com/cilcenk/coremetry/internal/sse"
	"github.com/cilcenk/coremetry/internal/tempo"
	"github.com/cilcenk/coremetry/internal/thanos"
	"github.com/cilcenk/coremetry/internal/vmetrics"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type Server struct {
	addr string
	// tracer (v0.10.114) — ✨ Explain span'larının kaynağı; nil →
	// selfobs.Tracer() (prod). Testler in-memory exporter'lı bir SDK
	// tracer'ı enjekte eder (ai_span_test.go).
	tracer trace.Tracer
	// roleIngestOff / roleAPIOff — inverted so the zero value keeps every
	// role ON (monolithic + direct-constructed test Servers unchanged);
	// SetRoles flips them from main's parsed COREMETRY_MODE (v0.8.346).
	roleIngestOff bool
	roleAPIOff    bool
	// chPing* back the 5s-cached CH reachability read on /api/health
	// (v0.8.339) — see chReachable.
	chPingMu sync.Mutex
	// watcherImportMu serialises the watcher import's name-collision
	// check + insert (v0.9.x, review F8) — closes the same-pod TOCTOU;
	// see importWatcher for the cross-pod posture.
	watcherImportMu sync.Mutex
	// meUsers — /api/auth/me'nin 30s kullanıcı cache'i (v0.8.519,
	// perf raporu #7); her kullanıcı-yazma yolu clear() çağırır.
	meUsers *meCache
	// problemCounts — page/filter/env-değişmez açık-problem sayaçlarının
	// 30s process-geneli cache'i (v0.8.533, getServices audit fix B);
	// getServices + /service-map paylaşır.
	problemCounts *problemCountsCache
	// serviceSeen — service_seen MV'sinin (v0.9.1317, entity-model A2)
	// süreç-geneli 5dk anlık görüntüsü + MV'nin kendi taban zamanı.
	// Sayfa/filtre/aralık-değişmez; getServices onu satırlara damgalar.
	serviceSeen *serviceSeenCache
	// svcRuntimes — /api/services-runtimes'ın merge+stale katmanı
	// (v0.9.46): 15m taramalar birikir, CH hatasında son harita
	// servis edilir; rozetler toptan sönmez.
	svcRuntimes serviceRuntimesCache
	chPingAt    time.Time
	chPingOK    bool
	// distQueue* — /api/health'in Distributed spool sinyali (v0.9.985).
	// Handler bu kilidi ALIR ama CH'yi BEKLEMEZ: tazeleme ayrı bir
	// goroutine'de koşar, handler her zaman son bilinen değeri servis
	// eder. Gerekçe ÖLÇÜLDÜ — probe medyanı arızalı kümede 646 ms (tepe
	// 1300 ms), /api/health ise 5 saniyede bir dönüyor ve hot-endpoint
	// bütçesi p99 < 50 ms. Senkron okuma bütçeyi 13 katına çıkarırdı.
	distQueueMu         sync.Mutex
	distQueueAt         time.Time
	distQueueRefreshing bool
	// cur/prev — trend İKİ ARDIŞIK ölçümden çıkar; prev yalnız GERÇEKTEN
	// ölçülmüş bir örnekle döner, düşen bir probe trend tabanını bozmaz.
	distQueueCur  *chstore.DistributionQueue
	distQueuePrev *chstore.DistributionQueue
	// spoolFlights (v0.9.1191) — Distributed spool runbook'unun flush
	// uçuş defteri (spool_actions.go). Sıfır değeri kullanılabilir.
	spoolFlights spoolFlights

	// stateUnify (v0.9.1312) — 0009 state birleştirme sihirbazının
	// tek-uçuş ilerleme kaydı. Göç 37 tabloyu tek tek gezer ve prod'da
	// dakikalar sürer; istek bağlamında koşamaz, bu yüzden durum
	// burada tutulup /api/admin/state-unify/status'tan yoklanır.
	stateUnify stateUnifyFlight
	// stateRepart (v0.9.1341) — 0010 partition sökme sihirbazının
	// tek-uçuş ilerleme kaydı. Aynı gerekçe: `problems` tablosunun
	// tamamı kopyalanır, istek bağlamında koşamaz.
	stateRepart stateRepartFlight
	// distQueueState — HİSTEREZİS durumu (v0.9.987). Karar artık iki
	// ölçümden değil, ÖNCEKİ KARAR + iki ölçümden çıkıyor: durumsuz hâlde
	// 44.320 → 44.318 (iki dosya) tek başına "degraded → ok" yapıyordu.
	distQueueState chstore.DistributionState
	// httpSrv is the live http.Server once Start() runs — kept so main
	// can Shutdown() it during the ordered v0.8.336 teardown (stop
	// ACCEPTING before draining consumers; a bare ListenAndServe had no
	// shutdown surface at all).
	httpSrv *http.Server
	store   *chstore.Store
	logs    logstore.Store // read-side abstraction; CH or external ES
	// logsMgr owns the UI-managed logstore config (Settings →
	// Elasticsearch, v0.8.232). Nil-safe: handlers 503 without it.
	logsMgr *logstore.ESManager
	// tails is the shared live-tail broadcaster (v0.8.236): one poll
	// loop per distinct /logs filter, fanned out to every SSE tab.
	tails *tailBroker
	ing   *otlp.Ingester
	// lockDegraded — Redis was configured but the leader lock fell back to the
	// always-leader Noop (Redis down at boot). Surfaced on /admin/stats so a
	// multi-pod operator sees that background jobs are duplicated. Set by
	// SetLockDegraded from main.go. v0.8.212. Atomic since v0.8.341: the Redis
	// re-probe goroutine CLEARS it at runtime (after swapping the real lock in)
	// while /admin/stats handlers read it concurrently.
	lockDegraded atomic.Bool
	webFS        embed.FS
	auth         *auth.Service
	oidc         *auth.OIDCService // nil when SSO disabled
	ldap         *ldap.Service     // always set; Enabled() reports config presence
	// ldapGroupSync — LDAP/AD group-membership sync engine (v0.8.526).
	// Set via SetLdapGroupSync from main(); nil-safe — the group-sync
	// admin handlers return 503 until wired. Reads the in-memory snapshot
	// pointer (no CH round-trip on the status read).
	ldapGroupSync *ldap.SyncEngine
	cache         cache.Cache // Noop when Redis isn't configured
	// presence — throttled per-user "last authenticated activity"
	// stamps for the admin Users page's online indicator (v0.8.403).
	// See presence.go for the write/read paths + semantics.
	presence *presenceTracker
	// sf dedupes concurrent upstream calls when a hot cache
	// key misses or enters the SWR refresh window — see
	// cache.go for the multi-tier read path.
	sf singleflight.Group
	// l1 is the in-process front tier ahead of Redis. Short
	// per-entry TTL (≤5s); catches same-node burst traffic
	// without crossing the network. Sized at 1024 entries —
	// enough for every distinct cache key the API exposes,
	// generous on memory because each entry stores marshaled
	// JSON not raw objects.
	l1 *l1Cache
	// stats records per-tier hit counts and hottest keys so
	// the System page can show whether the multi-tier cache
	// is doing useful work. Exposed via /api/admin/cache-stats.
	stats   *cacheStats
	notify  *notify.Notifier
	copilot *copilot.Service // nil when AI key not configured
	// rag — doküman RAG servisi (v0.8.438). SetRAG ile bağlanır;
	// nil / yapılandırılmamışken tüm RAG yolları sessizce kapalı.
	rag *rag.Service
	bus *sse.Broker // in-process SSE pub/sub for live UI updates
	// rolloutCfg — v0.10.200: rollouts bayrağı + vidalar (SetRollout ile bağlanır).
	rolloutCfg *rollout.SettingsService
	// tempo is the external Tempo backend (v0.5.208). When
	// configured, getTrace falls back to Tempo on a CH miss so
	// operators running Coremetry at 5% sampling + Tempo at
	// 100% can still resolve long-tail trace IDs in the same
	// /trace?id= URL. nil-safe — every accessor on the service
	// short-circuits when the receiver is nil.
	tempo *tempo.Service

	// thanos — çoklu-cluster Thanos Querier istemcisi (v0.8.576).
	// /clusters yüzeyi + Settings → Clusters. nil ya da boş liste →
	// rotalar 404/boş snapshot döner.
	thanos *thanos.Service
	// v0.10.129 — K8s entity katmanı (entity_routes.go): ayarlar her modda, syncer yalnız worker.
	entitySettings *entity.SettingsService
	entitySync     *entity.Syncer

	// vmetrics — dış VictoriaMetrics OKUMA backend'i (v0.9.1150, Faz 1).
	// Configured() true olduğunda metrik keşif/sorgu yüzeyleri
	// (katalog+picker, Explore, dashboard "metric" panelleri, MCP
	// query_metric, etiket değerleri, attr anahtarları) CH yerine VM'den
	// beslenir; kapalıyken CH yolu bayt-bayt aynıdır. Span türevli her
	// şey ve sabit-adlı iç okuyucular CH'de KALIR (metricsource.go
	// başlığındaki kapsam listesi). nil-safe.
	vmetrics *vmetrics.Service

	// influx — InfluxDB 2.x dış metrik kaynakları (v0.10.222, D1: yalnız
	// kaynak yönetimi + test; poller D2). nil-safe: handler'lar 503.
	influx *influx.Service
	// influxWorker — bu pod'daki poll işçisi (v0.10.223; yalnız worker/all
	// rolünde set edilir). Durum ucu bellek durumunu buradan okur; nil = bu
	// pod poll'lamıyor.
	influxWorker *influx.Worker

	// devops — Azure DevOps Server / TFS bağlantısı (v0.9.829).
	// ŞİMDİLİK YALNIZ BAĞLANTI: ayar + kimlik + erişilebilirlik
	// testi. Repo eşleme ve kod-inceleme sonraki dilim, dolayısıyla
	// bu istemcinin henüz tüketicisi yok. nil-safe.
	devops *devops.Service
	// schema (v0.10.115) — uygulama DB şema kataloğu anlık görüntüsü
	// (appschema); SQLCODE'lu Explain'lerde kolon tanımı kanıtı.
	schema *appschema.Service

	// mcpClient — dış MCP sunucularının istemci servisi (v0.10.87,
	// dilim ②). Şimdilik ayar katmanı; sohbet köprüsü dilim ③. nil-safe.
	mcpClient *mcpclient.Service

	// cluster — per-pod heartbeat / membership service (v0.5.253).
	// Always non-nil when Set; the service degenerates to a single-
	// pod view when Redis is absent so handlers don't need to nil-
	// check before calling Members.
	cluster *cluster.Service

	// pipeline — ingest-time drop / enrich rule engine (v0.5.263).
	// Admin-managed via Settings → Pipeline. May be nil before
	// SetPipeline is called from main(); admin handlers nil-check
	// and return 503.
	pipeline *pipeline.Engine

	// autocomplete — Redis-backed picker-facet cache (v0.8.80). The
	// service/operation/attribute-value pickers try it first and fall
	// back to ClickHouse on a miss. nil-safe: every accessor on the
	// store short-circuits to a miss when the receiver is nil/disabled.
	autocomplete *acache.Store

	// mcp — Model Context Protocol server (v0.6.4). Exposes
	// Coremetry's tools / resources / prompts to external LLM
	// clients (Claude Desktop, Anthropic API tool-calling,
	// internal copilots). nil-safe: routes are only registered
	// when SetMCP is called from main(). HTTP+SSE transport on
	// /api/mcp/sse + /api/mcp/messages, Streamable-HTTP on
	// /api/mcp; auth via the existing JWT/cmk_ middleware.
	//
	// Rol taşıması (v0.9.1136): middleware yalnız KİMLİĞİ taşır —
	// MCP'nin tek uç noktası var, dolayısıyla auth.RequireRole
	// takılacak bir route tablosu yok. Rol zorlaması mcpCallGate'te
	// (mcp_gate.go) mcp.Tool/Resource/Prompt.MinRole'e karşı yapılır.
	// Bu satır v0.9.1136'ya dek "role-based access carries into MCP"
	// diyordu; kapı rolü HİÇ okumuyordu — yorum yanlıştı, mekanizma
	// artık var.
	mcp *mcp.Server

	// Demo deployments only — when true, /api/auth/config returns
	// initial admin credentials so the login page can pre-fill them.
	demoMode     bool
	demoEmail    string
	demoPassword string

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

	// MCP tools/call rate limiti (v0.9.14) — kimlik-anahtarlı sabit
	// pencere; desen subRateBy'ın aynısı (mcp_gate.go).
	mcpRateMu sync.Mutex
	mcpRateBy map[string]*mcpRateBucket

	// version is the build-time release tag stamped via -ldflags.
	// Surfaced unauthenticated on /api/version so the login page
	// can show it before the operator has a session.
	version string
	// buildVersion (v0.9.339) is the tag baked into the binary, kept apart
	// from `version` so a COREMETRY_VERSION override can be reported AS an
	// override instead of quietly replacing the build identity.
	buildVersion string

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
	clusterNodesMu   sync.RWMutex
	lastClusterNodes []CHClusterNode
	lastClusterAt    time.Time
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

// SetBuildVersion records what the IMAGE actually is, independent of any
// display override. v0.5.394 removed the env override precisely because a
// stale value masked this; keeping both is what lets the override come back
// safely (v0.9.339).
func (s *Server) SetBuildVersion(v string) {
	s.buildVersion = v
}

// SetTempo wires the external Tempo client. Always called from
// main() with a non-nil service — Configured() reports whether the
// operator has actually filled in the settings.
func (s *Server) SetTempo(t *tempo.Service) {
	s.tempo = t
}

// SetThanos wires the multi-cluster Thanos Querier client
// (v0.8.576). Always called from main() with a non-nil service —
// HasEnabledClusters() reports whether the operator configured
// any cluster.
func (s *Server) SetThanos(t *thanos.Service) {
	s.thanos = t
}

// SetVMetrics wires the external VictoriaMetrics read backend
// (v0.9.1150). Always called from main() with a non-nil service —
// Configured() reports whether the operator enabled it AND gave it a
// URL, and that predicate alone decides which store answers the metric
// surfaces (metricsource.go).
func (s *Server) SetVMetrics(v *vmetrics.Service) {
	s.vmetrics = v
}

// SetInflux — InfluxDB kaynak servisi (v0.10.222; influx_routes.go).
func (s *Server) SetInflux(i *influx.Service) {
	s.influx = i
}

// SetInfluxWorker — bu pod'daki Influx poll işçisi (v0.10.223); nil-safe.
func (s *Server) SetInfluxWorker(w *influx.Worker) {
	s.influxWorker = w
}

// SetDevOps wires the Azure DevOps / TFS connection client
// (v0.9.829). Always called from main() with a non-nil service;
// Configured() reports whether the operator filled in a server
// URL. Connection layer only — no reader consumes it yet.
// SetSchemaCatalog (v0.10.115) — main.go bağlar; nil güvenli (Summary boş).
func (s *Server) SetSchemaCatalog(sc *appschema.Service) {
	s.schema = sc
}

func (s *Server) SetDevOps(d *devops.Service) {
	s.devops = d
}

// SetMCPClient — dış MCP istemci servisini bağlar (v0.10.87). main()
// her zaman non-nil verir; handler'lar yine de nil'e dayanıklı (unit
// testlerin kısmi kurulumu boş snapshot görür, çökmez).
func (s *Server) SetMCPClient(m *mcpclient.Service) {
	s.mcpClient = m
}

// SetCluster wires the per-pod heartbeat / membership service
// (v0.5.253). Always called from main() — the service degenerates
// to a single-pod view when Redis isn't configured so handlers
// don't need to nil-check before calling Members.
func (s *Server) SetCluster(c *cluster.Service) {
	s.cluster = c
}

// SetAutocomplete wires the Redis autocomplete cache (v0.8.80). Called once
// from main() after the api.Server is constructed. nil-safe — the picker
// handlers fall back to ClickHouse when it's absent or cold.
func (s *Server) SetAutocomplete(a *acache.Store) {
	s.autocomplete = a
}

// SetMCP wires the Model Context Protocol server (v0.6.4). Called
// once from main() after the api.Server is constructed. nil is
// valid — leaves the /api/mcp/* routes unregistered.
func (s *Server) SetMCP(m *mcp.Server) {
	s.mcp = m
	if m != nil {
		// v0.9.14 — çağrı kapısı: kimlik başına 60/dk (mcp_gate.go).
		// v0.9.1136 — aynı kapı artık ROL de kontrol ediyor (mcp.Tool/
		// Resource/Prompt.MinRole) ve tools/call'ın yanı sıra
		// resources/read + prompts/get yollarını da kapsıyor. Paket
		// auth-agnostik kalır; kimliği api katmanı context'ten okur,
		// MinRole çözümünü mcp paketi (registry sahibi) yapar.
		m.SetCallGate(s.mcpCallGate)
	}
}

type rateSample struct {
	at    time.Time
	count int64
}

// SetRAG bağlar (v0.8.438) — cluster.Set deseninde opsiyonel bağımlılık.
func (s *Server) SetRAG(r *rag.Service) { s.rag = r }

// SetLdapGroupSync wires the LDAP group-sync engine (v0.8.526).
func (s *Server) SetLdapGroupSync(e *ldap.SyncEngine) { s.ldapGroupSync = e }

func NewServer(addr string, ing *otlp.Ingester, store *chstore.Store, logs logstore.Store, webFS embed.FS, authSvc *auth.Service, oidcSvc *auth.OIDCService, ldapSvc *ldap.Service, c cache.Cache, n *notify.Notifier, cop *copilot.Service, bus *sse.Broker) *Server {
	return &Server{
		addr: addr, store: store, logs: logs, tails: newTailBroker(logs), ing: ing, webFS: webFS,
		auth: authSvc, oidc: oidcSvc, ldap: ldapSvc, cache: c, notify: n, copilot: cop,
		bus:           bus,
		presence:      newPresenceTracker(c),
		rateSamples:   map[string]rateSample{},
		l1:            newL1Cache(1024),
		stats:         newCacheStats(),
		meUsers:       newMeCache(30 * time.Second),
		problemCounts: newProblemCountsCache(30 * time.Second),
		serviceSeen:   newServiceSeenCache(5 * time.Minute),
		subRateBy:     map[string]int64{},
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

// SetLockDegraded records that the distributed leader lock fell back to the
// always-leader Noop despite Redis being configured (Redis down at boot) — so
// /admin/stats can warn that multi-pod background jobs are duplicated. v0.8.212.
// Called with false by the Redis re-probe (v0.8.341) once the real lock is
// swapped back in — the /admin/stats warning clears without a pod restart.
func (s *Server) SetLockDegraded(b bool) { s.lockDegraded.Store(b) }

// SetLogstoreESManager wires the UI-managed logstore config owner
// (v0.8.232). main() constructs the manager alongside the Switchable
// logstore; the Settings → Elasticsearch handlers are 503 no-ops
// without it (partial init / tests).
func (s *Server) SetLogstoreESManager(m *logstore.ESManager) { s.logsMgr = m }

// SetRoles wires the pod's runtime role split into the HTTP surface
// (v0.8.346, HA audit H6). main.go's old comment claimed "api.NewServer
// handles the role guard internally" — no such code existed: every role
// registered POST /v1/* while only ingest pods Start() the consumers, so
// a collector pointed at an api-role pod had its Exports 200-OK'd into
// channels NOBODY DRAINED (silent black hole; queue gauges even looked
// healthy at a constant 100%). Defaults (unset) = all roles on, which
// keeps monolithic mode and test-constructed Servers byte-identical.
func (s *Server) SetRoles(ingest, apiRole bool) {
	s.roleIngestOff = !ingest
	s.roleAPIOff = !apiRole
}

// otlpRouteGuard 501s OTLP ingest routes on pods whose role never
// starts the consumers — a spec-visible refusal the collector logs and
// alerts on, instead of the silent ACK-into-void. Pure so the guard
// decision is table-tested.
func otlpRouteGuard(disabled bool, next http.Handler) http.Handler {
	if !disabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w,
			"this pod does not run the ingest role (COREMETRY_MODE) — point the collector at the ingest Service",
			http.StatusNotImplemented)
	})
}

// editorRoles is the role bundle used by RequireAnyRole on routes
// that admin + editor may both use (dashboards, monitors, alerts,
// incidents, exception triage). Admin-only routes (user mgmt, system
// settings, channels, status page) keep RequireRole for clarity.
var editorRoles = []string{auth.RoleAdmin, auth.RoleEditor}

func (s *Server) Start() error {
	mux := s.buildMux()
	log.Printf("[api] HTTP listening on %s", s.addr)
	// v0.6.42 — self-observability. otelhttp wraps the outer-most
	// handler so every inbound request gets a server span tagged
	// with route + status + duration. Frontend's `traceparent`
	// header (lib/otel.ts) is extracted by otelhttp's
	// W3C propagator, so a UI click → server-span chain joins on
	// /trace/{id}. Goes OUTSIDE cors + auth middleware so the span
	// captures the entire request lifecycle (including 401s).
	// presence.middleware sits INSIDE auth.Middleware so claims are
	// already resolved (JWT + trusted-header paths both covered) —
	// v0.8.403, online indicator on the admin Users page. Throttled +
	// fire-and-forget: zero added latency, no Redis dependency on the
	// auth path (see presence.go).
	return s.listen(mux)
}

// buildMux → route_registry.go (v0.10.247): registerRoutes + init()
// defteri (registerRoutesExtra). Yeni yüzey api.go'ya satır eklemez.
func (s *Server) registerRoutes(mux *http.ServeMux) {

	// OTLP HTTP
	otlpHandler := otlpRouteGuard(s.roleIngestOff, otlp.HTTPHandler(s.ing))
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
	mux.HandleFunc("GET /api/namespaces", s.getNamespaces)
	// v0.8.576 — Thanos-backed remote-cluster pod metrikleri
	// (/clusters yüzeyi). Fan-out istemcide: sayfa cluster başına
	// ayrı istek atar, her cluster kendi cache slotunda (audit §6).
	mux.HandleFunc("GET /api/clusters/pods", s.getClusterPods)
	mux.HandleFunc("GET /api/clusters/pods/detail", s.getClusterPodDetail)
	mux.HandleFunc("GET /api/clusters/nodes", s.getClusterNodes)                              // v0.8.583 — node CPU/mem (dar kapsam)
	mux.HandleFunc("GET /api/clusters/summary", s.getClusterSummary)                          // v0.8.586 — kart özeti (skaler)
	mux.HandleFunc("GET /api/clusters/network-trend", s.getClusterNetworkTrend)               // v0.9.9 — Overview throughput
	mux.HandleFunc("GET /api/clusters/resource-trend", s.getClusterResourceTrend)             // v0.9.35 — Overview CPU/Mem
	mux.HandleFunc("GET /api/clusters/deploy-trend", s.getClusterDeployTrend)                 // v0.9.50 — Service→Infra CPU/Mem (§8)
	mux.HandleFunc("GET /api/clusters/haproxy-trend", s.getClusterHaproxyTrend)               // v0.9.534 — Service→Infra Router/HAProxy
	mux.HandleFunc("GET /api/clusters/jmx-metrics", s.getClusterJMXMetrics)                   // v0.9.144 — JBoss/JVM JMX auto-discovery
	mux.HandleFunc("GET /api/clusters/jmx-trend", s.getClusterJMXTrend)                       // v0.9.140 — Service→Infra JBoss/JVM JMX trend
	mux.HandleFunc("GET /api/clusters/alerts", s.getClusterAlerts)                            // v0.9.36 — firing alerts
	mux.HandleFunc("GET /api/clusters/namespaces", s.getClusterNamespaces)                    // v0.8.588 — ns rollup
	mux.HandleFunc("GET /api/clusters/namespaces/detail", s.getClusterNamespaceDetail)        // v0.9.2 — ns trend
	mux.HandleFunc("GET /api/clusters/namespaces/pods-trend", s.getClusterNamespacePodsTrend) // v0.9.3 — multi-pod
	mux.HandleFunc("GET /api/clusters/deployments", s.getClusterDeployments)                  // v0.9.22 — iş yükü rollup'u
	mux.HandleFunc("GET /api/clusters/sources", s.getClusterSources)
	// v0.8.383 — distinct deploy_env values in the window; feeds the
	// global Topbar environment picker (env-separation Phase 1).
	mux.HandleFunc("GET /api/environments", s.getEnvironments)
	// v0.9.135 (scale-audit 2026-07-20) — admin-only, matching the rest of
	// /api/admin/* (ingest drop counters + disk/per-table stats are operator
	// internals; the handler itself had no role check).
	mux.HandleFunc("GET /api/admin/system-stats", auth.RequireRole(auth.RoleAdmin, s.getSystemStats))
	// v0.5.328 — ClickHouse self-stats: slow queries, in-flight
	// merges, part-count hotspots, replication queue lag. Admin
	// only — same gate the rest of /admin/* uses.
	mux.HandleFunc("GET /api/admin/clickhouse", auth.RequireRole(auth.RoleAdmin, s.getClickHouseHealth))
	// v0.6.8 — admin-only CH query AI optimizer. Operator pastes
	// raw SQL; server runs it through the Copilot with the
	// SystemPromptCHQueryOptimize template that knows the MV
	// catalogue + the hard-constraint checklist. Returns
	// {optimized, explanation}.
	mux.HandleFunc("POST /api/admin/clickhouse/optimize-query", auth.RequireRole(auth.RoleAdmin, s.requireCopilot(s.copilotOptimizeCHQuery)))
	// v0.9.494 — koordinatör dağılımı: hangi CH node'u kaç sorgunun
	// giriş noktası oldu (is_initial_query üzerinden, clusterAllReplicas
	// fan-out'uyla). Okuma havuzu dilimlerinin öncesi/sonrası kabul
	// ölçüsü; ayrı uç + 60s TTL çünkü gövde okumasından çok daha geniş
	// bir satır kümesini grupluyor.
	mux.HandleFunc("GET /api/admin/clickhouse/coordinators", auth.RequireRole(auth.RoleAdmin, s.getCHCoordinatorSpread))
	// v0.9.1191 — Distributed spool runbook'u (spool_actions.go): durum
	// okuması + iki adlandırılmış SYSTEM eylemi. /admin/sql salt-okunur
	// kalır; bunlar serbest SQL değil, doğrulanmış-hedefli sabit komutlar.
	mux.HandleFunc("GET /api/admin/clickhouse/spool", auth.RequireRole(auth.RoleAdmin, s.getSpoolState))
	mux.HandleFunc("POST /api/admin/clickhouse/spool/flush", auth.RequireRole(auth.RoleAdmin, s.postSpoolFlush))
	mux.HandleFunc("POST /api/admin/clickhouse/spool/start-sends", auth.RequireRole(auth.RoleAdmin, s.postSpoolStartSends))
	// v0.9.543 — node iş dağılımı: CPU · merge · insert · fetch host
	// başına, HAM kümülatif (pencereyi istemci delta ile açar).
	mux.HandleFunc("GET /api/admin/clickhouse/nodework", auth.RequireRole(auth.RoleAdmin, s.getCHNodeWork))
	// v0.9.613 — dağıtık DDL kuyruğu teşhisi (üç gecelik prod vakası,
	// ch_ddl_queue.go). registerXxxRoutes deseni.
	s.registerCHDDLQueueRoutes(mux)
	// v0.9.770 — rollup kurulum sihirbazı: 0001+0003 migration'ları
	// /admin/clickhouse'tan, ön kontrollü. Boot'ta ASLA koşmaz —
	// tek tetikleyici admin (gerekçe: admin_rollup.go başlığı).
	s.registerRollupAdminRoutes(mux)
	s.registerEntityLayerAdminRoutes(mux)  // v0.10.134 — 0011 entity katmanı şeması sihirbazı, admin_entity_layer.go
	s.registerRolloutLayerAdminRoutes(mux) // v0.10.197 — 0012 rollouts katmanı şeması sihirbazı, admin_rollout_layer.go
	s.registerStateUnifyRoutes(mux)
	s.registerStateRepartRoutes(mux)
	s.registerTraceBackfillRoutes(mux) // v0.10.103 — /traces tarihçe sihirbazı, admin_trace_backfill.go
	s.registerSchemaCatalogRoutes(mux) // v0.10.115 — şema kataloğu, schema_catalog.go
	mux.HandleFunc("GET /api/correlations", s.getCorrelations)
	// v0.9.135 (scale-audit 2026-07-20) — admin-only (Redis internals);
	// only AdminStats reads it, handler had no role check.
	mux.HandleFunc("GET /api/admin/redis-stats", auth.RequireRole(auth.RoleAdmin, s.getRedisStats))
	mux.HandleFunc("GET /api/admin/cache-stats", auth.RequireRole(auth.RoleAdmin, s.getCacheStats))
	mux.HandleFunc("GET /api/admin/cardinality", auth.RequireRole(auth.RoleAdmin, s.getCardinality))
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
		mux.HandleFunc("GET /api/mcp/sse", s.mcp.HandleSSE)
		mux.HandleFunc("POST /api/mcp/messages", s.mcp.HandleMessage)
		// v0.9.14 — Streamable-HTTP (2025-03-26), stateless: Claude
		// Code'un birincil `--transport http` yolu; session'sız olduğu
		// için çok-pod LB'de afinite gerektirmez (audit EK BULGU'nun
		// kökten çözümü). SSE yolu eski istemciler için aynen kalır.
		mux.HandleFunc("POST /api/mcp", s.mcp.HandleStreamable)
	}
	mux.HandleFunc("GET /api/services/{name}/bundle", s.getServiceBundle)
	mux.HandleFunc("GET /api/services/{name}/structure", s.getServiceStructure)
	mux.HandleFunc("GET /api/services/{name}/clusters", s.getServiceClusterBreakdown)
	mux.HandleFunc("GET /api/services/{name}/metric-throughput", s.getServiceMetricThroughput) // v0.9.665 — job etiketinden metrik türevli throughput
	// v0.8.383 — per-service env chips (Service header Envs group).
	mux.HandleFunc("GET /api/services/{name}/environments", s.getServiceEnvironments)
	mux.HandleFunc("GET /api/services/{name}/neighbors", s.getServiceNeighbors)
	// v0.6.29 — Service dependency impact scorer ("blast
	// radius"). Open Problem on service X → which OTHER
	// services are seeing broken calls because they invoke X?
	// Surfaced as a chip on the /problems row + tooltip with
	// top N cascading callers.
	mux.HandleFunc("GET /api/services/{name}/blast-radius", s.getServiceBlastRadius)
	mux.HandleFunc("GET /api/topology", s.getTopology)
	mux.HandleFunc("GET /api/topology/ops", s.getTopologyOps)
	mux.HandleFunc("GET /api/topology/service", s.getServiceTopology)
	mux.HandleFunc("GET /api/topology/flows", s.getRootFlows)
	mux.HandleFunc("GET /api/topology/flow", s.getFlowTopology)
	mux.HandleFunc("GET /api/topology/hidden", s.getTopologyHidden)
	mux.HandleFunc("PUT /api/topology/hidden", auth.RequireAnyRole(editorRoles, s.putTopologyHidden))
	mux.HandleFunc("GET /api/topology/drawio", s.exportTopologyDrawIO)
	mux.HandleFunc("GET /api/topology/edge/instances", s.getTopologyEdgeInstances)
	mux.HandleFunc("POST /api/slos/autocreate", auth.RequireRole(auth.RoleAdmin, s.autoCreateSLOs))
	mux.HandleFunc("GET /api/topology/service/drawio", s.exportServiceTopologyDrawIO)
	mux.HandleFunc("GET /api/topology/flow/drawio", s.exportFlowTopologyDrawIO)
	mux.HandleFunc("GET /api/service-map", s.getServiceMap)
	mux.HandleFunc("GET /api/endpoints", s.getEndpoints)
	mux.HandleFunc("GET /api/endpoints/downstream", s.getEndpointDownstream)
	mux.HandleFunc("GET /api/endpoints/callers", s.getEndpointCallers)
	mux.HandleFunc("GET /api/services/{name}/attrs", s.getServiceAttrs)
	mux.HandleFunc("GET /api/databases", s.getDatabases)
	mux.HandleFunc("GET /api/databases/trends", s.getDBTrends)
	mux.HandleFunc("GET /api/databases/detail", s.getDatabaseDetail)
	// Cross-service slow-query catalog (v0.5.165). One row per
	// (service, normalised statement) ordered by total wall-clock
	// time — what's actually worth optimising globally.
	mux.HandleFunc("GET /api/databases/slow-queries", s.getSlowQueriesGlobal)
	mux.HandleFunc("GET /api/databases/oracle", s.getOracleMetrics)
	mux.HandleFunc("GET /api/databases/postgres", s.getPostgresMetrics)
	mux.HandleFunc("GET /api/databases/mysql", s.getMySQLMetrics)
	mux.HandleFunc("GET /api/databases/redis", s.getRedisMetrics)
	mux.HandleFunc("GET /api/messaging", s.getMessaging)
	// v0.9.434 — /messaging satır-içi trend (getDBTrends paritesi).
	mux.HandleFunc("GET /api/messaging/trends", s.getMessagingTrends)
	mux.HandleFunc("GET /api/messaging/detail", s.getMessagingDetail)
	mux.HandleFunc("GET /api/services/{name}/backtrace", s.getServiceBacktrace)
	mux.HandleFunc("GET /api/services/{name}/infra", s.getServiceInfraMetrics)
	mux.HandleFunc("GET /api/services/{name}/instances", s.getServiceInstances)
	mux.HandleFunc("GET /api/services/{name}/runtime", s.getServiceRuntime)
	mux.HandleFunc("GET /api/services/{name}/db-queries", s.getServiceDBQueries)
	mux.HandleFunc("GET /api/services/{name}/deploys", s.getServiceDeploys)
	// Deploy history with impact deltas — drives the Service
	// detail page's "Recent deploys" panel (v0.5.189).
	mux.HandleFunc("GET /api/services/{name}/deploy-history", s.getDeployHistory)
	// Pod-churn rollouts — instance-set turnover events (replaces
	// version-bump markers when service.version is constant). v0.8.x.
	mux.HandleFunc("GET /api/services/{name}/rollouts", s.getServiceRollouts)
	mux.HandleFunc("GET /api/services/{name}/metadata", s.getServiceMetadata)
	mux.HandleFunc("PUT /api/services/{name}/metadata", auth.RequireAnyRole(editorRoles, s.putServiceMetadata))
	mux.HandleFunc("GET /api/services-metadata", s.listServiceMetadata)
	mux.HandleFunc("GET /api/services-runtimes", s.getAllServiceRuntimes)
	mux.HandleFunc("GET /api/services/graph", s.getServiceGraph)
	// v0.8.10 — OTel-native service graph (topology rebuild Stage 1). Compact
	// {nodes,edges} from topology_edges_5m MV; ?focus=&scope=neighborhood|global.
	mux.HandleFunc("GET /api/servicegraph", s.getOtelServiceGraph)
	mux.HandleFunc("GET /api/services/sparklines", s.getServiceSparklines)
	mux.HandleFunc("GET /api/service-names", s.getServiceNames)
	mux.HandleFunc("GET /api/operation-names", s.getOperationNames)
	mux.HandleFunc("GET /api/attribute-keys", s.getAttributeKeys)
	mux.HandleFunc("GET /api/attribute-values", s.getAttributeValues)
	mux.HandleFunc("GET /api/operations", s.getOperations)
	mux.HandleFunc("GET /api/traces", s.getTraces)
	// CSV export — same filter shape as /api/traces but streams
	// the result as a downloadable CSV. Cap raised to 10k rows
	// (vs the UI's 50/page) since auditors / postmortem flows
	// usually want a fuller slice. Content-Disposition is set
	// so the browser triggers a download rather than rendering.
	mux.HandleFunc("GET /api/traces/export.csv", s.exportTracesCSV)
	mux.HandleFunc("GET /api/traces/aggregate", s.getTraceAggregate)
	mux.HandleFunc("GET /api/traces/count", s.getTracesCount)
	// v0.8.x (Gap 3) — span-relationship / structural trace operators.
	// Two predicate sets (parent / child) + a kind (child-of /
	// descendant-of / sequence) + direct-only flag → a BOUNDED self-join
	// over raw spans (the one legitimate MV bypass) resolving a
	// LIMIT-capped trace-id list, then the existing GetTraces page fetch.
	// Read-only (viewer baseline); serveCached 30s with all-input key.
	// v0.5.264 — trace shape clustering. Groups traces by their
	// sorted-unique (service, operation) fingerprint; surfaces
	// the dominant call-pattern cohorts. Sample-based so the
	// query stays under the 30s ceiling at billion-span scale.
	mux.HandleFunc("GET /api/traces/shapes", s.getTraceShapes)
	// v0.5.265 — Unified query language (DQL-lite). Operator
	// admin-types pipe-shape queries; backend compiles to a
	// chstore Plan + executes via the same hot path the UI
	// builders use.
	mux.HandleFunc("POST /api/query/run", auth.RequireRole(auth.RoleAdmin, s.runDQL))
	// Public-share endpoints — POST mints a token (any authenticated
	// user, viewers included: v0.8.102 operator request — viewers
	// hand traces to support/vendors too; the mint is audited with
	// the actor's email so a leak is traceable), GET resolves without
	// auth (auth middleware's SkipPath allowlist lets it through),
	// DELETE revokes (editor+ so a viewer can't nuke other operators'
	// active shares — minting your own link is fine, deleting the
	// shared pool isn't).
	mux.HandleFunc("POST /api/traces/{id}/share", s.createTraceSnapshot)
	mux.HandleFunc("GET  /api/traces/{id}/shares", s.listTraceSnapshots)
	mux.HandleFunc("DELETE /api/traces/share/{token}", auth.RequireAnyRole(editorRoles, s.revokeTraceSnapshot))
	mux.HandleFunc("GET  /api/public/trace/{token}", s.getPublicTrace)
	// /api/logs rotaları logs_routes.go'da (v0.10.281, defter kaydı).
	mux.HandleFunc("GET /api/metrics/names", s.getMetricNames)
	mux.HandleFunc("GET /api/metrics", s.getMetrics)
	mux.HandleFunc("GET /api/metrics/query", s.queryMetric)
	// v0.9.11x (F4 Phase 2) — PromQL range query over the OTel metric store.
	mux.HandleFunc("GET /api/metrics/promql", s.queryPromQL)
	// v0.8.53 (doorway D4) — server-side descriptor resolution. A
	// MetricQuery descriptor rides as ?m=<base64url(JSON)> (the same
	// codec the frontend deep links use) and the resolver picks the
	// spanmetrics tier / tracemetrics path, applies the formula, and
	// returns series + optional exemplars. ONE place descriptor→CH.
	mux.HandleFunc("GET /api/metrics/resolve", s.resolveMetric)
	mux.HandleFunc("GET /api/metrics/histogram", s.getMetricHistogram)
	// v0.5.350 — span-metrics-derived per-service RED. Lets
	// operators whose collectors emit spanmetrics (traces.
	// spanmetrics.calls.total / duration) see the metric
	// stream as a first-class APM surface, side-by-side with
	// the span-derived RED on /services.
	mux.HandleFunc("GET /api/metrics/labels", s.getMetricLabelValues)
	// v0.9.771 — the key half of /api/metrics/labels (which answers values).
	// Same open posture as its sibling: a label-name list is no more sensitive
	// than the metric catalogue the picker already serves.
	mux.HandleFunc("GET /api/metrics/attr-keys", s.getMetricAttrKeys)
	mux.HandleFunc("GET /api/spans/metric", s.spanMetric)
	mux.HandleFunc("POST /api/spans/metric-batch", s.spanMetricBatch)
	mux.HandleFunc("POST /api/dashboards/data", s.dashboardsData)
	mux.HandleFunc("GET /api/spans/repeats", s.spanRepeats)
	mux.HandleFunc("GET /api/spans/exemplar", s.spanExemplar)
	// Correlated Signals (task #6) — one cross-signal pivot bundle (trace ↔ logs
	// ↔ metrics, joined on trace_id → service.name → window). Read-only, open
	// (writes no state; same posture as /api/correlations, /api/problems). Pure
	// orchestration over existing reads — no new CH query.
	mux.HandleFunc("GET /api/correlate/context", s.getCorrelationContext)
	s.registerPivotRoutes(mux)
	s.registerRAGRoutes(mux)
	s.registerAPITokenRoutes(mux)        // v0.8.330 — cross-signal pivot query layer (exemplars / trace links / window metrics), pivot.go
	s.registerEndpointsDetailRoutes(mux) // v0.8.360 — /endpoints detail drill-down (histogram / status / exceptions / failing traces / split), endpoints_detail.go
	s.registerDBStmtDetailRoutes(mux)    // v0.8.378 — /slow-queries statement drill-down (trend / callers / exemplars / compare), dbstmt_detail.go
	s.registerExternalRoutes(mux)        // v0.8.446 — /external third-party API inventory from topology_edges_5m (Wave 3 / A1), external.go
	s.registerHostRoutes(mux)            // v0.8.449 — /hosts host/pod inventory from metric_points (Wave 3 / A4), hosts.go
	s.registerLdapGroupSyncRoutes(mux)   // v0.8.526 — LDAP/AD group-membership sync (summary / sync-now / preview), ldap_groupsync.go
	s.registerRolloutRoutes(mux)         // v0.10.200 — rollouts olay tablosu + agregat + ayar + tail, rollouts.go
	mux.HandleFunc("GET /api/spans/heatmap", s.spanHeatmap)
	mux.HandleFunc("GET /api/spans/bubbleup", s.spanBubbleUp)
	mux.HandleFunc("GET /api/profiles", s.listProfiles)
	mux.HandleFunc("GET /api/profiles/by-span", s.profilesForSpan)
	mux.HandleFunc("GET /api/profiles/by-span/hotspots", s.profileHotspotsForSpan)
	mux.HandleFunc("GET /api/profiles/hotspots", s.profileHotspots)
	mux.HandleFunc("GET /api/profiles/{id}", s.getProfile)
	// Errors Inbox — stateful exception groups (read = any, write = admin)
	mux.HandleFunc("GET    /api/exception-groups", s.listExceptionGroups)
	mux.HandleFunc("GET    /api/exception-groups/{fp}", s.getExceptionGroup)
	mux.HandleFunc("GET    /api/exception-groups/{fp}/samples", s.getExceptionGroupSamples)
	mux.HandleFunc("GET    /api/exception-groups/{fp}/occurrences", s.getExceptionGroupOccurrences)
	mux.HandleFunc("POST   /api/exception-groups/{fp}/state", auth.RequireAnyRole(editorRoles, s.setExceptionGroupState))
	// v0.9.252 — bulk sibling of the route above. Same gate, same
	// invalidations, ONE audit row for the whole batch.
	mux.HandleFunc("POST   /api/exception-groups/state", auth.RequireAnyRole(editorRoles, s.setExceptionGroupStateBulk))
	mux.HandleFunc("POST   /api/exception-groups/{fp}/assign", auth.RequireAnyRole(editorRoles, s.assignExceptionGroup))
	mux.HandleFunc("GET    /api/services/{name}/operations", s.svcOperationSummary)
	mux.HandleFunc("GET    /api/services/{name}/span-breakdown", s.svcSpanBreakdown)
	mux.HandleFunc("GET    /api/problems", s.listProblems)
	mux.HandleFunc("GET    /api/problems/count", s.countProblems)
	// v0.9.1109 — /alerts satır rozeti: kural başına açık problem.
	mux.HandleFunc("GET    /api/problems/rule-counts", s.countProblemsByRule)
	// v0.9.550 — evaluator kalp atışı. /api/problems ile AYNI yetki
	// duruşu (rol kapısı yok): boş bir Problems sayfasının "sorun yok"
	// mu yoksa "evaluator ölü" mü olduğunu görmek her rolün hakkı.
	// Admin'e kilitlemek, viewer'ı tam da bu ayrımdan mahrum bırakırdı.
	mux.HandleFunc("GET    /api/problems/evaluator", s.getEvaluatorHealth)
	mux.HandleFunc("GET    /api/problems/buckets", s.listProblemBuckets)
	// v0.9.825 — bildirim derin linkinin tekil okuma ucu. Liste "en yeni
	// 200" penceresiydi; çözülmüş bir problem oradan düşünce e-postadaki
	// bağlantı "not found" diyordu (gerekçe: problem_by_id.go).
	//
	// Rota sırası ÖNEMSİZ: Go 1.22 ServeMux'ta literal segment joker'i
	// yener, yani yukarıdaki /count · /evaluator · /buckets bu desene
	// DÜŞMEZ. Yetki de liste ucuyla aynı (rol kapısı yok) — aynı veriyi
	// iki farklı kurala bağlamamak için.
	mux.HandleFunc("GET    /api/problems/{id}", s.getProblemByID)
	s.registerProblemNotificationRoutes(mux) // v0.9.1344 — bu probleme kim haber aldı (ve kimse almadıysa o), problem_notifications.go
	s.registerDeploymentReportRoutes(mux)    // v0.10.79 — deploy analiz raporu (PR #31), deployment_report.go
	mux.HandleFunc("GET    /api/problems/{id}/rootcause", s.getProblemRootCause)
	// Copilot prose narration of the persisted problem hypothesis (rc #4) —
	// problem-anchored sibling of the anomaly explain route. Lazy/opt-in,
	// version-keyed cache, routes through s.copilotExplain. The 5-segment
	// {id}/rootcause/explain out-ranks the 4-segment {id}/rootcause — no collision.
	mux.HandleFunc("GET    /api/problems/{id}/rootcause/explain", s.getProblemRootCauseExplain)
	// v0.9.1281 — KALICI verdict okuma. /rootcause/explain'in ÜRETİMSİZ
	// ikizi: model çağırmaz, rca_verdicts satırını okur. Ankor türü query
	// parametresinde (problem|anomaly) çünkü tek uç iki ankoru da
	// karşılıyor — kardeş explain uçlarının aksine burada anchor kind bir
	// yol segmenti olmayı hak edecek kadar davranış değiştirmiyor.
	// Viewer-okunur; salt okuma olduğu için rol kapısı yok (kardeş
	// explain uçlarıyla aynı duruş).
	mux.HandleFunc("GET    /api/rootcause/verdict", s.getRootCauseVerdict)
	mux.HandleFunc("POST   /api/problems/acknowledge", auth.RequireAnyRole(editorRoles, s.acknowledgeProblems))
	mux.HandleFunc("PATCH  /api/problems/{id}/assignee", auth.RequireAnyRole(editorRoles, s.setProblemAssignee))
	// Unified triage inbox (v0.5.211) — merges Problems +
	// Exception groups + Anomaly events with a normalised
	// priority blend so operators stop tab-hopping.
	mux.HandleFunc("GET    /api/inbox", s.inbox)
	mux.HandleFunc("GET    /api/inbox/count", s.inboxCount)
	mux.HandleFunc("GET    /api/alert-rules", s.listAlertRules)
	mux.HandleFunc("POST   /api/alert-rules", auth.RequireAnyRole(editorRoles, s.createAlertRule))
	mux.HandleFunc("PUT    /api/alert-rules/{id}", auth.RequireAnyRole(editorRoles, s.updateAlertRule))
	mux.HandleFunc("DELETE /api/alert-rules/{id}", auth.RequireAnyRole(editorRoles, s.deleteAlertRule))
	mux.HandleFunc("POST   /api/alert-rules/{id}/enable", auth.RequireAnyRole(editorRoles, s.enableAlertRule))
	mux.HandleFunc("POST   /api/alert-rules/{id}/disable", auth.RequireAnyRole(editorRoles, s.disableAlertRule))
	mux.HandleFunc("GET    /api/alert-rules/baseline", auth.RequireAnyRole(editorRoles, s.getAlertBaseline))
	// ES Watcher import (Faz-1) — raw PUT _watcher/watch body →
	// projected alert rule (Metric="watcher") with a field-by-field
	// mapping report; dryRun previews without persisting. Same gate
	// as the alert-rule CRUD it feeds.
	mux.HandleFunc("POST   /api/watchers/import", auth.RequireAnyRole(editorRoles, s.importWatcher))
	// /watchers page reads (v0.9.196) — per-rule problems rollup for
	// the list (ONE bounded GROUP BY, never N per-row calls) + one
	// rule's fire/notify/resolve history for the drawer. Read-only,
	// any signed-in role (viewers see watcher state read-only). The
	// literal /summary segment out-ranks {id} in the Go 1.22 mux.
	mux.HandleFunc("GET    /api/watchers/summary", s.watchersSummary)
	mux.HandleFunc("GET    /api/watchers/{id}/history", s.watcherHistory)
	// Runbooks (v0.7.0) — operator-authored executable procedures.
	// GET list/detail open so viewers see them read-only (invariant #7);
	// every write gated to editor+. Executions + agent dispatch land next.
	mux.HandleFunc("GET    /api/runbooks", s.listRunbooks)
	mux.HandleFunc("POST   /api/runbooks", auth.RequireAnyRole(editorRoles, s.createRunbook))
	mux.HandleFunc("GET    /api/runbooks/{id}", s.getRunbook)
	mux.HandleFunc("PUT    /api/runbooks/{id}", auth.RequireAnyRole(editorRoles, s.updateRunbook))
	mux.HandleFunc("DELETE /api/runbooks/{id}", auth.RequireAnyRole(editorRoles, s.deleteRunbook))
	mux.HandleFunc("POST   /api/runbooks/{id}/enable", auth.RequireAnyRole(editorRoles, s.enableRunbook))
	mux.HandleFunc("POST   /api/runbooks/{id}/disable", auth.RequireAnyRole(editorRoles, s.disableRunbook))
	// Runbook executions (v0.7.0) — a run is the audit record. List/detail
	// open (read-only audit for viewers); start/step/cancel gated to editor+.
	// Literal /executions segments out-rank the {id} wildcard in the Go 1.22
	// mux, so they match before /api/runbooks/{id}.
	mux.HandleFunc("POST   /api/runbooks/{id}/execute", auth.RequireAnyRole(editorRoles, s.executeRunbook))
	mux.HandleFunc("GET    /api/runbooks/executions", s.listExecutions)
	mux.HandleFunc("GET    /api/runbooks/executions/{id}", s.getExecution)
	mux.HandleFunc("POST   /api/runbooks/executions/{id}/steps/{stepId}", auth.RequireAnyRole(editorRoles, s.execStepAction))
	mux.HandleFunc("POST   /api/runbooks/executions/{id}/cancel", auth.RequireAnyRole(editorRoles, s.cancelExecution))
	mux.HandleFunc("GET /api/health", s.getHealth)
	// v0.8.339 (HA audit H3) — liveness split from readiness. /api/health
	// 503s on overload BY DESIGN (pull the pod from the LB to drain);
	// pointing the LIVENESS probe at it made kubelet KILL overloaded pods
	// — destroying every buffered item and crash-looping through the
	// backlog. /livez answers one question only: is the process alive.
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// Alias for the k8s readinessProbe convention. Same body +
	// same 503-on-overload behaviour so a `httpGet: { path:
	// /healthz }` probe pulls overloaded pods out of the
	// Service endpoints set automatically.
	mux.HandleFunc("GET /healthz", s.getHealth)
	mux.HandleFunc("GET /api/version", s.getVersion)
	// Branding overlay — public GET so the login page can read it
	// before authentication; PUT is admin-only and capped at 256KB
	// to keep the logo data URI from bloating the settings table.
	s.registerAnnouncementRoutes(mux) // v0.8.486 — sayfa-üstü duyuru şeridi (Settings'ten), announcement.go
	mux.HandleFunc("GET /api/branding", s.getBranding)
	mux.HandleFunc("PUT /api/branding", auth.RequireRole(auth.RoleAdmin, s.putBranding))
	mux.HandleFunc("GET /api/anomalies/log-patterns", s.getLogPatternAnomalies)
	mux.HandleFunc("GET /api/anomalies/trace-ops", s.getTraceOpAnomalies)
	mux.HandleFunc("GET /api/anomalies/metric", s.getMetricAnomalies)
	mux.HandleFunc("GET /api/anomalies/events", s.getAnomalyEvents)
	// v0.9.471 — ?id= sorgu paramı, path segmenti DEĞİL: Go 1.22 ServeMux
	// "GET /api/anomalies/events/{id}" ile "GET /api/anomalies/{id}/rootcause"
	// kalıplarını çakıştırıp BOOT'ta panic'liyor (ikisi de
	// /api/anomalies/events/rootcause'u eşler, hiçbiri daha spesifik
	// değil). v0.9.465-470 tag'leri bu yüzden açılamadı — testler mux'u
	// hiç kurmadığından sınıf yakalanmıyordu; TestMuxRoutePatterns artık
	// kuruyor.
	mux.HandleFunc("GET /api/anomalies/event", s.getAnomalyEvent)
	// Anomaly-anchored root cause (v0.8.x, release #1) — same parallel
	// soft-fail fan-out as /api/problems/{id}/rootcause but anchored on an
	// AnomalyEvent window. Read-only, open (writes no state; same posture
	// as the problem-rootcause route and /api/anomalies). The 4-segment
	// {id}/rootcause shape doesn't collide with the literal sibling routes
	// above (log-patterns/trace-ops/metric/events are 3-segment leaves).
	mux.HandleFunc("GET /api/anomalies/{id}/rootcause", s.getAnomalyRootCause)
	// Optional Copilot PROSE narration on top of the deterministic ranking
	// (v0.8.x, release #4). Lazy + opt-in (frontend ✨ Explain button fetches
	// on click). Reads the PERSISTED hypothesis (GetHypothesis), routes through
	// s.copilotExplain for /ai attribution, serveCached keyed on the hypothesis
	// version. Viewer-readable; no audit (the copilotExplain wrapper records the
	// ai_calls row). The 5-segment {id}/rootcause/explain shape is MORE specific
	// than the 4-segment {id}/rootcause above, so Go 1.22's mux matches it first
	// — no collision (audit CHECK 7).
	mux.HandleFunc("GET /api/anomalies/{id}/rootcause/explain", s.getAnomalyRootCauseExplain)
	// Cmd-K palette autocomplete for the "silence anomaly" action
	// (v0.5.459). v0.9.1136 (AI Faz 3.1, A7) — editor kapısı KALDIRILDI,
	// viewer okuyabiliyor. Üç gerekçe, hepsi doğrulandı:
	//   1. invariant #7 — viewer state'i GÖRÜR, yazmaz;
	//   2. tek anlamlı sonraki adım olan silence YAZMASI ayrı ve kendi
	//      başına editor-kapılı (aşağıdaki POST/DELETE/bulk-delete) —
	//      okumayı da kapamak hiçbir şey korumuyordu;
	//   3. AYNI satırlar zaten kapısız iki yoldan görünüyordu:
	//      GET /api/anomalies/events ve MCP list_anomalies (ikisi de
	//      ListAnomalyEvents okur). Yani kapı yalnız Cmd-K paletini
	//      viewer'a bozuyordu, veriyi gizlemiyordu.
	// Böylece REST↔MCP sapma tablosu SIFIRA indi: list_anomalies
	// MinRole "" kalır (mcptools/tools.go).
	mux.HandleFunc("GET /api/anomalies/active", s.listActiveAnomalies)
	// Anomaly silencing — editor+ can mute/unmute (invariant #7:
	// viewer = read-only everywhere). A silence suppresses a signal
	// for EVERY operator, so it's a state mutation, not a personal
	// preference. GET stays open so viewers see the muted strip
	// read-only; the frontend hides the mute/unmute buttons for
	// viewers (features/anomalies/streams.tsx) so they never click
	// into a 403.
	mux.HandleFunc("GET    /api/anomalies/silences", s.listAnomalySilences)
	mux.HandleFunc("POST   /api/anomalies/silences", auth.RequireAnyRole(editorRoles, s.createAnomalySilence))
	mux.HandleFunc("DELETE /api/anomalies/silences/{id}", auth.RequireAnyRole(editorRoles, s.deleteAnomalySilence))
	s.registerAnomalyVerdictRoutes(mux) // v0.10.181 — «anomali / değil» kararı, anomaly_verdicts.go
	mux.HandleFunc("POST   /api/anomalies/silences/bulk-delete", auth.RequireAnyRole(editorRoles, s.bulkDeleteAnomalySilences))
	// Audit log — admin-only read.
	mux.HandleFunc("GET /api/admin/audit", s.listAuditLog)
	mux.HandleFunc("GET /api/admin/alert-tuning/noisy-rules", s.alertTuningNoisyRules)
	mux.HandleFunc("GET /api/admin/audit/export", s.exportAuditLog)
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
	mux.HandleFunc("POST /api/admin/config/diff", auth.RequireRole(auth.RoleAdmin, s.diffConfig))
	// SQL playground — admin only; readonly=2 + 60s cap on the
	// CH side, allow-list of SELECT/WITH/SHOW/DESCRIBE/EXPLAIN
	// on the application side.
	// v0.5.297 — admin-only route gates. Both handlers had inline
	// auth checks, but the middleware wrapper is the convention
	// across the rest of the admin surface (catches the route at
	// registration time so a future refactor can't accidentally
	// drop the inline check).
	mux.HandleFunc("POST /api/admin/sql/query", auth.RequireRole(auth.RoleAdmin, s.execSQL))
	mux.HandleFunc("GET  /api/admin/sql/schema", auth.RequireRole(auth.RoleAdmin, s.sqlSchema))
	mux.HandleFunc("POST /api/admin/sql/elastic", auth.RequireRole(auth.RoleAdmin, s.execElasticSQL))
	// v0.5.466 — ES index inventory: name, docs, size, health,
	// ILM phase/policy. Admin-only (read of cluster metadata).
	mux.HandleFunc("GET  /api/admin/elastic/indices", auth.RequireRole(auth.RoleAdmin, s.adminElasticIndices))
	mux.HandleFunc("GET  /api/admin/elastic/errors", auth.RequireRole(auth.RoleAdmin, s.adminElasticErrors))
	// v0.8.348 — pivot Phase 1c: trace-context SELF-DISCOVERY. The
	// system verifies its own configured logs backend (trace-id
	// field mapping verdict + 24h coverage) — /admin/elastic card
	// + /admin/stats snapshot line. Admin-only, 5m cached.
	mux.HandleFunc("GET  /api/admin/logstore/trace-context", auth.RequireRole(auth.RoleAdmin, s.adminLogstoreTraceContext))
	// v0.8.407 — same trace-context report, viewer-safe: the Trace
	// page's empty Logs tab explains WHY it's empty (service ships
	// logs without a trace field vs no logs at all). Read-only
	// diagnostics; shares the admin handler + its 5-min serveCached
	// key, so the second route adds zero backend load.
	mux.HandleFunc("GET  /api/logstore/trace-context", s.adminLogstoreTraceContext)
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
	mux.HandleFunc("GET    /api/operator-events", s.listEvents)
	mux.HandleFunc("POST   /api/operator-events", auth.RequireAnyRole(editorRoles, s.createEvent))
	mux.HandleFunc("DELETE /api/operator-events/{id}", auth.RequireAnyRole(editorRoles, s.deleteEvent))
	// v0.8.241 — notification dispatch history (email/slack/teams/
	// zoom/webhook/whatsapp sends, success + failure). Read-only;
	// any signed-in role (targets are masked at write time).
	mux.HandleFunc("GET    /api/notifications/log", s.listNotificationLog)
	// Saved views — per-user CRUD (server scopes by session).
	mux.HandleFunc("GET    /api/views", s.listSavedViews)
	mux.HandleFunc("POST   /api/views", s.createSavedView)
	mux.HandleFunc("DELETE /api/views/{id}", s.deleteSavedView)
	// AI observability (v0.5.163) — admin-only since prompts and
	// response samples can contain telemetry the viewer role might
	// not otherwise have access to. Tighten further (per-team) if
	// the operator surface ever sends third-party data into prompts.
	// AI gözlemlenebilirliği + AI ayarları + copilot uçları →
	// ai_routes.go (v0.9.1117, AI Assistant Faz 0.1). Yeni AI ucu
	// ORAYA eklenir, api.go'ya değil.
	s.registerAIRoutes(mux)
	mux.HandleFunc("GET /api/status", s.getStatus)

	// ── Public status page ────────────────────────────────────────
	// Read-only unauth: anyone with the URL can see status + subscribe.
	mux.HandleFunc("GET  /api/public-status", s.publicStatus)
	mux.HandleFunc("POST /api/public-status/subscribe", s.publicStatusSubscribe)
	mux.HandleFunc("GET  /api/public-status/confirm", s.publicStatusConfirm)
	// Admin-gated config + subscriber management.
	mux.HandleFunc("GET    /api/status-page/config", auth.RequireRole(auth.RoleAdmin, s.statusPageGetConfig))
	mux.HandleFunc("PUT    /api/status-page/config", auth.RequireRole(auth.RoleAdmin, s.statusPagePutConfig))
	mux.HandleFunc("GET    /api/status-page/components", auth.RequireRole(auth.RoleAdmin, s.statusPageListComponents))
	mux.HandleFunc("POST   /api/status-page/components", auth.RequireRole(auth.RoleAdmin, s.statusPageCreateComponent))
	mux.HandleFunc("PUT    /api/status-page/components/{id}", auth.RequireRole(auth.RoleAdmin, s.statusPageUpdateComponent))
	mux.HandleFunc("DELETE /api/status-page/components/{id}", auth.RequireRole(auth.RoleAdmin, s.statusPageDeleteComponent))
	mux.HandleFunc("GET    /api/status-page/subscribers", auth.RequireRole(auth.RoleAdmin, s.statusPageListSubscribers))
	mux.HandleFunc("DELETE /api/status-page/subscribers", auth.RequireRole(auth.RoleAdmin, s.statusPageDeleteSubscriber))
	mux.HandleFunc("PUT    /api/status-page/incidents/{id}/publish", auth.RequireRole(auth.RoleAdmin, s.statusPagePublishIncident))
	// v0.8.196 — "factory reset" of observability data (admin-only, audited).
	mux.HandleFunc("POST   /api/admin/purge-telemetry", auth.RequireRole(auth.RoleAdmin, s.purgeTelemetry))

	// ── Runtime settings (admin) ───────────────────────────────────
	mux.HandleFunc("GET /api/settings/retention", auth.RequireRole(auth.RoleAdmin, s.getRetention))
	mux.HandleFunc("PUT /api/settings/retention", auth.RequireRole(auth.RoleAdmin, s.putRetention))
	mux.HandleFunc("GET /api/settings/anomaly-promotion", auth.RequireRole(auth.RoleAdmin, s.getAnomalyPromotion))
	mux.HandleFunc("PUT /api/settings/anomaly-promotion", auth.RequireRole(auth.RoleAdmin, s.putAnomalyPromotion))
	// v0.9.800 — anomali dedektörünün İZLEDİĞİ metrik seti. Ayrı
	// anahtar: anomaly_promotion sinyalin Problem'e TERFİSİNİ ayarlar,
	// bu ise sinyalin hiç ÖLÇÜLÜP ölçülmeyeceğini. request_rate
	// varsayılan kapalı (operatör: false-pozitif).
	mux.HandleFunc("GET /api/settings/anomaly-tracked", auth.RequireRole(auth.RoleAdmin, s.getAnomalyTracked))
	mux.HandleFunc("PUT /api/settings/anomaly-tracked", auth.RequireRole(auth.RoleAdmin, s.putAnomalyTracked))
	// v0.9.826 — dedektörün EŞİKLERİ (anomaly-tracked'in kardeşi: o
	// hangi metriğin ölçüleceğini, bu ölçülenin ne zaman olay
	// sayılacağını ayarlar).
	mux.HandleFunc("GET /api/settings/anomaly-sensitivity", auth.RequireRole(auth.RoleAdmin, s.getAnomalySensitivity))
	mux.HandleFunc("PUT /api/settings/anomaly-sensitivity", auth.RequireRole(auth.RoleAdmin, s.putAnomalySensitivity))
	mux.HandleFunc("GET /api/settings/runtime-alerts", auth.RequireRole(auth.RoleAdmin, s.getRuntimeAlerts))
	mux.HandleFunc("PUT /api/settings/runtime-alerts", auth.RequireRole(auth.RoleAdmin, s.putRuntimeAlerts))
	// v0.9.1279 — self-health alarm ailesinin eşikleri (self_health blobu).
	mux.HandleFunc("GET /api/settings/self-health", auth.RequireRole(auth.RoleAdmin, s.getSelfHealth))
	mux.HandleFunc("PUT /api/settings/self-health", auth.RequireRole(auth.RoleAdmin, s.putSelfHealth))
	mux.HandleFunc("GET /api/settings/problem-escalation", auth.RequireRole(auth.RoleAdmin, s.getProblemEscalation))
	mux.HandleFunc("PUT /api/settings/problem-escalation", auth.RequireRole(auth.RoleAdmin, s.putProblemEscalation))
	// v0.9.775 — exception triyaj basamağının pencereleri (P1 tazeliği /
	// P2 aynı-gün / bayat auto-resolve). Ayrı anahtar: problem
	// escalation her Problem'e, bu yalnız exception gruplarına uygulanır.
	mux.HandleFunc("GET /api/settings/exception-triage", auth.RequireRole(auth.RoleAdmin, s.getExceptionTriage))
	mux.HandleFunc("PUT /api/settings/exception-triage", auth.RequireRole(auth.RoleAdmin, s.putExceptionTriage))
	// v0.9.838 — ALERT PROBLEMİ öncelik merdiveninin vidaları (ihlal katı
	// + bayat-critical saati). Üçüncü ayrı anahtar, çünkü üç ayrı hat:
	// problem-escalation yaşa göre TIRMANMA, exception-triage exception
	// GRUPLARI, bu ise alert kurallarının P1/P2/P3 basamağı. Aynı bildirim
	// min-öncelik süzgecini de besliyor (notify/alert_title.go).
	mux.HandleFunc("GET /api/settings/problem-priority", auth.RequireRole(auth.RoleAdmin, s.getProblemPriority))
	mux.HandleFunc("PUT /api/settings/problem-priority", auth.RequireRole(auth.RoleAdmin, s.putProblemPriority))
	// v0.9.1036 — failure-rate (%) SLO eşiği. GET rol KAPISIZ (kimlikli
	// herkes): bu bir yönetim vidası değil, hata-oranı grafiğindeki
	// referans ÇİZGİ. Viewer durumu GÖRÜR (invariant 7) — admin'e
	// kapatmak rolün okunan grafiği değiştirmesi olurdu.
	mux.HandleFunc("GET /api/settings/failure-slo", s.getFailureSLO)
	mux.HandleFunc("PUT /api/settings/failure-slo", auth.RequireRole(auth.RoleAdmin, s.putFailureSLO))
	// v0.9.797 — metrik route dışlamaları. Admin: kural HER operatörün
	// gördüğü grafiği değiştiriyor (ve dropAtIngest'te veriyi hiç
	// yazmıyor), yani editor yetkisi yetmez.
	mux.HandleFunc("GET /api/settings/metric-exclusions", auth.RequireRole(auth.RoleAdmin, s.getMetricExclusions))
	mux.HandleFunc("PUT /api/settings/metric-exclusions", auth.RequireRole(auth.RoleAdmin, s.putMetricExclusions))
	// GET/PUT /api/settings/ai → ai_routes.go (v0.9.1117).
	// GET/PUT/test /api/settings/victoria-metrics → vmetrics_routes.go
	// (v0.9.1150).
	s.registerVMetricsRoutes(mux)
	s.registerInfluxRoutes(mux)         // v0.10.222 — InfluxDB kaynakları GET/PUT/test, influx_routes.go
	s.registerThanosIdentityRoutes(mux) // v0.10.128 — Remote Cluster etiket rozeti, thanos_identity.go
	s.registerEntityRoutes(mux)         // v0.10.129 — entity katmanı bayrak + sync yönetimi, entity_routes.go
	s.registerEntityQueryRoutes(mux)    // v0.10.130 — entity pivot uçları, entities.go
	s.registerExceptionPodRoutes(mux)   // v0.10.138 — hata grubu pod/node dağılımı, exception_pods.go
	// External Tempo backend — admin-only because the token grants
	// read access to every trace in the operator's Tempo cluster.
	mux.HandleFunc("GET /api/settings/tempo", auth.RequireRole(auth.RoleAdmin, s.getTempoSettings))
	mux.HandleFunc("PUT /api/settings/tempo", auth.RequireRole(auth.RoleAdmin, s.putTempoSettings))
	// v0.9.655 — dış log sistemi köprü şablonu. Admin-only: bir URL
	// şablonu, cevaplarda tıklanabilir link üretiyor.
	mux.HandleFunc("GET /api/settings/correlation-link", auth.RequireRole(auth.RoleAdmin, s.getCorrelationLinkSetting))
	mux.HandleFunc("PUT /api/settings/correlation-link", auth.RequireRole(auth.RoleAdmin, s.putCorrelationLinkSetting))
	mux.HandleFunc("GET /api/settings/thanos", auth.RequireRole(auth.RoleAdmin, s.getThanosSettings))
	mux.HandleFunc("PUT /api/settings/thanos", auth.RequireRole(auth.RoleAdmin, s.putThanosSettings))
	// Azure DevOps / TFS bağlantısı (v0.9.829) — admin-only: kayıtlı
	// PAT operatörün tüm koleksiyonunu okur. Test ucu kaydetmeden
	// dener, sonucu {ok,error} olarak 200 ile döner.
	s.registerMCPClientRoutes(mux) // v0.10.87 — dış MCP sunucu listesi, mcp_client_routes.go
	mux.HandleFunc("GET  /api/settings/devops", auth.RequireRole(auth.RoleAdmin, s.getDevOpsSettings))
	mux.HandleFunc("PUT  /api/settings/devops", auth.RequireRole(auth.RoleAdmin, s.putDevOpsSettings))
	mux.HandleFunc("POST /api/settings/devops/test", auth.RequireRole(auth.RoleAdmin, s.testDevOpsSettings))
	// v0.9.1242 — servis → depo/branş/ağaç PROVASI. Salt teşhis:
	// yazmaz (audit yok), cache'lenmez (operatör konvansiyonu
	// düzenleyip hemen yeniden dener) ve FetchCode'a girmediği için
	// v0.9.1241'in isabet-oranı sayaçlarına dokunmaz. Kapı, kardeş
	// devops uçlarıyla aynı: kayıtlı PAT tüm koleksiyonu okur.
	mux.HandleFunc("POST /api/devops/resolve-dryrun", auth.RequireRole(auth.RoleAdmin, s.devopsResolveDryRun))
	mux.HandleFunc("GET  /api/settings/logstore", auth.RequireRole(auth.RoleAdmin, s.getLogstoreESSettings))
	mux.HandleFunc("PUT  /api/settings/logstore", auth.RequireRole(auth.RoleAdmin, s.putLogstoreESSettings))
	mux.HandleFunc("POST /api/settings/logstore/test", auth.RequireRole(auth.RoleAdmin, s.testLogstoreESSettings))
	// External Kibana deep-link config (v0.5.236). GET is open
	// to any signed-in user — the Logs page renders the link
	// for everyone; only the admin can change the base URL.
	mux.HandleFunc("GET /api/settings/kibana", s.getKibanaSettings)
	mux.HandleFunc("PUT /api/settings/kibana", auth.RequireRole(auth.RoleAdmin, s.putKibanaSettings))
	mux.HandleFunc("GET  /api/settings/ldap", auth.RequireRole(auth.RoleAdmin, s.getLDAPSettings))
	mux.HandleFunc("PUT  /api/settings/ldap", auth.RequireRole(auth.RoleAdmin, s.putLDAPSettings))
	mux.HandleFunc("POST /api/settings/ldap/test", auth.RequireRole(auth.RoleAdmin, s.testLDAPConnection))
	mux.HandleFunc("GET  /api/settings/ldap/search", auth.RequireRole(auth.RoleAdmin, s.searchLDAPUsers))
	mux.HandleFunc("GET  /api/settings/ldap/inspect", auth.RequireRole(auth.RoleAdmin, s.inspectLDAPUser))
	mux.HandleFunc("POST /api/users/from-ldap", auth.RequireRole(auth.RoleAdmin, s.provisionLDAPUser))

	// ── AI Copilot ─────────────────────────────────────────────────
	// /api/copilot/* uçları → ai_routes.go (v0.9.1117, Faz 0.1).
	mux.HandleFunc("GET    /api/shift", s.getShiftSummary) // v0.9.1071 — vardiya özeti (AI DEĞİL, veri ucu)

	// ── Incident management ───────────────────────────────────────
	mux.HandleFunc("GET    /api/incidents", s.listIncidents)
	mux.HandleFunc("POST   /api/incidents", auth.RequireAnyRole(editorRoles, s.createIncident))
	mux.HandleFunc("GET    /api/incidents/{id}", s.getIncident)
	mux.HandleFunc("PUT    /api/incidents/{id}", auth.RequireAnyRole(editorRoles, s.updateIncident))
	mux.HandleFunc("POST   /api/incidents/{id}/ack", auth.RequireAnyRole(editorRoles, s.ackIncident))
	mux.HandleFunc("POST   /api/incidents/{id}/resolve", auth.RequireAnyRole(editorRoles, s.resolveIncident))
	mux.HandleFunc("POST   /api/incidents/{id}/note", auth.RequireAnyRole(editorRoles, s.addIncidentNote))
	mux.HandleFunc("GET    /api/incidents/{id}/timeline", s.incidentTimeline)
	mux.HandleFunc("GET    /api/incidents/{id}/problems", s.incidentProblems)

	// ── Synthetic monitoring ───────────────────────────────────────
	mux.HandleFunc("GET    /api/monitors", s.listMonitors)
	mux.HandleFunc("POST   /api/monitors", auth.RequireAnyRole(editorRoles, s.createMonitor))
	mux.HandleFunc("GET    /api/monitors/{id}", s.getMonitor)
	mux.HandleFunc("PUT    /api/monitors/{id}", auth.RequireAnyRole(editorRoles, s.updateMonitor))
	mux.HandleFunc("DELETE /api/monitors/{id}", auth.RequireAnyRole(editorRoles, s.deleteMonitor))
	mux.HandleFunc("GET    /api/monitors/{id}/timeline", s.monitorTimeline)
	// Heartbeat ingest — no auth so cron jobs / batch jobs can hit
	// it directly with curl. The token in the URL is the security
	// boundary (random 32-char hex per monitor).
	mux.HandleFunc("POST /api/heartbeats/{token}", s.acceptHeartbeat)
	mux.HandleFunc("GET  /api/heartbeats/{token}", s.acceptHeartbeat) // GET for `curl` and uptime trackers that only do GET

	// Auth
	mux.HandleFunc("GET  /api/auth/config", s.authConfig)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.HandleFunc("GET  /api/auth/me", s.me)
	mux.HandleFunc("GET  /api/auth/me/photo", s.mePhoto)
	mux.HandleFunc("POST /api/auth/password", s.changeOwnPassword)
	mux.HandleFunc("GET  /api/auth/oidc/start", s.oidcStart)
	mux.HandleFunc("GET  /api/auth/oidc/callback", s.oidcCallback)

	// SLOs (read = any user, write = admin)
	mux.HandleFunc("GET    /api/slos", s.listSLOs)
	mux.HandleFunc("GET    /api/slos/{id}", s.getSLO)
	mux.HandleFunc("GET    /api/slos/{id}/status", s.sloStatus)
	mux.HandleFunc("GET    /api/slos/{id}/burn-series", s.sloBurnSeries)
	// v0.6.30 — SLO burn-down forecast. Projects when the error
	// budget will be exhausted at the current short-window burn
	// rate. /slos list page surfaces "breaches in <24h" chip.
	mux.HandleFunc("GET    /api/slos/{id}/forecast", s.sloForecast)
	mux.HandleFunc("POST   /api/slos", auth.RequireAnyRole(editorRoles, s.createSLO))
	mux.HandleFunc("DELETE /api/slos/{id}", auth.RequireAnyRole(editorRoles, s.deleteSLO))

	// Dashboards (read = any user, write = admin)
	mux.HandleFunc("GET    /api/dashboards", s.listDashboards)
	mux.HandleFunc("GET    /api/dashboards/{id}", s.getDashboard)
	mux.HandleFunc("POST   /api/dashboards", auth.RequireAnyRole(editorRoles, s.createDashboard))
	mux.HandleFunc("PUT    /api/dashboards/{id}", auth.RequireAnyRole(editorRoles, s.updateDashboard))
	mux.HandleFunc("DELETE /api/dashboards/{id}", auth.RequireAnyRole(editorRoles, s.deleteDashboard))

	// Settings + notification channels (admin only)
	mux.HandleFunc("GET    /api/settings/team-contacts", auth.RequireRole(auth.RoleAdmin, s.getTeamContacts))
	mux.HandleFunc("PUT    /api/settings/team-contacts", auth.RequireRole(auth.RoleAdmin, s.putTeamContacts))
	// v0.9.427 — LDAP↔telemetri takım adı eşleme tablosu (team_aliases.go).
	mux.HandleFunc("GET    /api/settings/team-aliases", auth.RequireRole(auth.RoleAdmin, s.getTeamAliases))
	mux.HandleFunc("PUT    /api/settings/team-aliases", auth.RequireRole(auth.RoleAdmin, s.putTeamAliases))
	mux.HandleFunc("GET    /api/settings/smtp", auth.RequireRole(auth.RoleAdmin, s.getSMTPSettings))
	mux.HandleFunc("PUT    /api/settings/smtp", auth.RequireRole(auth.RoleAdmin, s.putSMTPSettings))
	mux.HandleFunc("POST   /api/settings/smtp/test", auth.RequireRole(auth.RoleAdmin, s.testSMTPSettings))
	mux.HandleFunc("GET    /api/channels", auth.RequireRole(auth.RoleAdmin, s.listChannels))
	mux.HandleFunc("POST   /api/channels", auth.RequireRole(auth.RoleAdmin, s.createChannel))
	mux.HandleFunc("PUT    /api/channels/{id}", auth.RequireRole(auth.RoleAdmin, s.updateChannel))
	mux.HandleFunc("DELETE /api/channels/{id}", auth.RequireRole(auth.RoleAdmin, s.deleteChannel))
	mux.HandleFunc("POST   /api/channels/{id}/test", auth.RequireRole(auth.RoleAdmin, s.testChannel))
	// Per-channel dispatch health derived from notification_log
	// (v0.9.1278). Same admin gate as listChannels — it is the
	// same page's data and the error strings can name internal
	// relay hosts.
	mux.HandleFunc("GET    /api/notify/channels/health", auth.RequireRole(auth.RoleAdmin, s.channelHealth))
	// Maintenance windows — admin-only CRUD. While an active
	// window matches a problem's (service, severity), the
	// notifier skips the live fan-out. Problems still open +
	// auto-resolve so the post-window timeline review is
	// intact; only the channel spam is suppressed.
	mux.HandleFunc("GET    /api/maintenance-windows", auth.RequireRole(auth.RoleAdmin, s.listMaintenanceWindows))
	mux.HandleFunc("POST   /api/maintenance-windows", auth.RequireRole(auth.RoleAdmin, s.createMaintenanceWindow))
	mux.HandleFunc("DELETE /api/maintenance-windows/{id}", auth.RequireRole(auth.RoleAdmin, s.deleteMaintenanceWindow))
	// Zoom-specific helper: list channels the configured S2S
	// OAuth app can see, so admins pick a Channel ID from a
	// searchable list instead of pasting JIDs by hand. POST
	// because we need the un-redacted client secret in the body
	// for un-saved configurations (and we don't want secrets in
	// the URL query string).
	mux.HandleFunc("POST   /api/channels/zoom/list-channels",
		auth.RequireRole(auth.RoleAdmin, s.listZoomChannels))

	// User management (admin only)
	mux.HandleFunc("GET    /api/users", auth.RequireRole(auth.RoleAdmin, s.listUsers))
	// Per-team membership lookup — readable by any
	// authenticated user (not admin-only). Drives the
	// owner-team / SRE-team chip popover on /service?name=…
	// Returns email + role + team only; password hash is
	// never serialised regardless.
	mux.HandleFunc("GET    /api/users/by-team", s.listUsersByTeam)
	mux.HandleFunc("POST   /api/users", auth.RequireRole(auth.RoleAdmin, s.createUser))
	mux.HandleFunc("DELETE /api/users/{id}", auth.RequireRole(auth.RoleAdmin, s.deleteUser))
	mux.HandleFunc("GET    /api/users/{id}/photo", auth.RequireRole(auth.RoleAdmin, s.userPhoto))
	mux.HandleFunc("POST   /api/users/{id}/password", auth.RequireRole(auth.RoleAdmin, s.resetUserPassword))
	mux.HandleFunc("PUT    /api/users/{id}/role", auth.RequireRole(auth.RoleAdmin, s.setUserRole))
	mux.HandleFunc("PUT    /api/users/{id}/team", auth.RequireRole(auth.RoleAdmin, s.setUserTeam))
	mux.HandleFunc("PUT    /api/users/{id}/custom-role", auth.RequireRole(auth.RoleAdmin, s.setUserCustomRole))

	// Custom roles — operator-defined viewer subsets (v0.5.251).
	// Page list is sourced from a single backend registry so the
	// sidebar + checkbox grid + custom-role pages share IDs.
	mux.HandleFunc("GET    /api/admin/custom-roles", auth.RequireRole(auth.RoleAdmin, s.listCustomRoles))
	mux.HandleFunc("POST   /api/admin/custom-roles", auth.RequireRole(auth.RoleAdmin, s.upsertCustomRole))
	mux.HandleFunc("DELETE /api/admin/custom-roles/{name}", auth.RequireRole(auth.RoleAdmin, s.deleteCustomRole))
	mux.HandleFunc("GET    /api/admin/pages", auth.RequireRole(auth.RoleAdmin, s.listAvailablePages))

	// Cluster membership — multi-pod HA visibility (v0.5.253).
	// Single round-trip: SCAN coremetry:pod:* in Redis + MGET. Empty
	// list (no Redis) falls back to a single-pod view.
	mux.HandleFunc("GET    /api/admin/cluster", auth.RequireRole(auth.RoleAdmin, s.listClusterMembers))
	// v0.5.277 — "what changed" banner data. Open problem
	// counts + recent service.version transitions. Cheap
	// (15s cache); polled from the global AppShell.

	// Ingest pipeline (v0.5.263) — admin-managed drop / enrich
	// rules applied before the sampler. List + upsert + delete.
	mux.HandleFunc("GET    /api/admin/pipeline-rules", auth.RequireRole(auth.RoleAdmin, s.listPipelineRules))
	mux.HandleFunc("POST   /api/admin/pipeline-rules", auth.RequireRole(auth.RoleAdmin, s.upsertPipelineRule))
	mux.HandleFunc("DELETE /api/admin/pipeline-rules/{id}", auth.RequireRole(auth.RoleAdmin, s.deletePipelineRule))

	// Tempo-compatible API (Grafana datasource integration)
	s.registerTempoRoutes(mux)
	// v0.10.36 — K8s bağlam kapsama kartı (entity katmanı Faz 0), k8scoverage.go
	s.registerK8sCoverageRoutes(mux)

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
	// v0.8.346 (HA audit H6) — the warmer belongs to the api role: on a
	// 6-ingest + 2-api distributed fleet every pod used to run the heavy
	// /databases + /messaging GROUP-BY warm set every 25s.
	if !s.roleAPIOff {
		go s.warmDependenciesCache()
	}

}

func (s *Server) listen(mux *http.ServeMux) error {
	handler := otelhttp.NewHandler(cors(s.auth.Middleware(s.presence.middleware(mux))),
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
	s.httpSrv = &http.Server{Addr: s.addr, Handler: handler}
	return s.httpSrv.ListenAndServe()
}

// Shutdown drains the HTTP server (v0.8.336, HA audit H1): stops
// accepting new connections, lets in-flight requests finish within
// ctx's deadline. Safe before Start() (nil server = no-op).
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpSrv == nil {
		return nil
	}
	return s.httpSrv.Shutdown(ctx)
}

// warmDepsClaimKey is the fleet-wide SETNX claim for one warmer cycle
// (v0.8.350, HA 🟡4). No release path on purpose: the TTL (= the warm
// tick) is the claim's whole lifetime, so a crashed winner's claim
// simply expires into the next window.
const warmDepsClaimKey = "coremetry:warm:deps"

// warmClaim reports whether THIS pod should run the current warm cycle.
// SETNX semantics: the first pod each window wins; the rest skip (their
// readers are served from the shared Redis L2 the winner populates).
// Redis errors fail OPEN — a pod with no reachable Redis has no shared
// L2 and must warm its own L1. Package-level func over the Cache
// interface so the claim behaviour is unit-testable without a Server.
func warmClaim(ctx context.Context, c cache.Cache, ttl time.Duration) bool {
	ok, err := c.SetNX(ctx, warmDepsClaimKey, []byte("1"), ttl)
	if err != nil {
		return true
	}
	return ok
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
	// v0.10.28 — NIL KORUMASI. Bu işçi `registerRoutes`ten goroutine
	// olarak başlıyor, yani rota kaydının bir YAN ETKİSİ. Alanları sıfır
	// bir Server ile çağrıldığında (testler böyle yapıyor) ilk SETNX
	// nil'e dokunup panikliyordu — ve Go'da bir goroutine'deki
	// yakalanmamış panik TÜM SÜRECİ düşürür.
	//
	// Prod'da tetiklenmiyor: main.go cache'i `cache.NewNoop()` ile
	// kuruyor ve Redis hatası dalında da Noop'ta KALIYOR, yani cacheImpl
	// asla nil olmuyor. Zarar test tarafındaydı ve sinsiydi: panik
	// `go test -race ./internal/api/` koşusunu düşürüyordu, yani bu
	// pakette YARIŞ DEDEKTÖRÜ kullanılamıyordu — üstelik paket sohbetin
	// eşzamanlı SSE kodunu barındırıyor (v0.10.27 heartbeat'i tam da
	// orada yaşıyor).
	//
	// Guard, api.go:2216'daki mevcut `s.cache == nil` kontrolüyle aynı
	// sözleşme; orada vardı, burada yoktu.
	if s == nil || s.cache == nil || s.store == nil {
		return
	}
	const (
		// v0.8.476 (perf dalga-1 #5) — serbest 25s uyku, cacheBucket'ın
		// 30s grid'ine göre faz kaydırıyordu: her yeni slotun ortalama
		// yarısı soğuk kalıyor, ısıtılan yüzeylerde bile ilk istek
		// 120-190ms ödüyordu. Tick artık grid sınırının 1s SONRASINA
		// hizalı (sleepToGrid): yeni anahtar doğar doğmaz ısınır, soğuk
		// pencere ~1s'e iner. Claim TTL 29s: grid tick'leri ~30s arayla
		// gelir; ölen lider bir sonraki tick'te devredilir, aynı tick'te
		// çifte ısıtma olmaz.
		claimTTL  = 29 * time.Second
		ttl       = 30 * time.Second
		warmWin   = time.Hour
		queryBudg = 20 * time.Second
	)
	sleepToGrid := func() {
		now := time.Now()
		next := now.Truncate(30 * time.Second).Add(30*time.Second + time.Second)
		time.Sleep(next.Sub(now))
	}
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
	for {
		// v0.8.350 (HA 🟡4) — cross-pod dedup: ONE warm cycle per fleet
		// window, not per pod. A short SETNX claim (TTL = the tick)
		// elects this window's warmer; losers skip the cycle — the
		// winner's results land in shared Redis via storeCached, so
		// every pod's readers hit warm L2 entries regardless of who
		// ran the GROUP-BYs. Redis errors fail OPEN (claim granted):
		// without a reachable Redis there is no shared L2, so each pod
		// must keep warming its own L1.
		claimCtx, cancelClaim := context.WithTimeout(context.Background(), 2*time.Second)
		claimed := warmClaim(claimCtx, s.cache, claimTTL)
		cancelClaim()
		if !claimed {
			sleepToGrid()
			continue
		}
		to := time.Now()
		from := to.Add(-warmWin)
		// v0.9.821 — önek getDatabases handler'ıyla BİREBİR aynı kalmalı
		// (databases:v2:…:env=…:cmp=…), yoksa ısıtılan slotu kimse okumaz.
		// Isıtılan slot filtresiz + compare'siz olan: sayfayı açan
		// operatörün ilk isteği bu.
		warm("databases", fmt.Sprintf("databases:v2:%s:env=:cmp=false", cacheBucket(from, to)), ttl,
			func(ctx context.Context) (any, error) {
				return s.store.GetDatabases(ctx, chstore.DatabasesQuery{
					From: from, To: to, IncludeCallers: true, IncludeReceivers: true,
				})
			})
		// v0.9.813 — önek getMessaging handler'ıyla BİREBİR aynı kalmalı
		// (messaging:v2:), yoksa ısıtılan slotu kimse okumaz.
		warm("messaging", "messaging:v2:"+cacheBucket(from, to), ttl,
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
		// v0.8.383 — env picker options. Mirrors the clusters warm
		// entry (same key shape as getEnvironments' default window)
		// so the Topbar picker's very first fetch after login lands
		// warm. The store-side 1h scan clamp keeps this a cheap
		// LowCardinality dict GROUP BY per tick.
		warm("environments", "environments:"+cacheBucket(from.Add(-23*time.Hour), to)+":q=", 60*time.Second,
			func(ctx context.Context) (any, error) {
				names, total, err := s.store.ListEnvironments(ctx, from.Add(-23*time.Hour), to, "", 50)
				if err != nil {
					return nil, err
				}
				return map[string]any{"environments": names, "total": total}, nil
			})
		// v0.8.474 (perf dalga-1 #3'ün warm ayağı; v473 refactor'ı
		// computeInboxCount'u çıkardı, girişler bu sürümde) — rozet
		// anahtarları: sidebar'ın 30s poll'u artık hiç CH'a inmez (her
		// poll taze HIT); CH maliyeti sekme-başına-30s'den filo-başına-
		// 25s'e düşer. Anahtar şekilleri handler'larla birebir
		// (problems-count default = status=open, filtresiz).
		// v0.8.475 (perf dalga-1 #4) — Wave-3 sayfaları warmer'dan sonra
		// eklendiği için hiç ısıtılmıyordu; ilk navigasyondaki ölçülen
		// 120-190ms soğuk-slot silinir.
		//
		// v0.9.690 — `hosts` ve `external` GİRİŞLERİ KALDIRILDI.
		//
		// O sayfalar v0.9.489-499 sadeleştirmesinde nav'dan gizlendi
		// (Sidebar.tsx: "hidden from nav, code alive") ama warm girişleri
		// güncellenmedi: 30 saniyede bir, kimsenin açmadığı iki sayfa
		// için tam tarama.
		//
		// ÖLÇÜLDÜ (chc-0 query_log, 6 saat):
		//   hosts warmer   : 723 + 723 çağrı · 132 GiB · SELECT baytının %13.9
		//   external warmer:       867 çağrı ·  24 GiB · %2.5
		//   723 çağrı / 6 saat = tam 30 s ızgara → SIFIR kullanıcı isteği
		//
		// Handler'lar ve rotalar DURUYOR: URL'den erişilebilir ve
		// `serveCached` ile talep üzerine servis ediliyor. Kaldırılan tek
		// şey, kimsenin istemediği veriyi önden çekmek.
		//
		// Sayfalar nav'a geri gelirse warm girişleri de geri gelmeli —
		// warm_hidden_pages_test.go bu çifti bir arada tutuyor.
		warm("problems-count", "problems-count:status=open:svc=:sev=:env=", 15*time.Second,
			func(ctx context.Context) (any, error) {
				n, err := s.store.CountProblems(ctx, chstore.ProblemFilter{Status: "open"})
				if err != nil {
					return nil, err
				}
				return map[string]any{"count": n}, nil
			})
		// v0.9.219 — key via inboxCountKey so the warmed payload lands on the
		// exact key the handler reads. Only the unscoped badge is warmed; an
		// env-scoped one computes on demand (15s TTL, three parallel COUNTs)
		// rather than multiplying the warm loop by the env cardinality.
		warm("inbox-count", inboxCountKey(""), 15*time.Second, s.computeInboxCount)
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
		// v0.9.231 (scale-audit) — this string used to be hand-written and had
		// gone stale: servicesListKey grew :env=:ns=:wt= and the warm key never
		// did, so the handler always looked up a key the warmer never wrote.
		// The entry was unreachable — a fleet-wide MV scan every 25s that
		// nothing ever read, while the landing page (the whole reason this
		// warmer exists) still paid a cold scan on every login. Building it
		// through the same function the handler uses means they cannot drift
		// again.
		landingKey := servicesLandingWarmKey(landingFrom, to) // v0.10.256 — handler ile aynı fonksiyon (ek dahil)
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
		sleepToGrid()
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// getClusters returns the distinct k8s / openshift cluster names
// observed in the requested window. Drives the cluster-filter
// dropdown on /services + the per-cluster selector on
// /service?name=. Cached 5min — the cluster set changes very rarely, so
// the longer TTL (+ serveCached's stale-while-revalidate) keeps the
// dropdown instant and means a transient slow ListClusters refresh serves
// the last-good value instead of blanking (operator-reported empties).
func (s *Server) getClusters(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, 24*time.Hour)
	key := "clusters:" + cacheBucket(from, to)
	s.serveCached(w, r, key, 5*time.Minute, func(ctx context.Context) (any, error) {
		names, err := s.store.ListClusters(ctx, from, to)
		if err != nil {
			return nil, err
		}
		return map[string]any{"clusters": names}, nil
	})
}

// getNamespaces returns the distinct derived namespaces across the
// service catalog — the option list for the Services-page namespace
// filter (v0.9.189). Sourced from service_metadata.Namespace (the
// deriver coalesces service.namespace AND k8s.namespace.name), so it
// costs NO span scan — a catalog read (~thousands of rows), 5-min
// cached like getClusters. Small LowCardinality set → plain <select>.
func (s *Server) getNamespaces(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, 24*time.Hour)
	key := "namespaces:" + cacheBucket(from, to)
	s.serveCached(w, r, key, 5*time.Minute, func(ctx context.Context) (any, error) {
		catalog, err := s.store.ListServiceMetadata(ctx)
		if err != nil {
			return nil, err
		}
		seen := map[string]struct{}{}
		names := []string{}
		for _, md := range catalog {
			ns := strings.TrimSpace(md.Namespace)
			if ns == "" {
				continue
			}
			if _, ok := seen[ns]; ok {
				continue
			}
			seen[ns] = struct{}{}
			names = append(names, ns)
		}
		sort.Strings(names)
		return map[string]any{"namespaces": names}, nil
	})
}

// getEnvironments returns the distinct deployment environments
// (spans.deploy_env) observed in the requested window — the option
// list for the global Topbar env picker (v0.8.383, env-separation
// Phase 1). Mirrors getClusters: 60s serveCached TTL (+ SWR + the
// warmer below keeps the first click after login warm), bucketed
// from/to key, empty env excluded store-side. The store clamps the
// scan to the most recent hour (clusterScanWindow precedent) — the
// env set is deploy-stable, so a wider scan only burns read
// bandwidth.
// v0.8.389 (operator-reported): feature-branch envs exploded the set
// past the old alphabetical LIMIT 50 — "release" never surfaced.
// The list is now count-ordered with an optional ?q= substring
// search (search widens the store-side scan clamp 1h→24h so quiet
// envs are findable) and ships `total` so the picker labels
// truncation. q rides the cache key (free-text precedent: the
// services search key).
func (s *Server) getEnvironments(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, 24*time.Hour)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	key := "environments:" + cacheBucket(from, to) + ":q=" + q
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		names, total, err := s.store.ListEnvironments(ctx, from, to, q, 50)
		if err != nil {
			return nil, err
		}
		return map[string]any{"environments": names, "total": total}, nil
	})
}

// getServiceEnvironments returns the distinct environments ONE
// service emitted from in the window — the Envs chip group on the
// Service detail header (v0.8.383, env-separation Phase 0c).
// Same shape as getServiceClusterBreakdown: cached on the
// (service, window) tuple so range toggles share a query.
func (s *Server) getServiceEnvironments(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("service-envs:%s:%s", name, cacheBucket(from, to))
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		names, err := s.store.GetServiceEnvironments(ctx, name, from, to)
		if err != nil {
			return nil, err
		}
		return map[string]any{"environments": names}, nil
	})
}

// servicesUseMV is the /api/services MV fast-path gate. The
// service_summary_5m MV writes a row every 5 minutes (sub-5m windows
// would read empty) and carries NEITHER a cluster NOR an env
// dimension — either filter disqualifies the MV and the read falls
// to the bounded raw-spans path (cluster since v0.5.372, env since
// v0.8.385 — env-separation Phase 2, cluster-parity raw-fallback,
// NO MV changes). Factored pure so env_gate_test.go pins it.
func servicesUseMV(window time.Duration, cluster, env string) bool {
	return window >= 5*time.Minute && cluster == "" && env == ""
}

// servicesListKey builds the /api/services cache key from ALL
// inputs (hash-all-inputs rule; the v0.5.187 class). env joined in
// v0.8.385 — without it an env-filtered page would cross-poison the
// unfiltered one inside the same 30s bucket.
func servicesListKey(useMV bool, limit, offset int, bucket, nameMatch, sort, dir, ownerTeam, sreTeam, cluster, env, namespace string, withTotal bool) string {
	return fmt.Sprintf("services:mv=%t:limit=%d:offset=%d:bucket=%s:name=%s:sort=%s:dir=%s:ot=%s:st=%s:cl=%s:env=%s:ns=%s:wt=%t",
		useMV, limit, offset, bucket, nameMatch, sort, dir, ownerTeam, sreTeam, cluster, env, namespace, withTotal)
}

// servicesFullKey — v0.10.256 (perf §7 madde 2, C1 ⭐): handler anahtarı =
// liste anahtarı + görüntü süzgeci eki. Isıtıcı da BUNU kurar; v0.9.231
// aynı sınıfı bir kez düzeltmişti (":env=:ns=:wt=" eki), sonra
// ":err=…:cmp=…" eki yalnız handler'a eklendi ve ısıtılan anahtar yine
// erişilmez oldu — servis sayfası her girişte soğuk MV taraması ödedi.
// Test: TestServicesLandingWarmKeyMatchesHandler.
func servicesFullKey(useMV bool, limit, offset int, bucket, nameMatch, sort, dir, ownerTeam, sreTeam, cluster, env, namespace string, withTotal bool, display chstore.ServiceDisplayFilters, compare bool) string {
	return servicesListKey(useMV, limit, offset, bucket, nameMatch, sort, dir, ownerTeam, sreTeam, cluster, env, namespace, withTotal) +
		fmt.Sprintf(":err=%v:minSpans=%d:minP99=%g:cmp=%v", display.ErrorsOnly, display.MinSpans, display.MinP99Ms, compare)
}

// servicesLandingWarmKey — ısıtıcının (api.go warm("services-landing")) ve
// testin ortak anahtarı: açılış isteği = mv, 50/0, errorRate desc, süzgeç
// yok, 15 dk pencere.
func servicesLandingWarmKey(landingFrom, to time.Time) string {
	return servicesFullKey(true, 50, 0, cacheBucket(landingFrom, to),
		"", "errorRate", "desc", "", "", "", "", "", false, chstore.ServiceDisplayFilters{}, false)
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
	// v0.9.1111 (Faz 5 "en çok kötüleşenler") — opsiyonel önceki-pencere
	// kıyası. compare=prior, aynı uzunluktaki bir önceki pencereyi
	// SAYFANIN servis adlarıyla (ServiceIn) okuyup Prior* alanlarını
	// doldurur — endpoints'in liste-vs-liste birleşiminden kesin:
	// sayfadaki her satır prior değerini alır, sıralama kayması satır
	// düşürmez. sort=p99Delta aday-havuzu desenini kullanır (endpoints
	// v0.9.761 emsali) ve compare'i zorlar (delta prior'suz tanımsız);
	// havuz filo-geneli sıralandığından offset'li bir "delta sayfası"
	// dürüst kurulamaz → offset 0'a, hasMore false'a sabitlenir.
	compare := q.Get("compare") == "prior"
	deltaSort := sort == "p99Delta"
	if deltaSort {
		compare = true
		offset = 0
	}
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
	// Global env filter (v0.8.385, env-separation Phase 2) — the
	// Topbar picker's ?env=. Narrows to spans.deploy_env with the
	// same raw-fallback semantics as cluster (the MV has no env dim).
	env := strings.TrimSpace(q.Get("env"))
	// Namespace filter (v0.9.189) — narrows to services whose DERIVED
	// namespace (service_metadata.Namespace, coalesced by the deriver
	// from service.namespace AND k8s.namespace.name) matches. Resolved
	// via the catalog into the serviceIn allowlist like ownerTeam/sreTeam
	// — so, UNLIKE cluster/env, it does NOT disqualify the MV fast path.
	namespace := strings.TrimSpace(q.Get("namespace"))
	// v0.9.345 — the three display filters, now server-side.
	//
	// They ran in the BROWSER over the 50 rows of the current page, so at
	// 1000s of services "Errors only" could empty page 1 while erroring
	// services sat on page 7. They are aggregate predicates (HAVING), which
	// means paging now walks the MATCHING services instead of paging
	// everything and hiding rows after the fact.
	//
	// Parsed leniently: a blank or unparseable box is "no constraint", the
	// same thing the old client-side code did with an empty input.
	display := chstore.ServiceDisplayFilters{
		ErrorsOnly: q.Get("errorsOnly") == "1" || q.Get("errorsOnly") == "true",
		MinSpans:   uint64(maxInt(parseInt(q.Get("minSpans"), 0), 0)),
		// parseFloat returns 0 on garbage, which is exactly "no constraint".
		MinP99Ms: parseFloat(strings.TrimSpace(q.Get("minP99"))),
	}
	if from.IsZero() {
		from = time.Now().Add(-since)
	}
	if to.IsZero() {
		to = time.Now()
	}
	// MV-backed fast path. The MV writes a row every 5 minutes, so windows
	// shorter than ~5 min would return empty — fall through to the raw
	// scan in that case (small window = small scan, also fast).
	// MV path doesn't carry a cluster or env dimension — force raw
	// scan when either filter is set. Trade-off: full scan over the
	// window, but bounded by the filter so the cost stays
	// proportional to the chosen slice's traffic.
	useMV := servicesUseMV(to.Sub(from), cluster, env)
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
	// Display filters join the key: they change WHICH services come back, so
	// two operators on different filters must not share one cached page —
	// the v0.5.187 cross-poisoning shape.
	key := servicesFullKey(useMV, limit, offset, bucket, nameMatch, sort, dir, ownerTeam, sreTeam, cluster, env, namespace, withTotal, display, compare)
	// 30s cache. The 5m-MV-backed query is already sub-second on
	// 10k+ services, but 30s collapses every page-flip and tab
	// switch in a session into one CH round-trip per (page,
	// filter, range). Refresh button on the page can ?refresh=1.
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		// Resolve team filters → service-name allowlist via
		// the catalog. Bounded by catalog size (~thousands),
		// not span volume; effectively free.
		var serviceIn []string
		if ownerTeam != "" || sreTeam != "" || namespace != "" {
			catalog, cerr := s.store.ListServiceMetadata(ctx)
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
				if namespace != "" && md.Namespace != namespace {
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
		baseSort, baseDir := sort, dir
		if deltaSort {
			// Aday havuzu: en çok trafik alan ilk N; delta bu evren
			// içinde hesaplanıp sıralanır. Evren notu UI'da.
			probeLimit = servicesDeltaPool(limit)
			baseSort, baseDir = "spanCount", "desc"
		}
		// v0.8.532 — the v0.8.530 errgroup parallelization was reverted
		// to serial: running the list + open-problem counts + total
		// concurrently tripled the per-recompute CH connection footprint
		// (each Query grabs a pooled conn) and, under a cold-cache burst
		// of distinct keys, exhausted the shared pool → connection-acquire
		// queueing → the operator-reported prod slowness. Serial holds one
		// conn at a time per recompute, as it did pre-v530. The warm path
		// is 30s-cached and sub-second, so the marginal cold-recompute
		// saving never justified the pool pressure. (Audit:
		// docs/audit/getservices-errgroup-audit.md.)
		var rows []chstore.ServiceSummary
		var err error
		if useMV {
			rows, err = s.store.GetServicesAggFiltered2(ctx, from, to, nameMatch, serviceIn, baseSort, baseDir, probeLimit, offset, display)
		} else {
			rows, err = s.store.GetServicesQuery(ctx, chstore.ServicesQuery{
				Since: since, From: from, To: to, NameMatch: nameMatch, ServiceIn: serviceIn,
				Sort: baseSort, Dir: baseDir, Limit: probeLimit, Offset: offset,
				Cluster: cluster, Env: env, Display: display,
			})
		}
		if err != nil {
			return nil, err
		}
		hasMore := len(rows) > limit
		if deltaSort {
			// Havuz limit'ten büyük; kesme delta sıralamasından SONRA.
			hasMore = false
		} else if hasMore {
			rows = rows[:limit]
		}
		// v0.9.1111 — prior penceresi, sayfadaki (deltaSort'ta havuzdaki)
		// adlara sabitlenmiş ikinci okuma. Düşerse trend'siz dönülür —
		// 500 değil (endpoints'in soft-fail sözleşmesi).
		if compare && len(rows) > 0 {
			names := make([]string, len(rows))
			for i := range rows {
				names[i] = rows[i].Name
			}
			dur := to.Sub(from)
			pfrom, pto := from.Add(-dur), from
			var priorRows []chstore.ServiceSummary
			var perr error
			if useMV {
				priorRows, perr = s.store.GetServicesAggFiltered2(ctx, pfrom, pto, "", names, "spanCount", "desc", len(names), 0, chstore.ServiceDisplayFilters{})
			} else {
				priorRows, perr = s.store.GetServicesQuery(ctx, chstore.ServicesQuery{
					Since: since, From: pfrom, To: pto, ServiceIn: names,
					Limit: len(names), Cluster: cluster, Env: env,
				})
			}
			if perr == nil {
				mergePriorServices(rows, priorRows)
			}
		}
		if deltaSort {
			rows = sortServicesByP99Delta(rows, limit)
		}
		// v0.5.274 — auto-score health per service from errorRate +
		// open-problem counts. Single FINAL scan bounded by status=open.
		counts, perr := s.openProblemCountsCached(ctx)
		if perr == nil {
			for i := range rows {
				c := counts[rows[i].Name]
				rows[i].OpenProblems = c.Critical + c.Warning + c.Info
				rows[i].Health, rows[i].HealthReason = scoreHealth(&rows[i], c)
			}
		}
		// v0.9.1317 (entity-model A2) — lifecycle pair onto the same page.
		// This rides the existing response rather than a second endpoint:
		// the rows are already here, already cached, and already enriched
		// from a whole-fleet cached map one line above, so the marginal
		// cost is a map lookup per row. A dedicated /api/services/seen
		// would have re-paginated the same service list to deliver one
		// column, and the page would have to join them client-side.
		s.applyServiceSeen(ctx, rows)
		resp := map[string]any{
			"services": rows,
			"hasMore":  hasMore,
			"offset":   offset,
			"limit":    limit,
		}
		// v0.7.44 — opt-in distinct-service count for the First/Last pager.
		// MV path only (cheap uniqExact over service_summary_5m).
		if withTotal && useMV {
			if total, terr := s.store.CountServicesAgg(ctx, from, to, nameMatch, serviceIn); terr == nil {
				resp["total"] = total
			}
		}
		return resp, nil
	})
}

// scoreHealth (v0.5.274) — Datadog-Watchdog-style red/yellow/
// green verdict per service. Blend rules, in order:
//
//	RED:    1+ open critical problem
//	     OR errorRate > 5%
//	YELLOW: 1+ open warning problem
//	     OR errorRate > 1%
//	     OR 1+ open info problem
//	GREEN:  otherwise
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
	s.serveCached(w, r, "system-stats", 60*time.Second, func(ctx context.Context) (any, error) {
		st, err := s.store.GetSystemStats(ctx)
		if err != nil || st == nil {
			return st, err
		}
		// Augment with the live in-process ingest data-loss counters. Kept
		// out of GetSystemStats (CH-only) so chstore stays free of any otlp
		// dependency; the handler has both s.store and s.ing. nil-guarded for
		// api-only pods that don't run receivers (distributed mode).
		if s.ing != nil {
			if s.ing.Spans != nil {
				st.Drops.SpansQueueFull = s.ing.Spans.Dropped()
				st.Drops.SpansWriteFailed = s.ing.Spans.WriteFailed()
			}
			if s.ing.Logs != nil {
				st.Drops.LogsQueueFull = s.ing.Logs.Dropped()
				st.Drops.LogsWriteFailed = s.ing.Logs.WriteFailed()
			}
			if s.ing.Metrics != nil {
				st.Drops.MetricsQueueFull = s.ing.Metrics.Dropped()
				st.Drops.MetricsWriteFailed = s.ing.Metrics.WriteFailed()
			}
			// v0.8.282 — intentional ingest-pipeline drops per signal
			// (drop / sample rules). Read straight off the Ingester's
			// atomic counters, not the consumers.
			st.Drops.SpansPipeline = int64(s.ing.SpansDroppedByPipeline())
			st.Drops.LogsPipeline = int64(s.ing.LogsDroppedByPipeline())
			st.Drops.MetricsPipeline = int64(s.ing.MetricsDroppedByPipeline())
			// v0.8.328 — OTLP exemplar ingest totals (accepted vs dropped by
			// the require-trace-context gate), same Ingester-atomics pattern.
			st.Exemplars.Ingested = int64(s.ing.ExemplarsIngested())
			st.Exemplars.DroppedNoTrace = int64(s.ing.ExemplarsDroppedNoTrace())
			st.Exemplars.DroppedCapped = int64(s.ing.ExemplarsDroppedCapped())
			// v0.8.329 — span-link ingest totals (accepted vs dropped for an
			// empty/all-zero linked trace id), same pattern.
			st.SpanLinks.Ingested = int64(s.ing.SpanLinksIngested())
			st.SpanLinks.DroppedInvalid = int64(s.ing.SpanLinksDroppedInvalid())
		}
		// v0.9.936 — davranış motorunun tik ölçümü. Süreç-içi atomikler
		// (ingest sayaçlarıyla aynı kablo): motor bu pod'da koşmuyorsa
		// (COREMETRY_MODE=api, ya da lider başka pod) sıfır kalır ve bu
		// DOĞRU cevaptır — o pod gerçekten tarama yapmıyor.
		obs := anomaly.BehaviorObservability()
		st.Behavior = chstore.BehaviorDetectorStats{
			Ticks:          obs.Ticks,
			Candidates:     obs.Candidates,
			LastUnix:       obs.LastUnix,
			LastDurationMs: obs.LastDurationMs,
			// v0.9.957 — bütçe kırılımı + sessizliğin gerekçesi.
			LastQueryMs:       obs.LastQueryMs,
			LastWriteMs:       obs.LastWriteMs,
			LastCandidates:    obs.LastCandidates,
			LastServices:      obs.LastServices,
			LastScarceBuckets: obs.LastScarceBuckets,
			LastError:         obs.LastError,
		}
		// v0.9.1241 — "Kodu da incele" kod-çekme sonuçları. Aynı kablo:
		// süreç-içi atomikler, CH yazımı yok. Bu yol FAIL-OPEN olduğu
		// için başarısızlığı hiçbir ekranda iz bırakmıyordu; toplamda
		// "isabet ediyor mu" sorusunun cevabı yalnız burada.
		// s.devops nil ise (kod entegrasyonu hiç kurulmamış) sıfır
		// kalır ve panel "hiç denenmedi" der — sıfır isabet DEMEZ.
		cobs := s.devops.CodeObservability()
		st.CodeFetch = chstore.CodeFetchStats{
			Attempts:      cobs.Attempts,
			OK:            cobs.OK,
			Partial:       cobs.Partial,
			LastUnix:      cobs.LastUnix,
			LastOutcome:   cobs.LastOutcome,
			LastError:     cobs.LastError,
			LastErrorUnix: cobs.LastErrorUnix,
		}
		for _, m := range cobs.Misses {
			st.CodeFetch.Misses = append(st.CodeFetch.Misses,
				chstore.CodeFetchMiss{Class: m.Class, Count: m.Count})
		}
		// v0.9.1344 — bildirim yönlendirme sonucu. Aynı kablo: süreç-içi
		// atomikler (chstore, notify'ı import EDEMEZ — ters yön zaten var).
		//
		// NEDEN GEREKLİ: hiçbir kanalla eşleşmeyen bir problem SESSİZCE
		// düşüyordu; ne sayaç ne log ne işaret vardı. Unmatched'in
		// sıfırdan farklı olması tek başına bir aksiyon çağrısı.
		// Unconfigured AYRI kova ve bilinçli: kanalsız bir kurulumda her
		// problem oraya düşer ve bu bir kusur DEĞİLDİR.
		//
		// Sayaç süreç-içi olduğu için lider olmayan / worker koşmayan
		// pod'larda sıfır kalır ve bu DOĞRU cevaptır — kalıcı defter
		// notification_log'da (bkz. /events ve problem detayındaki
		// "Bildirim" paneli).
		robs := notify.RoutingObservability()
		st.NotifyRouting = chstore.NotifyRoutingStats{
			Delivered:            robs.Delivered,
			Suppressed:           robs.Suppressed,
			Unconfigured:         robs.Unconfigured,
			Unmatched:            robs.Unmatched,
			LastUnmatchedUnix:    robs.LastUnmatchedUnix,
			LastUnmatchedID:      robs.LastUnmatchedID,
			LastUnmatchedService: robs.LastUnmatchedService,
			LastUnmatchedReason:  robs.LastUnmatchedReason,
		}
		// v0.8.212 — surface the duplicate-worker HA hazard (Redis configured but
		// the lock fell back to always-leader Noop). main.go owns the lock state;
		// since v0.8.341 the re-probe clears it live, hence the atomic load.
		st.Health.LockDegraded = s.lockDegraded.Load()
		// v0.8.230 — ES query-failure visibility. The ES logstore counts its
		// failed queries; non-zero here points the operator at
		// /admin/elastic → Recent query errors for the exact requests.
		if diag, ok := logstore.Unwrap(s.logs).(logstore.Diagnoser); ok {
			st.Health.ESQueryErrors = diag.Diagnostics().QueryErrors
		}
		return st, nil
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
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		// v0.9.1062 (Faz 2.1) — pencere ≥5dk ise MV (invariant #3;
		// evaluator'ın useSummaryMV eşiğiyle aynı mantık); 5dk altı
		// serbest pencere isteyen çağıran ham yolda kalır (MV 5dk
		// grid'inde dar pencere tek kovaya yapışır, kıyas anlamsızlaşır).
		if windowSec >= 300 {
			return s.store.GetCorrelatedChangesMV(ctx, at, windowSec, baselineSec)
		}
		return s.store.GetCorrelatedChanges(ctx, at, windowSec, baselineSec)
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
	s.serveCached(w, r, "redis-stats", 5*time.Second, func(ctx context.Context) (any, error) {
		if s.cache == nil {
			return cache.RedisStats{}, nil
		}
		return s.cache.Stats(ctx)
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
	s.serveCached(w, r, "cardinality", 5*time.Minute, func(ctx context.Context) (any, error) {
		return s.store.GetCardinality(ctx)
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
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		rows, err := s.store.GetServiceClusterBreakdown(ctx, name, from, to)
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
	// v0.9.1039 — global Topbar env picker. Narrows the two span-derived
	// slots this brief scopes (service KPI → header dot + tile fallback,
	// operations table) to deploy_env via the raw path. The other slots
	// (problems / deploys / endpoints) are OUT of the env(a) scope and
	// stay all-env; env still joins the cache key so the two truths never
	// cross-poison one bundle entry (v0.5.187 class).
	env := strings.TrimSpace(q.Get("env"))

	key := fmt.Sprintf("svc-bundle:svc=%s:since=%s:from=%s:to=%s:env=%s",
		svc, q.Get("since"), q.Get("from"), q.Get("to"), env)
	// 60s soft TTL — every slot on this bundle is operator-
	// curated (catalog, problems, operations summary, deploys)
	// rather than per-span data; minute-grain freshness is
	// indistinguishable to a human and the SWR tier from
	// v0.5.36 extends stale-but-usable to 3 min. Combined
	// with the v0.5.66 hover-prefetch this means most
	// operator clicks within their session land on a HIT-L1.
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		type result struct {
			Service    *chstore.ServiceSummary    `json:"service"`
			Problems   []chstore.Problem          `json:"problems"`
			Operations []chstore.OperationSummary `json:"operations"`
			Deploys    []chstore.Deploy           `json:"deploys"`
			// v0.9.377 (redesign D1) — Overview'un Top endpoints kartı:
			// GİRİŞ span'leri (server+consumer; operation_summary_5m'in
			// kind boyutu yok, o yüzden operations listesi client
			// span'lerini de içerir ve "endpoint" diye etiketlenemez).
			// HTTP + RPC/messaging girişleri birleşik, toplam wall-clock
			// (calls×avg) sıralı. Boş kalırsa UI eski Operations
			// kartına düşer — görünmez-düşme yok.
			Endpoints []chstore.EndpointRow `json:"endpoints"`
		}
		out := &result{}

		// Fan out in parallel — each branch sets its slot of
		// the result struct under its own goroutine. Errors
		// log + leave the field at zero value (nil slice or
		// pointer) so a single CH blip on one query doesn't
		// blank the whole panel. WaitGroup keeps the
		// goroutines bounded to this request's lifetime.
		var wg sync.WaitGroup
		wg.Add(5)

		go func() {
			defer wg.Done()
			// v0.9.1039 — env narrows the service KPI too, so the header
			// dot + tile fallback agree with the env-scoped RED tiles. The
			// summary MV carries no deploy_env dim, so a non-empty env takes
			// the raw-spans path (GetServicesQuery, Env set — the same
			// servicesUseMV trade-off /api/services makes). NameMatch is a
			// substring, so the exact-name loop below still selects svc.
			var list []chstore.ServiceSummary
			var err error
			if env != "" {
				list, err = s.store.GetServicesQuery(ctx, chstore.ServicesQuery{
					From: from, To: to, NameMatch: svc, Env: env,
					Sort: "spanCount", Dir: "desc", Limit: 50,
				})
			} else {
				list, err = s.store.GetServicesAggFiltered(ctx, from, to,
					svc, "spanCount", "desc", 1, 0)
			}
			if err != nil {
				log.Printf("[svc-bundle] services %s (env=%q): %v", svc, env, err)
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
			probs, err := s.store.ListProblems(ctx, chstore.ProblemFilter{
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
			ops, err := s.store.GetOperationSummary(ctx, svc, since, from, to, false, env)
			if err != nil {
				log.Printf("[svc-bundle] operations %s (env=%q): %v", svc, env, err)
				return
			}
			out.Operations = ops
		}()

		go func() {
			defer wg.Done()
			out.Deploys = s.serviceDeploysNonBlocking(ctx, svc, from, to)
		}()

		go func() {
			defer wg.Done()
			out.Endpoints = s.bundleTopEndpoints(ctx, svc, from, to)
		}()

		wg.Wait()
		return out, nil
	})
}

// bundleTopEndpoints — Overview kartının giriş-span endpoint listesi
// (v0.9.377, redesign D1). İki bounded MV okuması (EntryHTTP + EntryRPC,
// SkipStatus: raw sidecar'sız), Go'da calls×avg DESC birleşik top-12.
// Best-effort: hata boş liste bırakır, bundle düşmez (diğer slotlarla
// aynı sözleşme).
func (s *Server) bundleTopEndpoints(ctx context.Context, svc string, from, to time.Time) []chstore.EndpointRow {
	var rows []chstore.EndpointRow
	for _, entry := range []chstore.EntryKind{chstore.EntryHTTP, chstore.EntryRPC} {
		part, err := s.store.GetEndpoints(ctx, chstore.EndpointsQuery{
			From: from, To: to, Service: svc, Limit: 12,
			Sort: "totalTime", Dir: "desc", Entry: entry, SkipStatus: true,
		})
		if err != nil {
			log.Printf("[svc-bundle] endpoints(%s) %s: %v", entry, svc, err)
			continue
		}
		rows = append(rows, part...)
	}
	sort.Slice(rows, func(i, j int) bool {
		ti := float64(rows[i].Calls) * rows[i].AvgMs
		tj := float64(rows[j].Calls) * rows[j].AvgMs
		if ti != tj {
			return ti > tj
		}
		return rows[i].Path < rows[j].Path
	})
	if len(rows) > 12 {
		rows = rows[:12]
	}
	return rows
}

// deployCacheTTL — deploy markers move on a ROLLOUT cadence (hours),
// not a request cadence, so a value that is minutes stale reads
// identically to a fresh one.
const deployCacheTTL = 10 * time.Minute

// deployFillBudget — ceiling on the background fill. Sits just above
// serviceDeploysSQL's own `max_execution_time = 15` so ClickHouse, not
// the Go context, is what reports a timeout (a CH-side abort names the
// query in system.query_log; a Go-side cancel just says "context
// canceled").
const deployFillBudget = 20 * time.Second

// deployWindowBucket snaps the bundle window to a 5-minute grid for the
// deploys cache key. cacheBucket's 30s grid is right for per-request
// data, but a rolling `to=now` window would mint a fresh key on every
// poll — the expensive fill would be recomputed forever and never
// actually reused. Deploy history at 5-minute granularity is
// indistinguishable to an operator.
func deployWindowBucket(from, to time.Time) string {
	const grid = 5 * time.Minute
	return fmt.Sprintf("%d_%d", from.Truncate(grid).UnixNano(), to.Truncate(grid).UnixNano())
}

// serviceDeploysNonBlocking returns the service's deploy markers from
// cache, and on a miss kicks a background fill instead of waiting for
// one.
//
// v0.9.244 (operator-reported: /api/services/<svc>/bundle took 17.41s in
// prod). Of the bundle's four fan-out legs this is the only one that
// scans RAW spans, and it scans a 48h lookback (deployLookback — the
// v0.9.205 phantom-marker fix) under `max_execution_time = 15`. At prod
// span volume it burned the full 15s for essentially every service, and
// because the fan-out joins on wg.Wait() that single leg gated the whole
// response while the other three finished in ~200ms.
//
// Measured on local CH (2026-07-25): the query costs ~58ms over 205k
// rows and is linear in rows scanned — the per-row cost is dominated by
// decompressing res_keys/res_values, and ClickHouse already does a good
// job on the multiIf (a fingerprint-first rewrite benchmarked SLOWER,
// 65ms, so it was dropped). Prod's 15s therefore means tens of millions
// of rows per service, which no query-shape change fixes. Making the
// slot async is what actually returns the bundle to the speed of its
// fast legs; a version/deploy MV is the real fix for the query itself
// and is queued separately.
//
// A nil slot is already the established degradation here — the previous
// code left out.Deploys nil on any error and the Deploy History panel
// renders empty rather than breaking. So a cold first view loses nothing
// it wasn't already losing in prod, and every later view is a cache hit.
func (s *Server) serviceDeploysNonBlocking(
	ctx context.Context, svc string, from, to time.Time,
) []chstore.Deploy {
	key := fmt.Sprintf("svc-deploys:v1:svc=%s:w=%s", svc, deployWindowBucket(from, to))
	if raw, ok, err := s.cache.Get(ctx, key); err == nil && ok && len(raw) > 0 {
		var deps []chstore.Deploy
		if json.Unmarshal(raw, &deps) == nil {
			return deps
		}
	}
	go func() {
		// singleflight under the cache key: one service watched by
		// several operators (or polled by one tab) must not fan out
		// into N concurrent 48h scans.
		_, _, _ = s.sf.Do("fill:"+key, func() (any, error) {
			// Fresh context — the request that triggered this fill has
			// already returned, so the request context is cancelled.
			fctx, cancel := context.WithTimeout(context.Background(), deployFillBudget)
			defer cancel()
			deps, err := s.store.GetServiceDeploys(fctx, svc, from, to)
			if err != nil {
				log.Printf("[svc-deploys] fill %s: %v", svc, err)
				return nil, err
			}
			body, err := json.Marshal(deps)
			if err != nil {
				log.Printf("[svc-deploys] marshal %s: %v", svc, err)
				return nil, err
			}
			return nil, s.cache.Set(fctx, key, body, deployCacheTTL)
		})
	}()
	return nil
}

func (s *Server) getServiceStructure(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
		return
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
	s.serveCached(w, r, key, time.Hour, func(ctx context.Context) (any, error) {
		roots, totalSpans, sampledFrom, err := s.store.AggregateServiceStructure(
			ctx, name, since, samples, internalOnly)
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
	// v0.10.365 — metricSource seam (infra_metric.go); CH path verbatim.
	src, err := s.metricSourceFor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	key := fmt.Sprintf("infra-metrics:%s:svc=%s:since=%s", src.Name(), name, since)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		if src.Name() == metricSourceCH {
			return s.store.GetInfraMetrics(ctx, name, since, 0)
		}
		return buildInfraMetric(ctx, src, name, since, time.Now())
	})
}

// getServiceInstances returns one row per pod/host emitting metrics for the
// service — latest CPU% / memory (and memory % when a limit is reported) per
// host_name. Powers the per-pod "Instances" card on the Service Overview tab.
// One bounded metric_points query (no raw-spans scan). 30s cache.
func (s *Server) getServiceInstances(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
		return
	}
	since := parseDuration(r.URL.Query().Get("since"), 15*time.Minute)
	to := time.Now()
	from := to.Add(-since)
	key := fmt.Sprintf("svc-instances:svc=%s:since=%s", name, since)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.ServiceInstances(ctx, name, from, to)
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
	s.serveCached(w, r, key, 5*time.Minute, func(ctx context.Context) (any, error) {
		// v0.9.1053 — canlı semantik: şimdiye göre parmak izi.
		return s.store.GetServiceRuntime(ctx, name, time.Now())
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
	s.serveCached(w, r, "all-service-runtimes", 5*time.Minute, func(ctx context.Context) (any, error) {
		// v0.9.46 — 15m tarama + merge + hata durumunda stale-serve
		// (service_runtimes_cache.go): prod'da 1h taramanın timeout'u
		// tüm rozetleri birden söndürüyordu.
		return s.serviceRuntimesMerged(ctx)
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
	// Preserve the deriver-managed provenance columns — the PUT body doesn't
	// carry owner_team_auto/sre_team_auto, so copy them from the existing row.
	// Otherwise an unrelated edit (e.g. description) would zero them and
	// accidentally pin the auto-derived team, blocking future rename propagation
	// (v0.8.100).
	if existing, _ := s.store.GetServiceMetadata(r.Context(), name); existing != nil {
		m.OwnerTeamAuto = existing.OwnerTeamAuto
		m.SRETeamAuto = existing.SRETeamAuto
		// v0.8.438 — namespace_auto da aynı sınıf: PUT gövdesi taşımaz
		// (json:"-"); kopyalanmazsa alakasız bir katalog düzenlemesi
		// provenance'ı sıfırlar ve mergeNamespace türetilmiş namespace'i
		// insan pini sanıp sonsuza dek günceller (review-confirmed).
		m.NamespaceAuto = existing.NamespaceAuto
	}
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
	s.serveCached(w, r, "services-metadata", 60*time.Second, func(ctx context.Context) (any, error) {
		return s.store.ListServiceMetadata(ctx)
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
	s.serveCached(w, r, key, time.Minute, func(ctx context.Context) (any, error) {
		return s.store.GetServiceDeploys(ctx, name, from, to)
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
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		deploys, err := s.store.GetServiceDeploys(ctx, name, from, to)
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
				ctx, name, d.Version, d.TimeUnixNs, windowSec)
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

// getServiceRollouts returns pod-churn rollout events for a service
// (instance-set turnover, v0.8.x) with per-rollout before/after RED
// impact. Replaces version-bump deploy markers when service.version
// is constant; the response's versionConstant flag lets the UI hide
// the version chip everywhere it would otherwise render "1.0.0".
// impactStart returns the first index of the newest-`cap` window over an
// ASC (oldest-first) slice of length n — the rollouts whose RED impact is
// worth paying ComputeDeployImpact for. Pure; pinned by impact_window_test.
func impactStart(n, cap int) int {
	if n <= cap {
		return 0
	}
	return n - cap
}

func (s *Server) getServiceRollouts(w http.ResponseWriter, r *http.Request) {
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
	// v0.9.360 — anahtar 30s grid'e oturur (cacheBucket, kardeş handler'lar
	// gibi). Eski anahtar nanosaniye-hassastı ve HİÇ tutmuyordu: her Details
	// açılışı 5-dk bucket'lı groupUniqArray taramasını + 8 rollout için
	// ComputeDeployImpact'i sıfırdan ödüyordu — üstelik sekme aynı ucu üç
	// ayrı pencereden üç kez çağırıyordu.
	key := fmt.Sprintf("service-rollouts:svc=%s:w=%s", name, cacheBucket(from, to))
	s.serveCached(w, r, key, time.Minute, func(ctx context.Context) (any, error) {
		res, err := s.store.GetServiceRollouts(ctx, name, from, to)
		if err != nil {
			return nil, err
		}
		// Fill before/after RED impact per rollout (cap to bound cost,
		// like deploy-history). Best-effort — a failed compute leaves
		// impact nil and the UI shows "—" deltas.
		// v0.9.365 — pencere EN YENİ 8: GetServiceRollouts ASC döner ve
		// eski döngü ilk 8'i (en eski) hesaplıyordu; 7g penceresinde çok
		// rollout'lu bir serviste operatörün baktığı GÜNCEL satırlar hep
		// "—" kalıyordu. Sıra sözleşmesi (ASC) değişmedi — strip
		// deploys[len-1] ile en yeniyi seçmeye devam eder.
		const maxImpact = 8
		for i := impactStart(len(res.Rollouts), maxImpact); i < len(res.Rollouts); i++ {
			imp, err := s.store.ComputeDeployImpact(
				ctx, name, res.Rollouts[i].VersionAfter,
				res.Rollouts[i].TimeUnixNs, 600)
			if err != nil {
				log.Printf("[rollouts] impact %s @%d: %v", name, res.Rollouts[i].TimeUnixNs, err)
				continue
			}
			res.Rollouts[i].Impact = imp
		}
		return res, nil
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
	// v0.10.16 (F0.2a) — anahtar nanosaniye taşıdığı sürece `to`
	// şimdi-tabanlı olduğu için HER istek benzersizdi: cache'in dört
	// mekanizması da ölüydü. Pencere ve anahtar aynı dakika kovasından
	// türetiliyor ki veriyle adı uyuşsun (service_dbqueries_key.go).
	from, to = serviceDBQueriesWindow(from, to)
	key := serviceDBQueriesKey(name, from, to, limit)
	s.serveCached(w, r, key, time.Minute, func(ctx context.Context) (any, error) {
		return s.store.GetTopDBQueries(ctx, name, from, to, limit)
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
	// v0.9.272 — narrow by the actual database (Oracle service name / SID,
	// PostgreSQL or MongoDB database name), not just the engine.
	dbName := strings.TrimSpace(q.Get("db_name"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	// dbName is part of the cache key: leaving it out would serve one
	// database's rows for another's request — the cross-poisoning class
	// CLAUDE.md calls out (v0.5.187).
	key := fmt.Sprintf("slow-queries-global:from=%d:to=%d:sys=%s:db=%s:limit=%d",
		from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute),
		dbSystem, dbName, limit)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetSlowQueriesGlobal(ctx, from, to, dbSystem, dbName, limit)
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
		http.Error(w, "service name required", http.StatusBadRequest)
		return
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
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		// v0.5.368 — read from service_callers_5m MV. The
		// raw-spans ServiceCallers() runs a self-join that's
		// unviable at billion-span scale; topology aggregator
		// now pre-aggregates the rollup every 5 min so reads
		// stay sub-second regardless of fleet size.
		rows, err := s.store.ReadServiceCallersAgg(ctx, name, from, to, limit)
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
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		// v0.9.1047 — canlı uç: [now-since, now] davranışı bayt-bayt aynı;
		// pencere artık çağıran tarafta kurulur (kök-neden yolu problem
		// penceresini geçer, buradaki "şu an" anlamı değişmedi).
		now := time.Now()
		return s.store.GetServiceBlastRadius(ctx, name, now.Add(-since), now)
	})
}

func (s *Server) getServiceNeighbors(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
		return
	}
	since := parseDuration(r.URL.Query().Get("since"), time.Hour)
	// v0.9.257 — ceiling, mirroring getServiceBlastRadius above. ServiceNeighbors
	// picks the top-N traces with GROUP BY trace_id over raw `spans`; left
	// unbounded a 720h window blows max_execution_time and the panel just errors
	// out. Callers clamp too (rangeToSince in lib/utils.ts) and SAY they clamped
	// — this is the server-side backstop for anyone who doesn't.
	if since > 24*time.Hour {
		since = 24 * time.Hour
	}
	samples := parseInt(r.URL.Query().Get("samples"), 50)

	key := fmt.Sprintf("service-neighbors:svc=%s:since=%s:samples=%d",
		name, since, samples)
	// v0.9.227 (operatör) — 1h → 24h. Bir servisin komşuluğu deploy
	// temposunda değişir, dakika temposunda değil; 1 saat gereğinden sık
	// yeniden hesaplatıyordu. serveCached'in SWR'ı bunu güvenli kılıyor:
	// TTL geçse bile kayıt staleFactor×TTL boyunca ANINDA servis edilir ve
	// arka planda tazelenir (singleflight ile tekilleştirilmiş), yani uzun
	// TTL "eski veri" değil "ilk isteyen bekleme" demek. Taze veri şart
	// olduğunda ?refresh=1 okumayı atlar.
	s.serveCached(w, r, key, 24*time.Hour, func(ctx context.Context) (any, error) {
		upstream, downstream, sampledFrom, totalSpans, err := s.store.ServiceNeighbors(
			ctx, name, since, samples)
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
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		rows, err := s.store.GetServiceAttrs(ctx, name, from, to, top, samples)
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

// endpointsListKey builds the /api/endpoints cache key from ALL
// inputs (hash-all-inputs rule; the v0.5.187 class). env joined in
// v0.8.385 — without it an env-filtered table would cross-poison the
// unfiltered one inside the same 30s bucket.
// v0.9.313 — entry joins the key: the HTTP and RPC tabs are DIFFERENT
// row sets from the same endpoint, so sharing an entry would serve one
// tab's table under the other's heading.
// sortEndpointsByP99Delta — SAF: P99 kötüleşme yüzdesine göre azalan;
// prior'u olmayanlar (yeni endpoint, delta tanımsız) gerçek
// regresyonların ARKASINA, kendi içinde calls DESC. Eşitlikte
// (service, path) — refetch'ler arası kararlı. Sonuç limit'e kesilir.
func sortEndpointsByP99Delta(rows []chstore.EndpointRow, limit int) []chstore.EndpointRow {
	deltaOf := func(r chstore.EndpointRow) (float64, bool) {
		if r.PriorP99Ms <= 0 {
			return 0, false
		}
		return (r.P99Ms - r.PriorP99Ms) / r.PriorP99Ms, true
	}
	sort.SliceStable(rows, func(i, j int) bool {
		di, oi := deltaOf(rows[i])
		dj, oj := deltaOf(rows[j])
		if oi != oj {
			return oi // tanımlı delta önce
		}
		if !oi {
			return rows[i].Calls > rows[j].Calls
		}
		if di != dj {
			return di > dj
		}
		if rows[i].Service != rows[j].Service {
			return rows[i].Service < rows[j].Service
		}
		return rows[i].Path < rows[j].Path
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// endpointsPoolCap — p99Delta aday havuzunun üst sınırı. 10000 KEYFİ
// DEĞİL: chstore.GetEndpoints'in kendi tavanı da 10000 ve üstünü
// SESSİZCE 500'e düşürür (`q.Limit > 10000 → 500`), yani daha büyük bir
// havuz istemek havuzu büyütmez, çökertir. Aynı sayı UI'daki
// "All (10000)" seçeneğinin zaten ödediği maliyet: ölçüm (lokal 24s
// penceresi, 1.15M MV satırı, query_log medyanı, n=7) LIMIT 1000 →
// 595ms / 33MiB, LIMIT 10000 → 843ms / 121MiB; TARAMA İKİSİNDE AYNI
// (read_rows birebir eşit), fark yalnız sıralama+serileştirme. 1s
// altı = sıcak olmayan okuma bütçesi içinde.
const endpointsPoolCap = 10000

// endpointsPool, p99Delta sıralamasının aday havuzunu ve havuzun
// tasarım niyetinden kısılıp kısılmadığını döndürür.
//
// v0.9.762'de havuz `clamp(limit*5, 500, 1000)` idi: limit ∈
// {2000,5000,10000} seçildiğinde havuz 1000'de kalıyordu, sonuç da
// havuzdan büyük olamayacağı için tablo EN FAZLA 1000 satır dönüyordu —
// menüdeki üç seçenek ÖLÜYDÜ ve hiçbir şey bunu söylemiyordu. Tavan
// artık limit'ten bağımsız (endpointsPoolCap), böylece havuz her menü
// seçeneği için ≥ limit.
//
// capped = tasarım niyeti (limit×5 aday) tavana takıldı demektir;
// sıralama evreni istenen genişlikten dar. Çağıran bunu havuzun GERÇEK
// dolduğu durumla birleştirir (aşağıda) — 26 endpoint'lik bir kurulumda
// "liste eksik olabilir" uyarısı basmak, bu düzeltmenin kaldırmaya
// çalıştığı sessiz yalanın aynadaki hâli olurdu.
func endpointsPool(limit int) (pool int, capped bool) {
	want := limit * 5
	pool = want
	if pool < 500 {
		pool = 500
	}
	if pool > endpointsPoolCap {
		pool = endpointsPoolCap
	}
	return pool, pool < want
}

// endpointsPoolCapped — şeridi basıp basmayacağımıza karar veren SAF
// yüklem. İKİ koşul birden gerekir:
//
//	want   — havuz tasarım niyetinden (limit×5) kısıldı, VE
//	filled — havuz gerçekten doldu (dönen aday sayısı havuz kadar).
//
// İkincisi olmadan uyarı yalan olur: 26 endpoint'lik bir kurulumda
// evrenin tamamı havuza sığar, dışarıda kalan hiçbir şey yoktur ve
// "liste eksik olabilir" demek, bu sürümün kaldırdığı sessiz yanlışın
// ta kendisidir — sadece ters yönde.
func endpointsPoolCapped(pool int, want bool, gotRows int) bool {
	return want && gotRows >= pool
}

// endpointsListResponse — /api/endpoints zarfı (v0.9.812). Liste
// eskiden çıplak dizi dönüyordu; p99Delta sıralamasının "evren = en çok
// çağrılan ilk N" gerçeğini taşıyacak bir yer yoktu, UI da bunu
// sabit "~1000" metniyle tahmin ediyordu. pool/poolCapped yalnız delta
// sıralamasında dolar (diğer sıralamalarda havuz kavramı yok).
type endpointsListResponse struct {
	Rows []chstore.EndpointRow `json:"rows"`
	// Pool — sıralama evreninin boyutu (çağrıya göre ilk N aday).
	Pool int `json:"pool,omitempty"`
	// PoolCapped — havuz tasarım niyetinden (limit×5) kısıldı VE
	// gerçekten doldu: evrenin dışında kalan endpoint'ler var.
	PoolCapped bool `json:"poolCapped,omitempty"`
}

// v0.9.812 — "v2" ad alanı, yanıtın çıplak diziden zarfa geçmesi
// yüzünden ZORUNLU: yuvarlanan deploy sırasında eski ve yeni pod'lar
// AYNI Redis anahtarlarını paylaşır. Ad alanı olmasa yeni pod eski
// pod'un yazdığı çıplak diziyi okuyup zarf sanır (tablo sessizce boş)
// ya da eski frontend zarfı dizi sanıp render'da patlardı. Ayrı ad
// alanı iki sürümü birbirinden yalıtır; eski anahtarlar kendi TTL'iyle
// düşer.
func endpointsListKey(bucket, service, search, cluster, env string, limit int, compare, bySignature bool, sortBy, sortDir, entry string) string {
	return fmt.Sprintf("endpoints:v2:%s:%s:%s:%s:env=%s:%d:cmp=%v:sig=%v:sort=%s:dir=%s:entry=%s",
		bucket, service, search, cluster, env, limit, compare, bySignature, sortBy, sortDir, entry)
}

// getEndpoints — v0.5.365. Per-endpoint RED rollup driven from
// the OTel http.route attribute on server / consumer spans.
// Operator-asked: a /services-like top-level view, but keyed on
// the inbound request path instead of the service. Top N by the
// requested sort (v0.8.356 — server-side ORDER BY before the
// LIMIT; default calls DESC) so the table is a true global
// ranking, not a re-sorted top-N-by-calls page.
func (s *Server) getEndpoints(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to := parseFromTo(r, time.Hour)
	service := q.Get("service")
	search := q.Get("search")
	cluster := q.Get("cluster")
	// Global env filter (v0.8.385, env-separation Phase 2) — the
	// Topbar picker's ?env=. Forces the raw-spans path (deploy_env
	// conjunct) exactly like cluster; the spanmetrics_1m MV has no
	// env dimension and stays untouched.
	env := strings.TrimSpace(q.Get("env"))
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
	// v0.8.x — Uptrace-style "group by shape": cluster paths carrying IDs
	// (/orders/8421) into a normalized signature (/orders/:id) at read time.
	bySignature := q.Get("groupBy") == "signature"
	// v0.8.356 — server-side global sort (whitelisted in
	// chstore.endpointsOrderBy; unknown ids fall back to calls DESC).
	// Rides the cache key like every other input.
	sortBy := q.Get("sort")
	sortDir := q.Get("dir")
	// v0.9.313 (brief N1) — which inbound surface. Unknown values fall
	// back to http so a hand-edited URL lands on the familiar table
	// rather than an empty one.
	entry := chstore.EntryHTTP
	if strings.TrimSpace(r.URL.Query().Get("entry")) == "rpc" {
		entry = chstore.EntryRPC
	}
	key := endpointsListKey(cacheBucket(from, to), service, search, cluster, env, limit, compare, bySignature, sortBy, sortDir, string(entry))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		// v0.8.356 — default path reads the spanmetrics_1m MV; the raw
		// CTE survives only behind the cluster + env filters
		// (dimensions the MV lacks). Dispatch lives in
		// chstore.GetEndpoints.
		// v0.9.761 (mockup onayı: "kötüleşenler önce") — sort=p99Delta
		// SUNUCUDA sıralanır ama delta SQL'de yok: aday havuzu deseni.
		// Çağrıya göre ilk N*5 (500..1000) aday çekilir, prior birleşir,
		// delta hesaplanıp sıralanır, orijinal limit'e kesilir. Dürüstlük:
		// evren "en çok çağrılan ilk <havuz>" — UI notu bunu söyler.
		// compare bu sıralamada ZORUNLU (delta prior'suz tanımsız).
		deltaSort := sortBy == "p99Delta"
		if deltaSort {
			compare = true
		}
		eq := chstore.EndpointsQuery{
			From: from, To: to,
			Service: service, Search: search, Cluster: cluster, Env: env,
			Limit: limit, BySignature: bySignature,
			Sort: sortBy, Dir: sortDir,
			Entry: entry,
		}
		// v0.9.812 — havuz artık limit'e saygılı (endpointsPool);
		// eski clamp(…, 1000) menünün üst üç seçeneğini ölü bırakıyordu.
		pool, poolWantCapped := 0, false
		if deltaSort {
			pool, poolWantCapped = endpointsPool(limit)
			eq.Limit = pool
			eq.Sort, eq.Dir = "calls", "desc" // aday havuzu tabanı
		}
		rows, err := s.store.GetEndpoints(ctx, eq)
		if err != nil {
			return nil, err
		}
		// Havuz uyarısı yalnız havuz GERÇEKTEN dolduğunda anlamlı:
		// evren tavandan darsa ama tüm endpoint'ler havuza sığdıysa
		// eksik bir şey yok.
		resp := endpointsListResponse{Rows: rows}
		if deltaSort {
			resp.Pool = pool
			resp.PoolCapped = endpointsPoolCapped(pool, poolWantCapped, len(rows))
		}
		if !compare || len(rows) == 0 {
			return resp, nil
		}
		// Prior window: same length, shifted back by exactly the
		// window width so the comparison stays apples-to-apples.
		// SkipStatus: the delta merge only reads calls/errors/avg/p99,
		// so the prior read skips the status/method sidecar.
		dur := to.Sub(from)
		prior := eq
		prior.From = from.Add(-dur)
		prior.To = from
		prior.SkipStatus = true
		priorRows, err := s.store.GetEndpoints(ctx, prior)
		if err != nil {
			// Prior failure is non-fatal — return current rows
			// without trends rather than 500'ing the page. Delta
			// sıralaması prior'suz yapılamaz: havuz satırları
			// (limit'ten büyük olabilir) yine limit'e kesilir,
			// yoksa "top 100" sessizce 500 satır dönerdi.
			if deltaSort && limit > 0 && len(rows) > limit {
				rows = rows[:limit]
			}
			resp.Rows = rows
			return resp, nil
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
		if deltaSort {
			rows = sortEndpointsByP99Delta(rows, limit)
		}
		resp.Rows = rows
		return resp, nil
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
	// topN (v0.8.215) — overview cap: bound the rendered graph to the heaviest N
	// services so a 1000s-service prod map isn't an unreadable hairball. 0 = no
	// cap (the full sampled graph, unchanged default).
	topN := parseInt(r.URL.Query().Get("topN"), 0)

	hidPats := s.topologyHiddenPatterns(r.Context())
	key := fmt.Sprintf("service-map:since=%s:samples=%d:diff=%s:topN=%d:hid=%s", since, samples, diffStr, topN, hiddenDigest(hidPats))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		var m *chstore.ServiceMap
		var err error
		if diff > 0 {
			m, err = s.store.GetServiceMapWithDiff(ctx, since, samples, diff, diffStr)
		} else {
			m, err = s.store.GetServiceMap(ctx, since, samples)
		}
		if err != nil {
			return nil, err
		}
		// v0.8.241 — hidden-pattern policy on the legacy map too.
		if hidden := hiddenNodeMatcher(hidPats); len(hidPats) > 0 {
			nodes := m.Nodes[:0]
			for _, n := range m.Nodes {
				if hidden(n.Service) {
					continue
				}
				nodes = append(nodes, n)
			}
			m.Nodes = nodes
			edges := m.Edges[:0]
			for _, e := range m.Edges {
				if hidden(e.Caller) || hidden(e.Callee) {
					continue
				}
				edges = append(edges, e)
			}
			m.Edges = edges
		}
		s.store.PruneServiceMapTopN(m, topN)
		// v0.8.297 — db pill'i "oracle" değil "oracle · COREBANK" okusun:
		// dominant db.name enrichment (best-effort, MV yolundaki v0.8.37
		// davranışının aynısı). Prune SONRASI: yalnız görünen düğümler.
		now := time.Now()
		if dbNames, err := s.store.DbNamesBySystem(ctx, now.Add(-since), now); err == nil {
			s.store.AnnotateDbNames(m.Nodes, dbNames)
		}
		return m, nil
	})
}

// getServiceSparklines returns one bucket array per service for the
// requested window, sourced from the 5-minute summary MV. Used by the
// services list to render thumbnail charts next to each row without a
// separate round-trip per service.
//
// Response shape:
//
//	{ "<service>": [ { "t": <unix ns>, "spans": N, "errs": N,
//	                   "avgMs": F, "p99Ms": F }, ... ] }
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
	// Sort the service set (display order is result-irrelevant — an unsorted
	// join fragments the cache) and bucket the window so the now()-anchored
	// range can't defeat the 30s TTL. (scale-audit v0.8.201)
	sortedSvcs := append([]string(nil), wantSvcs...)
	sort.Strings(sortedSvcs)
	// v0.10.262 (CDV-1) — anahtar v2: gövde artık slot grid'i (≤120 dilim).
	// v0.10.269 — slot genişliği CH'ye iner (GROUP BY slot, tek tDigest
	// merge); sparklineSlots artık geçiş (aynı grid, aynı origin) — ≤120
	// slot garantisi ve grid hizası orada kalır. Anahtar v3: slot girer.
	// v0.10.286 (D1) — istemci piksel bütçesi: ?maxSlots= basamağa snap
	// (40/60/80/120); yoksa 120 = eski gövde. Anahtar v4: basamak girer.
	maxSlots := sparklineSlotRung(parseInt(q.Get("maxSlots"), 0))
	width := sparklineSlotWidth(from, to, maxSlots)
	key := fmt.Sprintf("services-spark:v4:window=%s:slot=%d:max=%d:svcs=%s",
		cacheBucket(from, to), int(width/time.Second), maxSlots, strings.Join(sortedSvcs, ","))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		rows, err := s.store.GetServiceSummarySlots(ctx, wantSvcs, from, to, width)
		if err != nil {
			return nil, err
		}
		// 7 g × 50 servis ≈ 100k nesne → ≤maxSlots slot × servis (sparkline_slots.go).
		return sparklineSlots(rows, from, to, maxSlots), nil
	})
}

// getServiceNames is the lookup endpoint behind every service-name picker
// in the UI. Distinct service names from the MV (cheap — already
// per-service grouped), with wildcard substring search and paging.
//
// Why a dedicated endpoint instead of /api/services?
//
//	/api/services is top-N capped (50 by default) so the dashboard can
//	render in sub-second on 10k-service installs. That cap leaks into
//	any picker that scraped the names from the same response — users
//	couldn't pick a less-busy service. This endpoint exists purely for
//	pickers and never aggregates anything beyond DISTINCT.
//
// Wildcard:
//   - "pay"     → substring match (LIKE '%pay%'), case-insensitive
//   - "pay*"    → prefix match (LIKE 'pay%')
//   - "*pay"    → suffix match (LIKE '%pay')
//   - "*pay*"   → explicit substring (same as plain "pay")
//   - "p?y"     → '?' becomes single-char wildcard (LIKE 'p_y')
//
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

	// acache fast path (v0.8.80) — keyed by service, so cross-service search
	// (no service) and deeper paging fall through to the CH path below.
	if service != "" && offset == 0 {
		if names, total, hit := s.autocomplete.GetOperations(r.Context(), service, pattern, limit); hit {
			w.Header().Set("X-Cache", "HIT-ACACHE")
			writeJSON(w, map[string]any{"names": names, "total": total, "hasMore": len(names) < total})
			return
		}
	}

	key := fmt.Sprintf("op-names:svc=%s:q=%s:limit=%d:offset=%d", service, pattern, limit, offset)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		names, total, err := s.store.ListOperationNames(ctx, service, pattern, limit, offset)
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

	// acache fast path (v0.8.80) — frequency-ranked, served from Redis in
	// microseconds. Only the first page (offset 0) comes from the cache; deeper
	// paging and a cold cache fall through to the CH-backed path below.
	if offset == 0 {
		if names, total, hit := s.autocomplete.GetServices(r.Context(), pattern, limit); hit {
			w.Header().Set("X-Cache", "HIT-ACACHE")
			writeJSON(w, map[string]any{"names": names, "total": total, "hasMore": len(names) < total})
			return
		}
	}

	key := fmt.Sprintf("svc-names:q=%s:limit=%d:offset=%d", pattern, limit, offset)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		names, total, err := s.store.ListServiceNames(ctx, pattern, limit, offset)
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
	// v0.9.969 (UX denetimi Ö15) — ABSOLUTE window, opt-in.
	//
	// `since` can only ever mean "the last N", which makes a brushed window
	// unreachable: the client could send its LENGTH and the server scanned
	// that length ending NOW. Zoom into yesterday's 30-minute spike and the
	// suggester answered about the last 30 minutes — a different question,
	// answered silently.
	//
	// Both bounds or neither: a half-specified window is a client bug, and
	// guessing the missing edge (now? epoch?) would produce a confident wrong
	// scan. Falls back to the relative path, which is still the right shape
	// for a relative preset — that one IS now-anchored, and keeping it on
	// `since` keeps its 60s cache entry shared across every operator on the
	// same preset.
	absFrom, absTo := parseTime(q.Get("from")), parseTime(q.Get("to"))
	absolute := !absFrom.IsZero() && !absTo.IsZero() && absTo.After(absFrom)
	// v0.5.261 — context-aware attribute suggester. When the
	// operator already has a filter set in /explore, the
	// dropdown should show attribute keys with data UNDER those
	// filters, not the global top-N. Filters arrive as a
	// JSON-encoded FilterExpr[] under `filters`; empty / missing
	// keeps the old global-scan behaviour.
	rawFilters := q.Get("filters")
	filters, ferr := parseFilters(rawFilters)
	if ferr != nil {
		writeErr(w, ferr)
		return
	}
	// v0.8.x gap-2 — grouped AND/OR builder context. When the operator has a
	// grouped filter set in /explore, the attribute-key suggester should scope
	// to data UNDER that group. filterGroup supersedes the flat filters= (a
	// flat-AND group is byte-identical); both strings enter the cache key so a
	// grouped vs flat context can't cross-poison.
	rawFilterGroup := q.Get("filterGroup")
	root, gerr := parseFilterGroup(rawFilterGroup)
	if gerr != nil {
		writeErr(w, gerr)
		return
	}

	// Cache key carries EVERY input that changes the answer. The window is
	// one key or the other, never both, so a relative and an absolute request
	// can't collide: `since` is empty on the absolute path (from/to carry it)
	// and from/to are 0 on the relative path.
	var keyFrom, keyTo int64
	keySince := q.Get("since")
	if absolute {
		keySince = ""
		// v0.10.256 — 30 s grid (cacheBucket ile aynı basamak): SPA to=Date.now() her poll MISS etmesin.
		keyFrom, keyTo = absFrom.Truncate(cacheRawQueryGrid).UnixNano(), absTo.Truncate(cacheRawQueryGrid).UnixNano()
	}
	key := fmt.Sprintf("attr-keys:since=%s:from=%d:to=%d:limit=%d:f=%s:fg=%s",
		keySince, keyFrom, keyTo, limit, rawFilters, rawFilterGroup)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		// Filter-derived WHERE fragment via the public chstore helper so we
		// don't reach into the package's internal whereClause type. AND-merged
		// with the time floor inside attributeKeysSQL for each union branch.
		var filterSQL string
		var filterArgs []any
		if root != nil {
			filterSQL, filterArgs = chstore.BuildFilterGroupWhere(*root)
		} else {
			filterSQL, filterArgs = chstore.BuildFilterWhere(filters)
		}
		extra := ""
		if filterSQL != "" {
			extra = " AND " + strings.TrimPrefix(filterSQL, "WHERE ")
		}
		// v0.7.30 — sample the inner scan (see attributeKeysSQL). At billions of
		// spans the old full-window arrayJoin timed out (max_execution_time=30
		// → error → empty), and the Traces "Add column" picker showed "no more
		// attribute keys to add". The sample makes it O(sample), not O(window).
		sqlText := attributeKeysSQL(extra, attrKeysSampleRows, absolute)
		// Args layout: span(time…, filter-args...), resource(time…, filter-args...),
		// limit. "time…" is one arg on the relative path and two on the
		// absolute one — the branches must stay symmetric or the positional
		// binds shift and the second branch silently reads the wrong window.
		var timeArgs []any
		if absolute {
			timeArgs = []any{absFrom, absTo}
		} else {
			timeArgs = []any{int64(since.Seconds())}
		}
		args := []any{}
		for range 2 {
			args = append(args, timeArgs...)
			args = append(args, filterArgs...)
		}
		args = append(args, limit)

		rows, err := s.store.Conn().Query(ctx, sqlText, args...)
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

// attrValuesSampleRows bounds the inner scan in getAttributeValues' array path
// when no q is given. Range-bound autocomplete (v0.8.x) can now select a wide
// window, so the sample keeps the GROUP BY O(sample) not O(window); an explicit
// q runs the exact filtered scan instead, so long-tail values stay reachable.
const attrValuesSampleRows = 200_000

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
// v0.9.969 (UX denetimi Ö15) — `absolute` switches the time predicate from
// the now()-anchored duration to explicit bounds.
//
// The relative form can only say "the last N", so a BRUSHED window is
// unreachable by construction: the client could only send the window's
// LENGTH, and the server then scanned that length ending NOW. An operator
// who zoomed into a 30-minute spike yesterday got the attribute keys of the
// last 30 minutes — not a narrower answer, a DIFFERENT one, and silently: a
// key that exists all over the incident simply is not offered, which reads
// as "no such attribute" (the exact misreading Ö14c already fixed for
// window LENGTH).
//
// Both union branches take the same shape, so the arg layout stays
// symmetric: 2 args per branch when absolute, 1 when relative.
func attributeKeysSQL(extra string, sampleRows int, absolute bool) string {
	when := "time >= now() - toIntervalSecond(?)"
	if absolute {
		when = "time >= ? AND time <= ?"
	}
	return fmt.Sprintf(`
		SELECT scope, k, count() AS c FROM (
			SELECT 'span' AS scope, arrayJoin(attr_keys) AS k
			FROM (
				SELECT attr_keys FROM spans
				WHERE %s%s
				LIMIT %d
			)
			UNION ALL
			SELECT 'resource' AS scope, arrayJoin(res_keys) AS k
			FROM (
				SELECT res_keys FROM spans
				WHERE %s%s
				LIMIT %d
			)
		)
		GROUP BY scope, k
		ORDER BY c DESC
		LIMIT ?
		SETTINGS max_execution_time = 25`, when, extra, sampleRows, when, extra, sampleRows)
}

// getAttributeValues returns the most-frequent values observed for a
// given attribute key over a recent time window. Powers the
// FilterBuilder value autocomplete: as soon as the operator picks an
// attribute they get a real top-N value list, not a blank field.
//
// Two paths depending on the key:
//  1. Well-known semconv key with a dedicated structured column
//     (http.method → http_method, db.system → db_system, etc.) →
//     query the LowCardinality column directly. O(rows) but the
//     compressed column reads cheap.
//  2. Anything else → array index lookup
//     attr_values[indexOf(attr_keys, ?)] (or res_values for the
//     resource-scoped scope).
//
// Result is cached 60s in the same Redis layer the rest of the cheap-
// fan-out endpoints use, keyed by `key:since:limit` — so 100 SREs
// opening the same FilterBuilder generate one CH scan, not 100.
func (s *Server) getAttributeValues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rawKey := strings.TrimSpace(q.Get("key"))
	if rawKey == "" {
		http.Error(w, "key required", http.StatusBadRequest)
		return
	}
	if !isSafeAttrKey(strings.TrimPrefix(strings.TrimPrefix(rawKey, "resource."), "span.")) {
		http.Error(w, "invalid key", http.StatusBadRequest)
		return
	}
	since := parseDuration(q.Get("since"), time.Hour)
	// v0.8.x (trace-query gap-1) — range-bind the value autocomplete to the
	// operator's selected window. FilterBuilder used to hard-code since=1h, so
	// a value picked on a 24h/7d traces view never matched the rows in view.
	// from/to take precedence; since stays the back-compat fallback.
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	limit := parseInt(q.Get("limit"), 200)
	if limit > 1000 {
		limit = 1000
	}
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

	// acache fast path (v0.8.80) — low-cardinality keys only. High-cardinality
	// keys come back freeText (HLL count, no values); those fall through to the
	// CH value-search so the operator can still reach long-tail values. A cold
	// cache falls through too. The bare key (scope prefix stripped) matches how
	// the cache stores keys.
	bareKey := strings.TrimPrefix(strings.TrimPrefix(rawKey, "resource."), "span.")
	if vals, _, freeText, hit := s.autocomplete.GetAttributeValues(r.Context(), bareKey, pattern, limit); hit && !freeText && pattern == "" {
		if vals == nil {
			vals = []acache.ValueCount{}
		}
		w.Header().Set("X-Cache", "HIT-ACACHE")
		writeJSON(w, vals)
		return
	}

	cacheKey := fmt.Sprintf("attr-values:%s:since=%s:from=%s:to=%s:limit=%d:q=%s", rawKey, q.Get("since"), q.Get("from"), q.Get("to"), limit, pattern)
	s.serveCached(w, r, cacheKey, 60*time.Second, func(ctx context.Context) (any, error) {
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
		// v0.8.x — window bound: explicit from/to when present (range-bound
		// autocomplete), else the legacy since-duration fallback.
		timeWhere := "time >= now() - toIntervalSecond(?)"
		timeArgs := []any{int64(since.Seconds())}
		if !from.IsZero() && !to.IsZero() {
			timeWhere = "time >= ? AND time <= ?"
			timeArgs = []any{from, to}
		}
		// Optional q filter — ILIKE on the value alias `v`, applied in HAVING
		// (WHERE doesn't see SELECT aliases) on the column path.
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
				FROM spans
				WHERE %s
				  AND %s != ''
				GROUP BY v
				HAVING 1=1%s
				ORDER BY c DESC
				LIMIT ?
				SETTINGS max_execution_time = 25`, col, timeWhere, col, havingQ)
			args = append([]any{}, timeArgs...)
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
			if likeFilter == "" {
				// No q — sample-bound the inner scan so a wide window on a hot
				// key can't run past max_execution_time. Top-N over a bounded
				// sample is fine for an autocomplete dropdown.
				sql = fmt.Sprintf(`
					SELECT v, count() AS c FROM (
					    SELECT %s[indexOf(%s, ?)] AS v
					    FROM spans
					    WHERE %s AND has(%s, ?)
					    LIMIT %d
					)
					WHERE v != ''
					GROUP BY v
					ORDER BY c DESC
					LIMIT ?
					SETTINGS max_execution_time = 25`, arrVals, arrKeys, timeWhere, arrKeys, attrValuesSampleRows)
				args = append([]any{key}, timeArgs...)
				args = append(args, key, limit)
			} else {
				// Explicit q — exact filtered scan (ILIKE + has + time bound +
				// max_execution_time keep it bounded), so a long-tail value on a
				// high-cardinality key is always reachable.
				// v0.9.242 (scale-audit M4) — the ILIKE used to sit in HAVING,
				// i.e. AFTER the GROUP BY: ClickHouse built a hash table over
				// EVERY distinct value of the key and only then threw away the
				// non-matching ones. On a high-cardinality key that grouping is
				// the whole cost, and this is a typeahead running under a 30s
				// ceiling — 150× the hot-endpoint budget.
				//
				// Moving the predicate ahead of the GROUP BY is exact, not an
				// approximation: same rows in, same rows out (verified against
				// live CH, identical result sets). The sibling no-q branch
				// above still needs its sample bound because it has no
				// predicate to narrow with; this one does.
				sql = fmt.Sprintf(`
					SELECT v, count() AS c FROM (
					    SELECT %s[indexOf(%s, ?)] AS v
					    FROM spans
					    WHERE %s
					      AND has(%s, ?)
					)
					WHERE v != '' AND v ILIKE ?
					GROUP BY v
					ORDER BY c DESC
					LIMIT ?
					SETTINGS max_execution_time = 25`, arrVals, arrKeys, timeWhere, arrKeys)
				args = append([]any{key}, timeArgs...)
				args = append(args, key, likeFilter, limit)
			}
		}

		rows, err := s.store.Conn().Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
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
	if topN < 1 {
		topN = 300
	}
	if topN > 5000 {
		topN = 5000
	}

	// 30s cache. The per-service neighbourhood is small and changes
	// only when new edges form; a 30-second window collapses every
	// re-render of the same selection into one CH round-trip.
	key := fmt.Sprintf("service-graph:svc=%s:since=%s:from=%s:to=%s:topN=%d",
		service, q.Get("since"), q.Get("from"), q.Get("to"), topN)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetServiceGraphTopN(ctx, service, since, from, to, topN)
	})
}

func (s *Server) getOperations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), 24*time.Hour)
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	key := fmt.Sprintf("operations:svc=%s:since=%s:from=%s:to=%s",
		q.Get("service"), q.Get("since"), q.Get("from"), q.Get("to"))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetOperations(ctx, q.Get("service"), since, from, to)
	})
}

// parseTraceFilter — ?query -> chstore.TraceFilter.
//
// v0.9.638'de getTraces'in gövdesinden ÇIKARILDI, davranış değişmeden.
// Sebep: /api/traces/count listenin SAYDIĞI kümeyle aynı evreni saymak
// zorunda ve bu ancak AYNI ayrıştırmadan geçerse garanti. İkinci bir
// ayrıştırıcı, bu oturumun on yedi sürümünü doğuran hata sınıfının ta
// kendisi olurdu: bir kural iki yerde, zamanla ayrışıyor.
func parseTraceFilter(q url.Values) (chstore.TraceFilter, error) {
	// v0.9.1055 — pencere VARSAYILANI (1h). from/to gelmeyince filtre
	// sıfır-zamanla iniyordu ve chstore'un zaman predicate'i koşullu
	// (repo.go buildTraceWhere `if !f.From.IsZero()`): spans TAM TARAMA
	// → 25s max_execution_time → 500. UI her zaman pencere yollar, o
	// yüzden latent kaldı; API-doğrudan/MCP çağıranlar düşüyordu
	// ("time-bounded WHERE" sert kısıtının açık ucu). Sınırda kapatılır:
	// to yoksa now, from yoksa to-1h.
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		from = to.Add(-time.Hour)
	}
	f := chstore.TraceFilter{
		Service:  q.Get("service"),
		Search:   q.Get("search"),
		TraceID:  strings.ToLower(strings.TrimSpace(q.Get("traceId"))),
		From:     from,
		To:       to,
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
		MinMs:   parseFloat(q.Get("minMs")),
		MaxMs:   parseFloat(q.Get("maxMs")),
		AttrKey: q.Get("attrKey"),
		AttrVal: q.Get("attrVal"),
		// v0.8.383 — global env picker (?env=). First-class filter so it
		// survives the FilterRoot-supersedes-Filters rule; rides the
		// RawQuery cache key below like every other param.
		Env: strings.TrimSpace(q.Get("env")),
		// v0.9.943 (B3/Ö5) — ?cluster=. /endpoints ve EndpointDetail'in
		// "Traces →" pivotu bu paramı v0.9.307'den beri YAZIYORDU;
		// /traces onu okumuyordu, yani cluster=A altında bakılan bir
		// satırın pivotu TÜM cluster'ların trace'lerini listeliyordu.
		// Aynı ayrıştırmadan geçtiği için /api/traces/count de aynı
		// evreni sayar (v0.9.638 sözleşmesi).
		Cluster: strings.TrimSpace(q.Get("cluster")),
		Sort:    q.Get("sort"),
		Order:   q.Get("order"),
		Limit:   parseInt(q.Get("limit"), 50),
		Offset:  parseInt(q.Get("offset"), 0),
	}
	filters, ferr := parseFiltersAndDSL(q.Get("filters"), q.Get("dsl"))
	if ferr != nil {
		return chstore.TraceFilter{}, fmt.Errorf("invalid query DSL: %w", ferr)
	}
	f.Filters = filters
	// v0.8.x gap-2 — grouped AND/OR builder. When the FE sends a filterGroup
	// it SUPERSEDES the flat filters= (a flat-AND group is byte-identical, an
	// OR/nested group disqualifies the MV fast-path). filterGroup rides the
	// raw query string so the "traces:"+RawQuery cache key already hashes it.
	root, gerr := parseFilterGroup(q.Get("filterGroup"))
	if gerr != nil {
		return chstore.TraceFilter{}, gerr
	}
	f.FilterRoot = root
	// Extra attribute columns — shared parser in traces_extras.go (FAZ 2).
	f.ExtraAttrs = parseExtraAttrs(q)
	// count mode — opt-in for the expensive count(DISTINCT trace_id):
	//   skip   (default) — no count; UI shows ">=N+1" when the page is full
	//   approx           — count over top N+1 trace_ids only (fast)
	//   exact            — full scan (the legacy behaviour, 30s+ at scale)
	f.CountMode = q.Get("count")
	if f.CountMode == "" {
		f.CountMode = "skip"
	}
	return f, nil
}

func (s *Server) getTraces(w http.ResponseWriter, r *http.Request) {
	// FAZ 2 — ?traceIds= skips phase-1 and serves bounded extras only (traces_extras.go).
	if s.serveTracesExtras(w, r) {
		return
	}
	q := r.URL.Query()
	f, ferr := parseTraceFilter(q)
	if ferr != nil {
		http.Error(w, ferr.Error(), http.StatusBadRequest)
		return
	}
	// 20s cache. /api/traces is the heaviest uncached read: a filtered
	// query disqualifies the trace_summary MV fast-path and falls back to
	// a raw GROUP BY trace_id over spans. Pagination round-trips, sort /
	// filter toggles and concurrent operators all repeat the same
	// predicate, so serveCached gives cache-hit + singleflight coalescing
	// + stale-while-revalidate. Key = full query string (same shape as the
	// traces-agg / span-metric keys); the handler is a pure function of the
	// query string (GET, no body/role variance). raw from/to keep a
	// relative window stable within the TTL (do NOT key on parsed now()-
	// ticking time, the v0.5.184 class).
	key := "traces:" + cacheRawQuery(r) // v0.10.256 — from/to 30 s grid, refresh düşer
	// v0.10.326 — ?explain=1 (yalnız admin): cache'i ATLAR, yanıta yol
	// kararları + her CH adımı (SQL/arg/ms/satır/hata) eklenir. Prod'da
	// tekrar etmeyen "kısa pencere boş" sınıfının teşhis aracı.
	explain := q.Get("explain") == "1"
	if explain && !explainAllowed(auth.FromContext(r.Context())) {
		writeJSONError(w, http.StatusForbidden, "explain requires admin")
		return
	}
	// v0.10.329 — explain kaydı HER istekte tutulur (ucuz: SQL metni + arg);
	// yanıta yalnız explain=1'de girer, boş sonuçta pod loguna düşer.
	f.Explain = &chstore.TraceExplain{}
	fn := func(ctx context.Context) (any, error) {
		// OUT param (v0.8.369): set to the recency-slice size when a
		// non-time sort was ranked within the newest-N slice, so the
		// UI can hint honestly (0 = exact/global ordering served).
		rankedWithin := 0
		f.RankedWithin = &rankedWithin
		// v0.9.297 — OUT param: set when Stage 2 exhausted its resources
		// and the window had to be halved to answer at all. Emitted only
		// when it actually moved, exactly like rankedWithinRecent.
		var narrowedFrom time.Time
		f.NarrowedFrom = &narrowedFrom
		// v0.10.342 — kimlik-önce arama (function_id / trace id) sonucu.
		// v0.10.343 — Operator-reported: değer "Trace ID…" kutusuna yazılmıştı
		// (?traceId=). 32-hex olmayan traceId bir KİMLİKTİR (function_id gibi):
		// Search'e taşınır (ham hâli — büyük/küçük harf duyarlı eşitlik),
		// TraceID boşalır; eski istemci/API çağrıları da bundan yararlanır.
		if search, tid := identityFromTraceIDParam(q.Get("traceId"), f.Search); tid == "" && f.TraceID != "" {
			f.TraceID, f.Search = "", search
		}
		var identity chstore.IdentityHit
		f.IdentityHit = &identity
		traces, total, hasMore, err := s.store.GetTraces(ctx, f)
		if err != nil {
			return nil, err
		}
		resp := map[string]interface{}{"traces": traces, "hasMore": hasMore}
		// v0.10.329 — boş liste öz-teşhisi (operatör, prod: kısa pencerede
		// aramalı liste boş, şerit dolu; lokalde tekrar etmiyor). Aynı
		// WHERE + arama yüklemiyle span sayımı: N>0 = liste sorgusu
		// kaybediyor, N=0 = veri/yüklem. Yanıta emptyDiag, pod loguna
		// explain (SQL/arg/ms/satır) — operatör tıklaması gerekmez.
		if chstore.EmptyDiagWanted(f, len(traces)) {
			n, cerr := s.store.CountMatchingSpans(ctx, f)
			diag := map[string]any{"matchingSpans": n}
			if cerr != nil {
				diag["error"] = cerr.Error()
			}
			// v0.10.339 — Operator-reported (prod): `channel_code = …` çipiyle
			// liste boş, aynı span'ler çipsiz listede o değerle görünüyor.
			// Filtre terfi kolonuna derlenmişti ve kolon o replikada değeri
			// taşımıyordu. Boş sonuçta kolon/dizi sayımı host başına
			// karşılaştırılır; dizi fazlaysa aynı istek dizi yoluyla yeniden
			// koşar (doğru ama yavaş) ve harita askıya alınır.
			if pk := chstore.PromotedFilterKeys(f); len(pk) > 0 && cerr == nil && n == 0 {
				diag["promotedKeys"] = pk
				if hosts, perr := s.store.PromotedMismatch(ctx, f); perr != nil {
					diag["promotedProbeError"] = perr.Error()
				} else {
					diag["promotedHosts"] = hosts
					if chstore.PromotedMismatchDetected(hosts) {
						f2 := f
						f2.NoPromoted = true
						if t2, _, hm2, err2 := s.store.GetTraces(ctx, f2); err2 == nil {
							traces, hasMore = t2, hm2
							resp["traces"], resp["hasMore"] = traces, hasMore
							diag["promotedFallback"] = true
						} else {
							diag["promotedFallbackError"] = err2.Error()
						}
						s.store.SuspendPromotedKeys(pk)
						log.Printf("[traces] PROMOTED COLUMN MISMATCH keys=%v hosts=%+v — dizi yoluna düşüldü (%d satır), harita askıya alındı; replika şemasını kontrol et (system.mutations / DROP+ADD onarımı)", pk, hosts, len(traces))
					}
				}
			}
			resp["emptyDiag"] = diag
			if ej, jerr := json.Marshal(f.Explain); jerr == nil {
				log.Printf("[traces] EMPTY list (window=%s→%s service=%q search=%q filters=%d hasError=%v sort=%s) matchingSpans=%d explain=%s",
					f.From.UTC().Format(time.RFC3339), f.To.UTC().Format(time.RFC3339), f.Service, f.Search, len(f.Filters), f.HasError, f.Sort, n, ej)
			}
		}
		// v0.10.124 — pencere MV'de boş bir güne değiyorsa ham yoldan okundu;
		// UI bunu söyler (sihirbaz doldurunca kendiliğinden kaybolur).
		if s.store.TraceMVGap(ctx, f.From, f.To) {
			resp["mvGap"] = true
		}
		if !narrowedFrom.IsZero() {
			// The operator asked about a window this query could not
			// afford. Say which window actually answered — a top-N over
			// a smaller one is a different answer, not a slower one.
			resp["narrowedFromNs"] = narrowedFrom.UnixNano()
		}
		if rankedWithin > 0 {
			resp["rankedWithinRecent"] = rankedWithin
		}
		if identity.Keys != nil {
			resp["identity"] = identity
		}
		// Only emit `total` when the caller actually computed one — clients
		// distinguish "unknown total" from "zero total" by the field's
		// presence, not its value.
		if f.CountMode != "skip" {
			resp["total"] = total
		}
		if explain {
			resp["explain"] = f.Explain
		}
		return resp, nil
	}
	if explain {
		res, err := fn(r.Context())
		if err != nil {
			// Hata da teşhistir: adımlar kaydedildi, hatayla birlikte döner.
			writeJSON(w, map[string]any{"error": err.Error(), "explain": f.Explain})
			return
		}
		writeJSON(w, res)
		return
	}
	s.serveCached(w, r, key, 20*time.Second, fn)
}

// explainAllowed — v0.10.326: teşhis çıktısı SQL + arg taşır; yalnız admin.
func explainAllowed(c *auth.Claims) bool {
	return c != nil && c.Role == auth.RoleAdmin
}

// exportTracesCSV serves the current /traces filter set as a
// downloadable CSV. Two operator workflows ask for this on
// every install:
//   - postmortem authors who want to paste rows into the
//     incident doc / postmortem template
//   - auditors who need a flat artifact of "what hit service
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
		// v0.8.383 — ?env= parity with /api/traces so the CSV export
		// matches exactly what's on screen. v0.9.943: ?cluster= aynı
		// gerekçeyle — dışa aktarım ekrandaki kapsamı taşımalı.
		Env:       strings.TrimSpace(q.Get("env")),
		Cluster:   strings.TrimSpace(q.Get("cluster")),
		Sort:      q.Get("sort"),
		Order:     q.Get("order"),
		Limit:     limit,
		Offset:    0,
		CountMode: "skip",
	}
	filters, ferr := parseFiltersAndDSL(q.Get("filters"), q.Get("dsl"))
	if ferr != nil {
		http.Error(w, "invalid query DSL: "+ferr.Error(), http.StatusBadRequest)
		return
	}
	f.Filters = filters
	// v0.8.x gap-2 — grouped builder parity with /api/traces so a CSV export
	// matches exactly what's on screen.
	root, gerr := parseFilterGroup(q.Get("filterGroup"))
	if gerr != nil {
		writeErr(w, gerr)
		return
	}
	f.FilterRoot = root
	f.ExtraAttrs = parseExtraAttrs(q)

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

// getTracesCount — /traces "Toplamı göster" rozetinin tavanlı sayısı.
//
// v0.9.638 — sayım liste isteğinden AYRILDI. Öncesi ?count=exact tek
// başına countModeAllowsMV'yi kapatıp listeyi ham spans yoluna
// düşürüyordu: çift ceza. Ayrı endpoint sayesinde liste SQL'i bayt bayt
// aynı kalıyor, yani "toplamı göster" listeyi MV'de BIRAKIYOR.
//
// Filtre AYNI parseTraceFilter'dan geçiyor — ikinci bir ayrıştırıcı
// sayımı listeden ayrıştırırdı.
//
// Rol: /api/traces ile aynı (viewer okur). Önbellek 20s: sayı sayfa
// değiştikçe DEĞİŞMEDİĞİ için sayfalama turları tek sorguya biner —
// eski davranışta her offset yeniden ödüyordu.
func (s *Server) getTracesCount(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, ferr := parseTraceFilter(q)
	if ferr != nil {
		http.Error(w, ferr.Error(), http.StatusBadRequest)
		return
	}
	s.serveCached(w, r, "traces-count:"+cacheRawQuery(r), 20*time.Second,
		func(ctx context.Context) (any, error) {
			return s.store.CountTracesCapped(ctx, f)
		})
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
		// v0.8.383 — global env picker (?env=); rides the RawQuery key.
		Env: strings.TrimSpace(q.Get("env")),
		// v0.9.943 (B3) — ?cluster=; liste ve toplu görünüm AYNI kapsamı
		// okumalı, yoksa sekme değiştirmek soruyu sessizce genişletirdi.
		Cluster: strings.TrimSpace(q.Get("cluster")),
		Sort:    q.Get("sort"),
		Order:   q.Get("order"),
		Limit:   parseInt(q.Get("limit"), 100),
	}
	filters, ferr := parseFiltersAndDSL(q.Get("filters"), q.Get("dsl"))
	if ferr != nil {
		http.Error(w, "invalid query DSL: "+ferr.Error(), http.StatusBadRequest)
		return
	}
	f.Filters = filters
	// v0.8.x gap-2 — grouped AND/OR builder supersedes flat filters= when
	// present; rides the raw query string so the "traces-agg:"+RawQuery cache
	// key already hashes it.
	root, gerr := parseFilterGroup(q.Get("filterGroup"))
	if gerr != nil {
		writeErr(w, gerr)
		return
	}
	f.FilterRoot = root
	// v0.8.453 (B2-c) — genel HAVING: ?having=[{"metric":"errorRate",
	// "op":">","value":1},…]. Whitelist ValidateHaving'de; RawQuery
	// cache key'i parametreyi zaten taşıyor. Bozuk JSON / bilinmeyen
	// metrik-operatör = 400, sessiz yutma yok.
	if hraw := strings.TrimSpace(q.Get("having")); hraw != "" {
		var hs []chstore.HavingExpr
		if err := json.Unmarshal([]byte(hraw), &hs); err != nil {
			http.Error(w, "invalid having JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := chstore.ValidateHaving(hs); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.Having = hs
	}

	// 20s cache. /traces aggregated tab is the default landing
	// view; sort / group toggles re-call this and tend to repeat
	// the same predicate, and a filtered group-by disqualifies the
	// trace_summary MV and falls back to a raw GROUP BY trace_id over
	// spans. Filter set goes through the raw query string so the key
	// is stable across distinct callers (pure function of the query
	// string; GET, no body/role variance). raw from/to keep a
	// relative window stable within the TTL (v0.5.184 class).
	key := "traces-agg:" + cacheRawQuery(r)
	s.serveCached(w, r, key, 20*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetTraceAggregate(ctx, f)
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
		http.Error(w, "trace id required", http.StatusBadRequest)
		return
	}
	// Validate the trace actually exists before issuing a token —
	// stops users from minting share links for typos.
	// v0.9.632 — Tempo fallback dahil: örneklemeyle CH dışında kalmış
	// bir trace'in paylaşım linki de üretilebilmeli.
	spans, _, err := s.resolveTraceSpans(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(spans) == 0 {
		http.Error(w, "trace not found", http.StatusNotFound)
		return
	}
	ttlHours := parseInt(r.URL.Query().Get("ttlHours"), 24)
	if ttlHours <= 0 {
		ttlHours = 24
	}
	if ttlHours > 24*30 {
		ttlHours = 24 * 30
	}

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
	// v0.8.252 — freeze the trace's logs INTO the share at mint time
	// ("o andaki" loglar). The public route then serves this copy: the
	// anonymous viewer never queries the live logstore, and the share
	// outlives log retention. Best-effort — a logstore hiccup mints a
	// share without logs rather than failing the share.
	if logsPage, lerr := s.logs.Search(r.Context(), logstore.Filter{
		TraceID: id, Limit: snapshotLogsMax,
	}); lerr != nil {
		log.Printf("[share] log snapshot capture failed for trace %s: %v", id, lerr)
	} else {
		snap.LogsJSON = snapshotLogsJSON(logsPage.Logs, snapshotLogsMax, int64(logsPage.Total))
	}
	if err := s.store.CreateTraceSnapshot(r.Context(), snap); err != nil {
		writeErr(w, err)
		return
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
	if err != nil {
		writeErr(w, err)
		return
	}
	if snap == nil {
		http.Error(w, "snapshot not found or expired", http.StatusNotFound)
		return
	}
	// v0.9.632 — Tempo fallback dahil; aksi halde paylaşılan bir
	// Tempo-only trace boş açılıyordu.
	spans, _, err := s.resolveTraceSpans(r.Context(), snap.TraceID)
	if err != nil {
		writeErr(w, err)
		return
	}
	// v0.8.252 — logs come from the frozen share-time snapshot, never a
	// live logstore query (anonymous route). Empty column (pre-v0.8.252
	// share / capture failure) serves an empty array.
	logsRaw := json.RawMessage(snap.LogsJSON)
	if snap.LogsJSON == "" {
		logsRaw = json.RawMessage("[]")
	}
	writeJSON(w, map[string]any{
		"traceId":   snap.TraceID,
		"spans":     spans,
		"logs":      logsRaw,
		"expiresAt": snap.ExpiresAt,
		"createdBy": snap.CreatedBy,
	})
}

// snapshotLogsMax bounds the share-time log capture. 500 lines covers
// any realistic trace (typical: 1-100) while keeping the snapshot row
// small; beyond it the tail is cut, newest-first order preserved.
const snapshotLogsMax = 500

// snapshotLogsJSON marshals up to max log records into the frozen
// share payload. Pure (v0.8.252) — tested in snapshot_logs_test.go.
// nil/empty → "" so the column stays empty (the reader serves []).
//
// v0.9.475 (dürüstlük A10) — gövde artık zarf: {"logs":[...],"total":N,
// "truncated":bool}. Dış alıcının (retention sonrası, drill-out yok)
// 500'de kesilmiş listeyi tam sanmasının HİÇBİR düzeltme yolu yoktu —
// kesiklik mint anında donmuş gövdenin içinde taşınmak zorunda. total
// = logstore'un mint anındaki gerçek toplamı. Eski (çıplak dizi)
// snapshot'lar viewer'da aynen çalışır (Array.isArray dalı) — şema
// değişikliği yok.
func snapshotLogsJSON(logs []*logstore.LogRecord, max int, total int64) string {
	if len(logs) == 0 {
		return ""
	}
	if max > 0 && len(logs) > max {
		logs = logs[:max]
	}
	b, err := json.Marshal(map[string]any{
		"logs":      logs,
		"total":     total,
		"truncated": total > int64(len(logs)),
	})
	if err != nil {
		return ""
	}
	return string(b)
}

// getMetricNames now accepts q + limit + offset (v0.5.181) for
// the MetricNamePicker debounced search. Legacy callers that
// pass no params get the unlimited behaviour the response used
// to have. New shape is {names, total, hasMore}; the response
// type is conditional on the presence of `q` so older clients
// (just calling /api/metrics/names?service=X) still receive a
// plain MetricInfo[] body — no breaking change for the existing
// /metrics page until it migrates to the new picker.
// v0.9.1150 — okuma artık metrik kaynak seam'inden geçiyor (CH ya da
// VictoriaMetrics) ve `src` cache anahtarının parçası. Backend işareti
// anahtarda OLMASAYDI operatör Settings'ten VM'yi açtığı an, TTL boyunca
// (60s / 30s) eski CH gövdeleri servis edilirdi ve tazelemeyi farklı
// anlarda yapan iki pod aynı katalogda farklı liste gösterirdi — v0.5.187
// çapraz-zehirlenme sınıfının aynısı, bu kez girdi bir AYAR.
//
// Zarf da `source` taşıyor: katalog sayfası rozeti bunu okur, ayrı bir
// /api/settings çağrısı YAPMAZ (viewer o ucu göremez, ve iki istek
// arasında ayar değişirse rozet gövdeye yalan söylerdi).
func (s *Server) getMetricNames(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	svc := q.Get("service")
	pattern := strings.TrimSpace(q.Get("q"))
	limit := parseInt(q.Get("limit"), 0)
	offset := parseInt(q.Get("offset"), 0)
	// v0.9.1151 — ?metricsrc=vm|ch bu İSTEK için backend'i seçer (deneme
	// modu, metricsource.go). Param yoksa davranış bayt-bayt v0.9.1150:
	// karar Settings'ten çıkar. Çözümleme cache anahtarından ÖNCE, çünkü
	// dönen kaynak hem anahtarı hem okumayı besliyor.
	src, err := s.metricSourceFor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Old shape — no pagination params → return MetricInfo[]
	// like pre-v0.5.181 callers expect.
	//
	// v0.9.833 — cache anahtarları `:v2`. MetricInfo zarfı büyüdü
	// (lastSeenNs + serviceCount); sürüm eklemeden deploy edilirse
	// Redis'teki eski gövdeler TTL boyunca (60s / 30s) yeni alanları
	// EKSİK döner ve arayüz "Son veri: —" gösterip sanki metrik
	// susmuş gibi yalan söyler. Zarf değişti = anahtar değişti.
	if pattern == "" && limit == 0 && offset == 0 {
		s.serveCached(w, r, "metric-names:v3:src="+src.Name()+":svc="+svc, 60*time.Second, func(ctx context.Context) (any, error) {
			// GetMetricNames, ListMetricNames(svc, "", 0, 0)'ın ta kendisi
			// (repo.go:4342) — seam'de ayrı bir metot tutmamak için doğrudan
			// o çağrı yapılıyor. CH tarafında SQL bayt-bayt aynı.
			names, _, err := src.ListMetricNames(ctx, svc, "", 0, 0)
			return names, err
		})
		return
	}
	if limit > 1000 {
		limit = 1000
	}
	key := fmt.Sprintf("metric-names:v3:src=%s:svc=%s:q=%s:limit=%d:offset=%d",
		src.Name(), svc, pattern, limit, offset)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		names, total, err := src.ListMetricNames(ctx, svc, pattern, limit, offset)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"names":   names,
			"total":   total,
			"hasMore": offset+len(names) < total,
			// source: hangi store cevapladı. Gövdeyle BİRLİKTE gidiyor,
			// yani rozet her zaman ekrandaki satırların kaynağını gösterir.
			"source": src.Name(),
		}, nil
	})
}

// getMetrics — v0.8.456: serveCached'e alındı (kardeş queryMetric'in
// dakika-bucket'lı anahtar deseni). Cache'siz her izleyici/poll raw
// metric_points taraması koşuyordu; 30s bayatlık operatörce görünmez.
func (s *Server) getMetrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name, svc := q.Get("name"), q.Get("service")
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	limit := parseInt(q.Get("limit"), 500)
	// v0.9.464 (dürüstlük A12) — zarf {points, truncated}: anahtar v2.
	key := fmt.Sprintf("metric-points:v2:name=%s:svc=%s:limit=%d:from=%d:to=%d",
		name, svc, limit, from.Unix()/60, to.Unix()/60)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		points, truncated, err := s.store.GetMetricPoints(ctx, name, svc, from, to, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"points": points, "truncated": truncated}, nil
	})
}

// ── Metric query (multi-series, attribute filters, group-by) ─────────────────

// queryMetric backs the Grafana-style MetricQueryEditor / MetricsExplorer
// (one fetch per enabled query row, on every debounced keystroke + poll).
// Cached 30s (v0.8.4 — was an uncached raw metric_points GROUP BY scan per
// viewer/keystroke/tab, breaking the /api/* p99 budget under fan-out). Key
// carries every result-affecting input; the window is bucketed to the minute
// so concurrent polls + Dashboards MQE panels share a round-trip, and the raw
// groupBy + filter strings go in verbatim (no length-only collapse, cf.
// v0.5.187). Mirrors getMetricHistogram.
func (s *Server) queryMetric(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	step, _ := strconv.Atoi(q.Get("step"))
	// v0.9.105 (F1) — panel px width ≈ target bucket count; drives pixel-
	// adaptive step when step is auto. Bounded [0,4000] so a rogue value can't
	// blow up the bucket count / cache-key cardinality.
	maxDP, _ := strconv.Atoi(q.Get("maxDataPoints"))
	if maxDP < 0 {
		maxDP = 0
	} else if maxDP > 4000 {
		maxDP = 4000
	}
	name := q.Get("name")
	// v0.9.1152 — isim yokluğu İSTEMCİ hatasıdır: store katmanının
	// "metric name required" hatası VM yolunda errUpstream sarmalına
	// girip 502 görünüyordu (1150 canlı doğrulama bulgusu) — operatörü
	// sağlıklı VM'yi kontrol etmeye gönderir. Kaynağa hiç inmeden 400.
	// v0.9.1155 — gövde JSON zarfına geçti: bu yüzeydeki diğer hatalar
	// (çevrilemeyen agg 400'ü, VM-down 502'si) writeErr'in {"error":…}
	// zarfını kullanıyor; düz metin, hata gövdesini JSON parse eden
	// istemciyi boğuyordu (1153 doğrulama notu).
	if strings.TrimSpace(name) == "" {
		writeErr(w, fmt.Errorf("%w: name (metrik adı) parametresi zorunlu", errBadRequest))
		return
	}
	svc := q.Get("service")
	agg := q.Get("agg")
	groupByRaw := q.Get("groupBy")
	filtersRaw := q.Get("filters")
	// v0.9.279 — DB receiver drill scoping. Not expressible as an ordinary
	// filter: the receivers disagree about where the instance identity lives,
	// so it compiles to a per-engine OR (dbInstanceScopeClause). Both go in
	// the cache key — without them one instance's chart would be served for
	// another's request, the cross-poisoning class CLAUDE.md calls out.
	inst := strings.TrimSpace(q.Get("instance"))
	engine := strings.TrimSpace(q.Get("engine"))
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	// v0.9.458 (dürüstlük A1) — zarf {series, rowsCapped}: anahtar v2
	// (gövde şekli değişti, v0.9.443 dersi).
	// v0.9.776 → v3: histogram+delta agg=avg artık gözlem-ağırlıklı gerçek
	// ortalama (sum(sum_value)/sum(count)). Gövde ŞEKLİ aynı ama SAYILAR
	// değişti — anahtar bumplanmazsa rolling deploy'da 30 sn boyunca eski
	// (ortalamaların ortalaması) değerler servis edilirdi ve iki pod aynı
	// panelde farklı sayı gösterirdi (v0.9.443/458 dersi).
	// v0.9.797 — DIŞLAMA SETİNİN ÖZETİ ANAHTARDA. Kural seti sorgunun
	// SONUCUNU değiştiriyor (WHERE'e NOT match ekleniyor, rollup kademesi
	// kapanıyor); anahtarda olmasaydı kural eklenir eklenmez 30 sn boyunca
	// filtresiz sonuç servis edilir, ve yenilemeyi farklı anlarda yapan
	// iki pod aynı panelde farklı sayı gösterirdi. Özet SIRADAN BAĞIMSIZ
	// FNV (v0.5.187 kuralı: len() değil, tüm girdiler); kural yokken sabit
	// "0" — kuralsız kurulumda anahtar kardinalitesi artmaz.
	// v0.9.1150 — `src` (ch|vm) anahtarın parçası. Bu uç aynı gövde
	// şeklini İKİ farklı store'dan üretebiliyor; işaret olmadan Settings
	// toggle'ı 30 saniye boyunca eski backend'in SAYILARINI servis eder.
	// Şekil aynı, sayılar farklı — v0.9.776 anahtar-bump'ının tam olarak
	// bu gerekçesi.
	// v0.9.1151 — ?metricsrc= deneme modu: seçim istek başına olabilir.
	src, srcErr := s.metricSourceFor(r)
	if srcErr != nil {
		writeErr(w, srcErr)
		return
	}
	// v0.9.1157 → v4: zarfa `note` eklendi (VM yolunda BOŞ dönen bir
	// yüzdeliğin SEBEBİ). Alan `omitempty` — CH gövdesi bayt-bayt eski —
	// ama anahtar yine de bumplanır: rolling deploy'da eski pod'un
	// yazdığı notsuz gövde 30 sn boyunca servis edilirse operatör tam da
	// bu özelliğin engellemek için var olduğu şeyi görür, sessiz boş
	// grafiği (v0.9.443/458 dersi).
	// v0.9.1159 — metricNameRuleTag: VM'de ADIN çözümlendiği seri değişti
	// (aday alternation'ı), istek ise bayt-aynı. Damga YALNIZ VM anahtarına
	// giriyor; CH gövdesi etkilenmediği için onun anahtarı da değişmiyor.
	key := fmt.Sprintf("metric-query:v4%s:src=%s:name=%s:svc=%s:agg=%s:step=%d:mdp=%d:gb=%s:f=%s:inst=%s:eng=%s:from=%d:to=%d:mx=%s",
		s.metricNameRuleTag(src),
		src.Name(), name, svc, agg, step, maxDP, groupByRaw, filtersRaw, inst, engine, from.Unix()/60, to.Unix()/60,
		s.store.MetricExclusions().Digest())
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		// queryMetricNoted, kaynağın açıklayabildiği durumda notu da
		// getirir (metricNoteSource); CH kaynağı bu yeteneği taşımıyor ve
		// "" döner — yani bu satır CH davranışını değiştirmez.
		mfilters, ferr := parseFilters(filtersRaw)
		if ferr != nil {
			return nil, ferr
		}
		series, note, qerr := queryMetricNoted(ctx, src, chstore.MetricQueryFilter{
			Name:          name,
			Service:       svc,
			Instance:      inst,
			Engine:        engine,
			Filters:       mfilters,
			GroupBy:       splitNonEmpty(groupByRaw, ','),
			Aggregation:   agg,
			From:          from,
			To:            to,
			StepSeconds:   step,
			MaxDataPoints: maxDP,
		})
		if qerr != nil {
			return nil, qerr
		}
		out := map[string]any{
			"series":     series,
			"rowsCapped": chstore.SeriesRowsCapped(series),
		}
		// Yalnız DOLU notta alan eklenir: boş bir "note":"" her gövdeyi
		// büyütür ve arayüzde "not var mı" kontrolünü değersizleştirir.
		if note != "" {
			out["note"] = note
		}
		return out, nil
	})
}

// getMetricHistogram renders an explicit-histogram metric as a time×bucket
// heatmap + per-bucket p50/p95/p99 (v0.6.56). Cached 30s; the key carries
// every result-affecting input — the window is bucketed to the minute so
// concurrent polls share a round-trip, and the full filter string is in the
// key (no length-only collapse, cf. v0.5.187).
//
// v0.9.1157 (VM Faz 2) — SEAM'e girdi. ClickHouse yolu aynı
// MetricQueryFilter'ı aynı store metoduna veriyor (chMetricSource sadece
// delegasyon), yani SQL ve gövde şekli değişmedi; değişen tek şey, VM
// açıkken/`?metricsrc=vm` ile bu ucun `sum by (le) (increase(<ad>_bucket))`
// üzerinden aynı zarfı kurabilmesi. Uçlardan biri seam'de kalıp öteki
// kalmasaydı, aynı sayfadaki çizgi grafiği VM'den, ısı haritası
// ClickHouse'tan okuyacaktı — iki store, tek panel.
func (s *Server) getMetricHistogram(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	step, _ := strconv.Atoi(q.Get("step"))
	name := q.Get("name")
	svc := q.Get("service")
	filtersRaw := q.Get("filters")
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	// v0.9.1157 — isim yokluğu İSTEMCİ hatasıdır (kardeş queryMetric'in
	// v0.9.1152 düzeltmesinin ta kendisi): store katmanının "metric name
	// required" hatası VM yolunda errUpstream sarmalına girip 502
	// görünürdü ve operatörü sapasağlam bir VictoriaMetrics'i kontrol
	// etmeye yollardı. CH yolunda bu 500 → 400 demek; bilinçli, çünkü
	// eksik zorunlu parametre hiçbir yolda sunucu arızası değil.
	if strings.TrimSpace(name) == "" {
		writeErr(w, fmt.Errorf("%w: name (metrik adı) parametresi zorunlu", errBadRequest))
		return
	}
	// v0.9.1157 — src anahtarda (bkz. getMetricNames'in gerekçesi):
	// gövde ŞEKLİ iki store'da aynı, SAYILAR farklı. İşaret olmadan
	// Settings toggle'ı ya da `?metricsrc=` denemesi 30 sn boyunca öteki
	// backend'in ısı haritasını servis eder (v0.5.187 sınıfı). Anahtar
	// dizesinin kendisi de değiştiği için eski gövdeler zaten erişilemez —
	// zarfa eklenen `note` alanı için ayrı bir sürüm damgası gerekmiyor.
	src, srcErr := s.metricSourceFor(r)
	if srcErr != nil {
		writeErr(w, srcErr)
		return
	}
	// mx: dışlama seti özeti — ısı haritası da NOT match'li WHERE'den
	// okuyor (metrichist.go), yani aynı çapraz-zehirlenme kapısı burada da
	// açıktı.
	// v0.9.1159 — aday alternation'ı ısı haritasının kova adını da değiştirdi
	// (bkz. metricNameRuleTag); damga yalnız VM tarafına.
	key := fmt.Sprintf("metric-hist%s:src=%s:name=%s:svc=%s:step=%d:f=%s:from=%d:to=%d:mx=%s",
		s.metricNameRuleTag(src),
		src.Name(), name, svc, step, filtersRaw, from.Unix()/60, to.Unix()/60,
		s.store.MetricExclusions().Digest())
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		hfilters, ferr := parseFilters(filtersRaw)
		if ferr != nil {
			return nil, ferr
		}
		return src.QueryMetricHistogram(ctx, chstore.MetricQueryFilter{
			Name:        name,
			Service:     svc,
			Filters:     hfilters,
			From:        from,
			To:          to,
			StepSeconds: step,
		})
	})
}

// getMetricLabelValues — v0.8.456: serveCached'e alındı. Her (metric,
// key) çifti izleyici başına 24h DISTINCT metric_points taraması
// koşuyordu (Explore compare 2x fetch atar); etiket setleri yavaş
// değişir, 60s bayatlık görünmez. since anahtarın parçası.
func (s *Server) getMetricLabelValues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), 24*time.Hour)
	// v0.9.275 — ceiling on the window. This is a SUGGESTION list for a filter
	// picker, not a measurement: a label value that fell out of the window is
	// one the operator types by hand, it is not a wrong number on a chart.
	// That is what makes narrowing acceptable HERE and not in, say, the audit
	// log (v0.9.261), where the same silence hid records.
	//
	// Clamped in the handler rather than the store so the cache key reflects
	// the window that was actually queried — otherwise `since=30d` and
	// `since=7d` would occupy two keys holding identical bodies.
	if since > 7*24*time.Hour {
		since = 7 * 24 * time.Hour
	}
	metric, lkey := q.Get("metric"), q.Get("key")
	// v0.9.1150 — src anahtarda (bkz. getMetricNames'in gerekçesi).
	// v0.9.1151 — ?metricsrc= deneme modu.
	src, err := s.metricSourceFor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	// v0.9.1159 — etiket keşfi de aday alternation'ıyla kapsamlanıyor, yani
	// VM'de dönen liste değişti; damga yalnız VM tarafına (CH'nin DISTINCT
	// taraması bu anahtarın var olma sebebi, boşuna ısıtılmaz).
	key := fmt.Sprintf("metric-labels%s:src=%s:m=%s:k=%s:since=%s",
		s.metricNameRuleTag(src), src.Name(), metric, lkey, since)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		return src.MetricLabelValues(ctx, metric, lkey, since)
	})
}

// getMetricAttrKeys — v0.9.771: the KEY half of the pair above. The store
// method (MetricAttrKeys, v0.9.124) already existed for PromQL `without(L)`
// but was never reachable over HTTP; the PromQL editor's label autocomplete
// (Faz 2) needs it to answer "what can I write inside {}?".
//
// Same posture as getMetricLabelValues: suggestion list, not a measurement —
// 60s staleness is invisible, and the window is clamped in the HANDLER so the
// cache key names the window actually queried (v0.9.275). Key hashes ALL
// inputs (metric, service, since) — v0.5.187.
func (s *Server) getMetricAttrKeys(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), 24*time.Hour)
	if since > 7*24*time.Hour {
		since = 7 * 24 * time.Hour
	}
	metric, service := q.Get("metric"), q.Get("service")
	// v0.9.1150 — src anahtarda (bkz. getMetricNames'in gerekçesi).
	// v0.9.1151 — ?metricsrc= deneme modu.
	src, err := s.metricSourceFor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	// v0.9.1159 — aynı gerekçe getMetricLabelValues'takiyle.
	key := fmt.Sprintf("metric-attr-keys%s:src=%s:m=%s:svc=%s:since=%s",
		s.metricNameRuleTag(src), src.Name(), metric, service, since)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		return src.MetricAttrKeys(ctx, metric, service, since)
	})
}

// ── Span metrics (Tempo span-metrics generator + Dynatrace MDA) ──────────────

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
	key := fmt.Sprintf("repeats:%s:%s:%s:%d:%d:%s",
		q.Get("dsl"), q.Get("filters"), strings.Join(groupBy, ","),
		minRepeats, limit, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.QueryRepeatedSpans(ctx, chstore.RepeatedSpanFilter{
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
	// v0.8.x gap-2 (Explore) — grouped AND/OR builder. When the FE sends a
	// filterGroup it supersedes the flat filters= (a flat-AND group is
	// byte-identical; an OR / nested group disqualifies the MV fast-path and
	// falls to the bounded raw-spans GROUP BY). Same precedence getTraces uses.
	// The cache key (r.URL.RawQuery, below) already hashes filterGroup so
	// distinct group shapes don't poison each other.
	root, gerr := parseFilterGroup(q.Get("filterGroup"))
	if gerr != nil {
		writeErr(w, gerr)
		return
	}
	f.FilterRoot = root
	// v0.8.32 (Phase-2 #1) — 30s cache. Explore's default latency/rate chart
	// re-fires this on every keystroke + range nudge, and the compare-period
	// overlay fires a second call per render. When a filter/DSL/sub-5min step
	// disqualifies the MV fast-path, QuerySpanMetric scans raw spans — the
	// heaviest uncached read in the app. Key = full query string so distinct
	// callers with the same predicate share the warm entry (same shape as
	// the traces-agg cache key); from/to are frontend-memoised so they don't
	// tick now() and poison the key (v0.5.184 class).
	// v0.10.147 — v2: zarfa `tail` eklendi; rolling deploy'da eski pod'un
	// tail'siz gövdesi 90 s servis edilmesin (v0.9.1157 anahtar-bump kuralı).
	key := "span-metric:v2:" + cacheRawQuery(r)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		// v0.8.x — trim the wire payload on a high-cardinality groupBy. The
		// frontend (PanelStack) only ever renders the top ≤TOP_N_MAX series by
		// area, so returning thousands wastes wire + parse. QuerySpanMetricTopN
		// keeps the top-50 by the SAME area metric and reports the pre-trim
		// total so the "+N more" stays accurate. totalSeries omitted (== len)
		// when no trim happened, keeping the response compact.
		series, tail, total, capped, err := s.store.QuerySpanMetricTopN(ctx, f)
		if err != nil {
			return nil, err
		}
		resp := spanMetricResponse{Series: series, RowsCapped: capped}
		if total > len(series) {
			resp.TotalSeries = total
			resp.Tail = tail // v0.10.147 — kırpılan kuyruğun ön-toplamı
		}
		return resp, nil
	})
}

// spanMetricResponse is the /api/spans/metric envelope. Series is the (possibly
// top-N-trimmed) list; TotalSeries is the pre-trim count, omitted when it equals
// len(Series) so the frontend defaults the "+N more" to series length. The
// resolver + batch paths return the bare series slice and never set this — the
// frontend type makes totalSeries optional so they keep working unchanged.
type spanMetricResponse struct {
	Series      []chstore.SpanMetricSeries `json:"series"`
	TotalSeries int                        `json:"totalSeries,omitempty"`
	// Tail (v0.10.147) — top-N dışında kalan serilerin zaman-başı ham
	// sum/count'u; yalnız kırpma olduysa. FE foldTopN kesin "others" için.
	Tail []chstore.TailPoint `json:"tail,omitempty"`
	// RowsCapped (v0.9.458, dürüstlük A1) — 50k satır tavanı doldu:
	// ORDER BY gk alfabetik olduğundan geç-harfli seriler eksik
	// olabilir; totalSeries'in anlattığı top-N kırpmasından AYRI yalan.
	RowsCapped bool `json:"rowsCapped,omitempty"`
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
		From    int64           `json:"from"`
		To      int64           `json:"to"`
		Step    int             `json:"step"`
		GroupBy []string        `json:"groupBy"`
		Filters json.RawMessage `json:"filters"`
		DSL     string          `json:"dsl"`
		// Search (v0.9.601) — serbest metin yüklemi. spanMetric'in
		// ?search= parametresiyle AYNI alana gidiyor; eksikliği bu
		// yüzeyi /traces için kullanılamaz kılıyordu: hacim şeridi
		// aramayı yok sayıp filtrelenmemiş seriyi çizerdi, tablo ise
		// filtreli sonucu gösterirdi — sessiz ve yanıltıcı bir ayrışma.
		//
		// Maliyet sınıfı: Search != "" iken batch yolu da MV/rollup
		// fast-path'lerini ATLAR (QuerySpanMetricMulti'deki fastPathOK
		// kapısı) — tek-agg yoluyla aynı davranış.
		//
		// ⚠ v0.9.601'de bu yorum spanmetric.go:157'yi gerekçe
		// gösteriyordu; o satır TEK-AGG yolunu yönetiyor, bu çağrı
		// yolunu DEĞİL. Batch tarafında kapı YOKTU ve arama sessizce
		// düşebiliyordu (v0.9.618 ekledi). Yanlış gerekçe, eksik
		// kapıdan tehlikeliydi: okuyan "korunuyor" sanıp bir daha
		// bakmaz.
		Search string `json:"search"`
		// v0.9.391 (grafik-audit Faz B) — panel nokta bütçesi; 0 = eski
		// davranış + 2000 emniyet tavanı. queryMetric ile aynı clamp.
		MaxDataPoints int `json:"maxDataPoints"`
		// v0.9.723 — Prometheus rate[W] kayan penceresi (saniye).
		// 0 = kapalı. Clamp [0,600] chstore'da (spanMetricWindow).
		RateWindow int `json:"rateWindow"`
		Aggs       []struct {
			Name  string `json:"name"`
			Agg   string `json:"agg"`
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
	if body.MaxDataPoints < 0 {
		body.MaxDataPoints = 0
	}
	if body.MaxDataPoints > 4000 {
		body.MaxDataPoints = 4000
	}
	f := chstore.SpanMetricBatchFilter{
		Filters:       filters,
		Search:        body.Search,
		GroupBy:       body.GroupBy,
		From:          time.Unix(0, body.From),
		To:            time.Unix(0, body.To),
		StepSeconds:   body.Step,
		MaxDataPoints: body.MaxDataPoints,
		RateWindowSec: body.RateWindow,
		Aggs:          specs,
	}
	// v0.9.229 — this endpoint had NO cache at all. It is the Service
	// Overview's main payload (the page fires TWO of these: the RED bundle
	// and the kafka-excluded latency set), and every load, range change and
	// remount ran the full multi-aggregation over raw spans. Measured on a
	// 9-service local demo the repeats never dropped below ~80ms because
	// nothing was ever served from cache; at real scale that is the page's
	// dominant cost. Operator: "Service Overview eskiye göre daha geç
	// yükleniyor / redisi çok kullandığımızı düşünmüyorum" — for this
	// endpoint that was literally true.
	//
	// 30s soft TTL matches the client's staleTime, so a back-navigation
	// inside the window is free; serveCached's SWR keeps it usable to 90s
	// with a background refresh, and there is no poll on this query so the
	// TTL×staleFactor ≤ interval trap (v0.9.228) can't apply here.
	// v0.9.391 — yanıt zarfı {series, stepSeconds}: frontend bucket
	// genişliğini artık TAHMİN etmiyor (bar genişliği/gap eşiği için
	// sözleşme; rollup /api/rollup/red planıyla AYNI kontrat).
	s.serveCached(w, r, spanMetricBatchKey(body.From, body.To, body.Step, body.MaxDataPoints,
		body.RateWindow, body.GroupBy, string(body.Filters), body.DSL, body.Search, specs), 30*time.Second,
		func(ctx context.Context) (any, error) {
			series, stepSec, err := s.store.QuerySpanMetricMulti(ctx, f)
			if err != nil {
				return nil, err
			}
			return map[string]any{"series": series, "stepSeconds": stepSec}, nil
		})
}

// spanMetricBatchKey hashes EVERY input that changes the result: the
// minute-bucketed window (raw ns would never repeat, so an unbucketed key
// caches nothing), step, group-by set, the filter JSON, the DSL, and each
// agg's (name, aggregation, field) triple.
//
// The aggs are sorted before hashing so two callers asking for the same
// metrics in a different order share one entry, and every component is
// NUL-separated — without the separator "rate"+"p99" and "ratep"+"99"
// would collide, the v0.5.187 rule applied to strings. Fields are hashed
// individually rather than summarised by count, which is the same rule
// applied to the set.
func spanMetricBatchKey(fromNs, toNs int64, step, maxDataPoints, rateWindow int, groupBy []string,
	filters, dsl, search string, aggs []chstore.SpanMetricAggSpec) string {
	h := fnv.New64a()
	write := func(parts ...string) {
		for _, p := range parts {
			h.Write([]byte(p))
			h.Write([]byte{0})
		}
	}
	gb := append([]string(nil), groupBy...)
	sort.Strings(gb)
	for _, g := range gb {
		write("gb", g)
	}
	// v0.9.601 — search ANAHTARA GİRER. Girmeseydi iki farklı arama aynı
	// önbellek girdisini paylaşırdı: operatör "timeout" arar, sonraki
	// "refused" arar ve ilkinin serisini görür. CLAUDE.md sert kısıtı
	// (v0.5.187 çapraz-zehirlenme) — anahtar TÜM girdileri hash'ler.
	write("f", filters, "dsl", dsl, "q", search)
	// v0.9.391 — mdp key'de: farklı genişlikteki paneller farklı çözünürlük
	// ister; key'e girmezse birbirinin çözünürlüğünü zehirler (v0.5.187).
	write("mdp", strconv.Itoa(maxDataPoints))
	// v0.9.723 — rateWindow anahtara girer: pencereli/penceresiz seri
	// kümeleri farklıdır; girmese v2 panel ile eski panel birbirini
	// zehirlerdi (v0.5.187 kuralı).
	write("rw", strconv.Itoa(rateWindow))
	specs := append([]chstore.SpanMetricAggSpec(nil), aggs...)
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	for _, a := range specs {
		write("a", a.Name, a.Aggregation, a.Field)
	}
	return fmt.Sprintf("span-metric-batch:from=%d:to=%d:step=%d:h=%x",
		time.Unix(0, fromNs).Truncate(time.Minute).UnixNano(),
		time.Unix(0, toNs).Truncate(time.Minute).UnixNano(),
		step, h.Sum64())
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
	// v0.9.393 — v2: yanıt exemplar ızgarası kazandı; eski cache girdileri
	// alansız kalmasın diye anahtar sürümlendi (şekil değişiminde kural).
	key := fmt.Sprintf("span-heatmap:v2:window=%s:buckets=%d:filters=%s:dsl=%s",
		cacheBucket(from, to), timeBuckets,
		q.Get("filters"), q.Get("dsl"))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetLatencyHeatmap(ctx, filters, from, to, timeBuckets)
	})
}

// spanBubbleUp — Honeycomb-style "what's special about THESE
// spans" attribute investigator. Two predicate sets:
//   - baseline (?filters / ?dsl) — the wider population
//   - selection (?selFilters / ?selDsl) — narrower subset
//
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
	key := fmt.Sprintf("span-bubbleup:window=%s:f=%s:d=%s:sf=%s:sd=%s",
		cacheBucket(from, to),
		q.Get("filters"), q.Get("dsl"),
		q.Get("selFilters"), q.Get("selDsl"))
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		// v0.9.1063 — Explore yolu aynı-pencere alt-küme kıyası (eski
		// davranış bayt-bayt): iki tarafa da aynı pencere geçer.
		return s.store.BubbleUp(ctx, baseline, selection, from, to, from, to)
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
//	POST /ingest?name=<app>.<profileType>{tag=val,...}
//	             &from=<unix-sec>&until=<unix-sec>
//	             &spyName=<spy>&sampleRate=<hz>&format=pprof
//	Content-Type: binary/octet-stream
//	Body: pprof bytes (gzipped or plain)
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
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		merged := &chstore.FlameNode{Name: "root"}
		parsed, failed := 0, 0
		var earliest, latest time.Time
		// v0.5.340 — streaming aggregation. Each pprof is parsed +
		// merged + discarded inside the callback so RAM peak ≈
		// one payload instead of N × payload size. At 200 × 1MB
		// the old slice approach burned ~200MB per request;
		// streaming holds it to ~1MB peak.
		err := s.store.IterateProfilePayloads(ctx, chstore.ProfileFilter{
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
			"service":        service,
			"profileType":    ptype,
			"profilesUsed":   parsed,
			"profilesFailed": failed,
			"totalSamples":   merged.Value,
			"earliest":       earliest.UnixNano(),
			"latest":         latest.UnixNano(),
			"hotspots":       hotspots,
			"breakdown":      breakdown,
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
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		merged := &chstore.FlameNode{Name: "root"}
		parsed, failed := 0, 0
		// v0.5.340 — single CH round-trip + streaming parse.
		// Replaces the prior N+1 pattern (FindProfilesForSpan +
		// GetProfileBytes per row) and the per-request RAM
		// double-buffer.
		err := s.store.IterateProfilesForSpan(ctx, service,
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
//   - Edge whitespace via unicode.IsSpace — covers NBSP (U+00A0),
//     narrow NBSP (U+202F), and the 17 other Unicode space
//     characters Go's TrimSpace already handles.
//   - Anywhere in string:
//     U+00AD  soft-hyphen          (invisible, conditional break)
//     U+200B  zero-width space     (Word / PDF copy)
//     U+200C  zero-width non-joiner
//     U+200D  zero-width joiner
//     U+2060  word joiner          (invisible)
//     U+FEFF  zero-width no-break / BOM (common in Word copy)
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
			0x2060, // word joiner
			0xFEFF: // zero-width no-break (BOM)
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
	// v0.8.450 — son login damgası (operatör isteği: Users sayfası).
	// Başarısız damga geçerli girişi asla bloklamaz.
	if terr := s.store.TouchUserLogin(r.Context(), user.ID); terr != nil {
		log.Printf("[auth] last-login stamp failed for %q: %v", user.Email, terr)
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
		// v0.8.238 — lets the SPA skip the guaranteed-404 <img> request
		// for local/OIDC accounts and render the initials fallback.
		"hasPhoto": u.HasPhoto,
	}
	// v0.8.266 — directory identity for the sidebar/profile chrome.
	// Omitted (not empty-stringed) when unset so the SPA's ?? chains
	// fall through cleanly.
	if u.FullName != "" {
		out["fullName"] = u.FullName
	}
	if u.Org != "" {
		out["org"] = u.Org
	}
	// v0.9.528 — hitap adı. Ayrıştırma SUNUCUDA yapılır çünkü iki
	// tüketici var: sohbetin karşılaması (SPA) ve sistem prompt'u
	// (backend). Aynı ayrıştırıcıyı iki dilde tutmak sessiz ayrışma
	// demekti. Boş = güvenle ad çıkarılamadı; çağıran isimsiz
	// karşılamaya düşer (bkz. greeting.go).
	if fn := firstNameFrom(u.FullName, u.Email); fn != "" {
		out["firstName"] = fn
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

// mePhoto serves the signed-in user's own directory photo (v0.8.238).
// Any authenticated role — it's their own picture.
func (s *Server) mePhoto(w http.ResponseWriter, r *http.Request) {
	c := auth.FromContext(r.Context())
	if c == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	s.serveUserPhoto(w, r, c.UserID)
}

// userPhoto serves any user's photo for the admin user-management
// list's avatar column (route is admin-gated — same as the list).
func (s *Server) userPhoto(w http.ResponseWriter, r *http.Request) {
	s.serveUserPhoto(w, r, r.PathValue("id"))
}

// serveUserPhoto streams the stored LDAP photo bytes. Not serveCached:
// the response is per-user binary behind auth; the browser caches it
// via Cache-Control instead (1h — a re-login refreshes the row, the
// avatar catches up on the next hard load).
func (s *Server) serveUserPhoto(w http.ResponseWriter, r *http.Request, userID string) {
	u, err := s.store.GetUserByID(r.Context(), userID)
	if err != nil {
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	if u == nil || len(u.Photo) == 0 {
		http.Error(w, "no photo", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(u.Photo))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(u.Photo)
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
	// v0.8.519 — 30s cache: bu okuma her cold tab'ın SERİ kapısı
	// (SPA me() bitmeden route verisi istemiyor); FINAL'lı satır
	// okumasını tab başına değil 30s'de bire indirir.
	if u, ok := s.meUsers.get(c.UserID, time.Now()); ok && u != nil {
		writeJSON(w, s.userPayload(u))
		return
	}
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
	s.meUsers.put(c.UserID, u, time.Now())
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
// resolveLdapLoginRole decides the role a returning/new LDAP user gets,
// given the row that already exists (nil = first login), the role the
// directory groups resolved to (groupRole), and whether that came from
// an EXPLICIT GroupRoleMap match (roleFromGroup) vs the DefaultRole
// fallback. Pure so the operator-reported regression is table-tested
// (v0.8.528). Branches:
//   - No existing row → groupRole (first-time provision).
//   - Existing LDAP row: if an explicit group matched, refresh to
//     groupRole (AD promotion/demotion wins); if NOT (fallback), keep
//     the existing role so a manual admin grant survives re-login.
//   - Existing local row (admin-pinned, converting to LDAP) → keep it.
//
// Invalid/empty result is normalised to viewer by the caller.
func resolveLdapLoginRole(existing *chstore.User, groupRole string, roleFromGroup bool) string {
	role := groupRole
	switch {
	case existing == nil:
		// first-time login — role already holds groupRole
	case existing.AuthProvider == "ldap" && existing.Role != "":
		if !roleFromGroup {
			role = existing.Role
		}
	case existing.AuthProvider != "ldap" && existing.Role != "":
		role = existing.Role
	}
	if !auth.IsValidRole(role) {
		role = auth.RoleViewer
	}
	return role
}

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
	role := resolveLdapLoginRole(existing, res.Role, res.RoleFromGroup)

	u := chstore.User{
		Email:        finalEmail,
		Role:         role,
		AuthProvider: "ldap",
		// v0.8.238 — persist the directory photo on every login so the
		// avatar tracks the directory (removed there = cleared here).
		Photo: res.User.Photo,
		// v0.8.266 — directory identity: displayName → full name,
		// company/o → organization, department/ou → team (below).
		FullName: strings.TrimSpace(res.User.DisplayName),
		Org:      strings.TrimSpace(res.User.Company),
		// v0.8.526 — persist the directory sAMAccountName (lowercased) as
		// the O(1) join key to the LDAP group snapshot. Email stays the
		// canonical identity. Store omits it when the column is absent.
		LdapUsername: strings.ToLower(strings.TrimSpace(res.User.Username)),
	}
	firstLogin := existing == nil
	if existing != nil {
		u.ID = existing.ID
		u.CreatedAt = existing.CreatedAt
		// v0.8.238 — ReplacingMergeTree replaces the WHOLE row: carry
		// the admin-managed fields forward or this login would silently
		// wipe them (latent bug: every LDAP re-login cleared team +
		// custom role + any stored password hash).
		u.Team = existing.Team
		u.CustomRole = existing.CustomRole
		u.PasswordHash = existing.PasswordHash
		u.LastLoginAt = existing.LastLoginAt // v0.8.450 — whole-row carry
		// Directory values refresh on every login but never WIPE a
		// stored value when the attribute is absent — an admin's
		// manual fill survives a sparse directory entry.
		if u.FullName == "" {
			u.FullName = existing.FullName
		}
		if u.Org == "" {
			u.Org = existing.Org
		}
		// v0.8.526 — never WIPE a stored ldap_username on a sparse
		// re-login (whole-row replace); a present directory value refreshes.
		if u.LdapUsername == "" {
			u.LdapUsername = existing.LdapUsername
		}
	} else {
		idBytes := make([]byte, 8)
		_, _ = rand.Read(idBytes)
		u.ID = hex.EncodeToString(idBytes)
	}
	// v0.8.266 — the directory's department/ou IS the team when
	// present (operator: "ekip bilgisi de gelsin" — the directory is
	// the source of truth). An absent attribute keeps the admin's
	// manual assignment (carried forward above).
	if dep := strings.TrimSpace(res.User.Department); dep != "" {
		u.Team = dep
	}
	if err := s.store.UpsertUser(ctx, u); err != nil {
		return nil, fmt.Errorf("provision ldap user: %w", err)
	}
	s.meUsers.clear() // v0.8.519 — /api/auth/me cache'i
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
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	// Drop the "password is set" sentinel — the field's empty value
	// from the UI means "keep current", which Service.SavePersisted
	// already handles.
	if c.BindPassword == "__SET__" {
		c.BindPassword = ""
	}
	for _, m := range c.GroupRoleMap {
		if m.Role != "" && !auth.IsValidRole(m.Role) {
			http.Error(w, "invalid role in mapping: "+m.Role, http.StatusBadRequest)
			return
		}
	}
	if c.DefaultRole != "" && !auth.IsValidRole(c.DefaultRole) {
		http.Error(w, "invalid defaultRole", http.StatusBadRequest)
		return
	}
	if err := s.ldap.SavePersisted(r.Context(), s.store, c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
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
		http.Error(w, "ldap not enabled", http.StatusBadRequest)
		return
	}
	q := r.URL.Query().Get("q")
	limit := 25
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscan(v, &limit)
	}
	users, err := s.ldap.Search(r.Context(), q, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"users": users})
}

// inspectLDAPUser (v0.8.430) — discovery affordance: dump every
// readable directory attribute for one user so an admin can SEE which
// attribute carries the sub-team before setting teamAttribute
// (operator-reported: users.team showed the top division because AD
// stores it in `department`). Read-only; binary values summarized.
func (s *Server) inspectLDAPUser(w http.ResponseWriter, r *http.Request) {
	if !s.ldap.Enabled() {
		http.Error(w, "ldap not enabled", http.StatusBadRequest)
		return
	}
	username := r.URL.Query().Get("username")
	if strings.TrimSpace(username) == "" {
		http.Error(w, "username required", http.StatusBadRequest)
		return
	}
	res, err := s.ldap.InspectUser(r.Context(), username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, res)
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
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if body.Email == "" {
		http.Error(w, "email required", http.StatusBadRequest)
		return
	}
	if !auth.IsValidRole(body.Role) {
		http.Error(w, "role must be admin, editor or viewer", http.StatusBadRequest)
		return
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
		u.LastLoginAt = existing.LastLoginAt // v0.8.450 — whole-row carry
	} else {
		idBytes := make([]byte, 8)
		_, _ = rand.Read(idBytes)
		u.ID = hex.EncodeToString(idBytes)
	}
	if err := s.store.UpsertUser(r.Context(), u); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.meUsers.clear() // v0.8.519 — /api/auth/me cache'i
	saved, _ := s.store.GetUserByEmail(r.Context(), body.Email)
	if saved == nil {
		saved = &u
	}
	s.audit(r, "user.provision_ldap", "user", u.ID, fmt.Sprintf(`{"email":%q,"role":%q}`, body.Email, body.Role))
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
		s.meUsers.clear() // v0.8.519 — /api/auth/me cache'i
		log.Printf("[oidc] auto-provisioned user %q (role=%s)", email, role)
	}

	tok, exp, err := s.auth.Issue(user.ID, user.Email, user.Role)
	if err != nil {
		s.oidcFail(w, r, err.Error())
		return
	}
	// v0.8.450 — son login damgası (OIDC yolu da sayılır).
	if terr := s.store.TouchUserLogin(r.Context(), user.ID); terr != nil {
		log.Printf("[oidc] last-login stamp failed for %q: %v", user.Email, terr)
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

// Team routing (v0.8.429) — the team_contacts settings blob drives the
// automatic problem-open → owner/SRE team e-mail path in internal/notify.
// Plain addresses (not credentials): echoed back verbatim, full details
// in the audit row.
func (s *Server) getTeamContacts(w http.ResponseWriter, r *http.Request) {
	tc, err := s.store.GetTeamContacts(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if tc.Contacts == nil {
		tc.Contacts = map[string]string{}
	}
	writeJSON(w, tc)
}

func (s *Server) putTeamContacts(w http.ResponseWriter, r *http.Request) {
	var body chstore.TeamContacts
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.MinSeverity != "" && body.MinSeverity != "info" && body.MinSeverity != "warning" && body.MinSeverity != "critical" {
		http.Error(w, "minSeverity must be info | warning | critical", http.StatusBadRequest)
		return
	}
	if err := s.store.PutTeamContacts(r.Context(), body); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{
		"enabled": body.Enabled, "minSeverity": body.MinSeverity, "teams": len(body.Contacts),
	})
	s.audit(r, "settings.teamcontacts.update", "settings", chstore.TeamContactsKey, string(details))
	writeJSON(w, body)
}

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
	// v0.9.235 — this sends real mail to an operator-supplied ARBITRARY
	// recipient and left no trace. "Who mailed this address from our SMTP
	// relay, and when" had no answer. The recipient is the whole point of
	// the record, so it goes in the details.
	details, _ := json.Marshal(map[string]any{"recipient": body.Recipient})
	s.audit(r, "settings.smtp.test", "settings", "smtp", string(details))
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

// channelHealth serves the per-channel dispatch verdict that drives the
// Settings → Channels "Health" column (v0.9.1278). Configuration was
// visible there but OUTCOME was not: a dead SMTP relay or a rotated
// webhook token only surfaced on /events, and a missed page is the most
// expensive failure class this product has.
//
// The read takes NO inputs — the lookback is a fixed 30 days inside the
// store — so a constant cache key hashes all of them. 60s TTL keeps
// repeated Settings visits off ClickHouse; the "Test" button refetches
// with ?refresh=1 so the badge reflects a just-sent probe immediately
// instead of up to a minute later.
func (s *Server) channelHealth(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "notify-channel-health", 60*time.Second, func(ctx context.Context) (any, error) {
		return s.store.ChannelHealth(ctx)
	})
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
// validateWebhookChannel — v0.8.445: webhook kanalının BodyTemplate'i
// KAYIT anında parse + örnek-render'dan geçer; bozuk şablon hiç
// kaydedilmez (runtime'da default gövdeye düşüş yalnız beklenmedik
// veri hataları için kalır).
func validateWebhookChannel(c chstore.NotificationChannel) error {
	if c.Type != "webhook" || len(c.Config) == 0 {
		return nil
	}
	var wc notify.WebhookChannelConfig
	if err := json.Unmarshal(c.Config, &wc); err != nil {
		return fmt.Errorf("webhook config: %w", err)
	}
	if err := notify.ValidateWebhookTemplate(wc.BodyTemplate); err != nil {
		return fmt.Errorf("bodyTemplate: %w", err)
	}
	return nil
}

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
	case "webhook":
		// v0.8.445 — headers auth key taşıyabilir; UI'a geri dönmez
		// (mergeSecrets boş bırakılanı korur).
		delete(m, "headers")
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
	if err := validateWebhookChannel(c); err != nil {
		http.Error(w, `{"error":`+strconv.Quote(err.Error())+`}`, http.StatusBadRequest)
		return
	}
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
	// v0.9.235 — accepts an un-saved Zoom clientSecret in the body and calls
	// out to Zoom with it; that is a credential-bearing egress and deserves
	// a row. accountID identifies WHICH tenant was probed; the secret and
	// the channel list itself never touch the audit details.
	zDetails, _ := json.Marshal(map[string]any{
		"accountId": accID, "channels": len(channels),
	})
	s.audit(r, "channel.zoom.list", "channel", body.ChannelID, string(zDetails))
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
	// v0.9.235 — fires a REAL notification into the configured Slack /
	// Teams / Zoom / webhook target and left no audit row. Type and name
	// only: the channel config can hold a secret, so it stays out.
	details, _ := json.Marshal(map[string]any{"type": c.Type, "name": c.Name})
	s.audit(r, "channel.test", "channel", c.ID, string(details))
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
	s.audit(r, "maintenance_window.create", "maintenance_window", mw.ID, fmt.Sprintf(`{"service":%q,"severity":%q}`, mw.Service, mw.Severity))
	writeJSON(w, mw)
}

func (s *Server) deleteMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteMaintenanceWindow(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "maintenance_window.delete", "maintenance_window", id, "")
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
	// Presence enrichment (v0.8.403): one batched MGET for every row's
	// stamp. Best-effort — degraded Redis returns an empty map and the
	// page renders without online/lastSeenAt rather than erroring.
	ids := make([]string, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	seen := s.presence.lastSeen(r.Context(), ids)
	nowNs := time.Now().UnixNano()
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
			// v0.8.238/266 — avatar + directory identity for the
			// admin Users table.
			"hasPhoto": u.HasPhoto,
			"fullName": u.FullName,
			"org":      u.Org,
		}
		// v0.8.452 — kalıcı son login (v0.8.450 kolonu); 0 = hiç.
		// listUsers el yapımı map kurduğundan struct alanı buraya
		// AYRICA eklenmeli — v450'de unutuldu, kolon hep "—" kaldı.
		if u.LastLoginAt > 0 {
			row["lastLoginAt"] = u.LastLoginAt
		}
		// v0.8.403 — presence: online = authenticated API activity in
		// the last 5 minutes. lastSeenAt (unix ns) only while a stamp
		// is live (TTL = online window); absent = never/unknown.
		if stampNs, ok := seen[u.ID]; ok {
			row["online"] = presenceOnline(stampNs, nowNs)
			row["lastSeenAt"] = stampNs
		} else {
			row["online"] = false
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
	s.meUsers.clear() // v0.8.519 — /api/auth/me cache'i
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
	s.meUsers.clear() // v0.8.519 — /api/auth/me cache'i
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
	s.meUsers.clear() // v0.8.519 — /api/auth/me cache'i
	details, _ := json.Marshal(map[string]any{"email": target.Email, "from": prevRole, "to": role})
	s.audit(r, "user.set_role", "user", target.ID, string(details))
	// v0.9.352 — bu pod yeni rolü BİR SONRAKİ istekte uygular; diğer
	// podlar liveAuthzTTL içinde (10s). Öncesinde demote, kullanıcının
	// token'ı dolana kadar (24 saate kadar) hiç etkili olmuyordu.
	s.auth.InvalidateAuthz(target.ID)
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
	s.auth.InvalidateAuthz(id)
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
	// Parola sıfırlama oturumu düşürmüyor (rol değişmedi), ama önbelleği
	// tazelemek zararsız ve hesap aynı turda devre dışı bırakıldıysa
	// doğru olanı yapıyor.
	s.auth.InvalidateAuthz(id)
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
		// v0.8.318 — sort + search run server-side: the inbox is
		// LIMIT/OFFSET paginated, so the old client-side sort/search of
		// one 50-row page mis-prioritized ("top by occurrences" was
		// really "most-recent 50, reordered") and search missed matches
		// on other pages. Sort ids are whitelisted in chstore.
		Search: strings.TrimSpace(q.Get("q")),
		Sort:   q.Get("sort"),
		Dir:    q.Get("dir"),
		Limit:  parseInt(q.Get("limit"), 50),
		Offset: parseInt(q.Get("offset"), 0),
		// v0.9.315 (operator-reported) — occurrence floor. The list was
		// filling with one-off exceptions: a single Java socket timeout
		// rendered a row indistinguishable from a sustained outage.
		// Default 0 keeps every existing caller unchanged; the Problems
		// UI sends its own floor and tells the operator what it hid.
		MinOccurrences: uint64(parseInt(q.Get("minOccurrences"), 0)),
	}
	ownerTeam, sreTeam := q.Get("ownerTeam"), q.Get("sreTeam")
	// v0.9.941 (UX denetimi B1/K8) — ORTAM SÜZGECİ. /problems Exceptions
	// sekmesi Topbar'ın `?env=` seçicisini GÖSTERİYOR ama uygulamıyordu
	// (`envApplies={false}`): operatör prod'u seçip int/uat exception'ları
	// listede görmeye devam ediyordu. Şema değişikliği GEREKMİYOR — filtre
	// zaten `Services` alanını taşıyor (takım süzgeci onu kullanıyor) ve
	// env → servis çözümü EnvMemberServices'te hazır.
	env := q.Get("env")
	// v0.8.455 — ana triage yüzeyi (list+count, 2-3 CH sorgusu) artık
	// serveCached'li: her tab/sort/sayfa değişimi ve eş-zamanlı her
	// izleyici aynı sorguları tekrarlıyordu ("don't bypass serveCached
	// on hot reads"). Anahtar RawQuery'den (hash-all-inputs: state/
	// service/assignee/q/sort/dir/limit/offset/ownerTeam/sreTeam); TTL
	// kardeş /api/problems ile aynı 5s, state/assign mutasyonları
	// prefix'i anında düşürür — bayatlık operatörce görülmez.
	key := "exc-groups:" + cacheRawQuery(r)
	s.serveCached(w, r, key, 5*time.Second, func(ctx context.Context) (any, error) {
		// Ortam kısıtı ÖNCE çözülür ki takım süzgeciyle KESİŞEBİLSİN;
		// ikisi birden seçiliyse cevap "bu takımın BU ortamdaki
		// servisleri" olmalı, biri diğerini ezmemeli.
		//
		// SOFT-FAIL → KISITSIZ (env haritası hatası): kardeş yüzeylerin
		// (inbox listesi + rozeti, envScopeProblems) tamamıyla aynı duruş
		// ve aynı gerekçe — geçici bir CH tökezlemesi ateş eden bir P1'i
		// SAKLAYAMAMALI. Takım süzgeci bilerek FARKLI davranıyor (hata
		// döndürür): o operatörün açık bir daraltma isteği, bu ise
		// sayfa-üstü bir kapsam.
		var envServices []string
		envScoped := false
		if env != "" {
			if members, err := s.store.EnvMemberServices(ctx, env); err == nil {
				envServices, envScoped = members, true
				if envServices == nil {
					// "Çözüldü ama boş" ≠ "kısıt yok". nil bırakırsak
					// intersectServices onu "bu eksenden kısıt yok" sayar
					// (sözleşmesi bu) ve ortam süzgeci SESSİZCE kaybolur.
					envServices = []string{}
				}
			} else {
				log.Printf("[exception-groups] env %q çözülemedi (kısıtsız devam): %v", env, err)
			}
		}
		if ownerTeam != "" || sreTeam != "" {
			// Owner/SRE team filter (v0.8.310) — resolve the pick to its
			// member services and constrain with service IN (…) so it
			// bites BEFORE limit/offset. A team with no member services
			// returns an empty page — never an unfiltered one.
			mds, err := s.store.ListServiceMetadata(ctx)
			if err != nil {
				return nil, err
			}
			svcs := servicesForTeam(s.teamAliasesCtx(ctx), mds, ownerTeam, sreTeam)
			if envScoped {
				svcs = intersectServices(svcs, envServices)
			}
			if len(svcs) == 0 {
				return map[string]any{
					"items": []chstore.ExceptionGroup{}, "total": 0,
					"limit": f.Limit, "offset": f.Offset,
				}, nil
			}
			f.Services = svcs
		} else if envScoped {
			// Exception grupları HER ZAMAN bir servis taşır (span'lerden
			// türüyorlar), yani inbox'taki "servissiz satır her zaman
			// sayılır" istisnasının burada karşılığı YOK: katı IN.
			// Üyesi olmayan bir ortam BOŞ sayfa döndürür, kısıtsız
			// DEĞİL — takım süzgeciyle aynı sözleşme (v0.8.310).
			if len(envServices) == 0 {
				return map[string]any{
					"items": []chstore.ExceptionGroup{}, "total": 0,
					"limit": f.Limit, "offset": f.Offset,
				}, nil
			}
			f.Services = envServices
		}
		items, err := s.store.ListExceptionGroups(ctx, f)
		if err != nil {
			return nil, err
		}
		total, err := s.store.CountExceptionGroups(ctx, f)
		if err != nil {
			return nil, err
		}
		// `items` can be nil from the store on an empty page — serialise
		// as [] so the frontend never has to null-guard the array.
		if items == nil {
			items = []chstore.ExceptionGroup{}
		}
		// v0.10.364 — öncelik (P1/P2/P3) + gerekçe satır başına: Triage
		// Inbox ile AYNI kural (exceptionPriorityAt), kural ekranda görünür.
		for i := range items {
			items[i].Priority, items[i].PriorityReason = exceptionPriority(items[i])
		}
		return map[string]any{
			"items":  items,
			"total":  total,
			"limit":  f.Limit,
			"offset": f.Offset,
		}, nil
	})
}

// getExceptionGroup resolves a single fingerprint straight from the
// store (bypassing the paginated list) so a shared /problems?exc=<fp>
// link can't land on "not found" just because the group fell off the
// requester's currently-loaded page/filter (mirrors AlertProblemHost's
// fetch fallback for ?problem=<id> on the frontend).
func (s *Server) getExceptionGroup(w http.ResponseWriter, r *http.Request) {
	g, err := s.store.GetExceptionGroup(r.Context(), r.PathValue("fp"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if g == nil {
		http.Error(w, `{"error":"exception group not found"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, g)
}

func (s *Server) getExceptionGroupSamples(w http.ResponseWriter, r *http.Request) {
	limit := parseInt(r.URL.Query().Get("limit"), 10)
	res, err := s.store.GetExceptionGroupSamples(r.Context(), r.PathValue("fp"), limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	if res.Samples == nil {
		res.Samples = []chstore.ExceptionSample{}
	}
	// v0.9.463 (dürüstlük A11) — zarf: boş liste "örnek yok" DEĞİL "aday
	// penceresi yetmedi" olabilir; frontend farkı söyler. v0.9.795 zarfa
	// windowExhausted ekledi: tarama artık partili, yani "5000 aday tavanına
	// çarptık" ile "grubun penceresini sonuna kadar okuduk" iki AYRI
	// cevaptır ve ekranda iki ayrı cümle olmalı. Bu uç serveCached'li
	// DEĞİL (zaten fingerprint başına tek atış) — anahtar sürümü kuralı
	// uygulanmıyor.
	writeJSON(w, res)
}

// getExceptionGroupOccurrences serves the "occurrences over time"
// histogram on the problem detail page — a real server-side, gap-filled
// COUNT over the group's whole window (v0.8.309), replacing the old
// client-side bucketing of 100 recent samples. Cached briefly: the only
// input is the fingerprint; last_seen creeps for an active group, so a
// short TTL bounds staleness without thrashing on every poll.
func (s *Server) getExceptionGroupOccurrences(w http.ResponseWriter, r *http.Request) {
	fp := r.PathValue("fp")
	s.serveCached(w, r, "exc-occ:"+fp, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetExceptionOccurrences(ctx, fp)
	})
}

// setExceptionGroupStateBulk applies one state to many exception
// groups (v0.9.252). The triage list's bulk-acknowledge needs it:
// looping the single-fingerprint route client-side would fire N
// requests, N cache invalidations and N audit rows for one operator
// gesture, and a failure halfway would leave the batch half-applied
// with no record of the intent.
//
// Partial success is reported, not hidden: a fingerprint that fails
// mid-batch does not abort the rest, and the response carries both
// counts so the UI can say "42 acknowledged, 3 failed" instead of a
// bare error over a mostly-successful action.
func (s *Server) setExceptionGroupStateBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Fingerprints []string `json:"fingerprints"`
		State        string   `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	switch body.State {
	case chstore.ExStateNew, chstore.ExStateAcknowledged,
		chstore.ExStateResolved, chstore.ExStateIgnored:
		// ok
	default:
		http.Error(w, `{"error":"invalid state"}`, http.StatusBadRequest)
		return
	}
	// Cap the batch. The list this is driven from is server-capped at
	// 500, so a larger request is a malformed client, not a real
	// gesture — and each fingerprint is its own CH write.
	const maxBulkExceptionState = 500
	if len(body.Fingerprints) == 0 {
		http.Error(w, `{"error":"fingerprints required"}`, http.StatusBadRequest)
		return
	}
	if len(body.Fingerprints) > maxBulkExceptionState {
		http.Error(w, `{"error":"too many fingerprints"}`, http.StatusBadRequest)
		return
	}

	var applied int
	var failed []string
	for _, fp := range body.Fingerprints {
		if fp == "" {
			continue
		}
		if err := s.store.SetExceptionGroupState(r.Context(), fp, body.State); err != nil {
			log.Printf("[exception-groups] bulk state %s: %v", fp, err)
			failed = append(failed, fp)
			continue
		}
		applied++
	}

	// Same three invalidations as the single route — the badge and the
	// list must both drop the rows immediately (v0.8.455 / v0.8.472 /
	// v0.9.234), otherwise a bulk ack looks like it did nothing for a
	// whole TTL window.
	s.cacheInvalidatePrefix(r.Context(), "exc-groups:")
	s.cacheInvalidatePrefix(r.Context(), "inbox:count")
	s.cacheInvalidatePrefix(r.Context(), inboxListCachePrefix)
	s.audit(r, "exception_group.set_state_bulk", "exception_group", "",
		fmt.Sprintf(`{"state":%q,"requested":%d,"applied":%d,"failed":%d}`,
			body.State, len(body.Fingerprints), applied, len(failed)))
	writeJSON(w, map[string]any{"applied": applied, "failed": len(failed)})
}

func (s *Server) setExceptionGroupState(w http.ResponseWriter, r *http.Request) {
	var body struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	switch body.State {
	case chstore.ExStateNew, chstore.ExStateAcknowledged,
		chstore.ExStateResolved, chstore.ExStateIgnored:
		// ok
	default:
		http.Error(w, `{"error":"invalid state"}`, http.StatusBadRequest)
		return
	}
	if err := s.store.SetExceptionGroupState(r.Context(), r.PathValue("fp"), body.State); err != nil {
		writeErr(w, err)
		return
	}
	// v0.8.455 — triage listesi artık cache'li; state değişimi listede
	// ANINDA görünmeli (resolve edilen satır 5s asılı kalmasın).
	s.cacheInvalidatePrefix(r.Context(), "exc-groups:")
	s.cacheInvalidatePrefix(r.Context(), "inbox:count") // v0.8.472
	// v0.9.234 — the LIST too, not just the badge: dropping only the count
	// left the sidebar number falling instantly while /inbox kept the
	// resolved row for the whole TTL window (the badge-vs-list divergence
	// v0.9.219/220 closed from the other two directions).
	s.cacheInvalidatePrefix(r.Context(), inboxListCachePrefix)
	s.audit(r, "exception_group.set_state", "exception_group", r.PathValue("fp"), fmt.Sprintf(`{"state":%q}`, body.State))
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) assignExceptionGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Assignee string `json:"assignee"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := s.store.AssignExceptionGroup(r.Context(), r.PathValue("fp"), body.Assignee); err != nil {
		writeErr(w, err)
		return
	}
	s.cacheInvalidatePrefix(r.Context(), "exc-groups:") // v0.8.455 — bkz. setExceptionGroupState
	s.audit(r, "exception_group.assign", "exception_group", r.PathValue("fp"), fmt.Sprintf(`{"assignee":%q}`, body.Assignee))
	writeJSON(w, map[string]string{"status": "ok"})
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
	key := fmt.Sprintf("svc-breakdown:svc=%s:window=%s", svc, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetSpanBreakdown(ctx, svc, from, to)
	})
}

// svcOperationSummary returns per-operation aggregates for a single
// service. Drives the Operations table on the service detail page.
// svcOpsCacheKey builds the serveCached key for the per-service operation
// summary. normalized is part of the key because raw-name and op_group-shape
// groupings return different row sets for the SAME window — omitting it would
// cross-poison the two cached results (the v0.5.187 class of bug: a key that
// doesn't hash every input that changes the response). Extracted as a pure
// function (v0.5.447 regression-test pattern) so op_group_cache_key_test.go can
// pin the normalized-distinctness invariant without a live server.
// v0.9.60 — compare de anahtarda: prior'lu ve prior'suz yanıt farklı
// gövdedir; anahtar dışı bırakmak iki modu çapraz-zehirlerdi (v0.5.187).
func svcOpsCacheKey(svc, since, from, to string, normalized, compare bool, env string) string {
	return fmt.Sprintf("svc-ops:svc=%s:since=%s:from=%s:to=%s:norm=%t:cmp=%t:env=%s",
		svc, since, from, to, normalized, compare, env)
}

func (s *Server) svcOperationSummary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	svc := r.PathValue("name")
	since := parseDuration(q.Get("since"), 24*time.Hour)
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	// normalized=1 groups operations by op_group (normalized shape;
	// group_id rel B) instead of raw name. MUST be in the cache key —
	// raw and normalized return different row sets for the same window,
	// so omitting it would cross-poison the two (the v0.5.187 class of
	// bug: a key that doesn't hash all inputs).
	normalized := q.Get("normalized") == "1"
	// v0.9.60 — compare=prior: bir-önceki eş-pencere skalerleri + gölge
	// serileri (Elastic-parity Operations sekmesi).
	compare := q.Get("compare") == "prior"
	// v0.9.1039 — global Topbar env picker. Non-empty forces the raw-spans
	// path (deploy_env conjunct) in GetOperationSummary. MUST hash into the
	// key: env-scoped and all-env row sets differ (v0.5.187 class).
	env := strings.TrimSpace(q.Get("env"))
	key := svcOpsCacheKey(svc, q.Get("since"), q.Get("from"), q.Get("to"), normalized, compare, env)
	// 30s TTL — operation set changes on deploys (minutes
	// apart), not seconds. With the SWR tier in cache.go, a
	// 30s soft TTL still gives 90s of stale-but-usable
	// fallback before a hard miss; net effect is the
	// operator never sees a cold-load delay on this endpoint
	// during normal traffic. Pre-v0.5.58 this was 15s which
	// half-the-time forced an upstream re-fetch the operator
	// would never notice if it was stale.
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		if compare {
			return s.store.GetOperationSummaryCompared(ctx, svc, since, from, to, normalized, env)
		}
		return s.store.GetOperationSummary(ctx, svc, since, from, to, normalized, env)
	})
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
	ID         string `json:"id"`
	Title      string `json:"title"`
	Body       string `json:"body,omitempty"`
	Status     string `json:"status"`
	Severity   string `json:"severity"`
	StartedAt  int64  `json:"startedAt"`
	ResolvedAt *int64 `json:"resolvedAt,omitempty"`
}

type publicStatusResp struct {
	Title       string               `json:"title"`
	Description string               `json:"description,omitempty"`
	SupportURL  string               `json:"supportUrl,omitempty"`
	Status      string               `json:"status"` // worst-of components
	CheckedAt   string               `json:"checkedAt"`
	Components  []publicComponentRow `json:"components"`
	Incidents   []publicIncidentRow  `json:"incidents"`
}

func (s *Server) publicStatus(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "public-status", 30*time.Second, func(ctx context.Context) (any, error) {
		return s.collectPublicStatus(ctx)
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
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(strings.ToLower(body.Email))
	if email == "" || !strings.ContainsRune(email, '@') {
		http.Error(w, "valid email required", http.StatusBadRequest)
		return
	}
	token, err := s.store.AddStatusSubscriber(r.Context(), email)
	if err != nil {
		writeErr(w, err)
		return
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
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, c)
}
func (s *Server) statusPagePutConfig(w http.ResponseWriter, r *http.Request) {
	var c chstore.StatusPageConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := s.store.UpsertStatusPageConfig(r.Context(), c); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "status_page.config_update", "status_page", "", "")
	s.cacheInvalidate(r.Context(), "public-status")
	writeJSON(w, c)
}
func (s *Server) statusPageListComponents(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListStatusComponents(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}
func (s *Server) statusPageCreateComponent(w http.ResponseWriter, r *http.Request) {
	var c chstore.StatusComponent
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	c.ID = ""
	if err := s.store.UpsertStatusComponent(r.Context(), &c); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "status_page.component_create", "status_page", c.ID, c.Name)
	s.cacheInvalidate(r.Context(), "public-status")
	writeJSON(w, c)
}
func (s *Server) statusPageUpdateComponent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var c chstore.StatusComponent
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	c.ID = id
	if err := s.store.UpsertStatusComponent(r.Context(), &c); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "status_page.component_update", "status_page", id, c.Name)
	s.cacheInvalidate(r.Context(), "public-status")
	writeJSON(w, c)
}
func (s *Server) statusPageDeleteComponent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteStatusComponent(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "status_page.component_delete", "status_page", id, "")
	s.cacheInvalidate(r.Context(), "public-status")
	writeJSON(w, map[string]string{"status": "deleted"})
}
func (s *Server) statusPageListSubscribers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListStatusSubscribers(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}
func (s *Server) statusPageDeleteSubscriber(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("email")
	if email == "" {
		http.Error(w, "email required", http.StatusBadRequest)
		return
	}
	if err := s.store.RemoveStatusSubscriber(r.Context(), email); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "status_page.subscriber_delete", "status_page", email, "")
	writeJSON(w, map[string]string{"status": "deleted"})
}
func (s *Server) statusPagePublishIncident(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p chstore.PublishedIncident
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	p.IncidentID = id
	if err := s.store.SetIncidentPublished(r.Context(), p); err != nil {
		writeErr(w, err)
		return
	}
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
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, sp)
}

// putRetention applies new TTLs via ALTER TABLE and persists the new
// values to system_settings so a restart picks them up.
func (s *Server) putRetention(w http.ResponseWriter, r *http.Request) {
	var sp chstore.RetentionSpec
	if err := json.NewDecoder(r.Body).Decode(&sp); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.SetRetention(r.Context(), sp, actorOf(r)); err != nil {
		writeErr(w, err)
		return
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

// getRuntimeAlerts / putRuntimeAlerts (v0.9.485, operator-reported:
// "JVM alertleri daha sıkı olmalı, false pozitif çok") — runtime pod
// detektörünün eşikleri. Varsayılanlar chstore.DefaultRuntimeAlerts
// (GC pause 2000/3000ms — operatör ölçütü "2-3 saniye"); evaluator her
// tick taze okur, PUT bir sonraki tick'te devrede.
func (s *Server) getRuntimeAlerts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.GetRuntimeAlerts(r.Context()))
}

func (s *Server) putRuntimeAlerts(w http.ResponseWriter, r *http.Request) {
	var c chstore.RuntimeAlertConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Sınır kontrolü: ters/absürt eşik sessizce her şeyi critical ya da
	// hiç-alarm yapmasın (v0.9.247 dersi). Ayrıntılı zero-patch okuma
	// tarafında (GetRuntimeAlerts) zaten var; burada yalnız kaba sınırlar.
	if c.GCPauseWarnMs < 100 || c.GCPauseWarnMs > 60000 || c.GCPauseCritMs <= c.GCPauseWarnMs {
		http.Error(w, "gcPauseWarnMs 100-60000 aralığında ve gcPauseCritMs > gcPauseWarnMs olmalı", http.StatusBadRequest)
		return
	}
	if c.GCShareWarnPct <= 0 || c.GCShareWarnPct >= 100 || c.GCShareCritPct <= c.GCShareWarnPct {
		http.Error(w, "gcShareWarnPct 0-100 aralığında ve gcShareCritPct > gcShareWarnPct olmalı", http.StatusBadRequest)
		return
	}
	if c.HeapPostGCWarnPct <= 0 || c.HeapPostGCCritPct <= c.HeapPostGCWarnPct ||
		c.HeapRawWarnPct <= 0 || c.HeapRawCritPct <= c.HeapRawWarnPct ||
		c.HeapPostGCCritPct > 100 || c.HeapRawCritPct > 100 {
		http.Error(w, "heap eşikleri 0-100 aralığında ve crit > warn olmalı", http.StatusBadRequest)
		return
	}
	if err := s.store.SaveRuntimeAlerts(r.Context(), c); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "settings.runtime_alerts.update", "settings", "runtime_alerts",
		fmt.Sprintf(`{"gcPauseWarnMs":%v,"gcPauseCritMs":%v,"gcShareWarnPct":%v,"gcShareCritPct":%v}`,
			c.GCPauseWarnMs, c.GCPauseCritMs, c.GCShareWarnPct, c.GCShareCritPct))
	writeJSON(w, c)
}

// getSelfHealth / putSelfHealth (v0.9.1279) — self-health alarm
// ailesinin eşikleri (ingest durması / spool derinliği / disk ETA / ölü
// bildirim kanalı). runtime-alerts şablonu birebir: admin-kapılı, PUT
// audit'li, evaluator her tik taze okur → bir sonraki tikte devrede.
//
// Settings SEKMESİ bu dilimde bilinçli YOK. Vidanın kendisi yine de
// döndürülebilir olmalıydı: dönmeyen bir vida vida değildir ve
// SaveSelfHealth'i çağıran hiçbir yol olmasaydı ölü kod olurdu.
func (s *Server) getSelfHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.GetSelfHealth(r.Context()))
}

func (s *Server) putSelfHealth(w http.ResponseWriter, r *http.Request) {
	var c chstore.SelfHealthConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Kaba sınırlar. Ayrıntılı zero-patch okuma tarafında
	// (patchSelfHealth); burada yalnız absürt değerler elenir — bir
	// dakikalık stall penceresi her deploy'da alarm çalar, 365 günlük
	// disk ufku hiç çalmaz. İkisi de vidayı işlevsiz kılar.
	if c.IngestStallMin != 0 && (c.IngestStallMin < 5 || c.IngestStallMin > 1440) {
		http.Error(w, "ingestStallMin 5-1440 dakika aralığında olmalı", http.StatusBadRequest)
		return
	}
	if c.DiskEtaDays != 0 && (c.DiskEtaDays <= 0 || c.DiskEtaDays > 90) {
		http.Error(w, "diskEtaDays 0-90 gün aralığında olmalı", http.StatusBadRequest)
		return
	}
	if c.ChannelConsecFails != 0 && (c.ChannelConsecFails < 2 || c.ChannelConsecFails > 100) {
		http.Error(w, "channelConsecFails 2-100 aralığında olmalı", http.StatusBadRequest)
		return
	}
	// v0.9.1294 — hacim sıçraması vidaları. Alt sınırlar ÖLÇÜLMÜŞ:
	// 101 servislik yerel demoda meşru büyümenin tepesi 2.42× (pencere
	// ortasında doğan servis), yani 2'nin altındaki bir katsayı normal
	// gün-içi dalgayı alarma çevirir. 1000 span'in altındaki bir hacim
	// tabanı da kapıyı işlevsiz kılar — kural o noktada yeniden bir
	// yanlış-pozitif fabrikasıdır.
	if c.VolumeSpikeFactor != 0 && (c.VolumeSpikeFactor < 2 || c.VolumeSpikeFactor > 1000) {
		http.Error(w, "volumeSpikeFactor 2-1000 aralığında olmalı", http.StatusBadRequest)
		return
	}
	if c.VolumeSpikeMinSpans != 0 && c.VolumeSpikeMinSpans < 1000 {
		http.Error(w, "volumeSpikeMinSpans en az 1000 olmalı", http.StatusBadRequest)
		return
	}
	if err := s.store.SaveSelfHealth(r.Context(), c); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "settings.self_health.update", "settings", "self_health",
		fmt.Sprintf(`{"enabled":%v,"ingestStallMin":%d,"spoolMaxFiles":%d,"spoolMaxBytes":%d,"diskEtaDays":%v,"channelConsecFails":%d,"volumeSpikeFactor":%v,"volumeSpikeMinSpans":%d}`,
			c.SelfHealthOn(), c.IngestStallMin, c.SpoolMaxFiles, c.SpoolMaxBytes,
			c.DiskEtaDays, c.ChannelConsecFails, c.VolumeSpikeFactor, c.VolumeSpikeMinSpans))
	writeJSON(w, s.store.GetSelfHealth(r.Context()))
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
	// v0.9.247 — a critical cut-off BELOW the promotion gate would
	// silently make every promoted anomaly critical. Reject it here
	// rather than let the read-side clamp hide the operator's typo.
	if c.CriticalPeakRatio < c.MinPeakRatio || c.CriticalPeakRatio > 1000 {
		http.Error(w, "criticalPeakRatio must be between minPeakRatio and 1000", http.StatusBadRequest)
		return
	}
	if err := s.store.SaveAnomalyPromotion(r.Context(), c); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "settings.anomaly_promotion.update", "settings", "anomaly_promotion", fmt.Sprintf(`{"minPeakRatio":%v,"criticalPeakRatio":%v,"minSustainedSec":%v,"minCount":%v}`, c.MinPeakRatio, c.CriticalPeakRatio, c.MinSustainedSec, c.MinCount))
	writeJSON(w, c)
}

// getProblemEscalation returns the age-based escalation config that
// drives escalateStaleProblems. Defaults live in chstore so a
// never-edited install reports the pre-v0.9.248 15min/30min ladder.
func (s *Server) getProblemEscalation(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.GetProblemEscalation(r.Context()))
}

// putProblemEscalation validates + persists the escalation windows.
// The evaluator picks them up on its own (the sweep reads through a
// short-lived memo), so no restart.
//
// Bounds are deliberately loose at the top end — an operator who
// wants "never escalate before 12h" is expressing a real policy, not
// a typo. The floor is 60s because anything shorter escalates inside
// a single evaluator tick, which reads as "it opened at critical"
// and makes the ladder meaningless.
func (s *Server) putProblemEscalation(w http.ResponseWriter, r *http.Request) {
	var c chstore.ProblemEscalationConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if c.InfoToWarningSec < 60 || c.InfoToWarningSec > 7*24*3600 {
		http.Error(w, "infoToWarningSec must be between 60 and 604800", http.StatusBadRequest)
		return
	}
	if c.WarningToCriticalSec < 60 || c.WarningToCriticalSec > 7*24*3600 {
		http.Error(w, "warningToCriticalSec must be between 60 and 604800", http.StatusBadRequest)
		return
	}
	// critical before warning would let an `info` problem jump
	// straight past `warning`. Reject rather than silently reorder —
	// the read-side clamp exists for legacy rows, not for hiding a
	// typo the operator could fix.
	if c.WarningToCriticalSec < c.InfoToWarningSec {
		http.Error(w, "warningToCriticalSec must be >= infoToWarningSec", http.StatusBadRequest)
		return
	}
	if err := s.store.SaveProblemEscalation(r.Context(), c); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "settings.problem_escalation.update", "settings", "problem_escalation",
		fmt.Sprintf(`{"enabled":%v,"infoToWarningSec":%v,"warningToCriticalSec":%v}`,
			c.Enabled, c.InfoToWarningSec, c.WarningToCriticalSec))
	writeJSON(w, c)
}

// ── AI Copilot ──────────────────────────────────────────────────────────────

// copilotConfig surfaces whether the feature is enabled — UI uses this
// to show or hide the "AI explain" buttons. Doesn't leak the key.
// wf — Active() (not Configured()): the AI affordances hide when the
// operator flips the "Enable AI Copilot" toggle off even though the
// creds are still stored. Active() nil-guards internally (s.copilot is
// nil when no key was ever configured), so no separate nil check.
//
// v0.9.1037 — yanıt `model` alanını da taşıyor (AI çekmecesinin model
// çipi). ÜÇ sınır, üçü de kasıtlı:
//
//   - Uç KİMLİK İSTER: auth.SkipPath("/api/copilot/config") false'tur,
//     yani anonim bir çağıran (public trace / status sayfaları) buradan
//     hiçbir şey okuyamaz. Model adı bu yüzden "yalnız kimlikli yanıt"
//     kuralını otomatik sağlar; copilotConfigPublicShapeTest bunu pinler.
//   - Model YALNIZ Active() iken sızar (copilot.ActiveModel) — kapalı
//     kurulum boş döner ve çip hiç çizilmez.
//   - baseURL / apiKey / provider ASLA girmez. Model adı operatörün
//     Helm values'ında duran bir tanımlayıcı; baseURL bir ADRES,
//     apiKey bir SIR. Yanıt tipi bu üçünü taşıyamayacak şekilde DAR
//     yazıldı (getAISettings admin yüzeyi ayrı ve öyle kalıyor).
//
// copilotProfileOption — v0.10.183: sohbet seçicisi için SIRSIZ görünüm
// (kimlik + etiket + model adı); baseUrl/apiKey/provider ASLA (şekil testi).
type copilotProfileOption struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	Model string `json:"model,omitempty"`
}

type copilotConfigResponse struct {
	Enabled bool `json:"enabled"`
	// omitempty: kapalı/modelsiz kurulumda alan hiç görünmez, yani
	// istemci "boş string mi yoksa yok mu" ayrımını yapmak zorunda
	// kalmaz — çip yoksa alan da yok.
	Model string `json:"model,omitempty"`
	// v0.10.183 — birden çok profil varsa seçici için liste + varsayılan; tek
	// profilde alanlar hiç görünmez (eski şekil aynen).
	Profiles       []copilotProfileOption `json:"profiles,omitempty"`
	DefaultProfile string                 `json:"defaultProfile,omitempty"`
}

func (s *Server) copilotConfig(w http.ResponseWriter, r *http.Request) {
	resp := copilotConfigResponse{
		Enabled: s.copilot.Active(),
		Model:   s.copilot.ActiveModel(),
	}
	if profiles, def, _ := s.copilot.ProfilesSnapshot(); len(profiles) > 1 {
		resp.DefaultProfile = def
		for _, p := range profiles {
			resp.Profiles = append(resp.Profiles, copilotProfileOption{ID: p.ID, Label: p.Label, Model: p.Model})
		}
	}
	writeJSON(w, resp)
}

// getAISettings returns the current Copilot config minus the actual
// key. UI uses it to drive the editable form. We expose
// {provider, model, baseUrl, hasKey} — the secret never round-trips.
// baseUrl is non-secret (operators put it in their Helm values), so
// we echo it so the UI can show "currently pointing at <local
// endpoint>" without operator memorisation.
func (s *Server) getAISettings(w http.ResponseWriter, r *http.Request) {
	// wf — enabled is the master on/off toggle, DISTINCT from hasKey
	// (creds stored). The Settings form binds the checkbox to enabled
	// and shows the "key stored" indicator off hasKey independently.
	provider, model, baseURL, hasKey, skipTLS, enabled := s.copilot.Snapshot()
	// v0.9.1120 (Faz 0.4) — LLM call tuning. These are OVERRIDES, not
	// effective values: 0 / null means "no override, running the
	// built-in default" (4096 / 0.2 / 180s). The form renders the
	// default as placeholder text off exactly that signal, so opening
	// Settings and saving doesn't silently freeze today's defaults into
	// the stored blob.
	maxTokens, temperature, timeoutS := s.copilot.TuningSnapshot()
	autoExplain := s.copilot.AutoExplainEnabled()
	pp := s.aiProfilesPayload() // tek snapshot (#15)
	writeJSON(w, map[string]any{
		"provider":    provider,
		"model":       model,
		"baseUrl":     baseURL,
		"hasKey":      hasKey,
		"skipTls":     skipTLS,
		"enabled":     enabled,
		"maxTokens":   maxTokens,
		"temperature": temperature,
		"timeoutS":    timeoutS,
		"autoExplain": autoExplain,
		// v0.10.172 — serbest soru sınıflandırıcısı kipi (copilot_intent.go).
		"intentClassify": s.copilot.IntentClassifyMode(),
		// v0.10.175 — model profilleri (ai_settings_profiles.go); düz alanlar VARSAYILAN profildir.
		"profiles":       pp["profiles"],
		"defaultProfile": pp["defaultProfile"],
		"surfaceMap":     pp["surfaceMap"],
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
		// wf — Enabled is a POINTER so an older PUT client that doesn't
		// send the field (nil) defaults to enabled=true and can't
		// accidentally disable AI. The Settings UI always sends it.
		Enabled *bool `json:"enabled"`
		// v0.9.1120 (Faz 0.4) — LLM call tuning. Omitted / 0 / null =
		// "use the built-in default", which is what an older client that
		// knows nothing about these fields sends, so it cannot clamp the
		// budget by accident. Temperature is a POINTER for the same
		// reason it is one in the blob: 0 is a valid temperature.
		MaxTokens   int      `json:"maxTokens"`
		Temperature *float64 `json:"temperature"`
		TimeoutS    int      `json:"timeoutS"`
		// v0.9.1138 — arka plan otomatik açıklayıcı vidası. *bool,
		// Enabled idyomu: alanı bilmeyen eski istemci nil gönderir ve
		// nil⇒açık — kazara kapatamaz.
		AutoExplain *bool `json:"autoExplain"`
		// v0.10.175 — boş apiKey = MEVCUT korunur (resolveLegacyAPIKey); açık
		// temizleme yalnız clearKey:true ile (AiTab "Clear key" yolu).
		ClearKey bool `json:"clearKey"`
		// v0.10.172 — bkz. copilot.IntentOff/On/OnNoLoop. *string: alanı
		// bilmeyen eski istemci nil gönderir → MEVCUT değer korunur (autoExplain
		// deyimi; "" kabul edilseydi bayat bundle açık 'off'u sessizce sıfırlardı).
		IntentClassify *string `json:"intentClassify"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	intentClassify := s.copilot.IntentClassifyMode()
	if in.IntentClassify != nil {
		intentClassify = *in.IntentClassify
	}
	if err := copilot.ValidateIntentClassify(intentClassify); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Provider != "" &&
		in.Provider != copilot.ProviderAnthropic &&
		in.Provider != copilot.ProviderGitHub &&
		in.Provider != copilot.ProviderOpenAI {
		http.Error(w, "provider must be 'anthropic', 'github' or 'openai'", http.StatusBadRequest)
		return
	}
	// Bounds live next to the defaults in internal/copilot (pure +
	// table-tested) rather than being spelled out here — one place to
	// read "what is a legal knob value".
	if err := copilot.ValidateTuning(in.MaxTokens, in.Temperature, in.TimeoutS); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	enabled := in.Enabled == nil || *in.Enabled
	curKey := ""
	if profiles, def, _ := s.copilot.ProfilesSnapshot(); def != "" {
		for _, p := range profiles {
			if p.ID == def {
				curKey = p.APIKey
			}
		}
	}
	apiKey := resolveLegacyAPIKey(in.APIKey, curKey, in.ClearKey)
	if err := s.copilot.SavePersisted(r.Context(), s.store, in.Provider, apiKey, in.Model, in.BaseURL, in.SkipTLS, enabled, in.MaxTokens, in.Temperature, in.TimeoutS, in.AutoExplain, intentClassify); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishConfigReload(r.Context(), "ai")
	provider, model, baseURL, hasKey, skipTLS, enabledNow := s.copilot.Snapshot()
	maxTokensNow, temperatureNow, timeoutSNow := s.copilot.TuningSnapshot()
	// apiKey itself never enters audit_log; hasKey is the only
	// secret-adjacent bit and it's already part of the public GET.
	// wf — enabled recorded so the audit trail shows the operator
	// flipping AI off/on (the disable-without-clearing-creds action).
	// v0.9.1120 — the tuning knobs join details (same entry kind /
	// resource / id): "why did every explanation get truncated last
	// Tuesday" has to be answerable from audit_log, and maxTokens is
	// exactly the knob that would do it.
	details, _ := json.Marshal(map[string]any{
		"provider": provider, "model": model, "baseUrl": baseURL, "hasKey": hasKey, "skipTls": skipTLS, "enabled": enabledNow,
		"maxTokens": maxTokensNow, "temperature": temperatureNow, "timeoutS": timeoutSNow,
		"intentClassify": s.copilot.IntentClassifyMode(),
	})
	s.audit(r, "settings.ai.update", "settings", "ai", string(details))
	pp := s.aiProfilesPayload() // tek snapshot (#15)
	writeJSON(w, map[string]any{
		"provider":       provider,
		"model":          model,
		"baseUrl":        baseURL,
		"hasKey":         hasKey,
		"skipTls":        skipTLS,
		"enabled":        enabledNow,
		"maxTokens":      maxTokensNow,
		"temperature":    temperatureNow,
		"timeoutS":       timeoutSNow,
		"autoExplain":    s.copilot.AutoExplainEnabled(),
		"intentClassify": s.copilot.IntentClassifyMode(),
		"defaultModel":   s.copilot.DefaultModels(), // v0.10.253 — tek kaynak (D5)
		"profiles":       pp["profiles"],
		"defaultProfile": pp["defaultProfile"],
		"surfaceMap":     pp["surfaceMap"],
	})
}

// copilotExplainTrace fetches the spans for a trace, builds a compact
// JSON description, and asks the model for an SRE-flavoured summary.
// Heavy lifting (gathering context) happens server-side so the
// browser doesn't ship trace data back to the API just to ship it on
// to Anthropic.
//
// v0.9.482 — kanıt montajı (span seçimi + ilişkili loglar + dürüstlük
// notu) buildTraceExplainInput'a taşındı: AI çekmecesindeki sohbet AYNI
// paketi kurabilsin (operatör raporu: "logda ne yazıyor" takipleri kör
// cevaplanıyordu). Prompt bayt-bayt aynıdır — explain_trace_input_test.go
// pinler. Emsal: anomaly.BuildExceptionExplainInput (v0.9.415).
func (s *Server) copilotExplainTrace(w http.ResponseWriter, r *http.Request) {
	r, xid := withExchange(r)
	in, err := s.buildTraceExplainInput(r.Context(), r.PathValue("id"))
	if errors.Is(err, errExplainTraceNotFound) {
		http.Error(w, "trace not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	// v0.9.831 — "Kodu da incele" (opsiyonel gövde). Kod bağlamı
	// trace'in LOGLARINDAKİ stacktrace'ten çıkar ve o stack'i basan
	// SERVİSİN deposunda aranır (bkz. traceExplainInput.StackService).
	opts := decodeExplainOptions(r)
	var cc devops.CodeContext
	run := s.explainPrompt(r, copilot.SystemPromptTrace(), in.User)
	// v0.10.83 — önbellek anahtarı GERÇEK prompt'tan: kod dalında kod
	// bloğu da kimliğe girer (kodlu/kodsuz cevap ayrı satır; blok
	// değişirse anahtar değişir).
	cacheKey := explainCacheKey(copilot.SystemPromptTrace(), in.User, "")
	if opts.IncludeCode {
		cc = s.buildCodeContext(r.Context(), in.StackService, in.Stack)
		// v0.10.115 — SQL hatasında şema kanıtı (hata span'ının db_statement'ı
		// → katalog); kod bloğunun arkasına, kendi bütçesiyle.
		se := s.buildSchemaEvidence(in.ErrorText, in.DBStatements, mapperBlocks(cc))
		cacheKey = explainCacheKey(copilot.SystemPromptTraceWithCode(), in.User, cc.PromptBlock()+se.Block)
		run = explainPromptBuffered(func() (string, error) {
			return s.copilotExplainEvidence(r,
				copilot.SystemPromptTrace(), copilot.SystemPromptTraceWithCode(), in.User, cc, se)
		})
	}
	// v0.9.1127 (Faz 1.5) — cevabın çıkışı tek yerden (deliverExplain).
	s.deliverExplain(w, r, xid, map[string]any{
		"evidenceSpanIds": in.Evidence,
		"code":            codePayload(cc, opts.IncludeCode),
	}, run, in.RootService, cacheKey)
}

// copilotExplainSpan focuses the LLM on ONE span instead of the
// whole trace: target span + parent + direct children + any
// error spans in the same trace. Tighter prompt + cheaper round-
// trip than re-summarising the entire waterfall. Works with any
// configured backend — Anthropic, OpenAI, or a local LLM via
// OpenAI-compatible base URL (Ollama / vLLM / LM Studio).
func (s *Server) copilotExplainSpan(w http.ResponseWriter, r *http.Request) {
	traceID := r.PathValue("traceId")
	spanID := strings.TrimSpace(r.URL.Query().Get("span"))
	if spanID == "" {
		http.Error(w, "span query param required", http.StatusBadRequest)
		return
	}
	spans, _, err := s.resolveTraceSpans(r.Context(), traceID) // v0.9.632 — Tempo fallback dahil
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(spans) == 0 {
		http.Error(w, "trace not found", http.StatusNotFound)
		return
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
		http.Error(w, "span not found in trace", http.StatusNotFound)
		return
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
		spanID, traceID, len(compact), promptfmt.FenceSafe(string(payload))) // v0.10.404 — çit kaçışı
	r, xid := withExchange(r)
	s.deliverExplain(w, r, xid, nil, s.explainPrompt(r, copilot.SystemPromptSpan(), user), "", explainCacheKey(copilot.SystemPromptSpan(), user, ""))
}

// copilotExplainProblem fetches a Problem and asks the model for a
// likely-cause + first-action summary. Useful on a fresh page during
// an incident.
func (s *Server) copilotExplainProblem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// v0.9.343 — ask for the row, don't page the newest 1000 and scan.
	//
	// This paged ListProblems(Limit: 1000) and linear-scanned in Go, so any
	// problem outside the newest 1000 answered "problem not found" — the same
	// answer as "deleted", for a row the operator is looking at. Identical
	// shape to the v0.9.332 GetIncident bug, with the right primitive
	// (GetProblem, WHERE id = ?) already sitting in the same package.
	p, err := s.store.GetProblem(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if p == nil {
		http.Error(w, "problem not found", http.StatusNotFound)
		return
	}
	// Attach deploy correlation for the explain prompt — the
	// "regression right after a deploy" pattern is the single
	// highest-signal cause hint the model should consider first.
	enriched := s.store.EnrichProblemsWithDeploys(r.Context(),
		[]chstore.Problem{*p}, 30*time.Minute)
	p = &enriched[0]
	user := fmt.Sprintf(
		"Service: %s\nMetric: %s\nValue: %.2f%s (threshold %.2f%s)\nSeverity: %s\nRule: %s\nDescription: %s",
		p.Service, p.Metric, p.Value, problemMetricUnitTR(p.Metric), p.Threshold, problemMetricUnitTR(p.Metric), p.Severity, p.RuleName, p.Description,
	)
	if p.RecentDeploy != nil {
		user += fmt.Sprintf(
			"\n\nRecent deploy: service.version=%q first seen %d seconds before this problem opened. Consider whether this regression coincides with that deploy.",
			p.RecentDeploy.Version, p.RecentDeploy.AgeSeconds)
	}
	// v0.8.394 (AI audit A1) — fuse the persisted deterministic root-cause
	// hypothesis (RootCauseSynthesizer) into the operator-clicked explain,
	// the SAME block the background auto-explainer injects — the operator
	// who sees the "Root cause: X (NN%)" ribbon must not get an Explain
	// that ignores it. Best-effort: absent/erroring hypothesis renders "".
	if hyp, err := s.store.GetHypothesis(r.Context(), "problem", p.ID); err == nil {
		if block := anomaly.HypothesisPromptBlockTR(hyp); block != "" {
			user += "\n" + block
		}
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
		user += s.serviceCorrelationContext(r.Context(), p.Service, time.Unix(0, p.StartedAt))
	}
	r, xid := withExchange(r)
	s.deliverExplain(w, r, xid, nil, s.explainPrompt(r, copilot.SystemPromptProblem(), user), "", explainCacheKey(copilot.SystemPromptProblem(), user, ""))
}

// serviceCorrelationContext gathers the multi-signal evidence the
// root-cause prompt reasons over (v0.6.54; v0.9.418'de problem'dan
// servis-genel hale getirildi — anomaly explain de kullanır): 1-hop
// topology neighbours + error-trace exemplars around the anchor time.
// All bounded (top-5, windowed) and best-effort — any signal that
// errors or comes up empty is simply omitted so a flaky lookup never
// blocks the explain. Window is [anchor-30m, anchor+5m]; the leading
// 30m captures the run-up, the trailing 5m the immediate aftermath.
func (s *Server) serviceCorrelationContext(ctx context.Context, service string, anchor time.Time) string {
	var b strings.Builder
	from, to := anchor.Add(-30*time.Minute), anchor.Add(5*time.Minute)

	// 1) Topology neighbours — who calls / is called by this service.
	// Direction is the key root-cause hint: a callee erroring points
	// downstream, a caller spike points upstream.
	if up, down, _, _, err := s.store.ServiceNeighbors(ctx, service, 35*time.Minute, 50); err == nil && (len(up) > 0 || len(down) > 0) {
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
		Service: service, HasError: true, From: from, To: to,
		Limit: 5, Sort: "time", Order: "desc", CountMode: "skip",
	}); terr == nil && len(traces) > 0 {
		b.WriteString("\n\nError-trace exemplars (this service, in window):")
		for _, t := range traces {
			b.WriteString(fmt.Sprintf("\n  %s — %.0fms, %d spans", t.RootName, t.DurationMs, t.SpanCount))
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
	// Two-stage lookup: IncidentProblems returns IDs only, then the rows.
	// Capped at 8 to keep the prompt size reasonable.
	//
	// v0.9.343 — the second stage used to page ListProblems(Limit: 2000) and
	// keep the wanted subset, so an attached problem older than the newest
	// 2000 silently vanished from the prompt: the model was told the incident
	// had FEWER attached problems than it does, and answered confidently on
	// the smaller set. Fails quiet — it degrades the explanation instead of
	// erroring. Now the ids go into the query.
	pIDs, _ := s.store.IncidentProblems(r.Context(), id)
	if len(pIDs) > 8 {
		pIDs = pIDs[:8]
	}
	var probs []chstore.Problem
	if len(pIDs) > 0 {
		probs, _ = s.store.ListProblems(r.Context(), chstore.ProblemFilter{
			IDs: pIDs, Limit: len(pIDs),
		})
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
	r, xid := withExchange(r)
	s.deliverExplain(w, r, xid, nil, s.explainPrompt(r, copilot.SystemPromptIncident(), user), "", explainCacheKey(copilot.SystemPromptIncident(), user, ""))
}

// copilotExplainAnomaly handles log-pattern / trace-op anomaly
// rows. Different model prompt — anomalies are soft signals
// ("something changed") so the response should help an
// operator decide whether to act, not assume something is
// broken.
func (s *Server) copilotExplainAnomaly(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// v0.9.343 — ask for the row, don't page 30 days and scan.
	//
	// The old comment argued "anomalies have short retention (30d) so the
	// universe is small", but the read was capped at 2000 rows AND — since
	// v0.9.326 — ordered `status = 'active' DESC, last_seen DESC`. So a
	// CLEARED anomaly past the 2000th row could not be explained: the handler
	// answered "anomaly not found" for a row the operator had open in front
	// of them. GetAnomalyEvent does WHERE id = ?, which is both correct and
	// cheaper than the window it replaces.
	ev, err := s.store.GetAnomalyEvent(r.Context(), id, 0)
	if err != nil {
		writeErr(w, err)
		return
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
	// v0.9.418 (CoSRE fikir #3 — korelasyon zinciri): explain-problem'ın
	// üçlüsü artık anomaly'de de. Sıra bilinçli: deterministik hipotez
	// ÖNCE (model onu birincil kanıt sayar), sonra deploy penceresi,
	// sonra topology komşuları + hata-trace örnekleri. Hepsi best-effort.
	if hyp, herr := s.store.GetHypothesis(r.Context(), "anomaly", ev.ID); herr == nil {
		if block := anomaly.HypothesisPromptBlockTR(hyp); block != "" {
			user += "\n" + block
		}
	}
	if ev.Service != "" {
		started := time.Unix(0, ev.StartedAt)
		if deps, derr := s.store.GetServiceDeploys(r.Context(), ev.Service,
			started.Add(-6*time.Hour), time.Unix(0, ev.LastSeen)); derr == nil && len(deps) > 0 {
			if parts := anomaly.PickDeploysAroundStart(deps, ev.StartedAt); len(parts) > 0 {
				user += "\n\nAynı servisin yakın DEPLOY'ları: " + fmt.Sprintf("%v", parts)
			}
		}
		user += s.serviceCorrelationContext(r.Context(), ev.Service, started)
		user += "\n\nZİNCİRİ anlat: anomali → (varsa) tetikleyen deploy → komşu servislere etkisi. Yönü belirt (upstream'den mi geldi, downstream'e mi yayılıyor); kanıt yoksa 'bilinmiyor' de."
	}
	r, xid := withExchange(r)
	s.deliverExplain(w, r, xid, nil, s.explainPrompt(r, copilot.SystemPromptAnomaly(), user), "", explainCacheKey(copilot.SystemPromptAnomaly(), user, ""))
}

// truncate caps s at n BYTES, cutting on a rune boundary.
//
// v0.9.842 — the cut used to be a bare s[:n], which lands mid-rune on
// any multi-byte character. json.Marshal then replaces the fragment
// with U+FFFD, so every truncated Turkish log body ended in a silent
// "" — evidence corruption in exactly the field the Explain prompts
// quote from (the v0.9.414 lesson, one layer down). The budget stays a
// BYTE budget so no caller's prompt cost changes; only the cut moves,
// by at most three bytes.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
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
	// v0.9.392 (Faz B-2) — üç ayrı sorgu tek Multi'ye indi: aynı WHERE,
	// tek CH taraması (SPA'nın batch yolu ile aynı şekil). Soft-fail aynı.
	redM, _, _ := s.store.QuerySpanMetricMulti(r.Context(), chstore.SpanMetricBatchFilter{
		Filters: svcFilter, From: from, To: to, GroupBy: []string{"name"},
		Aggs: []chstore.SpanMetricAggSpec{
			{Name: "rate", Aggregation: "rate"},
			{Name: "error_rate", Aggregation: "error_rate"},
			{Name: "p99", Aggregation: "p99", Field: "duration_ms"},
		},
	})
	rps, errs, p99 := redM["rate"], redM["error_rate"], redM["p99"]

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
	r, xid := withExchange(r)
	s.deliverExplain(w, r, xid, nil, s.explainPrompt(r, copilot.SystemPromptServiceHealth(), user), "", explainCacheKey(copilot.SystemPromptServiceHealth(), user, ""))
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
	id := r.PathValue("id")
	// v0.9.343 — ask for the row, don't page the newest 1000 and scan.
	//
	// This paged ListProblems(Limit: 1000) and linear-scanned in Go, so any
	// problem outside the newest 1000 answered "problem not found" — the same
	// answer as "deleted", for a row the operator is looking at. Identical
	// shape to the v0.9.332 GetIncident bug, with the right primitive
	// (GetProblem, WHERE id = ?) already sitting in the same package.
	p, err := s.store.GetProblem(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
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
		"  Service: %s\n  Metric: %s\n  Value: %.2f%s (threshold %.2f%s)\n  Severity: %s\n  Rule: %s\n  Description: %s\n",
		p.Service, p.Metric, p.Value, problemMetricUnitTR(p.Metric), p.Threshold, problemMetricUnitTR(p.Metric), p.Severity, p.RuleName, p.Description)
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

	// v0.9.1198 (Faz 5.5) — kanıt zinciri girdiye eklendi: checklist
	// artık "geçmişte ne kadar sürdü"nün yanında otomatik araştırmacının
	// neyi suçlu bulduğunu da biliyor (şüpheli + aday yollar + bulunan
	// derin sinyaller). Hipotez yoksa blok hiç eklenmez.
	if hyp, _ := s.store.GetHypothesis(r.Context(), "problem", id); hyp != nil {
		if chain := renderRunbookEvidenceChain(hyp); chain != "" {
			sb.WriteString("\n" + chain)
		}
	}

	r, xid := withExchange(r)
	s.deliverExplain(w, r, xid,
		map[string]any{"similarCount": len(similar)},
		s.explainPrompt(r, copilot.SystemPromptRunbook(), sb.String()), "",
		explainCacheKey(copilot.SystemPromptRunbook(), sb.String(), ""))
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
//   - Either trace not found → 404 with the missing id.
//   - Both traces identical (same ID typed twice) → still
//     valid; the model will say "essentially the same".
func (s *Server) copilotCompareTraces(w http.ResponseWriter, r *http.Request) {
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

	aSpans, _, err := s.resolveTraceSpans(r.Context(), aID) // v0.9.632 — Tempo fallback dahil
	if err != nil || len(aSpans) == 0 {
		http.Error(w, "trace A not found: "+aID, http.StatusNotFound)
		return
	}
	bSpans, _, err := s.resolveTraceSpans(r.Context(), bID) // v0.9.632 — Tempo fallback dahil
	if err != nil || len(bSpans) == 0 {
		http.Error(w, "trace B not found: "+bID, http.StatusNotFound)
		return
	}

	user := buildCompareTracesPrompt(aID, aSpans, bID, bSpans)
	r, xid := withExchange(r)
	out, err := s.copilotExplain(r,
		copilot.SystemPromptCompareTraces(), user)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"explanation": out, "exchangeId": xid})
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
		Count     uint64  `json:"count"`
		RPS       float64 `json:"rps"`
		ErrorRate float64 `json:"errorRate"`
		P99Ms     float64 `json:"p99Ms"`
		AvgMs     float64 `json:"avgMs"`
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
		         `+s.store.ShardSkipSetting(),
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
	// v0.9.231 (scale-audit) — groupArrayIf RETAINS duplicates: it
	// materialised one String per matching ROW into each array before
	// arrayDistinct ever ran. Over the ±6h window this handler allows, on a
	// busy service at 1B spans/day, that is tens of millions of strings held
	// in memory — max_execution_time bounds wall clock, not memory, and this
	// route is viewer-baseline. groupUniqArrayIf(N) dedupes as it goes and
	// caps the state, which is the form every other site in the codebase
	// uses (podservice.go, deploys.go, hosts.go, topology.go). 200 is far
	// past the 10 the prompt keeps.
	opsRows, err := s.store.Conn().Query(r.Context(), `
		WITH
		  groupUniqArrayIf(200)(name, time < ? AND name != '')   AS bef_ops,
		  groupUniqArrayIf(200)(name, time >= ? AND name != '')  AS aft_ops
		SELECT arrayDistinct(arrayFilter(x -> NOT has(bef_ops, x), aft_ops)) AS new_ops
		FROM spans
		WHERE service_name = ? AND time >= ? AND time <= ?
		SETTINGS max_execution_time = 10,
		         `+s.store.ShardSkipSetting(),
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

	r, xid := withExchange(r)
	out, err := s.copilotExplain(r,
		copilot.SystemPromptDeployImpact(), sb.String())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"explanation": out,
		"exchangeId":  xid,
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
	// v0.9.1083 (F3.4) — yörünge artık DETERMİNİSTİK girdide: /forecast
	// ve /burn-series uçları vardı ama anlatım beslemesi yoktu; prompt
	// "Y hours to exhaustion" isteyince model Y'yi UYDURUYORDU. Soft-
	// fail: üretilemezse kanıt bunu açıkça söyler, model de öyle der.
	fc, _ := s.store.ComputeSLOForecast(r.Context(), *slo, time.Hour)
	series, _ := s.store.ComputeSLOBurnSeries(r.Context(), *slo, 7)
	sb.WriteString(sloTrajectoryEvidence(fc, series))

	r, xid := withExchange(r)
	out, err := s.copilotExplain(r,
		copilot.SystemPromptSLOBurn(), sb.String())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"explanation": out,
		"exchangeId":  xid,
		"status":      status,
		"fastBurn":    fastRate,
		"slowBurn":    slowRate,
	})
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
		k        key
		aMs, bMs float64
		dMs      float64
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
//   - Service has no recent spans → fingerprint is sparse but
//     the model still produces a sensible "low confidence"
//     guess from the name alone.
//   - Model returns prose instead of JSON → we surface a
//     "no suggestions available" rather than poisoning the
//     form with non-JSON garbage.
func (s *Server) copilotSuggestServiceTags(w http.ResponseWriter, r *http.Request) {
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
	got.runtime, _ = s.store.GetServiceRuntime(r.Context(), service, time.Now())
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
	// Top span operations — v0.9.1067 (Faz 3.6 / Q11): MV'den.
	// Ham spans GROUP BY name invariant #3 ihlaliydi (sınırlıydı ama
	// operation_summary_5m aynı cevabı önceden toplanmış veriyor).
	if rows, err := s.store.Conn().Query(r.Context(), `
		SELECT name, countMerge(span_count_state) AS c
		FROM operation_summary_5m
		WHERE service_name = ? AND time_bucket >= now() - INTERVAL 24 HOUR
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

	// v0.9.1067 (Faz 3.6 / Q7) — katı-JSON yüzeyi JSON kipinden geçer
	// (kardeşleri nl_query/ch_optimize gibi); elle {..} ayıklama
	// savunma olarak duruyor ama artık nadiren gerekir.
	out, err := s.copilotExplainJSON(r,
		copilot.SystemPromptServiceTags(), sb.String(), nil)
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
		// v0.8.387 — global ?env= picker (service-scoped, see
		// ProblemFilter.Env). The sidebar badge passes it so badge
		// and /problems list agree; the env→services resolution
		// rides the 60s-cached map, so the 30s-per-user poll adds
		// no query beyond this COUNT.
		Env: strings.TrimSpace(q.Get("env")),
	}
	key := fmt.Sprintf("problems-count:status=%s:svc=%s:sev=%s:env=%s",
		f.Status, f.Service, f.Severity, f.Env)
	// v0.8.471 (perf dalga-1 #1) — TTL 5s→15s: SWR sert penceresi
	// 3×TTL'dir; 5s'te pencere (15s) 30s'lik rozet poll'unun ALTINDA
	// kalıyor ve her poll soğuk CH sorgusu ödüyordu (canlı p95 751ms).
	// 15s'te pencere 45s > 30s → poll STALE yolundan <10ms döner, arka
	// planda tazelenir. Ack/resolve cacheInvalidatePrefix("problems")
	// yayınladığından read-your-writes bozulmaz.
	s.serveCached(w, r, key, 15*time.Second, func(ctx context.Context) (any, error) {
		n, err := s.store.CountProblems(ctx, f)
		if err != nil {
			return nil, err
		}
		return map[string]any{"count": n}, nil
	})
}

// countProblemsByRule — /alerts satır rozeti: kural başına açık
// problem sayısı (v0.9.1109, Faz 5 "alarm→veri yolu bugün çıkmaz
// sokak"). Girdisi yok; sabit anahtar hash-all-inputs kuralını
// boşlukta sağlar. TTL 15s — countProblems'la aynı gerekçe (SWR
// sert penceresi 45s, 30-60s'lik sayfa poll'unun üstünde kalır;
// ack/resolve cacheInvalidatePrefix("problems") ile taze düşer).
func (s *Server) countProblemsByRule(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "problems-rule-counts", 15*time.Second, func(ctx context.Context) (any, error) {
		m, err := s.store.CountOpenProblemsByRule(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"counts": m}, nil
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
		// v0.8.387 — same service-scoped env narrowing as the list +
		// count endpoints (ProblemFilter.Env resolves it in SQL), so
		// chip counts can't disagree with the rows under an env pick.
		Env:   strings.TrimSpace(q.Get("env")),
		Limit: 2000,
	}
	// v0.9.474 (dürüstlük A16) — team/cluster da chip'lere iner: liste bu
	// daraltmaları SQL servis setine çeviriyordu, chip'ler görmüyordu —
	// "mirrors every OTHER filter" iddiası team/cluster altında yanlıştı
	// (takım seçili operatörde P1 chip'i tüm filoyu sayıyordu). Aynı
	// çözümleme listeyle bire bir; çözümleme hatası daraltmayı KAPALI
	// bırakır (liste ile aynı fail-open duruşu).
	ownerTeam := strings.TrimSpace(q.Get("ownerTeam"))
	sreTeam := strings.TrimSpace(q.Get("sreTeam"))
	clusterFilter := strings.TrimSpace(q.Get("cluster"))
	key := fmt.Sprintf("problems-buckets:v2:status=%s:svc=%s:env=%s:owner=%s:sre=%s:cluster=%s",
		f.Status, f.Service, f.Env, ownerTeam, sreTeam, clusterFilter)
	s.serveCached(w, r, key, 5*time.Second, func(ctx context.Context) (any, error) {
		if ownerTeam != "" || sreTeam != "" {
			if mds, err := s.store.ListServiceMetadata(ctx); err == nil {
				f.Services = servicesForTeam(s.teamAliasesCtx(ctx), mds, ownerTeam, sreTeam)
				// v0.9.1345 — listedeki kardeşiyle AYNI istisna: db
				// özneleri servis-adı listesinde olamaz, sahiplikleri
				// türetiliyor, o yüzden SQL daraltmasından geçip
				// aşağıdaki kesin eşleştirmeye ulaşmalılar. Bayrak
				// olmadan chip'ler db problemlerini SAYMAZ ama liste
				// GÖSTERİR — v0.9.474'ün kapattığı "chip ile satır
				// ıraksıyor" kusurunun aynısı, db tarafında.
				f.ServicesAllowDBSubjects = true
			}
		}
		if clusterFilter != "" {
			if cm, err := s.store.GetServiceClusterMap(ctx, time.Hour); err == nil {
				inCluster := make([]string, 0, 32)
				for svc, cs := range cm {
					for _, c := range cs {
						if c == clusterFilter {
							inCluster = append(inCluster, svc)
							break
						}
					}
				}
				f.Services = intersectServices(f.Services, inCluster)
			}
		}
		probs, err := s.store.ListProblems(ctx, f)
		if err != nil {
			return nil, err
		}
		// v0.9.1345 — SQL daraltması db öznelerini GEVŞETİYOR (yukarıdaki
		// bayrak), yani kesin eşleştirme burada yapılmak ZORUNDA. Listenin
		// ikinci geçişiyle birebir aynı: zenginleştir, sonra
		// matchesTeamFilter. Aksi hâlde chip'ler BAŞKA takımların db
		// problemlerini de sayardı.
		if ownerTeam != "" || sreTeam != "" {
			probs = s.store.EnrichProblemsWithTeams(ctx, probs)
			ta := s.teamAliasesCtx(ctx)
			keep := probs[:0]
			for _, p := range probs {
				if matchesTeamFilter(ta, p.OwnerTeam, p.SRETeam, ownerTeam, sreTeam) {
					keep = append(keep, p)
				}
			}
			probs = keep
		}
		// Priority depends on RecentDeploy (fresh-deploy bump), so
		// the deploys enrich must run before the priority enrich.
		// Runbooks/clusters enrichment skipped — buckets don't need
		// either, and skipping the CH round-trips keeps this cheap.
		probs = s.enrichProblemsForRead(ctx, probs) // v0.9.553 — deploy+öncelik, sırası sabit
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
	// Owner/SRE team filters (v0.8.290) — resolved server-side
	// against the service catalog (read-time enriched onto each
	// Problem) so the narrowing is correct across the whole result,
	// not just the loaded rows. Empty value = "all". Same
	// EqualFold/empty-means-all semantics as the inbox.
	ownerTeam := strings.TrimSpace(q.Get("ownerTeam"))
	sreTeam := strings.TrimSpace(q.Get("sreTeam"))
	// Cluster filter (v0.9.181) — narrows to problems whose service touched
	// the selected cluster (p.Clusters, enriched at read time by
	// EnrichProblemsWithClusters). Post-enrich + full-set → correct across the
	// whole result, not just the loaded page (server-paged table discipline).
	// Empty = all clusters. (clusterFilter, not `cluster` — that name is the
	// imported cluster package.)
	clusterFilter := strings.TrimSpace(q.Get("cluster"))
	f := chstore.ProblemFilter{
		Status: q.Get("status"), Service: q.Get("service"),
		Severity: q.Get("severity"),
		// v0.9.583 — Priority alanı ProblemFilter'dan KALDIRILDI: mağaza
		// ona hiç bakmıyordu (öncelik okuma anı hesabı, CH kolonu değil).
		// Daraltma aşağıda AÇIKÇA yapılıyor; tarama da problemScanLimit
		// ile ona göre genişletiliyor (birkaç satır aşağıda).
		// v0.8.387 — env-separation Phase 3: the global ?env= picker.
		// Service-scoped semantics (problems carry no env dimension;
		// ProblemFilter.Env narrows to services seen in the env in the
		// last hour via the 60s-cached map). Resolved in SQL inside
		// ListProblems so this list, /count and /buckets agree.
		Env:   strings.TrimSpace(q.Get("env")),
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
	// v0.9.342 — priority cannot move into SQL (it is computed at read time
	// from the enriched values), so the candidate scan widens instead. Picking
	// P1 used to return "the P1s among the newest 100", not "the newest 100
	// P1s": on prod, with ~800 open problems, a P1 selection could show a
	// fraction of what exists and nothing said so.
	sqlLimit := problemScanLimit(f.Limit, len(prios) > 0 && len(prios) < 3)
	key := fmt.Sprintf("problems:v2:status=%s:svc=%s:sev=%s:prio=%s:owner=%s:sre=%s:env=%s:cluster=%s:limit=%d:scan=%d",
		f.Status, f.Service, f.Severity, excludeKeyDigest(prioMap), ownerTeam, sreTeam, f.Env, clusterFilter, f.Limit, sqlLimit)
	// v0.8.471 — count ile aynı gerekçe (liste p95 903ms/max 3s → STALE ~10ms).
	s.serveCached(w, r, key, 15*time.Second, func(ctx context.Context) (any, error) {
		// v0.9.342 — team and cluster both resolve to a SERVICE SET, so they
		// go into SQL where the LIMIT can respect them. They used to run in
		// Go over the already-capped page, which is the defect family swept
		// out of the triage queue in v0.9.322/330/335/336: a filter applied
		// after a LIMIT answers over a slice, and the rows the operator asked
		// for may never have entered the window.
		//
		// Resolution failures leave the filter OFF rather than narrowing to
		// nothing: a catalog blip must not silently empty the page.
		scan := f
		scan.Limit = sqlLimit
		if ownerTeam != "" || sreTeam != "" {
			if mds, err := s.store.ListServiceMetadata(ctx); err == nil {
				scan.Services = servicesForTeam(s.teamAliasesCtx(ctx), mds, ownerTeam, sreTeam)
				// v0.9.1345 — db özneleri bu listede OLAMAZ (liste servis
				// adlarından kuruluyor, onların `service` alanı bir
				// DBSubjectID). Sahiplikleri artık TÜRETİLİYOR, o yüzden
				// SQL daraltmasından geçmeliler; kesin eşleştirmeyi
				// aşağıdaki matchesTeamFilter geçişi yapıyor. Bu satır
				// olmadan satırın çipi "core-banking" yazarken
				// owner=core-banking süzgeci onu SESSİZCE gizlerdi.
				scan.ServicesAllowDBSubjects = true
			}
		}
		if clusterFilter != "" {
			if cm, err := s.store.GetServiceClusterMap(ctx, time.Hour); err == nil {
				inCluster := make([]string, 0, 32)
				for svc, cs := range cm {
					for _, c := range cs {
						if c == clusterFilter {
							inCluster = append(inCluster, svc)
							break
						}
					}
				}
				scan.Services = intersectServices(scan.Services, inCluster)
			}
		}
		probs, err := s.store.ListProblems(ctx, scan)
		scanTruncated := err == nil && len(probs) >= sqlLimit
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
		probs = s.store.EnrichProblemsWithRunbooks(ctx, probs)
		// Owner/SRE team chips + the source of truth for the team
		// filter below (v0.8.290) — pulled from the service catalog
		// at read time, same batch-lookup shape as runbooks. Mirrors
		// the inbox enrichment so /problems and /inbox agree on which
		// team owns a firing service.
		probs = s.store.EnrichProblemsWithTeams(ctx, probs)
		// Cluster chips — same read-time pattern. One batch
		// CH query for the service→clusters map, soft-fails
		// silently on error so a transient blip doesn't
		// blank the page.
		probs = s.store.EnrichProblemsWithClusters(ctx, probs, time.Hour)
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
		// Priority bucket (v0.5.210) — pure function over the
		// already-enriched values, no CH round-trip. Runs last
		// so it can read RecentDeploy + value/threshold +
		// status in their final form. Recomputed on every read
		// so a worsening metric or fresh deploy reranks
		// instantly without rewriting the problems row.
		// v0.9.553 — deploy+öncelik ikilisi tek çağrıda; sıra
		// artık yorumla değil kodla garanti (problem_enrich.go).
		probs = s.enrichProblemsForRead(ctx, probs)
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
		// Team filter — v0.9.342 pushed this into SQL (scan.Services above).
		// Kept as a second pass because the SQL narrow is skipped when the
		// catalog read fails, and because a service can be added to a team
		// between the two reads. Cheap over an already-capped page; it is no
		// longer the ONLY place the filter applies, which is what made the
		// page lie.
		if ownerTeam != "" || sreTeam != "" {
			ta := s.teamAliasesCtx(r.Context())
			keep := probs[:0]
			for _, p := range probs {
				if matchesTeamFilter(ta, p.OwnerTeam, p.SRETeam, ownerTeam, sreTeam) {
					keep = append(keep, p)
				}
			}
			probs = keep
		}
		// Cluster filter (v0.9.181) — likewise pushed into SQL in v0.9.342 and
		// kept here as the exact-membership check: the SQL set comes from the
		// service→cluster map, this reads the per-problem enrichment, and the
		// two can differ at the edges of the 1h window.
		if clusterFilter != "" {
			keep := probs[:0]
			for _, p := range probs {
				for _, c := range p.Clusters {
					if c == clusterFilter {
						keep = append(keep, p)
						break
					}
				}
			}
			probs = keep
		}
		// rc #3 — attach the persisted root-cause top-suspect summary in
		// ONE batch read (GetHypotheses) so each problem row renders the
		// in-page ribbon without a per-row /rootcause fetch. Runs AFTER the
		// priority filter so the IN-list only covers the visible rows. Soft-
		// fails to the unenriched slice; rows with no hypothesis keep
		// RootCause=nil (honest "no clear cause yet"). Read-only — no audit.
		probs = s.store.EnrichProblemsWithRootCause(ctx, probs)
		// v0.9.455 (dürüstlük taraması A3) — çıplak dizi yerine zarf:
		// sayfa 200-cap'li diziden "N open" sayarken sidebar rozeti
		// gerçek COUNT'la çelişiyordu ve hiçbir şey kırpmayı
		// söylemiyordu. truncated aday taramasının tavanına çarptı
		// demektir; total yalnız SQL'e inen daraltmalarla (status/
		// service/severity/env) hesaplanır — takım/cluster daraltması
		// aktifken sayı yanıltır, o durumda -1 (bilinmiyor) döner ve
		// şerit sayısız konuşur (inbox chip'lerinin belgeli duruşu).
		total := int64(-1)
		if ownerTeam == "" && sreTeam == "" && clusterFilter == "" {
			if n, cerr := s.store.CountProblems(ctx, chstore.ProblemFilter{
				Status: f.Status, Service: f.Service, Severity: f.Severity, Env: f.Env,
			}); cerr == nil {
				total = int64(n)
			}
		}
		return map[string]any{
			"items":     probs,
			"total":     total,
			"truncated": scanTruncated,
		}, nil
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
	// Fetch first so we can (a) email a rich notification and (b) know the OLD
	// assignee to suppress a re-send on a no-op save (v0.8.289). A fetch miss is
	// non-fatal — the assign still proceeds, just without the notification.
	prev, _ := s.store.GetProblem(r.Context(), id)
	if err := s.store.SetProblemAssignee(r.Context(), id, body.Assignee); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"id": id, "assignee": body.Assignee})
	s.audit(r, "problem.assign", "problem", id, string(details))
	// v0.8.350 (HA 🟡9) — same cross-pod eviction as acknowledgeProblems:
	// the assignee chip must show on every replica's next /problems read.
	s.cacheInvalidatePrefix(r.Context(), "problems")
	s.cacheInvalidatePrefix(r.Context(), "inbox:count")        // v0.8.472 — rozet anında güncellensin
	s.cacheInvalidatePrefix(r.Context(), inboxListCachePrefix) // v0.9.234 — liste de
	// v0.8.289 (operator request) — when a Problem is assigned to a PERSON
	// (email assignee) and it actually changed, email them the assignment.
	if prev != nil {
		oldAssignee := prev.Assignee
		if email, send := assigneeNotifyEmail(body.Assignee, oldAssignee); send {
			p := *prev
			p.Assignee = body.Assignee
			s.notifyAssignee(email, p, s.problemLink(r, id))
		}
	}
	writeJSON(w, map[string]any{"id": id, "assignee": body.Assignee})
}

// problemLink builds the deep link into the Problems drawer for a problem id,
// from the request scheme+host (mirrors sendStatusConfirmation's URL logic).
func (s *Server) problemLink(r *http.Request, id string) string {
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
	if host == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s/problems?problem=%s", scheme, host, url.QueryEscape(id))
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
	// v0.8.350 (HA 🟡9) — cross-pod read-your-writes: evict every
	// problems-derived cache namespace (the "problems" prefix covers
	// problems: / problems-count: / problems-buckets:) on ALL replicas —
	// L2 DelPrefix + own L1 + "prefix:" pub/sub broadcast — so an ack on
	// pod A is visible through pod B's next read instead of after the
	// 5s TTL × SWR stale window (~15s of a "still open" ghost).
	s.cacheInvalidatePrefix(r.Context(), "problems")
	s.cacheInvalidatePrefix(r.Context(), "inbox:count")        // v0.8.472 — rozet anında güncellensin
	s.cacheInvalidatePrefix(r.Context(), inboxListCachePrefix) // v0.9.234 — liste de
	writeJSON(w, map[string]any{"acknowledged": n})
}

func (s *Server) listAlertRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.ListAlertRules(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rules)
}

// rejectDeadLogQuery — kayıtlı-arama alarmı, koştuğu arka ucun
// ANLAMADIĞI bir yazımla kaydedilemez (v0.9.1384).
//
// `evaluateLogQuery` kuralın metnini logstore.Filter.Search'e veriyor.
// ClickHouse — VARSAYILAN arka uç — o alanı gövdede arıyor, dolayısıyla
// `service.name:"x"` gibi bir alan yazımı YAPISAL OLARAK eşleşemez
// (canlı ölçüm: aynı servis için yapısal filtre 858 satır, bu yazım 0).
// Sonuç: alarm daima 0 sayar, eşiği asla aşmaz ve HATA DA VERMEZ —
// operatör kapsandığını sanır. Sessizce ateşlenmeyen bir alarm, hiç
// kurulmamış olandan kötüdür, çünkü yerini doldurur.
//
// Kabul edip uyarmak yerine REDDETMEK bilinçli: uyarı yanıt gövdesinde
// kalır, kural listede sağlıklı görünür ve altı ay sonra kimse bakmaz.
// Reddetme, operatörün ÖĞRENDİĞİ tek an.
//
// Arka uç çalışma zamanında değişebiliyor (Settings toggle + boot
// degrade), o yüzden karar O ANKİ arka uca göre veriliyor; ES'e geçen
// operatör aynı kuralı sorunsuz kaydeder.
func (s *Server) rejectDeadLogQuery(w http.ResponseWriter, rule chstore.AlertRule) bool {
	q := strings.TrimSpace(rule.LogQuery)
	if q == "" || s.logs == nil {
		return false
	}
	backend := s.logs.Backend()
	if !logstore.LooksLikeFieldQuery(q) || logstore.BackendUnderstandsFieldQuery(backend) {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]string{
		"error": fmt.Sprintf(
			"log query %q uses field:value syntax, which the %s log backend does not parse — "+
				"it would be searched as literal text in the log body and this rule would never fire. "+
				"Use plain text (a distinctive substring) instead, or switch the log backend to elasticsearch.",
			q, backend),
	})
	return true
}

func (s *Server) createAlertRule(w http.ResponseWriter, r *http.Request) {
	var rule chstore.AlertRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if rule.ID == "" {
		rule.ID = newID(8)
	} else if existing, err := s.store.GetAlertRule(r.Context(), rule.ID); err == nil && existing != nil {
		// v0.9.x (review F13) — POST with a caller-supplied id that
		// already exists would blind whole-row-replace the rule
		// (ReplacingMergeTree) and silently wipe watcher_json: the PUT
		// path's preservation guard never runs on create. Narrowest
		// fix that keeps id-supplying config scripts working for NEW
		// ids: creates never overwrite — update goes through PUT.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("an alert rule with id %q already exists — use PUT /api/alert-rules/%s to update it", rule.ID, rule.ID),
		})
		return
	}
	if !s.acceptRuleTarget(w, rule) {
		return
	}
	if s.rejectDeadLogQuery(w, rule) {
		return
	}
	rule.BuiltIn = false
	rule.CreatedAt = time.Now().UnixNano()
	if err := s.store.UpsertAlertRule(r.Context(), rule); err != nil {
		s.writeRuleErr(w, err)
		return
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
		http.Error(w, `{"error":"rule not found"}`, http.StatusNotFound)
		return
	}
	var rule chstore.AlertRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rule.ID = existing.ID
	rule.BuiltIn = existing.BuiltIn
	rule.CreatedAt = existing.CreatedAt
	// v0.9.x — whole-row replace carry: the UI edit payload doesn't
	// round-trip the imported ES Watcher definition; an empty value
	// here means "not editing it", never "clear it" (same preservation
	// posture as builtIn/createdAt above, and as stored secrets in
	// Settings). Watcher rules lose their evaluation source otherwise.
	if rule.WatcherJSON == "" {
		rule.WatcherJSON = existing.WatcherJSON
	}
	// Create ile AYNI kapı. Yalnız create'i kapatmak, kuralı önce boş
	// bırakıp sonra düzenleyerek geçilebilir bir kapı olurdu — ve o yol
	// bir kaçamak değil, düzenlemenin NORMAL akışıdır.
	if !s.acceptRuleTarget(w, rule) {
		return
	}
	if s.rejectDeadLogQuery(w, rule) {
		return
	}
	if err := s.store.UpsertAlertRule(r.Context(), rule); err != nil {
		s.writeRuleErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"name": rule.Name, "service": rule.Service, "metric": rule.Metric})
	s.audit(r, "alert_rule.update", "alert_rule", rule.ID, string(details))
	writeJSON(w, rule)
}

// acceptRuleTarget — v0.10.331: hedefli kural doğrulaması (400). Saf kural
// chstore.ValidateRuleTarget; hedefli kuralda Service boş (tüm çağıranlar).
func (s *Server) acceptRuleTarget(w http.ResponseWriter, rule chstore.AlertRule) bool {
	if err := chstore.ValidateRuleTarget(rule); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

// writeRuleErr — kolon henüz yoksa (ertelenmiş DDL, iki-boot) 409 + net metin.
func (s *Server) writeRuleErr(w http.ResponseWriter, err error) {
	if errors.Is(err, chstore.ErrRuleTargetColumnMissing) {
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	writeErr(w, err)
}

func (s *Server) deleteAlertRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteAlertRule(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "alert_rule.delete", "alert_rule", id, "")
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) enableAlertRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.SetAlertRuleEnabled(r.Context(), id, true); err != nil {
		writeErr(w, err)
		return
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
		writeErr(w, err)
		return
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
//   - `>`  / `>=` → high values trip (p95 → warn, p99 → crit)
//   - `<`  / `<=` → low values trip (5×p99/100 capped, 0.01 fallback)
//
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
	s.serveCached(w, r, cacheKey, 5*time.Minute, func(ctx context.Context) (any, error) {
		b, err := s.store.GetMetricBaseline(ctx, service, metric, 7*24*time.Hour)
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
//   - `>` / `>=` (alert on spike): warn at p95, crit at p99 —
//     fires when ~5% / ~1% of samples already crossed. Pure
//     percentile floors keep operators from setting "1 / 5"
//     thresholds that page nightly because the actual baseline
//     is 800ms / 8%.
//   - `<` / `<=` (alert on drop): warn at p5, crit at p1
//     approximated as max(0.01, value/2 / value/5) — fires
//     when traffic genuinely fell below the floor instead of
//     just dipping at a quiet hour.
//   - Round to a reasonable precision so the threshold input
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
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (s *Server) getDashboard(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.GetDashboard(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if d == nil {
		http.Error(w, `{"error":"dashboard not found"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, d)
}

func (s *Server) createDashboard(w http.ResponseWriter, r *http.Request) {
	var d chstore.Dashboard
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if d.Name == "" {
		http.Error(w, `{"error":"name required"}`, http.StatusBadRequest)
		return
	}
	d.ID = newID(8)
	if err := s.store.UpsertDashboard(r.Context(), d); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"name": d.Name})
	s.audit(r, "dashboard.create", "dashboard", d.ID, string(details))
	writeJSON(w, d)
}

func (s *Server) updateDashboard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.store.GetDashboard(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if existing == nil {
		http.Error(w, `{"error":"dashboard not found"}`, http.StatusNotFound)
		return
	}
	var d chstore.Dashboard
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	// v0.9.563 — gövdede GELMEYEN alanlar mevcut satırdan taşınır.
	// Öncesinde yalnız CreatedAt taşınıyordu; frontend `variables`
	// göndermediği için dashboard'un $service değişkeni İLK KAYITTA
	// kalıcı siliniyordu ve ${service} referanslı paneller sessizce
	// filo geneline geçiyordu (bkz. dashboard_merge.go).
	d.ID = id
	d = mergeDashboardUpdate(d, *existing)
	if err := s.store.UpsertDashboard(r.Context(), d); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"name": d.Name})
	s.audit(r, "dashboard.update", "dashboard", d.ID, string(details))
	writeJSON(w, d)
}

func (s *Server) deleteDashboard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteDashboard(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "dashboard.delete", "dashboard", id, "")
	writeJSON(w, map[string]string{"status": "ok"})
}

// ── SLOs ─────────────────────────────────────────────────────────────────────

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
	// v0.8.339 (HA audit H3) — readiness honesty: ALL FIVE ingest queues
	// (exemplars + span_links were invisible here) and a cached CH ping.
	// The audited failure: CH down with fast connection-refused → flushers
	// drop instantly → queues stay EMPTY → health reported "ok" while 100%
	// of telemetry was discarded. The ping (3s timeout, 5s cache) makes
	// readiness track the actual write path, so the LB stops routing and
	// the collector's own queue holds data instead of our discard path.
	spansLen, spansCap := s.ing.Spans.QueueLen(), s.ing.Spans.Capacity()
	logsLen, logsCap := s.ing.Logs.QueueLen(), s.ing.Logs.Capacity()
	metricsLen, metricsCap := s.ing.Metrics.QueueLen(), s.ing.Metrics.Capacity()
	exLen, exCap := s.ing.Exemplars.QueueLen(), s.ing.Exemplars.Capacity()
	slLen, slCap := s.ing.SpanLinks.QueueLen(), s.ing.SpanLinks.Capacity()
	overloaded := isOverloaded(spansLen, spansCap) ||
		isOverloaded(logsLen, logsCap) ||
		isOverloaded(metricsLen, metricsCap) ||
		isOverloaded(exLen, exCap) ||
		isOverloaded(slLen, slCap)
	degraded := isDegraded(spansLen, spansCap) ||
		isDegraded(logsLen, logsCap) ||
		isDegraded(metricsLen, metricsCap) ||
		isDegraded(exLen, exCap) ||
		isDegraded(slLen, slCap)
	chOK := s.chReachable(r.Context())
	// v0.9.985 — dağıtık kipte bir INSERT'in "OK" dönmesi verinin İNDİĞİ
	// anlamına gelmez: Distributed motoru diske spool'layıp hemen OK der.
	// 2026-08-12'de lokal küme 3s39d boyunca hiç span yazamazken bu
	// endpoint yemyeşildi (aşağıdaki sayaçların HEPSİ doğruydu — hepsi
	// yanlış katmanı ölçüyordu). Tek-düğümde dq nil'dir ve hiçbir sorgu
	// çalışmaz; gövde de o kurulumda bayt-bayt eskisi gibi kalır.
	dq, spoolDegraded, spoolDetail := s.distributionBacklog()
	load, code := healthVerdict(overloaded, degraded, spoolDegraded, chOK)
	body := map[string]interface{}{
		"status":                  load,
		"spans_queued":            spansLen,
		"logs_queued":             logsLen,
		"metrics_queued":          metricsLen,
		"spans_capacity":          spansCap,
		"logs_capacity":           logsCap,
		"metrics_capacity":        metricsCap,
		"spans_dropped":           s.ing.Spans.Dropped(),
		"logs_dropped":            s.ing.Logs.Dropped(),
		"metrics_dropped":         s.ing.Metrics.Dropped(),
		"sse_dropped":             s.sseDropped(), // v0.10.200 — dolu abone kanalına düşen olaylar
		"spans_write_failed":      s.ing.Spans.WriteFailed(),
		"logs_write_failed":       s.ing.Logs.WriteFailed(),
		"metrics_write_failed":    s.ing.Metrics.WriteFailed(),
		"spans_accepted":          s.ing.Spans.Accepted(),
		"logs_accepted":           s.ing.Logs.Accepted(),
		"metrics_accepted":        s.ing.Metrics.Accepted(),
		"exemplars_queued":        exLen,
		"exemplars_capacity":      exCap,
		"exemplars_dropped":       s.ing.Exemplars.Dropped(),
		"exemplars_write_failed":  s.ing.Exemplars.WriteFailed(),
		"span_links_queued":       slLen,
		"span_links_capacity":     slCap,
		"span_links_dropped":      s.ing.SpanLinks.Dropped(),
		"span_links_write_failed": s.ing.SpanLinks.WriteFailed(),
		// v0.10.293 — VM çift yazım ileti kuyruğu (self-observability kuralı:
		// yeni ingest yolu = sayaç).
		"vm_metrics_forward_enqueued": s.ing.MetricForwardEnqueued(),
		"vm_metrics_forward_dropped":  s.ing.MetricForwardDropped(),
		"vm_metrics_forward_filtered": s.ing.MetricForwardFiltered(), // v0.10.373 — drop kuralları
		"otlp_convert_degrades":       otlp.ConvertDegradeCounts(),   // v0.10.388 — sessiz degrade sayaçları
		// v0.10.300 — attribute hash indeksi: hazır mı, kaç yüklem bloom yolunda.
		"attr_index_available": chstore.AttrIndexAvailable(),
		"attr_index_used":      chstore.AttrIndexUsed(),
		"attr_slice_used":      chstore.AttrSliceUsed(), // v0.10.301 — indeks-güdümlü aday dilimi
		"clickhouse":           chStatusLabel(chOK, spoolDegraded),
		// v0.9.238 — which roles THIS pod actually runs. In distributed mode
		// the api and ingest Deployments answer the same hostname through
		// different Services, and until now nothing in the response said
		// which one you reached: a 501 from /v1/traces was the only clue,
		// and only if you happened to POST OTLP at it. Two consequences this
		// surfaces directly — the browser RUM exporter pointing at an
		// api-role pod, and "why is this endpoint slow" questions where the
		// first thing to establish is which pod answered.
		"roles": map[string]bool{
			"ingest": !s.roleIngestOff,
			"api":    !s.roleAPIOff,
		},
	}
	// Spool alanları YALNIZ dağıtık kipte eklenir. Tek-düğümde dq nil'dir
	// (sorgu hiç koşmadı) ve gövde v0.9.984 ile bayt-bayt aynı kalır —
	// orada spool diye bir kavram yok, sıfır basmak yalan olurdu.
	if dq != nil {
		body["distributed_spool_files"] = dq.Files
		body["distributed_spool_bytes"] = dq.Bytes
		body["distributed_spool_errors"] = dq.ErrorCount
		body["distributed_spool_broken_files"] = dq.BrokenFiles
		// measured=false → "ölçemedim", "temiz" DEĞİL (v0.9.984 dersi).
		body["distributed_spool_measured"] = dq.Measured
		if dq.Partial {
			// Küme geneli okuma düştü, sayılar yalnız bu düğümün —
			// yaklaşıklık itiraf edilir, "hepsi bu" diye okunmasın.
			body["distributed_spool_partial"] = true
		}
		if spoolDetail != "" {
			body["distributed_spool_detail"] = spoolDetail
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if code != http.StatusOK {
		w.WriteHeader(code)
	}
	_ = json.NewEncoder(w).Encode(body)
}

// healthVerdict maps the pressure signals + CH reachability onto the
// readiness (status, http code) pair (v0.8.339). Pure + table-tested:
// CH-unreachable is a 503 even with EMPTY queues — the fast-refuse
// outage keeps queues drained while everything is being discarded.
//
// v0.9.985 — spoolDegraded: dağıtık kipte Distributed spool birikiyor,
// yani INSERT'ler "OK" dönerken veri *_local'a İNMİYOR.
//
// İki bilinçli karar:
//
//   - KENDİ etiketi var ("clickhouse-spool-backlog"), genel "degraded"
//     değil. Bu arıza kuyruk doluluğuyla aynı şey değil ve çaresi de
//     farklı (ClickHouse tarafı), curl eden operatör bunu ilk satırda
//     görmeli.
//   - 503 DEĞİL, 200. 503 pod'u LB'den düşürür; backlog ClickHouse
//     tarafındadır, pod'u rotasyondan çıkarmak tek bayt kurtarmaz,
//     üstelik API yarısını da karartır. "degraded stays 200" (v0.8.339)
//     ile aynı gerekçe: görünür ol, tahliye etme.
//
// Sıralama: gerçek erişilemezlik > kuyruk taşması > spool > kuyruk
// baskısı. Spool, queue-degraded'ın ÜSTÜNDE çünkü %70 dolu bir tampon
// henüz veri kaybettirmiyor; inmeyen bir spool zaten kaybettiriyor.
func healthVerdict(overloaded, degraded, spoolDegraded, chOK bool) (string, int) {
	switch {
	case !chOK:
		return "clickhouse-unreachable", http.StatusServiceUnavailable
	case overloaded:
		return "overloaded", http.StatusServiceUnavailable
	case spoolDegraded:
		return "clickhouse-spool-backlog", http.StatusOK
	case degraded:
		return "degraded", http.StatusOK
	default:
		return "ok", http.StatusOK
	}
}

// chStatusLabel — gövdedeki `clickhouse` alanı. Üç hâl: erişilemez /
// spool birikiyor / iyi. Pure + tablo-testli.
//
// v0.9.985 öncesi bu alan iki hâlliydi ve TCP'ye cevap veren bir
// ClickHouse her koşulda "ok" derdi — 3.5 saat boyunca hiçbir span'in
// inmediği küme dâhil.
func chStatusLabel(chOK, spoolDegraded bool) string {
	switch {
	case !chOK:
		return "unreachable"
	case spoolDegraded:
		return "degraded"
	default:
		return "ok"
	}
}

// distQueueRefreshEvery — spool ölçümleri arasındaki süre; aynı zamanda
// TREND PENCERESİ (verdict iki ardışık örneği kıyaslar).
//
// 30 sn iki yönden de gerekçeli: (a) 5 sn'lik health nabzında ölçüm
// gürültüden ibaret olurdu — gönderici saniyede dosya alıp verir;
// 30 sn boyunca sürekli büyüyen bir kuyruk gerçektir. (b) Maliyet:
// ölçülen 646 ms'lik probe 30 sn'de bir = tek bir CH thread'inin ~%2'si,
// pod başına. Handler zaten beklemiyor.
const distQueueRefreshEvery = 30 * time.Second

// distributionBacklog — /api/health'in spool sinyali. ASLA CH beklemez:
// bayat ise arka planda tazeleme başlatır ve SON BİLİNEN değeri döner.
//
// Dönen: (son ölçüm | nil), degraded, kısa neden. nil = tek-düğüm
// kurulumu ya da henüz hiç ölçüm bitmemiş (pod'un ilk ~1 sn'si) — iki
// hâlde de sinyal YOKTUR ve health eskisi gibi davranır.
// ctx ALMAZ: hiçbir CH çağrısını isteğin ömrüne bağlamaz (bkz. refresh).
func (s *Server) distributionBacklog() (*chstore.DistributionQueue, bool, string) {
	s.distQueueMu.Lock()
	stale := time.Since(s.distQueueAt) >= distQueueRefreshEvery
	if stale && !s.distQueueRefreshing {
		s.distQueueRefreshing = true
		go s.refreshDistributionBacklog()
	}
	cur, st := s.distQueueCur, s.distQueueState
	s.distQueueMu.Unlock()
	return cur, st.Degraded, st.Detail
}

// refreshDistributionBacklog — tek uçuşlu arka plan tazeleme.
//
// İsteğin ctx'i BİLEREK kullanılmıyor: ölçüm istekten uzun yaşar,
// yoksa curl'ü Ctrl-C'leyen operatör tazelemeyi de iptal ederdi.
// Kendi bütçesi var — ölçülen tepe 1300 ms'ye karşı 5 sn, tıkanmış bir
// kümede bile probe'un kendi kendini süresiz asmasını engeller.
func (s *Server) refreshDistributionBacklog() {
	// Kopuk goroutine'de panik SÜRECİ öldürür (handler'daki gibi
	// net/http tarafından toparlanmaz) — readiness ucunun arkasında
	// böyle bir risk taşınmaz.
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[health] spool probe panic: %v", rec)
			s.distQueueMu.Lock()
			s.distQueueRefreshing = false
			s.distQueueAt = time.Now()
			s.distQueueMu.Unlock()
		}
	}()
	// 20 sn: CH tarafındaki iki bütçenin (fan-out 10 sn + yerel fallback
	// 3 sn) ÜSTÜNDE kalmalı, yoksa ctx iptali CH'nin kendi hatasının
	// önüne geçer ve fallback hiç koşamaz (v0.9.986). Kimse beklemiyor —
	// bu goroutine isteğin ömrüne bağlı değil.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sample := s.store.CollectDistributionQueue(ctx)

	s.distQueueMu.Lock()
	defer s.distQueueMu.Unlock()
	s.distQueueRefreshing = false
	s.distQueueAt = time.Now()
	// Trend tabanı yalnız GERÇEK ölçümlerle döner: düşen bir probe
	// (Measured=false) prev'i ezerse trend penceresi sessizce sıfırlanır
	// ve süregelen bir arıza "ilk ölçüm" gibi görünüp degraded'dan çıkardı.
	if sample != nil && sample.Measured && s.distQueueCur != nil && s.distQueueCur.Measured {
		s.distQueuePrev = s.distQueueCur
	}
	s.distQueueCur = sample
	// Histerezis (v0.9.987): önceki KARAR da girdi. Durumsuz verdict tek
	// bir örneğin gürültüsüyle çevriliyordu — canlı flap 44.320 → 44.318
	// (iki dosya) ile "degraded → ok" oldu, üstelik arıza sürerken.
	was := s.distQueueState.Degraded
	s.distQueueState = chstore.DistributionVerdict(s.distQueueState, s.distQueueCur, s.distQueuePrev)
	switch {
	case s.distQueueState.Degraded:
		log.Printf("[health] Distributed spool degraded: %s", s.distQueueState.Detail)
	case was:
		// Geri dönüş de loglanır: histerezisin ne zaman dolduğu, sinyalin
		// ne zaman sustuğu kadar önemli bir olay.
		log.Printf("[health] Distributed spool recovered: %s", s.distQueueState.Detail)
	}
}

// chReachable is the cached CH ping behind /api/health (v0.8.339).
// 5s cache so the 5s-period readiness probe costs at most one ping per
// window across all callers; 3s timeout so a black-holed CH can't make
// the probe itself hang past its own deadline.
func (s *Server) chReachable(ctx context.Context) bool {
	s.chPingMu.Lock()
	defer s.chPingMu.Unlock()
	if time.Since(s.chPingAt) < 5*time.Second {
		return s.chPingOK
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	s.chPingOK = s.store.Ping(pctx) == nil
	s.chPingAt = time.Now()
	return s.chPingOK
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
	b := s.buildVersion
	if b == "" {
		b = v
	}
	// v0.9.339 — build identity travels with the display version, and the
	// response says outright when they disagree.
	//
	// The env override was removed in v0.5.394 because a stale value silently
	// became "the version" and nothing anywhere could contradict it. Shipping
	// the pair means a wrong override is visible instead of authoritative:
	// whoever is debugging "why does this say 0.9.100" can see what the image
	// really is in the same payload. `version` keeps its meaning for every
	// existing caller (the login page reads exactly that field).
	writeJSON(w, map[string]any{
		"version":      v,
		"buildVersion": b,
		"overridden":   v != b,
	})
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
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		hits, err := anomaly.DetectTraceOpAnomalies(ctx, s.store, window)
		if err != nil {
			return nil, err
		}
		muted, _ := s.store.ActiveSilencedFingerprints(ctx)
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
	// v0.9.465 (dürüstlük A9) — zarf {items, activeTotal, clearedTotal,
	// truncated}: sayılar SQL'den, kırpma söylenir (anahtar v2).
	key := fmt.Sprintf("anomaly:events:v2:since=%s:limit=%d", since, limit)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		sinceNs := time.Now().Add(-since).UnixNano()
		rows, err := s.store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{
			SinceNs: sinceNs,
			Limit:   limit,
		})
		if err != nil {
			return nil, err
		}
		activeN, clearedN, cErr := s.store.CountAnomalyEventsByStatus(ctx, sinceNs, 0)
		if cErr != nil {
			// Soft-fail: sayım yoksa frontend sayfa-türevine düşer;
			// dürüstlük şeridi sayfayı asla boşaltmaz.
			activeN, clearedN = 0, 0
		}
		rows = s.store.EnrichAnomaliesWithClusters(ctx, rows, time.Hour)
		// v0.5.286 — attach the most recent deploy per service
		// in the 30 min preceding each event's startedAt so the
		// /anomalies page can answer "did this break because of a
		// deploy?" without a context switch. Uses the v0.5.283
		// effective-version chain (Helm labels, image tags) so
		// installs with no service.version still correlate.
		rows = s.store.EnrichAnomaliesWithDeploys(ctx, rows, 30*time.Minute)
		// rc #3 — attach the persisted root-cause top-suspect summary in
		// ONE batch read (GetHypotheses) so each anomaly row renders the
		// in-page ribbon without a per-row /rootcause fetch. Soft-fails to
		// the unenriched rows; rows with no hypothesis keep RootCause=nil
		// (honest "no clear cause yet" state). Stays inside serveCached —
		// the existing key already hashes since+limit; this join is
		// read-only, no new audit.
		rows = s.store.EnrichAnomaliesWithRootCause(ctx, rows)
		rows = s.store.EnrichAnomaliesWithVerdicts(ctx, rows) // v0.10.181
		return map[string]any{
			"items":        rows,
			"activeTotal":  activeN,
			"clearedTotal": clearedN,
			"truncated":    len(rows) >= limit,
		}, nil
	})
}

// getAnomalyEvent (v0.9.465, dürüstlük A9) — tek event, id ile.
// ?event= deep-link'i 200'lük liste penceresinin dışına düşünce sayfa
// sessiz hiçlik gösteriyordu; frontend bu uçtan kurtarır.
func (s *Server) getAnomalyEvent(w http.ResponseWriter, r *http.Request) {
	ev, err := s.store.GetAnomalyEvent(r.Context(), r.URL.Query().Get("id"), 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	if ev == nil {
		http.Error(w, "anomaly not found", http.StatusNotFound)
		return
	}
	writeJSON(w, ev)
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
	// v0.8.270 (operator: "log anomalies elastic backend'de çok fazla
	// sorgu yapmasın") — snap the window to a fixed rung set. The
	// window is part of the cache key, so an unbounded param let every
	// distinct ?window= value mint its own key and pay its own
	// _msearch against ES; snapping caps the key cardinality at 4,
	// which caps the endpoint's worst-case ES rate at 4 batched
	// round-trips per TTL regardless of callers.
	window := snapAnomalyWindow(parseDuration(r.URL.Query().Get("window"), 5*time.Minute))
	key := fmt.Sprintf("anomaly:log-patterns:window=%s", window)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		// v0.5.241 — pass the logstore (not chstore) so the
		// detector runs against whichever backend is wired:
		// CH path = match() + tokenbf prefilter; ES path =
		// query_string token-OR.
		hits, err := anomaly.DetectLogPatterns(ctx, s.logs, window)
		if err != nil {
			return nil, err
		}
		// Drop silenced fingerprints — operator has muted them
		// explicitly. They still get persisted into anomaly_events
		// by the recorder so history shows them with status.
		muted, _ := s.store.ActiveSilencedFingerprints(ctx)
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
	s.serveCached(w, r, "system-status", 5*time.Second, func(ctx context.Context) (any, error) {
		return s.collectStatus(ctx), nil
	})
}

type componentStatus struct {
	Name      string `json:"name"`
	Status    string `json:"status"` // operational | degraded | outage
	Message   string `json:"message,omitempty"`
	LatencyMs int64  `json:"latencyMs,omitempty"`
	// Free-form key/value extras shown on the row — version, address,
	// db name, queue depth, ingest rate, etc. Kept loose so each
	// component can surface what's relevant without bloating the type.
	Info map[string]string `json:"info,omitempty"`
	// Per-second rate (only set on ingest queue components).
	RatePerSec float64 `json:"ratePerSec,omitempty"`
}

type systemStatus struct {
	Status     string            `json:"status"` // worst of the components
	CheckedAt  string            `json:"checkedAt"`
	Components []componentStatus `json:"components"`
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
		case interface {
			Info(context.Context) (map[string]string, error)
		}:
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
		s.queueStatusWithRate("Spans ingest", s.ing.Spans),
		s.queueStatusWithRate("Logs ingest", s.ing.Logs),
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
func queueStatus(name string, q interface {
	QueueLen() int
	Capacity() int
	Dropped() int64
}) componentStatus {
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

		// Cache policy (v0.7.85). Vite emits content-hashed, immutable
		// filenames under assets/ (e.g. assets/index-BgyfWA8B.js) — a
		// new build = a new name, so they can cache for a year and never
		// be re-fetched on a reload / client-side navigation (the ~570kB
		// JS bundle was being re-downloaded every load with stdlib
		// defaults). index.html + any other root file (favicon …) MUST
		// revalidate: index.html references the hashed chunks, so caching
		// it would pin the browser to a stale deploy's asset URLs. Set
		// once here so it applies through whichever ServeFileFS branch
		// below runs.
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
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

// writeJSONError — status code + a REAL JSON {"error": …} body.
//
// v0.9.803: the handlers hand-built this body by string concatenation
// (`http.Error(w, `{"error":"`+err.Error()+`"}`, code)`), which produces
// INVALID JSON the moment the message carries a double quote. That is not a
// theoretical case: pipeline's validateCondition formats the offending
// pattern with %q ("invalid RE2 pattern %q: %w"), so EVERY bad-regex 400
// shipped a broken body. The SPA's humanize() JSON.parses the body and falls
// back to the raw string on failure, so v0.9.802's under-the-field pattern
// error rendered the raw `{"error":"invalid RE2 pattern "[": …"}` blob at the
// operator instead of the message.
//
// Marshalling through encoding/json escapes quotes, backslashes and control
// characters, so any message is safe. Header MUST be set before WriteHeader —
// that ordering is the reason this can't just be `WriteHeader` + writeJSON.
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
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
	// v0.9.825 — "bu kayıt yok" 500 DEĞİLDİR. Bir tekil okuma ucu
	// (ilk kullanıcı: GET /api/problems/{id}) eksik kimliği 500 ile
	// bildirirse istemci "sunucu bozuk" ile "kayıt gitmiş"i ayırt
	// edemez ve hata ekranı gösterir — oysa doğru cevap dürüst bir boş
	// durumdur. Loglanmaz da: eksik bir derin link operatör hatası
	// değil, normal bir istektir.
	if errors.Is(err, errNotFound) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	// v0.9.1150 — DIŞ okuma backend'inin hatası 500 DEĞİLDİR. VM açıkken
	// erişilemezse bu Coremetry arızası değil, operatörün seçtiği
	// upstream'in cevabıdır: 502 + VM'nin kendi metni ("connection
	// refused" / "401" / "unknown label"). Sessiz CH fallback'i YOK —
	// kaynak hakkında yalan söylemek yerine dürüst hata dönüyoruz.
	// 500 olarak kalsaydı coremetry-api'nin öz-gözlem error_rate'ini de
	// şişirip kendi anomali dedektörünü tetiklerdi (v0.7.13 dersi).
	if errors.Is(err, errUpstream) {
		log.Printf("[api] upstream error: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	// v0.9.1150 — "bu istek bu backend'de İFADE EDİLEMİYOR" (reddedilen
	// bir agg, MetricsQL karşılığı olmayan bir filtre operatörü) 400'dür,
	// 502 değil. Ayrım bir TEŞHİS: 502, "VictoriaMetrics'iniz bozuk"
	// der ve operatörü tamamen sağlıklı bir cluster'ı kontrol etmeye
	// yollar. Loglanmaz — istemci hatası, sunucu arızası değil.
	if errors.Is(err, errBadRequest) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	log.Printf("[api] error: %v", err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// parseFilters decodes the JSON-encoded `filters` query parameter — a list
// of {k, op, v} objects — into the typed FilterExpr slice consumed by the
// query layer.
//
// v0.10.118 — bozuk JSON ya da derlenemeyen yan tümce (bilinmeyen op, boş
// anahtar) artık HATA (errBadRequest → 400), sessizce "filtre yok" DEĞİL.
// Ölçülen olay: geçersiz op'lu bir filtre ApplyFilters'ta atlanıyor, ama
// yan tümcenin varlığı MV yolunu diskalifiye ettiği için ham GROUP BY 24h
// penceresini tarıyor (1.57 M satır, 6.2 s) ve FİLTRESİZ 51 satır dönüyordu
// — operatörün gördüğü "makul" ama yanlış bir liste. Sınırda reddetmek hem
// doğruluğu hem taramayı kapatır; UI zaten geçerli op gönderir.
func parseFilters(raw string) ([]chstore.FilterExpr, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []chstore.FilterExpr
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("%w: filters: %v", errBadRequest, err)
	}
	if err := chstore.ValidateFilters(out); err != nil {
		return nil, fmt.Errorf("%w: %v", errBadRequest, err)
	}
	return out, nil
}

// parseFiltersAndDSL merges the JSON `filters` param with a free-form `dsl`
// param. Both are optional; conditions from each are AND-joined.
// A DSL parse error is surfaced as a 4xx via the returned error.
func parseFiltersAndDSL(jsonFilters, dsl string) ([]chstore.FilterExpr, error) {
	out, err := parseFilters(jsonFilters)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(dsl) == "" {
		return out, nil
	}
	parsed, err := chstore.ParseDSL(dsl)
	if err != nil {
		return nil, err
	}
	return append(out, parsed...), nil
}

// parseFilterGroup decodes the JSON-encoded `filterGroup` query parameter —
// the grouped AND/OR builder shape (v0.8.x trace-query gap-2). Empty/missing
// → nil so callers fall back to the legacy flat `filters=` path. A malformed
// body returns nil (logged) rather than erroring: the grouped builder is an
// additive, default-off upgrade and a bad blob must never break the page —
// the flat path picks up the slack. The repo layer treats a flat-AND group
// byte-identically to []FilterExpr, so this is pure additive behaviour.
//
// v0.10.118 — parseFilters ile aynı sözleşme: bozuk JSON / derlenemeyen
// yaprak → errBadRequest (400), sessiz nil değil (buildGroupFragment'ın
// atlaması son çare olarak kalır).
func parseFilterGroup(raw string) (*chstore.FilterGroup, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var g chstore.FilterGroup
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		return nil, fmt.Errorf("%w: filterGroup: %v", errBadRequest, err)
	}
	if err := g.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", errBadRequest, err)
	}
	return &g, nil
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

// parseBoolParam reads a query-string boolean: "1" / "true" (any
// case) are true, everything else false. v0.8.406.
func parseBoolParam(s string) bool {
	return s == "1" || strings.EqualFold(s, "true")
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

// identityFromTraceIDParam — SAF (v0.10.343): ?traceId= 32-hex ise trace id
// (küçük harf); değilse kimlik değeri → (search, "") — mevcut search boşsa
// onun yerine geçer, doluysa search korunur ve traceId yine düşer.
func identityFromTraceIDParam(rawTraceID, search string) (string, string) {
	t := strings.TrimSpace(rawTraceID)
	if t == "" {
		return search, ""
	}
	if isTraceIDHex(strings.ToLower(t)) {
		return search, strings.ToLower(t)
	}
	if search == "" {
		return t, ""
	}
	return search, ""
}
