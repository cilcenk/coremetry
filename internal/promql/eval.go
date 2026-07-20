package promql

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// nsToTime converts unix nanoseconds to time.Time; 0 → zero time (the chstore
// filter treats a zero From/To as "default the window").
func nsToTime(ns int64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// eval.go (Phase 2, v0.9.11x) — the PromQL EVALUATOR. Hybrid model (approved):
// LEAF selectors + rate/increase/histogram_quantile push down to the bounded
// chstore machinery (F1-F3: LIMIT + max_execution_time + time-bounded WHERE,
// distributed-safe); series-over-series ops (aggregations, binary) will
// evaluate in Go in Phases 3-4. Range query → []chstore.SpanMetricSeries (the
// shape the UI charts already consume).
//
// PERFORMANCE IS A HARD CONSTRAINT (operator, 2026-07-20). Every guard here is
// deliberate: leaves reuse the already-bounded fetches; a query is rejected
// BEFORE any CH round trip if it is too deep or names no metric; the result is
// rejected if it explodes past MaxSeries. No unbounded selector, no Cartesian
// blow-up, no full-catalogue scan.

// MetricStore is the subset of *chstore.Store the evaluator needs — an
// interface so eval is unit-testable with a fake. *chstore.Store satisfies it.
type MetricStore interface {
	QueryMetric(ctx context.Context, f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error)
	QueryMetricRate(ctx context.Context, f chstore.MetricQueryFilter, mode string) ([]chstore.SpanMetricSeries, error)
	QueryMetricHistogramQuantile(ctx context.Context, f chstore.MetricQueryFilter, q float64) ([]chstore.SpanMetricSeries, error)
}

// EvalOptions bounds one evaluation. Zero values get safe defaults.
type EvalOptions struct {
	FromNs        int64 // range start (unix ns)
	ToNs          int64 // range end (unix ns)
	Step          int   // bucket seconds; 0 = width-aware auto (via MaxDataPoints)
	MaxDataPoints int   // panel px width ≈ target bucket count (F1)
	MaxSeries     int   // reject a result exceeding this many series (default 1000)
	MaxLeaves     int   // reject a query with more than this many selectors (default 25)
	MaxDepth      int   // reject an AST deeper than this (default 40)
}

func (o *EvalOptions) withDefaults() {
	if o.MaxSeries <= 0 {
		o.MaxSeries = 1000
	}
	if o.MaxLeaves <= 0 {
		o.MaxLeaves = 25
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = 40
	}
}

type evaluator struct {
	ctx    context.Context
	store  MetricStore
	opt    EvalOptions
	leaves int
}

// Eval compiles + runs a parsed PromQL expression as a RANGE query, returning
// time series. Enforces the depth/leaf/series caps.
func Eval(ctx context.Context, store MetricStore, expr Expr, opt EvalOptions) ([]chstore.SpanMetricSeries, error) {
	opt.withDefaults()
	if d := depth(expr); d > opt.MaxDepth {
		return nil, fmt.Errorf("promql: query nesting too deep (%d > %d)", d, opt.MaxDepth)
	}
	ev := &evaluator{ctx: ctx, store: store, opt: opt}
	series, err := ev.eval(expr)
	if err != nil {
		return nil, err
	}
	if len(series) > opt.MaxSeries {
		return nil, fmt.Errorf("promql: result has %d series (cap %d) — add label matchers or an aggregation to narrow it", len(series), opt.MaxSeries)
	}
	return series, nil
}

// EvalString parses + evaluates in one call (the API entry point).
func EvalString(ctx context.Context, store MetricStore, query string, opt EvalOptions) ([]chstore.SpanMetricSeries, error) {
	expr, err := Parse(query)
	if err != nil {
		return nil, err
	}
	return Eval(ctx, store, expr, opt)
}

func (ev *evaluator) eval(expr Expr) ([]chstore.SpanMetricSeries, error) {
	switch e := expr.(type) {
	case *NumberLiteral:
		return ev.scalarSeries(e.Val), nil
	case *ParenExpr:
		return ev.eval(e.Expr)
	case *UnaryExpr:
		s, err := ev.eval(e.Expr)
		if err != nil {
			return nil, err
		}
		if e.Op == "-" {
			mapValues(s, func(v float64) float64 { return -v })
		}
		return s, nil
	case *VectorSelector:
		return ev.evalVectorSelector(e)
	case *MatrixSelector:
		return nil, fmt.Errorf("promql: a range vector like %s must be inside a function such as rate()/increase()", e.String())
	case *Call:
		return ev.evalCall(e)
	case *AggregateExpr:
		return ev.evalAggregate(e)
	case *BinaryExpr:
		return nil, fmt.Errorf("promql: binary operator %q is not supported yet (Phase 4)", e.Op)
	case *SubqueryExpr:
		return nil, fmt.Errorf("promql: subqueries are not supported yet")
	default:
		return nil, fmt.Errorf("promql: cannot evaluate %T", expr)
	}
}

// evalVectorSelector — a bare instant vector: sample the metric per step. Maps
// to QueryMetric with agg=last (PromQL instant vector = latest value in step).
func (ev *evaluator) evalVectorSelector(vs *VectorSelector) ([]chstore.SpanMetricSeries, error) {
	f, err := ev.selectorFilter(vs)
	if err != nil {
		return nil, err
	}
	f.Aggregation = "last"
	return ev.store.QueryMetric(ev.ctx, f)
}

func (ev *evaluator) evalCall(c *Call) ([]chstore.SpanMetricSeries, error) {
	fn := strings.ToLower(c.Func)
	switch fn {
	case "rate", "increase", "irate":
		if len(c.Args) != 1 {
			return nil, fmt.Errorf("promql: %s() takes one range-vector argument", fn)
		}
		ms, ok := c.Args[0].(*MatrixSelector)
		if !ok {
			return nil, fmt.Errorf("promql: %s() expects a range vector like foo[5m], got %s", fn, c.Args[0].String())
		}
		f, err := ev.selectorFilter(ms.VectorSelector)
		if err != nil {
			return nil, err
		}
		mode := "rate"
		if fn == "increase" {
			mode = "increase"
		}
		// NOTE: Coremetry's rate is reset-safe per-step (F2); the [range] window
		// is advisory here (step-based smoothing), a documented approximation
		// vs Prometheus's over-window average. irate maps to rate for now.
		return ev.store.QueryMetricRate(ev.ctx, f, mode)

	case "histogram_quantile":
		if len(c.Args) != 2 {
			return nil, fmt.Errorf("promql: histogram_quantile(scalar, vector) takes two arguments")
		}
		ql, ok := c.Args[0].(*NumberLiteral)
		if !ok {
			return nil, fmt.Errorf("promql: histogram_quantile's first argument must be a scalar quantile")
		}
		if ql.Val < 0 || ql.Val > 1 {
			return nil, fmt.Errorf("promql: histogram_quantile quantile %g out of range [0,1]", ql.Val)
		}
		vs := underlyingHistogramSelector(c.Args[1])
		if vs == nil {
			return nil, fmt.Errorf("promql: histogram_quantile's second argument must be a histogram metric selector (nested sum(rate(...)) lands in Phase 3); got %s", c.Args[1].String())
		}
		f, err := ev.selectorFilter(vs)
		if err != nil {
			return nil, err
		}
		// Arbitrary quantile (v0.9.119) — not just p50/p95/p99.
		return ev.store.QueryMetricHistogramQuantile(ev.ctx, f, ql.Val)

	// Cheap per-point scalar functions — evaluate the vector then map in Go.
	case "abs", "ceil", "floor", "round", "sgn":
		if len(c.Args) != 1 {
			return nil, fmt.Errorf("promql: %s() takes one argument", fn)
		}
		s, err := ev.eval(c.Args[0])
		if err != nil {
			return nil, err
		}
		mapValues(s, unaryMathFn(fn))
		return s, nil
	case "clamp_min", "clamp_max":
		if len(c.Args) != 2 {
			return nil, fmt.Errorf("promql: %s(vector, scalar) takes two arguments", fn)
		}
		bound, ok := c.Args[1].(*NumberLiteral)
		if !ok {
			return nil, fmt.Errorf("promql: %s's second argument must be a scalar", fn)
		}
		s, err := ev.eval(c.Args[0])
		if err != nil {
			return nil, err
		}
		if fn == "clamp_min" {
			mapValues(s, func(v float64) float64 { return math.Max(v, bound.Val) })
		} else {
			mapValues(s, func(v float64) float64 { return math.Min(v, bound.Val) })
		}
		return s, nil

	default:
		return nil, fmt.Errorf("promql: function %q is not supported yet", c.Func)
	}
}

// evalAggregate handles sum/avg/min/max over a LEAF (selector or
// rate/increase/irate), pushing the by(L) grouping down into the leaf fetch's
// GroupBy. This is the PERFORMANCE-SAFE shape: the fetch groups by exactly the
// aggregation labels (bounded by L's cardinality, not the full label set) and
// does the reduction in ClickHouse, so `sum by(le)(rate(bucket[5m]))` is a
// single grouped rate query — no per-series fan-out into Go.
//
// Deferred (clear errors): without(…); topk/bottomk/quantile/stddev/count/
// count_values/group; avg/min/max OF a rate (QueryMetricRate only sums);
// aggregating a complex (non-leaf) inner like sum(a+b) — those need the
// per-series Go path / binary ops of a later phase.
func (ev *evaluator) evalAggregate(a *AggregateExpr) ([]chstore.SpanMetricSeries, error) {
	op := strings.ToLower(a.Op)
	// topk/bottomk operate on the evaluated inner VECTOR (usually itself a
	// bounded sum by(…)), selecting the k highest/lowest series in Go.
	if op == "topk" || op == "bottomk" {
		return ev.evalTopK(a, op)
	}
	switch op {
	case "sum", "avg", "min", "max":
	default:
		return nil, fmt.Errorf("promql: aggregation %q is not supported yet (have: sum/avg/min/max/topk/bottomk)", a.Op)
	}
	if a.Without {
		return nil, fmt.Errorf("promql: without(…) is not supported yet — use by(…)")
	}

	leaf, isRate, rateMode, err := aggLeaf(a.Expr)
	if err != nil {
		return nil, err
	}
	f, err := ev.selectorFilter(leaf)
	if err != nil {
		return nil, err
	}
	f.GroupBy = a.Grouping // by(L) → the leaf's GroupBy (nil/[] = global one series)

	if isRate {
		if op != "sum" {
			return nil, fmt.Errorf("promql: %s of a rate/increase is not supported yet — only sum by(…)(rate(…))", op)
		}
		return ev.store.QueryMetricRate(ev.ctx, f, rateMode)
	}
	f.Aggregation = op // sum/avg/min/max → QueryMetric's grouped reduction
	return ev.store.QueryMetric(ev.ctx, f)
}

// aggLeaf unwraps an aggregation's argument to the underlying selector, and
// reports whether it is wrapped in rate/increase/irate (so the caller routes to
// QueryMetricRate). Anything more complex is rejected for a later phase.
func aggLeaf(e Expr) (leaf *VectorSelector, isRate bool, rateMode string, err error) {
	switch x := e.(type) {
	case *ParenExpr:
		return aggLeaf(x.Expr)
	case *VectorSelector:
		return x, false, "", nil
	case *Call:
		fn := strings.ToLower(x.Func)
		if fn == "rate" || fn == "increase" || fn == "irate" {
			if len(x.Args) != 1 {
				return nil, false, "", fmt.Errorf("promql: %s() takes one range-vector argument", fn)
			}
			ms, ok := x.Args[0].(*MatrixSelector)
			if !ok {
				return nil, false, "", fmt.Errorf("promql: %s() expects a range vector like foo[5m]", fn)
			}
			mode := "rate"
			if fn == "increase" {
				mode = "increase"
			}
			return ms.VectorSelector, true, mode, nil
		}
		return nil, false, "", fmt.Errorf("promql: aggregating %s() is not supported yet", x.Func)
	default:
		return nil, false, "", fmt.Errorf("promql: can only aggregate a metric selector or rate()/increase() for now, got %s", e.String())
	}
}

// evalTopK evaluates topk/bottomk: eval the inner vector (typically an already-
// bounded sum by(…)), then keep the k series with the highest/lowest value. To
// avoid the per-timestamp flapping of true PromQL topk (a series can enter/leave
// the set over time), we rank by each series' MAX value over the window — the
// stable "show me the k worst" semantics operators actually want. A by(…) on the
// topk itself (per-group top-k) is deferred.
func (ev *evaluator) evalTopK(a *AggregateExpr, op string) ([]chstore.SpanMetricSeries, error) {
	if len(a.Grouping) > 0 {
		return nil, fmt.Errorf("promql: %s by(…) (per-group) is not supported yet — use %s(k, sum by(…)(…))", op, op)
	}
	kn, ok := a.Param.(*NumberLiteral)
	if !ok {
		return nil, fmt.Errorf("promql: %s(k, vector) — k must be a scalar", op)
	}
	k := int(kn.Val)
	if k <= 0 {
		return []chstore.SpanMetricSeries{}, nil
	}
	inner, err := ev.eval(a.Expr)
	if err != nil {
		return nil, err
	}
	if k >= len(inner) {
		return inner, nil
	}
	type ranked struct {
		s chstore.SpanMetricSeries
		v float64
	}
	rs := make([]ranked, len(inner))
	for i, s := range inner {
		m := math.Inf(-1)
		for _, p := range s.Points {
			if p.Value > m {
				m = p.Value
			}
		}
		rs[i] = ranked{s, m}
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if op == "topk" {
			return rs[i].v > rs[j].v
		}
		return rs[i].v < rs[j].v
	})
	out := make([]chstore.SpanMetricSeries, 0, k)
	for i := 0; i < k && i < len(rs); i++ {
		out = append(out, rs[i].s)
	}
	return out, nil
}

// selectorFilter converts a VectorSelector to a bounded MetricQueryFilter,
// enforcing the leaf cap + the metric-name requirement (no full-catalogue
// scan from a nameless selector).
func (ev *evaluator) selectorFilter(vs *VectorSelector) (chstore.MetricQueryFilter, error) {
	ev.leaves++
	if ev.leaves > ev.opt.MaxLeaves {
		return chstore.MetricQueryFilter{}, fmt.Errorf("promql: too many metric selectors (cap %d)", ev.opt.MaxLeaves)
	}
	if vs.Name == "" {
		return chstore.MetricQueryFilter{}, fmt.Errorf("promql: a selector must name a metric — bare {…} matchers would scan the whole catalogue")
	}
	f := chstore.MetricQueryFilter{
		Name:          vs.Name,
		From:          nsToTime(ev.opt.FromNs),
		To:            nsToTime(ev.opt.ToNs),
		StepSeconds:   ev.opt.Step,
		MaxDataPoints: ev.opt.MaxDataPoints,
	}
	for _, m := range vs.Matchers {
		name := m.Name
		// PromQL's `job` is the scrape job ≈ the OTel service; alias it to
		// service.name so = AND != resolve consistently (metricPointsWellKnown
		// maps service.name → the service_name column for both operators — a
		// bare `job` attr lookup would make job!= a silent match-all).
		if name == "job" {
			name = "service.name"
		}
		// = on the service label → the Service shortcut (partition prune).
		if m.Type == MatchEqual && (name == "service.name" || name == "service") {
			f.Service = m.Value
			continue
		}
		fe, err := matcherToFilter(&LabelMatcher{Name: name, Type: m.Type, Value: m.Value})
		if err != nil {
			return f, err
		}
		f.Filters = append(f.Filters, fe)
	}
	return f, nil
}

// matcherToFilter maps a PromQL label matcher to a chstore FilterExpr. Regex
// matchers (=~/!~, v0.9.118) map to the CH match() op (RE2, anchored).
func matcherToFilter(m *LabelMatcher) (chstore.FilterExpr, error) {
	switch m.Type {
	case MatchEqual:
		return chstore.FilterExpr{Key: m.Name, Op: "=", Values: []string{m.Value}}, nil
	case MatchNotEqual:
		return chstore.FilterExpr{Key: m.Name, Op: "!=", Values: []string{m.Value}}, nil
	case MatchRegexp:
		return chstore.FilterExpr{Key: m.Name, Op: "=~", Values: []string{m.Value}}, nil
	case MatchNotRegexp:
		return chstore.FilterExpr{Key: m.Name, Op: "!~", Values: []string{m.Value}}, nil
	default:
		return chstore.FilterExpr{}, fmt.Errorf("promql: unknown matcher type")
	}
}

// underlyingHistogramSelector unwraps the histogram_quantile 2nd arg to a bare
// selector. Coremetry's QueryMetricHistogramPercentile already does the
// rate + sum-by-le + quantile internally (F3), so histogram_quantile(q, <hist
// metric>) is the idiomatic form. A ParenExpr around a selector is unwrapped;
// a nested aggregation/rate is Phase 3 (returns nil → caller errors clearly).
func underlyingHistogramSelector(e Expr) *VectorSelector {
	switch x := e.(type) {
	case *VectorSelector:
		return x
	case *ParenExpr:
		return underlyingHistogramSelector(x.Expr)
	default:
		return nil
	}
}

// scalarSeries renders a scalar constant as a flat 2-point line spanning the
// query window (so `0.5` or a threshold expression charts as a reference line).
func (ev *evaluator) scalarSeries(v float64) []chstore.SpanMetricSeries {
	return []chstore.SpanMetricSeries{{
		GroupKey: nil,
		Points: []chstore.SpanMetricPoint{
			{Time: ev.opt.FromNs, Value: v},
			{Time: ev.opt.ToNs, Value: v},
		},
	}}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func mapValues(series []chstore.SpanMetricSeries, fn func(float64) float64) {
	for i := range series {
		for j := range series[i].Points {
			series[i].Points[j].Value = fn(series[i].Points[j].Value)
		}
	}
}

func unaryMathFn(fn string) func(float64) float64 {
	switch fn {
	case "abs":
		return math.Abs
	case "ceil":
		return math.Ceil
	case "floor":
		return math.Floor
	case "round":
		return math.Round
	case "sgn":
		return func(v float64) float64 {
			switch {
			case v > 0:
				return 1
			case v < 0:
				return -1
			default:
				return v // 0 or NaN passes through
			}
		}
	}
	return func(v float64) float64 { return v }
}

// depth returns the AST height — the complexity guard the evaluator checks
// before touching ClickHouse.
func depth(e Expr) int {
	switch x := e.(type) {
	case *ParenExpr:
		return 1 + depth(x.Expr)
	case *UnaryExpr:
		return 1 + depth(x.Expr)
	case *BinaryExpr:
		return 1 + maxInt(depth(x.LHS), depth(x.RHS))
	case *Call:
		m := 0
		for _, a := range x.Args {
			m = maxInt(m, depth(a))
		}
		return 1 + m
	case *AggregateExpr:
		d := depth(x.Expr)
		if x.Param != nil {
			d = maxInt(d, depth(x.Param))
		}
		return 1 + d
	case *MatrixSelector:
		return 1 + depth(x.VectorSelector)
	case *SubqueryExpr:
		return 1 + depth(x.Expr)
	default:
		return 1
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
