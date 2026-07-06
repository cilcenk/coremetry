package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// getTraceShapes (v0.5.264) returns the dominant trace-shape
// clusters for a time window — operator-facing "mass data
// analysis" view that collapses millions of traces into a few
// dozen distinct (service, operation) signature cohorts.
//
// 30s cached because the underlying CH query is heavy enough
// (full-table GROUP BY trace_id at sample rate) that operators
// hammering refresh would saturate one CPU per replica. Cache
// key includes the filter set so different slices cache
// independently.
func (s *Server) getTraceShapes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	if from.IsZero() || to.IsZero() {
		// Default 15-minute window if none supplied — matches the
		// rest of the explore page's default.
		to = time.Now()
		from = to.Add(-15 * time.Minute)
	}
	service := q.Get("service")
	limit := parseInt(q.Get("limit"), 30)

	key := fmt.Sprintf("trace-shapes:from=%d:to=%d:svc=%s:lim=%d",
		from.UnixNano(), to.UnixNano(), service, limit)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetTraceShapes(ctx, chstore.TraceShapesFilter{
			From: from, To: to, Service: service, Limit: limit,
		})
	})
}
