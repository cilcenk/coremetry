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
	"strings"
	"time"
	"unicode/utf8"
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

	// RCEntity / RCFailMode (v0.9.595) — İMZA. Bir vakayı "kök neden
	// şu varlıkta, şu arıza kipiyle" diye özetleyen ikili; LEARN
	// katmanı geçmiş vakaları bununla eşleştiriyor.
	RCEntity   string `json:"rootCauseEntity,omitempty"`
	RCFailMode string `json:"rootCauseFailureMode,omitempty"`

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

	// Body (v0.9.1281) — operatöre GÖSTERİLEN metin. prose varsa o,
	// yoksa deterministik summary. 8KB'a kırpılır (trimRCAVerdictBody).
	//
	// Neden kararın kendisiyle aynı satırda: enum + güven + kalkanlar
	// v0.9.591'den beri kalıcıydı ama METİN yalnız 30dk'lık explain
	// önbelleğindeydi. "Bu verdict yanlıştı" geri bildirimi, verdict'in
	// ne dediği bilinmeden yorumlanamaz — 👎 ile eşleşecek gövde
	// önbellekle birlikte buharlaşıyordu.
	Body string `json:"body,omitempty"`

	// Source (v0.9.1281) — 'operator' (✨ Explain tıklaması) | 'auto'
	// (derin soruşturma kapısı). Kalite ölçümünde ikisi AYNI kefeye
	// konamaz: otomatik üretim operatörün seçmediği bir ankorda koşar.
	Source string `json:"source,omitempty"`

	CreatedAt int64 `json:"createdAt"`
}

const (
	// RCAVerdictSourceOperator / RCAVerdictSourceAuto — source enum'u.
	// Sabit çünkü İKİ tarafta okunuyor (yazan api, gösteren frontend) ve
	// elle yazılan iki dize bir gün sessizce ayrışır.
	RCAVerdictSourceOperator = "operator"
	RCAVerdictSourceAuto     = "auto"

	// rcaVerdictBodyMax — kalıcı gövde tavanı (bayt).
	//
	// 8KB: verdict özeti birkaç paragraf; bundan uzun bir metin ya
	// modelin taşması ya da bir hata. Tavan olmadan tek bir kaçak yanıt
	// state tablosuna megabaytlarca metin yazabilir ve bu tablo FINAL ile
	// okunuyor (kalite paneli, imza okuması) — şişmesi okuma yolunu da
	// yavaşlatır.
	rcaVerdictBodyMax = 8 * 1024
)

// trimRCAVerdictBody — gövdeyi tavana kırpar. SAF.
//
// Kırpma UTF-8 SINIRINDA yapılır: ham `s[:n]` çok baytlı bir karakterin
// ortasından keser ve CH'ye geçersiz UTF-8 yazar (Türkçe metinde bu
// istisna değil, beklenen durum — "ı", "ş", "ğ" hepsi 2 bayt).
//
// Kırpıldığı GÖRÜNÜR ("…"): sessizce kesilmiş bir tanı, tam sanılır.
func trimRCAVerdictBody(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= rcaVerdictBodyMax {
		return s
	}
	cut := rcaVerdictBodyMax
	// Geçerli bir rune sınırına kadar geri sar. utf8.RuneStart false
	// olduğu sürece devam takip baytındayız.
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
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
	// v0.9.1281 — body/source KOŞULLU. Kolonlar henüz inmemişse (küme
	// kipinde DDL ertelendiği için kolonu ekleyen boot bunu false okur)
	// onlarsız yazarız: kayıt yine düşer, yalnız gövde taşımaz. Koşulsuz
	// yazsaydık o boot boyunca HER verdict yazımı code 16 ile ölürdü —
	// yani muhasebe için eklenen kolon, muhasebenin tamamını silerdi.
	cols := `(exchange_id, anchor_kind, anchor_id, service, verdict,
		 rc_entity, rc_fail_mode,
		 confidence, model_conf, hypo_conf, hypo_version,
		 parsed, repaired, shield_notes, created_at)`
	if s.hasRCAVerdictBodyCol {
		cols = `(exchange_id, anchor_kind, anchor_id, service, verdict,
		 rc_entity, rc_fail_mode,
		 confidence, model_conf, hypo_conf, hypo_version,
		 parsed, repaired, shield_notes, body, source, created_at)`
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO rca_verdicts `+cols)
	if err != nil {
		return err
	}
	args := []any{
		v.ExchangeID, v.AnchorKind, v.AnchorID, v.Service, v.Verdict,
		v.RCEntity, v.RCFailMode,
		v.Confidence, v.ModelConf, v.HypoConf, v.HypoVersion,
		boolToUInt8(v.Parsed), boolToUInt8(v.Repaired), notes,
	}
	if s.hasRCAVerdictBodyCol {
		args = append(args, trimRCAVerdictBody(v.Body), v.Source)
	}
	args = append(args, created)
	if err := batch.Append(args...); err != nil {
		return err
	}
	return batch.Send()
}

// LatestRCAVerdictForAnchor — bir ankorun EN YENİ verdict kaydı
// (v0.9.1281). Kayıt yoksa (nil, nil).
//
// Bu okumanın var olma sebebi: verdict metni 30dk'lık explain
// önbelleğinden düştüğünde operatör için hiçbir yerde kalmıyordu. Kart
// ve çekmece artık önbellek ıskasında BURAYA düşüyor — ve bu okuma
// ÜRETİMSİZ, yani "kart otomatik LLM ateşlemez" (A2) kararı aynen
// geçerli: kalıcı bir satırı okumak model çağırmak değildir.
//
// Bounded: rca_verdicts 90g TTL'li küçük bir state tablosu; zaman
// pencereli WHERE + LIMIT 1 + max_execution_time. ORDER BY exchange_id
// olduğu için ankor filtresi bir tarama — tabloda binler mertebesi satır
// var (ai_calls emsali), MV gerektiren bir agregasyon değil.
//
// FINAL şart: ReplacingMergeTree, aynı exchange_id yeniden yazılabilir.
//
// hasRCAVerdictBodyCol false iken body/source SELECT'ten DÜŞER — kolonu
// olmayan tabloda onları istemek okumanın tamamını hataya çevirirdi ve
// gövdesiz de olsa karar okunabilmeli.
func (s *Store) LatestRCAVerdictForAnchor(ctx context.Context, anchorKind, anchorID string) (*RCAVerdictRecord, error) {
	if anchorKind == "" || anchorID == "" {
		return nil, nil
	}
	bodyExpr, sourceExpr := `''`, `''`
	if s.hasRCAVerdictBodyCol {
		bodyExpr, sourceExpr = `v.body`, `v.source`
	}
	row := s.conn.QueryRow(ctx, `
		SELECT v.exchange_id, v.service, v.verdict,
		       v.rc_entity, v.rc_fail_mode,
		       v.confidence, v.model_conf, v.hypo_conf, v.hypo_version,
		       v.parsed, v.repaired, v.shield_notes,
		       `+bodyExpr+` AS body, `+sourceExpr+` AS source,
		       toUnixTimestamp64Nano(v.created_at) AS created_ns
		FROM rca_verdicts AS v FINAL
		WHERE v.anchor_kind = ? AND v.anchor_id = ?
		  AND v.created_at >= ?
		ORDER BY v.created_at DESC
		LIMIT 1
		SETTINGS max_execution_time = 5`,
		anchorKind, anchorID, chDateTime64Arg(time.Now().Add(-rcaSignatureLookback)))

	var (
		rec              RCAVerdictRecord
		parsed, repaired uint8
	)
	if err := row.Scan(&rec.ExchangeID, &rec.Service, &rec.Verdict,
		&rec.RCEntity, &rec.RCFailMode,
		&rec.Confidence, &rec.ModelConf, &rec.HypoConf, &rec.HypoVersion,
		&parsed, &repaired, &rec.ShieldNotes,
		&rec.Body, &rec.Source, &rec.CreatedAt); err != nil {
		// Ev kalıbı (rootcause_hypothesis.go:183, user.go:99): clickhouse-go
		// boş sonucu bu dizeyle bildiriyor. Kayıt yokluğu HATA DEĞİL —
		// çağıran "henüz verdict üretilmemiş" ile "okuma patladı"yı ayırt
		// edebilmeli.
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	rec.AnchorKind, rec.AnchorID = anchorKind, anchorID
	rec.Parsed, rec.Repaired = parsed == 1, repaired == 1
	return &rec, nil
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
// Sayaç alanları uint64 ve bu ZORUNLU: count()/countIf() ClickHouse'ta
// UInt64 döner. int'e taramak DERLENİR, tüm testlerden GEÇER ve yalnız
// gerçek CH'de patlar — bu oturumun baskın hata sınıfı (v0.9.543 struct
// alanı ↔ kolon tipi). ComputeAIStats emsali toUInt64() + uint64 ile
// açıkça eşliyor; aynısı burada.
type RCAVerdictQuality struct {
	Total         uint64  `json:"total"`
	RootCause     uint64  `json:"rootCauseIdentified"`
	Probable      uint64  `json:"probableCause"`
	Insufficient  uint64  `json:"insufficientEvidence"`
	Unparsed      uint64  `json:"unparsed"`
	Repaired      uint64  `json:"repaired"`
	Shielded      uint64  `json:"shielded"`
	AvgConfidence float64 `json:"avgConfidence"`
	ThumbsUp      uint64  `json:"thumbsUp"`
	ThumbsDown    uint64  `json:"thumbsDown"`
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
			-- toUInt64 AÇIK: count()/countIf() zaten UInt64 döner ama
			-- niyeti kaynakta yazmak, struct alanını değiştiren birinin
			-- eşleşmeyi görmesini sağlıyor (ComputeAIStats emsali).
			toUInt64(count())                                          AS total,
			toUInt64(countIf(v.verdict = 'root_cause_identified'))     AS rc,
			toUInt64(countIf(v.verdict = 'probable_cause'))            AS pc,
			toUInt64(countIf(v.verdict = 'insufficient_evidence'))     AS ie,
			toUInt64(countIf(v.parsed = 0))                            AS unparsed,
			toUInt64(countIf(v.repaired = 1))                          AS repaired,
			toUInt64(countIf(length(v.shield_notes) > 0))              AS shielded,
			-- ifNotFinite: boş pencerede avg() NaN döner ve NaN JSON'a
			-- serileştirilemez (sanitizeFloats'a güvenmek yerine
			-- kaynağında kes — bu okuma başka çağıranlara da açık).
			ifNotFinite(toFloat64(avg(v.confidence)), 0)               AS avg_conf,
			toUInt64(countIf(f.verdict = 1))                           AS up,
			toUInt64(countIf(f.verdict = -1))                          AS down
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

// ── LEARN: bilinen imzalar (v0.9.595) ────────────────────────────────
//
// Tasarım dokümanının [6] LEARN katmanı (docs/cosre-verdict-design.md
// §11). Fikir: geçmişte DOĞRULANMIŞ bir kök neden, benzer bir vakada
// modele ÖN BİLGİ olarak verilsin.
//
// TEK NİTELİK ÖLÇÜTÜ: operatörün 👍'sı.
//
// Neden bu kadar dar — ve neden "aynı imza N kez tekrarlandı" ölçütü
// KASTEN reddedildi: o ölçüt kendi kendini besler. Model servis S için
// X önerir, kayda geçer; bir dahaki sefere ön bilgi "X daha önce
// önerilmişti" der; model X'i daha emin önerir; tekrar sayısı büyür.
// Sonunda motor kendi geçmiş tahminlerini kanıt sanar. Modelin
// kendisiyle hemfikir olması hiçbir şeyin kanıtı değildir.
//
// 👍 bu döngüyü kırar çünkü sisteme DIŞARIDAN girer.
//
// Sonucu dürüstçe söylemek gerekir: hiç oy yokken bu okuma BOŞ döner
// ve prompt hiç değişmez. Yani LEARN, operatör oy vermeye başlayana
// kadar ETKİSİZDİR. Bu bir eksiklik değil, tasarımın kendisi — veri
// olmadan öğrenmek, tahmin etmenin süslü adıdır.

// RCASignature — geçmişte DOĞRULANMIŞ bir kök neden.
type RCASignature struct {
	Service     string `json:"service"`
	Entity      string `json:"entity"`
	FailureMode string `json:"failureMode"`
	// Confirmations — kaç AYRI vakada 👍 aldı. Ayrı vaka şartı
	// önemli: aynı vakanın tekrar tekrar oylanması bir örüntü değil.
	// uint64 — uniqExact() UInt64 döner (yukarıdaki tip notunun aynısı).
	Confirmations uint64 `json:"confirmations"`
	// LastSeen — unix ns. Bayat bir imza, taze olandan az şey söyler.
	LastSeen int64 `json:"lastSeen"`
}

const (
	// rcaSignatureLookback — imzalar ne kadar geriye bakar.
	// rca_verdicts TTL'i 90 gün; ondan uzun bir pencere anlamsız.
	rcaSignatureLookback = 90 * 24 * time.Hour
	// rcaSignatureLimit — prompt'a en fazla kaç imza taşınır.
	// 5: küçük modelde uzun ön bilgi listesi asıl kanıtı bastırır ve
	// model geçmişi bugüne tercih etmeye başlar.
	rcaSignatureLimit = 5
)

// ConfirmedRCASignatures — bir servis için DOĞRULANMIŞ kök nedenler.
//
// Yalnız 👍 almış verdict'ler; 👎 almış ya da hiç oylanmamışlar
// dışarıda. INNER JOIN bilinçli (kalite okumasındaki LEFT JOIN'in
// tersi): orada amaç her verdict'i saymaktı, burada amaç yalnız
// onaylananları almak.
//
// Bounded: küçük TTL'li state tablosu, servis + zaman WHERE, LIMIT,
// max_execution_time. FINAL şart — hem rca_verdicts hem ai_feedback
// ReplacingMergeTree ve operatör oyunu değiştirebilir.
func (s *Store) ConfirmedRCASignatures(ctx context.Context, service string, now time.Time) ([]RCASignature, error) {
	if service == "" {
		return nil, nil
	}
	rows, err := s.conn.Query(ctx, `
		SELECT v.service, v.rc_entity, v.rc_fail_mode,
		       -- AYRI vaka sayısı: aynı ankorun tekrar tekrar
		       -- oylanması bir örüntü değil, tek bir gözlemdir.
		       toUInt64(uniqExact(v.anchor_id)) AS confirmations,
		       toUnixTimestamp64Nano(max(v.created_at)) AS last_seen
		FROM rca_verdicts AS v FINAL
		INNER JOIN (
			SELECT exchange_id FROM ai_feedback FINAL WHERE verdict = 1
		) AS f ON f.exchange_id = v.exchange_id
		WHERE v.service = ? AND v.created_at >= ?
		  AND v.rc_entity != '' AND v.verdict != 'insufficient_evidence'
		GROUP BY v.service, v.rc_entity, v.rc_fail_mode
		ORDER BY confirmations DESC, last_seen DESC
		LIMIT ?
		SETTINGS max_execution_time = 5`,
		service, chDateTime64Arg(now.Add(-rcaSignatureLookback)), rcaSignatureLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RCASignature
	for rows.Next() {
		var sig RCASignature
		if err := rows.Scan(&sig.Service, &sig.Entity, &sig.FailureMode,
			&sig.Confirmations, &sig.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, sig)
	}
	return out, rows.Err()
}
