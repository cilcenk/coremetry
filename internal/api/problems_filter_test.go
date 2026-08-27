package api

import (
	"reflect"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.8.290 — owner/SRE team filter for /problems (mirrors the inbox
// filter + the Services page team dropdowns). matchesTeamFilter is
// the pure decision behind the server-side narrowing; this table
// pins every branch so an empty axis never accidentally hides rows
// and a set axis never leaks a mismatched team. Original request:
// operator asked to filter Problems by owner team + SRE team "aynı
// services sayfasında olduğu gibi" (like the Services page).
func TestMatchesTeamFilter(t *testing.T) {
	tests := []struct {
		name                                 string
		rowOwner, rowSRE, wantOwner, wantSRE string
		keep                                 bool
	}{
		// No filter set → everything passes (empty means "all").
		{"no filter keeps all", "payments", "platform", "", "", true},
		{"no filter keeps un-attributed row", "", "", "", "", true},

		// Owner axis only.
		{"owner match", "payments", "platform", "payments", "", true},
		{"owner mismatch", "payments", "platform", "checkout", "", false},
		{"owner filter drops un-attributed row", "", "platform", "payments", "", false},
		{"owner case-insensitive", "Payments", "platform", "payments", "", true},
		{"owner case-insensitive reverse", "payments", "platform", "PAYMENTS", "", true},

		// SRE axis only.
		{"sre match", "payments", "platform", "", "platform", true},
		{"sre mismatch", "payments", "platform", "", "storage", false},
		{"sre filter drops un-attributed row", "payments", "", "", "platform", false},
		{"sre case-insensitive", "payments", "Platform", "", "platform", true},

		// Both axes AND together.
		{"both match", "payments", "platform", "payments", "platform", true},
		{"both set owner mismatch", "checkout", "platform", "payments", "platform", false},
		{"both set sre mismatch", "payments", "storage", "payments", "platform", false},
		{"both set both mismatch", "checkout", "storage", "payments", "platform", false},
		{"both match case-fold both axes", "Payments", "Platform", "payments", "platform", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesTeamFilter(chstore.TeamAliases{}, tt.rowOwner, tt.rowSRE, tt.wantOwner, tt.wantSRE)
			if got != tt.keep {
				t.Errorf("matchesTeamFilter(owner=%q sre=%q want-owner=%q want-sre=%q) = %v, want %v",
					tt.rowOwner, tt.rowSRE, tt.wantOwner, tt.wantSRE, got, tt.keep)
			}
		})
	}
}

// v0.8.310 — the Problems INBOX is server-paginated, so its owner/SRE
// team filter resolves a team pick to member services (service IN (…))
// instead of post-filtering the page. servicesForTeam is that pure
// resolver. The load-bearing distinction: NIL means "no team constraint"
// (unfiltered), a non-nil EMPTY slice means "team set but nothing matches"
// (empty page). Confusing the two would either leak every row or hide
// every row, so the table pins both.
func TestServicesForTeam(t *testing.T) {
	catalog := map[string]chstore.ServiceMetadata{
		"payments-api": {Service: "payments-api", OwnerTeam: "payments", SRETeam: "core-platform-sre"},
		"ledger":       {Service: "ledger", OwnerTeam: "payments", SRETeam: "core-platform-sre"},
		"risk-scoring": {Service: "risk-scoring", OwnerTeam: "risk-engineering", SRETeam: "ml-platform-sre"},
		"web-bff":      {Service: "web-bff", OwnerTeam: "digital-channels", SRETeam: "edge-sre"},
		"orphan":       {Service: "orphan"}, // catalog entry with no team
	}
	tests := []struct {
		name               string
		wantOwner, wantSRE string
		want               []string // nil is meaningful — see doc above
	}{
		{"no axis set → nil (no constraint)", "", "", nil},
		{"owner only", "payments", "", []string{"ledger", "payments-api"}},
		{"sre only", "", "core-platform-sre", []string{"ledger", "payments-api"}},
		{"owner case-insensitive", "PAYMENTS", "", []string{"ledger", "payments-api"}},
		{"both axes AND", "risk-engineering", "ml-platform-sre", []string{"risk-scoring"}},
		{"both axes no overlap → empty (not nil)", "payments", "ml-platform-sre", []string{}},
		{"unknown owner → empty (not nil)", "does-not-exist", "", []string{}},
		{"single service team", "digital-channels", "", []string{"web-bff"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := servicesForTeam(chstore.TeamAliases{}, catalog, tt.wantOwner, tt.wantSRE)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("servicesForTeam(owner=%q sre=%q) = %#v, want %#v",
					tt.wantOwner, tt.wantSRE, got, tt.want)
			}
			// Guard the nil-vs-empty contract explicitly: DeepEqual treats
			// []string(nil) and []string{} as UNequal, but make the intent
			// unmissable for a future editor.
			if (got == nil) != (tt.want == nil) {
				t.Errorf("nil-ness mismatch: got nil=%v, want nil=%v", got == nil, tt.want == nil)
			}
		})
	}
}

// v0.8.387 — env-separation Phase 3: the /inbox merged-list env filter.
// v0.9.1358 — the rule moved to chstore.EnvScopeKeepsRow (one body, SQL
// tested against it in env_members_test.go). What is left to pin HERE is
// the WIRING: that the merged list hands the rule the row's SUBJECT kind
// and not its source kind, and that a db-subject row therefore survives
// an env pick while an unknown service still does not.
//
// SEMPTOM (denetim §2.4): `?env=prod` seçiliyken /inbox'ın db şeridi
// TAMAMEN boşalıyordu — satırın `service` alanı bir DBSubjectID ve o ne
// boş ne de bir env üyesi olabilir.
func TestEnvFilterInboxItems(t *testing.T) {
	members := map[string]bool{"payments": true, "mobile-bff": true}

	items := []InboxItem{
		// Global (service-less) row — log-query monitors are
		// env-unattributable; an env pick must never hide a firing
		// global alert.
		{ID: "global", Kind: "problem", Service: ""},
		{ID: "member", Kind: "problem", Service: "payments", SubjectKind: "service"},
		{ID: "multi-env", Kind: "problem", Service: "mobile-bff", SubjectKind: "service"},
		// Genuinely env-scoped service absent from the map (env-less
		// infra, or simply not in this env) — MUST stay hidden.
		{ID: "unknown-svc", Kind: "problem", Service: "oracle-rac", SubjectKind: "service"},
		// v0.9.1358 — the row this release restores.
		{ID: "db", Kind: "problem", Service: "db:oracle@corebank-scan.prod", SubjectKind: "db"},
		// Exception/anomaly rows carry a real service and an EMPTY
		// subject kind — they must keep the strict membership rule.
		{ID: "exc-member", Kind: "exception", Service: "payments"},
		{ID: "exc-unknown", Kind: "exception", Service: "oracle-rac"},
	}
	// Copy: the filter reuses the caller's backing array.
	in := append([]InboxItem(nil), items...)
	got := envFilterInboxItems(in, members)

	var gotIDs []string
	for _, it := range got {
		gotIDs = append(gotIDs, it.ID)
	}
	want := []string{"global", "member", "multi-env", "db", "exc-member"}
	if !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("env filter kept %v, want %v", gotIDs, want)
	}

	// Empty member set: global + db rows remain, service rows do not.
	in2 := append([]InboxItem(nil), items...)
	got2 := envFilterInboxItems(in2, map[string]bool{})
	var gotIDs2 []string
	for _, it := range got2 {
		gotIDs2 = append(gotIDs2, it.ID)
	}
	if !reflect.DeepEqual(gotIDs2, []string{"global", "db"}) {
		t.Fatalf("empty member set kept %v, want [global db]", gotIDs2)
	}

	// WIRING GATE — the row's SOURCE kind must never be what the rule
	// reads. InboxItem.Kind is "problem" | "exception" | "anomaly";
	// feeding it in place of SubjectKind makes EVERY row miss the db
	// escape, which is precisely the defect v0.9.1358 fixes and which
	// no type would catch (both fields are string).
	dbRow := InboxItem{Kind: "problem", Service: "db:oracle@x", SubjectKind: "db"}
	if !chstore.EnvScopeKeepsRow(dbRow.Service, dbRow.SubjectKind, members) {
		t.Error("db subject must survive when SubjectKind is passed")
	}
	if chstore.EnvScopeKeepsRow(dbRow.Service, dbRow.Kind, members) {
		t.Error("passing InboxItem.Kind (the SOURCE) must NOT satisfy the db escape — " +
			"if this ever passes, the two fields have been conflated again (v0.9.1339 sınıfı)")
	}
}
