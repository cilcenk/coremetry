package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Exemplar lookup — given a chart point the user clicked on
// (service + optional operation + time bucket + slow/error
// flavour), pick a single representative span and return its
// trace_id so the UI can drill straight from the metric line
// into the actual trace that produced it.
//
// Why this matters: looking at a P99 latency spike at 14:32 is
// not actionable. Looking at *the* trace that landed at that
// percentile in that 30-second bucket is. This is the standard
// Datadog / Honeycomb / Grafana exemplar pattern.
//
// Picking strategy:
//   - kind = "slow":   ORDER BY duration DESC LIMIT 1 — the
//     single slowest span matching the filter. Best mapped to
//     a P99 / max chart click.
//   - kind = "error":  same, restricted to status_code='error'.
//     The "open the loudest failing trace" button.
//   - kind = "any":    duration-desc on the bucket. Most users
//     want the slow-or-error one, but "any" is here for
//     count/rate charts where the chart datum is just "stuff
//     happened".
//
// Performance: leans on spans.ORDER BY (service_name, time)
// primary key. Adding service_name + time predicates lets CH
// prune partitions and skip-index granules; LIMIT 1 short-
// circuits past the first match. Sub-100ms even on
// billion-span tables.

type ExemplarKind string

const (
	ExemplarSlow  ExemplarKind = "slow"
	ExemplarError ExemplarKind = "error"
	ExemplarAny   ExemplarKind = "any"
)

type ExemplarReq struct {
	Service   string       // service_name (required)
	Operation string       // span name (optional — empty = any op)
	From      time.Time    // bucket start (inclusive)
	To        time.Time    // bucket end (inclusive)
	Kind      ExemplarKind // slow | error | any
}

type Exemplar struct {
	TraceID    string `json:"traceId"`
	SpanID     string `json:"spanId"`
	Service    string `json:"service"`
	Name       string `json:"name"`
	DurationNs int64  `json:"durationNs"`
	StatusCode string `json:"statusCode"`
	TimeUnixNs int64  `json:"timeUnixNs"`
}

// FindExemplarRollup resolves the representative trace_id for (service, window,
// kind) straight off the spanmetrics rollup's argMax(If) exemplar states — the
// EXACT same per-bucket exemplar trace_id Explore's ◆ glyphs use, and the most
// precise + cheapest exemplar we have: one MV read over spanmetrics_1m (the
// always-present 30d tier), no raw-spans scan.
//
// slow_exemplar_state is argMaxState(trace_id, duration) and
// error_exemplar_state is argMaxIfState(trace_id, duration, status_code='error')
// — argMaxMerge over the whole window collapses every bucket's per-bucket winner
// into THE single max-duration (or max-duration-among-errors) trace for the
// span, so one row gives the representative trace directly. Combinator contract:
// argMaxMerge for the slow state, argMaxIfMerge for the If-state (mixing them up
// fails at runtime — the v0.8.51 catch).
//
// Returns (nil, nil) — the soft "not found" posture — when the window predates
// the rollup cutover / has TTL'd away / has no exemplar for that span, so the
// caller can fall through to FindExemplar (raw spans, still real). The result
// carries only TraceID + Service (the rollup exemplar state is a trace_id, not a
// span row); the caller pivots on the trace_id, which is the precise part.
//
// Bound: spanmetrics_1m is an AggregatingMergeTree MV — the WHERE is
// service_name equality + a time_bucket range on the ORDER BY prefix, so CH
// prunes to the relevant granules. LIMIT 1 + max_execution_time cap it.
func (s *Store) FindExemplarRollup(ctx context.Context, req ExemplarReq) (*Exemplar, error) {
	if strings.TrimSpace(req.Service) == "" {
		return nil, fmt.Errorf("service is required")
	}
	if req.From.IsZero() || req.To.IsZero() {
		return nil, fmt.Errorf("from/to are required")
	}

	// Pick the exemplar-state finalizer per kind. Throughput ("any") has no
	// representative span — the slow state is the most useful stand-in (the
	// loudest trace in the window) but the caller labels that join weak.
	var traceExpr string
	switch req.Kind {
	case ExemplarError:
		traceExpr = "argMaxIfMerge(error_exemplar_state)"
	case ExemplarSlow, ExemplarAny, "":
		traceExpr = "argMaxMerge(slow_exemplar_state)"
	default:
		return nil, fmt.Errorf("unknown exemplar kind %q", req.Kind)
	}

	sql := fmt.Sprintf(`
		SELECT %s AS trace_id
		FROM %s
		WHERE service_name = ? AND time_bucket >= ? AND time_bucket <= ?
		LIMIT 1
		SETTINGS max_execution_time = 10`, traceExpr, s.spanmetricsSourceFor("spanmetrics_1m"))

	row := s.conn.QueryRow(ctx, sql, req.Service, req.From, req.To)
	var traceID string
	if err := row.Scan(&traceID); err != nil {
		// Empty / no-exemplar rollup surfaces as a scan error — same clean
		// "not found" treatment FindExemplar uses. Caller falls back.
		return nil, nil
	}
	if strings.TrimSpace(traceID) == "" {
		// argMaxIfMerge over an all-non-error window yields the empty default.
		return nil, nil
	}
	return &Exemplar{TraceID: traceID, Service: req.Service}, nil
}

func (s *Store) FindExemplar(ctx context.Context, req ExemplarReq) (*Exemplar, error) {
	if strings.TrimSpace(req.Service) == "" {
		return nil, fmt.Errorf("service is required")
	}
	if req.From.IsZero() || req.To.IsZero() {
		return nil, fmt.Errorf("from/to are required")
	}

	var conds []string
	args := []any{req.Service, req.From, req.To}
	conds = append(conds,
		"service_name = ?",
		"time >= ?",
		"time <= ?",
	)
	if op := strings.TrimSpace(req.Operation); op != "" {
		conds = append(conds, "name = ?")
		args = append(args, op)
	}

	switch req.Kind {
	case ExemplarError:
		conds = append(conds, "status_code = 'error'")
	case ExemplarSlow, ExemplarAny, "":
		// no additional predicate
	default:
		return nil, fmt.Errorf("unknown exemplar kind %q", req.Kind)
	}

	sql := fmt.Sprintf(`
		SELECT trace_id, span_id, service_name, name,
		       duration, status_code,
		       toUnixTimestamp64Nano(time) AS time_ns
		FROM spans
		WHERE %s
		ORDER BY duration DESC
		LIMIT 1`, strings.Join(conds, " AND "))

	row := s.conn.QueryRow(ctx, sql, args...)
	var e Exemplar
	var timeNs int64
	if err := row.Scan(&e.TraceID, &e.SpanID, &e.Service, &e.Name,
		&e.DurationNs, &e.StatusCode, &timeNs); err != nil {
		// Empty result set surfaces here as scan error — same
		// pattern used elsewhere in chstore (anomaly_event.go,
		// incident.go). Treat as a clean "not found".
		return nil, nil
	}
	e.TimeUnixNs = timeNs
	return &e, nil
}
