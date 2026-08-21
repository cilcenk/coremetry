package api

// rca_compat.go — v0.9.1208 (Faz 6.3b). RCA kanıt kataloğu + varlık
// taramasının SAF çekirdeği internal/rca'ya indi: problem-auto-explain
// (package anomaly) AYNI katalog/kalkan makinesini kullanmak zorunda ve
// import yönü api→anomaly olduğundan çekirdek paylaşılan pakette yaşar.
//
// Bu shim eski paket-içi adları AYNEN korur: 9 dosyalık çağıran kümesi
// ve testleri edit çalkantısı olmadan derlenir. YENİ kod rca.* kullanır.

import (
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/rca"
)

type (
	rcaEvidenceKind    = rca.EvidenceKind
	rcaEvidenceRef     = rca.EvidenceRef
	rcaEvidenceCatalog = rca.EvidenceCatalog
	rcaCatalogExtras   = rca.CatalogExtras
)

const (
	rcaPositive = rca.Positive
	rcaNegative = rca.Negative
)

func buildRCAEvidenceCatalog(h *chstore.RootCauseHypothesis) rca.EvidenceCatalog {
	return rca.BuildEvidenceCatalog(h)
}

func buildRCAEvidenceCatalogExt(h *chstore.RootCauseHypothesis, extras rca.CatalogExtras) rca.EvidenceCatalog {
	return rca.BuildEvidenceCatalogExt(h, extras)
}

func renderRCAEvidenceCatalog(c rca.EvidenceCatalog) string { return rca.RenderEvidenceCatalog(c) }
func rcaAllowedEntities(c rca.EvidenceCatalog) []string     { return rca.AllowedEntities(c) }

func scanUnknownEntities(known map[string]bool, texts ...string) []string {
	return rca.ScanUnknownEntities(known, texts...)
}
func lowerKnownSet(names ...string) map[string]bool { return rca.LowerKnownSet(names...) }
func addShownTokens(known map[string]bool, texts ...string) {
	rca.AddShownTokens(known, texts...)
}
