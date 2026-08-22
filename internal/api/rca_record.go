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
	"strings"

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
// v0.9.1281 — imza body + source aldı. İkisi de ZORUNLU parametre
// (opsiyonel alan değil) ve bu bilinçli: rca_verdicts ReplacingMergeTree,
// yani her yazım TAM SATIR replace. Alanı "isteğe bağlı" bırakmak, bir
// gün gövdeyi doldurmayan ikinci bir yazıcının var olan gövdeyi sessizce
// silmesi demekti — ev kuralının tam da uyardığı şey.
func rcaVerdictRecordOf(exchangeID, anchorKind, anchorID, source string,
	h *chstore.RootCauseHypothesis, v *RCAVerdict, prose *string) (chstore.RCAVerdictRecord, bool) {
	if v == nil || exchangeID == "" {
		return chstore.RCAVerdictRecord{}, false
	}
	rec := chstore.RCAVerdictRecord{
		ExchangeID:  exchangeID,
		AnchorKind:  anchorKind,
		AnchorID:    anchorID,
		Verdict:     v.Verdict,
		RCEntity:    v.RootCause.Entity,
		RCFailMode:  v.RootCause.FailureMode,
		Confidence:  v.Confidence,
		ModelConf:   v.ModelConfidence,
		HypoConf:    v.HypothesisConfidence,
		Parsed:      v.Shields.Parsed,
		Repaired:    v.Shields.Repaired,
		ShieldNotes: v.Shields.Notes,
		Body:        rcaVerdictBodyOf(prose, v.Summary),
		Source:      source,
	}
	// Hipotez nil olabilir (savunmacı): sürüm ve servis ondan gelir ve
	// ikisi de "bu verdict neye bakarak verildi" sorusunun parçası.
	if h != nil {
		rec.HypoVersion = h.Version
		rec.Service = h.Service
	}
	return rec, true
}

// rcaVerdictBodyOf — operatörün GÖRDÜĞÜ metin. SAF (v0.9.1281).
//
// Öncelik prose, yedek summary — ve bu sıra kalıcı kaydın anlamını
// belirliyor: ekranda hangi kutu doluysa o yazılmalı. Model çözümlenirse
// operatör LLM anlatımını okur (prose); çözümlenemezse deterministik
// cümleyi okur (summary, buildRCAVerdict sözleşmesi gereği prose'a
// YAZILMAZ). Yanlış olanı seçmek, "operatöre ne gösterdik" sorusuna
// yanlış cevap veren bir kayıt bırakırdı — kaydın var olma sebebi tam
// da bu soru.
//
// prose nil VEYA boşluk-dolu ⇒ summary. İkisi de boşsa boş dize:
// uydurmayız, gövdesiz kayıt dürüst bir hâl.
func rcaVerdictBodyOf(prose *string, summary string) string {
	if prose != nil {
		if t := strings.TrimSpace(*prose); t != "" {
			return t
		}
	}
	return strings.TrimSpace(summary)
}

// recordRCAVerdict — kaydı yazar. EN İYİ ÇABA.
//
// Hata cevabı DÜŞÜREMEZ ve bu tartışmaya kapalı: operatörün beklediği
// tanı, bizim muhasebemiz yüzünden kaybolmamalı. Gözlemlenebilirlik
// katmanının gözlemlediği şeyi bozması, çözdüğü sorundan kötüdür
// (aynı ilke: internal/evaluator/heartbeat.go writeHeartbeat).
func (s *Server) recordRCAVerdict(ctx context.Context, exchangeID, anchorKind, anchorID, source string,
	h *chstore.RootCauseHypothesis, v *RCAVerdict, prose *string) {
	rec, ok := rcaVerdictRecordOf(exchangeID, anchorKind, anchorID, source, h, v, prose)
	if !ok {
		return
	}
	if err := s.store.UpsertRCAVerdict(ctx, rec); err != nil {
		log.Printf("[rca] verdict kaydı yazılamadı (%s/%s): %v", anchorKind, anchorID, err)
	}
}
