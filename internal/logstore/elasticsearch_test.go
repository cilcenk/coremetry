package logstore

import (
	"encoding/json"
	"strings"
	"testing"
)

// v0.8.228 — operator wanted Coremetry to query their ES the way they do:
// app-* indices with trace_id / @timestamp / message field paths. The field
// map is now operator-configurable via COREMETRY_ES_FIELD_*. This guards the
// two halves of that contract: (1) an unset/empty map falls back to the
// ECS-ish defaults, and (2) an explicit override wins and is the path the
// query actually uses — so a future refactor can't silently drop an operator
// override and send the wrong field to ES.
func TestESFieldMapDefaultsAndOverrides(t *testing.T) {
	t.Run("empty members fall back to defaults", func(t *testing.T) {
		cfg := ESConfig{}
		cfg.defaults()
		want := map[string]string{
			"Index":      "app-*",
			"Timestamp":  "@timestamp",
			"TraceID":    "trace.id",
			"SpanID":     "span.id",
			"Service":    "service.name",
			"Body":       "message",
			"SeverityTx": "log.level",
		}
		got := map[string]string{
			"Index":      cfg.Index,
			"Timestamp":  cfg.Fields.Timestamp,
			"TraceID":    cfg.Fields.TraceID,
			"SpanID":     cfg.Fields.SpanID,
			"Service":    cfg.Fields.Service,
			"Body":       cfg.Fields.Body,
			"SeverityTx": cfg.Fields.SeverityTx,
		}
		for k, w := range want {
			if got[k] != w {
				t.Errorf("default %s: got %q, want %q", k, got[k], w)
			}
		}
		// SeverityNo has no default (skipped when absent) — must stay empty.
		if cfg.Fields.SeverityNo != "" {
			t.Errorf("SeverityNo default: got %q, want empty", cfg.Fields.SeverityNo)
		}
	})

	t.Run("explicit overrides survive defaults() and reach the query", func(t *testing.T) {
		// The operator's mapping: app-creditcard family, message body,
		// @timestamp, and a non-ECS service path.
		cfg := ESConfig{
			Index: "app-*",
			Fields: ESFieldMap{
				Timestamp: "@timestamp",
				TraceID:   "trace_id",
				Service:   "app.name",
				Body:      "message",
			},
		}
		cfg.defaults()
		if cfg.Fields.TraceID != "trace_id" {
			t.Errorf("override TraceID clobbered: got %q, want trace_id", cfg.Fields.TraceID)
		}
		if cfg.Fields.Service != "app.name" {
			t.Errorf("override Service clobbered: got %q, want app.name", cfg.Fields.Service)
		}
		// Unset member still defaults alongside the overrides.
		if cfg.Fields.SpanID != "span.id" {
			t.Errorf("unset SpanID should default: got %q", cfg.Fields.SpanID)
		}
		// The overridden service path is what the built query targets
		// (on the .keyword sub-field, per TestBuildQueryUsesKeywordForExactFilters).
		s := &ESStore{fields: cfg.Fields, cfg: cfg}
		raw, err := json.Marshal(s.buildQuery(Filter{Service: "creditcard"}))
		if err != nil {
			t.Fatalf("marshal query: %v", err)
		}
		if !strings.Contains(string(raw), "app.name") {
			t.Errorf("built query should target overridden service field app.name; got %s", raw)
		}
	})
}

// v0.7.16 regression — the ES Search filter matched the ANALYZED text field
// (service.name) with a term query. ES dynamic-maps service.name as
// text+keyword, so the standard analyzer tokenizes a hyphenated value like
// "java-demo" into ["java","demo"] and a term for the literal "java-demo"
// matches NOTHING → the service filter silently returned 0 on the ES backend
// (the operator's primary logstore) once we read collector-written,
// dynamically-mapped indices. The fix targets the exact-value `.keyword`
// sub-field, matching the histogram/pattern aggs already in this file. The
// cluster filter had the same latent bug.
func TestBuildQueryUsesKeywordForExactFilters(t *testing.T) {
	cfg := ESConfig{}
	cfg.defaults()
	s := &ESStore{fields: cfg.Fields, cfg: cfg}

	raw, err := json.Marshal(s.buildQuery(Filter{Service: "java-demo", Cluster: "prod-eu"}))
	if err != nil {
		t.Fatalf("marshal query: %v", err)
	}
	q := string(raw)

	cases := []struct {
		name      string
		mustHave  string // substring that MUST be present
		mustNotHave string // substring that MUST be absent ("" = skip)
	}{
		{
			name:        "service filter targets the keyword sub-field",
			mustHave:    `"service.name.keyword":"java-demo"`,
			mustNotHave: `"service.name":"java-demo"`, // the analyzed-field term that returned 0
		},
		{
			name:     "cluster filter targets the keyword sub-field",
			mustHave:  `"resource_attributes.k8s.cluster.name.keyword":"prod-eu"`,
			mustNotHave: `"resource_attributes.k8s.cluster.name":"prod-eu"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(q, c.mustHave) {
				t.Errorf("query missing %q\n%s", c.mustHave, q)
			}
			if c.mustNotHave != "" && strings.Contains(q, c.mustNotHave) {
				t.Errorf("query must NOT term-match the analyzed field %q\n%s", c.mustNotHave, q)
			}
		})
	}
}
