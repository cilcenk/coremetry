package logstore

import (
	"encoding/json"
	"strings"
	"testing"
)

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
