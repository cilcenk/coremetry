package modelcaps

import "testing"

// v0.10.534 — tablo: aile/düşünme çözümü ve gövde anahtarı; bilinmeyen aile
// "off"u sessizce yutmaz (ok=false), varsayılan ayar gövdeye dokunmaz.
func TestForAndExtraBody(t *testing.T) {
	cases := []struct {
		model  string
		family Family
		reason bool
		sw     string
	}{
		{"Qwen3-8B", FamilyQwen3, true, SwitchChatTemplate},
		{"qwen3.5-2b-instruct", FamilyQwen3, true, SwitchChatTemplate},
		{"QwQ-32B", FamilyQwQ, true, ""},
		{"deepseek-r1:14b", FamilyDeepSeekR1, true, ""},
		{"gemma4", FamilyGemma, false, ""},
		{"meta-llama/Llama-3.1-8B", FamilyLlama, false, ""},
		{"mixtral-8x7b", FamilyMistral, false, ""},
		{"o3-mini", FamilyOSeries, true, ""},
		{"gpt-5-mini", FamilyGPT, true, ""},
		{"gpt-4o-mini", FamilyGPT, false, ""},
		{"claude-sonnet-5", FamilyClaude, false, ""},
		{"", FamilyUnknown, false, ""},
		{"my-finetune", FamilyUnknown, false, ""},
	}
	for _, c := range cases {
		got := For(c.model)
		if got.Family != c.family || got.Reasoning != c.reason || got.ThinkingSwitch != c.sw {
			t.Errorf("%q → %+v, want {%s %v %q}", c.model, got, c.family, c.reason, c.sw)
		}
	}
	if eb, ok := ExtraBody(For("qwen3-8b"), ThinkingOff); !ok || eb[SwitchChatTemplate].(map[string]any)["enable_thinking"] != false {
		t.Fatalf("qwen3 off: %v %v", eb, ok)
	}
	if eb, ok := ExtraBody(For("qwen3-8b"), ThinkingOn); !ok || eb[SwitchChatTemplate].(map[string]any)["enable_thinking"] != true {
		t.Fatalf("qwen3 on: %v %v", eb, ok)
	}
	if eb, ok := ExtraBody(For("gemma4"), ThinkingOff); ok || eb != nil {
		t.Fatalf("gemma anahtar bilmez: ok=false beklenir, %v %v", eb, ok)
	}
	if eb, ok := ExtraBody(For("qwen3-8b"), ThinkingDefault); !ok || eb != nil {
		t.Fatalf("varsayılan gövdeye dokunmaz: %v %v", eb, ok)
	}
	for _, s := range []string{"", "off", "on"} {
		if !ValidThinking(s) {
			t.Errorf("%q geçerli olmalı", s)
		}
	}
	if ValidThinking("auto") {
		t.Error("auto geçersiz")
	}
}
