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
		"systemServiceCharts":      systemServiceCharts,
		"systemRunbook":            systemRunbook,
		"systemCompareTraces":      systemCompareTraces,
		"systemDeployImpact":       systemDeployImpact,
		"systemSLOBurn":            systemSLOBurn,
		"systemSlowQuery":          systemSlowQuery,
		"systemRootCauseNarration": systemRootCauseNarration,
		// v0.9.831 — kod bağlamlı ikizler. Ek, direktifin ÖNÜNE
		// giriyor; direktif her iki varyantta da SON cümle kalmalı.
		"systemTraceCode":     systemTraceCode,
		"systemExceptionCode": systemExceptionCode,
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

// TestCodePromptsExtendBaseVerbatim — v0.9.831. Kod varyantı, kodsuz
// prompt'un ÜSTÜNE ek yapar; tabanı yeniden yazmaz.
//
// Neden pin: iki yüzey (kodlu/kodsuz) aynı soruyu cevaplıyor ve
// aralarındaki tek fark kod olmalı. Taban metin kopyalanıp
// ayrışırsa operatör aynı exception'da kutuyu işaretleyip
// işaretlememesine göre bambaşka bir cevap alır ve nedenini
// göremez.
func TestCodePromptsExtendBaseVerbatim(t *testing.T) {
	cases := []struct {
		name, body, code string
	}{
		{"exception", systemExceptionBody, systemExceptionCode},
		{"trace", systemTraceBody, systemTraceCode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.HasPrefix(tc.code, tc.body) {
				t.Fatalf("%s kod prompt'u tabandan başlamıyor", tc.name)
			}
			if !strings.Contains(tc.code, systemCodeAddendum) {
				t.Fatalf("%s kod prompt'unda kod eki yok", tc.name)
			}
			if !strings.HasSuffix(tc.code, AnswerInTurkish) {
				t.Fatalf("%s kod prompt'u dil direktifiyle bitmiyor", tc.name)
			}
			// Direktif TEK kez: gövde + ek + direktif, gövdenin kendi
			// direktifi değil.
			if n := strings.Count(tc.code, AnswerInTurkish); n != 1 {
				t.Fatalf("%s dil direktifi %d kez geçiyor, 1 olmalı", tc.name, n)
			}
		})
	}
	// Kodsuz prompt'lar kod ekini TAŞIMAMALI — kodsuz istekte
	// "gördüğün kod" talimatı vermek modele olmayan bir kanıt
	// vaat eder.
	for name, p := range map[string]string{"systemException": systemException, "systemTrace": systemTrace} {
		if strings.Contains(p, "KOD BAĞLAMI") {
			t.Errorf("%s kodsuz olmasına rağmen kod eki taşıyor", name)
		}
	}
}
