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
	Surface    string `json:"surface"`             // resolved server-side from the ai_calls row
	Verdict    int8   `json:"verdict"`             // 1 = thumbs up, -1 = thumbs down
	UserEmail  string `json:"userEmail,omitempty"` // who rated (full fidelity, house policy)
	CreatedAt  int64  `json:"createdAt"`           // unix ns
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
		(exchange_id, surface, verdict, user_email, created_at)`)
	if err != nil {
		return err
	}
	if err := batch.Append(f.ExchangeID, f.Surface, f.Verdict, f.UserEmail, created); err != nil {
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
