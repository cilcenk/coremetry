package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// stream.go — SSE (stream:true) taşıması. FAZ 1.3 taşıması:
// copilot/stream.go'daki streamOpenAIWithUsage + streamAnthropicWithUsage
// gövdeleri, SSE hattı (scanSSE/sseDataPayload) ve iki biriktirici
// buraya taşındı; gövde şekli BİREBİR aynı.
//
// GÖREV AYRIMI — bu dilimin can alıcı yeri:
//
//	provider (burada) → gövde, header, SSE çözümleme, ve yanıt BAŞININ
//	                    SINIFLANDIRMASI (ClassifyStreamResponse, saf).
//	copilot.Service   → KARAR. Geri düşülecek mi, hangi buffered yol
//	                    çağrılacak, karar önbelleğe yazılacak mı, ne
//	                    loglanacak. Hepsi durum.
//
// Bu yüzden geri-düşüş bir HATA TİPİYLE dışarı verilir
// (StreamFallbackError): taşıma "akış olmadı, sebep bu, elimde şu gövde
// var" der; ne yapılacağına Service karar verir. Transport'un kendi
// başına buffered çağrıya düşmesi, kararı (ve kota kesicisini, ve
// "verdict cached" muhasebesini) iki yere bölerdi.

// StreamVerdict — stream:true yoklamasının yanıt BAŞINA verilen karar.
// Yalnız "bu istek şekli kabul edilmiyor" diyen statüler kalıcı karar
// üretir; geçici olan her şey YALNIZ bu çağrı için geri düşer ve bir
// dahakine yeniden yoklanır.
type StreamVerdict int

const (
	// VerdictStream — 200 + text/event-stream: akışı tüket.
	VerdictStream StreamVerdict = iota
	// VerdictParseBuffered — 200 + SSE-olmayan gövde: sunucu stream:true
	// bayrağını yuttu ve tek atışta cevapladı. Gövde ZATEN cevaptır —
	// ikinci (faturalı) çağrı yapmadan çözümlenmeli.
	VerdictParseBuffered
	// VerdictFallbackCache — bayrağın kesin reddi (bazı vLLM sürümleri
	// stream:true'ya 400 döner): buffered'a BİR kez düş + kararı
	// önbelleğe yaz.
	VerdictFallbackCache
	// VerdictFallbackOnce — geçici ya da akışa özgü olmayan hata (429
	// kota, 5xx, auth): buffered'a BİR kez düş ama önbelleğe YAZMA.
	VerdictFallbackOnce
)

// StreamStage — geri düşüşün hangi aşamada olduğunu söyler. Service'in
// log satırı üçünü ayrı cümlelerle yazıyor ve operatör bu cümlelerle
// teşhis koyuyor (bağlanamadı mı, baş mı reddetti, akış boş mu geldi).
type StreamStage int

const (
	// StageConnect — istek hiç kurulamadı (DNS/TCP/TLS/timeout).
	StageConnect StreamStage = iota
	// StageHead — yanıt başı geldi ve akış DEĞİL (statü/content-type).
	StageHead
	// StageEmptyStream — SSE başlıkları geldi ama gövde TEK bir olay
	// bile üretmeden bitti. Hâlâ ilk-bayt bölgesi: bir kez geri düş,
	// karar YAZMA.
	StageEmptyStream
)

// StreamFallbackError — "akış olmadı". Service errors.As ile okur,
// Verdict + Stage'e bakıp kararı verir.
//
// Body, taşımanın ZATEN okuduğu gövdedir: VerdictParseBuffered'da tam
// tek-atış cevabı (Service onu buffered çözümleyiciye verir — ikinci
// faturalı çağrı YOK), diğerlerinde log için kırpılmış parçadır.
type StreamFallbackError struct {
	Verdict     StreamVerdict
	Stage       StreamStage
	Provider    string // "openai-compat" | "anthropic" — log/hata öneki
	Status      int    // StageHead dışında 0
	ContentType string
	Body        []byte
	Err         error // bağlanma / okuma hatası, varsa
}

func (e *StreamFallbackError) Error() string {
	switch e.Stage {
	case StageConnect:
		return fmt.Sprintf("%s stream connect: %v", e.Provider, e.Err)
	case StageEmptyStream:
		return fmt.Sprintf("%s stream produced no events (read err: %v)", e.Provider, e.Err)
	}
	return fmt.Sprintf("%s stream rejected %d: %s", e.Provider, e.Status, strings.TrimSpace(string(e.Body)))
}

func (e *StreamFallbackError) Unwrap() error { return e.Err }

// ClassifyStreamResponse, stream:true yoklamasının yanıt BAŞINI bir
// karara eşler. Saf + tablo testli.
func ClassifyStreamResponse(status int, contentType string) StreamVerdict {
	if status >= 200 && status < 300 {
		if strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
			return VerdictStream
		}
		return VerdictParseBuffered
	}
	switch status {
	case 400, 404, 405, 415, 422, 501:
		return VerdictFallbackCache
	}
	return VerdictFallbackOnce
}

// ─── SSE hattı ──────────────────────────────────────────────────────

// sseDataPayload, bir SSE "data:" satırının yükünü ayıklar. Çerçeve
// satırları (boş, "event:", "id:", ":" yorumları) ok=false döner.
func sseDataPayload(line string) (string, bool) {
	line = strings.TrimSuffix(line, "\r")
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	return strings.TrimSpace(line[len("data:"):]), true
}

// scanSSE, SSE gövdesini satır satır fn'e verir. Okuma hatasını döner
// (temiz EOF'ta nil). 1MB satır tavanı — tek bir SSE parçası birkaç
// token'dır, daha büyüğü bozuk sunucudur.
func scanSSE(r io.Reader, fn func(string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		fn(sc.Text())
	}
	return sc.Err()
}

// ─── OpenAI-compat biriktirici (saf, tablo testli) ──────────────────

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// openAIStreamAccum bir OpenAI-compat SSE akışını biriktirir. feed()
// ham satırları tüketir ve YAYINLANACAK içerik parçasını döndürür
// ("" = yayınlanacak bir şey yok: çerçeve, reasoning, tutulan think).
type openAIStreamAccum struct {
	content   strings.Builder // TÜM ham content parçaları, son kurtarma için
	reasoning strings.Builder // delta.reasoning_content / delta.reasoning — biriktirilir, ASLA yayınlanmaz
	gateBuf   strings.Builder // satır-içi <think> öneki belirsizken tutulan içerik
	gateOpen  bool            // içerik canlı yayına açıldı
	inThink   bool            // satır-içi <think>…</think> bloğu tutuluyor
	emitted   bool            // en az bir parça dışarı verildi
	sawData   bool            // en az bir çözümlenebilir data olayı (ilk-bayt hatası dedektörü)
	done      bool            // [DONE] görüldü
	finish    string          // son boş-olmayan finish_reason
	inTokens  int             // varsa son parçadaki usage
	outTokens int
}

// feed tek bir SSE satırını çözer. Bozuk data satırları atlanır — bir
// akış tek kötü parça yüzünden ASLA yarıda kesilmemeli.
func (a *openAIStreamAccum) feed(line string) string {
	payload, ok := sseDataPayload(line)
	if !ok {
		return ""
	}
	if payload == "[DONE]" {
		a.done = true
		return ""
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"` // vLLM --reasoning-parser şekli
				Reasoning        string `json:"reasoning"`         // v0.8.384 geçit şekli
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return "" // bozuk satır — atla
	}
	a.sawData = true
	if chunk.Usage != nil {
		if chunk.Usage.PromptTokens > 0 {
			a.inTokens = chunk.Usage.PromptTokens
		}
		if chunk.Usage.CompletionTokens > 0 {
			a.outTokens = chunk.Usage.CompletionTokens
		}
	}
	if len(chunk.Choices) == 0 {
		return "" // yalnız-usage son parçası (stream_options.include_usage)
	}
	c := chunk.Choices[0]
	if c.FinishReason != "" {
		a.finish = c.FinishReason
	}
	if c.Delta.ReasoningContent != "" {
		a.reasoning.WriteString(c.Delta.ReasoningContent)
	}
	if c.Delta.Reasoning != "" {
		a.reasoning.WriteString(c.Delta.Reasoning)
	}
	if c.Delta.Content == "" {
		return ""
	}
	a.content.WriteString(c.Delta.Content)
	return a.gate(c.Delta.Content)
}

// gate, BAŞTAKİ satır-içi <think>…</think> bloğunu canlı akıştan
// gizler (--reasoning-parser'sız bir vLLM düşünceyi content'e gömer).
// İçerik önek belirsizliği çözülene dek tutulur: think değilse boşalt
// ve sonrasını yayınla; think ise kapanış etiketine dek sessizce tut,
// sonra kuyruğu yayınla. Tutulan/ham içerik a.content'te durduğu için
// son kurtarma her hâlükârda onu görür.
func (a *openAIStreamAccum) gate(d string) string {
	if a.gateOpen {
		a.emitted = true
		return d
	}
	a.gateBuf.WriteString(d)
	buf := a.gateBuf.String()
	if !a.inThink {
		trimmed := strings.TrimLeft(buf, " \t\r\n")
		switch {
		case trimmed == "":
			return "" // şimdilik yalnız boşluk — tutmaya devam
		case len(trimmed) < len(thinkOpen) && strings.HasPrefix(thinkOpen, trimmed):
			return "" // hâlâ "<think>" olabilir — tutmaya devam
		case !strings.HasPrefix(trimmed, thinkOpen):
			a.gateOpen = true
			a.gateBuf.Reset()
			a.emitted = true
			return buf // think bloğu değil — tutulanı boşalt
		}
		a.inThink = true
	}
	if i := strings.Index(buf, thinkClose); i >= 0 {
		a.gateOpen, a.inThink = true, false
		a.gateBuf.Reset()
		after := buf[i+len(thinkClose):]
		if after != "" {
			a.emitted = true
		}
		return after
	}
	return "" // hâlâ think bloğunun içinde
}

// finishOpenAI son cevabı v0.8.384 kurtarma zinciriyle çözer ve
// istemciye HÂLÂ borçlu olunan son parçayı döndürür — bu parça tam
// olarak canlıda HİÇBİR ŞEY akmadığında doludur (yalnız-reasoning akış
// ya da kuyruksuz think bloğu): kurtarılan cevabın tamamı tek bir son
// parça olarak gider.
//
// Zincirin kendisi SalvageAnswer'dır (salvage.go): biriktirici
// reasoning_content ve reasoning'i tek tampona yazdığı için üçüncü
// argüman boştur. Faz 1.3'e dek burada zincirin ikinci bir yazılışı
// duruyordu.
func (a *openAIStreamAccum) finishOpenAI() (final, trailing string, err error) {
	final = SalvageAnswer(a.content.String(), a.reasoning.String(), "")
	if final == "" {
		if a.finish == "length" {
			// Bütçe teşhisi buffered yolla BİREBİR aynı cümle olmalı —
			// operatör aynı sorunu iki yoldan da aynı şekilde okur.
			return "", "", EmptyAnswerError("length")
		}
		return "", "", errors.New("openai-compat stream: model returned empty content — no answer in content/reasoning")
	}
	if !a.emitted {
		trailing = final
	}
	return final, trailing, nil
}

// ─── Anthropic biriktirici (saf, tablo testli) ──────────────────────

// anthropicStreamAccum bir Messages akışını biriktirir. Anthropic her
// data yükünü "type" ile etiketlediği için dağıtımda event: satırlarına
// ihtiyaç yok. text_delta akar; thinking_delta biriktirilir.
type anthropicStreamAccum struct {
	content   strings.Builder
	reasoning strings.Builder // thinking_delta — biriktirilir, ASLA yayınlanmaz
	emitted   bool
	sawData   bool
	errMsg    string // error olayının yükü
	inTokens  int
	outTokens int
}

func (a *anthropicStreamAccum) feed(line string) string {
	payload, ok := sseDataPayload(line)
	if !ok {
		return ""
	}
	var ev struct {
		Type    string `json:"type"`
		Message *struct {
			Usage struct {
				InputTokens int `json:"input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Delta *struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"delta"`
		Usage *struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		return "" // bozuk satır — atla
	}
	a.sawData = true
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			a.inTokens = ev.Message.Usage.InputTokens
		}
	case "content_block_delta":
		if ev.Delta == nil {
			return ""
		}
		switch ev.Delta.Type {
		case "text_delta":
			if ev.Delta.Text != "" {
				a.content.WriteString(ev.Delta.Text)
				a.emitted = true
				return ev.Delta.Text
			}
		case "thinking_delta":
			a.reasoning.WriteString(ev.Delta.Thinking)
		}
	case "message_delta":
		if ev.Usage != nil {
			a.outTokens = ev.Usage.OutputTokens
		}
	case "error":
		if ev.Error != nil {
			a.errMsg = ev.Error.Message
		}
	}
	return ""
}

func (a *anthropicStreamAccum) finishAnthropic() (final, trailing string, err error) {
	if a.errMsg != "" {
		return "", "", fmt.Errorf("anthropic stream error: %s", a.errMsg)
	}
	final = strings.TrimSpace(a.content.String())
	if final == "" {
		// Yalnız-düşünce akışı — openai-compat ile aynı kurtarma duruşu.
		final = StripThinking(strings.TrimSpace(a.reasoning.String()))
	}
	if final == "" {
		return "", "", errors.New("anthropic stream: empty response")
	}
	if !a.emitted {
		trailing = final
	}
	return final, trailing, nil
}

// ─── OpenAI-compat akış çağrısı ─────────────────────────────────────

// StreamOpenAI tek bir stream:true chat.completion çağrısı yapar ve
// içerik parçalarını onDelta ile yayınlar. onDelta nil olabilir.
//
// Akış kurulamazsa *StreamFallbackError döner — geri düşme KARARI
// çağıranındır (bkz. dosya başı). Akış başladıktan SONRA kopan bağlantı
// geri düşüş DEĞİLDİR: parçalar istemciye ulaştı, hata düz döner.
func StreamOpenAI(ctx context.Context, cfg Config, req Request, onDelta func(string)) (Response, error) {
	if cfg.HTTPClient == nil {
		return Response{}, errors.New("provider: nil HTTPClient — timeout ve TLS-skip ayarları onun içinde yaşıyor")
	}
	if req.JSONLevel != JSONPlain {
		// Akış yoluna bugüne dek hiç response_format gönderilmedi;
		// sessizce yok saymak "uyguladım" yanılsaması olurdu.
		return Response{}, fmt.Errorf("provider: akış yolunda JSONLevel=%d desteklenmiyor (çağıran basamağı düşürmeliydi)", req.JSONLevel)
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

	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"stream":     true,
		// Usage son parçada gelir. vLLM + OpenAI + Gemini'nin uyumluluk
		// katmanı include_usage'ı onurlandırıyor; reddeden bir sunucu
		// stream:true'yu reddedenle aynı 400→buffered geri düşüşüne
		// düşer — cevaplar iki hâlde de doğru kalır.
		"stream_options": map[string]any{"include_usage": true},
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
	url := strings.TrimRight(base, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		httpReq.Header.Set("api-key", cfg.APIKey) // v0.8.384 geçit şekli
	}

	resp, err := cfg.HTTPClient.Do(httpReq)
	if err != nil {
		// Bağlanma hatası. Gerçekten ölü bir uç buffered'da da patlar ve
		// normal yoldan yüzeye çıkar; karar önbelleğe YAZILMAZ.
		return Response{}, &StreamFallbackError{
			Verdict: VerdictFallbackOnce, Stage: StageConnect,
			Provider: labelOpenAI, Err: err,
		}
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if v := ClassifyStreamResponse(resp.StatusCode, ct); v != VerdictStream {
		limit := int64(4096)
		if v == VerdictParseBuffered {
			limit = maxRespBytes // gövde ZATEN cevap — tamamını taşı
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
		return Response{}, &StreamFallbackError{
			Verdict: v, Stage: StageHead, Provider: labelOpenAI,
			Status: resp.StatusCode, ContentType: ct, Body: respBody,
		}
	}

	acc := &openAIStreamAccum{}
	scanErr := scanSSE(resp.Body, func(line string) {
		if d := acc.feed(line); d != "" && onDelta != nil {
			onDelta(d)
		}
	})
	if !acc.sawData {
		// SSE başlıkları geldi ama gövde HİÇBİR olay üretmeden bitti
		// (anında EOF / anlık hata) — bu hâlâ ilk-bayt bölgesi.
		return Response{}, &StreamFallbackError{
			Verdict: VerdictFallbackOnce, Stage: StageEmptyStream,
			Provider: labelOpenAI, Err: scanErr,
		}
	}
	usage := Response{InputTokens: acc.inTokens, OutputTokens: acc.outTokens}
	if scanErr != nil {
		// Veri aktıktan SONRA kopma — geri düşüş yok (parçalar istemciye
		// ulaştı); hatayı yüzeye çıkar.
		return usage, fmt.Errorf("openai-compat stream read: %w", scanErr)
	}
	final, trailing, ferr := acc.finishOpenAI()
	if ferr != nil {
		return usage, ferr
	}
	if trailing != "" && onDelta != nil {
		onDelta(trailing)
	}
	usage.Text = final
	return usage, nil
}

// ─── Anthropic akış çağrısı ─────────────────────────────────────────

// StreamAnthropic tek bir stream:true Messages çağrısı yapar.
// Sözleşme StreamOpenAI ile aynı (bkz. yukarısı).
//
// Uç sabittir: Config.BaseURL bu yolda OKUNMAZ — buffered ikizindeki
// (DoAnthropic) kararın aynısı.
func StreamAnthropic(ctx context.Context, cfg Config, req Request, onDelta func(string)) (Response, error) {
	if cfg.HTTPClient == nil {
		return Response{}, errors.New("provider: nil HTTPClient — timeout ve TLS-skip ayarları onun içinde yaşıyor")
	}
	if req.JSONLevel != JSONPlain {
		return Response{}, fmt.Errorf("provider: anthropic akış yolunda JSONLevel=%d desteklenmiyor (response_format bu API'de yok)", req.JSONLevel)
	}
	model := req.Model
	if model == "" {
		model = cfg.Model
	}
	if model == "" {
		model = DefaultAnthropicModel
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	// v0.9.1120 — 1024 paritesinin ÜÇÜNCÜ ve son sitesi buydu: akış
	// yolu, uzun cevabın beklendiği ve kırpılmışının en görünmez olduğu
	// yer (metin öylece durur). Bütçe artık Request'ten gelir.
	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"system":     req.System,
		"stream":     true,
		"messages": []map[string]any{
			{"role": "user", "content": req.User},
		},
	}
	// temperature bilinçli olarak GÖNDERİLMEZ (anthropic.go başlığı, v0.10.253 D1).
	raw, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicURL, bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("X-API-Key", cfg.APIKey)
	httpReq.Header.Set("Anthropic-Version", anthropicVersion)

	resp, err := cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return Response{}, &StreamFallbackError{
			Verdict: VerdictFallbackOnce, Stage: StageConnect,
			Provider: labelAnthropic, Err: err,
		}
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if v := ClassifyStreamResponse(resp.StatusCode, ct); v != VerdictStream {
		limit := int64(4096)
		if v == VerdictParseBuffered {
			limit = maxRespBytes
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
		return Response{}, &StreamFallbackError{
			Verdict: v, Stage: StageHead, Provider: labelAnthropic,
			Status: resp.StatusCode, ContentType: ct, Body: respBody,
		}
	}

	acc := &anthropicStreamAccum{}
	scanErr := scanSSE(resp.Body, func(line string) {
		if d := acc.feed(line); d != "" && onDelta != nil {
			onDelta(d)
		}
	})
	if !acc.sawData {
		return Response{}, &StreamFallbackError{
			Verdict: VerdictFallbackOnce, Stage: StageEmptyStream,
			Provider: labelAnthropic, Err: scanErr,
		}
	}
	usage := Response{InputTokens: acc.inTokens, OutputTokens: acc.outTokens}
	if scanErr != nil {
		return usage, fmt.Errorf("anthropic stream read: %w", scanErr)
	}
	final, trailing, ferr := acc.finishAnthropic()
	if ferr != nil {
		return usage, ferr
	}
	if trailing != "" && onDelta != nil {
		onDelta(trailing)
	}
	usage.Text = final
	return usage, nil
}
