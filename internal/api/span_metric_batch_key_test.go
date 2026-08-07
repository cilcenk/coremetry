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
	return spanMetricBatchKey(tFrom, tTo, 60, 0, 0, []string{"service.name"}, "", `service.name = "a"`, "",
		[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms")})
}

func TestSpanMetricBatchKeyMDP(t *testing.T) {
	// v0.9.391 — maxDataPoints key bileşeni: farklı genişlik bucket'ları
	// farklı çözünürlük ister; aynı diğer-girdilerle mdp=0 vs 1200 AYRI
	// key üretmeli (v0.5.187 cross-poisoning kuralının mdp hali).
	a := spanMetricBatchKey(tFrom, tTo, 0, 0, 0, nil, "", `service.name = "a"`, "", nil)
	b := spanMetricBatchKey(tFrom, tTo, 0, 1200, 0, nil, "", `service.name = "a"`, "", nil)
	if a == b {
		t.Fatalf("mdp farkı key'i değiştirmeli: %s", a)
	}
}

func TestSpanMetricBatchKey_EveryInputSeparates(t *testing.T) {
	base := baseKey()
	cases := map[string]string{
		"from": spanMetricBatchKey(tFrom+int64(time.Hour), tTo, 60, 0, 0, []string{"service.name"}, "", `service.name = "a"`, "",
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms")}),
		"to": spanMetricBatchKey(tFrom, tTo+int64(time.Hour), 60, 0, 0, []string{"service.name"}, "", `service.name = "a"`, "",
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms")}),
		"step": spanMetricBatchKey(tFrom, tTo, 300, 0, 0, []string{"service.name"}, "", `service.name = "a"`, "",
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms")}),
		"groupBy": spanMetricBatchKey(tFrom, tTo, 60, 0, 0, []string{"host.name"}, "", `service.name = "a"`, "",
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms")}),
		"filters": spanMetricBatchKey(tFrom, tTo, 60, 0, 0, []string{"service.name"}, `[{"k":"x"}]`, `service.name = "a"`, "",
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms")}),
		"dsl": spanMetricBatchKey(tFrom, tTo, 60, 0, 0, []string{"service.name"}, "", `service.name = "b"`, "",
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms")}),
		"agg field": spanMetricBatchKey(tFrom, tTo, 60, 0, 0, []string{"service.name"}, "", `service.name = "a"`, "",
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "latency_ms")}),
		"agg kind": spanMetricBatchKey(tFrom, tTo, 60, 0, 0, []string{"service.name"}, "", `service.name = "a"`, "",
			[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p95", "duration_ms")}),
		"extra agg": spanMetricBatchKey(tFrom, tTo, 60, 0, 0, []string{"service.name"}, "", `service.name = "a"`, "",
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
	a := spanMetricBatchKey(tFrom, tTo, 60, 0, 0, []string{"a", "bc"}, "", "", "", nil)
	b := spanMetricBatchKey(tFrom, tTo, 60, 0, 0, []string{"ab", "c"}, "", "", "", nil)
	if a == b {
		t.Fatal(`group-by ["a","bc"] and ["ab","c"] collided — separator missing`)
	}
	c := spanMetricBatchKey(tFrom, tTo, 60, 0, 0, nil, "x", "y", "", nil)
	d := spanMetricBatchKey(tFrom, tTo, 60, 0, 0, nil, "xy", "", "", nil)
	if c == d {
		t.Fatal("filters/dsl boundary collided — separator missing")
	}
	e := spanMetricBatchKey(tFrom, tTo, 60, 0, 0, nil, "", "", "", []chstore.SpanMetricAggSpec{agg("n", "a", "f")})
	f := spanMetricBatchKey(tFrom, tTo, 60, 0, 0, nil, "", "", "", []chstore.SpanMetricAggSpec{agg("n", "af", "")})
	if e == f {
		t.Fatal("agg field boundary collided — separator missing")
	}
}

// Order must not matter: the response is a map keyed by agg name, so two
// callers asking for the same metrics in a different order want the SAME
// entry. Same for group-by.
func TestSpanMetricBatchKey_OrderInsensitive(t *testing.T) {
	a := spanMetricBatchKey(tFrom, tTo, 60, 0, 0, []string{"a", "b"}, "", "", "",
		[]chstore.SpanMetricAggSpec{agg("rate", "rate", ""), agg("p99", "p99", "duration_ms")})
	b := spanMetricBatchKey(tFrom, tTo, 60, 0, 0, []string{"b", "a"}, "", "", "",
		[]chstore.SpanMetricAggSpec{agg("p99", "p99", "duration_ms"), agg("rate", "rate", "")})
	if a != b {
		t.Fatalf("reordering group-by/aggs must not change the key:\n %q\n %q", a, b)
	}
}

// The window is minute-bucketed on purpose: from/to arrive as now()-derived
// nanoseconds, so an unbucketed key would be unique per request and cache
// exactly nothing.
func TestSpanMetricBatchKey_WindowBucketedToMinute(t *testing.T) {
	base := spanMetricBatchKey(tFrom, tTo, 60, 0, 0, nil, "", "", "", nil)
	within := spanMetricBatchKey(tFrom+int64(37*time.Second), tTo+int64(11*time.Second), 60, 0, 0, nil, "", "", "", nil)
	if base != within {
		t.Fatal("sub-minute jitter must land on the same key, otherwise nothing ever caches")
	}
	next := spanMetricBatchKey(tFrom+int64(90*time.Second), tTo, 60, 0, 0, nil, "", "", "", nil)
	if base == next {
		t.Fatal("crossing a minute boundary must produce a new key")
	}
	if !strings.HasPrefix(base, "span-metric-batch:") {
		t.Fatalf("key must stay under its own prefix for invalidation; got %q", base)
	}
}

// v0.9.723 — rateWindow ANAHTARA girer (v0.5.187 kuralı). Bu pin
// olmadan write("rw", ...) satırı düşürülse tüm testler yeşil kalır
// ve v2 panel (rw=180) ile eski panel (rw=0) aynı girdiyi paylaşıp
// birbirini zehirlerdi — anahtarın önlemeye çalıştığı senaryonun
// kendisi (review bulgusu).
func TestSpanMetricBatchKeyRateWindow(t *testing.T) {
	a := spanMetricBatchKey(tFrom, tTo, 60, 0, 0, nil, "", `service.name = "a"`, "", nil)
	b := spanMetricBatchKey(tFrom, tTo, 60, 0, 180, nil, "", `service.name = "a"`, "", nil)
	if a == b {
		t.Fatal("rateWindow=0 ve 180 aynı cache key üretti — çapraz zehirlenme (v0.5.187)")
	}
}

// v0.9.761 — "kötüleşenler önce" saf sıralayıcı: tanımlı delta önce
// (azalan), prior'suzlar arkada calls DESC, eşitlikte (service,path),
// limit kesimi.
func TestSortEndpointsByP99Delta(t *testing.T) {
	rows := []chstore.EndpointRow{
		{Service: "s", Path: "/stabil", P99Ms: 100, PriorP99Ms: 100, Calls: 50},
		{Service: "s", Path: "/yeni", P99Ms: 30, Calls: 900}, // prior yok
		{Service: "s", Path: "/patlayan", P99Ms: 300, PriorP99Ms: 100, Calls: 10},
		{Service: "s", Path: "/iyilesen", P99Ms: 50, PriorP99Ms: 100, Calls: 70},
	}
	got := sortEndpointsByP99Delta(rows, 3)
	if len(got) != 3 {
		t.Fatalf("limit kesimi çalışmadı: %d", len(got))
	}
	if got[0].Path != "/patlayan" || got[1].Path != "/stabil" || got[2].Path != "/iyilesen" {
		t.Fatalf("sıra yanlış: %s, %s, %s", got[0].Path, got[1].Path, got[2].Path)
	}
}
