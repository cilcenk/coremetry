package api

// blast_radius_routes_test.go — v0.10.260: servis listesi tekil+sıralı;
// küme özeti içerik-duyarlı (aynı uzunluk farklı küme → farklı, sıra
// değişmez); rota defterden çözülür.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBlastRadiusServicesAndDigest(t *testing.T) {
	got := blastRadiusServices(" b, a ,b,,c ")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("services: %v", got)
	}
	d1 := blastRadiusSetDigest([]string{"a", "b"})
	d2 := blastRadiusSetDigest([]string{"a", "c"})
	d3 := blastRadiusSetDigest(blastRadiusServices("b,a"))
	if d1 == d2 {
		t.Error("aynı uzunlukta farklı küme aynı özet (v0.5.187 sınıfı)")
	}
	if d1 != d3 {
		t.Error("sıra bağımsızlığı: b,a == a,b olmalı")
	}
	if blastRadiusSetDigest([]string{"ab"}) == blastRadiusSetDigest([]string{"a", "b"}) {
		t.Error("ayraç: ab ≠ a+b")
	}
}

func TestBlastRadiusBatchRouteResolves(t *testing.T) {
	s := &Server{}
	mux := s.buildMux()
	req := httptest.NewRequest(http.MethodGet, "/api/blast-radius?services=a", nil)
	if _, pattern := mux.Handler(req); pattern != "GET /api/blast-radius" {
		t.Fatalf("kalıp %q", pattern)
	}
}
