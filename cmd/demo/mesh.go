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
// Slice 2 grows the catalog to ~100 with three more domains — the
// batch/ETL & data platform (EOD orchestration, CDC, DWH loads,
// regulatory reporting), the open-banking/API ecosystem (TPP gateway,
// consent, quotas, webhooks, partner portal) and ops/infra-adjacent
// app services (GDPR erasure, PKI rotation, backups, chaos probes).
// Same connect-don't-float rule: the EOD batch consumes risk.eod
// (treasury-service), webhooks consume payment.settled
// (payments-orchestrator), consent checks ride auth-service, account
// reads land on the existing account-service + Oracle core, and the
// audit trail forwards into the existing audit-service.
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

// ─── New services (slice 1: 30, slice 2: 25) ────────────────────────────────────

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
	// Batch/ETL & data platform (slice 2)
	svc("eod-batch-orchestrator", rtJava17, "eodb-prod-1", "eodb-prod-2"),
	svc("statement-generator", rtJava21, "stmtgen-prod-1", "stmtgen-prod-2"),
	svc("dwh-loader", rtPy, "dwhl-prod-1", "dwhl-prod-2"),
	svc("cdc-streamer", rtGo, "cdc-prod-1", "cdc-prod-2", "cdc-prod-3"),
	svc("report-engine", rtJava21, "rpteng-prod-1", "rpteng-prod-2"),
	svc("data-quality-checker", rtPy, "dqc-prod-1", "dqc-prod-2"),
	svc("archive-service", rtGo, "arch-prod-1", "arch-prod-2"),
	svc("gl-posting-batch", rtJava17, "glpost-prod-1", "glpost-prod-2"),
	svc("regulatory-reporter", rtJava21, "regrep-prod-1", "regrep-prod-2"),
	// Open-banking / API ecosystem (slice 2)
	svc("consent-manager", rtGo, "consmgr-prod-1", "consmgr-prod-2"),
	svc("tpp-gateway", rtGo, "tppgw-prod-1", "tppgw-prod-2", "tppgw-prod-3"),
	svc("account-info-api", rtJava21, "aisapi-prod-1", "aisapi-prod-2", "aisapi-prod-3"),
	svc("payment-init-api", rtJava21, "pisapi-prod-1", "pisapi-prod-2"),
	svc("api-analytics", rtPy, "apianl-prod-1", "apianl-prod-2"),
	svc("quota-manager", rtGo, "quota-prod-1", "quota-prod-2"),
	svc("webhook-dispatcher", rtNode, "whdisp-prod-1", "whdisp-prod-2", "whdisp-prod-3"),
	svc("partner-portal-bff", rtNode, "pportal-prod-1", "pportal-prod-2"),
	// Ops / infra-adjacent app services (slice 2)
	svc("notification-orchestrator", rtNode, "notifo-prod-1", "notifo-prod-2"),
	svc("audit-trail-service", rtJava17, "audtrl-prod-1", "audtrl-prod-2"),
	svc("gdpr-eraser", rtGo, "gdpr-prod-1", "gdpr-prod-2"),
	svc("backup-coordinator", rtGo, "bkup-prod-1", "bkup-prod-2"),
	svc("key-rotation-service", rtGo, "keyrot-prod-1", "keyrot-prod-2"),
	svc("cert-manager-app", rtGo, "certmgr-prod-1", "certmgr-prod-2"),
	svc("chaos-probe", rtRust, "chaos-prod-1", "chaos-prod-2"),
	svc("capacity-planner", rtPy, "capln-prod-1", "capln-prod-2"),
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

	"eod-batch-orchestrator": {"data-platform", "data-platform-sre"},
	"statement-generator":    {"data-platform", "data-platform-sre"},
	"dwh-loader":             {"data-platform", "data-platform-sre"},
	"cdc-streamer":           {"data-platform", "data-platform-sre"},
	"report-engine":          {"data-platform", "data-platform-sre"},
	"data-quality-checker":   {"data-platform", "data-platform-sre"},
	"archive-service":        {"data-platform", "data-platform-sre"},
	"gl-posting-batch":       {"data-platform", "data-platform-sre"},
	"regulatory-reporter":    {"data-platform", "data-platform-sre"},

	"consent-manager":    {"open-banking", "open-banking-sre"},
	"tpp-gateway":        {"open-banking", "open-banking-sre"},
	"account-info-api":   {"open-banking", "open-banking-sre"},
	"payment-init-api":   {"open-banking", "open-banking-sre"},
	"api-analytics":      {"open-banking", "open-banking-sre"},
	"quota-manager":      {"open-banking", "open-banking-sre"},
	"webhook-dispatcher": {"open-banking", "open-banking-sre"},
	"partner-portal-bff": {"open-banking", "open-banking-sre"},

	"notification-orchestrator": {"platform-ops", "platform-ops-sre"},
	"audit-trail-service":       {"platform-ops", "platform-ops-sre"},
	"gdpr-eraser":               {"platform-ops", "platform-ops-sre"},
	"backup-coordinator":        {"platform-ops", "platform-ops-sre"},
	"key-rotation-service":      {"platform-ops", "platform-ops-sre"},
	"cert-manager-app":          {"platform-ops", "platform-ops-sre"},
	"chaos-probe":               {"platform-ops", "platform-ops-sre"},
	"capacity-planner":          {"platform-ops", "platform-ops-sre"},
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
		kafkaLinks.record(h.topic, t.traceID, sid)
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
		// Cross-trace link back to recent producers of this topic — the
		// batch-consume shape the "Linked traces" pivot exists for.
		t.Link(sid, kafkaLinks.maybe(h.topic, t.traceID))
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

// ─── The chains (slice 1: 8, slice 2: 8) ────────────────────────────────────────

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

	// EOD batch (slice 2, LONG): consumes the risk.eod topic the OLD
	// mesh publishes (treasury-service), seals the GL day with the
	// EXISTING ledger-service, bulk-loads the Oracle DWH schema, gates
	// the load on data quality, renders the EOD pack into the EXISTING
	// document-store, files regulatory returns with the external
	// regulator gateway, then hands off to statement generation over
	// Kafka. The whole run sits in a slow, batch-shaped latency band.
	{name: "MeshEodBatch", weight: 1, root: chainHop{
		svc: "eod-batch-orchestrator", proto: "kafka-consume", topic: "risk.eod",
		minMs: 900, maxMs: 2600, biz: "batch.runs",
		failPct: 1, errMsg: "EOD batch window overrun: cutoff 06:00 exceeded — aborting run, resuming next window",
		kids: []chainHop{
			{svc: "gl-posting-batch", proto: "grpc", op: "RunPostings", minMs: 180, maxMs: 650,
				kids: []chainHop{
					{svc: "gl-posting-batch", proto: "db-oracle", op: "BEGIN", dbName: "COREBANK",
						table: "GL_POSTINGS", stmt: "BEGIN PKG_GL.POST_EOD_BATCH(:1); END;",
						minMs: 80, maxMs: 320},
					{svc: "ledger-service", proto: "grpc", op: "SealDay", minMs: 25, maxMs: 90,
						kids: []chainHop{
							{svc: "ledger-service", proto: "db-oracle", op: "UPDATE", dbName: "COREBANK",
								table: "GL_PERIODS", stmt: "UPDATE GL_PERIODS SET STATUS = 'CLOSED' WHERE PERIOD = :1",
								minMs: 4, maxMs: 20},
						}},
				}},
			{svc: "dwh-loader", proto: "grpc", op: "LoadFacts", minMs: 200, maxMs: 800,
				failPct: 2, errMsg: "ORA-01653: unable to extend table DWH.FACT_GL_POSTINGS — partition full",
				kids: []chainHop{
					{svc: "dwh-loader", proto: "db-oracle", op: "INSERT", dbName: "DWH",
						table: "FACT_GL_POSTINGS", stmt: "INSERT /*+ APPEND */ INTO FACT_GL_POSTINGS SELECT * FROM STG_GL_POSTINGS WHERE LOAD_ID = :1",
						minMs: 120, maxMs: 520},
				}},
			{svc: "data-quality-checker", proto: "grpc", op: "ValidateLoad", minMs: 60, maxMs: 240,
				failPct: 2, errMsg: "data-quality gate failed: null ratio 4.2% on FACT_GL_POSTINGS.ACCT_ID over 1% threshold",
				kids: []chainHop{
					{svc: "data-quality-checker", proto: "db-postgres", op: "SELECT", dbName: "dataplatform",
						table: "dq_rules", stmt: "SELECT rule_id, expr, threshold FROM dq_rules WHERE dataset = $1 AND enabled",
						minMs: 2, maxMs: 12},
				}},
			{svc: "report-engine", proto: "grpc", op: "RenderEodPack", minMs: 90, maxMs: 380,
				kids: []chainHop{
					{svc: "document-store", proto: "grpc", op: "StoreReport", minMs: 8, maxMs: 30,
						kids: []chainHop{
							{svc: "document-store", proto: "db-mongo", op: "insert", dbName: "docstore",
								table: "reports", stmt: `{"insert":"reports","documents":[{"type":"eod-pack"}]}`,
								minMs: 3, maxMs: 14},
						}},
				}},
			{svc: "regulatory-reporter", proto: "grpc", op: "FileReturns", minMs: 100, maxMs: 420,
				biz: "reports.filed", failPct: 2,
				errMsg: "regulator gateway rejected COREP return: schema validation error in template C 66.01",
				kids: []chainHop{
					{svc: "regulatory-reporter", proto: "ext", op: "POST https://gateway.regulator.example/v2/returns",
						target: "regulator-gateway", minMs: 60, maxMs: 300},
				}},
			{svc: "eod-batch-orchestrator", proto: "kafka-pub", topic: "batch.eod.completed", minMs: 8, maxMs: 25,
				kids: []chainHop{
					{svc: "statement-generator", proto: "kafka-consume", topic: "batch.eod.completed",
						minMs: 150, maxMs: 600, biz: "statements.batch_generated",
						kids: []chainHop{
							{svc: "statement-generator", proto: "db-oracle", op: "SELECT", dbName: "COREBANK",
								table: "STATEMENT_RUNS", stmt: "SELECT ACCT_ID, PERIOD FROM STATEMENT_RUNS WHERE STATUS = 'DUE'",
								minMs: 20, maxMs: 90},
							{svc: "document-store", proto: "grpc", op: "StoreStatement", minMs: 10, maxMs: 40,
								kids: []chainHop{
									{svc: "document-store", proto: "db-mongo", op: "insert", dbName: "docstore",
										table: "statements", stmt: `{"insert":"statements","documents":[{"period":"?"}]}`,
										minMs: 3, maxMs: 14},
								}},
						}},
				}},
		},
	}},

	// CDC pipeline (slice 2): cdc-streamer mines the Oracle redo log,
	// publishes change events, dwh-loader consumes and MERGEs into the
	// DWH dimension, then the delta is quality-checked — the continuous
	// counterpart to the nightly EOD bulk load.
	{name: "MeshCdcStream", weight: 2, root: chainHop{
		svc: "cdc-streamer", proto: "grpc", op: "MineRedoBatch", minMs: 40, maxMs: 180, biz: "cdc.events",
		kids: []chainHop{
			{svc: "cdc-streamer", proto: "db-oracle", op: "SELECT", dbName: "COREBANK",
				table: "LOGMNR_CONTENTS", stmt: "SELECT SCN, OPERATION, SQL_REDO FROM V$LOGMNR_CONTENTS WHERE SCN > :1",
				minMs: 15, maxMs: 90, failPct: 1,
				errMsg: "ORA-01291: missing logfile — redo already aged out, CDC falling back to snapshot"},
			{svc: "cdc-streamer", proto: "kafka-pub", topic: "cdc.corebank.events", minMs: 6, maxMs: 20,
				kids: []chainHop{
					{svc: "dwh-loader", proto: "kafka-consume", topic: "cdc.corebank.events",
						minMs: 30, maxMs: 140,
						kids: []chainHop{
							{svc: "dwh-loader", proto: "db-oracle", op: "MERGE", dbName: "DWH",
								table: "DIM_CUSTOMER", stmt: "MERGE INTO DIM_CUSTOMER d USING STG_CDC s ON (d.CUST_ID = s.CUST_ID) WHEN MATCHED THEN UPDATE SET d.SEGMENT = s.SEGMENT",
								minMs: 10, maxMs: 60},
							{svc: "data-quality-checker", proto: "grpc", op: "ValidateDelta", minMs: 8, maxMs: 35,
								kids: []chainHop{
									{svc: "data-quality-checker", proto: "db-postgres", op: "SELECT", dbName: "dataplatform",
										table: "dq_rules", stmt: "SELECT rule_id, expr, threshold FROM dq_rules WHERE dataset = $1 AND enabled",
										minMs: 2, maxMs: 12},
								}},
						}},
				}},
		},
	}},

	// Open-banking AISP read (slice 2): inbound from an external TPP
	// through the new tpp-gateway — quota gate (Redis), consent check
	// (EXISTING auth-service + Postgres), account read through the
	// EXISTING account-service/Oracle core, usage event to analytics.
	{name: "MeshTppAccountInfo", weight: 2, root: chainHop{
		svc: "tpp-gateway", proto: "http", op: "GET /open-banking/v4/aisp/accounts",
		minMs: 90, maxMs: 320, biz: "tpp.requests",
		kids: []chainHop{
			{svc: "quota-manager", proto: "grpc", op: "Consume", minMs: 3, maxMs: 14,
				failPct: 2, errMsg: "TPP rate limit breach: 300 req/min quota exceeded — throttling 429",
				kids: []chainHop{
					{svc: "quota-manager", proto: "redis", op: "INCR", table: "quota:{tpp}", minMs: 1, maxMs: 4},
				}},
			{svc: "consent-manager", proto: "grpc", op: "Verify", minMs: 8, maxMs: 35,
				failPct: 2, errMsg: "consent expired for TPP — returning 403",
				kids: []chainHop{
					{svc: "auth-service", proto: "grpc", op: "ValidateToken", minMs: 5, maxMs: 18},
					{svc: "consent-manager", proto: "db-postgres", op: "SELECT", dbName: "openbankingdb",
						table: "consents", stmt: "SELECT scope, status, expires_at FROM consents WHERE consent_id = $1",
						minMs: 2, maxMs: 10},
				}},
			{svc: "account-info-api", proto: "grpc", op: "ListAccounts", minMs: 25, maxMs: 110,
				kids: []chainHop{
					{svc: "account-service", proto: "grpc", op: "ListAccounts", minMs: 15, maxMs: 60,
						kids: []chainHop{
							{svc: "account-service", proto: "db-oracle", op: "SELECT", dbName: "COREBANK",
								table: "ACCOUNTS", stmt: "SELECT ACCT_ID, IBAN, BALANCE, CCY FROM ACCOUNTS WHERE CUST_ID = :1",
								minMs: 5, maxMs: 25},
						}},
				}},
			{svc: "tpp-gateway", proto: "kafka-pub", topic: "api.usage", minMs: 4, maxMs: 12,
				kids: []chainHop{
					{svc: "api-analytics", proto: "kafka-consume", topic: "api.usage", minMs: 8, maxMs: 40,
						kids: []chainHop{
							{svc: "api-analytics", proto: "db-postgres", op: "INSERT", dbName: "openbankingdb",
								table: "api_usage_events", stmt: "INSERT INTO api_usage_events(tpp_id, endpoint, status, latency_ms) VALUES($1,$2,$3,$4)",
								minMs: 2, maxMs: 10},
						}},
				}},
		},
	}},

	// Open-banking PISP init (slice 2): the write-side sibling — grants
	// a payments consent, then initiates through payment-init-api into
	// the EXISTING payments-orchestrator, which publishes the same
	// payment.initiated topic fraud-scoring-v2 already consumes.
	{name: "MeshTppPaymentInit", weight: 1, root: chainHop{
		svc: "tpp-gateway", proto: "http", op: "POST /open-banking/v4/pisp/domestic-payments",
		minMs: 140, maxMs: 480, biz: "tpp.requests",
		kids: []chainHop{
			{svc: "quota-manager", proto: "grpc", op: "Consume", minMs: 3, maxMs: 14,
				failPct: 1, errMsg: "TPP rate limit breach: 300 req/min quota exceeded — throttling 429",
				kids: []chainHop{
					{svc: "quota-manager", proto: "redis", op: "INCR", table: "quota:{tpp}", minMs: 1, maxMs: 4},
				}},
			{svc: "consent-manager", proto: "grpc", op: "Grant", minMs: 12, maxMs: 50, biz: "consents.granted",
				kids: []chainHop{
					{svc: "auth-service", proto: "grpc", op: "ValidateToken", minMs: 5, maxMs: 18},
					{svc: "consent-manager", proto: "db-postgres", op: "INSERT", dbName: "openbankingdb",
						table: "consents", stmt: "INSERT INTO consents(consent_id, tpp_id, scope, expires_at) VALUES($1,$2,'payments:write',$3)",
						minMs: 2, maxMs: 10},
				}},
			{svc: "payment-init-api", proto: "grpc", op: "Initiate", minMs: 40, maxMs: 160,
				kids: []chainHop{
					{svc: "payments-orchestrator", proto: "grpc", op: "Initiate", minMs: 30, maxMs: 120,
						kids: []chainHop{
							{svc: "payments-orchestrator", proto: "kafka-pub", topic: "payment.initiated", minMs: 8, maxMs: 25},
						}},
				}},
		},
	}},

	// Webhook fan-out (slice 2): consumes the payment.settled topic the
	// OLD mesh publishes and delivers to three partner endpoints in
	// PARALLEL, with a concurrent Redis idempotency-key check per batch.
	{name: "MeshWebhookFanout", weight: 2, root: chainHop{
		svc: "webhook-dispatcher", proto: "kafka-consume", topic: "payment.settled",
		minMs: 40, maxMs: 160, biz: "webhooks.delivered", par: true,
		kids: []chainHop{
			{svc: "webhook-dispatcher", proto: "redis", op: "GET", table: "wh:idemp:{event}", minMs: 1, maxMs: 4},
			{svc: "webhook-dispatcher", proto: "ext", op: "POST https://webhooks.partner-one.example/v1/events",
				target: "partner-one", minMs: 15, maxMs: 90, failPct: 3,
				errMsg: "webhook rejected: HMAC signature mismatch (401) — endpoint secret out of sync"},
			{svc: "webhook-dispatcher", proto: "ext", op: "POST https://events.partner-two.example/hooks/payments",
				target: "partner-two", minMs: 15, maxMs: 90, failPct: 2,
				errMsg: "webhook delivery timeout after 10s — attempt parked for retry"},
			{svc: "webhook-dispatcher", proto: "ext", op: "POST https://api.partner-three.example/webhooks",
				target: "partner-three", minMs: 15, maxMs: 90},
		},
	}},

	// Nightly archive (slice 2): the slowest band in the demo — scans
	// aged TXN_JOURNAL partitions, applies GDPR retention, ships to the
	// same external s3 node document-service uses, prunes the journal,
	// snapshots the run and appends to the audit trail, which forwards
	// into the EXISTING audit-service.
	{name: "MeshNightlyArchive", weight: 1, root: chainHop{
		svc: "archive-service", proto: "grpc", op: "RunNightlyArchive", minMs: 2500, maxMs: 7000,
		kids: []chainHop{
			{svc: "archive-service", proto: "db-oracle", op: "SELECT", dbName: "COREBANK",
				table: "TXN_JOURNAL", stmt: "SELECT TXN_ID FROM TXN_JOURNAL WHERE POSTED_TS < ADD_MONTHS(SYSDATE, -84)",
				minMs: 400, maxMs: 1600},
			{svc: "gdpr-eraser", proto: "grpc", op: "ApplyRetention", minMs: 120, maxMs: 480,
				biz: "erasures.completed", failPct: 2,
				errMsg: "GDPR erasure conflict: active legal hold on subject — erasure deferred to compliance queue",
				kids: []chainHop{
					{svc: "gdpr-eraser", proto: "db-oracle", op: "DELETE", dbName: "COREBANK",
						table: "CUSTOMER_EVENTS", stmt: "DELETE FROM CUSTOMER_EVENTS WHERE CUST_ID = :1 AND EVENT_TS < :2",
						minMs: 40, maxMs: 220},
				}},
			{svc: "archive-service", proto: "ext", op: "PUT https://s3.eu-west-1.amazonaws.com/bank-archive-cold",
				target: "s3", minMs: 300, maxMs: 1400},
			{svc: "archive-service", proto: "db-oracle", op: "DELETE", dbName: "COREBANK",
				table: "TXN_JOURNAL", stmt: "DELETE FROM TXN_JOURNAL WHERE TXN_ID IN (SELECT TXN_ID FROM ARCHIVE_MANIFEST WHERE RUN_ID = :1)",
				minMs: 200, maxMs: 900},
			{svc: "backup-coordinator", proto: "grpc", op: "SnapshotArchive", minMs: 60, maxMs: 240,
				kids: []chainHop{
					{svc: "backup-coordinator", proto: "db-postgres", op: "INSERT", dbName: "opsdb",
						table: "backup_catalog", stmt: "INSERT INTO backup_catalog(run_id, kind, bytes, status) VALUES($1,'archive',$2,'DONE')",
						minMs: 2, maxMs: 10},
				}},
			{svc: "capacity-planner", proto: "grpc", op: "UpdateForecast", minMs: 30, maxMs: 120,
				kids: []chainHop{
					{svc: "capacity-planner", proto: "db-postgres", op: "SELECT", dbName: "opsdb",
						table: "capacity_snapshots", stmt: "SELECT resource, used_bytes, capacity_bytes FROM capacity_snapshots WHERE taken_at > now() - interval '7 days'",
						minMs: 2, maxMs: 12},
				}},
			{svc: "audit-trail-service", proto: "grpc", op: "Append", minMs: 8, maxMs: 30,
				kids: []chainHop{
					{svc: "audit-service", proto: "grpc", op: "Record", minMs: 5, maxMs: 20},
				}},
		},
	}},

	// Partner portal (slice 2): the bank-side TPP developer portal —
	// auth through the EXISTING auth-service, then usage stats, quota
	// status and recent webhook deliveries from the open-banking stores.
	{name: "MeshPartnerPortal", weight: 1, root: chainHop{
		svc: "partner-portal-bff", proto: "http", op: "GET /portal/dashboard", minMs: 80, maxMs: 300,
		kids: []chainHop{
			{svc: "auth-service", proto: "grpc", op: "ValidateToken", minMs: 5, maxMs: 18},
			{svc: "api-analytics", proto: "grpc", op: "UsageSummary", minMs: 15, maxMs: 70,
				kids: []chainHop{
					{svc: "api-analytics", proto: "db-postgres", op: "SELECT", dbName: "openbankingdb",
						table: "api_usage_events", stmt: "SELECT endpoint, count(*), avg(latency_ms) FROM api_usage_events WHERE tpp_id = $1 AND ts > now() - interval '24h' GROUP BY endpoint",
						minMs: 5, maxMs: 30},
				}},
			{svc: "quota-manager", proto: "grpc", op: "Status", minMs: 3, maxMs: 12,
				kids: []chainHop{
					{svc: "quota-manager", proto: "redis", op: "GET", table: "quota:{tpp}", minMs: 1, maxMs: 4},
				}},
			{svc: "webhook-dispatcher", proto: "grpc", op: "RecentDeliveries", minMs: 8, maxMs: 35,
				kids: []chainHop{
					{svc: "webhook-dispatcher", proto: "db-postgres", op: "SELECT", dbName: "openbankingdb",
						table: "webhook_deliveries", stmt: "SELECT event_id, endpoint, status, attempts FROM webhook_deliveries WHERE tpp_id = $1 ORDER BY ts DESC LIMIT 50",
						minMs: 3, maxMs: 15},
				}},
		},
	}},

	// Platform maintenance (slice 2): scheduled key/cert rotation —
	// new key material via the EXISTING secrets-broker/vault, ACME
	// renewal, inventory update, a post-change chaos-probe verification
	// against the EXISTING api-gateway, then a rotation notice fanned
	// out through notification-orchestrator + email-renderer.
	{name: "MeshPlatformMaintenance", weight: 1, root: chainHop{
		svc: "key-rotation-service", proto: "grpc", op: "RotateExpiring", minMs: 250, maxMs: 900,
		kids: []chainHop{
			{svc: "secrets-broker", proto: "grpc", op: "PutSecret", minMs: 8, maxMs: 35,
				kids: []chainHop{
					{svc: "secrets-broker", proto: "ext", op: "POST https://vault.internal.example:8200/v1/transit/keys/payments/rotate",
						target: "vault", minMs: 5, maxMs: 25},
				}},
			{svc: "cert-manager-app", proto: "grpc", op: "RenewExpiring", minMs: 60, maxMs: 260,
				failPct: 2, errMsg: "ACME renewal failed: http-01 challenge returned 403 for api.openbanking.example",
				kids: []chainHop{
					{svc: "cert-manager-app", proto: "ext", op: "POST https://acme-v02.ca.example/acme/new-order",
						target: "acme-ca", minMs: 40, maxMs: 200},
					{svc: "cert-manager-app", proto: "db-postgres", op: "UPDATE", dbName: "opsdb",
						table: "certificates", stmt: "UPDATE certificates SET not_after = $1, status = 'RENEWED' WHERE cn = $2",
						minMs: 2, maxMs: 10},
				}},
			{svc: "key-rotation-service", proto: "db-postgres", op: "UPDATE", dbName: "opsdb",
				table: "key_inventory", stmt: "UPDATE key_inventory SET rotated_at = now(), version = version + 1 WHERE key_id = $1",
				minMs: 2, maxMs: 10},
			{svc: "chaos-probe", proto: "grpc", op: "VerifyEndpoints", minMs: 30, maxMs: 130,
				kids: []chainHop{
					{svc: "api-gateway", proto: "http", op: "GET /api/v1/health", minMs: 3, maxMs: 15},
				}},
			{svc: "key-rotation-service", proto: "kafka-pub", topic: "platform.key.rotated", minMs: 6, maxMs: 20,
				kids: []chainHop{
					{svc: "notification-orchestrator", proto: "kafka-consume", topic: "platform.key.rotated",
						minMs: 10, maxMs: 45,
						kids: []chainHop{
							{svc: "email-renderer", proto: "grpc", op: "Render", minMs: 10, maxMs: 45},
						}},
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
