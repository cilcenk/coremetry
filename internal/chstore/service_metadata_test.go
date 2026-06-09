package chstore

import "testing"

// TestMergeTeams pins the auto-derive merge rule (v0.8.95): fill owner/sre team
// ONLY when the catalog field is empty (manual edits win), and never touch the
// other metadata fields.
func TestMergeTeams(t *testing.T) {
	cases := []struct {
		name               string
		md                 ServiceMetadata
		derived            ServiceTeams
		wantOwner, wantSRE string
		wantChanged        bool
	}{
		{"fill both empty", ServiceMetadata{Service: "s"}, ServiceTeams{"ug", "sy"}, "ug", "sy", true},
		{"manual owner wins, fill sre", ServiceMetadata{Service: "s", OwnerTeam: "manual"}, ServiceTeams{"ug", "sy"}, "manual", "sy", true},
		{"manual both wins, no change", ServiceMetadata{Service: "s", OwnerTeam: "mo", SRETeam: "ms"}, ServiceTeams{"ug", "sy"}, "mo", "ms", false},
		{"derived empty owner only fills sre", ServiceMetadata{Service: "s"}, ServiceTeams{"", "sy"}, "", "sy", true},
		{"derived both empty, no change", ServiceMetadata{Service: "s"}, ServiceTeams{"", ""}, "", "", false},
	}
	for _, c := range cases {
		got, changed := mergeTeams(c.md, c.derived)
		if got.OwnerTeam != c.wantOwner || got.SRETeam != c.wantSRE || changed != c.wantChanged {
			t.Errorf("%s: got owner=%q sre=%q changed=%v; want %q/%q/%v",
				c.name, got.OwnerTeam, got.SRETeam, changed, c.wantOwner, c.wantSRE, c.wantChanged)
		}
	}

	// Non-team fields must survive the merge (UpsertServiceMetadata is a
	// full-row replace, so a dropped field would clobber a manual edit).
	md := ServiceMetadata{Service: "s", Description: "keep", Repository: "repo", ChatChannel: "chan", RunbookURL: "rb"}
	got, _ := mergeTeams(md, ServiceTeams{"ug", "sy"})
	if got.Description != "keep" || got.Repository != "repo" || got.ChatChannel != "chan" || got.RunbookURL != "rb" {
		t.Errorf("mergeTeams must preserve non-team fields, got %+v", got)
	}
}
