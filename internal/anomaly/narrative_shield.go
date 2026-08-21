package anomaly

// narrative_shield.go — v0.9.1208 (Faz 6.3b, onaylı plan). Problem
// auto-explain ÇIKTISININ kalkanı: verdict yolunun K3 taramasıyla AYNI
// makine (internal/rca — entity_scan + katalog beyaz listesi).
//
// İlke v0.9.598'inkiyle simetrik: modele GÖSTERİLEN her ad meşrudur
// (prompt'un jetonları bilinen kümeye girer), göstermediğimiz
// servis-biçimli bir ad ise doğrulanamaz. Verdict yolunda bu, kalkan
// raporuna düşer; auto özet düz markdown olduğundan buradaki karşılığı
// metnin SONUNA açık bir işaret satırıdır — anlatı silinmez (operatör
// bağlamı yine görür), iddia kalkansız da YAŞAYAMAZ.
//
// Prompt'a katalog metni BİLİNÇLİ eklenmedi: aynı olgular zaten
// HypothesisPromptBlockTR + renderEvidence + renderDeepEvidence'ta ve
// küçük modelde tekrar, sinyali zayıflatır (K3 bütçe ilkesi). "Aynı
// katalogdan beslenme"nin mekanizması beyaz liste + tarama paylaşımı.

import (
	"strings"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/rca"
)

// shieldNarrative — SAF. Anlatıda, modele gösterilen prompt'ta ve
// katalog varlıklarında OLMAYAN servis-biçimli adlar varsa metnin
// sonuna işaret satırı ekler; yoksa metni aynen döndürür.
func shieldNarrative(narrative, shownPrompt, service string, hyp *chstore.RootCauseHypothesis) string {
	known := rca.LowerKnownSet(service)
	for e := range rca.BuildEvidenceCatalog(hyp).Entities {
		known[strings.ToLower(e)] = true
	}
	rca.AddShownTokens(known, shownPrompt)
	unknown := rca.ScanUnknownEntities(known, narrative)
	if len(unknown) == 0 {
		return narrative
	}
	return narrative + "\n\n⚠ Doğrulanamayan ad(lar): " +
		strings.Join(unknown, ", ") + " — modele gösterilen kanıtta yok."
}
