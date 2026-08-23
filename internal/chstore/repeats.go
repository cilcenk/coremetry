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
//
// RootName resolves in two layers (v0.9.1288): the trace's actual
// root span, or — when that root started before the queried window
// and so is not in the joined slice — the earliest span still
// visible. It is therefore best read as "the entry point this
// repetition hangs off", not strictly "parent_id is empty".
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
	Filters    []FilterExpr
	GroupBy    []string
	MinRepeats int
	From, To   time.Time
	Limit      int
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

	q := repeatedSpansSQL(keysArrayLiteral, wc.sql())
	args := repeatedSpansArgs(keyArgs, wc.args, f.MinRepeats, limit, f.From, f.To)

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

// repeatedSpansSQL builds the N+1 finder's two-pass query. Pure so
// the shape can be pinned by a test (repeats_join_test.go) —
// v0.9.1285 shipped the extraction precisely because the join shape
// below is the part that regressed and a text pin over the whole
// file would not have proved the builder emits it.
//
// `keysArrayLiteral` is the Array(String) group-key projection and
// `whereSQL` is the already-rendered inner WHERE (time bounds +
// operator filters). Both arrive with their `?` placeholders in
// place; the caller binds in TEXT order, so the placeholder
// positions here are load-bearing — the two trailing `?` in the
// joined subquery stand where the old ON-clause bounds stood.
//
// v0.9.1285 — two distributed-cluster defects lived on this join,
// and between them the whole Explore "Repeats" mode returned HTTP
// 500 on every window against an external Distributed ClickHouse:
//
//  1. UNBOUNDED RIGHT SIDE. The bounds used to sit in the JOIN ON
//     clause (`... AND s.time >= ? AND s.time <= ?`). ON-clause
//     predicates on the right table are join conditions, not scan
//     filters — ClickHouse cannot use them for partition or
//     primary-key pruning, so the right side was a full scan of the
//     ENTIRE spans table to answer a 10-minute question. Measured
//     on the local 2-shard cluster: 22.69M rows / 1.58 GiB / 20.5s,
//     which hit the max_execution_time ceiling and 500'd, versus
//     75.65K rows / 11.34 MiB / 0.056s median once the bound moved
//     INTO a subquery. The bound belongs in a subquery WHERE, never
//     in ON.
//
//  2. NO GLOBAL. `spans` is Distributed with a rand() sharding key,
//     so one trace's spans scatter across every shard. A non-GLOBAL
//     join re-runs the right side shard-locally and each shard sees
//     only its own slice, so the resolved service / root-op comes
//     from a partial trace. GLOBAL is the JOIN twin of the house
//     GLOBAL IN rule (v0.5.427); on a single-node install the two
//     forms are semantically identical.
//
// v0.9.1287 — the HAVING carries a second conjunct, and it is a
// correctness fix, not a cosmetic one. The group key is a tuple; when
// EVERY element of that tuple is the empty string the row is not a
// span shape at all, it is "the spans in this trace that carry none
// of the requested attributes". Grouping by `db.statement` collapses
// every non-DB span in a trace — HTTP handlers, internal work, queue
// consumers — into one empty bucket, and that bucket then counts as a
// repeat of itself.
//
// Measured live on the 2-shard local cluster at v0.9.1286,
// GET /api/spans/repeats?groupBy=db.statement&minRepeats=5 over 1h:
// 2837 candidate groups, of which 2721 (95.9%) were the all-empty
// tuple at 19-20 spans apiece. They are individually smaller than the
// real N+1 (40×) but they are so numerous that they ate the LIMIT
// 200: the response held 146 blank rows against 54 real ones, and 62 of
// the 116 genuinely repeating statements never reached the operator
// at all. The top of Explore's Repeats list was blank rows.
//
// PARTIAL-empty tuples are deliberately KEPT. `arrayExists` asks for
// ANY non-empty element, not all of them. groupBy=name,http.route on
// the same window is 116 rows shaped ["SELECT instrument_prices", ""]
// — a DB span has no HTTP route, and that absence is a real property
// of a real shape. Only a tuple with no identity anywhere is dropped.
//
// The conjunct is bind-arg free on purpose. repeatedSpansArgs binds
// positionally in SQL TEXT order and v0.9.1286 shipped a 500 because
// a group-key arg drifted one slot; a filter that needs no `?` cannot
// reopen that. It also sits in HAVING rather than WHERE so it applies
// after aggregation, where the tuple actually exists — and therefore
// before ORDER BY / LIMIT, which is what hands the 200 slots back to
// real rows.
//
// v0.9.1288 — the root-operation column was ALWAYS blank. It used to
// read
//
//	any(if(s.parent_id = '', s.name, ''))
//
// and the flaw is that `any` picks an arbitrary row out of the joined
// set and only THEN asks whether that row was the root. A trace that repeats a
// statement 40× has ~45 spans and exactly one root, so the arbitrary
// pick lands on a non-root roughly 44 times out of 45 and the `if`
// collapses it to the empty string. Measured live on the 2-shard
// local cluster at v0.9.1287,
// GET /api/spans/repeats?groupBy=db.statement&minRepeats=5 over 1h:
// 200 of 200 rows (100%) came back with an empty rootName, so Explore's
// Repeats table could not tell the operator WHICH entry point was
// generating the N+1 — the whole point of the column.
//
// The filter has to live INSIDE the aggregate, not after it. The fix
// is two layers, because "the root span" is not guaranteed to be in
// the joined set:
//
//  1. PRIMARY — anyIf, carrying the root test as the aggregate's OWN
//     condition, picks a row from the subset that actually IS the
//     root, so it degrades to the empty string only when no root was
//     joined at all.
//
//  2. FALLBACK — the joined subquery is time-bounded by the SAME
//     window the operator asked for (v0.9.1285 put the bound there on
//     purpose). A trace that STARTED before `from` and repeated the
//     statement inside the window is fully legitimate, but its root
//     span is outside the bound and layer 1 sees nothing. Rather than
//     widen the scan — which is precisely the unbounded right side
//     v0.9.1285 removed — fall back to `argMin(s.name, s.time)`, the
//     earliest span still visible. Same measurement: 35 of the 200
//     rows (17.5%) resolve through this layer, and they name the real
//     entry into the visible slice (`session-gateway/Validate`) rather
//     than nothing.
//
// The two layers are spelled inline rather than via SELECT aliases
// because every item in this SELECT list is a scanned column — the Go
// side reads exactly seven — so an alias for the intermediate value
// would add a column the scanner does not expect. ClickHouse folds the
// repeated `anyIf` itself.
//
// `time` joins the subquery projection for layer 2. It adds no `?`, so
// the positional bind contract (v0.9.1286) is untouched.
func repeatedSpansSQL(keysArrayLiteral, whereSQL string) string {
	return `
		WITH dup_traces AS (
			SELECT trace_id,
			       ` + keysArrayLiteral + ` AS group_values,
			       count()                      AS cnt,
			       sum(duration) / 1e6          AS total_ms,
			       min(time)                    AS earliest
			FROM spans ` + whereSQL + `
			GROUP BY trace_id, group_values
			HAVING cnt >= ? AND arrayExists(x -> x != '', group_values)
			ORDER BY cnt DESC
			LIMIT ?
		)
		SELECT d.trace_id, d.group_values, d.cnt, d.total_ms, toUnixTimestamp64Nano(d.earliest),
		       any(s.service_name),
		       if(anyIf(s.name, s.parent_id = '') != '',
		          anyIf(s.name, s.parent_id = ''),
		          argMin(s.name, s.time))
		FROM dup_traces d
		GLOBAL LEFT JOIN (
			SELECT trace_id, service_name, parent_id, name, time
			FROM spans
			WHERE time >= ? AND time <= ?
		) AS s ON s.trace_id = d.trace_id
		GROUP BY d.trace_id, d.group_values, d.cnt, d.total_ms, d.earliest
		ORDER BY d.cnt DESC
		SETTINGS max_execution_time = 20,
		         distributed_product_mode = 'global'`
}

// repeatedSpansArgs binds repeatedSpansSQL's placeholders. Pure and
// separate from the caller because binding is POSITIONAL: the list
// has to follow the order the `?` appear in the SQL TEXT, and the
// SQL text is the pure function above.
//
// Text order is: group-key projection (inner SELECT) → inner WHERE →
// HAVING → LIMIT → the joined subquery's from/to.
//
// v0.9.1286 — this used to append the where args FIRST, so any
// group-by carrying a bind arg got the From timestamp shoved into
// its attr-name slot. Well-known keys (db.statement → the
// `db_statement` column) emit no bind arg at all, which is exactly
// why the default group-by hid the defect and every other pick 500'd:
//
//	GET /api/spans/repeats?groupBy=resource.k8s.namespace.name
//	  → 500 code 386, "There is no supertype for types String,
//	    DateTime ... indexOf(res_keys, toDateTime('2026-08-22 ...'))"
//
// The three sibling group-by builders (spanmetric.go ×2,
// exemplar_otlp.go) all fold the group args into the FRONT and carry
// a comment saying why; repeats.go was the one that had it backwards.
func repeatedSpansArgs(keyArgs, whereArgs []any, minRepeats, limit int, from, to time.Time) []any {
	args := make([]any, 0, len(keyArgs)+len(whereArgs)+4)
	args = append(args, keyArgs...)
	args = append(args, whereArgs...)
	return append(args, minRepeats, limit, from, to)
}
