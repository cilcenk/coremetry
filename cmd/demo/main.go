// Qmetry demo: realistic e-commerce traffic generator that emits
// OTLP traces, logs, and metrics over HTTP to Qmetry.
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
	profileEndpoint = flag.String("profile-endpoint", "", "Qmetry profile ingest endpoint (defaults to -endpoint with /v1/profiles, NOT collector!)")
	rps             = flag.Float64("rps", 2.0, "Scenarios per second to generate")
)

var httpClient = &http.Client{Timeout: 5 * time.Second}

// ─── Service definitions ──────────────────────────────────────────────────────

type Service struct{ Name, Host string }

var services = map[string]Service{
	"frontend":        {"frontend", "web-prod-1"},
	"api-gateway":     {"api-gateway", "gw-prod-1"},
	"user-service":    {"user-service", "users-prod-2"},
	"order-service":   {"order-service", "orders-prod-1"},
	"payment-service": {"payment-service", "pay-prod-1"},
	"product-service": {"product-service", "products-prod-1"},
	"cart-service":    {"cart-service", "cart-prod-1"},
	"search-service":  {"search-service", "search-prod-1"},
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
}

func NewTrace() *Trace {
	return &Trace{traceID: randID(16), t0: time.Now()}
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
	byService := make(map[string][]*tracepb.Span)
	for _, si := range t.spans {
		byService[si.service] = append(byService[si.service], si.span)
	}
	var rs []*tracepb.ResourceSpans
	for svcKey, spans := range byService {
		s, ok := services[svcKey]
		if !ok {
			s = Service{Name: svcKey, Host: svcKey + "-1"}
		}
		rs = append(rs, &tracepb.ResourceSpans{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				kvStr("service.name", s.Name),
				kvStr("host.name", s.Host),
				kvStr("deployment.environment", "demo"),
				kvStr("service.version", "1.0.0"),
			}},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "qmetry-demo"},
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

	// Order creation
	orderStart := 30 * time.Millisecond
	orderDur := apiDur - 40*time.Millisecond
	orderSpan := t.Add("order-service", "OrderService.Create", tracepb.Span_SPAN_KIND_SERVER,
		apiSpan, orderStart, orderDur, kv("order.id", fmt.Sprintf("ord-%d", mrand.IntN(99999)),
			"order.amount", mrand.Float64()*500+10, "peer.service", "order-service"),
		!payFail, ifErr(payFail, "payment service error"))

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
		s = Service{Name: service, Host: service + "-1"}
	}
	req := &logscollpb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			kvStr("service.name", s.Name), kvStr("host.name", s.Host),
		}},
		ScopeLogs: []*logspb.ScopeLogs{{
			Scope:      &commonpb.InstrumentationScope{Name: "qmetry-demo"},
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
		if !ok { s = Service{Name: svcKey, Host: svcKey + "-1"} }
		rms = append(rms, &metricspb.ResourceMetrics{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				kvStr("service.name", s.Name), kvStr("host.name", s.Host),
				kvStr("deployment.environment", "demo"),
			}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Scope:   &commonpb.InstrumentationScope{Name: "qmetry-demo"},
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
	{"BrowseProducts", 5, scenarioBrowseProducts},
	{"Search", 4, scenarioSearch},
	{"AddToCart", 3, scenarioCart},
	{"Login", 2, scenarioUserLogin},
	{"Checkout", 1, scenarioCheckout},
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
		// Default to qmetry directly (collector doesn't speak our pprof protocol).
		*profileEndpoint = "http://qmetry:8088"
	}
	log.Printf("Qmetry demo")
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
		sendLog(target.service, 17, "ERROR",
			fmt.Sprintf("%s failed: %s", scenarioName, target.span.Status.Message),
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
	req.Header.Set("X-Qmetry-Service", service)
	req.Header.Set("X-Qmetry-Host", host)
	req.Header.Set("X-Qmetry-Profile-Type", ptype)
	req.Header.Set("X-Qmetry-Start-Time-Ns", fmt.Sprintf("%d", startNs))
	req.Header.Set("X-Qmetry-Duration-Ns", fmt.Sprintf("%d", durNs))
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

