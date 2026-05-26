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

