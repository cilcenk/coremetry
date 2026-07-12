package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cilcenk/coremetry/internal/auth"
)

// ldap_groupsync.go — v0.8.526 LDAP/AD group-sync admin surface
// (registrar pattern; api.go gains only the one register call).
//
//   GET  /api/admin/ldap/groupsync          → snapshot summary (in-memory)
//   POST /api/admin/ldap/groupsync/sync      → run a sync now (admin + audit)
//   GET  /api/admin/ldap/groupsync/preview   → live dry-run, never writes CH
//
// All admin-gated. The summary reads the engine's atomic snapshot pointer
// (no CH round-trip), so it is deliberately NOT wrapped in s.serveCached —
// caching would only add staleness over an in-memory read and must not
// mask a just-completed sync-now.

func (s *Server) registerLdapGroupSyncRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET  /api/admin/ldap/groupsync", auth.RequireRole(auth.RoleAdmin, s.getLdapGroupSync))
	mux.HandleFunc("POST /api/admin/ldap/groupsync/sync", auth.RequireRole(auth.RoleAdmin, s.postLdapGroupSyncNow))
	mux.HandleFunc("GET  /api/admin/ldap/groupsync/preview", auth.RequireRole(auth.RoleAdmin, s.getLdapGroupSyncPreview))
}

func (s *Server) getLdapGroupSync(w http.ResponseWriter, r *http.Request) {
	if s.ldapGroupSync == nil {
		http.Error(w, "ldap group sync not available", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, s.ldapGroupSync.Summary())
}

func (s *Server) postLdapGroupSyncNow(w http.ResponseWriter, r *http.Request) {
	if s.ldapGroupSync == nil {
		http.Error(w, "ldap group sync not available", http.StatusServiceUnavailable)
		return
	}
	// Bound the manual sync by the configured timeout so a slow directory
	// can't hang the admin request indefinitely.
	ctx, cancel := context.WithTimeout(r.Context(), s.ldapGroupSync.SyncTimeout())
	defer cancel()
	snap, err := s.ldapGroupSync.Sync(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "ldap.groupsync.sync", "ldap_groupsync", "",
		fmt.Sprintf(`{"groups":%d,"users":%d,"tombstoned":%d,"matchRatio":%.3f}`,
			snap.Stats.Groups, snap.Stats.Users, snap.Stats.Tombstoned, snap.Stats.MatchRatio))
	writeJSON(w, s.ldapGroupSync.Summary())
}

func (s *Server) getLdapGroupSyncPreview(w http.ResponseWriter, r *http.Request) {
	if s.ldapGroupSync == nil {
		http.Error(w, "ldap group sync not available", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.ldapGroupSync.SyncTimeout())
	defer cancel()
	res, err := s.ldapGroupSync.Preview(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}
