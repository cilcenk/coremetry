package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/dql"
)

// runDQL (v0.5.265) — Coremetry's unified query language
// dispatcher. Operator POSTs a DQL string + window; the parser
// compiles it to a Plan, the executor routes to the matching
// chstore method, and the response carries the resulting series
// PLUS the SQL preview so the /admin/query UI can show
// "what actually ran".
//
// Admin-only: this is the most-powerful general read surface
// in the app + the same posture as /admin/sql.
func (s *Server) runDQL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query string `json:"query"`
		From  int64  `json:"from"` // unix ns
		To    int64  `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid JSON: `+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	plan, err := dql.Compile(body.Query)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	from := time.Unix(0, body.From)
	to := time.Unix(0, body.To)
	if body.From == 0 || body.To == 0 {
		to = time.Now()
		from = to.Add(-15 * time.Minute)
	}

	var series []chstore.SpanMetricSeries
	switch plan.Table {
	case dql.TableSpans:
		f := chstore.SpanMetricFilter{
			Filters:     plan.Filters,
			Aggregation: plan.Aggregation,
			Field:       plan.Field,
			From:        from, To: to,
			StepSeconds: plan.StepSeconds,
		}
		series, err = s.store.QuerySpanMetric(r.Context(), f)
	case dql.TableMetrics:
		f := chstore.MetricQueryFilter{
			Name:        plan.MetricName,
			Filters:     plan.Filters,
			Aggregation: plan.Aggregation,
			From:        from, To: to,
			StepSeconds: plan.StepSeconds,
		}
		series, err = s.store.QueryMetric(r.Context(), f)
	default:
		http.Error(w, `{"error":"only spans + metrics tables wired in this release"}`, http.StatusBadRequest)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]any{
		"plan":   plan,
		"sql":    plan.SQLPreview(from, to),
		"series": series,
		"window": map[string]int64{"fromNs": from.UnixNano(), "toNs": to.UnixNano()},
	})
}
