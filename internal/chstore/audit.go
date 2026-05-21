package chstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// AuditEntry is one row in the audit_log table — append-only,
// representing a state-changing action by a user. Read by the
// /admin/audit page.
type AuditEntry struct {
	ID          string `json:"id"`
	Time        int64  `json:"time"` // unix ns
	ActorID     string `json:"actorId"`
	ActorEmail  string `json:"actorEmail"`
	ActorRole   string `json:"actorRole"`
	Action      string `json:"action"`     // e.g. "alert_rule.update"
	TargetKind  string `json:"targetKind"` // e.g. "alert_rule"
	TargetID    string `json:"targetId"`
	IP          string `json:"ip"`
	Details     string `json:"details"`    // JSON or freeform
}

// AppendAuditBatch writes N entries in a single CH INSERT. ID
// + Time defaults are applied per-row. Used by the API
// server's background drainer (v0.5.339) so a burst of admin
// actions doesn't fan out into N goroutines + N one-row
// INSERTs — both wasteful at scale.
func (s *Store) AppendAuditBatch(ctx context.Context, entries []AuditEntry) error {
	if len(entries) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO audit_log")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.ID == "" {
			e.ID = newAuditID()
		}
		if e.Time == 0 {
			e.Time = time.Now().UnixNano()
		}
		if err := batch.Append(
			e.ID, time.Unix(0, e.Time),
			e.ActorID, e.ActorEmail, e.ActorRole,
			e.Action, e.TargetKind, e.TargetID,
			e.IP, e.Details,
		); err != nil {
			return err
		}
	}
	return batch.Send()
}

// AppendAudit writes one entry. Best-effort — callers fire
// without checking the error since auth-success paths shouldn't
// be blocked by audit-write failure (we log internally).
func (s *Store) AppendAudit(ctx context.Context, e AuditEntry) error {
	if e.ID == "" {
		e.ID = newAuditID()
	}
	if e.Time == 0 {
		e.Time = time.Now().UnixNano()
	}
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO audit_log")
	if err != nil {
		return err
	}
	if err := batch.Append(
		e.ID, time.Unix(0, e.Time),
		e.ActorID, e.ActorEmail, e.ActorRole,
		e.Action, e.TargetKind, e.TargetID,
		e.IP, e.Details,
	); err != nil {
		return err
	}
	return batch.Send()
}

type AuditFilter struct {
	SinceNs    int64  // unix ns; 0 = last 24h
	Actor      string // user id OR email substring; empty = all
	Action     string // exact match; empty = all
	TargetKind string // exact match; empty = all
	Limit      int
}

func (s *Store) ListAuditLog(ctx context.Context, f AuditFilter) ([]AuditEntry, error) {
	if f.Limit == 0 {
		f.Limit = 200
	}
	since := f.SinceNs
	if since == 0 {
		since = time.Now().Add(-24 * time.Hour).UnixNano()
	}
	var wc whereClause
	wc.add("toUnixTimestamp64Nano(time) >= ?", since)
	if f.Actor != "" {
		wc.add("(actor_id = ? OR positionCaseInsensitive(actor_email, ?) > 0)", f.Actor, f.Actor)
	}
	if f.Action != "" {
		wc.add("action = ?", f.Action)
	}
	if f.TargetKind != "" {
		wc.add("target_kind = ?", f.TargetKind)
	}
	rows, err := s.conn.Query(ctx, `
		SELECT id, toUnixTimestamp64Nano(time),
		       actor_id, actor_email, actor_role,
		       action, target_kind, target_id, ip, details
		FROM audit_log `+wc.sql()+`
		ORDER BY time DESC
		LIMIT ?`, append(wc.args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(
			&e.ID, &e.Time,
			&e.ActorID, &e.ActorEmail, &e.ActorRole,
			&e.Action, &e.TargetKind, &e.TargetID, &e.IP, &e.Details,
		); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func newAuditID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
