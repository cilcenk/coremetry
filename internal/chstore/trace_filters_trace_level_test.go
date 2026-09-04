package chstore

import (
	"strings"
	"testing"
)

// trace_filters_trace_level_test.go — v0.10.341. Operator-reported (prod):
// servis + operasyon araması + HERHANGİ bir attribute çipi → "No traces
// found" (channel_code bir örnekti). Arama HAVING'de trace düzeyi, çip
// WHERE'de span düzeyiydi → aynı span ikisini de taşımalıydı; tablo ise
// attribute'u trace'in herhangi bir span'inden gösteriyordu. Çivilenen:
// arama varken çipler WHERE'den çıkar, HAVING'e countIf > 0 olarak girer;
// arama yokken WHERE'de kalır (indeks budaması).

func TestChipsMoveToTraceLevelHavingWithSearch(t *testing.T) {
	withPromoted(t, "channel_code", "attr_channel_code")
	chip := FilterExpr{Key: "channel_code", Op: "=", Values: []string{"060203"}}

	noSearch := TraceFilter{Service: "svc", Filters: []FilterExpr{chip}}
	if filtersTraceLevel(noSearch) {
		t.Fatal("arama yokken çip span düzeyi (WHERE) kalır")
	}
	wc := buildGetTracesWhere(noSearch, "")
	if !strings.Contains(wc.sql(), "attr_channel_code = ?") {
		t.Fatalf("arama yokken WHERE'de olmalı: %s", wc.sql())
	}

	withSearch := TraceFilter{Service: "svc", Search: "POST /x", Filters: []FilterExpr{chip}}
	if !filtersTraceLevel(withSearch) {
		t.Fatal("arama + çip → trace düzeyi")
	}
	wc = buildGetTracesWhere(withSearch, "")
	if strings.Contains(wc.sql(), "attr_channel_code") {
		t.Fatalf("arama varken çip WHERE'den ÇIKMALI: %s", wc.sql())
	}
	if !strings.Contains(wc.sql(), "service_name = ?") {
		t.Fatalf("servis daraltması WHERE'de kalır: %s", wc.sql())
	}
	parts, args := traceLevelFilterHaving(withSearch)
	if len(parts) != 1 || parts[0] != "countIf(attr_channel_code = ?) > 0" || len(args) != 1 || args[0] != "060203" {
		t.Fatalf("HAVING karşılığı: %v %v", parts, args)
	}

	// Span-düzeyi prob eski şekli ister.
	forced := withSearch
	forced.forceFiltersInWhere = true
	wc = buildGetTracesWhere(forced, "")
	if !strings.Contains(wc.sql(), "attr_channel_code = ?") {
		t.Fatalf("forceFiltersInWhere: %s", wc.sql())
	}

	// Birden çok çip → çip başına ayrı countIf (attribute'lar farklı span'de olabilir).
	two := TraceFilter{Search: "x", Filters: []FilterExpr{chip, {Key: "function_code", Op: "=", Values: []string{"MFY0001"}}}}
	parts, args = traceLevelFilterHaving(two)
	// function_code terfi değil: dizi yolu anahtar+değer (2 arg) → toplam 3.
	if len(parts) != 2 || len(args) != 3 || !strings.HasPrefix(parts[1], "countIf(") || !strings.HasSuffix(parts[1], ") > 0") {
		t.Fatalf("iki çip: %v %v", parts, args)
	}
	// NoPromoted saygılı: dizi yolu.
	two.NoPromoted = true
	parts, _ = traceLevelFilterHaving(two)
	if strings.Contains(parts[0], "attr_channel_code") || !strings.Contains(parts[0], "indexOf(attr_keys") {
		t.Fatalf("NoPromoted dizi yolu: %v", parts)
	}
}

func TestTraceLevelHavingForGroups(t *testing.T) {
	withPromoted(t, "channel_code", "attr_channel_code")
	// Düz AND kök → çip başına countIf.
	flat := TraceFilter{Search: "x", FilterRoot: &FilterGroup{Join: "AND", Filters: []FilterExpr{
		{Key: "channel_code", Op: "=", Values: []string{"a"}}, {Key: "kind", Op: "=", Values: []string{"server"}}}}}
	if !filtersTraceLevel(flat) {
		t.Fatal("kök grup + arama → trace düzeyi")
	}
	parts, _ := traceLevelFilterHaving(flat)
	if len(parts) != 2 {
		t.Fatalf("düz kök: %v", parts)
	}
	// OR kök → tek countIf(grup).
	or := TraceFilter{Search: "x", FilterRoot: &FilterGroup{Join: "OR", Filters: []FilterExpr{
		{Key: "channel_code", Op: "=", Values: []string{"a"}}, {Key: "channel_code", Op: "=", Values: []string{"b"}}}}}
	parts, args := traceLevelFilterHaving(or)
	if len(parts) != 1 || !strings.Contains(parts[0], " OR ") || len(args) != 2 {
		t.Fatalf("OR kök tek countIf: %v %v", parts, args)
	}
	// Yüklemsiz kök → trace düzeyi değil.
	if filtersTraceLevel(TraceFilter{Search: "x", FilterRoot: &FilterGroup{}}) {
		t.Fatal("boş kök çip sayılmaz")
	}
	sql := countMatchingTracesSQL("WHERE service_name = ?", " HAVING countIf(x) > 0")
	if !strings.HasPrefix(sql, "SELECT count() FROM (SELECT trace_id FROM spans WHERE") || !strings.Contains(sql, "GROUP BY trace_id HAVING countIf(x) > 0)") {
		t.Fatalf("trace sayım SQL: %s", sql)
	}
}

// v0.10.349 — Aggregated sekmesi: aynı tuzak (çip WHERE, arama iç HAVING).
func TestAggregateChipsMoveToInnerHavingWithSearch(t *testing.T) {
	withPromoted(t, "channel_code", "attr_channel_code")
	chip := FilterExpr{Key: "channel_code", Op: "=", Values: []string{"060203"}}
	lvl, having, args := aggregateChipHaving(AggregateFilter{Filters: []FilterExpr{chip}})
	if lvl || having != "" || args != nil {
		t.Fatalf("arama yokken WHERE kalır: %v %q %v", lvl, having, args)
	}
	lvl, having, args = aggregateChipHaving(AggregateFilter{Search: "POST /x", Filters: []FilterExpr{chip}})
	if !lvl || having != " AND countIf(attr_channel_code = ?) > 0" || len(args) != 1 || args[0] != "060203" {
		t.Fatalf("arama + çip → iç HAVING: %v %q %v", lvl, having, args)
	}
	lvl, having, _ = aggregateChipHaving(AggregateFilter{Search: "x", FilterRoot: &FilterGroup{Join: "OR", Filters: []FilterExpr{chip, {Key: "channel_code", Op: "=", Values: []string{"b"}}}}})
	if !lvl || !strings.Contains(having, " OR ") || strings.Count(having, "countIf(") != 1 {
		t.Fatalf("OR kök tek countIf: %q", having)
	}
}
