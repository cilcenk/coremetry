package chstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// TraceSnapshot is a public, time-boxed share token for a trace.
// Created via POST /api/trace/{id}/share, resolved by the public
// route GET /api/public/trace/{token}.
type TraceSnapshot struct {
	Token     string `json:"token"`
	TraceID   string `json:"traceId"`
	CreatedBy string `json:"createdBy,omitempty"`
	CreatedAt int64  `json:"createdAt"`           // unix ns
	ExpiresAt int64  `json:"expiresAt"`           // unix ns
}

// NewSnapshotToken generates a 16-byte URL-safe hex token. Long
// enough that random guessing is computationally infeasible — same
// shape we use for OIDC nonces and heartbeat tokens.
func NewSnapshotToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// CreateTraceSnapshot persists a new (or rotates an existing) share
// token. We don't dedupe — calling twice for the same trace mints
// two unrelated tokens, which is fine: revoking one doesn't break
// the other.
func (s *Store) CreateTraceSnapshot(ctx context.Context, snap TraceSnapshot) error {
	if snap.Token == "" {
		snap.Token = NewSnapshotToken()
	}
	now := time.Now().UnixNano()
	if snap.CreatedAt == 0 {
		snap.CreatedAt = now
	}
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO trace_snapshots (token, trace_id, created_by, created_at, expires_at)")
	if err != nil {
		return fmt.Errorf("prepare snapshot: %w", err)
	}
	if err := batch.Append(
		snap.Token,
		snap.TraceID,
		snap.CreatedBy,
		time.Unix(0, snap.CreatedAt).UTC(),
		time.Unix(0, snap.ExpiresAt).UTC(),
	); err != nil {
		return fmt.Errorf("append snapshot: %w", err)
	}
	return batch.Send()
}

// GetTraceSnapshot returns (snapshot, nil) for a valid + unexpired
// token, (nil, nil) for "not found / already expired" — caller
// translates both to a 404 so we don't leak which case it was.
func (s *Store) GetTraceSnapshot(ctx context.Context, token string) (*TraceSnapshot, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT token, trace_id, created_by,
		       toUnixTimestamp64Nano(created_at),
		       toUnixTimestamp64Nano(expires_at)
		FROM trace_snapshots FINAL
		WHERE token = ?
		LIMIT 1`, token)
	var snap TraceSnapshot
	if err := row.Scan(&snap.Token, &snap.TraceID, &snap.CreatedBy, &snap.CreatedAt, &snap.ExpiresAt); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	if snap.ExpiresAt > 0 && time.Now().UnixNano() > snap.ExpiresAt {
		return nil, nil
	}
	return &snap, nil
}
