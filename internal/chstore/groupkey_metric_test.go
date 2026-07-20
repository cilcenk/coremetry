package chstore

import (
	"strings"
	"testing"
)

// v0.9.126 review MAJOR — the metric_points GROUP BY resolver must NOT emit
// spans-only columns (http_method, db_system, …); a colliding attr key like
// http.method is a datapoint-attr array lookup, not a nonexistent column.
func TestGroupKeyExprMetric(t *testing.T) {
	cases := []struct {
		key    string
		subExpr string // must appear
		notCol  string // must NOT appear (spans column)
	}{
		{"service.name", "service_name", ""},
		{"host.name", "host_name", ""},
		{"http.method", "attr_values[indexOf(attr_keys", "http_method"},
		{"db.system", "attr_values[indexOf(attr_keys", "db_system"},
		{"status_code", "attr_values[indexOf(attr_keys", "status_code)"},
		{"resource.k8s.pod", "res_values[indexOf(res_keys", ""},
		{"deployment.environment", "coalesce", ""},
	}
	for _, c := range cases {
		expr, _ := groupKeyExprMetric(c.key)
		if !strings.Contains(expr, c.subExpr) {
			t.Errorf("groupKeyExprMetric(%q) = %q, want to contain %q", c.key, expr, c.subExpr)
		}
		if c.notCol != "" && strings.Contains(expr, c.notCol) {
			t.Errorf("groupKeyExprMetric(%q) = %q, must NOT reference spans column %q", c.key, expr, c.notCol)
		}
	}
}
