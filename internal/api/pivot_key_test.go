package api

// pivot_key_test.go — pins the v0.8.330 pivot query layer's pure seams:
// the /api/exemplars cache key (pivotExemplarKey / pivotFPDigest), the
// minute bucketing, the ?window_s= clamp and the 32-hex trace-id gate.
//
// The key tests are the v0.5.187 anti-pattern guard, same class as
// cache_key_test.go / correlate_key_test.go: a fingerprint SET must digest by
// CONTENT (sorted + FNV), never by length or by insertion order — two
// distinct sets sharing a key cross-poison each other's cached exemplars,
// and an order-sensitive digest fragments the cache for identical charts.

import (
	"testing"
	"time"
)

func TestPivotFPDigest_OrderInvariant(t *testing.T) {
	// The same SET in any order must digest identically — the chart client
	// assembles fingerprints from map iteration, so order is never stable.
	cases := []struct {
		name string
		a, b []uint64
	}{
		{"pair swapped", []uint64{1, 2}, []uint64{2, 1}},
		{"triple rotated", []uint64{7, 42, 999}, []uint64{999, 7, 42}},
		{"large values", []uint64{18446744073709551615, 1}, []uint64{1, 18446744073709551615}},
		{"single", []uint64{5}, []uint64{5}},
	}
	for _, c := range cases {
		if da, db := pivotFPDigest(c.a), pivotFPDigest(c.b); da != db {
			t.Errorf("%s: order changed the digest: %q != %q (sort step removed?)", c.name, da, db)
		}
	}
}

func TestPivotFPDigest_DistinctSetsDistinctDigests(t *testing.T) {
	// The cross-poisoning class: sets that differ in ANY way — including
	// same-length sets (the len-based v0.5.187 failure) — must not collide.
	cases := []struct {
		name string
		a, b []uint64
	}{
		{"same length different content", []uint64{1, 2}, []uint64{1, 3}},
		{"subset vs superset", []uint64{1, 2}, []uint64{1, 2, 3}},
		{"empty vs one", nil, []uint64{0}},
		{"adjacent values", []uint64{100}, []uint64{101}},
		// Fixed-width encoding guard: {0x0102, 0x03} vs {0x01, 0x0203} would
		// collide under naive byte concatenation of variable-width encodings.
		{"concatenation ambiguity", []uint64{0x0102, 0x03}, []uint64{0x01, 0x0203}},
	}
	for _, c := range cases {
		if da, db := pivotFPDigest(c.a), pivotFPDigest(c.b); da == db {
			t.Errorf("%s: v0.5.187 regression — distinct sets %v and %v share digest %q", c.name, c.a, c.b, da)
		}
	}
}

func TestPivotFPDigest_Stable(t *testing.T) {
	first := pivotFPDigest([]uint64{3, 1, 2})
	for i := 0; i < 10; i++ {
		if got := pivotFPDigest([]uint64{3, 1, 2}); got != first {
			t.Fatalf("digest unstable on call %d: first=%q got=%q", i, first, got)
		}
	}
}

func TestPivotExemplarKey_HashesAllInputs(t *testing.T) {
	// Flip exactly one input at a time; the key must move each time — an
	// input that stops contributing silently serves another request's rows.
	base := func() string {
		return pivotExemplarKey([]uint64{1, 2}, "http.duration", "checkout", 100,
			time.Unix(600, 0), time.Unix(1200, 0))
	}
	b := base()
	cases := []struct {
		name string
		got  string
	}{
		{"fingerprints", pivotExemplarKey([]uint64{1, 3}, "http.duration", "checkout", 100, time.Unix(600, 0), time.Unix(1200, 0))},
		{"metric", pivotExemplarKey([]uint64{1, 2}, "db.duration", "checkout", 100, time.Unix(600, 0), time.Unix(1200, 0))},
		{"service", pivotExemplarKey([]uint64{1, 2}, "http.duration", "payments", 100, time.Unix(600, 0), time.Unix(1200, 0))},
		{"limit", pivotExemplarKey([]uint64{1, 2}, "http.duration", "checkout", 200, time.Unix(600, 0), time.Unix(1200, 0))},
		{"from", pivotExemplarKey([]uint64{1, 2}, "http.duration", "checkout", 100, time.Unix(0, 0), time.Unix(1200, 0))},
		{"to", pivotExemplarKey([]uint64{1, 2}, "http.duration", "checkout", 100, time.Unix(600, 0), time.Unix(1800, 0))},
	}
	for _, c := range cases {
		if c.got == b {
			t.Errorf("v0.5.187 regression: changing %s did NOT change the key — that input is not hashed", c.name)
		}
	}
	if again := base(); again != b {
		t.Fatalf("key unstable for identical inputs: %q != %q", again, b)
	}
}

func TestPivotExemplarKey_FingerprintOrderInvariant(t *testing.T) {
	a := pivotExemplarKey([]uint64{9, 4, 7}, "m", "s", 0, time.Unix(60, 0), time.Unix(120, 0))
	b := pivotExemplarKey([]uint64{4, 7, 9}, "m", "s", 0, time.Unix(60, 0), time.Unix(120, 0))
	if a != b {
		t.Fatalf("fingerprint order fragmented the cache: %q != %q", a, b)
	}
}

func TestPivotMinuteBucketing(t *testing.T) {
	// Times within the same minute share the bucket (concurrent triage
	// clicks share one upstream trip); crossing the minute boundary moves it.
	inMinuteA := time.Unix(60, 0)          // 00:01:00
	inMinuteB := time.Unix(119, 999999999) // 00:01:59.999…
	nextMin := time.Unix(120, 0)           // 00:02:00
	if pivotMinuteBucket(inMinuteA) != pivotMinuteBucket(inMinuteB) {
		t.Errorf("same-minute times bucketed apart: %d != %d",
			pivotMinuteBucket(inMinuteA), pivotMinuteBucket(inMinuteB))
	}
	if pivotMinuteBucket(inMinuteB) == pivotMinuteBucket(nextMin) {
		t.Errorf("minute boundary did not move the bucket: both %d", pivotMinuteBucket(nextMin))
	}

	// And through the REAL key: same minute → same key, next minute → new key.
	fp := []uint64{1}
	k1 := pivotExemplarKey(fp, "m", "s", 0, inMinuteA, inMinuteA.Add(time.Hour))
	k2 := pivotExemplarKey(fp, "m", "s", 0, inMinuteB, inMinuteB.Add(time.Hour))
	k3 := pivotExemplarKey(fp, "m", "s", 0, nextMin, nextMin.Add(time.Hour))
	if k1 != k2 {
		t.Errorf("same-minute windows should share the key: %q != %q", k1, k2)
	}
	if k2 == k3 {
		t.Errorf("next-minute window should get a fresh key: both %q", k2)
	}
}

func TestPivotWindowMetricsKey_HashesAllInputs(t *testing.T) {
	base := pivotWindowMetricsKey("checkout", time.Unix(600, 0), 900)
	cases := []struct {
		name string
		got  string
	}{
		{"service", pivotWindowMetricsKey("payments", time.Unix(600, 0), 900)},
		{"at (next minute)", pivotWindowMetricsKey("checkout", time.Unix(660, 0), 900)},
		{"window", pivotWindowMetricsKey("checkout", time.Unix(600, 0), 600)},
	}
	for _, c := range cases {
		if c.got == base {
			t.Errorf("v0.5.187 regression: changing %s did NOT change the key", c.name)
		}
	}
	// Same-minute anchors share the key (the bucketing contract).
	if pivotWindowMetricsKey("checkout", time.Unix(630, 0), 900) != base {
		t.Errorf("same-minute anchor fragmented the window-metrics key")
	}
}

func TestPivotWindowClamp(t *testing.T) {
	// Every rung: absent, garbage, negative, zero, below floor, floor,
	// in-range, ceiling, above ceiling — the [60, 3600] / default-900 contract.
	cases := []struct {
		raw  string
		want int
	}{
		{"", 900},        // absent → default ±15m
		{"abc", 900},     // garbage → default
		{"-5", 900},      // negative → default
		{"0", 900},       // zero → default
		{"1", 60},        // below floor → clamp up
		{"59", 60},       // just below floor
		{"60", 60},       // floor exact
		{"900", 900},     // default passed explicitly
		{"1800", 1800},   // in range
		{"3600", 3600},   // ceiling exact
		{"3601", 3600},   // above ceiling → clamp down
		{"999999", 3600}, // way above → clamp down
	}
	for _, c := range cases {
		if got := pivotWindowClamp(c.raw); got != c.want {
			t.Errorf("pivotWindowClamp(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}

func TestIsHex32(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"0123456789abcdef0123456789abcdef", true},
		{"ffffffffffffffffffffffffffffffff", true},
		{"0123456789abcdef0123456789abcde", false},   // 31 chars
		{"0123456789abcdef0123456789abcdef0", false}, // 33 chars
		{"0123456789ABCDEF0123456789ABCDEF", false},  // uppercase (handler lowercases first)
		{"0123456789abcdef0123456789abcdeg", false},  // non-hex char
		{"", false},
		{"'; DROP TABLE spans; --", false},
	}
	for _, c := range cases {
		if got := isHex32(c.in); got != c.want {
			t.Errorf("isHex32(%q) = %t, want %t", c.in, got, c.want)
		}
	}
}

// TestPivotSeriesExemplarKey — v0.8.432 (audit Faz B). The by-series
// key must hash ALL inputs (v0.5.187 rule): groupBy rides a digest that
// is order-invariant but set-distinct; filters raw JSON folds in; the
// window stays minute-bucketed like the sibling key.
func TestPivotSeriesExemplarKey(t *testing.T) {
	from := time.Unix(1_700_000_000, 0)
	to := from.Add(30 * time.Minute)

	a := pivotSeriesExemplarKey("m", "svc", []string{"host.name", "region"}, `[]`, 50, from, to)
	b := pivotSeriesExemplarKey("m", "svc", []string{"region", "host.name"}, `[]`, 50, from, to)
	if a != b {
		t.Fatalf("groupBy order must not change the key:\n%s\n%s", a, b)
	}
	c := pivotSeriesExemplarKey("m", "svc", []string{"host.name"}, `[]`, 50, from, to)
	if a == c {
		t.Fatalf("distinct groupBy sets must produce distinct keys")
	}
	d := pivotSeriesExemplarKey("m", "svc", []string{"host.name", "region"}, `[{"k":"env"}]`, 50, from, to)
	if a == d {
		t.Fatalf("filters must fold into the key")
	}
	e := pivotSeriesExemplarKey("m2", "svc", []string{"host.name", "region"}, `[]`, 50, from, to)
	f := pivotSeriesExemplarKey("m", "svc2", []string{"host.name", "region"}, `[]`, 50, from, to)
	g := pivotSeriesExemplarKey("m", "svc", []string{"host.name", "region"}, `[]`, 99, from, to)
	for i, other := range []string{e, f, g} {
		if a == other {
			t.Fatalf("input %d must change the key", i)
		}
	}
	// Minute bucketing: seconds within the same minute share a key.
	// (minute-aligned base — 1_700_000_000 itself sits at :20s)
	base := time.Unix(1_699_999_980, 0)
	h1 := pivotSeriesExemplarKey("m", "svc", nil, `[]`, 50, base.Add(10*time.Second), base.Add(30*time.Minute+20*time.Second))
	h2 := pivotSeriesExemplarKey("m", "svc", nil, `[]`, 50, base.Add(40*time.Second), base.Add(30*time.Minute+50*time.Second))
	if h1 != h2 {
		t.Fatalf("window must be minute-bucketed:\n%s\n%s", h1, h2)
	}
}
