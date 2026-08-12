package chstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// v0.8.386 — operator-reported: /api/services 500'd on SOME of prod's
// 18 clusters. On external-Distributed spans without the promoted
// cluster column, the cluster conjunct is a per-row res/attr derive
// over the whole window; at prod volume it blows the exec-time/memory
// guards, and cache warmth made it look cluster-specific. The fix
// narrows the scan to the cached cluster's member services FIRST —
// service_name is the PK prefix — while keeping the derive conjunct
// for exact per-cluster numbers. These tests pin the narrowing rules
// on both raw paths via a seeded map cache (conn-less Store).
func seededStore(m map[string][]string) *Store {
	s := &Store{}
	s.clusterMapVal = m
	s.clusterMapFor = time.Hour
	s.clusterMapAt = time.Now()
	return s
}

func TestServicesListWhereClusterNarrowing(t *testing.T) {
	s := seededStore(map[string][]string{
		"checkout": {"eu-west", "eu-central"},
		"payments": {"eu-west"},
		"batch":    {"us-east"},
	})
	ctx := context.Background()
	now := time.Now()

	wc := s.servicesListWhere(ctx, 0, now.Add(-time.Hour), now, "", nil, "eu-west", "")
	sql := wc.sql()
	if !strings.Contains(sql, "service_name IN (?,?)") {
		t.Fatalf("expected 2-member narrowing\n%s", sql)
	}
	// Sorted membership → deterministic arg order.
	found := 0
	for i, a := range wc.args {
		if a == "checkout" || a == "payments" {
			found++
			if a == "payments" && i > 0 && wc.args[i-1] != "checkout" {
				t.Fatalf("members not sorted: %v", wc.args)
			}
		}
	}
	if found != 2 {
		t.Fatalf("member args missing: %v", wc.args)
	}
	// The exactness conjunct stays.
	if !strings.Contains(sql, "= ?") || wc.args[len(wc.args)-1] != "eu-west" {
		t.Fatalf("cluster conjunct lost\n%s\nargs=%v", sql, wc.args)
	}

	// Unknown cluster → NO narrowing (never an empty page), conjunct only.
	wc2 := s.servicesListWhere(ctx, 0, now.Add(-time.Hour), now, "", nil, "ap-south", "")
	if strings.Contains(wc2.sql(), "service_name IN") {
		t.Fatalf("unknown cluster must not narrow\n%s", wc2.sql())
	}

	// Explicit serviceIn wins — no double narrowing.
	wc3 := s.servicesListWhere(ctx, 0, now.Add(-time.Hour), now, "", []string{"checkout"}, "eu-west", "")
	if strings.Count(wc3.sql(), "service_name IN") != 1 {
		t.Fatalf("serviceIn + cluster must produce exactly one IN\n%s", wc3.sql())
	}

	// Conn-less cache miss → graceful no-narrowing (no panic).
	bare := &Store{}
	wc4 := bare.servicesListWhere(ctx, 0, now.Add(-time.Hour), now, "", nil, "eu-west", "")
	if strings.Contains(wc4.sql(), "service_name IN") {
		t.Fatalf("cold conn-less map must not narrow\n%s", wc4.sql())
	}
}

func TestEndpointsRawFiltersClusterNarrowing(t *testing.T) {
	s := seededStore(map[string][]string{"checkout": {"eu-west"}})
	sql, args := s.endpointsRawFilters(EndpointsQuery{Cluster: "eu-west"}, "path")
	if !strings.Contains(sql, "service_name IN (?)") {
		t.Fatalf("expected narrowing\n%s", sql)
	}
	if args[0] != "checkout" || args[len(args)-1] != "eu-west" {
		t.Fatalf("arg order broken: %v", args)
	}
	// Service-scoped query skips the narrowing.
	sql2, _ := s.endpointsRawFilters(EndpointsQuery{Cluster: "eu-west", Service: "checkout"}, "path")
	if strings.Contains(sql2, "service_name IN") {
		t.Fatalf("service-scoped must not narrow\n%s", sql2)
	}
}

// v0.8.392 — operator-reported (prod, 18 clusters): /api/services with
// a cluster filter 500'd ~1.3s in — too fast for the 20s execution
// cap, the signature of ClickHouse's memory guard (code 241) killing
// the GROUP BY hash table. The heavy fallback scans now spill to disk
// instead of dying. Values pinned: 2 GiB spill threshold, 8 GiB
// per-query ceiling (the topology writer's prod-proven pair).
//
// v0.9.975 — those two numbers are now a REQUEST, not a guarantee: on a
// node whose own max_server_memory_usage is smaller than 8 GiB the
// ceiling never fired at all. The requested bytes are unchanged
// wherever the server has room (the fail-open case below, which is what
// every prod cluster with real RAM sees); on a constrained node the
// clause carries numbers that can actually fire.
func TestHeavyScanSpill(t *testing.T) {
	// Fail-open Store (server ceiling unread) — byte-identical to
	// v0.8.392.
	var s Store
	got := s.heavyScanSpill()
	for _, want := range []string{
		"max_bytes_before_external_group_by = 2000000000",
		"max_memory_usage = 8000000000",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("heavyScanSpill missing %q\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, ",") {
		t.Error("heavyScanSpill must start with a comma — it appends to an existing SETTINGS list")
	}

	// Constrained node (the measured local 2.8 GiB ceiling): both
	// numbers come down, and the spill stays BELOW the cap so it can
	// still fire.
	s.memPlan = resolveQueryMemory(0, 0, 0, 3_006_477_107, 0.6)
	clamped := s.heavyScanSpill()
	for _, want := range []string{
		"max_bytes_before_external_group_by = 751619276",
		"max_memory_usage = 1803886264",
	} {
		if !strings.Contains(clamped, want) {
			t.Errorf("clamped heavyScanSpill missing %q\n%s", want, clamped)
		}
	}
	if strings.Contains(clamped, "8000000000") {
		t.Errorf("the 8 GiB ceiling survived onto a 2.8 GiB node — it can never fire\n%s", clamped)
	}
}
