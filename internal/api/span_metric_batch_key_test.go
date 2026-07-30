package api

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.229 — /api/spans/metric-batch (the Service Overview's main payload)
// had no cache at all. Adding one puts it squarely in v0.5.187 territory:
// the inputs include a group-by SET and an agg SET, and summarising either
// by count — or concatenating without a separator — lets two different
// requests share an entry and serve each other's numbers. These tests pin
// the key's separation properties.

func agg(name, aggregation, field string) chstore.SpanMetricAggSpec {
	return chstore.SpanMetricAggSpec{Name: name, Aggregation: aggregation, Field: field}
}

const (
	tFrom = int64(1_700_000_000_000_000_000)
	tTo   = int64(1_700_003_600_000_000_000)
)

func baseKey() string {
	return spanMetricBatchKey(tFrom, tTo, 60, 0, []string{"service.name"}, "", `service.name = "a"`,
		[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms")})
}

func TestSpanMetricBatchKeyMDP(t *testing.T) {
	// v0.9.391 — maxDataPoints key bileşeni: farklı genişlik bucket'ları
	// farklı çözünürlük ister; aynı diğer-girdilerle mdp=0 vs 1200 AYRI
	// key üretmeli (v0.5.187 cross-poisoning kuralının mdp hali).
	a := spanMetricBatchKey(tFrom, tTo, 0, 0, nil, "", `service.name = "a"`, nil)
	b := spanMetricBatchKey(tFrom, tTo, 0, 1200, nil, "", `service.name = "a"`, nil)
	if a == b {
		t.Fatalf("mdp farkı key'i değiştirmeli: %s", a)
	}
}

func TestSpanMetricBatchKey_EveryInputSeparates(t *testing.T) {
	base := baseKey()
	cases := map[string]string{
		"from": spanMetricBatchKey(tFrom+int64(time.Hour), tTo, 60, 0, []string{"service.name"}, "", `service.name = "a"`,
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms")}),
		"to": spanMetricBatchKey(tFrom, tTo+int64(time.Hour), 60, 0, []string{"service.name"}, "", `service.name = "a"`,
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms")}),
		"step": spanMetricBatchKey(tFrom, tTo, 300, 0, []string{"service.name"}, "", `service.name = "a"`,
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms")}),
		"groupBy": spanMetricBatchKey(tFrom, tTo, 60, 0, []string{"host.name"}, "", `service.name = "a"`,
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms")}),
		"filters": spanMetricBatchKey(tFrom, tTo, 60, 0, []string{"service.name"}, `[{"k":"x"}]`, `service.name = "a"`,
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms")}),
		"dsl": spanMetricBatchKey(tFrom, tTo, 60, 0, []string{"service.name"}, "", `service.name = "b"`,
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms")}),
		"agg field": spanMetricBatchKey(tFrom, tTo, 60, 0, []string{"service.name"}, "", `service.name = "a"`,
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "latency_ms")}),
		"agg kind": spanMetricBatchKey(tFrom, tTo, 60, 0, []string{"service.name"}, "", `service.name = "a"`,
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p95", "duration_ms")}),
		"extra agg": spanMetricBatchKey(tFrom, tTo, 60, 0, []string{"service.name"}, "", `service.name = "a"`,
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms"), agg("p50", "p50", "duration_ms")}),
	}
	for name, got := range cases {
		if got == base {
			t.Fatalf("%s must change the key, got the same: %q", name, got)
		}
	}
}

// The v0.5.187 rule for strings: without a separator, moving a character
// across a boundary produces the same byte stream and therefore the same key.
func TestSpanMetricBatchKey_NoBoundaryCollision(t *testing.T) {
	a := spanMetricBatchKey(tFrom, tTo, 60, 0, []string{"a", "bc"}, "", "", nil)
	b := spanMetricBatchKey(tFrom, tTo, 60, 0, []string{"ab", "c"}, "", "", nil)
	if a == b {
		t.Fatal(`group-by ["a","bc"] and ["ab","c"] collided — separator missing`)
	}
	c := spanMetricBatchKey(tFrom, tTo, 60, 0, nil, "x", "y", nil)
	d := spanMetricBatchKey(tFrom, tTo, 60, 0, nil, "xy", "", nil)
	if c == d {
		t.Fatal("filters/dsl boundary collided — separator missing")
	}
	e := spanMetricBatchKey(tFrom, tTo, 60, 0, nil, "", "", []chstore.SpanMetricAggSpec{agg("n", "a", "f")})
	f := spanMetricBatchKey(tFrom, tTo, 60, 0, nil, "", "", []chstore.SpanMetricAggSpec{agg("n", "af", "")})
	if e == f {
		t.Fatal("agg field boundary collided — separator missing")
	}
}

// Order must not matter: the response is a map keyed by agg name, so two
// callers asking for the same metrics in a different order want the SAME
// entry. Same for group-by.
func TestSpanMetricBatchKey_OrderInsensitive(t *testing.T) {
	a := spanMetricBatchKey(tFrom, tTo, 60, 0, []string{"a", "b"}, "", "",
		[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms")})
	b := spanMetricBatchKey(tFrom, tTo, 60, 0, []string{"b", "a"}, "", "",
		[]chstore.SpanMetricAggSpec{agg("p99", "p99", "duration_ms"), agg("rate", "rate", "")})
	if a != b {
		t.Fatalf("reordering group-by/aggs must not change the key:\n %q\n %q", a, b)
	}
}

// The window is minute-bucketed on purpose: from/to arrive as now()-derived
// nanoseconds, so an unbucketed key would be unique per request and cache
// exactly nothing.
func TestSpanMetricBatchKey_WindowBucketedToMinute(t *testing.T) {
	base := spanMetricBatchKey(tFrom, tTo, 60, 0, nil, "", "", nil)
	within := spanMetricBatchKey(tFrom+int64(37*time.Second), tTo+int64(11*time.Second), 60, 0, nil, "", "", nil)
	if base != within {
		t.Fatal("sub-minute jitter must land on the same key, otherwise nothing ever caches")
	}
	next := spanMetricBatchKey(tFrom+int64(90*time.Second), tTo, 60, 0, nil, "", "", nil)
	if base == next {
		t.Fatal("crossing a minute boundary must produce a new key")
	}
	if !strings.HasPrefix(base, "span-metric-batch:") {
		t.Fatalf("key must stay under its own prefix for invalidation; got %q", base)
	}
}
