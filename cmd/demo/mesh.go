// Coremetry demo — table-driven mesh extension (v0.8.326).
//
// bank_extra.go widened the base mesh to ~45 services with hand-wired
// scenario functions. Hand-wiring stops scaling past that point: every
// new hop is ~10 lines of span plumbing and the topology only grows as
// fast as we can type. This file grows the mesh to ~75 services by
// expressing a scenario as DATA — a chainSpec is an ordered tree of
// hops (protocol, latency band, failure odds, children) and one generic
// builder walks it. Adding a service or a whole call chain is now a
// table edit, not a new function.
//
// Four new banking domains: the mobile/web channel platform, the
// fraud-ML pipeline, modern payment rails (instant + SWIFT/ISO20022),
// and the internal platform layer. Chains deliberately root at or call
// EXISTING services (api-gateway, auth-service, ledger-service,
// fraud-service, forex-service, payments-orchestrator) and consume
// topics the old mesh publishes (payment.initiated,
// customer.onboarded), so the new graph CONNECTS to the old one
// instead of floating beside it.
//
// Realism contract (docs/DEMO-REALISM.md): every hop's latency goes
// through dur(minMs,maxMs) and every failure through rollFail(failPct)
// — the shared load model — so the mesh saturates, spikes and fails in
// lockstep with the rest of the demo. Everything self-registers in
// init(); the 17 hand-wired scenarios stay untouched.
package main

import (
	"strings"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// ─── New services (30) ──────────────────────────────────────────────────────────

// meshServices — pod fleets sized 2-4 by heat, runtimes mixed via the
// bank_extra presets so the polyglot badge spread stays believable.
var meshServices = []Service{
	// Mobile/web channel platform
	svc("web-bff", rtNode, "wbff-prod-1", "wbff-prod-2", "wbff-prod-3"),
	svc("session-gateway", rtGo, "sessgw-prod-1", "sessgw-prod-2", "sessgw-prod-3"),
	svc("push-notification", rtGo, "push-prod-1", "push-prod-2"),
	svc("sms-gateway", rtGo, "smsgw-prod-1", "smsgw-prod-2"),
	svc("email-renderer", rtNode, "emailr-prod-1", "emailr-prod-2"),
	svc("device-registry", rtGo, "devreg-prod-1", "devreg-prod-2"),
	svc("preferences-service", rtNode, "prefs-prod-1", "prefs-prod-2"),
	// Fraud-ML pipeline
	svc("feature-store", rtGo, "featst-prod-1", "featst-prod-2", "featst-prod-3"),
	svc("model-serving", rtPy, "mdlsrv-prod-1", "mdlsrv-prod-2", "mdlsrv-prod-3", "mdlsrv-prod-4"),
	svc("fraud-scoring-v2", rtPy, "fscore-prod-1", "fscore-prod-2", "fscore-prod-3"),
	svc("case-manager", rtJava21, "casemgr-prod-1", "casemgr-prod-2"),
	svc("rules-engine", rtJava17, "rules-prod-1", "rules-prod-2"),
	svc("model-registry", rtPy, "mdlreg-prod-1", "mdlreg-prod-2"),
	svc("device-fingerprint", rtRust, "devfp-prod-1", "devfp-prod-2"),
	// Payment rails (instant + SWIFT/ISO20022)
	svc("fast-payment-gateway", rtGo, "fastpay-prod-1", "fastpay-prod-2", "fastpay-prod-3"),
	svc("swift-connector", rtJava17, "swiftc-prod-1", "swiftc-prod-2"),
	svc("iso20022-transformer", rtJava21, "iso-prod-1", "iso-prod-2"),
	svc("sanctions-screening-v2", rtPy, "sanc2-prod-1", "sanc2-prod-2", "sanc2-prod-3"),
	svc("liquidity-manager", rtJava21, "liqmgr-prod-1", "liqmgr-prod-2"),
	svc("clearing-adapter", rtDotnet, "clear-prod-1", "clear-prod-2"),
	svc("reconciliation-service", rtJava17, "recon-prod-1", "recon-prod-2"),
	svc("payment-status-tracker", rtGo, "paystat-prod-1", "paystat-prod-2"),
	// Internal platform
	svc("config-server-v2", rtJava21, "cfgsrv-prod-1", "cfgsrv-prod-2"),
	svc("feature-flags", rtGo, "fflags-prod-1", "fflags-prod-2", "fflags-prod-3"),
	svc("document-store", rtGo, "docst-prod-1", "docst-prod-2"),
	svc("search-indexer", rtJava17, "searchi-prod-1", "searchi-prod-2"),
	svc("pricing-engine", rtRust, "priceng-prod-1", "priceng-prod-2"),
	svc("customer-360", rtJava21, "c360-prod-1", "c360-prod-2", "c360-prod-3"),
	svc("secrets-broker", rtGo, "secbrk-prod-1", "secbrk-prod-2"),
	svc("entitlements-service", rtDotnet, "entitl-prod-1", "entitl-prod-2"),
}

// meshTeams pins {owner, SRE} per mesh service by exact name. teamsFor
// consults this FIRST because its substring buckets would misfile the
// new domains (e.g. "fraud-scoring-v2" contains "fraud" and would land
// on risk-engineering instead of the fraud-ml team).
var meshTeams = map[string][2]string{
	"web-bff":             {"channel-platform", "edge-sre"},
	"session-gateway":     {"channel-platform", "edge-sre"},
	"push-notification":   {"channel-platform", "edge-sre"},
	"sms-gateway":         {"channel-platform", "edge-sre"},
	"email-renderer":      {"channel-platform", "edge-sre"},
	"device-registry":     {"channel-platform", "edge-sre"},
	"preferences-service": {"channel-platform", "edge-sre"},

	"feature-store":      {"fraud-ml", "ml-platform-sre"},
	"model-serving":      {"fraud-ml", "ml-platform-sre"},
	"fraud-scoring-v2":   {"fraud-ml", "ml-platform-sre"},
	"case-manager":       {"fraud-ml", "ml-platform-sre"},
	"rules-engine":       {"fraud-ml", "ml-platform-sre"},
	"model-registry":     {"fraud-ml", "ml-platform-sre"},
	"device-fingerprint": {"fraud-ml", "ml-platform-sre"},

	"fast-payment-gateway":   {"payment-rails", "core-platform-sre"},
	"swift-connector":        {"payment-rails", "core-platform-sre"},
	"iso20022-transformer":   {"payment-rails", "core-platform-sre"},
	"sanctions-screening-v2": {"payment-rails", "security-sre"},
	"liquidity-manager":      {"payment-rails", "core-platform-sre"},
	"clearing-adapter":       {"payment-rails", "core-platform-sre"},
	"reconciliation-service": {"payment-rails", "core-platform-sre"},
	"payment-status-tracker": {"payment-rails", "core-platform-sre"},

	"config-server-v2":     {"core-platform", "core-platform-sre"},
	"feature-flags":        {"core-platform", "core-platform-sre"},
	"document-store":       {"core-platform", "core-platform-sre"},
	"search-indexer":       {"core-platform", "core-platform-sre"},
	"pricing-engine":       {"core-platform", "core-platform-sre"},
	"customer-360":         {"core-platform", "core-platform-sre"},
	"secrets-broker":       {"core-platform", "security-sre"},
	"entitlements-service": {"core-platform", "security-sre"},
}

// ─── Attr helpers (mirror oraDB's shape) ────────────────────────────────────────

// grpcHop builds the client-side attrs for an outbound gRPC call.
func grpcHop(method, peer string) map[string]any {
	return kv("rpc.system", "grpc", "rpc.method", method, "peer.service", peer)
}

// pgDB builds the attribute map for a PostgreSQL client span. Same
// db.* semconv shape as oraDB so the /database surface groups these
// hops by table/operation; peer.service stays "postgres" so the graph
// collapses every pg hop onto one node.
func pgDB(dbName, op, table, stmt string) map[string]any {
	return kv(
		"db.system", "postgresql",
		"db.name", dbName,
		"db.operation", op,
		"db.sql.table", table,
		"db.statement", stmt,
		"server.address", "postgres:5432",
		"server.port", 5432,
		"network.peer.address", "postgres:5432",
		"peer.service", "postgres",
	)
}

// mongoDB builds the attribute map for a MongoDB client span. The
// collection rides db.mongodb.collection (the semconv slot) AND
// db.sql.table so the shared table/operation grouping keeps working.
func mongoDB(dbName, op, coll, stmt string) map[string]any {
	return kv(
		"db.system", "mongodb",
		"db.name", dbName,
		"db.operation", op,
		"db.mongodb.collection", coll,
		"db.sql.table", coll,
		"db.statement", stmt,
		"server.address", "mongodb:27017",
		"server.port", 27017,
		"network.peer.address", "mongodb:27017",
		"peer.service", "mongodb",
	)
}

// ─── chainSpec: a scenario as data ──────────────────────────────────────────────

// chainHop is one hop of a mesh scenario. `svc` executes the hop; for
// http/grpc it is the CALLEE (the builder emits the caller's CLIENT
// span from context). failPct==0 marks a structural hop that never
// fails on its own — non-zero odds are rolled through rollFail so
// failures cluster during incidents like everywhere else in the demo.
type chainHop struct {
	svc          string     // service executing the hop
	proto        string     // http | grpc | db-oracle | db-postgres | db-mongo | redis | kafka-pub | kafka-consume | ext
	op           string     // http/ext: "METHOD /route|url"; grpc: method; db: operation; redis: command
	target       string     // ext only: the external peer.service
	dbName       string     // db-* only
	table        string     // db-*: table/collection; redis: key pattern
	stmt         string     // db-* only
	topic        string     // kafka-* only
	minMs, maxMs int        // latency band — always sampled via dur()
	failPct      int        // per-hop failure odds — rolled via rollFail()
	errMsg       string     // status message when the hop fails
	biz          string     // optional business counter recorded when reached
	par          bool       // kids fan out in parallel instead of sequentially
	kids         []chainHop // downstream calls made while serving this hop
}

// chainSpec names a weighted, driver-registered chain rooted at one hop.
type chainSpec struct {
	name   string
	weight int
	root   chainHop
}

// hopGap is the dispatch stagger between a parent span and its kids
// (and between sequential siblings) — same 2-4ms feel the hand-wired
// scenarios use.
const hopGap = 4 * ms1

// builtHop is a chainHop with its randomness resolved: duration
// sampled, failure rolled, children pruned when the hop failed.
type builtHop struct {
	h    *chainHop
	d    time.Duration
	fail bool
	kids []builtHop
}

// resolveHop samples the tree bottom-up. A failed hop drops its kids
// (downstream skip); a healthy parent stretches to envelope its
// children so the waterfall nests cleanly. kafka-pub is the exception:
// its kids are async consumer continuations that run AFTER the publish,
// so they never inflate the producer span.
func resolveHop(h *chainHop, roll func(int) bool) builtHop {
	b := builtHop{h: h, d: dur(h.minMs, h.maxMs)}
	if h.failPct > 0 && roll(h.failPct) {
		b.fail = true
		return b
	}
	var kidSpan time.Duration
	for i := range h.kids {
		kb := resolveHop(&h.kids[i], roll)
		if h.par {
			if kb.d > kidSpan {
				kidSpan = kb.d
			}
		} else {
			kidSpan += kb.d + hopGap
		}
		b.kids = append(b.kids, kb)
	}
	if h.proto != "kafka-pub" {
		if min := kidSpan + 2*hopGap; len(b.kids) > 0 && b.d < min {
			b.d = min
		}
	}
	return b
}

// splitOp splits "METHOD rest" — the http/ext op encoding.
func splitOp(op string) (method, rest string) {
	if i := strings.IndexByte(op, ' '); i > 0 {
		return op[:i], op[i+1:]
	}
	return "GET", op
}

// pascal turns "session-gateway" into "SessionGateway" for the server
// span name convention (SessionGateway.Establish) the rest of the demo
// uses.
func pascal(name string) string {
	parts := strings.Split(name, "-")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

// emitHop turns one resolved hop into span(s) under (parentSvc, parent)
// at offset off, then recurses into the kids. Span/attr shapes copy the
// hand-wired helpers' conventions so the mesh is indistinguishable from
// the old scenarios in every UI surface.
func emitHop(t *Trace, b *builtHop, parentSvc string, parent []byte, off time.Duration) {
	h := b.h
	ok := !b.fail
	msg := ifErr(b.fail, h.errMsg)
	if h.biz != "" {
		M.RecordBiz(h.biz)
	}
	// Server-span duration sits just inside the client span, floor 1ms.
	inner := b.d - 2*ms1
	if inner < ms1 {
		inner = ms1
	}

	switch h.proto {
	case "http":
		method, route := splitOp(h.op)
		status := iff(ok, 200, 500)
		M.RecordHTTP(h.svc, method, route, status, ms(b.d))
		if parent == nil {
			sid := t.Add(h.svc, h.op, tracepb.Span_SPAN_KIND_SERVER, nil, off, b.d,
				kv("http.method", method, "http.route", route, "http.status_code", status,
					"peer.service", h.svc), ok, msg)
			emitKids(t, b, sid, off+hopGap)
			return
		}
		t.Add(parentSvc, h.op, tracepb.Span_SPAN_KIND_CLIENT, parent, off, b.d,
			kv("http.method", method, "http.url", route, "peer.service", h.svc), ok, msg)
		sid := t.Add(h.svc, h.op, tracepb.Span_SPAN_KIND_SERVER, parent, off+2*ms1, inner,
			kv("http.method", method, "http.route", route, "http.status_code", status), ok, msg)
		emitKids(t, b, sid, off+2*ms1+hopGap)

	case "grpc":
		if parent == nil {
			sid := t.Add(h.svc, pascal(h.svc)+"."+h.op, tracepb.Span_SPAN_KIND_SERVER, nil, off, b.d,
				kv("rpc.system", "grpc", "rpc.method", h.op, "peer.service", h.svc), ok, msg)
			emitKids(t, b, sid, off+hopGap)
			return
		}
		t.Add(parentSvc, h.svc+"/"+h.op, tracepb.Span_SPAN_KIND_CLIENT, parent, off, b.d,
			grpcHop(h.op, h.svc), ok, msg)
		sid := t.Add(h.svc, pascal(h.svc)+"."+h.op, tracepb.Span_SPAN_KIND_SERVER, parent, off+2*ms1, inner,
			kv("rpc.system", "grpc", "rpc.method", h.op), ok, msg)
		emitKids(t, b, sid, off+2*ms1+hopGap)

	case "db-oracle":
		M.RecordDB(h.svc, "oracle", h.op, ms(b.d))
		sid := t.Add(h.svc, h.op+" "+h.table, tracepb.Span_SPAN_KIND_CLIENT, parent, off, b.d,
			oraDB(h.dbName, h.op, h.table, h.stmt, iff(h.op == "SELECT", oracleDG, oracleCore)), ok, msg)
		emitKids(t, b, sid, off+hopGap)

	case "db-postgres":
		M.RecordDB(h.svc, "postgresql", h.op, ms(b.d))
		sid := t.Add(h.svc, h.op+" "+h.table, tracepb.Span_SPAN_KIND_CLIENT, parent, off, b.d,
			pgDB(h.dbName, h.op, h.table, h.stmt), ok, msg)
		emitKids(t, b, sid, off+hopGap)

	case "db-mongo":
		M.RecordDB(h.svc, "mongodb", h.op, ms(b.d))
		sid := t.Add(h.svc, h.op+" "+h.table, tracepb.Span_SPAN_KIND_CLIENT, parent, off, b.d,
			mongoDB(h.dbName, h.op, h.table, h.stmt), ok, msg)
		emitKids(t, b, sid, off+hopGap)

	case "redis":
		sid := t.Add(h.svc, "redis."+h.op+" "+h.table, tracepb.Span_SPAN_KIND_CLIENT, parent, off, b.d,
			kv("db.system", "redis", "db.operation", h.op, "peer.service", "redis"), ok, msg)
		emitKids(t, b, sid, off+hopGap)

	case "kafka-pub":
		M.RecordBiz("kafka.events_published")
		sid := t.Add(h.svc, "kafka.publish "+h.topic, tracepb.Span_SPAN_KIND_PRODUCER, parent, off, b.d,
			kv("messaging.system", "kafka", "messaging.destination", h.topic,
				"messaging.operation", "publish", "peer.service", "kafka"), ok, msg)
		// Async continuation: consumers are independent groups, so they
		// all start together, AFTER the publish lands on the broker —
		// same producer-parented shape as scenarioTransferEvent.
		cOff := off + b.d + 3*ms1
		for i := range b.kids {
			emitHop(t, &b.kids[i], h.svc, sid, cOff)
		}

	case "kafka-consume":
		M.RecordBiz("kafka.events_consumed")
		sid := t.Add(h.svc, "kafka.consume "+h.topic, tracepb.Span_SPAN_KIND_CONSUMER, parent, off, b.d,
			kv("messaging.system", "kafka", "messaging.destination", h.topic,
				"messaging.operation", "receive", "peer.service", "kafka"), ok, msg)
		emitKids(t, b, sid, off+hopGap)

	case "ext":
		method, url := splitOp(h.op)
		status := iff(ok, 200, 502)
		sid := t.Add(h.svc, method+" "+h.target, tracepb.Span_SPAN_KIND_CLIENT, parent, off, b.d,
			ext(h.target, method, url, status), ok, msg)
		emitKids(t, b, sid, off+hopGap)
	}
}

// emitKids lays the children out under `parent`: sequentially with a
// hopGap stagger, or all at the same offset when the hop fans out.
func emitKids(t *Trace, b *builtHop, parent []byte, start time.Duration) {
	cur := start
	for i := range b.kids {
		emitHop(t, &b.kids[i], b.h.svc, parent, cur)
		if !b.h.par {
			cur += b.kids[i].d + hopGap
		}
	}
}

// buildMeshTrace is the production entry point — failures roll through
// the shared load model. buildMeshTraceRoll is the injectable seam the
// tests pin the failure/skip contract on.
func buildMeshTrace(spec *chainSpec) *Trace { return buildMeshTraceRoll(spec, rollFail) }

func buildMeshTraceRoll(spec *chainSpec, roll func(int) bool) *Trace {
	t := NewTrace()
	b := resolveHop(&spec.root, roll)
	emitHop(t, &b, "", nil, 0)
	return t
}

// ─── The chains (8) ─────────────────────────────────────────────────────────────

var meshChains = []chainSpec{
	// Web-channel dashboard: web-bff establishes a session (Redis +
	// auth-service JWT check), hydrates the customer-360 profile
	// (Mongo ∥ preferences), then evaluates rollout flags.
	{name: "MeshWebDashboard", weight: 2, root: chainHop{
		svc: "web-bff", proto: "http", op: "GET /web/dashboard", minMs: 120, maxMs: 420,
		kids: []chainHop{
			{svc: "session-gateway", proto: "grpc", op: "Establish", minMs: 15, maxMs: 60,
				failPct: 2, errMsg: "JWT signature expired for session cookie", biz: "sessions.established",
				kids: []chainHop{
					{svc: "session-gateway", proto: "redis", op: "GET", table: "sess:{sid}", minMs: 1, maxMs: 5},
					{svc: "auth-service", proto: "grpc", op: "ValidateToken", minMs: 6, maxMs: 18},
				}},
			{svc: "customer-360", proto: "grpc", op: "GetProfile", minMs: 30, maxMs: 110,
				biz: "customer360.lookups", par: true,
				kids: []chainHop{
					{svc: "customer-360", proto: "db-mongo", op: "find", dbName: "customer360",
						table: "customer_profiles", stmt: `{"find":"customer_profiles","filter":{"custId":"?"}}`,
						minMs: 4, maxMs: 22, failPct: 1,
						errMsg: "MongoTimeoutError: server selection timed out after 30000 ms"},
					{svc: "preferences-service", proto: "grpc", op: "Get", minMs: 8, maxMs: 30,
						kids: []chainHop{
							{svc: "preferences-service", proto: "db-postgres", op: "SELECT", dbName: "channeldb",
								table: "preferences", stmt: "SELECT channel_optins, locale, theme FROM preferences WHERE cust_id = $1",
								minMs: 2, maxMs: 12},
						}},
				}},
			{svc: "feature-flags", proto: "grpc", op: "Evaluate", minMs: 3, maxMs: 12, biz: "flags.evaluated",
				kids: []chainHop{
					{svc: "feature-flags", proto: "redis", op: "MGET", table: "flags:web", minMs: 1, maxMs: 4},
				}},
		},
	}},

	// Push fan-out: broker-initiated (notification.requested), resolves
	// device tokens (Postgres), then pushes APNs + SMS + rendered email
	// (template from the document store) — the sms/email limbs mirror
	// the OTP fan-out the old mesh does synchronously.
	{name: "MeshPushFanout", weight: 2, root: chainHop{
		svc: "push-notification", proto: "kafka-consume", topic: "notification.requested",
		minMs: 60, maxMs: 240, biz: "push.sent",
		kids: []chainHop{
			{svc: "device-registry", proto: "grpc", op: "ResolveTokens", minMs: 8, maxMs: 30,
				kids: []chainHop{
					{svc: "device-registry", proto: "db-postgres", op: "SELECT", dbName: "channeldb",
						table: "device_tokens", stmt: "SELECT token, platform FROM device_tokens WHERE cust_id = $1 AND revoked = false",
						minMs: 2, maxMs: 10},
				}},
			{svc: "push-notification", proto: "ext", op: "POST https://api.push.apple.com/3/device/{token}",
				target: "apns", minMs: 20, maxMs: 90, failPct: 3,
				errMsg: "APNs BadDeviceToken — push token invalid, unregistering device"},
			{svc: "sms-gateway", proto: "grpc", op: "Send", minMs: 15, maxMs: 70,
				failPct: 2, errMsg: "sms provider rate limited",
				kids: []chainHop{
					{svc: "sms-gateway", proto: "ext", op: "POST https://rest.smsprovider.example/sms/json",
						target: "sms-provider", minMs: 10, maxMs: 50},
				}},
			{svc: "email-renderer", proto: "grpc", op: "Render", minMs: 10, maxMs: 45,
				kids: []chainHop{
					{svc: "document-store", proto: "grpc", op: "GetTemplate", minMs: 5, maxMs: 20,
						kids: []chainHop{
							{svc: "document-store", proto: "db-mongo", op: "find", dbName: "docstore",
								table: "templates", stmt: `{"find":"templates","filter":{"name":"push.receipt"}}`,
								minMs: 2, maxMs: 10},
						}},
				}},
		},
	}},

	// Fraud scoring v2: consumes the payment.initiated topic the OLD
	// mesh publishes (payments-orchestrator), pulls online features
	// (Redis ∥ Postgres), fingerprints the device (Mongo), runs the
	// model (registry lookup behind it), evaluates rules against the
	// Oracle core, records the score with the EXISTING fraud-service,
	// and hands high scores to the case manager over Kafka.
	{name: "MeshFraudScoreV2", weight: 2, root: chainHop{
		svc: "fraud-scoring-v2", proto: "kafka-consume", topic: "payment.initiated",
		minMs: 150, maxMs: 500, biz: "fraud.scored",
		kids: []chainHop{
			{svc: "feature-store", proto: "grpc", op: "GetOnlineFeatures", minMs: 6, maxMs: 28, par: true,
				kids: []chainHop{
					{svc: "feature-store", proto: "redis", op: "MGET", table: "feat:{cust}", minMs: 1, maxMs: 5},
					{svc: "feature-store", proto: "db-postgres", op: "SELECT", dbName: "featuredb",
						table: "feature_snapshots", stmt: "SELECT payload FROM feature_snapshots WHERE entity_id = $1 ORDER BY ts DESC LIMIT 1",
						minMs: 2, maxMs: 14, failPct: 1,
						errMsg: "pg: deadlock detected — rolling back feature snapshot read"},
				}},
			{svc: "device-fingerprint", proto: "grpc", op: "Match", minMs: 5, maxMs: 25,
				kids: []chainHop{
					{svc: "device-fingerprint", proto: "db-mongo", op: "find", dbName: "frauddb",
						table: "device_prints", stmt: `{"find":"device_prints","filter":{"hash":"?"}}`,
						minMs: 2, maxMs: 12},
				}},
			{svc: "model-serving", proto: "grpc", op: "Predict", minMs: 25, maxMs: 140,
				failPct: 3, errMsg: "gRPC UNAVAILABLE: all subchannels in TRANSIENT_FAILURE",
				kids: []chainHop{
					{svc: "model-registry", proto: "grpc", op: "ResolveVersion", minMs: 3, maxMs: 12,
						kids: []chainHop{
							{svc: "model-registry", proto: "db-postgres", op: "SELECT", dbName: "mlopsdb",
								table: "model_versions", stmt: "SELECT version, artifact_uri FROM model_versions WHERE model = $1 AND stage = 'prod'",
								minMs: 1, maxMs: 8},
						}},
				}},
			{svc: "rules-engine", proto: "grpc", op: "Evaluate", minMs: 6, maxMs: 30,
				kids: []chainHop{
					{svc: "rules-engine", proto: "db-oracle", op: "SELECT", dbName: "COREBANK",
						table: "FRAUD_RULES", stmt: "SELECT RULE_ID, EXPR, THRESHOLD FROM FRAUD_RULES WHERE ENABLED = 'Y'",
						minMs: 2, maxMs: 14},
				}},
			{svc: "fraud-service", proto: "grpc", op: "RecordScore", minMs: 5, maxMs: 22},
			{svc: "fraud-scoring-v2", proto: "kafka-pub", topic: "fraud.scored", minMs: 8, maxMs: 25,
				kids: []chainHop{
					{svc: "case-manager", proto: "kafka-consume", topic: "fraud.scored",
						minMs: 20, maxMs: 90, biz: "fraud.cases_opened",
						kids: []chainHop{
							{svc: "case-manager", proto: "db-oracle", op: "INSERT", dbName: "COREBANK",
								table: "FRAUD_CASES", stmt: "INSERT INTO FRAUD_CASES(TXN_REF, SCORE, STATUS) VALUES(:1,:2,'OPEN')",
								minMs: 3, maxMs: 16},
						}},
				}},
		},
	}},

	// Instant payment: enters through the EXISTING api-gateway, the
	// gateway orchestrates ISO transform → sanctions v2 → liquidity
	// reservation (Oracle nostro) → EXISTING ledger posting → clearing
	// network, then fans the settled event to the status tracker and
	// reconciliation over Kafka.
	{name: "MeshFastPayment", weight: 2, root: chainHop{
		svc: "api-gateway", proto: "http", op: "POST /api/v1/payments/fast", minMs: 180, maxMs: 600,
		kids: []chainHop{
			{svc: "fast-payment-gateway", proto: "grpc", op: "Submit", minMs: 120, maxMs: 420, biz: "fast.payments",
				kids: []chainHop{
					{svc: "iso20022-transformer", proto: "grpc", op: "ToPacs008", minMs: 6, maxMs: 28,
						failPct: 2, errMsg: "ISO20022 parse error: pacs.008 GrpHdr/CreDtTm invalid timestamp"},
					{svc: "sanctions-screening-v2", proto: "grpc", op: "Screen", minMs: 10, maxMs: 45,
						failPct: 3, errMsg: "sanctions screening DECLINE: watchlist match over threshold",
						kids: []chainHop{
							{svc: "sanctions-screening-v2", proto: "db-postgres", op: "SELECT", dbName: "screeningdb",
								table: "watchlist_entries", stmt: "SELECT list_id, match_score FROM watchlist_entries WHERE normalized_name = $1",
								minMs: 2, maxMs: 14},
						}},
					{svc: "liquidity-manager", proto: "grpc", op: "Reserve", minMs: 8, maxMs: 40,
						kids: []chainHop{
							{svc: "liquidity-manager", proto: "db-oracle", op: "SELECT", dbName: "COREBANK",
								table: "NOSTRO_POSITIONS", stmt: "SELECT AVAIL_BALANCE FROM NOSTRO_POSITIONS WHERE SCHEME = 'FPS' FOR UPDATE",
								minMs: 3, maxMs: 18},
						}},
					{svc: "ledger-service", proto: "grpc", op: "PostFastPayment", minMs: 30, maxMs: 120,
						kids: []chainHop{
							{svc: "ledger-service", proto: "db-oracle", op: "BEGIN", dbName: "COREBANK",
								table: "GL_POSTINGS", stmt: "BEGIN PKG_LEDGER.POST_FAST(:1,:2,:3); END;",
								minMs: 10, maxMs: 60},
						}},
					{svc: "clearing-adapter", proto: "grpc", op: "Clear", minMs: 25, maxMs: 110,
						failPct: 2, errMsg: "circuit breaker OPEN for faster-payments-network",
						kids: []chainHop{
							{svc: "clearing-adapter", proto: "ext", op: "POST https://api.fasterpayments.example/v1/instructions",
								target: "faster-payments-network", minMs: 18, maxMs: 90},
						}},
					{svc: "fast-payment-gateway", proto: "kafka-pub", topic: "payment.fast.settled", minMs: 8, maxMs: 25,
						kids: []chainHop{
							{svc: "payment-status-tracker", proto: "kafka-consume", topic: "payment.fast.settled",
								minMs: 10, maxMs: 40,
								kids: []chainHop{
									{svc: "payment-status-tracker", proto: "db-postgres", op: "UPDATE", dbName: "paymentsdb",
										table: "payment_status", stmt: "UPDATE payment_status SET state = 'SETTLED', settled_at = now() WHERE txn_ref = $1",
										minMs: 2, maxMs: 10},
								}},
							{svc: "reconciliation-service", proto: "kafka-consume", topic: "payment.fast.settled",
								minMs: 15, maxMs: 60, biz: "recon.runs",
								kids: []chainHop{
									{svc: "reconciliation-service", proto: "db-oracle", op: "INSERT", dbName: "COREBANK",
										table: "RECON_ENTRIES", stmt: "INSERT INTO RECON_ENTRIES(TXN_REF, SCHEME, AMOUNT, SIDE) VALUES(:1,'FPS',:2,'INTERNAL')",
										minMs: 3, maxMs: 16},
								}},
						}},
				}},
		},
	}},

	// SWIFT outbound: rooted at the EXISTING payments-orchestrator,
	// routed via the modern connector (external SWIFT network + Oracle
	// outbox) with liquidity commit and a status-tracker continuation.
	{name: "MeshSwiftOutbound", weight: 1, root: chainHop{
		svc: "payments-orchestrator", proto: "grpc", op: "RouteCrossBorder", minMs: 200, maxMs: 700,
		kids: []chainHop{
			{svc: "iso20022-transformer", proto: "grpc", op: "ToMt103", minMs: 8, maxMs: 32},
			{svc: "sanctions-screening-v2", proto: "grpc", op: "Screen", minMs: 10, maxMs: 45,
				kids: []chainHop{
					{svc: "sanctions-screening-v2", proto: "db-postgres", op: "SELECT", dbName: "screeningdb",
						table: "watchlist_entries", stmt: "SELECT list_id, match_score FROM watchlist_entries WHERE normalized_name = $1",
						minMs: 2, maxMs: 14},
				}},
			{svc: "swift-connector", proto: "grpc", op: "SendMT103", minMs: 90, maxMs: 320,
				failPct: 2, errMsg: "SWIFT MT103 rejected: NAK from correspondent — field 59 IBAN failed validation",
				biz: "swift.messages_sent",
				kids: []chainHop{
					{svc: "swift-connector", proto: "ext", op: "POST https://swift.example/fin/mt103",
						target: "swift-network", minMs: 60, maxMs: 240},
					{svc: "swift-connector", proto: "db-oracle", op: "INSERT", dbName: "COREBANK",
						table: "SWIFT_OUTBOX", stmt: "INSERT INTO SWIFT_OUTBOX(TXN_REF, MSG_TYPE, PAYLOAD, STATUS) VALUES(:1,'MT103',:2,'SENT')",
						minMs: 4, maxMs: 20},
				}},
			{svc: "liquidity-manager", proto: "grpc", op: "Commit", minMs: 8, maxMs: 40,
				kids: []chainHop{
					{svc: "liquidity-manager", proto: "db-oracle", op: "UPDATE", dbName: "COREBANK",
						table: "NOSTRO_POSITIONS", stmt: "UPDATE NOSTRO_POSITIONS SET AVAIL_BALANCE = AVAIL_BALANCE - :1 WHERE CORRESPONDENT = :2",
						minMs: 3, maxMs: 18},
				}},
			{svc: "payments-orchestrator", proto: "kafka-pub", topic: "swift.sent", minMs: 8, maxMs: 25,
				kids: []chainHop{
					{svc: "payment-status-tracker", proto: "kafka-consume", topic: "swift.sent",
						minMs: 10, maxMs: 40,
						kids: []chainHop{
							{svc: "payment-status-tracker", proto: "db-postgres", op: "UPDATE", dbName: "paymentsdb",
								table: "payment_status", stmt: "UPDATE payment_status SET state = 'SENT' WHERE txn_ref = $1",
								minMs: 2, maxMs: 10},
						}},
				}},
		},
	}},

	// Config rollout: config-server-v2 refresh pulls secrets (external
	// vault), reads versions (Postgres) and broadcasts config.updated;
	// feature-flags consumes and rewrites its Redis rule cache.
	{name: "MeshConfigRollout", weight: 1, root: chainHop{
		svc: "config-server-v2", proto: "http", op: "POST /config/refresh", minMs: 80, maxMs: 300,
		kids: []chainHop{
			{svc: "secrets-broker", proto: "grpc", op: "GetSecret", minMs: 6, maxMs: 30,
				failPct: 1, errMsg: "vault token expired — re-authentication required",
				kids: []chainHop{
					{svc: "secrets-broker", proto: "ext", op: "GET https://vault.internal.example:8200/v1/kv/data/platform",
						target: "vault", minMs: 4, maxMs: 22},
				}},
			{svc: "config-server-v2", proto: "db-postgres", op: "SELECT", dbName: "platformdb",
				table: "config_versions", stmt: "SELECT app, version, payload FROM config_versions WHERE env = 'prod'",
				minMs: 2, maxMs: 14},
			{svc: "config-server-v2", proto: "kafka-pub", topic: "config.updated", minMs: 8, maxMs: 25,
				kids: []chainHop{
					{svc: "feature-flags", proto: "kafka-consume", topic: "config.updated", minMs: 5, maxMs: 25,
						failPct: 1, errMsg: "feature-flag store stale: last successful sync 14m ago, serving cached rules",
						kids: []chainHop{
							{svc: "feature-flags", proto: "redis", op: "SET", table: "flags:web", minMs: 1, maxMs: 4},
						}},
				}},
		},
	}},

	// Search indexing: consumes the customer.onboarded topic the OLD
	// mesh publishes, hydrates profile + documents (Mongo) and bulk
	// indexes into Elasticsearch.
	{name: "MeshSearchIndex", weight: 1, root: chainHop{
		svc: "search-indexer", proto: "kafka-consume", topic: "customer.onboarded",
		minMs: 60, maxMs: 260, biz: "search.docs_indexed",
		kids: []chainHop{
			{svc: "customer-360", proto: "grpc", op: "GetProfile", minMs: 30, maxMs: 110, biz: "customer360.lookups",
				kids: []chainHop{
					{svc: "customer-360", proto: "db-mongo", op: "find", dbName: "customer360",
						table: "customer_profiles", stmt: `{"find":"customer_profiles","filter":{"custId":"?"}}`,
						minMs: 4, maxMs: 22},
				}},
			{svc: "document-store", proto: "grpc", op: "FetchDocs", minMs: 10, maxMs: 45,
				kids: []chainHop{
					{svc: "document-store", proto: "db-mongo", op: "find", dbName: "docstore",
						table: "documents", stmt: `{"find":"documents","filter":{"custId":"?"},"limit":50}`,
						minMs: 3, maxMs: 18},
				}},
			{svc: "search-indexer", proto: "ext", op: "POST https://elasticsearch:9200/customers/_bulk",
				target: "elasticsearch", minMs: 8, maxMs: 60},
		},
	}},

	// Product quote: web-bff → session check → entitlements (Postgres)
	// → pricing-engine, which resolves its Redis rate cache and the
	// EXISTING forex-service in parallel.
	{name: "MeshProductQuote", weight: 2, root: chainHop{
		svc: "web-bff", proto: "http", op: "GET /web/products/quote", minMs: 100, maxMs: 380,
		kids: []chainHop{
			{svc: "session-gateway", proto: "grpc", op: "Validate", minMs: 4, maxMs: 16,
				kids: []chainHop{
					{svc: "session-gateway", proto: "redis", op: "GET", table: "sess:{sid}", minMs: 1, maxMs: 4},
				}},
			{svc: "entitlements-service", proto: "grpc", op: "Check", minMs: 5, maxMs: 22,
				failPct: 1, errMsg: "entitlement denied for product tier",
				kids: []chainHop{
					{svc: "entitlements-service", proto: "db-postgres", op: "SELECT", dbName: "platformdb",
						table: "entitlements", stmt: "SELECT product, tier FROM entitlements WHERE cust_id = $1",
						minMs: 2, maxMs: 12},
				}},
			{svc: "pricing-engine", proto: "grpc", op: "QuoteRate", minMs: 12, maxMs: 60,
				biz: "pricing.quotes", par: true,
				kids: []chainHop{
					{svc: "pricing-engine", proto: "redis", op: "GET", table: "rates:{ccy}", minMs: 1, maxMs: 4},
					{svc: "forex-service", proto: "grpc", op: "GetRates", minMs: 5, maxMs: 20},
				}},
		},
	}},
}

// ─── Self-registration ──────────────────────────────────────────────────────────

func init() {
	for _, s := range meshServices {
		services[s.Name] = s
	}
	for i := range meshChains {
		c := &meshChains[i]
		scenarios = append(scenarios, struct {
			name   string
			weight int
			fn     scenario
		}{c.name, c.weight, func() *Trace { return buildMeshTrace(c) }})
	}
}
