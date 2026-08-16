package copilot

import (
	"context"
	"errors"
	"time"

	// aiprov — tek transport (bkz. provider_calls.go'daki görev ayrımı).
	aiprov "github.com/cilcenk/coremetry/internal/ai/provider"
)

// In-app chatbot tool-calling layer (v0.6.53). The single-shot
// Explain path can't drive the agentic chat: the chat needs
// multi-turn history AND function-calling so the LLM can pull
// telemetry on demand (the 7 MCP tools become its functions).
//
// ChatWithTools is provider-neutral at the boundary — the API
// handler builds a []ChatMessage + []ToolSpec and gets back a
// ChatTurn (either prose, or a batch of tool calls to execute and
// feed back). On-prem installs run any of the three providers, so all
// three carry the tool-calling path (operator decision 2026-05-28).
//
// FAZ 1.3 (v0.9.1125) — sağlayıcıya özgü TEL KODLAMASI bu dosyadan
// gitti: gövde/uç/header/çözümleme artık internal/ai/provider/tools.go.
// Burada kalan: sağlayıcı dallanması, GitHub oturum jetonu takası
// (DURUM), token kelepçesi ve ai_calls kaydı.

// Sağlayıcı-nötr sohbet sözlüğü PROVIDER'da tanımlı; burada TAKMA AD
// olarak yaşıyor.
//
// Neden takma ad, yeniden tanım değil: bu tipler internal/api ve
// internal/mcptools'ta 20+ yerde `copilot.ChatMessage`,
// `copilot.ToolSpec`, `copilot.ToolResult` diye geçiyor VE
// /api/copilot/chat gövdesinin JSON şeklini taşıyorlar. Takma ad Go'da
// AYNI tiptir: tek satır çağrı yeri değişmedi, tel şekli de değişmedi.
// Yeniden tanım (iki ayrı struct + dönüştürücü) hem o dosyaları
// dolaşırdı hem de tam bu fazın kapatmaya çalıştığı "iki yazılış"
// sınıfını geri açardı.
type (
	ToolSpec    = aiprov.ToolSpec
	ToolCall    = aiprov.ToolCall
	ToolResult  = aiprov.ToolResult
	ChatMessage = aiprov.ChatMessage
)

// ChatTurn is one model response. When ToolCalls is non-empty the
// caller must execute them and loop; otherwise Text is the final
// answer.
//
// Bilinçli olarak provider.ChatResponse'un takma adı DEĞİL: token
// alanları burada uint32 (ai_calls sütun tipi), transport'ta int
// (sunucu ne yollarsa). Dönüşüm clampTokens'tan geçer.
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
	case ProviderGitHub:
		return s.chatGitHubWithTools(ctx, system, msgs, tools)
	case ProviderOpenAI:
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
		ExchangeID:     meta.ExchangeID,
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

// ─── policy shells ──────────────────────────────────────────────────

// chatRequest — snapshot'ı tool'lu istek şekline çevirir. Ayarlar
// (bütçe/temperature/model) buffered explain ile TEK kaynaktan gelir:
// callSnapshot. 1024 paritesi tam da bunun yokluğundan doğmuştu.
func chatRequest(req aiprov.Request, system string, msgs []ChatMessage, tools []ToolSpec) aiprov.ChatRequest {
	return aiprov.ChatRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		System:      system,
		Messages:    msgs,
		Tools:       tools,
	}
}

func chatTurnFrom(r aiprov.ChatResponse) ChatTurn {
	return ChatTurn{
		Text:         r.Text,
		ToolCalls:    r.ToolCalls,
		InputTokens:  clampTokens(r.InputTokens),
		OutputTokens: clampTokens(r.OutputTokens),
	}
}

func (s *Service) chatAnthropicWithTools(ctx context.Context, system string, msgs []ChatMessage, tools []ToolSpec) (ChatTurn, error) {
	cfg, req, _, _, _ := s.callSnapshot()
	resp, err := aiprov.ChatAnthropicTools(ctx, cfg, chatRequest(req, system, msgs, tools))
	return chatTurnFrom(resp), err
}

func (s *Service) chatOpenAIWithTools(ctx context.Context, system string, msgs []ChatMessage, tools []ToolSpec) (ChatTurn, error) {
	cfg, req, _, _, _ := s.callSnapshot()
	resp, err := aiprov.ChatOpenAITools(ctx, cfg, chatRequest(req, system, msgs, tools))
	return chatTurnFrom(resp), err
}

// chatGitHubWithTools — Copilot tool-calling. Gövde openai-compat ile
// aynı; ayrışan tek şey uç + oturum jetonu.
//
// Sıra korunuyor: takas ÖNCE, snapshot SONRA (explainGitHub ile aynı
// gerekçe — takas kendi kilidini alıp jeton yazıyor).
func (s *Service) chatGitHubWithTools(ctx context.Context, system string, msgs []ChatMessage, tools []ToolSpec) (ChatTurn, error) {
	sessTok, err := s.githubSessionToken(ctx)
	if err != nil {
		return ChatTurn{}, err
	}
	cfg, req, _, _, _ := s.callSnapshot()
	cfg.APIKey = sessTok
	resp, err := aiprov.ChatGitHubTools(ctx, cfg, chatRequest(req, system, msgs, tools))
	return chatTurnFrom(resp), err
}
