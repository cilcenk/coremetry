package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// BubbleUp — Honeycomb's signature attribute investigation
// pattern. Given a "selected" subset of spans (e.g. the slow
// ones, the failing ones, a single histogram cell) and the
// "baseline" population (all spans matching the surrounding
// filter in the same window), find attributes whose value
// distribution differs most strongly between the two sets.
//
// The output answers "what's special about THESE spans" —
// usually one of {host=podX, http.route=/checkout,
// db.system=oracle} jumps out as 80% of the selection vs
// 5% of the baseline, and the operator has the smoking gun
// without doing N attribute filters by hand.
//
// Mechanism:
//   1. We sample N attribute keys observed on either set
//      (capped to 30 to keep the GROUP BY bounded).
//   2. For each (key, value) pair we count selection and
//      baseline rows in one CH query — uses the existing
//      attr_keys/attr_values arrays + arrayJoin.
//   3. Score each value by `selectionPct - baselinePct`
//      so values that are over-represented in the selection
//      surface first. We keep only values with a meaningful
//      absolute count (>= 5) so single-occurrence noise
//      doesn't dominate.
//
// Performance posture: at billion-span scale the partition
// prune (time + service.name) keeps the scan bounded;
// arrayJoin × 30 keys × N matched rows is the dominant cost
// but bounded by the WHERE clause. 60s server cache + the
// gating in the UI (only fires when the operator clicks
// "Investigate") means we run it once per investigation,
// not on every page render.

type BubbleUpAttribute struct {
	Key string             `json:"key"`
	// Top values by score, descending. Capped at 6 per key.
	Values []BubbleUpValue `json:"values"`
}

type BubbleUpValue struct {
	Value           string  `json:"value"`
	SelectionCount  int64   `json:"selectionCount"`
	BaselineCount   int64   `json:"baselineCount"`
	// Pct = count / total. Frontend renders as bar width.
	SelectionPct    float64 `json:"selectionPct"`
	BaselinePct     float64 `json:"baselinePct"`
	// Score = SelectionPct - BaselinePct, in [-1, 1]. Sorted
	// desc; positive values mean over-represented in selection.
	Score           float64 `json:"score"`
}

type BubbleUpResult struct {
	// Total spans on each side of the comparison.
	SelectionTotal int64 `json:"selectionTotal"`
	BaselineTotal  int64 `json:"baselineTotal"`
	// Attributes sorted by their top-value score so the
	// most "explanatory" attribute appears first.
	Attributes []BubbleUpAttribute `json:"attributes"`
}

// BubbleUp computes the attribute-divergence report for a
// (selection, baseline) pair. `baseline` is the WHERE clause
// for the wider population; `selection` is an additional
// predicate that narrows it. We pass them as parallel
// FilterExpr lists so callers compose the same way they
// compose other span queries.
func (s *Store) BubbleUp(
	ctx context.Context,
	baseline []FilterExpr,
	selection []FilterExpr,
	from, to time.Time,
) (*BubbleUpResult, error) {
	// Build the WHERE for baseline (the wider population).
	wcBase := whereClause{}
	wcBase.add("time >= ?", from)
	wcBase.add("time <= ?", to)
	ApplyFilters(&wcBase, baseline)

	// Selection = baseline + extra predicates (AND-joined).
	wcSel := whereClause{}
	wcSel.args = append(wcSel.args, wcBase.args...)
	wcSel.add("time >= ?", from)
	wcSel.add("time <= ?", to)
	ApplyFilters(&wcSel, baseline)
	ApplyFilters(&wcSel, selection)

	// Step 1 — totals (one query for both sides). Avoids two
	// round-trips and the racy window edge.
	var selTotal, baseTotal uint64
	totalsSQL := fmt.Sprintf(`
		SELECT
		  countIf(%s) AS sel_total,
		  count() AS base_total
		FROM spans
		%s
		SETTINGS max_execution_time = 25`,
		selectionPredicate(selection), wcBase.sql())
	if err := s.conn.QueryRow(ctx, totalsSQL,
		append(selectionPredicateArgs(selection), wcBase.args...)...,
	).Scan(&selTotal, &baseTotal); err != nil {
		return nil, fmt.Errorf("bubbleup totals: %w", err)
	}
	if selTotal == 0 || baseTotal == 0 {
		return &BubbleUpResult{
			SelectionTotal: int64(selTotal),
			BaselineTotal:  int64(baseTotal),
		}, nil
	}

	// Step 2 — top attribute keys observed on the selection
	// side. Caps at 30 keys so the GROUP BY at step 3 stays
	// bounded. Ordering by occurrence count surfaces the
	// most-populated keys (the others would only contribute
	// 1-2 rows each anyway, not enough signal for a divergence
	// score).
	keysSQL := fmt.Sprintf(`
		SELECT k, count() AS c
		FROM (
		  SELECT arrayJoin(attr_keys) AS k
		  FROM spans
		  %s
		)
		GROUP BY k
		ORDER BY c DESC
		LIMIT 30
		SETTINGS max_execution_time = 25`, wcSel.sql())
	keysRows, err := s.conn.Query(ctx, keysSQL, wcSel.args...)
	if err != nil {
		return nil, fmt.Errorf("bubbleup keys: %w", err)
	}
	var keys []string
	for keysRows.Next() {
		var k string
		var c uint64
		if err := keysRows.Scan(&k, &c); err != nil {
			keysRows.Close()
			return nil, err
		}
		// Skip very-high-cardinality keys (trace_id, request_id,
		// etc.) — every value would be unique, no clustering
		// signal. Heuristic: skip if the key contains
		// "id" / "uuid" suffixes that almost always carry
		// per-row uniqueness.
		if isHighCardinalityKey(k) {
			continue
		}
		keys = append(keys, k)
	}
	keysRows.Close()
	if len(keys) == 0 {
		return &BubbleUpResult{
			SelectionTotal: int64(selTotal),
			BaselineTotal:  int64(baseTotal),
		}, nil
	}

	// Step 3 — for each key, GROUP BY value and count both
	// sides. We do one query per key to keep ClickHouse
	// memory-bounded; 30 keys × ~50ms per query = sub-2s
	// total, well within the 30s execution cap. The
	// alternative (one giant GROUP BY (k, v)) blows up at
	// high cardinality.
	out := &BubbleUpResult{
		SelectionTotal: int64(selTotal),
		BaselineTotal:  int64(baseTotal),
	}
	for _, key := range keys {
		valSQL := fmt.Sprintf(`
			SELECT
			  attr_values[indexOf(attr_keys, ?)] AS v,
			  countIf(%s) AS sel,
			  count() AS base
			FROM spans
			%s
			AND has(attr_keys, ?)
			GROUP BY v
			HAVING sel + base >= 5
			ORDER BY (toFloat64(sel) / %d) - (toFloat64(base) / %d) DESC
			LIMIT 6
			SETTINGS max_execution_time = 15`,
			selectionPredicate(selection),
			wcBase.sql(),
			selTotal, baseTotal,
		)
		// argument order: selection-predicate args, then
		// baseline-WHERE args, then the indexOf key, then
		// the has(attr_keys) key.
		args := []any{key} // for indexOf in SELECT
		args = append(args, selectionPredicateArgs(selection)...)
		args = append(args, wcBase.args...)
		args = append(args, key) // for has(attr_keys, ?)
		valRows, err := s.conn.Query(ctx, valSQL, args...)
		if err != nil {
			return nil, fmt.Errorf("bubbleup values for %s: %w", key, err)
		}
		var attr BubbleUpAttribute
		attr.Key = key
		for valRows.Next() {
			var v string
			var sel, base uint64
			if err := valRows.Scan(&v, &sel, &base); err != nil {
				valRows.Close()
				return nil, err
			}
			if v == "" {
				continue
			}
			selPct := float64(sel) / float64(selTotal)
			basePct := float64(base) / float64(baseTotal)
			attr.Values = append(attr.Values, BubbleUpValue{
				Value:          v,
				SelectionCount: int64(sel),
				BaselineCount:  int64(base),
				SelectionPct:   selPct,
				BaselinePct:    basePct,
				Score:          selPct - basePct,
			})
		}
		valRows.Close()
		// Skip keys that produced no usable values (every value
		// fell under the HAVING threshold or had empty strings).
		if len(attr.Values) > 0 {
			out.Attributes = append(out.Attributes, attr)
		}
	}

	// Sort attributes by their top value's score so the most
	// explanatory key surfaces first. Operator scans top-down
	// and finds the smoking gun on the first row.
	for i := 0; i < len(out.Attributes); i++ {
		topI := scoreOf(out.Attributes[i])
		for j := i + 1; j < len(out.Attributes); j++ {
			topJ := scoreOf(out.Attributes[j])
			if topJ > topI {
				out.Attributes[i], out.Attributes[j] = out.Attributes[j], out.Attributes[i]
				topI = topJ
			}
		}
	}
	return out, nil
}

// scoreOf — peak value-score for an attribute, used to rank
// attributes against each other.
func scoreOf(a BubbleUpAttribute) float64 {
	if len(a.Values) == 0 {
		return 0
	}
	return a.Values[0].Score
}

// selectionPredicate — turns an extra-filter list into a SQL
// fragment we can drop inside countIf(...). Returns "1" when
// no extra filters (selection = baseline → no narrowing,
// every span counts).
func selectionPredicate(filters []FilterExpr) string {
	if len(filters) == 0 {
		return "1"
	}
	wc := whereClause{}
	ApplyFilters(&wc, filters)
	// wc.sql() produces "WHERE …"; strip the keyword for use
	// inside countIf.
	s := strings.TrimSpace(wc.sql())
	s = strings.TrimPrefix(s, "WHERE")
	return strings.TrimSpace(s)
}

// selectionPredicateArgs — corresponding args to drop in front
// of the wider WHERE's args. Mirrors selectionPredicate.
func selectionPredicateArgs(filters []FilterExpr) []any {
	if len(filters) == 0 {
		return nil
	}
	wc := whereClause{}
	ApplyFilters(&wc, filters)
	return wc.args
}

// isHighCardinalityKey — quick heuristic for keys whose
// values are almost always unique (UUID-shaped, request IDs,
// trace IDs, span IDs). BubbleUp on these surfaces no
// signal — every value occurs once in either set.
func isHighCardinalityKey(k string) bool {
	lk := strings.ToLower(k)
	if strings.HasSuffix(lk, "_id") || strings.HasSuffix(lk, ".id") || lk == "id" {
		return true
	}
	if strings.HasSuffix(lk, ".uuid") || strings.Contains(lk, "trace_id") || strings.Contains(lk, "span_id") {
		return true
	}
	if strings.Contains(lk, "request_id") || strings.Contains(lk, "correlation") {
		return true
	}
	return false
}
