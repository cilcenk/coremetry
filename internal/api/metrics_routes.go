package api

import (
	"net/http"

	"github.com/cilcenk/coremetry/internal/auth"
)

// metrics_routes.go — v0.10.294 (VM audit §7.1): metrik geçişinin YENİ
// uçları burada, defter kaydıyla (route_registry.go) — api.go'ya satır
// EKLENMEZ. Mevcut /api/metrics/* uçları api.go'da kalır (taşıma ayrı
// commit, logs_routes.go emsali).
func init() { registerRoutesExtra("metrics", (*Server).registerMetricsRoutes) }

func (s *Server) registerMetricsRoutes(mux *http.ServeMux) {
	// Tanı ucu: iki backend'e birden yük bindirir → admin.
	mux.HandleFunc("GET /api/metrics/compare", auth.RequireRole(auth.RoleAdmin, s.getMetricsCompare))
}
