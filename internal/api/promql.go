package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/promql"
)

// queryPromQL backs GET /api/metrics/promql?query=&from=&to=&step=&maxDataPoints=
// — an industry-standard PromQL range query over the OTel metric store
// (F4). Parses + evaluates via internal/promql: the HYBRID model — leaf
// selectors + rate/increase/histogram_quantile push down to the bounded
// chstore machinery (F1-F3: LIMIT + max_execution_time + time-bounded WHERE,
// distributed-safe); aggregations / binary ops land in later phases.
//
// Returns the same SpanMetricSeries[] shape the UI charts already consume.
// Auth: viewer+ via the global GET middleware (like /api/metrics/query — a
// read-only surface, no audit). serveCached (30s) keys on the full query so a
// dashboard's repeated polls stay cheap; sanitizeFloats (in serveCached)
// scrubs the NaN/Inf a PromQL expression can legitimately produce.
//
// PERFORMANCE (operator hard constraint): the evaluator rejects a too-deep or
// nameless query BEFORE any CH round trip, reuses the bounded leaf fetches, and
// caps the result series count — no unbounded selector, no full-catalogue scan.
func (s *Server) queryPromQL(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := strings.TrimSpace(q.Get("query"))
	if query == "" {
		writePromQLError(w, http.StatusBadRequest, "query is required")
		return
	}
	// Parse up front so a SYNTAX error is a clean 400 (the common user error),
	// not the 500 that serveCached's writeErr would emit for a bubbled error.
	expr, perr := promql.Parse(query)
	if perr != nil {
		writePromQLError(w, http.StatusBadRequest, perr.Error())
		return
	}

	step, _ := strconv.Atoi(q.Get("step"))
	maxDP, _ := strconv.Atoi(q.Get("maxDataPoints"))
	if maxDP < 0 {
		maxDP = 0
	} else if maxDP > 4000 {
		maxDP = 4000
	}
	to := parseTime(q.Get("to"))
	from := parseTime(q.Get("from"))
	now := time.Now()
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-1 * time.Hour)
	}

	key := fmt.Sprintf("promql:q=%s:step=%d:mdp=%d:from=%d:to=%d",
		query, step, maxDP, from.Unix()/60, to.Unix()/60)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return promql.Eval(ctx, s.store, expr, promql.EvalOptions{
			FromNs:        from.UnixNano(),
			ToNs:          to.UnixNano(),
			Step:          step,
			MaxDataPoints: maxDP,
		})
	})
}

func writePromQLError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
