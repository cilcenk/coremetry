package api

// admin_rollout_layer.go — v0.10.197 (0012 rollouts katmanı şeması sihirbazı;
// rollouts audit §5(j)). admin_entity_layer.go'nun aynası, kendi dosyasında,
// api.go tek satır.
//
//	GET  /api/admin/rollout-layer/status      admin — host başına nesne varlığı
//	GET  /api/admin/rollout-layer/preflight   admin — küme / 0011 / kapsama (cluster başına) / LC / MV kapısı
//	POST /api/admin/rollout-layer/apply       admin — {cluster, withMV}; audit rollout_layer.apply
//	POST /api/admin/rollout-layer/rollback    admin — {cluster}; yalnız MV; audit
//
// withMV yalnız preflight.mvGate açıkken kabul edilir (sunucu da kontrol
// eder — istemciye güvenmez): kapsama eşiğin altındayken MV'yi prod'a
// almak audit R1'in tam kendisi (boş değil ama işe yaramaz MV).
// Süre: ON CLUSTER DDL dağıtık kuyruğa girer; 5 dk üst sınır, istek kopsa da
// DDL yarıda kalmasın (context.WithoutCancel — rollup emsali).

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
)

func (s *Server) registerRolloutLayerAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/rollout-layer/status",
		auth.RequireRole(auth.RoleAdmin, s.getRolloutLayerStatus))
	mux.HandleFunc("GET /api/admin/rollout-layer/preflight",
		auth.RequireRole(auth.RoleAdmin, s.getRolloutLayerPreflight))
	mux.HandleFunc("POST /api/admin/rollout-layer/apply",
		auth.RequireRole(auth.RoleAdmin, s.postRolloutLayerApply))
	mux.HandleFunc("POST /api/admin/rollout-layer/rollback",
		auth.RequireRole(auth.RoleAdmin, s.postRolloutLayerRollback))
}

func (s *Server) getRolloutLayerStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := s.store.RolloutLayerStatus(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) getRolloutLayerPreflight(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res, err := s.store.RolloutLayerPreflight(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) postRolloutLayerApply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Cluster string `json:"cluster"`
		WithMV  bool   `json:"withMV"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	cluster := strings.TrimSpace(in.Cluster)
	if cluster == "" {
		writeJSONError(w, http.StatusBadRequest, "cluster required")
		return
	}
	// İnceleme S3/S4/S8: ön kontrol HER apply'da koşar — doğrudan POST
	// arayüz kapılarını atlayamaz. Supported=false ya da probe hatası → 409,
	// DDL basılmaz (emin olamadığımız kümeye yazmıyoruz). withMV yalnız
	// kapı AÇIK ve 0011 kolonları VARKEN (MV cluster/k8s_namespace okur).
	// Ön kontrol kendi bütçesiyle koşar (DDL bütçesini yemez); 23 ON CLUSTER
	// ifadesi için 10 dk (distributed_ddl_task_timeout 180 s/ifade) — FE
	// zaman aşımı (api.ts rolloutLayerApply) bunun üstünde.
	pctx, pcancel := context.WithTimeout(context.WithoutCancel(r.Context()), 45*time.Second)
	pre, err := s.store.RolloutLayerPreflight(pctx)
	pcancel()
	if err != nil || len(pre.ProbeErrors) > 0 || !pre.Supported {
		detail := pre.Detail
		if err != nil {
			detail = err.Error()
		} else if len(pre.ProbeErrors) > 0 {
			detail += " (" + strings.Join(pre.ProbeErrors, "; ") + ")"
		}
		s.audit(r, "rollout_layer.apply", "clickhouse", "0012_rollout_layer", "REDDEDİLDİ cluster="+cluster+": "+detail)
		writeJSONError(w, http.StatusConflict, "ön kontrol geçmedi — "+detail)
		return
	}
	withMV := in.WithMV && pre.MVGate && pre.Layer0011
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
	defer cancel()
	res := s.store.RolloutLayerApply(ctx, cluster, withMV)
	s.audit(r, "rollout_layer.apply", "clickhouse", "0012_rollout_layer", auditRollupDetail(cluster, res)+" withMV="+strconv.FormatBool(withMV))
	writeJSON(w, map[string]any{"statements": res, "ok": rollupResultsOK(res), "withMV": withMV})
}

func (s *Server) postRolloutLayerRollback(w http.ResponseWriter, r *http.Request) {
	cluster, ok := decodeEntityLayerAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Minute)
	defer cancel()
	res := s.store.RolloutLayerRollback(ctx, cluster)
	s.audit(r, "rollout_layer.rollback", "clickhouse", "0012_rollout_layer", auditRollupDetail(cluster, res))
	writeJSON(w, map[string]any{"statements": res, "ok": rollupResultsOK(res)})
}
