package copilot

import (
	"context"

	"github.com/cilcenk/coremetry/internal/ai/provider"
)

// provider_canary.go — FAZ 1.1: internal/ai/provider'ın TEK yüzeyde
// canlıya alınması (docs/plans/ai-assistant-design-2026-08-16.md §4).
//
// Neden canary: 8 istek üreticisini tek transport'a indirmek tüm AI
// yüzeylerini aynı anda riske atar. Tek yüzeyde (explain-charts)
// başlamak, geri dönüşü "şart bloğunu sil"e indirir ve eski kod
// Faz 1.3'e kadar dokunulmadan yaşar.
//
// Bu dosya Faz 1.2'de erir: tüm explain'ler transport'a geçtiğinde
// canaryProvider koşulu ve explainOpenAIWithUsage birlikte gider.

// canarySurface — yeni transport'tan geçen TEK ai_calls yüzeyi.
// aiSurfaceFromPath (internal/api) bu dizeyi /api/copilot/explain-charts
// yolundan üretir.
const canarySurface = "explain-charts"

// canaryProvider — bu çağrı canary yüzeyi mi? Karar CallMeta'dan
// okunur: yüzey kimliği zaten ai_calls'a giden alan, yani "hangi
// yüzey yeni yoldan geçti" sorusunun cevabı /ai sayfasında da
// doğrulanabilir (satır transport'a değil yüzeye bakıyor, ama
// operatör aynı yüzeyin eski/yeni yanıtını kıyaslayabiliyor).
func canaryProvider(ctx context.Context) bool {
	return MetaFromContext(ctx).Surface == canarySurface
}

// explainViaProvider — canary yolu.
//
// SNAPSHOT sözleşmesi: config'in sahibi Service'tir; transport durum
// tutmaz. Tüm alanlar TEK RLock altında okunur — httpClient() /
// tuneMaxTokens() / tuneTemperature() ayrı ayrı çağrılsaydı üç ayrı
// RLock olurdu ve aralarına giren bir Configure (30s config refresh
// ya da Settings PUT) yarı-eski yarı-yeni bir çağrı üretebilirdi.
// Locked yardımcılar tam bunun için var.
//
// Kayıt (recordNarration), kota kesici (noteProviderError) ve hata
// semantiği Explain'de KALIR — transport döner, Service kaydeder.
func (s *Service) explainViaProvider(ctx context.Context, systemPrompt, userPrompt string) (string, uint32, uint32, error) {
	s.mu.RLock()
	cfg := provider.Config{
		BaseURL:    s.baseURL,
		APIKey:     s.apiKey,
		Model:      s.model,
		HTTPClient: s.cli,
	}
	req := provider.Request{
		Model:     s.model,
		MaxTokens: s.tuneMaxTokensLocked(),
		System:    systemPrompt,
		User:      userPrompt,
		JSONLevel: provider.JSONPlain,
	}
	if t, ok := s.tuneTemperatureLocked(); ok {
		temp := t
		req.Temperature = &temp
	}
	s.mu.RUnlock()

	resp, err := provider.DoOpenAI(ctx, cfg, req)
	return resp.Text, clampTokens(resp.InputTokens), clampTokens(resp.OutputTokens), err
}

// clampTokens — provider int taşır (JSON'da sunucu neyi yollarsa),
// ai_calls uint32. Negatif/absürt bir usage değeri sarmalanıp devasa
// bir token sayısına dönmesin diye kelepçelenir.
func clampTokens(n int) uint32 {
	if n < 0 {
		return 0
	}
	const maxU32 = 1<<32 - 1
	if n > maxU32 {
		return maxU32
	}
	return uint32(n)
}
