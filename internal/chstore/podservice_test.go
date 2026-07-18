package chstore

import (
	"strings"
	"testing"
)

// v0.9.52 (openshift-cluster-attr audit B1) — pod↔servis eşleşmesinin
// cluster filtresi merkez clusterDeriveExpr'i kullanmalı: salt
// openshift.cluster.name basan OpenShift cluster'larında filtre boş
// eşleşiyordu (Service kolonu + Service→Infra korelasyonu kayboluyordu).
// Literal'e geri dönüş bu testi kırar.
func TestPodServiceMapSQLUsesClusterDeriveExpr(t *testing.T) {
	for _, key := range []string{
		"k8s.cluster.name", "openshift.cluster.name", "'cluster'",
	} {
		if !strings.Contains(podServiceMapSQL, key) {
			t.Errorf("podServiceMapSQL %s yedeğini kaybetmiş", key)
		}
	}
	if !strings.Contains(podServiceMapSQL, "attr_values") {
		t.Error("attr-yolu yedeği kayıp (res-only'ye gerileme)")
	}
	if !strings.Contains(podServiceMapSQL, clusterDeriveExpr) {
		t.Error("podServiceMapSQL merkez clusterDeriveExpr'i kullanmıyor — zincirler ayrışabilir")
	}
}
