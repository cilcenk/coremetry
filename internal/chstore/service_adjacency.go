package chstore

import (
	"context"
	"time"
)

// ServiceEdgePair is one directed service-to-service edge,
// stripped down to just the endpoints. Used by the incident
// correlator which only needs "who calls who" — not call counts,
// latency, or protocol.
type ServiceEdgePair struct {
	Caller string
	Callee string
}

// GetServiceAdjacency returns the distinct service→service edges
// observed in the last `since` window, read from the pre-
// aggregated topology_edges_5m MV.
//
// v0.5.304 — operator-reported boot timeout: the previous
// correlator path called GetServiceMap which runs
//
//	SELECT trace_id FROM spans WHERE time >= ? GROUP BY trace_id
//	ORDER BY count() DESC LIMIT 200
//
// over a 1h window. At billion-span scale that GROUP BY hits the
// 30s max_execution_time ceiling and the boot-time adjacency
// refresh fails (initial map stays empty until the next 5-min
// tick — also fails). This helper bypasses the trace walk
// entirely: the edges are already pre-aggregated per 5-min
// bucket, so we read directly and filter to node_kind = 'service'
// (db / queue / external nodes aren't separately addressable
// services to correlate against).
//
// time_bucket is aligned to the bucket boundary (5-min) per
// v0.5.299's predicate-overlap fix so the most-recent partial
// bucket isn't silently excluded.
func (s *Store) GetServiceAdjacency(
	ctx context.Context, since time.Duration,
) ([]ServiceEdgePair, error) {
	if since <= 0 {
		since = time.Hour
	}
	bucketStart := time.Now().Add(-since).Truncate(5 * time.Minute)
	rows, err := s.conn.Query(ctx, `
		SELECT parent_service, child_node
		FROM topology_edges_5m FINAL
		WHERE time_bucket >= ? AND node_kind = 'service'
		GROUP BY parent_service, child_node
		LIMIT 10000
		SETTINGS max_execution_time = 10`, bucketStart)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceEdgePair
	for rows.Next() {
		var e ServiceEdgePair
		if err := rows.Scan(&e.Caller, &e.Callee); err != nil {
			return nil, err
		}
		if e.Caller == "" || e.Callee == "" {
			continue
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
