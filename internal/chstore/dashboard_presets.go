package chstore

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
)

// Preset dashboards seeded into a fresh install — practical
// APM bundle (workload / runtime / store-specific) instead of
// the previous SRE-methodology bundle (Golden Signals / USE /
// RED). The bundle is intentionally pragmatic: each dashboard
// answers a "what is X doing" question for a specific tier
// (APM overview, HTTP services, databases, JVM, Kafka, etc.)
// rather than an SRE-framework question (Golden Signals, USE,
// RED). Operators land on the dashboard whose name matches
// the layer they're investigating.
//
// Versioning: the seeded set carries an opaque version string
// stored in system_settings under "preset_dashboards_version".
// On each boot we compare; if it doesn't match, we wipe rows
// whose ID starts with "preset-" (old + current bundle) and
// re-seed the new set, then write the new version. Bumping
// presetVersion forces a re-seed.
//
// User-created dashboards (any ID without the preset- prefix)
// are never touched.
//
// v0.9.564 — bu yorumun ikinci yarısı YANLIŞTI ve düzeltildi.
// Eskiden şöyle diyordu: "admin bir preseti yeniden adlandırdıysa
// yeniden adlandırması korunur çünkü kopyası yeni ID altında yaşar."
// Öyle bir kopya YOK: frontend kaydederken aynı `preset-` ID'sine
// yazıyor (Dashboard.tsx → api.updateDashboard(id, …)), dolayısıyla
// düzenleme preset satırının ÜSTÜNDE yaşıyor ve paket yükseltmesinde
// DELETE ile birlikte gidiyor.
//
// Yani bugünkü gerçek davranış: presetVersion bumplanırsa operatörün
// preset dashboard'lara yaptığı düzenlemeler KAYBOLUR. Yeni bir paket
// göndermeden önce bunun bilinçli olarak ele alınması gerekir —
// yanlış bir yorumun arkasına saklanmamalı.

const presetVersion = "apm-v1"

func (s *Store) SeedPresetDashboards(ctx context.Context) error {
	// v0.9.564 — HATA YUTULMUYOR.
	//
	// Öncesi `storedVersion, _ := s.GetSetting(...)` idi ve bu, boot
	// anında canlı bir VERİ KAYBI yoluydu. GetSetting'in sözleşmesi
	// (settings.go:19-30) kayıt-yok için (nil,nil), GERÇEK hata için
	// (nil,err) döner — ama `_` ikisini de aynı yere indiriyordu:
	// current = "".
	//
	// Sonuç: presetVersion hiç değişmemiş olsa bile, boot sırasında
	// geçici bir ClickHouse arızası current="" üretiyor, o da
	// presetVersion'a eşit olmadığı için aşağıdaki DELETE dalına
	// giriliyor ve TÜM preset- satırları siliniyordu. Operatörün o
	// dashboard'lara yaptığı düzenlemeler, kimse bir şey bumplamadan,
	// bir CH hıçkırığında yok oluyordu.
	//
	// Kural: sürümü OKUYAMIYORSAK yıkıcı yola GİRME. Bilmemek,
	// "eşleşmiyor" demek değildir.
	storedVersion, err := s.GetSetting(ctx, "preset_dashboards_version")
	if err != nil {
		return fmt.Errorf("read preset dashboards version: %w", err)
	}
	current := string(storedVersion)

	row := s.conn.QueryRow(ctx, `SELECT count() FROM dashboards FINAL`)
	var n uint64
	if err := row.Scan(&n); err != nil {
		return fmt.Errorf("count dashboards: %w", err)
	}
	if n == 0 {
		return s.seedAndStamp(ctx)
	}
	if current == presetVersion {
		return nil
	}
	// Bundle change → drop old preset-* rows so the new bundle
	// takes their tiles cleanly.
	if err := s.conn.Exec(ctx, `ALTER TABLE dashboards DELETE WHERE id LIKE 'preset-%'`); err != nil {
		return fmt.Errorf("delete old preset dashboards: %w", err)
	}
	return s.seedAndStamp(ctx)
}

func (s *Store) seedAndStamp(ctx context.Context) error {
	for _, d := range presetDashboards() {
		if err := s.UpsertDashboard(ctx, d); err != nil {
			return fmt.Errorf("seed dashboard %s: %w", d.ID, err)
		}
		log.Printf("[chstore] seeded preset dashboard %q", d.Name)
	}
	if err := s.PutSetting(ctx, "preset_dashboards_version", []byte(presetVersion)); err != nil {
		return fmt.Errorf("stamp preset version: %w", err)
	}
	return nil
}

// ── Preset bundle ───────────────────────────────────────────────────────────

func presetDashboards() []Dashboard {
	return []Dashboard{
		presetAPMOverview(),
		presetServicePerformance(),
		presetHTTPServices(),
		presetDatabase(),
		presetJavaJVM(),
		presetNodeJS(),
		presetGoRuntime(),
		presetKafkaMessaging(),
		presetErrorsExceptions(),
	}
}

// Panel construction helpers — keep the JSON shape close to the
// frontend Panel type (id, type, title, width, config). Width is
// the 4-col grid factor: 1=quarter, 2=half, 3=three-quarters,
// 4=full.

type panel struct {
	ID     string `json:"id"`
	Type   string `json:"type"`   // metric | spanmetric | stat | markdown | row
	Title  string `json:"title"`
	Width  int    `json:"width"`
	Config any    `json:"config"`
}

// row builds a Grafana-style row marker — full-width "header"
// panel that the dashboard renderer treats as a collapsible
// group separator.
func row(id, title string) panel {
	return panel{ID: id, Type: "row", Title: title, Width: 4, Config: map[string]any{}}
}

type spanCfg struct {
	Agg     string `json:"agg"`
	Field   string `json:"field,omitempty"`
	GroupBy string `json:"groupBy,omitempty"`
	DSL     string `json:"dsl,omitempty"`
	Filters string `json:"filters,omitempty"`
	Step    int    `json:"step,omitempty"`
}

// metricCfg backs metric-panel queries against the metric_points
// table — used by the Infrastructure dashboard for CPU/memory/
// heap timeseries.
type metricCfg struct {
	MetricName string `json:"metricName"`
	Service    string `json:"service,omitempty"`
	Agg        string `json:"agg,omitempty"`
	GroupBy    string `json:"groupBy,omitempty"`
	Step       int    `json:"step,omitempty"`
	Filters    string `json:"filters,omitempty"`
}

type statCfg struct {
	Source   string     `json:"source"` // "spanmetric" or "metric"
	Span     *spanCfg   `json:"span,omitempty"`
	Metric   *metricCfg `json:"metric,omitempty"`
	Unit     string     `json:"unit,omitempty"`
	Decimals int        `json:"decimals,omitempty"`
}

type mdCfg struct {
	Text string `json:"text"`
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("preset dashboard JSON: %v", err))
	}
	return b
}

func dash(id, name, desc string, panels []panel, vars ...dashVar) Dashboard {
	return Dashboard{
		ID:          id,
		Name:        name,
		Description: desc,
		Panels:      mustJSON(panels),
		Variables:   mustJSON(vars),
	}
}

type dashVar struct {
	Name         string   `json:"name"`
	Label        string   `json:"label,omitempty"`
	Type         string   `json:"type"`
	Options      []string `json:"options,omitempty"`
	DefaultValue string   `json:"defaultValue,omitempty"`
}

// stat is a tiny helper so the kpi tiles aren't a wall of struct
// literals. Default Source is spanmetric; for metric-based stats
// use statMetric below.
func stat(id, title, agg string, opts ...statOpt) panel {
	cfg := statCfg{Source: "spanmetric", Decimals: 1, Span: &spanCfg{Agg: agg}}
	for _, o := range opts {
		o(&cfg)
	}
	return panel{ID: id, Type: "stat", Title: title, Width: 1, Config: cfg}
}

type statOpt func(*statCfg)

func unit(u string) statOpt           { return func(c *statCfg) { c.Unit = u } }
func decimals(n int) statOpt          { return func(c *statCfg) { c.Decimals = n } }
func dsl(s string) statOpt            { return func(c *statCfg) { c.Span.DSL = s } }
func field(f string) statOpt          { return func(c *statCfg) { c.Span.Field = f } }
func groupByOpt(gb string) statOpt    { return func(c *statCfg) { c.Span.GroupBy = gb } } //nolint:unused // reserved
func filtersOpt(f string) statOpt     { return func(c *statCfg) { c.Span.Filters = f } } //nolint:unused // reserved

// line is the time-series spanmetric panel.
func line(id, title string, w int, cfg spanCfg) panel {
	return panel{ID: id, Type: "spanmetric", Title: title, Width: w, Config: cfg}
}

// metric is the time-series metric_points panel — for CPU,
// memory, heap timeseries on the Infrastructure dashboard.
func metric(id, title string, w int, cfg metricCfg) panel {
	return panel{ID: id, Type: "metric", Title: title, Width: w, Config: cfg}
}

func md(id, text string) panel {
	return panel{ID: id, Type: "markdown", Title: "", Width: 4, Config: mdCfg{Text: text}}
}

// ── 1. APM Overview ─────────────────────────────────────────────────────────
//
// Datadog APM "Services" landing-page equivalent — top-level
// pulse for the whole application. Headline KPIs, throughput
// + error breakdown by service, P50/P99 trends. The dashboard
// the operator opens first to establish "is the app healthy
// right now"; everything else drills deeper into a tier.
func presetAPMOverview() Dashboard {
	return dash(
		"preset-apm-overview",
		"APM Overview",
		"Top-level pulse for the application — headline KPIs, throughput and errors by service, latency trends. Open this first; if a panel looks off, drill into the matching tier dashboard (HTTP / Database / JVM / Kafka).",
		[]panel{
			md("intro",
				"**APM Overview.** The 30-second health check across the whole app. "+
					"Hover any chart for service-level detail; click a service in the "+
					"[Services list](/services) for its full RED dashboard. Tier "+
					"drill-downs: "+
					"[HTTP Services](/dashboards), "+
					"[Database Performance](/dashboards), "+
					"[Java / JVM](/dashboards), "+
					"[Kafka / Messaging](/dashboards), "+
					"[Errors & Exceptions](/dashboards)."),

			row("row-kpi", "Headline numbers"),
			stat("k-rps", "Requests/sec", "rate", unit("rps"), decimals(1)),
			stat("k-err", "Error rate", "error_rate", unit("%"), decimals(2)),
			stat("k-p95", "P95 latency", "p95", field("duration_ms"), unit("ms"), decimals(0)),
			stat("k-p99", "P99 latency", "p99", field("duration_ms"), unit("ms"), decimals(0)),

			row("row-traffic", "Throughput"),
			line("rps-svc", "RPS by service", 4,
				spanCfg{Agg: "rate", GroupBy: "service.name"}),

			row("row-errors", "Errors"),
			line("err-rate-svc", "Error rate (%) by service", 2,
				spanCfg{Agg: "error_rate", GroupBy: "service.name"}),
			line("err-cnt-svc", "Errors/sec by service", 2,
				spanCfg{Agg: "errors", GroupBy: "service.name"}),

			row("row-latency", "Latency"),
			line("p50-svc", "P50 by service", 2,
				spanCfg{Agg: "p50", Field: "duration_ms", GroupBy: "service.name"}),
			line("p99-svc", "P99 by service", 2,
				spanCfg{Agg: "p99", Field: "duration_ms", GroupBy: "service.name"}),

			row("row-slow-ops", "Slowest operations across the app"),
			line("p99-op", "P99 by operation", 4,
				spanCfg{Agg: "p99", Field: "duration_ms", GroupBy: "name"}),
		},
	)
}


// ── 2. Service Performance ──────────────────────────────────────────────────
//
// Per-service RED + saturation, scoped via the $service
// variable. Pick a service from the dropdown; every panel
// scopes to it. The single dashboard for an oncall
// investigating one specific service end-to-end.
func presetServicePerformance() Dashboard {
	const svc = `service.name = "${service}"`
	return dash(
		"preset-service-performance",
		"Service Performance",
		"Per-service RED (Rate / Errors / Duration) plus runtime saturation, scoped via $service. The dashboard for an oncall investigating one specific service end-to-end.",
		[]panel{
			md("intro",
				"**Pick a service** from `$service`. Every panel scopes to it. "+
					"For cross-service comparisons go back to **APM Overview**. "+
					"To zoom into a specific operation, click its line in the "+
					"per-operation panels — the legend supports click-to-isolate."),

			row("row-kpi", "Service KPIs"),
			stat("k-rps", "RPS", "rate", unit("rps"), decimals(2), dsl(svc)),
			stat("k-err", "Error rate", "error_rate", unit("%"), decimals(2), dsl(svc)),
			stat("k-p95", "P95", "p95", field("duration_ms"), unit("ms"), decimals(0), dsl(svc)),
			stat("k-p99", "P99", "p99", field("duration_ms"), unit("ms"), decimals(0), dsl(svc)),

			row("row-rate", "Rate"),
			line("rps-op", "RPS by operation", 4,
				spanCfg{Agg: "rate", DSL: svc, GroupBy: "name"}),

			row("row-errors", "Errors"),
			line("err-op", "Error rate (%) by operation", 2,
				spanCfg{Agg: "error_rate", DSL: svc, GroupBy: "name"}),
			line("err-cnt-op", "Errors/sec by operation", 2,
				spanCfg{Agg: "errors", DSL: svc, GroupBy: "name"}),

			row("row-duration", "Duration"),
			line("p50-op", "P50 by operation", 2,
				spanCfg{Agg: "p50", Field: "duration_ms", DSL: svc, GroupBy: "name"}),
			line("p99-op", "P99 by operation", 2,
				spanCfg{Agg: "p99", Field: "duration_ms", DSL: svc, GroupBy: "name"}),

			row("row-saturation", "Runtime"),
			metric("cpu", "CPU utilisation", 2,
				metricCfg{MetricName: "process.runtime.cpu.utilization", Service: "${service}"}),
			metric("mem", "Memory RSS", 2,
				metricCfg{MetricName: "process.runtime.memory.rss", Service: "${service}"}),
		},
		dashVar{Name: "service", Label: "Service", Type: "service"},
	)
}

// ── 3. HTTP Services ────────────────────────────────────────────────────────
//
// Web tier — RED for the HTTP layer. Filters on `http.method`
// being set so only inbound HTTP work shows. Routes / status
// codes / methods are the dimensions an HTTP-facing operator
// thinks in.
func presetHTTPServices() Dashboard {
	const httpAny = `http.method != ""`
	const httpErr = `http.method != "" AND status_code = "error"`
	return dash(
		"preset-http-services",
		"HTTP Services",
		"Web tier RED (Rate / Errors / Duration) filtered to HTTP spans. Per-route, per-status-code, per-method breakdown for the inbound API surface.",
		[]panel{
			md("intro",
				"**HTTP layer.** Every span with `http.method` set. The status-code "+
					"and route panels are the fastest way to see whether errors "+
					"concentrate on a specific endpoint or a 5xx storm. Cross-"+
					"reference with [Traces](/traces) filtered by `http.status_code:5*` "+
					"to find sample failed requests."),

			row("row-kpi", "HTTP KPIs"),
			stat("k-rps", "Requests/sec", "rate", unit("rps"), decimals(1), dsl(httpAny)),
			stat("k-err", "Error rate", "error_rate", unit("%"), decimals(2), dsl(httpAny)),
			stat("k-p95", "P95", "p95", field("duration_ms"), unit("ms"), decimals(0), dsl(httpAny)),
			stat("k-p99", "P99", "p99", field("duration_ms"), unit("ms"), decimals(0), dsl(httpAny)),

			row("row-status", "By status code"),
			line("rps-status", "RPS by status code", 2,
				spanCfg{Agg: "rate", DSL: httpAny, GroupBy: "http.status_code"}),
			line("err-status", "Errors/sec by status code", 2,
				spanCfg{Agg: "errors", DSL: httpErr, GroupBy: "http.status_code"}),

			row("row-route", "By route"),
			line("rps-route", "RPS by route", 2,
				spanCfg{Agg: "rate", DSL: httpAny, GroupBy: "http.route"}),
			line("p99-route", "P99 by route", 2,
				spanCfg{Agg: "p99", Field: "duration_ms", DSL: httpAny, GroupBy: "http.route"}),
			line("err-route", "Errors/sec by route", 4,
				spanCfg{Agg: "errors", DSL: httpErr, GroupBy: "http.route"}),

			row("row-method", "By method"),
			line("rps-method", "RPS by HTTP method", 2,
				spanCfg{Agg: "rate", DSL: httpAny, GroupBy: "http.method"}),
			line("p99-method", "P99 by HTTP method", 2,
				spanCfg{Agg: "p99", Field: "duration_ms", DSL: httpAny, GroupBy: "http.method"}),
		},
	)
}

// ── 4. Database Performance ─────────────────────────────────────────────────
//
// Storage tier RED. Scopes to spans where db.system is set.
// Cross-references the db-queries analyzer panel on /service
// for slow-statement drill-down.
func presetDatabase() Dashboard {
	const db = `db.system != ""`
	return dash(
		"preset-database",
		"Database Performance",
		"Query rate, latency and errors broken down by db.system, db.operation and calling service. Storage tier is the most common saturation culprit.",
		[]panel{
			md("intro",
				"**Database calls.** All spans with `db.system` set — PostgreSQL, "+
					"MySQL, Redis, MongoDB, etc. After spotting a hot db.system or "+
					"calling-service here, drill into the per-statement analyzer "+
					"on the [Service detail](/services) page (DB queries panel) "+
					"to find the actual slow normalised statement."),

			row("row-kpi", "Database KPIs"),
			stat("k-qps", "QPS", "rate", unit("qps"), decimals(1), dsl(db)),
			stat("k-err", "Error rate", "error_rate", unit("%"), decimals(2), dsl(db)),
			stat("k-p95", "P95", "p95", field("duration_ms"), unit("ms"), decimals(0), dsl(db)),
			stat("k-p99", "P99", "p99", field("duration_ms"), unit("ms"), decimals(0), dsl(db)),

			row("row-throughput", "Throughput"),
			line("rps-system", "QPS by db.system", 2,
				spanCfg{Agg: "rate", DSL: db, GroupBy: "db.system"}),
			line("rps-op", "QPS by db.operation", 2,
				spanCfg{Agg: "rate", DSL: db, GroupBy: "db.operation"}),

			row("row-latency", "Latency"),
			line("p95-op", "P95 by db.operation", 2,
				spanCfg{Agg: "p95", Field: "duration_ms", DSL: db, GroupBy: "db.operation"}),
			line("p95-svc", "P95 by calling service", 2,
				spanCfg{Agg: "p95", Field: "duration_ms", DSL: db, GroupBy: "service.name"}),
			line("p99-system", "P99 by db.system", 4,
				spanCfg{Agg: "p99", Field: "duration_ms", DSL: db, GroupBy: "db.system"}),

			row("row-errors", "Errors"),
			line("err-svc", "Error rate (%) by calling service", 2,
				spanCfg{Agg: "error_rate", DSL: db, GroupBy: "service.name"}),
			line("err-system", "Errors/sec by db.system", 2,
				spanCfg{Agg: "errors", DSL: db, GroupBy: "db.system"}),
		},
	)
}

// ── 5. Java / JVM ───────────────────────────────────────────────────────────
//
// Java-specific runtime dashboard. Spans filtered to
// telemetry.sdk.language=java for the request-side panels;
// jvm.* metrics for heap / GC / threads / classes. The two
// halves together answer "is my JVM healthy" without bouncing
// between dashboards.
func presetJavaJVM() Dashboard {
	const java = `resource.telemetry.sdk.language = "java"`
	return dash(
		"preset-java-jvm",
		"Java / JVM",
		"JVM-tier dashboard for Java services — heap, GC, threads, classes plus request-side RED filtered to language=java. Pair this with the JVM-specific runtime metrics your OTel Java agent exports.",
		[]panel{
			md("intro",
				"**Java services.** Top half = request-side RED filtered to "+
					"`telemetry.sdk.language=java`. Bottom half = JVM runtime "+
					"metrics (`jvm.*`) auto-emitted by the OTel Java agent. "+
					"Most common diagnosis paths: P99 spike + heap climbing → "+
					"GC churn (look at jvm.gc.duration); P99 spike + flat heap → "+
					"thread contention (look at jvm.thread.count + per-route P99 "+
					"on **HTTP Services**)."),

			row("row-kpi", "Java service KPIs"),
			stat("k-rps", "RPS (Java services)", "rate", unit("rps"), decimals(1), dsl(java)),
			stat("k-err", "Error rate", "error_rate", unit("%"), decimals(2), dsl(java)),
			stat("k-p95", "P95", "p95", field("duration_ms"), unit("ms"), decimals(0), dsl(java)),
			stat("k-p99", "P99", "p99", field("duration_ms"), unit("ms"), decimals(0), dsl(java)),

			row("row-svc", "By Java service"),
			line("rps-svc", "RPS by service", 2,
				spanCfg{Agg: "rate", DSL: java, GroupBy: "service.name"}),
			line("p99-svc", "P99 by service", 2,
				spanCfg{Agg: "p99", Field: "duration_ms", DSL: java, GroupBy: "service.name"}),

			row("row-heap", "JVM heap"),
			metric("heap-used", "jvm.memory.heap.used by service", 2,
				metricCfg{MetricName: "jvm.memory.heap.used", GroupBy: "service.name"}),
			metric("heap-committed", "jvm.memory.heap.committed by service", 2,
				metricCfg{MetricName: "jvm.memory.heap.committed", GroupBy: "service.name"}),

			row("row-gc", "Garbage collection"),
			metric("gc-duration", "jvm.gc.duration by service", 2,
				metricCfg{MetricName: "jvm.gc.duration", GroupBy: "service.name"}),
			metric("gc-collections", "jvm.gc.collections.count by service", 2,
				metricCfg{MetricName: "jvm.gc.collections.count", GroupBy: "service.name"}),

			row("row-threads", "Threads + classes"),
			metric("threads", "jvm.thread.count by service", 2,
				metricCfg{MetricName: "jvm.thread.count", GroupBy: "service.name"}),
			metric("classes", "jvm.class.count by service", 2,
				metricCfg{MetricName: "jvm.class.count", GroupBy: "service.name"}),
		},
	)
}

// ── 6. Node.js Runtime ──────────────────────────────────────────────────────
//
// Node-specific dashboard. Same shape as Java/JVM — request-
// side RED filtered to language=nodejs, then nodejs.* runtime
// metrics for event loop / memory / GC.
func presetNodeJS() Dashboard {
	const node = `resource.telemetry.sdk.language IN ("nodejs", "javascript")`
	return dash(
		"preset-nodejs",
		"Node.js Runtime",
		"Node-tier dashboard — request-side RED for Node services plus event-loop lag, heap and GC pressure. Most Node perf issues land on event loop saturation or heap fragmentation.",
		[]panel{
			md("intro",
				"**Node.js services.** Request-side RED filtered to "+
					"`telemetry.sdk.language IN (nodejs, javascript)` plus the "+
					"OTel Node SDK's runtime metrics. Event-loop lag spikes are "+
					"the canonical Node degradation; cross-reference with heap "+
					"committed to distinguish 'I/O blocked' from 'GC stop-the-world'."),

			row("row-kpi", "Node service KPIs"),
			stat("k-rps", "RPS (Node services)", "rate", unit("rps"), decimals(1), dsl(node)),
			stat("k-err", "Error rate", "error_rate", unit("%"), decimals(2), dsl(node)),
			stat("k-p95", "P95", "p95", field("duration_ms"), unit("ms"), decimals(0), dsl(node)),
			stat("k-p99", "P99", "p99", field("duration_ms"), unit("ms"), decimals(0), dsl(node)),

			row("row-svc", "By Node service"),
			line("rps-svc", "RPS by service", 2,
				spanCfg{Agg: "rate", DSL: node, GroupBy: "service.name"}),
			line("p99-svc", "P99 by service", 2,
				spanCfg{Agg: "p99", Field: "duration_ms", DSL: node, GroupBy: "service.name"}),

			row("row-eventloop", "Event loop"),
			metric("eventloop-delay", "nodejs.eventloop.delay by service", 2,
				metricCfg{MetricName: "nodejs.eventloop.delay", GroupBy: "service.name"}),
			metric("eventloop-utilization", "nodejs.eventloop.utilization by service", 2,
				metricCfg{MetricName: "nodejs.eventloop.utilization", GroupBy: "service.name"}),

			row("row-heap", "Heap"),
			metric("heap-used", "process.runtime.nodejs.heap.used by service", 2,
				metricCfg{MetricName: "process.runtime.nodejs.heap.used", GroupBy: "service.name"}),
			metric("heap-total", "process.runtime.nodejs.heap.total by service", 2,
				metricCfg{MetricName: "process.runtime.nodejs.heap.total", GroupBy: "service.name"}),
		},
	)
}

// ── 7. Go Runtime ───────────────────────────────────────────────────────────
//
// Go-specific dashboard. Goroutines, GC pause, heap_alloc —
// the standard runtime/pprof signals an oncall reaches for.
func presetGoRuntime() Dashboard {
	const goLang = `resource.telemetry.sdk.language = "go"`
	return dash(
		"preset-go-runtime",
		"Go Runtime",
		"Go-tier dashboard — goroutines, GC, heap_alloc plus request-side RED for Go services. Goroutine count climbing faster than RPS is the canonical 'leak' signal.",
		[]panel{
			md("intro",
				"**Go services.** Request-side RED filtered to "+
					"`telemetry.sdk.language=go` plus runtime metrics emitted by the "+
					"OTel Go SDK. Two telltale patterns: goroutines climbing without "+
					"matching RPS = leak; gc.pause climbing without matching "+
					"heap_alloc = pressure on small allocations (look at allocs by "+
					"alloc-rate metric)."),

			row("row-kpi", "Go service KPIs"),
			stat("k-rps", "RPS (Go services)", "rate", unit("rps"), decimals(1), dsl(goLang)),
			stat("k-err", "Error rate", "error_rate", unit("%"), decimals(2), dsl(goLang)),
			stat("k-p95", "P95", "p95", field("duration_ms"), unit("ms"), decimals(0), dsl(goLang)),
			stat("k-p99", "P99", "p99", field("duration_ms"), unit("ms"), decimals(0), dsl(goLang)),

			row("row-svc", "By Go service"),
			line("rps-svc", "RPS by service", 2,
				spanCfg{Agg: "rate", DSL: goLang, GroupBy: "service.name"}),
			line("p99-svc", "P99 by service", 2,
				spanCfg{Agg: "p99", Field: "duration_ms", DSL: goLang, GroupBy: "service.name"}),

			row("row-goroutines", "Goroutines"),
			metric("goroutines", "process.runtime.go.goroutines by service", 4,
				metricCfg{MetricName: "process.runtime.go.goroutines", GroupBy: "service.name"}),

			row("row-heap", "Heap"),
			metric("heap-alloc", "process.runtime.go.mem.heap_alloc by service", 2,
				metricCfg{MetricName: "process.runtime.go.mem.heap_alloc", GroupBy: "service.name"}),
			metric("heap-objects", "process.runtime.go.mem.heap_objects by service", 2,
				metricCfg{MetricName: "process.runtime.go.mem.heap_objects", GroupBy: "service.name"}),

			row("row-gc", "Garbage collection"),
			metric("gc-pause", "process.runtime.go.gc.pause_total_ns by service", 2,
				metricCfg{MetricName: "process.runtime.go.gc.pause_total_ns", GroupBy: "service.name"}),
			metric("gc-count", "process.runtime.go.gc.count by service", 2,
				metricCfg{MetricName: "process.runtime.go.gc.count", GroupBy: "service.name"}),
		},
	)
}

// ── 8. Kafka / Messaging ────────────────────────────────────────────────────
//
// Messaging-tier dashboard. Filters on messaging.system to
// scope to message-broker spans (kafka, rabbitmq, sqs, etc.);
// per-topic + per-system breakdown.
func presetKafkaMessaging() Dashboard {
	const msg = `messaging.system != ""`
	const kafka = `messaging.system = "kafka"`
	return dash(
		"preset-kafka-messaging",
		"Kafka / Messaging",
		"Producer + consumer rate, latency, and errors per messaging.system and per topic. Kafka-first but works for any broker the OTel SDKs instrument (RabbitMQ, SQS, NATS, etc.).",
		[]panel{
			md("intro",
				"**Messaging tier.** Spans where `messaging.system` is set. "+
					"Producer and consumer kinds split via `kind`. The "+
					"`messaging.destination.name` group-by surfaces hot topics. "+
					"Lag-by-consumer is best read from the broker's own metrics "+
					"(kafka_consumergroup_lag) — wire those into the metric "+
					"panels by name; they show up automatically when present."),

			row("row-kpi", "Messaging KPIs"),
			stat("k-rps", "Messages/sec", "rate", unit("msg/s"), decimals(1), dsl(msg)),
			stat("k-err", "Error rate", "error_rate", unit("%"), decimals(2), dsl(msg)),
			stat("k-p95", "P95", "p95", field("duration_ms"), unit("ms"), decimals(0), dsl(msg)),
			stat("k-p99", "P99", "p99", field("duration_ms"), unit("ms"), decimals(0), dsl(msg)),

			row("row-system", "By messaging.system"),
			line("rps-system", "Rate by messaging.system", 2,
				spanCfg{Agg: "rate", DSL: msg, GroupBy: "messaging.system"}),
			line("p99-system", "P99 by messaging.system", 2,
				spanCfg{Agg: "p99", Field: "duration_ms", DSL: msg, GroupBy: "messaging.system"}),

			row("row-kind", "Producer vs consumer"),
			line("rps-kind", "Rate by kind (producer / consumer)", 2,
				spanCfg{Agg: "rate", DSL: msg, GroupBy: "kind"}),
			line("err-kind", "Errors/sec by kind", 2,
				spanCfg{Agg: "errors", DSL: msg, GroupBy: "kind"}),

			row("row-kafka-topic", "Kafka topics (filtered to messaging.system=kafka)"),
			line("rps-topic", "Messages/sec by topic", 2,
				spanCfg{Agg: "rate", DSL: kafka, GroupBy: "messaging.destination.name"}),
			line("p99-topic", "P99 by topic", 2,
				spanCfg{Agg: "p99", Field: "duration_ms", DSL: kafka, GroupBy: "messaging.destination.name"}),

			row("row-broker", "Broker metrics (auto-populated when emitted)"),
			metric("kafka-lag", "kafka_consumergroup_lag by group", 2,
				metricCfg{MetricName: "kafka_consumergroup_lag", GroupBy: "consumergroup"}),
			metric("kafka-msg-in", "kafka.server.brokertopicmetrics.messagesin.rate", 2,
				metricCfg{MetricName: "kafka.server.brokertopicmetrics.messagesin.rate", GroupBy: "topic"}),
		},
	)
}

// ── 9. Errors & Exceptions ──────────────────────────────────────────────────
//
// Error-focused multi-axis breakdown — by service, operation,
// HTTP status, span kind. Pairs with the Exceptions inbox for
// the actual stack-trace details.
func presetErrorsExceptions() Dashboard {
	const errOnly = `status_code = "error"`
	const httpErr = `http.method != "" AND status_code = "error"`
	return dash(
		"preset-errors-exceptions",
		"Errors & Exceptions",
		"Multi-axis error breakdown — by service, operation, HTTP status, span kind. Pairs with the Exceptions inbox for actual stack-trace details. Open this when error rate trends up.",
		[]panel{
			md("intro",
				"**Where do errors live?** Same error firehose sliced four "+
					"different ways so the operator finds the cluster regardless "+
					"of dimension. After identifying the hot service / operation "+
					"here, jump to:\n\n"+
					"• [Exceptions](/errors) — grouped stack traces with sample occurrences\n"+
					"• [Anomalies](/anomalies) — log-pattern detector + trace-op spikes\n"+
					"• [Traces](/traces) (filter `status_code:error`) — sample failed traces"),

			row("row-rate", "Error rate"),
			stat("k-rate", "Error rate", "error_rate", unit("%"), decimals(2)),
			stat("k-eps", "Errors/sec", "errors", unit("eps"), decimals(1)),
			stat("k-cnt", "Total RPS", "rate", unit("rps"), decimals(0)),

			row("row-by-svc", "By service"),
			line("rate-svc", "Error rate (%) by service", 2,
				spanCfg{Agg: "error_rate", GroupBy: "service.name"}),
			line("cnt-svc", "Errors/sec by service", 2,
				spanCfg{Agg: "errors", GroupBy: "service.name"}),

			row("row-by-op", "By operation"),
			line("cnt-op", "Errors/sec by operation", 4,
				spanCfg{Agg: "errors", DSL: errOnly, GroupBy: "name"}),

			row("row-http", "HTTP errors"),
			line("http-status", "Errors/sec by HTTP status code", 2,
				spanCfg{Agg: "errors", DSL: httpErr, GroupBy: "http.status_code"}),
			line("http-route", "Errors/sec by HTTP route", 2,
				spanCfg{Agg: "errors", DSL: httpErr, GroupBy: "http.route"}),

			row("row-kind", "By span kind"),
			line("err-kind", "Errors/sec by kind", 4,
				spanCfg{Agg: "errors", DSL: errOnly, GroupBy: "kind"}),
		},
	)
}
