package promql

import (
	"testing"
	"time"
)

// Phase 1 (v0.9.111) — the parser ships on its own, so its tests are the
// safety net. Three angles: (1) round-trip — Parse(x).String() must render the
// canonical PromQL, and re-parsing that must be stable; (2) structural — the
// precedence/associativity corners PromQL pins (`-2^2 == -4`, left-assoc `-`,
// right-assoc `^`); (3) errors — malformed input must fail, not mis-parse.

func TestParseRoundTrip(t *testing.T) {
	cases := []struct{ in, want string }{
		// selectors + matchers
		{`up`, `up`},
		{`http_requests_total{job="api"}`, `http_requests_total{job="api"}`},
		{`http_requests_total{job="api",method!="GET"}`, `http_requests_total{job="api",method!="GET"}`},
		{`{__name__="up",job=~"a.*"}`, `up{job=~"a.*"}`}, // __name__ hoisted to the name
		{`foo{a!~"x"}`, `foo{a!~"x"}`},
		// range + offset + @
		{`rate(http_requests_total[5m])`, `rate(http_requests_total[5m])`},
		{`foo[1h30m]`, `foo[1h30m]`},
		{`foo offset 5m`, `foo offset 5m`},
		{`foo offset -1h`, `foo offset -1h`},
		{`rate(foo[5m] offset 1h)`, `rate(foo[5m] offset 1h)`},
		{`foo @ 1609746000`, `foo @ 1609746000`},
		{`foo @ start()`, `foo @ start()`},
		// functions
		{`histogram_quantile(0.95, foo)`, `histogram_quantile(0.95, foo)`},
		{`clamp_max(rate(foo[5m]), 100)`, `clamp_max(rate(foo[5m]), 100)`},
		{`label_replace(foo, "svc", "$1", "service", "(.*)")`, `label_replace(foo, "svc", "$1", "service", "(.*)")`},
		// aggregations — grouping canonicalizes to BEFORE the parens
		{`sum by (le) (rate(foo[5m]))`, `sum by (le) (rate(foo[5m]))`},
		{`sum(rate(foo[5m])) by (le)`, `sum by (le) (rate(foo[5m]))`},
		{`sum without (instance) (foo)`, `sum without (instance) (foo)`},
		{`topk(5, foo)`, `topk(5, foo)`},
		{`quantile(0.9, foo)`, `quantile(0.9, foo)`},
		// arithmetic / precedence rendering (no added parens)
		{`a + b * c`, `a + b * c`},
		{`a * b + c`, `a * b + c`},
		{`rate(a[5m]) / rate(b[5m])`, `rate(a[5m]) / rate(b[5m])`},
		{`(a + b) * c`, `(a + b) * c`},
		{`-foo`, `-foo`},
		{`-2`, `-2`},
		// binary modifiers
		{`a / on (le) b`, `a / on (le) b`},
		{`a / ignoring (pod) group_left (svc) b`, `a / ignoring (pod) group_left (svc) b`},
		{`rate(foo[5m]) > bool 0.5`, `rate(foo[5m]) > bool 0.5`},
		{`up == 1`, `up == 1`},
		// the canonical APM query
		{`histogram_quantile(0.95, sum(rate(http_server_duration[5m])) by (le))`,
			`histogram_quantile(0.95, sum by (le) (rate(http_server_duration[5m])))`},
		// subquery
		{`rate(foo[5m])[1h:1m]`, `rate(foo[5m])[1h:1m]`},
		{`max_over_time(rate(foo[5m])[30m:])`, `max_over_time(rate(foo[5m])[30m:])`},
	}
	for _, c := range cases {
		e, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q) error: %v", c.in, err)
			continue
		}
		if got := e.String(); got != c.want {
			t.Errorf("Parse(%q).String() = %q, want %q", c.in, got, c.want)
			continue
		}
		// Re-parsing the rendered form must be stable (idempotent String).
		e2, err := Parse(e.String())
		if err != nil {
			t.Errorf("re-Parse(%q) error: %v", e.String(), err)
			continue
		}
		if e2.String() != e.String() {
			t.Errorf("String not idempotent: %q → %q", e.String(), e2.String())
		}
	}
}

// TestParseAdversarial — curated from a 5-agent adversarial-case workflow
// (v0.9.112 verify): 126 cases run against the parser, 0 mismatches. These are
// the highest-value edge cases (dotted OTel names — the Coremetry extension,
// number/string lexing corners, unary folding, week normalization) locked in
// as permanent regressions.
func TestParseAdversarial(t *testing.T) {
	ok := []struct{ in, want string }{
		{`http.server.duration{service.name="checkout"}`, `http.server.duration{service.name="checkout"}`}, // dotted metric + dotted label
		{`foo[1w]`, `foo[7d]`},          // week → days (no 'w' in re-render)
		{`up # {curly} "quote =~`, `up`}, // comment to EOL discarded
		{`-inf`, `-Inf`},                // case-insensitive inf, canonical caps
		{`NaN`, `NaN`},
		{`0x1f`, `31`},     // hex → decimal
		{`.5`, `0.5`},      // leading-dot float
		{`1.5e-3`, `0.0015`}, // signed exponent
		{"`a\\tb`", `"a\\tb"`},  // raw backtick string: literal backslash
		{`+1`, `1`},        // unary plus folds away
		{`- -1`, `1`},      // double negation folds
		{`2^-2`, `2 ^ -2`}, // signed exponent RHS
	}
	for _, c := range ok {
		e, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q) error: %v", c.in, err)
			continue
		}
		if got := e.String(); got != c.want {
			t.Errorf("Parse(%q).String() = %q, want %q", c.in, got, c.want)
		}
	}
	// Must be rejected (lexer/parser errors), never mis-parsed or panicked.
	for _, bad := range []string{
		`5m`,     // bare duration is not an expression
		`5m2`,    // trailing digit invalidates the duration
		`5.5m`,   // fractional duration illegal
		`.foo`,   // '.' is continuation-only, never a start char
		`http.x:sum`, // ':' excluded from names (no recording rules)
		`!foo`,   // stray '!'
		`"oops`,  // unterminated string
	} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) = nil error, want error", bad)
		}
	}
}

func TestParsePrecedence(t *testing.T) {
	// `^` binds tighter than unary minus → -(2^2). Structural: root is Unary.
	e, err := Parse(`-2^2`)
	if err != nil {
		t.Fatalf("parse -2^2: %v", err)
	}
	u, ok := e.(*UnaryExpr)
	if !ok {
		t.Fatalf("-2^2 root = %T, want *UnaryExpr (unary looser than ^)", e)
	}
	if _, ok := u.Expr.(*BinaryExpr); !ok {
		t.Fatalf("-2^2 operand = %T, want *BinaryExpr(^)", u.Expr)
	}

	// `-` is left-associative: 1-2-3 → (1-2)-3.
	e, _ = Parse(`1 - 2 - 3`)
	be, ok := e.(*BinaryExpr)
	if !ok || be.Op != "-" {
		t.Fatalf("1-2-3 root = %T/%v, want BinaryExpr(-)", e, e)
	}
	if l, ok := be.LHS.(*BinaryExpr); !ok || l.Op != "-" {
		t.Fatalf("1-2-3 LHS = %T, want BinaryExpr(-) (left-assoc)", be.LHS)
	}

	// `^` is right-associative: 2^3^2 → 2^(3^2).
	e, _ = Parse(`2 ^ 3 ^ 2`)
	be, _ = e.(*BinaryExpr)
	if be == nil || be.Op != "^" {
		t.Fatalf("2^3^2 root not ^")
	}
	if r, ok := be.RHS.(*BinaryExpr); !ok || r.Op != "^" {
		t.Fatalf("2^3^2 RHS = %T, want BinaryExpr(^) (right-assoc)", be.RHS)
	}

	// `*` binds tighter than `+`: a+b*c → a+(b*c).
	e, _ = Parse(`a + b * c`)
	be, _ = e.(*BinaryExpr)
	if be == nil || be.Op != "+" {
		t.Fatalf("a+b*c root not +")
	}
	if r, ok := be.RHS.(*BinaryExpr); !ok || r.Op != "*" {
		t.Fatalf("a+b*c RHS = %T, want BinaryExpr(*)", be.RHS)
	}

	// comparison binds looser than arithmetic: a+b == c → (a+b)==c.
	e, _ = Parse(`a + b == c`)
	be, _ = e.(*BinaryExpr)
	if be == nil || be.Op != "==" {
		t.Fatalf("a+b==c root = %v, want ==", e)
	}
}

func TestParseSelectorFields(t *testing.T) {
	e, err := Parse(`http_server_duration{service="checkout",code=~"5.."} offset 10m`)
	if err != nil {
		t.Fatal(err)
	}
	vs, ok := e.(*VectorSelector)
	if !ok {
		t.Fatalf("got %T", e)
	}
	if vs.Name != "http_server_duration" {
		t.Errorf("name = %q", vs.Name)
	}
	if len(vs.Matchers) != 2 {
		t.Fatalf("matchers = %d, want 2", len(vs.Matchers))
	}
	if vs.Offset != 10*time.Minute {
		t.Errorf("offset = %v, want 10m", vs.Offset)
	}
	if vs.Matchers[1].Type != MatchRegexp {
		t.Errorf("second matcher type = %v, want =~", vs.Matchers[1].Type)
	}
}

func TestParseMatrixRange(t *testing.T) {
	e, err := Parse(`rate(foo[5m])`)
	if err != nil {
		t.Fatal(err)
	}
	call, ok := e.(*Call)
	if !ok || call.Func != "rate" {
		t.Fatalf("got %T", e)
	}
	ms, ok := call.Args[0].(*MatrixSelector)
	if !ok {
		t.Fatalf("arg = %T, want *MatrixSelector", call.Args[0])
	}
	if ms.Range != 5*time.Minute {
		t.Errorf("range = %v, want 5m", ms.Range)
	}
}

func TestParseAggregateParam(t *testing.T) {
	e, err := Parse(`topk(3, sum by (svc) (rate(foo[5m])))`)
	if err != nil {
		t.Fatal(err)
	}
	agg, ok := e.(*AggregateExpr)
	if !ok || agg.Op != "topk" {
		t.Fatalf("got %T", e)
	}
	pn, ok := agg.Param.(*NumberLiteral)
	if !ok || pn.Val != 3 {
		t.Errorf("param = %v, want 3", agg.Param)
	}
	if _, ok := agg.Expr.(*AggregateExpr); !ok {
		t.Errorf("inner = %T, want nested *AggregateExpr", agg.Expr)
	}
}

func TestParseErrors(t *testing.T) {
	bad := []string{
		``,                       // empty
		`(`,                      // unclosed paren
		`foo{`,                   // unclosed brace
		`foo{bar}`,               // matcher without operator
		`foo{bar=}`,              // matcher without value
		`rate(foo[5m]`,           // unclosed call
		`sum by (`,               // unclosed grouping
		`1 +`,                    // dangling operator
		`foo bar`,                // two exprs
		`[5m]`,                   // range with no selector
		`histogram_quantile(0.9`, // unclosed
		`foo @ `,                 // @ without timestamp
		`foo offset`,             // offset without duration
		`!foo`,                   // stray !
		`foo{a="b"} baz`,         // trailing junk
	}
	for _, q := range bad {
		if _, err := Parse(q); err == nil {
			t.Errorf("Parse(%q) = nil error, want a parse error", q)
		}
	}
}

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"5m", 5 * time.Minute},
		{"1h", time.Hour},
		{"1h30m", 90 * time.Minute},
		{"500ms", 500 * time.Millisecond},
		{"2d", 48 * time.Hour},
		{"1w", 7 * 24 * time.Hour},
		{"90s", 90 * time.Second},
	}
	for _, c := range cases {
		got, err := ParseDuration(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "5", "5x", "m", "5.5m", "5min"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%q) = nil error, want error", bad)
		}
	}
}

func TestParseDepthGuard(t *testing.T) {
	// 500-deep parens must be rejected cleanly (not a stack overflow / OOM).
	deep := ""
	for i := 0; i < 500; i++ {
		deep += "("
	}
	deep += "up"
	for i := 0; i < 500; i++ {
		deep += ")"
	}
	if _, err := Parse(deep); err == nil {
		t.Errorf("500-deep parens should error (depth guard)")
	}
	// A deep unary chain likewise.
	if _, err := Parse("--------------------------------------------------------------------------------------------------------------------------------------up"); err == nil {
		t.Errorf("deep unary chain should error")
	}
	// Normal shallow nesting still parses.
	if _, err := Parse("((up + 1) * 2)"); err != nil {
		t.Errorf("shallow nesting must still parse: %v", err)
	}
}
