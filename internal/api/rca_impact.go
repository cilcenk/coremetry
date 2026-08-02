// v0.9.559 — RCA impact: sayılar ClickHouse'tan, modelden DEĞİL.
//
// Tasarım: docs/cosre-verdict-design.md §4-K5.
//
// Modelden sayı istemek iki kere yanlış: uydurabilir, ve uydurduğunda
// operatör onu ÖLÇÜLMÜŞ sanır. Bu yüzden etki rakamları MV'den okunur
// ve modele yalnız YORUMLATILIR.
//
// Ama "ClickHouse'tan geliyor" güvencesi, okuma yanlışsa daha
// tehlikelidir: operatör sayıyı sorgulamaz. Bu yüzden üç tuzak açıkça
// kapatıldı:
//
//  1. DOĞRU VARLIK. Birinci tasarım ankorun servisini okuyordu; tipik
//     bir P1'de kök neden başka bir servistir ve "Kök neden: odeme-db"
//     başlığının altında odeme-api'nin sayıları görünürdü.
//
//  2. BOŞ SONUÇ ≠ SIFIR. MV henüz materyalize olmamışsa (son ~5dk)
//     sorgu 0 satır döner ve err == nil. Sıfır basmak "etkilenen istek
//     yok" demek olurdu — ve ✨ Explain'e basılan an tam olarak olayın
//     patladığı andır. Sayılar nil döner, sebep yazılır.
//
//  3. GELECEK PENCERE. boundAnalysisWindow ankorun başlangıcına 10dk
//     ekler; taze bir problemde pencere sonu GELECEKTE kalır. Üst sınır
//     now ile kırpılır, yoksa "sorgulanan aralık" yalan söyler.
//
// Kova hizalaması ayrıca gerekiyordu ve v0.9.555'te mağaza katmanında
// düzeltildi (bu tasarımın çekişmeli incelemesinin yan ürünü).
package api

import (
	"context"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// RCAImpact — ölçülmüş etki. Ölçülemeyen alanlar nil.
type RCAImpact struct {
	// Entity — sayıların AİT OLDUĞU varlık. Ankor servisi olmayabilir;
	// bu yüzden alan açıkça taşınıyor.
	Entity string `json:"entity"`
	// AnchorService — problemin/anomalinin kendi servisi. Ayrı tutulur
	// ki operatör hangi sayının neye ait olduğunu karıştırmasın.
	AnchorService string `json:"anchorService,omitempty"`

	// nil = ÖLÇEMEDİK (sıfır DEĞİL).
	ErrorCount   *uint64  `json:"errorCount"`
	RequestCount *uint64  `json:"requestCount"`
	ErrorShare   *float64 `json:"errorShare"`

	WindowFromNs int64 `json:"windowFromNs"`
	WindowToNs   int64 `json:"windowToNs"`

	// Note — ölçüm ölçülemediyse SEBEBİ. Boş = sayılar geçerli.
	Note string `json:"note,omitempty"`
}

// rcaImpactWindow — etki penceresi. SAF, tablo-testli.
//
// computeRCAImpact'in kendisi *chstore.Store'a bağlı olduğu için test
// edilemiyor (repoda emsali var: gatherDeepEvidence de edilemiyor).
// Bu yüzden hesabın YANLIŞ OLABİLECEK kısmı buraya ayrıldı.
func rcaImpactWindow(startedNs int64, now time.Time) (time.Time, time.Time) {
	started := time.Unix(0, startedNs)
	from, to := boundAnalysisWindow(started, now)
	// Üst sınırı gerçek zamana kırp: boundAnalysisWindow taze bir
	// ankorda pencereyi geleceğe uzatır ve "sorgulanan aralık" yalan
	// söyler.
	if to.After(now) {
		to = now
	}
	// Kırpma pencereyi ters çevirmiş olabilir (ankor gelecekte
	// damgalıysa — saat kayması). O hâlde ölçüm yapılamaz.
	if !to.After(from) {
		return from, from
	}
	return from, to
}

// summariseRCAImpact — MV satırlarını etkiye çevirir. SAF.
//
// Boş satır kümesi ile sıfır trafik AYRI şeyler ve ayrı raporlanır.
func summariseRCAImpact(rows []chstore.ServiceSummaryRow, entity, anchorSvc string, from, to time.Time) *RCAImpact {
	out := &RCAImpact{
		Entity:        entity,
		AnchorService: anchorSvc,
		WindowFromNs:  from.UnixNano(),
		WindowToNs:    to.UnixNano(),
	}
	if !to.After(from) {
		out.Note = "geçerli bir ölçüm penceresi kurulamadı (ankor zaman damgası tutarsız)"
		return out
	}
	if len(rows) == 0 {
		// EN ÖNEMLİ DAL. Sıfır basmak, tam patlama anında "etkilenen
		// istek yok" demek olurdu.
		out.Note = "özet görünümü bu pencere için henüz materyalize olmadı (son ~5dk) — ölçülemedi"
		return out
	}
	var spans, errs uint64
	for _, r := range rows {
		spans += r.SpanCount
		errs += r.ErrorCount
	}
	out.ErrorCount = &errs
	out.RequestCount = &spans
	if spans > 0 {
		share := float64(errs) / float64(spans)
		out.ErrorShare = &share
	} else {
		// Satır var ama hiç span yok: gerçek bir sıfır trafik. Oran
		// tanımsız, uydurulmaz.
		out.Note = "pencerede istek yok — hata oranı tanımsız"
	}
	return out
}

// computeRCAImpact — MV okumasını yapar ve saf özetleyiciye devreder.
//
// Soft-fail: CH hatası ölçümü nil bırakır, verdict'i düşürmez. Etki
// bilinmiyor olabilir; kök neden yine de anlatılabilir.
func (s *Server) computeRCAImpact(ctx context.Context, entity, anchorSvc string, startedNs int64) *RCAImpact {
	if entity == "" {
		return nil
	}
	from, to := rcaImpactWindow(startedNs, time.Now())
	if !to.After(from) {
		return summariseRCAImpact(nil, entity, anchorSvc, from, to)
	}
	rows, err := s.store.GetServiceSummary5m(ctx, entity, from, to)
	if err != nil {
		out := &RCAImpact{
			Entity: entity, AnchorService: anchorSvc,
			WindowFromNs: from.UnixNano(), WindowToNs: to.UnixNano(),
			Note: "etki ölçülemedi (özet okuması başarısız)",
		}
		return out
	}
	return summariseRCAImpact(rows, entity, anchorSvc, from, to)
}
