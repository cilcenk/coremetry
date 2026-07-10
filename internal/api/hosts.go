package api

// Host inventory handlers (v0.8.449, Wave 3 / A4). Thin wrappers per
// the api.go-growth-minimal rule. Read-only; viewer via the global
// auth middleware; no audit. Window clamping lives in chstore
// (clampHostWindow) so every caller shares the ≤6h guard.

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

func (s *Server) registerHostRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/hosts", s.getHosts)
	mux.HandleFunc("GET /api/hosts/detail", s.getHostDetail)
}

func (s *Server) getHosts(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, time.Hour)
	key := "hosts:" + cacheBucket(from, to)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetHosts(ctx, from, to)
	})
}

func (s *Server) getHostDetail(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	if host == "" {
		http.Error(w, `{"error":"host required"}`, http.StatusBadRequest)
		return
	}
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("hosts-detail:%s:%s", host, cacheBucket(from, to))
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetHostDetail(ctx, host, from, to)
	})
}
