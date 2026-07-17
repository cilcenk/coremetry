package api

import (
	"context"
	"fmt"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
)

// MCP tools/call rate limiti (v0.9.14, audit §3): kimlik başına
// sabit pencere — subRateBy'ın (api.go:174, IP-anahtarlı) map+mutex
// deseni, anahtar KİMLİK (Claims.UserID: cmk_ token id'si ya da
// kullanıcı) — IP değil: tek NAT arkasındaki iki ajan birbirini
// boğmasın. 60 çağrı/dk bir Claude Code oturumunun agresif
// araştırmasını rahat taşır (list/get/search zaten clampLimit'li +
// MV-öncelikli), kaçak tool-loop'unu keser. initialize/tools/list
// kapı dışı (mcp.SetToolCallGate yalnız tools/call'da çağrılır).

const (
	mcpRateWindow = time.Minute
	mcpRateLimit  = 60
)

type mcpRateBucket struct {
	windowStart int64 // unix saniye, pencere başı
	count       int
}

// mcpToolGate — mcp.ToolCallGate implementasyonu. Hata metni LLM'e
// tool sonucu olarak gider — bekleme süresini söyler.
func (s *Server) mcpToolGate(ctx context.Context, tool string) error {
	id := "anonymous"
	if c := auth.FromContext(ctx); c != nil && c.UserID != "" {
		id = c.UserID
	}
	now := time.Now().Unix()
	winStart := now - now%int64(mcpRateWindow.Seconds())

	s.mcpRateMu.Lock()
	defer s.mcpRateMu.Unlock()
	if s.mcpRateBy == nil {
		s.mcpRateBy = map[string]*mcpRateBucket{}
	}
	b := s.mcpRateBy[id]
	if b == nil || b.windowStart != winStart {
		// Yeni pencere — eski girdiler fırsatçı temizlenir (map
		// kimlik sayısıyla sınırlı kalır; subRateBy sözleşmesi).
		for k, v := range s.mcpRateBy {
			if v.windowStart != winStart {
				delete(s.mcpRateBy, k)
			}
		}
		b = &mcpRateBucket{windowStart: winStart}
		s.mcpRateBy[id] = b
	}
	if b.count >= mcpRateLimit {
		retry := b.windowStart + int64(mcpRateWindow.Seconds()) - now
		if retry < 1 {
			retry = 1
		}
		return fmt.Errorf("rate limited: %d tool calls/min per identity — retry in %ds (tool %q)",
			mcpRateLimit, retry, tool)
	}
	b.count++
	return nil
}
