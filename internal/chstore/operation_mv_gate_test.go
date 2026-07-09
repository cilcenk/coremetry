package chstore

import "testing"

// operation_mv_gate_test.go — v0.8.425. Review-confirmed gap: the
// operation-scoped RED queries (v0.8.414 — filters {service.name,
// name}, groupBy []) lost EVERY MV path on ≥5m fallback windows and
// fell to raw-spans scans, while their unscoped siblings rode
// operation_summary_5m. operationMVGate is the extracted PURE
// eligibility half of both operation fast-paths; this table pins the
// old accept/reject behaviour (regression) plus the new name-filter
// shapes.
func TestOperationMVGate(t *testing.T) {
	svcF := FilterExpr{Key: "service.name", Op: "=", Values: []string{"checkout"}}
	nameF := FilterExpr{Key: "name", Op: "=", Values: []string{"GET /cart"}}

	tests := []struct {
		name        string
		groupBy     []string
		filters     []FilterExpr
		wantOK      bool
		wantGroup   string
		wantService string
		wantOp      string
	}{
		// ── pre-v0.8.425 behaviour, pinned ──
		{"classic split: service filter + groupBy name",
			[]string{"name"}, []FilterExpr{svcF}, true, "[name]", "checkout", ""},
		{"two-key split, service first",
			[]string{"service.name", "name"}, nil, true, "[service_name, name]", "", ""},
		{"two-key split, name first",
			[]string{"name", "service.name"}, nil, true, "[name, service_name]", "", ""},
		{"groupBy name without any service scope → refused",
			[]string{"name"}, nil, false, "", "", ""},
		{"foreign groupBy key → refused",
			[]string{"http.method"}, []FilterExpr{svcF}, false, "", "", ""},
		{"foreign filter key → refused",
			[]string{"name"}, []FilterExpr{svcF, {Key: "http.method", Op: "=", Values: []string{"GET"}}},
			false, "", "", ""},
		{"non-equality name filter → refused",
			[]string{}, []FilterExpr{svcF, {Key: "name", Op: "!=", Values: []string{"x"}}},
			false, "", "", ""},
		{"service-only groupBy with no operation axis → refused (service MV territory)",
			[]string{"service.name"}, []FilterExpr{svcF}, false, "", "", ""},

		// ── v0.8.425: operation pinned by FILTER ──
		{"scoped RED shape: service+name filters, empty groupBy",
			[]string{}, []FilterExpr{svcF, nameF}, true, "[]::Array(String)", "checkout", "GET /cart"},
		{"operation alias key works as filter",
			[]string{}, []FilterExpr{svcF, {Key: "operation", Op: "=", Values: []string{"GET /cart"}}},
			true, "[]::Array(String)", "checkout", "GET /cart"},
		{"name filter + service split",
			[]string{"service.name"}, []FilterExpr{svcF, nameF}, true, "[service_name]", "checkout", "GET /cart"},
		{"name filter + name split (double axis) still fine",
			[]string{"name"}, []FilterExpr{svcF, nameF}, true, "[name]", "checkout", "GET /cart"},
		{"name filter WITHOUT service scope → still refused (cross-service op)",
			[]string{}, []FilterExpr{nameF}, false, "", "", ""},
		{"multi-value name IN → refused",
			[]string{}, []FilterExpr{svcF, {Key: "name", Op: "=", Values: []string{"a", "b"}}},
			false, "", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, ok := operationMVGate(tc.groupBy, tc.filters)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if plan.groupSelect != tc.wantGroup {
				t.Errorf("groupSelect = %q, want %q", plan.groupSelect, tc.wantGroup)
			}
			if plan.serviceFilter != tc.wantService {
				t.Errorf("serviceFilter = %q, want %q", plan.serviceFilter, tc.wantService)
			}
			if plan.nameFilter != tc.wantOp {
				t.Errorf("nameFilter = %q, want %q", plan.nameFilter, tc.wantOp)
			}
		})
	}
}
