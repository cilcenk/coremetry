package chstore

import (
	"strings"
	"testing"
)

// v0.10.143 (DETAY SAYFALARI adım 6) — traces listesinin `cluster` ek kolonu
// spans'in terfi `cluster` kolonundan (coalesce zinciri) gelir; çıplak dizi
// araması literal `cluster` attribute'unu bulur ve k8s.cluster.name /
// openshift.cluster.name kurulumlarında boş dönerdi (inceleme). Bind arg
// üretmez (kolon adı sabit).
func TestTraceExtrasProjectionClusterColumn(t *testing.T) {
	sel, args := traceExtrasProjection([]string{"cluster", "user.id"})
	if !strings.Contains(sel, "anyIf(cluster, cluster != '') AS extra_0") {
		t.Fatalf("cluster terfi kolonundan okunmalı: %s", sel)
	}
	if strings.Contains(sel, "indexOf(attr_keys, ?)], ''),nullIf(res_values[indexOf(res_keys, ?)], '')), has(attr_keys, ?) OR has(res_keys, ?)) AS extra_0") {
		t.Fatalf("cluster için dizi araması olmamalı: %s", sel)
	}
	// user.id dizi yoluna düşer: 4 bind arg (attr/res anahtar + has çifti)
	if len(args) != 4 || args[0] != "user.id" {
		t.Fatalf("dizi yolu bind'leri: %v", args)
	}
}
