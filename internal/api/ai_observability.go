// Package api — AI observability endpoints (v0.5.163). The
// /ai page is the Coremetry-native counterpart to Langfuse: every
// Copilot Explain call lands as a row in the ai_calls CH table
// and these endpoints surface it as KPIs / timeseries / a recent-
// calls table. No external service involvement: prompts + samples
// stay inside the customer's CH cluster.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// AIRate is the per-model price quote used by the /ai cost
// estimate (v0.5.167). USD per 1M tokens, separately for input
// and output. Stored as a JSON blob in system_settings["ai.rates"]
// so admins can override the bundled defaults without code change.
// Local-model endpoints (Ollama, vLLM, LM Studio) should
// configure 0/0 — that's the default shape for any model not
// in the table.
type AIRate struct {
	InputPer1M  float64 `json:"inputPer1M"`
	OutputPer1M float64 `json:"outputPer1M"`
}

// aiRatesKey persists per-model price overrides keyed by the
// model string Copilot reports (gpt-4o-mini, claude-sonnet-4-6,
// llama3.1:8b, etc.). Operator-set entries win over the bundled
// table. Frontend reads this on the /ai page to compute cost
// at render time.
const aiRatesKey = "ai.rates"

// getAIRates returns the operator-set rate overrides (may be
// empty). UI merges with bundled defaults client-side so a new
// install shows reasonable estimates immediately.
func (s *Server) getAIRates(w http.ResponseWriter, r *http.Request) {
	raw, err := s.store.GetSetting(r.Context(), aiRatesKey)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := map[string]AIRate{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			// Corrupt blob — fall back to empty rather than error,
			// the operator can re-save from the UI.
			out = map[string]AIRate{}
		}
	}
	writeJSON(w, out)
}

// putAIRates replaces the entire rate map. Empty map is valid
// (resets to bundled defaults).
func (s *Server) putAIRates(w http.ResponseWriter, r *http.Request) {
	var body map[string]AIRate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	// Drop entries with both rates zero — that's just the
	// default-zero state and clutters the persisted blob.
	cleaned := map[string]AIRate{}
	for k, v := range body {
		k = strings.TrimSpace(k)
		if k == "" || (v.InputPer1M == 0 && v.OutputPer1M == 0) {
			continue
		}
		cleaned[k] = v
	}
	raw, _ := json.Marshal(cleaned)
	if err := s.store.PutSetting(r.Context(), aiRatesKey, raw); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "settings.ai_rates.update", "settings", "ai.rates",
		fmt.Sprintf(`{"models":%d}`, len(cleaned)))
	writeJSON(w, cleaned)
}

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
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.ComputeAIStats(ctx, from, to)
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
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.AICallsTimeseries(ctx, from, to, bucketSec)
	})
}
