package api

// v0.6.8 — ClickHouse query AI optimizer. Operator pastes raw CH
// SQL in /admin/clickhouse → server forwards through the
// Copilot wrapper with the SystemPromptCHQueryOptimize template
// → returns {optimized, explanation} so the operator can review
// the rewritten query before running it. Same shape as the
// existing NL-to-query handler (copilot_nl_query.go); the model
// is asked for strict JSON so the frontend doesn't have to
// fence-strip pretty markdown.
//
// Admin-gated route. Read-only against the LLM, so no audit
// entry (no state change). Adds a /ai page row because every
// copilotExplain call writes one regardless.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/copilot"
)

type chOptimizeRequest struct {
	Query string `json:"query"`
}

type chOptimizeResponse struct {
	Optimized   string `json:"optimized"`
	Explanation string `json:"explanation"`
	// Raw surfaces the model output verbatim when JSON parsing
	// fails so the operator can still see what came back — same
	// belt-and-suspenders pattern as copilot_nl_query.go.
	Raw     string `json:"raw,omitempty"`
	Warning string `json:"warning,omitempty"`
}

func (s *Server) copilotOptimizeCHQuery(w http.ResponseWriter, r *http.Request) {
	if s.copilot == nil || !s.copilot.Active() {
		http.Error(w, `{"error":"AI Copilot not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var body chOptimizeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid JSON: `+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	body.Query = strings.TrimSpace(body.Query)
	if body.Query == "" {
		http.Error(w, `{"error":"query required"}`, http.StatusBadRequest)
		return
	}
	// Cap query size so a runaway paste doesn't burn the LLM
	// context budget. 8KB ≈ 2k tokens — enough for the gnarliest
	// real CH query an SRE writes by hand.
	if len(body.Query) > 8192 {
		body.Query = body.Query[:8192]
	}

	out, err := s.copilotExplain(r, copilot.SystemPromptCHQueryOptimize(), body.Query)
	if err != nil {
		writeErr(w, err)
		return
	}

	out = strings.TrimSpace(out)
	// Strip ```json / ``` fences if the model wrapped them
	// despite the strict-output instruction.
	out = strings.TrimPrefix(out, "```json")
	out = strings.TrimPrefix(out, "```")
	out = strings.TrimSuffix(out, "```")
	out = strings.TrimSpace(out)

	var parsed chOptimizeResponse
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		// Surface the raw output so the operator can see what
		// the model produced — useful for "why didn't this
		// parse?" diagnosis without re-running the prompt.
		writeJSON(w, chOptimizeResponse{
			Raw:     out,
			Warning: "model output was not valid JSON; raw text included",
		})
		return
	}
	writeJSON(w, parsed)
}
