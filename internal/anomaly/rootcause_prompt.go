package anomaly

import (
	"fmt"
	"strings"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.8.394 — AI audit A1: fuse the persisted root-cause hypothesis into the
// problem explain prompts. The RootCauseSynthesizer worker (rootcause_worker.go)
// already computes + persists a deterministic, LLM-free root-cause verdict per
// problem (correlator.Synthesize) — but until now the LLM narrated blind while
// that verdict sat next to it, only feeding the /problems ribbon. This file
// renders the hypothesis into a compact Turkish evidence block the model is
// instructed to TRUST as primary evidence (systemProblem, copilot.go) instead
// of re-guessing the suspect.
//
// Pure formatting only — no store, no ctx — so both the background
// ProblemExplainer and the operator-clicked /api/copilot/explain-problem
// handler inject the exact same block, and the shape is table-testable
// (rootcause_prompt_test.go).

// maxHypothesisCandidates caps the "Diğer adaylar" line so a wide candidate
// list can't blow the small-model token budget (same posture as
// maxEvidenceItems in fusion.go).
const maxHypothesisCandidates = 3

// HypothesisPromptBlockTR renders one persisted hypothesis as the Turkish
// evidence block the problem prompts carry. Returns "" for a nil hypothesis or
// one without a clear top suspect (a synthesized "no clear cause" row adds
// noise, not signal) — so a caller can unconditionally append the result and
// hypothesis-absent behaviour stays byte-identical to the pre-fusion prompt.
func HypothesisPromptBlockTR(h *chstore.RootCauseHypothesis) string {
	if h == nil || h.TopSuspect == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\nKÖK-NEDEN HİPOTEZİ (deterministik korelasyon motoru — BİRİNCİL kanıt):\n")

	// Top suspect + the fuser's human-readable "why this rank" reason, when the
	// candidate list carries it.
	var top *chstore.ScoredCause
	for i := range h.Candidates {
		if h.Candidates[i].Service == h.TopSuspect {
			top = &h.Candidates[i]
			break
		}
	}
	line := fmt.Sprintf("- Baş şüpheli: %s (skor %.2f, güven %.2f)", h.TopSuspect, h.TopScore, h.Confidence)
	if top != nil && top.Reason != "" {
		line += " — " + top.Reason
	}
	sb.WriteString(line + "\n")

	// Propagation path (anchor → … → suspect) when the scorer recorded one.
	if top != nil && len(top.Path) > 1 {
		fmt.Fprintf(&sb, "- Yayılım yolu: %s (%d hop)\n", strings.Join(top.Path, " → "), top.Hops)
	}

	// Deploy correlation — the "what changed" signal the fuser weighted.
	if h.RecentDeploy != nil {
		fmt.Fprintf(&sb, "- Deploy korelasyonu: %s, problem açılmadan %s önce\n",
			h.RecentDeploy.Version, fmtAgeTR(h.RecentDeploy.AgeSeconds))
	}

	// Remaining ranked candidates, capped — enough for the model to mention
	// alternatives without re-ranking them.
	others := make([]string, 0, maxHypothesisCandidates)
	for _, c := range h.Candidates {
		if c.Service == "" || c.Service == h.TopSuspect {
			continue
		}
		label := fmt.Sprintf("%s (skor %.2f", c.Service, c.Score)
		if c.Hops > 0 {
			label += fmt.Sprintf(", %d-hop", c.Hops)
		}
		label += ")"
		if c.Reason != "" {
			label += " — " + c.Reason
		}
		others = append(others, label)
		if len(others) >= maxHypothesisCandidates {
			break
		}
	}
	if len(others) > 0 {
		fmt.Fprintf(&sb, "- Diğer adaylar: %s\n", strings.Join(others, "; "))
	}
	return sb.String()
}

// fmtAgeTR renders a deploy-to-onset age in compact Turkish units. Every unit
// branch is exercised by TestFmtAgeTR (the Nh/Nd unit-mixing rule: templates
// with unit branches test EVERY unit at ship time). Negative ages (clock skew —
// deploy stamped after onset) clamp to 0 rather than rendering "-30sn önce".
func fmtAgeTR(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	switch {
	case seconds < 60:
		return fmt.Sprintf("%dsn", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%ddk", seconds/60)
	default:
		h := seconds / 3600
		m := (seconds % 3600) / 60
		if m == 0 {
			return fmt.Sprintf("%dsa", h)
		}
		return fmt.Sprintf("%dsa %ddk", h, m)
	}
}
