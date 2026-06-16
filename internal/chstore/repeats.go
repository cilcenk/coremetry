package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RepeatedSpanRow is one row of the "find repeated work inside a
// single trace" view — the N+1 detector. Each row is a
// (trace_id, group-by tuple) pair where the same span shape
// appeared `Count` times within the same trace.
//
// Classic use cases:
//   - Same SQL run 50× in one request → N+1 query smell
//   - Same `<peer.service, name>` chained 30× → chatty
//     downstream call from one upstream request
//   - Same HTTP route hit 20× → fan-out loop calling itself
//
// `Service` + `RootName` carry the originating service and the
// root operation name so the UI doesn't need a second lookup to
// label the row.
type RepeatedSpanRow struct {
	TraceID     string   `json:"traceId"`
	Service     string   `json:"service"`
	RootName    string   `json:"rootName"`
	GroupValues []string `json:"groupValues"` // parallel to filter's GroupBy
	Count       uint64   `json:"count"`
	TotalMs     float64  `json:"totalDurationMs"`
	StartedAt   int64    `json:"startedAt"` // unix ns of the trace's earliest span
}

// RepeatedSpanFilter is the input shape for the repeated-spans
// finder. MinRepeats defaults to 5 (operators rarely care about
// 2-3× duplicates); GroupBy is what defines "same span shape" —
// typical picks: ["db.statement"] for SQL N+1, ["name",
// "peer.service"] for chatty RPC, ["http.route"] for endpoint
// fan-out.
type RepeatedSpanFilter struct {
	Filters     []FilterExpr
	GroupBy     []string
	MinRepeats  int
	From, To    time.Time
	Limit       int
}

// QueryRepeatedSpans runs one GROUP BY (trace_id, <groupBy>)
// HAVING count >= MinRepeats pass over the spans table. Bounded
// at LIMIT 200 server-side so a wide window with many duplicate-
// heavy traces doesn't blow up the response.
//
// Performance posture: GROUP BY on a hash of (trace_id +
// groupBy) is cheap at billion-span scale because the time
// filter prunes by partition first; uniqExact + sum aggregates
// fit in memory inside a single 20s max_execution_time budget.
// `min(time)` per group surfaces the earliest span so the UI
// can show "when did this duplicate burst start".
func (s *Store) QueryRepeatedSpans(ctx context.Context, f RepeatedSpanFilter) ([]RepeatedSpanRow, error) {
	if f.From.IsZero() {
		f.From = time.Now().Add(-1 * time.Hour)
	}
	if f.To.IsZero() {
		f.To = time.Now()
	}
	if f.MinRepeats <= 0 {
		f.MinRepeats = 5
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if len(f.GroupBy) == 0 {
		// Sensible default — SQL is the most common N+1 source.
		f.GroupBy = []string{"db.statement"}
	}

	var wc whereClause
	wc.add("time >= ?", f.From)
	wc.add("time <= ?", f.To)
	ApplyFilters(&wc, f.Filters)

	// Build GROUP BY expression list and the parallel SELECT
	// projection that brings the same expressions out so we
	// can scan them per-row.
	keyExprs := make([]string, len(f.GroupBy))
	var keyArgs []any
	for i, k := range f.GroupBy {
		expr, args := groupKeyExpr(k, s.hasOpGroupCol)
		keyExprs[i] = expr
		keyArgs = append(keyArgs, args...)
	}
	// Wrap the group-by projections in an Array(String) so the
	// Go-side scan target is always []string regardless of how
	// many keys the operator picked. A bare string vs. a Tuple
	// would force two scanner paths.
	keysArrayLiteral := "[" + strings.Join(keyExprs, ", ") + "]"

	// The inner query does the per-(trace_id, group-by) count;
	// the outer ANY LEFT JOIN looks up the trace's root span
	// service + name + earliest time, indexed via (service_name,
	// time) primary key on spans. Two passes but bounded — the
	// outer LIMIT 200 caps the join's right side at 200 rows.
	q := `
		WITH dup_traces AS (
			SELECT trace_id,
			       ` + keysArrayLiteral + ` AS group_values,
			       count()                      AS cnt,
			       sum(duration) / 1e6          AS total_ms,
			       min(time)                    AS earliest
			FROM spans ` + wc.sql() + `
			GROUP BY trace_id, group_values
			HAVING cnt >= ?
			ORDER BY cnt DESC
			LIMIT ?
		)
		SELECT d.trace_id, d.group_values, d.cnt, d.total_ms, toUnixTimestamp64Nano(d.earliest),
		       any(s.service_name), any(if(s.parent_id = '', s.name, ''))
		FROM dup_traces d
		LEFT JOIN spans s ON s.trace_id = d.trace_id AND s.time >= ? AND s.time <= ?
		GROUP BY d.trace_id, d.group_values, d.cnt, d.total_ms, d.earliest
		ORDER BY d.cnt DESC
		SETTINGS max_execution_time = 20`

	args := append([]any{}, wc.args...)
	args = append(args, keyArgs...)
	args = append(args, f.MinRepeats, limit, f.From, f.To)

	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RepeatedSpanRow{}
	for rows.Next() {
		var r RepeatedSpanRow
		var rootName string
		if err := rows.Scan(&r.TraceID, &r.GroupValues, &r.Count, &r.TotalMs, &r.StartedAt, &r.Service, &rootName); err != nil {
			return nil, fmt.Errorf("scan repeat row: %w", err)
		}
		r.RootName = rootName
		out = append(out, r)
	}
	return out, rows.Err()
}
