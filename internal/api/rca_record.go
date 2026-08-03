// v0.9.591 — verdict kaydının API tarafı.
//
// Ayrı dosya çünkü sorumluluk ayrı: rca_verdict.go verdict'i ÜRETİR,
// bu dosya üretileni KAYDEDER. Kayıt yolu üretim yolunu asla
// etkilememeli ve bunu yapı gereği garanti etmenin en ucuz yolu iki
// dosyada tutmak.
package api

import (
	"context"
	"log"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// rcaVerdictRecordOf — SAF dönüşüm: yanıt şekli → depo şekli.
//
// Saf tutulması bilinçli. Bu oturumda iki kez, SQL'e dokunmayan
// testler yüzünden çalışmayan kod gönderildi (v0.9.543, v0.9.572);
// dönüşümün kendisi en azından tablo-testli olabilir.
//
// verdict nil ⇒ kayıt yok (ok=false). Boş bir satır yazmak, hiç
// yazmamaktan KÖTÜ olurdu: ölçümde "karar verildi" diye sayılır ama
// hiçbir soruya cevap vermez.
func rcaVerdictRecordOf(exchangeID, anchorKind, anchorID string,
	h *chstore.RootCauseHypothesis, v *RCAVerdict) (chstore.RCAVerdictRecord, bool) {
	if v == nil || exchangeID == "" {
		return chstore.RCAVerdictRecord{}, false
	}
	rec := chstore.RCAVerdictRecord{
		ExchangeID:  exchangeID,
		AnchorKind:  anchorKind,
		AnchorID:    anchorID,
		Verdict:     v.Verdict,
		Confidence:  v.Confidence,
		ModelConf:   v.ModelConfidence,
		HypoConf:    v.HypothesisConfidence,
		Parsed:      v.Shields.Parsed,
		Repaired:    v.Shields.Repaired,
		ShieldNotes: v.Shields.Notes,
	}
	// Hipotez nil olabilir (savunmacı): sürüm ve servis ondan gelir ve
	// ikisi de "bu verdict neye bakarak verildi" sorusunun parçası.
	if h != nil {
		rec.HypoVersion = h.Version
		rec.Service = h.Service
	}
	return rec, true
}

// recordRCAVerdict — kaydı yazar. EN İYİ ÇABA.
//
// Hata cevabı DÜŞÜREMEZ ve bu tartışmaya kapalı: operatörün beklediği
// tanı, bizim muhasebemiz yüzünden kaybolmamalı. Gözlemlenebilirlik
// katmanının gözlemlediği şeyi bozması, çözdüğü sorundan kötüdür
// (aynı ilke: internal/evaluator/heartbeat.go writeHeartbeat).
func (s *Server) recordRCAVerdict(ctx context.Context, exchangeID, anchorKind, anchorID string,
	h *chstore.RootCauseHypothesis, v *RCAVerdict) {
	rec, ok := rcaVerdictRecordOf(exchangeID, anchorKind, anchorID, h, v)
	if !ok {
		return
	}
	if err := s.store.UpsertRCAVerdict(ctx, rec); err != nil {
		log.Printf("[rca] verdict kaydı yazılamadı (%s/%s): %v", anchorKind, anchorID, err)
	}
}
