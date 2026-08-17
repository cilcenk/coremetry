package api

// v0.9.1150 — VictoriaMetrics read backend, Faz 1.
//
// Three gates live here, and all three protect properties that would fail
// SILENTLY:
//
//  1. SOURCE PIN — the metric handlers must go through s.metricSource().
//     A new handler that calls s.store directly would read ClickHouse
//     while every other metric surface reads VM, and the page would look
//     fine.
//  2. CACHE-KEY BACKEND MARKER — every metric cache key carries the
//     backend. Without it, flipping the Settings toggle serves the old
//     store's bodies for a full TTL and two pods refreshing at different
//     moments disagree (v0.5.187 class, with an INPUT that is a setting).
//  3. SELECTOR — Configured() alone decides, and a half-filled form must
//     stay on ClickHouse.

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/vmetrics"
)

// Metric store methods that MUST NOT be called directly from api.go.
//
// Scoped to api.go on purpose. Two other files call these legitimately
// and stay on ClickHouse by DESIGN (metricsource.go's header lists why):
//   - service_metric_throughput.go — the fixed-name throughput probe,
//   - dql.go — the DQL evaluator.
//
// Widening the scan to the package would therefore have to exempt them,
// and an exemption list is the thing that quietly grows. api.go is where
// every operator-facing metric handler lives, which makes it the precise
// boundary.
var metricStoreMethods = []string{
	"QueryMetric",
	"ListMetricNames",
	"GetMetricNames",
	"MetricLabelValues",
	"MetricAttrKeys",
}

func TestMetricHandlersGoThroughTheSourceSeam(t *testing.T) {
	raw, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoComments(string(raw))

	// Guard against a vacuous pass: if the comment stripper over-ate (a
	// `//` line containing `/*` can make the naive block regex swallow
	// real code — the reason this assertion exists), the scan below would
	// find nothing and report success.
	for _, marker := range []string{
		"func (s *Server) getMetricNames",
		"func (s *Server) queryMetric",
		"func (s *Server) getMetricLabelValues",
		"func (s *Server) getMetricAttrKeys",
	} {
		if !strings.Contains(src, marker) {
			t.Fatalf("comment stripping ate real code — %q is missing, so this scan proves nothing", marker)
		}
	}

	for _, m := range metricStoreMethods {
		re := regexp.MustCompile(`s\.store\.` + m + `\(`)
		if loc := re.FindStringIndex(src); loc != nil {
			t.Errorf("api.go calls s.store.%s directly at offset %d — metric reads must go "+
				"through s.metricSource() (metricsource.go), or the VictoriaMetrics backend "+
				"silently does not apply to this surface", m, loc[0])
		}
	}
}

// Every metric cache key must name the backend. The five keys are matched
// by their literal prefixes so a renamed key fails loudly rather than
// dropping out of the scan.
func TestMetricCacheKeysCarryTheBackendMarker(t *testing.T) {
	raw, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoComments(string(raw))

	keyPrefixes := []string{
		"metric-names:v3:src=",    // legacy bare-array shape
		"metric-names:v3:src=%s",  // {names,total,hasMore} envelope
		"metric-query:v3:src=%s",  // Explore / dashboards / MQE
		"metric-labels:src=%s",    // filter value suggestions
		"metric-attr-keys:src=%s", // filter key suggestions
	}
	for _, p := range keyPrefixes {
		if !strings.Contains(src, p) {
			t.Errorf("no metric cache key with prefix %q found in api.go — a metric key without "+
				"the backend marker cross-poisons across a Settings toggle (v0.5.187)", p)
		}
	}

	// And the inverse: no metric key may exist WITHOUT src=. Catches a new
	// key added by copy-pasting a pre-v0.9.1150 line.
	badKey := regexp.MustCompile(`"metric-(names|query|labels|attr-keys)[^"]*"`)
	for _, m := range badKey.FindAllString(src, -1) {
		if !strings.Contains(m, "src=") {
			t.Errorf("metric cache key %s has no backend marker", m)
		}
	}
}

// The selector is the whole routing decision. Note the "enabled but no
// URL" row: without it a half-filled form would route every metric
// surface at a backend that cannot answer, and there is no fallback.
func TestMetricSourceSelector(t *testing.T) {
	tests := []struct {
		name string
		cfg  *vmetrics.Settings // nil = no service wired at all
		want string
	}{
		{name: "no vmetrics service wired", cfg: nil, want: metricSourceCH},
		{name: "service wired but unconfigured", cfg: &vmetrics.Settings{}, want: metricSourceCH},
		{name: "disabled with a url", cfg: &vmetrics.Settings{BaseURL: "http://vm:8428"}, want: metricSourceCH},
		{name: "enabled without a url", cfg: &vmetrics.Settings{Enabled: true}, want: metricSourceCH},
		{name: "enabled with a url", cfg: &vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428"}, want: metricSourceVM},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{}
			if tc.cfg != nil {
				s.vmetrics = vmetrics.New()
				s.vmetrics.Configure(*tc.cfg)
			}
			if got := s.metricSource().Name(); got != tc.want {
				t.Fatalf("metricSource() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A VM failure is 502 with VM's own text, not 500. Counting it as 500
// would both blame Coremetry in the UI and inflate coremetry-api's
// self-observed error rate into a false anomaly (v0.7.13).
func TestUpstreamErrorIs502(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, upstream(errors.New("dial tcp 10.0.0.5:8428: connect: connection refused")))
	if rec.Code != 502 {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "connection refused") {
		t.Fatalf("VM's own error text was swallowed: %s", rec.Body.String())
	}

	// A plain error stays 500 — the classification must not leak.
	rec = httptest.NewRecorder()
	writeErr(rec, errors.New("some clickhouse problem"))
	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// A query we cannot TRANSLATE is 400, not 502. 502 would tell the operator
// their VictoriaMetrics is broken and send them to check a healthy
// cluster — a wrong diagnosis is worse than a blunt one.
func TestUntranslatableQueryIs400Not502(t *testing.T) {
	v := vmMetricSource{vmetrics.New()}
	v.svc.Configure(vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428"})

	// agg=p99 is refused by the translator BEFORE any HTTP happens, so this
	// needs no live VM.
	//
	// v0.9.1154 — this case used to be agg=last, which now TRANSLATES (Faz
	// 1.5). The premise going stale was invisible in the assertions: the
	// query simply reached HTTP, failed to dial vm:8428 and came back tagged
	// errUpstream, so the test's own errUpstream guard is what caught it.
	// The histogram percentile is the refusal that survives Faz 1.5, and it
	// is also the one operators actually meet — metricTemplates.ts hands p99
	// to every histogram family it recognises.
	_, err := v.QueryMetric(context.Background(), chstore.MetricQueryFilter{
		Name: "http.server.request.duration", Aggregation: "p99",
	})
	if err == nil {
		t.Fatal("want a refusal for an unsupported aggregation")
	}
	if errors.Is(err, errUpstream) {
		t.Fatal("a translation refusal must NOT be tagged errUpstream — it would 502 and " +
			"blame a healthy VictoriaMetrics")
	}
	if !errors.Is(err, errBadRequest) {
		t.Fatalf("refusal not tagged errBadRequest: %v", err)
	}

	rec := httptest.NewRecorder()
	writeErr(rec, err)
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	// The message must name the supported set so the operator can fix it
	// without reading the source, and — for the percentile — say WHERE the
	// missing piece lives, or "unsupported" reads as "never".
	body := rec.Body.String()
	for _, want := range []string{
		"avg", "sum", "min", "max", "count", "last", "rate", "increase",
		"histogram", "Faz 2",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("error body does not mention %q: %s", want, body)
		}
	}

	// A filter operator with no MetricsQL matcher takes the same path.
	_, err = v.QueryMetric(context.Background(), chstore.MetricQueryFilter{
		Name:    "m",
		Filters: []chstore.FilterExpr{{Key: "n", Op: ">", Values: []string{"5"}}},
	})
	if !errors.Is(err, errBadRequest) {
		t.Fatalf("filter refusal not tagged errBadRequest: %v", err)
	}
}

func TestUpstreamPreservesNilAndWraps(t *testing.T) {
	if upstream(nil) != nil {
		t.Fatal("upstream(nil) must stay nil — a nil error wrapped into a non-nil one 502s a successful read")
	}
	inner := errors.New("boom")
	got := upstream(inner)
	if !errors.Is(got, errUpstream) {
		t.Fatal("wrapped error must match errUpstream")
	}
	if !errors.Is(got, inner) {
		t.Fatal("wrapped error must still match the inner cause")
	}
}

// vmMetricSource must tag EVERY method's error. An untagged one 500s and
// reads as a Coremetry bug.
func TestVMSourceTagsEveryError(t *testing.T) {
	// Unconfigured service → every read returns its own "not configured"
	// error, which is the cheapest way to exercise all four paths without
	// a live VM.
	v := vmMetricSource{vmetrics.New()}
	ctx := context.Background()

	_, _, errNames := v.ListMetricNames(ctx, "", "", 10, 0)
	_, errQuery := v.QueryMetric(ctx, chstore.MetricQueryFilter{Name: "m"})
	_, errLabels := v.MetricLabelValues(ctx, "m", "pod", time.Hour)
	_, errKeys := v.MetricAttrKeys(ctx, "m", "", time.Hour)

	for name, err := range map[string]error{
		"ListMetricNames":   errNames,
		"QueryMetric":       errQuery,
		"MetricLabelValues": errLabels,
		"MetricAttrKeys":    errKeys,
	} {
		if err == nil {
			t.Fatalf("%s: want an error from an unconfigured backend, got nil", name)
		}
		if !errors.Is(err, errUpstream) {
			t.Errorf("%s: error is not tagged errUpstream (%v) — it would surface as a 500", name, err)
		}
	}
}

// Compile-time proof that both adapters satisfy the seam. If chstore or
// vmetrics drifts on a signature this line fails to build, which is the
// entire reason the two method sets were made identical.
var _ = []metricSource{chMetricSource{}, vmMetricSource{}}

func TestSourceNamesAreStable(t *testing.T) {
	// These strings ride in cache keys AND in the /api/metrics/names
	// response the frontend badge reads. Changing one silently invalidates
	// every cached key and un-badges the catalogue.
	if metricSourceCH != "ch" || metricSourceVM != "vm" {
		t.Fatalf("source names changed: %q / %q", metricSourceCH, metricSourceVM)
	}
	if got := fmt.Sprintf("%s|%s", chMetricSource{}.Name(), vmMetricSource{}.Name()); got != "ch|vm" {
		t.Fatalf("adapter names = %q", got)
	}
}
