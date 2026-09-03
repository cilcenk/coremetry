package api

import (
	"os"
	"strings"
	"testing"
)

// admin_attr_index_test.go — v0.10.306: beş uç deftere kayıtlı ve admin
// kapılı; apply ön kontrolden geçer; audit üçlüsü var; api.go büyümedi.
func TestAttrIndexAdminRoutes(t *testing.T) {
	b, err := os.ReadFile("admin_attr_index.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`registerRoutesExtra("attr-index"`,
		`"GET /api/admin/attr-index/status"`, `"GET /api/admin/attr-index/preflight"`,
		`"POST /api/admin/attr-index/apply"`, `"POST /api/admin/attr-index/materialize"`, `"POST /api/admin/attr-index/rollback"`,
		`s.audit(r, "attr_index.apply"`, `s.audit(r, "attr_index.materialize"`, `s.audit(r, "attr_index.rollback"`,
		"pre.Supported", "http.StatusConflict",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("%q yok", want)
		}
	}
	if strings.Count(src, "auth.RequireRole(auth.RoleAdmin") != 5 {
		t.Error("beş uç da admin kapılı olmalı")
	}
	if strings.Contains(readAPISourceNoComments(t, "api.go"), "attr-index") {
		t.Error("api.go büyümemeli")
	}
}
