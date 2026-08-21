package api

// rca_extras.go — RCA kanıt kataloğu GENİŞLEMESİ (v0.9.1203, AI Faz
// 6.1, onaylı plan K4a).
//
// /rootcause fan-out'u BubbleUp/BlastRadius/Correlations'ı hesaplayıp
// panelde çiziyordu ama verdict hakemi HİÇBİRİNİ görmüyordu —
// "hesaplanıp hiç anlatılmayan" sınıfı. Üçü artık E-uzayına katalog
// satırı olarak girer; shields + attribution + feedback OLDUĞU GİBİ
// çalışır (halüsinasyon koruması bedava — K4a'nın seçilme gerekçesi).
//
// Nedensellik ayrımı BİLİNÇLİ:
//   - Correlations komşuları OLASI NEDENDİR → entity dolu, K3 beyaz
//     listesi (ve şemanın root_cause.entity enum'u) GENİŞLER.
//   - Blast çağıranları MAĞDURDUR, neden değil → entity boş; adları
//     yalnız gösterilen-jeton yoluyla meşrulaşır (model zincirde
//     anabilir ama kök neden İLAN EDEMEZ).
//   - BubbleUp değerleri boyuttur, servis değil → entity boş.
//
// Maliyet: blast + correlations MV/state okumaları (saniye-altı);
// BubbleUp ham spans taraması olduğundan kendi 8 sn tavanıyla koşar ve
// hepsi soft-fail — kanıt toplanamazsa katalog o aile OLMADAN kurulur
// (dürüst yokluk), verdict düşmez. Hepsi 10 dk'lık verdict cache'inin
// arkasında, ankor başına bir kez.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

const (
	// rcaExtrasWindow — kanıt penceresi: ankorun AÇILIŞINI izleyen dilim
	// (correlate.go'nun 600 sn varsayılanıyla aynı; ≥300 sn olduğu için
	// correlations her zaman MV yolunda).
	rcaExtrasWindow = 10 * time.Minute
	// rcaExtrasCorrCap / rcaExtrasBubbleCap / rcaExtrasBlastWorst —
	// plandaki "ilk 3" kapları. Sabitler ID determinizminin parçası.
	rcaExtrasCorrCap    = 3
	rcaExtrasBubbleCap  = 3
	rcaExtrasBlastWorst = 3
	// rcaBubbleUpTimeout — ham-spans kıyası verdict kurulumunu
	// süresiz bekletemez; süre dolarsa aile atlanır.
	rcaBubbleUpTimeout = 8 * time.Second
)

// rcaCatalogExtras — katalog genişlemesinin girdileri. Sıfır değeri
// "hiçbiri toplanamadı" demek ve katalog bugünkü hâliyle kurulur.
type rcaCatalogExtras struct {
	Blast        *chstore.BlastRadius
	Correlations []chstore.ChangedService
	BubbleUp     *chstore.BubbleUpResult
}

// gatherRCACatalogExtras — üç kaynağı paralel, soft-fail toplar.
// anchorStartNs=0 (ankor satırı çözülememiş) ⇒ pencere kurulamaz,
// dürüstçe boş döner — eski pencereye uydurma kanıt bağlamayız.
func (s *Server) gatherRCACatalogExtras(ctx context.Context, h *chstore.RootCauseHypothesis, anchorStartNs int64) rcaCatalogExtras {
	var out rcaCatalogExtras
	if h == nil || h.Service == "" || anchorStartNs <= 0 {
		return out
	}
	from := time.Unix(0, anchorStartNs)
	to := from.Add(rcaExtrasWindow)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		if br, err := s.store.GetServiceBlastRadius(ctx, h.Service, from, to); err == nil && br.TotalCallers > 0 {
			out.Blast = &br
		}
	}()
	go func() {
		defer wg.Done()
		if cs, err := s.store.GetCorrelatedChangesMV(ctx, from, int(rcaExtrasWindow.Seconds()), int(4*rcaExtrasWindow.Seconds())); err == nil {
			out.Correlations = cs
		}
	}()
	go func() {
		defer wg.Done()
		bctx, cancel := context.WithTimeout(ctx, rcaBubbleUpTimeout)
		defer cancel()
		baseline := []chstore.FilterExpr{{Key: "service.name", Op: "=", Values: []string{h.Service}}}
		selection := []chstore.FilterExpr{
			{Key: "service.name", Op: "=", Values: []string{h.Service}},
			{Key: "status_code", Op: "=", Values: []string{"error"}},
		}
		if bu, err := s.store.BubbleUp(bctx, baseline, selection, from, to, from, to); err == nil {
			out.BubbleUp = bu
		}
	}()
	wg.Wait()
	return out
}

// buildRCAEvidenceCatalogExt — taban kataloğu kurar, üç yeni aileyi
// SONUNA ekler. Taban builder'a dokunulmaz: mevcut E/N kimlikleri
// bayt-bayt aynı kalır (10 dk cache'li verdict'in kimlik-kararlılık
// sözleşmesi, rca_evidence.go:89-91) — yeni aileler sayaçları taban
// kataloğun kaldığı yerden devralır.
func buildRCAEvidenceCatalogExt(h *chstore.RootCauseHypothesis, extras rcaCatalogExtras) rcaEvidenceCatalog {
	cat := buildRCAEvidenceCatalog(h)
	posN := len(cat.positiveIDs())
	addPos := func(entity, text string) {
		posN++
		ref := rcaEvidenceRef{ID: fmt.Sprintf("E%d", posN), Kind: rcaPositive, Entity: entity, Text: text}
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
		worst := make([]string, 0, rcaExtrasBlastWorst)
		for i, c := range b.Callers {
			if i >= rcaExtrasBlastWorst {
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
		if added >= rcaExtrasCorrCap {
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
			if n >= rcaExtrasBubbleCap || len(attr.Values) == 0 {
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
