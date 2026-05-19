package api

import "net/http"

// listClusterMembers returns the current Coremetry pod roster —
// every replica that's written a heartbeat to Redis in the last
// 30s. In single-instance mode (Noop cache) it returns a one-
// member list representing this process so the admin page renders
// the same in compose-up dev as in a 10-pod K8s deployment.
//
// Lightweight — single SCAN + MGET against Redis (or no I/O in
// single-instance mode). Safe to poll at 5-10s in the UI.
func (s *Server) listClusterMembers(w http.ResponseWriter, r *http.Request) {
	if s.cluster == nil {
		writeJSON(w, map[string]any{"members": []any{}})
		return
	}
	members, err := s.cluster.Members(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"members": members,
		"selfId":  s.cluster.MyID(),
	})
}
