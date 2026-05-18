// Package logstore is the read-side abstraction for log queries. The
// write side stays in chstore (OTLP logs are always batched into the
// ClickHouse `logs` table on ingest); on the read side an operator can
// point Coremetry at an external Elasticsearch cluster instead so
// queries hit the same index their existing logging pipeline ships to.
//
// Two backends ship today:
//   - chstore-backed (default) — uses the same `logs` table as ingest
//   - elasticsearch-backed     — wraps github.com/elastic/go-elasticsearch
//
// Both expose the same `Search` surface so api.go doesn't have to know
// which is in use.
package logstore

import (
	"context"
	"time"
)

// Filter is the union of every supported log-query parameter. Backends
// translate as much as they can; what they can't handle they ignore
// (with a log line).
type Filter struct {
	Service     string
	Search      string
	From, To    time.Time
	SeverityMin uint8     // OTel severity number ≥ this; 0 = no filter
	TraceID     string
	SpanID      string
	Limit       int
	Offset      int
}

// LogRecord is the in-memory shape returned by every backend. It mirrors
// chstore.Log but without the ClickHouse-specific tags so the JSON
// surface stays stable across backends.
type LogRecord struct {
	ID                 int64             `json:"id"`
	Timestamp          int64             `json:"timestamp"`     // unix ns
	Severity           uint8             `json:"severity"`      // OTel SeverityNumber 0..24
	SeverityText       string            `json:"severityText"`
	Body               string            `json:"body"`
	ServiceName        string            `json:"serviceName"`
	TraceID            string            `json:"traceId"`
	SpanID             string            `json:"spanId"`
	Attributes         map[string]string `json:"attributes"`
	ResourceAttributes map[string]string `json:"resourceAttributes"`
}

// Page is the result of a Search — total covers the full match count
// for paging UIs even when len(Logs) < Limit.
type Page struct {
	Total int          `json:"total"`
	Logs  []*LogRecord `json:"logs"`
}

// FacetField identifies a known facet dimension. Names map to a
// per-backend column / field path that already gets read by Search
// (so backends don't have to invent new fields for facets).
type FacetField string

const (
	FacetService   FacetField = "service"
	FacetSeverity  FacetField = "severity"
	FacetNamespace  FacetField = "namespace"
	FacetDeployment FacetField = "deployment"
	FacetPod        FacetField = "pod"
	FacetContainer FacetField = "container"
	FacetCluster   FacetField = "cluster"
)

// FacetBucket is one (value, count) pair from a facet aggregation.
type FacetBucket struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// FacetResult maps each requested facet to its top-N buckets.
type FacetResult map[FacetField][]FacetBucket

// Store is the read interface every backend implements.
type Store interface {
	Search(ctx context.Context, f Filter) (*Page, error)

	// Histogram returns one bucketed timeseries per group_value for
	// the requested filter. Powers the Logs source in /explore — the
	// caller sets the bucket size (e.g. 30s, 5m) and an optional
	// `groupBy` field name (one of "service", "severity", or any
	// attribute path the backend knows). Empty groupBy → a single
	// "_total" series.
	Histogram(ctx context.Context, f Filter, bucketSec int, groupBy string) ([]LogSeries, error)

	// Facets returns top-N (value, count) pairs per requested
	// facet, all scoped to the same Filter as Search. Powers the
	// /logs sidebar that lets operators narrow by click instead of
	// typing. Returns an empty bucket list for fields the backend
	// can't resolve (e.g. missing column).
	Facets(ctx context.Context, f Filter, fields []FacetField, topN int) (FacetResult, error)

	// Backend returns a short identifier shown in /api/health so an operator
	// can tell at a glance which log source is wired in.
	Backend() string

	// Ping reports liveness of the underlying backend. Used by /api/status
	// to surface "logs backend is down" before the user runs into an
	// empty-result query.
	Ping(ctx context.Context) error
}

// LogSeries is one bucketed timeseries returned by Histogram. Name
// is the group_value (or "_total" when grouping is off); each
// Point.T is the bucket-start (unix ns) and V is the count.
type LogSeries struct {
	Name   string     `json:"name"`
	Points []LogPoint `json:"points"`
}

type LogPoint struct {
	T int64 `json:"t"` // unix ns, bucket start
	V int64 `json:"v"` // count
}
