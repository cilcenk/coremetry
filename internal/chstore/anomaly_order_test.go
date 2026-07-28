package chstore

import (
	"strings"
	"testing"
)

// anomaly_order_test.go — v0.9.326.
//
// ListAnomalyEvents ordered by `status DESC`, where status is the string
// literal produced by
//
//	if(last_seen >= now64() - INTERVAL ? SECOND, 'active', 'cleared')
//
// ClickHouse compares those lexically, and 'active' < 'cleared'. DESC
// therefore returns CLEARED FIRST — the exact inverse of what every caller
// wants. Verified against a live server:
//
//	SELECT arrayReverseSort(['active','cleared'])  →  ['cleared','active']
//
// The consequence is silent and scales with history. The LIMIT fills with
// cleared rows, so on a busy install the active ones fall off the end: the
// inbox (which keeps ONLY active rows) shows none, while
// CountActiveAnomalyEvents — a SQL count with no ordering — still reports
// them. Badge says N, list says nothing, no error anywhere. Local at the time
// of the fix: 181 cleared vs 10 active in 24h, against a default LIMIT of
// 200. One more day of history and the active rows disappear.
//
// Callers that were all reading the wrong end: /api/anomalies (Limit 2000),
// the alert evaluator, the root-cause worker (Limit = batch), fusion
// (Limit 500), the MCP tools, and the merged triage queue.
func TestAnomalyEventsOrderActiveFirst(t *testing.T) {
	src := mustReadSource(t, "anomaly_event.go")

	if strings.Contains(src, "ORDER BY status DESC") {
		t.Error("ORDER BY status DESC returns cleared before active ('active' < 'cleared' lexically) — the LIMIT then hides the firing events")
	}
	if !strings.Contains(src, "ORDER BY status = 'active' DESC, last_seen DESC") {
		t.Error("order active first, newest within — written as a boolean so the lexical trap cannot return")
	}
}

// The trap itself, pinned in Go so the reasoning survives even if someone
// rewrites the SQL: sorting these two literals descending puts the WRONG one
// first, and that is not obvious by reading `status DESC`.
func TestAnomalyStatusLiteralsOrderIsATrap(t *testing.T) {
	if !("active" < "cleared") {
		t.Fatal("assumption broken — the whole v0.9.326 reasoning rests on this")
	}
	// i.e. descending puts "cleared" first. Anyone reading `ORDER BY status
	// DESC` and expecting "active, then cleared" is reading it backwards.
}
