package promql

import (
	"context"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// fakeStore records how the evaluator called it and returns canned series, so
// the tests pin the AST→chstore ROUTING + the perf caps without a database.
type fakeStore struct {
	lastFilter chstore.MetricQueryFilter
	lastMode   string  // rate/increase
	lastQ      float64 // histogram quantile
	called     string  // which method
	ret        []chstore.SpanMetricSeries
	nSeries    int // if >0, return this many empty series (for the MaxSeries cap)
}

func (f *fakeStore) result() []chstore.SpanMetricSeries {
	if f.nSeries > 0 {
		out := make([]chstore.SpanMetricSeries, f.nSeries)
		return out
	}
	if f.ret != nil {
		return f.ret
	}
	return []chstore.SpanMetricSeries{{GroupKey: []string{"a"}, Points: []chstore.SpanMetricPoint{{Time: 1, Value: 10}, {Time: 2, Value: -20}}}}
}
func (f *fakeStore) QueryMetric(ctx context.Context, flt chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
	f.lastFilter, f.called = flt, "QueryMetric"
	return f.result(), nil
}
func (f *fakeStore) QueryMetricRate(ctx context.Context, flt chstore.MetricQueryFilter, mode string) ([]chstore.SpanMetricSeries, error) {
	f.lastFilter, f.lastMode, f.called = flt, mode, "QueryMetricRate"
	return f.result(), nil
}
func (f *fakeStore) QueryMetricHistogramQuantile(ctx context.Context, flt chstore.MetricQueryFilter, q float64) ([]chstore.SpanMetricSeries, error) {
	f.lastFilter, f.lastQ, f.called = flt, q, "QueryMetricHistogramQuantile"
	return f.result(), nil
}

func mustEval(t *testing.T, fs *fakeStore, q string) []chstore.SpanMetricSeries {
	t.Helper()
	s, err := EvalString(context.Background(), fs, q, EvalOptions{FromNs: 1000, ToNs: 2000})
	if err != nil {
		t.Fatalf("EvalString(%q) error: %v", q, err)
	}
	return s
}

func TestEvalRouting(t *testing.T) {
	// bare selector → QueryMetric with agg=last + service shortcut + a filter.
	fs := &fakeStore{}
	mustEval(t, fs, `http_requests{service.name="checkout",code="500"}`)
	if fs.called != "QueryMetric" {
		t.Fatalf("bare selector called %s, want QueryMetric", fs.called)
	}
	if fs.lastFilter.Name != "http_requests" || fs.lastFilter.Aggregation != "last" {
		t.Errorf("filter = %+v, want name=http_requests agg=last", fs.lastFilter)
	}
	if fs.lastFilter.Service != "checkout" {
		t.Errorf("service.name= should map to Service, got %q", fs.lastFilter.Service)
	}
	if len(fs.lastFilter.Filters) != 1 || fs.lastFilter.Filters[0].Key != "code" || fs.lastFilter.Filters[0].Op != "=" {
		t.Errorf("code= should map to a FilterExpr, got %+v", fs.lastFilter.Filters)
	}

	// rate / increase → QueryMetricRate with the right mode.
	fs = &fakeStore{}
	mustEval(t, fs, `rate(http_requests[5m])`)
	if fs.called != "QueryMetricRate" || fs.lastMode != "rate" {
		t.Errorf("rate() → %s mode=%s, want QueryMetricRate/rate", fs.called, fs.lastMode)
	}
	fs = &fakeStore{}
	mustEval(t, fs, `increase(http_requests[1h])`)
	if fs.lastMode != "increase" {
		t.Errorf("increase() mode=%s, want increase", fs.lastMode)
	}

	// histogram_quantile → QueryMetricHistogramQuantile with the float quantile
	// (arbitrary q, v0.9.119 — 0.9 is no longer rejected).
	fs = &fakeStore{}
	mustEval(t, fs, `histogram_quantile(0.9, http.server.duration)`)
	if fs.called != "QueryMetricHistogramQuantile" || fs.lastQ != 0.9 {
		t.Errorf("histogram_quantile(0.9) → %s q=%g, want …Quantile/0.9", fs.called, fs.lastQ)
	}
	if fs.lastFilter.Name != "http.server.duration" {
		t.Errorf("dotted histogram metric name = %q", fs.lastFilter.Name)
	}

	// job= → Service shortcut (PromQL scrape-job aliased to the OTel service).
	fs = &fakeStore{}
	mustEval(t, fs, `up{job="checkout"}`)
	if fs.lastFilter.Service != "checkout" {
		t.Errorf("job= should map to Service, got %q", fs.lastFilter.Service)
	}
	// job!= must NOT silently vanish — it becomes a service.name != filter
	// (review MINOR: a bare `job` attr lookup made job!= a match-all).
	fs = &fakeStore{}
	mustEval(t, fs, `up{job!="checkout"}`)
	if fs.lastFilter.Service != "" {
		t.Errorf("job!= must not use the = Service shortcut, got Service=%q", fs.lastFilter.Service)
	}
	if len(fs.lastFilter.Filters) != 1 || fs.lastFilter.Filters[0].Key != "service.name" || fs.lastFilter.Filters[0].Op != "!=" {
		t.Errorf("job!= should be a service.name != filter, got %+v", fs.lastFilter.Filters)
	}

	// regex matchers (v0.9.118) → FilterExpr with =~ / !~ (CH match()).
	fs = &fakeStore{}
	mustEval(t, fs, `http_requests{code=~"5..",method!~"GET|HEAD"}`)
	if len(fs.lastFilter.Filters) != 2 {
		t.Fatalf("regex matchers → %d filters, want 2", len(fs.lastFilter.Filters))
	}
	if fs.lastFilter.Filters[0].Op != "=~" || fs.lastFilter.Filters[0].Values[0] != "5.." {
		t.Errorf("code=~ → %+v, want Op ==~ value 5..", fs.lastFilter.Filters[0])
	}
	if fs.lastFilter.Filters[1].Op != "!~" {
		t.Errorf("method!~ → Op %q, want !~", fs.lastFilter.Filters[1].Op)
	}
	// regex on the service label goes through the filter (not the = shortcut).
	fs = &fakeStore{}
	mustEval(t, fs, `up{service.name=~"api.*"}`)
	if fs.lastFilter.Service != "" {
		t.Errorf("service.name=~ must not use the = shortcut, got Service=%q", fs.lastFilter.Service)
	}
	if len(fs.lastFilter.Filters) != 1 || fs.lastFilter.Filters[0].Op != "=~" {
		t.Errorf("service.name=~ should be a =~ filter, got %+v", fs.lastFilter.Filters)
	}
}

func TestEvalAggregation(t *testing.T) {
	// sum by(le)(rate(bucket[5m])) → QueryMetricRate with GroupBy pushed down.
	fs := &fakeStore{}
	mustEval(t, fs, `sum by (le) (rate(http_bucket[5m]))`)
	if fs.called != "QueryMetricRate" || fs.lastMode != "rate" {
		t.Fatalf("sum(rate) → %s/%s, want QueryMetricRate/rate", fs.called, fs.lastMode)
	}
	if len(fs.lastFilter.GroupBy) != 1 || fs.lastFilter.GroupBy[0] != "le" {
		t.Errorf("by(le) should push GroupBy=[le], got %v", fs.lastFilter.GroupBy)
	}

	// sum without by → global (GroupBy empty).
	fs = &fakeStore{}
	mustEval(t, fs, `sum(rate(x[5m]))`)
	if len(fs.lastFilter.GroupBy) != 0 {
		t.Errorf("sum without by → GroupBy empty, got %v", fs.lastFilter.GroupBy)
	}

	// avg by(pod)(gauge) → QueryMetric GroupBy=[pod] agg=avg.
	fs = &fakeStore{}
	mustEval(t, fs, `avg by (pod) (jvm.memory.used)`)
	if fs.called != "QueryMetric" || fs.lastFilter.Aggregation != "avg" {
		t.Errorf("avg by(pod) → %s agg=%s, want QueryMetric/avg", fs.called, fs.lastFilter.Aggregation)
	}
	if len(fs.lastFilter.GroupBy) != 1 || fs.lastFilter.GroupBy[0] != "pod" {
		t.Errorf("by(pod) should push GroupBy=[pod], got %v", fs.lastFilter.GroupBy)
	}

	// max by(instance)(gauge) → agg=max.
	fs = &fakeStore{}
	mustEval(t, fs, `max by (instance) (system.cpu.utilization)`)
	if fs.lastFilter.Aggregation != "max" {
		t.Errorf("max agg = %q, want max", fs.lastFilter.Aggregation)
	}
}

func TestEvalTopK(t *testing.T) {
	mk := func(label string, v float64) chstore.SpanMetricSeries {
		return chstore.SpanMetricSeries{GroupKey: []string{label}, Points: []chstore.SpanMetricPoint{{Time: 1, Value: v}}}
	}
	four := func() []chstore.SpanMetricSeries {
		return []chstore.SpanMetricSeries{mk("a", 10), mk("b", 50), mk("c", 30), mk("d", 5)}
	}

	// topk(2, …) keeps the 2 highest-by-max series: b(50), c(30).
	fs := &fakeStore{ret: four()}
	s, err := EvalString(context.Background(), fs, `topk(2, sum by(svc)(foo))`, EvalOptions{FromNs: 1, ToNs: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 2 || s[0].GroupKey[0] != "b" || s[1].GroupKey[0] != "c" {
		t.Errorf("topk(2) = %v, want [b c]", groupKeys(s))
	}

	// bottomk(2, …) keeps the 2 lowest: d(5), a(10).
	fs = &fakeStore{ret: four()}
	s, _ = EvalString(context.Background(), fs, `bottomk(2, sum by(svc)(foo))`, EvalOptions{FromNs: 1, ToNs: 2})
	if len(s) != 2 || s[0].GroupKey[0] != "d" || s[1].GroupKey[0] != "a" {
		t.Errorf("bottomk(2) = %v, want [d a]", groupKeys(s))
	}

	// k >= series count → all returned; k <= 0 → empty; by(…) on topk → error.
	fs = &fakeStore{ret: four()}
	if s, _ := EvalString(context.Background(), fs, `topk(10, sum by(svc)(foo))`, EvalOptions{FromNs: 1, ToNs: 2}); len(s) != 4 {
		t.Errorf("topk(10) of 4 = %d, want 4", len(s))
	}
	fs = &fakeStore{ret: four()}
	if s, _ := EvalString(context.Background(), fs, `topk(0, foo)`, EvalOptions{FromNs: 1, ToNs: 2}); len(s) != 0 {
		t.Errorf("topk(0) = %d, want 0", len(s))
	}
	if _, err := EvalString(context.Background(), &fakeStore{}, `topk(2, foo) by (le)`, EvalOptions{FromNs: 1, ToNs: 2}); err == nil {
		t.Errorf("topk by(…) should error (deferred)")
	}
}

func groupKeys(s []chstore.SpanMetricSeries) []string {
	out := make([]string, len(s))
	for i, x := range s {
		if len(x.GroupKey) > 0 {
			out[i] = x.GroupKey[0]
		}
	}
	return out
}

func TestEvalScalarFunctions(t *testing.T) {
	// abs(x) applies per-point over the fetched series.
	fs := &fakeStore{ret: []chstore.SpanMetricSeries{{Points: []chstore.SpanMetricPoint{{Time: 1, Value: -5}, {Time: 2, Value: 3}}}}}
	s := mustEval(t, fs, `abs(foo)`)
	if s[0].Points[0].Value != 5 || s[0].Points[1].Value != 3 {
		t.Errorf("abs(foo) = %v, want [5 3]", s[0].Points)
	}

	// unary minus negates.
	fs = &fakeStore{ret: []chstore.SpanMetricSeries{{Points: []chstore.SpanMetricPoint{{Time: 1, Value: 5}}}}}
	s = mustEval(t, fs, `-foo`)
	if s[0].Points[0].Value != -5 {
		t.Errorf("-foo = %v, want -5", s[0].Points[0].Value)
	}

	// clamp_max caps values.
	fs = &fakeStore{ret: []chstore.SpanMetricSeries{{Points: []chstore.SpanMetricPoint{{Time: 1, Value: 150}, {Time: 2, Value: 50}}}}}
	s = mustEval(t, fs, `clamp_max(foo, 100)`)
	if s[0].Points[0].Value != 100 || s[0].Points[1].Value != 50 {
		t.Errorf("clamp_max = %v, want [100 50]", s[0].Points)
	}

	// scalar literal → flat 2-point reference line.
	fs = &fakeStore{}
	s, err := EvalString(context.Background(), fs, `0.5`, EvalOptions{FromNs: 1000, ToNs: 2000})
	if err != nil || len(s) != 1 || len(s[0].Points) != 2 || s[0].Points[0].Value != 0.5 || s[0].Points[1].Time != 2000 {
		t.Errorf("scalar 0.5 → %+v (err %v), want a flat [1000,2000]@0.5 line", s, err)
	}
}

func TestEvalCapsAndErrors(t *testing.T) {
	fs := &fakeStore{}
	bad := []struct{ q, wantSub string }{
		{`{code="500"}`, "must name a metric"},          // nameless selector
		{`foo[5m]`, "must be inside a function"},         // bare range vector
		{`rate(a[5m]) / rate(b[5m])`, "Phase 4"},         // binary deferred
		{`sum without (pod) (foo)`, "without"},           // without deferred
		{`avg(rate(foo[5m]))`, "only sum"},               // avg-of-rate deferred
		{`sum(a + b)`, "can only aggregate"},             // complex inner deferred
		{`histogram_quantile(1.5, foo)`, "out of range"}, // quantile out of [0,1]
		{`histogram_quantile(0.95, sum(foo))`, "Phase 3"},// nested hist arg
		{`rate(foo)`, "range vector"},                    // rate needs a matrix
	}
	for _, c := range bad {
		_, err := EvalString(context.Background(), fs, c.q, EvalOptions{FromNs: 1, ToNs: 2})
		if err == nil {
			t.Errorf("EvalString(%q) = nil error, want error containing %q", c.q, c.wantSub)
			continue
		}
		if !strings.Contains(err.Error(), c.wantSub) {
			t.Errorf("EvalString(%q) error = %q, want substring %q", c.q, err.Error(), c.wantSub)
		}
	}

	// MaxSeries cap: a fetch returning 5 series with cap 3 is rejected.
	fs = &fakeStore{nSeries: 5}
	_, err := EvalString(context.Background(), fs, `foo`, EvalOptions{FromNs: 1, ToNs: 2, MaxSeries: 3})
	if err == nil || !strings.Contains(err.Error(), "series") {
		t.Errorf("MaxSeries cap not enforced: %v", err)
	}

	// (MaxLeaves cap is exercised in Phase 3/4 — no Phase-2 query reaches 2+
	// selectors, since binary ops / aggregations are the multi-leaf shapes.)

	// MaxDepth cap: deep nesting rejected before any fetch.
	fs = &fakeStore{}
	_, err = EvalString(context.Background(), fs, `((((((foo))))))`, EvalOptions{FromNs: 1, ToNs: 2, MaxDepth: 3})
	if err == nil || !strings.Contains(err.Error(), "too deep") {
		t.Errorf("MaxDepth cap not enforced: %v", err)
	}
}
