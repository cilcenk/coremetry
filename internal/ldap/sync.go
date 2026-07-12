// sync.go — LDAP/AD group-membership sync engine (v0.8.526).
//
// Enumerates the in-scope directory groups and, per group, chain-searches
// their effective (nested-inclusive) members, then persists the result to
// ClickHouse (internal/chstore/ldap_groups.go) and publishes it as an
// atomically-swapped in-memory snapshot for O(1) authz lookups.
//
// Design (audit-driven):
//   - Group identity is objectGUID (stable across AD rename/move), not DN.
//   - Membership is resolved group→users via LDAP_MATCHING_RULE_IN_CHAIN
//     on a paged USER search, never member;range retrieval.
//   - The snapshot pointer is swapped ONLY on a fully successful Sync; any
//     page/connection error aborts the round and leaves the prior snapshot
//     (fail-stale, audit §7).
//   - The directory I/O sits behind the narrow ldapSearcher interface so
//     the engine is table-testable with a fake (sync_test.go, audit §12).
package ldap

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/selfobs"
	goldap "github.com/go-ldap/ldap/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ldapSearcher is the minimal directory surface the engine needs. The
// go-ldap *Conn satisfies it directly; tests supply a fake.
type ldapSearcher interface {
	SearchWithPaging(req *goldap.SearchRequest, pageSize uint32) (*goldap.SearchResult, error)
}

// searchConn is a connected ldapSearcher the caller must Close. *goldap.Conn
// satisfies it; the injectable dial seam lets sync_test.go drive Sync /
// Preview end-to-end against a fake directory (no real LDAP server).
type searchConn interface {
	ldapSearcher
	Close() error
}

// Syncer is the audit §2 contract: one round that returns the fresh
// snapshot or an error (never a partial). *SyncEngine satisfies it.
type Syncer interface {
	Sync(ctx context.Context) (*Snapshot, error)
}

var _ Syncer = (*SyncEngine)(nil)

// GroupStore is the persistence surface (implemented by *chstore.Store).
// Kept here with chstore-native types so the storage package carries no
// ldap dependency (feature → storage layering).
type GroupStore interface {
	UpsertLdapGroups(ctx context.Context, rows []chstore.LdapGroupRow, syncedAt time.Time) (written, tombstoned int, err error)
	HydrateLdapGroups(ctx context.Context) ([]chstore.LdapGroupRow, error)
	LdapIdentityOverlap(ctx context.Context, aliases []string) (matched, total int, err error)
}

// Group is one directory group + its effective members (lowercase
// sAMAccountName, sorted). UID is the objectGUID uuid string.
type Group struct {
	UID   string   `json:"uid"`
	CN    string   `json:"cn"`
	DN    string   `json:"dn"`
	Users []string `json:"users"`
}

// SyncStats is the per-round telemetry surfaced on the status card + /admin/stats.
type SyncStats struct {
	Groups     int     `json:"groups"`
	Users      int     `json:"users"`      // distinct member identities across all groups
	Pages      int     `json:"pages"`      // LDAP search calls issued this round
	Truncated  int     `json:"truncated"`  // groups clamped at MaxGroupMembers
	Tombstoned int     `json:"tombstoned"` // groups gone from the directory this round
	Matched    int     `json:"matched"`    // aliases that resolved to a users-table identity
	TotalAlias int     `json:"totalAlias"` // distinct alias keys
	MatchRatio float64 `json:"matchRatio"` // Matched / TotalAlias
	DurationMs int64   `json:"durationMs"`
}

// Snapshot is the published authz view. Groups is keyed by UID;
// UserGroups maps a normalized identity alias (lowercase sAMAccountName /
// UPN / UPN local-part / mail) to the group UIDs it belongs to. After a
// CH hydrate only the sAMAccountName alias is reconstructable (the richer
// aliases live only in the live-sync path — the durable join key is the
// lowercase sAMAccountName == users.ldap_username).
type Snapshot struct {
	Groups     map[string]Group    `json:"-"`
	UserGroups map[string][]string `json:"-"`
	SyncedAt   time.Time           `json:"syncedAt"`
	Stats      SyncStats           `json:"stats"`
}

// SyncEngine owns the snapshot pointer + directory/persistence wiring.
type SyncEngine struct {
	svc   *Service
	store GroupStore
	snap  atomic.Pointer[Snapshot]

	// dial opens a bound directory connection. Defaults to the real
	// service bind; overridden in tests to inject a fake directory.
	dial func() (searchConn, error)

	// Metrics — the first selfobs.Meter() call sites in the codebase.
	mDuration   metric.Float64Histogram
	mGroups     metric.Int64Gauge
	mUsers      metric.Int64Gauge
	mErrors     metric.Int64Counter
	mMatchRatio metric.Float64Gauge
}

// NewSyncEngine wires the engine. Instrument construction errors are
// logged, not fatal (they don't happen with a real or noop meter).
func NewSyncEngine(svc *Service, store GroupStore) *SyncEngine {
	e := &SyncEngine{svc: svc, store: store}
	e.dial = func() (searchConn, error) { return svc.groupSyncSearcher() }
	m := selfobs.Meter()
	var err error
	if e.mDuration, err = m.Float64Histogram("ldap_sync_duration_seconds",
		metric.WithDescription("Wall-clock duration of a full LDAP group sync"),
		metric.WithUnit("s")); err != nil {
		log.Printf("[ldap-groupsync] metric ldap_sync_duration_seconds: %v", err)
	}
	if e.mGroups, err = m.Int64Gauge("ldap_sync_groups",
		metric.WithDescription("Groups in the latest LDAP group snapshot")); err != nil {
		log.Printf("[ldap-groupsync] metric ldap_sync_groups: %v", err)
	}
	if e.mUsers, err = m.Int64Gauge("ldap_sync_users",
		metric.WithDescription("Distinct members in the latest LDAP group snapshot")); err != nil {
		log.Printf("[ldap-groupsync] metric ldap_sync_users: %v", err)
	}
	if e.mErrors, err = m.Int64Counter("ldap_sync_errors_total",
		metric.WithDescription("LDAP group sync failures")); err != nil {
		log.Printf("[ldap-groupsync] metric ldap_sync_errors_total: %v", err)
	}
	if e.mMatchRatio, err = m.Float64Gauge("ldap_sync_identity_match_ratio",
		metric.WithDescription("Fraction of synced group aliases resolving to a Coremetry user")); err != nil {
		log.Printf("[ldap-groupsync] metric ldap_sync_identity_match_ratio: %v", err)
	}
	return e
}

// Snapshot returns the current published snapshot (nil = never synced /
// hydrated). Cheap atomic load — safe on the login hot path.
func (e *SyncEngine) Snapshot() *Snapshot {
	if e == nil {
		return nil
	}
	return e.snap.Load()
}

// Enabled reports whether group sync is configured to run.
func (e *SyncEngine) Enabled() bool {
	if e == nil || e.svc == nil {
		return false
	}
	c := e.svc.rawConfig()
	return c.Enabled && c.Host != "" && c.GroupSync.Enabled
}

// SyncInterval is the configured tick period (default 30m).
func (e *SyncEngine) SyncInterval() time.Duration {
	if e == nil || e.svc == nil {
		return defaultGroupSyncInterval
	}
	return e.svc.rawConfig().GroupSync.IntervalDuration()
}

// SyncTimeout is the configured per-round wall-clock cap (default 60s).
func (e *SyncEngine) SyncTimeout() time.Duration {
	if e == nil || e.svc == nil {
		return defaultGroupSyncTimeout
	}
	return e.svc.rawConfig().GroupSync.TimeoutDuration()
}

// Sync runs one full round: enumerate in-scope groups → chain-search each
// group's members → persist (full set + tombstones) → compute identity
// overlap → atomically publish. Returns without touching the snapshot on
// ANY error (fail-stale).
func (e *SyncEngine) Sync(ctx context.Context) (*Snapshot, error) {
	cfg := e.svc.rawConfig()
	if !cfg.Enabled || cfg.Host == "" {
		return nil, fmt.Errorf("ldap not enabled")
	}
	if !cfg.GroupSync.Enabled {
		return nil, fmt.Errorf("ldap group sync not enabled")
	}

	tr := selfobs.Tracer()
	ctx, span := tr.Start(ctx, "ldap.groupsync")
	defer span.End()

	start := time.Now()
	searcher, err := e.dial()
	if err != nil {
		e.recordErr(ctx)
		return nil, fmt.Errorf("group sync bind: %w", err)
	}
	defer searcher.Close()

	refs, pages, err := enumerateGroups(searcher, cfg)
	if err != nil {
		e.recordErr(ctx)
		return nil, fmt.Errorf("enumerate groups: %w", err)
	}

	memberCap := cfg.GroupSync.MaxGroupMembers
	groups := make(map[string]Group, len(refs))
	userGroups := map[string]map[string]struct{}{}
	distinct := map[string]struct{}{}
	stats := SyncStats{Pages: pages}

	for _, ref := range refs {
		_, gspan := tr.Start(ctx, "ldap.search.users")
		gspan.SetAttributes(attribute.String("group_cn", ref.CN), attribute.String("group_dn", ref.DN))
		members, err := membersOfGroup(searcher, cfg, ref.DN)
		if err != nil {
			gspan.End()
			e.recordErr(ctx)
			return nil, fmt.Errorf("members of %q: %w", ref.CN, err)
		}
		stats.Pages++

		bySam := map[string]memberEntry{}
		for _, m := range members {
			sam := strings.ToLower(strings.TrimSpace(m.Sam))
			if sam == "" {
				continue
			}
			if _, ok := bySam[sam]; !ok {
				bySam[sam] = m
			}
		}
		sams := make([]string, 0, len(bySam))
		for sam := range bySam {
			sams = append(sams, sam)
		}
		sort.Strings(sams)
		if memberCap > 0 && len(sams) > memberCap {
			log.Printf("[ldap-groupsync] group %q has %d members > cap %d — truncating (set groupSync.maxGroupMembers to raise)", ref.CN, len(sams), memberCap)
			sams = sams[:memberCap]
			stats.Truncated++
		}
		gspan.SetAttributes(attribute.Int("members", len(sams)))

		g := ref
		g.Users = sams
		groups[g.UID] = g
		for _, sam := range sams {
			distinct[sam] = struct{}{}
			m := bySam[sam]
			for _, key := range aliasKeysFor(m.Sam, m.UPN, m.Mail) {
				set := userGroups[key]
				if set == nil {
					set = map[string]struct{}{}
					userGroups[key] = set
				}
				set[g.UID] = struct{}{}
			}
		}
		gspan.End()
	}

	// Persist: full set + tombstones for groups gone from the directory.
	syncedAt := time.Now()
	rows := groupsToRows(groups)
	written, tombstoned, err := e.store.UpsertLdapGroups(ctx, rows, syncedAt)
	if err != nil {
		e.recordErr(ctx)
		return nil, fmt.Errorf("persist ldap groups: %w", err)
	}
	stats.Groups = written
	stats.Tombstoned = tombstoned
	stats.Users = len(distinct)

	// Identity overlap (§10) — early-warning for the sAMAccountName↔email gap.
	aliases := make([]string, 0, len(userGroups))
	for k := range userGroups {
		aliases = append(aliases, k)
	}
	matched, total, err := e.store.LdapIdentityOverlap(ctx, aliases)
	if err != nil {
		// Overlap is a diagnostic, not correctness — log + continue; the
		// snapshot still publishes.
		log.Printf("[ldap-groupsync] identity overlap: %v", err)
	} else {
		stats.Matched, stats.TotalAlias = matched, total
		if total > 0 {
			stats.MatchRatio = float64(matched) / float64(total)
			if matched == 0 {
				log.Printf("[ldap-groupsync] WARN: sync succeeded (%d groups, %d members) but NO alias matched any Coremetry user — check groupSync.userNameAttribute / alias mapping vs users.email / users.ldap_username", written, len(distinct))
			}
		}
	}
	stats.DurationMs = time.Since(start).Milliseconds()

	snap := &Snapshot{
		Groups:     groups,
		UserGroups: finalizeUserGroups(userGroups),
		SyncedAt:   syncedAt,
		Stats:      stats,
	}
	e.snap.Store(snap)
	e.recordSuccess(ctx, stats)
	log.Printf("[ldap-groupsync] synced %d groups, %d distinct members, %d tombstoned, match_ratio=%.2f in %dms",
		stats.Groups, stats.Users, stats.Tombstoned, stats.MatchRatio, stats.DurationMs)
	return snap, nil
}

// Hydrate rebuilds the snapshot from ClickHouse (boot + periodic
// follower refresh). Only the sAMAccountName alias is reconstructable
// from storage; that is the durable authz join key.
func (e *SyncEngine) Hydrate(ctx context.Context) error {
	if e == nil || e.store == nil {
		return nil
	}
	rows, err := e.store.HydrateLdapGroups(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		// No sync has ever run — leave snapshot nil so group-based authz
		// stays inert (DefaultRole path), never locking everyone out.
		return nil
	}
	e.snap.Store(buildSnapshotFromRows(rows))
	return nil
}

// StartHydrateRefresh re-hydrates the snapshot from CH every `interval`
// on ALL pods, so api/follower pods track what the leader wrote. interval
// ≤ 0 → 30s. Mirrors Service.StartConfigRefresh.
func (e *SyncEngine) StartHydrateRefresh(ctx context.Context, interval time.Duration) {
	if e == nil {
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
			if err := e.Hydrate(ctx); err != nil {
				log.Printf("[ldap-groupsync] hydrate refresh: %v", err)
			}
		}
	}
}

// ── API projections ─────────────────────────────────────────────────────────

// GroupSummary is one group in the status card (no member list).
type GroupSummary struct {
	UID         string `json:"uid"`
	CN          string `json:"cn"`
	DN          string `json:"dn"`
	MemberCount int    `json:"memberCount"`
}

// SyncSummary is the GET /api/admin/ldap/groupsync payload.
type SyncSummary struct {
	Configured bool           `json:"configured"` // ldap enabled + host set
	Enabled    bool           `json:"enabled"`    // group sync turned on
	Interval   string         `json:"interval"`
	Synced     bool           `json:"synced"` // a snapshot exists
	SyncedAt   *time.Time     `json:"syncedAt,omitempty"`
	Groups     []GroupSummary `json:"groups"`
	Stats      SyncStats      `json:"stats"`
}

// Summary projects the current snapshot for the admin status card. Reads
// the in-memory pointer only — no directory or CH I/O.
func (e *SyncEngine) Summary() SyncSummary {
	cfg := e.svc.rawConfig()
	out := SyncSummary{
		Configured: cfg.Enabled && cfg.Host != "",
		Enabled:    cfg.Enabled && cfg.Host != "" && cfg.GroupSync.Enabled,
		Interval:   cfg.GroupSync.IntervalDuration().String(),
		Groups:     []GroupSummary{},
	}
	snap := e.snap.Load()
	if snap == nil {
		return out
	}
	out.Synced = true
	t := snap.SyncedAt
	out.SyncedAt = &t
	out.Stats = snap.Stats
	for _, g := range snap.Groups {
		out.Groups = append(out.Groups, GroupSummary{UID: g.UID, CN: g.CN, DN: g.DN, MemberCount: len(g.Users)})
	}
	sort.Slice(out.Groups, func(i, j int) bool { return out.Groups[i].CN < out.Groups[j].CN })
	return out
}

// PreviewGroup is one sampled group in a dry-run.
type PreviewGroup struct {
	UID           string   `json:"uid"`
	CN            string   `json:"cn"`
	DN            string   `json:"dn"`
	MemberCount   int      `json:"memberCount"`
	SampleMembers []string `json:"sampleMembers"`
}

// PreviewResult is the GET /api/admin/ldap/groupsync/preview payload — a
// live dry-run that NEVER writes CH or swaps the snapshot.
type PreviewResult struct {
	TotalGroupsInScope int            `json:"totalGroupsInScope"`
	SampledGroups      int            `json:"sampledGroups"`
	Groups             []PreviewGroup `json:"groups"`
	Matched            int            `json:"matched"`
	TotalAliases       int            `json:"totalAliases"`
	MatchRatio         float64        `json:"matchRatio"`
	Warning            string         `json:"warning,omitempty"`
}

// Preview runs a bounded, read-only dry-run: enumerate in-scope groups,
// sample the first few + their first members, and compute the identity
// overlap — without persisting or publishing. The diagnostic surface for
// the sAMAccountName↔email early-warning before committing a real sync.
func (e *SyncEngine) Preview(ctx context.Context) (*PreviewResult, error) {
	const sampleGroups = 10
	const sampleMembers = 5

	cfg := e.svc.rawConfig()
	if !cfg.Enabled || cfg.Host == "" {
		return nil, fmt.Errorf("ldap not enabled")
	}
	if !cfg.GroupSync.Enabled {
		return nil, fmt.Errorf("ldap group sync not enabled")
	}
	searcher, err := e.dial()
	if err != nil {
		return nil, fmt.Errorf("group sync bind: %w", err)
	}
	defer searcher.Close()

	refs, _, err := enumerateGroups(searcher, cfg)
	if err != nil {
		return nil, fmt.Errorf("enumerate groups: %w", err)
	}
	res := &PreviewResult{TotalGroupsInScope: len(refs), Groups: []PreviewGroup{}}
	aliasSet := map[string]struct{}{}
	limit := min(len(refs), sampleGroups)
	for i := 0; i < limit; i++ {
		ref := refs[i]
		members, err := membersOfGroup(searcher, cfg, ref.DN)
		if err != nil {
			return nil, fmt.Errorf("members of %q: %w", ref.CN, err)
		}
		bySam := map[string]memberEntry{}
		var sams []string
		for _, m := range members {
			sam := strings.ToLower(strings.TrimSpace(m.Sam))
			if sam == "" {
				continue
			}
			if _, ok := bySam[sam]; !ok {
				bySam[sam] = m
				sams = append(sams, sam)
			}
			for _, k := range aliasKeysFor(m.Sam, m.UPN, m.Mail) {
				aliasSet[k] = struct{}{}
			}
		}
		sort.Strings(sams)
		sample := sams
		if len(sample) > sampleMembers {
			sample = sample[:sampleMembers]
		}
		res.Groups = append(res.Groups, PreviewGroup{
			UID: ref.UID, CN: ref.CN, DN: ref.DN,
			MemberCount: len(sams), SampleMembers: append([]string{}, sample...),
		})
		res.SampledGroups++
	}

	aliases := make([]string, 0, len(aliasSet))
	for k := range aliasSet {
		aliases = append(aliases, k)
	}
	matched, total, err := e.store.LdapIdentityOverlap(ctx, aliases)
	if err == nil {
		res.Matched, res.TotalAliases = matched, total
		if total > 0 {
			res.MatchRatio = float64(matched) / float64(total)
			if matched == 0 {
				res.Warning = "sampled groups have members but NONE match a Coremetry user (email / ldap_username) — check groupSync.userNameAttribute and the identity alias mapping before running a full sync"
			}
		}
	}
	return res, nil
}

func (e *SyncEngine) recordErr(ctx context.Context) {
	if e.mErrors != nil {
		e.mErrors.Add(ctx, 1)
	}
}

func (e *SyncEngine) recordSuccess(ctx context.Context, s SyncStats) {
	if e.mDuration != nil {
		e.mDuration.Record(ctx, float64(s.DurationMs)/1000.0)
	}
	if e.mGroups != nil {
		e.mGroups.Record(ctx, int64(s.Groups))
	}
	if e.mUsers != nil {
		e.mUsers.Record(ctx, int64(s.Users))
	}
	if e.mMatchRatio != nil {
		e.mMatchRatio.Record(ctx, s.MatchRatio)
	}
}

// memberEntry is one parsed directory member of a group.
type memberEntry struct {
	Sam  string
	UPN  string
	Mail string
}

// enumerateGroups pages the (objectClass=group) search under the group
// base and keeps those passing the include/exclude DN-suffix filters.
// Returns groups WITHOUT members (filled per-group by membersOfGroup).
func enumerateGroups(searcher ldapSearcher, cfg Config) ([]Group, int, error) {
	timeoutSecs := int(cfg.GroupSync.TimeoutDuration().Seconds())
	req := goldap.NewSearchRequest(
		cfg.groupsSearchBase(), goldap.ScopeWholeSubtree, goldap.NeverDerefAliases,
		0, timeoutSecs, false,
		cfg.GroupSync.GroupFilter,
		[]string{"dn", "cn", "objectGUID"},
		nil,
	)
	res, err := searcher.SearchWithPaging(req, cfg.GroupSync.PageSize)
	// AD may return SizeLimitExceeded alongside a partial result — tolerate
	// it if we got entries (mirrors the login-path search handling).
	if err != nil && !(goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded) && res != nil && len(res.Entries) > 0) {
		return nil, 0, err
	}
	if res == nil {
		return nil, 1, nil
	}
	out := make([]Group, 0, len(res.Entries))
	for _, en := range res.Entries {
		if !groupInScope(en.DN, cfg.GroupSync.IncludePrefixes, cfg.GroupSync.ExcludePrefixes) {
			continue
		}
		uid := parseObjectGUID(en.GetRawAttributeValue("objectGUID"))
		if uid == "" {
			// No/unreadable objectGUID — fall back to the lowercased DN as
			// a stable-enough identity so the group still syncs.
			uid = strings.ToLower(en.DN)
		}
		out = append(out, Group{UID: uid, CN: en.GetAttributeValue("cn"), DN: en.DN})
	}
	return out, 1, nil
}

// membersOfGroup chain-searches the effective (nested-inclusive) members
// of one group via LDAP_MATCHING_RULE_IN_CHAIN, AND-ed with the operator
// pre-filter.
func membersOfGroup(searcher ldapSearcher, cfg Config, groupDN string) ([]memberEntry, error) {
	chain := fmt.Sprintf("(memberOf:1.2.840.113556.1.4.1941:=%s)", goldap.EscapeFilter(groupDN))
	filter := andFilter(cfg.GroupSync.UserFilter, chain)
	timeoutSecs := int(cfg.GroupSync.TimeoutDuration().Seconds())
	attrs := []string{"dn", cfg.GroupSync.UserNameAttribute, "userPrincipalName", "mail"}
	req := goldap.NewSearchRequest(
		cfg.usersSearchBase(), goldap.ScopeWholeSubtree, goldap.NeverDerefAliases,
		0, timeoutSecs, false,
		filter, attrs, nil,
	)
	res, err := searcher.SearchWithPaging(req, cfg.GroupSync.PageSize)
	if err != nil && !(goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded) && res != nil && len(res.Entries) > 0) {
		return nil, err
	}
	if res == nil {
		return nil, nil
	}
	out := make([]memberEntry, 0, len(res.Entries))
	for _, en := range res.Entries {
		out = append(out, memberEntry{
			Sam:  en.GetAttributeValue(cfg.GroupSync.UserNameAttribute),
			UPN:  en.GetAttributeValue("userPrincipalName"),
			Mail: en.GetAttributeValue("mail"),
		})
	}
	return out, nil
}

// andFilter combines the operator pre-filter with the chain predicate.
// Empty pre-filter → just the chain; otherwise (&<pre><chain>).
func andFilter(pre, chain string) string {
	pre = strings.TrimSpace(pre)
	if pre == "" {
		return chain
	}
	return "(&" + pre + chain + ")"
}

// groupInScope applies the DN-suffix include/exclude filters. A DN reads
// leaf→root ("CN=x,OU=y,DC=corp,DC=example"), so an OU-chain "prefix" is
// actually a DN SUFFIX — scope membership is a case-insensitive
// strings.HasSuffix. Empty include list = all groups included; exclude
// always wins.
func groupInScope(dn string, include, exclude []string) bool {
	ldn := strings.ToLower(strings.TrimSpace(dn))
	for _, ex := range exclude {
		ex = strings.ToLower(strings.TrimSpace(ex))
		if ex != "" && strings.HasSuffix(ldn, ex) {
			return false
		}
	}
	if len(include) == 0 {
		return true
	}
	for _, in := range include {
		in = strings.ToLower(strings.TrimSpace(in))
		if in != "" && strings.HasSuffix(ldn, in) {
			return true
		}
	}
	return false
}

// aliasKeysFor builds the lowercase identity aliases for one member: the
// sAMAccountName, the UPN, the UPN's local-part, and the mail address.
// Deduped, empties dropped. These are the keys UserGroups is indexed by
// and the alias set the identity-overlap check probes.
func aliasKeysFor(sam, upn, mail string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(v string) {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	add(sam)
	add(upn)
	if at := strings.IndexByte(upn, '@'); at > 0 {
		add(upn[:at])
	}
	add(mail)
	return out
}

// parseObjectGUID converts an AD objectGUID octet string to its canonical
// display UUID. AD stores the first three fields little-endian and the
// last two big-endian (the classic mixed-endian GUID). Returns "" for a
// non-16-byte value so the caller can fall back to the DN.
func parseObjectGUID(b []byte) string {
	if len(b) != 16 {
		return ""
	}
	d1 := binary.LittleEndian.Uint32(b[0:4])
	d2 := binary.LittleEndian.Uint16(b[4:6])
	d3 := binary.LittleEndian.Uint16(b[6:8])
	return fmt.Sprintf("%08x-%04x-%04x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		d1, d2, d3, b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

// groupsToRows maps the in-memory groups to storage rows (stable order by
// UID so the batch is deterministic).
func groupsToRows(groups map[string]Group) []chstore.LdapGroupRow {
	uids := make([]string, 0, len(groups))
	for uid := range groups {
		uids = append(uids, uid)
	}
	sort.Strings(uids)
	rows := make([]chstore.LdapGroupRow, 0, len(uids))
	for _, uid := range uids {
		g := groups[uid]
		rows = append(rows, chstore.LdapGroupRow{
			UID: g.UID, DN: g.DN, CN: g.CN, Users: g.Users,
		})
	}
	return rows
}

// buildSnapshotFromRows reconstructs a snapshot from stored rows (boot
// hydrate). UserGroups is keyed by lowercase sAMAccountName only.
func buildSnapshotFromRows(rows []chstore.LdapGroupRow) *Snapshot {
	groups := make(map[string]Group, len(rows))
	userGroups := map[string]map[string]struct{}{}
	distinct := map[string]struct{}{}
	var latest int64
	for _, r := range rows {
		groups[r.UID] = Group{UID: r.UID, CN: r.CN, DN: r.DN, Users: r.Users}
		if r.SyncedAt > latest {
			latest = r.SyncedAt
		}
		for _, sam := range r.Users {
			k := strings.ToLower(strings.TrimSpace(sam))
			if k == "" {
				continue
			}
			distinct[k] = struct{}{}
			set := userGroups[k]
			if set == nil {
				set = map[string]struct{}{}
				userGroups[k] = set
			}
			set[r.UID] = struct{}{}
		}
	}
	return &Snapshot{
		Groups:     groups,
		UserGroups: finalizeUserGroups(userGroups),
		SyncedAt:   time.Unix(0, latest).UTC(),
		Stats:      SyncStats{Groups: len(groups), Users: len(distinct)},
	}
}

// finalizeUserGroups converts the alias→set map into a sorted slice map.
func finalizeUserGroups(in map[string]map[string]struct{}) map[string][]string {
	out := make(map[string][]string, len(in))
	for alias, set := range in {
		uids := make([]string, 0, len(set))
		for uid := range set {
			uids = append(uids, uid)
		}
		sort.Strings(uids)
		out[alias] = uids
	}
	return out
}
