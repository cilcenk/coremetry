package anomaly

// v0.8.394 — AI audit A1 regression tests: the persisted deterministic
// root-cause hypothesis is fused into the problem explain prompt as a compact
// Turkish evidence block. Pins (a) the block's shape (suspect + reason, score,
// propagation path, deploy correlation, capped candidate list), (b) the
// hypothesis-absent path staying byte-identical to the pre-fusion prompt, and
// (c) fmtAgeTR across EVERY unit branch (the Nh/Nd unit-mixing rule).

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestHypothesisPromptBlockTR(t *testing.T) {
	full := &chstore.RootCauseHypothesis{
		AnchorKind: "problem",
		AnchorID:   "p1",
		Service:    "checkout",
		TopSuspect: "payment-db",
		TopScore:   0.78,
		Confidence: 0.71,
		Candidates: []chstore.ScoredCause{
			{Service: "payment-db", Score: 0.78, Hops: 2,
				Path:   []string{"checkout", "payment", "payment-db"},
				Reason: "fresh deploy 4m before onset"},
			{Service: "payment", Score: 0.31, Hops: 1},
			{Service: "auth", Score: 0.12, Hops: 1, Reason: "co-firing problem"},
			{Service: "ledger", Score: 0.08, Hops: 2},
			{Service: "cart", Score: 0.05, Hops: 3},
		},
		RecentDeploy: &chstore.RecentDeploy{Version: "v2.3.1", AgeSeconds: 240},
	}

	tests := []struct {
		name    string
		h       *chstore.RootCauseHypothesis
		want    []string // substrings that MUST appear
		notWant []string // substrings that must NOT appear
		empty   bool     // expect ""
	}{
		{
			name: "full hypothesis renders every section",
			h:    full,
			want: []string{
				"KÖK-NEDEN HİPOTEZİ",
				"Baş şüpheli: payment-db (skor 0.78, güven 0.71) — fresh deploy 4m before onset",
				"Yayılım yolu: checkout → payment → payment-db (2 hop)",
				"Deploy korelasyonu: v2.3.1, problem açılmadan 4dk önce",
				"Diğer adaylar: payment (skor 0.31, 1-hop); auth (skor 0.12, 1-hop) — co-firing problem; ledger (skor 0.08, 2-hop)",
			},
			// candidate list capped at maxHypothesisCandidates (3) — the 4th
			// non-suspect candidate must be dropped.
			notWant: []string{"cart"},
		},
		{
			name:  "nil hypothesis renders nothing",
			h:     nil,
			empty: true,
		},
		{
			name: "no clear suspect renders nothing",
			h: &chstore.RootCauseHypothesis{
				AnchorKind: "problem", AnchorID: "p2",
				TopSuspect: "", Confidence: 0.4,
			},
			empty: true,
		},
		{
			name: "suspect without candidates/deploy renders only the headline",
			h: &chstore.RootCauseHypothesis{
				AnchorKind: "problem", AnchorID: "p3",
				TopSuspect: "cart", TopScore: 0.5, Confidence: 0.3,
			},
			want:    []string{"Baş şüpheli: cart (skor 0.50, güven 0.30)"},
			notWant: []string{"Yayılım yolu", "Deploy korelasyonu", "Diğer adaylar"},
		},
		{
			name: "single-hop path (len 1) is not rendered as a propagation line",
			h: &chstore.RootCauseHypothesis{
				AnchorKind: "problem", AnchorID: "p4",
				TopSuspect: "db", TopScore: 0.6, Confidence: 0.5,
				Candidates: []chstore.ScoredCause{
					{Service: "db", Score: 0.6, Hops: 1, Path: []string{"db"}},
				},
			},
			notWant: []string{"Yayılım yolu"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HypothesisPromptBlockTR(tt.h)
			if tt.empty {
				if got != "" {
					t.Fatalf("want empty block, got %q", got)
				}
				return
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("block missing %q\nblock:\n%s", w, got)
				}
			}
			for _, nw := range tt.notWant {
				if strings.Contains(got, nw) {
					t.Errorf("block must not contain %q\nblock:\n%s", nw, got)
				}
			}
		})
	}
}

// TestFmtAgeTR exercises EVERY unit branch — the recurring value+unit
// template bug class (Nh/Nd, ms/s): an untested off-axis branch breaks
// silently.
func TestFmtAgeTR(t *testing.T) {
	tests := []struct {
		sec  int64
		want string
	}{
		{0, "0sn"},
		{-30, "0sn"}, // clock skew clamps, never renders a negative age
		{59, "59sn"},
		{60, "1dk"},
		{240, "4dk"},
		{3599, "59dk"},
		{3600, "1sa"},
		{7200, "2sa"},
		{8100, "2sa 15dk"},
	}
	for _, tt := range tests {
		if got := fmtAgeTR(tt.sec); got != tt.want {
			t.Errorf("fmtAgeTR(%d) = %q, want %q", tt.sec, got, tt.want)
		}
	}
}

// TestBuildProblemPromptHypothesisFusion pins the fusion contract: hypothesis
// present → the Turkish evidence block sits between the rule facts and the
// Phase 7 evidence; hypothesis absent (nil OR no clear suspect) → the prompt
// is byte-identical to the pre-fusion shape.
func TestBuildProblemPromptHypothesisFusion(t *testing.T) {
	p := chstore.Problem{
		ID: "p1", RuleName: "High error rate", Service: "checkout",
		Severity: "critical", Metric: "error_rate",
		Value: 0.14, Threshold: 0.05, StartedAt: 1_700_000_000_000_000_000,
	}
	bundle := EvidenceBundle{Problem: p} // confidence 0/1 → no evidence section
	hyp := &chstore.RootCauseHypothesis{
		AnchorKind: "problem", AnchorID: "p1",
		TopSuspect: "payment-db", TopScore: 0.78, Confidence: 0.71,
	}

	without := buildProblemPrompt(p, bundle, nil)
	if strings.Contains(without, "KÖK-NEDEN HİPOTEZİ") {
		t.Fatal("nil hypothesis must not render the hypothesis block")
	}

	with := buildProblemPrompt(p, bundle, hyp)
	if !strings.Contains(with, "KÖK-NEDEN HİPOTEZİ") {
		t.Fatal("hypothesis present but block missing from prompt")
	}
	if !strings.Contains(with, "Baş şüpheli: payment-db") {
		t.Fatalf("suspect missing from prompt:\n%s", with)
	}
	// Rule facts still lead the prompt (wire shape for the model unchanged).
	if !strings.HasPrefix(with, "Rule: High error rate\n") {
		t.Fatalf("prompt must start with the rule facts:\n%s", with)
	}

	// No-clear-suspect hypothesis degrades to the pre-fusion prompt.
	noSuspect := buildProblemPrompt(p, bundle, &chstore.RootCauseHypothesis{Confidence: 0.4})
	if noSuspect != without {
		t.Fatalf("no-suspect hypothesis must be byte-identical to nil-hypothesis prompt\nwith: %q\nwithout: %q", noSuspect, without)
	}
}
