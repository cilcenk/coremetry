// Prompts — v0.6.7. Exposes Coremetry's curated system prompts
// (the same ones the in-app ✨ Explain button uses) as MCP
// prompts so external LLM clients can invoke them via prompts/
// get and get a complete system+user message pair ready for a
// chat completion.
//
// What the renderer does: takes the system prompt body from
// `copilot.SystemPromptX()`, plus a freshly-fetched data payload
// (trace, problem, anomaly, etc.) from the store, and packages
// them into a `[system, user]` message pair the client can
// directly replay against any model. The MCP client doesn't
// have to do a separate tools/call to fetch the data — the
// prompt arrives self-contained.
//
// This mirrors the in-app affordance ("✨ Explain this trace")
// over the MCP wire so the same SRE workflow works in Claude
// Desktop or an internal copilot.

package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/mcp"
)

// registerPrompts installs the v0.6.7 prompt catalogue.
//
// We expose the prompts that have a one-arg "explain this <id>"
// shape — those are the ones an LLM-client / external operator
// can usefully invoke without context they don't have. Prompts
// that compose multiple traces (compare_traces) or deploy diffs
// (deploy_impact) need richer args and ship in a later release
// when MCP gets a structured-args extension or the operator UI
// surfaces them as palette actions.
func registerPrompts(srv *mcp.Server, d Deps) {
	srv.RegisterPrompt(mcp.Prompt{
		Name:        "explain_trace",
		Description: "Coremetry SRE: explain a trace flamegraph — root operation, slow spans, error pattern, hint for next investigation step.",
		Arguments: []mcp.PromptArgument{
			{Name: "trace_id", Description: "32-char hex trace ID.", Required: true},
		},
		Renderer: func(ctx context.Context, args map[string]string) ([]mcp.PromptMessage, error) {
			spans, err := d.Store.GetTrace(ctx, args["trace_id"])
			if err != nil {
				return nil, err
			}
			if len(spans) == 0 {
				return nil, fmt.Errorf("trace %s not found", args["trace_id"])
			}
			user, err := jsonText(map[string]any{"trace_id": args["trace_id"], "spans": spans})
			if err != nil {
				return nil, err
			}
			return pair(copilot.SystemPromptTrace(), user), nil
		},
	})

	srv.RegisterPrompt(mcp.Prompt{
		Name:        "explain_problem",
		Description: "Coremetry SRE: explain a Problem (alert that fired) — what it means in plain language, most likely causes ranked, first 3 things to check.",
		Arguments: []mcp.PromptArgument{
			{Name: "problem_id", Description: "Problem row ID from list_problems.", Required: true},
		},
		Renderer: func(ctx context.Context, args map[string]string) ([]mcp.PromptMessage, error) {
			prob, err := d.Store.GetProblem(ctx, args["problem_id"])
			if err != nil {
				return nil, err
			}
			if prob == nil {
				return nil, fmt.Errorf("problem %s not found", args["problem_id"])
			}
			user, err := jsonText(prob)
			if err != nil {
				return nil, err
			}
			return pair(copilot.SystemPromptProblem(), user), nil
		},
	})

	srv.RegisterPrompt(mcp.Prompt{
		Name:        "suggest_runbook",
		Description: "Coremetry SRE: numbered executable runbook for handling an open Problem. Anchors steps in past resolution times so fast-resolving rules surface quick paths first.",
		Arguments: []mcp.PromptArgument{
			{Name: "problem_id", Description: "Problem row ID.", Required: true},
		},
		Renderer: func(ctx context.Context, args map[string]string) ([]mcp.PromptMessage, error) {
			prob, err := d.Store.GetProblem(ctx, args["problem_id"])
			if err != nil {
				return nil, err
			}
			if prob == nil {
				return nil, fmt.Errorf("problem %s not found", args["problem_id"])
			}
			user, err := jsonText(prob)
			if err != nil {
				return nil, err
			}
			return pair(copilot.SystemPromptRunbook(), user), nil
		},
	})

	srv.RegisterPrompt(mcp.Prompt{
		Name:        "explain_service_health",
		Description: "Coremetry SRE: quick 'is this service healthy right now?' read on a service's RED metrics over the recent window.",
		Arguments: []mcp.PromptArgument{
			{Name: "service", Description: "Service name.", Required: true},
		},
		Renderer: func(ctx context.Context, args map[string]string) ([]mcp.PromptMessage, error) {
			from, to := rangeWindow(1800)
			rows, err := d.Store.GetServicesFiltered(ctx, 0, from, to, args["service"], "rps", "desc", 1, 0)
			if err != nil {
				return nil, err
			}
			if len(rows) == 0 {
				return nil, fmt.Errorf("service %s has no data in last 30m", args["service"])
			}
			probs, _ := d.Store.CountProblems(ctx, chstore.ProblemFilter{
				Status:  "open",
				Service: args["service"],
			})
			user, err := jsonText(map[string]any{
				"summary":       rows[0],
				"open_problems": probs,
				"window_s":      1800,
			})
			if err != nil {
				return nil, err
			}
			return pair(copilot.SystemPromptServiceHealth(), user), nil
		},
	})

	srv.RegisterPrompt(mcp.Prompt{
		Name:        "explain_exception",
		Description: "Coremetry SRE: explain a code exception (type + message + stacktrace) — meaning, likely cause from the call site, fix hint.",
		Arguments: []mcp.PromptArgument{
			{Name: "type", Description: "Exception class name.", Required: true},
			{Name: "message", Description: "Exception message.", Required: false},
			{Name: "stacktrace", Description: "Stack trace text.", Required: false},
			{Name: "service", Description: "Service where the exception fired.", Required: false},
		},
		Renderer: func(_ context.Context, args map[string]string) ([]mcp.PromptMessage, error) {
			user, err := jsonText(map[string]any{
				"type":       args["type"],
				"message":    args["message"],
				"stacktrace": args["stacktrace"],
				"service":    args["service"],
			})
			if err != nil {
				return nil, err
			}
			return pair(copilot.SystemPromptException(), user), nil
		},
	})

	// v0.6.17 — compare_traces. Mirrors the in-app "Compare with…"
	// affordance: operator picks two trace IDs and wants to know
	// why they diverged. We fetch both traces, produce a compact
	// diff (root summary + per-op latency delta + error footprint),
	// pair with the canonical compare-traces system prompt.
	srv.RegisterPrompt(mcp.Prompt{
		Name:        "compare_traces",
		Description: "Coremetry SRE: explain why two traces diverged — root summaries, per-op latency delta, services-only-in-one, error footprint. Use the typical 'today's slow trace vs yesterday's fast one' workflow.",
		Arguments: []mcp.PromptArgument{
			{Name: "trace_a", Description: "First trace ID (the 'baseline').", Required: true},
			{Name: "trace_b", Description: "Second trace ID (typically the slow / broken one).", Required: true},
		},
		Renderer: func(ctx context.Context, args map[string]string) ([]mcp.PromptMessage, error) {
			a, err := d.Store.GetTrace(ctx, args["trace_a"])
			if err != nil {
				return nil, err
			}
			if len(a) == 0 {
				return nil, fmt.Errorf("trace_a %s not found", args["trace_a"])
			}
			b, err := d.Store.GetTrace(ctx, args["trace_b"])
			if err != nil {
				return nil, err
			}
			if len(b) == 0 {
				return nil, fmt.Errorf("trace_b %s not found", args["trace_b"])
			}
			user, err := jsonText(map[string]any{
				"trace_a": map[string]any{"id": args["trace_a"], "summary": traceSummary(a)},
				"trace_b": map[string]any{"id": args["trace_b"], "summary": traceSummary(b)},
				"diff":    diffTraces(a, b),
			})
			if err != nil {
				return nil, err
			}
			return pair(copilot.SystemPromptCompareTraces(), user), nil
		},
	})

	// v0.6.17 — deploy_impact. Mirrors the in-app "Explain latest
	// deploy" affordance. Operator names a service + the deploy
	// time (ms since epoch); we compute before/after RED snapshots
	// over a ±10min window and ask the model "clean / minor
	// regression / rollback candidate?". Backend logic re-uses
	// chstore.GetServicesFiltered which already rides
	// service_summary_5m at this granularity.
	srv.RegisterPrompt(mcp.Prompt{
		Name:        "deploy_impact",
		Description: "Coremetry SRE: explain a deploy's RED-metric impact — before/after windows around the named deploy time, anchored to the named service. Returns a 'clean / minor regression / rollback candidate' verdict + the biggest delta.",
		Arguments: []mcp.PromptArgument{
			{Name: "service", Description: "Service that was deployed.", Required: true},
			{Name: "deploy_time_ms", Description: "Deploy time as ms since epoch.", Required: true},
			{Name: "window_s", Description: "Half-window seconds before+after deploy. Default 600 (10min).", Required: false},
			{Name: "version", Description: "Optional version label.", Required: false},
		},
		Renderer: func(ctx context.Context, args map[string]string) ([]mcp.PromptMessage, error) {
			deployMs, err := parseInt64(args["deploy_time_ms"])
			if err != nil {
				return nil, fmt.Errorf("deploy_time_ms: %w", err)
			}
			win := 600
			if s := args["window_s"]; s != "" {
				if w, err := parseInt64(s); err == nil && w > 0 {
					win = int(w)
				}
			}
			if win > 6*3600 {
				win = 6 * 3600
			}
			deployT := time.UnixMilli(deployMs)
			before := deployT.Add(-time.Duration(win) * time.Second)
			after := deployT.Add(time.Duration(win) * time.Second)
			befRows, err := d.Store.GetServicesFiltered(ctx, 0, before, deployT, args["service"], "rps", "desc", 1, 0)
			if err != nil {
				return nil, err
			}
			aftRows, err := d.Store.GetServicesFiltered(ctx, 0, deployT, after, args["service"], "rps", "desc", 1, 0)
			if err != nil {
				return nil, err
			}
			user, err := jsonText(map[string]any{
				"service":      args["service"],
				"version":      args["version"],
				"deploy_time":  deployT.UTC().Format(time.RFC3339),
				"window_s":     win,
				"before":       firstOrNil(befRows),
				"after":        firstOrNil(aftRows),
			})
			if err != nil {
				return nil, err
			}
			return pair(copilot.SystemPromptDeployImpact(), user), nil
		},
	})
}

// traceSummary captures the root + total span count of a trace —
// enough for the system prompt to reason about "trace A had N
// spans, root op X, total Y ms".
func traceSummary(spans []chstore.SpanRow) map[string]any {
	out := map[string]any{
		"span_count": len(spans),
	}
	for _, sp := range spans {
		if sp.ParentSpanID == "" {
			out["root_op"] = sp.Name
			out["root_service"] = sp.ServiceName
			out["root_duration_ms"] = sp.DurationMs
			break
		}
	}
	return out
}

// diffTraces produces a compact per-operation latency delta + the
// services-only-in-one set. Kept simple on purpose — the model is
// good at reasoning over a tidy JSON summary; piling every span
// in would blow the context window.
func diffTraces(a, b []chstore.SpanRow) map[string]any {
	aOps := opLatencies(a)
	bOps := opLatencies(b)
	type deltaRow struct {
		Op        string  `json:"op"`
		ADurMs    float64 `json:"a_ms"`
		BDurMs    float64 `json:"b_ms"`
		DeltaMs   float64 `json:"delta_ms"`
	}
	var shared []deltaRow
	for op, aMs := range aOps {
		if bMs, ok := bOps[op]; ok {
			shared = append(shared, deltaRow{Op: op, ADurMs: aMs, BDurMs: bMs, DeltaMs: bMs - aMs})
		}
	}
	// Sort by absolute delta desc — biggest contributor first.
	// Tiny n; bubble-sort would work but go's sort is cheaper.
	for i := 1; i < len(shared); i++ {
		for j := i; j > 0 && abs(shared[j].DeltaMs) > abs(shared[j-1].DeltaMs); j-- {
			shared[j], shared[j-1] = shared[j-1], shared[j]
		}
	}
	if len(shared) > 10 {
		shared = shared[:10]
	}
	return map[string]any{
		"top_op_deltas":       shared,
		"services_only_in_a":  serviceSetDiff(a, b),
		"services_only_in_b":  serviceSetDiff(b, a),
		"errors_in_a":         countErrors(a),
		"errors_in_b":         countErrors(b),
	}
}

// opLatencies returns the max duration (ms) for each operation name
// in the trace — for shared ops the larger number is the
// "characteristic" latency (LLMs do well with one-line-per-op).
func opLatencies(spans []chstore.SpanRow) map[string]float64 {
	out := map[string]float64{}
	for _, sp := range spans {
		if cur, ok := out[sp.Name]; !ok || sp.DurationMs > cur {
			out[sp.Name] = sp.DurationMs
		}
	}
	return out
}

func serviceSetDiff(left, right []chstore.SpanRow) []string {
	r := map[string]struct{}{}
	for _, sp := range right {
		r[sp.ServiceName] = struct{}{}
	}
	seen := map[string]struct{}{}
	var out []string
	for _, sp := range left {
		if _, ok := r[sp.ServiceName]; ok {
			continue
		}
		if _, dup := seen[sp.ServiceName]; dup {
			continue
		}
		seen[sp.ServiceName] = struct{}{}
		out = append(out, sp.ServiceName)
	}
	return out
}

func countErrors(spans []chstore.SpanRow) int {
	n := 0
	for _, sp := range spans {
		if sp.StatusCode == "error" {
			n++
		}
	}
	return n
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func firstOrNil[T any](rows []T) any {
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscan(s, &n)
	return n, err
}

// jsonText marshals v compactly for embedding as the user
// message body. We don't pretty-print — LLMs handle compact JSON
// faster and we save context-window tokens.
func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// pair builds the canonical [system, user] message pair from a
// system prompt body + a user-message body.
func pair(system, user string) []mcp.PromptMessage {
	return []mcp.PromptMessage{
		{Role: "system", Content: mcp.PromptContent{Type: "text", Text: system}},
		{Role: "user", Content: mcp.PromptContent{Type: "text", Text: user}},
	}
}

