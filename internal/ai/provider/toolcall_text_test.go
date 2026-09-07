package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// v0.10.545 — metin-gömülü tool çağrısı geri düşüşü: biçimler + kenar durumlar.
func argsOf(t *testing.T, c ToolCall) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(c.Input, &m); err != nil {
		t.Fatalf("args JSON değil: %s (%v)", c.Input, err)
	}
	return m
}

func TestParseTextToolCalls(t *testing.T) {
	known := []string{"resolve_entity", "search_traces"}
	cases := []struct {
		name    string
		in      string
		wantN   int
		first   string
		rest    string
		unicode string
	}{
		{"hermes", `<tool_call>{"name":"resolve_entity","arguments":{"text":"ödeme-servisi"}}</tool_call>`, 1, "resolve_entity", "", "ödeme-servisi"},
		{"özel token + düşünce", "<|channel>thought düşünüyorum<channel|><|tool_call|>{\"name\":\"search_traces\",\"arguments\":\"{\\\"service\\\":\\\"api\\\",\\\"filters\\\":[{\\\"key\\\":\\\"şube\\\",\\\"op\\\":\\\"=\\\",\\\"value\\\":\\\"İstanbul\\\"}]}\"}<|/tool_call|>", 1, "search_traces", "", "İstanbul"},
		{"json çiti + metin", "Servisi çözüyorum.\n```json\n{\"name\":\"resolve_entity\",\"parameters\":{\"text\":\"checkout\"}}\n```", 1, "resolve_entity", "Servisi çözüyorum.", "checkout"},
		{"tool_code pythonic", "```tool_code\nresolve_entity(text='checkout')\n```", 1, "resolve_entity", "", "checkout"},
		{"çıplak json dizisi (iki çağrı, seri)", `[{"name":"resolve_entity","arguments":{"text":"a"}},{"name":"resolve_entity","arguments":{"text":"b"}}]`, 2, "resolve_entity", "", "a"},
		{"pythonic iç içe", `search_traces(service="api", limit=20, range={"from":"2026-09-08T09:00:00Z","to":"2026-09-08T09:15:00Z"}, flags=[1,2], on=True)`, 1, "search_traces", "", "api"},
		{"function sarmalı", `{"function":{"name":"resolve_entity","arguments":"{\"text\":\"x\"}"}}`, 1, "resolve_entity", "", "x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls, rest, ok := ParseTextToolCalls(c.in, known)
			if !ok || len(calls) != c.wantN || calls[0].Name != c.first {
				t.Fatalf("ok=%v calls=%+v", ok, calls)
			}
			if strings.TrimSpace(rest) != c.rest {
				t.Errorf("rest %q, want %q", rest, c.rest)
			}
			blob := string(calls[0].Input)
			if !strings.Contains(blob, c.unicode) {
				t.Errorf("unicode/değer korunmadı: %s", blob)
			}
			if calls[0].ID == "" || !json.Valid(calls[0].Input) {
				t.Errorf("id/args: %+v", calls[0])
			}
		})
	}
	// pythonic iç içe değer tipleri
	calls, _, _ := ParseTextToolCalls(`search_traces(service="api", limit=20, range={"from":"a","to":"b"}, flags=[1,2], on=True)`, known)
	a := argsOf(t, calls[0])
	if a["limit"] != float64(20) || a["on"] != true || a["range"].(map[string]any)["to"] != "b" || len(a["flags"].([]any)) != 2 {
		t.Fatalf("pythonic tipler: %v", a)
	}
}

func TestParseTextToolCallsRejects(t *testing.T) {
	known := []string{"resolve_entity"}
	for _, in := range []string{
		"Sonuç: 3 servis bulundu.",                                   // düz metin
		`<tool_call>{"name":"resolve_entity","arguments":{"text":"a`, // kesik JSON (finish_reason=length)
		`{"name":"rm_rf","arguments":{}}`,                            // bilinmeyen ad (known verildi)
		"Örnek biçim şöyledir: {\"name\": \"resolve_entity\"} gibi.", // satır ortası, çağrı değil
		"",
	} {
		if calls, rest, ok := ParseTextToolCalls(in, known); ok || len(calls) != 0 || rest != in {
			t.Errorf("%q: ok=%v calls=%v rest=%q", in, ok, calls, rest)
		}
	}
	// known boş → her geçerli ad kabul (Executor "unknown tool" der)
	if calls, _, ok := ParseTextToolCalls(`{"name":"anything","arguments":{}}`, nil); !ok || calls[0].Name != "anything" {
		t.Fatal("known boşken ad süzgeci yok")
	}
}
