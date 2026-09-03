package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
)

// admin_attr_index.go — v0.10.306: 0014 attribute hash indeksi sihirbazı
// uçları (admin_function_id.go aynası; defter kaydı, api.go büyümez).
//
//	GET  /api/admin/attr-index/status       admin — host başına nesne + doluluk + bu pod hazır mı
//	GET  /api/admin/attr-index/preflight    admin — küme / spans_local / kolon-indeks / boot-managed
//	POST /api/admin/attr-index/apply        admin — {cluster}; 0014 ADIM 1-2-4; audit
//	POST /api/admin/attr-index/materialize  admin — {cluster}; ADIM 6 (eski part'lar); audit
//	POST /api/admin/attr-index/rollback     admin — {cluster}; INDEX → COLUMN; audit
func init() { registerRoutesExtra("attr-index", (*Server).registerAttrIndexRoutes) }

func (s *Server) registerAttrIndexRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/attr-index/status", auth.RequireRole(auth.RoleAdmin, s.getAttrIndexStatus))
	mux.HandleFunc("GET /api/admin/attr-index/preflight", auth.RequireRole(auth.RoleAdmin, s.getAttrIndexPreflight))
	mux.HandleFunc("POST /api/admin/attr-index/apply", auth.RequireRole(auth.RoleAdmin, s.postAttrIndexApply))
	mux.HandleFunc("POST /api/admin/attr-index/materialize", auth.RequireRole(auth.RoleAdmin, s.postAttrIndexMaterialize))
	mux.HandleFunc("POST /api/admin/attr-index/rollback", auth.RequireRole(auth.RoleAdmin, s.postAttrIndexRollback))
}

func (s *Server) getAttrIndexStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := s.store.AttrIndexStatus(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) getAttrIndexPreflight(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res, err := s.store.AttrIndexPreflight(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) postAttrIndexApply(w http.ResponseWriter, r *http.Request) {
	cluster, ok := decodeEntityLayerAction(w, r)
	if !ok {
		return
	}
	pctx, pcancel := context.WithTimeout(context.WithoutCancel(r.Context()), 45*time.Second)
	pre, err := s.store.AttrIndexPreflight(pctx)
	pcancel()
	if err != nil || !pre.Supported || len(pre.ProbeErrors) > 0 {
		detail := pre.Detail
		if err != nil {
			detail = err.Error()
		} else if len(pre.ProbeErrors) > 0 {
			detail += " (" + strings.Join(pre.ProbeErrors, "; ") + ")"
		}
		s.audit(r, "attr_index.apply", "clickhouse", "0014_attr_kvh", "REDDEDİLDİ cluster="+cluster+": "+detail)
		writeJSONError(w, http.StatusConflict, "ön kontrol geçmedi — "+detail)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	res := s.store.AttrIndexApply(ctx, cluster)
	s.audit(r, "attr_index.apply", "clickhouse", "0014_attr_kvh", auditRollupDetail(cluster, res))
	writeJSON(w, map[string]any{"statements": res, "ok": rollupResultsOK(res),
		"note": "pod'lar kolonu probe ile alır: yeniden başlatınca ya da ertelenmiş DDL inince (/api/health attr_index_available); indeks yalnız yeni part'larda — eski part'lar için MATERIALIZE ayrı eylem"})
}

func (s *Server) postAttrIndexMaterialize(w http.ResponseWriter, r *http.Request) {
	cluster, ok := decodeEntityLayerAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Minute)
	defer cancel()
	res := s.store.AttrIndexMaterialize(ctx, cluster)
	s.audit(r, "attr_index.materialize", "clickhouse", "0014_attr_kvh", auditRollupDetail(cluster, res))
	writeJSON(w, map[string]any{"statements": res, "ok": rollupResultsOK(res),
		"note": "mutasyonlar arka planda: SELECT * FROM system.mutations WHERE table = 'spans_local' AND NOT is_done — disk + merge maliyeti, mesai dışı"})
}

func (s *Server) postAttrIndexRollback(w http.ResponseWriter, r *http.Request) {
	cluster, ok := decodeEntityLayerAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Minute)
	defer cancel()
	res := s.store.AttrIndexRollback(ctx, cluster)
	s.audit(r, "attr_index.rollback", "clickhouse", "0014_attr_kvh", auditRollupDetail(cluster, res))
	writeJSON(w, map[string]any{"statements": res, "ok": rollupResultsOK(res)})
}
