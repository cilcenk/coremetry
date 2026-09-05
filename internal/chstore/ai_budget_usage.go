package chstore

// ai_budget_usage.go — v0.10.411 (CoSRE denetimi E8): bütçe rozeti için
// SON 24 SAAT kullanımı. /api/ai/stats seçici pencereyi ölçer; günlük
// tavan sabit pencere ister, yoksa 7g/30g penceresi günlük tavanla
// kıyaslanırdı. Dolar HESAPLANMAZ — fiyat tablosu istemcide
// (lib/ai-rates.ts); model başına token döner, istemci fiyatlar.

import (
	"context"
	"time"
)

// AIModelTokens — bir sağlayıcı/model çiftinin pencere içi token'ları.
type AIModelTokens struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	InputTokens  uint64 `json:"inputTokens"`
	OutputTokens uint64 `json:"outputTokens"`
}

// AIBudgetUsage — bütçe kıyası için pencere toplamları. Sayaçlar uint64
// (count/sum UInt64 döner — v0.9.543 sınıfı).
type AIBudgetUsage struct {
	Calls        uint64          `json:"calls"`
	InputTokens  uint64          `json:"inputTokens"`
	OutputTokens uint64          `json:"outputTokens"`
	P95Ms        float64         `json:"p95Ms"`
	ByModel      []AIModelTokens `json:"byModel"`
}

// ComputeAIBudgetUsage — [from, to) penceresi; ai_calls küçük bir state
// tablosu ama yine zaman sınırı + max_execution_time + LIMIT.
func (s *Store) ComputeAIBudgetUsage(ctx context.Context, from, to time.Time) (AIBudgetUsage, error) {
	var u AIBudgetUsage
	err := s.conn.QueryRow(ctx, `
		SELECT
			toUInt64(count()),
			toUInt64(sum(input_tokens)),
			toUInt64(sum(output_tokens)),
			coalesce(toFloat64(quantile(0.95)(toFloat64(duration_ms))), 0)
		FROM ai_calls
		WHERE created_at >= toDateTime64(?, 9, 'UTC')
		  AND created_at <  toDateTime64(?, 9, 'UTC')
		SETTINGS max_execution_time = 10`,
		chDateTime64Arg(from), chDateTime64Arg(to)).Scan(&u.Calls, &u.InputTokens, &u.OutputTokens, &u.P95Ms)
	if err != nil {
		return u, err
	}
	rows, err := s.conn.Query(ctx, `
		SELECT provider, model, toUInt64(sum(input_tokens)), toUInt64(sum(output_tokens))
		FROM ai_calls
		WHERE created_at >= toDateTime64(?, 9, 'UTC')
		  AND created_at <  toDateTime64(?, 9, 'UTC')
		GROUP BY provider, model
		ORDER BY sum(input_tokens) + sum(output_tokens) DESC
		LIMIT 200
		SETTINGS max_execution_time = 10`,
		chDateTime64Arg(from), chDateTime64Arg(to))
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var m AIModelTokens
		if err := rows.Scan(&m.Provider, &m.Model, &m.InputTokens, &m.OutputTokens); err != nil {
			return u, err
		}
		u.ByModel = append(u.ByModel, m)
	}
	return u, rows.Err()
}
