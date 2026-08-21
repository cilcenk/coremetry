package rca

// extras.go — katalog genişlemesinin SAF yarısı (v0.9.1203 Faz 6.1;
// v0.9.1208'de internal/rca'ya indi — Faz 6.3b: problem-auto-explain
// aynı katalog makinesini kullanır, api↔anomaly import yönü gereği saf
// çekirdek paylaşılan pakette yaşar). Toplama (IO) api tarafında kaldı
// (gatherRCACatalogExtras).

import (
	"fmt"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

const (
	// ExtrasWindow — kanıt penceresi: ankorun AÇILIŞINI izleyen dilim
	// (correlate.go'nun 600 sn varsayılanıyla aynı; ≥300 sn olduğu için
	// correlations her zaman MV yolunda).
	ExtrasWindow = 10 * time.Minute
	// extrasCorrCap / extrasBubbleCap / extrasBlastWorst —
	// plandaki "ilk 3" kapları. Sabitler ID determinizminin parçası.
	extrasCorrCap    = 3
	extrasBubbleCap  = 3
	extrasBlastWorst = 3
	// BubbleUpTimeout — ham-spans kıyası verdict kurulumunu
	// süresiz bekletemez; süre dolarsa aile atlanır.
	BubbleUpTimeout = 8 * time.Second
)

// CatalogExtras — katalog genişlemesinin girdileri. Sıfır değeri
// "hiçbiri toplanamadı" demek ve katalog bugünkü hâliyle kurulur.
type CatalogExtras struct {
	Blast        *chstore.BlastRadius
	Correlations []chstore.ChangedService
	BubbleUp     *chstore.BubbleUpResult
}

// BuildEvidenceCatalogExt — taban kataloğu kurar, üç yeni aileyi
// SONUNA ekler. Taban builder'a dokunulmaz: mevcut E/N kimlikleri
// bayt-bayt aynı kalır (10 dk cache'li verdict'in kimlik-kararlılık
// sözleşmesi, rca_evidence.go:89-91) — yeni aileler sayaçları taban
// kataloğun kaldığı yerden devralır.
func BuildEvidenceCatalogExt(h *chstore.RootCauseHypothesis, extras CatalogExtras) EvidenceCatalog {
	cat := BuildEvidenceCatalog(h)
	posN := len(cat.PositiveIDs())
	addPos := func(entity, text string) {
		posN++
		ref := EvidenceRef{ID: fmt.Sprintf("E%d", posN), Kind: Positive, Entity: entity, Text: text}
		cat.Refs = append(cat.Refs, ref)
		cat.byID[ref.ID] = ref
		if entity != "" {
			cat.Entities[entity] = true
		}
	}
	anchor := ""
	if h != nil {
		anchor = h.Service
	}

	// ── 5. Etki alanı (BlastRadius) — tek satır, MAĞDURLAR ────────────
	if b := extras.Blast; b != nil && b.TotalCallers > 0 {
		worst := make([]string, 0, extrasBlastWorst)
		for i, c := range b.Callers {
			if i >= extrasBlastWorst {
				break
			}
			t := fmt.Sprintf("%s (hata %%%.1f", c.Service, c.ErrorRate)
			if c.HasOpenProblem {
				t += ", açık problemi var"
			}
			t += ")"
			worst = append(worst, t)
		}
		txt := fmt.Sprintf("etki alanı: %d çağıran servis", b.TotalCallers)
		if b.CascadingCallers > 0 {
			txt += fmt.Sprintf(" (%d'sinde kaskad problem)", b.CascadingCallers)
		}
		if len(worst) > 0 {
			txt += " — öne çıkanlar: " + strings.Join(worst, ", ")
		}
		txt += ". Bunlar ETKİLENENLERDİR; etki alanı genişliği ciddiyeti gösterir, çağıranlar kök neden adayı değildir."
		addPos("", txt)
	}

	// ── 6. Aynı pencerede kötüleşen komşular (Correlations) ───────────
	// Olası nedenler: entity dolu → beyaz liste (ve şema enum'u) genişler.
	added := 0
	for _, cs := range extras.Correlations {
		if cs.Service == "" || cs.Service == anchor {
			continue
		}
		if added >= extrasCorrCap {
			break
		}
		added++
		addPos(cs.Service, fmt.Sprintf(
			"aynı pencerede kötüleşen komşu: %s (p99 Δ%+.0f%%, hata Δ%+.1f%%, istek Δ%+.0f%%)",
			cs.Service, cs.P99DeltaPct, cs.ErrDeltaPct, cs.RateDeltaPct))
	}

	// ── 7. Hatalarda ayrışan boyutlar (BubbleUp) ──────────────────────
	// Boyut değerleri servis değildir → entity boş; tireli değerler
	// gösterilen-jeton yoluyla K3'te meşrulaşır (checkRCAEntities).
	if bu := extras.BubbleUp; bu != nil && bu.SelectionTotal > 0 {
		n := 0
		for _, attr := range bu.Attributes {
			if n >= extrasBubbleCap || len(attr.Values) == 0 {
				break
			}
			v := attr.Values[0]
			if v.Score <= 0 {
				continue // hatalarda AYRIŞMAYAN boyut kanıt değildir
			}
			n++
			addPos("", fmt.Sprintf(
				"hatalarda ayrışan boyut: %s=%s (hatalı kümede %%%.0f, tabanda %%%.0f)",
				attr.Key, v.Value, v.SelectionPct, v.BaselinePct))
		}
	}

	return cat
}
