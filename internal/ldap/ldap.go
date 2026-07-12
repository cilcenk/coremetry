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
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
	"unicode/utf8"
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
	UseTLS     bool   `json:"useTLS"`   // direct ldaps:// (default port 636)
	StartTLS   bool   `json:"startTLS"` // upgrade plain → TLS on 389
	SkipVerify bool   `json:"skipVerify"`
	CACert     string `json:"caCert"` // PEM bundle for internal CA
	// CAFile (v0.8.526) — filesystem path to a PEM CA bundle. When set
	// AND readable it WINS over the inline CACert blob; empty falls back
	// to CACert (backward compat). A path, not a secret — safe to echo.
	CAFile string `json:"caFile,omitempty"`

	// Service account used to look up users / groups.
	BindDN       string `json:"bindDN"`
	BindPassword string `json:"bindPassword"`
	// BindPasswordFile / BindPasswordEnv (v0.8.526) — external references
	// for the bind secret so it need not live in the system_settings blob.
	// Precedence: File (if set & readable) > Env (if set & present) >
	// inline BindPassword blob (backward compat — never removed, the blob
	// path is a deliberate deployment option). Both hold a PATH / env-var
	// NAME, not the secret itself, so they round-trip through the API
	// unredacted.
	BindPasswordFile string `json:"bindPasswordFile,omitempty"`
	BindPasswordEnv  string `json:"bindPasswordEnv,omitempty"`

	// Search
	BaseDN           string `json:"baseDN"`
	UserSearchFilter string `json:"userSearchFilter"` // {{username}} placeholder
	UserAttribute    string `json:"userAttribute"`    // sAMAccountName | uid | mail
	EmailAttribute   string `json:"emailAttribute"`
	DisplayAttribute string `json:"displayAttribute"`
	// TeamAttribute (v0.8.430) — which directory attribute feeds
	// users.team on login. Operator-reported: the default
	// department→ou fallback surfaced the TOP division ("TEKNOLOJİ")
	// for every user because AD stores the division there; the actual
	// sub-team lives elsewhere (use /api/settings/ldap/inspect to find
	// where). "" = legacy department→ou; the special value "dn-ou"
	// takes the DEEPEST ou= RDN from the user's DN (the leaf-most OU
	// container — typically the sub-team in OU-per-team trees); any
	// other value is read as a literal attribute name with the legacy
	// chain as fallback.
	TeamAttribute string `json:"teamAttribute"`
	// TeamRegex (v0.8.434) — optional extraction on the RESOLVED team
	// source value, for directories that embed the sub-team inside a
	// composite attribute (operator's AD: displayName carries
	// "Ad Soyad (Bölüm) * ÜNVAN-Ekip" — TeamAttribute=displayName +
	// TeamRegex `-([^-]+)$` yields "Ekip"). First capture group wins
	// (whole match when the pattern has no group). NO match → team
	// stays EMPTY on purpose: the raw composite leaking into
	// users.team was the reported bug. Invalid pattern → ignored
	// (raw value passes through) and logged once at Configure.
	TeamRegex string `json:"teamRegex"`

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

	// GroupSync (v0.8.526) — periodic directory group→members snapshot.
	// Independent of the login-time group lookup: this enumerates the
	// in-scope groups and, per group, chain-searches their effective
	// members, persisting the result to ClickHouse for O(1) authz.
	GroupSync GroupSyncConfig `json:"groupSync"`
}

type GroupRoleMapping struct {
	Group string `json:"group"` // group DN (preferred) or CN — case-insensitive substring match
	Role  string `json:"role"`  // admin | editor | viewer
}

// GroupSyncConfig is the persisted definition for the background LDAP/AD
// group-membership sync (audit §4). All directory queries reuse the
// parent Config's connection (dial + bind + TLS) — no second endpoint.
type GroupSyncConfig struct {
	Enabled bool `json:"enabled"`
	// SyncInterval / Timeout are duration strings ("30m", "60s") so the
	// system_settings JSON blob stays human-editable; parsed via
	// time.ParseDuration with the defaults below.
	SyncInterval string `json:"syncInterval"` // default 30m
	Timeout      string `json:"timeout"`      // default 60s per full sync
	// PageSize for SearchWithPaging — kept under AD's MaxPageSize (1000).
	PageSize uint32 `json:"pageSize"` // default 500
	// UsersBaseDN / UserFilter — the pre-filter narrowing the per-group
	// chain USER search (resolveUserFilter-style: no {{username}}). The
	// chain predicate (memberOf:…IN_CHAIN:=<groupDN>) is AND-ed on.
	UsersBaseDN string `json:"usersBaseDN"`
	UserFilter  string `json:"userFilter"` // default (objectClass=user)
	// UserNameAttribute — the member identity written to ldap_groups.users.
	UserNameAttribute string `json:"userNameAttribute"` // default sAMAccountName
	// GroupsBaseDN / GroupFilter — where + what to enumerate for the group
	// list. Empty GroupsBaseDN falls back to UsersBaseDN (audit §AÇIK-3:
	// (objectClass=group) paged enumerate under the include scope).
	GroupsBaseDN string `json:"groupsBaseDN"`
	GroupFilter  string `json:"groupFilter"` // default (objectClass=group)
	// IncludePrefixes / ExcludePrefixes — DN suffix-match whitelist /
	// blacklist over enumerated groups. "prefix" is the OU-chain DN suffix
	// (DNs read leaf→root as "CN=x,OU=y,DC=…"), so scope membership is a
	// case-insensitive strings.HasSuffix(dn, prefix). Empty include = all.
	IncludePrefixes []string `json:"includePrefixes"`
	ExcludePrefixes []string `json:"excludePrefixes"`
	// MaxGroupMembers — dev-group safety cap (audit §AÇIK-4). A group with
	// more effective members is TRUNCATED (sorted, first N kept) + logged
	// WARN + counted in Stats.Truncated. 0 → default 50000.
	MaxGroupMembers int `json:"maxGroupMembers"`
}

// resolveUserFilter returns the filter actually used at search time.
// Two cases:
//  1. The configured filter contains {{username}} — substitute and use as-is.
//  2. It does not — wrap it as a Dex-style additional filter, AND-ing
//     a canonical OR clause that matches sAMAccountName / UPN / mail.
//     This is how operators familiar with Dex's `userSearch.filter:
//     "(objectclass=person)"` shorthand expect things to work; without
//     this wrap, the filter matches every person and the dedup check
//     fails with "user search returned N entries (filter too loose?)".
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
	c.GroupSync.normalize()
}

// Group-sync defaults (v0.8.526).
const (
	defaultGroupSyncInterval     = 30 * time.Minute
	defaultGroupSyncTimeout      = 60 * time.Second
	defaultGroupSyncPageSize     = uint32(500)
	defaultGroupSyncUserFilter   = "(objectClass=user)"
	defaultGroupSyncGroupFilter  = "(objectClass=group)"
	defaultGroupSyncUserNameAttr = "sAMAccountName"
	defaultMaxGroupMembers       = 50000
)

// normalize fills group-sync defaults in place. Mirrors Config.Normalize
// so a half-filled blob still produces a working sync definition.
func (g *GroupSyncConfig) normalize() {
	if g.PageSize == 0 {
		g.PageSize = defaultGroupSyncPageSize
	}
	if strings.TrimSpace(g.UserFilter) == "" {
		g.UserFilter = defaultGroupSyncUserFilter
	}
	if strings.TrimSpace(g.GroupFilter) == "" {
		g.GroupFilter = defaultGroupSyncGroupFilter
	}
	if strings.TrimSpace(g.UserNameAttribute) == "" {
		g.UserNameAttribute = defaultGroupSyncUserNameAttr
	}
	if g.MaxGroupMembers <= 0 {
		g.MaxGroupMembers = defaultMaxGroupMembers
	}
}

// IntervalDuration parses SyncInterval, falling back to 30m on empty /
// invalid input.
func (g GroupSyncConfig) IntervalDuration() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(g.SyncInterval)); err == nil && d > 0 {
		return d
	}
	return defaultGroupSyncInterval
}

// TimeoutDuration parses Timeout, falling back to 60s on empty / invalid.
func (g GroupSyncConfig) TimeoutDuration() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(g.Timeout)); err == nil && d > 0 {
		return d
	}
	return defaultGroupSyncTimeout
}

// groupsSearchBase is the base DN for the (objectClass=group) enumerate:
// explicit GroupsBaseDN wins, else UsersBaseDN, else the parent BaseDN.
func (c Config) groupsSearchBase() string {
	if b := strings.TrimSpace(c.GroupSync.GroupsBaseDN); b != "" {
		return b
	}
	if b := strings.TrimSpace(c.GroupSync.UsersBaseDN); b != "" {
		return b
	}
	return c.BaseDN
}

// usersSearchBase is the base DN for the per-group chain USER search.
func (c Config) usersSearchBase() string {
	if b := strings.TrimSpace(c.GroupSync.UsersBaseDN); b != "" {
		return b
	}
	return c.BaseDN
}

// applyExternalSecrets resolves the file/env references (v0.8.526) into
// the effective CACert / BindPassword on a COPY-friendly receiver. It
// mutates the receiver in place; dial/bindAdmin call it on their own
// value copy so the persisted config is never rewritten. Missing files
// are a hard error (a misconfigured path must not silently fall through
// to an empty/blob credential and mask the problem).
func (c *Config) applyExternalSecrets() error {
	if f := strings.TrimSpace(c.CAFile); f != "" {
		pem, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("ldap caFile %q: %w", f, err)
		}
		c.CACert = string(pem)
	}
	switch {
	case strings.TrimSpace(c.BindPasswordFile) != "":
		f := strings.TrimSpace(c.BindPasswordFile)
		pw, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("ldap bindPasswordFile %q: %w", f, err)
		}
		// Trim a single trailing newline — the common `echo secret > file`
		// shape — but preserve any other whitespace the secret may embed.
		c.BindPassword = strings.TrimRight(string(pw), "\r\n")
	case strings.TrimSpace(c.BindPasswordEnv) != "":
		if v, ok := os.LookupEnv(strings.TrimSpace(c.BindPasswordEnv)); ok {
			c.BindPassword = v
		}
	}
	return nil
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
	DN          string `json:"dn"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	// Department / Company — directory org info (v0.8.266, operator:
	// "organizasyon, ad soyad, ekip bilgisi de gelsin"). AD names them
	// department/company; inetOrgPerson uses ou/o — dirText resolves
	// the fallback. Department feeds the users.team column on login,
	// Company the org column.
	Department string   `json:"department,omitempty"`
	Company    string   `json:"company,omitempty"`
	Groups     []string `json:"groups,omitempty"`
	// Photo — raw thumbnailPhoto (AD) / jpegPhoto (inetOrgPerson) bytes
	// (v0.8.238). Never serialized: the directory-search UI JSON must
	// not ship images; the login path persists it to the users row and
	// the photo endpoints serve it from there.
	Photo []byte `json:"-"`
}

// maxPhotoBytes caps what we take from the directory. AD's
// thumbnailPhoto convention is ≤100 KB; jpegPhoto can be arbitrary —
// half a megabyte is plenty for an avatar and keeps the users row
// (whole-row-replaced on every upsert) small.
const maxPhotoBytes = 512 * 1024

// photoFromEntry pulls the profile-photo bytes off a directory entry:
// thumbnailPhoto (AD, small by convention) wins over jpegPhoto
// (inetOrgPerson). Oversized values are DROPPED, not truncated — a
// truncated JPEG renders as a broken image, no photo renders as the
// initials fallback. nil when neither attribute is present.
func photoFromEntry(e *goldap.Entry) []byte {
	for _, attr := range []string{"thumbnailPhoto", "jpegPhoto"} {
		if v := e.GetRawAttributeValue(attr); len(v) > 0 {
			if len(v) > maxPhotoBytes {
				log.Printf("[ldap] %s on %q is %d bytes (> %d cap) — skipping photo", attr, e.DN, len(v), maxPhotoBytes)
				continue
			}
			return v
		}
	}
	return nil
}

// dirText returns the first non-empty value among the given
// directory attributes — AD and inetOrgPerson name the same concept
// differently (department vs ou, company vs o), and some directories
// pad values with whitespace.
func dirText(e *goldap.Entry, attrs ...string) string {
	for _, a := range attrs {
		if v := strings.TrimSpace(e.GetAttributeValue(a)); v != "" {
			return v
		}
	}
	return ""
}

// deepestOU returns the leaf-most ou= component of a DN — in
// OU-per-team directory trees (CN=user,OU=SubTeam,OU=Division,…)
// that is the user's sub-team. Empty when the DN has no OU or fails
// to parse.
func deepestOU(dn string) string {
	parsed, err := goldap.ParseDN(dn)
	if err != nil {
		return ""
	}
	for _, rdn := range parsed.RDNs {
		for _, a := range rdn.Attributes {
			if strings.EqualFold(a.Type, "ou") && strings.TrimSpace(a.Value) != "" {
				return strings.TrimSpace(a.Value)
			}
		}
	}
	return ""
}

// teamFor resolves the users.team value for a directory entry per the
// TeamAttribute config (v0.8.430) — see the Config field comment —
// then applies the optional TeamRegex extraction (v0.8.434).
func teamFor(e *goldap.Entry, c Config) string {
	var raw string
	switch attr := strings.TrimSpace(c.TeamAttribute); attr {
	case "":
		raw = dirText(e, "department", "ou")
	case "dn-ou":
		raw = deepestOU(e.DN)
		if raw == "" {
			raw = dirText(e, "department", "ou")
		}
	default:
		raw = dirText(e, attr, "department", "ou")
	}
	return applyTeamRegex(raw, c.TeamRegex)
}

// applyTeamRegex — the pure v0.8.434 extraction half of teamFor. Empty
// pattern or empty input pass through; a match yields the first capture
// group (whole match without groups); NO match yields "" — see the
// Config.TeamRegex comment for why not-raw. An invalid pattern is
// treated as unset (Configure logs it once).
func applyTeamRegex(raw, pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || raw == "" {
		return raw
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return raw
	}
	m := re.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	if len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(m[0])
}

// teamAttrForFetch returns the extra attribute the searches must
// request for teamFor to see it — "" when TeamAttribute is unset or
// DN-derived (the DN always comes back).
func teamAttrForFetch(c Config) string {
	attr := strings.TrimSpace(c.TeamAttribute)
	if attr == "" || attr == "dn-ou" {
		return ""
	}
	return attr
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
	// v0.8.434 — surface a broken TeamRegex at config time (teamFor
	// silently ignores it per the field contract).
	if p := strings.TrimSpace(incoming.TeamRegex); p != "" {
		if _, err := regexp.Compile(p); err != nil {
			log.Printf("[ldap] teamRegex %q is invalid and will be IGNORED: %v", p, err)
		}
	}
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
	if err := c.applyExternalSecrets(); err != nil {
		return nil, err
	}
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
	// Resolve file/env secret refs on our own copy before both the TLS
	// (dial) and the bind read them. dial re-applies on its copy too;
	// applyExternalSecrets is idempotent.
	c.Normalize()
	if err := c.applyExternalSecrets(); err != nil {
		return nil, err
	}
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

// groupSyncSearcher opens + service-binds a connection for the group-sync
// engine (v0.8.526). The returned *goldap.Conn satisfies ldapSearcher and
// MUST be Close()d by the caller. One connection is reused for the group
// enumerate + every per-group member search in a sync round.
func (s *Service) groupSyncSearcher() (*goldap.Conn, error) {
	c := s.rawConfig()
	if !c.Enabled || c.Host == "" {
		return nil, errors.New("ldap not enabled")
	}
	return bindAdmin(c)
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
	// v0.8.238 — profile photo. AD stores it in thumbnailPhoto,
	// OpenLDAP inetOrgPerson in jpegPhoto. Requested unconditionally:
	// an absent attribute costs nothing on the wire, and photoFromEntry
	// caps what we keep (maxPhotoBytes).
	attrs = append(attrs, "thumbnailPhoto", "jpegPhoto")
	// v0.8.266 — directory org info (department/company + the
	// inetOrgPerson ou/o equivalents). Absent attrs are free.
	attrs = append(attrs, "department", "ou", "company", "o")
	if extra := teamAttrForFetch(c); extra != "" {
		attrs = append(attrs, extra)
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
		Department:  teamFor(e, c),
		Company:     dirText(e, "company", "o"),
		Photo:       photoFromEntry(e),
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
		append([]string{"dn", c.UserAttribute, c.EmailAttribute, c.DisplayAttribute,
			"department", "ou", "company", "o"},
			func() []string {
				if extra := teamAttrForFetch(c); extra != "" {
					return []string{extra}
				}
				return nil
			}()...),
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
			Department:  teamFor(e, c),
			Company:     dirText(e, "company", "o"),
		})
	}
	return out, nil
}

// Authenticate runs the standard "search-then-bind" auth pattern:
//  1. Service-bind (admin lookup credentials).
//  2. Find the user by username (or email — `username` may be either,
//     the configured UserSearchFilter handles it).
//  3. Re-bind with the user's DN + entered password — that's the
//     actual credential check.
//  4. Resolve groups → role via the configured GroupRoleMap; fall
//     back to DefaultRole.
//
// InspectResult is one directory entry with EVERY attribute the
// service account can read — the discovery affordance behind
// GET /api/settings/ldap/inspect (v0.8.430). Operator use-case:
// "users.team yanlış attribute'tan geliyor — alt ekip hangi
// attribute'ta?" Binary values (photos, GUIDs) are summarized as
// [N bytes], never shipped raw.
type InspectResult struct {
	DN         string              `json:"dn"`
	DeepestOU  string              `json:"deepestOu"` // what teamAttribute="dn-ou" would yield
	Team       string              `json:"team"`      // what the CURRENT config yields
	Attributes map[string][]string `json:"attributes"`
	// TeamCandidates (v0.8.523) — attribute başına tıkla-seç ekip
	// çıkarım adayları (canlı önizlemeli); UI bunlardan birini seçince
	// TeamAttribute+TeamRegex otomatik dolar.
	TeamCandidates map[string][]TeamCandidate `json:"teamCandidates,omitempty"`
}

// InspectUser finds one user (same filter the login path uses) and
// returns all readable attributes.
func (s *Service) InspectUser(ctx context.Context, username string) (*InspectResult, error) {
	c := s.rawConfig()
	conn, err := bindAdmin(c)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// v0.8.524 (operator-reported): Inspect kendi ham ReplaceAll'unu
	// yapıyordu — UserSearchFilter {{username}} placeholder'ı
	// İÇERMİYORSA (Dex-stili ön-filtre, ör. "(objectclass=person)")
	// yazılan kullanıcı adı filtreye hiç girmiyor, geniş filtrenin İLK
	// kaydı dönüyordu ("kendi bind user'ına bakıyor" belirtisi). Login
	// yolunun resolveUserFilter'ı bu durumu zaten doğru sarıyor —
	// artık Inspect de aynı yolu kullanır.
	filter := resolveUserFilter(c.UserSearchFilter, goldap.EscapeFilter(username))
	req := goldap.NewSearchRequest(
		c.BaseDN, goldap.ScopeWholeSubtree, goldap.NeverDerefAliases,
		2, 30, false,
		filter,
		[]string{"*"},
		nil,
	)
	res, err := conn.Search(req)
	if err != nil && !(goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded) && res != nil && len(res.Entries) > 0) {
		return nil, fmt.Errorf("inspect search: %w", err)
	}
	if res == nil || len(res.Entries) == 0 {
		return nil, fmt.Errorf("no directory entry matches %q (filter: %s)", username, filter)
	}
	// Belirsizlik koruması: filtre birden çok kayıt eşliyorsa keyfî
	// ilkini gösterme — login yolundaki "filter too loose" sözleşmesi.
	if len(res.Entries) > 1 {
		return nil, fmt.Errorf("user search returned %d entries for %q (filter too loose? filter: %s)",
			len(res.Entries), username, filter)
	}
	e := res.Entries[0]
	attrs := make(map[string][]string, len(e.Attributes))
	for _, a := range e.Attributes {
		vals := make([]string, 0, len(a.Values))
		for i, v := range a.Values {
			// Binary heuristics: known photo attrs OR non-UTF8 content.
			if a.Name == "thumbnailPhoto" || a.Name == "jpegPhoto" || !utf8.ValidString(v) {
				vals = append(vals, fmt.Sprintf("[%d bytes]", len(a.ByteValues[i])))
				continue
			}
			vals = append(vals, v)
		}
		attrs[a.Name] = vals
	}
	// v0.8.523 — uygun (tek-değerli-vari, metin) attribute'lar için
	// tıkla-seç ekip adayları üret.
	cands := map[string][]TeamCandidate{}
	for name, vals := range attrs {
		if !candidateEligible(vals) {
			continue
		}
		if c := TeamCandidates(vals[0]); len(c) > 0 {
			cands[name] = c
		}
	}
	return &InspectResult{
		DN:             e.DN,
		DeepestOU:      deepestOU(e.DN),
		Team:           teamFor(e, c),
		Attributes:     attrs,
		TeamCandidates: cands,
	}, nil
}

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
