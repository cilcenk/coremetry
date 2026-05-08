package chstore

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// AggSpanNode is one position in the multi-trace aggregated tree
// returned by AggregateServiceStructure. A node identifies a unique
// `(parent path → service → operation)` triple — every span across
// the sampled traces with that exact ancestry contributes to the
// counts. Keeps the tree shape (parent / child relationship intact)
// while collapsing literal repetitions into a single visible row.
//
// JSON shape mirrors what the SPA's AggregatedStructure component
// expects.
type AggSpanNode struct {
	Service    string         `json:"service"`
	Operation  string         `json:"operation"`
	Kind       string         `json:"kind,omitempty"`
	Count      int            `json:"count"`
	AvgMs      float64        `json:"avgMs"`
	MaxMs      float64        `json:"maxMs"`
	ErrorCount int            `json:"errorCount"`
	// AvgStartMs is the mean offset (in ms) from the trace's
	// earliest span — drives the bar's left edge in the renderer
	// so the visual chronology survives aggregation.
	AvgStartMs float64        `json:"avgStartMs"`
	Children   []*AggSpanNode `json:"children,omitempty"`
}

// GetSpansForTraces fetches every span belonging to the supplied
// trace IDs in a single round-trip. Used by the structure
// aggregator to avoid N round-trips per sample.
func (s *Store) GetSpansForTraces(ctx context.Context, traceIDs []string) ([]SpanRow, error) {
	if len(traceIDs) == 0 {
		return nil, nil
	}
	holders := make([]string, len(traceIDs))
	args := make([]any, len(traceIDs))
	for i, id := range traceIDs {
		holders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(`
		SELECT trace_id, span_id, parent_id, name, kind, service_name, host_name,
		       time, duration, status_code, status_msg,
		       attr_keys, attr_values, res_keys, res_values,
		       events, scope_name,
		       db_system, db_statement, http_method, http_route, http_status, peer_service
		FROM spans
		WHERE trace_id IN (%s)
		ORDER BY trace_id, time ASC
		SETTINGS max_execution_time = 30`, strings.Join(holders, ","))

	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []SpanRow{}
	for rows.Next() {
		var sp SpanRow
		var t time.Time
		var dur int64
		var attrK, attrV, resK, resV []string
		var eventsJSON string
		if err := rows.Scan(
			&sp.TraceID, &sp.SpanID, &sp.ParentSpanID, &sp.Name, &sp.Kind, &sp.ServiceName, &sp.HostName,
			&t, &dur, &sp.StatusCode, &sp.StatusMessage,
			&attrK, &attrV, &resK, &resV,
			&eventsJSON, &sp.ScopeName,
			&sp.DBSystem, &sp.DBStatement, &sp.HTTPMethod, &sp.HTTPRoute, &sp.HTTPStatus, &sp.PeerService,
		); err != nil {
			return nil, err
		}
		sp.StartTime = t.UnixNano()
		sp.EndTime = t.UnixNano() + dur
		sp.DurationMs = float64(dur) / 1e6
		sp.Attributes = arraysToMap(attrK, attrV)
		sp.ResourceAttributes = arraysToMap(resK, resV)
		_ = json.Unmarshal([]byte(eventsJSON), &sp.Events)
		out = append(out, sp)
	}
	return out, rows.Err()
}

// AggregateServiceStructure builds a Grafana-Drilldown-style
// composite span tree across the most recent N traces involving
// `service`. Nodes are bucketed by `(parent_path, service.name,
// displayName)`; counts + average / max duration + error count are
// accumulated per bucket.
//
// Returns:
//   roots        — top-level nodes (multiple if sampled traces
//                  have different root spans; chronological order
//                  by avg start time).
//   totalSpans   — span count across the sampled traces (for the
//                  "X spans used" header).
//   sampledFrom  — trace count actually inspected.
func (s *Store) AggregateServiceStructure(
	ctx context.Context, service string, since time.Duration, sampleCount int,
) (roots []*AggSpanNode, totalSpans, sampledFrom int, err error) {
	if sampleCount <= 0 || sampleCount > 200 {
		sampleCount = 50
	}
	// Step 1: pick top-N traces by span count for this service.
	tr, err := s.conn.Query(ctx, `
		SELECT trace_id FROM spans
		WHERE service_name = ?
		  AND time >= now() - toIntervalSecond(?)
		GROUP BY trace_id
		ORDER BY count() DESC
		LIMIT ?
		SETTINGS max_execution_time = 30`,
		service, int64(since.Seconds()), sampleCount)
	if err != nil {
		return nil, 0, 0, err
	}
	var traceIDs []string
	for tr.Next() {
		var id string
		if err := tr.Scan(&id); err != nil {
			tr.Close()
			return nil, 0, 0, err
		}
		traceIDs = append(traceIDs, id)
	}
	tr.Close()
	if len(traceIDs) == 0 {
		return nil, 0, 0, nil
	}

	// Step 2: bulk fetch every span across those traces.
	spans, err := s.GetSpansForTraces(ctx, traceIDs)
	if err != nil {
		return nil, 0, 0, err
	}

	// Step 3: walk each trace in memory, aggregate path-keyed.
	type acc struct {
		count    int
		sumDur   int64
		maxDur   int64
		sumStart int64
		errCnt   int
		kind     string
		children map[string]*acc
	}
	rootMap := map[string]*acc{}

	// Bucket spans by trace_id so each tree walk is bounded.
	byTrace := map[string][]*SpanRow{}
	for i := range spans {
		byTrace[spans[i].TraceID] = append(byTrace[spans[i].TraceID], &spans[i])
	}

	for _, ts := range byTrace {
		byID := make(map[string]*SpanRow, len(ts))
		kids := make(map[string][]*SpanRow)
		for _, sp := range ts {
			byID[sp.SpanID] = sp
		}
		for _, sp := range ts {
			if sp.ParentSpanID != "" && byID[sp.ParentSpanID] != nil {
				kids[sp.ParentSpanID] = append(kids[sp.ParentSpanID], sp)
			}
		}
		// Entry points are the spans BELONGING to `service` whose
		// parent is either outside this trace, the absolute root, or
		// emitted by a different service. Anything else (an upstream
		// caller higher up in the trace) is intentionally excluded —
		// the structure view is "what does THIS service do", not
		// "the full trace topology that happens to involve it".
		var entryPoints []*SpanRow
		for _, sp := range ts {
			if sp.ServiceName != service {
				continue
			}
			parent := byID[sp.ParentSpanID]
			if parent == nil || parent.ServiceName != service {
				entryPoints = append(entryPoints, sp)
			}
		}
		// Sort each parent's children chronologically so the
		// representative subtree of grouped siblings reflects
		// real call order.
		for k := range kids {
			sort.Slice(kids[k], func(i, j int) bool {
				return kids[k][i].StartTime < kids[k][j].StartTime
			})
		}
		sort.Slice(entryPoints, func(i, j int) bool {
			return entryPoints[i].StartTime < entryPoints[j].StartTime
		})
		// Offset baseline = earliest entry point of this service in
		// the trace, so the leftmost bar starts at 0 instead of
		// reflecting how much upstream work happened before the
		// service was called.
		var minT int64 = math.MaxInt64
		for _, ep := range entryPoints {
			if ep.StartTime < minT {
				minT = ep.StartTime
			}
		}

		var dfs func(parent *acc, sp *SpanRow)
		dfs = func(parent *acc, sp *SpanRow) {
			key := sp.ServiceName + "\x01" + DisplaySpanName(sp)
			var node *acc
			if parent == nil {
				node = rootMap[key]
				if node == nil {
					node = &acc{children: map[string]*acc{}, kind: sp.Kind}
					rootMap[key] = node
				}
			} else {
				node = parent.children[key]
				if node == nil {
					node = &acc{children: map[string]*acc{}, kind: sp.Kind}
					parent.children[key] = node
				}
			}
			dur := sp.EndTime - sp.StartTime
			if dur < 0 {
				dur = 0
			}
			node.count++
			node.sumDur += dur
			if dur > node.maxDur {
				node.maxDur = dur
			}
			node.sumStart += sp.StartTime - minT
			if sp.StatusCode == "error" {
				node.errCnt++
			}
			for _, c := range kids[sp.SpanID] {
				dfs(node, c)
			}
		}
		for _, r := range entryPoints {
			dfs(nil, r)
		}
	}

	// Step 4: convert acc-map → JSON-friendly tree, sorted
	// chronologically among siblings by avgStart.
	var convert func(m map[string]*acc) []*AggSpanNode
	convert = func(m map[string]*acc) []*AggSpanNode {
		out := make([]*AggSpanNode, 0, len(m))
		for key, n := range m {
			parts := strings.SplitN(key, "\x01", 2)
			srv := parts[0]
			op := ""
			if len(parts) > 1 {
				op = parts[1]
			}
			avgMs := 0.0
			avgStartMs := 0.0
			if n.count > 0 {
				avgMs = float64(n.sumDur) / float64(n.count) / 1e6
				avgStartMs = float64(n.sumStart) / float64(n.count) / 1e6
			}
			out = append(out, &AggSpanNode{
				Service:    srv,
				Operation:  op,
				Kind:       n.kind,
				Count:      n.count,
				AvgMs:      avgMs,
				MaxMs:      float64(n.maxDur) / 1e6,
				ErrorCount: n.errCnt,
				AvgStartMs: avgStartMs,
				Children:   convert(n.children),
			})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].AvgStartMs < out[j].AvgStartMs })
		return out
	}
	roots = convert(rootMap)
	return roots, len(spans), len(traceIDs), nil
}
