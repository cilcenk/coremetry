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

		// v0.9.296 — `scanned < budget` conflated TWO different endings,
		// and the common one was being read as the rare one.
		//
		// The row loop above BREAKS as soon as it has `want` distinct
		// ids, which happens well before the budget is consumed (a real
		// replay: 5,000 ids after 7,954 of 15,000 rows). So on every
		// ordinary page `scanned < budget` was true, "exhausted" was
		// true, and two things followed that should not have:
		//
		//   1. cut = f.From — Stage 2's whole reason for existing is
		//      that the id set prunes NOTHING (EXPLAIN: 215/218 granules
		//      kept), so the time floor is its only bound. Handing it
		//      f.From meant it re-read the entire requested window every
		//      time. Measured on 7 days: 1,611,504 rows / 1.0-3.5 s with
		//      the collapsed floor vs 24,460 rows / 93 ms with the real
		//      one. The v0.9.278 narrowing has never once engaged.
		//   2. RankedWithin was zeroed, which the UI reads as "this
		//      ordering is global". It was not: a non-time sort ranks
		//      inside the newest-N slice, and the honesty hint built to
		//      say so has never been shown.
		//
		// Getting enough ids is SUCCESS, not exhaustion. Only running
		// out of server rows without filling `want` is exhaustion.
		gotEnough := len(ids) >= want
		exhausted = !gotEnough && scanned < budget
		cut = last
		if exhausted {
			// Genuinely no more rows: the slice does cover the window,
			// so the ranking really is global and Stage 2 may not
			// narrow below what the caller asked for.
			cut = f.From
		}
		if exhausted || gotEnough {
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

// Stage-2 floor narrowing with clipping detection — v0.9.278.
const (
	// First attempt looks back 12 buckets (1 hour) below the slice cut.
	traceSliceLookbackBuckets = 12
	// Then x8, x64, clamped at f.From. Two retries is enough: the third
	// expansion already exceeds any window this page offers.
	traceSliceLookbackMaxRetry = 2
)

// runTraceStage2 executes Stage 2 with a narrowed time floor and widens that
// floor if the result shows any sign of clipping.
//
// floor is the bucket the recency slice was cut at; the zero value means "do
// not narrow" and Stage 2 runs over f.From..f.To exactly as before.
//
// The correctness hazard being managed: narrowing the floor means a trace whose
// rows extend below it contributes only PART of its own aggregate, so
// trace_start, dur_ms, span_count and has_error all come back wrong — quietly,
// with no error and a perfectly plausible-looking row. Rather than assume a
// lookback is "enough", Stage 2 returns min(time_bucket) per trace and we widen
// whenever a returned row sits on the floor.
func (s *Store) runTraceStage2(
	ctx context.Context, f TraceFilter, stage2 string, idArgs []any,
	floor time.Time, pageLimit int,
) ([]TraceRow, bool, error) {
	from := f.From
	lookback := time.Duration(traceSliceLookbackBuckets) * 5 * time.Minute
	if !floor.IsZero() {
		from = floor.Add(-lookback)
		if from.Before(f.From) {
			from = f.From
		}
	}

	for attempt := 0; ; attempt++ {
		args := append([]any{}, idArgs...)
		args = append(args, from, f.To, pageLimit, f.Offset)
		rows, err := s.conn.Query(ctx, stage2, args...)
		if err != nil {
			return nil, false, fmt.Errorf("stage2: %w", err)
		}
		out := []TraceRow{}
		clipped := false
		for rows.Next() {
			var t TraceRow
			var hasErr uint8
			var ts, firstBucket time.Time
			if serr := rows.Scan(&t.TraceID, &t.RootName, &t.ServiceName, &ts,
				&t.DurationMs, &t.SpanCount, &hasErr, &firstBucket); serr != nil {
				rows.Close()
				return nil, false, serr
			}
			t.StartTime = ts.UnixNano()
			t.HasError = hasErr != 0
			// Sitting ON the floor means rows may exist just below it.
			if !firstBucket.After(from) {
				clipped = true
			}
			out = append(out, t)
		}
		rows.Close()
		// v0.9.278 — this check did not exist. ClickHouse sends the header
		// block immediately, so a Stage 2 exception (159 timeout, 241 memory)
		// arrives DURING streaming: Query() succeeds, Next() simply returns
		// false, and the caller returned a short or empty page with err == nil
		// and HTTP 200. A timeout rendered as "no traces found". That is a
		// correctness bug on its own, and it would have turned every failure of
		// the narrowing above into a silently truncated list.
		if rerr := rows.Err(); rerr != nil {
			return nil, false, fmt.Errorf("stage2: %w", rerr)
		}

		// Widening is pointless once the floor IS the requested window start:
		// nothing can be clipped by a floor the caller asked for.
		if !clipped || from.Equal(f.From) || attempt >= traceSliceLookbackMaxRetry {
			hasMore := len(out) > f.Limit
			if hasMore {
				out = out[:f.Limit]
			}
			return out, hasMore, nil
		}
		lookback *= 8
		from = floor.Add(-lookback)
		if from.Before(f.From) {
			from = f.From
		}
	}
}
