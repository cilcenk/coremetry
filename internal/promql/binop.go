package promql

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// binop.go (Phase 4, v0.9.122) — PromQL binary operators, evaluated in Go over
// the already-bounded operand vectors (Prometheus's model). Supported:
//   - scalar ⊙ scalar, scalar ⊙ vector, vector ⊙ scalar
//   - vector ⊙ vector with DEFAULT 1:1 matching by the full label set (groupKey)
//   - arithmetic (+ - * / % ^), comparison (== != < <= > >=) with the `bool`
//     modifier (1/0) or filter semantics (drop non-matching points).
//   - set ops and/or/unless (default matching by groupKey).
//   - on()/ignoring() 1:1 matching by a label subset (labels derived from the
//     AST's by() lists). Deferred (clear errors): group_left/right many-to-one;
//     on()/ignoring() over a without() operand.
//
// Performance: no new fetches — operates on operand vectors bounded by the leaf
// fetches (chstore LIMIT 50000 + top-N fold). Note MaxSeries caps the FINAL
// result only, not the intermediate operands; range queries stay small because
// the pixel-adaptive step yields many buckets ⇒ few series per leaf.

func (ev *evaluator) evalBinary(be *BinaryExpr) ([]chstore.SpanMetricSeries, error) {
	op := be.Op
	if op == "and" || op == "or" || op == "unless" {
		return ev.evalSetOp(be, op)
	}
	// group_left/right (many-to-one) still deferred — must be caught even with
	// an empty on()/ignoring() (review MAJOR, v0.9.122). on()/ignoring() 1:1 is
	// handled in the vector-vector path below.
	if vm := be.VectorMatching; vm != nil && (len(vm.Include) > 0 || vm.Card != CardOneToOne) {
		return nil, fmt.Errorf("promql: group_left/right (many-to-one) matching is not supported yet")
	}

	lv, lScalar := scalarValue(be.LHS)
	rv, rScalar := scalarValue(be.RHS)

	switch {
	case lScalar && rScalar:
		v, keep := applyBinary(op, lv, rv, be.ReturnBool)
		if !keep {
			return []chstore.SpanMetricSeries{}, nil
		}
		return ev.scalarSeries(v), nil

	case lScalar: // scalar ⊙ vector
		rs, err := ev.eval(be.RHS)
		if err != nil {
			return nil, err
		}
		return applyScalarVector(op, lv, rs, true, be.ReturnBool), nil

	case rScalar: // vector ⊙ scalar
		ls, err := ev.eval(be.LHS)
		if err != nil {
			return nil, err
		}
		return applyScalarVector(op, rv, ls, false, be.ReturnBool), nil

	default: // vector ⊙ vector
		ls, err := ev.eval(be.LHS)
		if err != nil {
			return nil, err
		}
		rs, err := ev.eval(be.RHS)
		if err != nil {
			return nil, err
		}
		if vm := be.VectorMatching; vm != nil && (len(vm.MatchingLabels) > 0 || vm.On) {
			// on()/ignoring() 1:1 — match by a label SUBSET. The operand label
			// NAMES come from the query AST (an aggregation's by() list); the
			// series' groupKey values are positional to them.
			lLabels, lok := operandLabels(be.LHS)
			rLabels, rok := operandLabels(be.RHS)
			if !lok || !rok {
				return nil, fmt.Errorf("promql: on()/ignoring() is only supported over by()-aggregations for now (not without()/complex operands)")
			}
			return applyVectorVectorMatched(op, ls, rs, lLabels, rLabels, vm, be.ReturnBool)
		}
		return applyVectorVector(op, ls, rs, be.ReturnBool), nil
	}
}

// operandLabels returns the label NAMES positional to a vector expression's
// series groupKey, derivable from the AST: an aggregation contributes its by()
// list; a bare selector / rate collapses to one label-less series. Returns
// ok=false where the labels are not statically known (without(), unsupported
// shapes) so on()/ignoring() over them errors cleanly instead of mis-matching.
func operandLabels(e Expr) ([]string, bool) {
	switch x := e.(type) {
	case *ParenExpr:
		return operandLabels(x.Expr)
	case *UnaryExpr:
		return operandLabels(x.Expr)
	case *VectorSelector, *MatrixSelector, *NumberLiteral, *StringLiteral:
		return []string{}, true // collapsed single (or label-less) series
	case *Call:
		return []string{}, true // rate/increase/histogram_quantile/scalar-fn → label-less here
	case *AggregateExpr:
		if x.Without {
			return nil, false // computed at runtime, not statically known
		}
		return x.Grouping, true
	case *BinaryExpr:
		return operandLabels(x.LHS) // result carries LHS labels
	default:
		return nil, false
	}
}

// applyVectorVectorMatched is applyVectorVector with on()/ignoring() 1:1
// matching: the match key is the subset of each series' labels selected by
// on(L) (keep L) or ignoring(L) (drop L), built by NAME so label order doesn't
// matter. A match key appearing on >1 RHS series is a many-to-one situation →
// error (that needs group_left/right).
func applyVectorVectorMatched(op string, lhs, rhs []chstore.SpanMetricSeries, lLabels, rLabels []string, vm *VectorMatching, returnBool bool) ([]chstore.SpanMetricSeries, error) {
	ml := make(map[string]bool, len(vm.MatchingLabels))
	for _, l := range vm.MatchingLabels {
		ml[l] = true
	}
	matchKey := func(gk, labels []string) string {
		parts := make([]string, 0, len(labels))
		for i, name := range labels {
			if i >= len(gk) {
				break
			}
			in := ml[name]
			if (vm.On && in) || (!vm.On && !in) {
				parts = append(parts, name+"="+gk[i])
			}
		}
		sort.Strings(parts)
		return strings.Join(parts, "\x1f")
	}

	rhsIdx := make(map[string]map[int64]float64, len(rhs))
	dupe := make(map[string]bool)
	for _, s := range rhs {
		key := matchKey(s.GroupKey, rLabels)
		if _, seen := rhsIdx[key]; seen {
			dupe[key] = true // >1 RHS series map to the same match set
		}
		m := rhsIdx[key]
		if m == nil {
			m = make(map[int64]float64, len(s.Points))
			rhsIdx[key] = m
		}
		for _, p := range s.Points {
			m[p.Time] = p.Value
		}
	}
	out := make([]chstore.SpanMetricSeries, 0, len(lhs))
	lhsSeen := make(map[string]bool, len(lhs))
	for _, s := range lhs {
		key := matchKey(s.GroupKey, lLabels)
		if dupe[key] {
			return nil, fmt.Errorf("promql: many RHS series match one LHS series on this label set — needs group_right (not supported yet)")
		}
		rm := rhsIdx[key]
		if rm == nil {
			continue
		}
		// >1 LHS series sharing a match key that matches RHS = many-to-one;
		// PromQL requires an explicit group_left (review MINOR, v0.9.126).
		if lhsSeen[key] {
			return nil, fmt.Errorf("promql: many LHS series match one RHS series on this label set — needs group_left (not supported yet)")
		}
		lhsSeen[key] = true
		pts := make([]chstore.SpanMetricPoint, 0, len(s.Points))
		for _, p := range s.Points {
			rval, ok := rm[p.Time]
			if !ok {
				continue
			}
			v, keep := applyBinary(op, p.Value, rval, returnBool)
			if !keep {
				continue
			}
			pts = append(pts, chstore.SpanMetricPoint{Time: p.Time, Value: v})
		}
		if len(pts) > 0 {
			out = append(out, chstore.SpanMetricSeries{GroupKey: s.GroupKey, Points: pts})
		}
	}
	return out, nil
}

// scalarValue folds a constant sub-expression (literal, unary/paren of a
// constant, or arithmetic of constants) into a scalar. Anything touching a
// selector is a vector → (0, false).
func scalarValue(e Expr) (float64, bool) {
	switch x := e.(type) {
	case *NumberLiteral:
		return x.Val, true
	case *ParenExpr:
		return scalarValue(x.Expr)
	case *UnaryExpr:
		v, ok := scalarValue(x.Expr)
		if !ok {
			return 0, false
		}
		if x.Op == "-" {
			return -v, true
		}
		return v, true
	case *BinaryExpr:
		lv, lok := scalarValue(x.LHS)
		rv, rok := scalarValue(x.RHS)
		if lok && rok {
			v, _ := applyBinary(x.Op, lv, rv, x.ReturnBool)
			return v, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// applyBinary computes a ⊙ b. For arithmetic keep is always true. For a
// comparison: in bool mode the result is 1/0 (keep true); otherwise the result
// is the LHS value and keep is the truth value (PromQL filter semantics — a
// false comparison drops the sample).
func applyBinary(op string, a, b float64, returnBool bool) (float64, bool) {
	switch op {
	case "+":
		return a + b, true
	case "-":
		return a - b, true
	case "*":
		return a * b, true
	case "/":
		return a / b, true // ±Inf/NaN scrubbed at the JSON layer
	case "%":
		return math.Mod(a, b), true
	case "^":
		return math.Pow(a, b), true
	}
	t := compareTrue(op, a, b)
	if returnBool {
		if t {
			return 1, true
		}
		return 0, true
	}
	return a, t
}

func compareTrue(op string, a, b float64) bool {
	switch op {
	case "==":
		return a == b
	case "!=":
		return a != b
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	case ">=":
		return a >= b
	}
	return false
}

// applyScalarVector applies a scalar to every point of a vector. scalarLeft
// preserves operand order (scalar-vector vs vector-scalar), which matters for
// non-commutative ops (- / % ^) and comparisons.
func applyScalarVector(op string, scalar float64, vec []chstore.SpanMetricSeries, scalarLeft, returnBool bool) []chstore.SpanMetricSeries {
	out := make([]chstore.SpanMetricSeries, 0, len(vec))
	for _, s := range vec {
		pts := make([]chstore.SpanMetricPoint, 0, len(s.Points))
		for _, p := range s.Points {
			a, b := p.Value, scalar
			if scalarLeft {
				a, b = scalar, p.Value
			}
			v, keep := applyBinary(op, a, b, returnBool)
			if !keep {
				continue
			}
			pts = append(pts, chstore.SpanMetricPoint{Time: p.Time, Value: v})
		}
		if len(pts) > 0 {
			out = append(out, chstore.SpanMetricSeries{GroupKey: s.GroupKey, Points: pts})
		}
	}
	return out
}

// applyVectorVector applies op between two vectors with default 1:1 matching:
// an LHS series pairs with the RHS series that has the identical label set
// (groupKey), matched per timestamp. Unmatched series/points drop.
func applyVectorVector(op string, lhs, rhs []chstore.SpanMetricSeries, returnBool bool) []chstore.SpanMetricSeries {
	rhsIdx := make(map[string]map[int64]float64, len(rhs))
	for _, s := range rhs {
		key := strings.Join(s.GroupKey, "\x1f")
		m := rhsIdx[key]
		if m == nil {
			m = make(map[int64]float64, len(s.Points))
			rhsIdx[key] = m
		}
		for _, p := range s.Points {
			m[p.Time] = p.Value
		}
	}
	out := make([]chstore.SpanMetricSeries, 0, len(lhs))
	for _, s := range lhs {
		rm := rhsIdx[strings.Join(s.GroupKey, "\x1f")]
		if rm == nil {
			continue // no matching RHS series
		}
		pts := make([]chstore.SpanMetricPoint, 0, len(s.Points))
		for _, p := range s.Points {
			rval, ok := rm[p.Time]
			if !ok {
				continue // no RHS sample at this timestamp
			}
			v, keep := applyBinary(op, p.Value, rval, returnBool)
			if !keep {
				continue
			}
			pts = append(pts, chstore.SpanMetricPoint{Time: p.Time, Value: v})
		}
		if len(pts) > 0 {
			out = append(out, chstore.SpanMetricSeries{GroupKey: s.GroupKey, Points: pts})
		}
	}
	return out
}

// evalSetOp handles the PromQL set operators and/or/unless with DEFAULT matching
// (by full label set / groupKey). and/unless are per-timestamp; or is a
// series-level union (RHS series whose label set is absent from LHS). Explicit
// on()/ignoring() matching is deferred with a clear error.
func (ev *evaluator) evalSetOp(be *BinaryExpr, op string) ([]chstore.SpanMetricSeries, error) {
	if vm := be.VectorMatching; vm != nil && (len(vm.MatchingLabels) > 0 || vm.On || vm.Card != CardOneToOne) {
		return nil, fmt.Errorf("promql: on()/ignoring()/group_left on %q is not supported yet — default matching only", op)
	}
	ls, err := ev.eval(be.LHS)
	if err != nil {
		return nil, err
	}
	rs, err := ev.eval(be.RHS)
	if err != nil {
		return nil, err
	}
	switch op {
	case "and":
		return setAndUnless(ls, rs, false), nil
	case "unless":
		return setAndUnless(ls, rs, true), nil
	default: // or
		return setOr(ls, rs), nil
	}
}

// setAndUnless keeps LHS points whose (groupKey, timestamp) has a RHS point
// (and) or has NO RHS point (unless). Values are always the LHS values.
func setAndUnless(ls, rs []chstore.SpanMetricSeries, unless bool) []chstore.SpanMetricSeries {
	rhsIdx := make(map[string]map[int64]struct{}, len(rs))
	for _, s := range rs {
		key := strings.Join(s.GroupKey, "\x1f")
		m := rhsIdx[key]
		if m == nil {
			m = make(map[int64]struct{}, len(s.Points))
			rhsIdx[key] = m
		}
		for _, p := range s.Points {
			m[p.Time] = struct{}{}
		}
	}
	out := make([]chstore.SpanMetricSeries, 0, len(ls))
	for _, s := range ls {
		rm := rhsIdx[strings.Join(s.GroupKey, "\x1f")]
		pts := make([]chstore.SpanMetricPoint, 0, len(s.Points))
		for _, p := range s.Points {
			_, present := rm[p.Time]
			if present != unless { // and: keep when present; unless: keep when absent
				pts = append(pts, p)
			}
		}
		if len(pts) > 0 {
			out = append(out, chstore.SpanMetricSeries{GroupKey: s.GroupKey, Points: pts})
		}
	}
	return out
}

// setOr returns all LHS series plus the RHS series whose label set (groupKey)
// does not appear in LHS.
func setOr(ls, rs []chstore.SpanMetricSeries) []chstore.SpanMetricSeries {
	seen := make(map[string]struct{}, len(ls))
	out := make([]chstore.SpanMetricSeries, 0, len(ls)+len(rs))
	for _, s := range ls {
		seen[strings.Join(s.GroupKey, "\x1f")] = struct{}{}
		out = append(out, s)
	}
	for _, s := range rs {
		if _, ok := seen[strings.Join(s.GroupKey, "\x1f")]; !ok {
			out = append(out, s)
		}
	}
	return out
}
