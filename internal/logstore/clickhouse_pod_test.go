package logstore

import (
	"strings"
	"testing"
)

// v0.9.1249 — CH tarafındaki pod türetimi. Logs tablosu attribute'ları
// PARALEL DİZİ tutar (attr_keys/attr_values + res_keys/res_values),
// Map kolonu YOK: Map erişimi yazan ifade v0.8.400'de UNKNOWN_IDENTIFIER
// ile patlamıştı ve CH backend'i o yolu kullanmadığı için uzun süre
// sessiz kalmıştı. Yeni ifade doğduğu anda şekli çakılıyor.

func TestChLogsPodExpr_ResArrayLookup(t *testing.T) {
	for _, want := range []string{
		"res_values[indexOf(res_keys, 'k8s.pod.name')]",
		"res_values[indexOf(res_keys, 'kubernetes.pod_name')]",
		"res_values[indexOf(res_keys, 'kubernetes.pod.name')]",
		"res_values[indexOf(res_keys, 'pod_name')]",
		"coalesce(",
	} {
		if !strings.Contains(chLogsPodExpr, want) {
			t.Errorf("chLogsPodExpr missing %q:\n%s", want, chLogsPodExpr)
		}
	}
	// Kanonik semconv anahtarı ÖNCE coalesce edilmeli (env/cluster
	// zincirlerindeki aynı kural).
	canonical := strings.Index(chLogsPodExpr, "'k8s.pod.name'")
	legacy := strings.Index(chLogsPodExpr, "'kubernetes.pod_name'")
	if canonical < 0 || legacy < 0 || canonical > legacy {
		t.Fatalf("kanonik k8s.pod.name önce gelmeli:\n%s", chLogsPodExpr)
	}
	for _, banned := range []string{
		"resource_attributes[", "attributes[",
		// ES DOKÜMAN yolu; OTLP resource anahtarı olarak asla var
		// olmaz — buraya konursa ölü SQL olur.
		"resource_attributes.k8s.pod.name",
	} {
		if strings.Contains(chLogsPodExpr, banned) {
			t.Errorf("chLogsPodExpr must not contain %q:\n%s", banned, chLogsPodExpr)
		}
	}
}
