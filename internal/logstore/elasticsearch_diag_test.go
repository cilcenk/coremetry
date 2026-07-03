package logstore

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// v0.8.230 — operator-requested ES query-failure visibility: every
// failed query is recorded (console + in-memory ring) so /admin/elastic
// can show the exact request Coremetry sent. These tests pin the ring
// contract: bounded at esErrRingCap, newest-first in Diagnostics(),
// query body truncated at esErrQueryCap, counter cumulative.

func TestRecordQueryErrorRingCapAndOrder(t *testing.T) {
	s := &ESStore{}
	for i := 0; i < esErrRingCap+15; i++ {
		err := s.recordQueryError("search", []string{"app-x"},
			[]byte(fmt.Sprintf(`{"n":%d}`, i)), 500, fmt.Errorf("boom %d", i))
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("recordQueryError must return the error unchanged, got %v", err)
		}
	}
	d := s.Diagnostics()
	if d.QueryErrors != int64(esErrRingCap+15) {
		t.Fatalf("counter = %d, want %d (cumulative, not capped)", d.QueryErrors, esErrRingCap+15)
	}
	if len(d.RecentErrors) != esErrRingCap {
		t.Fatalf("ring len = %d, want cap %d", len(d.RecentErrors), esErrRingCap)
	}
	// Newest-first: entry 0 is the LAST recorded error.
	if want := fmt.Sprintf(`{"n":%d}`, esErrRingCap+14); d.RecentErrors[0].Query != want {
		t.Fatalf("RecentErrors[0].Query = %q, want newest %q", d.RecentErrors[0].Query, want)
	}
	// Oldest retained entry is (total - cap).
	if want := fmt.Sprintf(`{"n":%d}`, 15); d.RecentErrors[esErrRingCap-1].Query != want {
		t.Fatalf("RecentErrors[last].Query = %q, want oldest-retained %q",
			d.RecentErrors[esErrRingCap-1].Query, want)
	}
}

func TestRecordQueryErrorFields(t *testing.T) {
	s := &ESStore{}
	s.recordQueryError("histogram", []string{"app-a", "app-b"},
		[]byte(`{"aggs":{}}`), 429, errors.New("circuit_breaking_exception"))
	d := s.Diagnostics()
	if len(d.RecentErrors) != 1 {
		t.Fatalf("want 1 entry, got %d", len(d.RecentErrors))
	}
	e := d.RecentErrors[0]
	if e.Op != "histogram" || e.Index != "app-a,app-b" || e.Status != 429 ||
		e.Query != `{"aggs":{}}` || e.Error != "circuit_breaking_exception" || e.At == 0 {
		t.Fatalf("entry mismatch: %+v", e)
	}
}

func TestRecordQueryErrorTruncatesBody(t *testing.T) {
	s := &ESStore{}
	huge := strings.Repeat("x", esErrQueryCap*3) // e.g. a wide _msearch ndjson
	s.recordQueryError("msearch count-patterns", nil, []byte(huge), 0, errors.New("dial tcp: refused"))
	e := s.Diagnostics().RecentErrors[0]
	if len(e.Query) > esErrQueryCap+len("…(truncated)") {
		t.Fatalf("query not truncated: len=%d", len(e.Query))
	}
	if !strings.HasSuffix(e.Query, "…(truncated)") {
		t.Fatalf("truncated query must carry the marker, got suffix %q", e.Query[len(e.Query)-20:])
	}
	// Transport error (no HTTP response) records status 0.
	if e.Status != 0 {
		t.Fatalf("transport error status = %d, want 0", e.Status)
	}
}
