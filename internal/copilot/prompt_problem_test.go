package copilot

// v0.8.394 — AI audit A1: systemProblem moved from an English instruction to
// the Türkçe-native analyze-service pattern (single-shot + ONE few-shot +
// fixed plain-text section labels) and now teaches the model to trust the
// deterministic "KÖK-NEDEN HİPOTEZİ" block as primary evidence. Pins:
//   - exactly ONE Turkish language directive (the appended AnswerInTurkish —
//     the Türkçe-native body must not double it),
//   - the hypothesis-trust instruction + few-shot are present,
//   - the fixed output sections the small-model target (qwen3.5-2b) needs
//     shown rather than described,
//   - plain-text output contract (renderers are pre-wrap text, not JSON).

import (
	"strings"
	"testing"
)

func TestSystemProblemPrompt(t *testing.T) {
	p := SystemPromptProblem()

	// One language directive only — AnswerInTurkish is appended once; a
	// second in-body "Türkçe yanıt ver" would double-instruct the model.
	if got := strings.Count(p, "Türkçe yanıt ver"); got != 1 {
		t.Errorf("systemProblem must carry the Turkish directive exactly once, found %d", got)
	}
	if !strings.HasSuffix(p, AnswerInTurkish) {
		t.Error("systemProblem must end with the shared AnswerInTurkish directive")
	}

	for _, want := range []string{
		// hypothesis fusion: the deterministic block is primary evidence
		"KÖK-NEDEN HİPOTEZİ",
		"BİRİNCİL kanıt",
		"yeniden tahmin ETME",
		// analyze-service pattern: one few-shot, shown not described
		"ÖRNEK GİRDİ:",
		"ÖRNEK ÇIKTI:",
		"ÇIKTI FORMATI:",
		// fixed plain-text sections (wire format: pre-wrap text, not JSON)
		"Olası neden:",
		"Kanıt:",
		"İlk kontroller:",
		"DÜZ METİN",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("systemProblem missing %q", want)
		}
	}

	// Exactly ONE few-shot — a second example would bloat the 2B context for
	// no accuracy gain.
	if got := strings.Count(p, "ÖRNEK GİRDİ:"); got != 1 {
		t.Errorf("systemProblem must carry exactly one few-shot input, found %d", got)
	}
}
