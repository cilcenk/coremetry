package api

// db_waitlock.go — GET /api/databases/waitlock (v0.8.391, Stage-2
// D3): the cross-engine waits & locks strip for the /databases
// detail drawer. New surface arrives register-pattern style (the
// pivot.go convention) — its own file + registrar, api.go grows by
// exactly one line.

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// registerDBWaitLockRoutes mounts the D3 endpoint. Called ONCE from
// api.go's route block. Read-only, viewer-visible — same open
// posture as the sibling /api/databases/* reads.
func (s *Server) registerDBWaitLockRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/databases/waitlock", s.getDBWaitLock)
}

// getDBWaitLock serves the normalized wait/lock model for one
// (system, instance). The strip is drawer-lazy (fetched on row
// expand only) and the underlying read is at most two bounded
// metric_points trips, so 60s TTL keeps repeat drawer opens across
// a triage session on one cache slot. Key hashes ALL inputs:
// system + instance + minute-bucketed window (cacheBucket), the
// db-detail key convention.
func (s *Server) getDBWaitLock(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	system := q.Get("system")
	instance := q.Get("instance")
	if system == "" {
		http.Error(w, `{"error":"system required"}`, http.StatusBadRequest)
		return
	}
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("db-waitlock:%s:%s:%s", system, instance, cacheBucket(from, to))
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetDBWaitLock(ctx, system, instance, from, to)
	})
}
