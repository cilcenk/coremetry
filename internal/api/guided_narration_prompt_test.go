package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/copilot"
)

// v0.9.1131 regresyon — Operator-reported: "✨ Explain trace ile
// chat'te 'bu trace'i açıkla' farklı davranıyor; Explain daha iyi."
// Kanıt paketi v0.9.537'den beri ortak (buildTraceExplainInput);
// ayrışan tek şey anlatım prompt'uydu. Bu tablo iki kapıyı kilitler:
// trace/span intent'leri ✨ Explain'in prompt'unu BİREBİR kullanır,
// kalan intent'ler sohbet prompt'unda kalır.
func TestGuidedNarrationPromptPerIntent(t *testing.T) {
	traceSys := copilot.SystemPromptTrace()
	chatSys := copilot.SystemPromptGuidedChat()
	if traceSys == chatSys {
		t.Fatal("test öncülü çöktü: iki prompt zaten aynı")
	}
	cases := []struct {
		intent guidedIntent
		want   string
	}{
		{guidedTraceByID, traceSys},
		{guidedSpanByID, traceSys},
		{guidedProblems, chatSys},
		{guidedServiceHealth, chatSys},
		{guidedRootCause, chatSys},
		{guidedShiftSummary, chatSys},
	}
	for _, c := range cases {
		t.Run(string(c.intent), func(t *testing.T) {
			got := guidedNarrationPrompt(c.intent)
			if got != c.want {
				t.Errorf("intent %s yanlış anlatıcıda", c.intent)
			}
		})
	}
	// Derinliğin işareti: trace anlatıcısı 3-bölümlü yapıyı taşır.
	if !strings.Contains(traceSys, "İşlem Akışı") {
		t.Error("SystemPromptTrace 3-bölümlü yapı çapasını kaybetmiş — bu test yeniden değerlendirilmeli")
	}
}
