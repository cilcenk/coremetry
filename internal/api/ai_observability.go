// Package api — AI observability endpoints (v0.5.163). The
// /ai page is the Coremetry-native counterpart to Langfuse: every
// Copilot Explain call lands as a row in the ai_calls CH table
// and these endpoints surface it as KPIs / timeseries / a recent-
// calls table. No external service involvement: prompts + samples
// stay inside the customer's CH cluster.
package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// copilotExplain wraps copilot.Service.Explain with the surface +
// user metadata that the recorder needs to attribute the call. All
// existing s.copilot.Explain(r.Context(), …) call sites can be
// search/replaced to s.copilotExplain(r, …) without touching the
// rest of the handler — surface is derived from the request path
// and the auth claims live in ctx via the auth middleware.
func (s *Server) copilotExplain(r *http.Request, system, user string) (string, error) {
	surface := aiSurfaceFromPath(r.URL.Path)
	c := auth.FromContext(r.Context())
	uid, email := "", ""
	if c != nil {
		uid, email = c.UserID, c.Email
	}
	ctx := copilot.WithMeta(r.Context(), copilot.CallMeta{
		Surface:   surface,
		UserID:    uid,
		UserEmail: email,
	})
	return s.copilot.Explain(ctx, system, user)
}

// aiSurfaceFromPath maps the request path to a short stable
// surface label for grouping. Every /api/copilot/* endpoint has
// a unique path so we just take the last segment with the
// leading verb stripped — `/api/copilot/explain-span` → "explain-
// span", `/api/copilot/explain-slo/{id}` → "explain-slo", etc.
// Unknown paths collapse to "other" so the /ai breakdown stays
// finite.
func aiSurfaceFromPath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "copilot" {
		return "other"
	}
	seg := parts[2]
	// Drop trailing dynamic segments — anything past the verb is
	// an id or filter, not part of the surface name.
	return seg
}

// listAICalls — paginated recent-calls table on the /ai page.
// Filters: surface / provider / status / time range. Default
// window 24h.
func (s *Server) listAICalls(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := chstore.ListAICallsParams{
		Surface:  q.Get("surface"),
		Provider: q.Get("provider"),
		Status:   q.Get("status"),
		Limit:    parseInt(q.Get("limit"), 100),
	}
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	if !from.IsZero() {
		p.From = from
	}
	if !to.IsZero() {
		p.To = to
	}
	rows, err := s.store.ListAICalls(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	if rows == nil {
		rows = []chstore.AICall{}
	}
	writeJSON(w, rows)
}

// getAICall — single-call drill-in. Operator opens this from the
// list to see prompt + response in full (well, up to the 4KB
// sample cap applied at insert time).
func (s *Server) getAICall(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetAICall(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if c == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, c)
}

// aiStats — overview KPIs + per-surface + per-provider breakdown.
// Cached 30s; the /ai page polls this every minute and a heavy
// install with 30 operators on it shouldn't hit CH 30 times for
// the same numbers.
func (s *Server) aiStats(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, 24*time.Hour)
	key := fmt.Sprintf("ai-stats:from=%d:to=%d",
		from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute))
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.ComputeAIStats(r.Context(), from, to)
	})
}

// aiSeries — volume / errors / latency / token timeseries for the
// /ai page line chart. Bucket size is derived from the window
// length so a 24h window has 5-min buckets, a 7d window has 1h
// buckets, etc.
func (s *Server) aiSeries(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, 24*time.Hour)
	// Aim for ~120 points so the line chart looks dense but the
	// SVG stays responsive.
	bucketSec := int(to.Sub(from).Seconds() / 120)
	if bucketSec < 60 {
		bucketSec = 60
	}
	key := fmt.Sprintf("ai-series:from=%d:to=%d:b=%d",
		from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute), bucketSec)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.AICallsTimeseries(r.Context(), from, to, bucketSec)
	})
}
