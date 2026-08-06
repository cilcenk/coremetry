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
	// v0.9.55 — herhangi-biri-eşleşirse (? IN (...)): coalesce önceliği
	// iki anahtar farklı değer bastığında ikinci adı maskeliyordu.
	// IN seti derive zincirinin 6 anahtarının TAMAMINI taşımalı.
	if !strings.Contains(podServiceMapSQL, "? IN (") {
		t.Error("cluster filtresi any-of (? IN) formunda değil — öncelik maskelemesi geri gelir")
	}
	for _, frag := range []string{
		"res_values[indexOf(res_keys, 'k8s.cluster.name')]",
		"res_values[indexOf(res_keys, 'openshift.cluster.name')]",
		"res_values[indexOf(res_keys, 'cluster')]",
		"attr_values[indexOf(attr_keys, 'k8s.cluster.name')]",
		"attr_values[indexOf(attr_keys, 'openshift.cluster.name')]",
		"attr_values[indexOf(attr_keys, 'cluster')]",
	} {
		if !strings.Contains(podServiceMapSQL, frag) {
			t.Errorf("any-of setinde eksik yol: %s", frag)
		}
	}
}

// v0.9.53 (openshift-attr audit B2, operatör onayı) — deriver
// zincirlerinin OpenShift/legacy yedekleri: standart semconv önde,
// v0.9.715 — üç deriver deriveMetadataAllSQL'de birleşti; bu pin aynı
// anahtar-kaybı sözleşmesini yeni kaynakta sürdürüyor.
// kubernetes.* varyantları ve (deployment'ta) DeploymentConfig yedeği
// arkada. Anahtar düşerse mapping OpenShift filosunda sessizce boşalır.
func TestDeriverChainsCarryOpenShiftFallbacks(t *testing.T) {
	nsKeys := []string{
		"service.namespace", "k8s.namespace.name",
		"kubernetes.namespace.name", "kubernetes.namespace_name",
	}
	for _, k := range nsKeys {
		if !strings.Contains(deriveMetadataAllSQL, "'"+k+"'") {
			t.Errorf("deriveMetadataAllSQL %q anahtarını kaybetmiş", k)
		}
	}
	depKeys := []string{
		"k8s.deployment.name",
		"kubernetes.deployment.name", "kubernetes.deployment_name",
		"openshift.deployment.name",
	}
	for _, k := range depKeys {
		if !strings.Contains(deriveMetadataAllSQL, "'"+k+"'") {
			t.Errorf("deriveMetadataAllSQL %q anahtarını kaybetmiş", k)
		}
	}
	// Sıra sözleşmesi: standart semconv anahtarı legacy varyanttan ÖNCE
	// (semconv basan kurulumda davranış değişmemeli).
	if strings.Index(deriveMetadataAllSQL, "'k8s.namespace.name'") >
		strings.Index(deriveMetadataAllSQL, "'kubernetes.namespace.name'") {
		t.Error("namespace zincirinde legacy varyant standart anahtarın önüne geçmiş")
	}
	if strings.Index(deriveMetadataAllSQL, "'k8s.deployment.name'") >
		strings.Index(deriveMetadataAllSQL, "'kubernetes.deployment.name'") {
		t.Error("deployment zincirinde legacy varyant standart anahtarın önüne geçmiş")
	}
	// app-label takma adı BİLİNÇLİ dışarıda (yanlış eşleşme riski).
	if strings.Contains(deriveMetadataAllSQL, "kubernetes.labels.app") {
		t.Error("kubernetes.labels.app deriver'a girmemeli (audit B2 kararı)")
	}
}
