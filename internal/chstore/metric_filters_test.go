package chstore

import (
	"strings"
	"testing"
)

// v0.8.381 — audit-found: a bare deployment.environment (or any other
// spans-typed wellKnown key) in an Explore METRIC filter resolved to a
// column metric_points doesn't have → ClickHouse missing-column error.
// ApplyMetricFilters reroutes those keys to the array-lookup forms.
func TestApplyMetricFiltersReroutesSpansColumns(t *testing.T) {
	cases := []struct {
		name     string
		f        FilterExpr
		wantSub  string   // fragment that MUST appear
		banSubs  []string // fragments that must NOT appear
	}{
		{"env → resource lookup",
			FilterExpr{Key: "deployment.environment", Op: "=", Values: []string{"uat"}},
			"res_values[indexOf(res_keys", []string{"deploy_env"}},
		{"env new spelling → resource lookup",
			FilterExpr{Key: "deployment.environment.name", Op: "=", Values: []string{"int"}},
			"res_values[indexOf(res_keys", []string{"deploy_env"}},
		{"messaging.system → datapoint attr lookup",
			FilterExpr{Key: "messaging.system", Op: "=", Values: []string{"kafka"}},
			"attr_values[indexOf(attr_keys", []string{"msg_system"}},
		{"host.name keeps its metric_points column",
			FilterExpr{Key: "host.name", Op: "=", Values: []string{"n1"}},
			"host_name", nil},
		{"service.name keeps its metric_points column",
			FilterExpr{Key: "service.name", Op: "=", Values: []string{"api"}},
			"service_name", nil},
		{"explicit resource. prefix untouched",
			FilterExpr{Key: "resource.deployment.environment", Op: "=", Values: []string{"uat"}},
			"res_values[indexOf(res_keys", []string{"deploy_env"}},
		{"unknown key stays generic attr lookup",
			FilterExpr{Key: "custom.tag", Op: "=", Values: []string{"x"}},
			"attr_values[indexOf(attr_keys", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var wc whereClause
			ApplyMetricFilters(&wc, []FilterExpr{c.f})
			sql := wc.sql()
			if !strings.Contains(sql, c.wantSub) {
				t.Fatalf("missing %q\n--- SQL ---\n%s", c.wantSub, sql)
			}
			for _, ban := range c.banSubs {
				if strings.Contains(sql, ban) {
					t.Fatalf("must not reference %q\n--- SQL ---\n%s", ban, sql)
				}
			}
		})
	}
}

// The spans path is untouched: bare env still resolves to the fast
// typed column there.
func TestApplyFiltersSpansPathUnchanged(t *testing.T) {
	var wc whereClause
	ApplyFilters(&wc, []FilterExpr{{Key: "deployment.environment", Op: "=", Values: []string{"uat"}}})
	if !strings.Contains(wc.sql(), "deploy_env") {
		t.Fatalf("spans path lost the typed column\n%s", wc.sql())
	}
}
