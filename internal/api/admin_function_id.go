package api

// admin_function_id.go — v0.10.252 (0013 attr_function_id terfi kolonu
// sihirbazı; operatör: "0013 migration nedir sihirbazı var mı" → kuyruk).
// admin_rollout_layer.go aynası; kayıt route_registry.go defterinden
// (api.go büyümez).
//
//	GET  /api/admin/function-id-column/status       admin — host başına nesne + doluluk
//	GET  /api/admin/function-id-column/preflight    admin — küme / anahtar yazımı / kolon / index
//	POST /api/admin/function-id-column/apply        admin — {cluster}; ADIM 1-2-4; audit
//	POST /api/admin/function-id-column/materialize  admin — {cluster}; ADIM 5 (eski part'lar); audit
//	POST /api/admin/function-id-column/rollback     admin — {cluster}; INDEX → COLUMN; audit
//
// Apply HER seferinde ön kontrolü yeniden koşar (istemciye güvenmez):
// bootManaged / spans_local yok / küme yok → 409, DDL basılmaz.
// Süre: ON CLUSTER DDL dağıtık kuyruğa girer; 3 ifade için 5 dk, istek
// kopsa da DDL yarıda kalmasın (context.WithoutCancel). MATERIALIZE
// asenkron mutasyon başlatır: 2 dk yeterli, izleme system.mutations.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
)

func init() { registerRoutesExtra("function-id-column", (*Server).registerFunctionIDColumnRoutes) }

func (s *Server) registerFunctionIDColumnRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/function-id-column/status",
		auth.RequireRole(auth.RoleAdmin, s.getFunctionIDColumnStatus))
	mux.HandleFunc("GET /api/admin/function-id-column/preflight",
		auth.RequireRole(auth.RoleAdmin, s.getFunctionIDColumnPreflight))
	mux.HandleFunc("POST /api/admin/function-id-column/apply",
		auth.RequireRole(auth.RoleAdmin, s.postFunctionIDColumnApply))
	mux.HandleFunc("POST /api/admin/function-id-column/materialize",
		auth.RequireRole(auth.RoleAdmin, s.postFunctionIDColumnMaterialize))
	mux.HandleFunc("POST /api/admin/function-id-column/rollback",
		auth.RequireRole(auth.RoleAdmin, s.postFunctionIDColumnRollback))
}

func (s *Server) getFunctionIDColumnStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := s.store.FunctionIDColumnStatus(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) getFunctionIDColumnPreflight(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res, err := s.store.FunctionIDColumnPreflight(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) postFunctionIDColumnApply(w http.ResponseWriter, r *http.Request) {
	cluster, ok := decodeEntityLayerAction(w, r)
	if !ok {
		return
	}
	pctx, pcancel := context.WithTimeout(context.WithoutCancel(r.Context()), 45*time.Second)
	pre, err := s.store.FunctionIDColumnPreflight(pctx)
	pcancel()
	if err != nil || !pre.Supported || len(pre.ProbeErrors) > 0 {
		detail := pre.Detail
		if err != nil {
			detail = err.Error()
		} else if len(pre.ProbeErrors) > 0 {
			detail += " (" + strings.Join(pre.ProbeErrors, "; ") + ")"
		}
		s.audit(r, "function_id_column.apply", "clickhouse", "0013_function_id", "REDDEDİLDİ cluster="+cluster+": "+detail)
		writeJSONError(w, http.StatusConflict, "ön kontrol geçmedi — "+detail)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	res := s.store.FunctionIDColumnApply(ctx, cluster)
	s.audit(r, "function_id_column.apply", "clickhouse", "0013_function_id", auditRollupDetail(cluster, res))
	writeJSON(w, map[string]any{"statements": res, "ok": rollupResultsOK(res),
		"note": "pod'lar kolonu yeniden başlatınca haritaya alır (küme kipinde iki-restart); kolon boşken dizi yoluna düşülür"})
}

func (s *Server) postFunctionIDColumnMaterialize(w http.ResponseWriter, r *http.Request) {
	cluster, ok := decodeEntityLayerAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()
	res := s.store.FunctionIDColumnMaterialize(ctx, cluster)
	s.audit(r, "function_id_column.materialize", "clickhouse", "0013_function_id", auditRollupDetail(cluster, res))
	writeJSON(w, map[string]any{"statements": res, "ok": rollupResultsOK(res),
		"note": "mutasyon arka planda: SELECT * FROM system.mutations WHERE table = 'spans_local' AND NOT is_done"})
}

func (s *Server) postFunctionIDColumnRollback(w http.ResponseWriter, r *http.Request) {
	cluster, ok := decodeEntityLayerAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Minute)
	defer cancel()
	res := s.store.FunctionIDColumnRollback(ctx, cluster)
	s.audit(r, "function_id_column.rollback", "clickhouse", "0013_function_id", auditRollupDetail(cluster, res))
	writeJSON(w, map[string]any{"statements": res, "ok": rollupResultsOK(res)})
}
