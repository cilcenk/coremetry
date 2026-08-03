// v0.9.591 — kök-neden hakem kararının kalıcı kaydı.
//
// NEDEN: verdict istek başına üretilip yalnızca HTTP yanıtında
// yaşıyordu. Ne kararın kendisi ne de KALKANLARIN NE YAPTIĞI hiçbir
// yere düşüyordu.
//
// Tek kalıcı iz `ai_calls.response_sample`'daki modelin HAM çıktısıydı
// — yani kalkanlardan ÖNCEKİ hâli. Operatörün gördüğü o değil:
// aradaki farkı kalkanlar üretiyor (uydurma kanıt kimliği düşürülür,
// geçersiz eleme iptal edilir, güven tavanlanır). Dolayısıyla
// "operatöre ne gösterdik" sorusunun cevabı hiçbir yerde yoktu.
//
// İki şeyi birden mümkün kılıyor:
//
//  1. ÖLÇÜM — kalkanlar ne sıklıkla devreye giriyor, model ne
//     sıklıkla çözümlenemiyor, kaç karar insufficient_evidence.
//     Bunlar bilinmeden "verdict kalitesi" bir histen ibaret.
//  2. GERİ BİLDİRİM — exchange_id ai_feedback'e join olur; "👎 verilen
//     verdict neydi" cevaplanabilir hale gelir.
//
// (2) tasarım dokümanının [6] LEARN katmanının ÖN KOŞULU
// (docs/cosre-verdict-design.md §11). Öğrenmenin kendisi bu dilimde
// YOK ve bilinçli: hangi verdict'lerin yanlış olduğunu bilmeden neyi
// öğreteceğimizi de bilmiyoruz.
package chstore

import (
	"context"
	"time"
)

// RCAVerdictRecord — operatöre GÖSTERİLEN verdict'in kaydı.
//
// Modelin ham çıktısı DEĞİL: kalkanlardan geçmiş hâli. Ham çıktı zaten
// ai_calls.response_sample'da; buradaki kayıt onunla kıyaslanabilsin
// diye aynı exchange_id'yi taşıyor.
type RCAVerdictRecord struct {
	ExchangeID string `json:"exchangeId"`
	AnchorKind string `json:"anchorKind"` // problem | anomaly
	AnchorID   string `json:"anchorId"`
	Service    string `json:"service,omitempty"`

	// Verdict — üç enum'dan biri (root_cause_identified |
	// probable_cause | insufficient_evidence). Kalkanlar sonrası.
	Verdict string `json:"verdict"`

	// Üç ayrı güven, üçü FARKLI şey ve aynı ekranda buluşuyorlar:
	// Confidence nihai (tavanlanmış), ModelConf modelin beyanı,
	// HypoConf deterministik korelasyon motorunun güveni. Tavanın ne
	// kadar indirdiği ancak üçü birlikte saklanınca görünür.
	Confidence float64 `json:"confidence"`
	ModelConf  float64 `json:"modelConfidence"`
	HypoConf   float64 `json:"hypothesisConfidence"`

	// HypoVersion — verdict'in dayandığı hipotezin sürümü. Yeniden
	// sentez sürümü değiştirir; hangi girdiye bakıldığını bilmeden
	// "bu verdict yanlıştı" geri bildirimi yorumlanamaz.
	HypoVersion uint64 `json:"hypothesisVersion"`

	// Parsed / Repaired — modelin şemaya uyup uymadığı. parsed=false
	// ⇒ deterministik düşüş; o karar MODELİN değil bizim.
	Parsed   bool `json:"parsed"`
	Repaired bool `json:"repaired,omitempty"`

	// ShieldNotes — kalkanların operatöre gösterilen kısa notları.
	ShieldNotes []string `json:"shieldNotes,omitempty"`

	CreatedAt int64 `json:"createdAt"`
}

// UpsertRCAVerdict — bir verdict kaydını yazar.
//
// ReplacingMergeTree(version) ORDER BY exchange_id: aynı exchange_id
// ile ikinci yazım TAM SATIR değişimidir (ev kuralı — çağıran tüm
// alanları taşır), ALTER UPDATE yok.
//
// Yalnız MODEL GERÇEKTEN ÇAĞRILDIĞINDA yazılır (önbellek ıskası).
// Önbellekten sunulan her istekte yazsaydık aynı karar defalarca
// sayılır ve ölçüm şişerdi.
func (s *Store) UpsertRCAVerdict(ctx context.Context, v RCAVerdictRecord) error {
	if v.ExchangeID == "" {
		// Kimliksiz kayıt ai_feedback'e join olamaz, yani ölçüme de
		// geri bildirime de yaramaz. Sessizce geç: yazmak, sonradan
		// hiçbir soruya cevap vermeyen satır biriktirmek olurdu.
		return nil
	}
	created := time.Now().UTC()
	if v.CreatedAt > 0 {
		created = time.Unix(0, v.CreatedAt).UTC()
	}
	notes := v.ShieldNotes
	if notes == nil {
		notes = []string{}
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO rca_verdicts
		(exchange_id, anchor_kind, anchor_id, service, verdict,
		 confidence, model_conf, hypo_conf, hypo_version,
		 parsed, repaired, shield_notes, created_at)`)
	if err != nil {
		return err
	}
	if err := batch.Append(
		v.ExchangeID, v.AnchorKind, v.AnchorID, v.Service, v.Verdict,
		v.Confidence, v.ModelConf, v.HypoConf, v.HypoVersion,
		boolToUInt8(v.Parsed), boolToUInt8(v.Repaired), notes, created,
	); err != nil {
		return err
	}
	return batch.Send()
}

// boolToUInt8 — CH'de Nullable yok, UInt8 sentinel var (ev kuralı).
func boolToUInt8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// RCAVerdictQuality — bir pencerede verdict kalitesinin özeti.
//
// Alan seçimi "operatör neye bakıp "bu motor işe yarıyor mu" der"
// sorusundan türedi: kaç karar verildi, kaçı gerçekten kök neden
// gösterdi, model ne sıklıkla çözümlenemedi, kalkanlar ne sıklıkla
// devreye girdi, ve operatör ne dedi.
type RCAVerdictQuality struct {
	Total          int     `json:"total"`
	RootCause      int     `json:"rootCauseIdentified"`
	Probable       int     `json:"probableCause"`
	Insufficient   int     `json:"insufficientEvidence"`
	Unparsed       int     `json:"unparsed"`
	Repaired       int     `json:"repaired"`
	Shielded       int     `json:"shielded"`
	AvgConfidence  float64 `json:"avgConfidence"`
	ThumbsUp       int     `json:"thumbsUp"`
	ThumbsDown     int     `json:"thumbsDown"`
	AvgHypoVersion uint64  `json:"-"`
}

// RCAVerdictQualityStats — pencere içindeki verdict kalitesi.
//
// ai_feedback ile LEFT JOIN: derecelendirilmemiş verdict'ler de
// sayılmalı. INNER olsaydı yalnız oylananlar görünür ve oran
// yapay şekilde anlamlı görünürdü — oylama seyrek bir jest.
//
// Bounded: rca_verdicts 90g TTL'li küçük bir state tablosu, zaman
// pencereli WHERE + max_execution_time. FINAL şart (ReplacingMergeTree
// — yeniden yazılan bir verdict iki kez sayılmamalı).
func (s *Store) RCAVerdictQualityStats(ctx context.Context, from, to time.Time) (RCAVerdictQuality, error) {
	var q RCAVerdictQuality
	row := s.conn.QueryRow(ctx, `
		SELECT
			count()                                                   AS total,
			countIf(v.verdict = 'root_cause_identified')              AS rc,
			countIf(v.verdict = 'probable_cause')                     AS pc,
			countIf(v.verdict = 'insufficient_evidence')              AS ie,
			countIf(v.parsed = 0)                                     AS unparsed,
			countIf(v.repaired = 1)                                   AS repaired,
			countIf(length(v.shield_notes) > 0)                       AS shielded,
			-- ifNotFinite: boş pencerede avg() NaN döner ve NaN JSON'a
			-- serileştirilemez (sanitizeFloats'a güvenmek yerine
			-- kaynağında kes — bu okuma başka çağıranlara da açık).
			ifNotFinite(avg(v.confidence), 0)                         AS avg_conf,
			countIf(f.verdict = 1)                                    AS up,
			countIf(f.verdict = -1)                                   AS down
		FROM rca_verdicts AS v FINAL
		LEFT JOIN (
			SELECT exchange_id, verdict FROM ai_feedback FINAL
		) AS f ON f.exchange_id = v.exchange_id
		WHERE v.created_at >= ? AND v.created_at <= ?
		SETTINGS max_execution_time = 10`,
		chDateTime64Arg(from), chDateTime64Arg(to))

	var avg float64
	if err := row.Scan(&q.Total, &q.RootCause, &q.Probable, &q.Insufficient,
		&q.Unparsed, &q.Repaired, &q.Shielded, &avg, &q.ThumbsUp, &q.ThumbsDown); err != nil {
		return q, err
	}
	q.AvgConfidence = avg
	return q, nil
}
