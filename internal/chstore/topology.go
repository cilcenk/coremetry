package chstore

import (
	"context"
	"time"
)

// TopologyEdge is one parent→child operation invocation aggregated
// over a time window. Used by the op-level depth view; the service-
// level view consumes ServiceTopologyEdge below.
type TopologyEdge struct {
	ParentService string `json:"parentService"`
	ParentOp      string `json:"parentOp"`
	ChildService  string `json:"childService"`
	ChildOp       string `json:"childOp"`
	Calls         uint64 `json:"calls"`
}

// GetTopologyEdges aggregates parent→child operation pairs from
// the spans table over [from,to]. Self-join on (trace_id, span_id)
// = (trace_id, parent_id). Capped at `limit` heaviest edges so an
// install with very high operation cardinality (each HTTP route a
// distinct op) still serves an answer.
func (s *Store) GetTopologyEdges(ctx context.Context, from, to time.Time, limit int) ([]TopologyEdge, error) {
	if limit <= 0 || limit > 100000 {
		limit = 50000
	}
	rows, err := s.conn.Query(ctx, `
		SELECT
			p.service_name AS parent_service,
			p.name         AS parent_op,
			c.service_name AS child_service,
			c.name         AS child_op,
			count() AS calls
		FROM spans AS c
		INNER JOIN spans AS p
			ON p.trace_id = c.trace_id AND p.span_id = c.parent_id
		WHERE c.time >= ? AND c.time <= ?
		  AND p.time >= ? AND p.time <= ?
		  AND c.parent_id != ''
		GROUP BY parent_service, parent_op, child_service, child_op
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 30`,
		from, to, from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopologyEdge
	for rows.Next() {
		var e TopologyEdge
		if err := rows.Scan(&e.ParentService, &e.ParentOp,
			&e.ChildService, &e.ChildOp, &e.Calls); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ServiceTopologyEdge collapses the per-operation join into a
// service-level interaction with a protocol family. One edge per
// (parent_service, child_node, protocol) so the UI can draw
// "service A → service B via HTTP" and "service A → postgres via
// db" as two separate strands even when they share endpoints.
//
// TopLabels carries up to 5 distinct method+endpoint strings by
// frequency — the renderer shows TopLabels[0] inline on the edge
// and surfaces the rest on click-to-expand without a second
// round-trip. DistinctLabels is the global count, which lets the
// UI render "(N endpoints)" hints even when TopLabels truncates.
type ServiceTopologyEdge struct {
	ParentService  string   `json:"parentService"`
	ChildNode      string   `json:"childNode"`
	NodeKind       string   `json:"nodeKind"` // "service" | "db" | "queue" | "cache" | "external"
	Protocol       string   `json:"protocol"` // "http" | "rpc" | "kafka" | "db" | "internal"
	TopLabels      []string `json:"topLabels"`
	DistinctLabels uint64   `json:"distinctLabels"`
	Calls          uint64   `json:"calls"`
}

// GetServiceTopologyEdges returns service-pair interactions with
// protocol classification + a top label set per strand.
//
//   1. Cross-service pass (parent_service != child_service) joins
//      spans on (trace_id, parent_id). Grouped by (parent, child,
//      protocol) so HTTP-only and gRPC-only edges between the
//      same pair render separately.
//
//   2. Infra pass synthesises destination nodes from db_system /
//      msg_system / peer_service for leaf-ish client spans, so
//      databases / queues / external APIs render as nodes the
//      same way real services do.
//
// Both passes use topK(5)(label) for the per-edge top labels and
// uniqExact(label) for the global distinct count. argMax with a
// constant weight (the original v0.5.100 query) was buggy — it
// returned any label, not the most common one.
func (s *Store) GetServiceTopologyEdges(ctx context.Context, from, to time.Time, limit int) ([]ServiceTopologyEdge, error) {
	if limit <= 0 || limit > 100000 {
		limit = 20000
	}
	rows, err := s.conn.Query(ctx, `
		WITH
			multiIf(
				c.db_system  != '', 'db',
				c.msg_system != '', 'kafka',
				c.rpc_system != '', 'rpc',
				c.http_method != '', 'http',
				'internal'
			) AS proto,
			multiIf(
				c.http_method != '', concat(c.http_method, ' ',
					if(c.http_route != '', c.http_route, c.name)),
				c.rpc_method  != '', c.rpc_method,
				c.db_system   != '', concat(c.db_system, ' ', c.name),
				c.msg_system  != '', concat(c.msg_system, ' ', c.name),
				c.name
			) AS label
		SELECT
			p.service_name AS parent_service,
			c.service_name AS child_service,
			proto          AS protocol,
			topK(5)(label) AS top_labels,
			uniqExact(label) AS distinct_labels,
			count()        AS calls
		FROM spans AS c
		INNER JOIN spans AS p
			ON p.trace_id = c.trace_id AND p.span_id = c.parent_id
		WHERE c.time >= ? AND c.time <= ?
		  AND p.time >= ? AND p.time <= ?
		  AND c.parent_id != ''
		  AND p.service_name != c.service_name
		GROUP BY parent_service, child_service, protocol
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 30`,
		from, to, from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	var out []ServiceTopologyEdge
	for rows.Next() {
		var e ServiceTopologyEdge
		if err := rows.Scan(&e.ParentService, &e.ChildNode,
			&e.Protocol, &e.TopLabels, &e.DistinctLabels, &e.Calls); err != nil {
			rows.Close()
			return nil, err
		}
		e.NodeKind = "service"
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	infraRows, err := s.conn.Query(ctx, `
		WITH
			multiIf(
				db_system  != '', concat('db:',    db_system),
				msg_system != '', concat('queue:', msg_system),
				peer_service != '' AND kind = 'client', concat('ext:', peer_service),
				''
			) AS child,
			multiIf(
				db_system  != '', 'db',
				msg_system != '', 'kafka',
				peer_service != '', 'http',
				''
			) AS proto,
			multiIf(
				db_system  != '', 'db',
				msg_system != '', 'queue',
				peer_service != '', 'external',
				''
			) AS kind_out,
			multiIf(
				http_method != '', concat(http_method, ' ',
					if(http_route != '', http_route, name)),
				db_system   != '', name,
				msg_system  != '', name,
				name
			) AS label
		SELECT
			service_name AS parent_service,
			child,
			proto,
			kind_out,
			topK(5)(label) AS top_labels,
			uniqExact(label) AS distinct_labels,
			count() AS calls
		FROM spans
		WHERE time >= ? AND time <= ?
		  AND child != ''
		GROUP BY parent_service, child, proto, kind_out
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 30`,
		from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	defer infraRows.Close()
	for infraRows.Next() {
		var e ServiceTopologyEdge
		if err := infraRows.Scan(&e.ParentService, &e.ChildNode,
			&e.Protocol, &e.NodeKind, &e.TopLabels, &e.DistinctLabels, &e.Calls); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, infraRows.Err()
}
