package chstore

import "time"

// Span is an OTLP span normalised for ClickHouse storage.
type Span struct {
	TraceID     string
	SpanID      string
	ParentID    string
	Name        string
	Kind        string
	ServiceName string
	HostName    string
	DeployEnv   string
	StatusCode  string // "ok" | "error" | "unset"
	StatusMsg   string
	Time        time.Time
	Duration    int64 // nanoseconds
	DBSystem    string
	DBStatement string
	HTTPMethod  string
	HTTPRoute   string
	HTTPStatus  uint16
	RPCSystem   string
	RPCMethod   string
	PeerService string
	MsgSystem   string
	AttrKeys    []string
	AttrValues  []string
	ResKeys     []string
	ResValues   []string
	Events      string // JSON array
	ScopeName   string
}

// Log is a normalised OTLP log record.
type Log struct {
	TraceID      string
	SpanID       string
	Time         time.Time
	SeverityNum  uint8
	SeverityText string
	Body         string
	ServiceName  string
	HostName     string
	AttrKeys     []string
	AttrValues   []string
	ResKeys      []string
	ResValues    []string
	ScopeName    string
}

// MetricPoint is a single metric data point.
type MetricPoint struct {
	Metric      string
	Instrument  string // gauge, sum, histogram, summary
	Description string
	Unit        string
	ServiceName string
	HostName    string
	Time        time.Time
	StartTime   time.Time
	Value       float64
	Count       uint64
	SumValue    float64
	MinValue    float64
	MaxValue    float64
	AttrKeys    []string
	AttrValues  []string
	ResKeys     []string
	ResValues   []string
	// v0.5.358 — histogram bucket layout. Populated for
	// Histogram data points by otlp/convert.go; nil/empty for
	// all other instruments. BucketBounds carries the explicit
	// upper bounds (length N); BucketCounts carries the
	// per-bucket counts (length N+1 in canonical OTel,
	// element[i] = count of observations ≤ BucketBounds[i],
	// element[N] = +Inf bucket). At read time we sum these
	// element-wise across data points to compute quantiles.
	BucketBounds []float64
	BucketCounts []uint64
}

// ── API response types ────────────────────────────────────────────────────────

// OperationSummary is one row of the per-operation aggregate shown on
// the service detail page. Same shape as ServiceSummary but keyed by
// span name within a single service. Apdex is computed against the same
// 200ms threshold used by GetServices so the numbers are comparable.
//
// Sparkline carries a fixed-length call-rate histogram (uint64 per
// bucket) over the same window as the aggregate row. Length is
// SparklineBuckets (see repo.go) — the frontend renders it as an inline
// SVG so the operator can spot a slow-burn vs. spike pattern at a glance
// without leaving the table.
type OperationSummary struct {
	Name       string   `json:"name"`
	SpanCount  uint64   `json:"spanCount"`
	ErrorCount uint64   `json:"errorCount"`
	ErrorRate  float64  `json:"errorRate"`
	AvgMs      float64  `json:"avgDurationMs"`
	P50Ms      float64  `json:"p50DurationMs"`
	P95Ms      float64  `json:"p95DurationMs"`
	P99Ms      float64  `json:"p99DurationMs"`
	Apdex      float64  `json:"apdex"`
	Sparkline  []uint64 `json:"sparkline,omitempty"`
	// v0.5.392 — companion error + p99 sparklines aligned to the
	// same SparklineBuckets grid as Sparkline. Drives the per-row
	// metric drill-in modal on the service detail page so the
	// operator reads RED dimensions side-by-side without leaving
	// the table. Both fields are optional (the raw-spans fallback
	// path doesn't always compute them); UI tolerates absence.
	ErrorsSparkline []uint64  `json:"errorsSparkline,omitempty"`
	P99Sparkline    []float64 `json:"p99Sparkline,omitempty"`
}

type ServiceSummary struct {
	Name       string  `json:"name"`
	SpanCount  uint64  `json:"spanCount"`
	ErrorCount uint64  `json:"errorCount"`
	ErrorRate  float64 `json:"errorRate"`
	AvgMs      float64 `json:"avgDurationMs"`
	P99Ms      float64 `json:"p99DurationMs"`
	// Apdex score in [0, 1]. 1 = all satisfying; 0 = all frustrated.
	//   satisfied  : duration ≤ T
	//   tolerating : T < duration ≤ 4T
	//   frustrated : duration > 4T
	//   apdex = (satisfied + tolerating/2) / total
	Apdex            float64 `json:"apdex"`
	ApdexThresholdMs float64 `json:"apdexThresholdMs"`
	// Health (v0.5.274) — auto-scored red/yellow/green badge.
	// Computed at READ time in the api layer from errorRate +
	// open problem counts. NOT stored in the MV; recomputed on
	// every /api/services response so a freshly-opened
	// critical flips the badge immediately. The HealthReason
	// string explains the rule that fired so the operator
	// can argue with the verdict.
	Health         string `json:"health,omitempty"`         // "" | "green" | "yellow" | "red"
	HealthReason   string `json:"healthReason,omitempty"`   // short string e.g. "1 open critical"
	OpenProblems   int    `json:"openProblems,omitempty"`   // count of all open problems on this service
}

// ── Exception aggregate (Errors page) ────────────────────────────────────────

type ExceptionRow struct {
	Type         string `json:"type"`
	Message      string `json:"message"`
	Service      string `json:"service"`
	Count        uint64 `json:"count"`
	LastSeen     int64  `json:"lastSeen"`
	SampleTraceID string `json:"sampleTraceId"`
	SampleSpanID  string `json:"sampleSpanId"`
}

type TraceRow struct {
	TraceID     string  `json:"traceId"`
	RootName    string  `json:"rootName"`
	ServiceName string  `json:"serviceName"`
	StartTime   int64   `json:"startTime"`
	DurationMs  float64 `json:"durationMs"`
	SpanCount   uint64  `json:"spanCount"`
	HasError    bool    `json:"hasError"`
	// Per-trace lookup of attribute values requested by the caller
	// via TraceFilter.ExtraAttrs. Keys mirror the requested list;
	// missing/empty values surface as "" so the UI can render a
	// "—" placeholder. Omitted entirely when no extras requested.
	Extras map[string]string `json:"extras,omitempty"`
}

type SpanRow struct {
	TraceID            string                 `json:"traceId"`
	SpanID             string                 `json:"spanId"`
	ParentSpanID       string                 `json:"parentSpanId"`
	Name               string                 `json:"name"`
	Kind               string                 `json:"kind"`
	ServiceName        string                 `json:"serviceName"`
	HostName           string                 `json:"hostName"`
	StartTime          int64                  `json:"startTime"`
	EndTime            int64                  `json:"endTime"`
	DurationMs         float64                `json:"durationMs"`
	StatusCode         string                 `json:"statusCode"`
	StatusMessage      string                 `json:"statusMessage"`
	Attributes         map[string]string      `json:"attributes"`
	ResourceAttributes map[string]string      `json:"resourceAttributes"`
	Events             interface{}            `json:"events"`
	ScopeName          string                 `json:"scopeName"`
	DBSystem           string                 `json:"dbSystem,omitempty"`
	DBStatement        string                 `json:"dbStatement,omitempty"`
	HTTPMethod         string                 `json:"httpMethod,omitempty"`
	HTTPRoute          string                 `json:"httpRoute,omitempty"`
	HTTPStatus         uint16                 `json:"httpStatus,omitempty"`
	PeerService        string                 `json:"peerService,omitempty"`
}

type LogRow struct {
	ID                 uint64            `json:"id"`
	Timestamp          int64             `json:"timestamp"`
	SeverityNumber     uint8             `json:"severity"`
	SeverityText       string            `json:"severityText"`
	Body               string            `json:"body"`
	ServiceName        string            `json:"serviceName"`
	TraceID            string            `json:"traceId"`
	SpanID             string            `json:"spanId"`
	Attributes         map[string]string `json:"attributes"`
	ResourceAttributes map[string]string `json:"resourceAttributes"`
}

type MetricInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Unit        string `json:"unit"`
	Type        string `json:"type"`
}

type MetricPointRow struct {
	Time  int64   `json:"time"`
	Value float64 `json:"value"`
	Count uint64  `json:"count"`
	Sum   float64 `json:"sum"`
	Attrs string  `json:"attrs"`
}

// Profile is a stored pprof profile (CPU, heap, etc.).
type Profile struct {
	ProfileID    string
	ServiceName  string
	HostName     string
	ProfileType  string // cpu, heap, goroutine, alloc
	StartTime    time.Time
	DurationNs   int64
	PprofData    []byte
	SampleCount  uint32
	LabelsKeys   []string
	LabelsValues []string
}

type ProfileRow struct {
	ProfileID   string `json:"profileId"`
	ServiceName string `json:"serviceName"`
	HostName    string `json:"hostName"`
	ProfileType string `json:"profileType"`
	StartTime   int64  `json:"startTime"` // unix nanos
	DurationMs  int64  `json:"durationMs"`
	SampleCount uint32 `json:"sampleCount"`
}

// FlameNode is a single frame in the flame graph tree.
type FlameNode struct {
	Name     string       `json:"name"`
	File     string       `json:"file,omitempty"`
	Line     int64        `json:"line,omitempty"`
	Value    int64        `json:"value"`
	Self     int64        `json:"self,omitempty"`
	Children []*FlameNode `json:"children,omitempty"`
}

// AggregateRow is one bucket in the trace aggregate view (group-by operation
// or service). Counts are number of distinct traces (not spans).
type AggregateRow struct {
	GroupKey   string  `json:"groupKey"`
	GroupExtra string  `json:"groupExtra,omitempty"` // e.g. service name when grouping by operation
	TraceCount uint64  `json:"traceCount"`
	// WithRawAvailable — count of TraceCount trace_ids that still
	// have raw spans in the window. trace_summary_5m holds 90 days
	// while raw spans hold 30 (or whatever retention.spans is set
	// to), so older aggregate rows may not be drillable. The UI
	// renders a chip when this is lower than TraceCount so the
	// operator knows clicking won't always reach detail. The raw-
	// spans aggregate path (non-fast-path) sets this == TraceCount
	// since every counted trace by definition has raw data. v0.6.39.
	WithRawAvailable uint64 `json:"withRawAvailable"`
	// PerMin is traces per minute over the requested window —
	// Uptrace-style perMin(count()) so the operator can compare
	// throughput across windows of different lengths.
	PerMin     float64 `json:"perMin"`
	ErrorCount uint64  `json:"errorCount"`
	ErrorRate  float64 `json:"errorRate"`
	AvgMs      float64 `json:"avgMs"`
	P50Ms      float64 `json:"p50Ms"`
	P95Ms      float64 `json:"p95Ms"`
	P99Ms      float64 `json:"p99Ms"`
	MaxMs      float64 `json:"maxMs"`
	LastSeen   int64   `json:"lastSeen"` // unix nanos
}

type ServiceEdge struct {
	Source    string  `json:"source"`
	Target    string  `json:"target"`
	CallCount uint64  `json:"callCount"`
	ErrorRate float64 `json:"errorRate"`
	AvgMs     float64 `json:"avgMs"`
}

func arraysToMap(keys, values []string) map[string]string {
	m := make(map[string]string, len(keys))
	for i, k := range keys {
		if i < len(values) {
			m[k] = values[i]
		}
	}
	return m
}
