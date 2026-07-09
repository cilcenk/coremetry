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
	"errors"
	"fmt"
	"net"
	"time"
)

// LogsTailMax caps the per-tick batch the live-tail forward read returns
// (Filter.SinceNs mode). A bounded read per tick keeps the CH/ES query
// cheap; when a tick fills this cap the stream emits a `gap` marker so a
// busy service surfaces "fell behind" instead of silently dropping rows.
const LogsTailMax = 500

// logsTailMax is the unexported alias used inside this package.
const logsTailMax = LogsTailMax

// Filter is the union of every supported log-query parameter. Backends
// translate as much as they can; what they can't handle they ignore
// (with a log line).
type Filter struct {
	Service     string
	Cluster     string    // v0.5.471 — k8s/openshift cluster name; empty = any
	// Env (v0.8.400 — env-separation Phase 4) — the global ?env=
	// deployment-environment filter. CH backend: bounded res-array
	// lookup over BOTH semconv spellings (deployment.environment.name
	// + legacy deployment.environment). ES backend: term filter on the
	// operator-configured fields.Env or, when that's empty, a
	// SELF-DISCOVERED field (cached field_caps over the candidate
	// shapes — es_env_field.go); when no field resolves, the filter is
	// NOT applied and Page.EnvUnapplied reports it honestly.
	Env         string
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
	// HasTrace (v0.8.406 — operator ask: "sadece trace'i olan loglar")
	// keeps only records with a non-empty trace correlation, so the
	// operator can filter /logs to pivotable rows. CH: trace_id != ''.
	// ES: exists over the four common trace-field spellings (+ the
	// configured override) — same field fan-out as the TraceID lookup.
	HasTrace    bool
	Limit       int
	Offset      int
	// Cursor (v0.7.22, SAFE-CORE) — opaque keyset paging token.
	// When non-empty the backend decodes its OWN format and pages
	// AFTER the encoded position instead of using Offset. The API
	// layer treats this string as opaque (it just round-trips the
	// value from the previous Page.NextCursor). Empty = first page,
	// in which case Offset is still honoured for back-compat with
	// callers that page by offset. Each backend defines its own
	// cursor encoding (CH: base64("ch|"+timeNs+"|"+rowKey), where rowKey
	// is a cityHash64 row digest giving a strict total order; ES:
	// base64 of the hit's sort-values JSON array).
	Cursor string
	// Ascending (v0.7.83) — return oldest-first instead of the default
	// newest-first. Used by the /logs Context "after" window so a
	// LIMIT n read yields the n records immediately AFTER the pivot,
	// not the n newest in the forward window. Backends honour it only
	// on a non-cursor read (keyset paging is DESC-only).
	Ascending bool
	// SinceNs (v0.8.x) — FORWARD-TAIL mode for the live-tail SSE stream.
	// When > 0 the backend reads `time >= SinceNs` oldest-first, bounded
	// by Limit, and DELIBERATELY skips the total count() (the per-tick
	// cost the SSE tail exists to remove) AND the keyset Cursor/PIT
	// machinery (a forward tail at the live edge must NOT reuse the
	// DESC keyset cursor — elasticsearch.go's PIT-per-page would re-pin
	// segment readers every tick, the v0.8.3 incident shape). ES uses a
	// plain bounded range query (no PIT, track_total_hits:false, explicit
	// timeout, request_cache off). The caller (streamLogs handler) tracks
	// the newest timestamp it has emitted and passes it back as the next
	// SinceNs; it dedups same-ns boundary rows by LogRecord.ID. `>=` (not
	// `>`) so a log ingested late at the boundary ns is re-read, not
	// silently dropped (the v0.7.15 silent-drop failure).
	SinceNs int64

	// envField (v0.8.400, ES-internal) — the resolved document field
	// the Env term filter targets, stamped by ESStore.applyEnvResolution
	// before buildQuery runs. Unexported on purpose: callers set Env;
	// only the ES backend (same package) resolves the field. Env set
	// with envField empty ⇒ the ES query emits NO env clause and the
	// backend reports Page.EnvUnapplied.
	envField string
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
	// NextCursor (v0.7.22, SAFE-CORE) — opaque keyset token the
	// caller passes back as Filter.Cursor to fetch the next page.
	// Empty when this is the last page (fewer than Limit rows
	// returned). Opaque to the API layer; format is backend-owned.
	NextCursor string `json:"nextCursor,omitempty"`
	// EnvUnapplied (v0.8.400 — env-separation Phase 4) — the backend's
	// HONEST signal that Filter.Env was requested but could not be
	// applied (ES: no environment field resolvable in the mapping, and
	// none configured). The results are env-UNFILTERED; the /logs page
	// renders a warning chip instead of silently implying a narrowed
	// view (the v0.8.398 honesty pattern). Never set by the CH backend
	// (the res-array conjunct always applies).
	EnvUnapplied bool `json:"envUnapplied,omitempty"`
}

// EQLQuery — parameters for an Event Query Language sequence
// detection (v0.5.468). ES-native; CH backend rejects.
type EQLQuery struct {
	Query string    // EQL expression: `sequence … [event …] [event …]`
	From  time.Time // window start (zero = no lower bound)
	To    time.Time // window end (zero = now)
	Size  int       // max sequences to return; 0 = backend default (~10)
}

// EQLEvent — one event row inside a matched sequence. Carries
// only the columns Coremetry's /logs renderer needs.
type EQLEvent struct {
	Timestamp int64  `json:"timestamp"` // unix ns
	Body      string `json:"body"`
	Service   string `json:"service"`
	Severity  string `json:"severity"`
}

// EQLSequence — one matched sequence. JoinKeys lifts the `by`
// columns the operator's EQL expression grouped on.
type EQLSequence struct {
	JoinKeys []string   `json:"joinKeys"`
	Events   []EQLEvent `json:"events"`
}

// IndexInfo describes one log-backend index (ES) or shard table
// (CH). Returned by Store.Indices for /admin/elastic to surface
// per-index health + ILM lifecycle state. v0.5.466.
type IndexInfo struct {
	Name      string `json:"name"`
	DocCount  int64  `json:"docCount"`
	SizeBytes int64  `json:"sizeBytes"`
	Health    string `json:"health"`    // green | yellow | red | "" if unknown
	IlmPolicy string `json:"ilmPolicy"` // policy name attached, empty if none
	IlmPhase  string `json:"ilmPhase"`  // hot | warm | cold | frozen | delete | ""
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

// ESQueryError is one failed ES query, captured for the /admin/elastic
// "recent query errors" panel (v0.8.230, operator-requested: "when ES
// has a problem I want to see the queries it sent"). Query is the exact
// request body sent (truncated at 4 KiB) so the operator can replay it
// with curl against their cluster.
type ESQueryError struct {
	At     int64  `json:"at"` // unix ms
	Op     string `json:"op"`
	Index  string `json:"index"`
	Query  string `json:"query"`
	Status int    `json:"status"` // HTTP status; 0 = transport error
	Error  string `json:"error"`
}

// ESDiagnostics is the ES backend's self-observation snapshot: total
// failed queries since process start + the most recent failures.
type ESDiagnostics struct {
	QueryErrors  int64          `json:"queryErrors"`
	RecentErrors []ESQueryError `json:"recentErrors"` // newest-first, ≤20
}

// Diagnoser is implemented by backends that track per-query failure
// diagnostics (currently only ES). The API layer type-asserts — the CH
// backend doesn't implement it and the endpoint reports an empty set.
type Diagnoser interface {
	Diagnostics() ESDiagnostics
}

// ─── Trace-context self-discovery (v0.8.348, pivot Phase 1c) ────────────────
//
// The trace→log pivot silently dies when the backend's trace-id field is
// mis-mapped (ES `text` → term clauses match nothing) or absent. Instead of
// asking the operator to hand-query their cluster (audit §3 ⚠), the system
// verifies its OWN configured backend: field mapping verdict + "% of logs
// with trace context" coverage over the last 24h. Surfaced on
// GET /api/admin/logstore/trace-context → /admin/elastic + /admin/stats.

// TraceContextField is one candidate trace-id field shape as the backend
// maps it. Types empty = the field is absent from the mapping.
type TraceContextField struct {
	Name         string   `json:"name"`
	Types        []string `json:"types"` // mapping types found (sorted); empty = absent
	Searchable   bool     `json:"searchable"`
	Aggregatable bool     `json:"aggregatable"`
	Configured   bool     `json:"configured"` // the operator-configured TraceID field
}

// TraceContextServiceCoverage is one service's share of logs carrying
// trace context in the report window.
type TraceContextServiceCoverage struct {
	Service   string `json:"service"`
	Total     int64  `json:"total"`
	WithTrace int64  `json:"withTrace"`
}

// TraceContextReport is the cross-backend self-discovery result.
// Available=false + Reason replaces raw errors — the admin surface renders
// the reason instead of a 5xx. Reason may also be set with Available=true
// when the field verdict succeeded but the coverage aggregation failed.
type TraceContextReport struct {
	Available      bool   `json:"available"`
	Reason         string `json:"reason,omitempty"`
	EffectiveField string `json:"effectiveField"`
	// EffectiveType: "keyword" | "text" | … | "absent" on ES; "String" on CH.
	EffectiveType string              `json:"effectiveType"`
	PivotReady    bool                `json:"pivotReady"`
	Fields        []TraceContextField `json:"fields"`

	// Coverage over the last WindowHours.
	WindowHours int                           `json:"windowHours"`
	Total       int64                         `json:"total"`
	WithTrace   int64                         `json:"withTrace"`
	Services    []TraceContextServiceCoverage `json:"services"`
}

// TraceContextDiagnoser is the optional per-backend capability behind the
// report. Both shipped backends implement it (ES via field_caps + one size:0
// aggregation, CH via two bounded countIf queries); the API layer still
// type-asserts through Unwrap — the Switchable deliberately does not forward
// optional capabilities (see switchable.go).
type TraceContextDiagnoser interface {
	TraceContextDiagnostics(ctx context.Context) (*TraceContextReport, error)
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

	// Histogram returns one bucketed timeseries per group_value for
	// the requested filter. Powers the Logs source in /explore — the
	// caller sets the bucket size (e.g. 30s, 5m) and an optional
	// `groupBy` field name (one of "service", "severity", or any
	// attribute path the backend knows). Empty groupBy → a single
	// "_total" series.
	Histogram(ctx context.Context, f Filter, bucketSec int, groupBy string) ([]LogSeries, error)

	// EQLSearch runs an Elastic Event Query Language sequence
	// detection against the log index — "event A then event B
	// within N minutes" expressions Coremetry can't otherwise
	// express via plain search. Returns the matched sequences,
	// each with its ordered event list. v0.5.468.
	//
	// CH backend returns an "unsupported" error — EQL is ES-
	// specific and the CH frontend hides the panel when the
	// backend reports non-ES.
	EQLSearch(ctx context.Context, q EQLQuery) ([]EQLSequence, error)

	// Indices lists the log indices the backend currently has,
	// with health + size + ILM lifecycle info per index. Powers
	// /admin/elastic so the operator sees at a glance which
	// indices are hot/warm/cold/frozen, what their ILM policy
	// says, and if any have gone red. CH backend returns nil
	// (no per-index concept). v0.5.466.
	Indices(ctx context.Context) ([]IndexInfo, error)

	// FieldValues returns top values of a single indexed field
	// matching a typed prefix. Backs the /logs search box's
	// field-aware autocomplete (v0.5.464): operator types
	// "service.name:" → dropdown shows suggested service names
	// from the data itself. Empty prefix returns the most common
	// values. ES backend uses _terms_enum for sub-ms prefix
	// lookups; CH backend returns [] for now (CH-native
	// DISTINCT-with-LIKE is straightforward but the KQL search
	// box is Kibana-flavoured already, less urgent on CH).
	FieldValues(ctx context.Context, field, prefix string, limit int) ([]string, error)

	// FieldStats returns the top-N values of one field within the
	// filtered window, with per-value counts and the total doc count
	// they were drawn from (Discover fields-panel accordion,
	// v0.8.255). Called lazily — only when the operator expands a
	// field — and cached 60s at the API layer, so it must never be
	// polled. ES: single bounded terms agg (keyword-preferring, one
	// bare-field retry when unmapped); CH: GROUP BY over the resolved
	// column/attribute with grouping caps.
	FieldStats(ctx context.Context, f Filter, field string, limit int) (*FieldStatsResult, error)

	// Backend returns a short identifier shown in /api/health so an operator
	// can tell at a glance which log source is wired in.
	Backend() string

	// Ping reports liveness of the underlying backend. Used by /api/status
	// to surface "logs backend is down" before the user runs into an
	// empty-result query.
	Ping(ctx context.Context) error
}

// FieldValueCount pairs one field value with its doc count in the
// window. Part of the FieldStats result (fields-panel accordion).
type FieldValueCount struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// FieldStatsResult — top values of one field plus the total docs
// they were counted from (top buckets + remainder), so the UI can
// render percentage bars without a second query.
type FieldStatsResult struct {
	Field  string            `json:"field"`
	Total  int64             `json:"total"`
	Values []FieldValueCount `json:"values"`
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

// ─── Cross-signal pivot reads (v0.8.330, pivot Phase 2) ─────────────────────
//
// LogsForTrace / LogsForSpan are the trace→log and span→log pivots. They are
// PACKAGE-LEVEL functions over the Store interface, not interface methods, on
// purpose: Go interfaces carry no default implementations, so interface
// methods would force three delegating copies (CH + ES + Switchable) of the
// same Filter construction — the exact duplication the audit ruled out.
// Implemented once here, every backend (and any future one) gets them for
// free through its Search.

// ErrBackendSlow is the sentinel a pivot caller matches with errors.Is to
// degrade gracefully: the log backend timed out or is unreachable — NOT a
// query error. The trace view must never block on the log backend (audit §3);
// callers return a partial result with a degraded flag instead of 5xx.
var ErrBackendSlow = errors.New("log backend slow/unreachable")

// PivotTimeout is the default per-request budget for pivot log reads. 3s —
// tighter than the backends' own 10s query knobs on purpose: a pivot is one
// tab of a trace view already on screen, so past ~3s an empty "backend slow"
// tab beats a spinner holding the whole view hostage.
const PivotTimeout = 3 * time.Second

// SearchWithTimeout runs one Search under a per-request deadline (timeout
// ≤ 0 → PivotTimeout) and maps "the backend is slow or unreachable" —
// deadline exceeded, transport timeout, dial/connection failures — to
// ErrBackendSlow (wrapped, so the original cause stays visible in logs).
// Genuine query errors (bad field, ES 400, …) pass through unchanged: those
// are bugs to surface, not conditions to degrade on.
func SearchWithTimeout(ctx context.Context, st Store, f Filter, timeout time.Duration) (*Page, error) {
	if timeout <= 0 {
		timeout = PivotTimeout
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	page, err := st.Search(tctx, f)
	if err == nil {
		return page, nil
	}
	return nil, MapBackendSlow(err, tctx, ctx)
}

// MapBackendSlow classifies a failed backend call: slow/unreachable →
// ErrBackendSlow (wrapped, original cause preserved for logs), genuine
// query errors (ES 400, bad field, …) pass through unchanged. v0.8.350
// (HA 🟡6) — exported so the /logs search, histogram, field-stats and
// context handlers can extend the v0.8.331 trace-pivot degrade contract
// (200 {degraded:true} instead of 5xx) to calls that aren't plain
// Search (Histogram, FieldStats).
//
// opCtx is the context the backend call actually ran under (usually a
// WithTimeout child); parent is the caller's context. Our own deadline
// firing can surface as a backend-specific error string (the CH driver
// wraps ctx errors), so opCtx is checked directly — but only while the
// PARENT is still live: a client disconnect (context.Canceled all the
// way up) keeps its honest cancellation error, so healthy backends
// never get degraded payloads cached on their behalf.
func MapBackendSlow(err error, opCtx, parent context.Context) error {
	if err == nil {
		return nil
	}
	if isBackendSlow(err) || (opCtx.Err() == context.DeadlineExceeded && parent.Err() == nil) {
		return fmt.Errorf("%w: %v", ErrBackendSlow, err)
	}
	return err
}

// isBackendSlow classifies an error as "slow/unreachable" (→ ErrBackendSlow)
// vs a genuine query failure. Deadline exceeded covers both backends' ctx
// plumbing; net.Error timeouts and *net.OpError cover the ES/CH transports'
// dial-refused / no-route / reset shapes (the go-elasticsearch client returns
// them wrapped in *url.Error, which errors.As unwraps).
func isBackendSlow(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	var operr *net.OpError
	return errors.As(err, &operr)
}

// pivotLogLimit defaults/caps the pivot read size. 500 matches the
// TracePeekDrawer / correlate-bundle log cap — a trace's Logs tab reads
// density, not an export.
func pivotLogLimit(limit int) int {
	if limit <= 0 || limit > 500 {
		return 500
	}
	return limit
}

// LogsForTrace returns the logs carrying one trace id, under the pivot
// timeout semantics (ErrBackendSlow on a slow/unreachable backend).
//
// Window semantics (the correlate.go finding, kept caller-visible): ES
// ignores From/To when TraceID is set (a trace link can be older than any
// default slice); CH AND-applies them. Pass ZERO from/to to make trace_id
// the sole filter on both backends; pass a bounded window on CH installs to
// help the partition scan (the logs table has no trace_id skip index yet —
// audit §1).
func LogsForTrace(ctx context.Context, st Store, traceID string, from, to time.Time, limit int) (*Page, error) {
	if traceID == "" {
		return nil, fmt.Errorf("traceID is required")
	}
	return SearchWithTimeout(ctx, st, Filter{
		TraceID: traceID,
		From:    from,
		To:      to,
		Limit:   pivotLogLimit(limit),
	}, 0)
}

// LogsForSpan narrows LogsForTrace to one span (trace_id AND span_id — both
// backends translate Filter.SpanID). Same timeout + window semantics.
func LogsForSpan(ctx context.Context, st Store, traceID, spanID string, from, to time.Time, limit int) (*Page, error) {
	if traceID == "" || spanID == "" {
		return nil, fmt.Errorf("traceID and spanID are required")
	}
	return SearchWithTimeout(ctx, st, Filter{
		TraceID: traceID,
		SpanID:  spanID,
		From:    from,
		To:      to,
		Limit:   pivotLogLimit(limit),
	}, 0)
}
