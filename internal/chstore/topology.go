package chstore

import (
	"context"
	"fmt"
	"strings"
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
	// v0.5.393 — errors + error-rate per edge so the topology
	// page can tint hot edges red and surface (errors / calls)
	// in the tooltip. The errors column landed on
	// topology_edges_5m in v0.5.367; we now pipe it through to
	// the read path so the operator reads "is this edge
	// breaking?" directly off the graph rather than having to
	// click into the dependent service.
	Errors         uint64   `json:"errors"`
	ErrorRate      float64  `json:"errorRate"` // (errors / calls) * 100
	AvgMs          float64  `json:"avgMs"`   // window-wide avg ms (sum/calls)
	P99Ms          float64  `json:"p99Ms"`   // conservative window p99
	// v0.5.409 — known external SaaS / cloud annotation. When
	// NodeKind == "external" and the peer host matches the
	// external_catalogue, these carry the human-friendly display
	// name + category (payments / messaging / cdn / etc.) so the
	// frontend can render a colored badge. Empty when the peer
	// isn't in the catalogue — UI falls back to the raw
	// `ext:<peer>` label.
	ExtDisplay     string   `json:"extDisplay,omitempty"`
	ExtKind        string   `json:"extKind,omitempty"`
	// v0.5.410 — environment annotation per side. Resolved at
	// aggregation time from deployment.environment /
	// service.namespace / k8s.namespace.name resource attrs.
	// Display-only — same-name service in different envs still
	// merges in the MV's ReplacingMergeTree dedup (env not in
	// ORDER BY); a strict per-env split needs a table rebuild
	// and is deferred. Empty when no env attr was present on
	// the underlying spans.
	ParentEnv      string   `json:"parentEnv,omitempty"`
	ChildEnv       string   `json:"childEnv,omitempty"`
}

// RootFlow describes one business-level entry point: the root
// span (kind=server, parent_id='') groups under (service, op) and
// counts how many traces start there. Services carries the set
// of unique services those traces touch, in arbitrary order (use
// GetFlowTopology to recover the call-graph shape for one flow).
// P99Ns is the 99th-percentile root-span duration in the window —
// computed lazily by ComputeFlowsLatencyP99 and merged in at the
// API layer (v0.5.156). 0 = not yet computed / no samples.
type RootFlow struct {
	RootService string   `json:"rootService"`
	RootOp      string   `json:"rootOp"`
	TraceCount  uint64   `json:"traceCount"`
	Services    []string `json:"services"`
	P99Ns       uint64   `json:"p99Ns,omitempty"`
}

// GetRootFlows returns the top business flows by trace count over
// [from, to]. A flow is identified by (root_service, root_op);
// the typical examples are HTTP entry points (POST /login, POST
// /payment), Kafka consumer roots, and scheduled jobs. limit
// caps the number of flows returned so the UI list stays
// scannable. The companion Services slice is materialised via
// groupUniqArray so the operator can see "login flow involves:
// api-gateway, user-service, postgresql, redis" without opening
// each one.
func (s *Store) GetRootFlows(ctx context.Context, from, to time.Time, limit int) ([]RootFlow, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.conn.Query(ctx, `
		WITH root_traces AS (
			SELECT trace_id, service_name AS root_service, name AS root_op
			FROM spans
			WHERE parent_id = '' AND time >= ? AND time <= ?
		)
		SELECT
			rt.root_service,
			rt.root_op,
			uniqExact(rt.trace_id) AS trace_count,
			groupUniqArrayArray(50)(arrayDistinct([sp.service_name])) AS services
		FROM root_traces AS rt
		INNER JOIN spans AS sp
			ON sp.trace_id = rt.trace_id
		WHERE sp.time >= ? AND sp.time <= ?
		GROUP BY rt.root_service, rt.root_op
		ORDER BY trace_count DESC
		LIMIT ?
		SETTINGS max_execution_time = 30`,
		from, to, from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RootFlow
	for rows.Next() {
		var f RootFlow
		if err := rows.Scan(&f.RootService, &f.RootOp, &f.TraceCount, &f.Services); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetFlowTopology returns the service-level subgraph restricted
// to traces whose root span matches (rootService, rootOp). Same
// shape as GetServiceTopologyEdges so the renderer reuses one
// code path. Used by the flow-detail view.
func (s *Store) GetFlowTopology(ctx context.Context, from, to time.Time, rootService, rootOp string, limit int) ([]ServiceTopologyEdge, error) {
	if limit <= 0 || limit > 100000 {
		limit = 20000
	}
	// Two passes mirroring GetServiceTopologyEdges, both filtered
	// to the trace-id set whose root matches the flow signature.
	// The CTE-style filter is materialised once per query so each
	// pass benefits from CH's GLOBAL IN dedup.
	rows, err := s.conn.Query(ctx, `
		WITH root_traces AS (
			SELECT trace_id FROM spans
			WHERE parent_id = ''
			  AND service_name = ? AND name = ?
			  AND time >= ? AND time <= ?
		),
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
			proto AS protocol,
			topK(5)(label) AS top_labels,
			uniqExact(label) AS distinct_labels,
			count() AS calls
		FROM spans AS c
		GLOBAL INNER JOIN (
			SELECT trace_id, span_id, service_name
			FROM spans
			WHERE time >= ? AND time <= ?
		) AS p
			ON p.trace_id = c.trace_id AND p.span_id = c.parent_id
		WHERE c.trace_id GLOBAL IN (SELECT trace_id FROM root_traces)
		  AND c.time >= ? AND c.time <= ?
		  AND c.parent_id != ''
		  AND p.service_name != c.service_name
		GROUP BY parent_service, child_service, protocol
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 60,
		         join_algorithm = 'grace_hash',
		         max_bytes_in_join = 2000000000,
		         max_memory_usage = 4000000000,
		         distributed_product_mode = 'global'`,
		rootService, rootOp, from, to,
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
		// v0.5.407 — templating runs post-Scan so the stored
		// labels stay raw (no MV migration), only the rendered
		// edges show templated forms. Dedupe collapses concrete-
		// id variants that map to the same template.
		e.TopLabels = dedupTemplatedLabels(e.TopLabels)
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Infra pass — same filter, db/msg/peer destinations.
	infra, err := s.conn.Query(ctx, `
		WITH root_traces AS (
			SELECT trace_id FROM spans
			WHERE parent_id = ''
			  AND service_name = ? AND name = ?
			  AND time >= ? AND time <= ?
		),
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
			service_name AS parent_service, child, proto, kind_out,
			topK(5)(label) AS top_labels,
			uniqExact(label) AS distinct_labels,
			count() AS calls
		FROM spans
		WHERE trace_id GLOBAL IN (SELECT trace_id FROM root_traces)
		  AND time >= ? AND time <= ?
		  AND child != ''
		GROUP BY parent_service, child, proto, kind_out
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 30,
		         distributed_product_mode = 'global'`,
		rootService, rootOp, from, to,
		from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	defer infra.Close()
	for infra.Next() {
		var e ServiceTopologyEdge
		if err := infra.Scan(&e.ParentService, &e.ChildNode,
			&e.Protocol, &e.NodeKind, &e.TopLabels, &e.DistinctLabels, &e.Calls); err != nil {
			return nil, err
		}
		e.TopLabels = dedupTemplatedLabels(e.TopLabels)
		out = append(out, e)
	}
	return out, infra.Err()
}

// EdgeInstance is one (peer_service) bucket for an infra edge —
// the actual host / cluster behind a `db:postgresql` or
// `queue:kafka` node. Drives the EdgeDetailPanel "per-instance"
// expand in topology so the operator can see which postgres
// instance is hot without leaving the diagram.
type EdgeInstance struct {
	Instance string  `json:"instance"`
	Calls    uint64  `json:"calls"`
	AvgMs    float64 `json:"avgMs"`
	P99Ms    float64 `json:"p99Ms"`
}

// GetEdgeInstances returns the peer_service breakdown for one
// (parentService, system, kind) edge over [from, to]. Bounded by
// the spans (service_name, time) primary key + filtered by
// db_system / msg_system so the scan stays tight even at
// billions of spans/day. Limit caps the buckets — 50 hosts is
// more than enough for any realistic per-service db/queue fan-out.
//
// kind: "db" → filter by db_system; "queue" → filter by msg_system.
// Returns empty slice when nothing matches (empty window).
func (s *Store) GetEdgeInstances(ctx context.Context, parentService, system, kind string, from, to time.Time, limit int) ([]EdgeInstance, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var sysCol string
	switch kind {
	case "db":
		sysCol = "db_system"
	case "queue":
		sysCol = "msg_system"
	default:
		return []EdgeInstance{}, nil
	}
	rows, err := s.conn.Query(ctx, `
		SELECT
			coalesce(nullIf(peer_service, ''), 'unknown') AS instance,
			toUInt64(count())                              AS calls,
			toFloat64(avg(duration)) / 1e6                 AS avg_ms,
			toFloat64(quantile(0.99)(duration)) / 1e6      AS p99_ms
		FROM spans
		WHERE service_name = ?
		  AND `+sysCol+` = ?
		  AND time >= ? AND time <= ?
		GROUP BY instance
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 10,
		         distributed_product_mode = 'global'`,
		parentService, system, from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EdgeInstance{}
	for rows.Next() {
		var e EdgeInstance
		if err := rows.Scan(&e.Instance, &e.Calls, &e.AvgMs, &e.P99Ms); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// WriteTopologyBucket aggregates the service-level topology for
// one 5-min window and inserts the result rows into
// topology_edges_5m. Two passes (cross-service join + infra
// detection), each INSERT ... SELECT in a single CH round-trip.
//
// Idempotent: re-running over the same bucket inserts new rows
// with the same primary key (time_bucket, parent_service,
// child_node, node_kind, protocol). ReplacingMergeTree(version)
// dedupes them at read time by keeping the highest version. A
// background goroutine in internal/topology calls this every
// 5 minutes; the API never invokes the heavy join directly.
func (s *Store) WriteTopologyBucket(ctx context.Context, bucketStart time.Time) error {
	end := bucketStart.Add(5 * time.Minute)

	// Cross-service pass — service A → service B via http/rpc.
	//
	// Memory note: the right side of the join (`p`) is column-
	// projected to (trace_id, span_id, service_name) and pre-
	// filtered to the bucket window in a subquery so CH doesn't
	// load the whole spans row block into the hash side. With
	// join_algorithm='grace_hash' the hash table spills to disk
	// past the per-query limit, so a 1B-span 5-min slice still
	// fits even on a modest 4-8 GB CH. allow_experimental_analyzer
	// off keeps grace_hash on the legacy planner where it's
	// stable across 23.x→24.x.
	if err := s.conn.Exec(ctx, `
		INSERT INTO topology_edges_5m
			(time_bucket, parent_service, child_node, node_kind,
			 protocol, top_labels, distinct_labels, calls,
			 sum_duration_ns, p99_ms, errors,
			 parent_env, child_env, version)
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
			) AS label,
			-- v0.5.410 — per-span env derivation. Same coalesce
			-- chain across child + parent so the operator's
			-- prod/stage chip reads the same way regardless of
			-- which side the env came from.
			coalesce(
				nullIf(c.res_values[indexOf(c.res_keys, 'deployment.environment')], ''),
				nullIf(c.res_values[indexOf(c.res_keys, 'service.namespace')], ''),
				nullIf(c.res_values[indexOf(c.res_keys, 'k8s.namespace.name')], ''),
				''
			) AS c_env
		SELECT
			toDateTime(?, 'UTC') AS time_bucket,
			p.service_name        AS parent_service,
			c.service_name        AS child_node,
			'service'             AS node_kind,
			proto                 AS protocol,
			topK(5)(label)        AS top_labels,
			toUInt32(uniqExact(label)) AS distinct_labels,
			toUInt64(count())     AS calls,
			toUInt64(sum(c.duration)) AS sum_duration_ns,
			toFloat64(quantileExact(0.99)(c.duration)) / 1e6 AS p99_ms,
			-- v0.5.367 — per-edge error count powers /api/service-graph
			-- ErrorRate reads from the MV (no more raw-spans self-join).
			toUInt64(countIf(c.status_code = 'error')) AS errors,
			-- v0.5.410 — env per side. any() picks an arbitrary
			-- representative within the bucket; cardinality of env
			-- per (service, 5min) is typically 1 so the pick is
			-- stable for the operator's eye.
			any(p.env)            AS parent_env,
			any(c_env)            AS child_env,
			toUInt64(?)           AS version
		FROM spans AS c
		GLOBAL INNER JOIN (
			SELECT trace_id, span_id, service_name,
			       coalesce(
			         nullIf(res_values[indexOf(res_keys, 'deployment.environment')], ''),
			         nullIf(res_values[indexOf(res_keys, 'service.namespace')], ''),
			         nullIf(res_values[indexOf(res_keys, 'k8s.namespace.name')], ''),
			         ''
			       ) AS env
			FROM spans
			WHERE time >= toDateTime(?, 'UTC') AND time < toDateTime(?, 'UTC')
		) AS p
			ON p.trace_id = c.trace_id AND p.span_id = c.parent_id
		WHERE c.time >= toDateTime(?, 'UTC') AND c.time < toDateTime(?, 'UTC')
		  AND c.parent_id != ''
		  AND p.service_name != c.service_name
		GROUP BY parent_service, child_node, protocol
		SETTINGS max_execution_time = 180,
		         join_algorithm = 'grace_hash',
		         max_bytes_in_join = 4000000000,
		         max_memory_usage = 8000000000,
		         distributed_product_mode = 'global'`,
		bucketStart.Unix(),
		uint64(time.Now().UnixNano()),
		bucketStart.Unix(), end.Unix(),
		bucketStart.Unix(), end.Unix(),
	); err != nil {
		return fmt.Errorf("topology bucket cross-service pass: %w", err)
	}

	// Infra pass — service → db/queue/external.
	// v0.5.408 — DB / queue child_node now includes the host
	// instance suffix (e.g. `db:postgres@10.0.1.5` or
	// `db:postgres@orders-rds.aws.amazonaws.com`). Datadog /
	// Honeycomb / Dynatrace separate instances of the same DB
	// system because operationally they're different
	// destinations — different replicas, different availability,
	// different latency. Host resolved via the same coalesce
	// chain db_summary_5m already uses (peer_service →
	// server.address attr → net.peer.name attr). When all are
	// empty the node falls back to the flat `db:<system>` form.
	// External peer hosts keep the prior `ext:<service>` shape
	// since peer_service IS the canonical external name.
	if err := s.conn.Exec(ctx, `
		INSERT INTO topology_edges_5m
			(time_bucket, parent_service, child_node, node_kind,
			 protocol, top_labels, distinct_labels, calls,
			 sum_duration_ns, p99_ms, errors,
			 parent_env, child_env, version)
		WITH
			coalesce(
				nullIf(peer_service, ''),
				nullIf(attr_values[indexOf(attr_keys, 'server.address')], ''),
				nullIf(attr_values[indexOf(attr_keys, 'net.peer.name')], ''),
				''
			) AS infra_host,
			-- v0.5.410 — derive parent_env from resource attrs.
			-- child_env stays empty for infra targets — db/queue/
			-- external nodes don't inherit the caller's env (they
			-- ARE cross-env infra).
			coalesce(
				nullIf(res_values[indexOf(res_keys, 'deployment.environment')], ''),
				nullIf(res_values[indexOf(res_keys, 'service.namespace')], ''),
				nullIf(res_values[indexOf(res_keys, 'k8s.namespace.name')], ''),
				''
			) AS p_env,
			multiIf(
				db_system  != '' AND infra_host != '',
					concat('db:',    db_system, '@', infra_host),
				db_system  != '',
					concat('db:',    db_system),
				msg_system != '' AND infra_host != '',
					concat('queue:', msg_system, '@', infra_host),
				msg_system != '',
					concat('queue:', msg_system),
				peer_service != '' AND kind = 'client',
					concat('ext:', peer_service),
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
			-- Label format: include the instance/host (peer_service)
			-- when present so the edge detail panel surfaces "which
			-- postgres instance is hot" without forcing a separate
			-- query. Falls back to system+operation when peer is
			-- empty so labels stay informative on older spans.
			multiIf(
				http_method != '', concat(http_method, ' ',
					if(http_route != '', http_route, name)),
				db_system   != '' AND peer_service != '',
					concat(db_system, '@', peer_service, ' ', name),
				db_system   != '', concat(db_system, ' ', name),
				msg_system  != '' AND peer_service != '',
					concat(msg_system, '@', peer_service, ' ', name),
				msg_system  != '', concat(msg_system, ' ', name),
				name
			) AS label
		SELECT
			toDateTime(?, 'UTC') AS time_bucket,
			service_name         AS parent_service,
			child                AS child_node,
			kind_out             AS node_kind,
			proto                AS protocol,
			topK(5)(label)       AS top_labels,
			toUInt32(uniqExact(label)) AS distinct_labels,
			toUInt64(count())    AS calls,
			toUInt64(sum(duration)) AS sum_duration_ns,
			toFloat64(quantileExact(0.99)(duration)) / 1e6 AS p99_ms,
			-- v0.5.367 — infra-edge errors mirror the service-pair pass.
			toUInt64(countIf(status_code = 'error')) AS errors,
			any(p_env)           AS parent_env,
			''                   AS child_env,
			toUInt64(?)          AS version
		FROM spans
		WHERE time >= toDateTime(?, 'UTC') AND time < toDateTime(?, 'UTC')
		  AND child != ''
		GROUP BY parent_service, child, proto, kind_out
		SETTINGS max_execution_time = 120,
		         distributed_product_mode = 'global'`,
		bucketStart.Unix(),
		uint64(time.Now().UnixNano()),
		bucketStart.Unix(), end.Unix(),
	); err != nil {
		return fmt.Errorf("topology bucket infra pass: %w", err)
	}
	return nil
}

// WriteTopologyOpBucket pre-aggregates per-op edges for a 5-min
// bucket. Same shape as WriteTopologyBucket but at op granularity
// — used by /api/topology (operation deep-dive view).
func (s *Store) WriteTopologyOpBucket(ctx context.Context, bucketStart time.Time) error {
	end := bucketStart.Add(5 * time.Minute)
	if err := s.conn.Exec(ctx, `
		INSERT INTO topology_op_edges_5m
			(time_bucket, parent_service, parent_op,
			 child_service, child_op, calls, version)
		SELECT
			toDateTime(?, 'UTC') AS time_bucket,
			p.service_name AS parent_service,
			p.name         AS parent_op,
			c.service_name AS child_service,
			c.name         AS child_op,
			toUInt64(count()) AS calls,
			toUInt64(?)    AS version
		FROM spans AS c
		GLOBAL INNER JOIN (
			SELECT trace_id, span_id, service_name, name
			FROM spans
			WHERE time >= toDateTime(?, 'UTC') AND time < toDateTime(?, 'UTC')
		) AS p
			ON p.trace_id = c.trace_id AND p.span_id = c.parent_id
		WHERE c.time >= toDateTime(?, 'UTC') AND c.time < toDateTime(?, 'UTC')
		  AND c.parent_id != ''
		GROUP BY parent_service, parent_op, child_service, child_op
		SETTINGS max_execution_time = 180,
		         join_algorithm = 'grace_hash',
		         max_bytes_in_join = 4000000000,
		         max_memory_usage = 8000000000,
		         distributed_product_mode = 'global'`,
		bucketStart.Unix(),
		uint64(time.Now().UnixNano()),
		bucketStart.Unix(), end.Unix(),
		bucketStart.Unix(), end.Unix(),
	); err != nil {
		return fmt.Errorf("topology op bucket: %w", err)
	}
	return nil
}

// ReadTopologyOpEdgesAgg reads per-op edges from the aggregated
// table for the requested window. Returns the full edge set; the
// API handler runs the BFS to extract the bounded subgraph.
func (s *Store) ReadTopologyOpEdgesAgg(ctx context.Context, from, to time.Time, limit int) ([]TopologyEdge, error) {
	if limit <= 0 || limit > 200000 {
		limit = 50000
	}
	rows, err := s.conn.Query(ctx, `
		SELECT
			parent_service, parent_op, child_service, child_op,
			sum(calls) AS total_calls
		FROM topology_op_edges_5m FINAL
		WHERE time_bucket >= toStartOfFiveMinute(toDateTime(?, 'UTC'))
		  AND time_bucket <  toStartOfFiveMinute(toDateTime(?, 'UTC')) + INTERVAL 5 MINUTE
		GROUP BY parent_service, parent_op, child_service, child_op
		ORDER BY total_calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`,
		from.Unix(), to.Unix(), limit,
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

// WriteRootFlowsBucket pre-aggregates business flows for one
// 5-min window. Counts traces + collects unique services per
// (root_service, root_op). Mirrors GetRootFlows but materialises
// the result into the agg table for cheap fan-out reads later.
func (s *Store) WriteRootFlowsBucket(ctx context.Context, bucketStart time.Time) error {
	end := bucketStart.Add(5 * time.Minute)
	if err := s.conn.Exec(ctx, `
		INSERT INTO topology_root_flows_5m
			(time_bucket, root_service, root_op,
			 trace_count, services, version)
		WITH root_traces AS (
			SELECT trace_id, service_name AS root_service, name AS root_op
			FROM spans
			WHERE parent_id = ''
			  AND time >= toDateTime(?, 'UTC')
			  AND time <  toDateTime(?, 'UTC')
		)
		SELECT
			toDateTime(?, 'UTC') AS time_bucket,
			rt.root_service,
			rt.root_op,
			toUInt64(uniqExact(rt.trace_id)) AS trace_count,
			groupUniqArrayArray(50)(arrayDistinct([sp.service_name])) AS services,
			toUInt64(?) AS version
		FROM root_traces AS rt
		GLOBAL INNER JOIN (
			SELECT trace_id, service_name
			FROM spans
			WHERE time >= toDateTime(?, 'UTC') AND time < toDateTime(?, 'UTC')
		) AS sp ON sp.trace_id = rt.trace_id
		GROUP BY rt.root_service, rt.root_op
		SETTINGS max_execution_time = 180,
		         join_algorithm = 'grace_hash',
		         max_bytes_in_join = 4000000000,
		         max_memory_usage = 8000000000,
		         distributed_product_mode = 'global'`,
		bucketStart.Unix(), end.Unix(),
		bucketStart.Unix(),
		uint64(time.Now().UnixNano()),
		bucketStart.Unix(), end.Unix(),
	); err != nil {
		return fmt.Errorf("topology root flows bucket: %w", err)
	}
	return nil
}

// ReadRootFlowsAgg reads pre-aggregated business flows for a
// window. trace_count is summed across buckets; services arrays
// are merged + deduplicated. Limit caps the number of flows
// returned to the heaviest by trace volume.
func (s *Store) ReadRootFlowsAgg(ctx context.Context, from, to time.Time, limit int) ([]RootFlow, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.conn.Query(ctx, `
		SELECT
			root_service,
			root_op,
			toUInt64(sum(trace_count)) AS total_traces,
			arrayDistinct(arrayFlatten(groupArray(services))) AS services
		FROM topology_root_flows_5m FINAL
		WHERE time_bucket >= toStartOfFiveMinute(toDateTime(?, 'UTC'))
		  AND time_bucket <  toStartOfFiveMinute(toDateTime(?, 'UTC')) + INTERVAL 5 MINUTE
		GROUP BY root_service, root_op
		ORDER BY total_traces DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`,
		from.Unix(), to.Unix(), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RootFlow
	for rows.Next() {
		var f RootFlow
		if err := rows.Scan(&f.RootService, &f.RootOp, &f.TraceCount, &f.Services); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FlowSig identifies a business flow by its (root_service, root_op)
// pair. Used as a bounded IN-list for the p99 enrichment so the
// query never scans more roots than the caller already listed.
type FlowSig struct {
	RootService string
	RootOp      string
}

// ComputeFlowsLatencyP99 returns the p99 root-span duration (ns)
// for each requested flow signature over the window. Keyed on
// "service\x00op" so the caller can look up without a struct
// equality dance. Empty input → empty map, no query.
//
// The IN list is bounded by the caller's flow limit (cap 200 on
// the API surface), so even at billion-span scale this is a thin
// GROUP BY over (parent_id='') roots filtered to a handful of
// signatures — far cheaper than ranking flows from raw spans,
// which is why we let the agg path own ranking and use this only
// for latency enrichment.
func (s *Store) ComputeFlowsLatencyP99(ctx context.Context, from, to time.Time, sigs []FlowSig) (map[string]uint64, error) {
	if len(sigs) == 0 {
		return map[string]uint64{}, nil
	}
	// Build the IN-list as a flat (svc, op, svc, op, …) arg slice;
	// CH accepts `IN ((?,?), (?,?), …)` with positional binding.
	placeholders := make([]byte, 0, len(sigs)*8)
	args := make([]any, 0, 2+len(sigs)*2)
	args = append(args, from, to)
	for i, sig := range sigs {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '(', '?', ',', '?', ')')
		args = append(args, sig.RootService, sig.RootOp)
	}
	q := `
		SELECT service_name, name,
		       toUInt64(quantile(0.99)(toFloat64(duration))) AS p99_ns
		FROM spans
		WHERE parent_id = ''
		  AND time >= ? AND time < ?
		  AND (service_name, name) IN (` + string(placeholders) + `)
		GROUP BY service_name, name
		SETTINGS max_execution_time = 15`
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]uint64, len(sigs))
	for rows.Next() {
		var svc, op string
		var p99 uint64
		if err := rows.Scan(&svc, &op, &p99); err != nil {
			return nil, err
		}
		out[svc+"\x00"+op] = p99
	}
	return out, rows.Err()
}

// ListOpsForService returns the operation names that appear as
// outbound callers for a given service in the window. Drives the
// op-picker dropdown on the operation deep-dive view. Reads
// directly from the agg table so the response is fast.
func (s *Store) ListOpsForService(ctx context.Context, service string, from, to time.Time) ([]string, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT DISTINCT parent_op
		FROM topology_op_edges_5m FINAL
		WHERE parent_service = ?
		  AND time_bucket >= toStartOfFiveMinute(toDateTime(?, 'UTC'))
		  AND time_bucket <  toStartOfFiveMinute(toDateTime(?, 'UTC')) + INTERVAL 5 MINUTE
		ORDER BY parent_op
		LIMIT 500
		SETTINGS max_execution_time = 5`,
		service, from.Unix(), to.Unix(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var op string
		if err := rows.Scan(&op); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// ReadServiceTopologyAgg reads pre-aggregated topology rows for
// a window from topology_edges_5m. Each row in the agg table is
// one 5-min bucket; we sum calls + merge top_labels arrays across
// buckets to give an aggregate over the requested window.
//
// distinct_labels is approximated as the count of unique labels in
// the merged top_labels array — at 5 labels per bucket that's
// accurate up to a few dozen endpoints per strand, which is plenty
// for human-readable topology.
//
// Window is rounded out to the surrounding 5-min boundaries so a
// partially-covered bucket isn't dropped silently.
func (s *Store) ReadServiceTopologyAgg(ctx context.Context, from, to time.Time, limit int) ([]ServiceTopologyEdge, error) {
	if limit <= 0 || limit > 100000 {
		limit = 20000
	}
	// Subquery: aggregate within groups first, then post-process
	// the merged label array. Inlining the merged array twice in
	// the outer SELECT (once for arraySlice, once for length)
	// makes CH's analyzer reject the query as "aggregate inside
	// aggregate" — even though both wrappers are scalar array
	// functions. Splitting the merge into a named subquery field
	// sidesteps the false-positive.
	// toUInt64 casts on length() + sum() because the CH Go driver
	// is strict on Scan type matching — a UInt32 column won't bind
	// to *uint64 even though the value fits. Struct fields stay
	// uint64 so JSON encoding keeps the same shape across drivers.
	rows, err := s.conn.Query(ctx, `
		SELECT
			parent_service,
			child_node,
			node_kind,
			protocol,
			arraySlice(merged, 1, 5) AS top_labels,
			toUInt64(length(merged)) AS distinct_labels,
			total_calls,
			total_errors,
			avg_ms,
			max_p99_ms,
			parent_env,
			child_env
		FROM (
			SELECT
				parent_service,
				child_node,
				any(node_kind) AS node_kind,
				protocol,
				arrayDistinct(arrayFlatten(groupArray(top_labels))) AS merged,
				toUInt64(sum(calls)) AS total_calls,
				toUInt64(sum(errors)) AS total_errors,
				if(sum(calls) > 0,
				   toFloat64(sum(sum_duration_ns)) / sum(calls) / 1e6,
				   0) AS avg_ms,
				toFloat64(max(p99_ms)) AS max_p99_ms,
				-- v0.5.410 — env per side. any() picks an
				-- arbitrary representative across the merged
				-- buckets; in practice the env is stable per
				-- (service, day) so the pick is consistent.
				any(parent_env) AS parent_env,
				any(child_env)  AS child_env
			FROM topology_edges_5m FINAL
			WHERE time_bucket >= toStartOfFiveMinute(toDateTime(?, 'UTC'))
			  AND time_bucket <  toStartOfFiveMinute(toDateTime(?, 'UTC')) + INTERVAL 5 MINUTE
			GROUP BY parent_service, child_node, protocol
		)
		ORDER BY total_calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`,
		from.Unix(), to.Unix(), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceTopologyEdge
	for rows.Next() {
		var e ServiceTopologyEdge
		if err := rows.Scan(&e.ParentService, &e.ChildNode, &e.NodeKind,
			&e.Protocol, &e.TopLabels, &e.DistinctLabels, &e.Calls,
			&e.Errors, &e.AvgMs, &e.P99Ms,
			&e.ParentEnv, &e.ChildEnv); err != nil {
			return nil, err
		}
		if e.Calls > 0 {
			e.ErrorRate = float64(e.Errors) / float64(e.Calls) * 100
		}
		e.TopLabels = dedupTemplatedLabels(e.TopLabels)
		// v0.5.409 — annotate known 3rd-party external nodes.
		// External node format from the aggregator is
		// "ext:<peer_name>"; strip the prefix before looking up
		// in the catalogue. NodeKind=="external" gate keeps the
		// classifier from running on service/db/queue rows.
		if e.NodeKind == "external" && strings.HasPrefix(e.ChildNode, "ext:") {
			peer := strings.TrimPrefix(e.ChildNode, "ext:")
			if disp, kind, ok := classifyExternal(peer); ok {
				e.ExtDisplay = disp
				e.ExtKind = kind
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
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
		e.TopLabels = dedupTemplatedLabels(e.TopLabels)
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
