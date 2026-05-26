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
	// TraceIDs (v0.5.271) — multi-trace filter for the DQL
	// cross-signal join. When non-empty, backends should
	// match ANY trace_id in the list (OR semantics) in
	// addition to / instead of TraceID. Single-string TraceID
	// stays primary for the existing /logs page UX.
	TraceIDs    []string
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

// PatternSpec is the cross-backend description of a "find log
// lines that look like this" probe. Both backends consume it:
//
//   - ClickHouse evaluates Regex against the body field with the
//     Tokens list driving the tokenbf_v1 prefilter so granules
//     with none of the tokens are pruned before the regex pass.
//   - Elasticsearch ignores Regex (regex queries are slow on
//     large indices) and uses Tokens directly as a query_string
//     OR clause against the body field — inverted-index lookup
//     stays sub-second at billion-log scale.
//
// Tokens are lowercase substrings the body must contain when
// the regex matches. The detector author picks them so the OR
// clause has zero false-negatives vs the regex.
type PatternSpec struct {
	Regex  string
	Tokens []string
}

// PatternStats is the per-pattern signal a detector consumes:
// counts in the "current" + "baseline" windows, a representative
// service + sample, and the most recent occurrence time.
type PatternStats struct {
	Cur        uint64
	Base       uint64
	Service    string
	Sample     string
	LastSeenNs int64
	// TopServices — v0.5.287. Per-service breakdown of the
	// current-window hits, sorted by count desc. Up to 5 entries.
	// Populated by both backends when Cur > 0 so the
	// /logs LogPatternStrip can show "fires on these N services"
	// rosette without a follow-up call. Empty when only one
	// service or when the backend doesn't track it.
	TopServices []PatternServiceHit
}

// PatternServiceHit pairs a service name with how many times it
// produced the pattern in the current detection window.
type PatternServiceHit struct {
	Service string `json:"service"`
	Count   uint64 `json:"count"`
}

// SignificantPattern is one statistically-anomalous token surfaced
// by an unsupervised pass over recent logs (v0.5.243). DocCount is
// how often the token appeared in the current window; BgCount is
// the baseline window. Score is the backend's "rare-vs-baseline"
// signal (ES uses a chi-square-like measure; CH backend returns
// 0 because it has no native significant_text agg).
type SignificantPattern struct {
	Token    string  `json:"token"`
	DocCount uint64  `json:"docCount"`
	BgCount  uint64  `json:"bgCount"`
	Score    float64 `json:"score"`
}

// Store is the read interface every backend implements.
type Store interface {
	Search(ctx context.Context, f Filter) (*Page, error)

	// CountPatterns returns per-pattern current-window +
	// baseline-window counts, services, and samples. Plural form
	// so backends can batch — at billion-log scale on ES, an
	// _msearch with all N pattern bodies in a single HTTP
	// round-trip beats N parallel _search calls. CH backend
	// iterates sequentially (queries are cheap behind the
	// tokenbf_v1 skip index). Result slice index matches the
	// input slice index; empty PatternStats indicates "no match
	// in current window" (detector ignores these).
	CountPatterns(ctx context.Context, pats []PatternSpec, curStart, baseStart, now time.Time) ([]PatternStats, error)

	// SignificantPatterns runs an unsupervised "what tokens are
	// statistically rare in the current window vs the baseline"
	// pass. ES backend uses the native significant_text
	// aggregation; CH backend returns nil (no native equivalent
	// at billion-row scale — operators on CH rely on the
	// curated detector + saved-search alerts instead). Cur =
	// (curStart, now); Base = (baseStart, curStart). topN caps
	// the returned bucket count.
	SignificantPatterns(ctx context.Context, curStart, baseStart, now time.Time, topN int) ([]SignificantPattern, error)

	// Histogram returns one bucketed timeseries per group_value for
	// the requested filter. Powers the Logs source in /explore — the
	// caller sets the bucket size (e.g. 30s, 5m) and an optional
	// `groupBy` field name (one of "service", "severity", or any
	// attribute path the backend knows). Empty groupBy → a single
	// "_total" series.
	Histogram(ctx context.Context, f Filter, bucketSec int, groupBy string) ([]LogSeries, error)

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
