package api

import (
	"encoding/json"
	"net/http"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

// trace_facets_routes.go — v0.10.302 (trace arama Dilim 2a): operatör facet
// kaydı. Defter kaydı (route_registry.go) — api.go büyümez.
//
//	GET /api/settings/trace-facets  admin — kayıt + bu pod'un gördüğü durum + prod SQL
//	PUT /api/settings/trace-facets  admin — doğrula, kaydet, app-managed'da DDL+probe, audit
func init() { registerRoutesExtra("trace-facets", (*Server).registerTraceFacetsRoutes) }

func (s *Server) registerTraceFacetsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings/trace-facets", auth.RequireRole(auth.RoleAdmin, s.getTraceFacets))
	mux.HandleFunc("PUT /api/settings/trace-facets", auth.RequireRole(auth.RoleAdmin, s.putTraceFacets))
}

type traceFacetsResponse struct {
	Facets       []chstore.TraceFacet       `json:"facets"`
	Status       []chstore.TraceFacetStatus `json:"status"`
	BootManaged  bool                       `json:"bootManaged"`
	MigrationSQL string                     `json:"migrationSql"`
	// Note — PUT sonrası: app-managed'da "DDL uygulandı/ertelendi", dış
	// Distributed'da "SQL'i elle koşun".
	Note string `json:"note,omitempty"`
}

func (s *Server) traceFacetsView(r *http.Request, note string) traceFacetsResponse {
	facets := chstore.CurrentTraceFacets()
	return traceFacetsResponse{
		Facets:       facets,
		Status:       s.store.TraceFacetsStatus(r.Context()),
		BootManaged:  !s.store.SpansIsExternalDistributed(r.Context()),
		MigrationSQL: chstore.TraceFacetMigrationSQL(chstore.TraceFacetSettings{Facets: facets}),
		Note:         note,
	}
}

func (s *Server) getTraceFacets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.traceFacetsView(r, ""))
}

func (s *Server) putTraceFacets(w http.ResponseWriter, r *http.Request) {
	var in chstore.TraceFacetSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	cfg, err := s.store.SaveTraceFacets(r.Context(), in)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	bootManaged := s.store.ApplyTraceFacets(r.Context())
	details, _ := json.Marshal(map[string]any{"facets": cfg.Facets, "bootManaged": bootManaged})
	s.audit(r, "settings.trace_facets.update", "settings", "trace_facets", string(details))
	s.publishConfigReload(r.Context(), "trace-facets")
	note := "SQL'i prod ClickHouse'ta elle koşun (migrationSql), sonra pod'ları yeniden başlatın."
	if bootManaged {
		note = "Kolon/indeks DDL'i gönderildi (küme kipinde ertelenmiş olabilir); probe kolonu DOLU görünce filtreler kolona yönlenir — diğer pod'lar bir sonraki boot'ta."
	}
	writeJSON(w, s.traceFacetsView(r, note))
}
