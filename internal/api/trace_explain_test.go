package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/auth"
)

// v0.10.326 — explain yalnız admin; nil claims/viewer/editor reddedilir.
func TestExplainAllowed(t *testing.T) {
	if explainAllowed(nil) {
		t.Error("nil claims")
	}
	for _, r := range []string{auth.RoleViewer, auth.RoleEditor, "custom"} {
		if explainAllowed(&auth.Claims{Role: r}) {
			t.Errorf("%s izin almamalı", r)
		}
	}
	if !explainAllowed(&auth.Claims{Role: auth.RoleAdmin}) {
		t.Error("admin izinli olmalı")
	}
}
