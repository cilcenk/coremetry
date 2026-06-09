package chstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

type Feedback struct {
	ID        string `json:"id"`
	UserID    string `json:"userId"`
	UserEmail string `json:"userEmail"`
	Message   string `json:"message"`
	CreatedAt int64  `json:"createdAt"` // unix ns
}

const feedbackInsertCols = "id, user_id, user_email, message, created_at"

func (s *Store) InsertFeedback(ctx context.Context, f Feedback) (Feedback, error) {
	if f.ID == "" {
		b := make([]byte, 8)
		rand.Read(b)
		f.ID = hex.EncodeToString(b)
	}
	if f.CreatedAt == 0 {
		f.CreatedAt = time.Now().UnixNano()
	}
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO feedbacks ("+feedbackInsertCols+")")
	if err != nil {
		return Feedback{}, err
	}
	if err := batch.Append(f.ID, f.UserID, f.UserEmail, f.Message, time.Unix(0, f.CreatedAt)); err != nil {
		return Feedback{}, err
	}
	return f, batch.Send()
}

func (s *Store) ListFeedbacks(ctx context.Context, limit, offset int) ([]Feedback, bool, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	// Fetch one extra row to determine whether more pages exist.
	rows, err := s.conn.Query(ctx, `
		SELECT id, user_id, user_email, message, created_at
		FROM feedbacks FINAL
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?
		SETTINGS max_execution_time = 10`,
		limit+1, offset,
	)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var out []Feedback
	for rows.Next() {
		var f Feedback
		var createdAt time.Time
		if err := rows.Scan(&f.ID, &f.UserID, &f.UserEmail, &f.Message, &createdAt); err != nil {
			return nil, false, err
		}
		f.CreatedAt = createdAt.UnixNano()
		out = append(out, f)
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}
