package copilot

import (
	"strings"
	"testing"
)

// v0.8.374 — operator decision ("hepsi Türkçe"): every PROSE copilot
// surface carries the shared Turkish directive; the AI-analysis panel
// already had its own Turkish prompt, so Explain answering in English
// was the inconsistency. Strict-JSON surfaces are pinned to NOT carry
// it — a language directive invites prose around machine-parsed
// output (systemNLToQuery emits DSL JSON, systemCHQueryOptimize and
// systemServiceTags emit structured suggestions).
func TestProsePromptsAnswerInTurkish(t *testing.T) {
	prose := map[string]string{
		"systemTrace":              systemTrace,
		"systemSpan":               systemSpan,
		"systemProblem":            systemProblem,
		"systemException":          systemException,
		"systemIncident":           systemIncident,
		"systemAnomaly":            systemAnomaly,
		"systemServiceHealth":      systemServiceHealth,
		"systemRunbook":            systemRunbook,
		"systemCompareTraces":      systemCompareTraces,
		"systemDeployImpact":       systemDeployImpact,
		"systemSLOBurn":            systemSLOBurn,
		"systemSlowQuery":          systemSlowQuery,
		"systemRootCauseNarration": systemRootCauseNarration,
	}
	for name, p := range prose {
		if !strings.HasSuffix(p, AnswerInTurkish) {
			t.Errorf("%s must end with the Turkish directive", name)
		}
	}
	for name, p := range map[string]string{
		"systemNLToQuery":       systemNLToQuery,
		"systemCHQueryOptimize": systemCHQueryOptimize,
		"systemServiceTags":     systemServiceTags,
	} {
		if strings.Contains(p, "Türkçe") {
			t.Errorf("%s is a structured-output prompt and must NOT carry the language directive", name)
		}
	}
}
