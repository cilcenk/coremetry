package api

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/ai/agent/blocks"
	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/mcptools"
)

// v0.10.557 — CoSRE Faz 4c: kök-neden rotasının kanıt genişlemesi. Saf yarılar:
// desen süzgeci (ana servis YA DA topServices), desen metni, yapısal kanıt bloğu
// (tavanlar + verdict), evidence → block dönüşümü TEK sıralayıcıdan.
func TestFilterGuidedPatternsAndRender(t *testing.T) {
	pats := []anomaly.LogPatternAnomaly{
		{Pattern: "a", CurrentCount: 3, Service: "x", Kind: "new"},
		{Pattern: "b", CurrentCount: 9, Service: "y", Kind: "spike", TopServices: []logstore.PatternServiceHit{{Service: "x"}}, BaselineCount: 3, Ratio: 3},
		{Pattern: "c", CurrentCount: 1, Service: "z"},
	}
	got := filterGuidedPatterns(pats, "x", 5)
	if len(got) != 2 || got[0].Pattern != "b" || got[1].Pattern != "a" {
		t.Fatalf("süzgeç/sıra: %+v", got)
	}
	if len(filterGuidedPatterns(pats, "", 1)) != 1 {
		t.Fatal("tavan")
	}
	txt := renderLogPatternsTR(got, "x", 1800)
	for _, w := range []string{"Log desenleri", "- b ×9 (spike, baseline 3, ×3.0)", "- a ×3 (new)"} {
		if !strings.Contains(txt, w) {
			t.Errorf("metin %q taşımalı:\n%s", w, txt)
		}
	}
	if !strings.Contains(renderLogPatternsTR(nil, "x", 60), "desen yok") {
		t.Fatal("boş desen dürüst")
	}
}

func TestGuidedEvidencePayload(t *testing.T) {
	cx := &aiServiceContext{Current: aiRED{Spans: 10, ErrorRate: 12.5, P95Ms: 900}, Baseline: aiRED{Spans: 9, ErrorRate: 1, P95Ms: 120}}
	probs := make([]chstore.Problem, 0, 7)
	for i := 0; i < 7; i++ {
		probs = append(probs, chstore.Problem{ID: "p", RuleName: "r"})
	}
	probs[1].RootCause = &chstore.RootCauseSummary{TopSuspect: "ledger", Confidence: 0.82}
	changes := make([]mcptools.ChangeRow, 10)
	for i := range changes {
		changes[i] = mcptools.ChangeRow{Source: "rollout", Workload: "w"}
	}
	out := guidedEvidencePayload("svc", 3600, cx, probs, changes, []anomaly.LogPatternAnomaly{{Pattern: "x", Kind: "new", CurrentCount: 2}})
	if out["service"] != "svc" || len(out["problems"].([]map[string]any)) != 5 || len(out["changes"].([]map[string]any)) != 8 || len(out["logPatterns"].([]map[string]any)) != 1 {
		t.Fatalf("tavanlar: %+v", out)
	}
	if v := out["verdict"].(string); !strings.Contains(v, "ledger") || !strings.Contains(v, "0.82") {
		t.Fatalf("verdict: %s", v)
	}
	red := out["red"].(map[string]any)["current"].(map[string]any)
	if red["errorRate"] != 12.5 || red["p95Ms"] != float64(900) {
		t.Fatalf("red: %+v", red)
	}
	if v := guidedEvidencePayload("svc", 60, nil, nil, nil, nil)["verdict"].(string); !strings.Contains(v, "hipotez yok") {
		t.Fatalf("hipotezsiz verdict: %s", v)
	}
}

func TestWithBlockSeqSharesSequence(t *testing.T) {
	var got []struct {
		kind    string
		payload any
	}
	var seq blocks.Sequencer
	emit := withBlockSeq(func(k string, p any) {
		got = append(got, struct {
			kind    string
			payload any
		}{k, p})
	}, &seq)
	emit("block", seq.Next(blocks.TypeChart, map[string]any{"a": 1}))
	emit("evidence", map[string]any{"verdict": "v"})
	emit("step", map[string]any{"tool": "x"})
	if len(got) != 3 || got[0].kind != "block" || got[1].kind != "block" || got[2].kind != "step" {
		t.Fatalf("kinds: %+v", got)
	}
	b1 := got[0].payload.(map[string]any)
	b2 := got[1].payload.(map[string]any)
	if b1["id"] != "b1" || b2["id"] != "b2" || b2["type"] != blocks.TypeEvidence {
		t.Fatalf("tek sıralayıcı: %v %v", b1["id"], b2["id"])
	}
	// seq nil → evidence olduğu gibi geçer (harici yol), sessiz düşme yok.
	var raw []string
	withBlockSeq(func(k string, _ any) { raw = append(raw, k) }, nil)("evidence", nil)
	if len(raw) != 1 || raw[0] != "evidence" {
		t.Fatal("nil seq: olay geçmeli")
	}
}

// Kaynak pini: kök-neden demeti iki yeni adımı ve kanıt bloğunu yayar; chat
// tek sıralayıcı kurar.
func TestRootCauseBundleEmitsEvidence(t *testing.T) {
	src, err := os.ReadFile("copilot_guided.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "func (s *Server) guidedRootCauseBundle(")
	j := strings.Index(body[i:], "\nfunc ")
	fn := body[i : i+j]
	for _, w := range []string{`emitGuidedStep(emit, "list_deployments"`, `emitGuidedStep(emit, "log_patterns"`, `emit("evidence", guidedEvidencePayload(`} {
		if !strings.Contains(fn, w) {
			t.Errorf("kök-neden demeti %q taşımalı", w)
		}
	}
	chat, _ := os.ReadFile("copilot_chat.go")
	if strings.Count(string(chat), "var blockSeq blocks.Sequencer") != 1 || !strings.Contains(string(chat), "emit = withBlockSeq(emit, &blockSeq)") {
		t.Fatal("chat tek blockSeq + withBlockSeq sarmalı")
	}
}
