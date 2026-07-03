package logstore

import (
	"net/http"
	"testing"
)

// v0.8.237 — operator-reported 403 (captured by the v0.8.230 error
// panel): on a data-stream cluster the PIT pins concrete .ds-* backing
// indices, and a credential granted read on the stream pattern ("app*")
// is denied direct backing-index reads — the PIT search 403s while the
// Kibana-style pattern query works. Search must flip to plain paging
// and retry instead of erroring /logs. This pins the retry gate + the
// sticky pitDenied flag semantics.
func TestRetryPlainOnPITDenial(t *testing.T) {
	cases := []struct {
		name   string
		usePIT bool
		status int
		want   bool
	}{
		{"403 in PIT mode → retry plain", true, http.StatusForbidden, true},
		{"401 in PIT mode → retry plain", true, http.StatusUnauthorized, true},
		{"403 already plain → no retry (would loop)", false, http.StatusForbidden, false},
		{"500 in PIT mode → real error, no retry", true, http.StatusInternalServerError, false},
		{"404 in PIT mode → index problem, no retry", true, http.StatusNotFound, false},
		{"429 in PIT mode → pressure, no retry", true, http.StatusTooManyRequests, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := retryPlainOnPITDenial(c.usePIT, c.status); got != c.want {
				t.Fatalf("retryPlainOnPITDenial(%v, %d) = %v, want %v", c.usePIT, c.status, got, c.want)
			}
		})
	}
}

func TestPITDeniedFlagSticks(t *testing.T) {
	s := &ESStore{}
	if s.pitDenied.Load() {
		t.Fatal("fresh store must not start denied")
	}
	if !s.pitDenied.CompareAndSwap(false, true) {
		t.Fatal("first denial must win the CAS (logs the switch once)")
	}
	if s.pitDenied.CompareAndSwap(false, true) {
		t.Fatal("second denial must NOT re-CAS (no log spam)")
	}
	if !s.pitDenied.Load() {
		t.Fatal("flag must stay set for the life of the process")
	}
}
