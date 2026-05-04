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

// Store is the read interface every backend implements.
type Store interface {
	Search(ctx context.Context, f Filter) (*Page, error)

	// Backend returns a short identifier shown in /api/health so an operator
	// can tell at a glance which log source is wired in.
	Backend() string
}
