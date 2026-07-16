package chstore

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// endpoints_detail.go — v0.8.360 (Stage-2 slice E2, docs/pages-enhancement-
// audit.md §1): route-scoped readers behind the /endpoints detail drawer.
// One (service, path) tuple, one bounded window, five independent sections:
//
//   • EndpointLatencyHistogram — the GetLatencyHeatmap core with an
//     endpoint predicate (service + route + inbound-kind), collapsed to a
//     1-D latency distribution by CollapseLatencyHistogram.
//   • EndpointStatusBreakdown  — per-status-CODE counts + the v0.8.356
//     sidecar's class rollup, for ONE endpoint.
//   • EndpointTopExceptions    — exception groups observed ON this route's
//     spans (see the scoping note on the func).
//   • EndpointFailingTraces    — top-N error spans on the route, grouped to
//     distinct traces, worst duration first (the direct trace pivot).
//   • EndpointExemplars        — slow + error exemplar trace_ids off the
//     spanmetrics_1m argMax states, route-scoped (the MV carries http_route
//     as a dimension — FindExemplarRollup can't scope below service).
//
// Every raw read here is bounded the hard-constraint way: WHERE rides the
// (service_name, time) PK prefix + LIMIT + max_execution_time. Signature
// mode ("group by shape", v0.8.x) is honoured everywhere via
// endpointRoutePred so a collapsed row (/orders/:id) drills into the same
// population the table aggregated.

// EndpointDetailQuery scopes every reader in this file to one
// (service, path) tuple over a window. BySignature marks Path as an
// ID-collapsed shape (/orders/:id) — readers then match
// opSigWrap(http_route) instead of the raw column.
type EndpointDetailQuery struct {
	Service     string
	Path        string
	BySignature bool
	From, To    time.Time
}

// endpointRoutePred appends the route-scoping predicate for q.Path.
// Raw mode is a plain http_route equality (LowCardinality column,
// dictionary-fast). Signature mode compares the same ID-collapsing
// rewrite the /endpoints table groups by (opSigWrap), with the regex
// patterns bound as args (the v0.8.356 clickhouse-go server-side-
// param trap — braces in SQL text flip the driver into `{name:type}`
// mode, so the patterns must never be inlined).
func endpointRoutePred(wc *whereClause, path string, bySig bool) {
	if bySig {
		wc.add(opSigWrap("http_route")+" = ?", append(opSigArgs(), path)...)
		return
	}
	wc.add("http_route = ?", path)
}

// endpointDetailWhere builds the shared scope every raw-spans reader
// in this file starts from: time window on the PK, service on the PK
// prefix, inbound kinds only (client/producer spans count under the
// callee — the v0.5.386 posture the table uses), route match.
func endpointDetailWhere(q EndpointDetailQuery) whereClause {
	wc := whereClause{}
	wc.add("time >= ?", q.From)
	wc.add("time <= ?", q.To)
	wc.add("service_name = ?", q.Service)
	wc.add("kind NOT IN ('client', 'producer')")
	endpointRoutePred(&wc, q.Path, q.BySignature)
	return wc
}

// EndpointLatencyHistogram runs the shared latency-heatmap core
// (heatmap.go — same log10 bin grid, same >1h trace-ID sampling with
// Go-side scale-back) restricted to one endpoint. The caller collapses
// the 2-D result to a 1-D distribution via CollapseLatencyHistogram —
// the drawer wants "what is THIS endpoint's latency shape", not
// "when"; the time dimension is already covered by the row sparkline.
func (s *Store) EndpointLatencyHistogram(ctx context.Context, q EndpointDetailQuery) (*LatencyHeatmap, error) {
	sampleN := heatmapSampleN(q.From, q.To)
	wc := whereClause{}
	wc.add("time >= ?", q.From)
	wc.add("time <= ?", q.To)
	if sampleN > 1 {
		wc.add(fmt.Sprintf("cityHash64(trace_id) %% %d = 0", sampleN))
	}
	wc.add("service_name = ?", q.Service)
	wc.add("kind NOT IN ('client', 'producer')")
	endpointRoutePred(&wc, q.Path, q.BySignature)
	return s.latencyHeatmapWhere(ctx, wc, q.From, q.To, 60, sampleN)
}

// CollapseLatencyHistogram sums a 2-D (time × duration) heatmap's
// counts across the time axis into a 1-D latency distribution over
// the same log-scale duration bins. Pure — table-tested
// (endpoints_detail_test.go, v0.8.360). Bin j's count is Σ over all
// time buckets of Counts[i][j]; bins keep the heatmap's upper-bound-
// in-ms labelling so the drawer's axis matches the heatmap's Y axis
// exactly. Sampling extrapolation already happened in the heatmap
// read (counts arrive pre-multiplied), so this is a plain sum.
func CollapseLatencyHistogram(hm *LatencyHeatmap) (bins []float64, counts []uint64) {
	if hm == nil || len(hm.DurationBins) == 0 {
		return nil, nil
	}
	counts = make([]uint64, len(hm.DurationBins))
	for _, col := range hm.Counts {
		for j, c := range col {
			if j < len(counts) {
				counts[j] += uint64(c)
			}
		}
	}
	return hm.DurationBins, counts
}

// EndpointStatus is the drawer's error-breakdown-by-status payload:
// the v0.8.356 sidecar's four class counts plus the per-code map the
// table row never had room for ("is the 5xx one 500 or a 503 storm").
type EndpointStatus struct {
	Http2xx uint64            `json:"http2xx"`
	Http3xx uint64            `json:"http3xx"`
	Http4xx uint64            `json:"http4xx"`
	Http5xx uint64            `json:"http5xx"`
	Codes   map[string]uint64 `json:"codes"`
}

// EndpointStatusBreakdown groups one endpoint's spans by the dedicated
// http_status UInt16 column (minmax-indexed — same source the v0.8.356
// sidecar reads) and rolls the classes up in Go. Zero statuses (non-
// HTTP spans / SDKs that never set http.status_code) are excluded so
// the map doesn't lead with a meaningless "0".
func (s *Store) EndpointStatusBreakdown(ctx context.Context, q EndpointDetailQuery) (*EndpointStatus, error) {
	wc := endpointDetailWhere(q)
	wc.add("http_status > 0")
	args := append([]any{}, wc.args...)
	args = append(args, 50)
	rows, err := s.conn.Query(ctx, `
		SELECT http_status, count() AS cnt
		FROM spans
		`+wc.sql()+`
		GROUP BY http_status
		ORDER BY cnt DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &EndpointStatus{Codes: map[string]uint64{}}
	for rows.Next() {
		var code uint16
		var cnt uint64
		if err := rows.Scan(&code, &cnt); err != nil {
			return nil, err
		}
		out.Codes[fmt.Sprintf("%d", code)] = cnt
		switch {
		case code >= 200 && code <= 299:
			out.Http2xx += cnt
		case code >= 300 && code <= 399:
			out.Http3xx += cnt
		case code >= 400 && code <= 499:
			out.Http4xx += cnt
		case code >= 500 && code <= 599:
			out.Http5xx += cnt
		}
	}
	return out, rows.Err()
}

// EndpointException is one exception type observed on the endpoint's
// spans in the window, with the Go-side inbox fingerprint so the
// drawer deep-links straight into /problems?exception=<fp>.
type EndpointException struct {
	Type        string `json:"type"`
	Message     string `json:"message"`
	Fingerprint string `json:"fingerprint"`
	Count       uint64 `json:"count"`
	LastSeenNs  int64  `json:"lastSeenNs"`
}

// EndpointTopExceptions returns the top exception types on this
// endpoint, by occurrence count in the window.
//
// SCOPING (the honest part, documented per the E2 audit note): the
// exception_groups inbox has NO route dimension, so we count raw
// exception events instead, on spans of the service that either
// carry the route (http_route match, signature-collapsed when the
// drawer is in shape mode) OR whose span NAME equals the path — the
// common SDK conventions ("GET /orders/{id}" spans carry http_route;
// some SDKs name the server span exactly the route). LIMITATION: an
// exception recorded on a child internal span with neither the route
// attr nor a route-shaped name is attributed to the service, not
// this endpoint, and won't appear here — the /problems inbox remains
// the service-complete view. Fingerprint per row is the SAME
// FingerprintException(type, msg, service, stack) the inbox refresher
// computes (over the argMax-latest sample), so the deep link lands on
// the matching group whenever that sample is representative.
func (s *Store) EndpointTopExceptions(ctx context.Context, q EndpointDetailQuery, limit int) ([]EndpointException, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	// Route-OR-name scope: build the route predicate standalone so it
	// can sit inside the OR (endpointDetailWhere would AND it).
	var routeWC whereClause
	endpointRoutePred(&routeWC, q.Path, q.BySignature)
	wc := whereClause{}
	wc.add("time >= ?", q.From)
	wc.add("time <= ?", q.To)
	wc.add("service_name = ?", q.Service)
	wc.add(exMatchPred)
	wc.add("("+routeWC.conds[0]+" OR name = ?)", append(append([]any{}, routeWC.args...), q.Path)...)
	args := append([]any{}, wc.args...)
	args = append(args, limit)
	// max(time) keeps the column's DateTime64 type so
	// toUnixTimestamp64Nano is safe on the external Distributed schema
	// (the v0.8.312 trap only bites toStartOfInterval's DateTime).
	rows, err := s.conn.Query(ctx, `
		SELECT
		  `+exTypeExpr+` AS ex_type,
		  argMax(`+exMsgExpr+`, time) AS ex_msg,
		  argMax(`+exStackExpr+`, time) AS ex_stack,
		  count() AS cnt,
		  toUnixTimestamp64Nano(max(time)) AS last_seen
		FROM spans
		`+wc.sql()+`
		GROUP BY ex_type
		ORDER BY cnt DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EndpointException{}
	for rows.Next() {
		var e EndpointException
		var stack string
		if err := rows.Scan(&e.Type, &e.Message, &stack, &e.Count, &e.LastSeenNs); err != nil {
			return nil, err
		}
		e.Fingerprint = FingerprintException(e.Type, e.Message, q.Service, stack)
		out = append(out, e)
	}
	return out, rows.Err()
}

// EndpointFailingTrace is one distinct trace with at least one error
// span on the endpoint — the drawer's direct pivot into /trace?id=.
// DurationMs is the WORST endpoint-span duration inside the trace
// (the right ranking for an endpoint drawer — whole-trace duration
// would rank by unrelated downstream work).
type EndpointFailingTrace struct {
	TraceID    string  `json:"traceId"`
	DurationMs float64 `json:"durationMs"`
	SpanName   string  `json:"spanName"`
	StatusMsg  string  `json:"statusMsg,omitempty"`
	HttpStatus uint16  `json:"httpStatus,omitempty"`
	ErrorSpans uint64  `json:"errorSpans"`
	TimeNs     int64   `json:"timeNs"`
}

// EndpointFailingTraces returns the top `limit` failing traces on the
// endpoint, worst span duration first. Deliberately a bounded raw
// GROUP BY trace_id rather than GetTraces: the trace-list machinery
// (root resolution, count modes, MV gate) buys nothing here, and this
// scan rides the (service_name, time) PK + route + status filters, so
// the grouped set is tiny by construction.
func (s *Store) EndpointFailingTraces(ctx context.Context, q EndpointDetailQuery, limit int) ([]EndpointFailingTrace, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	wc := endpointDetailWhere(q)
	wc.add("status_code = 'error'")
	args := append([]any{}, wc.args...)
	args = append(args, limit)
	rows, err := s.conn.Query(ctx, `
		SELECT trace_id,
		       max(duration) / 1e6                  AS dur_ms,
		       argMax(name, duration)               AS span_name,
		       argMax(status_msg, duration)         AS status_msg,
		       argMax(http_status, duration)        AS http_status,
		       count()                              AS err_spans,
		       toUnixTimestamp64Nano(min(time))     AS t_ns
		FROM spans
		`+wc.sql()+`
		GROUP BY trace_id
		ORDER BY dur_ms DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EndpointFailingTrace{}
	for rows.Next() {
		var t EndpointFailingTrace
		if err := rows.Scan(&t.TraceID, &t.DurationMs, &t.SpanName,
			&t.StatusMsg, &t.HttpStatus, &t.ErrorSpans, &t.TimeNs); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// endpointExemplarArgs builds the path projection + bind args for the
// exemplar read. Split out of EndpointExemplars so the window alignment
// and the arg ORDER (the class that bit exemplar ingest in v0.8.435) are
// pinnable by a pure test.
//
// v0.8.535 — From is aligned DOWN to the spanmetrics_1m grain. The MV
// keys on time_bucket (the minute FLOOR), so `time_bucket >= q.From`
// with a sub-minute From drops the entire bucket the window starts
// inside — the slowest trace of that first partial minute becomes
// invisible and the drawer's "slowest →" link silently resolves to the
// runner-up. parseFromTo (api.go:2526) hands us wall-clock times, never
// snapped: the minute bucket lives only in the CACHE KEY
// (endpoints_detail.go:63), so every real request carries a sub-minute
// From. Measured on the live 2-shard cluster (to=now, 30 probes/window):
// a ~5-minute window returned the wrong trace_id 7/30 times before,
// 1/30 after; 15m and 1h windows were already 0/30 — the loss scales
// inversely with window width. Sibling: endpoints.go:343.
//
// To stays RAW on purpose. Ceiling it would admit a bucket lying wholly
// past the window; flooring it changes nothing (the bucket at the floor
// is still <= To). With to=now the trailing bucket cannot hold data past
// the window anyway, so the residual over-inclusion only exists for
// windows pinned to a historical To — and there it is bounded by the
// 1-minute grain, the same trade endpoints.go already accepts.
func endpointExemplarArgs(q EndpointDetailQuery) (pathProj string, args []any) {
	pathProj = "http_route"
	args = append(args, q.From.Truncate(time.Minute), q.To, q.Service)
	if q.BySignature {
		pathProj = opSigWrap("http_route")
		args = append(args, opSigArgs()...)
	}
	args = append(args, q.Path)
	return pathProj, args
}

// EndpointExemplars resolves the slow + error exemplar trace_ids for
// one endpoint off the spanmetrics_1m argMax states — MV-first and
// endpoint-precise: the MV carries http_route as a dimension, which
// FindExemplarRollup (service+window keyed) cannot scope to. One MV
// read yields both (argMaxMerge / argMaxIfMerge — the combinator
// contract from the v0.8.51 catch). Reads via spanmetricsSourceFor()
// so chstore-owned clusters fan out across shards (the v0.8.356 per-
// shard-MV posture). Empty strings mean "no exemplar in window"
// (pre-cutover / TTL'd / all-healthy for the error state) — soft,
// the caller renders the section without links.
func (s *Store) EndpointExemplars(ctx context.Context, q EndpointDetailQuery) (slowTraceID, errorTraceID string, err error) {
	pathProj, args := endpointExemplarArgs(q)
	row := s.conn.QueryRow(ctx, `
		SELECT argMaxMerge(slow_exemplar_state)    AS slow_tid,
		       argMaxIfMerge(error_exemplar_state) AS err_tid
		FROM `+s.spanmetricsSourceFor("spanmetrics_1m")+`
		WHERE time_bucket >= ? AND time_bucket <= ?
		  AND kind NOT IN ('client', 'producer')
		  AND service_name = ?
		  AND `+pathProj+` = ?
		SETTINGS max_execution_time = 10`, args...)
	if scanErr := row.Scan(&slowTraceID, &errorTraceID); scanErr != nil {
		// Empty rollup surfaces as a scan error — the FindExemplarRollup
		// "clean not found" posture. Callers get ("", "", nil).
		return "", "", nil
	}
	return slowTraceID, errorTraceID, nil
}

// EndpointSplitRow is one attribute value's RED rollup inside the
// drawer's split-by section.
type EndpointSplitRow struct {
	Value     string  `json:"value"`
	Calls     uint64  `json:"calls"`
	Errors    uint64  `json:"errors"`
	ErrorRate float64 `json:"errorRate"`
	AvgMs     float64 `json:"avgMs"`
	P99Ms     float64 `json:"p99Ms"`
}

// endpointSplitDims is the split-by WHITELIST: UI ids → CH column
// expressions. Free-form identifiers NEVER reach the SQL text — the
// reader errors on anything outside this map. Entries follow the
// wellKnownFacets catalogue (facets.go) minus the two identity
// dimensions the drawer is already scoped by (service.name /
// http.route), plus the semconv resource keys operators split
// incidents by (pod, version). http.status_code maps zero (unset) to
// '' so the shared blank-value filter drops non-HTTP spans.
var endpointSplitDims = map[string]string{
	"deployment.environment": "deploy_env",
	// Current semconv spelling (≥1.27) — same typed column (v0.8.379).
	"deployment.environment.name": "deploy_env",
	"host.name":              "host_name",
	"http.method":            "http_method",
	"http.status_code":       "if(http_status = 0, '', toString(http_status))",
	"status_code":            "status_code",
	"span.kind":              "kind",
	"peer.service":           "peer_service",
	"k8s.pod.name":           "res_values[indexOf(res_keys, 'k8s.pod.name')]",
	"service.version":        "res_values[indexOf(res_keys, 'service.version')]",
}

// EndpointSplitDims returns the whitelisted split-by ids, sorted —
// for the handler's 400 message and to keep the frontend's select in
// lockstep (pinned by endpoints_detail_test.go).
func EndpointSplitDims() []string {
	out := make([]string, 0, len(endpointSplitDims))
	for k := range endpointSplitDims {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// EndpointSplit groups the endpoint's spans by one whitelisted
// attribute and returns the top `limit` values by call volume with
// RED each. Bounded raw GROUP BY: the scope predicate rides the
// (service_name, time) PK + route filter, quantileTDigest keeps the
// per-group quantile memory flat, LIMIT + max_execution_time cap the
// rest. `by` outside the whitelist is a caller bug → error (handler
// 400s).
func (s *Store) EndpointSplit(ctx context.Context, q EndpointDetailQuery, by string, limit int) ([]EndpointSplitRow, error) {
	expr, ok := endpointSplitDims[by]
	if !ok {
		return nil, fmt.Errorf("unknown split dimension %q", by)
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	wc := endpointDetailWhere(q)
	wc.add(expr + " != ''")
	if by == "status_code" {
		// OTel's default for never-set span status — noise, same
		// exclusion the facets panel applies (havingExpr).
		wc.add(expr + " != 'unset'")
	}
	args := append([]any{}, wc.args...)
	args = append(args, limit)
	rows, err := s.conn.Query(ctx, `
		SELECT `+expr+`                                   AS v,
		       count()                                    AS calls,
		       countIf(status_code = 'error')             AS errors,
		       sum(duration) / nullIf(count(), 0) / 1e6   AS avg_ms,
		       quantileTDigest(0.99)(duration) / 1e6      AS p99_ms
		FROM spans
		`+wc.sql()+`
		GROUP BY v
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EndpointSplitRow{}
	for rows.Next() {
		var r EndpointSplitRow
		var avgMs, p99Ms *float64
		if err := rows.Scan(&r.Value, &r.Calls, &r.Errors, &avgMs, &p99Ms); err != nil {
			return nil, err
		}
		r.AvgMs = safeF(avgMs)
		r.P99Ms = safeF(p99Ms)
		if r.Calls > 0 {
			r.ErrorRate = float64(r.Errors) * 100.0 / float64(r.Calls)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
