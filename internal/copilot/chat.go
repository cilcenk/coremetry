package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// In-app chatbot tool-calling layer (v0.6.53). The single-shot
// Explain path can't drive the agentic chat: the chat needs
// multi-turn history AND function-calling so the LLM can pull
// telemetry on demand (the 7 MCP tools become its functions).
//
// ChatWithTools is provider-neutral at the boundary — the API
// handler builds a []ChatMessage + []ToolSpec and gets back a
// ChatTurn (either prose, or a batch of tool calls to execute and
// feed back). Provider-specific wire encoding lives in the three
// chat*WithTools branches:
//   - Anthropic: tools + tool_use/tool_result content blocks
//   - OpenAI / GitHub: OpenAI tools + tool_calls + role:tool msgs
// On-prem installs run any of the three, so all three carry the
// tool-calling path (operator decision 2026-05-28).

// ToolSpec is the provider-neutral function description handed to
// the LLM. InputSchema is JSON Schema (draft 2020-12) — the same
// shape the MCP tools already declare, so the API handler maps
// mcp.Tool → ToolSpec 1:1.
type ToolSpec struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// ToolCall is one function invocation the model requested.
type ToolCall struct {
	ID    string          // provider-issued id, echoed back in the result
	Name  string          // tool name
	Input json.RawMessage // arguments as JSON
}

// ToolResult is the output of executing a ToolCall, fed back into
// the next turn so the model can read it.
type ToolResult struct {
	CallID  string
	Name    string
	Content string // JSON-stringified tool output (or an error string)
	IsError bool
}

// ChatMessage is one provider-neutral conversation turn. A user
// turn carries Text (the question) OR ToolResults (function
// outputs fed back). An assistant turn carries Text (prose) and/or
// ToolCalls (functions it wants run).
type ChatMessage struct {
	Role        string       // "user" | "assistant"
	Text        string       `json:",omitempty"`
	ToolCalls   []ToolCall   `json:",omitempty"`
	ToolResults []ToolResult `json:",omitempty"`
}

// ChatTurn is one model response. When ToolCalls is non-empty the
// caller must execute them and loop; otherwise Text is the final
// answer.
type ChatTurn struct {
	Text         string
	ToolCalls    []ToolCall
	InputTokens  uint32
	OutputTokens uint32
}

// ChatWithTools runs ONE model turn over the conversation with the
// given tools available. Branches on the configured provider. No
// ai_calls recording here — the handler records once per user
// message after the agentic loop settles (RecordUsage), summing
// the per-turn token usage, so one chat exchange = one ai_calls row.
func (s *Service) ChatWithTools(ctx context.Context, system string, msgs []ChatMessage, tools []ToolSpec) (ChatTurn, error) {
	if !s.Active() {
		return ChatTurn{}, errors.New("AI copilot not available (disabled or not configured — open Settings → AI Copilot)")
	}
	s.mu.RLock()
	provider := s.provider
	s.mu.RUnlock()
	switch provider {
	case ProviderGitHub, ProviderOpenAI:
		return s.chatOpenAIWithTools(ctx, system, msgs, tools)
	default:
		return s.chatAnthropicWithTools(ctx, system, msgs, tools)
	}
}

// RecordUsage writes a single ai_calls row for a completed chat
// exchange. Mirrors the recording block in Explain so the /ai page
// attributes chat usage alongside the ✨ Explain surfaces. Surface
// comes from MetaFromContext (the handler sets it to "chat").
func (s *Service) RecordUsage(ctx context.Context, inTok, outTok uint32, status, errMsg, promptSample, respSample string) {
	if s.recorder == nil {
		return
	}
	s.mu.RLock()
	provider, model, baseURL := s.provider, s.model, s.baseURL
	s.mu.RUnlock()
	meta := MetaFromContext(ctx)
	rec := CallRecord{
		CreatedAt:      time.Now(),
		Surface:        meta.Surface,
		Provider:       provider,
		Model:          model,
		BaseURL:        baseURL,
		InputTokens:    inTok,
		OutputTokens:   outTok,
		Status:         status,
		PromptChars:    uint32(len(promptSample)),
		ResponseChars:  uint32(len(respSample)),
		UserID:         meta.UserID,
		UserEmail:      meta.UserEmail,
		PromptSample:   truncForSample(promptSample),
		ResponseSample: truncForSample(respSample),
	}
	if status == "error" {
		rec.ErrorMsg = truncErr(errMsg)
	}
	go func(r Recorder, rec CallRecord) {
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r.RecordCall(rctx, rec)
	}(s.recorder, rec)
}

// ── Anthropic tool-calling ──────────────────────────────────────

func (s *Service) chatAnthropicWithTools(ctx context.Context, system string, msgs []ChatMessage, tools []ToolSpec) (ChatTurn, error) {
	s.mu.RLock()
	apiKey, model := s.apiKey, s.model
	s.mu.RUnlock()
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	apiMsgs := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		var blocks []map[string]any
		if m.Text != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": m.Text})
		}
		for _, tc := range m.ToolCalls {
			var input any
			_ = json.Unmarshal(tc.Input, &input)
			blocks = append(blocks, map[string]any{
				"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": input,
			})
		}
		for _, tr := range m.ToolResults {
			blocks = append(blocks, map[string]any{
				"type": "tool_result", "tool_use_id": tr.CallID,
				"content": tr.Content, "is_error": tr.IsError,
			})
		}
		apiMsgs = append(apiMsgs, map[string]any{"role": m.Role, "content": blocks})
	}

	apiTools := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		apiTools = append(apiTools, map[string]any{
			"name": t.Name, "description": t.Description, "input_schema": t.InputSchema,
		})
	}

	body := map[string]any{
		"model": model, "max_tokens": 1500, "system": system,
		"messages": apiMsgs, "tools": apiTools,
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.anthropic.com/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return ChatTurn{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Anthropic-Version", "2023-06-01")

	resp, err := s.cli.Do(req)
	if err != nil {
		return ChatTurn{}, fmt.Errorf("anthropic chat: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return ChatTurn{}, fmt.Errorf("anthropic %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  uint32 `json:"input_tokens"`
			OutputTokens uint32 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ChatTurn{}, fmt.Errorf("decode anthropic chat: %w", err)
	}
	turn := ChatTurn{InputTokens: parsed.Usage.InputTokens, OutputTokens: parsed.Usage.OutputTokens}
	var text strings.Builder
	for _, c := range parsed.Content {
		switch c.Type {
		case "text":
			text.WriteString(c.Text)
		case "tool_use":
			turn.ToolCalls = append(turn.ToolCalls, ToolCall{ID: c.ID, Name: c.Name, Input: c.Input})
		}
	}
	turn.Text = text.String()
	return turn, nil
}

// ── OpenAI / GitHub tool-calling ────────────────────────────────
// GitHub Copilot speaks the OpenAI chat-completions shape, so the
// two share this encoder. Only the endpoint + auth headers differ
// (handled by openAIChatTransport).

func (s *Service) chatOpenAIWithTools(ctx context.Context, system string, msgs []ChatMessage, tools []ToolSpec) (ChatTurn, error) {
	apiMsgs := []map[string]any{{"role": "system", "content": system}}
	for _, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			tcs := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				tcs = append(tcs, map[string]any{
					"id": tc.ID, "type": "function",
					"function": map[string]any{"name": tc.Name, "arguments": string(tc.Input)},
				})
			}
			apiMsgs = append(apiMsgs, map[string]any{
				"role": "assistant", "content": m.Text, "tool_calls": tcs,
			})
			continue
		}
		if len(m.ToolResults) > 0 {
			// OpenAI: each tool result is its own role:tool message.
			for _, tr := range m.ToolResults {
				apiMsgs = append(apiMsgs, map[string]any{
					"role": "tool", "tool_call_id": tr.CallID, "content": tr.Content,
				})
			}
			continue
		}
		apiMsgs = append(apiMsgs, map[string]any{"role": m.Role, "content": m.Text})
	}

	apiTools := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		apiTools = append(apiTools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.InputSchema,
			},
		})
	}

	url, hdrs, model, err := s.openAIChatTransport(ctx)
	if err != nil {
		return ChatTurn{}, err
	}
	body := map[string]any{
		"model": model, "max_tokens": 1500, "temperature": 0.2,
		"messages": apiMsgs, "tools": apiTools,
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return ChatTurn{}, err
	}
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	resp, err := s.cli.Do(req)
	if err != nil {
		return ChatTurn{}, fmt.Errorf("openai-compat chat: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return ChatTurn{}, fmt.Errorf("openai-compat %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     uint32 `json:"prompt_tokens"`
			CompletionTokens uint32 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ChatTurn{}, fmt.Errorf("decode openai-compat chat: %w", err)
	}
	turn := ChatTurn{InputTokens: parsed.Usage.PromptTokens, OutputTokens: parsed.Usage.CompletionTokens}
	if len(parsed.Choices) == 0 {
		return turn, errors.New("openai-compat: empty response")
	}
	msg := parsed.Choices[0].Message
	turn.Text = msg.Content
	for _, tc := range msg.ToolCalls {
		args := tc.Function.Arguments
		if args == "" {
			args = "{}"
		}
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{
			ID: tc.ID, Name: tc.Function.Name, Input: json.RawMessage(args),
		})
	}
	return turn, nil
}

// openAIChatTransport resolves endpoint + headers + model for the
// OpenAI-compat path, branching real-OpenAI vs GitHub-Copilot
// (which needs the session-token exchange + integration headers).
func (s *Service) openAIChatTransport(ctx context.Context) (url string, hdrs map[string]string, model string, err error) {
	s.mu.RLock()
	provider, apiKey, base, m := s.provider, s.apiKey, s.baseURL, s.model
	s.mu.RUnlock()
	if provider == ProviderGitHub {
		sessTok, terr := s.githubSessionToken(ctx)
		if terr != nil {
			return "", nil, "", terr
		}
		if m == "" {
			m = "gpt-4o"
		}
		return "https://api.githubcopilot.com/chat/completions", map[string]string{
			"Content-Type":            "application/json",
			"Authorization":           "Bearer " + sessTok,
			"Editor-Version":          "vscode/1.85.0",
			"Editor-Plugin-Version":   "copilot-chat/0.12.0",
			"Copilot-Integration-Id":  "vscode-chat",
			"User-Agent":              "GithubCopilot/1.155.0",
		}, m, nil
	}
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if m == "" {
		m = "gpt-4o-mini"
	}
	h := map[string]string{"Content-Type": "application/json"}
	if apiKey != "" {
		h["Authorization"] = "Bearer " + apiKey
	}
	return strings.TrimRight(base, "/") + "/chat/completions", h, m, nil
}
