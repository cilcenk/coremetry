package chstore

import (
	"strings"
	"testing"
)

// filter_validate_test.go — v0.10.118. Validate, SQL() ile AYNI kararı
// verir: parse'ın kabul ettiği her yan tümce derlenir, reddettiği hiçbiri
// derlenmez. İki muhafız ayrışırsa v0.9.269 sınıfı (sessiz genişleme)
// geri gelir.
func TestFilterExprValidateMirrorsSQL(t *testing.T) {
	cases := []FilterExpr{
		{Key: "http.method", Op: "=", Values: []string{"GET"}},
		{Key: "http.method", Values: []string{"GET"}}, // boş op = "="
		{Key: "x", Op: "like", Values: []string{"%a%"}},
		{Key: "x", Op: "contains", Values: []string{"a"}},
		{Key: "x", Op: "startsWith", Values: []string{"a"}},
		{Key: "", Op: "=", Values: []string{"a"}},
		{Key: "x", Op: "EXISTS"},
		{Key: "x", Op: "between", Values: []string{"1", "2"}},
	}
	for _, f := range cases {
		verr := f.Validate()
		_, _, serr := f.SQL()
		if (verr == nil) != (serr == nil) {
			t.Errorf("%+v: Validate=%v SQL=%v — iki muhafız ayrıştı", f, verr, serr)
		}
	}
	if err := ValidateFilters([]FilterExpr{{Key: "a", Op: "="}, {Key: "b", Op: "nope"}}); err == nil || !strings.Contains(err.Error(), "filters[1]") {
		t.Errorf("liste hatası indeks taşımıyor: %v", err)
	}
	var g *FilterGroup
	if err := g.Validate(); err != nil {
		t.Error("nil grup geçerli olmalı")
	}
	g = &FilterGroup{Join: "OR", Groups: []FilterGroup{{Filters: []FilterExpr{{Key: "a", Op: "~"}}}}}
	if err := g.Validate(); err == nil || !strings.Contains(err.Error(), "groups[0]") {
		t.Errorf("alt grup hatası: %v", err)
	}
}
