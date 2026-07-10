package chstore

// v0.8.446 — External API monitoring read layer (SigNoz/Uptrace
// gap-closure Wave 3 / A1). Answers "which 3rd-party APIs do my
// services depend on, and how are those dependencies behaving?"
// — the Datadog API-catalog shape.
//
// MV-first by construction: every read here hits topology_edges_5m
// (node_kind='external'), which the 5-min topology aggregator already
// fills with calls / errors / sum_duration_ns / p99_ms / top_labels
// per (caller service, ext:<host>) edge. No new MV, no raw-spans
// scan; the p99 merge is max(p99_ms) across buckets — the same
// conservative roll-up the flow graph reads use.

import (
	"context"
	"time"
)

// ExternalHost is one row of the /external overview — a distinct
// third-party destination aggregated across every calling service
// in the window. Display/Category come from the static vendor
// catalogue (external_catalogue.go) and stay empty for unrecognised
// hosts — the frontend then shows the raw host without a badge.
type ExternalHost struct {
	Host        string   `json:"host"`
	Display     string   `json:"display,omitempty"`
	Category    string   `json:"category,omitempty"`
	Callers     uint64   `json:"callers"`
	CallerNames []string `json:"callerNames"`
	Calls       uint64   `json:"calls"`
	Errors      uint64   `json:"errors"`
	ErrorRate   float64  `json:"errorRate"`
	AvgMs       float64  `json:"avgMs"`
	P99Ms       float64  `json:"p99Ms"`
	TopLabels   []string `json:"topLabels"`
}

// ExternalCaller is the per-service breakdown inside one host's
// detail drawer — who depends on this API and how hard.
type ExternalCaller struct {
	Service   string   `json:"service"`
	Calls     uint64   `json:"calls"`
	Errors    uint64   `json:"errors"`
	ErrorRate float64  `json:"errorRate"`
	AvgMs     float64  `json:"avgMs"`
	P99Ms     float64  `json:"p99Ms"`
	TopLabels []string `json:"topLabels"`
}

// ExternalTrendPoint is one 5-minute bucket of the host's RED trend.
// Bucket is unix seconds (the MV's native grain).
type ExternalTrendPoint struct {
	Bucket int64   `json:"bucket"`
	Calls  uint64  `json:"calls"`
	Errors uint64  `json:"errors"`
	AvgMs  float64 `json:"avgMs"`
	P99Ms  float64 `json:"p99Ms"`
}

// ExternalHostDetail is the drawer payload for one host.
type ExternalHostDetail struct {
	Host     string               `json:"host"`
	Display  string               `json:"display,omitempty"`
	Category string               `json:"category,omitempty"`
	Callers  []ExternalCaller     `json:"callers"`
	Trend    []ExternalTrendPoint `json:"trend"`
}

// GetExternalHosts returns every external destination seen in the
// window, busiest first. Capped at 500 rows — beyond that the page
// is a search problem, not a list problem.
func (s *Store) GetExternalHosts(ctx context.Context, from, to time.Time) ([]ExternalHost, error) {
	// Bucket-align the lower bound (same idiom as the other *_5m
	// readers) so a 13:03 winStart catches the 13:00 bucket.
	bucketStart := from.Truncate(5 * time.Minute)

	// The GLOBAL NOT IN excludes instrumented services: SDKs that set
	// peer.service on *internal* calls (seen in the wild and in the
	// bundled demos) produce ext: edges whose "host" is really another
	// monitored service. Datadog semantics — if it emits spans, it is
	// not a third party; it belongs on /services, not here. The
	// exclusion set is DISTINCT service_name over the same window from
	// service_summary_5m (every instrumented service lands there),
	// small by construction (thousands). GLOBAL for distributed
	// installs (make audit CHECK 5); a no-op on single-node.
	rows, err := s.conn.Query(ctx, `
		SELECT substring(child_node, 5)          AS host,
		       uniqExact(parent_service)         AS callers,
		       arraySort(groupUniqArray(8)(parent_service)) AS caller_names,
		       sum(calls)                        AS total_calls,
		       sum(errors)                       AS total_errors,
		       sum(sum_duration_ns) / nullIf(sum(calls), 0) / 1e6 AS avg_ms,
		       max(p99_ms)                       AS p99_ms,
		       arraySlice(arrayDistinct(arrayFlatten(groupArray(top_labels))), 1, 8) AS labels
		FROM topology_edges_5m FINAL
		WHERE node_kind = 'external'
		  AND time_bucket >= ? AND time_bucket <= ?
		  AND substring(child_node, 5) GLOBAL NOT IN (
			SELECT DISTINCT service_name FROM service_summary_5m
			WHERE time_bucket >= ? AND time_bucket <= ?
		  )
		GROUP BY child_node
		ORDER BY total_calls DESC
		LIMIT 500
		SETTINGS max_execution_time = 10`,
		bucketStart, to, bucketStart, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ExternalHost{}
	for rows.Next() {
		var h ExternalHost
		var avgMs *float64
		if err := rows.Scan(&h.Host, &h.Callers, &h.CallerNames,
			&h.Calls, &h.Errors, &avgMs, &h.P99Ms, &h.TopLabels); err != nil {
			return nil, err
		}
		h.AvgMs = safeF(avgMs)
		if h.Calls > 0 {
			h.ErrorRate = float64(h.Errors) * 100 / float64(h.Calls)
		}
		if display, kind, ok := classifyExternal(h.Host); ok {
			h.Display, h.Category = display, kind
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetExternalHostDetail returns the drawer payload for one host:
// per-caller breakdown plus the 5-minute RED trend. Two bounded MV
// reads; either could legitimately be empty (host aged out of the
// window) — the caller renders the empty state, not an error.
func (s *Store) GetExternalHostDetail(ctx context.Context, host string, from, to time.Time) (*ExternalHostDetail, error) {
	bucketStart := from.Truncate(5 * time.Minute)
	d := &ExternalHostDetail{Host: host}
	if display, kind, ok := classifyExternal(host); ok {
		d.Display, d.Category = display, kind
	}

	// Per-caller breakdown, heaviest dependency first. The GLOBAL
	// NOT IN mirrors GetExternalHosts' instrumented-service
	// exclusion — without it a hand-edited ?host=<service> deep
	// link would render a monitored service as a third party; with
	// it the drawer's empty state renders, matching list semantics.
	rows, err := s.conn.Query(ctx, `
		SELECT parent_service,
		       sum(calls)  AS total_calls,
		       sum(errors) AS total_errors,
		       sum(sum_duration_ns) / nullIf(sum(calls), 0) / 1e6 AS avg_ms,
		       max(p99_ms) AS p99_ms,
		       arraySlice(arrayDistinct(arrayFlatten(groupArray(top_labels))), 1, 5) AS labels
		FROM topology_edges_5m FINAL
		WHERE node_kind = 'external'
		  AND child_node = concat('ext:', ?)
		  AND time_bucket >= ? AND time_bucket <= ?
		  AND ? GLOBAL NOT IN (
			SELECT DISTINCT service_name FROM service_summary_5m
			WHERE time_bucket >= ? AND time_bucket <= ?
		  )
		GROUP BY parent_service
		ORDER BY total_calls DESC
		LIMIT 100
		SETTINGS max_execution_time = 10`,
		host, bucketStart, to, host, bucketStart, to)
	if err != nil {
		return nil, err
	}
	d.Callers = []ExternalCaller{}
	for rows.Next() {
		var c ExternalCaller
		var avgMs *float64
		if err := rows.Scan(&c.Service, &c.Calls, &c.Errors,
			&avgMs, &c.P99Ms, &c.TopLabels); err != nil {
			rows.Close()
			return nil, err
		}
		c.AvgMs = safeF(avgMs)
		if c.Calls > 0 {
			c.ErrorRate = float64(c.Errors) * 100 / float64(c.Calls)
		}
		d.Callers = append(d.Callers, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 5-minute trend across all callers. 14d TTL × 5m grain caps
	// this at ~4k buckets; LIMIT guards a TTL change from silently
	// unbounding the read.
	trows, err := s.conn.Query(ctx, `
		SELECT toUnixTimestamp(time_bucket) AS bucket,
		       sum(calls)  AS total_calls,
		       sum(errors) AS total_errors,
		       sum(sum_duration_ns) / nullIf(sum(calls), 0) / 1e6 AS avg_ms,
		       max(p99_ms) AS p99_ms
		FROM topology_edges_5m FINAL
		WHERE node_kind = 'external'
		  AND child_node = concat('ext:', ?)
		  AND time_bucket >= ? AND time_bucket <= ?
		  AND ? GLOBAL NOT IN (
			SELECT DISTINCT service_name FROM service_summary_5m
			WHERE time_bucket >= ? AND time_bucket <= ?
		  )
		GROUP BY time_bucket
		ORDER BY time_bucket ASC
		LIMIT 5000
		SETTINGS max_execution_time = 10`,
		host, bucketStart, to, host, bucketStart, to)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	d.Trend = []ExternalTrendPoint{}
	for trows.Next() {
		var p ExternalTrendPoint
		var bucket uint32
		var avgMs *float64
		if err := trows.Scan(&bucket, &p.Calls, &p.Errors, &avgMs, &p.P99Ms); err != nil {
			return nil, err
		}
		p.Bucket = int64(bucket)
		p.AvgMs = safeF(avgMs)
		d.Trend = append(d.Trend, p)
	}
	return d, trows.Err()
}
