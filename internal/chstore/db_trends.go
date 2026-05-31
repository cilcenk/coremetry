package chstore

import (
	"context"
	"time"
)

// DBTrendPoint is one 5-minute bucket of a database's RED trend.
// Aligned to the db_summary_5m time_bucket grid so the frontend
// can stitch sparklines without re-bucketing. t is unix ns at the
// bucket start; the three RED series ride alongside it.
//
// Rps is spans/sec within the bucket (span_count / 300s, since
// the MV buckets on a 5-minute = 300s interval). ErrorRate is
// 0..100. P99Ms is the merged 0.99 quantile in milliseconds.
type DBTrendPoint struct {
	T         int64   `json:"t"`         // unix ns — bucket start
	Rps       float64 `json:"rps"`       // call rate: span_count / 300
	ErrorRate float64 `json:"errorRate"` // 0..100
	P99Ms     float64 `json:"p99Ms"`     // p99 duration, ms
}

// DBTrend is the per-row sparkline + latest-bucket health snapshot
// for one database on the /databases (or /messaging) overview grid.
// Keyed identically to chstore.DBInstance / the frontend DepRow:
// (DbSystem, Instance, DbName, Cluster). Cluster is empty for
// db_summary_5m-sourced rows (DB rows carry no cluster dimension);
// it rides the struct so the same shape can serve the messaging
// grid join key without a second type.
//
// Points is an ascending-time array of ~bucketsPerWindow entries
// (one per 5-minute bucket the window covers, capped). The Cur*
// fields are the latest non-empty bucket's snapshot — what the
// per-row health gauge renders without the frontend having to
// scan the array.
type DBTrend struct {
	DbSystem string `json:"dbSystem"`
	Instance string `json:"instance"`
	DbName   string `json:"dbName"`
	Cluster  string `json:"cluster"`

	Points []DBTrendPoint `json:"points"`

	// Latest-bucket health snapshot (gauge source).
	CurRps       float64 `json:"curRps"`
	CurErrorRate float64 `json:"curErrorRate"` // 0..100
	CurP99Ms     float64 `json:"curP99Ms"`
}

// dbTrendBucketSeconds is the MV bucket width in seconds. The
// db_summary_5m MV groups on toStartOfInterval(time, INTERVAL 5
// MINUTE), so each bucket spans 300s — the divisor that turns a
// bucket's span_count into a rate.
const dbTrendBucketSeconds = 300.0

// GetDBTrends reads db_summary_5m bucketed by (db_system,
// instance, db_name, time_bucket) over [from,to] and returns one
// DBTrend per (db_system, instance, db_name) — a small call-rate /
// p99 / error-rate sparkline plus the latest-bucket health
// snapshot. Drives the per-row RED sparklines (#1) + health
// gauges (#6) on the /databases overview grid.
//
// MV-only (NOT raw spans) per the aggregate-read invariant — same
// AggregatingMergeTree the overview's GetDatabases reads, so the
// row identities line up exactly. The query is bounded three ways
// even though the MV is already small: time-bounded WHERE on the
// ORDER-BY-leading time_bucket, a LIMIT on the result, and a
// max_execution_time guard.
//
// The from is truncated to the 5-minute grid so a rolling window
// snaps to bucket boundaries (the same trick GetDatabases uses) —
// keeps the cache key + bucket alignment stable across adjacent
// polls.
func (s *Store) GetDBTrends(ctx context.Context, from, to time.Time) ([]DBTrend, error) {
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	bucketStart := from.Truncate(5 * time.Minute)

	// One row per (db_system, instance, db_name, time_bucket).
	// ORDER BY trailing time_bucket so points arrive ascending
	// and we can append directly into each series. The LIMIT is a
	// hard safety net: at 5000 DBs × a 24h window (288 buckets)
	// the worst case is bounded; in practice the MV holds far
	// fewer rows because most DBs aren't seen every bucket.
	//
	// countIfMerge for the error state because the MV defines it
	// as countIfState(status_code = 'error'); countMerge would
	// silently read the wrong aggregate.
	rows, err := s.conn.Query(ctx, `
		SELECT db_system,
		       instance,
		       db_name,
		       toUnixTimestamp64Nano(toDateTime64(time_bucket, 9))                     AS bucket_ns,
		       countMerge(span_count_state)                                            AS span_count,
		       countIfMerge(error_count_state)                                         AS error_count,
		       arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM db_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		GROUP BY db_system, instance, db_name, time_bucket
		ORDER BY db_system, instance, db_name, time_bucket
		LIMIT 200000
		SETTINGS max_execution_time = 15`, bucketStart, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []DBTrend{}
	type key struct{ system, instance, dbName string }
	idxByKey := map[key]int{}

	for rows.Next() {
		var (
			system, instance, dbName string
			bucketNs                 int64
			spanCount, errorCount    uint64
			p99Ms                    *float64
		)
		if err := rows.Scan(&system, &instance, &dbName, &bucketNs, &spanCount, &errorCount, &p99Ms); err != nil {
			return nil, err
		}
		k := key{system, instance, dbName}
		i, ok := idxByKey[k]
		if !ok {
			out = append(out, DBTrend{
				DbSystem: system,
				Instance: instance,
				DbName:   dbName,
				Points:   []DBTrendPoint{},
			})
			i = len(out) - 1
			idxByKey[k] = i
		}

		pt := DBTrendPoint{
			T:     bucketNs,
			Rps:   float64(spanCount) / dbTrendBucketSeconds,
			P99Ms: safeF(p99Ms),
		}
		if spanCount > 0 {
			pt.ErrorRate = float64(errorCount) / float64(spanCount) * 100
		}
		out[i].Points = append(out[i].Points, pt)

		// ORDER BY time_bucket ascending means the last point we
		// see for a key is its latest bucket — keep overwriting so
		// Cur* lands on the freshest snapshot without a second pass.
		out[i].CurRps = pt.Rps
		out[i].CurErrorRate = pt.ErrorRate
		out[i].CurP99Ms = pt.P99Ms
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
