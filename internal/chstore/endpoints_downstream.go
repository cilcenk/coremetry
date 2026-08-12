package chstore

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
)

// "Where the time goes" for one endpoint (v0.9.311, brief N4).
//
// THE QUESTION IT ANSWERS. The /endpoints drawer could say a route's
// p99 is 900ms; it could not say where those 900ms went. The nearest
// existing surface, DependencyStrip, is SERVICE-level and carries no
// duration share — it says "this endpoint's service touches postgres,
// redis and payments-api", never "600 of this route's 900ms are in
// postgres".
//
// WHY A QUERY AND NOT AN MV. No materialized view carries
// route→downstream: topology_op_edges_5m is keyed on span NAME and
// holds only calls (no duration, no errors), service_callers_5m has no
// route dimension, and db_*_summary_5m is keyed on service_name.
// Adding a route dimension to the topology writer was costed and
// rejected — route × downstream cardinality on top of a 180s exec with
// a 4GB join. So this is a sampled read, and it says so on screen.
//
// THE SHAPE. Two bounded passes over raw spans, the neighbors.go
// pattern narrowed to a route:
//
//  1. sample ≤N route-scoped trace_ids using the PK prefix
//     (service_name, time) plus http_route;
//  2. ONE pass over those ids collecting the edges, from which the
//     downstream side falls out of child spans and the caller side out
//     of parent spans — two tabs, one sample.
//
// SAMPLING BIAS, STATED. neighbors.go orders by count() DESC, which
// leans toward structurally fat traces. That is the wrong lean for a
// latency question, so this samples by DURATION: the slow tail is
// exactly the population the operator opened the drawer about. It is
// still a sample, and the payload carries the count so the UI can say
// so — the Drain-templating honesty rule.

// endpointDownstreamSample bounds how many traces one answer walks.
// 200 keeps the second pass's IN list inside the parser budget the
// v0.8.363 catch established (~35 bytes per id) with room to spare,
// and a latency SHARE stabilises long before 200 samples.
const endpointDownstreamSample = 200

// EndpointEdge is one downstream dependency or one caller of a route.
type EndpointEdge struct {
	// Name — the downstream service / db_system / peer, or the calling
	// service. "self" is reserved for the entry span's own time.
	Name string `json:"name"`
	// Kind — "service" | "db" | "messaging" | "self", so the UI can
	// pick an icon without re-deriving it from the name.
	Kind  string `json:"kind"`
	Calls uint64 `json:"calls"`
	// AvgMs / P99Ms over the SAMPLED spans, not the window.
	AvgMs  float64 `json:"avgMs"`
	P99Ms  float64 `json:"p99Ms"`
	Errors uint64  `json:"errors"`
	// ShareMs — total milliseconds this edge accounts for across the
	// sample. The UI draws the share bar from it.
	ShareMs float64 `json:"shareMs"`
}

// EndpointDownstream is the payload behind the drawer's two tabs.
type EndpointDownstream struct {
	Downstream []EndpointEdge `json:"downstream"`
	Callers    []EndpointEdge `json:"callers"`
	// SampledFrom — how many traces the numbers above were derived
	// from. Zero means the window held no matching trace; the UI says
	// that rather than drawing an empty chart.
	SampledFrom int `json:"sampledFrom"`
	// TotalMs — the sampled entry spans' total duration, i.e. the
	// denominator of every share. Carried so the UI never re-derives it
	// from a rounded sum.
	TotalMs float64 `json:"totalMs"`
	// Backends (v0.9.311) — database / cache / broker time found at ANY
	// depth beneath the route, NOT just in its direct children.
	//
	// This exists because direct children alone answer the question too
	// shallowly. Verified on live data: a gateway route's 659ms had one
	// direct child (account-service, 645ms) whose OWN child was a 623ms
	// Oracle query. "645ms in account-service" is true and nearly
	// useless; "623ms of it in Oracle" is the answer the operator came
	// for — and it is the brief's own motivating example.
	//
	// Kept OUT of the Downstream share arithmetic on purpose: that
	// Oracle time is INSIDE account-service's 645ms, so listing it as a
	// sibling share would double-count the same milliseconds. Nested
	// breakdown, nested label.
	Backends []EndpointEdge `json:"backends"`
}

// EndpointWhereTheTimeGoes samples this route's traces and splits their
// time across downstream edges (plus the route's own self time), and
// collects who calls it.
func (s *Store) EndpointWhereTheTimeGoes(
	ctx context.Context, q EndpointDetailQuery,
) (*EndpointDownstream, error) {
	ids, entryTotalMs, entryIDs, err := s.sampleEndpointTraces(ctx, q)
	if err != nil {
		return nil, err
	}
	out := &EndpointDownstream{
		Downstream:  []EndpointEdge{},
		Callers:     []EndpointEdge{},
		Backends:    []EndpointEdge{},
		SampledFrom: len(ids),
		TotalMs:     entryTotalMs,
	}
	if len(ids) == 0 {
		return out, nil
	}
	return s.walkEndpointEdges(ctx, q, ids, entryIDs, out)
}

// sampleEndpointTraces picks the route's SLOWEST traces in the window.
//
// Ordering by duration rather than by span count (the neighbors.go
// choice) is deliberate: this panel exists to explain latency, so the
// sample must represent the tail the operator is looking at. Sampling
// the fattest traces instead would answer a structural question with a
// latency chart.
func (s *Store) sampleEndpointTraces(
	ctx context.Context, q EndpointDetailQuery,
) (traceIDs []string, totalMs float64, entrySpanIDs map[string]struct{}, err error) {
	wc := s.endpointDetailWhere(q)
	rows, qerr := s.conn.Query(ctx, `
		SELECT trace_id, span_id, duration / 1e6 AS dur_ms
		FROM spans `+wc.sql()+`
		ORDER BY duration DESC
		LIMIT ?
		SETTINGS max_execution_time = 10,
		         `+s.shardSkipSetting()+s.heavyScanSpill(),
		append(append([]any{}, wc.args...), endpointDownstreamSample)...)
	if qerr != nil {
		return nil, 0, nil, fmt.Errorf("endpoint downstream sample: %w", qerr)
	}
	defer rows.Close()
	entrySpanIDs = map[string]struct{}{}
	seen := map[string]struct{}{}
	for rows.Next() {
		var tid, sid string
		var dur float64
		if serr := rows.Scan(&tid, &sid, &dur); serr != nil {
			return nil, 0, nil, serr
		}
		entrySpanIDs[sid] = struct{}{}
		totalMs += dur
		if _, dup := seen[tid]; dup {
			// One trace can hit the same route twice (a retry, a loop).
			// Its entry spans both count toward the denominator, but the
			// id is fetched once.
			continue
		}
		seen[tid] = struct{}{}
		traceIDs = append(traceIDs, tid)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, 0, nil, fmt.Errorf("endpoint downstream sample: %w", rerr)
	}
	return traceIDs, totalMs, entrySpanIDs, nil
}

// walkEndpointEdges runs the SECOND bounded pass and folds it into the
// two tabs.
//
// The time predicate is mandatory, not cosmetic: v0.9.231 found that
// the same shape WITHOUT it ran bloom-filter analysis across every
// daily partition in retention. The window is padded by traceFetchPad
// so a trace that started just before the window still resolves whole.
func (s *Store) walkEndpointEdges(
	ctx context.Context, q EndpointDetailQuery,
	traceIDs []string, entrySpanIDs map[string]struct{},
	out *EndpointDownstream,
) (*EndpointDownstream, error) {
	holders := make([]string, len(traceIDs))
	args := make([]any, 0, len(traceIDs)+2)
	for i, id := range traceIDs {
		holders[i] = "?"
		args = append(args, id)
	}
	args = append(args, q.From.Add(-traceFetchPad), q.To.Add(traceFetchPad))

	rows, err := s.conn.Query(ctx, fmt.Sprintf(`
		SELECT span_id, parent_id, service_name, db_system, kind,
		       duration / 1e6 AS dur_ms,
		       status_code = 'error' AS is_err
		FROM spans
		WHERE trace_id IN (%s)
		  AND time >= ? AND time <= ?
		SETTINGS max_execution_time = 10,
		         `+s.shardSkipSetting()+s.heavyScanSpill(),
		strings.Join(holders, ",")), args...)
	if err != nil {
		return nil, fmt.Errorf("endpoint downstream walk: %w", err)
	}
	defer rows.Close()

	down := map[string]*edgeAcc{}
	callers := map[string]*edgeAcc{}
	backends := map[string]*edgeAcc{}
	var childMs float64

	add := func(m map[string]*edgeAcc, name, kind string, ms float64, isErr bool) {
		if name == "" {
			return
		}
		a := m[name]
		if a == nil {
			a = &edgeAcc{kind: kind}
			m[name] = a
		}
		a.calls++
		a.sumMs += ms
		a.durs = append(a.durs, ms)
		if isErr {
			a.errs++
		}
	}

	for rows.Next() {
		var sid, pid, svc, dbSys, kind string
		var durMs float64
		var isErr uint8
		if serr := rows.Scan(&sid, &pid, &svc, &dbSys, &kind, &durMs, &isErr); serr != nil {
			return nil, serr
		}
		if _, isEntry := entrySpanIDs[sid]; isEntry {
			continue // the route's own span is the denominator, not an edge
		}
		// Backend time at ANY depth. Collected before the direct-child
		// test so a DB call that IS a direct child appears in both
		// views — correctly: once as the share it occupies, once in the
		// backend breakdown. The two lists answer different questions
		// and are never summed together.
		if dbSys != "" {
			add(backends, dbSys, "db", durMs, isErr != 0)
		}
		if _, parentIsEntry := entrySpanIDs[pid]; parentIsEntry {
			// A DIRECT child of the route's span: this is where the
			// route's time actually went. Grandchildren are deliberately
			// excluded — counting them would double-count time already
			// inside a child's duration.
			name, k := svc, "service"
			switch {
			case dbSys != "":
				name, k = dbSys, "db"
			case kind == "producer" || kind == "consumer":
				k = "messaging"
			}
			add(down, name, k, durMs, isErr != 0)
			childMs += durMs
			continue
		}
		if _, childIsEntry := entrySpanIDs[sid]; !childIsEntry && pid == "" {
			// A root span in a sampled trace that is NOT our route: the
			// caller side. Recorded by service.
			add(callers, svc, "service", durMs, isErr != 0)
		}
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("endpoint downstream walk: %w", rerr)
	}

	// self = the route's own time minus what its children accounted
	// for. Parallel children overlap, so the subtraction can go
	// negative; clamp at zero and let the UI explain why it can.
	if self := out.TotalMs - childMs; self > 0 {
		down["self"] = &edgeAcc{kind: "self", calls: uint64(len(traceIDs)), sumMs: self}
	}

	out.Downstream = finishEdges(down)
	out.Callers = finishEdges(callers)
	out.Backends = finishEdges(backends)
	return out, nil
}

// edgeAcc accumulates one edge's sampled spans.
type edgeAcc struct {
	kind  string
	calls uint64
	errs  uint64
	sumMs float64
	durs  []float64
}

// finishEdges turns the accumulators into rows ordered by the share
// they account for — the answer to "where did the time go" reads top
// down. p99 is computed over the SAMPLE, so it is the sample's p99 and
// the payload's SampledFrom is what lets the UI say so.
func finishEdges(m map[string]*edgeAcc) []EndpointEdge {
	out := make([]EndpointEdge, 0, len(m))
	for name, a := range m {
		e := EndpointEdge{
			Name: name, Kind: a.kind, Calls: a.calls,
			Errors: a.errs, ShareMs: a.sumMs,
		}
		if a.calls > 0 {
			e.AvgMs = a.sumMs / float64(a.calls)
		}
		e.P99Ms = pctOf(a.durs, 0.99)
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ShareMs != out[j].ShareMs {
			return out[i].ShareMs > out[j].ShareMs
		}
		return out[i].Name < out[j].Name // stable for equal shares
	})
	return out
}

// pctOf is a nearest-rank percentile over a sample. Exact by
// construction (the sample is already in memory and small), so no
// tdigest and no interpolation to explain.
func pctOf(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	i := int(math.Ceil(p*float64(len(v)))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(v) {
		i = len(v) - 1
	}
	return v[i]
}
