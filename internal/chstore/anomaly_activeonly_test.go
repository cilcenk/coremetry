package chstore

import (
	"strings"
	"testing"
)

// anomaly_activeonly_test.go — v0.9.335.
//
// The inbox's "open" pivot keeps ONLY active anomaly events and dropped the
// rest in Go, after the LIMIT. That is the same silent-scope shape already
// fixed for problems and incidents in v0.9.322: a caller asking for N rows to
// find the firing ones spends its whole budget on cleared history, and on a
// deep table the active ones fall off the end entirely.
//
// It was the FOURTH and last inbox source still narrowing post-cap, and the
// reason the wide 2000-row candidate scan was doing real work rather than
// being a safety margin.
//
// "Active" adds no new notion: it is exactly the freshness predicate the
// status column is already derived from. The change moves it to where the
// LIMIT can respect it.
func TestAnomalyEventsActiveOnlyNarrowsInSQL(t *testing.T) {
	src := mustReadSource(t, "anomaly_event.go")

	if !strings.Contains(src, "ActiveOnly bool") {
		t.Error("ListAnomalyEventsFilter needs an ActiveOnly cut — otherwise every caller filters after the LIMIT")
	}
	if !strings.Contains(src, `activeSQL = " AND last_seen >= now64() - INTERVAL ? SECOND"`) {
		t.Error("the active narrow must be a WHERE conjunct, not a post-scan Go filter")
	}
	// The predicate has to match the status derivation exactly, or a row can
	// be returned as 'cleared' by a query that filtered for active.
	if !strings.Contains(src, "if(last_seen >= now64() - INTERVAL ? SECOND, 'active', 'cleared')") {
		t.Error("status derivation changed — the ActiveOnly predicate must be kept identical to it")
	}
}

// Placeholder order is positional in ClickHouse, and this query now has a
// conditional one in the middle. Getting it wrong binds the since-timestamp
// as a duration (or vice versa) and silently returns the wrong window — the
// class of bug that does not error, it just answers differently.
func TestAnomalyEventsPlaceholderOrder(t *testing.T) {
	src := mustReadSource(t, "anomaly_event.go")
	i := strings.Index(src, "args := []any{int64(f.ActiveAge.Seconds()), since}")
	if i < 0 {
		t.Fatal("arg assembly changed — re-verify the placeholder order by hand")
	}
	tail := src[i:]
	// ActiveAge (SELECT) → since (WHERE) → ActiveAge (conditional) → Limit.
	wantOrder := []string{
		"args := []any{int64(f.ActiveAge.Seconds()), since}",
		"args = append(args, int64(f.ActiveAge.Seconds()))",
		"args = append(args, f.Limit)",
	}
	pos := -1
	for _, w := range wantOrder {
		p := strings.Index(tail, w)
		if p <= pos {
			t.Fatalf("argument order broken around %q", w)
		}
		pos = p
	}
}
