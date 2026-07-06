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
		name                               string
		rowOwner, rowSRE, wantOwner, wantSRE string
		keep                               bool
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
			got := matchesTeamFilter(tt.rowOwner, tt.rowSRE, tt.wantOwner, tt.wantSRE)
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
		name             string
		wantOwner, wantSRE string
		want             []string // nil is meaningful — see doc above
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
			got := servicesForTeam(catalog, tt.wantOwner, tt.wantSRE)
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
