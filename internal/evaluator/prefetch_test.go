package evaluator

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.8.352 (perf P2-A) — the evaluator's per-(rule, service) CH reads
// (~70k queries/hour measured) collapsed into a per-tick batched prefetch.
// These tests pin the plumbing with a stub measurer (no CH):
//
//   - collectMeasureKeys extracts the DISTINCT (metric, window) pairs +
//     count windows, excluding disabled / log-query / sub-5m rules;
//   - prefetchMeasures issues exactly ONE call per pair and marks a failed
//     pair so its rules are skipped for the tick (never crashing the tick);
//   - absentMeasure reproduces the per-service query's empty-result
//     behavior per metric class (0 / NaN / skip) so a zero-traffic service
//     evaluates identically to the pre-batch code.

type stubBatch struct {
	measureCalls []measureKey
	countCalls   []int
	values       map[measureKey]map[string]float64
	counts       map[int]map[string]uint64
	failMeasure  map[measureKey]bool
	failCounts   map[int]bool
}

func (s *stubBatch) MeasureAllServices(_ context.Context, metric string, window time.Duration, _ time.Time) (map[string]float64, error) {
	k := measureKey{Metric: metric, WindowSec: int(window / time.Second)}
	s.measureCalls = append(s.measureCalls, k)
	if s.failMeasure[k] {
		return nil, errors.New("stub: CH unavailable")
	}
	return s.values[k], nil
}

func (s *stubBatch) MeasureCountAllServices(_ context.Context, window time.Duration, _ time.Time) (map[string]uint64, error) {
	w := int(window / time.Second)
	s.countCalls = append(s.countCalls, w)
	if s.failCounts[w] {
		return nil, errors.New("stub: CH unavailable")
	}
	return s.counts[w], nil
}

func TestCollectMeasureKeys(t *testing.T) {
	rules := []chstore.AlertRule{
		// Two rules sharing (error_rate, 300) — must dedup to one pair.
		{ID: "a", Metric: "error_rate", WindowSec: 300, MinSamples: 50, Enabled: true},
		{ID: "b", Metric: "error_rate", WindowSec: 300, MinSamples: 100, Enabled: true},
		{ID: "c", Metric: "http_p99_ms", WindowSec: 600, MinSamples: 100, Enabled: true},
		// Absolute metrics: measured, but NO count window even with
		// MinSamples set (v0.8.314 — the gate never runs for them).
		{ID: "d", Metric: "request_rate", WindowSec: 300, MinSamples: 100, Enabled: true},
		{ID: "e", Metric: "error_count", WindowSec: 600, MinSamples: 10, Enabled: true},
		// Excluded: disabled / log-query / sub-5m custom window.
		{ID: "f", Metric: "p99_ms", WindowSec: 300, Enabled: false},
		{ID: "g", Metric: "log_query", LogQuery: `level:"error"`, WindowSec: 300, Enabled: true},
		{ID: "h", Metric: "error_rate", WindowSec: 60, MinSamples: 50, Enabled: true},
	}
	measures, countWindows := collectMeasureKeys(rules)

	wantMeasures := []measureKey{
		{Metric: "error_count", WindowSec: 600},
		{Metric: "error_rate", WindowSec: 300},
		{Metric: "http_p99_ms", WindowSec: 600},
		{Metric: "request_rate", WindowSec: 300},
	}
	if !reflect.DeepEqual(measures, wantMeasures) {
		t.Fatalf("measures = %v, want %v", measures, wantMeasures)
	}
	// Count windows: error_rate@300 (sample-floor + MinSamples) and
	// http_p99_ms@600 — deduped, sorted, absolutes excluded.
	if !reflect.DeepEqual(countWindows, []int{300, 600}) {
		t.Fatalf("countWindows = %v, want [300 600]", countWindows)
	}
}

func TestPrefetchMeasuresPlumbing(t *testing.T) {
	rules := []chstore.AlertRule{
		{ID: "a", Metric: "error_rate", WindowSec: 300, MinSamples: 50, Enabled: true},
		{ID: "b", Metric: "http_p99_ms", WindowSec: 600, MinSamples: 100, Enabled: true},
		{ID: "c", Metric: "request_rate", WindowSec: 300, Enabled: true},
	}
	erKey := measureKey{Metric: "error_rate", WindowSec: 300}
	httpKey := measureKey{Metric: "http_p99_ms", WindowSec: 600}
	rrKey := measureKey{Metric: "request_rate", WindowSec: 300}

	stub := &stubBatch{
		values: map[measureKey]map[string]float64{
			erKey: {"payments": 12.5, "checkout": 0},
			rrKey: {"payments": 42},
		},
		counts: map[int]map[string]uint64{
			300: {"payments": 900},
			600: {"payments": 1800},
		},
		failMeasure: map[measureKey]bool{httpKey: true}, // partial failure
	}
	pre := prefetchMeasures(context.Background(), stub, rules, time.Now())

	// Exactly ONE batched call per distinct pair / count window.
	if len(stub.measureCalls) != 3 {
		t.Fatalf("measure calls = %v, want exactly 3 (one per distinct pair)", stub.measureCalls)
	}
	if !reflect.DeepEqual(stub.countCalls, []int{300, 600}) {
		t.Fatalf("count calls = %v, want [300 600]", stub.countCalls)
	}

	// Successful pair: values flow through.
	vals, failed, ok := pre.measureFor("error_rate", 300)
	if failed || !ok || vals["payments"] != 12.5 {
		t.Fatalf("measureFor(error_rate,300) = (%v, failed=%v, ok=%v)", vals, failed, ok)
	}
	// A present zero is a real measurement, distinct from an absent key.
	if v, present := vals["checkout"]; !present || v != 0 {
		t.Fatalf("checkout must be present with value 0, got (%v, %v)", v, present)
	}
	if _, present := vals["ghost-svc"]; present {
		t.Fatal("ghost-svc must be ABSENT (zero traffic) — absentMeasure handles it")
	}

	// Failed pair: marked, its rules skip this tick — the others survive.
	if _, failed, _ := pre.measureFor("http_p99_ms", 600); !failed {
		t.Fatal("http_p99_ms/600 prefetch failed — measureFor must report it")
	}
	if _, failed, ok := pre.measureFor("request_rate", 300); failed || !ok {
		t.Fatal("request_rate/300 must be unaffected by the http_p99_ms failure")
	}

	// Never-prefetched pair: not failed, not ok → per-service fallback.
	if _, failed, ok := pre.measureFor("db_p99_ms", 300); failed || ok {
		t.Fatal("un-prefetched pair must report (failed=false, ok=false)")
	}

	counts, failed, ok := pre.countFor(300)
	if failed || !ok || counts["payments"] != 900 {
		t.Fatalf("countFor(300) = (%v, failed=%v, ok=%v)", counts, failed, ok)
	}
	if _, failed, ok := pre.countFor(120); failed || ok {
		t.Fatal("un-prefetched count window must report (failed=false, ok=false)")
	}
}

func TestPrefetchCountFailureMarked(t *testing.T) {
	rules := []chstore.AlertRule{
		{ID: "a", Metric: "error_rate", WindowSec: 300, MinSamples: 50, Enabled: true},
	}
	stub := &stubBatch{
		values:     map[measureKey]map[string]float64{{Metric: "error_rate", WindowSec: 300}: {"svc": 1}},
		failCounts: map[int]bool{300: true},
	}
	pre := prefetchMeasures(context.Background(), stub, rules, time.Now())
	if _, failed, _ := pre.countFor(300); !failed {
		t.Fatal("failed count window must be marked so MinSamples-gated rules skip the tick")
	}
	// The measure itself still succeeded — rules without the gate keep going.
	if _, failed, ok := pre.measureFor("error_rate", 300); failed || !ok {
		t.Fatal("measure prefetch must be independent of the count failure")
	}
}

// TestAbsentMeasure pins the zero-traffic semantics: a service missing
// from the batched map must evaluate EXACTLY like the per-service query's
// empty result did — 0 for absolute counts (traffic-drop `request_rate <`
// rules MUST still fire on silent services), skip for ratios/MV-avg (the
// old NULL-scan error path), NaN for quantiles + raw transport avg (empty
// quantile()/avg() → NaN → compare() false → resolve branch reachable).
func TestAbsentMeasure(t *testing.T) {
	cases := []struct {
		metric   string
		value    float64
		nan      bool
		evaluate bool
	}{
		{"error_count", 0, false, true},
		{"request_rate", 0, false, true}, // the v0.8.314 traffic-drop class
		{"error_rate", 0, false, false},
		{"avg_ms", 0, false, false},
		{"p50_ms", 0, true, true},
		{"p95_ms", 0, true, true},
		{"p99_ms", 0, true, true},
		{"http_p99_ms", 0, true, true},
		{"db_p95_ms", 0, true, true},
		{"mq_consume_p99_ms", 0, true, true},
		{"db_avg_ms", 0, true, true}, // raw avg() over empty = NaN, evaluated
		{"http_5xx_rate", 0, false, false},
		{"db_error_rate", 0, false, false},
		{"mq_publish_count", 0, false, true},
		{"totally_unknown", 0, false, false},
	}
	for _, c := range cases {
		t.Run(c.metric, func(t *testing.T) {
			v, evaluate := absentMeasure(c.metric)
			if evaluate != c.evaluate {
				t.Fatalf("absentMeasure(%q) evaluate = %v, want %v", c.metric, evaluate, c.evaluate)
			}
			if c.nan {
				if !math.IsNaN(v) {
					t.Fatalf("absentMeasure(%q) = %v, want NaN", c.metric, v)
				}
			} else if v != c.value {
				t.Fatalf("absentMeasure(%q) = %v, want %v", c.metric, v, c.value)
			}
		})
	}
}
