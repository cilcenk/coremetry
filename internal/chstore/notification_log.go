package chstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NotificationLog is one dispatched notification — the append-only
// audit trail of every channel send (success AND failure) fanned out
// by internal/notify. v0.8.241.
//
// The row is IMMUTABLE once written (a send happened at a point in
// time), so the engine is a plain MergeTree — NOT ReplacingMergeTree.
// Reads never use FINAL. Retained 90 days via a partition-aligned day
// TTL (see the CREATE TABLE in store.go migrate()).
type NotificationLog struct {
	ID          string `json:"id"`
	SentAt      int64  `json:"sentAt"`      // unix ns
	ChannelKind string `json:"channelKind"` // email|slack|mattermost|teams|zoomchat|webhook|whatsapp
	ChannelName string `json:"channelName"`
	Target      string `json:"target"` // full-fidelity recipient (operator policy); webhook URLs host-only (URL embeds a live credential)
	Subject     string `json:"subject"`
	BodyPreview string `json:"bodyPreview"` // first ~200 chars of the notification body
	RelatedKind string `json:"relatedKind"` // problem|test|runbook|incident|alert|monitor|…
	RelatedID   string `json:"relatedId"`
	OK          bool   `json:"ok"`
	Error       string `json:"error"`
}

// InsertNotificationLog appends one send record. Fire-and-forget from
// the notify funnel — the caller logs-and-continues on error so a
// record failure never blocks (or re-fires) the notification itself.
// Uses the async_insert context like every other write path.
func (s *Store) InsertNotificationLog(ctx context.Context, e NotificationLog) error {
	if e.ID == "" {
		e.ID = newNotificationLogID()
	}
	if e.SentAt == 0 {
		e.SentAt = time.Now().UnixNano()
	}
	ctx = asyncInsertCtx(ctx)
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO notification_log")
	if err != nil {
		return err
	}
	ok := uint8(0)
	if e.OK {
		ok = 1
	}
	// Value order MUST match the notification_log CREATE TABLE column
	// order in store.go (id … error).
	if err := batch.Append(
		e.ID,
		time.Unix(0, e.SentAt),
		e.ChannelKind,
		e.ChannelName,
		e.Target,
		e.Subject,
		e.BodyPreview,
		e.RelatedKind,
		e.RelatedID,
		ok,
		e.Error,
	); err != nil {
		return err
	}
	return batch.Send()
}

// ListNotificationLog reads the dispatch history newest-first. Always
// time-bounded (a zero from/to defaults to the full 90-day retention
// window) so the read carries a prefix predicate on the sent_at ORDER
// BY key — never a full-table scan. kind filters on channel_kind
// exactly; "" = all channels.
func (s *Store) ListNotificationLog(ctx context.Context, from, to time.Time, kind string, limit, offset int) ([]NotificationLog, error) {
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		// Default to the retention window rather than unbounded so the
		// query keeps its time-prefix predicate.
		from = to.Add(-90 * 24 * time.Hour)
	}
	q, args := buildNotificationLogQuery(from, to, kind, limit, offset)
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotificationLog
	for rows.Next() {
		var e NotificationLog
		var ok uint8
		if err := rows.Scan(
			&e.ID, &e.SentAt, &e.ChannelKind, &e.ChannelName,
			&e.Target, &e.Subject, &e.BodyPreview,
			&e.RelatedKind, &e.RelatedID, &ok, &e.Error,
		); err != nil {
			return nil, err
		}
		e.OK = ok == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// buildNotificationLogQuery is the pure SQL builder for
// ListNotificationLog — extracted so the mandatory read bounds
// (time-bounded WHERE on the sent_at ORDER BY prefix + LIMIT +
// max_execution_time) are table-testable without a live CH. from/to
// are assumed already normalised (non-zero) by the caller.
func buildNotificationLogQuery(from, to time.Time, kind string, limit, offset int) (string, []any) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	conds := []string{"sent_at >= ?", "sent_at < ?"}
	args := []any{from, to}
	if kind != "" {
		conds = append(conds, "channel_kind = ?")
		args = append(args, kind)
	}
	q := `
		SELECT id,
		       toUnixTimestamp64Nano(sent_at),
		       channel_kind, channel_name, target,
		       subject, body_preview,
		       related_kind, related_id, ok, error
		FROM notification_log
		WHERE ` + joinAnd(conds) + `
		ORDER BY sent_at DESC
		LIMIT ? OFFSET ?
		SETTINGS max_execution_time = 10`
	args = append(args, limit, offset)
	return q, args
}

func newNotificationLogID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "nl-" + hex.EncodeToString(b)
}
