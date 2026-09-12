package api

import (
	"strings"
	"testing"
)

// incident_rootcause_pin_test.go — v0.10.698 (Dynatrace paritesi #1, dilim B).
//
// "Test edilmiş ama ulaşılamaz" sınıfı: chstore'daki seçici saf ve yeşil
// olsa da liste VE detay handler'ı zenginleştirmeyi çağırmazsa satırda
// kök neden yok. İki çağrı yerini kaynaktan çiviler.
func TestIncidentHandlersEnrichRootCause(t *testing.T) {
	src := readSourceFile(t, "api_incidents.go")
	if strings.Count(src, "EnrichIncidentsWithRootCause(") < 2 {
		t.Fatal("listIncidents VE getIncident EnrichIncidentsWithRootCause çağırmalı (liste + detay aynı üretici)")
	}
	list := src[strings.Index(src, "func (s *Server) listIncidents"):strings.Index(src, "func (s *Server) getIncident")]
	if !strings.Contains(list, "EnrichIncidentsWithRootCause(") {
		t.Error("listIncidents kök neden zenginleştirmesini atlıyor")
	}
	get := src[strings.Index(src, "func (s *Server) getIncident"):strings.Index(src, "func (s *Server) createIncident")]
	if !strings.Contains(get, "EnrichIncidentsWithRootCause(") {
		t.Error("getIncident kök neden zenginleştirmesini atlıyor — detay çipi boş kalır")
	}
}
