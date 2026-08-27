package selfobs

// selfobs_k8s_test.go — v0.10.91. /api/k8s/coverage denetimi
// coremetry-monolithic'in YEDİ k8s alanını da sıfır buldu: bu resource
// hiç k8s.* üretmiyordu ve hattaki collector'da k8sattributes yok.
// Downward API env'leri (chart v0.10.2) buradaki dört attribute'a akar.

import (
	"testing"
)

func resourceAttrMap(t *testing.T) map[string]string {
	t.Helper()
	res, err := buildResource("all", "test")
	if err != nil {
		t.Fatalf("buildResource: %v", err)
	}
	out := map[string]string{}
	for _, kv := range res.Attributes() {
		out[string(kv.Key)] = kv.Value.Emit()
	}
	return out
}

func TestBuildResourceCarriesDownwardAPIK8sAttrs(t *testing.T) {
	t.Setenv("COREMETRY_K8S_NAMESPACE", "obs")
	t.Setenv("COREMETRY_K8S_POD_NAME", "coremetry-abc12")
	t.Setenv("COREMETRY_K8S_POD_UID", "11111111-2222-3333-4444-555555555555")
	t.Setenv("COREMETRY_K8S_NODE_NAME", "node-a")

	attrs := resourceAttrMap(t)
	for key, want := range map[string]string{
		"k8s.namespace.name": "obs",
		"k8s.pod.name":       "coremetry-abc12",
		"k8s.pod.uid":        "11111111-2222-3333-4444-555555555555",
		"k8s.node.name":      "node-a",
	} {
		if attrs[key] != want {
			t.Errorf("%s=%q, istenen %q", key, attrs[key], want)
		}
	}
}

// Boş env'de alan HİÇ yazılmaz: boş string yazmak coverage sayacına
// sahte doluluk saymaktı (k8s_coverage res_keys üzerinden sayıyor).
func TestBuildResourceOmitsK8sAttrsOutsideK8s(t *testing.T) {
	for _, e := range []string{"COREMETRY_K8S_NAMESPACE", "COREMETRY_K8S_POD_NAME",
		"COREMETRY_K8S_POD_UID", "COREMETRY_K8S_NODE_NAME"} {
		t.Setenv(e, "")
	}
	attrs := resourceAttrMap(t)
	for _, key := range []string{"k8s.namespace.name", "k8s.pod.name", "k8s.pod.uid", "k8s.node.name"} {
		if _, ok := attrs[key]; ok {
			t.Errorf("%s boş env'e rağmen yazılmış (%q)", key, attrs[key])
		}
	}
}
