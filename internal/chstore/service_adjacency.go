package chstore

import (
	"context"
	"time"
)

// ServiceEdgePair is one directed service-to-service edge. The two
// endpoints (Caller → Callee) are always populated; the weight fields
// are filled only by GetServiceAdjacencyWeighted.
//
// v0.8.67 (correlator Faz 5) — added the weight fields so the
// correlator can build a DIRECTED, WEIGHTED adjacency graph (rank a
// service's downstream deps by error-carrying volume) instead of the
// symmetric unweighted set it used through Faz 4. The plain
// GetServiceAdjacency still returns endpoint-only pairs (weights
// zero) — its only caller, the fusion evidence bundle, needs just the
// "who calls who" topology, so its lean query is left untouched.
type ServiceEdgePair struct {
	Caller string
	Callee string

	// Weighted-edge fields — populated by GetServiceAdjacencyWeighted,
	// left zero by GetServiceAdjacency.
	Calls         uint64 // total calls on this edge in the window
	Errors        uint64 // error-status calls on this edge in the window
	SumDurationNs uint64 // summed span duration — window avg = SumDurationNs/Calls
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

// GetServiceAdjacencyWeighted is GetServiceAdjacency plus per-edge
// weights — total calls, error calls and summed duration over the
// window — summed across the 5-min buckets (and protocols) of each
// (parent_service, child_node) pair. The correlator uses these to
// build a directed weighted graph (v0.8.67, Faz 5): Caller's
// downstream deps ranked by error-carrying volume, Callee's upstream
// callers likewise.
//
// Same MV, same bounds and partition pruning as GetServiceAdjacency
// (MV-bypass invariant satisfied — this never touches raw spans). The
// only addition is the three sum() aggregates, which FINAL collapses
// per ORDER-BY key before summing across buckets, so duplicate
// ReplacingMergeTree versions of a bucket are not double-counted —
// the exact pattern GetServiceGraph (repo.go) and ReadServiceTopologyAgg
// (topology.go) already use.
//
// ORDER BY errors DESC, calls DESC before the LIMIT so that, at a
// 1000s-services mesh where the distinct directed-edge count can exceed
// the cap, truncation is DETERMINISTIC and keeps the highest-error /
// highest-volume edges — the ones the correlator's Downstream/Upstream
// ranking (errors-first) actually consumes. Without it, LIMIT returns an
// arbitrary subset and could silently drop the single edge carrying the
// incident's error traffic. Cap is 20000 (matching ReadServiceTopologyAgg)
// — ~20k EdgeStat structs is a few MB, trivially bounded memory.
func (s *Store) GetServiceAdjacencyWeighted(
	ctx context.Context, since time.Duration,
) ([]ServiceEdgePair, error) {
	if since <= 0 {
		since = time.Hour
	}
	bucketStart := time.Now().Add(-since).Truncate(5 * time.Minute)
	rows, err := s.conn.Query(ctx, `
		SELECT parent_service,
		       child_node,
		       sum(calls)           AS calls,
		       sum(errors)          AS errors,
		       sum(sum_duration_ns) AS sum_dur
		FROM topology_edges_5m FINAL
		WHERE time_bucket >= ? AND node_kind = 'service'
		GROUP BY parent_service, child_node
		ORDER BY errors DESC, calls DESC
		LIMIT 20000
		SETTINGS max_execution_time = 10`, bucketStart)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceEdgePair
	for rows.Next() {
		var e ServiceEdgePair
		if err := rows.Scan(&e.Caller, &e.Callee, &e.Calls, &e.Errors, &e.SumDurationNs); err != nil {
			return nil, err
		}
		if e.Caller == "" || e.Callee == "" {
			continue
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
