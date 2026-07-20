// Package promql is Coremetry's PromQL query engine (v0.9.111+) — a real
// lexer + recursive-descent parser + AST that compiles down to the existing
// chstore metric machinery (QueryMetric / QueryMetricRate /
// QueryMetricHistogramPercentile — the F1-F3 work). One industry-standard
// query surface so operators can paste the same PromQL they run in Grafana.
//
// Architecture (approved 2026-07-20 — "hybrid model"):
//   - LEAF fetches (vector/matrix selectors, rate/increase/histogram_quantile)
//     push down to ClickHouse via the existing bounded chstore methods
//     (LIMIT + max_execution_time + time-bounded WHERE, distributed-safe).
//   - SERIES-OVER-SERIES ops (aggregations, binary operators) evaluate in Go
//     over the already-bounded fetched series — Prometheus's own model.
//
// Phase 1 (this file + lex.go + parse.go): lexer + parser + AST ONLY. No
// evaluator yet — the parser is shippable + testable on its own (golden ASTs).
// Grammar mirrors Prometheus; the eval (eval.go, Phase 2+) reuses F1-F3.
//
// PERFORMANCE IS A HARD CONSTRAINT (operator, 2026-07-20). The AST carries no
// execution cost; the guards live in the evaluator (series-count caps, reuse
// of the bounded leaf fetches, complexity limits). This file only shapes the
// tree — but it is designed so the evaluator can cheaply reject a too-broad
// query (e.g. a bare `{__name__=~".+"}` with no name) before any CH round trip.
package promql

import (
	"strconv"
	"strings"
	"time"
)

// Expr is any node in a parsed PromQL expression tree.
type Expr interface {
	// exprNode is a marker so only this package's types satisfy Expr.
	exprNode()
	// String renders the node back to canonical PromQL — used by the API to
	// echo the normalized query (transparency, like DQL's equivalent-SQL).
	String() string
}

// ValueType is the PromQL value kind an expression evaluates to. The parser
// tags a few nodes; the evaluator uses it for type checks (e.g. the first arg
// of histogram_quantile is a scalar, the second a vector).
type ValueType string

const (
	ValueTypeNone   ValueType = "none"
	ValueTypeScalar ValueType = "scalar"
	ValueTypeVector ValueType = "vector"
	ValueTypeMatrix ValueType = "matrix"
	ValueTypeString ValueType = "string"
)

// MatchType is a label-matcher operator.
type MatchType int

const (
	MatchEqual     MatchType = iota // =
	MatchNotEqual                   // !=
	MatchRegexp                     // =~
	MatchNotRegexp                  // !~
)

func (m MatchType) String() string {
	switch m {
	case MatchEqual:
		return "="
	case MatchNotEqual:
		return "!="
	case MatchRegexp:
		return "=~"
	case MatchNotRegexp:
		return "!~"
	}
	return "?"
}

// LabelMatcher is one `label OP "value"` predicate inside `{…}`. The special
// label __name__ carries the metric name when written inside the braces.
type LabelMatcher struct {
	Name  string
	Type  MatchType
	Value string
}

func (lm *LabelMatcher) String() string {
	return lm.Name + lm.Type.String() + strconv.Quote(lm.Value)
}

// VectorSelector selects a set of series: `metric_name{matchers}`. Name may be
// empty when the metric is given via a __name__ matcher inside the braces.
type VectorSelector struct {
	Name     string
	Matchers []*LabelMatcher
	Offset   time.Duration // `offset 5m` — shifts the window back
	At       *float64      // `@ 1609746000` (unix seconds) — pins the eval time
	AtStart  bool          // `@ start()`
	AtEnd    bool          // `@ end()`
}

func (v *VectorSelector) exprNode() {}
func (v *VectorSelector) String() string {
	var b strings.Builder
	b.WriteString(v.Name)
	if len(v.Matchers) > 0 {
		b.WriteByte('{')
		for i, m := range v.Matchers {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(m.String())
		}
		b.WriteByte('}')
	}
	writeAtOffset(&b, v.At, v.AtStart, v.AtEnd, v.Offset)
	return b.String()
}

// MatrixSelector wraps a VectorSelector with a range: `metric[5m]`. It is the
// argument to range functions (rate, increase, delta …).
type MatrixSelector struct {
	VectorSelector *VectorSelector
	Range          time.Duration
}

func (m *MatrixSelector) exprNode() {}
func (m *MatrixSelector) String() string {
	// The @/offset live on the inner vector selector but PromQL prints them
	// AFTER the range bracket; re-render with the bracket in the middle.
	vs := m.VectorSelector
	var b strings.Builder
	b.WriteString(vs.Name)
	if len(vs.Matchers) > 0 {
		b.WriteByte('{')
		for i, mm := range vs.Matchers {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(mm.String())
		}
		b.WriteByte('}')
	}
	b.WriteByte('[')
	b.WriteString(model(m.Range))
	b.WriteByte(']')
	writeAtOffset(&b, vs.At, vs.AtStart, vs.AtEnd, vs.Offset)
	return b.String()
}

// SubqueryExpr: `expr[range:step]` — an instant expr sampled over a range.
// Parsed in Phase 1; the evaluator support lands with the range machinery.
type SubqueryExpr struct {
	Expr    Expr
	Range   time.Duration
	Step    time.Duration // 0 = default resolution
	Offset  time.Duration
	At      *float64
	AtStart bool
	AtEnd   bool
}

func (s *SubqueryExpr) exprNode() {}
func (s *SubqueryExpr) String() string {
	var b strings.Builder
	b.WriteString(s.Expr.String())
	b.WriteByte('[')
	b.WriteString(model(s.Range))
	b.WriteByte(':')
	if s.Step > 0 {
		b.WriteString(model(s.Step))
	}
	b.WriteByte(']')
	writeAtOffset(&b, s.At, s.AtStart, s.AtEnd, s.Offset)
	return b.String()
}

// Call is a function application: `rate(x[5m])`, `histogram_quantile(0.95, x)`.
type Call struct {
	Func string
	Args []Expr
}

func (c *Call) exprNode() {}
func (c *Call) String() string {
	var b strings.Builder
	b.WriteString(c.Func)
	b.WriteByte('(')
	for i, a := range c.Args {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(a.String())
	}
	b.WriteByte(')')
	return b.String()
}

// AggregateExpr: `sum by (le) (rate(x[5m]))`, `topk(5, x)`.
type AggregateExpr struct {
	Op       string // sum, avg, min, max, count, stddev, stdvar, quantile, topk, bottomk, count_values, group
	Expr     Expr   // the vector being aggregated
	Param    Expr   // topk/bottomk/quantile/count_values scalar/string param; nil otherwise
	Grouping []string
	Without  bool // `without(…)` vs `by(…)`
}

func (a *AggregateExpr) exprNode() {}
func (a *AggregateExpr) String() string {
	var b strings.Builder
	b.WriteString(a.Op)
	if len(a.Grouping) > 0 || a.Without {
		if a.Without {
			b.WriteString(" without (")
		} else {
			b.WriteString(" by (")
		}
		b.WriteString(strings.Join(a.Grouping, ", "))
		b.WriteString(") ")
	}
	b.WriteByte('(')
	if a.Param != nil {
		b.WriteString(a.Param.String())
		b.WriteString(", ")
	}
	b.WriteString(a.Expr.String())
	b.WriteByte(')')
	return b.String()
}

// VectorMatching describes how a binary op lines up two vectors.
type VectorMatching struct {
	Card           VectorMatchCardinality
	MatchingLabels []string
	On             bool     // true = on(…), false = ignoring(…)
	Include        []string // group_left(…) / group_right(…) extra labels
}

type VectorMatchCardinality int

const (
	CardOneToOne VectorMatchCardinality = iota
	CardManyToOne
	CardOneToMany
)

// BinaryExpr: `a / b`, `rate(x) > 0.5`.
type BinaryExpr struct {
	Op             string // + - * / % ^ == != > >= < <= and or unless
	LHS, RHS       Expr
	VectorMatching *VectorMatching // nil for scalar/scalar
	ReturnBool     bool            // `> bool 0.5` — comparison yields 0/1 instead of filtering
}

func (be *BinaryExpr) exprNode() {}
func (be *BinaryExpr) String() string {
	var b strings.Builder
	b.WriteString(be.LHS.String())
	b.WriteByte(' ')
	b.WriteString(be.Op)
	if be.ReturnBool {
		b.WriteString(" bool")
	}
	if vm := be.VectorMatching; vm != nil && (len(vm.MatchingLabels) > 0 || vm.On) {
		if vm.On {
			b.WriteString(" on (")
		} else {
			b.WriteString(" ignoring (")
		}
		b.WriteString(strings.Join(vm.MatchingLabels, ", "))
		b.WriteByte(')')
		if len(vm.Include) > 0 {
			if vm.Card == CardManyToOne {
				b.WriteString(" group_left (")
			} else {
				b.WriteString(" group_right (")
			}
			b.WriteString(strings.Join(vm.Include, ", "))
			b.WriteByte(')')
		}
	}
	b.WriteByte(' ')
	b.WriteString(be.RHS.String())
	return b.String()
}

// UnaryExpr: `-x`.
type UnaryExpr struct {
	Op   string // "-" or "+"
	Expr Expr
}

func (u *UnaryExpr) exprNode() {}
func (u *UnaryExpr) String() string { return u.Op + u.Expr.String() }

// ParenExpr preserves an explicit `(…)` grouping in the tree (and re-render).
type ParenExpr struct{ Expr Expr }

func (p *ParenExpr) exprNode() {}
func (p *ParenExpr) String() string { return "(" + p.Expr.String() + ")" }

// NumberLiteral is a scalar constant.
type NumberLiteral struct{ Val float64 }

func (n *NumberLiteral) exprNode() {}
func (n *NumberLiteral) String() string {
	return strconv.FormatFloat(n.Val, 'g', -1, 64)
}

// StringLiteral — the string arg to label_replace / label_join / count_values.
type StringLiteral struct{ Val string }

func (s *StringLiteral) exprNode() {}
func (s *StringLiteral) String() string { return strconv.Quote(s.Val) }

// writeAtOffset renders the shared `@ … offset …` suffix.
func writeAtOffset(b *strings.Builder, at *float64, atStart, atEnd bool, offset time.Duration) {
	switch {
	case atStart:
		b.WriteString(" @ start()")
	case atEnd:
		b.WriteString(" @ end()")
	case at != nil:
		b.WriteString(" @ ")
		// 'f' (not 'g') so a unix timestamp renders as 1609746000, not 1.6e+09.
		b.WriteString(strconv.FormatFloat(*at, 'f', -1, 64))
	}
	if offset != 0 {
		b.WriteString(" offset ")
		b.WriteString(model(offset))
	}
}

// model renders a duration in PromQL form (5m, 1h30m, 500ms). Prometheus uses
// its own model.Duration; we render the common units without the dependency.
func model(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	neg := d < 0
	if neg {
		d = -d
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	type unit struct {
		suffix string
		size   time.Duration
	}
	// PromQL has no "week" in re-render for safety; days is the largest unit.
	for _, u := range []unit{
		{"d", 24 * time.Hour}, {"h", time.Hour}, {"m", time.Minute},
		{"s", time.Second}, {"ms", time.Millisecond},
	} {
		if d >= u.size {
			n := d / u.size
			d -= n * u.size
			b.WriteString(strconv.FormatInt(int64(n), 10))
			b.WriteString(u.suffix)
		}
	}
	if b.Len() == 0 || (neg && b.Len() == 1) {
		return "0s"
	}
	return b.String()
}
