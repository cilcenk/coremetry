package promql

import (
	"fmt"
	"math"
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
// Deferred (clear errors): explicit vector matching on/ignoring/group_left/right.
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
	// group_left/right (Card != one-to-one) must be caught even with an empty
	// on()/ignoring() and no include list, or a many-to-one join silently
	// degrades to positional 1:1 → wrong/empty result (review MAJOR, v0.9.122).
	if vm := be.VectorMatching; vm != nil && (len(vm.MatchingLabels) > 0 || vm.On || len(vm.Include) > 0 || vm.Card != CardOneToOne) {
		return nil, fmt.Errorf("promql: on()/ignoring()/group_left/right matching is not supported yet — default 1:1 label matching only")
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
		return applyVectorVector(op, ls, rs, be.ReturnBool), nil
	}
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
