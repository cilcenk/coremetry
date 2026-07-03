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

	// v0.8.239 UPDATE — the exactness contract evolved: bare-field terms
	// are now ALLOWED (ECS templates type paths keyword directly, no
	// .keyword sub-field — the service-detail Logs tab returned 0 there)
	// but ONLY behind a must_not exists(<field>.keyword) guard, which
	// keeps the analyzed-field branch dead on dynamic mappings. So the
	// invariant this test pins is: every bare term rides with its
	// exists-guard; the .keyword term stays primary.
	cases := []struct {
		name      string
		keywordTerm string
		guard       string
	}{
		{
			name:        "service filter — keyword term + guarded bare term",
			keywordTerm: `"service.name.keyword":"java-demo"`,
			guard:       `"must_not":[{"exists":{"field":"service.name.keyword"}}]`,
		},
		{
			name:        "cluster filter — keyword term + guarded bare term",
			keywordTerm: `"resource_attributes.k8s.cluster.name.keyword":"prod-eu"`,
			guard:       `"must_not":[{"exists":{"field":"resource_attributes.k8s.cluster.name.keyword"}}]`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(q, c.keywordTerm) {
				t.Errorf("query missing keyword term %q\n%s", c.keywordTerm, q)
			}
			if !strings.Contains(q, c.guard) {
				t.Errorf("bare-field term must carry the exists guard %q\n%s", c.guard, q)
			}
		})
	}
	// The v0.7.16 false-positive guarantee, restated: a bare analyzed-
	// field term may ONLY appear inside a guarded bool. Count naked
	// occurrences by requiring every bare term to be immediately inside
	// the guarded shape emitted by exactTermsBothShapes.
	if strings.Count(q, `"service.name":"java-demo"`) != strings.Count(q, `{"bool":{"must":[{"term":{"service.name":"java-demo"}}]`) {
		t.Errorf("unguarded bare service term found:\n%s", q)
	}
}
