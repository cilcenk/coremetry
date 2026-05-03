package chstore

import (
	"context"
	"fmt"
)

type User struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"`         // admin | viewer
	Disabled     bool   `json:"disabled"`
	AuthProvider string `json:"authProvider"` // local | oidc — drives "Change password" UI
	CreatedAt    int64  `json:"createdAt"`    // unix nanoseconds
}

// GetUserByEmail returns the latest version of a user (ReplacingMergeTree FINAL).
// Returns (nil, nil) when no row matches — callers treat that as "unknown user".
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT id, email, password_hash, role, disabled, auth_provider,
		       toUnixTimestamp64Nano(created_at)
		FROM users FINAL
		WHERE email = ? AND disabled = 0
		LIMIT 1`, email)
	var u User
	var disabled uint8
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &disabled, &u.AuthProvider, &u.CreatedAt); err != nil {
		// clickhouse-go returns sql.ErrNoRows analogue; surface as nil/nil.
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	u.Disabled = disabled != 0
	return &u, nil
}

func (s *Store) GetUserByID(ctx context.Context, id string) (*User, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT id, email, password_hash, role, disabled, auth_provider,
		       toUnixTimestamp64Nano(created_at)
		FROM users FINAL
		WHERE id = ? AND disabled = 0
		LIMIT 1`, id)
	var u User
	var disabled uint8
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &disabled, &u.AuthProvider, &u.CreatedAt); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	u.Disabled = disabled != 0
	return &u, nil
}

func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	row := s.conn.QueryRow(ctx, `SELECT count() FROM users FINAL WHERE disabled = 0`)
	var n uint64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return int64(n), nil
}

func (s *Store) UpsertUser(ctx context.Context, u User) error {
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO users (id, email, password_hash, role, disabled, auth_provider)")
	if err != nil {
		return fmt.Errorf("prepare users: %w", err)
	}
	var dis uint8
	if u.Disabled {
		dis = 1
	}
	provider := u.AuthProvider
	if provider == "" {
		provider = "local"
	}
	if err := batch.Append(u.ID, u.Email, u.PasswordHash, u.Role, dis, provider); err != nil {
		return fmt.Errorf("append user: %w", err)
	}
	return batch.Send()
}

// ListUsers returns every active user, newest first. Disabled users are
// hidden — they're effectively deleted from the UI's perspective.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, email, password_hash, role, disabled, auth_provider,
		       toUnixTimestamp64Nano(created_at)
		FROM users FINAL
		WHERE disabled = 0
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var disabled uint8
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &disabled, &u.AuthProvider, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.Disabled = disabled != 0
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountAdmins is used to reject the "disable / demote the last admin"
// case before it can lock everyone out.
func (s *Store) CountAdmins(ctx context.Context) (int64, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT count() FROM users FINAL
		WHERE disabled = 0 AND role = 'admin'`)
	var n uint64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return int64(n), nil
}

// UpdatePassword writes a new bcrypt hash for an existing user. Other
// fields are preserved by reading them first — needed because ReplacingMergeTree
// replaces the whole row on insert.
func (s *Store) UpdatePassword(ctx context.Context, userID, newHash string) error {
	u, err := s.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if u == nil {
		return fmt.Errorf("user not found")
	}
	u.PasswordHash = newHash
	return s.UpsertUser(ctx, *u)
}

// DisableUser soft-deletes by inserting a new row with disabled=1.
func (s *Store) DisableUser(ctx context.Context, userID string) error {
	u, err := s.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if u == nil {
		return fmt.Errorf("user not found")
	}
	u.Disabled = true
	return s.UpsertUser(ctx, *u)
}
