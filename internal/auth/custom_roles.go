package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// CustomRole is an admin-defined subset of viewer's page access.
// Each role names a set of sidebar paths the user is allowed to see;
// the frontend filters the sidebar + redirects direct-URL access to
// the first visible page. Custom roles only apply when the user's
// base role is viewer — admin/editor get no further restriction.
type CustomRole struct {
	Name  string   `json:"name"`
	Pages []string `json:"pages"`
}

// customRoleStore is the small interface the auth.Service needs to
// persist the role catalog. Defined locally to avoid pulling chstore
// in as a hard dependency.
type customRoleStore interface {
	GetCustomRolesRaw(ctx context.Context) ([]byte, error)
	PutCustomRolesRaw(ctx context.Context, raw []byte) error
}

// LoadPersistedCustomRoles hydrates the in-memory custom-role catalog
// from system_settings. Missing blob = empty catalog. Called once at
// boot from main(); safe to call again on demand.
func (s *Service) LoadPersistedCustomRoles(ctx context.Context, store customRoleStore) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := store.GetCustomRolesRaw(ctx)
	if err != nil {
		return err
	}
	var roles []CustomRole
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &roles); err != nil {
			return fmt.Errorf("custom roles decode: %w", err)
		}
	}
	s.mu.Lock()
	s.customRoles = indexRoles(roles)
	s.mu.Unlock()
	return nil
}

// StartCustomRoleRefresh — v0.5.318. Runs a background goroutine
// that re-reads the custom-role catalog from the shared chstore
// every `interval` (default 30s when ≤0). In a multi-pod
// cluster the previous load-once-at-boot pattern meant a role
// created on pod A wasn't visible to a session served by pod B
// until B's process restarted. Polling closes that gap to a
// bounded staleness window without requiring pub/sub
// infrastructure.
//
// Returns when ctx is cancelled. Errors are logged but never
// fatal — a transient CH blip leaves the previous catalog in
// place rather than clearing it.
func (s *Service) StartCustomRoleRefresh(ctx context.Context, store customRoleStore, interval time.Duration) {
	if s == nil || store == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.LoadPersistedCustomRoles(ctx, store); err != nil {
				log.Printf("[auth] custom-role refresh: %v", err)
			}
		}
	}
}

// CustomRoles returns a snapshot copy of the current role catalog
// ordered by name. The caller can freely mutate the returned slice.
func (s *Service) CustomRoles() []CustomRole {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]CustomRole, 0, len(s.customRoles))
	for _, r := range s.customRoles {
		out = append(out, copyRole(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// CustomRolePages returns the page list for a named role, or nil if
// the role isn't found. nil is the signal for "this user has no page
// restriction" — callers MUST distinguish nil (unrestricted) from
// empty slice (restricted to zero pages, effectively no access).
func (s *Service) CustomRolePages(name string) []string {
	if name == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.customRoles[name]
	if !ok {
		return nil
	}
	pages := make([]string, len(r.Pages))
	copy(pages, r.Pages)
	return pages
}

// UpsertCustomRole writes a single role (create or replace by name).
// Persists the updated catalog atomically — partial writes don't
// occur because the whole catalog is one system_settings blob.
func (s *Service) UpsertCustomRole(ctx context.Context, store customRoleStore, role CustomRole) error {
	if store == nil {
		return fmt.Errorf("custom role store not configured")
	}
	role.Name = strings.TrimSpace(role.Name)
	if role.Name == "" {
		return fmt.Errorf("role name required")
	}
	// Reserve built-in role names so the operator can't shadow
	// admin/editor/viewer with a custom role of the same name.
	if role.Name == RoleAdmin || role.Name == RoleEditor || role.Name == RoleViewer {
		return fmt.Errorf("role name %q is reserved", role.Name)
	}
	role.Pages = normalisePages(role.Pages)

	s.mu.Lock()
	if s.customRoles == nil {
		s.customRoles = make(map[string]CustomRole)
	}
	s.customRoles[role.Name] = role
	snapshot := snapshotRoles(s.customRoles)
	s.mu.Unlock()

	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return store.PutCustomRolesRaw(ctx, raw)
}

// DeleteCustomRole removes a role by name. Persists the updated
// catalog. Caller is responsible for clearing the custom_role field
// on any user assigned to it (the API handler does this — see
// deleteCustomRole in internal/api/api.go).
func (s *Service) DeleteCustomRole(ctx context.Context, store customRoleStore, name string) error {
	if store == nil {
		return fmt.Errorf("custom role store not configured")
	}
	s.mu.Lock()
	delete(s.customRoles, name)
	snapshot := snapshotRoles(s.customRoles)
	s.mu.Unlock()

	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return store.PutCustomRolesRaw(ctx, raw)
}

// indexRoles converts a slice to a name→role map.
func indexRoles(roles []CustomRole) map[string]CustomRole {
	out := make(map[string]CustomRole, len(roles))
	for _, r := range roles {
		r.Pages = normalisePages(r.Pages)
		out[r.Name] = r
	}
	return out
}

// snapshotRoles converts the map back to a sorted slice for
// deterministic persistence (operator diffs stay readable).
func snapshotRoles(m map[string]CustomRole) []CustomRole {
	out := make([]CustomRole, 0, len(m))
	for _, r := range m {
		out = append(out, copyRole(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func copyRole(r CustomRole) CustomRole {
	pages := make([]string, len(r.Pages))
	copy(pages, r.Pages)
	return CustomRole{Name: r.Name, Pages: pages}
}

// normalisePages sorts + dedupes the page list so two equivalent
// roles persist identically (avoids audit-log churn on no-op saves).
func normalisePages(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
