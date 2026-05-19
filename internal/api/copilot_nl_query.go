package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/copilot"
)

// copilotNLToQuery — v0.5.255. Operator types a plain-English
// description of what they're looking for ("yesterday's slow
// checkouts") on /explore; this endpoint runs the Copilot with a
// strict-JSON system prompt and returns a {filters, range, explain}
// payload the SPA applies directly to its filter/range state.
//
// The endpoint is intentionally simple: pass-through user prompt,
// validate the JSON shape on the way out so a hallucinated string
// in `v[]` doesn't reach the FilterBuilder. On parse failure the
// frontend renders a "couldn't parse — try rephrasing" hint and
// leaves the existing filters in place; we never auto-clear the
// operator's working state.
//
// Surface = "nl-to-query" so /ai breaks out this traffic from
// operator-clicked Explain calls.

type nlToQueryFilter struct {
	K  string   `json:"k"`
	Op string   `json:"op"`
	V  []string `json:"v"`
}

type nlToQueryResponse struct {
	Filters []nlToQueryFilter `json:"filters"`
	Range   struct {
		Preset string `json:"preset"`
	} `json:"range"`
	Explain string `json:"explain"`
}

// allowed ops mirror the FilterOp union in frontend/src/lib/types.ts.
// A hallucinated op gets dropped client-side too, but a server-side
// guard means /ai analytics never sees the bad payload.
var allowedOps = map[string]bool{
	"=": true, "!=": true,
	"LIKE": true, "NOT LIKE": true,
	"IN": true, "NOT IN": true,
	">": true, ">=": true, "<": true, "<=": true,
	"EXISTS": true, "NOT EXISTS": true,
}

// allowed range presets mirror PRESET_SECONDS in frontend/src/lib/utils.ts.
var allowedPresets = map[string]bool{
	"1m": true, "5m": true, "15m": true, "30m": true,
	"1h": true, "3h": true, "6h": true, "12h": true,
	"24h": true, "2d": true, "3d": true, "7d": true,
	"14d": true, "30d": true,
}

func (s *Server) copilotNLToQuery(w http.ResponseWriter, r *http.Request) {
	if !s.copilot.Configured() {
		http.Error(w, `{"error":"AI Copilot not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid JSON: `+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	body.Prompt = strings.TrimSpace(body.Prompt)
	if body.Prompt == "" {
		http.Error(w, `{"error":"prompt required"}`, http.StatusBadRequest)
		return
	}
	// Cap the user-prompt to a sane length — 1KB is plenty for a
	// search description, and prevents a runaway operator paste
	// from blowing the LLM context budget on a single query.
	if len(body.Prompt) > 1024 {
		body.Prompt = body.Prompt[:1024]
	}

	out, err := s.copilotExplain(r, copilot.SystemPromptNLToQuery(), body.Prompt)
	if err != nil {
		writeErr(w, err)
		return
	}

	// Strip a fenced code block if the model ignored the instruction
	// and wrapped its output in ```. Common enough to be worth the
	// belt-and-suspenders parse step.
	out = strings.TrimSpace(out)
	out = strings.TrimPrefix(out, "```json")
	out = strings.TrimPrefix(out, "```")
	out = strings.TrimSuffix(out, "```")
	out = strings.TrimSpace(out)

	var parsed nlToQueryResponse
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		// Surface the raw output so the operator can see what the
		// model produced — useful for "why didn't this parse?".
		writeJSON(w, map[string]any{
			"filters": []any{},
			"range":   map[string]any{"preset": "1h"},
			"explain": "",
			"warning": "model output was not valid JSON",
			"raw":     out,
		})
		return
	}

	// Validate / sanitise. Drop any filter with an unknown op or
	// empty key; coerce an unknown range preset back to 1h.
	clean := nlToQueryResponse{
		Explain: parsed.Explain,
	}
	clean.Range.Preset = parsed.Range.Preset
	if !allowedPresets[clean.Range.Preset] {
		clean.Range.Preset = "1h"
	}
	for _, f := range parsed.Filters {
		f.Op = strings.TrimSpace(strings.ToUpper(f.Op))
		// Restore mixed-case for the comparators (the upper-case
		// fold above turned LIKE → LIKE which is fine, but `=`
		// stays `=` — no harm done; the map is upper-keyed for
		// the textual ops only).
		if f.Op == "=" || f.Op == "!=" || f.Op == ">" || f.Op == ">=" ||
			f.Op == "<" || f.Op == "<=" {
			// canonical already
		}
		if !allowedOps[f.Op] {
			continue
		}
		f.K = strings.TrimSpace(f.K)
		if f.K == "" || len(f.V) == 0 {
			continue
		}
		// Cap pathological v[]: if the model dumps 100 values
		// drop the tail, keeps the URL state from blowing up.
		if len(f.V) > 20 {
			f.V = f.V[:20]
		}
		clean.Filters = append(clean.Filters, f)
	}

	writeJSON(w, clean)
}
