package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.8.329 — span_links write/read invariants (cross-signal pivot Phase 1b).
// Template: exemplar_otlp_test.go (v0.8.328). Pins:
//   - INSERT column/value POSITIONAL alignment: the named column list in
//     spanLinksInsertSQL and the args spanLinkAppendArgs emits must stay in
//     lockstep — a drift writes trace ids into the wrong column silently
//     (the class the v0.8.186 named-column discipline exists to catch);
//   - nil attr arrays normalise to empty slices (Array columns reject nil);
//   - every span-link read is LIMIT-bounded: default 100, cap 1000.

func TestSpanLinksInsertAlignment(t *testing.T) {
	l := &SpanLinkRow{
		TraceID: "t-own", SpanID: "s-own",
		LinkedTraceID: "t-linked", LinkedSpanID: "s-linked",
		Time:        time.Unix(1, 0).UTC(),
		ServiceName: "svc",
		AttrKeys:    []string{"link.kind"},
		AttrVals:    []string{"follows_from"},
	}
	args := spanLinkAppendArgs(l)

	// Column count from the statement's parenthesised list.
	open := strings.Index(spanLinksInsertSQL, "(")
	if open < 0 || !strings.HasSuffix(spanLinksInsertSQL, ")") {
		t.Fatalf("malformed insert statement: %s", spanLinksInsertSQL)
	}
	cols := strings.Split(spanLinksInsertSQL[open+1:len(spanLinksInsertSQL)-1], ",")
	if len(cols) != len(args) {
		t.Fatalf("POSITIONAL MISALIGNMENT — %d columns vs %d values", len(cols), len(args))
	}

	// Spot-check the load-bearing positions: the two id PAIRS must not swap
	// (owning vs linked is the whole point of the two tables), and the
	// arrays must land on attr_keys/attr_values.
	want := map[string]any{
		"trace_id":        "t-own",
		"span_id":         "s-own",
		"linked_trace_id": "t-linked",
		"linked_span_id":  "s-linked",
		"service_name":    "svc",
	}
	for i, c := range cols {
		name := strings.TrimSpace(c)
		if wv, ok := want[name]; ok {
			if got, ok := args[i].(string); !ok || got != wv {
				t.Errorf("column %q at position %d bound to %v, want %v", name, i, args[i], wv)
			}
		}
		if name == "attr_keys" {
			if ks, ok := args[i].([]string); !ok || len(ks) != 1 || ks[0] != "link.kind" {
				t.Errorf("attr_keys bound to %v", args[i])
			}
		}
		if name == "attr_values" {
			if vs, ok := args[i].([]string); !ok || len(vs) != 1 || vs[0] != "follows_from" {
				t.Errorf("attr_values bound to %v", args[i])
			}
		}
	}
}

// Array columns reject nil — a link without attributes (the common case) must
// still bind empty []string, never nil.
func TestSpanLinksInsertNilAttrsNormalised(t *testing.T) {
	args := spanLinkAppendArgs(&SpanLinkRow{TraceID: "t", LinkedTraceID: "lt"})
	for i, a := range args {
		if s, ok := a.([]string); ok && s == nil {
			t.Fatalf("arg %d is a nil []string — Array column bind would fail", i)
		}
	}
}

// clampSpanLinkLimit bounds every span-link read (house rule: LIMIT on every
// raw-table query). Default 100, cap 1000 — same rungs as the exemplar reads.
func TestClampSpanLinkLimit(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 100}, {-5, 100}, {1, 1}, {100, 100}, {999, 999}, {1000, 1000}, {5000, 1000},
	}
	for _, c := range cases {
		if got := clampSpanLinkLimit(c.in); got != c.want {
			t.Errorf("clampSpanLinkLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
