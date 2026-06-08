package acache

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// TestStaticPolicyClassify pins the allowlist/denylist/default routing — the
// knob that lets a new attribute be a config change, not a code change.
func TestStaticPolicyClassify(t *testing.T) {
	p := NewStaticPolicy(
		[]string{"http.route", "db.system"},
		[]string{"trace_id", "k8s.pod.name"},
		CardHLL,
	)
	cases := []struct {
		key  string
		want Card
	}{
		{"http.route", CardTrack},
		{"db.system", CardTrack},
		{"trace_id", CardHLL},
		{"k8s.pod.name", CardHLL},
		{"some.unknown.key", CardHLL}, // default
	}
	for _, c := range cases {
		if got := p.Classify(c.key); got != c.want {
			t.Errorf("Classify(%q) = %d, want %d", c.key, got, c.want)
		}
	}
}

// TestStaticPolicyDefaultSkip verifies the default can be flipped to Skip so an
// operator who wants zero unclassified-key tracking gets exactly that.
func TestStaticPolicyDefaultSkip(t *testing.T) {
	p := NewStaticPolicy([]string{"http.route"}, nil, CardSkip)
	if got := p.Classify("random.key"); got != CardSkip {
		t.Errorf("default Classify = %d, want CardSkip", got)
	}
	if got := p.Classify("http.route"); got != CardTrack {
		t.Errorf("allowlisted Classify = %d, want CardTrack", got)
	}
}

// TestTrimStop guards the ZREMRANGEBYRANK top-N math: to keep the top N highest
// scores you remove ranks [0 .. -(N+1)]. For N=1000 the stop is -1001 (matches
// the spec). An off-by-one here silently truncates or never trims.
func TestTrimStop(t *testing.T) {
	cases := []struct {
		n    int
		want int64
	}{
		{1000, -1001},
		{1, -2},
		{50, -51},
	}
	for _, c := range cases {
		if got := trimStop(c.n); got != c.want {
			t.Errorf("trimStop(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}

// TestMatchPattern covers the picker matching contract: case-insensitive
// substring for bare terms, glob for * / ?, and '/' treated as a normal rune
// (http.route values contain slashes).
func TestMatchPattern(t *testing.T) {
	cases := []struct {
		value, pattern string
		want           bool
	}{
		{"payment-service", "pay", true},      // substring
		{"payment-service", "PAY", true},      // case-insensitive
		{"payment-service", "ment", true},     // mid substring
		{"payment-service", "xyz", false},     // no match
		{"payment-service", "pay*", true},     // prefix glob
		{"payment-service", "*service", true}, // suffix glob
		{"payment-service", "*ment*", true},   // contains glob
		{"payment-service", "p?yment*", true}, // single-char wildcard
		{"payment-service", "p?yment", false}, // glob is full-match, not substring
		{"/api/v1/pay", "/api/*", true},       // slash is a normal rune
		{"/api/v1/pay", "*/pay", true},
		{"GET", "g?t", true},
	}
	for _, c := range cases {
		if got := matchPattern(c.value, c.pattern); got != c.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", c.value, c.pattern, got, c.want)
		}
	}
}

// TestFilterByPatternOrder confirms filtering preserves input (rank) order so
// the frequency ordering from ZREVRANGE survives prefix filtering.
func TestFilterByPatternOrder(t *testing.T) {
	in := []string{"payments", "payroll", "auth", "paygate"}
	got := filterByPattern(in, "pay")
	want := []string{"payments", "payroll", "paygate"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order mismatch: got %v, want %v", got, want)
		}
	}
}

// TestObserveSpanAggregates verifies the hot-path folding: N spans collapse to
// per-(service,op,value) deltas, dedicated columns map to semconv keys, and the
// policy routes values to Track vs HLL vs Skip. This is the local
// pre-aggregation that turns N ZINCRBY +1 into one ZINCRBY +N.
func TestObserveSpanAggregates(t *testing.T) {
	pol := NewStaticPolicy(
		[]string{"http.route", "http.method", "http.status_code"},
		[]string{"trace_id"},
		CardSkip,
	)
	s := NewStore(nil, Options{Policy: pol}) // nil client: disabled for I/O
	// Disabled stores short-circuit ObserveSpan, so flip the flag off to
	// exercise the aggregator directly (white-box test).
	s.disabled = false

	mk := func(svc, op, route, method string, status uint16) *chstore.Span {
		return &chstore.Span{
			ServiceName: svc, Name: op, HTTPRoute: route, HTTPMethod: method, HTTPStatus: status,
			AttrKeys:   []string{"trace_id", "custom.tag"},
			AttrValues: []string{"abc123", "v1"},
		}
	}
	s.ObserveSpan(mk("checkout", "POST /pay", "/pay", "POST", 200))
	s.ObserveSpan(mk("checkout", "POST /pay", "/pay", "POST", 200))
	s.ObserveSpan(mk("checkout", "GET /cart", "/cart", "GET", 500))

	a := s.cur
	if a.services["checkout"] != 3 {
		t.Errorf("service delta = %d, want 3", a.services["checkout"])
	}
	if a.ops["checkout"]["POST /pay"] != 2 {
		t.Errorf("op delta = %d, want 2", a.ops["checkout"]["POST /pay"])
	}
	if a.attrVals["http.route"]["/pay"] != 2 {
		t.Errorf("http.route /pay delta = %d, want 2", a.attrVals["http.route"]["/pay"])
	}
	if a.attrVals["http.status_code"]["500"] != 1 {
		t.Errorf("http.status_code 500 delta = %d, want 1", a.attrVals["http.status_code"]["500"])
	}
	// trace_id is high-cardinality → HLL set, never a value ZSET.
	if _, ok := a.attrVals["trace_id"]; ok {
		t.Error("trace_id must not land in attrVals (it's CardHLL)")
	}
	if len(a.attrHLL["trace_id"]) != 1 { // one distinct value abc123
		t.Errorf("trace_id HLL distinct = %d, want 1", len(a.attrHLL["trace_id"]))
	}
	// custom.tag defaults to CardSkip → key recorded, no value stored.
	if _, ok := a.keys["custom.tag"]; !ok {
		t.Error("custom.tag key should be recorded")
	}
	if _, ok := a.attrVals["custom.tag"]; ok {
		t.Error("custom.tag is CardSkip → no value tracking")
	}
	// Every attribute key seen must be in the keys set.
	for _, k := range []string{"http.route", "http.method", "http.status_code", "trace_id", "custom.tag"} {
		if _, ok := a.keys[k]; !ok {
			t.Errorf("attr key %q missing from keys set", k)
		}
	}
}

// TestObserveSpanNoDoubleCount guards the review's MAJOR finding: the OTLP
// converter keeps well-known attrs in BOTH the dedicated column AND the
// AttrKeys/AttrValues arrays. ObserveSpan must count such a value ONCE (from
// the column), not twice, or its frequency rank is inflated. db.statement is
// NOT a harvested column, so it must still flow through the array.
func TestObserveSpanNoDoubleCount(t *testing.T) {
	pol := NewStaticPolicy([]string{"http.method", "db.statement"}, nil, CardHLL)
	s := NewStore(nil, Options{Policy: pol})
	s.disabled = false

	// http.method appears as a dedicated column AND in the attr arrays (as the
	// converter actually stores it); db.statement only in the arrays.
	s.ObserveSpan(&chstore.Span{
		ServiceName: "svc",
		HTTPMethod:  "GET",
		AttrKeys:    []string{"http.method", "db.statement"},
		AttrValues:  []string{"GET", "SELECT 1"},
	})

	if got := s.cur.attrVals["http.method"]["GET"]; got != 1 {
		t.Errorf("http.method GET delta = %d, want 1 (double-count guard)", got)
	}
	if got := s.cur.attrVals["db.statement"]["SELECT 1"]; got != 1 {
		t.Errorf("db.statement delta = %d, want 1 (must flow through arrays)", got)
	}
}

// TestObserveSpanMaxValLen confirms oversized values are dropped (a guard
// against a mis-allowlisted high-cardinality blob bloating the aggregator).
func TestObserveSpanMaxValLen(t *testing.T) {
	pol := NewStaticPolicy([]string{"big.attr"}, nil, CardSkip)
	s := NewStore(nil, Options{Policy: pol, MaxValLen: 8})
	s.disabled = false
	big := "0123456789" // len 10 > 8
	s.ObserveSpan(&chstore.Span{
		ServiceName: "svc", AttrKeys: []string{"big.attr"}, AttrValues: []string{big},
	})
	if _, ok := s.cur.attrVals["big.attr"]; ok {
		t.Error("oversized value should be ignored")
	}
}

// TestDisabledStoreIsNoop verifies the graceful-degradation contract: a nil
// client makes every write a no-op and every read a miss.
func TestDisabledStoreIsNoop(t *testing.T) {
	s := NewStore(nil, Options{})
	if s.Enabled() {
		t.Fatal("nil-client store must report disabled")
	}
	s.ObserveSpan(&chstore.Span{ServiceName: "svc", Name: "op"})
	if !s.cur.empty() {
		t.Error("disabled store must not accumulate")
	}
	if _, _, hit := s.GetServices(nil, "", 10); hit {
		t.Error("disabled GetServices must miss")
	}
	if _, hit := s.GetAttributeKeys(nil); hit {
		t.Error("disabled GetAttributeKeys must miss")
	}
	if _, _, _, hit := s.GetAttributeValues(nil, "k", "", 10); hit {
		t.Error("disabled GetAttributeValues must miss")
	}
}

func TestClampLimit(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 200}, {-5, 200}, {50, 50}, {1000, 1000}, {5000, 1000},
	}
	for _, c := range cases {
		if got := clampLimit(c.in); got != c.want {
			t.Errorf("clampLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
