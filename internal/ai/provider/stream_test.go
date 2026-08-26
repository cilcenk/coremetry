// stream_test.go — FAZ 1.3'ün kabul ölçütü.
//
// İki katman pinleniyor:
//
//  1. SAF biriktiriciler + karar tablosu. Bunlar copilot/stream_test.go'dan
//     BİREBİR taşındı (v0.8.404'te yazılmışlardı): content parçaları, vLLM
//     reasoning-delta şekli, satır-içi <think> kapısı, [DONE], bozuk satır
//     atlama, son parçadan usage, anthropic olay şekli, 4 karar.
//  2. TEL ÜSTÜNDEKİ ŞEKİL: stream:true + stream_options.include_usage +
//     bütçe/temperature + header'lar. v0.9.1120'nin dersi — aynı sabitin
//     başka bir yazılışla geri gelmesini yalnız gövde testi yakalar; ve
//     include_usage düşerse akış yolunda usage SESSİZCE sıfırlanır
//     (ai_calls satırı maliyeti kaybeder, hiçbir davranış testi patlamaz).
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// ─── SSE roundtripper ───────────────────────────────────────────────

// streamRT — akış yanıtı üreten kaydedici taşıma. captureRT'den ayrı:
// burada Content-Type ve gövde okuyucusu vakadan vakaya değişiyor
// (SSE / JSON / yarıda kopan okuyucu).
type streamRT struct {
	reqs    []*http.Request
	bodies  []map[string]any
	headers []http.Header

	status      int
	contentType string
	body        string
	reader      io.Reader // doluysa body yerine bu kullanılır
	err         error
}

func (s *streamRT) RoundTrip(req *http.Request) (*http.Response, error) {
	s.reqs = append(s.reqs, req)
	s.headers = append(s.headers, req.Header.Clone())
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil && m != nil {
			s.bodies = append(s.bodies, m)
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	st := s.status
	if st == 0 {
		st = 200
	}
	ct := s.contentType
	if ct == "" {
		ct = "text/event-stream"
	}
	var r io.Reader = strings.NewReader(s.body)
	if s.reader != nil {
		r = s.reader
	}
	return &http.Response{
		StatusCode: st,
		Header:     http.Header{"Content-Type": []string{ct}},
		Body:       io.NopCloser(r),
		Request:    req,
	}, nil
}

func newStreamClient(rt *streamRT) *http.Client { return &http.Client{Transport: rt} }

// sse, satırları SSE çerçevesine sarar.
func sse(payloads ...string) string {
	var b strings.Builder
	for _, p := range payloads {
		fmt.Fprintf(&b, "data: %s\n\n", p)
	}
	return b.String()
}

// ─── 1. Saf biriktiriciler (copilot/stream_test.go'dan taşındı) ─────

// feedAll pushes raw SSE lines through the accumulator, collecting the
// live deltas exactly as the scan loop would.
func feedAll(a *openAIStreamAccum, lines []string) []string {
	var deltas []string
	for _, l := range lines {
		if d := a.feed(l); d != "" {
			deltas = append(deltas, d)
		}
	}
	return deltas
}

func TestOpenAIStreamAccumContentDeltas(t *testing.T) {
	a := &openAIStreamAccum{}
	deltas := feedAll(a, []string{
		`event: chunk`, // framing line — ignored
		``,             // blank separator — ignored
		`: keepalive comment`,
		`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
		`data: {"choices":[{"delta":{"content":"Hel"}}]}`,
		`data: not-json at all`, // malformed — skipped, never aborts
		`data: {"choices":[{"delta":{"content":"lo "}}]}`,
		`data: {"choices":[{"delta":{"content":"world"},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":42,"completion_tokens":7}}`, // include_usage final chunk
		`data: [DONE]`,
	})
	if got := strings.Join(deltas, "|"); got != "Hel|lo |world" {
		t.Fatalf("deltas = %q; want Hel|lo |world", got)
	}
	if !a.done || !a.sawData {
		t.Fatalf("done=%v sawData=%v; want both true", a.done, a.sawData)
	}
	if a.inTokens != 42 || a.outTokens != 7 {
		t.Fatalf("usage = (%d,%d); want (42,7) from the final chunk", a.inTokens, a.outTokens)
	}
	final, trailing, err := a.finishOpenAI()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if final != "Hello world" || trailing != "" {
		t.Fatalf("final=%q trailing=%q; want \"Hello world\" and no trailing (content streamed live)", final, trailing)
	}
}

func TestOpenAIStreamAccumReasoningOnlyStream(t *testing.T) {
	// The vLLM --reasoning-parser shape: every token in
	// delta.reasoning_content (or delta.reasoning), content never set.
	// NOTHING streams live; finish emits the salvaged answer as ONE
	// trailing delta — the v0.8.384 fallback, streamed.
	for _, field := range []string{"reasoning_content", "reasoning"} {
		t.Run(field, func(t *testing.T) {
			a := &openAIStreamAccum{}
			deltas := feedAll(a, []string{
				fmt.Sprintf(`data: {"choices":[{"delta":{"%s":"Merhaba! "}}]}`, field),
				fmt.Sprintf(`data: {"choices":[{"delta":{"%s":"Sorun payment-service."}}]}`, field),
				`data: [DONE]`,
			})
			if len(deltas) != 0 {
				t.Fatalf("reasoning must buffer silently; streamed %q", deltas)
			}
			final, trailing, err := a.finishOpenAI()
			if err != nil {
				t.Fatalf("finish: %v", err)
			}
			// ⚠ v0.10.66 — İŞARET BEKLENİYOR. Bu akışta content HİÇ
			// gelmiyor: operatörün eline geçen metin modelin ÇALIŞMA
			// NOTUDUR. v0.10.37 yalnız <think> dalını işaretliyordu ve
			// fark tamamen TAŞIYICIDAN geliyordu (aynı madde, ayrı alan).
			body := "Merhaba! Sorun payment-service."
			if !IsSalvagedThinking(final) {
				t.Fatalf("kurtarılan düşünce İŞARETSİZ: %q", final)
			}
			if !strings.Contains(final, body) || final != trailing {
				t.Fatalf("final=%q trailing=%q; ikisi de %q içermeli", final, trailing, body)
			}
		})
	}
}

func TestOpenAIStreamAccumInlineThinkGate(t *testing.T) {
	// A reasoning model WITHOUT a server-side parser inlines
	// <think>…</think> in content — the chain-of-thought must not
	// stream, the post-think answer must.
	a := &openAIStreamAccum{}
	deltas := feedAll(a, []string{
		`data: {"choices":[{"delta":{"content":"<th"}}]}`, // ambiguous prefix — held
		`data: {"choices":[{"delta":{"content":"ink>let me ponder"}}]}`,
		`data: {"choices":[{"delta":{"content":" the trace</think>The "}}]}`,
		`data: {"choices":[{"delta":{"content":"answer."}}]}`,
		`data: [DONE]`,
	})
	if got := strings.Join(deltas, "|"); got != "The |answer." {
		t.Fatalf("deltas = %q; want the post-think tail only", got)
	}
	final, trailing, err := a.finishOpenAI()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if final != "The answer." || trailing != "" {
		t.Fatalf("final=%q trailing=%q; want \"The answer.\" with no trailing", final, trailing)
	}
}

func TestOpenAIStreamAccumNonThinkPrefixFlushes(t *testing.T) {
	// "<p..." disambiguates as NOT <think> — the held prefix flushes.
	a := &openAIStreamAccum{}
	deltas := feedAll(a, []string{
		`data: {"choices":[{"delta":{"content":"<"}}]}`, // held (could become <think>)
		`data: {"choices":[{"delta":{"content":"p99 rose"}}]}`,
		`data: [DONE]`,
	})
	if got := strings.Join(deltas, "|"); got != "<p99 rose" {
		t.Fatalf("deltas = %q; want the flushed \"<p99 rose\"", got)
	}
}

func TestOpenAIStreamAccumThinkOnlySalvage(t *testing.T) {
	// Only a think block, no tail → nothing streams; finish salvages
	// the inside-think text as the one trailing delta.
	a := &openAIStreamAccum{}
	deltas := feedAll(a, []string{
		`data: {"choices":[{"delta":{"content":"<think>The checkout span holds an Oracle row lock."}}]}`,
		`data: {"choices":[{"delta":{"content":"</think>"}}]}`,
		`data: [DONE]`,
	})
	if len(deltas) != 0 {
		t.Fatalf("think-only content must not stream; got %q", deltas)
	}
	final, trailing, err := a.finishOpenAI()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	// v0.10.37 — kurtarma AYNEN çalışıyor (testin asıl niyeti) ama artık
	// İŞARETLİ: kurtarılan düşünce, nihai cevaptan ayırt edilebilmeli.
	const body = "The checkout span holds an Oracle row lock."
	if !strings.Contains(final, body) || final != trailing {
		t.Fatalf("final=%q trailing=%q; kurtarılan reasoning tek delta olarak gelmeliydi", final, trailing)
	}
	if !IsSalvagedThinking(final) {
		t.Fatalf("kurtarılan düşünce İŞARETSİZ — operatör onu nihai cevap sanar: %q", final)
	}
}

func TestOpenAIStreamAccumLengthBudgetError(t *testing.T) {
	a := &openAIStreamAccum{}
	feedAll(a, []string{
		`data: {"choices":[{"delta":{"reasoning_content":""},"finish_reason":"length"}]}`,
		`data: [DONE]`,
	})
	_, _, err := a.finishOpenAI()
	if err == nil {
		t.Fatal("expected the token-budget error for an empty length-terminated stream")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "budget") && !strings.Contains(err.Error(), "max_tokens") {
		t.Fatalf("error %q should mention the token budget / max_tokens", err.Error())
	}
	// Faz 1.3: bütçe cümlesi buffered yolla TEK yazılış (EmptyAnswerError).
	if err.Error() != EmptyAnswerError("length").Error() {
		t.Fatalf("akış bütçe hatası buffered ikizinden ayrışmış:\n stream  = %q\n buffered= %q",
			err.Error(), EmptyAnswerError("length").Error())
	}
}

func TestAnthropicStreamAccum(t *testing.T) {
	a := &anthropicStreamAccum{}
	var deltas []string
	for _, l := range []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":25}}}`,
		`data: {"type":"ping"}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`, // buffered
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Sorun "}}`,
		`data: not json`, // skipped
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"redis."}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}`,
		`data: {"type":"message_stop"}`,
	} {
		if d := a.feed(l); d != "" {
			deltas = append(deltas, d)
		}
	}
	if got := strings.Join(deltas, "|"); got != "Sorun |redis." {
		t.Fatalf("deltas = %q; want text_delta content only (thinking buffered)", got)
	}
	if a.inTokens != 25 || a.outTokens != 15 {
		t.Fatalf("usage = (%d,%d); want (25,15)", a.inTokens, a.outTokens)
	}
	final, trailing, err := a.finishAnthropic()
	if err != nil || final != "Sorun redis." || trailing != "" {
		t.Fatalf("finish = (%q,%q,%v); want (\"Sorun redis.\",\"\",nil)", final, trailing, err)
	}
}

func TestAnthropicStreamAccumErrorEvent(t *testing.T) {
	a := &anthropicStreamAccum{}
	a.feed(`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	if _, _, err := a.finishAnthropic(); err == nil || !strings.Contains(err.Error(), "Overloaded") {
		t.Fatalf("err = %v; want the stream error surfaced", err)
	}
}

func TestClassifyStreamResponse(t *testing.T) {
	cases := []struct {
		name   string
		status int
		ct     string
		want   StreamVerdict
	}{
		{"200 SSE streams", 200, "text/event-stream", VerdictStream},
		{"200 SSE with charset streams", 200, "text/event-stream; charset=utf-8", VerdictStream},
		{"200 JSON = server ignored stream:true, parse one-shot", 200, "application/json", VerdictParseBuffered},
		{"200 no content-type = parse one-shot", 200, "", VerdictParseBuffered},
		{"400 = deterministic rejection, cache", 400, "application/json", VerdictFallbackCache},
		{"404 = wrong route, cache", 404, "text/plain", VerdictFallbackCache},
		{"405 = method rejected, cache", 405, "", VerdictFallbackCache},
		{"415 = media type rejected, cache", 415, "", VerdictFallbackCache},
		{"422 = body rejected, cache", 422, "application/json", VerdictFallbackCache},
		{"501 = not implemented, cache", 501, "", VerdictFallbackCache},
		{"401 auth = fallback once, never cache", 401, "application/json", VerdictFallbackOnce},
		{"403 = fallback once, never cache", 403, "", VerdictFallbackOnce},
		{"429 quota (Gemini) = fallback once, never cache", 429, "application/json", VerdictFallbackOnce},
		{"500 = transient, fallback once", 500, "", VerdictFallbackOnce},
		{"503 = transient, fallback once", 503, "text/html", VerdictFallbackOnce},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyStreamResponse(c.status, c.ct); got != c.want {
				t.Fatalf("ClassifyStreamResponse(%d, %q) = %v; want %v", c.status, c.ct, got, c.want)
			}
		})
	}
}

// ─── 2. Golden request bodies ───────────────────────────────────────

// TestStreamOpenAI_GoldenRequestBody pins the SSE probe's wire shape.
// stream_options.include_usage is the field with no behavioural test
// of its own: drop it and answers stay correct while every ai_calls
// row for a streamed call silently reports 0 tokens.
func TestStreamOpenAI_GoldenRequestBody(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	rt := &streamRT{body: sse(
		`{"choices":[{"delta":{"content":"ok"}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":3}}`,
		`[DONE]`,
	)}
	cfg := Config{BaseURL: "http://llm.invalid/v1/", APIKey: "k", Model: "model-x", HTTPClient: newStreamClient(rt)}
	resp, err := StreamOpenAI(context.Background(), cfg,
		Request{Model: "model-x", MaxTokens: 8192, Temperature: f(0.9), System: "sys", User: "usr"}, nil)
	if err != nil {
		t.Fatalf("StreamOpenAI: %v", err)
	}
	if resp.Text != "ok" || resp.InputTokens != 11 || resp.OutputTokens != 3 {
		t.Fatalf("resp = %+v; want text=ok usage=(11,3)", resp)
	}
	if got := rt.reqs[0].URL.String(); got != "http://llm.invalid/v1/chat/completions" {
		t.Fatalf("url = %q (sondaki eğik çizgi tekrarlanmamalı)", got)
	}
	b := rt.bodies[0]
	if b["stream"] != true {
		t.Fatalf("stream = %v; want true", b["stream"])
	}
	so, ok := b["stream_options"].(map[string]any)
	if !ok || so["include_usage"] != true {
		t.Fatalf("stream_options = %v; want {include_usage:true} — düşerse usage sessizce 0'lanır", b["stream_options"])
	}
	if b["model"] != "model-x" || b["max_tokens"] != float64(8192) || b["temperature"] != float64(0.9) {
		t.Fatalf("model/max_tokens/temperature = %v/%v/%v", b["model"], b["max_tokens"], b["temperature"])
	}
	msgs, _ := b["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v; want [system,user]", b["messages"])
	}
	m0, _ := msgs[0].(map[string]any)
	m1, _ := msgs[1].(map[string]any)
	if m0["role"] != "system" || m0["content"] != "sys" || m1["role"] != "user" || m1["content"] != "usr" {
		t.Fatalf("messages = %v", msgs)
	}
	h := rt.headers[0]
	if h.Get("Accept") != "text/event-stream" {
		t.Fatalf("Accept = %q; want text/event-stream", h.Get("Accept"))
	}
	if h.Get("Authorization") != "Bearer k" || h.Get("Api-Key") != "k" {
		t.Fatalf("auth headers: %q / %q — v0.8.384 ikizi ikisini de ister", h.Get("Authorization"), h.Get("Api-Key"))
	}
}

func TestStreamOpenAI_KeylessEndpointSendsNoAuth(t *testing.T) {
	// Ollama varsayılanı: anahtarsız yerel uç (buffered yolla aynı kural).
	rt := &streamRT{body: sse(`{"choices":[{"delta":{"content":"ok"}}]}`, `[DONE]`)}
	cfg := Config{BaseURL: "http://ollama:11434/v1", Model: "llama3.1", HTTPClient: newStreamClient(rt)}
	if _, err := StreamOpenAI(context.Background(), cfg, Request{System: "s", User: "u"}, nil); err != nil {
		t.Fatalf("StreamOpenAI: %v", err)
	}
	if h := rt.headers[0]; h.Get("Authorization") != "" || h.Get("Api-Key") != "" {
		t.Fatalf("anahtarsız uçta auth header gönderildi: %q / %q", h.Get("Authorization"), h.Get("Api-Key"))
	}
	if b := rt.bodies[0]; b["model"] != "llama3.1" || b["max_tokens"] != float64(defaultMaxTokens) {
		t.Fatalf("varsayılanlar uygulanmadı: %v", b)
	}
}

// TestStreamAnthropic_GoldenRequestBody — v0.9.1120'nin ÜÇÜNCÜ sitesi.
// Bu yol ~1000 sürüm boyunca sabit 1024 + temperature'sız gitti; akış
// yolunda kırpılmış cevap "bitmiş" gibi göründüğü için en görünmez
// hâliydi. Sürüm header'ı da burada: düşerse yol TAMAMEN kırılır.
func TestStreamAnthropic_GoldenRequestBody(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	rt := &streamRT{body: sse(
		`{"type":"message_start","message":{"usage":{"input_tokens":25}}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"cevap"}}`,
		`{"type":"message_delta","usage":{"output_tokens":15}}`,
	)}
	cfg := Config{BaseURL: "http://ignored.invalid/v1", APIKey: "sk-ant", Model: "claude-x", HTTPClient: newStreamClient(rt)}
	resp, err := StreamAnthropic(context.Background(), cfg,
		Request{MaxTokens: 8192, Temperature: f(0.9), System: "sys", User: "usr"}, nil)
	if err != nil {
		t.Fatalf("StreamAnthropic: %v", err)
	}
	if resp.Text != "cevap" || resp.InputTokens != 25 || resp.OutputTokens != 15 {
		t.Fatalf("resp = %+v", resp)
	}
	// Uç SABİT: operatörün openai-compat baseURL'i bu yola sızmamalı.
	if got := rt.reqs[0].URL.String(); got != anthropicURL {
		t.Fatalf("url = %q; want %q (baseURL bu yolda okunmaz)", got, anthropicURL)
	}
	b := rt.bodies[0]
	if b["stream"] != true || b["system"] != "sys" || b["model"] != "claude-x" ||
		b["max_tokens"] != float64(8192) || b["temperature"] != float64(0.9) {
		t.Fatalf("gövde = %v", b)
	}
	if _, has := b["stream_options"]; has {
		t.Fatalf("anthropic gövdesine openai'ye özgü stream_options sızmış: %v", b)
	}
	msgs, _ := b["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v; want tek user turu (system ayrı alan)", b["messages"])
	}
	h := rt.headers[0]
	if h.Get("Anthropic-Version") != anthropicVersion {
		t.Fatalf("Anthropic-Version = %q; eksikse API 400 döner", h.Get("Anthropic-Version"))
	}
	if h.Get("X-Api-Key") != "sk-ant" || h.Get("Accept") != "text/event-stream" {
		t.Fatalf("headers: X-API-Key=%q Accept=%q", h.Get("X-Api-Key"), h.Get("Accept"))
	}
}

func TestStreamRejectsJSONLevel(t *testing.T) {
	// Akış yoluna response_format hiç gönderilmedi; sessizce yok saymak
	// "uyguladım" yanılsaması olurdu (fail-open-silently-unapplies).
	rt := &streamRT{}
	cfg := Config{BaseURL: "http://llm.invalid/v1", HTTPClient: newStreamClient(rt)}
	req := Request{System: "s", User: "u", JSONLevel: JSONObject}
	if _, err := StreamOpenAI(context.Background(), cfg, req, nil); err == nil {
		t.Fatal("StreamOpenAI JSONObject'i sessizce kabul etti")
	}
	if _, err := StreamAnthropic(context.Background(), cfg, req, nil); err == nil {
		t.Fatal("StreamAnthropic JSONObject'i sessizce kabul etti")
	}
	if len(rt.reqs) != 0 {
		t.Fatalf("reddedilen basamakta yine de istek gitti: %d", len(rt.reqs))
	}
}

// ─── 3. Geri-düşüş raporu (karar Service'te, RAPOR burada) ──────────

// TestStreamFallbackReport — taşımanın Service'e ne söylediğini pinler:
// hangi verdict, hangi aşama, gövde taşınıyor mu. Kararın kendisi
// (buffered'a düş / önbelleğe yaz) copilot tarafında test ediliyor.
func TestStreamFallbackReport(t *testing.T) {
	oneShot := `{"choices":[{"message":{"content":"tek atış"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`
	cases := []struct {
		name        string
		rt          *streamRT
		wantVerdict StreamVerdict
		wantStage   StreamStage
		wantBody    string
	}{
		{
			name:        "400 stream:true reddi → kalıcı karar",
			rt:          &streamRT{status: 400, contentType: "application/json", body: `{"error":{"message":"stream is not supported"}}`},
			wantVerdict: VerdictFallbackCache, wantStage: StageHead,
			wantBody: `{"error":{"message":"stream is not supported"}}`,
		},
		{
			name:        "429 kota → yalnız bu çağrı",
			rt:          &streamRT{status: 429, contentType: "application/json", body: `{"error":"quota"}`},
			wantVerdict: VerdictFallbackOnce, wantStage: StageHead,
			wantBody: `{"error":"quota"}`,
		},
		{
			name:        "200 + JSON → gövde ZATEN cevap, tamamı taşınır",
			rt:          &streamRT{status: 200, contentType: "application/json", body: oneShot},
			wantVerdict: VerdictParseBuffered, wantStage: StageHead,
			wantBody: oneShot,
		},
		{
			name:        "SSE başlıkları + anında EOF → ilk-bayt, karar YAZILMAZ",
			rt:          &streamRT{status: 200, contentType: "text/event-stream", body: ""},
			wantVerdict: VerdictFallbackOnce, wantStage: StageEmptyStream,
		},
		{
			name:        "bağlanamadı → karar YAZILMAZ",
			rt:          &streamRT{err: errors.New("dial tcp: connection refused")},
			wantVerdict: VerdictFallbackOnce, wantStage: StageConnect,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := Config{BaseURL: "http://llm.invalid/v1", HTTPClient: newStreamClient(c.rt)}
			_, err := StreamOpenAI(context.Background(), cfg, Request{System: "s", User: "u"}, nil)
			var fe *StreamFallbackError
			if !errors.As(err, &fe) {
				t.Fatalf("err = %v; want *StreamFallbackError", err)
			}
			if fe.Verdict != c.wantVerdict || fe.Stage != c.wantStage {
				t.Fatalf("verdict/stage = %v/%v; want %v/%v", fe.Verdict, fe.Stage, c.wantVerdict, c.wantStage)
			}
			if fe.Provider != labelOpenAI {
				t.Fatalf("provider etiketi = %q; want %q", fe.Provider, labelOpenAI)
			}
			if c.wantBody != "" && string(fe.Body) != c.wantBody {
				t.Fatalf("body = %q; want %q", fe.Body, c.wantBody)
			}
			// VerdictParseBuffered'ın taşıdığı gövde ikinci bir faturalı
			// çağrı olmadan çözümlenebilmeli.
			if c.wantVerdict == VerdictParseBuffered {
				parsed, perr := ParseOpenAIChat(fe.Body)
				if perr != nil || parsed.Text != "tek atış" {
					t.Fatalf("tek-atış gövdesi çözümlenemedi: %v / %+v", perr, parsed)
				}
			}
		})
	}
}

func TestStreamAnthropicFallbackCarriesItsOwnLabel(t *testing.T) {
	rt := &streamRT{status: 400, contentType: "application/json", body: `{"type":"error"}`}
	cfg := Config{APIKey: "k", HTTPClient: newStreamClient(rt)}
	_, err := StreamAnthropic(context.Background(), cfg, Request{System: "s", User: "u"}, nil)
	var fe *StreamFallbackError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v; want *StreamFallbackError", err)
	}
	if fe.Provider != labelAnthropic || fe.Verdict != VerdictFallbackCache {
		t.Fatalf("fe = %+v; want anthropic/FallbackCache", fe)
	}
}

// ─── 4. Akış BAŞLADIKTAN sonra kopma: geri düşüş YOK ────────────────

// halfReader, verilen baytları döndürür sonra hata verir — akış
// ortasında kopan bir bağlantı.
type halfReader struct {
	data []byte
	done bool
}

func (h *halfReader) Read(p []byte) (int, error) {
	if h.done {
		return 0, errors.New("unexpected EOF mid-stream")
	}
	h.done = true
	n := copy(p, h.data)
	return n, nil
}

func TestStreamOpenAIMidStreamBreakIsNotFallback(t *testing.T) {
	rt := &streamRT{reader: &halfReader{data: []byte(
		"data: {\"choices\":[{\"delta\":{\"content\":\"yarı\"}}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":1}}\n\n")}}
	cfg := Config{BaseURL: "http://llm.invalid/v1", HTTPClient: newStreamClient(rt)}
	var deltas []string
	resp, err := StreamOpenAI(context.Background(), cfg, Request{System: "s", User: "u"},
		func(d string) { deltas = append(deltas, d) })
	if err == nil {
		t.Fatal("mid-stream kopması hata döndürmeli")
	}
	var fe *StreamFallbackError
	if errors.As(err, &fe) {
		t.Fatalf("mid-stream kopması GERİ DÜŞÜŞ değildir (parçalar istemciye ulaştı): %v", err)
	}
	if !strings.Contains(err.Error(), "openai-compat stream read") {
		t.Fatalf("err = %v; want the stream-read wrapper", err)
	}
	// Faturalanmış token'lar hata yolunda da taşınmalı (/ai satırı).
	if resp.InputTokens != 9 || resp.OutputTokens != 1 {
		t.Fatalf("usage = (%d,%d); want (9,1) — başarısız çağrı da faturalı", resp.InputTokens, resp.OutputTokens)
	}
	if strings.Join(deltas, "") != "yarı" {
		t.Fatalf("deltas = %v; kopmadan önceki parçalar yayınlanmış olmalı", deltas)
	}
}

// ─── 5. Trailing delta ──────────────────────────────────────────────

func TestStreamOpenAIEmitsSalvagedAnswerAsOneDelta(t *testing.T) {
	// Yalnız-reasoning akış: canlıda hiçbir şey akmaz, kurtarılan cevap
	// TEK son parça olarak gider (yoksa panel boş kalırdı).
	rt := &streamRT{body: sse(
		`{"choices":[{"delta":{"reasoning_content":"Sorun redis bağlantı havuzu."}}]}`,
		`[DONE]`,
	)}
	cfg := Config{BaseURL: "http://llm.invalid/v1", HTTPClient: newStreamClient(rt)}
	var deltas []string
	resp, err := StreamOpenAI(context.Background(), cfg, Request{System: "s", User: "u"},
		func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("StreamOpenAI: %v", err)
	}
	// ⚠ v0.10.66 — content HİÇ gelmedi, yani bu metin modelin ÇALIŞMA
	// NOTU ve işaretli çıkmalı. Sözleşmenin geri kalanı aynı: TEK son
	// parça, ve gövde korunuyor.
	body := "Sorun redis bağlantı havuzu."
	if len(deltas) != 1 || strings.Join(deltas, "") != resp.Text {
		t.Fatalf("text=%q deltas=%v; tek son parça bekleniyordu", resp.Text, deltas)
	}
	if !IsSalvagedThinking(resp.Text) {
		t.Fatalf("kurtarılan düşünce İŞARETSİZ: %q", resp.Text)
	}
	if !strings.Contains(resp.Text, body) {
		t.Fatalf("gövde kayboldu: %q", resp.Text)
	}
}

func TestStreamNilHTTPClientRejected(t *testing.T) {
	// Nil'e sessizce http.DefaultClient koymak operatörün 180s
	// timeout'unu ve kurumsal-CA muafiyetini haber vermeden düşürürdü.
	if _, err := StreamOpenAI(context.Background(), Config{}, Request{}, nil); err == nil {
		t.Fatal("nil HTTPClient sessizce kabul edildi (openai)")
	}
	if _, err := StreamAnthropic(context.Background(), Config{}, Request{}, nil); err == nil {
		t.Fatal("nil HTTPClient sessizce kabul edildi (anthropic)")
	}
}
