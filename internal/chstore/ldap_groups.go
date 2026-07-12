package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ldap_groups — persisted LDAP/AD group-membership snapshot (v0.8.526).
//
// Written by the leader-gated group-sync worker (internal/ldap sync
// engine) once per syncInterval; read at boot + every 30s by every pod
// to re-hydrate the in-memory authz snapshot. Kept as a small state
// table (thousands of groups, not billions of rows), so it follows the
// ReplacingMergeTree(version) + FINAL discipline of `users` /
// `saved_views` — NOT the highVolumeTables `_local` split (adaptDDL
// gives it ON CLUSTER + ReplicatedReplacingMergeTree only, same as every
// other state table).
//
// Row identity is objectGUID (see internal/ldap: a group rename/move
// changes the DN but not the GUID, so the RMT key stays stable and a
// moved group never double-writes). `deleted` is a tombstone: each sync
// writes the FULL in-scope set with deleted=0 and marks any UID that
// disappeared from the directory deleted=1 (the saved_views tombstone
// precedent). Hydrate reads FINAL WHERE deleted=0 so tombstoned groups
// vanish from the snapshot.
const ldapGroupsDDL = `CREATE TABLE IF NOT EXISTS ldap_groups (
	group_uid  String,                                          -- objectGUID (uuid string)
	dn         String,
	cn         LowCardinality(String),
	users      Array(String),                                   -- sAMAccountName, sorted
	deleted    UInt8         DEFAULT 0,                          -- 1 = tombstone (gone from directory)
	synced_at  DateTime64(9) DEFAULT now64(9),
	version    UInt64        DEFAULT toUnixTimestamp64Nano(now64(9))
) ENGINE = ReplacingMergeTree(version)
ORDER BY group_uid`

// LdapGroupRow is the storage projection of one directory group. The
// ldap sync engine maps its own ldap.Group ↔ LdapGroupRow, keeping this
// package free of any ldap import (feature → storage layering).
type LdapGroupRow struct {
	UID      string   // objectGUID as a uuid string
	DN       string   // distinguished name (display / prefix filter)
	CN       string   // common name (display)
	Users    []string // sAMAccountName members, sorted
	Deleted  bool     // tombstone
	SyncedAt int64    // unix ns of the sync that produced this row
}

// UpsertLdapGroups writes the FULL in-scope group set (deleted=0) and
// tombstones (deleted=1) every UID that was live before this sync but is
// absent now. One monotonic version stamps the whole batch so a later
// sync always wins the ReplacingMergeTree merge. Returns (written,
// tombstoned) counts for the sync stats.
func (s *Store) UpsertLdapGroups(ctx context.Context, rows []LdapGroupRow, syncedAt time.Time) (written, tombstoned int, err error) {
	prev, err := s.liveLdapGroupUIDs(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("read live ldap group uids: %w", err)
	}
	next := make([]string, 0, len(rows))
	for _, r := range rows {
		next = append(next, r.UID)
	}
	gone := missingUIDs(prev, next)

	// One version for the whole batch — later syncs monotonically win.
	version := uint64(time.Now().UnixNano())
	sAt := syncedAt.UTC()

	batch, err := s.conn.PrepareBatch(ctx,
		`INSERT INTO ldap_groups (group_uid, dn, cn, users, deleted, synced_at, version)`)
	if err != nil {
		return 0, 0, fmt.Errorf("prepare ldap_groups: %w", err)
	}
	for _, r := range rows {
		members := r.Users
		if members == nil {
			members = []string{} // Array columns reject nil
		}
		if err := batch.Append(r.UID, r.DN, r.CN, members, uint8(0), sAt, version); err != nil {
			return 0, 0, fmt.Errorf("append ldap group: %w", err)
		}
	}
	for _, uid := range gone {
		if err := batch.Append(uid, "", "", []string{}, uint8(1), sAt, version); err != nil {
			return 0, 0, fmt.Errorf("append ldap group tombstone: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return 0, 0, fmt.Errorf("send ldap_groups: %w", err)
	}
	return len(rows), len(gone), nil
}

// liveLdapGroupUIDs returns the group UIDs currently NOT tombstoned.
func (s *Store) liveLdapGroupUIDs(ctx context.Context) ([]string, error) {
	rows, err := s.conn.Query(ctx, `SELECT group_uid FROM ldap_groups FINAL WHERE deleted = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out = append(out, uid)
	}
	return out, rows.Err()
}

// HydrateLdapGroups loads the live (non-tombstoned) group set for the
// boot / periodic snapshot rebuild. Ordered by cn for deterministic UI.
func (s *Store) HydrateLdapGroups(ctx context.Context) ([]LdapGroupRow, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT group_uid, dn, cn, users, toUnixTimestamp64Nano(synced_at)
		FROM ldap_groups FINAL
		WHERE deleted = 0
		ORDER BY cn`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LdapGroupRow
	for rows.Next() {
		var r LdapGroupRow
		if err := rows.Scan(&r.UID, &r.DN, &r.CN, &r.Users, &r.SyncedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LdapIdentityOverlap answers the §10 early-warning question: of the
// distinct alias keys produced by the sync, how many resolve to a real
// user in the `users` table (lowercase email OR ldap_username)? A ratio
// of 0 means "sync succeeded but nothing matched" — the classic
// sAMAccountName↔email mismatch. `aliases` are already normalized
// (lowercased) by the caller. Reads the bounded `users` state table
// (< 10k rows) fully rather than shipping a giant IN list.
func (s *Store) LdapIdentityOverlap(ctx context.Context, aliases []string) (matched, total int, err error) {
	aset := make(map[string]struct{}, len(aliases))
	for _, a := range aliases {
		a = strings.TrimSpace(a)
		if a != "" {
			aset[a] = struct{}{}
		}
	}
	total = len(aset)
	if total == 0 {
		return 0, 0, nil
	}

	ident := make(map[string]struct{}, 1024)
	emailRows, err := s.conn.Query(ctx,
		`SELECT lowerUTF8(email) FROM users FINAL WHERE disabled = 0 AND email != ''`)
	if err != nil {
		return 0, total, err
	}
	if err := collectIdentities(emailRows, ident); err != nil {
		return 0, total, err
	}
	if s.hasLdapUsernameCol {
		unRows, err := s.conn.Query(ctx,
			`SELECT lowerUTF8(ldap_username) FROM users FINAL WHERE disabled = 0 AND ldap_username != ''`)
		if err != nil {
			return 0, total, err
		}
		if err := collectIdentities(unRows, ident); err != nil {
			return 0, total, err
		}
	}

	for a := range aset {
		if _, ok := ident[a]; ok {
			matched++
		}
	}
	return matched, total, nil
}

// collectIdentities scans a single-string-column result into the set and
// closes the rows. Split out so the two source queries share one path.
func collectIdentities(rows interface {
	Next() bool
	Scan(...any) error
	Close() error
	Err() error
}, into map[string]struct{}) error {
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return err
		}
		if v = strings.TrimSpace(v); v != "" {
			into[v] = struct{}{}
		}
	}
	return rows.Err()
}

// missingUIDs returns every UID present in prev but absent from next —
// the set to tombstone on this sync. Pure + order-preserving over prev so
// the tombstone batch is deterministic. (v0.8.526 §12.7 regression test:
// ldap_groups_test.go.)
func missingUIDs(prev, next []string) []string {
	live := make(map[string]struct{}, len(next))
	for _, u := range next {
		live[u] = struct{}{}
	}
	var gone []string
	seen := make(map[string]struct{}, len(prev))
	for _, u := range prev {
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		if _, ok := live[u]; !ok {
			gone = append(gone, u)
		}
	}
	return gone
}
