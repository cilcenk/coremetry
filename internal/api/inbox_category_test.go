package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.706 — inbox kategori: problem satırı zenginleştirmeden, diğer türler
// saf türetimden; ?cat= çipi tam sözlükte süzmez, alt kümede süzer.
func TestInboxDerivedCategoryAndFacet(t *testing.T) {
	cases := []struct {
		name string
		it   InboxItem
		want string
	}{
		{"exception", InboxItem{Kind: "exception"}, chstore.CategoryError},
		{"httperror", InboxItem{Kind: "httperror"}, chstore.CategoryError},
		{"anomaly log pattern", InboxItem{Kind: "anomaly", Anomaly: &InboxAnomalyRef{Kind: "log_pattern", Pattern: "NPE"}}, chstore.CategoryError},
		{"anomaly trace latency", InboxItem{Kind: "anomaly", Anomaly: &InboxAnomalyRef{Kind: "trace_op", Pattern: "GET /x latency p99"}}, chstore.CategorySlowdown},
		{"anomaly nil ref", InboxItem{Kind: "anomaly"}, chstore.CategoryError},
		{"incident", InboxItem{Kind: "incident"}, chstore.CategoryCustom},
		{"problem fallback", InboxItem{Kind: "problem", Problem: &InboxProblemRef{RuleID: "anomaly:s:p99_ms", Metric: "p99_ms"}}, chstore.CategorySlowdown},
	}
	for _, c := range cases {
		if got := inboxDerivedCategory(c.it); got != c.want {
			t.Errorf("%s: %s, beklenen %s", c.name, got, c.want)
		}
	}
	items := []InboxItem{{ID: "a", Category: chstore.CategoryError}, {ID: "b", Category: chstore.CategorySlowdown}, {ID: "c", Category: chstore.CategoryCustom}}
	if got := applyInboxCategoryFacet(append([]InboxItem(nil), items...), chstore.ProblemCategories); len(got) != 3 {
		t.Fatal("tam sözlük süzmez")
	}
	got := applyInboxCategoryFacet(append([]InboxItem(nil), items...), []string{chstore.CategorySlowdown, chstore.CategoryCustom})
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "c" {
		t.Fatalf("alt küme: %+v", got)
	}
	// problemToInbox alanları taşır.
	p := chstore.Problem{ID: "p1", RuleID: "anomaly:s:error_rate", Metric: "error_rate", Category: chstore.CategoryError, DisplayID: "P-abc"}
	it := problemToInbox(p)
	if it.Category != chstore.CategoryError || it.DisplayID != "P-abc" {
		t.Fatalf("problemToInbox kategori/görüntü kimliği taşımalı: %+v", it)
	}
}
