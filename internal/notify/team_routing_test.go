package notify

import (
	"reflect"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// team_routing_test.go — v0.8.429. Pins the pure halves of the
// problem-open → team-mail path: recipient resolution (case-
// insensitive catalog team lookup, comma-split, cross-team dedup) and
// the severity floor. The send/dedup plumbing rides the existing
// sendOne/notification_log paths, exercised by their own tests.
func TestResolveTeamRecipients(t *testing.T) {
	contacts := chstore.TeamContacts{Contacts: map[string]string{
		"AvengersUG": "ug@bank.example",
		"avengersy":  "sy@bank.example, oncall@bank.example",
		"SharedTeam": "same@bank.example",
		"emptyteam":  "   ",
	}}

	md := func(owner, sre string) *chstore.ServiceMetadata {
		return &chstore.ServiceMetadata{OwnerTeam: owner, SRETeam: sre}
	}

	tests := []struct {
		name string
		md   *chstore.ServiceMetadata
		want []string
	}{
		{"both teams resolve, comma-split on SRE",
			md("AvengersUG", "AvengerSY"),
			[]string{"ug@bank.example", "sy@bank.example", "oncall@bank.example"}},
		{"case-insensitive team lookup (v0.8.330 mixed-casing lesson)",
			md("AVENGERSUG", ""),
			[]string{"ug@bank.example"}},
		{"same DL on both teams dedupes to one",
			md("SharedTeam", "sharedteam"),
			[]string{"same@bank.example"}},
		{"team without a configured address is skipped silently",
			md("UnknownTeam", "AvengersUG"),
			[]string{"ug@bank.example"}},
		{"whitespace-only contact value is no address",
			md("emptyteam", ""),
			nil},
		{"no catalog row → nil",
			nil,
			nil},
		{"catalog row with empty teams → nil",
			md("", ""),
			nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveTeamRecipients(tc.md, contacts)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTeamContactsSeverityFloor(t *testing.T) {
	tests := []struct {
		min  string
		sev  string
		want bool
	}{
		{"", "info", false},        // default floor is warning
		{"", "warning", true},
		{"", "critical", true},
		{"info", "info", true},
		{"critical", "warning", false},
		{"critical", "critical", true},
		{"warning", "unknown-sev", true}, // unknown ranks as warning
	}
	for _, tc := range tests {
		got := chstore.TeamContacts{MinSeverity: tc.min}.SeverityAllows(tc.sev)
		if got != tc.want {
			t.Errorf("min=%q sev=%q: got %v, want %v", tc.min, tc.sev, got, tc.want)
		}
	}
}
