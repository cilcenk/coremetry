package ldap

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	goldap "github.com/go-ldap/ldap/v3"
)

// sync_test.go — v0.8.526 LDAP/AD group-sync engine tests (audit §12).
// The directory sits behind the injectable dial seam + a fake ldapSearcher;
// no real LDAP server. Covers: (1) paging + pageSize propagation, (2) the
// chain group→users inversion + include/exclude DN-suffix filters, (3)
// fail-stale (a failed round leaves the prior snapshot), (4) identity alias
// normalization, (5) objectGUID octet→uuid parse, (6) boot hydrate.

// ── fakes ────────────────────────────────────────────────────────────────────

type fakeSearcher struct {
	groups    []*goldap.Entry            // enumerate response
	members   map[string][]*goldap.Entry // per-groupDN member response
	pageSizes []uint32                   // captured pageSize per call
	enumErr   error                      // fail the group enumerate
	memberErr error                      // fail every member search
	closed    bool
}

func (f *fakeSearcher) SearchWithPaging(req *goldap.SearchRequest, pageSize uint32) (*goldap.SearchResult, error) {
	f.pageSizes = append(f.pageSizes, pageSize)
	if strings.Contains(req.Filter, "1.2.840.113556.1.4.1941") {
		if f.memberErr != nil {
			return nil, f.memberErr
		}
		dn := chainDN(req.Filter)
		return &goldap.SearchResult{Entries: f.members[dn]}, nil
	}
	if f.enumErr != nil {
		return nil, f.enumErr
	}
	return &goldap.SearchResult{Entries: f.groups}, nil
}

func (f *fakeSearcher) Close() error { f.closed = true; return nil }

// chainDN pulls the group DN out of (memberOf:1.2.840.113556.1.4.1941:=<dn>).
func chainDN(filter string) string {
	const marker = "1.2.840.113556.1.4.1941:="
	i := strings.Index(filter, marker)
	if i < 0 {
		return ""
	}
	rest := filter[i+len(marker):]
	// EscapeFilter escapes any literal ')' in the DN, so the FIRST ')'
	// terminates the memberOf clause.
	if j := strings.IndexByte(rest, ')'); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

type upsertCall struct {
	rows     []chstore.LdapGroupRow
	syncedAt time.Time
}

type fakeStore struct {
	upserts    []upsertCall
	hydrate    []chstore.LdapGroupRow
	matched    int
	total      int
	upsertErr  error
	hydrateErr error
}

func (f *fakeStore) UpsertLdapGroups(ctx context.Context, rows []chstore.LdapGroupRow, syncedAt time.Time) (int, int, error) {
	if f.upsertErr != nil {
		return 0, 0, f.upsertErr
	}
	f.upserts = append(f.upserts, upsertCall{rows: rows, syncedAt: syncedAt})
	return len(rows), 0, nil
}

func (f *fakeStore) HydrateLdapGroups(ctx context.Context) ([]chstore.LdapGroupRow, error) {
	return f.hydrate, f.hydrateErr
}

func (f *fakeStore) LdapIdentityOverlap(ctx context.Context, aliases []string) (int, int, error) {
	if f.total == 0 {
		return f.matched, len(aliases), nil
	}
	return f.matched, f.total, nil
}

// ── entry builders ───────────────────────────────────────────────────────────

func groupEntry(dn, cn string, guid []byte) *goldap.Entry {
	return &goldap.Entry{DN: dn, Attributes: []*goldap.EntryAttribute{
		{Name: "cn", Values: []string{cn}},
		{Name: "objectGUID", ByteValues: [][]byte{guid}},
	}}
}

func userEntry(dn, sam, upn, mail string) *goldap.Entry {
	return &goldap.Entry{DN: dn, Attributes: []*goldap.EntryAttribute{
		{Name: "sAMAccountName", Values: []string{sam}},
		{Name: "userPrincipalName", Values: []string{upn}},
		{Name: "mail", Values: []string{mail}},
	}}
}

// guid16 returns a deterministic 16-byte objectGUID for the given seed.
func guid16(seed byte) []byte {
	b := make([]byte, 16)
	for i := range b {
		b[i] = seed + byte(i)
	}
	return b
}

// baseCfg is an enabled group-sync config against a sanitized directory.
func baseCfg() Config {
	c := Config{
		Enabled: true,
		Host:    "dc.corp.example",
		BaseDN:  "DC=corp,DC=example",
		GroupSync: GroupSyncConfig{
			Enabled:      true,
			UsersBaseDN:  "OU=Users,DC=corp,DC=example",
			GroupsBaseDN: "OU=Groups,DC=corp,DC=example",
		},
	}
	c.Normalize()
	return c
}

func newTestEngine(searcher searchConn, store GroupStore, cfg Config) *SyncEngine {
	svc := New()
	svc.Configure(cfg)
	e := NewSyncEngine(svc, store)
	e.dial = func() (searchConn, error) { return searcher, nil }
	return e
}

// ── (1) paging + pageSize propagation ────────────────────────────────────────

func TestSyncPagingAndPageSize(t *testing.T) {
	fs := &fakeSearcher{
		groups: []*goldap.Entry{
			groupEntry("CN=Eng,OU=Groups,DC=corp,DC=example", "Eng", guid16(1)),
			groupEntry("CN=Sre,OU=Groups,DC=corp,DC=example", "Sre", guid16(20)),
			groupEntry("CN=Ops,OU=Groups,DC=corp,DC=example", "Ops", guid16(40)),
		},
		members: map[string][]*goldap.Entry{
			"CN=Eng,OU=Groups,DC=corp,DC=example": {userEntry("CN=A,OU=Users,DC=corp,DC=example", "alice", "alice@corp.example", "alice@corp.example")},
			"CN=Sre,OU=Groups,DC=corp,DC=example": {userEntry("CN=B,OU=Users,DC=corp,DC=example", "bob", "bob@corp.example", "")},
			"CN=Ops,OU=Groups,DC=corp,DC=example": {},
		},
	}
	store := &fakeStore{}
	e := newTestEngine(fs, store, baseCfg())

	snap, err := e.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if snap.Stats.Groups != 3 {
		t.Fatalf("groups = %d, want 3", snap.Stats.Groups)
	}
	if !fs.closed {
		t.Fatalf("searcher not closed")
	}
	// Every SearchWithPaging call (1 enumerate + 3 member searches) must
	// carry the configured/defaulted page size (500).
	if len(fs.pageSizes) != 4 {
		t.Fatalf("search calls = %d, want 4", len(fs.pageSizes))
	}
	for i, ps := range fs.pageSizes {
		if ps != defaultGroupSyncPageSize {
			t.Fatalf("call %d pageSize = %d, want %d", i, ps, defaultGroupSyncPageSize)
		}
	}
	// Distinct members across all groups = alice, bob.
	if snap.Stats.Users != 2 {
		t.Fatalf("distinct users = %d, want 2", snap.Stats.Users)
	}
}

// ── (2) chain inversion + include/exclude DN-suffix filters ───────────────────

func TestSyncChainInversionAndPrefixFilters(t *testing.T) {
	inScope := "CN=Payments,OU=App,OU=Groups,DC=corp,DC=example"
	excluded := "CN=Builtin,OU=System,OU=Groups,DC=corp,DC=example"
	unincluded := "CN=Random,OU=Other,DC=corp,DC=example"

	fs := &fakeSearcher{
		groups: []*goldap.Entry{
			groupEntry(inScope, "Payments", guid16(1)),
			groupEntry(excluded, "Builtin", guid16(20)),
			groupEntry(unincluded, "Random", guid16(40)),
		},
		members: map[string][]*goldap.Entry{
			inScope: {
				userEntry("CN=A,OU=Users,DC=corp,DC=example", "Alice", "Alice@corp.example", "alice@corp.example"),
				userEntry("CN=B,OU=Users,DC=corp,DC=example", "bob", "bob@corp.example", "bob@corp.example"),
			},
		},
	}
	store := &fakeStore{}
	cfg := baseCfg()
	cfg.GroupSync.IncludePrefixes = []string{"OU=App,OU=Groups,DC=corp,DC=example"}
	cfg.GroupSync.ExcludePrefixes = []string{"OU=System,OU=Groups,DC=corp,DC=example"}
	e := newTestEngine(fs, store, cfg)

	snap, err := e.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	// Only the included-scope group survives (excluded blocked, unincluded
	// not whitelisted).
	if snap.Stats.Groups != 1 {
		t.Fatalf("groups = %d, want 1 (only in-scope)", snap.Stats.Groups)
	}
	uid := parseObjectGUID(guid16(1))
	g, ok := snap.Groups[uid]
	if !ok {
		t.Fatalf("in-scope group %s missing from snapshot", uid)
	}
	// Members stored lowercased + sorted.
	if !reflect.DeepEqual(g.Users, []string{"alice", "bob"}) {
		t.Fatalf("group users = %v, want [alice bob]", g.Users)
	}
	// group→users inversion: each alias maps back to the group UID.
	for _, alias := range []string{"alice", "alice@corp.example", "bob", "bob@corp.example"} {
		got := snap.UserGroups[alias]
		if len(got) != 1 || got[0] != uid {
			t.Fatalf("UserGroups[%q] = %v, want [%s]", alias, got, uid)
		}
	}
	// Only in-scope member searches ran: 1 enumerate + 1 member search.
	if len(fs.pageSizes) != 2 {
		t.Fatalf("search calls = %d, want 2 (excluded/unincluded groups never searched)", len(fs.pageSizes))
	}
	// Persisted exactly the one in-scope group.
	if len(store.upserts) != 1 || len(store.upserts[0].rows) != 1 {
		t.Fatalf("persisted rows = %+v, want 1 group", store.upserts)
	}
}

// ── (3) fail-stale — a failed round leaves the prior snapshot intact ──────────

func TestSyncFailStale(t *testing.T) {
	fs := &fakeSearcher{
		groups: []*goldap.Entry{groupEntry("CN=Eng,OU=Groups,DC=corp,DC=example", "Eng", guid16(1))},
		members: map[string][]*goldap.Entry{
			"CN=Eng,OU=Groups,DC=corp,DC=example": {userEntry("CN=A,OU=Users,DC=corp,DC=example", "alice", "alice@corp.example", "")},
		},
	}
	e := newTestEngine(fs, &fakeStore{}, baseCfg())

	first, err := e.Sync(context.Background())
	if err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if e.Snapshot() != first {
		t.Fatalf("snapshot pointer not published after first sync")
	}
	firstSyncedAt := first.SyncedAt

	// Second round: the directory enumerate fails.
	fs.enumErr = errors.New("directory unreachable")
	if _, err := e.Sync(context.Background()); err == nil {
		t.Fatalf("second Sync: expected error, got nil")
	}
	// Pointer + SyncedAt unchanged — fail-stale.
	if e.Snapshot() != first {
		t.Fatalf("snapshot swapped on a failed round (must stay stale)")
	}
	if !e.Snapshot().SyncedAt.Equal(firstSyncedAt) {
		t.Fatalf("SyncedAt mutated on a failed round")
	}
}

// ── (4) identity alias normalization ─────────────────────────────────────────

func TestAliasKeysFor(t *testing.T) {
	tests := []struct {
		name          string
		sam, upn, mel string
		want          []string
	}{
		{"upn split + lowercase", "JDoe", "JDoe@Corp.Example", "j.doe@corp.example",
			[]string{"jdoe", "jdoe@corp.example", "j.doe@corp.example"}},
		{"upn local part distinct from sam", "svc1", "service.one@corp.example", "",
			[]string{"svc1", "service.one@corp.example", "service.one"}},
		{"empty fields dropped + dedupe", "  alice  ", "alice", "ALICE",
			[]string{"alice"}},
		{"only sam", "bob", "", "", []string{"bob"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := aliasKeysFor(tc.sam, tc.upn, tc.mel)
			// Order isn't contractual beyond "sam-first"; compare as sets.
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("aliasKeysFor(%q,%q,%q) = %v, want %v", tc.sam, tc.upn, tc.mel, got, want)
			}
		})
	}
}

// ── (5) objectGUID octet-string → uuid parse ─────────────────────────────────

func TestParseObjectGUID(t *testing.T) {
	// AD stores the first three fields little-endian, the last two big-endian.
	b := []byte{0x10, 0x32, 0x54, 0x76, 0x98, 0xBA, 0xDC, 0xFE,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF}
	want := "76543210-ba98-fedc-0123-456789abcdef"
	if got := parseObjectGUID(b); got != want {
		t.Fatalf("parseObjectGUID = %q, want %q", got, want)
	}
	// Stability: same bytes → same string (RMT key must be stable).
	if parseObjectGUID(b) != parseObjectGUID(append([]byte(nil), b...)) {
		t.Fatalf("parseObjectGUID not deterministic")
	}
	// Wrong length → "" so the caller falls back to the DN.
	if got := parseObjectGUID([]byte{1, 2, 3}); got != "" {
		t.Fatalf("parseObjectGUID(short) = %q, want empty", got)
	}
	if got := parseObjectGUID(nil); got != "" {
		t.Fatalf("parseObjectGUID(nil) = %q, want empty", got)
	}
}

// ── (6) boot hydrate — snapshot rebuilt from CH rows ─────────────────────────

func TestHydrateFromRows(t *testing.T) {
	rows := []chstore.LdapGroupRow{
		{UID: "uid-eng", DN: "CN=Eng,OU=Groups,DC=corp,DC=example", CN: "Eng",
			Users: []string{"alice", "bob"}, SyncedAt: 1000},
		{UID: "uid-sre", DN: "CN=Sre,OU=Groups,DC=corp,DC=example", CN: "Sre",
			Users: []string{"bob"}, SyncedAt: 2000},
	}
	e := newTestEngine(&fakeSearcher{}, &fakeStore{hydrate: rows}, baseCfg())
	if err := e.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	snap := e.Snapshot()
	if snap == nil {
		t.Fatalf("snapshot nil after hydrate")
	}
	if snap.Stats.Groups != 2 || snap.Stats.Users != 2 {
		t.Fatalf("stats groups=%d users=%d, want 2/2", snap.Stats.Groups, snap.Stats.Users)
	}
	// bob is in both groups.
	bob := snap.UserGroups["bob"]
	sort.Strings(bob)
	if !reflect.DeepEqual(bob, []string{"uid-eng", "uid-sre"}) {
		t.Fatalf("UserGroups[bob] = %v, want [uid-eng uid-sre]", bob)
	}
	// SyncedAt tracks the newest row.
	if got := snap.SyncedAt.UTC(); !got.Equal(time.Unix(0, 2000).UTC()) {
		t.Fatalf("SyncedAt = %v, want %v", got, time.Unix(0, 2000).UTC())
	}
}

// Empty CH → nil snapshot (never a blank-membership lockout).
func TestHydrateEmptyKeepsNilSnapshot(t *testing.T) {
	e := newTestEngine(&fakeSearcher{}, &fakeStore{}, baseCfg())
	if err := e.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if e.Snapshot() != nil {
		t.Fatalf("empty hydrate produced a non-nil snapshot")
	}
}

// ── groupInScope DN-suffix semantics ─────────────────────────────────────────

func TestGroupInScope(t *testing.T) {
	dn := "CN=Payments,OU=App,OU=Groups,DC=corp,DC=example"
	tests := []struct {
		name             string
		include, exclude []string
		want             bool
	}{
		{"empty include = all", nil, nil, true},
		{"suffix include match", []string{"OU=App,OU=Groups,DC=corp,DC=example"}, nil, true},
		{"include no match", []string{"OU=Infra,OU=Groups,DC=corp,DC=example"}, nil, false},
		{"exclude wins over include", []string{"DC=corp,DC=example"}, []string{"OU=App,OU=Groups,DC=corp,DC=example"}, false},
		{"case-insensitive", []string{"ou=app,ou=groups,dc=corp,dc=example"}, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := groupInScope(dn, tc.include, tc.exclude); got != tc.want {
				t.Fatalf("groupInScope(%v,%v) = %v, want %v", tc.include, tc.exclude, got, tc.want)
			}
		})
	}
}

// Preview must never persist or publish (dry-run contract).
func TestPreviewDoesNotPersistOrPublish(t *testing.T) {
	fs := &fakeSearcher{
		groups: []*goldap.Entry{groupEntry("CN=Eng,OU=Groups,DC=corp,DC=example", "Eng", guid16(1))},
		members: map[string][]*goldap.Entry{
			"CN=Eng,OU=Groups,DC=corp,DC=example": {userEntry("CN=A,OU=Users,DC=corp,DC=example", "alice", "alice@corp.example", "")},
		},
	}
	store := &fakeStore{}
	e := newTestEngine(fs, store, baseCfg())

	res, err := e.Preview(context.Background())
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if res.TotalGroupsInScope != 1 || res.SampledGroups != 1 {
		t.Fatalf("preview scope = %+v", res)
	}
	if len(store.upserts) != 0 {
		t.Fatalf("Preview wrote to the store (%d upserts) — must be a dry-run", len(store.upserts))
	}
	if e.Snapshot() != nil {
		t.Fatalf("Preview published a snapshot — must not touch the pointer")
	}
}

// Sanity: the chain filter embeds the group DN so the fake dispatches
// correctly (guards the membersOfGroup filter shape).
func TestMembersChainFilterShape(t *testing.T) {
	cfg := baseCfg()
	dn := "CN=Eng,OU=Groups,DC=corp,DC=example"
	fs := &fakeSearcher{members: map[string][]*goldap.Entry{dn: {userEntry("CN=A", "a", "", "")}}}
	got, err := membersOfGroup(fs, cfg, dn)
	if err != nil {
		t.Fatalf("membersOfGroup: %v", err)
	}
	if len(got) != 1 || got[0].Sam != "a" {
		t.Fatalf("members = %+v, want one (a)", got)
	}
	if len(fs.pageSizes) != 1 || fs.pageSizes[0] != defaultGroupSyncPageSize {
		t.Fatalf("pageSize propagation = %v", fs.pageSizes)
	}
}
