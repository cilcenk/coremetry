package chstore

import (
	"strings"
	"testing"
)

// TestFilterExprRegex — v0.9.118 PromQL regex matchers (=~/!~) → ClickHouse
// match() (RE2). PromQL fully anchors the pattern, CH match() does not, so the
// value must be wrapped ^(?:…)$; !~ negates. Pins both against a silent drift
// back to substring LIKE.
func TestFilterExprRegex(t *testing.T) {
	pos := FilterExpr{Key: "code", Op: "=~", Values: []string{"5.."}}
	sql, args, err := pos.SQLForMetricPoints()
	if err != nil {
		t.Fatalf("=~ err: %v", err)
	}
	if !strings.Contains(sql, "match(") {
		t.Errorf("=~ must use match(), got %q", sql)
	}
	// code is an attr (not a well-known column), so args = [key, pattern] — the
	// anchored pattern is the LAST bound arg.
	if len(args) == 0 || args[len(args)-1] != "^(?:5..)$" {
		t.Errorf("=~ pattern must be anchored ^(?:…)$, got %v", args)
	}

	neg := FilterExpr{Key: "method", Op: "!~", Values: []string{"GET|HEAD"}}
	sqln, argsn, err := neg.SQLForMetricPoints()
	if err != nil {
		t.Fatalf("!~ err: %v", err)
	}
	if !strings.Contains(sqln, "NOT (match(") {
		t.Errorf("!~ must be NOT match(), got %q", sqln)
	}
	if len(argsn) == 0 || argsn[len(argsn)-1] != "^(?:GET|HEAD)$" {
		t.Errorf("!~ pattern must be anchored, got %v", argsn)
	}

	// Also works on the spans SQL path (both delegate to f.sql).
	if s, _, err := pos.SQL(); err != nil || !strings.Contains(s, "match(") {
		t.Errorf("spans =~ SQL = %q (err %v)", s, err)
	}
}
