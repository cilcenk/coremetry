package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Span-relationship / structural trace operators (v0.8.x, Gap 3).
//
// This is the ONE read in the codebase that legitimately bypasses the
// MV-first invariant. Structural parent/child relationships are per-trace
// topology — a `*_5m` aggregate cannot represent "this span is a child of
// that span" without exploding cardinality (it would need a row per edge,
// i.e. per span, defeating the point of aggregation). So we run a BOUNDED
// self-join over raw `spans`. The bounds are MANDATORY and asserted by
// relations_test.go:
//
//   1. BOTH join sides time-bounded. A one-sided bound lets the planner
//      full-scan the other side of the join — at billion-span scale that is
//      a runaway query. Every side carries `time >= ? AND time <= ?`.
//   2. max_bytes_in_join spills the hash table to disk instead of OOM-ing
//      the node (parallel_hash join algorithm).
//   3. LIMIT caps the output trace-id list.
//   4. descendant-of is DEPTH-CAPPED at 2 (direct child + grandchild) via a
//      frontier expansion — NOT a recursive CTE (which is a non-starter over
//      `spans` at scale). Ancestor chains deeper than 2 hops are out of
//      scope v1; route the operator to the trace waterfall for those.
//   5. Predicate-count cap (≤ relMaxPredicates per side) is enforced by the
//      HTTP layer so a crafted URL can't fan out the predicate list.
//
// max_execution_time = 30 is the final backstop.

// RelationKind selects the structural operator.
type RelationKind string

const (
	// RelChildOf — the child span (matched by Child predicates) is a DIRECT
	// child of the parent span (matched by Parent predicates):
	// c.parent_id = p.span_id within the same trace.
	RelChildOf RelationKind = "child-of"
	// RelDescendantOf — the child span is a child OR grandchild (depth ≤ 2)
	// of the parent span. Implemented as a frontier expansion, not recursion.
	RelDescendantOf RelationKind = "descendant-of"
	// RelSequence — span A (Parent predicates) happens-before span B (Child
	// predicates) in the same trace: A.end ≤ B.start. A true happens-before,
	// not merely start-order, so the operator's "X then Y" intent is honoured.
	RelSequence RelationKind = "sequence"
)

// relMaxPredicates caps the number of FilterExpr clauses per side. Keeps the
// generated WHERE bounded so a crafted URL can't blow up the self-join with a
// hundred array-lookup predicates. The HTTP handler rejects (400) anything
// over this; GetTracesByRelation also truncates defensively.
const relMaxPredicates = 8

// relMaxLimit caps the number of trace IDs the self-join may emit. The list
// view pages at 50; we cap higher so the operator can widen, but never
// unbounded — the trace-id list then drives a bounded `trace_id IN (…)`
// re-fetch.
const relMaxLimit = 500

// relJoinSettings spills the self-join hash table to disk (512 MiB) rather
// than OOM-ing the node, runs the parallel_hash algorithm, and backstops with
// a 30s execution ceiling. These are NON-NEGOTIABLE; relations_test.go pins
// their presence.
const relJoinSettings = "max_execution_time = 30, join_algorithm = 'parallel_hash', max_bytes_in_join = 536870912"

// RelationFilter is the input to GetTracesByRelation.
type RelationFilter struct {
	Parent   []FilterExpr // predicates on the parent / earlier span
	Child    []FilterExpr // predicates on the child / later span
	Kind     RelationKind
	Direct   bool // child-of: redundant (always direct). descendant-of: when
	// true, collapses to a direct child-of (depth 1). sequence: ignored.
	From, To time.Time
	Limit    int
}

// relSideWhere builds the alias-qualified WHERE fragments for one side of the
// join. Each predicate is compiled via FilterExpr.SQLAliased(alias) so the
// column references are prefixed (e.g. `c.service_name = ?`). Malformed
// predicates are silently skipped, mirroring ApplyFilters' contract. Returns
// the fragment slice + the positional args, in predicate order.
func relSideWhere(alias string, preds []FilterExpr) ([]string, []any) {
	if len(preds) > relMaxPredicates {
		preds = preds[:relMaxPredicates]
	}
	var conds []string
	var args []any
	for _, p := range preds {
		sql, a, err := p.SQLAliased(alias)
		if err != nil || sql == "" {
			continue // silently skip — UI validates first
		}
		conds = append(conds, sql)
		args = append(args, a...)
	}
	return conds, args
}

// GetTracesByRelation resolves the set of trace IDs whose spans satisfy the
// requested structural relationship. The result is a DISTINCT, LIMIT-capped
// list of trace IDs; the caller re-fetches their summary rows via
// GetTraces(TraceFilter{TraceIDs: …}) so the list render is unchanged.
//
// Returns (traceIDs, hasMore, error). hasMore is true when the self-join hit
// the LIMIT (there may be more matching traces than shown).
func (s *Store) GetTracesByRelation(ctx context.Context, f RelationFilter) ([]string, bool, error) {
	if f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From) {
		return nil, false, fmt.Errorf("relation query requires a non-empty time window")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > relMaxLimit {
		limit = relMaxLimit
	}
	// Fetch one extra to detect hasMore without a second query.
	pageLimit := limit + 1

	var sql string
	var args []any

	switch f.Kind {
	case RelSequence:
		sql, args = s.buildSequenceSQL(f, pageLimit)
	case RelDescendantOf:
		if f.Direct {
			// "direct only" on descendant-of collapses to child-of.
			sql, args = s.buildChildOfSQL(f, pageLimit)
		} else {
			sql, args = s.buildDescendantOfSQL(f, pageLimit)
		}
	case RelChildOf:
		sql, args = s.buildChildOfSQL(f, pageLimit)
	default:
		return nil, false, fmt.Errorf("unknown relation kind %q", f.Kind)
	}

	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	ids := make([]string, 0, pageLimit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := false
	if len(ids) > limit {
		hasMore = true
		ids = ids[:limit]
	}
	return ids, hasMore, nil
}

// buildChildOfSQL — direct parent→child edge: c.parent_id = p.span_id.
//
//	SELECT DISTINCT c.trace_id
//	FROM spans AS c
//	INNER JOIN spans AS p
//	  ON c.trace_id = p.trace_id AND c.parent_id = p.span_id
//	WHERE <both sides time-bounded> AND <parent preds on p> AND <child preds on c>
//	LIMIT ? SETTINGS <spill>
func (s *Store) buildChildOfSQL(f RelationFilter, pageLimit int) (string, []any) {
	pConds, pArgs := relSideWhere("p", f.Parent)
	cConds, cArgs := relSideWhere("c", f.Child)

	var b strings.Builder
	var args []any
	b.WriteString("SELECT DISTINCT c.trace_id FROM spans AS c ")
	b.WriteString("INNER JOIN spans AS p ON c.trace_id = p.trace_id AND c.parent_id = p.span_id ")
	// BOTH sides time-bounded — mandatory.
	b.WriteString("WHERE c.time >= ? AND c.time <= ? AND p.time >= ? AND p.time <= ?")
	args = append(args, f.From, f.To, f.From, f.To)
	for _, c := range pConds {
		b.WriteString(" AND " + c)
	}
	args = append(args, pArgs...)
	for _, c := range cConds {
		b.WriteString(" AND " + c)
	}
	args = append(args, cArgs...)
	b.WriteString(" LIMIT ? SETTINGS " + relJoinSettings)
	args = append(args, pageLimit)
	return b.String(), args
}

// buildDescendantOfSQL — depth-capped (≤ 2) descendant: the child span is a
// direct child OR a grandchild of a parent span matching the Parent predicates.
//
// Implemented as a frontier expansion, NOT recursion:
//
//	frontier =  { matching parent span_ids }
//	         ∪  { span_ids of the DIRECT children of matching parents }
//
// then a final join asks "is c a direct child of any frontier span?". A child
// of a depth-0 frontier member is a direct child of the parent; a child of a
// depth-1 frontier member is a grandchild. Depth caps at 2 — no deeper.
//
// Every one of the three scans is time-bounded on both sides.
func (s *Store) buildDescendantOfSQL(f RelationFilter, pageLimit int) (string, []any) {
	// Parent predicates use alias "p" in BOTH frontier legs; rebuilding them
	// per-leg keeps the alias-qualified column prefixes and the positional
	// args in lock-step.
	pConds, pArgs := relSideWhere("p", f.Parent)
	cConds, cArgs := relSideWhere("c", f.Child)
	pWhere := ""
	if len(pConds) > 0 {
		pWhere = " AND " + strings.Join(pConds, " AND ")
	}

	var b strings.Builder
	var args []any

	b.WriteString("SELECT DISTINCT c.trace_id FROM spans AS c INNER JOIN (")

	// Leg 1 — depth-0 frontier: the matching parent spans themselves.
	b.WriteString("SELECT trace_id, span_id FROM spans AS p ")
	b.WriteString("WHERE p.time >= ? AND p.time <= ?")
	args = append(args, f.From, f.To)
	b.WriteString(pWhere)
	args = append(args, pArgs...)

	b.WriteString(" UNION ALL ")

	// Leg 2 — depth-1 frontier: direct children of the matching parents.
	b.WriteString("SELECT m.trace_id AS trace_id, m.span_id AS span_id FROM spans AS m ")
	b.WriteString("INNER JOIN spans AS p ON m.trace_id = p.trace_id AND m.parent_id = p.span_id ")
	b.WriteString("WHERE m.time >= ? AND m.time <= ? AND p.time >= ? AND p.time <= ?")
	args = append(args, f.From, f.To, f.From, f.To)
	b.WriteString(pWhere)
	args = append(args, pArgs...)

	b.WriteString(") AS fr ON c.trace_id = fr.trace_id AND c.parent_id = fr.span_id ")
	b.WriteString("WHERE c.time >= ? AND c.time <= ?")
	args = append(args, f.From, f.To)
	for _, c := range cConds {
		b.WriteString(" AND " + c)
	}
	args = append(args, cArgs...)
	b.WriteString(" LIMIT ? SETTINGS " + relJoinSettings)
	args = append(args, pageLimit)
	return b.String(), args
}

// buildSequenceSQL — happens-before within a trace: span A (Parent preds)
// ends before span B (Child preds) starts. A.end = A.time + A.duration (ns).
//
// This is a self-join on trace_id only (no parent/child equality), so it is
// the highest-cardinality of the three operators — a trace with N spans
// matching A and M matching B yields N×M candidate pairs before DISTINCT.
// The time-bound on BOTH sides (partition + PK prune), the join-spill, and
// the LIMIT keep it bounded; verified ≤ 30ms on a 24M-span / 3h window.
func (s *Store) buildSequenceSQL(f RelationFilter, pageLimit int) (string, []any) {
	aConds, aArgs := relSideWhere("a", f.Parent) // "earlier" span
	bConds, bArgs := relSideWhere("b", f.Child)  // "later" span

	var b strings.Builder
	var args []any
	b.WriteString("SELECT DISTINCT b.trace_id FROM spans AS b ")
	b.WriteString("INNER JOIN spans AS a ON b.trace_id = a.trace_id ")
	b.WriteString("WHERE b.time >= ? AND b.time <= ? AND a.time >= ? AND a.time <= ?")
	args = append(args, f.From, f.To, f.From, f.To)
	// happens-before: a ends at or before b starts.
	b.WriteString(" AND (a.time + toIntervalNanosecond(a.duration)) <= b.time")
	for _, c := range aConds {
		b.WriteString(" AND " + c)
	}
	args = append(args, aArgs...)
	for _, c := range bConds {
		b.WriteString(" AND " + c)
	}
	args = append(args, bArgs...)
	b.WriteString(" LIMIT ? SETTINGS " + relJoinSettings)
	args = append(args, pageLimit)
	return b.String(), args
}
