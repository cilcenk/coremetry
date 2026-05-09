// Coremetry demo: realistic e-commerce traffic generator that emits
// OTLP traces, logs, and metrics over HTTP to Coremetry.
//
// Usage:
//   go run ./cmd/demo -endpoint http://localhost:14318 -rps 2.0
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"log"
	mrand "math/rand/v2"
	"net/http"
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
type Service struct {
	Name string
	Pods []string
}

var services = map[string]Service{
	// Edge / public-facing
	"frontend":               {"frontend",               []string{"web-prod-1", "web-prod-2", "web-prod-3"}},
	"api-gateway":            {"api-gateway",            []string{"gw-prod-1", "gw-prod-2", "gw-prod-3"}},
	// Core domain services
	"user-service":           {"user-service",           []string{"users-prod-1", "users-prod-2", "users-prod-3"}},
	"order-service":          {"order-service",          []string{"orders-prod-1", "orders-prod-2"}},
	"payment-service":        {"payment-service",        []string{"pay-prod-1", "pay-prod-2"}},
	"product-service":        {"product-service",        []string{"products-prod-1", "products-prod-2", "products-prod-3"}},
	"cart-service":           {"cart-service",           []string{"cart-prod-1", "cart-prod-2"}},
	"search-service":         {"search-service",         []string{"search-prod-1", "search-prod-2"}},
	// Supporting services — added so the topology fan-out reads
	// like a real e-commerce mesh and the backtrace / graph views
	// have meaningful caller-callee chains to inspect.
	"inventory-service":      {"inventory-service",      []string{"inv-prod-1", "inv-prod-2", "inv-prod-3"}},
	"recommendation-service": {"recommendation-service", []string{"rec-prod-1", "rec-prod-2"}},
	"review-service":         {"review-service",         []string{"review-prod-1", "review-prod-2"}},
	"pricing-service":        {"pricing-service",        []string{"pricing-prod-1", "pricing-prod-2"}},
	"shipping-service":       {"shipping-service",       []string{"ship-prod-1", "ship-prod-2"}},
	"fraud-service":          {"fraud-service",          []string{"fraud-prod-1", "fraud-prod-2"}},
	"notification-service":   {"notification-service",   []string{"notif-prod-1", "notif-prod-2"}},
	"email-service":          {"email-service",          []string{"mail-prod-1"}},
	"sms-service":            {"sms-service",            []string{"sms-prod-1"}},
	"analytics-service":      {"analytics-service",      []string{"analytics-prod-1", "analytics-prod-2"}},
	"audit-service":          {"audit-service",          []string{"audit-prod-1"}},
	"ml-service":             {"ml-service",             []string{"ml-prod-1", "ml-prod-2"}},
}

// User-agent pool — each trace picks one to put on the frontend's
// client/server boundary so the backtrace view can group traffic by
// browser / mobile app / health check / scraper.
var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/124.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_4) Safari/17.4",
	"Mozilla/5.0 (X11; Linux x86_64) Firefox/126.0",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) Mobile/15E148 Safari/604.1",
	"CoremetryApp/2.4.1 (Android 14; Pixel 8)",
	"curl/8.6.0",
	"kube-probe/1.29",
	"GoogleBot/2.1 (+http://www.google.com/bot.html)",
}

// Synthetic egress IPs for the user-facing frontend hits — looks
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
	// Per-trace browser-like client identity for the frontend's
	// inbound edge (frontend's user request). All other inbound
	// edges (api-gateway → user-service, order-service → payment,
	// …) derive client.address from the parent pod's IP.
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
	// spans with client.address + user_agent.original. The frontend's
	// inbound edge gets the synthetic browser IP / UA picked at trace
	// creation; every other inbound edge gets the parent pod's
	// deterministic IP and the same UA so the request signature
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
		if parent.service == "frontend" {
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
		rs = append(rs, &tracepb.ResourceSpans{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				kvStr("service.name", s.Name),
				kvStr("host.name", pod),
				kvStr("service.instance.id", pod),
				kvStr("k8s.pod.name", pod),
				kvStr("k8s.pod.ip", podIP(pod)),
				kvStr("k8s.namespace.name", "demo"),
				kvStr("deployment.environment", "demo"),
				kvStr("service.version", "1.0.0"),
			}},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "coremetry-demo"},
				Spans: spans,
			}},
		})
	}
	return sendOTLP("/v1/traces", &tracecollpb.ExportTraceServiceRequest{ResourceSpans: rs})
}

// ─── Scenarios ────────────────────────────────────────────────────────────────

type scenario func() *Trace

// scenarioBrowseProducts: GET /products → product-service → cache/db
func scenarioBrowseProducts() *Trace {
	t := NewTrace()
	totalDur := dur(60, 180)
	method := pick("GET", "GET", "GET", "POST")
	route := pick("/api/products", "/api/products/featured", "/api/products?category=electronics")
	M.RecordHTTP("api-gateway", method, route, 200, ms(totalDur))
	M.RecordDB("product-service", "postgresql", "SELECT", ms(totalDur)*0.6)

	feSpan := t.Add("frontend", method+" "+route, tracepb.Span_SPAN_KIND_CLIENT,
		nil, 0, totalDur, kv("http.method", method, "http.url", route, "user.agent", "Mozilla/5.0",
			"peer.service", "api-gateway"), true, "")

	apiDur := totalDur - 10*time.Millisecond
	apiSpan := t.Add("api-gateway", method+" "+route, tracepb.Span_SPAN_KIND_SERVER,
		feSpan, 5*time.Millisecond, apiDur, kv("http.method", method, "http.route", route,
			"http.status_code", 200), true, "")

	prodDur := apiDur - 20*time.Millisecond
	prodSpan := t.Add("product-service", "ProductService.List", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, 10*time.Millisecond, prodDur, kv("rpc.system", "grpc", "rpc.method", "List",
			"peer.service", "product-service"), true, "")

	t.Add("product-service", "db.SELECT products", tracepb.Span_SPAN_KIND_CLIENT,
		prodSpan, 15*time.Millisecond, prodDur-20*time.Millisecond,
		kv("db.system", "postgresql", "db.name", "shop", "db.statement",
			"SELECT id, name, price FROM products WHERE active=true LIMIT 50",
			"peer.service", "postgres"), true, "")
	return t
}

// scenarioUserLogin: POST /login → api → user-service → db
func scenarioUserLogin() *Trace {
	t := NewTrace()
	totalDur := dur(80, 220)
	authFail := mrand.IntN(100) < 8 // 8% fail
	status := iff(authFail, 401, 200)
	M.RecordHTTP("api-gateway", "POST", "/api/auth/login", status, ms(totalDur))
	M.RecordDB("user-service", "postgresql", "SELECT", ms(totalDur)*0.5)
	if authFail {
		M.RecordBiz("users.login_failed")
	} else {
		M.RecordBiz("users.logged_in")
	}

	feSpan := t.Add("frontend", "POST /login", tracepb.Span_SPAN_KIND_CLIENT,
		nil, 0, totalDur, kv("http.method", "POST", "http.url", "/login",
			"peer.service", "api-gateway"), !authFail, ifErr(authFail, "401 Unauthorized"))

	statusCode := 200
	if authFail {
		statusCode = 401
	}
	apiSpan := t.Add("api-gateway", "POST /api/auth/login", tracepb.Span_SPAN_KIND_SERVER,
		feSpan, 3*time.Millisecond, totalDur-6*time.Millisecond,
		kv("http.method", "POST", "http.route", "/api/auth/login", "http.status_code", statusCode),
		!authFail, ifErr(authFail, "invalid credentials"))

	userDur := totalDur - 20*time.Millisecond
	userSpan := t.Add("user-service", "UserService.Authenticate", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, 8*time.Millisecond, userDur, kv("rpc.system", "grpc", "rpc.method", "Authenticate",
			"peer.service", "user-service"), !authFail, ifErr(authFail, "user not found or wrong password"))

	t.Add("user-service", "db.SELECT users", tracepb.Span_SPAN_KIND_CLIENT,
		userSpan, 15*time.Millisecond, userDur-25*time.Millisecond,
		kv("db.system", "postgresql", "db.name", "auth", "db.statement",
			"SELECT id, password_hash FROM users WHERE email=$1",
			"peer.service", "postgres"), true, "")
	// On a successful sign-in from a new device, user-service fires
	// off an alert to notification-service. notification-service
	// now has two upstream entry points (kafka consumer for events +
	// rpc from user-service for live alerts) — exercises fan-in
	// across transport types in the consumer view.
	if !authFail && mrand.IntN(100) < 35 {
		notifDur := dur(20, 60)
		t.Add("user-service", "notification-service/SendLoginAlert", tracepb.Span_SPAN_KIND_CLIENT,
			userSpan, 30*time.Millisecond, notifDur,
			kv("rpc.system", "grpc", "rpc.method", "SendLoginAlert",
				"peer.service", "notification-service"), true, "")
		notifSpan := t.Add("notification-service", "NotificationService.SendLoginAlert", tracepb.Span_SPAN_KIND_SERVER,
			userSpan, 32*time.Millisecond, notifDur-4*time.Millisecond,
			kv("rpc.system", "grpc", "rpc.method", "SendLoginAlert"), true, "")
		t.Add("notification-service", "email-service/Send", tracepb.Span_SPAN_KIND_CLIENT,
			notifSpan, 34*time.Millisecond, dur(15, 45),
			kv("rpc.system", "grpc", "rpc.method", "Send",
				"peer.service", "email-service"), true, "")
	}
	return t
}

// scenarioCheckout: complex multi-service trace with payment
func scenarioCheckout() *Trace {
	t := NewTrace()
	totalDur := dur(350, 700)
	payFail := mrand.IntN(100) < 5 // 5% fail
	M.RecordHTTP("api-gateway", "POST", "/api/orders", iff(payFail, 502, 201), ms(totalDur))
	M.RecordDB("order-service", "postgresql", "INSERT", ms(totalDur)*0.25)
	if payFail {
		M.RecordBiz("orders.failed")
		M.RecordBiz("payments.failed")
	} else {
		M.RecordBiz("orders.created")
		M.RecordBiz("payments.processed")
		M.RecordBiz("kafka.messages_published")
	}

	feSpan := t.Add("frontend", "POST /checkout", tracepb.Span_SPAN_KIND_CLIENT,
		nil, 0, totalDur, kv("http.method", "POST", "http.url", "/checkout",
			"order.items", mrand.IntN(5)+1, "peer.service", "api-gateway"),
		!payFail, ifErr(payFail, "payment failed"))

	apiDur := totalDur - 10*time.Millisecond
	apiSpan := t.Add("api-gateway", "POST /api/orders", tracepb.Span_SPAN_KIND_SERVER,
		feSpan, 5*time.Millisecond, apiDur, kv("http.method", "POST", "http.route", "/api/orders",
			"http.status_code", iff(payFail, 502, 201), "user.id", mrand.IntN(99999)+1),
		!payFail, ifErr(payFail, "downstream payment failed"))

	// Auth check
	t.Add("api-gateway", "user-service/ValidateToken", tracepb.Span_SPAN_KIND_CLIENT,
		apiSpan, 8*time.Millisecond, dur(8, 18), kv("rpc.system", "grpc", "rpc.method", "ValidateToken",
			"peer.service", "user-service"), true, "")

	// Inventory reserve before the order is placed — checkout
	// fails fast if any item is out of stock so this fan-out is
	// authentic for the topology view.
	invDur := dur(10, 30)
	t.Add("api-gateway", "inventory-service/Reserve", tracepb.Span_SPAN_KIND_CLIENT,
		apiSpan, 16*time.Millisecond, invDur,
		kv("rpc.system", "grpc", "rpc.method", "Reserve",
			"peer.service", "inventory-service"), true, "")
	invSpan := t.Add("inventory-service", "InventoryService.Reserve", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, 18*time.Millisecond, invDur-4*time.Millisecond,
		kv("rpc.system", "grpc", "rpc.method", "Reserve"), true, "")
	t.Add("inventory-service", "db.UPDATE inventory", tracepb.Span_SPAN_KIND_CLIENT,
		invSpan, 20*time.Millisecond, dur(4, 18),
		kv("db.system", "postgresql", "db.name", "inventory",
			"db.statement", "UPDATE inventory SET reserved=reserved+$1 WHERE sku=$2",
			"peer.service", "postgres"), true, "")

	// Order creation
	orderStart := 30 * time.Millisecond
	orderDur := apiDur - 40*time.Millisecond
	orderSpan := t.Add("order-service", "OrderService.Create", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, orderStart, orderDur, kv("order.id", fmt.Sprintf("ord-%d", mrand.IntN(99999)),
			"order.amount", mrand.Float64()*500+10, "peer.service", "order-service"),
		!payFail, ifErr(payFail, "payment service error"))

	// Pre-order fraud screen — order-service asks fraud-service for
	// a quick green-light before persisting the order. fraud-service
	// now has two upstream callers (order-service here, plus the
	// deeper card-side check inside payment-service below).
	t.Add("order-service", "fraud-service/PreScreen", tracepb.Span_SPAN_KIND_CLIENT,
		orderSpan, orderStart+3*time.Millisecond, dur(8, 28),
		kv("rpc.system", "grpc", "rpc.method", "PreScreen",
			"peer.service", "fraud-service"), true, "")
	t.Add("fraud-service", "FraudService.PreScreen", tracepb.Span_SPAN_KIND_SERVER,
		orderSpan, orderStart+4*time.Millisecond, dur(6, 24),
		kv("rpc.system", "grpc", "rpc.method", "PreScreen",
			"fraud.score", mrand.Float64()*0.5), true, "")

	// Final pricing / discount validation. pricing-service has
	// cart-service (live cart total) + order-service (final
	// discount apply) as upstreams now.
	t.Add("order-service", "pricing-service/ApplyDiscounts", tracepb.Span_SPAN_KIND_CLIENT,
		orderSpan, orderStart+4*time.Millisecond, dur(6, 18),
		kv("rpc.system", "grpc", "rpc.method", "ApplyDiscounts",
			"peer.service", "pricing-service"), true, "")
	priceSpan := t.Add("pricing-service", "PricingService.ApplyDiscounts", tracepb.Span_SPAN_KIND_SERVER,
		orderSpan, orderStart+5*time.Millisecond, dur(4, 14),
		kv("rpc.system", "grpc", "rpc.method", "ApplyDiscounts"), true, "")
	t.Add("pricing-service", "redis.GET discounts:active", tracepb.Span_SPAN_KIND_CLIENT,
		priceSpan, orderStart+6*time.Millisecond, dur(1, 3),
		kv("db.system", "redis", "db.operation", "GET", "peer.service", "redis"), true, "")

	// DB insert
	t.Add("order-service", "db.INSERT orders", tracepb.Span_SPAN_KIND_CLIENT,
		orderSpan, orderStart+5*time.Millisecond, dur(40, 120),
		kv("db.system", "postgresql", "db.name", "orders", "db.statement",
			"INSERT INTO orders(user_id, total, status) VALUES($1, $2, $3) RETURNING id",
			"peer.service", "postgres"), true, "")

	// Payment call (the one that may fail)
	payDur := dur(120, 280)
	payStart := orderStart + dur(140, 200)
	t.Add("order-service", "payment-service/Charge", tracepb.Span_SPAN_KIND_CLIENT,
		orderSpan, payStart, payDur, kv("rpc.system", "grpc", "rpc.method", "Charge",
			"peer.service", "payment-service"), !payFail, ifErr(payFail, "stripe gateway timeout"))

	paySpan := t.Add("payment-service", "PaymentService.Charge", tracepb.Span_SPAN_KIND_SERVER,
		orderSpan, payStart+1*time.Millisecond, payDur-2*time.Millisecond,
		kv("payment.provider", "stripe", "payment.amount", mrand.Float64()*500+10,
			"payment.currency", "USD"), !payFail, ifErr(payFail, "stripe: gateway timeout"))

	// Fraud check fans out from payment-service before charging —
	// real flow regardless of whether the charge later succeeds.
	t.Add("payment-service", "fraud-service/Score", tracepb.Span_SPAN_KIND_CLIENT,
		paySpan, payStart+3*time.Millisecond, dur(15, 50),
		kv("rpc.system", "grpc", "rpc.method", "Score",
			"peer.service", "fraud-service"), true, "")
	fraudSpan := t.Add("fraud-service", "FraudService.Score", tracepb.Span_SPAN_KIND_SERVER,
		paySpan, payStart+5*time.Millisecond, dur(12, 45),
		kv("rpc.system", "grpc", "rpc.method", "Score",
			"fraud.score", mrand.Float64()), true, "")
	t.Add("fraud-service", "redis.GET fraud:rules", tracepb.Span_SPAN_KIND_CLIENT,
		fraudSpan, payStart+6*time.Millisecond, dur(1, 4),
		kv("db.system", "redis", "db.operation", "GET", "peer.service", "redis"), true, "")

	if payFail {
		t.Add("payment-service", "stripe.charge", tracepb.Span_SPAN_KIND_CLIENT,
			paySpan, payStart+5*time.Millisecond, payDur-10*time.Millisecond,
			kv("http.method", "POST", "http.url", "https://api.stripe.com/v1/charges",
				"http.status_code", 504, "peer.service", "stripe"), false, "504 Gateway Timeout")
	} else {
		t.Add("payment-service", "stripe.charge", tracepb.Span_SPAN_KIND_CLIENT,
			paySpan, payStart+5*time.Millisecond, payDur-10*time.Millisecond,
			kv("http.method", "POST", "http.url", "https://api.stripe.com/v1/charges",
				"http.status_code", 200, "peer.service", "stripe"), true, "")
		// Shipping label generation (only on payment success)
		shipStart := orderStart + orderDur - 60*time.Millisecond
		shipDur := dur(20, 70)
		t.Add("order-service", "shipping-service/CreateLabel", tracepb.Span_SPAN_KIND_CLIENT,
			orderSpan, shipStart, shipDur,
			kv("rpc.system", "grpc", "rpc.method", "CreateLabel",
				"peer.service", "shipping-service"), true, "")
		shipSpan := t.Add("shipping-service", "ShippingService.CreateLabel", tracepb.Span_SPAN_KIND_SERVER,
			orderSpan, shipStart+2*time.Millisecond, shipDur-4*time.Millisecond,
			kv("rpc.system", "grpc", "rpc.method", "CreateLabel"), true, "")
		t.Add("shipping-service", "fedex.create_shipment", tracepb.Span_SPAN_KIND_CLIENT,
			shipSpan, shipStart+4*time.Millisecond, dur(15, 50),
			kv("http.method", "POST", "http.url", "https://api.fedex.com/ship/v1/shipments",
				"http.status_code", 200, "peer.service", "fedex"), true, "")

		// Kafka publish on success
		kafkaStart := orderStart + orderDur - 30*time.Millisecond
		t.Add("order-service", "kafka.publish order.confirmed", tracepb.Span_SPAN_KIND_PRODUCER,
			orderSpan, kafkaStart, dur(8, 25), kv("messaging.system", "kafka",
				"messaging.destination", "order.confirmed", "peer.service", "kafka"), true, "")
	}
	return t
}

// scenarioSearch: full-text search
func scenarioSearch() *Trace {
	t := NewTrace()
	totalDur := dur(40, 140)
	query := pick("laptop", "headphones", "monitor 4k", "mechanical keyboard", "wireless mouse")
	M.RecordHTTP("api-gateway", "GET", "/api/search", 200, ms(totalDur))
	M.RecordDB("search-service", "elasticsearch", "search", ms(totalDur)*0.7)
	M.RecordBiz("products.searched")

	feSpan := t.Add("frontend", "GET /search", tracepb.Span_SPAN_KIND_CLIENT,
		nil, 0, totalDur, kv("http.method", "GET", "http.url", "/search?q="+query,
			"peer.service", "api-gateway"), true, "")
	apiSpan := t.Add("api-gateway", "GET /api/search", tracepb.Span_SPAN_KIND_SERVER,
		feSpan, 3*time.Millisecond, totalDur-6*time.Millisecond,
		kv("http.method", "GET", "http.route", "/api/search", "http.status_code", 200,
			"search.query", query), true, "")
	searchSpan := t.Add("search-service", "SearchService.Query", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, 8*time.Millisecond, totalDur-20*time.Millisecond,
		kv("rpc.system", "grpc", "rpc.method", "Query", "peer.service", "search-service",
			"search.results", mrand.IntN(50)+1), true, "")
	t.Add("search-service", "elasticsearch.search products", tracepb.Span_SPAN_KIND_CLIENT,
		searchSpan, 12*time.Millisecond, totalDur-30*time.Millisecond,
		kv("db.system", "elasticsearch", "db.statement",
			fmt.Sprintf(`{"query":{"match":{"name":"%s"}}}`, query),
			"peer.service", "elasticsearch"), true, "")
	// Semantic re-ranking: search-service hands the top hits to
	// ml-service for a vector-similarity rerank. ml-service now
	// has two distinct upstream callers (recommendation-service +
	// search-service), exercising the fan-in case in the
	// backtrace view.
	rerankStart := totalDur - 25*time.Millisecond
	rerankDur := dur(8, 25)
	t.Add("search-service", "ml-service/Rerank", tracepb.Span_SPAN_KIND_CLIENT,
		searchSpan, rerankStart, rerankDur,
		kv("rpc.system", "grpc", "rpc.method", "Rerank",
			"peer.service", "ml-service"), true, "")
	mlSpan := t.Add("ml-service", "MLService.Rerank", tracepb.Span_SPAN_KIND_SERVER,
		searchSpan, rerankStart+1*time.Millisecond, rerankDur-2*time.Millisecond,
		kv("rpc.system", "grpc", "rpc.method", "Rerank",
			"ml.model", "rerank-v2", "search.query", query), true, "")
	t.Add("ml-service", "redis.GET emb:query", tracepb.Span_SPAN_KIND_CLIENT,
		mlSpan, rerankStart+3*time.Millisecond, dur(1, 4),
		kv("db.system", "redis", "db.operation", "GET", "peer.service", "redis"), true, "")
	return t
}

// scenarioCart: add item to cart
func scenarioCart() *Trace {
	t := NewTrace()
	totalDur := dur(40, 110)
	M.RecordHTTP("api-gateway", "POST", "/api/cart/items", 201, ms(totalDur))
	M.RecordDB("cart-service", "redis", "HSET", ms(totalDur)*0.3)
	M.RecordBiz("cart.items_added")
	feSpan := t.Add("frontend", "POST /cart/items", tracepb.Span_SPAN_KIND_CLIENT,
		nil, 0, totalDur, kv("http.method", "POST", "http.url", "/cart/items",
			"peer.service", "api-gateway"), true, "")
	apiSpan := t.Add("api-gateway", "POST /api/cart/items", tracepb.Span_SPAN_KIND_SERVER,
		feSpan, 3*time.Millisecond, totalDur-6*time.Millisecond,
		kv("http.method", "POST", "http.route", "/api/cart/items", "http.status_code", 201), true, "")
	cartSpan := t.Add("cart-service", "CartService.AddItem", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, 8*time.Millisecond, totalDur-20*time.Millisecond,
		kv("rpc.system", "grpc", "rpc.method", "AddItem", "peer.service", "cart-service"), true, "")
	t.Add("cart-service", "redis.HSET cart:user", tracepb.Span_SPAN_KIND_CLIENT,
		cartSpan, 12*time.Millisecond, dur(2, 8),
		kv("db.system", "redis", "db.operation", "HSET", "peer.service", "redis"), true, "")
	// Cart now also calls inventory + pricing for the live total —
	// gives the graph two more downstream edges out of cart-service.
	t.Add("cart-service", "inventory-service/CheckStock", tracepb.Span_SPAN_KIND_CLIENT,
		cartSpan, 14*time.Millisecond, dur(6, 18),
		kv("rpc.system", "grpc", "rpc.method", "CheckStock", "peer.service", "inventory-service"),
		true, "")
	invSpan := t.Add("inventory-service", "InventoryService.CheckStock", tracepb.Span_SPAN_KIND_SERVER,
		cartSpan, 16*time.Millisecond, dur(4, 14),
		kv("rpc.system", "grpc", "rpc.method", "CheckStock"), true, "")
	t.Add("inventory-service", "redis.GET stock:sku", tracepb.Span_SPAN_KIND_CLIENT,
		invSpan, 17*time.Millisecond, dur(1, 4),
		kv("db.system", "redis", "db.operation", "GET", "peer.service", "redis"), true, "")
	t.Add("cart-service", "pricing-service/Calculate", tracepb.Span_SPAN_KIND_CLIENT,
		cartSpan, 22*time.Millisecond, dur(4, 12),
		kv("rpc.system", "grpc", "rpc.method", "Calculate", "peer.service", "pricing-service"),
		true, "")
	priceSpan := t.Add("pricing-service", "PricingService.Calculate", tracepb.Span_SPAN_KIND_SERVER,
		cartSpan, 24*time.Millisecond, dur(2, 10),
		kv("rpc.system", "grpc", "rpc.method", "Calculate"), true, "")
	t.Add("pricing-service", "redis.GET prices:sku", tracepb.Span_SPAN_KIND_CLIENT,
		priceSpan, 25*time.Millisecond, dur(1, 3),
		kv("db.system", "redis", "db.operation", "GET", "peer.service", "redis"), true, "")
	return t
}

// scenarioHomePage: parallel fan-out for the home page — frontend
// requests a personalised home view that the api-gateway resolves
// by calling several services concurrently. Exercises a wide
// cross-service edge set (api-gateway → recommendation-service →
// ml-service, api-gateway → product-service → postgres,
// api-gateway → user-service → postgres) so the graph view shows
// real fan-out from a single inbound request.
func scenarioHomePage() *Trace {
	t := NewTrace()
	totalDur := dur(180, 360)
	M.RecordHTTP("api-gateway", "GET", "/api/home", 200, ms(totalDur))
	M.RecordBiz("home.viewed")

	feSpan := t.Add("frontend", "GET /home", tracepb.Span_SPAN_KIND_CLIENT,
		nil, 0, totalDur, kv("http.method", "GET", "http.url", "/home",
			"peer.service", "api-gateway"), true, "")
	apiSpan := t.Add("api-gateway", "GET /api/home", tracepb.Span_SPAN_KIND_SERVER,
		feSpan, 4*time.Millisecond, totalDur-8*time.Millisecond,
		kv("http.method", "GET", "http.route", "/api/home", "http.status_code", 200), true, "")

	// Recommendations branch: api-gateway → recommendation → ml-service
	recDur := dur(80, 180)
	t.Add("api-gateway", "recommendation-service/Personalise", tracepb.Span_SPAN_KIND_CLIENT,
		apiSpan, 10*time.Millisecond, recDur,
		kv("rpc.system", "grpc", "rpc.method", "Personalise",
			"peer.service", "recommendation-service"), true, "")
	recSpan := t.Add("recommendation-service", "RecommendationService.Personalise", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, 12*time.Millisecond, recDur-4*time.Millisecond,
		kv("rpc.system", "grpc", "rpc.method", "Personalise"), true, "")
	t.Add("recommendation-service", "ml-service/Embed", tracepb.Span_SPAN_KIND_CLIENT,
		recSpan, 16*time.Millisecond, dur(40, 110),
		kv("rpc.system", "grpc", "rpc.method", "Embed", "peer.service", "ml-service"),
		true, "")
	mlSpan := t.Add("ml-service", "MLService.Embed", tracepb.Span_SPAN_KIND_SERVER,
		recSpan, 18*time.Millisecond, dur(35, 100),
		kv("rpc.system", "grpc", "rpc.method", "Embed", "ml.model", "embedding-v3"), true, "")
	t.Add("ml-service", "redis.GET emb:user", tracepb.Span_SPAN_KIND_CLIENT,
		mlSpan, 22*time.Millisecond, dur(1, 5),
		kv("db.system", "redis", "db.operation", "GET", "peer.service", "redis"), true, "")

	// Featured products branch (parallel to recommendations)
	prodDur := dur(40, 110)
	t.Add("api-gateway", "product-service/ListFeatured", tracepb.Span_SPAN_KIND_CLIENT,
		apiSpan, 10*time.Millisecond, prodDur,
		kv("rpc.system", "grpc", "rpc.method", "ListFeatured",
			"peer.service", "product-service"), true, "")
	prodSpan := t.Add("product-service", "ProductService.ListFeatured", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, 12*time.Millisecond, prodDur-4*time.Millisecond,
		kv("rpc.system", "grpc", "rpc.method", "ListFeatured"), true, "")
	t.Add("product-service", "db.SELECT products WHERE featured", tracepb.Span_SPAN_KIND_CLIENT,
		prodSpan, 16*time.Millisecond, dur(20, 80),
		kv("db.system", "postgresql", "db.name", "shop", "db.statement",
			"SELECT id, name, price FROM products WHERE featured=true LIMIT 12",
			"peer.service", "postgres"), true, "")

	// User profile branch (parallel)
	userDur := dur(20, 60)
	t.Add("api-gateway", "user-service/GetProfile", tracepb.Span_SPAN_KIND_CLIENT,
		apiSpan, 10*time.Millisecond, userDur,
		kv("rpc.system", "grpc", "rpc.method", "GetProfile",
			"peer.service", "user-service"), true, "")
	userSpan := t.Add("user-service", "UserService.GetProfile", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, 12*time.Millisecond, userDur-4*time.Millisecond,
		kv("rpc.system", "grpc", "rpc.method", "GetProfile"), true, "")
	t.Add("user-service", "db.SELECT users", tracepb.Span_SPAN_KIND_CLIENT,
		userSpan, 14*time.Millisecond, dur(8, 30),
		kv("db.system", "postgresql", "db.name", "auth", "db.statement",
			"SELECT id, email, prefs FROM users WHERE id=$1",
			"peer.service", "postgres"), true, "")
	return t
}

// scenarioProductDetail: product page with reviews + live stock —
// product-service fans out to review-service (mongodb) and
// inventory-service (redis). Adds two more services into the
// product-service neighbourhood.
func scenarioProductDetail() *Trace {
	t := NewTrace()
	totalDur := dur(120, 280)
	productID := fmt.Sprintf("prod-%d", mrand.IntN(9999)+1)
	M.RecordHTTP("api-gateway", "GET", "/api/products/{id}", 200, ms(totalDur))

	feSpan := t.Add("frontend", "GET /products/"+productID, tracepb.Span_SPAN_KIND_CLIENT,
		nil, 0, totalDur, kv("http.method", "GET", "http.url", "/products/"+productID,
			"peer.service", "api-gateway"), true, "")
	apiSpan := t.Add("api-gateway", "GET /api/products/{id}", tracepb.Span_SPAN_KIND_SERVER,
		feSpan, 3*time.Millisecond, totalDur-6*time.Millisecond,
		kv("http.method", "GET", "http.route", "/api/products/{id}",
			"http.status_code", 200, "product.id", productID), true, "")
	prodSpan := t.Add("product-service", "ProductService.Get", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, 8*time.Millisecond, totalDur-20*time.Millisecond,
		kv("rpc.system", "grpc", "rpc.method", "Get", "product.id", productID), true, "")
	t.Add("product-service", "db.SELECT product", tracepb.Span_SPAN_KIND_CLIENT,
		prodSpan, 12*time.Millisecond, dur(8, 40),
		kv("db.system", "postgresql", "db.name", "shop", "db.statement",
			"SELECT * FROM products WHERE id=$1", "peer.service", "postgres"), true, "")
	// Reviews fan-out
	t.Add("product-service", "review-service/List", tracepb.Span_SPAN_KIND_CLIENT,
		prodSpan, 18*time.Millisecond, dur(20, 80),
		kv("rpc.system", "grpc", "rpc.method", "List", "peer.service", "review-service"),
		true, "")
	revSpan := t.Add("review-service", "ReviewService.List", tracepb.Span_SPAN_KIND_SERVER,
		prodSpan, 20*time.Millisecond, dur(18, 70),
		kv("rpc.system", "grpc", "rpc.method", "List"), true, "")
	t.Add("review-service", "mongodb.find reviews", tracepb.Span_SPAN_KIND_CLIENT,
		revSpan, 22*time.Millisecond, dur(15, 60),
		kv("db.system", "mongodb", "db.operation", "find", "db.collection", "reviews",
			"peer.service", "mongodb"), true, "")
	// Stock fan-out
	t.Add("product-service", "inventory-service/CheckStock", tracepb.Span_SPAN_KIND_CLIENT,
		prodSpan, 22*time.Millisecond, dur(4, 16),
		kv("rpc.system", "grpc", "rpc.method", "CheckStock",
			"peer.service", "inventory-service"), true, "")
	invSpan := t.Add("inventory-service", "InventoryService.CheckStock", tracepb.Span_SPAN_KIND_SERVER,
		prodSpan, 24*time.Millisecond, dur(2, 12),
		kv("rpc.system", "grpc", "rpc.method", "CheckStock"), true, "")
	t.Add("inventory-service", "redis.GET stock:sku", tracepb.Span_SPAN_KIND_CLIENT,
		invSpan, 25*time.Millisecond, dur(1, 4),
		kv("db.system", "redis", "db.operation", "GET", "peer.service", "redis"), true, "")
	// Related-products fan-out — recommendation-service now has a
	// second upstream (api-gateway from the home page + product-
	// service from the product detail page), so the consumer view
	// for recommendation-service shows real fan-in.
	relStart := 24 * time.Millisecond
	relDur := dur(20, 60)
	t.Add("product-service", "recommendation-service/RelatedProducts", tracepb.Span_SPAN_KIND_CLIENT,
		prodSpan, relStart, relDur,
		kv("rpc.system", "grpc", "rpc.method", "RelatedProducts",
			"peer.service", "recommendation-service"), true, "")
	relSpan := t.Add("recommendation-service", "RecommendationService.RelatedProducts", tracepb.Span_SPAN_KIND_SERVER,
		prodSpan, relStart+2*time.Millisecond, relDur-4*time.Millisecond,
		kv("rpc.system", "grpc", "rpc.method", "RelatedProducts",
			"product.id", productID), true, "")
	t.Add("recommendation-service", "redis.GET related:product", tracepb.Span_SPAN_KIND_CLIENT,
		relSpan, relStart+4*time.Millisecond, dur(1, 5),
		kv("db.system", "redis", "db.operation", "GET", "peer.service", "redis"), true, "")
	return t
}

// scenarioOrderEvent: event-driven side. Synthesises an
// order.confirmed Kafka event being consumed by notification +
// analytics + audit services; notification then fans out to email
// and sms. Roots itself at a kafka consumer span (no parent) so
// the trace is initiated by the broker, not the user — exercises
// the producer / consumer kind handling and gives the graph view
// a downstream tree from kafka outward.
func scenarioOrderEvent() *Trace {
	t := NewTrace()
	totalDur := dur(120, 280)
	M.RecordBiz("kafka.events_consumed")

	notifSpan := t.Add("notification-service", "kafka.consume order.confirmed", tracepb.Span_SPAN_KIND_CONSUMER,
		nil, 0, totalDur, kv("messaging.system", "kafka",
			"messaging.destination", "order.confirmed",
			"messaging.operation", "receive", "peer.service", "kafka"), true, "")

	// Email branch
	emailDur := dur(40, 120)
	t.Add("notification-service", "email-service/Send", tracepb.Span_SPAN_KIND_CLIENT,
		notifSpan, 8*time.Millisecond, emailDur,
		kv("rpc.system", "grpc", "rpc.method", "Send", "peer.service", "email-service"),
		true, "")
	emailSpan := t.Add("email-service", "EmailService.Send", tracepb.Span_SPAN_KIND_SERVER,
		notifSpan, 10*time.Millisecond, emailDur-4*time.Millisecond,
		kv("rpc.system", "grpc", "rpc.method", "Send", "email.template", "order.confirmation"),
		true, "")
	t.Add("email-service", "sendgrid.send", tracepb.Span_SPAN_KIND_CLIENT,
		emailSpan, 14*time.Millisecond, dur(30, 90),
		kv("http.method", "POST", "http.url", "https://api.sendgrid.com/v3/mail/send",
			"http.status_code", 202, "peer.service", "sendgrid"), true, "")

	// SMS branch (parallel)
	smsFail := mrand.IntN(100) < 4
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

	// Analytics consumer (parallel root in real life — modelled
	// here as a sibling so the trace stays a single tree).
	analyticsDur := dur(20, 80)
	analSpan := t.Add("analytics-service", "kafka.consume order.confirmed", tracepb.Span_SPAN_KIND_CONSUMER,
		notifSpan, 4*time.Millisecond, analyticsDur,
		kv("messaging.system", "kafka", "messaging.destination", "order.confirmed",
			"messaging.operation", "receive"), true, "")
	t.Add("analytics-service", "elasticsearch.index events", tracepb.Span_SPAN_KIND_CLIENT,
		analSpan, 6*time.Millisecond, dur(15, 70),
		kv("db.system", "elasticsearch", "db.operation", "index",
			"peer.service", "elasticsearch"), true, "")

	// Audit consumer
	auditSpan := t.Add("audit-service", "kafka.consume order.confirmed", tracepb.Span_SPAN_KIND_CONSUMER,
		notifSpan, 4*time.Millisecond, dur(20, 70),
		kv("messaging.system", "kafka", "messaging.destination", "order.confirmed"), true, "")
	t.Add("audit-service", "db.INSERT audit_log", tracepb.Span_SPAN_KIND_CLIENT,
		auditSpan, 6*time.Millisecond, dur(8, 30),
		kv("db.system", "postgresql", "db.name", "audit",
			"db.statement", "INSERT INTO audit_log(event, payload) VALUES($1, $2)",
			"peer.service", "postgres"), true, "")
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

type httpKey struct{ service, method, route string; status int }
type dbKey struct{ service, system, op string }

type metricsState struct {
	mu sync.Mutex
	// HTTP RED: requests, errors, duration histograms keyed by (service, method, route, status)
	httpRequests map[httpKey]uint64        // counter (delta)
	httpDuration map[httpKey]*histogramAgg // histogram (delta)
	// Business counters
	bizCounters map[string]uint64 // metric name → delta count
	// DB query counts
	dbQueries map[dbKey]uint64
	dbDuration map[dbKey]*histogramAgg
	// Cumulative totals (for monotonic Sum metrics)
	cumRequests map[httpKey]uint64
	cumBiz      map[string]uint64
	cumDBQ      map[dbKey]uint64
}

type histogramAgg struct {
	count uint64
	sum   float64
	min   float64
	max   float64
}

func (h *histogramAgg) record(v float64) {
	if h.count == 0 || v < h.min { h.min = v }
	if v > h.max { h.max = v }
	h.count++
	h.sum += v
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
	if h == nil { h = &histogramAgg{}; m.httpDuration[k] = h }
	h.record(durMs)
}

func (m *metricsState) RecordDB(service, system, op string, durMs float64) {
	k := dbKey{service, system, op}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dbQueries[k]++
	m.cumDBQ[k]++
	h := m.dbDuration[k]
	if h == nil { h = &histogramAgg{}; m.dbDuration[k] = h }
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
			Value: &metricspb.NumberDataPoint_AsInt{AsInt: int64(m.cumDBQ[k])},
			Attributes: []*commonpb.KeyValue{kvStr("db.system", k.system), kvStr("db.operation", k.op)},
		})
	}
	for k, h := range m.dbDuration {
		sum := h.sum; mn, mx := h.min, h.max
		dbDurByService[k.service] = append(dbDurByService[k.service], &metricspb.HistogramDataPoint{
			StartTimeUnixNano: startNs, TimeUnixNano: nowNs,
			Count: h.count, Sum: &sum, Min: &mn, Max: &mx,
			Attributes: []*commonpb.KeyValue{kvStr("db.system", k.system), kvStr("db.operation", k.op)},
		})
	}
	for svc, dps := range dbReqByService {
		addMetric(svc, &metricspb.Metric{
			Name: "db.client.queries", Unit: "1", Description: "Total DB client queries",
			Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
				IsMonotonic: true,
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				DataPoints: dps,
			}},
		})
	}
	for svc, dps := range dbDurByService {
		addMetric(svc, &metricspb.Metric{
			Name: "db.client.duration", Unit: "ms", Description: "DB client query duration",
			Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
				DataPoints: dps,
			}},
		})
	}

	// ── Business counters (orders, payments, cart) ───────────────────────────
	bizMap := map[string]string{
		"orders.created":      "order-service",
		"orders.failed":       "order-service",
		"payments.processed":  "payment-service",
		"payments.failed":     "payment-service",
		"cart.items_added":    "cart-service",
		"users.logged_in":     "user-service",
		"users.login_failed":  "user-service",
		"products.searched":   "search-service",
		"kafka.messages_published": "order-service",
	}
	for name, svc := range bizMap {
		v, ok := m.cumBiz[name]
		if !ok { continue }
		addMetric(svc, &metricspb.Metric{
			Name: "demo." + name, Unit: "1",
			Description: "Demo business counter: " + name,
			Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
				IsMonotonic: true,
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				DataPoints: []*metricspb.NumberDataPoint{{
					StartTimeUnixNano: startNs, TimeUnixNano: nowNs,
					Value: &metricspb.NumberDataPoint_AsInt{AsInt: int64(v)},
				}},
			}},
		})
	}

	// ── System / runtime gauges (per backend service) ─────────────────────────
	for svcKey := range services {
		if svcKey == "frontend" { continue }
		addMetric(svcKey, &metricspb.Metric{
			Name: "process.runtime.memory.rss", Unit: "By",
			Description: "Process resident memory size",
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
				DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: nowNs,
					Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: float64(280_000_000 + mrand.IntN(180_000_000))},
				}},
			}},
		})
		addMetric(svcKey, &metricspb.Metric{
			Name: "process.runtime.cpu.utilization", Unit: "1",
			Description: "Process CPU utilization",
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
				DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: nowNs,
					Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 0.05 + mrand.Float64()*0.45},
				}},
			}},
		})
		addMetric(svcKey, &metricspb.Metric{
			Name: "process.runtime.goroutines", Unit: "1",
			Description: "Number of goroutines",
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
				DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: nowNs,
					Value: &metricspb.NumberDataPoint_AsInt{AsInt: int64(20 + mrand.IntN(180))},
				}},
			}},
		})
		addMetric(svcKey, &metricspb.Metric{
			Name: "http.server.active_requests", Unit: "1",
			Description: "Currently active HTTP server requests",
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
				DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: nowNs,
					Value: &metricspb.NumberDataPoint_AsInt{AsInt: int64(2 + mrand.IntN(40))},
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
				kvStr("deployment.environment", "demo"),
			}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Scope:   &commonpb.InstrumentationScope{Name: "coremetry-demo"},
				Metrics: mts,
			}},
		})
	}
	return rms
}

func sendMetrics(startNs uint64) uint64 {
	now := uint64(time.Now().UnixNano())
	rms := M.flush(startNs, now)
	if len(rms) == 0 { return now }
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
	{"BrowseProducts", 4, scenarioBrowseProducts},
	{"HomePage",       4, scenarioHomePage},
	{"ProductDetail",  4, scenarioProductDetail},
	{"Search",         3, scenarioSearch},
	{"AddToCart",      3, scenarioCart},
	{"Login",          2, scenarioUserLogin},
	{"Checkout",       2, scenarioCheckout},
	{"OrderEvent",     2, scenarioOrderEvent},
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
	log.Printf("Coremetry demo")
	log.Printf("  endpoint:         %s (traces/logs/metrics)", *endpoint)
	log.Printf("  profile endpoint: %s", *profileEndpoint)
	log.Printf("  rate:             %.1f scenarios/sec", *rps)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var wg sync.WaitGroup

	// ── Continuous CPU profiling ─────────────────────────────────────────────
	// Capture a 5-second CPU profile every 10 seconds and push it.
	wg.Add(1)
	go runProfileLoop(ctx, &wg)

	// Trace generator
	wg.Add(1)
	go func() {
		defer wg.Done()
		interval := time.Duration(float64(time.Second) / *rps)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		count := 0
		var mu sync.Mutex

		// counter logger
		go func() {
			for range time.Tick(10 * time.Second) {
				mu.Lock()
				log.Printf("[stats] sent %d traces", count)
				mu.Unlock()
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				name, fn := pickScenario()
				t := fn()
				if err := t.Send(); err != nil {
					log.Printf("[trace] send error: %v", err)
					continue
				}
				// Send a log for some traces
				if mrand.IntN(3) == 0 {
					sendScenarioLog(name, t)
				}
				mu.Lock()
				count++
				mu.Unlock()
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
		body := fmt.Sprintf("%s failed: %s", scenarioName, target.span.Status.Message)
		if mrand.IntN(100) < 40 {
			body = pickAnomalyLine(target.service)
		}
		sendLog(target.service, 17, "ERROR", body,
			t.traceID, target.span.SpanId, kv("error.type", "OperationFailed"))
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
var anomalyLines = []string{
	"ORA-00060: deadlock detected while waiting for resource",
	"ORA-01017: invalid username/password; logon denied",
	"ORA-12541: TNS:no listener — connection to oracle-prod refused",
	"java.lang.NullPointerException: Cannot invoke method on null reference at com.shop.UserService.lookup(UserService.java:142)",
	"java.lang.OutOfMemoryError: Java heap space — heap dump written to /var/log/heapdump-1715.hprof",
	"OOMKilled: container exceeded memory limit (1Gi), restarting",
	"deadlock detected on relation orders: process 12345 waits for ShareLock; killed",
	"ECONNREFUSED: connection refused to redis-prod-2:6379 — circuit breaker open",
	"x509: certificate has expired or is not yet valid: current time 2026-05-09 is after 2026-05-08T00:00:00Z",
	"tls: handshake failure with payment-gateway: unsupported cipher suite",
	"panic: runtime error: index out of range [5] with length 3 — goroutine 142 stack",
	"context deadline exceeded — upstream user-service did not respond within 5s",
	"401 Unauthorized — invalid credentials for tenant=acme route=/api/orders",
	"no space left on device: cannot write to /var/lib/postgresql/wal — disk full",
	"java.lang.IllegalStateException: cannot complete transaction — already committed",
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
	const profileSvc = "order-service"

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

func dur(minMs, maxMs int) time.Duration {
	return time.Duration(minMs+mrand.IntN(maxMs-minMs)) * time.Millisecond
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

