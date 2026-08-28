package api

// admin_entity_layer.go — v0.10.134 (0011 entity katmanı şeması sihirbazı;
// operator-reported: "cluster eşleşme için sihirbaz — 0011 MV'yi görmedim").
//
// api.go BÜYÜMEYECEK kuralı: rollup sihirbazının (admin_rollup.go) aynası,
// kendi dosyasında, api.go tek satır.
//
//	GET  /api/admin/entity-layer/status      admin — host başına nesne varlığı
//	GET  /api/admin/entity-layer/preflight   admin — küme/kapsama/kardinalite hükmü
//	POST /api/admin/entity-layer/apply       admin — {cluster}; audit entity_layer.apply
//	POST /api/admin/entity-layer/rollback    admin — {cluster}; yalnız MV'ler; audit
//
// Süre: ON CLUSTER DDL dağıtık kuyruğa girer; ifade başına
// distributed_ddl_task_timeout (180 s) → 5 dk üst sınır, istek kopsa da
// DDL yarıda kalmasın (context.WithoutCancel — rollup emsali).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
)

func (s *Server) registerEntityLayerAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/entity-layer/status",
		auth.RequireRole(auth.RoleAdmin, s.getEntityLayerStatus))
	mux.HandleFunc("GET /api/admin/entity-layer/preflight",
		auth.RequireRole(auth.RoleAdmin, s.getEntityLayerPreflight))
	mux.HandleFunc("POST /api/admin/entity-layer/apply",
		auth.RequireRole(auth.RoleAdmin, s.postEntityLayerApply))
	mux.HandleFunc("POST /api/admin/entity-layer/rollback",
		auth.RequireRole(auth.RoleAdmin, s.postEntityLayerRollback))
}

func (s *Server) getEntityLayerStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := s.store.EntityLayerStatus(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) getEntityLayerPreflight(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res, err := s.store.EntityLayerPreflight(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func decodeEntityLayerAction(w http.ResponseWriter, r *http.Request) (string, bool) {
	var in struct {
		Cluster string `json:"cluster"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return "", false
	}
	c := strings.TrimSpace(in.Cluster)
	if c == "" {
		writeJSONError(w, http.StatusBadRequest, "cluster required")
		return "", false
	}
	return c, true
}

func (s *Server) postEntityLayerApply(w http.ResponseWriter, r *http.Request) {
	cluster, ok := decodeEntityLayerAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	res := s.store.EntityLayerApply(ctx, cluster)
	s.audit(r, "entity_layer.apply", "clickhouse", "0011_entity_layer", auditRollupDetail(cluster, res))
	writeJSON(w, map[string]any{"statements": res, "ok": rollupResultsOK(res)})
}

func (s *Server) postEntityLayerRollback(w http.ResponseWriter, r *http.Request) {
	cluster, ok := decodeEntityLayerAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Minute)
	defer cancel()
	res := s.store.EntityLayerRollback(ctx, cluster)
	s.audit(r, "entity_layer.rollback", "clickhouse", "0011_entity_layer", auditRollupDetail(cluster, res))
	writeJSON(w, map[string]any{"statements": res, "ok": rollupResultsOK(res)})
}
