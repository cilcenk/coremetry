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
