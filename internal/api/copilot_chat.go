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
// progress, then an `answer` event with the final prose. v0.8.404
// adds token streaming to the GUIDED path (copilot_guided.go emits
// `delta` events from StreamText, with a transparent buffered
// fallback); this free tool loop stays buffered — tool-call streaming
// is a different beast (see internal/copilot/stream.go header).
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

Use the provided tools to ground EVERY factual claim in live data — never invent service names, error rates, or trace IDs. When a question needs data, call a tool; when you have enough, answer concisely. Prefer specific numbers ("p99 was 2,130ms", "23 traces") over vague prose. Time windows: tools take range_s (seconds back from now); default to 1800 (30m) unless the operator says otherwise. If a tool returns nothing, say so plainly rather than guessing. Keep answers short and scannable — lead with the answer, then the supporting evidence. When the operator asks to SEE a chart or a visual trend would help, call render_chart — the UI draws it live; never draw ASCII charts or describe individual data points.` + copilot.AnswerInTurkish

type chatRequest struct {
	Messages []copilot.ChatMessage `json:"messages"`
	// Context (v0.9.164) — frontend'in bulunduğu sayfadan geçirdiği ipucu
	// (context-awareness): mesaj bir servis ADI taşımıyorsa guided router bu
	// servisi varsayılan alır ("neden yavaş?" checkout sayfasında → checkout).
	// Şeffaf: chat banner'ı "checkout servisindesin" der.
	Context struct {
		Service string `json:"service,omitempty"`
		// Operation (v0.9.184) — the ?op= the operator is viewing on a
		// service page; lets a bare "bu operasyonun durumu" scope RED to
		// that span name (guided router's operation fallback).
		Operation string `json:"operation,omitempty"`
	} `json:"context,omitempty"`
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
	//
	// exchangeID (v0.8.399, AI audit feedback slice) — one crypto/rand
	// hex id per exchange. Emitted to the UI in the answer event so the
	// thumbs up/down can POST it to /api/ai/feedback, and threaded via
	// CallMeta into the ai_calls row (exchange_id) so the verdict joins
	// back to the exact call it rates. Provider-agnostic plumbing —
	// works identically for anthropic / openai-compat / github, and for
	// both the guided path and the free tool loop.
	c := auth.FromContext(r.Context())
	uid, email := "", ""
	if c != nil {
		uid, email = c.UserID, c.Email
	}
	exchangeID := newRandID(16)
	ctx := copilot.WithMeta(r.Context(), copilot.CallMeta{
		Surface: "chat", UserID: uid, UserEmail: email, ExchangeID: exchangeID,
	})

	// v0.8.397 (AI audit A3) — guided mode first, for EVERY provider:
	// a deterministic intent router recognises the highest-value
	// question shapes, the server prefetches the data, and the model
	// makes exactly ONE tool-less narration call (copilot_guided.go).
	// Deterministic beats tool-roulette on these shapes even for
	// frontier models; the 2B-class primary target (qwen3.5-2b) can't
	// drive the 5-round × 11-schema loop reliably at all. No match →
	// the free tool loop below runs UNCHANGED.
	if handled, gok := s.copilotChatGuided(ctx, emit, req.Messages, req.Context.Service, req.Context.Operation); handled {
		emit("done", map[string]bool{"ok": gok})
		return
	}

	// v0.8.438 — doküman RAG yolu: guided telemetri router'ı
	// eşleşmediyse ve soru yüklü dokümanlara yeterince benziyorsa
	// (skor tabanı) tek narration çağrısıyla kaynak atıflı cevap.
	// Sıra bilinçli: telemetri şekilleri > dokümanlar > serbest döngü.
	if handled, rok := s.ragChatAnswer(ctx, emit, req.Messages); handled {
		emit("done", map[string]bool{"ok": rok})
		return
	}

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

	// CoSRE Faz-2 — render_chart server-side emission: the model PICKS
	// the chart by calling the render_chart tool; the SERVER builds the
	// deterministic ```chart``` fence from the handler's VALIDATED spec
	// (chartFence, copilot_guided.go) and appends it to the final
	// answer. A gemma4-class small model never formats chart JSON, and
	// a hallucinated service/metric never reaches the UI (the handler
	// returns ok:false for those). Blocks accumulate across rounds,
	// deduped by service+operation+agg.
	var chartBlocks []string
	chartSeen := map[string]bool{}
	appendCharts := func(text string) string {
		if len(chartBlocks) == 0 {
			return text
		}
		return strings.TrimRight(text, "\n") + "\n" + strings.Join(chartBlocks, "")
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
		// No tool calls → this turn's text is the final answer (plus any
		// chart blocks accumulated from earlier render_chart rounds).
		if len(turn.ToolCalls) == 0 {
			finalText = appendCharts(turn.Text)
			emit("answer", map[string]string{"text": finalText, "exchangeId": exchangeID})
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
			// CoSRE Faz-2 — intercept render_chart: parse the handler's
			// validated output (never tc.Input — the model's raw args may
			// name a service that doesn't exist) into a ```chart``` fence.
			if tc.Name == "render_chart" && herr == nil {
				if block, key := chatChartBlock(out); block != "" && !chartSeen[key] {
					chartSeen[key] = true
					chartBlocks = append(chartBlocks, block)
				}
			}
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
				finalText = appendCharts(turn2.Text)
				emit("answer", map[string]string{"text": finalText, "exchangeId": exchangeID})
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

// chatChartBlock (CoSRE Faz-2) parses the render_chart handler's output
// — the server-VALIDATED spec, not the model's raw tool input — and
// returns the deterministic ```chart``` fence (chartFence,
// copilot_guided.go) plus a service+operation+agg dedup key. Empty
// block = not renderable (ok:false, malformed, or incomplete spec).
// Pure — table-tested in copilot_chat_test.go.
func chatChartBlock(out string) (block, key string) {
	var rc struct {
		OK   bool `json:"ok"`
		Spec struct {
			Service   string `json:"service"`
			Operation string `json:"operation"`
			Agg       string `json:"agg"`
			RangeS    int64  `json:"rangeS"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(out), &rc); err != nil || !rc.OK || rc.Spec.Service == "" || rc.Spec.Agg == "" {
		return "", ""
	}
	titleBase := rc.Spec.Service
	if rc.Spec.Operation != "" {
		titleBase = rc.Spec.Operation
	}
	fence := chartFence(guidedChartSpec{
		Title:     titleBase + " · " + rc.Spec.Agg,
		Service:   rc.Spec.Service,
		Operation: rc.Spec.Operation,
		Agg:       rc.Spec.Agg,
		RangeS:    rc.Spec.RangeS,
	})
	return fence, rc.Spec.Service + "\x00" + rc.Spec.Operation + "\x00" + rc.Spec.Agg
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
