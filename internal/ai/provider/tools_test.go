// tools_test.go — FAZ 1.3, tool-calling tarafının kabul ölçütü.
//
// Bu yolun kırılganlığı tel-şekli seviyesinde: iki sağlayıcı aynı
// kavramı (fonksiyon çağrısı + sonucun geri beslenmesi) TAMAMEN farklı
// kodluyor ve arada bir de Gemini'nin uyumluluk ucu var. Bu yüzden
// testler gövdeleri AÇIKÇA yazıyor:
//
//   - anthropic: tool_use / tool_result içerik blokları
//   - openai:    tools[] + assistant.tool_calls + role:tool mesajları
//   - gemini:    yakalanan HAM tool_call nesnesinin birebir tekrarı
//     (v0.8.373 — thought_signature düşerse ikinci tur 400 alır)
//
// Ayrıca tool-call turunun BOŞ content'i: reasoning yedeği oraya
// yazarsa döngü "cevap geldi" sanıp tool'u hiç çalıştırmadan biter.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func toolsClient(rt *captureRT) Config {
	return Config{BaseURL: "http://llm.invalid/v1", APIKey: "k", Model: "model-x", HTTPClient: newCaptureClient(rt)}
}

func demoTools() []ToolSpec {
	return []ToolSpec{{
		Name:        "list_problems",
		Description: "Açık problemleri listeler",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"status": map[string]any{"type": "string"}},
		},
	}}
}

// ─── OpenAI golden body ─────────────────────────────────────────────

func TestChatOpenAITools_GoldenRequestBody(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	rt := &captureRT{}
	resp, err := ChatOpenAITools(context.Background(), toolsClient(rt), ChatRequest{
		Model: "model-x", MaxTokens: 8192, Temperature: f(0.9), System: "sys",
		Messages: []ChatMessage{{Role: "user", Text: "son 1 saatte hata var mı?"}},
		Tools:    demoTools(),
	})
	if err != nil {
		t.Fatalf("ChatOpenAITools: %v", err)
	}
	if resp.Text != "ok" || resp.InputTokens != 11 || resp.OutputTokens != 22 {
		t.Fatalf("resp = %+v", resp)
	}
	if got := rt.reqs[0].URL.String(); got != "http://llm.invalid/v1/chat/completions" {
		t.Fatalf("url = %q", got)
	}
	b := rt.bodies[0]
	if b["max_tokens"] != float64(8192) || b["temperature"] != float64(0.9) || b["model"] != "model-x" {
		t.Fatalf("tuning gövdeye binmedi: %v", b)
	}
	msgs, _ := b["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v; want [system, user]", b["messages"])
	}
	if m := msgs[0].(map[string]any); m["role"] != "system" || m["content"] != "sys" {
		t.Fatalf("ilk mesaj system olmalı: %v", m)
	}
	tools, _ := b["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", b["tools"])
	}
	tw := tools[0].(map[string]any)
	fn, _ := tw["function"].(map[string]any)
	if tw["type"] != "function" || fn["name"] != "list_problems" || fn["description"] != "Açık problemleri listeler" {
		t.Fatalf("openai tool sarmalayıcısı bozuk: %v", tw)
	}
	// openai `parameters` der, anthropic `input_schema` — karışırsa
	// model hiçbir tool'u çağıramaz.
	if _, ok := fn["parameters"].(map[string]any); !ok {
		t.Fatalf("openai tool şeması `parameters` altında olmalı: %v", fn)
	}
	if h := rt.reqs[0].Header; h.Get("Authorization") != "Bearer k" || h.Get("Api-Key") != "k" {
		t.Fatalf("auth ikizi eksik: %q / %q", h.Get("Authorization"), h.Get("Api-Key"))
	}
}

// TestChatOpenAITools_EmptyToolListStillSendsField — döngünün son turu
// tool'ları KASITLI olarak nil geçiyor ("elindekiyle cevapla"). Alan
// tamamen düşerse bazı uçlar önceki turdaki tool_calls'ı çözemez.
func TestChatOpenAITools_EmptyToolListStillSendsField(t *testing.T) {
	rt := &captureRT{}
	if _, err := ChatOpenAITools(context.Background(), toolsClient(rt), ChatRequest{
		System: "sys", Messages: []ChatMessage{{Role: "user", Text: "q"}},
	}); err != nil {
		t.Fatalf("ChatOpenAITools: %v", err)
	}
	tools, ok := rt.bodies[0]["tools"].([]any)
	if !ok || len(tools) != 0 {
		t.Fatalf("tools = %v; want boş dizi (alan düşmemeli)", rt.bodies[0]["tools"])
	}
}

// TestChatOpenAITools_ToolRoundTrip — tool_call → tool sonucu → sonraki
// tur kodlaması. OpenAI'de her sonuç KENDİ role:tool mesajıdır;
// asistan turu ise content + tool_calls taşır.
func TestChatOpenAITools_ToolRoundTrip(t *testing.T) {
	rt := &captureRT{}
	msgs := []ChatMessage{
		{Role: "user", Text: "hata var mı?"},
		{Role: "assistant", Text: "bakıyorum", ToolCalls: []ToolCall{
			{ID: "c9", Name: "list_problems", Input: json.RawMessage(`{"status":"open"}`)},
		}},
		{Role: "user", ToolResults: []ToolResult{
			{CallID: "c9", Name: "list_problems", Content: `{"problems":[]}`},
			{CallID: "c9b", Name: "list_problems", Content: "error: timeout", IsError: true},
		}},
	}
	if _, err := ChatOpenAITools(context.Background(), toolsClient(rt), ChatRequest{
		System: "sys", Messages: msgs, Tools: demoTools(),
	}); err != nil {
		t.Fatalf("ChatOpenAITools: %v", err)
	}
	got, _ := rt.bodies[0]["messages"].([]any)
	// system, user, assistant(+tool_calls), tool, tool
	if len(got) != 5 {
		t.Fatalf("messages = %d adet; want 5 (%v)", len(got), got)
	}
	asst := got[2].(map[string]any)
	if asst["role"] != "assistant" || asst["content"] != "bakıyorum" {
		t.Fatalf("asistan turu = %v", asst)
	}
	tcs, _ := asst["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("tool_calls = %v", asst["tool_calls"])
	}
	tc := tcs[0].(map[string]any)
	fn := tc["function"].(map[string]any)
	// Raw yokken yeniden kurulan şekil: arguments STRING'dir, nesne değil.
	if tc["id"] != "c9" || tc["type"] != "function" || fn["name"] != "list_problems" ||
		fn["arguments"] != `{"status":"open"}` {
		t.Fatalf("yeniden kurulan tool_call bozuk: %v", tc)
	}
	for i, want := range []struct{ id, content string }{{"c9", `{"problems":[]}`}, {"c9b", "error: timeout"}} {
		m := got[3+i].(map[string]any)
		if m["role"] != "tool" || m["tool_call_id"] != want.id || m["content"] != want.content {
			t.Fatalf("tool sonucu[%d] = %v; want role:tool %s", i, m, want.id)
		}
	}
}

// TestChatOpenAITools_GeminiRawReplay — v0.8.373, operatör-bildirimi.
// Gemini'nin uyumluluk ucu tool_call'a extra_content →
// thought_signature iliştiriyor ve tekrar oynatmada eksikse İKİNCİ turu
// 400 INVALID_ARGUMENT ile reddediyor. Nesne HAM hâliyle saklanır ve
// BİREBİR geri gönderilir.
func TestChatOpenAITools_GeminiRawReplay(t *testing.T) {
	const geminiStyle = `{"choices":[{"message":{"content":"",` +
		`"tool_calls":[{"id":"call_1","type":"function",` +
		`"function":{"name":"list_problems","arguments":"{\"status\":\"open\"}"},` +
		`"extra_content":{"google":{"thought_signature":"SIG-XYZ"}}}]}}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":5}}`

	rt := &captureRT{body: geminiStyle}
	turn, err := ChatOpenAITools(context.Background(), toolsClient(rt), ChatRequest{
		System: "sys", Messages: []ChatMessage{{Role: "user", Text: "q"}}, Tools: demoTools(),
	})
	if err != nil {
		t.Fatalf("tur 1: %v", err)
	}
	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Name != "list_problems" ||
		turn.ToolCalls[0].ID != "call_1" || string(turn.ToolCalls[0].Input) != `{"status":"open"}` {
		t.Fatalf("yürütücü görüşü bozuk: %+v", turn.ToolCalls)
	}
	if !strings.Contains(string(turn.ToolCalls[0].Raw), "SIG-XYZ") {
		t.Fatalf("ham tool_call sağlayıcı eklerini kaybetti: %s", turn.ToolCalls[0].Raw)
	}

	// Tur 2: tekrar oynatma — gövde thought_signature'ı TAŞIMALI.
	rt2 := &captureRT{}
	if _, err := ChatOpenAITools(context.Background(), toolsClient(rt2), ChatRequest{
		System: "sys", Messages: []ChatMessage{
			{Role: "user", Text: "q"},
			{Role: "assistant", ToolCalls: turn.ToolCalls},
			{Role: "user", ToolResults: []ToolResult{{CallID: "call_1", Name: "list_problems", Content: `{"problems":[]}`}}},
		},
	}); err != nil {
		t.Fatalf("tur 2: %v", err)
	}
	replayed, _ := json.Marshal(rt2.bodies[0])
	if !strings.Contains(string(replayed), "thought_signature") || !strings.Contains(string(replayed), "SIG-XYZ") {
		t.Fatalf("tekrar oynatılan gövde thought_signature'ı düşürdü:\n%s", replayed)
	}
	if !strings.Contains(string(replayed), `"tool_call_id":"call_1"`) {
		t.Fatalf("tool sonucu call id'sini kaybetti:\n%s", replayed)
	}
}

// TestParseOpenAIToolsChat_EmptyContentGuard — reasoning yedeği YALNIZ
// tool çağrısı YOKKEN devreye girer. Bir tool-call turunun boş content'i
// meşrudur; oraya düşünce metni yazılırsa döngü tool'u hiç çalıştırmadan
// "cevap" basar.
func TestParseOpenAIToolsChat_EmptyContentGuard(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantText  string
		wantCalls int
	}{
		{
			name: "tool çağrısı YOK + reasoning dolu → kurtar",
			body: `{"choices":[{"message":{"content":null,"reasoning":"<think>hm</think>Merhaba!"}}],` +
				`"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			wantText: "Merhaba!",
		},
		{
			name: "reasoning_content da aynı kurtarmayı alır",
			body: `{"choices":[{"message":{"content":"","reasoning_content":"Cevap burada."}}],` +
				`"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			wantText: "Cevap burada.",
		},
		{
			name: "tool çağrısı VAR + reasoning dolu → content BOŞ kalır",
			body: `{"choices":[{"message":{"content":null,"reasoning":"düşünüyorum...",` +
				`"tool_calls":[{"id":"c1","type":"function","function":{"name":"list_problems","arguments":"{}"}}]}}],` +
				`"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			wantText: "", wantCalls: 1,
		},
		{
			name: "argümansız tool_call {} olur (nil JSON çözümlenemez)",
			body: `{"choices":[{"message":{"content":"",` +
				`"tool_calls":[{"id":"c2","type":"function","function":{"name":"list_problems","arguments":""}}]}}],` +
				`"usage":{}}`,
			wantText: "", wantCalls: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOpenAIToolsChat([]byte(tc.body), labelOpenAI)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got.Text != tc.wantText {
				t.Fatalf("text = %q; want %q", got.Text, tc.wantText)
			}
			if len(got.ToolCalls) != tc.wantCalls {
				t.Fatalf("tool çağrısı = %d; want %d", len(got.ToolCalls), tc.wantCalls)
			}
			for _, c := range got.ToolCalls {
				if len(c.Input) == 0 {
					t.Fatalf("boş argüman {} ile doldurulmalıydı: %+v", c)
				}
				if len(c.Raw) == 0 {
					t.Fatalf("Raw yakalanmadı — Gemini tekrar oynatması kırılır: %+v", c)
				}
			}
		})
	}
}

func TestChatOpenAITools_HTTPErrorCarriesStatus(t *testing.T) {
	rt := &captureRT{status: 429, body: `{"error":{"message":"quota exceeded"}}`}
	_, err := ChatOpenAITools(context.Background(), toolsClient(rt), ChatRequest{
		System: "sys", Messages: []ChatMessage{{Role: "user", Text: "q"}},
	})
	if err == nil {
		t.Fatal("429 hata döndürmeliydi")
	}
	// copilot.isQuotaErr METNE bakıyor (" 429") — biçim eski
	// fmt.Errorf ile birebir aynı kalmalı, yoksa kota kesicisi sessizce
	// silahsızlanır.
	if !strings.HasPrefix(err.Error(), "openai-compat 429: ") {
		t.Fatalf("hata metni = %q; want \"openai-compat 429: …\"", err.Error())
	}
	if !strings.Contains(err.Error(), "quota") {
		t.Fatalf("gövde hata metnine girmedi: %q", err.Error())
	}
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 429 {
		t.Fatalf("HTTPError tipi/statüsü okunamadı: %v", err)
	}
}

// ─── Anthropic golden body ──────────────────────────────────────────

func TestChatAnthropicTools_GoldenRequestBody(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	rt := &captureRT{body: `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":41,"output_tokens":17}}`}
	cfg := Config{BaseURL: "http://ignored.invalid/v1", APIKey: "sk-ant", Model: "claude-x", HTTPClient: newCaptureClient(rt)}
	resp, err := ChatAnthropicTools(context.Background(), cfg, ChatRequest{
		MaxTokens: 8192, Temperature: f(0.9), System: "sys",
		Messages: []ChatMessage{{Role: "user", Text: "hata var mı?"}},
		Tools:    demoTools(),
	})
	if err != nil {
		t.Fatalf("ChatAnthropicTools: %v", err)
	}
	if resp.Text != "ok" || resp.InputTokens != 41 || resp.OutputTokens != 17 {
		t.Fatalf("resp = %+v", resp)
	}
	if got := rt.reqs[0].URL.String(); got != anthropicURL {
		t.Fatalf("url = %q; want %q (baseURL bu yolda okunmaz)", got, anthropicURL)
	}
	if h := rt.reqs[0].Header; h.Get("Anthropic-Version") != anthropicVersion || h.Get("X-Api-Key") != "sk-ant" {
		t.Fatalf("headers: version=%q key=%q", h.Get("Anthropic-Version"), h.Get("X-Api-Key"))
	}
	b := rt.bodies[0]
	if b["max_tokens"] != float64(8192) || b["temperature"] != float64(0.9) ||
		b["system"] != "sys" || b["model"] != "claude-x" {
		t.Fatalf("gövde = %v", b)
	}
	tools, _ := b["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", b["tools"])
	}
	tw := tools[0].(map[string]any)
	if tw["name"] != "list_problems" {
		t.Fatalf("anthropic tool düz nesnedir (sarmalayıcısız): %v", tw)
	}
	if _, ok := tw["input_schema"].(map[string]any); !ok {
		t.Fatalf("anthropic şeması `input_schema` altında olmalı: %v", tw)
	}
	if _, bad := tw["function"]; bad {
		t.Fatalf("openai sarmalayıcısı anthropic gövdesine sızmış: %v", tw)
	}
}

// TestChatAnthropicTools_BlockRoundTrip — anthropic'te tool çağrısı ve
// sonucu MESAJ İÇİ BLOKLARDIR; openai'nin ayrı role:tool mesajı burada
// 400 alır. input ayrıca NESNE olarak gider (openai'de string).
func TestChatAnthropicTools_BlockRoundTrip(t *testing.T) {
	rt := &captureRT{body: `{"content":[],"usage":{}}`}
	cfg := Config{APIKey: "k", Model: "claude-x", HTTPClient: newCaptureClient(rt)}
	msgs := []ChatMessage{
		{Role: "user", Text: "hata var mı?"},
		{Role: "assistant", Text: "bakıyorum", ToolCalls: []ToolCall{
			{ID: "tu_1", Name: "list_problems", Input: json.RawMessage(`{"status":"open"}`)},
		}},
		{Role: "user", ToolResults: []ToolResult{
			{CallID: "tu_1", Name: "list_problems", Content: `{"problems":[]}`},
			{CallID: "tu_2", Name: "list_problems", Content: "error: timeout", IsError: true},
		}},
	}
	if _, err := ChatAnthropicTools(context.Background(), cfg, ChatRequest{System: "sys", Messages: msgs}); err != nil {
		t.Fatalf("ChatAnthropicTools: %v", err)
	}
	got, _ := rt.bodies[0]["messages"].([]any)
	if len(got) != 3 {
		t.Fatalf("messages = %d; want 3 (system AYRI alan, tool sonuçları mesaj AÇMAZ)", len(got))
	}
	asst := got[1].(map[string]any)
	blocks, _ := asst["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("asistan blokları = %v; want [text, tool_use]", asst["content"])
	}
	tu := blocks[1].(map[string]any)
	if tu["type"] != "tool_use" || tu["id"] != "tu_1" || tu["name"] != "list_problems" {
		t.Fatalf("tool_use bloğu = %v", tu)
	}
	if in, ok := tu["input"].(map[string]any); !ok || in["status"] != "open" {
		t.Fatalf("anthropic input NESNE olmalı (openai'de string): %v", tu["input"])
	}
	res, _ := got[2].(map[string]any)
	rblocks, _ := res["content"].([]any)
	if len(rblocks) != 2 {
		t.Fatalf("sonuç blokları = %v; want iki tool_result", res["content"])
	}
	r0 := rblocks[0].(map[string]any)
	r1 := rblocks[1].(map[string]any)
	if r0["type"] != "tool_result" || r0["tool_use_id"] != "tu_1" || r0["is_error"] != false {
		t.Fatalf("tool_result[0] = %v", r0)
	}
	if r1["is_error"] != true || r1["content"] != "error: timeout" {
		t.Fatalf("hata sonucu is_error taşımalı: %v", r1)
	}
}

func TestParseAnthropicToolsChat(t *testing.T) {
	body := `{"content":[{"type":"text","text":"Bakıyorum. "},` +
		`{"type":"thinking","thinking":"gizli"},` +
		`{"type":"tool_use","id":"tu_9","name":"list_problems","input":{"status":"open"}},` +
		`{"type":"text","text":"Sonuç geldi."}],` +
		`"usage":{"input_tokens":7,"output_tokens":3}}`
	got, err := parseAnthropicToolsChat([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Text != "Bakıyorum. Sonuç geldi." {
		t.Fatalf("text = %q; text blokları birleşmeli, thinking ATLANMALI", got.Text)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "tu_9" ||
		string(got.ToolCalls[0].Input) != `{"status":"open"}` {
		t.Fatalf("tool çağrısı = %+v", got.ToolCalls)
	}
	if got.ToolCalls[0].Raw != nil {
		t.Fatalf("anthropic yolunda Raw nil kalmalı (tekrar oynatma openai'ye özgü): %s", got.ToolCalls[0].Raw)
	}
	if got.InputTokens != 7 || got.OutputTokens != 3 {
		t.Fatalf("usage = (%d,%d)", got.InputTokens, got.OutputTokens)
	}
}

// ─── GitHub ─────────────────────────────────────────────────────────

// TestChatGitHubTools_EndpointAndHeaders — gövde openai ile AYNI
// yazılıştan çıkar; ayrışan tek şey uç + entegrasyon header'ları
// (eksikse edge 403 döner).
func TestChatGitHubTools_EndpointAndHeaders(t *testing.T) {
	rt := &captureRT{}
	cfg := Config{BaseURL: "http://ignored.invalid/v1", APIKey: "sess-tok", HTTPClient: newCaptureClient(rt)}
	if _, err := ChatGitHubTools(context.Background(), cfg, ChatRequest{
		System: "sys", Messages: []ChatMessage{{Role: "user", Text: "q"}}, Tools: demoTools(),
	}); err != nil {
		t.Fatalf("ChatGitHubTools: %v", err)
	}
	if got := rt.reqs[0].URL.String(); got != githubChatURL {
		t.Fatalf("url = %q; want %q", got, githubChatURL)
	}
	h := rt.reqs[0].Header
	for k, want := range map[string]string{
		"Authorization":          "Bearer sess-tok",
		"Copilot-Integration-Id": "vscode-chat",
		"Editor-Version":         "vscode/1.85.0",
		"Editor-Plugin-Version":  "copilot-chat/0.12.0",
		"User-Agent":             "GithubCopilot/1.155.0",
	} {
		if got := h.Get(k); got != want {
			t.Fatalf("%s = %q; want %q", k, got, want)
		}
	}
	// Model varsayılanı Copilot'un kendi varsayılanı olmalı.
	if b := rt.bodies[0]; b["model"] != defaultGitHubModel {
		t.Fatalf("model = %v; want %q", b["model"], defaultGitHubModel)
	}
	// Gövde openai ile aynı şekli taşımalı (tools sarmalayıcısı dahil).
	tools, _ := rt.bodies[0]["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["type"] != "function" {
		t.Fatalf("github gövdesi openai tool şeklini taşımalı: %v", rt.bodies[0]["tools"])
	}
}

func TestChatToolsNilHTTPClientRejected(t *testing.T) {
	for name, fn := range map[string]func() error{
		"openai":    func() error { _, e := ChatOpenAITools(context.Background(), Config{}, ChatRequest{}); return e },
		"anthropic": func() error { _, e := ChatAnthropicTools(context.Background(), Config{}, ChatRequest{}); return e },
		"github":    func() error { _, e := ChatGitHubTools(context.Background(), Config{}, ChatRequest{}); return e },
	} {
		if err := fn(); err == nil {
			t.Fatalf("%s: nil HTTPClient sessizce kabul edildi", name)
		}
	}
}
