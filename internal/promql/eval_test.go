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
	lastMode   string // rate/increase
	lastAgg    string // histogram percentile
	called     string // which method
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
func (f *fakeStore) QueryMetricHistogramPercentile(ctx context.Context, flt chstore.MetricQueryFilter, agg string) ([]chstore.SpanMetricSeries, error) {
	f.lastFilter, f.lastAgg, f.called = flt, agg, "QueryMetricHistogramPercentile"
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

	// histogram_quantile → QueryMetricHistogramPercentile with the mapped agg.
	fs = &fakeStore{}
	mustEval(t, fs, `histogram_quantile(0.95, http.server.duration)`)
	if fs.called != "QueryMetricHistogramPercentile" || fs.lastAgg != "p95" {
		t.Errorf("histogram_quantile(0.95) → %s agg=%s, want …Percentile/p95", fs.called, fs.lastAgg)
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
		{`sum(rate(foo[5m]))`, "Phase 3"},                // aggregation deferred
		{`rate(a[5m]) / rate(b[5m])`, "Phase 4"},         // binary deferred
		{`foo{bar=~"x.*"}`, "regex matcher"},             // regex deferred
		{`histogram_quantile(0.9, foo)`, "0.5, 0.95"},    // unsupported quantile
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
