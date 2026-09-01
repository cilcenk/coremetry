package api

// influx_routes.go — v0.10.222 (Influx D1, docs/audit/influx-integration.md
// §10; operatör onayı 2026-09-01).
//
// api.go BÜYÜMEYECEK kuralı (registerVMetricsRoutes emsali): Influx kaynak
// yönetiminin rotaları + handler'ları burada, api.go tek satır register
// çağrısıyla büyür. Dosya adı spec'te böyle istendi (influx_routes.go);
// handler'lar ayrı dosyaya çıkacak kadar çok değil (3).
//
//	GET  /api/settings/influx       admin — snapshot (tokenRef bir REFERANS,
//	                                 görünür; tokenResolved rozeti)
//	PUT  /api/settings/influx       admin — tüm liste atomik (thanos deseni);
//	                                 audit settings.influx.update
//	POST /api/settings/influx/test  admin — formdaki TEK kaynağı KAYDETMEDEN
//	                                 dener; 200 + ok:false başarısızlıkta
//
// Üçü de admin (vmetrics gerekçesi): kaydedilen tokenRef operatörün Influx
// org'unu okur; GET'te bile kaynak URL'leri ve sorgular var.
//
// Neden merge yok (vmetrics'in "boş token saklıyı korur" kuralı): burada
// secret saklanmıyor, tokenRef görünür bir referans — form onu geri alır
// ve aynen yollar. Sunucu sahipli tek şey ID; onu influx.Normalize önceki
// kayıttan taşır.

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/influx"
)

func (s *Server) registerInfluxRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings/influx", auth.RequireRole(auth.RoleAdmin, s.getInfluxSettings))
	mux.HandleFunc("PUT /api/settings/influx", auth.RequireRole(auth.RoleAdmin, s.putInfluxSettings))
	mux.HandleFunc("POST /api/settings/influx/test", auth.RequireRole(auth.RoleAdmin, s.testInfluxSource))
}

func (s *Server) getInfluxSettings(w http.ResponseWriter, r *http.Request) {
	if s.influx == nil {
		http.Error(w, "influx not available", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, s.influx.Snapshot())
}

func (s *Server) putInfluxSettings(w http.ResponseWriter, r *http.Request) {
	if s.influx == nil {
		http.Error(w, "influx not available", http.StatusServiceUnavailable)
		return
	}
	var in influx.Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	cfg, err := influx.Normalize(in, s.influx.CurrentSettings(), influx.NewSourceID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// WithoutCancel: istemci cevabı beklemeden kapatırsa yazım yarıda
	// kalmasın (thanos_handlers.go:787 dersi).
	if err := s.influx.SavePersisted(context.WithoutCancel(r.Context()), s.store, cfg); err != nil {
		writeErr(w, err)
		return
	}
	s.publishConfigReload(r.Context(), "influx")
	snap := s.influx.Snapshot()
	names := make([]string, 0, len(cfg.Sources))
	enabled := 0
	for _, src := range cfg.Sources {
		names = append(names, src.Name)
		if src.Enabled {
			enabled++
		}
	}
	details, _ := json.Marshal(map[string]any{
		"sources": len(cfg.Sources), "enabled": enabled, "names": names,
	})
	s.audit(r, "settings.influx.update", "settings", "influx_sources", string(details))
	writeJSON(w, snap)
}

func (s *Server) testInfluxSource(w http.ResponseWriter, r *http.Request) {
	if s.influx == nil {
		http.Error(w, "influx not available", http.StatusServiceUnavailable)
		return
	}
	var in influx.SourceConfig
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	// Kapalı bir taslağı da denemek meşru: doğrulama etkin-kaynak kurallarıyla.
	in.Enabled = true
	cfg, err := influx.Normalize(influx.Settings{Sources: []influx.SourceConfig{in}}, s.influx.CurrentSettings(), influx.NewSourceID)
	if err != nil {
		writeJSON(w, influx.TestResult{OK: false, Error: err.Error(), Queries: []influx.QueryProbe{}})
		return
	}
	writeJSON(w, s.influx.Test(r.Context(), cfg.Sources[0]))
}
