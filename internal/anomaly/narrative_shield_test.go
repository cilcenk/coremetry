package anomaly

// v0.9.1208 (Faz 6.3b) — auto-explain çıktı kalkanının pinleri:
// (a) prompt'ta GÖSTERİLEN ad (tireli servis dahil) işaretlenmez,
// (b) katalog varlığı (hipotez şüphelisi) işaretlenmez,
// (c) uydurulan servis-biçimli ad metnin SONUNA açık işaretle düşer —
//     anlatı silinmez, iddia kalkansız da yaşamaz,
// (d) temiz anlatı bayt-bayt aynen döner.

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestShieldNarrative(t *testing.T) {
	hyp := &chstore.RootCauseHypothesis{
		Service: "checkout", TopSuspect: "payment-db", TopScore: 3, Confidence: 0.8,
	}
	prompt := "Rule: hata orani\nService: checkout\n- Komşu: mobile-bff üzerinde aktif anomali\n"

	clean := "checkout hatalari payment-db kaynakli görünüyor; mobile-bff etkileniyor."
	if got := shieldNarrative(clean, prompt, "checkout", hyp); got != clean {
		t.Fatalf("gösterilen/katalog adları işaretlenmemeli:\n%s", got)
	}

	invented := "Kök neden fraud-engine servisindeki kilitlenme."
	got := shieldNarrative(invented, prompt, "checkout", hyp)
	if !strings.Contains(got, "⚠ Doğrulanamayan ad(lar): fraud-engine") {
		t.Fatalf("uydurulan ad işaretlenmeliydi:\n%s", got)
	}
	if !strings.HasPrefix(got, invented) {
		t.Fatalf("anlatı silinmemeli, işaret SONA eklenmeli:\n%s", got)
	}

	// hyp=nil (hipotezsiz problem) — kalkan yine çalışır, panik yok;
	// yalnız GÖSTERİLEN adlar meşru. payment-db bu durumda hiçbir yerde
	// gösterilmedi — işaretlenmesi DOĞRU davranış (kalkan hipotezsiz
	// yolda gevşemez).
	shown := "checkout hatalari mobile-bff çağıranını etkiliyor."
	if got := shieldNarrative(shown, prompt, "checkout", nil); got != shown {
		t.Fatalf("hipotezsiz yolda gösterilen adlar işaretlenmemeli:\n%s", got)
	}
	if got := shieldNarrative(clean, prompt, "checkout", nil); !strings.Contains(got, "payment-db — modele gösterilen") {
		t.Fatalf("hipotezsiz yolda katalog-dışı ad işaretlenmeliydi:\n%s", got)
	}
}
