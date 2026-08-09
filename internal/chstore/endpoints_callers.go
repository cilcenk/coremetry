package chstore

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// "Who calls this endpoint" (v0.9.839) — the caller breakdown behind
// the /endpoint detail page's panel of the same name.
//
// THE QUESTION IT ANSWERS. /databases has had a caller table for
// versions: open a database, see which services drive its calls,
// errors and latency, ranked by impact. /endpoints had no equivalent,
// so the operator could see that a route was slow and not who was
// paying for it. The operator asked for the DB table's exact shape on
// the endpoint page; this is its reader.
//
// WHY NOT AN MV. Every candidate was checked and none carries the
// pair (route, caller):
//   • service_callers_5m keys on the RECEIVING SERVICE only — it can
//     say "who calls payment-api", never "who calls POST /charge".
//   • topology_op_edges_5m keys parent_op / child_op on the span NAME.
//     The endpoint identity on this page is http.route, and span name
//     is not it — an SDK that names the server span "HTTP POST" would
//     silently fold every route of the service into one row. That is
//     precisely the identity trap the /endpoint page exists to respect.
//   • db_caller_summary_5m is db-side and keyed on db_system.
// So this is a bounded raw-spans read, and — like
// EndpointWhereTheTimeGoes — it states its own sampling on screen.
//
// THE SHAPE. Two bounded passes, both PK-pruned:
//
//  1. sample the route's ENTRY spans through the shared
//     endpointDetailWhere scope (service_name + time PK prefix, route
//     predicate, inbound kinds, env/cluster). Ordering is
//     cityHash64(span_id) — a DETERMINISTIC, UNBIASED sample.
//     EndpointWhereTheTimeGoes orders by duration on purpose because
//     it explains the tail; doing that here would be a bug: the
//     caller with the slowest traces would look like the caller with
//     the most traffic. Ordering by time would answer "who called in
//     the last N seconds" instead of "who calls this route".
//  2. resolve those spans' parent_id → caller service. Parent lookup
//     rides the trace_id bloom index (idx_trace) plus a span_id list,
//     with the mandatory time predicate padded by traceFetchPad so a
//     parent that started just before the window still resolves.
//
// HONESTY. Callers whose parent span is not in the store (uninstru-
// mented, sampled away, aged out) land in Unresolved; entry spans
// with no parent at all land in DirectEntries. Neither is quietly
// folded into a caller's share — the panel renders both counts,
// because "we could not see the caller" and "there was no caller" are
// different answers and both differ from "this service called you".

// endpointCallersSample bounds pass 1. 800 keeps BOTH id lists of
// pass 2 inside the query-text budget the v0.8.363 catch established
// (~35 bytes per trace id + ~19 per span id ⇒ ~43KB, comfortably
// under CH's 256KiB max_query_size) and a caller SHARE stabilises
// long before 800 samples.
const endpointCallersSample = 800

// EndpointCaller is one calling service of a route, in the same
// columns the /databases caller table uses: calls, error %, p95,
// impact share.
type EndpointCaller struct {
	Service string `json:"service"`
	Calls   uint64 `json:"calls"`
	Errors  uint64 `json:"errors"`
	// ErrorRate — percent of this caller's sampled calls that errored.
	ErrorRate float64 `json:"errorRate"`
	// P95Ms — nearest-rank p95 of THIS ROUTE's duration when called by
	// this service, over the sample. Exact for the sample, which is
	// what SampledSpans lets the UI say.
	P95Ms float64 `json:"p95Ms"`
	// ShareMs / SharePct — total sampled milliseconds this caller
	// accounts for, and its share of the sample's total. The impact
	// column: a low-volume caller that only ever hits the slow path
	// outranks a chatty one that doesn't.
	ShareMs  float64 `json:"shareMs"`
	SharePct float64 `json:"sharePct"`
}

// EndpointCallers is the panel payload.
type EndpointCallers struct {
	Callers []EndpointCaller `json:"callers"`
	// SampledSpans — entry spans behind every number here.
	SampledSpans int `json:"sampledSpans"`
	// Sampled — true when pass 1 hit the cap, i.e. the window held
	// MORE entry spans than were read. False means the numbers cover
	// every matching span in the window, and the UI must not slap a
	// "sampled" label on a complete answer.
	Sampled bool `json:"sampled"`
	// DirectEntries — sampled entry spans with no parent span at all:
	// the route was entered from outside the traced system.
	DirectEntries uint64 `json:"directEntries"`
	// Unresolved — sampled entry spans whose parent id did not resolve
	// to a span in the window.
	Unresolved uint64 `json:"unresolved"`
	// TotalMs — the sample's total entry duration, the share denominator.
	TotalMs float64 `json:"totalMs"`
}

// endpointCallerSample is one sampled entry span of the route.
type endpointCallerSample struct {
	ParentID string
	DurMs    float64
	IsErr    bool
}

// EndpointCallers returns who called this route in the window.
func (s *Store) EndpointCallers(
	ctx context.Context, q EndpointDetailQuery, limit int,
) (*EndpointCallers, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	samples, traceIDs, err := s.sampleEndpointEntrySpans(ctx, q)
	if err != nil {
		return nil, err
	}
	out := &EndpointCallers{
		Callers:      []EndpointCaller{},
		SampledSpans: len(samples),
		Sampled:      len(samples) >= endpointCallersSample,
	}
	if len(samples) == 0 {
		return out, nil
	}
	parentSvc, err := s.resolveEndpointParents(ctx, q, samples, traceIDs)
	if err != nil {
		return nil, err
	}
	return foldEndpointCallers(samples, parentSvc, limit, out), nil
}

// sampleEndpointEntrySpans runs pass 1: an unbiased, bounded sample of
// the route's entry spans with the ids needed to find their parents.
func (s *Store) sampleEndpointEntrySpans(
	ctx context.Context, q EndpointDetailQuery,
) ([]endpointCallerSample, []string, error) {
	wc := s.endpointDetailWhere(q)
	args := append([]any{}, wc.args...)
	args = append(args, endpointCallersSample)
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT trace_id, span_id, parent_id,
		       duration / 1e6 AS dur_ms,
		       status_code = 'error' AS is_err
		FROM spans
		`+wc.sql()+`
		ORDER BY cityHash64(span_id)
		LIMIT ?
		SETTINGS max_execution_time = 10,
		         `+s.shardSkipSetting()+heavyScanSpill, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("endpoint callers sample: %w", err)
	}
	defer rows.Close()
	var (
		samples  []endpointCallerSample
		traceIDs []string
		seenTID  = map[string]struct{}{}
	)
	for rows.Next() {
		var tid, sid, pid string
		var dur float64
		var isErr uint8
		if serr := rows.Scan(&tid, &sid, &pid, &dur, &isErr); serr != nil {
			return nil, nil, serr
		}
		samples = append(samples, endpointCallerSample{
			ParentID: pid, DurMs: dur, IsErr: isErr != 0,
		})
		if pid == "" || pid == emptySpanID {
			continue // no parent to look up
		}
		if _, dup := seenTID[tid]; dup {
			continue
		}
		seenTID[tid] = struct{}{}
		traceIDs = append(traceIDs, tid)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, nil, fmt.Errorf("endpoint callers sample: %w", rerr)
	}
	return samples, traceIDs, nil
}

// emptySpanID is the all-zero span id some SDKs emit for "no parent"
// instead of an empty string (the same pair topology_root_flows_5m's
// root predicate tests).
const emptySpanID = "0000000000000000"

// resolveEndpointParents runs pass 2: parent span id → its service.
//
// The time predicate is mandatory — v0.9.231 found the same shape
// without one running bloom analysis across every partition in
// retention. trace_id narrows via idx_trace; span_id then filters
// the granules down to the ≤800 rows actually wanted.
func (s *Store) resolveEndpointParents(
	ctx context.Context, q EndpointDetailQuery,
	samples []endpointCallerSample, traceIDs []string,
) (map[string]string, error) {
	parentIDs := make([]string, 0, len(samples))
	seen := map[string]struct{}{}
	for _, sm := range samples {
		if sm.ParentID == "" || sm.ParentID == emptySpanID {
			continue
		}
		if _, dup := seen[sm.ParentID]; dup {
			continue
		}
		seen[sm.ParentID] = struct{}{}
		parentIDs = append(parentIDs, sm.ParentID)
	}
	if len(parentIDs) == 0 || len(traceIDs) == 0 {
		return map[string]string{}, nil
	}
	args := make([]any, 0, len(traceIDs)+len(parentIDs)+3)
	tHolders := make([]string, len(traceIDs))
	for i, id := range traceIDs {
		tHolders[i] = "?"
		args = append(args, id)
	}
	pHolders := make([]string, len(parentIDs))
	for i, id := range parentIDs {
		pHolders[i] = "?"
		args = append(args, id)
	}
	args = append(args, q.From.Add(-traceFetchPad), q.To.Add(traceFetchPad),
		len(parentIDs))

	rows, err := s.telemetryReadConn().Query(ctx, fmt.Sprintf(`
		SELECT span_id, service_name
		FROM spans
		WHERE trace_id IN (%s)
		  AND span_id IN (%s)
		  AND time >= ? AND time <= ?
		LIMIT ?
		SETTINGS max_execution_time = 10,
		         `+s.shardSkipSetting()+heavyScanSpill,
		strings.Join(tHolders, ","), strings.Join(pHolders, ",")), args...)
	if err != nil {
		return nil, fmt.Errorf("endpoint callers parent resolve: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string, len(parentIDs))
	for rows.Next() {
		var sid, svc string
		if serr := rows.Scan(&sid, &svc); serr != nil {
			return nil, serr
		}
		if svc != "" {
			out[sid] = svc
		}
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("endpoint callers parent resolve: %w", rerr)
	}
	return out, nil
}

// foldEndpointCallers turns the sample + parent map into ranked rows.
// PURE — table-tested (endpoints_callers_test.go, v0.9.839).
//
// Ranking is by ShareMs, not by calls: the panel's job is "who is this
// route's time being spent on behalf of". A caller with a tenth of the
// traffic but ten times the latency is the one worth seeing first, and
// sorting by call count would bury it.
func foldEndpointCallers(
	samples []endpointCallerSample, parentSvc map[string]string,
	limit int, out *EndpointCallers,
) *EndpointCallers {
	type acc struct {
		calls, errs uint64
		sumMs       float64
		durs        []float64
	}
	byService := map[string]*acc{}
	for _, sm := range samples {
		out.TotalMs += sm.DurMs
		if sm.ParentID == "" || sm.ParentID == emptySpanID {
			out.DirectEntries++
			continue
		}
		svc := parentSvc[sm.ParentID]
		if svc == "" {
			out.Unresolved++
			continue
		}
		a := byService[svc]
		if a == nil {
			a = &acc{}
			byService[svc] = a
		}
		a.calls++
		a.sumMs += sm.DurMs
		a.durs = append(a.durs, sm.DurMs)
		if sm.IsErr {
			a.errs++
		}
	}
	rows := make([]EndpointCaller, 0, len(byService))
	for svc, a := range byService {
		c := EndpointCaller{
			Service: svc, Calls: a.calls, Errors: a.errs,
			ShareMs: a.sumMs, P95Ms: pctOf(a.durs, 0.95),
		}
		if a.calls > 0 {
			c.ErrorRate = float64(a.errs) / float64(a.calls) * 100
		}
		if out.TotalMs > 0 {
			c.SharePct = a.sumMs / out.TotalMs * 100
		}
		rows = append(rows, c)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ShareMs != rows[j].ShareMs {
			return rows[i].ShareMs > rows[j].ShareMs
		}
		return rows[i].Service < rows[j].Service // stable for ties
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out.Callers = rows
	return out
}
