package chstore

import (
	"context"
	"fmt"
	"time"
)

// DistinctTraceIDsForFilters returns up to `limit` distinct
// trace IDs from the spans table that match the supplied
// filters within [from, to]. Powers the DQL cross-signal join
// (v0.5.271) where the source-side trace_id set narrows the
// target-side aggregation.
//
// Cap is a hard ceiling — operators writing
// `spans | filter X | join logs ...` get a join scoped to at
// most `limit` traces. At billion-row scale this prevents the
// generated `trace_id IN (...)` clause from ballooning past
// what the target backend (ES / CH) can plan in one query.
func (s *Store) DistinctTraceIDsForFilters(ctx context.Context, filters []FilterExpr, from, to time.Time, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 1000
	}
	if limit > 10000 {
		limit = 10000
	}

	var wc whereClause
	if !from.IsZero() {
		wc.add("time >= ?", from)
	}
	if !to.IsZero() {
		wc.add("time <= ?", to)
	}
	ApplyFilters(&wc, filters)

	sql := fmt.Sprintf(`
		SELECT DISTINCT trace_id
		FROM spans %s
		LIMIT ?
		SETTINGS max_execution_time = 30`,
		wc.sql())
	args := append([]any{}, wc.args...)
	args = append(args, limit)

	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("distinct trace_ids: %w", err)
	}
	defer rows.Close()
	out := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}
