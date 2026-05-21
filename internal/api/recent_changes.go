package api

import (
	"net/http"
	"time"
)

// getRecentChanges (v0.5.277) — global "what changed" summary
// fired from the page-top banner. Returns:
//   • open problem counts broken down by severity (any service)
//   • recent service.version transitions in the last 30 min
//
// One round-trip for the banner; cached 15s so a 30-second
// poll from every operator's open tab collapses to one CH
// query per replica per quarter-minute.
func (s *Server) getRecentChanges(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "recent-changes", 15*time.Second, func() (any, error) {
		// Tally open problems globally — sum the per-service
		// counts. Reuses the existing GetOpenProblemCountsByService
		// scan so we don't fan a second FINAL over the problems
		// table.
		counts, err := s.store.GetOpenProblemCountsByService(r.Context())
		var critical, warning, info int
		if err == nil {
			for _, c := range counts {
				critical += c.Critical
				warning += c.Warning
				info += c.Info
			}
		}
		deploys, derr := s.store.GetRecentDeploys(r.Context(), 30*time.Minute, 20)
		if derr != nil {
			// Banner is best-effort — deploy lookup failure
			// shouldn't blank the problem counts.
			deploys = nil
		}
		return map[string]any{
			"openProblems": map[string]int{
				"critical": critical,
				"warning":  warning,
				"info":     info,
			},
			"recentDeploys": deploys,
		}, nil
	})
}

