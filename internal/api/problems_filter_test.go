package api

import "testing"

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
