package chstore

import (
	"reflect"
	"testing"
)

// v0.8.526 — LDAP/AD group sync tombstone diff (audit §12.7). Each sync
// writes the FULL in-scope set (deleted=0) and tombstones (deleted=1)
// every UID that was live before but vanished from the directory this
// round. missingUIDs is the pure heart of that semantic; this table
// pins it so a refactor can't silently stop tombstoning (which would
// leave a removed AD group visible forever after HydrateLdapGroups
// filters deleted=0).
func TestMissingUIDs(t *testing.T) {
	tests := []struct {
		name       string
		prev, next []string
		want       []string
	}{
		{"nothing gone (superset)", []string{"a", "b"}, []string{"a", "b", "c"}, nil},
		{"one gone", []string{"a", "b", "c"}, []string{"a", "c"}, []string{"b"}},
		{"all gone", []string{"a", "b"}, nil, []string{"a", "b"}},
		{"fresh install (no prev)", nil, []string{"a"}, nil},
		{"identical sets", []string{"a", "b"}, []string{"b", "a"}, nil},
		{"dedupe repeated prev", []string{"a", "a", "b"}, []string{"a"}, []string{"b"}},
		{"order preserved over prev", []string{"z", "y", "x"}, []string{"y"}, []string{"z", "x"}},
		{"full rotation", []string{"a", "b"}, []string{"c", "d"}, []string{"a", "b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := missingUIDs(tc.prev, tc.next)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("missingUIDs(%v, %v) = %v, want %v", tc.prev, tc.next, got, tc.want)
			}
		})
	}
}
