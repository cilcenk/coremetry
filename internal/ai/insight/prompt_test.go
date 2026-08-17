package insight

import (
	"strings"
	"testing"
)

// prompt_test.go — v0.9.1129. Kartın prose yarısının KULLANICI prompt'u
// aynı kanıttan türüyor; testin işi "kartta gördüğün sayı modele de
// gitti mi" sorusunu kilitlemek.

func TestProblemPromptUserCarriesEvidence(t *testing.T) {
	now := int64(1_700_000_000) * 1e9
	ev := ProblemEvidence{
		ID: "p1", Service: "checkout", Metric: "error_rate",
		Severity: "critical", Priority: "P1", PriorityReason: "kritik + deploy",
		Comparator: ">", Value: 12.5, Threshold: 5,
		StartedNs: now - 2*3600*1e9, NowNs: now,
		Deploy:    &DeployRef{Version: "v2.1.0", AgeSec: 240, HasImpact: true, P99DeltaPct: 34, ErrDeltaPP: 2.4},
		Blast:     &BlastRef{TotalCallers: 12, CascadingCallers: 3, TopCallers: []string{"web", "bff"}},
		SlowOp:    &OpRef{Name: "POST /pay", P95Ms: 842, ErrorRate: 6.5},
	}
	got := ProblemPromptUser(ev, "")
	for _, want := range []string{
		"checkout", "error_rate", "12.5", "eşik 5", `ihlal yönü ">"`,
		"kritik", "P1 (kritik + deploy)", "açık, 2sa önce açıldı",
		"v2.1.0", "p99 +34%", "hata oranı +2.4 puan",
		"12 çağıran servis", "3 tanesinin KENDİ açık problemi var",
		"web, bff", "POST /pay", "842ms",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt %q taşımıyor:\n%s", want, got)
		}
	}
	// Uydurma kalkanı + eyleme dönük kapanış: kartın "sonra ne yapayım"
	// yarısı prose'dan gelir.
	if !strings.Contains(got, "UYDURMA") || !strings.Contains(got, "İLK adımı") {
		t.Errorf("prompt kalkan/kapanış cümlelerini taşımıyor:\n%s", got)
	}
}

// Hipotez bloğu VARSA çıplak aday satırı yazılmaz: aynı bilgiyi iki kez
// yazmak token yakar ve iki farklı ifadeyle çelişme riski açar.
func TestProblemPromptUserDoesNotDuplicateHypothesis(t *testing.T) {
	ev := ProblemEvidence{ID: "p", Service: "s",
		Hyp: &HypothesisRef{TopSuspect: "redis", Confidence: 0.9}}

	withBlock := ProblemPromptUser(ev, "KÖK-NEDEN HİPOTEZİ (deterministik): redis")
	if strings.Contains(withBlock, "KÖK-NEDEN ADAYI (korelasyon motoru)") {
		t.Errorf("hipotez bloğu varken çıplak aday satırı da yazıldı:\n%s", withBlock)
	}
	if !strings.Contains(withBlock, "KÖK-NEDEN HİPOTEZİ (deterministik): redis") {
		t.Error("hipotez bloğu aynen taşınmadı")
	}

	withoutBlock := ProblemPromptUser(ev, "  ")
	if !strings.Contains(withoutBlock, "KÖK-NEDEN ADAYI (korelasyon motoru): redis, güven %90") {
		t.Errorf("blok yokken aday satırı yazılmadı:\n%s", withoutBlock)
	}
}

func TestProblemPromptUserSparseDoesNotPanic(t *testing.T) {
	got := ProblemPromptUser(ProblemEvidence{}, "")
	if !strings.Contains(got, "PROBLEM:") || !strings.Contains(got, "—") {
		t.Errorf("boş kanıtta prompt bozuldu:\n%s", got)
	}
}
