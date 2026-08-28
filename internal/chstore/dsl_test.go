package chstore

import (
	"reflect"
	"testing"
)

// TestSplitDSLAnd guards the inline " AND " split that recovers
// callers (notably the SpanDetail aggregate panel pre-v0.4.70) who
// build multi-predicate DSLs on one line. Without this split the
// regex captures the second predicate as a quoted value of the
// first; the bug surfaced as "0 spans" in the UI.
func TestSplitDSLAnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "single predicate untouched",
			in:   `service.name = "api"`,
			want: []string{`service.name = "api"`},
		},
		{
			// The split consumes both the leading and trailing space
			// around " AND " — callers don't have to TrimSpace afterwards.
			name: "two predicates split",
			in:   `service.name = "api" AND name = "GET /healthz"`,
			want: []string{`service.name = "api"`, `name = "GET /healthz"`},
		},
		{
			name: "case-insensitive AND",
			in:   `service.name = "api" and name = "x"`,
			want: []string{`service.name = "api"`, `name = "x"`},
		},
		{
			name: "AND inside a quoted value is not a separator",
			in:   `message = "AND is a word" AND service.name = "api"`,
			want: []string{`message = "AND is a word"`, `service.name = "api"`},
		},
		{
			name: "escaped quote does not unbalance",
			in:   `message = "say \"and\" loud" AND service = "x"`,
			want: []string{`message = "say \"and\" loud"`, `service = "x"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := splitDSLAnd(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("splitDSLAnd(%q)\n got  %#v\n want %#v", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseDSL_MultiPredicateOneLine is the integration-level check
// — the regression that motivated splitDSLAnd. Prior to the fix
// this parsed to one FilterExpr with a mis-quoted value; the
// returned slice now contains both predicates.
func TestParseDSL_MultiPredicateOneLine(t *testing.T) {
	got, err := ParseDSL(`service.name = "api" AND name = "GET /healthz"`)
	if err != nil {
		t.Fatalf("ParseDSL: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 predicates, got %d: %#v", len(got), got)
	}
	if got[0].Key != "service.name" || got[0].Values[0] != "api" {
		t.Errorf("first predicate wrong: %#v", got[0])
	}
	if got[1].Key != "name" || got[1].Values[0] != "GET /healthz" {
		t.Errorf("second predicate wrong: %#v", got[1])
	}
}

// TestParseDSL_NewlineAndInline keeps both separator forms working
// together — callers can mix newlines with inline ANDs without
// surprise.
func TestParseDSL_NewlineAndInline(t *testing.T) {
	src := `service.name = "api"
name = "GET" AND duration > 500ms`
	got, err := ParseDSL(src)
	if err != nil {
		t.Fatalf("ParseDSL: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 predicates, got %d: %#v", len(got), got)
	}
	if got[2].Key != "duration_ms" {
		t.Errorf("duration alias not applied: %#v", got[2])
	}
}

// v0.10.139 — Pod.tsx'in ürettiği çoklu-cluster kapsamı: `cluster in ["a", "b"]`
// (küçük harf operatör, köşeli parantez, tırnaklı değerler). Büyük harf `IN`
// dslLineRe'ye uymaz — sözleşme burada pinlenir ki FE yazımı sessizce 400'e
// düşmesin (lokal e2e bunu yakaladı).
func TestDSLClusterInList(t *testing.T) {
	fs, err := ParseDSL(`resource.k8s.pod.name = "api-1" AND cluster in ["prod-eu-west", "fake-1"]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 || fs[1].Key != "cluster" || fs[1].Op != "IN" || len(fs[1].Values) != 2 || fs[1].Values[1] != "fake-1" {
		t.Fatalf("beklenmeyen çözüm: %+v", fs)
	}
	if _, err := ParseDSL(`cluster IN ["a"]`); err == nil {
		t.Fatal("büyük harf IN reddedilir (regex küçük harf); FE küçük harf yazmalı")
	}
}
