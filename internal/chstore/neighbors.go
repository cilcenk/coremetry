package chstore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// NeighborStat is one neighbouring service in the service-level
// topology — the count of distinct sampled traces in which the edge
// (this neighbour ↔ inspected service) was observed, plus the raw
// number of crossing call edges.
type NeighborStat struct {
	Service    string `json:"service"`
	TraceCount int    `json:"traceCount"`
	SpanCount  int    `json:"spanCount"`
}

// ServiceNeighbors returns the service-level upstream / downstream
// neighbours of `service` derived purely from trace topology — no
// peer.service reliance. We sample the same recent N traces the
// structure view uses and walk parent-child edges in memory: an
// edge from a span S whose service != `service` to a child whose
// service == `service` makes S's service an upstream caller; the
// reverse pattern makes the child's service a downstream callee.
//
// Returns:
//
//	upstream     — services that called `service` (caller side of inbound edges)
//	downstream   — services `service` called (callee side of outbound edges)
//	sampledFrom  — number of traces actually inspected (≤ samples)
//	totalSpans   — span count across the sampled traces (header line)
func (s *Store) ServiceNeighbors(
	ctx context.Context, service string, since time.Duration, sampleCount int,
) (upstream, downstream []NeighborStat, sampledFrom, totalSpans int, err error) {
	if sampleCount <= 0 || sampleCount > 200 {
		sampleCount = 50
	}

	tr, err := s.conn.Query(ctx, `
		SELECT trace_id FROM spans
		WHERE service_name = ?
		  AND time >= now() - toIntervalSecond(?)
		GROUP BY trace_id
		ORDER BY count() DESC
		LIMIT ?
		SETTINGS max_execution_time = 30,
		         `+s.shardSkipSetting(),
		service, int64(since.Seconds()), sampleCount)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	var traceIDs []string
	for tr.Next() {
		var id string
		if err := tr.Scan(&id); err != nil {
			tr.Close()
			return nil, nil, 0, 0, err
		}
		traceIDs = append(traceIDs, id)
	}
	tr.Close()
	if len(traceIDs) == 0 {
		return nil, nil, 0, 0, nil
	}

	// Light-touch fetch: only the four columns the edge walk needs.
	// Skips the heavy attribute / event blobs the structure aggregator
	// pulls so opening this panel alone stays cheap.
	holders := make([]string, len(traceIDs))
	args := make([]any, len(traceIDs))
	for i, id := range traceIDs {
		holders[i] = "?"
		args[i] = id
	}
	rows, err := s.conn.Query(ctx, fmt.Sprintf(`
		SELECT trace_id, span_id, parent_id, service_name
		FROM spans
		WHERE trace_id IN (%s)
		SETTINGS max_execution_time = 30`, strings.Join(holders, ",")), args...)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	defer rows.Close()

	type edgeKey struct{ peer string }
	// Per-direction tally: distinct trace coverage in a separate set.
	upSpan := map[string]int{}
	upTraces := map[edgeKey]map[string]struct{}{}
	dnSpan := map[string]int{}
	dnTraces := map[edgeKey]map[string]struct{}{}

	type spanInfo struct{ parent, svc string }
	byTrace := map[string]map[string]spanInfo{} // trace_id → span_id → info
	totalSpans = 0
	for rows.Next() {
		var traceID, spanID, parentID, svc string
		if err := rows.Scan(&traceID, &spanID, &parentID, &svc); err != nil {
			return nil, nil, 0, 0, err
		}
		m, ok := byTrace[traceID]
		if !ok {
			m = map[string]spanInfo{}
			byTrace[traceID] = m
		}
		m[spanID] = spanInfo{parent: parentID, svc: svc}
		totalSpans++
	}
	if err := rows.Err(); err != nil {
		return nil, nil, 0, 0, err
	}

	for traceID, spans := range byTrace {
		for _, sp := range spans {
			parent, ok := spans[sp.parent]
			if !ok || parent.svc == sp.svc {
				continue
			}
			// Only edges that cross this service's boundary.
			if sp.svc == service {
				k := edgeKey{peer: parent.svc}
				upSpan[parent.svc]++
				if upTraces[k] == nil {
					upTraces[k] = map[string]struct{}{}
				}
				upTraces[k][traceID] = struct{}{}
			} else if parent.svc == service {
				k := edgeKey{peer: sp.svc}
				dnSpan[sp.svc]++
				if dnTraces[k] == nil {
					dnTraces[k] = map[string]struct{}{}
				}
				dnTraces[k][traceID] = struct{}{}
			}
		}
	}

	collect := func(spanCounts map[string]int, traceSets map[edgeKey]map[string]struct{}) []NeighborStat {
		out := make([]NeighborStat, 0, len(spanCounts))
		for svc, sc := range spanCounts {
			tc := len(traceSets[edgeKey{peer: svc}])
			out = append(out, NeighborStat{Service: svc, TraceCount: tc, SpanCount: sc})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].TraceCount != out[j].TraceCount {
				return out[i].TraceCount > out[j].TraceCount
			}
			if out[i].SpanCount != out[j].SpanCount {
				return out[i].SpanCount > out[j].SpanCount
			}
			return out[i].Service < out[j].Service
		})
		return out
	}

	upstream = collect(upSpan, upTraces)
	downstream = collect(dnSpan, dnTraces)
	return upstream, downstream, len(traceIDs), totalSpans, nil
}
