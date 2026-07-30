package logstore

import (
	"os"
	"strings"
	"testing"
)

// pit_intent_test.go — v0.9.361.
//
// A Point-in-Time exists ONLY to hand a stable cursor back to a pager. The
// retain path has gated on the caller's declared intent since v0.9.286
// (WantCursor), but the OPEN did not: every cursorless read — the service
// Logs tab (which deliberately omits paging, v0.9.286 documents it), the
// trace drawers, span detail, the Drain puller — opened a PIT on external
// Elasticsearch and immediately closed it. Two extra round-trips per read,
// purchased for nothing, on a cluster doing ~10B docs/day. The operator's
// standing constraint is "do NOT create heavy ES query load".
func TestPITOpensOnlyWithCursorIntent(t *testing.T) {
	b, err := os.ReadFile("elasticsearch.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "else if !f.WantCursor {")
	j := strings.Index(src, "else if pid, err := s.openPIT(")
	if i < 0 {
		t.Fatal("the WantCursor gate on the PIT open is missing — every cursorless read pays an open+close round-trip pair")
	}
	if j < 0 || j < i {
		t.Fatal("the WantCursor gate must come BEFORE the openPIT branch, or it gates nothing")
	}
}
