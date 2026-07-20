package promql

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// parse.go — recursive-descent PromQL parser. Explicit precedence levels
// (clearer than precedence-climbing for the unary/pow corner: PromQL says
// `-2^2 == -4`, i.e. `^` binds tighter than unary minus, and `^` is
// right-associative). Grammar mirrors Prometheus.
//
// Precedence, lowest → highest:
//   or  <  and/unless  <  ==/!=/</<=/>/>=  <  +/-  <  *//%/atan2  <  unary -/+  <  ^  <  postfix([range]/offset/@)  <  primary

// aggregators — identifiers that take the `by/without` grouping syntax.
var aggregators = map[string]bool{
	"sum": true, "avg": true, "min": true, "max": true, "count": true,
	"count_values": true, "stddev": true, "stdvar": true, "quantile": true,
	"topk": true, "bottomk": true, "group": true,
}

// aggregatorsWithParam — need a first scalar/string parameter.
var aggregatorsWithParam = map[string]bool{
	"quantile": true, "topk": true, "bottomk": true, "count_values": true,
}

// Parse turns a PromQL string into an AST. Returns a positioned error on
// syntax failure so the API can underline the offending token.
func Parse(input string) (Expr, error) {
	toks, err := lex(input)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, src: input}
	e, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.cur().kind != tEOF {
		return nil, p.errf("unexpected %q after a complete expression", p.cur().val)
	}
	return e, nil
}

type parser struct {
	toks []token
	pos  int
	src  string
}

func (p *parser) cur() token  { return p.toks[p.pos] }
func (p *parser) peek() token {
	if p.pos+1 < len(p.toks) {
		return p.toks[p.pos+1]
	}
	return p.toks[len(p.toks)-1] // EOF
}
func (p *parser) advance() token { t := p.toks[p.pos]; if t.kind != tEOF { p.pos++ }; return t }

func (p *parser) errf(format string, a ...any) error {
	return fmt.Errorf("promql: "+format+" (at position %d)", append(a, p.cur().pos)...)
}

func (p *parser) expect(k tokenKind, what string) (token, error) {
	if p.cur().kind != k {
		return token{}, p.errf("expected %s, got %q", what, p.cur().val)
	}
	return p.advance(), nil
}

// isKeyword reports whether the current token is the identifier keyword `kw`.
func (p *parser) isKeyword(kw string) bool {
	t := p.cur()
	return t.kind == tIdentifier && t.val == kw
}

// ── precedence levels ───────────────────────────────────────────────────────

func (p *parser) parseExpr() (Expr, error) { return p.parseOr() }

func (p *parser) parseOr() (Expr, error) {
	lhs, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.isKeyword("or") {
		p.advance()
		vm, err := p.parseVectorMatching(false)
		if err != nil {
			return nil, err
		}
		rhs, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		lhs = &BinaryExpr{Op: "or", LHS: lhs, RHS: rhs, VectorMatching: vm}
	}
	return lhs, nil
}

func (p *parser) parseAnd() (Expr, error) {
	lhs, err := p.parseCompare()
	if err != nil {
		return nil, err
	}
	for p.isKeyword("and") || p.isKeyword("unless") {
		op := p.advance().val
		vm, err := p.parseVectorMatching(false)
		if err != nil {
			return nil, err
		}
		rhs, err := p.parseCompare()
		if err != nil {
			return nil, err
		}
		lhs = &BinaryExpr{Op: op, LHS: lhs, RHS: rhs, VectorMatching: vm}
	}
	return lhs, nil
}

var compareOps = map[tokenKind]string{
	tEQL: "==", tNEQ: "!=", tLSS: "<", tLTE: "<=", tGTR: ">", tGTE: ">=",
}

func (p *parser) parseCompare() (Expr, error) {
	lhs, err := p.parseAddSub()
	if err != nil {
		return nil, err
	}
	for {
		op, ok := compareOps[p.cur().kind]
		if !ok {
			return lhs, nil
		}
		p.advance()
		returnBool := false
		if p.isKeyword("bool") {
			p.advance()
			returnBool = true
		}
		vm, err := p.parseVectorMatching(true)
		if err != nil {
			return nil, err
		}
		rhs, err := p.parseAddSub()
		if err != nil {
			return nil, err
		}
		lhs = &BinaryExpr{Op: op, LHS: lhs, RHS: rhs, ReturnBool: returnBool, VectorMatching: vm}
	}
}

var addSubOps = map[tokenKind]string{tAdd: "+", tSub: "-"}

func (p *parser) parseAddSub() (Expr, error) {
	lhs, err := p.parseMulDiv()
	if err != nil {
		return nil, err
	}
	for {
		op, ok := addSubOps[p.cur().kind]
		if !ok {
			return lhs, nil
		}
		p.advance()
		vm, err := p.parseVectorMatching(true)
		if err != nil {
			return nil, err
		}
		rhs, err := p.parseMulDiv()
		if err != nil {
			return nil, err
		}
		lhs = &BinaryExpr{Op: op, LHS: lhs, RHS: rhs, VectorMatching: vm}
	}
}

var mulDivOps = map[tokenKind]string{tMul: "*", tDiv: "/", tMod: "%"}

func (p *parser) parseMulDiv() (Expr, error) {
	lhs, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		op, ok := mulDivOps[p.cur().kind]
		if !ok && p.isKeyword("atan2") {
			op, ok = "atan2", true
		}
		if !ok {
			return lhs, nil
		}
		p.advance()
		vm, err := p.parseVectorMatching(true)
		if err != nil {
			return nil, err
		}
		rhs, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		lhs = &BinaryExpr{Op: op, LHS: lhs, RHS: rhs, VectorMatching: vm}
	}
}

// parseUnary — prefix +/-; the operand is a pow-level expr so `-2^2 == -4`.
func (p *parser) parseUnary() (Expr, error) {
	if p.cur().kind == tSub || p.cur().kind == tAdd {
		op := p.advance().val
		operand, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if op == "+" {
			return operand, nil // unary plus is a no-op
		}
		// Fold `-<number literal>` so a leading sign stays a scalar constant.
		if nl, ok := operand.(*NumberLiteral); ok {
			return &NumberLiteral{Val: -nl.Val}, nil
		}
		return &UnaryExpr{Op: "-", Expr: operand}, nil
	}
	return p.parsePow()
}

// parsePow — `^` is right-associative; RHS is parseUnary so `2^-2` works.
func (p *parser) parsePow() (Expr, error) {
	lhs, err := p.parsePostfix()
	if err != nil {
		return nil, err
	}
	if p.cur().kind == tPow {
		p.advance()
		vm, err := p.parseVectorMatching(true)
		if err != nil {
			return nil, err
		}
		rhs, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &BinaryExpr{Op: "^", LHS: lhs, RHS: rhs, VectorMatching: vm}, nil
	}
	return lhs, nil
}

// parsePostfix — primary followed by any of: [range], [range:step] (subquery),
// `offset <dur>`, `@ <ts>|start()|end()`. These may repeat/combine.
func (p *parser) parsePostfix() (Expr, error) {
	e, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		switch {
		case p.cur().kind == tLBracket:
			e, err = p.parseRangeOrSubquery(e)
			if err != nil {
				return nil, err
			}
		case p.isKeyword("offset"):
			p.advance()
			neg := false
			if p.cur().kind == tSub {
				neg = true
				p.advance()
			}
			d, err := p.expectDuration()
			if err != nil {
				return nil, err
			}
			if neg {
				d = -d
			}
			if err := p.applyOffset(e, d); err != nil {
				return nil, err
			}
		case p.cur().kind == tAt:
			p.advance()
			if err := p.parseAt(e); err != nil {
				return nil, err
			}
		default:
			return e, nil
		}
	}
}

// parseRangeOrSubquery handles `[dur]` (matrix selector) and `[dur:step]`
// (subquery). The `[dur]` form is only valid directly on a vector selector.
func (p *parser) parseRangeOrSubquery(e Expr) (Expr, error) {
	p.advance() // '['
	rng, err := p.expectDuration()
	if err != nil {
		return nil, err
	}
	if p.cur().kind == tColon {
		p.advance()
		var step time.Duration
		if p.cur().kind == tDuration {
			step, err = p.expectDuration()
			if err != nil {
				return nil, err
			}
		}
		if _, err := p.expect(tRBracket, "']'"); err != nil {
			return nil, err
		}
		return &SubqueryExpr{Expr: e, Range: rng, Step: step}, nil
	}
	if _, err := p.expect(tRBracket, "']'"); err != nil {
		return nil, err
	}
	vs, ok := e.(*VectorSelector)
	if !ok {
		return nil, p.errf("range [%s] is only valid on a metric selector", model(rng))
	}
	return &MatrixSelector{VectorSelector: vs, Range: rng}, nil
}

// applyOffset attaches an offset to the underlying vector selector.
func (p *parser) applyOffset(e Expr, d time.Duration) error {
	switch v := e.(type) {
	case *VectorSelector:
		v.Offset = d
	case *MatrixSelector:
		v.VectorSelector.Offset = d
	case *SubqueryExpr:
		v.Offset = d
	default:
		return p.errf("offset can only follow a selector or subquery")
	}
	return nil
}

// parseAt attaches an `@` modifier (unix-seconds timestamp, or start()/end()).
func (p *parser) parseAt(e Expr) error {
	var at *float64
	var atStart, atEnd bool
	switch {
	case p.isKeyword("start"):
		p.advance()
		if _, err := p.expect(tLParen, "'('"); err != nil {
			return err
		}
		if _, err := p.expect(tRParen, "')'"); err != nil {
			return err
		}
		atStart = true
	case p.isKeyword("end"):
		p.advance()
		if _, err := p.expect(tLParen, "'('"); err != nil {
			return err
		}
		if _, err := p.expect(tRParen, "')'"); err != nil {
			return err
		}
		atEnd = true
	case p.cur().kind == tNumber || p.cur().kind == tSub || p.cur().kind == tAdd:
		neg := false
		if p.cur().kind == tSub {
			neg = true
			p.advance()
		} else if p.cur().kind == tAdd {
			p.advance()
		}
		if p.cur().kind != tNumber {
			return p.errf("@ expects a unix timestamp, got %q", p.cur().val)
		}
		f, err := parseNumber(p.advance().val)
		if err != nil {
			return err
		}
		if neg {
			f = -f
		}
		at = &f
	default:
		return p.errf("@ expects a timestamp or start()/end(), got %q", p.cur().val)
	}
	switch v := e.(type) {
	case *VectorSelector:
		v.At, v.AtStart, v.AtEnd = at, atStart, atEnd
	case *MatrixSelector:
		v.VectorSelector.At, v.VectorSelector.AtStart, v.VectorSelector.AtEnd = at, atStart, atEnd
	case *SubqueryExpr:
		v.At, v.AtStart, v.AtEnd = at, atStart, atEnd
	default:
		return p.errf("@ can only follow a selector or subquery")
	}
	return nil
}

// ── primary ─────────────────────────────────────────────────────────────────

func (p *parser) parsePrimary() (Expr, error) {
	t := p.cur()
	switch t.kind {
	case tNumber:
		p.advance()
		f, err := parseNumber(t.val)
		if err != nil {
			return nil, err
		}
		return &NumberLiteral{Val: f}, nil
	case tString:
		p.advance()
		return &StringLiteral{Val: t.val}, nil
	case tLParen:
		p.advance()
		inner, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tRParen, "')'"); err != nil {
			return nil, err
		}
		return &ParenExpr{Expr: inner}, nil
	case tLBrace:
		// Nameless selector: {__name__="x", …}
		matchers, err := p.parseLabelMatchers()
		if err != nil {
			return nil, err
		}
		return p.selectorFromMatchers("", matchers)
	case tIdentifier:
		return p.parseIdentifierExpr()
	default:
		return nil, p.errf("unexpected %q", t.val)
	}
}

// parseIdentifierExpr disambiguates aggregation / function call / vector
// selector, all of which start with an identifier.
func (p *parser) parseIdentifierExpr() (Expr, error) {
	name := p.cur().val
	// Aggregation: `op (` or `op by(…)` / `op without(…)`.
	if aggregators[name] {
		next := p.peek()
		if next.kind == tLParen || (next.kind == tIdentifier && (next.val == "by" || next.val == "without")) {
			return p.parseAggregate()
		}
	}
	// Function call: identifier directly followed by '('.
	if p.peek().kind == tLParen {
		p.advance() // name
		args, err := p.parseCallArgs()
		if err != nil {
			return nil, err
		}
		return &Call{Func: name, Args: args}, nil
	}
	// Otherwise a vector selector: metric name + optional {matchers}.
	p.advance() // name
	var matchers []*LabelMatcher
	if p.cur().kind == tLBrace {
		var err error
		matchers, err = p.parseLabelMatchers()
		if err != nil {
			return nil, err
		}
	}
	return p.selectorFromMatchers(name, matchers)
}

func (p *parser) parseCallArgs() ([]Expr, error) {
	if _, err := p.expect(tLParen, "'('"); err != nil {
		return nil, err
	}
	var args []Expr
	if p.cur().kind == tRParen {
		p.advance()
		return args, nil
	}
	for {
		a, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		args = append(args, a)
		if p.cur().kind == tComma {
			p.advance()
			continue
		}
		break
	}
	if _, err := p.expect(tRParen, "')'"); err != nil {
		return nil, err
	}
	return args, nil
}

// parseAggregate handles both `op by(…) (expr)` and `op(expr) by(…)`.
func (p *parser) parseAggregate() (Expr, error) {
	op := p.advance().val
	agg := &AggregateExpr{Op: op}

	parseGrouping := func() error {
		without := p.cur().val == "without"
		p.advance() // by | without
		labels, err := p.parseLabelList()
		if err != nil {
			return err
		}
		agg.Without = without
		agg.Grouping = labels
		return nil
	}

	// Optional grouping BEFORE the parens.
	if p.isKeyword("by") || p.isKeyword("without") {
		if err := parseGrouping(); err != nil {
			return nil, err
		}
	}

	if _, err := p.expect(tLParen, "'('"); err != nil {
		return nil, err
	}
	first, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if aggregatorsWithParam[op] && p.cur().kind == tComma {
		p.advance()
		agg.Param = first
		agg.Expr, err = p.parseExpr()
		if err != nil {
			return nil, err
		}
	} else {
		agg.Expr = first
	}
	if _, err := p.expect(tRParen, "')'"); err != nil {
		return nil, err
	}

	// Optional grouping AFTER the parens (only if not already given).
	if (p.isKeyword("by") || p.isKeyword("without")) && agg.Grouping == nil && !agg.Without {
		if err := parseGrouping(); err != nil {
			return nil, err
		}
	}
	return agg, nil
}

// parseVectorMatching parses the optional `on(…)/ignoring(…)` +
// `group_left(…)/group_right(…)` after a binary operator. allowGroup is false
// for and/or/unless (set ops take on/ignoring but not group_*).
func (p *parser) parseVectorMatching(allowGroup bool) (*VectorMatching, error) {
	if !p.isKeyword("on") && !p.isKeyword("ignoring") {
		return nil, nil
	}
	vm := &VectorMatching{Card: CardOneToOne}
	vm.On = p.cur().val == "on"
	p.advance()
	labels, err := p.parseLabelList()
	if err != nil {
		return nil, err
	}
	vm.MatchingLabels = labels
	if allowGroup && (p.isKeyword("group_left") || p.isKeyword("group_right")) {
		if p.cur().val == "group_left" {
			vm.Card = CardManyToOne
		} else {
			vm.Card = CardOneToMany
		}
		p.advance()
		// group_left/right may carry an optional include-label list.
		if p.cur().kind == tLParen {
			inc, err := p.parseLabelList()
			if err != nil {
				return nil, err
			}
			vm.Include = inc
		}
	}
	return vm, nil
}

// parseLabelList parses `( label, label, … )` — a possibly-empty paren list of
// bare label names (used by by/without/on/ignoring/group_*).
func (p *parser) parseLabelList() ([]string, error) {
	if _, err := p.expect(tLParen, "'('"); err != nil {
		return nil, err
	}
	var labels []string
	if p.cur().kind == tRParen {
		p.advance()
		return labels, nil
	}
	for {
		if p.cur().kind != tIdentifier {
			return nil, p.errf("expected a label name, got %q", p.cur().val)
		}
		labels = append(labels, p.advance().val)
		if p.cur().kind == tComma {
			p.advance()
			continue
		}
		break
	}
	if _, err := p.expect(tRParen, "')'"); err != nil {
		return nil, err
	}
	return labels, nil
}

// parseLabelMatchers parses `{ name OP "value", … }`.
func (p *parser) parseLabelMatchers() ([]*LabelMatcher, error) {
	if _, err := p.expect(tLBrace, "'{'"); err != nil {
		return nil, err
	}
	var ms []*LabelMatcher
	if p.cur().kind == tRBrace {
		p.advance()
		return ms, nil
	}
	for {
		if p.cur().kind != tIdentifier {
			return nil, p.errf("expected a label name, got %q", p.cur().val)
		}
		name := p.advance().val
		var mt MatchType
		switch p.cur().kind {
		case tAssign:
			mt = MatchEqual
		case tNEQ:
			mt = MatchNotEqual
		case tEQLRegex:
			mt = MatchRegexp
		case tNEQRegex:
			mt = MatchNotRegexp
		default:
			return nil, p.errf("expected a matcher operator (=, !=, =~, !~), got %q", p.cur().val)
		}
		p.advance()
		if p.cur().kind != tString {
			return nil, p.errf("expected a quoted matcher value, got %q", p.cur().val)
		}
		val := p.advance().val
		ms = append(ms, &LabelMatcher{Name: name, Type: mt, Value: val})
		if p.cur().kind == tComma {
			p.advance()
			// trailing comma before } is allowed
			if p.cur().kind == tRBrace {
				break
			}
			continue
		}
		break
	}
	if _, err := p.expect(tRBrace, "'}'"); err != nil {
		return nil, err
	}
	return ms, nil
}

// selectorFromMatchers builds a VectorSelector, hoisting a __name__ matcher
// into Name and validating that exactly one metric name is given.
func (p *parser) selectorFromMatchers(name string, matchers []*LabelMatcher) (Expr, error) {
	kept := matchers[:0:0]
	for _, m := range matchers {
		if m.Name == "__name__" && m.Type == MatchEqual {
			if name != "" {
				return nil, p.errf("metric name given both as %q and __name__=%q", name, m.Value)
			}
			name = m.Value
			continue
		}
		kept = append(kept, m)
	}
	return &VectorSelector{Name: name, Matchers: kept}, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

func (p *parser) expectDuration() (time.Duration, error) {
	if p.cur().kind != tDuration {
		return 0, p.errf("expected a duration (e.g. 5m), got %q", p.cur().val)
	}
	return ParseDuration(p.advance().val)
}

// parseNumber parses a PromQL numeric literal incl Inf/NaN and 0x hex.
func parseNumber(s string) (float64, error) {
	ls := strings.ToLower(s)
	switch ls {
	case "inf", "+inf":
		return math.Inf(1), nil
	case "-inf":
		return math.Inf(-1), nil
	case "nan":
		return math.NaN(), nil
	}
	if strings.HasPrefix(ls, "0x") {
		i, err := strconv.ParseInt(s[2:], 16, 64)
		if err != nil {
			return 0, fmt.Errorf("promql: bad hex number %q", s)
		}
		return float64(i), nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("promql: bad number %q", s)
	}
	return f, nil
}

// ParseDuration parses a PromQL duration ("5m", "1h30m", "500ms"). Exported so
// the evaluator + API can reuse it. Weeks/years use fixed 7d / 365d.
func ParseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("promql: empty duration")
	}
	orig := s
	var total time.Duration
	for len(s) > 0 {
		i := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == 0 {
			return 0, fmt.Errorf("promql: bad duration %q", orig)
		}
		n, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, fmt.Errorf("promql: bad duration %q", orig)
		}
		rest := s[i:]
		var unit time.Duration
		var ulen int
		switch {
		case strings.HasPrefix(rest, "ms"):
			unit, ulen = time.Millisecond, 2
		case strings.HasPrefix(rest, "s"):
			unit, ulen = time.Second, 1
		case strings.HasPrefix(rest, "m"):
			unit, ulen = time.Minute, 1
		case strings.HasPrefix(rest, "h"):
			unit, ulen = time.Hour, 1
		case strings.HasPrefix(rest, "d"):
			unit, ulen = 24*time.Hour, 1
		case strings.HasPrefix(rest, "w"):
			unit, ulen = 7*24*time.Hour, 1
		case strings.HasPrefix(rest, "y"):
			unit, ulen = 365*24*time.Hour, 1
		default:
			return 0, fmt.Errorf("promql: bad duration unit in %q", orig)
		}
		total += time.Duration(n) * unit
		s = rest[ulen:]
	}
	return total, nil
}
