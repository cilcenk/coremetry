package chstore

// Messaging end-to-end produce→consume latency — v0.8.372 (Stage-2 M2,
// docs/pages-enhancement-audit.md §3 "Uçtan uca produce→consume gecikmesi").
//
// Correlation source is the span_links table (v0.8.329): a consumer span
// links back to the producer span whose message it processed (the demo
// emits exactly this shape since v0.8.335; Kafka SDK instrumentations do
// the same per OTel messaging semconv). One link row carries the CONSUMER
// side's identity + start time (`time` is set to the owning span's start
// at ingest, see otlp.appendSpanLinks) and the PRODUCER's trace/span ids —
// but NO producer-side timestamps. So the e2e delta
//
//	lag = consumer_start − producer_end
//
// needs the producer's (time + duration) from `spans`, and the link table
// itself carries no destination either, so BOTH sides of the pair resolve
// through bounded spans subqueries:
//
//   - consumer side: destination + window WHERE, kind='consumer' (OTel
//     semconv: receive/process spans are CONSUMER kind) — this is the
//     scoping side, same predicates as every other messaging read here;
//   - producer side: destination + window widened backwards by
//     msgE2EProducerSlack (a consumer early in the window links to a
//     producer just before it), kind='producer'. The join keys pin the
//     EXACT linked span, the predicates only bound the scan.
//
// Cluster mode: spans shards by cityHash64(service_name) and span_links by
// cityHash64(trace_id) (cluster.go defaultShardPolicy) — neither join is
// colocated, so both are GLOBAL INNER JOINs (backtrace.go precedent): the
// initiator runs each bounded subquery once and broadcasts the narrow
// (id, id[, time, duration]) rows; on single-node CH GLOBAL is a no-op.
//
// Output is overall p50/p95/p99 + pair count + a 5-min avg-lag series +
// the slowest pair's trace ids (the drawer's exemplar pivot) — all from
// ONE scan via GROUP BY … WITH ROLLUP: bucket rows feed the series, the
// rollup total row (bucket collapses to epoch → t=0) feeds the overall
// stats. Zero link pairs in the window → zero rows → Linkless=true so the
// UI can say "no span links from the SDKs" instead of a misleading "0ms".

import (
	"context"
	"fmt"
	"time"
)

// MsgE2E is the end-to-end produce→consume latency block of the messaging
// destination detail (v0.8.372). Lag is clamped at 0 server-side — cross-
// host clock skew can put a consumer start before the producer end, and a
// negative "latency" reads as a bug, not as skew.
type MsgE2E struct {
	Count    uint64        `json:"count"`              // correlated produce→consume pairs in window
	P50Ms    float64       `json:"p50Ms"`
	P95Ms    float64       `json:"p95Ms"`
	P99Ms    float64       `json:"p99Ms"`
	Linkless bool          `json:"linkless,omitempty"` // no span links correlated — SDKs not emitting links
	Series   []MsgE2EPoint `json:"series"`
	// Slowest pair — the drawer's one exemplar pivot (→ /trace?id=consumer).
	SlowestLagMs           float64 `json:"slowestLagMs,omitempty"`
	SlowestConsumerTraceID string  `json:"slowestConsumerTraceId,omitempty"`
	SlowestProducerTraceID string  `json:"slowestProducerTraceId,omitempty"`
}

// MsgE2EPoint is one 5-minute bucket of the e2e series: pair count + the
// bucket's average lag (avg, not a quantile — a 26px sparkline can't show
// a distribution, and per-bucket plain quantiles wouldn't merge into the
// overall p50/p95/p99 anyway; those come from the rollup total row).
type MsgE2EPoint struct {
	TimeS int64   `json:"timeS"`
	Count uint64  `json:"count"`
	AvgMs float64 `json:"avgMs"`
}

const (
	// msgE2ESpanSideLimit bounds EACH spans-side subquery (consumer scope
	// set / producer lookup set). 50k spans per side per destination per
	// window is far beyond any drawer-relevant volume; past it the
	// distribution is a sample, which is honest enough for a p99 chip.
	msgE2ESpanSideLimit = 50000
	// msgE2EPairLimit bounds the joined pair set fed into the aggregate.
	msgE2EPairLimit = 200000
	// msgE2ESeriesLimit bounds the bucket rows (5000 ≈ >17 days of 5-min
	// buckets — same rung as the M1 produce/consume series). +1 for the
	// rollup total row.
	msgE2ESeriesLimit = 5000 + 1
	// msgE2EProducerSlack widens the producer-side window backwards: a
	// consumer at the start of the window links to producers just before
	// it. One hour covers any healthy consumer lag; pairs older than that
	// drop out (bounded read wins over the pathological tail).
	msgE2EProducerSlack = time.Hour
)

// msgDestExpr mirrors GetMessagingDetail's destination resolution
// (messaging.destination.name → messaging.destination → peer.service) so
// the e2e read scopes by exactly the same destination identity the drawer
// row was built from.
const msgDestExpr = `coalesce(
	nullIf(attr_values[indexOf(attr_keys, 'messaging.destination.name')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'messaging.destination')], ''),
	nullIf(peer_service, ''),
	'unknown'
)`

// msgE2ESQL — the single-scan e2e query (rationale in the file header).
// Placeholder order is pinned by msgE2EArgs + TestMsgE2EArgsAlignment.
var msgE2ESQL = fmt.Sprintf(`
	SELECT toUnixTimestamp(b)                    AS t,
	       count()                               AS n,
	       avg(lag_ms)                           AS avg_ms,
	       quantiles(0.5, 0.95, 0.99)(lag_ms)    AS qs,
	       max(lag_ms)                           AS max_ms,
	       argMax(consumer_trace, lag_ms)        AS slow_consumer,
	       argMax(producer_trace, lag_ms)        AS slow_producer
	FROM (
		SELECT toStartOfInterval(l.time, INTERVAL 5 MINUTE) AS b,
		       l.trace_id        AS consumer_trace,
		       l.linked_trace_id AS producer_trace,
		       greatest(0., (toUnixTimestamp64Nano(l.time)
		         - (toUnixTimestamp64Nano(p.time) + p.duration)) / 1e6) AS lag_ms
		FROM span_links AS l
		GLOBAL INNER JOIN (
			SELECT trace_id, span_id
			FROM spans
			WHERE time >= ? AND time <= ?
			  AND msg_system = ? AND %[1]s = ? AND %[2]s = ?
			  AND kind = 'consumer'
			LIMIT %[3]d
		) AS c ON l.trace_id = c.trace_id AND l.span_id = c.span_id
		GLOBAL INNER JOIN (
			SELECT trace_id, span_id, time, duration
			FROM spans
			WHERE time >= ? AND time <= ?
			  AND msg_system = ? AND %[1]s = ? AND %[2]s = ?
			  AND kind = 'producer'
			LIMIT %[3]d
		) AS p ON l.linked_trace_id = p.trace_id AND l.linked_span_id = p.span_id
		WHERE l.time >= ? AND l.time <= ?
		LIMIT %[4]d
	)
	GROUP BY b WITH ROLLUP
	ORDER BY t
	LIMIT %[5]d
	SETTINGS max_execution_time = 10`,
	clusterExpr, msgDestExpr, msgE2ESpanSideLimit, msgE2EPairLimit, msgE2ESeriesLimit)

// msgE2EArgs binds msgE2ESQL's placeholders in order: consumer subquery
// (from, to, system, cluster, dest), producer subquery (from−slack, to,
// system, cluster, dest), outer link-window (from, to). Pure — alignment
// pinned by the v0.8.372 regression test.
func msgE2EArgs(system, cluster, destination string, from, to time.Time) []any {
	pFrom := from.Add(-msgE2EProducerSlack)
	return []any{
		from, to, system, cluster, destination, // consumer side
		pFrom, to, system, cluster, destination, // producer side (widened back)
		from, to, // span_links window (link time = consumer start)
	}
}

// msgE2ERow is one scanned result row — bucket rows carry t>0, the WITH
// ROLLUP total row collapses b to epoch (t=0). Kept as a struct so the
// fold below is a pure function (table-driven tested, v0.8.372).
type msgE2ERow struct {
	t     int64
	n     uint64
	avgMs float64
	qs    []float64
	maxMs float64
	slowC string
	slowP string
}

// buildMsgE2E folds the scanned rows into the payload block. Rows arrive
// ORDER BY t so the epoch/total row leads and the series lands ascending
// in one pass. NaN/Inf scrub via safeF (the v0.5.301 JSON-marshal class).
// Zero rows (no correlated pairs at all) → zeros + Linkless=true.
func buildMsgE2E(rows []msgE2ERow) *MsgE2E {
	out := &MsgE2E{Series: []MsgE2EPoint{}}
	for i := range rows {
		r := &rows[i]
		if r.t == 0 { // ROLLUP total row
			out.Count = r.n
			if len(r.qs) == 3 {
				out.P50Ms = safeF(&r.qs[0])
				out.P95Ms = safeF(&r.qs[1])
				out.P99Ms = safeF(&r.qs[2])
			}
			out.SlowestLagMs = safeF(&r.maxMs)
			out.SlowestConsumerTraceID = r.slowC
			out.SlowestProducerTraceID = r.slowP
			continue
		}
		out.Series = append(out.Series, MsgE2EPoint{
			TimeS: r.t, Count: r.n, AvgMs: safeF(&r.avgMs),
		})
	}
	out.Linkless = out.Count == 0
	return out
}

// getMessagingE2E runs the e2e read for one destination + window. Called
// best-effort from GetMessagingDetail — an error leaves MessagingDetail.E2E
// nil and the drawer simply omits the section (never blocks on it).
func (s *Store) getMessagingE2E(
	ctx context.Context, system, cluster, destination string, from, to time.Time,
) (*MsgE2E, error) {
	rows, err := s.conn.Query(ctx, msgE2ESQL,
		msgE2EArgs(system, cluster, destination, from, to)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var scanned []msgE2ERow
	for rows.Next() {
		var r msgE2ERow
		if err := rows.Scan(&r.t, &r.n, &r.avgMs, &r.qs, &r.maxMs, &r.slowC, &r.slowP); err != nil {
			return nil, err
		}
		scanned = append(scanned, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return buildMsgE2E(scanned), nil
}
