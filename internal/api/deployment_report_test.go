package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestFilterOpenProblemsSince_ExcludesStartedBeforeDeploy(t *testing.T) {
	sinceNs := int64(1000)
	snapshot := map[string]*chstore.Problem{
		"rule1|svcA": {ID: "p1", Service: "svcA", StartedAt: 500}, // before deploy
	}
	got := filterOpenProblemsSince(snapshot, sinceNs)
	if len(got) != 0 {
		t.Fatalf("expected 0 problems (started before deploy), got %d", len(got))
	}
}

func TestFilterOpenProblemsSince_IncludesStartedAfterDeploy(t *testing.T) {
	sinceNs := int64(1000)
	snapshot := map[string]*chstore.Problem{
		"rule1|svcA": {ID: "p1", Service: "svcA", StartedAt: 1500}, // after deploy
	}
	got := filterOpenProblemsSince(snapshot, sinceNs)
	if len(got) != 1 || got[0].ID != "p1" {
		t.Fatalf("expected 1 problem p1, got %+v", got)
	}
}

func TestFilterOpenProblemsSince_BoundaryIsInclusive(t *testing.T) {
	sinceNs := int64(1000)
	snapshot := map[string]*chstore.Problem{
		"rule1|svcA": {ID: "p1", Service: "svcA", StartedAt: 1000}, // exactly at deploy
	}
	got := filterOpenProblemsSince(snapshot, sinceNs)
	if len(got) != 1 {
		t.Fatalf("expected StartedAt == since to be included (inclusive boundary), got %d", len(got))
	}
}

func TestFilterOpenProblemsSince_SortedByStartedAtDesc(t *testing.T) {
	sinceNs := int64(1000)
	snapshot := map[string]*chstore.Problem{
		"rule1|svcA": {ID: "older", Service: "svcA", StartedAt: 1500},
		"rule2|svcA": {ID: "newer", Service: "svcA", StartedAt: 2500},
	}
	got := filterOpenProblemsSince(snapshot, sinceNs)
	if len(got) != 2 || got[0].ID != "newer" || got[1].ID != "older" {
		t.Fatalf("expected [newer, older] order, got %+v", got)
	}
}

func TestIntersectServices_NilTeamSvcsPassesThroughUnfiltered(t *testing.T) {
	svcOrder := []string{"svcA", "svcB"}
	got := intersectServices(svcOrder, nil)
	if len(got) != 2 {
		t.Fatalf("expected the unfiltered list when no team is selected, got %+v", got)
	}
}

func TestIntersectServices_EmptyTeamSvcsCollapsesToEmpty(t *testing.T) {
	svcOrder := []string{"svcA", "svcB"}
	got := intersectServices(svcOrder, []string{}) // team selected, zero member services
	if len(got) != 0 {
		t.Fatalf("expected a team with no member services to collapse the result to empty, got %+v", got)
	}
}

func TestIntersectServices_KeepsOnlyMatchingPreservesOrder(t *testing.T) {
	svcOrder := []string{"svcC", "svcA", "svcB"}
	got := intersectServices(svcOrder, []string{"svcA", "svcB"})
	if len(got) != 2 || got[0] != "svcA" || got[1] != "svcB" {
		t.Fatalf("expected [svcA, svcB] preserving svcOrder's order, got %+v", got)
	}
}

func TestRedComparisonWindow_SymmetricDuration(t *testing.T) {
	// Deploy was 10 minutes ago.
	nowNs := int64(20 * time.Minute)
	sinceNs := int64(10 * time.Minute)
	beforeFrom, beforeTo, afterFrom, afterTo := redComparisonWindow(sinceNs, nowNs)

	afterDur := afterTo.Sub(afterFrom)
	beforeDur := beforeTo.Sub(beforeFrom)
	if afterDur != beforeDur {
		t.Fatalf("expected symmetric windows, before=%s after=%s", beforeDur, afterDur)
	}
	wantAfterDur := 10 * time.Minute
	if afterDur != wantAfterDur {
		t.Fatalf("expected after-window duration %s, got %s", wantAfterDur, afterDur)
	}
	if !afterTo.Equal(time.Unix(0, nowNs)) {
		t.Fatalf("expected afterTo == now, got %s", afterTo)
	}
	if !beforeTo.Equal(time.Unix(0, sinceNs)) {
		t.Fatalf("expected beforeTo == since (the deploy boundary), got %s", beforeTo)
	}
}

func TestRedComparisonWindow_DeployJustHappened(t *testing.T) {
	// since == now: after-window has zero duration, before-window mirrors it (also zero).
	nowNs := int64(5000)
	sinceNs := int64(5000)
	beforeFrom, beforeTo, afterFrom, afterTo := redComparisonWindow(sinceNs, nowNs)
	if !afterFrom.Equal(afterTo) {
		t.Fatalf("expected zero-width after window when since==now, got %s..%s", afterFrom, afterTo)
	}
	if !beforeFrom.Equal(beforeTo) {
		t.Fatalf("expected zero-width before window when since==now, got %s..%s", beforeFrom, beforeTo)
	}
}

func TestNonNilSlice_NilBecomesEmpty(t *testing.T) {
	var s []chstore.Problem
	got := nonNilSlice(s)
	if got == nil {
		t.Fatal("expected a non-nil empty slice, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected an empty slice, got %d items", len(got))
	}
}

func TestNonNilSlice_PreservesNonNil(t *testing.T) {
	s := []chstore.Problem{{ID: "p1"}}
	got := nonNilSlice(s)
	if len(got) != 1 || got[0].ID != "p1" {
		t.Fatalf("expected the original slice preserved, got %+v", got)
	}
}

// Operator-reported: a service with no anomalies/new-errors serialized
// those fields as JSON `null` (Go nil-slice default), and the frontend's
// `s.anomalies.map(...)` threw "TypeError: Cannot read properties of
// null (reading 'map')" on exactly that response shape. Locks the fix:
// every array field on ServiceReportSection must marshal as `[]`.
func TestServiceReportSection_EmptySlicesMarshalAsArrayNotNull(t *testing.T) {
	sec := ServiceReportSection{
		Service:   "checkout-service",
		Health:    "red",
		Problems:  nonNilSlice[chstore.Problem](nil),
		Anomalies: nonNilSlice[chstore.AnomalyEvent](nil),
		NewErrors: nonNilSlice[chstore.ExceptionGroup](nil),
	}
	b, err := json.Marshal(sec)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	got := string(b)
	for _, want := range []string{`"problems":[]`, `"anomalies":[]`, `"newErrors":[]`} {
		if !strings.Contains(got, want) {
			t.Fatalf("regression: expected %s in JSON, got %s", want, got)
		}
	}
}
