package chstore

// trace_edges.go — v0.10.439 (CoSRE router boşlukları D4): örneklenen
// trace'lerin servis→servis kenar sayımı (fan-out sorusu: "A→B gidenlerin
// hepsi C'ye gidiyor mu, istek başına ortalama kaç C çağrısı"). neighbors.go
// deseni: trace_id IN (≤200) + zaman sınırı (traceFetchPad), dört kolon,
// yürüyüş Go'da. Örnek tabanlı — çağıran bunu ilan eder.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TraceEdge — parent servisi → child servisi (aynı trace içinde).
type TraceEdge struct {
	Parent, Child string
}

const traceEdgesMaxIDs = 200

type traceEdgeRow struct {
	TraceID, SpanID, ParentID, Service string
}

// foldTraceEdges — SAF: satırlar → trace başına kenar sayımı. Parent'ı
// olmayan ya da aynı servisteki span kenar üretmez.
func foldTraceEdges(rows []traceEdgeRow) map[string]map[TraceEdge]int {
	type info struct{ parent, svc string }
	byTrace := map[string]map[string]info{}
	for _, r := range rows {
		m, ok := byTrace[r.TraceID]
		if !ok {
			m = map[string]info{}
			byTrace[r.TraceID] = m
		}
		m[r.SpanID] = info{r.ParentID, r.Service}
	}
	out := map[string]map[TraceEdge]int{}
	for tid, spans := range byTrace {
		edges := map[TraceEdge]int{}
		for _, sp := range spans {
			if sp.parent == "" {
				continue
			}
			p, ok := spans[sp.parent]
			if !ok || p.svc == "" || sp.svc == "" || p.svc == sp.svc {
				continue
			}
			edges[TraceEdge{p.svc, sp.svc}]++
		}
		out[tid] = edges
	}
	return out
}

// SpanEdgesForTraces — verilen trace'lerin kenar sayımları; ids ≤ 200,
// zaman sınırı [from-pad, to+pad], LIMIT + max_execution_time.
func (s *Store) SpanEdgesForTraces(ctx context.Context, ids []string, from, to time.Time) (map[string]map[TraceEdge]int, error) {
	if len(ids) == 0 {
		return map[string]map[TraceEdge]int{}, nil
	}
	if len(ids) > traceEdgesMaxIDs {
		ids = ids[:traceEdgesMaxIDs]
	}
	holders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+2)
	for i, id := range ids {
		holders[i] = "?"
		args = append(args, id)
	}
	args = append(args, from.Add(-traceFetchPad), to.Add(traceFetchPad))
	rows, err := s.conn.Query(ctx, fmt.Sprintf(`
		SELECT trace_id, span_id, parent_id, service_name
		FROM spans
		WHERE trace_id IN (%s)
		  AND time >= ? AND time <= ?
		LIMIT 400000
		SETTINGS max_execution_time = 25`, strings.Join(holders, ",")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []traceEdgeRow
	for rows.Next() {
		var r traceEdgeRow
		if err := rows.Scan(&r.TraceID, &r.SpanID, &r.ParentID, &r.Service); err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return foldTraceEdges(all), nil
}
