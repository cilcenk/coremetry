package chstore

import (
	"strings"
	"testing"
)

// trace_facets_test.go — v0.10.302: kolon adı türetimi, doğrulama (çakışma,
// sınırlar, yazım), yerleşik listeyle birleşme, prod SQL şekli/sırası.

func TestFacetColumn(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"tenant", "attr_f_tenant"},
		{"Tenant.ID", "attr_f_tenant_id"},
		{"  banking.txn_ref ", "attr_f_banking_txn_ref"},
		{"a--b__c", "attr_f_a_b_c"},
		{"___", ""},
		{strings.Repeat("k", 100), "attr_f_" + strings.Repeat("k", facetColMaxLen-len("attr_f_"))},
	} {
		if got := FacetColumn(tc.in); got != tc.want {
			t.Errorf("FacetColumn(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeTraceFacets(t *testing.T) {
	ok, err := NormalizeTraceFacets(TraceFacetSettings{Facets: []TraceFacet{
		{Key: " tenant ", Spellings: []string{"TENANT_ID", "tenant", ""}},
		{Key: "region", Scope: "resource", Type: "string"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ok.Facets) != 2 || ok.Facets[0].Key != "tenant" || strings.Join(ok.Facets[0].Spellings, ",") != "tenant,TENANT_ID" ||
		ok.Facets[0].Scope != "span" || ok.Facets[0].Type != "lc" || ok.Facets[1].Scope != "resource" || ok.Facets[1].Type != "string" {
		t.Errorf("normalize: %+v", ok.Facets)
	}
	bad := []TraceFacetSettings{
		{Facets: []TraceFacet{{Key: ""}}},
		{Facets: []TraceFacet{{Key: "resource.x"}}},
		{Facets: []TraceFacet{{Key: "x", Scope: "pod"}}},
		{Facets: []TraceFacet{{Key: "x", Type: "int"}}},
		{Facets: []TraceFacet{{Key: "x", Spellings: []string{"a'b"}}}},
		{Facets: []TraceFacet{{Key: "x"}, {Key: "X"}}},          // aynı anahtar
		{Facets: []TraceFacet{{Key: "a.b"}, {Key: "a-b"}}},      // aynı kolon
		{Facets: []TraceFacet{{Key: "channel_code"}}},           // yerleşik terfi
		{Facets: []TraceFacet{{Key: "service.name"}}},           // bilinen kolon
		{Facets: []TraceFacet{{Key: strings.Repeat("k", 200)}}}, // uzun
	}
	for i, in := range bad {
		if _, err := NormalizeTraceFacets(in); err == nil {
			t.Errorf("vaka %d reddedilmeliydi: %+v", i, in.Facets)
		}
	}
	many := TraceFacetSettings{}
	for i := 0; i < traceFacetsMax+1; i++ {
		many.Facets = append(many.Facets, TraceFacet{Key: "k" + strings.Repeat("x", i)})
	}
	if _, err := NormalizeTraceFacets(many); err == nil {
		t.Error("tavan aşımı reddedilmeli")
	}
}

func TestFacetsMergeIntoPromotedList(t *testing.T) {
	registerTraceFacets(TraceFacetSettings{Facets: []TraceFacet{{Key: "tenant", Spellings: []string{"tenant", "TENANT_ID"}}}})
	t.Cleanup(func() { registerTraceFacets(TraceFacetSettings{}) })
	all := allPromotedAttrs()
	if len(all) != len(promotedAttrs)+1 {
		t.Fatalf("birleşik liste %d; yerleşik %d + 1", len(all), len(promotedAttrs))
	}
	last := all[len(all)-1]
	if last.col != "attr_f_tenant" || strings.Join(last.keys, ",") != "tenant,TENANT_ID" || last.res || last.colType() != "LowCardinality(String)" {
		t.Errorf("facet → promotedAttr: %+v", last)
	}
	// Aynı makine: DDL ifadesi iki yazımı da okur, set(0) indeks.
	stmts := promotedAttrDDL(last, "", false, false)
	if len(stmts) != 2 || !strings.Contains(stmts[0], "attr_f_tenant LowCardinality(String) MATERIALIZED coalesce(nullIf(attr_values[indexOf(attr_keys, 'tenant')], ''), nullIf(attr_values[indexOf(attr_keys, 'TENANT_ID')], ''), '')") || !strings.Contains(stmts[1], "idx_attr_f_tenant attr_f_tenant TYPE set(0)") {
		t.Errorf("DDL: %v", stmts)
	}
	cur := CurrentTraceFacets()
	if len(cur) != 1 || cur[0].Key != "tenant" || cur[0].Scope != "span" {
		t.Errorf("CurrentTraceFacets: %+v", cur)
	}
	// Resolve: kolon kayıtlı değilken dizi ifadesi, iki yazım.
	expr, args, ok := promotedAttrResolve("TENANT_ID")
	if !ok || !strings.Contains(expr, "coalesce(") || len(args) != 2 {
		t.Errorf("resolve: %v %s %v", ok, expr, args)
	}
}

func TestTraceFacetMigrationSQL(t *testing.T) {
	cfg, _ := NormalizeTraceFacets(TraceFacetSettings{Facets: []TraceFacet{{Key: "tenant"}, {Key: "region", Scope: "resource", Type: "string"}}})
	sql := TraceFacetMigrationSQL(cfg)
	for _, want := range []string{
		"ALTER TABLE spans_local ON CLUSTER uptrace_all\n  ADD COLUMN IF NOT EXISTS attr_f_tenant LowCardinality(String)\n  MATERIALIZED coalesce(nullIf(attr_values[indexOf(attr_keys, 'tenant')], ''), '');",
		"ALTER TABLE spans ON CLUSTER uptrace_all\n  ADD COLUMN IF NOT EXISTS attr_f_tenant LowCardinality(String)",
		"ADD INDEX IF NOT EXISTS idx_attr_f_tenant attr_f_tenant TYPE set(0) GRANULARITY 4;",
		"attr_f_region String\n  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'region')], ''), '');",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("%q yok:\n%s", want, sql)
		}
	}
	l, w, i := strings.Index(sql, "ALTER TABLE spans_local ON CLUSTER uptrace_all\n  ADD COLUMN IF NOT EXISTS attr_f_tenant"), strings.Index(sql, "ALTER TABLE spans ON CLUSTER uptrace_all\n  ADD COLUMN IF NOT EXISTS attr_f_tenant"), strings.Index(sql, "idx_attr_f_tenant")
	if !(l < w && w < i) {
		t.Errorf("sıra local → wrapper → index: %d %d %d", l, w, i)
	}
	if !strings.Contains(TraceFacetMigrationSQL(TraceFacetSettings{}), "(facet yok)") {
		t.Error("boş kayıt notu")
	}
}
