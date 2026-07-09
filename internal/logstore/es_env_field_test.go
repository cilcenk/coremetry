package logstore

import (
	"encoding/json"
	"strings"
	"testing"
)

// v0.8.400 — env-separation Phase 4: the ES ?env= filter's field
// SELF-DISCOVERY (es_env_field.go). Table-pins:
//
//   - the candidate list (order = verdict priority; new semconv
//     spelling before legacy at equal nesting, mirroring the v0.8.379
//     ingest fallback and topoEnvChainSQL v0.8.380);
//   - resolveEnvFieldFromCaps — first EXISTING keyword-capable
//     candidate wins; text-only candidates are SKIPPED (a term on an
//     analyzed field silently matches nothing — the pivot-audit
//     silent-killer class); keyword capability counts through the
//     .keyword multi-field too;
//   - buildQuery's env clause shape — exactTermsBothShapes on the
//     resolved field (v0.8.239 ECS-vs-dynamic parity with the
//     service/cluster filters), and NO clause when the field is
//     unresolved (honesty over a fake empty result).

func TestEnvFieldCandidates_OrderAndSet(t *testing.T) {
	want := []string{
		"resource.deployment.environment.name",
		"resource.deployment.environment",
		"deployment.environment.name",
		"deployment.environment",
		"resource.attributes.deployment.environment.name",
		"labels.deployment_environment",
		"env",
		"environment",
	}
	got := envFieldCandidates()
	if len(got) != len(want) {
		t.Fatalf("candidate count = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate[%d] = %q, want %q (order IS the verdict priority)", i, got[i], want[i])
		}
	}
}

func TestResolveEnvFieldFromCaps(t *testing.T) {
	kw := traceFieldCap{Types: []string{"keyword"}, Searchable: true, Aggregatable: true}
	txt := traceFieldCap{Types: []string{"text"}, Searchable: true, Aggregatable: false}
	cands := envFieldCandidates()

	cases := []struct {
		name     string
		caps     map[string]traceFieldCap
		wantName string
		wantOK   bool
	}{
		{
			name:     "all absent → unresolved",
			caps:     map[string]traceFieldCap{},
			wantName: "", wantOK: false,
		},
		{
			name:     "bare keyword mapping resolves",
			caps:     map[string]traceFieldCap{"deployment.environment.name": kw},
			wantName: "deployment.environment.name", wantOK: true,
		},
		{
			name: "text-only field is SKIPPED (term would match nothing)",
			caps: map[string]traceFieldCap{"deployment.environment.name": txt},
			wantName: "", wantOK: false,
		},
		{
			name: "text + .keyword multi-field resolves via the subfield",
			caps: map[string]traceFieldCap{
				"deployment.environment.name":         txt,
				"deployment.environment.name.keyword": kw,
			},
			wantName: "deployment.environment.name", wantOK: true,
		},
		{
			name: "priority: nested resource.* new-semconv beats flattened",
			caps: map[string]traceFieldCap{
				"resource.deployment.environment.name": kw,
				"deployment.environment.name":          kw,
				"env":                                   kw,
			},
			wantName: "resource.deployment.environment.name", wantOK: true,
		},
		{
			name: "new semconv spelling beats legacy at equal nesting",
			caps: map[string]traceFieldCap{
				"deployment.environment.name": kw,
				"deployment.environment":      kw,
			},
			wantName: "deployment.environment.name", wantOK: true,
		},
		{
			name: "text-only higher candidate falls through to keyword lower one",
			caps: map[string]traceFieldCap{
				"deployment.environment.name": txt, // no .keyword — skip
				"environment":                 kw,
			},
			wantName: "environment", wantOK: true,
		},
		{
			name: "bare custom shapes resolve last",
			caps: map[string]traceFieldCap{"env": kw},
			wantName: "env", wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, ok := resolveEnvFieldFromCaps(cands, tc.caps)
			if name != tc.wantName || ok != tc.wantOK {
				t.Fatalf("resolveEnvFieldFromCaps = (%q, %v), want (%q, %v)",
					name, ok, tc.wantName, tc.wantOK)
			}
		})
	}
}

func TestEnvFieldCapsFields_IncludesKeywordVariants(t *testing.T) {
	fields := envFieldCapsFields([]string{"a", "b.c"})
	want := []string{"a", "a.keyword", "b.c", "b.c.keyword"}
	if len(fields) != len(want) {
		t.Fatalf("got %v, want %v", fields, want)
	}
	for i := range want {
		if fields[i] != want[i] {
			t.Fatalf("got %v, want %v", fields, want)
		}
	}
}

func TestBuildQuery_EnvTermShape(t *testing.T) {
	s := &ESStore{}
	s.cfg.defaults()
	s.fields = s.cfg.Fields

	// Resolved field → both mapping shapes, exists-guarded bare term
	// (exactTermsBothShapes — service/cluster filter parity).
	raw, err := json.Marshal(s.buildQuery(Filter{
		Env: "uat", envField: "deployment.environment.name",
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	q := string(raw)
	for _, want := range []string{
		`"deployment.environment.name.keyword":"uat"`, // dynamic text mapping
		`"deployment.environment.name":"uat"`,         // ECS keyword-typed (guarded)
		`"must_not":[{"exists":{"field":"deployment.environment.name.keyword"}}]`,
		`"minimum_should_match":1`,
	} {
		if !strings.Contains(q, want) {
			t.Errorf("missing %s in: %s", want, q)
		}
	}

	// Env requested but NO resolved field → NO env clause at all (the
	// backend reports Page.EnvUnapplied instead of silently matching
	// nothing).
	raw, err = json.Marshal(s.buildQuery(Filter{Env: "uat"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "uat") {
		t.Errorf("unresolved env field must emit NO clause; got: %s", raw)
	}
}
