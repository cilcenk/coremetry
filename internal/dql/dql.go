// Package dql is Coremetry's unified query language (v0.5.265) —
// a Kusto/Dynatrace-DQL-flavoured pipe shape that compiles down
// to the existing chstore aggregations. One syntax across spans
// / metrics, no per-signal context switch.
//
// MVP grammar:
//
//   query   := table ('|' pipe)*
//   table   := "spans" | "metrics"
//   pipe    := "filter" predicate
//            | "summarize" agg ("by" group)?
//   predicate := ident op value
//   op      := "==" | "!=" | "contains" | "startswith" | "endswith"
//   agg     := "count()"
//            | "rate()"
//            | "error_rate()"
//            | ("p50" | "p95" | "p99" | "avg" | "max" | "min") "(" ident ")"
//   group   := "bin(time," duration ")"  (optional — defaults to auto)
//   value   := quoted-string | number
//   duration := <int> ("s"|"m"|"h"|"d")
//
// Logs (KQL-backed) deferred to Phase 2 — the parser knows the
// table name but the executor errors with a clear message.
//
// Design notes:
//
//   • Parser is a hand-rolled token stream — ~250 lines, no
//     external dep, no codegen. The grammar is small enough
//     that a parser-generator would be more weight than it's
//     worth.
//   • Compile() returns a structured Plan that the API layer
//     dispatches to existing chstore methods. Keeps the
//     execution paths uniform with /metrics and /explore so a
//     DQL query and a UI-built query hit the SAME cache + the
//     SAME CH MV when applicable.
//   • The plan exposes the equivalent SQL the executor will
//     run so /admin/query can surface it in the UI for
//     transparency + audit.
package dql

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// Table — the OTel signal kind a query operates on.
type Table string

const (
	TableSpans   Table = "spans"
	TableMetrics Table = "metrics"
	TableLogs    Table = "logs"
)

// Plan is the structured form of a parsed query. The API
// dispatcher reads this and calls the matching chstore method.
type Plan struct {
	Table       Table
	Filters     []chstore.FilterExpr // AND-joined predicates
	Aggregation string               // count, rate, error_rate, p50/p95/p99/avg/max/min
	Field       string               // empty for count/rate/error_rate; the column for quantile/avg
	MetricName  string               // metrics-table-only — the metric being queried
	StepSeconds int                  // 0 = auto-pick from window width
}

// Compile parses a DQL string into a Plan. Returns a wrapped
// parse error with the column number on syntax failure so the
// /admin/query UI can underline the offending token.
func Compile(q string) (*Plan, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, fmt.Errorf("query is empty")
	}
	steps := splitPipes(q)
	if len(steps) == 0 {
		return nil, fmt.Errorf("query must start with a table name")
	}

	tbl := strings.TrimSpace(steps[0])
	plan := &Plan{}
	switch tbl {
	case "spans":
		plan.Table = TableSpans
	case "metrics":
		plan.Table = TableMetrics
		return nil, fmt.Errorf("metrics queries must name the metric — use `metrics http.server.duration | …`")
	case "logs":
		plan.Table = TableLogs
		return nil, fmt.Errorf("logs DQL not yet implemented — use the /logs page for now")
	default:
		// Metrics-with-name shortcut: `metrics <name> | filter …`
		if strings.HasPrefix(tbl, "metrics ") {
			plan.Table = TableMetrics
			plan.MetricName = strings.TrimSpace(strings.TrimPrefix(tbl, "metrics"))
			if plan.MetricName == "" {
				return nil, fmt.Errorf("metrics requires a metric name: `metrics http.server.duration | …`")
			}
		} else {
			return nil, fmt.Errorf("unknown table %q — try `spans` or `metrics <name>`", tbl)
		}
	}

	for i := 1; i < len(steps); i++ {
		step := strings.TrimSpace(steps[i])
		if step == "" {
			continue
		}
		if err := applyStep(plan, step); err != nil {
			return nil, fmt.Errorf("step %d (%q): %w", i, step, err)
		}
	}

	if plan.Aggregation == "" {
		// Default — operator wrote `spans | filter X==Y` with no
		// summarize. Implicit count() over the matching rows.
		plan.Aggregation = "count"
	}
	return plan, nil
}

// applyStep dispatches a single pipe segment to its handler.
func applyStep(plan *Plan, step string) error {
	switch {
	case strings.HasPrefix(step, "filter"):
		return parseFilter(plan, strings.TrimSpace(strings.TrimPrefix(step, "filter")))
	case strings.HasPrefix(step, "where"):
		return parseFilter(plan, strings.TrimSpace(strings.TrimPrefix(step, "where")))
	case strings.HasPrefix(step, "summarize"):
		return parseSummarize(plan, strings.TrimSpace(strings.TrimPrefix(step, "summarize")))
	default:
		return fmt.Errorf("unknown operator: %s", firstWord(step))
	}
}

// parseFilter handles `filter ident op value`. Quoted values
// (single or double) preserve whitespace; unquoted are taken
// as bare identifiers / numbers.
func parseFilter(plan *Plan, expr string) error {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return fmt.Errorf("filter expects: ident op value")
	}
	// Try operators longest-first so "==" doesn't get split by "=".
	ops := []struct {
		token string
		fop   string
	}{
		{"==", "="},
		{"!=", "!="},
		{"contains", "LIKE"},
		{"startswith", "LIKE"},
		{"endswith", "LIKE"},
		{">=", ">="},
		{"<=", "<="},
		{">", ">"},
		{"<", "<"},
	}
	for _, op := range ops {
		idx := strings.Index(expr, op.token)
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(expr[:idx])
		raw := strings.TrimSpace(expr[idx+len(op.token):])
		val := unquote(raw)
		switch op.token {
		case "contains":
			val = "%" + val + "%"
		case "startswith":
			val = val + "%"
		case "endswith":
			val = "%" + val
		}
		plan.Filters = append(plan.Filters, chstore.FilterExpr{
			Key:    key,
			Op:     op.fop,
			Values: []string{val},
		})
		return nil
	}
	return fmt.Errorf("no recognised operator in %q (try ==, !=, contains, startswith, endswith, >, <, >=, <=)", expr)
}

// parseSummarize handles `summarize agg by bin(time, Nm)`.
func parseSummarize(plan *Plan, expr string) error {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return fmt.Errorf("summarize expects: agg [by bin(time, duration)]")
	}
	// Split into the agg + optional "by …".
	var aggPart, byPart string
	if i := strings.Index(strings.ToLower(expr), " by "); i >= 0 {
		aggPart = strings.TrimSpace(expr[:i])
		byPart = strings.TrimSpace(expr[i+len(" by "):])
	} else {
		aggPart = expr
	}

	// Aggregation function — case-insensitive.
	low := strings.ToLower(aggPart)
	switch {
	case low == "count()":
		plan.Aggregation = "count"
	case low == "rate()":
		plan.Aggregation = "rate"
	case low == "error_rate()":
		plan.Aggregation = "error_rate"
	case strings.HasPrefix(low, "p50("),
		strings.HasPrefix(low, "p95("),
		strings.HasPrefix(low, "p99("),
		strings.HasPrefix(low, "avg("),
		strings.HasPrefix(low, "max("),
		strings.HasPrefix(low, "min("):
		open := strings.Index(aggPart, "(")
		close := strings.LastIndex(aggPart, ")")
		if open < 0 || close < 0 || close <= open {
			return fmt.Errorf("aggregation must look like p99(field): %s", aggPart)
		}
		plan.Aggregation = strings.ToLower(aggPart[:open])
		plan.Field = strings.TrimSpace(aggPart[open+1 : close])
		if plan.Field == "" {
			return fmt.Errorf("%s aggregation needs a field name", plan.Aggregation)
		}
	default:
		return fmt.Errorf("unknown aggregation %q — try count(), rate(), p99(field), avg(field), error_rate()", aggPart)
	}

	if byPart != "" {
		if !strings.HasPrefix(byPart, "bin(time,") {
			return fmt.Errorf("only bin(time, …) grouping is supported (got %q)", byPart)
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(byPart, "bin(time,"), ")")
		inner = strings.TrimSpace(inner)
		secs, err := parseDuration(inner)
		if err != nil {
			return fmt.Errorf("bin(time, …) duration: %w", err)
		}
		plan.StepSeconds = secs
	}
	return nil
}

// splitPipes splits the input on top-level `|`. Quoted strings
// preserve embedded pipes; we don't yet support escapes.
func splitPipes(s string) []string {
	out := []string{}
	var cur strings.Builder
	q := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q == 0 && (c == '\'' || c == '"'):
			q = c
			cur.WriteByte(c)
		case q != 0 && c == q:
			q = 0
			cur.WriteByte(c)
		case q == 0 && c == '|':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func parseDuration(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	unit := s[len(s)-1]
	num := s[:len(s)-1]
	n, err := strconv.Atoi(num)
	if err != nil {
		return 0, fmt.Errorf("bad duration value: %s", s)
	}
	switch unit {
	case 's':
		return n, nil
	case 'm':
		return n * 60, nil
	case 'h':
		return n * 3600, nil
	case 'd':
		return n * 86400, nil
	}
	return 0, fmt.Errorf("unknown duration unit %c (use s/m/h/d)", unit)
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

// SQLPreview returns a representative ClickHouse query string
// for the Plan — strictly for the operator-visible "show the
// SQL" affordance in /admin/query. The actual execution runs
// through the typed chstore methods (which build parameterised
// queries); this preview is illustrative, not executed.
func (p *Plan) SQLPreview(from, to time.Time) string {
	var b strings.Builder
	switch p.Table {
	case TableSpans:
		b.WriteString("SELECT toStartOfInterval(time, INTERVAL ")
		if p.StepSeconds > 0 {
			fmt.Fprintf(&b, "%d SECOND) AS bucket, ", p.StepSeconds)
		} else {
			b.WriteString("60 SECOND) AS bucket, ")
		}
		b.WriteString(aggToSQLFragment(p.Aggregation, p.Field))
		b.WriteString(" AS value\nFROM spans\nWHERE time >= '")
		b.WriteString(from.UTC().Format("2006-01-02 15:04:05"))
		b.WriteString("' AND time <= '")
		b.WriteString(to.UTC().Format("2006-01-02 15:04:05"))
		b.WriteString("'")
		for _, f := range p.Filters {
			fmt.Fprintf(&b, "\n  AND %s %s '%s'", f.Key, f.Op, escapeSQL(firstString(f.Values)))
		}
		b.WriteString("\nGROUP BY bucket\nORDER BY bucket\nSETTINGS max_execution_time = 30")
	case TableMetrics:
		b.WriteString("SELECT toStartOfInterval(time, INTERVAL ")
		if p.StepSeconds > 0 {
			fmt.Fprintf(&b, "%d SECOND) AS bucket, ", p.StepSeconds)
		} else {
			b.WriteString("60 SECOND) AS bucket, ")
		}
		b.WriteString(aggToSQLFragment(p.Aggregation, "value"))
		b.WriteString(" AS value\nFROM metric_points\nWHERE metric = '")
		b.WriteString(escapeSQL(p.MetricName))
		b.WriteString("'")
		b.WriteString(" AND time >= '")
		b.WriteString(from.UTC().Format("2006-01-02 15:04:05"))
		b.WriteString("' AND time <= '")
		b.WriteString(to.UTC().Format("2006-01-02 15:04:05"))
		b.WriteString("'")
		for _, f := range p.Filters {
			fmt.Fprintf(&b, "\n  AND %s %s '%s'", f.Key, f.Op, escapeSQL(firstString(f.Values)))
		}
		b.WriteString("\nGROUP BY bucket\nORDER BY bucket\nSETTINGS max_execution_time = 30")
	}
	return b.String()
}

func aggToSQLFragment(agg, field string) string {
	if field == "" {
		field = "duration_ms"
	}
	switch agg {
	case "count":
		return "count()"
	case "rate":
		return "count() / 60.0"
	case "error_rate":
		return "countIf(status_code = 'error') / count()"
	case "avg":
		return "avg(" + field + ")"
	case "max":
		return "max(" + field + ")"
	case "min":
		return "min(" + field + ")"
	case "p50":
		return "quantile(0.5)(" + field + ")"
	case "p95":
		return "quantile(0.95)(" + field + ")"
	case "p99":
		return "quantile(0.99)(" + field + ")"
	}
	return agg + "(" + field + ")"
}

func escapeSQL(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func firstString(v []string) string {
	if len(v) > 0 {
		return v[0]
	}
	return ""
}
