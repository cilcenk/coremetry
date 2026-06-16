package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/mcptools"
)

// In-app AI chatbot (v0.6.53). An agentic loop that lets the
// operator ask free-form questions ("why is payment-service slow?",
// "errors in the last hour") and answers them grounded in their own
// telemetry. The LLM's function-calling backend is the SAME 7 tools
// the MCP server exposes (mcptools.ToolList) — list_services,
// get_service_health, list_problems, list_anomalies, search_logs,
// get_trace, query_metric — so the chat can read live data without
// any new query plumbing.
//
// Transport: POST with the full conversation, response streamed as
// SSE. v1 is STEP-streaming (operator decision 2026-05-28): we emit
// a `step` event per tool call so the operator sees "⚙ list_services"
// progress, then an `answer` event with the final prose. True
// token-streaming (Anthropic stream:true) is a follow-up.
//
// Conversation is EPHEMERAL — the frontend holds history in
// component state and sends it whole each turn; nothing persists
// server-side (no saved_views row). Auth: any authenticated user —
// all 7 tools are read-only, so a viewer chatting is safe.

const (
	chatMaxToolRounds = 5  // guardrail: cap the agentic loop so a model can't fan tool calls forever
	chatMaxMessages   = 40 // cap conversation length fed back to the LLM (token budget)
)

// chatSystemPrompt frames the assistant as a Coremetry-native SRE
// copilot and tells it the tools are its only source of truth so it
// doesn't hallucinate service names / metrics.
const chatSystemPrompt = `You are Coremetry's in-app observability assistant. You help operators investigate their own telemetry: services, traces, logs, metrics, problems, and anomalies.

Use the provided tools to ground EVERY factual claim in live data — never invent service names, error rates, or trace IDs. When a question needs data, call a tool; when you have enough, answer concisely. Prefer specific numbers ("p99 was 2,130ms", "23 traces") over vague prose. Time windows: tools take range_s (seconds back from now); default to 1800 (30m) unless the operator says otherwise. If a tool returns nothing, say so plainly rather than guessing. Keep answers short and scannable — lead with the answer, then the supporting evidence.`

type chatRequest struct {
	Messages []copilot.ChatMessage `json:"messages"`
}

// copilotChat is the SSE chat endpoint. Runs the agentic loop and
// streams progress + the final answer. One ai_calls row is written
// per exchange (summing per-round token usage) via RecordUsage.
func (s *Server) copilotChat(w http.ResponseWriter, r *http.Request) {
	if s.copilot == nil || !s.copilot.Active() {
		http.Error(w, `{"error":"AI copilot not available (disabled or not configured)"}`, http.StatusServiceUnavailable)
		return
	}
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		http.Error(w, `{"error":"messages required"}`, http.StatusBadRequest)
		return
	}
	// Trim to the most recent N so a long session can't blow the
	// token budget; the tail carries the active question + context.
	if len(req.Messages) > chatMaxMessages {
		req.Messages = req.Messages[len(req.Messages)-chatMaxMessages:]
	}

	// SSE plumbing — same header set + flusher assert the sse.Broker
	// handler uses.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	emit := func(event string, payload any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}

	// Attribution: tag ctx so RecordUsage attributes the exchange to
	// the "chat" surface on the /ai page.
	c := auth.FromContext(r.Context())
	uid, email := "", ""
	if c != nil {
		uid, email = c.UserID, c.Email
	}
	ctx := copilot.WithMeta(r.Context(), copilot.CallMeta{Surface: "chat", UserID: uid, UserEmail: email})

	// Build the tool set once (closures over the live store + logs)
	// and the LLM-facing specs from the same list.
	tools := mcptools.ToolList(mcptools.Deps{Store: s.store, LogStore: s.logs})
	byName := make(map[string]func(context.Context, json.RawMessage) (any, error), len(tools))
	specs := make([]copilot.ToolSpec, 0, len(tools))
	for _, t := range tools {
		byName[t.Name] = t.Handler
		specs = append(specs, copilot.ToolSpec{
			Name: t.Name, Description: t.Description, InputSchema: t.InputSchema,
		})
	}

	conv := req.Messages
	var totalIn, totalOut uint32
	var lastErr error
	var finalText string

	for round := 0; round < chatMaxToolRounds; round++ {
		turn, err := s.copilot.ChatWithTools(ctx, chatSystemPrompt, conv, specs)
		totalIn += turn.InputTokens
		totalOut += turn.OutputTokens
		if err != nil {
			lastErr = err
			emit("error", map[string]string{"error": err.Error()})
			break
		}
		// No tool calls → this turn's text is the final answer.
		if len(turn.ToolCalls) == 0 {
			finalText = turn.Text
			emit("answer", map[string]string{"text": finalText})
			break
		}
		// Record the assistant's tool-call turn, then execute each
		// call and feed results back as a user turn.
		conv = append(conv, copilot.ChatMessage{
			Role: "assistant", Text: turn.Text, ToolCalls: turn.ToolCalls,
		})
		results := make([]copilot.ToolResult, 0, len(turn.ToolCalls))
		for _, tc := range turn.ToolCalls {
			emit("step", map[string]string{"tool": tc.Name, "args": string(tc.Input)})
			h, found := byName[tc.Name]
			if !found {
				results = append(results, copilot.ToolResult{
					CallID: tc.ID, Name: tc.Name, IsError: true,
					Content: fmt.Sprintf("unknown tool %q", tc.Name),
				})
				continue
			}
			out, herr := runChatTool(ctx, h, tc.Input)
			tr := copilot.ToolResult{CallID: tc.ID, Name: tc.Name}
			if herr != nil {
				tr.IsError = true
				tr.Content = "error: " + herr.Error()
			} else {
				tr.Content = out
			}
			results = append(results, tr)
		}
		conv = append(conv, copilot.ChatMessage{Role: "user", ToolResults: results})

		// Hit the round cap with tool calls still pending → ask the
		// model for a best-effort answer with what it has, no more
		// tools, so the operator isn't left hanging.
		if round == chatMaxToolRounds-1 {
			turn2, err2 := s.copilot.ChatWithTools(ctx, chatSystemPrompt+
				"\n\nYou have reached the tool-call limit. Answer now with what you have.", conv, nil)
			totalIn += turn2.InputTokens
			totalOut += turn2.OutputTokens
			if err2 != nil {
				lastErr = err2
				emit("error", map[string]string{"error": err2.Error()})
			} else {
				finalText = turn2.Text
				emit("answer", map[string]string{"text": finalText})
			}
		}
	}

	// One ai_calls row per exchange. Prompt sample = the operator's
	// last user message; response sample = the final answer.
	status, errMsg := "ok", ""
	if lastErr != nil {
		status, errMsg = "error", lastErr.Error()
	}
	s.copilot.RecordUsage(ctx, totalIn, totalOut, status, errMsg, lastUserText(req.Messages), finalText)

	emit("done", map[string]bool{"ok": lastErr == nil})
}

// runChatTool invokes a tool handler with a bounded timeout and
// JSON-stringifies the result for feeding back to the LLM. The
// per-tool clampLimit caps (in mcptools) already bound result size;
// the timeout guards a slow CH query from stalling the whole chat.
func runChatTool(ctx context.Context, h func(context.Context, json.RawMessage) (any, error), args json.RawMessage) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := h(tctx, args)
	if err != nil {
		return "", err
	}
	b, merr := json.Marshal(out)
	if merr != nil {
		return "", merr
	}
	return string(b), nil
}

// lastUserText pulls the most recent user-typed message for the
// ai_calls prompt sample (skips tool-result turns).
func lastUserText(msgs []copilot.ChatMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && strings.TrimSpace(msgs[i].Text) != "" {
			return msgs[i].Text
		}
	}
	return ""
}
