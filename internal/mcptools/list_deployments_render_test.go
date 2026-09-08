package mcptools

import (
	"strings"
	"testing"
)

// v0.10.557 — RenderChangesTR / ChangesOf: guided rota için kompakt metin;
// rollouts katmanı kapalıysa SÖYLENİR; 8 satır tavanı "+N daha".
func TestRenderChangesTR(t *testing.T) {
	rows := make([]ChangeRow, 0, 10)
	for i := 0; i < 10; i++ {
		rows = append(rows, ChangeRow{Source: "rollout", TimeISO: "2026-09-08T06:00:00Z", Workload: "api", Service: "api", Version: "v1.2", Status: "complete", Namespace: "pay"})
	}
	out := map[string]any{"rows": rows, "sources": map[string]any{"rollouts_layer": "on"}}
	if len(ChangesOf(out)) != 10 {
		t.Fatal("ChangesOf")
	}
	txt := RenderChangesTR(out)
	if !strings.Contains(txt, "- 2026-09-08T06:00:00Z [rollout] api → v1.2 · complete · ns pay") || !strings.Contains(txt, "+2 satır daha") {
		t.Fatalf("metin:\n%s", txt)
	}
	off := RenderChangesTR(map[string]any{"rows": []ChangeRow{}, "sources": map[string]any{"rollouts_layer": "disabled"}})
	if !strings.Contains(off, "değişiklik yok") || !strings.Contains(off, "Rollouts katmanı: disabled") {
		t.Fatalf("kapalı katman ilan edilmeli:\n%s", off)
	}
	if ChangesOf(map[string]any{}) != nil {
		t.Fatal("rows yoksa nil")
	}
}
