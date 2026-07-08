package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.8.383 — env-separation Phase 1: the global Topbar env picker's
// ?env= lands on TraceFilter.Env / AggregateFilter.Env as a
// FIRST-CLASS conjunct, not an injected FilterExpr. The risk this
// pins: the FilterRoot-supersedes-Filters rule (buildGetTracesWhere)
// would silently DROP an env leaf appended to .Filters whenever the
// operator has a grouped OR/nested filter active — and wrapping the
// group one level deeper hits the FilterGroup depth cap (a nested
// group's own Groups are ignored, filterexpr.go). So Env must emit
// its own always-AND `deploy_env = ?` regardless of which filter
// path is in play.

func TestTraceFilterEnv_AlwaysAndConjunct(t *testing.T) {
	from := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	to := from.Add(1 * time.Hour)

	cases := []struct {
		name string
		f    TraceFilter
	}{
		{"env only", TraceFilter{From: from, To: to, Env: "uat"}},
		{"env + legacy flat filters", TraceFilter{
			From: from, To: to, Env: "uat",
			Filters: []FilterExpr{{Key: "http.status_code", Op: ">=", Values: []string{"500"}}},
		}},
		// The bug class the first-class field prevents: FilterRoot
		// supersedes Filters, so an env injected as a filter leaf
		// would vanish here. Env must still emit its conjunct.
		{"env + OR FilterRoot", TraceFilter{
			From: from, To: to, Env: "uat",
			FilterRoot: &FilterGroup{Join: "OR", Filters: []FilterExpr{
				{Key: "http.status_code", Op: ">=", Values: []string{"500"}},
				{Key: "db.system", Op: "=", Values: []string{"oracle"}},
			}},
		}},
		// Nested group at the depth cap — wrapping would have dropped
		// the inner Groups; the first-class Env doesn't touch them.
		{"env + nested FilterRoot", TraceFilter{
			From: from, To: to, Env: "prep",
			FilterRoot: &FilterGroup{Join: "OR",
				Filters: []FilterExpr{{Key: "db.system", Op: "=", Values: []string{"oracle"}}},
				Groups: []FilterGroup{{Join: "AND", Filters: []FilterExpr{
					{Key: "http.status_code", Op: ">=", Values: []string{"500"}},
				}}},
			},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wc := buildGetTracesWhere(tc.f)
			sql := wc.sql()
			if !strings.Contains(sql, "deploy_env = ?") {
				t.Fatalf("WHERE must carry the env conjunct; got %q", sql)
			}
			found := false
			for _, a := range wc.args {
				if a == tc.f.Env {
					found = true
				}
			}
			if !found {
				t.Fatalf("args must carry the env value %q; got %#v", tc.f.Env, wc.args)
			}
			// The operator's own predicates must survive alongside env
			// (supersede rule untouched).
			if tc.f.FilterRoot != nil && !strings.Contains(sql, " OR ") {
				t.Fatalf("FilterRoot's OR group must survive next to env; got %q", sql)
			}
		})
	}
}

func TestTraceFilterEnv_EmptyMeansAllEnvironments(t *testing.T) {
	from := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	wc := buildGetTracesWhere(TraceFilter{From: from, To: from.Add(time.Hour)})
	if strings.Contains(wc.sql(), "deploy_env") {
		t.Fatalf("empty Env must add no deploy_env predicate; got %q", wc.sql())
	}
}
