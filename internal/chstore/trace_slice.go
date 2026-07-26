package chstore

import (
	"context"
	"fmt"
	"time"
)

// Recency slice for the /traces list — v0.9.277.
//
// THE DEFECT IT REPLACES. Stage 1 used to be:
//
//	SELECT trace_id FROM trace_summary_5m
//	WHERE time_bucket >= ? AND time_bucket <= ?
//	GROUP BY trace_id ORDER BY max(time_bucket) DESC LIMIT 5000
//
// trace_summary_5m is ORDER BY (time_bucket, trace_id), so trace_id is the
// SECOND key column — GROUP BY trace_id is not a sorting-key PREFIX and
// optimize_aggregation_in_order cannot engage. And because a GROUP BY is
// present at all, optimize_read_in_order is inert too: it serves an ORDER BY
// straight from merge-tree read order, never through an aggregation. Both
// SETTINGS on that query were decorative.
//
// So ClickHouse built a hash table over EVERY distinct trace_id in the window
// before ORDER BY/LIMIT could pick anything. LIMIT bounded the output, never
// the work: cost was linear in window width. Measured on the local cluster,
// which is ~500x smaller than the production install this was reported from:
// a 2-day window read 1,145,198 rows — 100% of the window — in 875 ms / 41 MiB.
// At 1000s of services and billions of spans that is 10^8-10^9 String keys; it
// spills, then dies, and the operator gets an HTTP 500.
//
// THE SHAPE THAT WORKS. Drop the GROUP BY. ORDER BY time_bucket IS the sorting
// key prefix, so with no aggregation in the way ClickHouse reads the newest
// granules and stops at LIMIT — EXPLAIN PIPELINE confirms
// MergeTreeSelect(pool: ReadPoolInOrder, algorithm: InReverseOrder) with no
// AggregatingTransform. Same window, same cluster: 19 ms / 350,574 rows / 6 MiB.
//
// Cost stops scaling with the window and starts scaling with (parts × page),
// which is the property that matters at production density: a single 5-minute
// bucket there holds on the order of a million traces, so any design whose cost
// tracks "traces in window" is lost before it starts.
//
// WHAT THE CALLER MUST KNOW. Without GROUP BY the query returns ROWS, not
// traces: one trace occupies a row for every (5-min bucket × shard × unmerged
// part) it appears in. Locally that inflation measured 1.75x on a 2-shard
// Distributed setup, and it grows with shard count and part fragmentation. So
// we over-provision, deduplicate in Go, and — if the slice still came up short
// — retry ONCE using the ratio we just measured rather than another guess. The
// cost of a bad estimate is one cheap query, never a wrong page.

// traceSliceOverprovision multiplies the wanted trace count to get a row
// budget. 3x is the opening guess; the retry below uses the real ratio.
const traceSliceOverprovision = 3

// traceSliceMaxRows caps the row budget so pathological inflation (many
// shards, heavy part fragmentation) can't turn the slice back into the
// whole-window scan it exists to avoid.
const traceSliceMaxRows = 250_000

// traceSliceRetryBudget decides whether a short slice is worth one more read,
// and how big it should be. Pure — table-tested.
//
// `scanned` rows produced `kept` distinct traces against a target of `want`.
// The observed inflation is scanned/kept; asking for want*inflation rows should
// land the target, with a little headroom for the tail being denser than the
// head. Returns ok=false when a retry cannot help: the target is already met,
// nothing came back (the window is genuinely empty), or the budget is already
// at the ceiling.
func traceSliceRetryBudget(scanned, kept, want int) (int, bool) {
	if kept >= want || kept == 0 || scanned >= traceSliceMaxRows {
		return 0, false
	}
	inflation := float64(scanned) / float64(kept)
	next := int(float64(want) * inflation * 1.25) // 25% headroom
	if next <= scanned {
		// The measurement says we already read enough rows to have found
		// them; reading the same amount again would return the same slice.
		return 0, false
	}
	if next > traceSliceMaxRows {
		next = traceSliceMaxRows
	}
	return next, true
}

// traceSliceScanSQL builds the aggregation-free slice read.
//
// optimize_aggregation_in_order is deliberately NOT set: there is no
// aggregation, and writing it would be a comment that lies. max_execution_time
// is 10 — comfortably under the 30s client ReadTimeout (store.go:376), which
// clickhouse-go arms once per read phase and never refreshes, so any server cap
// at or above 30 is unreachable by construction.
func (s *Store) traceSliceScanSQL(order string, errorsOnly bool) string {
	dir := "DESC"
	if order == "asc" {
		dir = "ASC"
	}
	errFilter := ""
	if errorsOnly {
		// Row-level, no GROUP BY needed: countIf partials cannot be negative,
		// so "any partial > 0" is exactly "this trace has >= 1 error span".
		// Stage 2 keeps its HAVING as the exactness backstop; this is a
		// superset prefilter by construction.
		errFilter = "\n\t\t  AND finalizeAggregation(error_count_state) > 0"
	}
	return `
		SELECT trace_id, time_bucket
		FROM trace_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?` + errFilter + `
		ORDER BY time_bucket ` + dir + `
		LIMIT ?
		SETTINGS max_execution_time = 10,
		         optimize_read_in_order = 1,
		         ` + s.shardSkipSetting()
}

// traceRecencySlice returns the newest `want` distinct trace ids in the window
// plus the bucket the slice was cut at, so Stage 2 can narrow its own floor to
// the slice's real extent instead of rescanning the whole window.
//
// exhausted=true means the server ran out of rows before the budget did: the
// slice IS the window, so the ordering is global and the "ranked within newest
// N" hint must be suppressed rather than shown with a number that isn't true.
func (s *Store) traceRecencySlice(
	ctx context.Context, f TraceFilter, want int, errorsOnly bool,
) (ids []any, cut time.Time, exhausted bool, err error) {
	if want <= 0 {
		return nil, time.Time{}, false, nil
	}
	budget := want * traceSliceOverprovision
	if budget > traceSliceMaxRows {
		budget = traceSliceMaxRows
	}
	sql := s.traceSliceScanSQL(f.Order, errorsOnly)

	for attempt := 0; attempt < 2; attempt++ {
		rows, qerr := s.conn.Query(ctx, sql, f.From, f.To, budget)
		if qerr != nil {
			return nil, time.Time{}, false, fmt.Errorf("trace slice: %w", qerr)
		}
		seen := make(map[string]struct{}, want)
		ids = ids[:0]
		scanned := 0
		var last time.Time
		for rows.Next() {
			var id string
			var tb time.Time
			if serr := rows.Scan(&id, &tb); serr != nil {
				rows.Close()
				return nil, time.Time{}, false, serr
			}
			scanned++
			last = tb
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
			if len(ids) >= want {
				break
			}
		}
		rows.Close()
		if rerr := rows.Err(); rerr != nil {
			return nil, time.Time{}, false, rerr
		}

		// Fewer rows than asked for means the server had no more to give:
		// the slice covers the whole window and the ranking is global.
		exhausted = scanned < budget
		cut = last
		if exhausted || len(ids) >= want {
			if exhausted {
				cut = f.From
			}
			return ids, cut, exhausted, nil
		}
		next, ok := traceSliceRetryBudget(scanned, len(ids), want)
		if !ok {
			return ids, cut, exhausted, nil
		}
		budget = next
	}
	return ids, cut, exhausted, nil
}
