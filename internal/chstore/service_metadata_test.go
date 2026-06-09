package chstore

import "testing"

// TestMergeTeams pins the auto-derive ownership rule (v0.8.95 fill, v0.8.100
// rename-propagation): the deriver owns a field while it's empty OR equals the
// value it last auto-wrote (*_team_auto); a human edit (value != auto) pins it.
// When owned the field tracks the derived value (rename propagates); manual
// edits and the other metadata fields are never touched.
func TestMergeTeams(t *testing.T) {
	cases := []struct {
		name                       string
		md                         ServiceMetadata
		derived                    ServiceTeams
		wantOwner, wantSRE         string
		wantOwnerAuto, wantSREAuto string
		wantChanged                bool
	}{
		{"fill both empty", ServiceMetadata{Service: "s"}, ServiceTeams{"ug", "sy"}, "ug", "sy", "ug", "sy", true},
		{"manual owner (no auto) pinned, fill sre", ServiceMetadata{Service: "s", OwnerTeam: "manual"}, ServiceTeams{"ug", "sy"}, "manual", "sy", "", "sy", true},
		{"manual both pinned, no change", ServiceMetadata{Service: "s", OwnerTeam: "mo", SRETeam: "ms"}, ServiceTeams{"ug", "sy"}, "mo", "ms", "", "", false},
		{"derived empty owner only fills sre", ServiceMetadata{Service: "s"}, ServiceTeams{"", "sy"}, "", "sy", "", "sy", true},
		{"derived both empty, no change", ServiceMetadata{Service: "s"}, ServiceTeams{"", ""}, "", "", "", "", false},
		// v0.8.100 — auto-owned field tracks a rename in the span attrs.
		{"auto-owned updates on rename", ServiceMetadata{Service: "s", OwnerTeam: "old", OwnerTeamAuto: "old", SRETeam: "sold", SRETeamAuto: "sold"}, ServiceTeams{"new", "snew"}, "new", "snew", "new", "snew", true},
		{"auto-owned same value, no change", ServiceMetadata{Service: "s", OwnerTeam: "x", OwnerTeamAuto: "x", SRETeam: "y", SRETeamAuto: "y"}, ServiceTeams{"x", "y"}, "x", "y", "x", "y", false},
		// Human edited owner away from its auto value → pinned, deriver backs off.
		{"manual edit (owner != auto) pins, sre still owned", ServiceMetadata{Service: "s", OwnerTeam: "manual", OwnerTeamAuto: "derivedX", SRETeam: "sy", SRETeamAuto: "sy"}, ServiceTeams{"derivedY", "sy2"}, "manual", "sy2", "derivedX", "sy2", true},
	}
	for _, c := range cases {
		got, changed := mergeTeams(c.md, c.derived)
		if got.OwnerTeam != c.wantOwner || got.SRETeam != c.wantSRE || changed != c.wantChanged ||
			got.OwnerTeamAuto != c.wantOwnerAuto || got.SRETeamAuto != c.wantSREAuto {
			t.Errorf("%s: got owner=%q sre=%q ownerAuto=%q sreAuto=%q changed=%v; want %q/%q/%q/%q/%v",
				c.name, got.OwnerTeam, got.SRETeam, got.OwnerTeamAuto, got.SRETeamAuto, changed,
				c.wantOwner, c.wantSRE, c.wantOwnerAuto, c.wantSREAuto, c.wantChanged)
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
