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
	// LogsJSON — the trace's log lines captured AT SHARE TIME
	// (v0.8.252), pre-marshalled JSON array. The public viewer serves
	// this frozen copy: no live logstore query on the anonymous route,
	// and the share keeps its logs even after log-retention TTLs eat
	// the originals. Empty = shared before v0.8.252 or capture failed.
	LogsJSON string `json:"-"`
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
		"INSERT INTO trace_snapshots (token, trace_id, created_by, created_at, expires_at, logs)")
	if err != nil {
		return fmt.Errorf("prepare snapshot: %w", err)
	}
	if err := batch.Append(
		snap.Token,
		snap.TraceID,
		snap.CreatedBy,
		time.Unix(0, snap.CreatedAt).UTC(),
		time.Unix(0, snap.ExpiresAt).UTC(),
		snap.LogsJSON,
	); err != nil {
		return fmt.Errorf("append snapshot: %w", err)
	}
	return batch.Send()
}

// ListTraceSnapshots returns every active (unexpired) snapshot
// for one trace_id, ordered by most recent first. Drives the
// admin "manage shares" panel — operator sees what's out there
// AND who minted each one. Capped at 50 entries per trace to
// keep the response small (operationally there should never be
// more than 1-2 active per trace anyway).
func (s *Store) ListTraceSnapshots(ctx context.Context, traceID string) ([]TraceSnapshot, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT token, trace_id, created_by,
		       toUnixTimestamp64Nano(created_at),
		       toUnixTimestamp64Nano(expires_at)
		FROM trace_snapshots FINAL
		WHERE trace_id = ? AND expires_at > now64()
		ORDER BY created_at DESC
		LIMIT 50`, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TraceSnapshot
	for rows.Next() {
		var snap TraceSnapshot
		if err := rows.Scan(&snap.Token, &snap.TraceID, &snap.CreatedBy,
			&snap.CreatedAt, &snap.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, rows.Err()
}

// RevokeTraceSnapshot immediately invalidates a share token by
// setting its expires_at to now. The ReplacingMergeTree
// (version-keyed) takes the higher version on read, so the
// next GetTraceSnapshot returns nil → 404 on the public route.
// Idempotent — revoking an already-expired token is a no-op.
func (s *Store) RevokeTraceSnapshot(ctx context.Context, token string) error {
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO trace_snapshots (token, trace_id, created_by, created_at, expires_at, logs)")
	if err != nil {
		return fmt.Errorf("prepare revoke: %w", err)
	}
	// We need the existing row's trace_id / created_by /
	// created_at to preserve audit semantics. Fetch first.
	prev, err := s.GetTraceSnapshot(ctx, token)
	if err != nil {
		return err
	}
	if prev == nil {
		return nil // already gone
	}
	now := time.Now().UTC()
	if err := batch.Append(
		prev.Token, prev.TraceID, prev.CreatedBy,
		time.Unix(0, prev.CreatedAt).UTC(),
		now, // expires_at = now → instant expiry
		prev.LogsJSON,
	); err != nil {
		return fmt.Errorf("append revoke: %w", err)
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
		       toUnixTimestamp64Nano(expires_at),
		       logs
		FROM trace_snapshots FINAL
		WHERE token = ?
		LIMIT 1`, token)
	var snap TraceSnapshot
	if err := row.Scan(&snap.Token, &snap.TraceID, &snap.CreatedBy, &snap.CreatedAt, &snap.ExpiresAt, &snap.LogsJSON); err != nil {
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
