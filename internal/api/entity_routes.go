package api

// entity_routes.go — v0.10.129 (K8s entity katmanı adım 3: bayrak +
// senkronizasyon yönetimi; docs/plans/entity-layer-design-2026-08-28.md §5).
//
// api.go BÜYÜMEYECEK kuralı (registerVMetricsRoutes emsali): yüzeyin
// rotaları burada, api.go tek satır register çağrısıyla büyür.
//
//	GET  /api/settings/entities            admin — bayrak + vidalar
//	PUT  /api/settings/entities            admin — audit settings.entities.update
//	GET  /api/admin/entities/sync          admin — obs sayaçları + son koşular (entity_sync_runs, 24 h)
//	POST /api/admin/entities/sync/run      admin — bu pod'da hemen bir tick (audit admin.entity_sync.run)
//
// Bayrak kapalıyken sync/run 404 {disabled:true} — mevcut sayfalar
// etkilenmez. Sorgu/pivot uçları (design §5) adım 6'da ayrı dosyada.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/entity"
	"github.com/cilcenk/coremetry/internal/mcptools"
)

// SetEntity — main.go: ayar servisi her modda; syncer yalnız worker rolünde
// (api-only pod'da nil → run ucu 409).
func (s *Server) SetEntity(settings *entity.SettingsService, syncer *entity.Syncer) {
	s.entitySettings = settings
	s.entitySync = syncer
}

func (s *Server) registerEntityRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/settings/entities", auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.getEntitySettings)))
	mux.Handle("PUT /api/settings/entities", auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.putEntitySettings)))
	mux.Handle("GET /api/admin/entities/sync", auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.getEntitySync)))
	mux.Handle("POST /api/admin/entities/sync/run", auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.runEntitySync)))
}

func (s *Server) getEntitySettings(w http.ResponseWriter, r *http.Request) {
	if s.entitySettings == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "entity layer not available")
		return
	}
	cur := s.entitySettings.Current()
	res := cur.Resolved()
	writeJSON(w, map[string]any{
		"settings": cur,
		"resolved": map[string]any{
			"enabled": res.Enabled, "syncInterval": res.SyncInterval.String(), "podGap": res.PodGap.String(),
			"staleAfter": res.StaleAfter.String(), "parallelClusters": res.ParallelClusters,
		},
		"defaults": entity.DefaultSettings(),
	})
}

func (s *Server) putEntitySettings(w http.ResponseWriter, r *http.Request) {
	if s.entitySettings == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "entity layer not available")
		return
	}
	var in entity.Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	// Kelepçe PUT'ta değil okumada (Resolved): operatör girdiği dizeyi
	// geri görür, uygulanan değeri "resolved" bölümünde görür.
	if err := s.entitySettings.SavePersisted(r.Context(), s.store, in); err != nil {
		writeErr(w, err)
		return
	}
	s.publishConfigReload(r.Context(), "entities")
	details, _ := json.Marshal(in)
	s.audit(r, "settings.entities.update", "settings", entity.SettingsKey, string(details))
	mcptools.ResetEntityIndexCache() // v0.10.490 (Astra #7) — resolve_entity indeksi bayrak/cluster değişimini hemen görsün
	s.getEntitySettings(w, r)
}

func (s *Server) getEntitySync(w http.ResponseWriter, r *http.Request) {
	if s.entitySettings == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "entity layer not available")
		return
	}
	if !s.entitySettings.Resolved().Enabled {
		writeJSON(w, map[string]any{"disabled": true})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	runs, err := s.store.EntitySyncRuns(ctx, time.Now().Add(-24*time.Hour), 200)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := map[string]any{"runs": runs, "workerOnThisPod": s.entitySync != nil}
	if s.entitySync != nil {
		out["observability"] = s.entitySync.Observability()
	}
	writeJSON(w, out)
}

func (s *Server) runEntitySync(w http.ResponseWriter, r *http.Request) {
	if s.entitySettings == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "entity layer not available")
		return
	}
	if !s.entitySettings.Resolved().Enabled {
		writeJSONError(w, http.StatusNotFound, "entity layer disabled")
		return
	}
	if s.entitySync == nil {
		writeJSONError(w, http.StatusConflict, "bu pod worker rolünde değil — sync worker pod'unda koşar")
		return
	}
	s.audit(r, "admin.entity_sync.run", "entity_sync", "all", `{}`)
	// Arka planda: Thanos fan-out 30 s sürebilir; yanıt hemen döner,
	// durum GET /api/admin/entities/sync'ten okunur.
	go s.entitySync.Tick(context.Background())
	writeJSON(w, map[string]any{"ok": true, "started": true})
}
