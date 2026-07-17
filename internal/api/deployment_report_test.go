package api

import (
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
