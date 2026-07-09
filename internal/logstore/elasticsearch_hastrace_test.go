package logstore

import (
	"encoding/json"
	"strings"
	"testing"
)

// v0.8.406 — trace-only filter: the ES query must gain an exists
// bool/should over the SAME four common trace-field spellings the
// TraceID term lookup fans out to (+ the configured override), and
// must stay absent when the filter is off (an always-on exists clause
// would silently drop every non-traced log from plain searches).

func TestHasTraceAddsExistsOverAllTraceFieldShapes(t *testing.T) {
	s := &ESStore{}
	s.cfg.defaults()
	s.fields = s.cfg.Fields
	raw, err := json.Marshal(s.buildQuery(Filter{HasTrace: true}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	q := string(raw)
	for _, fld := range []string{"trace.id", "TraceId", "trace_id", "traceId"} {
		if !strings.Contains(q, `"exists":{"field":"`+fld+`"}`) {
			t.Errorf("query must carry exists on %q; got %s", fld, q)
		}
	}

	rawOff, err := json.Marshal(s.buildQuery(Filter{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(rawOff), `"exists"`) {
		t.Errorf("hasTrace off must not add an exists clause; got %s", rawOff)
	}
}
