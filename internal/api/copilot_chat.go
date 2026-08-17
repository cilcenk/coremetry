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
// telemetry. The LLM's function-calling backend is the SAME tool set
// the MCP server exposes (mcptools.ToolList — TEK kayıt defteri;
// güncel sayı ve katalog orada yaşar, burada sayı tutmuyoruz: iki kez
// bayatladı v0.9.1141'e gelene dek) — so the chat can read live data
// without any new query plumbing. ToolList is the single source; this
// comment stopped counting so it can't drift again.
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
// component state and sends it whole each turn; BU handler hiçbir şey
// kalıcılaştırmaz — konuşma kalıcılığı v0.9.1139'dan beri AYRI uçta
// (ai_conversations.go, istemci-güdümlü upsert). Auth: any
// authenticated user — tool'lar read-only + MinRole süzgeçli
// (v0.9.1136), so a viewer chatting is safe.

const (
	chatMaxToolRounds = 5  // guardrail: cap the agentic loop so a model can't fan tool calls forever
	chatMaxMessages   = 40 // cap conversation length fed back to the LLM (token budget)
)


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
		// Explain (v0.9.479) — AI çekmecesindeki sohbetin bağlam devri:
		// operatörün AZ ÖNCE OKUDUĞU açıklamanın metni (+ kanıt id'leri).
		// Boşken bu dosyadaki her yol bayt-bayt eski davranıştadır; dolu
		// olduğunda guided/explain-grounded ayrımını copilot_drawer.go
		// yönetir (kök neden + tasarım orada yazılı).
		Explain string `json:"explain,omitempty"`
		// Subject (v0.9.482) — çekmecenin öznesi, frontend'in `?ai=` kodeği
		// biçiminde ("trace:<id>", "span:<trace>:<span>", "exception:<fp>").
		// Sunucu bundan İLGİLİ EXPLAIN'İN KANIT PAKETİNİ yeniden kurar
		// (copilot_drawer.go): açıklamanın metni takip sorularına yetmiyordu
		// — "logda ne yazıyor" kör cevaplanıyordu (operatör raporu).
		// Boşken v0.9.479 davranışı bayt-bayt korunur.
		Subject string `json:"subject,omitempty"`
		// RangeS (v0.9.529) — operatörün EKRANDAKİ zaman aralığı,
		// saniye. Soru AÇIK bir pencere taşımıyorsa guided router sabit
		// 30dk yerine bunu kullanır: 6 saatlik pencereye bakarken "hata
		// oranı ne" diye soran operatör, baktığından BAŞKA bir pencerenin
		// cevabını alıyordu ve fark görünmüyordu. Açık pencere taşıyan
		// soru bunu EZER. 0/absent = eski istemci, davranış değişmez.
		RangeS int64 `json:"rangeS,omitempty"`
		// Trace (v0.9.537) — operatörün EKRANDA baktığı trace'in ID'si
		// (/trace?id=). "bu trace neden yavaş" gibi ID'siz sorular
		// bununla çözülür; mesajda açık 32-hex varsa o kazanır.
		Trace string `json:"trace,omitempty"`
	} `json:"context,omitempty"`
}

// copilotChat is the SSE chat endpoint. Runs the agentic loop and
// streams progress + the final answer. One ai_calls row is written
// per exchange (summing per-round token usage) via RecordUsage.
func (s *Server) copilotChat(w http.ResponseWriter, r *http.Request) {
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
	// v0.9.528 Faz 2 — model kiminle konuştuğunu bilsin: ad (hitap için)
	// ve rol (viewer'a yapamayacağı eylemi ÖNERMEMESİ için). Ad çözümü
	// /api/auth/me'nin 30s cache'ini kullanır, yani sohbet başına yeni
	// bir FINAL satır okuması EKLEMEZ. Çözülemezse ön-söz boş kalır ve
	// iki prompt da bayt-bayt eskisi olur.
	addressee := s.chatAddressee(r.Context(), c)
	ctx = ctxWithAddressee(ctx, addressee)

	// v0.8.397 (AI audit A3) — guided mode first, for EVERY provider:
	// a deterministic intent router recognises the highest-value
	// question shapes, the server prefetches the data, and the model
	// makes exactly ONE tool-less narration call (copilot_guided.go).
	// Deterministic beats tool-roulette on these shapes even for
	// frontier models; the 2B-class primary target (qwen3.5-2b) can't
	// drive the 5-round × 11-schema loop reliably at all. No match →
	// the free tool loop below runs UNCHANGED.
	if handled, gok := s.copilotChatGuided(ctx, emit, req.Messages, req.Context.Service, req.Context.Operation, req.Context.Explain, req.Context.RangeS, req.Context.Trace); handled {
		emit("done", map[string]bool{"ok": gok})
		return
	}

	// v0.9.479 — AI çekmecesi sohbeti: ekrandaki açıklama bağlam olarak
	// geldiyse (context.explain) ve guided somut bir özneye oturmadıysa,
	// cevabı tek narration çağrısıyla O AÇIKLAMAYA dayandır. Sıra
	// bilinçli: guided (canlı telemetri, somut özne) > çekmece bağlamı >
	// dokümanlar > serbest döngü. Çekmece sohbeti özne-kapsamlıdır;
	// filo/doküman soruları global CoSRE penceresinde kalır.
	// (ai_calls satırını guided'da olduğu gibi tek narration çağrısı
	// KENDİSİ yazar — burada ikinci bir RecordUsage yok.)
	// v0.9.482 — özne (context.subject) doluysa aynı yol ilgili explain'in
	// HAM KANITINI da yeniden kurup anlatıma katar; kanıt çekilemezse
	// v0.9.479'un metin-tabanlı anlatımı aynen sürer (soft-fail).
	if handled, dok := s.copilotChatDrawer(ctx, emit, req.Messages, req.Context.Explain, req.Context.Subject, req.Context.Service); handled {
		emit("done", map[string]bool{"ok": dok})
		return
	}

	// v0.8.438 — doküman RAG yolu: guided telemetri router'ı
	// eşleşmediyse ve soru yüklü dokümanlara yeterince benziyorsa
	// (skor tabanı) tek narration çağrısıyla kaynak atıflı cevap.
	// Sıra bilinçli: telemetri şekilleri > dokümanlar > serbest döngü.
	if handled, rok := s.ragChatAnswer(ctx, emit, req.Messages, req.Context.Service); handled {
		emit("done", map[string]bool{"ok": rok})
		return
	}

	// Build the tool set once (closures over the live store + logs)
	// and the LLM-facing specs from the same list.
	//
	// v0.9.1136 (AI Faz 3.1) — rol filtresi: MinRole'ü çağıranın
	// rolünü AŞAN tool ne LİSTELENİR ne de ÇAĞRILABİLİR. Gizlemek
	// reddetmekten iyidir (reddedilen tool bir tur + bağlam harcar),
	// ama yalnız gizlemek yetmez: serbest döngü ismi byName'den
	// çözüyor, yani filtre İKİ map'e de uygulanır — model adı
	// tahmin etse bile "unknown tool" alır.
	role := ""
	if c != nil {
		role = c.Role
	}
	tools := toolsForRole(mcptools.ToolList(s.mcpDeps()), role)
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
	// v0.9.528 Faz 2 — serbest döngünün sistem prompt'u da kiminle
	// konuşulduğunu taşır. Ön-söz boşsa sabitin aynısı.
	loopPrompt := withAddressee(addressee, copilot.SystemPromptChat())

	for round := 0; round < chatMaxToolRounds; round++ {
		turn, err := s.copilot.ChatWithTools(ctx, loopPrompt, conv, specs)
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
			// v0.9.709 (operatör-bildirimi) — cevaptaki request_id'ler log
			// köprüsü çipi olur; altyapı (links + ChatBubble çipleri)
			// v0.9.419'dan beri hazırdı, yalnız guided yayınlıyordu.
			emit("answer", map[string]any{"text": finalText, "exchangeId": exchangeID,
				"links": s.answerRequestIDLinks(ctx, finalText, req.Context.Service)})
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
			turn2, err2 := s.copilot.ChatWithTools(ctx, loopPrompt+
				"\n\nYou have reached the tool-call limit. Answer now with what you have.", conv, nil)
			totalIn += turn2.InputTokens
			totalOut += turn2.OutputTokens
			if err2 != nil {
				lastErr = err2
				emit("error", map[string]string{"error": err2.Error()})
			} else {
				finalText = appendCharts(turn2.Text)
				// v0.9.709 (operatör-bildirimi) — cevaptaki request_id'ler log
			// köprüsü çipi olur; altyapı (links + ChatBubble çipleri)
			// v0.9.419'dan beri hazırdı, yalnız guided yayınlıyordu.
			emit("answer", map[string]any{"text": finalText, "exchangeId": exchangeID,
				"links": s.answerRequestIDLinks(ctx, finalText, req.Context.Service)})
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
