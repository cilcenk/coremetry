package correlator

// node_causes_test.go — v0.10.94, dikey eksen dilim ②.

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func pl(svc, node string) chstore.RunsOnPlacement {
	return chstore.RunsOnPlacement{Service: svc, Node: node}
}

func TestRankNodeCauses(t *testing.T) {
	t.Run("alarmda komşu → node aday, skor oran", func(t *testing.T) {
		got := RankNodeCauses([]chstore.RunsOnPlacement{
			pl("checkout", "w1"), pl("payments", "w1"), pl("catalog", "w1"),
		}, "checkout", map[string]bool{"payments": true})
		if len(got) != 1 {
			t.Fatalf("aday=%d, 1 bekleniyordu: %+v", len(got), got)
		}
		c := got[0]
		if c.Service != "node:w1" || c.Kind != chstore.NodeKindNode {
			t.Errorf("kimlik/tür yanlış: %+v", c)
		}
		// 2 kiracı komşudan 1'i alarmda → 0.5.
		if c.Score != 0.5 || c.Hops != 1 {
			t.Errorf("skor/hop: %+v", c)
		}
		if len(c.Path) != 2 || c.Path[0] != "checkout" || c.Path[1] != "node:w1" {
			t.Errorf("path: %+v", c.Path)
		}
	})
	t.Run("tek kiracılı node aday olmaz", func(t *testing.T) {
		if got := RankNodeCauses([]chstore.RunsOnPlacement{pl("checkout", "w1")},
			"checkout", map[string]bool{"checkout": true}); len(got) != 0 {
			t.Errorf("tek kiracı aday üretti: %+v", got)
		}
	})
	t.Run("komşu alarmda değilse aday olmaz", func(t *testing.T) {
		if got := RankNodeCauses([]chstore.RunsOnPlacement{
			pl("checkout", "w1"), pl("payments", "w1")},
			"checkout", nil); len(got) != 0 {
			t.Errorf("alarmsız yerleşim aday üretti: %+v", got)
		}
	})
	t.Run("tetikleyenin olmadığı node görünmez", func(t *testing.T) {
		if got := RankNodeCauses([]chstore.RunsOnPlacement{
			pl("payments", "w9"), pl("catalog", "w9")},
			"checkout", map[string]bool{"payments": true}); len(got) != 0 {
			t.Errorf("yabancı node aday oldu: %+v", got)
		}
	})
	t.Run("deterministik sıra: skor desc, ad asc", func(t *testing.T) {
		placements := []chstore.RunsOnPlacement{
			pl("checkout", "a"), pl("s1", "a"), pl("s2", "a"), // a: 1/2
			pl("checkout", "b"), pl("s3", "b"), // b: 1/1
			pl("checkout", "c"), pl("s4", "c"), // c: 1/1 — b ile eşit, ad sırası
		}
		firing := map[string]bool{"s1": true, "s3": true, "s4": true}
		for i := 0; i < 10; i++ { // map sırası sızarsa yakalansın
			got := RankNodeCauses(placements, "checkout", firing)
			if len(got) != 3 || got[0].Service != "node:b" ||
				got[1].Service != "node:c" || got[2].Service != "node:a" {
				t.Fatalf("sıra deterministik değil: %+v", got)
			}
		}
	})
}

// TestSynthesizeCarriesNodeKind — Kind kopya SİTESİNDEN geçer ve node
// adayının reason'ı hata-payı cümlesi DEĞİL yerleşim cümlesidir (skor
// farklı bir oranı ölçüyor; yanlış cümle yanlış kanıt iddiası olurdu).
func TestSynthesizeCarriesNodeKind(t *testing.T) {
	h := Synthesize("anomaly", "a1", "checkout", 42, SynthesisInput{
		Neighbours: []ScoredCause{
			{Service: "payments", Score: 0.8, Hops: 1, Path: []string{"checkout", "payments"}},
			{Service: "node:w1", Score: 0.5, Hops: 1,
				Path: []string{"checkout", "node:w1"}, Kind: chstore.NodeKindNode},
		},
	})
	var node, call *chstore.ScoredCause
	for i := range h.Candidates {
		switch h.Candidates[i].Service {
		case "node:w1":
			node = &h.Candidates[i]
		case "payments":
			call = &h.Candidates[i]
		}
	}
	if node == nil || call == nil {
		t.Fatalf("adaylar eksik: %+v", h.Candidates)
	}
	if node.Kind != chstore.NodeKindNode || call.Kind != "" {
		t.Errorf("Kind kopyası yanlış: node=%q call=%q", node.Kind, call.Kind)
	}
	if !strings.Contains(node.Reason, "same-node placement") ||
		!strings.Contains(node.Reason, "co-tenant") {
		t.Errorf("node reason yerleşimi anlatmıyor: %q", node.Reason)
	}
	if strings.Contains(node.Reason, "error share") {
		t.Errorf("node reason hata-payı cümlesi taşıyor — skor o oranı ölçmüyor: %q", node.Reason)
	}
}
