package api

// ai_budget.go — v0.10.411 (CoSRE denetimi E8): AI maliyet/gecikme
// bütçesi. system_settings("ai.budget") JSON blobu; GET durum (bütçe +
// son 24 saat kullanımı), PUT admin yazımı + audit. Kardeş şablon:
// getAIRates/putAIRates (ai_observability.go). Rotalar burada kayıtlı,
// api.go BÜYÜMEZ (ai_budget_test pinler).
//
// Sıfır = tavan yok. Dolar hesabı istemcide (fiyat tablosu orada);
// sunucu token + p95 verir, rozet kararı saf istemci yardımcısında
// (pages/ai/aiBudgetView.ts) — bilinmeyen model maliyeti null kalır,
// "$0 = bütçe içinde" yalanı yok.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

const aiBudgetKey = "ai.budget"

// aiBudgetWindow — kullanım penceresi: kayan 24 saat (takvim günü
// değil — saat dilimi belirsizliği yok, önbellek anahtarı dakikaya
// yuvarlanır).
const aiBudgetWindow = 24 * time.Hour

// AIBudget — operatör tavanları; 0 = yok.
type AIBudget struct {
	DailyTokens  uint64  `json:"dailyTokens"`
	DailyCostUSD float64 `json:"dailyCostUsd"`
	P95Ms        uint32  `json:"p95Ms"`
}

// AIBudgetStatus — /api/ai/budget yanıtı.
type AIBudgetStatus struct {
	Budget     AIBudget              `json:"budget"`
	Configured bool                  `json:"configured"`
	WindowS    int                   `json:"windowS"`
	Usage      chstore.AIBudgetUsage `json:"usage"`
}

func (b AIBudget) configured() bool {
	return b.DailyTokens > 0 || b.DailyCostUSD > 0 || b.P95Ms > 0
}

// normalizeAIBudget — saf doğrulama: negatif / NaN / sonsuz reddedilir.
func normalizeAIBudget(b AIBudget) (AIBudget, error) {
	if math.IsNaN(b.DailyCostUSD) || math.IsInf(b.DailyCostUSD, 0) || b.DailyCostUSD < 0 {
		return b, errors.New("dailyCostUsd negatif ya da sayı değil")
	}
	if b.DailyCostUSD > 0 && b.DailyCostUSD < 0.01 {
		return b, errors.New("dailyCostUsd en az 0.01 olmalı")
	}
	return b, nil
}

func (s *Server) loadAIBudget(ctx context.Context) (AIBudget, error) {
	var b AIBudget
	raw, err := s.store.GetSetting(ctx, aiBudgetKey)
	if err != nil {
		return b, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &b); err != nil {
			// Bozuk blob — hata değil "bütçe yok"; operatör UI'dan yeniden yazar.
			b = AIBudget{}
		}
	}
	return b, nil
}

// getAIBudget — bütçe + son 24 saat kullanımı. Önbellek anahtarı bütçe
// değerlerini de taşır: PUT sonrası 30 sn bayat tavan servis edilmez.
func (s *Server) getAIBudget(w http.ResponseWriter, r *http.Request) {
	b, err := s.loadAIBudget(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	to := time.Now().UTC()
	from := to.Add(-aiBudgetWindow)
	key := fmt.Sprintf("ai-budget:v411:m=%d:b=%d/%g/%d",
		to.UnixNano()/int64(time.Minute), b.DailyTokens, b.DailyCostUSD, b.P95Ms)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		u, err := s.store.ComputeAIBudgetUsage(ctx, from, to)
		if err != nil {
			return nil, err
		}
		if u.ByModel == nil {
			u.ByModel = []chstore.AIModelTokens{}
		}
		return AIBudgetStatus{Budget: b, Configured: b.configured(), WindowS: int(aiBudgetWindow / time.Second), Usage: u}, nil
	})
}

// putAIBudget — tüm blobu değiştirir; sıfırlar geçerli ("tavan yok").
func (s *Server) putAIBudget(w http.ResponseWriter, r *http.Request) {
	var body AIBudget
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	b, err := normalizeAIBudget(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	raw, _ := json.Marshal(b)
	if err := s.store.PutSetting(r.Context(), aiBudgetKey, raw); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "settings.ai_budget.update", "settings", aiBudgetKey, string(raw))
	writeJSON(w, b)
}

// registerAIBudgetRoutes — ai_routes.go'dan çağrılır.
func (s *Server) registerAIBudgetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/ai/budget", auth.RequireRole(auth.RoleAdmin, s.getAIBudget))
	mux.HandleFunc("PUT /api/ai/budget", auth.RequireRole(auth.RoleAdmin, s.putAIBudget))
}
