package api

import (
	"context"
	"fmt"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/mcp"
)

// MCP çağrı kapısı — iki soru, bu SIRAYLA: (1) rol yetiyor mu,
// (2) rate bütçesi var mı.
//
// Rate limiti (v0.9.14, audit §3): kimlik başına sabit pencere —
// subRateBy'ın (api.go:174, IP-anahtarlı) map+mutex deseni, anahtar
// KİMLİK (Claims.UserID: cmk_ token id'si ya da kullanıcı) — IP
// değil: tek NAT arkasındaki iki ajan birbirini boğmasın. 60 çağrı/dk
// bir Claude Code oturumunun agresif araştırmasını rahat taşır
// (list/get/search zaten clampLimit'li + MV-öncelikli), kaçak
// tool-loop'unu keser.
//
// Rol kontrolü (v0.9.1136, AI Faz 3.1): mcp.Tool/Resource/Prompt
// MinRole taşır, kapı Claims.Role ile karşılaştırır. Sıra ÖNEMLİ ve
// testli: rol reddi rate bütçesini TÜKETMEZ. Aksi halde yetkisiz bir
// istemci, hiç veri alamadığı çağrılarla kendi meşru çağrılarının
// bütçesini yakar (ve dahası: bir dış istemci 60 redle bütçeyi
// bitirip kendini kilitler, sebebi görünmez).
//
// Kapı artık tools/call + resources/read + prompts/get'i kapsıyor
// (v0.9.1136'ya dek yalnız tools/call'daydı; resource/prompt yolları
// aynı chstore okumalarını kapısız yapıyordu). initialize/ping/*-list
// hâlâ kapı dışı: veri okumazlar.
const (
	mcpRateWindow = time.Minute
	mcpRateLimit  = 60
)

type mcpRateBucket struct {
	windowStart int64 // unix saniye, pencere başı
	count       int
}

// roleRank — admin > editor > viewer. Bilinmeyen rol (operatörün
// tanımladığı custom rol dâhil) viewer'ın ÜSTÜNE çıkamaz: custom
// roller tanım gereği viewer'ın alt kümesidir (auth.go:77-81), yani
// rank 0 doğru yöndür — MinRole "" olan okumaları geçerler, editor
// eşiğini geçemezler. Boş rol -1: kimliksiz istek hiçbir kapıyı
// geçmez.
func roleRank(role string) int {
	switch role {
	case auth.RoleAdmin:
		return 2
	case auth.RoleEditor:
		return 1
	case auth.RoleViewer:
		return 0
	case "":
		return -1
	default:
		return 0 // custom rol = viewer alt kümesi
	}
}

// roleSatisfies — saf karar fonksiyonu (table-testli). minRole "" =
// viewer tabanı: kimliği doğrulanmış HERKES geçer, kimliksiz geçmez.
//
// TANINMAYAN minRole (kayıt defterinde yazım hatası, ör. "editr")
// KAPALI yönde çözülür: admin bile geçmez. Fail-open yönü seçilseydi
// bir harf hatası kapıyı sessizce kaldırırdı (feedback-fail-open-
// silently-unapplies); kapalı yön gürültülüdür — hata anında görünür.
func roleSatisfies(role, minRole string) bool {
	if roleRank(role) < 0 {
		return false
	}
	if minRole == "" {
		return true
	}
	if !auth.IsValidRole(minRole) {
		return false
	}
	return roleRank(role) >= roleRank(minRole)
}

// toolsForRole — in-app sohbetin (copilot_chat.go) tool kümesini role
// göre süzer: MinRole'ü aşan tool modele HİÇ gösterilmez. Saf ve
// property-testli; kapının kendisiyle AYNI karar fonksiyonunu
// (roleSatisfies) kullanır — iki yolun ayrışması sapma bug'ı olurdu.
//
// Not: guided router (copilot_guided.go) tool katmanını bilerek
// ATLAR — veriyi kendisi prefetch eder. MinRole > "" olan bir tool
// eklendiğinde onun guided ikizine de aynı rol kontrolü GEREKİR;
// bugün tüm tool'lar "" olduğu için açık bir delik yok.
func toolsForRole(tools []mcp.Tool, role string) []mcp.Tool {
	out := make([]mcp.Tool, 0, len(tools))
	for _, t := range tools {
		if roleSatisfies(role, t.MinRole) {
			out = append(out, t)
		}
	}
	return out
}

// mcpCallGate — mcp.CallGate implementasyonu. Hata metni LLM'e sonuç
// olarak gider: rol reddi gereken rolü ADIYLA söyler (model "bu
// yetkiyle yapılamaz" diyebilsin, boşuna yeniden denemesin), rate
// reddi bekleme süresini söyler.
func (s *Server) mcpCallGate(ctx context.Context, c mcp.GateCall) error {
	role, id := "", "anonymous"
	if claims := auth.FromContext(ctx); claims != nil {
		role = claims.Role
		if claims.UserID != "" {
			id = claims.UserID
		}
	}
	return s.mcpGateDecide(role, id, c, time.Now())
}

// mcpGateDecide — kapının kararı, context'ten AYRIŞTIRILMIŞ hali
// (rol + kimlik + saat parametre). auth.Middleware kurmadan test
// edilebilir olması şart: "rol reddi rate bütçesi TÜKETMEZ" sırası
// ancak burada mutasyonla kanıtlanıyor.
func (s *Server) mcpGateDecide(role, id string, c mcp.GateCall, at time.Time) error {
	// (1) ROL — rate penceresine DOKUNMADAN önce. Sıra sözleşmedir:
	// reddedilen çağrı bütçe yakarsa yetkisiz bir istemci 60 redde
	// kendini kilitler (ve sebebi "rate limited" diye görünür).
	if !roleSatisfies(role, c.MinRole) {
		switch {
		case roleRank(role) < 0:
			return mcp.Denied("%s %q: authentication required (no session/token on this call)", c.Kind, c.Name)
		case !auth.IsValidRole(c.MinRole):
			return mcp.Denied("%s %q declares an unrecognized MinRole %q — Coremetry registry misconfiguration, refusing; report this to the operator",
				c.Kind, c.Name, c.MinRole)
		default:
			return mcp.Denied("%s %q requires the %q role or higher; this identity is %q — do not retry, ask the operator for a token with that role",
				c.Kind, c.Name, c.MinRole, role)
		}
	}

	// (2) RATE.
	now := at.Unix()
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
		return fmt.Errorf("rate limited: %d calls/min per identity — retry in %ds (%s %q)",
			mcpRateLimit, retry, c.Kind, c.Name)
	}
	b.count++
	return nil
}
