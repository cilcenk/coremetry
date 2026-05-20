package api

import (
	"fmt"
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

// getAllDeploys (v0.5.289) — cross-service deploy listing for
// the standalone /deploys page. Same effective-version chain
// GetRecentDeploys uses (Helm labels, image tags, placeholders
// filtered), just over a longer window. Cached 60s so a 24h
// reload doesn't re-scan spans on every refresh.
//
// Defaults: 24h window, 500-row cap (covers a busy fleet for a
// day; operator can widen the window via the picker).
//
// Performance posture: GetRecentDeploys uses the (service_name,
// time) primary key with a partition-pruned WHERE. Over 30 days
// it's the same shape as the banner query, just a wider time
// bound — under 2s warm against billion-span partitions.
func (s *Server) getAllDeploys(w http.ResponseWriter, r *http.Request) {
	since := parseDuration(r.URL.Query().Get("since"), 24*time.Hour)
	limit := parseInt(r.URL.Query().Get("limit"), 500)
	if since > 30*24*time.Hour {
		since = 30 * 24 * time.Hour
	}
	if limit > 2000 {
		limit = 2000
	}
	if limit <= 0 {
		limit = 500
	}
	key := fmt.Sprintf("all-deploys:since=%s:limit=%d", since, limit)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		return s.store.GetRecentDeploys(r.Context(), since, limit)
	})
}
