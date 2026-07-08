package chstore

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// v0.8.387 — env-separation Phase 3: /problems consumes the global
// ?env= picker. Problems carry no env dimension, so the filter means
// "problems whose SERVICE ran in the selected env" via the 60s-cached
// 1h service→env map (the env twin of clusterMemberServices,
// v0.8.386). These tests pin (a) the map inversion, (b) the pure
// service-scope conjunct, and (c) the resolve-and-apply wrapper's
// soft-fail — all conn-less against a seeded map cache, the
// cluster_narrow_test.go pattern.

func seededEnvStore(m map[string][]string) *Store {
	s := &Store{}
	s.envMapVal = m
	s.envMapFor = time.Hour
	s.envMapAt = time.Now()
	return s
}

func TestEnvMemberServicesInversion(t *testing.T) {
	s := seededEnvStore(map[string][]string{
		"mobile-bff": {"int", "prep", "uat"}, // multi-env — member of each
		"payments":   {"uat"},
		"batch":      {"int"},
		// env-less infra (e.g. shared Oracle RAC) never enters the map:
		// GetServiceEnvMap excludes deploy_env = ''.
	})
	ctx := context.Background()

	cases := []struct {
		env  string
		want []string
	}{
		{"uat", []string{"mobile-bff", "payments"}}, // sorted
		{"int", []string{"batch", "mobile-bff"}},
		{"prep", []string{"mobile-bff"}},
		{"prod", []string{}}, // unknown env → authoritative EMPTY, not error
	}
	for _, tc := range cases {
		got, err := s.EnvMemberServices(ctx, tc.env)
		if err != nil {
			t.Fatalf("env=%s: unexpected error %v", tc.env, err)
		}
		if got == nil {
			t.Fatalf("env=%s: members must be non-nil (empty = authoritative)", tc.env)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("env=%s: got %v want %v", tc.env, got, tc.want)
		}
	}

	// Conn-less cold cache → ERROR (callers soft-fail to unfiltered),
	// never a silent empty set that would blank the triage page.
	bare := &Store{}
	if _, err := bare.EnvMemberServices(ctx, "uat"); err == nil {
		t.Fatal("cold conn-less store must return an error, not an empty set")
	}
}

func TestApplyEnvServiceScope(t *testing.T) {
	cases := []struct {
		name     string
		members  []string
		wantSQL  string
		wantArgs []any
	}{
		{
			name:    "empty members — only global rows survive",
			members: nil,
			wantSQL: "WHERE service = ''",
		},
		{
			name:     "one member",
			members:  []string{"payments"},
			wantSQL:  "WHERE (service = '' OR service IN (?))",
			wantArgs: []any{"payments"},
		},
		{
			name:     "two members, order preserved (already sorted upstream)",
			members:  []string{"mobile-bff", "payments"},
			wantSQL:  "WHERE (service = '' OR service IN (?,?))",
			wantArgs: []any{"mobile-bff", "payments"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var wc whereClause
			applyEnvServiceScope(&wc, tc.members)
			if got := wc.sql(); got != tc.wantSQL {
				t.Fatalf("sql: got %q want %q", got, tc.wantSQL)
			}
			if len(tc.wantArgs) == 0 {
				if len(wc.args) != 0 {
					t.Fatalf("args: got %v want none", wc.args)
				}
				return
			}
			if !reflect.DeepEqual(wc.args, tc.wantArgs) {
				t.Fatalf("args: got %v want %v", wc.args, tc.wantArgs)
			}
		})
	}
}

func TestEnvScopeProblems(t *testing.T) {
	ctx := context.Background()
	s := seededEnvStore(map[string][]string{
		"payments": {"uat"},
	})

	// env unset → no-op.
	var wc whereClause
	wc.add("status = ?", "open")
	s.envScopeProblems(ctx, &wc, "")
	if got := wc.sql(); got != "WHERE status = ?" {
		t.Fatalf("env-less filter must be untouched: %q", got)
	}

	// env set → conjunct ANDed after the existing filters, service=''
	// carve-out included (global log-query problems always show).
	var wc2 whereClause
	wc2.add("status = ?", "open")
	s.envScopeProblems(ctx, &wc2, "uat")
	want := "WHERE status = ? AND (service = '' OR service IN (?))"
	if got := wc2.sql(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if wc2.args[len(wc2.args)-1] != "payments" {
		t.Fatalf("member arg missing: %v", wc2.args)
	}

	// Unknown env → authoritative empty membership: only global rows.
	var wc3 whereClause
	s.envScopeProblems(ctx, &wc3, "prod")
	if got := wc3.sql(); got != "WHERE service = ''" {
		t.Fatalf("unknown env must keep only global rows: %q", got)
	}

	// Cold conn-less map (resolve error) → SOFT-FAIL unfiltered:
	// a transient map blip must never hide firing problems, and
	// list/count both soft-fail the same way so they still agree.
	bare := &Store{}
	var wc4 whereClause
	bare.envScopeProblems(ctx, &wc4, "uat")
	if strings.Contains(wc4.sql(), "service") {
		t.Fatalf("map error must not filter: %q", wc4.sql())
	}
}

// v0.8.389 — operator-reported: feature-branch envs pushed the set
// past the alphabetical LIMIT 50, so "release" never surfaced and
// search found nothing. Pins the enumeration window rule: cheap 1h
// clamp unsearched, 24h reach for explicit searches.
func TestEnvEnumWindow(t *testing.T) {
	to := time.Now()
	deepFrom := to.Add(-72 * time.Hour)

	if got := envEnumWindow(deepFrom, to, ""); !got.Equal(to.Add(-time.Hour)) {
		t.Fatalf("unsearched clamp: got %v, want to-1h", got)
	}
	if got := envEnumWindow(deepFrom, to, "release"); !got.Equal(to.Add(-24*time.Hour)) {
		t.Fatalf("searched clamp: got %v, want to-24h", got)
	}
	// A window narrower than the clamp is respected as-is.
	narrow := to.Add(-10 * time.Minute)
	if got := envEnumWindow(narrow, to, "release"); !got.Equal(narrow) {
		t.Fatalf("narrow window must pass through: got %v", got)
	}
	// Whitespace-only q = no search.
	if got := envEnumWindow(deepFrom, to, "   "); !got.Equal(to.Add(-time.Hour)) {
		t.Fatalf("blank q must keep the 1h clamp: got %v", got)
	}
}
