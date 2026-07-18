package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.46 — merge-cache davranışı: (1) yeni turlar birikir (boşta
// kalan servis rozetini kaybetmez), (2) attribute'suz taze satır dolu
// eskiyi ezmez, (3) refresh copy-on-write'tır — okuyucuya verilen
// snapshot sonraki refresh'te DEĞİŞMEZ (Go 1.25 concurrent-map-fatal
// sınıfına karşı yapısal koruma).
func TestServiceRuntimesCacheMerge(t *testing.T) {
	c := &serviceRuntimesCache{}

	first := c.refresh(map[string]chstore.ServiceRuntime{
		"api-gw":  {Service: "api-gw", Language: "go", RuntimeVersion: "go1.22.5"},
		"billing": {Service: "billing", Language: "java"},
	})
	if len(first) != 2 {
		t.Fatalf("ilk refresh: 2 bekleniyordu, %d geldi", len(first))
	}

	// Tur 2: billing bu pencerede boşta (haritada yok), api-gw
	// attribute'suz döndü (sidecar son basmış), yeni servis python.
	second := c.refresh(map[string]chstore.ServiceRuntime{
		"api-gw": {Service: "api-gw"}, // boş — eskiyi ezmemeli
		"py-svc": {Service: "py-svc", Language: "python"},
	})
	if len(second) != 3 {
		t.Fatalf("merge sonrası: 3 bekleniyordu, %d geldi", len(second))
	}
	if second["billing"].Language != "java" {
		t.Error("boşta kalan billing rozetini kaybetti")
	}
	if second["api-gw"].Language != "go" {
		t.Errorf("attribute'suz taze satır dolu eskiyi ezdi: %+v", second["api-gw"])
	}
	if second["py-svc"].Language != "python" {
		t.Error("yeni servis eklenmedi")
	}

	// Copy-on-write: ilk snapshot ikinci refresh'ten etkilenmemiş olmalı.
	if _, ok := first["py-svc"]; ok {
		t.Error("refresh önceki snapshot'ı yerinde değiştirdi (copy-on-write ihlali)")
	}
}
