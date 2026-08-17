package vmetrics

// v0.9.1150 — VictoriaMetrics read backend, Faz 1.
//
// The PromQL translation is the whole correctness surface of this
// backend: everything past it is HTTP. The aggregation × group-by matrix
// is exhaustive on purpose (the value+unit lesson from v0.6.36 applied to
// a different two-axis template: one untested combination is a silently
// wrong chart, not a crash).
//
// Two properties are pinned harder than the rest because breaking either
// produces a WRONG NUMBER rather than an error:
//
//   - an inexpressible filter must ERROR, never vanish (v0.9.566: a
//     dropped jvm.memory.type="heap" charted heap+non-heap AS "heap"),
//   - GroupKey order must follow the REQUESTED order, not VM's map
//     iteration, or series get relabelled between polls.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestPromAggregator(t *testing.T) {
	ok := map[string]string{
		"":      "avg",
		"avg":   "avg",
		"AVG":   "avg",
		" avg ": "avg",
		"sum":   "sum",
		"min":   "min",
		"max":   "max",
		"count": "count",
	}
	for in, want := range ok {
		got, err := promAggregator(in)
		if err != nil {
			t.Fatalf("promAggregator(%q): unexpected error %v", in, err)
		}
		if got != want {
			t.Fatalf("promAggregator(%q) = %q, want %q", in, got, want)
		}
	}
	// Refused — each would need real work to be CORRECT, and a plausible
	// wrong answer is the failure mode we are avoiding.
	for _, in := range []string{"rate", "increase", "last", "p50", "p95", "p99", "median", "nonsense"} {
		if _, err := promAggregator(in); err == nil {
			t.Fatalf("promAggregator(%q): want error, got nil", in)
		}
	}
}

// Full aggregation × group-by matrix.
func TestBuildPromQLAggregationGroupByMatrix(t *testing.T) {
	aggs := []struct{ in, op string }{
		{"", "avg"}, {"avg", "avg"}, {"sum", "sum"},
		{"min", "min"}, {"max", "max"}, {"count", "count"},
	}
	groupBys := []struct {
		name string
		in   []string
		by   string // "" = no by-clause
	}{
		{name: "no group-by", in: nil, by: ""},
		{name: "empty slice", in: []string{}, by: ""},
		{name: "single", in: []string{"pod"}, by: "pod"},
		{name: "two", in: []string{"pod", "host.name"}, by: "pod, host_name"},
		{name: "dotted key sanitized", in: []string{"jvm.memory.type"}, by: "jvm_memory_type"},
	}
	for _, a := range aggs {
		for _, g := range groupBys {
			t.Run(a.in+"/"+g.name, func(t *testing.T) {
				got, err := buildPromQL(chstore.MetricQueryFilter{
					Name:        "jvm.memory.used",
					Aggregation: a.in,
					GroupBy:     g.in,
				})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				want := a.op + `({__name__="jvm.memory.used"})`
				if g.by != "" {
					want = a.op + ` by (` + g.by + `) ({__name__="jvm.memory.used"})`
				}
				if got != want {
					t.Fatalf("got  %s\nwant %s", got, want)
				}
			})
		}
	}
}

func TestBuildPromQLSelector(t *testing.T) {
	tests := []struct {
		name    string
		f       chstore.MetricQueryFilter
		want    string
		wantErr string
	}{
		{
			// The metric name is NEVER translated: whichever spelling the
			// catalogue handed us is the spelling VM has. __name__ selector
			// form is what makes a dotted name expressible at all.
			name: "dotted metric name goes through verbatim",
			f:    chstore.MetricQueryFilter{Name: "http.server.request.duration"},
			want: `avg({__name__="http.server.request.duration"})`,
		},
		{
			name: "underscored metric name also verbatim",
			f:    chstore.MetricQueryFilter{Name: "http_server_request_duration"},
			want: `avg({__name__="http_server_request_duration"})`,
		},
		{
			name: "service becomes service_name",
			f:    chstore.MetricQueryFilter{Name: "m", Service: "api-gateway"},
			want: `avg({__name__="m", service_name="api-gateway"})`,
		},
		{
			name: "quotes in a value are escaped",
			f:    chstore.MetricQueryFilter{Name: `m"x`, Service: `a\b`},
			want: `avg({__name__="m\"x", service_name="a\\b"})`,
		},
		{
			name:    "empty metric name",
			f:       chstore.MetricQueryFilter{Name: "  "},
			wantErr: "metric name required",
		},
		{
			// DB-receiver instance scoping is a CH-side per-engine OR over
			// receiver attributes. Guessing a VM equivalent would scope the
			// chart to the WRONG instance — the exact cross-poisoning the
			// CH clause was added for.
			name:    "instance scoping refused",
			f:       chstore.MetricQueryFilter{Name: "m", Instance: "pg-1", Engine: "postgresql"},
			wantErr: "instance/engine scoping is unsupported",
		},
		{
			name:    "engine alone also refused",
			f:       chstore.MetricQueryFilter{Name: "m", Engine: "postgresql"},
			wantErr: "instance/engine scoping is unsupported",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildPromQL(tc.f)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v (%q)", tc.wantErr, err, got)
				}
				// The SENTINEL is the contract the API classifies on (400 vs
				// 502); the wording is only for the operator. "metric name
				// required" is a caller bug, not an untranslatable request,
				// so it is exempt.
				if !strings.Contains(tc.wantErr, "required") && !errors.Is(err, ErrUnsupported) {
					t.Fatalf("refusal not tagged ErrUnsupported — the API would 502 and blame "+
						"a healthy VictoriaMetrics: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestPromMatcher(t *testing.T) {
	tests := []struct {
		name    string
		fe      chstore.FilterExpr
		want    string
		wantErr string
	}{
		{name: "eq single", fe: chstore.FilterExpr{Key: "pod", Op: "=", Values: []string{"api-1"}},
			want: `pod="api-1"`},
		{name: "neq single", fe: chstore.FilterExpr{Key: "pod", Op: "!=", Values: []string{"api-1"}},
			want: `pod!="api-1"`},
		{name: "dotted key sanitized", fe: chstore.FilterExpr{Key: "jvm.memory.type", Op: "=", Values: []string{"heap"}},
			want: `jvm_memory_type="heap"`},
		{name: "resource prefix stripped", fe: chstore.FilterExpr{Key: "resource.host.name", Op: "=", Values: []string{"h1"}},
			want: `host_name="h1"`},
		{name: "span prefix stripped", fe: chstore.FilterExpr{Key: "span.http.route", Op: "=", Values: []string{"/x"}},
			want: `http_route="/x"`},
		{
			// PromQL has no IN — the alternation must be regexp-QUOTED, or
			// `api-1.2` would also match `api-1x2`.
			name: "multi-value eq becomes an escaped alternation",
			fe:   chstore.FilterExpr{Key: "pod", Op: "=", Values: []string{"api-1.2", "api-2"}},
			want: `pod=~"api-1\\.2|api-2"`,
		},
		{name: "IN is multi eq", fe: chstore.FilterExpr{Key: "pod", Op: "IN", Values: []string{"a", "b"}},
			want: `pod=~"a|b"`},
		{name: "IN single collapses to eq", fe: chstore.FilterExpr{Key: "pod", Op: "IN", Values: []string{"a"}},
			want: `pod="a"`},
		{name: "NOT IN", fe: chstore.FilterExpr{Key: "pod", Op: "NOT IN", Values: []string{"a", "b"}},
			want: `pod!~"a|b"`},
		{
			// Already a regex — must NOT be QuoteMeta'd, or the operator's
			// `.*` becomes a literal.
			name: "regex passes through unquoted",
			fe:   chstore.FilterExpr{Key: "pod", Op: "=~", Values: []string{"api-.*"}},
			want: `pod=~"api-.*"`,
		},
		{name: "negative regex", fe: chstore.FilterExpr{Key: "pod", Op: "!~", Values: []string{"canary-.*"}},
			want: `pod!~"canary-.*"`},
		{name: "EXISTS", fe: chstore.FilterExpr{Key: "pod", Op: "EXISTS"}, want: `pod!=""`},
		{name: "NOT EXISTS", fe: chstore.FilterExpr{Key: "pod", Op: "NOT EXISTS"}, want: `pod=""`},
		{name: "lowercase exists", fe: chstore.FilterExpr{Key: "pod", Op: "exists"}, want: `pod!=""`},
		// Refusals — a filter we cannot express must never be dropped.
		{name: "LIKE refused", fe: chstore.FilterExpr{Key: "pod", Op: "LIKE", Values: []string{"api%"}},
			wantErr: "unsupported"},
		{name: "NOT LIKE refused", fe: chstore.FilterExpr{Key: "pod", Op: "NOT LIKE", Values: []string{"api%"}},
			wantErr: "unsupported"},
		{name: "gt refused", fe: chstore.FilterExpr{Key: "n", Op: ">", Values: []string{"5"}},
			wantErr: "unsupported"},
		{name: "gte refused", fe: chstore.FilterExpr{Key: "n", Op: ">=", Values: []string{"5"}},
			wantErr: "unsupported"},
		{name: "lt refused", fe: chstore.FilterExpr{Key: "n", Op: "<", Values: []string{"5"}},
			wantErr: "unsupported"},
		{name: "lte refused", fe: chstore.FilterExpr{Key: "n", Op: "<=", Values: []string{"5"}},
			wantErr: "unsupported"},
		{name: "empty key refused", fe: chstore.FilterExpr{Key: "", Op: "=", Values: []string{"a"}},
			wantErr: "empty key"},
		{name: "no value refused", fe: chstore.FilterExpr{Key: "pod", Op: "=", Values: nil},
			wantErr: "has no value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := promMatcher(tc.fe)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v (%q)", tc.wantErr, err, got)
				}
				// An UNSUPPORTED OPERATOR is tagged (→ 400); a malformed
				// filter (empty key / no value) is a caller bug and is not.
				if strings.Contains(tc.wantErr, "unsupported") && !errors.Is(err, ErrUnsupported) {
					t.Fatalf("operator refusal not tagged ErrUnsupported: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// A refused filter must fail the WHOLE query, not get skipped on the way
// into the selector.
func TestBuildPromQLRefusedFilterFailsTheQuery(t *testing.T) {
	_, err := buildPromQL(chstore.MetricQueryFilter{
		Name: "m",
		Filters: []chstore.FilterExpr{
			{Key: "pod", Op: "=", Values: []string{"api-1"}},
			{Key: "n", Op: ">", Values: []string{"5"}},
		},
	})
	if err == nil {
		t.Fatal("an inexpressible filter was silently dropped — this is the v0.9.566 class")
	}
}

func TestBuildPromQLFilterOrderPreserved(t *testing.T) {
	got, err := buildPromQL(chstore.MetricQueryFilter{
		Name:    "m",
		Service: "svc",
		Filters: []chstore.FilterExpr{
			{Key: "a", Op: "=", Values: []string{"1"}},
			{Key: "b", Op: "=", Values: []string{"2"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `avg({__name__="m", service_name="svc", a="1", b="2"})`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestPromLabel(t *testing.T) {
	tests := map[string]string{
		"pod":                "pod",
		"service.name":       "service_name",
		"http.route":         "http_route",
		"k8s.pod.name":       "k8s_pod_name",
		"resource.host.name": "host_name",
		"span.http.method":   "http_method",
		"weird-key!":         "weird_key_",
		"2xx":                "_2xx",
		"":                   "",
		"  ":                 "",
	}
	for in, want := range tests {
		if got := promLabel(in); got != want {
			t.Fatalf("promLabel(%q) = %q, want %q", in, got, want)
		}
	}
	if serviceLabel() != "service_name" {
		t.Fatalf("serviceLabel() = %q", serviceLabel())
	}
}

func TestPromStep(t *testing.T) {
	from := time.Unix(1700000000, 0)
	tests := []struct {
		name       string
		rangeSec   int
		step       int
		maxDP      int
		want       int
		wantReason string
	}{
		{name: "explicit step honoured", rangeSec: 3600, step: 30, want: 30},
		{name: "explicit step of 1s honoured", rangeSec: 600, step: 1, want: 1},
		{
			// 30 days at 10s would be 259200 points — VM 4xxs past ~11k.
			name:     "explicit step widened past the points ceiling",
			rangeSec: 30 * 24 * 3600, step: 10, want: 236,
			wantReason: "ceil(2592000/11000)",
		},
		{name: "pixel-adaptive", rangeSec: 3600, maxDP: 400, want: 9},
		{name: "pixel-adaptive rounds up", rangeSec: 100, maxDP: 30, want: 4},
		{name: "pixel-adaptive never below 1s", rangeSec: 10, maxDP: 4000, want: 1},
		{name: "no step no maxDP falls back to bucket count", rangeSec: 3600, want: 12},
		{name: "fallback on a 24h window", rangeSec: 86400, want: 288},
		{name: "explicit beats maxDP", rangeSec: 3600, step: 60, maxDP: 4000, want: 60},
		{name: "zero-length window still yields a legal step", rangeSec: 0, want: 1},
		{name: "negative window still yields a legal step", rangeSec: -60, want: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			to := from.Add(time.Duration(tc.rangeSec) * time.Second)
			got := promStep(from, to, tc.step, tc.maxDP)
			if got != tc.want {
				t.Fatalf("promStep(range=%ds, step=%d, maxDP=%d) = %d, want %d %s",
					tc.rangeSec, tc.step, tc.maxDP, got, tc.want, tc.wantReason)
			}
			if got < 1 {
				t.Fatalf("step must never be < 1s, got %d", got)
			}
			if tc.rangeSec > 0 && tc.rangeSec/got > maxPromPoints {
				t.Fatalf("step %d leaves %d points, over the %d ceiling",
					got, tc.rangeSec/got, maxPromPoints)
			}
		})
	}
}

// GroupKey is POSITIONAL — the frontend joins it with "|" to label the
// line. Reading VM's label map in map order would relabel series between
// polls at random.
func TestSeriesGroupKey(t *testing.T) {
	labels := map[string]string{
		"service_name":    "api",
		"pod":             "api-1",
		"jvm_memory_type": "heap",
		"__name__":        "jvm.memory.used",
	}
	tests := []struct {
		name    string
		groupBy []string
		want    []string
	}{
		{name: "nil group-by yields nil", groupBy: nil, want: nil},
		{name: "single", groupBy: []string{"pod"}, want: []string{"api-1"}},
		{
			name:    "order follows the REQUEST not the map",
			groupBy: []string{"jvm.memory.type", "pod", "service.name"},
			want:    []string{"heap", "api-1", "api"},
		},
		{
			name:    "reversed request reverses the tuple",
			groupBy: []string{"service.name", "pod"},
			want:    []string{"api", "api-1"},
		},
		{
			// A missing label yields "" IN PLACE. Skipping it would shift
			// the tuple left and mislabel every remaining dimension.
			name:    "missing label holds its slot",
			groupBy: []string{"pod", "nope", "service.name"},
			want:    []string{"api-1", "", "api"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := seriesGroupKey(tc.groupBy, labels)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// pageNames carries the whole {names,total,hasMore} contract for VM,
// because VM's label-values endpoint has neither a substring filter nor
// an offset. `hasMore` is derived by the handler as
// offset+len(names) < total, so `total` must count MATCHES, never the
// returned page.
func TestPageNames(t *testing.T) {
	all := []string{"a.one", "a.two", "b.one", "b.two", "c.one"}
	tests := []struct {
		name      string
		pattern   string
		limit     int
		offset    int
		unlimited bool
		want      []string
		wantTotal int
	}{
		{
			name: "unlimited returns everything", unlimited: true,
			want: all, wantTotal: 5,
		},
		{
			name: "first page", limit: 2,
			want: []string{"a.one", "a.two"}, wantTotal: 5,
		},
		{
			name: "second page", limit: 2, offset: 2,
			want: []string{"b.one", "b.two"}, wantTotal: 5,
		},
		{
			// total stays 5 so hasMore = 4+1 < 5 = false. If total were the
			// page length the picker would never stop paging.
			name: "last partial page", limit: 2, offset: 4,
			want: []string{"c.one"}, wantTotal: 5,
		},
		{
			name: "offset past the end is empty, not an error", limit: 2, offset: 99,
			want: []string{}, wantTotal: 5,
		},
		{
			name: "pattern narrows total too", pattern: "a.", limit: 10,
			want: []string{"a.one", "a.two"}, wantTotal: 2,
		},
		{
			name: "pattern is case-insensitive", pattern: "A.ONE", limit: 10,
			want: []string{"a.one"}, wantTotal: 1,
		},
		{
			name: "pattern matches mid-name", pattern: "one", limit: 10,
			want: []string{"a.one", "b.one", "c.one"}, wantTotal: 3,
		},
		{
			name: "no match", pattern: "zzz", limit: 10,
			want: []string{}, wantTotal: 0,
		},
		{
			name: "zero limit defaults to 200", limit: 0, offset: 1,
			want: []string{"a.two", "b.one", "b.two", "c.one"}, wantTotal: 5,
		},
		{
			name: "negative offset clamps to 0", limit: 1, offset: -5,
			want: []string{"a.one"}, wantTotal: 5,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, total := pageNames(all, tc.pattern, tc.limit, tc.offset, tc.unlimited)
			if total != tc.wantTotal {
				t.Fatalf("total = %d, want %d", total, tc.wantTotal)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// Empty names are dropped rather than becoming a blank catalogue row.
func TestPageNamesDropsEmpties(t *testing.T) {
	got, total := pageNames([]string{"", "a", "", "b"}, "", 10, 0, false)
	if total != 2 || len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %v (total %d)", got, total)
	}
}
