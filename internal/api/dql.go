package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/dql"
	"github.com/cilcenk/coremetry/internal/logstore"
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
			GroupBy:     plan.GroupBy,
			From:        from, To: to,
			StepSeconds: plan.StepSeconds,
		}
		series, err = s.store.QuerySpanMetric(r.Context(), f)
	case dql.TableMetrics:
		f := chstore.MetricQueryFilter{
			Name:        plan.MetricName,
			Filters:     plan.Filters,
			Aggregation: plan.Aggregation,
			GroupBy:     plan.GroupBy,
			From:        from, To: to,
			StepSeconds: plan.StepSeconds,
		}
		series, err = s.store.QueryMetric(r.Context(), f)
	case dql.TableLogs:
		// v0.5.267 Phase 3 — logs via logstore.Store.Histogram.
		// Limitations (per logstore.Filter's narrow shape):
		//   • only count() agg makes sense — anything else
		//     errors with a clear "logs.summarize count() only"
		//   • filter shape: well-known fields (service.name,
		//     trace.id, severity) map to dedicated Filter
		//     fields; everything else collapses into a
		//     Search substring match (lossy but useful).
		//   • groupBy: only "service" / "severity" string
		//     buckets — that's what logstore.Histogram supports.
		if plan.Aggregation != "count" {
			http.Error(w, `{"error":"logs table only supports summarize count() in this release"}`, http.StatusBadRequest)
			return
		}
		series, err = runLogsHistogram(r, s.logs, plan, from, to)
	default:
		http.Error(w, `{"error":"unknown table"}`, http.StatusBadRequest)
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

// runLogsHistogram bridges a DQL Plan to logstore.Store.Histogram.
// Well-known filter keys map to dedicated Filter fields; unknown
// keys collapse into the Search substring match. GroupBy is
// reduced to the single string logstore supports ("service" or
// "severity"); any other groupBy errors back clearly so the
// operator knows the shape isn't logs-supported.
func runLogsHistogram(r *http.Request, logs logstore.Store, plan *dql.Plan, from, to time.Time) ([]chstore.SpanMetricSeries, error) {
	if logs == nil {
		return nil, fmt.Errorf("logs backend not configured")
	}
	f := logstore.Filter{From: from, To: to}
	for _, fe := range plan.Filters {
		val := ""
		if len(fe.Values) > 0 {
			val = fe.Values[0]
		}
		switch fe.Key {
		case "service.name", "service":
			f.Service = val
		case "trace.id", "traceId", "traceID":
			f.TraceID = val
		case "span.id", "spanId":
			f.SpanID = val
		default:
			// Everything else gets folded into the substring
			// Search. Lossy compared to a real attribute
			// predicate, but matches the /logs page UX.
			if f.Search == "" {
				f.Search = val
			} else {
				f.Search = f.Search + " " + val
			}
		}
	}

	// GroupBy must collapse to "service" or "severity" — the
	// only two strings logstore.Histogram recognises today.
	// Anything else errors.
	groupBy := ""
	for _, g := range plan.GroupBy {
		switch g {
		case "service.name", "service":
			if groupBy != "" {
				return nil, fmt.Errorf("logs.histogram supports only one groupBy attribute (got %q after %q)", g, groupBy)
			}
			groupBy = "service"
		case "severity", "severity_text", "severity.text":
			if groupBy != "" {
				return nil, fmt.Errorf("logs.histogram supports only one groupBy attribute (got %q after %q)", g, groupBy)
			}
			groupBy = "severity"
		default:
			return nil, fmt.Errorf("logs.histogram groupBy must be service or severity (got %q)", g)
		}
	}

	bucketSec := plan.StepSeconds
	if bucketSec <= 0 {
		// Match the LogsExplorer auto-bucket formula so DQL and
		// the UI builder agree on cadence for the same window.
		windowSec := int(to.Sub(from).Seconds())
		bucketSec = windowSec / 60
		if bucketSec < 1 {
			bucketSec = 1
		}
		if bucketSec > 1800 {
			bucketSec = 1800
		}
	}

	hist, err := logs.Histogram(r.Context(), f, bucketSec, groupBy)
	if err != nil {
		return nil, err
	}
	// Bridge logstore.LogSeries → chstore.SpanMetricSeries so the
	// rest of the DQL response pipeline (and the MultiLineChart
	// frontend) doesn't need a second shape.
	out := make([]chstore.SpanMetricSeries, 0, len(hist))
	for _, s := range hist {
		key := []string{s.Name}
		if s.Name == "" || s.Name == "_total" {
			key = []string{}
		}
		points := make([]chstore.SpanMetricPoint, 0, len(s.Points))
		for _, p := range s.Points {
			points = append(points, chstore.SpanMetricPoint{
				Time:  p.T,
				Value: float64(p.V),
			})
		}
		out = append(out, chstore.SpanMetricSeries{
			GroupKey: key,
			Points:   points,
		})
	}
	return out, nil
}
