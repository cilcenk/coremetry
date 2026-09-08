package mcptools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.555 — Faz 4a problem-tarafı araçları. Saf yarılar: problemView sınırları,
// evidenceSection bölüm sözleşmesi (bilinmeyen = ok=false; tavan 10 + toplam),
// capabilitiesFrom (nil kapanış "unknown" ilan eder; VM adı → victoriametrics).
func TestProblemViewBounds(t *testing.T) {
	resolved := int64(1_700_000_600_000_000_000)
	p := chstore.Problem{ID: "p1", RuleID: "r", Service: "svc", Status: "resolved", StartedAt: 1_700_000_000_000_000_000,
		ResolvedAt: &resolved, Description: strings.Repeat("x", 700), Pod: "pod-1", Clusters: []string{"c1"}}
	v := problemView(p)
	if d := v["description"].(string); len([]rune(d)) != problemDescriptionMax+1 || !strings.HasSuffix(d, "…") {
		t.Fatalf("açıklama kırpılmalı: %d", len([]rune(d)))
	}
	if v["durationS"] != int64(600) || v["pod"] != "pod-1" {
		t.Fatalf("durationS/pod: %+v", v)
	}
	if _, ok := v["assignee"]; ok {
		t.Fatal("boş alan yazılmaz")
	}
}

func TestEvidenceSectionContract(t *testing.T) {
	de := &chstore.DeepEvidence{}
	for i := 0; i < 15; i++ {
		de.TraceIDs = append(de.TraceIDs, "t")
		de.Checked = append(de.Checked, chstore.CheckedSignal{Family: "f"})
	}
	v, n, ok := evidenceSection(de, "traceIds")
	if !ok || n != 15 || len(v.([]string)) != evidenceSectionCap {
		t.Fatalf("tavan/toplam: ok=%v n=%d", ok, n)
	}
	if _, _, ok := evidenceSection(de, "nope"); ok {
		t.Fatal("bilinmeyen bölüm ok=false olmalı")
	}
	if _, n, ok := evidenceSection(nil, "rollouts"); !ok || n != 0 {
		t.Fatal("nil deep → boş bölüm, hata değil")
	}
	for _, s := range evidenceSections {
		if _, _, ok := evidenceSection(de, s); !ok {
			t.Errorf("enum'daki %s bölümü çözülmüyor", s)
		}
	}
}

func TestCapabilitiesFrom(t *testing.T) {
	caps := capabilitiesFrom(Deps{})
	for _, k := range []string{"entity", "rollouts", "thanos", "rag"} {
		if c := caps[k]; c.Enabled || !strings.Contains(c.Reason, "unknown") {
			t.Errorf("%s: probe yokken unknown ilan edilmeli: %+v", k, c)
		}
	}
	d := Deps{
		EntityEnabled: func() bool { return true }, RolloutsEnabled: func() bool { return false },
		Clusters:    func() []ClusterRef { return []ClusterRef{{ID: "a", Name: "prod-eu"}} },
		MetricsName: func() string { return "vm" }, RAGReady: func() bool { return true },
		CopilotModel: func() string { return "gemma" },
	}
	caps = capabilitiesFrom(d)
	if !caps["entity"].Enabled || caps["rollouts"].Enabled || !caps["thanos"].Enabled || !strings.Contains(caps["thanos"].Detail, "prod-eu") ||
		!caps["victoriametrics"].Enabled || !caps["rag"].Enabled || !caps["copilot"].Enabled || caps["chatContext"].Enabled {
		t.Fatalf("yetenekler: %+v", caps)
	}
	tool := toolByName(t, ToolList(d), "get_capabilities")
	out, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if len(m["enabled"].([]string)) == 0 || len(m["disabled"].([]string)) == 0 {
		t.Fatalf("enabled/disabled listeleri: %+v", m)
	}
}

func TestProblemToolsRegisteredWithSchema(t *testing.T) {
	tools := ToolList(Deps{})
	for _, name := range []string{"get_problem", "get_correlation_evidence", "similar_problems", "get_capabilities"} {
		tool := toolByName(t, tools, name)
		if tool.ShortDescription == "" || tool.Description == "" {
			t.Errorf("%s: açıklama boş", name)
		}
	}
	ev := toolByName(t, tools, "get_correlation_evidence")
	props := ev.InputSchema["properties"].(map[string]any)
	items := props["sections"].(map[string]any)["items"].(map[string]any)
	if got := items["enum"].([]string); len(got) != len(evidenceSections) {
		t.Fatalf("sections enum katalogla aynı olmalı: %v", got)
	}
	if _, err := ev.Handler(context.Background(), json.RawMessage(`{"problem_id":"x","sections":["nope"]}`)); err == nil || !strings.Contains(err.Error(), "unknown section") {
		t.Fatalf("bilinmeyen bölüm hata vermeli: %v", err)
	}
	sim := toolByName(t, tools, "similar_problems")
	if _, err := sim.Handler(context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "problem_id") {
		t.Fatalf("anahtar yokken hata: %v", err)
	}
}
