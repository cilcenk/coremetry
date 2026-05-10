package chstore

import (
	"context"
	"fmt"
	"time"
)

// Deploy is one observed (service, service.version) entry. The
// frontend renders one vertical dashed line per Deploy on the
// metric / latency / error charts so an operator can read at a
// glance whether a regression coincides with a deploy.
//
// "Deploy" here is the moment a previously-unseen version of
// the service first emitted a span — that's what an operator
// reads as "the new code shipped". OTel populates
// resource.service.version from the SDK; if your build process
// doesn't set it (no SDK env var, no .ServiceVersion()), there
// will be nothing to show, which is the right answer.
type Deploy struct {
	Service       string `json:"service"`
	Version       string `json:"version"`
	// TimeUnixNs is the first-seen timestamp of this version
	// in the queried window — the marker position on the chart.
	TimeUnixNs    int64  `json:"timeUnixNs"`
	// SpanCount = how many spans this version has produced
	// since first appearance. Helps the UI dim out noise: a
	// version that produced 3 spans is probably a stuck
	// straggler instance, not a real deploy.
	SpanCount     int    `json:"spanCount"`
}

// GetServiceDeploys returns every distinct service.version
// observed for `service` in the time window, ordered by first
// appearance. Each row carries the first-seen timestamp — the
// position the deploy marker lands on the chart.
//
// Why min(time): in a continuous-deployment shop, an old
// version may have stragglers running for a few minutes after
// the new one ships. Using min(time) per version finds the
// *earliest* moment that version became active — the actual
// deploy timestamp — rather than the moment some pod last saw
// it.
//
// CH posture: the (service_name, time) primary key prunes by
// the time bound; the resource-attribute lookup is a single
// indexOf per row, cheap. Limit 50 is a hard cap so a chatty
// CD pipeline doesn't return thousands of rows.
func (s *Store) GetServiceDeploys(
	ctx context.Context, service string, from, to time.Time,
) ([]Deploy, error) {
	const sql = `
		SELECT
			res_values[indexOf(res_keys, 'service.version')] AS version,
			toUnixTimestamp64Nano(min(time))                 AS first_seen_ns,
			count()                                          AS span_count
		FROM spans
		WHERE service_name = ?
		  AND time >= ? AND time <= ?
		  AND has(res_keys, 'service.version')
		GROUP BY version
		HAVING version != ''
		ORDER BY first_seen_ns ASC
		LIMIT 50
		SETTINGS max_execution_time = 15`
	rows, err := s.conn.Query(ctx, sql, service, from, to)
	if err != nil {
		return nil, fmt.Errorf("query deploys: %w", err)
	}
	defer rows.Close()

	out := []Deploy{}
	for rows.Next() {
		var d Deploy
		var spanCnt uint64
		if err := rows.Scan(&d.Version, &d.TimeUnixNs, &spanCnt); err != nil {
			return nil, err
		}
		d.Service = service
		d.SpanCount = int(spanCnt)
		out = append(out, d)
	}
	return out, rows.Err()
}
