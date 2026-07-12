package chstore

import (
	"context"
	"fmt"
	"time"
)

type User struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"` // admin | editor | viewer
	Disabled     bool   `json:"disabled"`
	AuthProvider string `json:"authProvider"` // local | oidc — drives "Change password" UI
	// Team — free-text grouping the admin picks (e.g. "platform-sre",
	// "fraud", "payments"). Backed by a LowCardinality column so
	// repeated values are cheap. Empty when unassigned; the
	// users-list UI groups "Unassigned" separately so an admin
	// can see who still needs labelling.
	Team string `json:"team"`
	// CustomRole — optional pointer into the auth.Service custom-role
	// catalog. Only meaningful when Role == viewer; otherwise ignored
	// (admin/editor get no further restriction). Empty = no custom
	// role, unrestricted viewer.
	CustomRole string `json:"customRole,omitempty"`
	CreatedAt  int64  `json:"createdAt"` // unix nanoseconds
	// Photo — LDAP thumbnailPhoto/jpegPhoto bytes (v0.8.238), refreshed
	// on each directory login; empty for local/OIDC accounts. Never
	// serialized into user JSON — served by the dedicated photo
	// endpoints. Get* loads the bytes (read-modify-write paths must
	// carry them through UpsertUser or the ReplacingMergeTree row
	// replace would wipe the photo); List* loads only HasPhoto.
	Photo    []byte `json:"-"`
	HasPhoto bool   `json:"hasPhoto"`
	// FullName / Org — directory identity (v0.8.266, operator: "ad
	// soyad + organizasyon da gelsin"). Refreshed on each LDAP login
	// (displayName → FullName, company/o → Org; department/ou lands
	// in Team above). Empty for local/OIDC accounts unless set.
	FullName string `json:"fullName,omitempty"`
	Org      string `json:"org,omitempty"`
	// LastLoginAt — son başarılı login (unix ns, 0 = hiç; v0.8.450).
	// Login yolları TouchUserLogin ile damgalar; whole-row replace
	// gereği her Upsert taşır.
	LastLoginAt int64 `json:"lastLoginAt"`
	// LdapUsername — directory sAMAccountName, lowercased (v0.8.526).
	// The join key between an LDAP group snapshot's members and this
	// user; email stays the canonical identity. Set by loginViaLDAP,
	// empty for local/OIDC accounts. Read-modify-write paths must carry
	// it forward (whole-row replace). Only persisted/read when the store
	// probed the column present (Store.hasLdapUsernameCol).
	LdapUsername string `json:"ldapUsername,omitempty"`
}

// userSelectExpr is the shared column list for the single-row user
// reads. ldap_username (v0.8.526) is appended only when the store probed
// the column present, so an install that pre-dates it (or a transient
// mid-deploy skew) still reads users cleanly.
func (s *Store) userSelectExpr() string {
	expr := `id, email, password_hash, role, disabled, auth_provider, team, custom_role,
	       toUnixTimestamp64Nano(created_at), photo, full_name, org,
	       toUnixTimestamp64Nano(last_login_at)`
	if s.hasLdapUsernameCol {
		expr += `, ldap_username`
	}
	return expr
}

// scanUserRow scans one user row produced by userSelectExpr. hasLdap must
// match the flag used to build the projection.
func scanUserRow(sc interface{ Scan(...any) error }, hasLdap bool) (*User, error) {
	var u User
	var disabled uint8
	var photo string
	dst := []any{&u.ID, &u.Email, &u.PasswordHash, &u.Role, &disabled, &u.AuthProvider,
		&u.Team, &u.CustomRole, &u.CreatedAt, &photo, &u.FullName, &u.Org, &u.LastLoginAt}
	if hasLdap {
		dst = append(dst, &u.LdapUsername)
	}
	if err := sc.Scan(dst...); err != nil {
		return nil, err
	}
	u.Disabled = disabled != 0
	u.Photo, u.HasPhoto = []byte(photo), photo != ""
	return &u, nil
}

// GetUserByEmail returns the latest version of a user (ReplacingMergeTree FINAL).
// Returns (nil, nil) when no row matches — callers treat that as "unknown user".
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT `+s.userSelectExpr()+`
		FROM users FINAL
		WHERE email = ? AND disabled = 0
		LIMIT 1`, email)
	u, err := scanUserRow(row, s.hasLdapUsernameCol)
	if err != nil {
		// clickhouse-go returns sql.ErrNoRows analogue; surface as nil/nil.
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return u, nil
}

func (s *Store) GetUserByID(ctx context.Context, id string) (*User, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT `+s.userSelectExpr()+`
		FROM users FINAL
		WHERE id = ? AND disabled = 0
		LIMIT 1`, id)
	u, err := scanUserRow(row, s.hasLdapUsernameCol)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return u, nil
}

func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	row := s.conn.QueryRow(ctx, `SELECT count() FROM users FINAL WHERE disabled = 0`)
	var n uint64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return int64(n), nil
}

// TouchUserLogin stamps last_login_at = now after a successful login
// (v0.8.450). Read-modify-write like UpdatePassword — ReplacingMergeTree
// replaces the whole row, so the fresh read carries photo/team/role
// through. Non-fatal for callers: a failed stamp must never block a
// valid login.
func (s *Store) TouchUserLogin(ctx context.Context, userID string) error {
	u, err := s.GetUserByID(ctx, userID)
	if err != nil || u == nil {
		return err
	}
	u.LastLoginAt = time.Now().UnixNano()
	return s.UpsertUser(ctx, *u)
}

func (s *Store) UpsertUser(ctx context.Context, u User) error {
	// ldap_username joins the column list only when the store probed it
	// present. This is the auth-critical guard: UpsertUser runs on EVERY
	// login via TouchUserLogin, so a naked column list that named an
	// absent column would break all logins (the v0.8.186 code-16 hazard
	// class, applied to the write path that matters most).
	cols := "id, email, password_hash, role, disabled, auth_provider, team, custom_role, photo, full_name, org, last_login_at"
	if s.hasLdapUsernameCol {
		cols += ", ldap_username"
	}
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO users ("+cols+")")
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
	args := []any{u.ID, u.Email, u.PasswordHash, u.Role, dis, provider, u.Team, custom,
		string(u.Photo), u.FullName, u.Org, time.Unix(0, u.LastLoginAt).UTC()}
	if s.hasLdapUsernameCol {
		args = append(args, u.LdapUsername)
	}
	if err := batch.Append(args...); err != nil {
		return fmt.Errorf("append user: %w", err)
	}
	return batch.Send()
}

// ListUsers returns every active user, newest first. Disabled users are
// hidden — they're effectively deleted from the UI's perspective.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, email, password_hash, role, disabled, auth_provider, team, custom_role,
		       toUnixTimestamp64Nano(created_at), length(photo) > 0 AS has_photo, full_name, org,
		       toUnixTimestamp64Nano(last_login_at)
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
		var disabled, hasPhoto uint8
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &disabled, &u.AuthProvider, &u.Team, &u.CustomRole, &u.CreatedAt, &hasPhoto, &u.FullName, &u.Org, &u.LastLoginAt); err != nil {
			return nil, err
		}
		u.Disabled = disabled != 0
		u.HasPhoto = hasPhoto != 0
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
		       toUnixTimestamp64Nano(created_at), length(photo) > 0 AS has_photo, full_name, org,
		       toUnixTimestamp64Nano(last_login_at)
		FROM users FINAL
		-- v0.8.487 — Türkçe-güvenli eşleşme: CH lower() ASCII-only'dir,
		-- 'Bankacılık' gibi İ/ı'lı takım adlarında katlama çalışmıyordu;
		-- baş/son boşluk da kırpılır (katalog etiketi ile LDAP değeri
		-- arasında görünmez fark eşleşmeyi öldürmesin).
		WHERE disabled = 0 AND lowerUTF8(trim(team)) = lowerUTF8(trim(?))
		ORDER BY email`, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var disabled, hasPhoto uint8
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &disabled, &u.AuthProvider, &u.Team, &u.CustomRole, &u.CreatedAt, &hasPhoto, &u.FullName, &u.Org, &u.LastLoginAt); err != nil {
			return nil, err
		}
		u.Disabled = disabled != 0
		u.HasPhoto = hasPhoto != 0
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
