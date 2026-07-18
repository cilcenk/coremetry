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

// v0.9.53 (openshift-attr audit B2, operatör onayı) — deriver
// zincirlerinin OpenShift/legacy yedekleri: standart semconv önde,
// kubernetes.* varyantları ve (deployment'ta) DeploymentConfig yedeği
// arkada. Anahtar düşerse mapping OpenShift filosunda sessizce boşalır.
func TestDeriverChainsCarryOpenShiftFallbacks(t *testing.T) {
	nsKeys := []string{
		"service.namespace", "k8s.namespace.name",
		"kubernetes.namespace.name", "kubernetes.namespace_name",
	}
	for _, k := range nsKeys {
		if !strings.Contains(deriveNamespaceSQL, "'"+k+"'") {
			t.Errorf("deriveNamespaceSQL %q anahtarını kaybetmiş", k)
		}
	}
	depKeys := []string{
		"k8s.deployment.name",
		"kubernetes.deployment.name", "kubernetes.deployment_name",
		"openshift.deploymentconfig.name",
	}
	for _, k := range depKeys {
		if !strings.Contains(deriveDeploymentSQL, "'"+k+"'") {
			t.Errorf("deriveDeploymentSQL %q anahtarını kaybetmiş", k)
		}
	}
	// Sıra sözleşmesi: standart semconv anahtarı legacy varyanttan ÖNCE
	// (semconv basan kurulumda davranış değişmemeli).
	if strings.Index(deriveNamespaceSQL, "'k8s.namespace.name'") >
		strings.Index(deriveNamespaceSQL, "'kubernetes.namespace.name'") {
		t.Error("namespace zincirinde legacy varyant standart anahtarın önüne geçmiş")
	}
	if strings.Index(deriveDeploymentSQL, "'k8s.deployment.name'") >
		strings.Index(deriveDeploymentSQL, "'kubernetes.deployment.name'") {
		t.Error("deployment zincirinde legacy varyant standart anahtarın önüne geçmiş")
	}
	// app-label takma adı BİLİNÇLİ dışarıda (yanlış eşleşme riski).
	if strings.Contains(deriveDeploymentSQL, "kubernetes.labels.app") {
		t.Error("kubernetes.labels.app deriver'a girmemeli (audit B2 kararı)")
	}
}
