package chstore

// v0.6.34 — aged-out trace stub. Operator-reported: /traces
// aggregate view shows traces (read from trace_summary_5m, 90-day
// retention) that no longer have raw span data (raw `spans` table
// has 30-day TTL). Clicking such a trace returned an empty spans
// array with no explanation.
//
// GetTraceAggregateStub looks up a single trace_id in
// trace_summary_5m and returns the aggregated stats the MV still
// holds. The handler at /api/traces/{id} consults this on a CH+
// Tempo double-miss and returns source="mv_only" so the frontend
// can render a "trace aged out, only aggregates remain" pane
// instead of a blank page.

import "context"

// TraceAggregateStub is the minimal "we have aggregates only"
// payload the UI uses to explain the missing-spans case. All
// fields come from the trace_summary_5m MV's *Merge() finalisers.
type TraceAggregateStub struct {
	RootService  string  `json:"rootService"`
	RootName     string  `json:"rootName"`
	StartTimeNs  int64   `json:"startTimeNs"`   // earliest span start in MV
	EndTimeNs    int64   `json:"endTimeNs"`     // latest span end in MV
	SpanCount    uint64  `json:"spanCount"`
	ErrorCount   uint64  `json:"errorCount"`
	DurationMs   float64 `json:"durationMs"`
}

// GetTraceAggregateStub returns (stub, true) when the trace_id
// exists in any trace_summary_5m bucket; (zero, false) otherwise.
// One FINAL read keyed by trace_id — sub-ms even at billion-trace
// scale because the MV's ORDER BY (time_bucket, trace_id) keeps
// the lookup index-friendly when combined with the trace_id
// equality.
func (s *Store) GetTraceAggregateStub(ctx context.Context, traceID string) (TraceAggregateStub, bool) {
	var stub TraceAggregateStub
	if traceID == "" {
		return stub, false
	}
	// argMaxIfMerge over the per-bucket states; min/max merge over
	// the time states. countMerge for span + error counts.
	// SETTINGS max_execution_time = 5 — small bounded lookup.
	row := s.conn.QueryRow(ctx, `
		SELECT
		  argMaxIfMerge(root_service_state)          AS root_service,
		  argMaxIfMerge(root_name_state)             AS root_name,
		  toUnixTimestamp64Nano(minMerge(trace_start_state)) AS start_ns,
		  toInt64(maxMerge(trace_end_state))         AS end_ns,
		  countMerge(span_count_state)               AS span_count,
		  countMerge(error_count_state)              AS error_count
		FROM trace_summary_5m FINAL
		WHERE trace_id = ?
		SETTINGS max_execution_time = 5`, traceID)
	var startNs, endNs int64
	if err := row.Scan(&stub.RootService, &stub.RootName,
		&startNs, &endNs, &stub.SpanCount, &stub.ErrorCount); err != nil {
		return stub, false
	}
	// SpanCount == 0 means the trace_id had no aggregate state at
	// all — i.e. genuinely unknown to the MV, not just aged out.
	// Return false so the caller surfaces a clean "not found" vs
	// the aged-out hint.
	if stub.SpanCount == 0 {
		return stub, false
	}
	stub.StartTimeNs = startNs
	stub.EndTimeNs = endNs
	stub.DurationMs = float64(endNs-startNs) / 1e6
	if stub.DurationMs < 0 {
		stub.DurationMs = 0
	}
	return stub, true
}
