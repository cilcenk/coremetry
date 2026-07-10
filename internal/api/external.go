package api

// External API monitoring handlers (v0.8.446, Wave 3 / A1). Thin
// wrappers per the api.go-growth-minimal rule: parse the window,
// front the MV read with s.serveCached. Read-only surface — global
// auth middleware (viewer+) is the gate, no audit entries.

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

func (s *Server) registerExternalRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/external", s.getExternalHosts)
	mux.HandleFunc("GET /api/external/host", s.getExternalHostDetail)
}

// getExternalHosts — /external overview: one row per third-party
// destination seen in the window, from topology_edges_5m.
func (s *Server) getExternalHosts(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, time.Hour)
	key := "external:" + cacheBucket(from, to)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetExternalHosts(ctx, from, to)
	})
}

// getExternalHostDetail — drawer payload for one host: per-caller
// breakdown + 5m RED trend. Distinct cache slot per (host, window).
func (s *Server) getExternalHostDetail(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	if host == "" {
		http.Error(w, `{"error":"host required"}`, http.StatusBadRequest)
		return
	}
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("external-host:%s:%s", host, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetExternalHostDetail(ctx, host, from, to)
	})
}
