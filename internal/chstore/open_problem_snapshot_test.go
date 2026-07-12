package chstore

import "testing"

// open_problem_snapshot_test.go — v0.8.520 regression. Evaluator'ın
// nokta FindOpenProblem sorguları tick başına tek snapshot'a indi;
// map indirgeme semantiği FindOpenProblem'ın ORDER BY started_at DESC
// LIMIT 1'iyle birebir olmalı.
func TestReduceLatestProblem(t *testing.T) {
	m := map[string]*Problem{}
	reduceLatestProblem(m, &Problem{ID: "a", RuleID: "r1", Service: "s1", StartedAt: 100})
	reduceLatestProblem(m, &Problem{ID: "b", RuleID: "r1", Service: "s1", StartedAt: 200})
	reduceLatestProblem(m, &Problem{ID: "c", RuleID: "r1", Service: "s1", StartedAt: 150})
	reduceLatestProblem(m, &Problem{ID: "d", RuleID: "r1", Service: "s2", StartedAt: 50})

	if got := m[OpenProblemKey("r1", "s1")]; got == nil || got.ID != "b" {
		t.Fatalf("en yeni started_at kazanmalıydı, got=%+v", got)
	}
	if got := m[OpenProblemKey("r1", "s2")]; got == nil || got.ID != "d" {
		t.Fatalf("farklı servis ayrı anahtar, got=%+v", got)
	}
	if len(m) != 2 {
		t.Fatalf("2 anahtar bekleniyordu, got=%d", len(m))
	}
	// Eşit started_at: mevcut kazanır (>= karşılaştırması) — davranış sabit.
	reduceLatestProblem(m, &Problem{ID: "e", RuleID: "r1", Service: "s1", StartedAt: 200})
	if m[OpenProblemKey("r1", "s1")].ID != "b" {
		t.Fatal("eşit damgada ilk gelen korunmalı")
	}
}
