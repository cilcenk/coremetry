package chstore

import (
	"strings"
	"testing"
)

// v0.8.x — free-text trace search moved from a single-needle 4-leg OR-chain
// (positionCaseInsensitive over name/route/method/attrs) to ALL-tokens match
// via searchPredicate: whitespace-split the query, every token must appear in
// the combined haystack (multiSearchAllPositions + arrayAll). These pin the
// pure builder's contract — token count, arg pairing, blank-input no-op —
// since three call sites (GetTraces HAVING, GetTraceAggregate inner HAVING,
// spanmetric WHERE) depend on the SQL and args staying in lockstep.
func TestSearchPredicate(t *testing.T) {
	t.Run("blank and whitespace-only yield no predicate", func(t *testing.T) {
		for _, in := range []string{"", "   ", "\t \n"} {
			sql, args := searchPredicate(in)
			if sql != "" || args != nil {
				t.Fatalf("searchPredicate(%q) = (%q, %v); want (\"\", nil) so callers add no predicate", in, sql, args)
			}
		}
	})

	t.Run("single token = one placeholder, one arg", func(t *testing.T) {
		sql, args := searchPredicate("account")
		if n := strings.Count(sql, "?"); n != 1 {
			t.Fatalf("placeholder count = %d; want 1\nSQL: %s", n, sql)
		}
		if len(args) != 1 || args[0] != "account" {
			t.Fatalf("args = %v; want [account]", args)
		}
		if !strings.Contains(sql, "multiSearchAllPositionsCaseInsensitiveUTF8") || !strings.Contains(sql, "arrayAll(p -> p > 0") {
			t.Fatalf("SQL missing the ALL-tokens machinery: %s", sql)
		}
	})

	t.Run("multi-token = placeholder + arg per token, in order", func(t *testing.T) {
		sql, args := searchPredicate("  GET   /account  timeout ")
		if n := strings.Count(sql, "?"); n != 3 {
			t.Fatalf("placeholder count = %d; want 3 (one per token)\nSQL: %s", n, sql)
		}
		want := []string{"GET", "/account", "timeout"}
		if len(args) != len(want) {
			t.Fatalf("args = %v; want %v", args, want)
		}
		for i, w := range want {
			if args[i] != w {
				t.Fatalf("args[%d] = %v; want %q (token order must match placeholder order)", i, args[i], w)
			}
		}
	})
}
