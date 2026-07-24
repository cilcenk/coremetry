package auth

import (
	"reflect"
	"testing"
)

// v0.9.209 — the custom-role page catalogue had drifted from the sidebar, so
// /endpoints, /clusters and /watchers could not be granted to a custom role at
// all, and two catalogue IDs (/topology, /admin/stats) named routes the
// AppShell guard never matches: a role holding them was redirected straight
// off the page it was supposed to unlock. normalisePages heals the stored
// values; these tests pin the remap and the de-dup/sort contract around it.
func TestNormalisePagesRemapsLegacyIDs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "retired /topology heals to /service-map",
			in:   []string{"/topology"},
			want: []string{"/service-map"},
		},
		{
			name: "pre-v0.8 /admin/stats heals to /system",
			in:   []string{"/admin/stats"},
			want: []string{"/system"},
		},
		{
			name: "legacy and current form collapse to one entry",
			in:   []string{"/topology", "/service-map"},
			want: []string{"/service-map"},
		},
		{
			name: "remap survives surrounding whitespace",
			in:   []string{"  /topology  "},
			want: []string{"/service-map"},
		},
		{
			name: "newly grantable pages pass through untouched",
			in:   []string{"/watchers", "/endpoints", "/clusters"},
			want: []string{"/clusters", "/endpoints", "/watchers"},
		},
		{
			name: "blanks dropped, result sorted, duplicates removed",
			in:   []string{"/logs", "", "  ", "/logs", "/inbox"},
			want: []string{"/inbox", "/logs"},
		},
		{
			name: "empty input stays empty, never nil-panics",
			in:   []string{},
			want: []string{},
		},
		{
			name: "a page whose prefix looks legacy is NOT remapped",
			in:   []string{"/topology-custom"},
			want: []string{"/topology-custom"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalisePages(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("normalisePages(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A remap target must itself be stable — otherwise a second normalise pass
// would keep rewriting the value and the healed role never settles.
func TestNormalisePagesIsIdempotent(t *testing.T) {
	in := []string{"/topology", "/admin/stats", "/endpoints"}
	once := normalisePages(in)
	twice := normalisePages(once)
	if !reflect.DeepEqual(once, twice) {
		t.Fatalf("normalisePages not idempotent: %q then %q", once, twice)
	}
	for _, p := range once {
		if to, ok := legacyPageIDs[p]; ok {
			t.Fatalf("remap target %q is itself a legacy ID (→ %q)", p, to)
		}
	}
}
