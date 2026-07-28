package chstore

import (
	"strings"
	"testing"
)

// incident_getbyid_test.go — v0.9.332.
//
// GetIncident used to call ListIncidents(Limit: 1000) and linear-scan the
// result in Go. Any incident outside the newest 1000 was invisible, and the
// function returned (nil, nil) — the same answer it gives for "does not
// exist". Locally the table held 7,539 incidents, so ~87% of them could not
// be fetched at all.
//
// Every single-incident read failed quietly as a result: the auto-resolve
// cascade skipped old incidents (`inc == nil → continue`), Acknowledge /
// Resolve / Update answered 404, and the detail page opened empty.
//
// The store needs a live ClickHouse, so what is pinned here is the SHAPE:
// the filter carries an id and the lookup goes through it.
func TestGetIncidentQueriesByID(t *testing.T) {
	src := mustReadSource(t, "incident.go")

	if strings.Contains(src, "ListIncidents(ctx, IncidentFilter{Limit: 1000})") {
		t.Error("GetIncident is paging the newest 1000 and scanning in Go — anything older is invisible")
	}
	if !strings.Contains(src, "ListIncidents(ctx, IncidentFilter{ID: id, Limit: 1})") {
		t.Error("GetIncident must ask the database for the row it wants")
	}
	if !strings.Contains(src, `wc.add("id = ?", f.ID)`) {
		t.Error("IncidentFilter.ID must reach the WHERE clause, or the narrow is decorative")
	}
}

// The rollup must EMIT every unresolved incident, including ones with nothing
// attached. Both joins were INNER, so an unattached incident produced no row
// and the cascade could never consider it — the mechanism that let 26 of 32
// open incidents survive for days.
func TestOpenIncidentRollupsSeesUnattached(t *testing.T) {
	src := mustReadSource(t, "incident.go")

	if strings.Contains(src, "INNER JOIN incident_problems") || strings.Contains(src, "INNER JOIN problems") {
		t.Error("INNER JOIN drops incidents with nothing attached — they never reach the cascade")
	}
	if !strings.Contains(src, "LEFT JOIN incident_problems") || !strings.Contains(src, "LEFT JOIN problems") {
		t.Error("the rollup must LEFT JOIN so unattached incidents still emit a row")
	}
	// Acknowledged incidents must be in scope too: picking one up should not
	// make it immortal. Matches inboxKeepsIncident and the sidebar badge.
	if !strings.Contains(src, "WHERE i.status != 'resolved'") {
		t.Error("the cascade must consider acknowledged incidents, not just open ones")
	}
}
