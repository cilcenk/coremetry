package api

// trace_explain_unified.go — v0.10.453 (operatör-bildirimli, prod ekranı
// 2026-09-06): sohbetteki "Bu trace'i açıkla" ile ✨ Explain trace FARKLI
// cevap üretiyordu. İkisi de aynı kanıtı (buildTraceExplainInput) ve aynı
// sistem prompt'unu (SystemPromptTrace) kullanıyordu ama sohbet yolu kanıtı
// soru + sohbet geçmişi + hitap sarmalayıcısına (guidedNarrationUser,
// withAddressee) sokup "chat-guided" yüzeyinde, explain ÖNBELLEĞİNE
// uğramadan anlatıyordu → daha kısa, sohbet biçimli, her seferinde farklı
// bir özet. Operatör: "Explain trace daha doğru; tüm trace açıklamaları o
// şekilde olmalı, ister kullanıcı sorsun ister Explain'e bassın."
//
// Şimdi trace_by_id rotası Explain'in çekirdeğini BİREBİR çağırır:
//   • sistem = SystemPromptTrace(), kullanıcı = in.User (sarmalayıcı yok),
//   • önbellek anahtarı = explainCacheKey(SystemPromptTrace(), in.User, "")
//     — copilotExplainTrace ile AYNI formül: düğme ve sohbet aynı cevabı
//     paylaşır (biri üretti, diğeri isabet eder; ai_calls satırı üretende),
//   • yüzey "explain-trace:chat" (/ai ayrı sayar; evalset Trace ailesi),
//   • "Kaynak:" alt satırı yok (Explain'de de yok), evidenceSpanIds olayda.
// Trace'e dair AÇIK bir soru ("bu trace neden yavaş", "hangi span hatalı")
// aynı kanıt ve aynı sistem prompt'uyla ama soru eklenerek cevaplanır
// (önbelleğe girmez — soruya özel). Trace bulunamazsa eski dürüst yol
// (guidedTraceBundle "BULUNAMADI" kanıtı) aynen koşar.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/copilot"
)

// isTraceExplainAsk — SAF: soru "açıkla/özetle/anlat" şekilli mi ya da
// yalnız kimlik mi (yapıştırılan id, çip). Açık bir soru şekli varsa
// (neden/yavaş/hata/hangi…) odaklı varyant.
func isTraceExplainAsk(question string) bool {
	norm := normalizeGuidedMsg(question)
	toks := guidedTokens(norm)
	rest := make([]string, 0, len(toks))
	for _, t := range toks {
		if extractTraceID(t) != "" || t == "trace" || t == "trace'i" || t == "bu" || t == "şu" || t == "this" || t == "the" {
			continue
		}
		rest = append(rest, t)
	}
	if len(rest) == 0 {
		return true // yalnız kimlik / "bu trace"
	}
	if tokenHasPrefix(rest, "açıkla", "acikla", "özet", "ozet", "anlat", "explain", "summar", "describe", "yorumla", "incele", "analiz") {
		return true
	}
	if hasWhySignal(rest) || hasErrorSignal(rest) || hasSlowTraceSignal(norm) || tokenHasPrefix(rest, "hangi", "which", "nerede", "where", "kaç", "kac", "how") {
		return false
	}
	return len(rest) <= 2 // "trace nasıl" gibi kısa/genel sorular = açıkla
}

// guidedTraceExplain — trace_by_id için Explain çekirdeği. handled=false:
// trace bulunamadı ya da girdi kurulamadı → çağıran eski kanıt yoluna düşer.
func (s *Server) guidedTraceExplain(ctx context.Context, emit func(string, any), route guidedRoute, question string, from, to time.Time, ctxService string) (handled, ok bool) {
	n := emitGuidedStep(emit, "trace", `{"id":"`+route.TraceID+`"}`)
	in, err := s.buildTraceExplainInput(ctx, route.TraceID)
	if err != nil {
		if errors.Is(err, errExplainTraceNotFound) {
			emitGuidedStepResult(emit, n, "trace", "bulunamadı", nil)
		} else {
			emitGuidedStepResult(emit, n, "trace", "", err)
		}
		return false, false
	}
	emitGuidedStepResult(emit, n, "trace", in.User, nil)
	system, user := copilot.SystemPromptTrace(), in.User
	cacheKey := ""
	if isTraceExplainAsk(question) {
		cacheKey = explainCacheKey(system, user, "") // copilotExplainTrace ile AYNI anahtar
	} else {
		user = in.User + "\n\nOperatörün sorusu (yalnız yukarıdaki kanıta dayanarak, kanıtta olmayanı söylemeden cevapla): " + strings.TrimSpace(question)
	}
	xid := copilot.MetaFromContext(ctx).ExchangeID
	var text string
	cached := false
	if env, hit := s.explainCacheGetCtx(ctx, cacheKey); hit {
		text, cached, xid = env.Text, true, env.Xid
		emit("delta", map[string]string{"text": text})
	} else {
		raw, exErr := s.copilotStreamSurface(ctx, "explain-trace:chat", system, user, func(delta string) {
			emit("delta", map[string]string{"text": delta})
		})
		if exErr != nil {
			emit("error", map[string]string{"error": exErr.Error()})
			return true, false
		}
		text = strings.TrimSpace(raw)
		s.explainCacheSet(ctx, cacheKey, text, xid)
	}
	links := guidedAnswerLinks(route, linkWindowBetween(from, to))
	links = append(links, s.answerRequestIDLinks(ctx, text, ctxService)...)
	ans := map[string]any{
		"text": text, "exchangeId": xid,
		"suggestions":     guidedSuggestions(route),
		"links":           dedupLinksByHref(links),
		"evidenceSpanIds": in.Evidence,
	}
	if cached {
		ans["cached"] = true
	}
	emit("answer", ans)
	return true, true
}
