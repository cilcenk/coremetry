// Coremetry demo: realistic retail-banking traffic generator that
// emits OTLP traces, logs, and metrics over HTTP to Coremetry.
//
// The simulated domain is a retail-banking backend: balance
// inquiry, money transfer, card payment, bill pay, fraud check and
// account-statement flows. Multi-hop HTTP server/client spans chain
// the mobile/web channel → API gateway → core-banking services, and
// every persistence hop emits an Oracle DB client span (db.system =
// "oracle", Oracle-style SQL + PL/SQL, server.address like
// "corebank-scan.prod:1521"). Errors (~5-10%) cover insufficient
// funds, fraud blocks and ORA- style database faults.
//
// Usage:
//
//	go run ./cmd/demo -endpoint http://localhost:14318 -rps 2.0
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	mrand "math/rand/v2"
	"net/http"
	"strings"
	"os"
	"os/signal"
	"runtime/pprof"
	"sync"
	"syscall"
	"time"

	"google.golang.org/protobuf/proto"

	logscollpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricscollpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracecollpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

var (
	endpoint        = flag.String("endpoint", "http://localhost:14318", "OTLP HTTP endpoint (collector host port)")
	profileEndpoint = flag.String("profile-endpoint", "", "Coremetry profile ingest endpoint (defaults to -endpoint with /v1/profiles, NOT collector!)")
	rps             = flag.Float64("rps", 2.0, "Scenarios per second to generate")
)

var httpClient = &http.Client{Timeout: 5 * time.Second}

// ─── Service definitions ──────────────────────────────────────────────────────

// Each service runs as a small pod fleet so traces show realistic
// (caller_service × caller_pod) variation in the backtrace view —
// previously every service had a single host and the consumer table
// collapsed every caller into one row.
//
// Runtime fingerprint (Lang / RuntimeName / RuntimeVersion /
// RuntimeDesc) is per-service so the /services list and /service
// detail badge surface a believable polyglot mesh — Node frontend,
// Go gateway, Java order/payment, Python ML, etc. Without these
// fields the spans only carry service.name, so the runtime
// extractor finds nothing and the badge silently disappears.
type Service struct {
	Name           string
	Pods           []string
	Lang           string // telemetry.sdk.language
	RuntimeName    string // process.runtime.name
	RuntimeVersion string // process.runtime.version
	RuntimeDesc    string // process.runtime.description
}

var services = map[string]Service{
	// Edge / public-facing channels
	"mobile-bff":  {"mobile-bff", []string{"mbff-prod-1", "mbff-prod-2", "mbff-prod-3"}, "nodejs", "node", "20.11.1", "Node.js v20.11.1"},
	"api-gateway": {"api-gateway", []string{"gw-prod-1", "gw-prod-2", "gw-prod-3"}, "go", "go", "1.22.5", "go version go1.22.5 linux/amd64"},
	// Core domain services
	"auth-service":     {"auth-service", []string{"auth-prod-1", "auth-prod-2", "auth-prod-3"}, "go", "go", "1.22.5", "go version go1.22.5 linux/amd64"},
	"account-service":  {"account-service", []string{"acct-prod-1", "acct-prod-2", "acct-prod-3"}, "java", "OpenJDK Runtime Environment", "21.0.2+13", "OpenJDK 64-Bit Server VM Temurin-21.0.2+13 (build 21.0.2+13-LTS)"},
	"transfer-service": {"transfer-service", []string{"xfer-prod-1", "xfer-prod-2"}, "java", "OpenJDK Runtime Environment", "21.0.2+13", "OpenJDK 64-Bit Server VM Temurin-21.0.2+13 (build 21.0.2+13-LTS)"},
	"ledger-service":   {"ledger-service", []string{"ledger-prod-1", "ledger-prod-2"}, "java", "OpenJDK Runtime Environment", "17.0.10+7", "OpenJDK 64-Bit Server VM Temurin-17.0.10+7 (build 17.0.10+7-LTS)"},
	"card-service":     {"card-service", []string{"card-prod-1", "card-prod-2"}, "dotnet", ".NET", "8.0.4", ".NET 8.0.4"},
	"payment-service":  {"payment-service", []string{"pay-prod-1", "pay-prod-2"}, "go", "go", "1.22.5", "go version go1.22.5 linux/amd64"},
	// Supporting services — modelled so the topology fan-out reads
	// like a real core-banking mesh and the backtrace / graph views
	// have meaningful caller-callee chains to inspect.
	"fraud-service":        {"fraud-service", []string{"fraud-prod-1", "fraud-prod-2", "fraud-prod-3"}, "python", "CPython", "3.12.2", "CPython 3.12.2 (main, Feb  6 2024, 20:19:44) [GCC 12.2.0]"},
	"billpay-service":      {"billpay-service", []string{"billpay-prod-1", "billpay-prod-2"}, "go", "go", "1.22.5", "go version go1.22.5 linux/amd64"},
	"statement-service":    {"statement-service", []string{"stmt-prod-1", "stmt-prod-2"}, "java", "OpenJDK Runtime Environment", "21.0.2+13", "OpenJDK 64-Bit Server VM Temurin-21.0.2+13 (build 21.0.2+13-LTS)"},
	"customer-service":     {"customer-service", []string{"cust-prod-1", "cust-prod-2"}, "ruby", "ruby", "3.3.0", "ruby 3.3.0 (2023-12-25 revision 5124f9ac75) [x86_64-linux]"},
	"limits-service":       {"limits-service", []string{"limits-prod-1", "limits-prod-2"}, "dotnet", ".NET", "8.0.4", ".NET 8.0.4"},
	"forex-service":        {"forex-service", []string{"forex-prod-1", "forex-prod-2"}, "rust", "rust", "1.78.0", "rustc 1.78.0 (9b00956e5 2024-04-29)"},
	"notification-service": {"notification-service", []string{"notif-prod-1", "notif-prod-2"}, "nodejs", "node", "20.11.1", "Node.js v20.11.1"},
	"sms-service":          {"sms-service", []string{"sms-prod-1"}, "go", "go", "1.22.5", "go version go1.22.5 linux/amd64"},
	"email-service":        {"email-service", []string{"mail-prod-1"}, "nodejs", "node", "20.11.1", "Node.js v20.11.1"},
	"aml-service":          {"aml-service", []string{"aml-prod-1", "aml-prod-2"}, "python", "CPython", "3.12.2", "CPython 3.12.2 (main, Feb  6 2024, 20:19:44) [GCC 12.2.0]"},
	"audit-service":        {"audit-service", []string{"audit-prod-1"}, "java", "OpenJDK Runtime Environment", "17.0.10+7", "OpenJDK 64-Bit Server VM Temurin-17.0.10+7 (build 17.0.10+7-LTS)"},
	"fraud-ml-service":     {"fraud-ml-service", []string{"fraudml-prod-1", "fraudml-prod-2"}, "python", "CPython", "3.12.2", "CPython 3.12.2 (main, Feb  6 2024, 20:19:44) [GCC 12.2.0]"},
}

// User-agent pool — each trace picks one to put on the channel's
// client/server boundary so the backtrace view can group traffic by
// mobile app / web banking / ATM / partner API / health check.
var userAgents = []string{
	"RetailBankApp/4.7.2 (iOS 17.4; iPhone15,3)",
	"RetailBankApp/4.7.2 (Android 14; Pixel 8)",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/124.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_4) Safari/17.4",
	"Mozilla/5.0 (X11; Linux x86_64) Firefox/126.0",
	"ATM-NCR-SelfServ/6.3.1 (XFS 3.40)",
	"OpenBanking-AISP/1.0 (psp_id=ACME-PISP-042)",
	"kube-probe/1.29",
}

// Synthetic egress IPs for the customer-facing channel hits — looks
// like real-world geo-distributed traffic in the consumer table.
var browserIPs = []string{
	"185.42.18.91", "203.0.113.42", "198.51.100.7", "104.16.249.180",
	"172.217.18.46", "212.58.244.15", "82.165.197.208", "94.130.55.12",
	"35.190.247.13", "13.107.42.14",
}

// podIP synthesises a deterministic 10.x.x.x address from the pod
// name so every span resource consistently reports the same IP for
// the same pod across the entire run.
func podIP(pod string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(pod); i++ {
		h ^= uint32(pod[i])
		h *= 16777619
	}
	return fmt.Sprintf("10.%d.%d.%d", (h>>16)&0xff, (h>>8)&0xff, h&0xff|1)
}

// demoClusters is the synthetic multi-cluster set we spread the
// demo services across so the per-cluster breakdown panel on
// /service?name= and the cluster filter on /services have
// something to show. Three names cover the typical bank
// deployment shape (eu-west / eu-central / us-east).
var demoClusters = []string{"prod-eu-west", "prod-eu-central", "prod-us-east"}

// clusterFor deterministically maps a service name to one of the
// demoClusters via FNV-1a hash modulo the cluster count. Same
// service always lands on the same cluster, so reloads stay
// consistent. Some services (frontend, api-gateway) get pinned
// to a single cluster so the trace topology stays readable.
func clusterFor(serviceName string) string {
	switch serviceName {
	case "mobile-bff", "api-gateway", "web-bff":
		// Channel tier pinned to the EU-west "primary" cluster so
		// the trace waterfall has a stable entry point.
		return demoClusters[0]
	}
	var h uint32 = 2166136261
	for i := 0; i < len(serviceName); i++ {
		h ^= uint32(serviceName[i])
		h *= 16777619
	}
	return demoClusters[int(h)%len(demoClusters)]
}

// teamsFor maps a demo service to a plausible owner team (ug-team) + SRE team
// (sy-team) by banking domain, so the catalog surfaces a realistic spread of
// teams once Coremetry's team-derive job reads these resource attributes.
func teamsFor(name string) (owner, sre string) {
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(name, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("fraud", "risk", "scoring", "ml", "underwrit", "credit"):
		return "risk-engineering", "ml-platform-sre"
	case has("payment", "transfer", "ledger", "posting", "settlement", "sepa", "swift"):
		return "payments", "core-platform-sre"
	case has("card", "atm", "pos", "merchant"):
		return "cards-and-channels", "core-platform-sre"
	case has("auth", "identity", "openbanking", "kyc", "consent"):
		return "identity", "security-sre"
	case has("mobile", "bff", "web", "frontend", "gateway", "api"):
		return "digital-channels", "edge-sre"
	case has("notification", "email", "sms", "comms", "messaging"):
		return "customer-comms", "core-platform-sre"
	default:
		return "core-banking", "core-platform-sre"
	}
}

// ─── Trace builder ────────────────────────────────────────────────────────────

type spanInfo struct {
	service string
	span    *tracepb.Span
}

type Trace struct {
	traceID []byte
	spans   []spanInfo
	t0      time.Time
	// One pod pick per service for the duration of the trace, so
	// every span emitted by the same service in this trace agrees
	// on host.name / service.instance.id. Lazy: first reference to
	// a service materialises its pod.
	podOf map[string]string
	// Per-trace device-like client identity for the channel's
	// inbound edge (mobile-bff's customer request). All other inbound
	// edges (api-gateway → transfer-service, transfer-service →
	// ledger-service, …) derive client.address from the parent pod's IP.
	browserIP string
	userAgent string
}

func NewTrace() *Trace {
	return &Trace{
		traceID:   randID(16),
		t0:        time.Now(),
		podOf:     map[string]string{},
		browserIP: browserIPs[mrand.IntN(len(browserIPs))],
		userAgent: userAgents[mrand.IntN(len(userAgents))],
	}
}

// pickPod returns this trace's pod for `svc`, choosing one uniformly
// at random from the service's pool on first request.
func (t *Trace) pickPod(svc string) string {
	if pod, ok := t.podOf[svc]; ok {
		return pod
	}
	s, ok := services[svc]
	if !ok || len(s.Pods) == 0 {
		t.podOf[svc] = svc + "-1"
	} else {
		t.podOf[svc] = s.Pods[mrand.IntN(len(s.Pods))]
	}
	return t.podOf[svc]
}

// rollPodGeneration gives THIS process's pods a fresh generation
// suffix, so every (re)deploy of the demo rolls the entire pod set —
// a deploy IS a pod rollout. Coremetry's pod-churn detector then
// marks the restart moment on every service's charts. Between deploys
// the pods are stable, so there are no false rollouts. Added so the
// rollout feature has REAL churn to detect in the synthetic env,
// where service.version stays constant (which is exactly why the old
// version-based deploy markers were noise). v0.8.x.
func rollPodGeneration() {
	gen := fmt.Sprintf("r%d", 1000+mrand.IntN(9000)) // distinct per process start
	for name, s := range services {
		if len(s.Pods) == 0 {
			continue
		}
		np := make([]string, len(s.Pods))
		for i, p := range s.Pods {
			np[i] = p + "-" + gen
		}
		s.Pods = np
		services[name] = s
	}
	log.Printf("  pod generation:   %s (each redeploy rolls all pods → a rollout marker)", gen)
}

// Add inserts a span and returns its ID for use as a parent.
// startOffset/duration are relative to the trace start (t0).
func (t *Trace) Add(service, name string, kind tracepb.Span_SpanKind,
	parent []byte, startOffset, duration time.Duration,
	attrs map[string]any, statusOK bool, errMsg string) []byte {

	spanID := randID(8)
	start := t.t0.Add(startOffset)
	sp := &tracepb.Span{
		TraceId:           t.traceID,
		SpanId:            spanID,
		Name:              name,
		Kind:              kind,
		StartTimeUnixNano: uint64(start.UnixNano()),
		EndTimeUnixNano:   uint64(start.Add(duration).UnixNano()),
		Attributes:        mapToKVs(attrs),
	}
	if parent != nil {
		sp.ParentSpanId = parent
	}
	if statusOK {
		sp.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK}
	} else {
		sp.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: errMsg}
	}
	t.spans = append(t.spans, spanInfo{service, sp})
	return spanID
}

func (t *Trace) Send() error {
	// ~15% of traces get their root span dropped before sending —
	// simulates a real-world OTel sampler / collector dropping the
	// entry span (or the SDK initialising mid-request). The orphan
	// children still go out with their original parent_id pointing
	// at the now-missing root, so the trace appears in storage as a
	// 'root not available' fragment. Higher than realistic on
	// purpose — lets us see the 'Root traces' filter actually
	// excluding these in seconds rather than minutes.
	dropRoot := mrand.IntN(100) < 15
	byService := make(map[string][]*tracepb.Span)
	for _, si := range t.spans {
		if dropRoot && len(si.span.ParentSpanId) == 0 {
			continue // skip the trace's true root
		}
		byService[si.service] = append(byService[si.service], si.span)
	}

	// Walk parent edges and decorate inbound (cross-service) server
	// spans with client.address + user_agent.original. The mobile/web
	// channel's inbound edge gets the synthetic device IP / UA picked
	// at trace creation; every other inbound edge gets the parent
	// pod's deterministic IP and the same UA so the request signature
	// flows through the call chain.
	spanByID := map[string]spanInfo{}
	for _, si := range t.spans {
		spanByID[string(si.span.SpanId)] = si
	}
	for _, si := range t.spans {
		// Only enrich the receiving end of a cross-service edge.
		if si.span.Kind != tracepb.Span_SPAN_KIND_SERVER || len(si.span.ParentSpanId) == 0 {
			continue
		}
		parent, ok := spanByID[string(si.span.ParentSpanId)]
		if !ok || parent.service == si.service {
			continue
		}
		var clientAddr string
		if parent.service == "mobile-bff" {
			clientAddr = t.browserIP
		} else {
			clientAddr = podIP(t.pickPod(parent.service))
		}
		si.span.Attributes = append(si.span.Attributes,
			kvStr("client.address", clientAddr),
			kvStr("user_agent.original", t.userAgent),
		)
	}

	var rs []*tracepb.ResourceSpans
	for svcKey, spans := range byService {
		s, ok := services[svcKey]
		if !ok {
			s = Service{Name: svcKey, Pods: []string{svcKey + "-1"}}
		}
		pod := t.pickPod(svcKey)
		// Resource attributes — base set + per-service runtime
		// fingerprint so the /services list and detail badge
		// surface a believable polyglot mesh ("Go 1.22 / Java 21
		// / Python 3.12 / Node 20 / .NET 8 / Rust 1.78 / Ruby
		// 3.3"). Values default to empty when the service entry
		// doesn't carry them so a future service without
		// runtime metadata silently drops the badge instead of
		// rendering "Unknown".
		attrs := []*commonpb.KeyValue{
			kvStr("service.name", s.Name),
			kvStr("host.name", pod),
			kvStr("service.instance.id", pod),
			kvStr("k8s.pod.name", pod),
			kvStr("k8s.pod.ip", podIP(pod)),
			kvStr("k8s.namespace.name", "demo"),
			kvStr("k8s.cluster.name", clusterFor(s.Name)),
			kvStr("deployment.environment", "demo"),
			kvStr("service.version", "1.0.0"),
		}
		// ug-team (owner) + sy-team (SRE) as RESOURCE attrs (v0.8.97) so
		// Coremetry's team-derive job auto-populates the service catalog.
		ugTeam, syTeam := teamsFor(s.Name)
		attrs = append(attrs, kvStr("ug-team", ugTeam), kvStr("sy-team", syTeam))
		if s.Lang != "" {
			attrs = append(attrs,
				kvStr("telemetry.sdk.language", s.Lang),
				kvStr("telemetry.sdk.name", "opentelemetry"),
				kvStr("telemetry.sdk.version", "1.30.0"),
			)
		}
		if s.RuntimeName != "" {
			attrs = append(attrs, kvStr("process.runtime.name", s.RuntimeName))
		}
		if s.RuntimeVersion != "" {
			attrs = append(attrs, kvStr("process.runtime.version", s.RuntimeVersion))
		}
		if s.RuntimeDesc != "" {
			attrs = append(attrs, kvStr("process.runtime.description", s.RuntimeDesc))
		}
		rs = append(rs, &tracepb.ResourceSpans{
			Resource: &resourcepb.Resource{Attributes: attrs},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "coremetry-demo"},
				Spans: spans,
			}},
		})
	}
	return sendOTLP("/v1/traces", &tracecollpb.ExportTraceServiceRequest{ResourceSpans: rs})
}

// ─── Banking domain helpers ────────────────────────────────────────────────────

// Oracle core-banking endpoints. corebank-scan.prod is the RAC SCAN
// listener (shared across the read/write core), corebank-dg.prod is
// the Data Guard standby that read-only inquiries route to. Both
// listen on the conventional Oracle 1521 port.
const (
	oracleCore = "corebank-scan.prod:1521"
	oracleDG   = "corebank-dg.prod:1521"
)

// oraDB builds the attribute map for an Oracle DB client span. system
// + name + statement + operation + sql.table + server.address follow
// the OTel db.* semantic conventions so the /database surface groups
// by table / operation correctly. peer.service stays "oracle" so the
// topology graph collapses every core-banking DB hop onto one node.
func oraDB(dbName, op, table, stmt, server string) map[string]any {
	return kv(
		"db.system", "oracle",
		"db.name", dbName,
		"db.operation", op,
		"db.sql.table", table,
		"db.statement", stmt,
		"server.address", server,
		"server.port", 1521,
		"network.peer.address", server,
		"peer.service", "oracle",
	)
}

// acctID / cardMasked / txnRef synthesise believable banking
// identifiers for span attributes and log context.
func acctID() string { return fmt.Sprintf("ACCT-%09d", mrand.IntN(900_000_000)+100_000_000) }
func custID() string { return fmt.Sprintf("CUST-%07d", mrand.IntN(9_000_000)+1_000_000) }
func txnRef() string { return fmt.Sprintf("TXN-%d-%06d", time.Now().Year(), mrand.IntN(999_999)+1) }
func cardMasked() string {
	return fmt.Sprintf("4%03d-****-****-%04d", mrand.IntN(1000), mrand.IntN(10000))
}

// amount returns a plausible money amount in the given range, rounded
// to cents.
func amount(minMaj, maxMaj int) float64 {
	cents := mrand.IntN((maxMaj-minMaj)*100) + minMaj*100
	return float64(cents) / 100.0
}

// ccy picks a settlement currency. EUR-heavy because the primary
// cluster is eu-west, with a long tail of FX-able currencies that
// route through forex-service.
func ccy() string { return pick("EUR", "EUR", "EUR", "GBP", "USD", "CHF", "PLN") }

// ─── Scenarios ────────────────────────────────────────────────────────────────

type scenario func() *Trace

// scenarioBalanceInquiry: GET /accounts/{id}/balance → account-service
// → Oracle (read-only, routes to the Data Guard standby). The hot
// path of retail banking — highest weight in the driver.
func scenarioBalanceInquiry() *Trace {
	t := NewTrace()
	totalDur := dur(40, 140)
	acct := acctID()
	M.RecordHTTP("api-gateway", "GET", "/api/v1/accounts/{id}/balance", 200, ms(totalDur))
	M.RecordDB("account-service", "oracle", "SELECT", ms(totalDur)*0.6)
	M.RecordBiz("balance.inquiries")

	feSpan := t.Add("mobile-bff", "GET /accounts/balance", tracepb.Span_SPAN_KIND_CLIENT,
		nil, 0, totalDur, kv("http.method", "GET", "http.url", "/accounts/balance",
			"peer.service", "api-gateway"), true, "")

	apiDur := totalDur - 8*time.Millisecond
	apiSpan := t.Add("api-gateway", "GET /api/v1/accounts/{id}/balance", tracepb.Span_SPAN_KIND_SERVER,
		feSpan, 4*time.Millisecond, apiDur, kv("http.method", "GET",
			"http.route", "/api/v1/accounts/{id}/balance", "http.status_code", 200,
			"banking.account_id", acct), true, "")

	acctDur := apiDur - 14*time.Millisecond
	acctSpan := t.Add("account-service", "AccountService.GetBalance", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, 8*time.Millisecond, acctDur, kv("rpc.system", "grpc", "rpc.method", "GetBalance",
			"peer.service", "account-service", "banking.account_id", acct), true, "")

	t.Add("account-service", "SELECT ACCOUNTS", tracepb.Span_SPAN_KIND_CLIENT,
		acctSpan, 14*time.Millisecond, acctDur-22*time.Millisecond,
		oraDB("COREBANK", "SELECT", "ACCOUNTS",
			"SELECT BALANCE, AVAIL_BALANCE, CCY, STATUS FROM ACCOUNTS WHERE ACCT_ID = :1",
			oracleDG), true, "")
	return t
}

// scenarioLogin: POST /auth/login → api → auth-service → Oracle.
// Verifies credentials + steps up to OTP on a new device.
func scenarioLogin() *Trace {
	t := NewTrace()
	totalDur := dur(80, 220)
	authFail := rollFail(8) // ~8% bad credential, more during incidents
	status := iff(authFail, 401, 200)
	cust := custID()
	M.RecordHTTP("api-gateway", "POST", "/api/v1/auth/login", status, ms(totalDur))
	M.RecordDB("auth-service", "oracle", "SELECT", ms(totalDur)*0.5)
	if authFail {
		M.RecordBiz("auth.login_failed")
	} else {
		M.RecordBiz("auth.logged_in")
	}

	feSpan := t.Add("mobile-bff", "POST /auth/login", tracepb.Span_SPAN_KIND_CLIENT,
		nil, 0, totalDur, kv("http.method", "POST", "http.url", "/auth/login",
			"peer.service", "api-gateway"), !authFail, ifErr(authFail, "401 Unauthorized"))

	statusCode := 200
	if authFail {
		statusCode = 401
	}
	apiSpan := t.Add("api-gateway", "POST /api/v1/auth/login", tracepb.Span_SPAN_KIND_SERVER,
		feSpan, 3*time.Millisecond, totalDur-6*time.Millisecond,
		kv("http.method", "POST", "http.route", "/api/v1/auth/login", "http.status_code", statusCode,
			"banking.customer_id", cust),
		!authFail, ifErr(authFail, "invalid credentials"))

	authDur := totalDur - 20*time.Millisecond
	authSpan := t.Add("auth-service", "AuthService.Authenticate", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, 8*time.Millisecond, authDur, kv("rpc.system", "grpc", "rpc.method", "Authenticate",
			"peer.service", "auth-service", "banking.customer_id", cust),
		!authFail, ifErr(authFail, "credential mismatch for customer"))

	t.Add("auth-service", "SELECT CUSTOMER_AUTH", tracepb.Span_SPAN_KIND_CLIENT,
		authSpan, 15*time.Millisecond, authDur-25*time.Millisecond,
		oraDB("COREBANK", "SELECT", "CUSTOMER_AUTH",
			"SELECT CUST_ID, PWD_HASH, MFA_ENABLED, STATUS FROM CUSTOMER_AUTH WHERE LOGIN_ID = :1",
			oracleDG), true, "")

	// New-device step-up: auth-service issues an OTP via the
	// notification fan-out (sms + email). notification-service has
	// two upstream entry points (kafka consumer for async events +
	// rpc from auth-service for live OTP) — exercises fan-in across
	// transport types in the consumer view.
	if !authFail && mrand.IntN(100) < 35 {
		notifDur := dur(20, 60)
		t.Add("auth-service", "notification-service/SendOTP", tracepb.Span_SPAN_KIND_CLIENT,
			authSpan, 30*time.Millisecond, notifDur,
			kv("rpc.system", "grpc", "rpc.method", "SendOTP",
				"peer.service", "notification-service"), true, "")
		notifSpan := t.Add("notification-service", "NotificationService.SendOTP", tracepb.Span_SPAN_KIND_SERVER,
			authSpan, 32*time.Millisecond, notifDur-4*time.Millisecond,
			kv("rpc.system", "grpc", "rpc.method", "SendOTP", "notification.channel", "sms"), true, "")
		t.Add("notification-service", "sms-service/Send", tracepb.Span_SPAN_KIND_CLIENT,
			notifSpan, 34*time.Millisecond, dur(15, 45),
			kv("rpc.system", "grpc", "rpc.method", "Send",
				"peer.service", "sms-service"), true, "")
	}
	return t
}

// scenarioTransfer: the flagship multi-hop banking flow.
// POST /transfers → api-gateway → transfer-service, which:
//  1. resolves both accounts (account-service → Oracle)
//  2. checks the daily transfer limit (limits-service → Oracle)
//  3. makes a CLIENT span to fraud-service for a real-time score
//     (fraud-service → fraud-ml-service model inference)
//  4. posts the double-entry to the ledger via a PL/SQL package
//     call (ledger-service → Oracle PKG_LEDGER.POST_TRANSFER)
//  5. fires a confirmation notification on success.
//
// ~5% of transfers fail: insufficient funds or a fraud block, each
// setting span status=Error with a descriptive message.
func scenarioTransfer() *Trace {
	t := NewTrace()
	totalDur := dur(350, 700)
	srcAcct, dstAcct := acctID(), acctID()
	ref := txnRef()
	amt := amount(10, 5000)
	currency := ccy()

	// Failure mode: ~3% insufficient funds, ~2% fraud blocked — both rise
	// during an incident via the load model's error bump.
	insufficient := rollFail(3)
	fraudBlocked := !insufficient && rollFail(2)
	failed := insufficient || fraudBlocked
	httpStatus := iff(failed, 422, 201)

	M.RecordHTTP("api-gateway", "POST", "/api/v1/transfers", httpStatus, ms(totalDur))
	M.RecordDB("ledger-service", "oracle", "BEGIN", ms(totalDur)*0.3)
	M.RecordBiz("transfers.attempted")
	if failed {
		M.RecordBiz("transfers.failed")
		if fraudBlocked {
			M.RecordBiz("fraud.blocked")
		}
	} else {
		M.RecordBiz("transfers.completed")
	}

	feSpan := t.Add("mobile-bff", "POST /transfers", tracepb.Span_SPAN_KIND_CLIENT,
		nil, 0, totalDur, kv("http.method", "POST", "http.url", "/transfers",
			"banking.txn_ref", ref, "banking.amount", amt, "banking.currency", currency,
			"peer.service", "api-gateway"),
		!failed, ifErr(failed, iff(insufficient, "insufficient funds", "transfer blocked by fraud")))

	apiDur := totalDur - 10*time.Millisecond
	apiSpan := t.Add("api-gateway", "POST /api/v1/transfers", tracepb.Span_SPAN_KIND_SERVER,
		feSpan, 5*time.Millisecond, apiDur, kv("http.method", "POST", "http.route", "/api/v1/transfers",
			"http.status_code", httpStatus, "banking.txn_ref", ref),
		!failed, ifErr(failed, iff(insufficient, "422 Unprocessable Entity: insufficient funds",
			"422 Unprocessable Entity: fraud block")))

	// Token validation at the edge.
	t.Add("api-gateway", "auth-service/ValidateToken", tracepb.Span_SPAN_KIND_CLIENT,
		apiSpan, 8*time.Millisecond, dur(6, 16), kv("rpc.system", "grpc", "rpc.method", "ValidateToken",
			"peer.service", "auth-service"), true, "")

	// Transfer orchestration begins.
	xferStart := 24 * time.Millisecond
	xferDur := apiDur - 30*time.Millisecond
	xferSpan := t.Add("transfer-service", "TransferService.Execute", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, xferStart, xferDur, kv("rpc.system", "grpc", "rpc.method", "Execute",
			"peer.service", "transfer-service", "banking.txn_ref", ref,
			"banking.source_account", srcAcct, "banking.dest_account", dstAcct,
			"banking.amount", amt, "banking.currency", currency),
		!failed, ifErr(failed, iff(insufficient, "debit rejected: insufficient available balance",
			"debit rejected: fraud decision DECLINE")))

	// 1. Resolve source account + balance (read-only → standby).
	t.Add("transfer-service", "account-service/GetAccount", tracepb.Span_SPAN_KIND_CLIENT,
		xferSpan, xferStart+3*time.Millisecond, dur(15, 40),
		kv("rpc.system", "grpc", "rpc.method", "GetAccount",
			"peer.service", "account-service"), true, "")
	acctSpan := t.Add("account-service", "AccountService.GetAccount", tracepb.Span_SPAN_KIND_SERVER,
		xferSpan, xferStart+4*time.Millisecond, dur(12, 36),
		kv("rpc.system", "grpc", "rpc.method", "GetAccount",
			"banking.account_id", srcAcct), true, "")
	insMsg := ""
	if insufficient {
		insMsg = "balance check: available balance below transfer amount"
	}
	t.Add("account-service", "SELECT ACCOUNTS", tracepb.Span_SPAN_KIND_CLIENT,
		acctSpan, xferStart+6*time.Millisecond, dur(8, 28),
		oraDB("COREBANK", "SELECT", "ACCOUNTS",
			"SELECT BALANCE, STATUS FROM ACCOUNTS WHERE ACCT_ID = :1",
			oracleCore), !insufficient, insMsg)

	// 2. Daily-limit guard.
	limStart := xferStart + 30*time.Millisecond
	t.Add("transfer-service", "limits-service/CheckDaily", tracepb.Span_SPAN_KIND_CLIENT,
		xferSpan, limStart, dur(6, 18), kv("rpc.system", "grpc", "rpc.method", "CheckDaily",
			"peer.service", "limits-service"), true, "")
	limSpan := t.Add("limits-service", "LimitsService.CheckDaily", tracepb.Span_SPAN_KIND_SERVER,
		xferSpan, limStart+1*time.Millisecond, dur(4, 14),
		kv("rpc.system", "grpc", "rpc.method", "CheckDaily",
			"banking.account_id", srcAcct, "banking.amount", amt), true, "")
	t.Add("limits-service", "SELECT TRANSFER_LIMITS", tracepb.Span_SPAN_KIND_CLIENT,
		limSpan, limStart+2*time.Millisecond, dur(2, 8),
		oraDB("COREBANK", "SELECT", "TRANSFER_LIMITS",
			"SELECT DAILY_LIMIT, USED_TODAY FROM TRANSFER_LIMITS WHERE ACCT_ID = :1",
			oracleDG), true, "")

	// 3. Real-time fraud score — the CLIENT span the task calls out.
	fraudStart := xferStart + 56*time.Millisecond
	fraudOK := !fraudBlocked
	fraudScore := mrand.Float64() * 0.4
	if fraudBlocked {
		fraudScore = 0.85 + mrand.Float64()*0.15
	}
	t.Add("transfer-service", "fraud-service/ScoreTransfer", tracepb.Span_SPAN_KIND_CLIENT,
		xferSpan, fraudStart, dur(30, 90), kv("rpc.system", "grpc", "rpc.method", "ScoreTransfer",
			"peer.service", "fraud-service", "banking.txn_ref", ref),
		fraudOK, ifErr(fraudBlocked, "fraud decision: DECLINE"))
	fraudSvcSpan := t.Add("fraud-service", "FraudService.ScoreTransfer", tracepb.Span_SPAN_KIND_SERVER,
		xferSpan, fraudStart+2*time.Millisecond, dur(26, 84),
		kv("rpc.system", "grpc", "rpc.method", "ScoreTransfer",
			"fraud.score", fraudScore, "fraud.decision", iff(fraudBlocked, "DECLINE", "APPROVE"),
			"banking.txn_ref", ref),
		fraudOK, ifErr(fraudBlocked, "rule HIGH_VELOCITY + model score 0.9 over threshold"))
	// Model inference hop — fraud-service → fraud-ml-service.
	t.Add("fraud-service", "fraud-ml-service/Predict", tracepb.Span_SPAN_KIND_CLIENT,
		fraudSvcSpan, fraudStart+4*time.Millisecond, dur(15, 50),
		kv("rpc.system", "grpc", "rpc.method", "Predict",
			"peer.service", "fraud-ml-service"), true, "")
	mlSpan := t.Add("fraud-ml-service", "FraudMLService.Predict", tracepb.Span_SPAN_KIND_SERVER,
		fraudSvcSpan, fraudStart+6*time.Millisecond, dur(12, 44),
		kv("rpc.system", "grpc", "rpc.method", "Predict",
			"ml.model", "fraud-gbm-v7", "fraud.score", fraudScore), true, "")
	t.Add("fraud-ml-service", "redis.HGETALL feat:acct", tracepb.Span_SPAN_KIND_CLIENT,
		mlSpan, fraudStart+8*time.Millisecond, dur(1, 5),
		kv("db.system", "redis", "db.operation", "HGETALL", "peer.service", "redis"), true, "")

	if failed {
		// On insufficient funds the failing DB hop is the ACCOUNTS
		// read above; on a fraud block the failing edge is the
		// fraud ScoreTransfer call. No ledger post happens. The
		// transfer-service span carries the error status set above.
		return t
	}

	// 4. Post the double-entry to the ledger via PL/SQL.
	postStart := fraudStart + dur(90, 140)
	t.Add("transfer-service", "ledger-service/PostTransfer", tracepb.Span_SPAN_KIND_CLIENT,
		xferSpan, postStart, dur(80, 180), kv("rpc.system", "grpc", "rpc.method", "PostTransfer",
			"peer.service", "ledger-service", "banking.txn_ref", ref), true, "")
	ledgerSpan := t.Add("ledger-service", "LedgerService.PostTransfer", tracepb.Span_SPAN_KIND_SERVER,
		xferSpan, postStart+2*time.Millisecond, dur(74, 170),
		kv("rpc.system", "grpc", "rpc.method", "PostTransfer",
			"banking.txn_ref", ref, "banking.amount", amt, "banking.currency", currency), true, "")
	// PL/SQL package call posts both legs atomically.
	t.Add("ledger-service", "BEGIN PKG_LEDGER.POST_TRANSFER", tracepb.Span_SPAN_KIND_CLIENT,
		ledgerSpan, postStart+4*time.Millisecond, dur(40, 120),
		oraDB("COREBANK", "BEGIN", "GL_POSTINGS",
			"BEGIN PKG_LEDGER.POST_TRANSFER(:1, :2, :3); END;",
			oracleCore), true, "")
	// Statement journal insert.
	t.Add("ledger-service", "INSERT TXN_JOURNAL", tracepb.Span_SPAN_KIND_CLIENT,
		ledgerSpan, postStart+50*time.Millisecond, dur(10, 40),
		oraDB("COREBANK", "INSERT", "TXN_JOURNAL",
			"INSERT INTO TXN_JOURNAL(TXN_REF, SRC_ACCT, DST_ACCT, AMOUNT, CCY, POSTED_TS) "+
				"VALUES(:1, :2, :3, :4, :5, SYSTIMESTAMP)",
			oracleCore), true, "")

	// 5. Confirmation notification + Kafka event on success.
	notifStart := postStart + dur(40, 90)
	t.Add("transfer-service", "notification-service/SendTransferReceipt", tracepb.Span_SPAN_KIND_CLIENT,
		xferSpan, notifStart, dur(15, 50),
		kv("rpc.system", "grpc", "rpc.method", "SendTransferReceipt",
			"peer.service", "notification-service"), true, "")
	notifSpan := t.Add("notification-service", "NotificationService.SendTransferReceipt", tracepb.Span_SPAN_KIND_SERVER,
		xferSpan, notifStart+2*time.Millisecond, dur(10, 40),
		kv("rpc.system", "grpc", "rpc.method", "SendTransferReceipt",
			"notification.channel", "push"), true, "")
	t.Add("notification-service", "email-service/Send", tracepb.Span_SPAN_KIND_CLIENT,
		notifSpan, notifStart+4*time.Millisecond, dur(10, 30),
		kv("rpc.system", "grpc", "rpc.method", "Send", "peer.service", "email-service"), true, "")

	kafkaStart := postStart + dur(60, 110)
	t.Add("transfer-service", "kafka.publish transfer.posted", tracepb.Span_SPAN_KIND_PRODUCER,
		xferSpan, kafkaStart, dur(8, 25), kv("messaging.system", "kafka",
			"messaging.destination", "transfer.posted", "peer.service", "kafka",
			"banking.txn_ref", ref), true, "")
	M.RecordBiz("kafka.events_published")
	return t
}

// scenarioCardPayment: card authorization at POS / online.
// POST /cards/authorize → api-gateway → card-service, which scores
// fraud, applies an authorization hold on the ledger, and (rarely)
// declines for insufficient funds or a fraud block.
func scenarioCardPayment() *Trace {
	t := NewTrace()
	totalDur := dur(120, 320)
	pan := cardMasked()
	ref := txnRef()
	amt := amount(2, 800)
	currency := ccy()
	merchant := pick("AMZ MKTPLACE", "TESCO STORES 4471", "SHELL OIL 9921",
		"UBER *TRIP", "NETFLIX.COM", "STEAM GAMES")

	insufficient := rollFail(4)
	fraudBlocked := !insufficient && rollFail(3)
	declined := insufficient || fraudBlocked
	authStatus := iff(declined, 402, 200)

	M.RecordHTTP("api-gateway", "POST", "/api/v1/cards/authorize", authStatus, ms(totalDur))
	M.RecordDB("card-service", "oracle", "UPDATE", ms(totalDur)*0.4)
	M.RecordBiz("card.authorizations")
	if declined {
		M.RecordBiz("card.declined")
		if fraudBlocked {
			M.RecordBiz("fraud.blocked")
		}
	} else {
		M.RecordBiz("card.approved")
	}

	feSpan := t.Add("mobile-bff", "POST /cards/authorize", tracepb.Span_SPAN_KIND_CLIENT,
		nil, 0, totalDur, kv("http.method", "POST", "http.url", "/cards/authorize",
			"banking.card", pan, "banking.amount", amt, "banking.currency", currency,
			"banking.merchant", merchant, "peer.service", "api-gateway"),
		!declined, ifErr(declined, iff(insufficient, "402 insufficient funds", "402 fraud decline")))

	apiDur := totalDur - 8*time.Millisecond
	apiSpan := t.Add("api-gateway", "POST /api/v1/cards/authorize", tracepb.Span_SPAN_KIND_SERVER,
		feSpan, 4*time.Millisecond, apiDur, kv("http.method", "POST",
			"http.route", "/api/v1/cards/authorize", "http.status_code", authStatus,
			"banking.txn_ref", ref), !declined,
		ifErr(declined, iff(insufficient, "insufficient funds", "fraud block")))

	cardStart := 10 * time.Millisecond
	cardDur := apiDur - 16*time.Millisecond
	cardSpan := t.Add("card-service", "CardService.Authorize", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, cardStart, cardDur, kv("rpc.system", "grpc", "rpc.method", "Authorize",
			"peer.service", "card-service", "banking.card", pan, "banking.txn_ref", ref,
			"banking.amount", amt, "banking.merchant", merchant),
		!declined, ifErr(declined, iff(insufficient, "auth declined: NSF",
			"auth declined: fraud DECLINE")))

	// Resolve card → linked account.
	t.Add("card-service", "SELECT CARDS", tracepb.Span_SPAN_KIND_CLIENT,
		cardSpan, cardStart+3*time.Millisecond, dur(6, 22),
		oraDB("CARDS", "SELECT", "CARDS",
			"SELECT ACCT_ID, CARD_STATUS, AVAIL_CREDIT FROM CARDS WHERE CARD_TOKEN = :1",
			oracleDG), true, "")

	// Real-time fraud score on the card txn.
	fraudStart := cardStart + 28*time.Millisecond
	fraudScore := mrand.Float64() * 0.4
	if fraudBlocked {
		fraudScore = 0.82 + mrand.Float64()*0.18
	}
	t.Add("card-service", "fraud-service/ScoreCard", tracepb.Span_SPAN_KIND_CLIENT,
		cardSpan, fraudStart, dur(20, 70), kv("rpc.system", "grpc", "rpc.method", "ScoreCard",
			"peer.service", "fraud-service", "banking.txn_ref", ref),
		!fraudBlocked, ifErr(fraudBlocked, "fraud decision: DECLINE"))
	fraudSpan := t.Add("fraud-service", "FraudService.ScoreCard", tracepb.Span_SPAN_KIND_SERVER,
		cardSpan, fraudStart+2*time.Millisecond, dur(16, 64),
		kv("rpc.system", "grpc", "rpc.method", "ScoreCard",
			"fraud.score", fraudScore, "fraud.decision", iff(fraudBlocked, "DECLINE", "APPROVE"),
			"banking.merchant", merchant),
		!fraudBlocked, ifErr(fraudBlocked, "rule CARD_NOT_PRESENT_GEO_MISMATCH"))
	t.Add("fraud-service", "redis.GET fraud:cardrules", tracepb.Span_SPAN_KIND_CLIENT,
		fraudSpan, fraudStart+4*time.Millisecond, dur(1, 4),
		kv("db.system", "redis", "db.operation", "GET", "peer.service", "redis"), true, "")

	if declined {
		return t
	}

	// Apply the authorization hold on the ledger.
	holdStart := fraudStart + dur(40, 80)
	t.Add("card-service", "ledger-service/Hold", tracepb.Span_SPAN_KIND_CLIENT,
		cardSpan, holdStart, dur(20, 60), kv("rpc.system", "grpc", "rpc.method", "Hold",
			"peer.service", "ledger-service", "banking.txn_ref", ref), true, "")
	ledgerSpan := t.Add("ledger-service", "LedgerService.Hold", tracepb.Span_SPAN_KIND_SERVER,
		cardSpan, holdStart+2*time.Millisecond, dur(16, 54),
		kv("rpc.system", "grpc", "rpc.method", "Hold", "banking.amount", amt), true, "")
	t.Add("ledger-service", "UPDATE ACCOUNTS", tracepb.Span_SPAN_KIND_CLIENT,
		ledgerSpan, holdStart+4*time.Millisecond, dur(10, 40),
		oraDB("COREBANK", "UPDATE", "ACCOUNTS",
			"UPDATE ACCOUNTS SET HOLD_AMOUNT = HOLD_AMOUNT + :1 WHERE ACCT_ID = :2",
			oracleCore), true, "")
	return t
}

// scenarioBillPay: pay a registered biller.
// POST /billpay → api-gateway → billpay-service, which looks up the
// biller, debits via the ledger, and notifies the customer.
func scenarioBillPay() *Trace {
	t := NewTrace()
	totalDur := dur(140, 360)
	acct := acctID()
	ref := txnRef()
	amt := amount(5, 1200)
	biller := pick("BRITISH GAS", "THAMES WATER", "VODAFONE UK", "COUNCIL TAX LBTH",
		"SKY BROADBAND", "TV LICENSING")

	insufficient := rollFail(5)
	httpStatus := iff(insufficient, 422, 201)
	M.RecordHTTP("api-gateway", "POST", "/api/v1/billpay", httpStatus, ms(totalDur))
	M.RecordDB("billpay-service", "oracle", "SELECT", ms(totalDur)*0.3)
	M.RecordBiz("billpay.attempted")
	if insufficient {
		M.RecordBiz("billpay.failed")
	} else {
		M.RecordBiz("billpay.completed")
	}

	feSpan := t.Add("mobile-bff", "POST /billpay", tracepb.Span_SPAN_KIND_CLIENT,
		nil, 0, totalDur, kv("http.method", "POST", "http.url", "/billpay",
			"banking.biller", biller, "banking.amount", amt, "peer.service", "api-gateway"),
		!insufficient, ifErr(insufficient, "insufficient funds"))
	apiSpan := t.Add("api-gateway", "POST /api/v1/billpay", tracepb.Span_SPAN_KIND_SERVER,
		feSpan, 3*time.Millisecond, totalDur-6*time.Millisecond,
		kv("http.method", "POST", "http.route", "/api/v1/billpay", "http.status_code", httpStatus,
			"banking.txn_ref", ref), !insufficient,
		ifErr(insufficient, "422 Unprocessable Entity: insufficient funds"))

	bpStart := 8 * time.Millisecond
	bpDur := totalDur - 16*time.Millisecond
	bpSpan := t.Add("billpay-service", "BillPayService.Pay", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, bpStart, bpDur, kv("rpc.system", "grpc", "rpc.method", "Pay",
			"peer.service", "billpay-service", "banking.account_id", acct,
			"banking.biller", biller, "banking.amount", amt, "banking.txn_ref", ref),
		!insufficient, ifErr(insufficient, "debit rejected: insufficient available balance"))

	// Biller lookup.
	t.Add("billpay-service", "SELECT BILLERS", tracepb.Span_SPAN_KIND_CLIENT,
		bpSpan, bpStart+3*time.Millisecond, dur(6, 22),
		oraDB("COREBANK", "SELECT", "BILLERS",
			"SELECT BILLER_ID, SETTLE_ACCT, STATUS FROM BILLERS WHERE BILLER_REF = :1",
			oracleDG), true, "")

	// Ledger debit (the leg that fails on NSF).
	postStart := bpStart + dur(30, 70)
	insMsg := ""
	if insufficient {
		insMsg = "ORA-20012: PKG_LEDGER raised INSUFFICIENT_FUNDS for ACCT"
	}
	t.Add("billpay-service", "ledger-service/Debit", tracepb.Span_SPAN_KIND_CLIENT,
		bpSpan, postStart, dur(40, 120), kv("rpc.system", "grpc", "rpc.method", "Debit",
			"peer.service", "ledger-service", "banking.txn_ref", ref),
		!insufficient, ifErr(insufficient, "ledger debit failed"))
	ledgerSpan := t.Add("ledger-service", "LedgerService.Debit", tracepb.Span_SPAN_KIND_SERVER,
		bpSpan, postStart+2*time.Millisecond, dur(36, 110),
		kv("rpc.system", "grpc", "rpc.method", "Debit", "banking.amount", amt),
		!insufficient, ifErr(insufficient, insMsg))
	t.Add("ledger-service", "BEGIN PKG_LEDGER.POST_DEBIT", tracepb.Span_SPAN_KIND_CLIENT,
		ledgerSpan, postStart+4*time.Millisecond, dur(20, 80),
		oraDB("COREBANK", "BEGIN", "GL_POSTINGS",
			"BEGIN PKG_LEDGER.POST_DEBIT(:1, :2, :3); END;",
			oracleCore), !insufficient, insMsg)

	if insufficient {
		return t
	}

	// Confirmation notification on success.
	notifStart := postStart + dur(40, 90)
	t.Add("billpay-service", "notification-service/SendBillReceipt", tracepb.Span_SPAN_KIND_CLIENT,
		bpSpan, notifStart, dur(12, 40), kv("rpc.system", "grpc", "rpc.method", "SendBillReceipt",
			"peer.service", "notification-service"), true, "")
	t.Add("notification-service", "NotificationService.SendBillReceipt", tracepb.Span_SPAN_KIND_SERVER,
		bpSpan, notifStart+2*time.Millisecond, dur(8, 32),
		kv("rpc.system", "grpc", "rpc.method", "SendBillReceipt",
			"notification.channel", "push"), true, "")
	return t
}

// scenarioDashboard: parallel fan-out for the post-login dashboard —
// the mobile/web channel requests a consolidated view that the
// api-gateway resolves by calling several core services concurrently.
// Exercises a wide cross-service edge set (api-gateway →
// account-service → Oracle, api-gateway → customer-service → Oracle,
// api-gateway → forex-service for the FX ticker) so the graph view
// shows real fan-out from a single inbound request.
func scenarioDashboard() *Trace {
	t := NewTrace()
	totalDur := dur(180, 360)
	cust := custID()
	M.RecordHTTP("api-gateway", "GET", "/api/v1/dashboard", 200, ms(totalDur))
	M.RecordBiz("dashboard.viewed")

	feSpan := t.Add("mobile-bff", "GET /dashboard", tracepb.Span_SPAN_KIND_CLIENT,
		nil, 0, totalDur, kv("http.method", "GET", "http.url", "/dashboard",
			"peer.service", "api-gateway"), true, "")
	apiSpan := t.Add("api-gateway", "GET /api/v1/dashboard", tracepb.Span_SPAN_KIND_SERVER,
		feSpan, 4*time.Millisecond, totalDur-8*time.Millisecond,
		kv("http.method", "GET", "http.route", "/api/v1/dashboard", "http.status_code", 200,
			"banking.customer_id", cust), true, "")

	// Accounts + balances branch: api-gateway → account-service → Oracle
	acctDur := dur(80, 180)
	t.Add("api-gateway", "account-service/ListAccounts", tracepb.Span_SPAN_KIND_CLIENT,
		apiSpan, 10*time.Millisecond, acctDur,
		kv("rpc.system", "grpc", "rpc.method", "ListAccounts",
			"peer.service", "account-service"), true, "")
	acctSpan := t.Add("account-service", "AccountService.ListAccounts", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, 12*time.Millisecond, acctDur-4*time.Millisecond,
		kv("rpc.system", "grpc", "rpc.method", "ListAccounts",
			"banking.customer_id", cust), true, "")
	t.Add("account-service", "SELECT ACCOUNTS", tracepb.Span_SPAN_KIND_CLIENT,
		acctSpan, 16*time.Millisecond, dur(40, 110),
		oraDB("COREBANK", "SELECT", "ACCOUNTS",
			"SELECT ACCT_ID, PRODUCT, BALANCE, AVAIL_BALANCE, CCY FROM ACCOUNTS WHERE CUST_ID = :1",
			oracleDG), true, "")
	t.Add("account-service", "SELECT TXN_JOURNAL", tracepb.Span_SPAN_KIND_CLIENT,
		acctSpan, 60*time.Millisecond, dur(20, 70),
		oraDB("COREBANK", "SELECT", "TXN_JOURNAL",
			"SELECT * FROM (SELECT TXN_REF, AMOUNT, CCY, POSTED_TS FROM TXN_JOURNAL "+
				"WHERE ACCT_ID = :1 ORDER BY POSTED_TS DESC) WHERE ROWNUM <= 10",
			oracleDG), true, "")

	// Customer profile branch (parallel)
	custDur := dur(40, 110)
	t.Add("api-gateway", "customer-service/GetProfile", tracepb.Span_SPAN_KIND_CLIENT,
		apiSpan, 10*time.Millisecond, custDur,
		kv("rpc.system", "grpc", "rpc.method", "GetProfile",
			"peer.service", "customer-service"), true, "")
	custSpan := t.Add("customer-service", "CustomerService.GetProfile", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, 12*time.Millisecond, custDur-4*time.Millisecond,
		kv("rpc.system", "grpc", "rpc.method", "GetProfile", "banking.customer_id", cust), true, "")
	t.Add("customer-service", "SELECT CUSTOMERS", tracepb.Span_SPAN_KIND_CLIENT,
		custSpan, 16*time.Millisecond, dur(8, 30),
		oraDB("COREBANK", "SELECT", "CUSTOMERS",
			"SELECT CUST_ID, FULL_NAME, SEGMENT, KYC_STATUS FROM CUSTOMERS WHERE CUST_ID = :1",
			oracleDG), true, "")

	// FX ticker branch (parallel) — forex-service serves live rates
	// out of its in-memory cache, no DB hop.
	fxDur := dur(20, 60)
	t.Add("api-gateway", "forex-service/GetRates", tracepb.Span_SPAN_KIND_CLIENT,
		apiSpan, 10*time.Millisecond, fxDur,
		kv("rpc.system", "grpc", "rpc.method", "GetRates",
			"peer.service", "forex-service"), true, "")
	fxSpan := t.Add("forex-service", "ForexService.GetRates", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, 12*time.Millisecond, fxDur-4*time.Millisecond,
		kv("rpc.system", "grpc", "rpc.method", "GetRates", "banking.base_ccy", "EUR"), true, "")
	t.Add("forex-service", "redis.MGET fx:EUR:*", tracepb.Span_SPAN_KIND_CLIENT,
		fxSpan, 14*time.Millisecond, dur(1, 5),
		kv("db.system", "redis", "db.operation", "MGET", "peer.service", "redis"), true, "")
	return t
}

// scenarioStatement: generate / fetch an account statement.
// GET /accounts/{id}/statement → api-gateway → statement-service,
// which fans out to account-service (header + balance) and reads the
// transaction journal directly, then renders a PDF. Adds two more
// services into the statement neighbourhood.
func scenarioStatement() *Trace {
	t := NewTrace()
	totalDur := dur(220, 520)
	acct := acctID()
	period := pick("2026-04", "2026-03", "2026-02", "2026-01")
	M.RecordHTTP("api-gateway", "GET", "/api/v1/accounts/{id}/statement", 200, ms(totalDur))
	M.RecordDB("statement-service", "oracle", "SELECT", ms(totalDur)*0.5)
	M.RecordBiz("statements.generated")

	feSpan := t.Add("mobile-bff", "GET /accounts/statement", tracepb.Span_SPAN_KIND_CLIENT,
		nil, 0, totalDur, kv("http.method", "GET", "http.url", "/accounts/statement?period="+period,
			"banking.account_id", acct, "peer.service", "api-gateway"), true, "")
	apiSpan := t.Add("api-gateway", "GET /api/v1/accounts/{id}/statement", tracepb.Span_SPAN_KIND_SERVER,
		feSpan, 3*time.Millisecond, totalDur-6*time.Millisecond,
		kv("http.method", "GET", "http.route", "/api/v1/accounts/{id}/statement",
			"http.status_code", 200, "banking.account_id", acct, "banking.period", period), true, "")

	stmtStart := 8 * time.Millisecond
	stmtDur := totalDur - 16*time.Millisecond
	stmtSpan := t.Add("statement-service", "StatementService.Generate", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, stmtStart, stmtDur, kv("rpc.system", "grpc", "rpc.method", "Generate",
			"banking.account_id", acct, "banking.period", period), true, "")

	// Account header fan-out.
	t.Add("statement-service", "account-service/GetAccount", tracepb.Span_SPAN_KIND_CLIENT,
		stmtSpan, stmtStart+3*time.Millisecond, dur(20, 60),
		kv("rpc.system", "grpc", "rpc.method", "GetAccount",
			"peer.service", "account-service"), true, "")
	acctSpan := t.Add("account-service", "AccountService.GetAccount", tracepb.Span_SPAN_KIND_SERVER,
		stmtSpan, stmtStart+5*time.Millisecond, dur(16, 52),
		kv("rpc.system", "grpc", "rpc.method", "GetAccount", "banking.account_id", acct), true, "")
	t.Add("account-service", "SELECT ACCOUNTS", tracepb.Span_SPAN_KIND_CLIENT,
		acctSpan, stmtStart+7*time.Millisecond, dur(8, 30),
		oraDB("COREBANK", "SELECT", "ACCOUNTS",
			"SELECT ACCT_ID, IBAN, PRODUCT, OPEN_BAL, CCY FROM ACCOUNTS WHERE ACCT_ID = :1",
			oracleDG), true, "")

	// Transaction journal scan for the period — the heavy read.
	t.Add("statement-service", "SELECT TXN_JOURNAL", tracepb.Span_SPAN_KIND_CLIENT,
		stmtSpan, stmtStart+10*time.Millisecond, dur(80, 220),
		oraDB("COREBANK", "SELECT", "TXN_JOURNAL",
			"SELECT TXN_REF, AMOUNT, CCY, DESCR, POSTED_TS FROM TXN_JOURNAL "+
				"WHERE ACCT_ID = :1 AND POSTED_TS >= :2 AND POSTED_TS < :3 ORDER BY POSTED_TS",
			oracleCore), true, "")

	// Standing-orders / direct-debit schedule fan-out.
	soStart := stmtStart + 30*time.Millisecond
	t.Add("statement-service", "billpay-service/ListSchedule", tracepb.Span_SPAN_KIND_CLIENT,
		stmtSpan, soStart, dur(15, 50), kv("rpc.system", "grpc", "rpc.method", "ListSchedule",
			"peer.service", "billpay-service"), true, "")
	bpSpan := t.Add("billpay-service", "BillPayService.ListSchedule", tracepb.Span_SPAN_KIND_SERVER,
		stmtSpan, soStart+2*time.Millisecond, dur(12, 44),
		kv("rpc.system", "grpc", "rpc.method", "ListSchedule",
			"banking.account_id", acct), true, "")
	t.Add("billpay-service", "SELECT STANDING_ORDERS", tracepb.Span_SPAN_KIND_CLIENT,
		bpSpan, soStart+4*time.Millisecond, dur(6, 24),
		oraDB("COREBANK", "SELECT", "STANDING_ORDERS",
			"SELECT SO_ID, BILLER_REF, AMOUNT, NEXT_RUN FROM STANDING_ORDERS WHERE ACCT_ID = :1",
			oracleDG), true, "")

	// PDF render — statement-service writes the document to object
	// storage (S3-compatible), no DB.
	pdfStart := stmtStart + dur(120, 200)
	t.Add("statement-service", "s3.PutObject statement.pdf", tracepb.Span_SPAN_KIND_CLIENT,
		stmtSpan, pdfStart, dur(20, 70),
		kv("http.method", "PUT", "http.url", "https://s3.eu-west-1.amazonaws.com/bank-statements",
			"http.status_code", 200, "peer.service", "s3"), true, "")
	return t
}

// scenarioTransferEvent: event-driven side. Synthesises a
// transfer.posted Kafka event being consumed by notification +
// aml (AML / transaction-monitoring) + audit services; notification
// then fans out to email and sms. Roots itself at a kafka consumer
// span (no parent) so the trace is initiated by the broker, not the
// customer — exercises the producer / consumer kind handling and
// gives the graph view a downstream tree from kafka outward.
func scenarioTransferEvent() *Trace {
	t := NewTrace()
	totalDur := dur(120, 280)
	ref := txnRef()
	M.RecordBiz("kafka.events_consumed")

	notifSpan := t.Add("notification-service", "kafka.consume transfer.posted", tracepb.Span_SPAN_KIND_CONSUMER,
		nil, 0, totalDur, kv("messaging.system", "kafka",
			"messaging.destination", "transfer.posted",
			"messaging.operation", "receive", "peer.service", "kafka",
			"banking.txn_ref", ref), true, "")

	// Email branch
	emailDur := dur(40, 120)
	t.Add("notification-service", "email-service/Send", tracepb.Span_SPAN_KIND_CLIENT,
		notifSpan, 8*time.Millisecond, emailDur,
		kv("rpc.system", "grpc", "rpc.method", "Send", "peer.service", "email-service"),
		true, "")
	emailSpan := t.Add("email-service", "EmailService.Send", tracepb.Span_SPAN_KIND_SERVER,
		notifSpan, 10*time.Millisecond, emailDur-4*time.Millisecond,
		kv("rpc.system", "grpc", "rpc.method", "Send", "email.template", "transfer.receipt"),
		true, "")
	t.Add("email-service", "sendgrid.send", tracepb.Span_SPAN_KIND_CLIENT,
		emailSpan, 14*time.Millisecond, dur(30, 90),
		kv("http.method", "POST", "http.url", "https://api.sendgrid.com/v3/mail/send",
			"http.status_code", 202, "peer.service", "sendgrid"), true, "")

	// SMS branch (parallel)
	smsFail := rollFail(4)
	smsDur := dur(50, 140)
	t.Add("notification-service", "sms-service/Send", tracepb.Span_SPAN_KIND_CLIENT,
		notifSpan, 8*time.Millisecond, smsDur,
		kv("rpc.system", "grpc", "rpc.method", "Send", "peer.service", "sms-service"),
		!smsFail, ifErr(smsFail, "sms send failed"))
	smsSpan := t.Add("sms-service", "SmsService.Send", tracepb.Span_SPAN_KIND_SERVER,
		notifSpan, 10*time.Millisecond, smsDur-4*time.Millisecond,
		kv("rpc.system", "grpc", "rpc.method", "Send"),
		!smsFail, ifErr(smsFail, "twilio: rate limited"))
	twilioStatus := 200
	if smsFail {
		twilioStatus = 429
	}
	t.Add("sms-service", "twilio.send", tracepb.Span_SPAN_KIND_CLIENT,
		smsSpan, 14*time.Millisecond, dur(35, 110),
		kv("http.method", "POST", "http.url", "https://api.twilio.com/2010-04-01/Messages.json",
			"http.status_code", twilioStatus, "peer.service", "twilio"),
		!smsFail, ifErr(smsFail, "429 Too Many Requests"))

	// AML / transaction-monitoring consumer (parallel root in real
	// life — modelled here as a sibling so the trace stays a single
	// tree). Screens the posting against sanctions + structuring
	// rules and indexes it for downstream analytics.
	amlDur := dur(20, 80)
	amlSpan := t.Add("aml-service", "kafka.consume transfer.posted", tracepb.Span_SPAN_KIND_CONSUMER,
		notifSpan, 4*time.Millisecond, amlDur,
		kv("messaging.system", "kafka", "messaging.destination", "transfer.posted",
			"messaging.operation", "receive", "banking.txn_ref", ref), true, "")
	t.Add("aml-service", "elasticsearch.index txn", tracepb.Span_SPAN_KIND_CLIENT,
		amlSpan, 6*time.Millisecond, dur(15, 70),
		kv("db.system", "elasticsearch", "db.operation", "index",
			"peer.service", "elasticsearch"), true, "")

	// Audit consumer — immutable audit trail in Oracle.
	auditSpan := t.Add("audit-service", "kafka.consume transfer.posted", tracepb.Span_SPAN_KIND_CONSUMER,
		notifSpan, 4*time.Millisecond, dur(20, 70),
		kv("messaging.system", "kafka", "messaging.destination", "transfer.posted",
			"banking.txn_ref", ref), true, "")
	t.Add("audit-service", "INSERT AUDIT_LOG", tracepb.Span_SPAN_KIND_CLIENT,
		auditSpan, 6*time.Millisecond, dur(8, 30),
		oraDB("AUDIT", "INSERT", "AUDIT_LOG",
			"INSERT INTO AUDIT_LOG(EVENT, TXN_REF, PAYLOAD, TS) VALUES(:1, :2, :3, SYSTIMESTAMP)",
			oracleCore), true, "")
	return t
}

// ─── Logs ─────────────────────────────────────────────────────────────────────

func sendLog(service string, severity int32, sevText, body string, traceID, spanID []byte, attrs map[string]any) {
	rec := &logspb.LogRecord{
		TimeUnixNano:   uint64(time.Now().UnixNano()),
		SeverityNumber: logspb.SeverityNumber(severity),
		SeverityText:   sevText,
		Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: body}},
		Attributes:     mapToKVs(attrs),
	}
	if traceID != nil {
		rec.TraceId = traceID
	}
	if spanID != nil {
		rec.SpanId = spanID
	}
	s := services[service]
	if s.Name == "" {
		s = Service{Name: service, Pods: []string{service + "-1"}}
	}
	pod := s.Pods[mrand.IntN(len(s.Pods))]
	req := &logscollpb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			kvStr("service.name", s.Name),
			kvStr("host.name", pod),
			kvStr("service.instance.id", pod),
			kvStr("k8s.pod.name", pod),
			kvStr("k8s.cluster.name", clusterFor(s.Name)),
		}},
		ScopeLogs: []*logspb.ScopeLogs{{
			Scope:      &commonpb.InstrumentationScope{Name: "coremetry-demo"},
			LogRecords: []*logspb.LogRecord{rec},
		}},
	}}}
	_ = sendOTLP("/v1/logs", req)
}

// ─── Metrics collector (RED + business + system) ─────────────────────────────
//
// Records actual traffic from each scenario into in-memory aggregates.
// Flushed every 10s as OTLP metrics with method/route/status labels.

type httpKey struct {
	service, method, route string
	status                 int
}
type dbKey struct{ service, system, op string }

type metricsState struct {
	mu sync.Mutex
	// HTTP RED: requests, errors, duration histograms keyed by (service, method, route, status)
	httpRequests map[httpKey]uint64        // counter (delta)
	httpDuration map[httpKey]*histogramAgg // histogram (delta)
	// Business counters
	bizCounters map[string]uint64 // metric name → delta count
	// DB query counts
	dbQueries  map[dbKey]uint64
	dbDuration map[dbKey]*histogramAgg
	// Cumulative totals (for monotonic Sum metrics)
	cumRequests map[httpKey]uint64
	cumBiz      map[string]uint64
	cumDBQ      map[dbKey]uint64
}

type histogramAgg struct {
	count   uint64
	sum     float64
	min     float64
	max     float64
	buckets []uint64 // per-bucket counts over latencyBounds (len+1)
}

func (h *histogramAgg) record(v float64) {
	if h.count == 0 || v < h.min {
		h.min = v
	}
	if v > h.max {
		h.max = v
	}
	h.count++
	h.sum += v
	if h.buckets == nil {
		h.buckets = make([]uint64, len(latencyBounds)+1)
	}
	h.buckets[bucketIndex(v)]++
}

// bucketsOrZero returns the bucket counts, allocating an all-zero slice if
// the aggregate never recorded a value (keeps the OTLP point well-formed).
func (h *histogramAgg) bucketsOrZero() []uint64 {
	if h.buckets == nil {
		return make([]uint64, len(latencyBounds)+1)
	}
	return h.buckets
}

var M = &metricsState{
	httpRequests: map[httpKey]uint64{},
	httpDuration: map[httpKey]*histogramAgg{},
	bizCounters:  map[string]uint64{},
	dbQueries:    map[dbKey]uint64{},
	dbDuration:   map[dbKey]*histogramAgg{},
	cumRequests:  map[httpKey]uint64{},
	cumBiz:       map[string]uint64{},
	cumDBQ:       map[dbKey]uint64{},
}

// RecordHTTP is called by scenarios for each HTTP server span.
func (m *metricsState) RecordHTTP(service, method, route string, status int, durMs float64) {
	k := httpKey{service, method, route, status}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.httpRequests[k]++
	m.cumRequests[k]++
	h := m.httpDuration[k]
	if h == nil {
		h = &histogramAgg{}
		m.httpDuration[k] = h
	}
	h.record(durMs)
}

func (m *metricsState) RecordDB(service, system, op string, durMs float64) {
	k := dbKey{service, system, op}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dbQueries[k]++
	m.cumDBQ[k]++
	h := m.dbDuration[k]
	if h == nil {
		h = &histogramAgg{}
		m.dbDuration[k] = h
	}
	h.record(durMs)
}

func (m *metricsState) RecordBiz(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bizCounters[name]++
	m.cumBiz[name]++
}

// flush builds OTLP metric points and resets delta counters.
func (m *metricsState) flush(startNs, nowNs uint64) []*metricspb.ResourceMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()

	// group metrics by service for proper Resource attribution
	byService := map[string][]*metricspb.Metric{}
	addMetric := func(svc string, mt *metricspb.Metric) { byService[svc] = append(byService[svc], mt) }

	// ── HTTP RED metrics ─────────────────────────────────────────────────────
	httpReqByService := map[string][]*metricspb.NumberDataPoint{}
	httpDurByService := map[string][]*metricspb.HistogramDataPoint{}

	for k, count := range m.httpRequests {
		httpReqByService[k.service] = append(httpReqByService[k.service], &metricspb.NumberDataPoint{
			StartTimeUnixNano: startNs, TimeUnixNano: nowNs,
			Value: &metricspb.NumberDataPoint_AsInt{AsInt: int64(m.cumRequests[k])},
			Attributes: []*commonpb.KeyValue{
				kvStr("http.method", k.method),
				kvStr("http.route", k.route),
				kvIntKV("http.status_code", int64(k.status)),
			},
		})
		_ = count
	}
	for k, h := range m.httpDuration {
		sum := h.sum
		mn, mx := h.min, h.max
		httpDurByService[k.service] = append(httpDurByService[k.service], &metricspb.HistogramDataPoint{
			StartTimeUnixNano: startNs, TimeUnixNano: nowNs,
			Count: h.count, Sum: &sum, Min: &mn, Max: &mx,
			BucketCounts:   h.bucketsOrZero(),
			ExplicitBounds: latencyBounds,
			Attributes: []*commonpb.KeyValue{
				kvStr("http.method", k.method),
				kvStr("http.route", k.route),
			},
		})
	}
	for svc, dps := range httpReqByService {
		addMetric(svc, &metricspb.Metric{
			Name: "http.server.requests", Unit: "1", Description: "Total HTTP server requests",
			Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
				IsMonotonic:            true,
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				DataPoints:             dps,
			}},
		})
	}
	for svc, dps := range httpDurByService {
		addMetric(svc, &metricspb.Metric{
			Name: "http.server.duration", Unit: "ms", Description: "HTTP server request duration",
			Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
				DataPoints:             dps,
			}},
		})
	}

	// ── DB metrics ────────────────────────────────────────────────────────────
	dbReqByService := map[string][]*metricspb.NumberDataPoint{}
	dbDurByService := map[string][]*metricspb.HistogramDataPoint{}
	for k := range m.dbQueries {
		dbReqByService[k.service] = append(dbReqByService[k.service], &metricspb.NumberDataPoint{
			StartTimeUnixNano: startNs, TimeUnixNano: nowNs,
			Value:      &metricspb.NumberDataPoint_AsInt{AsInt: int64(m.cumDBQ[k])},
			Attributes: []*commonpb.KeyValue{kvStr("db.system", k.system), kvStr("db.operation", k.op)},
		})
	}
	for k, h := range m.dbDuration {
		sum := h.sum
		mn, mx := h.min, h.max
		dbDurByService[k.service] = append(dbDurByService[k.service], &metricspb.HistogramDataPoint{
			StartTimeUnixNano: startNs, TimeUnixNano: nowNs,
			Count: h.count, Sum: &sum, Min: &mn, Max: &mx,
			BucketCounts:   h.bucketsOrZero(),
			ExplicitBounds: latencyBounds,
			Attributes:     []*commonpb.KeyValue{kvStr("db.system", k.system), kvStr("db.operation", k.op)},
		})
	}
	for svc, dps := range dbReqByService {
		addMetric(svc, &metricspb.Metric{
			Name: "db.client.queries", Unit: "1", Description: "Total DB client queries",
			Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
				IsMonotonic:            true,
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				DataPoints:             dps,
			}},
		})
	}
	for svc, dps := range dbDurByService {
		addMetric(svc, &metricspb.Metric{
			Name: "db.client.duration", Unit: "ms", Description: "DB client query duration",
			Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
				DataPoints:             dps,
			}},
		})
	}

	// ── Business counters (transfers, cards, bill pay, fraud) ─────────────────
	// Owning service per banking counter. transfers.* / fraud.blocked
	// feed the transfers-per-sec + fraud-block-rate dashboards; the
	// HTTP duration histogram on transfer-service / card-service is
	// the payment-latency distribution.
	bizMap := map[string]string{
		"balance.inquiries":      "account-service",
		"dashboard.viewed":       "api-gateway",
		"transfers.attempted":    "transfer-service",
		"transfers.completed":    "transfer-service",
		"transfers.failed":       "transfer-service",
		"card.authorizations":    "card-service",
		"card.approved":          "card-service",
		"card.declined":          "card-service",
		"billpay.attempted":      "billpay-service",
		"billpay.completed":      "billpay-service",
		"billpay.failed":         "billpay-service",
		"statements.generated":   "statement-service",
		"fraud.blocked":          "fraud-service",
		"auth.logged_in":         "auth-service",
		"auth.login_failed":      "auth-service",
		"kafka.events_published": "transfer-service",
	}
	for name, svc := range bizMap {
		v, ok := m.cumBiz[name]
		if !ok {
			continue
		}
		addMetric(svc, &metricspb.Metric{
			Name: "demo." + name, Unit: "1",
			Description: "Demo business counter: " + name,
			Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
				IsMonotonic:            true,
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				DataPoints: []*metricspb.NumberDataPoint{{
					StartTimeUnixNano: startNs, TimeUnixNano: nowNs,
					Value: &metricspb.NumberDataPoint_AsInt{AsInt: int64(v)},
				}},
			}},
		})
	}

	// ── System / runtime + saturation gauges (per backend service) ────────────
	// Everything here is correlated with the live load factor so that, at the
	// morning peak or during an incident, CPU, memory, in-flight + queued
	// requests, connection-pool usage, GC pauses and Kafka consumer lag all
	// climb together — the saturation signature an operator expects — while
	// cache hit-ratio dips. Factors are read once per flush.
	lf := L.latencyFactor()
	rf := L.rateFactor()

	gaugeI := func(svc, name, unit, desc string, v int64) {
		addMetric(svc, &metricspb.Metric{
			Name: name, Unit: unit, Description: desc,
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
				DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: nowNs,
					Value: &metricspb.NumberDataPoint_AsInt{AsInt: v}}},
			}},
		})
	}
	gaugeD := func(svc, name, unit, desc string, v float64) {
		addMetric(svc, &metricspb.Metric{
			Name: name, Unit: unit, Description: desc,
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
				DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: nowNs,
					Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: v}}},
			}},
		})
	}
	clamp := func(v, lo, hi float64) float64 {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}

	for svcKey := range services {
		if svcKey == "mobile-bff" {
			continue
		}
		s := services[svcKey]

		// Resident memory: per-service baseline that grows with load and
		// balloons under a GC / contention incident (lf).
		rss := (300_000_000.0 + 140_000_000.0*rf) * (0.85 + 0.35*lf) * (0.9 + 0.2*mrand.Float64())
		gaugeD(svcKey, "process.runtime.memory.rss", "By", "Process resident memory size", rss)

		// Process + host CPU utilisation track load and incident pressure.
		cpu := clamp((0.08+0.30*rf)*(0.7+0.6*lf)*(0.85+0.3*mrand.Float64()), 0.02, 0.99)
		gaugeD(svcKey, "process.runtime.cpu.utilization", "1", "Process CPU utilization", cpu)
		gaugeD(svcKey, "system.cpu.utilization", "1", "Host CPU utilization",
			clamp(cpu*(0.8+0.3*mrand.Float64())+0.05, 0.02, 0.99))
		gaugeD(svcKey, "system.memory.utilization", "1", "Host memory utilization",
			clamp(0.45+0.25*rf+0.1*mrand.Float64(), 0.1, 0.97))

		// Goroutines / worker threads in flight scale with concurrency.
		gaugeI(svcKey, "process.runtime.goroutines", "1", "Number of goroutines / worker threads",
			int64(clamp(24+160*rf*(0.6+0.5*lf), 4, 4000)))

		// Active + queued requests: the queue builds when latency (lf) rises
		// faster than capacity — the classic saturation tell.
		active := int64(clamp(2+34*rf, 0, 500))
		gaugeI(svcKey, "http.server.active_requests", "1", "Currently active HTTP server requests", active)
		gaugeI(svcKey, "http.server.queued_requests", "1", "Requests waiting for a worker",
			int64(clamp(float64(active)*(lf-1)*1.5, 0, 800)))

		// DB connection pool: usage approaches the cap under load, so the
		// pool-saturation panel actually moves.
		poolUse := clamp(6+34*rf*(0.7+0.5*lf), 0, 50)
		gaugeI(svcKey, "db.client.connections.usage", "{connection}", "Connections in use from the pool", int64(poolUse))
		gaugeI(svcKey, "db.client.connections.max", "{connection}", "Max pool size", 50)
		gaugeD(svcKey, "db.client.connections.utilization", "1", "Pool utilization (usage/max)", poolUse/50.0)

		// GC pause: small in steady state, large during a gc-pause-storm
		// incident (lf high). Emitted only for GC-managed runtimes.
		if gcManaged(s.Lang) {
			gaugeD(svcKey, "process.runtime.gc.pause", "ms", "Most recent GC pause duration",
				(1.5+3.5*mrand.Float64())*lf*lf)
		}

		// Cache hit-ratio for Redis-backed services: ~0.97 normally, dips
		// when a dependency-degraded incident cold-starts the cache.
		if redisBackedServices[svcKey] {
			gaugeD(svcKey, "cache.hit_ratio", "1", "Cache hit ratio",
				clamp(0.97-0.12*(lf-1)-0.02*mrand.Float64(), 0.4, 0.999))
		}

		// Kafka consumer lag for consumer services: backs up under load and
		// during incidents, drains otherwise.
		if kafkaConsumerServices[svcKey] {
			gaugeI(svcKey, "messaging.kafka.consumer.lag", "{message}", "Consumer group lag",
				int64(clamp(20+400*(rf-0.5)+1500*(lf-1), 0, 50000)))
		}
	}

	// ── Banking domain metrics ────────────────────────────────────────────────
	// Derived gauges + histogram the operator dashboards graph directly:
	// transfers/sec, fraud-block rate, and the payment latency
	// distribution. Computed from this interval's delta counters /
	// duration aggregates (the flush cadence is the 10s window).
	const windowSec = 10.0

	// transfers/sec — completed transfers in the interval / window.
	tps := float64(m.bizCounters["transfers.completed"]) / windowSec
	addMetric("transfer-service", &metricspb.Metric{
		Name: "banking.transfers.rate", Unit: "{transfer}/s",
		Description: "Completed money transfers per second",
		Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
			DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: nowNs,
				Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: tps}}},
		}},
	})

	// fraud-block rate — fraud blocks / total risk-scored attempts
	// (transfers + card authorizations) this interval.
	scored := m.bizCounters["transfers.attempted"] + m.bizCounters["card.authorizations"]
	var blockRate float64
	if scored > 0 {
		blockRate = float64(m.bizCounters["fraud.blocked"]) / float64(scored)
	}
	addMetric("fraud-service", &metricspb.Metric{
		Name: "banking.fraud.block_rate", Unit: "1",
		Description: "Fraction of risk-scored transactions blocked by fraud",
		Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
			DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: nowNs,
				Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: blockRate}}},
		}},
	})

	// payment latency histogram — folds the transfer + card-payment
	// HTTP server durations into a single payment-path distribution.
	payLat := &histogramAgg{buckets: make([]uint64, len(latencyBounds)+1)}
	for k, h := range m.httpDuration {
		if k.service != "transfer-service" && k.service != "card-service" {
			continue
		}
		if payLat.count == 0 || h.min < payLat.min {
			payLat.min = h.min
		}
		if h.max > payLat.max {
			payLat.max = h.max
		}
		payLat.count += h.count
		payLat.sum += h.sum
		for i, b := range h.bucketsOrZero() {
			payLat.buckets[i] += b
		}
	}
	if payLat.count > 0 {
		psum, pmn, pmx := payLat.sum, payLat.min, payLat.max
		addMetric("payment-service", &metricspb.Metric{
			Name: "banking.payment.latency", Unit: "ms",
			Description: "End-to-end payment (transfer + card) latency",
			Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
				DataPoints: []*metricspb.HistogramDataPoint{{
					StartTimeUnixNano: startNs, TimeUnixNano: nowNs,
					Count: payLat.count, Sum: &psum, Min: &pmn, Max: &pmx,
					BucketCounts:   payLat.buckets,
					ExplicitBounds: latencyBounds,
				}},
			}},
		})
	}

	// reset deltas
	m.httpRequests = map[httpKey]uint64{}
	m.httpDuration = map[httpKey]*histogramAgg{}
	m.dbQueries = map[dbKey]uint64{}
	m.dbDuration = map[dbKey]*histogramAgg{}
	m.bizCounters = map[string]uint64{}

	// build resource metrics
	var rms []*metricspb.ResourceMetrics
	for svcKey, mts := range byService {
		s, ok := services[svcKey]
		if !ok {
			s = Service{Name: svcKey, Pods: []string{svcKey + "-1"}}
		}
		pod := s.Pods[mrand.IntN(len(s.Pods))]
		rms = append(rms, &metricspb.ResourceMetrics{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				kvStr("service.name", s.Name),
				kvStr("host.name", pod),
				kvStr("service.instance.id", pod),
				kvStr("k8s.pod.name", pod),
				kvStr("k8s.cluster.name", clusterFor(s.Name)),
				kvStr("deployment.environment", "demo"),
			}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Scope:   &commonpb.InstrumentationScope{Name: "coremetry-demo"},
				Metrics: mts,
			}},
		})
	}

	// ── OracleDB receiver metrics ─────────────────────────────────────────────
	// Synthetic oracledb.* points that make the /databases OracleDB-receiver
	// panel show real data (Status="up") + surface a source="receiver" Oracle
	// row per instance. Self-contained ResourceMetrics (own service.name =
	// oracledb-receiver), appended on the existing metrics tick. See
	// oracle_metrics.go for the read-contract mapping.
	rms = append(rms, oracleReceiverMetrics(startNs, nowNs)...)

	return rms
}

func sendMetrics(startNs uint64) uint64 {
	now := uint64(time.Now().UnixNano())
	rms := M.flush(startNs, now)
	if len(rms) == 0 {
		return now
	}
	if err := sendOTLP("/v1/metrics", &metricscollpb.ExportMetricsServiceRequest{ResourceMetrics: rms}); err != nil {
		log.Printf("[metrics] send error: %v", err)
	}
	return now
}

// ─── Driver ───────────────────────────────────────────────────────────────────

var scenarios = []struct {
	name   string
	weight int
	fn     scenario
}{
	{"BalanceInquiry", 5, scenarioBalanceInquiry},
	{"Dashboard", 4, scenarioDashboard},
	{"CardPayment", 4, scenarioCardPayment},
	{"BillPay", 3, scenarioBillPay},
	{"Statement", 3, scenarioStatement},
	{"Login", 2, scenarioLogin},
	{"Transfer", 3, scenarioTransfer},
	{"TransferEvent", 2, scenarioTransferEvent},
}

func pickScenario() (string, scenario) {
	total := 0
	for _, s := range scenarios {
		total += s.weight
	}
	r := mrand.IntN(total)
	for _, s := range scenarios {
		if r < s.weight {
			return s.name, s.fn
		}
		r -= s.weight
	}
	return scenarios[0].name, scenarios[0].fn
}

func main() {
	flag.Parse()
	if *profileEndpoint == "" {
		// Default to coremetry directly (collector doesn't speak our pprof protocol).
		*profileEndpoint = "http://coremetry:8088"
	}
	log.Printf("Coremetry demo — retail-banking traffic generator")
	log.Printf("  endpoint:         %s (traces/logs/metrics)", *endpoint)
	log.Printf("  profile endpoint: %s", *profileEndpoint)
	log.Printf("  rate:             %.1f scenarios/sec", *rps)
	rollPodGeneration()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var wg sync.WaitGroup

	// ── Load / incident model ────────────────────────────────────────────────
	// Drives the diurnal traffic curve, organic micro-spikes, and the
	// transient incidents that make latency + error rate move together.
	go L.run(ctx)

	// ── Continuous CPU profiling ─────────────────────────────────────────────
	// Capture a 5-second CPU profile every 10 seconds and push it.
	wg.Add(1)
	go runProfileLoop(ctx, &wg)

	// Trace generator — the inter-arrival gap is recomputed every iteration
	// from the configured -rps scaled by the live diurnal/spike rate factor,
	// with exponential jitter so arrivals look Poisson-ish rather than
	// metronomic. The demo therefore genuinely slows overnight, surges at
	// the morning peak, and bursts during incidents.
	wg.Add(1)
	go func() {
		defer wg.Done()
		count := 0
		var mu sync.Mutex

		// counter logger — also surfaces the live rate factor + any active
		// incident so the console narrates what the data is doing.
		go func() {
			for range time.Tick(10 * time.Second) {
				mu.Lock()
				c := count
				mu.Unlock()
				inc := L.incidentLabel()
				if inc == "" {
					inc = "none"
				}
				log.Printf("[stats] sent %d traces | rate x%.2f | latency x%.2f | incident: %s",
					c, L.rateFactor(), L.latencyFactor(), inc)
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			name, fn := pickScenario()
			t := fn()
			if err := t.Send(); err == nil {
				if mrand.IntN(3) == 0 {
					sendScenarioLog(name, t)
				}
				mu.Lock()
				count++
				mu.Unlock()
			} else {
				log.Printf("[trace] send error: %v", err)
			}

			// Effective scenarios/sec = configured rps × live load factor.
			rate := *rps * L.rateFactor()
			if rate < 0.05 {
				rate = 0.05
			}
			// Exponential inter-arrival (Poisson process) around the mean gap.
			mean := float64(time.Second) / rate
			u := mrand.Float64()
			if u < 1e-9 {
				u = 1e-9
			}
			gap := time.Duration(-math.Log(u) * mean)
			timer := time.NewTimer(gap)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()

	// Metrics ticker (every 10s) — flushes accumulated counters/histograms
	wg.Add(1)
	go func() {
		defer wg.Done()
		startNs := uint64(time.Now().UnixNano())
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				sendMetrics(startNs) // final flush
				return
			case <-ticker.C:
				startNs = sendMetrics(startNs)
			}
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	wg.Wait()
}

func sendScenarioLog(scenarioName string, t *Trace) {
	if len(t.spans) == 0 {
		return
	}
	// pick a span (preferably an error one)
	var target spanInfo
	for _, si := range t.spans {
		if si.span.Status != nil && si.span.Status.Code == tracepb.Status_STATUS_CODE_ERROR {
			target = si
			break
		}
	}
	if target.span == nil {
		target = t.spans[mrand.IntN(len(t.spans))]
	}

	switch {
	case target.span.Status != nil && target.span.Status.Code == tracepb.Status_STATUS_CODE_ERROR:
		// On failure, swap the generic message for one of the
		// curated SRE-grade signal lines (ORA-, NullPointer,
		// deadlock, panic, OOM, …) ~40% of the time. Density is
		// tuned so a fresh demo deployment shows live content in
		// the /anomalies log-pattern section within a minute or
		// two — too low and the section stays empty during a
		// walkthrough; too high and patterns blur together.
		threshold := 40
		extra := kv("error.type", "OperationFailed")
		// During an active incident, error logs both spike in density and
		// carry the incident label so the log patterns line up with the
		// latency/error-rate anomaly the operator is staring at.
		if inc := L.incidentLabel(); inc != "" {
			threshold = 80
			extra["incident"] = inc
		}
		body := fmt.Sprintf("%s failed: %s", scenarioName, target.span.Status.Message)
		if mrand.IntN(100) < threshold {
			body = pickAnomalyLine(target.service)
		}
		sendLog(target.service, 17, "ERROR", body,
			t.traceID, target.span.SpanId, extra)
	case mrand.IntN(4) == 0:
		sendLog(target.service, 13, "WARN",
			fmt.Sprintf("Slow %s: took longer than threshold", scenarioName),
			t.traceID, target.span.SpanId, kv("threshold.ms", 200))
	default:
		sendLog(target.service, 9, "INFO",
			fmt.Sprintf("%s completed successfully", scenarioName),
			t.traceID, target.span.SpanId, nil)
	}
}

// anomalyLines are realistic-looking log bodies for each curated
// production signal pattern. Each entry contains the substring(s)
// the server-side detector matches on so the /anomalies page has
// a live source of synthetic ORA- / NPE / OOM / deadlock / panic
// lines without us having to hand-craft a separate scenario.
// Banking-flavoured: Oracle core-banking faults, ledger PL/SQL
// exceptions, fraud / limit declines, payment-rail timeouts.
var anomalyLines = []string{
	"ORA-00060: deadlock detected while waiting for resource on TXN_JOURNAL",
	"ORA-01017: invalid username/password; logon denied for COREBANK_APP",
	"ORA-12541: TNS:no listener — connection to corebank-scan.prod:1521 refused",
	"ORA-00054: resource busy and acquire with NOWAIT specified on ACCOUNTS row",
	"ORA-01555: snapshot too old: rollback segment too small during statement scan",
	"ORA-20012: PKG_LEDGER.POST_TRANSFER raised INSUFFICIENT_FUNDS for ACCT-204918337",
	"ORA-04068: existing state of package PKG_LEDGER has been discarded",
	"java.lang.NullPointerException: Cannot invoke balance on null Account at com.bank.LedgerService.post(LedgerService.java:214)",
	"java.lang.OutOfMemoryError: Java heap space — heap dump written to /var/log/heapdump-1715.hprof",
	"OOMKilled: container exceeded memory limit (1Gi), restarting",
	"ECONNREFUSED: connection refused to redis-prod-2:6379 — fraud rule cache circuit breaker open",
	"x509: certificate has expired or is not yet valid: current time 2026-05-09 is after 2026-05-08T00:00:00Z",
	"tls: handshake failure with card-network gateway: unsupported cipher suite",
	"panic: runtime error: index out of range [5] with length 3 — goroutine 142 stack",
	"context deadline exceeded — upstream ledger-service did not respond within 5s",
	"401 Unauthorized — MFA step-up required for customer route=/api/v1/transfers",
	"transfer declined: daily transfer limit exceeded for ACCT-118273645",
	"fraud decision DECLINE: rule HIGH_VELOCITY tripped, model score 0.93 over threshold 0.80",
	"no space left on device: cannot write Oracle redo log to /u02/oradata — disk full",
}

func pickAnomalyLine(service string) string {
	line := anomalyLines[mrand.IntN(len(anomalyLines))]
	return service + ": " + line
}

// ─── OTLP HTTP send ───────────────────────────────────────────────────────────

// ─── Profile loop ─────────────────────────────────────────────────────────────

func runProfileLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	hostname, _ := os.Hostname()

	// Pick a "service" label for the profile. We use one of our backend
	// services so trace-to-profile linking finds matches in the demo data.
	const profileSvc = "ledger-service"

	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		captureAndPush(ctx, profileSvc, hostname)
	}
}

func captureAndPush(ctx context.Context, service, host string) {
	startNs := time.Now().UnixNano()
	const window = 5 * time.Second

	var buf bytes.Buffer
	if err := pprof.StartCPUProfile(&buf); err != nil {
		log.Printf("[profile] StartCPUProfile: %v", err)
		return
	}
	select {
	case <-ctx.Done():
		pprof.StopCPUProfile()
		return
	case <-time.After(window):
	}
	pprof.StopCPUProfile()

	if err := pushProfile(service, host, "cpu", startNs, int64(window), buf.Bytes()); err != nil {
		log.Printf("[profile] push cpu: %v", err)
	} else {
		log.Printf("[profile] pushed cpu profile (%d bytes, %ds window)", buf.Len(), int(window.Seconds()))
	}

	// Also grab a heap snapshot — cheap, useful for memory inspection.
	// Tag it with the same 10s tick window so trace→profile linking can
	// match it against spans that happened in the interval.
	var heap bytes.Buffer
	if err := pprof.WriteHeapProfile(&heap); err == nil {
		const tick = 10 * time.Second
		heapStart := time.Now().Add(-tick).UnixNano()
		if err := pushProfile(service, host, "heap", heapStart, int64(tick), heap.Bytes()); err != nil {
			log.Printf("[profile] push heap: %v", err)
		} else {
			log.Printf("[profile] pushed heap snapshot (%d bytes)", heap.Len())
		}
	}
}

func pushProfile(service, host, ptype string, startNs, durNs int64, data []byte) error {
	req, _ := http.NewRequest("POST", *profileEndpoint+"/v1/profiles", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Coremetry-Service", service)
	req.Header.Set("X-Coremetry-Host", host)
	req.Header.Set("X-Coremetry-Profile-Type", ptype)
	req.Header.Set("X-Coremetry-Start-Time-Ns", fmt.Sprintf("%d", startNs))
	req.Header.Set("X-Coremetry-Duration-Ns", fmt.Sprintf("%d", durNs))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// sendOTLP posts an OTLP message to {endpoint}{path} as protobuf binary.
// We send protobuf (not JSON) because OTLP/JSON requires hex-encoded
// trace/span IDs while protojson defaults to base64 — the spec-compliant
// OTel Collector rejects the base64 form.
func sendOTLP(path string, msg proto.Message) error {
	body, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	req, _ := http.NewRequest("POST", *endpoint+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func randID(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// dur returns a realistic, right-skewed latency derived from [minMs,maxMs].
//
// Real service latencies are log-normal: a dense body just above the floor
// and a long tail toward (and occasionally past) the nominal ceiling — not
// the flat uniform band the demo used to emit. We sample a standard normal
// (Box–Muller) and exponentiate it so the median lands ~30% into the band
// while the p99 stretches out. The whole distribution is then scaled by the
// current load/incident factor, so saturation shows up as a coordinated
// latency rise across every hop in the mesh (and therefore across the
// duration histograms) rather than as isolated random blips.
func dur(minMs, maxMs int) time.Duration {
	lo := float64(minMs)
	span := float64(maxMs - minMs)
	if span < 1 {
		span = 1
	}
	u1 := mrand.Float64()
	if u1 < 1e-9 {
		u1 = 1e-9
	}
	u2 := mrand.Float64()
	z := math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
	const sigma = 0.55 // heavy but bounded tail
	frac := 0.30 * math.Exp(sigma*z)
	if frac > 2.5 { // clamp pathological tail to 2.5x the band
		frac = 2.5
	}
	msv := (lo + frac*span) * L.latencyFactor()
	if msv < 1 {
		msv = 1
	}
	return time.Duration(msv * float64(time.Millisecond))
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func pick[T any](xs ...T) T { return xs[mrand.IntN(len(xs))] }

func iff[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}
func ifErr(cond bool, msg string) string {
	if cond {
		return msg
	}
	return ""
}

func kv(args ...any) map[string]any {
	m := make(map[string]any, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		k, _ := args[i].(string)
		m[k] = args[i+1]
	}
	return m
}

func kvStr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func kvIntKV(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

func mapToKVs(m map[string]any) []*commonpb.KeyValue {
	if len(m) == 0 {
		return nil
	}
	out := make([]*commonpb.KeyValue, 0, len(m))
	for k, v := range m {
		out = append(out, &commonpb.KeyValue{Key: k, Value: anyVal(v)})
	}
	return out
}

func anyVal(v any) *commonpb.AnyValue {
	switch x := v.(type) {
	case string:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: x}}
	case int:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: int64(x)}}
	case int64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: x}}
	case float64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: x}}
	case bool:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: x}}
	default:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: fmt.Sprint(v)}}
	}
}
