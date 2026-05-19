package chstore

import (
	"context"
	"fmt"
)

type User struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"`         // admin | editor | viewer
	Disabled     bool   `json:"disabled"`
	AuthProvider string `json:"authProvider"` // local | oidc — drives "Change password" UI
	// Team — free-text grouping the admin picks (e.g. "platform-sre",
	// "fraud", "payments"). Backed by a LowCardinality column so
	// repeated values are cheap. Empty when unassigned; the
	// users-list UI groups "Unassigned" separately so an admin
	// can see who still needs labelling.
	Team      string `json:"team"`
	// CustomRole — optional pointer into the auth.Service custom-role
	// catalog. Only meaningful when Role == viewer; otherwise ignored
	// (admin/editor get no further restriction). Empty = no custom
	// role, unrestricted viewer.
	CustomRole string `json:"customRole,omitempty"`
	CreatedAt int64  `json:"createdAt"` // unix nanoseconds
}

// GetUserByEmail returns the latest version of a user (ReplacingMergeTree FINAL).
// Returns (nil, nil) when no row matches — callers treat that as "unknown user".
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT id, email, password_hash, role, disabled, auth_provider, team, custom_role,
		       toUnixTimestamp64Nano(created_at)
		FROM users FINAL
		WHERE email = ? AND disabled = 0
		LIMIT 1`, email)
	var u User
	var disabled uint8
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &disabled, &u.AuthProvider, &u.Team, &u.CustomRole, &u.CreatedAt); err != nil {
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
		SELECT id, email, password_hash, role, disabled, auth_provider, team, custom_role,
		       toUnixTimestamp64Nano(created_at)
		FROM users FINAL
		WHERE id = ? AND disabled = 0
		LIMIT 1`, id)
	var u User
	var disabled uint8
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &disabled, &u.AuthProvider, &u.Team, &u.CustomRole, &u.CreatedAt); err != nil {
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
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO users (id, email, password_hash, role, disabled, auth_provider, team, custom_role)")
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
	// Custom role only applies when base role is viewer; defensively
	// clear it for admin/editor so a stale assignment doesn't surface
	// after a role promotion.
	custom := u.CustomRole
	if u.Role != "viewer" {
		custom = ""
	}
	if err := batch.Append(u.ID, u.Email, u.PasswordHash, u.Role, dis, provider, u.Team, custom); err != nil {
		return fmt.Errorf("append user: %w", err)
	}
	return batch.Send()
}

// ListUsers returns every active user, newest first. Disabled users are
// hidden — they're effectively deleted from the UI's perspective.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, email, password_hash, role, disabled, auth_provider, team, custom_role,
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
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &disabled, &u.AuthProvider, &u.Team, &u.CustomRole, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.Disabled = disabled != 0
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListUsersByTeam returns every active user whose team field
// matches the requested label (case-insensitive). Drives the
// /service?name=… page's "owner team members" popover so an
// operator viewing a service can see who to ping. Bounded list
// (users < 10k in any realistic install) so no LIMIT; ordering
// by email keeps the popover deterministic across refreshes.
func (s *Store) ListUsersByTeam(ctx context.Context, team string) ([]User, error) {
	if team == "" {
		return nil, nil
	}
	rows, err := s.conn.Query(ctx, `
		SELECT id, email, password_hash, role, disabled, auth_provider, team, custom_role,
		       toUnixTimestamp64Nano(created_at)
		FROM users FINAL
		WHERE disabled = 0 AND lower(team) = lower(?)
		ORDER BY email`, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var disabled uint8
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &disabled, &u.AuthProvider, &u.Team, &u.CustomRole, &u.CreatedAt); err != nil {
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

// SetUserTeam updates the user's team label. Re-uses the
// upsert pipeline so ReplacingMergeTree picks up the new row
// as the latest version. Empty string clears the assignment
// (the UI groups those under "Unassigned").
func (s *Store) SetUserTeam(ctx context.Context, userID, team string) error {
	u, err := s.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if u == nil {
		return fmt.Errorf("user not found")
	}
	u.Team = team
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
