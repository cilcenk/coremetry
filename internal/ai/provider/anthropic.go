package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// anthropic.go — Anthropic Messages API gövdesi (buffered).
//
// FAZ 1.2 taşıması: copilot.go:explainAnthropicWithUsage'ın birebir
// aynısı. İki şey bilinçli olarak SABİT:
//
//   - Uç. Config.BaseURL bu yolda OKUNMAZ; Anthropic'in tek bir API
//     host'u var ve operatörün baseURL alanı openai-compat uçları için
//     var. Sessizce baseURL'e uymak, "openai" seçiliyken girilmiş bayat
//     bir uca anahtar göndermek demekti (Configure'un baseURL'i yalnız
//     openai için okuması aynı kararın öteki yarısı).
//   - JSON basamağı yok. Bu yola bugüne kadar hiç response_format
//     gönderilmedi (Anthropic'in yazılışı zaten farklı: tool-forcing).
//     Taşıma davranış değiştirmez — JSONPlain dışındaki basamak AÇIK
//     hatayla reddedilir ki çağıran "uyguladım" sanmasın. Service, bu
//     sağlayıcıda basamağı bugün olduğu gibi düşürüyor.
const (
	anthropicURL = "https://api.anthropic.com/v1/messages"
	// anthropicVersion — zorunlu sürüm header'ı. Eksikse API 400 döner;
	// yani bu sabit düşerse ANTHROPIC YOLU TAMAMEN kırılır.
	anthropicVersion = "2023-06-01"
	// defaultAnthropicModel — copilot.go'nun aynı varsayılanı.
	defaultAnthropicModel = "claude-sonnet-4-6"
)

// DoAnthropic tek bir buffered Messages çağrısı yapar.
//
// max_tokens ve temperature Request'ten gelir. Bu, v0.9.1120'nin
// düzelttiği hatanın taşınmış hâli: bu yol ~1000 sürüm boyunca sabit
// 1024 ve HİÇ temperature göndermişti (openai-compat 4096 alırken).
// Değerlerin sahibi Service; buranın işi onları gövdeye basmak.
func DoAnthropic(ctx context.Context, cfg Config, req Request) (Response, error) {
	if cfg.HTTPClient == nil {
		return Response{}, errors.New("provider: nil HTTPClient — timeout ve TLS-skip ayarları onun içinde yaşıyor")
	}
	if req.JSONLevel != JSONPlain {
		return Response{}, fmt.Errorf("provider: anthropic yolunda JSONLevel=%d desteklenmiyor (response_format bu API'de yok — çağıran basamağı düşürmeliydi)", req.JSONLevel)
	}
	model := req.Model
	if model == "" {
		model = cfg.Model
	}
	if model == "" {
		model = defaultAnthropicModel
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"system":     req.System,
		"messages": []map[string]any{
			{"role": "user", "content": req.User},
		},
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicURL, bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", cfg.APIKey)
	httpReq.Header.Set("Anthropic-Version", anthropicVersion)

	resp, err := cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("anthropic call: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
	if resp.StatusCode >= 300 {
		return Response{}, &HTTPError{Provider: labelAnthropic, Status: resp.StatusCode, Body: strings.TrimSpace(string(respBody))}
	}
	return ParseAnthropic(respBody)
}

// ParseAnthropic, buffered bir Messages gövdesini çözer. Saf: ağ yok,
// durum yok. Streaming yolu da bunu çağırıyor — bir vekil stream:true
// bayrağını yutup tek-atış JSON döndürdüğünde, elde OLAN gövdeyi
// çözümlemek ikinci bir faturalı çağrıdan iyidir (v0.8.404).
//
// content[] birden çok text bloğu taşıyabilir (Anthropic uzun yanıtı
// bölebiliyor); text OLMAYAN bloklar (tool_use, thinking) atlanır.
func ParseAnthropic(respBody []byte) (Response, error) {
	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Response{}, fmt.Errorf("decode anthropic response: %w", err)
	}
	var out strings.Builder
	for _, c := range parsed.Content {
		if c.Type == "text" {
			out.WriteString(c.Text)
		}
	}
	// Not: boş metin burada HATA DEĞİL — openai-compat'ın kurtarma
	// zincirinin (EmptyAnswerError) anthropic karşılığı yok ve olmadı.
	// Bu, taşınan davranış; değiştirmek ayrı bir ürün kararı olurdu.
	return Response{
		Text:         out.String(),
		InputTokens:  parsed.Usage.InputTokens,
		OutputTokens: parsed.Usage.OutputTokens,
	}, nil
}
