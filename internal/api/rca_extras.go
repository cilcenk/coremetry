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
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/rca"
)

// gatherRCACatalogExtras — üç kaynağı paralel, soft-fail toplar.
// anchorStartNs=0 (ankor satırı çözülememiş) ⇒ pencere kurulamaz,
// dürüstçe boş döner — eski pencereye uydurma kanıt bağlamayız.
func (s *Server) gatherRCACatalogExtras(ctx context.Context, h *chstore.RootCauseHypothesis, anchorStartNs int64) rcaCatalogExtras {
	var out rcaCatalogExtras
	if h == nil || h.Service == "" || anchorStartNs <= 0 {
		return out
	}
	from := time.Unix(0, anchorStartNs)
	to := from.Add(rca.ExtrasWindow)

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
		if cs, err := s.store.GetCorrelatedChangesMV(ctx, from, int(rca.ExtrasWindow.Seconds()), int(4*rca.ExtrasWindow.Seconds())); err == nil {
			out.Correlations = cs
		}
	}()
	go func() {
		defer wg.Done()
		bctx, cancel := context.WithTimeout(ctx, rca.BubbleUpTimeout)
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
