package chstore

import (
	"context"
	"time"
)

// AI answer feedback (v0.8.399, AI audit feedback slice). One row per
// rated exchange: the chat handler mints an exchange_id per answer,
// the UI's thumbs up/down posts it back, and the row lands here.
// ReplacingMergeTree(version) ORDER BY exchange_id — the user can
// change their mind; the latest verdict wins on FINAL reads. The /ai
// page aggregates this into a per-surface thumbs-up rate next to the
// call stats. Provider-agnostic: pure correlation plumbing, nothing
// here knows which LLM answered.

// AIFeedback is one operator verdict on one AI answer.
type AIFeedback struct {
	ExchangeID string `json:"exchangeId"`
	Surface    string `json:"surface"` // resolved server-side from the ai_calls row
	Verdict    int8   `json:"verdict"` // 1 = thumbs up, -1 = thumbs down
	// Comment (v0.9.1193, Faz 5.1) — 👎'nin serbest metni. Tam-satır
	// replace: her Upsert bu alanı da taşır; flip'te korunması çağıranın
	// (API preserve yolu) işi.
	Comment   string `json:"comment,omitempty"`
	UserEmail string `json:"userEmail,omitempty"` // who rated (full fidelity, house policy)
	CreatedAt int64  `json:"createdAt"`           // unix ns
}

// UpsertAIFeedback inserts a verdict row. ReplacingMergeTree dedup by
// exchange_id means re-rating the same answer is a whole-row replace
// (all fields carried forward by the caller, house rule) — no ALTER
// UPDATE, no read-modify-write.
func (s *Store) UpsertAIFeedback(ctx context.Context, f AIFeedback) error {
	created := time.Now().UTC()
	if f.CreatedAt > 0 {
		created = time.Unix(0, f.CreatedAt).UTC()
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO ai_feedback
		(exchange_id, surface, verdict, comment, user_email, created_at)`)
	if err != nil {
		return err
	}
	if err := batch.Append(f.ExchangeID, f.Surface, f.Verdict, f.Comment, f.UserEmail, created); err != nil {
		return err
	}
	return batch.Send()
}

// AICallSurfaceByExchange resolves the surface label of the ai_calls
// row a feedback POST refers to — server-side, so the client can't
// mislabel a verdict, and so an unknown exchangeId is detectable
// (returns ""). ai_calls is a small 90d-TTL table; the unindexed
// exchange_id filter follows the GetAICall `WHERE id = ?` precedent,
// bounded the same way.
func (s *Store) AICallSurfaceByExchange(ctx context.Context, exchangeID string) (string, error) {
	if exchangeID == "" {
		return "", nil
	}
	rows, err := s.conn.Query(ctx, `
		SELECT surface FROM ai_calls
		WHERE exchange_id = ?
		ORDER BY created_at DESC
		LIMIT 1
		SETTINGS max_execution_time = 5`, exchangeID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if rows.Next() {
		var surface string
		if err := rows.Scan(&surface); err != nil {
			return "", err
		}
		return surface, nil
	}
	return "", rows.Err()
}

// aiFeedbackAgg is one surface's verdict tally over a window.
type aiFeedbackAgg struct {
	Total uint64
	Up    uint64
}

// aiFeedbackBySurface tallies latest-verdict-wins feedback per surface
// for ComputeAIStats. FINAL (ReplacingMergeTree house rule) so a
// re-rated exchange counts once, with its newest verdict. Bounded:
// tiny state table, window-filtered, LIMIT 50 mirrors the surface
// breakdown's own cap.
func (s *Store) aiFeedbackBySurface(ctx context.Context, from, to time.Time) (map[string]aiFeedbackAgg, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT surface,
		       toUInt64(count()),
		       toUInt64(countIf(verdict = 1))
		FROM ai_feedback FINAL
		WHERE created_at >= toDateTime64(?, 9, 'UTC')
		  AND created_at <  toDateTime64(?, 9, 'UTC')
		GROUP BY surface
		LIMIT 50
		SETTINGS max_execution_time = 5`,
		chDateTime64Arg(from), chDateTime64Arg(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]aiFeedbackAgg)
	for rows.Next() {
		var surface string
		var agg aiFeedbackAgg
		if err := rows.Scan(&surface, &agg.Total, &agg.Up); err != nil {
			return nil, err
		}
		out[surface] = agg
	}
	return out, rows.Err()
}

// NegativeFeedbackCall — 👎 alan bir cevabın madencilik satırı (v0.9.423).
type NegativeFeedbackCall struct {
	Surface   string `json:"surface"`
	CreatedAt int64  `json:"createdAt"` // unix ns
	UserEmail string `json:"userEmail,omitempty"`
	Prompt    string `json:"prompt"`
	Response  string `json:"response,omitempty"`
	// Comment (v0.9.1193) — operatörün 👎'ye eklediği neden. Madenciliğin
	// asıl sinyali: prompt neyin SORULDUĞUNU, yorum neyin EKSİK olduğunu
	// söyler.
	Comment string `json:"comment,omitempty"`
}

// ListNegativeFeedbackCalls (v0.9.423, CoSRE fikir #6) — pencere içindeki
// verdict=-1 feedback'leri ai_calls örnekleriyle birleştirir: hangi soru
// şekilleri kötü cevap alıyor → yeni guided-intent adayları VERİDEN çıkar.
// İki tablo da küçük state tablosu (ai_feedback ReplacingMergeTree 90g TTL,
// ai_calls örnekleri 4KB cap'li) — JOIN hot-path değil, admin paneli okuması.
func (s *Store) ListNegativeFeedbackCalls(ctx context.Context, from, to time.Time, limit int) ([]NegativeFeedbackCall, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.conn.Query(ctx, `
		SELECT f.surface, toUnixTimestamp64Nano(f.created_at), f.user_email,
		       c.prompt_sample, c.response_sample, f.comment
		FROM ai_feedback AS f FINAL
		LEFT JOIN ai_calls AS c ON c.exchange_id = f.exchange_id
		WHERE f.verdict = -1 AND f.created_at >= ? AND f.created_at <= ?
		ORDER BY f.created_at DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NegativeFeedbackCall
	for rows.Next() {
		var r NegativeFeedbackCall
		if err := rows.Scan(&r.Surface, &r.CreatedAt, &r.UserEmail, &r.Prompt, &r.Response, &r.Comment); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AIFeedbackCommentByExchange — flip'te yorumun KORUNMASI için nokta
// okuma (v0.9.1193). ReplacingMergeTree tam-satır replace: yorum
// göndermeyen bir re-rate (👎→👍 ya da başka yüzeyden tık) yeni sürümü
// yorumsuz yazar ve FINAL yorumlu satırı DÜŞÜRÜRDÜ — operatörün yazdığı
// metin bir tık yüzünden kaybolurdu. API, gövdede comment alanı HİÇ
// yoksa saklananı buradan taşır. Küçük state tablosu, FINAL nokta
// okuması; soft-fail çağıranda (yorum kaybı, POST düşürmekten ucuz).
func (s *Store) AIFeedbackCommentByExchange(ctx context.Context, exchangeID string) (string, error) {
	if exchangeID == "" {
		return "", nil
	}
	rows, err := s.conn.Query(ctx, `
		SELECT comment FROM ai_feedback FINAL
		WHERE exchange_id = ?
		LIMIT 1
		SETTINGS max_execution_time = 5`, exchangeID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return "", err
		}
		return c, nil
	}
	return "", rows.Err()
}
