package logstore

import (
	"os"
	"strings"
	"testing"
)

// v0.9.452 (operatör isteği, prod OpenShift ES) — cluster filtresi üç
// OTel resource_attributes yolunu tarıyordu; OpenShift cluster-logging
// (ClusterLogForwarder) cluster adını ÜST-DÜZEY openshift.labels.cluster
// alanına yazar. Prod'da /logs cluster seçimi bu yüzden HİÇBİR kaydı
// eşleştirmiyordu. Pin: yol listede kalır.
func TestClusterFilterCoversOpenShiftLabels(t *testing.T) {
	b, err := os.ReadFile("elasticsearch.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, `"resource_attributes.k8s.cluster.name",`)
	if i < 0 {
		t.Fatal("cluster path list not found")
	}
	if !strings.Contains(src[i:i+400], `"openshift.labels.cluster",`) {
		t.Error("openshift.labels.cluster missing from the cluster filter paths — OpenShift cluster-logging indices match on the TOP-LEVEL field, not resource_attributes.*; the /logs cluster picker matches nothing on prod without it")
	}
}

// v0.9.452 — alfabetik listFieldsMax kırpması geç harfli konvansiyonel
// yolları (openshift.*) düşürüyordu; frontend'in Popular fields grubu
// "listede-varlık = gerçek" sözleşmesiyle çalıştığından cap'e kurban
// giden yol geri eklenir. seen'de OLMAYAN alan asla icat edilmez.
func TestEnsureConventionalFields(t *testing.T) {
	seen := map[string]struct{}{
		"aaa": {}, "bbb": {},
		"openshift.labels.cluster": {},
		"kubernetes.pod_name":      {},
	}
	out := ensureConventionalFields([]string{"aaa", "bbb"}, seen)
	joined := strings.Join(out, "|")
	if !strings.Contains(joined, "openshift.labels.cluster") || !strings.Contains(joined, "kubernetes.pod_name") {
		t.Errorf("mapping'de var olan konvansiyonel yollar geri eklenmedi: %v", out)
	}
	if strings.Contains(joined, "service_name") {
		t.Errorf("mapping'de OLMAYAN alan icat edildi: %v", out)
	}
	// idempotent: zaten listedeyse çoğaltma
	out2 := ensureConventionalFields(out, seen)
	if len(out2) != len(out) {
		t.Errorf("duplicate append: %v", out2)
	}
}
