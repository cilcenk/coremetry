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

// github.go — GitHub Copilot chat/completions gövdesi (buffered).
//
// FAZ 1.2 taşıması: copilot.go:explainGitHubWithUsage'ın birebir
// aynısı. Üç şey bilinçli:
//
//  1. Config.APIKey burada OTURUM JETONUdur, operatörün OAuth jetonu
//     (ghu_…) DEĞİL. Jeton takası (copilot_internal/v2/token → ~25dk
//     ömürlü oturum jetonu) DURUM taşır — önbellek + son kullanma —
//     ve bu paketin kuralı durum tutmamak. Takas copilot.Service'te
//     kalır (githubSessionToken), çözülmüş jeton buraya Config ile
//     gelir.
//  2. Uç sabit. Copilot'un tek bir edge'i var; baseURL okunmaz.
//  3. Yanıt çözümlemesi openai-compat'tan AYRI. Copilot edge'i
//     reasoning_content/reasoning alanları ya da <think> blokları
//     üretmiyor; ParseOpenAIChat'i buraya bağlamak kurtarma zincirini
//     ve BAŞKA bir boş-yanıt hata metnini bu yola sokardı — davranış
//     değişikliği olurdu. Taşıma davranış değiştirmez.
const (
	githubChatURL = "https://api.githubcopilot.com/chat/completions"
	// defaultGitHubModel — copilot.go'nun aynı varsayılanı.
	defaultGitHubModel = "gpt-4o"
)

// DoGitHub tek bir buffered Copilot chat.completion çağrısı yapar.
// cfg.APIKey = ÇÖZÜLMÜŞ oturum jetonu (yukarıdaki (1)).
func DoGitHub(ctx context.Context, cfg Config, req Request) (Response, error) {
	if cfg.HTTPClient == nil {
		return Response{}, errors.New("provider: nil HTTPClient — timeout ve TLS-skip ayarları onun içinde yaşıyor")
	}
	if req.JSONLevel != JSONPlain {
		return Response{}, fmt.Errorf("provider: github yolunda JSONLevel=%d desteklenmiyor (çağıran basamağı düşürmeliydi)", req.JSONLevel)
	}
	model := req.Model
	if model == "" {
		model = cfg.Model
	}
	if model == "" {
		model = defaultGitHubModel
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages": []map[string]any{
			{"role": "system", "content": req.System},
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
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, githubChatURL, bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	// Entegrasyon header'ları: editör kimliği gibi görünüyorlar ama kapı
	// bekçisi — eksik/yanlış olduklarında edge 403 döner. Jeton takası
	// (copilot.Service.githubSessionToken) aynı üçlüyü gönderir; takas
	// DURUM taşıdığı için orada kaldı, bu yüzden üçlü iki yerde yazılı.
	httpReq.Header.Set("Copilot-Integration-Id", "vscode-chat")
	httpReq.Header.Set("Editor-Version", "vscode/1.85.0")
	httpReq.Header.Set("Editor-Plugin-Version", "copilot-chat/0.12.0")
	httpReq.Header.Set("User-Agent", "GithubCopilot/1.155.0")

	resp, err := cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("github copilot call: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
	if resp.StatusCode >= 300 {
		return Response{}, &HTTPError{Provider: labelGitHub, Status: resp.StatusCode, Body: strings.TrimSpace(string(respBody))}
	}
	return ParseGitHubChat(respBody)
}

// ParseGitHubChat, Copilot'un chat.completion gövdesini çözer.
// Kasıtlı olarak ParseOpenAIChat'ten ayrı — yukarıdaki (3).
func ParseGitHubChat(respBody []byte) (Response, error) {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Response{}, fmt.Errorf("decode github copilot response: %w", err)
	}
	usage := Response{
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
	}
	if len(parsed.Choices) == 0 {
		return usage, errors.New("github copilot: empty response")
	}
	usage.Text = parsed.Choices[0].Message.Content
	return usage, nil
}
