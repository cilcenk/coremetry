package chstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Event — operator-marked moment in time. v0.5.476.
//
// Surfaces as a vertical marker on every time-series chart in
// Coremetry (frontend v0.5.477). Examples operators mark:
//
//   - kind="deploy"      label="payments v1.2.3" service="payments"
//   - kind="config"      label="feature flag X enabled"
//   - kind="incident"    label="incident-5 opened"
//   - kind="maintenance" label="DB upgrade window"
//
// Time vs CreatedAt: Time is when the event HAPPENED (operator
// supplies — defaults to now if unset). CreatedAt is when the
// row was inserted (audit trail). Most events are marked at
// creation, so the two match; backfill cases (operator adds a
// missed deploy event later) is why we keep them separate.
type Event struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`     // deploy | config | incident | maintenance | custom
	Label     string `json:"label"`
	Time      int64  `json:"time"`     // unix ns; when it happened
	Service   string `json:"service"`  // optional service scope ("" = global)
	Link      string `json:"link"`     // optional URL
	Owner     string `json:"owner"`    // creator email
	CreatedAt int64  `json:"createdAt"`
}

// EventFilter — read-side cut for /api/events.
type EventFilter struct {
	From    time.Time // inclusive; zero = unbounded
	To      time.Time // exclusive; zero = unbounded
	Service string    // exact match; "" = all
	Kind    string    // exact match; "" = all
	Limit   int       // 0 = 200
}

func (s *Store) UpsertEvent(ctx context.Context, e Event) (Event, error) {
	if e.ID == "" {
		e.ID = newEventID()
	}
	if e.Time == 0 {
		e.Time = time.Now().UnixNano()
	}
	if e.CreatedAt == 0 {
		e.CreatedAt = time.Now().UnixNano()
	}
	if e.Kind == "" {
		e.Kind = "custom"
	}
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO events")
	if err != nil {
		return e, err
	}
	if err := batch.Append(
		e.ID, e.Kind, e.Label,
		time.Unix(0, e.Time), e.Service, e.Link, e.Owner,
		time.Unix(0, e.CreatedAt),
		uint64(time.Now().UnixNano()),
	); err != nil {
		return e, err
	}
	if err := batch.Send(); err != nil {
		return e, err
	}
	return e, nil
}

func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]Event, error) {
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	conds := []string{}
	args := []any{}
	if !f.From.IsZero() {
		conds = append(conds, "time >= ?")
		args = append(args, f.From)
	}
	if !f.To.IsZero() {
		conds = append(conds, "time < ?")
		args = append(args, f.To)
	}
	if f.Service != "" {
		conds = append(conds, "(service = ? OR service = '')")
		args = append(args, f.Service)
	}
	if f.Kind != "" {
		conds = append(conds, "kind = ?")
		args = append(args, f.Kind)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + joinAnd(conds)
	}
	q := `
		SELECT id, kind, label,
		       toUnixTimestamp64Nano(time),
		       service, link, owner,
		       toUnixTimestamp64Nano(created_at)
		FROM events FINAL ` + where + `
		ORDER BY time DESC
		LIMIT ?
		SETTINGS max_execution_time = 5`
	args = append(args, limit)
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Kind, &e.Label,
			&e.Time, &e.Service, &e.Link, &e.Owner, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) DeleteEvent(ctx context.Context, id string) error {
	// ReplacingMergeTree: insert a tombstone with a higher
	// version, but events table doesn't carry a Deleted column —
	// simplest cleanup is a direct ALTER … DELETE which CH
	// handles in the background.
	return s.conn.Exec(ctx,
		`ALTER TABLE events DELETE WHERE id = ?`, id)
}

func newEventID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "ev-" + hex.EncodeToString(b)
}

func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}
