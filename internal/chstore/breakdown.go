package chstore

import (
	"context"
	"time"
)

// SpanBreakdownPoint is one time bucket of the "where does this
// service spend its time?" stacked-area chart. Elastic APM's
// signature service-overview surface — gives the operator a quick
// "is the slowness coming from DB, HTTP, internal compute, or
// queue waits" answer without flipping between panels.
//
// Kinds is a map of bucket → cumulative ms of duration grouped by
// span.kind (server / client / internal / producer / consumer)
// plus a synthetic "db" / "queue" / "http" bucket derived from
// db.system / messaging.system / http.method when set. The
// frontend renders each as a stacked band.
type SpanBreakdownPoint struct {
	TimeNs int64              `json:"time"`
	Kinds  map[string]float64 `json:"kinds"`
}

// GetSpanBreakdown returns time-bucketed cumulative duration per
// span "category" for a single service. Category is derived from
// db.system / messaging.system / http_method / span.kind in
// priority order so DB time doesn't double-count as "client"
// time. Bucket size auto-picks from the window so the resulting
// series fits a chart cleanly (~60-200 buckets).
//
// Cached for 30s on the calling handler — the surrounding service
// detail page is rendered every time an operator switches range,
// so the cache amortises the GROUP BY across N visitors during
// active triage.
func (s *Store) GetSpanBreakdown(
	ctx context.Context, service string, from, to time.Time,
) ([]SpanBreakdownPoint, error) {
	if service == "" {
		return nil, nil
	}
	if from.IsZero() {
		from = to.Add(-1 * time.Hour)
	}
	winSec := int64(to.Sub(from).Seconds())
	step := bucketForWindow(winSec)

	// Category expression: pick the most informative tag.
	// Priority: db.system > messaging.system > http.method > span.kind.
	// Putting the DB / queue / HTTP test first keeps "client"
	// from monopolising the chart — a CLIENT span hitting Redis
	// reads as "db" not "client".
	q := `
		SELECT toStartOfInterval(time, INTERVAL ? SECOND) AS bucket,
		       multiIf(
		         db_system != '',           concat('db:', db_system),
		         msg_system != '',          concat('queue:', msg_system),
		         http_method != '',         'http',
		         kind != '' AND kind != 'internal', kind,
		         'internal'
		       ) AS category,
		       sum(duration) / 1e6 AS dur_ms
		FROM spans
		WHERE service_name = ? AND time >= ? AND time <= ?
		GROUP BY bucket, category
		ORDER BY bucket
		LIMIT 10000
		SETTINGS max_execution_time = 15,
		         ` + s.shardSkipSetting()
	rows, err := s.conn.Query(ctx, q, step, service, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []SpanBreakdownPoint{}
	var cur *SpanBreakdownPoint
	for rows.Next() {
		var bucket time.Time
		var category string
		var durMs float64
		if err := rows.Scan(&bucket, &category, &durMs); err != nil {
			return nil, err
		}
		ts := bucket.UnixNano()
		if cur == nil || cur.TimeNs != ts {
			out = append(out, SpanBreakdownPoint{
				TimeNs: ts,
				Kinds:  map[string]float64{},
			})
			cur = &out[len(out)-1]
		}
		cur.Kinds[category] += durMs
	}
	return out, rows.Err()
}

// bucketForWindow picks a step (seconds) that yields ~60-150
// buckets across the requested window. Keeps the rendered chart
// readable at any range. Mirrors the heuristic in spanmetric.go.
func bucketForWindow(winSec int64) int64 {
	switch {
	case winSec <= 600:
		return 10
	case winSec <= 3600:
		return 30
	case winSec <= 6*3600:
		return 60
	case winSec <= 24*3600:
		return 300
	case winSec <= 7*24*3600:
		return 1800
	default:
		return 3600
	}
}
