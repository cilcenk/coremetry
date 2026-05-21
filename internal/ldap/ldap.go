// Package ldap is the enterprise auth provider — connects to a
// corporate LDAP/AD directory, authenticates users with their domain
// credentials, and resolves their group memberships into Coremetry
// roles via an admin-configurable mapping.
//
// Designed for enterprise-style on-prem deployments:
//   - LDAPS (port 636) is the default; StartTLS (389→TLS upgrade) is
//     supported for legacy AD setups.
//   - Custom CA paste field for internal CAs; SkipVerify toggle as
//     last-resort escape hatch for self-signed certs.
//   - Group→role mapping is the primary provisioning path. Pre-
//     provisioned users (admin pinned a row) override the group map.
//   - Bind password is stored in plaintext in system_settings — that
//     was an explicit deployment-time decision; an env-keyed encrypt
//     path can be bolted on later if needed.
package ldap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
)

// Config is the persisted LDAP connection + mapping definition.
//
// Defaults are filled in by Normalize() so the config struct can be
// built from a half-empty PUT body and still produce sensible probes.
type Config struct {
	Enabled bool `json:"enabled"`

	// Connection
	Host       string `json:"host"`
	Port       int    `json:"port"`
	UseTLS     bool   `json:"useTLS"`     // direct ldaps:// (default port 636)
	StartTLS   bool   `json:"startTLS"`   // upgrade plain → TLS on 389
	SkipVerify bool   `json:"skipVerify"`
	CACert     string `json:"caCert"` // PEM bundle for internal CA

	// Service account used to look up users / groups.
	BindDN       string `json:"bindDN"`
	BindPassword string `json:"bindPassword"`

	// Search
	BaseDN           string `json:"baseDN"`
	UserSearchFilter string `json:"userSearchFilter"` // {{username}} placeholder
	UserAttribute    string `json:"userAttribute"`    // sAMAccountName | uid | mail
	EmailAttribute   string `json:"emailAttribute"`
	DisplayAttribute string `json:"displayAttribute"`

	// Group lookup
	GroupSearchBase string `json:"groupSearchBase"`
	GroupFilter     string `json:"groupFilter"` // {{userDN}} placeholder
	// SkipMemberOfFetch drops `memberOf` from the user-search
	// attribute list. AD enforces MaxValRange (default 1500)
	// and MaxReceiveBuffer (1MB on some configs); a senior
	// user with thousands of nested group memberships trips
	// these and the login fails with LDAP_ADMIN_LIMIT_EXCEEDED
	// or a 1MB-cap error. Skipping memberOf moves the
	// authoritative membership lookup to the separate
	// GroupSearchBase + GroupFilter pass (LDAP_MATCHING_RULE_IN_CHAIN
	// recurses through nested groups cleanly), which pulls
	// only DN values without the per-user attribute bloat.
	// Required pre-req: GroupSearchBase must be set; otherwise
	// the auth fall-through has nothing to derive roles from.
	SkipMemberOfFetch bool `json:"skipMemberOfFetch"`

	// Role assignment
	DefaultRole  string             `json:"defaultRole"`  // role for users without group match
	GroupRoleMap []GroupRoleMapping `json:"groupRoleMap"` // first match wins (admin > editor > viewer)
}

type GroupRoleMapping struct {
	Group string `json:"group"` // group DN (preferred) or CN — case-insensitive substring match
	Role  string `json:"role"`  // admin | editor | viewer
}

// resolveUserFilter returns the filter actually used at search time.
// Two cases:
//   1. The configured filter contains {{username}} — substitute and use as-is.
//   2. It does not — wrap it as a Dex-style additional filter, AND-ing
//      a canonical OR clause that matches sAMAccountName / UPN / mail.
//      This is how operators familiar with Dex's `userSearch.filter:
//      "(objectclass=person)"` shorthand expect things to work; without
//      this wrap, the filter matches every person and the dedup check
//      fails with "user search returned N entries (filter too loose?)".
func resolveUserFilter(rawFilter, escapedUsername string) string {
	if strings.Contains(rawFilter, "{{username}}") {
		return strings.ReplaceAll(rawFilter, "{{username}}", escapedUsername)
	}
	usernameClause := fmt.Sprintf("(|(sAMAccountName=%s)(userPrincipalName=%s)(mail=%s))",
		escapedUsername, escapedUsername, escapedUsername)
	if rawFilter == "" {
		return usernameClause
	}
	// AND the operator's pre-filter with the username clause. Mirrors
	// Dex's behaviour: the saved filter narrows the candidate set
	// (e.g. (objectclass=person)), then we add the username predicate.
	return "(&" + rawFilter + usernameClause + ")"
}

// Normalize fills in Active-Directory-friendly defaults so half-filled
// configs produce a working probe. Mutates in place.
func (c *Config) Normalize() {
	if c.Port == 0 {
		if c.UseTLS {
			c.Port = 636
		} else {
			c.Port = 389
		}
	}
	if c.UserSearchFilter == "" {
		// Default that handles the three common login formats Corporate-
		// style AD users will type:
		//   - bare sAMAccountName  ("j.doe")
		//   - full UPN              ("j.doe@example.com")
		//   - mail address          ("j.doe@example.com")
		// objectclass=person prevents matching computer / service
		// principal accounts that may share a similar name.
		c.UserSearchFilter = "(&(objectclass=person)(|(sAMAccountName={{username}})(userPrincipalName={{username}})(mail={{username}})))"
	}
	if c.UserAttribute == "" {
		c.UserAttribute = "sAMAccountName"
	}
	if c.EmailAttribute == "" {
		// userPrincipalName lights up on every AD account and matches
		// the address the user typed at the login form. `mail` is
		// often unset on internal-only accounts, so UPN is the safer
		// default for inserting into our users table.
		c.EmailAttribute = "userPrincipalName"
	}
	if c.DisplayAttribute == "" {
		c.DisplayAttribute = "displayName"
	}
	if c.GroupFilter == "" {
		// LDAP_MATCHING_RULE_IN_CHAIN — walks nested group membership
		// recursively, so `Coremetry-Admins` can be a member of
		// `Coremetry-Roles` etc. Standard `(member={{userDN}})` only
		// catches direct membership and breaks for AD-style nesting.
		c.GroupFilter = "(member:1.2.840.113556.1.4.1941:={{userDN}})"
	}
	if c.DefaultRole == "" {
		c.DefaultRole = "viewer"
	}
}

// Sanitize returns a copy with the bind password cleared — used for
// API responses so the secret never round-trips back to the UI.
func (c *Config) Sanitize() Config {
	out := *c
	out.BindPassword = ""
	if c.BindPassword != "" {
		// One-bit indicator that a password is set, mapped client-side
		// to "leave empty to keep current". Empty would mean "no pwd".
		out.BindPassword = "__SET__"
	}
	out.GroupRoleMap = append([]GroupRoleMapping(nil), c.GroupRoleMap...)
	return out
}

// LDAPUser is the lightweight projection of a directory entry that
// the UI consumes (search results + provisioning picker).
type LDAPUser struct {
	DN          string   `json:"dn"`
	Username    string   `json:"username"`
	Email       string   `json:"email"`
	DisplayName string   `json:"displayName"`
	Groups      []string `json:"groups,omitempty"`
}

// AuthResult bundles the authenticated user + the role we resolved
// from their group memberships.
type AuthResult struct {
	User LDAPUser
	Role string
}

// ── Service ─────────────────────────────────────────────────────────────────

// Service holds the live config; mutates safely under RWMutex so the
// admin Settings PUT can swap config while a login is in flight.
type Service struct {
	mu  sync.RWMutex
	cfg Config
}

func New() *Service { return &Service{} }

func (s *Service) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Enabled && s.cfg.Host != ""
}

// Snapshot returns a sanitized copy (no plain bind password).
func (s *Service) Snapshot() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Sanitize()
}

// rawConfig returns a full copy including the bind password — for
// internal use only (connection establishment).
func (s *Service) rawConfig() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.cfg
	c.GroupRoleMap = append([]GroupRoleMapping(nil), s.cfg.GroupRoleMap...)
	return c
}

// Configure swaps the live config. If incoming.BindPassword == "" but
// a password is already saved, the old one is preserved (matches the
// "leave empty to keep current" UX).
func (s *Service) Configure(incoming Config) {
	incoming.Normalize()
	s.mu.Lock()
	defer s.mu.Unlock()
	if incoming.BindPassword == "" && s.cfg.BindPassword != "" {
		incoming.BindPassword = s.cfg.BindPassword
	}
	s.cfg = incoming
}

// ── Persistence ─────────────────────────────────────────────────────────────

const settingsKey = "ldap"

type SettingsStore interface {
	GetSetting(ctx context.Context, key string) ([]byte, error)
	PutSetting(ctx context.Context, key string, value []byte) error
}

func (s *Service) LoadPersisted(ctx context.Context, store SettingsStore) error {
	raw, err := store.GetSetting(ctx, settingsKey)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	c.Normalize()
	s.mu.Lock()
	s.cfg = c
	s.mu.Unlock()
	return nil
}

// StartConfigRefresh — v0.5.324. Background goroutine that
// re-reads the persisted LDAP config from the shared store
// every `interval`. Closes the multi-pod gap where one pod
// wrote new settings but other pods kept serving stale
// in-memory cfg until restart. interval ≤ 0 → 30s.
func (s *Service) StartConfigRefresh(ctx context.Context, store SettingsStore, interval time.Duration) {
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
			if err := s.LoadPersisted(ctx, store); err != nil {
				log.Printf("[ldap] config refresh: %v", err)
			}
		}
	}
}

func (s *Service) SavePersisted(ctx context.Context, store SettingsStore, c Config) error {
	c.Normalize()
	// Preserve existing bind password when caller submits empty.
	s.mu.Lock()
	if c.BindPassword == "" && s.cfg.BindPassword != "" {
		c.BindPassword = s.cfg.BindPassword
	}
	s.cfg = c
	s.mu.Unlock()
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := store.PutSetting(ctx, settingsKey, raw); err != nil {
		return err
	}
	log.Printf("[ldap] config saved enabled=%v host=%s:%d tls=%v startTLS=%v baseDN=%q userFilter=%q groupFilter=%q mappings=%d defaultRole=%s",
		c.Enabled, c.Host, c.Port, c.UseTLS, c.StartTLS, c.BaseDN,
		c.UserSearchFilter, c.GroupFilter, len(c.GroupRoleMap), c.DefaultRole)
	return nil
}

// ── Connection ──────────────────────────────────────────────────────────────

// dial opens a connection using the saved config (or a transient one
// for TestConnection). Caller is responsible for Close().
func dial(c Config) (*goldap.Conn, error) {
	c.Normalize()
	if c.Host == "" {
		return nil, errors.New("ldap host not configured")
	}
	addr := fmt.Sprintf("%s:%d", c.Host, c.Port)
	tlsCfg := &tls.Config{ServerName: c.Host, InsecureSkipVerify: c.SkipVerify}
	if c.CACert != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(c.CACert)) {
			return nil, errors.New("ldap: failed to parse CA cert (expecting PEM)")
		}
		tlsCfg.RootCAs = pool
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var (
		conn *goldap.Conn
		err  error
	)
	switch {
	case c.UseTLS:
		conn, err = goldap.DialURL("ldaps://"+addr,
			goldap.DialWithTLSConfig(tlsCfg),
			goldap.DialWithDialer(dialer))
	default:
		conn, err = goldap.DialURL("ldap://"+addr,
			goldap.DialWithDialer(dialer))
	}
	if err != nil {
		return nil, fmt.Errorf("ldap dial %s: %w", addr, err)
	}
	conn.SetTimeout(10 * time.Second)
	if c.StartTLS && !c.UseTLS {
		if err := conn.StartTLS(tlsCfg); err != nil {
			conn.Close()
			return nil, fmt.Errorf("ldap StartTLS: %w", err)
		}
	}
	return conn, nil
}

// bindAdmin opens a connection and binds with the configured service
// account. Returned conn must be closed by the caller.
func bindAdmin(c Config) (*goldap.Conn, error) {
	conn, err := dial(c)
	if err != nil {
		return nil, err
	}
	if c.BindDN != "" {
		if err := conn.Bind(c.BindDN, c.BindPassword); err != nil {
			conn.Close()
			return nil, fmt.Errorf("ldap bind %q: %w", c.BindDN, err)
		}
	}
	return conn, nil
}

// ── Operations ──────────────────────────────────────────────────────────────

// TestConnection establishes + service-binds + closes. Returns nil on
// success so the UI's "Test connection" button can flip green.
func (s *Service) TestConnection(ctx context.Context, override *Config) error {
	cfg := s.rawConfig()
	if override != nil {
		// Caller is testing a draft config the admin hasn't saved yet.
		// Inherit the saved password when override leaves it empty so
		// the test can be re-run against the existing creds.
		c := *override
		if c.BindPassword == "" {
			c.BindPassword = cfg.BindPassword
		}
		cfg = c
	}
	log.Printf("[ldap] test-connection host=%s:%d tls=%v startTLS=%v bindDN=%q",
		cfg.Host, cfg.Port, cfg.UseTLS, cfg.StartTLS, cfg.BindDN)
	conn, err := bindAdmin(cfg)
	if err != nil {
		log.Printf("[ldap] test-connection FAILED: %v", err)
		return err
	}
	defer conn.Close()
	log.Printf("[ldap] test-connection OK")
	return nil
}

// findUser looks up a directory entry by username (resolving the
// configured filter template). Returns (nil, nil) for "no such user".
//
// Search limits: sizeLimit 50 / timeLimit 30s. The previous 2/5
// values were tuned for tiny test directories — enterprise-scale AD with
// sub-tree search at the root naming context routinely tripped both.
// 50 is still small enough to catch a "filter too loose" misconfig
// (10+ matches means the operator should narrow their filter), and
// AD's default page size is 1000, so 50 stays well under any forest-
// wide policy.
func findUser(conn *goldap.Conn, c Config, username string) (*LDAPUser, error) {
	filter := resolveUserFilter(c.UserSearchFilter, goldap.EscapeFilter(username))
	if !strings.Contains(c.UserSearchFilter, "{{username}}") {
		log.Printf("[ldap] WARN: userSearchFilter has no {{username}} placeholder — auto-wrapping with sAMAccountName/UPN/mail OR clause. Set the filter to e.g. (sAMAccountName={{username}}) to silence this.")
	}
	// Attribute list — request the well-known identity fields
	// plus memberOf for downstream role mapping. Operators who
	// have hit AD's MaxValRange / MaxReceiveBuffer (1MB) cap on
	// users with very large memberOf collections can set
	// SkipMemberOfFetch in the LDAP settings to drop memberOf
	// here and rely purely on the separate group-search pass.
	attrs := []string{"dn", c.UserAttribute, c.EmailAttribute, c.DisplayAttribute}
	if !c.SkipMemberOfFetch {
		attrs = append(attrs, "memberOf")
	}
	log.Printf("[ldap] user search baseDN=%q filter=%s attrs=%v", c.BaseDN, filter, attrs)
	req := goldap.NewSearchRequest(
		c.BaseDN, goldap.ScopeWholeSubtree, goldap.NeverDerefAliases,
		50, 30, false,
		filter,
		attrs,
		nil,
	)
	res, err := conn.Search(req)
	// Soft-handle SizeLimitExceeded — AD sometimes returns this even
	// when the result fits, alongside a partial response. As long as
	// we got entries we can keep going; the dedup check below catches
	// the "filter actually matches too much" case.
	if err != nil {
		if goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded) && res != nil && len(res.Entries) > 0 {
			log.Printf("[ldap] user search hit size-limit but returned %d partial entries — continuing", len(res.Entries))
		} else {
			log.Printf("[ldap] user search FAILED: %v", err)
			return nil, fmt.Errorf("user search: %w", err)
		}
	}
	if res == nil || len(res.Entries) == 0 {
		log.Printf("[ldap] user search returned 0 entries — entered username %q does not match filter or baseDN scope", username)
		return nil, nil
	}
	if len(res.Entries) > 1 {
		dns := make([]string, 0, len(res.Entries))
		for _, e := range res.Entries {
			dns = append(dns, e.DN)
		}
		log.Printf("[ldap] user search returned %d entries (filter too loose?): %v", len(res.Entries), dns)
		return nil, fmt.Errorf("user search returned %d entries (filter too loose?)", len(res.Entries))
	}
	e := res.Entries[0]
	groups := e.GetAttributeValues("memberOf")
	log.Printf("[ldap] user found dn=%q (%s=%q, mail=%q, memberOf=%d direct groups)",
		e.DN, c.UserAttribute, e.GetAttributeValue(c.UserAttribute),
		e.GetAttributeValue(c.EmailAttribute), len(groups))
	return &LDAPUser{
		DN:          e.DN,
		Username:    firstNonEmpty(e.GetAttributeValue(c.UserAttribute), username),
		Email:       e.GetAttributeValue(c.EmailAttribute),
		DisplayName: e.GetAttributeValue(c.DisplayAttribute),
		Groups:      groups,
	}, nil
}

// Search looks up users matching `query` (substring on username,
// email or displayName). Used by the admin "pick a user to provision"
// flow. Returns at most `limit` entries.
func (s *Service) Search(ctx context.Context, query string, limit int) ([]LDAPUser, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	c := s.rawConfig()
	conn, err := bindAdmin(c)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	q := goldap.EscapeFilter(query)
	filter := fmt.Sprintf("(|(%s=*%s*)(%s=*%s*)(%s=*%s*))",
		c.UserAttribute, q, c.EmailAttribute, q, c.DisplayAttribute, q)
	if query == "" {
		filter = fmt.Sprintf("(%s=*)", c.UserAttribute)
	}
	req := goldap.NewSearchRequest(
		c.BaseDN, goldap.ScopeWholeSubtree, goldap.NeverDerefAliases,
		limit, 30, false,
		filter,
		[]string{"dn", c.UserAttribute, c.EmailAttribute, c.DisplayAttribute},
		nil,
	)
	res, err := conn.Search(req)
	// Soft-handle SizeLimitExceeded — caller asked for `limit` rows
	// and AD returned partial results before timing out; that's still
	// the answer.
	if err != nil && !(goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded) && res != nil) {
		return nil, fmt.Errorf("search: %w", err)
	}
	out := make([]LDAPUser, 0, len(res.Entries))
	for _, e := range res.Entries {
		out = append(out, LDAPUser{
			DN:          e.DN,
			Username:    e.GetAttributeValue(c.UserAttribute),
			Email:       e.GetAttributeValue(c.EmailAttribute),
			DisplayName: e.GetAttributeValue(c.DisplayAttribute),
		})
	}
	return out, nil
}

// Authenticate runs the standard "search-then-bind" auth pattern:
//   1. Service-bind (admin lookup credentials).
//   2. Find the user by username (or email — `username` may be either,
//      the configured UserSearchFilter handles it).
//   3. Re-bind with the user's DN + entered password — that's the
//      actual credential check.
//   4. Resolve groups → role via the configured GroupRoleMap; fall
//      back to DefaultRole.
func (s *Service) Authenticate(ctx context.Context, username, password string) (*AuthResult, error) {
	if password == "" {
		return nil, errors.New("password required")
	}
	c := s.rawConfig()
	if !c.Enabled {
		return nil, errors.New("ldap disabled")
	}
	log.Printf("[ldap] authenticate attempt username=%q", username)
	conn, err := bindAdmin(c)
	if err != nil {
		log.Printf("[ldap] service-bind FAILED: %v", err)
		return nil, err
	}
	defer conn.Close()

	user, err := findUser(conn, c, username)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, errors.New("user not found in directory")
	}
	if err := conn.Bind(user.DN, password); err != nil {
		log.Printf("[ldap] user-bind FAILED for dn=%q: %v", user.DN, err)
		return nil, errors.New("invalid credentials")
	}
	log.Printf("[ldap] user-bind OK dn=%q", user.DN)

	// Group lookup — separate search if the user entry didn't ship
	// memberOf (some directories don't populate it). Also folds the
	// memberOf list we already have so we get the full picture.
	groups := append([]string(nil), user.Groups...)
	if c.GroupSearchBase != "" {
		grpFilter := strings.ReplaceAll(c.GroupFilter, "{{userDN}}", goldap.EscapeFilter(user.DN))
		log.Printf("[ldap] group search baseDN=%q filter=%s", c.GroupSearchBase, grpFilter)
		grpReq := goldap.NewSearchRequest(
			c.GroupSearchBase, goldap.ScopeWholeSubtree, goldap.NeverDerefAliases,
			0, 30, false,
			grpFilter,
			[]string{"dn", "cn"},
			nil,
		)
		grpRes, err := conn.Search(grpReq)
		// Same SizeLimitExceeded tolerance as user search — partial
		// results still tell us something about the user's groups.
		if err != nil && !goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded) {
			log.Printf("[ldap] group search FAILED: %v (continuing with memberOf only)", err)
		} else if grpRes != nil {
			if err != nil {
				log.Printf("[ldap] group search hit size-limit but returned %d partial entries — continuing", len(grpRes.Entries))
			}
			for _, e := range grpRes.Entries {
				groups = append(groups, e.DN)
			}
			log.Printf("[ldap] group search returned %d entries", len(grpRes.Entries))
		}
	}
	user.Groups = groups
	role := mapRole(groups, c.GroupRoleMap, c.DefaultRole)
	log.Printf("[ldap] resolved role=%q (matched %d groups against %d mappings, fallback=%q)",
		role, len(groups), len(c.GroupRoleMap), c.DefaultRole)
	if len(groups) > 0 && len(groups) <= 10 {
		// Only dump full group list when it's small — large AD users
		// can have 50+ memberships and we don't want to flood logs.
		log.Printf("[ldap] user groups: %v", groups)
	}
	return &AuthResult{User: *user, Role: role}, nil
}

// mapRole picks the highest-privilege role any of the user's groups
// matches. Match is case-insensitive substring — works for both DN
// ("CN=Coremetry-Admins,OU=Groups,DC=corp,DC=example") and bare CN
// ("Coremetry-Admins") values in the mapping config.
func mapRole(userGroups []string, mappings []GroupRoleMapping, fallback string) string {
	rank := func(role string) int {
		switch role {
		case "admin":
			return 3
		case "editor":
			return 2
		case "viewer":
			return 1
		}
		return 0
	}
	best := ""
	for _, m := range mappings {
		needle := strings.ToLower(strings.TrimSpace(m.Group))
		if needle == "" {
			continue
		}
		for _, g := range userGroups {
			if strings.Contains(strings.ToLower(g), needle) {
				if rank(m.Role) > rank(best) {
					best = m.Role
				}
				break
			}
		}
	}
	if best == "" {
		return fallback
	}
	return best
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
