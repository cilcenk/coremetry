package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// openai.go — OpenAI uyumlu /chat/completions gövdesi.
//
// "openai" sağlayıcısı Coremetry'de TÜM OpenAI-compat uçları demek:
// gerçek OpenAI, vLLM, KServe, Ollama, LM Studio. Gövde ve header
// şekli eski copilot.go:explainOpenAIWithUsage'ın birebir aynısıdır —
// amaç davranış değiştirmek değil, TAŞIMAK (o üretici Faz 1.2'de
// silindi; bu dosya tek yazılış).

const (
	// defaultBaseURL — operatör uç vermediyse. baseURL /v1 önekini
	// İÇERİR; üstüne /chat/completions eklenir.
	defaultBaseURL = "https://api.openai.com/v1"
	// defaultModel — copilot.go'nun aynı varsayılanı; operatör
	// tipik olarak uç-başına ezer (llama3.1, qwen2.5-coder, …).
	defaultModel = "gpt-4o-mini"
	// defaultMaxTokens — çağıran bütçe vermediyse. copilot'un
	// defaultMaxTokens'ıyla aynı 4096: reasoning modelleri cevabı
	// üretmeden önce düşünme fazında token yakıyor, 1024'te sık sık
	// düşüncenin ortasında bitiyorlardı (v0.8.138 → v0.9.1120).
	defaultMaxTokens = 4096
	// maxRespBytes — yanıt gövdesi okuma tavanı (copilot.go ile aynı).
	maxRespBytes = 1 << 20
)

// DoOpenAI tek bir buffered (stream'siz) chat.completion çağrısı
// yapar ve kurtarma zincirinden geçmiş metni döndürür.
//
// Hata semantiği copilot.go'daki ikiziyle aynı tutulmuştur:
// taşıma hatası "openai-compat call: %w", ≥300 yanıtı
// "openai-compat %d: <gövde>" (HTTPError tipiyle, statü ayıklanabilir).
// Kota kesicisi (429 → 1h pencere) ve JSON merdiveni ÇAĞIRANDA kalır:
// transport döner, Service karar verir.
func DoOpenAI(ctx context.Context, cfg Config, req Request) (Response, error) {
	if cfg.HTTPClient == nil {
		return Response{}, errors.New("provider: nil HTTPClient — timeout ve TLS-skip ayarları onun içinde yaşıyor")
	}
	rf, err := responseFormat(req)
	if err != nil {
		return Response{}, err
	}

	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	model := req.Model
	if model == "" {
		model = cfg.Model
	}
	if model == "" {
		model = defaultModel
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	url := strings.TrimRight(base, "/") + "/chat/completions"
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
	// v0.9.517/527 — katı JSON isteyen yüzeylerde çözümlemeyi SUNUCUDA
	// kısıtla. Basamağı Service seçti (yetenek kararları onda); burada
	// yalnız seçilen basamağın gövde şekli basılır.
	if rf != nil {
		body["response_format"] = rf
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		// Bazı self-hosted geçitler (KServe/route auth arkasındaki
		// vLLM, Azure-tarzı proxy'ler) Bearer yerine çıplak `api-key`
		// header'ıyla doğruluyor (v0.8.384, operatörün air-gapped test
		// LLM'i). İkisini birden yollamak zararsız — sunucu bildiğini
		// okur.
		httpReq.Header.Set("api-key", cfg.APIKey)
	}

	resp, err := cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("openai-compat call: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
	if resp.StatusCode >= 300 {
		return Response{}, &HTTPError{Provider: labelOpenAI, Status: resp.StatusCode, Body: strings.TrimSpace(string(respBody))}
	}
	return ParseOpenAIChat(respBody)
}

// responseFormat, istenen JSON basamağının gövde parçasını üretir.
// nil = alan hiç gönderilmez (JSONPlain).
//
// Saf + tablo testli: merdivenin ÜÇ basamağının tel-üstü şekli
// v0.9.517/527'den beri sabit ve iki yüzey (nl-to-query, rootcause
// verdict) şemanın `strict:true` ile gitmesine güveniyor.
func responseFormat(req Request) (map[string]any, error) {
	switch req.JSONLevel {
	case JSONPlain:
		return nil, nil
	case JSONObject:
		return map[string]any{"type": "json_object"}, nil
	case JSONSchema:
		if req.JSONSchemaName == "" || len(req.JSONSchema) == 0 {
			// Şemasız json_schema = kesin 400 + gereksiz yetenek kararı.
			// Çağıran (Service) bu durumu bir alt basamağa indirmekle
			// yükümlü; buraya gelmesi programlama hatasıdır.
			return nil, errors.New("provider: JSONSchema basamağı ad+şema ister (şemasız istek bir alt basamağa indirilmeliydi)")
		}
		return map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   req.JSONSchemaName,
				"schema": req.JSONSchema,
				"strict": true,
			},
		}, nil
	}
	return nil, fmt.Errorf("provider: bilinmeyen JSONLevel=%d", req.JSONLevel)
}

// ParseOpenAIChat, buffered bir chat.completion gövdesini çözer ve
// kurtarma zincirini uygular. Saf: ağ yok, durum yok — tablo testli.
func ParseOpenAIChat(respBody []byte) (Response, error) {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"` // deepseek-r1 / Qwen3
				Reasoning        string `json:"reasoning"`         // vLLM ≥0.24
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Response{}, fmt.Errorf("decode openai-compat response: %w", err)
	}
	usage := Response{
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
	}
	if len(parsed.Choices) == 0 {
		return usage, errors.New("openai-compat: empty response")
	}
	msg := parsed.Choices[0].Message
	out := SalvageAnswer(msg.Content, msg.ReasoningContent, msg.Reasoning)
	if out == "" {
		// Kurtarılacak hiçbir şey yok. Ham şekli logla ki operatör
		// modelinin gerçekte ne döndürdüğünü görebilsin (yanlış model
		// adı/uç, standart-dışı şema, hiç content üretmeyen model).
		//
		// Önek bilerek "[copilot]": EmptyAnswerError metni operatöre
		// "[copilot] pod log"a bakmasını söylüyor ve o sözleşme bu
		// paketten eski. Önek DEĞİŞMEZ — hata metni onu adres olarak
		// veriyor, log satırı başka bir önekle çıksa adres yalan olurdu.
		log.Printf("[copilot] openai-compat empty answer: finish_reason=%q content_len=%d reasoning_content_len=%d reasoning_len=%d raw=%.500s",
			parsed.Choices[0].FinishReason, len(msg.Content), len(msg.ReasoningContent), len(msg.Reasoning),
			strings.TrimSpace(string(respBody)))
		return usage, EmptyAnswerError(parsed.Choices[0].FinishReason)
	}
	usage.Text = out
	return usage, nil
}
