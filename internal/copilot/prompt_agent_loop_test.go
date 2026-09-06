package copilot

import (
	"strings"
	"testing"
)

// v0.10.482 — ajan çekirdek döngüsü: kataloğun gerçek tool adlarını anar,
// Ek A düzeltmeleri (N1 cluster attribute'tan; N5 OR yerine anahtar başına
// arama) uygulanmış; systemChat'i yeniden YAZMAZ (ayrı const, öne eklenir).
func TestChatAgentLoopPrompt(t *testing.T) {
	p := SystemPromptChatAgentLoop()
	for _, tool := range []string{"resolve_entity", "describe_attributes", "find_attribute_by_value", "search_traces", "trace_stats", "search_logs", "build_link", "set_context", "get_context"} {
		if !strings.Contains(p, tool) {
			t.Errorf("tool adı %q prompt'ta yok", tool)
		}
	}
	for _, bad := range []string{"prod-<cluster", "OR ile", " OR "} {
		if strings.Contains(p, bad) {
			t.Errorf("Ek A düzeltmesi uygulanmamış: %q", bad)
		}
	}
	if strings.Contains(SystemPromptChat(), "ÇEKİRDEK DÖNGÜ") {
		t.Error("çekirdek döngü systemChat'e gömülmemeli (ayrı const, öne eklenir)")
	}
	if !strings.Contains(p, "telemetri yok") || !strings.Contains(p, "hangi cluster") {
		t.Error("dürüstlük kuralları (telemetri yok ≠ workload yok; hangi cluster) prompt'ta olmalı")
	}
}
